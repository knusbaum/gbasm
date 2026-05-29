package main

// Shared selector resolution.
//
// Selector chains like `pkg.fn`, `v.field`, `T.static_method`, and (later)
// `io.io_error.NOT_FOUND` are all left-associative `Dot` chains in the AST.
// Before this resolver existed, each chain meaning was decided in a different
// place: Symbol.ASTType for bare names, Dot.ASTType for member access,
// compileTop's *Dot case for code generation, and parser folds for
// `pkg.fn(args)` and `pkg.Type{...}`. Adding new selector meanings (like
// values cases) without a single dispatch point would let type checking and
// code generation disagree about what the same chain means.
//
// ResolveSelector walks an AST left-to-right and returns a ResolvedObject
// that names what the chain refers to. Callers project from the result
// (an ASTType, a value, a callee descriptor) instead of re-discovering the
// meaning at every site.
//
// Root-name precedence is preserved verbatim from the pre-resolver code:
// local bindings shadow package-level names which shadow builtins. Within
// the package-level tier, the existing per-map lookup order is preserved
// (bindings → imports → funcs/types) — Stage 1 does not add cross-map
// collision rejection.

// ResolvedKind tags what a ResolvedObject represents.
type ResolvedKind int

const (
	ResolvedUnknown ResolvedKind = iota
	// ResolvedRuntimeValue is a value computed at runtime (a local, global,
	// or arbitrary expression result). Type holds its ASTType.
	ResolvedRuntimeValue
	// ResolvedPackage is an imported package name. Members are looked up
	// in the package's exported tables.
	ResolvedPackage
	// ResolvedType is a named type (typealias, struct, or interface) used
	// in non-value position — e.g. as the LHS of `T.static_method` or
	// `T(expr)` in a cast. Type carries the type identity.
	ResolvedType
	// ResolvedFunction is a free function reference. Decl is the FuncDecl.
	// Pkg is set when the function lives in an imported package.
	ResolvedFunction
	// ResolvedStructField is a member access that resolved to a struct
	// field. Field carries the field type; Owner is the resolved object
	// the field was accessed through (for codegen access path).
	ResolvedStructField
	// ResolvedValuesType is a values type used as a namespace for its
	// cases. Stage 2 populates this when ValuesDecl exists.
	ResolvedValuesType
	// ResolvedValuesCase is a specific case in a values type. Stage 2
	// populates this.
	ResolvedValuesCase
)

// ResolvedObject names what a selector chain refers to.
//
// Only fields relevant to Kind are populated. Callers should dispatch on
// Kind before reading the optional fields.
type ResolvedObject struct {
	Kind ResolvedKind

	// Name is the leaf identifier of the chain. For runtime values, this
	// is the binding name (empty when the root was an arbitrary
	// expression). For packages, the package name. For types and
	// functions, the unqualified name. For struct fields, the field name.
	Name string

	// Pkg is the importing package qualifier for types and functions that
	// live in an imported package. Empty for locally-declared things.
	Pkg string

	// Type is the ASTType this chain projects to when used as a value.
	// For ResolvedRuntimeValue: the value's type.
	// For ResolvedType / ResolvedValuesType: the type identity.
	// For ResolvedStructField: the field's type.
	// For ResolvedFunction / ResolvedValuesCase: their value-form type.
	Type ASTType

	// Expr is the AST that produced a runtime value. For RuntimeValue
	// rooted in a bare Symbol, this is the Symbol itself; for arbitrary
	// expressions or nested struct-field access, this is the full AST
	// the resolver walked. Codegen reads Expr to compile the value.
	Expr AST

	// Decl points at the FuncDecl for ResolvedFunction.
	Decl *FuncDecl

	// StructDecl points at the StructDecl for ResolvedType when the type
	// is a struct, or for the owner of a ResolvedStructField.
	StructDecl *StructDecl
}

// ResolveSelector resolves an AST that may be a Symbol, a Dot chain, or
// an arbitrary expression, and returns what it refers to. The resolver
// is total: any non-resolvable form returns ResolvedRuntimeValue with
// its ASTType (which may itself error on lookup).
//
// For Stage 1 the resolver is a thin wrapper that preserves existing
// behavior: each step delegates to the same Context queries the
// pre-resolver code used (TypeForVar, IsImportedPackage, StructDeclForName,
// etc.). Migration of values cases happens in Stage 2.
func ResolveSelector(c *Context, expr AST) ResolvedObject {
	switch e := expr.(type) {
	case *Symbol:
		return resolveRoot(c, e.Name, e)
	case *Dot:
		prev := ResolveSelector(c, e.Val)
		return stepSelector(c, prev, e.Member, e)
	default:
		// Arbitrary expression: project to its ASTType as a runtime value.
		// ASTType may itself panic with an interpreterError if the
		// expression is malformed; that is the existing behavior.
		t := expr.ASTType(c)
		return ResolvedObject{
			Kind: ResolvedRuntimeValue,
			Type: t,
			Expr: expr,
		}
	}
}

// resolveRoot looks up a bare identifier in the context. Precedence
// (preserved from the pre-resolver code):
//
//   1. local bindings (TypeForVar walks the context chain to globals)
//   2. imported package names
//   3. user-defined struct / typealias / interface names
//   4. locally-defined functions
//
// Anything that doesn't match becomes a ResolvedRuntimeValue with an
// undefined type; the caller (Symbol.ASTType, etc.) is responsible for
// emitting the existing "Variable %s undeclared" diagnostic.
func resolveRoot(c *Context, name string, sym *Symbol) ResolvedObject {
	if t, ok := c.TypeForVar(name); ok {
		return ResolvedObject{
			Kind: ResolvedRuntimeValue,
			Name: name,
			Type: t,
			Expr: sym,
		}
	}
	if c.IsImportedPackage(name) {
		return ResolvedObject{
			Kind: ResolvedPackage,
			Name: name,
		}
	}
	if d, ok := c.StructDeclForName(name); ok {
		return ResolvedObject{
			Kind:       ResolvedType,
			Name:       name,
			Type:       ASTType{Name: name},
			StructDecl: d,
		}
	}
	if _, ok := c.TypeByName(name); ok {
		// Built-in or user-declared type alias.
		t, _ := c.TypeByName(name)
		return ResolvedObject{
			Kind: ResolvedType,
			Name: name,
			Type: t,
		}
	}
	if _, ok := c.InterfaceForName(name); ok {
		return ResolvedObject{
			Kind: ResolvedType,
			Name: name,
			Type: ASTType{Name: name},
		}
	}
	if d, ok := c.FuncDeclForName(name); ok {
		return ResolvedObject{
			Kind: ResolvedFunction,
			Name: name,
			Decl: d,
		}
	}
	// Unresolved: leave Kind as ResolvedUnknown. Symbol.ASTType emits the
	// classic "Variable %s undeclared" panic when projecting from this.
	return ResolvedObject{
		Kind: ResolvedUnknown,
		Name: name,
		Expr: sym,
	}
}

// stepSelector advances one selector under a previously resolved object.
// dotExpr is the full Dot node so the caller can reach back to the AST
// (e.g. struct-field access needs the original Dot to compile its base).
func stepSelector(c *Context, prev ResolvedObject, member string, dotExpr AST) ResolvedObject {
	switch prev.Kind {
	case ResolvedPackage:
		// pkg.member — exported variable, function, or type from the
		// imported package. Preserves pre-resolver lookup order: vars
		// first (matches Dot.ASTType today), then functions, then types
		// (Stage 1 has no imported types as selectors-of-package; that
		// is the qualified-struct-literal path handled separately).
		if vt, ok := c.ImportedVarType(prev.Name, member); ok {
			return ResolvedObject{
				Kind: ResolvedRuntimeValue,
				Name: prev.Name + "." + member,
				Pkg:  prev.Name,
				Type: vt,
				Expr: dotExpr,
			}
		}
		if pkgFuncs, ok := c.imports[prev.Name]; ok {
			if d, ok := pkgFuncs[member]; ok {
				return ResolvedObject{
					Kind: ResolvedFunction,
					Name: member,
					Pkg:  prev.Name,
					Decl: d,
				}
			}
		}
		// Imported struct/typealias/interface: register the type
		// identity with the package qualifier. The qualified key is the
		// one StructDeclForName / TypeAliasFor / InterfaceForName
		// already index by.
		qualified := prev.Name + "." + member
		if d, ok := c.StructDeclForName(qualified); ok {
			return ResolvedObject{
				Kind:       ResolvedType,
				Name:       qualified,
				Pkg:        prev.Name,
				Type:       ASTType{Name: qualified},
				StructDecl: d,
			}
		}
		if t, ok := c.TypeByName(qualified); ok {
			return ResolvedObject{
				Kind: ResolvedType,
				Name: qualified,
				Pkg:  prev.Name,
				Type: t,
			}
		}
		if _, ok := c.InterfaceForName(qualified); ok {
			return ResolvedObject{
				Kind: ResolvedType,
				Name: qualified,
				Pkg:  prev.Name,
				Type: ASTType{Name: qualified},
			}
		}
		return ResolvedObject{
			Kind: ResolvedUnknown,
			Name: qualified,
			Pkg:  prev.Name,
		}

	case ResolvedRuntimeValue, ResolvedStructField:
		// v.field — struct-field access. Both kinds carry a typed
		// runtime value; a nested chain like `o.i.a` lands here on
		// every step. Pointer-through-nil is checked here so callers
		// do not have to duplicate the check.
		t := prev.Type
		if t.Indirection != 0 && t.NilMask&1 != 0 {
			CompileErrorF(dotExpr, "Cannot access field %s through nullable pointer type %s", member, t)
		}
		decl, ok := structDeclForType(c, t)
		if !ok {
			return ResolvedObject{
				Kind: ResolvedUnknown,
				Name: member,
				Expr: dotExpr,
			}
		}
		for _, f := range decl.Fields {
			if f.Name == member {
				ft := fieldTypeForBase(t, f.Type)
				if ft.Indirection > 0 && ft.NilMask&1 != 0 {
					if path, ok := FlowPathForExpr(dotExpr); ok && c.NullFact(path) == NullKnownNonNull {
						ft.NilMask &^= 1
					}
				}
				return ResolvedObject{
					Kind:       ResolvedStructField,
					Name:       member,
					Type:       ft,
					Expr:       dotExpr,
					StructDecl: decl,
				}
			}
		}
		// No such field: leave for Dot.ASTType / compileTop to surface
		// the existing diagnostic. Returning Unknown with the original
		// Dot expression lets the legacy error path fire unchanged.
		return ResolvedObject{
			Kind: ResolvedUnknown,
			Name: member,
			Expr: dotExpr,
		}

	case ResolvedType:
		// T.member where T is an ordinary named type. Stage 1: callers
		// use this only for T.static_method via Funcall. The plain Dot
		// path doesn't reach here today, so Unknown is correct.
		return ResolvedObject{
			Kind: ResolvedUnknown,
			Name: prev.Name + "." + member,
		}
	}
	return ResolvedObject{Kind: ResolvedUnknown}
}
