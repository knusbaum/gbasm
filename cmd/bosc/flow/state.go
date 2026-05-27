package flow

import (
	"fmt"
	"strings"
)

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

type TargetValidity int

const (
	TargetLive TargetValidity = iota
	TargetMoved
	TargetDead
	TargetUnknown
)

type OriginKind int

const (
	OriginUnknown OriginKind = iota
	OriginLocal
	OriginAllocated
)

type originInfo struct {
	kind     OriginKind
	validity TargetValidity
}

type State struct {
	pointers     map[Binding]PointerExpr
	origins      map[Origin]originInfo
	fieldPointers map[string]PointerExpr // keys: "binding.field"
}

func NewState() *State {
	return &State{
		pointers:      make(map[Binding]PointerExpr),
		origins:       make(map[Origin]originInfo),
		fieldPointers: make(map[string]PointerExpr),
	}
}

func (s *State) Clone() *State {
	if s == nil {
		return NewState()
	}
	out := NewState()
	for name, ptr := range s.pointers {
		out.pointers[name] = ptr
	}
	for o, info := range s.origins {
		out.origins[o] = info
	}
	for k, v := range s.fieldPointers {
		out.fieldPointers[k] = v
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
	allOrigins := make(map[Origin]bool)
	if a != nil {
		for o := range a.origins {
			allOrigins[o] = true
		}
	}
	if b != nil {
		for o := range b.origins {
			allOrigins[o] = true
		}
	}
	for o := range allOrigins {
		var ai, bi originInfo
		var aok, bok bool
		if a != nil {
			ai, aok = a.origins[o]
		}
		if b != nil {
			bi, bok = b.origins[o]
		}
		switch {
		case aok && bok:
			out.origins[o] = mergeOriginInfo(ai, bi)
		case aok:
			out.origins[o] = ai
		default:
			out.origins[o] = bi
		}
	}
	allFields := make(map[string]bool)
	if a != nil {
		for k := range a.fieldPointers {
			allFields[k] = true
		}
	}
	if b != nil {
		for k := range b.fieldPointers {
			allFields[k] = true
		}
	}
	for k := range allFields {
		var ap, bp PointerExpr
		if a != nil {
			ap = a.fieldPointers[k]
		}
		if b != nil {
			bp = b.fieldPointers[k]
		}
		out.fieldPointers[k] = mergePointerExpr(ap, bp)
	}
	return out
}

func mergeOriginInfo(a, b originInfo) originInfo {
	kind := a.kind
	if a.kind != b.kind {
		kind = OriginUnknown
	}
	return originInfo{kind: kind, validity: mergeValidity(a.validity, b.validity)}
}

func mergeValidity(a, b TargetValidity) TargetValidity {
	order := []TargetValidity{TargetDead, TargetMoved, TargetUnknown, TargetLive}
	for _, v := range order {
		if a == v || b == v {
			return v
		}
	}
	return TargetLive
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
	s.ForgetFieldPointers(name)
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

func (s *State) NewLocalOrigin(name Binding) PointerExpr {
	o := Origin(name)
	s.origins[o] = originInfo{kind: OriginLocal, validity: TargetLive}
	return PointerExpr{Origin: o, KnownOrigin: true}
}

func (s *State) NewAllocatedOrigin(name Binding) PointerExpr {
	o := Origin(name)
	s.origins[o] = originInfo{kind: OriginAllocated, validity: TargetLive}
	return PointerExpr{Origin: o, KnownOrigin: true}
}

func (s *State) InvalidateOrigin(o Origin, v TargetValidity) {
	if info, ok := s.origins[o]; ok {
		info.validity = v
		s.origins[o] = info
	}
}

func (s *State) CheckDerefValidity(ptr PointerExpr) (ok bool, reason string) {
	if !ptr.KnownOrigin || ptr.Origin == Unknown {
		return true, ""
	}
	info, exists := s.origins[ptr.Origin]
	if !exists {
		return true, ""
	}
	switch info.validity {
	case TargetMoved:
		return false, fmt.Sprintf("cannot dereference pointer to %q: the target was consumed", string(ptr.Origin))
	case TargetDead:
		return false, fmt.Sprintf("cannot dereference pointer to %q: the allocation was freed", string(ptr.Origin))
	}
	return true, ""
}

func (s *State) OriginKindOf(o Origin) OriginKind {
	if s == nil {
		return OriginUnknown
	}
	return s.origins[o].kind
}

func fieldKey(name Binding, field string) string {
	return string(name) + "." + field
}

func (s *State) SetFieldPointer(name Binding, field string, ptr PointerExpr) {
	s.fieldPointers[fieldKey(name, field)] = ptr
}

func (s *State) GetFieldPointer(name Binding, field string) PointerExpr {
	if s == nil {
		return PointerExpr{}
	}
	return s.fieldPointers[fieldKey(name, field)]
}

func (s *State) ForgetFieldPointers(name Binding) {
	prefix := string(name) + "."
	for k := range s.fieldPointers {
		if strings.HasPrefix(k, prefix) {
			delete(s.fieldPointers, k)
		}
	}
}

func (s *State) CopyFieldPointers(src, dst Binding) {
	prefix := string(src) + "."
	s.ForgetFieldPointers(dst)
	for k, v := range s.fieldPointers {
		if strings.HasPrefix(k, prefix) {
			field := k[len(prefix):]
			s.fieldPointers[fieldKey(dst, field)] = v
		}
	}
}

// CheckStructFieldEscape returns (true, fieldName) if any field pointer for
// the binding targets a OriginLocal origin that is still known.
func (s *State) CheckStructFieldEscape(name Binding) (escaped bool, field string) {
	if s == nil {
		return false, ""
	}
	prefix := string(name) + "."
	for k, ptr := range s.fieldPointers {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		if !ptr.KnownOrigin {
			continue
		}
		if s.origins[ptr.Origin].kind == OriginLocal {
			return true, k[len(prefix):]
		}
	}
	return false, ""
}
