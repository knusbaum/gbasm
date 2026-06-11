package main

import (
	"io"

	"github.com/knusbaum/gbasm/cmd/bosc/flow"
)

// retalias_engine.go — the return-alias summary engine.
//
// A function's summary (per return slot, the set of parameter indices the
// slot may alias) is computed by running the REAL compile —
// compileFunctionBody, the same code that does live codegen — over the
// function's body with output discarded, and reading the return value's
// provenance out of the one true flow tracker at each *Return. There is no
// parallel re-implementation of flow semantics: whatever the tracker says
// during a real compile is, by construction, what the summary says.
// (DESIGN_return_alias_engine.md is the authoritative design note.)
//
// The capture hook lives in compileTop's *Return case: when the current
// context carries an aliasCaptureState (installed by aliasSet for the
// analysis run; nil during real codegen), the return's slot expressions
// have their tracker provenance read and folded into the accumulator.

// aliasCaptureState accumulates per-slot borrowed-parameter indices during
// an analysis run of one function.
type aliasCaptureState struct {
	f      *FuncDecl
	result [][]int
}

// captureState walks the context chain to the nearest enclosing
// aliasCaptureState. Nil during real codegen.
func (c *Context) captureState() *aliasCaptureState {
	for cur := c; cur != nil; cur = cur.parent {
		if cur.aliasCapture != nil {
			return cur.aliasCapture
		}
	}
	return nil
}

// aliasSet returns f's inferred return-parameter alias set, computing it on
// first demand by running the real borrow analysis over f's body to a
// discard writer. Memoized via f.AliasesComputed. Imported FuncDecls arrive
// with AliasesComputed=true (the importer attaches the deserialized fact),
// so cross-package lookups never walk a body. (The cross-package import
// graph is a DAG, so cycles only occur within one compilation unit.)
//
// Cycles (direct or mutual recursion) are resolved by FIXPOINT, not
// conservatism. On re-entry of an in-progress function, the member's
// PROVISIONAL summary (∅-seeded) is returned and the consumption is
// counted (aliasTaint). A computation that consumed any provisional is
// cycle-tainted: its result is not memoized. The outermost tainted entry
// iterates — re-running its analysis with its provisional updated to the
// monotone union of iterations — until the union stops growing (the
// lattice is sets of param indices, finite and only growing, so this
// terminates). Only the outermost entry memoizes itself; the cycle is
// then broken for every other member (their next demand sees a memoized
// callee and computes precisely, no iteration needed).
//
// Worked example (DESIGN_return_alias_engine.md): mutual recursion
//   fn a(x *mut i64) *mut i64 { if (*x < 10) { return b(x) } return x }
//   fn b(x *mut i64) *mut i64 { *x = *x - 1; return a(x) }
// iter1: analyze(a): b consumes prov(a)=∅ → b=∅; a = {x} ∪ ∅ = {0}
// iter2: analyze(a): b consumes prov(a)={0} → b={0}; a = {0} ∪ {0} = {0}
// stable → a memoized [[0]]; b later computes [[0]] against memoized a.
func aliasSet(c *Context, f *FuncDecl) [][]int {
	if f == nil {
		return nil
	}
	if f.AliasesComputed {
		return f.ReturnAliases
	}
	// A bodyless decl (imported without a fact, forward decl) infers empty.
	if f.Body == nil {
		f.ReturnAliases = nil
		f.AliasesComputed = true
		return nil
	}

	root := c
	for root.parent != nil {
		root = root.parent
	}
	inProgress := c.aliasInProgressSet()
	if inProgress[f] {
		// Cycle re-entry: hand back the provisional and record the
		// dependency in every active analysis frame (each enclosing
		// activation's result now depends on f's not-yet-final value).
		for _, frame := range root.aliasDepStack {
			frame[f] = true
		}
		if prov, ok := root.aliasProvisional[f]; ok {
			return prov
		}
		empty := make([][]int, returnSlotCount(f.Return))
		return empty
	}
	inProgress[f] = true
	defer delete(inProgress, f)

	if root.aliasProvisional == nil {
		root.aliasProvisional = make(map[*FuncDecl][][]int)
	}

	for {
		// Fresh dependency frame per iteration: deps from a previous
		// iteration may have finalized since.
		frame := make(map[*FuncDecl]bool)
		root.aliasDepStack = append(root.aliasDepStack, frame)
		res := analyzeFunctionAliases(c, f)
		root.aliasDepStack = root.aliasDepStack[:len(root.aliasDepStack)-1]

		// A dependency matters only if it is on a function whose summary
		// is STILL provisional: f's own provisional (self-recursion /
		// f's cycle), or another in-progress function's. Dependencies on
		// since-finalized functions are resolved.
		selfDep := frame[f]
		othersPending := false
		for d := range frame {
			if d == f {
				continue
			}
			if !d.AliasesComputed {
				othersPending = true
			}
		}
		// Propagate still-pending deps (incl. f's own, which is pending
		// from an ANCESTOR's perspective until f memoizes) to the
		// enclosing frame so ancestors know what their result rests on.
		if len(root.aliasDepStack) > 0 {
			parent := root.aliasDepStack[len(root.aliasDepStack)-1]
			for d := range frame {
				if !d.AliasesComputed {
					parent[d] = true
				}
			}
		}

		if !selfDep && !othersPending {
			// The result rests only on finalized facts: it is final,
			// regardless of stack depth. (This is what keeps deep demand
			// chains linear — a non-cycle ancestor of a converged cycle
			// memoizes on its first pass.)
			delete(root.aliasProvisional, f)
			f.ReturnAliases = res
			f.AliasesComputed = true
			return res
		}

		// The result depends on a provisional. Fold this iteration into
		// f's monotone union.
		prev := root.aliasProvisional[f]
		next := unionAliases(prev, res)
		if !aliasesEqual(prev, next) {
			// Still growing: update the provisional and iterate (the
			// re-analysis re-runs the in-cycle subtree against the larger
			// provisional).
			root.aliasProvisional[f] = next
			continue
		}

		// Union stable. If the only pending dependency is f ITSELF, the
		// cycle through f has converged: memoize at any depth. If another
		// in-progress function's provisional was consumed, f sits inside
		// an ANCESTOR's cycle — leave the provisional for the ancestor's
		// iteration and return without memoizing.
		if !othersPending {
			delete(root.aliasProvisional, f)
			f.ReturnAliases = next
			f.AliasesComputed = true
			return next
		}
		return res
	}
}

// unionAliases returns the per-slot union of two alias sets (sorted,
// deduplicated). Slot counts are expected to match; the longer wins.
func unionAliases(a, b [][]int) [][]int {
	n := len(a)
	if len(b) > n {
		n = len(b)
	}
	out := make([][]int, n)
	for s := 0; s < n; s++ {
		var merged []int
		if s < len(a) {
			merged = append(merged, a[s]...)
		}
		if s < len(b) {
			merged = append(merged, b[s]...)
		}
		out[s] = sortDedup(merged)
	}
	return out
}

// aliasesEqual reports per-slot equality of two (sorted) alias sets.
func aliasesEqual(a, b [][]int) bool {
	if len(a) != len(b) {
		return false
	}
	for s := range a {
		if len(a[s]) != len(b[s]) {
			return false
		}
		for i := range a[s] {
			if a[s][i] != b[s][i] {
				return false
			}
		}
	}
	return true
}

// analyzeFunctionAliases runs the real compile of f's body to a discard
// writer in an isolated flow state and returns the captured summary.
func analyzeFunctionAliases(c *Context, f *FuncDecl) [][]int {
	capture := &aliasCaptureState{
		f:      f,
		result: make([][]int, returnSlotCount(f.Return)),
	}

	// Isolate the analysis run completely from the live compile:
	//  - flow state: clone-save the live state, install a fresh one,
	//    restore after. (bosc aborts on the first CompileErrorF, so no
	//    defer-restore is needed on the error path — fail-fast is correct:
	//    an invalid dependency's error IS the compile's error.)
	//  - lexical scope: root the analysis at the file-scope root context so
	//    the analyzed body sees every declaration but no enclosing
	//    function's locals (BindVar shadow checks would fire on same-named
	//    params otherwise).
	//  - root-resident emission state: MarkAddress writes addressNames on
	//    the ROOT — if the analysis run's `&x` marks leaked, the real
	//    codegen of the same function would see NameIsAddress(x) already
	//    true and SKIP emitting the `volatile x` directive, producing
	//    silently wrong code (bas keeps x cached in a register across the
	//    pointer write). AddAnonGlobal blind-appends to a root queue — an
	//    analysis run would enqueue unreferenced duplicate __static_N
	//    globals. Snapshot and restore both. (String/StringSliceHeader
	//    interning is content-keyed and idempotent — safe to leak.)
	saved := c.PointerFlow().Clone()
	c.RestorePointerFlow(flow.NewState())
	defer c.RestorePointerFlow(saved)

	root := c
	for root.parent != nil {
		root = root.parent
	}
	savedAddrs := make(map[string]bool, len(root.addressNames))
	for k, v := range root.addressNames {
		savedAddrs[k] = v
	}
	savedAnonGlobals := root.anonGlobals
	savedAnonCount := root.anonGlobalCount
	// Root checker facts: Set{NullFact,OwnedFieldConsumed,Borrowed,Moved}
	// write into the DECLARING context's checker — for a global binding,
	// the root. An analysis run that narrows a global's nullability (or
	// consumes/moves it) must not leak that fact into the live compile:
	// the caller would then deref the nullable global unchecked.
	savedNull := cloneStringMap(root.checker.nullFacts)
	savedOwnedFields := cloneBoolMap(root.checker.ownedFieldFacts)
	savedBorrowed := cloneBoolMap(root.checker.borrowedBindings)
	savedMoved := cloneBoolMap(root.checker.movedBindings)
	defer func() {
		root.addressNames = savedAddrs
		root.anonGlobals = savedAnonGlobals
		root.anonGlobalCount = savedAnonCount
		root.checker.nullFacts = savedNull
		root.checker.ownedFieldFacts = savedOwnedFields
		root.checker.borrowedBindings = savedBorrowed
		root.checker.movedBindings = savedMoved
	}()
	fc := root.SubContext()
	fc.aliasCapture = capture
	retlab := fc.PushRetlabel(f.Return)
	defer fc.PopRetlabel()
	defer fc.ForgetPointerBindings()

	// The real thing: same param setup, same body lowering, same tracker
	// transitions as live codegen — output thrown away.
	compileFunctionBody(io.Discard, fc, f, retlab)

	for s := range capture.result {
		capture.result[s] = sortDedup(capture.result[s])
	}
	return capture.result
}

// captureReturnAliases is the *Return-case hook: during an analysis run it
// reads each returned slot expression's provenance from the live tracker
// and records the borrowed-parameter indices it reaches. During real
// codegen (no capture state) it is a no-op.
//
// Multi-value `return e0, e1` arrives as a StructLiteral with positional
// fields _0, _1, ... — slot s's expression is field _s's value. Slot
// indexing is Return.AnonFields[s] per the design.
func captureReturnAliases(c *Context, ret *Return) {
	cap := c.captureState()
	if cap == nil || ret.Val == nil {
		return
	}
	if cap.f.Return.MultiReturn {
		if sl, ok := ret.Val.(*StructLiteral); ok {
			for _, field := range sl.Fields {
				s := slotIndexFromFieldName(field.Name)
				if s < 0 || s >= len(cap.result) {
					continue
				}
				cap.result[s] = append(cap.result[s],
					returnExprParamAliases(c, cap.f, field.Val, returnSlotType(cap.f.Return, s))...)
			}
			return
		}
	}
	if len(cap.result) > 0 {
		cap.result[0] = append(cap.result[0],
			returnExprParamAliases(c, cap.f, ret.Val, returnSlotType(cap.f.Return, 0))...)
	}
}

// returnExprParamAliases reads one returned expression's provenance from
// the tracker and maps every borrowed origin it reaches back to a
// parameter index of f. This is a QUERY of tracker state — by the time a
// *Return lowers, the real compile has already threaded every
// binding/assignment/merge into the flow state, so the expression's
// provenance is resolved by reading, not re-deriving.
//
// The reads, covering every value shape:
//  1. the expression's own origin (pointerExprForAST + join expansion):
//     slices, pointers, call results with a tracked root;
//  2. lvalue-root fallback for field/sub-slice reads through a borrowed
//     POINTER (`s.buf`, `s.buf[0:n]` where s is a pointer receiver):
//     readProvenancePath deliberately leaves pointer-rooted paths opaque,
//     but the view borrows whatever the root pointer borrows — the root
//     binding's borrowed-ness is tracker state (IsBorrowedBinding);
//  3. for an aggregate value (struct symbol / literal / call result):
//     `return expr` is an assignment to the return slot, so run the SAME
//     tracker transition the assignment path runs — against a synthetic
//     return binding — and read the union of its field origins back
//     (EscapingFieldOrigins + join expansion).
//
// Borrowed origins map to param indices; a variadic-param view is a hard
// error; locals are rejected by the Return case's own escape checks;
// globals/heap/unknown record nothing.
func returnExprParamAliases(c *Context, f *FuncDecl, expr AST, slotType ASTType) []int {
	var out []int
	record := func(origin flow.Origin) {
		for _, member := range c.PointerFlow().JoinMembers(origin) {
			name := string(member)
			kind := c.PointerFlow().OriginKindOf(member)
			if kind == flow.OriginLocal {
				// A local origin reaching a returned slot HERE means it got
				// here through a call (`return mk(loc[:])`) — the precise
				// per-site checks ran first (capture is ordered after them in
				// the *Return case) and cannot see through a call boundary.
				// The summary engine is the order-independent backstop.
				CompileErrorF(expr, "Borrowed slice escapes through return")
			}
			idx := paramIndexOf(f, name)
			if idx < 0 {
				// Not a param (global/heap/intermediate): records nothing.
				continue
			}
			borrowed := kind == flow.OriginBorrowed || c.IsBorrowedBinding(name)
			if !borrowed {
				continue
			}
			if paramIsVariadic(f, idx) {
				// A view into the variadic pack must not escape: its
				// backing is the caller-frame args slice, whose lifetime
				// is the call — not recordable as a caller-storage alias.
				CompileErrorF(expr, "Cannot return a view into variadic parameter %q; the packed args slice does not outlive the call", name)
			}
			out = append(out, idx)
		}
	}

	// Reads 1+2 only apply to VIEW-shaped slots: a scalar return
	// (`return b.val` where val is an i64) copies the value out — no
	// aliasing, nothing to record, even when the source is borrowed.
	// Slices, pointers, AND interfaces are views: an interface value
	// wrapping a borrowed pointer (`return b` into a Getter slot)
	// aliases whatever the pointer borrows — the fat pointer's data word
	// IS the borrowed pointer.
	rt := c.ResolveUnderlying(slotType)
	slotIsView := rt.Indirection > 0 || rt.IsSlice() || c.IsInterfaceType(rt)

	if slotIsView {
		// Read 1: the expression's own tracked origin.
		ptr := pointerExprForAST(c, expr, "")
		if ptr.KnownOrigin {
			record(ptr.Origin)
		}

		// Read 2: rooted field/sub-slice views whose precise path fact is
		// unknown.
		//  - Pointer root: readProvenancePath leaves these opaque, but the
		//    view's backing is whatever the root pointer borrows.
		//  - Struct-binding root: the precise field fact may be missing
		//    (e.g. the binding was initialized from a call, whose borrow
		//    lives on the coarse __callret sentinel, not the named field).
		//    Conservatively the view borrows the union of the binding's
		//    escaping field origins — sound (over-records); a local field
		//    origin surfaces and rejects.
		// Slice/pointer slots ONLY: an interface value READ OUT of a field
		// is a copy of a fat pointer, not a view into the root — its data
		// word's provenance was recorded at its own coercion site. Treating
		// it as borrowing the root over-rejects (e.g. Builder.error()
		// returning the err_ field). Interface slots still get read 1: a
		// borrowed pointer COERCED to an interface at the return is the
		// expression's own tracked origin.
		if !ptr.KnownOrigin && (rt.Indirection > 0 || rt.IsSlice()) {
			if root, ok := lvalueRootSymbolName(expr); ok && !c.IsGlobalBinding(root) {
				if t, exists := c.TypeForVar(root); exists {
					if t.Indirection > 0 && c.IsBorrowedBinding(root) {
						record(flow.Origin(root))
					} else if t.Indirection == 0 && !t.IsSlice() {
						if _, isStruct := structDeclForType(c, t); isStruct {
							for _, origin := range c.PointerFlow().FieldOrigins(flow.Binding(root)) {
								record(origin)
							}
						}
					}
				}
			}
		}
	}

	// Read 3: aggregate values — union of field origins.
	if rt.Indirection == 0 && !rt.IsSlice() {
		if _, isStruct := structDeclForType(c, rt); isStruct {
			switch v := unwrapReturnExpr(expr).(type) {
			case *Deref:
				// `return *p` — the returned struct COPY's slice/pointer
				// fields still point at whatever p's pointee's fields point
				// at. Field-level provenance behind a pointer is not
				// tracked, so conservatively the copy borrows p itself: the
				// caller must keep p's referent alive as long as the copy's
				// views are used. Sound (over-records); precise per-field
				// through-pointer provenance is a documented tracker gap.
				if dp := pointerExprForAST(c, v.Val, ""); dp.KnownOrigin {
					record(dp.Origin)
				} else if root, ok := lvalueRootSymbolName(v.Val); ok && !c.IsGlobalBinding(root) {
					if t, exists := c.TypeForVar(root); exists && t.Indirection > 0 && c.IsBorrowedBinding(root) {
						record(flow.Origin(root))
					}
				}
			case *Symbol:
				if !c.IsGlobalBinding(v.Name) {
					for _, origin := range c.PointerFlow().FieldOrigins(flow.Binding(v.Name)) {
						record(origin)
					}
				}
			case *StructLiteral, *Funcall:
				// `return <literal>` / `return mk(...)` assigns the value to
				// the return slot: run the same transition an assignment
				// runs, against a synthetic binding, then read it back.
				const synth = "__retval"
				c.PointerFlow().ForgetFieldPointers(flow.Binding(synth))
				if sl, ok := v.(*StructLiteral); ok {
					recordStructLiteralFieldFacts(c, VarFlowPath(synth), sl)
				} else {
					recordStructCallResultAtPath(c, synth, slotType, v.(*Funcall))
				}
				for _, origin := range c.PointerFlow().FieldOrigins(flow.Binding(synth)) {
					record(origin)
				}
				c.PointerFlow().ForgetFieldPointers(flow.Binding(synth))
			}
		}
	}
	return out
}

// unwrapReturnExpr strips OwnedPromotion/NonNullAssert wrappers from a
// returned expression.
func unwrapReturnExpr(expr AST) AST {
	for {
		switch v := expr.(type) {
		case *OwnedPromotion:
			expr = v.Val
		case *NonNullAssert:
			expr = v.Val
		default:
			return expr
		}
	}
}

// lvalueRootSymbolName walks an expression's lvalue chain (Dot, Index,
// SliceOp, NonNullAssert, OwnedPromotion) to its root symbol name. Used by
// the pointer-rooted-view fallback: `s.buf` / `s.buf[0:n]` root at `s`.
func lvalueRootSymbolName(expr AST) (string, bool) {
	for {
		switch v := expr.(type) {
		case *Symbol:
			return v.Name, true
		case *Dot:
			expr = v.Val
		case *Index:
			expr = v.Val
		case *SliceOp:
			expr = v.Val
		case *NonNullAssert:
			expr = v.Val
		case *OwnedPromotion:
			expr = v.Val
		default:
			return "", false
		}
	}
}

// cloneStringMap copies a map[string]NullState.
func cloneStringMap(m map[string]NullState) map[string]NullState {
	out := make(map[string]NullState, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// cloneBoolMap copies a map[string]bool.
func cloneBoolMap(m map[string]bool) map[string]bool {
	out := make(map[string]bool, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// paramIsVariadic reports whether param index idx is f's variadic
// parameter. Variadic params are never recordable aliases (their backing
// is the caller-frame args pack, a different lifetime than caller storage);
// the *Return case's escape checks reject a returned view of one.
func paramIsVariadic(f *FuncDecl, idx int) bool {
	return f.Variadic && idx == len(f.Args)-1
}
