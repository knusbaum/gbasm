# Test coverage model — invariants, equivalence classes, the audit

This is a **living process doc**, not a one-time matrix. Test coverage of the
compiler's mechanics is organized around three artifacts with three lifespans:

1. **Invariants** (§2) — what the language guarantees, as falsifiable
   properties. This is what every test *asserts*. Durable; effectively the spec.
2. **Equivalence classes** (§4) — "these shapes lower through the same code, so
   one representative covers them." The justification for *not* writing the
   redundant tests. Derived from **current** codegen; they change when codegen does.
3. **The audit** (§5) — re-derive the classes from current code, confirm each
   still has a live invariant test through it, surface classes that have split.
   Run periodically **and after any codegen refactor** in an affected area.

## 1. Why it's built this way

- **Pure invariants → combinatorial explosion.** Being exhaustive about "value
  types copy independently" means every type × size × position × channel.
- **Indexing by codegen path → rot.** Tests pegged to "the struct-field path"
  go stale and stop revealing gaps when lowering changes.
- **So:** index by invariant (durable identity + oracle), *prune* with
  equivalence classes (finite corpus), *audit* to re-validate (bounded residual
  risk). The invariant is the test's purpose; "shares a code path" is only the
  excuse for skipping its siblings.

**The residual risk we accept on purpose:** a passing test proves the invariant
for its representative's *class*, not for every shape. A refactor can silently
**split a class**, leaving the newly-distinct shape untested with no signal —
exactly how the ≤8/>8 split hid the struct-copy bug behind 17 passing `struct`
tests. The audit cadence bounds this; we accept it because the alternatives
(explode, or assume) are worse.

**Two rules learned the hard way (the `#8` lesson):**

1. **"Saturated" means the cross-product is enumerated with a falsifiable test
   per equivalence class — *never* "there are a lot of tests in this directory."**
   We trusted a test *count* (111 `owned` tests) to declare ownership covered,
   and a real cell stayed open. Test-count is the exact fallacy this whole model
   exists to fight; a region is only "covered" when its cells are enumerated.
2. **Position is a first-class axis that crosses *every* invariant** —
   `{binding, field, element, slice-element, nested, param, return, global}` ×
   `{I1…I14}`, ownership included. We crossed it for the value model (that's the
   `cov_*` corpus) but left ownership as a 1-D blob, so `I11 × owned-field-value-
   borrow` (`#8`) was never a cell. Every enforced invariant gets the position
   sweep, not just the value-mechanics ones (§3.5).

## 2. The invariants (the durable index / spec-in-progress)

### Value model — the region this effort gives invariant discipline for the first time
- **I1 Value independence** — copying a value type (any channel) yields an
  independent value; mutating one never affects the other.
- **I2 Reference semantics** — every reference to one storage observes mutations
  through any of them; copying a reference copies the reference. (A slice header
  is a *value*; its backing is *shared*.)
- **I3 Storage fidelity** — a write is fully and exactly readable back; writes to
  distinct locations don't interfere. (No partial copies, wrong addressing, overlap.)
- **I4 Default initialization** — unwritten storage reads as zero; a partial
  literal zeroes the remainder.
- **I5 Equality** — `==`/`!=` are for scalars (value) and pointers (identity)
  only; **aggregates (struct/array/slice) are rejected at compile time**.
  *DECIDED.* Memberwise `==` silently changes meaning as fields evolve (add a
  pointer field and it becomes identity-compare), and the change is invisible at
  the use site — so equality of an aggregate must be an explicit author
  decision (`fn eq(a, b) bool`), not an operator default. (Future ergonomics:
  opt-in *derived* equality, Rust-style — not now.)
- **I6 Aggregate shape** — length/window preserved through copy / slice / pass / return.

### Type system
- **I7 No implicit numeric conversion** — mixed-width arithmetic/assignment
  rejected; casts behave (sign/zero extension). *[partly covered]*
- **I8 Mutability (per-level)** — no write through an immutable binding or
  non-`mut` pointer/slice. Mutability is **per indirection level** (`MutMask`):
  reaching a **value** field/element by projection needs the *container* writable
  to yield a `*mut` view (`&mut x.f` iff `x` is mutable); reaching **through a
  pointer** field (`*x.p`) is governed by the pointer's *own* pointee-mut bit,
  independent of the container ("const pointer to mutable value"). `&` of a
  projection must shift existing bits up (preserving inner pointer-muts) and set
  the new outer bit from the value-path writability — **currently not implemented
  for projections (#7); see §6.**

### Ownership / lifetime — invariant-framed; **binding-level** well-covered, **× position not**
- **I9 Move consumes** · **I10 Discharge exactly once** · **I11 No
  use-after-discharge** · **I12 No reference escapes its frame.**
  *(Ownership was always tested by invariant, which is why it's robust at the
  **binding** level. But it was never crossed with the position axis — owned
  **fields** and **elements** — and that hole hid `#8` behind 111 passing tests.
  "Saturated" applied to the binding column only; the field/element columns are
  open. See §3.5.)*

### Control flow
- **I13 Conservative merge** — flow facts (null, move) reconcile to the safe meet
  at joins. *[partly covered]*
- **I14 Traps, not crashes** — out-of-bounds / nil deref trap with a defined exit,
  never segfault.

> Every memory-backed bug found so far is a violation of **I1** or **I3**.

## 3. Checks (falsifiable oracle per invariant)

A check must be able to **fail** — "ran without crashing" is not an oracle.

> **Cover both sides.** Every invariant has a *positive* side (the valid
> behavior holds) and, wherever the invariant has an **enforced boundary**, a
> *negative* side (a violation is **rejected** at compile time or **trapped** at
> runtime, never silently wrong or crashing). Add negative tests for everything
> where it makes sense — a positive-only corpus silently loses the enforcement
> when a refactor drops it (e.g. ordering `<` on aggregates compiled silently
> because only `==`/`!=` had a reject test). The negative side uses the **reject**
> (`_err`) and **exit/trap** (`.exit`) oracle flavors; treat them as first-class,
> not afterthoughts. The all-scalar/all-saturated regions (I9–I12) are already
> covered this way — bring the value/type/flow invariants to the same standard.

| Inv | Positive oracle | Negative oracle (reject / trap) |
|-----|-----------------|---------------------------------|
| I1 | copy → **mutate source → read copy, assert OLD**; channels {`:=`, `=`, arg, return, literal, field→bind, elem→bind}; full-fidelity; copy-chain | — (runtime property; no compile boundary) |
| I2 | mutate-through-pointer; two refs share; slice header-independence; `*mut` param mutates caller | — |
| I3 | write-read-back; no-cross-contamination; self-overlap; two access paths | — |
| I4 | zero-init; partial-literal zeroes the rest | — |
| I5 | scalars/pointers: value-equal→true, differ→false | **`==`/`!=` AND ordering `< > <= >=` on aggregates (struct/array/slice) → REJECT** (`_err`) |
| I6 | `len`/window after copy/slice/pass/return | — |
| I7 | cast round-trip | **mixed-width arithmetic → REJECT** (`_err`) |
| I8 | `&mut x.f` writes through iff container mutable; `*x.p` writes through a `*mut` field of an *immutable* container (per-level) | **write through `*T` / immutable `&x.f` / `&arr[i]` → REJECT** (`_err`) |
| I14 | — | **bounds / nil → TRAP** (`.exit` exit-code), not segfault |

**S-THRESH** (the 8-byte boundary) is not a modifier but a check in its own
right: the *same* op with the type grown `7→8→9→16→24`. It is the fault line;
apply it wherever a class's anchor branches on size.

**Oracle flavors:** **stdout** (value differs), **reject** (`_err_test`),
**exit/trap** (`.exit`). A cell names which.

## 3.5 Invariant × position grid (the matrix to fill)

Position crosses every enforced invariant. A cell is "covered" only when it has a
falsifiable test (cov or existing-suite). Legend: **✓** covered · **○** open
(gap, needs a test) · **?** audit pending (status unknown until we map existing
tests) · **·** N/A.

| Inv \ position | local binding | struct field | array/slice elem | nested (`a.b.c`, `a.f[i]`) | param (by-val) | return | global |
|---|---|---|---|---|---|---|---|
| I1 value-independence | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ? |
| I2 reference sharing | ✓ | ? | ✓ (slice backing) | ? | ✓ (`*mut` param) | ? | ? |
| I3 storage fidelity | ✓ | ✓ | ✓ | ✓ (`#4` was here) | ✓ | ✓ | ? |
| I4 init / zero | ✓ | ✓ (partial-lit) | ? | ? | · | · | ? |
| I5 aggregate `==`/ordering reject | ✓ | · | · | · | · | · | · |
| I6 aggregate shape (len) | ? | ? | ? | ? | ? | ? | ? |
| I7 numeric / cast | ✓ (reject) | ? | ? | · | ? | ? | ? |
| I8 mutability (per-level `&`) | ✓ | ✓ (`#7`) | ✓ (`#7`) | ? | ? | · | ? |
| I9 move consumes | ✓ | ? (`owned_field_move_*`) | ○ | ○ | ✓ | ✓ | · |
| I10 discharge exactly once | ✓ | ? | ○ | ○ | ✓ | ✓ | · |
| I11 no use-after-discharge | ✓ | **○ `#8` (value-borrow)** / ✓ (ptr-borrow) | ○ | ○ | ✓ | ✓ | · |
| I12 no escape | ✓ (`retalias`) | ? | ? | ? | ✓ | ✓ | · |
| I13 conservative merge | ? | ? | ? | ? | · | · | · |
| I14 traps | ✓ (bounds/nil) | ? | ✓ (bounds) | ? | · | · | ? |

The **`?` cells are §6.5's audit job** (map existing tests → cells). The **`○`
cells are the gaps to fill** — chiefly the **ownership × {field, element,
nested}** column the binding-level corpus never reached. Newly-enumerated
ownership×position cells to probe (`#8` is the first):
- **I11 × owned-element value-borrow** — `b i64 := owned_arr[i]; dispose(owned_arr); read b` (sibling of `#8`).
- **I11 × owned-field/element ptr-borrow** — `&` form (cov_field_ptr_* covers field; element TBD).
- **I9/I10 × owned element** — move/consume an owned array element; leak check.
- **I12 × owned field/element** — `&owned_field` must not escape the frame.
- **nested owned** — owned field of an owned field; consume the outer, observe.

## 4. Equivalence classes (the pruning — from CURRENT codegen)

Each class collapses a set of shapes that share a lowering route; we test one
**representative** and skip the rest. The **anchor** ties the class to code (so a
failure points at it, and the audit can check the class still holds). **Split
risk** is the condition that, if it changes, forks the class — the thing the
audit watches.

| Class | collapses | representative | anchor (compile.go) | split risk |
|-------|-----------|----------------|---------------------|-----------|
| CL-REG | register scalars | i64 copy/r-w | scalar `move` | width/signedness |
| CL-MEMVAL | struct/array value copy (decl, assign) | 8-byte struct via `:=` | `move`/`spot_memcpy` | **size ≤8 vs >8** (forked → bug 1) |
| CL-PTR | load/store thru pointer (`*p`,`p.f`,`*p=`) | deref of ≤8 **and** >8 struct | `*Deref` ~3742–3762 | **size ≤8 vs >8** (forked → bug 3) |
| CL-FIELD | struct field r/w | small-struct member | `Dot` ~3688–3760 | member is struct vs scalar |
| CL-ELEM-ARR | fixed-array element r/w | const **and** variable index | `Index` ~3868–3969 | const vs var index; elem size |
| CL-ELEM-SLICE | slice element r/w | slice-of-struct elem | `Index` slice branch | elem memory-backed |
| CL-GLOBAL | static-init emitter | global struct + array init | `globals.go` | distinct emitter from locals |
| CL-CALL | arg spill + param + return-by-value | pass & return a struct | call/return lowering | arg vs return; size |
| **CL-COMPOSE** | **each nesting is its OWN class** | array-in-struct, struct-in-array, `a.b.c`, 2D, `p.f`… | composition of anchors | **never assume a composition is covered by its parts** (forked → bug 4) |
| CL-ADDR | `&`-of-projection mutability (`&x.f`, `&arr[i]`) | `&value-field` of mutable vs immutable container; `&pointer-field` | `Address.ASTType` projection branch ~2755 (vs named ~2710) | value-field vs pointer-field; container mut (**under-implemented → #7**) |
| CL-EQ | `==`/`!=` lowering | struct `==` (must reject) | comparison lowering / type check | aggregate vs scalar/pointer |

Inner sweep per cell (parameters, not separate cells): type kind {struct,
array-of-scalar, array-of-struct, slice-of-scalar, slice-of-struct}; **size
{1, 8, 9, 16, 24}**; mutability {immutable, `var`, `*T`, `*mut T`}; init {full,
partial, zero}. Reuse one fixed type set (`S1b/S8/S9/S16/S24`) across all cells.

## 5. The audit procedure (periodic + after any codegen refactor)

1. **Re-grep the proxies.** `nameIsAddress`, `typeIsMemoryBacked`, `Size(c) [<>=]
   8`, `case 1, 2, 4, 8`, `scale ==`, `Indirection`, plus each anchor in §4.
2. **Per class:** confirm the representative test exists and passes; confirm the
   class still *holds* — the shapes it collapses still reach the same anchor code.
3. **Detect splits.** Where an anchor now branches on a new condition (a size
   gate, a kind check) that didn't exist last audit, the class has forked — add a
   representative for **each** new sub-class. (This is the check that should have
   caught ≤8/>8.)
4. **Re-confirm the size buckets** at every split point (S-THRESH: 7/8/9/16/24).
5. **Record** the audit date and any class changes in §4, and append a dated line
   to §7.

## 6. Status — known invariant violations

Every open issue has a **failing `cov_*` test driving it** (turns green on fix);
fixed ones have a passing regression test.

| Inv | Cell | Symptom | Status | Driving test(s) |
|-----|------|---------|--------|-----------------|
| I1 | CL-MEMVAL, struct ≤8 | copy aliases | **fixed** `450a36c` | `cov_value_indep_*` (green) |
| I3 | CL-ELEM-ARR, struct ≤8 (local) | segfault | **fixed** `30261d5` | `cov_fidelity_array_elem` (green) |
| I1/I3 | CL-PTR, deref ≤8 struct | segfault | **fixed** `a0da85f` | `cov_ref_deref_{small,1byte}` (green) |
| I3 | CL-COMPOSE, array-in-struct (any size) | silent wrong value | **fixed** `94a4e90` | `cov_{compose_array_in_struct,array_in_struct_16byte}` (green) |
| I5 | CL-EQ, struct `==` | address-compare | **fixed** `6761623` — reject | `cov_equality_struct_err` (green) |
| — | CL-PTR, inline deref-field >8 | internal type error | **fixed** `ed7480a` | `cov_deref_field_inline_large` (green) |
| I8 | CL-ADDR, `&value-field`/`&elem` of mutable container | read-only (no `*mut` view) | **fixed** `22ac391` — per-level §I8 | `cov_amp_{field,elem}_mut` (green) |
| I11 | owned-field **value** borrow (`b i64 := s.h`), then dispose struct | **silent** use-after-dispose — not tracked (binding-level `b := fd` IS) | **open #8** | `cov_owned_field_borrow_use_after_dispose_err` |

Field-level borrow soundness otherwise confirmed (green guards): a field *pointer*
borrow (`&s.f`) tracks via the struct origin, so dispose invalidates it and
re-init does not revive it (`cov_field_ptr_use_after_dispose_err`,
`cov_field_ptr_no_revival_err`) — this is also the evidence that binding-level
origin-generations suffice (`DESIGN_origin_generations.md` §10).

**Memory-backed value violations all fixed; #8 (an owned-field tracking gap)
open.** #5 was reject (aggregate `==` is a compile
error); #7 implemented the per-level projection mutability rule (value-field/elem
gets the outer mut from writability; pointer/owned/nullable fields untouched;
owned-slot gate preserved). Guards green throughout: `cov_amp_field_immutable_err`
(immutable container stays read-only), `cov_ptr_field_writethrough` (`*x.p`
through an immutable container works). Whole `cov_*` corpus: **33/33 green**.

Known *loud* limitations (explicit panics, not silent): array copy through a
deref (~3750); slicing element types >8 (~4033).

## 7. Audit log
- (initial) — model established; first audit = `AUDIT_memory_backed_values.md`
  (white-box proxy grep + invariant probes), surfacing the §6 violations.
- (coverage built) — 33 `cov_*` tests realize the hot zone: 24 green value-tests
  + 1 green `_err` guard (regression net for the sound core), 8 red driving the
  5 open issues (#3:2, #4:2, #5:1, #6:1, #7:2). Folded in the #5 (reject) and #7
  (implement, per-level mutability) decisions; added CL-ADDR and CL-EQ classes.
  Coverage complete — ready to fix, each fix turning its driving test green.
- (fixed) — all 5 open issues fixed (`94a4e90` #4, `a0da85f` #3, `ed7480a` #6,
  `6761623` #5, `22ac391` #7), each paired with its now-green driving test. Full
  bosc suite **590 PASS**, go_test 11/11, `cov_*` 33/33. The #7 fix needed a
  follow-on (Address codegen spot type must match Address.ASTType) the full-suite
  gate caught after the cov set was already green — confirming the gate "red cell
  green AND full suite green," not just the cov cell.
- (negatives) — added the negative side (reject/trap oracles) of the enforced
  invariants: aggregate `==`/`!=` (array, slice, struct) and I8 write-through-
  non-mut / `&`-immutable rejects (all enforcement already held). Negative
  probing surfaced one **missing enforcement** — relational `< > <= >=` on
  aggregates compiled silently (#5 covered only `==`/`!=`) — fixed in `9ed6644`.
  `cov_*` now **39/39**, full bosc suite **596 PASS**. Lesson banked in §3: cover
  both sides; a positive-only corpus hides dropped enforcement.
