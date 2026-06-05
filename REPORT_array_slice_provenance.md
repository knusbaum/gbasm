# REPORT: Indexed-aggregate provenance model

## Summary

The escape checker's `arr.[]` element-fact bucket is unsound under
element-by-element overwrites. Writing a known-safe element silently
erases a prior borrowed-element fact, even though the borrowed value
is still sitting in some *other* element of the same array.

Repro (compiles cleanly today; should be rejected):

```boson
package main
var g byte[]
fn bad(s byte[]) byte[] {
    var arr byte[][2]
    arr[0] = s              // arr.[] = OriginBorrowed(s)
    arr[1] = g[:]           // arr.[] overwritten with Unknown — borrow fact lost
    return arr[0][:]        // arr.[] is Unknown → silently accepted
}
fn main() {}
```

`arr[0]` still holds the borrowed `s` at runtime; the returned slice
escapes; the checker doesn't know.

This is **not** the partial-overwrite class of bug just fixed in the
struct-literal path (`recordStructLiteralFieldFacts` is sound for
structs). The bucket issue is structural to how indexed aggregates are
tracked, and the kind difference between structs and arrays/slices is
what motivates this report.

## Why this is structural

Boson has three storage shapes the fact model has to handle:

1. **Scalar bindings** — `var x i64`. One name, one value, one fact
   slot. Exact.

2. **Structured aggregates** — `var b B` where B has named fields. Each
   field has its own statically-knowable path (`b.inner.buf`). Writes
   are localized; per-field facts are exact. The path-keyed
   `fieldPointers` table handles this correctly today.

3. **Indexed aggregates** — `var arr T[N]`, slices `T[]`. Elements
   are addressed by runtime values. The compiler can't in general
   statically distinguish `arr[i]` from `arr[j]`. The path key `[]`
   collapses every element to one bucket.

Path-based per-slot facts work for (1) and (2) and break for (3).

## Read aggregation vs write aggregation

The single `[]` bucket aggregates element facts. There are two ways to
aggregate, and they want opposite semantics:

- **Read aggregation** — "what could a read of `arr[i]` see?" Answer:
  the union of every value ever written into the array since the last
  whole-array overwrite. Monotone-growing.

- **Write aggregation** — "what does writing `arr[j] = v` overwrite?"
  Answer: one slot, not all slots. The bucket can't be replaced by the
  new write's fact alone, because other elements still hold prior data.

The current code conflates these: one bucket, every write replaces it.
That's the unsoundness.

## Short-term: monotone-join the `[]` bucket

The minimal sound fix:

- An element write to `arr.[]` only *adds* escape-restriction; it
  cannot remove it. If the existing bucket holds an escape-restricted
  origin and the new write's source is Unknown, the bucket stays
  escape-restricted.
- Whole-array overwrite (Symbol-copy of the full array, an explicit
  array-fill that touches every element, fresh allocation) clears the
  bucket — at runtime, every element really is replaced.

This is sound and conservative: an array that ever held a borrowed
slice "remembers" it forever, until the whole array is overwritten.

Implementation is small: a join helper for SetPathPointer at `[]` paths
that picks the more-restricted of (existing bucket, new write).

## Medium-term: constant-index precision

Constant indices (`arr[0]`, `arr[1]`, …) could be tracked as per-index
buckets (`arr.[0]`, `arr.[1]`, …), with runtime-indexed reads/writes
falling back to a join over all known per-index slots.

- Sound: each per-index slot is exact for constant-indexed access.
- More precise: `arr[0] = s; arr[0] = clean; return arr[0][:]` no
  longer trips the join — the specific slot was overwritten.
- Bounded growth: only constant-indexed writes produce facts; bound by
  source occurrences of `arr[N]`, not by array size.

Strict improvement over short-term for code that uses constant indices,
with the same soundness guarantee. Worth doing the moment short-term's
conservatism causes friction in real code.

## Slices are not just arrays

Slices add an indirection: the slice header aliases backing storage
that the slice value itself doesn't own. The implications for the fact
model:

- A locally-rooted slice (sliced from a known local array) — writes
  through `s[i]` write to the array's backing. The `arr.[]` bucket
  should reflect that. We don't currently track this; writes through
  slice elements are conservatively gated by `targetLifetimeOpaque`
  (treated as opaque destinations).
- An opaque-rooted slice (parameter, field, slice-of-slice) — backing
  is unknown. Writes through `s[i]` are rejected if the source is
  escape-restricted; reads conservatively return Unknown.

The slice case is structurally harder because the indirection breaks
the binding-rooted path model entirely. Without lifetime annotations
saying "this slice's data lives at least as long as that binding,"
slice element writes from non-trivial sources have to be rejected.

The current behavior is consistent with this: slice-target writes are
opaque-gated. The element-bucket issue specifically affects fixed
arrays where the binding root is known.

## Long-term: lifetime annotations

The complete answer is lifetime parameters: `arr[i]` has lifetime ≤
`arr`'s, `(*s)[i]` has lifetime ≤ `*s`'s target lifetime. Writes from
shorter-lived sources are rejected exactly; reads carry the right
lifetime forward. This is the same shift that closes the
parameter-slice-return story too. Not blocking — but it's the
structural endpoint.

## Recommendation

Land short-term (monotone-join `[]`) when the appetite is there. It's
sound, small, and closes the repro above. Document the "tainted
forever" semantics in passing — users will hit it on legitimate code
that overwrites all elements element-by-element rather than via a
whole-array copy, and the workaround is "copy a clean array in" rather
than reusing the same array.

Reserve medium-term (constant-index buckets) for when (1)'s
conservatism causes a real test to fail. Reserve long-term (lifetime
annotations) for the broader Boson lifetime conversation; it lands the
array/slice case as a free corollary.

## Open questions

- **Nested array-in-struct**: `B{ items: T[N] }` — writes to
  `b.items[i]` should taint `b.items.[]`. The path key works already
  (`b.items.[]`); the join semantics need to apply at any depth, not
  only at the top level.
- **Address-of-element**: taking `&arr[i]` should taint the bucket
  conservatively, since later writes through the resulting pointer
  can't be tracked. Today this path is rejected at the `Address` site
  for most shapes, but worth re-auditing once short-term lands.
- **Slice header reassignment**: `s = otherSlice` clears at the slice
  binding level; correct, but interactions with array-backed slices
  (where `s` was sliced from a tracked local array) need verification
  when short-term lands — the new slice's backing might be a different
  array whose bucket has different taint.
- **Whole-array literal overwrite**: an array literal `arr = [a, b, c]`
  that touches every element should clear the bucket as a whole-array
  overwrite. The literal-init codegen and the fact-model boundary need
  to agree on what "whole" means.
