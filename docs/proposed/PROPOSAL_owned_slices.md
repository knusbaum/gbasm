# Owned slices: constructing, appending to, and draining containers of owned values

## History

- 2026-07-23 — Resolving open questions one at a time. **Q1 resolved:** v1 is the
  pop-stack `owned []owned T`, `free`-time guard is an exact runtime `len == 0`
  check (trap on nonzero = leak); the slot-map and its live-count question are
  deferred. **Q4 (drain loop) deferred:** `free` needs the runtime guard on every
  path regardless, so the loop's static-drained payoff doesn't exist. **Q2
  resolved:** `dispose` is *not* forbidden — it is the coarse zero-runtime-effect
  discharge (verified it clears both levels of a stacked `owned *owned i64`), so
  it validly terminates an elements-owned slice of any backing provenance; this
  dissolves the non-heap concern and needs no `owned(...)` restriction. **Q3
  resolved:** `s = append(s, x)` (consume-and-rebind + `MoveTransfer`); requires a
  prerequisite checker fix for self-consuming reassignment (`x = transition(x)`),
  confirmed to be a bug and logged in `TODO.md`.
- 2026-07-23 — Proposed. Design settled across a long working session:
  ownership≠allocation authority (the keystone that kills append-reallocates),
  capacity decouples `append` from allocation, the two-gate `append` rule, and
  the mutable-view-of-owned prohibition that makes `mut` a sufficient element
  gate. `Vec[T]` deferred to generics. Supersedes the "Ownership in arrays and
  spans" and "Dynamic span / array allocation" roadmap entries in
  [`TODO.md`](../../TODO.md); those now point here.

---

## Motivation

Today there is **no way to build a slice that actually contains owned values.**
The type grammar for ownership-in-containers is settled and correct — `owned []T`
(backing owned, bit 0), `[]owned T` (elements owned, bit 1), `owned [N]T` (whole
array, bit 0), `[N]owned T` rejected — and the encoding round-trips. But the
grammar describes shapes that cannot be *constructed*:

- `make([]owned T, n)` is disallowed: you cannot conjure `n` uninstantiated owned
  elements (same reason an array of non-nullable pointers cannot be declared
  without instantiation). Verified: `[N]owned T` is a parse error, and `make`
  does not exist yet.
- Reslicing produces a **borrowed** view (`s[:]` on an `owned` aggregate strips
  ownership — the `cov_owned_array_reslice_*` work), never an owned container.
- `owned(existing)` promotion produces an `owned []T` whose backing provenance is
  unknown (see below), so it cannot support growth.

The driving use case is concrete: **hold an unknown number of `owned FD`s in a
slice, use them by borrow (`files[i].read(...)`), drain-close at the end.** That
needs a constructor, a move-in operation, borrow-by-index, a move-out/drain, and
a terminal discharge. This proposal specifies all of them, and — just as
important — records *why* each rule is shaped the way it is, because most of the
design is forced by invariants Boson already enforces, and the wrong framing
(allocation-awareness) repeatedly produced unsound designs before the right one
fell out.

---

## The keystone: ownership is a discharge obligation, not allocation authority

Every early attempt to design `append` foundered on the same mistake: assuming an
`owned` slice's backing is heap memory the operation may `free` and reallocate.
It is not. `owned` is a **compile-time obligation to discharge**, and what
discharge *means* is context-dependent — an `owned FD` closes, an `owned *T` from
the heap frees, a borrowed/BYOM buffer releases nothing. The `owned` bit never
claims "this came from the allocator."

The counterexample that settles it is `owned(...)` promotion, which exists today:

```
x owned []T := owned(s)   // s can be a stack `bytes`, a global, a subslice of
                          // someone else's buffer, BYOM memory — anything
```

So an `owned []T` may own a backing that must never be `free`d. Any operation
that reallocates-and-frees the backing (`append`, Go-style growth) has **no basis
in the owned bit** to do so. `free()`ing a promoted stack/global/BYOM backing is
a bug.

**Corollary — allocation authority comes from the *constructor/type*, not the
owned bit.** A container that *knows* it owns freeable heap backing (because its
constructor allocated it, and it is a distinct type that cannot be produced by
`owned(existing)`) may grow/realloc/free soundly. A general `owned []T` cannot.
This single distinction resolves the entire append/growth question below, and it
is the rule to keep sacred: **never reason about ownership in allocation terms.**
Talk about allocation only at the append/free sites where memory genuinely
changes hands — never to explain *why* something is owned.

---

## Consequence: capacity decouples `append` from allocation

If `append` may not freely reallocate, how does it exist at all? By never
allocating. **Allocate capacity up front; `append` only ever moves an element
into an unused slot in `[len, cap)` and traps at `cap`.** An append that merely
fills pre-owned capacity needs *no* allocation authority — it is a move into
storage you already hold — so it works uniformly on a `make`'d or an
`owned(existing)` backing. Provenance only mattered for the free/realloc it no
longer performs.

This requires slices to carry a **capacity**, which today's `{ptr, len}` header
does not. Adding it (a 3-word `{ptr, len, cap}` header) is the enabling change.
The objection that not-storing-cap protects against slices growing into each
other is void — reslicing already hands you overlapping spans:

```
x [10]byte
s1 := x[1:]      // borrowed view
s2 := x[0:8]     // borrowed view, overlaps s1 — legal today
```

Not-storing-cap was never buying overlap safety. For an *owned* slice, safety
comes from **exclusive backing title** (no other live owned span aliases the
backing; borrowed views get invalidated when the append touches the slice), not
from hiding the capacity. Overlapping *owned* spans arise only through `owned(...)`
promotion — the unsafe BYOM escape hatch, where the programmer has explicitly
taken the wheel.

Growth (the allocating part) is therefore **not** a language operation. It is
deferred to `Vec[T]` (see below), which composes the fixed-capacity primitive
with *explicit* `alloc`/`free`. The language provides allocation-free append; the
library provides amortized growth, with every allocation named. Nothing is
hidden the way Go's realloc-or-not is.

---

## The two collection shapes

Two genuinely different containers fall out, distinguished by whether a **hole is
representable**:

| Shape | Backing | Elements | Hole? | Move-in | Move-out |
|---|---|---|---|---|---|
| `owned []owned T` | owned | owned, no gaps | no | `append` (end only) | `pop` / drain |
| `owned []?*owned T` | owned | owned, nullable | yes (null) | `swap(i,x)→old` | `take(i)→…` |

- **The append-vector (`owned []owned T`)** grows from `len 0` by move-in, one
  `append` at a time. It never instantiates `n` owned elements up front — only
  `[0, len)` is ever claimed live; `[len, cap)` is raw, unclaimed capacity — so it
  dodges the "can't make `n` owned elements" blocker by construction. This is the
  primary inline owned-value container and serves the driving FD use case.
- **The slot-map (`owned []?*owned T`)** is a fixed-length backing of nullable
  owned pointers, initially all null (no obligations). `null` is a legal resting
  state, so it supports random-access move-in/out. Holds *pointers* to owned
  things, not owned values inline; a secondary, sparse/indexed tool.

`owned []owned T` obtained via `owned(existing)` (the adopt-a-view case) supports
only provenance-neutral operations and no growth.

---

## `append`: the two-gate rule

`append` does two things — it writes an element into `[len]` and it bumps `len`.
The write is gated by the **element** discipline, not the header discipline:

- **Header reassignment / reslice** (`s = s[:1]`) is gated on **binding
  mutability** (`var`, i.e. `mut []T`). It repositions/narrows the window within
  existing bounds and claims nothing.
- **Element write** (what `append` does) is gated on **`[]mut T`** — mutable
  *elements*. These were always separate bits; `append` needs the element one.

For a **non-owned element**, `[]mut T` is the *full* gate. Appending into a
borrowed subslice with spare capacity is then naturally allowed, no special
"appendable" concept required:

```
var x [1024]byte
var y []mut byte := x[:0]   // borrowed view, mut elements, cap 1024 (end of x)
y = append(y, 10)          // writes x[0], len→1 — a mut element-write into cap
```

Every append here is `x[len] = v; len++`, a mutation of storage `y` is permitted
to mutate, bounded by `cap`. Safe for the same reason `x[0] = 10` is. The only
hazard is the aliasing-stomp already possible via reslicing (another live view of
`x` sees the writes) — not a new unsafety. (Verified today:
`var x [1024]byte; var y []mut byte := x[:0]` typechecks, `mut` propagating from
`x` being `var`.)

For an **owned element**, `[]mut T` is *not* sufficient: the moved-in value
carries an obligation that must live in storage whose lifetime you control. So
owned-element append additionally requires **backing ownership**:

- `append(y []mut T, v T)` — non-owned element. Gate: `[]mut T`. Backing may be
  borrowed.
- `append(s owned []owned T, v owned T)` — owned element. Gate: backing-owned
  **and** element-owned. You cannot append an `owned FD` into a *borrowed*
  backing, because whoever really owns that backing could `dispose` it and strand
  the FD's obligation (freed as bytes, never closed). The obligation needs a home
  you will drain before you release.

Both are "write an element into capacity"; the owned case adds "…and the
obligation must live somewhere you control."

### `append` signature, and why `s = append(s, x)` is legal

`append` on the owned-element slice is a **consuming state-machine transition**:

```
fn append(s owned []owned T, v owned T) owned []owned T
//         ^consumes the slice  ^consumes the element  ^returns the rebound obligation
```

Both `owned` parameters are consumed; the return re-initializes `s`. So `s =
append(s, x)` is **not** a naked mutation of a live owned binding (which is
rejected — see the mutation rule below). The RHS consumes `s`'s current value
*before* the assignment re-inits it: the state-machine shape `s = transition(s)`,
threading the obligation through rather than dropping it. (The non-owned form
`append(y []mut T, v T)` takes a plain `T` and needs none of this: `y` isn't
owned, so `y = append(y, v)` never trips the owned-assign gate.)

### `append` does not invalidate other spans (`MoveTransfer`)

Although the spelling is consume-and-rebind, `append` must behave like **creating
a field at runtime** — the slot at `[len]` was inaccessible (dead capacity) and
becomes live; no existing element is overwritten and the backing does not move
(no realloc, ever). So borrows into `[0, len)` must **survive** the append. This
is exactly `MoveTransfer` semantics (checker.go): the storage transfers to the
rebound `s` with its flow origin *preserved*, so aliases of that storage stay
live — as opposed to `MoveConsume`, which would kill them. Because `append` is a
builtin, the compiler *knows* its return aliases `s`'s backing and can emit
`MoveTransfer`. (A tiny bit of append-specific knowledge today; in the future the
same aliasing could be expressed generally through subfield/return ownership
info rather than special-cased.) `s` must be a mutable binding (`var`) to be
reassigned, plus the element gate above.

### Prerequisite bug: self-consuming reassignment is rejected today

`s = append(s, x)` is the self-reassignment shape `s = transition(s)`, and that
shape is **rejected today** — a confirmed bug, not a load-bearing restriction.
The scalar analogue fails identically:

```
var x owned i64 := 5
x = transition(x)   // transition(v owned i64) owned i64
// REJECTED: "Cannot assign to owned binding 'x' before consuming its current value"
```

The checker tests LHS assignability *without* crediting that the RHS already
consumed the target. DESIGN.md's state-machine section only ever uses fresh
bindings / nested calls (`process(start())`), so this in-place typestate-update
idiom was never exercised — but the ownership system was **designed with it in
mind**. The fix: **process the RHS's consume effects before the LHS
overwrite-safety check.** Then `s = append(s, x)` and general `x = transition(x)`
both work, while `s = f()` (where `f` does not consume `s`) is still correctly
rejected as an obligation drop. This fix is a prerequisite for the owned-slice
`append` and is worth landing on its own; tracked in [`TODO.md`](../../TODO.md).

---

## The keystone that makes `mut` a sufficient element gate

The claim "`[]mut T` is the full gate for non-owned elements" is only safe if you
**cannot construct a `[]mut T` whose elements are owned.** Boson's ownership model
already guarantees this: *borrowing an owned resource strips the mutable-view
capability, not merely ownership.* A slice is a **view** (it shares its backing),
so a `[]mut T` over owned elements would be a mutable view of owned storage —
forbidden by the same rule that rejects a `*mut` pointer into an owned slot.

Verified today on the scalar/pointer foundation:

```
var x owned i64 := 5
var z *mut i64 := &x   // REJECTED: "Cannot take mutable address of owned binding"
var z *i64 := &x       // allowed — immutable borrow of an owned resource is fine
```

So any `[]mut T` you can hold necessarily has non-owned elements, and `mut`-on-
the-element is a genuinely sufficient append gate.

### Copy vs view — why the header can be a mutable copy of an owned slice

There is an apparent exception that is actually the mechanism working:

```
var y i64 := x         // ALLOWED — a mutable *copy* of an owned scalar
```

This is allowed because it is a **copy**, not a view: DESIGN.md's "value coercion
is alias, not copy" duplicates the bit pattern into `y`'s own storage with a
flow-alias back to `x` for use-after-move tracking. Mutating `y` never touches
`x`; reading `y` after `x` moves is rejected. A mutable *copy* of an owned scalar
is fine (independent storage); a mutable *view* is not.

A slice header is itself a value, so the same distinction applies to it:

```
var x owned []owned T := make(...)
var y []T := x[:0]     // ALLOWED — mutable *copy* of the header, borrowed *view*
                       //           of the elements
// y can be resliced (mutable header copy): y = y[:1]
// y cannot append: it is []T, not []mut T — the element view is not mutable
```

`y` gets its own `{ptr, len=0, cap}` header (a copy, like the scalar `var y i64
:= x`), pointing into `x`'s backing (shared, borrowed). The header copy is
mutable, so reslice works; the element view is `[]T` (mutable-view-of-owned
stripped), so append does not. This unifies the whole picture: **the header is a
value that may be copied mutably even from an owned source; the backing is a view
that is borrowed and mut-stripped.** `y` is tracked as a borrow rooted at `x`'s
origin (reslice-borrow machinery), so disposing `x` invalidates `y`, and
reslicing `y` keeps it rooted at `x`.

---

## Teardown: `pop`, `swap`/`take`, and the terminal

Move-out and random access, chosen to be self-contained (no special loop shape,
no scoped access rules):

- **`pop(s owned []owned T) -> (owned []owned T, owned T)`** — take the last
  element off, return the shortened slice and the element. Provenance-neutral (no
  backing allocation touched). Preferred over a `for owned x in s { … }` drain
  intrinsic, which is a nice-to-have layered on top (defer it; and even with it,
  the terminal `free` still needs an emptiness check, so the loop buys ergonomics,
  not a cheaper terminal).
- **`swap(i, x) -> old`** — move `x` into slot `i`, return the displaced owned
  value. Total on `[]?*owned T` (old may be null); on `[]owned T` it always
  returns an `owned T` the caller must handle. Named `swap`, not `set`, because it
  conserves an obligation in each direction. Overwriting a live slot needs `mut`
  in addition to the owned discipline (unlike `append`, which initializes a dead
  slot).
- **`take(i) -> …`** — move out at `i`; needs a representable hole, so it is
  random-access only on `[]?*owned T`. On `[]owned T` you can only `pop` the end
  or drain.

`s[i]` (raw index read) yields a **borrow** — you never move an element out by
raw index. Symmetrically, raw indexed *store* (`s[i] = v`) into an owned-element
slice is **forbidden**: a blind store either drops the displaced obligation
(leak) or would need a runtime null-check for a property that should be static.
All obligation-crossing goes through the named operations above, which conserve
obligations by construction.

- For **non-nullable** `owned []owned T`, every in-bounds slot is always live, so
  `s[i] = p` is *unconditionally* a leak → forbidden outright, no runtime question.
- For **nullable** `[]?*owned T`, null is the only non-leaking case, and
  `swap(i,p)→old` hands the displaced value back statically — so still no runtime
  trap.

### The terminal: `free` (checked heap release) or `dispose` (coarse escape)

Discharge differs by which owned bits are set:

| Type | Terminal |
|---|---|
| `owned []T` (backing only) | `free(s)` (heap) **or** `dispose(s)` (no runtime release) |
| `owned []owned T` (elements owned) | drain elements out (`pop`/`take`) then `free(s)` (runtime `len == 0` assert, releases heap backing) — **or** `dispose(s)` (discharges backing + all elements, no runtime effect, leaks by choice) |

Two complementary terminals, **not** a guarded-vs-forbidden split (an earlier
draft wrongly forbade `dispose` here):

- **`free(s)`** — a runtime op that releases the backing. It carries the
  leak-guard (assert `len == 0` before releasing — same family as `index_oob` /
  `nil_assert`) and assumes **heap** backing. This is the checked path: drain the
  elements individually (`pop`/`take`), then `free` releases the (now provably
  empty) backing.
- **`dispose(s)`** — the coarse escape, with **zero runtime effect**, so it is
  always memory-safe (it frees nothing, so it cannot free the wrong backing or
  double-free). It discharges the backing obligation *and* every element
  obligation at once — exactly as `dispose` on a stacked `owned *owned i64`
  discharges both levels (verified). Nothing is closed or freed; that is
  `dispose`'s explicit "I've accounted for it, no cleanup needed" contract, and
  leaking-by-choice is what it is for.

Because `dispose` does no runtime work, it is a valid terminal for **any** backing
provenance, including non-heap. That is what makes the promotion question below
(Q2) go away: a stack/global-promoted elements-owned slice discharges through
`dispose`; a heap one can additionally use `free` when drained.

---

## `Vec[T]` is a library, not a builtin

A growable, amortized dynamic array is `Vec[T]`, written in **ordinary Boson over
generics** — not a compiler builtin. A builtin Vec with methods and a constructor
would be a second, privileged object system nobody else can write; it should be
plain Boson. It therefore **waits on generics.**

`Vec[T]` composes the fixed-capacity primitive with explicit `alloc`/`free`:
internally it holds a heap-owned backing with capacity; when full, Vec code
allocates a bigger backing, moves the elements over, and frees the old one — all
visible Boson, at named sites. Its constructor is the allocation-authority
guarantee (it is a distinct type, never produced by `owned(existing)`), which is
exactly what makes its growth sound where a general `owned []owned T`'s would not
be. Growth's "hidden allocation" concern dissolves: the language never reallocs;
the library does, explicitly.

---

## Reslice always borrows; whole-slice move is plain assignment

Reslicing an owned aggregate is **always** a borrow (it strips ownership),
producing a `[]T` view rooted at the owner's flow origin. It never moves. A
*partial* reslice cannot move (you cannot hand off "half" a backing obligation),
and a full-extent reslice-that-moves would just be a footgun alias for assigning
the binding. **Whole-slice ownership transfer is `x := s`** (assign the binding
itself), not `x := s[:]`.

---

## Implementation notes and latent gaps

The current toolchain is correct for what is constructible today, but three
element-level paths are unimplemented precisely because owned-element slices
cannot yet be built. They are the spots to get right when they become
constructible:

1. **Reslice must strip the element bits, not just the backing bit.** Today
   `SliceOp.ASTType` does `t.OwnedMask &^= 1` (strips bit 0, backing-owned) and
   preserves the element `owned` (bit 1) and element `mut`. That is correct while
   the only constructible owned slices are `owned []T` / `owned [N]T`. For
   `owned []owned mut T`, a borrowed view must yield `[]T` — stripping **element-
   owned (bit 1)** and **element-`mut`** as well — so the result is a fully-
   borrowed, non-mutable element view. Getting this wrong is what would let a
   `[]mut T` form over owned elements and leak the gate.

2. **Element-`mut` stripping on an owned-element borrow must be enforced exactly
   like address-taking.** `&`-of-owned already refuses to yield `*mut` (verified:
   "Cannot take mutable address of owned binding"). The element-borrow path
   (`[]owned mut T` coerced/resliced to a view) must apply the same rule:
   `[]owned mut T` ↛ `[]mut T`, only ↛ `[]T`. Mirror `checkAddressOfOwnedForDest`.

3. **The flow checker collapses everything through `HasOwned()`** — any owned bit
   makes the whole binding one obligation, discharged coarsely by `dispose`. It
   does not distinguish `owned []T` (backing) from `[]owned T` (elements) and has
   no per-element notion. Element-granular `append`/`pop`/`swap`/`take` need the
   checker to track container-level vs element-level obligations separately, and
   the terminal `free` needs the runtime drained-assert.

Enabling change under all of this: the **3-word `{ptr, len, cap}` slice header**,
which touches header construction, reslice's `cap` computation (Go's rule: `cap`
runs to the end of the backing), `make` with-cap forms, and a `cap()` intrinsic.

---

## Open questions

1. **RESOLVED (2026-07-23) — v1 is the pop-stack; the guard is `len == 0`.**
   `append`/`pop` maintain the invariant *every slot in `[0, len)` holds exactly
   one live owned element; `[len, cap)` holds none* (append extends the live
   region by one; pop moves the element out — transferring its obligation to the
   returned `owned T`, leaving no residual in the dead slot — and shrinks `len`).
   So `len == 0` is not a heuristic but an *exact* characterization of drained-ness,
   checkable with one comparison. `free(s owned []owned T)` traps on `len != 0` (a
   leak) and otherwise releases the backing: `free` discharges the backing
   obligation (bit 0); the `pop`-loop discharged the element obligations (bit 1)
   individually. The nullable slot-map (`owned []?*owned T`, where `len` stays `n`
   with null holes, so "drained" = "every slot null" — an O(n) scan or a
   maintained live-count) is **deferred with the slot-map itself**; that
   sub-question does not arise in v1. The runtime trap is the accepted price of
   not having the static drain loop — see Q4.

2. **RESOLVED (2026-07-23) — no restriction; `dispose` is the universal
   terminal.** The non-heap worry assumed `free` was the only terminal and
   `dispose` was forbidden. Un-forbidding `dispose` (see the terminal section)
   removes the problem: `dispose` does zero runtime work, so it validly discharges
   an elements-owned slice of *any* backing provenance (stack, global, BYOM, heap)
   — it just leaks by choice. `free` remains the checked heap release. So
   `owned(...)` promotion **into** an elements-owned slice needs no special
   restriction: a promoted slice discharges through `dispose`; a heap slice can
   also `free` when drained.

3. **RESOLVED (2026-07-23) — `s = append(s, x)`, consume-and-rebind +
   `MoveTransfer`.** `append(s owned []owned T, v owned T) owned []owned T`
   consumes both `owned` params and returns the rebound obligation, so the
   assignment is a re-init (state-machine shape), not a drop. `MoveTransfer`
   preserves `s`'s backing origin across the rebind so borrows into `[0, len)`
   survive ("creating a field"). Requires the prerequisite checker fix — crediting
   RHS consumption before the LHS overwrite check — which also fixes the general
   `x = transition(x)` self-reassignment bug (see the append section and
   `TODO.md`). A `*mut` header parameter was rejected: you cannot take a `*mut` of
   an owned binding (the same rule that gives us the `mut` element gate).

4. **`for owned x in s` drain loop — DEFERRED (2026-07-23), and its payoff
   doesn't exist.** The loop's only marginal benefit over `pop` would be a
   *static* drained-ness guarantee that drops the `free` trap. But `free(owned
   []owned T)` is a general operation callable however the slice was drained, and
   we do not want to forbid it — so it must carry the runtime `len == 0` guard on
   every path regardless. The loop would give static emptiness only along its own
   path; it cannot remove a trap `free` needs anyway. So the loop buys ergonomics
   only, and is deferred. If it is ever added, the open sub-question is early exit
   (`break`/`return`/mid-loop error) leaving the remainder and backing undischarged
   — options: forbid early exit (leaning); treat the slice as still-live/
   inconsistent past the loop (second discharge required); force-discharge the
   remainder (with what, for an `owned FD`?).

---

## Minimal v1 vs. later

- **v1 (no generics):** 3-word header + `cap()`; `make([]T, len, cap)` including
  `make([]owned T, 0, cap)`; `append` (two-gate, allocation-free, traps at cap);
  `pop`; `s[i]` borrow; terminal `free` (drained-assert) **or** `dispose` (coarse,
  any backing); the `s = append(s, x)` re-init spelling (Q3) and its checker fix;
  reslice/element-`mut`-strip enforcement (notes 1–2). Serves the driving FD use
  case with a chosen-up-front capacity.
- **Later:** `swap`/`take` and the `owned []?*owned T` slot-map; the
  `for owned x in s` drain; `Vec[T]` once generics land.
