package main

import (
	"strings"
	"testing"
)

func TestVariantSwitchConflictPositionInForLoop(t *testing.T) {
	src := `package main
type V values { A { a owned i64 }; B { b owned i64 } }
fn main() {
 for (var i i64 := 0; i < 1; i += 1) {
  v owned V := V.A{a: owned(i64(1))}
  switch (v) {
   case A { observe(v.a) }
   case B { drop(v.b) }
  }
  dispose(v)
 }
}
fn drop(x owned i64) { dispose(x) }
fn observe(x i64) { _ = x }
`
	_, err := compileBosonSourceForTest(src)
	if err == nil || !strings.Contains(err.Error(), "must either all consume") {
		t.Fatalf("expected conflicting switch diagnostic, got %v", err)
	}
	e, ok := err.(*interpreterError)
	if !ok {
		t.Fatalf("expected positioned error, got %T: %v", err, err)
	}
	if e.p.fname != "matrix.bos" || e.p.lineoff != 8 || e.p.linecharoff != 11 {
		t.Fatalf("expected conflicting case block at matrix.bos:8:11, got %v", e.p)
	}
}
