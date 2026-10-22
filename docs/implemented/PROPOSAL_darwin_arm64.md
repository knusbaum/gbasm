# Darwin ARM64 backend

## History

- 2026-10-22 — Implemented native Darwin compilation, metaassembly, Mach-O payloads/linking, startup and runtime adaptation, with native compiler-corpus verification.

Boson now compiles and runs on Apple Silicon. The current specification lives
in DESIGN.md under Target selection and Darwin ARM64.

The initial implementation deliberately uses Apple's integrated assembler and
linker rather than building another instruction database and signing writer.
Boson owns metadata, named storage, explicit register bindings, instruction
lowering, frames, and its internal calling convention. Native Mach-O payloads
preserve Apple's relocation records rather than squeezing page/branch
relocations into the existing x86 PC-relative relocation shape.

The compiler's backend interface initially adapts the shared two-operand
instruction stream. This preserves the existing frontend and x86 lowering while
providing an ARM implementation. Replacing that stream with typed operations,
adding Linux/ARM64, and writing an independent ARM encoder remain possible
follow-up work; none is necessary to run the Darwin target.

Build tools directly when mmk is unavailable:

```sh
go build ./cmd/bosc
go build ./cmd/bas
go build ./cmd/bld
```

With mmk installed, build the standard example:

```sh
cd examples/hello
BOSON_HOME=../.. BOSON_TARGET=darwin/arm64 mmk
./target/hello
```

Apple Command Line Tools must be installed. The Darwin executable links
libSystem and is signed ad hoc by the platform linker. This produces a native macOS executable.
Target changes invalidate build-library artifacts through `.target` sidecars.

Verification:

```sh
go test ./internal/arm64 ./macho ./cmd/bosc .
BOSON_ARM64_ALL_TESTS=1 go test ./internal/arm64 -run TestDarwinPrograms
```

The native harness builds the real tools and runtime, then compiles and executes
existing compiler tests. It covers scalar widths, signed overflow, floating-point
NaNs, calls and stack arguments, globals/data relocations, structs and slices,
interfaces and assertions, variadics, ownership paths, and heap zeroing/release.
Apple's stack-entry alignment residue differs from x86, so the architecture
probes assert zero on ARM. Tests without `.expected` files follow the existing
suite's exit-status-only behavior.

Porting also exposed two shared lowering defects: nullable owned fields needed
full-width zero stores, and variadic aggregate elements needed materialized
addresses rather than nested memory operands. Variadic headers now carry the
canonical data/length/capacity layout. Existing language tests plus a multi-slice
variadic regression exercise those corrections.

The historical x86 Mach-O writer remains available in `macho/macho.go` for
reference. Production Darwin linking uses `macho/native.go`.
