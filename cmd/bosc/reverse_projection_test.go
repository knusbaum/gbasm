package main

import (
	"strings"
	"testing"
)

func TestReverseProjectionDiagnostics(t *testing.T) {
	tests := []struct{ name, declaration, body, want string }{
		{"duplicate numbers", `type E values (i64) { A: 1; B: 1 }`, `fn f() ?E { return ?E(1) }`, "cases A and B have the same projected value"},
		{"duplicate strings", `type E values ([]byte) { A: "same"; B: "same" }`, `fn f() ?E { return ?E("same") }`, "not injective"},
		{"signed zero", `type E values (f64) { A: 0.0; B: -0.0 }`, `fn f() ?E { return ?E(0.0) }`, "not injective"},
		{"ambiguous literal", `type E values (i8, i64) { A: 1, 2 }`, `fn f() ?E { return ?E(1) }`, "ambiguous projection"},
		{"no projection", `type E values ([]byte) { A: "one" }`, `fn f() ?E { return ?E(1) }`, "no matching projection"},
		{"not a values type", `type E values (i64) { A: 1 }`, `fn f() ?i64 { return ?i64(1) }`, "requires a values type"},
		{"unchecked forward", `type E values (i64) { A: 1 }`, `fn f(n i64) i64 { x := ?E(n); return i64(x) }`, "check for nil first"},
		{"unchecked return", `type E values (i64) { A: 1 }`, `fn f(n i64) E { return ?E(n) }`, "Cannot return"},
		{"unchecked identity", `type E values (i64) { A: 1 }`, `fn f(n i64) E { x := ?E(n); return E(x) }`, "check for nil first"},
		{"truthiness", `type E values (i64) { A: 1 }`, `fn f(n i64) i64 { x := ?E(n); if(x) { return 1 }; return 0 }`, "explicit comparison with nil"},
		{"assignment loses narrowing", `type E values (i64) { A: 1 }`, `fn f(n i64) i64 { var x := ?E(n); if(x != nil) { x = ?E(2); return i64(x) }; return 0 }`, "check for nil first"},
		{"forward duplicates remain valid", `type E values (i64) { A: 1; B: 1 }`, `fn f() i64 { return i64(E.B) }`, ""},
		{"narrowed", `type E values (i64) { A: 1 }`, `fn f(n i64) i64 { x := ?E(n); if(x != nil) { return i64(x) }; return 0 }`, ""},
		{"asserted", `type E values (i64) { A: 1 }`, `fn f(n i64) i64 { x := ?E(n); return i64(x?) }`, ""},
		{"nullable identity", `type E values (i64) { A: 1 }`, `fn f(n i64) ?E { x := ?E(n); return ?E(x) }`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := compilePackageForTest([]string{"package main\n" + tt.declaration + "\n" + tt.body}, nil)
			if tt.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("got %v, want %q", err, tt.want)
			}
		})
	}
}
