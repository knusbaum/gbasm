---
title: Variables and Constants
section: Basics
order: 3
---
Bindings come in two kinds. `const` introduces a value that never changes
after initialization; `var` introduces one you can reassign. Boson's
convention is to reach for `const` first and use `var` only when you
actually need to mutate.

Both can name their type explicitly (`const limit i64 = 10`) or let the
compiler infer it from the initializer (`var count = 0` is an `i64`).

Here `count` is reassigned, so it must be a `var`; `limit` never changes, so
it is a `const`.
