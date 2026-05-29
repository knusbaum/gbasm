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

Boson is a statically typed, imperative language. It is intentionally small: no generics (yet), no closures, and only a small compiler-supported allocator interface. Its feature set is roughly comparable to a restricted dialect of C with first-class ownership, nullable-pointer flow tracking, non-escaping borrows, and write-through mutability tracking.

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

Integer literals can be written in four bases:

| Form | Example | Description |
|------|---------|-------------|
| Decimal | `42`, `1024` | Default base |
| Hexadecimal | `0xFF`, `0xDEADBEEF` | `0x` prefix, hex digits (case-insensitive) |
| Octal | `0o755`, `0o644` | `0o` prefix |
| Binary | `0b1010`, `0b11000011` | `0b` prefix |

Character literals (`'A'`, `'\n'`) produce a `byte`. See [Strings](#strings) for the escape-sequence table that both character literals and string literals accept.

**Reference and composite types:**

| Type | Description |
|------|-------------|
| `*T` | Non-null pointer to T |
| `*?T` | Nullable pointer to T |
| `T[]` | Slice (fat pointer: data pointer + length, 16 bytes) |
| `T[N]` | Fixed-size array |
| `type Name struct { ... }` | Named record type (file scope) |
| `struct { ... }` | Anonymous struct type (usable as a parameter, return, or var type) |
| `fn(args) ret` | Function-pointer type (pointer-sized) |

Named struct types are declared with `type Name struct { ... }` (see [Type aliases with methods](#type-aliases-with-methods)); the struct body is pure data — no methods inside. Anonymous struct types appear in parameter, return, and var-declaration positions. Two anonymous struct types are compatible when their field lists agree on both name and type, field-by-field. They are otherwise identical to named structs in layout and semantics.

```
fn divide(a i64, b i64) struct { quot i64, rem i64 } {
    var q i64 = a / b
    return { quot: q, rem: a - q * b }
}

fn use_result(r struct { quot i64, rem i64 }) i64 { return r.quot + r.rem }
```

Types are divided into **direct** (fit in a single register: scalars, pointers) and **indirect** (too large for a register: structs, arrays, slices — held as pointers in registers, with their data on the stack or in memory).

String literals have type `byte[]` (an immutable slice of bytes). The data is stored in the data section (in `o.Data` rather than `o.Vars`, distinguishing immutable string constants from writable globals at the metadata level) and the slice header has the literal's length. A future hardening pass can split `.data` and `.rodata` into separate LOAD segments so this immutability is hardware-enforced; today the distinction is only at the source level.

### Type qualifiers

Three orthogonal qualifiers can apply to types:

- **`mut`** — write-through mutability for pointers and slices. `*mut T` lets you write to T through the pointer; `*T` does not. Nests independently at each pointer level (`*mut *T`, `**mut T`).
- **`?` on pointers** — nullability marker. `*T` is non-null; `*?T` may be `nil`. Non-null pointers can be used where nullable pointers are expected. Nullable pointers require a proof or assertion before non-null use: `if (p) { ... }` narrows `p` to non-null inside the true branch, `if (!p) { ... } else { ... }` narrows it in the else branch, and postfix `p?` inserts a runtime nil check and produces a non-null pointer.
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

Bitwise: `&` (AND), `|` (OR). Operate on integer types of any width. Operand widths must match; integer literals coerce to the other operand's type. `&` binds tighter than `|`, both bind tighter than comparison.

Unary: `-` (negation), `*` (dereference), `&` (address-of). Precedence: unary prefix > `.`/`[]` > `*`,`/` > `+`,`-` > `&`,`|` > comparisons > assignment.

Struct field access uses `.` for both direct structs and pointer-to-struct (`p.x` auto-dereferences when needed).

Postfix `?` asserts that a nullable pointer is non-null:

```
var p *?node = ...
if (p) {
    use(p)       // p is treated as *node in this branch
}
use(p?)          // runtime nil check, then *node
```

If flow analysis already knows the pointer is non-null, `p?` emits no runtime check. If flow analysis knows the pointer is nil, `p?` is a compile error.

Array indexing: `arr[i]`. Slice/array bounds checking is inserted by the compiler; out-of-range indices call `_init.index_oob` which prints a diagnostic and exits.

Slice operations: `s[lo:hi]`, `s[lo:]`, `s[:hi]` produce new slice headers without copying data.

### Statements

```
var x i64                 // declaration without initializer
var x i64 = 42            // declaration with initializer
var x = 42                // type inferred from initializer (i64)
const y i64 = 100         // const binding (must be initialized)
const y = 100             // const binding with inferred type
x = 10                    // assignment
p.x = i + 1               // field assignment
*p = 5                    // write-through (requires p: *mut T)

if (cond) { ... } else { ... }

for (init; cond; step) { ... }

break
continue
return expr               // return a value
return                    // bare return (only valid in void functions or as an early exit)
return a, b               // multi-value return
dispose(x)                // consume an owned binding (see Ownership)
```

**Multi-value return and destructuring bind.** A function may return more than one value by listing the return types comma-separated:

```
fn divide(a i64, b i64) i64, i64 {
    var q i64 = a / b
    return q, a - q * b
}
```

The call site binds all returned values in a single declaration. Each bind position is independent — names may be inferred or typed, and individual binds may be `var` or `const`:

```
var q, rem = divide(17, 5)                          // both inferred i64
var q2 i64, const rem2 i64 = divide(100, 7)         // explicit types, mixed const/var
```

A name in the bind list may already exist in the enclosing scope — the existing binding is reused (re-assigned). This is how the `(value, err)` pattern composes across calls without forcing fresh names every time:

```
var f, err = io.open(path1, io.O_RDONLY, 0)
if (err != 0) { ... }
f, err = io.open(path2, io.O_RDONLY, 0)             // err re-binds the existing variable
```

A single-name bind to a multi-return function is rejected — the count of binds must match the count of returns.

**Type inference for `var`/`const`.** When the type slot is omitted, the binding's type is inferred from the initializer. Untyped integer literals infer as `i64`; expressions whose type cannot be expressed as a value (a `void` call, the `nil` literal) are rejected with a directed error.

```
var p = alloc(i64)            // owned *mut i64
var r = divide(17, 5)         // struct { quot i64, rem i64 }
var n = some_void_call()      // COMPILE ERROR: cannot infer from void expression
```

`for` loops are source-level sugar. The compiler lowers them to a block containing the initializer and an unconditional loop with an explicit conditional `break`, body, and step. `continue` inside a lowered `for` carries the step expression so loop ownership and nullability checks use the same control-flow machinery as other blocks and branches.

**Statement boundaries.** Newlines terminate statements, following the Go convention. The lexer synthesizes a `;` after the last token of a line when that token can validly end a statement (identifiers, literals, `)`, `]`, `}`, `break`, `continue`, `return`). Newlines inside `(...)` or `[...]` are suppressed so multi-line argument lists, slice expressions, and continuation lines (e.g., a binary operator at the end of a line) work without ceremony. The parser sees a token stream with explicit `;` separators; users don't type them outside `for`-loop headers.

One consequence worth knowing: `} else` style is enforced. A bare `else` on a new line after `}` doesn't parse, because `}` is a statement-ender and the auto-inserted `;` separates the two.

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

Pointer parameters without `owned` are non-escaping borrows by default. The callee may read or write through the pointer according to the pointer's `mut` bits, and may forward it to other non-owning calls, but may not return it or store it somewhere that outlives the call. See [Borrowing](#borrowing).

Arguments follow the System V AMD64 ABI: the first six integer/pointer arguments go in RDI, RSI, RDX, RCX, R8, R9; additional arguments go on the stack. The single-value return goes in RAX.

Sub-64-bit return values are sign- or zero-extended into RAX as required by the ABI.

**Multi-value return.** A function may declare a comma-separated return-type list (`fn divide(a i64, b i64) i64, i64`). The compiler lowers a multi-value return to a synthetic anonymous struct (`{_0: e0, _1: e1, …}`) marked `MultiReturn=true`. The function returns that struct through the standard indirect-struct-return path: the caller allocates a `bytes` slot for the return values, the callee fills its fields, and `rax` points to the slot on return. The destructuring-bind form at the call site copies each field out into its own named binding (see [Statements](#statements)). Because the wire representation is a struct, multi-return crosses package boundaries the same way struct return types do.

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

**Cross-package struct types.** A `.bo` file carries not only its exported functions but its struct definitions as well. When source declares `import "io"`, the consuming compilation registers `io`'s structs under qualified names (`io.SomeStruct`). The same qualified-name form is used at use sites:

```
import "pair"

var p pair.pair = pair.pair{a: 3, b: 4}   // type and literal use pkg.Name
string.puti(pair.sum(p))                  // call takes pair.pair, transparent
```

The `.bs` carries Boson struct shapes via the `struct Name { … }` directive (see [Directives Reference](#directives-reference)); the assembler stores them in the `.bo` and bosc reads them back during `Context.Import`. Built-in type names and any cross-package references inside imported signatures are left alone; only leaf type names that match the producer's own structs get qualified at import time.

**Cross-package interfaces.** Interface declarations export the same way structs and type aliases do. The producer's `.bs` carries each `interface Name { method ... }` declaration via the `interface` directive; the `.bo` stores it as an `InterfaceShape` (method name, ordered params with names and ASTType-rendered type strings, plus the return-type string). On import, bosc reparses the types, qualifies any leaf names that match the producer's structs or aliases, and registers the interface under the qualified name (e.g. `io.reader`). Caller code can then write `fn use(r io.reader) { ... }` and coerce a borrowed `*io.FD` into it; the vtable is emitted in the *consumer's* `.bo` and its method-pointer relocations target the type's owning package (`io.FD.read`).

**Cross-package type aliases.** `type Name Base` declarations export the same way structs do, with their attached method tables. On import, the type is registered under the qualified name (`io.FD`); the method set is reconstructed from the already-imported function set using a method-name list carried in the `typealias` directive. Qualified casts (`io.FD(3)`) work in expression contexts and in file-scope static initializers.

**Cross-package variable access.** File-scope `var`/`const` declarations are exported from a `.bo` along with functions and struct types. Source code reads them through the qualified form `pkg.name`:

```
import "io"

fn main() {
    string.puts("hello\n")
    var f = io.STDIN          // read a cross-package var
}
```

Cross-package vars are read-only from the consumer's side. Writing through a foreign-package name is rejected; mutation must go through a function exported by the owning package.

**The `builtin` package.** A single package, `builtin`, is auto-imported into every compilation unit (except `builtin` itself). It contains the `error` interface and serves as the home for declarations that need to be visible without an explicit `import`. The `-listimports` driver always emits `builtin` first, so the build system pulls in `target/builtin.bo` automatically. Bare references to `error` resolve to `builtin.error` the same way an explicitly imported package's interface would, but without requiring `import "builtin"` at the top of every file.

### Built-in Functions

Built-ins are split into two groups: **compiler intrinsics** (lowered by bosc itself, not callable through a function pointer) and **runtime packages** (ordinary Boson packages that any program imports). The runtime packages are `string`, `io`, `_io_sys`, `_init`, `_heap`, `pair`, and `builtin`.

**Compiler intrinsics:**

| Built-in | Type | Description |
|----------|------|-------------|
| `len(s)` | `(T[]) i64` or `(T[N]) i64` | Length of a slice (runtime load of the fat-pointer length word) or fixed array (compile-time constant `N`). |
| `alloc(T)` | `owned *mut T` | Allocate writable storage for one `T`; consumes a type expression, not a runtime value. |
| `new(expr)` | `owned *mut T` | Allocate writable storage for the expression's type, initialize with `expr`, return an owned pointer. |
| `free(p)` | `void` | Free an `owned *T` / `owned *mut T` pointer and consume that pointer's obligation. |
| `dispose(x)` | `void` | Consume an `owned` binding with no runtime effect (see [Ownership](#ownership)). |
| `owned(expr)` | `owned T` | Unsafe ownership promotion (see [Bring-your-own-memory](#bring-your-own-memory-byom)). |
| `T(expr)` | `T` | Type cast (for any type `T`, including primitives and user-defined aliases). |

`alloc`, `new`, and `free` lower to `_heap.alloc(size i64)` and `_heap.free(p *mut byte)`. `len` lowers inline — slice length is a `[ref+8]` load, fixed-array length is a literal. None of these are callable through a function-pointer value: passing `len` or `alloc` as a value is a compile error.

The `builtin` package is special: bosc auto-imports it into every package (except `builtin` itself) and the `-listimports` driver always emits `builtin` first, so the build system pulls in `target/builtin.bo` automatically. The package currently exposes:

| Name | Form | Description |
|------|------|-------------|
| `error` | `interface { message(e *self) byte[]; destroy(e owned *self) }` | The standard error-condition interface. Concrete types satisfy it by providing both methods; see [Example: the error interface](#example-the-error-interface). |

Bare references to `error` resolve to `builtin.error` without needing an explicit `import "builtin"`.

**`string` package** — string formatting and stdout primitives:

| Function | Signature | Description |
|----------|-----------|-------------|
| `string.puts` | `(byte[]) void` | Write a byte slice to stdout |
| `string.puti` | `(i64) void` | Print a signed integer to stdout |
| `string.putb` | `(byte) void` | Print a byte value as a decimal integer |
| `string.putc` | `(byte) void` | Print a single byte as a character |
| `string.exit` | `(i64 code) void` | Exit the process with the given code |

File IO lives in the `io` and `_io_sys` packages. The typed FD API in `io`:

| Function/Method | Signature | Description |
|------|------|------|
| `io.O_RDONLY`, `O_WRONLY`, `O_RDWR`, `O_CREAT`, `O_EXCL`, `O_TRUNC`, `O_APPEND`, `O_NONBLOCK`, `O_CLOEXEC` | `i64` | File-mode flag constants matching Linux `<fcntl.h>`. Combine with bitwise `|`. |
| `io.open` | `(path byte[], flags i64, mode i64) owned FD, i64` | Open a file. Returns `(fd, err)` where `err` is `0` on success or a positive errno on failure. On failure the returned `FD` is invalid and must be `dispose`d rather than `close`d. |
| `io.FD.read` | `(fd *FD, buf mut byte[]) i64, i64` | Read up to `len(buf)` bytes. Returns `(n, err)` — `n=0` on EOF; partial reads with `err=0` may be retried. |
| `io.FD.write` | `(fd *FD, buf byte[]) i64, i64` | Write `buf`. Returns `(n, err)`; short writes with `err=0` are not errors. |
| `io.FD.close` | `(fd *owned FD) i64` | Close the fd and consume the owned obligation. Returns `0` on success or a positive errno. |
| `io.STDIN`/`STDOUT`/`STDERR` | `FD` | Standard file descriptors (0, 1, 2). Borrowed — must not be `close`d. |
| `io.reader` | `interface { read(r *self, buf mut byte[]) i64, i64 }` | Anything readable; `FD` satisfies it. |
| `io.writer` | `interface { write(w *self, buf byte[]) i64, i64 }` | Anything writable; `FD` satisfies it. |
| `io.copy` | `(dst writer, src reader) i64` | Copy bytes from `src` to `dst` until EOF; returns total bytes copied (or a negative errno on a read/write error). |

The raw syscall wrappers, taking i64 fd values directly, live in `_io_sys`:

| Function | Signature | Description |
|------|------|------|
| `_io_sys.read` | `(i64 fd, mut byte[] buf) i64` | Raw `read(2)` syscall |
| `_io_sys.write` | `(i64 fd, byte[] buf) i64` | Raw `write(2)` syscall |
| `_io_sys.open` | `(byte[] path, i64 flags, i64 mode) i64` | Raw `open(2)` syscall |
| `_io_sys.close` | `(i64 fd) i64` | Raw `close(2)` syscall |

The `_init` package provides `_init.start` (the ELF entry point) and `_init.index_oob` (called by bounds checks). The `_heap` package provides the bootstrap allocator (mmap-backed: each allocation is one mapping with a small size header, `free` calls `munmap`). The `pair` package exists as a minimal cross-package struct used by tests.

`alloc(T)` is only valid for zero-initializable types. Types containing non-null pointers are not zero-initializable, so they must be constructed with `new(expr)` or some other initialized storage path.

`free` consumes only allocation ownership (`owned *T`); it does not recursively dispose a pointee obligation such as `*owned T`. If the pointee is a struct with owned fields, those fields must already have been consumed before `free` is allowed.

### Struct Literals

```
type point struct { x i64, y i64, z i64 }

var p point = point{ x: 10, y: 20, z: 30 }
```

Struct literals are context-typed. Omitted fields are zero-initialized only if the field's type is zero-initializable. Non-null pointer fields are not zero-initializable and must be provided explicitly.

**Bare struct literals.** A `{ field: val, ... }` form without a leading type name is legal at any site where the destination shape is known: return statements, typed `var`/`const` initializers, assignments to a struct-typed lvalue, and function arguments whose parameter type is a struct. The literal takes the destination's struct shape. Using a bare literal where no shape is in scope (e.g. as an unbound expression) is a compile error.

```
fn divide(a i64, b i64) struct { quot i64, rem i64 } {
    var q i64 = a / b
    return { quot: q, rem: a - q * b }     // shape from return type
}

var r struct { quot i64, rem i64 } = { quot: 1, rem: 0 }
r = { quot: 99, rem: 1 }                   // shape from lvalue
```

---

## Ownership

### Motivation

Many resources require a specific sequence of operations before they can be abandoned. Memory must be freed. File descriptors must be closed. Database transactions must be committed or rolled back. Forgetting to do so causes leaks, corruption, or undefined behavior.

The ownership system enforces these invariants at compile time. It is not a security mechanism — it is an invariant enforcement mechanism. It does not prevent all bugs, but it makes a specific class of bugs — failing to discharge a resource obligation — into compile errors rather than runtime failures.

### The `owned` qualifier

`owned` is a compile-time qualifier that can be applied to any type. It has no effect on runtime representation — `owned i64` is still just an i64 in registers and memory. What it does is:

1. Make the obligation **non-duplicable** — the cleanup responsibility cannot be held by two bindings at once. Coercing an owned value to a non-owned destination produces a *borrow*: for pointers, a non-owning pointer alias; for value types, a tracked value-alias that the compiler invalidates when the owned source is moved. The only way to obtain a second independent obligation is to construct it explicitly (see [Controlled copying](#controlled-copying)).
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

The re-init form also accepts a struct literal of the matching shape, mirroring how the first initialization works:

```
var f owned foo = foo{x: 1}
dispose(f)
f = foo{x: 20}      // re-init via struct literal, no owned(...) wrapper needed
dispose(f)
```

Re-initialization is rejected while any **live owned alias** still references the binding's storage. The check uses the pointer-flow tracker: an alias is `&f` (or a struct field that copied `&f`) bound to an owned type whose obligation has not yet been consumed. Borrowed `*T` aliases do not block re-init — they carry no cleanup responsibility, so observing the new value through them is memory-safe.

```
var f owned foo = foo{x: 1}
var fp *owned foo = &f
f = foo{x: 20}        // COMPILE ERROR: fp still owns f's storage
dispose(fp)

var f owned foo = foo{x: 1}
var fp *owned foo = &f
dispose(fp)           // fp consumed; alias is no longer live
f = foo{x: 20}        // ok
dispose(f)
```

`const` bindings cannot be re-established — once moved, they remain invalid for the rest of the scope.

The compiler tracks invalidity across all control flow paths. A variable that is moved on one branch but not another is rejected at the branch join:

```
fn maybe_close(fd owned i64, should_close bool) void {
    if (should_close) {
        close(fd)
    }
    // COMPILE ERROR: fd may not have been consumed — else path leaves obligation open
}
```

Every path through a function must either consume every owned obligation or pass it to a function that will. Loop backedges and loop exits are checked separately: all paths that continue to the next iteration must agree on owned state, and all paths that leave the loop must agree on owned state. A loop body that consumes an outer-scope `owned` variable without re-establishing it before the next iteration is rejected.

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

### Aliasing and ownership

Ownership is unique, but access may alias. The compiler separates these two ideas:

- **Ownership aliases are not allowed.** An `owned T` obligation cannot be held by two bindings at once. Passing to an owned parameter moves it; assigning to another owned binding moves it; using it afterward is rejected.
- **Non-owning aliases are allowed but tracked.** A value of type `*T`, `*mut T`, `*?T`, etc. may be copied freely. A value-typed binding coerced from an owned source (`var t i64 = fd`) may also be created freely. In both cases the alias is recorded in flow state with the source's Origin, and the compiler invalidates it when the source is moved.
- **Coercing to a non-owned destination strips ownership.** Passing `owned *owned mut node` to a parameter of type `*node` or `*mut node` does not move either obligation: `Accepts` strips owned at the coercion site. The callee receives only an access capability. Coercing to an `owned` destination (e.g. `*owned node`) moves instead.
- **Value coercion is alias, not copy.** Stripping `owned` from a non-pointer source (e.g. `var t i64 = fd_owned`) duplicates the bit pattern into the destination but records a flow-state alias back to the source. Reading the destination after the source is moved is rejected as "cannot dereference pointer to X: the target was consumed," the same diagnostic a borrowed `*T` would produce. To obtain an independent obligation, construct it explicitly through a copy-returning function (see [Controlled copying](#controlled-copying)).

For example:

```
const n owned *owned mut node = new(node{...})
const a *mut node = n
const b *mut node = a

b.val = 10       // mutates the object; ownership still belongs to n
free(n)          // consumes n's allocation obligation
```

This is intentionally not Rust-style exclusive mutable borrowing. Multiple non-owning aliases to the same object may exist at the same time. The price is conservative flow invalidation: if a write through one alias may affect an owned field, facts learned through another alias are no longer trusted.

```
type box struct {
    val i64
    child owned *?owned mut node
}

fn bad(b owned *owned mut box) {
    const alias *mut box = b
    var child owned *?owned mut node = b.child
    alias.child = nil       // may have overwritten the field whose obligation was moved
    free(b)                 // COMPILE ERROR: b.child fact was invalidated
}

fn ok(b owned *owned mut box) {
    const alias *mut box = b
    var child owned *?owned mut node = b.child
    alias.val = 10          // does not touch owned storage
    free(b)                 // allowed if all owned fields are known consumed
    if (child) { free(child) }
}
```

The compiler tracks pointer aliases by origin. A pointer created by `new` or `alloc` and assigned to local `p` has origin `p`; aliases copied from it share that origin. Writes through unknown pointers, globals, conflicting branch merges, or opaque `*mut` calls are conservative and invalidate every owned-field fact that could depend on pointer precision.

Pointer-to-pointer aliases are tracked as aliases to pointer slots:

```
var alias *mut box = b
var slot *mut *mut box = &alias
*slot = other
alias.val = 1       // now affects other, not b
```

In this case `*slot = other` changes the pointer value stored in `alias`; it does not mutate the object `alias` used to point at, so facts about `b` are not invalidated merely by the rebind.

**Writing through an owned pointer.** `*p = value` where `p`'s immediate pointee is owned is the indirect form of "cannot reassign an owned binding before consuming it." When `p` is a bare symbol with a live obligation, the assignment is rejected just as `f = ...` would be on a still-live `owned f`. For non-trivial pointer expressions (`*p.field`, `**pp`, etc.), the write is rejected conservatively today; sharpening this is a future refinement on top of the pointer-flow alias machinery.

#### Why these rules exist

The ownership and aliasing rules are not meant to make aliasing impossible. They are meant to preserve a small set of invariants that make local reasoning possible:

- Every live ownership obligation has exactly one owner.
- A consuming operation either receives the obligation or is rejected.
- A caller that still owns an aggregate can prove which owned fields have already been consumed.
- A non-owning alias can observe or mutate reachable data only through the capability in its type; it cannot silently become another owner.
- A fact about an owned field remains valid only while no compiler-visible alias could have overwritten the storage that fact describes.

These invariants are what let the compiler answer questions like "is this owned value still usable?", "will this scope leak an obligation?", and "is this aggregate safe to raw-free?" without needing whole-program reasoning for ordinary local code.

The distinction between an owned value and a borrowed view is central. If `owned *owned mut node` is passed as `*mut node`, the callee may mutate the node, but it does not receive the allocation obligation and it does not receive the inner `owned node` obligation. The caller must still discharge both. This keeps ownership accounting independent from access aliasing.

The pointer-flow model exists to preserve field-consumption facts in the presence of ordinary pointer aliases. If the compiler knows that `alias` and `b` point at the same object, then `alias.val = 10` does not invalidate the fact that `b.child` was consumed, because `val` is not owned storage. But `alias.child = nil` must invalidate that fact, because it writes exactly the slot whose obligation was previously moved out. Rejecting the later `free(b)` is required for soundness: otherwise the compiler would be trusting an old fact about storage that may no longer contain the consumed value.

Pointer-to-pointer tracking is required for the same reason. A local pointer variable is itself storage. If `slot` points at the pointer variable `alias`, then `*slot = other` changes what `alias` points to. After that rebind, mutations through `alias` should affect `other`, not the object that `alias` used to point at. Without modeling pointer slots separately from pointed-to objects, the compiler would either invalidate too much or, worse, keep trusting facts for the wrong object.

#### Required for soundness

These rules are required by the ownership model:

- The obligation is non-duplicable. Two bindings cannot both hold the same cleanup responsibility; that would mean two `dispose`/`free`/`close` calls for one resource.
- Passing or assigning to an `owned` destination moves the obligation. The source cannot be used afterward.
- Coercing to a non-owned destination strips ownership at the coercion site. The destination receives an *alias*: a non-owning pointer for `*T`-shaped sources, a value-alias bound to the source's Origin for scalar sources. The destination is invalidated when the source's obligation is consumed.
- Writes that may touch owned storage invalidate facts about that storage unless the compiler can prove the write is unrelated.
- Unknown pointer state is conservative. If the compiler cannot prove what a mutable pointer aliases, it must assume owned-field facts reachable through that pointer may be stale.
- Mutable access to an owner slot is rejected. Code must not be able to replace `a` in `var a owned *T` without also consuming the old obligation.

The mutable-address rule is what address-taking of owned bindings enforces (see [The address-of operator](#the-address-of-operator)). `&x` itself carries the source's owned bits — whether the result acts as a borrow or as a move is determined by the destination type, the same way it is for value-typed `owned T` bindings. `*slot = other` is foreclosed by rejecting any `*mut` view of an owner slot, not by stripping ownership from the pointer expression.

#### Conservative current choices

Some restrictions are not fundamental to ownership, but are the current shape of the implementation:

- Mutable calls are treated as opaque unless the compiler is analyzing the write directly. A function taking `*mut T` may only write non-owned fields, but without an effect summary the caller must assume owned-field facts may be invalidated.
- Unknown aliases collapse precision. Globals, unresolved pointer origins, conflicting branch merges, and values that cross boundaries the flow model does not understand all force conservative invalidation.
- Stacked ownership cannot be partially moved. Consuming only the outer `owned *` or only the inner `owned T` would require the source binding's type/state to change.
- Non-null owned fields cannot be moved out of an aggregate. There is no valid placeholder to leave behind, so the compiler cannot represent the aggregate as still borrowable or freeable after the move.
- Stacked values and non-null aggregates have no explicit "partially consumed but still in scope" type. Nullable owned fields use `nil` as the representable consumed state; non-null fields do not have that escape hatch.
- Borrow checking is call-local and non-escaping, not a full lifetime/provenance system. It prevents simple stored borrowed pointers, but it is not intended to prove every memory-safety property.

These choices are deliberately conservative. They reject some programs that could be safe with a richer model, but they preserve the invariants above without requiring typestate, interprocedural effect summaries, or full region/lifetime analysis.

#### Benefits and guarantees

For accepted code, the compiler can guarantee the ownership properties it tracks:

- Ownership obligations are not implicitly duplicated.
- A moved owned value is not used again through its old binding.
- Raw `free` of an owned aggregate is rejected unless owned fields are known consumed or not present.
- Read-only borrows do not disturb ownership cleanup facts.
- Local pointer aliases, including multi-level pointer-slot aliases, are modeled well enough to keep precise facts when a write is unrelated and invalidate facts when a write may matter.
- Branch joins cannot silently hide ownership consumption on only one path.

This gives Boson a practical C-like aliasing surface while still making owned cleanup mechanically checkable in common local code.

#### Blind spots

The model still has important limits:

- `owned(expr)` is an assertion escape hatch. It can introduce an owned type without proving that the expression really contains fresh obligations.
- Conservative invalidation creates false positives around opaque calls, unknown pointers, globals, and complex control flow.
- The compiler does not yet have interprocedural write/effect summaries, so it cannot distinguish a harmless `*mut` helper from one that overwrites owned fields unless the write is visible locally.
- There is no general typestate model for "this binding is still live, but one obligation inside it has moved."
- There is no full lifetime model. Non-escaping borrowed parameters prevent the most direct dangling-pointer cases, but unsafe ownership promotion, raw disposal, and external code can still violate assumptions.
- The guarantees are compile-time ownership guarantees, not runtime hardware isolation. Pointer arithmetic, foreign calls, or other unsafe features can still undermine them if added or used without matching rules.

### Owned struct fields

`owned` on a struct field is conditional on the ownership of the containing value:

```
type box struct {
    x owned i64
    y i64
}
```

For a plain `box`, `x` behaves as `i64`. For an `owned box`, `x` behaves as `owned i64`. This keeps ordinary non-owning code lightweight while still letting an owned aggregate carry obligations through its fields.

Struct literals are context-typed. When a literal initializes or returns an owned struct, every owned field must be present and must be initialized from an owned value:

```
fn make_box() owned box {
    var x owned i64 = 42
    return box{ x: x, y: 10 }
}
```

Borrowing an owned struct as a plain parameter strips field ownership inside the callee, just like borrowing any other owned value:

```
fn inspect(b box) {
    use(b.x)       // b.x is i64 here, not owned i64
}
```

`owned(expr)` remains an explicit assertion escape hatch: it promotes the expression to an owned type without proving that every owned field came from an owned source. This is useful for construction patterns that the compiler does not yet track precisely.

Direct reassignment of an owned field through an owned aggregate is rejected. Replacing such a field safely requires partial-disposal / typestate support, which is intentionally deferred:

```
var b owned box = make_box()
b.x = 1           // COMPILE ERROR
```

Owned nullable pointer fields may be moved out of an owned aggregate. Moving such a field copies the pointer to the destination, writes `nil` back into the original field, and records that the field's ownership obligation has been consumed:

```
type node struct {
    val i64
    next owned *?owned mut node
}

fn destroy_one(n owned *owned mut node) owned *?owned mut node {
    var next owned *?owned mut node = n.next  // consumes n.next, sets n.next = nil
    free(n)                                  // allowed: n.next is known consumed
    return next
}
```

Non-null pointer fields cannot be moved out this way because there is no valid nil value to leave behind. If a field is intended to be moveable, it must be nullable (`*?T`).

The compiler tracks consumed owned fields through flow paths such as `curr.next`. These facts participate in branch merging: if one branch consumes an owned field and another does not, the join is rejected. Assigning to a path invalidates facts for that path and its descendants, so a fact like `curr.next consumed` does not survive `curr = next`.

Raw deallocation via `free` checks owned fields before freeing storage:

```
fn bad(l owned *owned mut node) {
    free(l)        // COMPILE ERROR: l.next still owned
}

fn also_bad(l owned *owned mut node) {
    free(l.next)   // COMPILE ERROR: l.next.next still owned
}
```

Passing an owned field to any consuming function uses the same ownership-consumption path as `free`. `free` is special only because it is the raw allocator primitive; ordinary consuming functions take responsibility for the obligation they receive.

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

### Nullable pointers and ownership

Nullable owned pointers are useful for data structures because they provide a clear "empty" value when moving ownership out of fields:

```
var next owned *?owned mut node = curr.next
```

The source field becomes `nil`, so there is never a second owned pointer to the same allocation. Flow analysis also understands boolean checks on nullable pointers:

```
if (next) {
    curr = next        // next is known non-null in this branch
} else {
    dispose(next)      // checking false consumes the nullable owned nil obligation
}
```

Postfix `?` can be used when the programmer knows a nullable pointer is non-null but flow analysis cannot prove it:

```
curr = next?
```

If the compiler already knows the pointer is non-null, no runtime check is emitted. If it knows the pointer is nil, the assertion is rejected.

### Controlled copying

Coercion creates an alias, not a duplicate obligation. To produce a second independent obligation, an API author writes an explicit constructor that returns a fresh `owned T`:

```
fn copy(x *T) owned T { ... }   // borrows x (no owned), returns a new obligation

fn main() {
    const fd  owned i64 = open(...)
    const fd2 owned i64 = copy(&fd)   // fd borrowed — still valid. fd2 is new.
    close(fd)
    close(fd2)
}
```

The implicit-coercion path that creates aliases (described in [Aliasing and ownership](#aliasing-and-ownership)) handles the "I want to read this value as a non-owned thing" case. `copy` handles the "I want a second independent obligation" case. The two never collide: aliases are invalidated when the source is moved; constructed copies are fresh obligations the source's lifetime does not constrain.

Note that `copy` takes `*T`, not `*owned T`. It does not need to own the source to read from it. This means `copy` could be called on a value that has already been disposed — it would read freed or uninitialized memory. Preventing that correctly requires a witness mechanism to enforce that the source is still live. That mechanism is not part of the current system and is deferred to a future design.

### Future direction: typestate and witnessed borrows

Two related limitations exist in the current system:

**Partial consumption of stacked types.** `owned *owned T` cannot have one obligation consumed independently of the other. Doing so would require the variable's type to change at a specific program point — that is typestate. This forces BYOM patterns to use separate variables for separate obligations rather than stacking them.

**Witnessed borrows.** There is currently no way to express "this function requires the caller to own the argument, but does not take the obligation." A function taking `*owned T` always moves. A function taking `*T` always borrows and imposes no ownership requirement on the caller. There is no middle ground. This means a function like `copy` cannot enforce that its argument is still live — it can only borrow.

Both limitations can be resolved with **typestate**: allowing a variable's type to change at specific program points as obligations are consumed or transferred. Under typestate, calling a function with `*owned T` could downgrade the caller's variable from `owned *owned T` to `owned *T` without invalidating it entirely; and a "witnessed" parameter type could assert ownership without moving it.

The current system is designed to be forward-compatible with typestate. The concepts of `owned`, move semantics, `dispose`, and stacking all carry forward unchanged. Typestate adds expressiveness without requiring the foundation to be redesigned.

---

## Borrowing

Borrowing is the non-owning side of ownership. Passing an owned value to a parameter whose type contains no `owned` borrows the value; ownership stays with the caller.

```
fn print_node(n *node) void {
    string.puti(n.val)
}

fn destroy_node(n owned *owned mut node) void {
    ...
}

fn main() {
    const n owned *owned mut node = new(node{...})
    print_node(n)       // borrow; n remains owned by caller
    destroy_node(n)     // move; n is invalid after this call
}
```

### Non-escaping by default

Pointer parameters without `owned` are non-escaping borrows by default. A borrowed pointer may be used during the call and forwarded to other functions that also take non-owning pointer parameters:

```
fn read_node(n *node) i64 {
    return n.val
}

fn forward(n *node) i64 {
    return read_node(n)     // allowed: forwarded borrow
}
```

But a borrowed pointer may not be stored or returned:

```
var saved *?node

fn bad_return(n *node) *node {
    return n                // COMPILE ERROR: borrowed pointer escapes
}

fn bad_global(n *node) {
    saved = n               // COMPILE ERROR: borrowed pointer escapes
}

type holder struct {
    n *?node
}

fn bad_field(n *node) {
    var h holder
    h.n = n                 // COMPILE ERROR: borrowed pointer escapes
}

fn bad_literal(n *node) {
    var h holder = holder{n: n}  // COMPILE ERROR
}
```

Local pointer aliases preserve borrowed provenance:

```
fn bad_alias(n *node) *node {
    var alias *node = n
    return alias            // COMPILE ERROR: alias is also borrowed
}
```

This is intentionally not a full Rust-style borrow checker. The first rule is simpler: borrowed pointer parameters are call-local capabilities. They can be used and forwarded, but not persisted.

### Borrowing and ownership cleanup

The current system verifies ownership cleanup along compiler-visible ownership paths. Non-escaping borrows are what make that meaningful: a function taking `*T` is not allowed to stash the pointer and observe freed storage later.

Within a function, mutable borrows interact with owned-field facts through pointer-flow analysis. The compiler tracks which local pointer bindings share an origin and invalidates owned-field facts when a mutable write or opaque `*mut` call may have changed owned storage through an alias.

Read-only borrows preserve facts:

```
fn inspect(b *box) i64 { return b.val }

fn destroy(b owned *owned mut box) {
    var child owned *?owned mut node = b.child
    inspect(b)       // read-only borrow; b.child fact remains valid
    free(b)
    if (child) { free(child) }
}
```

Mutable borrows are conservative across function calls because the callee body may be unavailable or may write through the pointer:

```
fn touch(b *mut box) { b.val = b.val + 1 }

fn destroy(b owned *owned mut box) {
    var child owned *?owned mut node = b.child
    touch(b)         // may have written owned fields; b.child fact invalidated
    free(b)          // COMPILE ERROR
}
```

Direct field writes are more precise: writing a non-owned field such as `b.val` does not invalidate facts for `b.child`, while writing an owned field does.

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
type foo struct {
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

Assignment through `*p` checks mutability of the immediate pointee slot, not the deepest pointee. Therefore `*mut *mut T` permits replacing the intermediate pointer with `*p = other`, while `**mut T` only permits mutating the final `T` after another dereference; it does not permit replacing `*p`.

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

`&` accepts more than bare names. At runtime, `&someVar`, `&arr[i]` (with constant or runtime index), and `&s.field` all produce a `*T` to the addressed storage; the compiler delegates index and field forms to the existing lvalue-walk machinery, which already knows how to compute element/field addresses.

Owned bindings have one extra rule. Taking a mutable address of an owned binding is rejected, because it would expose the owner slot itself and allow code to overwrite the obligation without consuming it:

```
var a owned *owned mut thing = new(thing{...})
var slot *mut *mut thing = &a      // COMPILE ERROR
```

`&` of an owned binding preserves the owned bits in the resulting pointer type. Whether the result acts as a borrow or as an ownership transfer is determined entirely by the destination type:

```
var x owned foo = make_foo()

inspect(&x)                  // param is *foo: Accepts strips owned, x stays owned (borrow)
var y *owned foo = &x        // dest is *owned foo: x is moved; y now carries the obligation
dispose(y)                   // discharges through y
// x is no longer usable

const a owned *owned mut thing = new(thing{...})
const slot *owned *owned mut thing = &a     // const owned ptr: a is moved into slot
free(slot)
```

This mirrors how value-typed owned bindings behave: `var y owned T = x` moves x because the destination is owned, while `var y T = x` would borrow. The pointer form follows the same destination-driven rule.

The compiler enforces these invariants without relying on `&` itself stripping ownership:

1. `Accepts` strips owned at coercion sites whose destination is non-owned, so `inspect(&x)` (param `*foo`) borrows without moving.
2. Move tracking marks the underlying variable as consumed when its address is delivered to an owned destination, and rejects subsequent reads or address-taking of the moved binding.
3. The mutable-address rule above prevents creating any `*mut`-shaped view of an owner slot, eliminating slot-replacement leaks.

In static-init context (see [Static Initializers](#static-initializers)) two more forms become legal:

- `&someGlobal` — pointer slot relocated to the named global.
- `&SomeStruct{...}` — allocates an anonymous file-scope global to hold the struct literal's bytes, then relocates the pointer slot to it. Composes recursively: `foo{bar: &bar{inner: &inner{...}}}` produces one anonymous global per nested `&`.

`&literal` at runtime is rejected with "cannot take the address of this expression at runtime; address-of-literal is only valid in static initializers" — there's no stable storage to point at outside file scope.

### Function pointers

A function-pointer type `fn(args) ret` is a pointer-sized slot holding the address of a function with the given signature. `&someFunc` produces a value of that type:

```
fn add(x i64, y i64) i64 { return x + y }

var op fn(i64, i64) i64 = &add       // file scope: static reloc to add's symbol
fn use() {
    var fp fn(i64, i64) i64 = &add   // function scope: lea of add's symbol
    fp = &sub                         // reassignment
    string.puti(fp(3, 4))             // indirect call
}
```

Function-pointer-typed parameters, return types, and struct fields compose the same way:

```
fn apply(f fn(i64, i64) i64, a i64, b i64) i64 {
    return f(a, b)
}
apply(&add, 100, 25)

fn pick(which i64) fn(i64, i64) i64 {
    if (which == 0) { return &add }
    return &sub
}
var chosen fn(i64, i64) i64 = pick(0)

type dispatch struct { op fn(i64, i64) i64 }
var d dispatch = dispatch{op: &add}
d.op(7, 8)                            // struct-field indirect call
d.op = &sub                            // field reassignment
```

Implementation: a function-pointer type is an `ASTType` with `FuncSig != nil` (no `Name`, no `Element`). It's pointer-sized (8 bytes), and its type string renders as `fn(...)ret` without spaces so the rendered form survives bas's whitespace-tokenized `var name type` directive. At a static initializer, `&someFunc` encodes as an 8-byte zero slot with a `DataReloc` against `pkg.fname`; at runtime it lowers to `lea reg, pkg.fname` (the linker resolves data and code symbols uniformly). Indirect calls use `call r/m64` rather than the rel32 form: bas's CALL handler dispatches to the indirect encoding whenever the operand is a Ralloc or register, and the bosc compiler loads the target into a temp before evicting caller-saved registers so a register-resident function-pointer argument isn't lost during arg setup. Struct-field calls (`d.op(...)`) reach the indirect-call path through the same Funcall dispatch — the parser packs `d.op(args)` as `Funcall{Pkg: "d", FName: "op"}` regardless of whether `d` is a package or a variable, and the compiler distinguishes the cases by looking `d` up in the package table first, then the local/global variable table.

Parser limitation: `(args)` postfix is only legal after a bare symbol or `pkg.fn` shape, so `pick(0)(args)` (calling the result of a call directly) requires binding the returned pointer to a name first. Lifting this would require generalizing `parsePostfix` to allow `(args)` after any expression whose type resolves to a `FuncSig`.

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

A mutable byte buffer has type `mut byte[]`. String literals cannot be coerced to `mut byte[]` — they're emitted by the compiler as `data` rather than `var` (the metadata distinction noted under [Types](#types)) and are intended to be treated as read-only even though the current loader maps the whole data section writable.

String literals are emitted by the compiler with both the slice data and a trailing null byte, so they can be passed to APIs that require null-terminated strings (like the `open` syscall) even though the null is outside the slice's stated length.

**Escape sequences** in string and character literals:

| Escape | Byte |
|--------|------|
| `\n` | LF (0x0a) |
| `\r` | CR (0x0d) |
| `\t` | TAB (0x09) |
| `\0` | NUL (0x00) |
| `\\` | Backslash |
| `\"` | Quote |
| `\'` | Single quote (in character literals) |
| `\xHH` | Arbitrary byte; exactly two hex digits |

### Future: unused-mutability warning

The compiler could warn when a `var` binding is never directly reassigned and no mutable reference to it (`&x`) is ever taken: "var x i16 never mutated — use const instead." Taking `&x` (which yields `*mut T` for a `var` binding) counts as a potential mutation even if the callee only reads through the pointer, to avoid false positives. The check fires at the end of each scope and at function exit for `var` parameters. Not yet implemented.

---

## Interfaces

Interfaces provide structural polymorphism over types that share a common set of method signatures. An interface value is a fat pointer (data pointer + vtable pointer, 16 bytes) whose dispatch table is generated statically by the compiler at each coercion site.

### Type aliases with methods

`type Name Underlying` declares a distinct named type. The underlying type may be any type expression — a scalar, a pointer, a function-pointer type, or a `struct { ... }` body. An optional method block follows:

```
type foo bar {
    do_something(self *foo) {
        string.puts("did something\n")
    }
    compute(self *foo, x i64) i64 {
        return self.x + x
    }
}
```

`struct { ... }` as the underlying type is the standard way to define a named record with fields. The struct body is **pure data** — method definitions belong in the type block, not in the struct body:

```
type point struct {
    x i64
    y i64
} {
    scale(self *point, n i64) {
        self.x = self.x * n
        self.y = self.y * n
    }
}
```

Field separators inside a struct body may be `,`, a newline (the lexer inserts `;` automatically), or both; a trailing separator before `}` is allowed.

- Instance methods take a pointer receiver as their first parameter. The parameter name is arbitrary; `self` is conventional. **Value receivers are not allowed.**
- The receiver may carry ownership qualifiers: `*T` (borrow), `*mut T` (mutable borrow), `owned *owned T` (consuming — takes ownership of the pointed-to value).
- **Static methods** take *no* receiver. They are namespaced under the type and called as `T.method(args)`. A static method has the same symbol shape (`pkg.T.method`) as an instance method; the only difference is that no receiver is implicit at the call site.
- Methods are emitted as regular functions with three-part symbol names: `pkg.TypeName.method_name` (e.g. `mypkg.foo.compute`).
- `type foo bar` without a block continues to work as before — a distinct named type with no attached methods.

**Concrete method call desugaring:**

- `v.method(args...)` where `v` has type `T` → `T.method(&v, args...)` (address taken automatically for the pointer receiver).
- `p.method(args...)` where `p` has type `*T` or `*mut T` → `T.method(p, args...)` (passed directly, no extra indirection).
- `(expr).method(args...)` where `expr` is an arbitrary expression of concrete type `T` → the expression is evaluated, its type looked up, and dispatched the same way. Interface-typed expression receivers are not yet supported; bind the value to a named variable first.
- `T.method(args...)` where `T` is a type with a zero-receiver static method → direct call to `pkg.T.method(args...)` with no implicit receiver.
- Ownership qualifiers on the receiver are checked at the call site: calling a method with `owned *owned T` receiver on a non-owned binding is a compile error.
- Calling a static method through an instance (`v.method(...)` where `T.method` has no receiver) is rejected with a directed error suggesting the `T.method(...)` form.

### Interface declarations

```
interface Writer {
    write(self *self, b byte[]) i64
}

interface error {
    message(self *self) byte[]
    destroy(self owned *self)
}
```

- `self` is a reserved type placeholder in interface method signatures. It substitutes for the concrete implementing type at satisfaction-check time.
- Method signatures use the same ownership qualifiers as regular function parameters. The placeholder type `self` can appear at any pointer level: `*self`, `*mut self`, `owned *self`, `*owned self`, `owned *owned self`.
- The parameter name before the receiver type (e.g. `self` in `self *self`) is arbitrary and ignored by the compiler; only the type matters for satisfaction checking.
- The interface name becomes a type usable in parameter positions, return types, and variable declarations.

### Structural satisfaction

A type `T` satisfies interface `I` if, for every method `m` declared in `I`, type `T` has a method where:
- The name matches exactly.
- The first-parameter type matches after substituting `self` → `T` (including all ownership and mutability qualifiers).
- The remaining parameter types and return type match exactly.

Satisfaction is checked at coercion sites only — there is no eager or declaration-time check. If a coercion is never written, the compiler never checks whether the type satisfies the interface.

### Interface values (fat pointer)

An interface value is 16 bytes regardless of how many methods the interface declares:

```
{ data *byte, vtable *VtableShape }
```

The `data` pointer is an opaque type-erased pointer to the concrete value. The `vtable` pointer points to a compiler-generated static global.

### Vtable layout

For concrete type `T` satisfying interface `I` with N methods, the compiler emits one static vtable global per `(T, I)` pair, named `__vtable_pkg.T_I`:

```
data __vtable_mypkg.foo_Writer {
    bytes 8
    reloc 0 mypkg.foo.write 0
    ...one entry per method, in interface declaration order...
}
```

Each entry is an 8-byte function pointer to the concrete method. Entries appear in the order methods are declared in the interface.

**ABI note:** vtable entries are called with the opaque `data` pointer as the first argument, followed by the remaining method arguments. Since `*byte` and `*T` have identical representation and calling convention on x86-64 (pointer-sized register), no thunks are needed. The concrete method interprets its first argument as `*T` and the caller passes the interface's `data` field — both sides agree on the machine-level ABI.

### Ownership in interfaces

Interface types carry an optional `owned` qualifier:

- `I` (no qualifier): the interface value **borrows** the underlying data.
  - Only methods with non-owned receivers (`*self`, `*mut self`) are callable.
  - Calling a method whose receiver carries any `owned` bit is a compile error: "Cannot call consuming method I.m on non-owned I".
- `owned I`: the interface value **owns** the underlying data.
  - Methods with any receiver qualifier are callable.
  - Calling a method whose receiver carries any `owned` bit consumes the interface binding. After the call, the interface variable is dead — any further use is a compile error.

Both representations are identical at runtime (16 bytes). The ownership qualifier is compile-time-only, consistent with how `owned *T` differs from `*T` elsewhere in the type system.

The "consuming" check is keyed on `HasOwned()` of the receiver parameter — `owned *self`, `*owned self`, and `owned *owned self` all count. The canonical form for a method that takes the heap pointer and frees it is `owned *self` (the pointer itself is owned; passing it to `free` discharges the obligation).

**Coercion from a concrete owned pointer to `owned I`:**

```
const e owned *mut myerror = new(myerror(42))   // owned *mut myerror
const err owned error = e                       // moves ownership into the interface value; e is consumed
err.message()                                   // ok — borrows from owned interface
err.destroy()                                   // ok — consumes err (owned-receiver method)
// err is no longer usable after destroy()
```

Stack-allocated values can only coerce to a non-owned interface (borrow). An `owned I` value requires a source whose pointer level carries an `owned` bit.

**Calling a consuming method on an owned interface:**

1. Load the data pointer and the function pointer (`vtable[methodIdx]`) from the interface binding into registers.
2. Compile and place user arguments into the user-arg registers (`rsi`, `rdx`, …).
3. Mark the interface binding consumed (`MoveConsume`) before the call — the data pointer is already captured in a register at this point.
4. Move the data pointer into `rdi` and emit the indirect `call`.

The concrete method (e.g. `destroy(self owned *myerror)`) receives the pointer as `owned *myerror` and is responsible for calling `free` (or `dispose`, for non-heap-backed concrete types) to discharge it.

### Example: the error interface

```
interface error {
    message(self *self) byte[]
    destroy(self owned *self)
}

type myerror i64 {
    message(self *myerror) byte[] {
        return "an error occurred\n"
    }
    destroy(self owned *myerror) {
        free(self)
    }
}

fn make_error() owned error {
    return new(myerror(42))   // coerces owned *mut myerror → owned error
}

fn handle(err owned error) {
    string.puts(err.message())   // borrows from owned err; err survives
    err.destroy()                // consumes err; cannot use err after this
}
```

### Implementation notes

A change to add interfaces touches each compiler layer in order:

1. **Lexer** (`cmd/bosc/lexer.go`): add `interface` and `self` as keywords.
2. **Parser** (`cmd/bosc/parser.go`): extend `type Name Base` to parse an optional `{ method... }` block; add `interface Name { sig... }` top-level form; add method-signature parsing (method name, parameter list with `self` placeholder, return type, optional body).
3. **AST** (`cmd/bosc/ast.go`): new node types for method definitions and interface declarations; new `ASTType` variant for interface types; `Context` extended to map type names → method sets and interface names → method signatures.
4. **Checker** (`cmd/bosc/checker.go`): structural satisfaction check at coercion sites; ownership compatibility checks at interface method call sites (owned vs. non-owned interface value vs. receiver qualifier).
5. **Codegen** (`cmd/bosc/compile.go`):
   - Method definitions → emit as `.bs` functions named `pkg.TypeName.method`.
   - Concrete method call on `v.method(args)` → desugar to `pkg.TypeName.method(&v, args)` or `pkg.TypeName.method(v, args)` depending on whether `v` is already a pointer.
   - Coercion from concrete type to interface type → emit `__vtable_pkg.T_I` static global (if not already emitted for this pair); build 16-byte fat pointer in two slots (`data` + vtable address).
   - Interface method call → load vtable, index to method offset, indirect call with data pointer prepended.
6. **Symbol naming**: three-part `pkg.TypeName.method` names flow through the assembler's auto-qualification and the linker's symbol table without structural changes — they are just dot-separated symbol strings like cross-package calls today.

---

## File-Scope Declarations

`var` and `const` work at file scope as well as inside functions:

```
var counter i64                  // zero-initialized
var answer  i64 = 42             // primitive literal
var label   byte = 'Z'
var buf     byte[100]            // zero-filled fixed array

const greeting byte[] = "hi\n"   // slice over a string constant
```

File-scope declarations are visible to every function in the package, regardless of source order. They are forward-bindable: a function defined earlier in the file can call into a global declared later. This is consistent with how cross-function calls work and is implemented by an early pass that registers all top-level names before any body codegen happens.

### Static Initializers

Initializers on file-scope `var` (and `const`) declarations must be **compile-time constants** in the bosc-internal sense: the encoder must be able to produce the value's bytes and any pointer relocations at compile time, with no runtime code.

The supported forms compose recursively:

- Integer literals (any width, any signedness; negative via the `0 - N` parser shape).
- Byte literals.
- String literals into `byte[N]` (inline copy, zero-padded).
- String literals into `byte[]` (16-byte slice header with a relocation to a string constant in `.data`).
- Struct literals (each field encoded recursively, byte offsets concatenated, relocations shifted into the outer struct's coordinates).
- Array literals `[e1, e2, …]` into `T[N]` (element-by-element encoding, length must match) or into `T[]` (anonymous backing array `T[len]` queued via the anonymous-globals path, plus a 16-byte slice header with a relocation to it).
- `&someGlobal` — 8-byte pointer slot with a relocation to the named global.
- `&SomeStruct{...}` — recursively encodes the inner struct into a fresh anonymous global (`__static_0`, `__static_1`, …) and emits a pointer slot relocated to it.
- `&someGlobalArr[N]` for compile-time-constant N — a single relocation to the array's symbol with `Addend = N * elementSize`. Pointer-into-array without any auxiliary storage.
- Type-alias casts of a literal integer — `var fd FD = FD(3)` (or the qualified form `io.FD(3)`). The encoder evaluates the inner literal at compile time and writes the resulting bytes into the alias's slot.

Anything else (function calls, runtime expressions, type mismatches, length mismatches) produces a specific diagnostic at compile time.

**Pointer fields in static structures** work naturally because the encoder is recursive and `Var.Relocs` is per-var:

```
type config struct {
    threshold i64
    current   *i64       // pointer field
    greeting  byte[]     // slice field
}

var counter i64 = 0
var cfg config = config{
    threshold: 100,
    current:   &counter,
    greeting:  "configured\n",
}
```

`cfg` is a 32-byte global with two relocations: one for `current` (pointing at `counter`) and one for `greeting.ptr` (pointing at the auto-generated string constant). The `greeting.len` field is encoded inline.

**Anonymous globals** carry their own bytes and relocations; the linker treats them like any other named global. Each call to `&literal` allocates a fresh anonymous, with no deduplication. Two `&SomeStruct{a:1}` literals produce two distinct globals; structural-equality dedup is a possible future optimization but is intentionally absent to keep semantics simple (identity matches occurrence, not value).

### Implementation: anonymous globals and relocations

At the `.bo` level, every `Var` may carry a list of `DataReloc` entries. Each `DataReloc` is `{Offset uint32, Symbol string, Addend int64}` and instructs the linker to write the absolute virtual address of `Symbol + Addend` into the 8-byte slot at `Offset` within the var's bytes. This is distinct from `Relocation`, which encodes 32-bit PC-relative displacements for code references.

At link time, the linker walks each placed var's `Relocs` and applies them. Reachability extends through `Relocs` too: when a var is placed in the data section because some piece of code references it, all its `Relocs` targets are recursively placed.

---

## The Boson Compiler (bosc)

`cmd/bosc/` implements the compiler as a single-pass pipeline over the AST. Validation happens inline during AST construction and code generation; there is no separate validator pass.

### Lexer (`lexer.go`)

Produces a flat token stream from Boson source. Recognizes keywords (`fn`, `var`, `const`, `mut`, `owned`, `dispose`, `type`, `struct`, `interface`, `if`, `else`, `for`, `return`, `break`, `continue`, `import`, `package`), identifiers, integer literals (decimal, `0x`, `0o`, `0b`), string and character literals (with `\n`, `\r`, `\t`, `\0`, `\\`, `\"`, `\'`, `\xHH` escapes), and all operators.

Integer literals are parsed as `uint64` for full-precision representation; downstream stages decide the final type. Decimal-point syntax is accepted but the fractional part is truncated (no float support yet).

Positions (file, line, column) are recorded on every token for error messages.

The lexer also performs **Go-style automatic semicolon insertion**: on a newline whose preceding token can end a statement (identifiers, literals, `)`, `]`, `}`, `break`/`continue`/`return`) and where paren/bracket nesting depth is zero, it synthesizes a `tok_semicolon` before continuing. This makes the parser's statement-boundary logic uniform and fixes a family of bugs where a unary `*` or `-` at the start of a statement would be greedily consumed as a binary operator on the previous statement. See [Statement boundaries](#statements).

### Parser (`parser.go`)

Recursive descent. Builds an AST from the token stream. Operator precedence is handled by structured recursive calls (`parseAddSub`, `parseUnary`, `parseSubexpr`, etc.). Produces positioned AST nodes so errors downstream can report source locations.

Statement-separating `;` tokens (typically auto-inserted by the lexer; see [Lexer](#lexer-lexergo)) are skipped uniformly at the top level, inside block bodies, between struct field decls, and inside struct literals. `for`-loop headers expect them explicitly (the existing `(init; cond; step)` form).

Postfix chains compose: `a.b[i].c[lo:hi]` is parsed by a single `parsePostfix` loop that handles `.member`, `[index]`, `[lo:hi]`, `(args)`, and `{fields}` uniformly on any prior value. The `pkg.fn(args)` and `pkg.Type{fields}` forms are recognized by the same loop (Dot-of-symbol followed by `(` or `{`).

### AST (`ast.go`)

Defines all node types: declarations (package, import, struct, function, type alias, dispose, owned-promotion), statements (var/const, assign, if, for, return, break, continue, expression), and expressions (binary op, unary op, call, qualified call, cast, index, slice, field access, literal, identifier, address-of, deref).

Defines `ASTType` with `Name`, `Indirection`, `ArraySize`, `Element`, `Signed`, `MutMask`, `OwnedMask` fields. A slice/array type is identified by `Element != nil` (with `ArraySize > 0` distinguishing array from slice); pointer/scalar types use `Name` and `Indirection`. Provides equality (`Same`), compatibility (`MutCompatible`, `OwnedCompatible`), and stringification. `Same` is structural — `ASTType` carries a pointer (`Element`) so `==` comparison is unsafe.

`Address` has two forms: `{Var string}` for address-of-named-variable, and `{Lit AST}` for address-of-literal (used in static-init context).

The `Context` type carries name resolution state: variable bindings (with `const`/`var` flags and moved/owned state), struct declarations, function declarations, imports (per-package namespace), type aliases, file-scope address-backed names (`addressNames`), pending anonymous-global emissions (`anonGlobals`), and the current package name. Helpers like `NameIsAddress(name)` consult both the explicit globals set and the type-based `typeIsMemoryBacked(t)` predicate to decide whether codegen should treat a name as an address (load through it) or as a value.

### Importcfg loading (`main.go`)

Before parsing any source file, bosc reads the `-importcfg=<file>` flag to obtain a mapping from import keys to `.bo` paths. When source declares `import "X"`, bosc looks up `X` in this map, loads the `.bo`, reads its embedded `pkgname`, and registers that .bo's functions under that pkgname in the current Context.

A `-listimports` mode prints just the import keys declared by the input files and exits. Used by the build system to discover transitive dependencies before compiling.

### Code Generator (`compile.go`)

Walks the AST and emits `.bs` assembly text. Key responsibilities:

- **Locals**: Each `var`/`const` declaration emits a `local` (for scalars and pointers) or `bytes` (for structs and arrays) directive in the assembly. The choice is driven by `typeIsMemoryBacked(t)` — true for structs and values larger than 8 bytes, false for everything else (including pointers regardless of pointee size). The assembler's register allocator handles actual placement.
- **File-scope vars**: Top-level `var`/`const` declarations go through `emitGlobalVarDecl` rather than the local path. Initializers are encoded at compile time (see [Static Initializers](#static-initializers)). The bas-level emission is the `var name type N` size form (zero-init), `var name type "..."` string-literal form (no relocations), or the block form `var name type { bytes "..." reloc <off> <sym> <addend> ... }` when any pointer fields require fixups.
- **Spots and addressing**: The compiler's intermediate `spot{ref, t, nameIsAddress}` records both the bas-level name and whether that name resolves to a value (`local`-allocated scalar) or to a memory address (`bytes`-allocated chunk, or file-scope global). Indirection sites (`*p`, `arr[i]`, `slice[lo:hi]`) consult `spot.nameIsAddress` to decide whether they need to materialize the address into a register before further indirection. This abstracts away the distinction between register-resident and memory-resident sources — the same codegen path handles both.
- **Control flow**: `if`/`else` and `for` lower to compare-and-jump sequences with generated labels (`_LABEL_for_N`, `_LABEL_break_N`, `_LABEL_cont_N`, `_LABEL_return_N`).
- **Function calls**: Arguments are evaluated into temporaries, then moved into the ABI argument registers before `call`. The emitted `call` is always fully qualified (`pkg.fname`); for in-package calls the current package's name is prepended. Function-defined-in-source signatures are emitted as `type fn(arg-types) ret` directives so cross-package import works for Boson-defined packages, not just hand-written bas runtime packages.
- **Address-of**: For `&x` where `x` is a named variable, the assembler `volatile x` directive is emitted (suppressed for memory-backed names where it's already true) to mark `x` as memory-resident, then `lea`. For `&arr[i]` and `&s.field` at runtime, the compiler delegates to the lvalue-walk machinery which already produces an address-bearing spot. For `&literal` at runtime, an error fires; the literal form is only valid in static-init.
- **Struct access**: Field offsets are computed at compile time. Pointer-to-struct access auto-dereferences. For a small inner struct accessed via dot (e.g. `outer.middle.field`), the inner Dot returns a pointer to the struct rather than copying its bytes, so subsequent Dot/Index walks land on the right address.
- **Slices**: A slice is two words (pointer and length). Indexing bounds-checks against the length word and computes `base + index * element_size`. Slice operations adjust both pointer and length.
- **Bounds checking**: Array and slice accesses emit a compare and a conditional call to `_init.index_oob` if the index is out of range.
- **Casts**: Type casts (`FD(x)`, `i64(fd)`) generate sign-extend, zero-extend, or truncating moves as appropriate. Widening 32→64 unsigned uses the synthetic `MOVZX r64, r/m32` form (translated by bas); widening 32→64 signed uses `MOVSXD`; narrowing uses partial-of-alloc syntax (`mov dest src:N`).
- **Multiplication / division**: 64-bit signed multiplication uses the two-operand `IMUL r64, r/m64` form, avoiding any `inreg` on user variables. Sub-64-bit signed multiplication and all unsigned multiplication/division route through fresh rax-pinned temps so the `inreg` constraints fall on temporaries, not on user-declared variables (which may be `volatile`).
- **Temporaries**: The compiler allocates temporaries as locals with names like `Temp_1`, `Temp_2`. The assembler's register allocator places these in registers or spills them to memory.
- **Register-scaled indirection through globals**: x86-64 RIP-relative addressing has no `[symbol + reg*scale]` form. When `arr[i]` indexes a name-is-address base with a runtime index, the compiler emits `lea tmp symbol` first to materialize the address into a register, then uses a normal `[reg + idx*scale]` SIB form off the temp.
- **Diagnostics**: Positioned errors include a source-context snippet — five lines centered on the offending position, with an arrow pointing at the column. The arrow is rendered in red ANSI when stderr is a TTY (plain otherwise so captured output stays clean).

### Ownership tracking

The compiler tracks per-binding move state in `Context.movedBindings`. Each `Funcall` whose parameter type contains `owned` marks the argument variable as moved. References to moved variables are compile errors.

Pointer alias state is kept separately in `cmd/bosc/flow`. `Context` owns a flow state and snapshots it with other control-flow facts. The flow model records pointer origins (which local object a pointer aliases), pointer-slot targets (`&alias`), unknown pointers, branch merges, and invalidation results. `compile.go` converts AST expressions into flow operations such as pointer assignment, store-through-slot, direct mutable write, and opaque `*mut` call; owned-field facts remain in `Context` and are invalidated from the flow result.

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

Every file begins with a `package` declaration. Code is grouped into `function` blocks. `data` and `var` declarations both define global blocks; the distinction is metadata-level (intended-immutable vs writable). Both currently land in the same `.data` LOAD segment in the linked ELF — `data` blocks are not yet placed in a read-only segment, though that's a planned hardening pass. The package name is the source of truth for symbol qualification: defined symbols are emitted as `<pkgname>.<name>`, and bare-name relocations in this file are qualified with the package name automatically by the assembler.

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
| `data` | `data name type "..."` | Global immutable data (e.g., string constants emitted by bosc). Stored in `o.Data`. |
| `var` | `var name type "..."` | Global writable data (string-literal payload form). Stored in `o.Vars`. |
| `var` (size) | `var name type N` | Global writable data, N zero-filled bytes (uninitialized). |
| `var` (block) | `var name type { bytes "..." reloc <off> <sym> <addend> ... }` | Multi-line form: explicit bytes payload plus zero or more per-var data relocations. Used by bosc to emit globals containing pointers (slice headers, struct fields holding addresses, anonymous-globals-as-pointers). |
| `struct` | `struct Name { fname ftype \n ... }` | Multi-line declaration carrying a Boson struct shape into the `.bo`. Field types are stored verbatim; bosc reparses them on import. Used for cross-package struct types. |
| `typealias` | `typealias Name underlying [m1 m2 ...]` | Single-line declaration carrying a Boson type alias into the `.bo`. The method-name list lets bosc reconstruct the type's method table on import from the already-imported function set. |
| `interface` | `interface Name { method m1 { param p t \n ... \n return rt } ... }` | Multi-line declaration carrying a Boson interface shape into the `.bo`. Each method's params and return type are reparsed by bosc on import. Used for cross-package interface types. |
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
| Indirect named + offset | `[name+N]` | `mov rax [buf+8]` |
| Sized indirect | `qword[...]`, `dword[...]`, `word[...]`, `byte[...]` | `mov qword[rsp+16] 1` |
| Partial of alloc | `name:N` (N ∈ {8,16,32,64}) | `mov dest src:32` |

`name` resolves differently depending on whether the name is a `local`-allocated (register-resident) local, a `bytes`-allocated stack chunk, or a file-scope `var`/`data` global:

- **`local`-allocated**: `name` is the value in the register; `[name]` is a register-relative dereference (`[reg]`). The name *is* the value.
- **`bytes`-allocated**: `name` is the base of the stack chunk; `[name+N]` is stack-relative (`[rbp+slotoff+N]`).
- **File-scope global**: `name` resolves to the symbol; `[name+N]` is RIP-relative (`[rip+disp32]`) with `N` baked into the relocation's `Addend`.

The combined effect: `mov reg [name]` reads the storage at `name` for memory-backed forms (`bytes`, `var`, `data`), and dereferences the register for `local`. Symmetrically, `mov [name] reg` stores into the storage / the register. Compiler-side codegen consults `spot.nameIsAddress` (see [Code Generator](#code-generator-compilego)) to decide whether further indirection requires materializing the address into a register first.

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

Inside `data`/`var` string literals:

| Escape | Byte |
|--------|------|
| `\n` | LF (0x0a) |
| `\r` | CR (0x0d) |
| `\t` | TAB (0x09) |
| `\0` | NUL (0x00) |
| `\\` | Backslash |
| `\"` | Quote |
| `\xHH` | Arbitrary byte, exactly two hex digits |

The `\xHH` escape exists so compiler-emitted global vars can carry binary payloads (encoded integers, struct field bytes, slice headers) through the existing string-literal syntax without needing a separate byte-literal directive.

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
- Per-`Var` data relocations (`DataReloc{Offset, Symbol, Addend}`) so static globals can hold pointers to other globals or to anonymous data
- Boson struct shapes (`Structs map[string]*StructShape`) — each carries the struct's field names paired with rendered type strings, allowing cross-package struct types to flow from producer to consumer .bo

---

## The Linker (bld)

`cmd/bld/main.go` is a thin wrapper over `linker.go`. It accepts a list of `.bo` object files and an output path, invokes the linker, and writes the ELF64 binary. The output file is set executable.

The linker requires each input `.bo` to declare a non-empty `Pkgname` and rejects duplicates. It registers all defined functions, vars, and data under their qualified names (`pkg.name`). Since the compiler and assembler emit all relocations qualified, the linker has a simple symbol table — no bare-name fallback. Bare-name targets inside `DataReloc` entries (from hand-written `.bs`) are auto-qualified at link time against the owning .bo's package, the same way function-body `Relocation` symbols are.

**Reachability** is computed transitively. Starting from the entry point, function relocations pull in their targets; placing a var (or data block) in the data section then walks that var's `Relocs` and recursively places every targeted symbol. This means a var referenced only by another var's pointer field still gets emitted into the final ELF; no need for the code section to mention it directly.

After all sections are positioned and section base addresses are known, the linker walks each placed var's `Relocs` and writes the absolute virtual address `targetVA + Addend` into the 8-byte pointer slot at `Offset`. Code-section relocations remain PC-relative 32-bit (`Relocation.Apply`) — distinct math from `DataReloc.Apply`'s 64-bit absolute writes.

The ELF entry point is fixed: the linker looks for `_init.start`. The `_init` package (provided by the runtime's `init_linux.bs`) must define a `start` function that calls `main.main` (passing argv as `byte[][]` in rdi) and exits with main's return value.

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

Custom binary object file format (`.bo`). Stores encoded instructions, symbol names and offsets, relocation entries, type descriptors, the package name, per-var data relocations (`DataReloc{Offset, Symbol, Addend}`), Boson struct shapes (`StructShape{Name, Fields}` where each field carries a name and a rendered type string), Boson type-alias shapes (`TypeAliasShape{Name, Base, Methods}`), and Boson interface shapes (`InterfaceShape{Name, Methods}` where each method is `{Name, Params, Return}` with params/return as rendered type strings). `bwrite.go` provides serialization/deserialization. `ReadOFile` populates `Function.Pkgname` from the .bo's package field so the linker can use it.

### `linker.go`

Combines multiple `.bo` files into a single ELF64 executable. Concatenates text sections, merges symbol tables under fully-qualified names, resolves relocations by computing final virtual addresses, and writes the output binary.

---

## Object File Format (.bo)

The custom `.bo` format stores:

| Section | Contents |
|---------|----------|
| Header | Magic bytes, version, section count |
| Pkgname | Package identity (a single string) |
| Text | Raw x86-64 encoded bytes per function |
| Symbols | Name → offset mappings for defined functions/globals |
| Code relocations | (offset, symbol, addend) triples for unresolved code references; all symbols are fully qualified; 32-bit PC-relative |
| Type info | Function signatures for type checking by importers |
| Data (`Data`) | Immutable global blocks (e.g. string constants). Each carries its bytes and an optional list of per-block `DataReloc` entries. |
| Vars (`Vars`) | Writable global blocks. Same shape as Data — bytes plus per-block relocations. |
| Structs | Boson struct shapes (name + ordered list of {field name, rendered type string}) for cross-package struct types. |
| Type aliases | Boson `type Name Base` shapes (name + base type + method-name list) for cross-package alias-with-methods types. |
| Interfaces | Boson interface shapes (name + ordered list of methods, each with ordered params and a rendered return-type string) for cross-package interface types. |

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
  4. At body time: builds the executable's own package into `target/_exe_<name>.work/`, then links the final executable to `target/<name>` along with all the `target/*.bo` deps plus init. The `_exe_` prefix on the workdir avoids a collision with `target/<name>.work/` from a same-named package.

All build artifacts — the executable, package `.bo`s, and per-package work directories holding intermediate `.bs` files — live under `target/`. Nothing is written to the project root.

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
| `_init`    | `init_linux.bs` | Process entry (`start`) calling `main.main`. Bounds-check trap (`index_oob`). |
| `_heap`    | `heap_linux.bs` | mmap-backed allocation and deallocation for `alloc`, `new`, and `free`. |
| `_io_sys`  | `io_sys_linux.bs` | Raw Linux file-IO syscall wrappers (`read`, `write`, `open`, `close`) taking i64 fds. |
| `string`   | `string.bs`, `puts_linux.bs` | String formatting and stdout primitives: `puts`, `puti`, `putb`, `putc`, `exit`, plus internal `strlen`, `itoa`, `uitoa`. File IO is no longer here; see `_io_sys` and `io`. |
| `io`       | `io.bos` | Typed file IO: `type FD i64` with `read`/`write`/`close` methods; `fn open(byte[], i64, i64) owned FD, i64`; `STDIN`/`STDOUT`/`STDERR` globals; `reader`/`writer` interfaces; `fn copy(writer, reader) i64`. Wraps `_io_sys`. |
| `pair`     | `pair.bos` | Minimal cross-package struct used only by tests. |
| `builtin`  | `builtin.bos` | Auto-imported into every package. Currently holds the `error` interface and acts as the home for documentation of compiler intrinsics. |

These files encode Linux-specific behavior directly. There is a `macho/` directory in the core library suggesting macOS support was planned but not yet implemented.

---

## Build and Test

The Go side (encoder, decoder, parser unit tests) and the integration test suites run via `make`:

```
make test       # Run all test suites
make go_test    # Go unit tests
make bas_test   # Assembler integration tests (38 tests)
make bosc_test  # Compiler integration tests
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
- Anonymous struct types (`struct { quot i64, rem i64 }`) as parameter, return, and var types
- Bare struct literals (`{ field: val }`) with shape inferred from the surrounding context (return, typed initializer, assignment lvalue, argument position)
- `var x = expr` / `const x = expr` type inference from the initializer (`<intlit>` resolves to `i64`; void/nil sources rejected)
- Nullable pointers (`*?T`), non-null pointers (`*T`), boolean narrowing for nullable pointers, and postfix `?` non-null assertions
- Nested slices/arrays (`byte[][]`, `T[N][M]`, etc.)
- `const`/`var` bindings with declaration initializers (`const x i64 = 42`)
- File-scope `var`/`const` with compile-time-constant initializers — integer/byte literals, string-into-`byte[N]`, string-into-`byte[]` (slice with relocated pointer), struct literals composing all of the above, array literals (`[1, 2, 3]`) for fixed-array and slice destinations, `&someGlobal`, `&SomeStruct{...}` (anonymous globals), `&globalArr[N]` (pointer-into-array via relocation addend)
- Array literals at runtime as initializers for fixed-array locals; slice destinations rejected with a directed error (lifetime issue)
- Full nested `*mut T` write-through mutability with implicit coercion
- Full `owned T` ownership type system: move semantics, `dispose()`, `owned()` promotion, owned struct fields, owned-field move tracking, if/else branch analysis, loop backedge/exit checks, and scope-exit checks
- Re-initialization of a moved `var owned` binding via assignment (including struct-literal RHS), gated by a pointer-flow check that rejects when a live owned alias still references the binding's storage
- Address-of of an owned binding (`&x`) preserves owned bits; whether the result borrows or moves is decided by the destination type (`*T` borrows, `*owned T` moves; `*mut`-shaped views of the owner slot remain rejected)
- Value-alias tracking for owned scalars: declaring `var fd owned i64 = ...` registers a flow-state Origin; coercing to a non-owned destination (`var t i64 = fd`, `thingy(fd)`, etc.) records the destination as an alias of that Origin; `c.Move` on the source invalidates all such aliases, and reading the destination after the move is rejected at the Symbol-use site with the same diagnostic as a borrowed-pointer use-after-move
- Field-level pointer provenance: per-field pointer facts in flow state catch self-referential struct escapes at return, dispose/free invalidating field-pointer aliases, and out-of-scope local-address extraction through a struct field
- Interfaces: structural satisfaction at coercion sites, fat-pointer representation (data + vtable, 16 bytes), automatic vtable emission per `(T, I)` pair, indirect dispatch through the vtable, and `owned I` / `I` interface qualifiers
- Type-block methods: `type T <base> { ... }` declarations carrying instance methods (pointer receiver) and static methods (no receiver), called as `v.method(args)` / `T.method(args)` respectively; `struct { ... }` is a valid base (fields in the struct body, methods in the type block)
- Non-escaping borrowed pointer parameters by default, including local alias provenance and escape rejection for returns, globals, fields, and struct literals
- Compiler built-ins `alloc(T)`, `new(expr)`, and `free(p)` backed by the `_heap` runtime package; `alloc(T)` and `new(expr)` return owned mutable pointers
- `for` lowering to block + loop control flow; `continue` runs the lowered step expression
- Path-based imports with qualified calls (`import "stdlib/io"; io.puts(...)`)
- Cross-package struct types (`pair.pair`, `pair.pair{...}` literals) with auto-qualification of leaf type names at import time
- Bosc-emitted function signatures (`type fn(...) ret`) so cross-package function calls work with Boson-source packages as well as hand-written bas runtime packages
- `argv` passed to `main(args byte[][])` by the `_init.start` entry stub
- `&arr[i]`, `&s.field` at runtime; `&literal` rejected at runtime with a directed error
- Register-scaled indexing into globals via `lea` materialization (no `[symbol + reg*scale]` SIB form exists on x86-64; bosc emits an `lea` to materialize the base, then `[reg + idx*scale]`)
- Postfix chains compose: `a.b[i].c[lo:hi]` parses and codegens uniformly
- Go-style automatic semicolon insertion (newline after a statement-ending token; suppressed inside `(...)` and `[...]`)
- Positioned compiler diagnostics with source-context snippets (5 lines + colored caret on TTY); the lexer records each token's start position, and `ASTType` carries that position into "no such type" and related shape diagnostics
- mmk-driven build with auto-discovery via `bosc -listimports`; build failures halt the chain cleanly (set -e in defbody scripts, proper exit-code propagation through `pkg_import_targets`)
- bas synthetic instructions: `volatile`, `name:N` partials, size-qualified indirects, `movzx r64, r/m32`
- bas struct directive (carries Boson struct shape into .bo); bas var block form with embedded `reloc` lines for data-section relocations; bas string escapes include `\xHH`, `\r`, `\t`
- bas `typealias` and `interface` directives for cross-package export of type aliases (with method tables) and interfaces
- Cross-package variable read access: `pkg.name` qualified reads against a foreign-package `var`/`const`
- Built-in `len(s)` intrinsic for slice (runtime length load) and fixed-array (compile-time constant) operands
- `builtin` package auto-imported into every compilation; currently provides the `error` interface
- `owned error` ownership semantics: consuming-receiver methods (any owned bit on the receiver) require an owned interface, mark the interface variable consumed after the call, and forbid being called on a non-owned interface; return-statement checks reject silently dropping ownership (`valType.HasOwned() && !retType.HasOwned()`) and validate general return-type compatibility via `ResolveUnderlying + Accepts` with a `sameIgnoringOwned` escape so owned↔non-owned variants of the same underlying type still pass
- Multi-value return (`fn f() i64, i64`) and destructuring bind (`var a, b = f()`), including mixed `var`/`const` per-bind qualifiers, per-bind type annotations, and re-binding when a name already exists in scope
- Integer literal bases: decimal, hexadecimal (`0xFF`), octal (`0o755`), and binary (`0b1010`)
- Bitwise `&` and `|` operators on integer operands
- Bare `return` (no value) in `void` functions or as an early exit
- `(expr).method(...)` dispatch for arbitrary concrete-type expression receivers (interface-typed receivers still require binding first)
- Boson source string and character literal escapes: `\n`, `\r`, `\t`, `\0`, `\\`, `\"`, `\'`, `\xHH`
- Type-alias casts as file-scope static initializers (`var fd FD = FD(3)`, qualified form `io.FD(3)`)
- Position tracking through a leading file-level doc comment preamble (errors point at the correct source line even when the file begins with comments)

**Known limitations / future direction:**

- Heap allocator is intentionally minimal: one mmap mapping per allocation, with no reuse or pooling.
- No floats. Lexer accepts decimal-point syntax but truncates.
- Generics / type polymorphism not implemented.
- Stacked `owned *owned T` cannot have partial consumption (needs typestate).
- No witnessed borrows or explicit escaping-reference mechanism.
- True read-only `.rodata` segment split not yet done. String constants and other immutable data are tagged at the `o.Data` level but currently land in the writable `.data` LOAD segment. Hardware-enforced const requires splitting the ELF layout.
- No deduplication of structurally-identical anonymous globals. Each `&literal` produces a fresh `__static_N`.
- macOS support stubbed but not implemented.
- Unused-mutability warning not implemented.

---

## Design Observations

- **Layered debuggability.** Each pipeline stage produces an inspectable artifact. A compiler bug can be isolated to the `.bs` output; an assembler bug to the `.bo` binary; a linker bug to the final ELF.
- **Assembler as IR.** The `.bs` language occupies an unusual middle ground: it has named variables and a register allocator, making it usable as a compiler IR, while still being human-readable assembly.
- **Minimal runtime.** No GC and no dynamic loading. Programs are fully static ELF64 binaries that make raw Linux syscalls; the heap runtime is a small mmap/munmap allocator.
- **Single-pass compiler.** The compiler does not perform optimization. It lowers each AST node directly to assembly, relying on the register allocator to minimize unnecessary spills.
- **Conservative-by-default ownership and mutability.** Defaults favor strictness (const, read-only, no implicit promotion) with explicit opt-in for looser forms (var, mut, owned()). The type system encodes obligations the compiler can check; programmer-asserted unsafe operations are explicit (`owned(...)`, `dispose(...)`).
- **Name-is-address as a single distinction.** `local` allocations are register-resident — the name *is* the value. `bytes`/`var`/`data` allocations are memory-resident — the name *is* the address. Bosc tracks this with a single bool per `spot` (`nameIsAddress`), populated at allocation/declaration time. Every site that needs to follow a pointer through such storage consults the same flag and emits the same lea-then-deref or load-first pattern. The distinction isn't between local and global scope (a `bytes`-allocated local and a `var` global behave the same way); it's about register vs memory residency.
- **Data relocations as a uniform mechanism.** A `.bo` file's globals can carry per-block relocations the linker resolves at link time, producing absolute pointer slots. Any compile-time-constant expression — primitive literal, struct literal, `&someGlobal`, `&literal` — collapses to a `(bytes, relocations)` pair. Slice headers, struct fields holding pointers, and anonymous globals all fall out of that one mechanism without per-feature code paths.
