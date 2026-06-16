# Boson TODO

A lightweight backlog of changes we want but that don't yet have a full
`PROPOSAL_*.md`. Keep entries short — just enough context to pick the work
up later. Promote an item to a `PROPOSAL_*.md` when it's ready to design.

---

## Language

### Expanded boolean operations
Booleans are thin today: a `bool` is produced only by a comparison
(`<`, `==`, `!=`, …) or `!` (logical not). There are **no `&&` / `||`
short-circuit operators** and **no `true` / `false` literals**. Want to
flesh this out — at least `&&`/`||` (with short-circuit evaluation) and
boolean literals. Decide whether `&`/`|` stay integer-only or also act on
`bool`.

### Compound assignment (`+=` and friends)
`x = x + 1` is the only form. Add `+=`, `-=`, `*=`, `/=`, etc.
*Integration note:* whatever lands must mark its target as a reassignment
for the never-reassigned and unused-binding checks — trivial if it
desugars to `x = x <op> v`; otherwise one `markMutRelied` + `markUsed`
call on the target.

### Operator completeness
Several expected operators don't exist as tokens yet:
- `%` (modulo) — remainder must currently be written `a - q*b` (see the
  `divmod` multi-return example/tour lesson).
- `^` (bitwise xor), `<<` / `>>` (shifts), `~` (bitwise not) — only `&`
  and `|` exist.

Worth doing as one pass so the arithmetic/bitwise surface is complete.

### Floating-point types
No floats yet (`f32` / `f64`). The lexer already accepts decimal-point
syntax but **truncates** the fractional part. Needs: the types, literal
support, codegen (SSE), casts to/from integers, and print verbs (`%f`).
The integers tour lesson currently has to say "there are no
floating-point types yet" — revisit it when this lands.

---

## Tooling

### Code formatter (`go fmt`-style)
A canonical, opinionated formatter for `.bos` source — no config, one
true layout, like `gofmt`. Would also give the editor integrations
(`boson-mode.el`, future LSP) a format-on-save target. Decide on a name
(`bosfmt`? a `fmt` subcommand of a future `bos` driver?).

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
The tour never teaches `fn main(args byte[][])` (argv) or `fn main() i64`
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
findings in `DESIGN_10_field_provenance.md`. Settled: coarse param-level
summaries are sound for borrows (the caller flattens a named struct arg's field
origins); field-level is *precision*, not soundness.

**Soundness — DONE except pointer-aliasing:**
- call face — literal-arg flattening in `argAliasProvenance` (`6b0093f`). ✓
- #18 — owned-aggregate return adopts its borrowed-param provenance (`9c2d170`). ✓
- global face — `checkPointerEscapeToGlobal` (`7a5c844`). ✓
- pointee-store/pointee-construct **direct** case — pointee-field tracking: lifted the
  `readProvenancePath` pointer-root cutoff + record on store / `new` (`1b2361f`). ✓
- **REMAINING — pointer aliasing:** a borrow stored through one pointer alias of
  a pointee and read through another (`h2 := h; h2.p = &s.x; *h.p`) still
  slips (pre-existing; path-keyed facts can't see the alias). Sound fix =
  **pointee-IDENTITY keying** (paths resolving to the same pointee origin share
  the fact); composes on the recording sites + lifted cutoff already built. Held
  `cov_owned_field_borrow_pointee_alias_err`. **Own focused pass.**
  Invalidation: KEEP facts, do nothing on opaque writes (dropping is unsound —
  see DESIGN_10).

**Precision (PLANNED follow-on, after soundness):** the field-level `.bo` fact
(`ReturnAliases [][]FieldAlias`, param-relative aliasing projection, k-limited,
param-level interface grammar with coarsen-then-⊆ satisfaction). Stops over-
rejecting independent/owned fields of a partially-borrowing aggregate. Full rep
design in `DESIGN_10_field_provenance.md`.

Held drivers: `cov_owned_field_borrow_escapes_{call,pointee_construct,pointee_store}_err`,
`cov_owned_aggregate_return_borrow_lost_err`.
