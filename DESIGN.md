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
| `uintptr` | Unsigned, pointer-width integer (8 bytes). Any pointer converts to it with an ordinary cast; the reverse requires `unsafe_cast` |
| `<intlit>` | Compile-time-only type for integer literals; resolved to a concrete integer type based on context |

`uintptr` is an ordinary unsigned integer in every respect — the full set of
arithmetic, bitwise, shift, and comparison operators applies, with the same
defined two's-complement wrap as every other Boson integer. What makes it
useful is what it *cannot* do: a `uintptr` cannot be dereferenced. Converting a
pointer to one therefore discards pointer-ness safely, and address arithmetic
on the result can produce a nonsensical value but never an unsound operation.
That is why `T(expr)` accepts pointer → `uintptr` while the reverse direction —
manufacturing a pointer from an arbitrary integer — is reserved to
`unsafe_cast`. Boson has no GC, so `uintptr` carries none of the
object-liveness hazard the equivalent type has in a garbage-collected language;
it really is just an integer.

**Floating-point types:**

| Type | Description |
|------|-------------|
| `f32` | IEEE-754 binary32 (single precision) |
| `f64` | IEEE-754 binary64 (double precision) |
| `<floatlit>` | Compile-time-only type for floating-point literals; resolved from context |

A literal with a decimal point or an exponent is a floating-point literal:
`3.14`, `0.5`, `1e10`, `2.5e-3`. A digit is required on both sides of the
decimal point (`0.5`, not `.5`), and an exponent must have at least one digit.
An untyped float literal that is never given a context defaults to `f64` — the
width that loses nothing, since every `f32` value is exactly representable in
it.

`<floatlit>` differs from `<intlit>` in what accepts it. An integer literal is
meaningful as a float, so `<intlit>` coerces into any numeric destination and
`x f64 := 3` is 3.0. A float literal has no meaning in an integer slot, so
`<floatlit>` coerces only into `f32` or `f64`: `z i64 := 2.5` is an error, not a
silent truncation. An explicit cast still converts — see
[Expressions](#expressions).

`<intlit>` is the type of an integer literal `42` before it is assigned somewhere. Constant expressions over `<intlit>` (e.g. `4 * 1024`) are evaluated at compile time using arbitrary-precision arithmetic and range-checked against the destination type at the point of assignment.

Integer literals can be written in four bases:

| Form | Example | Description |
|------|---------|-------------|
| Decimal | `42`, `1024` | Default base |
| Hexadecimal | `0xFF`, `0xDEADBEEF` | `0x` prefix, hex digits (case-insensitive) |
| Octal | `0o755`, `0o644` | `0o` prefix |
| Binary | `0b1010`, `0b11000011` | `0b` prefix |

Character literals (`'A'`, `'\n'`) produce a `byte`. See [Strings](#strings) for the escape-sequence table that both character literals and string literals accept.

Boolean literals are `true` and `false` (reserved words), each of type `bool`. A `bool` can otherwise be produced by a comparison (`<`, `==`, `!=`, …) or logical not (`!`).

**Reference and composite types:**

| Type | Description |
|------|-------------|
| `*T` | Non-null pointer to T |
| `?*T` | Nullable pointer to T |
| `[]T` | Slice header `{ptr@0, len@8, cap@16}`, 24 bytes. (`cap` is carried through construction/reslice but not yet consumed by any operation — `make`/`append`/`pop` are future work; see [`docs/reference/AUDIT_slice_representation.md`](docs/reference/AUDIT_slice_representation.md).) |
| `[N]T` | Fixed-size array |
| `type Name struct { ... }` | Named record type (file scope) |
| `struct { ... }` | Anonymous struct type (usable as a parameter, return, or var type) |
| `fn(args) ret` | Function-pointer type (pointer-sized) |

Named struct types are declared with `type Name struct { ... }` (see [Type aliases with methods](#type-aliases-with-methods)); the struct body is pure data — no methods inside. Anonymous struct types appear in parameter, return, and var-declaration positions. Two anonymous struct types are compatible when their field lists agree on both name and type, field-by-field. They are otherwise identical to named structs in layout and semantics.

```
fn divide(a i64, b i64) struct { quot i64, rem i64 } {
    var q i64 := a / b
    return { quot: q, rem: a - q * b }
}

fn use_result(r struct { quot i64, rem i64 }) i64 { return r.quot + r.rem }
```

Types are divided into **direct** (fit in a single register: scalars, pointers) and **indirect** (too large for a register: structs, arrays, slices — held as pointers in registers, with their data on the stack or in memory).

String literals have type `[]byte` (an immutable slice of bytes). The data is stored in the data section (in `o.Data` rather than `o.Vars`, distinguishing immutable string constants from writable globals at the metadata level) and the slice header has the literal's length. A future hardening pass can split `.data` and `.rodata` into separate LOAD segments so this immutability is hardware-enforced; today the distinction is only at the source level.

### Type qualifiers

Three orthogonal qualifiers can apply to types:

- **`mut`** — write-through mutability for pointers and slices. `*mut T` lets you write to T through the pointer; `*T` does not. Nests independently at each pointer level (`*mut *T`, `**mut T`).
- **`?` on pointers** — nullability marker. `*T` is non-null; `?*T` may be `nil`. Non-null pointers can be used where nullable pointers are expected. Nullable pointers require a proof or assertion before non-null use: `if (p) { ... }` narrows `p` to non-null inside the true branch, `if (!p) { ... } else { ... }` narrows it in the else branch, and postfix `p?` inserts a runtime nil check and produces a non-null pointer.
- **`?` on interfaces** — same marker. `T` (interface) is always valid; `?T` may be null (vtable = 0). A non-nullable interface can't be constructed null. A nullable interface can be initialized or returned as `nil`; a fallible cast (see [type assertion](#interface-to-interface-assertion)) also yields a nullable interface that is nil on failure. A `?T` must be narrowed before dispatch — `if (k != nil) { k.method() }` narrows `?T → T` in the true branch (a vtable-`!= 0` test) — and dispatching, or asserting/widening from, an un-narrowed `?T` is rejected, exactly like dereferencing a `?*` pointer. Interfaces also support truth testing in `if`, `for`, `!`, `&&`, and `||`: a zero vtable is false and a nonzero vtable is true, independently of the data word. `if (err)` narrows a nullable interface to non-null in the true branch; `if (!err)` narrows it in the else branch, just as for pointers. This does not implicitly convert an interface into a `bool` assignment or argument.
- **`owned`** — compile-time ownership obligation that must be discharged before the variable goes out of scope. See [Ownership](#ownership).
- **`:=` / `var`** — binding mutability: a binding is introduced with `:=` and is immutable; `var` makes it reassignable. Distinct from `mut`. Immutable is the default everywhere — bare `:=` locals and globals, and unmarked parameters; `var` is the opt-in for reassignment.

### Type syntax: prefix reading order

Type modifiers are **prefix** and read left-to-right, each binding to the type on its right (Go/Rust order). The pointer/slice ambiguity that C-style suffix syntax needs parentheses to resolve simply does not arise:

- `[]*byte` — a **slice of** `*byte`. The `[]` is outermost; the `*` binds to the element `byte`.
- `*[]byte` — a **pointer to** a `[]byte` slice. The `*` is outermost. This is the type of `&s` where `s []byte`, and of a function-scope `&"literal"`, so `%s`-style code can name and dereference (`*p`) the pointer-to-slice a `...any` string argument produces.

Slices themselves are not nullable: `?[]byte` is rejected. A zero-initialized `[]byte` is a valid empty slice. Use `?*[]byte` for a nullable pointer to a slice, or `[]?*byte` for a slice of nullable pointers.

`mut`, `owned`, and `?` bind the same way: `[]mut byte` is a slice of mutable bytes, `?*byte` a nullable pointer, `*mut []byte` a pointer to a mutable slice, `[N]?*T` an array of nullable pointers. Arrays reverse when nested: `[1][8]byte` is one row of eight bytes. `ASTType.String()` emits exactly this canonical prefix form and `parseTypeName` is its inverse — the two round-trip byte-for-byte (`cmd/bosc/type_encoding_test.go`).

### Type aliases

```
type FD i64
type Tag i16
```

`type Name Underlying` defines a **distinct** named type. The underlying representation is identical (same size, same arithmetic), but the type system treats them as incompatible. A function that takes `FD` will not accept a bare `i64`. This is the nominal-type approach (like Go's `type Foo int`), not transparent aliasing.

Casts use the type name as a function:

```
var fd FD := FD(3)         // i64 literal → FD
var n  i64 := i64(fd)      // FD → i64 (same bits)
var t  Tag := Tag(n)       // i64 → Tag (truncating)
```

Integer literals coerce naturally. Widening (signed source → larger type) uses sign-extension; widening (unsigned source → larger type) uses zero-extension; same-size cast is a reinterpretation; narrowing truncates.

**A cast across the integer/float boundary converts the value, not the bits.** Between two integer types a same-size cast reinterprets, but an integer and a float of the same width hold entirely different encodings, so `f64(n)` produces the floating-point value of `n` and `i64(x)` produces the integer value of `x`, **truncated toward zero** — the same rule integer division follows, on both signs. `f32`→`f64` is exact; `f64`→`f32` rounds. A constant is converted the same way as a runtime value, so `i64(3.9)` and `i64(x)` with `x = 3.9` both give 3. Casting a NaN or an infinity to an integer is a compile error when the value is a constant, since neither has an integer value.

A second `type Name <kind> { ... }` form, `type Name values { ... }`, declares a closed symbolic value type whose user-visible cases are scoped names rather than integer literals. See [Values Types](#values-types).

### Expressions

Arithmetic: `+`, `-`, `*`, `/`, `%`. Multiplication, division, and modulo dispatch to signed (`imul`/`idiv`) or unsigned (`mul`/`div`) instructions based on the operand types; `%` is the division remainder (from `rdx`), truncated toward zero so its sign follows the dividend. `+`, `-`, `*` and `/` accept **any numeric** operand — integer or floating-point — while `%`, the bitwise operators and the shifts are **integer-only**: they have no meaning on an IEEE value, so `f % 2` and `f & 1` are rejected, as is `~f`. `bool` is not numeric, so `true + true` is rejected too.

Boson does not convert between number types implicitly. The two operands of an arithmetic operator must be the same type, and mixing an integer with a float requires an explicit cast in one direction or the other. A literal is the exception: it adopts the other operand's type, so `x * 2` works whether `x` is an `i64` or an `f64`. When both operands are literals the expression stays pending and the destination decides, with a float literal dominating an integer one — `1.5 + 2` is a float expression.

**Integer overflow wraps.** Runtime integer arithmetic that overflows its type wraps modulo 2ⁿ (two's-complement), for both signed and unsigned types. Overflow is **fully defined** — never a trap, never undefined behavior — matching Go; a program may rely on the wrapped result. This is a deliberate language decision, not merely inherited hardware behavior: wrapping is the defined, portable, zero-cost default (every target ISA is two's-complement and wraps at the instruction level, and no per-operation overflow check is emitted, so tight numeric loops stay vectorizable). It is distinct from **compile-time** constant arithmetic over `<intlit>`, which is arbitrary-precision and range-checked at the point of assignment — a constant that does not fit its destination is a compile *error*, not a wrap. Silent wrap is nonetheless a real bug class where it is unintended (size calculations, untrusted input); the planned explicit opt-in is a `checked_*` intrinsic family (returning an optional/error on overflow), keeping ordinary arithmetic cheap while making the check available where a wrong wrap would matter.

**Floating-point evaluation precision: the declared type is the type it computes in.** An `f32` expression is evaluated in `f32` — intermediates are never silently widened to `f64` and narrowed back — and the rule holds at compile time as well, so constant folding is unobservable: an expression means the same thing whether its operands are literals or variables. The type is a promise about precision, not a storage hint; widening intermediates would make results depend on where the compiler chose to round, which is the x87 failure mode that SSE exists to have fixed. Widening is explicit and exact: `f64(a) * f64(b)` costs one conversion per operand and is visible in the source.

The difference is not subtle. With all-`f32` `a = 1e8`, `b = 1.0`, `c = -1e8`, the expression `a + b + c` is `0.0` — `1.0` is below the ulp of `1e8` and vanishes — where `f64` intermediates would give `1.0`. Fused multiply-add is likewise unavailable, for the same reason: it computes `a*b+c` with a single rounding and would be another source of "same source, different answer".

Note this diverges deliberately from Go, whose untyped constants are arbitrary-precision: in Go, `0.1 + 0.2` folded as constants differs from the same sum computed from variables. Boson trades that exactness for the invariant that folding is invisible.

Comparison: `==`, `!=`, `<`, `>`, `<=`, `>=`. Equality (`==`/`!=`) applies to scalars (value equality) and pointers (identity); ordering (`<`, `>`, `<=`, `>=`) applies to scalars only. Signed and unsigned comparisons select the appropriate `setl`/`setb`/etc. variant.

**Floating-point comparison follows IEEE-754 exactly, including its fourth outcome.** Besides less, equal and greater, two values can be *unordered*, which is what happens when either is a NaN. Every ordered comparison against a NaN is false, `x == x` is false for a NaN, and `x != x` is true — the standard NaN test. Negative zero compares equal to positive zero despite their differing bit patterns. `==` on floats is permitted: it is the correct operator for the many cases where the comparand is exactly representable (`x == 0.0`), and no mainstream language bans it. Where an approximate comparison is wanted, it belongs in a library function with an explicit tolerance, not in the operator. **Aggregates — structs, arrays, and slices — are not comparable with `==`/`!=`**; the compiler rejects it and directs you to write an explicit equality function. A memberwise `==` would silently change meaning as a struct's fields evolve — adding a pointer field turns value-equality into pointer-identity — and the change is invisible at the use site, so equality of an aggregate must be an explicit author decision, not an operator default.

Logical: `&&` (and), `||` (or). **Short-circuit**: `a && b` evaluates `b` only when `a` is true; `a || b` evaluates `b` only when `a` is false. The result is a `bool`. `&&` binds tighter than `||`, so `a || b && c` is `a || (b && c)`.

**Precedence**, loosest to tightest:

| Level | Operators |
|-------|-----------|
| 1 | `\|\|` |
| 2 | `&&` |
| 3 | `==` `!=` `<` `>` `<=` `>=` |
| 4 | `\|` |
| 5 | `^` |
| 6 | `&` |
| 7 | `+` `-` |
| 8 | `*` `/` `%` `<<` `>>` |
| 9 | unary `!` `~` `-` `*` `&` |

**The bitwise operators bind tighter than comparison**, so `x & MASK == 0` is
`(x & MASK) == 0` — the idiom means what it looks like. This differs from C,
which places `&` `^` `|` *outside* comparison; that ordering is a
widely-acknowledged mistake in C's design and Go corrects it too.

**Shifts sit at the multiplicative level**, so `1 << n + 1` is `(1 << n) + 1`.
C and Python place shifts *below* `+`/`-`, which makes that expression
`1 << (n + 1)` — and since `1 << k` is overwhelmingly "power of two", the
result is silently double what was meant. Go moved shifts up for this reason
and Boson follows.

Boson stops short of Go's remaining difference, which folds `|`/`^` into the
`+` level and `&` into the `*` level. Under Boson `a & b + c` is `a & (b + c)`;
under Go it would be `(a & b) + c`. Keeping the bitwise operators looser than
arithmetic preserves the useful intuition that they are the outermost thing in
an expression, and Go's unification buys only a shorter table.

The two departures from C differ in cost, which is worth recording. Lifting the
bitwise operators past comparison could not change any working program: bitwise
operators reject `bool` operands, so every expression it affects was previously
a type error. Moving shifts past `+`/`-` *can* silently change meaning, since
both parses type-check — it was made deliberately and early, while the
consequences were a single test line.

Bitwise: `&` (AND), `^` (XOR), `|` (OR), `<<`/`>>` (left/right shift), and unary `~` (NOT). Operate on integer types of any width. For `&`/`^`/`|` the operand widths must match (integer literals coerce to the other operand's type); for shifts the count may be any integer width. A right shift on a signed operand is arithmetic (`sar`, sign-preserving); on an unsigned operand it is logical (`shr`, zero-filling). `~x` is the bitwise complement, equal to `-x - 1`.

Unary: `-` (negation), `~` (bitwise not), `!` (logical not), `*` (dereference), `&` (address-of).

Precedence, tightest to loosest (C-conventional): unary prefix > `.`/`[]` > `*` `/` `%` > `+` `-` > `<<` `>>` > comparisons (`< <= > >= == !=`) > `&` > `^` > `|` > `&&` > `||` > assignment. Note that, as in C, the bitwise operators are *looser* than comparison, so `flag & MASK == 0` parses as `flag & (MASK == 0)` — write `(flag & MASK) == 0`. (Because bitwise operands must be integers, the former is a type error rather than a silent miscalculation.)

Compound assignment: `+=`, `-=`, `*=`, `/=`, `%=`, `&=`, `|=`, `^=`, `<<=`, `>>=`. Each is sugar for `lhs = lhs op rhs` — `x += 1` is exactly `x = x + 1`, so the target must be a mutable (`var`) lvalue and the operand-type rules of the underlying operator apply. The lhs is re-evaluated (it appears on both sides of the desugaring).

Struct field access uses `.` for both direct structs and pointer-to-struct (`p.x` auto-dereferences when needed).

Postfix `?` asserts that a nullable pointer is non-null:

```
var p ?*node := ...
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
var x i64                 // mutable declaration without initializer
var x i64 := 42           // mutable declaration with initializer
var x := 42               // mutable, type inferred from initializer (i64)
y i64 := 100              // immutable binding, typed (must be initialized)
y := 100                  // immutable binding, inferred type
x = 10                    // assignment (':=' declares, '=' assigns)
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

**Declarations vs. assignment.** `:=` *introduces* a binding (immutable, or `var` for mutable); `=` *assigns* to one that already exists. A bare `name = expr` whose name is not in scope is an error, not an implicit declaration — so the operator records intent and a forgotten declaration is caught at the site.

**Expression statements must have an effect.** A statement-position expression is legal only if it is a function call. A bare value expression with no effect — `x * y`, `5 + 3`, or the common `x == 5` written where `x = 5` was meant — is rejected (`expression statement has no effect; only function calls are valid as statements`). Declarations, assignments, control flow, and `dispose`/`free` are statements in their own right; only stray *values* are rejected.

**Multi-value return and destructuring bind.** A function may return more than one value by listing the return types comma-separated:

```
fn divide(a i64, b i64) i64, i64 {
    var q i64 := a / b
    return q, a - q * b
}
```

The call site binds all returned values in a single declaration. Each bind position is independent — names may be inferred or typed, and `var` applies per target (an unmarked target is immutable):

```
q, rem := divide(17, 5)                        // both inferred i64, both immutable
var q2 i64, rem2 i64 := divide(100, 7)         // explicit types, q2 mutable, rem2 immutable
```

A name in the bind list may already exist in the enclosing scope — the existing binding is reused (re-assigned). This is how the `(value, err)` pattern composes across calls without forcing fresh names every time (the reused target must be `var`):

```
var n, var err := source.read(buf)
if (err != nil) { ... }
n, err = source.read(buf)             // reassign both existing variables
```

A single-name bind to a multi-return function is rejected — the count of binds must match the count of returns.

**Type inference.** When the type slot is omitted, the binding's type is inferred from the initializer. Untyped integer literals infer as `i64`; expressions whose type cannot be expressed as a value (a `void` call, the `nil` literal) are rejected with a directed error.

`[e1, e2, ...]` is an array literal. In a local inferred declaration, `x := [0, 1, 2, 3]` produces `[4]i64`; untyped floating-point elements default to `f64`. Element type inference follows the first element, recursively for nested arrays, and every element must be compatible with that type. An inferred first element of `nil` is rejected. Explicitly typed fixed-array initializers provide the element type instead. Nonempty array literals can initialize slices at file scope, where static backing storage is emitted; local slice destinations are rejected.

`[]` is an empty slice literal. It takes its element type, mutability, and ownership from a slice destination: `s []byte := []`, `s = []`, `return []` in a slice-returning function, slice arguments, and slice fields in struct literals. It emits a zeroed `{data, len, cap}` header without allocating backing storage. An owned empty slice can be freed normally. `x := []` is rejected because there is no element type to infer, and `[]` cannot initialize a scalar or fixed array. Projection casts such as `[]byte(value)` retain their existing meaning.

Nonempty array literals used as expressions have backing storage in the current function's frame. A view such as `['a', 'b', 'c'][:]` can be used locally or passed as a borrow, but cannot escape through a return or longer-lived storage. String literals, by contrast, have static backing storage.

```
var p := alloc(i64)            // owned *mut i64
var r := divide(17, 5)         // struct { quot i64, rem i64 }
var n := some_void_call()      // COMPILE ERROR: cannot infer from void expression
```

`for` loops are source-level sugar. The compiler lowers them to a block containing the initializer and an unconditional loop with an explicit conditional `break`, body, and step. `continue` inside a lowered `for` carries the step expression so loop ownership and nullability checks use the same control-flow machinery as other blocks and branches.

**Statement boundaries.** Newlines terminate statements, following the Go convention. The lexer synthesizes a `;` after the last token of a line when that token can validly end a statement (identifiers, literals, `)`, `]`, `}`, `break`, `continue`, `return`). Newlines inside `(...)` or `[...]` are suppressed so multi-line argument lists, slice expressions, and continuation lines (e.g., a binary operator at the end of a line) work without ceremony. The parser sees a token stream with explicit `;` separators; users don't type them outside `for`-loop headers.

One consequence worth knowing: `} else` style is enforced. A bare `else` on a new line after `}` doesn't parse, because `}` is a statement-ender and the auto-inserted `;` separates the two.

### Functions

```
fn add(x i64, y i64) i64 {
    return x + y
}

fn write_into(p *mut i64) {       // immutable parameter, but allows write-through
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

Pointer parameters without `owned` are borrows by default. The callee may read or write through the pointer according to the pointer's `mut` bits, may forward it to other non-owning calls, and may return it (or a view derived from it) — the compiler infers which parameters each return slot may alias and the caller's borrow tracking picks up the obligation. A borrow may not be stored somewhere that outlives the call. See [Borrowing](#borrowing).

Arguments follow [the Boson ABI](#the-boson-abi-babi-0): the first six integer/pointer arguments go in RDI, RSI, RDX, RCX, R8, R9, and the first eight floating-point arguments in XMM0–XMM7, with the two sequences counted independently. Integer/pointer arguments past the sixth are passed on the stack; floating-point arguments past the eighth are still a compile error. The single-value return goes in RAX, or XMM0 for a float.

Sub-64-bit return values are sign- or zero-extended into RAX.

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

Pass all Boson files for a package to one compiler invocation:

```sh
bosc -importcfg=importcfg -o package.bs first.bos second.bos
```

Every input must declare the same package. Top-level declarations are shared across the files, including private declarations, and all declarations are registered before function bodies are compiled. Duplicate declarations are errors. The compiler emits one assembly file with shared package metadata; `boson.mmk` uses this mode for both library packages and executables.

Imports and import aliases belong only to the file that declares them. An optional alias replaces the package's default source qualifier:

```
import stream "stdlib/io"
// This file refers to io's writer interface as stream.writer.
```

Different files may use the same alias for different packages or different aliases for the same package. Types in shared signatures retain their canonical package identity, and each function body resolves imports in its defining file. A sibling file may use an inferred imported value through a shared declaration, but naming that package explicitly requires its own import. Aliases do not affect emitted linker symbols.

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

var p pair.pair := pair.pair{a: 3, b: 4}   // type and literal use pkg.Name
string.puti(pair.sum(p))                  // call takes pair.pair, transparent
```

The `.bs` carries Boson struct shapes via the `struct Name { … }` directive (see [Directives Reference](#directives-reference)); the assembler stores them in the `.bo` and bosc reads them back during `Context.Import`. Built-in type names and any cross-package references inside imported signatures are left alone; only leaf type names that match the producer's own structs get qualified at import time.

**Cross-package interfaces.** Interface declarations export the same way structs and type aliases do. The producer's `.bs` carries each `interface Name { method ... }` declaration via the `interface` directive; the `.bo` stores it as an `InterfaceShape` (method name, ordered params with names and ASTType-rendered type strings, plus the return-type string). On import, bosc reparses the types, qualifies any leaf names that match the producer's structs or aliases, and registers the interface under the qualified name (e.g. `io.reader`). Caller code can then write `fn use(r io.reader) { ... }` and coerce a borrowed `*io.FD` into it; the vtable is emitted in the *consumer's* `.bo` and its method-pointer relocations target the type's owning package (`io.FD.read`).

**Cross-package type aliases.** `type Name Base` declarations export the same way structs do, with their attached method tables. On import, the type is registered under the qualified name (`io.FD`); the method set is reconstructed from the already-imported function set using a method-name list carried in the `typealias` directive. Qualified casts (`io.FD(3)`) work in expression contexts and in file-scope static initializers.

**Cross-package variable access.** File-scope variable declarations are exported from a `.bo` along with functions and struct types. Source code reads them through the qualified form `pkg.name`:

```
import "io"

fn main() {
    string.puts("hello\n")
    var f := io.STDIN          // read a cross-package var
}
```

Cross-package var access follows the usual mutability rules: mutable (`var`) declarations are readable and writable by importers; immutable declarations are read-only. Boson does not impose an extra access-control rule on cross-package vars beyond what the producer's mutability already says — protection against unwanted external mutation comes from whether the package exports the binding at all (a visibility system, when one exists) or from the producer exposing only function-mediated accessors. (Cross-package immutability enforcement currently relies on the consumer-side declaration: the `.bo` format does not yet carry the producer's `IsConst` flag, so writes to a producer-declared immutable global from an importer compile until that flag is plumbed through.)

Owned types are not allowed at file scope. A global never goes out of scope, so the end-of-scope move check that gives `owned` its teeth can never fire; a `dispose()` inside a function isn't visible elsewhere either. `var x owned …` and `x owned … :=` at the top level are rejected.

**The `builtin` package.** A single package, `builtin`, is auto-imported into every compilation unit (except `builtin` itself). It contains the `error` and `any` interfaces, the built-in scalar typedescs (`__typedesc_i64`, `__typedesc_byte`, …), and serves as the home for declarations that need to be visible without an explicit `import`. The `-listimports` driver always emits `builtin` first, so the build system pulls in `target/builtin.bo` automatically. Bare references to `error` and `any` resolve to `builtin.error` / `builtin.any` the same way an explicitly imported package's interface would, but without requiring `import "builtin"` at the top of every file.

### Built-in Functions

Built-ins are split into two groups: **compiler intrinsics** (lowered by bosc itself, not callable through a function pointer) and **runtime packages** (ordinary Boson packages that any program imports). The runtime packages are `string`, `io`, `fmt`, `sys`, `_sys`, `_init`, `_heap`, `_iface`, `pair`, and `builtin`.

**Compiler intrinsics:**

| Built-in | Type | Description |
|----------|------|-------------|
| `len(s)` | `([]T) i64` or `([N]T) i64` | Length of a slice (runtime load of the header length word) or fixed array (compile-time constant `N`). |
| `cap(s)` | `([]T) i64` or `([N]T) i64` | Capacity of a slice (header capacity word — space from the pointer to the end of the backing) or fixed array (its length). |
| `make([]T, len)` / `make([]T, len, cap)` | `owned []T` | Allocate a slice of `cap` (default `len`) zeroed elements from the heap and return an `owned []T` that owns the backing. Element `T` must be zero-initializable and scalar/pointer-sized (memory-backed/struct elements are not yet supported); `[]owned T` construction is not yet supported (build via `owned(...)` + `append`). |
| `append(s, v)` | `(S, E) S` | Write element `v` into `s`'s spare capacity `[len, cap)` and return a header with `len+1`; **traps** if `len == cap` (non-allocating — no reallocation). Ownership-polymorphic: the result type is exactly `s`'s type `S`, so `append([]T,T)→[]T`, `append([]owned T, owned T)→[]owned T`, etc. Used as `s = append(s, v)`. |
| `pop(s)` | `(S) (S, E)` | Remove the last element, returning `(shortened slice, element)`; **traps** if `s` is empty. Ownership-polymorphic like `append`. Used as `s, x = pop(s)`. |
| `alloc(T)` | `owned *mut T` | Allocate writable storage for one `T`; consumes a type expression, not a runtime value. |
| `new(expr)` | `owned *mut T` | Allocate writable storage for the expression's type, initialize with `expr`, return an owned pointer. |
| `free(p)` | `void` | Free an `owned *T` pointer, or an `owned []T` slice's backing, and consume the obligation. Freeing an *element-owned* slice (`owned []owned T`) **traps** unless it is drained (`len == 0`) — `free` only releases the backing, so live elements must be `pop`ped out first or they would leak. |
| `size_of(T)` | `i64` | Size in bytes of `T`'s representation. A compile-time constant; consumes a type expression, not a runtime value. |
| `align_of(T)` | `i64` | Required alignment of `T` in bytes. A compile-time constant; consumes a type expression, not a runtime value. |
| `dispose(x)` | `void` | Consume an `owned` binding with no runtime effect (see [Ownership](#ownership)). |
| `owned(expr)` | `owned T` | Unsafe ownership promotion; **destination-typed** — adopts the LHS's owned mask at every level (see [Bring-your-own-memory](#bring-your-own-memory-byom)). |
| `unsafe_cast(T, e)` | `T` | Reinterpret a pointer or `uintptr` as exactly `T`. The one construct that can manufacture a pointer. |
| `T(expr)` | `T` | Type cast (for any type `T`, including primitives and user-defined aliases). |

`alloc`, `new`, `make`, and `free` lower to `_heap.alloc(size i64)` and `_heap.free(p *mut byte)` (`make` allocates `cap * sizeof(T)` bytes; `free` of a slice frees its backing pointer). `len`, `cap`, `append`, and `pop` lower inline — `len`/`cap` are `[ref+8]`/`[ref+16]` loads, and `append`/`pop` are header reads plus a bounds-checked element store/load. None of these are callable through a function-pointer value: passing `len` or `alloc` as a value is a compile error. A user may still declare an ordinary function named `make`/`append`/… reachable qualified (`pkg.make(...)`); the intrinsic only claims the bare, unqualified call site.

`unsafe_cast(T, e)` reinterprets `e` — a pointer or a `uintptr` — as `T`. `T` is
the **full result type**, not a pointee, so mut and nullable bits are stated at
the cast: `unsafe_cast(*mut node, a)`. Both arguments are unusual: the first is
a *type*, parsed by the type grammar, and the second an ordinary expression.

The source must already be a pointer or a `uintptr`; a plain integer is
rejected with a message directing you to `uintptr(...)`. That costs one visible
conversion and buys an audit surface — every raw address in a program is a
`uintptr`, so nothing becomes a pointer without passing through a construct
that says so.

`T` may not carry `owned`. Owned bits imply a consumption obligation the cast
cannot establish, so ownership stays with the existing escape and the two
compose: `owned(unsafe_cast(*mut node, a))`. Keeping them separate keeps each
assertion auditable on its own.

**The cast has no precondition.** It is always legal and always succeeds,
because it only produces a value; alignment and validity are properties of what
you subsequently *do* with the result, not of its existence. Holding a
temporarily-misaligned or not-yet-valid pointer is legitimate. Its result
carries no provenance the checker can know, so it is an *untracked* pointer —
valid to dereference, but tracking to no origin. That is the precise sense in
which the operation is unsafe. Pointers obtained any other way keep their full
use-after-free and borrow checking.

`size_of` and `align_of` report the layout described in [The Boson ABI](#the-boson-abi-babi-0) — they read the same `Size`/`Alignment` computation the compiler uses to lay out storage, so they cannot drift from it. Both fold to constants at compile time and are therefore legal anywhere a literal is, including a file-scope initializer; there is no runtime size query. Like `alloc(T)`, the argument is a **type**, parsed by the type grammar rather than the expression grammar — `size_of(x)` for a value `x` is not accepted. `align_of` exists rather than being spelled as a literal because BABI-0's alignment ceiling is a property of the ABI and may rise (see [Alignment](#alignment)); code that asks stays correct across that change.

The `builtin` package is special: bosc auto-imports it into every package (except `builtin` itself) and the `-listimports` driver always emits `builtin` first, so the build system pulls in `target/builtin.bo` automatically. The package currently exposes:

| Name | Form | Description |
|------|------|-------------|
| `error` | `interface { message(e *self) []byte; destroy(e owned *self) }` | The standard error-condition interface. Concrete types satisfy it by providing both methods; see [Example: the error interface](#example-the-error-interface). |

Bare references to `error` resolve to `builtin.error` without needing an explicit `import "builtin"`.

**`string` package** — string formatting and stdout primitives:

| Function | Signature | Description |
|----------|-----------|-------------|
| `string.puts` | `([]byte) void` | Write a byte slice to stdout |
| `string.puti` | `(i64) void` | Print a signed integer to stdout |
| `string.putb` | `(byte) void` | Print a byte value as a decimal integer |
| `string.putc` | `(byte) void` | Print a single byte as a character |
| `string.exit` | `(i64 code) void` | Exit the process with the given code |

File IO lives in the `io` package, built on the raw syscall wrappers in `sys`. The typed FD API in `io`:

| Function/Method | Signature | Description |
|------|------|------|
| `io.O_RDONLY`, `O_WRONLY`, `O_RDWR`, `O_CREAT`, `O_EXCL`, `O_TRUNC`, `O_APPEND`, `O_NONBLOCK`, `O_CLOEXEC` | `i64` | File-mode flag constants matching Linux `<fcntl.h>`. Combine with bitwise `|`. |
| `io.open` | `(path []byte, flags i64, mode i64) owned open_result` | Returns `FD{fd owned FD}` on success or `ERR{err error}` on failure. Extract the descriptor through a consuming switch, or destroy an unused result. |
| `io.open_result.destroy` | `(r owned open_result) void` | Closes a successful result; consumes either variant. |
| `io.FD.read` | `(fd *FD, buf []mut byte) i64, ?error` | Read up to `len(buf)` bytes. Returns `(n, err)` — `n=0` on EOF; `err=nil` on success. |
| `io.FD.write` | `(fd *FD, buf []byte) i64, ?error` | Write `buf`. Returns `(n, err)`; short writes with `err=nil` are not errors. |
| `io.FD.close` | `(fd *owned FD) ?error` | Close the fd and consume the owned obligation. Returns `nil` on success or an error. |
| `io.STDIN`/`STDOUT`/`STDERR` | `FD` | Standard file descriptors (0, 1, 2). Borrowed — must not be `close`d. |
| `io.reader` | `interface { read(r *self, buf []mut byte) i64, ?error }` | Anything readable; `FD` satisfies it. |
| `io.writer` | `interface { write(w *self, buf []byte) i64, ?error }` | Anything writable; `FD` satisfies it. |
| `io.copy` | `(dst writer, src reader) ?error` | Copy until EOF; returns `nil` on success or a read/write error. |

The raw syscall wrappers `io` is built on live in `sys` alongside the rest of
the syscall surface: `sys.read(i64, []mut byte) i64`, `sys.write(i64, []byte)
i64`, `sys.open([]byte, i64, i64) i64`, `sys.close(i64) i64`, each returning the
kernel's raw result (negative errno on failure). See § Built-in Functions for
the `sys` package.

The `_init` package provides `_init.start` (the ELF entry point) and `_init.index_oob` (called by bounds checks). The `_heap` package provides the memory allocator, written in Boson (`runtime/_heap/heap.bos`): a three-tier design -- power-of-two size classes for small objects, contiguous span runs for large ones, direct `mmap` for huge ones -- carving 4MiB chunks out of 1GiB reserves, with `MADV_DONTNEED`-based release. It imports `sys`, so those objects link alongside it. The `_iface` package provides `assert_to`, the runtime helper backing interface-to-interface type assertion (lazy per-typedesc itab cache). The `_sys` package is the generic syscall floor — `syscall0`…`syscall6`, the only hand-written assembly the syscall surface needs — and `sys` is the typed Boson package built on it: file IO (`read`, `write`, `open`, `close`, which `io` wraps), memory (`mmap`, `munmap`, `mprotect`, `madvise`, `brk`, `map_anon`), plus the `PROT_`/`MAP_` constants and the errno convention. Adding a syscall means adding a Boson wrapper, not another assembly stub. The `pair` package exists as a minimal cross-package struct used by tests.

**`fmt` package** — composable formatting, built on `io.writer` and runtime type assertion. Five layers, smallest first; all storage is caller-owned (no GC obligations in the public surface), and the only heap traffic is the lazy itab cache inside `%v` dispatch.

*Layer 1 — raw conversions* render one value into the head of a caller-provided `[]mut byte` and return the rendered bytes as a sub-slice of that buffer (`out[0:k]`, a borrow of the `out` parameter made legal by return-alias inference). No allocation, no I/O. The caller reads the result's `len` for the byte count and compares it against `n_digits`/`n_udigits`/`n_hex_digits` to detect truncation.

| Function | Signature | Description |
|----------|-----------|-------------|
| `fmt.itoa` | `(out []mut byte, n i64) i64` | Signed decimal; leading `-`. |
| `fmt.utoa` | `(out []mut byte, n u64) i64` | Unsigned decimal. |
| `fmt.htoa` | `(out []mut byte, n u64) i64` | Lowercase hex, no `0x` prefix. |
| `fmt.n_digits` / `n_udigits` / `n_hex_digits` | `(n) i64` | Predicted byte counts (for truncation detection). |

Truncation: if `out` is too small, the longest prefix that fits is written and the returned count equals `len(out)`; compare against the matching `n_*` helper to detect it.

*Layer 2 — writer helpers* format one value into a stack scratch buffer and emit it through an `io.writer` in exactly one `write` call, returning the `(written, error)` pair that mirrors `io.writer.write`: `fmt.str`, `fmt.int`, `fmt.uint`, `fmt.hex` (`0x`-prefixed), `fmt.char`, `fmt.bool`, `fmt.nl`.

*Layer 3 — `fmt.Builder`* is a bring-your-own-memory accumulator: `struct { buf []mut byte, pos i64, err_ error }`, constructed inline by struct literal (no constructor). Append methods (`str`/`int`/`uint`/`hex`/`char`/`bool`/`nl`) follow a latched-error pattern — the first append that would overflow sets `err_` to `io.io_err.ENOSPC` and every later append is a no-op; inspect `error()` once at the end. `bytes()` returns the borrowed view `buf[0:pos]`; `space()` is remaining capacity; `reset()` rewinds the cursor and clears the latched error. The append methods return nothing (not `*mut Builder`): Boson forbids returning the borrowed `*mut` receiver, so chaining is expressed as a sequence of statements. `fmt.BuilderWriter{b: &builder}` adapts a `*mut Builder` to the `*self`-shaped `io.writer` so a Builder can be a `printf`/`Formatter` sink.

*Layer 4 — user-extension interfaces* `fmt.stringer { string(self *self) []byte from(self) }` and `fmt.Formatter { format(self *self, w io.writer) error }`. The `from(self)` clause lets a stringer return a borrowed view of its own bytes (`return self.buf`, zero allocation); a static-returning `string()` also satisfies it (∅ ⊆ {self}). A type renders under `%v` via `stringer` if it implements it (cheap, no writer); otherwise via `Formatter` (constructs into a writer). Both are discovered at runtime through interface assertion.

*Layer 5 — variadic helpers* `fmt.print(format []byte, args ...any) i64, error` (to stdout) and `fmt.printf(w io.writer, format []byte, args ...any) i64, error`. A verb names a *rendering*; the argument's own type (carried by the `any`) decides correctness, so there is no verb whose job is to announce the type. Directives: `%d`→decimal for any integer type (signed types signed, unsigned unsigned), `%x`→`0x`-prefixed hex for any integer type, `%s`→pointer-to-`[]byte` (`&name` or `&"literal"`), `%c`→an integer as a character (its low byte), `%v`→natural by the value's type (integers in decimal, `bool` as `true`/`false`, `[]byte` as text, then `stringer`/`Formatter`, else a `%!v(?)` placeholder), `%%`→literal `%`. (C's `%u`/`%t` are gone — the type already carries signedness and boolean-ness; the verbs that existed only to restate the argument's type are unneeded.) A verb against a genuinely incompatible value (e.g. `%d` of a `bool`) writes an inline `%!d(BADTYPE)`-style marker and latches a non-OK error but continues; an unknown verb (e.g. `%q`) consumes its arg, writes `%!q(BADTYPE)`, and latches the same error; an out-of-args slot writes `%!d(MISSING)`; surplus args are appended `%v`-style and latch a surplus error; a write failure stops immediately. The `BADTYPE` / `?` placeholders are fixed v1 markers: surfacing the arg's actual type name in the marker awaits a typedesc-name intrinsic that bosc does not yet expose to source (fmt proposal Open Question #6); until it lands the markers carry no type name. `Formatter` dispatch wraps the writer in a stack-allocated counting adapter so the returned byte total includes the Formatter's output.

Because a slice (24 bytes) cannot be coerced into an interface by value, `[]byte` arguments at a `...any` call site must be addressed — `fmt.print("%s", &name)`. Function-scope `&"literal"` (below) supplies the literal form `fmt.print("%s", &"hello")`.

`alloc(T)` is only valid for zero-initializable types. Types containing non-null pointers are not zero-initializable, so they must be constructed with `new(expr)` or some other initialized storage path.

`free` consumes only allocation ownership (`owned *T`); it does not recursively dispose a pointee obligation such as `*owned T`. If the pointee is a struct with owned fields, those fields must already have been consumed before `free` is allowed.

### GNU-style option parsing (`flags/gnu`)

`flags/gnu` supplies independent `Parser` objects implementing GNU
`getopt_long` and `getopt_long_only` parsing rules. `Parser.new(argv,
short_options, long_options)` accepts argv including the program name,
a GNU option string (`a`, `b:`, `c::`, `W;`), and a slice of `Option`
records (`name`, `argument`, `flag`, `value`). `Parser.configured` also accepts
`Config{long_only: ..., posixly_correct: ...}`. `next()` returns option codes,
zero for explicit flag-slot writes, code 1 for operands in return-in-order
mode, error codes `?` / `:`, or -1 at end. Fields expose argument presence,
long-option index, original token, and typed errors; no diagnostics are
printed automatically.

Default GNU ordering scans past operands and stably permutes a private
shallow copy of argument headers. Leading `+` stops at the first operand;
leading `-` returns operands in encounter order. `--` ends option processing.
`operands()` borrows the final operand suffix; `reset()` restores the original
order. `destroy()` releases the parser and its header copy. Input bytes,
option definitions, and flag slots remain borrowed. There is no per-option
allocation. The POSIXLY_CORRECT setting is explicit because the runtime has
no environment API. See the [package and API doc comments](runtime/flags/gnu/gnu.bos), also
available through `bdoc`, for a tutorial from a single short flag through
long names, option values, and operands, followed by advanced parsing rules.
Documentation comments support plain paragraphs, `##` section headings, and
fenced code examples;
`bdoc` preserves example indentation and escapes their contents as text.

### Struct Literals

```
type point struct { x i64, y i64, z i64 }

var p point := point{ x: 10, y: 20, z: 30 }
```

Struct literals are context-typed. Omitted fields are initialized to their default on every construction, including when stack storage is reused. This is permitted only if the field's type is zero-initializable. Non-null pointer fields are not zero-initializable and must be provided explicitly.

**Bare struct literals.** A `{ field: val, ... }` form without a leading type name is legal at any site where the destination shape is known: return statements, typed `:=` and `var` initializers, assignments to a struct-typed lvalue, and function arguments whose parameter type is a struct. The literal takes the destination's struct shape. Using a bare literal where no shape is in scope (e.g. as an unbound expression) is a compile error.

```
fn divide(a i64, b i64) struct { quot i64, rem i64 } {
    var q i64 := a / b
    return { quot: q, rem: a - q * b }     // shape from return type
}

var r struct { quot i64, rem i64 } := { quot: 1, rem: 0 }
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
var fd owned i64 := open(...)        // stack i64, carries an obligation
var t  owned transaction := ...      // stack struct, carries an obligation
var p  owned *whatever := alloc(...) // heap pointer, carries an obligation
```

Ownership obligations are orthogonal to memory location. A file descriptor is just an integer on the stack, but it has an obligation. A heap pointer has a memory obligation. Neither implies the other.

### Creating owned values

An owned value is created by declaring it with `owned`:

```
var t owned transaction := transaction{ socket = open_socket() }
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
    fd owned i64 := open(...)
    do_something(fd)   // owned i64 coerces to i64 — obligation stays with caller
    read(fd, ...)      // still valid
    close(fd)          // owned in parameter — fd is moved, now invalid
    read(fd, ...)      // COMPILE ERROR: fd was moved
}
```

### Move semantics

After a variable is moved into a consuming function, it is **invalid** for the remainder of its scope. The compiler refuses any use of it — reading, writing, or passing it anywhere.

```
t owned transaction := create_transaction(...)
commit_transaction(t)   // t moved — invalid
abort_transaction(t)    // COMPILE ERROR: t already moved
```

Moving an owned value to a **new owner in the same scope** — `t2 owned transaction := t` — transfers the obligation to the new owner rather than discharging it. The source binding `t` is invalid afterward, but a borrow taken *before* the move stays valid: it now answers to the new owner and remains usable until that owner is itself consumed. (This holds for scalar values via both declaration and assignment, and for aggregates via declaration; re-binding an aggregate owner by *assignment* is a current limitation.)

```
t owned transaction := create_transaction(...)
view i64 := t          // borrow: no obligation
t2 owned transaction := t   // obligation transfers to t2; t invalid
describe(view)         // ok — view answers to t2's live resource
commit_transaction(t2) // discharges the obligation; view now invalid
```

`var` bindings can be re-established after a move by assigning a new value:

```
var fd owned i64 := open(...)
close(fd)           // fd invalid
fd = open(...)      // fd valid again — new obligation
close(fd)
```

The re-init form also accepts a struct literal of the matching shape, mirroring how the first initialization works:

```
var f owned foo := foo{x: 1}
dispose(f)
f = foo{x: 20}      // re-init via struct literal, no owned(...) wrapper needed
dispose(f)
```

Re-initialization is rejected while any **live owned alias** still references the binding's storage. The check uses the pointer-flow tracker: an alias is `&f` (or a struct field that copied `&f`) bound to an owned type whose obligation has not yet been consumed. Borrowed `*T` aliases do not block re-init — they carry no cleanup responsibility, so observing the new value through them is memory-safe.

```
var f owned foo := foo{x: 1}
var fp *owned foo := &f
f = foo{x: 20}        // COMPILE ERROR: fp still owns f's storage
dispose(fp)

var f owned foo := foo{x: 1}
var fp *owned foo := &f
dispose(fp)           // fp consumed; alias is no longer live
f = foo{x: 20}        // ok
dispose(f)
```

Immutable (`:=`) bindings cannot be re-established — once moved, they remain invalid for the rest of the scope.

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
- `[]owned T` → OwnedMask at the element level (bit 1, bumped by the slice): each element is owned
- `owned []T` → OwnedMask bit 0: the slice owns its backing buffer
- `owned [N]T` → OwnedMask bit 0 set (the whole array value is one obligation)

The same convention applies to MutMask. This means `*mut T` and `*owned T` are placed symmetrically — both qualifiers sit between the `*` and the type they modify.

`mut` before a base type is rejected (`mut i64` is meaningless; `mut` only makes sense on a pointer or slice level). `owned` before any type is valid.

#### `owned` on slices and fixed arrays

Position is meaningful, exactly as it is for `mut` (see
[Mutability](#mutability-and-the-type-system)): a leading `owned` binds the
level to its right, and where it lands distinguishes *which* thing is owned.

- **`owned []T` — the slice owns its backing.** A slice is a fat pointer
  `{ptr → backing, len}`; a leading `owned` puts the obligation at bit 0, on the
  slice value itself. One obligation, discharged by a single `free` of the
  backing. The elements are not individually owned.
- **`[]owned T` — each element is owned.** Putting `owned` after the `[]` binds
  it to the *element*; the slice bumps it to bit 1. This is per-element
  ownership: every element carries an obligation, managed through the slice's
  operations (append moves one in, remove/drain moves them out) — you never move
  an element out by raw index (`s[i]` yields a borrow). This mirrors `[]mut T`
  (mutable *elements*) vs `mut []T` (mutable *slice*).
- **`owned [N]T` — the whole array is one obligation.** A fixed array has no
  separate backing — it *is* its storage, inline, like a struct's bytes — so
  `owned` attaches to the value (bit 0) and the array is disposed as a unit. The
  array analogue of `owned <struct>` ([Owned struct fields](#owned-struct-fields)).
- **`[N]owned T` is rejected.** A fixed array has no per-element storage seam, so
  element-position ownership is meaningless — you own the whole array or nothing.
  This is exactly parallel to `[N]mut T` being rejected: neither qualifier is
  meaningful in an array's element position, because the array fuses element and
  container into one value. (Per-element ownership needs the separate backing a
  *slice* has; hence `[]owned T` is allowed and `[N]owned T` is not.)

**Reslicing an owned aggregate borrows — it never transfers ownership.**
`a[:]` on an `owned [N]T` array (or `s[:]` on an `owned []T` slice) yields a
plain, non-owned `[]T`: a *borrowed view* into the owner's storage, exactly like
`&a` yields a borrowed `*T`. It does **not** produce a second `owned []T` — that
would fabricate a duplicate obligation over one storage (the slice analogue of
copying an `owned *T` into a second `owned *T` and keeping both, which is a
move). The single obligation stays with the array/slice binding and is
discharged by disposing *it*. The view roots at the owner's flow origin, so
disposing the owner invalidates the view and any raw pointer taken into it
(`&s[i]`): a read or deref through the stale view is rejected as
use-after-consume. The slice's element writability still follows the source
lvalue's mutability (`var` array → `[]mut T`, `const` → `[]T`); only the *owned*
bit is stripped.

The same rooting applies when the array lives behind a pointer. `(*p)[:]` is not
a copy — a plain value deref like `y := *arr` copies the array's bytes into an
independent binding (so `y` outlives `free(p)`), but `[:]` is an *address-taking*
operation: "deref `p`, take the address of that array, build a slice header over
it." The resulting view aliases `p`'s backing, so it roots at `p`'s origin and
`free(p)` (or any invalidation of `p`) invalidates the view — a read or store
through a saved `s := (*p)[:]` after `free(p)` is rejected as use-after-free,
just as the direct `(*p)[i]` deref is rejected at its own site. (`[:]` and `&`
are the two address-of forms; both propagate the base's provenance, whereas a
value deref does not, because it copies.)

**A value copy carries the tracking of the value it copied.** `x := *e` reads
`*e` and copies it; the copy's tracking is whatever *that value* tracks, decided
by the value's type, not by where the storage came from. A scalar tracks nothing
(`z := *new(i64)` is independent — and identical to `z := *f()` where `f`
returns `owned *i64`; both are just "copy the pointee scalar"). A **pointer**
value tracks its target, so copying a pointer out of another pointer preserves
the chain: in

```
pp **i64 := &p     // pp points at p's slot
x  := *pp          // x copies p's value — x now points where p points
free(p)            // (or free/consume p's target through any owner)
return *x          // rejected: x's target is gone
```

`x` inherits `p`'s target, so a later free/consume of that target is caught at
`*x`. This is one level per deref — each `*` peels one indirection — and it
composes through `z := &y; x := *z`. Ownership alone drives this: there is no
"allocation" notion in the tracker; an owned pointer is an owned pointer whether
it came from `new`, `alloc`, or a function returning `owned *T`. A value read
*through a borrow* (`q *i64 := &p; x := *q`) yields a plain, non-owned value —
the borrow stripped ownership — so that copy is correctly independent.

**Implementation status.** Dynamic construction and the fill/drain cycle are
built: `make([]T, len[, cap])` allocates an `owned []T`, `append`/`pop` fill and
drain it, `s[i]` borrows, and `free`/`dispose` discharge it (the driving
"collect N owned things, use by borrow, drain" case — see [Built-in
Functions](#built-in-functions) and [`docs/implemented/PROPOSAL_owned_slices.md`](docs/implemented/PROPOSAL_owned_slices.md)).
Ownership is tracked **coarsely**: the flow checker treats any owned bit as a
single whole-binding obligation, so a `[]owned T` is discharged as a unit
(`dispose` coarsely, or drained element-by-element through `pop`) rather than
per-slot. `[N]owned T` is rejected at parse time. Remaining, in
[`TODO.md`](TODO.md): per-element (granular) enforcement, `make([]owned T, n)`
element-owned construction, memory-backed (struct) elements — a broader
slices-of-`>8`-byte-elements gap that also affects reslice — the `for owned x in
s` drain sugar, `swap`/`take` for the nullable slot-map, and a growable `Vec[T]`
(a library, pending generics — explicitly *not* part of this feature).

**Transferring slice backing ownership.** Slicing normally creates a borrow.
When the destination requires `owned []T`, a prefix of an owned slice can
instead transfer its backing obligation:

```
fn build() owned []byte {
    buffer := make([]mut byte, 1024)
    // Fill buffer and determine the written length n.
    n := 12
    return buffer[:n]
}

text := build()
// Use text; len(text) is 12 and cap(text) is 1024.
free(text)
```

This works for initialization, assignment, arguments, and return slots,
including multiple returns. The source owner is moved; its backing resource
remains live under the destination. Existing borrows continue to refer to that
resource and become invalid when the destination owner is consumed. A helper
that accepts and returns an owning slice preserves the resource dependency
through its return-alias summary.

The lower bound must be omitted or a compile-time expression equal to zero.
Every prefix in a nested slice expression must satisfy that rule. An offset
slice such as `buffer[1:]` cannot transfer ownership: the runtime header holds
only `{ptr, len, cap}`, and `free` needs the original backing pointer. A
borrowed binding cannot supply ownership, even if it happens to reference a
prefix of an owner. An owning pointer to an array can transfer its obligation
through `(*owner)[:n]`; an ordinary local array cannot.

Slices with owned elements must be moved whole rather than transferred through
slicing, so shortening cannot abandon element obligations. Checked prefix
transfer preserves an existing obligation; it does not implicitly promote a
borrow to ownership. Resource tracking follows ownership and scope dependencies,
independently of whether construction used `make`, `alloc`, or a helper.

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
var tr owned *owned transaction := alloc(transaction)
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
- **Non-owning aliases are allowed but tracked.** A value of type `*T`, `*mut T`, `?*T`, etc. may be copied freely. A value-typed binding coerced from an owned source (`var t i64 := fd`) may also be created freely. In both cases the alias is recorded in flow state with the source's Origin, and the compiler invalidates it when the source is moved.
- **Coercing to a non-owned destination strips ownership.** Passing `owned *owned mut node` to a parameter of type `*node` or `*mut node` does not move either obligation: `Accepts` strips owned at the coercion site. The callee receives only an access capability. Coercing to an `owned` destination (e.g. `*owned node`) moves instead.
- **Value coercion is alias, not copy.** Stripping `owned` from a non-pointer source (e.g. `var t i64 := fd_owned`) duplicates the bit pattern into the destination but records a flow-state alias back to the source. Reading the destination after the source is moved is rejected as "cannot dereference pointer to X: the target was consumed," the same diagnostic a borrowed `*T` would produce. To obtain an independent obligation, construct it explicitly through a copy-returning function (see [Controlled copying](#controlled-copying)).

For example:

```
n owned *owned mut node := new(node{...})
a *mut node := n
b *mut node := a

b.val = 10       // mutates the object; ownership still belongs to n
free(n)          // consumes n's allocation obligation
```

This is intentionally not Rust-style exclusive mutable borrowing. Multiple non-owning aliases to the same object may exist at the same time. The price is conservative flow invalidation: if a write through one alias may affect an owned field, facts learned through another alias are no longer trusted.

```
type box struct {
    val i64
    child owned ?*owned mut node
}

fn bad(b owned *owned mut box) {
    alias *mut box := b
    var child owned ?*owned mut node := b.child
    alias.child = nil       // may have overwritten the field whose obligation was moved
    free(b)                 // COMPILE ERROR: b.child fact was invalidated
}

fn ok(b owned *owned mut box) {
    alias *mut box := b
    var child owned ?*owned mut node := b.child
    alias.val = 10          // does not touch owned storage
    free(b)                 // allowed if all owned fields are known consumed
    if (child) { free(child) }
}
```

The compiler tracks pointer aliases by origin. A pointer created by `new` or `alloc` and assigned to local `p` has origin `p`; aliases copied from it share that origin. Writes through unknown pointers, globals, or opaque `*mut` calls conservatively invalidate consumed facts for nullable fields that may acquire a new obligation. Non-null consumed facts persist until an explicit tracked reinitialization; opaque calls cannot establish restoration. Known branch alternatives retain their allocation identities.

Pointer-to-pointer aliases are tracked as aliases to pointer slots:

```
var alias *mut box := b
var slot *mut *mut box := &alias
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

#### Borrows across mutable calls

A borrowed result described by `from(X)` (explicitly on an interface, or inferred
from a function body) is valid only until opaque mutable access may change X's
storage. Passing a non-owning `*mut X` invalidates existing derived views, even if
the particular implementation happens not to reallocate on that invocation.
This is a rule about the capabilities in the signature, not an effect summary.
The object X remains usable, as do ordinary aliases pointing at X itself; new
borrows obtained after the call are valid.

```
b := fmt.StringBuilder.new()
b.str("abc")
var view []byte := b.borrow()
b.expand(1024)
// use(view)                 // rejected: the old backing may have been freed
view = b.borrow()            // fresh view of the current backing
use(view)
b.destroy()
```

The rule applies to concrete methods, direct and indirect functions, interface
method receivers, and interface arguments that grant mutable receiver access.
Read-only calls preserve loans. Calls through aliases use the same source
identity. Unknown mutation targets conservatively invalidate outstanding views;
known unrelated objects do not. A mutation on any reachable branch invalidates
a view at the join. Borrow copies and views stored in fields retain that state.
A result borrowing from multiple arguments depends on all of them.

Arguments are checked after evaluation and mutation invalidation: passing X
mutably together with a view of X to the same call is rejected, as is evaluating
a later argument that invalidates an earlier borrowed argument. For example,
`b.str(b.borrow())` is not safe in general because expansion may free the source
before the copy. Use independently owned source storage when that is needed.

The tracker keeps allocation lifetime separate from loan validity. A loan stores
a snapshot of its source and backing-allocation dependencies. Mutable calls
expire snapshots without freeing the owner. Fresh borrows use a new snapshot;
copying an old borrow never refreshes it. Freeing a captured allocation also
expires its loans, so reinitializing a field cannot resurrect views of its old
backing. This adds no runtime checks or representation changes.

Neither backing-storage views nor references to the aggregate prevent field
consumption. Struct aliases share consumed-field state: after `free(x.buf)`,
`y.buf` is unreadable when `y *mystruct := &x`, but other fields remain readable.
After `x.buf` is reinitialized, `y.buf` reads the new buffer. A slice copied from
the old buffer stays invalid. Local aliases can also be formed while a field is
consumed, provided they do not read it before restoration.

Consumption facts follow aliases stored in fields and every known alternative
of a branch-merged pointer. Consuming or initializing an owned field through an
alias with multiple possible target allocations is rejected: that write would
leave different ownership states depending on the runtime pointer. A partially
consumed aggregate cannot be copied, passed, or returned, including through its
aliases, because the receiving scope cannot see the consumed-slot facts.

#### Conservative current choices

Some restrictions are not fundamental to ownership, but are the current shape of the implementation:

- Mutable calls are treated as opaque unless the compiler is analyzing the write directly. A function taking `*mut T` may only write non-owned fields, but without an effect summary the caller must assume owned-field facts may be invalidated.
- Unknown aliases collapse precision. Globals, unresolved pointer origins, conflicting branch merges, and values that cross boundaries the flow model does not understand all force conservative invalidation.
- Stacked ownership cannot be partially moved. Consuming only the outer `owned *` or only the inner `owned T` would require the source binding's type/state to change.
- Non-null owned fields can be temporarily consumed within a function. The aggregate cannot cross a call or return boundary until those fields are restored; disposal/free remains permitted once all obligations are discharged.
- Partially consumed aggregates have no separate user-visible type. Their consumed slots are tracked locally through aliases; nullable fields can additionally represent consumption as `nil` across call boundaries.
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
    var x owned i64 := 42
    return box{ x: x, y: 10 }
}
```

Borrowing an owned struct as a plain by-value parameter strips field ownership inside the callee, just like borrowing any other owned value:

```
fn inspect(b box) {
    use(b.x)       // b.x is i64 here, not owned i64
}
```

Pointer views separate access to field obligations from responsibility for the whole struct. A `*mut box` exposes `x` as `owned i64`, allowing the callee to consume and replace that field without taking ownership of the struct. A `*box` exposes a borrowed `i64` and cannot consume the field: consuming it is a write. An ownership qualifier on the pointee (`*owned box`) carries responsibility for the whole struct but does not grant mutation. It cannot hide live field obligations from raw-free checks.

A mutable borrow must restore every consumed non-nullable owned field before any explicit return or fallthrough. Assigning to a live owned field is rejected; assigning a replacement after consumption moves its ownership into the field. Consumption facts follow tracked aliases and nested fields. Distinct backing-allocation origins let freeing an owned buffer invalidate outstanding borrowed views without invalidating the enclosing struct.

`owned(expr)` remains an explicit assertion escape hatch: it promotes the expression to an owned type without proving that every owned field came from an owned source. This is useful for construction patterns that the compiler does not yet track precisely.

A **non-null owned field** — whether a pointer (`owned *T`), a scalar (`owned i64`, `owned FD`), or a struct value (`owned T`) — may be moved out of an owned aggregate, so an aggregate holding owned resources can be destructed field by field (see [`PROPOSAL_owned_field_move.md`](docs/implemented/PROPOSAL_owned_field_move.md)). Moving a field out copies its value to the destination and records the field's obligation as consumed — just like moving a local `owned` binding. As defense-in-depth a register-sized slot is scrubbed to zero on move-out; a memory-backed struct-value field's bits are left in place (a wider store the scrub does not attempt), since correctness rests on the consumed-field tracking and the alias/escape gates, not on the scrub. The rules:

- Moving the field out leaves the aggregate **inconsistent** (the field has no nil placeholder; the slot is zeroed and the non-null type carries no nil check). While inconsistent, the aggregate may not **escape its scope** — it can't be passed to a function, returned, or copied to another value binding — because the receiving scope cannot see its consumed slots. Local reference aliases remain permitted and share those slots' consumed state. Only the shape-blind intrinsics `dispose` and `free` may consume it. Reading a moved-out field is a use-after-move error.
- **Re-initialization** restores consistency: assigning to a moved-out field (`f.a = new(...)`) is legal and marks it live again; once no non-null owned field is consumed, the aggregate may escape normally. Assigning to a *live* owned field is still rejected (it would leak its current value):

```
var b owned box := make_box()       // box{ x owned i64 }
b.x = 1                            // COMPILE ERROR: assign to live owned field; consume it first
```

(Value-typed owned fields — `owned i64`, `owned FD`, and struct-value `owned T` — are moved out and re-initialized exactly like pointer fields above; the move is a value copy and the same inconsistent-until-reinitialized tracking applies. The one shape that still cannot be moved out is a field whose *outermost* level is not owned — `*owned T`, where the pointer is a borrow of owned storage — which is consumed only by disposing the whole aggregate.)

Owned nullable pointer fields may also be moved out of an owned aggregate, and — unlike non-null fields — this leaves the aggregate **consistent** (`nil` is a representable, safe state), so it stays freely passable. Moving such a field copies the pointer to the destination, writes `nil` back into the original field, and records that the field's ownership obligation has been consumed:

```
type node struct {
    val i64
    next owned ?*owned mut node
}

fn destroy_one(n owned *owned mut node) owned ?*owned mut node {
    var next owned ?*owned mut node := n.next  // consumes n.next, sets n.next = nil
    free(n)                                  // allowed: n.next is known consumed
    return next
}
```

Non-null pointer fields cannot be moved out this way because there is no valid nil value to leave behind. If a field is intended to be moveable, it must be nullable (`?*T`).

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
    w  owned *whatever := alloc(whatever)     // memory obligation in w
    w2 *owned whatever := create_whatever(w)  // whatever obligation in w2

    // w and w2 alias the same memory, but carry separate obligations.
    // The type system tracks them independently.
    // The programmer must not access whatever fields through w while w2 is live.

    destroy_whatever(w2)   // owned in parameter — w2 is moved, invalid. obligation consumed.
    free(w)                // owned in parameter — w is moved, invalid. memory freed.
}
```

`owned(expr)` is an unsafe built-in: it asserts that `expr` may be used wherever an owned obligation is required, without consuming any source variable. It is **destination-typed**, like a bare struct literal: it adopts the destination's owned mask verbatim, asserting whatever ownership the LHS demands at every level at once. So `owned(&r)` on a `*T` satisfies `*owned T`, `owned *T`, or `owned *owned T` according to the destination, and `owned(x[:0])` on a `[]T` view satisfies `[]owned T` (element-owned) or `owned []T` (backing-owned) as demanded — the escape hatch expresses exactly the ownership the destination wants. When there is no destination context (e.g. `:= owned(expr)` inferring its own type) it falls back to setting the single innermost-level bit. The compiler cannot verify the semantic correctness; the programmer is responsible for the aliasing invariant.

Implicit ownership promotion is rejected: `var x owned T := some_non_owned_T` is a compile error. The only way to assert ownership that the source does not carry is `owned(expr)`. Integer literals are exempt (they can initialize an `owned T` directly, since there's no source value to alias). A checked prefix-slice transfer, described above, moves the source owner's existing backing obligation instead of asserting a new one.

For the common cases — stack-owned values and heap-owned values with bundled obligations — none of this complexity arises.

### Nullable pointers and ownership

Nullable owned pointers are useful for data structures because they provide a clear "empty" value when moving ownership out of fields:

```
var next owned ?*owned mut node := curr.next
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
    fd  owned i64 := open(...)
    fd2 owned i64 := copy(&fd)   // fd borrowed — still valid. fd2 is new.
    close(fd)
    close(fd2)
}
```

The implicit-coercion path that creates aliases (described in [Aliasing and ownership](#aliasing-and-ownership)) handles the "I want to read this value as a non-owned thing" case. `copy` handles the "I want a second independent obligation" case. The two never collide: aliases are invalidated when the source is moved; constructed copies are fresh obligations the source's lifetime does not constrain.

Note that `copy` takes `*T`, not `*owned T`. It does not need to own the source to read from it. This means `copy` could be called on a value that has already been disposed — it would read freed or uninitialized memory. Preventing that correctly requires a witness mechanism to enforce that the source is still live. That mechanism is not part of the current system and is deferred to a future design.

### Future direction: typestate and witnessed borrows

Two related limitations exist in the current system. (A third — partial consumption of an aggregate's **owned fields** — has been **implemented**; see [Owned struct fields](#owned-struct-fields) and [`PROPOSAL_owned_field_move.md`](docs/implemented/PROPOSAL_owned_field_move.md). A non-null owned field can be moved out, tracked as a local "inconsistent aggregate" fact that may not cross a scope boundary. The remaining two below are still future work.)

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
    n owned *owned mut node := new(node{...})
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

A borrowed pointer may not be STORED somewhere that outlives the call:

```
var saved ?*node

fn bad_global(n *node) {
    saved = n               // COMPILE ERROR: borrowed pointer escapes
}
```

But a borrowed pointer (or slice, or an aggregate whose fields borrow one)
MAY be **returned**. The compiler infers, per function, which parameters
each return slot may alias — the *return alias set* — records it in the
object file (the `retaliases` directive), and propagates it at every call
site, so the caller's own borrow tracking extends across the call:

```
fn id(n *node) *node {
    return n                // OK: id's return aliases parameter 0
}

fn first_word(s []byte) []byte {
    return s[0:1]           // OK: a sub-slice of the parameter
}

fn new_builder(buf []mut byte) Builder {
    return Builder{buf: buf, pos: 0}   // OK: the struct's field borrows buf
}

fn caller() *node {
    var local node
    return id(&local)       // COMPILE ERROR: the local escapes THROUGH id
}
```

The lifetime obligation moves to the caller: the returned view must not
outlive the argument it aliases, and a *local* reaching a returned slot —
directly or through any chain of calls — is still rejected. The inference
is automatic (no syntax), interprocedural (summaries travel through `.bo`
files for cross-package calls), and handles recursion by fixpoint.

#### Interface borrow contracts (`from(...)`)

A dynamically-dispatched method has no body to infer from at the call site,
so an interface method **declares** its borrow contract: a `from(...)` clause
on a return-type position names the parameters that slot may borrow (`self`
denotes the receiver):

```
pub interface stringer {
    string(self *self) []byte from(self)   // result may be a view of the receiver
}
```

A concrete type satisfies the interface iff, for each return slot, its
*inferred* return-alias set is a **subset** of the interface's *declared*
set (`impl ⊆ declared`, receiver = index 0). So an implementation may borrow
*less* than declared — a `string()` returning a static `.rodata` slice (∅)
satisfies `from(self)` — but never *more*. An interface with no `from`
clause declares ∅: a borrow-returning method cannot satisfy it (the old
coarse "no borrowing method may enter any interface" rule is the special
case ceiling = ∅). At a virtual call the result inherits the interface
value's provenance per the declared contract, so a borrowed result cannot
outlive the receiver.

The runtime assertion / type-switch path (`x.(K)`, which builds an itab from
the concrete type's full method table at runtime) is gated the same way: the
typedesc and `iface_desc` carry per-method borrow bitmasks, and
`_iface.assert_to` admits a method only when its impl mask is ⊆ the target's
declared mask — closing the path that would otherwise launder a borrowing
method to an under-declaring interface. The contract is parameter-level
(which parameter), not a Rust-style named lifetime; the expressiveness is
exactly the inferred return-alias set.

Local pointer aliases preserve borrowed provenance (an alias of a borrow
is the same borrow, recorded the same way in the alias set).

This is intentionally not a full Rust-style borrow checker: there are no
lifetime parameters and no user-facing annotations. Borrowed parameters
are capabilities that may be used, forwarded, and returned-with-tracking —
but never persisted into storage that outlives their source.

### Borrowing and ownership cleanup

The current system verifies ownership cleanup along compiler-visible ownership paths. Non-escaping borrows are what make that meaningful: a function taking `*T` is not allowed to stash the pointer and observe freed storage later.

Within a function, struct aliases share consumed-field facts through pointer-flow analysis. A field read through any alias fails while that slot is consumed and succeeds after explicit replacement. Opaque writes invalidate nullable-field cleanup facts because a callee may safely observe nil and install a new resource; they cannot prove restoration of a consumed non-null field.

Read-only borrows preserve facts:

```
fn inspect(b *box) i64 { return b.val }

fn destroy(b owned *owned mut box) {
    var child owned ?*owned mut node := b.child
    inspect(b)       // read-only borrow; b.child fact remains valid
    free(b)
    if (child) { free(child) }
}
```

Mutable borrows are conservative across function calls because the callee body may be unavailable or may write through the pointer:

```
fn touch(b *mut box) { b.val = b.val + 1 }

fn destroy(b owned *owned mut box) {
    var child owned ?*owned mut node := b.child
    touch(b)         // may have written owned fields; b.child fact invalidated
    free(b)          // COMPILE ERROR
}
```

Direct field writes are more precise: writing a non-owned field such as `b.val` does not invalidate facts for `b.child`, while writing an owned field does.

## Mutability and the Type System

Boson has two orthogonal axes of mutability. They are independent and compose cleanly.

### Axis 1: Binding mutability (`:=` / `var`)

Every binding is either immutable or mutable; this controls whether the binding itself can be rebound. A binding is introduced with `:=` and is **immutable by default** — mutability is the marked, opt-in case, because immutability is the common, safe one. Mark a binding `var` when you need to reassign it.

```
x i16 := 100         // immutable: x cannot be reassigned
var x i16 := 100     // mutable: x can be reassigned

myfoo foo := foo{ x: 10, y: &x }       // immutable: myfoo's fields can't be written
var myfoo foo := foo{ x: 10, y: &x }   // mutable: myfoo's fields can be written
```

Immutable-by-default and the `var` opt-out apply uniformly to all types — integers, structs, pointers, slices.

Function parameters are immutable by default. A parameter that needs to be reassigned within the function body must be declared `var` (`fn f(var x i64)`).

**`var` must be earned.** Because `var` is the marked, opt-in case, a
`var` binding that is never actually reassigned is a compile error: the
declaration claims a capability it doesn't use. The fix is to drop `var`.

```
var n i64 := compute()   // ERROR if n is never reassigned: "declared `var`
                         // but never reassigned; declare it immutable by
                         // dropping `var`"
n i64 := compute()       // correct
```

"Reassigned" means the binding's own var-ness is relied upon: a direct
rebind (`n = …`), a write to a value field or fixed-array element rooted
at the binding (`s.f = …`, `a[i] = …` on a by-value `a`), or taking a
mutable view of it (`&n`, `&n.f`, slicing an array `n[:]` — all of which
yield `*mut`/`mut` only because the binding is `var`). Writing *through* a
pointer (`*p = …`) or a `[]mut T` slice does not count — that mutability
comes from the pointee type, not the binding, so such a binding stays
immutable. Parameters are exempt (a `var` parameter documents a contract
even if a given body doesn't reassign it). The check is enforced per
scope, so sibling blocks may reuse a name independently.

**Bindings must be used.** Symmetrically, a binding — local *or* parameter
— that is never used is a compile error: declaring a name you never read
is dead code. "Used" means read, address-taken (`&x`), passed to a call,
or consumed (`dispose`/move). A write alone is not a use, so a binding
that is only ever assigned and never read is still unused. There are no
exemptions; the escape is the discard.

**The discard `_`.** `_` is a write-only sink: it binds nothing, may
appear repeatedly, and reading it is an error. It is valid anywhere a
binding target appears — `_ := expr`, destructuring slots (`q, _ :=
f()`), `_ = expr`, multi-assign slots, and parameters (`fn f(_ i64)`).
The initializer or argument is still evaluated, so `_ := side_effect()`
runs the call and drops the result. To keep a parameter's name for
documentation while satisfying the use rule, read it into a discard:

```
fn write(self *T, buf []byte) {
    _ := buf   // `buf` is read (used); the value is discarded
}
```

One hard limit, shared with everything else `_` touches: **an owned value
can't be discarded.** `_` of an owned-typed value, slot, or parameter is
an error — an obligation must be consumed, never dropped.

### Axis 2: Write-through mutability (`*mut T`, `[]mut T`)

This axis only exists for *reference types* — pointers and slices. It controls whether you can write to the data *through* the reference. It is a property of the type, not the binding.

```
*T        // pointer: cannot write through
*mut T    // pointer: can write through

[]T       // slice: cannot write through (elements are read-only)
[]mut T   // slice: can write through (elements are writable)
```

An immutable binding to a `*mut T` cannot be rebound, but can still write through the pointer. A `var` binding to a `*T` can be rebound to a different pointer, but cannot write through it.

`mut` as a type qualifier is meaningless on non-reference types. The compiler rejects `mut i16` as a standalone type — there is no indirection involved.

### Composing the two axes

For any binding:

```
y *i16              // cannot rebind y, cannot write through y
y *mut i16          // cannot rebind y, CAN write through y
var y *i16          // CAN rebind y, cannot write through y
var y *mut i16      // CAN rebind y, CAN write through y
```

For struct fields, the binding mutability of the *struct instance* determines whether fields can be directly written. Pointer fields carry their own write-through permission independently:

```
type foo struct {
    x i16
    y *mut i16    // y points to a mutable i16
}

myfoo foo := ...

myfoo.x = 10    // illegal — myfoo is immutable, field cannot be written
myfoo.y = &z    // illegal — myfoo is immutable, field cannot be rebound
*myfoo.y = 100  // LEGAL — the type of y carries write-through permission
```

Pointer indirection nests correctly. Each `*` level independently carries its own `mut`:

```
y **i16          // immutable binding; cannot write through either pointer level
y **mut i16      // immutable binding; cannot change intermediate pointer, CAN write innermost i16
y *mut *mut i16  // immutable binding; CAN change intermediate pointer; CAN write innermost i16
var y *mut *mut i16  // all four: rebind y, change intermediate pointer, write innermost i16
```

Assignment through `*p` checks mutability of the immediate pointee slot, not the deepest pointee. Therefore `*mut *mut T` permits replacing the intermediate pointer with `*p = other`, while `**mut T` only permits mutating the final `T` after another dereference; it does not permit replacing `*p`.

### The address-of operator

The write-through permission of a pointer obtained via `&` matches the binding mutability of the source:

```
var x i16 := 5
y i16 := 5

&x   // *mut i16 — x is var, so you get a write-through pointer
&y   // *i16     — y is immutable, so you get a read-only pointer
```

This prevents laundering an immutable binding into a `*mut` pointer. Write-through permission flows from the source.

The same rule applies to struct fields: `&myfoo.x` yields `*mut i16` if `myfoo` is `var`, and `*i16` if `myfoo` is immutable.

`&` accepts more than bare names. At runtime, `&someVar`, `&arr[i]` (with constant or runtime index), and `&s.field` all produce a `*T` to the addressed storage; the compiler delegates index and field forms to the existing lvalue-walk machinery, which already knows how to compute element/field addresses.

Owned bindings have one extra rule. Taking a mutable address of an owned binding is rejected, because it would expose the owner slot itself and allow code to overwrite the obligation without consuming it:

```
var a owned *owned mut thing := new(thing{...})
var slot *mut *mut thing := &a      // COMPILE ERROR
```

`&` of an owned binding preserves the owned bits in the resulting pointer type. Whether the result acts as a borrow or as an ownership transfer is determined entirely by the destination type:

```
var x owned foo := make_foo()

inspect(&x)                  // param is *foo: Accepts strips owned, x stays owned (borrow)
var y *owned foo := &x        // dest is *owned foo: x is moved; y now carries the obligation
dispose(y)                   // discharges through y
// x is no longer usable

a owned *owned mut thing := new(thing{...})
slot *owned *owned mut thing := &a     // immutable owned ptr: a is moved into slot
free(slot)
```

This mirrors how value-typed owned bindings behave: `var y owned T := x` moves x because the destination is owned, while `var y T := x` would borrow. The pointer form follows the same destination-driven rule.

The compiler enforces these invariants without relying on `&` itself stripping ownership:

1. `Accepts` strips owned at coercion sites whose destination is non-owned, so `inspect(&x)` (param `*foo`) borrows without moving.
2. Move tracking marks the underlying variable as consumed when its address is delivered to an owned destination, and rejects subsequent reads or address-taking of the moved binding.
3. The mutable-address rule above prevents creating any `*mut`-shaped view of an owner slot, eliminating slot-replacement leaks.

In static-init context (see [Static Initializers](#static-initializers)) two more forms become legal:

- `&someGlobal` — pointer slot relocated to the named global.
- `&SomeStruct{...}` — allocates an anonymous file-scope global to hold the struct literal's bytes, then relocates the pointer slot to it. Composes recursively: `foo{bar: &bar{inner: &inner{...}}}` produces one anonymous global per nested `&`.

Non-string `&literal` at runtime is rejected with "cannot take the address of this expression at runtime; address-of-literal is only valid in static initializers" — there's no stable storage to point at for an arbitrary expression outside file scope.

**Function-scope `&"literal"` is the one exception.** Taking the address of a *string literal* at function scope is valid: `&"hello"` evaluates to a pointer to a 24-byte static slice header `{ptr -> bytes, len, cap}` synthesized in read-only-style static memory (one header per distinct literal, identical literals shared). Its lifetime is forever and its storage is never written, so the result is a stable, never-dies pointer to a `[]byte` — exactly what a `...any` call site needs for a string argument (`fmt.print("%s", &"hello")`). The header rides the same anonymous-global machinery (`StringSliceHeader` / `WriteStrSliceHeaders` in bosc) that file-scope `&SomeStruct{...}` uses, emitted as a block-form `var` directive with a relocation to the underlying string constant. The result type is pointer-to-byte-slice, written `*[]byte` (see [Type syntax: prefix reading order](#type-syntax-prefix-reading-order)).

### Function pointers

A function-pointer type `fn(args) ret` is a pointer-sized slot holding the address of a function with the given signature. `&someFunc` produces a value of that type:

```
fn add(x i64, y i64) i64 { return x + y }

var op fn(i64, i64) i64 := &add       // file scope: static reloc to add's symbol
fn use() {
    var fp fn(i64, i64) i64 := &add   // function scope: lea of add's symbol
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
var chosen fn(i64, i64) i64 := pick(0)

type dispatch struct { op fn(i64, i64) i64 }
var d dispatch := dispatch{op: &add}
d.op(7, 8)                            // struct-field indirect call
d.op = &sub                            // field reassignment
```

Implementation: a function-pointer type is an `ASTType` with `FuncSig != nil` (no `Name`, no `Element`). It's pointer-sized (8 bytes), and its type string renders as `fn(...)ret` without spaces so the rendered form survives bas's whitespace-tokenized `var name type` directive. At a static initializer, `&someFunc` encodes as an 8-byte zero slot with a `DataReloc` against `pkg.fname`; at runtime it lowers to `lea reg, pkg.fname` (the linker resolves data and code symbols uniformly). Indirect calls use `call r/m64` rather than the rel32 form: bas's CALL handler dispatches to the indirect encoding whenever the operand is a Ralloc or register, and the bosc compiler loads the target into a temp before evicting caller-saved registers so a register-resident function-pointer argument isn't lost during arg setup. Struct-field calls (`d.op(...)`) reach the indirect-call path through the same Funcall dispatch — the parser packs `d.op(args)` as `Funcall{Pkg: "d", FName: "op"}` regardless of whether `d` is a package or a variable, and the compiler distinguishes the cases by looking `d` up in the package table first, then the local/global variable table.

Parser limitation: `(args)` postfix is only legal after a bare symbol or `pkg.fn` shape, so `pick(0)(args)` (calling the result of a call directly) requires binding the returned pointer to a name first. Lifting this would require generalizing `parsePostfix` to allow `(args)` after any expression whose type resolves to a `FuncSig`.

### Implicit coercion

`*mut T` is implicitly coercible to `*T`, and `[]mut T` is implicitly coercible to `[]T`. A more-capable reference can always be used where a less-capable one is expected:

```
fn foo(x *i16) { ... }

var myT i16
foo(&myT)   // ok: &myT is *mut i16, coerces to *i16
```

The reverse is not permitted — a `*T` cannot be passed where `*mut T` is required, since that would silently grant write permission that was not declared.

### Strings

Strings are immutable slices of bytes: `[]byte`. The `str` type (a bare pointer to null-terminated bytes) does not exist. String literals have type `[]byte`.

```
greeting []byte := "hello\n"   // typical string constant
```

A mutable byte buffer has type `[]mut byte`. String literals cannot be coerced to `[]mut byte` — they're emitted by the compiler as `data` rather than `var` (the metadata distinction noted under [Types](#types)) and are intended to be treated as read-only even though the current loader maps the whole data section writable.

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

The compiler could warn when a `var` binding is never directly reassigned and no mutable reference to it (`&x`) is ever taken: "var x i16 never mutated — drop var." Taking `&x` (which yields `*mut T` for a `var` binding) counts as a potential mutation even if the callee only reads through the pointer, to avoid false positives. The check fires at the end of each scope and at function exit for `var` parameters. Not yet implemented.

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

- Instance methods take their receiver as the first parameter. The parameter name is arbitrary; `self` is conventional. Most methods use a pointer receiver (`*T`, `*mut T`, `owned *owned T`); small register-resident types — including `values` types and `i64`-sized type aliases that want value-backed interface satisfaction — may instead use a **value receiver** (`name(e T)`). The call-site dispatch rule (below) consults the declared receiver shape and either takes the address or copies the value.
- The receiver may carry ownership qualifiers: `*T` (borrow), `*mut T` (mutable borrow), `owned *owned T` (consuming — takes ownership of the pointed-to value).
- Pointer receiver calls implicitly take the address of addressable value receivers, including struct fields, narrowed variant payload fields, and array/slice elements. A temporary value such as `make_value().method()` cannot satisfy a pointer receiver. Consuming a field through its address marks that field consumed without clearing the storage before the callee reads it.
- **Static methods** take *no* receiver. They are namespaced under the type and called as `T.method(args)`. A static method has the same symbol shape (`pkg.T.method`) as an instance method; the only difference is that no receiver is implicit at the call site.
- Methods are emitted as regular functions with three-part symbol names: `pkg.TypeName.method_name` (e.g. `mypkg.foo.compute`).
- `type foo bar` without a block continues to work as before — a distinct named type with no attached methods.

**Concrete method call desugaring:**

- `v.method(args...)` where `v` has type `T` → `T.method(&v, args...)` if `method`'s first parameter is `*T`/`*mut T`/etc. (address taken automatically), or `T.method(v, args...)` if it is a bare `T` value receiver (the value is passed directly).
- `p.method(args...)` where `p` has type `*T` or `*mut T` → `T.method(p, args...)` (passed directly, no extra indirection).
- `(expr).method(args...)` where `expr` is an arbitrary expression of concrete type `T` → the expression is evaluated, its type looked up, and dispatched the same way. An interface-typed expression receiver (e.g. a field `h.s.method()`) is also supported: the receiver's address is computed via the lvalue path and the fat pointer is loaded from it, and the borrow-validity check keys on the receiver's flow path (so disposing the borrowed source before the dispatch is rejected). A **consuming** (`owned`-receiver) method still requires a named-binding receiver, since consuming an interface held in a field would need path-level move tracking.
- `T.method(args...)` where `T` is a type with a zero-receiver static method → direct call to `pkg.T.method(args...)` with no implicit receiver.
- Ownership qualifiers on the receiver are checked at the call site: calling a method with `owned *owned T` receiver on a non-owned binding is a compile error.
- Calling a static method through an instance (`v.method(...)` where `T.method` has no receiver) is rejected with a directed error suggesting the `T.method(...)` form.

### Interface declarations

```
interface Writer {
    write(self *self, b []byte) i64
}

interface error {
    message(self *self) []byte
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
- The remaining (non-receiver) parameter types and return type match exactly.
- The method's **receiver shape** is satisfiable from the *source* of the coercion (below).

**Receiver matching is source-driven, with an asymmetric mut rule.** The direction (value vs pointer) and the mut capability come from the value being coerced, not from the interface's declared receiver marker:

| Source coerced into `I` | Receiver shapes the method may declare |
|--------------------------|----------------------------------------|
| a bare value (`T`)       | value receiver (`self`) only |
| a read-only pointer (`*T`, `?*T`) | `*self` only |
| a mutable pointer (`*mut T`, `?*mut T`) | `*self` **or** `*mut self` |

The mutable-source row encodes the legal `*mut → *` weakening: a mutable view may call a `*self` (read-only) method. The reverse — a read-only `*T` source driving a `*mut self` (mutating) method — is **rejected**, because that would mutate through a view that promised not to. (`&x` of a `var` yields `*mut T`, so the common `greet(&d)` pattern coerces a mutable pointer and can satisfy either receiver kind; `&x` of an immutable binding yields a read-only `*T`.)

The compile-time satisfaction filter and the runtime `_iface` helper derive the same source→capability mapping and apply the same asymmetric weakening, so a compile-time-legal coercion and a runtime interface-to-interface assertion always agree.

Satisfaction is checked at coercion sites only — there is no eager or declaration-time check. If a coercion is never written, the compiler never checks whether the type satisfies the interface.

### Interface values (fat pointer)

An interface value is 16 bytes regardless of how many methods the interface declares:

```
{ data <8 bytes>, vtable *VtableShape }
```

The `vtable` pointer points to a compiler-generated static global. The `data` word has two interpretations:

- **Pointer-backed** (the default): `data` is an opaque type-erased pointer to the concrete value. Used whenever the source coerced into the interface is a pointer (or carries owned bits at any pointer level).
- **Value-backed**: `data` is the concrete value bits themselves, sign- or zero-extended into 8 bytes. Used when the source is a scalar or `values`-typed value of ≤ 8 bytes and every interface method declares a non-pointer (value) receiver. No heap allocation is required, and the data word aliases no caller storage.

Value-backed coercion is rejected when the value is larger than 8 bytes, when the source is a slice / fixed array / function pointer (these have well-defined pointer forms instead), or when any interface method declares a pointer receiver — the dispatch site has only a value to hand to `rdi`, so a pointer-receiver method has no address to receive.

### Vtable layout

For a concrete source *shape* of base type `T` satisfying interface `I` with N
methods, the compiler emits one static vtable global per `(base, shape, I)`
coercion, named `__vtable_<base>_<shape>__<I>` (the `<shape>` segment is a short
deterministic mangling of the source shape — `v` for a value, `p` for `*T`, `pm`
for `*mut T`, `s` for a slice, etc.):

```
var __vtable_foo_pm__Writer byte[N+2 * 8] {
    bytes "<shape word, 8 bytes>"   ; written into slot 1
    reloc 0  pkg.__typedesc_foo 0   ; slot 0: base typedesc pointer
    reloc 16 pkg.foo.write 0        ; slot 2: first method
    ...one entry per method, in interface declaration order...
}
```

The layout is `[typedesc_ptr, shape_word, method_0 .. method_{N-1}]`:

- **Slot 0** is a relocation to the base type's single `__typedesc_<base>`
  symbol (the type's identity). There is exactly one typedesc per *base* type,
  emitted by that type's home package; built-in scalar typedescs
  (`__typedesc_i64`, `__typedesc_byte`, …) are emitted by the `builtin` package.
- **Slot 1** is a 64-bit **shape word**: a canonical, byte-deterministic encoding
  of the source type's shape modifiers (pointer level, mut/nullable, slice,
  array length) as a bounded constructor stack read innermost-first. The empty
  stack (`shape_word == 0`) is a bare value. The shape word is what
  distinguishes `var a any := m` (value) from `var b any := &m` (pointer) — both
  share `__typedesc_i64` at slot 0 but carry different shape words at slot 1.
- **Slots 2..N+1** are 8-byte function pointers to the concrete methods, in the
  interface's declaration order.

Method dispatch loads `[vtable + (methodIdx+2)*8]`, skipping the typedesc and
shape-word slots.

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
e owned *mut myerror := new(myerror(42))   // owned *mut myerror
err owned error := e                       // moves ownership into the interface value; e is consumed
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
    message(self *self) []byte
    destroy(self owned *self)
}

type myerror i64 {
    message(self *myerror) []byte {
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

## Values Types

`values` types are a third `type Name <kind> { ... }` form alongside type aliases and structs. A values type names a finite set of symbolic cases, optionally pairs each case with a list of statically-known projection values, and (also optionally) attaches methods. The runtime value is just a compiler-private dense tag; the user-visible cases are *not* integer literals.

The motivating use case is allocation-free symbolic error values: a value-receiver method whose receiver fits in 8 bytes can satisfy an interface without any heap pointer (see [Interface values](#interface-values-fat-pointer)).

### Declaration syntax

There are two forms. Tag-only:

```
type color values {
    RED
    GREEN
    BLUE
} {
    name(c color) []byte {
        if (c == color.RED)   { return "red" }
        if (c == color.GREEN) { return "green" }
        return "blue"
    }
}
```

And projection-bearing:

```
type io_error values (i64, []byte) {
    NOT_FOUND:         404, "not found"
    PERMISSION_DENIED: 403, "permission denied"
    BUSY:              503, "busy"
} {
    message(e io_error) []byte {
        return []byte(e)
    }
}
```

- The optional `(T1, T2, ...)` parenthesized list after `values` is the **projection signature**: each case must supply exactly one initializer for every projection type, in declared order. Duplicate projection types in the signature are rejected.
- The case body is `NAME[: expr1, expr2, ...]`. Cases are separated by `;` (or a newline — the lexer inserts `;` automatically) or `,`. Duplicate case names are rejected.
- The optional second `{ ... }` block carries methods. Method definitions follow the same grammar as type-alias methods; a value receiver (`name(c color) ...`) is the canonical form, matching the closed-symbolic-set nature of the type.
- A tag-only values type may have no cases with initializers; a projection-bearing values type may not have cases without them. The arity check is enforced at AST construction time.

### Cases are scoped

Case names are namespaced under the values type and are *not* introduced into the package namespace:

```
return io_error.NOT_FOUND          // reference to a local values case
return errors.io_error.NOT_FOUND   // reference to an imported values case
```

A bare `NOT_FOUND` does not resolve in v1, even with `io_error` in scope; this is a deliberate choice that avoids collisions between two values types that both want names like `OK` or `UNKNOWN`. Case access through a runtime values value (e.g. `e.NOT_FOUND` where `e` has type `io_error`) is rejected with a directed error suggesting the type-prefixed form.

Selector resolution (`io_error.NOT_FOUND`, `errors.io_error.NOT_FOUND`, `pkg.Type.case`) flows through a shared selector resolver in `cmd/bosc/resolve.go` that walks `Dot` chains left-to-right and classifies the result (`ResolvedPackage`, `ResolvedValuesType`, `ResolvedValuesCase`, etc.). The same resolver covers struct-field access, qualified function calls, and qualified type names, so type checking and code generation cannot disagree about what a selector means.

### Runtime representation

A values type is represented at runtime as its private tag — `i64` in v1, dense from 0 in declaration order. For tag-only and projection-bearing types, `?V` uses the same one-word representation, reserving tag `-1` for `nil`; ordinary case tags remain `0..N-1`. The sentinel is independent of projection values, which may themselves contain `-1`. The tag itself is unreachable from user code: there is no operation that exposes the raw bits, and conversion to an integer always goes through a declared integer projection.

Projection data lives in compiler-generated static tables, one per declared projection type, indexed by tag. For the `io_error` declaration above, codegen emits two table symbols (`__projection_io_error__0` for the `i64` projection, `__projection_io_error__1` for the `[]byte` projection) plus the method functions.

The projection-table symbol scheme uses the case's *index in the projection signature* rather than the projection type's printed form. Earlier revisions encoded the type's structure into the key, but every flat-string encoding is collidable against some user-chosen type alias. Indices are scoped to a single values declaration (whose duplicate-projection check already enforces uniqueness), so a numeric index can never collide.

### Projection casts

A cast `T(e)` where `e` has values type `V` and `T` is one of `V`'s declared projection target types lowers to an indexed load from the corresponding projection table:

```
e io_error := io_error.NOT_FOUND
string.puts([]byte(e))      // copies the []byte projection at tag(e)
string.puti(i64(e))         // copies the i64 projection at tag(e)
```

There is no implicit conversion: every projection use is an explicit cast. Casting to a type the values declaration does *not* project to is rejected with the directed `Cannot cast V to T: V has no T projection` diagnostic.

The cast path handles scalar and memory-backed projection element types uniformly: scalar projections (i8…i64, function pointers, plain pointers) lower to a single `mov dest [base+idx*scale]`; memory-backed projections (`[]byte`, structs, fixed arrays) lower to a `lea` of the table slot followed by `spot_memcpy`. Element sizes that x86-64's SIB byte cannot encode (24-byte slice headers, larger structs) precompute the byte offset (`shl` for power-of-two sizes, `imul` otherwise) and index with scale 1.

### Closed symbolic set

Values types are closed: construction always selects a declared case. Unchecked casts from another type into a non-nullable values type are rejected:

```
io_error(999)              // error: integer-to-values cast not allowed
io_error(some_other_v)     // error: cross-values-type cast not allowed
```

The same rule blocks integer literals coercing into a values destination at file scope, in struct fields, in function arguments, in returns, and in array elements — every site where a `var x io_error := 999` shape could otherwise sneak through. Identity casts (`io_error(e)` where `e` is already `io_error`) are accepted as a no-op.

### Checked reverse projections

`?V(value)` reverses a declared projection and returns `?V`. A matching projected value produces its case; an unmapped value produces `nil` (internal tag `-1`). This is an explicit, compiler-generated conversion, not an implicit assignment coercion.

```
type status values (i64, []byte) {
    MISSING: 404, "not found"
    BUSY:    503, "busy"
}

s := ?status(404)
if (s != nil) {
    code := i64(s)             // 404: the check narrows ?status to status
}
text_case := ?status("busy")   // status.BUSY
unknown := ?status(999)        // nil
```

The source type selects the projection. Untyped numeric literals adopt the sole compatible numeric projection; if several match, the caller must supply an explicit argument type. The selected projection must be injective: duplicate projected values reject reverse conversion, naming the conflicting cases for a local declaration. Forward projections with duplicates remain legal. This works across package boundaries and file-local import aliases; the producer exports a generated lookup function for each reversible projection, with no object-format extension.

Reverse conversion supports integer, floating-point, boolean, and `[]byte` projections. Numbers use numeric equality (including equal positive and negative zero); byte strings use content equality, independent of backing pointer and capacity. NaN cannot be matched. Other projection types require an explicit mapping function: aggregate projections have no language-defined equality, and pointer/function identity is not a reverse-conversion contract. Lookup currently performs a linear search without allocating.

Nullable tag-only and projection-bearing values can be stored, passed, returned, compared with `nil` or cases, and initialized with `nil`. Default initialization uses `nil`, including nested struct/array fields and `alloc`/`make` storage. An explicit `if (s != nil)` or `if (s == nil) ... else ...` narrows the tracked binding or field. Projection, conversion to a non-nullable value or interface, method calls, and variant switches require a non-null value. Postfix `s?` checks tag `-1` at runtime and yields `V`, trapping on nil. Nullable values support `if (s)`, `if (!s)`, loop conditions, and logical operators: truth means tag `!= -1`, so the first case (tag zero) is true. Direct binding/field conditions narrow just like explicit nil comparisons; a narrowed values operand still tests against `-1`. Nullability is independent at each pointer level: `?*?V` has a nullable pointer (null address `0`) to a nullable values slot (nil tag `-1`). Testing or asserting the pointer checks the address; dereferencing yields `?V`, which needs its own check or assertion. A live pointer to a nil slot is distinct from a null pointer. Nullable payload-bearing variants and values conversions in static initializers are not supported.

Equality is defined only between values of the same values type:

```
if (e == io_error.NOT_FOUND) { ... }   // ok
if (e == another_v.SOMETHING) { ... }  // error: cross-type comparison rejected
```

Different values types are rejected from comparison even when both are represented as `i64` internally.

### Value-receiver methods and interface satisfaction

Methods on a values type are emitted as regular three-part symbols (`pkg.io_error.message`) and are called the same way as any other type-block method. With a value-receiver shape (`message(e io_error) []byte`), `v.method()` passes `v` directly — no address is taken, no memory backing is forced.

A values type whose methods are value-receiver shaped and whose tag fits in 8 bytes satisfies a value-receiver interface without heap allocation. The interface fat pointer carries the tag in its data word and the producer's vtable in the vtable word:

```
interface error_val {
    message(e self) []byte
}

fn open() error_val {
    return io_error.NOT_FOUND   // value-backed coercion; no allocation
}
```

The cross-package case works the same way: a consumer can return another package's values case through an interface declared in either package, and the importer's compilation registers a `(pkg.io_error, error_val)` vtable just as it would for any other concrete-type → interface pair.

### Cross-package metadata

Values types cross package boundaries through a new `.bo` directive parallel to `interface` and `struct`. The schematic form is:

```
values io_error {
    tag i64
    case NOT_FOUND 0
    case PERMISSION_DENIED 1
    projection i64
    projection []byte
    method message
}
```

This carries the case list (with private tags), the projection signature, and the bare method names. The importing package reconstructs the `ValuesDecl` from this directive and from the already-imported function table, so `errors.io_error.NOT_FOUND`, `[]byte(err)`, and `err.message()` all work cross-package without re-running the producer's `ToAST`. The projection-table symbols themselves are referenced by their qualified form (`errors.__projection_io_error__0`); the consumer emits a reference and the linker resolves it to the producer's data section.

### Payload variants (ADTs) and the variant `switch`

A `values` case may carry a **struct payload** — per-case runtime data — turning
the type into a tagged union (an ADT). A variant is literally a struct, declared
with brace syntax, and the tag selects which struct:

```
type file_info values {
    FILE_INFO{ fd i64, path []byte }
    SOCKET{ fd i64, addr []byte }
    CLOSED
}
```

A payload-bearing value is laid out flat as `{ tag : i64 }` followed by the
largest variant's payload, and is memory-backed like any struct. A recursive
payload must go through a pointer (`NODE{ left *tree }`); a by-value cycle has no
finite size and is rejected. Payload variants and the constant-projection form
(`values (i64, []byte) {...}`) are mutually exclusive on one type.

Construction uses struct-literal syntax on the case selector; a bare variant is
just its selector:

```
e file_info := file_info.FILE_INFO{ fd: 3, path: "config.bos" }
c file_info := file_info.CLOSED
```

### Ordinary value switches

Every switch requires parentheses around its head, like `if` and `for`. A
head declaration uses `:=`, including type switches: `switch (v := x.(type))`.

A switch over an ordinary comparable value selects the first matching constant
case using the same equality rules as `==`. The head is evaluated exactly once;
case bodies do not fall through into following cases. Cases may be scalar
literals, constant arithmetic/bitwise/boolean expressions, numeric conversions,
`size_of`/`align_of`, or tag-only values cases when comparing an interface.
A `:=` binding is immutable but is still a runtime value, so it cannot be used
as a constant case. Duplicate cases are rejected after conversion to the head's
comparison type, including equivalent arithmetic and floating-point signed zero.
NaN cannot be a case because it never compares equal with `==`.

```
switch (code) {
    case i64('v') { verbose = true }
    case i64('o') { output = parser.optarg }
    case 256     { show_help() }
    default      { handle_other(code) }
}

answer := switch (n) {
    case 1 10
    case 2 20
    default 0
}
```

Statement switches may omit `default`; an unmatched value then does nothing.
Expression switches must produce a value on every path: they require `default`,
except that a boolean switch covering both `true` and `false` is exhaustive
without one. All result arms must agree on their type; untyped literal results
default to `i64` or `f64` when inferring a switch expression's type. A head binding
(`switch (x := expr)` or `switch (var x := expr)`) is scoped to the switch and works
for statements and expressions.

The usual ownership, borrowed-storage, nullability, and pointer-validity branch
checks apply to every arm, including the unmatched path when present. Returning
arms do not constrain the state merged for code after the switch. A nil case
narrows a nullable head binding in that arm, and excludes nil in subsequent arms
and `default`. Strings are slices in Boson: slices, arrays, and structs do not
support `==`, so they cannot be ordinary switch heads.

The **variant `switch`** is another mode of `switch` (alongside ordinary value
switches and interface type switches): it dispatches on the tag and **narrows** the scrutinee to the
matched variant inside each `case`, after which the variant's fields are in
scope. A consuming switch or an expression switch must cover every variant or have a
`default`. An inspecting statement switch may omit cases; unmatched values are
preserved:

```
switch (e) {
    case FILE_INFO { puti(e.fd); puts(e.path) }   // e narrowed to FILE_INFO
    case CLOSED    { puts("closed") }
}
```

Field access is ordinary struct field access on the narrowed value: `e.fd` reads,
`e.fd = x` writes (through a mutable scrutinee), `&e.fd` takes a pointer. It is
only valid where the scrutinee is narrowed to a single variant (not in a
`default`). Accessing a field name shared by several variants resolves to the
narrowed variant's field (offset and type).

**Ownership** follows the final state of each reachable arm, including returns,
breaks, and continues. A variant constructor returned from a function adopts its
declared return type, including ownership, just as a struct literal does. An
owned constructor consumes the owned arguments placed into its payload.

Switching an owned ADT infers one of two operations:

- **Inspect:** every active owned payload field remains live. A field consumed
  and then reinitialized is live again, so temporary replacement is inspection.
- **Consume:** every active owned payload field is transferred or consumed.
  The compiler discharges the outer wrapper automatically. It cannot be used
  afterward, and a further `dispose(wrapper)` is a use after move.

Partial consumption and disagreement between consuming and inspecting arms are
rejected. An arm with no owned payload fields is compatible with either mode.
A consuming switch must be exhaustive. A borrowed scrutinee may only be
inspected; it cannot transfer ownership of the caller's payload. Explicit
`dispose` retains its assertion semantics and may abandon the entire wrapper.

An expression arm yields a value with a bare expression. An arm that exits the
enclosing function or loop contributes no result type. All arms that continue
must yield compatible types; a block that falls through is not a value. For
example, a result type with `ERR{err error}` and `FD{fd owned FD}` can be unpacked
without an extraction method or manual wrapper cleanup:

```
fd := switch (r := open(path, flags, mode)) {
    case ERR {
        report(r.err)
        return
    }
    case FD r.fd
}
// fd owns the descriptor here; r is out of scope and fully discharged.
fd.close()
```

The destination does not acquire an ownership obligation until its initializer
yields. An early exit in an initializer therefore does not owe cleanup of the
unfinished destination. A zero-initializable owned scalar may also be declared
without an initializer: it starts consumed, cannot be read, and can be assigned
on every path that reaches its later use. Owned structs still require an
initializer.

The **tag cannot change while narrowed**: reassigning the whole scrutinee or
taking its address inside its own case is rejected. A scrutinee with a live
alias is not narrowed, so a field access cannot read the wrong payload.

### What is not in v1

- The variant `switch` works as both a statement and an **expression**
  (`y := switch (e) { case A e.x  case B 0 }`, with value-producing
  arms agreeing on their type and exiting arms contributing no value). Payload variants cross package
  boundaries, and a head-bound scrutinee (`switch (x := expr)`,
  `switch (var x := …)`) is supported. Remaining follow-ons: or-patterns and
  guards.
- No exposed raw tag value. The compiler may encode tags as a different representation in the future without changing surface semantics; users observing the tag must declare an integer projection.
- No or-patterns, guards, or field access in a `default` case.
- No shorthand for hoisting selected case names into local scope.
- No generated `.data()` or printing helpers — users project explicitly.

### Implementation notes

A change to add `values` touches the compiler layers in order:

1. **Lexer** (`cmd/bosc/lexer.go`): adds `values` as a global keyword.
2. **Parser** (`cmd/bosc/parser.go`): extends `parseTypeDecl` to recognize `type Name values ...` and delegate to `parseValuesDecl`. The parser produces a dedicated `n_valuesdecl` node carrying projection-type nodes, case nodes, and method nodes.
3. **AST** (`cmd/bosc/ast.go`): `ValuesDecl` and `ValuesCase` types; a new `valuesDecls` map on `Context` (distinct from `structs`, `typeAliases`, and `interfaceDecls`); cross-kind collision rejection at registration time. `ToAST` assigns each case a dense private tag, checks duplicate cases, checks duplicate projection types, and checks per-case initializer arity.
4. **Selector resolver** (`cmd/bosc/resolve.go`): `ResolvedValuesType` / `ResolvedValuesCase` classifications; case-on-instance rejection (`e.NOT_FOUND` against a runtime values value); qualified cross-package case access.
5. **Codegen** (`cmd/bosc/compile.go`):
   - `ValuesDecl` top-level case emits the `values` metadata directive, one `__projection_<type>__<index>` static table per projection (encoded through the same path as ordinary array initializers), and the method functions.
   - Case references compile via the selector resolver: a `ResolvedValuesCase` lowers to `mov dest <tag>` as a value of the values type.
   - Projection casts (`T(e)`) detect the values-source case in `compileCast` and route to `compileProjectionCast`, which materializes the table base with `lea` and emits the indexed load.
   - Closed-symbolic-set rules close coercion sites that would otherwise admit non-values sources into values destinations (file-scope vars, struct fields, arguments, returns, array elements).
6. **Cross-package metadata** (`ofile.go`, `bwrite.go`, `cmd/bas/main.go`): a `ValuesShape` carries the tag width, ordered cases-with-tags, projection signature, and method names through the `.bo` format; `cmd/bas` parses the `values` directive and stores it on the producer's `OFile`.
7. **Tools** (`cmd/bdump`, `cmd/bdoc`): both inspect and document values declarations alongside structs, type aliases, and interfaces.

---

## The `any` interface, variadics, and type assertion

### The `any` interface

`any` is the built-in empty interface — the moral equivalent of Go's
`interface{}`. It declares zero required methods, so every concrete type
satisfies it trivially. It is available everywhere without an `import` (it lives
in the `builtin` package alongside `error`, and resolves by bare name the same
way). An `any` value is a normal 16-byte interface fat pointer; its 2-slot
vtable carries the source's `__typedesc_<base>` at slot 0 and the source's shape
word at slot 1 (there are no method slots).

```
var m i64 := 42
var a any := m      ; value-backed: data = 42,    shape word = bare value
var b any := &m     ; pointer-backed: data = &m,  shape word = *mut i64
```

### Variadic parameters

A function's last parameter may be variadic, written `name ...T`. Inside the
body it is an ordinary `[]T` slice; the `...` is shed at the body boundary.

```
fn sum(xs ...i64) i64 { ... }       ; xs has type []i64 in the body
fn printf(fmt []byte, args ...any)  ; args has type []any
```

Rules:

- The variadic parameter must be **last**; only one is allowed.
- At each call site, the compiler stack-allocates a backing array in the
  caller's frame, coerces each trailing argument into an element (per-element
  concrete→interface coercion for an `[]any` element type, or a width-matched
  value copy for a scalar element type), and passes a slice header pointing at
  that array. No heap allocation.
- An untyped integer literal argument is materialized as `i64` before coercion.
- A `>8`-byte value argument to an `...any` parameter is rejected with the same
  "value interfaces can store at most 8 bytes inline" error as a direct
  value-backed interface coercion — pass a pointer instead.
- The callee's slice is **borrowed**: it may not escape the call (stored into a
  global, returned, etc.), enforced by the existing slice-escape borrow checker.

**Explicit forwarding.** A single trailing `slice...` argument forwards a
pre-existing slice verbatim, with no re-wrapping (parallel to Go's `args...`):

```
fn outer(xs ...any) { inner(xs...) }   ; passes xs through unchanged
```

### Type assertion

`x.(T)` asserts that interface value `x` holds a concrete type `T`. Its static
type is `multiretu{T, bool}`, bound with either the two-value **declaration**
form or, to pre-existing lvalues, the comma-LHS **re-assignment** form:

```
var v i64, var ok bool := a.(i64)   // declaration form
if (ok) { use(v) }

var w i64 := 0
var found bool
w, found = a.(i64)                 // re-assignment form (existing lvalues)
```

The re-assignment form is a general comma-LHS multi-assignment `lv0, lv1, … =
rhs`; it is recognized only at statement position (a top-level comma after a
parsed expression there can only begin a multi-assignment LHS, so it does not
conflict with the commas in argument lists or struct literals). Each target
must be an already-declared, mutable (`var`) variable whose type accepts the
corresponding result component.

The lowering is an inline two-compare check: slot-0 typedesc identity against
`__typedesc_<base(T)>`, plus slot-1 shape-word equality against the canonical
immediate for `T`. Both must match. Shape matching is **exact** — `var x any := p`
where `p *mut i64` succeeds only on `x.(*mut i64)`, not `x.(*i64)` or `x.(i64)`.
The shape word encodes each pointer level's mut/nullable bit and, for arrays,
the length (up to 8191; a longer array is a clean compile error).

### Type switch

A type switch dispatches on the dynamic type of an interface value. The head
binds `v` to the switched value; each case narrows `v` to the case's type inside
its body. Cases are top-down, first match wins, no fallthrough; `default` is
optional, and a no-match with no default is a no-op.

```
switch (v := args[i].(type)) {
    case i64       { use_int(v) }
    case *mut i64  { use_ptr(*v) }
    default        { use_unknown() }
}
```

### Interface-to-interface assertion

`x.(I)` where `I` is an interface asks "does the concrete value inside `x`
*also* satisfy interface `I`?" — answered at runtime. Type switches accept
interface cases (`case SomeIface`) alongside concrete cases, with the same
top-down first-match-wins ordering. Unlike a *concrete*-target assertion
(`x.(T)` → `multiretu{T, bool}`), an interface-target assertion yields a single
**nullable interface `?I`**: on success it shares `x`'s data word verbatim and
carries a vtable the runtime built for the `(typedesc, shape, I)` triple; on
failure its vtable is `0` (null). The maybe-ness is in the type — there is no
separate `ok` flag — so the result must be narrowed before use:

```
var k ?Stringer := x.(Stringer)
if (k != nil) { puts(k.string()) }   // narrowed to Stringer; dispatch allowed
```

The lowering loads `(typedesc, shape_word, data)` from the source fat pointer
and calls `_iface.assert_to(src_ti, src_shape, src_data, dst_desc)` (the helper
returns the itab vtable in `rax`, `0` on failure; the assertion stores it
directly as the result's vtable, so no separate success flag is needed). The helper walks
the source typedesc's lazy **itab cache** keyed by `(iface_desc, shape)`: a hit
returns the cached vtable in O(1); a miss allocates one itab via `_heap.alloc`,
fills each required method by a two-axis lookup over the typedesc method table
(name + non-receiver-signature hash, byte-equal name/sig text, and a
shape-derived receiver-shape match), links the itab into the cache, and returns
its vtable. A method that can't be resolved (or a receiver-shape mismatch)
frees the itab and returns `(nil, false)` — misses are not cached. The
runtime's `expected_receiver_shape(src_shape)` derivation and signature
canonicalization match the compile-time coercion filter exactly, so a value-
backed `any` refuses a `*self`-requiring interface and vice versa.

The `_iface` package is not a source-level import; the compiler emits a bare
`call _iface.assert_to` and the build always links `_iface.bo` (reachability
drops it when unused), mirroring how `_heap` is pulled in.

The static data backing all this:

- **`__typedesc_<T>`** (read-only `data`, home-package-emitted) — the structured
  typeinfo: name string, size, a relocation to its paired cache slot, and a
  method table sorted by `(name_hash, sig_hash, receiver_shape)`. Each entry
  carries the method name text, the canonical non-receiver signature text, both
  64-bit FNV-1a hashes, the receiver shape, and a relocation to the method body.
- **`__typedesc_cache_<T>`** (writable `var`, home-package-emitted alongside the
  head) — an 8-byte zero-initialized slot holding the head of the itab cache
  list. bas rejects a `typedesc` that lacks its paired `typedesc_cache` in the
  same object file.
- **`__iface_desc_<I>`** (read-only `data`, home-package-emitted) — the
  assertion-time companion: name string and a required-method table sorted by
  `(name_hash, sig_hash)`, each entry carrying the method name text, canonical
  non-receiver signature text, both hashes, and the method's declaration index
  (used to place the resolved fn at the correct itab vtable slot for dispatch).
  `__iface_desc_any` has zero methods; the helper allocates a 2-slot itab and
  preserves the `[typedesc, shape_word]` slot invariants.

There is exactly one `__typedesc_<T>` (and one `__iface_desc_<I>`) per declared
base type / interface, emitted by its home package; cross-package uses are
relocations. The linker's existing duplicate-rejection enforces the single-
emitter invariant — no new linker semantics. `bdump` pretty-prints all three
record kinds.

---

## File-Scope Declarations

Bindings work at file scope the same as inside functions:

```
var counter i64                  // zero-initialized
var answer  i64 := 42             // primitive literal
var label   byte := 'Z'
var buf     [100]byte            // zero-filled fixed array

greeting []byte := "hi\n"   // slice over a string constant
```

File-scope declarations are visible to every function in the package, regardless of source order. They are forward-bindable: a function defined earlier in the file can call into a global declared later. This is consistent with how cross-function calls work and is implemented by an early pass that registers all top-level names before any body codegen happens.

### Static Initializers

Initializers on file-scope variable declarations must be **compile-time constants** in the bosc-internal sense: the encoder must be able to produce the value's bytes and any pointer relocations at compile time, with no runtime code.

The supported forms compose recursively:

- Integer literals (any width, any signedness; negative via the `0 - N` parser shape).
- Byte literals.
- String literals into `byte[N]` (inline copy, zero-padded).
- String literals into `[]byte` (24-byte slice header ({ptr, len, cap}) with a relocation to a string constant in `.data`).
- Struct literals (each field encoded recursively, byte offsets concatenated, relocations shifted into the outer struct's coordinates).
- Array literals `[e1, e2, …]` into `[N]T` (element-by-element encoding, length must match) or into `[]T` (anonymous backing array `T[len]` queued via the anonymous-globals path, plus a 24-byte slice header ({ptr, len, cap}) with a relocation to it).
- `&someGlobal` — 8-byte pointer slot with a relocation to the named global.
- `&SomeStruct{...}` — recursively encodes the inner struct into a fresh anonymous global (`__static_0`, `__static_1`, …) and emits a pointer slot relocated to it.
- `&someGlobalArr[N]` for compile-time-constant N — a single relocation to the array's symbol with `Addend = N * elementSize`. Pointer-into-array without any auxiliary storage.
- Type-alias casts of a literal integer — `var fd FD := FD(3)` (or the qualified form `io.FD(3)`). The encoder evaluates the inner literal at compile time and writes the resulting bytes into the alias's slot.
- `&someGlobal` (or `&SomeStruct{...}`) assigned to an **interface-typed** global — a 16-byte fat pointer `[data, vtable]` with *two* relocations: the data word reuses the pointer-data encoding above (so the data target resolves the same way a plain pointer global would), and the vtable word relocates to the per-`(type, shape, interface)` vtable global. The vtable symbol is named and registered for emission by the same helper the runtime coercion path uses, so a statically-built interface and a runtime-built one share one vtable. Interface-to-interface conversion and value-backed interfaces in static init are not supported (use a pointer source / assign at runtime).

Anything else (function calls, runtime expressions, type mismatches, length mismatches) produces a specific diagnostic at compile time.

**Pointer fields in static structures** work naturally because the encoder is recursive and `Var.Relocs` is per-var:

```
type config struct {
    threshold i64
    current   *i64       // pointer field
    greeting  []byte     // slice field
}

var counter i64 := 0
var cfg config := config{
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

Produces a flat token stream from Boson source. Recognizes keywords (`fn`, `var`, `mut`, `owned`, `dispose`, `type`, `struct`, `interface`, `if`, `else`, `for`, `return`, `break`, `continue`, `import`, `package`), identifiers, integer literals (decimal, `0x`, `0o`, `0b`), string and character literals (with `\n`, `\r`, `\t`, `\0`, `\\`, `\"`, `\'`, `\xHH` escapes), and all operators.

Integer literals are parsed as `uint64` for full-precision representation; downstream stages decide the final type. A literal carrying a decimal point or an exponent is scanned as a floating-point literal instead and parsed as a `float64`.

Positions (file, line, column) are recorded on every token for error messages.

The lexer also performs **Go-style automatic semicolon insertion**: on a newline whose preceding token can end a statement (identifiers, literals, `)`, `]`, `}`, `break`/`continue`/`return`) and where paren/bracket nesting depth is zero, it synthesizes a `tok_semicolon` before continuing. This makes the parser's statement-boundary logic uniform and fixes a family of bugs where a unary `*` or `-` at the start of a statement would be greedily consumed as a binary operator on the previous statement. See [Statement boundaries](#statements).

### Parser (`parser.go`)

Recursive descent. Builds an AST from the token stream. Operator precedence is handled by structured recursive calls (`parseAddSub`, `parseUnary`, `parseSubexpr`, etc.). Produces positioned AST nodes so errors downstream can report source locations.

Statement-separating `;` tokens (typically auto-inserted by the lexer; see [Lexer](#lexer-lexergo)) are skipped uniformly at the top level, inside block bodies, between struct field decls, and inside struct literals. `for`-loop headers expect them explicitly (the existing `(init; cond; step)` form).

Postfix chains compose: `a.b[i].c[lo:hi]` is parsed by a single `parsePostfix` loop that handles `.member`, `[index]`, `[lo:hi]`, `(args)`, and `{fields}` uniformly on any prior value. The `pkg.fn(args)` and `pkg.Type{fields}` forms are recognized by the same loop (Dot-of-symbol followed by `(` or `{`).

### AST (`ast.go`)

Defines all node types: declarations (package, import, struct, function, type alias, dispose, owned-promotion), statements (binding declaration, assign, if, for, return, break, continue, expression), and expressions (binary op, unary op, call, qualified call, cast, index, slice, field access, literal, identifier, address-of, deref).

Defines `ASTType` with `Name`, `Indirection`, `ArraySize`, `Element`, `Signed`, `MutMask`, `OwnedMask` fields. A slice/array type is identified by `Element != nil` (with `ArraySize > 0` distinguishing array from slice); pointer/scalar types use `Name` and `Indirection`. Provides equality (`Same`), compatibility (`MutCompatible`, `OwnedCompatible`), and stringification. `Same` is structural — `ASTType` carries a pointer (`Element`) so `==` comparison is unsafe.

`Address` has two forms: `{Var string}` for address-of-named-variable, and `{Lit AST}` for address-of-literal (used in static-init context).

The `Context` type carries name resolution state: variable bindings (with a mutability flag — immutable by default, `var` for mutable — and moved/owned state), struct declarations, function declarations, imports (per-package namespace), type aliases, file-scope address-backed names (`addressNames`), pending anonymous-global emissions (`anonGlobals`), and the current package name. Helpers like `NameIsAddress(name)` consult both the explicit globals set and the type-based `typeIsMemoryBacked(t)` predicate to decide whether codegen should treat a name as an address (load through it) or as a value.

### Importcfg loading (`main.go`)

Before parsing any source file, bosc reads the `-importcfg=<file>` flag to obtain a mapping from import keys to `.bo` paths. The package driver (`package.go`) parses all input files, verifies their package declarations, and loads imported `.bo` metadata once into a shared package Context. Each file has a separate source namespace mapping its import names (or explicit aliases) to canonical package names. Type syntax is qualified in that namespace before AST conversion; function compilation and return-alias analysis use the defining file's namespace. Package declarations are registered across all files before code generation begins.

A `-listimports` mode prints just the import keys declared by the input files and exits. Used by the build system to discover transitive dependencies before compiling.

### Code Generator (`compile.go`)

Walks the AST and emits `.bs` assembly text. Key responsibilities:

- **Locals**: Each binding declaration emits a `local` (for scalars and pointers) or `bytes` (for structs and arrays) directive in the assembly. The choice is driven by `typeIsMemoryBacked(t)` — true for structs and values larger than 8 bytes, false for everything else (including pointers regardless of pointee size). The assembler's register allocator handles actual placement.
- **File-scope vars**: Top-level binding declarations go through `emitGlobalVarDecl` rather than the local path. Initializers are encoded at compile time (see [Static Initializers](#static-initializers)). The bas-level emission is the `var name type N` size form (zero-init), `var name type "..."` string-literal form (no relocations), or the block form `var name type { bytes "..." reloc <off> <sym> <addend> ... }` when any pointer fields require fixups.
- **Spots and addressing**: The compiler's intermediate `spot{ref, t, nameIsAddress}` records both the bas-level name and whether that name resolves to a value (`local`-allocated scalar) or to a memory address (`bytes`-allocated chunk, or file-scope global). Indirection sites (`*p`, `arr[i]`, `slice[lo:hi]`) consult `spot.nameIsAddress` to decide whether they need to materialize the address into a register before further indirection. This abstracts away the distinction between register-resident and memory-resident sources — the same codegen path handles both.
- **Control flow**: `if`/`else` and `for` lower to compare-and-jump sequences with generated labels (`_LABEL_for_N`, `_LABEL_break_N`, `_LABEL_cont_N`, `_LABEL_return_N`).
- **Function calls**: Arguments are evaluated into temporaries, then moved into the ABI argument registers before `call`. The emitted `call` is always fully qualified (`pkg.fname`); for in-package calls the current package's name is prepended. Function-defined-in-source signatures are emitted as `type fn(arg-types) ret` directives so cross-package import works for Boson-defined packages, not just hand-written bas runtime packages.
- **Address-of**: For `&x` where `x` is a named variable, the assembler `volatile x` directive is emitted (suppressed for memory-backed names where it's already true) to mark `x` as memory-resident, then `lea`. For `&arr[i]` and `&s.field` at runtime, the compiler delegates to the lvalue-walk machinery which already produces an address-bearing spot. For a function-scope `&"literal"`, a static slice header is synthesized once per distinct literal and the address `lea`'d. Any other `&literal` at runtime is an error; those forms are only valid in static-init.
- **Struct access**: Field offsets are computed at compile time. Pointer-to-struct access auto-dereferences. For a small inner struct accessed via dot (e.g. `outer.middle.field`), the inner Dot returns a pointer to the struct rather than copying its bytes, so subsequent Dot/Index walks land on the right address.
- **Slices**: A slice is two words (pointer and length) — there is **no** capacity word; a slice knows only the region `[ptr, ptr + len*element_size)`. Indexing bounds-checks against the length word and computes `base + index * element_size`. Slice operations adjust both pointer and length, and bounds-check the operation itself: `s[lo:hi]` requires `0 <= lo <= hi <= N`, where `N` is the *operand's own length* — `len(s)` (the length word) when slicing a slice, the declared size when slicing a fixed array. A present `hi` is checked against `N`, a present `lo` against `hi`. Because a slice carries no capacity, re-slicing can never extend past `len` (unlike Go, where `hi` may reach `cap`): the length word is the only valid region the slice can prove, so it is the only safe upper bound. For a fixed array `len == N == cap`, so the distinction is invisible there.
- **Bounds checking**: Array/slice *indexing* and slice *operations* (`s[lo:hi]`) both emit a compare and a conditional call to `_init.index_oob` when out of range. The runtime compares are unsigned, so a negative index or bound (enormous as `u64`) also traps. A *constant* index past a fixed array's size is caught earlier, at compile time; a constant *negative* index currently still traps at runtime rather than at compile time (it is decidable at compile time and is a candidate to hoist into an earlier phase).
- **Casts**: Type casts (`FD(x)`, `i64(fd)`) generate sign-extend, zero-extend, or truncating moves as appropriate. Widening 32→64 unsigned uses the synthetic `MOVZX r64, r/m32` form (translated by bas); widening 32→64 signed uses `MOVSXD`; narrowing uses partial-of-alloc syntax (`mov dest src:N`).
- **Multiplication / division**: 64-bit signed multiplication uses the two-operand `IMUL r64, r/m64` form, avoiding any `inreg` on user variables. Sub-64-bit signed multiplication and all unsigned multiplication/division route through fresh rax-pinned temps so the `inreg` constraints fall on temporaries, not on user-declared variables (which may be `volatile`).
- **Temporaries**: The compiler allocates temporaries as locals with names like `Temp_1`, `Temp_2`. The assembler's register allocator places these in registers or spills them to memory.
- **Register-scaled indirection through globals**: x86-64 RIP-relative addressing has no `[symbol + reg*scale]` form. When `arr[i]` indexes a name-is-address base with a runtime index, the compiler emits `lea tmp symbol` first to materialize the address into a register, then uses a normal `[reg + idx*scale]` SIB form off the temp.
- **Diagnostics**: Positioned errors include a source-context snippet — five lines centered on the offending position, with an arrow pointing at the column. The arrow is rendered in red ANSI when stderr is a TTY (plain otherwise so captured output stays clean).

### Ownership tracking

The compiler tracks per-binding move state in `Context.movedBindings`. Each `Funcall` whose parameter type contains `owned` marks the argument variable as moved. References to moved variables are compile errors.

Pointer alias state is kept separately in `cmd/bosc/flow` (including derived-loan
snapshots in `borrow.go`). `Context` owns a flow state and snapshots it with other control-flow facts. The flow model records pointer origins (which local object a pointer aliases), pointer-slot targets (`&alias`), unknown pointers, branch merges, and invalidation results. `compile.go` converts AST expressions into flow operations such as pointer assignment, store-through-slot, direct mutable write, and opaque `*mut` call; owned-field facts remain in `Context` and are invalidated from the flow result.

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
| `retaliases` | `retaliases <slot>: <idx> ...` | Records a return alias set: return slot `<slot>` may alias the parameters at the listed indices (receiver = 0). As a standalone directive it carries a *function's* inferred set (parsed into `Function.ReturnAliases`); inside an `interface` method block it carries an interface method's *declared* `from(...)` contract (parsed into `InterfaceMethodShape.ReturnAliases`). Emitted per non-empty slot; serialized through the `.bo` so cross-package borrow tracking and ⊆ conformance extend across the boundary. Absent ⇒ all slots alias nothing. |
| `data` | `data name type "..."` | Global immutable data (e.g., string constants emitted by bosc). Stored in `o.Data`. |
| `var` | `var name type "..."` | Global writable data (string-literal payload form). Stored in `o.Vars`. |
| `var` (size) | `var name type N` | Global writable data, N zero-filled bytes (uninitialized). |
| `var` (block) | `var name type { bytes "..." reloc <off> <sym> <addend> ... }` | Multi-line form: explicit bytes payload plus zero or more per-var data relocations. Used by bosc to emit globals containing pointers (slice headers, struct fields holding addresses, anonymous-globals-as-pointers). |
| `struct` | `struct Name { fname ftype \n ... }` | Multi-line declaration carrying a Boson struct shape into the `.bo`. Field types are stored verbatim; bosc reparses them on import. Used for cross-package struct types. |
| `typealias` | `typealias Name underlying [m1 m2 ...]` | Single-line declaration carrying a Boson type alias into the `.bo`. The method-name list lets bosc reconstruct the type's method table on import from the already-imported function set. |
| `interface` | `interface Name { method m1 { param p t \n ... \n return rt \n retaliases <slot>: <idx>... } ... }` | Multi-line declaration carrying a Boson interface shape into the `.bo`. Each method's params and return type are reparsed by bosc on import; optional `retaliases` lines carry the method's declared `from(...)` borrow contract. Used for cross-package interface types. |
| `typedesc` | `typedesc Name { name "..." \n size N \n cache_ref <sym> \n method <name> <sig> <name_hash> <sig_hash> <recv_shape> <fn_reloc> [<slot_mask>...] \n ... }` | Multi-line declaration of a structured typeinfo record (read-only, `o.Data`). Carries the type name string, size, a relocation to its paired cache slot, and a method table. Optional trailing per-slot `<slot_mask>` tokens (u64 bitmasks) are the method's *inferred* borrow descriptor, read by `_iface.assert_to`'s ⊆ gate. bas serializes the fixed binary layout and emits the `cache_ref` and per-method `fn_ptr` relocations. Must be paired with a same-named `typedesc_cache` in the same `.bo` (bas errors otherwise). |
| `typedesc_cache` | `typedesc_cache Name` | A bare 8-byte zero-initialized writable slot (`o.Vars`) holding the head of a type's lazy itab-cache list. One per `typedesc`, named in lockstep. |
| `iface_desc` | `iface_desc Name { name "..." \n method <name> <sig> <name_hash> <sig_hash> <decl_idx> [<slot_mask>...] \n ... }` | Multi-line declaration of the assertion-time interface descriptor (read-only, `o.Data`). Carries the interface name and a required-method table (name/sig text, both hashes, and the method's declaration index for itab dispatch ordering). Optional trailing per-slot `<slot_mask>` tokens are the method's *declared* `from(...)` borrow descriptor — the ceiling `_iface.assert_to` checks each impl mask against. |
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

The calling convention is one part of the Boson ABI, specified in full in
[The Boson ABI (BABI-0)](#the-boson-abi-babi-0). In brief: integer and pointer
arguments go in RDI, RSI, RDX, RCX, R8, R9; floating-point arguments go in
XMM0–XMM7; the two sequences are **counted independently**; scalar returns come
back in RAX (XMM0 for floats) and aggregate returns as an address in RAX.
Callee-saved is RBX, RBP, R12–R15; everything else, including every XMM
register, is caller-saved.

### Symbol qualification

The assembler stamps each function with its file's `package` name (`Function.Pkgname`). At resolve time, any bare relocation symbol (cross-function calls, jumps that aren't local labels, global var references) is automatically qualified with the function's package name. This means hand-written `.bs` files can use bare names internally (`call strlen`) and the assembler turns them into `string.strlen` (or whatever the package is) automatically. Cross-package calls use the full qualified form in source: `call other.func`.

Labels (jump targets declared with `label`) remain unqualified — they're function-scoped and never become relocations.

---

## The Boson ABI (BABI-0)

This section specifies the Boson Application Binary Interface as currently
implemented: data representation, calling convention, stack frame layout,
symbol naming, and the runtime entry and helper contracts. It is **normative
and self-contained** — an implementer should be able to produce object code
that interoperates with `bosc` output without consulting any other document.

BABI-0 is a **versioned snapshot, not a permanent commitment.** It describes
what the toolchain does today. The `-0` exists so a future revision — most
likely one that reconciles the divergences in
[Relationship to System V](#relationship-to-system-v) — can be named BABI-1
and the two discussed without ambiguity. Nothing here is guaranteed stable
across a version bump; there is no cross-version compatibility promise, and
objects are expected to be rebuilt together.

Where the current implementation is known to be **wrong** rather than merely
different, this section says so and links the tracking item, rather than
describing the bug as if it were the design.

**Platform.** x86-64 Linux, little-endian throughout. Every multi-byte scalar,
every header word, and every static initializer is little-endian; no other
byte order is supported or contemplated.

### Data representation

#### Scalars

| Type | Size (bytes) |
|------|--------------|
| `i8`, `u8`, `byte`, `bool` | 1 |
| `i16`, `u16` | 2 |
| `i32`, `u32`, `f32` | 4 |
| `i64`, `u64`, `f64` | 8 |
| any pointer, function pointer | 8 |

`bool` occupies one byte: `true` is `1`, `false` is `0`. No other bit pattern is
produced by the compiler, and none is given meaning.

Floating-point values are IEEE-754 binary32 (`f32`) and binary64 (`f64`).

#### Alignment

**Every value is naturally aligned.** A type's alignment is the address
granularity at which a value of that type must be placed:

| Type | Alignment |
|------|-----------|
| `i8`, `u8`, `byte`, `bool` | 1 |
| `i16`, `u16` | 2 |
| `i32`, `u32`, `f32` | 4 |
| `i64`, `u64`, `f64` | 8 |
| pointer, function pointer | 8 |
| slice, interface | 8 (each is a pair of 8-byte words) |
| `values` | 8 (leads with an `i64` tag) |
| `[N]T` | alignment of `T` |
| struct | the strictest alignment among its fields |

**8 is the maximum alignment in BABI-0.** Nothing in the language is wider
than 8 bytes at scalar level, so no object ever requires more. An
implementation that unconditionally 8-aligns every aggregate is conformant,
merely wasteful.

It is also the language's alignment *ceiling*, not just the requirement, and
where the guarantee comes from now differs by storage class. **Stack** storage
tops out at 8: RBP is 8-aligned but not 16 (a callee enters at RSP ≡ 8 mod 16
and the prologue subtracts 48 plus a multiple of 16 plus 8), so the
16-byte-aligned SSE forms — `movaps`, `movdqa` — remain unusable against a
stack slot. **Heap** storage is different since the allocator rewrite: every
`_heap.alloc` result is 16-byte aligned (small objects start at a
`SPAN_SIZE`-aligned span base past a header-free region; large and huge tiers
are span- or page-aligned), so those SSE forms *are* usable against
heap-resident data. Note the type system still assumes an 8-byte ceiling, so
nothing emits them yet. What natural alignment does deliver is correct scalar access,
C-compatible struct layout, atomicity for naturally-aligned accesses up to 8
bytes, and correctness on architectures that fault on unaligned access. Raising
the ceiling to 16 would mean 16-aligning RBP and widening the heap header; see
[`TODO.md`](TODO.md).

**Struct layout.** Fields keep declaration order and are never reordered —
only padded. Each field begins at the next offset satisfying its own
alignment, and the struct's total size is rounded up to the struct's own
alignment, so consecutive elements of an array stay aligned:

```
struct { a u8; b u32; c u8; d u64 }

offset  0   a (1 byte)
        1-3     padding
        4   b (4 bytes)
        8   c (1 byte)
        9-15    padding
       16   d (8 bytes)
size   24
```

This is the same layout a C compiler produces for the equivalent
declaration — deliberately, because struct layout is the one part of an ABI
that no amount of caller-side work can paper over.

**Alignment is a property of addresses, not just offsets.** A field's offset
being aligned only yields an aligned *address* if the aggregate's base is
aligned too, so every storage class carries the guarantee:

- **Stack slots** are allocated at aligned offsets from RBP. RBP itself is
  8-aligned (a callee enters with RSP ≡ 8 mod 16 and the prologue's six pushes
  subtract 48), so an offset that is a multiple of A yields an A-aligned
  address for any A ≤ 8.
- **Globals and data blocks** are padded into place by the linker, and the
  containing ELF section is itself 8-aligned.
- **Heap allocations** come from `_heap.alloc`, which returns 16-byte-aligned
  storage (the allocator's smallest size class is 16 and object regions start
  at a span boundary), so the heap over-satisfies the 8-byte ceiling.

An implementation that computes aligned offsets but places aggregates at
arbitrary addresses is **not** conformant: every field in such an aggregate is
misaligned, which is precisely the guarantee the offsets were computed to
provide.

**There is no packed representation and no `packed` attribute.** A program that
needs a byte-exact wire format must serialize explicitly rather than casting a
struct.

#### Slices

A slice `[]T` is a 24-byte header, independent of `T`:

| Offset | Width | Field |
|--------|-------|-------|
| 0 | 8 | pointer to the first element |
| 8 | 8 | length, in elements |
| 16 | 8 | capacity, in elements |

The **capacity** field records how many elements the backing can hold from the
pointer onward. It is populated by every header-construction site (a reslice
carries `source_cap - lo`; a literal/string uses `cap = len`) but is **not yet
consumed** by any operation — `make`, `append`, and `pop` are future work (see
[`docs/reference/AUDIT_slice_representation.md`](docs/reference/AUDIT_slice_representation.md)
and the owned-slices proposal). Slices are always memory-backed — a slice value
lives at an address, and passing one passes that address (see
[Aggregates](#aggregates-structs-arrays-slices-interfaces)).

#### Strings

A string is exactly a `[]byte`: the same 24-byte header, no distinct type. A
string *literal* additionally guarantees a NUL byte immediately past the end of
its backing bytes; that NUL is **not** counted in the length. This makes a
literal directly usable as a C string without a copy, while `len` still reports
the Boson length. A non-literal `[]byte` carries no such guarantee.

The compiler emits literal backing bytes into the object's data section
(immutable) rather than its var section (writable), and deduplicates identical
literals.

#### Fixed arrays

`[N]T` is `N` elements of `T`, contiguous, no padding, no header. Size is
`N * sizeof(T)`. The length is a compile-time property of the type, so there is
nothing to store. A fixed array is **always memory-backed regardless of size** —
it needs a stable address for indexing — which is why even a 1-byte array
behaves as an aggregate rather than a scalar.

#### Interfaces

An interface value is a 16-byte fat pointer, independent of method count:

| Offset | Width | Field |
|--------|-------|-------|
| 0 | 8 | data word |
| 8 | 8 | vtable pointer |

The data word has two interpretations, fixed at the coercion site:
**pointer-backed** (the data word is a pointer to the concrete value) or
**value-backed** (the data word holds the concrete value's bits directly). A
value-backed `f32` occupies only the low 4 bytes, so an assertion back to `f32`
reads exactly the bytes that were written.

A nil interface is the all-zero fat pointer; nil-ness is tested on the **vtable
word**, not the data word.

The vtable is a static, compiler-generated table:

| Offset | Contents |
|--------|----------|
| 0 | pointer to the concrete type's typedesc |
| 8 | shape word (u64) |
| 16 + 8·i | pointer to method *i*, in the interface's declaration order |

Method dispatch loads `[vtable + 16 + 8*i]` and calls it with the interface's
**data word as the first argument** (RDI), followed by the method's own
arguments. No thunks exist: a concrete method whose receiver is `*T` receives
the data word directly and interprets it as `*T`.

Interface-to-interface assertion builds an equivalent table on the heap at
runtime (an *itab*) whose vtable region has this same layout, so the dispatch
sequence is identical whichever provider produced the table.

#### Values types (tagged unions)

A `values` type is `{ tag : i64 @0 } ++ { payload }`, where the payload begins
at offset 8 and is sized to the largest variant's payload. The tag is
dense from 0 in declaration order. A `values` type with no payload variants is
a bare `i64` tag and is **not** memory-backed — it lives in a register like any
scalar.

#### Type qualifiers have no representation

`owned`, `mut`, and `?` (nullability) are compile-time qualifiers only. They
occupy no bits, change no size, and add no field. Two types differing only in
these qualifiers are bit-identical at runtime. The only runtime traces are
inserted *behavior* — a moved nullable pointer's source binding is zeroed, a
nil check calls `_init.nil_assert` — never a change in layout.

### Function calls

#### Argument classification

Each argument has a class, and each class has its own register sequence and its
own counter:

| Class | Registers | Members |
|-------|-----------|---------|
| INTEGER | RDI, RSI, RDX, RCX, R8, R9 | integers, `bool`, `byte`, pointers, function pointers, **and all aggregates** |
| SSE | XMM0–XMM7 | `f32`, `f64` |

**The counters are independent.** An argument's register comes from its
position among arguments *of its own class*, not its position in the argument
list. In `fn f(p i64, q f64, r i64, s f64)`, `p`→RDI, `r`→RSI, `q`→XMM0,
`s`→XMM1.

**Class is a property of raw scalars only.** An aggregate is INTEGER-class no
matter what it contains: a `struct { a f64; b f64 }` travels by address in an
integer register, never in XMM. BABI-0 has no per-eightbyte classification.

#### Scalar arguments

A scalar narrower than 8 bytes is **extended to the full 64-bit register** —
sign-extended if the type is signed, zero-extended otherwise. BABI-0 therefore
guarantees the upper bits of an argument register are defined, which is
stricter than System V (where they are unspecified). Callees read the
sub-register anyway, so the extension is defensive rather than load-bearing.

#### Aggregates: structs, arrays, slices, interfaces

Aggregates are passed **by value with caller-made copies**, in one of two forms
selected by size:

- **Size 1, 2, 4, or 8** — the caller loads the aggregate's bytes into the
  argument register and passes them by value. Sizes 1 and 2 are *zero*-extended
  regardless of any field's signedness, since the register holds opaque
  aggregate bits rather than a number.
- **Size greater than 8** — the caller allocates a fresh stack slot, copies the
  aggregate into it, and passes the **address of that copy**. The callee reads
  through the pointer. By-value semantics are preserved because the callee
  never sees the caller's original storage.

Slices (24-byte `{ptr, len, cap}`) and interfaces (16-byte `{data, vtable}`) are
memory-backed aggregates and take the second path: a callee receives a *pointer*
to the header. **A slice header is never split across registers.**

**Sizes 3, 5, 6, and 7 are rejected at compile time** rather than passed. A
by-value aggregate of one of those sizes — `struct { a i8; b i16 }`, for
instance — is a compile error directing the author to use a pointer or a
1/2/4/8-byte layout. This is an implementation limit, not a design decision;
see [`TODO.md`](TODO.md).

#### Arguments past the registers

INTEGER-class arguments past RDI, RSI, RDX, RCX, R8, R9 are **passed on the
stack**, as System V specifies. Argument *i* > 5 lives at
`[RBP + 56 + 8·(i-6)]` in the callee — just above the return address at
`[RBP + 48]`, which sits above the six registers the prologue saves.

Because the first stack argument occupies the lowest address, the caller pushes
them in **reverse** parameter order; the `call` then pushes the return address
below all of them. Each slot is 8 bytes regardless of the parameter's width, so
a narrower value is widened (`movsx`/`movzx`) before being pushed and the callee
names only the slot's low bytes.

Stack arguments are the caller's to reclaim — the callee does not pop them. And
since each is an 8-byte push while BABI-0 requires `RSP ≡ 0 (mod 16)` at the
`call`, an **odd** number of stack arguments is preceded by one 8-byte padding
slot. Neither of these is observable in a function's return value, so both are
pinned directly: `tests/stack_args_test.bos` measures callee-entry alignment
through a 7- and an 8-argument call, and checks that RSP is unchanged across a
loop of such calls.

**SSE-class arguments past XMM7 remain a compile error.** The callee side has no
path for them either (`Function.ArgFloat`), so accepting them would emit a call
the callee cannot read. See [`TODO.md`](TODO.md).

#### Return values

| Return type | Location |
|-------------|----------|
| `f32`, `f64` | XMM0 |
| scalar ≤ 8 bytes | RAX (as AL/AX/EAX/RAX by width) |
| pointer, function pointer | RAX |
| struct, array, slice, interface, multi-value | **address** in RAX |

A 1- or 2-byte scalar return is explicitly sign- or zero-extended into the full
RAX; a 4-byte return relies on x86-64's implicit zeroing of the upper half when
a 32-bit register is written.

**Multi-value returns are aggregates.** `fn f() i64, i64` is lowered to an
anonymous struct with positional fields `_0`, `_1`, … laid out by the ordinary
layout rules, and returned by the aggregate path. Nothing about multi-value
return is special at the ABI level, which is why it crosses package boundaries
exactly as a struct does.

**The returned address points into the callee's own frame**, which the epilogue
has already released by the time the caller sees it. The caller must copy the
result out before doing anything that pushes or calls; `bosc` emits that copy
immediately after the `call`. This is a real hazard rather than a convention —
the storage is below the caller's RSP and can be far beyond the 128-byte red
zone, so it is not even signal-safe — and it is **not** System V's MEMORY-class
protocol, which would have the caller pass a hidden result pointer in RDI. See
[`TODO.md`](TODO.md).

Note the deliberate asymmetry: an aggregate of size ≤ 8 is *passed* by value in
a register but *returned* by address. There is no small-aggregate return-by-value
path.

#### Register preservation

| Class | Registers |
|-------|-----------|
| Callee-saved | RBX, RBP, R12, R13, R14, R15 |
| Caller-saved | RAX, RCX, RDX, RSI, RDI, R8–R11, **and every XMM register** |

No XMM register is callee-saved, so every live floating-point value is spilled
across every call.

### Stack frame

The `prologue` directive emits exactly:

```
push rbp; push rbx; push r12; push r13; push r14; push r15
mov  rbp, rsp
sub  rsp, <frame size>          ; multiple of 16, minimum 16
```

and `epilogue` reverses it:

```
mov rsp, rbp; pop r15; pop r14; pop r13; pop r12; pop rbx; pop rbp
```

Two consequences that an implementer will otherwise get wrong:

- **All six callee-saved registers are saved unconditionally**, whether or not
  the function uses them. Every call pays 6 pushes and 6 pops.
- **RBP is established *after* the pushes**, so it does **not** point at a
  saved-RBP frame chain node. `[RBP]` holds the saved R15 and the return
  address is at `[RBP + 48]`. A debugger or unwinder cannot walk Boson frames
  by following RBP.

Locals live at **negative offsets from RBP**, each at an offset satisfying its
own alignment (see [Alignment](#alignment)),
allocated by a downward byte cursor. Freed slots are returned to a free list
and reused only on an exact size match, so local offsets are not monotonic in
declaration order. Nothing is stored below RSP: the red zone is neither used
nor relied upon for locals.

#### Stack alignment

RSP is 16-byte aligned immediately before every `call`, so a callee sees
`RSP % 16 == 8` on entry (the call pushed the return address).

The prologue is what establishes this. Its six pushes are 48 bytes — 0 mod 16 —
so the frame reservation supplies the missing 8: locals are rounded up to a
multiple of 16 and then 8 is added, making the prologue's net displacement
≡ 8 (mod 16).

A prologue that instead subtracted a plain multiple of 16 would *preserve* the
caller's alignment residue rather than establish a known one, leaving call-site
alignment alternating with call depth. That was the behavior before this rule
was written down, and it is the failure mode to check for when porting the
prologue to another target.

### Symbol naming

All symbols in a `.bo` object are fully qualified. There is no name mangling of
types or signatures into symbol names — overloading does not exist, so a name
plus a package is unique.

| Form | Meaning |
|------|---------|
| `pkg.name` | a package-level function or var |
| `pkg.Type.method` | a method on `Type` |
| `pkg.__typedesc_<Type>` | type descriptor for `Type` |
| `pkg.__typedesc_cache_<Type>` | 8-byte itab cache cell for `Type` |
| `pkg.__vtable_<Type>_<shape>__<Iface>` | static vtable for `Type` (at pointer shape `shape`) satisfying `Iface` |
| `pkg.__projection_<Type>__<i>` | projection table *i* of a `values` type |
| `pkg.__static_<N>` | anonymous global from an `&`-of-literal static initializer |
| `pkg.__bstr<N>` / `pkg.__bstrslice<N>` | string literal backing bytes / slice header |

Dots inside a type name are rewritten to underscores when it appears inside a
generated symbol, so a cross-package type produces a flat identifier.

In hand-written `.bs`, a bare symbol is automatically qualified with the file's
`package` at assemble time — `call strlen` in `package string` resolves to
`string.strlen`. Cross-package references must be written qualified. Labels are
function-scoped and never become symbols.

### Process entry

The entry point is `_init.start`. It runs on the raw kernel stack with no
prologue and never returns.

The kernel-supplied stack at entry:

```
[rsp+0]          argc
[rsp+8 ...]      argv[0..argc-1]   (each a NUL-terminated *byte)
[rsp+8+8*argc]   NULL
                 envp..., auxv..., strings
```

`start` converts that into Boson's `[][]byte`: it carves an inner array of
`argc` 24-byte `{ptr, len, cap}` headers plus one outer 24-byte `{data, len, cap}`
header from the stack (cap = len at both levels), walks `argv` until the NULL
terminator calling `strlen` on each entry, then calls `main.main` with the
**address of the outer header**
in RDI — the ordinary aggregate-argument convention. It zeroes RAX first for
hand-written entry functions. The compiler also clears RAX in the common
epilogue of a void `main`, so fallthrough and explicit bare `return` both exit
0 regardless of preceding calls. An explicit value returned by a non-void
`main` is preserved. Startup issues `exit(main's RAX)` via syscall 60.

A `main` declared without parameters is called the same way; it simply ignores
RDI. The argv slices live on the initial stack and remain valid for the
process lifetime.

### Runtime helper contracts

These are the compiler-inserted calls. They are part of the ABI because
compiler output depends on their exact contracts.

| Helper | Signature | Contract |
|--------|-----------|----------|
| `_init.index_oob` | RDI = offending index, RSI = length | **Never returns** — prints and exits 1 |
| `_init.nil_assert` | no arguments | **Never returns** — prints and exits 1 |
| `_heap.alloc` | RDI = size → RAX = `*mut byte` | Normal convention. Returns 16-byte-aligned, zeroed storage from the Boson allocator (`runtime/_heap/heap.bos`); 0 on failure |
| `_heap.free` | RDI = pointer from `alloc` | Normal convention. `free(NULL)` is a no-op; returns the block to its size class, span, or (huge tier) `munmap`s it |
| `_iface.assert_to` | RDI = source typedesc, RSI = source shape, RDX = source data, RCX = destination iface desc → **RAX = vtable (0 on failure), RDX = ok** | Returns normally. The **only** two-register return in the system |

That `index_oob` and `nil_assert` never return is load-bearing: it lets the
compiler emit a bounds-check trap after a conditional branch without modeling
what the trap does to flags or registers.

### Relationship to System V

BABI-0 is System V AMD64-shaped but is **not** System V-conformant. The
differences below are exactly the set that matters for interoperating with
anything not written in Boson; each is tracked in [`TODO.md`](TODO.md)
§ Full System V AMD64 ABI conformance.

| Area | System V | BABI-0 | Status |
|------|----------|--------|--------|
| Struct field alignment | natural alignment, padding inserted | same | **matches** |
| Aggregate classification | per eightbyte; a `{f64,f64}` goes in two XMM | aggregates are always INTEGER-class | **deliberate** |
| Stack alignment before `call` | RSP ≡ 0 (mod 16) | same | **matches** |
| Arguments past the registers | spilled to the stack | spilled to the stack (INTEGER); compile error (>8 SSE) | **matches** for INTEGER; SSE unimplemented |
| Odd-sized aggregates (3/5/6/7 bytes) | passed like any other | compile error | unimplemented |
| Large aggregate return | caller passes hidden pointer in RDI (MEMORY class) | address of the callee's dead frame in RAX | divergent |
| Sub-64-bit argument upper bits | unspecified | always sign/zero-extended | stricter |
| Variadic calls | AL = number of vector registers used | AL not set | divergent |
| Frame pointer chain | RBP chains to the caller's RBP | RBP is below six pushes; no chain | divergent |

Aggregate classification is **deliberate** — a considered scope-out, revisitable
only with a language-level decision.

Struct field alignment was, for a time, the reverse: fields were packed with no
padding, not by decision but because no alignment logic had ever been written.
That is now fixed — layout matches C — on the grounds that alignment-sensitive
instructions (`movaps`, atomics) and alignment-sensitive architectures are
foreclosed without it, and that the cost of the change only grows.

Everything else is a gap to close.

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

The ELF entry point is fixed: the linker looks for `_init.start`. The `_init` package (provided by the runtime's `init_linux.bs`) must define a `start` function that calls `main.main` (passing argv as `[][]byte` in rdi) and exits with main's return value.

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

The `bos_exe` run verb declares `defbody bos_exe run : $target`, so `mmk run hello` builds `hello` and its dependencies before executing `target/hello`. This requires mmk support for named-verb defbody dependency clauses.

All build artifacts — the executable, package `.bo`s, and per-package work directories holding intermediate `.bs` files — live under `target/`. Nothing is written to the project root.

A package can be pure `.bos`, pure `.bs`, or mixed; `bos_pkg` handles all three uniformly by compiling any `.bos` files to `.bs` first and then passing every `.bs` (source or generated) to the assembler in a single invocation.

### Search path

`BOSONPATH` supplies a colon-separated list of project package roots (default `.`). `boson.mmk` automatically prepends `$BOSON_HOME/runtime`, so setting `BOSONPATH=shared` keeps runtime imports available and resolves project imports beneath `shared/`. An import path `foo/bar` selects the first search root containing `foo/bar/`. Relative roots use the build working directory, just like `source=...`; no directory name is special. Set `BOSONPATH` before including `boson.mmk`.

Multiple binaries can share packages without declaring library targets:

```bash
BOSONPATH=shared
include $BOSON_HOME/boson.mmk

all : cat wc
bos_exe cat source=src/cat :
bos_exe wc source=src/wc :
```

Both binaries can `import "whatever"` to use the package in `shared/whatever/`, while imports such as `"fmt"` resolve from the automatically included runtime.

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
| `_heap`    | `heap.bos` | The Boson memory allocator: `alloc`, `free` (the targets of `alloc`/`new`/`free`), plus mallinfo-style introspection. Three tiers over 4MiB chunks carved from 1GiB reserves. |
| `string`   | `string.bs`, `puts_linux.bs` | String formatting and stdout primitives: `puts`, `puti`, `putb`, `putc`, `exit`, plus internal `strlen`, `itoa`, `uitoa`. File IO is no longer here; see `sys` and `io`. |
| `io`       | `io.bos` | Typed file IO: `type FD i64` with `read`/`write`/`close` methods; `fn open([]byte, i64, i64) owned open_result`; `STDIN`/`STDOUT`/`STDERR` globals; `reader`/`writer` interfaces; `fn copy(writer, reader) ?error`. Built on `sys`. |
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
- Floating-point types (`f32`, `f64`): literals with exponents, `<floatlit>` inference, arithmetic, IEEE comparison including NaN's unordered outcome, int↔float and f32↔f64 casts, static initializers, the System V SSE calling convention with independently-counted argument classes and XMM0 returns, and `%f`/`%v` formatting
- Compile-time floating-point constant folding in the destination's exact type, so folding is unobservable (`f32` folds in `f32`)
- Type aliases (`type Name Underlying`) with distinct-type semantics
- Type casts (`T(expr)`) including widening/narrowing/reinterpretation
- Slices (`[]T`), fixed arrays (`[N]T`), pointers (`*T`), structs
- Anonymous struct types (`struct { quot i64, rem i64 }`) as parameter, return, and var types
- Bare struct literals (`{ field: val }`) with shape inferred from the surrounding context (return, typed initializer, assignment lvalue, argument position)
- `x := expr` / `var x := expr` type inference from the initializer (`<intlit>` resolves to `i64`; void/nil sources rejected)
- Nullable pointers (`?*T`), non-null pointers (`*T`), boolean narrowing for nullable pointers, and postfix `?` non-null assertions
- Nested slices/arrays (`[][]byte`, `[N]T[M]`, etc.)
- Immutable and `var` bindings with declaration initializers (`x i64 := 42`)
- `var`-never-reassigned enforcement: a `var` whose mutability is never used (no rebind, value-field/array write, or mutable-view/`&`/array-slice) is a compile error directing the author to drop `var`; checked per scope, parameters exempt
- Unused-binding enforcement: a binding (local or parameter) that is never read, address-taken, passed, or consumed is a compile error; checked per scope at scope exit and per function for parameters; unused subsumes never-reassigned when both apply
- The discard `_`: a write-only, never-readable, repeatable binding target valid in declarations, assignments, destructuring slots, and parameters; the initializer/argument is still evaluated; discarding an owned value is rejected
- File-scope variable declarations with compile-time-constant initializers — integer/byte literals, string-into-`byte[N]`, string-into-`[]byte` (slice with relocated pointer), struct literals composing all of the above, array literals (`[1, 2, 3]`) for fixed-array and slice destinations, `&someGlobal`, `&SomeStruct{...}` (anonymous globals), `&globalArr[N]` (pointer-into-array via relocation addend)
- Array literals with inferred integer/float element types, nested initialization, and function-local expression backing tracked against escapes; context-typed empty slice literals (`[]`) without backing allocation. Nonempty local array literals cannot directly initialize slice destinations.
- Full nested `*mut T` write-through mutability with implicit coercion
- Consuming variant switches inferred from final payload ownership state, automatic wrapper discharge on fallthrough and early exits, and expression switches with exiting arms.
- Full `owned T` ownership type system: move semantics, `dispose()`, `owned()` promotion, owned struct fields, owned-field move tracking, if/else branch analysis, loop backedge/exit checks, and scope-exit checks
- Re-initialization of a moved `var owned` binding via assignment (including struct-literal RHS), gated by a pointer-flow check that rejects when a live owned alias still references the binding's storage
- Address-of of an owned binding (`&x`) preserves owned bits; whether the result borrows or moves is decided by the destination type (`*T` borrows, `*owned T` moves; `*mut`-shaped views of the owner slot remain rejected)
- Value-alias tracking for owned scalars: declaring `var fd owned i64 := ...` registers a flow-state Origin; coercing to a non-owned destination (`var t i64 := fd`, `thingy(fd)`, etc.) records the destination as an alias of that Origin; `c.Move` on the source invalidates all such aliases, and reading the destination after the move is rejected at the Symbol-use site with the same diagnostic as a borrowed-pointer use-after-move
- Field-level pointer provenance: per-field pointer facts in flow state catch self-referential struct escapes at return, dispose/free invalidating field-pointer aliases, and out-of-scope local-address extraction through a struct field
- Interfaces: structural satisfaction at coercion sites, fat-pointer representation (data + vtable, 16 bytes), automatic vtable emission per `(base, shape, I)` coercion, indirect dispatch through the vtable, and `owned I` / `I` interface qualifiers. The vtable carries a `[typedesc_ptr, shape_word, method_0..N]` layout: slot 0 is the single per-base-type `__typedesc_<base>` symbol (built-in scalar typedescs emitted by `builtin`), slot 1 is a canonical 64-bit shape word encoding the source's pointer/mut/slice/array shape, methods follow at slot 2+
- The `any` built-in empty interface (zero methods, satisfied by every concrete type, available without import); used as a coercion target and as the element type of an untyped variadic
- Variadic parameters (`name ...T`, last only): desugared to a `[]T` slice built on the caller's stack with per-element coercion, explicit `slice...` forwarding, and borrow-checked non-escaping callee slice
- Alias-aware consumed struct fields, replacement recovery, field reads and call/copy/return guards, with known branch alternatives retained.
- Invalidation of derived `from(X)` borrows across opaque mutable calls, with fresh-loan recovery, same-call argument validation, interface/alias support, backing-allocation dependencies, and conservative branch joins.
- Ordinary value switches with constant cases and `==` comparison semantics, no fallthrough, single head evaluation, duplicate-case checking, exhaustive expression results, and shared branch tracking with variant switches.
- Concrete-type assertion (`x.(T)` → `multiretu{T, bool}`, bound via the two-value declaration form) lowering to an inline two-compare check (typedesc identity + exact shape-word equality), and a type switch (`switch (v := x.(type)) { case T {...} default {...} }`) with first-match-wins flow-narrowed case binding
- Interface-to-interface assertion (`x.(I)` and `case SomeIface` in a type switch) backed by the `_iface.assert_to` runtime helper and per-typedesc lazy itab caches; the structured `__typedesc_<T>` / `__typedesc_cache_<T>` / `__iface_desc_<I>` records (with FNV-1a name/sig hashes, canonical non-receiver signature text, and a shape-derived receiver-shape match) are home-package-emitted and resolved cross-package by relocation; `x.(any)` allocates a 2-slot itab preserving the `[typedesc, shape]` invariant
- Coercion-time interface satisfaction uses the full shape-word receiver-shape filter (distinguishing value / `*self` directions exactly), in lockstep with the runtime helper's derivation; the new `typedesc` / `typedesc_cache` / `iface_desc` bas directives serialize the records and bas enforces the typedesc/cache single-emitter pairing; `bdump` pretty-prints all three
- Type-block methods: `type T <base> { ... }` declarations carrying instance methods (pointer receiver) and static methods (no receiver), called as `v.method(args)` / `T.method(args)` respectively; `struct { ... }` is a valid base (fields in the struct body, methods in the type block)
- Non-escaping borrowed pointer parameters by default, including local alias provenance and escape rejection for returns, globals, fields, and struct literals
- Compiler built-ins `alloc(T)`, `new(expr)`, and `free(p)` backed by the `_heap` runtime package; `alloc(T)` and `new(expr)` return owned mutable pointers
- `for` lowering to block + loop control flow; `continue` runs the lowered step expression
- Path-based imports with qualified calls (`import "stdlib/io"; io.puts(...)`)
- Cross-package struct types (`pair.pair`, `pair.pair{...}` literals) with auto-qualification of leaf type names at import time
- Bosc-emitted function signatures (`type fn(...) ret`) so cross-package function calls work with Boson-source packages as well as hand-written bas runtime packages
- `argv` passed to `main(args [][]byte)` by the `_init.start` entry stub
- `&arr[i]`, `&s.field` at runtime; function-scope `&"literal"` (static slice header, type `*[]byte`); other `&literal` forms rejected at runtime with a directed error
- Prefix pointer-to-slice / pointer-to-array types (`*[]byte`, `*mut []byte`, `*[8]byte`), distinct from slices/arrays of pointers (`[]*byte`, `[8]*byte`)
- Cross-package struct methods: a struct's method set rides the `struct` directive (method-name list) into the `.bo` and is reconstructed on import, so methods on imported structs resolve at compile time (parallel to type-alias methods)
- The `fmt` runtime package (five-layer formatting: raw conversions, writer helpers, `Builder`, `stringer`/`Formatter`, `print`/`printf`)
- The `flags/gnu` runtime package: independent GNU short/long option parsers, required/optional arguments, abbreviations, operand ordering, long-only mode, `-W`, structured errors, and reset; verified against GNU libc.
- Register-scaled indexing into globals via `lea` materialization (no `[symbol + reg*scale]` SIB form exists on x86-64; bosc emits an `lea` to materialize the base, then `[reg + idx*scale]`)
- Postfix chains compose: `a.b[i].c[lo:hi]` parses and codegens uniformly
- Go-style automatic semicolon insertion (newline after a statement-ending token; suppressed inside `(...)` and `[...]`)
- Positioned compiler diagnostics with source-context snippets (5 lines + colored caret on TTY); the lexer records each token's start position, and `ASTType` carries that position into "no such type" and related shape diagnostics
- mmk-driven build with auto-discovery via `bosc -listimports`; build failures halt the chain cleanly (set -e in defbody scripts, proper exit-code propagation through `pkg_import_targets`)
- bas synthetic instructions: `volatile`, `name:N` partials, size-qualified indirects, `movzx r64, r/m32`
- bas struct directive (carries Boson struct shape into .bo); bas var block form with embedded `reloc` lines for data-section relocations; bas string escapes include `\xHH`, `\r`, `\t`
- bas `typealias` and `interface` directives for cross-package export of type aliases (with method tables) and interfaces
- Cross-package variable read access: `pkg.name` qualified reads against a foreign-package variable
- Built-in `len(s)` intrinsic for slice (runtime length load) and fixed-array (compile-time constant) operands
- `builtin` package auto-imported into every compilation; provides the `error` and `any` interfaces and the single home for built-in scalar typedescs (`__typedesc_i64`, `__typedesc_byte`, …)
- `owned error` ownership semantics: consuming-receiver methods (any owned bit on the receiver) require an owned interface, mark the interface variable consumed after the call, and forbid being called on a non-owned interface; return-statement checks reject silently dropping ownership (`valType.HasOwned() && !retType.HasOwned()`) and validate general return-type compatibility via `ResolveUnderlying + Accepts` with a `sameIgnoringOwned` escape so owned↔non-owned variants of the same underlying type still pass
- Multi-value return (`fn f() i64, i64`) and destructuring bind (`a, b := f()`), including per-bind `var` qualifiers (immutable by default), per-bind type annotations, and re-binding when a name already exists in scope
- Integer literal bases: decimal, hexadecimal (`0xFF`), octal (`0o755`), and binary (`0b1010`)
- Bitwise `&` and `|` operators on integer operands
- Bare `return` (no value) in `void` functions or as an early exit
- `(expr).method(...)` dispatch for arbitrary concrete-type expression receivers, and for interface-typed expression receivers (non-consuming methods; a `owned`-receiver method still requires a named binding)
- Boson source string and character literal escapes: `\n`, `\r`, `\t`, `\0`, `\\`, `\"`, `\'`, `\xHH`
- Type-alias casts as file-scope static initializers (`var fd FD := FD(3)`, qualified form `io.FD(3)`)
- Position tracking through a leading file-level doc comment preamble (errors point at the correct source line even when the file begins with comments)
- Checked reverse projections (`?V(value)`) for injective numeric, boolean, and byte-string projections, with one-word nullable values types using tag `-1` for nil
- `values` types (`type Name values { ... }`): closed symbolic case sets with compiler-private dense `i64` tags, scoped case access (`T.CASE`, `pkg.T.CASE`), declared static projections with explicit projection casts (`i64(e)`, `[]byte(e)`, struct projections, function-pointer projections), optional value-receiver methods, value-backed interface satisfaction without heap allocation, and cross-package use through a `.bo` `values` directive
- Value-receiver methods (`name(e T)` rather than `name(e *T)`) on `i64`-sized type aliases and `values` types; recognized at call-site dispatch and accepted as the receiver shape for value-backed interface coercion
- Shared selector resolver (`cmd/bosc/resolve.go`) classifying `Dot` chains into `ResolvedRuntimeValue` / `ResolvedPackage` / `ResolvedType` / `ResolvedValuesType` / `ResolvedValuesCase` / `ResolvedStructField` / `ResolvedFunction`; `Funcall.Callee` carries the resolved chain, so qualified calls, methods, function pointers, and values cases reach codegen through the same path
- Cross-kind name collision rejection at package level between type-shaped declarations (struct / typealias / interface / values / imported package), so a values type cannot silently shadow a struct or vice versa

**Known limitations / future direction:**

- Heap allocator is intentionally minimal: one mmap mapping per allocation, with no reuse or pooling.
- No floats. Lexer accepts decimal-point syntax but truncates.
- Generics / type polymorphism not implemented.
- Stacked `owned *owned T` cannot have partial consumption (needs typestate).
- No witnessed borrows or explicit escaping-reference mechanism.
- True read-only `.rodata` segment split not yet done. String constants and other immutable data are tagged at the `o.Data` level but currently land in the writable `.data` LOAD segment. Hardware-enforced read-only data requires splitting the ELF layout.
- No deduplication of structurally-identical anonymous globals. Each non-string `&literal` produces a fresh `__static_N`. (Function-scope `&"literal"` headers *are* shared per distinct literal value within a compilation unit; cross-unit dedup of identical headers is not done.)
- macOS support stubbed but not implemented.
- Unused-mutability warning not implemented.

---

## Design Observations

- **Layered debuggability.** Each pipeline stage produces an inspectable artifact. A compiler bug can be isolated to the `.bs` output; an assembler bug to the `.bo` binary; a linker bug to the final ELF.
- **Assembler as IR.** The `.bs` language occupies an unusual middle ground: it has named variables and a register allocator, making it usable as a compiler IR, while still being human-readable assembly.
- **Minimal runtime.** No GC and no dynamic loading. Programs are fully static ELF64 binaries that make raw Linux syscalls; the heap runtime is a small mmap/munmap allocator.
- **Single-pass compiler.** The compiler does not perform optimization. It lowers each AST node directly to assembly, relying on the register allocator to minimize unnecessary spills.
- **Conservative-by-default ownership and mutability.** Defaults favor strictness (immutable, read-only, no implicit promotion) with explicit opt-in for looser forms (var, mut, owned()). The type system encodes obligations the compiler can check; programmer-asserted unsafe operations are explicit (`owned(...)`, `dispose(...)`).
- **Name-is-address as a single distinction.** `local` allocations are register-resident — the name *is* the value. `bytes`/`var`/`data` allocations are memory-resident — the name *is* the address. Bosc tracks this with a single bool per `spot` (`nameIsAddress`), populated at allocation/declaration time. Every site that needs to follow a pointer through such storage consults the same flag and emits the same lea-then-deref or load-first pattern. The distinction isn't between local and global scope (a `bytes`-allocated local and a `var` global behave the same way); it's about register vs memory residency.
- **Data relocations as a uniform mechanism.** A `.bo` file's globals can carry per-block relocations the linker resolves at link time, producing absolute pointer slots. Any compile-time-constant expression — primitive literal, struct literal, `&someGlobal`, `&literal` — collapses to a `(bytes, relocations)` pair. Slice headers, struct fields holding pointers, and anonymous globals all fall out of that one mechanism without per-feature code paths.
