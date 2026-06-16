---
title: Borrowing Owned Values
section: Ownership
order: 2
---

**Owned** values can be used everywhere a non-owned value could be used. Boson refers to this as "borrowing" a resource. An unowned "borrow" of an owned value carries no **obligation** — only the `owned` binding carries the **obligation**.

You can borrow an owned value by passing it to a function that takes a non-owned parameter, or by binding it to a non-owned variable:

```
	handle owned i64 := 10
	describe(handle)            // borrow by passing to a non-owned parameter
	handle_borrow i64 := handle // borrow by binding to a non-owned variable
```

Because a borrow carries no **obligation**, the original `owned` binding is still responsible for disposing the resource. The borrows simply become invalid once it does.

Play with the code, and try:
* Using `handle` or `handle_borrow` after `dispose(handle)` is called.
* Removing the `dispose(handle)` call.
