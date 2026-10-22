package arm64

import (
	"strings"
	"testing"

	"github.com/knusbaum/gbasm"
)

func TestNativeLowering(t *testing.T) {
	o, err := gbasm.NewOFile("test.bo", "p")
	if err != nil {
		t.Fatal(err)
	}
	o.Vars["count"] = &gbasm.Var{Name: "count", Val: make([]byte, 8)}
	src, err := Lower(o, map[string][]string{"T.method": {
		"argi a 0 64", "prologue", "local n 64", "bytes buf 24 x19",
		"mov n #42", "mov count n", "lea x0 [buf+8]", "evict",
		"cmp n 42", "je done", "mov w0 #1", "label done", "epilogue", "ret",
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{".globl _p.T.method", "adrp x17, _p.count@PAGE", "str x0, [x17]", "b.eq LT.method_done", "stp x27, x28", "ret\n"} {
		if !strings.Contains(src, want) {
			t.Errorf("missing %q in\n%s", want, src)
		}
	}
	if strings.Contains(src, "str x19, [x17]") {
		t.Fatal("spilled a bytes address over its payload")
	}
}
func TestVolatileCannotBePinned(t *testing.T) {
	o, _ := gbasm.NewOFile("test.bo", "p")
	_, err := Lower(o, map[string][]string{"f": {"local n 64", "prologue", "volatile n", "inreg n x0", "epilogue", "ret"}})
	if err == nil || !strings.Contains(err.Error(), "volatile") {
		t.Fatalf("volatile invariant: %v", err)
	}
}

func TestNativeLabelsAndComments(t *testing.T) {
	o, _ := gbasm.NewOFile("native.bo", "p")
	text, err := Lower(o, map[string][]string{"f": {"mov x0, #42 // result", "loop:", "cbnz x0, loop", "b.eq loop", "ret x30"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Lf_loop:", "cbnz x0, Lf_loop", "b.eq Lf_loop", "ret x30"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in %s", want, text)
		}
	}
}
