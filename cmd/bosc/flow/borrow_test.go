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
