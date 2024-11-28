package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/davecgh/go-spew/spew"
	"github.com/knusbaum/gbasm"
)

// Context holds types and bindings for the current lexical environment.
type Context struct {
	parent *Context

	// maps variable names to their types.
	bindings map[string]ASTType
	// maps struct names to their declarations.
	structs map[string]*StructDecl
	// maps function names to their declarations.
	funcs map[string]*FuncDecl

	// return label stack. Return returns from the current Context
	// by jumping to the label.
	retlabs []string
	labeli  int

	// Counter for temporaries
	tempi int

	// Keeps the strings to be written as data items later.
	strngs map[string]string
}

func NewContext() *Context {
	return &Context{
		bindings: make(map[string]ASTType),
		structs:  make(map[string]*StructDecl),
		funcs:    make(map[string]*FuncDecl),
		strngs:   make(map[string]string),
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

func (c *Context) BindVar(name string, t ASTType) {
	if _, ok := c.bindings[name]; ok {
		panic("TODO")
	}
	c.bindings[name] = t
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

// Push a new return label onto the return stack
func (c *Context) PushRetlabel() string {
	c.labeli++
	l := fmt.Sprintf("_LABEL_%d", c.labeli)
	c.retlabs = append(c.retlabs, l)
	return l
}

// Pop a return label from the return stack.
func (c *Context) PopRetlabel() {
	c.retlabs = c.retlabs[:len(c.retlabs)-1]
}

const temp_prefix = "Temp_"

func (c *Context) Temp() string {
	c.tempi++
	return fmt.Sprintf("%s%d", temp_prefix, c.tempi)
}

func (c *Context) Return(of io.Writer) {
	fmt.Fprintf(of, "\tjmp %s\n", c.retlabs[len(c.retlabs)-1])
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
	Indirection int // pointer level, i.e. ***int -> Indirection: 3
	ArraySize   int // zero for non-arrays.
}

const PTR_SIZE = 8

// The size in bytes that the type occupies.
func (t *ASTType) Size(c *Context) int {
	if t.Indirection > 0 {
		return PTR_SIZE
	}
	var baseSize int
	// builtin types
	switch t.Name {
	case "num":
		baseSize = 8
	case "str":
		baseSize = 8 // TODO: Is this right? We fucked this up with the last version.
	default:
		d, ok := c.StructDeclForName(t.Name)
		if !ok {
			panic(fmt.Sprintf("No such type %v. TODO: Errors", t.Name))
		}
		baseSize = d.Size(c)
	}
	if t.ArraySize > 0 {
		return baseSize * t.ArraySize
	}
	return baseSize
}

func (t *ASTType) Same(t2 *ASTType) bool {
	return t.Name == t2.Name &&
		t.Indirection == t2.Indirection &&
		t.ArraySize == t2.ArraySize
}

func mkTypename(n *Node) ASTType {
	var t ASTType
	if n.t != n_typename {
		ParseErrorF(n, "Expected type name but found %v", n.t)
	}
	t.Name = n.sval
	t.Indirection = int(n.ival)
	if len(n.args) > 0 {
		array := n.args[0]
		if array.t != n_index {
			ParseErrorF(array, "Expected an array specifier, but found %v", array.t)
		}
		t.ArraySize = int(array.ival)
	}
	return t
}

func voidASTType() ASTType {
	return ASTType{Name: "void"}
}

func numASTType() ASTType {
	return ASTType{Name: "num"}
}

func boolASTType() ASTType {
	return ASTType{Name: "bool"}
}

func strASTType() ASTType {
	return ASTType{Name: "str"}
}

type AST interface {
	// returns the type the expression gives.
	ASTType(*Context) ASTType
}

// A binding represents a name which is bound to a value of type Type
// in a specific context, such as struct members or function arguments.
type Binding struct {
	Name string
	Type ASTType
	// TODO: completeType(t *ASTType) BosonType
	// should fill out a complete BosonType based on the type name
	// given in the AST. For use in compile()
}

type StructDecl struct {
	TName  string
	Fields []Binding
}

func (*StructDecl) ASTType(*Context) ASTType {
	return voidASTType()
}

// Returns the size in bytes that the struct occupies.
func (s *StructDecl) Size(c *Context) int {
	size := 0
	for _, f := range s.Fields {
		size += f.Type.Size(c)
	}
	return size
}

type VarDecl struct {
	Name string
	Type ASTType
}

func (*VarDecl) ASTType(*Context) ASTType {
	return voidASTType()
}

type FuncDecl struct {
	Name   string
	Args   []Binding
	Return ASTType
	Body   *Block
}

func (*FuncDecl) ASTType(*Context) ASTType {
	return voidASTType()
}

type Block struct {
	Body []AST
}

func (*Block) ASTType(*Context) ASTType {
	// TODO: Would be nice for blocks to be expressions with a type,
	// but this requires complex checking for returns, etc.
	// We should do this, but not in the first pass.
	return voidASTType()
}

type Funcall struct {
	FName string
	Args  []AST
}

func (f *Funcall) ASTType(c *Context) ASTType {
	decl, ok := c.FuncDeclForName(f.FName)
	if !ok {
		panic("No such function. TODO: Nice error reports.")
	}
	return decl.Return
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
		panic("No such struct. TODO: Nice error reports.")
	}
	for _, f := range decl.Fields {
		if f.Name == d.Member {
			return f.Type
		}
	}
	panic("No such struct member. TODO: Nice error reports.")
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
	return t
}

type Address struct {
	// for now we can only take the address of a variable.
	// TODO: extend this to arbitrary expressions.
	// This is tricky, since not every expression can yield
	// something that is addressable, so for now we'll do the
	// easy thing.
	Var string
}

func (a *Address) ASTType(c *Context) ASTType {
	t, ok := c.TypeForVar(a.Var)
	if !ok {
		panic("Variable is not bound. TODO: Nice error reports.")
	}
	t.Indirection += 1
	return t
}

type Assignment struct {
	Target AST
	Val    AST
}

func (*Assignment) ASTType(c *Context) ASTType {
	return voidASTType()
}

type StructField struct {
	Name string
	Val  AST
}

type StructLiteral struct {
	Type   ASTType
	Fields []StructField
}

func (s *StructLiteral) ASTType(c *Context) ASTType {
	_, ok := c.StructDeclForName(s.Type.Name)
	if !ok {
		panic("(2) No such struct. TODO: Nice error reports.")
	}
	return s.Type
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
	case n_lt, n_le, n_gt, n_ge, n_deq:
		return boolASTType()
	case n_add, n_sub, n_mul, n_div:
		return numASTType()
	}
	panic("Bad Operation. TODO: Nice error reports.")
}

type Return struct {
	Val AST
}

func (*Return) ASTType(*Context) ASTType {
	return voidASTType()
}

// TODO: Do we need this, or can we just use the actual value?
// What's the purpose of boxing it?
type Literal struct {
	Val interface{}
}

func (l *Literal) ASTType(*Context) ASTType {
	switch l.Val.(type) {
	case string:
		return strASTType()
	case uint64:
		return numASTType()
	}
	panic("Bad Literal. TODO: Nice error reports.")
}

type Symbol struct {
	Name string
}

func (s *Symbol) ASTType(c *Context) ASTType {
	t, ok := c.TypeForVar(s.Name)
	if !ok {
		panic("Bad Variable. TODO: Nice error reports.")
	}
	return t
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
		c.BindVar(v.Name, v.Type)
		return &v
	case n_fn:
		var fn FuncDecl
		fn.Name = n.sval
		nargs := int(n.ival)
		args := n.args
		for i := 0; i < nargs; i++ {
			a := args[0]
			name := a.sval
			t := mkTypename(a.args[0])
			fn.Args = append(fn.Args, Binding{
				Name: name,
				Type: t,
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
		for _, bn := range n.args {
			b.Body = append(b.Body, bn.toASTTop(NewContext()))
		}
		return &b
	case n_funcall:
		var f Funcall
		f.FName = n.sval
		for _, a := range n.args {
			f.Args = append(f.Args, a.toASTTop(NewContext()))
		}
		return &f
	case n_str:
		//str := c.String(n.sval)
		//return &Literal{Val: str}
		return &Literal{Val: n.sval}
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
		return &Symbol{Name: n.sval}
	case n_eq:
		return &Assignment{
			Target: n.args[0].toASTTop(NewContext()),
			Val:    n.args[1].toASTTop(NewContext()),
		}
	case n_number:
		return &Literal{Val: n.ival}
	case n_stlit:
		var s StructLiteral
		s.Type = ASTType{Name: n.sval}
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
	case n_lt, n_le, n_gt, n_ge, n_deq, n_add, n_sub, n_mul, n_div:
		return &Op2{
			Type:   n.t,
			First:  n.args[0].toASTTop(NewContext()),
			Second: n.args[1].toASTTop(NewContext()),
		}
	case n_return:
		return &Return{
			Val: n.args[0].toASTTop(NewContext()),
		}
	}
	spew.Dump(n)
	ParseErrorF(n, "Node Type %s Fell through AST Generator.\n", n.t)
	return nil
}
