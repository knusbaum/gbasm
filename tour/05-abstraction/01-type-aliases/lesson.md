---
title: Type Aliases
section: Abstraction
order: 1
---
`type name base` introduces a new named type built on an existing one.
`type celsius i64` is *not* interchangeable with `i64` — it's a distinct
type, so the compiler won't let you accidentally mix a `celsius` with a
`fahrenheit` or a raw `i64`.

Converting between a named type and its base is an explicit cast, written
like a function call: `i64(c)` reads a `celsius` as a plain `i64`, and
`fahrenheit(x)` tags an `i64` as a `fahrenheit`. That explicitness is the
point — the names document intent and the casts mark every crossing.
