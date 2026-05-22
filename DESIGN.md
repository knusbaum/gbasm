# gbasm Design Document

## Overview

gbasm is a complete toolchain for a custom language called **Boson**. It targets x86-64 Linux, producing native ELF64 executables. The entire toolchain is written in Go.

The pipeline is:

```
Boson source (.bos)
    → bosc (compiler)
    → Assembly (.bs)
    → bas (assembler)
    → Object files (.bo)
    → bld (linker)
    → ELF64 executable
```

A separate build orchestrator (`mmk` with the `boson.mmk` library) drives the toolchain, computes the package dep graph, and handles incremental builds.

Each pipeline stage produces an inspectable intermediate artifact, which makes the pipeline easy to debug. The `.bs` assembly files in particular serve as a readable window into what the compiler generates.

---

## The Boson Language

Boson is a statically typed, imperative language. It is intentionally small: no dynamic memory allocation (yet), no generics (yet), no closures. Its feature set is roughly comparable to a restricted dialect of C with first-class ownership and write-through mutability tracking.

### Types

**Integer types:**

| Type | Description |
|------|-------------|
| `i8`, `i16`, `i32`, `i64` | Signed integers of the given width |
| `u8`, `u16`, `u32`, `u64` | Unsigned integers of the given width |
| `byte` | Alias for `u8` |
| `bool` | 1-byte boolean |
| `<intlit>` | Compile-time-only type for integer literals; resolved to a concrete integer type based on context |

`<intlit>` is the type of an integer literal `42` before it is assigned somewhere. Constant expressions over `<intlit>` (e.g. `4 * 1024`) are evaluated at compile time using arbitrary-precision arithmetic and range-checked against the destination type at the point of assignment.

**Reference and composite types:**

| Type | Description |
|------|-------------|
| `*T` | Pointer to T |
| `T[]` | Slice (fat pointer: data pointer + length, 16 bytes) |
| `T[N]` | Fixed-size array |
| `struct { ... }` | Named record type |

Types are divided into **direct** (fit in a single register: scalars, pointers) and **indirect** (too large for a register: structs, arrays, slices — held as pointers in registers, with their data on the stack or in memory).

String literals have type `byte[]` (an immutable slice of bytes). The data is stored in a read-only section and the slice header has the literal's length.

### Type qualifiers

Three orthogonal qualifiers can apply to types:

- **`mut`** — write-through mutability for pointers and slices. `*mut T` lets you write to T through the pointer; `*T` does not. Nests independently at each pointer level (`*mut *T`, `**mut T`).
- **`owned`** — compile-time ownership obligation that must be discharged before the variable goes out of scope. See [Ownership](#ownership).
- **`const` / `var`** — binding mutability: whether the named binding itself can be rebound. Distinct from `mut`. Defaults: `const` for function parameters (use `var` to opt out), explicit on local declarations.

### Type aliases

```
type FD i64
type Tag i16
```

`type Name Underlying` defines a **distinct** named type. The underlying representation is identical (same size, same arithmetic), but the type system treats them as incompatible. A function that takes `FD` will not accept a bare `i64`. This is the nominal-type approach (like Go's `type Foo int`), not transparent aliasing.

Casts use the type name as a function:

```
var fd FD = FD(3)         // i64 literal → FD
var n  i64 = i64(fd)      // FD → i64 (same bits)
var t  Tag = Tag(n)       // i64 → Tag (truncating)
```

Integer literals coerce naturally. Widening (signed source → larger type) uses sign-extension; widening (unsigned source → larger type) uses zero-extension; same-size cast is a reinterpretation; narrowing truncates.

### Expressions

Arithmetic: `+`, `-`, `*`, `/`. Multiplication and division dispatch to signed (`imul`/`idiv`) or unsigned (`mul`/`div`) instructions based on the operand types.

Comparison: `==`, `!=`, `<`, `>`, `<=`, `>=`. Signed and unsigned comparisons select the appropriate `setl`/`setb`/etc. variant.

Logical: `&&`, `||`.

Unary: `-` (negation), `*` (dereference), `&` (address-of). Precedence: unary prefix > `.`/`[]` > `*`,`/` > `+`,`-` > comparisons > assignment.

Struct field access uses `.` for both direct structs and pointer-to-struct (`p.x` auto-dereferences when needed).

Array indexing: `arr[i]`. Slice/array bounds checking is inserted by the compiler; out-of-range indices call `_init.index_oob` which prints a diagnostic and exits.

Slice operations: `s[lo:hi]`, `s[lo:]`, `s[:hi]` produce new slice headers without copying data.

### Statements

```
var x i64                 // declaration without initializer
var x i64 = 42            // declaration with initializer
const y i64 = 100         // const binding (must be initialized)
x = 10                    // assignment
p.x = i + 1               // field assignment
*p = 5                    // write-through (requires p: *mut T)

if (cond) { ... } else { ... }

for (init; cond; step) { ... }

break
continue
return expr
dispose(x)                // consume an owned binding (see Ownership)
```

### Functions

```
fn add(x i64, y i64) i64 {
    return x + y
}

fn write_into(p *mut i64) {       // const parameter, but allows write-through
    *p = 42
}

fn modify(var x i64) i64 {        // var parameter — can be reassigned
    x = x + 1
    return x
}

fn close(fd owned i64) {          // owned parameter — moves the obligation
    dispose(fd)
}
```

Arguments follow the System V AMD64 ABI: the first six integer/pointer arguments go in RDI, RSI, RDX, RCX, R8, R9; additional arguments go on the stack. The return value goes in RAX.

Sub-64-bit return values are sign- or zero-extended into RAX as required by the ABI.

### Packages and Imports

```
package main
import "string"
import "stdlib/io"

fn main() {
    string.puts("hello\n")
}
```

Each source file declares a package. The package name is what callers use in source code (`string.puts(...)`). The string in `import "..."` is an opaque key into the build system's importcfg.

**Visibility model.** The compiler is driven entirely by `import` declarations and the source-level package qualifier. The build system maintains an **importcfg** mapping import strings to `.bo` file locations:

```
string=target/string.bo
stdlib/io=target/stdlib/io.bo
```

`bosc -importcfg=<file>` consumes this mapping. When source declares `import "X"`, bosc looks up X in the importcfg, loads that `.bo`, reads its embedded `pkgname`, and registers the package's exported functions under that name in the current file's namespace.

Cross-package calls in source code use the package name (from the .bo's pkgname), not the import string:

```
import "stdlib/io"        // imports a package whose pkgname is "io"
io.puts(...)              // call qualified by pkgname
```

The compiler emits fully-qualified call symbols (`io.puts`, `string.strlen`) so the linker can distinguish same-name functions in different packages.

### Built-in Functions

Built-ins are not part of the language proper — they live in runtime packages that any program imports. The standard library currently provides one package, `string`, that bundles both string utilities and basic IO:

| Function | Signature | Description |
|----------|-----------|-------------|
| `string.puts` | `(byte[]) void` | Write a byte slice to stdout |
| `string.puti` | `(i64) void` | Print a signed integer to stdout |
| `string.putb` | `(byte) void` | Print a byte value as a decimal integer |
| `string.putc` | `(byte) void` | Print a single byte as a character |
| `string.lenb` | `(byte[]) i64` | Return the length of a byte slice |
| `string.lenn` | `(i64[]) i64` | Return the length of an i64 slice |
| `string.read` | `(i64 fd, byte[] buf) i64` | Read up to len(buf) bytes; returns count or -errno. (Should be `mut byte[]` once the runtime is updated.) |
| `string.write` | `(i64 fd, byte[] buf) i64` | Write buf to fd; returns count or -errno |
| `string.open` | `(byte[] path, i64 flags, i64 mode) i64` | Open a file (path must be null-terminated in memory) |
| `string.close` | `(i64 fd) i64` | Close a file descriptor |
| `string.exit` | `(i64 code) void` | Exit the process with the given code |

The `_init` package provides `_init.start` (the ELF entry point) and `_init.index_oob` (called by bounds checks).

### Struct Literals

```
struct point { x i64, y i64, z i64 }

var p point = point{ x: 10, y: 20, z: 30 }
```

### Built-in operations

- `dispose(x)` — consume an `owned T` binding with no other effect. See [Ownership](#ownership).
- `owned(expr)` — unsafe ownership promotion. Used for BYOM patterns. See [Ownership](#bring-your-own-memory-byom).
- `T(expr)` — type cast (for any type `T`, including primitives and user-defined aliases).

---

## Ownership

### Motivation

Many resources require a specific sequence of operations before they can be abandoned. Memory must be freed. File descriptors must be closed. Database transactions must be committed or rolled back. Forgetting to do so causes leaks, corruption, or undefined behavior.

The ownership system enforces these invariants at compile time. It is not a security mechanism — it is an invariant enforcement mechanism. It does not prevent all bugs, but it makes a specific class of bugs — failing to discharge a resource obligation — into compile errors rather than runtime failures.

### The `owned` qualifier

`owned` is a compile-time qualifier that can be applied to any type. It has no effect on runtime representation — `owned i64` is still just an i64 in registers and memory. What it does is:

1. Make the value **non-copyable** — you cannot duplicate an owned value.
2. Require **explicit consumption** before the value goes out of scope — failing to do so is a compile error.

`owned` can be applied to any type regardless of where the value lives:

```
var fd owned i64 = open(...)        // stack i64, carries an obligation
var t  owned transaction = ...      // stack struct, carries an obligation
var p  owned *whatever = alloc(...) // heap pointer, carries an obligation
```

Ownership obligations are orthogonal to memory location. A file descriptor is just an integer on the stack, but it has an obligation. A heap pointer has a memory obligation. Neither implies the other.

### Creating owned values

An owned value is created by declaring it with `owned`:

```
var t owned transaction = transaction{ socket = open_socket() }
```

The obligation begins at the point of declaration. Any code that can construct a `T` can create an `owned T`. Whether external code can construct a `T` at all is a matter of field visibility — a separate concern from ownership.

### The fundamental rule: `owned` in a parameter always moves

**If a function parameter contains `owned` anywhere in its type, passing a value to it moves the obligation. The caller's variable is invalid after the call.**

If a parameter contains no `owned`, it is a plain borrow. All `owned` qualifiers on the argument are coerced away. The caller retains every obligation it had.

These are the only two cases. There is no middle ground.

```
fn open(...)           owned i64
fn read(fd i64, ...)   void        // no owned in parameter — borrows, obligation stays
fn write(fd i64, ...)  void        // no owned in parameter — borrows, obligation stays
fn close(fd owned i64) void        // owned in parameter — moves, caller's fd is invalid

fn do_something(fd i64) void {
    read(fd, ...)    // fd is plain i64 here — no obligation, borrow freely
    write(fd, ...)
}

fn main() {
    const fd owned i64 = open(...)
    do_something(fd)   // owned i64 coerces to i64 — obligation stays with caller
    read(fd, ...)      // still valid
    close(fd)          // owned in parameter — fd is moved, now invalid
    read(fd, ...)      // COMPILE ERROR: fd was moved
}
```

### Move semantics

After a variable is moved into a consuming function, it is **invalid** for the remainder of its scope. The compiler refuses any use of it — reading, writing, or passing it anywhere.

```
const t owned transaction = create_transaction(...)
commit_transaction(t)   // t moved — invalid
abort_transaction(t)    // COMPILE ERROR: t already moved
```

`var` bindings can be re-established after a move by assigning a new value:

```
var fd owned i64 = open(...)
close(fd)           // fd invalid
fd = open(...)      // fd valid again — new obligation
close(fd)
```

`const` bindings cannot be re-established — once moved, they remain invalid for the rest of the scope.

The compiler tracks invalidity across all control flow paths. A variable that is moved on one branch but not another is invalid after the branch join:

```
fn maybe_close(fd owned i64, should_close bool) void {
    if (should_close) {
        close(fd)
    }
    // COMPILE ERROR: fd may not have been consumed — else path leaves obligation open
}
```

Every path through a function must either consume every owned obligation or pass it to a function that will. The compiler also rejects consuming an outer-scope `owned` variable inside a `for` loop body: the second iteration would re-enter with an invalid variable.

### `dispose`

`dispose(x)` is a built-in consuming operation that terminates an `owned T` obligation with no other effect. It is used inside consuming functions after all cleanup work is done:

```
fn close(fd owned i64) void {
    syscall_close(fd)
    dispose(fd)   // obligation satisfied
}
```

`dispose` can also be called directly by a caller who wants to explicitly abandon an obligation without doing any other work. The type system cannot enforce whether this is semantically correct for a given type — that is the programmer's responsibility.

### Bit-level encoding

`owned` and `mut` qualifiers are represented internally as bitmasks on the type, one bit per pointer/slice level (bit 0 = outermost, bit N = after N dereferences). For example:

- `owned i64` → OwnedMask bit 0 set
- `*owned T` → OwnedMask bit 1 set (the T at the other end is owned)
- `owned *T` → OwnedMask bit 0 set (the pointer itself is owned)
- `owned *owned T` → OwnedMask bits 0+1 (both obligations exist independently)
- `*mut T` → MutMask bit 1 set (write-through to T)

The same convention applies to MutMask. This means `*mut T` and `*owned T` are placed symmetrically — both qualifiers sit between the `*` and the type they modify.

`mut` before a base type is rejected (`mut i64` is meaningless; `mut` only makes sense on a pointer or slice level). `owned` before any type is valid.

### State machines

A consuming function can return a new owned value, encoding a state machine in the type system. The compiler forces callers to handle every state, because owned values cannot be silently dropped:

```
fn start()                owned T
fn process(x owned T)     owned U   // consuming T produces a U obligation
fn finalize(x owned U)    void
fn cancel(x owned T)      void      // alternate path for T

fn other_path(x owned T)  owned R   // T can also produce an R obligation
fn resolve(x owned R)     void
```

A caller who calls `process(start())` holds `owned U` and must reach `finalize` (or another U-consuming function) on every code path. There is no way to accidentally abandon the obligation.

### Stacking

Multiple `owned` qualifiers can exist at different levels of indirection in the same type:

```
var tr owned *owned transaction = alloc(transaction)
```

`tr` carries two independent obligations: memory (the `owned *`) and the transaction state machine (the inner `owned transaction`).

Because `owned` in a parameter always moves the obligation, and because moving part of a stacked type would require the variable's type to change (typestate, see below), **stacked types can only be passed in two ways**:

1. **Exact match** — pass to a function whose parameter type matches exactly. Both obligations move together, the caller's variable is fully invalid.

```
fn destroy_both(w owned *owned transaction) void { ... }
destroy_both(tr)   // both obligations consumed, tr invalid
```

2. **Full borrow** — pass to a function with no `owned` in the parameter type. All ownership is coerced away. No obligation moves.

```
fn inspect(w *transaction) void { ... }
inspect(tr)   // tr fully borrowed, both obligations stay with caller
```

Passing `owned *owned T` to a function taking `*owned T` or `owned *T` — consuming one obligation while retaining the other — is not supported. That would require the variable's type to change at the call site, which is typestate. It is a known limitation; see the future direction section below.

### Bring-your-own-memory (BYOM)

Some patterns require separating memory ownership from value ownership. The caller allocates memory and passes a pointer to a library that initializes it; later, a separate call deinitializes it before the caller frees the memory. Because the two obligations live in separate variables, there is no stacking problem:

```
fn create_whatever(w *mut whatever) *owned whatever {
    initialize(w)
    return owned(w)   // unsafe promotion — see below
}

fn destroy_whatever(w *owned whatever) void {
    // owned in parameter — w is moved, caller's variable invalid after call
    deinit(w)
    dispose(w)
}

fn main() {
    const w  owned *whatever = alloc(whatever)     // memory obligation in w
    const w2 *owned whatever = create_whatever(w)  // whatever obligation in w2

    // w and w2 alias the same memory, but carry separate obligations.
    // The type system tracks them independently.
    // The programmer must not access whatever fields through w while w2 is live.

    destroy_whatever(w2)   // owned in parameter — w2 is moved, invalid. obligation consumed.
    free(w)                // owned in parameter — w is moved, invalid. memory freed.
}
```

`owned(expr)` is an unsafe built-in: it asserts that `expr` may be used wherever an owned obligation is required, without consuming any source variable. It can promote at any level — `owned(&r)` produces a `*owned T` from a `*T`, and the result also satisfies `owned *owned T` if the destination demands it (both obligations are asserted at once). The compiler cannot verify the semantic correctness; the programmer is responsible for the aliasing invariant.

Implicit ownership promotion is rejected: `var x owned T = some_non_owned_T` is a compile error. The only way to gain owned bits a source value doesn't have is `owned(expr)`. Integer literals are exempt (they can initialize an `owned T` directly, since there's no source value to alias).

For the common cases — stack-owned values and heap-owned values with bundled obligations — none of this complexity arises.

### Controlled copying

Non-copyability is the default for owned types. API authors can produce new owned values explicitly, including copies. A copy is simply a new independent obligation:

```
fn copy(x *T) owned T { ... }   // borrows x (no owned), returns a new obligation

fn main() {
    const fd  owned i64 = open(...)
    const fd2 owned i64 = copy(&fd)   // fd borrowed — still valid. fd2 is new.
    close(fd)
    close(fd2)
}
```

Note that `copy` takes `*T`, not `*owned T`. It does not need to own the source to read from it. This means `copy` could be called on a value that has already been disposed — it would read freed or uninitialized memory. Preventing that correctly requires a witness mechanism to enforce that the source is still live. That mechanism is not part of the current system and is deferred to a future design.

### Future direction: typestate and witnessed borrows

Two related limitations exist in the current system:

**Partial consumption of stacked types.** `owned *owned T` cannot have one obligation consumed independently of the other. Doing so would require the variable's type to change at a specific program point — that is typestate. This forces BYOM patterns to use separate variables for separate obligations rather than stacking them.

**Witnessed borrows.** There is currently no way to express "this function requires the caller to own the argument, but does not take the obligation." A function taking `*owned T` always moves. A function taking `*T` always borrows and imposes no ownership requirement on the caller. There is no middle ground. This means a function like `copy` cannot enforce that its argument is still live — it can only borrow.

Both limitations can be resolved with **typestate**: allowing a variable's type to change at specific program points as obligations are consumed or transferred. Under typestate, calling a function with `*owned T` could downgrade the caller's variable from `owned *owned T` to `owned *T` without invalidating it entirely; and a "witnessed" parameter type could assert ownership without moving it.

The current system is designed to be forward-compatible with typestate. The concepts of `owned`, move semantics, `dispose`, and stacking all carry forward unchanged. Typestate adds expressiveness without requiring the foundation to be redesigned.

---

## Mutability and the Type System

Boson has two orthogonal axes of mutability. They are independent and compose cleanly.

### Axis 1: Binding mutability (`const` / `var`)

Every binding is either constant or variable. This controls whether the binding itself can be rebound.

```
const x i16 = 100   // x cannot be reassigned
var x i16 = 100     // x can be reassigned

const myfoo foo = foo{ x: 10, y: &x }   // myfoo's fields cannot be directly written
var myfoo foo                            // myfoo's fields can be directly written
```

`const` and `var` apply uniformly to all types — integers, structs, pointers, slices.

Function parameters are `const` by default. A parameter that needs to be reassigned within the function body must be declared `var`.

### Axis 2: Write-through mutability (`*mut T`, `mut T[]`)

This axis only exists for *reference types* — pointers and slices. It controls whether you can write to the data *through* the reference. It is a property of the type, not the binding.

```
*T        // pointer: cannot write through
*mut T    // pointer: can write through

T[]       // slice: cannot write through (elements are read-only)
mut T[]   // slice: can write through (elements are writable)
```

A `const` binding to a `*mut T` cannot be rebound, but can still write through the pointer. A `var` binding to a `*T` can be rebound to a different pointer, but cannot write through it.

`mut` as a type qualifier is meaningless on non-reference types. The compiler rejects `mut i16` as a standalone type — there is no indirection involved.

### Composing the two axes

For any binding:

```
const y *i16        // cannot rebind y, cannot write through y
const y *mut i16    // cannot rebind y, CAN write through y
var y *i16          // CAN rebind y, cannot write through y
var y *mut i16      // CAN rebind y, CAN write through y
```

For struct fields, the binding mutability of the *struct instance* determines whether fields can be directly written. Pointer fields carry their own write-through permission independently:

```
struct foo {
    x i16
    y *mut i16    // y points to a mutable i16
}

const myfoo foo = ...

myfoo.x = 10    // illegal — myfoo is const, field cannot be written
myfoo.y = &z    // illegal — myfoo is const, field cannot be rebound
*myfoo.y = 100  // LEGAL — the type of y carries write-through permission
```

Pointer indirection nests correctly. Each `*` level independently carries its own `mut`:

```
const y **i16          // const binding; cannot write through either pointer level
const y **mut i16      // const binding; cannot change intermediate pointer, CAN write innermost i16
const y *mut *mut i16  // const binding; CAN change intermediate pointer; CAN write innermost i16
var   y *mut *mut i16  // all four: rebind y, change intermediate pointer, write innermost i16
```

### The address-of operator

The write-through permission of a pointer obtained via `&` matches the binding mutability of the source:

```
var x i16 = 5
const y i16 = 5

&x   // *mut i16 — x is var, so you get a write-through pointer
&y   // *i16     — y is const, so you get a read-only pointer
```

This prevents laundering a `const` binding into a `*mut` pointer. Write-through permission flows from the source.

The same rule applies to struct fields: `&myfoo.x` yields `*mut i16` if `myfoo` is `var`, and `*i16` if `myfoo` is `const`.

### Implicit coercion

`*mut T` is implicitly coercible to `*T`, and `mut T[]` is implicitly coercible to `T[]`. A more-capable reference can always be used where a less-capable one is expected:

```
fn foo(x *i16) { ... }

var myT i16
foo(&myT)   // ok: &myT is *mut i16, coerces to *i16
```

The reverse is not permitted — a `*T` cannot be passed where `*mut T` is required, since that would silently grant write permission that was not declared.

### Strings

Strings are immutable slices of bytes: `byte[]`. The `str` type (a bare pointer to null-terminated bytes) does not exist. String literals have type `byte[]`.

```
const greeting byte[] = "hello\n"   // typical string constant
```

A mutable byte buffer has type `mut byte[]`. String literals cannot have type `mut byte[]` since they live in read-only data.

String literals are emitted by the compiler with both the slice data and a trailing null byte, so they can be passed to APIs that require null-terminated strings (like the `open` syscall) even though the null is outside the slice's stated length.

### Future: unused-mutability warning

The compiler could warn when a `var` binding is never directly reassigned and no mutable reference to it (`&x`) is ever taken: "var x i16 never mutated — use const instead." Taking `&x` (which yields `*mut T` for a `var` binding) counts as a potential mutation even if the callee only reads through the pointer, to avoid false positives. The check fires at the end of each scope and at function exit for `var` parameters. Not yet implemented.

---

## The Boson Compiler (bosc)

`cmd/bosc/` implements the compiler as a single-pass pipeline over the AST. Validation happens inline during AST construction and code generation; there is no separate validator pass.

### Lexer (`lexer.go`)

Produces a flat token stream from Boson source. Recognizes keywords (`fn`, `var`, `const`, `mut`, `owned`, `dispose`, `type`, `struct`, `if`, `else`, `for`, `return`, `break`, `continue`, `import`, `package`), identifiers, integer literals, string literals, byte literals, and all operators.

Integer literals are parsed as `uint64` for full-precision representation; downstream stages decide the final type. Decimal-point syntax is accepted but the fractional part is truncated (no float support yet).

Positions (file, line, column) are recorded on every token for error messages.

### Parser (`parser.go`)

Recursive descent. Builds an AST from the token stream. Operator precedence is handled by structured recursive calls (`parseAddSub`, `parseUnary`, `parseSubexpr`, etc.). Produces positioned AST nodes so errors downstream can report source locations.

### AST (`ast.go`)

Defines all node types: declarations (package, import, struct, function, type alias, dispose, owned-promotion), statements (var/const, assign, if, for, return, break, continue, expression), and expressions (binary op, unary op, call, qualified call, cast, index, slice, field access, literal, identifier, address-of, deref).

Defines `ASTType` with `Indirection`, `ArraySize`, `Slice`, `Signed`, `MutMask`, `OwnedMask` fields. Provides equality (`Same`), compatibility (`MutCompatible`, `OwnedCompatible`), and stringification.

The `Context` type carries name resolution state: variable bindings (with `const`/`var` flags and moved/owned state), struct declarations, function declarations, imports (per-package namespace), type aliases, and the current package name.

### Importcfg loading (`main.go`)

Before parsing any source file, bosc reads the `-importcfg=<file>` flag to obtain a mapping from import keys to `.bo` paths. When source declares `import "X"`, bosc looks up `X` in this map, loads the `.bo`, reads its embedded `pkgname`, and registers that .bo's functions under that pkgname in the current Context.

A `-listimports` mode prints just the import keys declared by the input files and exits. Used by the build system to discover transitive dependencies before compiling.

### Code Generator (`compile.go`)

Walks the AST and emits `.bs` assembly text. Key responsibilities:

- **Locals**: Each `var`/`const` declaration emits a `local` (for scalars and pointers) or `bytes` (for structs and arrays) directive in the assembly. The assembler's register allocator handles actual placement.
- **Control flow**: `if`/`else` and `for` lower to compare-and-jump sequences with generated labels (`_LABEL_for_N`, `_LABEL_break_N`, `_LABEL_cont_N`, `_LABEL_return_N`).
- **Function calls**: Arguments are evaluated into temporaries, then moved into the ABI argument registers before `call`. The emitted `call` is always fully qualified (`pkg.fname`); for in-package calls the current package's name is prepended.
- **Address-of**: When `&x` is compiled, the assembler `volatile x` directive is emitted to mark x as memory-resident, ensuring coherence with the new pointer alias.
- **Struct access**: Field offsets are computed at compile time. Pointer-to-struct access auto-dereferences.
- **Slices**: A slice is two words (pointer and length). Indexing bounds-checks against the length word and computes `base + index * element_size`. Slice operations adjust both pointer and length.
- **Bounds checking**: Array and slice accesses emit a compare and a conditional call to `_init.index_oob` if the index is out of range.
- **Casts**: Type casts (`FD(x)`, `i64(fd)`) generate sign-extend, zero-extend, or truncating moves as appropriate. Widening 32→64 unsigned uses the synthetic `MOVZX r64, r/m32` form (translated by bas); widening 32→64 signed uses `MOVSXD`; narrowing uses partial-of-alloc syntax (`mov dest src:N`).
- **Multiplication / division**: 64-bit signed multiplication uses the two-operand `IMUL r64, r/m64` form, avoiding any `inreg` on user variables. Sub-64-bit signed multiplication and all unsigned multiplication/division route through fresh rax-pinned temps so the `inreg` constraints fall on temporaries, not on user-declared variables (which may be `volatile`).
- **Temporaries**: The compiler allocates temporaries as locals with names like `Temp_1`, `Temp_2`. The assembler's register allocator places these in registers or spills them to memory.

### Ownership tracking

The compiler tracks per-binding move state in `Context.movedBindings`. Each `Funcall` whose parameter type contains `owned` marks the argument variable as moved. References to moved variables are compile errors.

For `if`/`else`: the compiler snapshots `movedBindings` before each branch, compiles them independently, and at the join point compares the two snapshots. Pre-existing owned variables consumed on one branch but not the other are an error.

For `for` loops: the compiler snapshots before the loop body and checks that no pre-existing owned variable was consumed inside.

At scope exit (end of a block), every owned binding declared in that scope must be marked moved.

---

## The Assembly Language (.bs)

The `.bs` format is a custom assembly language designed specifically for use as a compiler target. It sits at a level between raw x86-64 assembly and an IR: it has explicit registers but also named variables, a register allocator, a calling-convention-aware function model, and several synthetic instructions.

### File Structure

```
package <name>

function <name>
    // directives and instructions

data <name> string "text\0"
var <name> string "text\0"
```

Every file begins with a `package` declaration. Code is grouped into `function` blocks. `data` and `var` declarations define global read-only and mutable data respectively. The package name is the source of truth for symbol qualification: defined symbols are emitted as `<pkgname>.<name>`, and bare-name relocations in this file are qualified with the package name automatically by the assembler.

Comments use `//`.

### Function Model

A function begins with its argument and local declarations, then a `prologue`, then the body, then `epilogue` and `ret`:

```
function add
    arg a rdi       // parameter pinned to RDI
    arg b rsi       // parameter pinned to RSI
    local result 64 // 64-bit local variable
    prologue

    mov result a
    add result b
    mov rax result

    epilogue
    ret
```

`prologue` saves callee-saved registers and builds the stack frame. `epilogue` tears it down. These are required in any function that uses locals.

### Directives Reference

| Directive | Syntax | Purpose |
|-----------|--------|---------|
| `package` | `package name` | Sets file/package identity. Used to qualify defined symbols and bare-name relocations. |
| `function` | `function name` | Begins a function definition |
| `type` | `type fn(...) ret` | Annotates function signature (informational; consumed by importers) |
| `data` | `data name string "..."` | Global read-only data |
| `var` | `var name string "..."` | Global mutable data |
| `local` | `local name bits [reg]` | Stack/register local variable (scalars and pointers) |
| `bytes` | `bytes name size [reg]` | Stack byte array (non-register; required for structs and arrays) |
| `arg` | `arg name reg` | Pin argument to register |
| `arg` | `arg name offset` | Argument at stack offset |
| `argi` | `argi name index [size]` | Argument at index (0→RDI, 1→RSI, ...) with optional bit-width |
| `label` | `label name` | Jump target (function-local) |
| `prologue` | `prologue` | Save callee-saved regs, set up frame |
| `epilogue` | `epilogue` | Restore regs, tear down frame |
| `use` | `use reg` | Mark register in-use |
| `acquire` | `acquire reg` | Evict variables from register, claim it |
| `release` | `release reg` | Release register back to allocator |
| `inreg` | `inreg var reg` | Force variable into register (panics on volatile variables) |
| `forget` | `forget name` | Free a local variable's storage |
| `forgetall` | `forgetall` | Free all locals |
| `evict` | `evict [reg...]` | Spill variables from registers |
| `volatile` | `volatile name` | Mark a local as permanently memory-resident. Any subsequent attempt to cache it in a register (via `inreg`, `Register()`, etc.) panics. Used by the compiler when `&name` is taken, to maintain coherence between the named variable and the pointer alias. |

### Register Set

Full x86-64 register file, all widths:

| 64-bit | 32-bit | 16-bit | 8-bit (low) | 8-bit (high) |
|--------|--------|--------|-------------|--------------|
| RAX | EAX | AX | AL | AH |
| RBX | EBX | BX | BL | BH |
| RCX | ECX | CX | CL | CH |
| RDX | EDX | DX | DL | DH |
| RSI | ESI | SI | SIL | — |
| RDI | EDI | DI | DIL | — |
| RSP | ESP | SP | SPL | — |
| RBP | EBP | BP | BPL | — |
| R8–R15 | R8D–R15D | R8W–R15W | R8B–R15B | — |

Register names are case-insensitive.

### Addressing Modes

| Mode | Syntax | Example |
|------|--------|---------|
| Register | `reg` | `mov rax rbx` |
| Immediate | `N` or `0xN` | `mov rax 42` |
| Indirect | `[reg]` | `mov rax [rbx]` |
| Indirect + offset | `[reg±N]` | `mov rax [rbp+8]` |
| Base + index×scale | `[base+index*scale]` | `mov rax [rsp+rcx*8]` |
| Named variable | `name` | `mov rax myvar` |
| Indirect named | `[name]` | `mov rax [ptr]` |
| Sized indirect | `qword[...]`, `dword[...]`, `word[...]`, `byte[...]` | `mov qword[rsp+16] 1` |
| Partial of alloc | `name:N` (N ∈ {8,16,32,64}) | `mov dest src:32` |

Sized indirects force a specific store/load width regardless of the source operand. Used to ensure full-width stores when the immediate value is small (the default encoder picks the narrowest opcode, which can corrupt slice length fields when small numbers are stored to 64-bit slots).

Partial-of-alloc syntax exposes the low N bits of an allocation as an N-bit operand. If the allocation is in a register, the encoder uses the corresponding N-bit sub-register (e.g. `eax` for the 32-bit partial of an `rax`-held alloc). If the allocation is in memory, the encoder uses an N-bit indirect at the alloc's stack slot.

### Synthetic instructions

The assembler accepts a few instruction forms that don't exist in the x86-64 ISA but translate to legal ones:

- **`movzx r64, r/m32`** — translates to `mov dest32, src32`, relying on the hardware's automatic zero-extension of 32-bit destination writes. Used by the compiler for unsigned 32→64 widening, since the real `movzx` opcode doesn't have a 32-bit source form.

(`movsxd` is a real x86-64 instruction and is used directly for signed 32→64 widening.)

### Instructions

The assembler supports the full x86-64 instruction set (600+ instructions from an embedded Intel XML database). The most commonly used:

**Data movement:** `MOV`, `MOVSX`, `MOVZX`, `MOVSXD`, `LEA`, `XCHG`, `PUSH`, `POP`

**Arithmetic:** `ADD`, `SUB`, `IMUL`, `MUL`, `IDIV`, `DIV`, `INC`, `DEC`, `NEG`

**Bitwise:** `AND`, `OR`, `XOR`, `NOT`, `SHL`, `SHR`, `SAR`, `ROL`, `ROR`

**Comparison:** `CMP`, `TEST`, `SETE`/`SETNE`/`SETL`/`SETG`/`SETLE`/`SETGE` (and other SETcc), `SETA`/`SETB`/etc. for unsigned forms.

**Jumps:** `JMP`, `JE`/`JZ`, `JNE`/`JNZ`, `JL`, `JLE`, `JG`, `JGE`, `JB`, `JBE`, `JA`, `JAE`, `JS`, `JO`, `JRCXZ`, and more

**Control:** `CALL`, `RET`, `SYSCALL`

**Sign extension into rdx:** `CQO`, `CDQ`, `CWD` (for division)

The assembler automatically selects the correct encoding variant based on operand sizes.

### String Escape Sequences

Inside `data`/`var` string literals: `\n` (newline), `\0` (null), `\\` (backslash), `\"` (quote).

### Register Allocator

The assembler includes an LRU register allocator for named locals. When a local is declared with `local name bits`, the allocator:

1. Assigns it a register from the available pool (preferring caller-saved registers R10, R11 first to minimize prologue/epilogue overhead).
2. When all registers are full and a new value is needed, spills the least-recently-used variable to the stack.
3. Tracks sub-register widths — an 8-bit local uses `AL`/`R8B`/etc., while a 64-bit local uses the full register.

Variables can be pinned to specific registers with `local name bits reg` or `inreg name reg`. The `use`/`acquire`/`release` directives give manual control over which registers the allocator is allowed to touch.

A `volatile` local can never be placed in a register. Once marked volatile, `inreg` on it panics. This is enforced even for the encoder's fallback path that promotes Indirect operands to registers on encode failure — so any instruction that has no memory form for a volatile operand surfaces immediately rather than silently re-caching.

### Calling Convention

Follows System V AMD64 ABI exactly:

- **Argument registers (in order):** RDI, RSI, RDX, RCX, R8, R9
- **Stack arguments:** pushed right-to-left, accessible at positive offsets from RSP after prologue
- **Return value:** RAX (primary), RDX (secondary for 128-bit returns)
- **Callee-saved:** RBX, RSP, RBP, R12–R15 (preserved across calls; `prologue`/`epilogue` save/restore these)
- **Caller-saved:** RAX, RCX, RDX, RSI, RDI, R8–R11 (may be clobbered by any call)

### Symbol qualification

The assembler stamps each function with its file's `package` name (`Function.Pkgname`). At resolve time, any bare relocation symbol (cross-function calls, jumps that aren't local labels, global var references) is automatically qualified with the function's package name. This means hand-written `.bs` files can use bare names internally (`call strlen`) and the assembler turns them into `string.strlen` (or whatever the package is) automatically. Cross-package calls use the full qualified form in source: `call other.func`.

Labels (jump targets declared with `label`) remain unqualified — they're function-scoped and never become relocations.

---

## The Assembler (bas)

`cmd/bas/main.go` implements the assembler as a two-pass process:

1. **Parse pass:** Reads the `.bs` file line by line, processes directives, and emits instruction records with symbolic labels and variable references.
2. **Encode pass:** Uses the core `gbasm` library to encode each instruction into x86-64 binary. Label references become PC-relative offsets resolved at encode time (or left as relocations for the linker).

A top-level `recover()` converts panics from invariant violations (e.g. `volatile`/`inreg` conflicts) into clean `Fatal: <message>` exits, suitable for test runners and tooling.

The assembler outputs a `.bo` object file containing:
- The encoded binary text section
- A symbol table (function names with package prefix, global data names)
- A relocation table (unresolved `call` and `lea` targets, all fully qualified)
- Type descriptor records for function signatures (for importers)

---

## The Linker (bld)

`cmd/bld/main.go` is a thin wrapper over `linker.go`. It accepts a list of `.bo` object files and an output path, invokes the linker, and writes the ELF64 binary. The output file is set executable.

The linker requires each input `.bo` to declare a non-empty `Pkgname` and rejects duplicates. It registers all defined functions, vars, and data under their qualified names (`pkg.name`). Since the compiler and assembler emit all relocations qualified, the linker has a simple symbol table — no bare-name fallback.

The ELF entry point is fixed: the linker looks for `_init.start`. The `_init` package (provided by the runtime's `init_linux.bs`) must define a `start` function that calls `main.main` and exits.

---

## The Core Library

The root Go package implements the low-level encoding and object format shared by `bas` and `bld`.

### `encoder.go`

x86-64 instruction encoding. Loads the Intel XML specification (`x86_64.xml`) and provides an `Encode(mnemonic, operands...)` API. Handles:
- REX prefix generation for 64-bit operands and extended registers
- ModR/M byte encoding for register, memory, and indirect operands
- SIB byte for base+index×scale addressing
- Immediate value encoding with proper sign extension
- All operand size variants (8/16/32/64-bit)

Recognizes `*Ralloc` and `*RallocPartial` as operands and resolves them to concrete registers or memory operands as needed.

### `reg.go`

Register definitions and metadata. Each register entry records its name, bit width, encoding value, and parent/child relationships (e.g., AL is the low 8 bits of AX, which is the low 16 bits of EAX, which is the low 32 bits of RAX). The width tracking enables the assembler to automatically select the correct instruction variant. `partial(N)` returns the N-bit sub-register of a 64-bit register.

### `regalloc.go`

LRU register allocator. Maintains a pool of general-purpose registers and tracks which variable currently occupies each register. When a register is needed and the pool is exhausted, evicts the least-recently-used variable to the stack. Prefers caller-saved registers to minimize save/restore overhead.

### `function.go`

Function-level state: stack frame layout, local variable offsets, argument positions, callee-saved register list, package name. Manages the prologue/epilogue code generation, the synthetic `MOVZX r64, r/m32` translation, and the pkgname-based relocation qualification at resolve time.

`Ralloc` represents a named allocation (local or argument). `RallocPartial` represents the low N bits of a named allocation; it resolves to either a sub-register or a sized indirect depending on the alloc's current location.

### `elf64.go`

ELF64 file format implementation. Builds the ELF header, section headers (`.text`, `.data`, `.bss`, `.symtab`, `.strtab`, `.rela.text`), and writes a valid ELF64 binary. Follows the ELF-64 specification and System V ABI supplement.

### `ofile.go` / `bwrite.go`

Custom binary object file format (`.bo`). Stores encoded instructions, symbol names and offsets, relocation entries, type descriptors, and the package name. `bwrite.go` provides serialization/deserialization. `ReadOFile` populates `Function.Pkgname` from the .bo's package field so the linker can use it.

### `linker.go`

Combines multiple `.bo` files into a single ELF64 executable. Concatenates text sections, merges symbol tables under fully-qualified names, resolves relocations by computing final virtual addresses, and writes the output binary.

---

## Object File Format (.bo)

The custom `.bo` format stores:

| Section | Contents |
|---------|----------|
| Header | Magic bytes, version, section count |
| Pkgname | Package identity (a single string) |
| Text | Raw x86-64 encoded bytes |
| Symbols | Name → offset mappings for defined functions/globals |
| Relocations | (offset, symbol, type) triples for unresolved references; all symbols are fully qualified |
| Type info | Function signatures for type checking by importers |

The format is simpler than ELF to make assembler output straightforward. The linker translates `.bo` → ELF64 as its final step.

---

## Build System (mmk + boson.mmk)

Beyond the toolchain itself, gbasm ships a build orchestrator integration with **mmk** (a make-like build tool with a bash-native DSL, separately maintained).

### boson.mmk

`boson.mmk` is a library that user mmkfiles include. It defines:

- **`bos_pkg`** — a pattern rule matching `target/(.*)\.bo`. For any requested `.bo` artifact under `target/`, the rule:
  1. Resolves the import path to a source directory by walking `BOSONPATH`
  2. Discovers source files (`.bos` and `.bs`) in that directory
  3. Runs `bosc -listimports` to find transitive dependencies, each becoming a `target/<path>.bo` dep
  4. At body time: writes an importcfg from its package deps, compiles each `.bos` through `bosc`, then assembles all (`.bs` and generated) files into the target `.bo`

- **`bos_exe`** — a deftype for executables. The user writes `bos_exe hello source=src :` and the rule:
  1. Discovers sources in `$source`
  2. Runs `bosc -listimports $source` to find direct imports, each becoming a `target/<path>.bo` dep
  3. Adds `target/_init.bo` as an implicit dep (the runtime entry code)
  4. At body time: builds the executable's own package locally, then links it with all the `target/*.bo` deps plus init

A package can be pure `.bos`, pure `.bs`, or mixed; `bos_pkg` handles all three uniformly by compiling any `.bos` files to `.bs` first and then passing every `.bs` (source or generated) to the assembler in a single invocation.

### Search path

`BOSONPATH` is a colon-separated list of directories (default `$BOSON_HOME/runtime:.`). An import path `foo/bar` is resolved by walking `BOSONPATH` and selecting the first entry containing `foo/bar/`.

### Minimum user-facing mmkfile

```bash
include $BOSON_HOME/boson.mmk
bos_exe hello source=src :
```

The trailing `:` declares `hello` as a target with no body or explicit deps (the deftype and defbody dep clause supply everything).

---

## Runtime

The runtime lives at `runtime/<pkg>/*.bs` under `BOSON_HOME` and is shared by every program. There is no separate runtime library binary — runtime sources are assembled and linked into every program.

| Package | Files | Purpose |
|---------|-------|---------|
| `_init`  | `init_linux.bs` | Process entry (`start`) calling `main.main`. Bounds-check trap (`index_oob`). |
| `string` | `string.bs`, `puts_linux.bs` | IO and string utilities: `puts`, `puti`, `putb`, `putc`, `lenb`, `lenn`, `read`, `write`, `open`, `close`, `exit`, plus internal `strlen`, `itoa`, `uitoa`. |

These files encode Linux-specific behavior directly. There is a `macho/` directory in the core library suggesting macOS support was planned but not yet implemented.

---

## Build and Test

The Go side (encoder, decoder, parser unit tests) and the integration test suites run via `make`:

```
make test       # Run all test suites
make go_test    # Go unit tests
make bas_test   # Assembler integration tests (29 tests)
make bosc_test  # Compiler integration tests (69 tests)
```

Each compiler integration test:
1. Compiles the `.bos` source with `bosc` (using a project-wide importcfg)
2. Assembles with `bas`
3. Links with `bld` against the runtime and init `.bo` files
4. Runs the binary and captures stdout
5. Diffs stdout against the `.bos.expected` file

Tests whose names end in `_err_test.bos` (or `_err_test.bs` for bas) are expected to fail at compile/assemble time; their `.expected` file matches the stderr output.

The assembler tests follow the same pattern but start from `.bs` files directly.

---

## Implementation Status

**Implemented and tested:**

- Integer types (i8/i16/i32/i64, u8/u16/u32/u64, byte alias, bool)
- Compile-time integer literal arithmetic (`<intlit>` type) with arbitrary precision
- Type aliases (`type Name Underlying`) with distinct-type semantics
- Type casts (`T(expr)`) including widening/narrowing/reinterpretation
- Slices (`T[]`), fixed arrays (`T[N]`), pointers (`*T`), structs
- `const`/`var` bindings with declaration initializers (`const x i64 = 42`)
- Full nested `*mut T` write-through mutability with implicit coercion
- Full `owned T` ownership type system: move semantics, `dispose()`, `owned()` promotion, if/else branch analysis, loop-body protection, scope-exit checks
- Path-based imports with qualified calls (`import "stdlib/io"; io.puts(...)`)
- mmk-driven build with auto-discovery via `bosc -listimports`
- bas synthetic instructions: `volatile`, `name:N` partials, size-qualified indirects, `movzx r64, r/m32`

**Known limitations / future direction:**

- No heap allocator. Programs use stack and fixed-size storage only.
- No floats. Lexer accepts decimal-point syntax but truncates.
- Generics / type polymorphism not implemented.
- Stacked `owned *owned T` cannot have partial consumption (needs typestate).
- No witnessed borrows.
- argv not yet exposed to `main`.
- macOS support stubbed but not implemented.
- Unused-mutability warning not implemented.

---

## Design Observations

- **Layered debuggability.** Each pipeline stage produces an inspectable artifact. A compiler bug can be isolated to the `.bs` output; an assembler bug to the `.bo` binary; a linker bug to the final ELF.
- **Assembler as IR.** The `.bs` language occupies an unusual middle ground: it has named variables and a register allocator, making it usable as a compiler IR, while still being human-readable assembly.
- **Minimal runtime.** No heap, no GC, no dynamic loading. Programs are fully static ELF64 binaries that make raw Linux syscalls.
- **Single-pass compiler.** The compiler does not perform optimization. It lowers each AST node directly to assembly, relying on the register allocator to minimize unnecessary spills.
- **Conservative-by-default ownership and mutability.** Defaults favor strictness (const, read-only, no implicit promotion) with explicit opt-in for looser forms (var, mut, owned()). The type system encodes obligations the compiler can check; programmer-asserted unsafe operations are explicit (`owned(...)`, `dispose(...)`).
