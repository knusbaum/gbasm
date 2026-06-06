package main

import "testing"

func testOwnedType() ASTType {
	return ASTType{Name: "i64", Signed: true, OwnedMask: 1}
}

func TestCheckerStateTracksFactsAcrossContextScopes(t *testing.T) {
	root := NewContext()
	root.BindVar(&Symbol{Name: "owner"}, "owner", testOwnedType(), true)

	child := root.SubContext()
	child.BindVar(&Symbol{Name: "local"}, "local", testOwnedType(), true)

	if root.checker == child.checker {
		t.Fatal("child context should have distinct checker state")
	}

	child.MoveConsume("owner")
	if !root.IsMoved("owner") {
		t.Fatal("moving parent binding from child should update parent checker state")
	}

	child.MoveConsume("local")
	snap := child.OwnedBindingsSnapshot()
	if !snap["owner"] || !snap["local"] {
		t.Fatalf("snapshot did not include moved parent and child owned bindings: %#v", snap)
	}

	child.RestoreOwnedBindings(map[string]bool{"owner": false, "local": false})
	if root.IsMoved("owner") || child.IsMoved("local") {
		t.Fatal("restore should update the checker state attached to each binding scope")
	}
}

func TestCheckerStateSnapshotsFlowFacts(t *testing.T) {
	c := NewContext()
	c.BindVar(&Symbol{Name: "p"}, "p", ASTType{Name: "node", Indirection: 1, NilMask: 1}, false)

	c.SetNullFact(VarFlowPath("p"), NullKnownNonNull)
	c.SetOwnedFieldConsumed(VarFlowPath("p").Append("next"), true)
	c.SetBorrowedBinding("p", true)

	snap := c.FlowSnapshot()
	c.InvalidateFlowFacts(VarFlowPath("p"))
	c.SetBorrowedBinding("p", false)

	if c.NullFact(VarFlowPath("p")) != NullMaybe {
		t.Fatal("invalidation should clear null fact")
	}
	if c.OwnedFieldConsumed(VarFlowPath("p").Append("next")) {
		t.Fatal("invalidation should clear owned field fact")
	}
	if c.IsBorrowedBinding("p") {
		t.Fatal("borrowed binding should have been cleared")
	}

	c.RestoreFlowSnapshot(snap)
	if c.NullFact(VarFlowPath("p")) != NullKnownNonNull {
		t.Fatal("restore should recover null fact")
	}
	if !c.OwnedFieldConsumed(VarFlowPath("p").Append("next")) {
		t.Fatal("restore should recover owned field fact")
	}
	if !c.IsBorrowedBinding("p") {
		t.Fatal("restore should recover borrowed binding")
	}
}

func TestCheckerStateOwnsControlFlowFacts(t *testing.T) {
	c := NewContext()

	c.PushRetlabel(ASTType{Name: "i64", Signed: true})
	if got := c.ReturnType(); got.Name != "i64" {
		t.Fatalf("return type = %s, want i64", got)
	}
	c.PopRetlabel()
	if got := c.ReturnType(); !got.Same(voidASTType()) {
		t.Fatalf("return type after pop = %s, want void", got)
	}

	c.PushContLabel()
	c.RecordContinue(FlowSnapshot{Owned: map[string]bool{"x": true}})
	continues := c.ContinueStates()
	if len(continues) != 1 || !continues[0].Owned["x"] {
		t.Fatalf("continue states = %#v, want recorded ownership snapshot", continues)
	}
	c.PopContLabel()

	c.PushBreakLabel()
	c.RecordBreak(FlowSnapshot{Owned: map[string]bool{"y": true}})
	breaks := c.BreakStates()
	if len(breaks) != 1 || !breaks[0].Owned["y"] {
		t.Fatalf("break states = %#v, want recorded ownership snapshot", breaks)
	}
	c.PopBreakLabel()
}

func TestFlowPathHelpers(t *testing.T) {
	path := VarFlowPath("root").Append("left").Append("right")
	if path.Key() != "root.left.right" {
		t.Fatalf("path key = %q, want root.left.right", path.Key())
	}
	if got := path.ParentRoot(); got != VarFlowPath("root") {
		t.Fatalf("parent root = %#v, want root", got)
	}
	if got := FlowPathFromKey("root.left.right"); got != path {
		t.Fatalf("from key = %#v, want %#v", got, path)
	}

	expr := &Dot{
		Val:    &NonNullAssert{Val: &Dot{Val: &Symbol{Name: "root"}, Member: "left"}},
		Member: "right",
	}
	got, ok := FlowPathForExpr(expr)
	if !ok || got != path {
		t.Fatalf("expr path = %#v, %v; want %#v, true", got, ok, path)
	}
	if _, ok := FlowPathForExpr(&Literal{}); ok {
		t.Fatal("literal should not have a flow path")
	}
}

func TestProvenancePathForExprIncludesIndexBuckets(t *testing.T) {
	tests := []struct {
		name string
		expr AST
		want string
		ok   bool
	}{
		{
			name: "symbol",
			expr: &Symbol{Name: "s"},
			want: "s",
			ok:   true,
		},
		{
			name: "field",
			expr: &Dot{Val: &Symbol{Name: "b"}, Member: "buf"},
			want: "b.buf",
			ok:   true,
		},
		{
			name: "nested field",
			expr: &Dot{Val: &Dot{Val: &Symbol{Name: "o"}, Member: "inner"}, Member: "buf"},
			want: "o.inner.buf",
			ok:   true,
		},
		{
			name: "index bucket",
			expr: &Index{Val: &Symbol{Name: "arr"}},
			want: "arr.[]",
			ok:   true,
		},
		{
			name: "nested index bucket",
			expr: &Index{Val: &Dot{Val: &Symbol{Name: "b"}, Member: "items"}},
			want: "b.items.[]",
			ok:   true,
		},
		{
			name: "nonnull wrapper",
			expr: &NonNullAssert{Val: &Dot{Val: &Symbol{Name: "b"}, Member: "buf"}},
			want: "b.buf",
			ok:   true,
		},
		{
			name: "literal",
			expr: &Literal{},
			ok:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ProvenancePathForExpr(tt.expr)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if !ok {
				return
			}
			if got.Key() != tt.want {
				t.Fatalf("path = %q, want %q", got.Key(), tt.want)
			}
		})
	}
}

func TestCheckerStateMoveUnmoveAndUnconsumed(t *testing.T) {
	c := NewContext()
	c.BindVar(&Symbol{Name: "x"}, "x", testOwnedType(), false)
	c.BindVar(&Symbol{Name: "plain"}, "plain", ASTType{Name: "i64", Signed: true}, false)

	if got := c.UnconsumedOwned(); len(got) != 1 || got[0] != "x" {
		t.Fatalf("unconsumed = %#v, want x", got)
	}

	c.MoveConsume("x")
	if !c.IsMoved("x") {
		t.Fatal("x should be moved")
	}
	if got := c.UnconsumedOwned(); len(got) != 0 {
		t.Fatalf("unconsumed after move = %#v, want none", got)
	}

	c.Unmove("x")
	if c.IsMoved("x") {
		t.Fatal("x should no longer be moved")
	}
}

func TestCheckerStateVisibleUnconsumedOwned(t *testing.T) {
	root := NewContext()
	root.BindVar(&Symbol{Name: "outer"}, "outer", testOwnedType(), false)
	child := root.SubContext()
	child.BindVar(&Symbol{Name: "inner"}, "inner", testOwnedType(), false)

	got := child.UnconsumedOwnedVisible()
	seen := map[string]bool{}
	for _, name := range got {
		seen[name] = true
	}
	if !seen["outer"] || !seen["inner"] || len(seen) != 2 {
		t.Fatalf("visible unconsumed = %#v, want outer and inner", got)
	}
}

func TestCheckerStateMergeHelpers(t *testing.T) {
	p := VarFlowPath("p")
	q := VarFlowPath("q")

	nulls := MergeNullFacts(
		map[FlowPath]NullState{p: NullKnownNonNull, q: NullKnownNull},
		map[FlowPath]NullState{p: NullKnownNonNull, q: NullKnownNonNull},
	)
	if nulls[p] != NullKnownNonNull {
		t.Fatalf("merged p = %v, want non-null", nulls[p])
	}
	if _, ok := nulls[q]; ok {
		t.Fatal("conflicting q null facts should not survive merge")
	}

	borrowed := MergeBorrowedBindings(
		map[string]bool{"a": true, "b": false},
		map[string]bool{"c": true},
	)
	if !borrowed["a"] || !borrowed["c"] || borrowed["b"] {
		t.Fatalf("merged borrowed = %#v, want true a/c only", borrowed)
	}
}

func TestCheckerStatePointerInvalidation(t *testing.T) {
	c := NewContext()
	c.BindVar(&Symbol{Name: "a"}, "a", ASTType{Name: "box", Indirection: 1}, false)
	c.BindVar(&Symbol{Name: "b"}, "b", ASTType{Name: "box", Indirection: 1}, false)
	c.PointerFlow().AssignPointer("a", c.PointerFlow().NewObject("a"))
	c.PointerFlow().AssignPointer("b", c.PointerFlow().NewObject("b"))
	c.SetOwnedFieldConsumed(VarFlowPath("a").Append("child"), true)
	c.SetOwnedFieldConsumed(VarFlowPath("b").Append("child"), true)

	inv := c.PointerFlow().WriteThroughPointer(c.PointerFlow().Pointer("a"))
	c.InvalidateOwnedFieldFactsByPointerInvalidation(inv)

	if c.OwnedFieldConsumed(VarFlowPath("a").Append("child")) {
		t.Fatal("write through a should invalidate a.child")
	}
	if !c.OwnedFieldConsumed(VarFlowPath("b").Append("child")) {
		t.Fatal("write through a should not invalidate independent b.child")
	}

	c.InvalidateOwnedFieldFactsByPointerInvalidation(c.PointerFlow().WriteThroughPointer(c.PointerFlow().UnknownPointer()))
	if c.OwnedFieldConsumed(VarFlowPath("b").Append("child")) {
		t.Fatal("unknown pointer write should invalidate b.child")
	}
}

func TestCheckerStateForgetPointerBindings(t *testing.T) {
	c := NewContext()
	c.BindVar(&Symbol{Name: "p"}, "p", ASTType{Name: "node", Indirection: 1}, false)
	c.BindVar(&Symbol{Name: "x"}, "x", ASTType{Name: "i64", Signed: true}, false)
	c.PointerFlow().AssignPointer("p", c.PointerFlow().NewObject("p"))

	c.ForgetPointerBindings()

	inv := c.PointerFlow().WriteThroughPointer(c.PointerFlow().Pointer("p"))
	if !inv.Unknown {
		t.Fatalf("forgotten pointer invalidation = %#v, want unknown", inv)
	}
}
