# C Target Semantic Boundary — where Boson stops being canonical C

## History

- 2026-07-20 — Proposed. Recorded the interface-to-interface finding, the
  rejection of compile-time enumeration, and the pay-for-what-you-use decision
  (option 3). Implementation tabled; the rest of the C backend is unaffected.

---

## The question

The C backend exists to be a **safe, canonical C generator**: Boson is the
authoring-time invariant layer, and the emitted C should look as if a human
wrote it and drop into a real C project undetected. (If raw codegen quality were
the goal we would target LLVM IR directly — see
[`PROPOSAL_c_transpiler.md`](PROPOSAL_c_transpiler.md).)

So: does all of Boson map to canonical C?

**Answer: everything does, except one operation — interface-to-interface
assertion, `x.(I)` where `I` is an interface.** That single operation requires
evaluating Boson *ownership* semantics at runtime, which C has no vocabulary
for.

## Why it is irreducible

`x.(I)` has two separable pieces of work:

1. **The dispatch** — "what is x's concrete type?" This is *intrinsically*
   runtime. `x` holds some `T` decided by runtime data flow; no amount of
   compile-time or whole-program knowledge can know it at the assertion site.
   That is the entire point of interfaces. (This is true of native Boson and of
   any interface implementation; it is unrelated to separate compilation.)

2. **The matching** — "does concrete type `T` satisfy interface `I`, and which
   function pointers form the itab?" This depends only on `T`'s method set and
   `I`'s requirements — no runtime values.

Piece (1) is fine: a runtime type check is ordinary C (compare against a static
descriptor's address). Piece (2) is the problem, because Boson's satisfaction
relation is not structural equality. It includes two **directional** gates:

- **Receiver-shape weakening.** A `*mut self` method satisfies a `*self`
  requirement; the reverse is unsound and rejected. An asymmetric relation.
- **Borrow-contract subset.** The impl's *inferred* per-slot borrow set must be a
  subset of the interface's *declared* `from(...)` set: `(impl & decl) == impl`.
  A subset relation.

If both were equality tests we could fold them into the canonical signature
string, and matching would reduce to `strcmp` — plain structural RTTI, which is
perfectly ordinary C. It is precisely because they are **weakening and subset
relations rather than equality** that they must be carried as separate runtime
data (receiver shapes, borrow masks) and evaluated with Boson's rules.

Collapsing them to equality is not an option: it would reject programs the
native backend accepts, which is a semantic divergence. The C backend's standing
invariant is that it never diverges from native observable behavior — it either
matches or refuses to emit.

**Therefore:** supporting `x.(I)` means the emitted C carries Boson ownership
metadata and a matcher that implements Boson's rules. That is a Boson runtime,
not canonical C.

## Why compile-time enumeration was rejected

The tempting alternative is to precompute every `(T, I)` itab at compile time
(we know all method sets) and lower `x.(I)` to a lookup keyed on `x`'s type
descriptor — the dispatch stays runtime, only the *matching* moves to compile
time.

**Rejected.** It inverts the dependency direction. To enumerate the types that
could reach an assertion site, a translation unit must know about every type
that might *use* it, rather than only what it imports. Adding an implementor
anywhere forces recompiles everywhere — an N² object-dependency graph and
effectively a full rebuild on every change. That is fragile and is not how C
separate compilation works. Runtime construction is what preserves the normal
"import only what you use" flow.

## Blast radius — what still maps

Only the one operation is affected. Everything else needs *identity* at most
(a per-type descriptor with a stable address), never the method set:

| Feature | Boson semantics at runtime? | Canonical C? |
|---|---|---|
| Methods, vtable dispatch | No | Yes — shipped |
| Coerce `T` → `I` (T known at the site) | No | Yes — an ordinary vtable + type tag |
| ADTs: tagged unions, switch, projections | No | Yes — shipped |
| `any` holding a value | No — identity only | Yes |
| `x.(i64)` concrete assertion | No — `x->table->t == &i64_t` | Yes — comparing static descriptors is a normal C idiom |
| `x.(type)` switch over concrete types | No | Yes |
| **`x.(SomeInterface)`** | **Yes — weakening + borrow subset** | **No** |

The concrete casualty is narrow but real: **`fmt`'s `%v`**, which discovers
`stringer`/`Formatter` by exactly this assertion (see `runtime/fmt/fmt.bos`
`render_v`). Per-verb directives (`%d`, `%s`) use *concrete* assertions and are
unaffected.

## Decision — pay for what you use

Three options were weighed:

1. Exclude `x.(I)` from the C target entirely (reject at transpile time).
2. Ship a Boson runtime unconditionally (full fidelity; gives up the property
   that motivates this backend).
3. **Pay for what you use.**

**Decision: option 3.** A program that never asserts interface-to-interface
emits pure canonical C — no method-set metadata, no matcher, no runtime. A
program that *does* use it pulls in a Boson runtime, and that dependency is
visible in the output. The boundary becomes explicit and local rather than
global, and it is itself a very C way to handle the problem. Option 1 remains
the fallback if the canonical-C property is later required to be absolute.

## Agreed representation (for when this is built)

Settled during design; mirrors the native layout so the two backends stay
comparable:

```c
typedef struct { void *data; const bo_itab *table; } bo_iface;
struct bo_itab      { const bo_type_desc *t; /* method fn ptrs */ };
struct bo_type_desc { /* identity; + full method set only in the paid tier */ };
```

- One `type_desc` per type, with a **stable address**, so types are
  address-comparable: `extern const bo_type_desc i64_t;` in a header, defined
  once in a `.c`.
- Concrete check is `x->table->t == &i64_t`.
- Coercion `T` → `I` emits a **static** itab (T is known at the site).
- `any` is the zero-method case: its itab is the degenerate `{ type_desc *t }`,
  so `{data, table}` stays uniform across all interfaces.
- Only the paid tier adds the method set (name, signature, receiver shape,
  borrow mask per method), an `iface_desc` per interface, the matcher, and the
  per-type itab cache.

## Native mechanism, for reference

So this need not be re-derived: `runtime/_iface/iface_linux.bs` implements
`assert_to(src_ti, src_shape, src_data, dst_desc) -> (vtable, ok)`:

1. Walk `src_ti.cache_ref` for an itab already built for this
   `(dst_desc, shape)`; a hit returns it.
2. Miss → `_heap.alloc` an itab, then for each required method `lookup_method`
   scans the typedesc's method table matching **name_hash → sig_hash →
   byte-equal name/sig text → receiver-shape compatibility → borrow-contract
   subset**, placing each `fn_ptr` at `vtable[decl_idx + 2]`. All found → link
   into the cache and return; any missing → free and return `(0, 0)`.

That file documents the exact typedesc / iface_desc / itab byte layouts. A C
port would not need the hash-and-text-blob machinery — `strcmp` on name and
signature is more readable and equally correct — but it **must** keep the
receiver-shape and borrow-subset gates, because they change the observable
`ok`/fail result and the corpus tests them (`assert_iface_shape_safety`, the
borrow-contract tests).

## Status

**Tabled.** The rest of the C backend is unaffected and continues: methods,
interfaces (coercion + vtable dispatch), ADTs, and projections are implemented
and pass the corpus differentially with zero miscompiles. Concrete assertions,
`any`, and type switches remain implementable as canonical C whenever we return
to them; only `x.(I)` waits on the decision recorded here.
