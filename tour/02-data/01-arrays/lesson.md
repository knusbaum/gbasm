---
title: Arrays and Slices
section: Data
order: 1
---
An **array** has a fixed length baked into its type: `[4]i64` is exactly
four `i64`s, written with a literal like `[10, 20, 30, 40]`. The length
comes first, then the element type — read `[4]i64` as "4 of i64".

A **slice** (`[]i64`, no number) is a view into a run of elements — it knows
its length but doesn't own a fixed size. You make one by slicing an array
with `[lo:hi]`, which views the elements from `lo` up to *but not including*
`hi`. Either bound can be omitted, defaulting to the start or the end:

- `nums[1:3]` — elements 1 and 2
- `nums[2:]` — from index 2 through the end
- `nums[:3]` — from the start up to index 3
- `nums[:]` — the whole array, viewed as a slice

`len(x)` gives the element count of either, and `x[i]` indexes it. Indexing
out of bounds is caught at runtime — a later lesson covers that.

## Slices with their own storage

The slices above are *views* — they borrow an array's memory. A slice can
also carry its own backing buffer. `make([]i64, len, cap)` allocates one:
`len` is how many elements it starts with, and `cap` is how much room it has
before it's full. `cap(s)` reports that capacity.

```
var s := make([]i64, 0, 4)   // empty, room for 4
```

Two operations grow and shrink it:

- **`append(s, v)`** adds `v` to the end and hands back the grown slice, so
  you write `s = append(s, v)`. It fills the spare capacity; appending past
  `cap` traps rather than silently growing.
- **`pop(s)`** removes the last element and returns *two* values — the
  shortened slice and the element that came off: `s, x = pop(s)`.

When you're done with a made slice, **`free(s)`** releases its storage.
(Views don't need freeing — they point into storage that already exists
somewhere else.)
