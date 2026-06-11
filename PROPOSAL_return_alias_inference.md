# Proposal: Inferred Return-Parameter Aliasing

## Status

Implemented. Independent of the other in-flight proposals, but motivated
by them: a `fmt.Builder` constructor and the canonical
`fn first_word(s byte[]) byte[]` sub-slice pattern were both rejected
before this change, and both are unblocked by it.

The interface-coercion guard (option (a)) is deliberately strict: a
concrete type with **any** borrow-returning method cannot be coerced to
any interface — not even `any`. This closed a pre-existing soundness gap
(borrowed-field returns through a pointer receiver were silently accepted
at coercion) but it also **broke** `fmt`'s previously-shipped
borrowed-field `stringer` (`string()` returned a view of `self`). That
breakage is intended and is reconciled in
[Interface-method dispatch](#interface-method-dispatch): `fmt`'s
`stringer` contract now requires `string()` to return bytes that outlive
the receiver (a static/`.rodata` slice or caller-owned storage), and the
borrowed-view case moves to `Formatter`. Lifting the restriction for
borrowed-field stringers under a checked contract is **option (b)**,
documented future work.

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
*compiles* (the constructor body is rejected by the escape check,
regardless of where it is called). `fmt` is a single file
(`runtime/fmt/fmt.bos`), so `new_builder` would live alongside
`Builder` and be consumed by **cross-package** user code — the path
this proposal's `.bo`-carried alias fact supports directly.

A note on scope: this change makes the constructor *body* compile and
makes *cross-package* callers able to use it safely. It does **not**
enable a *same-package, different-file* caller — bosc compiles one
`.bos` per process with a fresh context, so a call to a function
defined in a sibling source file fails today with `No such function`
(`compile.go:2751`) and is unaffected here (see Background
observation 4). That limitation is pre-existing and orthogonal; it does
not touch the `fmt.Builder` case, which is single-file plus
cross-package consumption.

The rejection lives in **five** sites. Three funnel through the same
escape-restricted predicate (`IsEscapeRestricted`); the other two are
independent borrowed/local-origin gates that must be refactored in
lockstep or the feature is unsound through the paths they cover:

- direct slice return (`checkSliceEscape`, `cmd/bosc/compile.go:3619`),
- symbol-of-struct return (`CheckStructFieldEscape`, `cmd/bosc/compile.go:3627`),
- struct-literal return walk (`cmd/bosc/compile.go:3646`–`3678`).
- **pointer return** (`compile.go:3613`–`3616`): for any return whose
  resolved type has `Indirection > 0`, the `Return` case calls
  `checkBorrowedPointerDoesNotEscape` (`compile.go:699`–`702`) and
  `checkLocalOriginDoesNotEscape` (`compile.go:705`–`713`).
  `checkBorrowedPointerDoesNotEscape` rejects *any* borrowed pointer
  via `borrowedPointerExpr`, **independent of `IsEscapeRestricted`**.
  This is the path `fn f(p *T) *T { return p }` hits — a bare pointer
  passthrough, which the slice-only sites never see. It must become a
  recording site, not a hard reject, for borrowed pointer returns.
- **interface-coerced pointer return** (`compile.go:3689`–`3697`): when
  a concrete pointer is returned as an interface (`shouldCoerceToInterface`
  and `valType.Indirection > 0`), a separate check rejects only
  `OriginLocal` today (`compile.go:3694`). It does **not** currently
  reject borrowed pointers — but once borrowed pointers become
  recordable, this site must *participate in recording*, or
  `return someIface` built from a borrowed param produces no alias
  record and the borrow escapes the inference silently.

All five reject (or, for the interface site, will need to record) when
the returned value's origin is `OriginBorrowed` (a parameter view) or
`OriginLocal` (a local array). `OriginLocal` genuinely must stay
rejected — a local array's storage is gone after the frame pops.
`OriginBorrowed` is the over-conservative case: the parameter's
storage belongs to the caller and outlives the call.

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
  The relationship is fully inferred; syntax was explicitly considered
  and rejected.
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

- **No interface-dispatched return aliasing in v1.** Return-alias
  inference runs normally on every method body — a concrete
  `v.method()` that returns a borrowed receiver field works. But a
  concrete type that has **any** method (whether or not the interface
  requires it) with a non-empty inferred `ReturnAliases` may **not be
  coerced to *any* interface — including the zero-method `any`**; the
  coercion is rejected at the satisfaction chokepoint. This coarse
  exclusion (option (a)) denies a borrowing-method type ever becoming
  an interface value at all, which is what closes the runtime-dispatch
  hole: a value can only enter the interface world through a static
  coercion, so blocking that entry means no borrowing-method itab can
  ever be minted at runtime by `_iface.assert_to` (interface assertion
  `x.(B)` / type-switch). Interface-method dispatch results therefore
  stay origin-less (`UnknownPointer`) exactly as today, and the
  soundness hole the vtable path would otherwise open is closed by
  construction. This is not a goal change: the motivating cases
  (`first_word`, `new_builder`) are free functions, concrete BYOM
  methods keep working under concrete dispatch, and the only excluded
  shape is letting a borrowing-method type become an interface value.
  Carrying `ReturnAliases` on `InterfaceMethodSig` plus a per-implementer
  conformance check (option (b)) is a future extension that would lift
  the exclusion under a checked contract. See
  [Interface-method dispatch](#interface-method-dispatch).

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

Verified against `bosc` at commit `701545e`; the additional site/line
references added during review were re-verified at `80ba653`:

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

3. **The slice/aggregate escape rejection is a single predicate.**
   `IsEscapeRestricted` (`cmd/bosc/flow/state.go:363`) returns true for
   both `OriginLocal` and `OriginBorrowed`. Three of the five
   return-site checks (`compile.go:3619`, `:3627`, `:3646`–`3678`)
   funnel through it. This proposal splits the predicate's two cases:
   local stays rejected, borrowed becomes *recorded*. The two
   pointer-shaped sites (`:3613`–`3616` direct pointer return,
   `:3689`–`3697` interface-coerced pointer return) do **not** route
   through `IsEscapeRestricted` and must be refactored separately; see
   the five-site enumeration above.

4. **Within one compilation unit, the whole AST exists before
   codegen.** bosc compiles **one `.bos` file per process**
   (`boson.mmk:172`–`177` invokes `bosc` once per source file;
   `main.go:241` builds a fresh `NewContext()` per file). Within that
   single file the pipeline is ToAST (builds every top-level decl's AST)
   then Compile (codegen), so by the time any function body in the file
   is lowered, every *same-file* function's body AST exists — and
   demand-driven inference can walk a same-file callee's body on first
   reference. This is the only scope in which body-walking inference
   runs. **Same-package callees defined in a *different* file are not
   visible** — they are in neither `c.funcs` nor `c.imports`, and a call
   to one already fails today with `No such function` (`compile.go:2751`;
   verified empirically). So cross-file same-package inference is out of
   scope, but it is a *pre-existing* limitation: there is no code path
   that compiles such a call today, hence nothing for this proposal to
   make sound and nothing it regresses. Cross-*package* callees do not
   rely on this observation at all — their alias fact arrives
   pre-computed via the `.bo` (observation 6), and the demand-driven
   helper short-circuits to it without any body walk.

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

#### Index space: parameters and slots are positions, defined precisely

Both halves of `ReturnAliases` are positional, and both ends — the
inference walking a body and the caller propagating onto a result —
must agree on the exact numbering, or a mismatch silently attributes a
borrow to the wrong argument.

- **Param index `p`** indexes `FuncDecl.Args` (`ast.go:2238`) in
  declaration order. **For methods, `Args[0]` is the receiver.** A
  method `fn (s *S) field() byte[] { return s.buf }` has the receiver
  at param index 0, so it infers `ReturnAliases = [[0]]`. This is not a
  special case in the machinery: `compileConcreteMethodCall`
  (`compile.go:5159`–`5209`) desugars `v.method(args)` into a synthetic
  `Funcall` whose `Args` are `[receiver, args...]` (`:5183`–`5185`)
  before delegating to the ordinary call path, so at the desugared call
  site the receiver already occupies positional `Args[0]`. The same
  `Args[0] == receiver` convention holds on the decl side
  (`m.Args[0].Type` is the receiver type, e.g. `compile.go:5171`).
  Caller-side propagation therefore maps `ReturnAliases` param indices
  onto the *desugared* argument list, where index 0 is the receiver
  expression. The BYOM method-returns-receiver-field case is thus
  handled by the same positional mapping for **concrete** dispatch — it
  needs no separate mechanism, only the stated convention.

  **Interface dispatch is different and is scope-excluded in v1.**
  Unlike `compileConcreteMethodCall`, the interface-method path
  (`compileInterfaceMethodCall`, `compile.go:5218`+) does **not** build
  a synthetic `Funcall`: it loads the interface variable's data word
  into `rdi` and maps `callNode.Args` onto `isig.Params[1:]`
  (`compile.go:5236`–`5238`, `:5290`). `isig.Params[0]` is the receiver,
  but there is no desugared positional argument list for caller-side
  propagation to land a receiver-alias (param index 0) onto, and
  `InterfaceMethodSig` carries no `ReturnAliases`. A borrowed-returning
  method dispatched through a vtable would have its result fall to
  `pointerExprForAST`'s default `UnknownPointer()` (`compile.go:1311`)
  — "safe, records nothing" — so the borrow would escape uncaught.

  This hole is not confined to direct `compileInterfaceMethodCall`
  dispatch. Interface-to-interface assertion (`x.(B)`,
  `compileInterfaceAssert` at `compile.go:5694`, calling
  `_iface.assert_to` at `:5725`) and interface type-switch cases
  (`compileTypeSwitchIfaceCase` at `:5865`) mint the result interface's
  vtable **at runtime** from the source concrete type's *full* method
  table — they do not consult the declared interface's required-method
  subset. So even a guard that only inspected the *interface-required*
  methods of a coercion would be **vacuous for `any`** (which requires
  zero methods): a borrowing-method type could coerce to `any` freely,
  then `a.(B).field()` would dispatch the borrowing method through a
  runtime-minted itab and the result would be tracked as
  `UnknownPointer` — borrow escapes uncaught.

  v1 closes this not by excluding method bodies from inference (the
  concrete BYOM case above *needs* the body inference) but by
  forbidding a type that has **any** method with a non-empty
  `ReturnAliases` from being coerced to **any** interface at all
  (option (a), the coarse exclusion). Because a value can only enter
  the interface world through a static coercion — assertion and
  type-switch operate on values that are *already* interfaces — denying
  that entry is sufficient: if no borrowing-method type ever becomes an
  interface, none can be laundered into a dispatchable borrowing-method
  itab. See [Interface-method dispatch](#interface-method-dispatch).

- **Slot index `s`** indexes the return values in declaration order.
  Single-value returns have exactly slot 0. Multi-value returns are
  *not* a list of separate types in the AST: `FuncDecl.Return` is a
  **single** `ASTType` carrying the value types in its `AnonFields`
  with `MultiReturn = true` (field definitions at `ast.go:1407`–`1408`;
  the `AnonFields`+`MultiReturn=true` value is synthesized from the
  `<multiretu>` sentinel in `mkTypename` at `:1991`–`:1997`).
  (`FuncDecl.Args` and `FuncDecl.Return` are the field decls at
  `:2238`/`:2239`; those are not the synthesis site.)
  **Slot `s` ≡ `Return.AnonFields[s]`**, in the order
  `parseFuncType` (`ast.go:1068`) reconstructs them on import — which
  is the source declaration order. The inference's `enumerate(expr_list)`
  over a `return e_0, ..., e_k` must align position-for-position with
  this `AnonFields` order; the `retaliases <slot>: ...` directive's
  slot numbers index the same sequence. Any divergence between the
  return-expression position, the `AnonFields` index, and the directive
  slot number is wrong-slot attribution and silent unsoundness, so the
  emit/parse/propagate paths all key off `AnonFields` index explicitly.

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

`escaping_origins(e)` reuses the existing classification machinery —
`checkSliceEscape`'s `pointerExprForAST` for direct slice/pointer
returns, and `CheckStructFieldEscape` / `walkStructLiteralSliceEscape`
for aggregate returns — but the **call-expansion is genuinely new
machinery, not a thin reuse.** Today `pointerExprForAST`'s `*Funcall`
branch (`compile.go:1290`–`1311`) recognizes only two call shapes — a
type-cast `T(expr)` (recurse into the cast argument, `:1299`–`:1304`)
and an owned-allocating call (`:1305`–`:1310`) — and **falls through
to `UnknownPointer()`** (`:1311`) for every other call, including a
borrowed-returning call. This case does not exercise today because a
borrowed-returning *callee* (`fn wrap(b byte[]) byte[] { return b[0:1] }`)
is itself rejected today — `Borrowed slice escapes through return`,
verified — so `return wrap(...)` is never reached: the callee never
compiles. **The feature makes the callee legal**, and the moment it
does, the call result becomes a path that *must* be classified.
`checkSliceEscape` returns early when `!ptr.KnownOrigin`
(`compile.go:725`–`727`), and `pointerExprForAST` produces exactly an
unknown origin for a call result (`:1311`); so absent new machinery the
call result would carry *no* origin and slip every return-site and
assignment-site check. The proposal therefore must add a *new*
`*Funcall` value case (or an inference-side equivalent) that:

- looks up `alias_set(callee)`,
- for each param index `p` in the callee's slot-0 alias set, resolves
  the matching argument expression `arg_p`'s own `PointerExpr`,
- synthesizes a `PointerExpr` for the call result that carries the
  propagated origin (single param → inherit `arg_p`'s origin; multiple
  → the union representation from
  [Caller-side propagation](#caller-side-propagation)),

and this synthesized `PointerExpr` is consumed in **two** places: the
inference walk (so `return g(arg)` classifies by `arg`'s origin) and
caller-side result binding (so a `const r = g(x)` result inherits the
borrow). The local/borrowed/global classification that *follows* a
resolved origin is unchanged from today; the new parts are (a) the
`OriginBorrowed → record` branch at the recording sites and (b) this
call-expansion that produces a non-`Unknown` origin for a
borrowed-returning call where today there is none. This is **not** a
pre-existing bug being closed — it cannot trigger today because the
borrowed-returning callee is rejected first — but it is the machinery
that keeps the feature *sound*: without it, once the callee is made
legal, a local reaching a returned slot *through a call* would go
uncaught. The `retalias_call_arg_local_err_test` negative test is what
verifies this new case fires: with both `wrap` and `danger` legal to
*parse*, `danger`'s `return wrap(local[:])` must be rejected in
`danger`'s body.

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

Because every *same-file* function body's AST exists before codegen
(observation 4), the helper can always walk a not-yet-lowered
same-file callee's body. The result is cached on the `FuncDecl`, so
each function is inferred once regardless of how many call sites
reference it. A callee that resolves through an import
(`c.imports[...]`) skips the body walk entirely and reads the
deserialized fact (next paragraph); a callee that resolves to neither
the current file's `c.funcs` nor an import does not type-check at all
today and is unaffected.

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
   the **borrowed origin of argument `p`**.

   When `ReturnAliases[s]` lists exactly one param (the common case —
   `first_word`, `new_builder`, the sub-slice pattern), the result
   binding simply inherits that argument's `PointerExpr` origin, and
   the caller's existing escape/deref machinery handles it verbatim.

   **Multi-param union representation.** `PointerExpr`
   (`flow/state.go:13`–`18`) carries a *single* `Origin`, so a slot
   that may alias more than one argument (`pick` → `[[0,1]]`) cannot be
   represented by simply picking one — picking the live one would
   under-report a dead alias, picking the dead one would under-report a
   live alias; both directions are unsound. The result binding must
   therefore be modeled as escape-restricted/invalidated if **any**
   contributing argument is escape-restricted/invalid, and as
   validly-borrowed only when **all** contributing arguments are.
   Concretely, multi-param propagation cannot reuse the single-`Origin`
   `PointerExpr` shape; it requires one of:
   - **(preferred) a small representation extension**: let the result
     binding carry a *set* of contributing origins (e.g. a new
     `[]Origin` alongside the scalar `Origin`, or a synthesized join
     origin whose `originInfo` is the conservative meet of its
     members' validities), so a later deref/escape check that consults
     the binding sees "restricted if any member is restricted, dead if
     any member is dead." This is the sound, precise option and is the
     one this proposal adopts; it is additive to `flow.State` and does
     not perturb the single-origin fast path.
   - a fallback that, for any multi-param slot, marks the result with a
     conservative *synthetic local-equivalent* origin unless every
     contributing argument is a clean borrowed param — i.e. degrade a
     genuinely-ambiguous multi-source result to "treat as
     escape-restricted in the caller." Sound but coarse; usable as a v1
     shortcut if the representation extension slips.

   Either way the invariant is fixed: **the result is treated as live
   only if every contributing argument is live; restricted if any is
   restricted.** If any contributing argument is itself `OriginLocal`
   *in the caller's frame*, the result becomes escape-restricted in the
   caller, exactly as if the caller had written the sub-slice inline.

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

### Interface-method dispatch

The concrete method-call path (`compileConcreteMethodCall`,
`compile.go:5159`–`5209`) desugars `v.method(args)` into a synthetic
`Funcall` with `Args = [receiver, args...]` and delegates to the
ordinary call path, so caller-side propagation maps `ReturnAliases`
indices onto a real positional argument list (index 0 = receiver). The
**interface** method-call path is structurally different and cannot
reuse that machinery:

- `compileInterfaceMethodCall` (`compile.go:5218`+) builds **no**
  synthetic `Funcall`. It loads the interface variable's data word
  (the receiver) into `rdi` directly (`:5273`) and maps
  `callNode.Args` — user args only — onto `isig.Params[1:]`
  (`:5236`–`5238`, `:5290`). There is no desugared positional argument
  list carrying the receiver at index 0 for propagation to target.
- `InterfaceMethodSig` (`ast.go:3043`) carries no `ReturnAliases`, and
  the fact is never installed on `isig`. Even if a dispatch-site
  argument list existed, there would be no per-interface contract to
  propagate, and independent implementers of the same interface could
  disagree about which arguments their returns borrow with nothing
  checked.
- The dispatched result is bound by a direct `move` from `rax`
  (`compile.go:5400`); the caller's `pointerExprForAST` sees an
  ordinary `*Funcall` whose callee is a method form, hits the default
  `UnknownPointer()` (`:1311`), and records nothing — so a caller that
  stored a borrowed dispatched result long-lived would have the borrow
  escape **uncaught**.

This is distinct from the **return-as-interface** site
(`compile.go:3689`–`3697`), where a normal function returns a borrowed
pointer *as* an interface value: that function's own `ReturnAliases`
propagates to its caller normally, and that site stays a recording
site (see the five-site enumeration). The hole is the more general
*vtable-dispatched call result*, where the borrow is reconstructed from
the data word with no statically-known source argument — and it is
reachable not only through `compileInterfaceMethodCall` but through any
**runtime-minted itab**: interface-to-interface assertion (`x.(B)`,
`compileInterfaceAssert` at `:5694` → `_iface.assert_to` at `:5725`)
and interface type-switch cases (`compileTypeSwitchIfaceCase` at
`:5865`) build the result interface's vtable at runtime from the source
concrete type's **full** method table, not from the declared
interface's required-method subset.

**Why a required-methods-only guard is unsound (the `any` leak).** The
`any` interface requires **zero** methods, so a guard that only
inspected the *interface-required* methods of a coercion would be
**vacuous** for `any`: a type with a borrowing method satisfies `any`'s
empty contract trivially. Once such a value is an `any`, a downstream
`a.(B).field()` asserts to a real interface `B`, mints `B`'s itab at
runtime from the concrete type's full method table — including the
borrowing `field()` — and dispatches it, with the result tracked as
`UnknownPointer` (`compile.go:1311`). The borrow escapes uncaught. The
required-methods framing therefore inspects exactly the wrong set: the
runtime never restricts itself to the coercion-time interface's
requirements.

**v1 closes the hole at the body's entry into the interface world, not
at the body itself.** Excluding interface-method *bodies* from
inference is wrong in both directions: a method body is compiled once
as a concrete function, and the BYOM concrete case
(`fn (s *S) field() byte[] { return s.buf }`, Index-space section)
*needs* that body to infer `ReturnAliases = [[0]]` so direct
`s.field()` works — excluding it regresses a committed goal; while
leaving the body to infer but only dropping dispatch-site propagation
leaves the borrow legal in the body and still escaping via a
runtime-minted itab.

Instead, the rule (**option (a), the coarse exclusion**) is on
**entering the interface world at all**: a concrete type that has
**any** method — required by the target interface or not — with a
non-empty inferred `ReturnAliases` may **not be coerced to *any*
interface, including the zero-method `any`**. The check lives once, in
`validateInterfaceCoercion` (`compile.go:216`–`255`) — the single
chokepoint that *every* static concrete→interface coercion funnels
through (struct-field init `:1530`, var-init `:2294`, assignment
`:3293`, return-as-interface `:3699`/`:3713`, argument coercion
`:4066`/`:5306`, slice-element `:5492`, and the `any`-packing /
interface-equality path via `compileAsInterfaceValue` `:4688`, plus the
variadic `any[]`-element packing at `:5492` — all via
`emitInterfaceFatPtr` → `validateInterfaceCoercion` at `:5941`). The
new clause is **independent of `TypeSatisfiesInterfaceAs`'s result**
(`:238`): regardless of which methods the target interface requires, it
enumerates the concrete type's **full** method set (via
`c.TypeMethodsFor(typeName)`, the accessor behind
`methodEntriesForType` at `typeinfo.go:229`) and demand-drives
`alias_set(method)` on each; if any has a non-empty `ReturnAliases`,
the coercion is rejected. The guard must run for the `any` target too —
that is the entire point, since `any`'s empty required set would
otherwise let a borrowing-method type through. v1 is coarse: the
coercion is rejected if *any* method of the type borrows, not only the
ones that happen to be interface-required or to return a borrow.

**Sufficiency.** A value can only *enter* the interface world through
one of these static coercion sites — interface assertion and
type-switch operate on values that are *already* interfaces
(`compileTypeAssert` requires `c.IsInterfaceType(srcT)` at `:5567`,
`compileTypeSwitch` at `:5773`; `compileAsInterfaceValue` only mints a
fat pointer for a *non*-interface source at `:4681`, otherwise reusing
the existing interface value). So if no borrowing-method type ever
becomes an interface value, no runtime itab can ever be minted for one,
and the assertion/type-switch laundering path has no source to launder.
Interface→interface **widening** (`var a any = someB`) is not a
counterexample: `shouldCoerceToInterface` is false for an interface
source (it gates on `!IsInterfaceType(srct)`), so widening copies the
already-minted itab 16 bytes rather than minting a new one — and that
source itab could only exist if its concrete type had already passed
this guard. Blocking the single static entry point is therefore
complete; there is no separate fix needed at the assertion,
type-switch, or widening sites. (`emitInterfaceFatPtr` is the sole
value→interface minting primitive: all nine call sites are in
`compile.go` and route through `validateInterfaceCoercion`;
`globals.go` and `typeinfo.go` mint no fat pointers — the latter emits
only the static typedesc/iface_desc tables.)

Three properties make this safe and non-disruptive:

- **One deliberate, sound breakage — borrowed-field stringers.** The
  claim "no existing coercion is newly rejected" is *not* accurate, and
  the honest statement matters: this rule rejects a borrowed-field
  stringer that compiled before. Concretely, `fmt`'s shipped
  `stringer` contract was *render me as a borrowed view of my own
  fields* (`string(p *planet) byte[] { return p.name }`), and four
  positive tests exercised exactly that shape
  (`fmt_print_v_stringer`, `fmt_stringer_user_type`,
  `fmt_stringer_preferred`, and the borrowed-field arm of
  `fmt_print_v_value_shape`). Those coercions are now rejected — and
  rejecting them is the *point*: a `string()` that returns a view of
  `self` produces bytes that dangle once the interface value (the
  `any` that `fmt.print` packs varargs into) outlives the receiver.
  This closed a **pre-existing soundness gap**: borrowed-field returns
  through a pointer receiver were silently accepted at coercion before
  return-alias inference existed, so the unsound shape shipped as a
  supported pattern. The breakage is therefore a *fix*, not a
  regression in the "broke working sound code" sense — but it does
  break source that compiled, so it must be called out, not glossed.
  `fmt`'s `stringer` contract is corrected accordingly: `string()`
  must return bytes that **outlive the receiver** (a static/`.rodata`
  slice or caller-owned storage), never a borrowed view of `self`; a
  type that needs to render a view of its own fields uses `Formatter`
  (which writes into a caller-provided writer, so nothing escapes).
  The positive `%v`/stringer path is still covered by a corrected
  non-borrowing test (`fmt_print_v_stringer_test`, `string()` returns a
  static slice), and the rejection is pinned by
  `fmt_stringer_borrowed_field_coerce_err_test`. Supporting
  borrowed-field stringers under a checked contract is **option (b)**
  (below), explicit future work — it would lift this restriction.
- **Genuinely-unaffected common case.** Apart from the borrowed-field
  stringer shape above, no existing coercion is newly rejected: any
  type all of whose methods return non-borrows (counts, errors, owned
  storage) has an all-empty `ReturnAliases` and coerces unchanged.
- **Cross-package works via the importer.** For an imported concrete
  type, each method's `ReturnAliases` is visible at the coercion site
  because the importer attaches it to the rebuilt `FuncDecl` (see
  [Importer](#importer)) — the same mechanism that makes the fact
  available to ordinary cross-package call propagation. The full-method
  enumeration walks the imported type's method `FuncDecl`s, each
  carrying its deserialized `ReturnAliases`.
- **Intra-file demands, never reads a raw cache.** The guard must call
  `alias_set(method)` on each of the type's methods — the
  demand-driven, memoized helper — not read a `ReturnAliases` field that
  may still be empty. Top-level decl order is arbitrary: a coercion site
  (`make_iface()`) may be lowered *before* the method body
  (`(s *S) field()`) it depends on, so reading an unpopulated cache would
  see an empty set, allow the coercion, and reopen the hole. Observation
  4 guarantees every same-file method body's AST exists before any
  codegen, so the guard can always trigger inference on a not-yet-lowered
  method, exactly as call-site propagation does for a not-yet-lowered
  callee; the result is then memoized.

The guard bites **only** types that have a borrowing method. A type all
of whose methods have an empty `ReturnAliases` (the overwhelming common
case — `fmt.Builder`'s `write` returns a count, `io.writer`'s methods
return counts/errors) imposes no constraint, so ordinary types still
satisfy their interfaces unchanged. The motivating constructors
(`first_word`, `new_builder`) are free functions and never reach this
guard at all.

Consequently `fn make_view(buf byte[]) SomeIface { return &Thing{buf: buf} }`
is rejected too: the `&Thing` → `SomeIface` coercion inside it is
caught if `Thing` has any borrowing method. And so is
`var a any = thing` for a `Thing` with a borrowing method — the `any`
target is no escape hatch. Carrying `ReturnAliases` on
`InterfaceMethodSig` plus a per-implementer conformance check
(**option (b)**) — which would *permit* interface-dispatched return
aliasing under a checked contract, and would also need to carry the
contract through the runtime-itab assertion/type-switch path — is
deferred to [Future extensions](#future-extensions).

#### Why coarse-and-loud, not a marker or a per-method exclusion

Two finer-grained designs were considered and rejected, both for UX
rather than soundness:

- **A visible `borrowed`-return marker on the signature.** A marker you
  write to make the coercion go through carries no power the compiler
  doesn't already have: under option (a) the whole-type rule rejects the
  coercion regardless of whether the marker is present. So it can only
  fail one of two ways, both worse than just rejecting the coercion: if
  you forget it, compilation fails demanding you add it; once added, the
  coercion *still* fails because the interface needs a non-borrowing
  method. The marker is pure ceremony inserted *before* the error you
  actually needed; better to emit that error directly.

- **A per-method exclusion** (exclude only borrowing methods from the
  interface machinery, let the type otherwise be an interface). This is
  more expressive — a type stays usable as an interface for its
  non-borrowing methods — but it is both *more work* and *still spooky*.
  More work because a sound version needs **two** coordinated changes,
  not one: excluding borrowing methods from the runtime typedesc method
  table is not enough, because static coercion does not consult that
  table — `__vtable_T__I` is built from *direct* method relocations
  (`ast.go:670`, via `WriteVtables`), and static conformance is decided
  by `TypeSatisfiesInterfaceAs` (`ast.go:553`), which matches by
  name/signature/receiver only and has no aliasing-awareness. So a
  table-only exclusion still lets `var x Byter = b` wire a direct reloc
  to the borrowing `bytes` when `Byter` *requires* `bytes` — a static
  hole. A correct per-method exclusion therefore also has to teach the
  static conformance predicate to treat a borrowing method as a
  non-match. Still spooky even when done correctly: because the type
  *can* still become an interface for its other methods, `var a any = b`
  compiles (a `any` requires no methods), and only `a.(Byter)` fails at
  runtime because `bytes` was excluded from the table. An innocuous body
  edit that turns a method into a borrowing one would break a
  previously-working dynamic cast far from the edit, with no
  compile-time signal.

The coarse rule trades expressiveness for a property worth more here:
**every conformance-affecting change fails loudly, at compile time, at
a coercion site that exists in the source.** Because the rule rejects
*entering the interface world at all* — including the `var a any = b`
coercion the per-method variant let through — there is no surviving
dynamic path and therefore no runtime conformance surprise. The cost
is bluntness: a borrowing method disqualifies its whole type from
*every* interface, including ones that don't use it (a `Builder` that
was an `io.writer` via `write` stops being one the moment a borrowing
`bytes` is added). That is acceptable precisely because the failure is
loud and directed — see the diagnostic below — and because the
expressive form (option (b)) remains available as a future upgrade
that lifts the restriction without changing this rule's call sites.

#### Diagnostic

The quality of this rule's UX is the diagnostic. When
`validateInterfaceCoercion` rejects a coercion under this clause, it
must not say merely "Builder does not satisfy io.writer" — that reads
as a non-sequitur when the interface in question does not even mention
the offending method. It must instead enumerate the type's
borrow-returning methods, point at each definition, and explain the
whole-type rule. Shape:

```
error: cannot use `Builder` as interface `io.writer` here
  `Builder` is concrete-only: it has a method that returns a borrow,
  and a borrow-returning method cannot be dispatched through an
  interface (dispatch cannot track the borrow's lifetime). A type with
  any such method cannot be coerced to any interface — not even `any`.

  borrow-returning method(s) on `Builder`:
    bytes(b *Builder) byte[]   — returns a borrow of its receiver
        defined at builder.bos:42

  fix: call `bytes` directly on a concrete `Builder`, or change `bytes`
       so it does not return a borrow, to make `Builder` interface-eligible.
  (coercion required here →)  var w io.writer = b
```

The enumeration walks the concrete type's full method set (the same
`c.TypeMethodsFor` walk the guard already performs), reporting each
method whose `alias_set` is non-empty and a short rendering of *what*
it borrows (receiver and/or which parameter, from the inferred
`ReturnAliases`).

**Source position, and the imported-type gap.** For a **locally**
defined type the method's `FuncDecl` carries a usable position
(`FuncDecl.p` / `Pos()`, set during `ToAST`), so "defined at
file:line" is exact. For an **imported** type the position is *not*
available as the code stands: the importer rebuilds method
`FuncDecl`s via `parseFuncType`, which sets no position
(`ast.go:1116`–`1131`, `parser.go:256`), so the enumeration has no
line to print. Two ways to handle it, both acceptable:

- **Degrade gracefully (minimum):** for an imported method render
  "defined in package `<pkg>`" with no line. The diagnostic still
  names the offending method and the rule; it just can't point at the
  source line the consumer doesn't have.
- **Plumb the site (better):** the `.bo` already carries `SrcFile` /
  `SrcLine` on `gbasm.Function` (`function.go:737`, serialized in
  `bwrite.go`). A small importer change threading those onto the
  rebuilt `FuncDecl`'s position would give exact "defined at
  file:line" for imported types too, mirroring how `ReturnAliases`
  itself is plumbed across the boundary. Recommended but not required
  for v1.

Naming the methods (and, where available, their definition sites) is
what turns an accidental borrow-introducing edit into an immediately
actionable error pointing back at the edit, rather than a confusing
rejection at an unrelated coercion.

### Branch-merge of escape-restricted origins

`flow.Merge` is the shared join run at every if/else/loop/switch
convergence in the whole borrow checker — not just at returns. Before
this feature it dropped a top-level binding's origin to
unknown-and-safe (`KnownOrigin=false`) whenever the two branches'
origins differed, which was a **pre-existing soundness hole** the moment
borrowed/local returns became recordable: `var t byte[] = a; if (cond)
{ t = loc[:] } return t` with a *local* array in one branch had its
local origin collapsed away at the merge, so the return escaped
uncaught.

The fix unions the escape-restricted taint at the merge with a
**most-restrictive preference**, mirroring (and correcting) the
struct-field merge variant:

- A **local** origin from *either* branch surfaces, so a return/escape
  of the merged binding is rejected exactly as a direct local-slice
  return. Preference is most-restrictive, not first-restricted-wins: a
  borrowed origin in one branch must not mask a local in the other
  (that ordering-dependence was itself a latent hole, present in both
  the binding and field merge paths, and is closed in both).
- Two *different borrowed* origins (a clean param in each branch)
  combine into a synthesized **join origin** that carries the union, so
  inference records every contributing parameter
  (`ReturnAliases = [[0,1]]`) rather than under-reporting one of them.
  This applies to **both** the top-level binding merge (`mergePointerExpr`)
  **and** the struct-field merge (`mergeFieldPointerExpr`): the two paths
  share the same `newJoinOrigin`/`JoinMembers` mechanism, and the field
  consumer (`classifyStructSymbol` via `EscapingFieldOrigins`) expands the
  join's members exactly as the top-level consumer (`classifyExprOrigin`)
  does. The **union is the only sound direction** here, on both paths:
  `ReturnAliases` tells the caller which parameters to keep live for the
  returned value, so **over-recording** is conservative (the caller keeps
  an extra parameter live) while **under-recording a borrow is a
  use-after-free** — the caller may free a parameter the return still
  borrows. Recording only one of two merged borrowed fields would drop the
  other from the alias set; a caller could then pass a local (or a
  to-be-freed value) in the dropped slot and the escape would go uncaught.
  The earlier draft of this subsection called field-level under-recording
  "the acceptable (coarser-caller) direction" — that was **backwards and
  wrong**, and is corrected here: the field path records the full union.
  The only sanctioned residual coarseness is on the *over-record* side: the
  alias set is positional (slot → param, no field name), so a struct that
  borrows two params through *distinct* fields records both params against
  the whole return slot rather than per-field — conservative (it can only
  ask the caller to keep *more* live), never unsound. Eliminating that
  per-field imprecision is the deferred `[]Origin`-per-field representation
  extension noted under [Caller-side propagation](#caller-side-propagation).

Regression coverage: `retalias_branch_merge_local_err` /
`retalias_branch_merge_local_flipped_err` and
`retalias_branch_merge_field_local_err` /
`retalias_branch_merge_field_local_flipped_err` (local in one branch →
rejected, both branch orderings, for the top-level binding and the
struct-field merge respectively); `retalias_branch_merge_param`,
`retalias_branch_merge_field_param`, and
`retalias_branch_merge_field_union` (two borrowed params → allowed, union
recorded); `retalias_branch_merge_field_union_err` (a *local* in the
unrecorded-by-the-old-merge slot → rejected, the use-after-free the field
union closes); plus `TestReturnAliasInference` cases asserting the
top-level merge records `[[0,1]]` and the struct-field merge (`choose`)
records `[[0,1]]`.

Struct returned by value *from a call*: a struct whose borrowed field is
populated through a call (`var b B = mk(arg)`, `b = mk(arg)`, or directly
`return mk(arg)`) carries no single `Origin`, so the call result's borrow
lives in one of its fields, not in a top-level pointer fact. Inference
expands the callee's alias set onto the call arguments and records the
merged origin under a synthetic field key on the destination
(`recordStructReturnCallFieldFacts`, shared by the live compile and the
cold inference walk). The same key feeds `CheckStructFieldEscapeLocal`, so
a *local* argument flowing into a returned struct through a call is
rejected at the live return site exactly as the direct
`b.buf = loc[:]; return b` form is. Regression coverage:
`retalias_struct_return_call_arg_local_err` (local through the call →
rejected), `retalias_struct_return_call_param` (borrowed param through the
call → recorded and usable by the caller), plus `TestReturnAliasInference`
cases for the struct-through-call alias recording.

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
- A concrete→interface **coercion** of a type that has **any** method
  with a non-empty inferred `ReturnAliases` — including coercion to the
  zero-method `any` interface (see
  [Interface-method dispatch](#interface-method-dispatch)). The method
  itself stays legal and usable via concrete dispatch; only the type's
  coercion into any interface value is rejected. This is the coarse
  exclusion (option (a)) that closes the runtime-itab laundering path
  (`x.(B)` / type-switch) at the single static entry point.

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
ordinary functions). bas adds a **new `HasPrefix("retaliases")`
branch** alongside the `type` handler (`cmd/bas/main.go:982`), but it
is *not* a verbatim mirror of it: the `type` handler stores the whole
trailing string raw into `f.Type` (`:983`–`:985`), whereas
`retaliases` requires **structured parsing** — split off the
`<slot>:` prefix, parse the trailing space-separated integers into a
`[]int`, and **accumulate across multiple `retaliases` lines** (one
per non-empty slot, in the multi-return examples above) into the
`[][]int` `ReturnAliases` field, growing the outer slice to index
`<slot>`. So it shares the *prefix-dispatch shape* of the `type`
handler but owns its own parsing and accumulation logic.

The `retaliases` branch must be dispatched inside the directive
`HasPrefix` block (beside `type`/`local`, around `cmd/bas/main.go:982`)
**before** the line falls through to bas's generic instruction-matching
fallback — otherwise `retaliases 0: 0` is handed to the instruction
matcher and misparsed as an opcode, exactly as `type`/`local` would be
without their own prefix branches.

### New `Function` field

`function.go:732`'s `Function` gains:

```
ReturnAliases [][]int   // serialized; empty/nil for ordinary functions
```

`writeFunction` / `readFunction` (`bwrite.go:362` / `:436`) serialize
it. For the common all-empty case the on-disk cost is a single
zero-count varint.

**Ordering and back-compat.** `writeFunction`/`readFunction` read a
fixed positional sequence of fields (name, pub bit, type, srcfile,
srcline, args, symbols, relocations, body) with **no version tag and no
length-delimited record framing**. There is no room for an
out-of-order or optional insertion: if writer and reader disagree on
field order by one element, every subsequent field deserializes from
the wrong offset and corrupts silently. Therefore `ReturnAliases` must
be **appended strictly after the last existing field (the body)** on
*both* sides in lockstep — `writeFunction` writes it last,
`readFunction` reads it last. Because there is no version negotiation,
**any pre-existing `.bo` produced before this change is
unreadable by the new reader** (and vice versa): all `.bo` artifacts —
including the pre-assembled runtime `.bo`s the test harness stages
(`string.bo`, `init.bo`, `heap.bo`, `io.bo`, `errors.bo`, etc., per the
`cmd/bosc` mmkfile setup) and any checked-in/cached object files — must
be regenerated from source as part of landing this change. A future
versioned `.bo` header would remove this constraint but is out of
scope here.

### Importer

`cmd/bosc/ast.go:1116`–`1131`, where imported funcs are reconstructed
from `o.Funcs`, attaches `ReturnAliases` onto the rebuilt `FuncDecl`
so cross-package `alias_set` short-circuits to the imported fact. The
rebuilt `FuncDecl` is the `&t` registered at `ast.go:1129`–`1130`, so
the entire importer change is effectively the single assignment
`t.ReturnAliases = fn.ReturnAliases` — given that the
`Function.ReturnAliases` field is already populated by `readFunction`.

**Where the one line lands matters.** `t` is created by `parseFuncType`
(`ast.go:1121`); the assignment must sit **after** that call and
**before** `DefineImportedFunc` (`:1130`), with the type-qualification
loop (`:1125`–`:1128`) untouched in between. `ReturnAliases` is a
`[][]int` of pure positional parameter/slot indices — it carries no
type names, so requalification of `t.Args`/`t.Return` does not touch it
and it is qualification-invariant; the assignment may go either side of
the qualify loop, but stating it lands after `parseFuncType` and before
`DefineImportedFunc` is the precise contract. (The field's
serialization, the bas parsing, and the inference are where the real
work is; the importer itself is one line.)

### `bdump`

`bdump` prints the `ReturnAliases` of each function so cross-package
aliasing can be inspected when debugging a borrow error that crosses a
package boundary.

## Implementation impact

- **`cmd/bosc/flow/state.go`**: no new origin *kind*. A helper to
  enumerate the escaping origins of an expression as
  `(kind, paramIndex)` pairs rather than a bool, so the inference can
  record rather than reject. Plus the **multi-param union
  representation** (see [Caller-side propagation](#caller-side-propagation)):
  `PointerExpr` (`flow/state.go:13`–`18`) holds a single `Origin`, so a
  result that may alias more than one argument needs either a set of
  contributing origins on the binding or a synthesized join origin
  whose validity is the conservative meet of its members. This is the
  only `flow.State` shape change; it is additive and leaves the
  single-origin fast path untouched.
- **`cmd/bosc/compile.go`**:
  - The `alias_set` memoized inference helper + `in_progress` cycle
    guard.
  - Refactor **all five** return-site checks to call the inference's
    per-origin classifier (local → reject, borrowed → record):
    - the three `IsEscapeRestricted`-routed sites (`:3619`, `:3627`,
      `:3646`–`3678`);
    - `checkBorrowedPointerDoesNotEscape` / `checkLocalOriginDoesNotEscape`
      at the direct-pointer-return site (`:3613`–`3616`,
      helpers at `:699`–`713`). `checkBorrowedPointerDoesNotEscape`'s
      blanket borrowed-pointer reject must be split so a borrowed
      pointer return records its param index instead of erroring;
      `checkLocalOriginDoesNotEscape`'s `OriginLocal` reject stays.
    - the interface-coerced pointer-return site (`:3689`–`3697`), which
      today only rejects `OriginLocal`; it must additionally *record*
      a borrowed-pointer origin so a borrowed pointer returned as an
      interface contributes to `ReturnAliases`.
  - **A new `*Funcall` value case in `pointerExprForAST`**
    (`compile.go:1290`–`1311`): today it returns `UnknownPointer()` for
    a borrowed-returning call, so the result escapes every check. The
    new case looks up `alias_set(callee)`, maps each aliased param
    index to the matching argument's `PointerExpr`, and synthesizes a
    propagated origin for the result. This is core new machinery —
    consumed by both the inference walk and caller-side binding — not
    plumbing. It is the part that keeps the feature sound once the
    borrowed-returning callee is made legal (it cannot trigger today,
    because that callee is rejected first); see
    [the inference section](#inference-demand-driven-memoized-cycle-safe).
  - Call-site propagation: after `setupArgs` / result binding, apply
    the callee's `ReturnAliases` to the result's flow origins
    (`Funcall` lowering, around `:1290` / `:2637`), including the
    multi-param union when a slot lists more than one param.
  - Emit the `retaliases` directive in the function preamble next to
    the `type` line.
  - **Interface-coercion guard** in `validateInterfaceCoercion`
    (`:216`–`255`): reject the coercion if the concrete type has **any**
    method with a non-empty inferred `ReturnAliases`. The check is
    **independent of `TypeSatisfiesInterfaceAs`** (`:238`) — it does not
    key off the target interface's required-method subset, because the
    runtime itab (`_iface.assert_to`, used by `x.(B)` and type-switch)
    is minted from the concrete type's *full* method table; keying off
    the required subset is vacuous for the zero-method `any` and reopens
    the hole. Enumerate the type's full method set via
    `c.TypeMethodsFor(typeName)` (the accessor behind
    `methodEntriesForType`, `typeinfo.go:229`) and demand-drive
    `alias_set(method)` on each. **This guard must run for the `any`
    target too** — that is the case the widening exists to catch. This
    is the single chokepoint behind every static concrete→interface
    coercion (via `emitInterfaceFatPtr` → `validateInterfaceCoercion`
    at `:5941`, including the `any`-packing path
    `compileAsInterfaceValue:4688` and variadic `any[]`-element packing
    `:5492`), so the interface-dispatch soundness exclusion lands in
    exactly one place; see
    [Interface-method dispatch](#interface-method-dispatch). Static-init
    of interface-typed globals is *not* a separate entry point:
    `encodeStaticInit` (`globals.go:116`) has no interface-coercion case
    and globals.go never calls `emitInterfaceFatPtr`, so a file-scope
    `var g SomeIface = …` is not expressible and cannot bypass the
    guard.
  - **Directed rejection diagnostic.** On rejection the guard must
    enumerate the concrete type's borrow-returning methods (each method
    whose `alias_set` is non-empty), with each method's source position
    and a short rendering of what it borrows, and explain the
    whole-type "concrete-only" rule — *not* emit a bare "does not
    satisfy" message, which is a non-sequitur when the target interface
    does not mention the offending method. See
    [Diagnostic](#diagnostic). For a locally-defined type the method
    positions come from the `FuncDecl`s the `TypeMethodsFor`
    enumeration already walks; for an **imported** type the rebuilt
    `FuncDecl` carries no position, so the diagnostic either degrades
    to "defined in package X" or (recommended) the importer is extended
    to thread the `.bo`'s `SrcFile`/`SrcLine` (`function.go:737`) onto
    the rebuilt decl — see [Diagnostic](#diagnostic).
- **`cmd/bas/main.go`**: a new `retaliases` prefix branch beside the
  `type` handler at `:982`. Unlike `type` (raw-string store), it parses
  `<slot>: <idx>...` structurally and accumulates one entry per slot
  into the new `[][]int` `Function` field.
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
- `retalias_ptr_passthrough_test.bos` — `fn id(p *T) *T { return p }`,
  exercising the **direct-pointer-return** site
  (`checkBorrowedPointerDoesNotEscape`, `compile.go:3613`–`3616`) that
  the slice passthrough above never reaches. Without this test the
  pointer-return refactor (finding: five sites, not three) is
  unverified.
- `retalias_iface_return_test.bos` — a borrowed pointer returned as an
  interface, exercising the **interface-coerced pointer-return** site
  (`compile.go:3689`–`3697`) so the borrow it carries is recorded
  rather than silently dropped.
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
- `retalias_iface_coerce_err_test.bos` — a concrete type with a method
  that returns a borrowed receiver field (legal, inferred
  `ReturnAliases` non-empty), coerced to an interface declaring that
  method; rejected at the coercion site
  ([Interface-method dispatch](#interface-method-dispatch)). Verifies
  the coercion guard fires and the vtable-dispatch borrow hole is
  closed. The `.expected` must assert the **directed diagnostic** names
  the offending method and its definition site (see
  [Diagnostic](#diagnostic)) — a bare "does not satisfy" message is a
  test failure, since the whole point of the rule's UX is naming the
  borrowing method. A companion positive assertion (concrete
  `v.method()` on the same type still compiles and runs) belongs in
  `retalias_struct_ctor_test.bos` or a sibling, confirming the
  exclusion is at the coercion, not at the body.
- `retalias_iface_coerce_unrelated_err_test.bos` — the bluntness case:
  a type with a non-borrowing method that legitimately satisfies some
  interface (e.g. an `io.writer`-shaped `write`), *plus* an unrelated
  borrowing accessor, coerced to that interface. Rejected, and the
  diagnostic must explain the whole-type rule (the interface does not
  mention the borrowing method) rather than reading as a non-sequitur.
  Pins the diagnostic's handling of the coarse rule's main sharp edge.
- `retalias_iface_coerce_imported_err_test.bos` — a borrowing-method
  type defined in an **imported** package, coerced to an interface in
  the consumer. Rejected; pins the imported-type diagnostic rendering
  (degraded "defined in package X" or, if the position-plumbing step
  is implemented, the exact site). The other coercion `_err_test`s use
  local types and would not catch a regression in the imported path.
- `retalias_iface_coerce_any_err_test.bos` — the same borrowing-method
  type coerced to the **zero-method `any`** interface (`var a any =
  thing`); rejected. Verifies the widened guard runs for the `any`
  target — the case a required-methods-only guard would let through.
- `retalias_iface_launder_via_any_err_test.bos` — the laundering path:
  a borrowing-method type would be packed into `any`, then asserted
  with `a.(B).field()` to dispatch the borrowing method through a
  runtime-minted itab. Because the coercion *to* `any` is rejected
  (option (a)), the program never reaches the assertion; the test
  asserts the failure is reported **at the coercion to `any`**, not at
  the `.(B)` site — confirming the hole is closed at the single static
  entry point rather than at each runtime-dispatch site.

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
`owned`-param returns as today, and the *mechanism* matters: the
prologue registers `OriginBorrowed`/`SetBorrowedBinding` only for
**non-owned** params (`compile.go:2569` and `:2574` both gate on
`!a.Type.HasOwned()`). For an owned **slice** param, no origin is
registered at all (the `:2574` slice branch is skipped). For an owned
**pointer** param, the unconditional `AssignPointer(NewObject(...))` at
`:2572` still fires (it is gated only on `Indirection > 0`), but
`NewObject` (`flow/state.go:265`) records *no* `originInfo`, so
`OriginKindOf` returns the zero `OriginUnknown` and `IsEscapeRestricted`
is false. Either way the param's origin is **not `OriginBorrowed` and
not escape-restricted**, so the inference's classifier hits the
`otherwise: pass` branch and records no alias. The outcome stated in
the original (`no alias is recorded`) is correct, but it is reached by
"no origin → not escape-restricted → record nothing," not by an
explicit owned-param exclusion. Independently, the existing
`valType.HasOwned() && !retType.HasOwned()` drop check
(`compile.go:3684`) and `checkAddressOfOwnedForDest` still govern what
an owned return may legally do; this proposal does not touch them.
Worth confirming no pattern needs "return a borrowed view of an owned
param" — if it does, it's a separate analysis.

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
- **Interface-dispatched return aliasing (option (b), the precise
  upgrade).** v1 (option (a)) forbids coercing a borrowing-method type
  to *any* interface at all
  ([Interface-method dispatch](#interface-method-dispatch)) — a coarse
  but sound exclusion that closes the runtime-itab laundering path by
  denying entry. The precise upgrade lifts the exclusion under a
  statically-checked contract:
  - carry `ReturnAliases` on `InterfaceMethodSig` (the
    interface-declared contract);
  - at coercion/satisfaction time, check each implementer's inferred
    method `ReturnAliases` against the interface-declared contract
    (`TypeSatisfiesInterfaceAs`-style, but per-method aliasing rather
    than per-method signature), so the coercion is *permitted* exactly
    when the implementer's aliasing matches the declared contract;
  - propagate the interface method's `ReturnAliases` (param index 0 →
    the dispatch-site receiver/data word, index k → user arg k−1) onto
    the dispatched call result in `compileInterfaceMethodCall`;
  - and crucially **track interface-dispatch results** — replace the
    `UnknownPointer()` fallthrough at `compile.go:1311` for a
    vtable-dispatched call with a synthesized origin derived from the
    declared `ReturnAliases`, *and* carry the same contract through the
    runtime-itab assertion/type-switch path (`_iface.assert_to`,
    `compileInterfaceAssert`/`compileTypeSwitchIfaceCase`) so a
    `x.(B).field()` result is tracked rather than laundered.
  This is materially more machinery than option (a) (a new
  `InterfaceMethodSig` field, a per-implementer aliasing-conformance
  check, dispatch-result origin synthesis, and runtime-itab contract
  propagation) and is deferred — no current consumer needs an interface
  method that returns a borrow of its receiver.
- **Through-pointer output aliasing** — inferring that
  `fn init(b *mut T, buf byte[])` installs `buf` into `*b`, for the
  init-in-place constructor shape. A distinct analysis (alias flows
  into a parameter, not the return); deferred.
