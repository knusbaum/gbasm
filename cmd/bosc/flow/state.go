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
	// OriginLocal: storage tied to a stack frame's binding (a local `var`
	// or a fixed-array root). Invalidated at scope exit.
	OriginLocal
	// OriginAllocated: heap-allocated storage owned by a binding; valid
	// until the owner is consumed (free / dispose / consume-move).
	OriginAllocated
	// OriginBorrowed: a borrowed view's source — a function parameter
	// whose lifetime is the caller's, opaque to the callee. The view
	// must not outlive the call: escape gates treat it the same as
	// OriginLocal for that purpose, but unlike OriginLocal it is *not*
	// invalidated at scope exit (the source outlives the borrower).
	OriginBorrowed
)

type originInfo struct {
	kind     OriginKind
	validity TargetValidity
}

type State struct {
	pointers      map[Binding]PointerExpr
	origins       map[Origin]originInfo
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
		out.fieldPointers[k] = out.mergeFieldPointerExpr(ap, bp)
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

func (s *State) mergeFieldPointerExpr(a, b PointerExpr) PointerExpr {
	if a.KnownOrigin && s.IsEscapeRestricted(a.Origin) {
		return a
	}
	if b.KnownOrigin && s.IsEscapeRestricted(b.Origin) {
		return b
	}
	return mergePointerExpr(a, b)
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

// AliasesOf returns the names of bindings that reference `target` via
// pointer flow — either as a direct pointer binding whose PointerExpr
// matches target as an origin or slot, or as the *owner* of a field
// pointer that matches. For the field-pointer case the binding name
// returned is the owning struct binding, since that binding's liveness
// (and ownership obligation) governs whether the field alias is live.
// The caller is expected to filter by liveness and type qualifiers.
func (s *State) AliasesOf(target Binding) []Binding {
	if s == nil {
		return nil
	}
	matches := func(ptr PointerExpr) bool {
		return (ptr.KnownOrigin && ptr.Origin == Origin(target)) ||
			(ptr.KnownSlot && ptr.SlotTarget == target)
	}
	var out []Binding
	for name, ptr := range s.pointers {
		if name == target {
			continue
		}
		if matches(ptr) {
			out = append(out, name)
		}
	}
	for k, ptr := range s.fieldPointers {
		if !matches(ptr) {
			continue
		}
		i := strings.IndexByte(k, '.')
		if i <= 0 {
			continue
		}
		owner := Binding(k[:i])
		if owner == target {
			continue
		}
		out = append(out, owner)
	}
	return out
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

// NewBorrowedOrigin registers an origin for a binding whose source lives in
// the caller's frame — typically a non-owned slice or pointer parameter.
// The view is valid for the whole call (never invalidated at block exit)
// but must not escape the function: escape gates that reject OriginLocal
// also reject OriginBorrowed.
func (s *State) NewBorrowedOrigin(name Binding) PointerExpr {
	o := Origin(name)
	s.origins[o] = originInfo{kind: OriginBorrowed, validity: TargetLive}
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

// IsEscapeRestricted reports whether an origin's storage outlives a function
// frame only by virtue of being borrowed or stack-rooted — true for
// OriginLocal (local fixed-array or value binding) and OriginBorrowed
// (parameter view). The single predicate consulted by escape gates so
// any new short-lived origin kind has one place to be wired in.
func (s *State) IsEscapeRestricted(o Origin) bool {
	if s == nil {
		return false
	}
	switch s.origins[o].kind {
	case OriginLocal, OriginBorrowed:
		return true
	}
	return false
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

// SetPathPointer stores a provenance fact at a full dotted path produced
// by ProvenancePathForExpr (e.g. "b.inner.buf", "arr.[]"). Single-level
// keys match fieldKey's format so existing readers and writers continue
// to interop with multi-level entries in the same map.
func (s *State) SetPathPointer(path string, ptr PointerExpr) {
	if strings.HasSuffix(path, ".[]") {
		existing := s.fieldPointers[path]
		if existing.KnownOrigin && s.IsEscapeRestricted(existing.Origin) {
			return
		}
	}
	s.fieldPointers[path] = ptr
}

// GetPathPointer reads a provenance fact by full dotted path.
func (s *State) GetPathPointer(path string) PointerExpr {
	if s == nil {
		return PointerExpr{}
	}
	return s.fieldPointers[path]
}

func (s *State) ForgetFieldPointers(name Binding) {
	prefix := string(name) + "."
	for k := range s.fieldPointers {
		if strings.HasPrefix(k, prefix) {
			delete(s.fieldPointers, k)
		}
	}
}

// ForgetFieldPointersUnder deletes every fieldPointers entry whose key
// starts with `prefix + "."`. Called when an aggregate assignment
// overwrites a sub-path so descendant facts from the previous contents
// don't survive as false positives.
func (s *State) ForgetFieldPointersUnder(prefix string) {
	p := prefix + "."
	for k := range s.fieldPointers {
		if strings.HasPrefix(k, p) {
			delete(s.fieldPointers, k)
		}
	}
}

// CopyFieldPointersUnderPath copies descendant provenance facts from
// one sub-path to another. Clears any pre-existing descendants of dst
// first, then re-keys each `src.<suffix>` entry as `dst.<suffix>`. Used
// for sub-path struct copies like `o.inner = other.inner`.
func (s *State) CopyFieldPointersUnderPath(src, dst string) {
	s.ForgetFieldPointersUnder(dst)
	srcPrefix := src + "."
	for k, v := range s.fieldPointers {
		if strings.HasPrefix(k, srcPrefix) {
			s.fieldPointers[dst+"."+k[len(srcPrefix):]] = v
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
// the binding targets an escape-restricted origin (OriginLocal or
// OriginBorrowed) that is still known.
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
		if s.IsEscapeRestricted(ptr.Origin) {
			return true, k[len(prefix):]
		}
	}
	return false, ""
}
