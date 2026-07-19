package main

import (
	"fmt"
	"testing"
)

// This is the before/after oracle for the postfix→prefix type-syntax migration
// (DESIGN_prefix_type_syntax.md). Each case pairs a postfix form (today) with
// its target prefix form and the exact ASTType both must produce — captured
// from the current parser so a mask/nesting bug can't slip through the rewrite.
//
//   - TestTypeEncodingPostfix: the *current* parser must produce `want` for the
//     postfix form. Locks today's behavior; flips to reject-postfix once the
//     parser goes prefix-only.
//   - TestTypeEncodingPrefix: the *new* parser must produce the SAME `want` for
//     the prefix form. Fails until parseTypeName is rewritten — the TDD target.

// dumpAT renders an ASTType's full structure (every mask bit, nesting).
func dumpAT(t ASTType) string {
	s := fmt.Sprintf("Name=%q Ind=%d Arr=%d Mut=%b Own=%b Nil=%b Signed=%v",
		t.Name, t.Indirection, t.ArraySize, t.MutMask, t.OwnedMask, t.NilMask, t.Signed)
	if t.Element != nil {
		s += " Elem{" + dumpAT(*t.Element) + "}"
	}
	return s
}

var typeEncodingCases = []struct{ postfix, prefix, want string }{
	{"i64", "i64", `Name="i64" Ind=0 Arr=0 Mut=0 Own=0 Nil=0 Signed=true`},
	{"byte", "byte", `Name="byte" Ind=0 Arr=0 Mut=0 Own=0 Nil=0 Signed=false`},
	{"bool", "bool", `Name="bool" Ind=0 Arr=0 Mut=0 Own=0 Nil=0 Signed=false`},
	{"byte[]", "[]byte", `Name="" Ind=0 Arr=0 Mut=0 Own=0 Nil=0 Signed=false Elem{Name="byte" Ind=0 Arr=0 Mut=0 Own=0 Nil=0 Signed=false}`},
	{"i64[]", "[]i64", `Name="" Ind=0 Arr=0 Mut=0 Own=0 Nil=0 Signed=false Elem{Name="i64" Ind=0 Arr=0 Mut=0 Own=0 Nil=0 Signed=true}`},
	{"byte[8]", "[8]byte", `Name="" Ind=0 Arr=8 Mut=0 Own=0 Nil=0 Signed=false Elem{Name="byte" Ind=0 Arr=0 Mut=0 Own=0 Nil=0 Signed=false}`},
	{"i64[3]", "[3]i64", `Name="" Ind=0 Arr=3 Mut=0 Own=0 Nil=0 Signed=false Elem{Name="i64" Ind=0 Arr=0 Mut=0 Own=0 Nil=0 Signed=true}`},
	{"byte[][]", "[][]byte", `Name="" Ind=0 Arr=0 Mut=0 Own=0 Nil=0 Signed=false Elem{Name="" Ind=0 Arr=0 Mut=0 Own=0 Nil=0 Signed=false Elem{Name="byte" Ind=0 Arr=0 Mut=0 Own=0 Nil=0 Signed=false}}`},
	{"byte[8][1]", "[1][8]byte", `Name="" Ind=0 Arr=1 Mut=0 Own=0 Nil=0 Signed=false Elem{Name="" Ind=0 Arr=8 Mut=0 Own=0 Nil=0 Signed=false Elem{Name="byte" Ind=0 Arr=0 Mut=0 Own=0 Nil=0 Signed=false}}`},
	{"byte[][1]", "[1][]byte", `Name="" Ind=0 Arr=1 Mut=0 Own=0 Nil=0 Signed=false Elem{Name="" Ind=0 Arr=0 Mut=0 Own=0 Nil=0 Signed=false Elem{Name="byte" Ind=0 Arr=0 Mut=0 Own=0 Nil=0 Signed=false}}`},
	{"*i64", "*i64", `Name="i64" Ind=1 Arr=0 Mut=0 Own=0 Nil=0 Signed=true`},
	{"**i64", "**i64", `Name="i64" Ind=2 Arr=0 Mut=0 Own=0 Nil=0 Signed=true`},
	{"*byte", "*byte", `Name="byte" Ind=1 Arr=0 Mut=0 Own=0 Nil=0 Signed=false`},
	{"*byte[]", "[]*byte", `Name="" Ind=0 Arr=0 Mut=0 Own=0 Nil=0 Signed=false Elem{Name="byte" Ind=1 Arr=0 Mut=0 Own=0 Nil=0 Signed=false}`},
	{"*(byte[])", "*[]byte", `Name="" Ind=1 Arr=0 Mut=0 Own=0 Nil=0 Signed=false Elem{Name="byte" Ind=0 Arr=0 Mut=0 Own=0 Nil=0 Signed=false}`},
	{"*(byte[8])", "*[8]byte", `Name="" Ind=1 Arr=8 Mut=0 Own=0 Nil=0 Signed=false Elem{Name="byte" Ind=0 Arr=0 Mut=0 Own=0 Nil=0 Signed=false}`},
	{"*mut i64", "*mut i64", `Name="i64" Ind=1 Arr=0 Mut=10 Own=0 Nil=0 Signed=true`},
	{"mut byte[]", "[]mut byte", `Name="" Ind=0 Arr=0 Mut=10 Own=0 Nil=0 Signed=false Elem{Name="byte" Ind=0 Arr=0 Mut=0 Own=0 Nil=0 Signed=false}`},
	{"owned byte[]", "[]owned byte", `Name="" Ind=0 Arr=0 Mut=0 Own=10 Nil=0 Signed=false Elem{Name="byte" Ind=0 Arr=0 Mut=0 Own=0 Nil=0 Signed=false}`},
	{"owned byte[8]", "owned [8]byte", `Name="" Ind=0 Arr=8 Mut=0 Own=1 Nil=0 Signed=false Elem{Name="byte" Ind=0 Arr=0 Mut=0 Own=0 Nil=0 Signed=false}`},
	{"owned *mut i64", "owned *mut i64", `Name="i64" Ind=1 Arr=0 Mut=10 Own=1 Nil=0 Signed=true`},
	{"owned *owned mut node", "owned *owned mut node", `Name="node" Ind=1 Arr=0 Mut=10 Own=11 Nil=0 Signed=false`},
	{"*?i64", "?*i64", `Name="i64" Ind=1 Arr=0 Mut=0 Own=0 Nil=1 Signed=true`},
	{"*?*i64", "?**i64", `Name="i64" Ind=2 Arr=0 Mut=0 Own=0 Nil=1 Signed=true`},
	{"owned *?owned mut node", "owned ?*owned mut node", `Name="node" Ind=1 Arr=0 Mut=10 Own=11 Nil=1 Signed=false`},
}

func TestTypeEncodingPostfix(t *testing.T) {
	for _, c := range typeEncodingCases {
		at, err := parseTypeString(c.postfix)
		if err != nil {
			t.Errorf("%-24q parse err: %v", c.postfix, err)
			continue
		}
		if got := dumpAT(at); got != c.want {
			t.Errorf("%-24q\n  got  %s\n  want %s", c.postfix, got, c.want)
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
		if got := dumpAT(at); got != c.want {
			t.Errorf("%-24q\n  got  %s\n  want %s", c.prefix, got, c.want)
		}
	}
}
