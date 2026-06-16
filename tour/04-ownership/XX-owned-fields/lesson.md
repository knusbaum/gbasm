---
title: Owned Fields
section: Ownership
order: 3
---
An obligation can live inside a struct. If any field is `owned`, the whole
struct becomes owned — holding it means you're responsible for the field's
obligation too.

Consuming the struct discharges its fields: `dispose(s)` here satisfies the
`owned` `id` along with the rest of `s`. Non-owned fields like `count`
behave normally — read and assign them freely.

A *value-typed* owned field like `id` can't be pulled out on its own
(`var x = s.id` is rejected) — there's no separate handle to give it, so the
obligation travels with the whole aggregate and is discharged by `dispose(s)`.
A field that owns a *resource* through a pointer is different: it can be moved
out and cleaned up individually. That's the next lesson.
