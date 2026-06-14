package main

import (
	"bufio"
	"bytes"
	"fmt"
	"strings"
	"testing"
)

type importedVarForTest struct {
	pkg  string
	name string
	typ  ASTType
}

func compileBosonSourceForTest(src string) (string, error) {
	return compileBosonSourceWithImportsForTest(src, nil)
}

func compileBosonSourceWithImportsForTest(src string, importedVars []importedVarForTest) (string, error) {
	reader := bufio.NewReader(strings.NewReader(strings.TrimSpace(src) + "\n"))

	var pkgname string
	var linesConsumed uint
	for {
		ln, isPrefix, err := reader.ReadLine()
		if err != nil {
			return "", fmt.Errorf("missing package line")
		}
		if !isPrefix {
			linesConsumed++
		}
		line := strings.TrimSpace(string(ln))
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		if !strings.HasPrefix(line, "package") {
			return "", fmt.Errorf("source must start with package line, got %q", line)
		}
		pkgname = strings.TrimSpace(strings.TrimPrefix(line, "package"))
		break
	}

	ctx := NewContext()
	ctx.SetPkgname(pkgname)
	for _, v := range importedVars {
		if ctx.imports[v.pkg] == nil {
			ctx.imports[v.pkg] = make(map[string]*FuncDecl)
		}
		ctx.DefineImportedVar(v.pkg, v.name, v.typ)
	}
	parser := NewParserAt("matrix.bos", reader, linesConsumed+1)
	var asts []AST
	for {
		n, err := parser.Next()
		if err != nil {
			return "", err
		}
		if n == nil {
			break
		}
		if n.t == n_import {
			if _, ok := ctx.imports[n.sval]; !ok {
				return "", fmt.Errorf("import %q: not registered in compile matrix test", n.sval)
			}
			continue
		}
		a, err := n.ToAST(ctx)
		if err != nil {
			return "", err
		}
		if a != nil {
			asts = append(asts, a)
		}
	}

	var out bytes.Buffer
	for _, a := range asts {
		if err := Compile(&out, ctx, a); err != nil {
			return out.String(), err
		}
	}
	return out.String(), nil
}

func matrixSource(body string) string {
	return "package main\n\n" + strings.TrimSpace(body) + "\n\nfn main() {}\n"
}

func byteSliceArrayASTType(n int) ASTType {
	elem := byteSliceASTType()
	return ASTType{Element: &elem, ArraySize: n}
}

func TestSliceProvenanceCompileMatrix(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{
			// Now legal: a borrowed-parameter return is recordable via
			// inferred return-parameter aliasing (the obligation moves to
			// the caller). See retalias.go / PROPOSAL_return_alias_inference.
			name: "borrowed parameter return allowed (return-alias inference)",
			body: `fn ok(s byte[]) byte[] {
	return s
}`,
		},
		{
			name: "global array return allowed",
			body: `var g byte[8]
fn ok() byte[] {
	return g[:]
}`,
		},
		{
			name: "local array return rejected",
			body: `fn bad() byte[] {
	local byte[8]
	return local[:]
}`,
			wantErr: "Borrowed slice escapes through return",
		},
		{
			// Now legal: alias of a borrowed param returned — recordable.
			name: "alias of borrowed param return allowed (return-alias inference)",
			body: `fn ok(s byte[]) byte[] {
	alias byte[] := s
	return alias
}`,
		},
		{
			// Now legal: assignment-alias of a borrowed param returned.
			name: "assignment alias of borrowed param return allowed (return-alias inference)",
			body: `fn ok(s byte[]) byte[] {
	var alias byte[]
	alias = s
	return alias
}`,
		},
		{
			// Now legal: sub-slice of a borrowed param returned.
			name: "subslice of borrowed param return allowed (return-alias inference)",
			body: `fn ok(s byte[]) byte[] {
	return s[1:]
}`,
		},
		{
			// Now legal: struct whose field borrows a param, returned.
			name: "struct field borrowing param return allowed (return-alias inference)",
			body: `type B struct { buf byte[] }
fn ok(s byte[]) B {
	var b B
	b.buf = s
	return b
}`,
		},
		{
			// Now legal: struct literal field borrows a param (the
			// new_builder constructor shape).
			name: "struct literal field borrowing param return allowed (return-alias inference)",
			body: `type B struct { buf byte[] }
fn ok(s byte[]) B {
	return B{buf: s}
}`,
		},
		{
			// Now legal: nested struct literal field borrows a param.
			name: "nested struct literal field borrowing param return allowed (return-alias inference)",
			body: `type B struct { buf byte[] }
type Outer struct { inner B }
fn ok(s byte[]) Outer {
	return Outer{inner: B{buf: s}}
}`,
		},
		{
			name: "nested field array slice return rejected",
			body: `type Inner struct { buf byte[8] }
type Outer struct { inner Inner }
fn bad() byte[] {
	o Outer
	return o.inner.buf[:]
}`,
			wantErr: "Borrowed slice escapes through return",
		},
		{
			name: "array of arrays element slice return rejected",
			body: `fn bad() byte[] {
	a byte[8][1]
	return a[0][:]
}`,
			wantErr: "Borrowed slice escapes through return",
		},
		{
			// Now legal: re-slice of a stored borrowed-param field.
			name: "reslice stored borrowed-param struct field return allowed (return-alias inference)",
			body: `type B struct { buf byte[] }
fn ok(s byte[]) byte[] {
	var b B
	b.buf = s
	return b.buf[:]
}`,
		},
		{
			// Now legal: re-slice of a stored borrowed-param array element.
			name: "reslice stored borrowed-param array element return allowed (return-alias inference)",
			body: `fn ok(s byte[]) byte[] {
	var arr byte[][1]
	arr[0] = s
	return arr[0][:]
}`,
		},
		{
			// Now legal: struct copy carrying a borrowed-param descendant.
			name: "struct copy with borrowed-param descendant return allowed (return-alias inference)",
			body: `type Inner struct { buf byte[] }
type Outer struct { inner Inner }
fn ok(s byte[]) Outer {
	var o Outer
	o.inner.buf = s
	o2 Outer := o
	return o2
}`,
		},
		{
			name: "global slice assignment rejected",
			body: `var g mut byte[]
fn bad(s byte[]) {
	g = s
}`,
			wantErr: "Borrowed slice escapes via global g",
		},
		{
			name: "global field assignment rejected",
			body: `type B struct { buf mut byte[] }
var g B
fn bad(s byte[]) {
	g.buf = s
}`,
			wantErr: "Borrowed slice escapes via field B.buf",
		},
		{
			name: "borrowed pointer field assignment rejected",
			body: `type B struct { buf byte[] }
fn bad(out *mut B, s byte[]) {
	out.buf = s
}`,
			wantErr: "Borrowed slice escapes via field B.buf",
		},
		{
			name: "borrowed pointer struct literal assignment rejected",
			body: `type B struct { buf byte[] }
fn bad(out *mut B, s byte[]) {
	*out = B{buf: s}
}`,
			wantErr: "Borrowed slice escapes via field B.buf",
		},
		{
			name: "global array of slices assignment rejected",
			body: `var g byte[][1]
fn bad(s byte[]) {
	g[0] = s
}`,
			wantErr: "Borrowed slice escapes via slice element",
		},
		{
			name: "global source through pointer field allowed",
			body: `type B struct { buf byte[] }
var src byte[8]
fn fill(out *mut B) {
	out.buf = src[:]
}`,
		},
		{
			name: "local struct store without escape allowed",
			body: `type B struct { buf byte[] }
fn ok(s byte[]) {
	var b B
	b.buf = s
}`,
		},
		{
			name: "clean symbol struct field overwrite clears descendant",
			body: `type Inner struct { buf byte[] }
type Outer struct { inner Inner }
fn ok(s byte[]) Outer {
	var o Outer
	o.inner.buf = s
	clean Inner
	o.inner = clean
	return o
}`,
		},
		{
			name: "clean symbol array field overwrite clears bucket",
			body: `type Inner struct { bufs byte[][1] }
fn ok(s byte[]) Inner {
	var i Inner
	i.bufs[0] = s
	clean byte[][1]
	i.bufs = clean
	return i
}`,
		},
		{
			name: "function call struct field overwrite clears descendant",
			body: `type Inner struct { buf byte[] }
type Outer struct { inner Inner }
fn clean() Inner {
	i Inner
	return i
}
fn ok(s byte[]) Outer {
	var o Outer
	o.inner.buf = s
	o.inner = clean()
	return o
}`,
		},
		{
			name: "function call array field overwrite clears bucket",
			body: `type Inner struct { bufs byte[][1] }
fn clean() byte[][1] {
	a byte[][1]
	return a
}
fn ok(s byte[]) Inner {
	var i Inner
	i.bufs[0] = s
	i.bufs = clean()
	return i
}`,
		},
		{
			// The empty-literal overwrite leaves the prior borrowed-param
			// field bytes (and facts) in place, so the returned struct still
			// borrows the param. Now legal: recordable, not rejected. The
			// non-clearing behavior still matters — it keeps the alias
			// recorded rather than silently dropped.
			name: "empty struct literal overwrite preserves borrowed-param field, return allowed",
			body: `type Inner struct { buf byte[] }
type Outer struct { inner Inner }
fn ok(s byte[]) Outer {
	var o Outer
	o.inner.buf = s
	o.inner = Inner{}
	return o
}`,
		},
		{
			// Now legal: the borrowed-param bucket survives a sibling
			// global write and is returned — recordable, not rejected.
			name: "indexed bucket with later global write borrowed-param return allowed (return-alias inference)",
			body: `var g byte[8]
fn ok(s byte[]) byte[] {
	var arr byte[][2]
	arr[0] = s
	arr[1] = g[:]
	return arr[0][:]
}`,
		},
		{
			// Now legal: nested borrowed-param bucket survives a sibling
			// global write and is returned.
			name: "nested indexed bucket with later global write borrowed-param return allowed (return-alias inference)",
			body: `type B struct { items byte[][2] }
var g byte[8]
fn ok(s byte[]) B {
	var b B
	b.items[0] = s
	b.items[1] = g[:]
	return b
}`,
		},
		{
			name: "whole array overwrite clears indexed bucket",
			body: `fn clean() byte[][2] {
	a byte[][2]
	return a
}
fn ok(s byte[]) byte[] {
	var arr byte[][2]
	arr[0] = s
	arr = clean()
	return arr[0][:]
}`,
		},
		{
			name: "nested whole array overwrite clears indexed bucket",
			body: `type B struct { items byte[][2] }
fn clean() byte[][2] {
	a byte[][2]
	return a
}
fn ok(s byte[]) B {
	var b B
	b.items[0] = s
	b.items = clean()
	return b
}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := compileBosonSourceForTest(matrixSource(tt.body))
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("compile failed: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("compile succeeded, want error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestSliceProvenanceSourceMatrix(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{
			name: "string literal source may be returned",
			body: `fn ok() byte[] {
	return "static"
}`,
		},
		{
			name: "global slice alias may be returned",
			body: `var g byte[8]
fn ok() byte[] {
	s byte[] := g[:]
	return s
}`,
		},
		{
			name: "local struct field array source rejected",
			body: `type B struct { buf byte[8] }
fn bad() byte[] {
	b B
	return b.buf[:]
}`,
			wantErr: "Borrowed slice escapes through return",
		},
		{
			name: "local array field alias rejected",
			body: `type B struct { bufs byte[8][1] }
fn bad() byte[] {
	var b B
	s byte[] := b.bufs[0][:]
	return s
}`,
			wantErr: "Borrowed slice escapes through return",
		},
		{
			name: "local array source through struct field rejected",
			body: `type B struct { buf byte[] }
fn bad() B {
	var local byte[8]
	var b B
	b.buf = local[:]
	return b
}`,
			wantErr: `field "buf" contains a slice`,
		},
		{
			name: "static string through struct field may be returned",
			body: `type B struct { buf byte[] }
fn ok() B {
	var b B
	b.buf = "static"
	return b
}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := compileBosonSourceForTest(matrixSource(tt.body))
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("compile failed: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("compile succeeded, want error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestSliceProvenanceImportedSinkMatrix(t *testing.T) {
	imports := []importedVarForTest{
		{pkg: "visibility", name: "pub_slice", typ: byteSliceASTType()},
		{pkg: "visibility", name: "pub_slices", typ: byteSliceArrayASTType(1)},
	}
	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{
			name: "borrowed parameter to imported global rejected",
			body: `import "visibility"
fn bad(s byte[]) {
	visibility.pub_slice = s
}`,
			wantErr: "Borrowed slice escapes via global visibility.pub_slice",
		},
		{
			name: "local array to imported global rejected",
			body: `import "visibility"
fn bad() {
	var local byte[8]
	visibility.pub_slice = local[:]
}`,
			wantErr: "Borrowed slice escapes via global visibility.pub_slice",
		},
		{
			name: "global source to imported global allowed",
			body: `import "visibility"
var g byte[8]
fn ok() {
	visibility.pub_slice = g[:]
}`,
		},
		{
			name: "borrowed parameter to imported global array element rejected",
			body: `import "visibility"
fn bad(s byte[]) {
	visibility.pub_slices[0] = s
}`,
			wantErr: "Borrowed slice escapes via slice element",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := compileBosonSourceWithImportsForTest(matrixSource(tt.body), imports)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("compile failed: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("compile succeeded, want error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestSliceProvenanceOverwriteMatrix(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{
			name: "listed safe struct literal field clears prior borrowed field",
			body: `type Inner struct { buf byte[] }
type Outer struct { inner Inner }
var g byte[8]
fn ok(s byte[]) Outer {
	var o Outer
	o.inner.buf = s
	o.inner = Inner{buf: g[:]}
	return o
}`,
		},
		{
			// The omitted-field overwrite preserves the prior borrowed-param
			// field (codegen leaves the bytes), so the returned struct still
			// borrows the param. Now legal: recordable via return-alias
			// inference rather than rejected. The preservation fact still
			// matters — it is what makes the alias *recorded* (not dropped).
			name: "omitted struct literal field preserves prior borrowed-param field, return allowed",
			body: `type Inner struct { buf byte[]; n i64 }
type Outer struct { inner Inner }
fn ok(s byte[]) Outer {
	var o Outer
	o.inner.buf = s
	o.inner = Inner{n: 1}
	return o
}`,
		},
		{
			name: "scalar leaf overwrite clears descendant",
			body: `type Inner struct { buf byte[] }
type Outer struct { inner Inner }
var g byte[8]
fn ok(s byte[]) Outer {
	var o Outer
	o.inner.buf = s
	o.inner.buf = g[:]
	return o
}`,
		},
		{
			// A safe sibling write doesn't clear the borrowed-param field,
			// so the returned struct still borrows the param. Now legal:
			// recordable, not rejected.
			name: "safe sibling write preserves borrowed-param field, return allowed",
			body: `type Inner struct { buf byte[]; other byte[] }
type Outer struct { inner Inner }
var g byte[8]
fn ok(s byte[]) Outer {
	var o Outer
	o.inner.buf = s
	o.inner.other = g[:]
	return o
}`,
		},
		{
			name: "whole aggregate overwrite from local literal with listed safe field clears bucket",
			body: `type B struct { items byte[][2] }
var g byte[8]
fn ok(s byte[]) B {
	var b B
	b.items[0] = s
	b = B{items: [g[:], g[:]]}
	return b
}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := compileBosonSourceForTest(matrixSource(tt.body))
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("compile failed: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("compile succeeded, want error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestSliceProvenanceControlFlowMatrix(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{
			// One branch borrows the param into the field, so the merged
			// returned struct may borrow the param. Now legal: recordable
			// via return-alias inference, not rejected.
			name: "borrowed-param field on one branch, return allowed",
			body: `type B struct { buf byte[] }
var g byte[8]
fn ok(use_borrow bool, s byte[]) B {
	var b B
	if (use_borrow) {
		b.buf = s
	} else {
		b.buf = g[:]
	}
	return b
}`,
		},
		{
			// One branch borrows the param into the bucket. Now legal.
			name: "borrowed-param bucket on one branch, return allowed",
			body: `type B struct { items byte[][2] }
var g byte[8]
fn ok(use_borrow bool, s byte[]) B {
	var b B
	if (use_borrow) {
		b.items[0] = s
	} else {
		b.items[0] = g[:]
	}
	return b
}`,
		},
		{
			name: "both branches overwrite with safe source allowed",
			body: `type B struct { buf byte[] }
var g byte[8]
fn ok(use_first bool, s byte[]) B {
	var b B
	b.buf = s
	if (use_first) {
		b.buf = g[:]
	} else {
		b.buf = "static"
	}
	return b
}`,
		},
		{
			// The field borrows the param inside the loop; the returned
			// struct may borrow it. Now legal: recordable, not rejected.
			name: "loop break after borrowed-param field, return allowed",
			body: `type B struct { buf byte[] }
fn ok(s byte[]) B {
	var b B
	for (;;) {
		b.buf = s
		break
	}
	return b
}`,
		},
		{
			name: "loop break after safe overwrite allowed",
			body: `type B struct { buf byte[] }
var g byte[8]
fn ok(s byte[]) B {
	var b B
	b.buf = s
	for (;;) {
		b.buf = g[:]
		break
	}
	return b
}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := compileBosonSourceForTest(matrixSource(tt.body))
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("compile failed: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("compile succeeded, want error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tt.wantErr)
			}
		})
	}
}
