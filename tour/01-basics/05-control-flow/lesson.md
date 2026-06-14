---
title: Control Flow
section: Basics
order: 5
---
`if` runs a block when its condition is true; the condition is written in
parentheses. `for` is the loop, in the familiar three-part form
`for (init; condition; step)`.

Inside a loop, `continue` skips to the next iteration and `break` leaves the
loop entirely. The loop below prints `i` each pass, but skips the value `2`
with `continue` and stops at `4` with `break` — so it prints 0, 1, and 3.

Change the conditions and predict the output before you run it.
