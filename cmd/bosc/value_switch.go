package main

import (
	"fmt"
	"io"
	"math"
	"math/big"

	"github.com/knusbaum/gbasm/cmd/bosc/flow"
)

// BoundSwitch gives head bindings a scope while preserving expression results.
type BoundSwitch struct {
	Decl   *VarDecl
	Switch *VariantSwitch
	p      position
}

func (s *BoundSwitch) ASTType(c *Context) ASTType {
	sc := c.SubContext()
	t := switchConcreteType(s.Decl.Init.ASTType(c))
	sc.BindVar(s.Decl, s.Decl.Name, t, s.Decl.IsConst)
	return s.Switch.ASTType(sc)
}
func (s *BoundSwitch) Pos() position { return s.p }
func (s *BoundSwitch) Note() string  { return "switch with head binding" }

func compileBoundSwitch(of io.Writer, c *Context, s *BoundSwitch, dest spot) spot {
	sc := c.SubContext()
	compileTop(of, sc, s.Decl, nullspot)
	result := compileTop(of, sc, s.Switch, dest)
	if fallsThrough(s.Switch) {
		for _, name := range sc.UnconsumedOwned() {
			errUnconsumedOwned(sc, s, name)
		}
	}
	sc.reportUnused()
	sc.reportScopeNudge()
	sc.InvalidateLocalOriginsForScope()
	sc.ForgetPointerBindings()
	sc.FreeLocalVars(of)
	return result
}

// switchHead copies the cached scrutinee into each comparison's temporary.
// Comparison lowering may free its operands; the cached value must survive.
type switchHead struct{ PreboundSpot }

// A folded case retains its declared type; comparisons still use normal ==
// lowering and compatibility checks. Folding is also used to detect aliases
// of the same case (such as 2 and 1+1, or floating-point signed zero).
type switchConstant struct {
	Type     ASTType
	Int      *big.Int
	Float    float64
	Bool     bool
	Nil      bool
	Original AST // a tag-only values case, compared through an interface
	p        position
}

func (s *switchConstant) ASTType(*Context) ASTType { return s.Type }
func (s *switchConstant) Pos() position            { return s.p }
func (s *switchConstant) Note() string             { return "switch case constant" }

func switchConcreteType(t ASTType) ASTType {
	if t.Same(intlitASTType()) {
		return numASTType()
	}
	if t.Same(floatlitASTType()) {
		return ASTType{Name: "f64"}
	}
	return t
}

func (s *VariantSwitch) valueASTType(c *Context) ASTType {
	t := switchConcreteType(s.Scrutinee.ASTType(c))
	// This reuses =='s rejection of aggregates, including string slices.
	(&Op2{Type: n_deq, First: s.Scrutinee, Second: s.Scrutinee}).ASTType(c)
	if t.Name == "void" || t.Name == "<nil>" {
		CompileErrorF(s.Scrutinee, "Cannot switch on type %s", t)
	}
	for _, cs := range s.Cases {
		if fallsThrough(cs.Body) {
			return switchConcreteType(cs.Body.ASTType(c))
		}
	}
	if s.Default != nil && fallsThrough(s.Default) {
		return switchConcreteType(s.Default.ASTType(c))
	}
	return voidASTType()
}

func switchConstFail(a AST) {
	CompileErrorF(a, "Switch case must be a compile-time constant")
}

func foldSwitchConstant(c *Context, a AST) *switchConstant {
	return foldSwitchConstantAs(c, a, ASTType{})
}

func foldSwitchConstantAs(c *Context, a AST, expected ASTType) *switchConstant {
	// Literal-only arithmetic adopts its destination's floating-point type,
	// including division and rounding after every operation, just as assignment.
	if isFloatType(c, expected) && isPendingLit(a.ASTType(c)) {
		f, ok := evalFloatConst(c, a, c.ResolveUnderlying(expected).Name == "f32")
		if !ok {
			switchConstFail(a)
		}
		return &switchConstant{Type: expected, Float: f, p: a.Pos()}
	}
	result := &switchConstant{p: a.Pos()}
	switch v := a.(type) {
	case *Literal:
		result.Type = v.ASTType(c)
		switch x := v.Val.(type) {
		case uint64:
			result.Int = new(big.Int).SetUint64(x)
		case byte:
			result.Int = new(big.Int).SetUint64(uint64(x))
		case float64:
			result.Float = x
		case bool:
			result.Bool = x
		case nil:
			result.Nil = true
		default:
			switchConstFail(a)
		}
	case *Dot:
		resolved := ResolveSelector(c, a)
		if resolved.Kind != ResolvedValuesCase || resolved.Values.IsPayloadBearing() {
			switchConstFail(a)
		}
		result.Type = resolved.Type
		result.Original = a
		result.Int = big.NewInt(int64(resolved.Case.Tag))
	case *SizeOf:
		result.Type = v.ASTType(c)
		result.Int = big.NewInt(int64(v.Type.Size(c)))
	case *AlignOf:
		result.Type = v.ASTType(c)
		result.Int = big.NewInt(int64(v.Type.Alignment(c)))
	case *Not:
		x := foldSwitchConstant(c, v.Val)
		result.Type = boolASTType()
		switch {
		case x.Int != nil:
			result.Bool = x.Int.Sign() == 0
		case isFloatType(c, x.Type) || x.Type.Same(floatlitASTType()):
			result.Bool = x.Float == 0
		case x.Nil:
			result.Bool = true
		case c.ResolveUnderlying(x.Type).Same(boolASTType()):
			result.Bool = !x.Bool
		default:
			switchConstFail(a)
		}
	case *BitNot:
		x := foldSwitchConstant(c, v.Val)
		if x.Int == nil {
			switchConstFail(a)
		}
		result.Type = v.ASTType(c)
		result.Int = new(big.Int).Not(x.Int)
	case *Funcall:
		// Only type conversions are constant calls; immutable local variables and
		// calls to ordinary functions remain runtime expressions.
		t, ok := c.TypeByName(v.QualifiedName())
		if !ok || len(v.Args) != 1 {
			switchConstFail(a)
		}
		x := foldSwitchConstantAs(c, v.Args[0], t)
		if x.Original != nil {
			resolved := ResolveSelector(c, x.Original)
			projection := -1
			for i, pt := range resolved.Values.Projections {
				if pt.Same(t) {
					projection = i
					break
				}
			}
			if projection < 0 {
				CompileErrorF(a, "Cannot cast %s to %s: %s has no %s projection", x.Type, t, x.Type, t)
			}
			if projection >= len(resolved.Case.Expr) {
				switchConstFail(a)
			}
			projected := foldSwitchConstantAs(c, resolved.Case.Expr[projection], t)
			projected.Type = t
			return projected
		}
		result.Type = t
		switch {
		case isIntegerType(c, t):
			if x.Int != nil {
				result.Int = new(big.Int).Set(x.Int)
			} else if isFloatType(c, x.Type) || x.Type.Same(floatlitASTType()) {
				if math.IsNaN(x.Float) || math.IsInf(x.Float, 0) {
					CompileErrorF(a, "Non-finite switch case cannot be converted to %s", t)
				}
				result.Int, _ = big.NewFloat(x.Float).Int(nil)
			} else {
				switchConstFail(a)
			}
			if isPendingLit(x.Type) && !litFitsIn(result.Int, c.ResolveUnderlying(t)) {
				CompileErrorF(a, "Literal %s does not fit in type %s", result.Int, t)
			}
		case isFloatType(c, t):
			if x.Int != nil {
				result.Float, _ = new(big.Float).SetInt(x.Int).Float64()
			} else if isFloatType(c, x.Type) || x.Type.Same(floatlitASTType()) {
				result.Float = x.Float
			} else {
				switchConstFail(a)
			}
		case c.ResolveUnderlying(t).Same(boolASTType()) && x.Type.Same(boolASTType()):
			result.Bool = x.Bool
		default:
			switchConstFail(a)
		}
	case *Op2:
		x := foldSwitchConstant(c, v.First)
		y := foldSwitchConstant(c, v.Second)
		result.Type = v.ASTType(c)
		if x.Int != nil && y.Int != nil && (v.Type == n_deq || v.Type == n_neq || v.Type == n_lt || v.Type == n_le || v.Type == n_gt || v.Type == n_ge) {
			cmp := x.Int.Cmp(y.Int)
			switch v.Type {
			case n_deq:
				result.Bool = cmp == 0
			case n_neq:
				result.Bool = cmp != 0
			case n_lt:
				result.Bool = cmp < 0
			case n_le:
				result.Bool = cmp <= 0
			case n_gt:
				result.Bool = cmp > 0
			case n_ge:
				result.Bool = cmp >= 0
			}
		} else if x.Int != nil && y.Int != nil {
			// EvalConst implements Boson's integer arithmetic and shift precedence.
			count := y
			if v.Type == n_shl || v.Type == n_shr {
				// Match ordinary native shifts: CL counts are masked to five
				// bits for widths <=32, or six bits for 64-bit operands.
				mask := int64(31)
				underlying := c.ResolveUnderlying(result.Type)
				if underlying.Size(c) == 8 {
					mask = 63
				}
				count = &switchConstant{Type: y.Type, Int: new(big.Int).And(y.Int, big.NewInt(mask)), p: y.p}
			}
			folded := &Op2{Type: v.Type, First: x, Second: count}
			var ok bool
			result.Int, ok = EvalConst(c, folded)
			if !ok {
				switchConstFail(a)
			}
		} else if (isFloatType(c, result.Type) || result.Type.Same(floatlitASTType()) || result.Type.Same(boolASTType())) && (x.Int != nil || isFloatType(c, x.Type) || x.Type.Same(floatlitASTType())) && (y.Int != nil || isFloatType(c, y.Type) || y.Type.Same(floatlitASTType())) {
			xf, yf := x.Float, y.Float
			if x.Int != nil {
				xf, _ = new(big.Float).SetInt(x.Int).Float64()
			}
			if y.Int != nil {
				yf, _ = new(big.Float).SetInt(y.Int).Float64()
			}
			if c.ResolveUnderlying(result.Type).Name == "f32" {
				xf = float64(float32(xf))
				yf = float64(float32(yf))
			}
			switch v.Type {
			case n_add:
				result.Float = xf + yf
			case n_sub:
				result.Float = xf - yf
			case n_mul:
				result.Float = xf * yf
			case n_div:
				result.Float = xf / yf
			case n_deq:
				result.Bool = xf == yf
			case n_neq:
				result.Bool = xf != yf
			case n_lt:
				result.Bool = xf < yf
			case n_le:
				result.Bool = xf <= yf
			case n_gt:
				result.Bool = xf > yf
			case n_ge:
				result.Bool = xf >= yf
			default:
				switchConstFail(a)
			}
		} else if x.Type.Same(boolASTType()) && y.Type.Same(boolASTType()) {
			switch v.Type {
			case n_booland:
				result.Bool = x.Bool && y.Bool
			case n_boolor:
				result.Bool = x.Bool || y.Bool
			case n_deq:
				result.Bool = x.Bool == y.Bool
			case n_neq:
				result.Bool = x.Bool != y.Bool
			default:
				switchConstFail(a)
			}
		} else {
			switchConstFail(a)
		}
	default:
		switchConstFail(a)
	}
	// Concrete arithmetic wraps at the declared integer width, while pending
	// literal expressions retain arbitrary precision until contextual conversion.
	if result.Int != nil && isIntegerType(c, result.Type) && !result.Type.Same(intlitASTType()) {
		t := c.ResolveUnderlying(result.Type)
		bits := uint(t.Size(c) * 8)
		modulus := new(big.Int).Lsh(big.NewInt(1), bits)
		result.Int.Mod(result.Int, modulus)
		if t.Signed && result.Int.Bit(int(bits-1)) != 0 {
			result.Int.Sub(result.Int, modulus)
		}
	}
	if isFloatType(c, result.Type) && c.ResolveUnderlying(result.Type).Name == "f32" {
		result.Float = float64(float32(result.Float))
	}
	return result
}

func (v *switchConstant) key(c *Context, head ASTType) string {
	if v.Nil {
		return "nil"
	}
	if v.Original != nil {
		return "v:" + v.Type.String() + ":" + v.Int.String()
	}
	if v.Int != nil {
		if isFloatType(c, head) {
			f, _ := new(big.Float).SetInt(v.Int).Float64()
			if c.ResolveUnderlying(head).Name == "f32" {
				f = float64(float32(f))
			}
			return fmt.Sprintf("f:%g", f)
		}
		return "i:" + v.Int.String()
	}
	if isFloatType(c, v.Type) || v.Type.Same(floatlitASTType()) {
		f := v.Float
		if c.ResolveUnderlying(head).Name == "f32" {
			f = float64(float32(f))
		}
		if math.IsNaN(f) {
			CompileErrorF(v, "NaN switch case can never match with ==")
		}
		if f == 0 {
			f = 0
		}
		return fmt.Sprintf("f:%g", f)
	}
	return fmt.Sprintf("b:%t", v.Bool)
}

func compileSwitchConstant(of io.Writer, c *Context, v *switchConstant, dest spot) spot {
	if v.Original != nil {
		return compileTop(of, c, v.Original, dest)
	}
	t := v.Type
	if dest.empty() {
		dest = newSpot(of, c, c.Temp(), switchConcreteType(t))
	}
	switch {
	case v.Nil:
		return compileTop(of, c, &Literal{Val: nil, p: v.p}, dest)
	case v.Int != nil:
		if isFloatType(c, dest.t) {
			f, _ := new(big.Float).SetInt(v.Int).Float64()
			emitFloatConstant(of, c, dest, f)
		} else {
			if !litFitsIn(v.Int, c.ResolveUnderlying(dest.t)) {
				CompileErrorF(v, "Literal %s does not fit in type %s", v.Int, dest.t)
			}
			fmt.Fprintf(of, "\tmov %s %s\n", dest.ref, v.Int.String())
		}
	case isFloatType(c, t) || t.Same(floatlitASTType()):
		emitFloatConstant(of, c, dest, v.Float)
	default:
		fmt.Fprintf(of, "\tmov %s %d\n", dest.ref, map[bool]int{false: 0, true: 1}[v.Bool])
	}
	return dest
}

// switchResultOrigin uses the same origin union as ordinary branch joins.
// A local in any result arm must keep the whole expression escape-restricted.
func switchResultOrigin(c *Context, s *VariantSwitch, name string) flow.PointerExpr {
	if s.Scrutinee.ASTType(c).IsUnion() {
		return switchResultOrigin(c, lowerUnionSwitch(c, s), name)
	}

	var joined flow.PointerExpr
	path, hasPath := FlowPathForExpr(s.Scrutinee)
	vd, isVariant := c.ValuesDeclForName(s.Scrutinee.ASTType(c).Name)
	key := path.Key()
	previous, hadPrevious := c.variantNarrow[key]
	if hasPath && isVariant {
		defer func() {
			if hadPrevious {
				c.variantNarrow[key] = previous
			} else {
				delete(c.variantNarrow, key)
			}
		}()
	}
	for _, cs := range s.Cases {
		if !fallsThrough(cs.Body) {
			continue
		}
		if hasPath && isVariant {
			for i := range vd.Cases {
				if vd.Cases[i].Name == cs.Name {
					c.SetVariantNarrow(key, &vd.Cases[i])
					break
				}
			}
		}
		joined = c.PointerFlow().JoinOrigins(joined, pointerExprForAST(c, cs.Body, name))
	}
	if hasPath && isVariant {
		delete(c.variantNarrow, key)
	}
	if s.Default != nil {
		joined = c.PointerFlow().JoinOrigins(joined, pointerExprForAST(c, s.Default, name))
	}
	return joined
}

func boundSwitchContext(c *Context, s *BoundSwitch) *Context {
	sc := c.SubContext()
	t := switchConcreteType(s.Decl.Init.ASTType(c))
	sc.BindVar(s.Decl, s.Decl.Name, t, s.Decl.IsConst)
	if t.IsUnion() {
		recordUnionPayloadProvenance(sc, s.Decl.Name, t, s.Decl.Init)
	} else {
		sc.PointerFlow().AssignPointer(flow.Binding(s.Decl.Name), pointerExprForAST(c, s.Decl.Init, s.Decl.Name))
	}
	return sc
}

func compileSwitchResult(of io.Writer, c *Context, body AST, dest spot, returning bool) {
	if !fallsThrough(body) {
		value := compileTop(of, c, body, nullspot)
		value.free(of)
		return
	}
	if !dest.empty() {
		body = prepareUnionExpression(c, body, dest.t, returning)
		t := switchConcreteType(body.ASTType(c))
		if t.Same(voidASTType()) {
			CompileErrorF(body, "Expression switch case must yield a value or exit")
		}
		if _, reason := coerceType(c, dest.t, t); reason != coerceOK && !dest.t.Same(t) {
			CompileErrorF(body, "Switch case result has type %s, expected %s", t, dest.t)
		}
	}
	value := compileTop(of, c, body, dest)
	if !dest.empty() && !value.empty() && !value.same(&dest) {
		move(of, c, dest, value)
		value.free(of)
	}
	if !dest.empty() && dest.t.HasOwned() {
		moveOwnedSourceForLocalOwner(of, c, dest.t, body)
	}
	if dest.empty() {
		value.free(of)
	}
}

func compileValueSwitch(of io.Writer, c *Context, s *VariantSwitch, dest spot) spot {
	t := switchConcreteType(s.Scrutinee.ASTType(c))
	resultType := s.valueASTType(c)
	if dest.empty() && !resultType.Same(voidASTType()) {
		dest = newSpot(of, c, c.Temp(), resultType)
	}
	if t.OwnedMask&1 != 0 {
		switch s.Scrutinee.(type) {
		case *Funcall, *Alloc, *OwnedPromotion:
			CompileErrorF(s.Scrutinee, "Owned temporary in switch head requires a head binding: use `switch (x := expr)`")
		}
	}
	labels := make([]AST, len(s.Cases))
	seen := make(map[string]bool)
	for i, cs := range s.Cases {
		label := foldSwitchConstantAs(c, cs.Label, t)
		lt := label.Type
		if c.IsInterfaceType(t) {
			label.Type = switchConcreteType(label.Type)
			lt = label.Type
		}
		if !label.Nil {
			if _, reason := coerceType(c, t, lt); reason != coerceOK && !t.SameRepr(lt) && !c.IsInterfaceType(t) {
				CompileErrorF(cs.Label, "Switch case has type %s, expected %s", lt, t)
			}
		} else {
			// Use the ordinary equality nil check, including interface handling.
			underlying := c.ResolveUnderlying(t)
			if underlying.Indirection == 0 && underlying.FuncSig == nil && !c.IsInterfaceType(t) {
				CompileErrorF(cs.Label, "Cannot compare switch type %s with nil", t)
			}
		}
		key := label.key(c, t)
		if c.IsInterfaceType(t) && !label.Nil {
			key = switchConcreteType(lt).String() + ":" + key
		}
		if seen[key] {
			CompileErrorF(cs.Label, "Duplicate switch case %s", key[2:])
		}
		seen[key] = true
		// Leave nil as a literal so == uses its nullable/interface path.
		if label.Nil {
			labels[i] = &Literal{Val: nil, p: label.p}
		} else {
			labels[i] = label
		}
	}
	exhaustive := s.Default != nil || (c.ResolveUnderlying(t).Same(boolASTType()) && seen["b:true"] && seen["b:false"])
	s.Exhaustive = exhaustive
	if !dest.empty() && !exhaustive {
		CompileErrorF(s, "Expression switch requires a default case unless all possible values are covered")
	}
	// Snapshot the value once, even if it is a mutable binding or a call.
	head := newSpot(of, c, c.Temp(), t)
	value := compileTop(of, c, s.Scrutinee, head)
	if !value.same(&head) {
		move(of, c, head, value)
		value.free(of)
	}
	end := c.Label("swend")
	before := c.FlowSnapshot()
	remaining := before
	var after []FlowSnapshot
	for i, cs := range s.Cases {
		c.RestoreFlowSnapshot(remaining)
		next := c.Label("swnext")
		compare := &Op2{Type: n_deq, First: &switchHead{PreboundSpot{S: head, p: s.Scrutinee.Pos()}}, Second: labels[i]}
		compare.ASTType(c)
		test := compileTop(of, c, compare, nullspot)
		fmt.Fprintf(of, "\tcmp %s 0\n\tje %s\n", test.ref, next)
		test.free(of)
		// Comparing the cached value with nil narrows the original binding,
		// since no case body has executed on this path yet.
		condition := &Op2{Type: n_deq, First: s.Scrutinee, Second: labels[i]}
		path, conditionType, nonNullOnMatch, nullable := nullablePointerPathForIf(c, condition)
		unmatched := c.FlowSnapshot()
		if nullable {
			narrow := func(nonNull bool) {
				if nonNull {
					c.SetNullFact(path, NullKnownNonNull)
				} else {
					c.SetNullFact(path, NullKnownNull)
					if conditionType.HasOwned() && path.Fields == "" {
						c.MoveConsume(path.Root)
					}
				}
			}
			narrow(!nonNullOnMatch)
			unmatched = c.FlowSnapshot()
			c.RestoreFlowSnapshot(remaining)
			narrow(nonNullOnMatch)
		}
		compileSwitchResult(of, c, cs.Body, dest, s.ReturnContext)
		if fallsThrough(cs.Body) {
			after = append(after, c.FlowSnapshot())
		}
		fmt.Fprintf(of, "\tjmp %s\n\tlabel %s\n", end, next)
		remaining = unmatched
	}
	c.RestoreFlowSnapshot(remaining)
	if s.Default != nil {
		compileSwitchResult(of, c, s.Default, dest, s.ReturnContext)
		if fallsThrough(s.Default) {
			after = append(after, c.FlowSnapshot())
		}
	} else if !exhaustive {
		after = append(after, remaining)
	}
	mergeSwitchFlows(c, s, before, after, false)
	fmt.Fprintf(of, "\tlabel %s\n", end)
	head.free(of)
	return dest
}

// Ordinary switches use the same exact field-obligation merge as if/else.
// Variant switches may union consumed payload fields because narrowing ends.
func mergeSwitchFlows(c *Context, a AST, before FlowSnapshot, after []FlowSnapshot, variant bool) {
	if len(after) == 0 {
		c.RestoreFlowSnapshot(before)
		return
	}
	merged := after[0]
	for _, s := range after[1:] {
		for name := range before.Owned {
			if !c.SameObligationLiveAcross(merged, s, name) {
				CompileErrorF(a, "Owned binding %q is consumed on some switch cases but not others", name)
			}
		}
		if variant {
			fields := make(map[FlowPath]bool)
			for k, v := range merged.OwnedFields {
				if v {
					fields[k] = true
				}
			}
			for k, v := range s.OwnedFields {
				if v {
					fields[k] = true
				}
			}
			merged.OwnedFields = fields
		} else {
			merged.OwnedFields = mergeOwnedFieldFacts(c, a, merged, s)
		}
		merged.Null = MergeNullFacts(merged.Null, s.Null)
		merged.Borrowed = MergeBorrowedBindings(merged.Borrowed, s.Borrowed)
		merged.Pointer = flow.Merge(merged.Pointer, s.Pointer)
	}
	c.RestoreFlowSnapshot(merged)
}
