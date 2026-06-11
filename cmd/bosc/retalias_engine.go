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
// so cross-package lookups never walk a body.
//
// Cycles (direct or mutual recursion) are detected via the in-progress
// guard and resolved with a conservative self-alias (the recursive call's
// result is assumed to alias every non-variadic parameter). The full SCC
// fixpoint replaces this conservatism in the dependency-ordered driver.
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

	inProgress := c.aliasInProgressSet()
	if inProgress[f] {
		return conservativeSelfAlias(f)
	}
	inProgress[f] = true
	defer delete(inProgress, f)

	f.ReturnAliases = analyzeFunctionAliases(c, f)
	f.AliasesComputed = true
	return f.ReturnAliases
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
	defer func() {
		root.addressNames = savedAddrs
		root.anonGlobals = savedAnonGlobals
		root.anonGlobalCount = savedAnonCount
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

	// Reads 1+2 only apply to VIEW-shaped slots (slice/pointer): a scalar
	// return (`return b.val` where val is an i64) copies the value out —
	// no aliasing, nothing to record, even when the source is borrowed.
	rt := c.ResolveUnderlying(slotType)
	slotIsView := rt.Indirection > 0 || rt.IsSlice()

	if slotIsView {
		// Read 1: the expression's own tracked origin.
		ptr := pointerExprForAST(c, expr, "")
		if ptr.KnownOrigin {
			record(ptr.Origin)
		}

		// Read 2: pointer-rooted field/sub-slice views. readProvenancePath
		// leaves these opaque (a pointer root), but the view's backing is
		// whatever the root pointer borrows.
		if !ptr.KnownOrigin {
			if root, ok := lvalueRootSymbolName(expr); ok && !c.IsGlobalBinding(root) {
				if t, exists := c.TypeForVar(root); exists && t.Indirection > 0 && c.IsBorrowedBinding(root) {
					record(flow.Origin(root))
				}
			}
		}
	}

	// Read 3: aggregate values — union of field origins.
	if rt.Indirection == 0 && !rt.IsSlice() {
		if _, isStruct := structDeclForType(c, rt); isStruct {
			switch v := unwrapReturnExpr(expr).(type) {
			case *Symbol:
				if !c.IsGlobalBinding(v.Name) {
					for _, origin := range c.PointerFlow().EscapingFieldOrigins(flow.Binding(v.Name)) {
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
				for _, origin := range c.PointerFlow().EscapingFieldOrigins(flow.Binding(synth)) {
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

// paramIsVariadic reports whether param index idx is f's variadic
// parameter. Variadic params are never recordable aliases (their backing
// is the caller-frame args pack, a different lifetime than caller storage);
// the *Return case's escape checks reject a returned view of one.
func paramIsVariadic(f *FuncDecl, idx int) bool {
	return f.Variadic && idx == len(f.Args)-1
}
