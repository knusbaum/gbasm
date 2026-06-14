package main

import (
	"bytes"
	"strings"
	"testing"
)

// parseAndCompileForTest runs the full ToAST + Compile pipeline on a
// top-level body (no package line; pkgname "main" is assumed) and returns
// the populated context so a test can inspect cached inference results.
func parseAndCompileForTest(t *testing.T, body string) *Context {
	t.Helper()
	ctx := NewContext()
	ctx.SetPkgname("main")
	parser := NewParserAt("retalias_test.bos", strings.NewReader(strings.TrimSpace(body)+"\n"), 1)
	var asts []AST
	for {
		n, err := parser.Next()
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if n == nil {
			break
		}
		a, err := n.ToAST(ctx)
		if err != nil {
			t.Fatalf("ToAST: %v", err)
		}
		if a != nil {
			asts = append(asts, a)
		}
	}
	var out bytes.Buffer
	for _, a := range asts {
		if err := Compile(&out, ctx, a); err != nil {
			t.Fatalf("compile: %v", err)
		}
	}
	return ctx
}

// TestInterfaceDispatchBorrowPropagation is the discriminating case for
// Phase 3: a borrowed result of a virtual call must inherit the interface
// value's referent provenance. `v` is a LOCAL interface variable initialized
// from a pointer parameter `w`; `v.bytes()` (declared `from(self)`) aliases
// w's referent, so `get` must infer it returns a borrow of param 0 — [[0]],
// not [] (which would mean the propagation read v's own slot instead of its
// data-field origin) and not a rejection.
func TestInterfaceDispatchBorrowPropagation(t *testing.T) {
	ctx := parseAndCompileForTest(t, `
interface viewer { bytes(self *self) byte[] from(self) }
type W struct { buf byte[] } {
	bytes(w *W) byte[] { return w.buf }
}
fn get(w *W) byte[] {
	var v viewer = w
	return v.bytes()
}
fn main() i64 { return 0 }`)
	fn, ok := lookupFuncDeclForTest(ctx, "get")
	if !ok {
		t.Fatalf("get not found")
	}
	if !equalAliasSets(fn.ReturnAliases, [][]int{{0}}) {
		t.Fatalf("get.ReturnAliases = %v, want [[0]] (result borrows the interface receiver's referent, param 0)", fn.ReturnAliases)
	}
}

// lookupFuncDeclForTest resolves a function or "Type.method" name.
func lookupFuncDeclForTest(ctx *Context, name string) (*FuncDecl, bool) {
	if dot := strings.LastIndex(name, "."); dot >= 0 {
		if m, ok := ctx.MethodForType(name[:dot], name[dot+1:]); ok {
			return m, true
		}
	}
	return ctx.FuncDeclForName(name)
}

// TestReturnAliasInference checks alias_set inference over a range of
// return shapes: the inferred ReturnAliases must match the proposal's
// stated examples (param index per return slot).
func TestReturnAliasInference(t *testing.T) {
	tests := []struct {
		name string
		body string
		fn   string
		want [][]int
	}{
		{
			name: "subslice of param",
			body: `fn first_word(s byte[]) byte[] { return s[0:1] }`,
			fn:   "first_word",
			want: [][]int{{0}},
		},
		{
			// Mutual recursion: the cycle fixpoint must converge to the
			// precise {param 0} for the entry member (the design note's
			// worked example), not a conservative all-params set.
			name: "mutual recursion fixpoint converges to param 0",
			body: `
fn a_fn(x *mut i64) *mut i64 {
	if (*x < 10) {
		return b_fn(x)
	}
	return x
}
fn b_fn(x *mut i64) *mut i64 {
	*x = *x - 1
	return a_fn(x)
}`,
			fn:   "a_fn",
			want: [][]int{{0}},
		},
		{
			// Fixpoint PRECISION: a 2-param self-recursive function that
			// only ever returns param 0 must infer {0}, not the
			// conservative {0,1} the old cycle shortcut produced.
			name: "recursive 2-param infers only the returned param",
			body: `
fn weird(s byte[], t byte[], n i64) byte[] {
	if (n > 0) {
		return weird(s, t, n - 1)
	}
	return s[0:1]
}`,
			fn:   "weird",
			want: [][]int{{0}},
		},
		{
			// 3-member cycle: a→b→c→a must converge (dep-frame fixpoint),
			// and a post-convergence caller computes precisely against the
			// memoized members (cycle3_user is checked below in its own
			// case).
			name: "3-member cycle converges",
			body: `
fn c3a(x byte[], n i64) byte[] {
	if (n > 0) {
		return c3b(x, n - 1)
	}
	return x[0:1]
}
fn c3b(x byte[], n i64) byte[] { return c3c(x, n) }
fn c3c(x byte[], n i64) byte[] { return c3a(x, n) }`,
			fn:   "c3a",
			want: [][]int{{0}},
		},
		{
			// Cycle member demanded AFTER the cycle root converged: must
			// compute precisely against the memoized root, no iteration.
			name: "cycle member after convergence",
			body: `
fn c4a(x byte[], n i64) byte[] {
	if (n > 0) {
		return c4b(x, n - 1)
	}
	return x[0:1]
}
fn c4b(x byte[], n i64) byte[] { return c4a(x, n) }
fn c4user(s byte[]) byte[] { return c4b(s, 2) }`,
			fn:   "c4user",
			want: [][]int{{0}},
		},
		{
			// B1 regression: a forwarding function over a multi-param
			// callee records the FULL union, not one contributor.
			name: "multi-param forwarding records full union",
			body: `
fn yes2() bool { return (1 == 1) }
fn pick2(a byte[], b byte[], u bool) byte[] {
	if (u) {
		return a
	}
	return b
}
fn fwd2(x byte[], y byte[], f bool) byte[] { return pick2(x, y, f) }`,
			fn:   "fwd2",
			want: [][]int{{0, 1}},
		},
		{
			// B2 regression: a multi-return slot destructured into a
			// binding carries the callee's per-slot provenance.
			name: "multiret destructured slot records param",
			body: `
fn mk2(s byte[]) byte[], i64 {
	return s[0:4], 7
}
fn take2(s byte[]) byte[] {
	var v byte[], var n i64 = mk2(s)
	return v
}`,
			fn:   "take2",
			want: [][]int{{0}},
		},
		{
			// S1 regression: `return *p` (aggregate deref) conservatively
			// borrows p; the chained field view records transitively.
			name: "struct deref return borrows pointer",
			body: `
type DB struct { buf byte[] }
fn get2(p *DB) DB { return *p }
fn use2(p *DB) byte[] {
	const b DB = get2(p)
	return b.buf
}`,
			fn:   "use2",
			want: [][]int{{0}},
		},
		{
			// S2 regression: an interface-typed return wrapping a borrowed
			// pointer records the alias.
			name: "interface return records borrowed pointer",
			body: `
type GB struct { val i64 } {
	get(b *GB) i64 { return b.val }
}
interface G2 {
	get(s *self) i64
}
fn as_g2(b *GB) G2 { return b }`,
			fn:   "as_g2",
			want: [][]int{{0}},
		},
		{
			name: "slice passthrough",
			body: `fn id(s byte[]) byte[] { return s }`,
			fn:   "id",
			want: [][]int{{0}},
		},
		{
			// Reassignment (not init-binding) of a borrowed sub-slice into a
			// returned local. The Assignment path must install the binding's
			// OWN origin (mirroring the VarDecl-init path), else `return t`
			// under-records and a local can escape uncaught through a call.
			name: "reassigned subslice records param",
			body: `fn f(a byte[]) byte[] { var t byte[]; t = a[0:1]; return t }`,
			fn:   "f",
			want: [][]int{{0}},
		},
		{
			// Pointer reassignment shares the same Assignment-path origin
			// install: q initialized to one param, reassigned to another,
			// returned. The fix's Indirection>0 branch records the reassigned
			// param (the merge of the init and reassigned origins is out of
			// scope here — straight-line reassignment is what the fix covers).
			name: "reassigned pointer records reassigned param",
			body: `type T struct { v i64 }
fn g(p *T, r *T) *T { var q *T = p; q = r; return q }`,
			fn:   "g",
			want: [][]int{{1}},
		},
		{
			name: "pointer passthrough",
			body: `type T struct { v i64 }
fn id(p *T) *T { return p }`,
			fn:   "id",
			want: [][]int{{0}},
		},
		{
			name: "pick two params",
			body: `fn pick(a byte[], b byte[], use_a bool) byte[] {
	if (use_a) { return a }
	return b
}`,
			fn:   "pick",
			want: [][]int{{0, 1}},
		},
		{
			// Branch-merge of two borrowed params into one binding returned
			// AFTER the if. Unlike `pick` (two separate return sites), here a
			// single `return t` reads a binding whose origin was merged from
			// two different borrowed params. The merge synthesizes a join
			// origin carrying both, and JoinMembers expands it so the union
			// {0,1} is recorded — not just one, which would under-report the
			// alias to a caller and be unsound.
			name: "branch merge of two borrowed params records union",
			body: `fn merge2(a byte[], b byte[], cond bool) byte[] {
	var t byte[] = a
	if (cond) { t = b }
	return t
}`,
			fn:   "merge2",
			want: [][]int{{0, 1}},
		},
		{
			name: "struct constructor borrows param",
			body: `type B struct { buf mut byte[]; pos i64 }
fn new_builder(buf mut byte[]) B { return B{buf: buf, pos: 0} }`,
			fn:   "new_builder",
			want: [][]int{{0}},
		},
		{
			// Hole A: a struct returned BY VALUE FROM A CALL must expand the
			// callee's alias set onto the call's argument origins. Here
			// `outer` returns `mk(param)`; mk's return borrows its param
			// (slot 0 → param 0), so `outer`'s return aliases its own param.
			// Before the fix the struct-shaped slot only handled a *Symbol
			// and a *Funcall recorded nothing — letting a local through the
			// call escape uncaught (the _err integration test covers that
			// rejection).
			name: "struct returned from call records call-arg param",
			body: `type B struct { buf byte[] }
fn mk(s byte[]) B { return B{buf: s} }
fn outer(p byte[]) B { return mk(p) }`,
			fn:   "outer",
			want: [][]int{{0}},
		},
		{
			// Hole A, sentinel path: a struct returned from a call BOUND TO A
			// VAR then returned (`var b B = mk(p); return b`). The direct form
			// (`return mk(p)`) goes through the engine's call-expansion read; this var-
			// bound form has no single Origin, so recordStructReturnCallFieldFacts
			// records the borrowed-argument provenance onto b's sentinel field
			// key, which EscapingFieldOrigins then classifies. Before the fix
			// this recorded nothing (the struct-copy field propagation only
			// handled a *Symbol RHS), and a local in that slot escaped uncaught.
			name: "struct from call bound to var records param",
			body: `type B struct { buf byte[] }
fn mk(s byte[]) B { return B{buf: s} }
fn outer(p byte[]) B { var b B = mk(p); return b }`,
			fn:   "outer",
			want: [][]int{{0}},
		},
		{
			// Hole A, sentinel path via assignment (`var b B; b = mk(p); return b`).
			// updateFieldPointerFactsForAssignment routes the *Funcall RHS
			// through the same recordStructReturnCallFieldFacts helper, so the
			// assignment-bound form records the borrow identically to the
			// var-init form.
			name: "struct from call assigned records param",
			body: `type B struct { buf byte[] }
fn mk(s byte[]) B { return B{buf: s} }
fn outer(p byte[]) B { var b B; b = mk(p); return b }`,
			fn:   "outer",
			want: [][]int{{0}},
		},
		{
			// Hole B: two DIFFERENT borrowed params merged into one struct
			// FIELD across an if must record BOTH (the field union), matching
			// the top-level binding merge. mergeFieldPointerExpr synthesizes a
			// join origin and the engine's field-origin read expands its members. Before
			// the fix the field merge dropped one param (recorded [[0]] or
			// [[1]] depending on branch order), under-recording the borrow — a
			// use-after-free when a local sits in the dropped slot.
			name: "branch-merge struct field records param union",
			body: `type B struct { buf byte[] }
fn choose(s byte[], t byte[], use_s bool) B {
	var b B
	if (use_s) { b.buf = s } else { b.buf = t }
	return b
}`,
			fn:   "choose",
			want: [][]int{{0, 1}},
		},
		{
			name: "two hop transitive",
			body: `fn B(s byte[]) byte[] { return s[0:3] }
fn A(s byte[]) byte[] { return B(s) }`,
			fn:   "A",
			want: [][]int{{0}},
		},
		{
			name: "intermediate binding threads borrow",
			body: `fn tail(s byte[]) byte[] { return s[1:len(s)] }
fn ok(s byte[]) byte[] { const t byte[] = tail(s); return t }`,
			fn:   "ok",
			want: [][]int{{0}},
		},
		{
			name: "count return aliases nothing",
			body: `fn count(dst mut byte[], src byte[]) i64 { return len(src) }`,
			fn:   "count",
			want: [][]int{nil},
		},
		{
			name: "method returns receiver field",
			body: `type S struct { buf byte[] } {
	field(s *S) byte[] { return s.buf }
}`,
			fn:   "S.field",
			want: [][]int{{0}},
		},
		{
			// Preservation regression guards: the borrowed-param field
			// survives an empty-literal overwrite of its parent (codegen
			// leaves the bytes, the facts must stay), so the returned
			// struct still records the borrow. If field provenance were
			// dropped here, ReturnAliases would silently degrade [[0]]→[[]]
			// (under-recording) — these assert the fact is preserved.
			name: "empty literal overwrite preserves borrowed-param field",
			body: `type Inner struct { buf byte[] }
type Outer struct { inner Inner }
fn ok(s byte[]) Outer {
	var o Outer
	o.inner.buf = s
	o.inner = Inner{}
	return o
}`,
			fn:   "ok",
			want: [][]int{{0}},
		},
		{
			name: "safe sibling write preserves borrowed-param field",
			body: `type Inner struct { buf byte[]; other byte[] }
type Outer struct { inner Inner }
var g byte[8]
fn ok(s byte[]) Outer {
	var o Outer
	o.inner.buf = s
	o.inner.other = g[:]
	return o
}`,
			fn:   "ok",
			want: [][]int{{0}},
		},
		{
			name: "branch-merge borrowed-param field recorded",
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
			fn:   "ok",
			want: [][]int{{1}},
		},
		{
			name: "loop-break borrowed-param field recorded",
			body: `type B struct { buf byte[] }
fn ok(s byte[]) B {
	var b B
	for (;;) {
		b.buf = s
		break
	}
	return b
}`,
			fn:   "ok",
			want: [][]int{{0}},
		},
		{
			// An owned slice param registers no OriginBorrowed at prologue
			// (the seeding gates on !HasOwned), so the classifier hits the
			// pass branch and records nothing — the owned return constructs
			// new ownership, it does not alias the caller. (Open Q3.)
			name: "owned slice param return not aliased",
			body: `fn take(s owned mut byte[]) owned mut byte[] { return s }`,
			fn:   "take",
			want: [][]int{nil},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Run the full pipeline so inference is driven exactly as in
			// the real compile, then inspect the cached ReturnAliases.
			ctx := parseAndCompileForTest(t, tt.body)
			fd, ok := lookupFuncDeclForTest(ctx, tt.fn)
			if !ok {
				t.Fatalf("function %q not found", tt.fn)
			}
			got := aliasSet(ctx, fd)
			if !equalAliasSets(got, tt.want) {
				t.Fatalf("alias_set(%s) = %v, want %v", tt.fn, got, tt.want)
			}
		})
	}
}

// TestInterfaceBorrowContractDiagnostic checks the directed diagnostic for a
// ⊆-conformance failure: a borrowing method coerced to an interface that does
// not declare a covering `from(...)` contract names the method, what it
// borrows, and the fix (the harness's first-line-only integration matching
// cannot assert the multi-line body, so it is pinned here).
func TestInterfaceBorrowContractDiagnostic(t *testing.T) {
	src := `package main
type Holder struct { buf byte[] } {
	bytes(h *Holder) byte[] { return h.buf }
}
interface Byter { bytes(s *self) byte[] }
fn coerce(h *Holder) Byter { return h }
fn main() i64 { return 0 }`
	_, err := compileBosonSourceForTest(src)
	if err == nil {
		t.Fatalf("expected coercion to be rejected")
	}
	msg := err.Error()
	for _, want := range []string{
		"Type Holder does not implement interface Byter",
		"method bytes returns a borrow of its receiver",
		"declares no such borrow",
		"bytes(h *Holder) byte[]",
		"defined at",
		"from(...)",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("diagnostic missing %q\nfull message:\n%s", want, msg)
		}
	}
}

// Note: coercion of a borrowing type TO `any` is now allowed (the relaxation);
// the laundering it would enable is blocked at runtime by _iface.assert_to's
// mask check, pinned end-to-end by the integration test
// retalias_iface_launder_via_any_test.

func equalAliasSets(a, b [][]int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if len(a[i]) != len(b[i]) {
			return false
		}
		for j := range a[i] {
			if a[i][j] != b[i][j] {
				return false
			}
		}
	}
	return true
}
