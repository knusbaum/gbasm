# Proposal: anonymous union types and typed switch cases

## History

- 2026-10-18 — Implemented the initial union feature, including canonical identity, typed switches, ownership/provenance tracking, and cross-package metadata; moved to `docs/implemented/`. Full Go, assembler, and language checks passed.
- 2026-10-18 — Settled exact-first injection, identical-union forwarding, initial return/parameter/initialized-local scope, type precedence, and alias identity; deferred widening, composite storage, and nominal wrappers.

- 2026-10-18 — Recorded flat unordered identity, canonical tag ordering, exact cases without default, qualifier restrictions, non-addressable union containers, and addressable narrowed payloads; retained only unresolved choices as open questions.

- 2026-10-18 — Recorded the union-type design discussion, agreed syntax and matching rules, reuse of the values ADT representation, and questions to resolve before implementation.

## 1. Motivation

Boson encodes common C conventions in the type system so that callers do not
have to recover safety rules from comments. Ownership is one example. A
function that returns either a usable resource or an error is another.

Multiple return values express that both results exist:

```boson
fd, err := open(...)
```

They do not express that `fd` is valid only when there is no error. In
particular, an owned resource returned alongside an error creates an ownership
obligation even on a failure path where no resource was acquired.

A named `values` type can express the exclusive outcomes correctly:

```boson
type open_result values {
    FD{fd owned FD}
    ERR{err error}
}
```

However, declaring a wrapper type and case names for each such function adds
substantial API machinery. The caller also accesses a payload field only to
move that field out of its wrapper. This proposal provides anonymous sum types
whose alternatives are the payload types themselves.

The feature is general. Errors motivate it, but neither errors nor binary
success/failure results receive special language treatment.

## 2. Agreed design

The following decisions were settled in discussion:

1. Write alternatives with `|`: `T | U | error`. There is no two-alternative
   limit and no new keyword. Unions belong in the shared type syntax rather
   than a separate function-return grammar. Initially support return types,
   parameters, and initialized locals (§9).
2. Union identity is flat and unordered: `T | U` and `U | T` are identical,
   as are `T | (U | V)` and `T | U | V`. Grouping does not create another
   runtime union layer. Canonical ordering determines tags (§6).
3. A function returning a union may return a value belonging to one of its
   alternatives, without an explicit named wrapper construction. An exact type
   match wins; otherwise exactly one compatible alternative must exist (§3).
4. Reuse `switch` to select an alternative. Switch heads require parentheses;
   a local head binding uses the normal `:=` declaration syntax.
5. Union cases match exact types, including ownership, mutability, pointer
   structure, and nullability. They do not implicitly strip qualifiers.
6. Reject alternatives that differ only in ownership, mutability, or
   nullability. These qualifiers are not outcome discriminators.
7. Narrow the switched value to the complete payload type in each case.
   Ownership remains checked; selecting a case does not itself consume it.
   Consumption versus inspection follows the existing ADT branch rules.
8. Union switches have no `default`. Consuming and expression switches name
   every alternative explicitly. Inspecting statement switches may omit cases.
   There are no residual-union defaults or special last-alternative shortcuts.
9. Union containers are non-addressable and cannot have indirection or
   qualifiers applied to them. There are no borrowed views of union
   containers. Individual alternatives can still be pointers or carry
   ownership, mutability, and nullability where those are otherwise legal.
10. The narrowed payload is addressable. Its address denotes that payload,
    not the union container, and follows existing payload-borrow rules (§5).
11. Reuse the `values` ADT tagged-union representation and applicable payload
    operations. There is no separate union runtime or ownership metadata.
12. Qualifier inference in case patterns is deferred. `infer` was considered
    a reasonable name, but adding a keyword is not justified for the initial
    design. Start with exact matching and evaluate sugar through actual use.

The sections below describe how these decisions fit existing Boson semantics.
The language decisions below record the accepted design. The initial feature
is implemented; the canonical current specification is in `DESIGN.md`. Section
11 records the implementation obligations established by this proposal.

## 3. Function signatures and return construction

```boson
fn do_something() i64 | error {
    if (failed()) { return make_error() }
    return i64(42)
}

fn acquire() owned *mut Resource | error {
    if (failed()) { return make_error() }
    return new(Resource{...})
}
```

Each union instance contains exactly one alternative. Returning an owned
alternative transfers its obligations into the result. Returning an error
alternative does not fabricate an obligation to destroy the resource from
the other alternative. An error alternative can itself have ownership
obligations if its declared type requires them.

Qualifiers apply to the alternative in which they appear:

```boson
fn acquire() owned *mut Resource | error
```

Here, `owned` belongs to `*mut Resource`; it does not qualify every alternative
or introduce a separately owned wrapper. This distinction needs to be explicit
in the type grammar and checker representation.

**Construction rule:** select an exact
resolved type match first. If there is no exact match, apply existing
assignment/return compatibility rules to each alternative and require exactly
one compatible alternative. Reject no matches and ambiguous matches.

For example, returning a `T` into `T | error` chooses `T` even if `T`
implements `error`. A concrete value implementing two interface alternatives
is ambiguous if neither is an exact match. This uses a small, explicit
rule instead of a general hierarchy of "most specific" types.

Leftmost-compatible selection is incompatible with unordered union identity:
reordering the same type must not change which outcome a returned value selects.
Canonical storage order must not become a conversion-preference order either.

Explicitly disambiguate through existing conversions or typed intermediate
bindings, subject to their existing ownership checks. No new union-selection
keyword is introduced. For example, a binding explicitly typed as an interface
selects that exact interface alternative when injected into the union.
Untyped literals use existing contextual compatibility checking: `i64(42)`
selects an exact alternative, while `42` in a return type such as `i32 | i64`
is rejected if more than one alternative accepts it.

Forwarding a value with the same canonical union type preserves its tag,
payload, and obligations. Source ordering is irrelevant: forwarding `U | T`
to `T | U` is identical-union forwarding. Implicit widening to a different
union, such as `T | U` to `T | U | error`, is deferred and rejected initially.

## 4. Selecting and unpacking alternatives

```boson
switch (v := do_something()) {
    case i64 {
        use_number(v)
    }
    case error {
        report(v)
    }
}
```

Inside `case i64`, `v` is the `i64` payload. Inside `case error`, it is the
`error` payload. There is no extra `.value`, `.fd`, or `.err` field. Outside
the cases, an existing scrutinee retains its union type and the ownership
state determined by the branch merge. A head binding is scoped to the switch.

A union switch dispatches on its alternative tag. It does not need `.(type)`:
that syntax continues to mean dynamic type dispatch on an interface. If one
alternative is an interface, selecting that alternative does not also unwrap
the interface or test its dynamic concrete type.

More than two alternatives use the same mechanism:

```boson
fn find_input() owned *mut File | owned *mut Socket | error

switch (v := find_input()) {
    case owned *mut File {
        use_file(v)
        v.destroy()
    }
    case owned *mut Socket {
        use_socket(v)
        v.destroy()
    }
    case error {
        report(v)
    }
}
```

The case type is an exact selector, not a request to convert or borrow the
payload. `case *File` does not match `owned *mut File`. Package qualification
and type aliases should resolve through the compiler's normal type lookup;
exactness concerns resolved type identity and qualifiers, not identical source
text.

### Expression switches and early exits

Existing expression-switch behavior supplies the unpacking operation:

```boson
p := switch (v := acquire()) {
    case error {
        report(v)
        return
    }
    case owned *mut Resource v
}
p.destroy()
```

The success arm yields the payload and transfers its obligations into `p`.
The error arm exits the enclosing function and contributes no result type.
The destination acquires no obligation on a path where its initializer exits.

There is no `default`, even if only one alternative would remain. The caller
writes its exact type. This avoids residual union types and makes each branch
identify the outcome it handles.

### Exhaustiveness

Reuse the existing distinction between inspecting and consuming switches:

- Expression switches must explicitly cover every alternative, and every
  continuing arm must yield a compatible result.
- Consuming switches must cover every alternative and discharge each active
  payload's obligations on every relevant path.
- Inspecting statement switches may omit alternatives. An unmatched path
  preserves the original value.
- Cases do not fall through. Duplicate or non-member case types are errors.

## 5. Ownership and borrow tracking

A union's obligations come from its active payload. The compiler must track
them through the union even before narrowing makes that payload accessible.
An unknown alternative is not permission to discard a potentially owned value.

Use the existing inferred switch discipline rather than adding a consuming
switch keyword:

- Inspection preserves the active payload's obligations.
- Consumption transfers or discharges all of those obligations and makes the
  original union unavailable afterward.
- Alternatives without owned obligations are neutral, so an error arm that
  returns can coexist with a success arm that moves an owned resource out.
- Partial consumption or disagreement between consuming and preserving paths
  is rejected, following existing variant-switch rules.

### Non-addressable containers; addressable payloads

A union binding cannot be addressed or exposed through a borrowed union view.
For example, taking `&result` before selecting an alternative is rejected.
`*(T | U)`, `[](T | U)`, and qualifiers on `(T | U)` are not supported. An
array of union elements would likewise expose union container locations and is
excluded from the initial design. These restrictions concern the union itself;
`*T | U`, `[]T | U`, and `owned *mut T | error` have ordinary payload types.

The union needs no outer `owned` annotation: its active payload supplies its
obligations. Transferring a union that can carry an owned alternative transfers
those conditional obligations and invalidates the source as required by the
tracker. This differs from granting a non-owning union view: such views are
not available.

After narrowing, address-taking accesses the selected payload in place:

```boson
switch (v := open_resource()) {
    case owned FD {
        io.copy(&io.STDOUT, &v)
        v.close()
    }
    case error { report(v) }
}
```

Here `&v` points to the `FD` payload and `close` consumes that payload. Neither
operation takes the address of the tag-plus-payload container. Inspecting a
payload does not itself consume it. The payload remains addressable even when
it is an owned scalar needing an address for a method receiver.

The payload's declared permissions and existing ADT borrow rules govern that
address; narrowing does not add mutability. The tag cannot change while the
value is narrowed, and moving or replacing the union must invalidate derived
borrows under the existing tracker rules. Returning or storing a payload borrow
must satisfy existing lifetime/dependency checks; non-addressable containers
do not make their payload storage immortal.

Reuse flow paths, alias invalidation, and consuming-versus-inspecting branch
merges. No separate qualifier-matching rule for borrowed union containers is
needed because those views are forbidden.

## 6. Alternative identity and rejected unions

For duplicate detection, compare alternative types after removing ownership,
mutability, and nullability qualifiers at every level. Keep nominal type identity
and the rest of the type structure. This comparison is only a declaration-validity
rule; it is not the case-matching rule.

Reject, for example:

```boson
*T | owned *T
*T | *mut T
T | ?T
owned *owned *T | owned **T
```

The same rule rejects exact duplicate alternatives. Aliases must not evade it.
Distinct nominal resource types remain useful even if their storage types are
identical; `File | Socket` can describe distinct outcomes.

A nullable individual alternative is allowed wherever its payload type supports
nullability, for example `?*T | error`. The union itself is never nullable;
`?(T | U)` is rejected. `T | ?T` is rejected by duplicate-shape checking, not
because all nullable alternatives are forbidden. Existing restrictions still
apply: this proposal does not independently make `?i64` or nullable slices legal.

Overlapping interfaces are different from duplicate alternatives: matching a
selected union alternative is exact, while injecting a concrete value may
require disambiguation.

### Canonical identity and tag ordering

Normalize each union as follows:

1. Resolve types using existing type identity rules. Import aliases and source
   spelling do not affect identity; distinct nominal types remain distinct.
2. Flatten union alternatives recursively. Parentheses do not preserve an
   alternative group or introduce another tag.
3. Reject duplicate alternatives, including equal shapes after recursively
   removing ownership, mutability, and nullability. Do not silently deduplicate
   repeated alternatives introduced by flattening.
4. Compute the canonical encoding of each complete alternative, retaining all
   its qualifiers, and sort encodings lexicographically as unsigned bytes.
5. Assign tags `0` through `N-1` in that order. Use the same ordering for type
   comparison, code generation, and exported metadata.

This makes `T | U` and `U | T` identical in both type identity and tag mapping.
It does not expose tag values as source-level constants. Registration order,
source order, machine addresses, and hashes must not determine ordering.

### Canonical encoding

The following encoding defines a deterministic order, not a new public ABI or
source type spelling. Use version `1` consistently across metadata producers
and consumers; a future change requires coordinated versioning.

Define `L(x)` as the ASCII decimal length in bytes of byte string `x`, followed
by `:` and then `x`, with no leading zeros in the length except `0`. A node
consists of `L(kind)`, three ASCII flags (`owned`, `mut`, `nullable`, each `0`
or `1`), and zero or more `L(field)` entries in the order below. Strings use
UTF-8; unsigned numbers use ASCII decimal without leading zeros. Child nodes
are encoded recursively and length-prefixed as fields. The top-level encoding
is ASCII `1:` followed by the node encoding.

| Kind | Fields, in encoding order |
|---|---|
| `named` | Canonical defining package/import identity, declared type name |
| `pointer` | Pointee node |
| `slice` | Element node |
| `array` | Length, element node |
| `struct` | Field count, then each field's name and type node in declaration order |
| `function` | Variadic flag, parameter count, parameter type nodes in order, return count, return type nodes in order |
| `union` | Alternative count, then the flattened, sorted alternative nodes |

Builtin types use their canonical builtin identity and name. Named types are
encoded by identity, without expanding their definitions; this preserves
nominal distinctions and terminates recursion through named types. Genuine
aliases resolve according to existing alias rules before encoding. Anonymous
structs and function signatures are structural; parameter variable names and
source positions are excluded. Each qualifier appears at the node/level it
qualifies, independently of the compiler's internal bit-mask representation.
Multi-return signatures use the ordered return list, not a union node.

A union node has all three qualifier flags zero. Its alternatives retain their
own flags. Nested unions inside otherwise supported structural types use the
same canonical encoding; forbidden direct wrappers remain invalid regardless
of whether they could be encoded. Any additional type form admitted later
must define an unambiguous canonical node before it can be a union alternative.

## 7. Relationship to existing results and values types

Unions supplement multiple returns and named `values`; they do not replace
either universally.

| Situation | Appropriate representation |
|---|---|
| Exactly one of several differently typed outcomes | `T | U | error` |
| A count and an error can both be meaningful | Multiple returns or a struct |
| Outcomes need semantic names or repeat a payload type | Named `values` |
| A stable domain API needs methods on its result wrapper | Named `values` |

In particular, a read/write API may return both partial progress and an error.
Changing `n, err` into `i64 | error` would lose information unless the operation
already guarantees that those outcomes are exclusive.

Two meanings carried by the same type cannot be separate alternatives in this
design. Use named cases, such as `LEFT{value T}` and `RIGHT{value T}`, when the
distinction is semantic rather than a difference in payload types.

No standard-library migration is part of this proposal. Resource acquisition
is a useful first experiment; broad IO API changes should follow ergonomic
evaluation with real callers.

## 8. Representation and compiler integration

Reuse the tagged-union representation and machinery of payload-bearing
`values`: an inline tag plus storage sized and aligned for the largest
alternative. Each alternative is a variant carrying one payload. Injection
writes the payload and selects its tag; switching tests that tag. The feature
does not inherently require boxing, allocation, runtime ownership metadata,
or a runtime search through interface implementations.

Use existing ADT layout and calling-convention rules for the canonical ordered
alternatives, each with one payload. Cross-package metadata must carry the full
alternative types and use the canonical tag mapping in §6. Source order cannot
change the ABI of otherwise identical unions.

Implementation touches type parsing and identity, return/assignment checking,
switch narrowing and flow merges, payload layout/code generation, and exported
type metadata. Validation belongs inline in the existing AST construction and
compilation paths, consistent with the compiler's single-pass design. Reusing
`values` layout helpers does not make this merely an anonymous `values`
declaration: injection, type-selected cases, and payload-level narrowing are
new language behavior.

## 9. Scope and deferred syntax

The primary use is function results, and `|` is part of the common type grammar.
There is no separate union runtime or return-only internal representation.
Initially, unions are allowed in function return types, parameters, and
initialized local declarations, including explicitly typed locals:

```boson
fn obtain() T | U
fn accept(value T | U)

x := obtain()
y T | U := obtain()
var z T | U := obtain()
accept(x)
```

The binding's `var` controls reassignment; it does not apply `mut` to the union
type. Passing a union carrying owned obligations transfers those obligations;
it does not create a borrowed union container.

Uninitialized union declarations are rejected. There is no default alternative
or implicit zero-tag initialization. Fields, global union storage, arrays/slices
of unions, and other composite storage are deferred. The longer-term aspiration
is to admit unions wherever an ordinary type can be named, subject to the
non-addressability and qualifier restrictions above.

### Precedence and grouping

`|` joins complete alternative types. Prefix qualifiers and constructors bind
within an alternative; grouping unions flattens them rather than creating a
nested union:

```text
owned *T | error       union of owned *T and error
T | (U | V)           same union as T | U | V
*(T | U)              rejected: pointer to a union container
owned (T | U)         rejected: qualifier on a union container
```

A function type's return clause absorbs `|`. Parentheses distinguish a
function-valued alternative from a function returning a union:

```text
fn() T | U            function returning T | U
(fn() T) | U          union of a function returning T and U
```

Commas separate multiple results, and `|` joins the alternatives within each
result. A return clause `T | error, U` declares two results: the first is
`T | error`, and the second is `U`. Parentheses around a complete function type
terminate that function's return clause before an enclosing union separator.
These rules apply equally to source types and imported type descriptions.

### Aliases

Any alias of a union preserves its canonical alternative-set identity and tag
mapping. Aliasing does not make the union nominal, permit forbidden qualifiers
or wrappers, or evade the initial type-position restrictions. Distinct nominal
wrappers around anonymous unions are deferred. Existing named payload types
continue to retain their own nominal identities (§6).

There is no implicit error propagation operator, automatic destructor, boolean
success test, or special designation of an alternative as an error. Applicable
operations follow payload-bearing `values` semantics: layout, moves, borrows
of selected payloads, and mutation/flow restrictions. Anonymous unions do not
gain case names, constant projections, arithmetic, or numeric conversions merely
because an alternative provides those operations. Do not add a new operation
solely for anonymous unions when the corresponding ADT operation is unsupported.

Exact case matching is deliberately the initial design, even when verbose:

```boson
fn complicated() owned *owned mut *mut i64 | error

switch (v := complicated()) {
    case error { report(v) }
    case owned *owned mut *mut i64 { consume(v) }
}
```

Writing `case **i64` would not match that alternative. A future pattern could
infer qualifiers while selecting by structure, but introducing `infer` or any
other abbreviation is deferred until actual usage demonstrates a need.

## 10. Validation required for implementation

The implementation needs Go tests for type identity, parsing, and diagnostics,
plus real language tests for generated code and ownership flow. Cover at least:

- Two and three alternatives, with every tag exercised at runtime.
- Resource and error paths, including an error alternative with its own
  owned obligation and early exits from expression initializers.
- Exact qualifier matching; duplicates differing at outer and inner ownership,
  mutability, or nullability levels; aliases and package-qualified types.
- Missing and duplicate cases; non-member case types; exhaustive expressions
  and consuming switches; inspecting switches that omit alternatives.
- Payload movement, reuse after move, partial consumption, branch disagreement,
  payload addressability, and borrow invalidation after replacement.
- Rejection of `default`, union address-taking, direct pointer/slice/array
  wrappers, qualifiers on unions, and uninitialized union bindings.
- Identical layouts and tags for reordered and flattened unions across
  packages, including import aliases and nominally distinct payload types.
- Exact-match priority over interface coercion, ambiguous numeric/interface
  injection, contextual literals, and explicit disambiguation through typed
  intermediate values.
- Identical-union forwarding, rejection of implicit widening, union parameters,
  inferred and explicitly typed initialized locals, and rejection of deferred
  storage positions.
- Function-return versus function-alternative precedence, multi-return commas,
  grouping, and aliases preserving canonical identity.
- Cross-package calls with differently sized/aligned payloads, including
  interfaces, pointers, slices, and composites where supported.
- `continue` and `break` in initializer arms, including `for` increments.

Run the root Go, assembler, and language integration suites through the normal
`mmk` workflow. Do not overlap separate bosc test invocations that share runtime
fixtures.

## 11. Implementation obligations

The language decisions needed for the initial implementation are settled. In
particular, implementation must preserve:

- Flat unordered identity, the canonical encoding and tag order in §6, and
  aliases that preserve that identity.
- Exact-first injection, unique-compatible fallback, ambiguity rejection,
  identical-union forwarding, and no implicit widening.
- Return types, parameters, and initialized locals as the initial supported
  positions, with the grammar and grouping rules in §9.
- Exact cases without `default`, existing ADT branch-consumption rules,
  non-addressable union containers, and addressable narrowed payloads.
- Rejection of qualifier-only alternatives, qualifiers/indirection on union
  containers, uninitialized bindings, and deferred composite/global storage.
- Reuse of ADT representation and applicable operations rather than a new
  runtime union model.

Remaining work is to map these rules onto the shared type parser, structural
type identity, existing ADT layout/ABI helpers, metadata, narrowing, and the
ownership tracker, and to validate the cases in §10. Unsupported operations
need directed diagnostics. Qualifier-inference syntax, widening, composite
storage, and nominal wrappers remain future extensions rather than blockers
for this implementation.

## References

- [Current language reference](../../DESIGN.md), particularly ownership,
  expression switches, variant switches, and interface type switches.
- [Payload-bearing values and variant-switch proposal](../implemented/PROPOSAL_adts_and_switch.md),
  for the original representation and narrowing rationale. It is a historical
  design; current consuming-switch behavior is documented in `DESIGN.md`.
- [Nullable value types proposal](PROPOSAL_nullable_value_types.md), for the
  related proposal to represent absence for types without a spare bit pattern.
  Anonymous unions in this proposal are not nullable; qualifying individual
  alternatives remains subject to existing nullable-type rules.
