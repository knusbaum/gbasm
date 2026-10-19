package main

import (
	"crypto/sha256"
	"fmt"
	"io"
	"reflect"

	"github.com/knusbaum/gbasm/cmd/bosc/flow"
)

const unionPayloadField = "__union_payload"

// unionLayout is an anonymous values declaration. Its synthetic name is only
// compiler-local; exported signatures serialize the structural union type.
func unionLayout(c *Context, t ASTType) (*ValuesDecl, ASTType) {
	name := fmt.Sprintf("__union_%x", sha256.Sum256([]byte(unionContextNode(c, t, false))))
	if old, ok := c.ValuesDeclForName(name); ok {
		view := ASTType{Name: name}
		if t.HasOwned() {
			view.OwnedMask = 1
		}
		return old, view
	}
	vd := &ValuesDecl{Name: name, TagType: numASTType(), p: t.p}
	for i, a := range unionAlternatives(c, t) {
		vd.Cases = append(vd.Cases, ValuesCase{Name: a.String(), Tag: int64(i), Payload: []Binding{{Name: unionPayloadField, Type: a}}, p: t.p})
	}
	c.DefineValuesType(t.p, name, vd)
	view := ASTType{Name: name}
	if t.HasOwned() {
		view.OwnedMask = 1
	}
	return vd, view
}

// unionView changes only compiler type metadata, never storage or permissions.
// Only internally synthesized payload selectors can expose this ADT view.
type unionView struct {
	Val  AST
	Type ASTType
}

func (v *unionView) ASTType(*Context) ASTType { return v.Type }
func (v *unionView) Pos() position            { return v.Val.Pos() }
func (v *unionView) Note() string             { return "union storage" }

type unionInjection struct {
	ReturnContext bool // preserve the return checker's no-ownership-drop rule
	Val           AST
	Type          ASTType
}

func (v *unionInjection) ASTType(*Context) ASTType { return v.Type }
func (v *unionInjection) Pos() position            { return v.Val.Pos() }
func (v *unionInjection) Note() string             { return "union injection" }

// selectUnionAlternative prefers exact identity. Conversions are considered
// only when there is no exact match, and must identify a unique alternative.
func selectUnionAlternative(c *Context, at AST, dst, src ASTType) int {
	for i, a := range unionAlternatives(c, dst) {
		if unionContextNode(c, a, false) == unionContextNode(c, src, false) {
			return i
		}
	}
	if src.IsUnion() {
		CompileErrorF(at, "Cannot implicitly widen or convert union %s to %s", src, dst)
	}
	selected := -1
	for i, a := range unionAlternatives(c, dst) {
		if unionAlternativeAccepts(c, a, src) {
			if selected >= 0 {
				CompileErrorF(at, "Ambiguous union injection of %s into %s; use an explicit conversion or typed binding", src, dst)
			}
			selected = i
		}
	}
	if selected < 0 {
		CompileErrorF(at, "No alternative of %s accepts %s", dst, src)
	}
	return selected
}

func compileUnionInjection(of io.Writer, c *Context, v *unionInjection, dest spot) spot {
	index := selectUnionAlternative(c, v, v.Type, v.Val.ASTType(c))
	vd, view := unionLayout(c, v.Type)
	selected := vd.Cases[index].Payload[0].Type
	if v.ReturnContext && v.Val.ASTType(c).HasOwned() && !selected.HasOwned() {
		CompileErrorF(v, "Cannot return %s as non-owned union alternative %s", v.Val.ASTType(c), selected)
	}
	if dest.empty() {
		dest = newSpot(of, c, c.Temp(), v.Type)
	}
	target := dest
	target.t = view
	con := &ValuesConstruct{FromUnion: true, ValuesType: view, Decl: vd, Case: &vd.Cases[index], Fields: []StructField{{Name: unionPayloadField, Val: v.Val}}, p: v.Pos()}
	compileValuesConstruct(of, c, con, target)
	return dest
}

func unionCaseType(c *Context, cs *VariantCase) ASTType {
	if cs.Type != nil {
		return *cs.Type
	}
	var name func(AST) string
	name = func(a AST) string {
		switch n := a.(type) {
		case *Symbol:
			return n.Name
		case *Dot:
			if p := name(n.Val); p != "" {
				return p + "." + n.Member
			}
		}
		return ""
	}
	spelling := name(cs.Label)
	if spelling == "" {
		spelling = cs.Name
	}
	if spelling == "" {
		panic(&interpreterError{msg: "Union switch cases require exact alternative types", p: cs.p})
	}
	if scope := c.sourceScope(); scope != nil {
		var err error
		spelling, err = scope.typeName(spelling)
		if err != nil {
			panic(&interpreterError{msg: err.Error(), p: cs.p})
		}
	}
	return ASTType{Name: spelling, p: cs.p}
}

func lowerUnionSwitch(c *Context, s *VariantSwitch) *VariantSwitch {
	t := s.Scrutinee.ASTType(c)
	vd, view := unionLayout(c, t)
	if s.Default != nil {
		CompileErrorF(s, "Union switches do not support default; name each alternative explicitly")
	}
	root, ok := s.Scrutinee.(*Symbol)
	if !ok {
		CompileErrorF(s, "Union switches require a binding; use switch (v := expression)")
	}
	out := *s
	out.UnionType = &t
	out.Scrutinee = &unionView{Val: s.Scrutinee, Type: view}
	out.Cases = nil
	seen := map[int]bool{}
	for _, cs := range s.Cases {
		ct := unionCaseType(c, cs)
		index := -1
		for i, a := range unionAlternatives(c, t) {
			if unionContextNode(c, a, false) == unionContextNode(c, ct, false) {
				index = i
				break
			}
		}
		if index < 0 {
			panic(&interpreterError{msg: fmt.Sprintf("%s is not an exact alternative of union %s", ct, t), p: cs.p})
		}
		if seen[index] {
			panic(&interpreterError{msg: fmt.Sprintf("Duplicate union switch case %s", ct), p: cs.p})
		}
		seen[index] = true
		body := rewriteUnionPayload(cs.Body, root.Name, view)
		out.Cases = append(out.Cases, &VariantCase{Name: vd.Cases[index].Name, Body: body, p: cs.p})
	}
	s.Exhaustive = len(out.Cases) == len(t.Union)
	out.Exhaustive = s.Exhaustive
	return &out
}

// The existing ADT switch owns branch flow and payload tracking. Translate
// references to the narrowed binding into its single payload field, respecting
// lexical shadowing. Type metadata and declaration names are never rewritten.
func rewriteUnionPayload(a AST, name string, view ASTType) AST {
	if a == nil {
		return nil
	}
	if d, ok := a.(*Dispose); ok && d.Var == name {
		return &unionDispose{Val: &Dot{Val: &unionView{Val: &Symbol{Name: name, p: d.p}, Type: view}, Member: unionPayloadField}, p: d.p}
	}
	if loop, ok := a.(*For); ok {
		if decl, ok := loop.Init.(*VarDecl); ok && decl.Name == name {
			out := *loop
			out.Init = rewriteUnionPayload(loop.Init, name, view)
			return &out
		}
	}
	if ts, ok := a.(*TypeSwitch); ok && ts.BindName == name {
		out := *ts
		out.Val = rewriteUnionPayload(ts.Val, name, view)
		return &out
	}
	if sym, ok := a.(*Symbol); ok && sym.Name == name {
		return &Dot{Val: &unionView{Val: sym, Type: view}, Member: unionPayloadField}
	}
	if address, ok := a.(*Address); ok && address.Var == name {
		return &Address{Lit: &Dot{Val: &unionView{Val: &Symbol{Name: name, p: address.p}, Type: view}, Member: unionPayloadField}, p: address.p}
	}
	if b, ok := a.(*Block); ok {
		out := *b
		out.Body = nil
		shadowed := false
		for _, st := range b.Body {
			if shadowed {
				out.Body = append(out.Body, st)
			} else {
				out.Body = append(out.Body, rewriteUnionPayload(st, name, view))
			}
			if d, ok := st.(*VarDecl); ok && d.Name == name {
				shadowed = true
			}
			if d, ok := st.(*MultiBindDecl); ok {
				for _, v := range d.Bindings {
					if v.Name == name {
						shadowed = true
					}
				}
			}
		}
		return &out
	}
	if b, ok := a.(*BoundSwitch); ok && b.Decl.Name == name {
		out := *b
		decl := *b.Decl
		decl.Init = rewriteUnionPayload(decl.Init, name, view)
		out.Decl = &decl
		return &out
	}
	return rewriteUnionASTFields(a, func(child AST) AST { return rewriteUnionPayload(child, name, view) })
}

func rewriteUnionASTFields(a AST, rewrite func(AST) AST) AST {
	astInterface := reflect.TypeOf((*AST)(nil)).Elem()
	astType := reflect.TypeOf(ASTType{})
	var visit func(reflect.Value) reflect.Value
	visit = func(v reflect.Value) reflect.Value {
		if !v.IsValid() {
			return v
		}
		if v.Type() == astType {
			return v
		}
		if v.Kind() == reflect.Interface && v.Type().Implements(astInterface) {
			if v.IsNil() {
				return v
			}
			return reflect.ValueOf(rewrite(v.Interface().(AST)))
		}
		if v.Kind() == reflect.Ptr {
			if v.IsNil() {
				return v
			}
			if v.Type().Implements(astInterface) {
				return reflect.ValueOf(rewrite(v.Interface().(AST)))
			}
			if v.Elem().Kind() == reflect.Struct {
				out := reflect.New(v.Elem().Type())
				out.Elem().Set(visit(v.Elem()))
				return out
			}
			return v
		}
		if v.Kind() == reflect.Slice {
			if v.IsNil() {
				return v
			}
			out := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
			for i := 0; i < v.Len(); i++ {
				out.Index(i).Set(visit(v.Index(i)))
			}
			return out
		}
		if v.Kind() == reflect.Struct {
			out := reflect.New(v.Type()).Elem()
			out.Set(v)
			for i := 0; i < v.NumField(); i++ {
				if v.Type().Field(i).PkgPath == "" {
					out.Field(i).Set(visit(v.Field(i)))
				}
			}
			return out
		}
		return v
	}
	in := reflect.ValueOf(a)
	out := reflect.New(in.Elem().Type())
	out.Elem().Set(visit(in.Elem()))
	return out.Interface().(AST)
}

// Ensure the compiler never accidentally treats the structural union as an
// unnamed struct, an integer, or a borrowed interface value.
func unionContains(t ASTType) bool {
	if t.IsUnion() {
		return true
	}
	if t.Element != nil && unionContains(*t.Element) {
		return true
	}
	for _, f := range t.AnonFields {
		if unionContains(f.Type) {
			return true
		}
	}
	return false
}

// unionDispose applies dispose to the selected payload, not to a differently
// typed wrapper whose inactive alternatives might happen to own resources.
type unionDispose struct {
	Val AST
	p   position
}

func (*unionDispose) ASTType(*Context) ASTType { return voidASTType() }
func (d *unionDispose) Pos() position          { return d.p }
func (*unionDispose) Note() string             { return "dispose union payload" }

// Keep payload provenance distinct from the union container's own storage.
// Consuming the container must not invalidate an unrelated borrowed resource.
func recordUnionPayloadProvenance(c *Context, name string, t ASTType, source AST) {
	if injection, ok := source.(*unionInjection); ok {
		payload := unwrapReturnExpr(injection.Val)
		payloadType := c.ResolveUnderlying(payload.ASTType(c))
		if _, aggregate := structDeclForType(c, payloadType); aggregate && payloadType.Indirection == 0 {
			if sym, ok := payload.(*Symbol); ok {
				c.PointerFlow().CopyFieldPointersUnderPath(sym.Name, VarFlowPath(name).Append(unionPayloadField).Key())
				return
			}
			if literal, ok := payload.(*StructLiteral); ok {
				recordStructLiteralFieldFacts(c, VarFlowPath(name).Append(unionPayloadField), literal)
				return
			}
		}
	}
	if sym, ok := source.(*Symbol); ok {
		c.PointerFlow().CopyFieldPointers(flow.Binding(sym.Name), flow.Binding(name))
		return
	}
	ptr := pointerExprForAST(c, source, name)
	if ptr.KnownOrigin || ptr.KnownSlot {
		c.PointerFlow().SetPathPointer(VarFlowPath(name).Append(unionPayloadField).Key(), ptr)
	}
}

// Interface coercion eligibility is structural satisfaction, not merely the
// fact that a destination is an interface. Unrelated interfaces must not turn
// a unique injection into an ambiguity.
func unionAlternativeAccepts(c *Context, dst, src ASTType) bool {
	if c.IsInterfaceType(dst) {
		if src.Name == "<nil>" {
			return dst.NilMask&1 != 0
		}
		if src.Same(intlitASTType()) || src.Same(floatlitASTType()) {
			return false
		}
		target, _ := c.InterfaceForName(dst.Name)
		if c.IsInterfaceType(src) {
			source, _ := c.InterfaceForName(src.Name)
			return src.NilMask&^dst.NilMask == 0 && (!dst.HasOwned() || src.HasOwned()) && interfaceWidensTo(c, source, target)
		}
		if dst.HasOwned() && !src.HasOwned() {
			return false
		}
		if src.Indirection == 0 && (src.IsSliceOrArray() || src.FuncSig != nil || src.Size(c) > PTR_SIZE) {
			return false
		}
		levels, err := shapeStackFor(src)
		if err != nil {
			return false
		}
		return c.TypeSatisfiesInterfaceAs(shapeBaseName(src), target, expectedReceiverShape(levels))
	}
	if src.Same(intlitASTType()) {
		t := c.ResolveUnderlying(dst)
		if t.Indirection > 0 || t.IsSliceOrArray() || t.AnonFields != nil || t.FuncSig != nil || !isBuiltinTypeName(t.Name) {
			return false
		}
	}
	_, reason := coerceType(c, dst, src)
	return reason == coerceOK
}

// A declared union provides the result context for a switch expression.
// Inferred switches still require their arms to agree; they do not invent
// union types merely because two arms yield different payload types.
func prepareUnionExpression(c *Context, value AST, target ASTType, returning bool) AST {
	if value == nil || !target.IsUnion() {
		return value
	}
	switch s := value.(type) {
	case *VariantSwitch:
		s.ResultType = &target
		s.ReturnContext = returning
		return value
	case *BoundSwitch:
		s.Switch.ResultType = &target
		s.Switch.ReturnContext = returning
		return value
	}
	if sameUnionInContext(c, value.ASTType(c), target) {
		return value
	}
	return &unionInjection{Val: value, Type: target, ReturnContext: returning}
}
