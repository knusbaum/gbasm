# Design note: return-alias inference engine (rebuild)

**Status:** in progress on branch `feature/return-alias-inference`
(checkpoint commit `ee64ad1`). This note records the *engine* design we
converged on after finding the first engine (the demand-driven
shape-by-shape walk) to be architecturally wrong. It **supersedes the
"Inference", "five return sites", "caller-side propagation", and
"`classify*`/`thread*`" mechanics** in `PROPOSAL_return_alias_inference.md`.
The proposal's *goals/features* still hold unchanged: option (a) interface
guard + directed diagnostic, `.bs`/`.bo` transport (`retaliases`
directive), `bdump`, the `ReturnAliases [][]int` fact, no user syntax.

---

## Why the first engine was wrong

The feature = infer, per function, which **parameters** each return slot
may alias, so callers can extend their borrow analysis across the call.

The first engine (`cmd/bosc/retalias.go`, `aliasSet` + `classify*` +
`thread*`) was a **parallel re-implementation** of the borrow tracker: it
re-derived provenance by case-analysing the AST shape of each return
expression (struct literal vs symbol vs call vs slice field …). Every
shape got its own branch, and every place the re-derivation diverged from
what the real tracker would compute was a soundness hole — hence three+
rounds of whack-a-mole (reassignment, branch-merge, struct-through-call,
struct-param return, nested-literal-from-call …).

Two stacked problems:

1. **The inference doesn't query the tracker — it approximates it.** The
   real borrow analysis is fused into `compileTop` (flow mutations
   interleaved with codegen), so it can't be cleanly run standalone for a
   callee's summary; the first engine re-implemented a subset and the
   subset leaked.
2. **The tracker's struct-*value* model was itself incomplete.** It
   modeled direct field assignment (`b.buf = x`) for the existing
   local-escape checks, but not: by-value struct **params** (no field
   provenance at all — **Gap 1**), struct values returned **from a call**
   (faked with a `__callret` sentinel — part of **Gap 2**), etc. So even a
   clean query wouldn't have complete answers for structs.

Conclusion (user's call): **delete the approximation. There must be one
tracker, and we query it.**

---

## The two gaps in the *real* tracker

- **Gap 1 — by-value struct/array params carry no field provenance.**
  At function entry (`compile.go` param-setup) a pointer param gets a
  borrowed origin and a slice param gets a borrowed origin, but a struct
  param got nothing. The tracker never recorded that a by-value struct
  param's slice/pointer fields are borrowed views of caller storage
  (the struct is copied; its slice/pointer fields still point at the
  caller's data). Harmless for local-escape checks (a param's fields are
  never *local*); load-bearing for "does the return alias this param".
  **DONE** — `seedStructParamFieldProvenance` seeds each slice/pointer
  field (recursing into struct-typed fields) with a borrowed origin rooted
  at the param name. Verified green; no behavior change to existing checks.

- **Gap 2 — call results are unmodeled (this *is* the feature).**
  `return g(x)`, and equally the provenance of `var b = g(x)` (scalar or
  struct), require `g`'s summary. Populate the result's provenance from
  `g`'s real summary (replacing the `__callret` sentinel hack).

Everything else that looked like a separate struct corner case
(nested-literal-from-call, struct-copy-of-param, struct-through-call) is
**downstream** of Gap 1 / Gap 2 feeding *empty* provenance into the
existing path-keyed field machinery (`fieldPointers["b.inner.buf"]`,
`CopyFieldPointers`, `recordStructLiteralFieldFacts`), which already
handles nesting and copies. Seed the two gaps and the compositions fall
out — they are not independent gaps.

---

## The engine: one tracker, queried at returns

- **One implementation = `compileTop`.** A function's summary is computed
  by running the *real* `compileTop` over its body **to a discard writer**
  (bosc emits `.bs` *text*, not an OFile, so output is just discardable
  text; side effects like `NeedVtable` are idempotent). Same transitions
  as live codegen ⇒ the summary cannot diverge from what codegen sees.

- **Summary = a byproduct of the real `Return` handling.** At each
  `return X`, read `X`'s provenance from the (now-complete) tracker — using
  the *same* `pointerExprForAST` / field-provenance (`EscapingFieldOrigins`)
  the escape check already uses — and record which borrowed-param origins
  it reaches → the per-slot param-index set. No `classify*`/`thread*`
  walk. Works uniformly for all shapes because tracker state is complete
  (Gap 1 + Gap 2). `aliasRootSymbol`/per-shape classification is **deleted**.

- **Slot index ≡ `Return.AnonFields[s]`** (multi-return), as in the proposal.

---

## The driver: dependency order + fail-fast (NOT demand-driven-with-suppression)

Earlier framing (demand-driven; compile callee-to-discard on every call;
suppress the analysis run's errors) was **wrong** — flagged by the user:
suppressing a dependency's real borrow error is pointless (the program
fails anyway) and unsound to build on (a function that's actually invalid
gives an untrustworthy tracker state). Corrected design:

- **Build the call graph** from the ASTs (walk each function body for
  `Funcall` nodes — all ASTs exist before any `Compile`). Condense to
  **SCCs** (Tarjan). Process SCCs in **reverse-topological order**
  (callees before callers).

- **Acyclic function ⇒ compiled exactly once.** Its summary falls out of
  its single real compilation; by the time a caller is compiled, its
  callees' summaries already exist. Nothing thrown away.

- **Cycle (SCC) ⇒ throwaway summary fixpoint, then real compile.** You
  can't order an SCC's members, so compute their summaries by **fixpoint
  iteration**: start each at ∅, re-run each member's analysis using the
  SCC's current summaries to resolve intra-SCC calls, grow monotonically
  over the finite param-index lattice to convergence (terminates: bounded
  by #params, monotone). Then compile the members for real with summaries
  final.

- **Fail-fast, no error suppression.** The analysis run *is* the real
  borrow check; an invalid dependency reports its error there and
  compilation stops. Because we stop on the first error, a valid function
  errors in neither run and an invalid one errors on the first compile and
  never reaches the second — so **errors never double-report; no
  error-gating machinery is needed.**

- **The one principled deferral:** inside an SCC fixpoint, *checks* (not
  errors) are held until the summaries converge — running a borrow check
  against a half-converged summary is meaningless. After convergence the
  real check runs (during the members' real compile) and reports any real
  error. This is "don't check with incomplete facts," not "limp past a
  known failure."

### Divergence-freedom

There is exactly one transition implementation (`compileTop`) walking one
AST, with final summaries by the time anything is checked or emitted ⇒
the analysis state and the codegen state are identical by construction.
(Today, codegen does re-touch the tracker for emission-coupled decisions —
e.g. `markMovedIfOwnedSource` emits a `mov name 0` null-out as a
consequence of a move at `compile.go:~1613`. That's fine: same
transitions, final summaries. A future cleanup could have the analysis
pass *annotate* the AST/IR with emission decisions so codegen recomputes
nothing — strictly more infra, deferred.)

### Worked cycle (must converge)

```
fn a(x *mut i64) *mut i64 { if (*x < 10) { return b(x) } return x }
fn b(x *mut i64) *mut i64 { *x = *x - 1; return a(x) }
```
SCC {a,b}, fixpoint from ∅:
```
iter1: a: b(x)→(b=∅)→∅ ; x→{0}  ⇒ a={0}      b: a(x)→(a=∅)→∅  ⇒ b=∅
iter2: a:{0}                      b: a(x)→(a={0})→{0}            ⇒ b={0}
iter3: a: b(x)→(b={0})→{0} ; x→{0} ⇒ a={0}    b:{0}   — stable —
```
`summary(a) = summary(b) = {param 0}` ✓ (both ultimately return `x`).
The old conservative "on-cycle alias everything" shortcut is replaced by
this fixpoint.

---

## What gets deleted vs kept

**Delete:** `retalias.go`'s `classify*` / `thread*` walk, `aliasRootSymbol`,
the `__callret` sentinel + `recordStructReturnCallFieldFacts`-as-hack
(call-result provenance becomes summary-driven), the conservative
self-alias cycle shortcut.

**Keep:** `FuncDecl.ReturnAliases [][]int` + `AliasesComputed`, `Function.
ReturnAliases` + `.bs`/`.bo` serialization, the `retaliases` bas
directive, `bdump` printing, the importer attaching the imported fact
(+ `SrcFile`/`SrcLine` for the diagnostic), the interface-coercion guard
(`validateInterfaceCoercion`) + directed diagnostic, the join-origin
branch-merge machinery in `flow/state.go`, and the **entire accumulated
regression suite** (`cmd/bosc/tests/retalias_*`, the Go-unit
`TestReturnAliasInference` / `TestInterfaceCoercionGuardDiagnostic`, the
`testpkgs/retalias_pkg` cross-package fixture, the bas `retaliases_test`)
— it encodes every soundness case found across the review rounds and is
the safety net for the rebuild.

---

## Task status

1. **Gap 1: seed struct/array param field provenance** — DONE (in checkpoint).
2. **Analysis-mode driver: run real borrow analysis standalone** — in progress.
   Factor the `FuncDecl` case into "set up params + run body" (callable
   to-discard) vs. directive emission; instrument `Return` to accumulate
   the summary into a context-held accumulator when analysing.
3. **Summary = query tracker provenance at returns** — replaces the
   classify walk; delete the approximation.
4. **Interprocedural summaries: dependency-order driver + SCC fixpoint** —
   call-graph build, Tarjan SCC, reverse-topo processing, fixpoint for
   cycles, Gap 2 call-result provenance from summaries.
5. **Checks vs emission** — with fail-fast + dependency order, no
   error-gating needed; only the in-fixpoint check deferral. (The
   annotated-IR endpoint where codegen recomputes nothing is a documented
   follow-up, not required.)
6. **Verify** — full bosc+bas+go green; all known holes correct on the new
   engine (reassignment, branch-merge, struct-through-call, struct-param
   return, nested-literal-from-call, the a/b cycle); guard + transport +
   bdump intact; re-run the correctness + completeness adversarial
   reviewers to convergence.

## Verification baselines

Checkpoint suites: **bosc 487, bas 41, Go units** — all green. Run with
`cd cmd/bosc && rm -f bosc && /home/kjn/go/bin/mmk test` (≈ a few min),
`cd cmd/bas && rm -f bas && /home/kjn/go/bin/mmk test`, `go test ./...`.
Keep this green at every step.
