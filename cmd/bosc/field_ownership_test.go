package main

import (
	"strings"
	"testing"
)

func TestMutableBorrowFieldOwnership(t *testing.T) {
	tests := []struct{ name, body, want string }{
		{name: "replace buffer", body: `fn expand(b *mut Builder) {
 next := make([]mut byte, 16)
 free(b.buf)
 b.buf = next
}`},
		{name: "overwrite live buffer", body: `fn expand(b *mut Builder) {
 next := make([]mut byte, 16)
 b.buf = next
 free(next)
}`, want: `Cannot assign to owned field "b.buf" before consuming its current value`},
		{name: "replacement moves source", body: `fn expand(b *mut Builder) {
 next := make([]mut byte, 16)
 free(b.buf)
 b.buf = next
 free(next)
}`, want: `after it was moved`},
		{name: "restore on fallthrough", body: `fn expand(b *mut Builder) { free(b.buf) }`, want: `borrowed owned field "b.buf" is moved out`},
		{name: "restore on explicit return", body: `fn expand(b *mut Builder) { free(b.buf); return }`, want: `borrowed owned field "b.buf" is moved out`},
		{name: "restore before conditional return", body: `fn expand(b *mut Builder, n i64) {
 free(b.buf)
 if (n > 0) { return }
 b.buf = make([]mut byte, 16)
}`, want: `borrowed owned field "b.buf" is moved out`},
		{name: "restore both branches", body: `fn expand(b *mut Builder, n i64) {
 free(b.buf)
 if (n > 0) { b.buf = make([]mut byte, 16); return }
 b.buf = make([]mut byte, 32)
}`},
		{name: "readonly field borrow", body: `fn size(b *Builder) i64 { return len(b.buf) }`},
		{name: "readonly field cannot free", body: `fn bad(b *Builder) { free(b.buf) }`, want: `free requires an owned pointer or owned slice`},
		{name: "readonly owned field cannot free", body: `fn bad(b *owned Builder) { free(b.buf); dispose(b) }`, want: `free requires an owned pointer or owned slice`},
		{name: "readonly owner cannot hide leak", body: `fn bad(b owned *owned Builder) { free(b) }`, want: `would leak owned field b.buf`},
		{name: "mutable owner destructor", body: `fn destroy(b owned *mut owned Builder) { free(b.buf); free(b) }`},
		{name: "consume field through call", body: `fn take(s owned []mut byte) { free(s) }
fn expand(b *mut Builder) { take(b.buf); b.buf = make([]mut byte, 16) }`},
		{name: "borrow backing across free", body: `fn bad(b *mut Builder) i64 {
 old []byte := b.buf
 free(b.buf)
 b.buf = make([]mut byte, 16)
 return i64(old[0])
}`, want: `allocation was freed`},
		{name: "borrow backing before replacement", body: `fn good(b *mut Builder) i64 {
 old []byte := b.buf
 n := len(old)
 free(b.buf)
 b.buf = make([]mut byte, 16)
 return n
}`},
		{name: "alias cannot hide consumption", body: `fn bad(b *mut Builder) {
 alias := b
 free(alias.buf)
}`, want: `aliased`},
		{name: "move out then restore", body: `fn expand(b *mut Builder) {
 old := b.buf
 b.buf = old
}`},
		{name: "self consuming replacement", body: `fn shrink(s owned []mut byte) owned []mut byte { return s[:1] }
fn expand(b *mut Builder) { b.buf = shrink(b.buf) }`},
		{name: "opaque call cannot erase restoration debt", body: `fn other(p *mut i64) { *p = 1 }
fn bad(b *mut Builder, p *mut i64) { free(b.buf); other(p) }`, want: `borrowed owned field "b.buf" is moved out`},
		{name: "nested restoration", body: `type Outer struct { inner owned Builder }
fn bad(b *mut Outer) { free(b.inner.buf) }`, want: `borrowed owned field "b.inner.buf" is moved out`},
		{name: "nested replacement", body: `type Outer struct { inner owned Builder }
fn expand(b *mut Outer) { free(b.inner.buf); b.inner.buf = make([]mut byte, 16) }`},
		{name: "nested pointer restoration", body: `type Outer struct { child owned *mut owned Builder }
fn bad(b *mut Outer) { free(b.child.buf) }`, want: `borrowed owned field "b.child.buf" is moved out`},
		{name: "cannot move partially consumed child", body: `type Outer struct { inner owned Builder }
fn take(b owned Builder) { free(b.buf); dispose(b) }
fn bad(b *mut Outer) { free(b.inner.buf); take(b.inner) }`, want: `moved out`},
		{name: "untracked receiver cannot consume", body: `fn alias(b *mut Builder) *mut Builder { return b }
fn bad(b *mut Builder) { free(alias(b).buf) }`, want: `bind the receiver first`},
		{name: "nested pointer alias cannot hide consumption", body: `type Outer struct { child owned *mut owned Builder }
fn bad(b *mut Outer) { alias *mut Builder := b.child; free(alias.buf) }`, want: `borrowed owned field "b.child.buf" is moved out`},
		{name: "return view keeps parameter alias", body: `fn view(b *Builder) []byte { return b.buf }`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := "package main\ntype Builder struct { buf owned []mut byte }\n" + tt.body + "\nfn main() i64 { return 0 }\n"
			out, err := compileBosonSourceForTest(src)
			if tt.want != "" {
				if err == nil || !strings.Contains(err.Error(), tt.want) {
					t.Fatalf("want error containing %q, got %v", tt.want, err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if tt.name == "return view keeps parameter alias" && !strings.Contains(out, "retaliases 0: 0") {
				t.Fatalf("missing receiver borrow in:\n%s", out)
			}
		})
	}
}
