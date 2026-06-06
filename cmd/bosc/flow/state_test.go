package flow

import "testing"

func requireInvalidates(t *testing.T, got Invalidation, origins ...Origin) {
	t.Helper()
	if got.Unknown {
		t.Fatalf("got unknown invalidation, want origins %v", origins)
	}
	if len(got.Origins) != len(origins) {
		t.Fatalf("got origins %v, want %v", got.Origins, origins)
	}
	for _, origin := range origins {
		if !got.Origins[origin] {
			t.Fatalf("got origins %v, want %s", got.Origins, origin)
		}
	}
}

func requireUnknownInvalidation(t *testing.T, got Invalidation) {
	t.Helper()
	if !got.Unknown {
		t.Fatalf("got invalidation %#v, want unknown", got)
	}
}

func TestBasicAliasCopyInvalidatesOrigin(t *testing.T) {
	s := NewState()
	s.DeclarePointer("a")
	s.DeclarePointer("b")
	s.AssignPointer("a", s.NewObject("a"))
	s.AssignPointer("b", s.Pointer("a"))

	requireInvalidates(t, s.WriteThroughPointer(s.Pointer("b")), "a")
}

func TestMultiHopAliasCopyInvalidatesOrigin(t *testing.T) {
	s := NewState()
	s.AssignPointer("a", s.NewObject("a"))
	s.AssignPointer("b", s.Pointer("a"))
	s.AssignPointer("c", s.Pointer("b"))

	requireInvalidates(t, s.WriteThroughPointer(s.Pointer("c")), "a")
}

func TestMultipleAliasesInvalidateSameOrigin(t *testing.T) {
	s := NewState()
	s.AssignPointer("a", s.NewObject("a"))
	s.AssignPointer("b", s.Pointer("a"))
	s.AssignPointer("c", s.Pointer("a"))

	requireInvalidates(t, s.WriteThroughPointer(s.Pointer("b")), "a")
	requireInvalidates(t, s.WriteThroughPointer(s.Pointer("c")), "a")
}

func TestAssignPointerDoesNotMutateSourceFact(t *testing.T) {
	s := NewState()
	s.AssignPointer("a", s.NewObject("a"))
	s.AssignPointer("b", s.NewObject("b"))

	s.AssignPointer("a", s.Pointer("b"))

	requireInvalidates(t, s.WriteThroughPointer(s.Pointer("a")), "b")
	requireInvalidates(t, s.WriteThroughPointer(s.Pointer("b")), "b")
}

func TestPointerSlotRebindUpdatesSlotTarget(t *testing.T) {
	s := NewState()
	s.AssignPointer("a", s.NewObject("a"))
	s.AssignPointer("other", s.NewObject("other"))
	s.AssignPointer("alias", s.Pointer("a"))
	s.AssignPointer("slot", s.AddressOfPointerSlot("alias"))

	inv := s.StorePointerThrough(s.Pointer("slot"), s.Pointer("other"))
	if inv.Unknown || len(inv.Origins) != 0 {
		t.Fatalf("got invalidation %#v, want none", inv)
	}
	requireInvalidates(t, s.WriteThroughPointer(s.Pointer("alias")), "other")
}

func TestPointerSlotRebindDoesNotInvalidateOldPointee(t *testing.T) {
	s := NewState()
	s.AssignPointer("a", s.NewObject("a"))
	s.AssignPointer("other", s.NewObject("other"))
	s.AssignPointer("alias", s.Pointer("a"))
	s.AssignPointer("slot", s.AddressOfPointerSlot("alias"))

	inv := s.StorePointerThrough(s.Pointer("slot"), s.Pointer("other"))
	if inv.Unknown || len(inv.Origins) != 0 {
		t.Fatalf("got invalidation %#v, want none", inv)
	}
}

func TestMultiLevelSlotPropagation(t *testing.T) {
	s := NewState()
	s.AssignPointer("a", s.NewObject("a"))
	s.AssignPointer("other", s.NewObject("other"))
	s.AssignPointer("alias", s.Pointer("a"))
	s.AssignPointer("slot", s.AddressOfPointerSlot("alias"))
	s.AssignPointer("slot2", s.Pointer("slot"))

	s.StorePointerThrough(s.Pointer("slot2"), s.Pointer("other"))
	requireInvalidates(t, s.WriteThroughPointer(s.Pointer("alias")), "other")
}

func TestUnknownPointerStoreInvalidatesUnknown(t *testing.T) {
	s := NewState()
	s.AssignPointer("other", s.NewObject("other"))

	requireUnknownInvalidation(t, s.StorePointerThrough(s.UnknownPointer(), s.Pointer("other")))
}

func TestUnknownPointerWriteInvalidatesUnknown(t *testing.T) {
	s := NewState()

	requireUnknownInvalidation(t, s.WriteThroughPointer(s.UnknownPointer()))
}

func TestMutBorrowCallInvalidatesPointerOrigin(t *testing.T) {
	s := NewState()
	s.AssignPointer("p", s.NewObject("p"))

	requireInvalidates(t, s.MutBorrowCall(s.Pointer("p")), "p")
}

func TestAssignUnknownPointerForgetsPreciseOrigin(t *testing.T) {
	s := NewState()
	s.AssignPointer("p", s.NewObject("p"))
	s.AssignPointer("p", s.UnknownPointer())

	requireUnknownInvalidation(t, s.WriteThroughPointer(s.Pointer("p")))
}

func TestIndexedPathPointerKeepsEscapeRestrictedOrigin(t *testing.T) {
	s := NewState()
	borrowed := s.NewBorrowedOrigin("param")

	s.SetPathPointer("arr.[]", borrowed)
	s.SetPathPointer("arr.[]", s.UnknownPointer())

	got := s.GetPathPointer("arr.[]")
	if !got.KnownOrigin || !s.IsEscapeRestricted(got.Origin) {
		t.Fatalf("indexed bucket = %#v, want escape-restricted origin", got)
	}
}

func TestIndexedPathPointerCanBecomeEscapeRestricted(t *testing.T) {
	s := NewState()
	borrowed := s.NewBorrowedOrigin("param")

	s.SetPathPointer("arr.[]", s.UnknownPointer())
	s.SetPathPointer("arr.[]", borrowed)

	got := s.GetPathPointer("arr.[]")
	if !got.KnownOrigin || got.Origin != borrowed.Origin {
		t.Fatalf("indexed bucket = %#v, want %#v", got, borrowed)
	}
}

func TestNestedIndexedPathPointerKeepsEscapeRestrictedOrigin(t *testing.T) {
	s := NewState()
	borrowed := s.NewBorrowedOrigin("param")

	s.SetPathPointer("b.items.[]", borrowed)
	s.SetPathPointer("b.items.[]", s.UnknownPointer())

	got := s.GetPathPointer("b.items.[]")
	if !got.KnownOrigin || !s.IsEscapeRestricted(got.Origin) {
		t.Fatalf("nested indexed bucket = %#v, want escape-restricted origin", got)
	}
}

func TestForgetFieldPointersUnderClearsIndexedBucket(t *testing.T) {
	s := NewState()
	s.SetPathPointer("arr.[]", s.NewBorrowedOrigin("param"))

	s.ForgetFieldPointersUnder("arr")

	if got := s.GetPathPointer("arr.[]"); got.KnownOrigin || got.KnownSlot {
		t.Fatalf("indexed bucket after forget = %#v, want unknown", got)
	}
}

func TestCopyFieldPointersUnderPathCopiesDescendants(t *testing.T) {
	s := NewState()
	borrowed := s.NewBorrowedOrigin("param")
	s.SetPathPointer("o.inner.buf", borrowed)

	s.CopyFieldPointersUnderPath("o.inner", "o2.inner")

	got := s.GetPathPointer("o2.inner.buf")
	if !got.KnownOrigin || got.Origin != borrowed.Origin {
		t.Fatalf("copied descendant = %#v, want %#v", got, borrowed)
	}
}

func TestMergeKeepsEscapeRestrictedFieldPointer(t *testing.T) {
	a := NewState()
	borrowed := a.NewBorrowedOrigin("param")
	a.SetPathPointer("b.buf", borrowed)
	b := NewState()
	b.origins[borrowed.Origin] = a.origins[borrowed.Origin]
	b.SetPathPointer("b.buf", b.UnknownPointer())

	merged := Merge(a, b)

	got := merged.GetPathPointer("b.buf")
	if !got.KnownOrigin || !merged.IsEscapeRestricted(got.Origin) {
		t.Fatalf("merged field pointer = %#v, want escape-restricted origin", got)
	}
}

func TestMergeKeepsEscapeRestrictedIndexedFieldPointer(t *testing.T) {
	a := NewState()
	borrowed := a.NewBorrowedOrigin("param")
	a.SetPathPointer("b.items.[]", borrowed)
	b := NewState()
	b.origins[borrowed.Origin] = a.origins[borrowed.Origin]
	b.SetPathPointer("b.items.[]", b.UnknownPointer())

	merged := Merge(a, b)

	got := merged.GetPathPointer("b.items.[]")
	if !got.KnownOrigin || !merged.IsEscapeRestricted(got.Origin) {
		t.Fatalf("merged indexed field pointer = %#v, want escape-restricted origin", got)
	}
}

func TestUnknownBranchMerge(t *testing.T) {
	a := NewState()
	a.AssignPointer("p", a.NewObject("a"))
	b := NewState()
	b.AssignPointer("p", b.NewObject("b"))

	merged := Merge(a, b)
	requireUnknownInvalidation(t, merged.WriteThroughPointer(merged.Pointer("p")))
}

func TestStableBranchMerge(t *testing.T) {
	a := NewState()
	a.AssignPointer("p", a.NewObject("a"))
	b := NewState()
	b.AssignPointer("p", b.NewObject("a"))

	merged := Merge(a, b)
	requireInvalidates(t, merged.WriteThroughPointer(merged.Pointer("p")), "a")
}

func TestConflictingSlotMergeForgetsSlotTarget(t *testing.T) {
	a := NewState()
	a.AssignPointer("slot", a.AddressOfPointerSlot("p"))
	b := NewState()
	b.AssignPointer("slot", b.AddressOfPointerSlot("q"))

	merged := Merge(a, b)
	requireUnknownInvalidation(t, merged.StorePointerThrough(merged.Pointer("slot"), merged.NewObject("other")))
}

func TestSlotMerge(t *testing.T) {
	a := NewState()
	a.AssignPointer("slot", a.AddressOfPointerSlot("p"))
	b := NewState()
	b.AssignPointer("slot", b.AddressOfPointerSlot("p"))

	merged := Merge(a, b)
	merged.AssignPointer("other", merged.NewObject("other"))
	merged.StorePointerThrough(merged.Pointer("slot"), merged.Pointer("other"))
	requireInvalidates(t, merged.WriteThroughPointer(merged.Pointer("p")), "other")
}

func TestForgetDropsScopedPointerFact(t *testing.T) {
	s := NewState()
	s.AssignPointer("p", s.NewObject("p"))
	s.Forget("p")

	requireUnknownInvalidation(t, s.WriteThroughPointer(s.Pointer("p")))
}

func TestCloneIsIndependent(t *testing.T) {
	s := NewState()
	s.AssignPointer("p", s.NewObject("p"))

	clone := s.Clone()
	clone.AssignPointer("p", clone.NewObject("other"))

	requireInvalidates(t, s.WriteThroughPointer(s.Pointer("p")), "p")
	requireInvalidates(t, clone.WriteThroughPointer(clone.Pointer("p")), "other")
}

func TestNewLocalOriginRegistersLive(t *testing.T) {
	s := NewState()
	ptr := s.NewLocalOrigin("x")
	if s.OriginKindOf(ptr.Origin) != OriginLocal {
		t.Fatalf("got kind %v, want OriginLocal", s.OriginKindOf(ptr.Origin))
	}
	if ok, reason := s.CheckDerefValidity(ptr); !ok {
		t.Fatalf("expected live, got %q", reason)
	}
}

func TestInvalidateOriginMovedBlocksDeref(t *testing.T) {
	s := NewState()
	ptr := s.NewLocalOrigin("x")
	s.InvalidateOrigin(ptr.Origin, TargetMoved)
	ok, reason := s.CheckDerefValidity(ptr)
	if ok {
		t.Fatalf("expected dereference to fail, got ok")
	}
	if reason == "" {
		t.Fatalf("expected reason, got empty")
	}
}

func TestInvalidateOriginDeadBlocksDeref(t *testing.T) {
	s := NewState()
	ptr := s.NewAllocatedOrigin("p")
	s.InvalidateOrigin(ptr.Origin, TargetDead)
	ok, reason := s.CheckDerefValidity(ptr)
	if ok {
		t.Fatalf("expected dereference to fail, got ok")
	}
	if reason == "" {
		t.Fatalf("expected reason, got empty")
	}
}

func TestUnknownPointerAlwaysPassesValidity(t *testing.T) {
	s := NewState()
	if ok, _ := s.CheckDerefValidity(s.UnknownPointer()); !ok {
		t.Fatalf("expected unknown pointer to pass validity check")
	}
}

func TestNewAllocatedOriginRegistersAllocated(t *testing.T) {
	s := NewState()
	ptr := s.NewAllocatedOrigin("p")
	if s.OriginKindOf(ptr.Origin) != OriginAllocated {
		t.Fatalf("got kind %v, want OriginAllocated", s.OriginKindOf(ptr.Origin))
	}
}

func TestOriginKindOfUnregistered(t *testing.T) {
	s := NewState()
	if s.OriginKindOf("nonexistent") != OriginUnknown {
		t.Fatalf("got kind %v, want OriginUnknown", s.OriginKindOf("nonexistent"))
	}
}

func TestMergePreservesWorstValidity(t *testing.T) {
	a := NewState()
	pa := a.NewAllocatedOrigin("p")
	b := NewState()
	pb := b.NewAllocatedOrigin("p")
	b.InvalidateOrigin(pb.Origin, TargetDead)

	merged := Merge(a, b)
	if ok, _ := merged.CheckDerefValidity(pa); ok {
		t.Fatalf("merge should preserve worst validity (TargetDead)")
	}
}

func TestMergeLiveAndMovedPicksMoved(t *testing.T) {
	a := NewState()
	pa := a.NewLocalOrigin("x")
	b := NewState()
	pb := b.NewLocalOrigin("x")
	b.InvalidateOrigin(pb.Origin, TargetMoved)

	merged := Merge(a, b)
	if ok, _ := merged.CheckDerefValidity(pa); ok {
		t.Fatalf("merge should preserve TargetMoved over TargetLive")
	}
}
