package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// toASTForTest parses + ToASTs a top-level body (package "main") with no
// Compile pass, so front-end resolution — here, interface `from(...)` borrow
// clauses into declared ReturnAliases — can be inspected in isolation.
func toASTForTest(t *testing.T, body string) *Context {
	t.Helper()
	ctx := NewContext()
	ctx.SetPkgname("main")
	parser := NewParserAt("interface_from_test.bos", strings.NewReader(strings.TrimSpace(body)+"\n"), 1)
	for {
		n, err := parser.Next()
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if n == nil {
			break
		}
		if _, err := n.ToAST(ctx); err != nil {
			t.Fatalf("ToAST: %v", err)
		}
	}
	return ctx
}

func ifaceMethodForTest(t *testing.T, ctx *Context, iface, method string) InterfaceMethodSig {
	t.Helper()
	d, ok := ctx.InterfaceForName(iface)
	if !ok {
		t.Fatalf("interface %q not defined", iface)
	}
	for _, m := range d.Methods {
		if m.Name == method {
			return m
		}
	}
	t.Fatalf("method %q not found in interface %q", method, iface)
	return InterfaceMethodSig{}
}

// TestInterfaceFromClause pins the resolution of `from(...)` clauses to the
// declared ReturnAliases value (receiver = index 0, sorted+deduped per slot) —
// not just "it parses". An off-by-one or dropped set would misalign Phase 2's
// ⊆ conformance, so the index correctness is checked directly here.
func TestInterfaceFromClause(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		iface  string
		method string
		want   [][]int
	}{
		{
			name:  "receiver borrow",
			body:  `interface stringer { string(self *self) byte[] from(self) }`,
			iface: "stringer", method: "string",
			want: [][]int{{0}},
		},
		{
			name:  "receiver borrow, extra param ignored",
			body:  `interface keyed { at(self *self, k byte[]) byte[] from(self) }`,
			iface: "keyed", method: "at",
			want: [][]int{{0}},
		},
		{
			name:  "multi-source params resolve to indices",
			body:  `interface either { pick(self *self, a byte[], b byte[]) byte[] from(a, b) }`,
			iface: "either", method: "pick",
			want: [][]int{{1, 2}},
		},
		{
			name:  "out-of-order sources sort",
			body:  `interface ord { pick(self *self, a byte[], b byte[]) byte[] from(b, a) }`,
			iface: "ord", method: "pick",
			want: [][]int{{1, 2}},
		},
		{
			name:  "repeated source dedups",
			body:  `interface dup { f(self *self) byte[] from(self, self) }`,
			iface: "dup", method: "f",
			want: [][]int{{0}},
		},
		{
			name:  "multi-return both borrow self",
			body:  `interface splitter { split(self *self) byte[] from(self), byte[] from(self) }`,
			iface: "splitter", method: "split",
			want: [][]int{{0}, {0}},
		},
		{
			name:  "multi-return: borrowing slot then bare scalar slot",
			body:  `interface taker { take(self *self) byte[] from(self), i64 }`,
			iface: "taker", method: "take",
			want: [][]int{{0}, nil},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := toASTForTest(t, tt.body)
			sig := ifaceMethodForTest(t, ctx, tt.iface, tt.method)
			assert.Equal(t, tt.want, sig.ReturnAliases)
		})
	}
}

// TestInterfaceNoFromClause confirms an interface with no `from` anywhere has
// nil ReturnAliases (borrows nothing) and parses identically to before.
func TestInterfaceNoFromClause(t *testing.T) {
	ctx := toASTForTest(t, `interface plain { foo(self *self) byte[] }`)
	sig := ifaceMethodForTest(t, ctx, "plain", "foo")
	assert.Nil(t, sig.ReturnAliases)
}
