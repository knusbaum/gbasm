# Proposal: Immutable-by-default bindings (`:=`), drop `const`

## Summary

Make immutable the **default** binding form and mutability the explicit
opt-in, so the short, zero-thought path is also the safe one. Today it's the
reverse: `var` is shorter, always works, and never makes you choose, so
sketching drifts to `var` even though the repo's stated philosophy is
"`const` before `var`." The ergonomic gradient points the wrong way.

Changes:

- **`:=` becomes the declaration operator**, used uniformly. `=` is
  assignment only.
- **A bare declaration is immutable; `var` marks a mutable one.**
- **`const` is removed.**
- **Expression statements must have an effect** (be a function call). This
  is good on its own *and* it removes the only parser ambiguity the change
  would otherwise introduce.
- Two checker nudges keep the annotations minimal: a `var` that is never
  reassigned is an error ("drop `var`"), and an unused binding is an error.

```
x := 5            // immutable, inferred   (the sketching default)
x i64 := 5        // immutable, typed
var x := 5        // mutable,  inferred
var x i64 := 5    // mutable,  typed
x = 5             // assignment — error if x is undeclared
var x = 5         // rejected: declarations use ':=', not '='
```

## Motivation

CLAUDE.md: the repo "prefers conservative defaults with explicit opt-out:
`const` before `var`." But with two keywords of equal ceremony, `var` wins
in practice — it's shorter, it always lets you do what you want, and
*having to choose at all* is friction you avoid while sketching by just
always picking `var`. So the default-correct path is the harder one. The
fix is to make the unmarked form the immutable one, and require a mark
(`var`) only when you actually need to mutate.

## Design

### `:=` is the declaration operator

A binding is **introduced** with `:=` and **assigned** with `=`. This is the
one axis; mutability (`var` or not) is an orthogonal second axis. The two
never overload each other:

| Form | Meaning |
|------|---------|
| `name := expr` | declare immutable, inferred type |
| `name Type := expr` | declare immutable, explicit type |
| `var name := expr` | declare mutable, inferred |
| `var name Type := expr` | declare mutable, explicit |
| `name = expr` | assign to an existing binding (error if undeclared) |

`var name = expr` (declaration via `=`) is **rejected** — declarations use
`:=`. `var x := 5` does technically double-signal "declaration" (both `var`
and `:=`), but keeping `:=` uniform across every declaration is worth more
than removing the redundancy; there is exactly one way to introduce a
binding.

Note this gives `:=` the meaning **"introduce a binding"**, *not* Go's
"declare-and-infer" — the type is an optional annotation before `:=`. A Go
programmer reading `x i64 := 5` will do a double-take; that's a conscious
divergence, and "`:=` = introduce a binding" is the cleaner mental model.

### Why `:=` and not a bare uniform `name = expr`

We considered dropping the marker entirely (immutable bare, `name = expr`
declares-or-assigns by scope, typos caught by usage-tracking). Rejected:
`:=` carries the programmer's **intent**, which lets the compiler catch a
whole error class that a markerless form cannot, *scope-independently*:

- **Meant to declare, accidentally assigned** — you forgot a name is
  already in scope. `x := 20` when `x` exists → "`x` already declared."
  A bare `x = 20` would silently mutate the existing `x`.
- **Meant to assign, accidentally declared** (typo) — `foo = x` where
  `foo` is new is unambiguously an assignment → "undeclared `foo`,"
  immediately, at the site.

It also means a declaration is visually distinct from an assignment without
reading the surrounding scope. That readability + intent-capture is real
semantics, not ceremony — which is why `:=` earns its place where a
keyword-vs-keyword choice (`const`/`var`) did not.

### Expression statements must have an effect (coupled change)

Today a bare value expression is a legal statement: `x * foo`, `5 + 3`,
`x == 5` all compile to dead code (verified). We make a statement-position
expression legal only if its top node is a **function call**. Otherwise:
`expression statement has no effect; only function calls are valid as
statements`.

This is worth doing on its own — it catches dropped calls and, notably,
`x == 5` written where `x = 5` was meant (today: a silent no-op). And it is
the key that makes the parser change trivial (below).

### Parser changes (small, no scan, no ToAST surgery)

The work today is entirely in *deciding* to parse a declaration; the type
grammar is already handled by `parseTypeName()` (stacked `*`, arrays,
`owned`/`mut`, `fn(...)` — everything), and `parseBindingDecl()` already
does "name then `parseTypeName()`." The leading `var`/`const` keyword's only
job is to signal "a declaration follows." We replace that signal with a
**one-token peek** after the leading identifier:

```
IDENT :=                          → inferred declaration
IDENT =                           → assignment
IDENT ( | . | [                   → call/assignment expression
IDENT  IDENT|owned|mut|fn|*       → typed declaration → parseTypeName, expect ':='
```

A leading `var` is consumed first, then the same peek runs. Crucially, with
the effectful-statement rule in place, **`IDENT *` is unambiguously a
declaration** — `x * foo` is no longer a legal statement, and `x * foo = …`
is not a valid assignment target — so the pointer case needs no forward
scan, no backtracking, and no reinterpretation of a parsed expression tree
in ToAST. (Statements that legitimately start with `*`, like `*p = 5`,
begin with `*`, not `IDENT *`, and are unchanged.) Add one lexer token,
`:=`; reuse `parseTypeName`/`parseBindingDecl` wholesale.

### Checker additions

Three flow-level rules turn the system **self-correcting** — the compiler
walks you to the minimal annotation from either direction:

1. **Assign to immutable → error.** `cannot assign to immutable "x"`. (The
   existing const-assignment check, retargeted to "any non-`var`
   binding.") Hitting this is the signal to add `var`.
2. **`var` never reassigned → error.** `"var x" is never reassigned; drop
   "var"`. The reverse signal — you marked it mutable but didn't need to.
3. **Unused binding → error.** `"x" is declared but never used`. Independent
   value (dead-binding / typo detection, à la Go's unused-var) and a
   secondary backstop to the structural typo defense `:=` already provides.

All three ride the existing flow-state machinery (we already track far more
than reassignment and use).

## Migration

This breaks every existing binding declaration — ~530 bosc tests, the
examples, the 28 tour lessons, and DESIGN.md (the runtime `.bs` is assembly
and unaffected). The transformation is almost entirely mechanical:

- `const name … = expr`  →  `name … := expr`
- `var name … = expr`    →  `var name … := expr`

with care for multi-bind (`var a, const b = …`) and for-loop headers.
Phasing (introduce the new syntax first, migrate, *then* tighten — so the
tree never has to change in lockstep with the compiler):

1. **Add the new syntax alongside the old.** Add the `:=` token and the
   peek-after-name classify so `:=` declarations parse; add the
   effectful-statement rule (fix any bare-expression statements it flags).
   The existing `var`/`const` + `=` forms keep working. New code can use
   `:=`; nothing breaks yet.
2. **Migrate the tree.** Mechanically rewrite `const … = → … :=` and
   `var … = → var … :=` across tests, examples, the tour, and DESIGN.md;
   spot-fix multi-bind and for-loop headers.
3. **Tighten and remove.** Reject `=` in declaration position, remove the
   `const` keyword, and *now* enable the checker nudges
   (var-never-reassigned, unused-binding) — they'd be noise against
   un-migrated code, so they come last, once the tree is already minimal.

## Open questions

These need decisions before implementation; each has a real ergonomic
fork:

- **For-loop counters. RESOLVED: no special case.** A loop counter is a
  mutable binding like any other and is written `var i := 0`. Consistency
  beats saving three characters; `var i := 0` is short and clear. No
  implicit-mutable `for`-init rule.
- **Function parameters. RESOLVED: already done — this is the precedent.**
  Parameters are *already* `const` by default with a `var` opt-in
  (DESIGN.md §1072, §221; verified: a plain param rejects reassignment,
  `var x` accepts it). This proposal brings **locals into alignment with
  parameters**, not the other way round. Params take no initializer, so
  they keep their current spelling — `x i64` (immutable) / `var x i64`
  (mutable), **no `:=`**; the `:=` operator is only for declarations with
  an initializer. The `var`-never-reassigned check applies to a `var`
  param's body like any binding; the unused-binding check **exempts**
  params and the `self` receiver (you don't control every signature — the
  Go line: unused locals error, unused params don't). `var` on a param
  stays body-local and out of the function type, as today.
- **Multi-binding. RESOLVED.** `var` is a per-target modifier (binds to the
  name after it): `a, b := f()` both immutable, `var a, b := f()` a mutable
  / b immutable, `var a, var b := f()` both mutable — mirroring today's
  per-binding mutability minus `const`. **`:=` declares all targets, `=`
  assigns all targets; no mixed declare/assign in one statement** (if `b`
  already exists, `a, b := f()` is "`b` already declared" — split it). This
  is deliberately stricter than Go's "at least one new" rule, which silently
  reassigns when you meant to declare — exactly the intent confusion `:=`
  exists to prevent.
- **File-scope / globals. RESOLVED.** Globals use the same `:=` form as
  locals — `x i64 := 5` (immutable) / `var x i64 := 5` (mutable) — with the
  existing (unchanged) requirement that the initializer be a compile-time
  constant. No distinct file-scope syntax, and **no separate
  compile-time-constant (`constexpr`) concept** — a file-scope immutable is
  just an immutable binding with a constant initializer.
- **Does `const` survive anywhere? RESOLVED: fully gone.** Every role is
  covered without it (immutable local → bare `:=`, immutable param → bare,
  immutable global → bare `:=`, compile-time constant → not a separate
  concept). Keeping it as an optional synonym would reintroduce the exact
  "should I write `const`?" micro-decision this change exists to remove, for
  no semantic payoff. Removed completely.

**All open questions resolved — the design is fully specified.**

## Non-goals

- Compile-time-constant *evaluation* semantics (`const` as "constexpr") —
  out of scope; this is purely about binding mutability and declaration
  syntax.
- Changing `mut`/`owned` (target/value mutability and ownership) — those are
  orthogonal type-level concepts and unchanged.
