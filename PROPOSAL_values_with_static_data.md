# Proposal: `values` Types With Explicit Projections

## Status

Proposal only. This is not a confirmed language design.

## Motivation

Boson now has enough interface machinery to support small value-backed interface
implementations. If a concrete value fits in the 8-byte interface data word and
its methods use value receivers, it can be passed as an interface without heap
allocation.

That makes allocation-free symbolic error values attractive:

```boson
interface error_val {
	message(e self) byte[]
}
```

Today, a package author still has to define a distinct integer type, constants,
and a `message` method with a manual dispatch chain:

```boson
const SOME_ERROR io_error = io_error(0)
const OTHER_ERROR io_error = io_error(1)

type io_error i64 {
	message(e io_error) byte[] {
		if (e == SOME_ERROR) {
			return "some error"
		} else if (e == OTHER_ERROR) {
			return "other error"
		}
		return "unknown error"
	}
}
```

The goal is to make this common pattern concise while keeping the underlying
model general enough to support non-error uses later.

## Core Idea

Introduce a `values` form for defining a finite symbolic value type with
optional statically defined projections into other types.

The simplest form is a distinct symbolic set:

```boson
type color values {
	RED
	GREEN
	BLUE
}
```

The error-code form declares a projection signature once, then gives each case
one static initializer per projection:

```boson
type io_error values (i64, byte[]) {
	NOT_FOUND: 404, "not found"
	PERMISSION_DENIED: 403, "permission denied"
} {
	message(e io_error) byte[] {
		return byte[](e)
	}
}
```

The cases are symbols, not integers. The compiler may encode cases as dense
integer tags internally, but that tag is private. User-visible conversions use
declared projections only.

## Proposed Syntax

### Tag-Only Values

```boson
type Name values {
	CASE_A
	CASE_B
} {
	// optional methods
}
```

### Values With Static Projections

```boson
type Name values (ProjectionTypeA, ProjectionTypeB) {
	CASE_A: expr_a1, expr_a2
	CASE_B: expr_b1, expr_b2
} {
	// optional methods
}
```

Examples:

```boson
type io_error values (i64, byte[]) {
	NOT_FOUND: 404, "not found"
	PERMISSION_DENIED: 403, "permission denied"
} {
	message(e io_error) byte[] {
		return byte[](e)
	}
}
```

```boson
type token_kind values (byte[]) {
	IDENT: "identifier"
	NUMBER: "number"
	STRING: "string"
}
```

The projection signature is part of the type declaration. Every projected case
must provide exactly one initializer for every projection type, in the declared
order. This avoids repeating `i64(...)` and `byte[](...)` on every case and gives
the compiler a single place to enforce consistency.

Projection casts are explicit:

```boson
const e io_error = some_func()
string.puts(byte[](e))
log_status(i64(e))
```

There is no implicit conversion from `io_error` to `byte[]` or `i64`.

### Case Names

Cases are scoped under the values type:

```boson
return io_error.NOT_FOUND
```

Bare case names are not introduced into the package namespace in v1. This avoids
collisions between two values types that both want names such as `OK`,
`NOT_FOUND`, or `UNKNOWN`. A future import/use shorthand could make selected
cases available without the type prefix, but that should be a separate feature.

`io_error.NOT_FOUND` parses as the same generic `Dot` node the parser already
produces for any `A.B` expression. The parser does not decide what `io_error`
refers to. Case access should be implemented as part of a general selector
resolution model, not as a special two-component or three-component pattern.

## Semantics

For:

```boson
type io_error values (i64, byte[]) {
	NOT_FOUND: 404, "not found"
	PERMISSION_DENIED: 403, "permission denied"
} {
	message(e io_error) byte[] {
		return byte[](e)
	}
}
```

The compiler records:

- a distinct type named `io_error`,
- a private compiler tag for each case,
- scoped case symbols `io_error.NOT_FOUND` and
  `io_error.PERMISSION_DENIED`,
- one static projection table for `i64`,
- one static projection table for `byte[]`,
- the user-provided methods for `io_error`.

At runtime, an `io_error` value is only the private tag. The declared projection
data lives in generated static storage.

```text
io_error.NOT_FOUND       -> private tag 0
io_error.PERMISSION_DENIED -> private tag 1

i64 projection table:
  [404, 403]

byte[] projection table:
  ["not found", "permission denied"]
```

An explicit projection cast indexes the appropriate table with the private tag
and copies out the projected value:

```boson
i64(e)    // copy __io_error_i64_projection[tag(e)]
byte[](e) // copy __io_error_byte_slice_projection[tag(e)]
```

The internal tag is not itself a projection. If a user wants an integer code,
they must declare an integer projection.

```boson
type color values {
	RED
	BLUE
}

i64(color.RED) // error: color has no i64 projection
```

Likewise, integer-to-values casts are not allowed:

```boson
io_error(999) // error
```

That preserves the model that values cases are a closed symbolic set. The
compiler can manufacture tags for declared cases, but user code cannot create
arbitrary tags.

## Lowering Model, Not Macros

It is useful to describe the example as if it lowered to ordinary Boson:

```boson
type io_error <private-i64-tag> {
	message(e io_error) byte[] {
		return byte[](e)
	}
}

// Not real source-level declarations.
const io_error.NOT_FOUND io_error = <private tag 0>
const io_error.PERMISSION_DENIED io_error = <private tag 1>

var __io_error_i64_projection i64[2] = {
	404,
	403,
}

var __io_error_byte_slice_projection byte[][2] = {
	"not found",
	"permission denied",
}
```

That is only an explanatory model. The compiler should not implement this as a
textual macro or a source-to-source rewrite.

`bosc` currently has a direct parser-to-AST-to-codegen pipeline:

1. `parser.go` parses source into parser `Node`s.
2. `Node.ToAST(ctx)` converts parser nodes to AST nodes and registers semantic
   declarations in `Context`.
3. `compile.go` walks AST nodes and emits `.bs`.
4. Declarations also emit metadata directives such as `typealias`, `struct`,
   and `interface` so `.bo` imports can reconstruct package shape.

So this feature should be a first-class declaration that lowers into existing
compiler mechanisms during `ToAST` and code generation. "Desugaring" is only an
informal explanation; "lowering" is the accurate compiler term here.

## Compiler Integration Sketch

### Parser

`parseTypeDecl` already recognizes type aliases and struct declarations with
optional methods. Add a contextual `values` form alongside `struct`:

```boson
type Name values { cases... }
type Name values { cases... } { methods... }
type Name values (ProjectionTypes...) { cases... }
type Name values (ProjectionTypes...) { cases... } { methods... }
```

Using `values` as a contextual keyword after `type Name` is preferable if the
current lexer/parser can support it cleanly. That avoids reserving a new global
keyword until the design is confirmed.

The parser should produce a dedicated node kind rather than pretending this is a
normal type alias. The case list is not a type name, and the optional projection
signature needs validation against each case.

### Selector Resolution

Values cases require a more explicit selector-resolution model than `bosc` has
today. This should be treated as a general compiler cleanup that `values` uses,
not as a one-off special case for `Type.CASE`.

#### Current Parser Shape

The parser already represents selector chains left-associatively. For example:

```boson
A.B.C.D
```

is parsed as:

```text
Dot(
  Dot(
    Dot(Symbol("A"), Symbol("B")),
    Symbol("C"),
  ),
  Symbol("D"),
)
```

That shape is good: it means the compiler can resolve the chain from left to
right by resolving the left child, then resolving the next selector under the
object produced by the previous step. No parser inversion is needed for basic
selector chains.

The parser does still contain some selector-specific lowering today:

- `pkg.fn(args)` is represented as a `Dot(Symbol("pkg"), Funcall("fn"))`, then
  `ToAST` folds it into a `Funcall` with `Pkg: "pkg"`.
- `pkg.Type{...}` is flattened by parser logic into a struct literal whose type
  name is the string `"pkg.Type"`.
- Ordinary non-call selectors remain generic `Dot` AST nodes.

Those special cases are independent of `values`, but they show that selector
meaning is currently split between parser, AST construction, type queries, and
code generation.

#### Current Resolution Shape

Today, there is no single "resolve this selector chain" operation. Resolution is
spread across several paths:

- `Symbol.ASTType` resolves ordinary bindings.
- `Dot.ASTType` first checks whether the left side is an imported package and
  the member is an imported variable, then otherwise treats the selector as a
  struct-field access.
- Function calls use `Funcall` shape, including parser/`ToAST` folding for some
  qualified calls.
- Static methods are looked up through method/function tables, but only after
  the parser has shaped the expression as a call.
- Struct literals have a separate parser path for qualified type names.

This works for the current language because selectors mostly mean package
member, struct field, method call, or qualified struct literal. Values cases add
another selector meaning: a selector under a type namespace can produce a value.
That should not be bolted onto only `Dot.ASTType` or only `compileTop`, because
then type checking and code generation can disagree about what the same selector
means.

#### Proposed Model

Introduce a shared selector resolver that resolves selector chains
left-to-right. The root identifier resolves in the ordinary lexical and package
environment. Each subsequent selector resolves under the object produced by the
previous step.

A sketch of the result type:

```go
type ResolvedKind int

const (
	ResolvedRuntimeValue ResolvedKind = iota
	ResolvedPackage
	ResolvedType
	ResolvedValuesType
	ResolvedValuesCase
	ResolvedFunction
	ResolvedStructField
)

type ResolvedObject struct {
	Kind ResolvedKind
	Name string
	Type ASTType
	Pkg  string

	// Optional details depending on Kind.
	Expr   AST
	Values *ValuesDecl
	Case   *ValuesCase
}
```

The exact Go shape can differ, but the important part is that selector
resolution returns an object, not just an `ASTType`. Some resolved objects are
runtime values, some are namespaces, and some are compile-time symbols that can
later produce runtime values.

At the language level there is a single namespace per package. At the compiler
level, however, declarations are tracked in several maps (`funcs`,
`typeAliases`, `structs`, `interfaceDecls`, `imports`, and a new `valuesDecls`),
and a missing-check bug could produce live collisions whose meaning depends on
the order those maps are consulted. AST construction/import handling should
detect and reject package-level namespace collisions across globals, functions,
types, interfaces, values types, and imports. The root-name search should still
be deterministic and documented. A reasonable order, consulting all
package-level maps as one logical tier, is:

1. local bindings,
2. package-level names (vars, funcs, structs, type aliases, interfaces, values
   types, imported package names — all one tier, dispatched by what the name
   resolves to),
3. builtin/package-preloaded names.

If Boson already relies on a different ordering, the implementation should keep
that ordering and document it.

Selector steps then dispatch on the left object:

- `ResolvedPackage.member` resolves exported variables, functions, structs,
  type aliases, interfaces, and values types from imported package metadata.
- `ResolvedRuntimeValue.member` resolves struct fields and, in call position,
  interface or concrete methods. Case names on a values-typed runtime value are
  explicitly rejected (see Diagnostics): cases are reachable only through the
  values type name, not through an instance.
- `ResolvedType.member` resolves static methods and any future type-level
  members for ordinary named types.
- `ResolvedValuesType.member` resolves declared values cases and static methods
  attached to the values type.
- `ResolvedValuesCase.member` is rejected in v1 unless a future feature gives
  cases their own members.

With that model, these forms are just ordinary selector chains:

```boson
io_error.NOT_FOUND
io.io_error.NOT_FOUND
my_struct.field
pkg.some_global
T.static_method()
```

For `io.io_error.NOT_FOUND`, resolution proceeds as:

```text
io        -> ResolvedPackage("io")
.io_error -> ResolvedValuesType("io.io_error")
.NOT_FOUND -> ResolvedValuesCase(tag=0, type=io.io_error)
```

For `io_error.NOT_FOUND`, resolution proceeds as:

```text
io_error   -> ResolvedValuesType("io_error")
.NOT_FOUND -> ResolvedValuesCase(tag=0, type=io_error)
```

Code generation for `ResolvedValuesCase` emits the private tag bits as a value
of the values type. `ASTType` for the same object is the values type. That shared
resolution result keeps type checking, assignment compatibility, equality, and
code generation aligned.

This resolver should also become the common hook for future selector meanings,
so `values` does not create a second static-member mechanism.

The selector-resolution refactor is a prerequisite for `values`, not optional
cleanup that follows it. Existing parser special cases (`pkg.fn(args)` folded
into `Funcall{Pkg, FName}`, `pkg.Type{...}` flattened into a string-keyed struct
literal) should be migrated through the shared resolver as part of this work,
not deferred. Concretely: `Funcall`'s `Pkg string` field should be replaced by
a `Callee AST` (or equivalent selector chain), so that `pkg.fn(args)` and
`v.method(args)` and `Type.static_method(args)` all reach the call site through
the same resolved-selector path, with the resolver telling the call site
whether the callee is a free function, a method, or a function-pointer value.
The same migration carries qualified struct literals (`pkg.Type{...}`): the
literal stores a resolved named type (or `TypeRef`) produced by resolving the
selector chain, and codegen renders that identity to the package-qualified type
key it needs.

### AST Conversion

`Node.ToAST(ctx)` should own declaration registration and validation, consistent
with how the compiler works today. There should not be a separate validator
phase.

For a values declaration, `ToAST` should:

1. Register the values type in a new `valuesDecls` map on `Context`, distinct
   from `structs`, `typeAliases`, and `interfaceDecls`. Carrying it as its own
   kind matters because `typeIsMemoryBacked` (ast.go) consults these maps to
   decide register- vs. memory-backing, and a values type must read as
   register-resident; cross-package import metadata for values types also needs
   richer content than typealias carries.
2. Record the case list on the `ValuesDecl` so the shared selector resolver can
   resolve values cases later. ToAST does not itself resolve case references in
   expression position; it registers declarations and leaves selector meaning to
   the later resolution/codegen path.
3. Assign each case a compiler-private dense tag.
4. Validate duplicate case names.
5. Validate duplicate projection types in the header.
6. Validate that every projected case has the same number of initializers as the
   projection signature.
7. Record enough type context for each initializer to be encoded against its
   projection type. `ToAST` handles only declaration-shape validation that does
   not require the full type index — arity mistakes and duplicate names. Type
   identity (do the projection type names refer to declared types?) and typed
   initializer validation (is each initializer statically encodable for its
   destination type?) belong to the compile pass, after all top-level
   declarations are registered.
8. Record projection table shapes and static initializer expressions.
9. Register user methods and method metadata for interface satisfaction.

The AST should carry enough information for codegen to emit the static tables
and method functions. A possible shape:

```go
type ValuesDecl struct {
	Name        string
	TagType     ASTType // v1: always i64 internally
	Projections []ASTType
	Cases       []ValuesCase
	Methods     []*FuncDecl
	p           position
}

type ValuesCase struct {
	Name string
	Tag  int64
	Expr []AST
	p    position
}
```

Case expressions in user code are not represented as integer constants. The
shared selector resolver and codegen path materialize case references using an
internal-only construct, for example:

```go
type ValuesTag struct {
	TypeName string
	CaseName string
	Tag      int64
	p        position
}
```

Neither the parser nor `ToAST` constructs `ValuesTag` directly — the AST keeps
the generic `Dot`. At compile time, when selector resolution determines that a
chain refers to a values case, codegen produces a `ValuesTag`-shaped operand (or
directly emits the tag bits as a value of type `io_error`, depending on how
codegen spots are organized).

This is cleaner than exposing `io_error(0)` or emitting each case as a mutable
global. If the existing constant machinery can carry typed constants cleanly,
`ValuesTag` may compile through that path internally, but the surface language
still does not gain integer-to-values construction.

### Case Resolution At Compile Time

Case references reach the compiler as ordinary selector chains. The shared
selector resolver described above determines whether a selector resolves to a
`ResolvedValuesCase`.

On success, code generation emits the case's private tag as a value of the
values type. On failure, the resolver emits one of the case-related diagnostics
listed in the Diagnostics section (unknown root name, bare case name without
type prefix, missing case on a values type, or case access through a runtime
value).

### Code Generation

`compileTop` should add a `case *ValuesDecl`.

That case should emit:

- metadata for the values type,
- generated static projection tables,
- user method functions,
- any required local symbols for table addresses.

Case values do not need runtime storage. A case reference compiles to the
private tag value. Projection casts compile to table lookups.

Methods on a values type receive the values value itself. For v1, that is a
register-resident private `i64` tag, similar to how methods on `type FD i64`
receive the value. Inside a method, `byte[](e)` is the same projection-cast
lowering used at call sites. It is not a special method-only operation.

### Projection Cast Lowering

The existing cast path is the right hook. A cast like `byte[](e)` is already
parsed as a `Funcall` whose name is a type, and `compileCast` handles type-call
casts.

For values types, the cast path should:

1. Compile the source expression and determine its type.
2. If the source type is a values type, check whether the target type appears in
   that values type's projection signature.
3. If a projection exists, copy the projected value from the generated
   projection table at the source tag index.
4. If no projection exists, emit a direct diagnostic.
5. If the target type is the values type and the source is an integer, reject the
   cast unless a future explicit construction feature is added.

No implicit conversions are added. Assignment, argument passing, and return type
checking should continue to require exact compatibility unless the user wrote an
explicit projection cast.

### Static Initializers

Projection initializers go through the same static-init encoder that ordinary
global/static declarations use. Whatever shapes that encoder accepts elsewhere
(integer and string literals, struct and array literals, `&literal` producing
anonymous globals, references to other file-scope constants) are accepted in
projection initializers too. There is no values-specific restriction on
initializer shape.

The header projection signature gives the encoder a known destination type for
each initializer expression, the same as a typed `var foo T = ...` at file
scope. In:

```boson
type io_error values (byte[]) {
	NOT_FOUND: "not found"
}
```

the string literal is checked against a known `byte[]` destination and uses the
same static slice-header path global initializers already use.

One implementation point worth verifying early: the `byte[]` projection table
is an *array* of slice headers (`{ptr, len}` × N). Single static slice headers
work today for top-level `var s byte[] = "x"`. Arrays of slice headers with
per-element relocations to string-bytes literals may or may not be supported
in the current static encoder; if not, that is straightforward extension work
in `globals.go` / `encodeStaticInit`.

### Import Metadata

Cross-package projection use needs explicit metadata. Reusing the existing
`typealias` directive will likely become cramped, because imported code needs
more than an alias plus method names. It needs the case list and projection table
symbols too.

Add a new `.bo` directive for values types. A schematic shape:

```text
values io_error tag=i64 methods=[message] cases=[
	NOT_FOUND:0,
	PERMISSION_DENIED:1
] projections=[
	i64:__projection_pkg_io_error__i64,
	byte[]:__projection_pkg_io_error__byte_slice
]
```

The projection symbol naming follows the existing `__vtable_<type>__<interface>`
convention used in `compile.go` (the vtable encoder replaces dots in qualified
type names with underscores at compile.go:4063–4065 to avoid linker
misparsing; projection names do the same).

The exact encoding should match existing object-file conventions, but the
semantic content should include:

- values type name,
- private tag width/type, v1 always `i64`,
- method names for interface satisfaction,
- exported scoped case names and their private tags,
- per-projection target type and table symbol.

Imported package code can then compile:

```boson
return io.io_error.NOT_FOUND
byte[](err)
```

where `err` has imported type `io.io_error`, by using the imported projection
metadata to find the right table symbol.

This should be considered part of the first implementation. A package-local-only
error-values feature would be much less useful.

### Tools

`bdump` and `bdoc` should be updated so values types are inspectable and
documentable. This is not central to code generation, but values types will be a
new exported declaration form and should not be invisible in package tooling.

## Interface Interaction

A v1 `values` type is represented internally as an `i64` private tag, so it fits
in the 8-byte interface data word.

That means it can implement a value-backed interface without allocation:

```boson
interface error_val {
	message(e self) byte[]
}

type io_error values (i64, byte[]) {
	NOT_FOUND: 404, "not found"
	PERMISSION_DENIED: 403, "permission denied"
} {
	message(e io_error) byte[] {
		return byte[](e)
	}
}

fn open() error_val {
	return io_error.NOT_FOUND
}
```

The returned interface contains:

```text
[ data word: io_error tag ][ vtable pointer ]
```

No heap allocation is required.

## Equality, Matching, And Printing

For v1, values of the same values type should support equality and inequality by
comparing private tags:

```boson
if (e == io_error.NOT_FOUND) {
	...
}
```

Comparing different values types should be rejected, even though both may be
represented as `i64` internally.

Ordering, switch/match syntax, exhaustive checking, and default printing are not
part of this proposal. Users can project explicitly for display:

```boson
string.puts(byte[](e))
```

This keeps the initial feature focused on allocation-free symbolic values and
static projections.

## Diagnostics

The compiler should produce direct diagnostics for common mistakes. These checks
belong in `ToAST` where possible, because declaration validation is already
interleaved with AST construction.

Duplicate value name:

```text
Duplicate value name NOT_FOUND in values type io_error
```

Duplicate projection type in the header:

```text
Values type io_error declares projection byte[] more than once
```

Wrong initializer count:

```text
Value NOT_FOUND has 1 projection initializer but io_error declares 2 projections
```

Projection expression has wrong type:

```text
For value NOT_FOUND, expected projection byte[] but got i64
```

Unscoped case name:

```text
Unknown name NOT_FOUND; values cases must be referenced as io_error.NOT_FOUND
```

Case access through a runtime values value:

```text
Cannot access case NOT_FOUND through values value e; reference as io_error.NOT_FOUND
```

Missing case:

```text
Values type io_error has no case TIMEOUT
```

Missing projection for cast:

```text
Cannot cast io_error to byte[]: io_error has no byte[] projection
```

Integer-to-values cast:

```text
Cannot cast i64 to io_error: values cases must be constructed from declared cases
```

Attempting to pass a values value where a projection is needed:

```text
Cannot use io_error as byte[]; write byte[](value) to use the byte[] projection
```

## Relationship To Algebraic Data Types

This proposal deliberately starts smaller than full algebraic data types.

Future syntax may want payload variants:

```boson
type io_error values {
	NOT_FOUND(path byte[])
	OS_ERROR(errno i64)
}
```

That is closer to a tagged union:

```text
{ tag i64, payload union(...) }
```

Such values may not fit in the 8-byte interface data word and may require
pointer-backed interface use.

The proposed projection-based `values` feature is different: it stores only a
private tag in the runtime value and keeps projection data in static tables.
That makes it a good first step for error codes and symbolic sets.

## Representation Classes

Long term, `values` types can have multiple representation classes:

1. Tag-only values
   - runtime value is a private tag,
   - v1 tag representation is `i64`,
   - value-backed interface compatible.

2. Tag plus static projections
   - runtime value is still just a private tag,
   - projection data lives in compiler-generated static tables,
   - value-backed interface compatible.

3. Payload variants
   - runtime value includes tag plus payload,
   - representation may exceed 8 bytes,
   - interface conversion may require a pointer.

This proposal only covers classes 1 and 2.

The v1 tag representation is `i64`. The tag itself is unreachable from user
code — there is no operation that exposes raw tag bits, and conversion from a
values value to an integer always goes through a declared integer projection.
The tag is therefore opaque in the language-level sense regardless of the
chosen implementation type, and future variants may differ in representation
without changing surface semantics.

## Suggested First Implementation

### Prerequisite: shared selector resolver

Before any of the values-specific work, land the selector-resolution refactor
described under [Selector Resolution](#selector-resolution):

- Introduce the shared resolver that walks a `Dot` chain left-to-right and
  returns a `ResolvedObject`.
- Migrate `Symbol.ASTType`, `Dot.ASTType`, and `compileTop`'s dot handling to
  consult the resolver, so type checking and code generation cannot disagree
  about what a selector means.
- Replace `Funcall`'s `Pkg string` field with a `Callee AST` (or equivalent),
  and migrate the parser folds for `pkg.fn(args)` and `v.method(args)` to
  produce calls whose callee is a resolved selector chain.
- Migrate qualified struct literals (`pkg.Type{...}`) onto the same resolver
  rather than the current parser flattening to `"pkg.Type"` strings.
- Re-run the full test suite to confirm the refactor is behavior-preserving for
  all existing forms before adding any values handling.

### Values feature

Then implement:

```boson
type Name values (ProjectionTypeA, ProjectionTypeB) {
	CASE_A: static_expr_a1, static_expr_a2
	CASE_B: static_expr_b1, static_expr_b2
} {
	methods...
}
```

and:

```boson
type Name values {
	CASE_A
	CASE_B
} {
	methods...
}
```

With these restrictions:

- Runtime representation is a private `i64` tag.
- Case tags are compiler-private, implicit, and dense starting at zero.
- Case names are scoped as `TypeName.CASE`.
- Case references lower to an internal-only `ValuesTag` expression.
- Integer-to-values casts are rejected.
- Projection expressions must be statically initializable.
- Explicit casts to declared projection types are supported.
- User methods are compiled like normal value-receiver methods.
- Equality and inequality are supported only between values of the same values
  type.
- No payload variants.
- No user-visible explicit tag values.
- No dynamic projection data.
- No generated `.data()` method.
- No implicit conversions to projection types.
- Implement as a new parser/AST declaration lowered inside `ToAST` and
  `compileTop`, not as a source-rewriting macro pass.
- Add a dedicated `.bo` `values` metadata directive so imported packages can use
  cases, methods, and projection casts.
- Register values types in a new `valuesDecls` map on `Context`, distinct from
  `structs`, `typeAliases`, and `interfaceDecls`, so `typeIsMemoryBacked` and
  the shared selector resolver have an unambiguous home for them.
- Update `bdump` and `bdoc` for the new declaration form.

This gives ergonomic allocation-free error codes:

```boson
interface error_val {
	message(e self) byte[]
}

type io_error values (i64, byte[]) {
	NOT_FOUND: 404, "not found"
	PERMISSION_DENIED: 403, "permission denied"
} {
	message(e io_error) byte[] {
		return byte[](e)
	}
}

fn open() error_val {
	return io_error.NOT_FOUND
}
```

## Remaining Open Questions

- Should there eventually be shorthand syntax for importing selected case names
  into local scope?
- Should a future version accept per-case typed projection syntax as sugar over
  the header projection signature?
- What is the exact `.bo` text format for the new `values` directive?
- Should future payload variants live under `values`, or should they be a
  separate algebraic-data-type feature?
