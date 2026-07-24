# Nullable value types: `?` on any type, one meaning, hidden representation

## History

- 2026-07-23 — Proposed. Grew out of noticing that `?owned FD` (and even `?i64`)
  compile today but are unsound — `?` is applied to any base type with no
  validation, and the pointer world's `0`-is-nil convention is wrong for a scalar
  where `0` is a legitimate value (an `FD` of `0` is stdin). Rather than just
  reject `?` on value types, extend it to mean the same thing everywhere.

---

## Motivation

`?` is the nullability marker. Today it is **specified** only on pointers (`?*T`,
nil = a null pointer) and interfaces (`?T`, nil = a null vtable) — both of which
have a **niche**: a reserved bit pattern (`0`) that no valid value uses. The
compiler, however, applies `?` to *any* base type (the parser sets the nullable
bit unconditionally), so `?owned FD` and `?i64` typecheck. They are **unsound**:
they borrow the pointer world's "`0` means absent" convention, but `0` is a
perfectly valid `i64` and a valid file descriptor (stdin). "Absent" becomes
indistinguishable from the value `0`.

The fix is not to forbid `?` on value types — it is to make it *mean something*.
Being able to say "this function **maybe** returns a value, and the caller
**must** check for presence before using it" is both more consistent (`?` should
work on everything) and broadly useful (fallible lookups, optional fields,
parse-or-nothing) — and Boson already has the flow machinery to enforce the
check. This proposal makes `?T` a first-class nullable value type with **exactly
the semantics of `?*T`**, and an implementation the language never exposes.

---

## The one principle: uniform meaning, hidden representation

To the programmer there is **one `?`**. `?T` and `?*T` behave identically:

- A non-nullable value flows into a nullable one for free (`T` → `?T`, `*T` →
  `?*T`).
- A `?T` **must be narrowed before use** — you cannot read the value out of an
  un-narrowed `?T` any more than you can dereference an un-narrowed `?*T`.
- `if (x) { … }` narrows `x` to present in the true branch; `if (!x) {…} else
  {…}` narrows it in the else; postfix `x?` inserts a runtime presence check and
  yields the plain `T`, trapping if absent.
- `?owned T` carries a **conditional** obligation: absent ⇒ nothing to discharge,
  present ⇒ the value must be consumed. Exactly as `?*owned T` works today.

**The representation is not part of the language.** Whether a `?T` is a pointer
whose `0` means absent, or a value carried alongside a presence flag, is a
compiler decision the source can never observe. There is no struct, no `.ok`
field, no `.value` field, no nominal type — `{ value, ok }` is only a convenient
way to *describe* "a value together with a present-flag" for the value case; it
never has struct semantics and is never nameable or accessible. The programmer
writes `?T` and checks presence; nothing else is exposed.

---

## Representation (compiler-internal, niche-optimized)

Because representation is invisible, the compiler picks the cheapest correct one
per type — a **niche optimization**, the same idea as Rust's `Option`:

- **`T` has a niche** (a pointer or an interface): reuse it. `?*T` stays exactly
  a pointer, `0` = absent; `?interface` stays a fat pointer, `vtable == 0` =
  absent. **Zero cost, unchanged from today.** Nullable pointers are still just
  zero-value pointers.
- **`T` has no niche** (a scalar like `i64`/`FD`, a struct value): carry the
  value plus a presence discriminant. `?T` is then wider than `T`. This is the
  new case, and it is the *only* sound representation for a type where every bit
  pattern is a valid value.

The choice is a predicate `hasNiche(T)`. It affects only the leaf codegen
(construct / test / assert / project); everything above it is representation-blind.

---

## What is actually new (and what already exists)

The subtle part — **flow narrowing** — is already built and is
representation-agnostic. The checker tracks a *fact* ("this binding is known
present here"), not a bit pattern: the `if`-narrowing, the `else`-complement, the
postfix-`?` assertion, and the conditional-obligation tracking for
`?owned`/nullable-owned all key on that fact. They carry straight over to `?T`.

Only the **leaf codegen** branches on `hasNiche(T)`:

| Operation | pointer/interface (niche) | value (discriminant) |
|---|---|---|
| construct present (`T → ?T`) | store the pointer/value | store value; set present-flag |
| construct absent (`nil`) | store `0` | clear present-flag (value undefined) |
| test presence (`if (x)`) | compare to `0` | read present-flag |
| assert (`x?`) | trap if `0`, yield pointer | trap if absent, yield value |
| project narrowed value | the pointer itself | read the value field |

So this is a codegen extension keyed on a type predicate, not a new type-system
concept. `?owned FD` finally means what it should: present ⇒ you owe a `close`,
absent ⇒ you owe nothing.

---

## Implementation notes and touchpoints

- **Size / ABI.** A discriminant-carrying `?T` is larger than `T` (value + flag +
  padding). This rides `ASTType.Size()`, so allocation/copy/field-offset logic
  auto-follows (the same lever the slice-header widening used) — but passing a
  value-optional as an argument, returning it, and embedding it in a struct are
  genuinely new layouts to get right. Pointer/interface optionals are unaffected.
- **The absent literal.** `nil` currently means "the `0` pointer." For a value
  optional it must mean "clear the present-flag" (the value bits are then don't-
  care). Reusing `nil` as the single absent literal across representations is the
  natural choice and keeps the surface uniform; the codegen resolves it per type.
- **`NilMask`.** Today the nullable bit is meaningful at *pointer* levels. A `?`
  at a value level (bit 0 on a non-pointer) becomes the signal to select the
  discriminant representation — the one place the layout dispatch lives. The
  prefix type grammar (`?owned FD`) already parses; this gives that parse a sound
  meaning.
- **Owned interaction.** `?owned T` composes with the existing nullable-owned
  obligation tracking (obligation conditional on presence; branch-narrowing to
  absent discharges it, exactly like the `owned ?*T` handling).
- **Validation, meanwhile.** Until this lands, `?` on a value type is a silent
  unsoundness. The interim options are (a) reject `?` on non-pointer/non-interface
  types with a directed error, or (b) leave it as a known bug this proposal
  closes. Either way the resolution is recorded here, not left ambient.

---

## The long-term framing: `?T` is the smallest sum type

`?T` is a two-variant tagged union — `present(T) | absent`. If sum/enum types are
on the roadmap, the *most* consistent implementation is to build `?T` on that
machinery (the tag, exhaustive narrowing, variant layout) rather than a bespoke
discriminant, so there is only **one** discriminant mechanism in the language.
Niche optimization then generalizes to "a variant whose payload has a niche pays
no tag," of which nullable pointers are the special case. If sum types are not
near-term, the dedicated value-optional representation above is the pragmatic
build and nothing is lost — the leaf codegen is small and can later be re-expressed
over sum types. **This proposal should be sequenced against the sum-type
decision**, to avoid shipping two discriminant mechanisms that must later be
reconciled.

---

## Open questions

1. **Sum types first?** Whether to build `?T` standalone now or on a future
   sum-type foundation (see above). The answer gates the implementation shape.
2. **Absent literal.** Reuse `nil` for the value-optional absent case, or a
   distinct `none`? `nil` keeps `?` uniform; a distinct word disambiguates "null
   pointer" from "absent value" for readers. Leaning `nil`.
3. **User-declared niches.** A later optimization: let a `type` declare an
   invalid value (`type FD i64 invalid -1`) so `?FD` reclaims the discriminant
   space by reusing the sentinel. Strictly additive on top of this design; not
   part of it.
4. **Nesting and composition.** `??T`, `?owned ?*T`, `[]?T`, `?T` as a struct
   field — each needs a defined layout and narrowing story once the value
   representation exists. Most fall out of the per-level mask + `hasNiche`
   dispatch, but the value-optional-as-struct-field case wants explicit test
   coverage (it changes the containing struct's layout).
5. **Value-alias tracking.** `?owned` value scalars interact with the owned-scalar
   value-alias machinery (the `Origin`/move tracking for `owned i64` et al.);
   confirm the conditional-obligation and re-init paths compose with narrowing.

---

## Summary

Make `?` mean one thing on every type: *maybe a value, checked before use*. Keep
that meaning identical to `?*T`, and keep the representation entirely inside the
compiler — a niche where one exists (pointers/interfaces, unchanged and free), a
hidden value-plus-flag where none does. No struct is ever real; the flow
machinery that enforces the check already exists. The result is a consistent,
genuinely useful "optional value" with no new surface concept beyond the `?` the
language already has.
