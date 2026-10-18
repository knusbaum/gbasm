package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/knusbaum/gbasm"
)

func TestImportedStaticNewCall(t *testing.T) {
	path := filepath.Join(t.TempDir(), "factory.bo")
	obj, err := gbasm.NewOFile(path, "factory")
	if err != nil {
		t.Fatal(err)
	}
	obj.Structs["Builder"] = &gbasm.StructShape{Name: "Builder", IsPub: true, Fields: []gbasm.FieldShape{{Name: "x", Type: "i64"}}, MethodNames: []string{"new"}}
	obj.Funcs["Builder.new"] = &gbasm.Function{Name: "Builder.new", IsPub: true, Type: "fn() owned *mut owned Builder"}
	if err := obj.Output(); err != nil {
		t.Fatal(err)
	}
	for _, qualifier := range []string{"factory", "f"} {
		importLine := `import "factory"`
		if qualifier != "factory" {
			importLine = `import f "factory"`
		}
		src := `package main
` + importLine + `
fn main() i64 {
 b := ` + qualifier + `.Builder.new()
 n := b.x
 free(b)
 return n
}`
		out, err := compilePackageForTest([]string{src}, map[string]string{"factory": path})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "call factory.Builder.new") {
			t.Fatalf("missing qualified constructor call:\n%s", out)
		}
	}
}

func TestExpressionReceiverIntrinsicNames(t *testing.T) {
	for _, name := range []string{"new", "free", "len", "cap", "append", "pop"} {
		t.Run(name, func(t *testing.T) {
			src := `package main
type Factory struct { x i64 } {
 ` + name + `(self *Factory) i64 { return self.x }
}
fn identity(f *Factory) *Factory { return f }
fn call(f *Factory) i64 { return identity(f).` + name + `() }
fn main() i64 { return 0 }
`
			if _, err := compileBosonSourceForTest(src); err != nil {
				t.Fatal(err)
			}
		})
	}
}
