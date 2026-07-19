# PROPOSAL: Boson → C Transpiler Backend

## History

- 2026-07-19 — **Reframed to readability-first.** The primary goal is idiomatic,
  human-readable C (a Boson module that drops into a real C project undetected),
  not a "portable optimizing backend that happens to emit C." This supersedes the
  earlier "emit well-formed C / never invoke UB" framing: the output is real C
  with C's semantics, so `+` maps to `+` and signed-overflow UB is handled
  case-by-case as a human C author would (unsigned wrap is still C-defined). Set
  natural naming (no mangling), the public/private header split with escape
  analysis, and the interface-representation open decision. Phase 1a landed
  (functions/i64/calls/arithmetic; branch `c-transpiler`).
- 2026-07-19 — Integer-overflow semantics settled at the *language* level
  (**wraps**, both signs; DESIGN.md §Expressions). The native backend honors it;
  the C backend's fidelity is discussed below.
- 2026-07-19 — (superseded) Revised UB handling toward well-formed C via helpers.
  The readability reframe above replaces this for the C backend.
- 2026-07-19 — Proposed. Grounded in the codegen fork point, the runtime
  representations, and the check-segregation audit
  ([`../reference/AUDIT_codegen_check_segregation.md`](../reference/AUDIT_codegen_check_segregation.md)).

---

## Summary

Add a second code-generation backend that emits **idiomatic, human-readable C**.
The output must look hand-written — natural type names, natural identifiers,
natural operators, the source's own structure — so a Boson module can be
transpiled and dropped into an existing C project with nobody the wiser. The
consumer of the C is a **human**, not just a compiler.

The backend is a *sibling* of `compile.go`: it forks from the same typed AST
(after `ToAST` + the checker) and re-prints it as structured C rather than
lowering to `.bs`. But its target audience is a C *reader*. Boson earns its keep
at **authoring time** — enforcing ownership, mutability, bounds, and nullability,
and generating headers and vtables — and hands back clean C. The existing
`.bs`/`bas`/`bld` path is unchanged and remains the semantics-exact,
self-contained toolchain.

> If we only wanted an optimizer, we would target LLVM IR. **Readability is the
> entire point** — it turns Boson from a toy into a tool for writing safe C.

---

## Motivation

The driving use case: **write a module in Boson, transpile to C, maintain it as
C.** You get Boson's invariant enforcement and codegen (vtables, headers) while
producing code your C project's other authors read and edit as ordinary C.

Secondary benefits that come along for free: portability (any target with a C
compiler), the C optimizer, FFI/interop, and a second-opinion correctness check
(the same program compiled two independent ways; UBSan/ASan on the output).

---

## Premises

1. **Human-readable output is the primary goal.** Idiomatic C: `<stdint.h>` type
   names, natural identifiers, operators inline at the use site, control flow and
   structure preserved. No manufactured helper functions for things C spells with
   an operator; no name mangling.
2. **High-level structured sibling of `compile.go`.** Forks from the typed AST;
   never a modification of the `.bs` lowering (which flattens control flow and
   allocates registers — the opposite of what a C reader wants).
3. **Two backends, two fidelity contracts.** The native `.bs` backend is
   semantics-exact and self-contained. The C backend prioritizes *source*
   fidelity (see below); its runtime semantics are C's.
4. **Safety is enforced at authoring time.** The front end already accepted the
   program. The C output carries the *checks a human writes* (explicit bounds/nil
   guards) and `const`/`NULL` where natural, but does not smuggle exotic runtime
   semantics into the code to re-prove what the checker proved.
5. **Explicit drops → passthrough.** No RAII; `dispose` emits nothing, `free(p)`
   is the author's own call. The emitter computes no destructor points.

---

## The fidelity contract (and the overflow resolution)

The C backend chooses **source fidelity over semantic fidelity**: the output is
real C, and where Boson's semantics and idiomatic C diverge, idiomatic C wins and
the divergence is documented. Concretely:

- **`+` maps to `+`.** Signed-overflow is UB in C; the emitted C carries C's
  rules, exactly like every hand-written C module. **Unsigned** overflow *is*
  C-defined (modular), so Boson's wrap is preserved there. Boson's defined
  *signed* wrap is a **language** guarantee honored by the native backend; an
  author who needs portable wrapping in C uses an unsigned type — which is what
  careful C already does. We do not emit `bo_add_i64(x,y)` helpers and we do not
  require host flags like `-fwrapv` (either would betray the drop-in goal).
- **C's other corners** (oversized shifts, div-by-zero, aliasing on type-punning)
  are handled the same way: emit what a competent C author writes, and address a
  specific corner *only if it actually bites in practice*, by updating the
  emitter for that case. We do not preemptively contort the output to dodge UB
  the source will almost never hit.
- A **UBSan build of the output** remains available as a diagnostic when chasing a
  real miscompile, but it is a tool, not a gate.

This is the deliberate consequence of a *human* target. It reverses the earlier
"never invoke UB" stance, which belonged to the self-contained-optimizer framing.

---

## Representation (natural C)

| Boson | C | notes |
|---|---|---|
| `i8..i64` / `byte` / `u8..u64` | `int8_t..int64_t` / `uint8_t..uint64_t` | `<stdint.h>` names |
| `bool` | `bool` | `<stdbool.h>` |
| `*T`, `?*T` | `T *` | nullable is a pointer that may be `NULL`; the checker proved non-null derefs |
| `mut` / non-`mut` | non-`const` / `const` where natural | best-effort; C `const` is shallow |
| `[]T` (slice) | `struct { T *ptr; int64_t len; }` | readable field names; no capacity |
| `[N]T` (array) | `T[N]` (or a small struct wrapper for value semantics) | |
| `struct` | `struct` | layout is `cc`'s job |
| interface `I` | `struct { void *self; const I_vtable *vtable; }` | vtable-pointer; see open decision |
| `values` / ADT | `struct { int64_t tag; union { … } payload; }` | tagged union; `switch` reads `tag` |
| multi-return | out-struct by value, or out-params — whichever reads better | natural C, not a synthetic type |
| `owned` | *(nothing)* | compile-time only |

Bounds/nil checks emit as the `if (i >= s.len) …` guard a human writes (likely a
knob — a hand-written module may omit them in release).

---

## Naming (idiomatic C, not mangling)

No length-encoded mangling. C's flat namespace is handled the way C libraries
handle it — **prefix conventions**:

- File-local (non-`pub`) function → bare name, `static`.
- Exported (`pub`) function in `package foo` → `foo_name` (the SDL/libgit2
  convention); a `pub` method `Type.m` → `foo_type_m`.
- `package main`'s `main` → the C `main` directly (its return is the exit code).
- A Boson name colliding with a C keyword gets a trailing `_`, as a human would.

Injectivity is by convention plus **collision detection** — if two symbols would
collide in C's flat namespace, the transpiler reports it (like a linker), rather
than emitting gibberish to guarantee uniqueness.

---

## Headers and the public/private split

Transpiling `foo.bos` yields **`foo.c`** (implementation + everything private)
and **`foo.h`** (the public API: `pub` function prototypes and `pub` type
definitions). The subtlety is C's opaque-vs-transparent tension: a Boson-private
type can still be forced into the header because C callers need its *size*. Per
private type, an **escape analysis**:

- **escapes by value** into a public signature (returned/passed by value, or
  embedded by value in a public type) → its full definition is promoted to the
  header, with a comment noting it is Boson-private, exposed for ABI reasons;
- **escapes only behind a pointer** → an opaque forward declaration in the header
  (`typedef struct Foo Foo;`), full definition in the `.c` — the idiomatic C
  opaque-pointer;
- **never escapes** → stays entirely in the `.c`.

This gives maximal encapsulation within what C physically permits — exactly what a
careful C library author does by hand.

---

## Interfaces (open decision — vtable-pointer recommended)

Two representations, both idiomatic C, different scaling:

- **vtable-pointer (recommended):** `struct { void *self; const I_vtable *vtable; }`
  with one `static const I_vtable` per concrete type; call as
  `it.vtable->method(it.self, …)`. This is *the* recognizable C polymorphism idiom
  (kernel `f_op`, GObject, COM). 16-byte value, cheap to pass, scales to any
  method count. Cost: a generated static table per (type, interface) — normal,
  hand-looking C.
- **flattened:** inline function pointers per interface value
  (`struct { void *self; ret (*method1)(…); … }`), called as `it.method1(it.self, …)`.
  Marginally simpler call site, no table globals; but the value grows with method
  count and every assignment/pass copies all pointers. Idiomatic only for tiny
  1–3-method callbacks.

Recommendation: vtable-pointer as the default (idiomatic *and* efficient). Pending
final call.

---

## Architecture

- **Fork point.** `bosc -target=c` selects `emitCFile` in place of the `.bs`
  `Compile` (`cmd/bosc/main.go`); the `.bs` path is untouched. `emitc.go` is a
  syntax-directed AST walker + type mapper + a small per-function `cEmitter` that
  tracks in-scope locals (function-scope bindings are not established in the
  `Context` on this path). There is no new IR — C is the high-level IR.
- **The oracle (soundness scaffold).** Until the leaked semantic checks are
  hoisted (see the audit), a from-AST backend cannot re-derive language errors on
  its own — the flow-sensitive checks are interleaved into `compile.go`'s
  lowering. So the mature `.bs` path runs as a **gate** (compile to a discard
  writer; emit C only if it passes), giving `accepted(C) = accepted(.bs) ∩
  supported(emitC)` by construction. It is a full compile (cheap relative to
  `cc`) and runs on a re-derived AST/Context. It retires as the audit's hoist
  makes the checker a standalone pass. *(Not yet wired; phase 1 currently guards
  only via emitC's own "not supported" errors.)*
- **Build integration.** A `boson.mmk` rule transpiles each package to `.c`/`.h`
  and invokes `cc`. No special UB-defining flags; a UBSan build is available for
  diagnostics.

---

## Testing: differential against the native backend

The ~490-file `.bos` corpus is a near-free correctness oracle: build each test
through *both* backends and compare stdout / exit code against the *same*
`.expected`. Any divergence is a transpiler bug. Honestly scoped: phase-gated
(needs the runtime floor first), covers only programs both backends accept with
deterministic output, and the documented C/Boson semantic divergences (signed
overflow) are expected mismatches, not bugs — so it is a strong net, not a proof.

---

## Phased plan

1. **1a — done.** Single `main` package; `i64`; function defs/calls; `return`;
   integer arithmetic. Idiomatic output verified end-to-end through `cc`.
2. **1b — output.** Integer/string output via a minimal, itself-hand-looking C
   floor (or libc), so stdout is visible and differential testing can begin.
   Wire a `cc`-driven test harness and the oracle.
3. **Aggregates.** Structs, arrays, slices + the explicit bounds/nil guards; the
   public/private header split and escape analysis.
4. **Control flow + full expressions + casts.**
5. **Interfaces + ADTs.** The chosen interface representation; tagged-union
   `switch`; the `any` type-switch.
6. **Ownership passthrough + multi-return + cross-package** (introduces the
   package-prefix naming).
7. **Relax the oracle** as the audit's hoist lands.

---

## Open decisions

- **Interface representation** — vtable-pointer (rec) vs. flattened.
- **Runtime floor** — raw syscalls (ratified for the self-contained case) vs.
  libc; for the drop-in use case the module ultimately targets the host's libc.
- **Bounds/nil checks** — always on, or a knob (a hand-written module may omit
  them in release).
- **Formatting** — tabs vs. spaces, brace style; pick one idiomatic default.
- **Driver/build** — `bosc -target=c` (in place) and the `boson.mmk` rule shape.

---

## Risks and non-goals

- **Two-backend maintenance tax** — every feature lowers twice; mitigated by
  differential testing and the segregation hoist.
- **Semantic divergence from the native backend** (signed overflow, other C
  corners) — accepted and documented as the cost of a human target; use unsigned
  for portable wrap.
- **Non-goal:** matching the Boson ABI for linker interop, or re-proving checked
  invariants at runtime in the C. The C is real C; Boson did the proving.
- **Non-goal:** exhaustive per-AST-node emission rules in this doc — that's
  implementation.
