# A real allocator for Boson

## History

- 2026-07-21 — Moved span descriptors out of the spans into chunk metadata.
  With an in-span header, `2 × 32768` exactly equals `SPAN_SIZE`, so the largest
  class fit one object where it should fit two — half a span wasted, and the
  tiling broken for every other class too. Out-of-span descriptors make every
  class divide the span exactly, and let large allocations return a
  `SPAN_SIZE`-aligned pointer with no in-band header.
- 2026-07-21 — Proposed. Design settled: span-metadata (headerless small
  objects), three tiers, 4MB chunks / 64KB spans, power-of-two classes to 32KB,
  three-level release policy. Depends on the raw-memory primitives and the
  syscall floor, both of which have landed.

---

## Why

`runtime/_heap/heap_linux.bs` is one `mmap` per allocation and one `munmap` per
free. Every `alloc(T)` and `new(expr)` in every Boson program is two syscalls
and a fresh page-table entry. It was always a placeholder.

The second reason is the one that drove the work leading here: the allocator is
the hardest thing to write in Boson, so writing it is how the language finds
out what it's missing — before a large stdlib is built on assumptions that turn
out to be wrong. That has already paid: the raw-memory primitives
([`PROPOSAL_raw_memory.md`](PROPOSAL_raw_memory.md)), integer stack arguments,
the generic syscall floor, and two unrelated codegen bugs all came out of
clearing the path to this document.

## What this depends on

All landed:

- `uintptr`, `unsafe_cast`, `size_of`, `align_of` — the raw-memory primitives.
- `sys.map_anon` / `sys.munmap` / `sys.madvise` — the typed syscall package.
- Integer stack arguments, which `syscall6` (and therefore `mmap`) needs.

## Assumptions

Stated rather than implied, because each one is a thing that would have to
change:

- **Single-threaded.** Boson has no threads. There is no locking anywhere in
  this design. Adding threads means per-thread caches or a lock around the
  chunk allocator and the per-class lists — it is not a small retrofit, and
  pretending otherwise now would be dishonest.
- **No `realloc`.** The language has no growing allocation. If one is added,
  the size-class structure makes in-place growth possible only within a class.
- **Sizes at `free` are not required.** The compiler *does* know the static
  size at every `free()` call (`compileFreeBuiltin` has the pointee type), and
  passing it would allow an even simpler allocator. This design deliberately
  does not depend on that, so a future dynamically-sized allocation API does
  not invalidate the allocator's ABI. `_heap.free(p *mut byte)` keeps its
  current signature, so **no compiler change is needed**.

---

## Structure

Three tiers. The important property is that **only the chunk allocator calls
the kernel** — everything else is bookkeeping, so no ordinary allocation
pattern can produce a syscall storm.

| Tier | Size | Served from |
|------|------|-------------|
| Small | ≤ 32KB | A power-of-two size class; many objects per span |
| Large | 32KB … chunk capacity | A run of N contiguous 64KB spans in a chunk |
| Huge | > chunk capacity | Direct `mmap`, with a header |

### Constants

```
CHUNK_SIZE  = 4 MiB      // 64 spans; the unit of mmap
SPAN_SIZE   = 64 KiB     // the unit of sub-allocation
CLASS_MIN   = 16 bytes   // also the alignment guarantee
CLASS_MAX   = 32 KiB     // 12 classes: 16,32,64,...,32768
MAX_CHUNKS  = 1024       // 4 GiB heap ceiling in v1 (see Registry)
```

`CHUNK_SIZE` is a starting guess, not a derived value. Too small and large
allocations spill to the huge path early; too large and a small-live-set program
reserves more address space than it needs (cheap, but not free). It is one
constant and is meant to be tuned once there is something to measure.

`CLASS_MAX` at 32KB is chosen against `SPAN_SIZE`: every class from 16 to 32768
divides 65536 exactly, so **each class tiles its span with zero span waste**.
That property depends on the span header living outside the span (see Layout) —
with an in-span header, 32768 would fit once rather than twice and waste half
the span, and every other class would lose its tiling too. An 8KB ceiling would
be worse in the other direction: a 9KB request would round up to a whole span
and waste 86%.

### Alignment

Every returned pointer is **16-byte aligned**. This costs nothing here because
small objects carry no header: class sizes are multiples of 16 and a span's
objects start at `span_base`, which is `SPAN_SIZE`-aligned, so alignment falls
out.

Note this over-satisfies BABI-0, whose ceiling is currently 8
(see [`TODO.md`](../../TODO.md)). Raising the language's ceiling to 16 for
SIMD is a separate change; the allocator does not need to wait for it and will
not need revisiting when it lands.

---

## Layout

### Chunks

A chunk is `CHUNK_SIZE` bytes, **aligned to `CHUNK_SIZE`**. Alignment is load
bearing: it makes both the chunk base and the span base derivable from any
interior pointer by masking, with no lookup.

```
chunk_base = p & ~(CHUNK_SIZE - 1)
span_base  = p & ~(SPAN_SIZE  - 1)
span_index = (p & (CHUNK_SIZE - 1)) / SPAN_SIZE
```

`mmap` only guarantees page alignment, so a chunk is obtained by mapping
`2 * CHUNK_SIZE`, rounding the base up to a `CHUNK_SIZE` boundary, and
unmapping the prefix and suffix. The transient over-reservation is address
space, not memory.

**Span 0 of each chunk is metadata**, not allocatable. It holds the chunk
header and the per-span descriptors for all 64 spans:

```
chunk_hdr {
    base       uintptr        // == the chunk's own base; a self-check
    free_spans i64            // count of unallocated spans
    purged     i64            // count of free spans already MADV_DONTNEED'd
    spans      [64]span_desc
}

span_desc {
    state     i64      // FREE / SMALL / LARGE_HEAD / LARGE_TAIL
    class     i64      // size-class index, when state == SMALL
    free_head uintptr  // intrusive free list; 0 if empty
    bump      uintptr  // next never-handed-out byte
    used      i64      // live objects
    clean     i64      // 1 if the span's pages are known zero
    runlen    i64      // for LARGE_HEAD: spans covered by the run
    next      uintptr  // link in the class's partial-span list
    prev      uintptr
}
```

**Span descriptors live here, not in the spans themselves.** That is what makes
the class sizes tile: a span is `SPAN_SIZE` bytes of object space with nothing
else in it, and every power-of-two class from 16 to 32768 divides 65536
exactly. An in-span header would cost up to half a span in the largest class
(2 × 32768 is exactly `SPAN_SIZE`, so one object would fit where two should)
and would break the tiling of every other class as well.

It costs nothing: span 0 is 64KB and the descriptors are ~72 bytes × 64 ≈ 4.5KB,
in space already reserved. One span in 64 is 1.6% overhead, in exchange for not
needing a separate allocation for chunk metadata — which would be a bootstrap
problem, since the allocator cannot allocate.

### Spans

A small-object span is `SPAN_SIZE` bytes of objects and nothing else. The first
object is at `span_base` exactly, so:

```
object_index = (p - span_base) / class_size
```

which is worth having for a debug-mode validity check — a pointer that does not
land on an object boundary was not one of ours.

The free list is intrusive: a freed block stores the next pointer in its own
first 8 bytes, which is why `CLASS_MIN` must be at least 8. It is 16 for
alignment anyway.

---

## Operations

### `alloc(size)`

1. **Huge** (`size > CHUNK_CAPACITY`): `mmap` `size + 16`, page-rounded. Write
   a 16-byte header (`{magic, total_size}`) and return `base + 16`, which is
   16-aligned because `mmap` returns page-aligned.
2. **Large** (`size > CLASS_MAX`): round to spans, find a free run of that
   length in some chunk, mark `LARGE_HEAD` + `LARGE_TAIL`, record `runlen`, and
   return `span_base` itself. No in-band header is needed — the descriptor
   already says it is large — so a large allocation is `SPAN_SIZE`-aligned.

   A request in (32KB, 64KB] still consumes a whole span. That is inherent to
   span-granular large allocation rather than to any header, and narrowing it
   would mean sub-span large allocation: real complexity for one octave.
3. **Small**: pick the class, then in order —
   - the class's current span's free list, if non-empty (**zero the block**);
   - the current span's bump region, if not exhausted (**zero only if
     `clean == 0`**);
   - the next partial span for that class;
   - a fresh span from a chunk;
   - a fresh chunk.

### `free(p)`

1. Fast reject: if `p` is outside `[min_chunk_base, max_chunk_base + CHUNK_SIZE)`
   it cannot be in a chunk — go to the huge path.
2. Look up `p & ~(CHUNK_SIZE-1)` in the chunk registry. A miss means huge:
   read the header at `p - 16`, check the magic, `munmap`.
3. A hit means in-chunk. Read `span_state[span_index]`:
   - `LARGE_HEAD` → return the whole run to `FREE`.
   - `SMALL` → push `p` onto the span's free list, decrement `used`. If `used`
     hits 0, unlink the span from its class list and return it to the chunk.

   Note both cases read the descriptor from chunk metadata, so `free` never
   touches the freed span's own pages except to write the free-list link. A
   purged span stays purged until it is reused.

### The chunk registry

`free` must distinguish in-chunk pointers from huge ones, and cannot simply
mask and dereference — for a huge pointer that address may be unmapped.

v1 uses a **fixed-size sorted array of chunk bases** (`MAX_CHUNKS` entries,
8KB of file-scope storage) with binary search, preceded by the range fast
reject above. Fixed size specifically to avoid the bootstrap problem: the
registry cannot itself be heap-allocated. The consequence is a 4 GiB ceiling on
in-chunk heap, which should be recorded in the error path rather than silently
failing; huge allocations are unaffected.

Replacing this with a growable or hashed structure is a contained change and is
the obvious first thing to revisit if the ceiling or the lookup cost matters.

---

## Zeroing

`alloc(T)` is specified to return zeroed memory — `compile.go` gates it on
`ZeroInitializable`, and today the guarantee comes free from `MAP_ANONYMOUS`. A
recycling allocator breaks that unless it re-zeroes, and this is the single most
likely way to introduce a subtle, wide-blast-radius bug in the rewrite.

The rule that makes it cheap without making it conditional-and-wrong:

- **Free-list blocks are always zeroed on hand-out.** They held user data.
- **Bump-region blocks are zeroed only if the span is not `clean`.** A `clean`
  span's pages came straight from `mmap` or from `MADV_DONTNEED`, and have never
  been handed out, so they are already zero.

`clean` is a property of where the span's *pages* came from, and freed blocks
never return to the bump region, so it is never invalidated by a free.

A pleasant interaction with the release policy: `MADV_DONTNEED` pages read back
as zero, so purging a span for RSS also re-marks it `clean`. Releasing memory
and the zeroing contract help each other rather than competing.

---

## Release policy

Three levels, cheapest first. The point of the tiering is that the common
alloc/free loop never reaches level 2 or 3.

1. **Span becomes empty** → mark `FREE` in its chunk. No syscall. A workload
   oscillating across a span boundary lives entirely here.
2. **A chunk accumulates free spans** past a watermark → `MADV_DONTNEED` the
   unpurged ones and set `span_clean`. Returns RSS while keeping the address
   range, so reacquiring costs a page fault, not an `mmap`. Gated on the
   watermark precisely so a churning workload never triggers it.
3. **A chunk is entirely free** → `munmap`, but only while more than
   `CHUNK_KEEP` (start at 1) fully-free chunks exist. Keeping one cached is
   what stops a program oscillating across a chunk boundary from paying
   `mmap`/`munmap` per cycle.

Huge allocations `munmap` immediately, with no cache. If a workload turns out to
churn huge blocks the same hysteresis argument applies to them, but that is an
optimization to make when there is evidence, not now.

---

## Bootstrap constraints

`_heap` cannot use the parts of the language that allocate:

- No `alloc` / `new` — it *is* those.
- No runtime interface assertion, and therefore no `fmt`'s `%v` path
  (`_iface.assert_to` allocates itabs). The lower `fmt` layers that write into
  caller buffers are fine, which matters for debugging.

All allocator state is file-scope globals and fixed-size arrays. Note the
dependency direction is already correct: `_iface` imports `_heap`, not the
reverse.

---

## Testing

The existing suite is a strong oracle — every Boson program allocates, so ~800
tests exercise the allocator incidentally, and the current one being trivially
correct means any regression is a real one.

Targeted tests on top of that:

- **Alignment** — every returned pointer 16-aligned, across all classes and
  both tiers.
- **Zeroing** — a block is zero on hand-out after having been written and
  freed. This is the contract most likely to break, so it should be tested per
  path (free-list reuse, dirty-bump, clean-bump), not just once.
- **Class boundaries** — sizes at, one below, and one above every class edge,
  plus the small/large and large/huge boundaries.
- **Reuse** — freeing and re-allocating returns usable memory; a span drains
  and refills; a chunk fills and grows.
- **No unbounded growth** — an alloc/free loop at several sizes leaves the
  chunk count stable. This is the test that proves the hysteresis works, and it
  needs the allocator to expose a chunk/span counter for inspection.
- **Huge path** — allocate past chunk capacity, use the memory, free it.

---

## Deferred

- A cache for huge allocations.
- A growable or hashed chunk registry (and with it, the 4 GiB ceiling).
- Passing the static size to `free`, which the compiler already knows.
- Per-thread caches, when threads exist.
- `realloc`/growth, when the language has a dynamically-sized allocation.
- Retiring `_io_sys` in favor of `sys`.
