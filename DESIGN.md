# gbasm Design Document

## Overview

gbasm is a complete three-stage compiler toolchain for a custom language called **Boson**. It targets x86-64 Linux, producing native ELF64 executables. The entire toolchain is written in Go.

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

Each stage produces an inspectable intermediate artifact, which makes the pipeline easy to debug. The `.bs` assembly files in particular serve as a readable window into what the compiler generates.

---

## The Boson Language

Boson is a statically typed, imperative language. It is intentionally small: no dynamic memory allocation, no generics, no closures. Its feature set is roughly comparable to a restricted dialect of C.

### Types

| Type | Description |
|------|-------------|
| `num` | 64-bit signed integer |
| `byte` | 8-bit unsigned integer |
| `str` | Pointer to a null-terminated byte string |
| `*T` | Pointer to type T |
| `T[]` | Slice (fat pointer: data pointer + length) |
| `T[N]` | Fixed-size array |
| `struct { ... }` | Named record type |

Types are divided into **direct** types (fit in a single register: `num`, `byte`, `str`, pointers) and **indirect** types (too large for a register: structs, arrays, slices — passed by pointer or on the stack).

### Expressions

Arithmetic: `+`, `-`, `*`, `/`

Comparison: `==`, `!=`, `<`, `>`, `<=`, `>=`

Logical: `&&`, `||`

Operator precedence is handled correctly (multiplication before addition, etc.).

Struct field access uses `.` for both direct structs and pointer-to-struct: `p.x`. The compiler dereferences automatically.

Array indexing: `arr[i]`. Bounds checking is inserted at compile time.

Slice operations: `s[1:]` produces a new slice advanced by one element.

### Statements

```
var i num               // local variable declaration
i = 10                  // assignment
p.x = i + 1            // field assignment

if (cond) { ... } else { ... }

for (init; cond; step) { ... }

break
continue
return expr
```

### Functions

```
fn add(x num, y num) num {
    return x + y
}
```

Arguments follow the System V AMD64 ABI: the first six integer/pointer arguments go in RDI, RSI, RDX, RCX, R8, R9; additional arguments go on the stack. The return value goes in RAX.

### Packages and Imports

```
package main
import "string.bo"
```

Packages compile to `.bo` object files. Imports name the object file to link against. The linker resolves cross-package symbol references.

### Built-in Functions

| Function | Signature | Description |
|----------|-----------|-------------|
| `puts` | `(str)` | Print a null-terminated string to stdout |
| `puti` | `(num)` | Print an integer to stdout |
| `putb` | `(byte)` | Print a single byte to stdout |
| `lenb` | `(byte[]) num` | Return the length of a byte slice |

These are provided by the runtime assembly files (`puts_linux.bs`, `string.bs`) that are linked into every program.

### Struct Literals

```
var p point
p = point{ x: 10, y: 20, z: 30 }
```

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

Every path through a function must either consume every owned obligation or pass it to a function that will.

### `dispose`

`dispose(x)` is a built-in consuming operation that terminates an `owned T` obligation with no other effect. It is used inside consuming functions after all cleanup work is done:

```
fn close(fd owned i64) void {
    syscall_close(fd)
    dispose(fd)   // obligation satisfied
}
```

`dispose` can also be called directly by a caller who wants to explicitly abandon an obligation without doing any other work. The type system cannot enforce whether this is semantically correct for a given type — that is the programmer's responsibility.

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
fn create_whatever(w *whatever) *owned whatever {
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

`owned(w)` is an unsafe built-in: it promotes a plain `*T` to `*owned T`, asserting that `*w` is now properly initialized and the caller is responsible for it. The compiler cannot verify this. The programmer is responsible for the aliasing invariant — that `w` is not used to access whatever fields while `w2` is live — which is also beyond what the type system can check.

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

const myfoo foo = { x=10, y=&x }  // myfoo's fields cannot be directly written
var myfoo foo                      // myfoo's fields can be directly written
```

`const` and `var` apply uniformly to all types — integers, structs, pointers, slices. There is no special treatment for any type.

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

`mut` as a type qualifier is meaningless on non-reference types. You cannot write `mut i16` as a standalone type — there is no indirection involved.

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

const myfoo foo = { ... }

myfoo.x = 10    // illegal — myfoo is const, field cannot be written
myfoo.y = &z    // illegal — myfoo is const, field cannot be rebound
*myfoo.y = 100  // LEGAL — the type of y carries write-through permission
```

Pointer indirection nests correctly. Each `*` level independently carries its own `mut`:

```
const y **i16       // const binding; cannot write through either pointer level
const y **mut i16   // const binding; cannot change intermediate pointer, CAN write innermost i16
const y *mut *mut i16  // const binding; CAN change intermediate pointer; CAN write innermost i16
var y *mut *mut i16    // all four: rebind y, change intermediate pointer, write innermost i16
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

Strings are immutable slices of bytes: `byte[]`. The `str` type (a bare pointer to null-terminated bytes) is removed. String literals have type `byte[]`.

```
const greeting byte[] = "hello\n"   // typical string constant
```

A mutable byte buffer has type `mut byte[]`. String literals cannot have type `mut byte[]` since they live in read-only data.

---

## The Boson Compiler (bosc)

`cmd/bosc/` implements the compiler as a classic single-pass pipeline over the AST.

### Lexer (`lexer.go`)

Produces a flat token stream from Boson source. Recognizes keywords (`fn`, `var`, `struct`, `if`, `else`, `for`, `return`, `break`, `continue`, `import`, `package`), identifiers, integer literals, string literals, and all operators. Reports positions (file, line, column) for error messages.

### Parser (`parser.go`)

Recursive descent. Builds an AST from the token stream. Handles operator precedence via a Pratt-style precedence table. Produces positioned AST nodes so errors downstream can report source locations.

### AST (`ast.go`)

Defines all node types: declarations (package, import, struct, function), statements (var, assign, if, for, return, break, continue, expression), and expressions (binary op, unary op, call, index, slice, field access, literal, identifier). Also defines the type system: `Type` structs for all Boson types, with methods for equality and size computation.

### Validator (`validate.go`)

Semantic analysis pass over the AST. Resolves identifiers against a scoped symbol table. Type-checks all expressions (infers types bottom-up, checks assignments and call sites top-down). Reports type mismatches, undefined symbols, and invalid operations with source positions. Annotates the AST with resolved types for the code generator.

### Code Generator (`compile.go`)

Walks the validated AST and emits `.bs` assembly text. Key responsibilities:

- **Locals**: Each `var` declaration emits a `local` directive in the assembly. The assembler's register allocator handles actual placement.
- **Control flow**: `if`/`else` and `for` lower to compare-and-jump sequences with generated labels (`_LABEL_for_N`, `_LABEL_break_N`, `_LABEL_cont_N`).
- **Function calls**: Arguments are evaluated into temporaries, then moved into the ABI argument registers before `call`.
- **Struct access**: Field offsets are computed at compile time. Direct struct access becomes a `mov` with a computed offset; pointer access dereferences first.
- **Slices**: A slice is two words (pointer and length). Indexing bounds-checks against the length word, then computes `base + index * element_size`. Slice operations (`s[1:]`) adjust both pointer and length.
- **Bounds checking**: Array and slice accesses emit a compare and a conditional call to a trap (or direct `HLT`/error) if the index is out of range.
- **Temporaries**: The compiler allocates temporaries as locals with names like `T1`, `T2`. The assembler's register allocator places these in registers or spills them to the stack.

---

## The Assembly Language (.bs)

The `.bs` format is a custom assembly language designed specifically for use as a compiler target. It sits at a level between raw x86-64 assembly and an IR: it has explicit registers but also named variables, a register allocator, and a calling-convention-aware function model.

### File Structure

```
package <name>

function <name>
    // directives and instructions
    
data <name> string "text\0"
var <name> string "text\0"
```

Every file begins with a `package` declaration. Code is grouped into `function` blocks. `data` and `var` declarations define global read-only and mutable data respectively.

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
| `package` | `package name` | Sets file/package identity |
| `function` | `function name` | Begins a function definition |
| `type` | `type fn(...) ret` | Annotates function signature (informational) |
| `data` | `data name string "..."` | Global read-only data |
| `var` | `var name string "..."` | Global mutable data |
| `local` | `local name bits [reg]` | Stack/register local variable |
| `bytes` | `bytes name size [reg]` | Stack byte array (non-register) |
| `arg` | `arg name reg` | Pin argument to register |
| `arg` | `arg name offset` | Argument at stack offset |
| `argi` | `argi name index` | Argument at index (0→RDI, 1→RSI, ...) |
| `label` | `label name` | Jump target |
| `prologue` | `prologue` | Save callee-saved regs, set up frame |
| `epilogue` | `epilogue` | Restore regs, tear down frame |
| `use` | `use reg` | Mark register in-use |
| `acquire` | `acquire reg...` | Evict variables from register, claim it |
| `release` | `release reg...` | Release register back to allocator |
| `inreg` | `inreg var reg` | Force variable into register |
| `forget` | `forget name` | Free a local variable's storage |
| `forgetall` | `forgetall` | Free all locals |
| `evict` | `evict [reg...]` | Spill variables from registers |

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

Valid scales for indexed addressing: 1, 2, 4, 8.

### Instructions

The assembler supports the full x86-64 instruction set (600+ instructions from an embedded Intel XML database). The most commonly used:

**Data movement:** `MOV`, `MOVSX`, `MOVZX`, `LEA`, `XCHG`, `PUSH`, `POP`

**Arithmetic:** `ADD`, `SUB`, `IMUL`, `MUL`, `IDIV`, `DIV`, `INC`, `DEC`, `NEG`

**Bitwise:** `AND`, `OR`, `XOR`, `NOT`, `SHL`, `SHR`, `SAR`, `ROL`, `ROR`

**Comparison:** `CMP`, `TEST`, `SETE`/`SETNE`/`SETL`/`SETG`/`SETLE`/`SETGE` (and other SETcc)

**Jumps:** `JMP`, `JE`/`JZ`, `JNE`/`JNZ`, `JL`, `JLE`, `JG`, `JGE`, `JB`, `JBE`, `JA`, `JAE`, `JS`, `JO`, `JRCXZ`, and more

**Control:** `CALL`, `RET`, `SYSCALL`

The assembler automatically selects the correct encoding variant based on operand sizes.

### String Escape Sequences

Inside `data`/`var` string literals: `\n` (newline), `\0` (null), `\\` (backslash), `\"` (quote).

### Register Allocator

The assembler includes an LRU register allocator for named locals. When a local is declared with `local name bits`, the allocator:

1. Assigns it a register from the available pool (preferring caller-saved registers R10, R11 first to minimize prologue/epilogue overhead).
2. When all registers are full and a new value is needed, spills the least-recently-used variable to the stack.
3. Tracks sub-register widths — an 8-bit local uses `AL`/`R8B`/etc., while a 64-bit local uses the full register.

Variables can be pinned to specific registers with `local name bits reg` or `inreg name reg`. The `use`/`acquire`/`release` directives give manual control over which registers the allocator is allowed to touch.

### Calling Convention

Follows System V AMD64 ABI exactly:

- **Argument registers (in order):** RDI, RSI, RDX, RCX, R8, R9
- **Stack arguments:** pushed right-to-left, accessible at positive offsets from RSP after prologue
- **Return value:** RAX (primary), RDX (secondary for 128-bit returns)
- **Callee-saved:** RBX, RSP, RBP, R12–R15 (preserved across calls; `prologue`/`epilogue` save/restore these)
- **Caller-saved:** RAX, RCX, RDX, RSI, RDI, R8–R11 (may be clobbered by any call)

---

## The Assembler (bas)

`cmd/bas/main.go` implements the assembler as a two-pass process:

1. **Parse pass:** Reads the `.bs` file line by line, processes directives, and emits instruction records with symbolic labels and variable references.
2. **Encode pass:** Uses the core `gbasm` library to encode each instruction into x86-64 binary. Label references become PC-relative offsets resolved at encode time (or left as relocations for the linker).

The assembler outputs a `.bo` object file containing:
- The encoded binary text section
- A symbol table (function names, global data names)
- A relocation table (unresolved `call` and `lea` targets)
- Type descriptor records for runtime type information

---

## The Core Library

The root Go package implements the low-level encoding and object format shared by `bas` and `bld`.

### `encoder.go`

x86-64 instruction encoding. Loads the Intel XML specification (`x86_64.xml`) and provides a `Encode(mnemonic, operands...)` API. Handles:
- REX prefix generation for 64-bit operands and extended registers
- ModR/M byte encoding for register, memory, and indirect operands
- SIB byte for base+index×scale addressing
- Immediate value encoding with proper sign extension
- All operand size variants (8/16/32/64-bit)

### `reg.go`

Register definitions and metadata. Each register entry records its name, bit width, encoding value, and parent/child relationships (e.g., AL is the low 8 bits of AX, which is the low 16 bits of EAX, which is the low 32 bits of RAX). The width tracking enables the assembler to automatically select the correct instruction variant.

### `regalloc.go`

LRU register allocator. Maintains a pool of general-purpose registers and tracks which variable currently occupies each register. When a register is needed and the pool is exhausted, evicts the least-recently-used variable to the stack. Prefers caller-saved registers to minimize save/restore overhead.

### `function.go`

Function-level state: stack frame layout, local variable offsets, argument positions, callee-saved register list. Manages the prologue/epilogue code generation.

### `elf64.go`

ELF64 file format implementation. Builds the ELF header, section headers (`.text`, `.data`, `.symtab`, `.strtab`, `.rela.text`), and writes a valid ELF64 binary. Follows the ELF-64 specification and System V ABI supplement.

### `ofile.go` / `bwrite.go`

Custom binary object file format (`.bo`). Stores encoded instructions, symbol names and offsets, relocation entries, and type descriptors. `bwrite.go` provides serialization/deserialization for this format.

### `linker.go`

Combines multiple `.bo` files into a single ELF64 executable. Concatenates text sections, merges symbol tables, resolves relocations by computing final virtual addresses, and writes the output binary.

---

## The Linker (bld)

`cmd/bld/main.go` is a thin wrapper over `linker.go`. It accepts a list of `.bo` object files and an output path, invokes the linker, and writes the ELF64 binary. The output file is set executable.

---

## Object File Format (.bo)

The custom `.bo` format stores:

| Section | Contents |
|---------|----------|
| Header | Magic bytes, version, section count |
| Text | Raw x86-64 encoded bytes |
| Symbols | Name → offset mappings for defined functions/globals |
| Relocations | (offset, symbol, type) triples for unresolved references |
| Type info | Function signatures for type checking at link time |

The format is simpler than ELF to make assembler output straightforward. The linker translates `.bo` → ELF64 as its final step.

---

## Build and Test

```
make test       # Run all test suites
make go_test    # Go unit tests (encoder, decoder, lexer, parser, validator)
make bas_test   # Assembler integration tests
make bosc_test  # Compiler integration tests
```

Each compiler integration test:
1. Compiles the `.bos` source with `bosc`
2. Assembles with `bas` (linking runtime `.bs` files)
3. Links with `bld`
4. Runs the binary and captures stdout
5. Diffs stdout against the `.bos.expected` file

The assembler tests follow the same pattern but start from `.bs` files directly.

---

## Runtime

There is no separate runtime library binary — instead, a handful of `.bs` source files are assembled and linked into every program:

| File | Purpose |
|------|---------|
| `init_linux.bs` | Process entry point (`_start`). Sets up `argc`/`argv`, calls `main`, exits via `exit` syscall. |
| `puts_linux.bs` | `puts(str)` — walks the string to find its length, then issues `write(1, ptr, len)` syscall. |
| `string.bs` | `strlen`, `atoi`, and other string utilities used by the standard library. |

These files encode Linux-specific behavior directly. There is a `macho/` directory stub suggesting macOS support was planned but not yet implemented.

---

## Design Observations

- **Layered debuggability.** Each pipeline stage produces an inspectable artifact. A compiler bug can be isolated to the `.bs` output; an assembler bug to the `.bo` binary; a linker bug to the final ELF.
- **Assembler as IR.** The `.bs` language occupies an unusual middle ground: it has named variables and a register allocator, making it usable as a compiler IR, while still being human-readable assembly.
- **Minimal runtime.** No heap, no GC, no dynamic loading. Programs are fully static ELF64 binaries that make raw Linux syscalls.
- **Incremental language growth.** The commit history shows features being added one at a time (slices, break/continue, bounds checking, boolean operators), each with dedicated test cases.
- **Single-pass compiler.** The compiler does not perform optimization. It lowers each AST node directly to assembly, relying on the register allocator to minimize unnecessary spills.
