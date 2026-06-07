# Proposal: Variadics, the `any` Interface, and Type Assertion

## Status

Draft. Not implemented. Depends on no other in-flight proposal.
Companion to [`PROPOSAL_fmt_package.md`](PROPOSAL_fmt_package.md): once
this lands, the deferred `printf` open question in that proposal becomes
implementable.

## Summary

Three changes that hang together:

1. **A built-in empty interface `any`** — the moral equivalent of Go's
   `interface{}`. Trivial extension of the existing interface
   machinery: zero required methods.

2. **Variadic function parameters as sugar** — `fn f(args ...any)`
   desugars to `fn f(args any[])`. At each call site, the compiler
   constructs the args slice on the caller's stack frame. There is no
   variadic *runtime*; the language gains a single syntactic
   transformation.

3. **Runtime type assertion** — both concrete-type
   (`x.(*foo)` / `x.(i64)`) and interface-to-interface (`x.(stringer)`).
   Concrete assertions inline as a two-`cmp` check (base-typedesc
   identity at vtable slot 0, plus shape-word equality at slot 1).
   Interface-to-interface assertions call a new runtime helper that
   maintains a per-typeinfo *lazy itab cache* — the first assertion to
   a given target interface scans the source typeinfo's method set,
   builds a vtable on the heap, and links it into the typeinfo's
   cache; subsequent assertions of the same pair hit the cache in
   O(1).

These together unblock printf-shaped APIs and the `Formatter`/`Stringer`
dynamic-dispatch patterns the fmt proposal sketched but couldn't reach.

The non-trivial design problem this proposal addresses is **how to make
interface→interface assertion work without leaking semantic computation
into the linker**. The chosen answer — per-typeinfo lazy itab caches
built by a runtime helper — keeps the linker doing pure single-emitter
duplicate-rejection on static symbols (no new dedup, no semantic
computation) while still giving normal interface method calls their
existing single-indirect-call dispatch cost. See [The runtime
helper](#the-runtime-helper).

## Motivation

The fmt proposal hit two walls that show up everywhere:

- **No printf-style API in v1.** A faithful `fmt.printf("%d %s\n", x,
  name)` needs either variadics or a heterogeneous slice literal. The
  former is the right answer; this proposal supplies it.
- **No `Formatter`/`Stringer` dispatch on arbitrary values.** Today an
  interface variable can be called against its declared method set,
  but there is no way to ask "does this concrete type *also* implement
  some other interface?" — the `x.(I)` shape doesn't exist. fmt has
  to either bake every supported user type into a closed set or
  forgo dynamic per-arg dispatch.

These aren't fmt-specific. Any place a function wants to accept "a
heterogeneous list of typed things" or "any value that knows how to
render itself" runs into the same walls.

## Goals

- A single empty interface `any` usable as a function parameter,
  variadic element type, and assertion target.
- A variadic call site that allocates *only* on the caller's stack —
  no heap, no hidden vtable allocation per arg.
- Concrete-type assertion (`x.(T)`) that costs two compares and a
  conditional branch (base typedesc + shape word; see [Layer 3](#layer-3-concrete-type-assertion)).
- Interface-to-interface assertion (`x.(I)`) that costs an
  O(distinct-targets-for-this-typeinfo) cache walk in steady state and
  one `_heap.alloc` per never-before-seen `(typeinfo, shape, iface)`
  triple.
- No linker-side semantic work — the linker keeps doing only the
  duplicate-rejection it already does, with the new symbols obeying
  the same single-emitter invariant typedescs already obey.

## Non-goals

- **No method-resolution-order, no struct embedding, no satisfaction
  by `forwarding`.** Method sets are computed exactly as today; this
  proposal extends *when* satisfaction is checked (compile-time for
  direct coercion, with the new shape-aware filter; runtime for
  interface→interface assertion) but not *how* a type's method set
  is derived.
- **No `reflect`-style runtime API.** typeinfo's `name` field is
  emitted but the only API consuming it in v1 is `bdump` and error
  messages. Reflection-style operations layer on later.
- **No multithreading story for the itab cache.** Boson is
  single-threaded today; the cache is a plain singly-linked list with
  no synchronization. Flagged in Open Questions for the eventual
  concurrency work.

## Background: empirical observations

Verified against `bosc` at commit `b0907ca`:

1. **Each named type already has a typedesc symbol.** `cmd/bosc/compile.go:259`
   emits `data __typedesc_<name> byte[1] "\0"` for every
   `TypeAliasDecl`, `TypeWithMethodsDecl`, struct, and interface. Today
   it is a 1-byte identity stub — its address is the type's identity,
   the contents are unused. The fmt-related io equality machinery
   already depends on it (`runtime/io/io.bos:446`). This proposal
   *grows* the typedesc into a typeinfo struct; the symbol name and
   role survive.

2. **Vtable layout already places typedesc at slot 0.** `cmd/bosc/ast.go:644`
   (`WriteVtables`) emits `__vtable_<T>__<I>` as N+1 8-byte slots, with
   slot 0 holding a relocation to `__typedesc_T` and slots 1..N holding
   relocations to T's methods in I's declaration order. The method-call
   path in `compileInterfaceMethodCall` (`cmd/bosc/compile.go:5054`)
   loads `[vtable + (methodIdx+1)*8]`, deliberately skipping slot 0.
   That skipped slot is exactly the typeinfo reach this proposal needs;
   the slot's *position* is reusable, but its *contents* are not — see
   observation 2a immediately below.

2a. **The existing typedesc name collapses indirection.**
   `interfaceConcreteTypeName` (`cmd/bosc/compile.go:216`) returns
   `t.StripOwned().Name`, which is the *bare* type name regardless of
   pointer level. `emitInterfaceFatPtr` (`cmd/bosc/compile.go:5179`)
   uses that string to construct both the vtable symbol name
   (`__vtable_<concreteTypeName>__<I>`) and the typedesc relocation
   (`__typedesc_<bareType>`). Consequence: `var a any = &m` (where
   `m i64`) and `var b any = m` produce **identical** vtables and reach
   the **same** `__typedesc_i64`. Both would type-assert successfully
   to `.(i64)` and both would fail `.(*i64)` — the opposite of the
   pointer-vs-value-shape distinction this proposal depends on.

   This proposal therefore *changes the vtable's content* to carry a
   distinct **shape word** alongside the typedesc pointer. There
   remains exactly one typedesc per declared base type (no
   `__typedesc_p_T`, `__typedesc_s_T`, etc. — see [Type identity:
   one typedesc per base, shape encoded in vtable](#type-identity-one-typedesc-per-base-shape-encoded-in-vtable)).
   The vtable layout grows by one word: slot 0 is still the typedesc
   pointer; slot 1 is the new shape word; methods move to slot 2..N+1.
   The "existing vtable emission is unchanged" framing in earlier
   drafts was wrong; the emission keeps the *shape* of "typedesc-plus-
   relocs at the head, methods after," but it grows one slot and the
   slot-0 target encodes a different identity (base type, not bare-
   plus-mangled-shape).

3. **Interface fat pointer is `[data:8 | vtable:8]`.**
   `cmd/bosc/compile.go:4993` documents the layout. The data word holds
   either a pointer (for pointer-shape interfaces) or an inline
   ≤8-byte value (for value-shape interfaces). The compiler rejects
   value-shape coercion of types larger than 8 bytes at
   `cmd/bosc/compile.go:244`: *"value interfaces can store at most 8
   bytes inline. Use a pointer instead."* This rejection persists; the
   variadic desugaring respects it.

4. **No variadic syntax exists.** No `...` token, no variadic
   parameter form, no variadic call site. Greenfield.

5. **No `any` / `anything` reserved identifier.** Free to claim.

6. **No type-assertion syntax exists.** The dot-paren form `x.(T)` is
   unused. The parser would need a small extension at `parsePostfix`
   to recognize it (alongside the existing `.member`, `[i]`, `[lo:hi]`,
   `(args)`, `{fields}`).

7. **The runtime currently has no allocator-using helpers outside
   `_heap.alloc`/`_heap.free`.** Adding an `_iface` package is a clean
   addition; it imports `_heap` and is imported only by code the
   compiler generates for assertions.

## Design overview

```
┌────────────────────────────────────────────────────────────────────┐
│  Sugar layer                                                        │
│    fn f(args ...any)         ⇨  fn f(args any[])                   │
│    f(x, y, z)                ⇨  { var __a[3] any; …; f(__a[:]) }   │
├────────────────────────────────────────────────────────────────────┤
│  Surface: type assertion                                            │
│    x.(T)  — inline 2-cmp:  [x.vt+0] == &__typedesc_<base(T)>       │
│                          && [x.vt+8] == shape_word_T               │
│    x.(I) — call:           _iface.assert_to(...)                   │
├────────────────────────────────────────────────────────────────────┤
│  Runtime helper                                                     │
│    _iface.assert_to(src_ti *typeinfo, src_shape u64,               │
│                     src_data i64, dst_desc *iface_desc)            │
│                  (vtable *u8, ok bool)                             │
├────────────────────────────────────────────────────────────────────┤
│  Static data                                                        │
│    __typedesc_<base>   one per declared base type, emitted by home │
│    __iface_desc_<I>    per interface I                              │
│    __vtable_<base>_<shape>__<I>  per (base, shape, I) coercion;     │
│                         layout [typedesc, shape_word, method_0..N]  │
└────────────────────────────────────────────────────────────────────┘
```

Each row uses only what is below it.

## The `any` interface

A new built-in interface declaration, conceptually:

```
pub interface any { }
```

…available everywhere without import. It has zero required methods.
Any concrete type satisfies it. The compiler treats `any` as the name
of an `InterfaceDecl` with `Methods == nil` and skips interface
satisfaction checks for it (every type satisfies trivially).

The `__vtable_<base>_<shape>__any` for a concrete-to-`any` coercion
is two slots under the shape-word layout: `__typedesc_<base>` at
offset 0 and the source's `shape_word` at offset 8. The bare-name
shape segment is what keeps `var a any = m` and `var b any = &m`
from colliding on the same symbol within a single coercing package
(see [Vtable layout](#vtable-layout) for the full naming rule).
`WriteVtables` grows by one slot to accommodate the shape word —
`len(spec.methods) == 0` yields `n = 2` total slots (typedesc +
shape word).

An interface variable typed `any` therefore has the same shape as any
other interface — `[data:8 | vtable_ptr:8]`, where `vtable_ptr` points
at a 2-slot vtable: typedesc pointer + shape word. Method dispatch is
moot (no methods). Concrete assertion is the same lowering as for any
interface — load vtable_ptr, then check both slot 0 (against the
target's `__typedesc_<base>`) and slot 1 (against the target's
expected shape word). Uniformity is the point.

**Naming.** `any` is the working name. `anything` is the alternative.
`any` is shorter, matches Go's recent precedent, and reads cleanly at
parameter position (`args ...any`). Either is fine — see Open
Questions.

## Variadics

### Syntax

A function parameter may be marked variadic by writing `...T` instead
of `T` as the type. The variadic parameter must be the *last* in the
parameter list:

```
fn printf(fmt byte[], args ...any) { ... }
fn append_bytes(dst mut byte[], src ...byte) mut byte[] { ... }
fn sum(xs ...i64) i64 { ... }
```

Inside the body, `args` is bound as a value of type `T[]` (here `any[]`,
`byte[]`, `i64[]`). The variadic marker is shed at the body boundary.

### Call-site desugaring

A call `printf("x=%d\n", x, y, z)` against a `fn printf(fmt byte[],
args ...any)` desugars to:

```
{
    // Stack-allocated args slice (caller frame).
    var __vargs_0 i64   // value-shape backing for x, if needed
    var __vargs_1 i64   // …for y, if needed
    …
    var __vargs[3] any
    __vargs[0] = construct_any(x)
    __vargs[1] = construct_any(y)
    __vargs[2] = construct_any(z)
    printf("x=%d\n", __vargs[:])
}
```

`construct_any(e)` is the existing concrete→interface coercion path,
unchanged. It handles both pointer-shape (`__vargs[i].data` holds the
pointer) and value-shape (`__vargs[i].data` holds the value inline)
exactly as today's `var v any = e` does.

The args array lives in the caller's stack frame. Its lifetime is the
call's lifetime. The callee receives a `T[]` slice header pointing at
this frame storage; the borrow checker treats it like any other
borrowed slice. The callee may not store the slice anywhere with a
longer lifetime than the call — same rules as a borrowed `byte[]`
parameter.

### Explicit forwarding

Following Go's `args...` convention, a single trailing `slice...`
argument passes a pre-existing slice as the variadic arg without
wrapping:

```
fn inner(xs ...any) { ... }
fn outer(xs ...any) {
    inner(xs...)         // passes xs through; no re-wrapping
}
```

Without the trailing `...`, `inner(xs)` would type-check as
`inner(xs_as_single_any_value)` and almost certainly do the wrong
thing. The `slice...` form is required for forwarding, parallel to Go.

### Value-shape vs pointer-shape, made explicit

The current `var v any = e` coercion already decides value-vs-pointer
shape from `e`'s static type: pointer or pointer-shaped `owned` →
pointer-shape; ≤8-byte value → value-shape; >8-byte value → compile
error. Variadic call sites use the *same rule per arg*. So:

```
printf("%d %d", n, &m)
```

— `n` (an i64) goes in as value-shape any; `&m` (an *i64) goes in as
pointer-shape any. The receiver disambiguates with the assertion
target: `args[0].(i64)` succeeds against the first; `args[1].(*i64)`
succeeds against the second. There is no auto-deref; **assertion type
must match the stored shape exactly.**

### What's *not* desugared

`printf(fmt, args...)` with `args` of type `[]any` already passes the
slice; no wrapping array, no per-element construction, no copy. The
slice header is forwarded verbatim. Lifetimes flow through normally.

## Layer 1: typeinfo (grown from typedesc)

The 1-byte `__typedesc_T` becomes a *pair* of symbols emitted
together: a read-only descriptor head and a writable cache slot. In
bas terms, the head is emitted as a `data` block (read-only; the
linker maps `Data` symbols into the F_READ section it currently
labels `.bss`) and the cache slot as a `var` block (writable; mapped
into the F_WRITE `.data` section). Keeping them as two distinct
symbols avoids needing the linker to straddle sections within a
single symbol — neither `gbasm.ofile` nor the existing linker has
machinery for that today, and adding it would be exactly the kind of
linker-semantic creep this proposal otherwise sidesteps.

```
// data block (read-only), emitted by T's home package:
__typedesc_T :=
    [u64]      name_offset        // offset into a name-string blob
    [u64]      name_len
    [u64]      size_bytes          // sizeof(T)
    [*u8]      cache_ref           // relocation to __typedesc_cache_T
    [u64]      method_count
    [*method]  methods[method_count]  // sorted by
                                       // (name_hash, sig_hash, receiver_shape)

method :=
    [u64]      name_offset
    [u64]      name_len
    [u64]      sig_offset           // canonical *non-receiver* signature
                                    //  text into the blob (params after
                                    //  the receiver + return type)
    [u64]      sig_len
    [u64]      name_hash            // 64-bit hash of method name
    [u64]      sig_hash             // 64-bit hash of non-receiver sig_text
                                    //  (lookup index; identity is sig_offset/sig_len)
    [u64]      receiver_shape       // canonical receiver-shape encoding
                                    //  (VALUE / *self / *mut self /
                                    //  owned *self / ...). Matched against
                                    //  the *expected* receiver shape derived
                                    //  from the source's shape_word; the
                                    //  iface_desc's own declared receiver
                                    //  marker is not consulted at runtime.
    [*fn]      fn_ptr               // relocation to method body

// var block (writable), emitted by T's home package alongside the head:
__typedesc_cache_T :=
    [*u8]      head_ptr            // initially nil; runtime appends itabs
```

**Single-emitter invariant.** Both symbols are emitted **only by T's
home package**. Every other package referring to T in an interface
context emits relocations against these symbols and never re-emits
them. Today's linker enforces this directly: `linker.go:162`/`:170`
already rejects duplicate qualified data/var definitions with
`"Duplicate definitions of data %s"` — there is no silent dedup, and
this proposal does not introduce one. The single-emitter rule is what
keeps cross-package typedesc identity sound; the linker's existing
duplicate-rejection turns any accidental violation into a hard link
error rather than a silently broken identity check.

The runtime helper reaches the cache head by following the
`cache_ref` relocation from the read-only head:
`read_cache_head(td) := *(td.cache_ref)`. Writers go through the same
path. The dual-symbol shape is invisible to compiler-generated
identity-compare code (which only reads typedesc pointers) — only the
runtime helper touches the cache slot.

### Method table

Sorted by `(name_hash, sig_hash, receiver_shape)` ascending. Lookup
uses binary search to find the *range* of entries matching the
search key, then linearly scans that range checking both
`name_text` and `sig_text` byte-for-byte until a full match is
found (or the range is exhausted). The hash triple is a fast index,
not a uniqueness key — see [Matching: two-axis
filter](#matching-two-axis-filter) for the full algorithm.

T's *source-level* method set is unique by real identity:
`(name, non-receiver-signature, receiver-shape)`. **Hash-triple
collisions are possible but harmless**: the compiler does not need
to detect or prevent them, because the post-hash text scan
deterministically picks the right entry (or rejects all of them if
none match). With 64-bit FNV-1a hashes the expected number of
collisions across a real program is effectively zero, but the
runtime does not rely on that — correctness comes from the text
check, not the hash distribution.

### Method signature hashing

The hash input is the textual form of the method signature with the
**receiver slot excluded** — only the non-receiver parameter types
and the return type contribute. Receiver shape lives separately on
each typedesc method entry as `receiver_shape u64`, and is matched
independently from the source's `shape_word` at assertion time. This
split is what lets the proposal preserve today's satisfaction
semantics (where the interface's declared receiver marker is
overridden by the source direction) while still giving the
non-receiver signature a deterministic byte-equal identity check.

The wider rationale and the matching algorithm are spelled out
below; the two hash inputs are:

```
name_hash_input := name              // method name only

sig_hash_input  := params_repr "->" return_repr
params_repr     := "(" type2 "," type3 "," ... ")"   // non-receiver params only
type_repr       := canonical_render(t)
                     // receiver slot is excluded; receiver shape lives in
                     // the separate `receiver_shape` field on each method
                     // entry.
```

`name_hash` and `sig_hash` are independent FNV-1a hashes over their
respective inputs — that independence is what the two-axis collision
check relies on.

**The receiver is intentionally excluded from `sig_text`/`sig_hash`.**
Boson's existing satisfaction rule (`TypeSatisfiesInterfaceAs`,
`cmd/bosc/ast.go:549`) does *not* treat the interface's declared
receiver marker as a hard constraint — for a value-source coercion
the expected receiver is forced to bare T regardless of whether the
interface declared `self` or `*self` (`cmd/bosc/ast.go:573`–`577`).
So a value-receiver method `f(self T) i64` is allowed to satisfy an
interface declaring `f(*self) i64` when coerced from a value source.
If we baked the receiver marker into the hashed/compared signature,
the proposal would silently change that semantics: byte-equal would
reject the legal satisfaction before the receiver-shape filter could
do anything useful.

Receiver direction is therefore matched on a separate axis: each
typedesc method entry carries a `receiver_shape u64` field (the
canonical encoding of `self` / `*self` / `*mut self` / `owned *self`);
the source's `shape_word` is mapped to an *expected* receiver shape
via Boson's satisfaction rule (value source → expects value-receiver;
pointer source → expects pointer-receiver of matching mut/owned
flavor); the runtime helper requires both axes to agree
independently. See [Matching: two-axis filter](#matching-two-axis-filter)
just below.

`receiver_shape` is part of the method-table sort key
(`(name_hash, sig_hash, receiver_shape)`), but its assertion-time
role is purely to validate that a *single* candidate is compatible
with the source's shape. Boson does not today allow receiver-shape
overloading on the same type — `DefineFunc` (`cmd/bosc/ast.go:337`)
rejects duplicate qualified method names, so `f(self T)` and
`f(*self T)` cannot coexist on the same `T`. The sort key includes
`receiver_shape` for sort stability and to make the future addition
of such overloading a localized change (sort-order only) rather
than a runtime rewrite, but the v1 design and runtime do not depend
on it.

The hash is 64-bit FNV-1a (or similar — pinned in implementation),
used as a **lookup key for fast binary search**, *not* as the
identity check. Identity on the non-receiver-signature axis is
verified by byte-for-byte comparison of the canonical signature text
(see [Matching: two-axis filter](#matching-two-axis-filter)).

The hash function must be reproducible across compilation units: same
input bytes ⇒ same hash, regardless of which package or build is doing
the hashing. FNV-1a is byte-for-byte deterministic and zero-state, so
this is automatic. Same goes for the canonical signature text — both
sides of an assertion (the source typedesc and the target iface_desc)
must produce byte-identical canonical strings for the same logical
non-receiver signature, or honest matches will fail to be recognized.

### Matching: two-axis filter

A method matches a required interface method iff *both* axes agree
independently:

1. **Name + non-receiver signature axis**: `(name_hash, sig_hash)`
   together with `receiver_shape` are the binary-search key into
   `typedesc.methods`. Because 64-bit hashes can collide, the binary
   search finds the *range* of entries sharing the key triple
   (`lower_bound`/`upper_bound`-style — the table is sorted, so
   equal-key entries are contiguous); the helper then linearly scans
   that range, comparing `name_text` and `sig_text` byte-for-byte
   against the iface_desc's `req_name_text` and `req_sig_text` until
   one entry matches both, or the range is exhausted. Both text
   compares are required (independent 64-bit hashes ⇒ independent
   collision risks); the range scan handles the rare case where two
   distinct methods happen to share *both* hashes and a receiver
   shape. Hash collisions are harmless because the texts settle
   identity deterministically; the runtime does not depend on
   collision-freedom. `sig_text` covers the non-receiver parameter
   types and the return type — receiver excluded.

2. **Receiver-shape axis**: the source's `shape_word` is mapped to an
   *expected* receiver shape by the same predicate Boson's
   coercion-time satisfaction uses:
   - empty-stack `shape_word` (bare value) → expect value-receiver
     (`self` flavor).
   - top-of-stack `PTR` → expect `*self`.
   - top-of-stack `MUT_PTR` → expect `*mut self`.
   - top-of-stack `OWNED_PTR` → expect `owned *self`.
   - deeper or unsupported stacks (e.g. `**T`, `*byte[]`) → no
     receiver shape is compatible; only `any`-shaped targets succeed.

   The candidate method's `receiver_shape` must equal the expected
   shape exactly. The receiver marker in the iface_desc's source-
   level declaration is *not* consulted at runtime — Boson's rule is
   that the source direction overrides it.

Sort order in the typedesc method table is
`(name_hash, sig_hash, receiver_shape)` so the binary search reaches
the start of the matching key-range in O(log N), and any entries
sharing the triple sit contiguously after it. In v1, receiver-shape
overloading is not allowed on the same type (`DefineFunc` rejects
duplicate qualified method names), so every key-range typically
contains exactly one entry, and the post-hash range scan finds it
in one step. The collision-scan loop exists for the rare case where
two distinct source-level methods happen to share both 64-bit
hashes and a receiver shape — empirically zero occurrences in any
real program — and as the localized hook for future receiver-shape
overloading should the language gain it.

The name text and the non-receiver signature text carried in each
entry are the same strings fed into FNV-1a to produce their
respective hashes. No new format, no new canonicalization pass —
just keep the bytes around instead of throwing them away after
hashing. Storage cost is two `(offset, len)` pairs plus the string
bytes themselves, deduplicated into a per-`.bo` string blob.
Lookup cost in the common (no-collision) case is two short
`memcmp`s on the single matching entry — one for the name, one for
the non-receiver signature — plus a `u64` compare on
`receiver_shape`. No allocations. On the rare double-hash + same-
receiver collision, the cost grows by the bounded range scan
described above (still no allocations, just additional `memcmp`s
across the contiguous range).

For users worried about adversarial collisions: they can't happen.
The "adversary" would need to craft a method whose name and
signature texts *both* hash to specific values *and* produce
byte-equal texts at the post-hash check — which collapses the
adversarial case to "have exactly the same canonical name+signature
as the target," at which point it isn't an attack. The hash function
choice is therefore purely a performance-vs-storage tradeoff
(FNV-1a is fine), not a security property.

### Type identity: one typedesc per base, shape encoded in vtable

Each declared *base* type gets exactly one typedesc symbol, emitted
by that type's home package. Shape modifiers (indirection, slice,
array, mut, nullability) are **not** part of typedesc identity — they
live in a separate **shape word** at vtable slot 1. This is what
keeps emission locally knowable: T's home package can't predict that
some downstream package will coerce `**T` or `mut T[]`, but under
this model it doesn't need to — there is only ever one
`__typedesc_T`, and every shape variant references it from a vtable
whose shape word carries the modifier.

Properties of *base-type* identity (what makes two base types
distinct):

| Property | Part of typedesc identity? | Why |
|----------|---------------------------|-----|
| Base name (`i64`, `foo`, `io.FD`) | yes | distinct named types are distinct |
| Package qualifier (`io.FD` vs local `FD`) | yes | distinct cross-package types |
| Indirection level / slice / array | **no — shape word** | varies per coercion site under separate compilation |
| `mut` qualifiers | **no — shape word** | same; not knowable by base-type home |
| Nullability | **no — shape word** | same |
| `owned` bits | no | `validateInterfaceCoercion` strips owned at coercion |

#### The shape word

A 64-bit canonical encoding of the source type's shape modifiers,
written into vtable slot 1 at every coercion site. The encoding is a
**bounded constructor stack** applied to the named base type — each
stack entry is one constructor (`PTR`, `MUT_PTR`, `SLICE`,
`MUT_SLICE`, `ARRAY(N)`, `NULLABLE`), and the stack is read
innermost-first. This is what lets composed types like `*byte[]` and
`byte[][]` be expressed: a single flat `kind` field couldn't stack
constructors, so the proposal does not use one.

The exact bit layout is pinned in implementation. The invariants are:

- **Canonical and byte-deterministic**: same source type ⇒ same
  64-bit value, regardless of which compilation unit computes it.
- **Composable**: each level is a constructor over the level beneath
  it; the bottom of the stack is the named base type.
- **Bounded**: capacity is finite (≥ 8 levels is plenty for any type
  the language can construct in practice; ≥ 12 with a tight 5-bit
  kind field). Types deeper than the bound are a compile error at
  the coercion site, not a runtime surprise.
- **Empty stack** ⇒ the bare base. `shape_word == 0` means
  `var x any = m` where `m i64`.

Worked examples:

```
i64           base i64,  stack []
*i64          base i64,  stack [PTR]
*mut i64      base i64,  stack [MUT_PTR]
**i64         base i64,  stack [PTR, PTR]
byte[]        base byte, stack [SLICE]
mut byte[]    base byte, stack [MUT_SLICE]
*byte[]       base byte, stack [SLICE, PTR]      ← &bs from the
                                                    motivating example
byte[][]      base byte, stack [SLICE, SLICE]    ← argv type passed
                                                    to main.main
**byte[]      base byte, stack [SLICE, PTR, PTR]
[16]byte      base byte, stack [ARRAY(16)]
*io.FD?       base io.FD,stack [NULLABLE, PTR]
```

The base is always a *named* type — `byte`, `foo`, `io.FD` —
emitted by its home package. Compositions like `byte[]`, `*byte[]`,
and `byte[][]` all reference the same `__typedesc_byte`; their
distinct identity at assertion sites comes entirely from the
constructor stack in the shape word. This is why typedescs can be
home-package-emitted under separate compilation: no consumer ever
needs to invent a typedesc for a composed type.

**Array length is part of identity.** `[16]byte` and `[32]byte` are
different types and produce different shape words (different
`ARRAY(N)` parameters at the same stack level). **Slice capacity is
not part of identity**: `byte[]` of any cap is one type with one
shape word; the cap is a runtime property of the value, not of the
type.

Owned variants share the shape word of their non-owned counterpart —
`validateInterfaceCoercion` strips owned before computing the shape.

#### Mut and assertion-time exactness

Assertion uses the *same* shape-equality rule as interface
satisfaction: shape words must match exactly. `var x any = p` where
`p *mut i64` produces a shape word with a `MUT_PTR` constructor;
`x.(*i64)` (a `PTR`-constructor target) **fails** on the slot-1
compare. There is no mut-weakening at assertion sites — if the user
wants to assert through to an immutable view, they assert against
`*mut i64` and rely on the existing implicit `*mut → *` conversion
at the use site (today's rule, unchanged).

#### Vtable layout

Per (source-shape, target-interface) coercion, the compiler emits
`__vtable_<base>_<shape_mangling>__<I>` — the *base* type identifies
the typedesc the vtable references, and the *shape mangling*
disambiguates vtables for distinct shape variants of the same base.
The shape mangling renders the canonical shape word as a short
deterministic string (e.g. `v` for value, `p1` for `*T`, `pm1` for
`*mut T`, `s` for slice, `sm` for `mut byte[]`, ...). The exact
mangling is pinned in implementation. The invariant the bare-name
mangling enforces is **collision-freedom within a single coercing
package**: two coercion sites in the same `.bo` with different
shapes produce different bare names so `NeedVtable`'s intra-package
dedup (`cmd/bosc/ast.go:622`) doesn't collapse them, and two
coercion sites in the same `.bo` with the same shape share one
emission. Across packages, vtables are package-qualified at link
time and never collide regardless of bare-name collisions — see [the
.bo and linker impact section](#bo-and-linker-impact) for the per-
coercer-package ownership model.

Layout:

```
vtable[0] = &__typedesc_<base>        (one per base type, shared
                                       across all shape variants)
vtable[1] = shape_word                (per shape variant — written as
                                       bytes by the emission, not a
                                       relocation)
vtable[2 + i] = method_i fn_ptr       (relocations to T's methods,
                                       filtered to receiver shape
                                       compatible with shape_word)
```

The method dispatch path in `compileInterfaceMethodCall`
(`cmd/bosc/compile.go:5054`) shifts from `[vtable + (methodIdx+1)*8]`
to `[vtable + (methodIdx+2)*8]` — slot 1 is now the shape word rather
than the first method.

For `var a any = m` and `var b any = &m` (m i64), the two coercions
build separate vtables whose slot 0 both point at `__typedesc_i64`
but whose slot 1 differs (empty-stack encoding for the value
coercion vs `[PTR]`-stack encoding for the pointer coercion). Two
distinct vtables, one shared typedesc, two distinct shape-aware
assertion identities.

#### Method tables in the typedesc

Each typedesc carries T's **full** method set — no per-shape
filtering at emission time, because there's only one typedesc per
base. The method-entry layout was given in detail under [Layer 1's
descriptor section](#layer-1-typeinfo-grown-from-typedesc) — each
entry holds name + non-receiver `sig_text` + `name_hash` +
`sig_hash` + `receiver_shape` + `fn_ptr`. Static methods (no
receiver) don't appear; they aren't callable through an interface.

Assertion-time filtering then has two entry points:

1. **At compile-time, for concrete→interface coercion**:
   `validateInterfaceCoercion` (`cmd/bosc/compile.go:220`) currently
   reduces the source to a binary `pointerDirection := srct.Indirection
   > 0` and calls `TypeSatisfiesInterfaceAs(typeName, ifaceDecl,
   pointerDirection)`. That's insufficient under the shape-word
   model — it can't distinguish `**T` from `*T`, `*mut T` from `*T`,
   or `*byte[]` from `byte[]`. This proposal replaces that binary
   with the full shape-word filter: compute `shape_word(srct)`,
   derive `expected_receiver_shape(shape_word(srct))`, walk T's
   method table, and accept candidates whose `receiver_shape`
   equals that expected value and whose non-receiver signature
   matches the interface method's. The compiler then writes the
   selected fn_ptrs into vtable slots 2..N+1; the shape word goes
   into slot 1 verbatim.

2. **At runtime, for interface→interface assertion**: handled by
   `_iface.assert_to` using the same two-axis filter and the same
   `expected_receiver_shape` derivation — see [Matching: two-axis
   filter](#matching-two-axis-filter) for the algorithm.

The runtime helper needs the source's shape word as input — its
signature grows by one parameter. See [The runtime helper](#the-runtime-helper).

### Cross-package typeinfo: single-emitter, not linker-dedup

A concrete type `T` declared in package `P` has exactly one
`__typedesc_T` symbol — emitted by `P`'s compilation, *period*. Other
packages that mention `T` in an interface context (coercion,
assertion) refer to the symbol by relocation and never re-emit it.
There is never more than one `__typedesc_T` in a linked executable
because there is only ever one *definition* of it.

This is a **single-emitter invariant**, not a linker dedup. The
current linker (`linker.go:162`/`:170`) actively rejects duplicate
qualified data/var definitions with `"Duplicate definitions of
data %s"`; the proposal does not change that and does not introduce
any silent dedup. The hard error is the enforcement mechanism — if
the compiler ever accidentally emitted `__typedesc_T` in two packages,
the link would fail loudly rather than picking one and hoping. No new
linker logic is needed; the existing duplicate-rejection is what makes
the single-emitter rule safe.

The compiler emits `__typedesc_T` when:

- The type is declared (`TypeAliasDecl`, `TypeWithMethodsDecl`, struct
  decl, interface decl) — *in the home package*.
- Built-in scalar types (`i64`, `byte`, etc.) — emitted as part of
  the `runtime/builtin` package's compilation, so cross-package
  assertion to `.(i64)` resolves to a single shared symbol.

Cross-package uses emit no typedesc — they only reference the home
package's symbol.

## Layer 2: interface_desc

For each interface declaration `interface I { sig1; sig2; ... }`, the
compiler emits an `__iface_desc_I` symbol:

```
__iface_desc_I :=
    [u64]      name_offset
    [u64]      name_len
    [u64]      method_count
    [*req]     methods[method_count]    // sorted by (name_hash, sig_hash)

req :=
    [u64]      name_offset
    [u64]      name_len
    [u64]      sig_offset           // canonical non-receiver-signature
                                    //  text into the blob
    [u64]      sig_len
    [u64]      name_hash
    [u64]      sig_hash             // lookup index — identity is the text;
                                    //  receiver shape comes from the
                                    //  source's shape_word at runtime,
                                    //  not from the iface_desc itself
```

This is the assertion-time companion to typeinfo's method table:
identical shape for the per-entry data, no `fn_ptr` (the iface_desc
just *requires* methods; the typeinfo *provides* them).

Emitted as a `data` block (read-only) by I's home package, *only* by
I's home package. Cross-package references are relocations; the
single-emitter invariant is enforced by the linker's
duplicate-rejection (`linker.go:162`), same as typeinfo.

For `any`, `__iface_desc_any` has `method_count == 0`. The runtime
helper handles this with no special-case branch: it allocates a
2-slot itab (`ITAB_HEADER_SIZE + (0+2)*8` bytes), writes `src_ti`
into vtable slot 0, `src_shape` into vtable slot 1, and skips the
zero-iteration method-lookup loop. The returned vtable pointer is the
address of that itab's vtable region, so `[vtable_ptr+0]` reads back
to `src_ti` and `[vtable_ptr+8]` reads back to `src_shape` — the
invariant for both slots is preserved uniformly. The itab is cached
in the per-typedesc list (keyed by `(__iface_desc_any, src_shape)`)
and reused for subsequent `x.(any)` calls against the same source
type and shape.

(An earlier draft proposed returning `src_ti` itself as the vtable
pointer to skip the allocation. That broke the slot-0 invariant —
`[src_ti+0]` is the typedesc head's `name_offset` field, not a
typedesc pointer — and a later concrete assertion through the
asserted `any` would have compared against `name_offset` and
silently misfired. The allocation cost is one `_heap.alloc` per
never-before-asserted `(T, shape, any)` triple — negligible, and it
preserves both slot invariants.)

## Layer 3: concrete-type assertion

Surface syntax:

```
x.(T)        // T may be any concrete type the language allows to be
             // stored in an interface: i64, *i64, *foo, *byte[],
             // *struct_name, ... — i.e. any type whose storage shape
             // fits the interface data slot (value types ≤ 8 bytes
             // inline, or any pointer). Slice and array *values* are
             // rejected at coercion time (`validateInterfaceCoercion`,
             // `cmd/bosc/compile.go:239`), so a target like
             // .(byte[]) is statically unreachable today — use the
             // pointer form .(*byte[]).
```

Result is `(T, bool)` — see Open Questions for the bind shape; this
proposal assumes the lowering returns the value and a success flag,
and surface syntax for accessing them is the next design pass.

### Lowering

Given an interface value `x` (16 bytes: data + vtable_ptr), the
assertion `x.(T)` lowers to a *two-cmp* check — base-type identity
plus shape:

```
    mov  rax, [x_addr+8]         ; load vtable_ptr
    mov  r10, [rax+0]            ; slot 0 = base typedesc pointer
    mov  r11, [rax+8]            ; slot 1 = shape word
    lea  rcx, __typedesc_<base>
    cmp  r10, rcx
    jne  .fail
    mov  rcx, <shape_word_T>     ; canonical shape encoding for T
    cmp  r11, rcx
    je   .success
.fail:
                                  ; fall through: (zeroT, false)
.success:
                                  ; success: extract value from data slot
                                  ;  - if T is pointer-shape: T = data
                                  ;  - if T is value-shape:   T = (T)data
                                  ; emit (extractedT, true)
```

`shape_word_T` is the canonical shape encoding of the assertion
target, computed by the compiler at lowering time — a 64-bit
immediate. For `.(i64)`, `shape_word_T == 0`; for `.(*i64)` it
encodes a single `PTR` stack entry; for `.(*byte[])` it encodes a
`[SLICE, PTR]` stack over base `byte`. (A `.(byte[])` target — slice
by value — never matches in v1 since slice values can't be coerced
into interfaces today; lowering it produces a shape word the source
side can never satisfy.)

Three notes:

- The base-type compare uses `__typedesc_<base>` — the *one*
  typedesc that exists per base type. There is no
  `__typedesc_p_i64` or `__typedesc_byte[]`; both `*i64` and `i64`
  reach the same `__typedesc_i64`, and the shape word at slot 1
  distinguishes them.

- The shape-word compare against an immediate is what makes
  pointer-vs-value-shape distinct in a world with only base
  typedescs. Without it, every shape variant would silently match
  every other shape variant of the same base — exactly the bug
  observation 2a documents.

- Two cmps + two branches is a small constant-cost increase over
  the one-cmp version that would have worked only with per-shape
  typedescs. On x86-64 the loads are adjacent in cache, the
  immediates are pinned at compile time, and the branches predict
  trivially. Cost is in the noise; correctness is the win.

### Pointer-vs-value-shape and the assertion target

Both the vtable's typedesc *and* its shape word participate in the
match. If you wrote `var x any = 5`, then `x.vtable[0] ==
&__typedesc_i64` and `x.vtable[1] == 0` (empty constructor stack);
the assertion `x.(i64)` succeeds because both slots match. The
assertion `x.(*i64)` fails on the shape compare (it expects the
shape word for a `[PTR]` stack; the vtable holds the empty-stack
encoding). Conversely, `var x any = &m` (m i64) populates
`x.vtable[0] == &__typedesc_i64` and `x.vtable[1] == encode([PTR])`;
`x.(*i64)` succeeds and `x.(i64)` fails on shape.

(Background observation 2a is the prior bug this fixes: today's
emission collapses both `var x any = 5` and `var x any = &m` to the
same single-slot vtable referencing the bare `__typedesc_i64`, so
neither pointer-ness nor value-ness can be distinguished at
assertion time. The shape word at slot 1 is what lets us share the
typedesc across shapes — locally knowable at the base type's home
package — while still distinguishing every shape at every coercion
site.)

## Layer 4: interface-to-interface assertion

Surface syntax:

```
x.(I)        // I is an interface type
```

Same `(I, bool)` shape as concrete assertion.

### Lowering

```
    mov  rax, [x_addr+8]              ; src_vtable_ptr
    mov  rdi, [rax+0]                  ; src_typedesc (base)
    mov  rsi, [rax+8]                  ; src_shape_word
    mov  rdx, [x_addr+0]              ; src_data
    lea  rcx, __iface_desc_I
    call _iface.assert_to              ; (src_ti, src_shape, src_data, dst_desc)
                                      ; returns (vtable_ptr in rax,
                                      ;          ok in rdx)
    test rdx, rdx
    jz   .failure
    ; success: build the new interface value
    ;   [dst+0]   = src_data           (carried through unchanged)
    ;   [dst+8]   = vtable_ptr         (from helper; itab carries the
                                      ;  source's shape_word at slot 1)
.failure:
    ; (zeroI, false)
```

`src_shape_word` enters the helper because the source's shape is
what selects which methods from `src_ti.methods` are valid
candidates. The returned itab's vtable carries the same shape word
at slot 1, so the asserted interface is itself shape-aware — a
subsequent concrete assertion on the asserted interface lowers the
same way and reads the same shape word.

Note `src_data` is carried through verbatim — no copy, no rewrap. The
asserted interface and the source interface share the same data slot.
For pointer-shape, both hold the same pointer to the same backing; for
value-shape, both hold the same inline value bits.

**Shape safety.** Since there is only one typedesc per base type, the
source typedesc's method table carries *all* of T's methods — both
value-receiver and pointer-receiver. The helper consults
`src_shape` (passed in at the call site, read from the source
vtable's slot 1) to filter candidates: a method only counts as
matching if its `receiver_shape` equals
`expected_receiver_shape(src_shape)`, the canonical-encoding
mapping that follows Boson's exact-match satisfaction rule
(`TypeSatisfiesInterfaceAs`, `cmd/bosc/ast.go:549`) — empty
constructor stack maps to value-receiver, top-of-stack `PTR` maps
to `*self`, `MUT_PTR` to `*mut self`, etc. No Go-style auto-deref;
no mut-weakening.

A value-backed `any` holding T thus refuses to assert to an
interface whose methods require `*self`, even though the typedesc
contains those pointer-receiver methods — they fail the shape
filter and `lookup_method` returns `nil`. The asserted-interface
result `(nil, false)` matches what the coercion-time check would
have produced if the user had written the equivalent direct
coercion.

## The runtime helper

New runtime package `_iface`, with one exported function:

```
// runtime/_iface/iface.bos (conceptual; final .bs version may be
// hand-written if convenient).
package _iface

import "_heap"

pub fn assert_to(src_ti *u8, src_shape u64, src_data i64, dst_desc *u8)
                 (*u8, bool) {
    // 1. Walk src_ti.itab_cache_head looking for an itab whose key
    //    matches (dst_desc, src_shape). Hit: return its vtable.
    //    The cache key includes the shape because two coercions of the
    //    same base type with different shapes (e.g. T vs *T) need
    //    *distinct* itabs — they pick different methods from the same
    //    typedesc.
    var entry *u8 = read_cache_head(src_ti)
    for (entry != nil) {
        if (itab_iface_desc(entry) == dst_desc &&
            itab_shape(entry) == src_shape) {
            return itab_vtable(entry), true
        }
        entry = itab_next(entry)
    }

    // 2. Miss: allocate an itab provisionally and fill its method
    //    slots directly. The itab's embedded vtable mirrors a static
    //    vtable: [typedesc, shape_word, fn_0, fn_1, ...].
    //
    //    (Misses are NOT cached in v1 — the program could mean to ask
    //    again. See Open Questions.)
    var fn_count i64 = iface_desc_method_count(dst_desc)
    var itab_bytes i64 = ITAB_HEADER_SIZE + (fn_count + 2) * 8
    var itab *u8 = _heap.alloc(itab_bytes)
    set_itab_iface_desc(itab, dst_desc)
    set_itab_shape(itab, src_shape)
    set_itab_vtable_slot(itab, 0, src_ti)       // vtable[0] = base typedesc
    set_itab_vtable_slot(itab, 1, src_shape)    // vtable[1] = shape word

    // Derive the receiver shape the source can call: empty stack →
    // value-receiver; PTR-top → *self; MUT_PTR-top → *mut self; etc.
    // Stacks deeper than one constructor level (or with non-receiver-
    // shaped tops) map to "no compatible receiver" and immediately
    // fail any non-empty interface assertion.
    const expected_recv u64 = expected_receiver_shape(src_shape)

    for (var i i64 = 0; i < fn_count; i = i + 1) {
        const req_name_hash u64 = iface_desc_method_name_hash(dst_desc, i)
        const req_name_text byte[] = iface_desc_method_name_text(dst_desc, i)
        const req_sig_hash  u64 = iface_desc_method_sig_hash(dst_desc, i)
        const req_sig_text  byte[] = iface_desc_method_sig_text(dst_desc, i)
        // lookup_method:
        //   (1) binary-searches src_ti.methods for the *range* of
        //       entries sharing key triple (req_name_hash,
        //       req_sig_hash, expected_recv) — typically one entry,
        //       occasionally zero, and only on a double-64-bit-hash
        //       collision more than one.
        //   (2) linearly scans the range, comparing BOTH name_text
        //       byte-equal to req_name_text AND sig_text byte-equal
        //       to req_sig_text. Returns the first entry whose both
        //       texts match.
        //   (3) returns nil if the range is empty or every entry in
        //       the range fails the text check.
        // The iface_desc's own declared receiver marker is *not*
        // consulted — Boson's satisfaction rule is that the source
        // direction (i.e. expected_recv) drives receiver selection.
        const fn *u8 = lookup_method(src_ti,
                                     req_name_hash, req_sig_hash,
                                     expected_recv,
                                     req_name_text, req_sig_text)
        if (fn == nil) {
            _heap.free(itab)
            return nil, false
        }
        set_itab_vtable_slot(itab, i + 2, fn)
    }

    // 3. All present: link into cache head and return.
    set_itab_next(itab, read_cache_head(src_ti))
    write_cache_head(src_ti, itab)
    return itab_vtable(itab), true
}
```

The exact field accessors are pointer arithmetic against the layouts in
[typeinfo](#layer-1-typeinfo-grown-from-typedesc) and [interface_desc](#layer-2-interface_desc).

**Single-threaded today.** The cache walk + link sequence is not safe
under concurrent assertion against the same typeinfo, and Boson has no
threads. Adding sync is a separate work item flagged in Open Questions.

**No deallocation.** Itabs live forever — the set of `(typeinfo, iface)`
pairs ever asserted in a program run is bounded by the program's source.
Releasing them would require tracking liveness of every potentially
asserting site; not worth it.

### Why is the helper in its own package?

`_iface.assert_to` is called from compiler-generated code at every
interface-target assertion site. Its symbol must be reachable through
the existing import-and-link mechanism. Putting it in `_iface` —
parallel to `_heap`, `_io_sys`, `_init` — slots cleanly into the
existing build (`mmkfile` adds a `_iface.bo` target alongside `heap.bo`
et al; `cmd/bosc/mmkfile` adds it to the runtime pre-assembly list and
to `test.importcfg`).

`_iface` imports `_heap` (for `alloc`). Nothing else imports `_iface`
directly in source; the compiler injects the import at any compilation
unit that emits an interface-to-interface assertion. Mechanism is the
same as how `_heap` is implicitly pulled in by code that uses
`alloc`/`free`.

## Examples

### Variadic print

```
import "io"
import "fmt"

fn print_line(args ...any) {
    for (var i i64 = 0; i < len(args); i = i + 1) {
        switch (v args[i].(type)) {
            case i64       { fmt.int(io.STDOUT, v) }
            case *byte[]   { fmt.str(io.STDOUT, *v) }
            case stringer  { fmt.str(io.STDOUT, v.string()) }
            default        { fmt.str(io.STDOUT, "??") }
        }
        fmt.str(io.STDOUT, " ")
    }
    fmt.nl(io.STDOUT)
}

fn main() {
    var answer byte[] = "answer:"
    var ok byte[] = "ok"
    var x i64 = 42
    // Slices can't be coerced to `any` by value (see Layer 3), so
    // string literals at variadic call sites must be addressed:
    print_line(&answer, x, &ok)
}
```

(`stringer` is a user- or library-defined interface declaring
`string(*self) byte[]`. The case `case stringer` works for any
concrete type whose method set includes a matching `string` —
satisfaction is checked via the runtime helper at the case site.)

### Cross-package use

```
// in package "point"
pub type Point struct { x i64, y i64 } {
    pub string(p *self) byte[] { ... }
}

// in package "main"
import "point"
import "stringers"   // declares interface stringers.Stringer { string(*self) byte[] }

fn main() {
    var p point.Point = point.Point{x: 1, y: 2}
    var a any = &p
    var s stringers.Stringer; var ok bool
    s, ok = a.(stringers.Stringer)
    if (ok) {
        // First call: helper walks Point's method table, finds string,
        // allocates an itab, links into Point's typedesc cache,
        // returns vtable. Subsequent calls hit the cache.
        io.STDOUT.write(s.string())
    }
}
```

The first assertion of `point.Point → stringers.Stringer` allocates an
itab; the second-and-after assertions of the same pair hit the cache
list rooted at `__typedesc_cache_point_Point`. Cross-package because
the typedesc + cache pair lives in `point`'s `.bo` (single home) and
the iface_desc lives in `stringers`'s `.bo` (single home); the linker
resolves all three to single addresses.

## `.bo` and linker impact

### New symbol shapes

Three new symbol shapes in the `.bo` format. All three reuse the
existing read-only-vs-writable distinction baked into `data`-block vs
`var`-block emission today (data → linker's `Data` map, F_READ
section; var → linker's `Vars` map, F_WRITE section). No new section
kind is introduced and the linker's section emission
(`linker.go:372`) stays as `.text`/`.data`/`.bss` exactly as today.

- **`typedesc <name>`** — supersedes the current 1-byte `data
  __typedesc_<name> byte[1]` emission. Read-only (lives in the linker's
  `Data` map). Carries: name string, size, a relocation to the paired
  cache symbol, method count, and a sorted method table. Per-entry:
  *method name text*, *canonical non-receiver signature text*,
  `name_hash`, `sig_hash`, `receiver_shape`, `fn_ptr` relocation.
  The two text fields are what identity comparison runs against on
  a hash hit (one `memcmp` each, to defeat collisions on either of
  the two independent 64-bit hashes); `receiver_shape` is the
  separate receiver-direction axis, matched against the source's
  derived expected-receiver-shape at assertion time. The writer in
  `bas` and the reader in `gbasm.ofile` / `linker` learn this
  shape; `bdump` learns to print it.

- **`typedesc_cache <name>`** — writable (lives in the linker's `Vars`
  map). A single 8-byte slot, initially zero. One per typedesc, named
  in lockstep (`__typedesc_cache_T` paired with `__typedesc_T`). The
  two symbols are emitted together by T's home package; cross-package
  code references them by relocation. Two separate symbols (instead
  of a single straddling-section symbol) keeps the linker's emission
  path unchanged.

- **`iface_desc <name>`** — read-only (`Data` map). Analogous to
  typedesc but without size, cache_ref, fn_ptrs, or receiver_shape
  (the source direction determines the expected receiver at runtime;
  the iface_desc's source-level declared receiver is recorded only
  for `bdump`/error messages, not for identity). Carries: name
  string, method count, sorted required-method table. Per-entry:
  *method name text*, *canonical non-receiver signature text*,
  `name_hash`, `sig_hash`. The text fields are matched byte-for-byte
  against the typedesc method's during assertion-time identity
  verification. `bdump` learns to print it.

The existing `__vtable_<concreteTypeName>__<I>` emission stays in the
existing `var` machinery — it's still a relocated byte array — but
the layout grows by one slot and the name grows a shape-mangling
segment:
`var __vtable_<base>_<shape_mangling>__<I> byte[(N+2)*8] {
reloc 0 __typedesc_<base> 0; bytes <shape_word_8_bytes>;
reloc 16 <base>.method_0 0; ...;
reloc ((N+1)*8) <base>.method_(N-1) 0 }`.

**Vtable ownership is per-coercer, not per-base-type.** Each package
that performs a `var x I = src` coercion emits the corresponding
vtable in *its own* `.bo`; the vtable is qualified by the coercing
package's name at link time (`linker.go:171` runs `qualify(o.Pkgname,
vname)`). So if both `bar` and `baz` write `var x foo.reader = &y`
for the same `y something`, they each emit a vtable — `bar.__vtable_
something_p1__foo.reader` and `baz.__vtable_something_p1__foo.reader` —
distinct qualified symbols, identical contents. The linker accepts
both because the qualified names differ; no duplicate-rejection
fires. This is the same model the current implementation already
uses for `__vtable_<T>__<I>` and the same model that lets bas auto-
qualify any package-emitted symbol.

The shape-mangling segment in the bare name is still required: within
*one* coercing package, `var x I = m` and `var x I = &m` produce
distinct vtables that must not collide on the bare name (which is
what `NeedVtable` in `cmd/bosc/ast.go:622` dedups against). Across
packages, the qualifier provides separation regardless. The cost is
bloat — one identical vtable per coercing package per `(base, shape,
I)` triple. For a typical program with a few dozen coercion sites
this is on the order of a few KB; acceptable for v1. A future
optimization could teach the linker to content-dedup identical
vtables across packages, but that's out of scope here.

What *is* home-package-owned remains the **typedesc** (and its cache
slot). All vtable copies, no matter which coercing package emitted
them, relocate slot 0 to the single home-package `__typedesc_<base>`,
so typedesc identity is preserved across coercion sites and
assertion identity works uniformly.

`interfaceConcreteTypeName` (`cmd/bosc/compile.go:216`) is replaced
by a base-name helper + a canonical `shape_word(ASTType) u64` + a
short canonical mangling of the shape word for use in symbol names.
The call site in `emitInterfaceFatPtr` (`cmd/bosc/compile.go:5179`–
`5196`) and the `vtableSpec` payload (`cmd/bosc/ast.go:3015`) thread
both the shape word *and* its mangled form through to `WriteVtables`.
No new `bas` directive for the vtable itself.

The typedesc shape, on the other hand, *does* need a real directive
because it carries multiple sub-records and the cache-ref relocation
that `bdump` and the linker need to recognize.

### `cmd/bas` impact

New directives:

```
typedesc <name> <pub?> {
    name "..."
    size <i64>
    cache_ref <cache_symbol_name>
    method <name> <sig_text> <name_hash> <sig_hash> <receiver_shape> <fn_relocation>
    method ...
}
typedesc_cache <name> <pub?>
iface_desc <name> <pub?> {
    name "..."
    method <name> <sig_text> <name_hash> <sig_hash>
    method ...
}
```

`typedesc` and `iface_desc` parse into structured records in
`gbasm.ofile`; `typedesc_cache` is a bare 8-byte writable symbol —
essentially `var <name> byte[8] { bytes "\0\0\0\0\0\0\0\0" }` with a
distinct kind for `bdump`'s benefit. Each `typedesc` directive is
expected to be paired with a `typedesc_cache` of the matching name in
the same `.bo`; `bas` errors on mismatch (so missing pairs are caught
before `bld` ever sees them).

### Linker impact

The linker gets no semantic changes. typedesc and iface_desc land in
the existing `Data` map; typedesc_cache lands in the existing `Vars`
map; section emission (`linker.go:372`) is the same `.text`/`.data`/
`.bss` layout as today. The single-emitter invariant is enforced by
the existing duplicate-rejection at `linker.go:162`/`:170` — if
two `.bo`s both define `__typedesc_T`, the link fails with
`"Duplicate definitions of data __typedesc_T"`. The compiler is
responsible for never producing such a duplicate; the linker just
catches the mistake.

### `bdump` impact

`bdump` learns to recognize the three new symbol kinds and print
them in a readable form. Useful for verifying which package emitted
a given typedesc (debugging single-emitter violations before they
hit the linker) and for inspecting method tables.

## Implementation impact

- **Compiler**:
  - Lexer: add `...` token.
  - Parser: recognize `...T` in parameter types; recognize `x.(T)` in
    postfix position; recognize trailing `slice...` in call args.
  - AST: `Variadic bool` on the last param of a `FuncDecl`; new
    `TypeAssert{ Val AST, T ASTType }` AST node; new `VariadicForward
    { Val AST }` node.
  - Type checker: variadic param's type is `T[]` inside the body;
    method-signature hashing function.
  - Codegen:
    - Variadic call sites: synthesize stack array + per-element
      construction; emit slice header pointing at it.
    - `x.(T)` for T concrete: emit inline two-cmp check (typedesc-
      pointer identity at vtable slot 0; shape-word equality at slot
      1 against the canonical immediate for T).
    - `x.(I)` for I interface: emit the four-register load of
      (typedesc, shape, data, dst_desc) and call `_iface.assert_to`.
    - typedesc emission: replace `emitTypedesc` with the new
      structured form. **One typedesc per declared base type**,
      emitted only in that type's home package; cross-package uses
      emit relocations to it. The typedesc's method table holds T's
      full method set, each entry tagged with `receiver_shape`. No
      per-shape variants — `*T`, `byte[]`, etc. share `__typedesc_<base>`
      and distinguish themselves via the shape word at the coercion
      site.
    - Shape-word computation: at every interface coercion site (and
      every assertion-target lowering) the compiler computes the
      canonical `shape_word` for the source/target type. Pure local
      operation over the canonical `ASTType`; same algorithm in every
      compilation unit.
    - **Coercion-time satisfaction rewrite**: replace the binary
      `pointerDirection := srct.Indirection > 0` reduction in
      `validateInterfaceCoercion` (`cmd/bosc/compile.go:229`) with
      the full shape-word filter the runtime helper uses. Concretely:
      compute `shape_word(srct)` once, derive the *expected* receiver
      shape via `expected_receiver_shape(shape_word)` (the same
      function the runtime helper calls), then for each interface
      method walk T's method set and accept candidates whose
      `receiver_shape` equals `expected_receiver_shape` *and* whose
      non-receiver signature byte-equals the interface method's
      non-receiver signature. The vtable's selected fn_ptrs come from
      that walk.
      `TypeSatisfiesInterfaceAs` (`cmd/bosc/ast.go:549`) and its
      `substituteReceiver` helper need to be reshaped in lockstep:
      the existing `pointerDirection bool` parameter becomes
      `srcShape shape_word`, the value/pointer branch on
      `expectedReceiver` (`ast.go:570`–`577`) is replaced by a
      shape-driven receiver derivation, and call sites
      (`interfaceSatisfactionError` at `compile.go:264`,
      `validateInterfaceCoercion` at `compile.go:230`,
      `TypeSatisfiesInterface` at `ast.go:533`) all thread the
      richer shape parameter. The end-to-end semantics of "value
      direction expects bare T receiver; pointer direction expects
      `*self`-shaped" are preserved — extended to handle `*mut T`,
      `**T`, `*byte[]`, etc. exactly instead of conflating them under
      `Indirection > 0`.
    - iface_desc emission: at each `interface` declaration, alongside
      the existing interface directive. The iface_desc's per-method
      entry carries the non-receiver signature only; the source-level
      declared receiver marker is recorded for `bdump` and error
      messages but **not** part of the assertion-time identity check
      (the runtime helper derives the expected receiver from the
      source's `shape_word`, as documented in [Matching: two-axis
      filter](#matching-two-axis-filter)).
    - **Vtable layout refit**: `emitInterfaceFatPtr`
      (`cmd/bosc/compile.go:5179`–`5196`) and `WriteVtables`
      (`cmd/bosc/ast.go:644`) grow the emitted vtable from
      `(typedesc, method_0, ..., method_N)` to
      `(typedesc, shape_word, method_0, ..., method_N)`. `vtableSpec`
      (`cmd/bosc/ast.go:3015`) carries the shape word.
      `compileInterfaceMethodCall` (`cmd/bosc/compile.go:5054`)
      shifts its `(methodIdx+1)*8` offset to `(methodIdx+2)*8` to
      skip the new shape slot. **Before declaring the layout done**:
      grep for any other reader of `[vtable+...]` at fixed offsets;
      anything still treating `+8` as "first method" is now reading
      the shape word and is silently wrong.
  - Built-in scalar typedescs (`i64`, `byte`, etc.): emitted as part
    of `runtime/builtin` so cross-package assertion to scalars
    resolves to one symbol.
- **`bas`**: parse `typedesc` and `iface_desc` directives; emit the
  structured form into `.bo`.
- **`gbasm.ofile`/`bwrite`**: new in-memory record types for the
  three new symbol shapes (typedesc, typedesc_cache, iface_desc);
  serialization.
- **Linker**: no semantic changes. The two new `data`-block symbol
  kinds (typedesc, iface_desc) and the new `var`-block kind
  (typedesc_cache) land in the existing `Data` and `Vars` maps;
  the existing duplicate-rejection at `linker.go:162`/`:170`
  enforces the single-emitter invariant.
- **`bdump`**: pretty-print the three new symbol kinds (typedesc,
  typedesc_cache, iface_desc).
- **Runtime**: new `_iface` package (`runtime/_iface/iface.bos` or
  `iface_linux.bs`), with `assert_to` and the small accessor helpers.
- **`mmkfile` and `boson.mmk`**: build `_iface.bo`; have
  `cmd/bosc/mmkfile` pre-assemble it for tests; update
  `test.importcfg`.

Approximate scope: 600–900 source lines across the compiler,
assembler, linker, and runtime, plus tests. The typedesc grow is the
biggest single piece by line count; the runtime helper is the
trickiest single piece by design.

## Tests

Integration tests under `cmd/bosc/tests/`:

- `any_basic_test.bos` — coercion to `any`, both value-shape and
  pointer-shape.
- `assert_concrete_value_test.bos` — `x.(i64)` succeed and fail.
- `assert_concrete_pointer_test.bos` — `x.(*foo)` succeed and fail.
- `assert_concrete_shape_mismatch_test.bos` — `x` is value-shape i64,
  `x.(*i64)` fails on the shape-word compare even though both
  share `__typedesc_i64`.
- `assert_pointer_value_distinct_test.bos` — the regression test for
  Background observation 2a: `var a any = &m; var b any = m` (same
  underlying type `i64`), then assert each both ways. `a.(*i64)`
  succeeds and `a.(i64)` fails; `b.(i64)` succeeds and `b.(*i64)`
  fails. Pins the shape-word distinction in place; this test would
  fail against the pre-proposal emission (which collapsed both
  coercions to the same single-slot vtable).
- `assert_mut_distinct_test.bos` — same shape over `*foo` vs
  `*mut foo`, pinning that mut bits move the shape word (both still
  reach `__typedesc_foo`, but their shape words differ).
- `assert_iface_shape_safety_test.bos` — T declares both
  `f(self T)` (value receiver) and `g(*self T)` (pointer receiver).
  Four cross-checks against interfaces `Vi { f(self) }` and `Pi { g(*self) }`:
  - `var a any = small_t_value` (value-shape) → `a.(Pi)` must return
    `false`; `a.(Vi)` must succeed. (Value source must not reach a
    pointer-receiver method.)
  - `var b any = &small_t_value` (pointer-shape) → `b.(Vi)` must
    return `false`; `b.(Pi)` must succeed. (Pointer source must not
    reach a value-receiver method either — exact-match satisfaction.)
  Pins both directions of the runtime helper's
  `receiver_shape`-vs-`src_shape` filter, matching the exact-match
  semantics of `TypeSatisfiesInterfaceAs` (`cmd/bosc/ast.go:549`).
- `assert_iface_success_test.bos` — `x.(stringer)` succeeds, method
  dispatchable.
- `assert_iface_failure_test.bos` — `x.(stringer)` fails, second
  branch taken.
- `assert_iface_cache_reuse_test.bos` — assert twice, verify
  reasonable behavior (mostly that nothing blows up; the cache
  observability is via runtime introspection we don't have, so the
  test exercises the second-call path).
- `variadic_zero_args_test.bos` — `f()` against `fn f(args ...any)`.
- `variadic_multi_arg_test.bos` — mixed value- and pointer-shape args.
- `variadic_forward_test.bos` — `inner(xs...)` from a variadic outer.
- `variadic_typed_test.bos` — `fn sum(xs ...i64) i64` and friends.
- `variadic_lifetime_err_test.bos` — attempt to store the variadic
  slice past the call's frame; rejected by borrow checker.
- `assert_cross_package_test.bos` — concrete in package A, asserted
  in package C against interface declared in B.
- `assert_cross_package_concrete_test.bos` — `.(pkg.T)` where pkg.T is
  declared elsewhere.

Negative tests (`_err_test.bos`):

- `variadic_not_last_err_test.bos` — `fn f(args ...any, x i64)` —
  rejected; variadic must be last.
- `variadic_two_err_test.bos` — two variadic params — rejected.
- `assert_target_not_type_err_test.bos` — `x.(some_non_type_name)`.
- `variadic_value_too_large_err_test.bos` — passing a 32-byte struct
  by value to `...any` — rejected with the existing "value interfaces
  can store at most 8 bytes inline" message.

## Decisions

The following were initially flagged as open and have since been
settled. Recorded here so the implementation has a single source of
truth.

### Bind-on-assertion: tuple form + dedicated type-switch construct

Two surfaces:

**Tuple form (general fallback).** `x.(T)` is an expression whose
static type is `multiretu{T, bool}` — Boson's existing multi-value
return shape. Two binding surfaces accept it:

```
// Declaration form (declares v and ok):
var v T, var ok bool = x.(T)
if (ok) { use(v) }

// Re-assignment form (assigns to pre-existing lvalues):
var v T = ...
var ok bool
v, ok = x.(T)
```

The re-assignment form is the general comma-LHS multi-assignment
`lv0, lv1, … = rhs`, recognized at statement position (it does not
conflict with argument-list / struct-literal commas, which belong to
their enclosing construct). Each target must be an already-declared,
non-`const` variable. Lowering for both is described in
[Layer 3](#layer-3-concrete-type-assertion) and [Layer 4](#layer-4-interface-to-interface-assertion).

**Type switch (the readable form).** A dedicated construct that mirrors
Boson's declaration syntax in its head: `v <type-slot>` where the
type slot is `x.(type)`. Reads as "v is of (the dynamic) type
x.(type)":

```
switch (v x.(type)) {
    case i64       { use(v) }
    case *byte[]   { use(*v) }   // slice-by-value isn't coercible
                                  // into `any` in v1; address it.
    case stringer  { use(v.string()) }
    default        { use_unknown() }
}
```

Cases are top-down, first match wins, no fallthrough. `default` is
optional; if absent and nothing matches, the construct is a no-op
(execution falls through to the statement after the switch). Each
`case T` lowers to:

- The inline two-cmp check from Layer 3 (T is a concrete type:
  base-typedesc identity + shape-word equality), or
- One `_iface.assert_to` call (T is an interface type).

On match, `v` is bound inside the case body with type T — narrowed
the same way nullable narrowing works today (case enters with a
flow-fact that pins `v`'s effective type). Outside any case body, `v`
is unbound; users wanting the original interface still have `x`.

The base tuple form covers ad-hoc one-off assertions; the type
switch covers the common dispatch pattern that drives `fmt.printf`
and friends.

### `any` is the built-in empty interface

Final name: `any`. Reserved at the lexer level.

### Failure caching: not in v1

Negative-result caching deferred. The runtime helper walks the method
table on every miss. If profile data eventually shows a hot
repeatedly-failing assertion, a negative cache entry can be added as
a one-bit shape change to the itab layout (a "missing" tag instead of
a vtable pointer). No structural rework needed; the cache layout is
forward-compatible.

### Concurrency: single-threaded for now

The cache walk + link is not safe under concurrent assertion against
the same typeinfo. Boson is single-threaded today, so this is fine.
When threads land, the link will become either a CAS on the cache
head (load head, build entry pointing at old head, CAS head) or a
per-typeinfo spinlock. The existing layout permits either; no
structural change required.

### Signature identity: two-axis match (non-receiver-text + receiver-shape)

The lookup *index* is 64-bit FNV-1a over the canonical *non-receiver*
signature text (params after the receiver + return type). FNV-1a is
byte-for-byte deterministic across builds, has no state, and is
trivial to implement. It is used only to drive the binary search
over the descriptor's method/req table.

**Identity** is split across two axes, matched independently:

- **Name + non-receiver signature**: the table is sorted on
  `(name_hash, sig_hash, receiver_shape)`; lookup finds the *range*
  of entries sharing that triple and linearly scans the range
  comparing both `name_text` and `sig_text` byte-for-byte. Both
  texts are carried in each method/req entry as `(offset, len)`
  pairs into the descriptor's string blob. Both compares are
  required: `name_hash` and `sig_hash` are independent 64-bit hashes,
  so a single text check on just one would silently accept a
  collision on the other. The range scan handles the rare case where
  two distinct methods share both 64-bit hashes and a receiver shape.
  Together they make `(name, non-receiver-signature)` identity exact
  regardless of hash distribution.
- **Receiver shape**: each typedesc method entry carries a
  `receiver_shape` field; the runtime helper derives an *expected*
  receiver shape from the source's `shape_word` via Boson's
  satisfaction rule and matches it directly. The iface_desc's own
  declared receiver marker is *not* part of the identity check, in
  keeping with the existing checker semantics
  (`TypeSatisfiesInterfaceAs`, `cmd/bosc/ast.go:573`–`577`, where
  value-direction coercion expects a bare T receiver regardless of
  what the interface declared).

Cost is two short `memcmp`s per positive lookup plus a `u64` compare
on `receiver_shape`, negligible compared to the cache-walk + alloc
on the miss path.

(Earlier drafts of this proposal baked the receiver marker into the
hashed signature text, which would have silently changed the
satisfaction semantics — a value-receiver method would have stopped
satisfying a `*self`-declared interface from a value-source coercion,
contrary to the existing checker. Splitting the receiver onto its
own axis preserves today's semantics while still giving deterministic
collision-free matching on the non-receiver sig.)

## Open Questions

### 1. Built-in scalar typedescs in `runtime/builtin`

`__typedesc_i64` and friends live in `runtime/builtin`, alongside the
other built-in type plumbing. The `builtin` package emits *one*
typedesc per primitive base type (`i64`, `i32`, ... `bool`, `byte`).
Composed shapes like `*i64`, `byte[]`, `*byte[]` reference the same
`builtin.__typedesc_<base>` and distinguish themselves via the shape
word at each coercion site — there are no separate typedesc symbols
for pointer or slice variants. Every other package that mentions a
primitive in interface context references the single shared base
typedesc by relocation. This matches the rule that each typedesc has
exactly one home package.

The alternative — "let each use site emit its own
`__typedesc_i64` and have the linker dedup" — was rejected
explicitly: only the home package emits, all others relocate.

### 2. Effect on the fmt proposal

This proposal makes the fmt "no printf in v1" non-goal removable.
Once both this and the v1 fmt land, a follow-on can add
`fmt.printf(w, fmt_string, args ...any)` parsing
`%d`/`%s`/`%v`/`%x` and dispatching via the type switch over each
arg. The fmt proposal's chain-helper Open Question can also be
revisited with variadics in hand.

(The `_iface.assert_to` calling convention is just `(*u8, bool)` — a
multi-value return, handled by the existing ABI with no special
casing.)

## Future Extensions

- **`printf`-style formatting** in fmt, as above.
- **A `Stringer` interface** in fmt or stringers, declaring
  `string(*self) byte[]`. Now expressible with this proposal.
- **Reflection-shaped runtime APIs** built on typeinfo: list methods,
  size, name — pure read-only operations over the existing static
  data.
- **Negative-cache entries** in the itab cache, if profile demands.
- **Thread-safe itab cache** when threads land.
