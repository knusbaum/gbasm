---
title: Multiple Return Values
section: Data
order: 4
---
A function can return more than one value: list the return types separated by
commas (`i64, i64`) and `return` that many expressions.

At the call site you destructure them into bindings in one statement. The
inferred form `var q, rem = divmod(...)` introduces both at once; you can
also name types and mix `var`/`const` per binding
(`var q i64, const rem i64 = ...`).

This is how Boson returns a result alongside an error or an "ok" flag —
you'll see that pattern throughout the runtime.
