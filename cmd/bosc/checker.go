package main

import (
	"strings"

	"github.com/knusbaum/gbasm/cmd/bosc/flow"
)

type NullState int

const (
	NullMaybe NullState = iota
	NullKnownNull
	NullKnownNonNull
)

type FlowSnapshot struct {
	Owned       map[string]bool
	Null        map[FlowPath]NullState
	OwnedFields map[FlowPath]bool
	Borrowed    map[string]bool
	Pointer     *flow.State
}

type FlowPath struct {
	Root   string
	Fields string
}

func VarFlowPath(name string) FlowPath {
	return FlowPath{Root: name}
}

func (p FlowPath) Key() string {
	if p.Fields == "" {
		return p.Root
	}
	return p.Root + "." + p.Fields
}

func (p FlowPath) ParentRoot() FlowPath {
	return FlowPath{Root: p.Root}
}

func (p FlowPath) Append(field string) FlowPath {
	if p.Fields == "" {
		return FlowPath{Root: p.Root, Fields: field}
	}
	return FlowPath{Root: p.Root, Fields: p.Fields + "." + field}
}

func FlowPathFromKey(key string) FlowPath {
	parts := strings.Split(key, ".")
	if len(parts) == 0 {
		return FlowPath{}
	}
	return FlowPath{Root: parts[0], Fields: strings.Join(parts[1:], ".")}
}

func FlowPathForExpr(a AST) (FlowPath, bool) {
	switch ast := a.(type) {
	case *Symbol:
		return VarFlowPath(ast.Name), true
	case *Dot:
		base, ok := FlowPathForExpr(ast.Val)
		if !ok {
			return FlowPath{}, false
		}
		return base.Append(ast.Member), true
	case *NonNullAssert:
		return FlowPathForExpr(ast.Val)
	default:
		return FlowPath{}, false
	}
}

// ProvenancePathForExpr is the slice-provenance counterpart of
// FlowPathForExpr. It mirrors the Dot/NonNullAssert recursion but adds
// an Index→"[]" arm: all array (or slice) elements share a single
// aggregate bucket, sound for escape (over-rejects) but explicitly
// unsound for null tracking (would over-narrow). The two builders must
// not be merged — null/owned tracking has the opposite over-
// approximation requirement.
func ProvenancePathForExpr(a AST) (FlowPath, bool) {
	switch ast := a.(type) {
	case *Symbol:
		return VarFlowPath(ast.Name), true
	case *Dot:
		base, ok := ProvenancePathForExpr(ast.Val)
		if !ok {
			return FlowPath{}, false
		}
		return base.Append(ast.Member), true
	case *Index:
		base, ok := ProvenancePathForExpr(ast.Val)
		if !ok {
			return FlowPath{}, false
		}
		return base.Append("[]"), true
	case *NonNullAssert:
		return ProvenancePathForExpr(ast.Val)
	default:
		return FlowPath{}, false
	}
}

// CheckerState holds flow-sensitive semantic state. Context owns names, types,
// codegen labels, temporaries, and emitted data; checker state owns facts about
// those names during checking and compilation.
type CheckerState struct {
	parent *CheckerState

	nullFacts        map[string]NullState
	ownedFieldFacts  map[string]bool
	borrowedBindings map[string]bool
	movedBindings    map[string]bool

	returnTypes    []ASTType
	continueStates [][]FlowSnapshot
	breakStates    [][]FlowSnapshot

	// pointerFlow is owned by the root checker state and shared through the
	// checker parent chain, matching the function-wide nature of pointer facts.
	pointerFlow *flow.State
}

func NewCheckerState(parent *CheckerState) *CheckerState {
	return &CheckerState{
		parent:             parent,
		nullFacts:        make(map[string]NullState),
		ownedFieldFacts:  make(map[string]bool),
		borrowedBindings: make(map[string]bool),
		movedBindings:    make(map[string]bool),
	}
}

func (s *CheckerState) root() *CheckerState {
	if s == nil {
		return nil
	}
	for s.parent != nil {
		s = s.parent
	}
	return s
}

func (s *CheckerState) PushReturnType(t ASTType) {
	root := s.root()
	root.returnTypes = append(root.returnTypes, t)
}

func (s *CheckerState) PopReturnType() {
	root := s.root()
	root.returnTypes = root.returnTypes[:len(root.returnTypes)-1]
}

func (s *CheckerState) ReturnType() ASTType {
	root := s.root()
	if root == nil || len(root.returnTypes) == 0 {
		return voidASTType()
	}
	return root.returnTypes[len(root.returnTypes)-1]
}

func (s *CheckerState) PushContinueFrame() {
	root := s.root()
	root.continueStates = append(root.continueStates, nil)
}

func (s *CheckerState) PopContinueFrame() {
	root := s.root()
	root.continueStates = root.continueStates[:len(root.continueStates)-1]
}

func (s *CheckerState) RecordContinue(snap FlowSnapshot) {
	root := s.root()
	if root == nil || len(root.continueStates) == 0 {
		return
	}
	i := len(root.continueStates) - 1
	root.continueStates[i] = append(root.continueStates[i], snap)
}

func (s *CheckerState) ContinueStates() []FlowSnapshot {
	root := s.root()
	if root == nil || len(root.continueStates) == 0 {
		return nil
	}
	return append([]FlowSnapshot(nil), root.continueStates[len(root.continueStates)-1]...)
}

func (s *CheckerState) PushBreakFrame() {
	root := s.root()
	root.breakStates = append(root.breakStates, nil)
}

func (s *CheckerState) PopBreakFrame() {
	root := s.root()
	root.breakStates = root.breakStates[:len(root.breakStates)-1]
}

func (s *CheckerState) RecordBreak(snap FlowSnapshot) {
	root := s.root()
	if root == nil || len(root.breakStates) == 0 {
		return
	}
	i := len(root.breakStates) - 1
	root.breakStates[i] = append(root.breakStates[i], snap)
}

func (s *CheckerState) BreakStates() []FlowSnapshot {
	root := s.root()
	if root == nil || len(root.breakStates) == 0 {
		return nil
	}
	return append([]FlowSnapshot(nil), root.breakStates[len(root.breakStates)-1]...)
}

func (c *Context) SetBorrowedBinding(name string, borrowed bool) {
	if ctx := c.BindingContext(name); ctx != nil {
		if borrowed {
			ctx.checker.borrowedBindings[name] = true
		} else {
			delete(ctx.checker.borrowedBindings, name)
		}
	}
}

func (c *Context) IsBorrowedBinding(name string) bool {
	if ctx := c.BindingContext(name); ctx != nil {
		return ctx.checker.borrowedBindings[name]
	}
	return false
}

func (c *Context) PointerFlow() *flow.State {
	if c == nil {
		return flow.NewState()
	}
	if c.parent != nil {
		return c.parent.PointerFlow()
	}
	if c.checker == nil {
		c.checker = NewCheckerState(nil)
	}
	if c.checker.pointerFlow == nil {
		c.checker.pointerFlow = flow.NewState()
	}
	return c.checker.pointerFlow
}

func (c *Context) RestorePointerFlow(s *flow.State) {
	if c.parent != nil {
		c.parent.RestorePointerFlow(s)
		return
	}
	if c.checker == nil {
		c.checker = NewCheckerState(nil)
	}
	c.checker.pointerFlow = s.Clone()
}

func (c *Context) ForgetPointerBindings() {
	for name, t := range c.bindings {
		if t.Indirection > 0 || t.IsSlice() {
			c.PointerFlow().Forget(flow.Binding(name))
		}
	}
}

// InvalidateLocalOriginsForScope marks TargetMoved for every OriginLocal
// registered for a non-pointer binding declared in this context level.
// Must be called before ForgetPointerBindings so outer-scope aliases see
// the invalidation.
func (c *Context) InvalidateLocalOriginsForScope() {
	pf := c.PointerFlow()
	for name, t := range c.bindings {
		if t.Indirection == 0 {
			o := flow.Origin(name)
			if pf.OriginKindOf(o) == flow.OriginLocal {
				pf.InvalidateOrigin(o, flow.TargetMoved)
			}
		}
	}
}

// MoveConsume marks an owned binding as consumed *and* invalidates the
// pointer-flow Origin associated with the binding's storage. For pointer
// bindings the pointed-at allocation is treated as freed (TargetDead);
// for value bindings the binding's own storage is treated as moved
// (TargetMoved). Use this when the move actually destroys the obligation
// — `dispose(p)`, passing a value-typed owned to a consuming parameter,
// branch-narrowing to a known-nil owned pointer, an uninitialized owned
// declaration. Any alias pointing at the invalidated Origin (borrowed
// pointer, value-alias of an owned scalar) is now stale and the next
// read of it through `CheckDerefValidity` will be rejected.
func (c *Context) MoveConsume(name string) {
	if c == nil {
		return
	}
	if t, ok := c.bindings[name]; ok {
		c.checker.movedBindings[name] = true
		pf := c.PointerFlow()
		if t.Indirection > 0 {
			ptrExpr := pf.Pointer(flow.Binding(name))
			if ptrExpr.KnownOrigin {
				pf.InvalidateOrigin(ptrExpr.Origin, flow.TargetDead)
			}
		} else {
			pf.InvalidateOrigin(flow.Origin(name), flow.TargetMoved)
		}
		return
	}
	c.parent.MoveConsume(name)
}

// MoveTransfer marks an owned binding as consumed without touching the
// pointer-flow Origin. Use this when the move hands the obligation to
// another binding that references the same storage — `var y *owned T =
// &x` transfers x's storage to y, `var q owned *T = p` transfers p's
// allocation to q. The source binding can no longer be used, but the
// storage is still live (the new owner references it) and aliases of
// that storage stay usable until the new owner is consumed.
func (c *Context) MoveTransfer(name string) {
	if c == nil {
		return
	}
	if _, ok := c.bindings[name]; ok {
		c.checker.movedBindings[name] = true
		return
	}
	c.parent.MoveTransfer(name)
}

// Unmove clears the consumed flag on a var binding after re-assignment.
func (c *Context) Unmove(name string) {
	if c == nil {
		return
	}
	if _, ok := c.bindings[name]; ok {
		c.checker.movedBindings[name] = false
		return
	}
	c.parent.Unmove(name)
}

// IsMoved reports whether an owned binding has been consumed.
func (c *Context) IsMoved(name string) bool {
	if c == nil {
		return false
	}
	if _, ok := c.bindings[name]; ok {
		return c.checker.movedBindings[name]
	}
	return c.parent.IsMoved(name)
}


// OwnedBindingsSnapshot returns a map of name->moved-state for all owned
// bindings visible in this context and its parents. Used for branch analysis.
func (c *Context) OwnedBindingsSnapshot() map[string]bool {
	snap := make(map[string]bool)
	for ctx := c; ctx != nil; ctx = ctx.parent {
		for name, t := range ctx.bindings {
			if t.HasOwned() {
				if _, exists := snap[name]; !exists {
					snap[name] = ctx.checker.movedBindings[name]
				}
			}
		}
	}
	return snap
}

// RestoreOwnedBindings restores move state from a snapshot, for each owned
// binding named in the snapshot that still exists in the context chain.
func (c *Context) RestoreOwnedBindings(snap map[string]bool) {
	for name, moved := range snap {
		for ctx := c; ctx != nil; ctx = ctx.parent {
			if _, ok := ctx.bindings[name]; ok {
				ctx.checker.movedBindings[name] = moved
				break
			}
		}
	}
}

func (c *Context) NullFactsSnapshot() map[FlowPath]NullState {
	snap := make(map[FlowPath]NullState)
	for ctx := c; ctx != nil; ctx = ctx.parent {
		for key, state := range ctx.checker.nullFacts {
			path := FlowPathFromKey(key)
			if _, exists := snap[path]; !exists {
				snap[path] = state
			}
		}
	}
	return snap
}

func (c *Context) OwnedFieldFactsSnapshot() map[FlowPath]bool {
	snap := make(map[FlowPath]bool)
	for ctx := c; ctx != nil; ctx = ctx.parent {
		for key, consumed := range ctx.checker.ownedFieldFacts {
			path := FlowPathFromKey(key)
			if _, exists := snap[path]; !exists {
				snap[path] = consumed
			}
		}
	}
	return snap
}

func (c *Context) BorrowedBindingsSnapshot() map[string]bool {
	snap := make(map[string]bool)
	for ctx := c; ctx != nil; ctx = ctx.parent {
		for name, borrowed := range ctx.checker.borrowedBindings {
			if _, exists := snap[name]; !exists {
				snap[name] = borrowed
			}
		}
	}
	return snap
}

func (c *Context) RestoreBorrowedBindings(snap map[string]bool) {
	for ctx := c; ctx != nil; ctx = ctx.parent {
		for name := range ctx.checker.borrowedBindings {
			delete(ctx.checker.borrowedBindings, name)
		}
	}
	for name, borrowed := range snap {
		c.SetBorrowedBinding(name, borrowed)
	}
}

func (c *Context) RestoreOwnedFieldFacts(snap map[FlowPath]bool) {
	for ctx := c; ctx != nil; ctx = ctx.parent {
		for name := range ctx.checker.ownedFieldFacts {
			delete(ctx.checker.ownedFieldFacts, name)
		}
	}
	for path, consumed := range snap {
		c.SetOwnedFieldConsumed(path, consumed)
	}
}

func (c *Context) RestoreNullFacts(snap map[FlowPath]NullState) {
	for ctx := c; ctx != nil; ctx = ctx.parent {
		for name := range ctx.checker.nullFacts {
			delete(ctx.checker.nullFacts, name)
		}
	}
	for path, state := range snap {
		c.SetNullFact(path, state)
	}
}

func (c *Context) FlowSnapshot() FlowSnapshot {
	return FlowSnapshot{
		Owned:       c.OwnedBindingsSnapshot(),
		Null:        c.NullFactsSnapshot(),
		OwnedFields: c.OwnedFieldFactsSnapshot(),
		Borrowed:    c.BorrowedBindingsSnapshot(),
		Pointer:     c.PointerFlow().Clone(),
	}
}

func (c *Context) RestoreFlowSnapshot(snap FlowSnapshot) {
	c.RestoreOwnedBindings(snap.Owned)
	c.RestoreNullFacts(snap.Null)
	c.RestoreOwnedFieldFacts(snap.OwnedFields)
	c.RestoreBorrowedBindings(snap.Borrowed)
	c.RestorePointerFlow(snap.Pointer)
}

func mergeNullState(a NullState, aok bool, b NullState, bok bool) (NullState, bool) {
	if !aok {
		a = NullMaybe
	}
	if !bok {
		b = NullMaybe
	}
	if a == b && a != NullMaybe {
		return a, true
	}
	return NullMaybe, false
}

func MergeNullFacts(a, b map[FlowPath]NullState) map[FlowPath]NullState {
	out := make(map[FlowPath]NullState)
	seen := make(map[FlowPath]bool)
	for path := range a {
		seen[path] = true
	}
	for path := range b {
		seen[path] = true
	}
	for path := range seen {
		state, ok := mergeNullState(a[path], a[path] != 0, b[path], b[path] != 0)
		if ok {
			out[path] = state
		}
	}
	return out
}

func MergeBorrowedBindings(a, b map[string]bool) map[string]bool {
	out := make(map[string]bool)
	for name, borrowed := range a {
		if borrowed {
			out[name] = true
		}
	}
	for name, borrowed := range b {
		if borrowed {
			out[name] = true
		}
	}
	return out
}

func (c *Context) SetNullFact(path FlowPath, state NullState) {
	for ctx := c; ctx != nil; ctx = ctx.parent {
		if _, ok := ctx.bindings[path.Root]; ok {
			key := path.Key()
			if state == NullMaybe {
				delete(ctx.checker.nullFacts, key)
			} else {
				ctx.checker.nullFacts[key] = state
			}
			return
		}
	}
}

func (c *Context) SetOwnedFieldConsumed(path FlowPath, consumed bool) {
	for ctx := c; ctx != nil; ctx = ctx.parent {
		if _, ok := ctx.bindings[path.Root]; ok {
			key := path.Key()
			if consumed {
				ctx.checker.ownedFieldFacts[key] = true
			} else {
				delete(ctx.checker.ownedFieldFacts, key)
			}
			return
		}
	}
}

func (c *Context) OwnedFieldConsumed(path FlowPath) bool {
	for ctx := c; ctx != nil; ctx = ctx.parent {
		if consumed, ok := ctx.checker.ownedFieldFacts[path.Key()]; ok {
			return consumed
		}
		if _, ok := ctx.bindings[path.Root]; ok {
			return false
		}
	}
	return false
}

func flowPathIsOrDescendsFrom(path, base FlowPath) bool {
	if path.Root != base.Root {
		return false
	}
	if base.Fields == "" {
		return true
	}
	return path.Fields == base.Fields || strings.HasPrefix(path.Fields, base.Fields+".")
}

func (c *Context) InvalidateFlowFacts(path FlowPath) {
	for ctx := c; ctx != nil; ctx = ctx.parent {
		if _, ok := ctx.bindings[path.Root]; !ok {
			continue
		}
		for key := range ctx.checker.nullFacts {
			if flowPathIsOrDescendsFrom(FlowPathFromKey(key), path) {
				delete(ctx.checker.nullFacts, key)
			}
		}
		for key := range ctx.checker.ownedFieldFacts {
			if flowPathIsOrDescendsFrom(FlowPathFromKey(key), path) {
				delete(ctx.checker.ownedFieldFacts, key)
			}
		}
		return
	}
}

func (c *Context) InvalidateOwnedFieldFactsByPointerInvalidation(inv flow.Invalidation) {
	for ctx := c; ctx != nil; ctx = ctx.parent {
		for key := range ctx.checker.ownedFieldFacts {
			path := FlowPathFromKey(key)
			// Bindings that are not pointer-typed and whose address was
			// never taken cannot be reached through any external pointer
			// write, so invalidation through an unknown pointer cannot
			// affect their fields.
			if rootType, ok := c.DeclaredTypeForVar(path.Root); ok &&
				rootType.Indirection == 0 && !c.AddressTaken(path.Root) {
				continue
			}
			root := c.PointerFlow().Pointer(flow.Binding(path.Root))
			if inv.Unknown || !root.KnownOrigin || inv.Origins[root.Origin] {
				delete(ctx.checker.ownedFieldFacts, key)
			}
		}
	}
}

func (c *Context) NullFact(path FlowPath) NullState {
	for ctx := c; ctx != nil; ctx = ctx.parent {
		if state, ok := ctx.checker.nullFacts[path.Key()]; ok {
			return state
		}
		if _, ok := ctx.bindings[path.Root]; ok {
			return NullMaybe
		}
	}
	return NullMaybe
}

// OwnedObligationLive reports whether the owned binding `name` currently
// carries an obligation that must be discharged. A binding has a live
// obligation when:
//   - the binding has not been moved, AND
//   - it is not an owned nullable pointer whose value is statically known nil.
//
// Owned nullable pointers represent the "no obligation" state with nil
// at runtime, so a known-nil binding is considered discharged.
func (c *Context) OwnedObligationLive(name string, t ASTType) bool {
	if !t.HasOwned() {
		return false
	}
	if c.IsMoved(name) {
		return false
	}
	if t.Indirection > 0 && t.NilMask&1 != 0 &&
		c.NullFact(VarFlowPath(name)) == NullKnownNull {
		return false
	}
	return true
}

// HasLiveOwnedAlias reports whether any currently-live owned pointer
// binding aliases the storage of `target`. Used to decide whether a
// previously-moved owned binding can be re-initialized: a re-init is
// safe only when no other binding could still observe or consume the
// storage through its existing pointer.
//
// A binding y is a live owned alias of target when ALL of:
//   - y's tracked pointer expression references target (via Origin or
//     SlotTarget),
//   - y's declared type carries an owned obligation,
//   - y still holds that obligation (not moved, not known-nil).
//
// Non-owned pointer bindings are not considered aliases for this
// check: they have no cleanup responsibility and would simply observe
// whatever value is in target's slot after the re-init, which is
// memory-safe.
func (c *Context) HasLiveOwnedAlias(target string) bool {
	for _, name := range c.PointerFlow().AliasesOf(flow.Binding(target)) {
		nameStr := string(name)
		t, ok := c.DeclaredTypeForVar(nameStr)
		if !ok {
			continue
		}
		if c.OwnedObligationLive(nameStr, t) {
			return true
		}
	}
	return false
}

// OwnedObligationLiveInSnap is the snapshot-based variant of
// OwnedObligationLive: it answers the same question against the facts
// recorded in `snap` rather than the live checker state. The declared
// type is looked up through `c` since types are static.
func (c *Context) OwnedObligationLiveInSnap(snap FlowSnapshot, name string) bool {
	t, ok := c.DeclaredTypeForVar(name)
	if !ok {
		return false
	}
	if !t.HasOwned() {
		return false
	}
	if snap.Owned[name] {
		return false
	}
	if t.Indirection > 0 && t.NilMask&1 != 0 &&
		snap.Null[VarFlowPath(name)] == NullKnownNull {
		return false
	}
	return true
}

// SameObligationLiveAcross reports whether the obligation-live status of
// `name` is consistent across two control-flow paths. Use this — not
// direct comparison of `snap.Owned[name]` — when checking branch-join,
// loop-backedge, or loop-exit consistency.
//
// For non-nullable owned bindings, "consistent" means equal: the static
// state must agree, because the runtime has no way to represent "maybe
// has obligation".
//
// For nullable owned bindings, the runtime nil pointer IS the "no
// obligation" representation, so a path that ends with NullFact=Null and
// a path that ends with NullFact=Maybe (or moved=true) can legitimately
// converge. The post-merge state will be NullFact=Maybe (lossy null-fact
// merge), and the caller can discharge any remaining obligation with
// free() — whose runtime handles nil safely — or leak-check at scope
// exit will flag the binding if neither branch consumed it.
func (c *Context) SameObligationLiveAcross(a, b FlowSnapshot, name string) bool {
	t, ok := c.DeclaredTypeForVar(name)
	if !ok {
		return true
	}
	if t.Indirection > 0 && t.NilMask&1 != 0 {
		return true
	}
	return c.OwnedObligationLiveInSnap(a, name) == c.OwnedObligationLiveInSnap(b, name)
}

// UnconsumedOwned returns the names of any owned bindings in this (non-parent)
// scope that still carry a live obligation. Used for scope-exit checks.
func (c *Context) UnconsumedOwned() []string {
	var out []string
	for name, t := range c.bindings {
		if c.OwnedObligationLive(name, t) {
			out = append(out, name)
		}
	}
	return out
}

// UnconsumedOwnedVisible returns unconsumed owned bindings in this context and
// its parents. Used when a non-local exit such as return leaves all scopes.
func (c *Context) UnconsumedOwnedVisible() []string {
	var out []string
	for ctx := c; ctx != nil; ctx = ctx.parent {
		out = append(out, ctx.UnconsumedOwned()...)
	}
	return out
}
