---
title: Bounds Checks and Traps
section: Pointers and Safety
order: 5
---
Every array and slice access is bounds-checked — an out-of-range index can
never read or write past the end. Boson enforces this two ways:

- When the index is a **compile-time constant**, the compiler rejects an
  out-of-range access outright (`nums[5]` on a length-3 array won't compile).
- When the index is **dynamic**, the compiler inserts a runtime check; an
  out-of-range access traps and the program is killed, rather than corrupting
  memory.

The loop to the right stays in range and runs cleanly. Try it, then experiment:
change `nums[i]` to `nums[5]` for a compile error, or loop to `i <= len(nums)`
for a runtime trap.
