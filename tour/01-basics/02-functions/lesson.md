---
title: Functions
section: Basics
order: 2
---
A function is declared with `fn`, a name, a parenthesized parameter list,
and an optional return type. Each parameter is written `name type`, and the
body returns with `return`.

`main` is just the function the program starts from; here it takes no
arguments and returns nothing. (It can also receive command-line arguments
and return an exit code.) To the right, `add` takes two `i64` parameters and
returns their sum, which `main` prints with the `%d` verb.

Try adding a second function and calling it from `main`.
