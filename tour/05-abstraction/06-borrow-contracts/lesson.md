---
title: Interface Borrow Contracts
section: Abstraction
order: 6
---
Methods that return pointers or slices raise a question: does the returned
value borrow the receiver, or is it independent? By default an interface
method may **not** return a borrow of its receiver — otherwise a caller
holding only the interface couldn't reason about how long the result lives.

A `from(self)` clause on a method signature changes that contract: it
declares "the value I return may borrow `self`." Here `viewer.view` is
allowed to hand back a slice into the receiver's own storage, so `doc.view`
can `return d.body` with zero copying. Remove the `from(self)` and the impl
is rejected — `doc.view returns a borrow of its receiver, but viewer
declares no such borrow`. The contract makes that aliasing explicit and
checkable.
