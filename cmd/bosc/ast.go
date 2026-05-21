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

	// maps variable names to their types.
	bindings map[string]ASTType
	// tracks which bindings are const (true) vs var (false).
	constBindings map[string]bool
	// tracks which owned bindings have been consumed (moved or disposed).
	movedBindings map[string]bool
	// maps struct names to their declarations.
	structs map[string]*StructDecl
	// maps function names to their declarations.
	funcs map[string]*FuncDecl
	// maps user-defined type alias names to their underlying types.
	typeAliases map[string]ASTType

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
		bindings:      make(map[string]ASTType),
		constBindings: make(map[string]bool),
		movedBindings: make(map[string]bool),
		structs:       make(map[string]*StructDecl),
		funcs:         make(map[string]*FuncDecl),
		typeAliases:   make(map[string]ASTType),
		strngs:        make(map[string]string),
	}
}

func (c *Context) SubContext() *Context {
	sc := NewContext()
	sc.parent = c
	return sc
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
	if t.Indirection > 0 || t.Slice || t.ArraySize > 0 {
		return t
	}
	if underlying, ok := c.TypeAliasFor(t.Name); ok {
		result := underlying
		result.MutMask = t.MutMask
		result.OwnedMask = t.OwnedMask
		return result
	}
	return t
}

// AugmentType propagates properties (Signed) from a type alias definition
// while keeping the alias name intact for type-distinctness checks.
func (c *Context) AugmentType(t ASTType) ASTType {
	if t.Indirection > 0 || t.Slice || t.ArraySize > 0 {
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

func (c *Context) TypeForVar(name string) (ASTType, bool) {
	if c == nil {
		return ASTType{}, false
	}
	if t, ok := c.bindings[name]; ok {
		return t, true
	}
	return c.parent.TypeForVar(name)
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
	return l
}

func (c *Context) PopContLabel() {
	if c.parent != nil {
		c.parent.PopContLabel()
		return
	}
	c.contlabs = c.contlabs[:len(c.contlabs)-1]
}

func (c *Context) Continue(a AST, of io.Writer) {
	if c.parent != nil {
		c.parent.Continue(a, of)
		return
	}
	if len(c.contlabs) == 0 {
		CompileErrorF(a, "Cannot continue, No context present.")
	}
	fmt.Fprintf(of, "\tjmp %s\n", c.contlabs[len(c.contlabs)-1])
}

func (c *Context) PushBreakLabel() string {
	if c.parent != nil {
		return c.parent.PushBreakLabel()
	}
	l := c.Label("break")
	c.breaklabs = append(c.breaklabs, l)
	return l
}

func (c *Context) PopBreakLabel() {
	if c.parent != nil {
		c.parent.PopBreakLabel()
		return
	}
	c.breaklabs = c.breaklabs[:len(c.breaklabs)-1]
}

func (c *Context) Break(of io.Writer) {
	if c.parent != nil {
		c.parent.Break(of)
		return
	}
	fmt.Fprintf(of, "\tjmp %s\n", c.breaklabs[len(c.breaklabs)-1])
}

// Push a new return label onto the return stack
func (c *Context) PushRetlabel() string {
	if c.parent != nil {
		return c.parent.PushRetlabel()
	}
	l := c.Label("return")
	c.retlabs = append(c.retlabs, l)
	return l
}

// Pop a return label from the return stack.
func (c *Context) PopRetlabel() {
	if c.parent != nil {
		c.parent.PopRetlabel()
		return
	}
	c.retlabs = c.retlabs[:len(c.retlabs)-1]
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

func (c *Context) Import(f string) error {
	o, err := gbasm.ReadOFile(f)
	if err != nil {
		return err
	}
	for _, fn := range o.Funcs {
		if fn.Type != "" {
			t, err := parseFuncType(fn.Type)
			if err != nil {
				return err
			}
			t.Name = fn.Name
			c.DefineFunc(fn.Name, &t)
		}
	}
	return nil
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
		fmt.Fprintf(of, "var %s string \"%s\\0\"\n", s, unparseString(k))
	}
}

type ASTType struct {
	Name        string
	Indirection int    // pointer level, i.e. ***int -> Indirection: 3
	ArraySize   int    // zero for non-arrays.
	Slice       bool
	Signed      bool   // true for signed integer types (i8, i16, i32, i64)
	MutMask     uint64 // bit N = write-through allowed at level N (0 = outermost pointer/slice)
	OwnedMask   uint64 // bit N = owned obligation at level N (same convention as MutMask)
}

const PTR_SIZE = 8

// The size in bytes that the type occupies in memory.
// NOTE: THIS IS NOT THE SIZE OF THE REGISTER.
// For instance, arrays and structs are held in registers
// as pointers.
func (t *ASTType) Size(c *Context) int {
	if t.Indirection > 0 {
		return PTR_SIZE
	}
	var baseSize int
	// builtin types
	switch t.Name {
	case "<intlit>":
		panic("Size() called on <intlit> type — this is a compiler bug")
	case "i64", "u64":
		baseSize = 8
	case "i32", "u32":
		baseSize = 4
	case "i16", "u16":
		baseSize = 2
	case "i8", "u8":
		baseSize = 1
	case "byte":
		baseSize = 1
	case "bool":
		baseSize = 1
	default:
		// Check user-defined type aliases.
		if underlying, ok := c.TypeAliasFor(t.Name); ok {
			return underlying.Size(c)
		}
		d, ok := c.StructDeclForName(t.Name)
		if !ok {
			panic(fmt.Sprintf("No such type %v. TODO: Errors", t.Name))
		}
		baseSize = d.Size(c)
	}
	if t.ArraySize > 0 {
		return baseSize * t.ArraySize
	}
	if t.Slice {
		// slice is struct{ptr, size}
		return 16
	}
	return baseSize
}

func (t ASTType) Same(t2 ASTType) bool {
	// Signed is not included: types with different signedness already have
	// different names (i64 vs u64, i8 vs u8, etc.), so Name alone distinguishes
	// them. Excluding Signed avoids false mismatches when aliases are created
	// without uniform Signed propagation through the AST construction phase.
	return t.Name == t2.Name &&
		t.Indirection == t2.Indirection &&
		t.ArraySize == t2.ArraySize &&
		t.Slice == t2.Slice &&
		t.MutMask == t2.MutMask &&
		t.OwnedMask == t2.OwnedMask
}

// HasOwned reports whether the type carries any ownership obligation.
func (t ASTType) HasOwned() bool { return t.OwnedMask != 0 }

// StripOwned returns a copy of the type with all owned bits cleared.
// Used when passing an owned value to a non-owned parameter (plain borrow).
func (t ASTType) StripOwned() ASTType {
	t.OwnedMask = 0
	return t
}

// MutCompatible reports whether a value of type src can be used where dst is
// expected, accounting for write-through coercion. A more-permissive reference
// (*mut T) is always acceptable where a less-permissive one (*T) is expected;
// the reverse is not allowed. All other fields must match exactly.
// MutCompatible reports whether src can be used where dst is expected,
// considering only write-through (mut) coercion. Owned bits are ignored here;
// ownership compatibility is checked separately by OwnedCompatible.
func (dst ASTType) MutCompatible(src ASTType) bool {
	if dst.Name != src.Name ||
		dst.Indirection != src.Indirection ||
		dst.ArraySize != src.ArraySize ||
		dst.Slice != src.Slice {
		return false
	}
	return dst.MutMask&src.MutMask == dst.MutMask
}

// OwnedCompatible reports whether an argument of type src can be passed to a
// parameter of type dst, accounting for ownership:
//   - If dst has no owned bits: borrow — src's owned bits are stripped and ignored.
//   - If dst has owned bits: src must carry exactly the same owned bits (move).
func (dst ASTType) OwnedCompatible(src ASTType) bool {
	if !dst.HasOwned() {
		return dst.MutCompatible(src.StripOwned())
	}
	return dst.Same(src)
}

func (t ASTType) String() string {
	var sb strings.Builder
	// Bit 0 = position 0 (before any '*'): owned qualifier on the outermost pointer/value.
	if t.OwnedMask&1 != 0 {
		sb.WriteString("owned ")
	}
	for i := 0; i < t.Indirection; i++ {
		sb.WriteRune('*')
		// Qualifiers between this '*' and the next token are at position i+1.
		if t.MutMask&(1<<uint(i+1)) != 0 {
			sb.WriteString("mut ")
		}
		if t.OwnedMask&(1<<uint(i+1)) != 0 {
			sb.WriteString("owned ")
		}
	}
	if t.Slice {
		// Slice element qualifiers are at position Indirection+1.
		if t.MutMask&(1<<uint(t.Indirection+1)) != 0 {
			sb.WriteString("mut ")
		}
		if t.OwnedMask&(1<<uint(t.Indirection+1)) != 0 {
			sb.WriteString("owned ")
		}
	}
	sb.WriteString(t.Name)
	if t.ArraySize > 0 {
		fmt.Fprintf(&sb, "[%d]", t.ArraySize)
	}
	if t.Slice {
		sb.WriteString("[]")
	}
	return sb.String()
}

func mkTypename(n *Node) ASTType {
	var t ASTType
	if n.t != n_typename {
		ParseErrorF(n, "Expected type name but found %v", n.t)
	}
	t.Name = n.sval
	t.Indirection = int(n.ival)
	t.MutMask = n.mutmask
	t.OwnedMask = n.ownedmask
	switch t.Name {
	case "i8", "i16", "i32", "i64":
		t.Signed = true
	}
	if len(n.args) > 0 {
		array := n.args[0]
		if array.t == n_index {
			t.ArraySize = int(array.ival)
		} else if array.t == n_slice {
			t.Slice = true
		} else {
			ParseErrorF(array, "Expected an array specifier, but found %v", array.t)
		}
	}
	return t
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
	return ASTType{Name: "byte", Slice: true}
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
	FName string
	Args  []AST
	p     position
}

func (f *Funcall) ASTType(c *Context) ASTType {
	// Cast expression: type name used as a function.
	if t, ok := c.TypeByName(f.FName); ok {
		return t
	}
	decl, ok := c.FuncDeclForName(f.FName)
	if !ok {
		panic(&interpreterError{
			msg: fmt.Sprintf("No such function \"%s\"", f.FName),
			p:   f.p,
		})
	}
	return decl.Return
}

func (f *Funcall) Note() string {
	return fmt.Sprintf("call %s(#%d)", f.FName, len(f.Args))
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

	}
	decl, ok := c.StructDeclForName(t.Name)
	if !ok {
		CompileErrorF(d, "No such struct \"%s\"", t.Name)
	}
	for _, f := range decl.Fields {
		if f.Name == d.Member {
			return f.Type
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
	t.Indirection -= 1
	t.MutMask >>= 1 // consume the outermost pointer level's mut bit
	// If the result is a plain non-pointer, non-slice value, MutMask is
	// meaningless (binding mutability is tracked in constBindings, not here).
	if t.Indirection == 0 && !t.Slice && t.ArraySize == 0 {
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

type Address struct {
	// for now we can only take the address of a variable.
	// TODO: extend this to arbitrary expressions.
	// This is tricky, since not every expression can yield
	// something that is addressable, so for now we'll do the
	// easy thing.
	Var string
	p   position
}

func (a *Address) ASTType(c *Context) ASTType {
	t, ok := c.TypeForVar(a.Var)
	if !ok {
		panic("Variable is not bound. TODO: Nice error reports.")
	}
	// Adding one pointer level: shift existing mut/owned bits up by one position.
	t.MutMask <<= 1
	t.OwnedMask <<= 1
	// &x places x at position 1 in the new type (one deref through the new pointer
	// reaches x's binding). If x is var, bit 1 = write-through to x's value.
	if !c.IsConst(a.Var) {
		t.MutMask |= 1 << 1
	}
	t.Indirection += 1
	return t
}

func (a *Address) Note() string {
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
		panic("(2) No such struct. TODO: Nice error reports.")
	}
	return s.Type
}

func (s *StructLiteral) Note() string {
	return fmt.Sprintf("struct literal %s", s.Type)
}

func (s *StructLiteral) Pos() position {
	return s.p
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
	return f.Init.Pos()
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
	p position
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
	// Probably shouldn't allow indexing pointers.
	// if t.Indirection > 0 {
	// 	t.Indirection -= 1
	// 	return t
	// }
	if t.Slice {
		t.Slice = false
		return t
	}
	if t.ArraySize > 0 {
		t.ArraySize = 0
		return t
	}
	panic(fmt.Sprintf("CANNOT INDEX INTO NON-ARRAY TYPE %v", t))
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
	if t.Slice {
		return t
	}
	if t.ArraySize > 0 {
		t.ArraySize = 0
		t.Slice = true
		return t
	}
	// No slicing pointers.
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
	}
	panic("Bad Literal. TODO: Nice error reports.")
}

func (l *Literal) Note() string {
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
		var d Dot
		d.Val = n.args[0].toASTTop(NewContext())
		if n.args[1].t != n_symbol {
			ParseErrorF(n.args[1], "Expected a member name, but got %v", n.args[1].t)
		}
		d.Member = n.args[1].sval
		return &d
	case n_deref:
		return &Deref{Val: n.args[0].toASTTop(NewContext())}
	case n_symbol:
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
		return &Address{
			Var: n.sval,
			p:   n.p,
		}
	case n_index:
		if n.args[0].t == n_number {
			// special optimization
			return &Index{
				Val: &Symbol{Name: n.sval, p: n.p},
				N:   n.args[0].ival,
			}
		} else {
			idx := n.args[0].toASTTop(NewContext())
			return &Index{
				Val:  &Symbol{Name: n.sval, p: n.p},
				NAST: idx,
			}
		}
	case n_slice:
		var lower, upper AST
		if n.args[0] != nil {
			lower = n.args[0].toASTTop(NewContext())
		}
		if n.args[1] != nil {
			upper = n.args[1].toASTTop(NewContext())
		}
		return &SliceOp{
			Val:   &Symbol{Name: n.sval, p: n.p},
			Lower: lower,
			Upper: upper,
		}
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
		init := n.args[0].toASTTop(NewContext())
		cond := n.args[1].toASTTop(NewContext())
		step := n.args[2].toASTTop(NewContext())
		body := n.args[3].toASTTop(NewContext())
		ct := cond.ASTType(c)
		if !ct.Same(boolASTType()) {
			ParseErrorF(n.args[1], "Cannot compile for loop with non-boolean condition: %v\n", ct)
		}
		return &For{
			Init: init,
			Cond: cond,
			Step: step,
			Body: body,
		}
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
