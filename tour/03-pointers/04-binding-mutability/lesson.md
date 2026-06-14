---
title: Binding Mutability
section: Pointers and Safety
order: 4
---
There are two different "mutabilities" in Boson, and it helps to keep them
apart:

- **Binding** mutability — can you reassign the *name*? `var` yes, `const`
  no.
- **Target** mutability — can you write through a *pointer or slice*? That's
  what `mut` controls (previous lesson).

This lesson is about the first. `limit` is declared `const`, so assigning to
it is rejected at compile time. This program is **expected to fail** — read
the error, then change `const` to `var` and it compiles.

The same rule covers **function parameters**: they're `const` by default, so
reassigning one is the same error. When a function needs to treat an argument
as a mutable local, it declares the parameter `var` — `fn f(var x i64)`.
