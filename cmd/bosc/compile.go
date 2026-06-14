package main

import (
	"fmt"
	"io"
	"math/big"
	"strings"

	"github.com/knusbaum/gbasm/cmd/bosc/flow"
)

var annotate bool = true

func note(of io.Writer, f string, args ...any) {
	if !annotate {
		return
	}
	fmt.Fprintf(of, f, args...)
}

func CompileErrorF(a AST, f string, args ...any) {
	panic(&interpreterError{
		msg: fmt.Sprintf(f, args...),
		p:   a.Pos(),
	})
}

// reportCoerceFailure picks the diagnostic for a non-OK coerceType result.
// The closed-symbolic-set rejection has one fixed wording across every
// coercion site; the generic Accepts-style mismatch keeps whatever
// site-specific wording the caller provides. Callers pass the
// user-visible dst and src for the values message (typically the
// declared, pre-ResolveUnderlying types).
//
// Both branches end in CompileErrorF, which panics with an
// interpreterError. The first branch does not return on the values
// path, so the fall-through to the generic CompileErrorF is unreachable
// when reason == coerceValuesFromIntLit.
func reportCoerceFailure(a AST, displayDst, displaySrc ASTType, reason coerceReason, generic string, args ...any) {
	if reason == coerceValuesFromIntLit {
		CompileErrorF(a, "Cannot construct %s from %s: values cases must be constructed from declared cases", displayDst, displaySrc)
	}
	CompileErrorF(a, generic, args...)
}

func Compile(of io.Writer, c *Context, a AST) (e error) {
	defer func() {
		if err := recover(); err != nil {
			if le, ok := err.(*interpreterError); ok {
				a = nil
				e = le
				return
			}
			panic(err)
		}
	}()
	compileTop(of, c, a, nullspot)
	return nil
}

var nullspot = spot{}
var regtype = ASTType{Name: "*untyped-register*"}

// spot describes a bas-level name produced by codegen. ref is the
// name as it'll appear in emitted assembly; t is the Boson type; and
// nameIsAddress records whether bas resolves the name to a memory
// location (address-style: `bytes`-allocated stack chunks, file-
// scope globals) or to a register holding the value (value-style:
// `local`-allocated scalars). Sites that need to follow a pointer
// — Deref, register-scaled Index/SliceOp — consult nameIsAddress
// to decide whether they need an extra load to materialize the
// value into a register first.
type spot struct {
	ref           string
	t             ASTType
	nameIsAddress bool
}

func newSpot(of io.Writer, c *Context, ref string, t ASTType) spot {
	sz := t.Size(c)
	memBacked := typeIsMemoryBacked(c, t)
	s := spot{ref: ref, t: t, nameIsAddress: memBacked}
	if memBacked {
		fmt.Fprintf(of, "\tbytes %s %d\n", ref, sz)
	} else {
		fmt.Fprintf(of, "\tlocal %s %d\n", ref, sz*8)
	}
	return s
}

func newSpotWithReg(of io.Writer, c *Context, ref string, t ASTType, reg string) spot {
	sz := t.Size(c)
	memBacked := typeIsMemoryBacked(c, t)
	s := spot{ref: ref, t: t, nameIsAddress: memBacked}
	if memBacked {
		fmt.Fprintf(of, "\tbytes %s %d %s\n", ref, sz, reg)
	} else {
		fmt.Fprintf(of, "\tlocal %s %d %s\n", ref, sz*8, reg)
	}
	return s
}

func regSpot(of io.Writer, name string) spot {
	fmt.Fprintf(of, "\tacquire %s\n", name)
	return spot{ref: name, t: regtype}
}

func (s *spot) free(of io.Writer) {
	if s.empty() {
		return
	}
	if s.t.Same(regtype) {
		// if this is a regtype spot, release it.
		fmt.Fprintf(of, "\trelease %s\n", s.ref)
	} else if strings.HasPrefix(s.ref, temp_prefix) {
		// otherwise, if this is a temporary local or bytes, forget it.
		// We only free temporaries, since variables should only me manually
		// released.
		fmt.Fprintf(of, "\tforget %s\n", s.ref)
	}
	s.ref = ""
}

func (s *spot) same(s2 *spot) bool {
	return s.ref == s2.ref
}

func (s *spot) empty() bool {
	return s.ref == ""
}

// compileArrayLiteralInto lays out an `[e1, e2, …]` initializer directly
// into the destination spot's storage. The destination must be a fixed
// array `T[N]` (slice destinations would need lifetime-stable backing
// storage that bosc can't generally synthesize at runtime — those are
// supported only in static-init context via the encoder).
//
// Each element is compiled to a temporary, then stored into the
// destination at `[dest.ref + i*elemSize]`. For scalar elements that's
// a single mov; for memory-backed elements (struct values, multi-byte
// arrays), we lea the element's address and memcpy into place.
func compileArrayLiteralInto(of io.Writer, c *Context, a AST, dest spot, lit *ArrayLiteral) {
	if !dest.t.IsArray() {
		if dest.t.IsSlice() {
			CompileErrorF(a, "array literal as slice initializer is only valid at file scope; use a fixed array (T[N]) here")
		}
		CompileErrorF(a, "array literal cannot initialize %s; expected a fixed array type", dest.t)
	}
	if dest.t.ArraySize != len(lit.Elements) {
		CompileErrorF(a, "array literal of length %d does not fit %s", len(lit.Elements), dest.t)
	}
	elemT := *dest.t.Element
	elemSize := elemT.Size(c)
	for i, e := range lit.Elements {
		// Capturing a whole inconsistent aggregate into an array element
		// forms an alias that could read its moved-out field.
		if name := aggregateBindingName(e); name != "" {
			checkAggregateNotAliasedWhileInconsistent(c, name, e)
		}
		// Every element flows into a slot of type elemT. Route through
		// coerceType so the closed-symbolic-set rule covers array
		// initializers — pre-unification this site had no check at all,
		// so `var cs color[2] = [0, color.GREEN]` happily compiled and
		// constructed a tag-0 color without going through color.RED.
		if _, reason := coerceType(c, elemT, e.ASTType(c)); reason != coerceOK {
			reportCoerceFailure(e, elemT, e.ASTType(c), reason,
				"Array element %d: expected type %v but got %v", i, elemT, e.ASTType(c))
		}
		off := i * elemSize
		if typeIsMemoryBacked(c, elemT) {
			// Multi-word element (struct value, fixed array, etc.):
			// compile the element to its own bytes-allocated spot,
			// then memcpy into the destination slot.
			elemTmp := newSpot(of, c, c.Temp(), elemT)
			val := compileTop(of, c, e, elemTmp)
			addrType := elemT
			addrType.Indirection++
			slotAddr := newSpot(of, c, c.Temp(), addrType)
			fmt.Fprintf(of, "\tlea %s [%s+%d]\n", slotAddr.ref, dest.ref, off)
			spot_memcpy(of, c, slotAddr, val, elemSize)
			slotAddr.free(of)
			val.free(of)
		} else {
			// Scalar element: compute and store directly. Source comes
			// back at its own width (often 64-bit for integer literals)
			// so use the partial-of-alloc syntax `val.ref:bits` to
			// select the low N bits matching the element type's width.
			val := compileTop(of, c, e, nullspot)
			elemBits := elemSize * 8
			fmt.Fprintf(of, "\tmov [%s+%d] %s:%d\n", dest.ref, off, val.ref, elemBits)
			val.free(of)
		}
	}
}

func spot_memcpy(of io.Writer, c *Context, dst, src spot, bytes int) {
	qwords := bytes / 8
	singles := bytes % 8

	// 8-byte scratch local for the qword copies.
	tmp := newSpot(of, c, c.Temp(), ASTType{Name: "i64"})
	for i := 0; i < qwords; i++ {
		fmt.Fprintf(of, "\tmov %s [%s+%d]\n", tmp.ref, src.ref, i*8)
		fmt.Fprintf(of, "\tmov [%s+%d] %s\n", dst.ref, i*8, tmp.ref)
	}
	tmp.free(of)
	if singles > 0 {
		tmp = newSpot(of, c, c.Temp(), ASTType{Name: "byte"})
		for i := 0; i < singles; i++ {
			fmt.Fprintf(of, "\tmovb %s [%s+%d]\n", tmp.ref, src.ref, qwords*8+i)
			fmt.Fprintf(of, "\tmovb [%s+%d] %s\n", dst.ref, qwords*8+i, tmp.ref)
		}
		tmp.free(of)
	}
}

func sameIgnoringOwned(a, b ASTType) bool {
	return a.StripOwned().Same(b.StripOwned())
}

func validateInterfaceCoercion(c *Context, errNode AST, dstt, srct ASTType) (valueBacked bool) {
	// Base type at the bottom of the shape's constructor stack. For composite
	// sources (`*byte[]`, `byte[][]`) srct.Name is empty; shapeBaseName walks
	// to the named leaf so satisfaction is checked against a real type's
	// method set (and the typedesc symbol later resolves).
	concreteTypeName := shapeBaseName(srct)
	// Interface borrow-contract gate (the ⊆ ceiling rule) is folded into
	// TypeSatisfiesInterfaceAs below: for each method the target interface
	// *requires*, the concrete method's inferred ReturnAliases[slot] must be
	// ⊆ the interface's declared set. A borrowing method the interface does
	// not require imposes no static constraint — it is reachable only via
	// runtime assertion, which _iface.assert_to's per-slot mask check gates.
	ifaceDecl, _ := c.InterfaceForName(dstt.Name)
	// The coercion direction is determined by the source, not by the
	// interface's declared receiver shape. A pointer source produces a
	// pointer-backed interface (data slot = pointer); a non-pointer
	// source produces a value-backed interface (data slot = value bits).
	// Dispatch is uniform: `mov rdi, [iface+0]` — so T's methods must
	// match the source direction, not the interface's syntactic shape.
	pointerDirection := srct.Indirection > 0
	// Shape-word filter: derive the expected receiver shape from the source's
	// full shape word (distinguishing *T / *mut T / **T / *byte[]) instead of
	// the binary pointerDirection. The compile-time filter and the runtime
	// helper (_iface.assert_to) use the same derivation so the two agree.
	expectedRecv := recvShapeNone
	if levels, serr := shapeStackFor(srct); serr == nil {
		expectedRecv = expectedReceiverShape(levels)
	}
	if !c.TypeSatisfiesInterfaceAs(concreteTypeName, ifaceDecl, expectedRecv) {
		CompileErrorF(errNode, "%s", interfaceSatisfactionError(c, concreteTypeName, dstt.Name, ifaceDecl, expectedRecv))
	}
	if dstt.OwnedMask != 0 && srct.OwnedMask == 0 {
		CompileErrorF(errNode, "Cannot use non-owned %s where owned %s is required", srct, dstt.Name)
	}
	if pointerDirection {
		return false
	}
	if srct.IsSliceOrArray() || srct.FuncSig != nil {
		CompileErrorF(errNode, "Cannot convert value of type %s to interface %s; use a pointer instead", srct, dstt.Name)
	}
	size := srct.Size(c)
	if size > PTR_SIZE {
		CompileErrorF(errNode, "Cannot convert value of type %s to interface %s: value is %d bytes; value interfaces can store at most 8 bytes inline. Use a pointer instead.", srct, dstt.Name, size)
	}
	return true
}

func shouldCoerceToInterface(c *Context, dstt, srct ASTType) bool {
	if !c.IsInterfaceType(dstt) {
		return false
	}
	if c.IsInterfaceType(srct) {
		// interface → interface: a same-type assignment is a plain 16-byte copy
		// (handled elsewhere). Distinct interface types route through the
		// widening emitter, which performs the conversion when it is a valid
		// *widening* (dst's contract covered by src) and emits a directed "use
		// an explicit `x.(D)` assertion" error for a narrowing.
		return !srct.Same(dstt)
	}
	return srct.Indirection > 0 || !srct.Same(intlitASTType())
}

func typedescSymbolName(typeName string) string {
	return "__typedesc_" + strings.ReplaceAll(typeName, ".", "_")
}

func emitTypedesc(of io.Writer, typeName string) {
	fmt.Fprintf(of, "data %s byte[1] \"\\0\"\n", typedescSymbolName(typeName))
}

// emitTypedescFor emits the structured typedesc + cache pair for a named base
// type declared in the current (home) package. The method table is the type's
// full method set; the size is sizeof(T).
func emitTypedescFor(of io.Writer, c *Context, isPub bool, typeName string) {
	ownerPkg := c.Pkgname()
	methods := methodEntriesForType(c, typeName, ownerPkg)
	sz := sizeOfNamedType(c, typeName)
	emitTypedescStructured(of, isPub, typeName, sz, methods)
}

// sizeOfNamedType returns sizeof of a named base type as it would appear by
// value (used for the typedesc's size_bytes field; informational only).
func sizeOfNamedType(c *Context, typeName string) (sz int) {
	defer func() {
		if recover() != nil {
			sz = 0
		}
	}()
	t := ASTType{Name: typeName}
	return t.Size(c)
}

// builtinScalarTypes is the set of primitive base types whose typedescs are
// emitted (once) by the builtin package.
var builtinScalarTypes = []string{
	"i8", "i16", "i32", "i64",
	"u8", "u16", "u32", "u64",
	"byte", "bool",
}

// isBuiltinScalarType reports whether name is a primitive scalar type whose
// typedesc lives in the builtin package.
func isBuiltinScalarType(name string) bool {
	for _, s := range builtinScalarTypes {
		if s == name {
			return true
		}
	}
	return false
}

// emitBuiltinScalarTypedescs emits the single shared typedesc for each
// primitive base type. Called only when compiling the builtin package.
func emitBuiltinScalarTypedescs(of io.Writer) {
	for _, s := range builtinScalarTypes {
		// Primitive scalars have no methods. Their size is fixed by name.
		emitTypedescStructured(of, true, s, scalarSize(s), nil)
	}
}

// scalarSize returns the byte size of a primitive scalar by name.
func scalarSize(name string) int {
	switch name {
	case "i64", "u64":
		return 8
	case "i32", "u32":
		return 4
	case "i16", "u16":
		return 2
	case "i8", "u8", "byte", "bool":
		return 1
	}
	return 0
}

// typedescOwnerPkg returns the package that emits the typedesc for the named
// base type. Built-in scalars are owned by "builtin"; everything else is owned
// by the package implied by the (possibly qualified) type name, defaulting to
// the current package.
func typedescOwnerPkg(c *Context, bareBase string) string {
	if isBuiltinScalarType(bareBase) {
		return "builtin"
	}
	return c.Pkgname()
}

// typedescSymbolForBase returns the fully-qualified typedesc symbol for a
// (possibly package-qualified) base type name. A name like "geom.Point"
// resolves to "geom.__typedesc_Point" (home package = geom); a bare name
// resolves through the current package, with built-in scalars owned by
// "builtin".
func typedescSymbolForBase(c *Context, baseName string) string {
	pkg := c.Pkgname()
	bare := baseName
	if dot := strings.LastIndex(baseName, "."); dot >= 0 {
		pkg = baseName[:dot]
		bare = baseName[dot+1:]
	} else if isBuiltinScalarType(baseName) {
		pkg = "builtin"
	}
	return fmt.Sprintf("%s.%s", pkg, typedescSymbolName(bare))
}

// expectedReceiverDesc renders a human-readable receiver type for the source's
// receiver capability: a value source expects a bare-T receiver; a `*T` source
// expects `*T`; a `*mut T` source accepts either `*T` or `*mut T` (the legal
// `*mut -> *` weakening); an unsupported source has no compatible receiver.
func expectedReceiverDesc(expectedRecv int, typeName string) string {
	switch expectedRecv {
	case recvShapeValue:
		return typeName
	case recvShapePtr:
		return "*" + typeName
	case recvShapeMutPtr:
		return "*" + typeName + " or *mut " + typeName
	default:
		return "(no compatible receiver)"
	}
}

func interfaceSatisfactionError(c *Context, typeName, ifaceName string, iface *InterfaceDecl, expectedRecv int) string {
	methods, ok := c.TypeMethodsFor(typeName)
	if !ok {
		return fmt.Sprintf("Type %s does not implement interface %s", typeName, ifaceName)
	}
	for _, isig := range iface.Methods {
		for _, m := range methods {
			if m.Name != isig.Name {
				continue
			}
			if len(m.Args) == 0 || len(isig.Params) == 0 {
				return fmt.Sprintf("Type %s does not implement interface %s: method %s has no receiver", typeName, ifaceName, isig.Name)
			}
			if !receiverSatisfies(receiverShapeOf(m.Args[0].Type), expectedRecv) {
				return fmt.Sprintf("Type %s does not implement interface %s: method %s receiver has type %s, expected %s", typeName, ifaceName, isig.Name, m.Args[0].Type, expectedReceiverDesc(expectedRecv, typeName))
			}
			if len(m.Args) != len(isig.Params) {
				return fmt.Sprintf("Type %s does not implement interface %s: method %s has %d parameters, expected %d", typeName, ifaceName, isig.Name, len(m.Args), len(isig.Params))
			}
			for i := 1; i < len(isig.Params); i++ {
				if !m.Args[i].Type.Same(isig.Params[i].Type) {
					return fmt.Sprintf("Type %s does not implement interface %s: method %s parameter %d has type %s, expected %s", typeName, ifaceName, isig.Name, i, m.Args[i].Type, isig.Params[i].Type)
				}
			}
			if !m.Return.Same(isig.Return) {
				return fmt.Sprintf("Type %s does not implement interface %s: method %s returns %s, expected %s", typeName, ifaceName, isig.Name, m.Return, isig.Return)
			}
			if !methodAliasesSatisfy(c, m, isig.ReturnAliases) {
				return interfaceBorrowMismatchError(c, typeName, ifaceName, m, isig)
			}
		}
		return fmt.Sprintf("Type %s does not implement interface %s", typeName, ifaceName)
	}
	return fmt.Sprintf("Type %s does not implement interface %s", typeName, ifaceName)
}

// interfaceBorrowMismatchError is the directed diagnostic for a method that
// structurally matches but whose inferred return borrow exceeds the interface
// method's declared `from(...)` ceiling (the ⊆ conformance failure). The first
// line carries the whole story (err-test comparison keeps only that line).
func interfaceBorrowMismatchError(c *Context, typeName, ifaceName string, m *FuncDecl, isig InterfaceMethodSig) string {
	declares := "no such borrow"
	if isig.ReturnAliases != nil {
		declares = "a narrower borrow"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Type %s does not implement interface %s: method %s %s, but %s declares %s",
		typeName, ifaceName, bareMethodName(m.Name), borrowDescription(m), ifaceName, declares)
	fmt.Fprintf(&b, "\n  method: %s", methodSignatureRendering(c, typeName, m))
	if site := methodDefinitionSite(typeName, m); site != "" {
		fmt.Fprintf(&b, "\n  %s", site)
	}
	fmt.Fprintf(&b, "\n  fix: declare a matching `from(...)` clause on %s's %s, or return a value that outlives the borrowed source.", ifaceName, isig.Name)
	return b.String()
}

// multiReturnReturnsClause formats the "<name> returns (T1, T2)" fragment
// used in the single-bind-rejection diagnostic. Falls back to a bare
// "returns (...)" form when the initializer isn't a recognizable call.
func multiReturnReturnsClause(init AST, t ASTType) string {
	var types []string
	for _, f := range t.AnonFields {
		types = append(types, f.Type.String())
	}
	tuple := "(" + strings.Join(types, ", ") + ")"
	if fc, ok := init.(*Funcall); ok {
		if name := fc.QualifiedName(); name != "" {
			return fmt.Sprintf("%s returns %s.", name, tuple)
		}
	}
	return fmt.Sprintf("expression returns %s.", tuple)
}

// multiReturnDestructureTemplate builds a copy-pasteable "var a T1, var b T2 = ..."
// template using the actual return types from a MultiReturn ASTType.
func multiReturnDestructureTemplate(t ASTType) string {
	names := []string{"a", "b", "c", "d", "e", "f"}
	var parts []string
	for i, f := range t.AnonFields {
		var n string
		if i < len(names) {
			n = names[i]
		} else {
			n = fmt.Sprintf("v%d", i)
		}
		parts = append(parts, fmt.Sprintf("var %s %s", n, f.Type.String()))
	}
	return strings.Join(parts, ", ") + " = ..."
}

// fillAnonymousLiteralIfNeeded fills in the type of a bare struct literal
// (one whose Type has Name=="" and AnonFields==nil) from the expected context
// type. Call this at every site where a StructLiteral may appear with an
// unresolved anonymous type: return, var init, assignment, and call arguments.
func fillAnonymousLiteralIfNeeded(sl *StructLiteral, expectedType ASTType) {
	if sl.Type.Name == "" && sl.Type.AnonFields == nil {
		sl.Type = expectedType
	}
}

func fallsThrough(a AST) bool {
	switch ast := a.(type) {
	case *Break, *Continue, *Return:
		return false
	case *Block:
		for _, st := range ast.Body {
			if !fallsThrough(st) {
				return false
			}
		}
		return true
	case *IfStmt:
		return ast.Else == nil || fallsThrough(ast.Then) || fallsThrough(ast.Else)
	case *Loop:
		return true
	default:
		return true
	}
}

func nullablePointerPathForIf(c *Context, cond AST) (FlowPath, ASTType, bool, bool) {
	nonNullOnThen := true
	var val AST
	switch ast := cond.(type) {
	case *Symbol, *Dot:
		val = ast
	case *Not:
		val = ast.Val
		nonNullOnThen = false
	case *Op2:
		if ast.Type != n_neq && ast.Type != n_deq {
			return FlowPath{}, ASTType{}, false, false
		}
		if lit, ok := ast.Second.(*Literal); ok && lit.Val == nil {
			val = ast.First
		} else if lit, ok := ast.First.(*Literal); ok && lit.Val == nil {
			val = ast.Second
		} else {
			return FlowPath{}, ASTType{}, false, false
		}
		if ast.Type == n_deq {
			nonNullOnThen = false
		}
	default:
		return FlowPath{}, ASTType{}, false, false
	}
	path, ok := FlowPathForExpr(val)
	if !ok {
		return FlowPath{}, ASTType{}, false, false
	}
	t, ok := declaredPathType(c, val)
	if !ok {
		return FlowPath{}, ASTType{}, false, false
	}
	if t.NilMask&1 == 0 {
		return FlowPath{}, ASTType{}, false, false
	}
	// A nullable pointer (`*?`) or a nullable interface (`?T`, Indirection 0)
	// can be narrowed by an `if (x != nil)` / `if (x == nil)` test.
	if t.Indirection == 0 && !c.IsInterfaceType(t) {
		return FlowPath{}, ASTType{}, false, false
	}
	return path, t, nonNullOnThen, true
}

// declaredPathType walks a (possibly multi-level) Symbol/Dot expression
// and returns the declared (un-narrowed) type for the leaf, so that
// flow-narrowing decisions are not skipped just because an outer pass
// already narrowed the value through ASTType.
func declaredPathType(c *Context, a AST) (ASTType, bool) {
	switch v := a.(type) {
	case *Symbol:
		return c.DeclaredTypeForVar(v.Name)
	case *Dot:
		baseType, ok := declaredPathType(c, v.Val)
		if !ok {
			return ASTType{}, false
		}
		if baseType.Indirection != 0 {
			if baseType.NilMask&1 != 0 {
				return ASTType{}, false
			}
		}
		decl, ok := structDeclForType(c, baseType)
		if !ok {
			return ASTType{}, false
		}
		for _, f := range decl.Fields {
			if f.Name == v.Member {
				return fieldTypeForBase(baseType, f.Type), true
			}
		}
		return ASTType{}, false
	case *NonNullAssert:
		t, ok := declaredPathType(c, v.Val)
		if !ok {
			return ASTType{}, false
		}
		t.NilMask &^= 1
		return t, true
	default:
		return ASTType{}, false
	}
}

func updateNullFactForAssignment(c *Context, path FlowPath, dst ASTType, val AST, src ASTType) {
	if dst.Indirection == 0 {
		c.SetNullFact(path, NullMaybe)
		return
	}
	if dst.NilMask&1 == 0 {
		c.SetNullFact(path, NullKnownNonNull)
		return
	}
	if lit, ok := val.(*Literal); ok && lit.Val == nil {
		c.SetNullFact(path, NullKnownNull)
		return
	}
	if src.Indirection > 0 && src.NilMask&1 == 0 {
		c.SetNullFact(path, NullKnownNonNull)
		return
	}
	c.SetNullFact(path, NullMaybe)
}

func pointeeType(t ASTType) ASTType {
	t.Indirection--
	t.MutMask >>= 1
	t.OwnedMask >>= 1
	t.NilMask >>= 1
	if t.Indirection == 0 && !t.IsSliceOrArray() {
		t.MutMask = 0
	}
	return t
}

func checkOwnedFieldsConsumedBeforeRawFree(c *Context, a AST, path FlowPath, ptrType ASTType) {
	pointee := pointeeType(ptrType)
	def, ok := c.StructDeclForName(pointee.Name)
	if !ok {
		return
	}
	for _, field := range def.Fields {
		fieldType := fieldTypeForBase(pointee, field.Type)
		if !fieldType.HasOwned() {
			continue
		}
		fieldPath := path.Append(field.Name)
		if !c.OwnedFieldConsumed(fieldPath) {
			CompileErrorF(a, "free(%s) would leak owned field %s", path.Key(), fieldPath.Key())
		}
	}
}

func mergeOwnedFieldFactsExact(c *Context, a AST, thenFacts, elseFacts map[FlowPath]bool) map[FlowPath]bool {
	out := make(map[FlowPath]bool)
	seen := make(map[FlowPath]bool)
	for path := range thenFacts {
		seen[path] = true
	}
	for path := range elseFacts {
		seen[path] = true
	}
	for path := range seen {
		thenConsumed := thenFacts[path]
		elseConsumed := elseFacts[path]
		if thenConsumed != elseConsumed {
			CompileErrorF(a, "Owned field \"%s\" is consumed on one branch but not the other", path.Key())
		}
		if thenConsumed {
			out[path] = true
		}
	}
	return out
}

// mergeFlowSnapshots joins two flow snapshots reached on different exits of
// the same loop. Owned-binding consistency is checked separately by the
// caller; this helper merges the remaining facts lossily where appropriate
// (null and pointer state can disagree across exits, owned-field facts must
// agree exactly).
func mergeFlowSnapshots(c *Context, a AST, x, y FlowSnapshot) FlowSnapshot {
	return FlowSnapshot{
		Owned:       x.Owned,
		Null:        MergeNullFacts(x.Null, y.Null),
		OwnedFields: mergeOwnedFieldFactsExact(c, a, x.OwnedFields, y.OwnedFields),
		Borrowed:    MergeBorrowedBindings(x.Borrowed, y.Borrowed),
		Pointer:     flow.Merge(x.Pointer, y.Pointer),
	}
}

func checkOwnedSourceAvailable(c *Context, val AST) {
	if nn, ok := val.(*NonNullAssert); ok {
		checkOwnedSourceAvailable(c, nn.Val)
		return
	}
	if op, ok := val.(*OwnedPromotion); ok {
		checkOwnedSourceAvailable(c, op.Val)
		return
	}
	switch v := val.(type) {
	case *Symbol:
		if c.IsMoved(v.Name) {
			CompileErrorF(val, "Cannot move \"%s\": it was already moved", v.Name)
		}
	case *Address:
		if v.Var != "" && c.IsMoved(v.Var) {
			CompileErrorF(val, "Cannot move \"%s\": it was already moved", v.Var)
		}
	case *Dot:
		baseType := v.Val.ASTType(c)
		fieldType := v.ASTType(c)
		if parentOwnsFields(baseType) && fieldType.HasOwned() {
			if fieldType.Indirection == 0 || fieldType.OwnedMask&1 == 0 {
				// Value-typed owned fields have no pointer identity to hand
				// off and no placeholder to leave behind, so they can only
				// be consumed by disposing the whole aggregate.
				CompileErrorF(val, "Cannot move owned field %s out of an owned aggregate", v.Member)
			}
			// Moving a non-null owned field out leaves its slot zeroed with
			// no nil check on the field's type. A live alias of the
			// aggregate could read that slot and crash, so refuse to
			// partially-consume an aliased aggregate. (Nullable fields are
			// exempt: the alias would read nil and must narrow.)
			if fieldType.NilMask&1 == 0 {
				if base, ok := v.Val.(*Symbol); ok && c.HasLiveAlias(base.Name) {
					CompileErrorF(val, "Cannot move owned field %s out of \"%s\" while it is aliased; the alias could read the moved-out field", v.Member, base.Name)
				}
			}
			// A non-null owned *pointer* field MAY be moved out. Doing so
			// zeroes the slot (see the *Dot case in markMovedIfOwnedSource)
			// and marks the field consumed, leaving the aggregate in an
			// inconsistent state that the escape gate
			// (checkAggregateMayEscape) prevents from crossing a scope
			// boundary. Nullable fields already worked this way with nil as
			// the placeholder.
			if path, ok := FlowPathForExpr(val); ok && c.OwnedFieldConsumed(path) {
				CompileErrorF(val, "Cannot move \"%s\": it was already moved", path.Key())
			}
		}
	}
}

// inconsistentOwnedField reports the first non-null owned field of the
// aggregate rooted at `path` (declared type `t`) that has been moved out
// (consumed). Such an aggregate is "inconsistent": the moved-out field has no
// nil placeholder, and the local consumption fact cannot cross a scope
// boundary, so the aggregate must not escape (see checkAggregateMayEscape). A
// consumed *nullable* field does not count — nil is a safe, representable
// state that keeps the aggregate passable.
func inconsistentOwnedField(c *Context, path FlowPath, t ASTType) (string, bool) {
	pointee := t
	if t.Indirection > 0 {
		pointee = pointeeType(t)
	}
	def, ok := structDeclForType(c, pointee)
	if !ok {
		return "", false
	}
	for _, field := range def.Fields {
		fieldType := fieldTypeForBase(pointee, field.Type)
		if !fieldType.HasOwned() || fieldType.Indirection == 0 || fieldType.NilMask&1 != 0 {
			continue
		}
		if c.OwnedFieldConsumed(path.Append(field.Name)) {
			return field.Name, true
		}
	}
	return "", false
}

// checkAggregateMayEscape rejects letting an owned aggregate binding leave the
// current function scope (verb: "pass", "return", "move") while it has a
// moved-out non-null owned field. The receiving scope cannot see the local
// consistency facts, so it would observe a field that looks live but is
// zeroed. Only the shape-blind intrinsics dispose/free may consume such an
// aggregate, and those are lowered on separate paths that never reach here.
func checkAggregateMayEscape(c *Context, name string, errNode AST, verb string) {
	declared, ok := c.DeclaredTypeForVar(name)
	if !ok {
		return
	}
	if field, bad := inconsistentOwnedField(c, VarFlowPath(name), declared); bad {
		CompileErrorF(errNode,
			"Cannot %s \"%s\" while owned field \"%s.%s\" is moved out; only dispose/free are allowed on a partially-consumed aggregate — consume or re-initialize \"%s.%s\" first",
			verb, name, name, field, name, field)
	}
}

// checkAggregateNotAliasedWhileInconsistent rejects forming a new alias of an
// aggregate (taking `&f`, or copying a pointer-to-aggregate into another
// binding) while it has a moved-out non-null owned field. The alias would
// outlive the local consistency fact and could read the zeroed slot — the
// symmetric counterpart of the move-while-aliased check, covering an alias
// formed *after* the move rather than before it.
func checkAggregateNotAliasedWhileInconsistent(c *Context, name string, errNode AST) {
	declared, ok := c.DeclaredTypeForVar(name)
	if !ok {
		return
	}
	if field, bad := inconsistentOwnedField(c, VarFlowPath(name), declared); bad {
		CompileErrorF(errNode,
			"Cannot alias \"%s\" while owned field \"%s.%s\" is moved out; the alias could read the moved-out field",
			name, name, field)
	}
}

// checkedAssignPointer records dst as an alias of src in the pointer-flow
// state, first rejecting the alias if src's origin/slot is an inconsistent
// aggregate (one with a moved-out non-null owned field). Gating at the
// recording point covers any binding-alias that funnels through here —
// `var x = f`, `var x = &f` — without a per-syntax gate. It no-ops for
// non-aggregate sources (the inconsistency predicate returns false). Other
// alias forms that do not reach a clean AssignPointer (assignment `x = f`,
// struct/array-literal capture) keep their own syntactic gates.
func checkedAssignPointer(c *Context, dst flow.Binding, src flow.PointerExpr, errNode AST) {
	name := ""
	if src.KnownOrigin {
		name = string(src.Origin)
	} else if src.KnownSlot {
		name = string(src.SlotTarget)
	}
	if name != "" {
		checkAggregateNotAliasedWhileInconsistent(c, name, errNode)
	}
	c.PointerFlow().AssignPointer(dst, src)
}

// aggregateBindingName returns the binding name an escape-site expression
// refers to (the aggregate itself `f`, or `&f`), or "" if the expression is
// not a whole-aggregate reference.
func aggregateBindingName(arg AST) string {
	switch v := arg.(type) {
	case *Symbol:
		return v.Name
	case *Address:
		return v.Var
	}
	return ""
}

func borrowedPointerExpr(c *Context, a AST) bool {
	if a.ASTType(c).Indirection == 0 {
		return false
	}
	switch ast := a.(type) {
	case *Symbol:
		return c.IsBorrowedBinding(ast.Name)
	case *Dot:
		return borrowedPointerExpr(c, ast.Val)
	case *Deref:
		return borrowedPointerExpr(c, ast.Val)
	case *Index:
		return borrowedPointerExpr(c, ast.Val)
	case *NonNullAssert:
		return borrowedPointerExpr(c, ast.Val)
	default:
		return false
	}
}

func checkBorrowedPointerDoesNotEscape(c *Context, a AST, what string) {
	if borrowedPointerExpr(c, a) {
		CompileErrorF(a, "Borrowed pointer escapes via %s", what)
	}
}

func checkLocalOriginDoesNotEscape(c *Context, a AST, what string) {
	ptr := pointerExprForAST(c, a, "")
	if !ptr.KnownOrigin {
		return
	}
	if c.PointerFlow().OriginKindOf(ptr.Origin) == flow.OriginLocal {
		CompileErrorF(a, "Pointer to local variable %q escapes via %s", string(ptr.Origin), what)
	}
}

// checkSliceEscape catches a slice flowing into a slot whose lifetime
// outlives the slice's root. The escape-restricted predicate covers
// both stack-rooted (OriginLocal: `local[:]`, fields of local structs)
// and borrowed (OriginBorrowed: parameter views, sub-slices of params,
// aliases of either) — registered uniformly by param setup, SliceOp,
// and the VarDecl/Assignment alias propagation. The caller passes the
// full trailing phrase (e.g. "through return", "via global g_slice",
// "via field B.buf") since the preposition varies by site.
func checkSliceEscape(c *Context, a AST, what string) {
	ptr := pointerExprForAST(c, a, "")
	if !ptr.KnownOrigin {
		return
	}
	if c.PointerFlow().IsEscapeRestricted(ptr.Origin) {
		CompileErrorF(a, "Borrowed slice escapes %s", what)
	}
}

// checkSliceEscapeLocalOnly is the return-site variant of checkSliceEscape:
// it rejects only a slice rooted at OriginLocal (a local fixed array whose
// storage dies with the frame). A slice rooted at OriginBorrowed (a view
// of a parameter) is *not* rejected here — with inferred return-parameter
// aliasing the borrow is recordable and the lifetime obligation moves to
// the caller, where alias_set propagation enforces it. Used only at the
// return site; assignment-to-longer-lived-storage sites keep the full
// escape-restricted reject (checkSliceEscape), since the caller cannot
// vouch for those.
func checkSliceEscapeLocalOnly(c *Context, a AST, what string) {
	ptr := pointerExprForAST(c, a, "")
	if !ptr.KnownOrigin {
		return
	}
	if c.PointerFlow().OriginKindOf(ptr.Origin) == flow.OriginLocal {
		CompileErrorF(a, "Borrowed slice escapes %s", what)
	}
}

// lvalueLocalRoot walks an lvalue chain (Symbol, Dot, Index, NonNullAssert)
// to its root binding and returns the binding name if every step stays
// inside the same stack frame — no pointer auto-deref at a Dot, no
// indexing through a slice, no global root, no pointer-typed Symbol.
// Used by SliceOp provenance to decide whether `expr[:]` carries the
// root binding's local-storage origin.
func lvalueLocalRoot(c *Context, expr AST) (string, bool) {
	for {
		switch v := expr.(type) {
		case *Symbol:
			if c.IsGlobalBinding(v.Name) {
				return "", false
			}
			t, ok := c.TypeForVar(v.Name)
			if !ok {
				return "", false
			}
			if t.Indirection > 0 {
				return "", false
			}
			return v.Name, true
		case *Dot:
			// Auto-deref through a pointer-typed receiver breaks the
			// local-root chain (the pointee's storage is opaque).
			if v.Val.ASTType(c).Indirection > 0 {
				return "", false
			}
			expr = v.Val
		case *Index:
			// Indexing into a slice reads through the slice header's
			// data pointer — that storage isn't necessarily local.
			// Indexing into a fixed array stays within the receiver's
			// storage, so the walk continues.
			recvType := v.Val.ASTType(c)
			if !recvType.IsArray() {
				return "", false
			}
			expr = v.Val
		case *NonNullAssert:
			expr = v.Val
		default:
			return "", false
		}
	}
}

// recordStructLiteralFieldFacts walks a struct-literal initializer and
// records pointer/slice provenance facts at full FlowPath keys.
// Recurses into nested struct literals so
// `var b Outer = Outer{inner: Inner{buf: s}}` stores at "b.inner.buf".
//
// Critically per-field, not blanket: a struct literal only writes the
// fields it lists, so the fact model must match. `o.inner = Inner{}`
// (empty literal) clears nothing — codegen leaves the prior bytes in
// place, and the prior facts must stay too, or returning `o` becomes
// unsoundly accepted while the borrowed slice still sits in `o.inner.buf`.
func recordStructLiteralFieldFacts(c *Context, base FlowPath, lit *StructLiteral) {
	for _, f := range lit.Fields {
		if f.Val == nil {
			continue
		}
		fieldPath := base.Append(f.Name)
		// Clear stale facts at or under this field — codegen is about
		// to overwrite this field's bytes specifically.
		c.PointerFlow().ForgetFieldPointersUnder(fieldPath.Key())
		c.PointerFlow().SetPathPointer(fieldPath.Key(), c.PointerFlow().UnknownPointer())
		if nested, ok := f.Val.(*StructLiteral); ok {
			recordStructLiteralFieldFacts(c, fieldPath, nested)
			continue
		}
		ft := f.Val.ASTType(c)
		if ft.Indirection > 0 || ft.IsSlice() {
			c.PointerFlow().SetPathPointer(fieldPath.Key(), pointerExprForAST(c, f.Val, ""))
			continue
		}
		// Struct-typed field sourced from a non-literal: the field value
		// carries its borrows in ITS fields, which must flow into this
		// field's path or they vanish (`Outer{inner: mk(p)}` / `Outer{inner:
		// b}` previously recorded nothing for inner, silently dropping the
		// borrow — and with it both the live local-escape reject and the
		// summary's param alias).
		if _, isStruct := structDeclForType(c, ft); isStruct {
			switch v := f.Val.(type) {
			case *Symbol:
				if !c.IsGlobalBinding(v.Name) {
					c.PointerFlow().CopyFieldPointersUnderPath(v.Name, fieldPath.Key())
				}
			case *Funcall:
				recordStructCallResultAtPath(c, fieldPath.Key(), ft, v)
			}
		}
	}
}

// seedStructParamFieldProvenance records, for a by-value struct parameter,
// that each of its slice/pointer fields is a borrowed view of caller storage.
// A by-value struct param is copied into the callee's frame, but the
// slice/pointer fields inside it still point at the caller's backing data — so
// returning the struct (or any of its fields) aliases the parameter. The
// tracker previously modeled nothing for struct params (only pointer and slice
// params were seeded), which left a returned struct param's borrow invisible.
//
// Every seeded field origin is a borrowed origin rooted at the parameter name,
// so a return-value provenance query maps it back to this parameter. Recurses
// into struct-typed fields ("b.inner.buf"). Owned obligations are out of scope:
// the caller gates this on a non-owned param, and fieldTypeForBase strips owned
// from the (borrowed) fields, so only borrowed views are seeded.
func seedStructParamFieldProvenance(c *Context, paramName string, base FlowPath, t ASTType) {
	sd, ok := structDeclForType(c, t)
	if !ok {
		return
	}
	for _, f := range sd.Fields {
		ft := fieldTypeForBase(t, f.Type)
		fieldPath := base.Append(f.Name)
		if ft.Indirection > 0 || ft.IsSlice() {
			c.PointerFlow().SetPathPointer(fieldPath.Key(),
				c.PointerFlow().NewBorrowedOrigin(flow.Binding(paramName)))
		} else if _, isStruct := structDeclForType(c, ft); isStruct {
			seedStructParamFieldProvenance(c, paramName, fieldPath, ft)
		}
	}
}

// walkLeafType walks a dotted Fields suffix (the form returned by
// CheckStructFieldEscape) starting from rootType and returns the leaf
// type. Recognizes "." separators between struct fields and "[]" steps
// for array/slice element traversal. Used by the return-by-value
// diagnostic to choose between "pointer to" and "slice into" wording
// when the escaping field lives several hops below the binding's type.
func walkLeafType(c *Context, rootType ASTType, fields string) (ASTType, bool) {
	cur := rootType
	for _, step := range strings.Split(fields, ".") {
		if step == "[]" {
			if !cur.IsArray() && !cur.IsSlice() {
				return ASTType{}, false
			}
			cur = cur.ElementType()
			continue
		}
		decl, ok := structDeclForType(c, cur)
		if !ok {
			return ASTType{}, false
		}
		found := false
		for _, fd := range decl.Fields {
			if fd.Name == step {
				cur = fd.Type
				found = true
				break
			}
		}
		if !found {
			return ASTType{}, false
		}
	}
	return cur, true
}

// readProvenancePath resolves a Dot/Index/NonNullAssert chain to its
// stored provenance fact via ProvenancePathForExpr. Returns
// UnknownPointer when the chain doesn't reach a binding-rooted path,
// the root is global, or the root is a pointer-typed local (in which
// case the path crosses an opaque pointee).
func readProvenancePath(c *Context, a AST) flow.PointerExpr {
	path, ok := ProvenancePathForExpr(a)
	if !ok || path.Fields == "" {
		return c.PointerFlow().UnknownPointer()
	}
	if c.IsGlobalBinding(path.Root) {
		return c.PointerFlow().UnknownPointer()
	}
	if t, ok := c.TypeForVar(path.Root); !ok || t.Indirection > 0 {
		return c.PointerFlow().UnknownPointer()
	}
	return c.PointerFlow().GetPathPointer(path.Key())
}

// rootSymbolName walks a Dot/Index/NonNullAssert chain to the rooted Symbol
// and returns its name. Returns ("", false) for any other shape.
func rootSymbolName(expr AST) (string, bool) {
	for {
		switch v := expr.(type) {
		case *Symbol:
			return v.Name, true
		case *Dot:
			expr = v.Val
		case *Index:
			expr = v.Val
		case *NonNullAssert:
			expr = v.Val
		default:
			return "", false
		}
	}
}

// isDirectStructFieldTarget returns (binding, field, true) if target is a
// single-level field access on a local non-pointer struct binding.
func isDirectStructFieldTarget(c *Context, target AST) (binding string, field string, ok bool) {
	dot, isDot := target.(*Dot)
	if !isDot {
		return "", "", false
	}
	sym, isSym := dot.Val.(*Symbol)
	if !isSym || c.IsGlobalBinding(sym.Name) {
		return "", "", false
	}
	t, exists := c.TypeForVar(sym.Name)
	if !exists || t.Indirection != 0 {
		return "", "", false
	}
	return sym.Name, dot.Member, true
}

func updateFieldPointerFactsForAssignment(c *Context, target AST, targetIsSymbol bool, targetSym *Symbol, dstt ASTType, val AST) {
	if targetIsSymbol && dstt.Indirection == 0 {
		c.PointerFlow().ForgetFieldPointers(flow.Binding(targetSym.Name))
		if sym, ok := val.(*Symbol); ok && !c.IsGlobalBinding(sym.Name) {
			c.PointerFlow().CopyFieldPointers(flow.Binding(sym.Name), flow.Binding(targetSym.Name))
		} else if sl, ok := val.(*StructLiteral); ok {
			recordStructLiteralFieldFacts(c, VarFlowPath(targetSym.Name), sl)
		} else if _, ok := val.(*Funcall); ok {
			// A struct returned by value FROM A CALL (`b = mk(arg)`): record
			// its borrowed-argument provenance onto the destination's field
			// facts so a later `return b` rejects a local arg through the
			// call, identical to the direct-assignment form.
			recordStructReturnCallFieldFacts(c, targetSym.Name, dstt, val)
		}
		return
	}
	// Non-Symbol target (Dot, Index, or a chain of them). Compute the
	// target's provenance path so all the path-keyed bookkeeping fires
	// at the same level.
	path, ok := ProvenancePathForExpr(target)
	if !ok || path.Fields == "" || c.IsGlobalBinding(path.Root) {
		return
	}
	if t, ok := c.TypeForVar(path.Root); !ok || t.Indirection > 0 {
		return
	}
	// Record new facts based on the value's shape. Clearing strategy
	// matches codegen exactly:
	//   - Scalar slice/pointer destination overwrites at the path,
	//     and clears descendants (a stale `path.[]` from prior contents
	//     no longer describes anything reachable through the new value).
	//   - Aggregate Symbol-copy is a memcpy of the whole source aggregate
	//     into the target — CopyFieldPointersUnderPath clears descendants
	//     of dst then re-keys src descendants under dst.
	//   - Aggregate StructLiteral RHS writes only the listed fields;
	//     unlisted fields keep their prior bytes (and their prior facts).
	//     `recordStructLiteralFieldFacts` clears per-listed-field, not
	//     blanket — otherwise `o.inner = Inner{}` would claim to wipe
	//     `o.inner.buf` while codegen leaves the bytes in place.
	switch {
	case dstt.Indirection > 0 || dstt.IsSlice():
		c.PointerFlow().ForgetFieldPointersUnder(path.Key())
		c.PointerFlow().SetPathPointer(path.Key(), pointerExprForAST(c, val, ""))
	default:
		if sl, ok := val.(*StructLiteral); ok {
			recordStructLiteralFieldFacts(c, path, sl)
		} else if srcPath, ok := ProvenancePathForExpr(val); ok && srcPath.Fields != "" && !c.IsGlobalBinding(srcPath.Root) {
			if t, ok := c.TypeForVar(srcPath.Root); ok && t.Indirection == 0 {
				c.PointerFlow().CopyFieldPointersUnderPath(srcPath.Key(), path.Key())
			}
		} else if sym, ok := val.(*Symbol); ok && !c.IsGlobalBinding(sym.Name) {
			if t, ok := c.TypeForVar(sym.Name); ok && t.Indirection == 0 {
				c.PointerFlow().CopyFieldPointersUnderPath(sym.Name, path.Key())
			}
		} else {
			c.PointerFlow().ForgetFieldPointersUnder(path.Key())
		}
	}
}

func checkBorrowedPointerAssignment(c *Context, a *Assignment, dst ASTType) {
	if !borrowedPointerExpr(c, a.Val) {
		return
	}
	if dst.Indirection == 0 {
		return
	}
	if sym, ok := a.Target.(*Symbol); ok && !c.IsGlobalBinding(sym.Name) {
		return
	}
	checkBorrowedPointerDoesNotEscape(c, a.Val, "assignment")
}

// targetLifetimeOpaque reports whether an assignment's destination has a
// lifetime that can't be proven equal to or shorter than the source's
// lifetime. Globals live forever; any path that goes through a pointer
// (auto-deref via Dot, or explicit Deref) reaches storage we can't see
// into — borrowed pointer parameters may point at heap, globals, or any
// caller's frame, and the function body has no way to discriminate.
// Both must reject escape-restricted sources. Local Symbol or local
// Symbol.field paths are safe (their lifetime is the current frame).
func targetLifetimeOpaque(c *Context, target AST) bool {
	switch t := target.(type) {
	case *Symbol:
		return c.IsGlobalBinding(t.Name)
	case *Dot:
		// Imported package selector (`p.g`): receiver is a package
		// symbol with no variable type — short-circuit before calling
		// ASTType, which would panic with "Variable 'p' undeclared".
		// The destination is an imported global, so opaque.
		if sym, ok := t.Val.(*Symbol); ok && c.IsImportedPackage(sym.Name) {
			return true
		}
		// Field access. If the receiver is a pointer the access is an
		// auto-deref and the pointee's storage is opaque. Otherwise the
		// answer recurses on the receiver.
		if t.Val.ASTType(c).Indirection > 0 {
			return true
		}
		return targetLifetimeOpaque(c, t.Val)
	case *Deref:
		return true
	case *Index:
		// Indexing into a slice yields a slot in whatever backs the
		// slice — opaque from the caller's view. Indexing into a fixed
		// array recurses on the array root.
		recvType := t.Val.ASTType(c)
		if recvType.IsSlice() {
			return true
		}
		return targetLifetimeOpaque(c, t.Val)
	}
	return false
}

// checkSliceFieldStoreEscape gates the per-field-store primitive: a slice
// value `val` is about to land in the `fieldName` slot of a struct whose
// type is `ownerType`. Used by both direct field assignment (`g.buf = s`)
// and struct-literal fill (`g = B{buf: s}`) so the same primitive enforces
// the same rule regardless of how the store is spelled.
func checkSliceFieldStoreEscape(c *Context, ownerType ASTType, fieldName string, val AST) {
	what := fmt.Sprintf("via field %v.%s", ownerType, fieldName)
	checkSliceEscape(c, val, what)
}

// checkSliceEscapeAssignment flags slice assignments whose destination has
// an opaque or longer-than-source lifetime. Routed through the single
// targetLifetimeOpaque predicate so every shape that the predicate
// understands (Symbol, Dot — value or pointer receiver, Index, Deref) is
// gated uniformly. The trailing "via …" phrase varies by target shape so
// the diagnostic stays anchored to something the reader can find.
func checkSliceEscapeAssignment(c *Context, a *Assignment, dst ASTType) {
	if !dst.IsSlice() {
		return
	}
	if !targetLifetimeOpaque(c, a.Target) {
		return
	}
	switch target := a.Target.(type) {
	case *Symbol:
		checkSliceEscape(c, a.Val, fmt.Sprintf("via global %s", target.Name))
	case *Dot:
		// Imported global selector (`p.g`): treat as a global slice
		// destination. The receiver is a package symbol, not a value
		// binding, so lvalueOwnerType's ASTType lookup would panic.
		if sym, ok := target.Val.(*Symbol); ok && c.IsImportedPackage(sym.Name) {
			checkSliceEscape(c, a.Val, fmt.Sprintf("via global %s.%s", sym.Name, target.Member))
			return
		}
		ownerType, ok := lvalueOwnerType(c, target.Val)
		if !ok {
			return
		}
		checkSliceFieldStoreEscape(c, ownerType, target.Member, a.Val)
	case *Index:
		// Slice element write (`g[i] = s`). No field name to anchor on.
		checkSliceEscape(c, a.Val, "via slice element")
	case *Deref:
		// Pointer write (`*out = s`). Pointee lifetime is opaque, so
		// any escape-restricted source is rejected.
		checkSliceEscape(c, a.Val, "through pointer write")
	}
}

// lvalueOwnerType returns the struct type whose fields are being addressed
// by `recv` (the type containing the field being written). For a pointer
// receiver the pointee type is returned; for a value receiver the
// receiver's type is returned directly.
func lvalueOwnerType(c *Context, recv AST) (ASTType, bool) {
	t := recv.ASTType(c)
	if t.Indirection > 0 {
		pt := t
		pt.Indirection--
		pt.MutMask >>= 1
		pt.OwnedMask >>= 1
		pt.NilMask >>= 1
		return pt, true
	}
	return t, true
}

// walkStructLiteralSliceEscape recurses into a struct literal looking for
// slice-typed fields whose value carries an escape-restricted origin.
// Nested struct literals are descended into so `Outer{inner: B{buf: s}}`
// reaches `s` even though it sits two layers deep. `onEscape` is invoked
// with the immediately-enclosing field's owner type and name so per-site
// callers can format their own diagnostic.
func walkStructLiteralSliceEscape(c *Context, ownerType ASTType, lit *StructLiteral, onEscape func(ownerType ASTType, fieldName string, val AST)) {
	for _, f := range lit.Fields {
		if f.Val == nil {
			continue
		}
		ft := f.Val.ASTType(c)
		if ft.IsSlice() {
			onEscape(ownerType, f.Name, f.Val)
			continue
		}
		if nested, ok := f.Val.(*StructLiteral); ok {
			walkStructLiteralSliceEscape(c, ft, nested, onEscape)
		}
	}
}

// checkStructLiteralFieldsForOpaqueTarget walks the literal's slice fields
// (including nested) and runs the per-field escape check when the
// assignment's target has an opaque or longer-than-source lifetime —
// globals, writes through borrowed pointers, etc. Equivalent to a sequence
// of per-field stores from the escape checker's point of view.
func checkStructLiteralFieldsForOpaqueTarget(c *Context, target AST, dst ASTType, lit *StructLiteral) {
	if !targetLifetimeOpaque(c, target) {
		return
	}
	walkStructLiteralSliceEscape(c, dst, lit, func(ownerType ASTType, fieldName string, val AST) {
		checkSliceFieldStoreEscape(c, ownerType, fieldName, val)
	})
}

func updateBorrowedBindingForAssignment(c *Context, target AST, dst ASTType, val AST) {
	sym, ok := target.(*Symbol)
	if !ok || dst.Indirection == 0 || c.IsGlobalBinding(sym.Name) {
		return
	}
	c.SetBorrowedBinding(sym.Name, borrowedPointerExpr(c, val))
}

func canWriteImmediatePointee(t ASTType) bool {
	return t.Indirection > 0 && t.MutMask&(1<<1) != 0
}

// lvalueIsWritable answers the type-system question "may this lvalue be
// written to?". It walks the lval AST and checks each link:
//
//   - Symbol root: writable iff the binding is var (not const).
//   - Deref: writable iff the pointer is *mut at the immediate pointee.
//   - Dot through a pointer expression (auto-deref): writable iff the
//     pointer is *mut at the immediate pointee.
//   - Dot through a value expression: writable iff the underlying base
//     is writable (recurse).
//   - Index/NonNullAssert: same pattern as Dot.
//
// The second return is a human-readable reason for the rejection. The
// Assignment handler is the only caller and uses the reason as the
// diagnostic. Codegen (compileLval) stays focused on producing an
// address — it does not consult or carry this property.
func lvalueIsWritable(c *Context, a AST) (bool, string) {
	switch v := a.(type) {
	case *Symbol:
		if c.IsConst(v.Name) {
			return false, fmt.Sprintf("Cannot assign to const binding %q", v.Name)
		}
		return true, ""
	case *Deref:
		t := v.Val.ASTType(c)
		if !canWriteImmediatePointee(t) {
			return false, fmt.Sprintf("Cannot write through read-only pointer of type %s; pointer must be *mut", t)
		}
		return true, ""
	case *Dot:
		// Route the parent through the shared selector resolver instead of
		// asking v.Val.ASTType directly. ASTType crashes on a package root
		// (Symbol.ASTType doesn't expect a package name) and re-implements
		// chain-walking that the resolver already does — both reasons the
		// rest of the compiler routes Dot chains through ResolveSelector.
		parent := ResolveSelector(c, v.Val)
		switch parent.Kind {
		case ResolvedPackage:
			// pkg.member = …. The producer's const/var distinction isn't
			// carried in the .bo today (Var has no IsConst flag), so we
			// permit writes to any exported var by name and rely on the
			// owning package to expose what it intends to be writable.
			// TODO(cross-pkg-const): once Var.IsConst flows through .bo,
			// reject writes here when the producer declared `const`.
			if _, ok := c.ImportedVarType(parent.Name, v.Member); !ok {
				return false, fmt.Sprintf("package %q has no variable %q", parent.Name, v.Member)
			}
			return true, ""
		case ResolvedRuntimeValue, ResolvedStructField:
			baseType := parent.Type
			if baseType.Indirection > 0 {
				if !canWriteImmediatePointee(baseType) {
					return false, fmt.Sprintf("Cannot write field %q through read-only pointer of type %s; pointer must be *mut", v.Member, baseType)
				}
				return true, ""
			}
			return lvalueIsWritable(c, v.Val)
		}
		// Types, values cases, functions, etc. aren't lvalues.
		return false, fmt.Sprintf("Cannot assign to %s.%s", parent.Name, v.Member)
	case *Index:
		baseType := v.Val.ASTType(c)
		if baseType.Indirection > 0 {
			if !canWriteImmediatePointee(baseType) {
				return false, fmt.Sprintf("Cannot write element through read-only pointer of type %s; pointer must be *mut", baseType)
			}
			return true, ""
		}
		if baseType.IsSlice() {
			if baseType.MutMask&(1<<1) == 0 {
				return false, fmt.Sprintf("Cannot write element through non-mut slice of type %s; declare the slice as mut %s", baseType, baseType)
			}
			return true, ""
		}
		// Fixed array: writes go to the array's own storage. The base
		// binding's writability governs.
		return lvalueIsWritable(c, v.Val)
	case *NonNullAssert:
		return lvalueIsWritable(c, v.Val)
	}
	// Unknown lval form. Be conservative — refuse rather than silently
	// accept. The caller's targeting machinery will already have rejected
	// most non-lvalue shapes long before this point.
	return false, fmt.Sprintf("Cannot assign to expression of this form")
}

// pointerExprForAST returns the binding-flow link for an expression: an Origin
// or pointer-slot reference that downstream alias bookkeeping can record.
// Despite the name, the link is not pointer-specific — a value-typed `owned`
// binding also has an Origin (created at its declaration), and any non-owned
// scalar derived from it carries the same Origin so c.Move on the source
// invalidates the alias the same way TargetDead invalidates a borrowed *T.
func pointerExprForAST(c *Context, a AST, assignedName string) flow.PointerExpr {
	switch ast := a.(type) {
	case *Symbol:
		if c.IsGlobalBinding(ast.Name) {
			return c.PointerFlow().UnknownPointer()
		}
		return c.PointerFlow().Pointer(flow.Binding(ast.Name))
	case *Address:
		if ast.Var != "" {
			name := ast.Var
			if c.IsGlobalBinding(name) {
				return c.PointerFlow().UnknownPointer()
			}
			if t, ok := c.TypeForVar(name); ok && t.Indirection > 0 {
				return c.PointerFlow().AddressOfPointerSlot(flow.Binding(name))
			}
			// Value-typed local. If the binding already carries a flow
			// link (owned self-link, or alias of another owned), &name
			// should preserve it so the resulting pointer references the
			// same Origin. Without this, taking &t of an aliased value
			// would silently produce a fresh Live origin and discard the
			// chain back to the moved source.
			existing := c.PointerFlow().Pointer(flow.Binding(name))
			if existing.KnownOrigin {
				return existing
			}
			return c.PointerFlow().NewLocalOrigin(flow.Binding(name))
		}
		if ast.Lit != nil {
			if root, ok := rootSymbolName(ast.Lit); ok && !c.IsGlobalBinding(root) {
				if t, ok := c.TypeForVar(root); ok && t.Indirection == 0 {
					return c.PointerFlow().NewLocalOrigin(flow.Binding(root))
				}
			}
		}
		return c.PointerFlow().UnknownPointer()
	case *Dot:
		return readProvenancePath(c, a)
	case *Index:
		return readProvenancePath(c, a)
	case *NonNullAssert:
		return pointerExprForAST(c, ast.Val, assignedName)
	case *OwnedPromotion:
		return pointerExprForAST(c, ast.Val, assignedName)
	case *SliceOp:
		// A slice's data pointer roots at the source's storage. Walk
		// the lvalue chain to its root:
		//   - Array sources rooted at a local binding (`local[:]`,
		//     `o.inner.buf[:]`, `a[0][:]`, `b.bufs[0][:]`, …) carry the
		//     binding's local-storage origin. The walker accepts any
		//     mix of Dot and Index as long as every step stays within
		//     the same stack frame: no pointer auto-deref, no indexing
		//     through a slice.
		//   - Slice sources (`s[:]` for a slice binding `s`) propagate
		//     the source binding's flow link directly.
		//   - Anything else (globals, sources behind a pointer, etc.)
		//     is opaque.
		valType := ast.Val.ASTType(c)
		if valType.IsArray() {
			if root, ok := lvalueLocalRoot(c, ast.Val); ok {
				existing := c.PointerFlow().Pointer(flow.Binding(root))
				if existing.KnownOrigin {
					return existing
				}
				return c.PointerFlow().NewLocalOrigin(flow.Binding(root))
			}
		}
		if valType.IsSlice() {
			// Re-slicing a slice keeps the same backing storage. Delegate
			// to pointerExprForAST so field facts (`b.buf[:]`) and array
			// element facts (`arr[0][:]`) propagate uniformly, not only
			// the bare-Symbol case.
			return pointerExprForAST(c, ast.Val, assignedName)
		}
		return c.PointerFlow().UnknownPointer()
	case *Funcall:
		// A type-cast Funcall (T(expr) where T is a type name) is structurally
		// a Funcall but semantically a reinterpretation of expr. compileCast
		// now rejects category-mismatched casts, so when the destination is a
		// pointer, the source must also be a pointer. In that case the cast
		// preserves the underlying address — the destination should inherit
		// the source's origin and validity, not be treated as an opaque new
		// pointer. Recurse into the cast argument so escape checks, deref
		// validity, and alias bookkeeping see the source's PointerExpr.
		pkg, name := ast.PkgAndName()
		if pkg == "" && len(ast.Args) == 1 {
			if _, ok := c.TypeByName(name); ok {
				return pointerExprForAST(c, ast.Args[0], assignedName)
			}
		}
		if assignedName != "" {
			retType := ast.ASTType(c)
			if retType.Indirection > 0 && retType.OwnedMask&1 != 0 {
				return c.PointerFlow().NewAllocatedOrigin(flow.Binding(assignedName))
			}
		}
		// Borrowed-returning call: the result aliases one or more of the
		// arguments per the callee's inferred ReturnAliases. Propagate the
		// matching argument's origin onto the result so the caller's escape
		// / deref machinery treats it exactly as a borrow produced inline.
		// This is the call-expansion that keeps the feature sound once a
		// borrowed-returning callee is made legal (it cannot fire today —
		// the callee would have been rejected first).
		if pe, ok := funcallResultOrigin(c, ast, assignedName); ok {
			return pe
		}
		// Virtual dispatch: a borrowed result of an interface method call
		// (`v.method(...)`) borrows the interface value per the declared
		// `from(...)` contract, so it cannot outlive v's referent.
		if pe, ok := interfaceCallResultOrigin(c, ast, assignedName); ok {
			return pe
		}
		return c.PointerFlow().UnknownPointer()
	default:
		return c.PointerFlow().UnknownPointer()
	}
}

// funcallResultOrigin synthesizes the PointerExpr for a call result from
// the callee's inferred slot-0 ReturnAliases. Single aliased param → the
// result inherits that argument's origin verbatim. Multiple → the result
// is modeled conservatively as escape-restricted iff any contributing
// argument is escape-restricted (the multi-param union fallback): a fresh
// local-shaped origin is registered when any contributor is escape-
// restricted, so the caller treats the result as un-returnable / un-
// storable-long-lived; otherwise the result is opaque (records nothing).
// Returns (_, false) when the callee carries no slot-0 alias.
func funcallResultOrigin(c *Context, call *Funcall, assignedName string) (flow.PointerExpr, bool) {
	callee, args := resolveCalleeForAlias(c, call)
	if callee == nil {
		return flow.PointerExpr{}, false
	}
	return resultOriginFromSlot0(c, aliasSet(c, callee), args, assignedName)
}

// resultOriginFromSlot0 synthesizes a call result's PointerExpr from return
// slot 0's alias set and the positional argument ASTs (args[0] is the
// receiver, args[k] the k-th parameter). Shared by direct calls (inferred
// aliases via funcallResultOrigin) and interface dispatch (declared aliases
// via interfaceCallResultOrigin); the single/multi-param handling is identical
// regardless of where the alias set came from.
func resultOriginFromSlot0(c *Context, aliases [][]int, args []AST, assignedName string) (flow.PointerExpr, bool) {
	if len(aliases) == 0 || len(aliases[0]) == 0 {
		return flow.PointerExpr{}, false
	}
	params := aliases[0]
	if len(params) == 1 {
		p := params[0]
		if p < 0 || p >= len(args) {
			return flow.PointerExpr{}, false
		}
		return argAliasProvenance(c, args[p]), true
	}
	// Multi-param union (conservative fallback). The result is treated as
	// live only if every contributing argument is live, and escape-
	// restricted if any is. Single-Origin PointerExpr cannot carry the
	// union, so the two consumers diverge:
	//
	//   - Bound result (`const t = pick(x,y)`, assignedName != ""): the
	//     binding persists and may later escape with a *different* arg
	//     being the local one, so it must be the conservative meet — a
	//     local-rooted origin keyed to the destination. This over-rejects
	//     a bound-then-escaped multi-param result (the proposal's
	//     sanctioned fallback coarseness; the precise fix is the deferred
	//     []Origin representation extension).
	//
	//   - Transient result (assignedName == ""): a global-assign RHS, a
	//     direct-return arg, any escape-check site — read exactly once by
	//     a kind-sensitive check that inspects the origin *kind*, never the
	//     param identity. Returning the most-restrictive contributor's real
	//     origin (a local contributor first, then a borrowed one) carries
	//     precisely the right semantics with no synthetic origin. A bail to
	//     opaque here would let a local reaching long-lived storage through
	//     a multi-param call escape uncaught.
	if assignedName != "" {
		anyRestricted := false
		for _, p := range params {
			if p < 0 || p >= len(args) {
				continue
			}
			ap := argAliasProvenance(c, args[p])
			if ap.KnownOrigin && c.PointerFlow().IsEscapeRestricted(ap.Origin) {
				anyRestricted = true
				break
			}
		}
		if anyRestricted {
			return c.PointerFlow().NewLocalOrigin(flow.Binding(assignedName)), true
		}
		// All contributors are clean borrowed params: inherit the first.
		for _, p := range params {
			if p >= 0 && p < len(args) {
				return argAliasProvenance(c, args[p]), true
			}
		}
		return flow.PointerExpr{}, false
	}
	// Transient: JOIN all contributors so every one survives into the
	// consumer. A local contributor still dominates (JoinOrigins is
	// most-restrictive-first), and a multi-borrowed union becomes a join
	// origin whose members the consumers expand (JoinMembers) — the
	// summary capture records EVERY contributing param, and an escape
	// check sees the most-restrictive member. Returning only one
	// contributor here under-recorded the alias set (`fwd(x,y){return
	// pick(x,y,f)}` inferred [[0]] instead of [[0,1]]), letting a local
	// in the dropped slot escape through the forwarding chain.
	var merged flow.PointerExpr
	for _, p := range params {
		if p < 0 || p >= len(args) {
			continue
		}
		ap := argAliasProvenance(c, args[p])
		if !ap.KnownOrigin {
			continue
		}
		if !merged.KnownOrigin {
			merged = ap
			continue
		}
		if merged.Origin == ap.Origin {
			continue
		}
		merged = c.PointerFlow().JoinOrigins(merged, ap)
	}
	if merged.KnownOrigin {
		return merged, true
	}
	return flow.PointerExpr{}, false
}

// interfaceCallResultOrigin synthesizes the result origin for a virtual call
// `v.method(args)` (v an interface variable) from the interface method's
// *declared* ReturnAliases (its `from(...)` contract). The receiver (the
// interface value v) is argument index 0, so a `from(self)` result inherits
// v's provenance — for an interface value that is v's `data`-field origin
// (the referent the fat pointer points at, via argAliasProvenance), exactly
// the lifetime `from(self)` bounds the result by. Mirrors funcallResultOrigin
// but reads the static contract instead of inferring a body. Returns false
// for any non-interface call or a method that declares no borrow.
func interfaceCallResultOrigin(c *Context, call *Funcall, assignedName string) (flow.PointerExpr, bool) {
	msig, recv, ok := resolveInterfaceCallForAlias(c, call)
	if !ok || len(msig.ReturnAliases) == 0 {
		return flow.PointerExpr{}, false
	}
	args := append([]AST{recv}, call.Args...)
	return resultOriginFromSlot0(c, msig.ReturnAliases, args, assignedName)
}

// resolveInterfaceCallForAlias recognizes the one supported interface-call
// shape — `v.method(args)` where v is an interface-typed variable (expression
// receivers are rejected at codegen) — and returns the declared method
// signature plus the receiver AST (the interface value v). Returns false
// otherwise.
func resolveInterfaceCallForAlias(c *Context, call *Funcall) (*InterfaceMethodSig, AST, bool) {
	pkg, fname := call.PkgAndName()
	if pkg == "" {
		return nil, nil, false
	}
	vt, ok := c.TypeForVar(pkg)
	if !ok || !c.IsInterfaceType(vt) {
		return nil, nil, false
	}
	iface, ok := c.InterfaceForName(vt.Name)
	if !ok {
		return nil, nil, false
	}
	for i := range iface.Methods {
		if iface.Methods[i].Name == fname {
			return &iface.Methods[i], &Symbol{Name: pkg}, true
		}
	}
	return nil, nil, false
}

func pointerSlotTargetForDeref(c *Context, target AST) (flow.PointerExpr, bool) {
	deref, ok := target.(*Deref)
	if !ok {
		return flow.PointerExpr{}, false
	}
	ptr := pointerExprForAST(c, deref.Val, "")
	if !ptr.KnownSlot {
		return flow.PointerExpr{}, false
	}
	return ptr, true
}

func targetMayOverwriteOwnedStorage(c *Context, target AST) bool {
	if target.ASTType(c).HasOwned() {
		return true
	}
	if dot, ok := target.(*Dot); ok {
		// Resolve through the selector so a package root doesn't blow up
		// in Symbol.ASTType. Only value-typed parents can be an owned
		// struct; anything else (cross-package var, type name, etc.) can't
		// overwrite owned storage by definition.
		parent := ResolveSelector(c, dot.Val)
		if parent.Kind != ResolvedRuntimeValue && parent.Kind != ResolvedStructField {
			return false
		}
		def, ok := structDeclForType(c, parent.Type)
		if !ok {
			return false
		}
		_, declaredType := def.ByteOffset(c, dot.Member)
		return declaredType.HasOwned()
	}
	return false
}

func updatePointerFlowForAssignment(c *Context, target AST, dst ASTType, val AST) {
	if ptr, ok := pointerSlotTargetForDeref(c, target); ok && dst.Indirection > 0 {
		src := pointerExprForAST(c, val, string(ptr.SlotTarget))
		c.PointerFlow().StorePointerThrough(ptr, src)
		c.SetBorrowedBinding(string(ptr.SlotTarget), borrowedPointerExpr(c, val))
		return
	}
	sym, ok := target.(*Symbol)
	if !ok {
		return
	}
	if c.IsGlobalBinding(sym.Name) {
		c.PointerFlow().AssignPointer(flow.Binding(sym.Name), c.PointerFlow().UnknownPointer())
		return
	}
	// Update the binding's flow link. For pointer-typed dests this is the
	// usual *p alias bookkeeping; for value-typed dests, an owned source
	// produces a value-alias whose origin gets invalidated on c.Move.
	// When the source has no link (UnknownPointer), assigning still clears
	// any stale link the destination held from a prior owned source.
	c.PointerFlow().AssignPointer(flow.Binding(sym.Name), pointerExprForAST(c, val, sym.Name))
}

func invalidateOwnedFieldFactsForMutableTarget(c *Context, target AST) {
	if _, ok := pointerSlotTargetForDeref(c, target); ok {
		return
	}
	path, ok := FlowPathForExpr(target)
	if !ok || path.Fields == "" {
		return
	}
	if !targetMayOverwriteOwnedStorage(c, target) {
		return
	}
	ptr := c.PointerFlow().Pointer(flow.Binding(path.Root))
	if ptr.KnownOrigin && ptr.Origin == flow.Origin(path.Root) {
		return
	}
	c.InvalidateOwnedFieldFactsByPointerInvalidation(c.PointerFlow().WriteThroughPointer(ptr))
}

func invalidatesOwnedFieldFactsParam(param ASTType) bool {
	if param.Indirection == 0 || param.HasOwned() {
		return false
	}
	return param.MutMask != 0
}

func checkAddressOfOwnedForDest(c *Context, val AST, dst ASTType) {
	if nn, ok := val.(*NonNullAssert); ok {
		checkAddressOfOwnedForDest(c, nn.Val, dst)
		return
	}
	a, ok := val.(*Address)
	if !ok {
		return
	}
	name, ok := a.NamedTarget()
	if !canWriteImmediatePointee(dst) {
		return
	}
	if !ok {
		return
	}
	if c.IsConst(name) {
		return
	}
	t, ok := c.TypeForVar(name)
	if !ok || !t.HasOwned() {
		return
	}
	// If the destination's immediate pointee is owned, the ownership
	// obligation transfers through the pointer to the destination, which
	// becomes responsible for discharging it. Overwrites through the
	// pointer are then policed by the deref-target obligation check at
	// the assignment site, so blocking the address-take here is no longer
	// necessary.
	if dst.OwnedMask&(1<<1) != 0 {
		return
	}
	CompileErrorF(a, "Cannot take mutable address of owned binding \"%s\"", name)
}

func markMovedIfOwnedSource(of io.Writer, c *Context, expected ASTType, val AST) {
	if !expected.HasOwned() {
		return
	}
	if nn, ok := val.(*NonNullAssert); ok {
		markMovedIfOwnedSource(of, c, expected, nn.Val)
		return
	}
	if op, ok := val.(*OwnedPromotion); ok {
		markMovedIfOwnedSource(of, c, expected, op.Val)
		return
	}
	switch v := val.(type) {
	case *Symbol:
		checkOwnedSourceAvailable(c, val)
		declared, _ := c.DeclaredTypeForVar(v.Name)
		// Pointer-typed owned source moving into an owned destination is a
		// *transfer*: the destination pointer becomes the new owner of the
		// same allocation, the storage stays alive. Value-typed sources are
		// a *consume*: the source's bits are conceptually moved out and
		// any aliases reading those bits afterwards are stale.
		if declared.Indirection > 0 {
			c.MoveTransfer(v.Name)
		} else {
			c.MoveConsume(v.Name)
		}
		if declared.Indirection > 0 && declared.NilMask&1 != 0 {
			fmt.Fprintf(of, "\tmov %s 0\n", v.Name)
			c.SetNullFact(VarFlowPath(v.Name), NullKnownNull)
		}
	case *Address:
		// `&x` into an owned-pointer destination transfers ownership: the
		// destination pointer becomes the new owner of x's storage, and x
		// the binding can no longer be used. The storage itself is still
		// live — the destination points at it and will be the one to
		// discharge the obligation, so other pointer aliases of the same
		// storage stay usable until the new owner is consumed.
		if v.Var != "" {
			checkOwnedSourceAvailable(c, &Symbol{Name: v.Var, p: v.p})
			c.MoveTransfer(v.Var)
		}
	case *Deref:
		ptype := v.Val.ASTType(c)
		if !canWriteImmediatePointee(ptype) {
			CompileErrorF(val, "Cannot move owned value through read-only pointer")
		}
	case *Dot:
		baseType := v.Val.ASTType(c)
		fieldType := v.ASTType(c)
		if parentOwnsFields(baseType) && fieldType.HasOwned() {
			checkOwnedSourceAvailable(c, val)
			lv := compileLval(of, c, val, nullspot)
			fmt.Fprintf(of, "\tmov [%s] 0\n", lv.ref)
			lv.free(of)
			if path, ok := FlowPathForExpr(val); ok {
				c.SetOwnedFieldConsumed(path, true)
			}
		}
	}
}

func compileStructLiteralInto(of io.Writer, c *Context, a AST, lit *StructLiteral, dest spot, ctxType ASTType) spot {
	if dest.empty() {
		dest = newSpot(of, c, c.Temp(), ctxType)
	} else {
		dest.t = ctxType
	}
	def, ok := structDeclForType(c, lit.Type)
	if !ok {
		CompileErrorF(a, "No such structure %v", lit.Type)
	}

	seen := make(map[string]bool)
	for _, f := range lit.Fields {
		if seen[f.Name] {
			CompileErrorF(a, "Duplicate field %q in struct literal %v", f.Name, lit.Type)
		}
		seen[f.Name] = true

		off, declaredType := def.ByteOffset(c, f.Name)
		if declaredType.Name == "" && declaredType.Element == nil && declaredType.FuncSig == nil && declaredType.AnonFields == nil {
			CompileErrorF(a, "No such struct member %q in struct %v", f.Name, lit.Type)
		}
		fieldType := fieldTypeForBase(ctxType, declaredType)
		srcType := f.Val.ASTType(c)
		// Capturing a whole inconsistent aggregate into a struct field (as a
		// bare pointer `f` or `&f`) forms an alias that could read its
		// moved-out field.
		if name := aggregateBindingName(f.Val); name != "" {
			checkAggregateNotAliasedWhileInconsistent(c, name, f.Val)
		}
		checkBorrowedPointerDoesNotEscape(c, f.Val, fmt.Sprintf("field %v.%s", lit.Type, f.Name))
		checkAddressOfOwnedForDest(c, f.Val, fieldType)
		// Interface field: route through the interface coercion path so
		// a concrete value gets a fat pointer into the field's storage.
		// This mirrors the assignment/return/arg-call coercion sites and
		// supports synthesized multi-return tuples whose fields use the
		// builtin error interface.
		if shouldCoerceToInterface(c, fieldType, srcType) {
			fieldAddr := newSpot(of, c, c.Temp(), ASTType{Name: "i64"})
			fmt.Fprintf(of, "\tlea %s [%s+%d]\n", fieldAddr.ref, dest.ref, off)
			emitInterfaceFatPtr(of, c, f.Val, fieldType, srcType, f.Val, fieldAddr.ref, "")
			if fieldType.OwnedMask != 0 {
				markMovedIfOwnedSource(of, c, fieldType, f.Val)
			}
			fieldAddr.free(of)
			continue
		}
		if _, reason := coerceType(c, fieldType, srcType); reason != coerceOK {
			reportCoerceFailure(f.Val, fieldType, srcType, reason,
				"For field %v.%s, expected type %v but got %v",
				lit.Type, f.Name, fieldType, srcType)
		}
		// Only the intlit rewrite is safe here; the downstream codegen
		// distinguishes `srcType.Same(fieldType)` (nullspot path,
		// suitable for intlit) from the typed-temp path (required for
		// nil — compileTop's nil handler needs a typed dest), and
		// rewriting nil → fieldType would route nil into the wrong path.
		if srcType.Same(intlitASTType()) {
			srcType = fieldType
		}

		// Memory-backed field (nested struct, array, anything > 8 bytes
		// with no indirection): place the value directly into the
		// containing storage to avoid the address-vs-bytes confusion
		// that a single `mov [dest+off] tmp` would cause for a
		// memory-backed source spot.
		if fieldType.Indirection == 0 && typeIsMemoryBacked(c, fieldType) {
			fieldAddr := newSpot(of, c, c.Temp(), ASTType{Name: "i64"})
			fmt.Fprintf(of, "\tlea %s [%s+%d]\n", fieldAddr.ref, dest.ref, off)
			fieldDest := spot{ref: fieldAddr.ref, t: fieldType, nameIsAddress: true}
			if sl, ok := f.Val.(*StructLiteral); ok && sameIgnoringOwned(fieldType, sl.Type) {
				compileStructLiteralInto(of, c, a, sl, fieldDest, fieldType)
			} else if al, ok := f.Val.(*ArrayLiteral); ok {
				compileArrayLiteralInto(of, c, a, fieldDest, al)
			} else {
				v := compileTop(of, c, f.Val, nullspot)
				spot_memcpy(of, c, fieldDest, v, fieldType.Size(c))
				v.free(of)
			}
			markMovedIfOwnedSource(of, c, fieldType, f.Val)
			fieldAddr.free(of)
			continue
		}

		var v spot
		if srcType.Same(fieldType) {
			v = compileTop(of, c, f.Val, nullspot)
		} else {
			tmp := newSpot(of, c, c.Temp(), fieldType)
			v = compileTop(of, c, f.Val, tmp)
		}
		markMovedIfOwnedSource(of, c, fieldType, f.Val)
		fmt.Fprintf(of, "\tmov [%s+%d] %s\n", dest.ref, off, v.ref)
		v.free(of)
	}

	for _, field := range def.Fields {
		ft := fieldTypeForBase(ctxType, field.Type)
		if !seen[field.Name] {
			if parentOwnsFields(ctxType) && ft.HasOwned() {
				CompileErrorF(a, "Missing owned field %v.%s in owned struct literal", lit.Type, field.Name)
			}
			if !ft.ZeroInitializable(c) {
				CompileErrorF(a, "Missing non-zero-initializable field %v.%s in struct literal", lit.Type, field.Name)
			}
		}
	}

	return dest
}

func compileAllocBuiltin(of io.Writer, c *Context, a AST, ast *Funcall, dest spot) spot {
	if len(ast.Args) != 1 {
		CompileErrorF(a, "alloc(T) requires exactly one type argument")
	}
	t, ok := typeExprFromAST(c, ast.Args[0])
	if !ok {
		CompileErrorF(ast.Args[0], "alloc argument must be a type")
	}
	if !t.ZeroInitializable(c) {
		CompileErrorF(ast.Args[0], "alloc(%s) requires a zero-initializable type; use new(expr) to allocate initialized storage", t)
	}
	retType := ast.ASTType(c)
	if dest.empty() {
		dest = newSpot(of, c, c.Temp(), retType)
	}
	rdi := regSpot(of, "rdi")
	fmt.Fprintf(of, "\tmov rdi %d\n", t.Size(c))
	fmt.Fprintf(of, "\tcall _heap.alloc\n")
	rdi.free(of)
	rax := regSpot(of, "rax")
	rax.t = retType
	move(of, c, dest, rax)
	rax.t = regtype
	rax.free(of)
	return dest
}

func newBuiltinTypeForDest(c *Context, ast *Funcall, dst ASTType) (ASTType, bool) {
	pkg, name := ast.PkgAndName()
	if pkg != "" || name != "new" || len(ast.Args) != 1 || dst.Indirection == 0 {
		return ASTType{}, false
	}
	exprType := ast.Args[0].ASTType(c)
	pointee := pointeeType(dst)
	if !sameIgnoringOwned(pointee, exprType) {
		return ASTType{}, false
	}
	return dst, true
}

func compileValueIntoAddress(of io.Writer, c *Context, a AST, val AST, addr spot, pointee ASTType) {
	if sl, ok := val.(*StructLiteral); ok && sameIgnoringOwned(pointee, sl.Type) {
		compileStructLiteralInto(of, c, a, sl, spot{ref: addr.ref, t: pointee, nameIsAddress: true}, pointee)
		return
	}
	if al, ok := val.(*ArrayLiteral); ok {
		compileArrayLiteralInto(of, c, a, spot{ref: addr.ref, t: pointee, nameIsAddress: true}, al)
		return
	}
	srcType := val.ASTType(c)
	checkAddressOfOwnedForDest(c, val, pointee)
	if _, reason := coerceType(c, pointee, srcType); reason != coerceOK {
		reportCoerceFailure(val, pointee, srcType, reason,
			"Cannot initialize allocated %s with value of type %s", pointee, val.ASTType(c))
	}
	// No effective rewrite needed here: the downstream temp is typed
	// `pointee` and compileTop drives off that, not srcType.
	tmp := newSpot(of, c, c.Temp(), pointee)
	v := compileTop(of, c, val, tmp)
	dst := spot{ref: addr.ref, t: pointee, nameIsAddress: true}
	if typeIsMemoryBacked(c, pointee) {
		spot_memcpy(of, c, dst, v, pointee.Size(c))
	} else {
		fmt.Fprintf(of, "\tmov [%s] %s\n", addr.ref, v.ref)
	}
	v.free(of)
	markMovedIfOwnedSource(of, c, pointee, val)
}

func compileNewBuiltin(of io.Writer, c *Context, a AST, ast *Funcall, dest spot) spot {
	if len(ast.Args) != 1 {
		CompileErrorF(a, "new(expr) requires exactly one argument")
	}
	if _, ok := typeExprFromAST(c, ast.Args[0]); ok {
		CompileErrorF(ast.Args[0], "new(T) is not implemented yet; use alloc(T) for zero-initializable types or new(expr)")
	}
	retType := ast.ASTType(c)
	if !dest.empty() {
		if inferred, ok := newBuiltinTypeForDest(c, ast, dest.t); ok {
			retType = inferred
		}
	}
	pointee := pointeeType(retType)
	if dest.empty() {
		dest = newSpot(of, c, c.Temp(), retType)
	} else {
		dest.t = retType
	}
	rdi := regSpot(of, "rdi")
	fmt.Fprintf(of, "\tmov rdi %d\n", pointee.Size(c))
	fmt.Fprintf(of, "\tcall _heap.alloc\n")
	rdi.free(of)
	rax := regSpot(of, "rax")
	rax.t = retType
	move(of, c, dest, rax)
	rax.t = regtype
	rax.free(of)
	compileValueIntoAddress(of, c, a, ast.Args[0], dest, pointee)
	return dest
}

func compileFreeBuiltin(of io.Writer, c *Context, a AST, ast *Funcall) spot {
	if len(ast.Args) != 1 {
		CompileErrorF(a, "free(p) requires exactly one argument")
	}
	arg := ast.Args[0]
	argt := arg.ASTType(c)
	if argt.Indirection == 0 || argt.OwnedMask&1 == 0 {
		CompileErrorF(ast.Args[0], "free requires an owned pointer, got %s", argt)
	}
	if path, ok := FlowPathForExpr(arg); ok {
		// A statically-known-nil owned pointer carries no obligation: there
		// is nothing to free. Reject the call as a likely bug. Runtime nil
		// safety is still tested when the compiler cannot prove the
		// argument is nil (e.g. via a function parameter).
		if c.NullFact(path) == NullKnownNull {
			CompileErrorF(arg, "free(%s) is redundant: %s is statically known to be nil", path.Key(), path.Key())
		}
		checkOwnedFieldsConsumedBeforeRawFree(c, arg, path, argt)
	} else {
		pointee := pointeeType(argt)
		if def, ok := structDeclForType(c, pointee); ok {
			for _, field := range def.Fields {
				if fieldTypeForBase(pointee, field.Type).HasOwned() {
					CompileErrorF(arg, "free(%s) would leak owned fields", argt)
				}
			}
		}
	}
	rdi := newSpotWithReg(of, c, c.Temp(), argt, "rdi")
	v := compileTop(of, c, arg, rdi)
	if !v.same(&rdi) {
		rdi.free(of)
		rdi = v
		fmt.Fprintf(of, "\tinreg %s rdi\n", rdi.ref)
	}
	fmt.Fprintf(of, "\tcall _heap.free\n")
	fmt.Fprintf(of, "\trelease rdi\n")
	rdi.free(of)
	markMovedIfOwnedSource(of, c, argt, arg)
	{
		ptrExpr := pointerExprForAST(c, arg, "")
		if ptrExpr.KnownOrigin {
			c.PointerFlow().InvalidateOrigin(ptrExpr.Origin, flow.TargetDead)
		}
	}
	return nullspot
}

// func spot_memset(of io.Writer, dst spot, val byte, bytes int) {
// 	qwords := bytes / 8
// 	singles := bytes % 8

// 	fmt.Fprintf(of, "\tacquire rax\n")
// 	fmt.Fprintf(of, "\tmov rax %d\n", val)
// 	for i := 0; i < qwords; i++ {
// 		fmt.Fprintf(of, "\tmov [%s+%d] rax\n", dst, i*8)
// 	}
// 	for i := 0; i < singles; i++ {
// 		fmt.Fprintf(of, "\tmovb [%s+%d] al\n", dst, qwords*8+i)
// 	}
// 	fmt.Fprintf(of, "\trelease rax\n")
// }

// can move T to T or T into *T
func move(of io.Writer, c *Context, dest spot, src spot) {
	if dest.t.Same(regtype) || src.t.Same(regtype) {
		// We have a register. Just move it.
		fmt.Fprintf(of, "\tmov %s %s\n", dest.ref, src.ref)
		return
	}
	if src.t.Indirection == 0 && src.t.Size(c) > 8 {
		spot_memcpy(of, c, dest, src, src.t.Size(c))
		return
	}
	if dest.t.SameRepr(src.t) {
		fmt.Fprintf(of, "\tmov %s %s\n", dest.ref, src.ref)
		return
	}
	depoint := dest.t
	depoint.Indirection--
	depoint.MutMask >>= 1
	depoint.OwnedMask >>= 1
	depoint.NilMask >>= 1
	if depoint.SameRepr(src.t) {
		// dest was *T and src is T
		fmt.Fprintf(of, "\tmov [%s] %s\n", dest.ref, src.ref)
		if src.t.Size(c) > 8 {
			panic("(TODO) Can't copy T into *T for T.size > 8 bytes")
		}
		return
	}

	fmt.Printf("DEST: %#v\nSRC:%#v\nDEPOINT: %#v\n", dest, src, depoint)
	panic("move TODO")
}

func materializePointerValue(of io.Writer, c *Context, s spot) spot {
	if s.nameIsAddress && s.t.Indirection > 0 {
		tmp := newSpot(of, c, c.Temp(), s.t)
		move(of, c, tmp, s)
		return tmp
	}
	return s
}

// compileLval compiles an AST into a spot of type a.ASTType *or* pointer to a.ASTType.
// If the type is the same as the value, it must me suitable to mov dest src.
// Otherwise, if a pointer, it must be suitable for mov [dest] src, or for larger
// types memcpy(dest, &src).
func compileLval(of io.Writer, c *Context, a AST, dest spot) spot {
	switch ast := a.(type) {
	case *Symbol:
		t := ast.ASTType(c)
		s := spot{ref: ast.Name, t: t, nameIsAddress: c.NameIsAddress(ast.Name)}
		if dest.same(&nullspot) {
			return s
		}
		if s.same(&dest) {
			return s
		}
		move(of, c, dest, s)
		return dest
		// t := ast.ASTType(c)
		// if t.Indirection == 0 && t.Size(c) > 8 {
		// 	// s is a value that doesn't fit in a
		// 	// register. We can just return it
		// 	// since it is a pointer type already.
		// 	s := spot{ref: ast.Name, t: t}
		// 	if dest.same(&nullspot) {
		// 		return s
		// 	}
		// 	move(of, c, dest, s)
		// 	return dest
		// }
		// pt := t
		// pt.Indirection++
		// if dest.empty() {
		// 	dest = newSpot(of, c, c.Temp(), pt)
		// }
		// fmt.Fprintf(of, "\tlea %s %s\n", dest.ref, ast.Name)
		// return dest
	case *Dot:
		// Cross-package variable lvalue: &pkg.varname → lea dest pkg.varname
		if sym, ok := ast.Val.(*Symbol); ok && c.IsImportedPackage(sym.Name) {
			vt, ok := c.ImportedVarType(sym.Name, ast.Member)
			if !ok {
				CompileErrorF(a, "package %q has no variable %q", sym.Name, ast.Member)
			}
			ref := sym.Name + "." + ast.Member
			vt.Indirection++
			vt.MutMask = (vt.MutMask << 1) | (1 << 1) // globals are var (mutable)
			vt.OwnedMask <<= 1
			vt.NilMask <<= 1
			if dest.empty() {
				dest = newSpot(of, c, c.Temp(), vt)
			}
			fmt.Fprintf(of, "\tlea %s %s\n", dest.ref, ref)
			return dest
		}
		l := compileLval(of, c, ast.Val, nullspot)
		orig := l
		l = materializePointerValue(of, c, l)
		if !l.same(&orig) {
			defer l.free(of)
		}
		if l.t.Indirection != 0 && l.t.NilMask&1 != 0 {
			CompileErrorF(ast.Val, "Cannot access field %s through nullable pointer type %s", ast.Member, l.t)
		}
		def, ok := structDeclForType(c, l.t)
		if !ok {
			CompileErrorF(a, "No such structure \"%v\"", l.t)
		}
		offset, declaredType := def.ByteOffset(c, ast.Member)
		mtype := fieldTypeForBase(l.t, declaredType)
		mtype.Indirection += 1
		if dest.empty() {
			dest = newSpot(of, c, c.Temp(), mtype)
		}
		fmt.Fprintf(of, "\t// ONE\n")
		fmt.Fprintf(of, "\tlea %s [%s+%d]\n", dest.ref, l.ref, offset)
		return dest
	case *Deref:
		v := compileTop(of, c, ast.Val, nullspot)
		t := v.t
		if t.Indirection == 0 {
			CompileErrorF(a, "Cannot dereference non-pointer type %s", t)
		}
		if t.NilMask&1 != 0 {
			CompileErrorF(ast.Val, "Cannot dereference nullable pointer type %s", t)
		}
		// We're just going to return the pointer.
		return v
	case *NonNullAssert:
		v := compileTop(of, c, ast, dest)
		return v
	case *Index:
		w := compileTop(of, c, ast.Val, nullspot)
		vt := ast.ASTType(c)
		max := newSpot(of, c, c.Temp(), numASTType())
		var addr spot
		if w.t.IsSlice() {
			t := vt
			t.Indirection += 1
			addr = newSpot(of, c, c.Temp(), t)
			fmt.Fprintf(of, "\tmov %s [%s]\n", addr.ref, w.ref)
			fmt.Fprintf(of, "\tmov %s [%s+8]\n", max.ref, w.ref)
		} else {
			addr = w
			fmt.Fprintf(of, "\tmov %s %d\n", max.ref, w.t.ArraySize)
		}
		lvt := vt
		lvt.Indirection += 1
		if dest.empty() {
			dest = newSpot(of, c, c.Temp(), lvt)
		}
		if ast.NAST == nil {
			// fixed offset at compile time
			off := uint64(vt.Size(c)) * ast.N
			if !w.t.IsSlice() {
				if ast.N >= uint64(w.t.ArraySize) {
					CompileErrorF(a, "Index %d greater than max for array of size %d", ast.N, w.t.ArraySize)
				}
			} else {
				l := c.Label("icheck")
				fmt.Fprintf(of, "\tcmp %s %d\n", max.ref, ast.N)
				fmt.Fprintf(of, "\tjg %s\n", l)
				fmt.Fprintf(of, "\tmov rdi %d\n", ast.N)
				fmt.Fprintf(of, "\tmov rsi %s\n", max.ref)
				fmt.Fprintf(of, "\tcall _init.index_oob\n")
				fmt.Fprintf(of, "\tlabel %s\n", l)
			}
			fmt.Fprintf(of, "\tlea %s [%s+%d]\n", dest.ref, addr.ref, off)
			return dest
		}
		base := addr
		// A name-is-address base (file-scope global, or a bytes-allocated
		// stack chunk) can't be the base of a '[name + reg*scale]' SIB
		// form: x86-64 RIP-relative addressing takes a disp32 only, and
		// stack-relative '[rbp + name.off + reg*scale]' isn't a syntax
		// bas exposes. Either way, lea the storage's address into a
		// register first.
		if base.nameIsAddress {
			addrT := vt
			addrT.Indirection++
			baseAddr := newSpot(of, c, c.Temp(), addrT)
			fmt.Fprintf(of, "\tlea %s %s\n", baseAddr.ref, base.ref)
			base = baseAddr
		}
		index := compileTop(of, c, ast.NAST, nullspot)
		scale := vt.Size(c)
		l := c.Label("icheck")
		if !index.t.Same(numASTType()) {
			itmp := newSpot(of, c, c.Temp(), numASTType())
			fmt.Fprintf(of, "\tmovzx %s %s\n", itmp.ref, index.ref)
			fmt.Fprintf(of, "\tcmp %s %s\n", itmp.ref, max.ref)
			itmp.free(of)
		} else {
			fmt.Fprintf(of, "\tcmp %s %s\n", index.ref, max.ref)
		}
		fmt.Fprintf(of, "\tjl %s\n", l)
		if index.t.Same(numASTType()) {
			fmt.Fprintf(of, "\tmov rdi %s\n", index.ref)
		} else {
			fmt.Fprintf(of, "\tmovzx rdi %s\n", index.ref)
		}
		fmt.Fprintf(of, "\tmov rsi %s\n", max.ref)
		fmt.Fprintf(of, "\tcall _init.index_oob\n")
		fmt.Fprintf(of, "\tlabel %s\n", l)
		switch scale {
		case 1, 2, 4, 8:
			fmt.Fprintf(of, "\tlea %s [%s+%s*%d]\n", dest.ref, base.ref, index.ref, scale)
		default:
			// Multi-word element: compute base + index * scale manually.
			fmt.Fprintf(of, "\tmov %s %s\n", dest.ref, index.ref)
			scaleTmp := newSpot(of, c, c.Temp(), numASTType())
			fmt.Fprintf(of, "\tmov %s %d\n", scaleTmp.ref, scale)
			fmt.Fprintf(of, "\timul %s %s\n", dest.ref, scaleTmp.ref)
			scaleTmp.free(of)
			fmt.Fprintf(of, "\tadd %s %s\n", dest.ref, base.ref)
		}
		return dest
	}
	panic(fmt.Sprintf("(LVAL) FALLTHROUGH: %#v\n", a))
}

// compileFunctionBody emits a function's parameter setup, prologue,
// body, and epilogue. It is the flow-bearing core of the *FuncDecl case,
// factored out so it can be run either for real (emitting to the .bs
// writer) or as a standalone borrow analysis (run to a discard writer to
// compute a function's return-alias summary without producing code).
// It assumes c is the function's own (sub)context with retlab pushed; it
// does NOT emit the `function`/`type`/`retaliases` preamble (the caller
// owns that) and does NOT itself compute the alias summary.
func compileFunctionBody(of io.Writer, c *Context, ast *FuncDecl, retlab string) {
	// Collect any memBacked-by-value params that need a prologue
	// spill: argi puts the arg in a (sub-)register, but the body
	// addresses fields via [name+offset] which requires a memory
	// base. For these params we declare a stack slot and receive
	// the value in a temporary register named __arg_<name>, then
	// spill register → stack after the prologue.
	type pendingSpill struct {
		name string
		size int
	}
	var spills []pendingSpill
	for i, a := range ast.Args {
		// memBacked params narrower than a pointer (1/2/4 bytes)
		// need a stack slot for [name+offset] field reads to land
		// somewhere addressable; the arg arrives in a sub-register
		// (DIL/DI/EDI...), which isn't a legal memory base on
		// x86-64. Spill it after the prologue. 8-byte memBacked
		// params already work through the older argi-into-RDI path
		// because RDI is a legal base register, and the existing
		// eviction logic in bas spills it to the reserved stack
		// offset on demand.
		if a.Type.Indirection == 0 && typeIsMemoryBacked(c, a.Type) && a.Type.Size(c) < PTR_SIZE {
			sz := a.Type.Size(c)
			fmt.Fprintf(of, "\tbytes %s %d\n", a.Name, sz)
			fmt.Fprintf(of, "\targi __arg_%s %d %d\n", a.Name, i, sz*8)
			spills = append(spills, pendingSpill{name: a.Name, size: sz})
		} else {
			fmt.Fprintf(of, "\targi %s %d %d\n", a.Name, i, a.Type.Size(c)*8)
		}
		c.BindVar(ast, a.Name, a.Type, a.IsConst)
		if a.Type.Indirection > 0 && !a.Type.HasOwned() {
			c.SetBorrowedBinding(a.Name, true)
		}
		if a.Type.Indirection > 0 {
			c.PointerFlow().AssignPointer(flow.Binding(a.Name), c.PointerFlow().NewObject(flow.Binding(a.Name)))
		} else if a.Type.IsSlice() && !a.Type.HasOwned() {
			// Borrowed slice parameter: register an OriginBorrowed origin
			// so the unified escape predicate (CheckStructFieldEscape and
			// checkSliceEscape) recognizes the binding as having a
			// borrowed root. Mirrors NewLocalOrigin's role for stack-
			// allocated value bindings.
			pexpr := c.PointerFlow().NewBorrowedOrigin(flow.Binding(a.Name))
			c.PointerFlow().AssignPointer(flow.Binding(a.Name), pexpr)
		} else if c.IsInterfaceType(a.Type) && !a.Type.HasOwned() {
			// Borrowed interface parameter: the fat pointer's data word
			// points at caller storage, so the value is a borrowed view
			// exactly like a slice param. Without this seeding,
			// `fn passthrough(g Getter) Getter { return g }` inferred ∅
			// and a local-backed interface laundered through the call.
			pexpr := c.PointerFlow().NewBorrowedOrigin(flow.Binding(a.Name))
			c.PointerFlow().AssignPointer(flow.Binding(a.Name), pexpr)
		} else if a.Type.Indirection == 0 && !a.Type.HasOwned() {
			// By-value struct parameter: seed each slice/pointer field as a
			// borrowed view of caller storage (rooted at the param), so a
			// returned struct param / field is recognized as aliasing the
			// parameter. The tracker modeled nothing here before.
			if _, isStruct := structDeclForType(c, a.Type); isStruct {
				seedStructParamFieldProvenance(c, a.Name, VarFlowPath(a.Name), a.Type)
			}
		}
	}
	fmt.Fprintf(of, "\n\tprologue\n\n")
	// Spill memBacked-by-value param registers into their stack
	// slots. This happens after the prologue so RBP/RSP are set
	// up, and before the body so [name+offset] reads see the
	// expected bits.
	for _, s := range spills {
		switch s.size {
		case 1:
			fmt.Fprintf(of, "\tmov byte[%s] __arg_%s\n", s.name, s.name)
		case 2:
			fmt.Fprintf(of, "\tmov word[%s] __arg_%s\n", s.name, s.name)
		case 4:
			fmt.Fprintf(of, "\tmov dword[%s] __arg_%s\n", s.name, s.name)
		case 8:
			fmt.Fprintf(of, "\tmov qword[%s] __arg_%s\n", s.name, s.name)
		}
		fmt.Fprintf(of, "\tforget __arg_%s\n", s.name)
	}
	if len(spills) > 0 {
		fmt.Fprintf(of, "\n")
	}
	compileTop(of, c, ast.Body, nullspot)
	for _, name := range c.UnconsumedOwned() {
		CompileErrorF(ast, "Owned binding \"%s\" goes out of scope without being consumed; call dispose() or pass it to a consuming function", name)
	}
	if ast.Name == "main" {
		note(of, "\n\t// default return 0 from main\n")
		fmt.Fprintf(of, "\tmov rax 0\n")
	}
	fmt.Fprintf(of, "\n\tlabel %s\n", retlab)
	fmt.Fprintf(of, "\tepilogue\n")
	fmt.Fprintf(of, "\tret\n\n")
}

func compileTop(of io.Writer, c *Context, a AST, dest spot) (spt spot) {
	defer func() {
		if e := recover(); e != nil {
			panic(e)
		}
		// This ensures that every spot ruturned by the compiler
		// contains the type expected to be produced by that AST.
		expect := a.ASTType(c)
		actual := spt.t
		if !expect.Same(actual) {
			if expect.Same(voidASTType()) && spt.empty() {
				// this is ok. void ASTs produce empty spots.
				return
			}
			if expect.Same(intlitASTType()) {
				// intlit expressions are compiled into whatever concrete
				// integer type the context demands.
				return
			}
			if expect.Name == "<nil>" {
				// nil expressions are compiled into whatever nullable pointer
				// type the context demands.
				return
			}
			if !dest.empty() {
				if _, reason := coerceType(c, actual, expect); reason == coerceOK {
					return
				}
			}
			if !dest.empty() && actual.SameRepr(expect) {
				return
			}
			//panic(fmt.Sprintf("Expected type %s but compiler produced %s", expect, actual))
			CompileErrorF(a, "Expected type %s but compiler produced %s", expect, actual)
		}
	}()

	if dest.empty() {
		note(of, "\t// begin %#v\n", a.Note())
	} else {
		note(of, "\t// begin %#v into %v\n", a.Note(), dest.ref)
	}
	defer note(of, "\t// end %#v\n", a.Note())

	// Integer literal expressions are evaluated at compile time and emitted
	// as a single immediate mov into whatever concrete type the context provides.
	if a.ASTType(c).Same(intlitASTType()) {
		val, ok := EvalConst(c, a)
		if !ok {
			CompileErrorF(a, "Could not evaluate integer literal expression at compile time")
		}
		if dest.empty() {
			dest = newSpot(of, c, c.Temp(), numASTType())
		}
		if !litFitsIn(val, c.ResolveUnderlying(dest.t)) {
			CompileErrorF(a, "Integer literal %s does not fit in type %s", val.String(), dest.t.Name)
		}
		fmt.Fprintf(of, "\tmov %s %s\n", dest.ref, val.String())
		return dest
	}

	switch ast := a.(type) {
	case *PreboundSpot:
		return ast.S
	case *TypeAssert:
		return compileTypeAssert(of, c, ast, dest)
	case *TypeSwitch:
		compileTypeSwitch(of, c, ast)
		return nullspot
	case *OwnedPromotion:
		// Compile the inner expression without a typed dest (the inner type differs
		// from the promoted type). Relabel the result with the promoted type.
		// The inner variable is deliberately NOT marked as moved — this is unsafe.
		s := compileTop(of, c, ast.Val, nullspot)
		s.t = ast.ASTType(c)
		if !dest.empty() && !s.same(&dest) {
			move(of, c, dest, s)
			s.free(of)
			dest.t = ast.ASTType(c)
			return dest
		}
		return s
	case *TypeAliasDecl:
		// Emit typealias directive so bas can carry it into the .bo.
		fmt.Fprintf(of, "%stypealias %s %s\n", pubPrefix(ast.IsPub), ast.Name, ast.Underlying)
		emitTypedescFor(of, c, ast.IsPub, ast.Name)
		return nullspot
	case *TypeWithMethodsDecl:
		// Emit typealias directive with method names for cross-package import.
		fmt.Fprintf(of, "%stypealias %s %s", pubPrefix(ast.IsPub), ast.Name, ast.Underlying)
		for _, m := range ast.Methods {
			fmt.Fprintf(of, " %s", m.Name)
		}
		fmt.Fprintf(of, "\n")
		emitTypedescFor(of, c, ast.IsPub, ast.Name)
		// Emit each method as `function TypeName.method_name` — the assembler
		// prepends the package name, producing pkg.TypeName.method_name.
		for _, m := range ast.Methods {
			qualified := &FuncDecl{
				Name:   ast.Name + "." + m.Name,
				Args:   m.Args,
				Return: m.Return,
				Body:   m.Body,
				IsPub:  m.IsPub,
				p:      m.p,
			}
			compileTop(of, c, qualified, nullspot)
		}
		return nullspot
	case *InterfaceDecl:
		// Emit an interface directive so bas can carry the shape into
		// the .bo for cross-package import. Each method's param/return
		// types are rendered via ASTType.String() and reparsed by the
		// importer with parseTypeString.
		fmt.Fprintf(of, "%sinterface %s {\n", pubPrefix(ast.IsPub), ast.Name)
		for _, m := range ast.Methods {
			fmt.Fprintf(of, "\tmethod %s {\n", m.Name)
			for _, p := range m.Params {
				fmt.Fprintf(of, "\t\tparam %s %s\n", p.Name, p.Type.String())
			}
			fmt.Fprintf(of, "\t\treturn %s\n", m.Return.String())
			// Declared borrow contract: one `retaliases <slot>: <idx>...` line
			// per slot that borrows, so the importer reconstructs the same
			// per-slot ReturnAliases (receiver = 0). Empty slots are omitted.
			for slot, params := range m.ReturnAliases {
				if len(params) == 0 {
					continue
				}
				fmt.Fprintf(of, "\t\tretaliases %d:", slot)
				for _, p := range params {
					fmt.Fprintf(of, " %d", p)
				}
				fmt.Fprintf(of, "\n")
			}
			fmt.Fprintf(of, "\t}\n")
		}
		fmt.Fprintf(of, "}\n")
		// Emit the structured iface_desc (assertion-time companion to
		// typedesc method tables). `any` yields method_count == 0.
		emitIfaceDescStructured(of, ast.IsPub, ast.Name, reqEntriesForInterface(c, ast))
		return nullspot
	case *ValuesDecl:
		// Cross-package metadata directive: bas parses this into a
		// ValuesShape on the producer's .bo so the importing bosc can
		// register the values type, its cases, projection signature,
		// and method names. Format mirrors the block-form `interface`
		// directive:
		//   values <Name> {
		//     tag <tag-type>
		//     case <case-name> <tag>
		//     projection <target-type>
		//     method <bare-method-name>
		//   }
		fmt.Fprintf(of, "%svalues %s {\n", pubPrefix(ast.IsPub), ast.Name)
		fmt.Fprintf(of, "\ttag %s\n", ast.TagType.String())
		for _, vc := range ast.Cases {
			fmt.Fprintf(of, "\tcase %s %d\n", vc.Name, vc.Tag)
		}
		for _, pt := range ast.Projections {
			fmt.Fprintf(of, "\tprojection %s\n", pt.String())
		}
		for _, m := range ast.Methods {
			fmt.Fprintf(of, "\tmethod %s\n", m.Name)
		}
		fmt.Fprintf(of, "}\n")
		emitTypedescFor(of, c, ast.IsPub, ast.Name)
		// One static projection table per declared projection. Each
		// table is a fixed array indexed by the case's compiler-private
		// tag, so a projection cast can lower to a single indexed load
		// at runtime. The encoder we use here is the same one that
		// drives ordinary `var t T[N] = [...]` declarations, so
		// arbitrary projection element types (integers, byte[], struct
		// values, &literal) compose without bespoke per-shape encoding.
		for projIdx, projType := range ast.Projections {
			n := len(ast.Cases)
			elements := make([]AST, n)
			for i := range ast.Cases {
				elements[i] = ast.Cases[i].Expr[projIdx]
			}
			arrType := ASTType{Element: &projType, ArraySize: n}
			synthLit := &ArrayLiteral{Elements: elements, p: ast.p}
			data, relocs, err := encodeStaticInit(c, arrType, synthLit)
			if err != nil {
				CompileErrorF(ast, "values type %s: cannot encode projection %s table: %v", ast.Name, projType, err)
			}
			symName := projectionSymbolName(ast.Name, projIdx)
			c.MarkAddress(symName)
			emitVarBlock(of, symName, arrType.String(), data, relocs, false)
			for _, ag := range c.DrainAnonGlobals() {
				c.MarkAddress(ag.Name)
				emitVarBlock(of, ag.Name, ag.Type, ag.Bytes, ag.Relocs, false)
			}
		}
		// Value-receiver methods land alongside the projection tables,
		// using the same emission path TypeWithMethodsDecl uses.
		for _, m := range ast.Methods {
			qualified := &FuncDecl{
				Name:   ast.Name + "." + m.Name,
				Args:   m.Args,
				Return: m.Return,
				Body:   m.Body,
				IsPub:  m.IsPub,
				p:      m.p,
			}
			compileTop(of, c, qualified, nullspot)
		}
		return nullspot
	case *StructDecl:
		c.DefineStruct(ast.TName, ast)
		// Emit the struct shape so bas can carry it into the .bo for
		// cross-package import. Other packages can then declare
		// 'mypkg.Name'-typed values, construct them via 'mypkg.Name{...}',
		// and walk their fields.
		// Method names ride on the opening line after the name (`struct Name
		// m1 m2 {`) so cross-package importers can reconstruct the method
		// set for compile-time method resolution. The method bodies are
		// emitted as functions below; this only records their names.
		fmt.Fprintf(of, "%sstruct %s", pubPrefix(ast.IsPub), ast.TName)
		if methods, ok := c.TypeMethodsFor(ast.TName); ok {
			for _, m := range methods {
				fmt.Fprintf(of, " %s", m.Name)
			}
		}
		fmt.Fprintf(of, " {\n")
		for _, f := range ast.Fields {
			fmt.Fprintf(of, "\t%s %s\n", f.Name, f.Type)
		}
		fmt.Fprintf(of, "}\n")
		emitTypedescFor(of, c, ast.IsPub, ast.TName)
		if methods, ok := c.TypeMethodsFor(ast.TName); ok {
			for _, m := range methods {
				qualified := &FuncDecl{
					Name:   ast.TName + "." + m.Name,
					Args:   m.Args,
					Return: m.Return,
					Body:   m.Body,
					IsPub:  m.IsPub,
					p:      m.p,
				}
				compileTop(of, c, qualified, nullspot)
			}
		}
		return nullspot
	case *VarDecl:
		if c.prebound[ast.Name] {
			// Top-level var: ToAST already bound this in actx for forward
			// references. Consume the marker, then emit a bas-level global
			// instead of the function-local stack/register form.
			delete(c.prebound, ast.Name)
			emitGlobalVarDecl(of, c, a, ast)
			return nullspot
		}
		// Aliasing a whole inconsistent aggregate into this binding
		// (`var alias = f` / `var alias = &f`) is gated centrally at the
		// pointer-flow recording point (checkedAssignPointer, below), not
		// here. A field initializer (`var a = f.a`) is a move, gated
		// elsewhere.
		if ast.Type.Name == "<infer>" {
			if c.parent == nil {
				CompileErrorF(a, "type inference not supported for file-scope variables")
			}
			inferred := ast.Init.ASTType(c)
			switch inferred.Name {
			case "<intlit>":
				inferred = numASTType()
			case "<nil>":
				CompileErrorF(a, "var %s: cannot infer type from nil", ast.Name)
			case "void":
				CompileErrorF(a, "var %s: cannot infer type from void expression", ast.Name)
			}
			ast.Type = inferred
		}
		// Reject single-binding a multi-value return: must use destructuring.
		if ast.Init != nil {
			initT := ast.Init.ASTType(c)
			if initT.MultiReturn {
				CompileErrorF(a, "var %s: cannot bind a multi-value return as a single variable; %s use destructuring: %s",
					ast.Name, multiReturnReturnsClause(ast.Init, initT), multiReturnDestructureTemplate(initT))
			}
		}
		c.BindVar(a, ast.Name, ast.Type, ast.IsConst)
		if c.ResolveUnderlying(ast.Type).Indirection > 0 {
			c.PointerFlow().DeclarePointer(flow.Binding(ast.Name))
		} else if ast.Type.HasOwned() {
			// Owned value-typed bindings (`var fd owned i64 = ...`) get an
			// Origin entry so c.Move(name) has a flow-state slot to
			// invalidate. The binding also stores a self-link in `pointers`
			// so a coercion at `var t i64 = fd` can read fd's PointerExpr
			// (Origin=fd) and stamp the same link on t. Reading t after
			// `take(fd)` then trips CheckDerefValidity at the Symbol read,
			// the same way `*p` would for a borrowed pointer to a moved
			// binding.
			pexpr := c.PointerFlow().NewLocalOrigin(flow.Binding(ast.Name))
			c.PointerFlow().AssignPointer(flow.Binding(ast.Name), pexpr)
		}
		s := newSpot(of, c, ast.Name, ast.Type)
		if ast.Init != nil {
			if sl, ok := ast.Init.(*StructLiteral); ok {
				fillAnonymousLiteralIfNeeded(sl, ast.Type)
			}
			initIsBorrowed := borrowedPointerExpr(c, ast.Init)
			if sl, ok := ast.Init.(*StructLiteral); ok && sameIgnoringOwned(ast.Type, sl.Type) {
				compileStructLiteralInto(of, c, a, sl, s, ast.Type)
				recordStructLiteralFieldFacts(c, VarFlowPath(ast.Name), sl)
				return nullspot
			}
			// Array literal initializer takes its own path: ASTType
			// returns a synthetic <intlit>[N]-shaped type that doesn't
			// satisfy normal assignment compatibility, and the literal needs to be
			// laid out into s element-by-element rather than copied as
			// a single value.
			if al, ok := ast.Init.(*ArrayLiteral); ok {
				compileArrayLiteralInto(of, c, a, s, al)
				return nullspot
			}
			srct := ast.Init.ASTType(c)
			dstt := ast.Type
			if fc, ok := ast.Init.(*Funcall); ok {
				if inferred, ok := newBuiltinTypeForDest(c, fc, dstt); ok {
					srct = inferred
				}
			}
			checkAddressOfOwnedForDest(c, ast.Init, dstt)
			// Interface coercion: concrete pointer or small concrete value → interface fat pointer.
			if shouldCoerceToInterface(c, dstt, srct) {
				emitInterfaceFatPtr(of, c, a, dstt, srct, ast.Init, s.ref, ast.Name)
				if dstt.OwnedMask != 0 {
					markMovedIfOwnedSource(of, c, dstt, ast.Init)
				}
				return nullspot
			}
			// Ownership promotion check, unless source is an integer literal.
			if !srct.Same(intlitASTType()) && srct.Name != "<nil>" {
				gained := dstt.OwnedMask &^ srct.OwnedMask
				if gained != 0 {
					if _, ok := ast.Init.(*OwnedPromotion); !ok {
						CompileErrorF(a, "Ownership promotion requires explicit owned(): initializing %s with %s", dstt, srct)
					}
				}
			}
			// Type compatibility check.
			if _, reason := coerceType(c, dstt, srct); reason != coerceOK {
				reportCoerceFailure(a, dstt, srct, reason,
					"Cannot initialize %s with value of type %s", dstt, srct)
			}
			// Compile the initializer. Same-type / intlit go directly into s;
			// coerced cases go via a temp.
			if srct.Same(dstt) || srct.Same(intlitASTType()) || srct.Name == "<nil>" {
				val := compileTop(of, c, ast.Init, s)
				if !val.same(&s) {
					move(of, c, s, val)
					val.free(of)
				}
			} else {
				val := compileTop(of, c, ast.Init, nullspot)
				move(of, c, s, val)
				val.free(of)
			}
			markMovedIfOwnedSource(of, c, dstt, ast.Init)
			updateNullFactForAssignment(c, VarFlowPath(ast.Name), dstt, ast.Init, srct)
			// Type-aliased pointers (`type myptr *T`) have Indirection==0 on the
			// ASTType for their name; resolve through the alias so they get the
			// same flow-state registration as a bare *T. Slice bindings now
			// inherit borrow-ness via the origin path (AssignPointer just
			// below propagates the source's PointerExpr), so they don't need
			// the borrowed-bindings flag.
			if c.ResolveUnderlying(dstt).Indirection > 0 {
				c.SetBorrowedBinding(ast.Name, initIsBorrowed)
			}
			// Record the source's flow link on the destination. For pointer-
			// typed bindings this is the *p alias relationship; for value-typed
			// non-owned destinations coerced from an owned source it is the
			// value-alias relationship. Either way, c.Move on the linked
			// Origin invalidates this binding's reads via CheckDerefValidity.
			pexpr := pointerExprForAST(c, ast.Init, ast.Name)
			if pexpr.KnownOrigin || pexpr.KnownSlot {
				checkedAssignPointer(c, flow.Binding(ast.Name), pexpr, a)
			}
			// Propagate field pointer facts on struct copy.
			if sym, ok := ast.Init.(*Symbol); ok && !c.IsGlobalBinding(sym.Name) && dstt.Indirection == 0 {
				c.PointerFlow().CopyFieldPointers(flow.Binding(sym.Name), flow.Binding(ast.Name))
			}
			// A struct returned by value from a CALL carries no single Origin;
			// record its borrowed-argument provenance onto the destination's
			// field facts so a later `return b` rejects a local arg via
			// CheckStructFieldEscapeLocal exactly as the direct assignment does.
			recordStructReturnCallFieldFacts(c, ast.Name, dstt, ast.Init)
		} else if !ast.Type.ZeroInitializable(c) {
			CompileErrorF(a, "Variable \"%s\" of type %s requires an initializer", ast.Name, ast.Type)
		} else if ast.Type.Indirection > 0 && ast.Type.NilMask&1 != 0 {
			// `var p *?T` (with or without `owned`) is sugar for `= nil`:
			// zero the storage so runtime reads see nil, and record the
			// static fact. For owned bindings, NullFact=Null is what
			// OwnedObligationLive uses to recognize the slot as carrying
			// no obligation, so no `c.Move()` is needed (and would be
			// wrong — it would forbid later reads through the binding).
			fmt.Fprintf(of, "\tmov %s 0\n", s.ref)
			c.SetNullFact(VarFlowPath(ast.Name), NullKnownNull)
		} else if ast.Type.HasOwned() {
			if _, ok := structDeclForType(c, ast.Type); ok && ast.Type.Indirection == 0 && parentOwnsFields(ast.Type) {
				CompileErrorF(a, "Owned struct binding \"%s\" must have an initializer", ast.Name)
			}
			// Uninitialized owned binding holds no resource; mark consumed so
			// scope exit doesn't complain. No aliases yet, so the Origin
			// invalidation in MoveConsume is a no-op.
			c.MoveConsume(ast.Name)
		}
		return nullspot
	case *MultiBindDecl:
		// Multi-value destructuring bind: var a T1, const b T2 = expr
		// The initializer must have a MultiReturn type. Compile the call (which
		// puts a pointer to the synthetic struct in rax), then copy each field
		// into its own named binding.
		initType := ast.Init.ASTType(c)
		if !initType.MultiReturn {
			CompileErrorF(a, "multi-bind requires a multi-value return expression on the right-hand side")
		}
		if len(ast.Bindings) != len(initType.AnonFields) {
			CompileErrorF(a, "multi-bind: %d bindings but function returns %d values", len(ast.Bindings), len(initType.AnonFields))
		}
		// Compile the initializer into a temporary buffer. For a multi-return
		// call the existing funcall path allocates `bytes Temp_N size`, copies
		// the callee's fields into it, and returns the spot. We read each field
		// out of srcSpot before releasing it.
		srcSpot := compileTop(of, c, ast.Init, nullspot)
		initDecl, _ := structDeclForType(c, initType)
		for i, b := range ast.Bindings {
			field := initType.AnonFields[i]
			bindType := b.Type
			if bindType.Name == "<infer>" {
				bindType = field.Type
			}
			// Reject gaining owned bits without explicit owned().
			gained := bindType.OwnedMask &^ field.Type.OwnedMask
			if gained != 0 {
				CompileErrorF(a, "multi-bind %s: ownership promotion requires explicit owned()", b.Name)
			}
			if _, reason := coerceType(c, bindType, field.Type); reason != coerceOK {
				reportCoerceFailure(a, bindType, field.Type, reason,
					"multi-bind %s: cannot assign %s to %s", b.Name, field.Type, bindType)
			}
			_, rebind := c.bindings[b.Name]
			if rebind {
				// Name already declared in this scope — re-binding, not a new declaration.
				// Apply the same writability check as regular assignment.
				if c.IsConst(b.Name) {
					CompileErrorF(a, "Cannot assign to const binding %q", b.Name)
				}
			} else {
				c.BindVar(a, b.Name, bindType, b.IsConst)
				if c.ResolveUnderlying(bindType).Indirection > 0 {
					c.PointerFlow().DeclarePointer(flow.Binding(b.Name))
				} else if bindType.HasOwned() {
					pexpr := c.PointerFlow().NewLocalOrigin(flow.Binding(b.Name))
					c.PointerFlow().AssignPointer(flow.Binding(b.Name), pexpr)
				}
			}
			// Propagate the callee's per-slot alias provenance onto the
			// destructured binding: slot i of a multi-return call may alias
			// the call's arguments per the callee's summary, and dropping
			// that fact here let `return v` escape a local through the
			// destructuring uncaught.
			recordMultiReturnSlotProvenance(c, b.Name, bindType, ast.Init, i)
			offset, _ := initDecl.ByteOffset(c, field.Name)
			if rebind {
				existingType := c.bindings[b.Name]
				s := spot{ref: b.Name, t: existingType, nameIsAddress: typeIsMemoryBacked(c, existingType)}
				if s.nameIsAddress {
					tmp := c.Temp()
					fmt.Fprintf(of, "\tlocal %s 64\n", tmp)
					sz := existingType.Size(c)
					for off := 0; off < sz; off += 8 {
						fmt.Fprintf(of, "\tmov %s [%s+%d]\n", tmp, srcSpot.ref, offset+off)
						fmt.Fprintf(of, "\tmov [%s+%d] %s\n", b.Name, off, tmp)
					}
					fmt.Fprintf(of, "\tforget %s\n", tmp)
				} else {
					fmt.Fprintf(of, "\tmov %s [%s+%d]\n", b.Name, srcSpot.ref, offset)
				}
			} else {
				s := newSpot(of, c, b.Name, bindType)
				if s.nameIsAddress {
					// Memory-backed binding: copy field bytes from srcSpot[offset].
					tmp := c.Temp()
					fmt.Fprintf(of, "\tlocal %s 64\n", tmp)
					sz := bindType.Size(c)
					for off := 0; off < sz; off += 8 {
						fmt.Fprintf(of, "\tmov %s [%s+%d]\n", tmp, srcSpot.ref, offset+off)
						fmt.Fprintf(of, "\tmov [%s+%d] %s\n", b.Name, off, tmp)
					}
					fmt.Fprintf(of, "\tforget %s\n", tmp)
				} else {
					fmt.Fprintf(of, "\tmov %s [%s+%d]\n", b.Name, srcSpot.ref, offset)
				}
			}
		}
		srcSpot.free(of)
		return nullspot
	case *MultiAssign:
		// Multi-value re-assignment to pre-declared lvalues: a, b = expr.
		initType := ast.Init.ASTType(c)
		if !initType.MultiReturn {
			CompileErrorF(a, "multi-assignment requires a multi-value expression on the right-hand side")
		}
		if len(ast.Targets) != len(initType.AnonFields) {
			CompileErrorF(a, "multi-assignment: %d targets but right-hand side yields %d values", len(ast.Targets), len(initType.AnonFields))
		}
		srcSpot := compileTop(of, c, ast.Init, nullspot)
		initDecl, _ := structDeclForType(c, initType)
		for i, tgt := range ast.Targets {
			field := initType.AnonFields[i]
			sym, ok := tgt.(*Symbol)
			if !ok {
				CompileErrorF(tgt, "multi-assignment target must be a simple variable name")
			}
			declType, declared := c.DeclaredTypeForVar(sym.Name)
			if !declared {
				CompileErrorF(tgt, "Variable %q undeclared.", sym.Name)
			}
			if c.IsConst(sym.Name) {
				CompileErrorF(tgt, "Cannot assign to const binding %q", sym.Name)
			}
			if _, reason := coerceType(c, declType, field.Type); reason != coerceOK {
				reportCoerceFailure(tgt, declType, field.Type, reason,
					"multi-assignment to %s: cannot assign %s to %s", sym.Name, field.Type, declType)
			}
			offset, _ := initDecl.ByteOffset(c, field.Name)
			dst := spot{ref: sym.Name, t: declType, nameIsAddress: typeIsMemoryBacked(c, declType)}
			if dst.nameIsAddress {
				tmp := c.Temp()
				fmt.Fprintf(of, "\tlocal %s 64\n", tmp)
				sz := declType.Size(c)
				for off := 0; off < sz; off += 8 {
					fmt.Fprintf(of, "\tmov %s [%s+%d]\n", tmp, srcSpot.ref, offset+off)
					fmt.Fprintf(of, "\tmov [%s+%d] %s\n", sym.Name, off, tmp)
				}
				fmt.Fprintf(of, "\tforget %s\n", tmp)
			} else {
				fmt.Fprintf(of, "\tmov %s [%s+%d]\n", sym.Name, srcSpot.ref, offset)
			}
			// Keep flow facts conservative: a freshly-assigned pointer is
			// treated as a new local origin so later reads don't trip stale
			// deref checks on a value we just produced from an assertion.
			if c.ResolveUnderlying(declType).Indirection > 0 {
				pexpr := c.PointerFlow().NewLocalOrigin(flow.Binding(sym.Name))
				c.PointerFlow().AssignPointer(flow.Binding(sym.Name), pexpr)
			}
			// Then override with the callee's real per-slot alias provenance
			// when the RHS is a resolvable call: slot i may alias the call's
			// arguments per the callee's summary, and dropping the fact let
			// a destructured borrowed/local view escape uncaught.
			recordMultiReturnSlotProvenance(c, sym.Name, declType, ast.Init, i)
		}
		srcSpot.free(of)
		return nullspot
	case *FuncDecl:
		c := c.SubContext()
		defer c.ForgetPointerBindings()
		retlab := c.PushRetlabel(ast.Return)
		defer c.PopRetlabel()
		fmt.Fprintf(of, "%sfunction %s\n", pubPrefix(ast.IsPub), ast.Name)
		// Emit a 'type fn(...) ret' directive so cross-package importers
		// (Context.Import → parseFuncType) can read this function's
		// signature back out of the .bo. Render args and return via
		// ASTType.String(); the parser eats the same forms.
		var sig strings.Builder
		sig.WriteString("fn(")
		for i, a := range ast.Args {
			if i > 0 {
				sig.WriteString(", ")
			}
			// Preserve the variadic marker across packages: a `...T`
			// parameter desugars to `args T[]` in ast.Args, but importers
			// must see it as variadic so zero-or-more trailing args at the
			// call site type-check. Render the element type prefixed with
			// `...` (the param's Type is the slice T[]; emit ...<element>).
			if a.Variadic && a.Type.IsSlice() && a.Type.Element != nil {
				sig.WriteString("...")
				sig.WriteString(a.Type.Element.String())
			} else {
				sig.WriteString(a.Type.String())
			}
		}
		sig.WriteString(") ")
		sig.WriteString(ast.Return.String())
		fmt.Fprintf(of, "\ttype %s\n", sig.String())
		// Infer this function's return-parameter alias set (demand-driven,
		// memoized, cycle-safe) and emit a `retaliases` directive per
		// non-empty slot so the fact travels through the .bo to
		// cross-package callers. The inference also rejects any OriginLocal
		// escape, so a local reaching a returned slot is caught here as
		// well as at the per-return checks below.
		for slot, params := range aliasSet(c, ast) {
			if len(params) == 0 {
				continue
			}
			fmt.Fprintf(of, "\tretaliases %d:", slot)
			for _, p := range params {
				fmt.Fprintf(of, " %d", p)
			}
			fmt.Fprintf(of, "\n")
		}
		compileFunctionBody(of, c, ast, retlab)
		return nullspot
	case *Block:
		sc := c.SubContext()
		for _, st := range ast.Body {
			note(of, "\n")
			s := compileTop(of, sc, st, nullspot)
			s.free(of)
			if !fallsThrough(st) {
				break
			}
		}
		if fallsThrough(ast) {
			// Scope-exit: every owned binding declared in this block must be consumed.
			for _, name := range sc.UnconsumedOwned() {
				CompileErrorF(a, "Owned binding \"%s\" goes out of scope without being consumed; call dispose() or pass it to a consuming function", name)
			}
		}
		sc.InvalidateLocalOriginsForScope()
		sc.ForgetPointerBindings()
		sc.FreeLocalVars(of)
		return nullspot
	case *Funcall:
		pkg, fname := ast.PkgAndName()
		if pkg == "" && fname == "alloc" {
			return compileAllocBuiltin(of, c, a, ast, dest)
		}
		if pkg == "" && fname == "new" {
			return compileNewBuiltin(of, c, a, ast, dest)
		}
		if pkg == "" && fname == "free" {
			return compileFreeBuiltin(of, c, a, ast)
		}
		if pkg == "" && fname == "len" {
			if len(ast.Args) != 1 {
				CompileErrorF(a, "len() requires exactly one argument")
			}
			v := compileTop(of, c, ast.Args[0], nullspot)
			if !v.t.IsSliceOrArray() {
				CompileErrorF(a, "len() argument must be a slice or array, got %s", v.t)
			}
			if dest.empty() {
				dest = newSpot(of, c, c.Temp(), numASTType())
			}
			if v.t.IsArray() {
				fmt.Fprintf(of, "\tmov %s %d\n", dest.ref, v.t.ArraySize)
			} else {
				fmt.Fprintf(of, "\tmov %s [%s+8]\n", dest.ref, v.ref)
			}
			v.free(of)
			return dest
		}
		// Cast expression: type name used as a single-argument function.
		// Works for unqualified names (FD(x)) and qualified names (io.FD(x)).
		//
		// When a function of the same bare name exists, the call form
		// wins. The proposal's package-level namespace rule wants only
		// one declaration per name, but checkPackageNameCollision
		// currently scopes the rejection to type-shaped decls (struct,
		// typealias, interface, values). A user who declares both a
		// `type pair struct {...}` and a `fn pair(n) i64` gets two
		// entries, and Stage 4's TypeByName(struct) recognition would
		// otherwise silently steer every `pair(x)` site into the cast
		// path — even when assignment context (`var n i64 = pair(x)`)
		// makes it unambiguous that the call was meant.
		{
			castName := ast.QualifiedName()
			if destType, ok := c.TypeByName(castName); ok {
				if _, _, hasFn := c.FuncDeclForCall(pkg, fname); !hasFn {
					if len(ast.Args) != 1 {
						CompileErrorF(a, "Type cast %s() requires exactly one argument", castName)
					}
					return compileCast(of, c, ast.Args[0], destType, dest)
				}
			}
		}

		// (expr).method(args) — receiver is an arbitrary expression.
		// Evaluate it, look up the method by type, and delegate to the
		// concrete method call path.
		if recv := ast.ReceiverExpr(); recv != nil {
			rt := recv.ASTType(c)
			typeName := rt.Name
			if c.IsInterfaceType(rt) {
				CompileErrorF(a, "interface method dispatch on expression receiver not yet supported; bind to a variable first")
			}
			if method, mok := c.MethodForType(typeName, fname); mok {
				return compileConcreteMethodCall(of, c, a, ast, rt, typeName, method, dest)
			}
			CompileErrorF(a, "no method %q on type %s", fname, typeName)
		}

		decl, resolvedPkg, ok := c.FuncDeclForCall(pkg, fname)
		if !ok {
			// Fall back to an indirect call through a function-pointer
			// value. Two shapes:
			//
			//   foo(args)    — pkg == "", call through a fn-typed
			//                  local/global named `foo`
			//   d.f(args)    — pkg == "d" (parser packs Dot-of-call
			//                  this way regardless of whether `d` is a
			//                  package or a struct-valued variable);
			//                  call through field `f` of struct `d`
			if pkg == "" {
				if vt, vok := c.TypeForVar(fname); vok && vt.FuncSig != nil {
					return compileIndirectCall(of, c, a, ast, fname, vt.FuncSig, dest)
				}
			} else {
				if vt, vok := c.TypeForVar(pkg); vok {
					// Interface method dispatch: v.method(args) where v is an interface type.
					if c.IsInterfaceType(vt) {
						ifaceDecl, _ := c.InterfaceForName(vt.Name)
						return compileInterfaceMethodCall(of, c, a, ast, vt, ifaceDecl, dest)
					}
					// Concrete method call: v.method(args) → TypeName.method(receiver, args).
					typeName := vt.Name // leaf type name regardless of pointer depth
					if method, mok := c.MethodForType(typeName, fname); mok {
						if len(method.Args) == 0 {
							CompileErrorF(a, "%s.%s is a static method (no receiver); call as %s.%s(...), not %s.%s(...)",
								typeName, fname, typeName, fname, pkg, fname)
						}
						return compileConcreteMethodCall(of, c, a, ast, vt, typeName, method, dest)
					}
					// Struct function-pointer field call.
					if sdecl, sok := structDeclForType(c, vt); sok {
						off, mtype := sdecl.ByteOffset(c, fname)
						if mtype.FuncSig != nil {
							baseAddr := compileTop(of, c, &Symbol{Name: pkg}, nullspot)
							srcRef := fmt.Sprintf("[%s+%d]", baseAddr.ref, off)
							ret := compileIndirectCall(of, c, a, ast, srcRef, mtype.FuncSig, dest)
							baseAddr.free(of)
							return ret
						}
					}
				}
			}
			CompileErrorF(a, "No such function \"%v\"", ast.QualifiedName())
		}
		if decl.Variadic {
			ast = desugarVariadicCall(of, c, a, ast, decl)
		} else if len(ast.Args) != len(decl.Args) {
			CompileErrorF(a, "%s expected %d arguments, but was called with %d",
				ast.QualifiedName(), len(decl.Args), len(ast.Args))
		}

		argorder := setupArgs(of, c, ast, decl)

		retType := ast.ASTType(c)
		raxName := raxForType(retType)
		note(of, "\t// acquire %s for call return\n", raxName)
		rax := regSpot(of, raxName)
		rax.t = retType
		// Always emit a fully-qualified call name so the linker sees
		// unambiguous symbols. Cross-package calls use the imported
		// package's name; in-package calls use the current package's name.
		callPkg := resolvedPkg
		if callPkg == "" {
			callPkg = c.Pkgname()
		}
		fmt.Fprintf(of, "\tcall %s.%s\n", callPkg, decl.Name)
		for i := 0; i < len(ast.Args); i++ {
			note(of, "\t// release call registers\n")
			fmt.Fprintf(of, "\trelease %s\n", argorder[i])
		}
		ret := nullspot
		if !decl.Return.Same(voidASTType()) {
			if !dest.empty() {
				ret = dest
			} else {
				ret = newSpot(of, c, c.Temp(), decl.Return)
			}
			move(of, c, ret, rax)
		}
		note(of, "\t// free rax for call return\n")
		rax.t = regtype
		rax.free(of)
		return ret
	case *Dot:
		// Route the selector through the shared resolver. Cross-package
		// variable reads land as ResolvedRuntimeValue with Pkg set; struct
		// field access lands as ResolvedStructField; values case references
		// land as ResolvedValuesCase. Everything else (qualified
		// call-position selectors, etc.) is handled by other compileTop
		// cases and never reaches here.
		{
			// A field whose value was moved out must not be read: its slot
			// was zeroed on move-out, and a non-null field type carries no
			// nil check, so a deref would crash. Reads during the move-out
			// itself and during free happen before the field is marked
			// consumed, so they do not trip this. Same message as a local
			// use-after-move.
			if path, ok := FlowPathForExpr(ast); ok && c.OwnedFieldConsumed(path) {
				CompileErrorF(ast, "Use of %q after it was moved", path.Key())
			}
			r := ResolveSelector(c, ast)
			if r.Kind == ResolvedValuesCase {
				// Emit the case's private tag as a value of the values
				// type. The tag is an i64 in v1.
				if dest.empty() {
					dest = newSpot(of, c, c.Temp(), r.Type)
				}
				fmt.Fprintf(of, "\tmov %s %d\n", dest.ref, r.Case.Tag)
				return dest
			}
			if r.Kind == ResolvedRuntimeValue && r.Pkg != "" {
				// Cross-package variable read: bas treats `pkg.varname`
				// as an external data symbol (isIdentifier accepts
				// dotted names) and the linker resolves the relocation
				// to the producer package's data section.
				ref := r.Name
				if dest.empty() {
					dest = newSpot(of, c, c.Temp(), r.Type)
				}
				fmt.Fprintf(of, "\tmov %s %s\n", dest.ref, ref)
				return dest
			}
			if r.Kind == ResolvedUnknown {
				if sym, ok := ast.Val.(*Symbol); ok && c.IsImportedPackage(sym.Name) {
					CompileErrorF(a, "package %q has no variable %q", sym.Name, ast.Member)
				}
			}
		}
		l := compileTop(of, c, ast.Val, nullspot)
		orig := l
		l = materializePointerValue(of, c, l)
		if !l.same(&orig) {
			defer l.free(of)
		}
		def, ok := structDeclForType(c, l.t)
		if !ok {
			CompileErrorF(a, "No such structure \"%v\"", l.t)
		}
		offset, declaredType := def.ByteOffset(c, ast.Member)
		mtype := fieldTypeForBase(l.t, declaredType)
		if dest.empty() {
			dest = newSpot(of, c, c.Temp(), mtype)
		}
		if typeIsMemoryBacked(c, mtype) {
			// The member is memory-backed (a struct, or any value
			// larger than 8 bytes). Keep a pointer to it rather than
			// loading the bytes, so a subsequent Dot/Index can walk
			// further. Without this, a small inner struct's value
			// would end up in a register and get dereferenced as if
			// it were a pointer on the next step.
			addrType := mtype
			addrType.Indirection++
			addr := newSpot(of, c, c.Temp(), addrType)
			fmt.Fprintf(of, "\tlea %s [%s+%d]\n", addr.ref, l.ref, offset)
			_, memberIsStruct := structDeclForType(c, mtype)
			if memberIsStruct && mtype.Size(c) <= 8 {
				// Small struct: callers expect to keep walking through
				// this address. Return the lea'd address directly with
				// the struct's type, instead of memcpy-ing the bytes
				// into a scalar dest. The spot holds an address in a
				// register, so it stays name-is-value — subsequent
				// indirect addressing through it uses register-relative
				// SIB without further materialization.
				addr.t = mtype
				return addr
			}
			spot_memcpy(of, c, dest, addr, mtype.Size(c))
			addr.free(of)
		} else {
			fmt.Fprintf(of, "\tmov %s [%s+%d]\n", dest.ref, l.ref, offset)
		}
		return dest
	case *Deref:
		{
			ptr := pointerExprForAST(c, ast.Val, "")
			if ok, reason := c.PointerFlow().CheckDerefValidity(ptr); !ok {
				CompileErrorF(a, "%s", reason)
			}
		}
		v := compileTop(of, c, ast.Val, nullspot)
		t := v.t
		if t.Indirection == 0 {
			CompileErrorF(a, "Cannot dereference non-pointer type %s", t)
		}
		t.Indirection -= 1
		t.MutMask >>= 1 // consume the outermost pointer level's mut bit
		t.NilMask >>= 1
		if t.Indirection == 0 && !t.IsSliceOrArray() {
			t.MutMask = 0 // plain value: MutMask is not meaningful
		}

		// When the pointer's name is an address (file-scope global, or
		// a bytes-allocated stack slot), [v.ref] would only do one
		// indirection — reading the pointer's stored value from its
		// storage. We need a second load to actually follow the
		// pointer. Materialize the pointer's value into a register
		// first; subsequent [reg] is the real dereference. For
		// register-resident pointers (the local-allocated case),
		// [v.ref] already does the one-step deref the caller wants.
		src := v.ref
		if v.nameIsAddress {
			ptmp := materializePointerValue(of, c, v)
			src = ptmp.ref
			defer ptmp.free(of)
		}

		if t.Indirection > 0 {
			if dest.empty() {
				fmt.Fprintf(of, "\t// New temp for deref, type: %#v\n", t)
				dest = newSpot(of, c, c.Temp(), t)
			}
			// it's a pointer. Just copy.
			fmt.Fprintf(of, "\tmov %s [%s]\n", dest.ref, src)
		} else if t.ArraySize > 0 {
			// it's an array.
			panic("Array copying not implemented yet\n")
		} else if t.Size(c) > 8 {
			// It's a large object, meaning we need to pass it by pointer.
			v.t.Indirection -= 1
			return v
		} else {
			if dest.empty() {
				fmt.Fprintf(of, "\t// New temp for deref, type: %#v\n", t)
				dest = newSpot(of, c, c.Temp(), t)
			}
			// It's a small object. Just copy it.
			fmt.Fprintf(of, "\tmov %s [%s]\n", dest.ref, src)
		}

		return dest
	case *Address:
		// Two named-target forms reduce to lea of a symbol: the
		// Var-shape (legacy AST) and Lit=*Symbol (parser produces this
		// when '&' is followed by a bare identifier). Other Lit shapes
		// either delegate to compileLval (which already knows how to
		// compute element / field addresses) or are rejected (no
		// stable storage for them at runtime).
		name, hasName := ast.NamedTarget()
		if hasName {
			// Taking the address of an inconsistent aggregate would create an
			// alias that could read its moved-out non-null field.
			checkAggregateNotAliasedWhileInconsistent(c, name, ast)
		}
		if !hasName {
			// Function-scope `&"literal"`: the only address-of-literal form
			// permitted at runtime. Materialize (once per distinct literal) a
			// static 16-byte slice header {ptr -> bytes, len} and lea its
			// address. The result is a pointer to a byte slice — stable,
			// read-only, never-dies storage. Other `&literal` forms remain
			// rejected by the default branch below.
			if lit, ok := ast.Lit.(*Literal); ok {
				if s, isStr := lit.Val.(string); isStr {
					sym := c.StringSliceHeader(s)
					c.MarkAddress(sym)
					if dest.empty() {
						dest = newSpot(of, c, c.Temp(), ast.ASTType(c))
					}
					fmt.Fprintf(of, "\tlea %s %s\n", dest.ref, sym)
					return dest
				}
			}
			switch ast.Lit.(type) {
			case *Index, *Dot:
				if path, ok := FlowPathForExpr(ast.Lit); ok {
					c.MarkAddress(path.Root)
				}
				// compileLval produces a register holding the address
				// of an element/field, with type bumped by one
				// Indirection — exactly what '&expr' is supposed to
				// yield. Forward its result directly.
				lvspot := compileLval(of, c, ast.Lit, nullspot)
				if dest.empty() {
					return lvspot
				}
				move(of, c, dest, lvspot)
				return dest
			default:
				CompileErrorF(a, "cannot take the address of this expression at runtime; address-of-literal is only valid in static initializers")
			}
		}
		if dest.empty() {
			dest = newSpot(of, c, c.Temp(), ast.ASTType(c))
		}
		// Function symbols: emit a fully-qualified lea — the linker
		// resolves them like any other relocation target. No volatile
		// needed (functions live at fixed addresses).
		if _, isFn := c.FuncDeclForName(name); isFn {
			pkg := c.Pkgname()
			fmt.Fprintf(of, "\tlea %s %s.%s\n", dest.ref, pkg, name)
			return dest
		}
		// Validate the value-alias link before producing the pointer.
		// `&t` on a binding whose origin chain has been invalidated
		// (e.g. t aliases `i` and `take(i)` already ran) creates a way
		// to observe stale storage. Catch here rather than waiting for
		// a downstream deref, which may never happen if the pointer
		// crosses a function boundary into opaque callee scope. Skipped
		// for pointer-typed bindings; their deref sites still own the
		// validity check.
		if t, ok := c.TypeForVar(name); ok && c.ResolveUnderlying(t).Indirection == 0 {
			ptr := c.PointerFlow().Pointer(flow.Binding(name))
			if ptr.KnownOrigin {
				if origin := string(ptr.Origin); origin != "" {
					if ok, _ := c.PointerFlow().CheckDerefValidity(ptr); !ok {
						if origin == name {
							CompileErrorF(a, "cannot take address of \"%s\": it was consumed", name)
						} else {
							CompileErrorF(a, "cannot take address of \"%s\": its alias source \"%s\" was consumed", name, origin)
						}
					}
				}
			}
		}
		// For register-resident locals, force-spill to memory so the
		// taken address is stable; for memory-backed names (globals,
		// bytes-allocated locals) the address is already stable and
		// the volatile directive would be a no-op or unrecognized.
		if !c.NameIsAddress(name) {
			fmt.Fprintf(of, "\tvolatile %s\n", name)
		}
		c.MarkAddress(name)
		fmt.Fprintf(of, "\tlea %s %s\n", dest.ref, name)
		return dest
	case *Index:
		vt := ast.ASTType(c)
		w := compileTop(of, c, ast.Val, nullspot)
		max := newSpot(of, c, c.Temp(), numASTType())
		var addr spot
		if w.t.IsSlice() {
			t := vt
			t.Indirection += 1
			addr = newSpot(of, c, c.Temp(), t)
			fmt.Fprintf(of, "\tmov %s [%s]\n", addr.ref, w.ref)
			fmt.Fprintf(of, "\tmov %s [%s+8]\n", max.ref, w.ref)
		} else {
			addr = w
			fmt.Fprintf(of, "\tmov %s %d\n", max.ref, w.t.ArraySize)
		}

		if dest.empty() {
			dest = newSpot(of, c, c.Temp(), vt)
		}
		if ast.NAST == nil {
			off := uint64(vt.Size(c)) * ast.N
			if !w.t.IsSlice() {
				if ast.N >= uint64(w.t.ArraySize) {
					CompileErrorF(a, "Index %d greater than max for array of size %d", ast.N, w.t.ArraySize)
				}
			} else {
				l := c.Label("icheck")
				fmt.Fprintf(of, "\tcmp %s %d\n", max.ref, ast.N)
				fmt.Fprintf(of, "\tjg %s\n", l)
				fmt.Fprintf(of, "\tmov rdi %d\n", ast.N)
				fmt.Fprintf(of, "\tmov rsi %s\n", max.ref)
				fmt.Fprintf(of, "\tcall _init.index_oob\n")
				fmt.Fprintf(of, "\tlabel %s\n", l)
			}
			if vt.Size(c) > 8 {
				// Multi-word element (struct, slice, etc.): memcpy via a
				// computed pointer.
				elemAddrT := vt
				elemAddrT.Indirection++
				elemAddr := newSpot(of, c, c.Temp(), elemAddrT)
				fmt.Fprintf(of, "\tlea %s [%s+%d]\n", elemAddr.ref, addr.ref, off)
				spot_memcpy(of, c, dest, elemAddr, vt.Size(c))
				elemAddr.free(of)
			} else {
				fmt.Fprintf(of, "\tmov %s [%s+%d]\n", dest.ref, addr.ref, off)
			}
			return dest
		}
		// Bounds check + index normalization. Common to both small- and
		// multi-word element paths.
		base := addr
		// A name-is-address base (file-scope global, bytes-allocated
		// chunk) can't be the base of a '[name + reg*scale]' SIB form;
		// lea its address into a register first.
		if base.nameIsAddress {
			addrT := vt
			addrT.Indirection++
			baseAddr := newSpot(of, c, c.Temp(), addrT)
			fmt.Fprintf(of, "\tlea %s %s\n", baseAddr.ref, base.ref)
			base = baseAddr
		}
		index := compileTop(of, c, ast.NAST, nullspot)
		l := c.Label("icheck")
		if !index.t.Same(numASTType()) {
			itmp := newSpot(of, c, c.Temp(), numASTType())
			fmt.Fprintf(of, "\tmovzx %s %s\n", itmp.ref, index.ref)
			fmt.Fprintf(of, "\tcmp %s %s\n", itmp.ref, max.ref)
			itmp.free(of)
		} else {
			fmt.Fprintf(of, "\tcmp %s %s\n", index.ref, max.ref)
		}
		fmt.Fprintf(of, "\tjl %s\n", l)
		if index.t.Same(numASTType()) {
			fmt.Fprintf(of, "\tmov rdi %s\n", index.ref)
		} else {
			fmt.Fprintf(of, "\tmovzx rdi %s\n", index.ref)
		}
		fmt.Fprintf(of, "\tmov rsi %s\n", max.ref)
		fmt.Fprintf(of, "\tcall _init.index_oob\n")
		fmt.Fprintf(of, "\tlabel %s\n", l)

		scale := vt.Size(c)
		switch scale {
		case 1, 2, 4, 8:
			// x86 base+index*scale addressing handles these directly.
			fmt.Fprintf(of, "\tmov %s [%s+%s*%d]\n", dest.ref, base.ref, index.ref, scale)
		default:
			// Multi-word element: compute &elem and memcpy.
			elemAddrT := vt
			elemAddrT.Indirection++
			elemAddr := newSpot(of, c, c.Temp(), elemAddrT)
			// elemAddr = index * scale + base.
			fmt.Fprintf(of, "\tmov %s %s\n", elemAddr.ref, index.ref)
			scaleTmp := newSpot(of, c, c.Temp(), numASTType())
			fmt.Fprintf(of, "\tmov %s %d\n", scaleTmp.ref, scale)
			fmt.Fprintf(of, "\timul %s %s\n", elemAddr.ref, scaleTmp.ref)
			scaleTmp.free(of)
			fmt.Fprintf(of, "\tadd %s %s\n", elemAddr.ref, base.ref)
			spot_memcpy(of, c, dest, elemAddr, scale)
			elemAddr.free(of)
		}
		return dest
	case *SliceOp:
		// TODO: dest optimization
		v := compileTop(of, c, ast.Val, nullspot)
		if !v.t.IsSliceOrArray() {
			panic("Somehow slicing a non-array, non-slice")
		}
		// baset is the element type; newt is the resulting slice type.
		baset := *v.t.Element
		elem := baset
		newt := ASTType{Element: &elem}
		if v.t.IsSlice() {
			newt.MutMask = v.t.MutMask
		} else if isLvalueMutable(c, ast.Val) {
			newt.MutMask = 1 << 1
		}
		var addr spot
		if v.t.IsSlice() {
			addrt := baset
			addrt.Indirection += 1
			addr = newSpot(of, c, c.Temp(), addrt)
			fmt.Fprintf(of, "\tmov %s [%s]\n", addr.ref, v.ref)
		} else {
			// Fixed array: materialize the array's address into a fresh
			// register temp via `lea`. Reusing the bare array name as
			// `addr.ref` worked for `bytes`-allocated locals (bas treats
			// the name as its address) but not for `var`-declared globals
			// (bas resolves the name to its contents).
			addrt := baset
			addrt.Indirection++
			addr = newSpot(of, c, c.Temp(), addrt)
			fmt.Fprintf(of, "\tlea %s [%s+0]\n", addr.ref, v.ref)
		}

		var upper spot
		if ast.Upper != nil {
			upper = compileTop(of, c, ast.Upper, nullspot)
		} else {
			upper = newSpot(of, c, c.Temp(), numASTType())
			if v.t.IsSlice() {
				fmt.Fprintf(of, "\tmov %s [%s+8]\n", upper.ref, v.ref)
			} else {
				// v.t is a fixed array; use its declared size.
				fmt.Fprintf(of, "\tmov %s %d\n", upper.ref, v.t.ArraySize)
			}
		}

		if ast.Lower != nil {
			lower := compileTop(of, c, ast.Lower, nullspot)
			if baset.Size(c) > 8 {
				panic("Slicing for types > 8 bytes not implemented.")
			}
			fmt.Fprintf(of, "\tlea %s [%s+%s*%d]\n", addr.ref, addr.ref, lower.ref, baset.Size(c))
			fmt.Fprintf(of, "\tsub %s %s\n", upper.ref, lower.ref)
			lower.free(of)
		}
		newslice := newSpot(of, c, c.Temp(), newt)
		fmt.Fprintf(of, "\tmov [%s] %s\n", newslice.ref, addr.ref)
		fmt.Fprintf(of, "\tmov [%s+8] %s\n", newslice.ref, upper.ref)
		return newslice

	case *Assignment:
		// Copying a whole inconsistent aggregate (`alias = f` / `alias = &f`)
		// forms a second handle that could read the moved-out field. Field
		// RHS (`x = f.a`) is a move, handled separately and not gated here.
		if name := aggregateBindingName(ast.Val); name != "" {
			checkAggregateNotAliasedWhileInconsistent(c, name, a)
		}
		dstt := ast.Target.ASTType(c)
		targetSym, targetIsSymbol := ast.Target.(*Symbol)
		if targetIsSymbol {
			var ok bool
			dstt, ok = c.DeclaredTypeForVar(targetSym.Name)
			if !ok {
				CompileErrorF(ast.Target, "Variable \"%s\" undeclared.", targetSym.Name)
			}
		}
		// Mutability gate: walk the lval path and reject any link that
		// blocks the write. Covers const-bound roots, non-mut pointer
		// auto-deref through Dot/Index, and explicit *p through a *T.
		if ok, reason := lvalueIsWritable(c, ast.Target); !ok {
			CompileErrorF(a, "%s", reason)
		}
		if targetIsSymbol {
			if c.OwnedObligationLive(targetSym.Name, dstt) {
				CompileErrorF(a, "Cannot assign to owned binding \"%s\" before consuming its current value", targetSym.Name)
			} else if dstt.HasOwned() && c.IsMoved(targetSym.Name) && c.HasLiveOwnedAlias(targetSym.Name) {
				// Re-init of a previously-consumed owned binding starts
				// a fresh lifecycle. It is rejected when any live owned
				// pointer could still observe or consume the binding's
				// storage; once those aliases are themselves consumed
				// the re-init is allowed.
				CompileErrorF(a, "Cannot re-initialize \"%s\": a live owned pointer still references its storage", targetSym.Name)
			}
		}
		// Deref-target obligation check: when overwriting *p and the
		// pointee carries owned, follow pointer flow to the binding the
		// pointer references and reject if it still holds a live
		// obligation. This is the indirect equivalent of the Symbol
		// check above. Both pointer-flow shapes count: `NewLocalOrigin`
		// (pointer to a value binding's storage) and
		// `AddressOfPointerSlot` (pointer to a pointer binding's slot).
		// Deref-target obligation check: when overwriting `*p` and the
		// pointee carries owned, the pointer `p` itself holds the
		// obligation (it transferred to `p` at the address-take or by
		// later moves). Reject if `p` still owes the obligation. This
		// is the indirect equivalent of the Symbol check above.
		//
		// For non-trivial pointer expressions (anything that isn't a
		// bare Symbol — e.g. *p.field, **pp), the obligation lives at
		// some node we don't have a direct query for, so reject
		// conservatively. Phase B can sharpen this with pointer-flow.
		if deref, isDeref := ast.Target.(*Deref); isDeref && dstt.HasOwned() {
			if sym, ok := deref.Val.(*Symbol); ok {
				ptype, hasType := c.DeclaredTypeForVar(sym.Name)
				if hasType && c.OwnedObligationLive(sym.Name, ptype) {
					CompileErrorF(a, "Cannot overwrite *%s before consuming its current value", sym.Name)
				}
			} else {
				CompileErrorF(a, "Cannot overwrite owned storage through a non-trivial pointer expression; assign through a named pointer or consume the obligation first")
			}
		}
		if dot, ok := ast.Target.(*Dot); ok {
			// Route through the selector so a package root (`pkg.x = …`)
			// doesn't trip Symbol.ASTType. Only value-typed parents can
			// be an owned aggregate; everything else (packages, types,
			// values cases) skips the check.
			parent := ResolveSelector(c, dot.Val)
			if parent.Kind == ResolvedRuntimeValue || parent.Kind == ResolvedStructField {
				if parentOwnsFields(parent.Type) && dstt.HasOwned() {
					// Assigning to an owned field is legal only to
					// re-initialize one whose previous value was already
					// moved out; assigning to a live field would drop its
					// obligation (a leak). Mirrors the owned-binding rule.
					path, hasPath := FlowPathForExpr(dot)
					if hasPath && c.OwnedFieldConsumed(path) {
						// Re-init: the slot was zeroed on the prior move; the
						// store below re-establishes it. Field is live again.
						c.SetOwnedFieldConsumed(path, false)
					} else {
						target := dot.Member
						if hasPath {
							target = path.Key()
						}
						CompileErrorF(a, "Cannot assign to owned field %q before consuming its current value", target)
					}
				}
			}
		}
		if sl, ok := ast.Val.(*StructLiteral); ok {
			fillAnonymousLiteralIfNeeded(sl, dstt)
		}
		checkAddressOfOwnedForDest(c, ast.Val, dstt)
		// Reject implicit ownership promotion: if the destination has owned bits
		// that the source doesn't, the source must be wrapped in owned().
		// Integer literals are exempt — they initialize owned values without a wrapper.
		// Struct literals matching the destination shape (ignoring owned) are also
		// exempt, mirroring the VarDecl special case: `foo{...}` is the canonical
		// way to initialize an `owned foo` binding, so re-init via the same syntax
		// after dispose stays consistent with first-init.
		{
			dsttmp := dstt
			srctmp := ast.Val.ASTType(c)
			if !srctmp.Same(intlitASTType()) && srctmp.Name != "<nil>" {
				gained := dsttmp.OwnedMask &^ srctmp.OwnedMask
				if gained != 0 {
					if _, ok := ast.Val.(*OwnedPromotion); !ok {
						sl, isSL := ast.Val.(*StructLiteral)
						if !isSL || !sameIgnoringOwned(dsttmp, sl.Type) {
							CompileErrorF(a, "Ownership promotion requires explicit owned(): assigning %s to %s", srctmp, dsttmp)
						}
					}
				}
			}
		}
		lv := compileLval(of, c, ast.Target, nullspot)
		if targetIsSymbol {
			lv.t = dstt
		}
		srct := ast.Val.ASTType(c)
		checkBorrowedPointerAssignment(c, ast, dstt)
		checkSliceEscapeAssignment(c, ast, dstt)
		// Interface coercion at assignment: concrete pointer or small concrete value → fat pointer.
		if shouldCoerceToInterface(c, dstt, srct) {
			destBinding := ""
			if targetIsSymbol {
				destBinding = targetSym.Name
				// updateFieldPointerFactsForAssignment below clears the
				// target's field pointers (Indirection==0 path), so we
				// must register data after that — but the registration
				// inside emitInterfaceFatPtr runs first. Forget here, the
				// helper's later Forget call is harmless on the empty
				// map, and emitInterfaceFatPtr's SetFieldPointer wins.
				c.PointerFlow().ForgetFieldPointers(flow.Binding(destBinding))
			}
			emitInterfaceFatPtr(of, c, a, dstt, srct, ast.Val, lv.ref, destBinding)
			markMovedIfOwnedSource(of, c, dstt, ast.Val)
			invalidateOwnedFieldFactsForMutableTarget(c, ast.Target)
			if path, ok := FlowPathForExpr(ast.Target); ok {
				c.InvalidateFlowFacts(path)
				updateNullFactForAssignment(c, path, dstt, ast.Val, srct)
			}
			updateBorrowedBindingForAssignment(c, ast.Target, dstt, ast.Val)
			updatePointerFlowForAssignment(c, ast.Target, dstt, ast.Val)
			return nullspot
		}
		// Struct-literal assignment to a matching destination (modulo owned):
		// initialize the slot directly through compileStructLiteralInto, the
		// same path VarDecl uses for `var f owned foo = foo{...}`. This makes
		// struct-literal re-init after dispose work without an explicit owned()
		// wrapper, and stays consistent with first-init.
		if sl, ok := ast.Val.(*StructLiteral); ok && sameIgnoringOwned(dstt, sl.Type) {
			// Per-field slice-escape gate: each slice field of the literal
			// is semantically a `target.fieldN = expr` store, even though
			// the codegen merges them. Route through the same primitive
			// the direct field-store path uses.
			checkStructLiteralFieldsForOpaqueTarget(c, ast.Target, dstt, sl)
			compileStructLiteralInto(of, c, a, sl, lv, dstt)
			if targetIsSymbol && dstt.HasOwned() {
				c.Unmove(targetSym.Name)
			}
			invalidateOwnedFieldFactsForMutableTarget(c, ast.Target)
			if path, ok := FlowPathForExpr(ast.Target); ok {
				c.InvalidateFlowFacts(path)
				updateNullFactForAssignment(c, path, dstt, ast.Val, srct)
			}
			updateBorrowedBindingForAssignment(c, ast.Target, dstt, ast.Val)
			updatePointerFlowForAssignment(c, ast.Target, dstt, ast.Val)
			updateFieldPointerFactsForAssignment(c, ast.Target, targetIsSymbol, targetSym, dstt, ast.Val)
			return nullspot
		}
		if !srct.Same(dstt) {
			if _, reason := coerceType(c, dstt, srct); reason != coerceOK {
				reportCoerceFailure(a, dstt, srct, reason,
					"Cannot assign different types %s = %s", dstt, srct)
			}
			// intlit: compileTop shortcut handles range checking and code gen
			dest := newSpot(of, c, c.Temp(), dstt)
			ret := compileTop(of, c, ast.Val, dest)
			if !ret.same(&dest) {
				dest.free(of)
			}
			move(of, c, lv, ret)
			ret.free(of)
			markMovedIfOwnedSource(of, c, dstt, ast.Val)
			if targetIsSymbol && dstt.HasOwned() {
				c.Unmove(targetSym.Name)
			}
			invalidateOwnedFieldFactsForMutableTarget(c, ast.Target)
			if path, ok := FlowPathForExpr(ast.Target); ok {
				c.InvalidateFlowFacts(path)
				updateNullFactForAssignment(c, path, dstt, ast.Val, srct)
			}
			updateBorrowedBindingForAssignment(c, ast.Target, dstt, ast.Val)
			updatePointerFlowForAssignment(c, ast.Target, dstt, ast.Val)
			updateFieldPointerFactsForAssignment(c, ast.Target, targetIsSymbol, targetSym, dstt, ast.Val)
			return nullspot
		}
		// if !lv.t.Same(dstt) {
		// 	panic(fmt.Sprintf("Expected dstt (%v) != lv.t (%v)\n", dstt, lv.t))
		// }
		if lv.t.Same(srct) {
			// srctype == dsttype
			// this means the dsttype is not a pointer to the location.
			// and we can use lv as the dest in the compile call.

			checkAddressOfOwnedForDest(c, ast.Val, dstt)
			// Would be nice to be able to compile *into* lv,
			// But sometimes lv is also in the Val AST, i.e.
			// n = 1 - n.
			// This (currently) can lead to n being overwritten
			// by the compiler before it is finished being used.
			// e.g.
			//  mov n 1
			//  sub n n
			val := compileTop(of, c, ast.Val, lv)
			//val := compileTop(of, c, ast.Val, nullspot)
			if !val.same(&lv) {
				move(of, c, lv, val)
				val.free(of)
			}
			markMovedIfOwnedSource(of, c, dstt, ast.Val)
			if targetIsSymbol && dstt.HasOwned() {
				c.Unmove(targetSym.Name)
			}
			invalidateOwnedFieldFactsForMutableTarget(c, ast.Target)
			if path, ok := FlowPathForExpr(ast.Target); ok {
				c.InvalidateFlowFacts(path)
				updateNullFactForAssignment(c, path, dstt, ast.Val, srct)
			}
			updateBorrowedBindingForAssignment(c, ast.Target, dstt, ast.Val)
			updatePointerFlowForAssignment(c, ast.Target, dstt, ast.Val)
			updateFieldPointerFactsForAssignment(c, ast.Target, targetIsSymbol, targetSym, dstt, ast.Val)
			return nullspot
		}

		checkAddressOfOwnedForDest(c, ast.Val, dstt)
		val := compileTop(of, c, ast.Val, nullspot)
		// if val.t.Size(c) != 8 {
		// 	panic(fmt.Sprintf("CANNOT MOVE TYPES THAT ARE NOT 64 BITS YET, but have %v %d\n", val.t.Size(c), val.t))
		// 	// TODO: Need to handle type sizes
		// }
		// if it's size == 8, it doesn't matter if it's a pointer or a value,
		// we can just copy it.
		move(of, c, lv, val)
		//fmt.Fprintf(of, "\tmov [%s] %s\n", lv.ref, val.ref)
		lv.free(of)
		val.free(of)
		markMovedIfOwnedSource(of, c, dstt, ast.Val)
		if targetIsSymbol && dstt.HasOwned() {
			c.Unmove(targetSym.Name)
		}
		invalidateOwnedFieldFactsForMutableTarget(c, ast.Target)
		if path, ok := FlowPathForExpr(ast.Target); ok {
			c.InvalidateFlowFacts(path)
			updateNullFactForAssignment(c, path, dstt, ast.Val, srct)
		}
		updateBorrowedBindingForAssignment(c, ast.Target, dstt, ast.Val)
		updatePointerFlowForAssignment(c, ast.Target, dstt, ast.Val)
		updateFieldPointerFactsForAssignment(c, ast.Target, targetIsSymbol, targetSym, dstt, ast.Val)
		return nullspot
	case *IfStmt:
		v := compileTop(of, c, ast.Cond, nullspot)
		labelse := c.Label("else")
		labend := c.Label("end")
		fmt.Fprintf(of, "\ttest %s %s\n", v.ref, v.ref)
		v.free(of)
		fmt.Fprintf(of, "\tjz %s\n", labelse)

		// Snapshot flow state before branches.
		snapBefore := c.FlowSnapshot()
		thenFallsThrough := fallsThrough(ast.Then)
		elseFallsThrough := ast.Else == nil || fallsThrough(ast.Else)
		condPath, condType, condNonNullOnThen, condIsNullablePtr := nullablePointerPathForIf(c, ast.Cond)

		if condIsNullablePtr {
			if condNonNullOnThen {
				c.SetNullFact(condPath, NullKnownNonNull)
			} else {
				c.SetNullFact(condPath, NullKnownNull)
				if condType.HasOwned() && condPath.Fields == "" {
					c.MoveConsume(condPath.Root)
				}
			}
		}
		v = compileTop(of, c, ast.Then, nullspot)
		if !v.empty() {
			v.free(of)
		}
		snapAfterThen := c.FlowSnapshot()

		if thenFallsThrough {
			fmt.Fprintf(of, "\tjmp %s\n", labend)
		}
		fmt.Fprintf(of, "\tlabel %s\n", labelse)

		// Restore to pre-branch state for the else branch.
		c.RestoreFlowSnapshot(snapBefore)
		if condIsNullablePtr {
			if condNonNullOnThen {
				c.SetNullFact(condPath, NullKnownNull)
				if condType.HasOwned() && condPath.Fields == "" {
					c.MoveConsume(condPath.Root)
				}
			} else {
				c.SetNullFact(condPath, NullKnownNonNull)
			}
		}

		if ast.Else != nil {
			v = compileTop(of, c, ast.Else, nullspot)
			if !v.empty() {
				v.free(of)
			}
		}
		snapAfterElse := c.FlowSnapshot()

		switch {
		case thenFallsThrough && elseFallsThrough:
			// Both fallthrough branches must agree on obligation-live
			// status for any owned binding that existed pre-branch.
			// Compare obligation-live (not raw moved bits) so that
			// "moved=true" and "moved=false with NullFact=Null" — both
			// "no obligation" encodings — are treated as equivalent.
			for name := range snapAfterThen.Owned {
				if _, existed := snapBefore.Owned[name]; !existed {
					continue // declared inside a branch, not our concern here
				}
				if !c.SameObligationLiveAcross(snapAfterThen, snapAfterElse, name) {
					CompileErrorF(a, "Owned binding \"%s\" is consumed on one branch but not the other", name)
				}
			}
			c.RestoreFlowSnapshot(FlowSnapshot{
				Owned:       snapAfterThen.Owned,
				Null:        MergeNullFacts(snapAfterThen.Null, snapAfterElse.Null),
				OwnedFields: mergeOwnedFieldFactsExact(c, a, snapAfterThen.OwnedFields, snapAfterElse.OwnedFields),
				Borrowed:    MergeBorrowedBindings(snapAfterThen.Borrowed, snapAfterElse.Borrowed),
				Pointer:     flow.Merge(snapAfterThen.Pointer, snapAfterElse.Pointer),
			})
		case thenFallsThrough:
			c.RestoreFlowSnapshot(snapAfterThen)
		case elseFallsThrough:
			c.RestoreFlowSnapshot(snapAfterElse)
		default:
			c.RestoreFlowSnapshot(snapBefore)
		}

		fmt.Fprintf(of, "\tlabel %s\n", labend)
		return nullspot
	case *Loop:
		snapBeforeLoop := c.FlowSnapshot()

		start := c.Label("loop")
		end := c.PushBreakLabel()
		cont := c.PushContLabel()
		fmt.Fprintf(of, "\tlabel %s\n", start)
		bodyFallsThrough := fallsThrough(ast.Body)
		v := compileTop(of, c, ast.Body, nullspot)
		if !v.empty() {
			v.free(of)
		}
		snapAfterBody := c.FlowSnapshot()
		continueStates := c.ContinueStates()
		breakStates := c.BreakStates()

		var backedgeStates []FlowSnapshot
		if bodyFallsThrough {
			backedgeStates = append(backedgeStates, snapAfterBody)
		}
		backedgeStates = append(backedgeStates, continueStates...)
		if len(backedgeStates) > 0 {
			c.RestoreFlowSnapshot(backedgeStates[0])
		}
		if len(backedgeStates) > 1 {
			for _, state := range backedgeStates[1:] {
				for name := range snapBeforeLoop.Owned {
					if !c.SameObligationLiveAcross(backedgeStates[0], state, name) {
						CompileErrorF(a, "Owned binding \"%s\" has inconsistent state across loop backedges", name)
					}
				}
				// Owned fields must agree across every back-edge too (the
				// fall-through and each continue-path). A field consumed on
				// one path but live on another would be re-moved (a
				// double-free) on the next iteration.
				seen := map[FlowPath]bool{}
				for path := range backedgeStates[0].OwnedFields {
					seen[path] = true
				}
				for path := range state.OwnedFields {
					seen[path] = true
				}
				for path := range seen {
					if backedgeStates[0].OwnedFields[path] != state.OwnedFields[path] {
						CompileErrorF(a, "Owned field \"%s\" has inconsistent state across loop backedges", path.Key())
					}
				}
			}
		}
		fmt.Fprintf(of, "\tlabel %s\n", cont)
		fmt.Fprintf(of, "\tjmp %s\n", start)
		fmt.Fprintf(of, "\tlabel %s\n", end)

		if len(breakStates) > 0 {
			merged := breakStates[0]
			for _, exit := range breakStates[1:] {
				for name := range snapBeforeLoop.Owned {
					if !c.SameObligationLiveAcross(merged, exit, name) {
						CompileErrorF(a, "Owned binding \"%s\" has inconsistent state across loop exits", name)
					}
				}
				merged = mergeFlowSnapshots(c, a, merged, exit)
			}
			c.RestoreFlowSnapshot(merged)
		} else {
			// No reachable exit (e.g. `for(;;) { ... }` with no break / return).
			// Post-loop code is unreachable; restore pre-loop facts so any
			// remaining checks see a coherent state rather than the body's
			// residual.
			c.RestoreFlowSnapshot(snapBeforeLoop)
		}
		c.PopBreakLabel()
		c.PopContLabel()

		// Check that no pre-loop owned var reaches a loop backedge invalid.
		// "Invalid" means: the body assumed an obligation was live going in,
		// but the back-edge would deliver no obligation on iteration N+1.
		// Compare obligation-live (not raw moved bits) so that
		// owned-nullable bindings whose pre-loop state is NullKnownNull
		// and whose back-edge state is moved=true are treated as
		// "no obligation → no obligation", not as consumption.
		if len(backedgeStates) > 0 {
			for name := range snapBeforeLoop.Owned {
				if !c.OwnedObligationLiveInSnap(snapBeforeLoop, name) {
					continue
				}
				if !c.OwnedObligationLiveInSnap(backedgeStates[0], name) {
					CompileErrorF(a, "Owned binding \"%s\" is consumed inside a loop body; this would be invalid on the second iteration", name)
				}
			}
			// Same for owned fields: a field live before the loop but moved
			// out at the back-edge (without re-initialization) would be
			// re-moved on the next iteration — a use-after-move/double-free.
			for path, consumed := range backedgeStates[0].OwnedFields {
				if consumed && !snapBeforeLoop.OwnedFields[path] {
					CompileErrorF(a, "Owned field \"%s\" is consumed inside a loop body; this would be invalid on the second iteration", path.Key())
				}
			}
		}
		return nullspot
	case *Op2:
		return doOp2(of, c, ast, dest)
	case *Not:
		v := compileTop(of, c, ast.Val, nullspot)
		if dest.empty() {
			dest = newSpot(of, c, c.Temp(), boolASTType())
		}
		fmt.Fprintf(of, "\ttest %s %s\n", v.ref, v.ref)
		fmt.Fprintf(of, "\tsete %s\n", dest.ref)
		v.free(of)
		return dest
	case *Return:
		retType := c.ReturnType()
		if ast.Val == nil {
			// Bare `return` — valid in void functions or as an early exit.
			if !retType.Same(voidASTType()) {
				CompileErrorF(a, "bare return in non-void function (return type is %s)", retType)
			}
			for _, name := range c.UnconsumedOwnedVisible() {
				CompileErrorF(a, "Owned binding \"%s\" goes out of scope without being consumed; call dispose() or pass it to a consuming function", name)
			}
			c.Return(of)
			return nullspot
		}
		if sl, ok := ast.Val.(*StructLiteral); ok {
			fillAnonymousLiteralIfNeeded(sl, retType)
		}
		valType := ast.Val.ASTType(c)
		// A partially-consumed aggregate cannot be returned: the caller's
		// scope cannot see the local consistency facts.
		if name := aggregateBindingName(ast.Val); name != "" {
			checkAggregateMayEscape(c, name, ast, "return")
		}
		// Resolve through type aliases so `type myptr *T` followed by
		// `return p` runs the same pointer-escape checks as a bare *T return.
		if c.ResolveUnderlying(retType).Indirection > 0 {
			// A borrowed pointer return is no longer a hard error: it is
			// recordable via alias_set (which ran above and emitted this
			// function's retaliases), so the lifetime obligation moves to
			// the caller. Only a pointer to *local* storage stays rejected
			// here — caller knowledge cannot rescue a dead stack slot.
			checkLocalOriginDoesNotEscape(c, ast.Val, "return")
		}
		retCompat := c.ResolveUnderlying(retType)
		if retCompat.IsSlice() {
			// Return site: reject only a local-array escape; a borrowed
			// (parameter-view) slice is recordable via alias_set, which
			// already ran for this function and emitted its retaliases.
			checkSliceEscapeLocalOnly(c, ast.Val, "through return")
		}
		if retType.Indirection == 0 {
			retVal := ast.Val
			if op, ok := retVal.(*OwnedPromotion); ok {
				retVal = op.Val
			}
			if sym, ok := retVal.(*Symbol); ok && !c.IsGlobalBinding(sym.Name) {
				// Local-only: a struct whose field borrows a *parameter* is
				// recordable via alias_set; only a field aliasing local
				// storage dangles in the returned copy and stays rejected.
				if escaped, field := c.PointerFlow().CheckStructFieldEscapeLocal(flow.Binding(sym.Name)); escaped {
					// A struct returned by value FROM A CALL borrows local
					// storage through one of its (coarsely-tracked) fields: the
					// alias set names a parameter, not a field, so the sentinel
					// key carries the provenance. Report it without leaking the
					// synthetic field name.
					if field == structReturnAliasFieldKey {
						CompileErrorF(ast.Val, "Cannot return %q by value: it holds a borrow of local-scope storage (through a returned struct value); the alias would dangle in the returned copy", sym.Name)
					}
					// The escaping field can be either a pointer or a slice.
					// The wording differs ("pointer to" vs "slice into", and
					// the slice form drops the binding name). For multi-level
					// paths like "inner.buf" the leaf type lives several
					// struct hops deep — walkLeafType follows the path.
					fieldIsSlice := false
					if t, ok := c.TypeForVar(sym.Name); ok {
						if leaf, ok := walkLeafType(c, t, field); ok {
							fieldIsSlice = leaf.IsSlice()
						}
					}
					if fieldIsSlice {
						CompileErrorF(ast.Val, "Cannot return struct by value: field %q contains a slice into local-scope storage; the alias would dangle in the returned copy", field)
					} else {
						CompileErrorF(ast.Val, "Cannot return %q by value: field %q contains a pointer to local-scope storage; the alias would dangle in the returned copy", sym.Name, field)
					}
				}
			}
			if sl, ok := retVal.(*StructLiteral); ok {
				// Pointer-field escape stays single-level — owned/borrowed
				// pointer field semantics are different enough from slices
				// that nesting through a struct value rarely matches a real
				// escape shape, and the pointer-field path hasn't unified
				// onto the IsEscapeRestricted predicate yet.
				for _, f := range sl.Fields {
					if f.Val == nil {
						continue
					}
					ft := f.Val.ASTType(c)
					if ft.Indirection > 0 {
						ptr := pointerExprForAST(c, f.Val, "")
						if ptr.KnownOrigin && c.PointerFlow().OriginKindOf(ptr.Origin) == flow.OriginLocal {
							CompileErrorF(ast.Val, "Cannot return struct literal by value: field %q contains a pointer to local-scope storage; the alias would dangle in the returned copy", f.Name)
						}
					}
				}
				// Slice-field escape walks into nested struct literals so
				// `return Outer{inner: B{buf: s}}` is rejected the same as
				// the flat `return B{buf: s}` form.
				ownerType := sl.Type
				if !sameIgnoringOwned(retType, ownerType) {
					// fillAnonymousLiteralIfNeeded earlier may have left
					// sl.Type empty; fall back to retType for the walk.
					ownerType = retType
				}
				walkStructLiteralSliceEscape(c, ownerType, sl, func(ot ASTType, fieldName string, val AST) {
					ptr := pointerExprForAST(c, val, "")
					// Local-only: a literal field borrowing a *parameter*
					// (the new_builder constructor case) is recordable via
					// alias_set; only a field aliasing local-scope storage
					// dangles in the returned copy and stays rejected.
					if ptr.KnownOrigin && c.PointerFlow().OriginKindOf(ptr.Origin) == flow.OriginLocal {
						CompileErrorF(ast.Val, "Cannot return struct literal by value: field %q contains a slice into local-scope storage; the alias would dangle in the returned copy", fieldName)
					}
				})
			}
		}
		// Analysis-run summary capture: when this compile is an alias-
		// inference run (aliasCapture installed by aliasSet; nil during
		// real codegen), read the return value's provenance from the
		// tracker and fold the borrowed-param indices it reaches into the
		// summary. Ordered AFTER the per-site escape checks above so the
		// precise local-escape diagnostics fire first; the capture's own
		// local-reject then only catches locals arriving through a call
		// boundary, which the per-site checks cannot see.
		captureReturnAliases(c, ast)
		if sl, ok := ast.Val.(*StructLiteral); ok && sameIgnoringOwned(retType, sl.Type) {
			valType = retType
		}
		if valType.HasOwned() && !retType.HasOwned() {
			CompileErrorF(a, "Cannot return %s as non-owned %s; ownership would be dropped", valType, retType)
		}
		checkAddressOfOwnedForDest(c, ast.Val, retType)
		// Interface coercion at return: concrete pointer → fat pointer in rax.
		if shouldCoerceToInterface(c, retType, valType) && valType.Indirection > 0 {
			// Borrowed-pointer source returned as a borrowed interface
			// escapes the caller's frame just like a bare `return &x`.
			if retType.OwnedMask == 0 {
				ptr := pointerExprForAST(c, ast.Val, "")
				if ptr.KnownOrigin && c.PointerFlow().OriginKindOf(ptr.Origin) == flow.OriginLocal {
					CompileErrorF(ast.Val, "Pointer to local variable %q escapes via interface return", string(ptr.Origin))
				}
			}
			s := newSpotWithReg(of, c, c.Temp(), retType, "rax")
			emitInterfaceFatPtr(of, c, a, retType, valType, ast.Val, s.ref, "")
			fmt.Fprintf(of, "\tinreg %s rax\n", s.ref)
			s.free(of)
			if retType.HasOwned() {
				markMovedIfOwnedSource(of, c, retType, ast.Val)
			}
			for _, name := range c.UnconsumedOwnedVisible() {
				CompileErrorF(a, "Owned binding \"%s\" goes out of scope without being consumed; call dispose() or pass it to a consuming function", name)
			}
			c.Return(of)
			return nullspot
		}
		if shouldCoerceToInterface(c, retType, valType) {
			s := newSpotWithReg(of, c, c.Temp(), retType, "rax")
			emitInterfaceFatPtr(of, c, a, retType, valType, ast.Val, s.ref, "")
			fmt.Fprintf(of, "\tinreg %s rax\n", s.ref)
			s.free(of)
			if retType.HasOwned() {
				markMovedIfOwnedSource(of, c, retType, ast.Val)
			}
			for _, name := range c.UnconsumedOwnedVisible() {
				CompileErrorF(a, "Owned binding \"%s\" goes out of scope without being consumed; call dispose() or pass it to a consuming function", name)
			}
			c.Return(of)
			return nullspot
		}
		valCompat := c.ResolveUnderlying(valType)
		if _, reason := coerceType(c, retCompat, valCompat); reason != coerceOK && !sameIgnoringOwned(retCompat, valCompat) {
			reportCoerceFailure(a, retType, valType, reason,
				"Cannot return %s from value of type %s", retType, valType)
		}
		if valType.Same(intlitASTType()) {
			valType = numASTType()
		}
		raxName := raxForType(valType)
		dest := newSpotWithReg(of, c, c.Temp(), valType, raxName)
		v := compileTop(of, c, ast.Val, dest)
		if !v.same(&dest) {
			// Memory-backed values (e.g. SliceOp) currently compile to a
			// fresh `bytes` slot rather than honoring `dest`. Copy into the
			// rax-pinned dest so the standard `inreg dest rax` epilogue
			// publishes the result's address. Non-memory-backed mismatches
			// still indicate a compiler bug.
			if typeIsMemoryBacked(c, valType) && dest.nameIsAddress {
				spot_memcpy(of, c, dest, v, valType.Size(c))
				v.free(of)
			} else {
				dest.free(of)
				dest = v
				CompileErrorF(a, "Return not same as dest. Should this happen?")
			}
		}
		fmt.Fprintf(of, "\tinreg %s %s\n", dest.ref, raxName)
		sz := valType.Size(c)
		if sz == 1 || sz == 2 {
			// Writing al/ax does not clear the upper bits of rax.
			// The SysV ABI requires rax to hold the sign/zero-extended return value.
			if valType.Signed {
				fmt.Fprintf(of, "\tmovsx rax %s\n", dest.ref)
			} else {
				fmt.Fprintf(of, "\tmovzx rax %s\n", dest.ref)
			}
		}
		// sz == 4: writing eax already zeros the upper 32 bits of rax automatically.
		dest.free(of)
		if retType.HasOwned() {
			markMovedIfOwnedSource(of, c, retType, ast.Val)
		}
		for _, name := range c.UnconsumedOwnedVisible() {
			CompileErrorF(a, "Owned binding \"%s\" goes out of scope without being consumed; call dispose() or pass it to a consuming function", name)
		}
		c.Return(of)

		return nullspot
	case *Dispose:
		t, ok := c.TypeForVar(ast.Var)
		if !ok {
			CompileErrorF(a, "dispose: \"%s\" is not declared", ast.Var)
		}
		if !t.HasOwned() {
			CompileErrorF(a, "dispose: \"%s\" has type %s which has no owned obligation", ast.Var, t)
		}
		if c.IsMoved(ast.Var) {
			CompileErrorF(a, "dispose: \"%s\" was already moved", ast.Var)
		}
		// dispose() consumes an obligation; if there is none (because the
		// binding is a statically-known-nil owned nullable pointer), the
		// call is redundant. Mirror free's rejection for consistency.
		declared, _ := c.DeclaredTypeForVar(ast.Var)
		if declared.Indirection > 0 && declared.NilMask&1 != 0 &&
			c.NullFact(VarFlowPath(ast.Var)) == NullKnownNull {
			CompileErrorF(a, "dispose: \"%s\" is statically known to be nil; no obligation to discharge", ast.Var)
		}
		c.MoveConsume(ast.Var)
		note(of, "\t// dispose %s — obligation satisfied, no runtime effect\n", ast.Var)
		return nullspot
	case *Continue:
		if ast.Step != nil {
			v := compileTop(of, c, ast.Step, nullspot)
			if !v.empty() {
				v.free(of)
			}
		}
		c.Continue(a, of)
		return nullspot
	case *Break:
		c.Break(of)
		return nullspot
	case *Symbol:
		if c.IsMoved(ast.Name) {
			CompileErrorF(a, "Use of \"%s\" after it was moved", ast.Name)
		}
		// Validate the binding's flow link. Value-typed bindings catch a
		// use-after-move on an aliased source (`var t i64 = fd; take(fd);
		// read t`). Pointer-typed bindings catch a use-after-move on a
		// derived pointer (`var p *i64 = &fd; take(fd); use_ptr(p)`).
		// The pointee *p deref site already validates explicit derefs;
		// this catches the cases where the pointer crosses a function or
		// method boundary opaquely (interface dispatch, fn args, method
		// receiver) — there is no other place the local tracker can flag
		// the staleness before the link is lost to the callee's scope.
		if ok, reason := c.PointerFlow().CheckDerefValidity(c.PointerFlow().Pointer(flow.Binding(ast.Name))); !ok {
			CompileErrorF(a, "%s", reason)
		}
		s := spot{ref: ast.Name, t: ast.ASTType(c), nameIsAddress: c.NameIsAddress(ast.Name)}
		if dest.same(&nullspot) {
			return s
		}
		if s.same(&dest) {
			note(of, "\t// destination is already %s\n", dest.ref)
			return s
		}
		move(of, c, dest, s)
		return dest
	case *NonNullAssert:
		s := compileTop(of, c, ast.Val, dest)
		knownNonNull := false
		if path, ok := FlowPathForExpr(ast.Val); ok && c.NullFact(path) == NullKnownNonNull {
			knownNonNull = true
		}
		if !knownNonNull {
			l := c.Label("nonnull")
			fmt.Fprintf(of, "\tcmp %s 0\n", s.ref)
			fmt.Fprintf(of, "\tjne %s\n", l)
			fmt.Fprintf(of, "\tcall _init.nil_assert\n")
			fmt.Fprintf(of, "\tlabel %s\n", l)
		}
		s.t = ast.ASTType(c)
		return s
	case *StructLiteral:
		ctxType := ast.Type
		if !dest.empty() && sameIgnoringOwned(dest.t, ast.Type) {
			ctxType = dest.t
		}
		return compileStructLiteralInto(of, c, a, ast, dest, ctxType)
	case *Literal:
		t := ast.ASTType(c)
		// literals can have no indirection and cannot be arrays (yet)
		if t.Indirection > 0 || t.ArraySize > 0 {
			panic("NOT IMPLEMENTED (TODO)")
		}
		if ast.Val == nil {
			if dest.empty() {
				CompileErrorF(a, "nil requires pointer context")
			}
			if dest.t.Indirection == 0 && dest.t.FuncSig == nil {
				CompileErrorF(a, "nil cannot initialize non-pointer type %s", dest.t)
			}
			fmt.Fprintf(of, "\tmov %s 0\n", dest.ref)
			return dest
		}
		if dest.empty() {
			dest = newSpot(of, c, c.Temp(), t)
		}

		switch v := ast.Val.(type) {
		case string:
			// String literals have type byte[]: a fat pointer {data_ptr, length}.
			// dest is a bytes slot (16 bytes). Use qword[] to force 64-bit stores.
			s := c.String(v)
			reg := regSpot(of, "r10")
			fmt.Fprintf(of, "\tlea r10 %s\n", s)
			fmt.Fprintf(of, "\tmov qword[%s] r10\n", dest.ref)
			fmt.Fprintf(of, "\tmov qword[%s+8] %d\n", dest.ref, len(v))
			reg.free(of)
		case uint64:
			fmt.Fprintf(of, "\tmov %s %d\n", dest.ref, v)
		case byte:
			fmt.Fprintf(of, "\tmov %s %d\n", dest.ref, v)
		default:
			panic("NOT IMPLEMENTED (TODO)")
		}
		return dest
	}
	panic(fmt.Sprintf("(TOP) FALLTHROUGH: %#v\n", a))
}

func containsFuncall(a AST) bool {
	switch ast := a.(type) {
	case *Funcall:
		return true
	case *Dot:
		return containsFuncall(ast.Val)
	case *Deref:
		return containsFuncall(ast.Val)
	case *NonNullAssert:
		return containsFuncall(ast.Val)
	case *Address:
		return false
	case *Op2:
		return containsFuncall(ast.First) || containsFuncall(ast.Second)
	case *Not:
		return containsFuncall(ast.Val)
	case *Literal:
		return false
	case *Symbol:
		return false
	case *Loop:
		// Statement form; should not appear in expression contexts, but
		// listed explicitly so the fallthrough panic stays informative
		// for genuinely new node kinds.
		return false
	}
	panic("ContainsFuncall Fallthrough\n")
}

// compileIndirectCall emits a call through a function-pointer value.
// The signature supplies the parameter types for argument setup; the
// pointer's value is materialized into a temp register and used as the
// call target via `call reg`. `srcRef` is the bas-level source operand
// for the load — typically a variable name like "fp" or "global_op", or
// an indirect form like "[addr+off]" for struct-field calls.
func compileIndirectCall(of io.Writer, c *Context, a AST, ast *Funcall, srcRef string, sig *FuncSig, dest spot) spot {
	if len(ast.Args) != len(sig.Args) {
		CompileErrorF(a, "function pointer %s expected %d arguments, but was called with %d",
			srcRef, len(sig.Args), len(ast.Args))
	}
	// Load the function-pointer value into a fresh temp BEFORE setting
	// up call arguments. setupArgs evicts whatever currently sits in
	// the arg registers (rdi, rsi, …). If srcRef is itself a
	// register-resident parameter (a fn(...)-typed param lives in rdi
	// when it's argument 0), the eviction would spill it to memory
	// after our read. Reading first into a temp avoids the round-trip.
	tgt := newSpot(of, c, c.Temp(), ASTType{Name: "i64"})
	fmt.Fprintf(of, "\tmov %s %s\n", tgt.ref, srcRef)

	// Reuse setupArgs by building a synthetic FuncDecl mirroring the
	// signature. setupArgs only reads d.Args[i].Type, so the Names and
	// IsConst flags don't matter.
	synth := &FuncDecl{Return: sig.Return}
	for _, at := range sig.Args {
		synth.Args = append(synth.Args, Binding{Type: at})
	}
	argorder := setupArgs(of, c, ast, synth)

	retType := sig.Return
	raxName := raxForType(retType)
	note(of, "\t// acquire %s for indirect call return\n", raxName)
	rax := regSpot(of, raxName)
	rax.t = retType

	fmt.Fprintf(of, "\tcall %s\n", tgt.ref)
	tgt.free(of)

	for i := 0; i < len(ast.Args); i++ {
		note(of, "\t// release call registers\n")
		fmt.Fprintf(of, "\trelease %s\n", argorder[i])
	}
	ret := nullspot
	if !sig.Return.Same(voidASTType()) {
		if !dest.empty() {
			ret = dest
		} else {
			ret = newSpot(of, c, c.Temp(), sig.Return)
		}
		move(of, c, ret, rax)
	}
	note(of, "\t// free %s for indirect call return\n", raxName)
	rax.t = regtype
	rax.free(of)
	return ret
}

func setupArgs(of io.Writer, c *Context, f *Funcall, d *FuncDecl) []string {
	order := []string{"rdi", "rsi", "rdx", "rcx", "r8", "r9"}
	// This double-commented code and all comments within are obsolete.
	// However, I still want to revisit how I'm setting up the call registers
	// and don't want to throw this old code away yet.
	//
	// // TODO: Want to be able to optimize this, but can't currently.
	// // Same issue as other register optimizations - we can't acquire the registers
	// // we want ahead-of-time since things like funcalls and register-specific ops
	// // like mul & div may need them.
	// //
	// // funcallArgs := false
	// // for i := 0; i < len(f.Args); i++ {
	// // 	arg := f.Args[i]
	// // 	if containsFuncall(arg) {
	// // 		funcallArgs = true
	// // 		break
	// // 	}
	// // }
	// // order := []string{"rdi", "rsi", "rdx", "rcx", "r8", "r9"}
	// // if !funcallArgs {
	// // 	// optimization. We know there are no function calls in the
	// // 	// arguments to this function, so we can copy directly into the
	// // 	// call registers.
	// // 	for i := 0; i < len(f.Args); i++ {
	// // 		arg := f.Args[i]
	// // 		argt := arg.ASTType(c)
	// // 		if !argt.Same(&d.Args[i].Type) {
	// // 			panic("BAD ARG TYPE! (TODO)\n")
	// // 		}
	// // 		if i > 5 {
	// // 			panic("More than 6 args not supported yet (TODO)")
	// // 		}
	// // 		fmt.Fprintf(of, "\t// acquire %s for funcall args\n", order[i])
	// // 		rspot := regSpot(of, order[i])
	// // 		a := compileTop(of, c, arg, rspot)
	// // 		if a == nullspot {
	// // 			panic("AAAH! NULLSPOT!\n")
	// // 		}
	// // 		if a.ref != order[i] {
	// // 			move(of, c, rspot, a)
	// // 			a.free(of)
	// // 		}
	// // 	}
	// // 	return order
	// // }
	// //
	// // This song and dance is to resolve the arguments into their
	// // associated registers.
	// //
	// // It's possible to optimize this, but this is ok for now.
	// // The reason we have two loops is that we can't acquire the
	// // call registers in the first loop while we're compiling the
	// // arguments. An argument might be a funcall, which itself will
	// // need the call registers.
	// // So instead, we first compile all the arguments into
	// // argspots, and then move each argument into its register for
	// // the call.

	// We try optimistically to keep the arguments in the call registers
	// using the newSpotWithReg function. If necessary, these are evicted for
	// things like funcalls and mul ops
	var argspots []spot
	// coercedToInterface[i] is true for args handled by the inline
	// interface-coercion block below. Their owned-source consumption is
	// performed inline there; the post-arg move-marking loop must skip them
	// because markMovedIfOwnedSource is not idempotent (a second call would
	// observe the already-moved binding and spuriously error).
	coercedToInterface := make([]bool, len(f.Args))
	for i := 0; i < len(f.Args); i++ {
		arg := f.Args[i]
		param := d.Args[i].Type
		if sl, ok := arg.(*StructLiteral); ok {
			fillAnonymousLiteralIfNeeded(sl, param)
		}
		argt := arg.ASTType(c)
		// A partially-consumed aggregate cannot cross the call boundary —
		// borrow or owning — because the callee names the concrete shape and
		// would observe the moved-out field. dispose/free are lowered on
		// separate intrinsic paths and never reach here.
		if name := aggregateBindingName(arg); name != "" {
			checkAggregateMayEscape(c, name, arg, "pass")
		}
		checkAddressOfOwnedForDest(c, arg, param)
		// Interface coercion at a call site: concrete pointer or small concrete value → fat pointer.
		// destBinding is "" because the destination is the callee's parameter
		// slot, not a binding we can track. Borrow-into-interface at a call
		// site needs no extra escape check — the same non-escaping-borrow rule
		// that applies to bare *T parameters covers it.
		if shouldCoerceToInterface(c, param, argt) {
			s := newSpot(of, c, c.Temp(), param)
			emitInterfaceFatPtr(of, c, arg, param, argt, arg, s.ref, "")
			if param.HasOwned() {
				markMovedIfOwnedSource(of, c, param, arg)
			}
			coercedToInterface[i] = true
			argspots = append(argspots, s)
			continue
		}
		effective, reason := coerceType(c, param, argt)
		if reason != coerceOK {
			reportCoerceFailure(arg, param, argt, reason,
				"For argument %d, expected type %v but got %v", i, param, argt)
		}
		argt = effective
		// If the parameter has owned, this is a move. Check for double-move now,
		// but defer the actual marking until after the argument is compiled.
		if param.HasOwned() {
			checkOwnedSourceAvailable(c, arg)
		}
		if i > 5 {
			CompileErrorF(arg, "More than 6 arguments are not supported")
		}
		var dest spot
		if argt.Size(c) == PTR_SIZE {
			dest = newSpotWithReg(of, c, c.Temp(), argt, order[i])
		} else {
			dest = newSpot(of, c, c.Temp(), argt)
		}
		a := compileTop(of, c, arg, dest)
		if a.empty() {
			panic("AAAH! NULLSPOT! This should not happen. An ast that's supposed to produce a value for the call produced a null spot.\n")
		}
		if !a.same(&dest) {
			dest.free(of)
			dest = a
		}
		argspots = append(argspots, dest)
	}
	// Now that all argument values are compiled, mark any consumed owned variables.
	// Args coerced to an interface parameter already had their owned source
	// consumed inline above; re-marking them here would double-consume.
	for i := 0; i < len(f.Args); i++ {
		if coercedToInterface[i] {
			continue
		}
		if d.Args[i].Type.HasOwned() {
			markMovedIfOwnedSource(of, c, d.Args[i].Type, f.Args[i])
		}
	}
	// Invalidate pointer aliases for consumed owned-pointer arguments.
	for i := 0; i < len(f.Args); i++ {
		if d.Args[i].Type.Indirection > 0 && d.Args[i].Type.OwnedMask&1 != 0 {
			ptrExpr := pointerExprForAST(c, f.Args[i], "")
			if ptrExpr.KnownOrigin {
				c.PointerFlow().InvalidateOrigin(ptrExpr.Origin, flow.TargetDead)
			}
		}
	}
	// Apply pointer-flow invalidation for any non-owning mutable pointer
	// parameters. This runs for direct and indirect calls alike: both go
	// through setupArgs, and an indirect call carries the same alias risk
	// as a direct one.
	for i, arg := range f.Args {
		if invalidatesOwnedFieldFactsParam(d.Args[i].Type) {
			c.InvalidateOwnedFieldFactsByPointerInvalidation(c.PointerFlow().MutBorrowCall(pointerExprForAST(c, arg, "")))
		}
	}
	for i := 0; i < len(f.Args); i++ {
		note(of, "\t// Ensuring argument %d (%v) is in register %v for call.\n",
			i, argspots[i].ref, order[i])
		argt := argspots[i].t
		switch {
		case argt.Indirection == 0 && typeIsMemoryBacked(c, argt) && argt.Size(c) < PTR_SIZE:
			// memBacked struct of 1, 2, or 4 bytes: pack the bits from
			// the stack chunk into the argument register via a width-
			// matched mov. mov to a 32-bit sub-register zero-extends to
			// the full 64-bit register on x86-64. 8-byte memBacked
			// values rode the register-hint + inreg path on the dest
			// allocation above and fall into the >= PTR_SIZE branch.
			packSmallMemBackedArg(of, c, f.Args[i], argspots[i].ref, argt.Size(c), i, order)
		case argt.Size(c) >= PTR_SIZE:
			// Pointer-sized scalar, or >8-byte memBacked passed by
			// address (the callee derefs).
			fmt.Fprintf(of, "\tinreg %s %s\n", argspots[i].ref, order[i])
		default:
			// Scalar narrower than a pointer (i8/i16/i32 etc.).
			fmt.Fprintf(of, "\tacquire %s\n", order[i])
			if argt.Signed {
				fmt.Fprintf(of, "\tmovsx %s %s\n", order[i], argspots[i].ref)
			} else {
				fmt.Fprintf(of, "\tmovzx %s %s\n", order[i], argspots[i].ref)
			}
		}
		argspots[i].free(of)
	}

	return order
}

// packSmallMemBackedArg emits the width-matched load that brings a memBacked
// value of size 1, 2, or 4 bytes from its stack chunk into the call
// register order[i]. 32-bit writes auto-zero-extend to 64 bits on x86-64;
// 8- and 16-bit loads go through movzx. Odd sizes (3, 5, 6, 7) are rejected
// — the natural alignment of struct fields means these layouts are unusual
// and supporting them would require multi-load packing. 8-byte memBacked
// args ride the bytes-with-register-hint + inreg path on the caller side.
func packSmallMemBackedArg(of io.Writer, c *Context, errNode AST, srcRef string, size int, argIdx int, regOrder []string) {
	reg64 := regOrder[argIdx]
	reg32 := []string{"edi", "esi", "edx", "ecx", "r8d", "r9d"}[argIdx]
	fmt.Fprintf(of, "\tacquire %s\n", reg64)
	switch size {
	case 1:
		fmt.Fprintf(of, "\tmovzx %s byte[%s]\n", reg64, srcRef)
	case 2:
		fmt.Fprintf(of, "\tmovzx %s word[%s]\n", reg64, srcRef)
	case 4:
		fmt.Fprintf(of, "\tmov %s dword[%s]\n", reg32, srcRef)
	default:
		CompileErrorF(errNode, "passing a %d-byte value by value is not supported; use a pointer or a 1/2/4/8-byte layout", size)
	}
}

func jumpOp(of io.Writer, c *Context, o *Op2, label string) {
	switch o.Type {
	case n_lt:
		first := compileTop(of, c, o.First, nullspot)
		second := compileTop(of, c, o.Second, nullspot)
		// TODO: For now we only use signed integers.
		// so we will use the setl/setg etc. instructions.
		// For unsigned integers we will need to use
		// setb/seta etc.
		fmt.Fprintf(of, "\tcmp %s %s\n", first.ref, second.ref)
		fmt.Fprintf(of, "\tjl %s\n", label)
	case n_le:
		first := compileTop(of, c, o.First, nullspot)
		second := compileTop(of, c, o.Second, nullspot)
		// TODO: For now we only use signed integers.
		// so we will use the setl/setg etc. instructions.
		// For unsigned integers we will need to use
		// setb/seta etc.
		fmt.Fprintf(of, "\tcmp %s %s\n", first.ref, second.ref)
		fmt.Fprintf(of, "\tgjle %s\n", label)
	case n_gt:
		first := compileTop(of, c, o.First, nullspot)
		second := compileTop(of, c, o.Second, nullspot)
		// TODO: For now we only use signed integers.
		// so we will use the setl/setg etc. instructions.
		// For unsigned integers we will need to use
		// setb/seta etc.
		fmt.Fprintf(of, "\tcmp %s %s\n", first.ref, second.ref)
		fmt.Fprintf(of, "\tjg %s\n", label)
	case n_ge:
		first := compileTop(of, c, o.First, nullspot)
		second := compileTop(of, c, o.Second, nullspot)
		// TODO: For now we only use signed integers.
		// so we will use the setl/setg etc. instructions.
		// For unsigned integers we will need to use
		// setb/seta etc.
		fmt.Fprintf(of, "\tcmp %s %s\n", first.ref, second.ref)
		fmt.Fprintf(of, "\tjge %s\n", label)
	}
}

// raxForType returns the rax sub-register name appropriate for the given return type.
// i8/u8/bool/byte → "al", i16/u16 → "ax", i32/u32 → "eax", everything else → "rax".
func raxForType(t ASTType) string {
	if t.Indirection > 0 || t.IsSliceOrArray() {
		return "rax"
	}
	switch t.Name {
	case "i8", "u8", "byte", "bool":
		return "al"
	case "i16", "u16":
		return "ax"
	case "i32", "u32":
		return "eax"
	default:
		return "rax"
	}
}

// mulDivRegs returns the rax/rdx equivalents for a given type size in bits.
// Used to select the right register pair for mul/div at each integer width.
func mulDivRegs(bits int) (raxName, rdxName string) {
	switch bits {
	case 32:
		return "eax", "edx"
	case 16:
		return "ax", "dx"
	default:
		return "rax", "rdx"
	}
}

// EvalConst evaluates a pure integer literal expression (no variables) using
// arbitrary-precision arithmetic. Returns (nil, false) if the expression
// contains any non-literal sub-expressions.
func EvalConst(c *Context, a AST) (*big.Int, bool) {
	switch ast := a.(type) {
	case *Literal:
		if v, ok := ast.Val.(uint64); ok {
			return new(big.Int).SetUint64(v), true
		}
		return nil, false
	case *Op2:
		fv, fok := EvalConst(c, ast.First)
		sv, sok := EvalConst(c, ast.Second)
		if !fok || !sok {
			return nil, false
		}
		result := new(big.Int)
		switch ast.Type {
		case n_add:
			result.Add(fv, sv)
		case n_sub:
			result.Sub(fv, sv)
		case n_mul:
			result.Mul(fv, sv)
		case n_div:
			if sv.Sign() == 0 {
				return nil, false
			}
			result.Quo(fv, sv)
		case n_bitand:
			result.And(fv, sv)
		case n_bitor:
			result.Or(fv, sv)
		default:
			return nil, false
		}
		return result, true
	}
	return nil, false
}

// splitQualifiedName splits a "pkg.leaf" identifier into its parts. For
// an unqualified name (no dot), pkg is empty and leaf is the input.
// Used by the projection cast path to construct cross-package symbol
// references (`pkg.__projection_leaf__index`) without re-qualifying
// local references.
func splitQualifiedName(name string) (pkg, leaf string) {
	if dot := strings.LastIndex(name, "."); dot >= 0 {
		return name[:dot], name[dot+1:]
	}
	return "", name
}

// projectionSymbolName returns the bas-level symbol for a values type's
// projection table. The form is
// __projection_<typename-with-dots-as-underscores>__<index>, where
// index is the 0-based position in the values decl's projection
// signature.
//
// Earlier revisions encoded the projection type's structure
// (indirection, mut/owned/nullable, slice/array, function signatures)
// into the key. Every such scheme is collidable: any flat
// string-encoded shape can be matched by a user-chosen type or alias
// name. (A user-declared `type fn_ret_i64 i64` collided with the
// FuncSig key for `fn() i64`, and an explicit `type ptr_i64 i64`
// would collide with `*i64`.) Indices are scoped to a single values
// decl, which already enforces uniqueness via the duplicate-projection
// check at ToAST time, so they can never collide. The cast site already
// knows the index — it walks vd.Projections to find a matching
// destination — so the rename is a pure naming change.
func projectionSymbolName(typeName string, projIndex int) string {
	return fmt.Sprintf("__projection_%s__%d",
		strings.ReplaceAll(typeName, ".", "_"),
		projIndex)
}

// compileCast compiles a type cast expression: destType(srcExpr).
// Handles integer literal coercion, same-size reinterpretation, widening, and narrowing.

// compileProjectionCast lowers `T(e)` where `e` has values type srcType
// and `T` is a declared projection of srcType. The runtime shape is an
// indexed load from the projection table the values decl emitted, but
// bas's [base+index*scale] addressing requires a register base — it
// rejects `[symbol + reg*scale]` because x86-64 RIP-relative addressing
// takes a disp32 only. So we first lea the table's address into a
// register, then index off that:
//
//	mov tag, <src expression>
//	lea base, <projection-table-symbol>
//	(elemSize in {1, 2, 4, 8})
//	  scalar  : mov dest, [base + tag*scale]
//	  memory  : lea slot, [base + tag*scale]; spot_memcpy dest, slot, sz
//	(elemSize > 8 — slice headers, structs)
//	  precompute offset = tag * elemSize (imul), then index with scale 1
//
// x86-64 SIB only encodes scales 1, 2, 4, 8, so anything else (a
// 16-byte slice-header element, for example) needs an explicit
// multiply.
func compileProjectionCast(of io.Writer, c *Context, src AST, srcType ASTType, projIdx int, projType ASTType, dest spot) spot {
	// Cross-package values: srcType.Name is "pkg.typename" (the
	// importer qualified it during Context.Import). The producer
	// emitted the projection table under the unqualified key
	// __projection_typename__index, and bas auto-qualified that
	// with the producer's package name. The consumer references the
	// already-qualified symbol explicitly so bas does not prepend
	// the consumer's package qualifier.
	pkg, leaf := splitQualifiedName(srcType.Name)
	symName := projectionSymbolName(leaf, projIdx)
	if pkg != "" {
		symName = pkg + "." + symName
	}
	tagSpot := compileTop(of, c, src, nullspot)
	base := newSpot(of, c, c.Temp(), ASTType{Name: "i64"})
	fmt.Fprintf(of, "\tlea %s %s\n", base.ref, symName)
	elemSize := projType.Size(c)

	// idxRef is the index expression to put in the SIB index slot. It
	// either points at tagSpot directly (when the SIB scale can encode
	// the element size) or at a temp holding tag*elemSize (when it
	// can't). idxScale is the SIB scale to emit.
	idxRef := tagSpot.ref
	idxScale := elemSize
	var offTmp spot
	if !sibScaleOK(elemSize) {
		// SIB can't scale; precompute the byte offset. For power-of-2
		// sizes (the common case — byte[] is 16 bytes, struct values
		// tend to align to 8/16/32), a single shl avoids loading the
		// constant. Otherwise stage the size in a scratch register and
		// imul, since bas's IMUL encoding only matches the
		// two-register form.
		offTmp = newSpot(of, c, c.Temp(), ASTType{Name: "i64"})
		fmt.Fprintf(of, "\tmov %s %s\n", offTmp.ref, tagSpot.ref)
		if shift, ok := log2PowerOf2(elemSize); ok {
			fmt.Fprintf(of, "\tshl %s %d\n", offTmp.ref, shift)
		} else {
			scratch := newSpot(of, c, c.Temp(), ASTType{Name: "i64"})
			fmt.Fprintf(of, "\tmov %s %d\n", scratch.ref, elemSize)
			fmt.Fprintf(of, "\timul %s %s\n", offTmp.ref, scratch.ref)
			scratch.free(of)
		}
		idxRef = offTmp.ref
		idxScale = 1
	}

	if typeIsMemoryBacked(c, projType) {
		// Memory-backed projection (slice headers, structs, fixed
		// arrays). lea the slot address, then memcpy into dest's
		// storage. dest must be address-style for spot_memcpy to land
		// in the right place.
		slot := newSpot(of, c, c.Temp(), ASTType{Name: "i64"})
		fmt.Fprintf(of, "\tlea %s [%s+%s*%d]\n", slot.ref, base.ref, idxRef, idxScale)
		spot_memcpy(of, c, dest, slot, elemSize)
		slot.free(of)
	} else {
		// Scalar projection: single indexed load straight into the
		// destination register.
		fmt.Fprintf(of, "\tmov %s [%s+%s*%d]\n", dest.ref, base.ref, idxRef, idxScale)
	}

	if !offTmp.empty() {
		offTmp.free(of)
	}
	base.free(of)
	tagSpot.free(of)
	return dest
}

// sibScaleOK reports whether n is one of x86-64's SIB scale values.
func sibScaleOK(n int) bool {
	return n == 1 || n == 2 || n == 4 || n == 8
}

// log2PowerOf2 returns the shift count for n when n > 0 is a power of
// two, and false otherwise. Lets the projection-cast index calculation
// pick `shl k` over `imul scratch` for the common byte[]/struct sizes.
func log2PowerOf2(n int) (int, bool) {
	if n <= 0 || n&(n-1) != 0 {
		return 0, false
	}
	s := 0
	for n > 1 {
		n >>= 1
		s++
	}
	return s, true
}

func compileCast(of io.Writer, c *Context, src AST, destType ASTType, dest spot) spot {
	if dest.empty() {
		dest = newSpot(of, c, c.Temp(), destType)
	}

	srcType := src.ASTType(c)

	// Values types are closed symbolic sets (proposal §215–222): the only
	// way to land a values-typed value is through a declared case. Reject
	// any cast that targets a values type from a source that isn't the
	// same values type — including integer literals, integer-typed
	// runtime values, and other values types.
	if _, dstIsValues := c.ValuesDeclForName(destType.Name); dstIsValues {
		if _, srcIsValues := c.ValuesDeclForName(srcType.Name); srcIsValues && srcType.Name == destType.Name {
			// Identity cast: compile straight into dest so we don't end
			// up with an un-forgetted temp from compileTop(..., nullspot).
			return compileTop(of, c, src, dest)
		}
		CompileErrorF(src, "Cannot cast %s to %s: values cases must be constructed from declared cases", srcType, destType)
	}
	// Values-to-other-type cast is a projection cast (proposal §583–596).
	// The source's values declaration knows which target types it carries
	// projections for; matching destinations lower to an indexed load
	// from the corresponding static table (emitted at values-decl
	// compile time). Anything else is rejected with the
	// no-such-projection diagnostic.
	if vd, srcIsValues := c.ValuesDeclForName(srcType.Name); srcIsValues && srcType.Name != destType.Name {
		projIdx := -1
		for i, pt := range vd.Projections {
			if pt.Same(destType) {
				projIdx = i
				break
			}
		}
		if projIdx < 0 {
			CompileErrorF(src, "Cannot cast %s to %s: %s has no %s projection", srcType, destType, srcType, destType)
		}
		return compileProjectionCast(of, c, src, srcType, projIdx, vd.Projections[projIdx], dest)
	}

	// Integer literal: compile directly into the destination type.
	if srcType.Same(intlitASTType()) {
		val, ok := EvalConst(c, src)
		if !ok {
			CompileErrorF(src, "Could not evaluate integer literal at compile time")
		}
		underlying := c.ResolveUnderlying(destType)
		if !litFitsIn(val, underlying) {
			CompileErrorF(src, "Literal %s does not fit in type %s", val, destType)
		}
		fmt.Fprintf(of, "\tmov %s %s\n", dest.ref, val.String())
		return dest
	}

	srcUnderlying := c.ResolveUnderlying(srcType)
	dstUnderlying := c.ResolveUnderlying(destType)
	srcSize := srcUnderlying.Size(c)
	dstSize := dstUnderlying.Size(c)

	// Reject category-mismatched casts. A same-width `mov` between a pointer
	// and a non-pointer is bit reinterpretation, not a type conversion: it
	// launders pointer-ness in either direction, hiding the value from the
	// alias tracker and ownership tracker. If a meaningful unsafe form is
	// needed later, it should be syntactically distinct from the regular
	// T(expr) cast.
	srcIsPtr := srcUnderlying.Indirection > 0
	dstIsPtr := dstUnderlying.Indirection > 0
	if srcIsPtr != dstIsPtr {
		CompileErrorF(src, "Cannot cast between pointer and non-pointer types: %s to %s", srcType, destType)
	}
	// Slice and array shapes carry their indirection on Element, not
	// Indirection, so the pointer-vs-scalar guard above misses
	// `byte[](n)` where n is i64. A 16-byte slice-header destination
	// against an 8-byte scalar source would otherwise fall through to
	// movsx/movzx and surface as a confusing bas-level "no encoding"
	// failure. Reject the cross-shape cast here with a clear bosc
	// diagnostic; the parser now exposes the `T[](e)` syntax for
	// projection casts so this category of mistake is easier to write
	// than before.
	srcIsSlice := srcUnderlying.IsSlice()
	dstIsSlice := dstUnderlying.IsSlice()
	srcIsArray := srcUnderlying.IsArray()
	dstIsArray := dstUnderlying.IsArray()
	if srcIsSlice != dstIsSlice || srcIsArray != dstIsArray {
		CompileErrorF(src, "Cannot cast between scalar and slice/array types: %s to %s", srcType, destType)
	}
	// Struct destinations are only reachable as a cast target through
	// a values projection (handled above) or as an identity copy. Any
	// other cross-kind cast — `pair(42)`, `pair(some_i64)`, or
	// `pair(other_struct)` — has no meaningful encoding; the fallthrough
	// to a same-size mov would memcpy bytes from a differently-shaped
	// source and produce garbage. Reject here with a clear bosc
	// diagnostic so the user sees the actual problem instead of a
	// downstream bas size mismatch.
	_, srcIsStruct := c.StructDeclForName(srcType.Name)
	_, dstIsStruct := c.StructDeclForName(destType.Name)
	if srcIsStruct != dstIsStruct {
		CompileErrorF(src, "Cannot cast between struct and non-struct types: %s to %s", srcType, destType)
	}
	if dstIsStruct && srcType.Name != destType.Name {
		CompileErrorF(src, "Cannot cast between distinct struct types: %s to %s", srcType, destType)
	}
	// Same-struct identity cast (`pair(p)` where p is also pair) is
	// rejected because the size-equal fallthrough below would emit a
	// scalar `mov dest src` against memory-backed slots and copy only
	// the first 8 bytes. Direct assignment (`var q pair = p`) is the
	// way to copy a struct value; there is no use for a values-style
	// cast of a struct onto itself.
	if dstIsStruct && srcType.Name == destType.Name {
		CompileErrorF(src, "Cannot cast %s to itself; assign directly instead of using a cast", srcType)
	}

	// Compile the source value.
	srcSpot := compileTop(of, c, src, nullspot)

	switch {
	case srcSize == dstSize:
		// Same size: just copy and relabel the type. Zero cost for same-width aliases.
		fmt.Fprintf(of, "\tmov %s %s\n", dest.ref, srcSpot.ref)
	case dstSize > srcSize:
		// Widening: sign- or zero-extend.
		// Signed 32→64: MOVSX r64, r/m32 doesn't exist; use MOVSXD instead.
		// Unsigned 32→64: MOVZX r64, r/m32 doesn't exist either, but bas
		// recognizes it as a synthetic instruction and translates it.
		if srcUnderlying.Signed && srcSize == 4 && dstSize == 8 {
			fmt.Fprintf(of, "\tmovsxd %s %s\n", dest.ref, srcSpot.ref)
		} else if srcUnderlying.Signed {
			fmt.Fprintf(of, "\tmovsx %s %s\n", dest.ref, srcSpot.ref)
		} else {
			fmt.Fprintf(of, "\tmovzx %s %s\n", dest.ref, srcSpot.ref)
		}
	default:
		// Narrowing: use the partial-of-alloc syntax to take the low N bits
		// of the source, where N is the destination size in bits.
		fmt.Fprintf(of, "\tmov %s %s:%d\n", dest.ref, srcSpot.ref, dstSize*8)
	}
	srcSpot.free(of)
	return dest
}

// litFitsIn reports whether val is in the mathematical range for type t.
// Caller should pass c.ResolveUnderlying(t) when t may be a type alias.
func litFitsIn(val *big.Int, t ASTType) bool {
	var lo, hi *big.Int
	switch t.Name {
	case "i8":
		lo, hi = big.NewInt(-128), big.NewInt(127)
	case "u8", "byte":
		lo, hi = big.NewInt(0), big.NewInt(255)
	case "i16":
		lo, hi = big.NewInt(-32768), big.NewInt(32767)
	case "u16":
		lo, hi = big.NewInt(0), big.NewInt(65535)
	case "i32":
		lo, hi = big.NewInt(-2147483648), big.NewInt(2147483647)
	case "u32":
		lo, hi = big.NewInt(0), new(big.Int).SetUint64(4294967295)
	case "i64":
		lo = new(big.Int).Neg(new(big.Int).Lsh(big.NewInt(1), 63))
		hi = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 63), big.NewInt(1))
	case "u64":
		lo = big.NewInt(0)
		hi = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 64), big.NewInt(1))
	default:
		return false
	}
	return val.Cmp(lo) >= 0 && val.Cmp(hi) <= 0
}

// resolveOperandType returns the concrete type for a binary operator's operands.
// If one side is <intlit> and the other is concrete, the concrete type wins.
// If both are <intlit>, defaults to i64.
// checkValuesComparisonCompat enforces proposal §722–733 on the closed
// symbolic set:
//   - For equality (==/!=), values types compare only with the *same*
//     values type; cross-type compare is rejected.
//   - For ordering (</>/<=/>=), any values type involvement is rejected,
//     because the tag order is a compiler-private encoding the
//     proposal explicitly keeps unreachable.
//
// Ordinary integer aliases (e.g. `type FD i64` / `type Pid i64`) keep
// their underlying-i64 compatibility — the rejection here is scoped to
// values types specifically, not generalized to all named numeric
// aliases.
func checkValuesComparisonCompat(c *Context, o *Op2, isOrdering bool) {
	ft := o.First.ASTType(c)
	st := o.Second.ASTType(c)
	_, fIsValues := c.ValuesDeclForName(ft.Name)
	_, sIsValues := c.ValuesDeclForName(st.Name)
	if !fIsValues && !sIsValues {
		return
	}
	if isOrdering {
		CompileErrorF(o.First, "Cannot order values of types %s and %s; values types are closed symbolic sets, not numeric", ft, st)
	}
	if fIsValues && sIsValues && ft.Name == st.Name {
		return
	}
	CompileErrorF(o.First, "Cannot compare values of types %s and %s", ft, st)
}

func resolveOperandType(ft, st ASTType) ASTType {
	intlit := intlitASTType()
	if !ft.Same(intlit) {
		return ft
	}
	if !st.Same(intlit) {
		return st
	}
	return numASTType()
}

func sameInterfaceType(c *Context, a, b ASTType) bool {
	a = a.StripOwned()
	b = b.StripOwned()
	if !c.IsInterfaceType(a) || !c.IsInterfaceType(b) {
		return false
	}
	if a.Name == b.Name {
		return true
	}
	ad, aok := c.InterfaceForName(a.Name)
	bd, bok := c.InterfaceForName(b.Name)
	return aok && bok && ad == bd
}

func compareUsesInterface(c *Context, o *Op2) bool {
	return c.IsInterfaceType(o.First.ASTType(c).StripOwned()) ||
		c.IsInterfaceType(o.Second.ASTType(c).StripOwned())
}

func compileAsInterfaceValue(of io.Writer, c *Context, errNode AST, ifaceType ASTType, val AST) spot {
	ifaceType = ifaceType.StripOwned()
	valType := val.ASTType(c)
	// Asserting/widening/comparing *from* a nullable interface dereferences its
	// vtable (vtable[0] = typedesc); a null source would crash. Require it
	// narrowed first, exactly like dispatch.
	if vt := valType.StripOwned(); c.IsInterfaceType(vt) && vt.NilMask&1 != 0 {
		CompileErrorF(errNode, "%s is a nullable interface and may be null; narrow it with `if (... != nil)` first", vt)
	}
	if c.IsInterfaceType(valType.StripOwned()) {
		if !sameInterfaceType(c, ifaceType, valType) {
			CompileErrorF(val, "Cannot compare interface values of types %s and %s", ifaceType, valType)
		}
		return compileTop(of, c, val, nullspot)
	}
	dst := newSpot(of, c, c.Temp(), ifaceType)
	emitInterfaceFatPtr(of, c, errNode, ifaceType, valType, val, dst.ref, "")
	return dst
}

// litIsNil reports whether an expression is the `nil` literal.
func litIsNil(ast AST) bool {
	lit, ok := ast.(*Literal)
	return ok && lit.Val == nil
}

func compileInterfaceEquality(of io.Writer, c *Context, o *Op2, dest spot) spot {
	// `k == nil` / `k != nil`: a (nullable) interface is null iff its vtable
	// slot is 0. Compare that slot to 0 rather than attempting full interface
	// equality — nil has no interface value to coerce against.
	if litIsNil(o.First) || litIsNil(o.Second) {
		ifaceOperand := o.First
		if litIsNil(o.First) {
			ifaceOperand = o.Second
		}
		iv := compileTop(of, c, ifaceOperand, nullspot)
		defer iv.free(of)
		if dest.empty() {
			dest = newSpot(of, c, c.Temp(), boolASTType())
		}
		vt := newSpot(of, c, c.Temp(), ASTType{Name: "i64"})
		fmt.Fprintf(of, "\tmov %s [%s+8]\n", vt.ref, iv.ref) // vtable slot
		fmt.Fprintf(of, "\tcmp %s 0\n", vt.ref)
		if o.Type == n_deq {
			fmt.Fprintf(of, "\tsete %s\n", dest.ref)
		} else {
			fmt.Fprintf(of, "\tsetne %s\n", dest.ref)
		}
		vt.free(of)
		return dest
	}
	ft := o.First.ASTType(c).StripOwned()
	st := o.Second.ASTType(c).StripOwned()
	var ifaceType ASTType
	switch {
	case c.IsInterfaceType(ft) && c.IsInterfaceType(st):
		if !sameInterfaceType(c, ft, st) {
			CompileErrorF(o.First, "Cannot compare interface values of types %s and %s", ft, st)
		}
		ifaceType = ft
	case c.IsInterfaceType(ft):
		ifaceType = ft
	case c.IsInterfaceType(st):
		ifaceType = st
	default:
		panic("compileInterfaceEquality called without an interface operand")
	}

	first := compileAsInterfaceValue(of, c, o.First, ifaceType, o.First)
	second := compileAsInterfaceValue(of, c, o.Second, ifaceType, o.Second)
	defer first.free(of)
	defer second.free(of)

	if dest.empty() {
		dest = newSpot(of, c, c.Temp(), boolASTType())
	}
	firstData := newSpot(of, c, c.Temp(), ASTType{Name: "i64"})
	firstVtable := newSpot(of, c, c.Temp(), ASTType{Name: "i64"})
	firstTypedesc := newSpot(of, c, c.Temp(), ASTType{Name: "i64"})
	secondData := newSpot(of, c, c.Temp(), ASTType{Name: "i64"})
	secondVtable := newSpot(of, c, c.Temp(), ASTType{Name: "i64"})
	secondTypedesc := newSpot(of, c, c.Temp(), ASTType{Name: "i64"})
	defer firstData.free(of)
	defer firstVtable.free(of)
	defer firstTypedesc.free(of)
	defer secondData.free(of)
	defer secondVtable.free(of)
	defer secondTypedesc.free(of)

	fmt.Fprintf(of, "\tmov %s [%s+0]\n", firstData.ref, first.ref)
	fmt.Fprintf(of, "\tmov %s [%s+8]\n", firstVtable.ref, first.ref)
	fmt.Fprintf(of, "\tmov %s [%s+0]\n", firstTypedesc.ref, firstVtable.ref)
	fmt.Fprintf(of, "\tmov %s [%s+0]\n", secondData.ref, second.ref)
	fmt.Fprintf(of, "\tmov %s [%s+8]\n", secondVtable.ref, second.ref)
	fmt.Fprintf(of, "\tmov %s [%s+0]\n", secondTypedesc.ref, secondVtable.ref)

	done := c.Label("ifacecmp")
	if o.Type == n_neq {
		fmt.Fprintf(of, "\tmov %s 1\n", dest.ref)
		fmt.Fprintf(of, "\tcmp %s %s\n", firstTypedesc.ref, secondTypedesc.ref)
		fmt.Fprintf(of, "\tjne %s\n", done)
		fmt.Fprintf(of, "\tcmp %s %s\n", firstData.ref, secondData.ref)
		fmt.Fprintf(of, "\tjne %s\n", done)
		fmt.Fprintf(of, "\tmov %s 0\n", dest.ref)
		fmt.Fprintf(of, "\tlabel %s\n", done)
		return dest
	}
	fmt.Fprintf(of, "\tmov %s 0\n", dest.ref)
	fmt.Fprintf(of, "\tcmp %s %s\n", firstTypedesc.ref, secondTypedesc.ref)
	fmt.Fprintf(of, "\tjne %s\n", done)
	fmt.Fprintf(of, "\tcmp %s %s\n", firstData.ref, secondData.ref)
	fmt.Fprintf(of, "\tjne %s\n", done)
	fmt.Fprintf(of, "\tmov %s 1\n", dest.ref)
	fmt.Fprintf(of, "\tlabel %s\n", done)
	return dest
}

func doOp2(of io.Writer, c *Context, o *Op2, dest spot) spot {
	//if dest.empty() {
	//dest = newSpot(of, c, c.Temp(), o.ASTType(c))
	//}
	// TODO: Validate operation types
	// if !o.First.ASTType(c).Same(numASTType()) {
	// 	panic("Cannot perform comparisons on non-numeric types.")
	// }
	switch o.Type {
	case n_add:
		fst := newSpot(of, c, c.Temp(), o.ASTType(c))
		first := compileTop(of, c, o.First, fst)
		var second spot
		if o.Second.ASTType(c).Same(o.ASTType(c)) {
			second = compileTop(of, c, o.Second, nullspot)
		} else {
			// If they are different types, we must create a spot
			// with the right type so the sub operands are of the right
			// size. This is for things like var n byte; n = n - 1
			snd := newSpot(of, c, c.Temp(), o.ASTType(c))
			second = compileTop(of, c, o.Second, snd)
		}
		fmt.Fprintf(of, "\tadd %s %s\n", first.ref, second.ref)
		if dest.empty() {
			dest = first
		} else {
			move(of, c, dest, first)
			first.free(of)
		}
		second.free(of)
		return dest
		// first := compileTop(of, c, o.First, dest)
		// if first == dest {
		// 	dest = newSpot(of, c, c.Temp(), o.ASTType(c))
		// }
		// second := compileTop(of, c, o.Second, dest)
		// fmt.Fprintf(of, "\tadd %s %s\n", first.ref, second.ref)
		// return first
	case n_sub:
		fst := newSpot(of, c, c.Temp(), o.ASTType(c))
		first := compileTop(of, c, o.First, fst)
		var second spot
		if o.Second.ASTType(c).Same(o.ASTType(c)) {
			second = compileTop(of, c, o.Second, nullspot)
		} else {
			// If they are different types, we must create a spot
			// with the right type so the sub operands are of the right
			// size. This is for things like var n byte; n = n - 1
			snd := newSpot(of, c, c.Temp(), o.ASTType(c))
			second = compileTop(of, c, o.Second, snd)
		}
		fmt.Fprintf(of, "\tsub %s %s\n", first.ref, second.ref)
		if dest.empty() {
			dest = first
		} else {
			move(of, c, dest, first)
			first.free(of)
		}
		second.free(of)
		return dest
		// first := compileTop(of, c, o.First, dest)
		// if first == dest {
		// 	dest = newSpot(of, c, c.Temp(), o.ASTType(c))
		// }
		// second := compileTop(of, c, o.Second, dest)
		// fmt.Fprintf(of, "\tsub %s %s\n", first.ref, second.ref)
		// return first
	case n_bitand:
		fst := newSpot(of, c, c.Temp(), o.ASTType(c))
		first := compileTop(of, c, o.First, fst)
		var second spot
		if o.Second.ASTType(c).Same(o.ASTType(c)) {
			second = compileTop(of, c, o.Second, nullspot)
		} else {
			snd := newSpot(of, c, c.Temp(), o.ASTType(c))
			second = compileTop(of, c, o.Second, snd)
		}
		fmt.Fprintf(of, "\tand %s %s\n", first.ref, second.ref)
		if dest.empty() {
			dest = first
		} else {
			move(of, c, dest, first)
			first.free(of)
		}
		second.free(of)
		return dest
	case n_bitor:
		fst := newSpot(of, c, c.Temp(), o.ASTType(c))
		first := compileTop(of, c, o.First, fst)
		var second spot
		if o.Second.ASTType(c).Same(o.ASTType(c)) {
			second = compileTop(of, c, o.Second, nullspot)
		} else {
			snd := newSpot(of, c, c.Temp(), o.ASTType(c))
			second = compileTop(of, c, o.Second, snd)
		}
		fmt.Fprintf(of, "\tor %s %s\n", first.ref, second.ref)
		if dest.empty() {
			dest = first
		} else {
			move(of, c, dest, first)
			first.free(of)
		}
		second.free(of)
		return dest
	case n_mul:
		ot := o.ASTType(c)
		sz := ot.Size(c)
		if sz == 1 {
			CompileErrorF(o.First, "8-bit multiplication not supported")
		}
		raxName, rdxName := mulDivRegs(sz * 8)
		signed := ot.Signed

		// Always compute into a fresh temporary, never into the caller's dest
		// directly. This avoids 'inreg' on user variables (which breaks volatile).
		// The result is moved into dest at the end if needed.
		var result spot
		if signed && sz == 8 {
			// 64-bit signed: two-operand IMUL r64, r/m64.
			// No implicit rax dependency, no inreg on user variables.
			// Second operand can be register or memory — bas picks the form.
			tmp := newSpot(of, c, c.Temp(), ot)
			first := compileTop(of, c, o.First, tmp)
			if !first.same(&tmp) {
				move(of, c, tmp, first)
				first.free(of)
			}
			second := compileTop(of, c, o.Second, nullspot)
			fmt.Fprintf(of, "\timul %s %s\n", tmp.ref, second.ref)
			second.free(of)
			result = tmp
		} else {
			// For sub-64-bit signed and all unsigned: one-operand form via rax.
			// Load first into a fresh rax-pinned temp (uses 'mov', not 'inreg').
			// Then inreg the temp (safe: fresh temp is never volatile).
			tmp := newSpotWithReg(of, c, c.Temp(), ot, raxName)
			first := compileTop(of, c, o.First, tmp)
			if !first.same(&tmp) {
				move(of, c, tmp, first)
				first.free(of)
			}
			second := compileTop(of, c, o.Second, nullspot)
			// Ensure first is in rax; compiling second may have evicted it.
			// Safe: tmp is a fresh Temp, never volatile.
			fmt.Fprintf(of, "\tinreg %s %s\n", tmp.ref, raxName)
			// Acquire the rdx-equivalent to protect any live variable there.
			// One-operand MUL/IMUL writes the high half to rdx as a side effect.
			rdx := regSpot(of, rdxName)
			if signed {
				fmt.Fprintf(of, "\timul %s\n", second.ref)
			} else {
				fmt.Fprintf(of, "\tmul %s\n", second.ref)
			}
			rdx.free(of)
			second.free(of)
			result = tmp
		}
		if !dest.empty() && !result.same(&dest) {
			move(of, c, dest, result)
			result.free(of)
			return dest
		}
		return result
	case n_div:
		ot := o.ASTType(c)
		sz := ot.Size(c)
		if sz == 1 {
			CompileErrorF(o.First, "8-bit division not supported")
		}
		raxName, rdxName := mulDivRegs(sz * 8)
		signed := ot.Signed

		// Always compute into a fresh rax-pinned temporary (never the caller's
		// dest). compileTop loads the dividend via 'mov rax src', which handles
		// the memory form for volatile variables without needing 'inreg'.
		tmp := newSpotWithReg(of, c, c.Temp(), ot, raxName)
		first := compileTop(of, c, o.First, tmp)
		if !first.same(&tmp) {
			move(of, c, tmp, first)
			first.free(of)
		}
		second := compileTop(of, c, o.Second, nullspot)
		// Ensure the dividend is in rax before the division instruction.
		// Compiling the second operand (e.g. a funcall) may have evicted tmp
		// from rax to its stack slot. inreg reloads it. tmp is always a fresh
		// Temp (never volatile), so this is safe.
		fmt.Fprintf(of, "\tinreg %s %s\n", tmp.ref, raxName)
		rdx := regSpot(of, rdxName)
		if signed {
			switch sz {
			case 8:
				fmt.Fprintf(of, "\tcqo\n")
			case 4:
				fmt.Fprintf(of, "\tcdq\n")
			case 2:
				fmt.Fprintf(of, "\tcwd\n")
			}
			fmt.Fprintf(of, "\tidiv %s\n", second.ref)
		} else {
			fmt.Fprintf(of, "\txor %s %s\n", rdxName, rdxName)
			fmt.Fprintf(of, "\tdiv %s\n", second.ref)
		}
		rdx.free(of)
		second.free(of)
		if !dest.empty() && !tmp.same(&dest) {
			move(of, c, dest, tmp)
			tmp.free(of)
			return dest
		}
		return tmp
	case n_lt:
		if compareUsesInterface(c, o) {
			CompileErrorF(o.First, "Cannot order interface values")
		}
		checkValuesComparisonCompat(c, o, true)
		ft := resolveOperandType(o.First.ASTType(c), o.Second.ASTType(c))
		fst := newSpot(of, c, c.Temp(), ft)
		first := compileTop(of, c, o.First, fst)
		var second spot
		if o.Second.ASTType(c).Same(ft) {
			second = compileTop(of, c, o.Second, nullspot)
		} else {
			snd := newSpot(of, c, c.Temp(), ft)
			second = compileTop(of, c, o.Second, snd)
		}
		if dest.empty() {
			dest = newSpot(of, c, c.Temp(), boolASTType())
		}
		fmt.Fprintf(of, "\tcmp %s %s\n", first.ref, second.ref)
		if ft.Signed {
			fmt.Fprintf(of, "\tsetl %s\n", dest.ref)
		} else {
			fmt.Fprintf(of, "\tsetb %s\n", dest.ref)
		}
		return dest
	case n_le:
		if compareUsesInterface(c, o) {
			CompileErrorF(o.First, "Cannot order interface values")
		}
		checkValuesComparisonCompat(c, o, true)
		ft := resolveOperandType(o.First.ASTType(c), o.Second.ASTType(c))
		fst := newSpot(of, c, c.Temp(), ft)
		first := compileTop(of, c, o.First, fst)
		var second spot
		if o.Second.ASTType(c).Same(ft) {
			second = compileTop(of, c, o.Second, nullspot)
		} else {
			snd := newSpot(of, c, c.Temp(), ft)
			second = compileTop(of, c, o.Second, snd)
		}
		if dest.empty() {
			dest = newSpot(of, c, c.Temp(), boolASTType())
		}
		fmt.Fprintf(of, "\tcmp %s %s\n", first.ref, second.ref)
		if ft.Signed {
			fmt.Fprintf(of, "\tsetle %s\n", dest.ref)
		} else {
			fmt.Fprintf(of, "\tsetbe %s\n", dest.ref)
		}
		return dest
	case n_gt:
		if compareUsesInterface(c, o) {
			CompileErrorF(o.First, "Cannot order interface values")
		}
		checkValuesComparisonCompat(c, o, true)
		ft := resolveOperandType(o.First.ASTType(c), o.Second.ASTType(c))
		fst := newSpot(of, c, c.Temp(), ft)
		first := compileTop(of, c, o.First, fst)
		var second spot
		if o.Second.ASTType(c).Same(ft) {
			second = compileTop(of, c, o.Second, nullspot)
		} else {
			snd := newSpot(of, c, c.Temp(), ft)
			second = compileTop(of, c, o.Second, snd)
		}
		if dest.empty() {
			dest = newSpot(of, c, c.Temp(), boolASTType())
		}
		fmt.Fprintf(of, "\tcmp %s %s\n", first.ref, second.ref)
		if ft.Signed {
			fmt.Fprintf(of, "\tsetg %s\n", dest.ref)
		} else {
			fmt.Fprintf(of, "\tseta %s\n", dest.ref)
		}
		return dest
	case n_ge:
		if compareUsesInterface(c, o) {
			CompileErrorF(o.First, "Cannot order interface values")
		}
		checkValuesComparisonCompat(c, o, true)
		ft := resolveOperandType(o.First.ASTType(c), o.Second.ASTType(c))
		fst := newSpot(of, c, c.Temp(), ft)
		first := compileTop(of, c, o.First, fst)
		var second spot
		if o.Second.ASTType(c).Same(ft) {
			second = compileTop(of, c, o.Second, nullspot)
		} else {
			snd := newSpot(of, c, c.Temp(), ft)
			second = compileTop(of, c, o.Second, snd)
		}
		if dest.empty() {
			dest = newSpot(of, c, c.Temp(), boolASTType())
		}
		fmt.Fprintf(of, "\tcmp %s %s\n", first.ref, second.ref)
		if ft.Signed {
			fmt.Fprintf(of, "\tsetge %s\n", dest.ref)
		} else {
			fmt.Fprintf(of, "\tsetae %s\n", dest.ref)
		}
		return dest
	case n_deq:
		if compareUsesInterface(c, o) {
			return compileInterfaceEquality(of, c, o, dest)
		}
		checkValuesComparisonCompat(c, o, false)
		ft := resolveOperandType(o.First.ASTType(c), o.Second.ASTType(c))
		fst := newSpot(of, c, c.Temp(), ft)
		first := compileTop(of, c, o.First, fst)
		var second spot
		if o.Second.ASTType(c).Same(ft) {
			snd := newSpot(of, c, c.Temp(), ft)
			second = compileTop(of, c, o.Second, snd)
		} else {
			snd := newSpot(of, c, c.Temp(), ft)
			second = compileTop(of, c, o.Second, snd)
		}
		if dest.empty() {
			dest = newSpot(of, c, c.Temp(), boolASTType())
		}
		fmt.Fprintf(of, "\tcmp %s %s\n", first.ref, second.ref)
		fmt.Fprintf(of, "\tsete %s\n", dest.ref)
		return dest
	case n_neq:
		if compareUsesInterface(c, o) {
			return compileInterfaceEquality(of, c, o, dest)
		}
		checkValuesComparisonCompat(c, o, false)
		ft := resolveOperandType(o.First.ASTType(c), o.Second.ASTType(c))
		fst := newSpot(of, c, c.Temp(), ft)
		first := compileTop(of, c, o.First, fst)
		var second spot
		if o.Second.ASTType(c).Same(ft) {
			second = compileTop(of, c, o.Second, nullspot)
		} else {
			snd := newSpot(of, c, c.Temp(), ft)
			second = compileTop(of, c, o.Second, snd)
		}
		if dest.empty() {
			dest = newSpot(of, c, c.Temp(), boolASTType())
		}
		fmt.Fprintf(of, "\tcmp %s %s\n", first.ref, second.ref)
		fmt.Fprintf(of, "\tsetne %s\n", dest.ref)
		return dest
	case n_booland:
		first := compileTop(of, c, o.First, nullspot)
		second := compileTop(of, c, o.Second, nullspot)
		if dest.empty() {
			dest = newSpot(of, c, c.Temp(), boolASTType())
		}
		fbool := newSpot(of, c, c.Temp(), byteASTType())
		sbool := newSpot(of, c, c.Temp(), byteASTType())
		fmt.Fprintf(of, "\ttest %s %s\n", first.ref, first.ref)
		fmt.Fprintf(of, "\tseta %s\n", fbool.ref)

		fmt.Fprintf(of, "\ttest %s %s\n", second.ref, second.ref)
		fmt.Fprintf(of, "\tseta %s\n", sbool.ref)

		fmt.Fprintf(of, "\tand %s %s\n", fbool.ref, sbool.ref)
		fmt.Fprintf(of, "\tseta %s\n", dest.ref)
		fbool.free(of)
		sbool.free(of)
		first.free(of)
		second.free(of)
		return dest
	case n_boolor:
		first := compileTop(of, c, o.First, nullspot)
		second := compileTop(of, c, o.Second, nullspot)
		if dest.empty() {
			dest = newSpot(of, c, c.Temp(), boolASTType())
		}
		fbool := newSpot(of, c, c.Temp(), byteASTType())
		sbool := newSpot(of, c, c.Temp(), byteASTType())
		fmt.Fprintf(of, "\ttest %s %s\n", first.ref, first.ref)
		fmt.Fprintf(of, "\tseta %s\n", fbool.ref)

		fmt.Fprintf(of, "\ttest %s %s\n", second.ref, second.ref)
		fmt.Fprintf(of, "\tseta %s\n", sbool.ref)

		fmt.Fprintf(of, "\tor %s %s\n", fbool.ref, sbool.ref)
		fmt.Fprintf(of, "\tseta %s\n", dest.ref)
		fbool.free(of)
		sbool.free(of)
		first.free(of)
		second.free(of)
		return dest
	}
	panic("Could not do op\n")
}

// compileConcreteMethodCall desugars v.method(args) into TypeName.method(receiver, args)
// and delegates to compileTop. The receiver is passed by address when the method
// declares a pointer receiver and by value when the method declares a value
// receiver; see the inline comment for the dispatch rule.
func compileConcreteMethodCall(of io.Writer, c *Context, a AST, callNode *Funcall,
	receiverType ASTType, typeName string, method *FuncDecl, dest spot) spot {
	// A method's declared first-arg type chooses whether we pass the
	// receiver by value or by address. `type FD i64 { read(fd *FD, ...) }`
	// wants a pointer, so `f.read(...)` becomes `read(&f, ...)`. A
	// values-type value-receiver method like `name(c color) byte[]` wants
	// the value itself, and taking `&c` would mismatch the parameter type
	// (and force a non-addressable register-resident value to memory).
	wantsPointerReceiver := len(method.Args) > 0 && method.Args[0].Type.Indirection > 0
	var receiver AST
	if recv := callNode.ReceiverExpr(); recv != nil {
		if wantsPointerReceiver && receiverType.Indirection == 0 {
			CompileErrorF(callNode, "cannot call method on non-addressable expression receiver (type %s)", receiverType)
		}
		receiver = recv
	} else if receiverType.Indirection > 0 || !wantsPointerReceiver {
		receiver = &Symbol{Name: callNode.PkgName(), p: callNode.p}
	} else {
		receiver = &Address{Var: callNode.PkgName(), p: callNode.p}
	}
	allArgs := make([]AST, 0, 1+len(callNode.Args))
	allArgs = append(allArgs, receiver)
	allArgs = append(allArgs, callNode.Args...)
	// If typeName is qualified (e.g. "io.FD"), split so the Funcall routes
	// through c.imports["io"]["FD.method"] rather than c.funcs["io.FD.method"].
	mname := callNode.FName()
	synthPkg := ""
	synthName := typeName + "." + mname
	if dot := strings.LastIndex(typeName, "."); dot >= 0 {
		synthPkg = typeName[:dot]
		synthName = typeName[dot+1:] + "." + mname
	}
	var callee AST
	if synthPkg != "" {
		callee = &Dot{
			Val:    &Symbol{Name: synthPkg, p: callNode.p},
			Member: synthName,
		}
	} else {
		callee = &Symbol{Name: synthName, p: callNode.p}
	}
	synthCall := &Funcall{
		Callee: callee,
		Args:   allArgs,
		p:      callNode.p,
	}
	return compileTop(of, c, synthCall, dest)
}

// compileInterfaceMethodCall emits a vtable-dispatch call for an interface method.
// The interface variable (named by callNode.PkgName()) is a memory-backed
// 16-byte fat pointer: [var+0] = data word, [var+8] = vtable pointer. For
// pointer-backed interfaces the data word is a pointer; for value-backed
// interfaces it is the concrete value bits. The data word is passed as the
// first argument (rdi); user-supplied args follow (rsi, rdx, ...).
func compileInterfaceMethodCall(of io.Writer, c *Context, a AST, callNode *Funcall,
	ifaceType ASTType, ifaceDecl *InterfaceDecl, dest spot) spot {

	mname := callNode.FName()
	// A nullable interface (`?T`) may be null — dispatching through it would
	// deref a null vtable. Reject until it is narrowed to non-null (the type
	// read strips the nullable bit inside `if (k != nil)`), mirroring the
	// rejection of a `*?` pointer deref.
	if ifaceType.NilMask&1 != 0 {
		CompileErrorF(a, "%s is a nullable interface and may be null; narrow it with `if (... != nil)` before calling %s", ifaceType, mname)
	}
	// Locate the method and its index in the interface.
	methodIdx := -1
	var isig InterfaceMethodSig
	for i, m := range ifaceDecl.Methods {
		if m.Name == mname {
			methodIdx = i
			isig = m
			break
		}
	}
	if methodIdx < 0 {
		CompileErrorF(a, "Interface %s has no method %s", ifaceDecl.Name, mname)
	}

	// Params[0] is the receiver; user provides Params[1:].
	receiverParam := isig.Params[0].Type
	userParams := isig.Params[1:]
	if len(callNode.Args) != len(userParams) {
		CompileErrorF(a, "Method %s.%s expected %d arguments, but called with %d",
			ifaceDecl.Name, mname, len(userParams), len(callNode.Args))
	}

	// The interface variable is memory-backed; its name is the base address.
	ifaceRef := callNode.PkgName()
	if receiverParam.HasOwned() {
		if !ifaceType.HasOwned() {
			CompileErrorF(a, "Cannot call consuming method %s.%s on non-owned %s", ifaceDecl.Name, mname, ifaceType)
		}
		if c.IsMoved(ifaceRef) {
			CompileErrorF(a, "Cannot move \"%s\": it was already moved", ifaceRef)
		}
	}

	// Validate the data pointer's flow-state origin before dispatch. If the
	// interface was constructed by borrowing &x and x has since been moved,
	// disposed, or fallen out of scope, the data slot aliases storage that
	// is no longer trustworthy. The field-pointer registered by
	// emitInterfaceFatPtr carries the source's origin; CheckDerefValidity
	// rejects the dispatch with the same wording the bare *T deref uses.
	if !ifaceType.HasOwned() {
		dataField := c.PointerFlow().GetFieldPointer(flow.Binding(ifaceRef), "data")
		if ok, reason := c.PointerFlow().CheckDerefValidity(dataField); !ok {
			CompileErrorF(a, "%s", reason)
		}
	}

	// Load data ptr, vtable ptr, and fn ptr into temps BEFORE setting up call
	// registers, so they are not evicted by arg compilation.
	dataPtr := newSpot(of, c, c.Temp(), ASTType{Name: "i64"})
	vtablePtr := newSpot(of, c, c.Temp(), ASTType{Name: "i64"})
	fnPtr := newSpot(of, c, c.Temp(), ASTType{Name: "i64"})
	fmt.Fprintf(of, "\tmov %s [%s+0]\n", dataPtr.ref, ifaceRef)
	fmt.Fprintf(of, "\tmov %s [%s+8]\n", vtablePtr.ref, ifaceRef)
	// Slot 0 = typedesc, slot 1 = shape word, methods at slot 2+.
	fmt.Fprintf(of, "\tmov %s [%s+%d]\n", fnPtr.ref, vtablePtr.ref, (methodIdx+2)*8)
	vtablePtr.free(of)

	order := []string{"rdi", "rsi", "rdx", "rcx", "r8", "r9"}

	// Compile user args into temp spots. They go into rsi+ (index 1+);
	// rdi (index 0) is reserved for the data pointer.
	var argspots []spot
	// coercedToInterface[i] marks args handled by the inline coercion block;
	// their owned-source consumption happens inline, so the post-arg
	// move-marking loop must skip them (markMovedIfOwnedSource is not
	// idempotent — a second pass would observe the already-moved binding and
	// spuriously error). Parallels the ordinary-call path in setupArgs.
	coercedToInterface := make([]bool, len(callNode.Args))
	for i, arg := range callNode.Args {
		param := userParams[i].Type
		argt := arg.ASTType(c)
		argIdx := i + 1 // rsi=1, rdx=2, ...
		if argIdx > 5 {
			CompileErrorF(arg, "More than 5 user arguments not supported for interface methods")
		}
		checkAddressOfOwnedForDest(c, arg, param)
		// Interface coercion at an interface-method call site: a concrete
		// pointer or small value argument passed where the method declares an
		// interface parameter must be wrapped into a fat pointer, exactly as
		// the ordinary-call path does. Without this, passing e.g. a
		// *mut counting_writer to a Formatter.format(w io.writer) param fails
		// the plain coerceType check.
		if shouldCoerceToInterface(c, param, argt) {
			s := newSpot(of, c, c.Temp(), param)
			emitInterfaceFatPtr(of, c, arg, param, argt, arg, s.ref, "")
			if param.HasOwned() {
				markMovedIfOwnedSource(of, c, param, arg)
			}
			coercedToInterface[i] = true
			argspots = append(argspots, s)
			continue
		}
		effective, reason := coerceType(c, param, argt)
		if reason != coerceOK {
			reportCoerceFailure(arg, param, argt, reason,
				"For argument %d, expected type %v but got %v", i, param, argt)
		}
		argt = effective
		if param.HasOwned() {
			checkOwnedSourceAvailable(c, arg)
		}
		var s spot
		if argt.Size(c) == PTR_SIZE {
			s = newSpotWithReg(of, c, c.Temp(), argt, order[argIdx])
		} else {
			s = newSpot(of, c, c.Temp(), argt)
		}
		compiled := compileTop(of, c, arg, s)
		if compiled.empty() {
			panic("interface method arg compiled to nullspot")
		}
		if !compiled.same(&s) {
			s.free(of)
			s = compiled
		}
		argspots = append(argspots, s)
	}

	// Mark owned moves after all args are compiled. Args coerced to an
	// interface parameter already consumed their owned source inline above;
	// re-marking them here would double-consume.
	for i, arg := range callNode.Args {
		if coercedToInterface[i] {
			continue
		}
		if userParams[i].Type.HasOwned() {
			markMovedIfOwnedSource(of, c, userParams[i].Type, arg)
		}
	}
	if receiverParam.HasOwned() {
		c.MoveConsume(ifaceRef)
	}

	// Move user args into rsi, rdx, ...
	for i, s := range argspots {
		argIdx := i + 1
		argt := s.t
		if argt.Size(c) >= PTR_SIZE {
			fmt.Fprintf(of, "\tinreg %s %s\n", s.ref, order[argIdx])
		} else {
			fmt.Fprintf(of, "\tacquire %s\n", order[argIdx])
			if argt.Signed {
				fmt.Fprintf(of, "\tmovsx %s %s\n", order[argIdx], s.ref)
			} else {
				fmt.Fprintf(of, "\tmovzx %s %s\n", order[argIdx], s.ref)
			}
		}
		s.free(of)
	}

	// Move data pointer into rdi.
	fmt.Fprintf(of, "\tinreg %s rdi\n", dataPtr.ref)
	dataPtr.free(of)

	// Acquire rax for return value.
	retType := isig.Return
	raxName := raxForType(retType)
	note(of, "\t// acquire %s for interface method return\n", raxName)
	rax := regSpot(of, raxName)
	rax.t = retType

	fmt.Fprintf(of, "\tcall %s\n", fnPtr.ref)
	fnPtr.free(of)

	// Release call registers: rdi + user arg regs.
	nRegs := len(callNode.Args) + 1
	for i := 0; i < nRegs; i++ {
		note(of, "\t// release call registers\n")
		fmt.Fprintf(of, "\trelease %s\n", order[i])
	}

	ret := nullspot
	if !retType.Same(voidASTType()) {
		if !dest.empty() {
			ret = dest
		} else {
			ret = newSpot(of, c, c.Temp(), retType)
		}
		move(of, c, ret, rax)
	}
	note(of, "\t// free %s for interface method return\n", raxName)
	rax.t = regtype
	rax.free(of)
	return ret
}

// desugarVariadicCall rewrites a call to a variadic function so the generic
// argument-setup path sees a fixed argument list. The N trailing variadic
// arguments are packed into a stack-allocated array in the caller's frame
// (with per-element concrete→element-type coercion), and a slice header
// pointing at that array replaces them. A single trailing `slice...`
// forwards the slice verbatim.
func desugarVariadicCall(of io.Writer, c *Context, a AST, ast *Funcall, decl *FuncDecl) *Funcall {
	nfixed := len(decl.Args) - 1
	variParam := decl.Args[nfixed]
	elemType := variParam.Type.ElementType()
	sliceType := variParam.Type

	if len(ast.Args) < nfixed {
		CompileErrorF(a, "%s expected at least %d arguments, but was called with %d",
			ast.QualifiedName(), nfixed, len(ast.Args))
	}

	// Explicit forwarding: a single trailing `slice...`.
	if len(ast.Args) == nfixed+1 {
		if fwd, ok := ast.Args[nfixed].(*VariadicForward); ok {
			srct := fwd.Val.ASTType(c)
			if !srct.IsSlice() || !srct.ElementType().Same(elemType) {
				CompileErrorF(a, "cannot forward %s as variadic %s; expected a %s", srct, sliceType, sliceType)
			}
			s := compileTop(of, c, fwd.Val, nullspot)
			newArgs := append([]AST{}, ast.Args[:nfixed]...)
			newArgs = append(newArgs, &PreboundSpot{S: s, p: ast.p})
			return &Funcall{Callee: ast.Callee, Args: newArgs, p: ast.p}
		}
	}

	// Reject a stray `slice...` that isn't the sole trailing argument.
	for i := nfixed; i < len(ast.Args); i++ {
		if _, ok := ast.Args[i].(*VariadicForward); ok {
			CompileErrorF(ast.Args[i], "`...` forwarding must be the only trailing variadic argument")
		}
	}

	nvar := len(ast.Args) - nfixed
	elemSize := elemType.Size(c)
	iface := c.IsInterfaceType(elemType)

	// Build the args slice (header + backing) and bind it as a prebound spot.
	var sliceSpot spot
	if nvar == 0 {
		// Empty slice: header with nil data and zero length.
		hdr := c.Temp()
		fmt.Fprintf(of, "\tbytes %s 16\n", hdr)
		fmt.Fprintf(of, "\tmov qword[%s] 0\n", hdr)
		fmt.Fprintf(of, "\tmov qword[%s+8] 0\n", hdr)
		sliceSpot = spot{ref: hdr, t: sliceType, nameIsAddress: true}
	} else {
		backing := c.Temp()
		fmt.Fprintf(of, "\tbytes %s %d\n", backing, elemSize*nvar)
		for i := 0; i < nvar; i++ {
			arg := ast.Args[nfixed+i]
			argt := arg.ASTType(c)
			// An untyped integer literal has no concrete size; materialize it
			// as i64 (the default integer type) before coercing.
			if argt.Same(intlitASTType()) {
				lit := newSpot(of, c, c.Temp(), numASTType())
				lv := compileTop(of, c, arg, lit)
				if !lv.same(&lit) {
					move(of, c, lit, lv)
					lv.free(of)
				}
				arg = &PreboundSpot{S: lit, p: arg.Pos()}
				argt = numASTType()
			}
			elemRef := fmt.Sprintf("[%s+%d]", backing, i*elemSize)
			if iface {
				// emitInterfaceFatPtr forms `[<destRef>+0]` / `[<destRef>+8]`.
				// bas can't parse a doubly-offset memory operand
				// (`[base+N+0]`), so materialize the element address in a
				// register and pass that as the (offset-zero) base.
				elemAddr := newSpot(of, c, c.Temp(), ASTType{Name: "i64"})
				fmt.Fprintf(of, "\tlea %s [%s+%d]\n", elemAddr.ref, backing, i*elemSize)
				elemBase := elemAddr.ref
				// A concrete (non-interface) element coerces into the slot via
				// emitInterfaceFatPtr regardless of whether shouldCoerceToInterface
				// fires — both arms built the same fat pointer. Only an
				// already-interface argument of the same type takes the 16-byte
				// copy path.
				if !c.IsInterfaceType(argt) {
					emitInterfaceFatPtr(of, c, arg, elemType, argt, arg, elemBase, "")
				} else {
					// Already an interface of the same type: copy 16 bytes.
					src := compileTop(of, c, arg, nullspot)
					tmp := newSpot(of, c, c.Temp(), ASTType{Name: "i64"})
					fmt.Fprintf(of, "\tmov %s [%s+0]\n", tmp.ref, src.ref)
					fmt.Fprintf(of, "\tmov [%s+%d] %s\n", backing, i*elemSize, tmp.ref)
					fmt.Fprintf(of, "\tmov %s [%s+8]\n", tmp.ref, src.ref)
					fmt.Fprintf(of, "\tmov [%s+%d] %s\n", backing, i*elemSize+8, tmp.ref)
					tmp.free(of)
					src.free(of)
				}
				elemAddr.free(of)
			} else {
				effective, reason := coerceType(c, elemType, argt)
				if reason != coerceOK {
					reportCoerceFailure(arg, elemType, argt, reason,
						"For variadic element %d, expected type %v but got %v", i, elemType, argt)
				}
				_ = effective
				elemSpot := spot{ref: elemRef, t: elemType, nameIsAddress: typeIsMemoryBacked(c, elemType)}
				if elemSpot.nameIsAddress {
					val := compileTop(of, c, arg, nullspot)
					move(of, c, elemSpot, val)
					val.free(of)
				} else {
					val := compileTop(of, c, arg, nullspot)
					storeScalarToMem(of, c, elemRef, elemType, val)
					val.free(of)
				}
			}
		}
		hdr := c.Temp()
		fmt.Fprintf(of, "\tbytes %s 16\n", hdr)
		lt := newSpot(of, c, c.Temp(), ASTType{Name: "i64"})
		fmt.Fprintf(of, "\tlea %s %s\n", lt.ref, backing)
		fmt.Fprintf(of, "\tmov qword[%s] %s\n", hdr, lt.ref)
		fmt.Fprintf(of, "\tmov qword[%s+8] %d\n", hdr, nvar)
		lt.free(of)
		sliceSpot = spot{ref: hdr, t: sliceType, nameIsAddress: true}
	}

	newArgs := append([]AST{}, ast.Args[:nfixed]...)
	newArgs = append(newArgs, &PreboundSpot{S: sliceSpot, p: ast.p})
	return &Funcall{Callee: ast.Callee, Args: newArgs, p: ast.p}
}

// storeScalarToMem stores a register-resident scalar value into a memory slot
// of the given (≤8-byte) type. For narrower-than-8 types it routes through a
// width-matched local so bas emits the correctly-sized sub-register store.
func storeScalarToMem(of io.Writer, c *Context, memRef string, t ASTType, val spot) {
	sz := t.Size(c)
	if sz >= 8 {
		fmt.Fprintf(of, "\tmov qword%s %s\n", memRef, val.ref)
		return
	}
	w := c.Temp()
	fmt.Fprintf(of, "\tlocal %s %d\n", w, sz*8)
	fmt.Fprintf(of, "\tmov %s %s\n", w, val.ref)
	switch sz {
	case 1:
		fmt.Fprintf(of, "\tmov byte%s %s\n", memRef, w)
	case 2:
		fmt.Fprintf(of, "\tmov word%s %s\n", memRef, w)
	case 4:
		fmt.Fprintf(of, "\tmov dword%s %s\n", memRef, w)
	}
	fmt.Fprintf(of, "\tforget %s\n", w)
}

// compileTypeAssert lowers a concrete-type assertion `x.(T)` into a multiretu
// {T, bool} result. T must be a concrete (non-interface) type. The check is
// the two-cmp form: slot-0 typedesc identity plus slot-1 shape-word equality.
func compileTypeAssert(of io.Writer, c *Context, ast *TypeAssert, dest spot) spot {
	srcT := ast.Val.ASTType(c)
	if !c.IsInterfaceType(srcT) {
		CompileErrorF(ast, "type assertion requires an interface value on the left of `.(...)`, got %s", srcT)
	}
	if c.IsInterfaceType(ast.T) {
		return compileInterfaceAssert(of, c, ast, dest, srcT)
	}
	tgt := ast.T
	baseName := shapeBaseName(tgt)
	if baseName == "" {
		CompileErrorF(ast, "type assertion target %s is not a concrete named type", tgt)
	}
	shapeWord, serr := shapeWordFor(tgt)
	if serr != nil {
		CompileErrorF(ast, "cannot assert to %s: %s", tgt, serr.Error())
	}
	tdSym := typedescSymbolForBase(c, baseName)

	// Compile the interface value to a 16-byte fat pointer (address in src.ref).
	src := compileAsInterfaceValue(of, c, ast.Val, srcT, ast.Val)
	defer src.free(of)

	resultType := ast.ASTType(c)
	if dest.empty() {
		dest = newSpot(of, c, c.Temp(), resultType)
	}
	// dest is a memory-backed block holding the multiretu {_0 T, _1 bool};
	// fields are packed, so compute the real offsets rather than assume +0/+8.
	resDecl, _ := structDeclForType(c, resultType)
	valOff, _ := resDecl.ByteOffset(c, "_0")
	okOff, _ := resDecl.ByteOffset(c, "_1")

	vtab := newSpot(of, c, c.Temp(), ASTType{Name: "i64"})
	td := newSpot(of, c, c.Temp(), ASTType{Name: "i64"})
	shp := newSpot(of, c, c.Temp(), ASTType{Name: "i64"})
	wantTd := newSpot(of, c, c.Temp(), ASTType{Name: "i64"})
	defer vtab.free(of)
	defer td.free(of)
	defer shp.free(of)
	defer wantTd.free(of)

	fail := c.Label("assertfail")
	done := c.Label("assertdone")

	fmt.Fprintf(of, "\tmov %s [%s+8]\n", vtab.ref, src.ref) // vtable ptr
	fmt.Fprintf(of, "\tmov %s [%s+0]\n", td.ref, vtab.ref)  // slot 0 typedesc
	fmt.Fprintf(of, "\tmov %s [%s+8]\n", shp.ref, vtab.ref) // slot 1 shape word
	fmt.Fprintf(of, "\tlea %s %s\n", wantTd.ref, tdSym)
	fmt.Fprintf(of, "\tcmp %s %s\n", td.ref, wantTd.ref)
	fmt.Fprintf(of, "\tjne %s\n", fail)
	fmt.Fprintf(of, "\tmov %s %d\n", wantTd.ref, int64(shapeWord))
	fmt.Fprintf(of, "\tcmp %s %s\n", shp.ref, wantTd.ref)
	fmt.Fprintf(of, "\tjne %s\n", fail)

	// Success: extract the data slot as T (pointer or value), ok = 1.
	// Read at the target width directly into a width-matched local so the
	// store back into the result block carries the right size.
	tsz := tgt.Size(c)
	if tsz >= 8 {
		dat := c.Temp()
		fmt.Fprintf(of, "\tlocal %s 64\n", dat)
		fmt.Fprintf(of, "\tmov %s [%s+0]\n", dat, src.ref)
		fmt.Fprintf(of, "\tmov qword[%s+%d] %s\n", dest.ref, valOff, dat)
		fmt.Fprintf(of, "\tforget %s\n", dat)
	} else {
		dat := c.Temp()
		fmt.Fprintf(of, "\tlocal %s %d\n", dat, tsz*8)
		switch tsz {
		case 1:
			fmt.Fprintf(of, "\tmov %s byte[%s+0]\n", dat, src.ref)
			fmt.Fprintf(of, "\tmov byte[%s+%d] %s\n", dest.ref, valOff, dat)
		case 2:
			fmt.Fprintf(of, "\tmov %s word[%s+0]\n", dat, src.ref)
			fmt.Fprintf(of, "\tmov word[%s+%d] %s\n", dest.ref, valOff, dat)
		case 4:
			fmt.Fprintf(of, "\tmov %s dword[%s+0]\n", dat, src.ref)
			fmt.Fprintf(of, "\tmov dword[%s+%d] %s\n", dest.ref, valOff, dat)
		}
		fmt.Fprintf(of, "\tforget %s\n", dat)
	}
	fmt.Fprintf(of, "\tmov byte[%s+%d] 1\n", dest.ref, okOff)
	fmt.Fprintf(of, "\tjmp %s\n", done)

	// Failure: zero the value slot and ok = 0.
	fmt.Fprintf(of, "\tlabel %s\n", fail)
	if tsz >= 8 {
		fmt.Fprintf(of, "\tmov qword[%s+%d] 0\n", dest.ref, valOff)
	} else {
		switch tsz {
		case 1:
			fmt.Fprintf(of, "\tmov byte[%s+%d] 0\n", dest.ref, valOff)
		case 2:
			fmt.Fprintf(of, "\tmov word[%s+%d] 0\n", dest.ref, valOff)
		case 4:
			fmt.Fprintf(of, "\tmov dword[%s+%d] 0\n", dest.ref, valOff)
		}
	}
	fmt.Fprintf(of, "\tmov byte[%s+%d] 0\n", dest.ref, okOff)
	fmt.Fprintf(of, "\tlabel %s\n", done)

	dest.t = resultType
	return dest
}

// ifaceDescSymbol returns the fully-qualified iface_desc symbol for an
// interface type. The owner package is the interface's home package: built-in
// interfaces (`any`) live in "builtin"; a qualified name `pkg.I` lives in
// `pkg`; a bare local name lives in the current package.
func ifaceDescSymbol(c *Context, ifaceName string) string {
	pkg := c.Pkgname()
	bare := ifaceName
	if dot := strings.LastIndex(ifaceName, "."); dot >= 0 {
		pkg = ifaceName[:dot]
		bare = ifaceName[dot+1:]
	} else if _, ok := c.InterfaceForName("builtin." + ifaceName); ok {
		// Built-in interface registered under the builtin package (e.g. `any`).
		if _, local := c.interfaceDecls[ifaceName]; !local {
			pkg = "builtin"
		}
	}
	return fmt.Sprintf("%s.__iface_desc_%s", pkg, strings.ReplaceAll(bare, ".", "_"))
}

// compileInterfaceAssert lowers an interface-to-interface assertion `x.(I)`:
// it loads (typedesc, shape, data) from the source fat pointer, calls
// _iface.assert_to with the target iface_desc, then builds the result
// multiretu{I, bool}. On success the asserted interface carries src_data
// verbatim and the helper-provided itab vtable; on failure it is zeroed.
// emitAssertToCall emits the _iface.assert_to call for source interface value
// srcRef (a 16-byte fat pointer) against target descriptor descSym, returning
// local temps holding the result vtable pointer (rax) and ok flag (rdx). The
// arg registers are pinned across the (already-compiled) argument loads.
func emitAssertToCall(of io.Writer, c *Context, srcRef, descSym string) (vtTmp, okTmp string) {
	fmt.Fprintf(of, "\tacquire rdi\n")
	fmt.Fprintf(of, "\tacquire rsi\n")
	fmt.Fprintf(of, "\tacquire rdx\n")
	fmt.Fprintf(of, "\tacquire rcx\n")
	fmt.Fprintf(of, "\tacquire rax\n")
	fmt.Fprintf(of, "\tmov rax [%s+8]\n", srcRef) // src vtable ptr
	fmt.Fprintf(of, "\tmov rdi [rax+0]\n")        // src_ti (concrete typedesc)
	fmt.Fprintf(of, "\tmov rsi [rax+8]\n")        // src_shape
	fmt.Fprintf(of, "\tmov rdx [%s+0]\n", srcRef) // src_data
	fmt.Fprintf(of, "\tlea rcx %s\n", descSym)
	fmt.Fprintf(of, "\tcall _iface.assert_to\n")
	okTmp = c.Temp()
	vtTmp = c.Temp()
	fmt.Fprintf(of, "\tlocal %s 64\n", okTmp)
	fmt.Fprintf(of, "\tlocal %s 64\n", vtTmp)
	fmt.Fprintf(of, "\tmov %s rdx\n", okTmp)
	fmt.Fprintf(of, "\tmov %s rax\n", vtTmp)
	fmt.Fprintf(of, "\trelease rax\n")
	fmt.Fprintf(of, "\trelease rcx\n")
	fmt.Fprintf(of, "\trelease rdx\n")
	fmt.Fprintf(of, "\trelease rsi\n")
	fmt.Fprintf(of, "\trelease rdi\n")
	return vtTmp, okTmp
}

// interfaceWidensTo reports whether interface src can be *implicitly* converted
// to interface dst — i.e. dst's contract is covered by src, so the conversion
// can never fail at runtime. For each method dst requires, src must declare a
// method matching on the same basis _iface.assert_to uses (canonical
// non-receiver signature + receiver shape — so the static and runtime matchers
// agree), with src's declared borrow set ⊆ dst's. The borrow direction is the
// inverse of impl conformance: the concrete behind src has borrow ⊆ src, so
// requiring src ⊆ dst keeps it ⊆ dst (assert_to's runtime mask gate can't
// fail). `any` (zero required methods) is the trivial always-widens case.
func interfaceWidensTo(c *Context, src, dst *InterfaceDecl) bool {
	if src == nil || dst == nil {
		return false
	}
	for _, dm := range dst.Methods {
		matched := false
		for _, sm := range src.Methods {
			if sm.Name == dm.Name && interfaceMethodCanonMatch(c, sm, dm) &&
				aliasSetSubset(sm.ReturnAliases, dm.ReturnAliases) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

// interfaceMethodCanonMatch compares two interface methods on the canonical
// basis _iface.assert_to matches by: the non-receiver signature text (which
// feeds sig_hash) and the receiver shape. Using canonicalSig here — the same
// function reqEntriesForInterface feeds the hashes — guarantees that a static
// match implies the runtime matcher agrees, so an "infallible" widening really
// does succeed.
func interfaceMethodCanonMatch(c *Context, sm, dm InterfaceMethodSig) bool {
	if len(sm.Params) == 0 || len(dm.Params) == 0 {
		return false
	}
	if receiverShapeOf(sm.Params[0].Type) != receiverShapeOf(dm.Params[0].Type) {
		return false
	}
	return canonicalSig(c, sm.Params[1:], sm.Return) == canonicalSig(c, dm.Params[1:], dm.Return)
}

// aliasSetSubset reports whether every parameter index in sub[slot] also
// appears in super[slot] — i.e. sub ⊆ super per return slot.
func aliasSetSubset(sub, super [][]int) bool {
	for slot, params := range sub {
		for _, p := range params {
			if !slotDeclaresParam(super, slot, p) {
				return false
			}
		}
	}
	return true
}

// emitInterfaceWiden lowers an implicit interface→interface widening
// `var dst D = src` into an _iface.assert_to call that rebuilds the fat
// pointer's vtable for D from the concrete typedesc src already carries
// (vtable[0]). The conversion is infallible by construction (the static
// interfaceWidensTo gate plus a congruent matcher), so the result is built
// unconditionally; a defensive `ok == 0` trap guards against any matcher
// desync rather than storing a null vtable. The widened value wraps src's
// data verbatim, so it borrows whatever src borrowed (data field-pointer
// copied for escape continuity).
func emitInterfaceWiden(of io.Writer, c *Context, errNode AST, dstt, srct ASTType, valAST AST, destRef, destBinding string) {
	srcIface, _ := c.InterfaceForName(srct.Name)
	dstIface, _ := c.InterfaceForName(dstt.Name)
	if !interfaceWidensTo(c, srcIface, dstIface) {
		CompileErrorF(errNode, "Cannot implicitly convert interface %s to %s: %s is not covered by %s (a narrowing). Use an explicit assertion `x.(%s)`, which yields an ok flag.",
			srct.Name, dstt.Name, dstt.Name, srct.Name, dstt.Name)
	}
	descSym := ifaceDescSymbol(c, dstt.Name)
	src := compileAsInterfaceValue(of, c, valAST, srct, valAST)
	defer src.free(of)

	vtTmp, okTmp := emitAssertToCall(of, c, src.ref, descSym)
	okLabel := c.Label("ifacewidenok")
	fmt.Fprintf(of, "\tcmp %s 0\n", okTmp)
	fmt.Fprintf(of, "\tjne %s\n", okLabel)
	fmt.Fprintf(of, "\tcall _init.nil_assert\n") // unreachable given the static gate
	fmt.Fprintf(of, "\tlabel %s\n", okLabel)

	dataTmp := c.Temp()
	fmt.Fprintf(of, "\tlocal %s 64\n", dataTmp)
	fmt.Fprintf(of, "\tmov %s [%s+0]\n", dataTmp, src.ref)
	fmt.Fprintf(of, "\tmov qword[%s+0] %s\n", destRef, dataTmp)
	fmt.Fprintf(of, "\tmov qword[%s+8] %s\n", destRef, vtTmp)
	fmt.Fprintf(of, "\tforget %s\n", dataTmp)
	fmt.Fprintf(of, "\tforget %s\n", okTmp)
	fmt.Fprintf(of, "\tforget %s\n", vtTmp)

	// Borrow continuity: the widened interface wraps the same data, so it
	// borrows whatever src borrowed. Skip for owned destinations (the move is
	// handled at the assignment site).
	if destBinding != "" && dstt.OwnedMask == 0 {
		c.PointerFlow().SetFieldPointer(flow.Binding(destBinding), "data", argAliasProvenance(c, valAST))
	}
}

func compileInterfaceAssert(of io.Writer, c *Context, ast *TypeAssert, dest spot, srcT ASTType) spot {
	descSym := ifaceDescSymbol(c, ast.T.Name)

	src := compileAsInterfaceValue(of, c, ast.Val, srcT, ast.Val)
	defer src.free(of)

	resultType := ast.ASTType(c) // ?T — a single nullable interface
	if dest.empty() {
		dest = newSpot(of, c, c.Temp(), resultType)
	}

	// assert_to returns the itab vtable in rax (0 on failure) and ok in rdx.
	// The result is the (possibly-null) interface [data, vtable]: on failure
	// vtable is 0, which is exactly the null sentinel `if (k != nil)` tests, so
	// no branch is needed — the ok flag is subsumed by the nullable type.
	vtTmp, okTmp := emitAssertToCall(of, c, src.ref, descSym)
	fmt.Fprintf(of, "\tforget %s\n", okTmp)
	dataTmp := c.Temp()
	fmt.Fprintf(of, "\tlocal %s 64\n", dataTmp)
	fmt.Fprintf(of, "\tmov %s [%s+0]\n", dataTmp, src.ref)
	fmt.Fprintf(of, "\tmov qword[%s+0] %s\n", dest.ref, dataTmp)
	fmt.Fprintf(of, "\tmov qword[%s+8] %s\n", dest.ref, vtTmp)
	fmt.Fprintf(of, "\tforget %s\n", dataTmp)
	fmt.Fprintf(of, "\tforget %s\n", vtTmp)

	dest.t = resultType
	return dest
}

// compileTypeSwitch lowers `switch (v x.(type)) { case T {...} ... }`. Each
// case is tested top-down; the first match binds `v` to the case's type
// (narrowed inside the case body) and runs the body. `default` runs if no
// case matched. No fallthrough.
func compileTypeSwitch(of io.Writer, c *Context, ast *TypeSwitch) {
	srcT := ast.Val.ASTType(c)
	if !c.IsInterfaceType(srcT) {
		CompileErrorF(ast, "type switch requires an interface value, got %s", srcT)
	}
	src := compileAsInterfaceValue(of, c, ast.Val, srcT, ast.Val)
	defer src.free(of)

	vtab := newSpot(of, c, c.Temp(), ASTType{Name: "i64"})
	td := newSpot(of, c, c.Temp(), ASTType{Name: "i64"})
	shp := newSpot(of, c, c.Temp(), ASTType{Name: "i64"})
	want := newSpot(of, c, c.Temp(), ASTType{Name: "i64"})
	defer vtab.free(of)
	defer td.free(of)
	defer shp.free(of)
	defer want.free(of)
	fmt.Fprintf(of, "\tmov %s [%s+8]\n", vtab.ref, src.ref)
	fmt.Fprintf(of, "\tmov %s [%s+0]\n", td.ref, vtab.ref)
	fmt.Fprintf(of, "\tmov %s [%s+8]\n", shp.ref, vtab.ref)

	end := c.Label("tswend")
	var defaultCase *TypeCase
	for _, cs := range ast.Cases {
		if cs.IsDefault {
			defaultCase = cs
			continue
		}
		if c.IsInterfaceType(cs.T) {
			compileTypeSwitchIfaceCase(of, c, ast, cs, src, td, shp, end)
			continue
		}
		baseName := shapeBaseName(cs.T)
		shapeWord, serr := shapeWordFor(cs.T)
		if serr != nil {
			CompileErrorF(cs.Body, "cannot match %s: %s", cs.T, serr.Error())
		}
		tdSym := typedescSymbolForBase(c, baseName)
		next := c.Label("tswnext")
		fmt.Fprintf(of, "\tlea %s %s\n", want.ref, tdSym)
		fmt.Fprintf(of, "\tcmp %s %s\n", td.ref, want.ref)
		fmt.Fprintf(of, "\tjne %s\n", next)
		fmt.Fprintf(of, "\tmov %s %d\n", want.ref, int64(shapeWord))
		fmt.Fprintf(of, "\tcmp %s %s\n", shp.ref, want.ref)
		fmt.Fprintf(of, "\tjne %s\n", next)
		// Match: bind v to the data slot as cs.T inside a fresh scope.
		compileTypeCaseBody(of, c, ast.BindName, cs.T, src, cs.Body)
		fmt.Fprintf(of, "\tjmp %s\n", end)
		fmt.Fprintf(of, "\tlabel %s\n", next)
	}
	if defaultCase != nil {
		compileTop(of, c, defaultCase.Body, nullspot)
	}
	fmt.Fprintf(of, "\tlabel %s\n", end)
}

// compileTypeCaseBody binds `bindName` to the source's data slot reinterpreted
// as type T, then compiles the case body in a child scope.
func compileTypeCaseBody(of io.Writer, c *Context, bindName string, T ASTType, src spot, body *Block) {
	child := c.SubContext()
	bindSlot := newSpot(of, child, bindName, T)
	child.BindVar(body, bindName, T, false)
	tsz := T.Size(c)
	if bindSlot.nameIsAddress {
		// Memory-backed (pointer-to-struct etc. is still pointer-sized; this
		// path only triggers for >8-byte value types, which can't be in an
		// interface, so it is effectively unreachable).
		dat := newSpot(of, child, child.Temp(), ASTType{Name: "i64"})
		fmt.Fprintf(of, "\tmov %s [%s+0]\n", dat.ref, src.ref)
		fmt.Fprintf(of, "\tmov [%s] %s\n", bindSlot.ref, dat.ref)
		dat.free(of)
	} else if tsz >= 8 {
		fmt.Fprintf(of, "\tmov %s [%s+0]\n", bindSlot.ref, src.ref)
	} else {
		// Read the data slot at the bind's width directly into the slot.
		switch tsz {
		case 1:
			fmt.Fprintf(of, "\tmov %s byte[%s+0]\n", bindSlot.ref, src.ref)
		case 2:
			fmt.Fprintf(of, "\tmov %s word[%s+0]\n", bindSlot.ref, src.ref)
		case 4:
			fmt.Fprintf(of, "\tmov %s dword[%s+0]\n", bindSlot.ref, src.ref)
		}
	}
	compileTop(of, child, body, nullspot)
	// Release the bind slot's Ralloc name so a sibling case can rebind the
	// same user-visible name to a different type.
	fmt.Fprintf(of, "\tforget %s\n", bindName)
}

// compileTypeSwitchIfaceCase lowers an interface-typed case in a type switch.
// It calls _iface.assert_to with the source typedesc/shape/data and the case
// interface's iface_desc; on success it binds `v` to the asserted interface
// (a 16-byte fat pointer) inside a fresh scope and runs the body, then jumps
// to end. On failure it falls through to the next case (first-match-wins).
func compileTypeSwitchIfaceCase(of io.Writer, c *Context, ast *TypeSwitch, cs *TypeCase, src, td, shp spot, end string) {
	descSym := ifaceDescSymbol(c, cs.T.Name)
	next := c.Label("tswnext")

	// Call assert_to: rdi=src_ti(td), rsi=shape(shp), rdx=data, rcx=desc.
	// The switch head already materialized src_ti and src_shape into the td/shp
	// spots once for all cases, so (unlike compileInterfaceAssert) no rax scratch
	// is needed to re-deref the vtable: rdi/rsi load straight from those spots.
	fmt.Fprintf(of, "\tacquire rdi\n")
	fmt.Fprintf(of, "\tacquire rsi\n")
	fmt.Fprintf(of, "\tacquire rdx\n")
	fmt.Fprintf(of, "\tacquire rcx\n")
	fmt.Fprintf(of, "\tmov rdi %s\n", td.ref)
	fmt.Fprintf(of, "\tmov rsi %s\n", shp.ref)
	fmt.Fprintf(of, "\tmov rdx [%s+0]\n", src.ref)
	fmt.Fprintf(of, "\tlea rcx %s\n", descSym)
	fmt.Fprintf(of, "\tcall _iface.assert_to\n")
	okTmp := c.Temp()
	vtTmp := c.Temp()
	fmt.Fprintf(of, "\tlocal %s 64\n", okTmp)
	fmt.Fprintf(of, "\tlocal %s 64\n", vtTmp)
	fmt.Fprintf(of, "\tmov %s rdx\n", okTmp)
	fmt.Fprintf(of, "\tmov %s rax\n", vtTmp)
	fmt.Fprintf(of, "\trelease rcx\n")
	fmt.Fprintf(of, "\trelease rdx\n")
	fmt.Fprintf(of, "\trelease rsi\n")
	fmt.Fprintf(of, "\trelease rdi\n")
	fmt.Fprintf(of, "\tcmp %s 0\n", okTmp)
	fmt.Fprintf(of, "\tje %s\n", next)

	// Match: build the asserted interface block, bind `v`, run body.
	child := c.SubContext()
	bindSlot := newSpot(of, child, ast.BindName, cs.T)
	child.BindVar(cs.Body, ast.BindName, cs.T, false)
	// bindSlot is a 16-byte memory-backed interface; [+0]=data [+8]=vtable.
	dataTmp := child.Temp()
	fmt.Fprintf(of, "\tlocal %s 64\n", dataTmp)
	fmt.Fprintf(of, "\tmov %s [%s+0]\n", dataTmp, src.ref)
	fmt.Fprintf(of, "\tmov [%s+0] %s\n", bindSlot.ref, dataTmp)
	fmt.Fprintf(of, "\tmov [%s+8] %s\n", bindSlot.ref, vtTmp)
	fmt.Fprintf(of, "\tforget %s\n", dataTmp)
	compileTop(of, child, cs.Body, nullspot)
	fmt.Fprintf(of, "\tforget %s\n", ast.BindName)
	fmt.Fprintf(of, "\tforget %s\n", okTmp)
	fmt.Fprintf(of, "\tforget %s\n", vtTmp)
	fmt.Fprintf(of, "\tjmp %s\n", end)
	fmt.Fprintf(of, "\tlabel %s\n", next)
}

// emitInterfaceFatPtr validates that srct satisfies the interface dstt, registers
// the vtable, and emits stores to fill the two-word fat pointer at destRef. srct
// may be a concrete pointer or a non-pointer concrete value that fits in the
// 8-byte data word. The caller is responsible for allocating destRef before
// calling and for handling owned-source marking and flow updates afterwards.
//
// destBinding is the name of the destination interface variable, or "" when the
// destination has no flow-tracked binding (e.g. a return-value slot or a callee
// parameter we don't own). When set and the destination is a non-owned (borrow)
// interface, the source's pointer expression is recorded as a field pointer
// keyed "<destBinding>.data" so the existing escape, deref-validity, and
// alias-invalidation machinery sees through the interface's opaque data slot
// the same way it does through a regular struct field.
//
// For owned interface coercions we deliberately skip the field-pointer
// registration: the source binding's obligation is moved into the interface
// by markMovedIfOwnedSource at the call site, which already invalidates the
// source's origin. Registering the data field would then make the destination
// interface look dead, which is the opposite of what an ownership transfer
// means.
func emitInterfaceFatPtr(of io.Writer, c *Context, errNode AST, dstt, srct ASTType, valAST AST, destRef, destBinding string) {
	// Interface → interface widening rebuilds the vtable at runtime from the
	// source's concrete typedesc (it has no static __vtable_T__I); delegate.
	if c.IsInterfaceType(srct) {
		emitInterfaceWiden(of, c, errNode, dstt, srct, valAST, destRef, destBinding)
		return
	}
	// The vtable and its typedesc relocation are keyed off the *base* type of
	// the source — the leaf at the bottom of the shape's constructor stack.
	// For composite sources (`*byte[]`, `byte[][]`, `*byte[N]`) the bare
	// srct.Name is empty; shapeBaseName walks to the named leaf (`byte`,
	// `geom.Point`, ...) so the typedesc symbol resolves to a real base type.
	concreteTypeName := shapeBaseName(srct)
	valueBacked := validateInterfaceCoercion(c, errNode, dstt, srct)
	ifaceDecl, _ := c.InterfaceForName(dstt.Name)
	// Split qualified type names (e.g. "io.FD") so the vtable's method
	// relocations target the correct owning package, and so the vtable
	// global's own name itself doesn't contain dots — bas treats '.' as
	// a package separator, so a name like __vtable_io.FD_io.writer
	// would be misparsed and miss the linker's name table.
	typePkg := c.Pkgname()
	bareType := concreteTypeName
	if dot := strings.LastIndex(concreteTypeName, "."); dot >= 0 {
		typePkg = concreteTypeName[:dot]
		bareType = concreteTypeName[dot+1:]
	}
	// Built-in scalar typedescs live in the builtin package, regardless of
	// which package performs the coercion.
	if isBuiltinScalarType(bareType) {
		typePkg = "builtin"
	}
	levels, serr := shapeStackFor(srct)
	if serr != nil {
		CompileErrorF(errNode, "Cannot convert value of type %s to interface %s: %s", srct, dstt.Name, serr.Error())
	}
	shapeWord, _ := shapeWordFor(srct)
	vtableName := fmt.Sprintf("__vtable_%s_%s__%s",
		strings.ReplaceAll(concreteTypeName, ".", "_"),
		shapeMangle(levels),
		strings.ReplaceAll(dstt.Name, ".", "_"))
	c.NeedVtable(vtableName, bareType, dstt.Name, typePkg, ifaceDecl, shapeWord)
	if destBinding != "" && dstt.OwnedMask == 0 && !valueBacked {
		c.PointerFlow().SetFieldPointer(flow.Binding(destBinding), "data", pointerExprForAST(c, valAST, ""))
	}
	val := compileTop(of, c, valAST, nullspot)
	if valueBacked {
		valSize := val.t.Size(c)
		if val.t.Indirection == 0 && typeIsMemoryBacked(c, val.t) && valSize <= PTR_SIZE {
			// memBacked struct ≤ 8 bytes: pack the value bits via a
			// width-matched memory load into a scratch register, then
			// store into the interface data slot. We grab r10 explicitly
			// — `local Temp 64` followed by `Temp:32` would force the
			// allocator to find a 64-bit register, which it may evict to
			// memory to free a different register for the source LEA;
			// the resulting partial-of-evicted-Ralloc resolves to a
			// memory operand, and bas can't encode mem-to-mem mov.
			fmt.Fprintf(of, "\tacquire r10\n")
			switch valSize {
			case 1:
				fmt.Fprintf(of, "\tmovzx r10 byte[%s]\n", val.ref)
			case 2:
				fmt.Fprintf(of, "\tmovzx r10 word[%s]\n", val.ref)
			case 4:
				// mov to 32-bit auto-zero-extends on x86-64.
				fmt.Fprintf(of, "\tmov r10d dword[%s]\n", val.ref)
			case 8:
				fmt.Fprintf(of, "\tmov r10 qword[%s]\n", val.ref)
			default:
				CompileErrorF(errNode, "Cannot convert value of type %s to interface %s: %d-byte values are not supported by the small-struct value-backed path; use a 1/2/4/8-byte layout or a pointer", val.t, dstt.Name, valSize)
			}
			fmt.Fprintf(of, "\tmov [%s+0] r10\n", destRef)
			fmt.Fprintf(of, "\trelease r10\n")
		} else if valSize < PTR_SIZE {
			// Scalar narrower than a pointer: existing sign/zero-extend
			// path on a sub-register source.
			tmp := newSpot(of, c, c.Temp(), ASTType{Name: "i64", Signed: val.t.Signed})
			if val.t.Signed {
				fmt.Fprintf(of, "\tmovsx %s %s\n", tmp.ref, val.ref)
			} else {
				fmt.Fprintf(of, "\tmovzx %s %s\n", tmp.ref, val.ref)
			}
			fmt.Fprintf(of, "\tmov [%s+0] %s\n", destRef, tmp.ref)
			tmp.free(of)
		} else if val.nameIsAddress {
			// memory-backed 8-byte scalar whose name resolves to memory: a
			// file-scope global, or a local made volatile by an earlier
			// `&` of it. bas can't encode a mem-to-mem mov into the data
			// slot, so load the value through a scratch register first.
			// `mov r10 <name>` lets bas resolve the name to its storage
			// (RIP-relative global, or volatile spill slot) and load the
			// value — a `qword[name]` form would instead dereference it.
			// r10 is grabbed explicitly for the same reason as the
			// small-struct path above.
			fmt.Fprintf(of, "\tacquire r10\n")
			fmt.Fprintf(of, "\tmov r10 %s\n", val.ref)
			fmt.Fprintf(of, "\tmov [%s+0] r10\n", destRef)
			fmt.Fprintf(of, "\trelease r10\n")
		} else {
			fmt.Fprintf(of, "\tmov [%s+0] %s\n", destRef, val.ref)
		}
	} else {
		fmt.Fprintf(of, "\tmov [%s+0] %s\n", destRef, val.ref)
	}
	val.free(of)
	vtmp := newSpot(of, c, c.Temp(), ASTType{Name: "i64"})
	fmt.Fprintf(of, "\tlea %s %s\n", vtmp.ref, vtableName)
	fmt.Fprintf(of, "\tmov [%s+8] %s\n", destRef, vtmp.ref)
	vtmp.free(of)
}
