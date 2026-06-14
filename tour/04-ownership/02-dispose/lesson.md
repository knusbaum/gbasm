---
title: dispose
section: Ownership
order: 2
---
Sometimes you hold an owned value and there's no consuming function to hand
it to — you just need to declare the obligation satisfied. That's `dispose`.

`dispose(x)` consumes an owned binding and ends its obligation. It has no
runtime effect of its own; it's the compile-time statement "I'm done with
this, and I've done whatever cleanup it needed." Consuming functions use it
internally after their real work (closing a socket, freeing memory); you can
also call it directly to discharge an obligation you're finished with.

After `dispose(handle)`, `handle` is consumed — using it again won't compile.
