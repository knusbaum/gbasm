# Proposal: Moving Owned Fields Out of Aggregates (typestate-lite)

## Summary

Allow a **non-null** `owned` field to be moved out of an owned aggregate,
so that aggregates holding owned resources can actually be destructed
field-by-field. Today this is rejected, and the only accepted way to
"consume" such an aggregate (`dispose`) silently leaks its fields.

Owned fields become first-class move sources, tracked like local `owned`
pointer variables. Moving a non-null owned field out puts the aggregate in
an **inconsistent** state: a local flow fact that cannot cross a function
scope boundary. While inconsistent, the aggregate may not escape the
current function (no call arguments, no return, no escaping assignment) —
the sole exceptions being the two shape-blind intrinsics `dispose` and
`free`. Re-initializing the field restores consistency.

This is the "partial-disposal / typestate" work that DESIGN.md §766 and
§886 ("Future direction: typestate and witnessed borrows") describe as
intentionally deferred. This proposal implements the field-move slice of
it. The nullable-owned-field path is unchanged.

## Motivation

A struct with a non-null owned pointer field can be **constructed** but
only **destructed by leaking**. Demonstrated against the current compiler:

```boson
type leaf struct { v i64 }
type box  struct { child owned *mut leaf }
```

| Attempt | Result (current compiler) |
|---|---|
| `var c owned *mut leaf = b.child` (move the field out) | **rejected:** `Cannot move non-null pointer field child; use *?T if the field may be emptied` |
| `free(b)` while `child` is live | **rejected:** `free(b) would leak owned field b.child` |
| `dispose(b)` | **compiles and runs — and leaks the leaf's heap memory** |

So the only path the compiler accepts is a silent leak. The compiler's
suggestion ("use `*?T`") forces every cleanup-bearing field to be nullable,
which is a workaround, not a fix: a non-null owned field is a perfectly
reasonable thing to want (a resource that is *always* present for the
lifetime of the aggregate), and it must be destructable.

## Current state

What already exists and is **unchanged** by this proposal:

- **Nullable owned fields** (`*?owned T`) can already be moved out. Moving
  one leaves `nil` behind — a valid, memory-safe placeholder — so the
  aggregate stays consistent and remains freely passable. The existing
  machinery handles this and must keep working byte-for-byte.
- **Per-field consumption facts.** `checker.go` tracks
  `ownedFieldFacts map[FlowPath]bool` with `SetOwnedFieldConsumed` /
  `OwnedFieldConsumed`, plus flow snapshot/restore and exact branch-merge
  (`mergeOwnedFieldFactsExact`). The `free` leak-check
  (`checkOwnedFieldsConsumedBeforeRawFree`, `compile.go:632`) already
  consults these facts.

What is **not** built (and this proposal adds):

- Moving a **non-null** owned field out (blocked by the guard at
  `compile.go:712`).
- The scope-escape restriction on an inconsistent aggregate.
- Re-initialization of a consumed owned field (blocked by
  `Cannot assign to owned field … of an owned aggregate after initialization`).

DESIGN.md §766: "Direct reassignment of an owned field through an owned
aggregate is rejected. Replacing such a field safely requires
partial-disposal / typestate support, which is intentionally deferred."
DESIGN.md §886 frames the general case as future typestate work and notes
the foundation is forward-compatible with it.

## The model

### Aggregate consistency states

An owned aggregate binding is in one of three states with respect to its
owned fields:

- **Consistent** — no non-null owned field is consumed. (All owned fields
  are live, *or* any consumed fields are nullable and therefore `nil`.)
  Fully usable: readable, borrowable, passable, returnable, movable.
- **Inconsistent (partial)** — at least one **non-null** owned field has
  been moved out and not re-initialized. Use-restricted (below).
- **Fully consumed** — every owned field is consumed. `free` becomes legal
  (the leak-check passes); the aggregate is otherwise still
  use-restricted exactly like the inconsistent state (its non-null fields
  still dangle).

The single predicate that gates everything:

> **`inconsistent(f)` ≡ `f` has at least one consumed *non-null* owned
> field.**

Consuming a **nullable** owned field does **not** make the aggregate
inconsistent — `nil` is a representable, safe state for that field, and the
machinery already deals with it. The new restriction applies *only* to
moved-out non-null owned fields, which dangle because there is no
placeholder to leave behind.

### Owned fields are first-class move sources

A non-null owned field may be moved out, exactly like moving a local
`owned` pointer:

```boson
var x owned *i64 = new(i64)
free(x)        // legal: moves x
// *x = 10     // illegal afterwards: use-after-move
```

After moving `f.a` out, `f.a` is consumed:

- moving or reading `f.a` again is a use-after-move error;
- moving/reading a **different**, still-live field `f.b` is fine
  (per-field tracking — moving `f.a` does **not** block `f.b`).

### The scope-escape gate (the new rule)

While `inconsistent(f)` holds, `f` (and `&f`, and any handle that exposes
its fields) **may not leave the current function scope**, because the
consistency facts are local and cannot propagate across the boundary; the
receiving scope would see a field that looks live but dangles. Rejected:

- **Passing `f` as a function argument** — borrow (`*foo`) *or* owning
  (`owned *foo`). A callee names the concrete shape and may dereference the
  dangling field; the compiler has no effect summaries and must assume it
  does.
- **Returning `f`.**
- **Assigning/moving `f` to a binding that outlives or leaves the scope**
  (e.g. a file-scope global), or any move that relabels it out of the
  tracked binding.

Allowed while inconsistent (all strictly local, or shape-blind):

- read/move any still-live sibling field; take `&f.b` of a live field and
  pass *that* (it is a live field, not the aggregate);
- **re-initialize** a consumed field (below);
- **`dispose(f)`** and **`free(f)`** — the two exceptions (below).

### The exceptions: `dispose` and `free` only

The exception set is exactly **`{dispose, free}`** and is provably closed.

An operation is safe on an inconsistent aggregate iff it is
**shape-blind** — it consumes the obligation without dereferencing the
pointee's fields. The only shape-blind consumers in the language are these
two compiler intrinsics:

- **`dispose(f)`** lowers to no code at all, so it cannot read any field.
  Legal in any state. (As today, it may abandon live fields — the
  programmer's documented responsibility.)
- **`free(f)`** lowers to `_heap.free(p *mut byte)`: it passes the raw
  pointer + size header to the allocator and never touches user fields.
  Its existing leak-check additionally forbids it unless *every* owned
  field is consumed, so by the time `free` is legal there is no live field
  for it to mishandle — it only releases the container block.

**Why no third exception can exist:** Boson has **no user-definable
generics**. Every user function names a concrete type in its signature, so
it *could* dereference the consumed field, and with no interprocedural
write/effect summaries the compiler must assume it does. Even a user
function whose body is literally `free(p)` is rejected, because the
compiler cannot see that. So "shape-blind" collapses to exactly "is a
direct `dispose`/`free` intrinsic call." Only the compiler's own
intrinsics qualify, and only these two *consume*. (`len` does not take
owned; `alloc`/`new` *produce* owned; `owned(expr)`/`T(expr)` are
promotion/cast and do not consume the aggregate's obligation.)

Note the exemption is for the **direct intrinsic call** only. Passing the
inconsistent aggregate to a user function that internally calls `free`/
`dispose` is still rejected — the compiler can't see the body.

### Re-initialization

Assignment to an owned field is legal **iff that field is currently
consumed**:

```boson
move_owned(f.a)     // f.a consumed; f now inconsistent
f.a = new(i64)      // re-init: legal (old value already moved; no leak)
                    // f.a live again
```

- Re-init clears the field's consumed fact; the field is live again.
- Assigning to a **live** owned field stays **illegal** — it would drop the
  old obligation without consuming it (a leak). (This preserves the intent
  of the current `Cannot assign to owned field … after initialization`,
  narrowing it to the live-field case.)
- Once no non-null owned field is consumed, `inconsistent(f)` is false and
  `f` may escape normally again.

### Local aliasing — implemented as a symmetric gate (was: test obligation)

Implementation found this needs an *active* gate, not just existing
coverage, and it must be **symmetric**:

- **Move while aliased.** Moving a non-null owned field out is rejected if
  the aggregate already has a live alias (`HasLiveAlias`, which — unlike
  `HasLiveOwnedAlias` — counts borrow-shaped `*T` aliases, since a borrow
  can still *read* the field).
- **Alias while inconsistent.** Symmetrically, *forming* a new alias of an
  already-inconsistent aggregate is rejected. Without this, an alias formed
  *after* the move would read the zeroed slot. The **complete** list of
  alias/escape sites that must be gated (enumerated explicitly because
  syntactic gating is only as sound as the enumeration):
  - call argument — `compileCall` arg loop;
  - `return` — `*Return`;
  - binding init / assignment — `*VarDecl` / `*Assignment` RHS
    (`var x = f`, `x = f`);
  - `&f` — `*Address` value path;
  - struct-literal field — `compileStructLiteralInto` (`holder{p: f}`);
  - array-literal element — `compileArrayLiteralInto` (`[f]`).

  All six call `aggregateBindingName` + `checkAggregateMayEscape` /
  `checkAggregateNotAliasedWhileInconsistent`. A field reference (`f.a`,
  `&f.b`) is a `Dot`, not a whole-aggregate `Symbol`/`Address`, so
  `aggregateBindingName` returns "" and field moves / live-sibling
  addressing are correctly **not** gated. A **method-call receiver**
  (`f.method()`) lowers the receiver through the same call-argument path, so
  it is covered by the call-arg gate (verified). A **store-through-pointer**
  (`*pp = f`) is an `*Assignment` whose `Val` is `f`, so the assignment gate
  catches it regardless of target shape (verified).

> **Partial consolidation (done).** Per-site syntactic gating is only as
> sound as the site enumeration — this list grew several times during
> implementation (alias-after-move, loops, struct/array literals,
> method-receiver) before it was complete. As a first step toward a
> recording-point gate, the **`VarDecl` binding-alias** (`var x = f` /
> `var x = &f`) is now gated centrally in `checkedAssignPointer`, the wrapper
> at the pointer-flow `AssignPointer` call — so any future var-init syntax
> that records a binding alias through that path is caught automatically. Its
> syntactic gate was removed.
>
> **Remaining (deferred).** The `*Assignment` (`x = f`) and multi-return
> paths don't reach a clean `AssignPointer` choke point, and struct/array
> literal capture records via `SetFieldPointer`/`SetPathPointer`, so those
> keep syntactic gates for now. Fully centralizing the local-alias half
> (routing `SetFieldPointer`/`SetPathPointer` through checked wrappers too)
> remains worthwhile defense-in-depth before adding new pointer-capturing
> syntax. Cross-boundary escape (call/return) stays a separate gate by design
> — no caller-side alias is recorded.

Both directions are required: the mutable-alias path is additionally blocked
by the pre-existing "cannot take mutable address of owned binding" rule, but
immutable aliases need these gates. Message:
`Cannot alias "f" while owned field "f.a" is moved out; the alias could read
the moved-out field`.

### Loop back-edge

A field moved out inside a loop body without re-initialization would be
re-moved on the next iteration (use-after-move / double-free). The
owned-*binding* loop machinery has **two** checks, and the owned-*field*
case needs both:

1. **Pre-loop vs. back-edge** — a field live before the loop but consumed at
   the back-edge is rejected (`Owned field "f.a" is consumed inside a loop
   body; this would be invalid on the second iteration`).
2. **Across all back-edges** — the back-edge is reached by the fall-through
   *and* every `continue` path. A field consumed on one path but live on
   another is rejected (`Owned field "f.a" has inconsistent state across loop
   backedges`). **This second check was initially missed** — the field code
   mirrored only check 1, not the cross-back-edge agreement the binding code
   does via `SameObligationLiveAcross`. A `continue` that frees a field while
   the fall-through leaves it live compiled and double-freed at runtime until
   this was added.

Re-initializing the field on every back-edge path clears the fact and is
allowed.

The branch-merge, loop, `break`/`continue`, nested-branch, and scope/alias
compositions were probed explicitly (12 scenarios): all reuse the existing
`ownedFieldFacts` snapshot + pointer-flow `State`, which are already plumbed
through fork/merge/loop/scope, so the `inconsistent` predicate (derived, not
stored) stays consistent with the merged state. The only gap found was the
cross-back-edge check above.

### Original note: local aliasing (now superseded by the gate above)

The escape gate covers values crossing the scope boundary. A dangling field
can also be reached through a **local alias** taken before the move:

```boson
var alias *mut foo = f
move_owned(f.a)
use(alias.a)        // must be rejected — alias.a dangles
```

This stays inside the scope, so it is the flow tracker's responsibility,
not the escape gate's. The existing machinery already invalidates
owned-field facts on `*mut` borrows (observed firing today). This proposal
treats local-alias soundness as a **test obligation**: explicit tests that
alias the aggregate, move a non-null field, and attempt to read through the
alias — confirming rejection — rather than assuming coverage.

### Scope end

Falls out of existing checks. An owned aggregate binding must be consumed
(disposed/freed) before its scope ends; `free` requires all owned fields
consumed. A partially-consumed aggregate that is neither fully consumed nor
freed by scope end is therefore already an error
(owned-binding-not-consumed / would-leak-field). Branch and loop merges
already require owned-field facts to agree exactly
(`mergeOwnedFieldFactsExact`), so a field consumed on one path but not
another is rejected at the join — no new merge logic required.

## Worked examples

Destructing an aggregate with two non-null owned fields (the motivating
case):

```boson
type foo struct {
    a owned *i64
    b owned *i64
}

fn main() {
    var f owned *foo = make_foo()

    free(f.a)        // f.a consumed; f inconsistent
    // pass_to(f)    // ILLEGAL while inconsistent: f.a dangles
    free(f.b)        // f.b consumed; f now fully consumed
    free(f)          // legal: all owned fields consumed
}
```

Re-init back to a passable state:

```boson
fn main() {
    var f owned *foo = make_foo()
    free(f.a)        // inconsistent
    f.a = new(i64)   // re-init: consistent again
    pass_to(f)       // legal again
    destroy(f)
}
```

Nullable field — unchanged, stays passable after nulling:

```boson
type bag struct { item owned *?mut leaf }
// moving item out leaves nil; bag remains consistent and passable.
```

## Implementation plan

Touch points (all in `cmd/bosc/`):

1. **Relax the move-out guard.** `compile.go:712` — remove the
   `Cannot move non-null pointer field` rejection for the
   `NilMask&1 == 0` case so a non-null owned *pointer* field can be moved
   out. Keep rejecting move-out of **value-typed** owned fields
   (`owned i64`, `owned T` with `Indirection == 0`): those have neither a
   placeholder nor a pointer identity to hand off, and are out of scope
   here. Moving the field sets `SetOwnedFieldConsumed(fieldPath, true)`
   (the nullable path already does this).
2. **`inconsistent(binding)` predicate.** A helper that reports whether a
   binding has any consumed **non-null** owned field (walk the struct decl;
   for each owned field with `NilMask&1 == 0`, check `OwnedFieldConsumed`).
3. **Escape gate.** At the sites where an aggregate value leaves the scope,
   reject when `inconsistent` and the callee is not the `dispose`/`free`
   intrinsic:
   - function-call argument lowering,
   - `return`,
   - assignment/move of the aggregate to another binding (esp. globals /
     escaping targets).
4. **Re-init.** Where `Cannot assign to owned field … after initialization`
   is raised, allow the assignment when the target field is currently
   consumed; on assignment, clear the consumed fact (field live again).
5. **Use-after-move on fields.** Ensure value-reads/derefs of a consumed
   non-null field are rejected, not only re-moves (extend the existing
   `OwnedFieldConsumed` check at field-read sites if needed).
6. **Leave the nullable path untouched** and the `free` leak-check as-is
   (it already gates on all-fields-consumed).

### Error messages (finalized)

Principle: reuse the existing **local-variable** ownership messages for the
field cases, so a field behaves and reports exactly like a local `owned`
pointer. The local-variable messages already exist and are good; this
feature extends them to fields rather than inventing parallel wording.

**Escape gate (1–3).** All three carry the *same* explanation and advice;
only the verb is specialized. Capitalized to match the existing `Cannot …`
style:

- pass as argument: `Cannot pass "<f>" while owned field "<f.x>" is moved
  out; only dispose/free are allowed on a partially-consumed aggregate —
  consume or re-initialize "<f.x>" first`
- return: `Cannot return "<f>" while owned field "<f.x>" is moved out;`
  …(same tail)
- escaping move/assign: `Cannot move "<f>" out of this scope while owned
  field "<f.x>" is moved out;` …(same tail)

**(4) Use of a moved-out field** — reuse the canonical use-after-move
message verbatim (same one locals get, `compile.go:4200`):

- `Use of "<f.x>" after it was moved`

**(5) Assign to a still-live owned field** — mirror the existing
local-binding message (`Cannot assign to owned binding "x" before consuming
its current value`), changing only "binding" → "field". This **replaces**
the current vague field message (`Cannot assign to owned field x of an
owned aggregate after initialization`), which gives no advice and
blanket-rejects re-init:

- `Cannot assign to owned field "<f.x>" before consuming its current value`

**Note — local-variable parity (answers DESIGN review).** The local case
is already correct and is the model: reassigning a *live* owned local is
rejected with the message above; reassigning a *moved* owned local is
already legal (re-init works — verified: `var x owned i64 = 1; take(x);
x = 2` compiles). The local messages need **no** improvement; this proposal
brings the field case to parity (same messages, same re-init behavior).

**Removed:** `Cannot move non-null pointer field next; use *?T if the field
may be emptied` — deleted; the move-out it blocked is now the feature.

## Existing tests that change (audited)

Three `_err_test` files pin behavior at the boundary of this change. Audited:

- **`owned_nonnullable_pointer_field_move_err_test.bos` → must become a
  passing test.** Its body *is* the destructor pattern this proposal
  enables (move a non-null `owned *mut i64` field out, `free` it,
  `dispose` the box). Today it asserts `Cannot move non-null pointer field
  next…`; after the change it is correct code. Convert `_err_test` →
  `_test` with an exit-0 / stdout expectation. **This is the headline
  behavior flip.**
- **`owned_struct_field_reassign_owned_err_test.bos` → stays rejected**
  (message may be reworded). It reassigns a **live** `owned i64` field
  (`b.x = y` with `b.x` never consumed) — still a leak under the new
  "assign legal iff field currently consumed" rule. Keep as `_err_test`;
  update the expected message if the wording changes.
- **`owned_plain_field_partial_move_err_test.bos` → stays rejected.** It
  moves a **value-typed** owned field (`owned child`, `Indirection == 0`)
  out — explicitly out of scope (no pointer identity/placeholder). The
  `Cannot move owned field … out of an owned aggregate` message stays.

Other pinned `_err` tests (`owned_free_live_field_err_test`,
`owned_alias_*_invalidate_err_test`,
`owned_mut_borrow_call_invalidate_err_test`,
`owned_free_field_nested_leak_err_test`, …) exercise `free`-with-live-field
and alias-invalidation, which this proposal preserves; they should remain
green, and the alias-invalidation ones double as coverage for the
local-aliasing obligation. Re-run the full suite to confirm.

## Test matrix

Add to `cmd/bosc/tests/` (runnable `_test.bos` for the passes, paired
`_err_test.bos` for the rejections):

1. **Move both non-null fields out, then `free` the aggregate** — passes.
2. **`dispose` an inconsistent aggregate** — passes (documents that it may
   leak live fields).
3. **Pass an inconsistent aggregate to a borrowing fn** — rejected.
4. **Pass an inconsistent aggregate to an owning fn (not dispose/free)** —
   rejected.
5. **Return an inconsistent aggregate** — rejected.
6. **Assign an inconsistent aggregate to a global** — rejected.
7. **Re-init the consumed field, then pass** — passes.
8. **Read/move a consumed field again** — rejected (use-after-move).
9. **Read/move a *live* sibling field while another is consumed** — passes.
10. **Local alias the aggregate, move a non-null field, read through the
    alias** — rejected (the aliasing obligation).
11. **Branch merge:** field consumed on one branch only — rejected at join.
12. **Nullable regression:** move a nullable field out and *still pass the
    aggregate* — passes (proves the existing path is untouched).

## DESIGN.md updates

- §766 ("Owned struct fields"): change from "reassignment … is rejected …
  intentionally deferred" to documenting move-out + re-init + the escape
  rule.
- §886 ("Future direction: typestate"): move the **partial consumption of
  fields** piece from "future" to "implemented (field move-out)," keeping
  the stacked-`owned *owned T` and witnessed-borrow items as still-future.
- §389 (free non-recursive): confirm wording still holds (it does — free
  still requires fields pre-consumed).

## Non-goals / still deferred

- **Stacked ownership** (`owned *owned T`) partial consumption — DESIGN
  §890. Unchanged.
- **Witnessed borrows** — DESIGN §892. Unchanged.
- **Value-typed owned fields** (`owned i64`, `owned T`) move-out — no
  pointer identity to hand off; still consumed only by disposing the whole
  aggregate. (Such fields rarely need real cleanup; an `owned i64` is the
  degenerate case.)
- **Interprocedural effect summaries** that would let a non-`dispose`/`free`
  function be exempt — explicitly out of scope; the closed `{dispose,free}`
  exception set depends on their absence.

## Open questions

1. ~~Should an inconsistent aggregate be movable to another local binding
   in the same scope?~~ **Resolved** — do whichever is easy without
   breaking tracking: if owned-binding local moves are already supported,
   allow it **provided the per-field consistency facts follow to the
   moved-to binding**; if they are not (or it's awkward), leave it
   rejected. Not important to allow; the only hard requirement is that the
   tracking stays correct.
2. Error-message wording above is a draft; finalize during implementation.
3. ~~Does any existing test rely on the current "non-null field move-out is
   rejected" behavior?~~ **Resolved** — see "Existing tests that change"
   above. One test flips to passing
   (`owned_nonnullable_pointer_field_move_err_test`); two stay rejected
   (live-field reassign; value-typed field move-out).
