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
- **I5 Equality** — `==`/`!=` mean value (in)equality, consistently — *or* are
  rejected at compile time. (Decision owed; see §6.)
- **I6 Aggregate shape** — length/window preserved through copy / slice / pass / return.

### Type system
- **I7 No implicit numeric conversion** — mixed-width arithmetic/assignment
  rejected; casts behave (sign/zero extension). *[partly covered]*
- **I8 Mutability** — no write through an immutable binding or non-`mut`
  pointer/slice. *[covered]*

### Ownership / lifetime — already invariant-framed and SATURATED (`owned`/`retalias`)
- **I9 Move consumes** · **I10 Discharge exactly once** · **I11 No
  use-after-discharge** · **I12 No reference escapes its frame.**
  *(These are why ownership is robust: it was always tested by invariant. The
  value model wasn't — it was assumed. That asymmetry is the whole diagnosis.)*

### Control flow
- **I13 Conservative merge** — flow facts (null, move) reconcile to the safe meet
  at joins. *[partly covered]*
- **I14 Traps, not crashes** — out-of-bounds / nil deref trap with a defined exit,
  never segfault.

> Every memory-backed bug found so far is a violation of **I1** or **I3**.

## 3. Checks (falsifiable oracle per invariant)

A check must be able to **fail** — "ran without crashing" is not an oracle.

| Inv | Oracle shapes |
|-----|---------------|
| I1 | copy → **mutate source → read copy, assert OLD**; across channels {`:=`, `=` re-init, by-value arg, return, literal field, literal element, field→bind, elem→bind}; full-fidelity (read *every* member); copy-chain |
| I2 | mutate-through-pointer (X sees it, a prior copy doesn't); two refs share; slice header-independence; `*mut` param mutates caller |
| I3 | write-read-back; no-cross-contamination (adjacent slots keep their own); self-overlap (`x=f(x)`, `arr[i]=arr[j]`, `s.a=s.b`); **two access paths** (direct `s.arr[i]` + `p:=&s.arr[i]`, mutate one observe the other) |
| I4 | zero-init; partial-literal zeroes the rest |
| I5 | value-equal distinct → true, differ-in-any-member → false (or **reject**) |
| I6 | `len`/window after copy/slice/pass/return |
| I7 | cast round-trip; mixed-width **reject** |
| I14 | bounds / nil → **trap** (exit-code oracle), not segfault |

**S-THRESH** (the 8-byte boundary) is not a modifier but a check in its own
right: the *same* op with the type grown `7→8→9→16→24`. It is the fault line;
apply it wherever a class's anchor branches on size.

**Oracle flavors:** **stdout** (value differs), **reject** (`_err_test`),
**exit/trap** (`.exit`). A cell names which.

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

| Inv | Cell | Symptom | Status |
|-----|------|---------|--------|
| I1 | CL-MEMVAL, struct ≤8 | copy aliases | **fixed** `450a36c` |
| I3 | CL-ELEM-ARR, struct ≤8 (local) | segfault | **fixed** `30261d5` |
| I1/I3 | CL-PTR, deref ≤8 struct | segfault | **open (#3)** |
| I3 | CL-COMPOSE, array-in-struct | silent wrong value | **open (#4)** |
| I5 | struct `==` | address-compare (silent wrong) | **open (#5)** — decide: field-wise vs reject |
| — | CL-PTR, inline deref-field >8 | internal type error (rejects) | **open (#6)** |

Known *loud* limitations (explicit panics, not silent): array copy through a
deref (~3750); slicing element types >8 (~4033).

## 7. Audit log
- (initial) — model established; first audit = `AUDIT_memory_backed_values.md`
  (white-box proxy grep + invariant probes), surfacing the §6 violations.
