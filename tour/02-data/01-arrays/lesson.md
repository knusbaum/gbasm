---
title: Arrays and Slices
section: Data
order: 1
---
An **array** has a fixed length baked into its type: `i64[4]` is exactly
four `i64`s, written with a literal like `[10, 20, 30, 40]`.

A **slice** (`i64[]`, no number) is a view into a run of elements — it knows
its length but doesn't own a fixed size. You make one by slicing an array
with `[lo:hi]`, which views the elements from `lo` up to *but not including*
`hi`. Either bound can be omitted, defaulting to the start or the end:

- `nums[1:3]` — elements 1 and 2
- `nums[2:]` — from index 2 through the end
- `nums[:3]` — from the start up to index 3
- `nums[:]` — the whole array, viewed as a slice

`len(x)` gives the element count of either, and `x[i]` indexes it. Indexing
out of bounds is caught at runtime — a later lesson covers that.
