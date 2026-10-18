package main

import (
	"strings"
	"testing"
)

func TestNewReturnOwnedPointeeFields(t *testing.T) {
	for _, tt := range []struct {
		name, fields string
		reject       bool
	}{
		{"missing owned field", "", true},
		{"initialized owned field", "buffer: make([]mut byte, 0)", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			src := `package main
type E struct { buffer owned []mut byte }
fn construct() owned *mut owned E { return new(E{` + tt.fields + `}) }
fn main() i64 { return 0 }
`
			_, err := compileBosonSourceForTest(src)
			if tt.reject {
				if err == nil || !strings.Contains(err.Error(), "Missing owned field E.buffer in owned struct literal") {
					t.Fatalf("expected missing owned field diagnostic, got %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}
