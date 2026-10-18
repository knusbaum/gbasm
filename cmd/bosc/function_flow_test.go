package main

import "testing"

func TestFunctionPointerLifetimeIsolation(t *testing.T) {
	src := `package main
fn destroy(self owned *i64) { free(self) }
fn read(self *i64) i64 { return *self }
fn main() i64 { return 0 }
`
	if _, err := compileBosonSourceForTest(src); err != nil {
		t.Fatalf("lifetime facts leaked between functions: %v", err)
	}
}
