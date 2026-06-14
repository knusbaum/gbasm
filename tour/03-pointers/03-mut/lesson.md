---
title: Writing Through Pointers with mut
section: Pointers and Safety
order: 3
---
A plain pointer is read-only — you can dereference it to read, but not to
write. To write through a pointer, it must be a `*mut`: a mutable view.

`fn bump(p *mut i64)` can do `*p = *p + 1`, modifying the caller's value in
place. You can only take a `*mut` of something you're allowed to mutate, so
`&x` here works because `x` is a `var`.

The same `mut` marker applies to slices: a `mut byte[]` is a slice whose
elements you can assign to, while a plain `byte[]` is read-only.
