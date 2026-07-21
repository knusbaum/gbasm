# Boson TODO

A lightweight backlog of changes we want but that don't yet have a full
proposal. Keep entries short — just enough context to pick the work up later.
Promote an item to a `PROPOSAL_<name>.md` in `docs/proposed/` when it's ready to
design (see the docs convention in CLAUDE.md § Documentation and design docs).

---

## Language

### Floating-point types
**Promoted to a design:
[`docs/proposed/PROPOSAL_floating_point.md`](docs/proposed/PROPOSAL_floating_point.md).**
`f32`/`f64`, literals, SSE codegen, int↔float casts, the System V float ABI, and
`%f`. The survey found the cost is *not* the front end: `bas` has no register-class
concept at all (`Registers.Get` selects purely by bit width, and the `.bs` `local`
directive carries only a width), so teaching the assembler about XMM is the
critical path. The instruction database and object format already suffice.
The integers tour lesson currently has to say "there are no floating-point types
yet" — revisit it when this lands.

---

### Checked-arithmetic intrinsics
Integer overflow wraps by default (defined two's-complement, both signs — see
DESIGN.md §Expressions). That is cheap and keeps numeric loops vectorizable, but
silent where a wrap is unintended (size math, untrusted input). Add an explicit
opt-in: a `checked_add`/`checked_mul`/… intrinsic family returning an
optional/error on overflow (and/or a checked operator). Mirrors "safe when you
ask" — the inverse of a wrapping operator on a trapping default. Lowers to an
overflow-flag check in the native backend and an explicitly-checked helper in the
C backend.

---

## Tooling

### `bas`: an unknown register name silently becomes an external symbol
`bas`'s operand-resolution chain falls through to `isIdentifier` →
`&gbasm.Var{Name: ...}` (`cmd/bas/main.go:1459`, `:1528`), so a register name the
assembler doesn't know — `xmm0`, or simply a typo like `rxa` — is not rejected.
It becomes an unresolved *external symbol* with a RIP-relative relocation, and
the mistake surfaces much later as a baffling link error (or worse, resolves
against a real symbol). Register-shaped identifiers should be rejected at parse
time with "no such register". Found while surveying for float support; fixing it
is independent of that work.

### Code formatter (`go fmt`-style)
A canonical, opinionated formatter for `.bos` source — no config, one
true layout, like `gofmt`. Would also give the editor integrations
(`boson-mode.el`, future LSP) a format-on-save target. Decide on a name
(`bosfmt`? a `fmt` subcommand of a future `bos` driver?).

---

## Docs

### Backfill DESIGN.md coverage of shipped features
`docs/implemented/` proposals are frozen historical records and `DESIGN.md` is
the single living reference (see CLAUDE.md § Documentation and design docs). Some
features likely shipped without their canonical spec fully folded into
`DESIGN.md`. Audit each `docs/implemented/` proposal against `DESIGN.md` and fill
the gaps so the reference stands on its own.

---

## Compiler architecture

### Hoist leaked semantic checks out of codegen
`compile.go` enforces ~179 language/type/flow rules that logically belong in
`ToAST`/the checker, not in `.bs` lowering. Full classified census (semantic vs
lowering vs ambiguous, with top hoist candidates) in
[`docs/reference/AUDIT_codegen_check_segregation.md`](docs/reference/AUDIT_codegen_check_segregation.md).
Target: codegen only ever raises *lowering* concerns ("not supported"/bug); all
semantics caught earlier — the prerequisite for a sound from-AST second backend
(e.g. a C transpiler) and for retiring the validation-oracle scaffold. The
ownership/move/escape bulk is flow-sensitive → checker, not `ToAST`; do the
type-only families (name resolution, arity, cast legality, struct-literal shape)
first.

### Odd-sized small structs can't be passed by value
A by-value struct of 3, 5, 6, or 7 bytes is rejected with "passing a N-byte value
by value is not supported; use a pointer or a 1/2/4/8-byte layout"
(`packSmallMemBackedArg`, `cmd/bosc/compile.go:5658`). Sizes 1/2/4/8 pack into a
register and anything over 8 bytes is passed by address, so only these in-between
sizes fail — e.g. `struct{ a i8; b i16 }`. Simplest fix is to route a ≤8-byte
aggregate whose size isn't 1/2/4/8 to the existing by-address path rather than
the packing path (widening the load instead would read past the allocation).
Found while surveying for float support; independent of it.

### Constant negative index should be a compile-time error
`nums[-1]` is decidable at compile time but only *runtime*-traps today, while
`nums[3]` (positive constant OOB) is already a compile-time error. The runtime
memory-safety hole is closed (unsigned index compare, `84ef387`); the remaining
work is the compile-time check — a small, clean hoist into `ToAST` that also
removes the positive/negative asymmetry. First concrete entry off the audit above.

---

## Runtime / Codegen

### Read-only `.rodata` hardening
String constants currently land in the writable `.data` LOAD segment;
their immutability is enforced only at the source level, not by hardware.
DESIGN flags this as "a future hardening pass": split `.data` and
`.rodata` into separate LOAD segments (touches `elf64.go` / `linker.go`)
so string-constant immutability is hardware-enforced.

---

## Tour

### Command-line arguments lesson
The tour never teaches `fn main(args [][]byte)` (argv) or `fn main() i64`
(exit code) — every lesson uses `fn main()`. Add a short lesson once
slices and strings are introduced (Data section), then drop the
forward-reference now sitting in the Functions lesson.

---

## Distribution / Packaging

### Installable distribution for users
There is no good way for a user to install Boson today. You build `bosc`/
`bas`/`bld` from source with `mmk`, and the runtime is found ad hoc via
`BOSONPATH`/the playground bundle. We want a real distribution: package the
toolchain binaries plus the runtime packages (`_init`, `_heap`, string, fmt,
io, …) into an installable artifact (tarball / OS package / installer) that
drops the tools on `PATH` and lets the compiler find the runtime without
manual `BOSONPATH` fiddling. Decide the runtime-discovery story (install prefix
vs. embedded bundle) as part of it.

---

## Borrow checker: #10 field-buried borrow escape (+ #18)

A borrow of local storage buried in an aggregate field can escape the frame
(through a call, a `new()` constructor, a pointer write, or a global) and be used
after the local is consumed, undetected. Four faces; full design + empirical
findings in `docs/proposed/DESIGN_10_field_provenance.md`. Settled: coarse param-level
summaries are sound for borrows (the caller flattens a named struct arg's field
origins); field-level is *precision*, not soundness.

**Soundness — DONE (all faces, incl. pointer-aliasing):**
- call face — literal-arg flattening in `argAliasProvenance` (`6b0093f`). ✓
- #18 — owned-aggregate return adopts its borrowed-param provenance (`9c2d170`). ✓
- global face — `checkPointerEscapeToGlobal` (`7a5c844`). ✓
- pointee-store/pointee-construct **direct** case — pointee-field tracking: lifted the
  `readProvenancePath` pointer-root cutoff + record on store / `new` (`1b2361f`). ✓
- pointer aliasing — **pointee-IDENTITY keying** (`pointeeFieldKey`: key the
  pointee-field fact on the root's pointee origin, which aliased pointers `h`/`h2`
  already share). `cov_owned_field_borrow_pointee_alias_err` green + precision
  guards `cov_pointee_{alias_repoint_live,distinct_alloc}`. Invalidation: KEEP
  facts, nothing on opaque writes (dropping is unsound).

**Precision (PLANNED follow-on, after soundness):** the field-level `.bo` fact
(`ReturnAliases [][]FieldAlias`, param-relative aliasing projection, k-limited,
param-level interface grammar with coarsen-then-⊆ satisfaction). Stops over-
rejecting independent/owned fields of a partially-borrowing aggregate. Full rep
design in `docs/proposed/DESIGN_10_field_provenance.md`.

Held drivers: `cov_owned_field_borrow_escapes_{call,pointee_construct,pointee_store}_err`,
`cov_owned_aggregate_return_borrow_lost_err`.
