# Proposal: Floating-Point Types (`f32` / `f64`)

## History

- 2026-07-20 — Proposed. Grounded in a survey of all three affected layers
  (assembler/encoder/allocator, compiler front end + codegen, runtime
  formatting); every "current state" claim below carries a file:line citation
  and the three critical-path claims were verified directly.

---

## Summary

Add IEEE-754 binary32/binary64 (`f32`, `f64`) to Boson: types, literals,
arithmetic, comparisons, int↔float casts, the System V SSE calling convention,
and `%f` formatting.

**The headline finding of the survey: this is not a front-end project.** The
type-system work is broad but shallow and well-centralized. The real content is
that **`bas` has no concept of a register class** — `Registers.Get(size int)`
(`regalloc.go:120`) selects a register purely by bit width, and the `.bs` IR
`bosc` emits (`local <name> <bits>`, `compile.go:102`) has no way to say "this
value wants an XMM register." Everything else bottoms out there.

The pleasant surprise is that the layers people expect to be expensive are
nearly free: the instruction database already has every scalar SSE form, the
encoder needs ~30 lines, and the object format needs none.

---

## Motivation

- **Boson cannot express whole categories of programs.** Graphics, signal
  processing, statistics, physics, geometry, and most numeric work are simply
  out of reach. Every other item on the backlog refines a language that works;
  this one removes a hole.
- **The docs already carry a standing admission.** `DESIGN.md:2551` lists "No
  floats. Lexer accepts decimal-point syntax but truncates," and the tour's
  integers lesson has to tell readers floats don't exist.
- **It is a silent correctness trap today.** `1.5` lexes successfully and is
  *truncated to `1`* (`cmd/bosc/lexer.go:496-506`) with no diagnostic. A program
  that reads as if it does fractional math compiles and computes something else.
  Even if floats were never implemented, this must become an error.

---

## Goals

1. `f32` and `f64` as first-class scalar types: locals, parameters, returns,
   struct fields, globals, array/slice elements.
2. Float literals (`1.5`, `1e9`, `1.5e-3`) with a `<floatlit>` compile-time type
   mirroring `<intlit>`.
3. Arithmetic (`+ - * /`), unary negation, and comparisons with correct IEEE NaN
   behavior.
4. Explicit int↔float and `f32`↔`f64` casts.
5. Full System V AMD64 float ABI (args in XMM0–7 with an independent counter,
   return in XMM0).
6. `%f` in `fmt`, plus `%v` rendering floats naturally.
7. Floats boxable into `any` / interfaces (typedescs, assertions).

## Non-goals

- **`%g` / `%e`** and shortest-round-trip printing (see *Runtime* — a named
  follow-up, not a prerequisite).
- **String→float parsing.** No `strconv` analog exists at all; the runtime has no
  shipped integer parser either (`atoi` appears only as test-local Boson
  functions, e.g. `cmd/bosc/tests/atoi_test.bos:10`). Integer parsing should land
  before float parsing, since correct decimal→binary needs the same machinery.
- **SIMD / packed vector types, AVX/VEX.** `parseForm` hard-errors on VEX/EVEX
  (`encoder.go:1420`), so only legacy SSE encodings are reachable. That is
  sufficient for scalar float and is where we stop.
- **`f80` / `long double`, `f16`.**
- **Float `%` (remainder).** SSE has no scalar remainder instruction; C reaches
  for the `fmod` libcall, which a self-contained runtime will not. Reject it at
  compile time with a directed error.

---

## Current state (survey findings)

### Already works — no changes needed

| Area | Finding |
|---|---|
| **Instruction database** | `x86_64.xml` carries every scalar SSE form we need — `ADDSD` (:752), `MOVSD` (:10499), `CVTSI2SD` (:240212), `CVTTSD2SI` (:251867), `PXOR` (:703602), `XORPS` (:3018480), plus SUB/MUL/DIV/UCOMIS/COMIS/CVTSS2SD/CVTSD2SS in both `ss` and `sd` widths — all using the `<Prefix mandatory>`/`<REX>`/`<Opcode>`/`<ModRM>` vocabulary the parser already understands. |
| **Prefixes / opcodes / REX** | `parseForm` already emits mandatory `F2`/`F3`/`66` prefixes in document order (`encoder.go:1380`), two-byte `0F` opcodes are ordinary opcodes (`:464`), and `rex.Encode` (`:219`) builds R/X/B from `Register.byte()>>3` — exactly what XMM8–15 need. |
| **ModRM** | `modrm.Encode` (`encoder.go:488`) is operand-*shape* keyed, not width-keyed, so `mod=11 reg=xmm rm=xmm` and RIP-relative `[sym]` both work once `byte()` returns 0–15 for XMM. |
| **Object format** | `Var.VType` (`ofile.go:20`) is already a free-form string tag — `f32`/`f64` are just new values. `AddData` does `binary.Write(..., LittleEndian, val)` (`ofile.go:331`), so a Go `float64` serializes correctly with zero format change. Referencing a constant RIP-relative + relocation is the existing `*Var` operand path (`encoder.go:559`). |
| **Memory backing** | `typeIsMemoryBacked` (`ast.go:201`) lands both `f32` and `f64` register-resident with no change. |
| **Checker** | `checker.go` is scalar-kind agnostic (ownership/move/alias only). **No change.** |

### The critical path

**`bas` has no register classes.** This is the whole project.

- `reg.go:9` — `type Register int`, a flat enum of GPR aliases. **No XMM, YMM, or
  MMX register exists anywhere in the codebase.** Eight hand-written switches
  (`String` :98, `ParseReg` :255, `needREX` :412, `byte` :438, `Width` :540,
  `fullReg` :719, `brother8` :697, `partial` :857) each have a fatal default.
- `regalloc.go:120` — **verified**: `func (rs *Registers) Get(size int)`. Keyed
  purely on bit size; iterates the package-level `regs64` list. *There is no way
  to ask for a register of a given class — the signature itself is the blocker.*
  A 64-bit float request is indistinguishable from a 64-bit integer request.
- `function.go:613` — `Rallocs.Evict(size)` picks the first LRU entry with
  `Width() >= size`. With XMM in one shared pool, an *integer* request could
  evict and steal an XMM. Proof the pool must be split by class.
- `function.go:243`/`:170` — spill/reload hardcode `MOV`/`MOVZX`; XMM needs
  `MOVSD`/`MOVSS`.
- `encoder.go:849-861` — **verified**: `case "xmm"`, `"xmm0"`, `"ymm"`, `"zmm"`,
  `"mm"`, `"k"` are all *literally commented out* in `Op.Match`, so every SSE
  form fails to match. `case "m128"` (`:964`) is present-but-empty.
  `Op.ConvertRalloc` (`:685`) panics on `xmm`.

**The `.bs` IR cannot express a register class.** Verified at
`cmd/bosc/compile.go:102`:
```go
fmt.Fprintf(of, "\tlocal %s %d\n", ref, sz*8)
```
`bosc` never calls `Ralloc` — it emits a textual directive and lets `bas`
allocate. So `f32` → `local x 32` and `f64` → `local x 64` are **bit-identical to
`i32`/`i64`**. Worse, `Registers.Get(32)` would return an EAX-family *sub*-register
(an f32 is the low 32 bits of a 128-bit register, which is not a "partial
register" in this model), and `move`'s same-width path (`compile.go:2667`) would
silently emit an integer `mov` for a float. **Extending the `local` directive
with a class tag is the first design decision this proposal must settle.**

### Latent bug found en route (fix independently)

`xmm0` does not cleanly fail in `bas` today — it **silently miscompiles**.
`ParseReg` errors, no other operand form matches, and `isIdentifier("xmm0")`
returns true (`cmd/bas/main.go:1528`), so it becomes `&Var{Name:"xmm0"}` — an
unresolved *external symbol* with a RIP-relative relocation. Any typo'd register
name becomes a baffling link error instead of "no such register." Worth a small
standalone fix: the operand chain should reject register-shaped identifiers
rather than fall through to the symbol case.

---

## Language design

### Types

`f32` and `f64` join the scalar table in `DESIGN.md` §Types. Sizes 4 and 8;
both register-resident.

### Literals and `<floatlit>`

A new compile-time-only type `<floatlit>` mirrors the existing `<intlit>`
(`ast.go:2321`), which is resolved to a concrete type by context and evaluated at
compile time in arbitrary precision.

- **Lexing.** Two truncation sites (`lexer.go:466-482`, `:487-508`) must produce
  a float token instead. `numberset` (`lexer.go:118`) is `"0123456789."` — it
  admits `.` but *not* `e`/`E`/`+`/`-`, so exponent syntax needs the scan loop
  extended. `token` (`lexer.go:121`) gains an `fval float64` alongside `nval`.
  (`parser.go:232` already carries the placeholder comment `//fval float64 //
  we'll add floats later.`)
- **Constant arithmetic** uses `big.Float`, mirroring `<intlit>`'s `big.Int`
  (`EvalConst`, `compile.go:5737`), which today returns `(nil,false)` for a
  non-`uint64` literal — float literals would silently fail to fold without a
  sibling path.
- **An integer literal coerces to a float destination** (`x f64 := 1` is legal,
  as is `x * 2` where `x` is `f64`) — `<intlit>` is untyped and adopts the
  destination. **A float literal never coerces to an integer destination**;
  `x i64 := 1.5` is an error.
- **Range/precision.** `litFitsIn` (`compile.go:6096`) gains a float sibling
  rejecting literals outside the destination's finite range.

### Arithmetic

`+ - * /` and unary `-` are defined for `f32` and `f64`. `%` and all bitwise/shift
operators are **rejected** for floats.

Today `Op2.ASTType` (`ast.go:3226-3260`) requires *every* operand to satisfy
`isIntegerType`, with the error "operator %s requires integer operands." This
splits into a **numeric** gate (`+ - * /`) and an **integral** gate (`% & | ^ <<
>>`). The existing exact-match rule `if !ft.SameRepr(st)` (`ast.go:3253`) carries
over unchanged, so mixed `f64 * i64` remains an error requiring an explicit cast
— consistent with Boson's current strictness.

**Overflow/edge semantics differ from integers and must be documented**:
integer division by zero traps (`#DE`); float division by zero yields ±Inf
silently, and `0.0/0.0` yields NaN. Integer overflow wraps (`DESIGN.md`
§Expressions); float overflow yields ±Inf.

### Comparisons and NaN

IEEE semantics: `NaN == NaN` is **false**, `NaN != NaN` is **true**, and all of
`< <= > >=` are **false** when either operand is NaN.

This is a *third* comparison lowering, not a tweak to the existing two. Both
current paths are integer-shaped — the value form (`compile.go:6665-6712`) emits
`cmp` + `setl`/`setb` keyed off `ASTType.Signed`, and the branch form `jumpOp`
(`:5662-5701`) emits `cmp` + `jl`/`jle`/`jg`/`jge`. Floats use
`ucomisd`/`ucomiss`, which set **CF/ZF** — so the *ordered* comparisons are
spelled with the **unsigned** condition codes (`setb`/`seta`/`setbe`/`setae`,
`jb`/`ja`/…), plus explicit `setp`/`jp` parity handling for the NaN cases.
Getting this wrong is silent and only shows up on NaN inputs, so it needs
dedicated tests.

### Casts

`compileCast` (`compile.go:5945-6070`) currently has no legality table — it is a
ladder of category rejections followed by a size-driven switch (`srcSize ==
dstSize` → plain `mov`; widening → `movsxd`/`movzx`; narrowing → truncating mov).
**Floats break that premise**: `i64(f)` and `f64(i)` are the same size but are
*not* a `mov` — they are `cvttsd2si` / `cvtsi2sd`. A float-aware branch must
precede the size switch:

| Cast | Instruction |
|---|---|
| `f64(i)` / `f32(i)` | `cvtsi2sd` / `cvtsi2ss` |
| `i64(f)` / `i32(f)` | `cvttsd2si` / `cvttss2si` (truncating, C-style) |
| `f32(f64)` / `f64(f32)` | `cvtsd2ss` / `cvtss2sd` |
| float ↔ pointer | rejected |

`Context.TypeByName` (`ast.go:815`) gates cast *syntax* — without `f32`/`f64`
entries, `f64(x)` parses as "no such function."

### Negation

`-x` on a float is not `0 - x` (that gets `-0.0` wrong: `0.0 - (-0.0)` is `0.0`,
not `-0.0`). Use a sign-bit flip via `XORPS`/`XORPD` against a sign mask. **Use
the register-register form**, not a memory operand: the data section is
8-byte-aligned (`elf64.go:341,395`) with no per-symbol alignment field, and
`XORPS xmm,[mem]` requires 16-byte alignment. (`MOVSD`/`MOVSS` are
unaligned-tolerant, so ordinary constant loads are fine at 8.)

---

## `bas` changes

1. **`reg.go`** — append `X_XMM0`…`X_XMM15` to the enum; add
   `func (r Register) Class() RegClass` (`ClassGPR` / `ClassXMM`) derived from
   enum ranges; teach the eight switches. `fullReg()` is identity for XMM and
   `partial()` must fail (XMM has no sub-registers in this model).
2. **`regalloc.go`** — `Get(class, size)`; a second `rs` map and `regsXMM` pool.
   All XMM0–15 are caller-saved on SysV, which *simplifies* the pool (no
   callee-saved subset to prefer).
3. **`function.go`** — `class` field on `Ralloc`; class-aware `Rallocs.Evict`;
   `MOVSD`/`MOVSS` in `Ralloc.Evict`/`Ralloc.Register` spill/reload. The existing
   8-byte spill slots (`Rallocs.space`, `:298`) suffice for scalar floats.
4. **`encoder.go`** — implement `case "xmm"` and `case "m128"` in `Op.Match`
   (:849-861, :964); add `xmm` to `ConvertRalloc` (:685). Return a size for `xmm`
   that does not perturb the immediate-selection logic. ~30 lines.
5. **`cmd/bas/main.go`** — `ParseReg` accepts `xmm0`–`xmm15` (which also fixes the
   latent bug above by construction); float literal parsing; the `local`
   class tag.
6. **The `local` directive gains a register class** — see *Open questions #1*.

Prologue/epilogue need **no change**: SysV has no callee-saved XMM registers.
`Rallocs.EvictForCall` (`function.go:658`) does grow — every live XMM must spill
across a call, since all are volatile.

---

## `bosc` changes

**Wide but shallow.** Scalars are distinguished purely by `ASTType.Name`
(`ast.go:1563`), so ~13 enumerations need `f32`/`f64` added — all mechanical:
`TypeByName` (:815), `isIntegerType` (:886), `Size` (:1643), `mkTypename`
(:2267), the two type-alias decls (:4549, :4602), `parseTypeName`
(`parser.go:541`), `builtinScalarTypes` (`compile.go:345`), `scalarSize` (:372),
`raxForType` (:5705), `litFitsIn` (:6096), `canonicalTypeName`
(`typeinfo.go:54`, or typedesc hashes desync across packages), and
`encodeIntBytes` (`globals.go:468`).

The substantial pieces are the ones above: the arithmetic gate split,
`compileCast`'s conversion branch, the third comparison lowering, and — the
architectural one — teaching `newSpot`/`move`/`doOp2` about register class so
`local` carries it and `move` emits `movsd` rather than `mov`.

`coerceType` (`ast.go:2045`) is the single hub every value-coercion site
consults; `<floatlit>` needs one parallel arm there plus a guard that it never
coerces to an integer destination.

**Interface/`any` boxing** (`packSmallMemBackedArg`, `compile.go:5600`) assumes a
value fits a GP register; a boxed float needs `MOVQ xmm↔r64` or to go by address.

---

## ABI

System V AMD64 classifies arguments into INTEGER and SSE classes with
**independent sequence numbers**. Today both layers index by *argument position*:

- `compile.go:5447` — `order := []string{"rdi","rsi","rdx","rcx","r8","r9"}`,
  used as `order[i]`.
- `function.go:483` — `argRegForSize(i, size)`.

Both break the moment a float appears anywhere in the list: `f(int, float, int)`
must place the second `int` in **RSI**, not RDX. Each becomes two cursors — an
integer cursor over RDI/RSI/RDX/RCX/R8/R9 and an SSE cursor over XMM0–7 — chosen
by classifying each parameter. Returns: `raxForType` (`compile.go:5705`) gains an
`f32`/`f64` → XMM0 arm, and every caller assuming a GP result must be audited.

`DESIGN.md` §Calling Convention currently claims "System V AMD64 exactly" while
documenting only the integer class; it should be corrected to note the SSE class
and (for future C interop) that `AL` must carry the vector-register count when
calling variadic C functions — which nothing sets today.

---

## Runtime and `fmt`

**Float printing needs no SSE.** Ryu/Grisu/Dragon4 all operate on the sign,
mantissa, and exponent extracted from the IEEE-754 *bit pattern* using pure
integer arithmetic. The conversion is therefore decoupled from the XMM work,
except for one transitive need: getting a runtime f64 into a GPR requires
`MOVQ xmm→r64`.

`fmt`'s structure absorbs floats cleanly. Layer 1 already has
`itoa`/`utoa`/`htoa` with a documented contract — render into the head of a
caller-provided `[]mut byte`, return `out[0:k]` (`fmt.bos:80-86`) — and Layer 2
wraps each in a stack-buffer writer. Float adds `ftoa` + `n_fdigits` and a
`fmt.float` writer alongside. There is **no verb table**: `render_one`
(`fmt.bos:639-701`) is a linear `if` chain on the verb byte, dispatching through
a `try_signed`/`try_unsigned` cascade of comma-ok assertions (`:602-628`). Floats
add `try_float`, a `verb == 102` (`'f'`) block, and a `try_float` probe in
`render_v` (:742). Hours of work, once Layer 1 exists.

**The hard part is float→decimal.** No 128-bit or bignum arithmetic exists in
Boson or the runtime; but `mul`'s 128-bit RDX:RAX product *is* reachable in
hand-written `.bs`, and `ucountdigits` already uses it
(`runtime/_init/print_linux.bs:32`). So a correct printer would follow this
codebase's established pattern: an arithmetic primitive written in `.bs`.

**Recommended first cut: fixed-precision `%f`, default 6 decimals.** Decompose
the bit pattern with integer ops; handle NaN/±Inf/±0 first as pure bit tests;
integer part via the existing `utoa`; fraction as a scaled `u64` zero-padded to
the precision. Document the inexactness beyond ~15 significant digits the same
way `itoa` documents its truncation contract. Roughly a day, plus tests.
**Shortest-round-trip (`%g`) is a named follow-up** — Ryu needs a ~1200-entry
power-of-ten table and 128-bit multiplies — and retrofitting it later does not
invalidate the `%f` API.

---

## Implementation plan

Staged so each stage is independently testable, hardest-and-most-uncertain first.

### Stage 1 — XMM in the backend (the critical path)

`reg.go` enum + `Class()`; `regalloc.go` class-aware `Get` + second pool;
`function.go` class-aware evict/spill/reload; `encoder.go` xmm/m128 operand
matching; `bas` `ParseReg`. **Validated entirely by hand-written `.bs` golden
tests in `cmd/bas/tests/` — no `bosc` involvement.** This de-risks the whole
project before the compiler knows floats exist. There are no SSE tests today, so
this stage also establishes that coverage.

### Stage 2 — The `.bs` register-class tag

Extend `local` (Open question #1) and teach `bas` to route it to the right pool.
Still `.bs`-only.

### Stage 3 — Types, literals, constants in `bosc`

Lexer (stop truncating; exponents), `<floatlit>`, the ~13 enumerations,
`coerceType`, `encodeFloatBytes` in `globals.go`. Testable with float constants
and static initializers before any SSE codegen exists — the compiler can format a
compile-time-known constant via Go's `strconv.FormatFloat`, so "declare and print
a float constant" ships here with zero runtime float code.

### Stage 4 — Scalar codegen

`newSpot`/`move`/`doOp2` register-class awareness, arithmetic, `compileCast`
conversions, the third comparison lowering with NaN handling.

### Stage 5 — ABI

Two-cursor argument classification in both layers, XMM0 returns,
`EvictForCall`, interface/`any` boxing via `MOVQ`.

### Stage 6 — `fmt`

`ftoa` fixed-precision, `fmt.float`, `try_float`, `%f`, `%v`, golden tests.

---

## Testing

The golden-file pattern (`cmd/bosc/tests/<name>_test.bos` + `.expected`, driven
by `cmd/bosc/mmkfile`) applies directly, and `fmt_itoa_test.bos` is the model to
copy — it deliberately covers zero, positive, negative, the min-i64 overflow
edge, and the truncation contract on both signs.

Float tests must be at least as disciplined. Exact-string diffing means **every
printed digit is load-bearing**, which is itself an argument against a naive
conversion algorithm (a fixed-precision or shortest-round-trip result is
deterministic; a naive one drifts). Required edges:

- `0.0` and **`-0.0`** (must print distinctly), `1.0`, `-1.5`
- `NaN`, `+Inf`, `-Inf` — and the comparison identities: `x != x` true for NaN,
  all four relational operators false
- smallest subnormal, `DBL_MAX`, and a value exceeding the buffer (truncation)
- `f32`↔`f64` round-trips and int↔float casts at the range edges
- **ABI tests with interleaved int/float parameters** — `f(int, float, int)` is
  precisely what a position-indexed allocator gets wrong, and it will not show up
  in an all-float or all-int signature.

Stage 1 additionally needs `.bs` encoding goldens in `cmd/bas/tests/`, which do
not exist for SSE today.

---

## Risks

- **Allocator regression.** Splitting `Registers.Get` touches the allocator every
  existing program depends on. Mitigation: stage 1 lands behind `.bs` tests with
  no `bosc` change, so any regression surfaces against the existing bas suite
  before floats are reachable from the language.
- **Silent NaN/comparison bugs.** The unsigned-condition-code subtlety is easy to
  get wrong and invisible on non-NaN inputs. Mitigation: dedicated NaN tests in
  stage 4, written before the lowering.
- **Positional-ABI breakage.** Mixed int/float signatures are the failure mode
  and are easy to omit from tests. Mitigation: explicit interleaved-signature
  tests.
- **`f32` sub-register confusion.** `local x 32` currently means "an EAX-family
  partial register." Until stage 2 lands, an `f32` would be silently allocated a
  GPR. Mitigation: stage 2 precedes any `bosc` float codegen; consider making
  `f32` an error until then.
- **Scope creep into a correct dtoa.** Shortest-round-trip is a genuine
  multi-week sub-project. Mitigation: it is explicitly a non-goal, and the `%f`
  API does not foreclose it.

---

## Open questions

1. **How does `.bs` express a register class?** The `local` directive carries
   only a bit width today. Options: type-tagged (`local x f64`), width + class
   (`local x 64 xmm`), or a separate directive. This also affects the arm64
   backend, which has its own FP register file — so the answer should be a
   general register-class concept, not an x86 `xmm` keyword. **This is the first
   decision to make; stages 2–6 all depend on it.**
2. **Does `f32` compute in `f32`, or promote to `f64` and round?** SSE supports
   both. Go computes `float32` in `float32`. Recommend true `f32` ops (`movss`,
   `addss`, …) for predictability.
3. **`<floatlit>` precision.** `big.Float` (mirroring `<intlit>`'s `big.Int`) at
   what mantissa width? And is `x f64 := 0.1` required to round-to-nearest-even
   at the assignment, matching what a runtime parse would produce?
4. **Should `f32`/`f64` be comparable with `==` at all?** IEEE equality on floats
   is a well-known footgun. Boson is a strictness-first language; there is an
   argument for requiring an explicit epsilon comparison. Recommend allowing it
   (surprising to forbid) but noting it in DESIGN.md.
5. **Is a constants-only intermediate state (stage 3) worth shipping as a
   milestone**, given it lets users declare and print float constants but not
   compute with them? It is valuable as a de-risking checkpoint; it is not
   user-facing value on its own.
6. **`%f` default precision and syntax.** Fixed 6 like C, and do we support
   `%.3f` width/precision syntax in the first cut? `fmt`'s directive parser has no
   width/precision handling today.
