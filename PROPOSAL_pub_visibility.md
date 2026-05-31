# Proposal: `pub` Visibility Modifier

## Status

Implemented in the current working tree. The core visibility model, runtime
migration, bdoc public filtering, and negative visibility tests are complete.

The intentionally-deferred item is a future `bosc --show-exports` command that
prints a doc-comment-preserving public header view.

## Summary

Add a single keyword, `pub`, as a leading modifier on top-level declarations
(`fn`, `type`, `var`, `const`, `interface`) in `.bos` source, with an analogous
modifier on the corresponding `.bs` directives. Declarations without `pub` are
**package-private** — they exist in their declaring package but cannot be
referenced through a qualified `pkg.name` from any other package. A
future `bosc --show-exports <pkg>` driver mode can print a package's public
surface as a synthesized "header" view; `bdoc` is updated now to render the
same public-only surface (with `_`-prefixed runtime packages hidden from
discovery).

This replaces today's de-facto rule that everything declared at top level is
reachable cross-package once the package is imported.

## Motivation

Boson currently has no visibility system. Every top-level declaration in a
package is implicitly exported the moment another package imports it. Two
things follow:

- Package authors have no way to hide helpers, internal types, or
  implementation-detail vars. Everything is part of the API by accident.
- There is no consolidated "what does this package expose?" view. Readers must
  skim every `.bos` file in the package to assemble the public surface in
  their head.

The conventional answers are capitalization (Go) or an `export {}` manifest.
Capitalization conflates naming style with visibility and forecloses on
short-lower-case type names; an explicit manifest duplicates declarations and
drifts from the bodies. A single `pub` keyword at the declaration site avoids
both, and the "header view" can be synthesized on demand by the compiler
rather than maintained by hand.

## Goals

- Add a `pub` keyword that marks individual top-level declarations as
  cross-package-visible.
- Default to **private** — an unmarked declaration is invisible outside its
  declaring package.
- Apply uniformly to `fn`, `type` (struct, alias, `values`), `interface`,
  `var`, `const`.
- Add an analogous leading modifier in `.bs` for hand-written runtime code.
- Carry the visibility bit through the `.bo` format so the consumer-side
  compiler can enforce visibility at name-resolution time.
- Update `bdoc` so the default index, per-package landing pages, and search
  results render only the `pub` surface.
- As a related, separable cleanup, hide packages whose name begins with `_`
  from `bdoc`'s discovery walk so runtime-internal packages stop appearing
  in the casual documentation view.

## Non-goals

- **No field-level visibility.** A `pub type` exposes all of its fields. The
  cases for an opaque/handle type are deferred until needed.
- **No per-method visibility within a type block.** Methods live inside the
  `type Name <kind> { ... }` declaration and share the type's visibility.
- **No re-exports** (`pub use pkg.thing`). Out of scope.
- **No selective exports through a manifest file.** The declaration site is
  the only place visibility is marked.
- **No retroactive enforcement on `.bo`-only references.** The visibility
  check is enforced by bosc when it imports producer `.bo` files and resolves
  source-level `pkg.name` references. The linker continues to resolve any
  symbol it is asked to.

## Core Rule

A leading `pub` keyword on any of these top-level forms marks that single
declaration as exported:

```boson
pub fn puts(s byte[]) { ... }
pub type point struct { x i64, y i64 } { ... }
pub type io_error values { ... } { ... }
pub interface reader { ... }
pub var STDOUT FD = FD(1)
pub const MAX i64 = 1024
```

Without `pub`, the declaration is private to its package. Other packages
cannot:

- Call it (`pkg.fn(...)`).
- Use the type in a type position (`pkg.T`, `pkg.T(...)`, `pkg.T{...}`).
- Read or write the var/const (`pkg.STDOUT`).
- Implement or refer to the interface (`pkg.reader`).

Bare references inside the declaring package are unaffected.

### Methods are part of their type's declaration

Methods are declared inside a `type Name <kind> { ... }` block, not as
standalone functions. Marking the type `pub` makes all of its methods
reachable through qualified dispatch (`pkg.T`'s instance and static methods
satisfy interfaces, can be called, etc.). There is no per-method `pub`.

This is the cleanest consequence of Boson's syntactic choice to keep methods
inside the type block: visibility has only one place to live.

### Struct fields follow the type

A `pub type point struct { x i64, y i64 }` exposes both fields to importers.
There is no `pub` for individual fields. If a package wants a value-typed
struct whose internals are hidden, the corresponding "opaque" form can be
added later (e.g. `pub opaque type`); the present proposal does not include
it.

### Anonymous types in pub signatures

Anonymous struct types appearing in the signature of a `pub fn` (e.g.
`fn divide(...) struct { quot i64, rem i64 }`) are exposed by virtue of the
signature itself. They are not declarations and need no `pub`.

### `builtin`

`builtin` is auto-imported into every compilation unit. Anything `builtin`
needs to expose to user code (today: `error`) must be marked `pub`. The
auto-import does not bypass the visibility check.

## `.bs` analogue

`.bs` directives accept `pub` as a leading modifier:

```
pub function strlen
pub var STDOUT i64
pub data BANNER byte[] = "..."
pub struct point { x i64, y i64 }
pub typealias FD i64
pub interface reader { ... }
```

Hand-written runtime code (the entire `runtime/*` tree today) opts in to
cross-package visibility by adding `pub` to whatever it expects user code to
call. `bas` records the visibility bit in the emitted `.bo`; bosc enforces it
when another package imports that `.bo`. Symbols without `pub` are still
emitted for intra-package use, but bosc will not register them as importable
from `.bos`.

## `.bo` format

Each exportable entry in the `.bo` symbol/type/var tables gains an `IsPub`
bit:

- `Function.IsPub`.
- The vars/data table gains an `IsPub` per entry.
- `StructShape.IsPub`, `TypeAliasShape.IsPub`, `InterfaceShape.IsPub`,
  `ValuesShape.IsPub`.

On import, bosc loads the producer's `.bo` as it does today, but registers
only `IsPub` entries into the consumer `Context`. Private entries are simply
not visible to source-level qualified lookup. There is no separate private-name
diagnostic table.

The linker is unchanged. It continues to resolve whatever the consumer's
`.bo` asked for. Hand-written `.bs` that explicitly writes
`call other_pkg.private_fn` would still link; the visibility rule is a
source-language constraint, not a link-time one.

## Deferred: `bosc --show-exports <path>`

A future driver mode should read a package (via `BOSONPATH` resolution, the
same way `bdoc` walks today) and print its public surface in declaration
order, one declaration per logical block, with attached doc comments:

```
$ bosc --show-exports io
package io

pub type FD i64 {
    read(self *FD, buf *mut byte[]) i64
    write(self *FD, buf byte[]) i64
    close(self owned *FD)
}

pub fn open(path byte[], flags i64, mode i64) owned FD

pub var STDIN FD
pub var STDOUT FD
pub var STDERR FD
```

Source-driven: it reads `.bos` and `.bs` files in the package and filters for
`pub`. This shares scanning code with `bdoc`, which already does the walk.
The `.bo` is not the source of truth here (it doesn't carry doc comments
today), but a separate `bosc --check-exports` could cross-check the `.bo`
against the source view if format drift becomes a concern.

## Updates to `bdoc`

The package-documentation server changes in lockstep with the language
change. Today `internal/bdoc/DiscoverPackages` walks `BOSONPATH` and the
server (`internal/bdoc/server.go`) renders every declaration that the
scanner produced. Two changes:

### Default to public-only views

The top-level index, the per-package landing page, and the search index
filter to declarations marked `pub`. A reader following the published
documentation sees the same surface a consumer of the package would see —
no private helpers, no internal types, no implementation-detail vars.

The scanner continues to record every declaration; filtering happens at
render time. That keeps a single source of truth and leaves room for a future
private-member view without requiring a re-scan.

### Hide `_`-prefixed packages from discovery

Packages whose name begins with `_` (today: `_init`, `_heap`, `_io_sys`) are
runtime internals — user code does not import them, the compiler emits
calls to them as part of code generation. They should not appear in the
package index or in search results. `DiscoverPackages` skips them.

This is a separable cleanup from the visibility rule (it would be
worthwhile even without `pub`), but landing both together gives readers a
clean public-surface view in one step.

## Migration

Every cross-package reference in the current tree needs the producer side
marked `pub`. The largest clusters:

- **Runtime (`.bs`).** `string.puts`, `string.puti`, `string.putc`,
  `string.exit`, `_init.start`, `_init.index_oob`, `_init.nil_assert`,
  `_heap.alloc`, `_heap.free`, `_io_sys.{read,write,open,close}`.
- **`io` (`.bos`).** `FD`, `STDIN`/`STDOUT`/`STDERR`, `open`.
- **`pair` (`.bos`).** The whole exported surface used by cross-package
  tests under `cmd/bosc/tests/`.
- **`builtin`.** `error`.
- **Cross-package tests.** Any test that imports a sibling test fixture
  package and expects to call into it.

Migration is mechanical: identify every name that appears in a qualified
`pkg.name` reference anywhere in the tree and ensure its declaration in `pkg`
has `pub`. A one-shot script driven by `bdump`/`bosc -listimports` can
enumerate the producer declarations and the visibility test suite catches
regressions.

## Implementation impact

Following the layering note in `CLAUDE.md`:

1. **Done: `.bo` shape** (`bwrite.go`, `ofile.go`) — add `IsPub` fields to
   `Function`, the vars/data table, `StructShape`, `TypeAliasShape`,
   `InterfaceShape`, and `ValuesShape`. Because Boson has no compatibility
   commitment yet, old `.bo` files can simply fail to read until rebuilt.
2. **Done: `.bs`** (`cmd/bas/main.go`) — accept `pub` as a leading modifier on
   `function`, `var`, `data`, `struct`, `typealias`, `interface`, and values
   declarations if/when `.bs` grows a values directive. Record into the
   corresponding `.bo` entry. Unmarked `.bs` directives are private.
3. **Done: bosc lexer/parser** (`cmd/bosc/lexer.go`, `cmd/bosc/parser.go`) — add
   `pub` to the `keywords` map and accept it as a leading modifier before
   `fn`, `type`, `interface`, `var`, `const` at file scope. Reject `pub`
   elsewhere with a directed error.
4. **Done: AST and codegen** (`cmd/bosc/ast.go`, `cmd/bosc/compile.go`,
   `globals.go`) — add an `IsPub` bit to the affected top-level node types
   and propagate it to the `.bo` entries written for functions, var/data, and
   type/interface/values shapes. Unmarked declarations are private.
5. **Done: import filtering / name resolution** (`cmd/bosc/ast.go`,
   `cmd/bosc/resolve.go`) — switch `Context.Import` to register only public
   producer entries in the consumer context. After this step, unmarked
   declarations are private and qualified lookups fail the same way as any
   missing imported name.
6. **Done: migration** — add `pub` to the runtime, builtin, test fixture, and example
   declarations that are intentionally referenced cross-package. Then remove
   the temporary "default public" compatibility path from `.bos` and `.bs`.
7. **Done: `bdoc`** (`cmd/bdoc/main.go` + `internal/bdoc/`) —
   `internal/bdoc/discover.go` skips packages whose name begins with `_`.
   `internal/bdoc/server.go` filters the index, per-package landing pages,
   and search results to `pub`-only declarations at render time. The
   `PackageScan` produced by `internal/bdoc/scan.go` continues to record
   every declaration so future private/developer documentation can be added as
   a pure render-time option.
8. **Future work: driver mode** — add `bosc --show-exports <package>` that
   reuses the scanning code from `bdoc` and prints a textual header with doc
   comments.
9. **Done: linker** — no changes.
10. **Done: tests** — the existing `cmd/bosc/tests/` suite needs `pub` added to
    every cross-package fixture symbol. A new set of negative tests
    (`*_err_test.bos`) covers attempted import-side use of private fn, type,
    var, const, interface, and values declarations.

## Open Questions

- **Opaque types.** A `pub opaque type T struct { ... }` that exports
  identity and layout but hides fields is the obvious next step if the
  "all-fields-public" rule starts to bite. Deferred until it does.
- **Field-level visibility.** Same as above. Only worth adding when the
  opaque form is insufficient.
- **Re-exports.** A `pub import "..."` or `pub use pkg.thing` form would let
  a facade package re-publish symbols. Not currently needed.
- **`.bo` doc comments.** Adding doc comments to the `.bo` would let
  `--show-exports` work from the binary alone, which would simplify offline
  rendering. Probably worth doing once `bdoc` has settled.
- **Visibility for `extern` / C-ABI bindings.** Boson does not currently
  have an `extern` form. If one is added, the visibility rule applies to it
  the same way it applies to `fn`.

## Future Extensions

If demand arises, the natural follow-ons are, in increasing order of
intrusiveness:

1. `pub opaque type T struct { ... }` — exposes identity/size, hides
   fields. Methods on the type are the only way for importers to read or
   write internals.
2. `pub(crate)`-style scoped visibility — friend-package visibility, where
   a declaration is visible to a named set of packages but not the world.
   Probably overkill for Boson.
3. `pub use` re-exports for facade packages.
4. A private/developer mode in `bdoc` that renders all declarations,
   including private members and `_`-prefixed runtime packages, for local
   package-author workflows.

None of these are part of this proposal; they are listed only to confirm
that the chosen design leaves room for them.
