---
title: Destroying Owned Aggregates
section: Ownership
order: 4
---
When a field owns a real resource — heap memory, a file handle — destroying
the aggregate means cleaning up that field, not just dropping the struct. You
do it by **moving the field out**, cleaning it up, then disposing the
now-empty aggregate.

Moving an owned pointer field out works exactly like moving a local owned
pointer: `var handle = c.handle` hands the obligation to `handle` and empties
`c.handle`. Then `free(handle)` releases it, and `dispose(c)` discharges the
aggregate, whose owned field is now accounted for.

While a field is moved out, the aggregate is *partially consumed*: you can
keep consuming its other fields, re-initialize the field, or `dispose`/`free`
it — but you can't pass it to a function, return it, or alias it until it's
whole again. That keeps a half-destructed value from leaking out where its
emptied field looks valid. Try passing `c` to a function between the move and
the `dispose`.
