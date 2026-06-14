---
title: Interfaces and Dispatch
section: Abstraction
order: 4
---
An `interface` is a set of method signatures. Any type that provides those
methods satisfies it — no `implements` keyword, the match is structural. The
method signatures use `*self` to stand for the eventual concrete receiver.

A function taking an interface (`describe(s shape)`) can be called with any
type that satisfies it, and `s.area()` dispatches to that type's method.
Boson builds the dispatch tables at compile time; passing `&sq` and `&r` to
the same `describe` runs `square`'s `area` and then `rect`'s. One function,
many concrete types.
