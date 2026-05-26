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
