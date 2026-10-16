package main

import (
	"strings"
	"testing"
)

func TestZeroTestOperands(t *testing.T) {
	const declarations = `interface K { get(s *self) i64 }
 type Holder struct { k ?K }
 `
	tests := []struct{ name, body, wantErr string }{
		{"nullable interface", `fn use(k ?K) { if (k) { _ := k.get() } }`, ""},
		{"nonnullable interface", `fn use(k K) { if (k) {} }`, ""},
		{"interface field", `fn use(h Holder) { if (h.k) { _ := h.k.get() } }`, ""},
		{"negated interface", `fn use(k ?K) { if (!k) {} else { _ := k.get() } }`, ""},
		{"standalone negation", `fn use(k ?K) { _ := !k }`, ""},
		{"logical left operand", `fn use(k ?K) { if (k && true) {} }`, ""},
		{"logical right operand", `fn use(k ?K) { if (false || k) {} }`, ""},
		{"standalone logical expression", `fn use(k ?K) { _ := true && k }`, ""},
		{"for condition", `fn use(k ?K) { for (; k; ) { _ := k.get(); break } }`, ""},
		{"negated interface field", `fn use(h Holder) { if (!h.k) {} else { _ := h.k.get() } }`, ""},
		{"field narrowing invalidated by assignment", `fn use(h Holder) {
   var copy := h
   if (copy.k) { copy.k = nil; _ := copy.k.get() }
  }`, "before calling get"},
		{"narrowed field can be reset", `fn use(h Holder) {
   var copy := h
   if (copy.k) { copy.k = nil }
  }`, ""},
		{"narrowing does not escape branch", `fn use(k ?K) { if (k) {} ; _ := k.get() }`, "nullable interface"},
		{"unnarrowed dispatch still rejected", `fn use(k ?K) { _ := k.get() }`, "nullable interface"},
		{"truthiness is not bool assignment", `fn use(k ?K) { b bool := k; _ := b }`, "Cannot initialize"},
		{"pointer to interface", `fn use(p ?*K) { if (p) {} }`, ""},
		{"slice condition", `fn use(s []byte) { if (s) {} }`, "Condition requires scalar, pointer, or interface operand"},
		{"struct condition", `fn use(h Holder) { if (h) {} }`, "Condition requires scalar, pointer, or interface operand"},
		{"float condition", `fn use(f f64) { if (f) {} }`, "Condition requires scalar, pointer, or interface operand"},
		{"explicit interface comparison", `fn use(k ?K) { if (k != nil) { _ := k.get() } }`, ""},
		{"negated explicit comparison", `fn use(k ?K) { if (!(k == nil)) {} }`, ""},
		{"pointer condition", `fn use(p ?*i64) { if (p) { _ := *p } }`, ""},
		{"scalar conditions", `fn use(n i64, b bool) { if (n) {} ; if (!b || b) {} }`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := compileBosonSourceForTest(matrixSource(declarations + tt.body))
			if tt.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}
