# Codegen Check-Segregation Audit

## History

- 2026-07-19 — Noted that the C transpiler's oracle (gate run *after* `emitC`)
  is the dynamic discovery mechanism for this worklist — every gate rejection of
  an `emitC`-emitted program is a leaked check to hoist.
- 2026-07-19 — Initial audit. Census of every user-facing error site in
  `cmd/bosc/compile.go` + `globals.go`, classified **semantic** vs **lowering**
  vs **ambiguous**, to size how much language-semantics checking has leaked into
  the code-generation stage. Motivated by scoping a Boson→C transpiler backend.
  Line-number baseline: commit `43c5697` (immediately before this session's two
  bounds-check fixes, `0cca0ac` and `84ef387`, which shifted some later lines).

---

## Why this exists

A second code-generation backend — specifically a **Boson→C transpiler** — would
fork from the *typed AST* (the output of `ToAST` + the checker), not from the
`.bs` assembly that `compile.go` emits. The whole safety story for such a backend
rests on one assumption:

> By the time codegen runs, the program has already been **fully validated** by
> the front end (parser, `ToAST`, checker). Codegen only *lowers*; it never
> discovers a new language error.

The conceptual target is clean:

- **All semantics** (type rules, name resolution, arity, ownership/flow) live in
  `ToAST` and the checker (`checker.go`/`flow/`).
- **`compile.go` only ever raises lowering concerns**: "not yet supported", a
  backend/ABI limit, or an internal invariant bug. Any *language* error out of
  codegen is a leak.

If that separation held, a from-AST C backend would be sound by construction: it
consumes an already-accepted tree. This audit measures how far the current code
is from that target — i.e. **how many language checks are only enforced inside
`.bs` lowering**, where a different backend would silently miss them.

### The oracle scaffold this feeds

Until the separation is real, a C backend can be made sound by using the mature
`.bs` path as a **validation oracle**: run `Compile`→`.bs` purely as a gate
(discard the output, never assemble), then run the C emitter only if it passed.
That buys, by construction:

```
accepted(C-target) = accepted(.bs) ∩ supported(emitC)
```

so the C path can never accept a program the main compiler rejects, even while
`emitC` is young and partial. The oracle is a scaffold, not the end state — it is
over-strict (it also inherits `.bs`-backend-specific rejections that aren't
language rules) and it does not transfer *runtime-safety* parity (a latent
unsoundness in `.bs` lowering is faithfully inherited — see the slice-bounds hole
found and fixed this session, `0cca0ac`). The end state is proper segregation:
hoist the leaked semantic checks into the front end, and the oracle becomes
vestigial. **This audit is the worklist for that hoist.**

The oracle also *discovers* this worklist dynamically. By running the gate
**after** `emitC` (which does no semantic checking of its own), any program that
`ToAST` accepted and `emitC` emitted but the gate rejects is, by construction, a
check that lives *only* in `compile.go`. Each such case is triaged: a language
semantic is hoisted into `ToAST`/the checker (so both backends inherit it and the
gate stops seeing it); a genuine `.bs`-backend limitation is left as accepted
over-strictness. The check is never duplicated into `emitC`. So the C transpiler's
gate is the ratchet that turns this static census into a steadily-shrinking
backlog. See the oracle section of
[`../proposed/PROPOSAL_c_transpiler.md`](../proposed/PROPOSAL_c_transpiler.md).

**First ratchet run (2026-07-19).** *(Measured on the `c-transpiler` branch,
where `emitC` and the gate live; the branch is tabled, but the worklist below is
about `compile.go` and stands on its own.)* Running the 313 `_err_test.bos` programs
through the C path (front end → emitC → gate): **14** caught by the front end
(`ToAST`/parse — already segregated), **27** caught only by the gate (leaked into
`compile.go` — concrete hoist candidates), **272** unclassifiable yet (emitC
feature gap, before the gate), and — crucially — **0 escaped** (every invalid
program is caught by *something*, so the C backend is sound). The 27 cluster as:
operand/assignment/init **type** checks ("operator + requires integer operands",
"Cannot initialize i64 with value of type bool", "mismatched number types"),
**literal-range** ("Integer literal 128 does not fit in type i8", decidable at
`ToAST`), **mutability** ("Cannot assign to immutable binding", ×5 tests / one
check), **discard-read**, and **ownership** ("owned binding … out of scope
without being consumed" — flow-sensitive, so the checker rather than `ToAST`).
The 27 is a lower bound: the 272 SKIPs re-classify as emitC coverage grows.

---

## Method

- **Scope:** `cmd/bosc/compile.go` and `cmd/bosc/globals.go` — everything
  reachable from `Compile` / `compileTop`. `ast.go`'s `ToAST` and `checker.go`
  are *excluded*: they are where semantics are *supposed* to live; this audit
  only counts checks that leaked *past* them into codegen.
- **Error mechanisms:** `compile.go` is **100% panic-based** — a single helper
  `CompileErrorF` (`compile.go:22`) panics an `*interpreterError`, caught by the
  top-level `recover`. There are **no** `return err` propagations. Every
  `fmt.Errorf` in `globals.go` funnels to the user through one call site
  (`globals.go:65`, `CompileErrorF(a, "%s", err.Error())`), so `globals.go`
  errors are classified by content.
- **Line numbers** are as of baseline `43c5697`. This session's bounds fixes
  added lines around the two index checks and the `SliceOp` block, so sites after
  those points now sit a few lines lower. **Anchor on the quoted message text,
  which is stable; treat line numbers as approximate.**

---

## Headline

| Bucket | Count | Meaning |
|---|---:|---|
| **SEMANTIC** | **179** | Leaked language/type/flow checks. A from-AST backend forking here accepts every one of these invalid programs. |
| **LOWERING** | 16 | Legitimately backend-owned: register/ABI limits, unimplemented feature stubs. |
| **AMBIGUOUS** | 24 | 16 are internal invariant assertions (not real user checks); 8 are genuine judgment calls. |
| total | 219 | live sites (excludes helper definitions, the recover re-panic, and commented-out panics) |

**Two structural facts that shape the hoist:**

1. **Single panic funnel.** One `CompileErrorF`; no error returns. Hoisting is
   therefore a matter of *moving the check earlier*, not rewiring error plumbing.
2. **Most of the leak is flow-sensitive.** The bulk of the 179 is the
   ownership/move/escape family, which needs flow facts. Its correct home is the
   **checker** (`checker.go`/`flow/`), **not `ToAST`**. Consequence for the C
   backend: it leaked into codegen only because this compiler *interleaves* flow
   analysis with lowering. A from-AST backend cannot be sound until the checker
   runs as a real, separate pass over the AST **before** any lowering.

---

## LOWERING (16) — genuine backend / feature-gap concerns

These legitimately belong to a backend. A different backend would have its own
version, or not need them.

| site (msg text) | why lowering |
|---|---|
| `new(T) is not implemented yet` | feature gap |
| `reassigning the matched value is not yet supported` | temporary gap (re-narrowing follow-on) |
| `Reassigning an owned aggregate … is not yet supported` | temporary gap |
| `More than 6 arguments are not supported` | SysV register-arg-slot limit |
| `passing a N-byte value by value is not supported; use a 1/2/4/8-byte layout` | ABI/register width |
| `8-bit multiplication not supported` | x86 opcode/register-form limit |
| `8-bit division not supported` | x86 opcode/register-form limit |
| `8-bit modulo not supported` | x86 opcode/register-form limit |
| `More than 5 user arguments not supported for interface methods` | register-slot limit (receiver takes one) |
| `N-byte values are not supported by the small-struct value-backed path` | lowering-path gap |
| `(TODO) Can't copy T into *T for T.size > 8 bytes` | explicit TODO + machine size |
| `Slicing for types > 8 bytes not implemented.` | not implemented + machine size |
| `NOT IMPLEMENTED (TODO)` ×2 | unimplemented stubs |
| `interface-to-interface conversion in a static initializer is not supported; assign at runtime` | static-init lowering gap (`globals.go`) |
| `value-backed interface in a static initializer is not supported; use a pointer source` | static-init lowering gap (`globals.go`) |

---

## AMBIGUOUS (24)

### Internal assertions / fallthrough guards (16)

Not a user-facing language rule *or* a lowering feature — a bug-catch that
surfaces as a crash-style compile error. Should never fire on a valid,
front-end-accepted program.

- `compiler invariant violated: owned binding adopts a non-live origin`
  (explicitly "a compiler bug, not a user error")
- `internal compiler error: don't know how to move X into Y`
- `(LVAL) FALLTHROUGH…`
- `Expected type X but compiler produced Y`
- `Somehow slicing a non-array, non-slice`
- `Return not same as dest. Should this happen?`
- `(TOP) FALLTHROUGH…`
- `ContainsFuncall Fallthrough`
- `AAAH! NULLSPOT! This should not happen.`
- `compileInterfaceEquality called without an interface operand`
- `Could not do op`
- `interface method arg compiled to nullspot`
- `internal: static initializer encoded N bytes, but type has size M` (`globals.go`)
- `address-of with no target (internal: …)` (`globals.go`)
- `element N: encoded N bytes, expected M` (`globals.go`)
- `field q: encoded N bytes, expected M` (`globals.go`)

### Genuinely either-way (8)

- `value is N bytes; value interfaces can store at most 8 bytes inline` — an
  inline-storage language rule, but stated in machine byte-size terms.
- `values type X: cannot encode projection table: …` — wraps a static-init encode
  failure during table lowering.
- `cannot call method on non-addressable expression receiver` — addressability is
  AST-derivable (semantic), yet the constraint exists because a register-resident
  value can't be spilled to take `&` (lowering).
- `initializer is not a compile-time constant` ×3 (`globals.go`) — "must be
  constant" is semantic, but the exact fold-able set is encoder capability.
- `unsupported literal type for static initializer: %T` (`globals.go`) —
  const-encoder fallthrough for an unhandled literal kind.
- `address-of-index in static init requires a named array and a compile-time-constant index`
  (`globals.go`) — const-init requirement mixed with encoder capability.

---

## SEMANTIC (179) — the leak surface

None of these mention registers, ABI slots, instruction forms, or byte-in-slot
encoding. All are permanent language/type/flow/name rules checkable against the
typed AST (plus flow facts, which the checker legitimately owns). Grouped by
theme; a from-AST C backend forking at `compileTop` would **miss every one**.

- **Ownership / move / consume** (flow-sensitive → checker): use-after-move,
  owned-drop, consume-on-one-branch, owned-field/aggregate consistency, dispose
  obligation. ~52 sites — the largest family.
- **Escape / dangling-borrow-out-of-scope** (flow-sensitive → checker):
  pointer/slice/borrow escaping local scope. ~11 sites.
- **Type / coercion / cast compatibility** (type-only → `ToAST`):
  "cannot assign/convert/cast/compare/order X to Y", interface satisfaction.
  ~38 sites.
- **Nullability** (mostly type-only): deref / field-access / compare through a
  nullable or non-pointer type. ~9 sites.
- **Name resolution / member existence** (→ `ToAST`): undeclared name; no such
  struct/field/member/method/function; duplicate field; missing required field;
  static-vs-instance method. ~18 sites.
- **Arity** (→ `ToAST`): wrong argument count for call/builtin/method/fn-pointer.
  ~8 sites.
- **Discard `_` / multi-value binding & assignment** (→ `ToAST`): invalid `_`
  discard; multi-bind/multi-assign count and shape. ~10 sites.
- **Mutability / immutability / initializer-required** (→ `ToAST`): assign to
  immutable; infer-from-nil/void; missing initializer; bare return in non-void;
  lvalue-not-writable. ~7 sites.
- **Const-bounds / static-index & switch** (→ `ToAST`): array index > size;
  address-of restrictions; not-a-variant / duplicate case / non-exhaustive switch;
  `...` variadic placement. ~9 sites.
- **`globals.go` static-init semantic checks** (→ `ToAST`; largely *duplicates*
  of the runtime literal checks): top-level-owned rejection; missing initializer;
  literal-type/size mismatch; non-array/non-pointer target; missing/duplicate/
  unknown struct field; integer-to-non-integer. ~19 sites.

---

## Top hoist candidates (highest value first)

The checks that would make a from-AST backend unsound if left in codegen, ordered
by clarity and payoff:

1. **Name resolution & member existence** — "undeclared", "no such
   struct/field/method/function". Textbook front-end checks; pure AST.
2. **Arity** — call/method/fn-pointer argument-count mismatch.
3. **Type / cast / coercion compatibility** — the whole cast-legality block and
   interface satisfaction; type-only, no flow needed.
4. **Struct-literal shape** — **duplicated**: "duplicate/unknown/missing field"
   logic exists *twice*, once in `compile.go` and again in `globals.go`'s
   static-init path. Consolidating into `ToAST` deletes the static-init copy
   outright. (Free win.)
5. **Switch validity** — non-`values` scrutinee, bad variant, duplicate case,
   non-exhaustive; decidable from the AST + the type's declared case set.
6. **Discard / multi-bind arity & shape.**

The large **ownership/move/escape** family is semantic too, but genuinely
flow-sensitive: its home is the **checker**, and hoisting it means running the
checker as a real pass over the AST before lowering (see structural fact #2).
That is the single biggest prerequisite for a sound from-AST backend.

---

## Hoist backlog seeded this session

- **Constant negative index → compile-time error.** `nums[-1]` is decidable at
  compile time but currently only *runtime*-traps, while `nums[3]` (positive
  constant OOB) is already a compile-time `CompileErrorF`. The runtime
  memory-safety hole was closed this session (`84ef387`, unsigned index compare),
  but the *compile-time* check is still missing — a clean, small hoist into
  `ToAST` that also removes the positive/negative asymmetry. Noted in DESIGN.md
  (§ codegen bounds-checking bullet).

---

## Related work this session

- `0cca0ac` — bounds-check the slice operation `s[lo:hi]` (was unchecked). A
  latent `.bs`-backend unsoundness the oracle would have laundered into the C
  path; fixed at the source.
- `84ef387` — trap negative runtime array/slice indices (signed→unsigned
  compare). Same class, indexing path.

Both are examples of the oracle's limit: it transfers *language-acceptance*
parity, not *runtime-safety* parity, so latent lowering unsoundness must be fixed
in the shared lowering (or re-derived in each backend), not assumed away.
