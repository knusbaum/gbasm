# Proposal: Revive Mach-O Executable Support

## Summary

Revive Mach-O output by replacing the old disconnected x86-64-only
`macho.WriteMacho(text []byte)` path with a real Mach-O64 writer fed by the
same `LinkedBin` and relocation model used by the linker. The primary target
should be `darwin/arm64` so Boson can eventually compile programs for Apple
Silicon Macs.

Mach-O work should be coordinated with the arm64 backend proposal. A Mach-O
writer without arm64 codegen cannot satisfy the Apple Silicon goal; arm64
codegen without Mach-O cannot produce native macOS executables.

## Motivation

The current repository has a `macho/` package, but it is not wired into the
live linker. `LinkExe` panics for `MACHO`, while `ELF` writes an ELF64
executable from the target-neutral linked sections. The Mach-O package writes
one x86-64 text blob with hardcoded headers, a `LC_UNIXTHREAD` command, and no
relationship to current object files, data sections, symbols, imports, or
relocations.

Reviving Mach-O should therefore mean building a new Mach-O writer around the
current linker architecture, not trying to patch the old text-only stub.

## Goals

- Add Mach-O64 executable output from `LinkedBin`.
- Support `darwin/arm64` as the primary design target.
- Keep format writing separate from symbol reachability and code generation.
- Represent Mach-O segments, sections, symbols, and relocations explicitly.
- Support enough executable structure for simple statically linked Boson
  programs on Apple Silicon.
- Provide tests that parse the generated file and verify headers, load
  commands, sections, symbols, entry point, and relocations.

## Non-goals

- Universal/fat binaries in the first version.
- Dynamic library generation.
- Debug info.
- Full cgo/libSystem integration in the first milestone.
- Reusing the current `macho.WriteMacho(text []byte)` API.

## Current-state evidence

The current linker surface:

- `LinkExe(exename string, p platform, os []*OFile)` supports `ELF` and panics
  for `MACHO`.
- `Link(os, ENTRY_ADDR)` produces a `LinkedBin` with sections and symbols.
- `LinkedBinToElfSections` maps those sections into ELF writer structures.

The current Mach-O surface:

- `macho/macho.go` has constants and structs for an x86-64 `mach_header_64`,
  segment command, section, and unix-thread command.
- CPU constants are x86-64 only.
- The writer accepts only `text []byte`.
- It has comments noting malformed-file behavior.
- It does not consume `.bo` files, `LinkedBin`, data sections, symbols, or
  relocation metadata.

This is enough scaffolding to preserve as reference material, but not enough
to use as the live Mach-O backend.

## Proposed architecture

Change executable writing to:

```go
func LinkExe(exename string, target Target, os []*OFile) error {
    bin := Link(os, target)
    switch target.Format {
    case "elf64":
        return elf64.Write(exename, target, bin)
    case "macho64":
        return macho.Write(exename, target, bin)
    }
}
```

The linker should produce:

- Logical sections: text, data, rodata if split later, bss if added later.
- Final virtual addresses and file offsets.
- Symbols with section, address, size, and kind.
- Relocations that have already been resolved where possible or are expressible
  to the file writer if Mach-O relocation records are needed.

The Mach-O writer should not know about Boson packages or source-level
constructs. It should know about Mach-O layout.

## Mach-O layout

For `darwin/arm64`, start with a minimal executable:

- `mach_header_64`
- `LC_SEGMENT_64 __PAGEZERO`
- `LC_SEGMENT_64 __TEXT`
- `__TEXT,__text`
- `LC_SEGMENT_64 __DATA` or `__DATA_CONST` as needed
- `__DATA,__data`
- `LC_SYMTAB`
- `LC_DYSYMTAB` if required by macOS tooling
- `LC_LOAD_DYLINKER` if using dyld
- `LC_MAIN` entry point
- `LC_BUILD_VERSION`
- `LC_CODE_SIGNATURE` if ad-hoc signing is required for execution

Prefer `LC_MAIN` over the old `LC_UNIXTHREAD` approach for modern macOS
executables unless testing proves a lower-level static form is viable.

## arm64 Mach-O constants

Add explicit constants for:

- `CPU_TYPE_ARM64`
- `CPU_SUBTYPE_ARM64_ALL`
- `MH_EXECUTE`
- `MH_NOUNDEFS`
- `MH_DYLDLINK`
- `MH_TWOLEVEL`
- `MH_PIE`
- `LC_SEGMENT_64`
- `LC_SYMTAB`
- `LC_DYSYMTAB`
- `LC_LOAD_DYLINKER`
- `LC_MAIN`
- `LC_BUILD_VERSION`
- `LC_CODE_SIGNATURE`

Do not import a large third-party Mach-O writer unless it materially reduces
risk. The file format subset needed here is small enough to write directly, and
tests can parse output with Go's standard `debug/macho`.

## Relocations

Mach-O arm64 relocation is the central technical risk. The arm64 backend should
emit relocation kinds that can map to Mach-O:

- Branch/call relocation for `bl`/`b`.
- Page relocation for `adrp`.
- Page-offset relocation for `add` or load/store.
- Pointer-sized data relocation.

This proposal depends on the arm64 backend proposal's general relocation record.
The Mach-O writer should reject relocation kinds it cannot encode rather than
silently writing incorrect binaries.

## Runtime and entry

There are two possible runtime strategies:

### Direct kernel entry

Try to create a standalone executable with an entry point that reaches
`_init.start` directly, similar to the current Linux runtime model.

Pros:

- Keeps the current static-toolchain philosophy.
- Avoids depending on C runtime setup.

Cons:

- Modern macOS may not support the same direct syscall/process-entry model for
  normal user programs.
- Codesigning and dyld expectations can make "fully static" Mach-O execution
  brittle.

### dyld/libSystem entry

Emit a normal dynamically linked Mach-O executable that starts through dyld and
uses libSystem for process startup or syscall wrappers.

Pros:

- More likely to behave like normal macOS executables.
- Easier to satisfy current loader expectations.

Cons:

- Introduces dynamic linking concepts the current linker does not have.
- Requires import stubs, symbol binding, and more Mach-O commands.

Recommendation: build the writer so it can express modern load commands, then
prototype the smallest runnable `darwin/arm64` program early. Let that result
decide whether the first supported path is direct entry or dyld/libSystem.

## Codesigning

Apple Silicon systems commonly require code signatures even for local
executables. The proposal should assume ad-hoc signing is part of the final
developer experience.

Two implementation options:

- Emit a valid ad-hoc `LC_CODE_SIGNATURE` directly.
- Provide a development workflow that runs `/usr/bin/codesign -s -` after
  linking on macOS.

Direct emission is better for a self-contained toolchain, but invoking
`codesign` may be acceptable for the first runnable milestone.

## Implementation plan

### Stage 1: replace the dead API

- Leave the old `macho.WriteMacho(text []byte)` as historical reference or
  remove it after tests cover the new writer.
- Add `macho.Write(exename string, target Target, bin LinkedBin) error`.
- Add unit tests that write a tiny `LinkedBin` and parse it with
  `debug/macho`.

### Stage 2: layout and load commands

- Implement Mach-O64 header writing.
- Implement `__PAGEZERO`, `__TEXT`, and `__DATA` segment commands.
- Implement section headers with correct file offsets and VM addresses.
- Add `LC_MAIN` and `LC_BUILD_VERSION`.
- Add `LC_SYMTAB` with local symbols.

### Stage 3: arm64 relocation mapping

- Add Mach-O relocation encoders for the relocation kinds produced by the
  arm64 backend.
- Add tests for branch, page, page-offset, and pointer relocations.
- Reject unsupported relocation kinds with precise errors.

### Stage 4: minimal runnable macOS program

- Add `darwin/arm64` target selection to `bld`.
- Add or select a minimal `runtime/_init` for Darwin.
- Produce a hello-world executable.
- Verify on Apple Silicon with `file`, `otool -l`, `otool -rv`, and execution.

### Stage 5: integrate with bosc/bas target selection

- Ensure `.bo` target identity flows from `bas` to `bld`.
- Run simple Boson programs through:

```text
bosc -target=darwin/arm64
bas  -target=darwin/arm64
bld  -target=darwin/arm64
```

## Testing

- Unit-test binary encoding of each load command.
- Parse generated files with `debug/macho`.
- Use `otool` tests on macOS CI when available.
- Verify `file` reports `Mach-O 64-bit executable arm64`.
- Run smoke executables on Apple Silicon.
- Add negative tests for unsupported relocation kinds and mixed-target objects.

## Dependencies

This proposal depends on, or should land alongside:

- A shared `Target` descriptor.
- `.bo` target metadata.
- General relocation records.
- arm64 bas instruction encoding.
- arm64 bosc code generation.
- Darwin-specific runtime startup.

## Open questions

- Is the first runnable milestone allowed to call `/usr/bin/codesign`, or must
  `bld` emit ad-hoc signatures itself?
- Can Boson keep a direct `_init.start` model on modern macOS, or should Darwin
  use dyld/libSystem from the start?
- Should Mach-O support be limited to arm64, or should x86-64 Mach-O be kept as
  a secondary test target once the writer is real?

