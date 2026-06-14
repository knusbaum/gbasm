---
title: Common Ownership Errors
section: Ownership
order: 6
---
You've seen the pieces; this is the failure they prevent. Passing an owned
value to a function whose parameter is `owned` **moves** it — the original
binding is consumed and may no longer be used, even through a pointer that
still aliases its storage.

Here `take(y)` consumes `y`. The pointer `yptr` still refers to `y`'s
storage, so dereferencing it afterward is a use-after-move. Boson rejects
this at **compile time** — this lesson is expected to fail.

Read the error, then fix it: print `*yptr` *before* the call to `take`,
and the program compiles.
