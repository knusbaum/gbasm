---
title: Integers and Casts
section: Basics
order: 3
---
Boson's built-in types are a small, fixed set. The integers come in signed
and unsigned forms at four widths — the number is the bit width:

- signed: `i8`, `i16`, `i32`, `i64`
- unsigned: `u8`, `u16`, `u32`, `u64`

`i64` is the everyday integer, and a plain integer literal (such as `100`) 
defaults to `i64`. In a context that expects a different type — a `u64`,
a `byte`, an `i32` — the literal takes that type instead, as long as its
value fits. Two more built-ins complete the set:

- `byte` — a one-byte unsigned integer. Character literals like `'A'` are
  just their byte value.
- `bool` — a truth value. Boson has **no `true`/`false` literals**; a
  `bool` is produced by a comparison (`<`, `==`, `!=`, and so on).

That is the whole set — there are no floating-point types yet.

Literals can be written in decimal, hex (`0x0F`), octal (`0o17`), or binary
(`0b1111`), and the two bitwise operators `&` and `|` work as you'd expect.

A **cast** is written like a call: `u64(x)` reinterprets a value as another
type. Between integer types it reinterprets the bits — which is why the
same `-1`, read as an unsigned `u64`, prints as a huge number.

Each print verb names a *rendering*, and the value's own type does the rest
— so `%d` prints any integer in decimal (an `i64` signed, a `u64`
unsigned), `%x` prints one in hex, `%c` prints it as a character, and `%v`
prints whatever is natural for the type.
