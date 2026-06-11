package flow

import (
	"fmt"
	"sort"
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
	// joinOrigins records, for a synthesized branch-merge origin, the set of
	// real contributing origins it stands for. Created by mergePointerExpr
	// when two branches reach a binding through *different* escape-restricted
	// borrowed origins: the single-Origin PointerExpr cannot carry both, so a
	// fresh join origin (registered as OriginBorrowed — it outlives the frame
	// like any borrow) represents the union, and JoinMembers expands it back
	// to its members for return-alias inference so both params are recorded.
	joinOrigins map[Origin][]Origin
}

func NewState() *State {
	return &State{
		pointers:      make(map[Binding]PointerExpr),
		origins:       make(map[Origin]originInfo),
		fieldPointers: make(map[string]PointerExpr),
		joinOrigins:   make(map[Origin][]Origin),
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
	for o, members := range s.joinOrigins {
		cp := make([]Origin, len(members))
		copy(cp, members)
		out.joinOrigins[o] = cp
	}
	return out
}

// JoinMembers returns the real contributing origins a synthesized join
// origin stands for, transitively flattening nested joins. For a non-join
// origin it returns just that origin. Used by return-alias inference to
// record every parameter a branch-merged binding may alias.
func (s *State) JoinMembers(o Origin) []Origin {
	if s == nil {
		return []Origin{o}
	}
	members, ok := s.joinOrigins[o]
	if !ok {
		return []Origin{o}
	}
	seen := make(map[Origin]bool)
	var out []Origin
	var walk func(x Origin)
	walk = func(x Origin) {
		if sub, isJoin := s.joinOrigins[x]; isJoin {
			for _, m := range sub {
				walk(m)
			}
			return
		}
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	for _, m := range members {
		walk(m)
	}
	return out
}

func Merge(a, b *State) *State {
	out := NewState()
	// Merge origins first: mergePointerExpr (below) consults the merged
	// state's origin kinds to decide which side's origin is more escape-
	// restricted, and to register synthesized join origins. The origins
	// merge does not read pointers, so the reorder is safe.
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
	// Carry forward any existing join-origin mappings from either branch so a
	// join origin synthesized on one side stays expandable after this merge.
	if a != nil {
		for o, m := range a.joinOrigins {
			cp := make([]Origin, len(m))
			copy(cp, m)
			out.joinOrigins[o] = cp
		}
	}
	if b != nil {
		for o, m := range b.joinOrigins {
			if _, ok := out.joinOrigins[o]; !ok {
				cp := make([]Origin, len(m))
				copy(cp, m)
				out.joinOrigins[o] = cp
			}
		}
	}
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
		out.pointers[name] = out.mergePointerExpr(ap, bp)
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

// mergePointerExprPlain is the origin-agnostic join: a known fact survives
// only when both branches agree on it. Used for the slot half and as the
// fallback tail once escape-restriction has been ruled out.
func mergePointerExprPlain(a, b PointerExpr) PointerExpr {
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

// mergePointerExpr merges a top-level binding's pointer facts across a
// branch join, unioning escape-restricted taint rather than collapsing to
// unknown-and-safe when the two branches differ. This mirrors the proven
// mergeFieldPointerExpr precedent for struct fields, with one correction:
// preference is *most-restrictive*, not first-escape-restricted-wins. A
// local origin from either branch must surface (so a return/escape of the
// merged binding is rejected); only when neither side is local do borrowed
// origins combine. Two *different* borrowed origins synthesize a join
// origin that records the union for return-alias inference.
func (s *State) mergePointerExpr(a, b PointerExpr) PointerExpr {
	aLocal := a.KnownOrigin && s.OriginKindOf(a.Origin) == OriginLocal
	bLocal := b.KnownOrigin && s.OriginKindOf(b.Origin) == OriginLocal
	// Most-restrictive first: a local in either branch wins, so the merged
	// binding stays local-tainted and a later return/escape is rejected.
	if aLocal {
		return a
	}
	if bLocal {
		return b
	}
	aRestricted := a.KnownOrigin && s.IsEscapeRestricted(a.Origin)
	bRestricted := b.KnownOrigin && s.IsEscapeRestricted(b.Origin)
	switch {
	case aRestricted && bRestricted:
		if a.Origin == b.Origin {
			return mergePointerExprPlain(a, b)
		}
		// Two different borrowed origins: synthesize a join origin carrying
		// both. The single-Origin PointerExpr cannot hold the union, so the
		// join origin (registered OriginBorrowed) stands for it and is
		// expanded by JoinMembers during inference so both params record.
		jo := s.newJoinOrigin(a.Origin, b.Origin)
		return PointerExpr{Origin: jo, KnownOrigin: true}
	case aRestricted:
		return a
	case bRestricted:
		return b
	}
	return mergePointerExprPlain(a, b)
}

// JoinOrigins unions two known-origin pointer facts into a single fact,
// applying the same most-restrictive precedence as the branch merges: a
// local origin in either position surfaces (so an escape of the joined
// fact is rejected), otherwise two distinct escape-restricted origins
// synthesize a join origin carrying both (expanded by JoinMembers during
// inference). Identical origins and the all-clean case collapse to one.
// Used by struct-by-value-call field-provenance recording, where several
// arguments may flow into one returned struct.
func (s *State) JoinOrigins(a, b PointerExpr) PointerExpr {
	if !a.KnownOrigin {
		return b
	}
	if !b.KnownOrigin {
		return a
	}
	if a.Origin == b.Origin {
		return a
	}
	if s.OriginKindOf(a.Origin) == OriginLocal {
		return a
	}
	if s.OriginKindOf(b.Origin) == OriginLocal {
		return b
	}
	jo := s.newJoinOrigin(a.Origin, b.Origin)
	return PointerExpr{Origin: jo, KnownOrigin: true}
}

// newJoinOrigin returns (creating if needed) a synthetic borrowed origin
// that stands for the union of a and b. The members are flattened so a
// chain of merges produces a single flat membership set, and the join name
// is order-independent so the same pair reuses one origin.
func (s *State) newJoinOrigin(a, b Origin) Origin {
	var members []Origin
	add := make(map[Origin]bool)
	for _, o := range []Origin{a, b} {
		for _, m := range s.JoinMembers(o) {
			if !add[m] {
				add[m] = true
				members = append(members, m)
			}
		}
	}
	sort.Slice(members, func(i, j int) bool { return members[i] < members[j] })
	var sb strings.Builder
	sb.WriteString("__join")
	for _, m := range members {
		sb.WriteByte('|')
		sb.WriteString(string(m))
	}
	jo := Origin(sb.String())
	s.joinOrigins[jo] = members
	// A join of borrowed origins is itself borrowed: it outlives the frame
	// (every member does) but must not escape — escape gates reject it like
	// any other OriginBorrowed.
	s.origins[jo] = originInfo{kind: OriginBorrowed, validity: TargetLive}
	return jo
}

func (s *State) mergeFieldPointerExpr(a, b PointerExpr) PointerExpr {
	// Most-restrictive first: a local origin from EITHER branch must surface
	// so a struct returned by value with a branch-local field is rejected.
	// (The prior first-escape-restricted-wins order let a borrowed field in
	// one branch mask a local field in the other, an order-dependent escape
	// of local storage — the same hole fixed in mergePointerExpr.)
	//
	// Borrowed/borrowed union: two DIFFERENT borrowed origins synthesize a
	// join origin carrying both, identical to mergePointerExpr's top-level
	// path. The UNION is the only sound direction here: ReturnAliases tells
	// the caller which params to keep live for the returned struct, so
	// over-recording is conservative (caller keeps an extra param live) but
	// under-recording drops a param the return still borrows — a
	// use-after-free if the caller frees it. Dropping one of two merged
	// borrowed fields would do exactly that, so we record both.
	aLocal := a.KnownOrigin && s.OriginKindOf(a.Origin) == OriginLocal
	bLocal := b.KnownOrigin && s.OriginKindOf(b.Origin) == OriginLocal
	if aLocal {
		return a
	}
	if bLocal {
		return b
	}
	aRestricted := a.KnownOrigin && s.IsEscapeRestricted(a.Origin)
	bRestricted := b.KnownOrigin && s.IsEscapeRestricted(b.Origin)
	switch {
	case aRestricted && bRestricted:
		if a.Origin == b.Origin {
			return mergePointerExprPlain(a, b)
		}
		jo := s.newJoinOrigin(a.Origin, b.Origin)
		return PointerExpr{Origin: jo, KnownOrigin: true}
	case aRestricted:
		return a
	case bRestricted:
		return b
	}
	return mergePointerExprPlain(a, b)
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

// EscapingFieldOrigins returns the distinct escape-restricted origins of
// every field under the given struct binding. Used by return-alias
// inference to classify a struct returned by value: each field whose
// provenance traces to a borrowed parameter contributes that param's
// origin (recorded as an alias); a field tracing to a local contributes a
// local origin (rejected through a call, passed on a direct return). This
// is the origin-level companion to CheckStructFieldEscape, which only
// reports existence.
func (s *State) EscapingFieldOrigins(name Binding) []Origin {
	if s == nil {
		return nil
	}
	prefix := string(name) + "."
	seen := make(map[Origin]bool)
	var out []Origin
	for k, ptr := range s.fieldPointers {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		if !ptr.KnownOrigin {
			continue
		}
		if !s.IsEscapeRestricted(ptr.Origin) {
			continue
		}
		if seen[ptr.Origin] {
			continue
		}
		seen[ptr.Origin] = true
		out = append(out, ptr.Origin)
	}
	return out
}

// FieldOrigins returns every known field origin under "name." with NO
// escape-restriction filter. The return-alias summary engine uses this:
// its classifier needs origins whose borrowed-ness lives on the BINDING
// flag rather than the origin kind (a pointer parameter's origin is
// registered via NewObject with no origin-kind entry), which the
// kind-filtered EscapingFieldOrigins drops. The caller is responsible for
// classifying each origin (local → reject, borrowed → record, else pass).
func (s *State) FieldOrigins(name Binding) []Origin {
	if s == nil {
		return nil
	}
	prefix := string(name) + "."
	seen := make(map[Origin]bool)
	var out []Origin
	for k, ptr := range s.fieldPointers {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		if !ptr.KnownOrigin {
			continue
		}
		if seen[ptr.Origin] {
			continue
		}
		seen[ptr.Origin] = true
		out = append(out, ptr.Origin)
	}
	return out
}

// CheckStructFieldEscapeLocal is the local-only counterpart of
// CheckStructFieldEscape: it reports an escape only for a field whose
// origin is OriginLocal (genuinely stack-bound storage). OriginBorrowed
// fields are not flagged here — with inferred return-parameter aliasing a
// returned struct that borrows a parameter is now recordable, not a hard
// error, so the borrowed case is recorded by alias_set rather than
// rejected at the return site.
func (s *State) CheckStructFieldEscapeLocal(name Binding) (escaped bool, field string) {
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
