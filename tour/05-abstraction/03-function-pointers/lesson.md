---
title: Function-Pointer Types
section: Abstraction
order: 3
---
Functions are values too. A function-pointer type is written `fn(params)
ret` — `fn(i64, i64) i64` is "a function taking two `i64`s and returning an
`i64`." You get a pointer to a named function with `&`, and you call it
through the variable just like a direct call.

This lets you pass behavior around: `apply` takes any `fn(i64, i64) i64` and
invokes it, so the same `apply` runs `add` or `sub` depending on what you
hand it. It's the lightweight cousin of interfaces — one function instead of
a method set.
