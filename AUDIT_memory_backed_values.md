# Coverage audit — memory-backed value mechanics

> This is the **first execution of the audit procedure** in
> [`COVERAGE_MATRIX.md`](COVERAGE_MATRIX.md) §5: a white-box re-grep of the
> value/address proxies plus invariant probes. Findings here are violations of
> invariants **I1** (value independence) and **I3** (storage fidelity), with one
> **I5** (equality). Future audits follow the same method; this one is the seed.

Motivation: we hit several severe bugs in *basic* features (struct copy, array
indexing) within days. This audit asks whether those were isolated or symptoms,
by auditing the feature-interaction surface rather than regression-testing finds
one at a time.

**Verdict: symptoms.** There is one fault line — *value vs. address* handling of
**memory-backed** types — and basic mechanics along it are broadly broken. A
20-minute probe sweep with proper oracles found **4 more live bugs** beyond the 2
already fixed, all the same class. Each is an **I1/I3 violation** in an
equivalence class (§4 here ↔ CL-* in the matrix) that had *no falsifiable test*
through it.

## 1. The feature axes

**Type kinds** (from `ASTType` shape discriminators) split into two camps by the
`typeIsMemoryBacked` / `nameIsAddress` predicate:
- **register-resident** (the name *is the value*): scalars ≤8, pointers,
  function pointers. Value semantics fall out of plain `mov`.
- **memory-backed** (the name *is an address*): structs, fixed arrays, anything
  >8 bytes. These need byte-copies and address arithmetic; a plain `mov name
  name` moves *addresses*, not contents.

**Operations** (AST forms): decl-copy (`:=`), assign-copy (`=`), arg-pass,
return, field read/write, index read/write, slice, address-of, deref, `==`/`!=`,
literals, call, method/interface dispatch, move/dispose.

**The matrix cell that fails** is *memory-backed type × value-moving operation*,
especially across the **size boundary (≤8 vs >8)** and under **nesting**. Every
bug below is one cell.

## 2. The fault line (one root cause)

The codegen repeatedly uses `Size > 8` or `scale ∈ {1,2,4,8}` as a proxy for "is
this a value or an address." It is not: an 8-byte struct is memory-backed
(address) but ≤8, so it takes the scalar path and gets its *address* moved /
loaded instead of its *bytes*. The correct predicate is `typeIsMemoryBacked` /
`nameIsAddress`. Both already-fixed bugs were this; so are the new ones.

## 3. Confirmed bugs (all one class)

| # | Cell | Symptom | Status | Site |
|---|------|---------|--------|------|
| 1 | struct ≤8 × copy (`:=`/`=`) | aliases source (mutate src → copy changes) | **FIXED** `450a36c` | `move` |
| 2 | struct ≤8 × array index (local) | segfault on field read | **FIXED** `30261d5` | Index rvalue |
| 3 | struct ≤8 × **deref** (`*px`, `(*px).a`, `q := *px`) | **segfault** | OPEN | `compile.go` ~3756 (`*Deref` ≤8 "small object just copy" value-loads) |
| 4 | **array nested in struct** (`s.arr[i].f`, any size) | **silent wrong value** (`0`) — every form: write+read, whole-elem store, struct-in-array-in-struct | OPEN | nested base-address for a struct's array field |
| 5 | struct `==` | **silent wrong** — compares addresses, two equal structs → not-equal | OPEN | `==` on memory-backed operands |
| 6 | struct >8 × inline deref-field (`(*px).a`) | internal type error "compiler produced S1" (rejects) | OPEN | `*Deref` >8 type propagation |

Severity note: #4 and #5 are **silent wrong values** — the worst kind, no crash.
#3 crashes. #6 rejects (loud).

Known *loud* limitations (explicit panics, not silent bugs — lower priority):
- array copying through a deref: `panic("Array copying not implemented")` (~3750).
- slicing arrays/slices whose element type is >8 bytes: `panic` (~4033).

## 4. White-box site list (`Size`/`nameIsAddress` value-vs-address proxies)

| Site | Path | Verdict |
|------|------|---------|
| `move` `Size>8` | binding copy | **was bug 1**, fixed (now `typeIsMemoryBacked`) |
| Index rvalue (const + var) | `arr[i]` read | **was bug 2**, fixed |
| `2471` move depoint `*T←T` | store thru ptr | shielded by the `move` fix ✓ |
| `2653` lvalue Index | `arr[i] = …` write | uses `lea` (address) — correct ✓ |
| `3696` Dot small-struct member | `s.inner` | returns address, comments the trap — correct ✓ |
| `3756` `*Deref` ≤8 | `*px` of struct | **BUG 3** — value-loads |
| variadic `6555`, return `4732` | pass/return | use `typeIsMemoryBacked` — correct ✓ |

The audit of `Size`/`{1,2,4,8}` sites is **complete** for `compile.go`; the only
remaining live one is #3. Bugs #4/#5/#6 are not `Size`-gated — they're address
arithmetic / type propagation / op-lowering, found black-box.

## 5. Why the tests missed all of this

- **Coverage tracks recency, not foundational-ness.** Prefix histogram: `owned`
  111, `retalias` 56, `iface`/`values`/`fmt`/`slice` 27–31 each — but `struct`
  17, `var` 5, `cast` 3, and *no* local array-value tests. The sophisticated
  recent features are saturated; the load-bearing basics are thin.
- **Test presence ≠ cell covered.** 17 `struct` tests, and the copy-aliasing bug
  passed all of them — because none mutated the source after a copy.
- **It's a test-*design* gap, not a harness limit.** The harness diffs stdout and
  can observe any behavior a program makes observable — including this. The
  existing struct tests were *construct-and-print*: `y := x; print y.a` prints
  `10` whether or not `y` aliases `x`, so that shape can't distinguish copying
  from aliasing. The *falsifying* shape — **copy → mutate source → read copy →
  print** — does make the bug observable (`20` vs `10`); it was simply never
  written. The corollary is that the fix needs **no tooling change** — only test
  programs whose oracle can fail (the probe pattern).

## 6. Recommendations

Order matters: **close the coverage gap first, let the fixes fall out.** The work
is governed by [`COVERAGE_MATRIX.md`](COVERAGE_MATRIX.md); this section is its
instantiation for the value model.

1. **Build invariant tests for I1/I3 across the hot equivalence classes**
   (`CL-MEMVAL`, `CL-PTR`, `CL-FIELD`, `CL-ELEM-ARR`, `CL-ELEM-SLICE`,
   `CL-COMPOSE`), one representative per class, swept across the size set
   {1,8,9,16,24} (S-THRESH) — because each class's anchor branches on size. The
   probe battery in §3/this audit is the seed corpus; promote it to
   `*_test.bos` + `.expected`. The open bugs surface as failing cells.
2. **Then fix** — #3/#6 are the same `typeIsMemoryBacked` correction made twice;
   #4 is `CL-COMPOSE` addressing; #5 is the I5 decision (field-wise vs reject).
   Batch them; the now-failing invariant tests are the regression suite.
3. **Record the audit** in the matrix's §7 log and update §4 classes if any
   anchor moved.
4. **Don't re-audit** the saturated, already-invariant-framed regions (owned /
   retalias / interface / values). The gap was only ever the value model, which
   had no invariant discipline until now.
