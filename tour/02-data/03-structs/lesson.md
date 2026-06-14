---
title: Structs
section: Data
order: 3
---
A `struct` groups named fields into one value. You declare the shape with a
`type` declaration, then build instances with a struct literal that names
each field: `point{x: 3, y: 4}`.

Fields are read and written with `.`, so `p.x = p.x + 10` updates one field
in place. The whole value lives inline — assigning a struct copies its
fields.
