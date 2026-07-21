# Boson TODO

A lightweight backlog of changes we want but that don't yet have a full
proposal. Keep entries short — just enough context to pick the work up later.
Promote an item to a `PROPOSAL_<name>.md` in `docs/proposed/` when it's ready to
design (see the docs convention in CLAUDE.md § Documentation and design docs).

---

## Language

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

### Flaky: `TestRunEndpointRunnerModeHelloIfBuilt` under full-suite load

`cmd/bplayd` enforces a 5-second per-run wall-clock timeout (`runTimeout`,
`main.go`). The playground test compiles, links, and runs a hello program
inside that budget, which is ample standalone but not when the whole suite is
competing for the machine — an observed failure took exactly 5.05s. Passes
alone and on re-run, so it reads as a spurious red rather than a real one,
which is the expensive kind of flake.

Fix by giving the test its own generous timeout rather than inheriting the
production default; the 5s ceiling is a *server* policy, not a property the
test means to assert.

### A Boson local named like a register breaks in `[name]` position
A `.bos` local whose name collides with an x86 register name (`sp`, `ax`, `di`,
…) assembles fine in direct position — bosc emits `local sp 64` / `mov sp 5`
and bas resolves the local. But the **indirect** form does not: a store through
such a local emits `mov [sp] <val>`, and bas's bracket parser resolves `sp` to
the stack-pointer register, producing
`MOV [(SIZE 0) [SP - 0], …]: Failed to find an instruction for Move` — an error
that names no source line and points at nothing the user wrote.

So `p := &x; *p = v` is fine but `sp := &x; *sp = v` is not, which is a
namespace leak from the assembler into the language. Found while writing the
global-store-through-pointer regression test.

**Decided direction, two halves:**

1. **bas rejects a `local` that shadows a register name.** Silently resolving
   one way in direct position and the other inside brackets is the actual
   defect; a hand-written `.bs` that declares `local sp` should be an error at
   the point of declaration, not a miscompile at the point of use. This half is
   self-contained — but check the runtime `.bs` files first, since any that
   already use such a name have to be renamed in the same change.
2. **bosc names locals so the collision cannot arise.** Undecided. Blanket
   mangling would work but makes the symbol table ugly to read, which matters
   because inspecting the `.bs` is the primary debugging path for this
   toolchain. Wants a scheme that stays legible — mangle only on collision, use
   a character registers can't contain, or scope-qualify. Needs thought before
   it is written.

Do (1) first: it converts a silent miscompile into a clear error and does not
depend on settling (2).

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

### Constant negative index should be a compile-time error
`nums[-1]` is decidable at compile time but only *runtime*-traps today, while
`nums[3]` (positive constant OOB) is already a compile-time error. The runtime
memory-safety hole is closed (unsigned index compare, `84ef387`); the remaining
work is the compile-time check — a small, clean hoist into `ToAST` that also
removes the positive/negative asymmetry. First concrete entry off the audit above.

---

## Runtime / Codegen

### Raise the alignment ceiling to 16 (needed for `movaps`/SIMD)

BABI-0 guarantees natural alignment up to **8 bytes**, which is all any current
Boson type needs. But 8 is also the ceiling: RBP is 8-aligned and not 16 (a
callee enters at RSP ≡ 8 mod 16 and the prologue subtracts 48 + a multiple of
16 + 8), and `_heap.alloc` returns a page-aligned mmap base plus an 8-byte
header. So the 16-byte-aligned SSE forms — `movaps`, `movdqa` — are still
unusable against Boson storage, on the stack or the heap.

That is fine today: nothing emits them, and `movss`/`movsd` are
unaligned-tolerant. It becomes a blocker the moment anything wants a vectorized
`memcpy`/compare, a SIMD intrinsic family, or a 16-byte scalar type.

Two pieces, independent: 16-align RBP (adjust the prologue's reservation and
keep `TestBABI0PrologueEstablishesStackAlignment` in step), and widen the heap
header to 16 so `alloc` returns a 16-aligned block. Both are small; the reason
to defer is that neither buys anything until there is a consumer.

### Full System V AMD64 ABI conformance

BABI-0 (DESIGN.md § The Boson ABI) matches System V on data layout, the integer
and SSE argument registers, the callee/caller-saved split, XMM0 returns, and
stack alignment. The corners below are still unimplemented or divergent. They
are listed separately because each can land on its own, but they are one body of
work: *say what we do, or do what we say*. The delta table at the end of the
BABI-0 section is the current, authoritative summary.

**Why it matters beyond tidiness.** The ABI is the boundary with everything not
written in Boson. Any future C interop — calling libc, being called from C, a
signal handler, a callback handed to an OS API — needs these corners right, and
each divergence is invisible until the day something on the other side of the
boundary exercises it. Conversely, if a corner is deliberately *not* going to be
implemented, DESIGN.md should say so plainly, the way it already does for
aggregate register classification.

#### Floating-point arguments past XMM7 aren't passed on the stack

INTEGER-class stack arguments now work on both sides (`bosc` pushes them in
reverse order with 16-byte alignment padding and reclaims them after the call;
the callee reads argument *i* > 5 at `[RBP + 56 + 8·(i-6)]`). The SSE half is
still missing: `Function.ArgFloat` refuses index >= 8 on the *callee* side, so
`argRegisters` deliberately keeps rejecting it on the caller side rather than
emitting a call the callee cannot read.

Lower priority than the integer case was — a nine-float function is much rarer
than a seven-argument one — but it is the remaining half of one feature.
Needs `ArgFloat` to fall back to `StackArg` and a load from the slot into an
XMM register, then the caller-side gate in `argRegisters` can come out.

#### Odd-sized small structs can't be passed by value

A by-value struct of 3, 5, 6, or 7 bytes is rejected with "passing a N-byte value
by value is not supported; use a pointer or a 1/2/4/8-byte layout"
(`packSmallMemBackedArg`, `cmd/bosc/compile.go`). Sizes 1/2/4/8 pack into a
register and anything over 8 bytes is passed by address, so only these in-between
sizes fail — e.g. `struct{ a i8; b i16 }`. Simplest fix is to route a ≤8-byte
aggregate whose size isn't 1/2/4/8 to the existing by-address path rather than
the packing path (widening the load instead would read past the allocation).
Found while surveying for float support; independent of it.

#### Aggregates are not classified per eightbyte

System V classifies an aggregate by splitting it into eightbytes and assigning
each a class, so `struct{ a f64; b f64 }` travels in *two SSE registers* and
`struct{ a i64; b f64 }` in one integer and one SSE register. Boson instead
treats class as a property of raw scalars only: an aggregate goes by address
above 8 bytes, or as opaque bits in an integer register at or below it,
whatever it contains. This is a **deliberate, documented** scope-out (see
DESIGN.md §Calling Convention and the floating-point proposal), and it is
self-consistent for Boson-to-Boson calls. It is listed here because it is
precisely the divergence that breaks C interop, and the decision should be
revisited — not silently inherited — when interop is on the table.

#### Aggregate returns need auditing against the MEMORY-class protocol

A >8-byte struct return currently works by handing back an address in RAX.
System V's MEMORY class instead requires the *caller* to allocate the result
and pass its address as a hidden first argument in RDI, with the callee
returning that same pointer in RAX. Whether Boson's mechanism coincides with
that or merely resembles it has not been checked. Audit it, then either
conform or document the divergence.

#### Variadic calls do not set AL

System V requires AL to hold the number of vector registers used when calling a
variadic function. Boson's own variadics do not go through the C convention (a
`...any` call builds a backing array), so nothing is wrong today — but a call
*into* a C variadic like `printf` would need this. File it with interop rather
than fix it speculatively.

### Negative caching for failed interface assertions
`_iface.assert_to` caches successful itabs per `(iface_desc, shape)` but
deliberately does not cache *failures* (`runtime/_iface/iface.bos`,
`DESIGN.md:1968`). A repeatedly-failing `x.(I)` therefore re-derives the
receiver shape, allocates an itab, walks the whole typedesc method table doing
hash *and* byte-equal name/sig compares, fails, and frees — every iteration,
which under today's one-mapping-per-allocation `_heap` is an `mmap`/`munmap`
pair per failed assertion. Record the failed `(dst_desc, shape)` so the second
attempt is O(1). Sequencing matters: this is a behavior change, so it must land
*after* the `_iface` port and the allocator (see
[`docs/proposed/PROPOSAL_raw_memory.md`](docs/proposed/PROPOSAL_raw_memory.md)),
both of which rely on the current semantics as their test oracle.

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
