---
title: Constructors and Destructors
section: Ownership
order: 4
---

The **Ownership** system itself doesn't tell you *how* to build and clean up resources. It only provides a primitive for creating, tracking, and destroying **obligations**.

Actual resource creation and destruction should usually be handled by dedicated constructor functions that return an `owned` type, and dedicated destructor functions that clean up and `dispose()` of the resource.

Initializing an `owned` value or calling `dispose()` doesn't actually create or destroy a resource. That's still the job of the programmer. These things just inform the compiler when resources have been successfully created and destroyed so it can track resource leaks.

This allows you to decide how your resources need to be created and destroyed, and lets you write multiple constructors and destructors for the different states your resources may end up in.

The **ownership** system, for instance, allows you to ensure that a database transaction is either committed or aborted by providing a user with 2 functions that accept ownership of a `transaction` object.
