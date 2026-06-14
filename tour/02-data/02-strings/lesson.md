---
title: Strings as byte[]
section: Data
order: 2
---
Boson has no separate string type — a string literal *is* a `byte[]`, a
slice of bytes. So everything you know about slices applies: `len` gives the
byte count, and indexing yields a single `byte`.

Printing one with the `%s` verb has a wrinkle: `%s` takes a *pointer* to the
slice, so you pass `&greeting` (or `&"literal"`) rather than the slice
itself. The `%c` verb prints a single byte as a character — here, the first
byte of the string.
