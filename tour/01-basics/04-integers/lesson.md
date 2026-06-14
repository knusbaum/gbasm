---
title: Integers and Casts
section: Basics
order: 4
---
Integers come in signed (`i64`, `i32`, …) and unsigned (`u64`, …) forms.
Literals may be written in decimal or hexadecimal (`0x0F`), and the usual
bitwise operators (`&`, `|`) work as you'd expect.

A cast is written like a function call: `u64(x)` reinterprets a value as
another integer type. The same bits read as a signed `i64` and as an
unsigned `u64` print very differently — watch what `-1` becomes when viewed
through `%u`.

The `%d` verb prints a signed value; `%u` prints an unsigned one.
