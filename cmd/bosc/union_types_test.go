package main

import (
	"strings"
	"testing"
)

func TestUnionTypeSyntaxAndIdentity(t *testing.T) {
	for _, pair := range [][2]string{
		{"i64 | bool", "bool | i64"},
		{"i64 | (bool | f64)", "f64 | bool | i64"},
		{"owned *mut T | error", "error | owned *mut T"},
		{"(fn() i64) | bool", "bool | (fn() i64)"},
	} {
		a, err := parseTypeString(pair[0])
		if err != nil {
			t.Fatal(err)
		}
		b, err := parseTypeString(pair[1])
		if err != nil {
			t.Fatal(err)
		}
		if !a.Same(b) {
			t.Fatalf("%s != %s", a, b)
		}
		round, err := parseTypeString(a.String())
		if err != nil || !a.Same(round) {
			t.Fatalf("roundtrip %s: %v", a, err)
		}
	}
	a, err := parseTypeString("fn() i64 | bool")
	if err != nil {
		t.Fatal(err)
	}
	if a.IsUnion() || a.FuncSig == nil || !a.FuncSig.Return.IsUnion() {
		t.Fatalf("function-return precedence: %#v", a)
	}
	b, _ := parseTypeString("(fn() i64) | bool")
	if !b.IsUnion() {
		t.Fatal("grouped function must be an alternative")
	}
}

func TestUnionTypeRejectsAmbiguousShapesAndWrappers(t *testing.T) {
	for _, src := range []string{"i64 | i64", "*T | owned *T", "*T | *mut T", "*T | ?*T", "i64 | (bool | i64)", "*(i64 | bool)", "[](i64 | bool)", "[2](i64 | bool)", "owned (i64 | bool)", "?(i64 | bool)", "void | i64"} {
		t.Run(src, func(t *testing.T) {
			if _, err := parseTypeString(src); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}

func TestUnionCanonicalEncoding(t *testing.T) {
	if got := canonicalTypeNode(numASTType(), false); got != "5:named0007:builtin3:i64" {
		t.Fatalf("encoding: %q", got)
	}
	a, _ := parseTypeString("owned *mut pkg.T")
	b, _ := parseTypeString("*pkg.T")
	if canonicalTypeNode(a, false) == canonicalTypeNode(b, false) {
		t.Fatal("lost qualifiers")
	}
	if canonicalTypeNode(a, true) != canonicalTypeNode(b, true) {
		t.Fatal("shape retained qualifiers")
	}
	c, _ := parseTypeString("*other.T")
	if canonicalTypeNode(b, false) == canonicalTypeNode(c, false) {
		t.Fatal("lost package identity")
	}
}

func TestUnionDoesNotBorrowByDroppingOwnership(t *testing.T) {
	a, _ := parseTypeString("owned *mut T | error")
	if !a.HasOwned() || !a.StripOwned().Same(a) {
		t.Fatal("union container became a borrowed view")
	}
	if a.ZeroInitializable(nil) {
		t.Fatal("union acquired a default alternative")
	}
}

func TestUnionSourceImportQualification(t *testing.T) {
	a, _ := parseTypeString("owned *mut alias.T | i64")
	scope := &sourceScope{imports: map[string]string{"alias": "pkg"}}
	if err := scope.qualifyType(&a); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(a.String(), "pkg.T") {
		t.Fatalf("not qualified: %s", a)
	}
}

func TestUnionAliasChainsRemainStructural(t *testing.T) {
	c := NewContext()
	u, err := parseTypeString("bool | i64")
	if err != nil {
		t.Fatal(err)
	}
	c.DefineTypeAlias(position{}, "Result", u)
	c.DefineTypeAlias(position{}, "Alias", ASTType{Name: "Result"})
	if got := resolveUnionAliases(c, ASTType{Name: "Alias"}); !got.Same(u) {
		t.Fatalf("union alias became nominal: %s", got)
	}
	c.DefineTypeAlias(position{}, "Number", numASTType())
	if got := resolveUnionAliases(c, ASTType{Name: "Number"}); got.Name != "Number" {
		t.Fatal("erased an ordinary nominal payload type")
	}
}

func TestUnionAliasInFunctionSignature(t *testing.T) {
	c := NewContext()
	u, _ := parseTypeString("i64|bool")
	c.DefineTypeAlias(position{}, "Result", u)
	f, _ := parseTypeString("fn(Result) Result")
	resolved := resolveUnionAliases(c, f)
	if !resolved.FuncSig.Args[0].Same(u) || !resolved.FuncSig.Return.Same(u) {
		t.Fatalf("unresolved nested union alias: %s", resolved)
	}
}

func TestUnionNestedSliceQualifiersRoundTrip(t *testing.T) {
	for _, src := range []string{"[]?*i64 | bool", "[]*owned i64 | bool", "[]*mut i64 | bool", "[]?*owned mut *i64 | bool"} {
		a, err := parseTypeString(src)
		if err != nil {
			t.Fatal(err)
		}
		b, err := parseTypeString(a.String())
		if err != nil {
			t.Fatal(err)
		}
		// Compare the actual mask representation, not only the canonical encoder,
		// so a shared bug in encoding and rendering cannot hide the lost bits.
		if len(a.Union) != len(b.Union) {
			t.Fatal("lost alternatives")
		}
		for i := range a.Union {
			if a.Union[i].OwnedMask != b.Union[i].OwnedMask || a.Union[i].MutMask != b.Union[i].MutMask || a.Union[i].NilMask != b.Union[i].NilMask {
				t.Fatalf("qualifiers changed in %s -> %s", src, a.String())
			}
		}
	}
	a, _ := parseTypeString("[]?*i64 | bool")
	b, _ := parseTypeString("[]*i64 | bool")
	if a.Same(b) {
		t.Fatal("nullable pointer elements became non-null through union identity")
	}
}
