package main

import (
	"fmt"
	"io"
	"reflect"
	"strings"

	"github.com/davecgh/go-spew/spew"
	"github.com/knusbaum/gbasm"
)

type interpreterError struct {
	msg string
	p   position
}

func (e *interpreterError) Error() string {
	return fmt.Sprintf("at %s: %s", e.p, e.msg)
}

// Context holds types and bindings for the current lexical environment.
type Context struct {
	parent *Context

	// the name of the package being compiled. Used to qualify in-package
	// calls so the linker sees unambiguous symbol names. Set at the root
	// context by the driver; child contexts inherit via the parent chain.
	pkgname string

	// maps variable names to their types.
	bindings map[string]ASTType
	// checker owns flow-sensitive semantic state such as moves, null facts,
	// borrowed provenance, and pointer alias state.
	checker *CheckerState
	// names that ToAST registered into bindings at the top level so that
	// function bodies declared earlier in source can resolve forward
	// references. The *VarDecl handler in Compile consumes the marker
	// when it revisits the declaration, skipping the redundant BindVar
	// call (which would otherwise collide with the pre-binding). Empty
	// for nested scopes, where ToAST writes to a throwaway Context.
	prebound map[string]bool
	// addressNames lists names whose storage is memory-backed in a way
	// that's externally visible (file-scope globals — the .data/.bss
	// section). Populated only on the root context, written by
	// emitGlobalVarDecl. NameIsAddress consults this set as well as the
	// type-based memory-backing rule (structs and >8-byte values are
	// allocated as `bytes` on the stack and accessed by address too).
	addressNames map[string]bool

	// anonGlobals is a queue of file-scope-data blocks synthesized
	// during static-init encoding (the `&someLiteral` case). Each
	// entry already has its bytes and relocations computed; the
	// caller of encodeStaticInit drains the queue and emits a `var`
	// block for each entry alongside the named global it's nested
	// inside. Maintained only on the root context.
	anonGlobals []anonGlobal
	// anonGlobalCount is a monotonic counter for `__static_N` names.
	// We can't use len(anonGlobals) because DrainAnonGlobals clears
	// the queue between top-level decls, which would let names from
	// later decls collide with earlier ones.
	anonGlobalCount int
	// tracks which bindings are const (true) vs var (false).
	constBindings map[string]bool
	// maps struct names to their declarations.
	structs map[string]*StructDecl
	// maps function names to their declarations.
	funcs map[string]*FuncDecl
	// maps imported package names to that package's function declarations.
	imports map[string]map[string]*FuncDecl
	// maps imported package names to that package's exported variables
	// (pkg → varname → ASTType). Used to resolve cross-package var reads
	// like `io.STDIN` at type-check and codegen time.
	importedVars map[string]map[string]ASTType
	// maps user-defined type alias names to their underlying types.
	typeAliases map[string]ASTType
	// maps interface names to their declarations.
	interfaceDecls map[string]*InterfaceDecl
	// maps values type names to their declarations.
	valuesDecls map[string]*ValuesDecl
	// maps type alias names to their attached methods.
	typeMethods map[string][]*FuncDecl
	// vtables collects (vtableName → spec) for emission at end of compilation.
	vtables map[string]vtableSpec

	// return, continue, break label stack. Return returns from the current Context
	// by jumping to the label. Continue and break do the usual within loops.
	retlabs   []string
	contlabs  []string
	breaklabs []string
	labeli    int

	// Counter for temporaries
	tempi int

	// Keeps the strings to be written as data items later.
	strngs map[string]string
}

func NewContext() *Context {
	return &Context{
		bindings:       make(map[string]ASTType),
		checker:        NewCheckerState(nil),
		prebound:       make(map[string]bool),
		addressNames:   make(map[string]bool),
		constBindings:  make(map[string]bool),
		structs:        make(map[string]*StructDecl),
		funcs:          make(map[string]*FuncDecl),
		imports:        make(map[string]map[string]*FuncDecl),
		importedVars:   make(map[string]map[string]ASTType),
		typeAliases:    make(map[string]ASTType),
		interfaceDecls: make(map[string]*InterfaceDecl),
		valuesDecls:    make(map[string]*ValuesDecl),
		typeMethods:    make(map[string][]*FuncDecl),
		vtables:        make(map[string]vtableSpec),
		strngs:         make(map[string]string),
	}
}

// typeIsMemoryBacked reports whether a value of type t is allocated as
// a stack chunk (`bytes`) rather than register-resident (`local`).
// Pointers always go in registers regardless of what they point at,
// so Indirection > 0 short-circuits to false. Otherwise: structs (the
// value, not a pointer to one) and anything strictly larger than 8
// bytes is memory-backed. Centralized so the allocation choice and
// the codegen sites that ask "is this name addressing memory" can't
// drift apart.
func typeIsMemoryBacked(c *Context, t ASTType) bool {
	if t.Indirection > 0 {
		return false
	}
	// Function pointers are pointer-sized values — register-resident
	// like any other pointer, regardless of signature.
	if t.FuncSig != nil {
		return false
	}
	// Fixed arrays must live in memory regardless of total size — they
	// need a stable address for `[arr+offset]` indexing to mean
	// anything. Allocating a small array in a sub-register would
	// produce nonsense like `[r11d+0]`, which isn't a valid x86-64
	// addressing form.
	if t.IsArray() {
		return true
	}
	if t.AnonFields != nil {
		return true
	}
	if c != nil {
		if _, ok := c.InterfaceForName(t.Name); ok {
			return true
		}
		// Values types are register-resident in v1 (private i64 tag).
		// Make the choice explicit here so future representations
		// (e.g. payload variants) can change it in one place.
		if _, ok := c.ValuesDeclForName(t.Name); ok {
			return false
		}
	}
	if _, isStruct := c.StructDeclForName(t.Name); isStruct {
		return true
	}
	return t.Size(c) > 8
}

// NameIsAddress reports whether the bas-level name `name` refers to a
// memory location (so the name resolves to an address, and reads need
// a load) rather than a register-resident value (where the name IS
// the value). Two flavors of name-is-address:
//   - File-scope globals, recorded in addressNames at declaration time.
//   - Locals of memory-backed types (structs and >8-byte values),
//     derived from the declared type via typeIsMemoryBacked.
func (c *Context) NameIsAddress(name string) bool {
	if c == nil {
		return false
	}
	// Globals: the addressNames set is maintained on the root context.
	root := c
	for root.parent != nil {
		root = root.parent
	}
	if root.addressNames[name] {
		return true
	}
	// Locals: derive from the declared type's memory-backing.
	if t, ok := c.TypeForVar(name); ok {
		return typeIsMemoryBacked(c, t)
	}
	return false
}

// AddressTaken reports whether `&name` has been compiled anywhere in
// the program (which is also implicitly true for globals, since they
// are marked at declaration time). Unlike NameIsAddress, this does not
// fold in the type's memory-backing — a stack-allocated struct whose
// address is never taken via `&` is not considered address-taken.
func (c *Context) AddressTaken(name string) bool {
	if c == nil {
		return false
	}
	root := c
	for root.parent != nil {
		root = root.parent
	}
	return root.addressNames[name]
}

// MarkAddress records name as memory-backed on the root context.
// Used by emitGlobalVarDecl when emitting a top-level var directive
// so that later codegen sites can recognize the name as address-style
// regardless of its declared type's size or shape.
func (c *Context) MarkAddress(name string) {
	if c.parent != nil {
		c.parent.MarkAddress(name)
		return
	}
	c.addressNames[name] = true
}

// anonGlobal is one queued anonymous global to emit. Created by
// AddAnonGlobal during static-init encoding when an `&literal` form
// needs storage to point at. The caller drains the queue via
// DrainAnonGlobals and emits each as a `var` block.
type anonGlobal struct {
	Name   string
	Type   string // rendered type string for the bas-level var directive
	Bytes  []byte
	Relocs []relocSpec
}

// AddAnonGlobal queues a fresh anonymous global with a unique
// `__static_N` name and returns the chosen name. Forwarded to the
// root context so the counter is unique across the whole compilation
// (mirrors how String() allocates `__bstrN`).
func (c *Context) AddAnonGlobal(typeStr string, bytes []byte, relocs []relocSpec) string {
	if c.parent != nil {
		return c.parent.AddAnonGlobal(typeStr, bytes, relocs)
	}
	name := fmt.Sprintf("__static_%d", c.anonGlobalCount)
	c.anonGlobalCount++
	c.anonGlobals = append(c.anonGlobals, anonGlobal{
		Name:   name,
		Type:   typeStr,
		Bytes:  bytes,
		Relocs: relocs,
	})
	return name
}

// DrainAnonGlobals returns and clears the pending anonymous globals.
// Called after a named global has been emitted; each pending entry
// gets emitted as its own `var` block.
func (c *Context) DrainAnonGlobals() []anonGlobal {
	if c.parent != nil {
		return c.parent.DrainAnonGlobals()
	}
	out := c.anonGlobals
	c.anonGlobals = nil
	return out
}

func (c *Context) SubContext() *Context {
	sc := NewContext()
	sc.parent = c
	sc.checker = NewCheckerState(c.checker)
	return sc
}

// Pkgname returns the name of the package being compiled, walking up the
// parent chain to find the root context that has it set.
func (c *Context) Pkgname() string {
	if c == nil {
		return ""
	}
	if c.pkgname != "" {
		return c.pkgname
	}
	return c.parent.Pkgname()
}

// SetPkgname records the package name on this context. Should only be called
// on the root context.
func (c *Context) SetPkgname(name string) {
	c.pkgname = name
}

// checkPackageNameCollision panics with a directed diagnostic when `name`
// is already declared at the package level under a kind other than
// excludeKind. The excludeKind argument is the kind owned by the caller
// (e.g. "values type") and is skipped so callers can keep their own
// within-kind idempotence handling.
//
// Stage 2 turned this on for the type-shaped decls (struct, type alias,
// interface, values type) so that two `type X ...` forms cannot silently
// coexist as different kinds and confuse the selector resolver. Free
// function names are intentionally not cross-checked here: methods
// register as `TypeName.method` (a dotted key that never collides with a
// bare type name), and bare function names sharing a type's bare name is
// a separate audit.
func (c *Context) checkPackageNameCollision(p position, name, excludeKind string) {
	root := c
	for root.parent != nil {
		root = root.parent
	}
	if excludeKind != "struct" {
		if _, ok := root.structs[name]; ok {
			panic(&interpreterError{msg: fmt.Sprintf("%s %q conflicts with existing struct of the same name", excludeKind, name), p: p})
		}
	}
	if excludeKind != "type alias" {
		if _, ok := root.typeAliases[name]; ok {
			panic(&interpreterError{msg: fmt.Sprintf("%s %q conflicts with existing type alias of the same name", excludeKind, name), p: p})
		}
	}
	if excludeKind != "interface" {
		if _, ok := root.interfaceDecls[name]; ok {
			panic(&interpreterError{msg: fmt.Sprintf("%s %q conflicts with existing interface of the same name", excludeKind, name), p: p})
		}
	}
	if excludeKind != "values type" {
		if _, ok := root.valuesDecls[name]; ok {
			panic(&interpreterError{msg: fmt.Sprintf("%s %q conflicts with existing values type of the same name", excludeKind, name), p: p})
		}
	}
}

func (c *Context) DefineStruct(name string, s *StructDecl) {
	if es, ok := c.structs[name]; ok {
		if es != s {
			panic(&interpreterError{msg: fmt.Sprintf("Struct %q already defined", name), p: s.p})
		}
		return
	}
	c.checkPackageNameCollision(s.p, name, "struct")
	c.structs[name] = s
}

func (c *Context) DefineFunc(name string, f *FuncDecl) {
	if ef, ok := c.funcs[name]; ok {
		if ef != f {
			panic(&interpreterError{msg: fmt.Sprintf("Function %q already defined", name), p: f.p})
		}
	}
	c.funcs[name] = f
}

func (c *Context) BindVar(a AST, name string, t ASTType, isConst bool) {
	if _, ok := c.bindings[name]; ok {
		CompileErrorF(a, "Variable \"%s\" already declared in this scope.", name)
	}
	if _, ok := c.TypeForVar(name); ok {
		CompileErrorF(a, "Variable \"%s\" shadows variable of same name in parent scope.", name)
	}
	c.bindings[name] = t
	c.constBindings[name] = isConst
}

func (c *Context) BindingContext(name string) *Context {
	for ctx := c; ctx != nil; ctx = ctx.parent {
		if _, ok := ctx.bindings[name]; ok {
			return ctx
		}
	}
	return nil
}

func (c *Context) IsGlobalBinding(name string) bool {
	ctx := c.BindingContext(name)
	return ctx != nil && ctx.parent == nil
}

// DefineTypeAlias records a user-defined type alias.
func (c *Context) DefineTypeAlias(p position, name string, underlying ASTType) {
	if _, ok := c.typeAliases[name]; ok {
		panic(&interpreterError{fmt.Sprintf("Type \"%s\" already declared", name), p})
	}
	c.checkPackageNameCollision(p, name, "type alias")
	c.typeAliases[name] = underlying
}

// DefineInterface registers an interface declaration on the root context.
func (c *Context) DefineInterface(p position, name string, decl *InterfaceDecl) {
	root := c
	for root.parent != nil {
		root = root.parent
	}
	if _, ok := root.interfaceDecls[name]; ok {
		panic(&interpreterError{fmt.Sprintf("Interface %q already defined", name), p})
	}
	root.checkPackageNameCollision(p, name, "interface")
	root.interfaceDecls[name] = decl
}

// DefineValuesType registers a values declaration on the root context.
func (c *Context) DefineValuesType(p position, name string, decl *ValuesDecl) {
	root := c
	for root.parent != nil {
		root = root.parent
	}
	if _, ok := root.valuesDecls[name]; ok {
		panic(&interpreterError{fmt.Sprintf("Values type %q already defined", name), p})
	}
	root.checkPackageNameCollision(p, name, "values type")
	root.valuesDecls[name] = decl
}

// ValuesDeclForName looks up a values declaration by name, walking the
// context chain to the root. Both unqualified and "pkg.Name" qualified
// keys are supported, matching the existing struct lookup convention.
func (c *Context) ValuesDeclForName(name string) (*ValuesDecl, bool) {
	if c == nil {
		return nil, false
	}
	if d, ok := c.valuesDecls[name]; ok {
		return d, true
	}
	return c.parent.ValuesDeclForName(name)
}

// InterfaceForName looks up an interface declaration by name, searching the root context.
// If the bare name is not found, it falls back to "builtin.<name>" so that
// builtin-package interfaces (e.g. error) are accessible without a prefix.
func (c *Context) InterfaceForName(name string) (*InterfaceDecl, bool) {
	if c == nil {
		return nil, false
	}
	root := c
	for root.parent != nil {
		root = root.parent
	}
	if d, ok := root.interfaceDecls[name]; ok {
		return d, true
	}
	d, ok := root.interfaceDecls["builtin."+name]
	return d, ok
}

// IsInterfaceType reports whether t is an interface type (non-pointer, non-slice,
// registered as an interface in the context).
func (c *Context) IsInterfaceType(t ASTType) bool {
	if c == nil {
		return false
	}
	if t.Indirection > 0 || t.IsSliceOrArray() || t.FuncSig != nil {
		return false
	}
	_, ok := c.InterfaceForName(t.Name)
	return ok
}

// DefineTypeMethods registers the methods for a type alias on the root context.
func (c *Context) DefineTypeMethods(typeName string, methods []*FuncDecl) {
	root := c
	for root.parent != nil {
		root = root.parent
	}
	root.typeMethods[typeName] = methods
}

// TypeMethodsFor returns the methods registered for a type alias.
func (c *Context) TypeMethodsFor(typeName string) ([]*FuncDecl, bool) {
	if c == nil {
		return nil, false
	}
	root := c
	for root.parent != nil {
		root = root.parent
	}
	m, ok := root.typeMethods[typeName]
	return m, ok
}

// MethodForType finds a specific method by name for a given type.
func (c *Context) MethodForType(typeName, methodName string) (*FuncDecl, bool) {
	methods, ok := c.TypeMethodsFor(typeName)
	if !ok {
		return nil, false
	}
	for _, m := range methods {
		if m.Name == methodName {
			return m, true
		}
	}
	return nil, false
}

// substituteReceiver replaces "self" type name with typeName at all levels of t.
func substituteReceiver(t ASTType, typeName string) ASTType {
	if t.Name == "self" {
		t.Name = typeName
		return t
	}
	if t.Element != nil {
		elem := substituteReceiver(*t.Element, typeName)
		t.Element = &elem
	}
	return t
}

// TypeSatisfiesInterface checks structural satisfaction: does type typeName
// implement all methods of iface, with self→typeName substituted in receiver types?
func (c *Context) TypeSatisfiesInterface(typeName string, iface *InterfaceDecl) bool {
	methods, ok := c.TypeMethodsFor(typeName)
	if !ok {
		return false
	}
	for _, isig := range iface.Methods {
		found := false
		for _, m := range methods {
			if m.Name != isig.Name {
				continue
			}
			if len(m.Args) == 0 || len(isig.Params) == 0 {
				continue
			}
			expectedReceiver := substituteReceiver(isig.Params[0].Type, typeName)
			if !m.Args[0].Type.Same(expectedReceiver) {
				continue
			}
			if len(m.Args) != len(isig.Params) {
				continue
			}
			match := true
			for i := 1; i < len(isig.Params); i++ {
				if !m.Args[i].Type.Same(isig.Params[i].Type) {
					match = false
					break
				}
			}
			if !match {
				continue
			}
			if !m.Return.Same(isig.Return) {
				continue
			}
			found = true
			break
		}
		if !found {
			return false
		}
	}
	return true
}

// NeedVtable registers a vtable to be emitted; no-op if already registered.
func (c *Context) NeedVtable(name, typeName, ifaceName, pkgName string, iface *InterfaceDecl) {
	root := c
	for root.parent != nil {
		root = root.parent
	}
	if _, ok := root.vtables[name]; ok {
		return
	}
	var methods []string
	for _, m := range iface.Methods {
		methods = append(methods, m.Name)
	}
	root.vtables[name] = vtableSpec{
		typeName:  typeName,
		ifaceName: ifaceName,
		pkgName:   pkgName,
		methods:   methods,
	}
}

// WriteVtables emits all registered vtable globals as `var` blocks to of.
func (c *Context) WriteVtables(of io.Writer) {
	root := c
	for root.parent != nil {
		root = root.parent
	}
	for name, spec := range root.vtables {
		n := len(spec.methods)
		zeros := make([]byte, n*8)
		fmt.Fprintf(of, "var %s byte[%d] {\n", name, n*8)
		fmt.Fprintf(of, "\tbytes \"%s\"\n", bytesToBasStringLiteral(zeros))
		for i, m := range spec.methods {
			fmt.Fprintf(of, "\treloc %d %s.%s.%s 0\n", i*8, spec.pkgName, spec.typeName, m)
		}
		fmt.Fprintf(of, "}\n")
	}
}

// TypeAliasFor returns the underlying ASTType for a user-defined alias, searching
// up the parent chain.
func (c *Context) TypeAliasFor(name string) (ASTType, bool) {
	if c == nil {
		return ASTType{}, false
	}
	if t, ok := c.typeAliases[name]; ok {
		return t, true
	}
	return c.parent.TypeAliasFor(name)
}

// TypeByName returns the ASTType for any named type: built-ins, user aliases,
// and structs (as pointer-sized indirect types). Returns false if not found.
// This is used by the cast expression logic.
func (c *Context) TypeByName(name string) (ASTType, bool) {
	switch name {
	case "i8":
		return ASTType{Name: "i8", Signed: true}, true
	case "i16":
		return ASTType{Name: "i16", Signed: true}, true
	case "i32":
		return ASTType{Name: "i32", Signed: true}, true
	case "i64":
		return ASTType{Name: "i64", Signed: true}, true
	case "u8":
		return ASTType{Name: "u8"}, true
	case "u16":
		return ASTType{Name: "u16"}, true
	case "u32":
		return ASTType{Name: "u32"}, true
	case "u64":
		return ASTType{Name: "u64"}, true
	case "byte":
		return ASTType{Name: "byte"}, true
	case "bool":
		return ASTType{Name: "bool"}, true
	}
	if t, ok := c.TypeAliasFor(name); ok {
		return ASTType{Name: name, Signed: t.Signed}, true
	}
	// Values types are named types too: they need to surface here so
	// cast detection in compileTop's *Funcall case (and compileCast)
	// sees `io_error(...)` as a cast against a known type instead of
	// falling through to "No such function". The cast path then
	// applies the closed-symbolic-set rejection.
	if _, ok := c.ValuesDeclForName(name); ok {
		return ASTType{Name: name}, true
	}
	// Struct types: `pair(v.A)` for a values projection of pair must
	// reach the cast path so compileCast can match it against the
	// declared projection list. Casts where neither side is a values
	// type are caught by the struct-shape guard in compileCast.
	if _, ok := c.StructDeclForName(name); ok {
		return ASTType{Name: name}, true
	}
	// Slice-type expressions in cast position: `byte[](e)` arrives here
	// as the name `"byte[]"`. Peel the suffix and recursively resolve
	// the inner type to build a proper slice ASTType. Projection casts
	// in particular need this so the parser's `byte[](e)` cast lands on
	// a known slice type the cast path can match against.
	if strings.HasSuffix(name, "[]") {
		inner := strings.TrimSuffix(name, "[]")
		if it, ok := c.TypeByName(inner); ok {
			return ASTType{Element: &it}, true
		}
	}
	return ASTType{}, false
}

// ResolveUnderlying follows type aliases to their underlying built-in ASTType,
// preserving qualifiers. Used where the concrete representation matters.
func (c *Context) ResolveUnderlying(t ASTType) ASTType {
	if t.Indirection > 0 || t.IsSliceOrArray() {
		return t
	}
	if underlying, ok := c.TypeAliasFor(t.Name); ok {
		result := underlying
		result.MutMask = t.MutMask
		result.OwnedMask = t.OwnedMask
		result.NilMask = t.NilMask
		return result
	}
	return t
}

// AugmentType propagates properties (Signed) from a type alias definition
// while keeping the alias name intact for type-distinctness checks.
func (c *Context) AugmentType(t ASTType) ASTType {
	if t.Indirection > 0 || t.IsSliceOrArray() {
		return t
	}
	if underlying, ok := c.TypeAliasFor(t.Name); ok {
		t.Signed = underlying.Signed
	}
	return t
}

func (c *Context) IsConst(name string) bool {
	if c == nil {
		return false
	}
	if v, ok := c.constBindings[name]; ok {
		return v
	}
	return c.parent.IsConst(name)
}

func (c *Context) FreeLocalVars(of io.Writer) {
	for n := range c.bindings {
		fmt.Fprintf(of, "\tforget %s\n", n)
	}
}

func (c *Context) TypeForVar(name string) (ASTType, bool) {
	if c == nil {
		return ASTType{}, false
	}
	if t, ok := c.bindings[name]; ok {
		if t.Indirection > 0 && t.NilMask&1 != 0 && c.NullFact(VarFlowPath(name)) == NullKnownNonNull {
			t.NilMask &^= 1
		}
		return t, true
	}
	return c.parent.TypeForVar(name)
}

func (c *Context) DeclaredTypeForVar(name string) (ASTType, bool) {
	if c == nil {
		return ASTType{}, false
	}
	if t, ok := c.bindings[name]; ok {
		return t, true
	}
	return c.parent.DeclaredTypeForVar(name)
}

func (c *Context) StructDeclForName(name string) (*StructDecl, bool) {
	if c == nil {
		return nil, false
	}
	if d, ok := c.structs[name]; ok {
		return d, true
	}
	return c.parent.StructDeclForName(name)
}

func (c *Context) FuncDeclForName(name string) (*FuncDecl, bool) {
	if c == nil {
		return nil, false
	}
	if d, ok := c.funcs[name]; ok {
		return d, true
	}
	return c.parent.FuncDeclForName(name)
}

// FuncDeclForCall resolves a function call and returns the resolved package
// name (always equal to the input pkg).
//
// Lookup:
//   - If pkg is set, look in the named imported package.
//   - If pkg is empty, look only in local funcs.
//
// Calls to imported functions must be qualified (e.g. string.puts, not puts).
func (c *Context) FuncDeclForCall(pkg, name string) (*FuncDecl, string, bool) {
	if pkg == "" {
		d, ok := c.FuncDeclForName(name)
		return d, "", ok
	}
	if c == nil {
		return nil, "", false
	}
	if pkgFuncs, ok := c.imports[pkg]; ok {
		d, ok := pkgFuncs[name]
		return d, pkg, ok
	}
	// Static method call: methods are registered as `TypeName.method` via
	// DefineFunc, so `thingy.hello()` resolves to the locally-defined
	// function `thingy.hello`.
	if d, ok := c.funcs[pkg+"."+name]; ok {
		return d, "", true
	}
	return c.parent.FuncDeclForCall(pkg, name)
}

// DefineImportedFunc registers a function from an imported package.
func (c *Context) DefineImportedFunc(pkg, name string, f *FuncDecl) {
	if c.imports[pkg] == nil {
		c.imports[pkg] = make(map[string]*FuncDecl)
	}
	c.imports[pkg][name] = f
}

// DefineImportedVar registers a variable from an imported package along
// with its type as seen from the consumer (i.e., struct/alias names
// already qualified to the producer's package).
func (c *Context) DefineImportedVar(pkg, name string, t ASTType) {
	if c.importedVars[pkg] == nil {
		c.importedVars[pkg] = make(map[string]ASTType)
	}
	c.importedVars[pkg][name] = t
}

// ImportedVarType returns the type of an imported package's variable,
// or false if no such var was imported.
func (c *Context) ImportedVarType(pkg, name string) (ASTType, bool) {
	if c == nil {
		return ASTType{}, false
	}
	if vs, ok := c.importedVars[pkg]; ok {
		if t, ok := vs[name]; ok {
			return t, true
		}
	}
	return c.parent.ImportedVarType(pkg, name)
}

// IsImportedPackage reports whether the given name refers to an imported package
// in this context chain. Used to distinguish qualified calls from struct accesses.
func (c *Context) IsImportedPackage(name string) bool {
	if c == nil {
		return false
	}
	if _, ok := c.imports[name]; ok {
		return true
	}
	return c.parent.IsImportedPackage(name)
}

func (c *Context) Label(tag string) string {
	if c.parent != nil {
		return c.parent.Label(tag)
	}
	c.labeli++
	l := fmt.Sprintf("_LABEL_%s_%d", tag, c.labeli)
	return l
}

func (c *Context) PushContLabel() string {
	if c.parent != nil {
		return c.parent.PushContLabel()
	}
	l := c.Label("cont")
	c.contlabs = append(c.contlabs, l)
	c.checker.PushContinueFrame()
	return l
}

func (c *Context) PopContLabel() {
	if c.parent != nil {
		c.parent.PopContLabel()
		return
	}
	c.contlabs = c.contlabs[:len(c.contlabs)-1]
	c.checker.PopContinueFrame()
}

func (c *Context) Continue(a AST, of io.Writer) {
	if c.parent != nil {
		c.RecordContinue(c.FlowSnapshot())
		c.parent.emitContinue(a, of)
		return
	}
	c.RecordContinue(c.FlowSnapshot())
	c.emitContinue(a, of)
}

func (c *Context) emitContinue(a AST, of io.Writer) {
	if c.parent != nil {
		c.parent.emitContinue(a, of)
		return
	}
	if len(c.contlabs) == 0 {
		CompileErrorF(a, "Cannot continue, No context present.")
	}
	fmt.Fprintf(of, "\tjmp %s\n", c.contlabs[len(c.contlabs)-1])
}

func (c *Context) RecordContinue(snap FlowSnapshot) {
	if c.parent != nil {
		c.parent.RecordContinue(snap)
		return
	}
	c.checker.RecordContinue(snap)
}

func (c *Context) ContinueStates() []FlowSnapshot {
	if c.parent != nil {
		return c.parent.ContinueStates()
	}
	return c.checker.ContinueStates()
}

func (c *Context) PushBreakLabel() string {
	if c.parent != nil {
		return c.parent.PushBreakLabel()
	}
	l := c.Label("break")
	c.breaklabs = append(c.breaklabs, l)
	c.checker.PushBreakFrame()
	return l
}

func (c *Context) PopBreakLabel() {
	if c.parent != nil {
		c.parent.PopBreakLabel()
		return
	}
	c.breaklabs = c.breaklabs[:len(c.breaklabs)-1]
	c.checker.PopBreakFrame()
}

func (c *Context) Break(of io.Writer) {
	if c.parent != nil {
		c.RecordBreak(c.FlowSnapshot())
		c.parent.emitBreak(of)
		return
	}
	c.RecordBreak(c.FlowSnapshot())
	c.emitBreak(of)
}

func (c *Context) emitBreak(of io.Writer) {
	if c.parent != nil {
		c.parent.emitBreak(of)
		return
	}
	fmt.Fprintf(of, "\tjmp %s\n", c.breaklabs[len(c.breaklabs)-1])
}

func (c *Context) RecordBreak(snap FlowSnapshot) {
	if c.parent != nil {
		c.parent.RecordBreak(snap)
		return
	}
	c.checker.RecordBreak(snap)
}

func (c *Context) BreakStates() []FlowSnapshot {
	if c.parent != nil {
		return c.parent.BreakStates()
	}
	return c.checker.BreakStates()
}

// Push a new return label onto the return stack
func (c *Context) PushRetlabel(t ASTType) string {
	if c.parent != nil {
		return c.parent.PushRetlabel(t)
	}
	l := c.Label("return")
	c.retlabs = append(c.retlabs, l)
	c.checker.PushReturnType(t)
	return l
}

// Pop a return label from the return stack.
func (c *Context) PopRetlabel() {
	if c.parent != nil {
		c.parent.PopRetlabel()
		return
	}
	c.retlabs = c.retlabs[:len(c.retlabs)-1]
	c.checker.PopReturnType()
}

func (c *Context) ReturnType() ASTType {
	if c.parent != nil {
		return c.parent.ReturnType()
	}
	return c.checker.ReturnType()
}

func (c *Context) Return(of io.Writer) {
	if c.parent != nil {
		c.parent.Return(of)
		return
	}
	fmt.Fprintf(of, "\tjmp %s\n", c.retlabs[len(c.retlabs)-1])
}

const temp_prefix = "Temp_"

func (c *Context) Temp() string {
	if c.parent != nil {
		return c.parent.Temp()
	}
	c.tempi++
	return fmt.Sprintf("%s%d", temp_prefix, c.tempi)
}

func parseFuncType(ftype string) (FuncDecl, error) {
	//fn(str) num
	r := strings.NewReader(ftype)
	p := NewParser("", r)
	return p.ParseFunctype()
}

// parseTypeString parses a rendered type string (the output of
// ASTType.String() — e.g., "byte[100]", "*mut Foo", "i64") back into
// an ASTType. Used when reloading struct field types from .bo files.
// Recovers from interpreterError panics raised by the recursive
// descent and returns them as errors.
func parseTypeString(s string) (t ASTType, err error) {
	defer func() {
		if r := recover(); r != nil {
			if ie, ok := r.(*interpreterError); ok {
				err = fmt.Errorf("parse %q: %s", s, ie.msg)
				return
			}
			panic(r)
		}
	}()
	p := NewParser("", strings.NewReader(s))
	n := p.parseTypeName()
	return mkTypename(n), nil
}

// Import loads a precompiled .bo file at path and registers its exported
// functions under the given package name. Cross-package calls are resolved
// against c.imports[pkgName].
// Import loads a precompiled .bo file at path and registers its exported
// functions under the .bo's own pkgname. The importKey is the string the
// source code used in its `import "..."` declaration; it's only used here
// for diagnostics — what makes a package callable in source is the pkgname
// embedded in the .bo file.
func (c *Context) Import(importKey, path string) error {
	o, err := gbasm.ReadOFile(path)
	if err != nil {
		return err
	}
	if o.Pkgname == "" {
		return fmt.Errorf("import %q: .bo at %s has no package name", importKey, path)
	}
	// Type names inside an imported package's signatures and struct
	// field types reference structs by their local (bare) name. From
	// the consumer's perspective those need to become qualified
	// "pkg.Type" references, since that's how this package's structs
	// are registered in the consuming context.
	for _, fn := range o.Funcs {
		if fn.Type != "" {
			t, err := parseFuncType(fn.Type)
			if err != nil {
				return err
			}
			for i := range t.Args {
				qualifyImportedTypeFull(&t.Args[i].Type, o.Pkgname, o.Structs, o.TypeAliases, o.Interfaces, o.Values)
			}
			qualifyImportedTypeFull(&t.Return, o.Pkgname, o.Structs, o.TypeAliases, o.Interfaces, o.Values)
			t.Name = fn.Name
			c.DefineImportedFunc(o.Pkgname, fn.Name, &t)
		}
	}
	for _, sh := range o.Structs {
		var fields []Binding
		for _, fs := range sh.Fields {
			ft, err := parseTypeString(fs.Type)
			if err != nil {
				return fmt.Errorf("import %q: struct %s field %s: %v", importKey, sh.Name, fs.Name, err)
			}
			qualifyImportedTypeFull(&ft, o.Pkgname, o.Structs, o.TypeAliases, o.Interfaces, o.Values)
			fields = append(fields, Binding{Name: fs.Name, Type: ft})
		}
		qname := o.Pkgname + "." + sh.Name
		c.DefineStruct(qname, &StructDecl{TName: qname, Fields: fields})
	}
	for _, ta := range o.TypeAliases {
		ut, err := parseTypeString(ta.Underlying)
		if err != nil {
			return fmt.Errorf("import %q: type alias %s underlying: %v", importKey, ta.Name, err)
		}
		qname := o.Pkgname + "." + ta.Name
		c.DefineTypeAlias(position{}, qname, ut)
		// Reconstruct the method FuncDecls from the already-imported function table.
		if len(ta.MethodNames) > 0 {
			pkgFuncs := c.imports[o.Pkgname]
			fds := make([]*FuncDecl, 0, len(ta.MethodNames))
			for _, mn := range ta.MethodNames {
				key := ta.Name + "." + mn
				if fd, ok := pkgFuncs[key]; ok {
					// Clone with bare method name so MethodForType matches on mn.
					clone := *fd
					clone.Name = mn
					fds = append(fds, &clone)
				}
			}
			c.DefineTypeMethods(qname, fds)
		}
	}
	for _, v := range o.Vars {
		if v.VType == "" {
			continue
		}
		vt, err := parseTypeString(v.VType)
		if err != nil {
			return fmt.Errorf("import %q: var %s: %v", importKey, v.Name, err)
		}
		qualifyImportedTypeFull(&vt, o.Pkgname, o.Structs, o.TypeAliases, o.Interfaces, o.Values)
		c.DefineImportedVar(o.Pkgname, v.Name, vt)
	}
	for _, ifc := range o.Interfaces {
		qname := o.Pkgname + "." + ifc.Name
		decl := &InterfaceDecl{Name: qname}
		for _, m := range ifc.Methods {
			sig := InterfaceMethodSig{Name: m.Name}
			for _, p := range m.Params {
				pt, err := parseTypeString(p.Type)
				if err != nil {
					return fmt.Errorf("import %q: interface %s method %s param %s: %v",
						importKey, ifc.Name, m.Name, p.Name, err)
				}
				qualifyImportedTypeFull(&pt, o.Pkgname, o.Structs, o.TypeAliases, o.Interfaces, o.Values)
				sig.Params = append(sig.Params, Binding{Name: p.Name, Type: pt, IsConst: true})
			}
			rt, err := parseTypeString(m.Return)
			if err != nil {
				return fmt.Errorf("import %q: interface %s method %s return: %v",
					importKey, ifc.Name, m.Name, err)
			}
			qualifyImportedTypeFull(&rt, o.Pkgname, o.Structs, o.TypeAliases, o.Interfaces, o.Values)
			sig.Return = rt
			decl.Methods = append(decl.Methods, sig)
		}
		c.DefineInterface(position{}, qname, decl)
	}
	for _, vs := range o.Values {
		qname := o.Pkgname + "." + vs.Name
		tagType, err := parseTypeString(vs.TagType)
		if err != nil {
			return fmt.Errorf("import %q: values %s tag: %v", importKey, vs.Name, err)
		}
		decl := &ValuesDecl{Name: qname, TagType: tagType}
		for _, vc := range vs.Cases {
			decl.Cases = append(decl.Cases, ValuesCase{Name: vc.Name, Tag: vc.Tag})
		}
		for _, pj := range vs.Projections {
			pt, err := parseTypeString(pj.TargetType)
			if err != nil {
				return fmt.Errorf("import %q: values %s projection %s: %v",
					importKey, vs.Name, pj.TargetType, err)
			}
			qualifyImportedTypeFull(&pt, o.Pkgname, o.Structs, o.TypeAliases, o.Interfaces, o.Values)
			decl.Projections = append(decl.Projections, pt)
		}
		c.DefineValuesType(position{}, qname, decl)
		// Reconstruct the method FuncDecls from the already-imported
		// function table, mirroring the typealias path. Method symbols
		// land in the importer's funcs map under their qualified key
		// `pkg.TypeName.method`; we lift them off and clone with bare
		// names so MethodForType matches on the leaf.
		if len(vs.MethodNames) > 0 {
			pkgFuncs := c.imports[o.Pkgname]
			fds := make([]*FuncDecl, 0, len(vs.MethodNames))
			for _, mn := range vs.MethodNames {
				key := vs.Name + "." + mn
				if fd, ok := pkgFuncs[key]; ok {
					clone := *fd
					clone.Name = mn
					fds = append(fds, &clone)
				}
			}
			c.DefineTypeMethods(qname, fds)
		}
	}
	return nil
}

// qualifyImportedTypeFull walks t's element chain and, when the leaf
// name matches a struct, type alias, interface, or values type defined
// in the producer .bo, rewrites it as "pkgname.LeafName". Built-in
// names and already-qualified names (containing a dot) are left alone.
func qualifyImportedTypeFull(t *ASTType, pkgname string, structs map[string]*gbasm.StructShape, aliases map[string]*gbasm.TypeAliasShape, ifaces map[string]*gbasm.InterfaceShape, values map[string]*gbasm.ValuesShape) {
	if t.Element != nil {
		qualifyImportedTypeFull(t.Element, pkgname, structs, aliases, ifaces, values)
		return
	}
	if t.AnonFields != nil {
		for i := range t.AnonFields {
			qualifyImportedTypeFull(&t.AnonFields[i].Type, pkgname, structs, aliases, ifaces, values)
		}
		return
	}
	if t.FuncSig != nil {
		for i := range t.FuncSig.Args {
			qualifyImportedTypeFull(&t.FuncSig.Args[i], pkgname, structs, aliases, ifaces, values)
		}
		qualifyImportedTypeFull(&t.FuncSig.Return, pkgname, structs, aliases, ifaces, values)
		return
	}
	if _, ok := structs[t.Name]; ok {
		t.Name = pkgname + "." + t.Name
		return
	}
	if _, ok := aliases[t.Name]; ok {
		t.Name = pkgname + "." + t.Name
		return
	}
	if _, ok := ifaces[t.Name]; ok {
		t.Name = pkgname + "." + t.Name
		return
	}
	if _, ok := values[t.Name]; ok {
		t.Name = pkgname + "." + t.Name
	}
}

func (c *Context) String(s string) string {
	if c.parent != nil {
		return c.parent.String(s)
	}
	r, ok := c.strngs[s]
	if !ok {
		i := len(c.strngs)
		r = fmt.Sprintf("__bstr%d", i)
		c.strngs[s] = r
	}
	return r
}

func (c *Context) WriteStrings(of io.Writer) {
	for k, s := range c.strngs {
		// Strings are immutable from the source-level point of view;
		// emit them as `data` so they land in o.Data, distinguishing
		// them from writable o.Vars. The linker currently places both
		// in the same .data segment, but the metadata distinction
		// readies us for a future read-only segment split.
		fmt.Fprintf(of, "data %s string \"%s\\0\"\n", s, unparseString(k))
	}
}

// ASTType represents a Boson type as a small recursive tree.
//
// A type is one of four shapes (read off in this order):
//
//  1. Slice:   Element != nil and ArraySize == 0
//  2. Array:   Element != nil and ArraySize >  0
//  3. Pointer: Element == nil and Indirection > 0; Name/Signed describe the leaf pointee
//  4. Scalar:  Element == nil and Indirection == 0; Name/Signed describe the value
//
// For slice/array types, Element is the canonical description of what each
// element is — including its own qualifiers and possible further nesting.
// For those outer types, Name/Indirection/Signed are not meaningful and
// should not be inspected directly; use the predicate and accessor methods
// instead (IsSlice, IsArray, ElementType, BaseType).
//
// ASTType contains a pointer (Element), so DO NOT compare two values with
// '==' or '!=': use the Same method, which compares structurally. Likewise
// ASTType is not suitable as a Go map key; if you need that, hash the
// String() form or intern types.
type ASTType struct {
	Name        string    // scalar/pointee-leaf name (for shapes 3 and 4); "" for anonymous struct
	Indirection int       // pointer depth (for shapes 3 and 4)
	ArraySize   int       // element count (for shape 2); 0 otherwise
	Element     *ASTType  // element type (for shapes 1 and 2); nil otherwise
	Signed      bool      // signed integer marker for scalar/pointee leaf
	MutMask     uint64    // write-through bit per level
	OwnedMask   uint64    // ownership bit per level
	NilMask     uint64    // nullable pointer bit per pointer level
	FuncSig     *FuncSig  // when set: function-pointer type, pointer-sized
	AnonFields  []Binding // non-nil: anonymous struct type (Name == "", no StructDecl lookup)
	MultiReturn bool      // true when AnonFields was synthesized from a multi-value return signature
	p           position  // source position where this type was written; used in error messages
}

// FuncSig is the signature of a function-pointer type — the argument
// types in declaration order and the return type. Used by ASTType when
// FuncSig != nil to encode `fn(t1, t2, ...) ret` types that show up at
// any value position (variable type, parameter, return, struct field).
// The function-pointer value itself is always pointer-sized (8 bytes);
// the signature is metadata for codegen at call sites.
type FuncSig struct {
	Args   []ASTType
	Return ASTType
}

// IsSlice reports whether the type is a slice (T[]).
func (t *ASTType) IsSlice() bool { return t.Element != nil && t.ArraySize == 0 }

// IsArray reports whether the type is a fixed-size array (T[N]).
func (t *ASTType) IsArray() bool { return t.Element != nil && t.ArraySize > 0 }

// IsSliceOrArray reports whether the type is either a slice or an array.
func (t *ASTType) IsSliceOrArray() bool { return t.Element != nil }

// ElementType returns the element type for slice/array types. It panics if
// the type is not a slice or array.
func (t *ASTType) ElementType() ASTType {
	if t.Element == nil {
		panic(fmt.Sprintf("ElementType called on non-slice/array type %s", t))
	}
	return *t.Element
}

// BaseType walks the Element chain down to the leaf (the type that is
// neither a slice nor an array) and returns it. For a non-slice/non-array
// type, returns the type itself.
func (t *ASTType) BaseType() ASTType {
	cur := *t
	for cur.Element != nil {
		cur = *cur.Element
	}
	return cur
}

const PTR_SIZE = 8

// The size in bytes that the type occupies in memory.
// NOTE: THIS IS NOT THE SIZE OF THE REGISTER.
// For instance, arrays and structs are held in registers
// as pointers.
func (t *ASTType) Size(c *Context) int {
	// Function-pointer types are pointer-sized regardless of signature.
	if t.FuncSig != nil {
		return PTR_SIZE
	}
	// Indirection is the outermost wrapper at every level: a pointer to
	// anything (scalar, slice, array, struct) is pointer-sized.
	if t.Indirection > 0 {
		return PTR_SIZE
	}
	// Slice header is fixed-width regardless of element type.
	if t.IsSlice() {
		return 16
	}
	// Array: total bytes = element size × element count.
	if t.IsArray() {
		return t.Element.Size(c) * t.ArraySize
	}
	// Scalar.
	switch t.Name {
	case "<intlit>":
		panic("Size() called on <intlit> type — this is a compiler bug")
	case "i64", "u64":
		return 8
	case "i32", "u32":
		return 4
	case "i16", "u16":
		return 2
	case "i8", "u8", "byte", "bool":
		return 1
	}
	// User-defined type aliases (resolve to underlying).
	if underlying, ok := c.TypeAliasFor(t.Name); ok {
		return underlying.Size(c)
	}
	// Interface type: fat pointer (data ptr + vtable ptr), always 16 bytes.
	if c != nil {
		if _, ok := c.InterfaceForName(t.Name); ok {
			return 16
		}
		// Values type: storage is the private tag type. v1 is always
		// i64, so the size lookup defers to the declared tag type.
		if vd, ok := c.ValuesDeclForName(t.Name); ok {
			return vd.TagType.Size(c)
		}
	}
	// Anonymous struct: sum of field sizes.
	if t.AnonFields != nil {
		total := 0
		for _, f := range t.AnonFields {
			total += f.Type.Size(c)
		}
		return total
	}
	// Named structs.
	d, ok := c.StructDeclForName(t.Name)
	if !ok {
		panic(&interpreterError{msg: fmt.Sprintf("No such type %q", t.Name), p: t.p})
	}
	return d.Size(c)
}

func (t ASTType) SameExact(t2 ASTType) bool {
	// Signed is not included: types with different signedness already have
	// different names (i64 vs u64, i8 vs u8, etc.), so Name alone distinguishes
	// them. Excluding Signed avoids false mismatches when aliases are created
	// without uniform Signed propagation through the AST construction phase.
	if t.Name != t2.Name ||
		t.Indirection != t2.Indirection ||
		t.ArraySize != t2.ArraySize ||
		t.MutMask != t2.MutMask ||
		t.OwnedMask != t2.OwnedMask ||
		t.NilMask != t2.NilMask {
		return false
	}
	// Compare Element recursively when present.
	if (t.Element == nil) != (t2.Element == nil) {
		return false
	}
	if t.Element != nil && !t.Element.Same(*t2.Element) {
		return false
	}
	// Compare function signatures structurally when present.
	if (t.FuncSig == nil) != (t2.FuncSig == nil) {
		return false
	}
	if t.FuncSig != nil {
		if len(t.FuncSig.Args) != len(t2.FuncSig.Args) {
			return false
		}
		for i := range t.FuncSig.Args {
			if !t.FuncSig.Args[i].Same(t2.FuncSig.Args[i]) {
				return false
			}
		}
		if !t.FuncSig.Return.Same(t2.FuncSig.Return) {
			return false
		}
	}
	// Anonymous struct fields: named struct (nil) never equals anonymous struct (non-nil).
	if (t.AnonFields == nil) != (t2.AnonFields == nil) {
		return false
	}
	if len(t.AnonFields) != len(t2.AnonFields) {
		return false
	}
	for i := range t.AnonFields {
		if t.AnonFields[i].Name != t2.AnonFields[i].Name ||
			!t.AnonFields[i].Type.SameExact(t2.AnonFields[i].Type) {
			return false
		}
	}
	return true
}

func (t ASTType) Same(t2 ASTType) bool {
	return t.SameExact(t2)
}

// SameRepr reports whether two types have the same runtime representation.
// It ignores ownership and mutability, which are compile-time qualifiers.
func (t ASTType) SameRepr(t2 ASTType) bool {
	if t.Name != t2.Name ||
		t.Indirection != t2.Indirection ||
		t.ArraySize != t2.ArraySize {
		return false
	}
	if (t.Element == nil) != (t2.Element == nil) {
		return false
	}
	if t.Element != nil && !t.Element.SameRepr(*t2.Element) {
		return false
	}
	if (t.FuncSig == nil) != (t2.FuncSig == nil) {
		return false
	}
	if (t.AnonFields == nil) != (t2.AnonFields == nil) {
		return false
	}
	if len(t.AnonFields) != len(t2.AnonFields) {
		return false
	}
	for i := range t.AnonFields {
		if t.AnonFields[i].Name != t2.AnonFields[i].Name ||
			!t.AnonFields[i].Type.SameRepr(t2.AnonFields[i].Type) {
			return false
		}
	}
	return true
}

// HasOwned reports whether the type carries any ownership obligation.
func (t ASTType) HasOwned() bool {
	if t.OwnedMask != 0 {
		return true
	}
	if t.Element != nil && t.Element.HasOwned() {
		return true
	}
	if t.FuncSig != nil {
		for _, a := range t.FuncSig.Args {
			if a.HasOwned() {
				return true
			}
		}
		return t.FuncSig.Return.HasOwned()
	}
	for _, f := range t.AnonFields {
		if f.Type.HasOwned() {
			return true
		}
	}
	return false
}

func (t ASTType) ZeroInitializable(c *Context) bool {
	if t.Indirection > 0 {
		return t.NilMask&1 != 0
	}
	if t.FuncSig != nil {
		return false
	}
	if t.IsSlice() {
		return true
	}
	if t.IsArray() {
		return t.Element.ZeroInitializable(c)
	}
	if underlying := c.ResolveUnderlying(t); underlying.Name != t.Name ||
		underlying.Indirection != t.Indirection ||
		underlying.ArraySize != t.ArraySize ||
		(underlying.Element == nil) != (t.Element == nil) ||
		(underlying.FuncSig == nil) != (t.FuncSig == nil) {
		return underlying.ZeroInitializable(c)
	}
	if t.AnonFields != nil {
		for _, field := range t.AnonFields {
			if !fieldTypeForBase(t, field.Type).ZeroInitializable(c) {
				return false
			}
		}
		return true
	}
	if def, ok := c.StructDeclForName(t.Name); ok {
		for _, field := range def.Fields {
			if !fieldTypeForBase(t, field.Type).ZeroInitializable(c) {
				return false
			}
		}
	}
	return true
}

// SameOwned reports whether two types carry the same ownership shape.
func (t ASTType) SameOwned(t2 ASTType) bool {
	if t.Name != t2.Name ||
		t.Indirection != t2.Indirection ||
		t.ArraySize != t2.ArraySize ||
		t.OwnedMask != t2.OwnedMask {
		return false
	}
	if (t.Element == nil) != (t2.Element == nil) {
		return false
	}
	if t.Element != nil && !t.Element.SameOwned(*t2.Element) {
		return false
	}
	if (t.FuncSig == nil) != (t2.FuncSig == nil) {
		return false
	}
	if t.FuncSig != nil {
		if len(t.FuncSig.Args) != len(t2.FuncSig.Args) {
			return false
		}
		for i := range t.FuncSig.Args {
			if !t.FuncSig.Args[i].SameOwned(t2.FuncSig.Args[i]) {
				return false
			}
		}
		if !t.FuncSig.Return.SameOwned(t2.FuncSig.Return) {
			return false
		}
	}
	if (t.AnonFields == nil) != (t2.AnonFields == nil) {
		return false
	}
	if len(t.AnonFields) != len(t2.AnonFields) {
		return false
	}
	for i := range t.AnonFields {
		if t.AnonFields[i].Name != t2.AnonFields[i].Name ||
			!t.AnonFields[i].Type.SameOwned(t2.AnonFields[i].Type) {
			return false
		}
	}
	return true
}

// StripOwned returns a copy of the type with all owned bits cleared.
// Used when passing an owned value to a non-owned parameter (plain borrow).
func (t ASTType) StripOwned() ASTType {
	t.OwnedMask = 0
	if t.Element != nil {
		elem := t.Element.StripOwned()
		t.Element = &elem
	}
	if t.FuncSig != nil {
		sig := *t.FuncSig
		sig.Args = append([]ASTType(nil), t.FuncSig.Args...)
		for i := range sig.Args {
			sig.Args[i] = sig.Args[i].StripOwned()
		}
		sig.Return = sig.Return.StripOwned()
		t.FuncSig = &sig
	}
	if t.AnonFields != nil {
		newFields := make([]Binding, len(t.AnonFields))
		for i, f := range t.AnonFields {
			newFields[i] = Binding{Name: f.Name, Type: f.Type.StripOwned()}
		}
		t.AnonFields = newFields
	}
	return t
}

func (dst ASTType) NilCompatible(src ASTType) bool {
	if dst.Name != src.Name ||
		dst.Indirection != src.Indirection ||
		dst.ArraySize != src.ArraySize {
		return false
	}
	if (dst.Element == nil) != (src.Element == nil) {
		return false
	}
	if dst.Element != nil && !dst.Element.NilCompatible(*src.Element) {
		return false
	}
	if src.NilMask&^dst.NilMask != 0 {
		return false
	}
	if (dst.AnonFields == nil) != (src.AnonFields == nil) {
		return false
	}
	if len(dst.AnonFields) != len(src.AnonFields) {
		return false
	}
	for i := range dst.AnonFields {
		if dst.AnonFields[i].Name != src.AnonFields[i].Name ||
			!dst.AnonFields[i].Type.NilCompatible(src.AnonFields[i].Type) {
			return false
		}
	}
	return true
}

// MutCompatible reports whether src can be used where dst is expected,
// considering only write-through coercion. A more-permissive reference
// (*mut T) is acceptable where a less-permissive one (*T) is expected; the
// reverse is not allowed. Owned bits are ignored by the callers that strip them.
func (dst ASTType) MutCompatible(src ASTType) bool {
	if dst.Name != src.Name ||
		dst.Indirection != src.Indirection ||
		dst.ArraySize != src.ArraySize {
		return false
	}
	if (dst.Element == nil) != (src.Element == nil) {
		return false
	}
	if dst.Element != nil && !dst.Element.MutCompatible(*src.Element) {
		return false
	}
	if (dst.AnonFields == nil) != (src.AnonFields == nil) {
		return false
	}
	if len(dst.AnonFields) != len(src.AnonFields) {
		return false
	}
	for i := range dst.AnonFields {
		if dst.AnonFields[i].Name != src.AnonFields[i].Name ||
			!dst.AnonFields[i].Type.MutCompatible(src.AnonFields[i].Type) {
			return false
		}
	}
	return dst.MutMask&src.MutMask == dst.MutMask
}

// OwnedCompatible is the old name for assignment-style compatibility.
func (dst ASTType) OwnedCompatible(src ASTType) bool {
	return dst.Accepts(src)
}

// Accepts reports whether a value of type src can be used where dst is
// expected. It includes the language's implicit coercions: integer literals
// into concrete integer destinations, nil into pointer/function-pointer
// destinations, borrowing owned values into unowned destinations, and dropping
// write-through mutability.
func (dst ASTType) Accepts(src ASTType) bool {
	if src.Same(intlitASTType()) {
		return true
	}
	if src.Name == "<nil>" {
		return dst.Indirection > 0 && dst.NilMask&1 != 0
	}
	if !dst.HasOwned() {
		src = src.StripOwned()
		return dst.MutCompatible(src) && dst.NilCompatible(src)
	}
	return dst.SameOwned(src) && dst.MutCompatible(src) && dst.NilCompatible(src)
}

// coerceReason classifies the outcome of coerceType so callers can produce
// directed diagnostics without re-deriving WHY the coercion failed.
type coerceReason int

const (
	coerceOK coerceReason = iota
	// coerceValuesFromIntLit: <intlit> cannot construct a values value;
	// proposal §215–222's closed-symbolic-set rule.
	coerceValuesFromIntLit
	// coerceTypeMismatch: ownership/mut/nil/shape disagreement.
	coerceTypeMismatch
)

// coerceType is the single place every value-coercion site (var init,
// assignment, return, call argument, struct-literal field, array-literal
// element, pointer-deref store) consults to ask "can src flow into dst,
// and if so what type does the value take on?"
//
// The two halves of the answer let callers stay uniform:
//
//   - reason discriminates OK, the closed-symbolic-set rejection, and the
//     generic Accepts-style mismatch, so each site can pick its own
//     wording for the generic case while everyone shares the values
//     message.
//
//   - effective is the type the value carries after the language's
//     implicit coercions:
//
//     <intlit> → dst (when dst is a normal integer-shaped type)
//     <nil>    → dst (when dst is a nullable pointer)
//     anything else → src
//
// Callers that previously rewrote argt=param after an intlit/nil check
// should use the returned effective. Callers that key downstream codegen
// off the original src.Same(intlitASTType()) shape can ignore effective
// and use reason as a pass/fail.
//
// Before coerceType existed, each site rolled its own ladder of
// intlit-rewrite + acceptsCtx + nil-rewrite + Accepts, and the
// fragmentation was the root cause of the values-construction bypasses
// at struct literals and array elements: the intlit rewrite happened
// before the values check could see it. Centralizing the rule here
// closes the family of bypasses by construction.
func coerceType(c *Context, dst, src ASTType) (effective ASTType, reason coerceReason) {
	if src.Same(intlitASTType()) {
		if c != nil {
			if _, ok := c.ValuesDeclForName(dst.Name); ok {
				return ASTType{}, coerceValuesFromIntLit
			}
		}
		return dst, coerceOK
	}
	if src.Name == "<nil>" {
		if dst.Indirection > 0 && dst.NilMask&1 != 0 {
			return dst, coerceOK
		}
		return ASTType{}, coerceTypeMismatch
	}
	if dst.Accepts(src) {
		return src, coerceOK
	}
	return ASTType{}, coerceTypeMismatch
}

func (t ASTType) String() string {
	// Outer pointer qualifiers (if any) wrap the rest of the type.
	var sb strings.Builder
	if t.OwnedMask&1 != 0 {
		sb.WriteString("owned ")
	}
	for i := 0; i < t.Indirection; i++ {
		sb.WriteRune('*')
		if t.NilMask&(1<<uint(i)) != 0 {
			sb.WriteRune('?')
		}
		if t.MutMask&(1<<uint(i+1)) != 0 {
			sb.WriteString("mut ")
		}
		if t.OwnedMask&(1<<uint(i+1)) != 0 {
			sb.WriteString("owned ")
		}
	}
	if t.IsSliceOrArray() {
		s := t.Element.String()
		var inner string
		if t.IsArray() {
			inner = fmt.Sprintf("%s[%d]", s, t.ArraySize)
		} else {
			prefix := ""
			if t.OwnedMask&(1<<1) != 0 {
				prefix = "owned "
			}
			if t.MutMask&(1<<1) != 0 {
				prefix += "mut "
			}
			inner = prefix + s + "[]"
		}
		// Parenthesize when there is outer pointer indirection so "*byte[]"
		// (slice of *byte) is distinguishable from "*(byte[])" (pointer to
		// a byte slice).
		if t.Indirection > 0 {
			sb.WriteString("(")
			sb.WriteString(inner)
			sb.WriteString(")")
		} else {
			sb.WriteString(inner)
		}
	} else if t.FuncSig != nil {
		// Render compactly (no spaces between tokens) so the rendered
		// form survives bas's whitespace-tokenized `var name type ...`
		// directive. Arguments whose own type strings contain spaces
		// (e.g. `*mut Foo`) aren't yet supported in function-pointer
		// positions for that reason — would need a quoted-type form
		// in bas to lift the restriction.
		sb.WriteString("fn(")
		for i, a := range t.FuncSig.Args {
			if i > 0 {
				sb.WriteString(",")
			}
			sb.WriteString(a.String())
		}
		sb.WriteString(")")
		if !t.FuncSig.Return.Same(voidASTType()) {
			sb.WriteString(t.FuncSig.Return.String())
		}
	} else if t.AnonFields != nil {
		if t.MultiReturn {
			sb.WriteString("multiretu{")
		} else {
			sb.WriteString("struct{")
		}
		for i, f := range t.AnonFields {
			if i > 0 {
				sb.WriteString(",")
			}
			sb.WriteString(f.Name)
			sb.WriteString(":")
			sb.WriteString(f.Type.String())
		}
		sb.WriteString("}")
	} else {
		sb.WriteString(t.Name)
	}
	return sb.String()
}

func mkTypename(n *Node) ASTType {
	if n.t != n_typename {
		ParseErrorF(n, "Expected type name but found %v", n.t)
	}
	// Anonymous struct type sentinel: parseTypeName packs `struct { f T, ... }`
	// into an n_typename with sval="<struct>" and args = n_stfield nodes.
	if n.sval == "<struct>" {
		var fields []Binding
		for _, fn := range n.args {
			ft := mkTypename(fn.args[0])
			fields = append(fields, Binding{Name: fn.sval, Type: ft})
		}
		return ASTType{AnonFields: fields}
	}
	// Multi-value return type sentinel: parseFn builds `<multiretu>` with positional
	// fields _0, _1, ... Same wire representation as an anonymous struct but
	// MultiReturn=true so the checker can enforce destructuring at call sites.
	if n.sval == "<multiretu>" {
		var fields []Binding
		for _, fn := range n.args {
			ft := mkTypename(fn.args[0])
			fields = append(fields, Binding{Name: fn.sval, Type: ft})
		}
		return ASTType{AnonFields: fields, MultiReturn: true}
	}
	// Function-pointer type sentinel: parseTypeName packs `fn(...) ret`
	// into an n_typename with sval="fn", ival=arg-count, and args=
	// [argType1, ..., argTypeN, returnType]. Recognize and assemble a
	// FuncSig-bearing ASTType. No slice/array wrappers may follow (we
	// don't currently support `fn(...)[]`); the caller's grammar makes
	// that unreachable since parseTypeName returns immediately after
	// reading the fn form.
	if n.sval == "fn" {
		nargs := int(n.ival)
		var sig FuncSig
		for i := 0; i < nargs; i++ {
			sig.Args = append(sig.Args, mkTypename(n.args[i]))
		}
		sig.Return = mkTypename(n.args[nargs])
		return ASTType{
			FuncSig:   &sig,
			MutMask:   n.mutmask,
			OwnedMask: n.ownedmask,
			NilMask:   n.nilmask,
		}
	}
	// Start with the bare base type (scalar or pointer). n.args lists
	// slice/array wrappers in innermost-first order; each wrapper produces
	// a new outer ASTType whose Element is the previous level.
	//
	// MutMask and OwnedMask belong on the outermost wrapper (the slice/array
	// type itself), not on the scalar element — e.g. "mut byte[]" means the
	// slice is writable through, and "owned byte[]" means the slice is owned.
	baseMutMask := n.mutmask
	baseOwnedMask := n.ownedmask
	base := ASTType{
		Name:        n.sval,
		Indirection: int(n.ival),
		MutMask:     0,
		OwnedMask:   0,
		NilMask:     n.nilmask,
		p:           n.p,
	}
	switch base.Name {
	case "i8", "i16", "i32", "i64":
		base.Signed = true
	}

	if len(n.args) == 0 {
		// No wrappers: pointer or scalar type. Masks live on the base itself.
		base.MutMask = baseMutMask
		base.OwnedMask = baseOwnedMask
		return base
	}

	// Wrap from innermost to outermost. Each iteration takes the current
	// type and makes it the Element of a new outer slice/array.
	cur := base
	for _, wrap := range n.args {
		inner := cur
		outer := ASTType{Element: &inner}
		switch wrap.t {
		case n_index:
			outer.ArraySize = int(wrap.ival)
		case n_slice:
			// ArraySize stays 0 — IsSlice() returns true.
		default:
			ParseErrorF(wrap, "Expected an array specifier, but found %v", wrap.t)
		}
		cur = outer
	}
	// MutMask and OwnedMask belong on the outermost slice/array wrapper.
	cur.MutMask = baseMutMask
	cur.OwnedMask = baseOwnedMask
	return cur
}

func voidASTType() ASTType {
	return ASTType{Name: "void"}
}

func numASTType() ASTType {
	return ASTType{Name: "i64", Signed: true}
}

func boolASTType() ASTType {
	return ASTType{Name: "bool"}
}

func byteASTType() ASTType {
	return ASTType{Name: "byte"}
}

func byteSliceASTType() ASTType {
	elem := ASTType{Name: "byte"}
	return ASTType{Element: &elem}
}

func intlitASTType() ASTType {
	return ASTType{Name: "<intlit>"}
}

type AST interface {
	// returns the type the expression gives.
	ASTType(*Context) ASTType
	Note() string
	Pos() position
}

// A binding represents a name which is bound to a value of type Type
// in a specific context, such as struct members or function arguments.
type Binding struct {
	Name    string
	Type    ASTType
	IsConst bool // false = var (rebindable); always false for struct fields
}

type StructDecl struct {
	TName  string
	Fields []Binding
	p      position
}

func (*StructDecl) ASTType(*Context) ASTType {
	return voidASTType()
}

func (s *StructDecl) Note() string {
	return fmt.Sprintf("struct %s {...}", s.TName)
}

func (s *StructDecl) Pos() position {
	return s.p
}

// Returns the size in bytes that the struct occupies.
func (s *StructDecl) Size(c *Context) int {
	size := 0
	for _, f := range s.Fields {
		size += f.Type.Size(c)
	}
	return size
}

func (s *StructDecl) ByteOffset(c *Context, field string) (int, ASTType) {
	offset := 0
	var mtype ASTType
	for _, f := range s.Fields {
		if f.Name == field {
			mtype = f.Type
			break
		}
		offset += f.Type.Size(c)
	}
	return offset, mtype
}

// structDeclForType returns a StructDecl for either a named struct
// (looked up by name in c) or an anonymous struct type (constructed
// from AnonFields inline). Returns false only when t is not a struct type.
func structDeclForType(c *Context, t ASTType) (*StructDecl, bool) {
	if t.AnonFields != nil {
		return &StructDecl{Fields: t.AnonFields}, true
	}
	return c.StructDeclForName(t.Name)
}

func parentOwnsFields(t ASTType) bool {
	return t.OwnedMask&(1<<uint(t.Indirection)) != 0
}

func fieldTypeForBase(base ASTType, field ASTType) ASTType {
	if parentOwnsFields(base) {
		return field
	}
	return field.StripOwned()
}

type VarDecl struct {
	Name    string
	Type    ASTType
	IsConst bool
	Init    AST // optional: nil if no initializer
	p       position
}

func (*VarDecl) ASTType(*Context) ASTType {
	return voidASTType()
}

func (v *VarDecl) Note() string {
	return fmt.Sprintf("var %s %s", v.Name, v.Type)
}

func (v *VarDecl) Pos() position {
	return v.p
}

// MultiBindDecl is the AST node for a multi-value destructuring declaration:
//   var a T1, const b T2 = expr
// Bindings holds each individual binding (Type/IsConst set; Init is nil).
// Init is the shared initializer, which must have a MultiReturn return type.
type MultiBindDecl struct {
	Bindings []VarDecl
	Init     AST
	p        position
}

func (*MultiBindDecl) ASTType(*Context) ASTType { return voidASTType() }
func (m *MultiBindDecl) Note() string           { return "multibind" }
func (m *MultiBindDecl) Pos() position          { return m.p }

type FuncDecl struct {
	Name   string
	Args   []Binding
	Return ASTType
	Body   *Block
	p      position
}

func (*FuncDecl) ASTType(*Context) ASTType {
	return voidASTType()
}

func (f *FuncDecl) Note() string {
	return fmt.Sprintf("fn %s (...) %s {...}", f.Name, f.Return)
}

func (f *FuncDecl) Pos() position {
	return f.p
}

type Block struct {
	Body []AST
	p    position
}

func (*Block) ASTType(*Context) ASTType {
	// TODO: Would be nice for blocks to be expressions with a type,
	// but this requires complex checking for returns, etc.
	// We should do this, but not in the first pass.
	return voidASTType()
}

func (*Block) Note() string {
	return fmt.Sprintf("block {...}")
}

func (b *Block) Pos() position {
	return b.p
}

// Funcall represents a call expression. Callee names the function being
// called and is one of:
//   - *Symbol            — bare call: `fn(args)` (built-in or local function)
//   - *Dot{Symbol, name} — qualified call: `pkg.fn(args)`,
//                          method on a named variable `v.method(args)`,
//                          or static method `Type.method(args)`.
//                          The call site asks the resolver which meaning applies.
//   - *Dot{<expr>, name} — method on an arbitrary expression: `(expr).method(args)`.
//
// Helper methods FName, PkgName, ReceiverExpr, PkgAndName, and QualifiedName
// destructure the Callee for readers that previously consulted separate Pkg
// and FName fields.
type Funcall struct {
	Callee AST
	Args   []AST
	p      position
}

// FName returns the leaf function or method name.
func (f *Funcall) FName() string {
	switch c := f.Callee.(type) {
	case *Symbol:
		return c.Name
	case *Dot:
		return c.Member
	}
	return ""
}

// PkgName returns the qualifier when the callee is `Symbol.member` shape.
// In today's code that string is overloaded to mean an imported package
// name, a receiver variable name, or a (static-method-bearing) type name;
// the call-site dispatch in ASTType / compileTop chooses among them.
// Returns empty for bare calls and for receiver-expression calls (use
// ReceiverExpr for the latter).
func (f *Funcall) PkgName() string {
	if d, ok := f.Callee.(*Dot); ok {
		if s, ok := d.Val.(*Symbol); ok {
			return s.Name
		}
	}
	return ""
}

// PkgAndName returns (PkgName, FName) in one call to keep call sites tidy.
func (f *Funcall) PkgAndName() (string, string) {
	return f.PkgName(), f.FName()
}

// ReceiverExpr returns the receiver expression for `(expr).method(args)`
// calls where the receiver is an arbitrary expression rather than a bare
// symbol. Returns nil for bare calls and for `pkg.fn(args)` / `v.method(args)`
// / `Type.method(args)` forms (those go through PkgName).
func (f *Funcall) ReceiverExpr() AST {
	if d, ok := f.Callee.(*Dot); ok {
		if _, ok := d.Val.(*Symbol); !ok {
			return d.Val
		}
	}
	return nil
}

// QualifiedName returns "pkg.fname" / "(...).fname" / "fname" for diagnostics.
func (f *Funcall) QualifiedName() string {
	if pkg := f.PkgName(); pkg != "" {
		return pkg + "." + f.FName()
	}
	if f.ReceiverExpr() != nil {
		return "(...)." + f.FName()
	}
	return f.FName()
}

func typeExprFromAST(c *Context, a AST) (ASTType, bool) {
	switch v := a.(type) {
	case *Symbol:
		if t, ok := c.TypeByName(v.Name); ok {
			return t, true
		}
		if _, ok := c.StructDeclForName(v.Name); ok {
			return ASTType{Name: v.Name}, true
		}
	case *Dot:
		if pkg, ok := v.Val.(*Symbol); ok {
			name := pkg.Name + "." + v.Member
			if t, ok := c.TypeByName(name); ok {
				return t, true
			}
			if _, ok := c.StructDeclForName(name); ok {
				return ASTType{Name: name}, true
			}
		}
	}
	return ASTType{}, false
}

func (f *Funcall) ASTType(c *Context) ASTType {
	pkg, name := f.PkgAndName()
	if pkg == "" && name == "alloc" {
		if len(f.Args) != 1 {
			CompileErrorF(f, "alloc(T) requires exactly one type argument")
		}
		t, ok := typeExprFromAST(c, f.Args[0])
		if !ok {
			CompileErrorF(f.Args[0], "alloc argument must be a type")
		}
		t.Indirection++
		t.MutMask <<= 1
		t.OwnedMask <<= 1
		t.NilMask <<= 1
		t.OwnedMask |= 1
		t.MutMask |= 1 << 1
		return t
	}
	if pkg == "" && name == "new" {
		if len(f.Args) != 1 {
			CompileErrorF(f, "new(expr) requires exactly one argument")
		}
		if _, ok := typeExprFromAST(c, f.Args[0]); ok {
			CompileErrorF(f.Args[0], "new(T) is not implemented yet; use alloc(T) for zero-initializable types or new(expr)")
		}
		t := f.Args[0].ASTType(c)
		t.Indirection++
		t.MutMask <<= 1
		t.OwnedMask <<= 1
		t.NilMask <<= 1
		t.OwnedMask |= 1
		t.MutMask |= 1 << 1
		return t
	}
	if pkg == "" && name == "free" {
		return voidASTType()
	}
	if pkg == "" && name == "len" {
		if len(f.Args) != 1 {
			CompileErrorF(f, "len() requires exactly one argument")
		}
		return numASTType()
	}
	// Cast expression: type name used as a function. Works for both
	// unqualified (FD(x)) and qualified (io.FD(x)) forms. When a
	// function of the same name exists, the call form wins so that
	// `pair(41)` for a `type pair struct ...` plus `fn pair ...`
	// collision resolves as the call's return type instead of the
	// struct cast's type. See compileTop's parallel guard.
	if t, ok := c.TypeByName(f.QualifiedName()); ok {
		if _, _, hasFn := c.FuncDeclForCall(pkg, name); !hasFn {
			return t
		}
	}
	// (expr).method(args) — receiver is an expression, not a named variable.
	if recv := f.ReceiverExpr(); recv != nil {
		rt := recv.ASTType(c)
		if method, mok := c.MethodForType(rt.Name, name); mok {
			return method.Return
		}
		panic(&interpreterError{
			msg: fmt.Sprintf("no method %q on type %s", name, rt.Name),
			p:   f.p,
		})
	}
	if decl, _, ok := c.FuncDeclForCall(pkg, name); ok {
		return decl.Return
	}
	// Fall back to an indirect call through a function-pointer
	// value:
	//   foo(...)   — bare name resolves to a fn-typed local/global
	//   d.f(...)   — when no package 'd' exists, treat 'd' as a
	//                struct-valued variable and look up field 'f'
	if pkg == "" {
		if vt, ok := c.TypeForVar(name); ok && vt.FuncSig != nil {
			return vt.FuncSig.Return
		}
	} else {
		if vt, ok := c.TypeForVar(pkg); ok {
			// Interface method dispatch: return the method's declared return type.
			if c.IsInterfaceType(vt) {
				if ifaceDecl, iok := c.InterfaceForName(vt.Name); iok {
					for _, isig := range ifaceDecl.Methods {
						if isig.Name == name {
							return isig.Return
						}
					}
				}
			}
			// Concrete type method call: look up by qualified name.
			if method, mok := c.MethodForType(vt.Name, name); mok {
				return method.Return
			}
			if sdecl, sok := c.StructDeclForName(vt.Name); sok {
				_, mtype := sdecl.ByteOffset(c, name)
				if mtype.FuncSig != nil {
					return mtype.FuncSig.Return
				}
			}
		}
	}
	panic(&interpreterError{
		msg: fmt.Sprintf("No such function \"%s\"", f.QualifiedName()),
		p:   f.p,
	})
}

func (f *Funcall) Note() string {
	return fmt.Sprintf("call %s(#%d)", f.QualifiedName(), len(f.Args))
}

func (f *Funcall) Pos() position {
	return f.p
}

type Dot struct {
	Val    AST
	Member string
}

func (d *Dot) ASTType(c *Context) ASTType {
	r := ResolveSelector(c, d)
	switch r.Kind {
	case ResolvedRuntimeValue, ResolvedStructField, ResolvedValuesCase:
		return r.Type
	case ResolvedUnknown:
		// Reproduce the pre-resolver diagnostics: distinguish the
		// "package has no such var" case (left was a known imported
		// package) from the "no such struct" / "no such member" cases.
		if sym, ok := d.Val.(*Symbol); ok && c.IsImportedPackage(sym.Name) {
			CompileErrorF(d, "package %q has no variable %q", sym.Name, d.Member)
		}
		t := d.Val.ASTType(c)
		if decl, ok := structDeclForType(c, t); ok {
			CompileErrorF(d, "No such struct member \"%s\" in struct \"%s\"", d.Member, decl.TName)
		}
		CompileErrorF(d, "No such struct \"%s\"", t)
	}
	CompileErrorF(d, "%s.%s does not name a value", d.Val.Note(), d.Member)
	return voidASTType()
}

func (d *Dot) Note() string {
	return fmt.Sprintf("Dot (%s).%s", d.Val.Note(), d.Member)
}

func (d *Dot) Pos() position {
	return d.Val.Pos()
}

type Deref struct {
	Val AST
}

func (d *Deref) ASTType(c *Context) ASTType {
	t := d.Val.ASTType(c)
	if t.Indirection == 0 {
		CompileErrorF(d, "Cannot dereference non-pointer type %s", t)
	}
	if t.NilMask&1 != 0 {
		CompileErrorF(d.Val, "Cannot dereference nullable pointer type %s", t)
	}
	t.Indirection -= 1
	t.MutMask >>= 1 // consume the outermost pointer level's mut bit
	t.MutMask &^= 1 // mut bit 0 is not meaningful on value/pointer types
	t.OwnedMask >>= 1
	t.NilMask >>= 1
	// If the result is a plain non-pointer, non-slice/array value, MutMask is
	// meaningless (binding mutability is tracked in constBindings, not here).
	if t.Indirection == 0 && !t.IsSliceOrArray() {
		t.MutMask = 0
	}
	return t
}

func (d *Deref) Note() string {
	return fmt.Sprintf("Deref *(...)")
}

func (d *Deref) Pos() position {
	return d.Val.Pos()
}

type NonNullAssert struct {
	Val AST
	p   position
}

func (n *NonNullAssert) ASTType(c *Context) ASTType {
	t := n.Val.ASTType(c)
	if path, ok := FlowPathForExpr(n.Val); ok && c.NullFact(path) == NullKnownNull {
		CompileErrorF(n, "Postfix ? asserts \"%s\" is non-null, but it is known to be nil", path.Key())
	}
	if t.Indirection == 0 || t.NilMask&1 == 0 {
		if sym, ok := n.Val.(*Symbol); ok {
			if declared, ok := c.DeclaredTypeForVar(sym.Name); ok && declared.Indirection > 0 && declared.NilMask&1 != 0 {
				return t
			}
		}
		CompileErrorF(n, "Postfix ? requires a nullable pointer, got %s", t)
	}
	t.NilMask &^= 1
	return t
}

func (n *NonNullAssert) Note() string {
	return fmt.Sprintf("nonnull (%s)?", n.Val.Note())
}

func (n *NonNullAssert) Pos() position {
	return n.p
}

// Address represents the `&` operator. Two forms:
//
//   - Var-of-name (`&someVar`): Var holds the variable name, Lit is nil.
//     Supported at runtime and in static-init contexts.
//
//   - Address-of-literal (`&someStructLiteral`): Lit holds the inner
//     expression, Var is "". Only supported in static-init contexts —
//     bosc allocates an anonymous global to hold the literal and the
//     pointer slot relocates to that global. Runtime codegen rejects
//     this form (can't take an address of a temporary in general).
type Address struct {
	Var string // when non-empty: address-of-named-variable
	Lit AST    // when non-nil:   address-of-literal (static-init only)
	p   position
}

func (a *Address) ASTType(c *Context) ASTType {
	if name, ok := a.NamedTarget(); ok {
		// Function address: &someFn yields a function-pointer type
		// (fn(args) ret) — pointer-sized value carrying the signature
		// so call sites can set up the ABI correctly.
		if decl, ok := c.FuncDeclForName(name); ok {
			var sig FuncSig
			for _, arg := range decl.Args {
				sig.Args = append(sig.Args, arg.Type)
			}
			sig.Return = decl.Return
			return ASTType{FuncSig: &sig}
		}
		t, ok := c.TypeForVar(name)
		if !ok {
			CompileErrorF(a, "Variable %q is not declared", name)
		}
		// Adding one pointer level: shift existing mut/owned bits up by one position.
		t.MutMask <<= 1
		t.OwnedMask <<= 1
		t.NilMask <<= 1
		// &x places x at position 1 in the new type (one deref through the new pointer
		// reaches x's binding). If x is var, bit 1 = write-through to x's value.
		if !c.IsConst(name) {
			t.MutMask |= 1 << 1
		}
		t.Indirection += 1
		return t
	}
	if a.Lit != nil {
		// Cross-package variable: `&pkg.varname` — Dot.ASTType already
		// resolves the member type correctly. Shift and set mut (globals
		// are always var) the same way the named-local path does.
		if dot, ok := a.Lit.(*Dot); ok {
			if sym, ok2 := dot.Val.(*Symbol); ok2 && c.IsImportedPackage(sym.Name) {
				t := a.Lit.ASTType(c)
				t.MutMask = (t.MutMask << 1) | (1 << 1)
				t.OwnedMask <<= 1
				t.NilMask <<= 1
				t.Indirection++
				return t
			}
		}
		// Address-of-literal: the type is *(literal's type), with no
		// existing mut/owned bits to shift since the literal is fresh
		// storage we're synthesizing.
		t := a.Lit.ASTType(c)
		t.Indirection += 1
		return t
	}
	panic("Address has no target")
}

func (a *Address) NamedTarget() (string, bool) {
	if a.Var != "" {
		return a.Var, true
	}
	if sym, ok := a.Lit.(*Symbol); ok {
		return sym.Name, true
	}
	return "", false
}

func (a *Address) Note() string {
	if a.Lit != nil {
		return fmt.Sprintf("Address &(%s)", a.Lit.Note())
	}
	return fmt.Sprintf("Address &%s", a.Var)
}

func (a *Address) Pos() position {
	return a.p
}

type Assignment struct {
	Target AST
	Val    AST
}

func (*Assignment) ASTType(c *Context) ASTType {
	return voidASTType()
}

func (a *Assignment) Note() string {
	return fmt.Sprintf("Assignment %s = %s", a.Target.Note(), a.Val.Note())
}

func (a *Assignment) Pos() position {
	return a.Target.Pos()
}

type StructField struct {
	Name string
	Val  AST
}

type StructLiteral struct {
	Type   ASTType
	Fields []StructField
	p      position
}

func (s *StructLiteral) ASTType(c *Context) ASTType {
	if s.Type.AnonFields != nil {
		return s.Type
	}
	if s.Type.Name == "" {
		panic(&interpreterError{
			msg: "anonymous struct literal type not resolved; use in a typed context (return, var x T = ..., etc.)",
			p:   s.p,
		})
	}
	_, ok := c.StructDeclForName(s.Type.Name)
	if !ok {
		panic(&interpreterError{
			msg: fmt.Sprintf("No such struct type %q", s.Type.Name),
			p:   s.p,
		})
	}
	return s.Type
}

func (s *StructLiteral) Note() string {
	return fmt.Sprintf("struct literal %s", s.Type)
}

func (s *StructLiteral) Pos() position {
	return s.p
}

// ArrayLiteral is `[e1, e2, …, eN]` at value-start position. Element
// types and length are intrinsic to the literal; element-to-destination
// coercion happens at the encoding / move site (following the same
// pattern as <intlit>). ASTType reports the inferred element type from
// the first element and the literal's length, so type-check sites can
// reason about it like any other array.
type ArrayLiteral struct {
	Elements []AST
	p        position
}

func (al *ArrayLiteral) ASTType(c *Context) ASTType {
	var elemT ASTType
	if len(al.Elements) > 0 {
		elemT = al.Elements[0].ASTType(c)
	} else {
		// Empty literal: defer the element type entirely to the
		// destination context. <intlit> acts as the polymorphic
		// placeholder per existing precedent.
		elemT = intlitASTType()
	}
	return ASTType{
		Element:   &elemT,
		ArraySize: len(al.Elements),
	}
}

func (al *ArrayLiteral) Note() string {
	return fmt.Sprintf("array literal [%d]", len(al.Elements))
}

func (al *ArrayLiteral) Pos() position {
	return al.p
}

type IfStmt struct {
	Cond AST
	Then AST
	Else AST
}

func (*IfStmt) ASTType(c *Context) ASTType {
	// TODO: Same as blocks, we'll make these expressions later.
	return voidASTType()
}

func (*IfStmt) Note() string {
	return fmt.Sprintf("if ...")
}

func (i *IfStmt) Pos() position {
	return i.Cond.Pos()
}

type For struct {
	Init AST
	Cond AST
	Step AST
	Body AST
	p    position
}

func (*For) ASTType(c *Context) ASTType {
	return voidASTType()
}

func (f *For) Note() string {
	if f.Init == nil {
		return fmt.Sprintf("for (; ...) { ... }")
	}
	return fmt.Sprintf("for (%s ...) { ... }", f.Init.Note())
}

func (f *For) Pos() position {
	if f.Init == nil {
		return f.p
	}
	return f.Init.Pos()
}

type Loop struct {
	Body AST
	p    position
}

func (*Loop) ASTType(c *Context) ASTType {
	return voidASTType()
}

func (*Loop) Note() string {
	return "loop { ... }"
}

func (l *Loop) Pos() position {
	return l.p
}

// Operation on 2 expressions (i.e. +, -, *, <, <=, == etc.)
type Op2 struct {
	Type   nodetype
	First  AST
	Second AST
}

func (o *Op2) ASTType(c *Context) ASTType {
	// TODO: this will be expanded as more types are added.
	// For now, it's only num that can have operations.
	switch o.Type {
	case n_lt, n_le, n_gt, n_ge, n_deq, n_neq, n_booland, n_boolor:
		return boolASTType()
	case n_add, n_sub, n_mul, n_div, n_bitand, n_bitor:
		ft := o.First.ASTType(c)
		if ft.Same(intlitASTType()) {
			return o.Second.ASTType(c)
		}
		return ft
	}
	panic(&interpreterError{msg: fmt.Sprintf("Operator %q is not supported on types %s and %s", o.Note(), o.First.ASTType(c), o.Second.ASTType(c)), p: o.Pos()})
}

func (o *Op2) Note() string {
	var op string
	switch o.Type {
	case n_add:
		op = "+"
	case n_sub:
		op = "-"
	case n_mul:
		op = "*"
	case n_div:
		op = "/"
	case n_deq:
		op = "=="
	case n_neq:
		op = "!="
	case n_lt:
		op = "<"
	case n_le:
		op = "<="
	case n_gt:
		op = ">"
	case n_ge:
		op = ">="
	case n_booland:
		op = "&&"
	case n_boolor:
		op = "||"
	case n_bitand:
		op = "&"
	case n_bitor:
		op = "|"
	}
	return fmt.Sprintf("Op (%s) %s (%s)", o.First.Note(), op, o.Second.Note())
}

func (o *Op2) Pos() position {
	return o.First.Pos()
}

type Not struct {
	Val AST
	p   position
}

func (n *Not) ASTType(c *Context) ASTType {
	t := n.Val.ASTType(c)
	// Logical not requires a scalar zero/non-zero value: any pointer,
	// integer, bool, or untyped integer literal. Slices, fixed arrays,
	// structs, and function-pointer types have no meaningful zero test.
	if t.Indirection == 0 {
		if t.IsSliceOrArray() || t.FuncSig != nil {
			CompileErrorF(n, "Logical not (!) requires scalar or pointer operand, got %s", t)
		}
		if _, ok := c.StructDeclForName(t.Name); ok {
			CompileErrorF(n, "Logical not (!) requires scalar or pointer operand, got %s", t)
		}
	}
	return boolASTType()
}

func (n *Not) Note() string {
	return fmt.Sprintf("not (%s)", n.Val.Note())
}

func (n *Not) Pos() position {
	return n.p
}

type Return struct {
	Val AST
	p   position
}

func (*Return) ASTType(*Context) ASTType {
	return voidASTType()
}

func (*Return) Note() string {
	return "return ..."
}

func (r *Return) Pos() position {
	return r.p
}

type Continue struct {
	Step AST
	p    position
}

func (*Continue) ASTType(*Context) ASTType {
	return voidASTType()
}

func (*Continue) Note() string {
	return "continue ..."
}

func (c *Continue) Pos() position {
	return c.p
}

func rewriteContinuesForStep(a AST, step AST) AST {
	if step == nil {
		return a
	}
	switch ast := a.(type) {
	case *Continue:
		return &Continue{Step: step, p: ast.p}
	case *Block:
		body := make([]AST, len(ast.Body))
		for i, st := range ast.Body {
			body[i] = rewriteContinuesForStep(st, step)
		}
		return &Block{Body: body}
	case *IfStmt:
		out := *ast
		out.Then = rewriteContinuesForStep(ast.Then, step)
		if ast.Else != nil {
			out.Else = rewriteContinuesForStep(ast.Else, step)
		}
		return &out
	case *Loop:
		return a
	default:
		return a
	}
}

func lowerForToLoop(f *For) AST {
	body := rewriteContinuesForStep(f.Body, f.Step)
	var loopBody AST
	if f.Cond != nil {
		then := &Block{Body: []AST{&Break{p: f.Cond.Pos()}}}
		elseBody := []AST{body}
		if f.Step != nil {
			elseBody = append(elseBody, f.Step)
		}
		loopBody = &IfStmt{
			Cond: &Not{Val: f.Cond, p: f.Cond.Pos()},
			Then: then,
			Else: &Block{Body: elseBody},
		}
	} else {
		stmts := []AST{body}
		if f.Step != nil {
			stmts = append(stmts, f.Step)
		}
		loopBody = &Block{Body: stmts}
	}
	loop := &Loop{Body: loopBody, p: f.p}
	if f.Init == nil {
		return loop
	}
	return &Block{Body: []AST{f.Init, loop}}
}

// OwnedPromotion wraps an expression to assert ownership: owned(x).
// This is an unsafe escape hatch — the compiler accepts the expression
// where an owned type is required, but does NOT mark the inner variable
// as moved. The programmer is responsible for the aliasing invariant.
type OwnedPromotion struct {
	Val AST
	p   position
}

func (o *OwnedPromotion) ASTType(c *Context) ASTType {
	t := o.Val.ASTType(c)
	// Set owned at the innermost level: for a plain value (Ind=0) this is bit 0
	// (the value itself is owned); for a pointer (Ind=1) this is bit 1 (the
	// pointed-to value is owned), matching the '*owned T' convention.
	t.OwnedMask |= 1 << uint(t.Indirection)
	return t
}
func (o *OwnedPromotion) Note() string  { return fmt.Sprintf("owned(%s)", o.Val.Note()) }
func (o *OwnedPromotion) Pos() position { return o.p }

type TypeAliasDecl struct {
	Name       string
	Underlying ASTType
	p          position
}

func (*TypeAliasDecl) ASTType(*Context) ASTType { return voidASTType() }
func (d *TypeAliasDecl) Note() string           { return fmt.Sprintf("type %s %s", d.Name, d.Underlying) }
func (d *TypeAliasDecl) Pos() position          { return d.p }

// InterfaceMethodSig is a single method signature in an interface declaration.
// Params[0] is the receiver; its type uses "self" as a placeholder for the
// concrete implementing type.
type InterfaceMethodSig struct {
	Name   string
	Params []Binding
	Return ASTType
	p      position
}

// InterfaceDecl is the AST node for `interface Name { sig... }`.
type InterfaceDecl struct {
	Name    string
	Methods []InterfaceMethodSig
	p       position
}

func (*InterfaceDecl) ASTType(*Context) ASTType { return voidASTType() }
func (d *InterfaceDecl) Note() string           { return fmt.Sprintf("interface %s", d.Name) }
func (d *InterfaceDecl) Pos() position          { return d.p }

// TypeWithMethodsDecl is the AST node for `type TypeName Base { methods... }`.
type TypeWithMethodsDecl struct {
	Name       string
	Underlying ASTType
	Methods    []*FuncDecl // each FuncDecl.Name is the bare method name
	p          position
}

func (*TypeWithMethodsDecl) ASTType(*Context) ASTType { return voidASTType() }
func (d *TypeWithMethodsDecl) Note() string {
	return fmt.Sprintf("type %s %s { ... }", d.Name, d.Underlying)
}
func (d *TypeWithMethodsDecl) Pos() position { return d.p }

// ValuesCase is one case in a values declaration: a symbolic name with an
// optional list of statically-evaluated projection initializers. Tag is a
// compiler-private dense index assigned at ToAST time and is never visible
// in user-level code.
type ValuesCase struct {
	Name string
	Tag  int64
	Expr []AST
	p    position
}

// ValuesDecl is the AST node for `type Name values [(projections)] { cases } [{ methods }]`.
// Projections names the target types (ordered) for each case's static
// initializer list; the per-case Expr slices match Projections positionally.
// Cases without projection initializers are valid only when the type has
// no projection signature (`type Name values { CASE_A; CASE_B }`).
type ValuesDecl struct {
	Name        string
	TagType     ASTType // v1: always i64
	Projections []ASTType
	Cases       []ValuesCase
	Methods     []*FuncDecl
	p           position
}

func (*ValuesDecl) ASTType(*Context) ASTType { return voidASTType() }
func (d *ValuesDecl) Note() string           { return fmt.Sprintf("type %s values { ... }", d.Name) }
func (d *ValuesDecl) Pos() position          { return d.p }

// vtableSpec records one vtable entry to be emitted.
type vtableSpec struct {
	typeName  string   // concrete type name (e.g. "myerror")
	ifaceName string   // interface name (e.g. "error")
	pkgName   string   // package that defines the type
	methods   []string // method names in interface declaration order
}

type Dispose struct {
	Var string
	p   position
}

func (*Dispose) ASTType(*Context) ASTType { return voidASTType() }
func (d *Dispose) Note() string           { return fmt.Sprintf("dispose(%s)", d.Var) }
func (d *Dispose) Pos() position          { return d.p }

type Break struct {
	p position
}

func (*Break) ASTType(*Context) ASTType {
	return voidASTType()
}

func (*Break) Note() string {
	return "break ..."
}

func (b *Break) Pos() position {
	return b.p
}

type Index struct {
	Val  AST
	NAST AST
	N    uint64
}

func (i *Index) ASTType(c *Context) ASTType {
	t := i.Val.ASTType(c)
	if !t.IsSliceOrArray() {
		panic(fmt.Sprintf("CANNOT INDEX INTO NON-ARRAY TYPE %v", t))
	}
	return t.ElementType()
}

func (i *Index) Note() string {
	return fmt.Sprintf("Index (...)[%d]", i.N)
}

func (i *Index) Pos() position {
	return i.Val.Pos()
}

type SliceOp struct {
	Val   AST
	Lower AST
	Upper AST
}

// isLvalueMutable reports whether the lvalue expression a refers to writable
// storage: a non-const binding, a *mut pointee, or an element of a mut slice.
// This mirrors the logic in lvalueIsWritable (compile.go) but returns bool
// rather than an error reason. Used by SliceOp.ASTType to propagate mut.
func isLvalueMutable(c *Context, a AST) bool {
	switch v := a.(type) {
	case *Symbol:
		return !c.IsConst(v.Name)
	case *Deref:
		t := v.Val.ASTType(c)
		return t.Indirection > 0 && t.MutMask&(1<<1) != 0
	case *Dot:
		baseType := v.Val.ASTType(c)
		if baseType.Indirection > 0 {
			return baseType.MutMask&(1<<1) != 0
		}
		return isLvalueMutable(c, v.Val)
	case *Index:
		baseType := v.Val.ASTType(c)
		if baseType.Indirection > 0 {
			return baseType.MutMask&(1<<1) != 0
		}
		if baseType.IsSlice() {
			return baseType.MutMask&(1<<1) != 0
		}
		return isLvalueMutable(c, v.Val)
	case *NonNullAssert:
		return isLvalueMutable(c, v.Val)
	}
	return false
}

func (s *SliceOp) ASTType(c *Context) ASTType {
	t := s.Val.ASTType(c)
	if t.IsSlice() {
		// Slice-of-slice: mut is already encoded in the slice type.
		return t
	}
	if t.IsArray() {
		// Convert array to slice. Propagate element writability from the
		// source lvalue: a var array produces mut T[], a const array produces T[].
		t.ArraySize = 0
		if isLvalueMutable(c, s.Val) {
			t.MutMask |= 1 << 1
		} else {
			t.MutMask &^= 1 << 1
		}
		return t
	}
	panic("Cannot perform slice operation on non-array or non-slice")
}

func (s *SliceOp) Note() string {
	return fmt.Sprintf("Slice operation %s[...:...]", s.Val.Note())
}

func (s *SliceOp) Pos() position {
	return s.Val.Pos()
}

// TODO: Do we need this, or can we just use the actual value?
// What's the purpose of boxing it?
type Literal struct {
	Val interface{}
	p   position
}

func (l *Literal) ASTType(*Context) ASTType {
	switch l.Val.(type) {
	case string:
		return byteSliceASTType()
	case uint64:
		return intlitASTType()
	case byte:
		return byteASTType()
	case nil:
		return ASTType{Name: "<nil>"}
	}
	panic(&interpreterError{msg: fmt.Sprintf("Unsupported literal value of type %T", l.Val), p: l.p})
}

func (l *Literal) Note() string {
	if l.Val == nil {
		return "nil"
	}
	return fmt.Sprintf("literal %v %v", l.Val, reflect.TypeOf(l.Val).String())
}

func (l *Literal) Pos() position {
	return l.p
}

type Symbol struct {
	Name string
	p    position
}

func (s *Symbol) ASTType(c *Context) ASTType {
	r := ResolveSelector(c, s)
	if r.Kind == ResolvedRuntimeValue {
		return r.Type
	}
	panic(&interpreterError{
		msg: fmt.Sprintf("Variable \"%s\" undeclared.", s.Name),
		p:   s.p,
	})
}

func (s *Symbol) Note() string {
	return fmt.Sprintf("symbol %v", s.Name)
}

func (s *Symbol) Pos() position {
	return s.p
}

func ParseErrorF(n *Node, f string, args ...any) {
	panic(&interpreterError{
		msg: fmt.Sprintf(f, args...),
		p:   n.p,
	})
}

func (n *Node) ToAST(c *Context) (a AST, e error) {
	defer func() {
		if err := recover(); err != nil {
			if le, ok := err.(*interpreterError); ok {
				a = nil
				e = le
				return
			} else if _, ok := err.(eofError); ok {
				a = nil
				e = nil
				return
			}
			panic(e)
		}
	}()
	return n.toASTTop(c), nil
}

// type kind int

// const (
// 	k_none kind = iota
// 	k_struct
// )

// toASTTop converts the parsed node tree into a more
// proper AST, doing some basic checks along the way.
//
// Note, when we call toASTTop recursively, we always pass
// buildStructDecl constructs a StructDecl from an n_typename node with sval="<struct>".
func buildStructDecl(name string, structNode *Node, p position) *StructDecl {
	var sd StructDecl
	sd.TName = name
	sd.p = p
	for _, a := range structNode.args {
		if a.t != n_stfield {
			ParseErrorF(a, "Expected a struct field, but found %s", a.t)
		}
		sd.Fields = append(sd.Fields, Binding{
			Name: a.sval,
			Type: mkTypename(a.args[0]),
		})
	}
	return &sd
}

// a new context. We only want to define globals. This also
// means the context is write-only, since we cannot rely on it
// to have complete information at any point during AST construction.
func (n *Node) toASTTop(c *Context) AST {
	switch n.t {
	case n_struct:
		ParseErrorF(n, "named struct declarations are not allowed; use `type %s struct { ... }` instead", n.sval)
		return nil
	case n_var:
		var v VarDecl
		v.Name = n.sval
		v.IsConst = n.ival != 0
		v.p = n.p
		if n.args[0].sval == "<infer>" {
			// Type inference: leave type as sentinel; compile.go resolves it
			// from the initializer once the full context is available.
			if len(n.args) < 2 {
				ParseErrorF(n, "var %s: type inference requires an initializer", v.Name)
			}
			v.Type = ASTType{Name: "<infer>"}
			v.Init = n.args[1].toASTTop(c)
			// Don't call BindVar or set prebound here; compile.go handles both
			// after resolving the inferred type.
		} else {
			v.Type = mkTypename(n.args[0])
			if len(n.args) > 1 {
				v.Init = n.args[1].toASTTop(c)
			}
			c.BindVar(&v, v.Name, v.Type, v.IsConst)
			// Mark the binding as pre-installed by ToAST so the *VarDecl
			// handler in Compile skips the redundant re-bind when it
			// revisits this declaration. For nested vars, c here is the
			// throwaway NewContext() passed by n_fn/n_block, and the
			// marker is discarded along with that context.
			c.prebound[v.Name] = true
		}
		return &v
	case n_multibind:
		// Multi-bind: args[0..N-2] are n_var binding specs (no initializer),
		// args[N-1] is the shared initializer expression.
		var mb MultiBindDecl
		mb.p = n.p
		for i := 0; i < len(n.args)-1; i++ {
			bnode := n.args[i]
			var v VarDecl
			v.Name = bnode.sval
			v.IsConst = bnode.ival != 0
			v.p = bnode.p
			if bnode.args[0].sval == "<infer>" {
				v.Type = ASTType{Name: "<infer>"}
			} else {
				v.Type = mkTypename(bnode.args[0])
			}
			mb.Bindings = append(mb.Bindings, v)
		}
		mb.Init = n.args[len(n.args)-1].toASTTop(NewContext())
		return &mb
	case n_fn:
		var fn FuncDecl
		fn.Name = n.sval
		fn.p = n.p
		nargs := int(n.ival)
		args := n.args
		for i := 0; i < nargs; i++ {
			a := args[0]
			name := a.sval
			t := mkTypename(a.args[0])
			// n_arg ival: 0 = const by default, 1 = var (explicitly declared)
			fn.Args = append(fn.Args, Binding{
				Name:    name,
				Type:    t,
				IsConst: a.ival == 0,
			})
			args = args[1:]
		}
		fn.Return = mkTypename(args[0])
		body := args[1]
		if body.t != n_block {
			ParseErrorF(body, "Was expecting a block for the function body, but got %v", body.t)
		}
		fn.Body = body.toASTTop(NewContext()).(*Block)
		c.DefineFunc(fn.Name, &fn)
		return &fn
	case n_block:
		var b Block
		b.p = n.p
		for _, bn := range n.args {
			b.Body = append(b.Body, bn.toASTTop(NewContext()))
		}
		return &b
	case n_funcall:
		var f Funcall
		f.Callee = &Symbol{Name: n.sval, p: n.p}
		f.p = n.p
		for _, a := range n.args {
			f.Args = append(f.Args, a.toASTTop(NewContext()))
		}
		return &f
	case n_str:
		//str := c.String(n.sval)
		//return &Literal{Val: str}
		return &Literal{Val: n.sval, p: n.p}
	case n_dot:
		// pkg.fn(args) — left is a bare symbol, right is a function call.
		// Construct a qualified Funcall whose Callee is Dot{Symbol(pkg), fn}.
		// The resolver and call-site dispatch tease this apart into
		// imported-package call, method on variable, or static-method-on-type.
		if n.args[0].t == n_symbol && n.args[1].t == n_funcall {
			fcall := n.args[1]
			var f Funcall
			f.Callee = &Dot{
				Val:    &Symbol{Name: n.args[0].sval, p: n.args[0].p},
				Member: fcall.sval,
			}
			f.p = n.p
			for _, a := range fcall.args {
				f.Args = append(f.Args, a.toASTTop(NewContext()))
			}
			return &f
		}
		// (expr).method(args) — left is an arbitrary expression, right is a call.
		// Callee carries the receiver as the Val side of the Dot so the call
		// site can evaluate it and look up the method by the receiver's type.
		if n.args[1].t == n_funcall {
			fcall := n.args[1]
			f := &Funcall{
				Callee: &Dot{
					Val:    n.args[0].toASTTop(NewContext()),
					Member: fcall.sval,
				},
				p: n.p,
			}
			for _, a := range fcall.args {
				f.Args = append(f.Args, a.toASTTop(NewContext()))
			}
			return f
		}
		var d Dot
		d.Val = n.args[0].toASTTop(NewContext())
		if n.args[1].t != n_symbol {
			ParseErrorF(n.args[1], "Expected a member name, but got %v", n.args[1].t)
		}
		d.Member = n.args[1].sval
		return &d
	case n_deref:
		return &Deref{Val: n.args[0].toASTTop(NewContext())}
	case n_nonnull:
		return &NonNullAssert{Val: n.args[0].toASTTop(NewContext()), p: n.p}
	case n_symbol:
		if n.sval == "nil" {
			return &Literal{Val: nil, p: n.p}
		}
		return &Symbol{Name: n.sval, p: n.p}
	case n_eq:
		return &Assignment{
			Target: n.args[0].toASTTop(NewContext()),
			Val:    n.args[1].toASTTop(NewContext()),
		}
	case n_number:
		return &Literal{Val: n.ival, p: n.p}
	case n_byte:
		return &Literal{Val: byte(n.ival), p: n.p}
	case n_stlit:
		var s StructLiteral
		s.p = n.p
		// Resolve the leading type identifier through the shared
		// selector resolver. For Stage 1 this is structural unification
		// with no observable change: the resolver returns the same
		// struct identity the legacy lookup found via sval. Stage 2
		// will route values types through this same step.
		if n.typeIdent != nil {
			ident := n.typeIdent.toASTTop(NewContext())
			r := ResolveSelector(c, ident)
			if r.Kind == ResolvedType {
				s.Type = r.Type
			} else {
				s.Type = ASTType{Name: n.sval}
			}
		} else {
			s.Type = ASTType{Name: n.sval}
		}
		for _, f := range n.args {
			if f.t != n_stfield {
				ParseErrorF(f, "Expected struct field, but got %v", f.t)
			}
			s.Fields = append(s.Fields, StructField{
				Name: f.sval,
				Val:  f.args[0].toASTTop(NewContext()),
			})
		}
		return &s
	case n_address:
		// Two forms from the parser: address-of-name (sval set, no args)
		// and address-of-literal (args[0] set, sval empty). The latter
		// is only meaningful in static-init contexts; runtime codegen
		// rejects it with a clear error.
		if len(n.args) > 0 {
			return &Address{
				Lit: n.args[0].toASTTop(NewContext()),
				p:   n.p,
			}
		}
		return &Address{
			Var: n.sval,
			p:   n.p,
		}
	case n_index:
		// args[0] = value to index into (any AST), args[1] = index expression.
		val := n.args[0].toASTTop(NewContext())
		idx := n.args[1]
		if idx.t == n_number {
			return &Index{Val: val, N: idx.ival}
		}
		return &Index{Val: val, NAST: idx.toASTTop(NewContext())}
	case n_slice:
		// args[0] = value to slice (any AST), args[1] = lower (nil-ok),
		// args[2] = upper (nil-ok).
		val := n.args[0].toASTTop(NewContext())
		var lower, upper AST
		if n.args[1] != nil {
			lower = n.args[1].toASTTop(NewContext())
		}
		if n.args[2] != nil {
			upper = n.args[2].toASTTop(NewContext())
		}
		return &SliceOp{Val: val, Lower: lower, Upper: upper}
	case n_if:
		ifs := IfStmt{
			Cond: n.args[0].toASTTop(NewContext()),
			Then: n.args[1].toASTTop(NewContext()),
		}
		if len(n.args) == 3 {
			ifs.Else = n.args[2].toASTTop(NewContext())
		}
		return &ifs
	case n_for:
		var init AST
		if n.args[0].t != n_none {
			init = n.args[0].toASTTop(NewContext())
		}
		var cond AST
		if n.args[1].t != n_none {
			cond = n.args[1].toASTTop(NewContext())
		}
		var step AST
		if n.args[2].t != n_none {
			step = n.args[2].toASTTop(NewContext())
		}
		body := n.args[3].toASTTop(NewContext())
		return lowerForToLoop(&For{
			Init: init,
			Cond: cond,
			Step: step,
			Body: body,
			p:    n.p,
		})
	case n_lt, n_le, n_gt, n_ge, n_deq, n_neq, n_add, n_sub, n_mul, n_div, n_booland, n_boolor, n_bitand, n_bitor:
		return &Op2{
			Type:   n.t,
			First:  n.args[0].toASTTop(NewContext()),
			Second: n.args[1].toASTTop(NewContext()),
		}
	case n_neg:
		return &Op2{
			Type:   n_sub,
			First:  &Literal{Val: uint64(0)},
			Second: n.args[0].toASTTop(NewContext()),
		}
	case n_not:
		return &Not{Val: n.args[0].toASTTop(NewContext()), p: n.p}
	case n_return:
		if len(n.args) == 0 {
			return &Return{p: n.p}
		}
		// Multi-value return: `return e1, e2` — lower to a StructLiteral with
		// positional fields _0, _1, ... The type is left empty so that
		// fillAnonymousLiteralIfNeeded in compile.go fills it in from the
		// function's declared return type (same path as bare `{ field: val }`).
		if len(n.args) > 1 {
			var fields []StructField
			for i, a := range n.args {
				fields = append(fields, StructField{
					Name: fmt.Sprintf("_%d", i),
					Val:  a.toASTTop(NewContext()),
				})
			}
			sl := &StructLiteral{Fields: fields, p: n.p}
			return &Return{Val: sl, p: n.p}
		}
		return &Return{
			Val: n.args[0].toASTTop(NewContext()),
			p:   n.p,
		}
	case n_continue:
		return &Continue{
			p: n.p,
		}
	case n_break:
		return &Break{
			p: n.p,
		}
	case n_dispose:
		return &Dispose{Var: n.sval, p: n.p}
	case n_ownedpromo:
		return &OwnedPromotion{Val: n.args[0].toASTTop(c), p: n.p}
	case n_arrlit:
		var al ArrayLiteral
		al.p = n.p
		for _, e := range n.args {
			al.Elements = append(al.Elements, e.toASTTop(NewContext()))
		}
		return &al
	case n_typedecl:
		if n.args[0].sval == "<struct>" {
			sd := buildStructDecl(n.sval, n.args[0], n.p)
			c.DefineStruct(sd.TName, sd)
			return sd
		}
		underlying := mkTypename(n.args[0])
		// Propagate Signed from base type if it's a built-in.
		switch underlying.Name {
		case "i8", "i16", "i32", "i64":
			underlying.Signed = true
		}
		c.DefineTypeAlias(n.p, n.sval, underlying)
		return &TypeAliasDecl{Name: n.sval, Underlying: underlying, p: n.p}
	case n_typedecl_with_methods:
		if n.args[0].sval == "<struct>" {
			sd := buildStructDecl(n.sval, n.args[0], n.p)
			c.DefineStruct(sd.TName, sd)
			var methods []*FuncDecl
			for _, mn := range n.args[1:] {
				if mn.t != n_fn {
					ParseErrorF(mn, "Expected method definition in type block, got %v", mn.t)
				}
				var fn FuncDecl
				fn.Name = mn.sval
				fn.p = mn.p
				nargs := int(mn.ival)
				margs := mn.args
				for i := 0; i < nargs; i++ {
					a := margs[0]
					fn.Args = append(fn.Args, Binding{
						Name:    a.sval,
						Type:    mkTypename(a.args[0]),
						IsConst: a.ival == 0,
					})
					margs = margs[1:]
				}
				fn.Return = mkTypename(margs[0])
				body := margs[1]
				if body.t != n_block {
					ParseErrorF(body, "Expected a block for method body, got %v", body.t)
				}
				fn.Body = body.toASTTop(NewContext()).(*Block)
				qualName := n.sval + "." + fn.Name
				c.DefineFunc(qualName, &FuncDecl{
					Name:   qualName,
					Args:   fn.Args,
					Return: fn.Return,
					Body:   fn.Body,
					p:      fn.p,
				})
				methods = append(methods, &fn)
			}
			c.DefineTypeMethods(n.sval, methods)
			return sd
		}
		underlying := mkTypename(n.args[0])
		switch underlying.Name {
		case "i8", "i16", "i32", "i64":
			underlying.Signed = true
		}
		c.DefineTypeAlias(n.p, n.sval, underlying)
		var methods []*FuncDecl
		for _, mn := range n.args[1:] {
			if mn.t != n_fn {
				ParseErrorF(mn, "Expected method definition in type block, got %v", mn.t)
			}
			var fn FuncDecl
			fn.Name = mn.sval // bare method name
			fn.p = mn.p
			nargs := int(mn.ival)
			margs := mn.args
			for i := 0; i < nargs; i++ {
				a := margs[0]
				fn.Args = append(fn.Args, Binding{
					Name:    a.sval,
					Type:    mkTypename(a.args[0]),
					IsConst: a.ival == 0,
				})
				margs = margs[1:]
			}
			fn.Return = mkTypename(margs[0])
			body := margs[1]
			if body.t != n_block {
				ParseErrorF(body, "Expected a block for method body, got %v", body.t)
			}
			fn.Body = body.toASTTop(NewContext()).(*Block)
			// Register method under qualified name so FuncDeclForCall can find it.
			qualName := n.sval + "." + fn.Name
			c.DefineFunc(qualName, &FuncDecl{
				Name:   qualName,
				Args:   fn.Args,
				Return: fn.Return,
				Body:   fn.Body,
				p:      fn.p,
			})
			methods = append(methods, &fn)
		}
		c.DefineTypeMethods(n.sval, methods)
		return &TypeWithMethodsDecl{Name: n.sval, Underlying: underlying, Methods: methods, p: n.p}
	case n_valuesdecl:
		nProj := int(n.ival)
		projNodes := n.args[:nProj]
		rest := n.args[nProj:]
		// Cases come first in `rest`, then methods. Both are tagged by
		// node type so we walk linearly until we hit the first non-case.
		var caseNodes []*Node
		for len(rest) > 0 && rest[0].t == n_valuescase {
			caseNodes = append(caseNodes, rest[0])
			rest = rest[1:]
		}
		methodNodes := rest

		decl := &ValuesDecl{
			Name:    n.sval,
			TagType: ASTType{Name: "i64", Signed: true},
			p:       n.p,
		}
		// Projections: validate that no projection type is repeated in
		// the header.
		seenProj := make(map[string]bool, nProj)
		for _, pn := range projNodes {
			pt := mkTypename(pn)
			key := pt.String()
			if seenProj[key] {
				ParseErrorF(pn, "Values type %s declares projection %s more than once", n.sval, pt)
			}
			seenProj[key] = true
			decl.Projections = append(decl.Projections, pt)
		}
		// Cases: dense tags from 0, duplicate-name check, arity check
		// against the projection signature.
		seenCase := make(map[string]bool, len(caseNodes))
		for i, cn := range caseNodes {
			if seenCase[cn.sval] {
				ParseErrorF(cn, "Duplicate value name %s in values type %s", cn.sval, n.sval)
			}
			seenCase[cn.sval] = true
			if nProj > 0 && len(cn.args) != nProj {
				ParseErrorF(cn,
					"Value %s has %d projection initializer(s) but %s declares %d projection(s)",
					cn.sval, len(cn.args), n.sval, nProj)
			}
			if nProj == 0 && len(cn.args) > 0 {
				ParseErrorF(cn,
					"Value %s has projection initializer(s) but %s declares no projections",
					cn.sval, n.sval)
			}
			vc := ValuesCase{
				Name: cn.sval,
				Tag:  int64(i),
				p:    cn.p,
			}
			for _, e := range cn.args {
				vc.Expr = append(vc.Expr, e.toASTTop(NewContext()))
			}
			decl.Cases = append(decl.Cases, vc)
		}
		// Register the values type before processing methods so that
		// methods can reference the receiver type by name.
		c.DefineValuesType(n.p, n.sval, decl)
		// Methods: same lowering as type-with-methods. Methods register
		// under `Name.method` in funcs and on the type's method table.
		var methods []*FuncDecl
		for _, mn := range methodNodes {
			if mn.t != n_fn {
				ParseErrorF(mn, "Expected method definition in values block, got %v", mn.t)
			}
			var fn FuncDecl
			fn.Name = mn.sval
			fn.p = mn.p
			nargs := int(mn.ival)
			margs := mn.args
			for i := 0; i < nargs; i++ {
				a := margs[0]
				fn.Args = append(fn.Args, Binding{
					Name:    a.sval,
					Type:    mkTypename(a.args[0]),
					IsConst: a.ival == 0,
				})
				margs = margs[1:]
			}
			fn.Return = mkTypename(margs[0])
			body := margs[1]
			if body.t != n_block {
				ParseErrorF(body, "Expected a block for method body, got %v", body.t)
			}
			fn.Body = body.toASTTop(NewContext()).(*Block)
			qualName := n.sval + "." + fn.Name
			c.DefineFunc(qualName, &FuncDecl{
				Name:   qualName,
				Args:   fn.Args,
				Return: fn.Return,
				Body:   fn.Body,
				p:      fn.p,
			})
			methods = append(methods, &fn)
		}
		decl.Methods = methods
		if len(methods) > 0 {
			c.DefineTypeMethods(n.sval, methods)
		}
		return decl
	case n_interface_decl:
		var decl InterfaceDecl
		decl.Name = n.sval
		decl.p = n.p
		for _, sig := range n.args {
			if sig.t != n_interface_method {
				ParseErrorF(sig, "Expected interface method signature, got %v", sig.t)
			}
			var isig InterfaceMethodSig
			isig.Name = sig.sval
			isig.p = sig.p
			nparams := int(sig.ival)
			sargs := sig.args
			for i := 0; i < nparams; i++ {
				a := sargs[0]
				isig.Params = append(isig.Params, Binding{
					Name:    a.sval,
					Type:    mkTypename(a.args[0]),
					IsConst: true,
				})
				sargs = sargs[1:]
			}
			isig.Return = mkTypename(sargs[0])
			decl.Methods = append(decl.Methods, isig)
		}
		c.DefineInterface(n.p, n.sval, &decl)
		return &decl
	}
	spew.Dump(n)
	ParseErrorF(n, "Node Type %s Fell through AST Generator.\n", n.t)
	return nil
}
