# Implementation Plan: `values` Types With Static Projections

## Context

`PROPOSAL_values_with_static_data.md` proposes a `values` form for declaring a
finite symbolic value type with optional static projections (e.g. an integer
code, a `byte[]` message). The motivating use is allocation-free error
interfaces: a `values` value is just a private `i64` tag at runtime, and
declared projections (`i64`, `byte[]`, ...) live in compiler-generated static
tables.

The proposal also identifies a prerequisite cleanup: today the compiler decides
what `A.B[.C]` means in several disjoint places (`Symbol.ASTType`,
`Dot.ASTType`, parser folds that rewrite `pkg.fn(args)` into
`Funcall{Pkg, FName}`, and the `n_stlit` flattening that stores qualified type
names as the string `"pkg.Type"`). Adding values cases as a new selector
meaning without unifying that path will let type checking and code generation
disagree.

This plan executes the proposal in six staged, behavior-preserving (or
strictly additive) milestones. Stage 1 lands the selector refactor; stages 2–4
build the local-only values feature on that base; stage 5 adds cross-package
metadata; stage 6 updates tooling. Each stage is independently testable
against the existing integration suites under `cmd/bas/tests/` and
`cmd/bosc/tests/`.

## Critical Files (current locations)

Front end and AST
- `cmd/bosc/lexer.go` — keyword table (top of file). Add `tok_values`.
- `cmd/bosc/parser.go`
  - `parseTypeDecl` at lines 1341–1366 (today: alias or alias+methods only).
  - Qualified struct literal flattening at line 677 (`pkg.Type{...}` →
    `n_stlit` with `sval = "pkg.Type"`).
- `cmd/bosc/ast.go`
  - `Context` decl maps at lines 23–96 (`structs`, `typeAliases`,
    `interfaceDecls`, `funcs`, `imports`, `importedVars`, `typeMethods`,
    `vtables`).
  - `typeIsMemoryBacked` at lines 125–149.
  - `Symbol.ASTType` at lines 2776–2785; `Dot.ASTType` at lines 2024–2057.
  - `Funcall` struct at lines 1866–1872 (carries `Pkg string`).
  - Parser fold `Dot(Symbol("pkg"), Funcall("fn"))` → `Funcall{Pkg, FName}` at
    lines 2950–2963.
  - Decl registration paths: `c.DefineStruct`, `c.DefineTypeAlias`,
    `c.DefineInterface`; type-with-methods at lines 3140–3189.
  - Import handling: `Context.Import` at lines 906–1006.

Codegen and lowering
- `cmd/bosc/compile.go`
  - `compileTop` switch starts ~line 1394. `case *Funcall` at ~1833 (with the
    cast-detection branch at 728–741).
  - Method emission for typealias-with-methods at lines 1478–1488 (`TypeName.method`).
  - Struct/typealias/interface metadata emit at lines 1468–1517.
  - `compileCast` at lines 3340–3404.
  - Vtable symbol naming (dot→underscore) at lines 4063–4065. Mirror for
    projection table symbols.
- `cmd/bosc/globals.go`
  - `relocSpec` at lines 14–18; `encodeStaticInit` at lines 90–153;
    single-slice-header path at 178–186; anonymous-global pattern at 62–70,
    245–249.

Object/link layer
- `ofile.go`
  - `StructShape` at lines 129–136; `DataReloc` at lines 34–51.
  - Add `ValuesShape` here.
- `bwrite.go`
  - `readTypeAliases` ~lines 674–704; `readInterfaces` ~746; `readStructs`
    ~613. Add `readValues`/`writeValues` siblings.
  - `TypeAliasShape.MethodNames` lives here (~line 146) — mirror for
    `ValuesShape.MethodNames`.

Tools
- `cmd/bdump/main.go` lines 38–92 — add a values-types dump section.
- `cmd/bdoc/scan.go` lines 204–250 — extend `tryBosDecl` / `parseBosType` for
  the `values` form.

Runtime / tests
- `cmd/bosc/tests/` — add positive and negative test pairs as each stage
  lands.
- `cmd/bas/tests/` — touched in stage 5 (new `values` assembler directive).

## Stage 1 — Selector Resolver Prerequisite

**Behavior-preserving.** Land first, all existing tests pass.

1. Add `ResolvedObject` / `ResolvedKind` in `cmd/bosc/ast.go`, with kinds
   `RuntimeValue`, `Package`, `Type`, `Function`, `StructField`. (Add
   `ValuesType`/`ValuesCase` kinds as inert placeholders now, populated in
   stage 2.)
2. Add `ResolveSelector(c *Context, root AST) ResolvedObject` plus a
   left-to-right `stepSelector(prev ResolvedObject, member string)`. Root
   resolution consults the package-tier maps as one logical step in the order
   already used today (locals → package-level → builtins), documented at the
   top of the resolver. Before adding any cross-map collision check, audit
   the current tree: grep every name across `structs`, `typeAliases`,
   `interfaceDecls`, `funcs`, `imports`, `importedVars` in both `runtime/`
   sources and every test fixture. If overlap is found, the current
   resolution order is load-bearing — preserve it verbatim and defer
   collision rejection to its own stage. Only if the audit is clean do we
   add rejection in stage 1.
3. Migrate `Symbol.ASTType` and `Dot.ASTType` to call the resolver and project
   to `ASTType` from the result. Migrate `compileTop`'s `*Dot` branch
   (~lines 1975–2032) to compile from a `ResolvedObject` rather than
   re-dispatching on the AST shape.
4. Replace `Funcall.Pkg string` with `Callee AST`. Touch every construction
   site:
   - The fold at ast.go:2950–2963 builds a `Funcall` with a resolved selector
     chain as `Callee`, not a raw package string.
   - `ReceiverExpr`-based method calls fold into the same `Callee` shape.
   - `QualifiedName()` and all readers of `Pkg` follow the new field.
   Codegen reads `Callee` through the shared resolver and routes free
   functions, method calls, and (future) values-type static methods through
   the same path. The resolver result tells the call site whether the callee
   is a free function, method, or function-pointer value.
5. Replace the parser flattening of `pkg.Type{...}` (parser.go:677). The
   `n_stlit` node carries either a single-symbol unqualified name or the
   resolved type identity (e.g. a `TypeRef` AST). `ToAST` for `n_stlit` reads
   that identity through the shared resolver. Codegen renders the
   package-qualified key it already needs without parsing a string.
6. Re-run `make test`. The entire suite must pass unchanged.

Open: keep the parser's existing AST shape for `Dot` chains (still
left-associative `Dot(Dot(...), member)`); the resolver walks it. No parser
inversion required.

### Stage 1 — Deferred polish

These items were called for in the proposal but deferred from the Stage 1
implementation because they are not required to land values v1:

1. **`compileTop`'s `*Funcall` dispatch still uses the legacy
   `PkgAndName`+`FuncDeclForCall`+`TypeForVar`+`MethodForType` chain rather
   than dispatching on a `ResolveSelector(c, ast.Callee)` result.** The
   resolver knows the leftmost identifier in the callee chain is an
   imported package, a local variable, a type, or a function; the call
   site re-derives that. This is deferred because values cases in v1 are
   not callable, so the dispatch's overloaded behavior never gets
   triggered by a values case. Re-route as polish after stage 5 lands.
2. **`ResolvedRuntimeValue` with `Pkg != ""` is the marker for a
   cross-package imported var read.** Stage 2 may want a distinct
   `ResolvedImportedVar` kind so that future additions (e.g. a
   `ResolvedValuesType` with a `Pkg` qualifier) do not have to share the
   meaning of `Pkg`. Reconsider when adding values to the resolver.
3. **Root-name precedence was changed silently:** the old
   `Dot.ASTType` checked imported packages BEFORE struct field access
   (which transitively goes through `Symbol.ASTType`'s local-binding
   lookup). The resolver checks locals first, then imports. Audit ran
   clean — no current test or runtime file uses an imported package
   name as a local binding — so no observable change in behavior, but
   this would surface if a user later wrote `var io i64 = 0`. Document
   the locals-first rule explicitly if a regression appears.

## Stage 2 — `values` Declaration: Parser + AST + Registration

1. Lexer: add `tok_values` to `cmd/bosc/lexer.go` and the `keywords` map.
   Contextual-keyword treatment is the proposal's stylistic preference
   (§279–282), but `parseTypeDecl` at parser.go:1348 calls `parseTypeName()`
   immediately after the type name, and `parseTypeName` would swallow
   `values` as an ordinary identifier without a one-token lookahead change.
   A reserved `tok_values` avoids that re-design at no real cost (no
   existing source uses `values` as an identifier; if a regression turns
   up, it is a renamable identifier in test fixtures only). Revisit
   contextualization as a polish item after the feature lands.
2. Parser: extend `parseTypeDecl` (parser.go:1341–1366) so that after
   `type Name`, the next token may be `values`. Add `parseValuesDecl`:
   - Optional projection signature `(T1, T2, ...)` after `values`.
   - Required `{ CASE_NAME [: expr1, expr2, ...] ; ... }` block.
   - Optional trailing `{ methods... }` block, reusing `parseMethodDef`.
   - Emit a dedicated node kind `n_valuesdecl` so AST construction does not
     need to recognize the values form by destructuring an alias node.
3. AST: add `ValuesDecl` and `ValuesCase` structs per proposal §510–526. Add
   `valuesDecls map[string]*ValuesDecl` to `Context` and a
   `Context.DefineValuesType` mirror of `DefineStruct`/`DefineTypeAlias`.
4. `Node.ToAST` for `n_valuesdecl` (ast.go) performs only declaration-shape
   validation that does not need the full type index:
   - Assign each case a dense, compiler-private `int64` tag starting at 0.
   - Validate duplicate case names (Diagnostic: "Duplicate value name ... in
     values type ...").
   - Validate duplicate projection types in the header (Diagnostic: "Values
     type ... declares projection ... more than once").
   - Validate per-case initializer arity against the header (Diagnostic:
     "Value ... has N projection initializers but ... declares M
     projections").
   - Register the decl via `c.DefineValuesType`.
   - Register methods through the same path as `n_typedecl_with_methods`
     (ast.go:3140–3189) into `c.typeMethods[name]`.
   Defer projection-type identity and per-initializer encodability checks to
   the compile pass (after all top-level decls are registered).
5. Update `typeIsMemoryBacked` (ast.go:125–149) so values types are treated
   as register-resident i64 (no change to behavior expected — i64 already
   falls through to register — but consult `valuesDecls` explicitly so the
   intent is unambiguous and future representations can change here).
6. Selector resolver (stage 1): wire `ResolvedValuesType.member` to return
   `ResolvedValuesCase` for declared cases. Add two distinct rejections in
   the dispatch table:
   - `ResolvedValuesCase.member` (`io_error.NOT_FOUND.foo`) — reject in v1
     with "no member ... on values case ..." (proposal §425–426).
   - `ResolvedRuntimeValue.member` where the value's type is a values type
     and `member` matches a declared case name — reject with the proposal
     §781 diagnostic "Cannot access case ... through values value e;
     reference as io_error.NOT_FOUND". This is the case-on-instance
     rejection from proposal §418 and is distinct from the previous one.
7. Tests: add parser-only positive and negative fixtures under
   `cmd/bosc/tests/` for duplicate case, wrong arity, etc. (`_err_test.bos`
   pairs).

## Stage 3 — Codegen for Cases, Methods, and Equality

1. Add `case *ValuesDecl` to `compileTop`. Emit:
   - A `.bo` metadata directive for the values type (stage 5 finalizes the
     wire format; for stage 3, gate on `inSamePackage` and skip metadata
     emit when only the local-pass is being exercised, or emit a placeholder
     directive that bwrite ignores).
   - Each method as `TypeName.method_name`, reusing the typealias path at
     compile.go:1478–1488. Receiver type is the values type; codegen passes
     the i64 tag directly (no slot allocation).
2. Case references in expression position: when `ResolveSelector` returns
   `ResolvedValuesCase`, codegen emits the tag bits as a value of the values
   type. Introduce an internal `ValuesTag` operand shape (proposal §531–550)
   only if existing constant machinery cannot cleanly emit a typed i64
   constant; otherwise route through the existing const path.
3. Equality (`==`, `!=`): the existing `n_deq` path at compile.go:3744–3761
   already does `cmp` then `sete` on the operand width. Resolve operand
   types through `c.ResolveUnderlying`; for two operands whose declared types
   are the *same* values type, this produces a correct tag comparison. Add
   a typing check that *rejects* equality between two different values types
   even when they share the i64 underlying (Diagnostic: "Cannot compare
   values of types ... and ...").
   Note: this is a deliberate asymmetry with ordinary type aliases
   (`type X i64`, `type Y i64`) which today compare via shared underlying
   i64. Per proposal §730–732, values types are closed symbolic sets, not
   numeric aliases, so cross-type comparison is rejected even though the
   underlying tag is i64. Do not generalize this restriction to
   `type X i64` aliases.
4. Reject integer-to-values casts in `compileCast`: when destType is a values
   type and srcType is not the same values type, emit "Cannot cast ... to
   ...: values cases must be constructed from declared cases".
5. Method satisfaction for interfaces: no new code path. The existing
   `interfaceSatisfactionError` (compile.go:200–262) iterates methods via
   `c.TypeMethodsFor(typeName)`, which already returns values-type methods
   once registered.
6. Tests: positive end-to-end (`fn main()` returns an interface backed by a
   values type, calls a method, prints, expects no allocation) and negative
   (integer cast, cross-type equality).

## Stage 4 — Projection Casts

### Pre-stage verification: arrays of slice headers

Before any stage 4 code lands, write the smallest possible probe and try
to compile it:

```boson
var t byte[][2] = {"a", "b"}
```

Inspect `globals.go`'s `encodeArrayLiteralBytes` (line 112) to confirm
whether it loops over array elements and, for each `byte[]` slot, emits
the two-qword slice header with a `relocSpec` against an anonymous global
holding the string bytes. If yes, stage 4 reuses the existing path. If no
— if the encoder only handles arrays of scalars today — extending it is a
substage of its own: add per-element relocation emission, plus a focused
test for static arrays of slice headers independent of values types.
Treat that substage as a prerequisite, not a bullet inside stage 4.

### Projection cast lowering

1. Extend `compileCast` (compile.go:3340–3404). After resolving
   `srcUnderlying`, if `srcType` resolves to a values type (via
   `c.valuesDecls`), branch:
   - If `destType` matches one of the declared projection types (compare via
     `ASTType.Same` over the registered projection signature), emit a table
     lookup. For a scalar projection (e.g. `i64`):
     ```text
     mov tmp, [__projection_<pkg>_<type>__i64 + 8*tag]
     ```
     For a `byte[]` projection (16-byte slice header), copy the two qwords
     into the destination spot.
   - Otherwise emit "Cannot cast ... to ...: ... has no ... projection".
2. Emit the projection tables themselves at values-decl compile time. Reuse
   `encodeStaticInit` for each initializer expression, passing the
   projection type as the destination context (so a `byte[]` initializer
   uses the same static slice-header path that
   `var s byte[] = "x"` uses today at globals.go:178–186).
3. Naming: emit projection table symbols as
   `__projection_<pkgQual>__<projTypeKey>` where `<pkgQual>` is the
   dot-to-underscore form mirroring the vtable convention at
   compile.go:4063–4065 and `<projTypeKey>` is a stable name for the
   projection type (`i64`, `byte_slice`, struct names with `.`→`_`).
4. Methods on values types can now use the projection cast directly:
   `byte[](e)` inside a method body is the same compile path as at any
   other call site. Verify with a test that mirrors the proposal's
   `io_error.message` example end to end.
5. Tests: per-projection-type pairs (i64 projection, byte[] projection, no
   projection rejection, integer→values rejection, casting between two
   different values types).

## Stage 5 — Cross-Package Metadata

The current pipeline for type metadata flows: bosc emits a textual directive
into `.bs` → `bas` parses it and writes a binary shape into `.bo` → importing
bosc reads the binary shape back. Every existing shape (`typealias`,
`struct`, `interface`) follows that three-step path. The new values
directive must too — skipping the bas step is not viable.

1. Add `ValuesShape` to `ofile.go` (alongside `StructShape` at lines
   129–136):
   ```go
   type ValuesShape struct {
       Name        string       // unqualified
       TagType     string       // v1 always "i64"
       Cases       []ValuesCaseShape
       Projections []ProjectionShape
       MethodNames []string     // mirrors TypeAliasShape.MethodNames
   }
   type ValuesCaseShape  struct { Name string; Tag int64 }
   type ProjectionShape  struct { TargetType, TableSymbol string }
   ```
   Also add an `OFile.Values []ValuesShape` field so the importer and
   `bdump` can enumerate values types alongside existing shapes.
2. `bwrite.go`: add `readValues`/`writeValues` mirroring `readTypeAliases` /
   `readStructs` etc. Include the binary section in the file write/read
   passes.
3. `cmd/bosc/compile.go`: emit a textual `values` directive in the same
   pre-codegen pass that emits `typealias`, `struct`, `interface`
   (lines 1468–1517). Schematic shape per proposal §641–652:
   ```text
   values <name> tag=i64 methods=[m1,m2] cases=[NAME:0,...] \
                 projections=[i64:<sym>,byte[]:<sym>]
   ```
   (Single-line, whitespace-separated, matching the existing directive
   style.)
4. `cmd/bas/main.go`: add a new substage. Register a directive-recognizer
   entry for `values` in the top-level directive loop (sibling to the
   existing `typealias`, `struct`, `interface` parsers). Parse the directive
   into a `ValuesShape` and write it via the new bwrite path. Add a paired
   integration test under `cmd/bas/tests/` (`values_directive_test.bs` +
   `.expected`) that confirms the directive round-trips through assembly
   and re-emerges in `bdump` output. Negative tests for malformed
   directives (`_err_test.bs`).
5. `cmd/bosc/main.go` / `Context.Import` (ast.go:906–1006): when a `.bo`
   carries a `ValuesShape`, register a `ValuesDecl` in the importing
   context's `valuesDecls` map. Method `FuncDecl`s are recovered from the
   imported function table via `TypeName.method_name` lookups, mirroring the
   typealias path at ast.go:956–966.
6. Cross-package case use: `io.io_error.NOT_FOUND` is resolved by the shared
   selector resolver from stage 1 (root `io` → `ResolvedPackage`; `.io_error`
   → `ResolvedValuesType`; `.NOT_FOUND` → `ResolvedValuesCase`). No new
   parser path is needed.
7. Cross-package projection casts use the imported `ProjectionShape` table
   symbol directly; relocations resolve at link time through the existing
   data-reloc path.
8. Tests: a small package under `cmd/bosc/tests/` (or under `runtime/` if a
   permanent fixture is preferable) that exports a values type with a method
   and a projection, and a consumer test that imports it.

## Stage 6 — Tooling

1. `cmd/bdump/main.go` (lines 38–92): iterate `o.Values` (new field on
   `OFile`) and print name, tag width, cases with tags, projection table
   symbols, method names.
2. `cmd/bdoc/scan.go` (lines 204–250): extend `tryBosDecl` / `parseBosType`
   to recognize `type Name values ...` declarations and surface a new
   `DeclValuesType` (or extend `DeclType` with a sub-kind) so the HTML
   renderer in `cmd/bdoc/server.go` can format cases and projections.
3. No changes to `cmd/bld/` are expected; the linker already resolves data
   relocations against the projection table symbols emitted by stage 4–5.

## Verification

Per-stage gates (all from repo root):

```sh
make all          # rebuild bld/bas/bosc
make go_test      # unit tests on encoder, parser, checker, flow
make bas_test     # ~70 .bs integration tests
make bosc_test    # ~260+ .bos integration tests
make test         # all of the above
```

Stage-specific checks:

- **Stage 1**: the full `make test` suite passes with no expected-output
  changes. Spot-check `import_qualified_test.bos`, `cross_pkg_struct_test.bos`,
  `static_method_test.bos`, `io_reader_writer_iface_test.bos` for unchanged
  behavior.
- **Stage 2**: `_err_test.bos` files for duplicate case name, duplicate
  projection, wrong initializer arity each match their `.expected` stderr
  (the bosc runner strips positional prefixes; write the expected without
  them).
- **Stage 3**: a positive test where a values type backs an interface and a
  method returns a byte[] via projection. Inspect the generated `.bs` via
  the failure-leftover convention (`${t}.bs`) to confirm no heap calls.
- **Stage 4**: positive tests for i64 and byte[] projections; negative tests
  for missing-projection, integer→values, cross-type equality.
- **Stage 5**: build a producer package (`bos_pkg`) and a consumer
  (`bos_exe`) under `examples/` or as test fixtures; `bdump` on the producer
  `.bo` should list the values metadata; the consumer should compile and
  link.
- **Stage 6**: `bdump producer.bo` shows the new values section;
  `bdoc` serves a page listing the values type with cases and projections.

End-to-end smoke test (after stage 5): port `runtime/io/io.bos`'s ad-hoc
error path (if any) to a values-backed `error_val` interface and ensure
`make bosc_test` still passes.

## Open Decisions for User

The proposal answers most design questions definitively, and the plan
pins the remaining stylistic calls (reserved `tok_values` over contextual;
textual `.bs` directive parsed by bas with binary `ValuesShape` in `.bo`,
matching the existing typealias/struct/interface pattern). One open
question remains worth surfacing before stage 5 lands:

1. Should the producer fixture for cross-package values testing live as a
   throwaway package under `cmd/bosc/tests/` (matching how
   `cross_pkg_struct_test.bos` works today via the runtime `pair` package),
   or be promoted to a real runtime package (e.g. a `errors` package)
   alongside `io` and `string`? The latter is closer to the motivating use
   case but expands the runtime API surface; the former keeps the test
   self-contained.

Defer this until stage 5 begins; the rest of the plan is stable.
