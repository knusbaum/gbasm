package main

import (
	"strings"
	"testing"
)

func TestInterfaceDiagnosticAfterMatchingMethod(t *testing.T) {
	for _, tt := range []struct{ name, method, want string }{
		{"receiver mismatch", "destroy(e owned E) { dispose(e) }", "method destroy receiver has type owned E, expected *E or *mut E"},
		{"missing method", "", "missing method destroy"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			src := `package main
interface BuiltError {
 message(e *mut self) []byte
 destroy(e owned *self)
}
type E struct { x i64 } {
 message(_ *mut E) []byte { return "error" }
 ` + tt.method + `
}
fn coerce(e owned *mut E) owned BuiltError { return e }
fn main() i64 { return 0 }
`
			_, err := compileBosonSourceForTest(src)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("expected diagnostic containing %q, got %v", tt.want, err)
			}
		})
	}
}
