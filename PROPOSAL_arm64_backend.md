# Proposal: arm64 Backend for bas and bosc

## Summary

Add an arm64 target to `bas` and a matching code-generation mode to `bosc`.
This requires turning the current implicit x86-64 backend into explicit target
interfaces for assembly parsing, instruction encoding, register allocation,
ABI lowering, runtime assembly, and executable linking.

The target name should be `arm64` for user-facing flags, with `aarch64` allowed
internally when matching architecture documentation.

## Motivation

The current toolchain targets x86-64 Linux and static ELF64 executables. That
was a good first backend, but it is now baked into several layers:

- `bosc` emits bas assembly that assumes x86-64 registers, x86 addressing, and
  System V calling rules.
- `bas` parses one assembly language and calls `gbasm.Encode`, which is backed
  by the x86 instruction database.
- `gbasm.Function` prologue/epilogue and register allocation assume x86-64.
- The runtime has Linux/x86-64 `.bs` files.
- The linker can only produce ELF in practice; Mach-O is a stub.

Adding arm64 by patching conditionals through those paths would make later
targets harder. The right change is to define target/backend boundaries first,
then implement arm64 behind them.

## Goals

- Add `bas -target=linux/amd64|linux/arm64|darwin/arm64` or equivalent.
- Add `bosc -target=...` that emits target-appropriate `.bs`.
- Preserve the current amd64 behavior while introducing target abstraction.
- Make backend-specific register sets, instruction selection, ABI rules, and
  assembly syntax explicit.
- Support enough arm64 to compile normal Boson programs, including calls,
  globals, structs, slices, interfaces, values types, bounds checks, and runtime
  calls.
- Prepare the linker boundary for Mach-O/darwin arm64 without requiring that
  all linker work land in the same patch.

## Non-goals

- Optimizing generated arm64 code in the first version.
- Supporting 32-bit ARM.
- Supporting dynamic linking.
- Supporting every arm64 instruction form before codegen needs it.
- Rewriting Boson front-end semantics.

## Target model

Introduce a target descriptor shared by `bosc`, `bas`, and `bld`:

```go
type Target struct {
    OS     string // linux, darwin
    Arch   string // amd64, arm64
    ABI    string // sysv, linux-arm64, darwin-arm64
    Format string // elf64, macho64
}
```

User-facing spellings:

- `linux/amd64`: current default.
- `linux/arm64`: first arm64 bring-up target.
- `darwin/arm64`: Apple Silicon target, dependent on Mach-O support.

The default should remain `linux/amd64` until another target is complete.

## bas changes

`cmd/bas/main.go` is currently both parser and assembler driver. Split it into:

- Target-independent directive parsing for Boson object metadata:
  `package`, `function`, `arg`, `var`, `data`, `struct`, `typealias`,
  `interface`, `values`.
- Target-specific instruction parsing and encoding.
- Target-specific pseudo-ops for prologue, epilogue, stack slots, calls, and
  relocations.

Suggested interfaces:

```go
type AsmBackend interface {
    Target() Target
    ParseOperand(text string) (Operand, error)
    Encode(inst Instruction, fn *ObjectFunction) error
    Prologue(fn *ObjectFunction) error
    Epilogue(fn *ObjectFunction) error
    RelocationKind(inst Instruction, operand Operand) (RelocKind, bool)
}
```

The existing x86 path can become `backend/amd64` with minimal behavioral
changes. arm64 then adds:

- Registers `x0-x30`, `sp`, `zr`, and `w0-w30`.
- Load/store syntax for `[xN, #imm]`, `[xN, xM, lsl #scale]` as needed.
- Branch/call relocations.
- Literal/global address materialization pseudo-ops.
- Stack-frame prologue and epilogue.

## bosc changes

`cmd/bosc/compile.go` currently writes textual `.bs` directly, often with x86
instructions such as `mov`, `lea`, `push`, `call`, and x86 memory forms. This
should be split into a target-independent lowering layer and target-specific
emitters.

Near-term structure:

```go
type CodegenBackend interface {
    Target() Target
    WordSize() int
    ArgLocations(sig Signature) []Location
    ReturnLocations(sig Signature) []Location
    EmitMove(dst, src Value)
    EmitLoad(dst Value, addr Address, size int, signed bool)
    EmitStore(addr Address, src Value, size int)
    EmitBinary(op BinaryOp, dst, lhs, rhs Value, typ ASTType)
    EmitCompare(...)
    EmitCall(...)
    EmitGlobalAddress(...)
    EmitBoundsCheck(...)
}
```

This does not require a full SSA IR immediately. A practical first step is a
small backend abstraction underneath the existing `compileTop` traversal:

1. Keep AST and checker unchanged.
2. Replace direct `fmt.Fprintf(of, "\tmov ...")` sites with backend methods in
   high-traffic code paths.
3. Move amd64 textual emission into an amd64 backend.
4. Implement arm64 emission behind the same methods.

Longer term, a typed low-level IR would make both backends cleaner, but the
first milestone should avoid a compiler rewrite before the target boundary is
proven.

## ABI requirements

The backend must own ABI facts, not scatter them through `compile.go` or
`function.go`.

For `linux/arm64`:

- Integer/pointer args in `x0-x7`.
- Integer/pointer returns in `x0` and `x1` where Boson multi-return needs it.
- Stack 16-byte aligned at public call boundaries.
- Caller-saved and callee-saved sets per AAPCS64.
- Linux syscall convention: syscall number in `x8`, args in `x0-x5`, `svc #0`.

For `darwin/arm64`:

- Apple arm64 calling convention details must be captured separately from
  Linux, especially syscall and process-entry behavior.
- Mach-O symbol naming and relocation rules belong to the object/linker layer,
  not to source-level Boson semantics.

## Runtime changes

Runtime assembly must become target-specific by filename or directory:

```text
runtime/_init/init_linux_amd64.bs
runtime/_init/init_linux_arm64.bs
runtime/_init/init_darwin_arm64.bs
runtime/_heap/heap_linux_amd64.bs
runtime/_heap/heap_linux_arm64.bs
runtime/string/puts_linux_amd64.bs
...
```

The build logic should select runtime files by target. Existing generic Boson
runtime packages such as `runtime/io/io.bos` can stay shared when they call
target-specific low-level packages.

## Object format and relocations

The `.bo` format should record target identity. A linker should reject mixed
targets before doing symbol work.

Relocations should be explicit enough for multiple ISAs:

- amd64 PC-relative branch/call relocation.
- amd64 absolute 64-bit data relocation.
- arm64 branch26 relocation.
- arm64 adrp/add or adrp/ldr page-based global-address relocation.
- Mach-O-flavored relocation mapping for darwin.

The internal `DataReloc{Offset, Symbol, Addend}` shape is not enough to express
all arm64 and Mach-O cases cleanly. Introduce a general relocation record with
kind, width, addend, target section, and architecture-specific validation.

## Linker changes

`gbasm.LinkExe` should take `Target`, not the current small `platform` enum.
Linking should produce a target-neutral `LinkedBin` first, then write ELF64 or
Mach-O64 through a format writer.

Required split:

- Symbol reachability and section placement: mostly target-neutral.
- Relocation application: target-specific.
- Executable format writing: ELF64 or Mach-O64.
- Entry-point policy: target and runtime-specific.

## Implementation plan

### Stage 1: make amd64 explicit

- Add target flag parsing to `bosc`, `bas`, and `bld`.
- Record target in `.bo`.
- Rename current behavior to `linux/amd64`.
- Move x86 register/instruction/prologue knowledge behind amd64 backend
  packages without changing generated output.
- Keep all existing tests passing.

### Stage 2: relocation and linker cleanup

- Replace ad hoc relocation records with typed relocation kinds.
- Teach `bdump` to display relocation kinds and target.
- Make linker reject mixed-target `.bo` files.
- Keep ELF64 amd64 output byte-for-byte stable where practical.

### Stage 3: arm64 assembler backend

- Add arm64 registers and operand parser.
- Encode the small instruction subset needed by compiler output:
  `mov`, `add`, `sub`, `mul`, signed/unsigned div, compare, conditional
  branch, branch, call, return, load/store sizes, sign/zero extension, stack
  adjustment, and syscall.
- Add focused bas tests for each instruction family and relocation kind.

### Stage 4: arm64 bosc backend

- Implement arm64 codegen for scalar expressions, locals, calls, branches, and
  returns.
- Add structs, slices, globals, bounds checks, interfaces, and values types.
- Port runtime assembly for `linux/arm64`.
- Run the compiler integration suite under emulation or native arm64 CI.

### Stage 5: darwin/arm64

- Add Mach-O writer support from `LinkedBin`.
- Add darwin arm64 runtime entry and syscall/libSystem strategy.
- Add codesigning/ad-hoc signing if required by the execution path.

## Testing

- Existing `make test` must continue to cover `linux/amd64`.
- Add `bas` golden tests per arm64 instruction form.
- Add object round-trip tests with `bdump`.
- Add linker relocation tests that inspect encoded branch and global-address
  fixups.
- Run Boson integration tests for `linux/arm64` on native arm64 or QEMU.
- Add cross-target negative tests for mixed `.bo` linking.

## Risks

- arm64 global address materialization requires paired relocations; treating it
  like amd64 absolute data relocation will not scale.
- bosc currently mixes semantic lowering and textual x86 emission. The backend
  seam must be introduced carefully to avoid a flag-driven duplicate compiler.
- Darwin arm64 is not just "arm64 plus Mach-O"; process entry, syscalls,
  runtime startup, and signing constraints need their own plan.

## Open questions

- Should the first arm64 target be `linux/arm64` for easier CI, or should work
  go directly to `darwin/arm64` because Apple Silicon is the motivating use
  case?
- Should `.bs` syntax become portable pseudo-assembly, or should it remain
  target-specific text selected by `bas -target`?
- How much low-level IR is worth introducing before arm64 codegen starts?

