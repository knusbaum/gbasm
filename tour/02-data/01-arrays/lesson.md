---
title: Arrays and Slices
section: Data
order: 1
---
An **array** has a fixed length baked into its type: `i64[4]` is exactly
four `i64`s, written with a literal like `[10, 20, 30, 40]`.

A **slice** (`i64[]`, no number) is a view into a run of elements — it knows
its length but doesn't own a fixed size. You make one by slicing an array
with `[lo:hi]`; `nums[1:3]` views elements 1 and 2.

`len(x)` gives the element count of either, and `x[i]` indexes it. Indexing
out of bounds is caught at runtime — a later lesson covers that.
