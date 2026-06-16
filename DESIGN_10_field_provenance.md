# Scoping: #10 — field-buried borrow escapes the frame

**Problem.** A borrow of local storage, buried in an aggregate field, escapes
the frame and is then used after the local is consumed — undetected. Four
faces, all the same shape (`&s.x` stored into a field that outlives `s`):

| Face | Shape | Driver (held) |
|---|---|---|
| **call** | `h := id(holder{p: &s.x})` (returned by value) | `cov_owned_field_borrow_escapes_call_err` |
| **heap-new** | `h := new(holder{p: &s.x})` | `cov_owned_field_borrow_escapes_heap_err` |
| **heap-write** | `h.p = &s.x` (through a `*mut`) | `cov_owned_field_borrow_escapes_heap_ptr_write_err` |
| **global** | `g = &s.x` (g a global ptr) | (probe; no driver yet) |

**Not heap-modeling.** The safe `new(cursor{current: &x})` vs unsafe
`new(holder{p: &s.x}); dispose(s)` differ only in whether the local is
*consumed* while the escaped borrow is reachable. That distinction already
exists: origin-invalidation on `dispose`/move. So we do **not** need heap
lifetime analysis — we need the **field provenance of the escaped borrow** to be
recorded, and the existing consume-invalidation fires automatically (the cursor,
never consumed, stays accepted). The missing capability is **pointee-/return-
field provenance**, the natural extension of the value-struct field machinery
(`fieldPointers`, `recordStructLiteralFieldFacts`, `CopyFieldPointers`).

The residual cost is **conservatism, not unsoundness**: once a pointee-field fact
exists it must be dropped on any opaque way the pointee could change (re-point,
alias-and-write, pass-as-`*mut` to a function — DESIGN §773–774: no
interprocedural write summaries). Dropping errs toward false positives.

---

## The current representation (anchors)

- **Summary fact:** `ReturnAliases [][]int` — per return slot, the set of
  **param indices** the slot may alias. *Slot-coarse: names a param, not a
  field* (`DESIGN_return_alias_engine.md` §"`__callret`").
  - `FuncDecl.ReturnAliases` (`ofile.go:192`), `Function.ReturnAliases`
    (`function.go:747`), interface method sigs (`cmd/bas/main.go:547`).
  - `.bo` serialize/deserialize: `bwrite.go:436` (nested varint counts).
  - `.bs`/interface directive: `retaliases <slot>: <idx>...` (`cmd/bas/main.go:508,1039`).
- **Production:** `returnExprParamAliases` (`retalias_engine.go:348`) reads the
  returned expression's tracked origin + field-origin union, emits `[]int`.
- **Caller application:** `recordStructCallResultAtPath` (`retalias.go:235`)
  merges all contributing args into **one** origin at `<dest>.__callret`
  (the coarse sentinel; `retalias.go:94`).
- **The cutoff:** `readProvenancePath` (`compile.go:1150`) returns `Unknown`
  the moment the path root is a pointer (`t.Indirection > 0`). So `h.p`
  (= `(*h).p`) carries **no** provenance today. **This is the core change.**
- **Invalidation:** `Forget` / `ForgetFieldPointersUnder` / `InvalidateOrigin`
  (`flow/state.go:407,627,530`).
- **Local-escape reject (reuse for global face):** `checkLocalOriginDoesNotEscape`,
  `OriginKindOf(...) == flow.OriginLocal`.

---

## Phased plan (ordered cheapest-/most-isolated-first)

**The key reframing: the `.bo`/objrep change is needed for *at most one* of the
four faces.** Three faces are closeable with in-frame flow tracking + recording
at the (locally visible) construction site, no format change.

### Phase 0 — global face (independent, cheap)
Reject storing a **local-origin** borrow into a **global**. A global outlives
every local, so this is unconditionally an escape. Reuse the existing
`OriginLocal` discriminator at the global-assignment store site (the `g = …`
path where the target is a global and the RHS is a local-origin borrow).
No objrep, no engine change. *Risk: low; over-rejection only if someone
legitimately parks `&local` in a global, which is already a dangle.*

### Phase 1 — pointee-field tracking + heap-write face (the core)
Lift the `readProvenancePath` pointer-root cutoff so `(*h).field` can carry a
fact, and record it on a direct store `h.p = &local`:
- **Store:** a field key that survives a pointer root — extend the
  `fieldPointers` keying (today `"binding.field"`) to a pointer pointee, e.g.
  record `h.p`'s origin on the store-through-pointer path (`compile.go` `*Deref`
  / field-store codegen).
- **Read:** `readProvenancePath` returns the recorded pointee-field fact instead
  of `Unknown` for a pointer root.
- **Invalidate (the load-bearing soundness work):** drop `h`'s pointee-field
  facts on — `h` reassigned; `h.p` re-pointed; an alias `h2 := h` then a write
  through `h2`; and **`h` (or `&h`) passed where it could be written
  opaquely** (any `*mut`-taking call). Conservative `ForgetFieldPointersUnder(h)`.
This closes heap-write with no objrep change (all in-frame through a local
pointer). *Risk: highest — it moves a deliberately-conservative boundary;
guard with the full in-frame regression set + the held drivers.*

### Phase 2 — heap-new face (small, builds on Phase 1)
`h := new(T{f: &local})`: the struct literal is **right there** at the call
site, so no callee summary is needed. When the initializer is `new(structLit)`,
run `recordStructLiteralFieldFacts` against the **pointee path** `(*h)` instead
of a value path. Reuses Phase 1's pointee-field storage. *Risk: low.*

### Phase 3 — call face (genuinely needs objrep; no free alternative)
This is the one face that **requires** a field-level fact in the `.bo`. The
general case is a **cross-shape, field-to-field** mapping computed inside the
callee:
```
type foo struct { x *i64 }
type bar struct { y *i64 }
fn doathing(f *foo) bar { return bar{ y: f.x } }   // return.y aliases param0(deref).x
```
`bar` is not `foo`; there is no `argP` whose fields can be copied onto `dest`.
The only expression of the fact is the mapping itself: *"return slot 0, field
`y`, aliases param 0, field `x`."* And it is **forced** to serialize: when
`doathing` is imported, the caller has only the `.bo`, not the AST, so it cannot
re-derive the mapping by any local analysis. There is no no-objrep path.

- **The fact:** extend `ReturnAliases [][]int` →
  `[][]FieldAlias{ReturnPath, Param, ParamPath}` (empty paths ≡ today's
  behavior, so the common case stays one byte; `ParamPath` may carry a deref
  marker for `*foo` params). Touches `ofile.go`/`function.go` types,
  `bwrite.go` ser/de, the `retaliases` directive (`cmd/bas/main.go` ×2 +
  interface form), `bdump`, and the importer. Mechanical but wide, and a
  cross-package format bump.
- **Production:** `returnExprParamAliases` (`retalias_engine.go:348`) already
  reads the returned expression's field-origin union; extend it to emit the
  *return-side field path* alongside each param-side origin instead of
  collapsing to a slot-level `[]int`.
- **Caller application:** replace the `__callret` collapse
  (`recordStructCallResultAtPath`) with per-`FieldAlias` recording: for each
  `(ReturnPath ← Param.ParamPath)`, set `dest.<ReturnPath>`'s origin to
  `argAliasProvenance(args[Param]).<ParamPath>`.
- **3a as an optional fast-path only:** when the return *is* a param (`return
  h`, same shape), `CopyFieldPointers(argP, dest)` is a valid shortcut — but it
  is **not** a substitute for the field-level fact and does not cover the
  cross-shape case above.

### Builtins
`new`/`alloc` need no summary — `new`'s provenance is the **local** struct
literal at the call site (Phase 2); `alloc` is fresh (none).

---

## What closes what

- Phases 0–2 close **global, heap-write, heap-new** — *no `.bo`/grammar change*
  (the borrow is constructed at a locally-visible site in each).
- Phase 3 closes **call** and **requires** the field-level `.bo` fact — the
  general case is a cross-shape field-to-field mapping (`bar{y: f.x}`) computed
  inside the callee and forced to serialize for cross-package imports. No
  no-objrep alternative; 3a is only a `return param` fast-path, not a substitute.

So the objrep/grammar work is needed for exactly **one** of the four faces (the
call face) — but for that face it is unavoidable. The other three faces avoid it
because their construction is locally visible. The high-risk item remains Phase
1 (moving the pointer-root provenance boundary + complete conservative
invalidation); everything else composes off it.

## Verification
Every phase: full bosc + bas + `go test ./...` + `mmk go_test` green, plus the
held #10 drivers flip per face. Phase 1 must **not** regress the precise in-
frame cases (`cov_owned_field_borrow_in_struct_*`, `new_struct_nonnullable_
pointer`, `cov_ref_share_*`, cursor/self-ref patterns) — that over-rejection
check is the gate for Phase 1.

---

## Phase 3 representation design (the `.bo`/grammar fact)

### What the summary *is* (the principled model)

A call `x := f(args)` is a **tracker operation**, exactly like `x := &y` is —
it installs provenance facts about `x` (and the args). Almost every facet of
that operation is already recoverable from the **signature**: result
nullability ← return `?` bit, result owned-ness ← return `owned` bit, args
consumed ← param `owned` bits. The **one** facet the signature does *not* give
is **aliasing/provenance** — which params/fields the return borrows. So the
summary serializes *exactly that and nothing else*: the **param-relative
aliasing projection of the callee's tracker state at the return site**.

(Deliberately *not* in scope: effects on params — e.g. `f(p){ p.x = nil }`
leaving the caller's `arg.x` stale. That's a full interprocedural effect
summary, the DESIGN §773–774 gap — a separate, larger feature #10 does not
need. We serialize the alias projection only.)

An origin in the return-state encodes three kinds, and the vocabulary handles
all three:
- **param-rooted** → a `FieldAlias` entry (below).
- **fresh** (allocated in the callee) → **absence** of an entry; the caller
  treats the path as fresh/owned, no caller-storage alias. (Load-bearing
  default.)
- **callee-local** → never an entry; returning a borrow of callee-local storage
  is the **escape reject** the engine already fires.

### The fact

```go
type FieldAlias struct {
    ReturnPath string // dot-path within the return slot ("" = whole slot)
    Param      int    // contributing parameter index
    ParamPath  string // dot-path within that parameter ("" = whole param)
}
ReturnAliases [][]FieldAlias   // per return slot, a SET of field aliases
```

This *is* the projection: `(ReturnPath) → {(Param, ParamPath)}`, keyed by return
sub-path, valued by the param-rooted origins it carries (a set, so a branch-
merge union `return.y` ← {param0.x, param1.z} is expressible).

- `doathing(f *foo) bar { return bar{y: f.x} }` → slot 0 =
  `[{ReturnPath:"y", Param:0, ParamPath:"x"}]`.
- The whole-slot passthrough `return param` is `[{"", p, ""}]`. (Not a
  compatibility goal — it just happens to be the empty-path point of the same
  projection. The rep is designed for the projection, not to preserve the old
  `[][]int`.)

### Path semantics — strings, implicit deref, no markers

A path is a **dot-joined field string** (with `[]` for an index step), the same
convention as `flow.FlowPath.Fields` and `ProvenancePathForExpr`. Crucially,
**no explicit deref marker is needed on either side**, because the fact is
*applied* by re-interpreting the path as field accesses on an expression, and
field access auto-derefs one pointer level:

- Param side: `ParamPath="x"` applied to arg `&foo_inst` (`*foo`) yields
  `(&foo_inst).x` = `foo_inst.x`. Applied to a value arg `foo_inst` yields
  `foo_inst.x`. Same path, deref falls out of the access.
- Return side: `ReturnPath="y"` applied to the bound result `h` yields `h.y` —
  for a value `bar` that's `h.y`; for a `*bar` result it's `(*h).y`. Same path.

So a path is just `field(.field|[])*`; the value-vs-pointer distinction is
resolved at application, not stored. (Multi-level pointer params/returns —
`**foo` — are out of scope; auto-deref is single-level, matching field access.)

### Termination — k-limiting (REQUIRED for the SCC fixpoint)

`aliasSetSubset` (`compile.go:6966`) is the monotone convergence check; today
its lattice is the finite set of param indices. Field paths make the lattice
**potentially infinite**: a recursive type (`type node struct { next *node;
v *i64 }`, `return node{next: f, v: f.v}`) can manufacture
`next.next.next…`-deep paths, and the fixpoint would never converge.

**Bound path depth to a fixed `k`.** Beyond depth `k`, **widen** the path to its
length-`k` prefix (drop the deeper suffix), which conservatively over-attributes
the borrow to the shallower aggregate — sound (the consume-invalidation still
fires; it just may invalidate a slightly larger sub-tree than strictly
necessary). The lattice is then finite (paths bounded by `k` × field-arity),
so the existing monotone fixpoint terminates unchanged. `k` small (2–3) covers
every non-recursive real case exactly and recursive ones soundly. This is the
one genuinely new correctness obligation the field-level fact introduces.

### Transport encoding

- **`.bs` directive** (`compile.go:3503` emit, `cmd/bas/main.go:1045` parse;
  interface form `compile.go:2979` / `main.go:508`). Keep the common whole-slot
  token as a bare index for readability/diff-minimality; field-level aliases use
  a triple token `<param>:<returnPath>:<paramPath>`:
  ```
  retaliases 0: 0:y:x          # {ReturnPath:y, Param:0, ParamPath:x}
  retaliases 1: 2              # bare index ≡ {ReturnPath:"", Param:2, ParamPath:""}
  ```
  The directive splits slot at the *first* `:`; tokens after it never contain a
  space, and `:` inside a token is unambiguous (exactly two per triple). Parser
  accepts both a bare `<idx>` and the `<idx>:<rp>:<pp>` triple.
- **`.bo`** (`bwrite.go:436` write / `:525` read). Per slot: `count`, then
  `count` entries of `param(varint) + returnPath(string) + paramPath(string)`
  via the existing `writeString`/`readString`. Common case adds two zero-length
  strings (2 bytes) per alias; the zero-slot-count fast path (one byte for an
  ordinary function) is unchanged.
- **`bdump`** (`main.go:95`): print `slot S: .<returnPath> <- param<idx>.<paramPath>`.

### In-memory + consumer touch sites

- Types: `FuncDecl.ReturnAliases` (`ast.go:2360`), `Function.ReturnAliases`
  (`function.go:747`), `InterfaceMethodSig.ReturnAliases` (`ast.go:3256`) →
  `[][]FieldAlias`. `AliasesComputed` unchanged.
- Importer: `ast.go:1207` (function), `:1317` (interface sig) — assignment is
  type-compatible; no logic change.
- Interface-satisfaction check `methodAliasesSatisfy` (`ast.go:667`): compare
  `FieldAlias` sets, not int sets (a method satisfies the contract if its alias
  set is a subset under the same widening).
- Fixpoint `aliasSetSubset` (`compile.go:6966`): subset over `FieldAlias`
  entries (post-k-limiting).
- Production `returnExprParamAliases` (`retalias_engine.go:348`): emit
  `(ReturnPath, Param, ParamPath)` from the returned expression's field-origin
  union instead of collapsing to `[]int`.
- Caller application: replace the `__callret` collapse
  (`recordStructCallResultAtPath`, `retalias.go:235`) with per-`FieldAlias`
  recording — `dest.<ReturnPath>` ← `argAliasProvenance(args[Param]).<ParamPath>`.
  The `__callret` sentinel (`retalias.go:94`) is **deleted** once field paths
  carry the fact precisely.

### Interface grammar: stays param-level (no expansion)

The `from(...)` declared-contract **grammar is not expanded**. Interface method
contracts remain **param-level** (a slot lists params, no field paths). The
field-level fact applies to *inferred* function summaries (the `.bo`), not to
hand-written interface declarations. Consequence:

- **Satisfaction (`methodAliasesSatisfy`, retalias.go:354):** project each
  inferred `FieldAlias` down to its `Param` (drop both paths) and run the
  existing `slotDeclaresParam` ⊆ check against the param-level declared set. A
  field-level method satisfies a param-level interface **iff the params it
  borrows are declared** — the precise shape need not be expressible. The only
  hard rejection is the existing one: borrowing a param the interface does not
  declare.
- **Interface dispatch caller-application:** use the **param-level (whole-
  param)** contract. Coarsening is sound — it only *adds* assumed aliasing
  (drop return-path ⇒ whole return aliases X; drop param-path ⇒ whole param
  aliases X), so the caller over-invalidates at worst. Never unsound.
- **Coarsening drops paths, not param-membership** — the load-bearing
  invariant. Canonical case:
  ```
  fn whatever(f *foo) bar { return bar{y: f.x} }   // {return.y ← param0.x}
  ```
  coarsens to `{slot0 borrows param0}`, NOT to "borrows nothing." So against an
  interface `something(f *foo) bar` with **no `from`** (declared set `{}`), the
  check `{0} ⊆ {}` **fails → rejected**. `whatever` satisfies `something` only
  if it declares `... bar from(f)`. A borrowing method can never be laundered
  into a non-borrowing interface, because a field-borrow of `f` always keeps
  `f` in the coarsened set.
- **Tradeoff (accepted):** direct calls keep full field-level precision (the
  inferred `.bo` fact); **interface dispatch coarsens to param-level**, so a
  program accepted via a direct call may be *over-rejected* through an
  interface. Errs safe. Revisit only if a real pattern needs field-level
  precision across an interface boundary.

### Migration note
This is a `.bo` format bump. Since the whole runtime + all packages are rebuilt
from source each `mmk`, no on-disk compatibility window is needed; bump and
rebuild. The `retalias_*` regression suite + `TestReturnAliasInference` are the
net (per `DESIGN_return_alias_engine.md` §"Keep").
