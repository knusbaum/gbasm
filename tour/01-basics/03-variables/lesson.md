---
title: Variables
section: Basics
order: 3
---
A binding is introduced with `:=`. By default it is **immutable** — `limit
:= 10` names a value that can't be reassigned. Immutability is the default
because it's the common, safe case; you opt into mutability only when you
need it, by marking the binding `var`: `var count := 0` can be reassigned
later.

The type is inferred from the initializer, or you can write it explicitly:
`limit i64 := 10`, `var count i64 := 0`.

Here `count` is reassigned, so it's a `var`; `limit` never changes, so it's
left as the bare, immutable form.
