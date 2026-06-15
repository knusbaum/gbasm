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
