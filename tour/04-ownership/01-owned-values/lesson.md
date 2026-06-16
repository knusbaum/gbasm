---
title: Owned Values and Obligations
section: Ownership
order: 1
---

**Ownership** is Boson's tool for tracking resources. Programs contain many
kinds of resources which can be created, and then must later be destroyed.
Boson helps you manage this by letting you mark values as `owned`, and
ensuring that `owned` resources can't go out of scope without being cleaned
up.

Any type can be `owned`, not just specially constructed types. Ownership just
tells Boson that you need to do *something* with that value, so it can help
you track that.

You can create an owned value with the `owned` keyword.
```
x i64 := 10       // regular i64
x owned i64 := 10 // owned i64
```

Declaring a value `owned` creates an **obligation** to dispose of the
resource. Owned values are disposed of by calling `dispose()` on them.

Failing to dispose of an owned value is a compile error.
Try removing the `dispose(x)` line in the example and see what happens.

Owned values cannot be used once they are disposed.
Try adding `fmt.print("x is: %d\n", x)` after the `dispose(x)` line and see
what happens.
