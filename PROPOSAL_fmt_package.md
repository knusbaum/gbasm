# Proposal: `fmt` Formatting Package

## Status

Draft. Not implemented. Companion to [`PROPOSAL_variadics_and_assertion.md`](PROPOSAL_variadics_and_assertion.md) —
this revision of the fmt design assumes the `any` interface,
variadic parameters, and type assertion all land first. Where the
earlier draft of this proposal deferred `printf` for lack of
language support, this version makes it Layer 5 and ships it.

## Summary

A runtime package `fmt` for string formatting, shaped to fit
Boson's constraints (no GC, immutable `byte[]`, value-shape coercion
limits) and Boson's character (small, explicit, the vtable visible).
Five layers, smallest first:

1. **Raw conversions** (`fmt.itoa`, `fmt.utoa`, `fmt.htoa`) — convert
   a single typed value into bytes written at the head of a
   caller-provided `mut byte[]`. No allocation, no I/O. Used by
   everything else.
2. **Writer helpers** (`fmt.str`, `fmt.int`, `fmt.uint`, `fmt.hex`,
   `fmt.char`, `fmt.bool`, `fmt.nl`) — format a single typed value
   into a stack-resident scratch buffer and emit it through an
   `io.writer`. No heap traffic; one underlying writer call per
   helper.
3. **Builder** (`fmt.Builder`) — a Bring-Your-Own-Memory accumulator
   over a caller-supplied `mut byte[]`, with chainable append
   methods and an extractable borrowed `byte[]` view.
4. **`stringer` + `Formatter` interfaces** — two optional
   user-extension hooks for "render me as bytes." `stringer` is
   preferred when implementable (no writer required, no construction
   overhead); `Formatter` is the general fallback for types whose
   string representation needs construction.
5. **Variadic helpers** (`fmt.print`, `fmt.printf`) — printf-style
   formatting backed by per-arg type assertion. `print` writes to
   stdout; `printf` writes to an explicit `io.writer`.

A `BuilderWriter` adapter lets `Builder` participate where an
`io.writer` is expected (Layers 4 and 5) without waiting on the
`io.writer` migration to `*mut self`.

The non-trivial design problem this proposal addresses, just as in
the earlier draft, is **allocation management**. The chosen split —
caller-owned buffers throughout, plus an explicit type-assertion
preference order in Layer 5 — keeps the heap out of every common
path while still giving user types a uniform extension hook. See
[Allocation discussion](#allocation-discussion).

## Motivation

The `string` runtime package today exposes four stdout-bound
printers (`puts`, `puti`, `putb`, `putc`) and `string.exit`. The
`io` package adds typed read/write over `FD` and an `io.writer`
interface. The `_iface` runtime (from the variadics+assertion
proposal) adds type assertion. Three needs follow:

- **Compose typed values into a single rendered output** —
  `fmt.printf(stdout, "x=%d y=%d\n", x, y)` instead of 4–8 separate
  `string.put*` calls.
- **Format a typed value *into memory*** (a `byte[]`) rather than
  directly to stdout — the Builder.
- **Give user types a uniform "print me" hook** — the
  `stringer`/`Formatter` pair, dispatched at runtime via
  `_iface.assert_to`.

The constraints this design has to respect:

- **No GC.** Returned slices borrow from caller-owned storage, or
  from self in the case of `stringer`. Heap touches are explicit.
- **Immutable `byte[]` for strings.** Composition requires a
  `mut byte[]` working buffer.
- **Value-shape coercion limits.** Interfaces store at most 8 bytes
  inline; slices (16 bytes) can't be coerced by value. This is why
  Layer 5 expects pointer-shape for `byte[]` arguments and why
  `&"hello"` is the canonical form at variadic call sites. See [The
  string-literal story](#the-string-literal-story).

## Goals

- A small, idiomatic API for formatting i64/u64/bool/byte/byte[]/
  pointer values into either a writer or a caller-owned buffer.
- Zero heap traffic on the common writer path (stdout, file,
  network). One `_heap.alloc` per never-before-seen interface-to-
  interface assertion pair during printf dispatch — bounded by the
  program's source.
- A Builder type that mirrors `bytes.Buffer` ergonomics for the "I
  need a rendered byte slice" case, without imposing a hidden
  allocation policy.
- Two `stringer`/`Formatter` interfaces so user types can plug into
  any writer-shaped sink, with a documented preference order.
- A printf-style API (`fmt.printf`) with strict per-directive
  matching and `%v` for stringer/Formatter dispatch.
- Clean cross-package use: the package is `pub` where it needs to
  be, holds no global state.

## Non-goals

- **No float formatting.** Boson has no float types yet.
- **No locale handling, padding, or width specifiers.** Those layer
  on top cleanly later — v1 emits the canonical, minimal-width
  form.
- **No internal heap-backed `Builder` that allocates its own backing.**
  Requires a slice-from-raw-pointer primitive that is not part of
  this proposal. The Builder is BYOM; future variant flagged.
- **No replacement of `string.puts/puti/putb/putc`.** Those remain
  as the fast-path stdout printers. `fmt` covers the writer/buffer
  general case.
- **No automatic `&` insertion at variadic call sites.** Users who
  pass a slice to `args ...any` write the `&` themselves. The
  alternative ("compiler auto-takes-address for slice sources") was
  considered and explicitly rejected — it would silently change the
  semantics of a literal depending on which parameter slot it's in.
  See [The string-literal story](#the-string-literal-story).
- **No struct-field rendering for `%v`.** Without reflection, `%v`
  on a struct falls through to the placeholder unless the struct
  implements `stringer` or `Formatter`.

## Background: empirical observations

Verified against `bosc` at commit `cac9817` (variadics+assertion
proposal committed):

1. **Variadics and `any` exist (per proposal).** A variadic
   parameter `args ...any` desugars to `args any[]` with a
   stack-allocated args slice at the caller. This is what Layer 5
   builds on.

2. **Type assertion exists (per proposal).** `x.(T)` for concrete T
   is a two-cmp inline check; `x.(I)` for interface I is a
   `_iface.assert_to` runtime call returning `(vtable*, bool)`.
   Layer 5 uses both — concrete assertions for typed directives
   (`%d`/`%s`/...), interface assertions for `%v` dispatch over
   `stringer`/`Formatter`.

3. **Slice value-shape coercion is rejected.**
   `validateInterfaceCoercion` (`cmd/bosc/compile.go:239`) rejects
   slice and array values being coerced into an interface by value:
   *"Cannot convert value of type %s to interface %s; use a pointer
   instead."* This rejection persists; the proposal expects
   pointer-shape for `byte[]`-typed args at variadic call sites.

4. **Interface fat pointer is `[data:8 | vtable:8]`** with the
   vtable carrying `[typedesc, shape_word, methods...]` per the
   variadics+assertion proposal.

5. **`io.writer` is `*self`-shaped.** Builder methods are
   `*mut Builder`-shaped, so Builder does not satisfy `io.writer`
   directly. The fmt package provides a `BuilderWriter` adapter
   (Layer 3) that wraps `*mut Builder` and exposes `*self` `write`
   methods — the adapter's `*self` reads its `*mut Builder` field
   and forwards through, with pointer-flavored mutability riding
   through the field load.

6. **String literals materialize slice headers at use sites today.**
   `"hello"` evaluates to a `byte[]` whose bytes live in `.rodata`
   but whose slice header is materialized wherever the literal is
   used (typically on the stack). `&"literal"` is not yet valid at
   function scope (per `CLAUDE.md`'s "things that bite"), so a
   `print("%s", &"hello")` call site is **not** yet expressible.
   See [The string-literal story](#the-string-literal-story) for the
   small companion proposal this depends on.

7. **The `_iface.assert_to` runtime helper exists (per proposal).**
   Layer 5's `%v` dispatch is two interface assertions per arg in
   the worst case — `stringer` first, `Formatter` second — both
   cache hits after the first invocation of each pair.

## Design overview

```
┌──────────────────────────────────────────────────────────────────┐
│  Layer 5: Variadic helpers (printf-shaped, per-arg type assert)  │
│    fmt.print  (format byte[], args ...any) i64, error            │
│    fmt.printf (w io.writer, format byte[], args ...any) i64, error│
├──────────────────────────────────────────────────────────────────┤
│  Layer 4: User-extension interfaces (orthogonal to Layers 1-3)   │
│    pub interface stringer  { string(*self) byte[] }              │
│    pub interface Formatter { format(*self, w io.writer) error }  │
│    BuilderWriter adapter: *mut Builder → io.writer                │
├──────────────────────────────────────────────────────────────────┤
│  Layer 3: Builder (BYOM accumulator + chainable methods)         │
│    fmt.Builder            { buf mut byte[], pos, err_ }          │
│    b.str/int/uint/hex/... return *mut Builder                    │
│    b.bytes() byte[]       (borrow)                                │
├──────────────────────────────────────────────────────────────────┤
│  Layer 2: Writer helpers (stack scratch buf + 1× write)          │
│    fmt.str/int/uint/hex/char/bool/nl(w io.writer, ...) i64, error│
├──────────────────────────────────────────────────────────────────┤
│  Layer 1: Raw conversions (write into caller's mut byte[])       │
│    fmt.itoa(out mut byte[], n i64) byte[]                        │
│    fmt.utoa(out mut byte[], n u64) byte[]                        │
│    fmt.htoa(out mut byte[], n u64) byte[]                        │
└──────────────────────────────────────────────────────────────────┘
```

Each layer is independently useful and depends only on layers below
it.

## The string-literal story

Layer 5's most natural call form — `fmt.print("name=%s\n", &name)` —
needs `&name` because the variadic `args ...any` slot requires
pointer-shape for slice-typed values. The same is true for string
literals at call sites: `fmt.print("hi %s\n", &"world")`.

`&"world"` at function scope is **not yet valid grammar**. To make
this proposal usable, a small companion change is required:

> **Companion proposal (out of fmt's scope):** allow `&"<literal>"`
> at function scope. Compiler emits the bytes in `.rodata` (already
> done today) and additionally emits a 16-byte slice header
> `{ptr: &bytes, len: N}` in `.rodata` per distinct literal.
> `&"hello"` evaluates to a `*byte[]` pointing at that static slice
> header. Lifetime is forever (read-only memory). No stack
> allocation; no heap allocation.

This proposal **assumes** that companion lands. Examples below use
`&"literal"` at function scope freely. Until then, the user-side
fallback is the explicit two-line form:

```
var greeting byte[] = "hello"
fmt.print("hi %s\n", &greeting)
```

Same semantics, more typing — but works today.

Surface A (explicit `&`) was chosen over Surface B (compiler
auto-takes-address at variadic-coercion sites for slice sources)
because the latter would silently change a literal's semantics based
on which parameter slot it appears in. The proposal is explicit: a
slice at a variadic-args position is addressed by the user, and the
resulting interface entry's typed assertion is `.(*byte[])`. The
vtable is visible.

## Layer 1: raw conversions

```
pub fn itoa(out mut byte[], n i64) byte[]
pub fn utoa(out mut byte[], n u64) byte[]
pub fn htoa(out mut byte[], n u64) byte[]
```

Each writes the canonical decimal/hex representation of `n` into
`out[0..k]` and returns `out[0:k]`. No NUL terminator. Sign is
emitted for negative `i64`. (Renamed `hex` → `htoa` from earlier
drafts — see Layer 2 for why; `fmt.hex` is now a writer helper.)

**Overflow behavior.** If the buffer is too small to hold the full
representation, the function writes the longest prefix that fits and
returns a slice whose `len` equals `len(out)`. Callers that want to
detect truncation compare the returned `len` against the predicted
digit count:

```
pub fn n_digits     (n i64) i64   // including sign
pub fn n_udigits    (n u64) i64
pub fn n_hex_digits (n u64) i64
```

For most callers, sizing `out` to `21` for decimal i64 / `16` for u64
hex is the right answer and truncation is impossible.

These three (plus the helpers) are the building blocks every other
layer calls. They allocate nothing.

## Layer 2: writer helpers

```
pub fn str  (w io.writer, s byte[]) i64, error
pub fn int  (w io.writer, n i64)    i64, error
pub fn uint (w io.writer, n u64)    i64, error
pub fn hex  (w io.writer, n u64)    i64, error
pub fn char (w io.writer, c byte)   i64, error
pub fn bool (w io.writer, b bool)   i64, error
pub fn nl   (w io.writer)           i64, error
```

Each formats its argument into a small stack buffer (24 bytes for
i64 decimal; 18 bytes for `0x` + 16 hex digits) and invokes
`w.write(slice)` exactly once. The `(written, error)` return
mirrors `io.writer.write` so call-site sequencing composes cleanly
with multi-value return bindings.

The `fmt.hex` ↔ `fmt.htoa` naming split fixes the awkward
`fmt.hex_w` from the earlier draft: Layer 1's hex conversion is now
`fmt.htoa` (parallel to `itoa`/`utoa`), freeing `fmt.hex` for the
writer-front. Same letter discipline as `fmt.str`/`fmt.int`/etc.

**No allocation.** All scratch buffers are stack-resident. The only
memory touched is the writer's own destination.

## Layer 3: the Builder

```
pub type Builder struct {
    buf  mut byte[]   // caller-provided backing
    pos  i64          // cursor; len(b.bytes()) == pos
    err_ error        // latched first error, io.io_err.OK while clean
} {
    // No constructor function. Builder is constructed inline in the
    // same frame that owns the backing buffer:
    //
    //     var backing byte[256]
    //     var b fmt.Builder = fmt.Builder{
    //         buf:  backing[:],
    //         pos:  0,
    //         err_: io.io_err.OK,
    //     }
    //
    // The struct-literal form is the public construction surface.

    // append-shaped methods. Each returns *mut Builder for chaining.
    str  (b *mut Builder, s byte[]) *mut Builder
    int  (b *mut Builder, n i64)    *mut Builder
    uint (b *mut Builder, n u64)    *mut Builder
    hex  (b *mut Builder, n u64)    *mut Builder
    char (b *mut Builder, c byte)   *mut Builder
    bool (b *mut Builder, v bool)   *mut Builder
    nl   (b *mut Builder)           *mut Builder

    // accumulated bytes, borrowed view.
    bytes(b *Builder) byte[]

    // remaining unwritten capacity.
    space(b *Builder) i64

    // latched error; OK while no append has overflowed.
    error(b *Builder) error

    // reset the cursor without disturbing buf. Clears the latched error.
    reset(b *mut Builder)
}
```

A Builder is a flat value type — no owned obligations, no hidden
allocation. The user supplies the backing buffer; the lifetime of
`b.bytes()` is the lifetime of `buf`.

**Latched error pattern.** Append methods cannot themselves return
errors (they return `*mut Builder` to support chaining). The first
append that overflows sets `b.err_` to a designated overflow value
(`io.io_err.ENOSPC`). Every subsequent append checks `b.err_` and
becomes a no-op. Callers inspect `b.error()` once at the end of a
chain.

(Carried over unchanged from the earlier draft — see that draft's
rationale for why latched-vs-per-call errors and why
`(*mut Builder, ...) *mut Builder` instead of
`(*mut Builder, ...) i64, error`.)

### The BuilderWriter adapter

`io.writer` is `*self`-shaped; Builder's append methods are
`*mut Builder`-shaped. Builder therefore doesn't satisfy `io.writer`
directly. This blocks `fmt.printf(builder, "...")` and any
`Formatter` implementation that expects to write to a Builder.

The fix is a small adapter that wraps `*mut Builder` and exposes a
`*self` `io.writer`-shaped interface:

```
pub type BuilderWriter struct {
    b *mut Builder
} {
    write(self *BuilderWriter, bs byte[]) i64, error {
        // Pointer-flavored mutability: reading `self.b` yields a
        // *mut Builder, and *mut methods are callable through it.
        // The adapter struct itself doesn't mutate; the Builder
        // behind the pointer does.
        const before i64 = self.b.pos
        self.b.str(bs)
        const after i64 = self.b.pos
        return after - before, self.b.error()
    }
}
```

The pattern — `*T`-receiver method reading a `*mut U` field and
calling a `*mut`-receiver method on the loaded pointer — was verified
to compile cleanly against bosc before locking this proposal: the
read of `self.b` is allowed (it's not a write to `self`), and the
resulting value carries `*mut U` semantics for the subsequent method
call, satisfying the `*mut`-receiver dispatch.

Construction is one struct literal in the user's frame:

```
var bw fmt.BuilderWriter = fmt.BuilderWriter{b: &b}
fmt.printf(bw, "name=%s\n", &name)
```

The adapter is cheap (one pointer, stack-allocated, no heap), and
it neatly defers the `io.writer` migration question. If that
migration eventually happens, `BuilderWriter` can be deprecated and
Builder can satisfy `io.writer` directly. Until then, the adapter
keeps the layering honest.

## Layer 4: user-extension interfaces

Two optional interfaces user types can implement. Both are
discovered at runtime via `_iface.assert_to` from the
variadics+assertion proposal.

```
pub interface stringer {
    // Return a byte slice rendering of self. The returned slice's
    // *lifetime is implementor-defined* and not reliably longer
    // than *self. Callers must finish using it before *self could
    // be moved or dropped. Implementors typically return a borrowed
    // sub-slice of a self field, or a slice into static memory.
    //
    // Cannot be implemented by types whose string representation
    // needs to be constructed at call time — there is nowhere for
    // the constructed bytes to live. Use Formatter for those.
    string(self *self) byte[]
}

pub interface Formatter {
    // Render self into w. Returns the first write error encountered
    // (or io.io_err.OK on success). Bytes-written count is not
    // surfaced — callers who need it can wrap their writer with a
    // counting adapter.
    format(self *self, w io.writer) error
}
```

### Preference order

When Layer 5 dispatches `%v` over an arg, it tries `stringer` first
and falls back to `Formatter`:

1. `args[i].(stringer)` succeeds → render via
   `fmt.str(w, s.string())`. Single virtual call to the type's
   `string` method, then one `io.writer.write`. No allocation by
   either side.
2. `args[i].(Formatter)` succeeds → call `f.format(w)`. The type
   handles its own scratch-buffer management and may issue multiple
   writes.
3. Neither matches → render a fallback placeholder like
   `%!v(<type-name>)`.

The order is fixed: stringer wins when both are implemented. The
rationale is that `stringer` is strictly cheaper (no writer
indirection, no multiple writes, no allocation pressure) and
implementing both signals "the cheap form is correct for this
type."

### When to implement which

| Type's string representation… | Implement |
|------------------------------|-----------|
| Is bytes already living in self (a field, or a sub-slice of one) | `stringer` |
| Is a static `.rodata` byte slice (enum labels, fixed names) | `stringer` |
| Must be constructed at call time (numbers, dates, multi-line ASCII) | `Formatter` |
| Both available (rare; e.g. a type that caches its rendering) | Both — `stringer` wins |

A type whose string requires construction can't honestly implement
`stringer` — there's nowhere for the constructed bytes to live.
`Formatter` is the answer; it gets a writer and decides where its
scratch buffer lives (typically stack-resident, via a Builder or
direct Layer 1 calls).

### Example: Formatter for a point

```
type point struct { x i64, y i64 } {
    format(p *point, w io.writer) error {
        var buf byte[64]
        var b fmt.Builder = fmt.Builder{
            buf: buf[:], pos: 0, err_: io.io_err.OK}
        b.str("(").int(p.x).str(", ").int(p.y).str(")")
        if (b.error() != io.io_err.OK) { return b.error() }
        const _, var err error = w.write(b.bytes())
        return err
    }
}
```

The Builder lives on `format`'s stack. Bytes assembled there, one
write to the caller's writer at the end. Zero heap.

## Layer 5: variadic helpers

```
pub fn print  (format byte[], args ...any) (i64, error)
pub fn printf (w io.writer, format byte[], args ...any) (i64, error)
```

`print` writes to stdout (saving the user from passing
`io.STDOUT` everywhere). `printf` takes an explicit writer. Both
have the same format-string semantics.

### Format directives (v1)

| Directive | Matches via | Renders as |
|-----------|-------------|-----------|
| `%d` | `args[i].(i64)` | signed decimal |
| `%u` | `args[i].(u64)` | unsigned decimal |
| `%x` | `args[i].(u64)` | hex with `0x` prefix |
| `%s` | `args[i].(*byte[])` | dereference and write bytes verbatim |
| `%c` | `args[i].(byte)` | one byte |
| `%t` | `args[i].(bool)` | `"true"` or `"false"` |
| `%v` | stringer → Formatter | per [preference order](#preference-order) |
| `%%` | (no arg consumed) | one literal `%` |

Each non-`%%` directive consumes one arg from `args`. Strict
per-directive matching: `%d` only accepts `i64`. If the assertion
fails, the renderer writes an inline error marker like
`%!d(typename=value-or-placeholder)` and continues. This mirrors
Go's behavior — surfaces the bug at the right spot without aborting
the whole call.

`%v`'s fallback (neither `stringer` nor `Formatter` implemented)
also writes an inline marker: `%!v(<type-name>)`. The type name
comes from the source typedesc's name field (Layer 1 of the
variadics+assertion proposal records this).

**`Formatter` dispatch and the byte count.** `Formatter.format`
returns `error` only — no byte count. To keep `printf`'s
`(written, error)` total honest, the helper wraps `w` in a
caller-stack-allocated counting writer before calling
`f.format(counting_w)`, then reads the delta after the call:

```
type counting_writer struct {
    inner io.writer
    n     i64
} {
    write(self *counting_writer, bs byte[]) i64, error {
        const k, var err = self.inner.write(bs)
        self.n = self.n + k
        return k, err
    }
}
```

(The same `*T`-receiver-reads-mut-and-mutates-through-pointer-field
pattern as `BuilderWriter` — except here the mutation is on the
counter struct's own field, so the receiver must be `*mut
counting_writer`. The adapter is stack-allocated per `printf` call;
no heap.) The helper's accumulated total includes everything the
Formatter wrote — no silent undercount.

**Shape constraints on `%v` over value-shape args.** Both `stringer`
and `Formatter` declare `*self` receivers. Under the exact-match
shape rule from the variadics+assertion proposal, a value-shape
interface entry (e.g., `var x any = small_value`) won't satisfy a
`*self`-receiver interface — the receiver shape derived from the
source's empty constructor stack is value, but the interface
requires a pointer-receiver. So `%v` over a by-value arg falls
straight to the `%!v(<type-name>)` placeholder, even if the type
implements `format(*self, ...)`. Users wanting `%v` dispatch on a
small value type pass it by reference: `fmt.print("%v", &x)`. All
examples in this proposal follow that convention.

### Strict `%s`

`%s` does **not** fall back to `stringer` when the arg isn't
`*byte[]`. Doing so would make the directive's meaning depend on
which user types happen to implement `stringer` — the same source
position could render differently across compilation units. Keep
`%s` narrow (bytes only), keep `%v` as the dispatch entry point.
Users who want stringer dispatch under control flow they own can
write `b.str(some_t.string())` directly.

### Out-of-args and trailing-directives

If the format string has more directives than args, the renderer
writes `%!<dir>(MISSING)` for each missing slot and returns an
error in the result tuple. If args has more entries than the format
consumes, trailing args are appended in `%v`-style, space-separated,
after the rendered format string. This is a small ergonomic win for
"I forgot to write %v %v %v" cases (and a debugging aid when
counting directives drifts), and the trailing-args case latches a
"surplus arg" error in the result tuple so callers can detect it.

### Error semantics

`(i64, error)` return matches `io.writer.write`. The `i64` is the
total bytes successfully written so far. The `error` is the first
non-OK error encountered, with the same shape as the other
writer-returning APIs in `io`:

- Write failure → stops processing further directives, returns
  immediately.
- Format mismatch (`%d` against a non-i64 arg) → writes the inline
  marker, continues, latches the error.
- Out-of-args → writes `MISSING` marker, continues, latches the
  error.

### Allocation profile of a `printf` call

For each arg's `%v`-dispatched assertion, the first call against a
given `(concrete_type, shape, target_iface)` triple does one
`_heap.alloc` inside `_iface.assert_to` (per the variadics+assertion
proposal's lazy itab cache). All subsequent calls of the same
triple hit the cache in O(1). For typed directives (`%d`, `%s`,
etc.) the dispatch is inline two-cmp concrete assertion — no heap
touch.

The args slice itself is **caller-frame-allocated** (per the
variadics proposal). No heap traffic from the variadic call shape.
No allocation in Layers 1–4. So a `printf` call's worst-case heap
cost is bounded by the number of distinct `(T, shape, stringer)`
and `(T, shape, Formatter)` pairs the *program* ever exercises —
not per-call, and not even per call site. In steady state, all
hits.

### Examples

```
// Plain literal + scalar.
fmt.print("hello, world\n")
fmt.print("answer = %d\n", 42)

// User type via stringer.
type planet struct { name byte[] } {
    string(p *self) byte[] { return p.name }
}

fn show(p *planet) {
    fmt.print("hi %v\n", p)              // %v finds stringer
}

// User type via Formatter.
type point struct { x i64, y i64 } {
    format(p *self, w io.writer) error { /* see Layer 4 */ }
}

fn show_point(p *point) {
    fmt.print("at %v\n", p)              // %v finds Formatter
}

// String literal as an arg requires &.
fn label(name byte[]) {
    fmt.print("name=%s\n", &name)        // *byte[] coercion
    fmt.print("greeting=%s\n", &"world") // requires &literal extension
}

// Writer variant.
fn dump_to_file(fd io.FD, x i64, y i64) {
    fmt.printf(fd, "x=%d y=%d\n", x, y)
}

// Builder as writer via adapter.
fn render_to_builder(b *mut fmt.Builder, x i64) {
    var bw fmt.BuilderWriter = fmt.BuilderWriter{b: b}
    fmt.printf(bw, "x = %d\n", x)
}
```

## Allocation discussion

This is the section the proposal exists to address. The shape of
the problem is unchanged from the earlier draft: in a language
without GC and without an owning slice type, building a string
requires answering where the backing storage lives, who allocates
it, and who frees it. The answers haven't changed for Layers 1–3:
caller-owned everywhere, no `owned`-shaped byte slice anywhere in
fmt's public surface.

Layer 5 adds one heap touch path: the lazy itab cache built by
`_iface.assert_to` on first interface-assertion against a given
`(typedesc, shape, target_iface)` triple. That's not fmt's
allocation — it belongs to `_iface` — but fmt is the package most
likely to exercise it.

- For `%d`/`%s`/`%c` directives and other concrete-typed paths, no
  interface assertion happens; no heap touch.
- For `%v` directives, the helper performs at most two interface
  assertions per arg (`stringer`, then `Formatter`). Each
  `(T, shape, stringer)` or `(T, shape, Formatter)` pair the
  program ever exercises does one `_heap.alloc` on its first
  occurrence; all subsequent occurrences hit the cache.

In a long-running program, the steady-state allocation rate from
fmt is zero. The startup cost is bounded by the source program's
distinct `(T, shape, I)` triples involved in `%v` dispatch.

### Why not return `owned byte[]` from a render call?

Same answer as the earlier draft: there is no language primitive for
a function to *construct* an owned dynamic-shape `owned byte[]`
slice. `_heap.alloc(n)` returns `*mut byte`, not a slice header;
`alloc(byte[N])` requires a compile-time N. Both could be solved by
additions, but those are independent of formatting and don't ship
here.

## `.bs` / `.bo` impact

None beyond ordinary package code. `fmt` is a pure-Boson package
with one `type` (Builder, plus the BuilderWriter adapter), two
`interface`s (stringer, Formatter), and a handful of `pub fn`
declarations. It imports `string`, `io`, and `_iface` (the last
implicitly, via the type-assertion lowerings). No new directive in
`.bs`, no new shape in `.bo`. Cross-package use is the standard
mechanism.

## Implementation impact

- **New package**: `runtime/fmt/fmt.bos`. ~400–550 source lines.
- **Top-level `mmkfile`**: add a `fmt.bo` build target alongside
  `io.bo`, with an importcfg pulling in `builtin`, `string`, `io`,
  and `_iface`.
- **`cmd/bosc/mmkfile`** (test scaffolding): build and link
  `fmt.bo` so tests in `tests/` can `import "fmt"`. Add to the
  `test.importcfg` template.
- **`runtime/builtin`** and other runtime packages: no change.
- **Compiler**: no change *from fmt itself*. The companion
  string-literal proposal (out of scope here) is a small grammar +
  codegen extension; see [The string-literal story](#the-string-literal-story).
- **Assembler / linker**: no change.

`bdoc` automatically picks up the new package once it's in the
runtime tree.

## Tests

Integration tests under `cmd/bosc/tests/`:

Layer 1:
- `fmt_itoa_test.bos` / `fmt_utoa_test.bos` / `fmt_htoa_test.bos`

Layer 2:
- `fmt_str_writer_test.bos` / `fmt_int_writer_test.bos` /
  `fmt_uint_writer_test.bos` / `fmt_hex_writer_test.bos` /
  `fmt_bool_writer_test.bos` / `fmt_char_writer_test.bos`

Layer 3:
- `fmt_builder_chain_test.bos` — full chain on a stack buffer.
- `fmt_builder_overflow_test.bos` — latched error on overflow.
- `fmt_builder_reset_test.bos` — reset semantics in a loop.
- `fmt_builderwriter_adapter_test.bos` — adapter forwards to
  underlying Builder; pointer-flavored mutability rides through.

Layer 4:
- `fmt_stringer_user_type_test.bos` — user type implementing
  stringer is discoverable via assertion.
- `fmt_formatter_user_type_test.bos` — user type implementing
  Formatter, ditto.
- `fmt_stringer_preferred_test.bos` — type implementing both;
  Layer 5 picks `stringer`.

Layer 5:
- `fmt_print_simple_test.bos` — `fmt.print("hello\n")`.
- `fmt_print_scalar_test.bos` — `%d`/`%u`/`%x`/`%c`/`%t`/`%%`.
- `fmt_print_string_arg_test.bos` — `%s` with `*byte[]` arg.
- `fmt_print_v_stringer_test.bos` — `%v` dispatches to stringer.
- `fmt_print_v_formatter_test.bos` — `%v` dispatches to Formatter.
- `fmt_print_v_formatter_count_test.bos` — a `%v`-via-Formatter
  arg whose `format` writes K bytes; the printf return total
  must include those K bytes (verifies the counting-writer wrap).
- `fmt_print_v_fallback_test.bos` — `%v` with neither, falls back
  to `%!v(<typename>)`.
- `fmt_printf_writer_test.bos` — explicit writer variant.
- `fmt_printf_builder_test.bos` — printf to a BuilderWriter.
- `fmt_print_mismatch_test.bos` — `%d` against a `byte` arg → inline
  marker, error returned.
- `fmt_print_missing_args_test.bos` — fewer args than directives.
- `fmt_print_extra_args_test.bos` — more args than directives.

Negative tests:
- `fmt_string_literal_value_err_test.bos` — `fmt.print("%s",
  "hello")` (no `&`) — until the string-literal companion lands,
  this should fail to compile with the existing "Cannot convert
  value of type byte[] to interface any" message.

## Open questions

### 1. Function-scope `&"literal"` (companion proposal, not fmt)

This proposal depends on `&"literal"` being valid at function scope
so that `fmt.print("%s", &"hello")` works. As covered in [The
string-literal story](#the-string-literal-story), the companion
work is small (emit a static slice header per literal, lift the
"`&literal` is static-init only" restriction for string literals).

**Open**: whether to bundle this into the fmt proposal or ship as
its own. My lean is its own — it's a grammar/codegen change with
no fmt dependencies in the other direction; bundling makes fmt's
scope creep. Either way, fmt should not merge until it lands.

### 2. `io.writer` migration to `*mut self`

The `BuilderWriter` adapter sidesteps this without ruling on it.
Once a concrete consumer surfaces — beyond Builder — that wants
Builder to satisfy `io.writer` directly, the migration becomes
worth doing. The fmt proposal does not block on it.

### 3. Builder's chain-helper utility

Earlier drafts of this proposal floated a `WriterChain`-style
helper for Layer 2 sequences that latches errors. With Layer 5
covering most of the use cases (`fmt.printf` returns
`(written, err)` for the whole sequence in one call), the chain
helper is much less compelling. **Pinned**: not in v1. Revisit if a
concrete use case surfaces that printf can't cover.

### 4. Width/padding/alignment specifiers

`%-5d`, `%05d`, `%10s` and friends. Not in v1; layer on top
straightforwardly once the core printf machinery is in place. The
directive parser is the natural place — extend it to recognize
flags + width + precision before the verb letter.

### 5. Float formatting

When Boson gains float types. Until then, not applicable.

### 6. Reflection-shaped runtime APIs

Listing methods, size, name from typedesc — pure read-only
operations. Worth doing once `%v`'s fallback placeholder text
exists as a real consumer of the typedesc name field. Not blocking
v1.

## Future extensions

- **Function-scope `&"literal"`** (companion proposal — load-bearing
  dependency).
- **Width/padding/alignment specifiers** on directives.
- **Float formatting** when Boson gains float types.
- **Heap-backed Builder** if a slice-from-raw-pointer primitive
  lands.
- **Linker dedup of identical static slice headers** across
  packages — small optimization once a profile shows it matters.
- **`fmt.errorf`** — format-into-error-message style, parallel to
  Go's `fmt.Errorf`, once the error story stabilizes around a
  consistent way to attach formatted messages to error values.
