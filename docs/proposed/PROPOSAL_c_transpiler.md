# PROPOSAL: Boson → C Transpiler Backend

## History

- 2026-07-19 — Revised UB handling: the emitter must emit **well-formed C**
  (unsigned-type wrap, `memcpy` punning, explicit shift masks and div guards;
  UBSan policing), not define UB away with flags — so `cc` stays free to optimize
  and the output stays genuinely portable. Ratified the raw-syscall floor;
  recorded the safe-C-generator future direction.
- 2026-07-19 — Proposed. Grounded in the codegen fork point, the runtime
  representations, and the check-segregation audit
  ([`../reference/AUDIT_codegen_check_segregation.md`](../reference/AUDIT_codegen_check_segregation.md)).

---

## Summary

Add a second code-generation backend that emits **C** instead of `.bs` assembly.
The C backend is a *sibling* of `compile.go`: it forks from the same typed AST
(after `ToAST` + the checker) and emits **structured, high-level C** —
preserving `if`/`for`/`while`/`switch`, typed structs, and expressions — then
hands register allocation and optimization to an off-the-shelf C compiler. The
existing `.bs`/`bas`/`bld` path is unchanged and remains the from-scratch,
self-contained, x86-64-Linux toolchain; the C path is the **portable, optimizing**
alternative, selected by a flag.

The design conceit that makes this tractable: **Boson is the safe subset of C
made explicit.** Interfaces are already vtable structs, `values` are tagged
unions, slices are fat pointers, ownership is explicit (no RAII). So the
translation is mostly a syntax-directed re-printing of an already-safe program —
the compiler *proved* safety on the Boson side; C only has to carry it forward as
faithfully as C allows (`const`, `NULL`), never to *enforce* it.

---

## Motivation

- **Portability.** Any target with a C compiler — ARM64, macOS, Windows, RISC-V,
  embedded — for near-zero incremental effort. Today the toolchain is x86-64
  Linux only.
- **Optimization.** `gcc`/`clang` -O2 vastly exceeds a single-pass compiler plus
  the `bas` register allocator (there is deliberately no optimizer in the native
  path). This is a large performance win at low cost.
- **Interop / FFI.** C output is inspectable, debuggable with standard tools, and
  a natural bridge to C libraries.
- **A second opinion.** Compiling the same program two independent ways is a
  powerful correctness check (see *Differential testing*), and C sanitizers
  (UBSan/ASan) become available as bug-finding tools for Boson programs.

---

## Settled premises

1. **High-level, structured target.** `emitC` forks from the typed AST as a
   sibling of `compile.go`, **never a modification of it**. `compile.go` lowers
   all the way to a register machine with flattened control flow; C wants the
   opposite (keep structure, let `cc` lower). Different altitude, different file.
2. **Two backends coexist.** C does not replace `.bs`. They serve different
   goals; the tax is that new language features must be lowered twice (mitigated
   by differential testing and by the segregation work below).
3. **Self-contained output.** Emit C, compile with `cc` to a native binary.
   Interop with the from-scratch ELF linker is explicitly *not* a goal — it would
   defeat the portability that motivates the whole backend.
4. **Safety is proven up-front, carried forward best-effort.** The front end
   already accepted the program. C emission adds `const` and respects `NULL`
   where it can, but these are defense-in-depth, not the safety mechanism. Bounds
   traps and nil-asserts are emitted explicitly (they are runtime checks, not
   type rules).
5. **Explicit drops → a dumb backend.** Boson has no RAII; `dispose` emits
   nothing and `free(p)` is the programmer's own call. The emitter never computes
   destructor points — it passes ownership operations through verbatim, exactly
   as the checker authorized.

---

## The representation contract

The generated code and the hand-written C runtime floor must agree **byte-for-byte**
on aggregate layout. That contract lives in a generated header, **`boson.h`**,
included by every emitted `.c` and by the floor. Core mappings:

| Boson | C | notes |
|---|---|---|
| `i8..i64` / `byte` | `int8_t..int64_t` / `uint8_t` | fixed-width `<stdint.h>` |
| `bool` | `_Bool` | |
| `*T`, `?*T` | `T*` | nullable is just a pointer that may be `NULL`; the checker already proved non-null derefs |
| `mut` / non-`mut` | non-`const` / `const` | best-effort; C `const` is shallow, so this is advisory |
| `[]T` (slice) | `boson_slice_T { T* ptr; int64_t len; }` | 16-byte fat pointer, **no capacity** (matches native: re-slicing can't grow) |
| `[N]T` (array) | `T[N]`, or a `struct { T v[N]; }` wrapper where value semantics are needed | |
| `struct` | `struct` | field offsets are `cc`'s job, not ours (self-contained) |
| interface `I` | `boson_iface { void* data; const boson_vtable_I* vt; }` | 16-byte; per-`(concrete,I)` static vtable global |
| `values` / ADT | `struct { int64_t tag; union { … } payload; }` | flat tagged union, `switch` reads word 0 |
| multi-return | by-value `struct` return | C has no multi-return; the synthetic `{_0,_1,…}` struct already exists in the AST |
| `owned` | *(nothing)* | compile-time only; zero runtime representation |
| function `pkg.f` | mangled C function (see *Name mangling*) | |

Interface **dispatch** maps directly: `iface.vt->method(iface.data, args…)`, with
per-`(concrete type, interface)` vtables emitted as `static const` globals
(`compile.go` already materializes these for `.bs`; the C emitter emits the same
tables as C initializers). ADT construction sets `tag` + the active union arm;
`switch` compares `tag`.

---

## Hard cases

Most constructs map trivially. These do not, and are the substance of the design.

### Emitting well-formed C — the emitter must never invoke UB

The `.bs` backend inherits x86's *defined* semantics for several operations that
are *undefined behavior* in C. The wrong response is to define them away with
flags (`-fwrapv`, `-fno-strict-aliasing`): that ships C which *is* UB and merely
asks one compiler to tolerate it — and it would be silently broken the day the
generated C is lifted out and compiled normally (the eventual safe-C-generator
goal). **The emitter's obligation is to generate C that is well-formed in the
first place.** Then no UB-defining flags are needed and `cc -O2`/`-O3` is free to
optimize aggressively, *because* there is no UB for the optimizer to exploit.

Each x86-defined-but-C-UB operation has a well-formed realization (compiling to
the same instructions):

- **Integer wrap** — only *signed* `+`, `-`, `*`, and unary `-` can overflow, and
  signed overflow is UB in C. (Unsigned types — `byte`, `u*` — are already
  modular-defined and need nothing; comparisons and bitwise ops can't overflow
  and stay bare.) To reproduce the native backend's wrapping without UB, route
  those ops through a tiny per-width helper in `boson.h` that computes in the
  unsigned type and converts back — generated code reads `bo_add_i64(a, b)`, and
  only the helper body holds the cast idiom (`(int64_t)((uint64_t)a +
  (uint64_t)b)`). The unsigned math is fully defined (modular); the narrowing
  back is *implementation-defined* (two's complement everywhere, mandated by
  C23) — **not UB**. Deliberately *not* `__builtin_add_overflow` (a gcc/clang
  extension; the safe-C-generator goal wants pure standard C). **Open language
  question:** whether integer overflow should *wrap* silently (current native
  behavior) or *trap* (more in keeping with Boson's safety ethos) — the
  transpiler follows the native backend either way, and only the helper body
  changes (unsigned dance vs. overflow-checked trap). What it is never is bare
  signed `a + b`.
- **Type punning** (interface inline value-backed `data`, the `any` type-switch)
  — never read bytes back through an incompatible pointer type. Move them with
  **`memcpy`**, the standard-blessed way to reinterpret representation, exempt
  from the strict-aliasing rule. (For pointer-*backed* interfaces the `data`
  pointer points at a genuine `T`, so reading it as `T*` is already legal — no
  punning at all.)
- **Shifts** — emit an explicit mask so the count is always in range:
  `x << (n & 63)` (`& 31` for 32-bit). No shift by `>=` width ever reaches the C.
- **Division / modulo** — emit an explicit guard that traps on a zero divisor
  (and on `INT64_MIN / -1`, itself both UB in C and `#DE` on x86), exactly like
  the bounds checks: `if (d == 0) bo_div_zero();`. Never rely on the hardware
  fault (UB from C's view); generate the check.

This is a **checklist the emitter must satisfy**, and it is *testable*: compiling
the generated C under `-fsanitize=undefined` (UBSan) must produce no diagnostics
across the corpus. That check — not a flag — is what enforces well-formedness.

### Name mangling — *not* a freebie

Boson symbols are `pkg.name`, methods `pkg.Type.method`, plus generated globals
(`__vtable_…`, `__typedesc_…`, `__projection_…`). Two problems with a naive
`.`→`__`:

1. **Not injective.** A name `pkg.Foo_bar` would collide with the method
   `pkg.Foo.bar`.
2. **C reserves identifiers** beginning with an underscore (file scope) and any
   containing a double underscore — so `_init`, `_heap`, `__vtable_…` are all in
   reserved namespaces.

**Recommendation:** an injective, length-prefixed (Itanium-lite) scheme under a
non-underscore prefix, e.g. `bo_` + per-segment `<len><segment>`:
`string.strlen` → `bo_6string6strlen`; `io.FD.read` → `bo_2io2FD4read`;
`_init.start` → `bo_5_init5start` (the embedded leading underscore is now
interior, so the identifier is not reserved). Generated globals get their own
tag namespace under the same prefix. Small, self-contained design task — flagged,
not hand-waved.

### Bounds checks and traps

Indexing and slice operations emit explicit checks mirroring the native traps
(including the slice-operation and negative-index checks added alongside this
proposal): `if ((uint64_t)i >= s.len) bo_index_oob(i, s.len);` — unsigned compare
so negatives trap, identical semantics to `_init.index_oob`. `nil_assert`
likewise. These are ordinary emitted C, not UB concerns.

### Multi-return, drops, entry point

- **Multi-return:** the AST already lowers this to a synthetic `{_0,_1,…}`
  struct (`MultiReturn`); emit it as a by-value struct return and destructure at
  the call site. Crosses package boundaries like any struct.
- **Drops:** `free(p)` → the floor's `bo_heap_free(p)`; `dispose` → nothing.
- **Entry point:** `_init.start` *becomes* C's `int main(int argc, char** argv)`
  — the floor builds the `[][]byte` argv Boson expects and calls `main.main`;
  the process exit code is `main`'s return.

---

## Architecture

### Fork point

In the driver's per-AST loop (`cmd/bosc/main.go`), a `-target=c` selects `emitC`
in place of `Compile`, with C analogues of `WriteVtables`/`WriteStrings`/
`WriteStrSliceHeaders`. `compile.go` is untouched. `emitC` is a syntax-directed
AST walker + a type-mapper (→ `boson.h` types) + the name-mangler; there is no
new IR — **C is the high-level IR**.

### The oracle — a full second pass, not "an extra walk"

Until the leaked semantic checks are hoisted (the audit), a from-AST backend
cannot re-derive language errors on its own: the census established that the
flow-sensitive checks are *interleaved into `compile.go`'s lowering*, with no
standalone checker pass to invoke. So soundness is bought by running the mature
path as a **gate**: compile to `.bs` into `/dev/null` (which performs the flow
checks), and only if it passes, run `emitC`. This yields, by construction:

```
accepted(C-target) = accepted(.bs) ∩ supported(emitC)
```

so the C path can never accept a program the native compiler rejects, even while
`emitC` is young. Mechanically it is a **full compile** (cheap relative to `cc`,
but not trivial), and because `Compile` mutates the `Context`, the emit pass
runs on a **re-derived AST + `Context`** (re-run `ToAST`), not the gate's dirtied
state. The oracle is a scaffold: it is over-strict (it also inherits
`.bs`-specific rejections that aren't language rules) and it transfers
*language-acceptance* parity, **not runtime-safety parity** (a latent lowering
unsoundness is inherited — cf. the slice-bounds hole fixed this session). It
retires as the audit's hoist proceeds and the checker becomes a real, separate
pass.

### `boson.h` and the compilation model

`boson.h` is the layout contract: it defines `boson_slice_*`, `boson_iface`,
`boson_vtable_*`, the ADT tagged-union shapes, and the floor's prototypes. Both
the generated `.c` and the hand-written floor include it; if they disagree on a
struct offset, the floor and the emitted code silently corrupt each other, so
this header is a first-class architectural element.

**Recommended model: one `.c` + one `.h` per package**, mirroring the existing
`.bo` + importcfg model — preserves separate compilation and matches how imports
already resolve. (Alternative: whole-program single-`.c`; simpler but no
separate compilation. Decide in phase 1.)

### Runtime floor

Most of the runtime is already **Boson source** (`io`, `fmt`, `string` logic) and
is transpiled by the same backend — only the thin syscall/entry floor needs
hand-written C: `_init` (argv construction, entry), `_heap` (`mmap`/`munmap`),
`_io_sys` (the read/write/open/close primitives), and the `index_oob`/`nil_assert`
traps. This is *less* hand-written code than the native path's `.bs` floor, and C
makes it easier (`int main`, libc or raw syscalls).

### Driver and build integration

`bosc -target=c -o pkg.c pkg.bos` emits C; a `boson.mmk` rule (`bos_exe` variant)
transpiles every package in the import closure, then invokes `cc` to compile and
link. No UB-defining flags are required (see *Emitting well-formed C*), so
optimization can be aggressive; test builds additionally pass
`-fsanitize=undefined` to police the well-formedness obligation. The oracle runs
as part of `bosc` when `-target=c` is set.

---

## Testing: differential against the native backend

The ~490-file `.bos` corpus becomes a **correctness oracle for the transpiler at
near-zero cost**: build each test through *both* backends and compare stdout (and
exit code for trap tests) against the *same* `.expected`. Any divergence between
the `.bs`-run and the C-run is a transpiler bug.

Scoping honestly (it is a strong net, not a proof): it is **phase-gated** (needs
the C floor before any corpus test can run through C) and covers only programs
both backends accept with deterministic stdout. Complement it with **UBSan on the
C output** across the corpus — that is the check that the emitter met its
well-formedness obligation; a wrap or punning mistake shows up as a UBSan
diagnostic or a differential-stdout mismatch, not as a latent `-O`-level surprise.

---

## Phased plan

Each phase keeps the oracle on and adds corpus coverage through the C path.

1. **Skeleton end-to-end.** Single `main` package; `i64`; function calls;
   integer/string output via a minimal hand-written C floor + `boson.h`. Goal:
   "hello" and integer output through `cc`. Establishes the mangler, the driver
   flag, the `boson.mmk` rule, and the mandated compile flags.
2. **Aggregates.** Structs, fixed arrays, slices, indexing + the bounds/nil
   traps. `boson_slice_*` generation.
3. **Control flow + expressions + casts.** Full operator set (with the UB
   guards), `if`/`for`/`while`, string constants and `&literal` static-init.
4. **Interfaces + ADTs.** Vtable structs and dispatch; tagged-union `values` and
   `switch`; `any` type-switch (the strict-aliasing-sensitive path).
5. **Ownership passthrough + multi-return + cross-package.** `free`/`dispose`;
   struct-return multi-value; per-package `.c`/`.h` and the importcfg analogue.
6. **Transpile the Boson-source runtime.** Bring `io`/`fmt`/`string` through the
   C backend; shrink the hand floor to the syscall/entry minimum.
7. **Relax the oracle** as the audit's hoist lands and the checker becomes a
   standalone pass.

---

## Open decisions (recommendations for ratification)

- **Runtime floor: raw Linux syscalls.** *Ratified.* Matches the toolchain's
  dependency-light, static-from-scratch ethos, and the runtime is already
  raw-syscall. libc (portability across OSes, less floor code) is deferred to the
  *Future direction* below, where lifting to libc is the natural step for
  hand-authored safe-C output.
- **Driver surface:** `bosc -target=c` (one compiler, two backends — *rec*) vs. a
  separate `bosc2c`/`bcc` tool.
- **C standard:** C99 vs C11. *Rec: C11* (`_Static_assert` for the layout
  contract, `_Alignas` if needed) with no other hard C11 dependence.
- **Compilation model:** per-package `.c`/`.h` (*rec*, separate compilation) vs.
  whole-program single `.c`.
- **Mangling scheme:** ratify the length-prefixed `bo_` scheme above (or pick
  another injective one).

---

## Future direction: Boson as a safe-C generator

A longer-horizon use of this backend is authoring **safe C by writing Boson**:
generate the C, then lift it out of the toolchain — drop `boson.h`, port the
Boson-specific runtime shims to libc by hand — and maintain it as ordinary C.
This is why the *well-formedness* obligation is non-negotiable rather than a
nicety: hand-edited output that only behaved under `-fwrapv`/`-fno-strict-aliasing`
would be a latent trap. Nothing here is built now; it is the north star that keeps
the emitted C honest, and the raw-syscall floor plus per-package model are the
first steps compatible with it. One step at a time.

## Risks and non-goals

- **Two-backend maintenance tax** — every new feature lowers twice. Mitigated by
  differential testing (catches drift immediately) and by the segregation hoist
  (semantics checked once, in the shared front end).
- **Emitting UB** — the *emitter*, not a flag, is responsible for well-formed C
  (see the obligations checklist); the residual risk is a *new* construct
  emitting UB, which UBSan-across-the-corpus plus differential testing are
  positioned to catch.
- **Oracle over-strictness** — the C path initially rejects some programs only
  because the `.bs` backend has non-language-specific limits; acceptable early,
  and the reason the oracle is a scaffold rather than the end state.
- **Non-goal:** replacing the native backend, or matching the Boson ABI for
  linker interop. The C path is self-contained and cc-native.
- **Non-goal:** exhaustive per-AST-node emission rules — those are implementation;
  this proposal fixes the contract, the hard cases, and the plan.
