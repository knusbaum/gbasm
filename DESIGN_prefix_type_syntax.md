# DESIGN: Prefix type syntax + the mutability model

Status: **design agreed, not yet implemented.** This captures the decisions
from the design discussion so they aren't lost. The mutability model (§2) is
the durable part — it's the reference for how `mut`/`owned`/`?` work regardless
of the syntax migration; §7 is the transient implementation plan. Nothing here
is built yet except the `alloc` prerequisite (§6).

The change: move array/slice **type** syntax from postfix to prefix
(`byte[N]` → `[N]byte`, `byte[]` → `[]byte`), Go/Rust style, and settle the
`mut`/`owned`/`?` qualifier model that the prefix form exposes.

---

## 1. Why

Boson today mixes a **prefix** `*` with a **postfix** `[]`/`[N]`. That's the
ergonomic pit modern languages learned to avoid:

- `*byte[N]` *looks* like "pointer to byte[N]" but binds as `(*byte)[N]` —
  an **array of pointers** — because `[]` binds tighter than prefix `*` (same
  as C's `int *a[N]`). Pointer-to-array needs parens: `*(byte[N])`.
- A mutable pointer-to-array was effectively unspellable and confusing.

Go/Rust dodge this by putting the array dimension on the **left** too, so the
whole type reads strictly left-to-right and no parens are ever needed:
`*[10]int` = pointer to array, `[10]*int` = array of pointers. That left-to-
right reading — every modifier is a prefix that applies to everything after it
— is the target.

**Precedence isn't the thing to flip; the array dimension's *position* is.**
Making `[N]`/`[]` prefixes (like `*`) removes the ambiguity at the source.

---

## 2. The mutability model (the conceptual core)

This is the part that took the most work to get right. **`mut` is not a
property of a reference.** It is a **qualifier that binds right and marks the
type immediately to its right as mutable — at every level**, exactly like `*`
and `[]` are constructors that apply to what's on their right.

### 2.1 The outermost `mut` is `var`, and a user never writes a leading `mut`

```
var x i64 := 10      // x's full type is `mut i64` — a mutable i64
x i64 := 10          // x's type is `i64` — an immutable i64
```

An object's full type conceptually carries an outermost (binding) `mut`, but a
user **never writes it as a leading `mut`**. There is exactly one written form
for it — `var` — and it lives only at **bindings**:

- **Locals:** `var` before the name (immutable by default).
- **Params:** same — `fn f(var p T)` makes `p` rebindable; params are `const`
  (immutable binding) by default (`parseParams`, parser.go).

**A leading `mut` on a type is forbidden in every source position** (see §2.4).
The outermost mut is legal only where a type is *read*, not written — error
messages, type displays — where the compiler may render a value's full type as
`mut i64`. Rationale: allowing a leading `mut` would give two spellings of a
mutable local (`var x i64` vs `x mut i64`) and break the "scan for `var`"
invariant.

**Return types and struct fields are pseudo-declarations** — a name and a type,
with no `var` slot — so they carry **no** outer mut either, which makes them
match `var`-declaration semantics rather than being a special case:

- A **return value's** mutability is the *caller's* binding (`x := f()` vs
  `var x := f()`), so a leading `mut` on a return type is inert — forbidden.
  (Forbidding immutable-return → mutable-binding was considered and rejected: it
  would force `mut T` on nearly every function, since the whole point is that
  the caller decides.)
- A **struct field's** mutability is the containing struct *value's* binding —
  one object, mutability controlled in exactly one place (consistent with arrays
  having no inner mut, §2.3/§2.6). Interior-mutable fields (a `Cell`-style
  "writable even in an immutable struct") are a deliberate future feature, not a
  side effect of "there's no `var` here" — and you already get interior
  mutability through a `*mut` pointer field. Deferred with the owned-aggregate
  work (§2.7, item 3 / §9).

### 2.2 A pointer is just a reference

You can always dereference a pointer; there is no "writing through" as a
concept. The two things you do with a pointer:

- **Deref** → yields the pointee. Whether you can write the pointee is the
  *pointee's* mutability, written on the pointee type: `*mut i64` = "pointer to
  a mutable i64", so `*p = x` works because the **i64** is mutable.
- **Rewrite the pointer itself** (reassign) → governed by the pointer's **own**
  (outer) mutability: `mut *i64` = "mutable pointer to immutable i64" → you can
  reassign `p`, you can't write `*p`. In a decl that's `var p *i64`.

So a *mutable pointer to a mutable i64* is `var p *mut i64` — the outer
(pointer's own) mutability via `var`, the inner (pointee) via `*mut`. Note there
is **no written `mut *T`**: that would be a leading `mut` (outer mutability),
which is `var` (§2.1). "Mutable pointer" is a binding property, spelled `var`.

### 2.3 The load-bearing principle

> **A mutability boundary requires a storage boundary, which requires
> indirection.**

- **References** — `*` (pointer) and `[]` (slice) — introduce a level of
  indirection, hence a storage boundary, hence an **independent** mutability at
  that level.
- **Value aggregators** — `[N]` (array) and struct nesting — fuse their
  contents into the parent's single storage. There is no seam, so **no
  independent `mut`**; the contents share the parent's (binding's) mutability.

### 2.4 Where `mut` is legal in *written* source

| position of `mut` | qualifies | written form? |
|---|---|---|
| outermost (leading `mut`) | the binding | ✗ — **never written**; use `var` at a binding (§2.1) |
| immediately after `*` | the pointee | ✓ `*mut T` |
| immediately after `[]` (slice) | the elements | ✓ `[]mut T` |
| immediately after `[N]` (array) | — nothing independent | ✗ **rejected** |
| on a struct field's own type | — nothing independent | ✗ **rejected** |

So in written type syntax, `mut` appears **only** as `*mut …` / `[]mut …`
(inner, reference-operand). The outermost (binding) mutability is `var`, never a
leading `mut`; a leading `mut` is legal only in *read-only* renderings (error
messages, type display), never parsed from source.

`[]` (slice, a **reference**) and `[N]` (array, a **value**) look like siblings
in prefix syntax but behave **oppositely** for `mut`. That is the value/
reference distinction showing through, not a wart — and the spec must state it
loudly: `[]mut byte` legal, `[N]mut byte` illegal.

### 2.5 Slices get the reslice-vs-element distinction (both axes, one concept)

A slice is a reference, so its header mutability and its element mutability are
independent — two **levels** of the one `mut` concept:

| type | header (reslice / reassign `s`) | elements (`s[i] = x`) | declared |
|---|---|---|---|
| `[]byte` | immutable | immutable | `s []byte :=` |
| `[]mut byte` | immutable | **mutable** | `s []mut byte :=` |
| `mut []byte` | **mutable** | immutable | `var s []byte` |
| `mut []mut byte` | **mutable** | **mutable** | `var s []mut byte` |

`mut []byte` and `[]mut byte` are **both legal and different** — I was wrong to
ever call `mut []byte` an error. The `var s []byte` row is the **read-only
cursor**: `for len(s) > 0 { emit(s[0]); s = s[1:] }` — advance over data you're
not allowed to write. Today's conflated `mut` can't express it; the split falls
out for free.

### 2.6 Arrays are one object; multi-level arrays too

`[10][10]byte` is 100 contiguous bytes — the inner arrays are sub-regions of the
one value, not separate objects. So:

- `mut [10][10]byte` = the **whole** value is mutable. There is **no way** to
  make one level immutable and another mutable — there is no storage seam
  between them. (`x[i] = row` and `x[i][j] = b` are both "write into the one
  blob.")
- To get per-level control, introduce indirection so the levels are separate
  storage:
  - `var x [10]*[10]byte` — outer mutable (reassign a slot `x[i] = ptr`), inner
    **im**mutable (can't write the pointee).
  - `x [10]*mut [10]byte :=` — outer **im**mutable, inner **mutable**.
  - `var x [10]*mut [10]byte` — both.

Choosing `[N][M]T` (value) vs `[N]*[M]T` / `[][]T` (indirected) **is** the choice
between "one fused mutability" and "independently controllable levels." Good:
separability has to be asked for by introducing the thing that separates it in
memory.

### 2.7 `owned` and `?` bind right too, with their own domains

Same bind-right parse, different validity because they *mean* different things:

- **`owned`** — an obligation. Legal at **any** level (owning a value is
  meaningful, unlike mutating one): `owned *T` (own the pointer) vs `*owned T`
  (pointee owned); `owned []T` (own the backing) vs `[]owned T` (each element
  owned). Legal after `[N]` too.
- **`?`** — nullable. Binds right like the rest: **`?*T`** = nullable pointer
  to T (the `?` qualifies the `*T`), **`*?T`** = pointer to nullable-T (the
  pointee is nullable). Legal only where nullability exists — the operand of `?`
  must be a **pointer or interface**; `?i64`, `?Struct` are illegal (values
  aren't nullable). *Implementation:* `?` becomes a standalone bind-right
  qualifier, so today's combined `*?` token (`tok_maybe_ptr`) splits into
  `*` + `?`.

---

## 3. The prefix grammar (target spelling)

Constructors (build a new type, apply to the right): `*` pointer, `[]` slice,
`[N]` array. Qualifiers (annotate the right): `mut`, `owned`, `?`. All read
left-to-right; no parens needed.

| postfix (today) | prefix (target) | meaning |
|---|---|---|
| `byte[]` | `[]byte` | slice of byte |
| `byte[N]` | `[N]byte` | array of N byte |
| `byte[][]` | `[][]byte` | slice of slice |
| `mut byte[]` | `[]mut byte` | slice of **mutable** byte (**mut moves inside**) |
| `*byte[]` | `[]*byte` | slice of pointers |
| `*(byte[])` | `*[]byte` | pointer to slice |
| `*(byte[8])` | `*[8]byte` | pointer to array |
| `*mut (byte[])` (the old confusion) | `*[]mut byte` | pointer to slice of mutable byte |
| `owned byte[]` | `owned []byte` | owned slice (obligation on the whole slice) |
| `owned byte[N]` | `owned [N]byte` | owned array |

Note the migration trap: **`mut byte[]` → `[]mut byte`**, *not* `mut []byte`
(the naïve "keep `mut` in front" produces the *other*, valid-but-wrong meaning).

Illegal (must be rejected): `[N]mut T` (mut after an array specifier),
`mut *T`-legality TBD (see open items).

---

## 4. What does NOT change

The internal type model (`ASTType`: `Name`/`Indirection`/`ArraySize`/`Element`/
`MutMask`/`OwnedMask`/`NilMask`) is unaffected. This is a **pure front-end
(parse + print) change**. All codegen, type-checking, and the `.bo` binary
*layout* are downstream of the model and don't care about surface syntax.

---

## 5. Implementation surface (the compiler change)

Three functions move in lockstep, and only these:

1. **`parseTypeName`** (parser.go:423) — the core work. Today it is
   *prefix `*`/`mut`/`owned`/`?` → base name → **postfix** `[N]`/`[]` wrappers*,
   and its parse Node is **flat**: an indirection *count* + an array-wrapper
   *list*. That encodes exactly one nesting (pointers innermost, arrays
   outermost); anything else needs the parenthesized "prebuilt" escape hatch.
   Prefix makes interleaving (`*[N]byte`, `[N]*byte`, `*[]byte`) the common case,
   which the flat form can't represent — so replace it with an **ordered
   constructor/qualifier fold** (read `*`/`[N]`/`[]`/`mut`/`owned`/`?` left to
   right; build the `ASTType` inside-out), and re-derive the per-level mask bits
   in that fold. Enforce the §2.4 legality here (reject `mut` after `[N]`).
2. **`ASTType.String()`** (ast.go:2085) — invert to prefix. Preserve: the
   element-level `mut`/`owned` placement, and drop the `*(byte[])`
   parenthesization (prefix needs none). `canonicalTypeString`
   (typeinfo.go) inherits it.
3. **`mkTypename`** (ast.go:2174) — fold the ordered modifier list instead of
   "indirection + outer wrappers."

Supporting facts (verified):

- **One parser, no separate reader.** `.bo` import (`parseTypeString`,
  `parseFuncType` → `ParseFunctype`) funnels into the *same* `parseTypeName`.
  Fixing it fixes source parsing **and** `.bo` import together.
- **`bas` is opaque passthrough** — it stores type strings verbatim, never
  parses their structure (main.go). No `bas` *code* change. (But the `.bs`
  *corpus text* still contains `byte[N]` in `var`/`data`/`struct`/`type fn`
  directives and must be rewritten — see §7.)
- **`isTypeStart` gains `[`** — trivial, safe (consulted only in type
  positions, e.g. optional return types).
- **`alloc` is already done** (§6) — no array-type wrinkle remains in it.

**Format bump.** Signature strings feed `canonicalSig` → an FNV `sigHash`
written into every `.bo` and used for interface matching. New syntax → new sig
strings → new hashes, so **every `.bo` (runtime bundle `target/playground`,
examples) must be rebuilt** and there is **no old/new `.bo` interop**. Standard
"format bump → rebuild the runtime bundle."

---

## 6. Prerequisite already landed: unified type parsing

Commit `d5b8f68` (`bosc: parse alloc's argument as a type; delete the shadow
type-parser`). `alloc(T)` used to parse its argument with the *expression*
grammar and reinterpret it via `typeExprFromAST` — a second, ad-hoc type parser
that only worked because `byte[N]` also parses as an index expression. That is
now a first-class builtin whose argument goes through `parseTypeName` (the Go
`make`/`new` treatment), and `typeExprFromAST` is deleted.

Consequence for this plan: **types are parsed in exactly one place**
(`parseTypeName`). Once it learns prefix syntax, `alloc([N]byte)` "just works"
— zero special-casing. The alloc wrinkle is gone.

---

## 7. Migration strategy: clean break (decided)

**Not** dual-syntax (accept both, migrate incrementally, drop postfix later).
The reasoning that settled it:

- A **prefix-only parser turns the test suite into a complete automatic
  checklist.** A missed postfix annotation *fails to parse*, loudly and
  immediately; green ⇒ provably done. This extends through the stack — a missed
  type string in a hand-written `.bs` rides opaquely into the `.bo`, then
  bosc's import (prefix-only `parseTypeName`) chokes on it at the runtime build.
- **Dual-syntax destroys that signal.** While both parse, every missed
  conversion stays green and invisible; you only find them when you finally
  remove postfix — i.e. it *relocates* the feared pain to later, minus the
  suite's help, plus throwaway dual-grammar code and the need to define
  semantics for nonsense mixed forms. It manufactures the failure mode it
  claims to avoid.

### Blast radius (agent-measured)

| Where | Files | ~Sites |
|---|---|---|
| `cmd/bosc/tests/*.bos` | 238 (~1 in 3 test files) | ~720 |
| `DESIGN.md` | 1 | ~75 |
| `runtime/**` (.bos + .bs) | 6 | ~70 (many in comments) |
| `cmd/bas/tests/*.bs` | 8 | ~11 |
| `examples/**` | 4 | ~7 |
| **Total** | **~257** | **~900–950** |

### Scriptability

- **~700 sites are safely scriptable**: when the identifier is a reserved
  primitive (`byte`, `i8`/`i16`/`i32`/`i64`, `u8`/…/`u64`, `bool`, `any`),
  `<prim>[…]` is *always* a type — those names are never variables, so
  `<prim>[i]` can't be an index. `[]byte` alone is 500 of the bosc occurrences.
  Slice rule: `mut T[]` → `[]mut T` (mut moves **inside**); reject/flag any
  `mut T[N]`.
- **~28 need hands**: struct-typed arrays (`Point[2]`, `S8[2]` — a capitalized
  ident is ambiguous with indexing a capitalized variable) and mixed nesting
  (`byte[8][1]` → `[8][1]byte`, verify dimensions don't transpose vs the parser's
  reading direction).
- **A blind global regex is unsafe** — `[]` is overloaded four ways sharing the
  `IDENT[...]` shape and *coexisting on the same line*: type annotation
  (rewrite), indexing `arr[0]`, slicing `s[:]`/`s[lo:hi]`, array literal
  `[1,2,3]`. The trap line: `var nums i64[4] := [1, 2, 3]` (flip the type, leave
  the literal). Restrict the script to primitive-keyword hits; the suite catches
  the rest.

### Order of operations

1. **Lock the grammar spec** — the full modifier-ordering table (see §8), since
   the script encodes it.
2. **Implement** `parseTypeName` (ordered fold) + `ASTType.String()` (invert) +
   `mkTypename`.
3. **Rebuild** the runtime bundle + examples (format bump).
4. **Script** the ~700 primitive corpus sites; hand-do the ~28 + `DESIGN.md` +
   examples + runtime `.bos`/`.bs`.
5. **Run the suite → red = missed/mangled → iterate to green.** Green ⇒ done.

Land it as one logical unit; intermediate red states are fine, the tip is green.

---

## 8. Open items

**Resolved** (decisions in §2.1–2.7):

- ~~Outermost `mut` in non-declaration positions~~ → **no outer mut anywhere it
  would be written.** Leading `mut` is never user-written; `var` is the sole
  binding-mutability form (locals/params); return types and struct fields are
  pseudo-declarations with no outer mut (their mutability is the caller's
  binding / the struct's binding). §2.1, §2.4.
- ~~Is `mut *T` legal?~~ → **No.** `mut *T` is a leading `mut` (outer); a mutable
  pointer is `var p *T`. §2.2.
- ~~`owned` after `[N]`~~ → **Legal.** `owned` binds right, valid at every level;
  the *semantics* of owned arrays/slices is deferred (§9). §2.7.
- ~~`?` on slices / `?` placement~~ → **`?` binds right; operand must be a pointer
  or interface**, so slices stay non-nullable. `?*T` nullable pointer, `*?T`
  pointer to nullable. §2.7.
- ~~Redundant forms~~ → the one is `var` + leading `mut`, killed by "leading `mut`
  never written." `[N]mut T` rejected, `[N]owned T` allowed. No others known.

**Still to produce before parser code:**

1. **Full modifier-ordering enumeration** — the exact legal/illegal grammar for
   `owned`/`mut`/`?` × `*` × `[]`/`[N]`, with nested cases worked through:
   `*[]mut byte`, `[]*mut byte`, `[][]byte`, `[]owned *mut T`, `[N]*mut T`,
   `owned [N]T`, `[N]owned T`, etc. The §2 rules fully determine each case; this
   is just writing them all out as the concrete table the parser and migration
   script are built against. **This is the one remaining artifact.**

**Implementation checks (not decisions):**

- The parser must **reject** a leading `mut` from source while the printer **may
  emit** it (read-only renderings). These don't collide on the `.bo` round-trip
  because binding mutability is a **separate flag**, not a `MutMask` bit, so
  serialized type strings carry no outer mut. Verified: locals/params use
  `Binding.IsConst`/`isVar`; globals use `IsConst` too (immutable global is
  `g T := …`, mutable is `var g T := …`, enforced in-package). The `.bo` `Var`
  record (`ofile.go`) has `VType` but **no** mutability field.
- Split the combined `*?` token (`tok_maybe_ptr`) into `*` + `?`.

**Orthogonal, noted for completeness:** a global's mutability *is* observable
cross-package (can I write `otherpkg.g`?), but it is **not** carried in the
`.bo` today — `TODO(cross-pkg-const)` at `compile.go:1754`, so imported globals
are currently treated as writable. The fix is a separate `Var.IsConst` boolean
field threaded through import, **not** a leading `mut` in `VType`. Independent
of this change; can land before/during/after.

---

## 9. Deferred, related (not part of this change)

- **Runtime-sized allocation** (no `make`, no slice-from-pointer). Folds into
  the dynamic-non-null-collections design discussion (capacity-vs-length,
  append/remove primitives) — a separate item.
