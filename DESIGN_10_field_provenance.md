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

### Phase 3 — call face (the only face that *might* need objrep)
`h := id(holder{p: &s.x})`: the construction is the **argument**, visible at the
call site; `id`'s summary says (today, coarse) "return slot 0 aliases param 0".
Two options, pick after a soundness pass:
- **3a (no objrep):** when a struct-by-value return's summary names param P,
  `CopyFieldPointers(argP, dest)` — a by-value struct return is a copy whose
  fields carry the same borrows as `argP`'s fields, so copying argP's field
  provenance to `dest` is sound *iff* the callee doesn't re-point fields it
  doesn't also surface in the summary. Replaces the `__callret` collapse with a
  precise field copy. **Likely sufficient and far cheaper.**
- **3b (objrep):** only if 3a proves unsound for some callee shape — extend the
  fact to field-level: `ReturnAliases [][]FieldAlias{ReturnPath, Param, ParamPath}`
  (empty paths ≡ today's behavior, so the common case stays one byte). Touches
  `ofile.go`/`function.go` types, `bwrite.go` ser/de, the `retaliases` directive
  (`cmd/bas/main.go` ×2 + interface form), `bdump`, the importer. Mechanical but
  wide, and a cross-package format bump.

### Builtins
`new`/`alloc` need no summary — `new`'s provenance is the **local** struct
literal at the call site (Phase 2); `alloc` is fresh (none).

---

## What closes what

- Phases 0–2 close **global, heap-write, heap-new** — *no `.bo`/grammar change*.
- Phase 3a closes **call** without objrep if the field-copy is sound; 3b is the
  fallback objrep bump.

So the objrep/grammar work the original framing feared is the **last, least-
certain** item, gating one face, with a no-format-change alternative (3a). The
high-risk item is Phase 1 (moving the pointer-root provenance boundary +
getting conservative invalidation complete); everything else composes off it.

## Verification
Every phase: full bosc + bas + `go test ./...` + `mmk go_test` green, plus the
held #10 drivers flip per face. Phase 1 must **not** regress the precise in-
frame cases (`cov_owned_field_borrow_in_struct_*`, `new_struct_nonnullable_
pointer`, `cov_ref_share_*`, cursor/self-ref patterns) — that over-rejection
check is the gate for Phase 1.
