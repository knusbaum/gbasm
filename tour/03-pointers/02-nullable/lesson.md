---
title: Nullable Pointers
section: Pointers and Safety
order: 2
---
A plain `*i64` can never be `nil` — the type system guarantees it. When a
pointer *might* be absent, you mark it nullable with `?`: `*?i64`.

Before you can dereference a nullable pointer, you must prove it isn't nil.
A nil check narrows it: inside `if (p != nil) { … }` the compiler knows `p`
is non-null, so `*p` is allowed. Alternatively, postfix `?` — as in `*p?` —
inserts a runtime nil check and dereferences, trapping if it really was nil.

Dereferencing a `*?i64` without one of these is a compile error — try
deleting the `if` guard around `*p` and running it.
