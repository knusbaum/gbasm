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

type NullState int

const (
	NullMaybe NullState = iota
	NullKnownNull
	NullKnownNonNull
)

type FlowSnapshot struct {
	Owned       map[string]bool
	Null        map[FlowPath]NullState
	OwnedFields map[FlowPath]bool
}

type FlowPath struct {
	Root   string
	Fields string
}

func VarFlowPath(name string) FlowPath {
	return FlowPath{Root: name}
}

func (p FlowPath) Key() string {
	if p.Fields == "" {
		return p.Root
	}
	return p.Root + "." + p.Fields
}

func (p FlowPath) ParentRoot() FlowPath {
	return FlowPath{Root: p.Root}
}

func (p FlowPath) Append(field string) FlowPath {
	if p.Fields == "" {
		return FlowPath{Root: p.Root, Fields: field}
	}
	return FlowPath{Root: p.Root, Fields: p.Fields + "." + field}
}

func FlowPathFromKey(key string) FlowPath {
	parts := strings.Split(key, ".")
	if len(parts) == 0 {
		return FlowPath{}
	}
	return FlowPath{Root: parts[0], Fields: strings.Join(parts[1:], ".")}
}

func FlowPathForExpr(a AST) (FlowPath, bool) {
	switch ast := a.(type) {
	case *Symbol:
		return VarFlowPath(ast.Name), true
	case *Dot:
		base, ok := FlowPathForExpr(ast.Val)
		if !ok {
			return FlowPath{}, false
		}
		return base.Append(ast.Member), true
	case *NonNullAssert:
		return FlowPathForExpr(ast.Val)
	default:
		return FlowPath{}, false
	}
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
	// flow-sensitive nullability facts for visible bindings.
	nullFacts map[string]NullState
	// flow-sensitive facts for owned fields that have been moved out.
	ownedFieldFacts map[string]bool
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
	// tracks which owned bindings have been consumed (moved or disposed).
	movedBindings map[string]bool
	// maps struct names to their declarations.
	structs map[string]*StructDecl
	// maps function names to their declarations.
	funcs map[string]*FuncDecl
	// maps imported package names to that package's function declarations.
	imports map[string]map[string]*FuncDecl
	// maps user-defined type alias names to their underlying types.
	typeAliases map[string]ASTType

	// return, continue, break label stack. Return returns from the current Context
	// by jumping to the label. Continue and break do the usual within loops.
	retlabs     []string
	rettypes    []ASTType
	contlabs    []string
	breaklabs   []string
	contStates  [][]map[string]bool
	breakStates [][]map[string]bool
	labeli      int

	// Counter for temporaries
	tempi int

	// Keeps the strings to be written as data items later.
	strngs map[string]string
}

func NewContext() *Context {
	return &Context{
		bindings:        make(map[string]ASTType),
		nullFacts:       make(map[string]NullState),
		ownedFieldFacts: make(map[string]bool),
		prebound:        make(map[string]bool),
		addressNames:    make(map[string]bool),
		constBindings:   make(map[string]bool),
		movedBindings:   make(map[string]bool),
		structs:         make(map[string]*StructDecl),
		funcs:           make(map[string]*FuncDecl),
		imports:         make(map[string]map[string]*FuncDecl),
		typeAliases:     make(map[string]ASTType),
		strngs:          make(map[string]string),
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

func (c *Context) DefineStruct(name string, s *StructDecl) {
	if es, ok := c.structs[name]; ok {
		if es != s {
			panic(fmt.Sprintf("RE-defining struct [%v]\n", name))
		}
	}
	c.structs[name] = s
}

func (c *Context) DefineFunc(name string, f *FuncDecl) {
	if ef, ok := c.funcs[name]; ok {
		if ef != f {
			panic(fmt.Sprintf("RE-defining function [%v]\n", name))
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

// Move marks an owned binding as consumed. Walks the parent chain to find the
// context that owns the binding so the state is stored in the right place.
func (c *Context) Move(name string) {
	if c == nil {
		return
	}
	if _, ok := c.bindings[name]; ok {
		c.movedBindings[name] = true
		return
	}
	c.parent.Move(name)
}

// Unmove clears the consumed flag on a var binding after re-assignment.
func (c *Context) Unmove(name string) {
	if c == nil {
		return
	}
	if _, ok := c.bindings[name]; ok {
		c.movedBindings[name] = false
		return
	}
	c.parent.Unmove(name)
}

// IsMoved reports whether an owned binding has been consumed.
func (c *Context) IsMoved(name string) bool {
	if c == nil {
		return false
	}
	if _, ok := c.bindings[name]; ok {
		return c.movedBindings[name]
	}
	return c.parent.IsMoved(name)
}

// OwnedBindingsSnapshot returns a map of name→moved-state for all owned
// bindings visible in this context and its parents. Used for branch analysis.
func (c *Context) OwnedBindingsSnapshot() map[string]bool {
	snap := make(map[string]bool)
	for ctx := c; ctx != nil; ctx = ctx.parent {
		for name, t := range ctx.bindings {
			if t.HasOwned() {
				if _, exists := snap[name]; !exists {
					snap[name] = ctx.movedBindings[name]
				}
			}
		}
	}
	return snap
}

// RestoreOwnedBindings restores move state from a snapshot, for each owned
// binding named in the snapshot that still exists in the context chain.
func (c *Context) RestoreOwnedBindings(snap map[string]bool) {
	for name, moved := range snap {
		for ctx := c; ctx != nil; ctx = ctx.parent {
			if _, ok := ctx.bindings[name]; ok {
				ctx.movedBindings[name] = moved
				break
			}
		}
	}
}

func (c *Context) NullFactsSnapshot() map[FlowPath]NullState {
	snap := make(map[FlowPath]NullState)
	for ctx := c; ctx != nil; ctx = ctx.parent {
		for key, state := range ctx.nullFacts {
			path := FlowPathFromKey(key)
			if _, exists := snap[path]; !exists {
				snap[path] = state
			}
		}
	}
	return snap
}

func (c *Context) OwnedFieldFactsSnapshot() map[FlowPath]bool {
	snap := make(map[FlowPath]bool)
	for ctx := c; ctx != nil; ctx = ctx.parent {
		for key, consumed := range ctx.ownedFieldFacts {
			path := FlowPathFromKey(key)
			if _, exists := snap[path]; !exists {
				snap[path] = consumed
			}
		}
	}
	return snap
}

func (c *Context) RestoreOwnedFieldFacts(snap map[FlowPath]bool) {
	for ctx := c; ctx != nil; ctx = ctx.parent {
		for name := range ctx.ownedFieldFacts {
			delete(ctx.ownedFieldFacts, name)
		}
	}
	for path, consumed := range snap {
		c.SetOwnedFieldConsumed(path, consumed)
	}
}

func (c *Context) RestoreNullFacts(snap map[FlowPath]NullState) {
	for ctx := c; ctx != nil; ctx = ctx.parent {
		for name := range ctx.nullFacts {
			delete(ctx.nullFacts, name)
		}
	}
	for path, state := range snap {
		c.SetNullFact(path, state)
	}
}

func (c *Context) FlowSnapshot() FlowSnapshot {
	return FlowSnapshot{
		Owned:       c.OwnedBindingsSnapshot(),
		Null:        c.NullFactsSnapshot(),
		OwnedFields: c.OwnedFieldFactsSnapshot(),
	}
}

func (c *Context) RestoreFlowSnapshot(snap FlowSnapshot) {
	c.RestoreOwnedBindings(snap.Owned)
	c.RestoreNullFacts(snap.Null)
	c.RestoreOwnedFieldFacts(snap.OwnedFields)
}

func mergeNullState(a NullState, aok bool, b NullState, bok bool) (NullState, bool) {
	if !aok {
		a = NullMaybe
	}
	if !bok {
		b = NullMaybe
	}
	if a == b && a != NullMaybe {
		return a, true
	}
	return NullMaybe, false
}

func MergeNullFacts(a, b map[FlowPath]NullState) map[FlowPath]NullState {
	out := make(map[FlowPath]NullState)
	seen := make(map[FlowPath]bool)
	for path := range a {
		seen[path] = true
	}
	for path := range b {
		seen[path] = true
	}
	for path := range seen {
		state, ok := mergeNullState(a[path], a[path] != 0, b[path], b[path] != 0)
		if ok {
			out[path] = state
		}
	}
	return out
}

func (c *Context) SetNullFact(path FlowPath, state NullState) {
	for ctx := c; ctx != nil; ctx = ctx.parent {
		if _, ok := ctx.bindings[path.Root]; ok {
			key := path.Key()
			if state == NullMaybe {
				delete(ctx.nullFacts, key)
			} else {
				ctx.nullFacts[key] = state
			}
			return
		}
	}
}

func (c *Context) SetOwnedFieldConsumed(path FlowPath, consumed bool) {
	for ctx := c; ctx != nil; ctx = ctx.parent {
		if _, ok := ctx.bindings[path.Root]; ok {
			key := path.Key()
			if consumed {
				ctx.ownedFieldFacts[key] = true
			} else {
				delete(ctx.ownedFieldFacts, key)
			}
			return
		}
	}
}

func (c *Context) OwnedFieldConsumed(path FlowPath) bool {
	for ctx := c; ctx != nil; ctx = ctx.parent {
		if consumed, ok := ctx.ownedFieldFacts[path.Key()]; ok {
			return consumed
		}
		if _, ok := ctx.bindings[path.Root]; ok {
			return false
		}
	}
	return false
}

func flowPathIsOrDescendsFrom(path, base FlowPath) bool {
	if path.Root != base.Root {
		return false
	}
	if base.Fields == "" {
		return true
	}
	return path.Fields == base.Fields || strings.HasPrefix(path.Fields, base.Fields+".")
}

func (c *Context) InvalidateFlowFacts(path FlowPath) {
	for ctx := c; ctx != nil; ctx = ctx.parent {
		if _, ok := ctx.bindings[path.Root]; !ok {
			continue
		}
		for key := range ctx.nullFacts {
			if flowPathIsOrDescendsFrom(FlowPathFromKey(key), path) {
				delete(ctx.nullFacts, key)
			}
		}
		for key := range ctx.ownedFieldFacts {
			if flowPathIsOrDescendsFrom(FlowPathFromKey(key), path) {
				delete(ctx.ownedFieldFacts, key)
			}
		}
		return
	}
}

func (c *Context) NullFact(path FlowPath) NullState {
	for ctx := c; ctx != nil; ctx = ctx.parent {
		if state, ok := ctx.nullFacts[path.Key()]; ok {
			return state
		}
		if _, ok := ctx.bindings[path.Root]; ok {
			return NullMaybe
		}
	}
	return NullMaybe
}

// DefineTypeAlias records a user-defined type alias.
func (c *Context) DefineTypeAlias(p position, name string, underlying ASTType) {
	if _, ok := c.typeAliases[name]; ok {
		panic(&interpreterError{fmt.Sprintf("Type \"%s\" already declared", name), p})
	}
	c.typeAliases[name] = underlying
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

// UnconsumedOwned returns the names of any owned bindings in this (non-parent)
// scope that have not yet been consumed. Used for scope-exit checks.
func (c *Context) UnconsumedOwned() []string {
	var out []string
	for name, t := range c.bindings {
		if t.HasOwned() && !c.movedBindings[name] {
			out = append(out, name)
		}
	}
	return out
}

// UnconsumedOwnedVisible returns unconsumed owned bindings in this context and
// its parents. Used when a non-local exit such as return leaves all scopes.
func (c *Context) UnconsumedOwnedVisible() []string {
	var out []string
	for ctx := c; ctx != nil; ctx = ctx.parent {
		out = append(out, ctx.UnconsumedOwned()...)
	}
	return out
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
	return c.parent.FuncDeclForCall(pkg, name)
}

// DefineImportedFunc registers a function from an imported package.
func (c *Context) DefineImportedFunc(pkg, name string, f *FuncDecl) {
	if c.imports[pkg] == nil {
		c.imports[pkg] = make(map[string]*FuncDecl)
	}
	c.imports[pkg][name] = f
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
	c.contStates = append(c.contStates, nil)
	return l
}

func (c *Context) PopContLabel() {
	if c.parent != nil {
		c.parent.PopContLabel()
		return
	}
	c.contlabs = c.contlabs[:len(c.contlabs)-1]
	c.contStates = c.contStates[:len(c.contStates)-1]
}

func (c *Context) Continue(a AST, of io.Writer) {
	if c.parent != nil {
		c.RecordContinue(c.OwnedBindingsSnapshot())
		c.parent.emitContinue(a, of)
		return
	}
	c.RecordContinue(c.OwnedBindingsSnapshot())
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

func (c *Context) RecordContinue(snap map[string]bool) {
	if c.parent != nil {
		c.parent.RecordContinue(snap)
		return
	}
	if len(c.contStates) == 0 {
		return
	}
	i := len(c.contStates) - 1
	c.contStates[i] = append(c.contStates[i], snap)
}

func (c *Context) ContinueStates() []map[string]bool {
	if c.parent != nil {
		return c.parent.ContinueStates()
	}
	if len(c.contStates) == 0 {
		return nil
	}
	return append([]map[string]bool(nil), c.contStates[len(c.contStates)-1]...)
}

func (c *Context) PushBreakLabel() string {
	if c.parent != nil {
		return c.parent.PushBreakLabel()
	}
	l := c.Label("break")
	c.breaklabs = append(c.breaklabs, l)
	c.breakStates = append(c.breakStates, nil)
	return l
}

func (c *Context) PopBreakLabel() {
	if c.parent != nil {
		c.parent.PopBreakLabel()
		return
	}
	c.breaklabs = c.breaklabs[:len(c.breaklabs)-1]
	c.breakStates = c.breakStates[:len(c.breakStates)-1]
}

func (c *Context) Break(of io.Writer) {
	if c.parent != nil {
		c.RecordBreak(c.OwnedBindingsSnapshot())
		c.parent.emitBreak(of)
		return
	}
	c.RecordBreak(c.OwnedBindingsSnapshot())
	c.emitBreak(of)
}

func (c *Context) emitBreak(of io.Writer) {
	if c.parent != nil {
		c.parent.emitBreak(of)
		return
	}
	fmt.Fprintf(of, "\tjmp %s\n", c.breaklabs[len(c.breaklabs)-1])
}

func (c *Context) RecordBreak(snap map[string]bool) {
	if c.parent != nil {
		c.parent.RecordBreak(snap)
		return
	}
	if len(c.breakStates) == 0 {
		return
	}
	i := len(c.breakStates) - 1
	c.breakStates[i] = append(c.breakStates[i], snap)
}

func (c *Context) BreakStates() []map[string]bool {
	if c.parent != nil {
		return c.parent.BreakStates()
	}
	if len(c.breakStates) == 0 {
		return nil
	}
	return append([]map[string]bool(nil), c.breakStates[len(c.breakStates)-1]...)
}

// Push a new return label onto the return stack
func (c *Context) PushRetlabel(t ASTType) string {
	if c.parent != nil {
		return c.parent.PushRetlabel(t)
	}
	l := c.Label("return")
	c.retlabs = append(c.retlabs, l)
	c.rettypes = append(c.rettypes, t)
	return l
}

// Pop a return label from the return stack.
func (c *Context) PopRetlabel() {
	if c.parent != nil {
		c.parent.PopRetlabel()
		return
	}
	c.retlabs = c.retlabs[:len(c.retlabs)-1]
	c.rettypes = c.rettypes[:len(c.rettypes)-1]
}

func (c *Context) ReturnType() ASTType {
	if c.parent != nil {
		return c.parent.ReturnType()
	}
	if len(c.rettypes) == 0 {
		return voidASTType()
	}
	return c.rettypes[len(c.rettypes)-1]
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
				qualifyImportedType(&t.Args[i].Type, o.Pkgname, o.Structs)
			}
			qualifyImportedType(&t.Return, o.Pkgname, o.Structs)
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
			qualifyImportedType(&ft, o.Pkgname, o.Structs)
			fields = append(fields, Binding{Name: fs.Name, Type: ft})
		}
		qname := o.Pkgname + "." + sh.Name
		c.DefineStruct(qname, &StructDecl{TName: qname, Fields: fields})
	}
	return nil
}

// qualifyImportedType walks t's element chain and, when the leaf
// name matches a struct defined in `structs` (the producer .bo's
// own struct map), rewrites it as "pkgname.LeafName". Built-in
// names (i64, byte, bool, …) and already-qualified names (those
// containing a dot, presumably from a transitive import) are left
// alone.
func qualifyImportedType(t *ASTType, pkgname string, structs map[string]*gbasm.StructShape) {
	if t.Element != nil {
		qualifyImportedType(t.Element, pkgname, structs)
		return
	}
	if _, ok := structs[t.Name]; ok {
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
	Name        string   // scalar/pointee-leaf name (for shapes 3 and 4)
	Indirection int      // pointer depth (for shapes 3 and 4)
	ArraySize   int      // element count (for shape 2); 0 otherwise
	Element     *ASTType // element type (for shapes 1 and 2); nil otherwise
	Signed      bool     // signed integer marker for scalar/pointee leaf
	MutMask     uint64   // write-through bit per level
	OwnedMask   uint64   // ownership bit per level
	NilMask     uint64   // nullable pointer bit per pointer level
	FuncSig     *FuncSig // when set: function-pointer type, pointer-sized
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
	// Structs.
	d, ok := c.StructDeclForName(t.Name)
	if !ok {
		panic(fmt.Sprintf("No such type %v. TODO: Errors", t.Name))
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
		return dst.Indirection > 0 && dst.NilMask != 0
	}
	if !dst.HasOwned() {
		src = src.StripOwned()
		return dst.MutCompatible(src) && dst.NilCompatible(src)
	}
	return dst.SameOwned(src) && dst.MutCompatible(src) && dst.NilCompatible(src)
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
			inner = s + "[]"
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
	} else {
		sb.WriteString(t.Name)
	}
	return sb.String()
}

func mkTypename(n *Node) ASTType {
	if n.t != n_typename {
		ParseErrorF(n, "Expected type name but found %v", n.t)
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
	base := ASTType{
		Name:        n.sval,
		Indirection: int(n.ival),
		MutMask:     n.mutmask,
		OwnedMask:   n.ownedmask,
		NilMask:     n.nilmask,
	}
	switch base.Name {
	case "i8", "i16", "i32", "i64":
		base.Signed = true
	}

	if len(n.args) == 0 {
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

type Funcall struct {
	Pkg   string // empty for in-package calls; package name for qualified calls
	FName string
	Args  []AST
	p     position
}

// QualifiedName returns "pkg.fname" for qualified calls, just "fname" otherwise.
func (f *Funcall) QualifiedName() string {
	if f.Pkg != "" {
		return f.Pkg + "." + f.FName
	}
	return f.FName
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
	if f.Pkg == "" && f.FName == "alloc" {
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
	if f.Pkg == "" && f.FName == "new" {
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
	if f.Pkg == "" && f.FName == "free" {
		return voidASTType()
	}
	// Cast expression: type name used as a function. Only valid for unqualified calls.
	if f.Pkg == "" {
		if t, ok := c.TypeByName(f.FName); ok {
			return t
		}
	}
	if decl, _, ok := c.FuncDeclForCall(f.Pkg, f.FName); ok {
		return decl.Return
	}
	// Fall back to an indirect call through a function-pointer
	// value:
	//   foo(...)   — bare name resolves to a fn-typed local/global
	//   d.f(...)   — when no package 'd' exists, treat 'd' as a
	//                struct-valued variable and look up field 'f'
	if f.Pkg == "" {
		if vt, ok := c.TypeForVar(f.FName); ok && vt.FuncSig != nil {
			return vt.FuncSig.Return
		}
	} else {
		if vt, ok := c.TypeForVar(f.Pkg); ok {
			if sdecl, sok := c.StructDeclForName(vt.Name); sok {
				_, mtype := sdecl.ByteOffset(c, f.FName)
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
	t := d.Val.ASTType(c)
	if t.Indirection != 0 {
		if t.NilMask&1 != 0 {
			CompileErrorF(d.Val, "Cannot access field %s through nullable pointer type %s", d.Member, t)
		}
	}
	decl, ok := c.StructDeclForName(t.Name)
	if !ok {
		CompileErrorF(d, "No such struct \"%s\"", t.Name)
	}
	for _, f := range decl.Fields {
		if f.Name == d.Member {
			ft := fieldTypeForBase(t, f.Type)
			if ft.Indirection > 0 && ft.NilMask&1 != 0 {
				if path, ok := FlowPathForExpr(d); ok && c.NullFact(path) == NullKnownNonNull {
					ft.NilMask &^= 1
				}
			}
			return ft
		}
	}
	CompileErrorF(d, "No such struct member \"%s\" in struct \"%s\"", d.Member, t.Name)
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
		panic("Cannot dereference non-pointer. TODO: Nice error reports.")
	}
	if t.NilMask&1 != 0 {
		CompileErrorF(d.Val, "Cannot dereference nullable pointer type %s", t)
	}
	t.Indirection -= 1
	t.MutMask >>= 1 // consume the outermost pointer level's mut bit
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
	if a.Lit != nil {
		// Address-of-literal: the type is *(literal's type), with no
		// existing mut/owned bits to shift since the literal is fresh
		// storage we're synthesizing.
		t := a.Lit.ASTType(c)
		t.Indirection += 1
		return t
	}
	// Function address: &someFn yields a function-pointer type
	// (fn(args) ret) — pointer-sized value carrying the signature
	// so call sites can set up the ABI correctly.
	if decl, ok := c.FuncDeclForName(a.Var); ok {
		var sig FuncSig
		for _, arg := range decl.Args {
			sig.Args = append(sig.Args, arg.Type)
		}
		sig.Return = decl.Return
		return ASTType{FuncSig: &sig}
	}
	t, ok := c.TypeForVar(a.Var)
	if !ok {
		panic("Variable is not bound. TODO: Nice error reports.")
	}
	// Adding one pointer level: shift existing mut/owned bits up by one position.
	t.MutMask <<= 1
	t.OwnedMask <<= 1
	t.NilMask <<= 1
	// &x places x at position 1 in the new type (one deref through the new pointer
	// reaches x's binding). If x is var, bit 1 = write-through to x's value.
	if !c.IsConst(a.Var) {
		t.MutMask |= 1 << 1
	}
	t.Indirection += 1
	return t
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
	case n_add, n_sub, n_mul, n_div:
		ft := o.First.ASTType(c)
		if ft.Same(intlitASTType()) {
			return o.Second.ASTType(c)
		}
		return ft
	}
	panic("Bad Operation. TODO: Nice error reports.")
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

func (n *Not) ASTType(*Context) ASTType {
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

func (s *SliceOp) ASTType(c *Context) ASTType {
	t := s.Val.ASTType(c)
	if t.IsSlice() {
		return t
	}
	if t.IsArray() {
		// Slicing an array yields a slice over the same element type.
		t.ArraySize = 0
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
	panic("Bad Literal. TODO: Nice error reports.")
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
	t, ok := c.TypeForVar(s.Name)
	if !ok {
		panic(&interpreterError{
			msg: fmt.Sprintf("Variable \"%s\" undeclared.", s.Name),
			p:   s.p,
		})
	}
	return t
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
// a new context. We only want to define globals. This also
// means the context is write-only, since we cannot rely on it
// to have complete information at any point during AST construction.
func (n *Node) toASTTop(c *Context) AST {
	switch n.t {
	case n_struct:
		var sd StructDecl
		sd.TName = n.sval
		sd.p = n.p
		for _, a := range n.args {
			if a.t != n_stfield {
				ParseErrorF(a, "Expected a struct field, but found %s", a.t)
			}
			fieldName := a.sval
			fieldType := mkTypename(a.args[0])
			sd.Fields = append(sd.Fields, Binding{
				Name: fieldName,
				Type: fieldType,
			})
		}
		c.DefineStruct(sd.TName, &sd)
		return &sd
	case n_var:
		var v VarDecl
		v.Name = n.sval
		v.Type = mkTypename(n.args[0])
		v.IsConst = n.ival != 0
		v.p = n.p
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
		return &v
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
		f.FName = n.sval
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
		// Construct a qualified Funcall. The parser produces this shape because
		// parseSubexpr handles dot then parseValue which may yield a Funcall.
		if n.args[0].t == n_symbol && n.args[1].t == n_funcall {
			fcall := n.args[1]
			var f Funcall
			f.Pkg = n.args[0].sval
			f.FName = fcall.sval
			f.p = n.p
			for _, a := range fcall.args {
				f.Args = append(f.Args, a.toASTTop(NewContext()))
			}
			return &f
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
		s.Type = ASTType{Name: n.sval}
		s.p = n.p
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
	case n_lt, n_le, n_gt, n_ge, n_deq, n_neq, n_add, n_sub, n_mul, n_div, n_booland, n_boolor:
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
		underlying := mkTypename(n.args[0])
		// Propagate Signed from base type if it's a built-in.
		switch underlying.Name {
		case "i8", "i16", "i32", "i64":
			underlying.Signed = true
		}
		c.DefineTypeAlias(n.p, n.sval, underlying)
		return &TypeAliasDecl{Name: n.sval, Underlying: underlying, p: n.p}
	}
	spew.Dump(n)
	ParseErrorF(n, "Node Type %s Fell through AST Generator.\n", n.t)
	return nil
}
