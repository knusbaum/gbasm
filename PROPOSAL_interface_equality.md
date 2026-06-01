# Proposal: Equality Comparison for Interface Values

## Status

Proposed. Not implemented.

## Summary

Define `==` and `!=` on interface-typed values, and extend Boson's existing
implicit concrete-to-interface coercion so it fires in comparison contexts.
After this change:

```boson
fn whatever() error {
    return io_err.WHATEVER     // after error uses a value-receiver shape
}

fn do_thing() {
    var e = whatever()
    if (e == io_err.WHATEVER) {  // new: same coercion fires on the RHS
        // matched
    }
}
```

The runtime semantics for `a == b` between two interface values are
"same dynamic type *and* same interface data word", evaluated as a
typedesc-pointer compare followed by a data-word compare. The typedesc is a
new per-concrete-type global emitted exactly once in T's owning package; the
vtable gains a new slot 0 that holds the typedesc pointer, so all
consumer-emitted vtables for a given (T, I) agree on T's identity regardless
of where the coercion happened. The comparison of an interface value against
a concrete operand desugars to `i == (I)(c)` — the same implicit coercion the
language already performs at every other interface-shaped position.

Both value-backed and pointer-backed interface comparisons use the same
machine rule: compare dynamic type first, then compare the interface `data`
word. For value-backed interfaces, `data` is the concrete value bits. For
pointer-backed interfaces, `data` is the concrete pointer, so equality is
pointer identity after the dynamic-type check.

## Motivation

Boson has two related friction points that this one feature resolves.

### Cleaner error-returning APIs

The `io` package currently has functions like `copy(dst writer, src reader)
io_err`. Returning `io_err` is awkward for two reasons:

- The caller often does not care which specific error the operation produced
  — they care whether it failed. `io_err`-returning signatures push package-
  specific detail onto every consumer.
- Even when the caller *does* care, the pattern they want to write is
  symmetric to Go's `err == io.EOF`. The natural Boson form, `e ==
  io_err.X`, currently does not compile when `e` is the more general `error`.

Today the workaround is to return `io_err` directly so the caller can match
on the concrete tag. That is precise but it leaks the producer's choice of
error type into every signature, and removes any room for a function to
synthesize errors from multiple sources.

The right shape is for `copy` to return `error` (and let any concrete type
that satisfies `error` flow through), with cheap caller-side matching on
known values:

```boson
pub fn copy(dst writer, src reader) error { ... }

var e = io.copy(out, in)
if (e == io_err.EOF) { return }
if (e == io_err.EIO) { ... }
```

**Current receiver-shape caveat.** In the compiler today, value-backed
interface coercion is valid only when the target interface's methods use
value receivers. `io_err` currently implements `message(e io_err) byte[]`,
while `builtin.error` currently declares `message(e *self) byte[]`. That
means `io_err` does not satisfy the current `builtin.error` interface as a
value-backed type. Landing the `io.copy(... ) error` migration therefore also
requires changing the standard error interface to a value-receiver shape (or
introducing a separate value-error interface and using that here). The
equality mechanism below assumes the target interface is one that `io_err`
actually satisfies.

### bdoc's free-function classifier

`internal/bdoc/scan.go` groups a free function under a local type when the
function returns exactly that type (and no other local type). The intent is
to surface `T` and its constructors together — `fn open(...) FD` shows up
under `FD`, which is correct.

The current rule miscategorizes free functions whose return *carries
information about failure* but whose purpose is not "construct one of
these". `io.copy` returns `io_err` and gets grouped under `io_err`, suggesting
it is an error constructor, which it is not.

Earlier discussion considered name-based heuristics and special-casing
"types that satisfy `builtin.error`". Both were rejected: name heuristics are
unreliable, and special-casing requires bdoc to know about specific stdlib
identifiers.

The fix proposed here is upstream of bdoc: change `copy`'s return type to
`error`. Since `error` lives in `builtin` (not in `io`), bdoc's existing
"single local type returned" rule naturally excludes it — `error` is not a
local type of the producing package. No bdoc change is required.

## Goals

- Define `==` and `!=` between two interface values of the same interface
  type, with **same-dynamic-type-and-same-data-word** semantics.
- Extend the existing implicit concrete-to-interface coercion to fire on
  either operand of `==` / `!=` when the other side is interface-typed, so
  `e == io_err.X` parses and type-checks.
- Reject the comparison at compile time when the concrete operand does not
  satisfy the interface (just as any other implicit coercion would).
- Define the interface `data`-word half of equality uniformly: value-backed
  interfaces compare the stored concrete value bits; pointer-backed
  interfaces compare the stored concrete pointer.

## Non-goals

- **No general value-equality contract on arbitrary types.** This proposal
  does not say anything new about `==` on user structs or slices. The
  comparison happens through the interface representation's 8-byte `data`
  word.
- **No `match` / type-switch construct.** A dedicated form for
  matching an interface value against a list of concrete tags is the natural
  follow-on (see [Future Extensions](#future-extensions)), but is not part
  of this change.
- **No `nil` semantics for interface values.** Interface values are not
  nullable today. If a future `?error` form is added, its comparison rules
  reuse the existing nullable comparison machinery and are independent of
  what this proposal defines.
- **No structural equality for pointer-backed interfaces.** Pointer-backed
  interface equality is pointer identity, not a recursive comparison of the
  pointed-to concrete object.

## Core Rule

### Interface ↔ interface equality

For two interface values `a` and `b` of the same interface type `I`
(ignoring ownership qualifiers, which are compile-time-only),
`a == b` evaluates to true iff:

1. The dynamic types of `a` and `b` are identical, **and**
2. The 8-byte `data` words stored in the interface representations are
   identical.

The dynamic-type check loads the vtable pointer from each fat pointer's
second word, dereferences slot 0 (the typedesc pointer described in
[Typedesc and vtable layout](#typedesc-and-vtable-layout)), and compares
those two pointers. Each concrete type T owns exactly one typedesc global,
emitted in its home package, so typedesc-pointer equality is dynamic-type
equality regardless of which consumer emitted the surrounding vtable.

The data-word check is an 8-byte compare of the `data` field. For
value-backed interface values this compares the stored concrete value bits.
For pointer-backed interface values this compares the stored concrete
pointer, so two interfaces wrapping the same object compare equal and two
interfaces wrapping byte-identical but distinct objects compare unequal.

`a != b` is the boolean inverse.

### Concrete-to-interface coercion in comparison position

The existing rule that an expression of type `T` is implicitly coerced to
interface type `I` at any position that requires `I` (assignment,
parameter, return) is extended to comparisons:

- `i == c` where `i : I` and `c : T` is well-typed iff `T` satisfies the
  underlying interface type of `I`, ignoring ownership qualifiers. It is
  rewritten to `i == (I_without_owned)(c)` and follows the
  interface-interface rule above.
- Symmetric: `c == i` is rewritten to `(I_without_owned)(c) == i`.
- If neither operand is interface-typed, this proposal changes nothing.
- If both operands are interface-typed but their interface types differ,
  after stripping ownership qualifiers, the comparison is rejected at compile
  time. (We do not introduce cross-interface equality.)

This is exactly the pattern Boson already uses for assignment:
`var e error = some_error_value` works via implicit coercion when the concrete
type satisfies `error`; `e == some_error_value` should work for the same
reason.

### Representation-specific meaning of the data word

The comparison itself is defined for both interface representations described
by [DESIGN.md §Interfaces](DESIGN.md):

- **Value-backed:** the concrete type is ≤ 8 bytes and every method of the
  interface declares a value receiver. The `data` word stores the concrete
  value bits, sign- or zero-extended to 8 bytes by the existing coercion path.
- **Pointer-backed:** the source coerced into the interface is a pointer (or
  otherwise uses the pointer-backed interface representation). The `data`
  word stores that pointer.

In both cases, after typedesc equality succeeds, `==` compares the raw 8-byte
`data` word. That means pointer-backed equality is intentionally pointer
identity rather than structural value equality.

One important consequence: a values type such as `io_err` is value-backed
only when the target interface uses value-receiver method signatures
(`message(e self) byte[]`). It does **not** satisfy an interface whose method
expects `*self` unless the source is a pointer form that can be passed to the
pointer receiver.

## Typedesc and vtable layout

Today (`cmd/bosc/ast.go:567`, `compile.go:4538`) the compiler emits one
vtable global per (T, I) coercion site, with name `__vtable_<T>__<I>` and N
8-byte slots holding relocations to `pkg.T.method` symbols in interface
declaration order. Vtables are emitted in the **consumer** package — the
compilation unit that performed the coercion — and the linker qualifies
var names by package (`linker.go:171`), so two packages that both coerce
the same concrete type to the same interface produce two distinct vtable
globals at two distinct addresses in the same binary.

That is fine for dispatch (the contents agree), but the **address** of
the vtable cannot serve as a stable identity for T at comparison time:
interface values produced by `app` and `lib`, both wrapping the same
concrete type through the same interface, would have different vtable
pointers and would falsely compare not-equal.

To fix this without a new linker feature, introduce a per-concrete-type
descriptor global emitted in T's home package:

```
data __typedesc_io_err byte[1] "\0"
```

One typedesc per named type, emitted unconditionally by the package that
declares the type. Emit it via the `data` directive (read-only) rather than
`var` — the bytes are immortal and never read at runtime in the MVP, so
landing it in the read-only section is the correct shape and costs nothing.
The initial contents are a single zero byte — the typedesc **address** is
the identity. Nothing inside it is read at comparison time.

The cross-package canonicalization story falls out of the existing linker
machinery: every vtable's slot-0 reloc names `<pkg>.__typedesc_<T>`, which
resolves through `linker.go`'s data-relocation path: symbols are registered
under qualified names (`linker.go:162`–`176`), vtable `DataReloc` targets are
followed when the vtable is placed (`linker.go:248`–`267`), and final absolute
addresses are written into the vtable bytes after layout (`linker.go:342`–`366`).
All vtables that wrap the same concrete type therefore point at the single
typedesc global emitted by T's home package; no linker change, no
comdat/linkonce mechanism, no symbol dedup logic is needed.

Each vtable gains one extra slot at offset 0 holding a relocation to the
appropriate typedesc:

```
var __vtable_io_io_err__builtin_error byte[(N+1)*8] {
    reloc 0       io.__typedesc_io_err 0
    reloc 8       io.io_err.message     0
    ; ...more method slots at 16, 24, ...
}
```

Method dispatch indices shift by one slot: methodIdx 0 now lives at
offset 8, not offset 0. `compileInterfaceMethodCall` and the existing
`mov [vtablePtr+methodIdx*8]` arithmetic gain a `+8` (or are rewritten to
`(methodIdx+1)*8`).

The interface fat-pointer layout is unchanged — still `[data, vtable]`,
16 bytes. The change is entirely inside the vtable.

### Why not inline the typedesc in the fat pointer?

An alternative layout would grow the interface value to 24 bytes —
`[data, vtable, typedesc]` — so the typedesc pointer is one memory load
away instead of two (fat-pointer load, then vtable slot-0 dereference).
Single-load compare on `==` and `is`, no chase through the vtable.

The tradeoff is permanent and ubiquitous: every interface value in the
language grows by 50%. Every parameter, return slot, struct field, and
local interface variable pays the cost on every operation, while only the
comparison and type-tag-check paths benefit from it. Method dispatch is
unaffected — it already loads the vtable pointer, and adding the typedesc
slot at vtable[0] costs nothing on the dispatch path because dispatch
never reads it. The proposal keeps the 16-byte fat pointer and accepts
the extra load on compare.

> **Lockstep landing requirement.** The vtable-layout change (adding the
> typedesc slot at offset 0) and the dispatch-arithmetic change (shifting
> `methodIdx*8` to `(methodIdx+1)*8` in `compileInterfaceMethodCall`)
> **must land in the same commit**. With only the layout change, every
> interface dispatch would call the typedesc address as if it were a
> function pointer and crash. With only the arithmetic change, every
> dispatch would skip past the last real method and read off the end of
> the vtable. The test suite catches either condition instantly, but the
> two edits are textually distant (one in `ast.go::WriteVtables`, one in
> `compile.go::compileInterfaceMethodCall`), so the proposal flags this
> explicitly. There is no incremental path: a feature flag would have
> to gate both edits together, which defeats the point.

### Future contents of the typedesc

The initial implementation stores nothing useful inside the typedesc. The
address is the contract. Plausible future additions, none required for this
proposal:

- Type name as a C-string, for stringification in diagnostics.
- Size and alignment, for any generic helper that wants to act on
  interface values without static knowledge of T.
- A destructor function pointer, useful if `owned` interface disposal
  ever wants a path that doesn't go through a vtable slot.
- Pointers into the per-type method set, the start of a reflection-like
  facility.

These can be added later without breaking the equality contract: the
typedesc's address remains the identity regardless of what bytes it
carries.

## Worked Example

Today's `io.copy`:

```boson
pub fn copy(dst writer, src reader) io_err {
    var buf [1024]byte
    ...
}
```

After this proposal lands, the signature becomes:

```boson
pub fn copy(dst writer, src reader) error {
    var buf [1024]byte
    ...
    if (err != io_err.OK) {
        return err  // implicit coercion after error uses a compatible receiver shape
    }
    ...
    return io_err.OK
}
```

Callers gain the ability to match on known concrete tags without losing
the option to receive an arbitrary error:

```boson
var e = io.copy(out, in)
if (e == io_err.OK) {
    return io_err.OK
}
if (e == io_err.EIO) {
    log("short write")
    return e
}
// Bubble up anything else as the generic error interface.
return e
```

`e == io_err.OK` desugars to `e == (error)(io_err.OK)`, which evaluates as:

1. Load `e`'s vtable pointer (`[e+8]`) and dereference slot 0 to get
   `e`'s typedesc pointer.
2. Compare against `io.__typedesc_io_err` (a static address known at
   compile time, since the RHS is the literal `io_err.OK`).
3. If unequal → result is false.
4. If equal → compare `e.data` (8 bytes) to the value-backed representation
   of `io_err.OK` (the tag).

### Bonus: bdoc classification falls out for free

`copy` now returns `builtin.error`, which is not a local type of the `io`
package. `internal/bdoc/scan.go`'s "returns exactly one local type" rule
no longer associates `copy` with `io_err`, so it lands under "Functions" at
the package level — where a reader would expect to find it. No bdoc code
change is needed.

## `.bs` and `.bo` impact

No new directives or shape fields. Two existing emission paths change:

- Every named type now causes its declaring package's `.bo` to contain
  one new local `data __typedesc_<T> byte[1] "\0"` global. Cross-package users
  reference it with the normal package qualifier, e.g. `io.__typedesc_io_err`.
  The producer-side `compileTop` for `type` declarations is the natural
  emission site.
- Vtable emission (`WriteVtables` in `cmd/bosc/ast.go`) prepends a
  typedesc reloc at offset 0 and shifts method-pointer relocations
  to offsets `(idx+1)*8`.

Method dispatch shifts in lockstep: `compileInterfaceMethodCall`
(`compile.go:4336`) reads `[vtablePtr + (methodIdx+1)*8]` instead of
`[vtablePtr + methodIdx*8]`. The interface fat-pointer layout — 16 bytes,
`[data, vtable]` — does not change.

The compiler emits the comparison inline at the call site. For an
interface-vs-concrete compare with the concrete operand known at compile
time (the common case, `e == io_err.X`):

```
; e is in [rsp+e_data], [rsp+e_vtable]
; rhs typedesc address: io.__typedesc_io_err  (static, lea-able)
; rhs data word:        io_err.X tag          (immediate)
mov  rax [rsp+e_vtable]    ; vtable ptr
mov  rax [rax+0]           ; typedesc ptr from slot 0
lea  r10 io.__typedesc_io_err
cmp  rax r10
jne  .neq
mov  rax [rsp+e_data]
cmp  rax <io_err.X tag>
jne  .neq
.eq:
```

For interface-vs-interface compares both sides go through the
`[vtable+0]` indirection.

## Implementation impact

Layered per the working-notes ordering in `CLAUDE.md`:

1. **Lexer** — no changes.
2. **Parser** — no changes. `==` / `!=` already parse for arbitrary
   expressions.
3. **AST** (`cmd/bosc/ast.go`) — extend the comparison node's
   ToAST/type-check path to recognize the `interface_t vs concrete_t`
   shape. The existing interface-coercion entry point (the one called by
   assignment and return) takes a target interface type and a source
   expression; reuse it from the comparison path.
4. **Checker** (`cmd/bosc/checker.go`) — confirm the concrete side
   satisfies the interface (already-existing predicate), using the same
   representation decision as ordinary interface coercion. Reject otherwise
   with a directed error pointing at the concrete operand.
5. **Typedesc emission** (`cmd/bosc/compile.go` or `globals.go`) — at
   `type` declaration sites, emit a `data __typedesc_<T> byte[1] "\0"`
   global (read-only) into the declaring package's `.bo`, using the same
   dot-free local symbol spelling rule as vtable names. One per named type
   (struct, values, type alias), unconditional. Cost is one byte per type
   in the producer's `.bo`; total binary impact is negligible.
6. **Vtable emission** (`cmd/bosc/ast.go::WriteVtables`) — bump the
   global's size from `N*8` to `(N+1)*8`; emit `reloc 0 <pkg>.__typedesc_<T> 0`
   at offset 0; shift all method relocs to offset `(idx+1)*8`.
7. **Method-dispatch codegen** (`cmd/bosc/compile.go::compileInterfaceMethodCall`)
   — adjust the slot arithmetic from `methodIdx*8` to `(methodIdx+1)*8`.
   This is a one-line change but must land in lockstep with step 6 to
   keep existing tests passing.
8. **Equality codegen** (`cmd/bosc/compile.go`) — emit the two-stage
   compare described in `.bs` and `.bo` impact above. Reuse the existing
   interface coercion machinery to materialize the concrete operand's
   typedesc and data word in the same representation normal coercion would
   use.
9. **bas** — no changes.
10. **Runtime** — no changes for the comparison itself. As a separate
    migration step, change `io.copy` (and any other `io_err`-returning
    function whose purpose is "do an operation that may fail" rather than
    "construct an `io_err`") to return `error`.

The migration step is mechanical once the standard error interface has a
receiver shape that `io_err` satisfies. The existing `io_err.OK`
sentinel-on-success convention continues to work because `io_err.OK`
coerces to that interface the same way any other case does.

## Tests

The `cmd/bosc/tests/` suite gains:

- A positive test: declare an interface, two satisfying value-backed
  concrete types, and verify `==` between an interface variable and a
  concrete operand yields the correct booleans for matching and
  non-matching cases.
- A positive test: same as above but the interface variable was
  constructed from a different concrete type than the RHS, so the dynamic-
  type check makes the comparison false.
- A positive test: equality across two interface variables of the same
  declared type, one matched-pair and one mismatched-pair.
- A positive test: pointer-backed interface equality compares pointer
  identity after the typedesc check.
- A negative test (`*_err_test.bos`): comparison with a concrete operand
  that does not satisfy the interface — expect a coercion-failure
  diagnostic.
- A negative test: comparison between two interfaces of different declared
  types — expect a type-mismatch diagnostic.

## Open Questions

### Interface ↔ nullable interface

When (and if) Boson grows nullable interface types (`?error`), the
nullable comparison machinery extends naturally: `?I == I` and `?I == c`
either short-circuit on null on the LHS or fall through to the rule above.
This proposal does not pre-commit to that work.

### Interaction with the move checker

Comparison reads the interface binding but does not consume it. Specifically,
`e == io_err.X` does not affect ownership of either operand. Ownership
qualifiers are not carried in the runtime interface representation, so
comparison strips them for type compatibility and then compares typedesc plus
the `data` word. No `MoveConsume` / `MoveTransfer` is invoked.

## Future Extensions

- **Type-switch / `match` construct.** Once `==` exists, the natural shape
  for multi-arm matching is a dedicated form, e.g.

  ```
  match e {
      io_err.OK    => return,
      io_err.EIO   => log("short write"),
      _            => return e,
  }
  ```

  A `match` form can lower to a chain of vtable+data compares, but with
  the compiler doing exhaustiveness analysis where possible. This is
  worth doing once two or three codebase sites exhibit the chained-`if`
  shape from the worked example.

- **`is` operator for type-tag-only checks.** A form like `e is io_err`
  that tests only the dynamic-type half (no data comparison) is useful
  when the caller wants to handle "any io error" generically. The
  runtime support is **already in the box** once equality lands —
  `is` is exactly the typedesc-compare half of `==` with the data-word
  step elided. Adding it later is a parser/AST/codegen tweak (recognize
  the keyword, type-check the RHS as a concrete type name, emit the
  one-stage compare); no new vtable layout, no new runtime concept, no
  new linker work. Listed here only because it does not need to ship in
  the same change as `==`.

- **Pattern destructuring.** A `match` form could eventually bind the
  concrete value with type narrowing (`io_err.EIO`-arm binds `err` as
  `io_err`). Not part of this proposal.
