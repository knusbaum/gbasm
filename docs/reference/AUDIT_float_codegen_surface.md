# Audit: the codegen surface floats reach

## History

- 2026-07-20 — Reframed. The original title ("integer assumptions in codegen")
  implied codegen was defective. It was not: it relied on an invariant the type
  checker guaranteed, which is how the layering is supposed to work. Rewritten
  as an inventory of the sites that must become class-aware, not a bug list.
- 2026-07-20 — Written during Stage 3→4 of
  [`PROPOSAL_floating_point.md`](../proposed/PROPOSAL_floating_point.md), after
  two silent miscompiles made it clear the affected sites needed enumerating
  rather than discovering one at a time.

---

## Why this exists

Boson's type system began with a single type: `num`, a signed 64-bit integer.
Codegen was written against — and is still written against — an invariant the
**type checker** supplies:

> Every value that reaches arithmetic, comparison or scalar storage is an
> integer, living in a general-purpose register, fully described by its size.

**That is not a latent bug. It is correct layering.** Codegen's job is to lower
what the type checker has already declared legal, not to re-check legality. The
invariant held for every type the language has ever had: `i8`…`u64`, `byte`,
`bool` and pointers are all integers in a GPR differing only in width, and
anything else — a pointer in a division, a bool in a multiply — is rejected
upstream and never arrives. Verified: `*i64 / *i64` fails with "operator /
requires numeric operands, but got \*i64", nowhere near instruction selection.

What changes with floats is **the invariant, not the design**. `f64` and `i64`
are the same *size* and a different *register file*, so "size fully describes a
scalar" stops being true. Codegen must now dispatch on register class as well as
size — and it must do so at exactly the sites the widened invariant lets a float
reach, which is what this document enumerates.

The corollary matters for how Stage 4 is built: **do not scatter defensive float
guards through codegen.** That would push type knowledge back down a layer and
duplicate the checker. Extend the dispatch where a float can now legitimately
arrive, and leave everything else alone.

### Why the two miscompiles happened

Neither was codegen's fault. Stage 3 widened the type checker — making `+ - * /`
accept floats — *before* codegen could lower them. The invariant codegen relies
on was broken from above, so it did the well-defined integer thing:

- `fn f(a f64, b f64) f64 { return a + b }` compiled clean, emitting
  `argi a 0 64` — the integer argument registers — and an integer `ADD` on the
  bits of two doubles.
- `fn f(_ f64)` did the same through the discarded-parameter path.

`requireFloatCodegenSupport` is the correct response: it temporarily narrows the
invariant back to what codegen can honor, so the gap fails loudly instead of
silently. It is scaffolding with a deletion date, tracked in the proposal's
gaps-and-debt checklist — not a permanent second line of defense.

This document is the inventory. It is a *reference*, not a plan: the plan is the
proposal's staged implementation. Use it to check that each stage covered what it
claims, and to know where to look when a float behaves like an integer.

---

## What actually reaches codegen (verified)

**The type checker is the gate, and it works.** A pointer or a bool never
reaches `doOp2` — `*i64 / *i64` is rejected with "operator / requires numeric
operands, but got \*i64" long before instruction selection. The reason floats get
further is not that the gate is missing; it is that **float arithmetic is
legal**. Stage 3 made `+ - * /` accept floats; Stage 4 has not yet taught codegen
to lower them.

So the reachable surface is exactly "the operations that are legal on floats",
which is much smaller than the inventory below might suggest. Measured against a
build with the codegen gate disabled:

| Operation | Reaches codegen? |
|---|---|
| `+` `-` `*` `/` | **yes** — legal, not yet lowered |
| `<` `<=` `>` `>=` `==` `!=` | **yes** — legal, not yet lowered |
| `%` `&` `\|` `^` `<<` `>>` | no — type checker rejects ("requires integer operands") |
| `~` | no — type checker rejects ("requires an integer operand") |
| unary `-` | no — but see below; it is caught *by accident* |

Everything else in the inventory (moves, casts, arguments, returns, literals,
field and element access) is reachable because it is not operator-gated at all:
those paths carry values of any type.

**Unary `-` is caught by accident, and will stop being caught.** `-x` is
desugared in `ToAST` to `Op2{n_sub, Literal{0}, x}`, and the literal `0` fails
`litFitsIn`, which has no float cases — so the error is *"Integer literal 0 does
not fit in type f64"*. That message leaks the desugaring and is wrong on its
face (0 is representable in an `f64`). The moment `litFitsIn` learns about
floats, this stops erroring and starts compiling to an integer `sub` — which also
gets `-0.0` wrong, since `0 - 0.0` is `+0.0`. Negation must be special-cased
*before* the desugar, and the diagnostic fixed.

The practical consequence: **the type checker is where non-float types are kept
out, and it should stay that way.** Codegen's job is to lower what is legal, not
to re-check legality. The gates described under *Current containment* are
scaffolding for unimplemented lowering, not a substitute for type checking.

---

## The one structural change

`spot` — codegen's handle for a value — has no register class, because until now
there was only one. `newSpot` emits `local <ref> <size*8>`, and the `.bs` `local`
directive read that as a general-purpose register until Stage 2 added the
`f32`/`f64` size operand.

Giving `spot` a class and threading it through `newSpot`/`move` is the single
change that makes most of the inventory below fall out, rather than needing a
per-site fix.

## The highest-leverage single fact

**`f32` and `f64` are `Signed: false`** (`ast.go`, `getBuiltinType`). Codegen
consults `.Signed` in eleven places to pick between a signed and an unsigned
instruction. Floats therefore take the *unsigned* arm everywhere by default:
`div` not `idiv`, `shr` not `sar`, `setb`/`seta` not `setl`/`setg`, `movzx` not
`movsx`.

Setting `Signed: true` would be equally wrong — it just picks the other integer
instruction. **The fix is a float guard *before* each `.Signed` branch**, not a
different value for the flag.

`.Signed` instruction-selection sites: `*Return` 1/2-byte extend; `setupArgs`
argument extend; `compileCast` (`movsxd`/`movsx`/`movzx`); `doOp2` mul, div and
mod (`imul`/`mul`, `idiv`+`cqo` / `xor rdx,rdx`+`div`); `doOp2` shift
(`sar`/`shr`); `doOp2` comparisons ×4 (`setl`/`setle`/`setg`/`setge` vs
`setb`/`setbe`/`seta`/`setae`); `compileInterfaceMethodCall` argument extend;
`emitInterfaceFatPtr` narrow-scalar extend.

---

## Inventory by category

Grouped by the kind of dispatch that must become class-aware, roughly in the
order the stages address them. "Silent" means a float reaching this site today
produces working code with the wrong answer.

### 1. `mov` as the generic value move

`move` is the central mover and emits a bare `mov` for register-to-register,
same-representation scalar, and store-through-pointer. **Every** float
assignment, temp copy, return-value pickup and call-result move flows through
it. Needs `movss`/`movsd`.

Also size-dispatched `mov` sites: `storeScalarToMem` (qword/dword/word/byte);
array-literal element stores; `*Dot` and variant-field member loads; `*Index`
element loads; `compileValueIntoAddress` (`new f64(x)`); `compileStructLiteralInto`
scalar fields; `MultiBind`/`MultiAssign` multi-return destructuring; `*Deref`
pointer loads; `compileTypeAssert` and `compileTypeCaseBody` payload extraction.

Benign: `spot_memcpy` (a byte blit — correct for float aggregates); variadic
backing-array chunk copies; `mov <ref> 0` zeroing, which happens to produce
`+0.0`.

### 2. Arithmetic — `doOp2`

No float branch exists anywhere in `doOp2`. Selection is by size and `.Signed`.

- **`add`/`sub`** — silent; need `addsd`/`addss`, `subsd`/`subss`.
- **Unary negation** — desugared to `Op2{n_sub, Literal{0}, x}`. Currently caught
  only incidentally (see *What actually reaches codegen*), with a misleading
  message, and will stop being caught once `litFitsIn` learns about floats. Needs
  a sign-bit `xorpd`/`xorps` and a special case *before* the desugar; a `sub`
  also gets `-0.0` wrong.
- **`mul`** — takes the one-operand `mul` via rax/rdx arm (because
  `Signed == false`). Silent and catastrophic.
- **`div`** — `xor rdx,rdx` + unsigned `div`. Silent.
- **`mod`, `&`, `|`, `^`, `~`, `<<`, `>>`** — **not reachable**; the type checker
  rejects all of them for floats (Stage 3 split `isIntegerType` from
  `isNumericType`), verified against a gate-free build. The codegen paths would
  lower a float if reached, so they are listed here for completeness, but they
  need no float guard as long as the type checker keeps its.

### 3. Comparisons

`doOp2`'s `<`, `<=`, `>`, `>=` emit `cmp` + a `setcc` chosen by `.Signed`, so
floats get an unsigned `setcc` on an integer `cmp`. `==`/`!=` emit `cmp` +
`sete`/`setne`, which gets both `-0.0 == 0.0` (must be true) and `NaN == NaN`
(must be false) wrong.

Needs `ucomisd`/`ucomiss` with the NaN-aware lowering from the proposal.

Note `jumpOp` — a fused `cmp`+`jcc` helper hardcoded to signed integers — **has
no callers**. It is dead code, but it is the path an optimizer would revive, so
it needs the same treatment or deletion.

### 4. Register-class decisions made by size alone

`newSpot`/`newSpotWithReg` (`local ref sz*8`); `compileFunctionBody` `argi`
(three arms: named, discarded, and memory-backed ≤8); `setupArgs` (pin to
`order[i]` when `Size == PTR_SIZE`, else `inreg …rdi`, else `movsx`/`movzx`);
`packSmallMemBackedArg` (`switch size {1,2,4,8}`); `*Return` 1/2-byte extend;
`compileCast` (see below); `doOp2` mul/div/mod via `mulDivRegs(sz*8)`; shift
count register width; `compileInterfaceMethodCall` (duplicates `setupArgs`'
logic); `storeScalarToMem`; `emitInterfaceFatPtr`; type-assert payload load and
store.

Purely-layout uses of `.Size` — struct field offsets, array strides, allocation
sizes — are correct as-is and are *not* included above. `scalarSize` is used only
for typedesc emission and already knows `f32`/`f64`.

### 5. Casts — `compileCast`

The final dispatch is entirely size-based with no int-vs-float check, and the
guard rails above it (ptr↔non-ptr, slice↔scalar, struct↔non-struct) have no
int↔float case. All three arms are wrong for floats:

| Cast | Current | Needed |
|---|---|---|
| `i64` ↔ `f64` | same size → plain `mov` (bit reinterpretation) | `cvtsi2sd` / `cvttsd2si` |
| `f32` → `f64` | larger dest → `movzx` (`Signed == false`) | `cvtss2sd` |
| `f64` → `f32` | narrowing → low-bits truncation | `cvtsd2ss` |

The literal fast path is `EvalConst`-based and integer-only, so `f64(3.5)` has no
route at all.

### 6. Argument passing and returns

- **Argument registers** are a hardcoded `rdi, rsi, rdx, rcx, r8, r9` with no
  XMM sequence and no separate float counter. An `f64` argument goes in `rdi`.
  The logic is duplicated in `compileInterfaceMethodCall`.
- **Return placement** — `raxForType` maps by type name with `default: "rax"`,
  so `f32` and `f64` both return in `rax` rather than `xmm0`. Four call sites.
- **Return pickup** at each call site goes through `move`, i.e. category 1 again.

### 7. No route to materialize a float constant

`*Literal` in `compileTop` handles `string`/`uint64`/`byte`/`bool` and panics on
anything else; `EvalConst` is `*big.Int`-only. There is no `mov xmm, imm` on
x86-64, so a float constant must come from a GPR via `MOVQ` or from `.rodata`.
This is a *missing capability*, not a wrong one — it fails loudly.

---

## Current containment

`requireFloatCodegenSupport` refuses float storage at every `newSpot`,
`newSpotWithReg`, and `argi` arm (including the discarded-parameter arm, which
the first attempt missed). A float literal in codegen gets a positioned
diagnostic rather than a bare panic. Untyped float literals default to `f64`,
mirroring `<intlit>` → `i64`.

**These gates are load-bearing until the corresponding stage lands.** The
approach for Stage 4 is to *narrow* them incrementally rather than remove them,
so anything not yet explicitly handled keeps refusing. Their removal is tracked
in the proposal's gaps-and-debt checklist.

---

## The general lesson

**Widening an invariant at one layer, before the layer below can honor it,
produces silence rather than failure.** The lower layer keeps doing the
well-defined thing its old contract allowed. The same shape produced the
encoder's REX bug (`304e8a3`), where wrong bytes ran correctly for years because
REX.X is ignored without a SIB byte.

The discipline is not "distrust the layer below". It is: when you widen what a
layer above will emit, either widen the layer below in the same change, or
narrow it explicitly — a loud refusal — until you do.
