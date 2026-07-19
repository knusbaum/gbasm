package main

import (
	"fmt"
	"testing"
)

// Correctness tests for prefix type-syntax encoding
// (DESIGN_prefix_type_syntax.md). Each case pairs a type spelling with the exact
// ASTType it must produce, written as a real ASTType literal so field names are
// compile-checked.
//
//   - TestTypeEncodingPrefix: parseTypeString(spelling) must equal `want`.
//   - TestTypeEncodingRoundTrip: parse → String() → re-parse must still equal
//     `want` (the `.bo` serialization round-trip).
//   - TestTypeEncodingTopLevelMutOmitted: String() omits a binding-level `mut`.

// atSame is a strict, position-insensitive, recursive ASTType equality. Stricter
// than SameExact (which recurses into Element with the laxer Same) and it skips
// the unexported position field that reflect.DeepEqual would choke on.
func atSame(a, b ASTType) bool {
	if a.Name != b.Name || a.Indirection != b.Indirection || a.ArraySize != b.ArraySize ||
		a.MutMask != b.MutMask || a.OwnedMask != b.OwnedMask || a.NilMask != b.NilMask ||
		a.Signed != b.Signed || a.MultiReturn != b.MultiReturn {
		return false
	}
	if (a.Element == nil) != (b.Element == nil) {
		return false
	}
	if a.Element != nil && !atSame(*a.Element, *b.Element) {
		return false
	}
	if (a.FuncSig == nil) != (b.FuncSig == nil) {
		return false
	}
	if a.FuncSig != nil {
		if len(a.FuncSig.Args) != len(b.FuncSig.Args) || !atSame(a.FuncSig.Return, b.FuncSig.Return) {
			return false
		}
		for i := range a.FuncSig.Args {
			if !atSame(a.FuncSig.Args[i], b.FuncSig.Args[i]) {
				return false
			}
		}
	}
	if len(a.AnonFields) != len(b.AnonFields) {
		return false
	}
	for i := range a.AnonFields {
		if a.AnonFields[i].Name != b.AnonFields[i].Name || !atSame(a.AnonFields[i].Type, b.AnonFields[i].Type) {
			return false
		}
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

// sl builds a slice ASTType {[]elem}; ar builds an array {[n]elem}. Only used to
// keep the nested `want` literals readable — they wrap a given element verbatim,
// they do NOT re-derive the mask "bump", so they can't mask a parser bug (the
// masks are written out explicitly on each `want`).
func sl(elem ASTType) ASTType     { return ASTType{Element: &elem} }
func ar(n int, e ASTType) ASTType { return ASTType{ArraySize: n, Element: &e} }

var typeEncodingCases = []struct {
	prefix string
	want   ASTType
}{
	{"i64", ASTType{Name: "i64", Signed: true}},
	{"byte", ASTType{Name: "byte"}},
	{"bool", ASTType{Name: "bool"}},
	{"[]byte", sl(ASTType{Name: "byte"})},
	{"[]i64", sl(ASTType{Name: "i64", Signed: true})},
	{"[8]byte", ar(8, ASTType{Name: "byte"})},
	{"[3]i64", ar(3, ASTType{Name: "i64", Signed: true})},
	{"[][]byte", sl(sl(ASTType{Name: "byte"}))},
	{"[1][8]byte", ar(1, ar(8, ASTType{Name: "byte"}))},
	{"[1][]byte", ar(1, sl(ASTType{Name: "byte"}))},
	{"*i64", ASTType{Name: "i64", Indirection: 1, Signed: true}},
	{"**i64", ASTType{Name: "i64", Indirection: 2, Signed: true}},
	{"*byte", ASTType{Name: "byte", Indirection: 1}},
	{"[]*byte", sl(ASTType{Name: "byte", Indirection: 1})},
	{"*[]byte", ASTType{Indirection: 1, Element: &ASTType{Name: "byte"}}},
	{"*[8]byte", ASTType{Indirection: 1, ArraySize: 8, Element: &ASTType{Name: "byte"}}},
	{"*mut i64", ASTType{Name: "i64", Indirection: 1, MutMask: 0b10, Signed: true}},
	{"[]mut byte", ASTType{MutMask: 0b10, Element: &ASTType{Name: "byte"}}},
	{"[]owned byte", ASTType{OwnedMask: 0b10, Element: &ASTType{Name: "byte"}}},
	{"owned [8]byte", ASTType{ArraySize: 8, OwnedMask: 0b1, Element: &ASTType{Name: "byte"}}},
	{"owned *mut i64", ASTType{Name: "i64", Indirection: 1, MutMask: 0b10, OwnedMask: 0b1, Signed: true}},
	{"owned *owned mut node", ASTType{Name: "node", Indirection: 1, MutMask: 0b10, OwnedMask: 0b11}},
	{"?*i64", ASTType{Name: "i64", Indirection: 1, NilMask: 0b1, Signed: true}},
	{"?**i64", ASTType{Name: "i64", Indirection: 2, NilMask: 0b1, Signed: true}},
	{"owned ?*owned mut node", ASTType{Name: "node", Indirection: 1, MutMask: 0b10, OwnedMask: 0b11, NilMask: 0b1}},

	// Arrays of pointers: owned/mut collapse onto the shared array level, but a
	// pointer element's nullability stays on the ELEMENT (an array is never
	// nullable). Regression: arrayWrap was moving nil to the array too.
	{"[3]?*i64", ASTType{ArraySize: 3, Element: &ASTType{Name: "i64", Indirection: 1, NilMask: 0b1, Signed: true}}},
	{"[3]*mut i64", ASTType{ArraySize: 3, MutMask: 0b10, Element: &ASTType{Name: "i64", Indirection: 1, Signed: true}}},
	{"[3]owned *i64", ASTType{ArraySize: 3, OwnedMask: 0b1, Element: &ASTType{Name: "i64", Indirection: 1, Signed: true}}},

	// String() branches the primitive cases above never exercise, but that the
	// runtime `.bo`s serialize (io.bos: `?error`, `owned FD`, fn-typed values):
	// a nullable-interface base (NilMask&1 at Indirection 0), a function type,
	// and an anonymous struct type. These gate the runtime-bundle rebuild.
	{"?error", ASTType{Name: "error", NilMask: 0b1}},
	{"fn([]byte)i64", ASTType{FuncSig: &FuncSig{
		Args:   []ASTType{{Element: &ASTType{Name: "byte"}}},
		Return: ASTType{Name: "i64", Signed: true},
	}}},
	{"struct{a:i64,b:[]byte}", ASTType{AnonFields: []Binding{
		{Name: "a", Type: ASTType{Name: "i64", Signed: true}},
		{Name: "b", Type: ASTType{Element: &ASTType{Name: "byte"}}},
	}}},
}

func TestTypeEncodingPrefix(t *testing.T) {
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

// TestTypeEncodingRoundTrip locks ASTType.String() as the exact inverse of the
// parser: parse the prefix form, render it back with String(), re-parse, and the
// result must still be `want`. This is the `.bo` serialization round-trip.
func TestTypeEncodingRoundTrip(t *testing.T) {
	for _, c := range typeEncodingCases {
		at, err := parseTypeString(c.prefix)
		if err != nil {
			t.Errorf("%-24q parse err: %v", c.prefix, err)
			continue
		}
		rendered := at.String()
		back, err := parseTypeString(rendered)
		if err != nil {
			t.Errorf("%-24q -> String()=%q re-parse err: %v", c.prefix, rendered, err)
			continue
		}
		if !atSame(back, c.want) {
			t.Errorf("%-24q -> String()=%q\n  got  %s\n  want %s", c.prefix, rendered, dumpAT(back), dumpAT(c.want))
		}
	}
}

// A leading bit-0 `mut` is the binding's mutability (spelled `var`), not part of
// the type string — the parser rejects it at top level, so String() must omit it
// there while still rendering it inside a pointer/slice operand. (Regression: an
// owner type carried as `mut B` was rendering "mut B" and would not re-parse.)
func TestTypeEncodingTopLevelMutOmitted(t *testing.T) {
	cases := []struct {
		at   ASTType
		want string
	}{
		{ASTType{Name: "B", MutMask: 0b1}, "B"},                                         // mut struct owner
		{ASTType{Element: &ASTType{Name: "byte"}, MutMask: 0b1}, "[]byte"},              // mut slice value
		{ASTType{Name: "i64", Indirection: 1, MutMask: 0b10, Signed: true}, "*mut i64"}, // pointee mut stays
		{ASTType{Element: &ASTType{Name: "byte"}, MutMask: 0b10}, "[]mut byte"},         // element mut stays
	}
	for _, c := range cases {
		got := c.at.String()
		if got != c.want {
			t.Errorf("String() = %q, want %q", got, c.want)
		}
		if _, err := parseTypeString(got); err != nil {
			t.Errorf("rendered %q does not re-parse: %v", got, err)
		}
	}
}
