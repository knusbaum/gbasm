---
title: Variables
section: Basics
order: 2
---
You bind a value to a name with `:=`. By default the name is **immutable** -
`limit := 10` can't be reassigned. Immutability is the default because it's
the common, safe case. When you need a name you *can* reassign, you mark it
`var` (a **variable**): `var count := 0` can change later.

The type is inferred from the initializer, or you can write it explicitly:
`limit i64 := 10`, `var count i64 := 0`.

Here `count` is reassigned, so it must be a `var`; `limit` never changes, so it's
left as the bare, immutable form.
