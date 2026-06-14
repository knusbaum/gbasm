---
title: Borrowed Pointers
section: Ownership
order: 5
---
Moving isn't the only way to pass an owned value. If a parameter has *no*
`owned` in its type, the call is a **borrow**: the function gets temporary
access, and the obligation stays with the caller.

Borrowing strips ownership. `&conn` is a pointer to an owned value, but
`inspect` takes a plain `*i64`, so inside `inspect` it's just a read-only
view — no obligation, nothing to clean up. Back in `main`, `conn` is still
owned, so `main` must still `dispose` it.

This is the common shape: lend a value out to be read or used, keep
responsibility for its lifetime yourself.
