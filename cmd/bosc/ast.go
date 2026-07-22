package main

import (
	"encoding/binary"
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

	// pointerHeldParams lists parameter names that are memory-backed values
	// (struct/slice/array/>8-byte) passed BY ADDRESS: the callee's slot holds a
	// pointer to the caller's value, not the value itself. Both these and
	// memory-backed locals report NameIsAddress==true, but they need opposite
	// handling when the *address of the value* is wanted -- a local's name IS
	// the storage (`lea name`), whereas one of these HOLDS the address
	// (`mov name`). NameHoldsPointer distinguishes them. Written by
	// compileFunctionBody as it declares each `argi`; per-function, so it lives
	// on the body context, not the root.
	pointerHeldParams map[string]bool

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
	// mutCandidates / mutRelied drive the var-never-reassigned nudge,
	// per-scope. mutCandidates holds the `var` declarations made *in this
	// scope* (name -> decl position). mutRelied marks the subset whose
	// var-ness was actually used (reassigned, value-field/array written,
	// address- or slice-taken); relied marks are routed to the binding's
	// own defining scope so sibling scopes that reuse a name stay distinct.
	// Reported at scope exit. Populated only when the nudge is enabled.
	mutCandidates map[string]position
	mutRelied     map[string]bool
	// usedCandidates / used drive the unused-binding check, per-scope.
	// usedCandidates holds every binding declared in this scope (locals
	// and, on a function context, parameters); used marks the subset that
	// is read, address-taken, passed, or consumed. A candidate not in used
	// at scope exit is dead. The discard `_` is never a candidate.
	usedCandidates map[string]position
	used           map[string]bool
	// bindingPos records the source position of every binding's declaration
	// (locals and parameters), keyed by name in its defining scope. Populated
	// alongside usedCandidates at each decl. Used to point diagnostics — e.g.
	// the unconsumed-owned error — at the declaration rather than the
	// enclosing function/block.
	bindingPos map[string]position
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
	// variantNarrow records, per scrutinee flow-path key, the values variant a
	// value is narrowed to inside a switch case body. Consulted by field
	// access (`e.fd`). Set on a per-case SubContext; reads walk parents.
	variantNarrow map[string]*ValuesCase
	// maps type alias names to their attached methods.
	typeMethods map[string][]*FuncDecl

	// aliasInProgress is the cycle guard for return-parameter alias
	// inference (alias_set). Keyed on the FuncDecl currently being
	// analyzed; re-entry (direct/mutual recursion) is detected here and
	// resolved by returning the member's provisional summary, with the
	// outermost SCC entry iterating to a fixpoint. Lives on the root
	// context only (lazily created).
	aliasInProgress map[*FuncDecl]bool
	// aliasProvisional holds in-flight summaries for functions on a
	// recursion cycle: a re-entered member returns its provisional
	// (∅-seeded, growing monotonically across fixpoint iterations)
	// instead of a final answer. Root-only.
	aliasProvisional map[*FuncDecl][][]int
	// aliasDepStack tracks, per active aliasSet analysis (one frame per
	// activation/iteration), WHICH in-progress functions' provisional
	// summaries the analysis consumed. A frame's dependencies decide
	// memoizability: a result that depended only on the function's OWN
	// provisional (or on since-finalized functions) is final and can be
	// memoized at any stack depth — this is what keeps a deep demand
	// chain over a recursive callee linear instead of exponential (each
	// non-cycle ancestor memoizes on its first pass once the cycle root
	// below it has finalized). Root-only.
	aliasDepStack []map[*FuncDecl]bool
	// aliasCapture, when non-nil, marks this compile as an analysis run:
	// compileFunctionBody is executing to a discard writer purely to
	// compute a return-alias summary, and the *Return case records each
	// return value's borrowed-param provenance into this accumulator
	// instead of (in addition to nothing — output is discarded anyway).
	// Set on the analysis run's function subcontext by aliasSet; nil
	// during real codegen. Inherited via SubContext so nested blocks of
	// the analyzed body see it, but NOT propagated into the analysis of a
	// different function (aliasSet installs a fresh one per function).
	aliasCapture *aliasCaptureState
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

	// strSliceHeaders maps a string literal value to the symbol name of a
	// static 16-byte slice header {ptr -> __bstrN, len} synthesized for a
	// function-scope `&"literal"`. The header lives in read-only static
	// memory forever; `&"literal"` evaluates to a pointer to it. Emitted by
	// WriteStrSliceHeaders after all functions are compiled.
	strSliceHeaders map[string]string
}

func NewContext() *Context {
	return &Context{
		bindings:        make(map[string]ASTType),
		checker:         NewCheckerState(nil),
		prebound:        make(map[string]bool),
		addressNames:      make(map[string]bool),
		pointerHeldParams: make(map[string]bool),
		constBindings:   make(map[string]bool),
		mutCandidates:   make(map[string]position),
		mutRelied:       make(map[string]bool),
		usedCandidates:  make(map[string]position),
		used:            make(map[string]bool),
		bindingPos:      make(map[string]position),
		structs:         make(map[string]*StructDecl),
		funcs:           make(map[string]*FuncDecl),
		imports:         make(map[string]map[string]*FuncDecl),
		importedVars:    make(map[string]map[string]ASTType),
		typeAliases:     make(map[string]ASTType),
		interfaceDecls:  make(map[string]*InterfaceDecl),
		valuesDecls:     make(map[string]*ValuesDecl),
		typeMethods:     make(map[string][]*FuncDecl),
		vtables:         make(map[string]vtableSpec),
		strngs:          make(map[string]string),
		strSliceHeaders: make(map[string]string),
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
		// A plain tag/projection values type is register-resident (private
		// i64 tag). A payload-bearing type is a tagged union of structs and
		// is memory-backed like any aggregate.
		if vd, ok := c.ValuesDeclForName(t.Name); ok {
			return vd.IsPayloadBearing()
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

// NameHoldsPointer reports whether name is a memory-backed value passed to the
// current function BY ADDRESS -- the slot named `name` holds a pointer to the
// caller's value, not the value itself.
//
// Such a name reports NameIsAddress==true just like a memory-backed local, but
// the two need opposite handling when the value's address is wanted: a local's
// name IS the storage, so its address is `lea name`; one of these HOLDS the
// address, so its address is the value it holds, `mov name`. Everything that
// reaches for a value's address must consult this to choose correctly (see
// valueAddress in compile.go). Marked at parameter binding by
// compileFunctionBody; searched up the scope chain so nested blocks see it.
func (c *Context) NameHoldsPointer(name string) bool {
	for cur := c; cur != nil; cur = cur.parent {
		if cur.pointerHeldParams[name] {
			return true
		}
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
	if err := checkInterfaceReceiverHomogeneity(decl); err != nil {
		panic(&interpreterError{err.Error(), p})
	}
	root.interfaceDecls[name] = decl
}

// checkInterfaceReceiverHomogeneity rejects interfaces whose methods mix
// value-receiver and pointer-receiver shapes. Every method's first parameter
// (the implicit receiver) must have the same indirection level. Methods with
// no parameters are skipped — they have no receiver to constrain. The
// homogeneous shape lets dispatch use a single uniform calling convention
// (data slot → rdi) regardless of which method is being called, without the
// vtable needing to encode per-method receiver kinds.
func checkInterfaceReceiverHomogeneity(decl *InterfaceDecl) error {
	haveShape := false
	wantPointer := false
	for _, m := range decl.Methods {
		if len(m.Params) == 0 {
			continue
		}
		isPointer := m.Params[0].Type.Indirection > 0
		if !haveShape {
			wantPointer = isPointer
			haveShape = true
			continue
		}
		if wantPointer != isPointer {
			return fmt.Errorf("interface %s mixes value-receiver and pointer-receiver methods; all methods must use the same receiver shape (all self or all *self)", decl.Name)
		}
	}
	return nil
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

// SetVariantNarrow records that the value at flow-path `key` is narrowed to
// variant `vc` in this context (a switch case body).
func (c *Context) SetVariantNarrow(key string, vc *ValuesCase) {
	if c.variantNarrow == nil {
		c.variantNarrow = make(map[string]*ValuesCase)
	}
	c.variantNarrow[key] = vc
}

// VariantNarrow returns the variant the value at flow-path `key` is narrowed
// to, walking the context chain to the root.
func (c *Context) VariantNarrow(key string) (*ValuesCase, bool) {
	if c == nil {
		return nil, false
	}
	if vc, ok := c.variantNarrow[key]; ok {
		return vc, true
	}
	return c.parent.VariantNarrow(key)
}

// narrowedValuesField resolves `d` = `e.fd` when e is a values value narrowed
// to a variant in context c. It returns the field's type and its byte offset
// within the values value (tag size + payload offset). ok=false when e is not
// a narrowed values value or fd is not a field of the narrowed variant.
func narrowedValuesField(c *Context, d *Dot) (ASTType, int, bool) {
	// Check the narrowing fact first — before typing d.Val — so a values-case
	// selector (`io_error.PERMISSION_DENIED`, whose base is a type name, not a
	// variable) is never type-queried here.
	path, ok := FlowPathForExpr(d.Val)
	if !ok {
		return ASTType{}, 0, false
	}
	vc, ok := c.VariantNarrow(path.Key())
	if !ok {
		return ASTType{}, 0, false
	}
	baseType := d.Val.ASTType(c)
	vd, ok := c.ValuesDeclForName(baseType.Name)
	if !ok {
		return ASTType{}, 0, false
	}
	payload := &StructDecl{Fields: vc.Payload}
	off, ft := payload.ByteOffset(c, d.Member)
	if ft.Name == "" && ft.Element == nil && ft.FuncSig == nil && ft.AnonFields == nil {
		return ASTType{}, 0, false
	}
	return fieldTypeForBase(baseType, ft), vd.TagType.Size(c) + off, true
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

// TypeSatisfiesInterface checks structural satisfaction: does type typeName
// implement all methods of iface, with self→typeName substituted in receiver
// types? Receiver shape is taken from the interface's declared form (the
// homogeneous direction of its methods).
func (c *Context) TypeSatisfiesInterface(typeName string, iface *InterfaceDecl) bool {
	// Use the interface's own homogeneous receiver direction as the source
	// shape (value-shape if value receivers, pointer-shape if *self).
	expected := recvShapeValue
	if interfaceIsPointerShape(iface) {
		expected = recvShapePtr
	}
	return c.TypeSatisfiesInterfaceAs(typeName, iface, expected)
}

// TypeSatisfiesInterfaceAs checks structural satisfaction against the
// source-derived receiver capability expectedRecv (one of the recvShape*
// classes: value, *self, *mut self, or none). A method's declared receiver
// must be satisfiable from that capability via receiverSatisfies — value
// sources reach value receivers, `*T` sources reach `*self` receivers, and
// `*mut T` sources reach `*self` or `*mut self` (the legal `*mut -> *`
// weakening; the unsound `* -> *mut` direction is rejected). Non-receiver
// args and return types must match the interface declaration exactly.
//
// This is the predicate used by the coercion path. The interface's own
// declared receiver shape becomes documentation rather than a hard
// constraint, letting (for example) a value-receiver
// `io_err.message(e io_err)` satisfy a pointer-shape `error.message(e *self)`
// interface when coerced from a value source.
func (c *Context) TypeSatisfiesInterfaceAs(typeName string, iface *InterfaceDecl, expectedRecv int) bool {
	// The empty interface (`any`) has no required methods, so every concrete
	// type satisfies it trivially — including primitives with no method set.
	if iface == nil || len(iface.Methods) == 0 {
		return true
	}
	// A source shape with no compatible receiver (e.g. **T, *byte[]) can only
	// satisfy a zero-method interface, handled above.
	if expectedRecv == recvShapeNone {
		return false
	}
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
			// Receiver-shape axis: the method's declared receiver shape must
			// be satisfiable from the source-derived expected capability. The
			// interface's own declared receiver marker is not consulted —
			// Boson's rule is that the source direction (and its mut
			// capability) drives receiver selection, with the legal
			// `*mut -> *` weakening but not the unsound `* -> *mut` direction.
			if !receiverSatisfies(receiverShapeOf(m.Args[0].Type), expectedRecv) {
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
			// Borrow-contract axis: the concrete method's inferred return
			// borrows must be ⊆ the interface method's declared `from(...)`
			// contract (a non-declaring interface = borrows nothing). A
			// borrowing method cannot satisfy an interface that doesn't admit
			// the borrow — otherwise dispatch would lose the lifetime bound.
			if !methodAliasesSatisfy(c, m, isig.ReturnAliases) {
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

// interfaceIsPointerShape reports the homogeneous receiver direction of an
// interface declaration. Homogeneity is enforced at declaration time
// (checkInterfaceReceiverHomogeneity), so we only need to look at the first
// method that has a receiver. Interfaces whose methods all lack receivers
// are reported as value-shape, but such interfaces can't be satisfied today
// because the satisfaction check skips zero-param methods.
func interfaceIsPointerShape(iface *InterfaceDecl) bool {
	for _, m := range iface.Methods {
		if len(m.Params) > 0 {
			return m.Params[0].Type.Indirection > 0
		}
	}
	return false
}

// NeedVtable registers a vtable to be emitted; no-op if already registered.
func (c *Context) NeedVtable(name, typeName, ifaceName, pkgName string, iface *InterfaceDecl, shapeWord uint64) {
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
		shapeWord: shapeWord,
	}
}

// WriteVtables emits all registered vtable globals as `var` blocks to of.
//
// Layout: slot 0 = relocation to the base type's __typedesc; slot 1 = the
// canonical shape word (written as raw bytes); slots 2..N+1 = relocations to
// the type's methods in the interface's declaration order.
func (c *Context) WriteVtables(of io.Writer) {
	root := c
	for root.parent != nil {
		root = root.parent
	}
	for name, spec := range root.vtables {
		n := len(spec.methods) + 2
		raw := make([]byte, n*8)
		// slot 1: shape word, little-endian.
		for i := 0; i < 8; i++ {
			raw[8+i] = byte(spec.shapeWord >> uint(8*i))
		}
		fmt.Fprintf(of, "var %s byte[%d] {\n", name, n*8)
		fmt.Fprintf(of, "\tbytes \"%s\"\n", bytesToBasStringLiteral(raw))
		fmt.Fprintf(of, "\treloc 0 %s.__typedesc_%s 0\n", spec.pkgName, spec.typeName)
		for i, m := range spec.methods {
			fmt.Fprintf(of, "\treloc %d %s.%s.%s 0\n", (i+2)*8, spec.pkgName, spec.typeName, m)
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
	case "uintptr":
		// Unsigned, pointer-width. Any pointer converts to it with an ordinary
		// cast; the reverse needs unsafe_cast. Safe on its own precisely
		// because a uintptr cannot be dereferenced -- address arithmetic can
		// produce a nonsensical value but not an unsound operation.
		return ASTType{Name: "uintptr"}, true
	case "f32":
		return ASTType{Name: "f32"}, true
	case "f64":
		return ASTType{Name: "f64"}, true
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
	// Slice/array-type expressions in cast position: `[]byte(e)` arrives here
	// as the name `"[]byte"` (the parser's prefix cast). Any bracketed name is
	// a full prefix type string — resolve it through the one type parser so
	// projection casts land on a known slice/array type the cast path matches.
	if strings.ContainsAny(name, "[]") {
		if at, err := parseTypeString(name); err == nil {
			return at, true
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

// isIntegerType reports whether t is a concrete integer type — i8/i16/i32/i64,
// u8/u16/u32/u64, byte, or an alias whose underlying is one of those. bool,
// pointers, slices, arrays, structs, and function pointers are not integers.
// The <intlit> pending-literal type is handled separately by callers.
func isIntegerType(c *Context, t ASTType) bool {
	if t.Indirection > 0 || t.IsSliceOrArray() || t.FuncSig != nil || t.AnonFields != nil {
		return false
	}
	switch c.ResolveUnderlying(t).Name {
	case "i8", "i16", "i32", "i64", "u8", "u16", "u32", "u64", "byte", "uintptr":
		return true
	}
	return false
}

// isFloatType reports whether t is a concrete floating-point type. Kept
// separate from isIntegerType rather than folded into it: the bitwise and
// shift operators are gated on isIntegerType and must keep rejecting floats.
// Only the operators that are genuinely defined on both classes consult
// isNumericType.
func isFloatType(c *Context, t ASTType) bool {
	if t.Indirection > 0 || t.IsSliceOrArray() || t.FuncSig != nil || t.AnonFields != nil {
		return false
	}
	switch c.ResolveUnderlying(t).Name {
	case "f32", "f64":
		return true
	}
	return false
}

// isNumericType reports whether arithmetic (+ - * /) is defined on t. That is
// the integers plus the floats; it deliberately does NOT cover %, &, |, ^ or
// the shifts, which remain integer-only.
func isNumericType(c *Context, t ASTType) bool {
	return isIntegerType(c, t) || isFloatType(c, t)
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
		// A nullable pointer (`*?`) or nullable interface (`?T`) narrowed to
		// non-null by flow reads as its non-nullable form.
		if t.NilMask&1 != 0 && c.NullFact(VarFlowPath(name)) == NullKnownNonNull &&
			(t.Indirection > 0 || c.IsInterfaceType(t)) {
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
		if !fn.IsPub {
			continue
		}
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
			// Attach the inferred return-parameter aliasing fact so a
			// cross-package alias_set() lookup short-circuits to it. The
			// indices are pure positional param/slot numbers, carrying no
			// type names, so the qualify loop above does not touch them;
			// the assignment is qualification-invariant. AliasesComputed=true
			// marks the imported decl as already-inferred (no body walk).
			t.ReturnAliases = fn.ReturnAliases
			t.AliasesComputed = true
			// Thread the .bo's source position onto the rebuilt decl so the
			// interface-coercion diagnostic can name "defined at file:line"
			// for imported borrowing-method types. parseFuncType sets no
			// position; degrade gracefully to package-only when absent.
			if fn.SrcFile != "" {
				t.p = position{fname: fn.SrcFile, lineoff: uint(fn.SrcLine)}
			}
			c.DefineImportedFunc(o.Pkgname, fn.Name, &t)
		}
	}
	for _, sh := range o.Structs {
		if !sh.IsPub {
			continue
		}
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
		// Reconstruct method FuncDecls from the already-imported function
		// table so cross-package method resolution (MethodForType) works on
		// imported structs, mirroring the type-alias path below.
		if len(sh.MethodNames) > 0 {
			pkgFuncs := c.imports[o.Pkgname]
			fds := make([]*FuncDecl, 0, len(sh.MethodNames))
			for _, mn := range sh.MethodNames {
				key := sh.Name + "." + mn
				if fd, ok := pkgFuncs[key]; ok {
					clone := *fd
					clone.Name = mn
					fds = append(fds, &clone)
				}
			}
			c.DefineTypeMethods(qname, fds)
		}
	}
	for _, ta := range o.TypeAliases {
		if !ta.IsPub {
			continue
		}
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
		if !v.IsPub {
			continue
		}
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
		if !ifc.IsPub {
			continue
		}
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
			// Declared borrow contract travels verbatim — parameter indices
			// are package-independent (receiver = 0, params in order).
			sig.ReturnAliases = m.ReturnAliases
			decl.Methods = append(decl.Methods, sig)
		}
		c.DefineInterface(position{}, qname, decl)
	}
	for _, vs := range o.Values {
		if !vs.IsPub {
			continue
		}
		qname := o.Pkgname + "." + vs.Name
		tagType, err := parseTypeString(vs.TagType)
		if err != nil {
			return fmt.Errorf("import %q: values %s tag: %v", importKey, vs.Name, err)
		}
		decl := &ValuesDecl{Name: qname, TagType: tagType}
		for _, vc := range vs.Cases {
			nc := ValuesCase{Name: vc.Name, Tag: vc.Tag}
			for _, pf := range vc.Payload {
				ft, err := parseTypeString(pf.Type)
				if err != nil {
					return fmt.Errorf("import %q: values %s case %s field %s: %v",
						importKey, vs.Name, vc.Name, pf.Name, err)
				}
				qualifyImportedTypeFull(&ft, o.Pkgname, o.Structs, o.TypeAliases, o.Interfaces, o.Values)
				nc.Payload = append(nc.Payload, Binding{Name: pf.Name, Type: ft})
			}
			decl.Cases = append(decl.Cases, nc)
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

// StringSliceHeader returns the symbol name of a static 16-byte slice
// header for the string literal s, registering one (and the backing string
// constant) on first request. The header is `{ptr -> bytes, len(s)}`; its
// address is a stable, read-only, never-dies pointer to a byte slice —
// exactly what function-scope `&"literal"` needs. Identical literals share
// one header.
func (c *Context) StringSliceHeader(s string) string {
	if c.parent != nil {
		return c.parent.StringSliceHeader(s)
	}
	if r, ok := c.strSliceHeaders[s]; ok {
		return r
	}
	// Ensure the backing byte constant exists.
	c.String(s)
	r := fmt.Sprintf("__bstrslice%d", len(c.strSliceHeaders))
	c.strSliceHeaders[s] = r
	return r
}

// WriteStrSliceHeaders emits the static slice headers registered by
// StringSliceHeader. Each is a 16-byte payload — 8 bytes relocated to the
// string constant, then the little-endian length — emitted via the same
// block-form `var` directive the file-scope `&literal` anon-globals use.
// The storage is statically initialized and never written at runtime, so it
// is effectively read-only; using `var` keeps it on the existing
// reloc-bearing emission path (the `data` directive has no block form). Must
// run after WriteStrings so the referenced __bstrN constants are declared.
func (c *Context) WriteStrSliceHeaders(of io.Writer) {
	for k, sym := range c.strSliceHeaders {
		strSym := c.String(k)
		payload := make([]byte, 16)
		binary.LittleEndian.PutUint64(payload[8:], uint64(len(k)))
		emitVarBlock(of, sym, byteSliceASTType().String(), payload, []relocSpec{{Offset: 0, Symbol: strSym, Addend: 0}}, false)
	}
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

// MAX_ALIGN is the strictest alignment any Boson type requires. Nothing in the
// language is wider than 8 bytes at scalar level -- slices and interfaces are
// pairs of 8-byte words, not 16-byte units -- so 8 bounds every alignment
// computation. Code that needs a conservatively-aligned base address can use
// this rather than deriving an alignment from a type it does not have.
const MAX_ALIGN = 8

// alignUp rounds n up to the next multiple of a, which must be a power of two.
func alignUp(n, a int) int {
	if a <= 1 {
		return n
	}
	return (n + a - 1) &^ (a - 1)
}

// Alignment returns the type's natural alignment in bytes: the address
// granularity at which a value of this type must be placed.
//
// See DESIGN.md § The Boson ABI (BABI-0) / Alignment. Scalars align to their
// own width, aggregates to the strictest alignment among their members.
func (t *ASTType) Alignment(c *Context) int {
	if t.FuncSig != nil || t.Indirection > 0 {
		return PTR_SIZE
	}
	// A slice header is {ptr, len} -- two 8-byte words, so 8-aligned.
	if t.IsSlice() {
		return PTR_SIZE
	}
	// An array aligns like its element; the element count is irrelevant.
	if t.IsArray() {
		return t.Element.Alignment(c)
	}
	switch t.Name {
	case "<intlit>":
		panic("Alignment() called on <intlit> type — this is a compiler bug")
	case "<floatlit>":
		panic("Alignment() called on <floatlit> type — this is a compiler bug")
	case "i64", "u64", "f64", "uintptr":
		return 8
	case "i32", "u32", "f32":
		return 4
	case "i16", "u16":
		return 2
	case "i8", "u8", "byte", "bool":
		return 1
	}
	if underlying, ok := c.TypeAliasFor(t.Name); ok {
		return underlying.Alignment(c)
	}
	if c != nil {
		// An interface value is {data, vtable} -- two 8-byte words.
		if _, ok := c.InterfaceForName(t.Name); ok {
			return PTR_SIZE
		}
		// A values type leads with its i64 tag, so it is at least 8-aligned;
		// since MAX_ALIGN is 8, no payload can require more.
		if _, ok := c.ValuesDeclForName(t.Name); ok {
			return PTR_SIZE
		}
	}
	if t.AnonFields != nil {
		_, _, align := fieldLayout(c, t.AnonFields)
		return align
	}
	d, ok := c.StructDeclForName(t.Name)
	if !ok {
		panic(&interpreterError{msg: fmt.Sprintf("No such type %q", t.Name), p: t.p})
	}
	return d.Alignment(c)
}

// fieldLayout computes the natural-alignment layout of a field list: each
// field's byte offset, the aggregate's total size, and its alignment.
//
// Fields keep declaration order and are never reordered -- only padded. Each
// field starts at the next offset satisfying its own alignment, and the total
// is rounded up to the aggregate's alignment so that consecutive elements of
// an array of this type stay aligned.
//
// This is the single place the layout rule lives. StructDecl.Size,
// StructDecl.Alignment, StructDecl.ByteOffset, and the anonymous-struct arm of
// ASTType.Size all route through it, so they cannot disagree.
func fieldLayout(c *Context, fields []Binding) (offsets []int, size int, align int) {
	offsets = make([]int, len(fields))
	off := 0
	align = 1
	for i := range fields {
		a := fields[i].Type.Alignment(c)
		if a > align {
			align = a
		}
		off = alignUp(off, a)
		offsets[i] = off
		off += fields[i].Type.Size(c)
	}
	return offsets, alignUp(off, align), align
}

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
	case "<floatlit>":
		panic("Size() called on <floatlit> type — this is a compiler bug")
	case "i64", "u64", "f64", "uintptr":
		return 8
	case "i32", "u32", "f32":
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
		// Values type: a plain tag/projection type is just the private tag
		// (i64 in v1). A payload-bearing type is a tagged union of structs:
		// the tag followed by enough bytes for the largest variant payload.
		if vd, ok := c.ValuesDeclForName(t.Name); ok {
			return vd.TagType.Size(c) + vd.PayloadMaxSize(c)
		}
	}
	// Anonymous struct: same natural-alignment layout as a named one.
	if t.AnonFields != nil {
		_, size, _ := fieldLayout(c, t.AnonFields)
		return size
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
	if c != nil && c.IsInterfaceType(t) {
		// A non-nullable interface's zero value is a nil fat pointer (null
		// vtable); dispatching through it derefs null. Like a non-nullable
		// pointer, it requires an initializer. A *nullable* interface (`?T`)
		// zero-initializes to null, mirroring `*?T`.
		return t.NilMask&1 != 0
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
	if src.Same(floatlitASTType()) {
		// Only a float destination. Unlike <intlit>, a float literal has no
		// meaning in an integer slot.
		return dst.Indirection == 0 && !dst.IsSliceOrArray() &&
			(dst.Name == "f32" || dst.Name == "f64")
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
	if src.Same(floatlitASTType()) {
		// Like <intlit>, a float literal takes the destination's type rather
		// than staying pending — callers use the effective type to size the
		// value, and <floatlit> has no size.
		if dst.Accepts(src) {
			return dst, coerceOK
		}
		return ASTType{}, coerceTypeMismatch
	}
	if src.Name == "<nil>" {
		if dst.Indirection > 0 && dst.NilMask&1 != 0 {
			return dst, coerceOK
		}
		// A nullable interface (`?I`, Indirection 0) also accepts nil: it is
		// the interface's null value (a zeroed fat pointer). Non-nullable
		// interfaces do not.
		if dst.NilMask&1 != 0 && c != nil && c.IsInterfaceType(dst) {
			return dst, coerceOK
		}
		return ASTType{}, coerceTypeMismatch
	}
	if dst.Accepts(src) {
		return src, coerceOK
	}
	// Array literal: elements coerce element-wise (e.g. <intlit>[N] -> i64[N]).
	// The typed `:=` decl path validates each element against the destination
	// element type directly; routing the whole-array case through here lets
	// `=` assign and struct-field init accept the same literals instead of
	// rejecting `<intlit>[N]` as a different type.
	if dst.IsArray() && src.IsArray() && dst.ArraySize == src.ArraySize &&
		dst.Element != nil && src.Element != nil {
		if _, r := coerceType(c, *dst.Element, *src.Element); r == coerceOK {
			return dst, coerceOK
		}
	}
	return ASTType{}, coerceTypeMismatch
}

// String renders the type in PREFIX form (DESIGN_prefix_type_syntax.md) — the
// exact inverse of parseTypeName. It peels the outermost constructor and
// recurses, inverting ptrWrap / sliceWrap / arrayWrap: a pointer shifts the
// qualifier masks down one level, a slice un-bumps its element's quals from
// bit 1, an array reads them at the shared bit 0. This is what gets written into
// `.bo` type strings, so it must round-trip through the parser byte-for-byte.
func (t ASTType) String() string {
	// The top level is a binding level, not a reference operand: a leading
	// bit-0 `mut` there is the binding's mutability (spelled `var`), which the
	// parser rejects in a type string, so it must not be emitted. Inside a
	// pointer/slice operand it IS part of the type (`*mut T`, `[]mut byte`) and
	// stringRef renders it.
	return t.stringRef(false)
}

// stringRef renders the type in prefix form. refOperand mirrors the parser: it
// is true when this level is the operand of a `*` or `[]` (the only place a
// leading `mut` is legal). owned/? are legal at any level and always render.
func (t ASTType) stringRef(refOperand bool) string {
	var sb strings.Builder
	// Leading (bit-0) qualifiers for THIS level. For a pointer these are the
	// pointer's own owned/nullable/mut and precede the '*' (`owned ?*T`); for a
	// base they precede the name (`?error`, `owned T`).
	if t.OwnedMask&1 != 0 {
		sb.WriteString("owned ")
	}
	if refOperand && t.MutMask&1 != 0 {
		sb.WriteString("mut ")
	}
	if t.NilMask&1 != 0 {
		sb.WriteString("?")
	}

	// Pointer: emit one '*' and recurse on the pointee (masks shifted down one).
	// The pointee is a reference operand.
	if t.Indirection > 0 {
		sb.WriteString("*")
		inner := t
		inner.OwnedMask >>= 1
		inner.MutMask >>= 1
		inner.NilMask >>= 1
		inner.Indirection--
		sb.WriteString(inner.stringRef(true))
		return sb.String()
	}

	// Slice / array: emit the prefix bracket and recurse on the element. The
	// element's own qualifier bits were moved onto this level by the wrap —
	// bumped to bit 1 for a slice, kept at bit 0 (shared) for an array — so an
	// array's element renders bare while a slice's element gets its quals back.
	// A slice element is a reference operand (`[]mut T` is legal); an array
	// element is not (`[N]mut T` is meaningless).
	if t.Element != nil {
		if t.IsArray() {
			sb.WriteString(fmt.Sprintf("[%d]", t.ArraySize))
			// The array shares its owned/mut with the element (no shift); nil
			// stayed on the element. Restore owned/mut so the element renders
			// them (e.g. `[3]owned *i64`, `[3]?*i64`).
			elem := *t.Element
			elem.OwnedMask |= t.OwnedMask
			elem.MutMask |= t.MutMask
			sb.WriteString(elem.stringRef(false))
		} else {
			sb.WriteString("[]")
			elem := *t.Element
			elem.OwnedMask |= (t.OwnedMask >> 1) & 1
			elem.MutMask |= (t.MutMask >> 1) & 1
			sb.WriteString(elem.stringRef(true))
		}
		return sb.String()
	}

	if t.FuncSig != nil {
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
	// Parenthesized type group sentinel: parseTypeName already assembled
	// the full ASTType (inner type wrapped in outer pointer levels) and
	// stashed it in n.prebuilt. Return it verbatim.
	if n.sval == "<prebuilt>" {
		if n.prebuilt == nil {
			ParseErrorF(n, "internal: <prebuilt> type node missing its payload")
		}
		return *n.prebuilt
	}
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

// <floatlit> is the pending type of a floating-point literal, the float
// analogue of <intlit>. It differs in one important way: <intlit> is accepted
// by any destination, because an integer literal is meaningful as a float too.
// <floatlit> is accepted only by a floating-point destination — 3.5 is not an
// i64, and silently truncating it is exactly the behavior this work removed
// from the lexer.
func floatlitASTType() ASTType {
	return ASTType{Name: "<floatlit>"}
}

// isPendingLit reports whether t is one of the pending literal types, whose
// concrete type is supplied by context rather than by the literal itself.
func isPendingLit(t ASTType) bool {
	return t.Same(intlitASTType()) || t.Same(floatlitASTType())
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
	Name     string
	Type     ASTType
	IsConst  bool // false = var (rebindable); always false for struct fields
	Variadic bool // true for a `...T` variadic parameter (Type is then T[])
	p        position
}

type StructDecl struct {
	TName  string
	IsPub  bool
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
	_, size, _ := fieldLayout(c, s.Fields)
	return size
}

// Alignment returns the struct's natural alignment: the strictest alignment
// of any field.
func (s *StructDecl) Alignment(c *Context) int {
	_, _, align := fieldLayout(c, s.Fields)
	return align
}

func (s *StructDecl) ByteOffset(c *Context, field string) (int, ASTType) {
	offsets, _, _ := fieldLayout(c, s.Fields)
	for i, f := range s.Fields {
		if f.Name == field {
			return offsets[i], f.Type
		}
	}
	return 0, ASTType{}
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
	IsPub   bool
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
//
//	var a T1, const b T2 = expr
//
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

// MultiAssign is multi-value re-assignment to pre-declared lvalues:
//
//	a, b = expr
//
// Targets are arbitrary lvalue expressions; Init must have a MultiReturn type.
type MultiAssign struct {
	Targets []AST
	Init    AST
	p       position
}

func (*MultiAssign) ASTType(*Context) ASTType { return voidASTType() }
func (m *MultiAssign) Note() string           { return "multiassign" }
func (m *MultiAssign) Pos() position          { return m.p }

type FuncDecl struct {
	Name     string
	IsPub    bool
	Args     []Binding
	Return   ASTType
	Body     *Block
	Variadic bool // true when the last parameter is variadic (`...T`)
	p        position

	// ReturnAliases[slot] = sorted parameter indices the return slot may
	// alias (inferred return-parameter aliasing). nil/empty for ordinary
	// functions. Populated by alias_set() (demand-driven inference) for
	// locally-defined functions, or attached by the importer from the .bo
	// for cross-package callees. AliasesComputed gates the memo: false
	// means the inference has not yet run on this (local) FuncDecl; the
	// importer sets it true so cross-package lookups short-circuit to the
	// deserialized fact without a body walk.
	ReturnAliases   [][]int
	AliasesComputed bool
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
//     method on a named variable `v.method(args)`,
//     or static method `Type.method(args)`.
//     The call site asks the resolver which meaning applies.
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

// isBareTypeNameExpr reports whether an expression is really a bare TYPE name
// (a scalar/struct/alias name, `pkg.Type`, or a `T[N]`-shaped index of one).
// Used ONLY to give `new(T)` a directed "use alloc(T)" error — it is a name
// lookup, not a type parser. Types are parsed in exactly one place now
// (parseTypeName); `alloc` carries its parsed type on the Alloc node.
func isBareTypeNameExpr(c *Context, a AST) bool {
	switch v := a.(type) {
	case *Symbol:
		if _, ok := c.TypeByName(v.Name); ok {
			return true
		}
		_, ok := c.StructDeclForName(v.Name)
		return ok
	case *Dot:
		if pkg, ok := v.Val.(*Symbol); ok {
			name := pkg.Name + "." + v.Member
			if _, ok := c.TypeByName(name); ok {
				return true
			}
			_, ok := c.StructDeclForName(name)
			return ok
		}
	case *Index:
		if v.NAST == nil && v.N > 0 {
			return isBareTypeNameExpr(c, v.Val)
		}
	}
	return false
}

func (f *Funcall) ASTType(c *Context) ASTType {
	pkg, name := f.PkgAndName()
	if pkg == "" && name == "new" {
		if len(f.Args) != 1 {
			CompileErrorF(f, "new(expr) requires exactly one argument")
		}
		if isBareTypeNameExpr(c, f.Args[0]) {
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
		// Interface receiver: resolve the method in the interface declaration
		// and return its declared return type (dispatch is virtual).
		if c.IsInterfaceType(rt.StripOwned()) {
			if decl, ok := c.InterfaceForName(rt.StripOwned().Name); ok {
				for _, m := range decl.Methods {
					if m.Name == name {
						return m.Return
					}
				}
			}
		}
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
	// Field access on a values value narrowed to a variant (`e.fd` inside a
	// matched switch case) resolves against the variant's payload struct.
	if c != nil {
		if ft, _, ok := narrowedValuesField(c, d); ok {
			return ft
		}
	}
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
		if _, isValues := c.ValuesDeclForName(t.Name); isValues {
			CompileErrorF(d, "cannot access field %q of values type %s here; match it in a switch case first (a variant's fields are only in scope inside its `case`)", d.Member, t.Name)
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
		t := a.Lit.ASTType(c)
		// A projection to a *value* field/element — `&w.inner`, `&arr[i]` where
		// the field/element is itself a value (Indirection 0) — addresses an
		// lvalue into existing storage, so it follows `&x`'s rule: the result is
		// `*mut T` when the projection lvalue is writable (the container is
		// mutable). The field's own type has no pointer-level bits to disturb,
		// so we set only the new outer write-through bit. Pointer/owned/nullable
		// fields keep their existing per-level bits untouched (their own mut
		// governs writing *through* them; shifting would corrupt owned/nullable
		// projections), and address-of-literal (`&SomeStruct{...}`) is fresh
		// storage. The owned-slot gate (checkAddressOfOwnedForDest) still
		// rejects `*mut` views of an owner slot.
		switch a.Lit.(type) {
		case *Dot, *Index:
			if t.Indirection == 0 {
				if writable, _ := lvalueIsWritable(c, a.Lit); writable {
					t.MutMask |= 1 << 1
				}
			}
		}
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
	// A `Type.CASE{ ... }` literal is payload-variant construction; its value
	// type is the values type. Resolved here (not at ToAST) because the ToAST
	// context can't see the values decl.
	if c != nil {
		if vd, _, ok := valuesCaseFromName(c, s.Type.Name); ok {
			return ASTType{Name: vd.Name}
		}
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

// valuesCaseFromName resolves a dotted name like "file_info.FILE_INFO" to its
// values declaration and case. Returns ok=false when the name does not name a
// case of a values type in scope.
func valuesCaseFromName(c *Context, name string) (*ValuesDecl, *ValuesCase, bool) {
	dot := strings.LastIndex(name, ".")
	if dot < 0 {
		return nil, nil, false
	}
	typeName, caseName := name[:dot], name[dot+1:]
	vd, ok := c.ValuesDeclForName(typeName)
	if !ok {
		return nil, nil, false
	}
	for i := range vd.Cases {
		if vd.Cases[i].Name == caseName {
			return vd, &vd.Cases[i], true
		}
	}
	return nil, nil, false
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

// aggregateKindName names t when it is an aggregate that `==`/`!=` must reject
// — a struct, fixed array, or slice. Scalars and pointers return ("", false).
// Equality of an aggregate is an author decision (the memberwise default would
// silently become pointer-identity as fields evolve), so the operator is
// rejected in favor of an explicit equality function.
func aggregateKindName(c *Context, t ASTType) (string, bool) {
	if t.Indirection > 0 || t.FuncSig != nil {
		return "", false
	}
	if t.IsArray() {
		return "array", true
	}
	if t.IsSlice() {
		return "slice", true
	}
	if t.AnonFields != nil {
		return "struct", true
	}
	if _, ok := structDeclForType(c, t); ok {
		return "struct", true
	}
	return "", false
}

// opSym returns the source spelling of a binary operator node type, for
// diagnostics.
func opSym(t nodetype) string {
	switch t {
	case n_add:
		return "+"
	case n_sub:
		return "-"
	case n_mul:
		return "*"
	case n_div:
		return "/"
	case n_bitand:
		return "&"
	case n_bitor:
		return "|"
	case n_mod:
		return "%"
	case n_xor:
		return "^"
	case n_shl:
		return "<<"
	case n_shr:
		return ">>"
	}
	return "?"
}

func (o *Op2) ASTType(c *Context) ASTType {
	// TODO: this will be expanded as more types are added.
	// For now, it's only num that can have operations.
	switch o.Type {
	case n_lt, n_le, n_gt, n_ge, n_deq, n_neq, n_booland, n_boolor:
		// Comparison operators apply to scalars and pointers only; aggregates
		// (struct/array/slice) have no defined comparison — equality would
		// silently become memberwise/pointer-identity, ordering is undefined —
		// so reject both `==`/`!=` and the relational operators in favor of an
		// explicit function. (`&&`/`||` operate on bools and are not checked.)
		rejectAggregate := func(sym, verb string) {
			for _, operand := range []AST{o.First, o.Second} {
				if kind, bad := aggregateKindName(c, operand.ASTType(c)); bad {
					panic(&interpreterError{msg: fmt.Sprintf("%ss are not comparable with %s; write an explicit %s", kind, sym, verb), p: o.Pos()})
				}
			}
		}
		switch o.Type {
		case n_deq, n_neq:
			rejectAggregate("==", "equality function")
		case n_lt:
			rejectAggregate("<", "comparison function")
		case n_le:
			rejectAggregate("<=", "comparison function")
		case n_gt:
			rejectAggregate(">", "comparison function")
		case n_ge:
			rejectAggregate(">=", "comparison function")
		}
		return boolASTType()
	case n_shl, n_shr:
		// Shifts: the result takes the left operand's type; the shift count
		// may be any integer width (it is not combined with the value), so
		// no same-representation requirement between the two operands.
		ft := o.First.ASTType(c)
		st := o.Second.ASTType(c)
		for _, operand := range []ASTType{ft, st} {
			if !operand.Same(intlitASTType()) && !isIntegerType(c, operand) {
				panic(&interpreterError{
					msg: fmt.Sprintf("operator %s requires integer operands, but got %s", opSym(o.Type), operand),
					p:   o.Pos(),
				})
			}
		}
		if ft.Same(intlitASTType()) {
			// `1 << n` with an untyped left operand defaults to i64, the
			// natural width for a shift result.
			return numASTType()
		}
		return ft
	case n_add, n_sub, n_mul, n_div, n_bitand, n_bitor, n_mod, n_xor:
		ft := o.First.ASTType(c)
		st := o.Second.ASTType(c)
		// + - * / are defined on any numeric type. The bitwise operators and
		// % stay integer-only: they have no meaning on an IEEE value, and
		// folding floats into isIntegerType would have quietly admitted
		// `f & 1` and `f % 2`.
		arith := o.Type == n_add || o.Type == n_sub || o.Type == n_mul || o.Type == n_div
		// Arithmetic and bitwise operators are defined on integers only.
		// bool, pointers, slices, structs, and function pointers are
		// rejected here rather than silently treated as small integers
		// (e.g. `true + true` or `&p & 1`). The <intlit> pending type is
		// integral and allowed; it adopts the other operand's type below.
		for _, operand := range []ASTType{ft, st} {
			if operand.Same(intlitASTType()) {
				continue
			}
			if operand.Same(floatlitASTType()) {
				// A float literal is numeric but has no concrete type yet.
				// It is legal only where floats are, so the bitwise and
				// shift operators still reject it below.
				if arith {
					continue
				}
				panic(&interpreterError{
					msg: fmt.Sprintf("operator %s requires integer operands, but got a float literal", opSym(o.Type)),
					p:   o.Pos(),
				})
			}
			if arith {
				if !isNumericType(c, operand) {
					panic(&interpreterError{
						msg: fmt.Sprintf("operator %s requires numeric operands, but got %s", opSym(o.Type), operand),
						p:   o.Pos(),
					})
				}
				continue
			}
			if !isIntegerType(c, operand) {
				panic(&interpreterError{
					msg: fmt.Sprintf("operator %s requires integer operands, but got %s", opSym(o.Type), operand),
					p:   o.Pos(),
				})
			}
		}
		// A literal adopts the other operand's type. When both are literals
		// the result stays pending, and a float literal dominates an integer
		// one — `1.5 + 2` is a float expression — so the destination decides
		// the concrete type and the arithmetic happens in it.
		fPend, sPend := isPendingLit(ft), isPendingLit(st)
		if fPend && sPend {
			if ft.Same(floatlitASTType()) || st.Same(floatlitASTType()) {
				return floatlitASTType()
			}
			return intlitASTType()
		}
		if fPend {
			if ft.Same(floatlitASTType()) && !isFloatType(c, st) {
				panic(&interpreterError{
					msg: fmt.Sprintf("operator %s: a float literal cannot be combined with %s; Boson does not convert between number types implicitly", opSym(o.Type), st),
					p:   o.Pos(),
				})
			}
			return st
		}
		if sPend {
			if st.Same(floatlitASTType()) && !isFloatType(c, ft) {
				panic(&interpreterError{
					msg: fmt.Sprintf("operator %s: a float literal cannot be combined with %s; Boson does not convert between number types implicitly", opSym(o.Type), ft),
					p:   o.Pos(),
				})
			}
			return ft
		}
		// Both operands are concrete. Boson does not convert between
		// number types implicitly, so the two must match exactly —
		// `i64 + i32` is an error directing the author to cast, not a
		// silent widening (and historically a codegen panic).
		if !ft.SameRepr(st) {
			panic(&interpreterError{
				msg: fmt.Sprintf("mismatched number types in arithmetic: %s and %s; Boson does not convert between number types implicitly — add an explicit cast such as %s(x)", ft, st, ft.Name),
				p:   o.Pos(),
			})
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
	case n_mod:
		op = "%"
	case n_xor:
		op = "^"
	case n_shl:
		op = "<<"
	case n_shr:
		op = ">>"
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

// BitNot is the bitwise complement `~x`. It requires an integer operand and
// preserves its type. Codegen desugars it to (0 - x) - 1 (the two's-complement
// identity, correct for any width and signedness); the dedicated node exists so
// the type check and diagnostics name `~` and point at it.
type BitNot struct {
	Val AST
	p   position
}

func (b *BitNot) ASTType(c *Context) ASTType {
	t := b.Val.ASTType(c)
	if t.Same(intlitASTType()) {
		return t
	}
	if !isIntegerType(c, t) {
		panic(&interpreterError{msg: fmt.Sprintf("operator ~ requires an integer operand, but got %s", t), p: b.p})
	}
	return t
}

func (b *BitNot) Note() string {
	return fmt.Sprintf("~(%s)", b.Val.Note())
}

func (b *BitNot) Pos() position {
	return b.p
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
	IsPub      bool
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
	// ReturnAliases is the method's *declared* borrow contract from `from(...)`
	// clauses: ReturnAliases[slot] = sorted parameter indices that return slot
	// may borrow (receiver = index 0), matching FuncDecl.ReturnAliases on the
	// inferred side. nil = no `from` clause anywhere (borrows nothing); a
	// present-but-empty per-slot set means that slot declares no borrow.
	ReturnAliases [][]int
	p             position
}

// InterfaceDecl is the AST node for `interface Name { sig... }`.
type InterfaceDecl struct {
	Name    string
	IsPub   bool
	Methods []InterfaceMethodSig
	p       position
}

func (*InterfaceDecl) ASTType(*Context) ASTType { return voidASTType() }
func (d *InterfaceDecl) Note() string           { return fmt.Sprintf("interface %s", d.Name) }
func (d *InterfaceDecl) Pos() position          { return d.p }

// interfaceReturnSlotTypes splits an interface method's return ASTType into
// one type per return slot: the per-field types of a multi-return, else the
// single return type (empty for void).
func interfaceReturnSlotTypes(ret ASTType) []ASTType {
	if ret.MultiReturn && len(ret.AnonFields) > 0 {
		ts := make([]ASTType, len(ret.AnonFields))
		for i, f := range ret.AnonFields {
			ts[i] = f.Type
		}
		return ts
	}
	if ret.Name == "void" && ret.Indirection == 0 && ret.Element == nil &&
		ret.FuncSig == nil && ret.AnonFields == nil {
		return nil
	}
	return []ASTType{ret}
}

// typeCanBorrow reports whether a return slot of type t can be a view of
// caller storage (pointer, slice/array, struct, or interface). Plain scalar
// leaves and function pointers cannot, so a `from(...)` on such a slot is a
// declaration error. Deliberately permissive on aggregates (a struct may
// transitively hold a view); the field-level precision is a future extension.
func typeCanBorrow(c *Context, t ASTType) bool {
	if t.Indirection > 0 || t.Element != nil || t.AnonFields != nil {
		return true
	}
	if c != nil {
		if _, ok := c.InterfaceForName(t.Name); ok {
			return true
		}
		if _, ok := c.StructDeclForName(t.Name); ok {
			return true
		}
	}
	return false
}

// resolveInterfaceFrom resolves an interface method's per-slot `from(...)`
// clauses into its declared ReturnAliases: ReturnAliases[slot] = sorted,
// deduped parameter indices the slot may borrow (receiver = index 0), matching
// the inferred-side convention in FuncDecl.ReturnAliases. fromArgs is one
// n_from node per return slot (positional). Enforces the v1 restrictions:
// unknown-name, D6 value-receiver reject, no variadic source, view-capable
// slot only.
func resolveInterfaceFrom(c *Context, isig *InterfaceMethodSig, variadic []bool, fromArgs []*Node) [][]int {
	slotTypes := interfaceReturnSlotTypes(isig.Return)
	if len(fromArgs) != len(slotTypes) {
		ParseErrorF(fromArgs[0], "from(...): %d borrow clauses for %d return slots in method %s",
			len(fromArgs), len(slotTypes), isig.Name)
	}
	out := make([][]int, len(fromArgs))
	for slot, fc := range fromArgs {
		var idxs []int
		for _, nm := range fc.args {
			name := nm.sval
			idx := -1
			if name == "self" {
				idx = 0
			} else {
				for pi := range isig.Params {
					if isig.Params[pi].Name == name {
						idx = pi
						break
					}
				}
			}
			if idx < 0 {
				ParseErrorF(nm, "from(...): %q is not a parameter of method %s", name, isig.Name)
			}
			if idx == 0 && len(isig.Params) > 0 && isig.Params[0].Type.Indirection == 0 {
				ParseErrorF(nm, "from(self) is not allowed on a value receiver (method %s): a result cannot borrow a by-value copy of the receiver", isig.Name)
			}
			if idx < len(variadic) && variadic[idx] {
				ParseErrorF(nm, "from(...): cannot borrow the variadic parameter %q in method %s", name, isig.Name)
			}
			idxs = append(idxs, idx)
		}
		if len(idxs) > 0 && !typeCanBorrow(c, slotTypes[slot]) {
			ParseErrorF(fc, "from(...): return slot %d of method %s has non-borrowable type %s; only pointers, slices, structs, and interfaces can borrow", slot, isig.Name, slotTypes[slot].String())
		}
		out[slot] = sortDedup(idxs)
	}
	return out
}

// TypeWithMethodsDecl is the AST node for `type TypeName Base { methods... }`.
type TypeWithMethodsDecl struct {
	Name       string
	IsPub      bool
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
	// Payload is the variant's struct shape when it carries per-case runtime
	// data (`FILE_INFO{ fd i64, path byte[] }`). Nil/empty for a bare variant
	// (`CLOSED`) or a projection-bearing case. A payload variant is literally a
	// struct; field resolution synthesizes `&StructDecl{Fields: Payload}` and
	// the tag selects which struct (see PROPOSAL_adts_and_switch.md).
	Payload []Binding
	p       position
}

// ValuesDecl is the AST node for `type Name values [(projections)] { cases } [{ methods }]`.
// Projections names the target types (ordered) for each case's static
// initializer list; the per-case Expr slices match Projections positionally.
// Cases without projection initializers are valid only when the type has
// no projection signature (`type Name values { CASE_A; CASE_B }`).
type ValuesDecl struct {
	Name        string
	IsPub       bool
	TagType     ASTType // v1: always i64
	Projections []ASTType
	Cases       []ValuesCase
	Methods     []*FuncDecl
	p           position
}

func (*ValuesDecl) ASTType(*Context) ASTType { return voidASTType() }
func (d *ValuesDecl) Note() string           { return fmt.Sprintf("type %s values { ... }", d.Name) }
func (d *ValuesDecl) Pos() position          { return d.p }

// checkTypeFinite rejects a value type that (transitively, through value-typed
// fields — structs, anonymous structs, other values payloads, arrays — but not
// pointers or slices) contains itself by value, which would have no finite size
// and overflow Size(). Recursive types must go through a pointer. `seen` is the
// set of named value types on the current path.
func checkTypeFinite(c *Context, t ASTType, seen []string, p position) {
	if t.Indirection > 0 || t.FuncSig != nil || t.IsSlice() {
		return // pointers, function pointers, and slice headers are finite
	}
	if t.IsArray() && t.Element != nil {
		checkTypeFinite(c, *t.Element, seen, p)
		return
	}
	name := t.Name
	if name != "" {
		for _, n := range seen {
			if n == name {
				panic(&interpreterError{fmt.Sprintf("recursive value type %q must go through a pointer: it contains itself by value, which has no finite size", name), p})
			}
		}
	}
	seen2 := seen
	if name != "" {
		seen2 = append(append([]string{}, seen...), name)
	}
	if vd, ok := c.ValuesDeclForName(name); ok {
		for i := range vd.Cases {
			for _, f := range vd.Cases[i].Payload {
				checkTypeFinite(c, f.Type, seen2, p)
			}
		}
		return
	}
	if t.AnonFields != nil {
		for _, f := range t.AnonFields {
			checkTypeFinite(c, f.Type, seen2, p)
		}
		return
	}
	if sd, ok := c.StructDeclForName(name); ok {
		for _, f := range sd.Fields {
			checkTypeFinite(c, f.Type, seen2, p)
		}
	}
}

// IsPayloadBearing reports whether any case carries a struct payload (making
// the type a tagged union of structs rather than a plain tag/projection type).
func (d *ValuesDecl) IsPayloadBearing() bool {
	for i := range d.Cases {
		if len(d.Cases[i].Payload) > 0 {
			return true
		}
	}
	return false
}

// PayloadMaxSize returns the byte size of the largest variant payload (0 if
// none). The runtime layout of a payload-bearing values value is
// { tag : TagType} followed by this many payload bytes.
func (d *ValuesDecl) PayloadMaxSize(c *Context) int {
	max := 0
	for i := range d.Cases {
		if len(d.Cases[i].Payload) == 0 {
			continue
		}
		sz := (&StructDecl{Fields: d.Cases[i].Payload}).Size(c)
		if sz > max {
			max = sz
		}
	}
	return max
}

// vtableSpec records one vtable entry to be emitted.
type vtableSpec struct {
	typeName  string   // concrete type name (e.g. "myerror")
	ifaceName string   // interface name (e.g. "error")
	pkgName   string   // package that defines the type
	methods   []string // method names in interface declaration order
	shapeWord uint64   // canonical shape word for the source type (vtable slot 1)
}

// VariadicForward is a trailing `slice...` argument: the slice is forwarded
// verbatim as the callee's variadic parameter, with no re-wrapping.
type VariadicForward struct {
	Val AST
	p   position
}

func (f *VariadicForward) ASTType(c *Context) ASTType { return f.Val.ASTType(c) }
func (f *VariadicForward) Note() string               { return "<slice>..." }
func (f *VariadicForward) Pos() position              { return f.p }

// TypeAssert is a runtime type assertion `x.(T)`. Its static type is a
// two-field multiretu{T, bool}.
type TypeAssert struct {
	Val AST
	T   ASTType
	p   position
}

func (a *TypeAssert) ASTType(c *Context) ASTType {
	// An interface-target assertion yields a single *nullable* interface
	// (`?T`): the vtable is 0 on failure, and the result must be narrowed via
	// `if (k != nil)` before dispatch. A concrete-target assertion yields the
	// 2-value `(T, bool)` — a concrete value plus a success flag (a concrete
	// value has no nullable form).
	if c != nil && c.IsInterfaceType(a.T) {
		nt := a.T
		nt.NilMask |= 1
		return nt
	}
	return ASTType{
		AnonFields:  []Binding{{Name: "_0", Type: a.T}, {Name: "_1", Type: boolASTType()}},
		MultiReturn: true,
	}
}
func (a *TypeAssert) Note() string  { return fmt.Sprintf("x.(%s)", a.T) }
func (a *TypeAssert) Pos() position { return a.p }

// TypeCase is one case in a type switch.
type TypeCase struct {
	T         ASTType // the case's target type; IsDefault true when this is `default`
	IsDefault bool
	Body      *Block
	p         position
}

// TypeSwitch is `switch (v x.(type)) { case T {...} ... default {...} }`.
type TypeSwitch struct {
	BindName string // the `v` binding narrowed inside each case body
	Val      AST    // the interface value being switched on
	Cases    []*TypeCase
	p        position
}

func (s *TypeSwitch) ASTType(*Context) ASTType { return voidASTType() }
func (s *TypeSwitch) Note() string             { return "switch (... .(type)) {...}" }
func (s *TypeSwitch) Pos() position            { return s.p }

// ValuesConstruct builds a payload-variant value:
// `file_info.FILE_INFO{ fd: 3, path: "config" }`. A variant is a struct; codegen
// writes the case tag followed by the payload struct at offset TagType.Size.
type ValuesConstruct struct {
	ValuesType ASTType // the values type (e.g. file_info)
	Decl       *ValuesDecl
	Case       *ValuesCase
	Fields     []StructField
	p          position
}

func (v *ValuesConstruct) ASTType(*Context) ASTType { return v.ValuesType }
func (v *ValuesConstruct) Note() string             { return "values construct" }
func (v *ValuesConstruct) Pos() position            { return v.p }

// VariantSwitch dispatches on an ADT's tag, narrowing the scrutinee to the
// matched variant inside each case body (PROPOSAL_adts_and_switch.md §5).
type VariantSwitch struct {
	Scrutinee AST
	Cases     []*VariantCase
	Default   AST // nil if no default/`_` case
	p         position
}

// ASTType is void for a statement switch (case bodies are blocks) and the
// common case-body type for an expression switch (`y := switch e { case A e.x
// ... }`). Because a case body may reference the narrowed variant's fields, the
// first case's variant narrowing is set (and restored) around the type query;
// codegen verifies all cases agree.
func (s *VariantSwitch) ASTType(c *Context) ASTType {
	var body AST
	switch {
	case len(s.Cases) > 0:
		body = s.Cases[0].Body
	case s.Default != nil:
		return s.Default.ASTType(c)
	default:
		return voidASTType()
	}
	if c == nil {
		return body.ASTType(c)
	}
	scrutType := s.Scrutinee.ASTType(c)
	vd, ok := c.ValuesDeclForName(scrutType.Name)
	if !ok {
		return body.ASTType(c)
	}
	var vc *ValuesCase
	for i := range vd.Cases {
		if vd.Cases[i].Name == s.Cases[0].Name {
			vc = &vd.Cases[i]
			break
		}
	}
	fp, ok := FlowPathForExpr(s.Scrutinee)
	if vc == nil || !ok {
		return body.ASTType(c)
	}
	key := fp.Key()
	prev, had := c.variantNarrow[key]
	c.SetVariantNarrow(key, vc)
	t := body.ASTType(c)
	if had {
		c.variantNarrow[key] = prev
	} else {
		delete(c.variantNarrow, key)
	}
	return t
}
func (s *VariantSwitch) Note() string  { return "switch (variant) {...}" }
func (s *VariantSwitch) Pos() position { return s.p }

// VariantCase is one `case VARIANT { body }` arm of a VariantSwitch.
type VariantCase struct {
	Name string
	Body AST
	p    position
}

// PreboundSpot wraps an already-compiled value handle so it can be threaded
// back through the generic argument-setup path. Used by variadic
// call-site desugaring, which builds the args slice before setupArgs runs.
type PreboundSpot struct {
	S spot
	p position
}

func (p *PreboundSpot) ASTType(*Context) ASTType { return p.S.t }
func (p *PreboundSpot) Note() string             { return "<prebound>" }
func (p *PreboundSpot) Pos() position            { return p.p }

type Dispose struct {
	Var string
	p   position
}

func (*Dispose) ASTType(*Context) ASTType { return voidASTType() }
func (d *Dispose) Note() string           { return fmt.Sprintf("dispose(%s)", d.Var) }
func (d *Dispose) Pos() position          { return d.p }

// Alloc is the `alloc(T)` intrinsic. Its argument is a TYPE, parsed by the one
// type parser (parseTypeName) at parse time and carried here as Type — no
// reinterpretation of an expression AST. ASTType returns the heap pointer the
// intrinsic yields: `owned *mut T`.
type Alloc struct {
	Type ASTType
	p    position
}

func (a *Alloc) ASTType(c *Context) ASTType {
	t := a.Type
	t.Indirection++
	t.MutMask <<= 1
	t.OwnedMask <<= 1
	t.NilMask <<= 1
	t.OwnedMask |= 1    // the returned pointer is owned
	t.MutMask |= 1 << 1 // ...and grants write-through to the pointee
	return t
}
func (a *Alloc) Note() string  { return fmt.Sprintf("alloc(%s)", a.Type) }
func (a *Alloc) Pos() position { return a.p }

// SizeOf is `size_of(T)`: the size in bytes of T's representation, as an i64
// compile-time constant. Its value is exactly what ASTType.Size reports, so it
// tracks the natural-alignment layout rules (BABI-0 § Data representation)
// rather than restating them.
type SizeOf struct {
	Type ASTType
	p    position
}

func (*SizeOf) ASTType(*Context) ASTType { return numASTType() }
func (s *SizeOf) Note() string           { return fmt.Sprintf("size_of(%s)", s.Type) }
func (s *SizeOf) Pos() position          { return s.p }

// UnsafeCast is `unsafe_cast(T, e)`: reinterpret e -- a uintptr or a pointer
// -- as exactly T. This is the one construct in the language that can
// manufacture a pointer, and therefore the one place raw-memory unsafety
// lives.
//
// Type is the FULL result type, not a pointee: `unsafe_cast(*mut node, a)`
// yields `*mut node`. That way the mut and nullable bits are stated at the
// cast rather than fixed by convention.
//
// The cast itself has no precondition -- it is always legal and always
// succeeds, because it merely produces a value. Alignment and validity are
// properties of what is subsequently done with the result, not of its
// existence: holding a temporarily-misaligned or not-yet-valid pointer is
// legitimate and must not be prohibited.
type UnsafeCast struct {
	Type ASTType
	Val  AST
	p    position
}

func (u *UnsafeCast) ASTType(*Context) ASTType { return u.Type }
func (u *UnsafeCast) Note() string             { return fmt.Sprintf("unsafe_cast(%s, ...)", u.Type) }
func (u *UnsafeCast) Pos() position            { return u.p }

// AlignOf is `align_of(T)`: T's required alignment in bytes, as an i64
// compile-time constant.
//
// It exists rather than being derived at the call site because BABI-0's
// alignment ceiling is 8 today and is intended to rise to 16 (see TODO.md).
// Code that hardcodes an alignment is silently wrong the day that changes;
// code that asks is not.
type AlignOf struct {
	Type ASTType
	p    position
}

func (*AlignOf) ASTType(*Context) ASTType { return numASTType() }
func (a *AlignOf) Note() string           { return fmt.Sprintf("align_of(%s)", a.Type) }
func (a *AlignOf) Pos() position          { return a.p }

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
// storage: a non-`var` binding, a *mut pointee, or an element of a mut slice.
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
	// Reached whenever the operand is neither a slice nor a fixed array -- a
	// pointer, a struct, a scalar. This is ordinary user error (`p[0:n]` on a
	// `*T` is an easy thing to try), so it gets a positioned diagnostic with a
	// source snippet like every other type error, not a raw panic.
	CompileErrorF(s, "Cannot slice a value of type %s; only slices and fixed arrays support [lo:hi]", t)
	return ASTType{}
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
	case float64:
		return floatlitASTType()
	case byte:
		return byteASTType()
	case bool:
		return boolASTType()
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
	if s.Name == "_" {
		panic(&interpreterError{
			msg: "cannot read from `_`: it is a discard, not a value",
			p:   s.p,
		})
	}
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
	sd.IsPub = structNode.isPub
	sd.p = p
	// The struct's fields come from the ASTType's AnonFields — parseTypeName now
	// folds `struct { f T, ... }` straight into a <prebuilt> ASTType.
	at := mkTypename(structNode)
	for _, f := range at.AnonFields {
		sd.Fields = append(sd.Fields, Binding{Name: f.Name, Type: f.Type})
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
		v.IsPub = n.isPub
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
		// Re-assignment form: targets are lvalue expressions, not n_var specs.
		if n.ival == 1 {
			var ma MultiAssign
			ma.p = n.p
			for i := 0; i < len(n.args)-1; i++ {
				ma.Targets = append(ma.Targets, n.args[i].toASTTop(NewContext()))
			}
			ma.Init = n.args[len(n.args)-1].toASTTop(NewContext())
			return &ma
		}
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
		fn.IsPub = n.isPub
		fn.p = n.p
		nargs := int(n.ival)
		args := n.args
		for i := 0; i < nargs; i++ {
			a := args[0]
			name := a.sval
			t := mkTypename(a.args[0])
			// n_arg ival: bit 0 = var (rebindable); bit 1 = variadic (...T).
			variadic := a.ival&2 != 0
			if variadic && i != nargs-1 {
				ParseErrorF(a, "variadic parameter %s must be the last parameter", name)
			}
			fn.Args = append(fn.Args, Binding{
				Name:     name,
				Type:     t,
				IsConst:  a.ival&1 == 0,
				Variadic: variadic,
				p:        a.p,
			})
			if variadic {
				fn.Variadic = true
			}
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
			if !isStatementForm(bn) {
				ParseErrorF(bn, "expression statement has no effect; only function calls are valid as statements")
			}
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
	case n_variadic_forward:
		return &VariadicForward{Val: n.args[0].toASTTop(NewContext()), p: n.p}
	case n_typeassert:
		return &TypeAssert{
			Val: n.args[0].toASTTop(NewContext()),
			T:   mkTypename(n.args[1]),
			p:   n.p,
		}
	case n_typeswitch:
		var s TypeSwitch
		s.p = n.p
		s.BindName = n.sval
		s.Val = n.args[0].toASTTop(NewContext())
		for _, cn := range n.args[1:] {
			tc := &TypeCase{p: cn.p}
			if cn.ival == 1 {
				tc.IsDefault = true
				tc.Body = cn.args[0].toASTTop(NewContext()).(*Block)
			} else {
				tc.T = mkTypename(cn.args[0])
				tc.Body = cn.args[1].toASTTop(NewContext()).(*Block)
			}
			s.Cases = append(s.Cases, tc)
		}
		return &s
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
	case n_float:
		return &Literal{Val: n.fval, p: n.p}
	case n_bool:
		return &Literal{Val: n.ival == 1, p: n.p}
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
				// Includes a values-case name (`file_info.FILE_INFO`): the
				// empty resolution context here can't see the values type, so
				// keep the dotted name and route to payload construction at
				// compile time (StructLiteral.ASTType / compileTop).
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
	case n_variantswitch:
		scrut := n.args[0].toASTTop(NewContext())
		vs := &VariantSwitch{Scrutinee: scrut, p: n.p}
		for _, cn := range n.args[1:] {
			if cn.t != n_variantcase {
				ParseErrorF(cn, "Expected a case in variant switch, got %v", cn.t)
			}
			body := cn.args[0].toASTTop(NewContext())
			if cn.ival == 1 {
				if vs.Default != nil {
					ParseErrorF(cn, "Duplicate default case in switch")
				}
				vs.Default = body
			} else {
				vs.Cases = append(vs.Cases, &VariantCase{Name: cn.sval, Body: body, p: cn.p})
			}
		}
		if n.sval != "" {
			// Head-bind: desugar `switch x := expr { ... }` to
			// `{ x := expr; switch x { ... } }` — reuses VarDecl type
			// inference and Block scoping (which enforces that an owned x is
			// discharged before the switch's scope exits).
			vs.Scrutinee = &Symbol{Name: n.sval, p: n.p}
			decl := &VarDecl{Name: n.sval, Type: ASTType{Name: "<infer>"}, IsConst: n.ival&1 == 0, Init: scrut, p: n.p}
			return &Block{Body: []AST{decl, vs}, p: n.p}
		}
		return vs
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
		// Keep the high-level For node. Lowering it to a Loop is a *codegen*
		// concern, so compileTop does it (via lowerForToLoop); pre-lowering here
		// would discard the loop's structure during AST construction, where
		// nothing needs it flattened.
		return &For{
			Init: init,
			Cond: cond,
			Step: step,
			Body: body,
			p:    n.p,
		}
	case n_lt, n_le, n_gt, n_ge, n_deq, n_neq, n_add, n_sub, n_mul, n_div, n_booland, n_boolor, n_bitand, n_bitor, n_mod, n_xor, n_shl, n_shr:
		return &Op2{
			Type:   n.t,
			First:  n.args[0].toASTTop(NewContext()),
			Second: n.args[1].toASTTop(NewContext()),
		}
	case n_neg:
		inner := n.args[0].toASTTop(NewContext())
		// Fold negation into a float literal rather than desugaring to
		// `0 - x`. Subtraction from zero is not negation in IEEE — it turns
		// -0.0 into +0.0 — and the untyped `0` has no meaning in an SSE
		// register. Folding is also what the constant-evaluation rule
		// requires: the literal is the value, already in the destination's
		// type.
		if lit, ok := inner.(*Literal); ok {
			if f, isFloat := lit.Val.(float64); isFloat {
				return &Literal{Val: -f, p: n.p}
			}
		}
		return &Op2{
			Type:   n_sub,
			First:  &Literal{Val: uint64(0)},
			Second: inner,
		}
	case n_bitnot:
		return &BitNot{Val: n.args[0].toASTTop(NewContext()), p: n.p}
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
	case n_alloc:
		// The single arg is a type node parsed by parseTypeName; fold it into
		// an ASTType with the one type builder, exactly like any annotation.
		return &Alloc{Type: mkTypename(n.args[0]), p: n.p}
	case n_sizeof:
		return &SizeOf{Type: mkTypename(n.args[0]), p: n.p}
	case n_alignof:
		return &AlignOf{Type: mkTypename(n.args[0]), p: n.p}
	case n_unsafecast:
		t := mkTypename(n.args[0])
		if t.Indirection == 0 {
			panic(&interpreterError{fmt.Sprintf("unsafe_cast requires a pointer target type, but got %s", t), n.p})
		}
		if t.OwnedMask != 0 {
			// Owned bits carry a consumption obligation the cast cannot
			// establish. Composing with the existing ownership escape keeps
			// the two assertions separately auditable: owned(unsafe_cast(T, e)).
			panic(&interpreterError{fmt.Sprintf("unsafe_cast cannot produce an owned type (%s); wrap the result: owned(unsafe_cast(...))", t), n.p})
		}
		return &UnsafeCast{Type: t, Val: n.args[1].toASTTop(c), p: n.p}
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
		underlying := mkTypename(n.args[0])
		if underlying.AnonFields != nil {
			n.args[0].isPub = n.isPub
			sd := buildStructDecl(n.sval, n.args[0], n.p)
			c.DefineStruct(sd.TName, sd)
			return sd
		}
		// Propagate Signed from base type if it's a built-in.
		switch underlying.Name {
		case "i8", "i16", "i32", "i64":
			underlying.Signed = true
		}
		c.DefineTypeAlias(n.p, n.sval, underlying)
		return &TypeAliasDecl{Name: n.sval, IsPub: n.isPub, Underlying: underlying, p: n.p}
	case n_typedecl_with_methods:
		if mkTypename(n.args[0]).AnonFields != nil {
			n.args[0].isPub = n.isPub
			sd := buildStructDecl(n.sval, n.args[0], n.p)
			c.DefineStruct(sd.TName, sd)
			var methods []*FuncDecl
			for _, mn := range n.args[1:] {
				if mn.t != n_fn {
					ParseErrorF(mn, "Expected method definition in type block, got %v", mn.t)
				}
				var fn FuncDecl
				fn.Name = mn.sval
				fn.IsPub = n.isPub
				fn.p = mn.p
				nargs := int(mn.ival)
				margs := mn.args
				for i := 0; i < nargs; i++ {
					a := margs[0]
					fn.Args = append(fn.Args, Binding{
						Name:    a.sval,
						Type:    mkTypename(a.args[0]),
						IsConst: a.ival == 0,
						p:       a.p,
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
					IsPub:  n.isPub,
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
			fn.IsPub = n.isPub
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
				IsPub:  n.isPub,
				Args:   fn.Args,
				Return: fn.Return,
				Body:   fn.Body,
				p:      fn.p,
			})
			methods = append(methods, &fn)
		}
		c.DefineTypeMethods(n.sval, methods)
		return &TypeWithMethodsDecl{Name: n.sval, IsPub: n.isPub, Underlying: underlying, Methods: methods, p: n.p}
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
			IsPub:   n.isPub,
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
			vc := ValuesCase{
				Name: cn.sval,
				Tag:  int64(i),
				p:    cn.p,
			}
			if cn.ival == 1 {
				// Payload variant: cn.args[0] is an anonymous-struct type node.
				// A variant is literally a struct; the tag selects which.
				if nProj > 0 {
					ParseErrorF(cn,
						"Value %s carries a struct payload, which is not allowed in a projection-bearing values type %s",
						cn.sval, n.sval)
				}
				payloadType := mkTypename(cn.args[0])
				seenField := make(map[string]bool, len(payloadType.AnonFields))
				for _, f := range payloadType.AnonFields {
					if seenField[f.Name] {
						ParseErrorF(cn, "Duplicate field %s in payload of variant %s", f.Name, cn.sval)
					}
					seenField[f.Name] = true
				}
				vc.Payload = payloadType.AnonFields
			} else {
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
				for _, e := range cn.args {
					vc.Expr = append(vc.Expr, e.toASTTop(NewContext()))
				}
			}
			decl.Cases = append(decl.Cases, vc)
		}
		// Register the values type before processing methods so that
		// methods can reference the receiver type by name.
		c.DefineValuesType(n.p, n.sval, decl)
		// Reject an infinitely-sized (recursive-by-value) payload type; a
		// recursive ADT must go through a pointer (`NODE{ left *tree }`).
		if decl.IsPayloadBearing() {
			checkTypeFinite(c, ASTType{Name: n.sval}, nil, n.p)
		}
		// Methods: same lowering as type-with-methods. Methods register
		// under `Name.method` in funcs and on the type's method table.
		var methods []*FuncDecl
		for _, mn := range methodNodes {
			if mn.t != n_fn {
				ParseErrorF(mn, "Expected method definition in values block, got %v", mn.t)
			}
			var fn FuncDecl
			fn.Name = mn.sval
			fn.IsPub = n.isPub
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
				IsPub:  n.isPub,
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
		decl.IsPub = n.isPub
		decl.p = n.p
		for _, sig := range n.args {
			if sig.t != n_interface_method {
				ParseErrorF(sig, "Expected interface method signature, got %v", sig.t)
			}
			var isig InterfaceMethodSig
			isig.Name = sig.sval
			isig.p = sig.p
			nparams := int(sig.ival)
			var variadic []bool
			sargs := sig.args
			for i := 0; i < nparams; i++ {
				a := sargs[0]
				isig.Params = append(isig.Params, Binding{
					Name:    a.sval,
					Type:    mkTypename(a.args[0]),
					IsConst: true,
				})
				variadic = append(variadic, a.ival&2 != 0)
				sargs = sargs[1:]
			}
			isig.Return = mkTypename(sargs[0])
			// Trailing args after the return type are the per-slot `from(...)`
			// borrow clauses (only present when some slot declares `from`).
			if fromArgs := sargs[1:]; len(fromArgs) > 0 {
				isig.ReturnAliases = resolveInterfaceFrom(c, &isig, variadic, fromArgs)
			}
			decl.Methods = append(decl.Methods, isig)
		}
		c.DefineInterface(n.p, n.sval, &decl)
		return &decl
	}
	spew.Dump(n)
	ParseErrorF(n, "Node Type %s Fell through AST Generator.\n", n.t)
	return nil
}
