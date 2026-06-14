---
title: Cross-Package Imports
section: Abstraction
order: 7
---
Code is organized into packages. `import "fmt"` brings the `fmt` package
into scope, and you reach its names through the package prefix:
`fmt.print`, `fmt.str`. A program can import several — here `fmt` and `io` —
and each name stays qualified by its package, so there's no ambiguity about
where `print` or `STDOUT` comes from.

`io.STDOUT` is a value exported by the `io` package; `fmt.str` is a function
from `fmt` that writes to it. Larger programs split their own code into
packages the same way and import across them; the tour runs single-file, so
here we lean on the runtime packages to show the mechanism.
