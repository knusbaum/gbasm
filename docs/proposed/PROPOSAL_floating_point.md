# Proposal: Floating-Point Types (`f32` / `f64`)

## History

- 2026-07-20 — **Float `==` reversed to allowed (full IEEE).** A survey of C,
  C++, Go, Rust, Zig, Java and Python found no mainstream language bans it;
  their guardrails are predicates, opt-in lints, a standard approximate
  comparison, or (Rust) excluding floats from total-order contexts via traits —
  a middle ground unavailable to us without a trait system. Banning would also
  have pushed legitimate exact comparisons through `bits()`, which is worse for
  `x == 0.0` since `bits(+0.0) != bits(-0.0)`. Cost is two extra ops plus a
  parity test for equality only; orderings stay free via the swapped-operand
  `seta`/`setae` form. `is_zero` is no longer owed, `is_nan` becomes a
  convenience, and `bits` remains as an `ftoa` primitive. Approximate comparison
  drops to deferred library work.
- 2026-07-20 — **Remaining language-surface questions settled.** Class tag: the
  float type name *replaces* the size operand (`local y f64`), carrying allocator
  and instruction width in one architecture-neutral token. `<floatlit>` is a Go
  `float64`, with the decimal→f64→f32 double-rounding case recorded as a known,
  accepted limitation (constructed and verified; unreachable at random).
  Float `==`/`!=` were **rejected** *(superseded — see the entry above; `==` is
  allowed)*; the NaN-false ordering via swapped operands and `seta`/`setae`
  survives the reversal unchanged. `%f` is fixed-6 with `ftoa` taking a
  precision parameter from day one; `%v` trims trailing zeros keeping `.0`;
  width/precision syntax is deferred to a general `fmt` feature. Only the shape of
  approximate comparison remains open.
- 2026-07-20 — **Evaluation precision settled** (former Open Question #2):
  `f32` computes in `f32` via the single-scalar instruction family;
  intermediates are never silently widened. Widening is explicit
  (`f64(a)`), exact, and costs one instruction.
- 2026-07-20 — **Aggregate ABI settled** (former Open Question #7): class is a
  property of *raw scalars only* — aggregates are passed by pointer (>8B) or as
  opaque bits in a GPR (≤8B), exactly as today, and a float inside an aggregate
  never acquires an SSE class. Removes SysV's per-eightbyte classification from
  scope entirely. Corrects an earlier misreading of `packSmallMemBackedArg`
  (large aggregates are passed by address, not rejected).
- 2026-07-20 — **Register allocation settled: parallel allocators per register
  file, over extracted shared frame state** (resolves the former Open Question
  #1). Rejected parameterizing the single allocator with a class. Added the
  `Frame` extraction as a prerequisite refactor, the full list of what must be
  coordinated across files, class-crossing moves, and the aggregate/multi-return
  ABI decision point.
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
- **Representation (decided): a Go `float64`.** It represents every `f32` and
  `f64` value exactly, so no wider type is needed. `EvalConst`
  (`compile.go:5737`) returns `(nil,false)` for a non-`uint64` literal today, so
  folding needs a `float64` sibling path or float literals silently fail to fold.

  *Known, accepted limitation:* parsing decimal→`f64`→`f32` can land one ulp off
  from decimal→`f32`, because `f64` may round the value *onto* an `f32` midpoint
  that then breaks round-half-even the other way. It is not reachable by accident
  — four million random decimals produced zero divergences — but it is
  constructible:

  ```
  1.000000178813934325304513262011596452794037759304046630859375
    decimal -> f32        : 1.0000001192092896   (correct: below the midpoint)
    decimal -> f64 -> f32 : 1.000000238418579    (one ulp high)
  ```

  It requires ~60 significant digits landing within half an `f64` ulp of an `f32`
  midpoint. The same applies to constants folded in `f64` and then narrowed. If
  we ever care, the fix is one line: keep the literal's source text and parse with
  `strconv.ParseFloat(text, 32)` when the destination is `f32`, so it rounds once.
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

### Evaluation precision (decided: `f32` computes in `f32`)

`f32` arithmetic is evaluated in `f32` using the single-scalar instruction family
(`addss`, `subss`, `mulss`, `divss`, `ucomiss`); `f64` uses the double-scalar
family. **Intermediates are never silently widened.**

The type is a promise about precision, not a storage hint. Widening intermediates
would make results depend on where the compiler chose to round — that is, on
register allocation and spilling — which is precisely the x87 failure mode that
SSE exists to have fixed (and that C had to add `FLT_EVAL_METHOD` to describe).
It also keeps results identical to every other SSE-era language.

The difference is not subtle. With all-`f32` `a = 1e8`, `b = 1.0`, `c = -1e8`:

| | `a + b` | `+ c` | result |
|---|---|---|---|
| in `f32` | `1e8` — `1.0` is below the ulp and vanishes | `0` | **`0.0`** |
| widened to `f64` | `100000001.0` exactly | `1.0` | **`1.0`** |

And for overflow, with `a = b = 1e30`, `a * b / a`: in `f32` the product exceeds
the ~3.4e38 maximum → `+Inf` → result `+Inf`; with `f64` intermediates the
product is fine and the result is `1e30`.

**Widening is explicit and cheap.** A user wanting more headroom writes
`f64(a) * f64(b)`, which lowers to one `cvtss2sd` per operand plus `mulsd` — the
cost is a single instruction per widened value and it is visible in the source.
The asymmetry makes this safe: **`f32`→`f64` is exact**, since `f64`'s range and
mantissa strictly contain `f32`'s; only the deliberate narrowing back rounds.

*Cost to us*: both the `ss` and `sd` variants of every arithmetic, comparison,
and conversion instruction, rather than `sd` plus conversions. Mechanical —
`x86_64.xml` carries every form.

*FMA is unavailable regardless.* A fused multiply-add computes `a*b+c` with a
single rounding — another source of "same source, different answer." FMA is
VEX-encoded and `parseForm` hard-errors on VEX (`encoder.go:1420`), so it cannot
be emitted even by accident.

This is a defined-evaluation-semantics rule and belongs in `DESIGN.md`
§Expressions when it lands, alongside the integer-overflow-wraps rule.

### Comparisons (decided: full IEEE, including `==`)

`==`, `!=`, and the four orderings are all valid on `f32`/`f64`, with IEEE
semantics.

*An earlier draft rejected `==`.* A survey of prior art reversed it: **no
mainstream language bans equality on floats.** C, C++, Go, Rust, Zig, Java, and
Python all allow it with IEEE semantics; their guardrails come in four kinds:

1. **Predicates only** — C (`isnan`/`isinf`/`fpclassify`), Go (`math.IsNaN`),
   Zig, Java.
2. **Encode the partiality in the type system** — Rust gives `f64`
   `PartialEq`/`PartialOrd` but *not* `Eq`/`Ord`, so floats are excluded from
   contexts that assume a **total** order (sorting, hash keys) without the
   operator being taken away.
3. **Opt-in lints** — GCC `-Wfloat-equal`, Clippy `float_cmp`; neither on by
   default.
4. **A standard approximate comparison** — only Python (`math.isclose`, PEP 485)
   and Zig (`approxEqAbs` / `approxEqRel`).

Rust's is the most principled, but it needs a trait system to attach the
distinction to. Boson has none — and no maps or sort where a total order would be
assumed — so that middle ground isn't available to us. That left the two
extremes: ban (unprecedented, and it forces legitimate exact comparisons through
`bits()`, which is *worse* for the common `x == 0.0` case, since
`bits(+0.0) != bits(-0.0)`), or allow. **We allow.**

Java is the cautionary tale for what not to do: it ships *two disagreeing*
equalities — primitive `==` is IEEE, while boxed `Double.equals` compares bit
patterns, so `NaN.equals(NaN)` is **true** and `0.0.equals(-0.0)` is **false**.
One equality, consistently applied, beats either choice made twice.

#### Semantics to document in DESIGN.md

- **Equality is not reflexive.** `x == x` is **false** when `x` is NaN. This
  breaks an assumption that holds for every other Boson type.
- **`+0.0 == -0.0` is true**, despite differing bit patterns. (`bits(x)`
  distinguishes them when that matters.)
- **Ordering is partial.** All four relations are **false** when either operand
  is NaN — there is a fourth outcome, *unordered*, beyond less/equal/greater.
- **Therefore `!(a < b)` is not `a >= b`.** With a NaN both are false. Any future
  optimizer must never rewrite one into the other (this is one of the things
  `-ffast-math` breaks). It is also why a float comparator is unsafe as a sort
  predicate — answering "false" in both directions violates what sort algorithms
  assume.

#### Lowering

`UCOMISD`/`UCOMISS` report all four outcomes in three flags, with **PF as the
"unordered" flag**:

| Outcome | ZF | PF | CF |
|---|---|---|---|
| unordered (NaN) | 1 | **1** | 1 |
| `a < b` | 0 | 0 | 1 |
| `a == b` | 1 | 0 | 0 |
| `a > b` | 0 | 0 | 0 |

Flags land the way an *unsigned* integer compare would (CF/ZF, never SF/OF), so
float comparisons use the above/below condition codes, not the signed ones. On
the unordered row the "below" forms answer **true** (wrong for us) while the
"above" forms answer **false** (what we want). So express every ordering with
`seta`/`setae`, choosing operand order to suit — no parity test, no extra
instruction:

| Boson | Rewritten | Emitted | NaN |
|---|---|---|---|
| `a < b`  | `b > a`  | `ucomisd b, a ; seta`  | false ✓ |
| `a <= b` | `b >= a` | `ucomisd b, a ; setae` | false ✓ |
| `a > b`  | —        | `ucomisd a, b ; seta`  | false ✓ |
| `a >= b` | —        | `ucomisd a, b ; setae` | false ✓ |

**Equality cannot use that trick.** `==` needs ZF=1, but *unordered also sets
ZF=1*, and swapping operands doesn't separate them (equality is symmetric). It
must test PF explicitly — two extra ops:

```
; a == b   →  ZF=1 AND PF=0        ; a != b   →  ZF=0 OR PF=1
ucomisd  a, b                      ucomisd  a, b
sete     r1                        setne    r1
setnp    r2                        setp     r2
and      r1, r2                    or       r1, r2
```

In branch position, equality needs two conditional jumps rather than one:

```
; if (a == b) goto L               ; if (a != b) goto L
ucomisd a, b                       ucomisd a, b
jp      .skip                      jp      L      ; unordered ⇒ not-equal is true
je      L                          jne     L
.skip:
```

#### Implementation hazard: `bas` does not model EFLAGS

The allocator may insert flag-clobbering instructions (spill reloads,
zero-extensions) between a compare and its dependent `setcc`/`jcc` — this
**already bit the `&&`/`||` codegen**, where a reload's `xor` wiped the flags a
`setne` needed (see CLAUDE.md § *bas does not model EFLAGS*).

The equality sequence is three flag-dependent instructions needing two scratch
registers — exactly the shape most likely to provoke a spill mid-sequence, and
the failure is silent, appearing only on NaN inputs. **Lower float equality by
branch materialization** — jump to terminal paths that write the result with
`mov <dest>, <imm>` — which is the same fix already applied to `&&`/`||` rather
than a new pattern. The orderings, being a single `setcc` adjacent to their
compare, are not exposed to this.

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

## Register allocation: parallel files over a shared frame (decided)

**Decision: one allocator per register file, not a class-parameterized `Get`.**

The two files have genuinely different semantics — GPRs have sub-registers, XMM
does not; GPRs mix caller- and callee-saved, XMM is entirely volatile;
spill/reload is `MOV`/`MOVZX` vs `MOVSD`/`MOVSS`. Parameterizing the single
allocator would thread class-aware no-ops through all eight sub-register switches
in `reg.go`, and would leave the cross-class eviction hazard (an integer request
stealing an XMM in `Rallocs.Evict`, `function.go:613`) as something to *remember*
rather than something structurally impossible.

The existing types already support this with no call-site churn:

```go
type Ralloc    struct { …; rallocs *Rallocs }        // alloc → its allocator
type Rallocs   struct { …; rs *Registers }           // allocator → its file
type Registers struct { rs map[Register]*rstate }    // pure availability map
```

A float `Ralloc` pointing at the float allocator dispatches through
`Register()`/`Evict()` naturally, `Get(size)` keeps its signature on both sides,
and the float file simply never has sub-register machinery rather than having
class-aware no-ops.

Note the allocator is consulted **only for named/allocated values**. Explicit
registers in `.bs`, `bytes`, `var`/`data`, and everything memory-backed bypass it
entirely — so this change's blast radius is narrower than "the register
allocator" sounds.

### Prerequisite: extract frame state out of `Rallocs`

`Rallocs` currently owns three fields that are **not register-allocator state**.
They live there because the spill path happened to need them:

| Field | Actually is | Touched only by |
|---|---|---|
| `localoff` | stack-frame cursor | `space()` (:312), `Epilogue` (:849) |
| `freeSpace` | spill-slot free list | `space()` / `free()` (:300-319) |
| `names` | the function's local symbol table | the alloc/lookup methods |

The discriminator: each changes when the *frame* grows or a *name* is bound —
never when a register is allocated.

This is not cosmetic. Two wholesale allocators would each carry their own
`localoff` cursor and `freeSpace` list while carving up **one** stack frame →
overlapping spill slots and silent memory corruption. `Epilogue` sizes the stack
from a single `localoff` (`function.go:849`), which is only correct if it *is*
single. Likewise `names` is how `bas` resolves operands (`f.AllocFor`), so
splitting it would force every lookup to try both files.

Extract them into a frame object both allocators hold by pointer:

```go
type Frame struct {                      // per function, shared by every file
    names     map[string]*Ralloc         // one namespace
    localoff  uint32                     // one stack frame
    freeSpace []struct{ size, offset int32 }
}

type Rallocs struct {                    // now purely register-file state
    regs  map[Register]*Ralloc
    lru   []Register
    rs    *Registers
    frame *Frame                         // shared
    f     *Function
}
```

**This refactor is worth doing on its own merits**, independent of floats: it
removes a conceptual misplacement and makes the allocator's real state obvious.
The change is small and mechanical — the three fields appear in only the handful
of sites listed above.

### What must be coordinated across files

Splitting the files makes most things fall out correctly, but four things span
both and must be driven together:

1. **`EvictForCall`** (`function.go:658`) — a call clobbers both files. All XMM
   are caller-saved, so *every* live float spills at *every* call. It currently
   walks only the caller-saved GPR list.
2. **`EvictAll`** (`:648`) and **`Function.Label`** (`:864`) — a label is a
   control-flow merge point for both files.
3. **Spill/reload instruction selection** — `Ralloc.Evict` (:243) and
   `Ralloc.Register` (:170) hardcode `MOV`/`MOVZX`. Once split this is naturally
   per-file, which is another argument for this shape.
4. **Frame allocation** — both files call `space()`/`free()` on the shared
   `Frame`. Scalar `f32`/`f64` fit the existing 8-byte slots; only packed 128-bit
   values would need a 16-byte size class, and those are out of scope.

### `(class, size)` means different things per class

Worth stating explicitly, because it shapes the allocator API. For GPRs, `size`
selects a **sub-register** (`Get` does `r.partial(size)`). For XMM there are no
sub-registers — the register is always 128 bits and `size` selects the
**instruction** (`movss` vs `movsd`). So the float allocator's `Get` does not
share the `partial()` path at all; size is carried for instruction selection, not
register selection. `fullReg()` is identity on XMM and `partial()` does not
apply.

### Remaining `bas` changes

1. **`reg.go`** — append `X_XMM0`…`X_XMM15`; add `Class()` derived from the enum
   ranges; teach `String`/`ParseReg`/`needREX`/`byte`/`Width`. The sub-register
   helpers (`fullReg`/`brother8`/`partial`/`subRegisters8`) need only to be
   correct-by-construction for XMM (identity / not-applicable).
2. **`regalloc.go`** — a second `Registers` instance over an `regsXMM` pool. All
   XMM0–15 are caller-saved, so the pool needs no callee-saved preference
   ordering — simpler than the GPR pool.
3. **`encoder.go`** — implement `case "xmm"` and `case "m128"` in `Op.Match`
   (:849-861, :964); add `xmm` to `ConvertRalloc` (:685). Return a size for `xmm`
   that does not perturb immediate-selection. ~30 lines.
4. **`cmd/bas/main.go`** — `ParseReg` accepts `xmm0`–`xmm15` (which fixes the
   latent unknown-register bug by construction); float-literal parsing; the class
   tag on `local` and argument directives.
5. **`func_arg_regs`** (`regalloc.go:84`) — a backend-side argument register list
   separate from `bosc`'s; needs a float sibling.

Prologue/epilogue need **no change**: SysV has no callee-saved XMM.

### The class tag on directives (decided: the float type replaces the size)

Explicit registers in `.bs` carry their own class, so only *allocated* things
need a tag: `local`, temporaries, and argument declarations.

**The size operand becomes "an integer bit-width *or* a float type name."**
Integers keep today's spelling; floats name their type:

```
local x 64        ; integer, 64-bit — unchanged
local y f64       ; float register, 64-bit ops (movsd/addsd/…)
local z f32       ; float register, 32-bit ops (movss/addss/…)
```

One token carries exactly the two facts needed — **which allocator** (float vs
integer) and **which instruction width** (`sd` vs `ss`) — with no separate class
keyword and no `size + class` pair. It is also architecture-neutral: arm64 would
spell it identically and route to its own FP file, so nothing about the IR
becomes x86-specific.

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

### Class-crossing moves

Nothing in the IR today expresses "move between register files," and floats need
it in two places:

- **Boxing a float into `any`/an interface.** The fat pointer's data slot is
  filled by `packSmallMemBackedArg` (`compile.go:5600`), which assumes a GP
  register — a boxed float needs `MOVQ xmm↔r64`, or to go by address.
- **The float printer.** `ftoa` works on the IEEE bit pattern with *integer*
  arithmetic, so it must get the bits into a GPR.

At the `.bs` level direct registers cover this (`movq rax, xmm0`), but `bosc`'s
`move` helper (`compile.go:2650`) dispatches on type and needs an explicit
cross-class case. This is a distinct operation from a same-class move — not a
size or a class on one value, but a pair of classes.

### Aggregates (decided: class is a property of raw scalars only)

**The rule: an aggregate is passed by pointer; a raw scalar is passed in a
register of its class.**

The aggregate half is already how Boson works. A memory-backed aggregate larger
than 8 bytes is passed **by address** — the caller copies and the callee
dereferences (`compile.go:5620`) — and one of 8 bytes or less is packed as
**opaque bits into a GPR** (`packSmallMemBackedArg`, `:5617`). Floats change
neither. A float *inside* an aggregate is just bits; it never acquires an SSE
class.

So the class decision happens in exactly one place — for a raw scalar, from its
own type: `f32`/`f64` → SSE (XMM0–7 for args, XMM0 for returns), everything else
→ INTEGER. **No per-eightbyte classification, no merge rule, no register pairs.**

#### What we are deliberately not implementing

SysV classifies aggregates **per eightbyte** — 8-byte chunks split by *offset*,
not by field:

1. Larger than 16 bytes (or containing unaligned fields) → MEMORY: on the stack,
   returned via a hidden pointer.
2. Otherwise each eightbyte is classified from the fields occupying it — SSE only
   if **every** field in that chunk is floating-point; **INTEGER wins any mix**.
3. Eightbytes are assigned walking in order to *independent* cursors: INTEGER to
   RDI/RSI/RDX/RCX/R8/R9, SSE to XMM0–7 (returns: RAX/RDX and XMM0/XMM1).
4. All-or-nothing — if the registers for *all* the eightbytes aren't free, the
   whole aggregate goes to the stack.

| Aggregate | SysV | Boson (ours) |
|---|---|---|
| `{f64,f64}` (16B) | XMM0, XMM1 | by pointer |
| `{i64,f64}` (16B) | RDI, XMM0 | by pointer |
| `{f32,f32}` (8B) | XMM0 (both packed low) | one GPR (opaque bits) |
| `{f32,i32}` (8B) | **one GPR** — mixed eightbyte merges to INTEGER | one GPR |
| `{f64,f64,f64}` (24B) | stack (MEMORY) | by pointer |

The `{f32,i32}` row is the trap: it *contains* a float but travels in an integer
register, so a naive "contains a float → XMM" rule silently mismatches. Adopting
classification means adopting the merge rule too, or being wrong.

This divergence is **pre-existing and independent of floats** — Boson already
declines to pass any aggregate in a register pair. Packing aggregates into
registers is a wanted future optimization; when it lands it will need the full
classification (merge rule included) to be correct. Floats neither require it nor
make it harder.

`DESIGN.md` §Calling Convention's "System V AMD64 exactly" is therefore already
inaccurate for aggregates, before floats enter the picture, and should be
qualified.

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

### Verb design (decided)

**`%f` renders fixed-precision, default 6** — matching C and Go, so it holds no
surprises.

**Width/precision syntax (`%.3f`, `%8.2f`) is deferred, and is not a float
feature.** `fmt` has no flags/width/precision parsing at all today: `render_one`
(`fmt.bos:639-701`) is a linear `if` chain on a single verb byte, and there is no
`%5d` or `%08x` either. Floats should not pay for building that layer; it belongs
to a general `fmt` directive feature that `%d`/`%x`/`%s` want equally.

**But `ftoa` takes a precision parameter from day one** — `ftoa(out, v, prec)`.
The capability then exists, and exposing `%.3f` later is purely parser work in
that general feature rather than a change to the conversion.

**`%v` renders fixed-6 with trailing zeros trimmed**, keeping one fractional
digit: `1.0` prints `1.0`, `3.14` prints `3.14`. Trimming is a few lines in the
writer and makes `%v` genuinely pleasant, where bare fixed-6 would print
`1.000000`. Retaining the `.0` keeps the value visibly a float, which fits how
`%v` is specified in `DESIGN.md` ("natural by the value's type"). *Documented
limitation:* this does **not** round-trip — a value needing more than 6 decimals
loses information — until `%g` lands.

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
Stage boundaries are **commit points, not ship gates** — nothing is released
until the whole feature is done; the stages exist so progress is captured and
each piece is verifiable on its own.

### Stage 0 — Extract `Frame` from `Rallocs`

Move `names`/`localoff`/`freeSpace` into a shared `Frame` object. **Pure
refactor, no float content, no behavior change** — the existing bas + bosc suites
are the whole test. Worth landing on its own merits, and it is the prerequisite
that makes a second allocator safe.

### Stage 1 — XMM in the backend (the critical path)

`reg.go` enum + `Class()`; a second `Registers` over an XMM pool;
per-file spill/reload; `encoder.go` xmm/m128 operand matching; `bas` `ParseReg`.
**Validated entirely by hand-written `.bs` golden tests in `cmd/bas/tests/` — no
`bosc` involvement.** This de-risks the whole project before the compiler knows
floats exist. There are no SSE tests today, so this stage also establishes that
coverage.

### Stage 2 — The `.bs` register-class tag

Extend `local` and the argument directives with a class tag (Open question #1) and
route each name to the right allocator. Wire `EvictForCall`/`EvictAll` to drive
both files. Still `.bs`-only.

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

**None blocking.** Every design question raised has been settled; what remains is
deferrable library work.

*Deferred (not a prerequisite):* the shape of **approximate comparison**. With
IEEE `==` allowed this is a library nicety rather than a language obligation, and
can land later in a math package. Recommend Zig's model — separate
`approx_eq_abs(a, b, tol)` and `approx_eq_rel(a, b, tol)`, tolerance always
explicit, **no default**. Python's `math.isclose` shows why a default is risky:
its `abs_tol=0.0` silently fails when comparing against zero, since relative
tolerance is meaningless there, and PEP 485 has to document the workaround.

### Settled during design

| Question | Resolution | Section |
|---|---|---|
| Register allocation shape | Parallel allocator per register file, over extracted shared `Frame` state | *Register allocation* |
| Class-tag surface syntax | The float type name **replaces** the size: `local y f64` | *The class tag on directives* |
| `f32` evaluation precision | Computes in `f32`; widening is explicit and exact | *Evaluation precision* |
| `<floatlit>` representation | Go `float64`; the decimal→f64→f32 double-rounding case is a known, accepted limitation | *Literals and `<floatlit>`* |
| Float `==` | **Allowed**, full IEEE — no mainstream language bans it, and Rust's type-system middle ground needs traits we lack | *Comparisons* |
| Aggregate ABI | Class is a property of raw scalars only; aggregates go by pointer | *Aggregates* |
| `%f` / `%v` | `%f` fixed-6; `ftoa` takes `prec` from day one; `%v` trims trailing zeros keeping `.0`; width/precision syntax deferred to a general `fmt` feature | *Verb design* |
| Milestones | Stage boundaries are commit points, not ship gates — nothing ships until it all does | *Implementation plan* |
