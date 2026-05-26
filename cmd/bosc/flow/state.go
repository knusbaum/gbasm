package flow

type Origin string
type Binding string

const Unknown Origin = "unknown"

type PointerExpr struct {
	Origin      Origin
	SlotTarget  Binding
	KnownOrigin bool
	KnownSlot   bool
}

type Invalidation struct {
	Unknown bool
	Origins map[Origin]bool
}

type State struct {
	pointers map[Binding]PointerExpr
}

func NewState() *State {
	return &State{pointers: make(map[Binding]PointerExpr)}
}

func (s *State) Clone() *State {
	if s == nil {
		return NewState()
	}
	out := NewState()
	for name, ptr := range s.pointers {
		out.pointers[name] = ptr
	}
	return out
}

func Merge(a, b *State) *State {
	out := NewState()
	seen := make(map[Binding]bool)
	if a != nil {
		for name := range a.pointers {
			seen[name] = true
		}
	}
	if b != nil {
		for name := range b.pointers {
			seen[name] = true
		}
	}
	for name := range seen {
		ap := a.Pointer(name)
		bp := b.Pointer(name)
		out.pointers[name] = mergePointerExpr(ap, bp)
	}
	return out
}

func mergePointerExpr(a, b PointerExpr) PointerExpr {
	var out PointerExpr
	if a.KnownOrigin && b.KnownOrigin && a.Origin == b.Origin {
		out.Origin = a.Origin
		out.KnownOrigin = true
	}
	if a.KnownSlot && b.KnownSlot && a.SlotTarget == b.SlotTarget {
		out.SlotTarget = a.SlotTarget
		out.KnownSlot = true
	}
	return out
}

func (s *State) DeclarePointer(name Binding) {
	s.pointers[name] = s.UnknownPointer()
}

func (s *State) Forget(name Binding) {
	delete(s.pointers, name)
}

func (s *State) SetPointer(name Binding, value PointerExpr) {
	s.pointers[name] = value
}

func (s *State) Pointer(name Binding) PointerExpr {
	if s == nil {
		return PointerExpr{}
	}
	ptr, ok := s.pointers[name]
	if !ok {
		return PointerExpr{}
	}
	return ptr
}

func (s *State) NewObject(name Binding) PointerExpr {
	return PointerExpr{Origin: Origin(name), KnownOrigin: true}
}

func (s *State) UnknownPointer() PointerExpr {
	return PointerExpr{}
}

func (s *State) AddressOfPointerSlot(name Binding) PointerExpr {
	return PointerExpr{SlotTarget: name, KnownSlot: true}
}

func (s *State) AssignPointer(dst Binding, src PointerExpr) {
	s.SetPointer(dst, src)
}

func (s *State) StorePointerThrough(ptr PointerExpr, src PointerExpr) Invalidation {
	if ptr.KnownSlot {
		s.AssignPointer(ptr.SlotTarget, src)
		return Invalidation{}
	}
	return Invalidation{Unknown: true}
}

func (s *State) WriteThroughPointer(ptr PointerExpr) Invalidation {
	return invalidationForPointer(ptr)
}

func (s *State) MutBorrowCall(arg PointerExpr) Invalidation {
	return invalidationForPointer(arg)
}

func invalidationForPointer(ptr PointerExpr) Invalidation {
	if !ptr.KnownOrigin || ptr.Origin == Unknown {
		return Invalidation{Unknown: true}
	}
	return Invalidation{Origins: map[Origin]bool{ptr.Origin: true}}
}
