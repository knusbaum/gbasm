---
title: Globals and Static Initialization
section: Data
order: 5
---
Bindings declared at file scope — outside any function — are globals, shared
across the whole package. They can carry an initializer that is computed at
build time, including array literals: `var counts i64[3] = [5, 10, 15]` is
laid out statically, no runtime setup.

Here a `const base` and a `var counts` array are read directly inside
`main` — `base` seeds the running total, and the array is summed into it.
Reach for globals sparingly; most state is better kept local and passed
explicitly.
