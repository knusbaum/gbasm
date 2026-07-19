# Proposal: payload-carrying `values` (ADTs) + a variant `switch`

## History

- 2026-07-19 — Filed under `docs/implemented/` in the docs reorganization; the feature had already shipped. Earlier history predates this log.

Status: design. No code yet.

This proposal adds **payload-carrying variants** to the existing `values` type
and a **variant-dispatch mode of the existing `switch`** — an exhaustive switch
over an ADT's variants. Together they give Boson tagged unions / sum types
(ADTs), integrated with the ownership system.

Two realizations shape everything:

1. **A payload variant is literally a struct, and the tag tells you which
   struct you have.** So the type is a tag + a union of named structs; field
   access, mutation, and ownership reuse the struct-field, value-field-move,
   and `dispose` machinery unchanged.
2. **Dispatch is not a new construct — it's a second mode of `switch`.** The
   existing `switch (v x.(type))` dispatches on an `any`'s dynamic type and
   narrows in each `case`. A variant switch `switch e { case FILE_INFO { … } }`
   does the same thing on an ADT's tag. One construct: *read a discriminant,
   branch, narrow the scrutinee in each branch.*

The only genuinely new machinery is the tag/union representation, the
variant-dispatch mode of `switch` (bare scrutinee, variant cases,
exhaustiveness, tag-read codegen), per-variant narrowing, and one new soundness
trigger — **a tag change invalidates everything derived from the old shape**
(§7).

---

## 1. Framing and locked decisions

Boson already has a closed tagged-set type, `values` (DESIGN.md §Values types),
in two forms — tag-only (`color`) and constant-projection-bearing
(`io_error (i64, byte[])`). It also already has `switch` — but only the `any`
**type-switch** (`switch (v x.(type)) { case T { … } }`, lowered by
`compileTypeSwitch`); there is no switch over `values` today. This proposal adds
payload variants *and* a variant-dispatch mode of `switch`.

Decisions settled in discussion:

1. **Payload variants extend `values`** — no new keyword.
2. **A variant is a struct**, declared with **brace/struct syntax**
   (`FILE_INFO{ fd i64, path byte[] }`), constructed with **struct-literal
   syntax**, accessed with **struct field syntax** (`e.fd`). The tag selects
   which struct.
3. **Dispatch reuses `switch`** — a new *variant mode*, not a new keyword. Cases
   are `case VARIANT { … }`. The `switch` **narrows the scrutinee**; it does not
   bind or move payloads into per-field bindings. Fields are reached through the
   (narrowed) scrutinee. There are **no field binders** and **no `owned`
   binder**.
4. **`switch` is (or becomes) an expression** in the variant mode — the case
   bodies already parse as expressions.
5. **Scrutinee** is any expression yielding an ADT; field access needs a
   **path** (§5). A temporary is head-bound (`switch x := expr`); a path
   scrutinee (`switch c.state`) narrows in place like `?T`.
6. **Monomorphic v1** — concrete payload types only; no generics.
7. **Ownership and mutation reuse existing machinery** — a `switch` never
   consumes the scrutinee; consuming/moving/mutating payloads is ordinary field
   access on a narrowed value (§6).

---

## 2. A variant is a struct

```
type file_info values {
    FILE_INFO{ fd i64, path byte[] }
    SOCKET{ fd i64, addr byte[] }
    CLOSED
}
```

Each brace-bearing variant declares a struct shape; a bare variant (`CLOSED`)
carries no payload. The field-declaration grammar inside `{ }` is exactly the
struct grammar and reuses its parser and AST. Field names are **load-bearing**:
they are the access path (`e.fd`), so payloads must be named — there is no
positional/tuple form (Boson has no tuple indexing, so `e.0` cannot exist).

This is orthogonal to the existing **constant projection** form: projections
are compile-time-constant lookup tables indexed by tag; payloads are per-variant
runtime structs. Whether one `values` type may carry both is deferred (§9); v1
targets payload variants.

### Cross-package

Payload layouts export through the existing `values` `.bo` directive, extended
with each variant's struct shape (reconstructed on import exactly as
`StructShape` is). Leaf type names inside payloads are qualified at import time
like struct field types.

---

## 3. Representation

A payload-bearing `values` value is laid out flat as **{ tag : i64, payload }**,
where the payload region is sized and aligned to the **largest** variant's
struct:

- `sizeof(V) = 8 + max over variants of sizeof(struct_v)` (with alignment).
- **Memory-backed** (threads through `nameIsAddress` / `typeIsMemoryBacked`),
  like any struct / >8-byte value.
- No boxing, no hidden allocation.
- The tag stays private (DESIGN's "no exposed raw tag"); it is reachable only
  as control flow through a `switch`, never as a value or a raw offset.
- Tag-only / projection-only `values` keep their current i64-tag representation
  (no payload region).

### Recursion needs indirection

A variant payload that (transitively) contains its own `values` type by value
would be infinitely sized. Recursive ADTs must go through a pointer:

```
type tree values {
    LEAF{ v i64 }
    NODE{ left *tree, right *tree }   // pointers, not `tree` by value
}
```

The compiler rejects an infinitely-sized value with a directed error (naming the
cycle), the same way a self-containing struct is rejected today.

---

## 4. Declaration & construction

Declaration is §2. Construction reuses **struct-literal** syntax on the
selector:

```
e file_info := file_info.FILE_INFO{ fd: 3, path: "config.bos" }
c file_info := file_info.CLOSED                       // payload-less: bare selector
```

Cross-package: `pkg.file_info.FILE_INFO{ ... }`. The existing selector resolver
(`resolve.go`) handles the qualified path; the `{ ... }` is parsed by the struct
literal path with the variant's shape as the destination type.

---

## 5. `switch` — the variant mode

```
switch e {                        // e is a binding; narrowed per case
    case FILE_INFO { puts(e.path) }
    case SOCKET    { puti(e.fd) }
    case CLOSED    { puts("closed") }
}
```

### Two dispatch modes of one construct

`switch` reads a discriminant, branches, and narrows the scrutinee in each
`case`. It has two modes, chosen by the head shape:

| | **type-switch** (existing) | **variant-switch** (new) |
|---|---|---|
| head | `switch (v x.(type))` | `switch e` / `switch x := e` / `switch c.state` |
| discriminant | dynamic type of an `any` | tag of a `values` value |
| case | `case i64` / `case *mut T` | `case FILE_INFO` |
| narrows to | the case's type (use `v`) | the variant (use `e.field`) |
| universe | open → **not** exhaustive, `default` optional | closed → **exhaustive**, `_`/`default` optional |

The parser branches on the presence of `.(type)` in the head. Everything else —
`case`/`default` grammar, per-case narrowing, expression-capable case bodies —
is shared.

### Cases, scrutinee, exhaustiveness

- **Cases** are `case VARIANT { … }` (block form, as the type-switch already
  uses); `default` / `_` is the catch-all. Inside a case, the scrutinee is
  **narrowed** to that variant, making its struct fields accessible (`e.fd`) at
  known offsets.
- **Scrutinee** may be **any expression yielding an ADT**, but field access and
  narrowing require a **path**, exactly as `?T` narrowing does:
  - A **path scrutinee** — a variable (`switch e`), field path
    (`switch c.state`), deref, or index (the same stable lvalues `?T` narrows) —
    is narrowed *in place*; payload fields are reached back through the same path
    (`c.state.fd`).
  - A **non-path temporary** (a call result) has no path, so it is **head-bound**
    (`switch x := expr`) to acquire one (`x`), or matched bare for pure
    tag-dispatch with no field access.
  - An **owned** temporary *must* be head-bound (it needs a name to be
    discharged); a bare owned temporary would leak and is rejected.
- **Exhaustiveness** is enforced in the variant mode: every variant must be
  covered, or a `_`/`default` present. It follows from the *closed* variant set;
  the type-switch stays non-exhaustive because its type universe is open. A `_`
  arm silently absorbs variants added to the type **later**, where explicit
  cases would newly error and force handling — accept `_` as the opt-out of
  evolution-safety; prefer explicit cases where that matters.
- **Field access requires single-variant narrowing.** In a `default`/`_` case,
  or (if or-patterns are ever added) a multi-variant case, the shape is not
  pinned, so `e.fd` is rejected. Field names may collide across variants
  (`FILE_INFO{ fd i64 }`, `SOCKET{ fd byte[] }`); narrowing disambiguates the
  offset **and type**.

### Head-bind scope & owned discharge

A head-bind `switch x := expr` scopes `x` to the switch (Go-like, as
`if y := f(); …`). Because an owned `x` cannot outlive the switch, **every case
must discharge it** (dispose, or move out its owned payload) — the destructor
pattern, working the same in statement and expression position. To discharge
once instead of per-case, use an outer binding:
`x := makeOwned(); switch x { … }; dispose(x)`.

### Expression form

In the variant mode `switch` yields a value; every case's tail must have the
same type. (No parser rewrite: `switch` already parses inside `parseParens` and
its case bodies through `parseExpression`; codegen lowers into a `dest spot`.
New obligation: same-type-across-cases + exhaustiveness.)

```
msg := switch e {
    case FILE_INFO { e.path }
    case SOCKET    { "socket" }
    default        { "other" }
}
```

---

## 6. Mutation & ownership — all reuse, no new knobs

Because `switch` narrows rather than binds, ownership and mutation are **not**
switch features — they are ordinary operations on a narrowed value:

- **Read:** `e.fd`.
- **Write:** `e.fd = x` when `e` is a mutable lvalue. Non-owned field → free
  overwrite; **owned** field → consume-then-reinit (a live owned field can't be
  overwritten; move/dispose the old one first). Exactly the value-typed-owned-
  field behavior already shipped (`PROPOSAL_owned_field_move.md`).
- **Move a payload out:** `f owned FD := e.fd` — a narrowed field-move riding
  the value-field-move machinery. `owned` lives on the destination binding.
- **Pointer into a payload:** `&e.fd` — address of the narrowed path, `*mut i64`
  (or `*i64` if `e` immutable) by the existing `&` rule. This *is* the "pointer
  binder": `&` applied to a field path.
- **Consume the whole value:** `dispose(e)`. A `switch` never consumes the
  scrutinee by matching it; an owned `e` survives and is discharged by
  `dispose(e)` (or by moving out its owned payload and finishing with
  `dispose(e)`).

So there are no arm-level/field-level `owned` knobs, no 2×2 table. The only
switch-specific ownership rule is a branch-merge rule (§7 H11): if an owned `e`
is consumed inside *any* case, every case must consume it.

Destructor pattern:

```
switch e {                              // e owned file_info
    case FILE_INFO { dispose_fd(e.fd) }
    case SOCKET    { dispose_fd(e.fd) }
    case CLOSED    { }
}
dispose(e)                              // discharge e's whole-binding obligation
```

---

## 7. Soundness: the tag is mutable shape

This is the section that matters. A struct's shape is fixed for its whole
lifetime, so `&s.f` is valid as long as `s` is alive. **An ADT's shape is
determined by its tag, and the tag can change.** So `&e.fd` is valid only while
`e` is alive *and its tag stays FILE_INFO*. Every hazard below descends from
that one extra condition — **tag stability** — and none arise for structs.

### The unifying principle

Narrowing (`e has known tag T`) is a **flow-fact**, in the same family as
nullability narrowing (`?T` / `if x?`). It:

- **(a)** enables field access at variant `T`'s offsets;
- **(b)** is **invalidated by any operation that can change `e`'s tag** —
  reassigning `e`, writing a new value through an alias of `e`, or consuming `e`;
- **(c)** when invalidated, must also **invalidate every pointer/borrow and
  field-move fact derived from the narrowed fields**, because they refer to
  storage whose type and existence just changed.

Most of this composes from existing machinery: narrowing ≈ nullability
narrowing; consumption-merge ≈ the owned-binding branch-merge rule; field-pointer
invalidation ≈ the existing alias-write invalidation; loss of narrowing when a
mutable borrow of `e` escapes a call (H19) ≈ how struct-state facts are already
dropped; the owned-reassign rule (H18) is the existing assign-to-live-owned rule
unchanged. The **one genuinely new trigger** is narrowly *(b)+(c) within lexical
scope: a tag change invalidates all field-derived facts of `e`.* That trigger —
and only that — is new work; it is the highest-risk item to design and test.

### Hazard / test ledger

Each row is an invariant to preserve and a test to write (invariant-framed, per
`COVERAGE_MATRIX.md`). Expected to grow — add rows as we find more.

| # | Scenario | Required rule | Test (planned) |
|---|---|---|---|
| **H1** | `x := &e.fd` inside a FILE_INFO case | `x` borrows `e.fd`, valid only while `e` alive **and** tag == FILE_INFO | `switch_field_borrow_tagdep_test` |
| **H2** | `e = file_info.CLOSED` inside a FILE_INFO case | assignment **re-narrows** `e` to CLOSED from that point; subsequent `e.fd` rejected | `switch_reassign_renarrows_err_test` |
| **H3** | `x := &e.fd; e = CLOSED; use(x)` | tag change **invalidates** `x` (storage retyped); use rejected | `switch_reassign_invalidates_fieldptr_err_test` |
| **H4** | `e = file_info.FILE_INFO{…}` (same-variant *literal*) | narrowing/`&e.fd` survive **only** when the RHS is a statically-known same-tag literal; `e = f()`, unknown tag, or a different-variant literal un-narrow and invalidate | `switch_same_variant_reassign_test` (+ `_err` for unknown RHS) |
| **H5** | `p := &e; *p = file_info.CLOSED` | tag change **through an alias** un-narrows `e` and invalidates its field pointers | `switch_tag_change_via_alias_err_test` |
| **H6** | `p : *mut file_info; p.fd` (no switch) | a whole-ADT pointer carries **no** narrowing; `p.fd` illegal until `switch p` | `switch_ptr_no_field_access_err_test` |
| **H7** | `default` / multi-variant case accesses `e.fd` | field access requires **single-variant** narrowing; rejected otherwise | `switch_default_no_field_access_err_test` |
| **H8** | field-name collision `FILE_INFO{fd i64}` / `SOCKET{fd byte[]}` | `e.fd` resolves per the narrowed variant to the correct offset **and type** | `switch_field_name_collision_test` |
| **H9** | `for { switch e { case FILE_INFO { e = CLOSED } } }` | variant-narrowing facts must not leak unsoundly across the loop back-edge | `switch_loop_backedge_tag_err_test` |
| **H10** | `x = &e.fd` in a case, used after the switch | borrow's tag-dependency persists past the switch; fires if `e` is later reassigned | `switch_escaping_borrow_tagdep_test` (+ `_err`) |
| **H11** | one case does `dispose(e)` (owned `e`), another doesn't | **all-or-nothing**: every case discharges an owned `e`, or none (then `dispose(e)` after) | `switch_owned_consume_all_or_nothing_err_test` |
| **H12** | `dispose(e)` / full move-out inside a case, then `e.fd` | consuming `e` invalidates narrowing + field access + field pointers | `switch_consume_then_field_access_err_test` |
| **H13** | `f owned FD := e.fd` (owned payload move) | value-field-move on tag-dependent storage: `e` partially-consumed **and** still tag-FILE_INFO coexist; re-read of `e.fd` rejected until reinit | `switch_owned_field_move_test` (+ use-after-move `_err`) |
| **H14** | missing variant / duplicate case / `_` coverage | exhaustiveness enforced; duplicate case rejected; `_` covers the rest | `switch_exhaustive_err_test`, `switch_default_test`, `switch_dup_case_err_test` |
| **H15** | `switch x := get_info() { … x.fd … }` | head-bind names a temporary; exhaustive over its type; owned temporary discharged by every case (§5) | `switch_headbind_test` (+ owned-temporary `_err`) |
| **H16** | recursive `tree{ NODE{ left *tree } }` vs by-value | by-value self-containment rejected (infinite size); pointer form accepted | `switch_recursive_ptr_test`, `switch_recursive_byvalue_err_test` |
| **H17** | expression-form cases with mismatched types | same-type-across-cases enforced | `switch_expr_case_type_mismatch_err_test` |
| **H18** | owned `e`, reassign while live | **Not a new rule** — an owned value can never be reassigned while live (consume/dispose first, then reinit), same as any owned scalar/struct. The "consume ⇒ may reassign" permission is an *iff*. Test only confirms tag reasoning ("e is CLOSED now, nothing owed") doesn't defeat it. | `switch_owned_reassign_live_leak_err_test`, `switch_owned_reassign_after_discharge_test` |
| **H19** | `switch e { case FILE_INFO { mutate(); use(e.fd) } }` | **Not a new rule** — flow facts about `e` are lost exactly when `e` is reachable by the callee through a **mutable** borrow that leaves lexical scope, as with struct-state facts. Immutable references / value copies keep narrowing; a mutable arg or a mutable global alias drops it. | `switch_narrowing_survives_immut_call_test`, `switch_narrowing_dropped_mut_escape_err_test`, `switch_narrowing_dropped_global_err_test` |

### Design obligations these impose on the checker

- A **variant-narrowing fact** per binding (or flow-path — §5), set on a case
  entry and on any assignment to the binding: re-narrow only when the RHS is a
  statically-known variant literal, else *un-narrow* (H4).
- **Tag-change invalidation** that, on reassignment of `e` (or a write through an
  alias) to a *different* variant, invalidates `e`'s narrowing and every
  field-derived pointer/borrow/field-move fact. Only a same-tag variant *literal*
  preserves them (H4).
- **Owned reassign** (H18) is *not* new: the existing assign-to-live-owned rule
  already covers it; just verify tag reasoning doesn't defeat it.
- **Narrowing lost on mutable escape** (H19) is *not* new: reuse the existing
  mutable-escape invalidation; immutable references and value copies keep it.
- Field access **gated on single-variant narrowing**, carrying the field's
  **type** for that variant, not just an offset (H8).
- The existing **owned branch-merge** rule extended to variant-narrowing facts
  (H9, H11) and to the coexistence of partial-consume + narrowing facts (H13).

---

## 8. Relationship to `?T` and the type-switch

- **The `any` type-switch is the *other mode of this same construct*** — not a
  separate thing. It dispatches on dynamic type (open universe, non-exhaustive,
  no-op on miss); the variant mode dispatches on a tag (closed, exhaustive).
  Shared: `switch`/`case`/`default`, per-case narrowing.
- **`?T` ≈ `Option<T>`** — a 2-case sum with a compact null-pointer
  representation and dedicated narrowing. Kept **separate** in v1; note the
  kinship (variant narrowing is modeled on nullability narrowing). Do not
  refactor `?T` into sugar over `switch`.

---

## 9. Scope boundaries (v1)

- **No generics.** Payloads are concrete types. Real ceiling (no reusable
  `Result<T,E>`), but ADTs are independently useful and don't block a later
  generics feature.
- **Payload + constant-projection coexistence on one type** — deferred; the
  mechanisms are orthogonal, tests start payload-only.
- **Or-patterns / multi-variant cases** — out of v1 (they interact with H7 field
  access); `default`/`_` is the only multi-variant case, with no field access.
- **Guards** (`case FILE_INFO if cond`) — out of v1; natural extension.
- **Narrowing beyond a switch** (narrowing a `values` binding across ordinary
  statements the way `?T` narrows) — out of v1; `switch` is the narrowing site.

---

## 10. Implementation layering

Standard order (CLAUDE.md §Editing rules of thumb):

1. **Lexer** — no new keyword (`switch`/`case`/`default` exist).
2. **Parser** — extend the `tok_switch` path (currently `parseTypeSwitch`,
   parser.go:948) to branch on the head: `.(type)` → type-switch (unchanged);
   else → **variant-switch** (bare scrutinee / `switch x := e` head-bind /
   path). Variant *declaration* reuses struct field grammar inside the `values`
   body (`VARIANT{ fields }`); construction reuses struct-literal parsing.
3. **AST** — a variant-switch node + case nodes (parallel to
   `n_typeswitch`/`n_typecase`), or a mode flag on the existing ones; extend
   `ValuesDecl`/case nodes with per-variant struct shapes. `ToAST` owns
   exhaustiveness structure, same-type-across-cases, and the infinite-size check.
4. **Checker / flow** — **the bulk of the work and the risk.** Variant-narrowing
   fact (modeled on nullability narrowing); tag-change invalidation of
   field-derived facts (§7); field access gated on single-variant narrowing;
   owned branch-merge extended to narrowing; the H-ledger.
5. **Codegen** — tag + union layout; branch-on-tag (compare/jump table);
   field access as struct offset within the narrowed variant (payload base = tag
   size); case-tail `Into(dest)`; join label. Parallel to `compileTypeSwitch`
   (compile.go:7377) but reading the tag field instead of a typedesc word.
6. **bas** — extend the `values` directive with per-variant struct shapes;
   `bdump` prints them.
7. **objrep** — `.bo` serialize/deserialize of per-variant payload layouts
   (parallel to `StructShape`); cross-package format bump (whole runtime rebuilt
   each `mmk`, so no on-disk compat window).

---

## 11. Holes to resolve before implementation

**HOLE-1 — RESOLVED by the head-bind scope rule (§5).** An owned head-bound `x`
is scoped to the switch, so every case must discharge it; this covers statement
and expression position uniformly. (Recorded here as closed for traceability.)

*(No open holes at present. New ones get numbered here.)*

## 12. Open questions

1. **Head-bind mutability spelling** — `switch var x := expr` for a mutable
   scrutinee, consistent with `:=`/`var`? (Lean: yes.)
2. **Common-field access across variants** — if several narrowed variants share a
   field at a common offset *and type*, ever allow `e.fd` without pinning one
   variant? (Lean: no in v1; requires layout guarantees. Keep H7 strict.)
3. **Payload + projection coexistence** (§9).
4. **`switch` on a `*T` scrutinee** — `switch p` where `p : *mut file_info`:
   cases narrow the pointee; `p.fd = 5` auto-derefs. Confirm the auto-deref path
   is the one-level rule field access already uses.

*Resolved:* keyword reuse (variant mode of `switch`, not a new `match`); scrutinee
shape (§5); head-bind scope & owned discharge (§5).

---

## 13. Test plan

The §7 hazard ledger is the core of the test set — one test per row, plus its
`_err` complement where noted. Around it:

- **Construction / representation:** each variant round-trips; layout is
  max-payload + aligned; tag stays private; recursive-via-pointer works.
- **Switch dispatch:** each case reached on the right tag; `default` fallback;
  cross-package `values`; both switch modes coexist (type-switch unaffected).
- **Expression form:** value position; same-type-across-cases enforced (H17).
- **Mutation:** `e.fd = x` write-through on a mutable scrutinee; non-owned free
  overwrite; owned consume-then-reinit; `&e.fd` pointer.
- **Ownership:** owned payload move-out (H13); destructor pattern (all cases
  discharge + `dispose(e)`); all-or-nothing (H11); owned head-bind temporary.

Build the checker in lockstep with the ledger: no row of §7 lands without its
red-then-green test, and the full suite stays green per batch (the coverage-
model gate).

---

## 14. Tour

The tour lesson plan lives in [`PROPOSAL_boson_tour.md`](PROPOSAL_boson_tour.md)
— its **Sum Types and Dispatch** section (items 28–34) now scopes the `any`
type-switch, `values` declaration/projections/comparisons, payload variants
(this proposal), and the variant `switch`. None of those lessons are built yet
(the tour dir stops at `05-abstraction/07-imports`), so shipping this feature
means authoring items 33–34 (payload variants + variant `switch`) alongside the
still-unbuilt `values`/type-switch lessons that precede them. When they land,
keep the all-lessons verifier (`tour/verify_test.go`, gated by `mmk go_test`)
green — every lesson's `main.bos` must compile, link, and run.
