# Proposal: Interface Borrow Contracts (`from` clauses)

## Status

Draft, design settled, not yet implemented. The direct, planned follow-on to
[`PROPOSAL_return_alias_inference.md`](PROPOSAL_return_alias_inference.md)
and its engine
([`DESIGN_return_alias_engine.md`](DESIGN_return_alias_engine.md)),
both of which shipped. This proposal *relaxes* the strict interface
guard those introduced; it does not revise the inference itself. The review
design questions are resolved (see [Decisions](#decisions)); **v1 scope is
interface-methods-only**, with the optional concrete/free-function `from`
(D2/D4) a decided fast-follow.

This is the "option (b)" the return-alias proposal deferred. Return-alias
inference lets a *function* return a borrow of its parameters, inferring
which. An *interface method*, dispatched dynamically, has no body to infer
from at the call site — so v1 (option (a)) coarsely forbade any
borrow-returning type from being coerced to any interface (even `any`).
This proposal adds syntax for an interface method to **declare** its borrow
contract, so borrow-returning types can satisfy interfaces that opt in, and
the borrow is tracked through virtual dispatch.

## Summary

A return type in an **interface method** may carry a `from(...)` clause
naming the parameters its result may borrow:

```
pub interface stringer {
    string(self *self) byte[] from(self)
}
```

`from(self)` says "the result may alias the receiver." This is exactly the
fact the return-alias engine already computes for concrete functions
(`ReturnAliases`), now *declared* on the interface instead of inferred —
because at a virtual call site there is no implementer to inspect, and the
caller needs the fact statically.

- **A concrete type satisfies the interface** iff each of its methods'
  *inferred* alias sets is a **subset** of the interface method's
  *declared* set (per return slot). An implementation may borrow *less*
  than declared (a static-returning `string()` satisfies `from(self)`); it
  may not borrow *more*.
- **At a virtual call site**, the compiler reads the interface's declared
  contract — statically, **with no per-dispatch cost and nothing read from
  the vtable** — and propagates "the result borrows the actual argument at
  each declared position." For `from(self)`, the result aliases the
  interface value's referent. The existing flow machinery carries the
  chain.
- **The runtime assertion / type-switch path is contract-aware.**
  Interface-to-interface assertion (`x.(K)`) and type-switch build an itab
  from the concrete type's full method table at runtime
  (`_iface.assert_to`). For soundness this matching must consider the
  borrow contract: the assertion succeeds only when each implemented
  method's recorded borrow set is ⊆ the target interface's declared set.
  This is what stops a borrow from being laundered to an under-declaring
  interface, and it is why the contract is carried (as a compact
  per-method descriptor) in the typedesc and iface_desc that `assert_to`
  reads. Cost is one mask compare per matched method — paid only on
  assertion, never on a normal virtual call.
- **The default is no clause = borrows nothing**, so every existing
  interface is unchanged and the contract is strictly opt-in.

The interface guard from the return-alias work relaxes accordingly: a
borrow-returning type may be coerced to an interface *that declares a
covering contract*, instead of being rejected outright — and entry is no
longer denied wholesale (even to `any`), because the assertion path is now
gated at runtime rather than by forbidding entry. Borrowed-field
`stringer` comes back.

## Motivation

Three things converge:

- **The strict guard is coarse.** Today a concrete type with *any*
  borrow-returning method cannot be coerced to *any* interface — not even
  `any`, not even interfaces that never call the borrowing method. That was
  the sound, shippable v1, but it excludes a real and useful pattern: a
  type that renders itself by returning a view of its own bytes.
- **`fmt.stringer` is the concrete casualty.** Its documented contract
  *wanted* `string()` to be able to return a borrowed view of `self`; the
  guard forced "must return bytes that outlive the receiver" and pushed the
  borrowed-view case to `Formatter`. The split should be the interface
  author's *choice*, not a blanket prohibition.
- **The fact is already computed.** `ReturnAliases` per concrete method is
  inferred and serialized. The only missing piece is a way to *declare* the
  same fact on an interface so dynamic dispatch can use it. No new analysis;
  a new declaration plus a conformance check plus call-site propagation.

This is also the design Rust validates: a trait method's lifetimes
(`fn string(&self) -> &[u8]` ≡ `fn string<'a>(&'a self) -> &'a [u8]`)
declare the borrow relationship in the signature, every impl conforms, the
call site reads it statically, and the vtable carries nothing (lifetimes
are erased). Boson's `from(...)` is the same idea expressed as
aliasing-provenance (which parameter) rather than named lifetimes.

## Vocabulary (what is — and isn't — tracked)

Three terms that are *not* synonyms:

- **Lifetime** — a property of *storage*: the span of program-time a
  location is valid. A local's is its scope; a `.rodata` static's is the
  whole program; a parameter's referent has the caller's argument's
  lifetime.
- **Alias** — a relationship between two *views*: they refer to the same
  storage. Structural; says nothing about time.
- **Borrow** — a non-owning *view* whose validity is *bounded by* the
  aliased storage's lifetime.

`ReturnAliases` (and therefore `from(...)`) is the **aliasing** fact,
tracked **solely as a lifetime-bounding device**: "the result is a view
into parameter *p*'s storage," which the caller composes with *p*'s known
lifetime to bound how long the result is usable. It is **not** a statement
about *effects*. In particular:

> `from(self)` is a **lifetime contract** — "the result is valid no longer
> than `self`" — **not** a "writes through the result reach `self`"
> contract.

For an **immutable** return the alias has no observable consequence beyond
the lifetime bound (you can only read). For a **mutable** return the alias
*additionally* implies writes-reach-the-source — but that is a *semantic*
property the language does not check (it is not derivable from
`ReturnAliases`, which knows "comes from `self`," not "writes propagate to
`self`"). An implementation that conforms to the lifetime contract but
returns a non-aliasing mutable buffer is a **correctness** bug, not a
soundness one — the program stays memory-safe; the function merely doesn't
do what its name implies. See [Soundness](#soundness-and-the-mutable-case).

## Goals

- Let an interface method **declare** which parameters (including the
  receiver) each return slot may borrow, expressing exactly the per-slot
  parameter-index sets the engine computes — no more (no lifetime ordering,
  no mut/shared distinction), no less.
- Permit a borrow-returning concrete type to satisfy an interface whose
  declared contract **covers** its inferred borrows; reject otherwise with
  a directed diagnostic.
- Track the borrow **through virtual dispatch** so a result borrowed from
  the receiver cannot outlive the interface value — with **no per-dispatch
  cost** (the static virtual-call path reads the contract from the
  interface's static type). The runtime assertion/type-switch path pays one
  mask compare per matched method, off the hot path.
- Keep it **opt-in and backward-compatible**: no clause means no borrow,
  every existing interface unchanged.
- Restore borrowed-field `stringer` as an interface-author choice.

## Non-goals

- **No named lifetimes, no lifetime ordering.** `from(p, q)` names sources;
  it cannot express "outlives," HRTB, or relationships Rust's region
  calculus can. The expressiveness is exactly `ReturnAliases` (`[][]int`).
- **No effect contracts.** `from(...)` promises a lifetime bound, not "the
  mutable result writes through to the source." The optional `==` exact
  form ([declined — Future extensions](#future-extensions)) is where that
  would be revisited, not `from` itself.
- **No mandatory annotation on concrete methods.** Concrete borrow-ness
  stays inferred. A `from(...)` clause on a concrete method is *optional*
  (an assertion checked against inference); it is never required to satisfy
  an interface.
- **No change to the inference engine.** This consumes `ReturnAliases`; it
  does not recompute it.

## Background: how things stand (verified at HEAD)

- **Interface methods** are `InterfaceMethodSig{Name, Params[], Return, p}`
  with `Params[0]` the receiver (`*self`) — `ast.go:3102`. They have no
  alias field today.
- **Conformance** is `TypeSatisfiesInterfaceAs` (`ast.go:586`): per
  required method, match by name, receiver shape, and `Same` param/return
  types. No aliasing consideration.
- **The strict guard** is `rejectBorrowingMethodCoercion`
  (`cmd/bosc/retalias.go`), called from `validateInterfaceCoercion`: a type
  with *any* method whose inferred `ReturnAliases` is non-empty is rejected
  from *every* coercion (the sole `emitInterfaceFatPtr` chokepoint).
- **The fact** is `FuncDecl.ReturnAliases [][]int` / `Function.ReturnAliases`,
  emitted as the `retaliases` directive and serialized through the `.bo`.
- **Interface transport**: `InterfaceShape` via `writeInterfaces` /
  `readInterfaces` (`bwrite.go:814/857`), the `interface` directive in bas
  (`cmd/bas/main.go:394`). No per-method alias field today.
- **Virtual dispatch** result origin: a call through an interface currently
  yields no tracked origin (the result is opaque) — this is what the strict
  guard's exclusion makes safe by denying entry.

## Design

### Syntax

A `from(...)` clause may follow any **interface-method** return-type
position. The names refer to that method's parameters; `self` is a keyword
denoting the receiver regardless of the receiver param's name (or whether
it is named):

```
stringer  { string(self *self) byte[] from(self) }
keyed      { at(self *self, k byte[]) byte[] from(self) }
either     { pick(self *self, a byte[], b byte[]) byte[] from(a, b) }
```

**Multiple sources** are just multiple names in the list. The dominant
multi-source shape is an aggregate return whose fields each borrow a
different parameter:

```
type pair struct { x *foo, y *foo }

// a method returning a struct that borrows BOTH params
joiner { make(self *self, x *foo, y *foo) pair from(x, y) }
```

Here `make`'s result borrows `x` and `y` simultaneously — the caller must
keep both alive while the `pair` lives. Note that `from(a, b)` is the same
declaration whether the result borrows *either* of two params (the `pick`
above) or *both* at once (this `pair`): the model is **may-alias**, so OR
and AND collapse to the same union `{a, b}`, and the caller's obligation —
keep every listed source alive — is identical either way.

`from(...)` is **parameter-level, per return slot** — it names which
parameters a slot may borrow, *not* which field borrows which. `from(x, y)`
on the `pair` says "the returned `pair` borrows `x` and `y`," not "field
`.x` borrows `x` and field `.y` borrows `y`." This matches exactly what the
inference engine computes (`ReturnAliases` is `[][]int` — per slot, a set
of params; no field paths), so a caller keeps *both* sources alive even if
it only ever reads `pair.x`. Per-field precision (`pair.x` depends on `x`
alone) is the deferred [field-level extension](#future-extensions), on both
the inference and the `from(...)` side symmetrically.

**Parentheses are required** and disambiguate from the comma-separated
multi-return list. Boson writes multi-return as `T1, T2` (no enclosing
parens), so a bare suffix would be ambiguous —
`byte[] from self, i64` could read as `from (self, i64)` (nonsense) or
`from(self)` then `, i64`. The parens bind the source list tightly:

```
split(self *self) byte[] from(self), byte[] from(self)    // both slots borrow self
take (self *self) byte[] from(self), i64                  // slot 0 borrows self; slot 1 nothing
combine(self *self, a *foo, b *foo) pair from(a, b), i64  // slot 0 borrows a and b; slot 1 nothing
mid  (self *self) i64, byte[] from(self), byte[]          // slot 1 borrows self; slots 0,2 nothing
```

No clause ⇒ that slot borrows nothing. `self` and parameter names resolve
to parameter indices (receiver = 0); the resolved per-slot index sets are
the interface method's **declared `ReturnAliases`**, stored identically to
a function's. `from` and `self` are **contextual** — keywords only inside a
return-type `from(...)` clause; outside it, `self` remains the receiver-type
placeholder and `from` is an ordinary identifier, so no existing source
breaks.

**Restrictions (normative, v1):**

- **A `from` source must be a borrowable (view-capable) parameter.** A slot
  whose return type holds no aliasable storage — a scalar like `i64`, a
  by-value `bool` — cannot carry `from`; the clause is rejected at `ToAST`.
  (This is the same predicate that decides whether a slot ever gets a
  non-empty inferred `ReturnAliases`: pointer, slice, interface, or an
  aggregate transitively containing one.)
- **`from(self)` is rejected on a value (non-pointer) receiver.** A
  `self`-by-value method returns a view of a *copy* that lives only for the
  call frame; binding the result to the interface value's lifetime would
  outlive that copy and dangle. v1 rejects `from` naming a value receiver
  outright — the strict, conservative default. (Lifting this would require
  modeling the coercion-time copy as caller storage; deferred, not relied
  on. See [D6 in Decisions](#decisions).)

### Conformance — the `⊆` ceiling rule

The interface declares the **ceiling**: the maximal borrow any
implementation may have. A concrete type satisfies the interface iff, for
each interface method and each return slot,

> `implementation.ReturnAliases[slot]` (inferred) **⊆**
> `interface.declared[slot]`

mapped positionally (the impl's param names need not match the interface's;
receiver = index 0 on both sides). Consequences:

- A method that borrows **nothing** (a static-returning `string()`,
  inferred `[[]]`) satisfies *any* declared contract — `[[]]` ⊆ anything.
- A method that borrows **exactly** the declared set conforms.
- A method that borrows a parameter the interface **did not** declare, or
  the receiver when the interface declared none, **fails** — the caller
  would not know to keep that storage alive.

So implementations sit anywhere at-or-below the declared ceiling. The
strict v1 guard is the special case "ceiling = ∅ for every interface";
`from(...)` raises the ceiling per interface.

A direct consequence: **the borrow contract is part of an interface's
identity for satisfaction and assertion.** Two interfaces with byte-
identical method *signatures* but different `from` contracts are different
interfaces — a value that satisfies `J { bytes(*self) byte[] from(self) }`
may *fail* `_.(K)` for `K { bytes(*self) byte[] }`, even though `K`'s
method "looks structurally identical," because the concrete behind it
borrows more than `K` permits. This is intended (it is the laundering gate
doing its job), but worth stating so a failed assertion on a
signature-identical interface isn't surprising.

### Call-site propagation

At a coercion site (concrete → interface), nothing about aliasing is stored
in the fat pointer or vtable — the contract lives on the **interface type**,
statically. At a **virtual call** `r := iface.m(args)`:

1. Look up `iface`'s declared `ReturnAliases` for `m` (static).
2. For each return slot, for each declared param index `p`: the result
   borrows the **actual argument at position `p`** — where position 0 is
   the **interface receiver value** (`iface` itself). Union over the
   declared params, via the same join-origin machinery direct calls use.
3. Bind the result's origin accordingly, **at the granularity the
   direct-call result path already uses** — a scalar/pointer slot gets a
   scalar origin, but a slot that is *itself* an interface or an aggregate
   gets its origin attached through the same field facts
   `funcallResultOrigin` / `emitInterfaceFatPtr` use (for an interface
   result, the `data` field fact that `argAliasProvenance` later reads back —
   `retalias.go:139`; for an aggregate, the per-field origins). Binding a
   bare scalar origin on an interface-typed slot would drop the fact and let
   that interface result escape — so the propagation must reuse the
   direct-call machinery rather than a flat origin write. The existing flow
   tracking then bounds the result's use by the interface value's (and
   transitively, its referent's) lifetime.

For `from(self)`, step 2 says "the result borrows `iface`," so the result
may not outlive the interface variable — exactly the lifetime the receiver
guarantees. The virtual-call hot path reads **only** the interface's static
type; **nothing is read from the vtable and there is no per-dispatch cost**,
precisely as Rust erases lifetimes from vtables. (The runtime *assertion*
path is different — see [Soundness](#soundness-assertion-and-type-switch).)

### Soundness: assertion and type-switch

The static virtual-call story above is the easy half. The hard half — the
one that makes this more than a parser change — is **interface-to-interface
assertion and type-switch**, where an itab is built **at runtime** from the
concrete type's **full** method table (`_iface.assert_to`). The previous
strict guard (option (a)) was sound by *denying entry*: a borrow-returning
type never became an interface value, so `assert_to` could never mint a
borrowing-method itab. This proposal deliberately *permits* entry (via
covering interfaces), which puts the concrete type's full table — including
its borrowing methods — back in reach of runtime assertion. Without care
that reopens a laundering hole:

```
type T { ... }  bytes(self *self) byte[] { return self.buf }   // inferred [[0]]
interface J { bytes(self *self) byte[] from(self) }   // T satisfies J (⊆) ✓
interface K { bytes(self *self) byte[] }              // declares NO borrow

fn leak() byte[] {
    var t T = ...                 // local
    var j J = &t                  // legitimate: J covers T's borrow
    var k K = j.(K)               // assert_to matches `bytes` by name/sig
                                  // (`from` is not part of the sig) — would SUCCEED
    return k.bytes()              // k's contract says non-borrowing → result untracked
                                  // → returns a view of local t → DANGLES
}
```

Entry through one covering interface (`J`) launders the borrowing method to
any under-declaring interface (`K`) — and equally to `any`, and via
type-switch. The name/sig matching keys `assert_to` uses do **not** carry
the `from` contract, so it cannot tell `J`'s `bytes` from `K`'s.

**Fix: contract-aware assertion.** The borrow contract becomes part of what
`assert_to` matches on:

- The **typedesc**'s per-method entry records that method's *inferred*
  borrow set (the impl contract). The **iface_desc**'s per-method entry
  records the *declared* borrow set (from the `from` clause).
- `assert_to`, when matching a target method against the concrete type's
  method, additionally requires the concrete method's borrow set **⊆** the
  target's declared set. A non-borrowing target method (declared ∅) is
  matched only by a non-borrowing implementation; a `from(self)` target is
  matched by `∅` or `{self}`. If the ⊆ test fails, the method does not
  match and the **assertion fails** (`(nil, false)`) — exactly as if a
  required method were absent.

In the example, `j.(K)` now fails: `T.bytes` borrows `{self}` ⊄ `K.bytes`'s
∅, so `assert_to` reports `T` does not satisfy `K`, and `k` is null —
caught by the assertion's own `ok` / the type-switch default. No dangle.

**Why this holds through chains (`j.(K).(M)`).** The gate would be
defeatable if a successful assertion *erased* the concrete type's true
contract, letting a later hop re-assert against a laundered descriptor. It
does not: `assert_to` writes the **original concrete typedesc** into the new
itab's `vtable[0]` (`mov qword[r15+24] r12`, r12 = the concrete `src_ti`;
the returned interface value's vtable base is `r15+24` —
`iface_linux.bs:127,167`). So every itab, no matter how many
interface-to-interface hops produced it, carries the *same* concrete
typedesc, and a chained `.(M)` re-reads the impl borrow masks from that
typedesc and re-applies `impl ⊆ M.declared` against the **full true impl
set**. This is the constraint that makes the encoding choice load-bearing:
the **impl** descriptor must live in the **typedesc** (which rides
`vtable[0]` through every chain), *not* only in the source `iface_desc` —
otherwise a chain would re-gate against an already-narrowed declared mask
and the laundering hole reopens at the second hop. The cache cannot bypass
this either: itabs are linked only after the gate passes (`.all_found`);
`.miss_free` does not cache, so a failing gate re-checks on every attempt.

This is **static data plus one comparison per matched method**, not a
per-dispatch cost: the borrow set is encoded compactly (e.g. a per-slot
parameter bitmask), so ⊆ is `impl_mask &^ declared_mask == 0` — and it runs
only inside `assert_to`, never on a normal virtual call. So "no
per-dispatch cost" holds, but "nothing in the typedesc" does **not**: the
typedesc and iface_desc method entries gain the borrow descriptor.

**Consequence — `any` no longer needs to be closed.** Because the
*assertion* path is gated, a borrow-returning type may freely enter `any`
(or any interface whose required methods it covers): a later `a.(K)` to an
under-declaring `K` simply fails the contract-aware match. Option (a) kept
`any` closed because it had no runtime gate; this proposal has one, so the
"deny entry" rule is replaced by "gate the dynamic construction." (Static
`__vtable_T__I` coercion is gated separately by conformance — next section.)

### The guard relaxation

There are two itab-construction paths, and both must be gated:

1. **Static coercion** (`emitInterfaceFatPtr` → `__vtable_T__I`, built at
   compile time). Gated by **conformance**: for each method the target `I`
   *requires*, `T`'s matching method's inferred `ReturnAliases[slot]` must
   be ⊆ `I`'s declared `ReturnAliases[slot]`. This folds into
   `TypeSatisfiesInterfaceAs` — the `⊆` test joins the existing
   name/receiver/param-type match. `rejectBorrowingMethodCoercion` is thus
   *subsumed*: a borrowing method `I` requires but does not cover →
   coercion fails, with the directed diagnostic naming the interface's
   declared contract and the offending slot ("declare `from(...)` on the
   interface, or stop borrowing"). A method `T` has that `I` does **not**
   require imposes no static constraint here — it is reachable only via
   assertion, which path 2 gates.
2. **Runtime construction** (`assert_to` for `x.(K)` / type-switch). Gated
   by the contract-aware match above.

Interface-to-interface **widening** (`var a any = someJ`) copies an
existing itab rather than constructing one, and that itab already passed
one of the two gates, so widening needs no separate check (as in the
return-alias proposal's analysis).

### Soundness and the mutable case

The conformance direction (`⊆`: an impl may alias **less** than declared)
is **return-lifetime covariance** and is **sound for mutable and immutable
returns alike**:

- The caller, under the declared contract, only ever uses the result within
  the declared window (e.g. while `self` is live). An implementation that
  aliases less produces a result valid for that window *and longer* (a
  static result is valid forever). Every use the caller makes is within a
  window where the actual result is valid ⇒ no dangling, no use-after-free.

The mutable case does *not* break soundness — it was tempting to think so,
but it does not:

- A `from(self) mut byte[]` result and a static `mut byte[]` result both
  offer the safe operations *read*, *write*, *use* — the static one over a
  longer window. Writing through either is a safe write to valid storage.
  The two writes land in *different places* (self's bytes vs static bytes),
  which changes program **behavior**, not **safety**. An implementation
  whose mutable result doesn't actually alias `self` is a **logic error**
  (writes meant for `self` go elsewhere) — the same category as returning
  the wrong value — and is the programmer's concern, not the borrow
  checker's. The program stays memory-safe.

Therefore `from(...)` needs **no mut/immut split** for the conformance
rule. The only place mutability matters is the *optional* exact contract
([declined — Future extensions](#future-extensions)): if an author wanted
"this mutable result *really* aliases `self`" to be a checked guarantee (so
writes provably reach `self`), that is the `==` form, an opt-in effect-level
contract — not
something `from`/`⊆` implies.

## Implementation impact

The layers, in dependency order:

- **Lexer/Parser** (`cmd/bosc/lexer.go`, `parser.go`): `from` as a
  **contextual** keyword — recognized only at a return-type position, *not*
  a reserved word, so existing identifiers named `from` keep working (the
  parser, not the lexer, decides; `self` stays the receiver-type placeholder
  everywhere else). `parseInterfaceMethodSig` (`parser.go:1524`) gains an
  optional `from(name, ...)` after each return-type position. Multi-return
  parsing threads a per-position clause. (Restricting `from` to
  interface-method return positions in v1 keeps the grammar change small; the
  concrete/free-function form is a decided fast-follow — see D2/D4 in
  [Decisions](#decisions).)
- **AST** (`ast.go`): `InterfaceMethodSig` (`ast.go:3102`) gains
  `ReturnAliases [][]int` (the declared contract); `ToAST` resolves the
  `from` names to parameter indices (receiver = 0, `self` keyword), with
  errors for unknown names, a named variadic param, a value-receiver `self`,
  or a `from` on a non-view return slot — the last two per the normative
  [Restrictions](#syntax).
- **Conformance** (`ast.go:586`, `TypeSatisfiesInterfaceAs`): after the
  existing name/receiver/param-type match, add the per-slot `⊆` aliasing
  check (`impl.ReturnAliases ⊆ isig.ReturnAliases`), demand-driving
  `aliasSet` on the implementer's method. This *subsumes* the standalone
  guard.
- **Static-coercion guard** (`cmd/bosc/retalias.go`,
  `validateInterfaceCoercion`): `rejectBorrowingMethodCoercion` is
  *subsumed* by the conformance `⊆` check (gate path 1) — for the methods
  the target requires, uncovered borrow → reject, diagnostic gains the
  interface-declared-contract context. No longer a full-method-set or
  `any`-closing check (the assertion path handles the rest).
- **Static call-site propagation** (`cmd/bosc/compile.go`): the
  interface-dispatch call path (`compileInterfaceMethodCall`) gains
  result-origin propagation from the interface's declared `ReturnAliases`,
  mapping declared param positions onto the actual args (receiver → the
  interface value), reusing the join-origin union — parallel to
  `funcallResultOrigin` for direct calls.
- **Runtime assertion gate (the load-bearing part)** — gate path 2:
  - **typedesc** per-method entries (`cmd/bosc/typeinfo.go`,
    `emitTypedescStructured`, and the `typedesc` bas directive +
    serialization) gain a compact **borrow descriptor** (per-slot parameter
    bitmask) = the method's inferred `ReturnAliases`. **Single source of
    truth:** this descriptor and the cross-package `.bo` fact that gate 1
    reads are *the same* computed `ReturnAliases` for that method (home
    package computes it once via the engine; the typedesc mask is a
    re-encoding of it, not an independent computation), so the two gates
    cannot disagree.
  - **iface_desc** per-method entries (the assertion-time interface
    descriptor) gain the **declared** borrow descriptor (from `from`).
    **Single source of truth (declared side):** mirror the impl-side
    argument — within a package both the cross-package `InterfaceShape`
    serialization that gate 1 reads and the iface_desc mask gate 2 reads
    derive from the one `InterfaceMethodSig.ReturnAliases` resolved from the
    `from` clause; cross-package, both the `InterfaceShape` field and the
    iface_desc bytes must round-trip identically, so the two gates see the
    same declared ceiling.
  - **Descriptor placement is not free.** `lookup_method` walks the typedesc
    method table and the iface_desc req table with **hand-coded fixed
    strides and offsets** — 64-byte typedesc entries (`add rdx 64`, fn_ptr at
    `[rdx+56]`, `iface_linux.bs:244,247`) and 56-byte req entries
    (`add r9 56`, `iface_linux.bs:151`). Appending a per-entry mask field
    re-strides both walks and shifts every later offset; "rides the existing
    serialization" is true for the *bytes* but **not** for the assembler
    routine. Two layouts were weighed: (a) widen the entry and re-derive the
    strides/offsets in `assert_to`/`lookup_method`, or (b) keep entries
    fixed-width and store the masks in a **parallel side-array** indexed by
    the same method index, so the existing walk is untouched and the mask is
    one extra indexed read. **D0 takes (b)** — the lower-risk change to
    load-bearing hand-written x86-64.
  - **`_iface.assert_to`** (`runtime/_iface/iface_linux.bs`, hand-written
    x86-64) gains, per matched method, the ⊆ check
    `impl_mask &^ declared_mask == 0`; a failing method makes the assertion
    fail. This is the one runtime-library change, and it is where the
    soundness actually lives — it must be specified and tested before
    anything depends on the relaxed guard.
- **`.bo` transport**: the interface's declared per-method `ReturnAliases`
  travels cross-package. `InterfaceMethodSig`/`InterfaceShape` gain the
  field; the bas `interface` directive (`cmd/bas/main.go:394`) and
  `writeInterfaces`/`readInterfaces` (`bwrite.go`) serialize it (natural
  shape: a per-method `retaliases <slot>: <idx>...` line in the method
  block). The typedesc/iface_desc borrow descriptors serialize alongside
  their tables as the **parallel side-array** D0 selects (entries unchanged,
  masks indexed by method position).
- **`bdump`**: print interface methods' declared `ReturnAliases` and the
  typedesc/iface_desc borrow descriptors.
- **`fmt`**: declare `stringer { string(self *self) byte[] from(self) }`
  (decided, D5); re-admit the borrowed-field stringer tests.
- **`DESIGN.md`**: the Borrowing section and the §Directives Reference
  (`retaliases` now also appears in `interface` blocks); the supersession
  note on the return-alias proposal's "option (b)."

## Tests

Integration tests under `cmd/bosc/tests/`, the unit harness for alias
facts, and cross-package fixtures under `testpkgs/`. At minimum:

- **Conformance (positive):** a type whose `string()` borrows `self`
  coerces to `stringer { ... from(self) }`; a *static*-returning `string()`
  coerces to the same interface (`⊆`, borrows less); both run through
  `%v`-style dispatch.
- **Conformance (negative):** a type whose method borrows a parameter the
  interface did **not** declare is rejected at coercion with the directed
  diagnostic; a type borrowing the receiver where the interface declared
  `∅` is rejected.
- **Laundering via assertion is blocked (the central soundness test):** a
  borrowing-method type enters a *covering* interface `J`; asserting that
  value to a sibling interface `K` that declares the method non-borrowing
  **fails at runtime** (`assert_to`'s ⊆ check), so a `case K:`
  type-switch / `j.(K)` does not produce a usable `k`, and no borrowed view
  escapes. The same via `any` (`var a any = j; a.(K)` fails). Both the
  must-fail (borrowing impl) and the must-succeed (static impl behind the
  same `J`, which *does* satisfy `K`) directions.
- **`any` entry is now allowed, and stays safe:** a borrowing-method type
  *can* coerce to `any`; pins that it no longer rejects at coercion, and
  that the dynamic gate (above) still blocks laundering out of `any`.
- **Virtual-dispatch lifetime:** `fn use(s stringer) byte[] { return
    s.string() }` infers the function borrows `s` (`[[0]]`, propagated from
  the interface contract); a caller that lets the result outlive the
  interface value is rejected; a caller using it within the value's
  lifetime runs.
- **Multi-source / multi-return:** `from(a, b)` and per-slot
  `... from(self), ...` declarations conform and propagate the union.
- **Cross-package:** the declared contract round-trips through the `.bo`
  (an interface in package A, an implementer in B, a caller in C).
- **Mutable (correctness, not safety):** a `from(self) mut` interface with
  a non-aliasing impl *compiles* (it's sound) — documents that the language
  does not police the effect (and motivates the `==` opt-in if we add it).

## Decisions

The design questions raised during review are settled as follows. **v1 ships
interface-methods-only**; items marked deferred/fast-follow are decided in
principle but not built in v1.

**D0 — Runtime borrow-descriptor encoding.** Per-slot parameter **bitmask**
(bit *p* of slot *s* = "slot *s* may borrow param *p*"), one `u64` per return
slot, so ⊆ is `impl &^ declared == 0` (one `andn`+test). Stored in a
**parallel side-array** off the typedesc/iface_desc header, indexed by method
position — *not* widened into the fixed-stride method entries — so the
hand-written `assert_to`/`lookup_method` walk is untouched (see the
[stride note](#implementation-impact)). **Per-slot is mandatory, never a
per-method union:** a union mask is unsound — an impl that permutes which
slot borrows which param (`(A from(self), B from(x))` vs
`(A from(x), B from(self))`) has the *same* union yet violates the per-slot
contract the caller bounds each result by, reopening a dangle. **64-param
ceiling**, a hard compile-time error if exceeded — fired **independently at
both emission sites** (impl mask in the type's home package, declared mask in
the interface's), never silent truncation (which would make `assert_to`
under-reject and reopen the hole). The full `[][]int` still rides the
typedesc/iface_desc for `bdump`/diagnostics; the mask is the assert-time fast
form.

**D1 — Keyword: `from`.** Chosen over `borrows` and `aliases`. `from` names
**provenance** ("the result comes from this source") and nothing else, which
is exactly what we track; `borrows` drags in the effect/capability
connotation the [Vocabulary](#vocabulary-what-is--and-isnt--tracked) section
exists to disclaim. Contextual keyword — recognized only at a return-type
position, never reserved.

**D2 / D4 — Optional `from` on concrete functions/methods: exact, deferred.**
A `from` clause on a concrete function or method is **optional** (inference
stays authoritative for conformance and the `.bo`) and, when written, checked
**exact** (`== inferred`) — it must equal what the body actually borrows,
else error: a precise, drift-catching assertion, not a loose ceiling.
(Upper-bound was rejected — its only use is letting source *overstate* its
borrows, which conformance never needs and which makes the source lie about
itself.) A method is a function with a receiver, so methods and free
functions share this one rule. **Scope: deferred to a fast-follow** — v1 is
interface-methods-only; this is a purely additive documentation/stability
nicety nothing in the core depends on.

**D3 — The `==` "really aliases" effect contract: declined.** Recorded as an
idea in [Future extensions](#future-extensions), not built. It would make a
*mutable* aliased return's "writes reach the source" a checked guarantee
(conformance becoming exact equality = the lifetime ⊆ *and* an effect ⊇).
Declined because it co-opts an **alias/lifetime** mechanism to police an
**effect/correctness** property it structurally cannot see: aliasing proves
only *provenance* ("the result points into `self`"), not that `self`'s
observable behavior depends on those bytes — an impl can alias `self.scratch`
while the state that matters lives in `self.value`, passing the check while
writes through the "guaranteed" view do nothing. A leaky guarantee with a
built-in escape hatch; not worth complicating the language for. Revisit only
if a concrete need appears.

**D5 — `fmt.stringer` declares `from(self)`:**
`stringer { string(self *self) byte[] from(self) }`. Re-admits borrowed-field
stringers (a `string()` that can't reference `self` is crippled), and under
⊆ still admits static/owned-returning impls (∅ ⊆ {self}), so it excludes no
one. The cost — `string()`/`%v` results are assumed to borrow the value — is
a single-statement borrow window in the dominant format-and-write-now path;
needing an object's string representation *after the object is dead* is the
rare, weird case.

**D6 — `from(self)` on a value (non-pointer) receiver: reject.** Normative in
v1 (see [Restrictions](#syntax)); the result would borrow a call-frame copy
and dangle. Lifting it later would require modeling the coercion-time copy as
caller storage; nothing here relies on that.

## Future extensions

- **Optional `from` on concrete functions/methods** (decided **exact**, per
  D2/D4; a fast-follow after the interface core) — Rust-style "declare and
  verify" at API boundaries, checked against inference.
- **The `==` exact / effect contract** (declined, per D3) — recorded as an
  idea, not planned: it would make a mutable aliased return's
  write-through-to-source a checked guarantee, but uses alias tracking to
  enforce an effect it cannot actually observe (leaky, circumventable).
- **Field-level precision.** As with the inference engine, `from(...)` is
  parameter-level (`from(self)` = "borrows the receiver," not "borrows
  `self.buf`"). Per-field declared contracts would tighten the conservative
  `%v` cost but require the deferred field-level alias representation.

## Supersession / relationship

This proposal **lifts the restriction** described in
`PROPOSAL_return_alias_inference.md` §Interface-method dispatch (option
(a)), including its "`any` stays closed" rule: option (a) was sound by
*denying entry* to the interface world (so `assert_to` could never mint a
borrowing-method itab); this proposal *permits* entry and instead makes
`assert_to` **contract-aware**, gating the dynamic construction directly.
Note this is a *stronger* mechanism than the return-alias proposal's own
option-(b) sketch, which proposed *tracking* the laundered result through
`assert_to` ("carry the contract … so a `x.(B).field()` result is tracked
rather than laundered"). Blocking the itab construction on a ⊆ violation is
simpler and never mints a bad itab in the first place; the borrow through a
*successful* assertion is then re-derived at the eventual virtual call from
the target interface's static contract, so no separate tracking-through is
needed.
The sole-chokepoint argument (`emitInterfaceFatPtr` for static coercion,
`assert_to` for runtime construction, copy-not-mint for widening) still
identifies the complete set of itab-construction sites — this proposal adds
a gate at the `assert_to` site that option (a) did not need because it
denied entry. The inference engine (`DESIGN_return_alias_engine.md`) is
unchanged — this consumes its `ReturnAliases` output; it does not alter how
the output is computed.
