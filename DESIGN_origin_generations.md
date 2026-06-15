# Design note — Origin generations

Status: planned, not started. **Reviewed** — see §10 for the decisions that
came out of review (notably: choose **(G)**, *not* eager creation). A blocking
prerequisite surfaced while investigating: a separate **struct value-copy
aliasing bug** (`y := x` for a memory-backed struct shares storage instead of
copying) is being fixed first; generations resume after.

Prereq context: the owned-scalar transfer work (borrows survive a same-scope
rebind) and the `assertOwnerAdoptsLiveOrigin` / `checkReadable` consolidations
are already in. This note plans the follow-on that lets owned **aggregate**
re-init work and closes the whole "revived borrow" class structurally.

## 1. Problem

Flow tracking stores liveness lazily and keys it by **name**:

- `type Origin string` — an origin *is* a binding name.
- `origins map[Origin]originInfo{kind, validity}` — one central mutable cell
  per name.
- A borrow stores the **key** (`PointerExpr{Origin}`), not a liveness snapshot.
- `CheckDerefValidity` reads `origins[ptr.Origin].validity` at read time.

So every holder of a name reads liveness *by reference* through one cell.
Re-initializing a binding needs a fresh **live** origin for it, but the only
name-keyed identity available is `Origin(name)` — the same one stale borrows of
the binding's *previous* value still hold. Reviving it to `Live` revives them.
That is why:

- owned **scalar** rebind is safe (it *transfers* the source's already-live
  origin; it never re-mints `Origin(name)`), but
- owned **aggregate** rebind by assignment is currently **rejected**
  (`ownedAggregateRebindSource`) — it consumes the source, then has nowhere
  sound to point the re-inited binding.

## 2. The invariant we want

> **An origin identity is never reused across binding lifetimes.**

Concretely: "the current origin of binding `X`" is *its flow link*
(`pointers[X].Origin`), and is **never reconstructed** as `Origin(X)`. Each
declaration or re-initialization mints a brand-new identity; old identities stay
dead forever.

## 3. Representation

Make `Origin` a unique identity that carries a display name:

```go
type Origin struct {
    Name string // the root binding's name — for diagnostics and name-keyed lookups
    Gen  uint32 // unique per lifetime
}
```

`Gen` is drawn from a **single shared monotonic counter** reached by pointer
through state snapshots (e.g. on the root `State`, or threaded from the
`Context`). Global uniqueness is the key property: because the flow `State` is
snapshotted and `Merge`d across branches, a per-`State` counter would mint
colliding gens on sibling branches. One shared sequence makes every minted
origin globally distinct, so `Merge`'s union-by-key never conflates two
lifetimes.

## 4. Get, mint, and get-or-create — the operations behind `Origin(name)`

Today `Origin(name)` does double duty: it both *names the current identity of a
binding* and *constructs a fresh one*. Generations force these apart into three
operations:

- **get** — the current live origin of a binding. This already exists as
  `Pointer(binding).Origin` (the link), which `Merge` keeps correct. The bare
  `flow.Origin(name)` reconstructions are all really *gets*.
- **mint** — a brand-new identity (declaration, re-init, an anonymous literal's
  storage, a synthesized derived alias). Fresh `Gen` each time.
- **get-or-create** — return the existing link if present, else mint. Today this
  is how a plain value local first acquires a storage identity (see §4.1).

The split has a useful asymmetry:

> `flow.Origin(name)` is a **type conversion** (`string`→`Origin`), not a
> method. Changing `type Origin string` to the struct makes every
> `flow.Origin(name)` a **compile error** — so the compiler enumerates the
> *get* sites for you; you cannot miss one. But the `New*Origin` calls keep
> compiling, so the **mint side is not auto-flagged**: each of the ~11 call
> sites must be hand-classified as mint vs get-or-create. The get-or-create
> ones are the hazard — a get-or-create silently demoted to *always mint*
> orphans every borrow chain through that binding (the `&x` fallback comment at
> `compile.go:1670` already documents exactly this trap).

### 4.1 Decision: keep lazy creation, *name* the get-or-create — option (G)

Only **owned** value locals get an origin at declaration (`compile.go` ~3092,
gated `else if ast.Type.HasOwned()`); pointer bindings get `DeclarePointer`;
**plain non-owned value locals get nothing**. So `var x i64` has no origin until
a pointer into it is formed — `&x` (`1670`) or `&arr[i]` (`1708`) — where
`pointerExprForAST` does get-or-create (`return existing link, else
NewLocalOrigin(name)`). That lazy create is the *only* reason `pointerExprForAST`
ever mints.

Two ways to defuse the get-or-create hazard were considered:
- **(E) eager creation** — give every value local a self-origin at declaration
  (drop the `HasOwned` gate at ~3092), so `&x` always *gets* and the create
  branches go dead. Yields a clean total invariant.
- **(G) name the operation** — leave creation lazy; route the `&x` fallbacks
  through a named `originForRead` (get-or-create-*once*, idempotent — never a
  fresh generation) so it can't be mistaken for a fresh mint. No change to
  *when* origins are created.

**Decision: (G).** Reasons (from review + investigation):
1. Get-or-create exists *only* for non-owned value locals, whose origins are
   pure **escape-tracking** artifacts — no consume/dispose/revival semantics.
   It is **orthogonal** to what generations fixes (owned re-init revival). Eager
   creation deletes it as a side effect of an unrelated escape-land change.
2. Eager creation perturbs the flow facts of every non-owned local. The decl
   re-link at `compile.go:3188` (`!ownedValueDestNoTransfer && pexpr.KnownOrigin`)
   already fires for `var y = x` whenever `x` has an origin — so giving all
   locals origins makes it fire universally. Empirically this is **inert for
   scalars and structs** at the borrow-check level (no rejection/escape change),
   but it engages aggregate origin-presence machinery (field-pointer
   propagation, slice-escape, the inconsistent-aggregate gate in
   `checkedAssignPointer`) — exactly the region generations already touches, for
   **zero benefit** to the revival fix.
3. (G) is behavior-preserving *by construction*; (E) is only verifiable by
   running the suite.

(E) is not wrong — if the total-invariant elegance is ever wanted, it is its own
change with its own suite run, not riding in on the ownership fix.

The other two `New*` calls in `pointerExprForAST` (`1675` anonymous `&literal`
storage, `1831` synthesized derived alias) are genuine mints, unaffected by (G).

Note on the over-link (`var y i64 = x` linking `y` to `x`'s origin): this is
imprecise but **sound and load-bearing**. It cannot be gated on "source is
owned": the transitive chain `fd owned; b i64 := fd; t i64 := b` requires the
re-link to fire even though `b`'s type is non-owned, because `b` carries `fd`'s
origin — consuming `fd` must still invalidate `t`. Origins don't record
ownership; soundness rides on "is this origin consumed," and non-owned origins
are never consumed, so over-linking to them is inert. Leave it; a code comment
at `3188`/`1670` is the right amount.

## 5. Site audit (the actual work)

Two assumptions are baked in today: **(a) an origin can be reconstructed from a
name** (`flow.Origin(name)`), and **(b) `string(origin)` is the binding name**
(used both for messages and for name-keyed lookups). Generations break both.

### 5a. Reconstruct-identity-from-name (all *gets*) → use the link
These are the sites the type change flags automatically.
- `compile.go:1990` — `ptr.Origin == flow.Origin(path.Root)` (the self-origin
  test in `invalidateOwnedFieldFactsForMutableTarget`). → compare against the
  binding's current origin: `ptr.Origin == pf.Pointer(Binding(path.Root)).Origin`.
  Add a helper `currentOriginOf(c, name)` and use it here.
- `checker.go:275` — `InvalidateLocalOriginsForScope` invalidates
  `flow.Origin(name)` for each value local at scope exit. → invalidate the
  binding's current link origin instead (a re-inited binding's live gen must be
  the one that dies at scope exit).
- `checker.go:321` — the `MoveConsume` fallback `InvalidateOrigin(flow.Origin(name))`
  (when the binding has no `KnownOrigin` link). An owned binding always has a
  link, so this fallback should be unreachable for owned consumes — keep it but
  route it through `currentOriginOf`, or assert and confirm nothing reaches it.
- `compile.go:1848` — `merged.Origin == ap.Origin` compares two origin *values*;
  **unaffected** (still a valid identity comparison once Origin is a struct).

### 5b. `string(origin)`-as-name → use `origin.Name`
- `flow/state.go:547/549` — `CheckDerefValidity` messages. → `ptr.Origin.Name`.
- `compile.go:862` — `name = string(src.Origin)` then `DeclaredTypeForVar(name)`
  in `checkedAssignPointer`. This uses the origin string as a **binding name for
  a type lookup** — the sharp one. → `src.Origin.Name` (the root binding the
  origin belongs to is exactly what that lookup wants).
- `compile.go:3841` — `origin := string(ptr.Origin); … origin == name` (address-
  of-field diagnostic). → `ptr.Origin.Name` for both the compare and the message.
- `compile.go:933`, `compile.go:4671` — escape diagnostics "Pointer to local
  variable %q". → `ptr.Origin.Name`.

`Origin.Name` carries the **root** binding's name (the owner the origin was
minted for), which is exactly what every name-keyed lookup above intends.

### 5c. `New*Origin` call sites (the *mint* side — hand-classify)
The type change does **not** flag these; classify each of the ~11 calls:
- declaration / re-init self-origins (`compile.go:3092`, `3280`, `3379`) — **mint**.
- `NewBorrowedOrigin` for params (`1091`, `2724`, `2732`) — **mint** (once per param).
- `NewAllocatedOrigin` for `alloc()` (`1737`) — **mint**.
- `1675` (`&literal` storage), `1831` (synthesized derived alias) — **mint**.
- the `&x`/`&arr[i]` fallbacks (`1670`, `1708`) — **get-or-create**. Under (G)
  these route through a named `originForRead` (create-once, *not* a fresh gen),
  so they never mint a new generation. This is the hazard the naming defuses.

The four `New*` minters (flow/state.go ~468–525) draw a fresh `Gen` when used as
a *mint*; `originForRead` mints only if the binding has no origin yet (its first
and only one). **Verify** no *mint* call is reached more than once for the same
intended identity.

## 6. Merge

Good news — little to no change. `flow.Merge` (state.go:136) already:
- unions origins by key and meets validity per key (`mergeOriginInfo`), and
- has `joinOrigins` to reconcile a binding whose origin differs across branches.

With a shared global gen counter, the two branches' origins are distinct keys,
so the union/meet works unchanged; "current origin of X after the join" is just
the merged `pointers[X]` link, which `Merge` already reconciles (and synthesizes
a join origin for when it differs). **The one thing to validate with a test**: a
binding reassigned on one branch only, then read after the join — confirm the
pre-join borrow resolves to the conservative (dead/unknown) result and a
post-join read is sound.

## 7. What it unlocks / removes

- Delete `ownedAggregateRebindSource` and the directed rejection; owned
  aggregate assignment-rebind (`s = k`) works via a fresh identity, no revival.
- Re-init becomes uniform across {scalar, aggregate} × {`:=`, `=`}.
- `assertOwnerAdoptsLiveOrigin` and `checkReadable` are unchanged and still
  valid (they assert/consult identity liveness, which generations only make more
  precise).

## 8. Migration (each step ends green)

1. **Name the operations — option (G).** Split the conflated uses into
   `currentOrigin` (get, the link), `NewOrigin` (mint fresh gen), and
   `originForRead` (get-or-create-once). Route the `&x`/`&arr[i]` fallbacks
   (`1670`, `1708`) through `originForRead`. No change to *when* origins are
   created — behavior-preserving. Suite green.
2. **Representation (two sub-steps).** (2a) Introduce the `Origin` struct (or
   opaque handle — see §10) + shared counter threaded through *every* `NewState()`
   path, with `Gen` always `0` — pure type churn, no behavior change. (2b) Make
   the *mint* sites (§5c) draw fresh gens; fix every §5 site (gets are
   compiler-flagged; mints are §5c). Keep the aggregate rejection in place.
   Splitting 2a/2b means a break tells you which half failed. Suite green.
3. **Lift the restriction** — *gated on the field-level check (§10)*. Remove
   `ownedAggregateRebindSource` + its rejection; let aggregate assignment-rebind
   mint a fresh identity. Convert `owned_struct_assign_rebind_err_test` → a
   positive run-test. Do **not** start this until the `fieldPointers` question is
   answered.
4. **Revival regression tests** (the point of the whole change): for scalar AND
   aggregate, decl AND assign — borrow a binding, dispose/consume it, re-init it,
   then read the *old* borrow → must stay REJECTED. Plus the merge-divergence
   test from §6. The existing D/E dispose-time tests must still pass.

## 9. Risks / watch-items

- The `string(Origin)`-as-name lookups (esp. `compile.go:862`) are the failure-
  prone edits; an off-by-one there silently looks up the wrong binding. Cover
  with an owned-alias-into-inconsistent-aggregate test (the existing
  `owned_alias_*` tests exercise `checkedAssignPointer`'s name use).
- `joinOrigins` already stores `[]Origin`; with the struct type those copies are
  by value — confirm no map-key or equality assumptions on the old string type
  leak through (e.g. anything using an `Origin` as a map key still works; struct
  keys are comparable, so they do).
- Keep `Origin` comparable (no slices/maps in the struct) so it stays a valid
  map key and `==` target.

## 10. Review outcomes — decisions and still-open questions

**Decided:**
- **(G), not eager creation** (§4.1). Keep lazy origin creation; name the
  operations.
- **Shared gen counter** lives as a *pointer* field on `State` (clones copy the
  pointer → one shared sequence; single-threaded → plain increment). Must be
  threaded through **every** `NewState()` call site, including `Clone()`'s `nil`
  branch (state.go:81) and `Merge`'s `out := NewState()` (state.go:136) — an
  orphaned `NewState()` would mint from a fresh sequence and collide.

**Still open — settle before implementing the relevant step:**
- **Representation: `{Name, Gen}` vs opaque handle.** `{Name, Gen}` is less
  plumbing at the `string(origin)` sites, but the join-origin key (state.go
  `newJoinOrigin`, content-addressed and *deduped* — "same pair reuses one
  origin") must then fold in the members' *gens*, not just names, or two joins
  over different generations of the same names collide. The alternative — `type
  Origin uint32` opaque handle + a side table `handle→{kind, validity, name,
  joinMembers}` — makes identity structurally never-reused and handles joins
  natively (member-set→handle dedup), at the cost of a `NameOf` indirection at
  the display sites. Leaning opaque handle as "more correct." **Decide explicitly
  and verify the join dedup still collapses with generationed members.**
- **Field-level identity — RESOLVED (binding-level generations suffice).** The
  feared revival twin (`&s.f` borrow → consume → re-init `s` → read the old
  borrow) **does not exist for borrows**: a field *pointer* borrow (`&s.f`) is
  tracked via the **struct's origin** (`Origin("s")`), not a separate revivable
  `fieldPointers` entry — so dispose invalidates it and re-init does not revive
  it (probed; guards `cov_field_ptr_use_after_dispose_err` and
  `cov_field_ptr_no_revival_err` are green). Once `Origin("s")` is generationed
  (never reused), field borrows ride the same generation and stay dead across a
  re-init. `fieldPointers` holds *forward facts* (what `s.f` points at), not
  revivable borrows. So **binding-level generations are sufficient to lift the
  rebind soundly**; the field-level work is *not* required.
  - *Separate bug found while probing (orthogonal to generations):* owned-field
    **value** borrows (`b i64 := s.h`, `h` owned) are not tracked at all, so
    use-after-dispose is silently missed — driver `cov_owned_field_borrow_use_after_dispose_err`.
    A tracking gap (the binding-level `b i64 := fd` IS tracked), not an
    identity-reuse issue; fix independently.
- **Scope — settled smaller.** With the field-level question resolved, the
  minimal-viable path (steps 1–2: G + opaque-handle + counter) *also lifts the
  rejection* — no separate field-level phase needed. The remaining scope is the
  representation rework + the §5 site audit, which is the "medium refactor" we
  estimated.

**Blocking prerequisite — CLEARED.** The `y := x` struct value-copy aliasing bug
(and the array-of-struct/deref family) were fixed this session (`450a36c` and
the surrounding commits). Generations is unblocked.
