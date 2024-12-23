package main

import (
	"fmt"
	"io"
	"strings"
)

var annotate bool = true

func note(of io.Writer, f string, args ...any) {
	if !annotate {
		return
	}
	fmt.Fprintf(of, f, args...)
}

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

func newSpotWithReg(of io.Writer, c *Context, ref string, t ASTType, reg string) spot {
	s := spot{ref: ref, t: t}
	sz := t.Size(c)
	if sz <= 8 {
		fmt.Fprintf(of, "\tlocal %s %d %s\n", ref, sz*8, reg)
	} else {
		fmt.Fprintf(of, "\tbytes %s %d %s\n", ref, sz, reg)
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
	if s.t.Same(regtype) {
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

func spot_memcpy(of io.Writer, dst, src spot, bytes int) {
	qwords := bytes / 8
	singles := bytes % 8

	fmt.Fprintf(of, "\tacquire rax\n")
	for i := 0; i < qwords; i++ {
		fmt.Fprintf(of, "\tmov rax [%s+%d]\n", src.ref, i*8)
		fmt.Fprintf(of, "\tmov [%s+%d] rax\n", dst.ref, i*8)
	}
	for i := 0; i < singles; i++ {
		fmt.Fprintf(of, "\tmovb al [%s+%d]\n", src.ref, qwords*8+i)
		fmt.Fprintf(of, "\tmovb [%s+%d] al\n", dst.ref, qwords*8+i)
	}
	fmt.Fprintf(of, "\trelease rax\n")
}

// func spot_memset(of io.Writer, dst spot, val byte, bytes int) {
// 	qwords := bytes / 8
// 	singles := bytes % 8

// 	fmt.Fprintf(of, "\tacquire rax\n")
// 	fmt.Fprintf(of, "\tmov rax %d\n", val)
// 	for i := 0; i < qwords; i++ {
// 		fmt.Fprintf(of, "\tmov [%s+%d] rax\n", dst, i*8)
// 	}
// 	for i := 0; i < singles; i++ {
// 		fmt.Fprintf(of, "\tmovb [%s+%d] al\n", dst, qwords*8+i)
// 	}
// 	fmt.Fprintf(of, "\trelease rax\n")
// }

func move(of io.Writer, c *Context, dest spot, src spot) {
	if dest.t == regtype || src.t == regtype {
		// We have a register. Just move it.
		fmt.Fprintf(of, "\tmov %s %s\n", dest.ref, src.ref)
		return
	}
	if src.t.Indirection == 0 && src.t.Size(c) > 8 {
		spot_memcpy(of, dest, src, src.t.Size(c))
		return
		//panic("MOVE OF BYTES TYPES NOT IMPLEMENTED!")
	}
	// TODO: other types, array stuff, etc.
	if dest.t.Same(src.t) {
		fmt.Fprintf(of, "\tmov %s %s\n", dest.ref, src.ref)
		return
	}
	depoint := dest.t
	depoint.Indirection--
	if depoint.Same(src.t) {
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
		if s.same(&dest) {
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
		l := compileLval(of, c, ast.Val, nullspot)
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
		mtype.Indirection += 1
		if dest.empty() {
			dest = newSpot(of, c, c.Temp(), mtype)
		}
		fmt.Fprintf(of, "\tlea %s [%s+%d]\n", dest.ref, l.ref, offset)
		return dest
	case *Deref:
		v := compileTop(of, c, ast.Val, nullspot)
		t := v.t
		if t.Indirection == 0 {
			panic("Cannot dereference non-pointer type")
		}
		// We're just going to return the pointer.
		return v
	}
	panic(fmt.Sprintf("(LVAL) FALLTHROUGH: %#v\n", a))
}

// var level int

// func indent() {
// 	for i := 0; i < level; i++ {
// 		fmt.Printf("\t")
// 	}
// }

func compileTop(of io.Writer, c *Context, a AST, dest spot) spot {
	if dest.empty() {
		note(of, "\t// begin %#v\n", a.Note())
	} else {
		note(of, "\t// begin %#v into %v\n", a.Note(), dest.ref)
	}
	defer note(of, "\t// end %#v\n", a.Note())
	// indent()
	// fmt.Printf("COMPILE %#v\n", a)
	// level++
	// defer func() {
	// 	level--
	// }()
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
		if ast.Name == "main" {
			note(of, "\n\t// default return 0 from main\n")
			fmt.Fprintf(of, "\tmov rax 0\n")
		}
		fmt.Fprintf(of, "\n\tlabel %s\n", retlab)
		fmt.Fprintf(of, "\tepilogue\n")
		fmt.Fprintf(of, "\tret\n\n")
		return nullspot
	case *Block:
		for _, st := range ast.Body {
			note(of, "\n")
			s := compileTop(of, c, st, nullspot)
			s.free(of)
		}
		return nullspot
	case *Funcall:
		decl, ok := c.FuncDeclForName(ast.FName)
		if !ok {
			panic(fmt.Sprintf("No such func \"%v\" TODO: error messages", ast.FName))
		}
		if len(ast.Args) != len(decl.Args) {
			panic("BAD NUMBER OF ARGS! (TODO)\n")
		}

		argorder := setupArgs(of, c, ast, decl)

		// We want some kind of to_var asm instruction
		// that can tell the assembler "this variable is in this register now"
		// that way we can point a temporary at rax and have the assembler
		// manage moving it around.
		//
		// for now, we'll just always move rax, even when nobody will use it.
		note(of, "\t// acquire rax for call return\n")
		rax := regSpot(of, "rax")
		fmt.Fprintf(of, "\tcall %s\n", ast.FName)
		for i := 0; i < len(ast.Args); i++ {
			note(of, "\t// release call registers\n")
			//fmt.Fprintf(of, "\t// release call registers\n")
			fmt.Fprintf(of, "\trelease %s\n", argorder[i])
		}
		ret := nullspot
		if decl.Return != voidASTType() {
			if !dest.empty() {
				ret = dest
			} else {
				ret = newSpot(of, c, c.Temp(), decl.Return)
			}
			move(of, c, ret, rax)
		}
		note(of, "\t// free rax for call return\n")
		rax.free(of)
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
		t.Indirection -= 1
		//move(of, c, dest, v)

		if t.Indirection > 0 {
			if dest.empty() {
				fmt.Fprintf(of, "\t// New temp for deref, type: %#v\n", t)
				dest = newSpot(of, c, c.Temp(), t)
			}
			// it's a pointer. Just copy.
			fmt.Fprintf(of, "\tmov %s [%s]\n", dest.ref, v.ref)
		} else if t.ArraySize > 0 {
			// it's an array.
			panic("Array copying not implemented yet\n")
		} else if t.Size(c) > 8 {
			// It's a large object, meaning we need to pass it by pointer.
			v.t.Indirection -= 1
			//fmt.Fprintf(of, "\tmov %s %s\n", dest.ref, v.ref)
			return v
		} else {
			if dest.empty() {
				fmt.Fprintf(of, "\t// New temp for deref, type: %#v\n", t)
				dest = newSpot(of, c, c.Temp(), t)
			}
			// It's a small object. Just copy it.
			fmt.Fprintf(of, "\tmov %s [%s]\n", dest.ref, v.ref)
		}

		return dest
	case *Address:
		if dest.empty() {
			dest = newSpot(of, c, c.Temp(), ast.ASTType(c))
		}
		fmt.Fprintf(of, "\tlea %s %s\n", dest.ref, ast.Var)
		return dest
	case *Index:
		v := compileTop(of, c, ast.Val, nullspot)
		if dest.empty() {
			dest = newSpot(of, c, c.Temp(), ast.ASTType(c))
		}
		if ast.NAST == nil {
			// Special case for index literals.
			if v.t.Name == "str" && v.t.Indirection == 0 && v.t.ArraySize == 0 {
				// SPECIAL CASE! We have a string. This needs to go away.
				// See: Index.ASTType() documentation.
				fmt.Fprintf(of, "\tmov %s [%s+%d]\n", dest.ref, v.ref, ast.N)
				return dest
			}
			targt := ast.ASTType(c)
			off := uint64(targt.Size(c)) * ast.N
			fmt.Fprintf(of, "\tmov %s [%s+%d]\n", dest.ref, v.ref, off)
			return dest
		}
		// TODO: Need to add assembler support for indexing like [base + index * size].
		// For now, we'll calculate it ourselves.
		if v.t.Name == "str" && v.t.Indirection == 0 && v.t.ArraySize == 0 {
			// SPECIAL CASE! We have a string. This needs to go away.
			// See: Index.ASTType() documentation.
			idx := newSpot(of, c, c.Temp(), ast.NAST.ASTType(c))
			nidx := compileTop(of, c, ast.NAST, idx)
			if !nidx.same(&idx) {
				idx.free(of)
			}
			fmt.Fprintf(of, "\tadd %s %s\n", nidx.ref, v.ref)
			fmt.Fprintf(of, "\tmov %s [%s]\n", dest.ref, nidx.ref)
			return dest
		}
		panic("Generic indexing not supported yet.")
	case *Assignment:
		lv := compileLval(of, c, ast.Target, nullspot)
		srct := ast.Val.ASTType(c)
		if lv.t.Same(srct) {
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
		move(of, c, lv, val)
		//fmt.Fprintf(of, "\tmov [%s] %s\n", lv.ref, val.ref)
		lv.free(of)
		val.free(of)
		return nullspot

	case *StructLiteral:
	case *IfStmt:
	case *For:
		v := compileTop(of, c, ast.Init, nullspot)
		if !v.empty() {
			v.free(of)
		}
		start := c.Label("for")
		end := c.Label("end")
		fmt.Fprintf(of, "\tlabel %s\n", start)
		v = compileTop(of, c, ast.Cond, nullspot)
		fmt.Fprintf(of, "\ttest %s %s\n", v.ref, v.ref)
		fmt.Fprintf(of, "\tjz %s\n", end)
		v.free(of)
		v = compileTop(of, c, ast.Body, nullspot)
		if !v.empty() {
			v.free(of)
		}
		v = compileTop(of, c, ast.Step, nullspot)
		if !v.empty() {
			v.free(of)
		}
		fmt.Fprintf(of, "\tjmp %s\n", start)
		fmt.Fprintf(of, "\tlabel %s\n", end)
		return nullspot
	case *Op2:
		return doOp2(of, c, ast, dest)
	case *Return:
		dest := newSpotWithReg(of, c, c.Temp(), ast.Val.ASTType(c), "rax")
		v := compileTop(of, c, ast.Val, dest)
		if !v.same(&dest) {
			dest.free(of)
			dest = v
			panic("Should this happen?")
		}
		fmt.Fprintf(of, "\tinreg %s %s\n", dest.ref, "rax")
		c.Return(of)
		dest.free(of)
		return nullspot
	case *Symbol:
		s := spot{ref: ast.Name, t: ast.ASTType(c)}
		if dest.same(&nullspot) {
			return s
		}
		if s.same(&dest) {
			note(of, "\t// destination is already %s\n", dest.ref)
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
		case byte:
			fmt.Fprintf(of, "\tmov %s %d\n", dest.ref, v)
		default:
			panic("NOT IMPLEMENTED (TODO)")
		}
		return dest
	}
	panic(fmt.Sprintf("(TOP) FALLTHROUGH: %#v\n", a))
}

func containsFuncall(a AST) bool {
	switch ast := a.(type) {
	case *Funcall:
		return true
	case *Dot:
		return containsFuncall(ast.Val)
	case *Deref:
		return containsFuncall(ast.Val)
	case *Address:
		return false
	case *Op2:
		return containsFuncall(ast.First) || containsFuncall(ast.Second)
	case *Literal:
		return false
	case *Symbol:
		return false
	}
	panic("ContainsFuncall Fallthrough\n")
}

func setupArgs(of io.Writer, c *Context, f *Funcall, d *FuncDecl) []string {
	order := []string{"rdi", "rsi", "rdx", "rcx", "r8", "r9"}
	// // TODO: Want to be able to optimize this, but can't currently.
	// // Same issue as other register optimizations - we can't acquire the registers
	// // we want ahead-of-time since things like funcalls and register-specific ops
	// // like mul & div may need them.
	// //
	// // funcallArgs := false
	// // for i := 0; i < len(f.Args); i++ {
	// // 	arg := f.Args[i]
	// // 	if containsFuncall(arg) {
	// // 		funcallArgs = true
	// // 		break
	// // 	}
	// // }
	// // order := []string{"rdi", "rsi", "rdx", "rcx", "r8", "r9"}
	// // if !funcallArgs {
	// // 	// optimization. We know there are no function calls in the
	// // 	// arguments to this function, so we can copy directly into the
	// // 	// call registers.
	// // 	for i := 0; i < len(f.Args); i++ {
	// // 		arg := f.Args[i]
	// // 		argt := arg.ASTType(c)
	// // 		if !argt.Same(&d.Args[i].Type) {
	// // 			panic("BAD ARG TYPE! (TODO)\n")
	// // 		}
	// // 		if i > 5 {
	// // 			panic("More than 6 args not supported yet (TODO)")
	// // 		}
	// // 		fmt.Fprintf(of, "\t// acquire %s for funcall args\n", order[i])
	// // 		rspot := regSpot(of, order[i])
	// // 		a := compileTop(of, c, arg, rspot)
	// // 		if a == nullspot {
	// // 			panic("AAAH! NULLSPOT!\n")
	// // 		}
	// // 		if a.ref != order[i] {
	// // 			move(of, c, rspot, a)
	// // 			a.free(of)
	// // 		}
	// // 	}
	// // 	return order
	// // }

	// // This song and dance is to resolve the arguments into their
	// // associated registers.
	// //
	// // It's possible to optimize this, but this is ok for now.
	// // The reason we have two loops is that we can't acquire the
	// // call registers in the first loop while we're compiling the
	// // arguments. An argument might be a funcall, which itself will
	// // need the call registers.
	// // So instead, we first compile all the arguments into
	// // argspots, and then move each argument into its register for
	// // the call.

	// We try optimistically to keep the arguments in the call registers
	// using the newSpotWithReg function. If necessary, these are evicted for
	// things like funcalls and mul ops
	var argspots []spot
	for i := 0; i < len(f.Args); i++ {
		arg := f.Args[i]
		argt := arg.ASTType(c)
		if !argt.Same(d.Args[i].Type) {
			panic("BAD ARG TYPE! (TODO)\n")
		}
		if i > 5 {
			panic("More than 6 args not supported yet (TODO)")
		}
		var dest spot
		if argt.Size(c) == PTR_SIZE {
			dest = newSpotWithReg(of, c, c.Temp(), argt, order[i])
		} else {
			dest = newSpot(of, c, c.Temp(), argt)
		}
		a := compileTop(of, c, arg, dest)
		if a == nullspot {
			panic("AAAH! NULLSPOT!\n")
		}
		if !a.same(&dest) {
			dest.free(of)
			dest = a
		}
		argspots = append(argspots, dest)
	}
	for i := 0; i < len(f.Args); i++ {
		//s := regSpot(of, order[i])
		//a := argspots[i]
		//move(of, c, s, a)
		note(of, "\t// Ensuring argument %d (%v) is in register %v for call.\n",
			i, argspots[i].ref, order[i])
		argt := f.Args[i].ASTType(c)
		if argt.Size(c) >= PTR_SIZE {
			// The >= requires some explanation here.
			// It relies on the fact that for objects of size > PTR_SIZE,
			// they are held as pointers in register, meaning we can move
			// them around as if they were 64-bit values.
			fmt.Fprintf(of, "\tinreg %s %s\n", argspots[i].ref, order[i])
		} else {
			fmt.Fprintf(of, "\tacquire %s\n", order[i])
			fmt.Fprintf(of, "\tmovzx %s %s\n", order[i], argspots[i].ref)
		}
		argspots[i].free(of)
	}

	return order
}

func jumpOp(of io.Writer, c *Context, o *Op2, label string) {
	switch o.Type {
	case n_lt:
		first := compileTop(of, c, o.First, nullspot)
		second := compileTop(of, c, o.Second, nullspot)
		// TODO: For now we only use signed integers.
		// so we will use the setl/setg etc. instructions.
		// For unsigned integers we will need to use
		// setb/seta etc.
		fmt.Fprintf(of, "\tcmp %s %s\n", first.ref, second.ref)
		fmt.Fprintf(of, "\tjl %s\n", label)
	case n_le:
		first := compileTop(of, c, o.First, nullspot)
		second := compileTop(of, c, o.Second, nullspot)
		// TODO: For now we only use signed integers.
		// so we will use the setl/setg etc. instructions.
		// For unsigned integers we will need to use
		// setb/seta etc.
		fmt.Fprintf(of, "\tcmp %s %s\n", first.ref, second.ref)
		fmt.Fprintf(of, "\tgjle %s\n", label)
	case n_gt:
		first := compileTop(of, c, o.First, nullspot)
		second := compileTop(of, c, o.Second, nullspot)
		// TODO: For now we only use signed integers.
		// so we will use the setl/setg etc. instructions.
		// For unsigned integers we will need to use
		// setb/seta etc.
		fmt.Fprintf(of, "\tcmp %s %s\n", first.ref, second.ref)
		fmt.Fprintf(of, "\tjg %s\n", label)
	case n_ge:
		first := compileTop(of, c, o.First, nullspot)
		second := compileTop(of, c, o.Second, nullspot)
		// TODO: For now we only use signed integers.
		// so we will use the setl/setg etc. instructions.
		// For unsigned integers we will need to use
		// setb/seta etc.
		fmt.Fprintf(of, "\tcmp %s %s\n", first.ref, second.ref)
		fmt.Fprintf(of, "\tjge %s\n", label)
	}
}

func doOp2(of io.Writer, c *Context, o *Op2, dest spot) spot {
	if dest.empty() {
		dest = newSpot(of, c, c.Temp(), o.ASTType(c))
	}
	switch o.Type {
	case n_add:
		first := compileTop(of, c, o.First, dest)
		if first == dest {
			// create a new dest for second arg
			dest = newSpot(of, c, c.Temp(), o.ASTType(c))
		}
		second := compileTop(of, c, o.Second, dest)
		fmt.Fprintf(of, "\tadd %s %s\n", first.ref, second.ref)
		return first
	case n_sub:
		first := compileTop(of, c, o.First, dest)
		if first == dest {
			// create a new dest for second arg
			dest = newSpot(of, c, c.Temp(), o.ASTType(c))
		}
		second := compileTop(of, c, o.Second, dest)
		fmt.Fprintf(of, "\tsub %s %s\n", first.ref, second.ref)
		return first
	case n_mul:
		if dest.empty() {
			dest = newSpotWithReg(of, c, c.Temp(), o.First.ASTType(c), "rax")
		}
		first := compileTop(of, c, o.First, dest)
		if !first.same(&dest) {
			panic(fmt.Sprintf("Expected to get %#v but got %#v from %#v\n", dest, first, o.First))
			//dest.free(of)
			dest = first
		}
		second := newSpotWithReg(of, c, c.Temp(), o.Second.ASTType(c), "rdx")
		snd := compileTop(of, c, o.Second, second)
		if !snd.same(&second) {
			//second.free(of)
			second = snd
		}

		fmt.Fprintf(of, "\tinreg %s rax\n", first.ref)
		fmt.Fprintf(of, "\tinreg %s rdx\n", second.ref)
		fmt.Fprintf(of, "\timul rdx\n")
		second.free(of)
		return first
	case n_div:
		if dest.empty() {
			dest = newSpotWithReg(of, c, c.Temp(), o.First.ASTType(c), "rax")
		}
		first := compileTop(of, c, o.First, dest)
		if !first.same(&dest) {
			panic(fmt.Sprintf("Expected to get %#v but got %#v from %#v\n", dest, first, o.First))
			//dest.free(of)
			dest = first
		}
		second := compileTop(of, c, o.Second, nullspot)
		fmt.Fprintf(of, "\tinreg %s rax\n", first.ref)
		rdx := regSpot(of, "rdx")
		fmt.Fprintf(of, "\txor rdx rdx\n")
		fmt.Fprintf(of, "\tdiv %s\n", second.ref)
		rdx.free(of)
		second.free(of)
		return first
	case n_lt:
		first := compileTop(of, c, o.First, nullspot)
		second := compileTop(of, c, o.Second, nullspot)
		if dest.empty() {
			dest = newSpot(of, c, c.Temp(), boolASTType())
		}

		// TODO: For now we only use signed integers.
		// so we will use the setl/setg etc. instructions.
		// For unsigned integers we will need to use
		// setb/seta etc.
		fmt.Fprintf(of, "\tcmp %s %s\n", first.ref, second.ref)
		fmt.Fprintf(of, "\tsetl %s\n", dest.ref)
		return dest
	case n_le:
		first := compileTop(of, c, o.First, nullspot)
		second := compileTop(of, c, o.Second, nullspot)
		if dest.empty() {
			dest = newSpot(of, c, c.Temp(), boolASTType())
		}

		// TODO: For now we only use signed integers.
		// so we will use the setl/setg etc. instructions.
		// For unsigned integers we will need to use
		// setb/seta etc.
		fmt.Fprintf(of, "\tcmp %s %s\n", first.ref, second.ref)
		fmt.Fprintf(of, "\tsetle %s\n", dest.ref)
		return dest
	case n_gt:
		first := compileTop(of, c, o.First, nullspot)
		second := compileTop(of, c, o.Second, nullspot)
		if dest.empty() {
			dest = newSpot(of, c, c.Temp(), boolASTType())
		}

		// TODO: For now we only use signed integers.
		// so we will use the setl/setg etc. instructions.
		// For unsigned integers we will need to use
		// setb/seta etc.
		fmt.Fprintf(of, "\tcmp %s %s\n", first.ref, second.ref)
		fmt.Fprintf(of, "\tsetg %s\n", dest.ref)
		return dest
	case n_ge:
		first := compileTop(of, c, o.First, nullspot)
		second := compileTop(of, c, o.Second, nullspot)
		if dest.empty() {
			dest = newSpot(of, c, c.Temp(), boolASTType())
		}

		// TODO: For now we only use signed integers.
		// so we will use the setl/setg etc. instructions.
		// For unsigned integers we will need to use
		// setb/seta etc.
		fmt.Fprintf(of, "\tcmp %s %s\n", first.ref, second.ref)
		fmt.Fprintf(of, "\tsetge %s\n", dest.ref)
		return dest
	}
	panic("Could not do op\n")
}
