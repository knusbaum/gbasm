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

The four mint sites draw a fresh `Gen` each call:
`NewObject`, `NewLocalOrigin`, `NewAllocatedOrigin`, `NewBorrowedOrigin`
(flow/state.go ~468–525).

## 4. Site audit (the actual work)

Two assumptions are baked in today: **(a) an origin can be reconstructed from a
name** (`flow.Origin(name)`), and **(b) `string(origin)` is the binding name**
(used both for messages and for name-keyed lookups). Generations break both, so
each site below must move to "use the binding's current link" or "use
`origin.Name`."

### 4a. Reconstruct-identity-from-name → use the link
- `compile.go:1990` — `ptr.Origin == flow.Origin(path.Root)` (the self-origin
  test in `invalidateOwnedFieldFactsForMutableTarget`). → compare against the
  binding's current origin: `ptr.Origin == pf.Pointer(Binding(path.Root)).Origin`.
  Add a helper `currentOriginOf(c, name)` and use it here.
- `checker.go:275` — `InvalidateLocalOriginsForScope` invalidates
  `flow.Origin(name)` for each value local at scope exit. → invalidate the
  binding's current link origin instead (a re-inited binding's live gen must be
  the one that dies at scope exit).
- `checker.go:321` — the `MoveConsume` fallback `InvalidateOrigin(flow.Origin(name))`
  (when the binding has no `KnownOrigin` link). With generations an owned
  binding always has a link, so this fallback should become a no-op or an
  assert; verify nothing reaches it.
- `flow/state.go:469/508/514/525` — these are *inside* the `New*` minters; they
  become the fresh-gen allocation, not name reconstruction.
- `compile.go:1848` — `merged.Origin == ap.Origin` compares two origin *values*;
  **unaffected** (still a valid identity comparison once Origin is a struct).

### 4b. `string(origin)`-as-name → use `origin.Name`
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

## 5. Merge

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

## 6. What it unlocks / removes

- Delete `ownedAggregateRebindSource` and the directed rejection; owned
  aggregate assignment-rebind (`s = k`) works via a fresh identity, no revival.
- Re-init becomes uniform across {scalar, aggregate} × {`:=`, `=`}.
- `assertOwnerAdoptsLiveOrigin` and `checkReadable` are unchanged and still
  valid (they assert/consult identity liveness, which generations only make more
  precise).

## 7. Migration (each step ends green)

1. **Representation.** Introduce the `Origin` struct + shared counter; convert
   the four `New*` minters to fresh-gen. Fix every site in §4. Keep the
   aggregate rejection in place. The suite should stay green: currently-accepted
   programs are unaffected (names were effectively unique-per-live-lifetime for
   accepted code), and the rejection still blocks the aggregate case.
2. **Lift the restriction.** Remove `ownedAggregateRebindSource` + its rejection;
   let aggregate assignment-rebind mint a fresh identity like decl-init does.
   Convert `owned_struct_assign_rebind_err_test` → a positive run-test.
3. **Revival regression tests** (the point of the whole change): for scalar AND
   aggregate, decl AND assign — borrow a binding, dispose/consume it, re-init it,
   then read the *old* borrow → must stay REJECTED. Plus the merge-divergence
   test from §5. The existing D/E dispose-time tests must still pass.

## 8. Risks / watch-items

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
