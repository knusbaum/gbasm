---
title: Welcome to Boson
section: Basics
order: 0
---
Boson is a simple, general-purpose programming language. Its aim is to fill
the same niche as languages like C and Zig, while departing from their designs
in significant ways.

In terms of complexity, Boson sits somewhere between C and Zig. C provides very
simple primitives with very few safety mechanisms. Zig is substantially safer,
but also adds quite a lot of features and new machinery, making the language
complex.

Boson, by contrast, seeks to codify many of the "safe" C patterns which have
evolved over more than 50 years into the language itself, while also removing
the footguns that cause programmers to avoid it.

For example:

| | C | Boson | Zig |
|----|---|-------|----|
| Resource Management | Manual - read the documentation | Ownership in the type system | `defer` / runtime tracking |
| Polymorphism | `void` pointers and vtables | Interfaces | Comptime metaprogramming and vtables |
| Dangling Pointers | Good luck! (memory corruption) | Borrow tracking | Runtime detection |
| Errors | In-band signalling or "out" parameters | Multi-value return | Error sets |
| Null | Good luck! (SIGSEGV) | Explicit nullability with enforced checks | Explicit nullability with enforced checks |
| Bounds Violations | Good luck! (memory corruption) | Arrays & slices, bounds-checked | Arrays & slices, bounds-checked |

The rest of this tour walks through these and other features one at a time, with a
runnable program on each page that you can edit and experiment with. Press **Next** to
begin.
