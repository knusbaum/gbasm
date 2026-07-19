package main

import (
	"fmt"
	"testing"
)

// This is the before/after oracle for the postfix→prefix type-syntax migration
// (DESIGN_prefix_type_syntax.md). Each case pairs a postfix form (today) with
// its target prefix form and the exact ASTType both must produce — the `want`
// values are ground truth captured from the current parser, written as real
// ASTType literals so field names are compile-checked.
//
//   - TestTypeEncodingPostfix: the *current* parser must produce `want` for the
//     postfix form. Locks today's behavior; flips to reject-postfix once the
//     parser goes prefix-only.
//   - TestTypeEncodingPrefix: the *new* parser must produce the SAME `want` for
//     the prefix form. Fails until parseTypeName is rewritten — the TDD target.

// atSame is a strict, position-insensitive, recursive ASTType equality. Stricter
// than SameExact (which recurses into Element with the laxer Same) and it skips
// the unexported position field that reflect.DeepEqual would choke on.
func atSame(a, b ASTType) bool {
	if a.Name != b.Name || a.Indirection != b.Indirection || a.ArraySize != b.ArraySize ||
		a.MutMask != b.MutMask || a.OwnedMask != b.OwnedMask || a.NilMask != b.NilMask ||
		a.Signed != b.Signed {
		return false
	}
	if (a.Element == nil) != (b.Element == nil) {
		return false
	}
	if a.Element != nil && !atSame(*a.Element, *b.Element) {
		return false
	}
	return true
}

// dumpAT renders an ASTType's structure for readable failure messages.
func dumpAT(t ASTType) string {
	s := fmt.Sprintf("Name=%q Ind=%d Arr=%d Mut=%b Own=%b Nil=%b Signed=%v",
		t.Name, t.Indirection, t.ArraySize, t.MutMask, t.OwnedMask, t.NilMask, t.Signed)
	if t.Element != nil {
		s += " Elem{" + dumpAT(*t.Element) + "}"
	}
	return s
}

// s builds a slice ASTType {[]elem}; a builds an array {[n]elem}. Only used to
// keep the nested `want` literals readable — they wrap a given element verbatim,
// they do NOT re-derive the mask "bump", so they can't mask a parser bug (the
// masks are written out explicitly on each `want`).
func sl(elem ASTType) ASTType   { return ASTType{Element: &elem} }
func ar(n int, e ASTType) ASTType { return ASTType{ArraySize: n, Element: &e} }

var typeEncodingCases = []struct {
	postfix, prefix string
	want            ASTType
}{
	{"i64", "i64", ASTType{Name: "i64", Signed: true}},
	{"byte", "byte", ASTType{Name: "byte"}},
	{"bool", "bool", ASTType{Name: "bool"}},
	{"byte[]", "[]byte", sl(ASTType{Name: "byte"})},
	{"i64[]", "[]i64", sl(ASTType{Name: "i64", Signed: true})},
	{"byte[8]", "[8]byte", ar(8, ASTType{Name: "byte"})},
	{"i64[3]", "[3]i64", ar(3, ASTType{Name: "i64", Signed: true})},
	{"byte[][]", "[][]byte", sl(sl(ASTType{Name: "byte"}))},
	{"byte[8][1]", "[1][8]byte", ar(1, ar(8, ASTType{Name: "byte"}))},
	{"byte[][1]", "[1][]byte", ar(1, sl(ASTType{Name: "byte"}))},
	{"*i64", "*i64", ASTType{Name: "i64", Indirection: 1, Signed: true}},
	{"**i64", "**i64", ASTType{Name: "i64", Indirection: 2, Signed: true}},
	{"*byte", "*byte", ASTType{Name: "byte", Indirection: 1}},
	{"*byte[]", "[]*byte", sl(ASTType{Name: "byte", Indirection: 1})},
	{"*(byte[])", "*[]byte", ASTType{Indirection: 1, Element: &ASTType{Name: "byte"}}},
	{"*(byte[8])", "*[8]byte", ASTType{Indirection: 1, ArraySize: 8, Element: &ASTType{Name: "byte"}}},
	{"*mut i64", "*mut i64", ASTType{Name: "i64", Indirection: 1, MutMask: 0b10, Signed: true}},
	{"mut byte[]", "[]mut byte", ASTType{MutMask: 0b10, Element: &ASTType{Name: "byte"}}},
	{"owned byte[]", "[]owned byte", ASTType{OwnedMask: 0b10, Element: &ASTType{Name: "byte"}}},
	{"owned byte[8]", "owned [8]byte", ASTType{ArraySize: 8, OwnedMask: 0b1, Element: &ASTType{Name: "byte"}}},
	{"owned *mut i64", "owned *mut i64", ASTType{Name: "i64", Indirection: 1, MutMask: 0b10, OwnedMask: 0b1, Signed: true}},
	{"owned *owned mut node", "owned *owned mut node", ASTType{Name: "node", Indirection: 1, MutMask: 0b10, OwnedMask: 0b11}},
	{"*?i64", "?*i64", ASTType{Name: "i64", Indirection: 1, NilMask: 0b1, Signed: true}},
	{"*?*i64", "?**i64", ASTType{Name: "i64", Indirection: 2, NilMask: 0b1, Signed: true}},
	{"owned *?owned mut node", "owned ?*owned mut node", ASTType{Name: "node", Indirection: 1, MutMask: 0b10, OwnedMask: 0b11, NilMask: 0b1}},
}

func TestTypeEncodingPostfix(t *testing.T) {
	for _, c := range typeEncodingCases {
		at, err := parseTypeString(c.postfix)
		if err != nil {
			t.Errorf("%-24q parse err: %v", c.postfix, err)
			continue
		}
		if !atSame(at, c.want) {
			t.Errorf("%-24q\n  got  %s\n  want %s", c.postfix, dumpAT(at), dumpAT(c.want))
		}
	}
}

func TestTypeEncodingPrefix(t *testing.T) {
	t.Skip("TDD target: un-skip once parseTypeName is prefix (DESIGN §7 step 2)")
	for _, c := range typeEncodingCases {
		at, err := parseTypeString(c.prefix)
		if err != nil {
			t.Errorf("%-24q parse err: %v", c.prefix, err)
			continue
		}
		if !atSame(at, c.want) {
			t.Errorf("%-24q\n  got  %s\n  want %s", c.prefix, dumpAT(at), dumpAT(c.want))
		}
	}
}
