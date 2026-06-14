---
title: Multiple Return Values
section: Data
order: 4
---
A function can return more than one value: list the return types separated by
commas (`i64, i64`) and `return` that many expressions.

At the call site you destructure them into bindings in one statement. The
inferred form `q, rem := divmod(...)` introduces both at once (both
immutable). Mutability is per target — `var q, rem := ...` makes `q`
mutable and leaves `rem` immutable — and you can name types too
(`q i64, rem i64 := ...`).

This is how Boson returns a result alongside an error or an "ok" flag —
you'll see that pattern throughout the runtime.
