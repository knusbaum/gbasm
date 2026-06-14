---
title: Nullable Interfaces
section: Abstraction
order: 5
---
You can ask whether a value satisfies an interface with a type assertion:
`a.(named)`. Because it might *not*, the result is a **nullable interface**,
`?named` — the same `?` you saw on pointers. A plain interface is always
valid; a `?named` may be null when the assertion fails.

Like a nullable pointer, you must narrow before using it: inside
`if (n != nil) { … }` the compiler knows `n` is a real `named`, so
`n.name()` dispatches. This is how Boson does a checked "is it a …?" without
a separate boolean — the maybe-ness lives in the type. Dispatching on an
un-narrowed `?named` is a compile error.
