package main

import (
	"fmt"
	"io"
	"strings"
)

func Compile(of io.Writer, c *Context, a AST) {
	compileTop(of, c, a, nullspot)
}

var nullspot = spot{}
var regtype = ASTType{Name: "*untyped-register*"}

type spot struct {
	ref string
	t   ASTType
}

func newSpot(of io.Writer, c *Context, ref string, t ASTType) spot {
	s := spot{ref: ref, t: t}
	sz := t.Size(c)
	if sz <= 8 {
		fmt.Fprintf(of, "\tlocal %s %d\n", ref, sz*8)
	} else {
		fmt.Fprintf(of, "\tbytes %s %d\n", ref, sz)
	}
	return s
}

func regSpot(of io.Writer, name string) spot {
	fmt.Fprintf(of, "\tacquire %s\n", name)
	return spot{ref: name, t: regtype}
}

func (s *spot) free(of io.Writer) {
	if s.empty() {
		return
	}
	if s.t.Same(&regtype) {
		// if this is a regtype spot, release it.
		fmt.Fprintf(of, "\trelease %s\n", s.ref)
	} else if strings.HasPrefix(s.ref, temp_prefix) {
		// otherwise, if this is a temporary local or bytes, forget it.
		// We only free temporaries, since variables should only me manually
		// released.
		fmt.Fprintf(of, "\tforget %s\n", s.ref)
	}
	s.ref = ""
}

func (s *spot) same(s2 *spot) bool {
	return s.ref == s2.ref
}

func (s *spot) empty() bool {
	return s.ref == ""
}

func move(of io.Writer, c *Context, dest spot, src spot) {
	if dest.t == regtype || src.t == regtype {
		// We have a register. Just move it.
		fmt.Fprintf(of, "\tmov %s %s\n", dest.ref, src.ref)
		return
	}
	if src.t.Indirection == 0 && src.t.Size(c) > 8 {
		panic("MOVE OF BYTES TYPES NOT IMPLEMENTED!")
	}
	// TODO: other types, array stuff, etc.
	if dest.t.Same(&src.t) {
		fmt.Fprintf(of, "\tmov %s %s\n", dest.ref, src.ref)
		return
	}
	depoint := dest.t
	depoint.Indirection--
	if depoint.Same(&src.t) {
		// dest was *T and src is T
		fmt.Fprintf(of, "\tmov [%s] %s\n", dest.ref, src.ref)
		return
	}

	fmt.Printf("DEST: %#v\nSRC:%#v\nDEPOINT: %#v\n", dest, src, depoint)
	panic("move TODO")
}

// func ensureNotRegspot(of io.Writer, c *Context, s spot) spot {
// 	if s.t == regtype {
// 		newSpot(of io.Writer, c *Context, ref string, t ASTType)
// 	}
// }

// compileLval compiles an AST into a spot of type a.ASTType *or* pointer to a.ASTType.
// If the type is the same as the value, it must me suitable to mov dest src.
// Otherwise, if a pointer, it must be suitable for mov [dest] src, or for larger
// types memcpy(dest, &src).
func compileLval(of io.Writer, c *Context, a AST, dest spot) spot {
	switch ast := a.(type) {
	case *Symbol:
		t := ast.ASTType(c)
		s := spot{ref: ast.Name, t: t}
		if dest.same(&nullspot) {
			return s
		}
		move(of, c, dest, s)
		return dest
		// t := ast.ASTType(c)
		// if t.Indirection == 0 && t.Size(c) > 8 {
		// 	// s is a value that doesn't fit in a
		// 	// register. We can just return it
		// 	// since it is a pointer type already.
		// 	s := spot{ref: ast.Name, t: t}
		// 	if dest.same(&nullspot) {
		// 		return s
		// 	}
		// 	move(of, c, dest, s)
		// 	return dest
		// }
		// pt := t
		// pt.Indirection++
		// if dest.empty() {
		// 	dest = newSpot(of, c, c.Temp(), pt)
		// }
		// fmt.Fprintf(of, "\tlea %s %s\n", dest.ref, ast.Name)
		// return dest
	case *Dot:
		l := compileTop(of, c, ast.Val, nullspot)
		def, ok := c.StructDeclForName(l.t.Name)
		if !ok {
			panic("No struct (TODO)")
		}
		offset := 0
		var mtype ASTType
		for _, f := range def.Fields {
			if f.Name == ast.Member {
				mtype = f.Type
				break
			}
			offset += f.Type.Size(c)
		}
		if dest.empty() {
			dest = newSpot(of, c, c.Temp(), mtype)
		}
		fmt.Fprintf(of, "\tlea %s [%s+%d]\n", dest.ref, l.ref, offset)
		return dest
	}
	panic(fmt.Sprintf("(LVAL) FALLTHROUGH: %#v\n", a))
}

func compileTop(of io.Writer, c *Context, a AST, dest spot) spot {
	switch ast := a.(type) {
	case *StructDecl:
		c.DefineStruct(ast.TName, ast)
		return nullspot
	case *VarDecl:
		c.BindVar(ast.Name, ast.Type)
		newSpot(of, c, ast.Name, ast.Type)
		return nullspot
	case *FuncDecl:
		c := c.SubContext()
		retlab := c.PushRetlabel()
		defer c.PopRetlabel()
		fmt.Fprintf(of, "function %s\n", ast.Name)
		// TODO: Add function type signature
		for i, a := range ast.Args {
			fmt.Fprintf(of, "\targi %s %d\n", a.Name, i)
			c.BindVar(a.Name, a.Type)
		}
		fmt.Fprintf(of, "\n\tprologue\n\n")
		compileTop(of, c, ast.Body, nullspot)
		fmt.Fprintf(of, "\n\tlabel %s\n", retlab)
		fmt.Fprintf(of, "\tepilogue\n")
		fmt.Fprintf(of, "\tret\n\n")
		return nullspot
	case *Block:
		for _, st := range ast.Body {
			s := compileTop(of, c, st, nullspot)
			s.free(of)
		}
		return nullspot
	case *Funcall:
		fmt.Fprintf(of, "\n\t// funcall %s\n", ast.FName)
		decl, ok := c.FuncDeclForName(ast.FName)
		if !ok {
			panic("No such func. TODO: error messages")
		}
		if len(ast.Args) != len(decl.Args) {
			panic("BAD NUMBER OF ARGS! (TODO)\n")
		}
		// This song and dance is to resolve the arguments into their
		// associated registers.
		//
		// It's possible to optimize this, but this is ok for now.
		// The reason we have two loops is that we can't acquire the
		// call registers in the first loop while we're compiling the
		// arguments. An argument might be a funcall, which itself will
		// need the call registers.
		// So instead, we first compile all the arguments into
		// argspots, and then move each argument into its register for
		// the call.
		order := []string{"rdi", "rsi", "rdx", "rcx", "r8", "r9"}
		var argspots []spot
		for i := 0; i < len(ast.Args); i++ {
			arg := ast.Args[i]
			argt := arg.ASTType(c)
			if !argt.Same(&decl.Args[i].Type) {
				panic("BAD ARG TYPE! (TODO)\n")
			}
			if i > 5 {
				panic("More than 6 args not supported yet (TODO)")
			}
			a := compileTop(of, c, arg, nullspot)
			if a == nullspot {
				panic("AAAH! NULLSPOT!\n")
			}
			argspots = append(argspots, a)
		}
		for i := 0; i < len(ast.Args); i++ {
			s := regSpot(of, order[i])
			a := argspots[i]
			move(of, c, s, a)
			a.free(of)
		}

		// We want some kind of to_var asm instruction
		// that can tell the assembler "this variable is in this register now"
		// that way we can point a temporary at rax and have the assembler
		// manage moving it around.
		//
		// for now, we'll just always move rax, even when nobody will use it.
		rax := regSpot(of, "rax")
		fmt.Fprintf(of, "\tcall %s\n", ast.FName)
		for i := 0; i < len(ast.Args); i++ {
			fmt.Fprintf(of, "\trelease %s\n", order[i])
		}
		ret := nullspot
		if decl.Return != voidASTType() {
			ret = newSpot(of, c, c.Temp(), decl.Return)
			move(of, c, ret, rax)
		}
		rax.free(of)
		fmt.Fprintf(of, "\n")
		return ret
	case *Dot:
		l := compileTop(of, c, ast.Val, nullspot)
		def, ok := c.StructDeclForName(l.t.Name)
		if !ok {
			panic("No struct (TODO)")
		}
		offset := 0
		var mtype ASTType
		for _, f := range def.Fields {
			if f.Name == ast.Member {
				mtype = f.Type
				break
			}
			offset += f.Type.Size(c)
		}
		if dest.empty() {
			dest = newSpot(of, c, c.Temp(), mtype)
		}
		fmt.Fprintf(of, "\tmov %s [%s+%d]\n", dest.ref, l.ref, offset)
		return dest
	case *Deref:
		v := compileTop(of, c, ast.Val, nullspot)
		t := v.t
		if t.Indirection == 0 {
			panic("Cannot dereference non-pointer type")
		}
		if dest.empty() {
			dest = newSpot(of, c, c.Temp(), t)
		}
		fmt.Fprintf(of, "\tmov %s [%s]\n", dest, v)
		return dest
	case *Address:
	case *Assignment:
		fmt.Fprintf(of, "\t// Assignment begin\n")
		lv := compileLval(of, c, ast.Target, nullspot)
		srct := ast.Val.ASTType(c)
		if lv.t.Same(&srct) {
			// srctype == dsttype
			// this means the dsttype is not a pointer to the location.
			// and we can use lv as the dest in the compile call.
			val := compileTop(of, c, ast.Val, lv)
			if !val.same(&lv) {
				move(of, c, lv, val)
				val.free(of)
			}
			return nullspot
		}
		val := compileTop(of, c, ast.Val, nullspot)
		if val.t.Size(c) != 8 {
			panic("CANNOT MOVE TYPES THAT ARE NOT 64 BITS YET!")
			// TODO: Need to handle type sizes
		}
		// if it's size == 8, it doesn't matter if it's a pointer or a value,
		// we can just copy it.
		fmt.Fprintf(of, "\t// Assignment.move\n")
		move(of, c, lv, val)
		//fmt.Fprintf(of, "\tmov [%s] %s\n", lv.ref, val.ref)
		lv.free(of)
		val.free(of)
		return nullspot

	case *StructLiteral:
	case *IfStmt:
	case *Op2:
	case *Return:
		// We always use rax for return values, due to System v calling convention.
		rax := regSpot(of, "rax")
		v := compileTop(of, c, ast.Val, rax)
		if !v.same(&rax) {
			move(of, c, rax, v)
			v.free(of)
		}
		c.Return(of)
		return nullspot
	case *Symbol:
		s := spot{ref: ast.Name, t: ast.ASTType(c)}
		if dest.same(&nullspot) {
			return s
		}
		move(of, c, dest, s)
		return dest
	case *Literal:
		t := ast.ASTType(c)
		// literals can have no indirection and cannot be arrays (yet)
		if t.Indirection > 0 || t.ArraySize > 0 {
			panic("NOT IMPLEMENTED (TODO)")
		}
		if dest.empty() {
			dest = newSpot(of, c, c.Temp(), t)
		}

		switch v := ast.Val.(type) {
		case string:
			s := c.String(v)
			fmt.Fprintf(of, "\tlea %s %s\n", dest.ref, s)
		case uint64:
			fmt.Fprintf(of, "\tmov %s %d\n", dest.ref, v)
		default:
			panic("NOT IMPLEMENTED (TODO)")
		}
		return dest
	}
	panic(fmt.Sprintf("(TOP) FALLTHROUGH: %#v\n", a))
}
