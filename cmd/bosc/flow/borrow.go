package flow

import (
	"crypto/sha256"
	"fmt"
	"strings"
)

// BorrowView captures the validity of a source at the current mutation epoch.
// The source allocation's identity stays intact: only derived snapshots expire.
// site identifies a source expression or destination field, not runtime storage.
func (s *State) BorrowView(source PointerExpr, site string, dependencies ...PointerExpr) PointerExpr {
	if !source.KnownOrigin && len(dependencies) == 0 {
		return source
	}
	base := source.Origin
	if !source.KnownOrigin {
		base = Unknown
	}
	key := fmt.Sprintf("%s\x00%s\x00%d", site, base, s.borrowEpoch)
	var deps []Origin
	for _, dependency := range dependencies {
		origin := dependency.Origin
		if !dependency.KnownOrigin {
			origin = Unknown
		}
		deps = append(deps, origin)
		key += "\x00" + string(origin)
	}
	origin := Origin(fmt.Sprintf("__borrow_%x", sha256.Sum256([]byte(key))))
	if _, exists := s.borrowSources[origin]; !exists {
		s.borrowSources[origin] = base
		s.borrowDependencies[origin] = deps
		s.origins[origin] = originInfo{kind: s.OriginKindOf(base), validity: TargetLive}
	}
	source.Origin = origin
	source.KnownOrigin = true
	return source
}

func (s *State) IsBorrow(ptr PointerExpr) bool {
	if !ptr.KnownOrigin {
		return false
	}
	if _, ok := s.borrowSources[ptr.Origin]; ok {
		return true
	}
	for _, member := range s.joinOrigins[ptr.Origin] {
		if s.IsBorrow(PointerExpr{Origin: member, KnownOrigin: true}) {
			return true
		}
	}
	return false
}

// OriginName hides synthetic snapshot names from source diagnostics.
func (s *State) OriginName(origin Origin) string {
	for {
		source, borrow := s.borrowSources[origin]
		if !borrow {
			if members, joined := s.joinOrigins[origin]; joined && len(members) > 0 {
				for _, member := range members {
					if s.OriginKindOf(member) == OriginLocal {
						return s.OriginName(member)
					}
				}
				return s.OriginName(members[0])
			}
			return string(origin)
		}
		origin = source
	}
}

// InvalidateBorrows treats all mutable authorities in one call together. An
// authority itself remains usable; views derived through it do not. Unknown
// authorities conservatively affect every outstanding view. Field allocation
// origins are followed so direct reads of owned slice/pointer fields expire too.
func (s *State) InvalidateBorrows(authorities []PointerExpr) {
	if len(authorities) == 0 {
		return
	}
	for _, arg := range authorities {
		targets := make(map[Origin]bool)
		unknown := !arg.KnownOrigin
		if arg.KnownOrigin {
			for _, root := range s.mutationSources(arg.Origin) {
				if root == Unknown {
					unknown = true
				}
				targets[root] = true
			}
		}
		// Follow pointee field facts transitively, including nested allocations.
		for changed := true; changed; {
			changed = false
			for path, ptr := range s.fieldPointers {
				if !ptr.KnownOrigin {
					continue
				}
				owner, _, ok := strings.Cut(path, ".")
				if !ok || !targets[Origin(owner)] {
					continue
				}
				for _, root := range s.mutationSources(ptr.Origin) {
					if !targets[root] {
						targets[root] = true
						changed = true
					}
				}
			}
		}
		for borrow := range s.borrowSources {
			if arg.KnownOrigin && borrow == arg.Origin {
				continue
			}
			matches := unknown
			for _, root := range s.mutationSources(borrow) {
				matches = matches || targets[root] || root == Unknown
			}
			if matches {
				info := s.origins[borrow]
				cause := Unknown
				for _, root := range s.mutationSources(borrow) {
					if targets[root] {
						cause = root
						break
					}
				}
				if info.validity != TargetMutated {
					info.mutationSource = cause
				}
				info.validity = mergeValidity(info.validity, TargetMutated)
				s.origins[borrow] = info
			}
		}
	}
	s.borrowEpoch++
}

func (s *State) mutationSources(origin Origin) []Origin {
	seen := make(map[Origin]bool)
	var out []Origin
	var visit func(Origin)
	visit = func(o Origin) {
		if seen[o] {
			return
		}
		seen[o] = true
		if source, borrow := s.borrowSources[o]; borrow {
			visit(source)
			for _, dependency := range s.borrowDependencies[o] {
				visit(dependency)
			}
		} else if members, joined := s.joinOrigins[o]; joined {
			for _, member := range members {
				visit(member)
			}
		} else {
			out = append(out, o)
		}
	}
	visit(origin)
	return out
}

// AllocationOrigin strips view snapshots while retaining allocation/slot
// identity for field paths and ownership bookkeeping.
func (s *State) AllocationOrigin(origin Origin) Origin {
	for {
		source, ok := s.borrowSources[origin]
		if !ok {
			return origin
		}
		origin = source
	}
}

func (s *State) validityMembers(origin Origin) []Origin {
	if members, ok := s.joinOrigins[origin]; ok {
		var out []Origin
		for _, member := range members {
			out = append(out, s.validityMembers(member)...)
		}
		return out
	}
	return []Origin{origin}
}
