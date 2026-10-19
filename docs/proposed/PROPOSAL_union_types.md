# Proposal: anonymous union types and typed switch cases

## History

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
   limit and no new keyword.
2. A function returning a union may return a value belonging to one of its
   alternatives, without an explicit named wrapper construction.
3. Reuse `switch` to select an alternative. Switch heads require parentheses;
   a local head binding uses the normal `:=` declaration syntax.
4. Union cases match exact types, including ownership, mutability, pointer
   structure, and nullability. They do not implicitly strip qualifiers.
5. Reject alternatives that differ only in ownership or mutability. Ownership
   and mutability are not outcome discriminators, even though a tag could
   technically distinguish those alternatives.
6. Narrow the switched value to the complete payload type in each case.
   Ownership remains checked; selecting a case does not itself consume it.
7. A `default` with exactly one remaining alternative narrows to that type.
   This is useful for the common result pattern but is not the general answer
   to selecting alternatives.
8. Qualifier inference in case patterns is deferred. `infer` was considered
   a reasonable name, but adding a keyword is not justified for the initial
   design. Start with exact matching and evaluate sugar through actual use.

The sections below describe how these decisions fit existing Boson semantics.
Specific choices not settled in discussion are identified as proposed or open.
All union examples describe proposed syntax, not an implemented feature.

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

**Proposed construction rule:** apply existing assignment/return compatibility
rules to each alternative. Exactly one alternative must accept the expression.
Reject no matches and ambiguous matches; do not choose an alternative based on
declaration order. Exact-match preference and explicit disambiguation syntax
remain open (§11).

For example, a union may contain both a concrete type and an interface that
the concrete type implements. The union declaration can be meaningful, but
injecting the concrete value may be ambiguous under ordinary compatibility
rules. A union tag must identify the selected alternative, not ask the runtime
to guess which interface or numeric conversion the producer intended.

Untyped literals need contextual checking. The explicit `i64(42)` above avoids
assuming a resolution rule for a return type such as `i32 | i64`.

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

When there is only one remaining alternative, the success case can instead be
written as `default v`. For `T | U | error`, handling only `error` leaves two
alternatives: `default` cannot pretend that either one is uniquely selected.
Whether that arm receives a residual `T | U` type is an open question (§11).

### Exhaustiveness

Reuse the existing distinction between inspecting and consuming switches:

- Expression switches must cover every alternative, explicitly or through
  `default`, and every continuing arm must yield a compatible result.
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
- Borrowing a union must not grant ownership of the original payload. Exact
  case selection must not become a way to manufacture ownership from a view.

The last point requires a precise separation between the declared alternative
used for selection and the permissions of a borrowed payload view. The spelling
of cases over such views needs to be settled before implementation (§11).

Derived borrows must preserve their source dependencies through injection,
narrowing, extraction, and branch merges. Moving or replacing the union cannot
leave a usable borrow into a former payload. An implementation should build on
the existing flow paths and alias/borrow invalidation machinery.

## 6. Alternative identity and rejected unions

For duplicate detection, compare alternative types after removing ownership
and mutability qualifiers at every level. Keep nominal type identity and the
rest of the type structure. This comparison is only a declaration-validity
rule; it is not the case-matching rule.

Reject, for example:

```boson
*T | owned *T
*T | *mut T
owned *owned *T | owned **T
```

The same rule rejects exact duplicate alternatives. Aliases must not evade it.
Distinct nominal resource types remain useful even if their storage types are
identical; `File | Socket` can describe distinct outcomes.

The handling of alternatives that differ only in nullability remains open.
It should not be implicitly decided by implementing the ownership/mutability
duplicate rule. Likewise, overlapping interfaces are different from duplicate
alternatives: matching a selected union alternative is exact, while injecting
a concrete value may require disambiguation.

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

The exact layout and ABI are not settled here. Cross-package type metadata must
carry the full alternative types and an agreed tag mapping. Different source
orderings must not accidentally disagree about the representation of what the
type checker considers the same union.

Implementation touches type parsing and identity, return/assignment checking,
switch narrowing and flow merges, payload layout/code generation, and exported
type metadata. Validation belongs inline in the existing AST construction and
compilation paths, consistent with the compiler's single-pass design. Reusing
`values` layout helpers does not make this merely an anonymous `values`
declaration: injection, type-selected cases, and payload-level narrowing are
new language behavior.

## 9. Scope and deferred syntax

The primary use is function results, but results must at least be inferable
into local bindings and passable onward. Whether explicit union types are
immediately allowed in every type position is open. There is no implicit
error propagation operator, automatic destructor, boolean success test, or
special designation of one alternative as an error.

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
- Exact qualifier matching; duplicates differing at outer and inner ownership
  or mutability levels; aliases and package-qualified types.
- Missing and duplicate cases; non-member case types; exhaustive expressions
  and consuming switches; inspecting switches that omit alternatives.
- Payload movement, reuse after move, partial consumption, branch disagreement,
  borrowed scrutinees, and borrow invalidation after replacement.
- Single-alternative `default` narrowing and handling of multiple remaining
  alternatives under the rule selected before implementation.
- Ambiguous numeric/interface injection and contextual literal checking.
- Cross-package calls with differently sized/aligned payloads, including
  interfaces, pointers, slices, and composites where supported.
- `continue` and `break` in initializer arms, including `for` increments.

Run the root Go, assembler, and language integration suites through the normal
`mmk` workflow. Do not overlap separate bosc test invocations that share runtime
fixtures.

## 11. Questions to settle before implementation

These details were not resolved by the design discussion:

1. **Type grammar and scope:** precedence of `|` relative to function types,
   pointer/slice modifiers, and multi-return commas; grouping syntax; supported
   type positions, aliases, and named wrappers around unions.
2. **Identity and nesting:** whether order matters, whether nested unions
   flatten, and how canonical identity determines exported tag mappings.
3. **Injection:** exact-match preference versus rejecting all multiple matches;
   explicit selection when interface or numeric conversions overlap; contextual
   untyped literals and `nil`; forwarding the same or a different union.
4. **Borrowed views:** which exact case spelling selects an owned declared
   alternative through a non-owning view without granting ownership, and how
   narrowing retains addressability and mutability permissions.
5. **Residual defaults:** whether multiple unmatched alternatives form a
   residual union inside `default`, or remain inaccessible as individual
   payloads until another switch.
6. **Nullability and initialization:** `T | ?T`, `?T | U`, nullable union syntax
   if any, default initialization, and definite assignment for a union whose
   alternatives have different initialization requirements.
7. **Other operations and ABI:** equality, assertions/conversions, interface
   participation, recursive layouts, global initialization, and the concrete
   layout/calling convention. Unsupported operations need directed diagnostics.

These questions should be resolved against representative callers before
implementation, without treating undocumented compiler behavior as the spec.

## References

- [Current language reference](../../DESIGN.md), particularly ownership,
  expression switches, variant switches, and interface type switches.
- [Payload-bearing values and variant-switch proposal](../implemented/PROPOSAL_adts_and_switch.md),
  for the original representation and narrowing rationale. It is a historical
  design; current consuming-switch behavior is documented in `DESIGN.md`.
- [Nullable value types proposal](PROPOSAL_nullable_value_types.md), for the
  related proposal to represent absence for types without a spare bit pattern.
  Its interaction with anonymous unions needs to be resolved alongside §11.
