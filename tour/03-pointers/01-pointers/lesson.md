---
title: Pointers and Address-of
section: Pointers and Safety
order: 1
---
A pointer holds the address of a value. `*i64` is "pointer to `i64`". You take
the address of a binding with `&`, and read through a pointer — dereference
it — with `*`.

Pointers let a function refer to a caller's value without copying it. Here
`deref` receives a `*i64` and reads the `i64` it points at. By default a
pointer is read-only and non-null; the next lessons relax each of those.
