---
title: Transferring Ownership
section: Ownership
order: 3
---


**Ownership** of a value can be transferred, through assignment, returning an `owned` type, or accepting an `owned` type as a parameter in a function.

Only one binding can carry ownership of a value. When ownership is transferred, the old binding is no longer usable.

### Transferring ownership by assignment
```
handle owned i64 := 10
other_handle owned i64 := handle // ownership transferred to `other_handle`.

// `handle` is no longer valid
```

### Transferring ownership by passing to a function
Ownership transferred to a function means that the caller no longer has
access to that value. It's then the caller's responsibility to properly
discharge the **obligation**.
```
// take_handle takes ownership of a handle and does... something with it.
fn take_handle(handle owned i64) { ... }

fn main() {
	handle owned i64 := 10
	take_handle(handle) // Ownership of `handle` was transferred to `take_handle`
	// handle is now invalid.
}
```

### Transferring ownership by return
```
// make_handle gives ownership to the caller.
fn make_handle() owned i64 { ... }

fn main() {
    handle := make_handle() // it's now *our* responsibility to discharge the obligation
	dispose(handle)
}
```
	
