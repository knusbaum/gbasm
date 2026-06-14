---
title: Methods on Named Types
section: Abstraction
order: 2
---
A named type can carry methods. You attach them in a block after the type's
definition, each taking a `self` receiver — usually `*self` (here written as
the concrete `*celsius`) so the method can read the value it's called on.

Call a method with the familiar `value.method()` syntax: `c.to_f()` runs
`to_f` with `self` pointing at `c`. Inside, `*self` dereferences to the
`celsius` value, and `i64(*self)` reads it as a plain integer for the
arithmetic. Methods are how behavior travels with a type.
