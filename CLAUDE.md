# CLAUDE.md — gbasm working notes

This file is the orientation guide for working in this repo. The
authoritative reference for the language and the toolchain is
[`DESIGN.md`](DESIGN.md) (≈1500 lines covering syntax, semantics,
ownership, mutability, ABI, file formats). This file does **not**
restate that material — it tells you where things live and how to
work the build/test loop without re-deriving conventions every
time.

When DESIGN.md and CLAUDE.md disagree, DESIGN.md wins; update this
file rather than the design doc.

---

## What this repo is

A complete toolchain for the **Boson** language, written in Go,
targeting x86-64 Linux and producing static ELF64 executables. The
pipeline is:

```
.bos  (Boson source)
  → bosc           → .bs  (custom assembly)
  → bas            → .bo  (custom object)
  → bld            → ELF64 executable
```

Each stage emits a human-inspectable artifact. When something
breaks, narrow the bug by looking at the `.bs` (compiler output) or
the `.bo` via `bdump` (assembler/linker boundary).

See [DESIGN.md §Overview](DESIGN.md) for the full pipeline rationale.

---

## Repository layout

### Top-level Go package (`github.com/knusbaum/gbasm`)

This is the shared core used by `bas` and `bld`. Edits here affect
encoding, register allocation, and the object/ELF formats.

| File | Purpose |
|------|---------|
| `encoder.go` | x86-64 instruction encoding driven by `x86_64.xml`. REX/ModR/M/SIB byte construction, immediate handling, operand-size variants. |
| `decoder.go` | Minimal disassembler (used by tests/debugging). |
| `reg.go` | Register table and sub-register relationships (RAX↔EAX↔AX↔AL/AH). `partial(N)` returns N-bit sub-register. |
| `regalloc.go` | LRU register allocator. Prefers caller-saved (R10/R11). Spills LRU on pressure. |
| `function.go` | `Function`, `Ralloc`, `RallocPartial`, prologue/epilogue, the synthetic `MOVZX r64,r/m32` lowering, package-name relocation qualification at resolve time. |
| `ofile.go`, `bwrite.go` | The `.bo` object format. `DataReloc{Offset, Symbol, Addend}` lives in `bwrite.go`. `StructShape` for cross-package struct types. |
| `elf64.go` | ELF64 writer (sections, symtab, .rela.text, LOAD segments). |
| `linker.go` | `LinkExe`. Reachability-driven placement; resolves both code (32-bit PC-rel) and data (64-bit absolute) relocations. Entry point hardcoded to `_init.start`. |
| `x86_64.xml` | The Intel instruction database. Don't edit by hand. |
| `macho/` | Stub for future Mach-O support. Not currently wired up. |
| `old/` | Pre-rewrite scratch code. Untouched by the live build; safe to ignore. |

### `cmd/bosc/` — the Boson compiler

The compiler is a single-pass pipeline. There is **no separate
validator phase** — validation happens inline in `ToAST` (during AST
construction) and in `Compile`/`compileTop` (during code gen). New
checks belong in those code paths, not in a phase that doesn't exist.

| File | Purpose |
|------|---------|
| `main.go` | Driver. Reads `-importcfg=<file>`, handles `-listimports`, drives one file at a time through lex/parse/AST/compile. |
| `lexer.go` | Tokenizer. Performs Go-style automatic `;` insertion (DESIGN.md §Statements). Records `position{fname,lineoff,linecharoff}` on every token. |
| `parser.go` | Recursive-descent. `parseAddSub` / `parseUnary` / `parseSubexpr` / `parsePostfix` for expression precedence. `parsePostfix` handles `.member`, `[i]`, `[lo:hi]`, `(args)`, `{fields}` uniformly. |
| `ast.go` | AST node definitions, `ASTType` (with `MutMask`/`OwnedMask` bitfields, plus a `p` position for diagnostics), `Context` (name resolution + flow facts + import state, including interface declarations and per-type method tables), `ToAST` for converting parse nodes to typed AST. |
| `checker.go` | `CheckerState`, `FlowSnapshot`, `FlowPath`. Flow-sensitive facts: nullability, owned-field consumption, borrowed bindings, moves. |
| `flow/state.go` | Pointer-flow / alias state used by the checker. `Origin`, `PointerExpr`, `Invalidation`, `Merge`, per-field pointer facts (`SetFieldPointer`/`GetFieldPointer`/`CopyFieldPointers`), and `AliasesOf` for re-init checks. |
| `compile.go` | The big one (~3000 lines). Lowers each AST node to `.bs`. `compileTop` is the dispatch on AST type. `spot{ref, t, nameIsAddress}` is the codegen handle for a value/location. |
| `globals.go` | `emitGlobalVarDecl` and `encodeStaticInit` for file-scope vars with compile-time-constant initializers. Anonymous globals (`__static_N`) and `relocSpec` live here. |
| `errctx.go` | `fatalCtx` + `printErrorContext` — five-line source snippet around an `interpreterError`'s position, with a caret arrow (red on TTY, plain otherwise). Always use `fatalCtx` for errors carrying positions. |
| `lexer_test.go`, `parser_test.go`, `checker_test.go` | Go unit tests for the front end. |
| `tests/` | ~490 files. Each `*_test.bos` has a paired `*_test.bos.expected`. See [Tests](#tests). |
| `run_tests.sh` | The compiler integration test runner. |
| `boson-mode.el` | Emacs major mode for `.bos` files. |

### `cmd/bas/` — the Boson assembler

`main.go` is the whole thing (~1000 lines). Two-pass: parse `.bs`
into instruction records, then encode. Wraps the top level in
`recover()` so panics from `volatile`/`inreg` invariant violations
print as `Fatal: <message>` (test-friendly) instead of Go tracebacks.

| File | Purpose |
|------|---------|
| `main.go` | Parser for directives (`function`, `local`, `bytes`, `arg`, `var`, `data`, `struct`, `prologue`, `epilogue`, etc.) and the line-by-line instruction matcher that calls `gbasm.Encode`. |
| `tests/` | ~73 files. `*_test.bs` + `.bs.expected`; some have `_err_test.bs` (negative tests). |
| `run_tests.sh` | Assembler integration runner. |

### `cmd/bld/` — the linker

Thin wrapper over `linker.go`. `main.go` is 50 lines: read `.bo`s,
reject duplicate package names, call `gbasm.LinkExe`, chmod +x.

### `cmd/bdump/` — object-file inspector

Dumps `.bo` contents (functions, vars, data, relocations, symbol
tables) in human form. Useful when debugging linker problems.

### `cmd/bdoc/` — documentation server

Walks `BOSONPATH`, scans each package's `.bos`/`.bs` files for top-
level declarations and their doc comments, serves HTML on
`:8686`. Independent of the toolchain proper. Files: `main.go`,
`discover.go`, `scan.go`, `server.go`.

### `runtime/` — the runtime library

Every linked program pulls in some subset of these.

| Package | Files | Purpose |
|---------|-------|---------|
| `_init`    | `init_linux.bs` | Process entry (`_init.start`). Builds `byte[][]` argv on stack, calls `main.main(args byte[][])` (or `main.main()`), exits with main's rax. Also defines `index_oob` (bounds-check trap) and `nil_assert`. |
| `_heap`    | `heap_linux.bs` | `_heap.alloc(i64) *mut byte` and `_heap.free(*mut byte)`. One mmap mapping per allocation; small size header; `free` calls munmap. Minimal, no pooling. |
| `_io_sys`  | `io_sys_linux.bs` | Raw Linux file-IO syscall wrappers: `_io_sys.read/write/open/close`. The low-level primitives that `io` is built on; end-user code should prefer `io`'s typed FD API. |
| `string`   | `string.bs`, `puts_linux.bs` | String formatting and stdout output: `puts`, `puti`, `putb`, `putc`, `lenb`, `lenn`, `lenbb`, `exit`. Internal: `strlen`, `itoa`, `uitoa`, `ucountdigits`. File-IO syscalls live in `_io_sys`; the `io` package wraps them with a typed FD. |
| `io`       | `io.bos` | Typed file IO: `type FD i64` with `read`/`write`/`close` methods, `fn open(path, flags, mode) owned FD`, and `STDIN`/`STDOUT`/`STDERR` globals. Wraps the raw `_io_sys` syscalls. |
| `pair`     | `pair.bos` | A tiny exported struct used only by the cross-package-struct tests under `cmd/bosc/tests/`. Exists so the import path for Boson-source packages is exercised end-to-end. |

### `examples/` — runnable example projects

`examples/hello` is the canonical minimal `bos_exe` build. `examples/linked` is a non-trivial example with a linked-list and ownership. `examples/interface` demonstrates interface dispatch and static methods on `type`-blocks.

Each example has an `mmkfile` that invokes the `bos_pkg`/`bos_exe`
rules from `boson.mmk`.

### Build orchestration

| File | Purpose |
|------|---------|
| `Makefile` | `make all` (builds `bld bas bosc`), `make test`, `make go_test`, `make bas_test`, `make bosc_test`, `make sloc`/`loc`. |
| `boson.mmk` | mmk library defining `bos_pkg` (pattern rule for `target/<import-path>.bo`) and `bos_exe`. Drives import discovery via `bosc -listimports`. Requires the external `mmk` build tool. |

---

## Build and test workflow

### Building the toolchain

```
make all           # builds ./bld ./bas ./bosc at repo root
```

The three binaries are checked into `.gitignore` semantically — the
working tree commonly carries them as untracked build outputs.

### Running tests

```
make test          # everything: Go unit tests + bas + bosc suites
make go_test       # Go unit tests only (encoder, parser, checker, flow)
make bas_test      # ~70 .bs integration tests
make bosc_test     # ~260 .bos integration tests
```

`run_tests.sh` in `cmd/bas` and `cmd/bosc` will rebuild the
toolchain itself before running. They also pre-assemble the runtime
(`string.bo`, `init.bo`, `heap.bo`, `pair.bo`) into the working
directory and tear them down on success.

### Test file conventions

For both `bas` and `bosc` integration suites:

- `<name>_test.bos` / `<name>_test.bs` — must compile/assemble,
  link, run, and produce stdout matching `<name>_test.bos.expected`.
- `<name>_err_test.bos` / `<name>_err_test.bs` — **must fail** at
  compile/assemble time. Its `.expected` file contains the expected
  stderr (positional prefixes `at file:line:col:` are stripped
  before diff for `bosc`; for `bas` the raw stderr is matched).
- `<name>_test.bos.args` — optional. Whitespace-split tokens become
  argv for the binary at run time. Used by tests that exercise `argv`.

To add a new test: create the `.bos` (or `.bs`), create the
matching `.expected`, then re-run the suite. The script cleans up
intermediate `.out`/`.stdout`/`.bo` files on success, so the only
permanent artifacts you commit are the source + expected.

### Inspecting failures

When a test fails, the runner leaves `${t}.bosc.out`, `${t}.bas.out`,
`${t}.bld.out`, `${t}.stdout`, and the intermediate `${t}.bs` /
`${t}.bo` in place so you can re-run pieces by hand. The runner
only deletes them on a clean pass.

### Building an example

```
cd examples/hello
BOSON_HOME=../.. mmk            # builds ./hello
mmk run                         # builds and runs
```

`mmk` itself is a separate tool (a make-like build orchestrator
with bash-native DSL). It's not part of this repo. If you don't
have it, the integration test scripts and `make test` exercise the
toolchain without it.

---

## Editing rules of thumb

A change to the language usually touches **several layers** in
order. The expected ordering is:

1. **Lexer** (`cmd/bosc/lexer.go`) — new keyword or token. Add to
   the `keywords` map and the `toktype` enum.
2. **Parser** (`cmd/bosc/parser.go`) — new syntactic form. Most
   precedence work lives in `parseAddSub`/`parseUnary`/`parsePostfix`.
3. **AST** (`cmd/bosc/ast.go`) — new node type (`n_*` constant +
   struct). `ToAST` on the parse node owns the validation that
   doesn't depend on flow.
4. **Checker** (`cmd/bosc/checker.go`, `flow/`) — flow-sensitive
   facts (moves, nullability, owned-field consumption, borrows).
5. **Codegen** (`cmd/bosc/compile.go`, sometimes `globals.go`) —
   lowering to `.bs`.
6. **bas** (`cmd/bas/main.go`) — only if you need a new directive
   or instruction-shape recognition.
7. **Runtime** (`runtime/*/*.bs`) — only if the feature needs new
   runtime support (e.g. a bounds-check trap, an allocator).

For a **bug fix** in codegen, the touch is usually just steps 4–5.
For a **runtime-format change** (new `.bo` field, new ELF section
behavior), the core library files (`bwrite.go`, `linker.go`,
`elf64.go`) come in instead.

### Things that bite

These are conventions worth remembering before editing.

#### Storage classes: `local` vs `bytes` vs `var`/`data`

The compiler's `spot{ref, t, nameIsAddress}` collapses three
storage classes into a uniform handle. The `nameIsAddress` bit is
populated at allocation/declaration time and consulted at every
indirection site:

- `local name bits` — register-resident scalar/pointer. The name
  *is* the value. `nameIsAddress = false`.
- `bytes name N` — stack chunk for a struct or large value. The
  name *is* the address. `nameIsAddress = true`.
- `var`/`data` at file scope — memory-resident global. The name
  *is* the address (resolved RIP-relative). `nameIsAddress = true`,
  recorded in `Context.addressNames`.

`NameIsAddress(name)` consults both the explicit `addressNames` set
and the type-based `typeIsMemoryBacked(t)` predicate (true for
structs and >8-byte values). If you add a new value form, decide
which side of this line it sits on early.

#### `volatile` + `inreg` panic

If a local is marked `volatile` (the compiler emits this whenever
`&name` is taken on a not-already-memory-backed local), any
subsequent `inreg` on it panics in `bas`. The top-level `recover()`
in `cmd/bas/main.go` converts that to `Fatal: …`. If you see this
in test output, the compiler emitted an `inreg` against a `&`-taken
name; the fix is on the bosc side.

#### `&literal` is static-init only

`&SomeStruct{…}` and `&globalArr[i]` are legal in file-scope
initializers (they allocate anonymous globals `__static_N`). The
same forms at function scope are rejected with a directed error.
If you're adding a new lvalue path that wants `&`-of-expression,
keep that asymmetry in mind.

#### Ownership: the two-line rule

DESIGN.md §Ownership covers the full system, but two consequences
drive most of the code:

- **"`owned` in a parameter always moves."** If the parameter type
  has any `owned` bit set anywhere, passing into it consumes the
  argument variable. Borrows are the strict complement: no `owned`
  in the parameter type means the obligation stays with the caller.
- **"Borrowing strips ownership."** Passing `owned *owned T` to a
  parameter of type `*T` produces a fully non-owning view; both
  obligations stay home.

When you're touching call-site code or thinking about a new
parameter kind, those two are the invariants to preserve.

#### Owned scalars create aliases on coercion, not copies

`var fd owned i64 = 10` registers `Origin("fd")` in the pointer-flow
state (compile.go's VarDecl branch). Coercing to a non-owned
destination — `var t i64 = fd`, `thingy(fd)`, passing as an `i64`
parameter — produces an alias: `pointers["t"] = {Origin: "fd"}`.
Reading the destination goes through the Symbol case in compileTop,
which calls `CheckDerefValidity` on the binding's link.
`c.MoveConsume(fd)` invalidates `Origin("fd")` with `TargetMoved`,
so any later use of `t` is rejected with "cannot dereference
pointer to 'fd': the target was consumed." Same machinery catches
use-of-stale-pointer (`use_ptr(p)` after `take(i)` where `p = &i`):
the Symbol read of `p` fires the same check.

If you add a new path that creates an owned scalar binding or a
non-owned scalar from one, mirror the existing decl/assign sites:
register the Origin at owned decl, run the source through
`pointerExprForAST` and `AssignPointer` at the coercion site.

#### MoveConsume vs MoveTransfer

Two distinct operations on owned bindings, both in checker.go:

- **`c.MoveConsume(name)`** — binding is dead AND its storage is
  dead. Invalidates the relevant Origin (`TargetDead` for pointers,
  `TargetMoved` for values), so any alias of that storage becomes
  stale. Use for `dispose(p)`, value-typed Symbol consume in
  `markMovedIfOwnedSource`, branch-narrow-to-null on an owned
  nullable, and the uninitialized-owned-decl path.
- **`c.MoveTransfer(name)`** — binding is dead but the storage
  transferred to a new owner who still references it. Only marks
  `movedBindings[name]`; Origin stays Live, so other pointer
  aliases of the same storage remain usable until the *new* owner
  is consumed. Use for `&x` into `*owned T` and pointer-typed
  Symbol move into owned destination.

Pick the wrong one and either aliases survive a real consume (test
suite catches it via the use-after-move tests), or every pointer
transfer falsely invalidates its own destination (test suite
catches via `owned_address_transfer_test` and friends). When in
doubt, search for an analogous case already in `markMoved
IfOwnedSource` or the dispose handler.

#### `&owned` and re-init

`&x` of an owned binding keeps its owned bits in the resulting
pointer type. Whether the result borrows or moves is decided at
the **destination**: `*T` borrows (`Accepts` strips owned), `*owned
T` moves (move tracking marks the source consumed). The function
`checkAddressOfOwnedForDest` still rejects `*mut`-shaped views of
an owner slot, preserving the slot-replacement invariant.

Assignment to a moved `var owned` binding can re-initialize it. The
gate is `HasLiveOwnedAlias(target)` (checker.go), which runs over
`AliasesOf` (flow/state.go) — that iterator consults both the
`pointers` map and the `fieldPointers` map, so a struct field that
copied `&x` still counts as an alias to `x`'s storage. Borrow-shaped
aliases do not block re-init. If you're adding a new pointer source
or alias kind, plumb it through `AliasesOf` or this check goes
silently unsound.

#### Bare struct literals are context-typed

`{ field: val }` with no leading type name only parses when the
parser can hand a destination shape to `ToAST`: return statements,
typed `var`/`const` initializers, the RHS of an assignment to a
struct lvalue, and function arguments. New sites that want to accept
a bare literal must thread the destination type to the literal's
`ToAST` call, or use the assignment-path shortcut that routes a
matching-shape struct literal RHS through `compileStructLiteralInto`
directly.

#### Symbol qualification

Bare names in `.bs` (function names, jump-target relocations, var
references) are auto-qualified at assemble time using the file's
`package` declaration. So a hand-written runtime function can
say `call strlen` and it becomes `call string.strlen` in the `.bo`.
Cross-package calls in `.bs` use the explicit form: `call other.func`.

For bosc, the compiler always emits fully-qualified call symbols
itself; the auto-qualification path in bas is mostly there for
hand-written runtime code.

#### `mov [mem] imm` encodes narrow

`mov qword[…] 1` works correctly; `mov [mem] imm` may pick the
narrowest opcode and produce a single-byte store. When writing
small immediates into 8-byte slots (like slice length fields),
either use the `qword[…]` form or route through a register. The
compiler routes through registers; the assembler accepts the sized
form.

#### Auto-semicolon insertion gotchas

The lexer emits `;` after newlines whose preceding token can end a
statement. `} else` on the same line works; `}` newline `else`
**does not parse** (the `;` separates them). DESIGN.md §Statements
has the full list of statement-ending tokens.

---

## Things to know about the working tree

The git status at session start commonly shows several untracked
items that are not pending work:

- `bas`, `bld`, `bosc` at the repo root — build outputs from
  `make all` (the binaries themselves are ignored from commits).
- `cmd/bosc/pair.bo` / `cmd/bosc/pair.bs` — test artifacts produced
  by `cmd/bosc/run_tests.sh` when it compiles the `pair` runtime
  package for cross-package-struct tests. The script removes them
  on a clean pass; if the suite was interrupted, they're left over.
- `examples/linked/target/`, `examples/interface/target/`,
  `examples/hello/target/` — built example artifacts. Each example's
  `target/` holds the executable (e.g. `target/linked`), the
  per-package `.bo` files, and the per-package work directories
  (`target/<name>.work/` with intermediate `.bs`/`.bo` files). There
  are no build outputs at the example's project root anymore.
- `cmd/bosc/main.bs`, `main.bs`, `bld`, `bosc` at repo root — see
  above.

Don't `git add` these or treat them as in-progress changes unless
the user asks. They're outputs.

---

## Repo conventions

- **Single-pass compiler.** No optimizer. The compiler lowers each
  AST node directly to `.bs`. Performance comes from the register
  allocator at the bas layer.
- **Inline validation.** No `Validate()` pass. Validation lives in
  `ToAST` (structural / type-shape) and in `Compile`/`compileTop`
  (flow-sensitive). When the design notes "validation," that's
  where it happens.
- **Errors carry position.** Use `interpreterError{p, msg}` (the
  AST-level error) or `CompileErrorF(node, fmt, args…)` (which
  pulls the position off the node). Both flow through `fatalCtx` /
  `printErrorContext` for the five-line snippet. Don't print raw
  errors with `log.Fatalf` from inside the compile loop — the
  source context will be lost.
- **No `Co-Authored-By: Claude` lines** in commits, PR
  descriptions, or generated docs. (Repository owner: Kyle
  Nusbaum; this is a personal rule that overrides any default
  attribution behavior.)
- **Don't edit `x86_64.xml`** — it's the Intel reference dump
  that the encoder reads. Adding instructions means re-deriving
  this file, not patching it.

---

## Quick references

- **Language reference** — [DESIGN.md](DESIGN.md). Sections worth
  bookmarking: §Ownership, §Mutability and the Type System,
  §Static Initializers, §Directives Reference (the `.bs` table).
- **Tokens and keywords** — `cmd/bosc/lexer.go` top.
- **AST node enum** — `cmd/bosc/ast.go` top (`nodetype` constants).
- **Assembly directives** — DESIGN.md §Directives Reference, and
  `cmd/bas/main.go` for actual parsing.
- **Runtime API** — DESIGN.md §Built-in Functions, with sources
  in `runtime/string/*.bs`.
- **ABI** — DESIGN.md §Calling Convention (System V AMD64 verbatim).
- **Build system** — DESIGN.md §Build System (mmk + boson.mmk).
- **Implementation status** — DESIGN.md §Implementation Status
  lists what is and isn't implemented as of the doc's last update.

---

## When the design doc disagrees with the code

DESIGN.md is treated as the canonical reference but it's hand-
maintained. If you find a divergence:

1. Code is the source of truth for *current behavior*.
2. If the divergence is a missing feature or a known TODO, leave it
   alone unless the user asks for the change.
3. If the divergence is the design doc being stale on a finished
   feature, update DESIGN.md as part of the work.

The repo prefers **conservative defaults with explicit opt-out**:
`const` before `var`, immutable references before `mut`,
non-escaping borrows before owned, no implicit ownership
promotion. When adding a new feature, the question to ask first is
"what's the strictest form, and what's the explicit opt-out?"
