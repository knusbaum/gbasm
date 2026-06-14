# Boson TODO

A lightweight backlog of changes we want but that don't yet have a full
`PROPOSAL_*.md`. Keep entries short — just enough context to pick the work
up later. Promote an item to a `PROPOSAL_*.md` when it's ready to design.

---

## Language

### Expanded boolean operations
Booleans are thin today: a `bool` is produced only by a comparison
(`<`, `==`, `!=`, …) or `!` (logical not). There are **no `&&` / `||`
short-circuit operators** and **no `true` / `false` literals**. Want to
flesh this out — at least `&&`/`||` (with short-circuit evaluation) and
boolean literals. Decide whether `&`/`|` stay integer-only or also act on
`bool`.

### Compound assignment (`+=` and friends)
`x = x + 1` is the only form. Add `+=`, `-=`, `*=`, `/=`, etc.
*Integration note:* whatever lands must mark its target as a reassignment
for the never-reassigned and unused-binding checks — trivial if it
desugars to `x = x <op> v`; otherwise one `markMutRelied` + `markUsed`
call on the target.

### Bitwise operator completeness  *(noticed, not yet requested)*
Only `&` and `|` are tokens. Missing: `^` (xor), `<<` / `>>` (shifts),
`~` (bitwise not). Found while writing the integers tour lesson — the
lesson currently states only `&` and `|` exist, which is accurate but
thin.

---

## Tour

### Command-line arguments lesson
The tour never teaches `fn main(args byte[][])` (argv) or `fn main() i64`
(exit code) — every lesson uses `fn main()`. Add a short lesson once
slices and strings are introduced (Data section), then drop the
forward-reference now sitting in the Functions lesson.
