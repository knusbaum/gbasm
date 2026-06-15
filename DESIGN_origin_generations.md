# Design note — Origin generations

Status: planned, not started. Prereq context: the owned-scalar transfer work
(borrows survive a same-scope rebind) and the `assertOwnerAdoptsLiveOrigin` /
`checkReadable` consolidations are already in. This note plans the follow-on
that lets owned **aggregate** re-init work and closes the whole "revived
borrow" class structurally.

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

### 4.1 Eager origin creation removes get-or-create entirely (recommended)

Only **owned** value locals get an origin at declaration (`compile.go` ~3092,
gated `else if ast.Type.HasOwned()`); pointer bindings get `DeclarePointer`;
**plain non-owned value locals get nothing**. So `var x i64` has no origin until
a pointer into it is formed — `&x` (`1670`) or `&arr[i]` (`1708`) — where
`pointerExprForAST` does get-or-create (`return existing link, else
NewLocalOrigin(name)`). That lazy create is the *only* reason `pointerExprForAST`
ever mints, and it is the hazardous middle case.

**Recommendation: mint a self-origin for every value local at its declaration**
(drop the `HasOwned` gate at ~3092 — give plain locals an `OriginLocal` too).
Then by the time `&x` / `&arr[i]` runs the link always exists, the create
branches (`1670`, `1708`) are **dead**, and `pointerExprForAST` only ever
*gets*. This yields the total invariant we want: every binding's current origin
is `Pointer(binding).Origin`, always present — no "not yet created" state to
reason about, and the most dangerous sites are deleted rather than merely
documented.

The other two `New*` calls in `pointerExprForAST` are *not* get-or-create and
stay as mints: `1675` mints an anonymous `&literal` storage, `1831` mints a
synthesized escape-restricted origin for a derived alias. Both are genuinely-new
identities.

Cost / audit for eager creation:
- A few inert origins (locals never addressed) — negligible per-function.
- **One audit**: search for branches that treat origin **absence**
  (`!KnownOrigin`) as a signal ("plain untracked local, skip"). Eager-minting
  makes value-local origins always present; anything that relied on absence to
  mean "untracked" changes. This is a grep over `KnownOrigin` guards, not a
  per-site judgment. (An always-present `OriginLocal` that no pointer escapes is
  inert, so a clean audit means eager creation is behavior-preserving.)

Do eager creation as its **own green step before** the struct change: it removes
get-or-create so the generations work is left with only unambiguous *get* and
*mint*.

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
  (when the binding has no `KnownOrigin` link). With eager creation (§4.1) an
  owned binding always has a link, so this fallback becomes dead — make it a
  no-op or an assert and confirm nothing reaches it.
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
The type change does **not** flag these. After eager creation (§4.1) the
get-or-create cases (`1670`, `1708`) are gone, leaving only genuine mints, so
this list should reduce to:
- declaration / re-init self-origins (`compile.go:3092`, `3280`, `3379`) — mint.
- `NewBorrowedOrigin` for params (`1091`, `2724`, `2732`) — mint (once per param).
- `NewAllocatedOrigin` for `alloc()` (`1737`) — mint.
- `1675` (`&literal` storage), `1831` (synthesized derived alias) — mint.
The four `New*` minters (flow/state.go ~468–525) draw a fresh `Gen`; each call
above is once-per-lifetime, so a fresh gen is correct. **Verify** no remaining
call is reached more than once for the same intended identity.

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
- `pointerExprForAST` only ever *gets* — no minting in a provenance query.
- `assertOwnerAdoptsLiveOrigin` and `checkReadable` are unchanged and still
  valid (they assert/consult identity liveness, which generations only make more
  precise).

## 8. Migration (each step ends green)

1. **Eager origin creation.** Drop the `HasOwned` gate so every value local gets
   a self-origin at declaration. Audit `!KnownOrigin`-as-untracked branches.
   Behavior-preserving; removes the get-or-create sites (`1670`, `1708`). Suite
   green.
2. **Representation.** Introduce the `Origin` struct + shared counter; convert
   the four `New*` minters to fresh-gen. Fix every site in §5 (the *get* sites
   are compiler-flagged; the *mint* sites in §5c are now unambiguous). Keep the
   aggregate rejection in place — currently-accepted programs are unaffected, and
   the rejection still blocks the aggregate case. Suite green.
3. **Lift the restriction.** Remove `ownedAggregateRebindSource` + its rejection;
   let aggregate assignment-rebind mint a fresh identity like decl-init does.
   Convert `owned_struct_assign_rebind_err_test` → a positive run-test.
4. **Revival regression tests** (the point of the whole change): for scalar AND
   aggregate, decl AND assign — borrow a binding, dispose/consume it, re-init it,
   then read the *old* borrow → must stay REJECTED. Plus the merge-divergence
   test from §6. The existing D/E dispose-time tests must still pass.

## 9. Risks / watch-items

- The `string(Origin)`-as-name lookups (esp. `compile.go:862`) are the failure-
  prone edits; an off-by-one there silently looks up the wrong binding. Cover
  with an owned-alias-into-inconsistent-aggregate test (the existing
  `owned_alias_*` tests exercise `checkedAssignPointer`'s name use).
- The `!KnownOrigin`-as-untracked audit (§4.1) is the gate on eager creation
  being behavior-preserving — do it before relying on the total invariant.
- `joinOrigins` already stores `[]Origin`; with the struct type those copies are
  by value — confirm no map-key or equality assumptions on the old string type
  leak through (e.g. anything using an `Origin` as a map key still works; struct
  keys are comparable, so they do).
- Keep `Origin` comparable (no slices/maps in the struct) so it stays a valid
  map key and `==` target.
