# AUDIT: slice header representation (adding `cap` — 16 → 24 bytes)

## History

- 2026-07-23 — Step 1 (the consolidation refactor) **landed**: the Go codegen
  now routes slice-header access through `sliceOffPtr`/`sliceOffLen`/
  `sliceHeaderBytes`, the read helpers `sliceHdrPtr`/`sliceHdrLen`, and the
  constructor `emitSliceHeaderStore` (`cmd/bosc/compile.go`), plus the two
  globals byte-encoders use the constants (`cmd/bosc/globals.go`). Pure refactor,
  behavior unchanged, full suite + `go_test` green. The cap widening is now a
  change to these definitions + the constructor + reslice cap-threading + the
  runtime `.bs`, not a scattered literal hunt. The interface fat-pointer sites
  were deliberately left at 16 (§D).
- 2026-07-23 — Audit performed ahead of the owned-slices `cap` work
  ([`PROPOSAL_owned_slices.md`](../proposed/PROPOSAL_owned_slices.md)). Surveys
  every place that infers the 2-word `{ptr, len}` slice layout so the widening to
  `{ptr, len, cap}` can be done without a silent miscompile. No code changed.

---

## Scope and the central findings

The owned-slices feature needs slices to carry a **capacity**. This audit finds
everywhere the current 2-word (16-byte) `{ptr, len}` header is assumed, so the
widening to a 3-word (24-byte) `{ptr, len, cap}` header is complete.

Four structural facts shape the whole change — establish these first, because
they decide what breaks and what doesn't:

1. **Put `cap` at offset +16 (Go layout: `ptr@0, len@8, cap@16`).** Then `ptr`
   and `len` keep their offsets, so every *read* of `[hdr]`/`[hdr+8]` is stable
   and does **not** change. This is the single most scope-reducing decision.

2. **Slices pass by pointer, not by value.** The runtime proves it: `puts` does
   `mov rsi [rdi]` / `mov rdx [rdi+8]` — it reads fields *through* `rdi` (the
   header's address), and callers `lea rdi [header]` (`runtime/_init/print_linux.bs`).
   So a slice argument is one pointer register regardless of header width: **the
   ABI register arity does not change.** (Contrast a by-value 16-byte aggregate,
   which would split across two registers — slices do not.)

3. **The Go codegen is largely `Size()`-driven, so it auto-follows.** Slice
   storage is a `bytes` slot sized by `ASTType.Size()` (via `typeIsMemoryBacked`);
   aggregate copies go through `move()` → `spot_memcpy(…, src.t.Size(c))`
   (`compile.go:2891`); struct field offsets are recomputed by `ByteOffset` from
   `Size()`; the `.bo` stores a `Var`'s header as `len(Var.Val)` bytes. Bump
   `Size()` for a slice from 16 to 24 and all of these follow automatically. **Do
   not chase these — verify them.**

4. **Interfaces are *also* 16-byte fat pointers and share idioms with slices.**
   `{data@0, vtable@8}`. Many `16`s and `bytes … 16` in the codebase are
   interface headers that **must stay 16**. Every shared 16-byte path has to be
   split so slices widen and interfaces don't. This is the main correctness trap.

**Net:** the breakage is not broad — it concentrates in (A) the type-size source
of truth, (B) header *construction* sites that must now also write `cap`, (C) a
handful of hardcoded literal `16`s in Go that are slice-specific, (D) the
hand-written runtime `.bs`, and (E) telling slice headers apart from interface
headers at the shared sites. Reads and `Size()`-driven allocation/copy come along
for free.

---

## A. Type-size source of truth (change first, watch the fallout)

| File:line | What | Action |
|---|---|---|
| `cmd/bosc/ast.go:1798` | `if t.IsSlice() { return 16 }` in `ASTType.Size()` | → `24`. This is the anchor; bumping it auto-propagates to all `Size()`-driven allocation/copy/offset logic (fact 3). |
| `cmd/bosc/babi0_test.go:142` | asserts `sliceOfByte.Size(ctx) == 16` | → `24`. |
| `cmd/bosc/babi0_test.go:149` | asserts `iface.Size(ctx) == 16` | **leave 16** (interface, not slice). |

---

## B. Header *construction* sites — must now also write `cap`

These build a header field-by-field. Today they write `ptr@0` and `len@8`; each
must additionally write `cap@16`. The destination slot is `Size()`-sized so it is
already 24 bytes wide — the bug would be leaving `cap` as garbage.

| File:line | Site | `cap` value to write |
|---|---|---|
| `cmd/bosc/compile.go:4780-4782` | **reslice** `SliceOp` codegen: `mov [ns] addr` / `mov [ns+8] upper` | `source_cap - lo`. **Requires reading the source's `cap@16`** (slice source) or using `ArraySize` (array source) — so reslice must be taught to read/compute cap. For `s[:]` full extent, `cap = source_cap`. |
| `cmd/bosc/compile.go:5690-5691` | **string literal** header: `mov qword[d] r10` / `mov qword[d+8] len` | `cap = len`. |
| `cmd/bosc/compile.go:4490` (region 4485-4520) | function-scope `&"literal"` static header | `cap = len`. |
| `cmd/bosc/globals.go:242` | file-scope `byte[]` (slice) static header, `make([]byte, 16)` | 24-byte, `cap@16 = len`. |
| `cmd/bosc/globals.go:382` | slice-typed **array-literal** global header, `make([]byte, 16)`; len at `out[8:]`, ptr reloc at offset 0 | 24-byte, `cap@16 = len`. |
| `cmd/bosc/globals.go:347` | **interface** static header (`make([]byte, 16)`, vtable reloc at offset 8) | **leave 16** — interface, not slice. |
| `runtime/_init/init_linux.bs` (argv builder) | outer + inner `[][]byte` headers, hand-built | see §D — inner `cap = len`, outer `cap = argc`. |

Reslice is the load-bearing one: it can only compute the new `cap` if the source
header already carries `cap`, which means the representation must flip
**atomically** — a half-migrated pipeline (some 16-byte, some 24-byte headers)
miscompiles silently.

---

## C. Runtime hand-written `.bs` (no `Size()` to hide behind — fix by hand)

The runtime assembles headers with literal offsets and sizes; none of it follows
`Size()`. Every one of these is a manual edit.

**`runtime/_init/init_linux.bs` — the `[][]byte` argv builder (highest stakes).**
It builds an outer slice-of-slices, so *both* levels widen:
- `30-36`: inner-array size = `argc * 16` via doubling ×4 → must be `argc * 24`
  (doubling no longer suffices; compute `*24`, e.g. `*16 + *8`).
- `43`: `sub rsp 16` (outer header) → `sub rsp 24`.
- `67`: `add rbp 16` (inner-header stride) → `24`.
- `63-64`: inner `ptr@[rbp]`, `len@[rbp+8]` → add `cap@[rbp+16] = len (= rax)`.
- `73-74`: outer `data@[r14]`, `len@[r14+8]` → add `cap@[r14+16] = argc (= rbx)`.

**`runtime/_init/print_linux.bs` — print helpers.** These build a scratch header
only to hand to `puts`, which reads *only* `ptr@0`/`len@8`:
- `puts` (`mov rsi [rdi]` / `mov rdx [rdi+8]`) — **read-only, stable, no change**.
- `puti` (`sub rsp 48`; header at `[rsp+24]`/`[rsp+32]`) and `putcstr`
  (`sub rsp 16`; header at `[rsp+0]`/`[rsp+8]`) build 16-byte scratch headers.
  Because `puts` never reads `cap`, these *function* correctly unchanged. But if
  the language now treats every `[]byte` as 24 bytes, passing a 16-byte scratch
  header is size-inconsistent — **update for uniformity** (widen the reservations,
  or leave with a comment that `puts` reads only two words). Decide the policy;
  don't leave it implicit.

---

## D. Interface / slice disambiguation (must NOT widen interfaces)

Interfaces are 16-byte `{data@0, vtable@8}` and share code and idioms with
slices. These sites are interface-only and **stay 16** — listed so the migration
doesn't widen them by pattern-matching `16`:

- `cmd/bosc/compile.go:467` (iface→iface 16-byte copy), `5659` (nullable iface
  nil = zeroed 16), `7733`/`7817` (fat-pointer layout), `8015`/`8095`
  (`bytes … 16`), `8059`/`8064`/`8154`/`8265`/`8707`/`8741` (assorted iface
  handling).
- `cmd/bosc/globals.go:262`, `:347` (interface static headers).
- `cmd/bosc/babi0_test.go:149` (iface size assertion).

The rule for the migration: change `16`→`24` **only** where the type is a slice
(`IsSlice()`), never where it is an interface. Prefer routing new size decisions
through `Size()` / a named `sliceHeaderBytes` constant rather than bare literals,
so the two never get conflated again.

---

## E. Read sites — STABLE with `cap@16` (inventory, no change)

All confirmed to read `ptr@[+0]` and/or `len@[+8]`, which do not move. Listed for
confidence, and because they are where a *wrong* cap-offset choice would surface:

- `runtime/_init/print_linux.bs` `puts`: `[rdi]`, `[rdi+8]`.
- `cmd/bosc/compile.go:4722` slice index base: `mov addr [v]`.
- `cmd/bosc/compile.go:4739`, `4132` `len()`: `mov … [v+8]` (array case uses
  `ArraySize` directly — also fine).
- `cmd/bosc/compile.go:3082-3083`, `4600-4601` for-range / bounds: `mov addr [w]`,
  `mov max [w+8]`.
- `runtime/sys/sys.bos:133` `buf_ptr(s []byte)` uses `&s[0]` (language-level index
  → `ptr@0`); `read`/`write` use `buf[:]` (reslice) + `len()`. All auto-follow
  once reslice/index/len are correct.

---

## F. `Size()`-driven — auto-follows a 24-byte `Size()` (verify, don't chase)

- `typeIsMemoryBacked` (`cmd/bosc/ast.go:213`) → slice stored in a `bytes` slot of
  `Size()` bytes → auto-24.
- `move()` / `spot_memcpy` (`compile.go:2878`, `383`, `2891`) → aggregate copy of
  `Size()` bytes → auto-24. (Spot-check that no path unrolls a slice copy as a
  fixed *two* qword movs instead of a `Size()`-driven copy.)
- Struct field layout: `StructDecl.ByteOffset` (`ast.go:2570`) sums `Size()` — a
  struct with a slice field auto-shifts later fields.
- `.bo` format: a `Var`'s header bytes are whatever `globals.go` emits
  (`len(Var.Val)`), so once the encoders in §B emit 24 bytes it stores 24.

---

## G. Cross-package / `.bo` compatibility (format concern)

Struct layouts and slice sizes are **recomputed** on both sides from `Size()`
(§F), not stored as absolute byte counts — so correctness requires *every*
package to agree that a slice is 24 bytes. A mix of old (16-byte) and new
(24-byte) `.bo` objects would miscompile silently (struct field offsets and
header copies would disagree). **Action:** bump the `.bo` format version (or
otherwise force a full rebuild of all `target/*.bo` and the runtime bundle) so a
stale 16-byte object can't link against 24-byte code. The prebuilt
`target/playground` runtime bundle must be rebuilt too (tour/bpipeline tests run
against it).

---

## `cap` semantics to decide per construction site

Not a code location but a design checklist the construction sites in §B need
answered:

- **`s[lo:hi]`** → `cap = source_cap - lo` (needs source cap).
- **`s[:]`** → `cap = source_cap`.
- **`arr[:]`** (array source) → `cap = ArraySize` (`- lo` for `arr[lo:]`).
- **string / slice / array literal** → `cap = len` (exact view; no spare).
- **`make([]T, len, cap)`** → `cap` explicit (this is where spare capacity is born).
- **argv inner headers** → `cap = len`; **argv outer** → `cap = argc`.

The reslice `s[lo:hi:max]` three-index form (Go's cap-limiting slice) is not in
the language today; if/when added, `cap = max - lo`.

---

## Recommended implementation order

1. **Decide layout + a named constant. — DONE.** `cap@16`; the Go codegen now
   routes header access through `sliceOffPtr`/`sliceOffLen`/`sliceHeaderBytes`,
   `sliceHdrPtr`/`sliceHdrLen`, and `emitSliceHeaderStore` (`compile.go`), with
   the globals encoders on the constants. Adding `cap` is now: extend
   `emitSliceHeaderStore` with a cap operand, add `sliceOffCap = 16`, bump
   `sliceHeaderBytes` to 24. (The interface fat-pointer sites in §D were left at
   16 and are untouched by these helpers.)
2. **Bump `ast.go:1798` → 24 and fix `babi0_test.go:142`.** Build and run the
   suite: `Size()`-driven paths (§F) follow; the failures that remain are the
   hand-written and hardcoded sites — a live worklist confirming this audit.
3. **Teach reslice to carry `cap`** (§B, `compile.go:4780`), threading source cap
   (read `cap@16` for slice sources, `ArraySize` for array sources). This is the
   keystone; do it before anything reads cap.
4. **Fix the other construction sites** (§B: string literal, `&"literal"`,
   `globals.go` slice headers) to write `cap`.
5. **Rewrite the runtime `.bs`** (§C: `init_linux.bs` argv, and the print-helper
   policy).
6. **Split interface vs slice** at the shared literals (§D) — verify no interface
   header widened.
7. **Bump the `.bo` version / force full rebuild** (§G).
8. Only then build **`cap()`**, and on top of it **`make` / `append` / `pop`**
   (the actual owned-slices feature).

Steps 1–7 are the representation migration and are the risky part; they should
land as one coherent change (fact: reslice needs source cap, so the pipeline
can't be half-migrated). Step 8 is additive on top of a correct 24-byte header.
