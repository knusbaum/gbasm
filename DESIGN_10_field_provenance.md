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
