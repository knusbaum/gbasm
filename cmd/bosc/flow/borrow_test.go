package flow

import "testing"

func TestBorrowMutationAndRefresh(t *testing.T) {
	s := NewState()
	owner := s.NewOwnedOrigin("buffer")
	old := s.BorrowView(owner, "view")
	alias := old
	s.InvalidateBorrows([]PointerExpr{owner})
	for _, ptr := range []PointerExpr{old, alias} {
		if ok, _ := s.CheckDerefValidity(ptr); ok {
			t.Fatal("old borrow survived mutable call")
		}
	}
	if ok, why := s.CheckDerefValidity(owner); !ok {
		t.Fatal(why)
	}
	fresh := s.BorrowView(owner, "view")
	if fresh.Origin == old.Origin {
		t.Fatal("fresh borrow reused stale snapshot")
	}
	if ok, why := s.CheckDerefValidity(fresh); !ok {
		t.Fatal(why)
	}
	s.InvalidateOrigin(owner.Origin, TargetDead)
	if ok, _ := s.CheckDerefValidity(fresh); ok {
		t.Fatal("fresh view outlived allocation")
	}
}

func TestBorrowMutationBranchMerge(t *testing.T) {
	before := NewState()
	owner := before.NewOwnedOrigin("buffer")
	view := before.BorrowView(owner, "view")
	before.AssignPointer("view", view)
	changed := before.Clone()
	changed.InvalidateBorrows([]PointerExpr{owner})
	merged := Merge(before, changed)
	if ok, _ := merged.CheckDerefValidity(merged.Pointer("view")); ok {
		t.Fatal("branch merge revived borrow")
	}
	fresh := merged.BorrowView(owner, "view")
	if ok, why := merged.CheckDerefValidity(fresh); !ok {
		t.Fatal(why)
	}
}

func TestBorrowMutationFieldAndUnrelated(t *testing.T) {
	s := NewState()
	owner := s.NewOwnedOrigin("buffer")
	backing := s.NewOwnedOrigin("data")
	s.SetFieldPointer("buffer", "data", backing)
	view := s.BorrowView(backing, "view")
	other := s.NewOwnedOrigin("other")
	s.InvalidateBorrows([]PointerExpr{other})
	if ok, why := s.CheckDerefValidity(view); !ok {
		t.Fatal(why)
	}
	s.InvalidateBorrows([]PointerExpr{owner})
	if ok, _ := s.CheckDerefValidity(view); ok {
		t.Fatal("owned field borrow survived mutation")
	}
}

func TestBorrowJoinPreservesSnapshotsAndLifetime(t *testing.T) {
	s := NewState()
	a := s.NewBorrowedOrigin("a")
	b := s.NewBorrowedOrigin("b")
	av := s.BorrowView(a, "av")
	bv := s.BorrowView(b, "bv")
	joined := s.JoinOrigins(av, bv)
	if members := s.JoinMembers(joined.Origin); len(members) != 2 {
		t.Fatalf("lifetime sources: %v", members)
	}
	s.InvalidateBorrows([]PointerExpr{b})
	if ok, _ := s.CheckDerefValidity(joined); ok {
		t.Fatal("join lost validity snapshot")
	}
}

func TestBorrowBackingReplacement(t *testing.T) {
	s := NewState()
	owner := s.NewOwnedOrigin("buffer")
	backing := s.NewOwnedOrigin("buffer.data")
	view := s.BorrowView(owner, "view", backing)
	s.AssignPointer("view", view)
	s.SetFieldPointer("holder", "view", view)
	if len(s.AliasesOf("buffer")) != 2 {
		t.Fatal("lost lifetime aliases")
	}
	merged := Merge(s, s.Clone())
	merged.InvalidateOrigin(backing.Origin, TargetDead)
	merged.NewOwnedOrigin("buffer.data")
	if ok, _ := merged.CheckDerefValidity(view); ok {
		t.Fatal("replacement revived old view")
	}
	fresh := merged.BorrowView(owner, "view", merged.NewOwnedOrigin("buffer.data"))
	if ok, why := merged.CheckDerefValidity(fresh); !ok {
		t.Fatal(why)
	}
}

func TestStoredFieldLoanValidity(t *testing.T) {
	s := NewState()
	owner := s.NewOwnedOrigin("source")
	view := s.BorrowView(owner, "stored")
	s.SetPathPointer("result.payload", view)
	if ok, reason := s.CheckFieldPointerValidity("result"); !ok {
		t.Fatal(reason)
	}
	s.InvalidateBorrows([]PointerExpr{owner})
	if ok, _ := s.CheckFieldPointerValidity("result"); ok {
		t.Fatal("stored field lost its expired loan snapshot")
	}
	if ok, reason := s.CheckFieldPointerValidity("other"); !ok {
		t.Fatal(reason)
	}
}

func TestBorrowMutationRequiresTrackedDependency(t *testing.T) {
	s := NewState()
	owner := s.NewOwnedOrigin("buffer")
	other := s.NewBorrowedOrigin("other_parameter")
	known := s.BorrowView(owner, "buffer_view")
	unrelated := s.BorrowView(other, "other_view")
	opaque := s.BorrowView(s.UnknownPointer(), "opaque_view", s.UnknownPointer())
	partial := s.BorrowView(s.UnknownPointer(), "partial_view", owner)
	s.InvalidateBorrows([]PointerExpr{s.UnknownPointer()})
	for _, p := range []PointerExpr{known, unrelated, opaque, partial} {
		if ok, why := s.CheckDerefValidity(p); !ok {
			t.Fatalf("unknown authority invalidated unrelated view: %s", why)
		}
	}
	s.InvalidateBorrows([]PointerExpr{owner})
	for _, p := range []PointerExpr{known, partial} {
		if ok, _ := s.CheckDerefValidity(p); ok {
			t.Fatal("tracked dependency survived mutation")
		}
	}
	for _, p := range []PointerExpr{unrelated, opaque} {
		if ok, why := s.CheckDerefValidity(p); !ok {
			t.Fatalf("unrelated view invalidated: %s", why)
		}
	}
}
