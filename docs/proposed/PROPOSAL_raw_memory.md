# Raw memory primitives: `uintptr`, `size_of`, `align_of`, `unsafe_cast`

## History

- 2026-07-21 — Added the `_iface` migration as the staged first exercise, ahead
  of `_heap`; recorded the `rdx` side-channel cleanup it depends on.
- 2026-07-21 — Proposed. Surface settled at four primitives; alignment and the
  no-UB taxonomy settled; atomics/checked-mode deferred with notes.

---

## Motivation

Boson cannot express a memory allocator. That is not a hypothetical gap — the
toolchain has already paid for it once.

`runtime/_iface/iface_linux.bs` is 314 lines of hand-written assembly for a
reason that is **not** "it makes syscalls." It walks typedesc tables, computes
`(decl_idx+2)*8+24` slot offsets, heap-allocates itabs and links them into a
cache (`iface_linux.bs:118-124`, `:129-132`). Every one of those operations is
raw pointer arithmetic over untyped memory. The runtime's dynamic-type
machinery lives in assembly because the language cannot say what it does.

`runtime/_heap/heap_linux.bs` is the second instance. Its 42 lines are mostly
syscall, but the load-bearing parts are `add r12 8` / `sub rdi 8` /
`mov [rax] r12` (`heap_linux.bs:11`, `:21-22`, `:33`) — writing a size header
below a returned pointer. Inexpressible today.

The goal is the Go runtime's property: the allocator should be written in
*legal Boson that no ordinary Boson program would ever write*. Not a separate
dialect, not a softer variant — the same language, using operations that are
official, documented, and tested, but which almost nothing else touches.

The strategic reason to do the allocator first, rather than a safe collection:
it is the component most likely to find holes in the language, and finding them
*before* a large stdlib is written in Boson is much cheaper than finding them
after. A safe collection would not serve as a precursor in any case — anything
built on `alloc()` is unusable *inside* the allocator, since it would recurse.

### What is already sufficient

The ownership system is **not** the obstacle, which was the surprise of the
survey. It already has every hatch an allocator needs:

- `owned(expr)` mints an owned pointer from nothing (`DESIGN.md:932`), already
  used exactly this way at `examples/linked/src/main.bos:19`.
- `dispose(x)` discharges an obligation *without* freeing — precisely "take
  ownership, put it on a free list."
- A mutable global free-list head works today. The assignment escape gate
  (`compile.go:1499` → `1039-1062`) fires only on *borrowed* bindings;
  `OriginAllocated` pointers are not checked.
- `new`/`alloc`/`free` lower to a `call` at three `Fprintf` sites
  (`compile.go:2613`, `:2702`, `:2748`), so repointing them is cosmetic.
- Syscall access follows the existing `runtime/_io_sys` pattern: a `.bs`
  package whose `type fn(...)` line is the sole source of truth for the
  signature, imported normally from `.bos`.

The entire gap is raw-memory expressiveness.

---

## The primitives

Four additions. Three of them are completely safe; all unsafety is
concentrated in the fourth.

### 1. `size_of(T) i64` — safe

Compile-time constant: the size in bytes of type `T`, as `ASTType.Size(c)`
already computes it. Usable anywhere a constant is, including a file-scope
`const`. Parses like `alloc(T)`, which already accepts a type in expression
position.

### 2. `align_of(T) i64` — safe

Compile-time constant: the alignment of `T`, as `ASTType.Alignment(c)` already
computes it (added with natural alignment in `9acd070`).

Included rather than derived because BABI-0's alignment ceiling is currently
8 but is intended to rise to 16 (`TODO.md:83-99`). An allocator that hardcodes
8 is silently wrong the day that changes; one that asks `align_of` is not.

`align_of` is **advisory**, not a precondition — see Alignment below. It exists
so code can *choose* correct alignment, not to satisfy a language rule.

### 3. `uintptr` — safe

An unsigned, pointer-width integer type. Any pointer converts to it with the
ordinary cast syntax:

```boson
a := uintptr(p)
```

Normal integer arithmetic applies, with the two's-complement wrap semantics
already defined for Boson integers (`56f4ad6`).

**This is safe on its own, and that is the design's load-bearing property:** a
`uintptr` cannot be dereferenced. Converting a pointer to an integer and doing
arithmetic on it can produce a nonsensical value, but not an unsound operation.
Nothing can go wrong until the value becomes a pointer again — which is exactly
one place.

Boson has no GC, so `uintptr` carries none of the object-liveness hazard that
makes Go's `uintptr` treacherous. It really is just an integer.

`uintptr → pointer` is **not** available through the ordinary cast. Only
`unsafe_cast` does that.

### 4. `unsafe_cast(T, x) T` — the unsafe one

Reinterprets `x` — a `uintptr` or any pointer — as type `T`.

`T` is the **exact result type**, not a pointee: `unsafe_cast(*mut node, a)`,
`unsafe_cast(?*owned mut node, a)`. Taking the full type means the mut,
nullable, and owned bits are stated at the cast rather than fixed by convention
and then patched with a wrapping `owned(...)`, which would split one assertion
across two constructs.

**The cast itself has no precondition.** It is always legal and always
succeeds; it produces a value. Alignment, validity, and provenance are
properties of what you subsequently *do* with the result, not of its existence
— holding a temporarily-misaligned or not-yet-valid pointer is legitimate and
must not be prohibited.

The result carries **no Origin**: it sits outside the flow/alias machinery
entirely. That is the precise, checkable sense in which the operation is
unsafe, and it is what makes "all unsafety is concentrated here" a statement
about the implementation rather than a slogan.

---

## Behavior model

### There is no undefined behavior in Boson

Every operation has defined behavior. An operation can be exactly one of:

1. **Rejected at compile time** — best, when the condition is decidable.
   `nums[3]` on a 3-element array already works this way.
2. **A defined runtime trap** — the `index_oob` / `nil_assert` family already
   in `runtime/_init`. Deterministic and identical on every target.
3. **A defined outcome, possibly a bad one** — a misaligned ordinary load
   performs the access and succeeds.
4. **Target-defined** — the operation is emitted; the hardware determines the
   result, and Boson documents that it does not constrain it.

**"Forbidden" is not on this list.** A rule the compiler cannot enforce is not
a rule; stated as a prohibition, it is undefined behavior with a disclaimer.
Any future feature must land in one of the four categories above.

Category 4 is the concession that keeps this affordable, and it is narrower
than C's UB in the way that matters. C's UB is dangerous less because a bad
thing happens than because **the optimizer assumes it cannot happen** and
miscompiles the surrounding code. Boson makes the weaker promise about the
outcome and keeps the stronger one about the compiler: *the compiler never
reasons backwards from an operation's behavior.* A single-pass compiler that
lowers each AST node directly to `.bs` gets this for free.

### Alignment

Alignment is a property of the **operation**, not of the pointer.

- **Ordinary loads and stores: misalignment is allowed and defined**
  (category 3). The access is performed with the ordinary instruction. On
  x86-64 an unaligned `mov` succeeds, possibly slower across a cache line. On
  AArch64 (`docs/proposed/PROPOSAL_arm64_backend.md`) ordinary loads and stores
  to Normal memory likewise tolerate misalignment. So the permissive answer is
  portable across both backends Boson plans, which is why it is cheap to grant.
- The compiler assumes nothing about alignment: it does not widen, reorder, or
  elide an access on the basis that it must have been aligned.

### Atomics and aligned SIMD — deferred, with a note

Neither exists in Boson today, so this proposal does not specify them. The
decision is recorded because it should not be re-litigated from scratch later.

The hardware genuinely diverges: AArch64 `LDXR`/`STXR` alignment-fault when
misaligned; x86-64 `lock cmpxchg` across a cache line still executes atomically
but may raise SIGBUS under kernel split-lock detection. The 16-byte-aligned SSE
forms (`movaps`, `movdqa`) fault outright, and BABI-0's ceiling is 8
(`TODO.md:83-99`).

Options, when the time comes:

- **Target-defined (category 4)** — emit the instruction, document that the
  hardware decides. Zero cost. The compiler never emits a misaligned atomic on
  its own; it can only happen downstream of an `unsafe_cast`, which is an
  explicit assertion by the programmer.
- **Checked trap (category 2)** — a test-and-branch per atomic, joining
  `index_oob` and `nil_assert`, buying a uniform guarantee across targets.

**Leaning target-defined.** A per-atomic branch to guard against misuse of an
explicitly unsafe operation is the wrong trade in the common case. The natural
home for the check is a **debug/release build mode** — which Boson does not have
and which would also be the natural home for `index_oob` and `nil_assert`
elision. That is its own proposal; noting it here so the atomics decision is not
made in ignorance of it.

### `unsafe_cast` to a non-nullable pointer

`unsafe_cast(*mut node, 0)` yields a value typed non-nullable that holds null.
Nil-tracking will not insert a `nil_assert` on it, so a subsequent deref faults
rather than trapping cleanly.

This is category 3 — defined, and bad — and is consistent with the framework.
It is called out explicitly because it is the one hole `unsafe_cast` opens
through an *existing* safety mechanism rather than merely stepping outside one.

---

## Sufficiency: a worked allocator

Idioms in isolation give false confidence. The following is the core of a real
free-list allocator, written against the four primitives, to demonstrate that
nothing further is required. If a primitive were missing it should be visible
here, not in month two of writing the allocator.

Two views of the same storage — the whole reason reinterpretation is needed.
An allocated block carries only a size header; while on the free list, the
user-data region is reused to hold the list link:

```boson
package _heap

import "_mem_sys"

// Present on every block, allocated or free.
type hdr struct {
	size i64
}

// The same storage, viewed while the block sits on the free list. The `next`
// field overlays what would be user data.
type freeblock struct {
	size i64
	next uintptr
}

const HDR i64 = size_of(hdr)
const ALIGN uintptr = uintptr(align_of(i64))

var free_head uintptr    // zero-initialized: 0 means empty

// align_up rounds a up to a multiple of to, which must be a power of two.
fn align_up(a uintptr, to uintptr) uintptr {
	return (a + to - uintptr(1)) & ~(to - uintptr(1))
}

pub fn alloc(size i64) *mut byte {
	total := align_up(uintptr(size) + uintptr(HDR), ALIGN)

	// First fit over the free list. Each iteration reinterprets a raw
	// address as a block view, reads its header, and follows the link that
	// lives inside the block's own storage.
	var prev uintptr := uintptr(0)
	var cur uintptr := free_head
	for (; cur != uintptr(0); ) {
		b := unsafe_cast(*mut freeblock, cur)
		if (uintptr(b.size) >= total) {
			if (prev == uintptr(0)) {
				free_head = b.next
			} else {
				p := unsafe_cast(*mut freeblock, prev)
				p.next = b.next
			}
			zero(cur + uintptr(HDR), b.size - HDR)
			return unsafe_cast(*mut byte, cur + uintptr(HDR))
		}
		prev = cur
		cur = b.next
	}

	// No fit: fresh mapping. mmap returns zeroed pages.
	base := _mem_sys.mmap(i64(total))
	h := unsafe_cast(*mut hdr, uintptr(base))
	h.size = i64(total)
	return unsafe_cast(*mut byte, uintptr(base) + uintptr(HDR))
}

pub fn free(p ?*mut byte) {
	if (!p) { return }
	h := uintptr(p?) - uintptr(HDR)
	b := unsafe_cast(*mut freeblock, h)
	b.next = free_head
	free_head = h
}

// zero writes n zero bytes at a. Required to preserve alloc's zeroing
// contract on recycled blocks -- see Bootstrap constraints.
fn zero(a uintptr, n i64) {
	var i i64 := 0
	for (; i < n; i = i + 1) {
		*unsafe_cast(*mut byte, a + uintptr(i)) = 0
	}
}
```

Every operation the allocator needs appears here: header write and read-back,
cursor arithmetic, alignment rounding, a link threaded through recycled memory,
two type views of one region, and a byte-granular store. **The four primitives
are sufficient.**

### What the sketch surfaced

Three findings that would not have come out of listing idioms separately:

1. **`alloc(T)`'s zeroing contract is load-bearing and easy to break.**
   Today `alloc` returns mmap `MAP_ANONYMOUS` pages, which are zeroed, and
   `compile.go:2604-2606` gates `alloc(T)` on `ZeroInitializable`. A recycling
   allocator returns *dirty* memory. The Boson `_heap` must therefore re-zero
   reused blocks (the `zero` helper above) or the language must restate the
   contract. This is the single most likely way to introduce a subtle,
   wide-blast-radius bug during the port.

2. **Nothing — unary `~` already exists.** An earlier draft of this document
   claimed it was missing. It is not: `tok_tilde` (`lexer.go:54`, `:905`),
   `n_bitnot` (`parser.go:50`), handled in `parseUnary` at `parser.go:1291`,
   desugared to `(0-x)-1`, and documented at `DESIGN.md:149`. So `align_up`
   should be written the obvious way:

   ```boson
   fn align_up(a uintptr, to uintptr) uintptr {
       return (a + to - uintptr(1)) & ~(to - uintptr(1))
   }
   ```

   Retained as a finding because it is the one place the sketch corrected a
   wrong assumption about the language rather than revealing a gap in it.

3. **The pointer→`uintptr` cast needs a carve-out in an existing guard.**
   `compile.go:6299-6302` rejects pointer↔non-pointer casts precisely because
   they launder pointer-ness. `uintptr` is the sanctioned exception, and the
   guard's own comment anticipates this: *"If a meaningful unsafe form is
   needed later, it should be syntactically distinct from the regular `T(expr)`
   cast."* Note that pointer→`uintptr` is the **safe** direction and stays on
   `T(expr)`; only `uintptr`→pointer moves to `unsafe_cast`.

---

## Staging: migrate `_iface` before writing `_heap`

The allocator is the goal, but it should not be the first code written against
these primitives. `runtime/_iface/iface_linux.bs` is the better first exercise,
and it is strictly easier than `_heap` in a way that is not obvious.

**It can still allocate.** `_iface` calls `_heap.alloc` for itabs
(`iface_linux.bs:118-124`). If `_heap` stays in assembly for now, a Boson
`_iface` may use `alloc`/`new` normally. The only bootstrap constraint on it is
that it must not use *runtime interface assertion* — that is, itself — nor
`fmt`'s `%v` path, which is backed by assertion. Static interface coercion and
vtable dispatch are fine. So it exercises every new primitive without the
self-referential allocation problem that makes `_heap` delicate.

**It has a pre-existing test oracle.** This is a behavior-preserving port of
working, well-exercised code — the interface suites in `cmd/bosc/tests/` and
`examples/interface` already pin the semantics. Bugs in the new primitives show
up as failures in tests that pass today, which is a far better signal than
discovering them in freshly-written allocator logic where an unfamiliar bug and
an unfamiliar primitive are indistinguishable.

**It is exactly what the primitives are for.** Nearly every line is "load a
qword at a fixed offset from a base pointer, compare, advance": typedesc head
walking, a 72-byte method-entry stride, a 64-byte iface_desc stride, the
`(decl_idx+2)*8+24` vtable slot computation, and a cache linked-list whose head
is written back through a raw pointer. In Boson these become struct
declarations matching the documented layouts plus `unsafe_cast` to obtain typed
views — which should be substantially more readable than the assembly, since
the layouts currently live only in a header comment (`iface_linux.bs:20-39`)
with no mechanical connection to the code that walks them.

### The one ABI issue, and why it dissolves

`assert_to` returns a **register pair**: `rax` = vtable, `rdx` = ok
(`iface_linux.bs:16-18`). Boson's multi-value return lowers to an indirect
struct return instead (`DESIGN.md:288`, `:2614`), so a Boson `assert_to`
declared `(*u8, bool)` would not match the bare `call` the compiler emits.

It does not need to. **`rdx` carries no information:** failure is `rax=0` *and*
`rdx=0`; success is `rax=vtable` and `rdx=1`, so `rax == 0` iff failure. Both
call sites nearly say so already — `compile.go:8098` `forget`s the ok flag
("the ok flag is subsumed by the nullable type") and `compile.go:8060` branches
on it into a path its own comment calls unreachable given the static gate.
Testing the vtable temp instead is equivalent at both.

Dropping `rdx` makes the function match its own declared signature — the `.bs`
already says `type fn(*u8,u64,i64,*u8) i64` (`iface_linux.bs:50`); `rdx` is an
undeclared side channel — and Boson's ordinary single-value return ABI then
applies with no compiler change.

**Do this cleanup first, as a separate commit against the existing assembly.**
It is a small, independently-testable simplification, and separating it from
the port means a test failure during migration cannot be caused by two things
at once.

### The itab cache: accounting, and what the port must preserve

Worth stating explicitly, because "the allocator is called from interface
assertion" invites the question of whether assertions leak. They do not.

- **Each typedesc carries a `cache_ref`** (`iface_linux.bs:58`) — a relocation
  to a paired *writable* `typedesc_cache` slot, since the typedesc itself lives
  in read-only `o.Data` (`cmd/bosc/typeinfo.go:230`, `DESIGN.md:2195`). Hence
  the double indirection when loading the list head. That slot heads a linked
  list of itabs, walked matching `(dst_desc, shape)` at `iface_linux.bs:60-74`.
- **On a hit**, the cached vtable is returned in O(1). No allocation.
- **On a miss**, one itab is allocated, filled, and linked into the cache
  (`:164-167`: `itab.next = head`, then `head = itab`).
- **On a lookup failure**, the itab is freed (`.miss_free`, `:156-158`) and the
  failure is *not* cached (`DESIGN.md:1968`).

So each distinct `(src_ti, dst_desc, shape)` triple allocates exactly one itab,
ever. Those are never freed, which is correct rather than leaky: itabs are
immortal by design, and the triple set is finite and statically bounded by the
program. This mirrors Go's itab table.

**The real cost is on repeated failure, and it is not a leak.** Because misses
are uncached, a failing assertion in a loop re-derives the receiver shape,
allocates an itab, walks the entire typedesc method table performing hash
compares *and* byte-equal name/sig comparisons, fails, and frees — every
iteration. Under today's `_heap` (one mapping per allocation) that is an `mmap`
**and** a `munmap` syscall pair per failed assertion.

This is a second, independent argument for the allocator work: a free-list
allocator turns those two syscalls into a few pointer writes.

**Negative caching** — recording a failed `(dst_desc, shape)` so the second
attempt is O(1) — is the obvious fix and should be done **later, as its own
change with its own tests**. The port must preserve free-on-miss and
no-cache-on-miss *exactly*, or the existing interface suites stop being a valid
oracle for it. Behavior-preserving port first; optimization second.

### Suggested order

1. Drop the `rdx` side channel from `_iface` and both call sites (pure cleanup,
   current assembly, existing tests).
2. Land `size_of` / `align_of`.
3. Land `uintptr` and its cast carve-out.
4. Land `unsafe_cast`.
5. Port `memeq`, then `lookup_method`, then `assert_to` — innermost first, each
   against the existing interface test suites.
6. Only then write `_heap`.

Negative caching of failed assertions is deliberately *not* in this list — it
is a behavior change, and it belongs after both the port and the allocator.

---

## Bootstrap constraints for a Boson `_heap`

A Boson-implemented allocator cannot use the parts of the language that
allocate. The survey established the exact boundary.

**Available:** globals, stack-resident structs and fixed arrays, slices and
reslicing (header construction, no copy), string literals (static `.data`),
static interface coercion and vtable dispatch, function pointers, loops,
recursion, arithmetic, casts.

**Unavailable — these allocate:**

- **Runtime interface assertion.** `_iface.assert_to` builds an itab on the
  heap (`iface_linux.bs:118-124`), with a rollback `free` at `:157-158`.
- **`fmt.print` / `fmt.printf`**, which are backed by per-argument assertion.
  `fmt.bos:17-19` states the rule: *"The only heap traffic is the lazy itab
  cache built by interface assertion inside %v dispatch."* Debugging the
  allocator therefore cannot use `fmt`'s `%v` path — the lower layers
  (`fmt.str`, `fmt.int`, which write into caller buffers) are fine.
- **`new` / `alloc`** themselves, obviously.

**Build graph.** `boson.mmk:118-128` links `target/_heap.bo` and
`target/_iface.bo` into every executable unconditionally, because the compiler
emits bare calls to both. A Boson `_heap` would be a package every program
depends on, including itself. That cycle has to be broken before the port —
likely by keeping `_heap` compiled with a flag that forbids the allocating
constructs, or by ordering the build so `_heap` is compiled against a
no-allocation subset.

**New package.** `_mem_sys` (`mmap`/`munmap`), cloning the `runtime/_io_sys`
pattern. Three `.bs` stubs; the only genuinely new build artifact.

---

## C backend obligations

`docs/proposed/PROPOSAL_c_transpiler.md` forks from the same typed AST and must
emit **well-formed C** (`:8-11`). This matters more here than anywhere else in
the language, and the reasoning runs opposite to the intuitive direction.

The "no optimizer, so no UB" argument protects the **native** backend only. The
C backend hands its output to gcc/clang at `-O2`, which *does* reason backwards
— and integer↔pointer round-trips with type punning are the textbook place
strict aliasing miscompiles.

The good news is that this proposal's shape is already the mitigation, not by
accident: doing arithmetic on `uintptr` and materializing a pointer only at
`unsafe_cast` means the C backend emits **integer** arithmetic (fully defined in
C) and never forms an out-of-object pointer via pointer arithmetic. A design
that instead allowed `p + 8` directly on `*T` would have been the UB-prone one.

The residual hazard is the reinterpreting deref. The C backend must therefore
either:

- build with `-fno-strict-aliasing` — what glibc, jemalloc, and essentially
  every allocator written in C rely on; or
- route punned access through `char*` / `memcpy`, which the standard defines.

This is a **backend obligation to record now**, not a caveat. It is cheap to
honor before the C backend exists and a genuinely nasty class of bug to
discover after.

---

## Implementation notes

- **Origin-less pointers are new.** `unsafe_cast` produces the first pointer in
  the language with no Origin. Verify the checker tolerates that rather than
  assuming total Origin coverage — `flow/state.go` and every `CheckDerefValidity`
  path.
- **Ordering.** `size_of` / `align_of` first (pure, testable in isolation), then
  `uintptr` with its cast carve-out (still safe, still testable alone), then
  `unsafe_cast`. Each stage is independently useful and independently tested.
- **The reinterpret hole is already closed.** `compileCast` now compares
  resolved representations for pointer→pointer casts, so a type alias can no
  longer hide a cross-pointee reinterpret behind the checked `T(expr)` form
  (test: `cmd/bosc/tests/cast_pointer_alias_reinterpret_err_test.bos`). This
  matters for *this* proposal specifically: `unsafe_cast` must be the only path
  to pointer reinterpretation, or the concentration-of-unsafety property is
  false.
- **Testing.** Each primitive needs its defined behavior pinned, including the
  ugly cases: misaligned ordinary load/store succeeds; `unsafe_cast` of 0 to a
  non-nullable pointer type compiles; `uintptr` arithmetic wraps as specified.
  Per `docs/reference/COVERAGE_MATRIX.md`, these are equivalence-class tests,
  not one-offs.

---

## Deferred

Recorded so they are not rediscovered, not resolved here:

- **Debug/release build modes**, and whether `index_oob` / `nil_assert` /
  a future atomic-alignment check are elided in release.
- **Atomics** and their alignment story (leaning target-defined; see above).
- **Raising BABI-0's alignment ceiling to 16** (`TODO.md:83-99`) — independent,
  but `align_of` is what makes the allocator survive the change.
- **Whether `_heap` stays in the C transpiler's hand-written floor.**
  `PROPOSAL_c_transpiler.md:248-250` assumes it does; a Boson `_heap` changes
  that assumption in the transpiler's favor.
