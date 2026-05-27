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
	if t.Indirection == 0 || t.NilMask&1 == 0 {
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
				CompileErrorF(val, "Cannot move owned field %s out of an owned aggregate", v.Member)
			}
			if fieldType.NilMask&1 == 0 {
				CompileErrorF(val, "Cannot move non-null pointer field %s; use *?T if the field may be emptied", v.Member)
			}
			if path, ok := FlowPathForExpr(val); ok && c.OwnedFieldConsumed(path) {
				CompileErrorF(val, "Cannot move \"%s\": it was already moved", path.Key())
			}
		}
	}
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
			for _, f := range sl.Fields {
				if f.Val != nil && f.Val.ASTType(c).Indirection > 0 {
					pexpr := pointerExprForAST(c, f.Val, "")
					c.PointerFlow().SetFieldPointer(flow.Binding(targetSym.Name), f.Name, pexpr)
				}
			}
		}
	} else if binding, field, ok := isDirectStructFieldTarget(c, target); ok && dstt.Indirection > 0 {
		pexpr := pointerExprForAST(c, val, "")
		c.PointerFlow().SetFieldPointer(flow.Binding(binding), field, pexpr)
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
		baseType := v.Val.ASTType(c)
		if baseType.Indirection > 0 {
			if !canWriteImmediatePointee(baseType) {
				return false, fmt.Sprintf("Cannot write field %q through read-only pointer of type %s; pointer must be *mut", v.Member, baseType)
			}
			return true, ""
		}
		return lvalueIsWritable(c, v.Val)
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

func pointerExprForAST(c *Context, a AST, assignedName string) flow.PointerExpr {
	if a.ASTType(c).Indirection == 0 {
		return c.PointerFlow().UnknownPointer()
	}
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
		if sym, ok := ast.Val.(*Symbol); ok && !c.IsGlobalBinding(sym.Name) {
			if t, ok2 := c.TypeForVar(sym.Name); ok2 && t.Indirection == 0 {
				return c.PointerFlow().GetFieldPointer(flow.Binding(sym.Name), ast.Member)
			}
		}
		return c.PointerFlow().UnknownPointer()
	case *NonNullAssert:
		return pointerExprForAST(c, ast.Val, assignedName)
	case *OwnedPromotion:
		return pointerExprForAST(c, ast.Val, assignedName)
	case *Funcall:
		if assignedName != "" {
			retType := ast.ASTType(c)
			if retType.Indirection > 0 && retType.OwnedMask&1 != 0 {
				return c.PointerFlow().NewAllocatedOrigin(flow.Binding(assignedName))
			}
		}
		return c.PointerFlow().UnknownPointer()
	default:
		return c.PointerFlow().UnknownPointer()
	}
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
		baseType := dot.Val.ASTType(c)
		def, ok := structDeclForType(c, baseType)
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
	if !ok || dst.Indirection == 0 {
		return
	}
	if c.IsGlobalBinding(sym.Name) {
		c.PointerFlow().AssignPointer(flow.Binding(sym.Name), c.PointerFlow().UnknownPointer())
		return
	}
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
		c.Move(v.Name)
		declared, _ := c.DeclaredTypeForVar(v.Name)
		if declared.Indirection > 0 && declared.NilMask&1 != 0 {
			fmt.Fprintf(of, "\tmov %s 0\n", v.Name)
			c.SetNullFact(VarFlowPath(v.Name), NullKnownNull)
		}
	case *Address:
		// owned(&x) discharges x's obligation through the pointer: the
		// callee will consume what x's slot referenced. Mark x as moved.
		if v.Var != "" {
			checkOwnedSourceAvailable(c, &Symbol{Name: v.Var, p: v.p})
			c.Move(v.Var)
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
		checkBorrowedPointerDoesNotEscape(c, f.Val, fmt.Sprintf("field %v.%s", lit.Type, f.Name))
		checkAddressOfOwnedForDest(c, f.Val, fieldType)
		if srcType.Same(intlitASTType()) {
			srcType = fieldType
		} else if !fieldType.Accepts(srcType) {
			CompileErrorF(f.Val, "For field %v.%s, expected type %v but got %v",
				lit.Type, f.Name, fieldType, srcType)
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
	if ast.Pkg != "" || ast.FName != "new" || len(ast.Args) != 1 || dst.Indirection == 0 {
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
	if srcType.Same(intlitASTType()) || srcType.Name == "<nil>" {
		srcType = pointee
	}
	checkAddressOfOwnedForDest(c, val, pointee)
	if !pointee.Accepts(srcType) {
		CompileErrorF(val, "Cannot initialize allocated %s with value of type %s", pointee, val.ASTType(c))
	}
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
			if !dest.empty() && actual.Accepts(expect) {
				return
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
		// Already registered in toASTTop; nothing to emit.
		return nullspot
	case *TypeWithMethodsDecl:
		// Type alias already registered in toASTTop.
		// Emit each method as `function TypeName.method_name` — the assembler
		// prepends the package name, producing pkg.TypeName.method_name.
		for _, m := range ast.Methods {
			qualified := &FuncDecl{
				Name:   ast.Name + "." + m.Name,
				Args:   m.Args,
				Return: m.Return,
				Body:   m.Body,
				p:      m.p,
			}
			compileTop(of, c, qualified, nullspot)
		}
		return nullspot
	case *InterfaceDecl:
		// No code to emit; interface declarations are purely type-level.
		return nullspot
	case *StructDecl:
		c.DefineStruct(ast.TName, ast)
		// Emit the struct shape so bas can carry it into the .bo for
		// cross-package import. Other packages can then declare
		// 'mypkg.Name'-typed values, construct them via 'mypkg.Name{...}',
		// and walk their fields.
		fmt.Fprintf(of, "struct %s {\n", ast.TName)
		for _, f := range ast.Fields {
			fmt.Fprintf(of, "\t%s %s\n", f.Name, f.Type)
		}
		fmt.Fprintf(of, "}\n")
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
		c.BindVar(a, ast.Name, ast.Type, ast.IsConst)
		if ast.Type.Indirection > 0 {
			c.PointerFlow().DeclarePointer(flow.Binding(ast.Name))
		}
		s := newSpot(of, c, ast.Name, ast.Type)
		if ast.Init != nil {
			if sl, ok := ast.Init.(*StructLiteral); ok {
				fillAnonymousLiteralIfNeeded(sl, ast.Type)
			}
			initIsBorrowed := borrowedPointerExpr(c, ast.Init)
			if sl, ok := ast.Init.(*StructLiteral); ok && sameIgnoringOwned(ast.Type, sl.Type) {
				compileStructLiteralInto(of, c, a, sl, s, ast.Type)
				for _, f := range sl.Fields {
					if f.Val != nil && f.Val.ASTType(c).Indirection > 0 {
						pexpr := pointerExprForAST(c, f.Val, "")
						c.PointerFlow().SetFieldPointer(flow.Binding(ast.Name), f.Name, pexpr)
					}
				}
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
			// Interface coercion: concrete pointer → interface fat pointer.
			if c.IsInterfaceType(dstt) && srct.Indirection > 0 {
				emitInterfaceFatPtr(of, c, a, dstt, srct, ast.Init, s.ref)
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
			if !dstt.Accepts(srct) {
				CompileErrorF(a, "Cannot initialize %s with value of type %s", dstt, srct)
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
			if dstt.Indirection > 0 {
				c.SetBorrowedBinding(ast.Name, initIsBorrowed)
				c.PointerFlow().AssignPointer(flow.Binding(ast.Name), pointerExprForAST(c, ast.Init, ast.Name))
			}
			// Propagate field pointer facts on struct copy.
			if sym, ok := ast.Init.(*Symbol); ok && !c.IsGlobalBinding(sym.Name) && dstt.Indirection == 0 {
				c.PointerFlow().CopyFieldPointers(flow.Binding(sym.Name), flow.Binding(ast.Name))
			}
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
			c.Move(ast.Name)
		}
		return nullspot
	case *FuncDecl:
		c := c.SubContext()
		defer c.ForgetPointerBindings()
		retlab := c.PushRetlabel(ast.Return)
		defer c.PopRetlabel()
		fmt.Fprintf(of, "function %s\n", ast.Name)
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
			sig.WriteString(a.Type.String())
		}
		sig.WriteString(")")
		if !ast.Return.Same(voidASTType()) {
			sig.WriteString(" ")
			sig.WriteString(ast.Return.String())
		}
		fmt.Fprintf(of, "\ttype %s\n", sig.String())
		for i, a := range ast.Args {
			fmt.Fprintf(of, "\targi %s %d %d\n", a.Name, i, a.Type.Size(c)*8)
			c.BindVar(ast, a.Name, a.Type, a.IsConst)
			if a.Type.Indirection > 0 && !a.Type.HasOwned() {
				c.SetBorrowedBinding(a.Name, true)
			}
			if a.Type.Indirection > 0 {
				c.PointerFlow().AssignPointer(flow.Binding(a.Name), c.PointerFlow().NewObject(flow.Binding(a.Name)))
			}
		}
		fmt.Fprintf(of, "\n\tprologue\n\n")
		compileTop(of, c, ast.Body, nullspot)
		for _, name := range c.UnconsumedOwned() {
			CompileErrorF(a, "Owned binding \"%s\" goes out of scope without being consumed; call dispose() or pass it to a consuming function", name)
		}
		if ast.Name == "main" {
			note(of, "\n\t// default return 0 from main\n")
			fmt.Fprintf(of, "\tmov rax 0\n")
		}
		fmt.Fprintf(of, "\n\tlabel %s\n", retlab)
		fmt.Fprintf(of, "\tepilogue\n")
		fmt.Fprintf(of, "\tret\n\n")
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
		if ast.Pkg == "" && ast.FName == "alloc" {
			return compileAllocBuiltin(of, c, a, ast, dest)
		}
		if ast.Pkg == "" && ast.FName == "new" {
			return compileNewBuiltin(of, c, a, ast, dest)
		}
		if ast.Pkg == "" && ast.FName == "free" {
			return compileFreeBuiltin(of, c, a, ast)
		}
		// Cast expression: type name used as a single-argument function.
		// Only valid for unqualified calls.
		if ast.Pkg == "" {
			if destType, ok := c.TypeByName(ast.FName); ok {
				if len(ast.Args) != 1 {
					CompileErrorF(a, "Type cast %s() requires exactly one argument", ast.FName)
				}
				return compileCast(of, c, ast.Args[0], destType, dest)
			}
		}

		decl, resolvedPkg, ok := c.FuncDeclForCall(ast.Pkg, ast.FName)
		if !ok {
			// Fall back to an indirect call through a function-pointer
			// value. Two shapes:
			//
			//   foo(args)    — ast.Pkg == "", call through a fn-typed
			//                  local/global named `foo`
			//   d.f(args)    — ast.Pkg == "d" (parser packs Dot-of-call
			//                  this way regardless of whether `d` is a
			//                  package or a struct-valued variable);
			//                  call through field `f` of struct `d`
			if ast.Pkg == "" {
				if vt, vok := c.TypeForVar(ast.FName); vok && vt.FuncSig != nil {
					return compileIndirectCall(of, c, a, ast, ast.FName, vt.FuncSig, dest)
				}
			} else {
				if vt, vok := c.TypeForVar(ast.Pkg); vok {
					// Interface method dispatch: v.method(args) where v is an interface type.
					if c.IsInterfaceType(vt) {
						ifaceDecl, _ := c.InterfaceForName(vt.Name)
						return compileInterfaceMethodCall(of, c, a, ast, vt, ifaceDecl, dest)
					}
					// Concrete method call: v.method(args) → TypeName.method(receiver, args).
					typeName := vt.Name // leaf type name regardless of pointer depth
					if method, mok := c.MethodForType(typeName, ast.FName); mok {
						if len(method.Args) == 0 {
							CompileErrorF(a, "%s.%s is a static method (no receiver); call as %s.%s(...), not %s.%s(...)",
								typeName, ast.FName, typeName, ast.FName, ast.Pkg, ast.FName)
						}
						return compileConcreteMethodCall(of, c, a, ast, vt, typeName, method, dest)
					}
					// Struct function-pointer field call.
					if sdecl, sok := structDeclForType(c, vt); sok {
						off, mtype := sdecl.ByteOffset(c, ast.FName)
						if mtype.FuncSig != nil {
							baseAddr := compileTop(of, c, &Symbol{Name: ast.Pkg}, nullspot)
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
		if len(ast.Args) != len(decl.Args) {
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
		if !hasName {
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
			// Fixed array: addr points at the array storage itself.
			addr = v
			addr.t = baset
			addr.t.Indirection++
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
			} else if dstt.HasOwned() && c.IsMoved(targetSym.Name) && c.AddressTaken(targetSym.Name) {
				// Phase A re-init: assignment to a previously-consumed
				// owned binding starts a fresh lifecycle, but only when
				// no pointer to its storage could still be live. The
				// `AddressTaken` flag is sticky, so we reject if `&x`
				// was ever taken in this binding's lifetime; precise
				// alias liveness would be Phase B.
				CompileErrorF(a, "Cannot re-initialize \"%s\": its address was taken; an alias may still reference its storage", targetSym.Name)
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
			baseType := dot.Val.ASTType(c)
			if parentOwnsFields(baseType) && dstt.HasOwned() {
				CompileErrorF(a, "Cannot assign to owned field %s of an owned aggregate after initialization", dot.Member)
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
		// Interface coercion at assignment: concrete pointer → fat pointer.
		if c.IsInterfaceType(dstt) && srct.Indirection > 0 {
			emitInterfaceFatPtr(of, c, a, dstt, srct, ast.Val, lv.ref)
			markMovedIfOwnedSource(of, c, dstt, ast.Val)
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
		// Struct-literal assignment to a matching destination (modulo owned):
		// initialize the slot directly through compileStructLiteralInto, the
		// same path VarDecl uses for `var f owned foo = foo{...}`. This makes
		// struct-literal re-init after dispose work without an explicit owned()
		// wrapper, and stays consistent with first-init.
		if sl, ok := ast.Val.(*StructLiteral); ok && sameIgnoringOwned(dstt, sl.Type) {
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
			if !dstt.Accepts(srct) {
				CompileErrorF(a, "Cannot assign different types %s = %s", dstt, srct)
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
					c.Move(condPath.Root)
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
					c.Move(condPath.Root)
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
		if sl, ok := ast.Val.(*StructLiteral); ok {
			fillAnonymousLiteralIfNeeded(sl, retType)
		}
		valType := ast.Val.ASTType(c)
		if retType.Indirection > 0 {
			checkBorrowedPointerDoesNotEscape(c, ast.Val, "return")
			checkLocalOriginDoesNotEscape(c, ast.Val, "return")
		}
		if retType.Indirection == 0 {
			retVal := ast.Val
			if op, ok := retVal.(*OwnedPromotion); ok {
				retVal = op.Val
			}
			if sym, ok := retVal.(*Symbol); ok && !c.IsGlobalBinding(sym.Name) {
				if escaped, field := c.PointerFlow().CheckStructFieldEscape(flow.Binding(sym.Name)); escaped {
					CompileErrorF(ast.Val, "Cannot return %q by value: field %q contains a pointer to local-scope storage; the alias would dangle in the returned copy", sym.Name, field)
				}
			}
			if sl, ok := retVal.(*StructLiteral); ok {
				for _, f := range sl.Fields {
					if f.Val != nil && f.Val.ASTType(c).Indirection > 0 {
						ptr := pointerExprForAST(c, f.Val, "")
						if ptr.KnownOrigin && c.PointerFlow().OriginKindOf(ptr.Origin) == flow.OriginLocal {
							CompileErrorF(ast.Val, "Cannot return struct literal by value: field %q contains a pointer to local-scope storage; the alias would dangle in the returned copy", f.Name)
						}
					}
				}
			}
		}
		if sl, ok := ast.Val.(*StructLiteral); ok && sameIgnoringOwned(retType, sl.Type) {
			valType = retType
		}
		checkAddressOfOwnedForDest(c, ast.Val, retType)
		// Interface coercion at return: concrete pointer → fat pointer in rax.
		if c.IsInterfaceType(retType) && valType.Indirection > 0 {
			s := newSpotWithReg(of, c, c.Temp(), retType, "rax")
			emitInterfaceFatPtr(of, c, a, retType, valType, ast.Val, s.ref)
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
		if valType.Same(intlitASTType()) {
			valType = numASTType()
		}
		raxName := raxForType(valType)
		dest := newSpotWithReg(of, c, c.Temp(), valType, raxName)
		v := compileTop(of, c, ast.Val, dest)
		if !v.same(&dest) {
			dest.free(of)
			dest = v
			CompileErrorF(a, "Return not same as dest. Should this happen?")
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
		c.Move(ast.Var)
		if declared.Indirection > 0 {
			ptrExpr := c.PointerFlow().Pointer(flow.Binding(ast.Var))
			if ptrExpr.KnownOrigin {
				c.PointerFlow().InvalidateOrigin(ptrExpr.Origin, flow.TargetDead)
			}
		} else {
			c.PointerFlow().InvalidateOrigin(flow.Origin(ast.Var), flow.TargetMoved)
		}
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
	for i := 0; i < len(f.Args); i++ {
		arg := f.Args[i]
		param := d.Args[i].Type
		if sl, ok := arg.(*StructLiteral); ok {
			fillAnonymousLiteralIfNeeded(sl, param)
		}
		argt := arg.ASTType(c)
		checkAddressOfOwnedForDest(c, arg, param)
		// Interface coercion at a call site: concrete pointer → fat pointer.
		if c.IsInterfaceType(param) && argt.Indirection > 0 {
			s := newSpot(of, c, c.Temp(), param)
			emitInterfaceFatPtr(of, c, arg, param, argt, arg, s.ref)
			if param.OwnedMask != 0 {
				markMovedIfOwnedSource(of, c, param, arg)
			}
			argspots = append(argspots, s)
			continue
		}
		if argt.Same(intlitASTType()) {
			argt = param
		} else if argt.Name == "<nil>" && param.Accepts(argt) {
			// nil literal takes the parameter's nullable-pointer type
			// rather than carrying its synthetic <nil> through codegen.
			argt = param
		} else if !param.Accepts(argt) {
			CompileErrorF(arg, "For argument %d, expected type %v but got %v",
				i, param, argt)
		}
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
	for i := 0; i < len(f.Args); i++ {
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
		if argt.Size(c) >= PTR_SIZE {
			// The >= requires some explanation here.
			// It relies on the fact that for objects of size > PTR_SIZE,
			// they are held as pointers in register, meaning we can move
			// them around as if they were 64-bit values.
			fmt.Fprintf(of, "\tinreg %s %s\n", argspots[i].ref, order[i])
		} else {
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
		default:
			return nil, false
		}
		return result, true
	}
	return nil, false
}

// compileCast compiles a type cast expression: destType(srcExpr).
// Handles integer literal coercion, same-size reinterpretation, widening, and narrowing.
func compileCast(of io.Writer, c *Context, src AST, destType ASTType, dest spot) spot {
	if dest.empty() {
		dest = newSpot(of, c, c.Temp(), destType)
	}

	srcType := src.ASTType(c)

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

	// Compile the source value.
	srcSpot := compileTop(of, c, src, nullspot)

	srcUnderlying := c.ResolveUnderlying(srcType)
	dstUnderlying := c.ResolveUnderlying(destType)
	srcSize := srcUnderlying.Size(c)
	dstSize := dstUnderlying.Size(c)

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
// and delegates to compileTop. The receiver is &v when v is a value type, or v itself
// when v is already a pointer type (since methods always take pointer receivers).
func compileConcreteMethodCall(of io.Writer, c *Context, a AST, callNode *Funcall,
	receiverType ASTType, typeName string, method *FuncDecl, dest spot) spot {
	var receiver AST
	if receiverType.Indirection > 0 {
		receiver = &Symbol{Name: callNode.Pkg, p: callNode.p}
	} else {
		receiver = &Address{Var: callNode.Pkg, p: callNode.p}
	}
	allArgs := make([]AST, 0, 1+len(callNode.Args))
	allArgs = append(allArgs, receiver)
	allArgs = append(allArgs, callNode.Args...)
	synthCall := &Funcall{
		Pkg:   "",
		FName: typeName + "." + callNode.FName,
		Args:  allArgs,
		p:     callNode.p,
	}
	return compileTop(of, c, synthCall, dest)
}

// compileInterfaceMethodCall emits a vtable-dispatch call for an interface method.
// The interface variable (named callNode.Pkg) is a memory-backed 16-byte fat pointer:
// [var+0] = data pointer, [var+8] = vtable pointer. The data pointer is passed as
// the first argument (rdi); user-supplied args follow (rsi, rdx, ...).
func compileInterfaceMethodCall(of io.Writer, c *Context, a AST, callNode *Funcall,
	ifaceType ASTType, ifaceDecl *InterfaceDecl, dest spot) spot {

	// Locate the method and its index in the interface.
	methodIdx := -1
	var isig InterfaceMethodSig
	for i, m := range ifaceDecl.Methods {
		if m.Name == callNode.FName {
			methodIdx = i
			isig = m
			break
		}
	}
	if methodIdx < 0 {
		CompileErrorF(a, "Interface %s has no method %s", ifaceDecl.Name, callNode.FName)
	}

	// Params[0] is the receiver; user provides Params[1:].
	userParams := isig.Params[1:]
	if len(callNode.Args) != len(userParams) {
		CompileErrorF(a, "Method %s.%s expected %d arguments, but called with %d",
			ifaceDecl.Name, callNode.FName, len(userParams), len(callNode.Args))
	}

	// The interface variable is memory-backed; its name is the base address.
	ifaceRef := callNode.Pkg

	// Load data ptr, vtable ptr, and fn ptr into temps BEFORE setting up call
	// registers, so they are not evicted by arg compilation.
	dataPtr := newSpot(of, c, c.Temp(), ASTType{Name: "i64"})
	vtablePtr := newSpot(of, c, c.Temp(), ASTType{Name: "i64"})
	fnPtr := newSpot(of, c, c.Temp(), ASTType{Name: "i64"})
	fmt.Fprintf(of, "\tmov %s [%s+0]\n", dataPtr.ref, ifaceRef)
	fmt.Fprintf(of, "\tmov %s [%s+8]\n", vtablePtr.ref, ifaceRef)
	fmt.Fprintf(of, "\tmov %s [%s+%d]\n", fnPtr.ref, vtablePtr.ref, methodIdx*8)
	vtablePtr.free(of)

	order := []string{"rdi", "rsi", "rdx", "rcx", "r8", "r9"}

	// Compile user args into temp spots. They go into rsi+ (index 1+);
	// rdi (index 0) is reserved for the data pointer.
	var argspots []spot
	for i, arg := range callNode.Args {
		param := userParams[i].Type
		argt := arg.ASTType(c)
		if argt.Same(intlitASTType()) {
			argt = param
		} else if argt.Name == "<nil>" && param.Accepts(argt) {
			argt = param
		} else if !param.Accepts(argt) {
			CompileErrorF(arg, "For argument %d, expected type %v but got %v", i, param, argt)
		}
		if param.HasOwned() {
			checkOwnedSourceAvailable(c, arg)
		}
		argIdx := i + 1 // rsi=1, rdx=2, ...
		if argIdx > 5 {
			CompileErrorF(arg, "More than 5 user arguments not supported for interface methods")
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

	// Mark owned moves after all args are compiled.
	for i, arg := range callNode.Args {
		if userParams[i].Type.HasOwned() {
			markMovedIfOwnedSource(of, c, userParams[i].Type, arg)
		}
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

// emitInterfaceFatPtr validates that srct (a concrete pointer) satisfies the interface
// dstt, registers the vtable, and emits stores to fill the two-word fat pointer at
// destRef. The caller is responsible for allocating destRef before calling and for
// handling owned-source marking and flow updates afterwards.
func emitInterfaceFatPtr(of io.Writer, c *Context, errNode AST, dstt, srct ASTType, valAST AST, destRef string) {
	concreteTypeName := srct.StripOwned().Name
	ifaceDecl, _ := c.InterfaceForName(dstt.Name)
	if !c.TypeSatisfiesInterface(concreteTypeName, ifaceDecl) {
		CompileErrorF(errNode, "Type %s does not implement interface %s", concreteTypeName, dstt.Name)
	}
	if dstt.OwnedMask != 0 && srct.OwnedMask == 0 {
		CompileErrorF(errNode, "Cannot use non-owned %s where owned %s is required", srct, dstt.Name)
	}
	vtableName := fmt.Sprintf("__vtable_%s_%s", concreteTypeName, dstt.Name)
	c.NeedVtable(vtableName, concreteTypeName, dstt.Name, c.Pkgname(), ifaceDecl)
	val := compileTop(of, c, valAST, nullspot)
	fmt.Fprintf(of, "\tmov [%s+0] %s\n", destRef, val.ref)
	val.free(of)
	vtmp := newSpot(of, c, c.Temp(), ASTType{Name: "i64"})
	fmt.Fprintf(of, "\tlea %s %s\n", vtmp.ref, vtableName)
	fmt.Fprintf(of, "\tmov [%s+8] %s\n", destRef, vtmp.ref)
	vtmp.free(of)
}
