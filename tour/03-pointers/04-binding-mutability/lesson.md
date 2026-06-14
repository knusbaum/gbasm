---
title: Binding Mutability
section: Pointers and Safety
order: 4
---
There are two different "mutabilities" in Boson, and it helps to keep them
apart:

- **Binding** mutability — can you reassign the *name*? A bare `:=` binding
  is immutable; mark it `var` to allow reassignment.
- **Target** mutability — can you write through a *pointer or slice*? That's
  what `mut` controls (previous lesson).

This lesson is about the first. `limit` is declared with a bare `:=`, so it's
immutable and assigning to it is rejected at compile time. This program is
**expected to fail** — read the error, then make it `var limit i64 := 5` and
it compiles.

The same rule covers **function parameters**: they're immutable by default,
so reassigning one is the same error. When a function needs to treat an
argument as a mutable local, it declares the parameter `var` — `fn f(var x
i64)`.
