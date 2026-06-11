package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/knusbaum/gbasm/cmd/bosc/flow"
)

// retalias.go implements inferred return-parameter aliasing (alias_set).
//
// For each function the compiler computes, per return slot, the set of
// parameter indices that slot may alias. The result is memoized on the
// FuncDecl (ReturnAliases + AliasesComputed) and serialized through the
// .bo so cross-package callers can read it.
//
// The inference is demand-driven (run on first need during the Compile
// pass), cycle-safe (a conservative self-alias breaks recursion), and
// performs both recording (borrowed parameter → record its index) and
// rejection (a local origin reaching a returned slot is a hard error,
// reported here so the reject is order-independent regardless of whether
// the demand came from lowering f's body, a call site, or the
// interface-coercion guard).

// aliasClassification is the outcome of classifying one escaping origin.
type aliasClassKind int

const (
	aliasPass   aliasClassKind = iota // not escape-restricted (global/heap/unknown): record nothing
	aliasRecord                       // borrowed parameter: record its index
	aliasReject                       // local origin or variadic param: hard error
)

// aliasInProgressSet returns the cycle-guard set, lazily created on the
// root context.
func (c *Context) aliasInProgressSet() map[*FuncDecl]bool {
	root := c
	for root.parent != nil {
		root = root.parent
	}
	if root.aliasInProgress == nil {
		root.aliasInProgress = make(map[*FuncDecl]bool)
	}
	return root.aliasInProgress
}

// aliasSet returns f's inferred return-parameter alias set, computing it
// on first demand. Memoized via f.AliasesComputed. Imported FuncDecls
// arrive with AliasesComputed=true (the importer attaches the fact), so
// cross-package lookups short-circuit without any body walk.
func aliasSet(c *Context, f *FuncDecl) [][]int {
	if f == nil {
		return nil
	}
	if f.AliasesComputed {
		return f.ReturnAliases
	}
	// A function with no body to walk (e.g. a forward-declared or
	// otherwise bodyless decl) infers the empty set.
	if f.Body == nil {
		f.ReturnAliases = nil
		f.AliasesComputed = true
		return nil
	}

	inProgress := c.aliasInProgressSet()
	if inProgress[f] {
		// Cycle (direct or mutual recursion). Return a conservative
		// self-alias: the recursive call's result is assumed to alias
		// every (non-variadic) parameter position. The caller's
		// classification of the *arguments* still rejects locals.
		return conservativeSelfAlias(f)
	}
	inProgress[f] = true

	nslots := returnSlotCount(f.Return)
	result := make([][]int, nslots)

	// Run the walk in an isolated flow state so cold inference never
	// pollutes the live compile. Clone-save the root flow, install a
	// fresh state, seed f's params exactly as the prologue does, walk,
	// then restore. bosc aborts on first CompileErrorF, so no
	// defer-restore is needed on the reject path.
	saved := c.PointerFlow().Clone()
	c.RestorePointerFlow(flow.NewState())

	// Base the inference scope on the *root* compilation context, not the
	// passed-in c. c may itself be an enclosing function's body/inference
	// scope carrying local bindings (a recursive or transitive alias_set
	// call expands a callee while the caller's params are still bound); a
	// SubContext of that would have BindVar's shadow check fire on a
	// same-named parameter. The root holds all declarations (funcs,
	// structs, imports) and only file-scope globals as bindings, so an
	// inference scope rooted there sees every callee/type but no enclosing
	// local. Flow state is shared at the root and was swapped to a fresh
	// State above, so the isolation still holds.
	root := c
	for root.parent != nil {
		root = root.parent
	}
	ic := root.SubContext()
	seedParamsForInference(ic, f)
	walkReturnsForInference(ic, f, f.Body, result)

	c.RestorePointerFlow(saved)

	for s := range result {
		result[s] = sortDedup(result[s])
	}
	delete(inProgress, f)

	f.ReturnAliases = result
	f.AliasesComputed = true
	return result
}

// conservativeSelfAlias produces the over-approximation used when a cycle
// is detected: every return slot may alias every non-variadic parameter.
func conservativeSelfAlias(f *FuncDecl) [][]int {
	nslots := returnSlotCount(f.Return)
	var params []int
	for i := range f.Args {
		if f.Variadic && i == len(f.Args)-1 {
			continue
		}
		params = append(params, i)
	}
	out := make([][]int, nslots)
	for s := range out {
		cp := make([]int, len(params))
		copy(cp, params)
		out[s] = cp
	}
	return out
}

// returnSlotCount returns the number of return slots for a function's
// declared return type. A multi-return signature carries its value types
// in Return.AnonFields with MultiReturn=true; a single non-void return is
// one slot; void is zero slots.
func returnSlotCount(ret ASTType) int {
	if ret.MultiReturn {
		return len(ret.AnonFields)
	}
	if ret.Same(voidASTType()) {
		return 0
	}
	return 1
}

// returnSlotType returns the declared type of return slot s.
func returnSlotType(ret ASTType, s int) ASTType {
	if ret.MultiReturn {
		if s >= 0 && s < len(ret.AnonFields) {
			return ret.AnonFields[s].Type
		}
		return ASTType{}
	}
	return ret
}

// seedParamsForInference registers f's parameters into the inference
// context, mirroring the prologue at compile.go's FuncDecl case so the
// classifier sees the same borrowed-origin facts the live compile would.
func seedParamsForInference(ic *Context, f *FuncDecl) {
	for _, a := range f.Args {
		ic.BindVar(f, a.Name, a.Type, a.IsConst)
		if a.Type.Indirection > 0 && !a.Type.HasOwned() {
			ic.SetBorrowedBinding(a.Name, true)
		}
		if a.Type.Indirection > 0 {
			ic.PointerFlow().AssignPointer(flow.Binding(a.Name), ic.PointerFlow().NewObject(flow.Binding(a.Name)))
		} else if a.Type.IsSlice() && !a.Type.HasOwned() {
			pexpr := ic.PointerFlow().NewBorrowedOrigin(flow.Binding(a.Name))
			ic.PointerFlow().AssignPointer(flow.Binding(a.Name), pexpr)
		}
	}
}

// paramIndexOf returns the parameter index of binding name in f, or -1 if
// name is not a parameter.
func paramIndexOf(f *FuncDecl, name string) int {
	for i, a := range f.Args {
		if a.Name == name {
			return i
		}
	}
	return -1
}

// walkReturnsForInference does a flat, sequential pre-order walk of f's
// body, threading binding-introducing statements into the inference flow
// state (so an intermediate borrowed binding is resolvable) and
// classifying every return statement's slot expressions. No branch-merge
// precision: a binding's origin is fixed at its decl, so sequential
// processing is sound. OriginLocal escapes are rejected here via
// CompileErrorF.
func walkReturnsForInference(ic *Context, f *FuncDecl, node AST, result [][]int) {
	switch n := node.(type) {
	case nil:
		return
	case *Block:
		// Open a fresh lexical scope per block so a `const x` redeclared
		// in a sibling block does not collide (BindVar enforces no
		// redeclaration / no shadowing within one scope). Flow origins
		// live on the root flow state and survive the sub-scope, so
		// origin facts threaded here remain visible to outer returns.
		sc := ic.SubContext()
		for _, stmt := range n.Body {
			walkReturnsForInference(sc, f, stmt, result)
		}
	case *IfStmt:
		// Process each branch over its own flow snapshot, then merge the
		// resulting states so a field borrowed on *either* branch survives
		// into the post-if return (flow.Merge keeps an escape-restricted
		// origin from either side — the union, which over-records soundly).
		// Returns *inside* a branch are classified against that branch's
		// own state during its walk.
		before := ic.PointerFlow().Clone()
		walkReturnsForInference(ic, f, n.Then, result)
		thenState := ic.PointerFlow().Clone()
		ic.RestorePointerFlow(before)
		walkReturnsForInference(ic, f, n.Else, result)
		elseState := ic.PointerFlow().Clone()
		ic.RestorePointerFlow(flow.Merge(thenState, elseState))
	case *For:
		sc := ic.SubContext()
		threadInferenceBinding(sc, n.Init)
		walkReturnsForInference(sc, f, n.Body, result)
	case *Loop:
		walkReturnsForInference(ic, f, n.Body, result)
	case *TypeSwitch:
		for _, tc := range n.Cases {
			// Each case narrows BindName to the case's concrete type.
			// Bind it (no tracked origin) so returns inside the case that
			// reference it resolve. Interface/value narrowings carry no
			// borrow in v1.
			sc := ic.SubContext()
			if n.BindName != "" {
				if _, exists := sc.TypeForVar(n.BindName); !exists {
					sc.BindVar(n, n.BindName, tc.T, false)
				}
			}
			walkReturnsForInference(sc, f, tc.Body, result)
		}
	case *VarDecl:
		threadInferenceBinding(ic, n)
	case *Assignment:
		threadInferenceAssignment(ic, n)
	case *MultiBindDecl:
		// `var a T, var b U = expr` binds each target. Register the names
		// with their declared types so later returns/uses over these
		// bindings resolve (their origins are conservatively Unknown — a
		// multi-value source is not a single borrowed root we track in v1).
		for i := range n.Bindings {
			b := &n.Bindings[i]
			if _, exists := ic.TypeForVar(b.Name); !exists {
				ic.BindVar(b, b.Name, b.Type, b.IsConst)
			}
		}
	case *Return:
		classifyReturn(ic, f, n, result)
	default:
		// Other statements introduce no flow-relevant bindings for the
		// purposes of inference and contain no nested returns we track.
	}
}

// threadInferenceBinding processes a VarDecl during the inference walk,
// registering the binding's origin so a later `return t` over an
// intermediate binding resolves to the right root (recording or
// rejecting). Mirrors the VarDecl Init handling in compileTop.
func threadInferenceBinding(ic *Context, node AST) {
	vd, ok := node.(*VarDecl)
	if !ok || vd == nil {
		return
	}
	t := vd.Type
	if _, exists := ic.TypeForVar(vd.Name); !exists {
		ic.BindVar(vd, vd.Name, t, vd.IsConst)
	}
	if vd.Init == nil {
		// Uninitialized local fixed array (`var l byte[16]`) roots a
		// local origin so `return l[:]` is rejected.
		if t.IsArray() {
			ic.PointerFlow().NewLocalOrigin(flow.Binding(vd.Name))
		}
		return
	}
	// Gate origin computation on escape-relevance. A non-escape-relevant
	// value binding (an i64, an error, an interface result) carries no
	// tracked borrow in v1, so we skip pointerExprForAST entirely — which
	// also avoids resolving (and potentially erroring on) an Init the
	// cold walk has not fully established. The name is still bound above
	// so escape-relevant Inits that *reference* it resolve.
	if !typeIsEscapeRelevantForInference(ic, t) {
		return
	}
	if t.Indirection > 0 && !t.HasOwned() {
		ic.SetBorrowedBinding(vd.Name, ic.IsBorrowedBinding(rootBindingName(vd.Init)))
	}
	pexpr := pointerExprForAST(ic, vd.Init, vd.Name)
	if pexpr.KnownOrigin || pexpr.KnownSlot {
		ic.PointerFlow().AssignPointer(flow.Binding(vd.Name), pexpr)
	}
	// A struct returned by value from a call (`var b B = mk(arg)`) carries no
	// single Origin; record its borrowed-argument field provenance so a later
	// `return b` classifies the borrow (EscapingFieldOrigins reads the
	// sentinel key) — mirroring the live compile's recordStructReturnCallFieldFacts.
	recordStructReturnCallFieldFacts(ic, vd.Name, t, vd.Init)
}

// typeIsEscapeRelevantForInference reports whether a binding of type t can
// carry a tracked borrow that inference must thread: slices, fixed arrays,
// pointers, and struct values (whose fields may borrow). Scalar values and
// interface results are not escape-relevant in v1.
func typeIsEscapeRelevantForInference(c *Context, t ASTType) bool {
	rt := c.ResolveUnderlying(t)
	if rt.IsSlice() || rt.IsArray() || rt.Indirection > 0 {
		return true
	}
	if c.IsInterfaceType(rt) {
		return false
	}
	if _, ok := structDeclForType(c, rt); ok {
		return true
	}
	return false
}

// threadInferenceAssignment records the field-provenance facts of an
// assignment during the inference walk, mirroring the live Assignment
// handler's updateFieldPointerFactsForAssignment call. Without this a
// struct returned by value whose field was set by assignment
// (`b.buf = s; return b`) would under-record its alias — the
// CheckStructFieldEscape that classifyStructSymbol consults reads exactly
// these field-pointer facts. Only the provenance-recording side effect is
// reproduced; the assignment's mutability/owned checks belong to the live
// compile and are not re-run here.
func threadInferenceAssignment(ic *Context, n *Assignment) {
	if n == nil || n.Target == nil {
		return
	}
	targetSym, targetIsSymbol := n.Target.(*Symbol)
	if targetIsSymbol {
		dstt, ok := ic.DeclaredTypeForVar(targetSym.Name)
		if !ok {
			return
		}
		// Install the binding's OWN slice/pointer origin, mirroring the
		// VarDecl-init path in threadInferenceBinding and the live compile's
		// updatePointerFlowForAssignment. Without this a borrowed slice
		// *reassigned* into a returned local (`var t byte[]; t = a[0:1];
		// return t`) is origin-less during inference, `return t` classifies
		// as non-escaping, and ReturnAliases under-records — letting a local
		// escape uncaught through a call. Gate on escape-relevance (same as
		// the VarDecl path) so a non-escape-relevant RHS like `n = 1` is not
		// run through pointerExprForAST, which can throw on an expression the
		// cold walk has not established. Only the origin-installing side
		// effect is reproduced; the assignment's mutability/owned/coercion
		// checks belong to the live compile and are not re-run here.
		if typeIsEscapeRelevantForInference(ic, dstt) && !ic.IsGlobalBinding(targetSym.Name) {
			if dstt.Indirection > 0 && !dstt.HasOwned() {
				ic.SetBorrowedBinding(targetSym.Name, ic.IsBorrowedBinding(rootBindingName(n.Val)))
			}
			pexpr := pointerExprForAST(ic, n.Val, targetSym.Name)
			if pexpr.KnownOrigin || pexpr.KnownSlot {
				ic.PointerFlow().AssignPointer(flow.Binding(targetSym.Name), pexpr)
			}
		}
		// updateFieldPointerFactsForAssignment routes a struct-by-value *Funcall
		// RHS through recordStructReturnCallFieldFacts itself, so a `b = mk(arg)`
		// assignment records its borrowed-argument field provenance here too —
		// no separate inference call is needed.
		updateFieldPointerFactsForAssignment(ic, n.Target, true, targetSym, dstt, n.Val)
		return
	}
	// Non-Symbol target (Dot/Index chain). updateFieldPointerFactsForAssignment
	// only records facts for a chain rooted at a resolvable non-pointer
	// local binding; anything else (a deref `*p`, a global root, a
	// pointer-rooted path) is a no-op there. Gate on that same precondition
	// up front so we never call ASTType on a target whose type the cold
	// walk cannot resolve (e.g. `*p` where p is a still-inferred `<infer>`
	// binding), which would throw mid-inference.
	path, ok := ProvenancePathForExpr(n.Target)
	if !ok || path.Fields == "" || ic.IsGlobalBinding(path.Root) {
		return
	}
	rootType, ok := ic.TypeForVar(path.Root)
	if !ok || rootType.Indirection > 0 {
		return
	}
	dstt := n.Target.ASTType(ic)
	updateFieldPointerFactsForAssignment(ic, n.Target, false, nil, dstt, n.Val)
}

// rootBindingName walks an expression to its root symbol name, or "".
func rootBindingName(a AST) string {
	if name, ok := rootSymbolName(a); ok {
		return name
	}
	return ""
}

// classifyReturn classifies the escaping origins of a return statement's
// slot expressions and records/rejects per slot. A single-value return is
// slot 0; a multi-value return is a StructLiteral whose field _s is slot s.
func classifyReturn(ic *Context, f *FuncDecl, ret *Return, result [][]int) {
	if ret.Val == nil {
		return
	}
	// Multi-value return: the AST lowers `return e0, e1` to a
	// StructLiteral with positional fields _0, _1, ... whose value for
	// field _s is slot s's expression.
	if f.Return.MultiReturn {
		if sl, ok := ret.Val.(*StructLiteral); ok {
			for _, field := range sl.Fields {
				s := slotIndexFromFieldName(field.Name)
				if s < 0 || s >= len(result) {
					continue
				}
				// Direct return: rejectLocal=false. A local origin reaching
				// this slot directly is rejected by the precise per-site
				// check during live lowering, not here.
				classifySlotExpr(ic, f, field.Val, returnSlotType(f.Return, s), s, false, result)
			}
			return
		}
	}
	classifySlotExpr(ic, f, ret.Val, returnSlotType(f.Return, 0), 0, false, result)
}

// slotIndexFromFieldName parses the positional field name "_N" produced by
// multi-value return lowering into the slot index N.
func slotIndexFromFieldName(name string) int {
	if len(name) < 2 || name[0] != '_' {
		return -1
	}
	n := 0
	for _, ch := range name[1:] {
		if ch < '0' || ch > '9' {
			return -1
		}
		n = n*10 + int(ch-'0')
	}
	return n
}

// classifySlotExpr classifies the escaping origins of a single return-slot
// expression and records/rejects into result[slot]. Only escape-relevant
// slots (slice / pointer / aggregate-with-borrowable-fields) participate:
// a scalar slot (an i64 count) records nothing even if its expression
// roots at a parameter.
func classifySlotExpr(ic *Context, f *FuncDecl, expr AST, slotType ASTType, slot int, rejectLocal bool, result [][]int) {
	// A struct literal slot: walk its fields and classify any
	// slice/pointer field. This covers the constructor case
	// (`return Builder{buf: buf}`).
	if sl, ok := expr.(*StructLiteral); ok {
		classifyStructLiteral(ic, f, sl, slot, rejectLocal, result)
		return
	}
	// A bare struct-symbol returned by value whose fields carry borrowed
	// slices/pointers (CheckStructFieldEscape's domain).
	resolved := ic.ResolveUnderlying(slotType)
	if resolved.Indirection == 0 && !resolved.IsSlice() {
		if sym, ok := expr.(*Symbol); ok && !ic.IsGlobalBinding(sym.Name) {
			classifyStructSymbol(ic, f, sym, slot, rejectLocal, result)
			return
		}
		// A struct returned by value *from a call* (`return mk(loc[:])`)
		// must expand the callee's alias set onto the call's argument
		// origins, exactly as the slice/pointer slot path does. Without
		// this a local flowing into the struct's borrowed field through
		// the call escapes uncaught (the struct slot's classification
		// otherwise stops here). classifyCallExpansion runs each mapped
		// argument with rejectLocal=true, so a local through the call is a
		// hard error and a borrowed param is recorded as the struct
		// return's alias.
		if call, ok := expr.(*Funcall); ok {
			classifyCallExpansion(ic, f, call, slot, result)
		}
		return
	}
	// Slice / pointer slot: classify the expression's escaping origin.
	classifyExprOrigin(ic, f, expr, slot, rejectLocal, result)
}

// classifyExprOrigin classifies one slice/pointer-shaped return expression.
// Dual path: (1) pointerExprForAST's resolved origin, when known; (2) a
// root-symbol fallback for shapes pointerExprForAST cannot resolve (a Dot
// through a pointer receiver — `s.buf` — yields no field-provenance
// origin, but its root is a borrowed parameter). Both paths run the same
// classifyBinding switch, so a local root is rejected on either.
func classifyExprOrigin(ic *Context, f *FuncDecl, expr AST, slot int, rejectLocal bool, result [][]int) {
	// A call whose result borrows its arguments expands: classify the
	// matching argument expressions' origins. Arguments flowing into a
	// returned slot *through a call* must reject locals here — the per-site
	// check cannot see them (the call result may carry no single origin),
	// so aliasSet is the order-independent backstop.
	if call, ok := expr.(*Funcall); ok {
		classifyCallExpansion(ic, f, call, slot, result)
		return
	}
	ptr := pointerExprForAST(ic, expr, "")
	if ptr.KnownOrigin {
		// A branch-merge of two different borrowed params resolves to a
		// synthesized join origin; expand it so every contributing param is
		// recorded (JoinMembers returns the single origin unchanged for a
		// non-join origin, so the common case is one classification).
		for _, o := range ic.PointerFlow().JoinMembers(ptr.Origin) {
			applyClassification(ic, f, classifyBinding(ic, f, string(o), rejectLocal), expr, slot, result)
		}
		return
	}
	// Fallback: root-symbol classification for pointer-rooted field
	// access and other shapes pointerExprForAST leaves Unknown. This walks
	// SliceOp as well as Dot/Index/NonNullAssert, so `s.buf[0:s.pos]`
	// (a sub-slice of a borrowed pointer receiver's field) resolves to the
	// borrowed root `s` — the readProvenancePath path bails on a pointer
	// root, leaving no origin, and this fallback is what records the borrow
	// (critical for the interface-coercion guard's borrowing-method
	// detection on struct methods).
	if root, ok := aliasRootSymbol(expr); ok && !ic.IsGlobalBinding(root) {
		applyClassification(ic, f, classifyBinding(ic, f, root, rejectLocal), expr, slot, result)
	}
}

// aliasRootSymbol walks an expression's lvalue chain to its root symbol
// name, traversing SliceOp in addition to Dot/Index/NonNullAssert. Used by
// the inference fallback to attribute a borrowed root when pointerExprForAST
// leaves the expression Unknown (a sub-slice or field access through a
// pointer receiver).
func aliasRootSymbol(expr AST) (string, bool) {
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
		case *SliceOp:
			expr = v.Val
		case *OwnedPromotion:
			expr = v.Val
		default:
			return "", false
		}
	}
}

// classifyStructLiteral walks a struct literal's slice/pointer fields and
// classifies each. Nested struct literals recurse.
func classifyStructLiteral(ic *Context, f *FuncDecl, sl *StructLiteral, slot int, rejectLocal bool, result [][]int) {
	for _, field := range sl.Fields {
		if field.Val == nil {
			continue
		}
		if nested, ok := field.Val.(*StructLiteral); ok {
			classifyStructLiteral(ic, f, nested, slot, rejectLocal, result)
			continue
		}
		ft := field.Val.ASTType(ic)
		if ft.Indirection == 0 && !ft.IsSlice() {
			continue
		}
		classifyExprOrigin(ic, f, field.Val, slot, rejectLocal, result)
	}
}

// classifyStructSymbol handles returning a struct by value whose fields
// carry borrowed slices/pointers. CheckStructFieldEscape reports whether
// such a field exists; for inference we conservatively classify the
// binding's root: if the binding is a borrowed param, record it; if it
// roots a local, reject. v1 is coarse (no per-field precision), so a
// struct symbol with any escaping field aliases whatever its binding
// roots at.
func classifyStructSymbol(ic *Context, f *FuncDecl, sym *Symbol, slot int, rejectLocal bool, result [][]int) {
	// Each escaping field's *origin* is what matters — not the struct
	// binding itself (the binding `b` is a local struct whose field
	// `b.buf` borrows the parameter `s`; classifying `b` would wrongly see
	// a local). Classify every escape-restricted field origin: a borrowed
	// param field records its index; a local field is rejected (through a
	// call) or passed (direct return, left to the per-site check). v1 is
	// coarse — no per-field precision — but over-records soundly.
	for _, origin := range ic.PointerFlow().EscapingFieldOrigins(flow.Binding(sym.Name)) {
		// A branch-merged field whose two branches borrowed *different*
		// params resolves to a synthesized join origin; expand it so every
		// contributing param is recorded (JoinMembers returns a non-join
		// origin unchanged, so the common single-field case classifies
		// once). Without this expansion a join origin would have
		// paramIndexOf == -1 and record nothing — under-recording a
		// borrow, a use-after-free. This mirrors classifyExprOrigin's
		// JoinMembers expansion for the top-level slice/pointer path.
		for _, member := range ic.PointerFlow().JoinMembers(origin) {
			applyClassification(ic, f, classifyBinding(ic, f, string(member), rejectLocal), sym, slot, result)
		}
	}
}

// structReturnAliasFieldKey is the synthetic field-provenance key suffix
// used to record a struct-by-value call result's borrowed-argument origins
// onto its destination binding. The alias set is coarse (slot → param, no
// real field name), so we cannot reconstruct which struct field borrows;
// a single sentinel sub-key under the destination's "binding." prefix
// carries the merged origin. Both consumers — CheckStructFieldEscapeLocal
// (live per-site reject) and EscapingFieldOrigins (inference) — are
// prefix-scans over "binding.", so the sentinel participates uniformly.
const structReturnAliasFieldKey = "__callret"

// recordStructReturnCallFieldFacts populates the destination struct
// binding's field provenance from a struct-by-value call result.
//
// A struct returned BY VALUE from a call (`var b B = mk(arg)`, `b = mk(arg)`)
// carries no single Origin — its borrow lives in one of its fields, which
// pointerExprForAST's single-Origin call-expansion cannot represent. Without
// recording it, a local flowing into the returned struct's field through the
// call escapes uncaught: the direct form (`b.buf = loc[:]; return b`) is
// rejected by CheckStructFieldEscapeLocal reading b's field pointers, but the
// call-bound form left those pointers empty.
//
// This expands the callee's alias set for return slot 0 onto the call's
// argument origins (via pointerExprForAST), unions them through the same
// join-origin mechanism the branch merge uses, and records the result under
// the destination's sentinel field key. The call-bound case then becomes
// byte-identical to the direct assignment: a local arg yields a local field
// origin (live per-site reject), a borrowed arg yields a borrowed field
// origin (inference records it for the destination's own callers).
//
// Called from BOTH the live compile (struct-var-init / struct-assignment
// from a call) and the inference walk (threadInferenceBinding /
// threadInferenceAssignment), so the live reject and the inferred alias stay
// consistent.
func recordStructReturnCallFieldFacts(c *Context, destBinding string, destType ASTType, init AST) {
	call, ok := init.(*Funcall)
	if !ok {
		return
	}
	rt := c.ResolveUnderlying(destType)
	if rt.Indirection > 0 || rt.IsSlice() {
		return
	}
	if _, ok := structDeclForType(c, rt); !ok {
		return
	}
	callee, args := resolveCalleeForAlias(c, call)
	if callee == nil {
		return
	}
	aliases := aliasSet(c, callee)
	// A struct-by-value return occupies slot 0 (multi-return does not return
	// a single struct binding).
	if len(aliases) == 0 || len(aliases[0]) == 0 {
		return
	}
	key := fmt.Sprintf("%s.%s", destBinding, structReturnAliasFieldKey)
	// Clear any stale sentinel fact before recording — the destination is
	// being (re)initialized from this call.
	c.PointerFlow().SetPathPointer(key, c.PointerFlow().UnknownPointer())
	var merged flow.PointerExpr
	for _, p := range aliases[0] {
		if p < 0 || p >= len(args) {
			continue
		}
		ap := pointerExprForAST(c, args[p], "")
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
		// Union two distinct argument origins via a join origin, so a later
		// EscapingFieldOrigins expands every contributing param and a local
		// in either position still surfaces (JoinMembersFor / OriginKindOf
		// see each member). The join is itself escape-restricted, so the
		// most-restrictive member governs the live reject.
		merged = c.PointerFlow().JoinOrigins(merged, ap)
	}
	if merged.KnownOrigin {
		c.PointerFlow().SetPathPointer(key, merged)
	}
}

// classifyCallExpansion classifies `return g(args)` by expanding g's own
// alias set: for each param index p in alias_set(g)[slot], classify the
// matching argument expression arg_p's origin. This is what keeps a local
// reaching a returned slot *through a call* rejected.
func classifyCallExpansion(ic *Context, f *FuncDecl, call *Funcall, slot int, result [][]int) {
	callee, args := resolveCalleeForAlias(ic, call)
	if callee == nil {
		return
	}
	calleeAliases := aliasSet(ic, callee)
	if slot >= len(calleeAliases) {
		return
	}
	for _, p := range calleeAliases[slot] {
		if p < 0 || p >= len(args) {
			continue
		}
		// rejectLocal=true: a local reaching a returned slot through this
		// call is a hard error caught here (per-site checks cannot see it).
		classifyExprOrigin(ic, f, args[p], slot, true, result)
	}
}

// resolveCalleeForAlias resolves a Funcall to its callee FuncDecl and the
// positional argument list (desugaring a concrete method call so the
// receiver occupies argument index 0, matching the param-index convention).
// Returns (nil, nil) for casts, builtins, indirect calls, and interface
// dispatch — none of which carry a recordable alias set in v1.
func resolveCalleeForAlias(ic *Context, call *Funcall) (*FuncDecl, []AST) {
	pkg, fname := call.PkgAndName()
	// Builtins and casts carry no alias set.
	if pkg == "" && (fname == "alloc" || fname == "new" || fname == "free" || fname == "len") {
		return nil, nil
	}
	// A type-cast Funcall (T(expr)) carries no alias set. The call form
	// wins when a function of the same name also exists, so only treat it
	// as a cast when no matching function is declared.
	if _, ok := ic.TypeByName(call.QualifiedName()); ok {
		if _, _, hasFn := ic.FuncDeclForCall(pkg, fname); !hasFn {
			return nil, nil
		}
	}
	// Expression-receiver method call: (expr).method(args).
	if recv := call.ReceiverExpr(); recv != nil {
		rt := recv.ASTType(ic)
		if ic.IsInterfaceType(rt) {
			return nil, nil
		}
		if method, ok := ic.MethodForType(rt.Name, fname); ok {
			args := make([]AST, 0, 1+len(call.Args))
			args = append(args, recv)
			args = append(args, call.Args...)
			return method, args
		}
		return nil, nil
	}
	decl, _, ok := ic.FuncDeclForCall(pkg, fname)
	if ok {
		return decl, call.Args
	}
	// v.method(args) where v is a variable with a concrete method.
	if pkg != "" {
		if vt, vok := ic.TypeForVar(pkg); vok {
			if ic.IsInterfaceType(vt) {
				return nil, nil
			}
			if method, mok := ic.MethodForType(vt.Name, fname); mok {
				var recv AST
				if vt.Indirection > 0 || len(method.Args) == 0 || method.Args[0].Type.Indirection == 0 {
					recv = &Symbol{Name: pkg}
				} else {
					recv = &Address{Var: pkg}
				}
				args := make([]AST, 0, 1+len(call.Args))
				args = append(args, recv)
				args = append(args, call.Args...)
				return method, args
			}
		}
	}
	return nil, nil
}

// classifyBinding is the unified per-origin classifier. A local origin is
// a hard reject; a borrowed parameter is recorded; everything else passes.
// The IsBorrowedBinding OR is load-bearing: pointer parameters are borrowed
// only via the borrowed-binding flag (NewObject records no OriginKind), so
// OriginKindOf alone would miss them.
func classifyBinding(ic *Context, f *FuncDecl, originName string, rejectLocal bool) aliasClassification {
	kind := ic.PointerFlow().OriginKindOf(flow.Origin(originName))
	if kind == flow.OriginLocal {
		// A local origin reaching a returned slot directly (rejectLocal
		// false) is left to the precise per-site check, which has the
		// exact "pointer to local variable" / "slice into local-scope
		// storage" wording. Through a call/recursion arg (rejectLocal
		// true) the per-site check cannot see it, so aliasSet rejects.
		if rejectLocal {
			return aliasClassification{kind: aliasReject}
		}
		return aliasClassification{kind: aliasPass}
	}
	borrowed := kind == flow.OriginBorrowed || ic.IsBorrowedBinding(originName)
	if !borrowed {
		return aliasClassification{kind: aliasPass}
	}
	idx := paramIndexOf(f, originName)
	if idx < 0 {
		// Borrowed, but not a direct parameter (e.g. an intermediate
		// borrowed binding whose origin name differs). Resolve via the
		// origin: if the origin name *is* a param, record it; otherwise
		// it traces to a param transitively and the AssignPointer chain
		// already keyed it to the param's origin name, so a non-param
		// name here means we cannot attribute it — pass conservatively
		// is unsound, so treat as reject only if local (already handled).
		// In practice the origin name of a borrowed intermediate is the
		// param's name (AssignPointer copies the source Origin), so this
		// branch is not normally reached.
		return aliasClassification{kind: aliasPass}
	}
	// Variadic parameter is never aliasable in v1.
	if f.Variadic && idx == len(f.Args)-1 {
		return aliasClassification{kind: aliasReject, variadic: true}
	}
	return aliasClassification{kind: aliasRecord, paramIdx: idx}
}

type aliasClassification struct {
	kind     aliasClassKind
	paramIdx int
	variadic bool
}

// applyClassification records or rejects per the classification outcome.
func applyClassification(ic *Context, f *FuncDecl, cls aliasClassification, errNode AST, slot int, result [][]int) {
	switch cls.kind {
	case aliasReject:
		if cls.variadic {
			CompileErrorF(errNode, "Cannot return a view into variadic parameter; the packed args slice does not outlive the call")
		}
		// Local origin escape. Mirror the existing return-site messages.
		CompileErrorF(errNode, "Borrowed slice escapes through return")
	case aliasRecord:
		result[slot] = append(result[slot], cls.paramIdx)
	case aliasPass:
		// Not escape-restricted: record nothing.
	}
}

// rejectBorrowingMethodCoercion enforces the interface-coercion guard:
// a concrete type with ANY method whose inferred ReturnAliases is
// non-empty may not be coerced to ANY interface (including `any`). It
// demand-drives alias_set on each of the type's methods (not a possibly-
// empty cache) and, on finding a borrowing method, emits the directed
// diagnostic naming each offending method, its definition site, and the
// whole-type rule. No-op for a type with no methods or no borrowing
// method (the overwhelming common case), so ordinary types coerce
// unchanged.
func rejectBorrowingMethodCoercion(c *Context, errNode AST, concreteTypeName, ifaceName string) {
	if concreteTypeName == "" {
		return
	}
	methods, ok := c.TypeMethodsFor(concreteTypeName)
	if !ok || len(methods) == 0 {
		return
	}
	var offending []*FuncDecl
	for _, m := range methods {
		if m == nil {
			continue
		}
		aliases := aliasSet(c, m)
		if aliasSetNonEmpty(aliases) {
			offending = append(offending, m)
		}
	}
	if len(offending) == 0 {
		return
	}
	CompileErrorF(errNode, "%s", borrowingMethodCoercionError(c, concreteTypeName, ifaceName, offending))
}

// aliasSetNonEmpty reports whether any slot of an alias set lists a param.
func aliasSetNonEmpty(aliases [][]int) bool {
	for _, slot := range aliases {
		if len(slot) > 0 {
			return true
		}
	}
	return false
}

// borrowingMethodCoercionError renders the directed diagnostic for the
// interface-coercion guard. It names each borrowing method, points at its
// definition site (degrading to "defined in package X" for imported types
// whose rebuilt FuncDecl carries no position), describes what it borrows,
// and explains the whole-type "concrete-only" rule.
func borrowingMethodCoercionError(c *Context, concreteTypeName, ifaceName string, offending []*FuncDecl) string {
	var b strings.Builder
	fmt.Fprintf(&b, "cannot use %q as interface %q here\n", concreteTypeName, ifaceName)
	fmt.Fprintf(&b, "  %q is concrete-only: it has a method that returns a borrow, and a\n", concreteTypeName)
	b.WriteString("  borrow-returning method cannot be dispatched through an interface\n")
	b.WriteString("  (dispatch cannot track the borrow's lifetime). A type with any such\n")
	b.WriteString("  method cannot be coerced to any interface — not even `any`.\n\n")
	fmt.Fprintf(&b, "  borrow-returning method(s) on %q:\n", concreteTypeName)
	for _, m := range offending {
		fmt.Fprintf(&b, "    %s   — %s\n", methodSignatureRendering(c, concreteTypeName, m), borrowDescription(m))
		if site := methodDefinitionSite(concreteTypeName, m); site != "" {
			fmt.Fprintf(&b, "        %s\n", site)
		}
	}
	b.WriteString("\n  fix: call the borrowing method directly on a concrete value, or change\n")
	b.WriteString("       it so it does not return a borrow, to make the type interface-eligible.")
	return b.String()
}

// methodSignatureRendering renders a short method signature for the
// diagnostic, e.g. `bytes(b *Builder) byte[]`.
func methodSignatureRendering(c *Context, typeName string, m *FuncDecl) string {
	var b strings.Builder
	b.WriteString(bareMethodName(m.Name))
	b.WriteByte('(')
	for i, a := range m.Args {
		if i > 0 {
			b.WriteString(", ")
		}
		if a.Name != "" {
			b.WriteString(a.Name)
			b.WriteByte(' ')
		}
		b.WriteString(a.Type.String())
	}
	b.WriteByte(')')
	if !m.Return.Same(voidASTType()) {
		b.WriteByte(' ')
		b.WriteString(m.Return.String())
	}
	return b.String()
}

// bareMethodName strips a "Type." qualifier from a method name.
func bareMethodName(name string) string {
	if dot := strings.LastIndex(name, "."); dot >= 0 {
		return name[dot+1:]
	}
	return name
}

// borrowDescription renders a short phrase describing what a method
// borrows, derived from its inferred ReturnAliases. Index 0 is the
// receiver; index k>0 is the (k)-th declared parameter.
func borrowDescription(m *FuncDecl) string {
	borrowsReceiver := false
	var params []string
	for _, slot := range m.ReturnAliases {
		for _, p := range slot {
			if p == 0 {
				borrowsReceiver = true
			} else if p > 0 && p < len(m.Args) {
				params = append(params, m.Args[p].Name)
			}
		}
	}
	switch {
	case borrowsReceiver && len(params) > 0:
		return "returns a borrow of its receiver and parameter(s) " + strings.Join(params, ", ")
	case borrowsReceiver:
		return "returns a borrow of its receiver"
	case len(params) > 0:
		return "returns a borrow of parameter(s) " + strings.Join(params, ", ")
	default:
		return "returns a borrow"
	}
}

// methodDefinitionSite renders the method's definition site for the
// diagnostic. A locally-defined method's FuncDecl carries a usable
// position; an imported method may carry a position threaded from the
// .bo's SrcFile/SrcLine, otherwise it degrades to "defined in package X".
func methodDefinitionSite(concreteTypeName string, m *FuncDecl) string {
	p := m.Pos()
	if p.fname != "" && p.lineoff != 0 {
		return fmt.Sprintf("defined at %s:%d", p.fname, p.lineoff)
	}
	if dot := strings.Index(concreteTypeName, "."); dot >= 0 {
		return fmt.Sprintf("defined in package %s", concreteTypeName[:dot])
	}
	return ""
}

// sortDedup sorts and removes duplicate parameter indices.
func sortDedup(in []int) []int {
	if len(in) == 0 {
		return nil
	}
	sort.Ints(in)
	out := in[:1]
	for _, v := range in[1:] {
		if v != out[len(out)-1] {
			out = append(out, v)
		}
	}
	return out
}
