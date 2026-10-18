package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/knusbaum/gbasm"
)

func packageImportFixture(t *testing.T, pkg string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), pkg+".bo")
	obj, err := gbasm.NewOFile(path, pkg)
	if err != nil {
		t.Fatal(err)
	}
	obj.Structs["Item"] = &gbasm.StructShape{Name: "Item", IsPub: true, Fields: []gbasm.FieldShape{{Name: "x", Type: "i64"}}}
	obj.TypeAliases["Num"] = &gbasm.TypeAliasShape{Name: "Num", IsPub: true, Underlying: "i64"}
	obj.Funcs["get"] = &gbasm.Function{Name: "get", IsPub: true, Type: "fn() i64"}
	obj.Vars["VALUE"] = &gbasm.Var{Name: "VALUE", IsPub: true, VType: "i64", Val: make([]byte, 8)}
	if err := obj.Output(); err != nil {
		t.Fatal(err)
	}
	return path
}

func compilePackageForTest(files []string, imports map[string]string) (string, error) {
	sources := make([]packageSource, len(files))
	for i, src := range files {
		sources[i] = packageSource{name: string(rune('a'+i)) + ".bos", reader: strings.NewReader(src)}
	}
	var out bytes.Buffer
	err := compilePackage(&out, sources, imports)
	return out.String(), err
}

func TestMultiFilePackage(t *testing.T) {
	imports := map[string]string{"alpha": packageImportFixture(t, "alpha"), "beta": packageImportFixture(t, "beta")}
	tests := []struct {
		name    string
		files   []string
		wantErr string
		want    []string
	}{
		{name: "cross file types globals constructors and calls", files: []string{
			`package sample
fn main() i64 { p := Point.new(); return p.read() + count }
fn borrow(p *Point) *Point { return identity(p) }`,
			`package sample
var count i64 := 3
type Point struct { x i64 } {
 new() Point { return Point{x: 42} }
 read(p *Point) i64 { return p.x }
}
fn identity(p *Point) *Point { return p }`,
		}, want: []string{"function Point.new", "call sample.Point.new", "function borrow"}},
		{name: "file local aliases and callee scope", files: []string{
			`package sample
import dep "alpha"
fn first() i64 { return dep.get() }
fn item() dep.Item { return dep.Item{x: 1} }
fn value() i64 { dep.VALUE = 2; p := &dep.VALUE; return *p }`,
			`package sample
import dep "beta"
fn second() i64 { return first() + dep.get() + item().x }`,
		}, want: []string{"call alpha.get", "call beta.get", "alpha.VALUE"}},
		{name: "aliased type cast", files: []string{
			`package sample
import a "alpha"
fn number() a.Num { return a.Num(42) }`,
			`package sample
import b "alpha"
fn use() b.Num { return number() }`,
		}},
		{name: "different aliases same type", files: []string{
			`package sample
import a "alpha"
fn identity(p *a.Item) *a.Item { return p }`,
			`package sample
import b "alpha"
fn use(p *b.Item) i64 { return identity(p).x }`,
		}},
		{name: "inferred imported type without import", files: []string{
			`package sample
import "alpha"
fn item() alpha.Item { return alpha.Item{x: 7} }`,
			`package sample
fn use() i64 { v := item(); return v.x }`,
		}},
		{name: "nested types and open slice bounds", files: []string{
			`package sample
import a "alpha"
type Holder struct { items []*a.Item }
interface Reader { read(r *self, h *Holder) *a.Item }
fn head(xs []*a.Item) *a.Item { return xs[:][0] }`,
			`package sample
import b "alpha"
fn use(xs []*b.Item) i64 { return head(xs).x }`,
		}},
		{name: "shared literals and values metadata", files: []string{
			`package sample
fn first() []byte { return "shared" }
fn cat() Cat { return Cat.RED{lives: 9} }`,
			`package sample
type Cat values { RED{lives i64}, BLUE }
fn second() []byte { return "shared" }`,
		}},
		{name: "source location in sibling", files: []string{
			"package sample\nfn first() {}",
			"package sample\nfn bad() i64 { return missing() }",
		}, wantErr: "at b.bos:2:"},
		{name: "imported cast not visible in sibling", files: []string{
			`package sample
import "alpha"`,
			`package sample
fn bad() i64 { x := alpha.Item(0); return x.x }`,
		}, wantErr: `No such function "alpha.Item"`},
		{name: "global import not visible in sibling", files: []string{
			`package sample
import "alpha"`,
			`package sample
fn bad() i64 { return alpha.VALUE }`,
		}, wantErr: `Variable "alpha" undeclared`},
		{name: "alias not visible in sibling", files: []string{
			`package sample
import dep "alpha"`,
			`package sample
fn bad() i64 { return dep.get() }`,
		}, wantErr: `No such function "dep.get"`},
		{name: "import not visible in sibling", files: []string{
			`package sample
import "alpha"
fn good() i64 { return alpha.get() }`,
			`package sample
fn bad() i64 { return alpha.get() }`,
		}, wantErr: `No such function "alpha.get"`},
		{name: "type import not visible in sibling", files: []string{
			`package sample
import "alpha"`,
			`package sample
fn bad(x *alpha.Item) i64 { return x.x }`,
		}, wantErr: `package "alpha" is not imported in this file`},
		{name: "literal import not visible in sibling", files: []string{
			`package sample
import "alpha"`,
			`package sample
fn bad() i64 { x := alpha.Item{x: 1}; return x.x }`,
		}, wantErr: `package "alpha" is not imported in this file`},
		{name: "alias does not expose canonical package name", files: []string{
			`package sample
import a "alpha"
fn bad() i64 { return alpha.get() }`,
		}, wantErr: `No such function "alpha.get"`},
		{name: "duplicate import alias", files: []string{
			`package sample
import dep "alpha"
import dep "beta"`,
		}, wantErr: `import name "dep" already defined`},
		{name: "package mismatch", files: []string{"package first\n", "package second\n"}, wantErr: `package "second" differs from package "first"`},
		{name: "missing package", files: []string{"// comment\n"}, wantErr: "missing package declaration"},
		{name: "duplicate globals", files: []string{"package sample\nvar x i64 := 1", "package sample\nvar x i64 := 2"}, wantErr: "already"},
		{name: "duplicate functions", files: []string{"package sample\nfn f() {}", "package sample\nfn f() {}"}, wantErr: `Function "f" already defined`},
		{name: "duplicate types", files: []string{"package sample\ntype T struct { x i64 }", "package sample\ntype T struct { y i64 }"}, wantErr: `Struct "T" already defined`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := compilePackageForTest(tt.files, imports)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("want %q, got %v", tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if strings.Count(out, "package sample\n") != 1 {
				t.Fatalf("expected one package directive:\n%s", out)
			}
			for _, want := range tt.want {
				if !strings.Contains(out, want) {
					t.Fatalf("missing %q in:\n%s", want, out)
				}
			}
			reversed := append([]string(nil), tt.files...)
			for i, j := 0, len(reversed)-1; i < j; i, j = i+1, j-1 {
				reversed[i], reversed[j] = reversed[j], reversed[i]
			}
			if _, err := compilePackageForTest(reversed, imports); err != nil {
				t.Fatalf("reversed file order: %v", err)
			}
		})
	}
}

func TestImportedSignatureDependenciesStayHidden(t *testing.T) {
	alpha := packageImportFixture(t, "alpha")
	provider := filepath.Join(t.TempDir(), "provider.bo")
	obj, err := gbasm.NewOFile(provider, "provider")
	if err != nil {
		t.Fatal(err)
	}
	obj.Funcs["get"] = &gbasm.Function{Name: "get", IsPub: true, Type: "fn() alpha.Item"}
	if err := obj.Output(); err != nil {
		t.Fatal(err)
	}
	imports := map[string]string{"provider": provider, "hidden/alpha": alpha}
	for _, tt := range []struct{ name, body, want string }{
		{"inferred dependency type", `fn use() i64 { return provider.get().x }`, ""},
		{"hidden dependency qualifier", `fn use() i64 { return alpha.get() }`, `No such function "alpha.get"`},
		{"hidden dependency type name", `fn use(x *alpha.Item) i64 { return x.x }`, `package "alpha" is not imported in this file`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := compilePackageForTest([]string{"package sample\nimport \"provider\"\n" + tt.body}, imports)
			if tt.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("want %q, got %v", tt.want, err)
			}
		})
	}
}

func TestPackageDiagnosticPhases(t *testing.T) {
	for _, tt := range []struct{ body, phase string }{
		{"fn f( {", "Parse Error"},
		{"fn f() { 1 }", "Failed to parse"},
	} {
		_, err := compilePackageForTest([]string{"package sample\n" + tt.body}, nil)
		stage, ok := err.(*packageCompileError)
		if !ok || stage.phase != tt.phase {
			t.Fatalf("want phase %q, got %v", tt.phase, err)
		}
	}
}
