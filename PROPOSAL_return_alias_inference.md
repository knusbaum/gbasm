# Proposal: Inferred Return-Parameter Aliasing

## Status

Draft. Not implemented. Independent of the other in-flight proposals,
but motivated by them: a `fmt.Builder` constructor and the canonical
`fn first_word(s byte[]) byte[]` sub-slice pattern are both rejected
today, and both are unblocked by this change.

## Summary

Today the borrow checker rejects any function that returns a borrowed
parameter, or an aggregate containing one:

```
fn first_word(s byte[]) byte[] {
    return s[0:1]                      // ✗ "Borrowed slice escapes through return"
}

fn new_builder(buf mut byte[]) Builder {
    return Builder{buf: buf, pos: 0}   // ✗ "field buf contains a slice into
                                        //    local-scope storage"
}
```

The rejection is sound but far too conservative. The function body
*cannot* locally prove the returned alias is safe — but the **caller**
can, because the caller can see both the real lifetime of the argument
it passed and the lifetime of the slot it stores the result into.

This proposal makes the compiler **infer**, per function, which
parameters each return value may alias, record that fact, and
propagate it to call sites so the caller's existing flow tracking can
extend its borrow analysis across the call boundary. No user syntax:
the relationship is derived entirely from the function body, exactly
as the compiler already derives it when checking the body today.

After this change, both examples above compile, and a *caller* that
misuses the result — storing it somewhere longer-lived than the
argument — is rejected at the call site with the same escape
machinery that fires today.

## Motivation

The sub-slice-of-input pattern is foundational. Tokenizers, header
parsers, trimming functions, field splitters — all return a view into
their input:

```
fn trim(s byte[]) byte[]                 // returns a sub-slice of s
fn first_word(s byte[]) byte[]           // ditto
fn field(row byte[], n i64) byte[]       // nth column, a view into row
```

None of these are expressible today. The same restriction blocks
**constructors** for any BYOM (bring-your-own-memory) type:

```
fn new_builder(buf mut byte[]) fmt.Builder      // Builder borrows buf
```

which is the immediate trigger — the `fmt` package ships with
struct-literal-only Builder construction because no constructor
compiles.

The rejection lives in three sites, all consulting the same
escape-restricted predicate:

- direct slice return (`checkSliceEscape`, `cmd/bosc/compile.go:3619`),
- symbol-of-struct return (`CheckStructFieldEscape`, `cmd/bosc/compile.go:3627`),
- struct-literal return walk (`cmd/bosc/compile.go:3646`–`3678`).

All three reject when the returned value's origin is `OriginBorrowed`
(a parameter view) or `OriginLocal` (a local array). `OriginLocal`
genuinely must stay rejected — a local array's storage is gone after
the frame pops. `OriginBorrowed` is the over-conservative case: the
parameter's storage belongs to the caller and outlives the call.

## Goals

- Allow a function to return a borrowed parameter, or an aggregate
  whose slice/pointer fields borrow parameters, when every escaping
  origin traces back to a parameter (never to a local).
- Infer the return→parameter relationship from the body. No new
  syntax, no annotations.
- Record the inferred relationship per function, carry it through
  `.bs`/`.bo`, and surface it to cross-package callers.
- Propagate the relationship at call sites so the caller's flow
  tracking treats the result as a borrow of the corresponding
  argument — making caller-side misuse a caught error at the call
  site.
- Keep `OriginLocal` escapes rejected exactly as today.

## Non-goals

- **No user-facing syntax.** No `from`/`borrows`/lifetime annotations.
  This was explicitly considered and rejected in favor of inference.
  (If a future need arises — e.g. declaring intent at an API boundary
  for documentation — syntax can be layered on; the inferred fact is
  what it would have to agree with.)
- **No field-level precision in v1.** `new_builder(buf) Builder` infers
  "some slice/pointer field of the returned Builder may alias `buf`,"
  not "specifically `Builder.buf` aliases `buf`." The coarser fact is
  sound (it over-approximates aliasing); precision is a future
  refinement.
- **No through-pointer output aliasing.** The
  `fn init(b *mut Builder, buf mut byte[])` shape — where the alias is
  installed into `*b` rather than returned — is a different mechanism
  (the annotation would attach to a parameter, not the return) and is
  out of scope. The return-aliasing form covers the constructor case,
  which is what we actually need.
- **No relaxation for `owned` returns.** A function returning
  `owned mut byte[]` constructs new ownership; nothing aliases a
  parameter. `owned` returns are unaffected and never carry an
  inferred alias set.

- **No aliasing of variadic parameters in v1.** A variadic parameter
  (`args ...any`) is desugared to a caller-frame-synthesized slice
  whose lifetime is the call, not a normal borrowed parameter, and
  the positional param-index→argument-expression mapping that
  call-site propagation relies on breaks down for the packed trailing
  args. A return that aliases a variadic parameter is therefore
  rejected (treated as an un-recordable escape), not inferred. There
  is no real use case for returning a view into the variadic pack;
  see [Variadic parameters](#variadic-parameters).

## Background: empirical observations

Verified against `bosc` at commit `701545e`:

1. **Origin kinds already distinguish the two cases.**
   `cmd/bosc/flow/state.go:34`–`50` defines `OriginLocal` ("storage
   tied to a stack frame's binding... invalidated at scope exit") and
   `OriginBorrowed` ("a function parameter whose lifetime is the
   caller's, opaque to the callee... *not* invalidated at scope exit
   — the source outlives the borrower"). The comment on
   `OriginBorrowed` already states the exact asymmetry this proposal
   exploits: escape gates treat it like `OriginLocal` "for that
   purpose," but its storage genuinely outlives the frame.

2. **Parameters register `OriginBorrowed` at prologue.**
   `cmd/bosc/compile.go:2569`–`2582`: non-owned pointer params call
   `SetBorrowedBinding`; non-owned slice params call
   `NewBorrowedOrigin`. So by the time a return is checked, the
   binding's borrowed-from-parameter root is already in the flow
   state.

3. **The escape rejection is a single predicate.**
   `IsEscapeRestricted` (`cmd/bosc/flow/state.go:363`) returns true for
   both `OriginLocal` and `OriginBorrowed`. The three return-site
   checks (`compile.go:3619`, `:3627`, `:3646`–`3678`) all funnel
   through it. This proposal splits the predicate's two cases: local
   stays rejected, borrowed becomes *recorded*.

4. **Compilation already has the whole AST before codegen.** The
   pipeline is ToAST (builds every top-level decl's AST) then
   Compile (codegen). By the time any function body is lowered, every
   other function's body AST exists — so demand-driven inference can
   walk a callee's body on first reference.

5. **Function metadata round-trips as a type string.** bosc emits
   `function <name>` + `type fn(...) ret` into `.bs`; bas stores the
   raw string into `Function.Type` (`cmd/bas/main.go:985`); the `.bo`
   carries `Function.Type` (`function.go:736`); the importer parses it
   via `parseFuncType` (`cmd/bosc/ast.go:1120`–`1130`). The inferred
   alias set needs a transport alongside this — a new `.bs` directive
   and a new serialized `Function` field (see [.bo impact](#bs--bo-impact)).

6. **The import graph is a DAG.** bld rejects import cycles across
   packages, so a cross-package callee's alias set is always fully
   computed by the producing package before any consumer reads it.
   Cycles can only occur *within* a single compilation unit (direct
   or mutual recursion), which the inference handles with a
   conservative SCC rule (see [Recursion and cycles](#recursion-and-cycles)).

## Design

### The inferred fact

For each function, the compiler computes a **return alias set**: for
each return slot (functions may return multiple values), the set of
parameter indices that slot may alias.

```
ReturnAliases [][]int   // ReturnAliases[slot] = sorted param indices
```

Examples:

```
fn first_word(s byte[]) byte[]
    // ReturnAliases = [[0]]            slot 0 may alias param 0 (s)

fn pick(a byte[], b byte[], use_a bool) byte[]
    // ReturnAliases = [[0, 1]]         slot 0 may alias param 0 or 1

fn new_builder(buf mut byte[]) Builder
    // ReturnAliases = [[0]]            the returned struct borrows param 0

fn split(s byte[]) byte[], byte[]
    // ReturnAliases = [[0], [0]]       both slots view s

fn copy(dst mut byte[], src byte[]) i64
    // ReturnAliases = [[]]             slot 0 (a count) aliases nothing

fn read(...) i64, error
    // ReturnAliases = [[], []]         neither slot is a borrow
```

A function whose every return slot has an empty alias set is exactly a
function under today's rules — no behavioral change for the
overwhelming majority of existing code.

### Inference: demand-driven, memoized, cycle-safe

The inference is a memoized function over functions:

```
alias_set(f):
    if cached(f):                return cache[f]
    if f in in_progress:                          # cycle (recursion)
        return conservative_self_alias(f)         # see Recursion below
    in_progress.add(f)

    result = [ empty set per return slot ]
    for each `return e_0, e_1, ..., e_k` in f's body:
        for slot, e in enumerate(expr_list):
            # escaping_origins(e) yields one origin per escaping
            # alias in e. For a direct slice/pointer/aggregate return
            # those are e's own roots. For a return whose value is a
            # call `g(arg_0..arg_m)`, it expands: for each param j in
            # alias_set(g)[slot], it yields the origin of *arg_j* — so
            # a call is classified by the origins of the arguments that
            # flow into g's aliased return, NOT by a params-only
            # shortcut. This is what keeps the OriginLocal case from
            # leaking through the call branch.
            for origin in escaping_origins(e):
                case OriginLocal:
                    reject                          # genuine UB, unchanged
                case OriginBorrowed(param p):
                    result[slot].add(index_of(p))
                otherwise:
                    # not escape-restricted: OriginUnknown (globals,
                    # opaque pointers), OriginAllocated (heap). Outlives
                    # the frame; safe, records nothing. This mirrors
                    # IsEscapeRestricted, which today rejects only
                    # OriginLocal and OriginBorrowed.
                    pass

    cache[f] = result
    in_progress.remove(f)
    return result
```

The crucial point — and the spot a params-only shortcut would get
wrong — is that a **call argument tracing to a local is rejected, not
silently dropped**. `escaping_origins` of a `return g(arg_j)` resolves
each aliased argument to its *origin* and feeds it through the same
`OriginLocal → reject / OriginBorrowed → record` switch as a direct
return. So:

```
fn wrap(b byte[]) byte[] { return b[0:1] }   // alias_set = [[0]]

fn danger(s byte[]) byte[] {
    var local byte[16]
    return wrap(local[:])    // wrap's slot 0 aliases its param 0;
                             // the corresponding arg is local[:],
                             // whose origin is OriginLocal → REJECT
}
```

is rejected *in `danger`'s body*, because the local origin reaches a
returned slot through the call. A function that passes a borrowed
parameter instead records the index:

```
fn ok(s byte[]) byte[] {
    return wrap(s)           // arg is s (OriginBorrowed param 0) → record 0
}                            // alias_set(ok) = [[0]]
```

`escaping_origins(e)` reuses the existing machinery: `checkSliceEscape`'s
`pointerExprForAST` for direct slice/pointer returns, and
`CheckStructFieldEscape` / `walkStructLiteralSliceEscape` for aggregate
returns. Its only new responsibility is the call-expansion above —
mapping a callee's return-alias set onto the origins of the matching
argument expressions. The local/borrowed/global classification that
follows is unchanged from today's checks; only the
`OriginBorrowed → record` branch is new (today it rejects).

The recursion through `alias_set(g)` walks the call graph in dependency
order naturally — `A` returning `B(s)` asks for `alias_set(B)`, which
resolves `B` (recording `{B's param 0}`), then expands the call:
`B`'s slot 0 aliases its param 0, the matching argument is `A`'s `s`
(`OriginBorrowed` param 0), so `A` records `{0}`. Your motivating
example resolves in exactly this order — and an `A` that passed a local
instead of `s` would be rejected by the same expansion.

### Where it runs

Not a new top-level pass. The inference is a **memoized helper invoked
during the Compile pass**, on first need:

- When lowering a function body, the return-site checks now ask
  `alias_set(this_function)` instead of rejecting outright. That
  triggers inference of this function (which may recurse into
  callees).
- When lowering a *call site*, the caller asks `alias_set(callee)` to
  decide how to propagate origins onto the result binding.

Because every function body's AST exists before codegen (observation 4),
the helper can always walk a not-yet-lowered callee's body. The result
is cached on the `FuncDecl`, so each function is inferred once
regardless of how many call sites reference it.

For **cross-package** callees, `alias_set` short-circuits to the
deserialized fact from the `.bo` — no body walk, because the imported
`FuncDecl` carries `ReturnAliases` directly (observation 6 guarantees
it is already computed).

### Caller-side propagation

At a call site `const r = g(x, y)` where `alias_set(g) = [[0]]` (slot 0
aliases param 0):

1. The caller already computes origins for the argument expressions
   (`x`, `y`) — that's how it type-checks passing them.
2. For each return slot `s` and each param index `p` in
   `ReturnAliases[s]`, the result binding's slot-`s` origin is set to
   the **borrowed origin of argument `p`**. If multiple params are
   listed (`pick` returns `[[0,1]]`), the result takes the *union* —
   the most conservative live origin among them. If any contributing
   argument is itself `OriginLocal` *in the caller's frame*, the
   result becomes escape-restricted in the caller, exactly as if the
   caller had written the sub-slice inline.

After propagation, `r` has the same flow facts it would have if the
borrow had been produced locally. The caller's existing return-site
and assignment-site escape checks then fire naturally:

```
fn do_something(s byte[]) byte[] {
    const t = tail(s)     // t inherits s's borrowed origin
    return t              // OK iff s is itself returnable — i.e. s is a
                          // param, so do_something infers ReturnAliases=[[0]]
}

fn oops() byte[] {
    var local byte[16]
    return tail(local[:]) // local[:] is OriginLocal; tail's result
                          // inherits it; return rejected — caught at the
                          // call/return site, where the lifetime is known
}
```

The lifetime evidence sits exactly where it can be evaluated: the
caller's frame, where both the argument's true origin and the result's
destination are visible.

### Recursion and cycles

Direct or mutual recursion creates a cycle in `alias_set`'s graph. The
`in_progress` guard catches re-entry and returns a **conservative
self-alias**: the recursive call's result is assumed to alias *every
argument position* of the recursive call. Those argument positions are
then classified by the *same* origin switch as everything else — a
recursive-call argument tracing to a local is rejected, a borrowed
parameter is recorded, a global/heap origin records nothing. The
"assume aliases every argument" is the over-approximation; the
local-reject is *not* relaxed inside a cycle.

```
fn weird(s byte[], t byte[]) byte[] {
    if (cond) { return weird(t, s) }   # cycle: result assumed to alias
                                        # both arg positions; args t,s are
                                        # OriginBorrowed params 1,0 → {0,1}
    return s[0:1]                       # direct: aliases {0}
}
# ReturnAliases = [[0, 1]]   (union of the two paths)

fn weird_local(s byte[]) byte[] {
    var buf byte[8]
    if (cond) { return weird_local(buf[:]) }  # cycle arg buf[:] is
                                               # OriginLocal → REJECT
    return s[0:1]
}
# rejected — a local reaches a returned slot through the recursive call
```

This over-approximates the *borrowed* set (never under-counts
aliasing, so it is sound) while keeping the local-escape rejection
exact, and it avoids a real fixpoint pass.

In practice, recursive functions that return parameter aliases are
rare, and the over-approximation only ever *widens* the alias set —
making the caller slightly more conservative, never unsound. If a real
program is hurt by the imprecision, the upgrade is localized: compute
strongly-connected components of the call graph and run a small
fixpoint within each SCC. That changes only the cycle branch of
`alias_set`, not the architecture.

### Variadic parameters

`ReturnAliases` is positional: slot `s` aliases formal parameter index
`p`, and call-site propagation maps index `p` to the `p`-th argument
expression. A variadic parameter breaks both halves of that:

- The trailing variadic args are packed into a single
  caller-frame-synthesized `any[]` slice (per the variadics proposal),
  so there is no one-to-one param-index→argument-expression mapping
  for them.
- A returned view *into* a variadic parameter aliases the synthesized
  args array, whose lifetime is the call — a different lifetime
  category than a normal borrowed parameter, which is the *caller's*
  storage that predates and outlives the call.

v1 sidesteps this entirely: **a return that escapes via a variadic
parameter is rejected, not recorded.** The inference classifies the
variadic parameter's origin as un-recordable (it is neither a clean
borrowed-caller-storage origin nor a local — it is the ephemeral args
pack), and a returned alias of it is a hard error like a local escape.
Non-variadic parameters of a variadic function are unaffected — only
the variadic parameter itself is excluded. There is no known use case
for returning a view into the variadic pack, so this costs nothing in
practice and removes a soundness hazard.

### What stays rejected

- Any return whose origin is `OriginLocal` (a local fixed array or a
  local value binding's address). The storage is gone after the frame
  pops; no amount of caller knowledge helps. Same message as today.
- A return mixing a local origin into an otherwise-borrowed path. If
  *any* path to a returned slot is `OriginLocal`, that slot's return
  is rejected — the inference records borrowed params but a local
  origin is a hard error, not a recordable fact. This holds whether
  the local reaches the slot directly, through a call argument, or
  through a recursive-call argument.
- A return aliasing a **variadic** parameter (see above).

## `.bs` / `.bo` impact

The inferred set must travel bosc → `.bs` → bas → `.bo` → importing
bosc.

### New `.bs` directive

Inside a `function` block, alongside the existing `type fn(...)` line,
bosc emits a `retaliases` directive when any slot has a non-empty set:

```
function first_word
    type fn(byte[]) byte[]
    retaliases 0: 0
    ...

function pick
    type fn(byte[], byte[], bool) byte[]
    retaliases 0: 0 1
    ...

function split
    type fn(byte[]) byte[], byte[]
    retaliases 0: 0
    retaliases 1: 0
    ...
```

Format: `retaliases <slot>: <param-index>...`. Absent directive ⇒ all
slots alias nothing (the common case; no emission overhead for
ordinary functions). bas parses it the same way it parses `type`
(`cmd/bas/main.go:982`) — a prefix match storing into a new field.

### New `Function` field

`function.go:732`'s `Function` gains:

```
ReturnAliases [][]int   // serialized; empty/nil for ordinary functions
```

`writeFunction` / `readFunction` (`bwrite.go:362` / `:436`) serialize
it. For the common all-empty case the on-disk cost is a single
zero-count varint.

### Importer

`cmd/bosc/ast.go:1116`–`1131`, where imported funcs are reconstructed
from `o.Funcs`, attaches `ReturnAliases` onto the rebuilt `FuncDecl`
so cross-package `alias_set` short-circuits to the imported fact.

### `bdump`

`bdump` prints the `ReturnAliases` of each function so cross-package
aliasing can be inspected when debugging a borrow error that crosses a
package boundary.

## Implementation impact

- **`cmd/bosc/flow/state.go`**: no new origin kind. Possibly a helper
  to enumerate the escaping origins of an expression as
  `(kind, paramIndex)` pairs rather than a bool, so the inference can
  record rather than reject.
- **`cmd/bosc/compile.go`**:
  - The `alias_set` memoized inference helper + `in_progress` cycle
    guard.
  - Refactor the three return-site checks (`:3619`, `:3627`,
    `:3646`–`3678`) to call the inference's per-origin classifier:
    local → reject, borrowed → record.
  - Call-site propagation: after `setupArgs` / result binding, apply
    the callee's `ReturnAliases` to the result's flow origins
    (`Funcall` lowering, around `:1290` / `:2637`).
  - Emit the `retaliases` directive in the function preamble next to
    the `type` line.
- **`cmd/bas/main.go`**: parse `retaliases` (mirrors the `type`
  handler at `:982`) into the new `Function` field.
- **`function.go` / `bwrite.go`**: the `ReturnAliases` field +
  serialization.
- **`cmd/bosc/ast.go`**: `FuncDecl` gains `ReturnAliases [][]int` and a
  memoization flag; importer attaches the imported fact.
- **`cmd/bdump`**: print `ReturnAliases`.

Rough scope: 700–1000 lines across compiler, assembler, object format,
and tests. The inference helper and the call-site propagation are the
substantive pieces; the rest is plumbing.

## Tests

Integration tests under `cmd/bosc/tests/`. The six
`slice_*_local_array_*_err_test.bos` files that currently document the
unsound shapes (per the fmt proposal's Open Question 0) stay as-is —
local-array escapes remain rejected. New tests:

Positive (now compile + run correctly):
- `retalias_subslice_test.bos` — `fn first_word(s byte[]) byte[]`
  returning `s[0:k]`; caller uses the view, prints it.
- `retalias_passthrough_test.bos` — `fn id(s byte[]) byte[] { return s }`.
- `retalias_struct_ctor_test.bos` — `fn new_builder(buf mut byte[])
  Builder`; the returned Builder writes through `buf`, caller sees the
  bytes.
- `retalias_two_hop_test.bos` — `fn A(s byte[]) byte[] { return B(s) }`
  where `B` returns a sub-slice; verifies transitive inference.
- `retalias_multi_param_test.bos` — `fn pick(a, b, use_a) byte[]`;
  result usable, aliases either.
- `retalias_multi_return_test.bos` — `fn split(s) byte[], byte[]`;
  per-slot inference.
- `retalias_recursive_test.bos` — a recursive function returning a
  param alias; conservative set still permits correct caller use.
- `retalias_cross_package_test.bos` — callee in package A returns a
  param sub-slice; caller in package B uses it correctly.

Negative (still rejected, now at the *caller*):
- `retalias_caller_escapes_local_err_test.bos` — caller passes
  `local[:]` to a sub-slice function and returns the result; rejected
  at the caller's return.
- `retalias_caller_stores_global_err_test.bos` — caller assigns a
  borrowed result into a global; rejected at the assignment.
- `retalias_local_in_callee_err_test.bos` — a function that tries to
  return a sub-slice of its *own* local array; still rejected in the
  callee (local origin, not borrowed).
- `retalias_call_arg_local_err_test.bos` — the soundness regression:
  `fn wrap(b byte[]) byte[] { return b[0:1] }` and `fn danger(s byte[])
  byte[] { var l byte[16]; return wrap(l[:]) }`. The local reaches a
  returned slot *through the call*; must be rejected in `danger`'s
  body. This is the case a params-only inference shortcut would let
  through.
- `retalias_recursive_call_arg_local_err_test.bos` — same hazard via a
  recursive-call argument; rejected.
- `retalias_variadic_param_err_test.bos` — a variadic function
  returning a view into its variadic parameter; rejected (variadic
  parameters are never aliasable in v1).

## Open questions

### 1. Field-level alias precision

v1 infers "the returned struct aliases param `p`" without saying which
field. A struct returned from a constructor that borrows one param into
one field and allocates another field independently would conservatively
mark the whole struct as borrowing. Is the coarseness ever a real
problem? The precise form tracks per-field provenance through the
return (the `fieldPointers` path machinery already exists in
`flow/state.go`); it's more bookkeeping in the inference and a richer
`.bo` encoding. Deferred unless a concrete case bites.

### 2. SCC fixpoint vs conservative self-alias

The cycle rule over-approximates recursive functions. Worth measuring
whether any real recursive code returns parameter aliases and suffers.
If not, the conservative rule stands; if so, an SCC-local fixpoint is a
localized upgrade.

### 3. Interaction with `owned` parameters

An `owned` parameter is consumed, not borrowed; returning a view of it
is a different lifetime story (the callee took ownership). v1 treats
`owned`-param returns as today (the param isn't `OriginBorrowed`, so no
alias is recorded; existing owned-return rules apply). Worth confirming
no pattern needs "return a borrowed view of an owned param" — if it
does, it's a separate analysis.

### 4. Should the inferred fact be visible in `bdoc`?

Cross-package callers benefit from knowing "this return borrows that
argument" as documentation, not just as a checker fact. Surfacing
`ReturnAliases` in `bdoc`'s rendered signature (e.g. annotating the
return) would communicate the lifetime contract without introducing
source syntax. Low priority, but a natural place for the inferred fact
to become human-visible.

## Future extensions

- **Field-level alias precision** (Open Question 1).
- **SCC fixpoint** for recursive precision (Open Question 2).
- **Optional surface syntax** as an *assertion* that the compiler
  checks against the inferred fact — for API authors who want the
  lifetime contract written at the boundary. Only worth it if the
  inferred-only model proves to hide too much at package boundaries.
- **Through-pointer output aliasing** — inferring that
  `fn init(b *mut T, buf byte[])` installs `buf` into `*b`, for the
  init-in-place constructor shape. A distinct analysis (alias flows
  into a parameter, not the return); deferred.
