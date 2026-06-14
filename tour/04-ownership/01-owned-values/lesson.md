---
title: Owned Values and Obligations
section: Ownership
order: 1
---
Ownership is Boson's way of tracking cleanup at compile time, with no garbage
collector. Declaring a value `owned` gives it an **obligation**: exactly one
place is responsible for consuming it, and the compiler checks that every
path through your code does so.

The rule that drives everything: **`owned` in a parameter moves.** If a
function's parameter type says `owned`, passing a value to it transfers the
obligation — the caller's binding is consumed and can't be used again.

Here `commit` takes `owned i64`, so `commit(ticket)` hands off `ticket`.
After that line, `ticket` is gone — and that's exactly why the program is
sound. Try adding a use of `ticket` after the call.
