package main

import (
	"fmt"
	"io"
	"math/big"
	"strings"
)

var annotate bool = true

func note(of io.Writer, f string, args ...any) {
	if !annotate {
		return
	}
	fmt.Fprintf(of, f, args...)
}

func CompileErrorF(a AST, f string, args ...any) {
	panic(&interpreterError{
		msg: fmt.Sprintf(f, args...),
		p:   a.Pos(),
	})
}

func Compile(of io.Writer, c *Context, a AST) (e error) {
	defer func() {
		if err := recover(); err != nil {
			if le, ok := err.(*interpreterError); ok {
				a = nil
				e = le
				return
			}
			panic(err)
		}
	}()
	compileTop(of, c, a, nullspot)
	return nil
}

var nullspot = spot{}
var regtype = ASTType{Name: "*untyped-register*"}

// spot describes a bas-level name produced by codegen. ref is the
// name as it'll appear in emitted assembly; t is the Boson type; and
// nameIsAddress records whether bas resolves the name to a memory
// location (address-style: `bytes`-allocated stack chunks, file-
// scope globals) or to a register holding the value (value-style:
// `local`-allocated scalars). Sites that need to follow a pointer
// — Deref, register-scaled Index/SliceOp — consult nameIsAddress
// to decide whether they need an extra load to materialize the
// value into a register first.
type spot struct {
	ref           string
	t             ASTType
	nameIsAddress bool
}

func newSpot(of io.Writer, c *Context, ref string, t ASTType) spot {
	sz := t.Size(c)
	memBacked := typeIsMemoryBacked(c, t)
	s := spot{ref: ref, t: t, nameIsAddress: memBacked}
	if memBacked {
		fmt.Fprintf(of, "\tbytes %s %d\n", ref, sz)
	} else {
		fmt.Fprintf(of, "\tlocal %s %d\n", ref, sz*8)
	}
	return s
}

func newSpotWithReg(of io.Writer, c *Context, ref string, t ASTType, reg string) spot {
	sz := t.Size(c)
	memBacked := typeIsMemoryBacked(c, t)
	s := spot{ref: ref, t: t, nameIsAddress: memBacked}
	if memBacked {
		fmt.Fprintf(of, "\tbytes %s %d %s\n", ref, sz, reg)
	} else {
		fmt.Fprintf(of, "\tlocal %s %d %s\n", ref, sz*8, reg)
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

func spot_memcpy(of io.Writer, c *Context, dst, src spot, bytes int) {
	qwords := bytes / 8
	singles := bytes % 8

	// 8-byte scratch local for the qword copies.
	tmp := newSpot(of, c, c.Temp(), ASTType{Name: "i64"})
	for i := 0; i < qwords; i++ {
		fmt.Fprintf(of, "\tmov %s [%s+%d]\n", tmp.ref, src.ref, i*8)
		fmt.Fprintf(of, "\tmov [%s+%d] %s\n", dst.ref, i*8, tmp.ref)
	}
	tmp.free(of)
	if singles > 0 {
		tmp = newSpot(of, c, c.Temp(), ASTType{Name: "byte"})
		for i := 0; i < singles; i++ {
			fmt.Fprintf(of, "\tmovb %s [%s+%d]\n", tmp.ref, src.ref, qwords*8+i)
			fmt.Fprintf(of, "\tmovb [%s+%d] %s\n", dst.ref, qwords*8+i, tmp.ref)
		}
		tmp.free(of)
	}
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

// shapeMatch reports whether two types have the same physical representation:
// same name, indirection depth, array size, and slice flag. MutMask and
// OwnedMask are type-system annotations with no runtime effect and are ignored.
func shapeMatch(a, b ASTType) bool {
	if a.Name != b.Name ||
		a.Indirection != b.Indirection ||
		a.ArraySize != b.ArraySize {
		return false
	}
	if (a.Element == nil) != (b.Element == nil) {
		return false
	}
	if a.Element != nil && !shapeMatch(*a.Element, *b.Element) {
		return false
	}
	return true
}

// can move T to T or T into *T
func move(of io.Writer, c *Context, dest spot, src spot) {
	if dest.t == regtype || src.t == regtype {
		// We have a register. Just move it.
		fmt.Fprintf(of, "\tmov %s %s\n", dest.ref, src.ref)
		return
	}
	if src.t.Indirection == 0 && src.t.Size(c) > 8 {
		spot_memcpy(of, c, dest, src, src.t.Size(c))
		return
	}
	if shapeMatch(dest.t, src.t) {
		fmt.Fprintf(of, "\tmov %s %s\n", dest.ref, src.ref)
		return
	}
	depoint := dest.t
	depoint.Indirection--
	depoint.MutMask >>= 1
	depoint.OwnedMask >>= 1
	if shapeMatch(depoint, src.t) {
		// dest was *T and src is T
		fmt.Fprintf(of, "\tmov [%s] %s\n", dest.ref, src.ref)
		if src.t.Size(c) > 8 {
			panic("(TODO) Can't copy T into *T for T.size > 8 bytes")
		}
		return
	}

	fmt.Printf("DEST: %#v\nSRC:%#v\nDEPOINT: %#v\n", dest, src, depoint)
	panic("move TODO")
}

// compileLval compiles an AST into a spot of type a.ASTType *or* pointer to a.ASTType.
// If the type is the same as the value, it must me suitable to mov dest src.
// Otherwise, if a pointer, it must be suitable for mov [dest] src, or for larger
// types memcpy(dest, &src).
func compileLval(of io.Writer, c *Context, a AST, dest spot) spot {
	switch ast := a.(type) {
	case *Symbol:
		t := ast.ASTType(c)
		s := spot{ref: ast.Name, t: t, nameIsAddress: c.NameIsAddress(ast.Name)}
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
			CompileErrorF(a, "No such structure \"%v\"", l.t.Name)
		}
		offset, mtype := def.ByteOffset(c, ast.Member)
		mtype.Indirection += 1
		if dest.empty() {
			dest = newSpot(of, c, c.Temp(), mtype)
		}
		fmt.Fprintf(of, "\t// ONE\n")
		fmt.Fprintf(of, "\tlea %s [%s+%d]\n", dest.ref, l.ref, offset)
		return dest
	case *Deref:
		v := compileTop(of, c, ast.Val, nullspot)
		t := v.t
		if t.Indirection == 0 {
			CompileErrorF(a, "Cannot dereference non-pointer type %s", t)
		}
		// We're just going to return the pointer.
		return v
	case *Index:
		w := compileTop(of, c, ast.Val, nullspot)
		vt := ast.ASTType(c)
		max := newSpot(of, c, c.Temp(), numASTType())
		var addr spot
		if w.t.IsSlice() {
			t := vt
			t.Indirection += 1
			addr = newSpot(of, c, c.Temp(), t)
			fmt.Fprintf(of, "\tmov %s [%s]\n", addr.ref, w.ref)
			fmt.Fprintf(of, "\tmov %s [%s+8]\n", max.ref, w.ref)
		} else {
			addr = w
			fmt.Fprintf(of, "\tmov %s %d\n", max.ref, w.t.ArraySize)
		}
		lvt := vt
		lvt.Indirection += 1
		if dest.empty() {
			dest = newSpot(of, c, c.Temp(), lvt)
		}
		if ast.NAST == nil {
			// fixed offset at compile time
			off := uint64(vt.Size(c)) * ast.N
			if !w.t.IsSlice() {
				if ast.N >= uint64(w.t.ArraySize) {
					CompileErrorF(a, "Index %d greater than max for array of size %d", ast.N, w.t.ArraySize)
				}
			} else {
				l := c.Label("icheck")
				fmt.Fprintf(of, "\tcmp %s %d\n", max.ref, ast.N)
				fmt.Fprintf(of, "\tjg %s\n", l)
				fmt.Fprintf(of, "\tmov rdi %d\n", ast.N)
				fmt.Fprintf(of, "\tmov rsi %s\n", max.ref)
				fmt.Fprintf(of, "\tcall _init.index_oob\n")
				fmt.Fprintf(of, "\tlabel %s\n", l)
			}
			fmt.Fprintf(of, "\tlea %s [%s+%d]\n", dest.ref, addr.ref, off)
			return dest
		}
		base := addr
		// A name-is-address base (file-scope global, or a bytes-allocated
		// stack chunk) can't be the base of a '[name + reg*scale]' SIB
		// form: x86-64 RIP-relative addressing takes a disp32 only, and
		// stack-relative '[rbp + name.off + reg*scale]' isn't a syntax
		// bas exposes. Either way, lea the storage's address into a
		// register first.
		if base.nameIsAddress {
			addrT := vt
			addrT.Indirection++
			baseAddr := newSpot(of, c, c.Temp(), addrT)
			fmt.Fprintf(of, "\tlea %s %s\n", baseAddr.ref, base.ref)
			base = baseAddr
		}
		index := compileTop(of, c, ast.NAST, nullspot)
		scale := vt.Size(c)
		l := c.Label("icheck")
		if !index.t.Same(numASTType()) {
			itmp := newSpot(of, c, c.Temp(), numASTType())
			fmt.Fprintf(of, "\tmovzx %s %s\n", itmp.ref, index.ref)
			fmt.Fprintf(of, "\tcmp %s %s\n", itmp.ref, max.ref)
			itmp.free(of)
		} else {
			fmt.Fprintf(of, "\tcmp %s %s\n", index.ref, max.ref)
		}
		fmt.Fprintf(of, "\tjl %s\n", l)
		if index.t.Same(numASTType()) {
			fmt.Fprintf(of, "\tmov rdi %s\n", index.ref)
		} else {
			fmt.Fprintf(of, "\tmovzx rdi %s\n", index.ref)
		}
		fmt.Fprintf(of, "\tmov rsi %s\n", max.ref)
		fmt.Fprintf(of, "\tcall _init.index_oob\n")
		fmt.Fprintf(of, "\tlabel %s\n", l)
		switch scale {
		case 1, 2, 4, 8:
			fmt.Fprintf(of, "\tlea %s [%s+%s*%d]\n", dest.ref, base.ref, index.ref, scale)
		default:
			// Multi-word element: compute base + index * scale manually.
			fmt.Fprintf(of, "\tmov %s %s\n", dest.ref, index.ref)
			scaleTmp := newSpot(of, c, c.Temp(), numASTType())
			fmt.Fprintf(of, "\tmov %s %d\n", scaleTmp.ref, scale)
			fmt.Fprintf(of, "\timul %s %s\n", dest.ref, scaleTmp.ref)
			scaleTmp.free(of)
			fmt.Fprintf(of, "\tadd %s %s\n", dest.ref, base.ref)
		}
		return dest
	}
	panic(fmt.Sprintf("(LVAL) FALLTHROUGH: %#v\n", a))
}

func compileTop(of io.Writer, c *Context, a AST, dest spot) (spt spot) {
	defer func() {
		if e := recover(); e != nil {
			panic(e)
		}
		// This ensures that every spot ruturned by the compiler
		// contains the type expected to be produced by that AST.
		expect := a.ASTType(c)
		actual := spt.t
		if !expect.Same(actual) {
			if expect.Same(voidASTType()) && spt.empty() {
				// this is ok. void ASTs produce empty spots.
				return
			}
			if expect.Same(intlitASTType()) {
				// intlit expressions are compiled into whatever concrete
				// integer type the context demands.
				return
			}
			//panic(fmt.Sprintf("Expected type %s but compiler produced %s", expect, actual))
			CompileErrorF(a, "Expected type %s but compiler produced %s", expect, actual)
		}
	}()

	if dest.empty() {
		note(of, "\t// begin %#v\n", a.Note())
	} else {
		note(of, "\t// begin %#v into %v\n", a.Note(), dest.ref)
	}
	defer note(of, "\t// end %#v\n", a.Note())

	// Integer literal expressions are evaluated at compile time and emitted
	// as a single immediate mov into whatever concrete type the context provides.
	if a.ASTType(c).Same(intlitASTType()) {
		val, ok := EvalConst(c, a)
		if !ok {
			CompileErrorF(a, "Could not evaluate integer literal expression at compile time")
		}
		if dest.empty() {
			dest = newSpot(of, c, c.Temp(), numASTType())
		}
		if !litFitsIn(val, c.ResolveUnderlying(dest.t)) {
			CompileErrorF(a, "Integer literal %s does not fit in type %s", val.String(), dest.t.Name)
		}
		fmt.Fprintf(of, "\tmov %s %s\n", dest.ref, val.String())
		return dest
	}

	switch ast := a.(type) {
	case *OwnedPromotion:
		// Compile the inner expression without a typed dest (the inner type differs
		// from the promoted type). Relabel the result with the promoted type.
		// The inner variable is deliberately NOT marked as moved — this is unsafe.
		s := compileTop(of, c, ast.Val, nullspot)
		s.t = ast.ASTType(c)
		if !dest.empty() && !s.same(&dest) {
			move(of, c, dest, s)
			s.free(of)
			dest.t = ast.ASTType(c)
			return dest
		}
		return s
	case *TypeAliasDecl:
		// Already registered in toASTTop; nothing to emit.
		return nullspot
	case *StructDecl:
		c.DefineStruct(ast.TName, ast)
		// Emit the struct shape so bas can carry it into the .bo for
		// cross-package import. Other packages can then declare
		// 'mypkg.Name'-typed values, construct them via 'mypkg.Name{...}',
		// and walk their fields.
		fmt.Fprintf(of, "struct %s {\n", ast.TName)
		for _, f := range ast.Fields {
			fmt.Fprintf(of, "\t%s %s\n", f.Name, f.Type)
		}
		fmt.Fprintf(of, "}\n")
		return nullspot
	case *VarDecl:
		if c.prebound[ast.Name] {
			// Top-level var: ToAST already bound this in actx for forward
			// references. Consume the marker, then emit a bas-level global
			// instead of the function-local stack/register form.
			delete(c.prebound, ast.Name)
			emitGlobalVarDecl(of, c, a, ast)
			return nullspot
		}
		c.BindVar(a, ast.Name, ast.Type, ast.IsConst)
		s := newSpot(of, c, ast.Name, ast.Type)
		if ast.Init != nil {
			srct := ast.Init.ASTType(c)
			dstt := ast.Type
			// Ownership promotion check, unless source is an integer literal.
			if !srct.Same(intlitASTType()) {
				gained := dstt.OwnedMask &^ srct.OwnedMask
				if gained != 0 {
					if _, ok := ast.Init.(*OwnedPromotion); !ok {
						CompileErrorF(a, "Ownership promotion requires explicit owned(): initializing %s with %s", dstt, srct)
					}
				}
			}
			// Type compatibility check.
			if !srct.Same(intlitASTType()) && !srct.Same(dstt) && !dstt.MutCompatible(srct) {
				CompileErrorF(a, "Cannot initialize %s with value of type %s", dstt, srct)
			}
			// Compile the initializer. Same-type / intlit go directly into s;
			// coerced cases go via a temp.
			if srct.Same(dstt) || srct.Same(intlitASTType()) {
				val := compileTop(of, c, ast.Init, s)
				if !val.same(&s) {
					move(of, c, s, val)
					val.free(of)
				}
			} else {
				val := compileTop(of, c, ast.Init, nullspot)
				move(of, c, s, val)
				val.free(of)
			}
		}
		return nullspot
	case *FuncDecl:
		c := c.SubContext()
		retlab := c.PushRetlabel()
		defer c.PopRetlabel()
		fmt.Fprintf(of, "function %s\n", ast.Name)
		// Emit a 'type fn(...) ret' directive so cross-package importers
		// (Context.Import → parseFuncType) can read this function's
		// signature back out of the .bo. Render args and return via
		// ASTType.String(); the parser eats the same forms.
		var sig strings.Builder
		sig.WriteString("fn(")
		for i, a := range ast.Args {
			if i > 0 {
				sig.WriteString(", ")
			}
			sig.WriteString(a.Type.String())
		}
		sig.WriteString(")")
		if !ast.Return.Same(voidASTType()) {
			sig.WriteString(" ")
			sig.WriteString(ast.Return.String())
		}
		fmt.Fprintf(of, "\ttype %s\n", sig.String())
		for i, a := range ast.Args {
			fmt.Fprintf(of, "\targi %s %d %d\n", a.Name, i, a.Type.Size(c)*8)
			c.BindVar(ast, a.Name, a.Type, a.IsConst)
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
		sc := c.SubContext()
		for _, st := range ast.Body {
			note(of, "\n")
			s := compileTop(of, sc, st, nullspot)
			s.free(of)
		}
		// Scope-exit: every owned binding declared in this block must be consumed.
		for _, name := range sc.UnconsumedOwned() {
			CompileErrorF(a, "Owned binding \"%s\" goes out of scope without being consumed; call dispose() or pass it to a consuming function", name)
		}
		sc.FreeLocalVars(of)
		return nullspot
	case *Funcall:
		// Cast expression: type name used as a single-argument function.
		// Only valid for unqualified calls.
		if ast.Pkg == "" {
			if destType, ok := c.TypeByName(ast.FName); ok {
				if len(ast.Args) != 1 {
					CompileErrorF(a, "Type cast %s() requires exactly one argument", ast.FName)
				}
				return compileCast(of, c, ast.Args[0], destType, dest)
			}
		}

		decl, resolvedPkg, ok := c.FuncDeclForCall(ast.Pkg, ast.FName)
		if !ok {
			CompileErrorF(a, "No such function \"%v\"", ast.QualifiedName())
		}
		if len(ast.Args) != len(decl.Args) {
			CompileErrorF(a, "%s expected %d arguments, but was called with %d",
				ast.QualifiedName(), len(decl.Args), len(ast.Args))
		}

		argorder := setupArgs(of, c, ast, decl)

		retType := ast.ASTType(c)
		raxName := raxForType(retType)
		note(of, "\t// acquire %s for call return\n", raxName)
		rax := regSpot(of, raxName)
		rax.t = retType
		// Always emit a fully-qualified call name so the linker sees
		// unambiguous symbols. Cross-package calls use the imported
		// package's name; in-package calls use the current package's name.
		callPkg := resolvedPkg
		if callPkg == "" {
			callPkg = c.Pkgname()
		}
		fmt.Fprintf(of, "\tcall %s.%s\n", callPkg, ast.FName)
		for i := 0; i < len(ast.Args); i++ {
			note(of, "\t// release call registers\n")
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
		rax.t = regtype
		rax.free(of)
		return ret
	case *Dot:
		l := compileTop(of, c, ast.Val, nullspot)
		def, ok := c.StructDeclForName(l.t.Name)
		if !ok {
			//panic("No struct (TODO)")
			CompileErrorF(a, "No such structure \"%v\"", l.t.Name)
		}
		offset, mtype := def.ByteOffset(c, ast.Member)
		if dest.empty() {
			dest = newSpot(of, c, c.Temp(), mtype)
		}
		if typeIsMemoryBacked(c, mtype) {
			// The member is memory-backed (a struct, or any value
			// larger than 8 bytes). Keep a pointer to it rather than
			// loading the bytes, so a subsequent Dot/Index can walk
			// further. Without this, a small inner struct's value
			// would end up in a register and get dereferenced as if
			// it were a pointer on the next step.
			addrType := mtype
			addrType.Indirection++
			addr := newSpot(of, c, c.Temp(), addrType)
			fmt.Fprintf(of, "\tlea %s [%s+%d]\n", addr.ref, l.ref, offset)
			_, memberIsStruct := c.StructDeclForName(mtype.Name)
			if memberIsStruct && mtype.Size(c) <= 8 {
				// Small struct: callers expect to keep walking through
				// this address. Return the lea'd address directly with
				// the struct's type, instead of memcpy-ing the bytes
				// into a scalar dest. The spot holds an address in a
				// register, so it stays name-is-value — subsequent
				// indirect addressing through it uses register-relative
				// SIB without further materialization.
				addr.t = mtype
				return addr
			}
			spot_memcpy(of, c, dest, addr, mtype.Size(c))
			addr.free(of)
		} else {
			fmt.Fprintf(of, "\tmov %s [%s+%d]\n", dest.ref, l.ref, offset)
		}
		return dest
	case *Deref:
		v := compileTop(of, c, ast.Val, nullspot)
		t := v.t
		if t.Indirection == 0 {
			CompileErrorF(a, "Cannot dereference non-pointer type %s", t)
		}
		t.Indirection -= 1
		t.MutMask >>= 1 // consume the outermost pointer level's mut bit
		if t.Indirection == 0 && !t.IsSliceOrArray() {
			t.MutMask = 0 // plain value: MutMask is not meaningful
		}

		// When the pointer's name is an address (file-scope global, or
		// a bytes-allocated stack slot), [v.ref] would only do one
		// indirection — reading the pointer's stored value from its
		// storage. We need a second load to actually follow the
		// pointer. Materialize the pointer's value into a register
		// first; subsequent [reg] is the real dereference. For
		// register-resident pointers (the local-allocated case),
		// [v.ref] already does the one-step deref the caller wants.
		src := v.ref
		if v.nameIsAddress {
			ptmp := newSpot(of, c, c.Temp(), v.t)
			fmt.Fprintf(of, "\tmov %s [%s]\n", ptmp.ref, v.ref)
			src = ptmp.ref
			defer ptmp.free(of)
		}

		if t.Indirection > 0 {
			if dest.empty() {
				fmt.Fprintf(of, "\t// New temp for deref, type: %#v\n", t)
				dest = newSpot(of, c, c.Temp(), t)
			}
			// it's a pointer. Just copy.
			fmt.Fprintf(of, "\tmov %s [%s]\n", dest.ref, src)
		} else if t.ArraySize > 0 {
			// it's an array.
			panic("Array copying not implemented yet\n")
		} else if t.Size(c) > 8 {
			// It's a large object, meaning we need to pass it by pointer.
			v.t.Indirection -= 1
			return v
		} else {
			if dest.empty() {
				fmt.Fprintf(of, "\t// New temp for deref, type: %#v\n", t)
				dest = newSpot(of, c, c.Temp(), t)
			}
			// It's a small object. Just copy it.
			fmt.Fprintf(of, "\tmov %s [%s]\n", dest.ref, src)
		}

		return dest
	case *Address:
		if dest.empty() {
			dest = newSpot(of, c, c.Temp(), ast.ASTType(c))
		}
		// Mark the variable as volatile so bas keeps it in memory from
		// this point on, ensuring coherence with any pointer aliases.
		fmt.Fprintf(of, "\tvolatile %s\n", ast.Var)
		fmt.Fprintf(of, "\tlea %s %s\n", dest.ref, ast.Var)
		return dest
	case *Index:
		vt := ast.ASTType(c)
		w := compileTop(of, c, ast.Val, nullspot)
		max := newSpot(of, c, c.Temp(), numASTType())
		var addr spot
		if w.t.IsSlice() {
			t := vt
			t.Indirection += 1
			addr = newSpot(of, c, c.Temp(), t)
			fmt.Fprintf(of, "\tmov %s [%s]\n", addr.ref, w.ref)
			fmt.Fprintf(of, "\tmov %s [%s+8]\n", max.ref, w.ref)
		} else {
			addr = w
			fmt.Fprintf(of, "\tmov %s %d\n", max.ref, w.t.ArraySize)
		}

		if dest.empty() {
			dest = newSpot(of, c, c.Temp(), vt)
		}
		if ast.NAST == nil {
			off := uint64(vt.Size(c)) * ast.N
			if !w.t.IsSlice() {
				if ast.N >= uint64(w.t.ArraySize) {
					CompileErrorF(a, "Index %d greater than max for array of size %d", ast.N, w.t.ArraySize)
				}
			} else {
				l := c.Label("icheck")
				fmt.Fprintf(of, "\tcmp %s %d\n", max.ref, ast.N)
				fmt.Fprintf(of, "\tjg %s\n", l)
				fmt.Fprintf(of, "\tmov rdi %d\n", ast.N)
				fmt.Fprintf(of, "\tmov rsi %s\n", max.ref)
				fmt.Fprintf(of, "\tcall _init.index_oob\n")
				fmt.Fprintf(of, "\tlabel %s\n", l)
			}
			if vt.Size(c) > 8 {
				// Multi-word element (struct, slice, etc.): memcpy via a
				// computed pointer.
				elemAddrT := vt
				elemAddrT.Indirection++
				elemAddr := newSpot(of, c, c.Temp(), elemAddrT)
				fmt.Fprintf(of, "\tlea %s [%s+%d]\n", elemAddr.ref, addr.ref, off)
				spot_memcpy(of, c, dest, elemAddr, vt.Size(c))
				elemAddr.free(of)
			} else {
				fmt.Fprintf(of, "\tmov %s [%s+%d]\n", dest.ref, addr.ref, off)
			}
			return dest
		}
		// Bounds check + index normalization. Common to both small- and
		// multi-word element paths.
		base := addr
		// A name-is-address base (file-scope global, bytes-allocated
		// chunk) can't be the base of a '[name + reg*scale]' SIB form;
		// lea its address into a register first.
		if base.nameIsAddress {
			addrT := vt
			addrT.Indirection++
			baseAddr := newSpot(of, c, c.Temp(), addrT)
			fmt.Fprintf(of, "\tlea %s %s\n", baseAddr.ref, base.ref)
			base = baseAddr
		}
		index := compileTop(of, c, ast.NAST, nullspot)
		l := c.Label("icheck")
		if !index.t.Same(numASTType()) {
			itmp := newSpot(of, c, c.Temp(), numASTType())
			fmt.Fprintf(of, "\tmovzx %s %s\n", itmp.ref, index.ref)
			fmt.Fprintf(of, "\tcmp %s %s\n", itmp.ref, max.ref)
			itmp.free(of)
		} else {
			fmt.Fprintf(of, "\tcmp %s %s\n", index.ref, max.ref)
		}
		fmt.Fprintf(of, "\tjl %s\n", l)
		if index.t.Same(numASTType()) {
			fmt.Fprintf(of, "\tmov rdi %s\n", index.ref)
		} else {
			fmt.Fprintf(of, "\tmovzx rdi %s\n", index.ref)
		}
		fmt.Fprintf(of, "\tmov rsi %s\n", max.ref)
		fmt.Fprintf(of, "\tcall _init.index_oob\n")
		fmt.Fprintf(of, "\tlabel %s\n", l)

		scale := vt.Size(c)
		switch scale {
		case 1, 2, 4, 8:
			// x86 base+index*scale addressing handles these directly.
			fmt.Fprintf(of, "\tmov %s [%s+%s*%d]\n", dest.ref, base.ref, index.ref, scale)
		default:
			// Multi-word element: compute &elem and memcpy.
			elemAddrT := vt
			elemAddrT.Indirection++
			elemAddr := newSpot(of, c, c.Temp(), elemAddrT)
			// elemAddr = index * scale + base.
			fmt.Fprintf(of, "\tmov %s %s\n", elemAddr.ref, index.ref)
			scaleTmp := newSpot(of, c, c.Temp(), numASTType())
			fmt.Fprintf(of, "\tmov %s %d\n", scaleTmp.ref, scale)
			fmt.Fprintf(of, "\timul %s %s\n", elemAddr.ref, scaleTmp.ref)
			scaleTmp.free(of)
			fmt.Fprintf(of, "\tadd %s %s\n", elemAddr.ref, base.ref)
			spot_memcpy(of, c, dest, elemAddr, scale)
			elemAddr.free(of)
		}
		return dest
	case *SliceOp:
		// TODO: dest optimization
		v := compileTop(of, c, ast.Val, nullspot)
		if !v.t.IsSliceOrArray() {
			panic("Somehow slicing a non-array, non-slice")
		}
		// baset is the element type; newt is the resulting slice type.
		baset := *v.t.Element
		elem := baset
		newt := ASTType{Element: &elem}
		var addr spot
		if v.t.IsSlice() {
			addrt := baset
			addrt.Indirection += 1
			addr = newSpot(of, c, c.Temp(), addrt)
			fmt.Fprintf(of, "\tmov %s [%s]\n", addr.ref, v.ref)
		} else {
			// Fixed array: addr points at the array storage itself.
			addr = v
			addr.t = baset
			addr.t.Indirection++
		}

		var upper spot
		if ast.Upper != nil {
			upper = compileTop(of, c, ast.Upper, nullspot)
		} else {
			upper = newSpot(of, c, c.Temp(), numASTType())
			if v.t.IsSlice() {
				fmt.Fprintf(of, "\tmov %s [%s+8]\n", upper.ref, v.ref)
			} else {
				// v.t is a fixed array; use its declared size.
				fmt.Fprintf(of, "\tmov %s %d\n", upper.ref, v.t.ArraySize)
			}
		}

		if ast.Lower != nil {
			lower := compileTop(of, c, ast.Lower, nullspot)
			if baset.Size(c) > 8 {
				panic("Slicing for types > 8 bytes not implemented.")
			}
			fmt.Fprintf(of, "\tlea %s [%s+%s*%d]\n", addr.ref, addr.ref, lower.ref, baset.Size(c))
			fmt.Fprintf(of, "\tsub %s %s\n", upper.ref, lower.ref)
			lower.free(of)
		}
		newslice := newSpot(of, c, c.Temp(), newt)
		fmt.Fprintf(of, "\tmov [%s] %s\n", newslice.ref, addr.ref)
		fmt.Fprintf(of, "\tmov [%s+8] %s\n", newslice.ref, upper.ref)
		return newslice

	case *Assignment:
		// Reject assignment to const-bound variables.
		if sym, ok := ast.Target.(*Symbol); ok {
			if c.IsConst(sym.Name) {
				CompileErrorF(a, "Cannot assign to const binding \"%s\"", sym.Name)
			}
			// Re-establishment: assigning to a var binding clears the moved flag.
			c.Unmove(sym.Name)
		}
		// Reject implicit ownership promotion: if the destination has owned bits
		// that the source doesn't, the source must be wrapped in owned().
		// Integer literals are exempt — they initialize owned values without a wrapper.
		{
			dsttmp := ast.Target.ASTType(c)
			srctmp := ast.Val.ASTType(c)
			if !srctmp.Same(intlitASTType()) {
				gained := dsttmp.OwnedMask &^ srctmp.OwnedMask
				if gained != 0 {
					if _, ok := ast.Val.(*OwnedPromotion); !ok {
						CompileErrorF(a, "Ownership promotion requires explicit owned(): assigning %s to %s", srctmp, dsttmp)
					}
				}
			}
		}
		// Reject write-through on a non-mut pointer: *p = x requires p: *mut T.
		if deref, ok := ast.Target.(*Deref); ok {
			ptype := deref.Val.ASTType(c)
			if ptype.MutMask&(1<<uint(ptype.Indirection)) == 0 {
				CompileErrorF(a, "Cannot write through read-only pointer of type %s; pointer must be *mut", ptype)
			}
		}
		lv := compileLval(of, c, ast.Target, nullspot)
		srct := ast.Val.ASTType(c)
		dstt := ast.Target.ASTType(c)
		if !srct.Same(dstt) {
			if !srct.Same(intlitASTType()) {
				// Allow *mut T → *T coercion (dropping mut is always safe).
				if !dstt.MutCompatible(srct) {
					CompileErrorF(a, "Cannot assign different types %s = %s", dstt, srct)
				}
			}
			// intlit: compileTop shortcut handles range checking and code gen
			dest := newSpot(of, c, c.Temp(), dstt)
			ret := compileTop(of, c, ast.Val, dest)
			if !ret.same(&dest) {
				dest.free(of)
			}
			move(of, c, lv, ret)
			ret.free(of)
			return nullspot
		}
		// if !lv.t.Same(dstt) {
		// 	panic(fmt.Sprintf("Expected dstt (%v) != lv.t (%v)\n", dstt, lv.t))
		// }
		if lv.t.Same(srct) {
			// srctype == dsttype
			// this means the dsttype is not a pointer to the location.
			// and we can use lv as the dest in the compile call.

			// Would be nice to be able to compile *into* lv,
			// But sometimes lv is also in the Val AST, i.e.
			// n = 1 - n.
			// This (currently) can lead to n being overwritten
			// by the compiler before it is finished being used.
			// e.g.
			//  mov n 1
			//  sub n n
			val := compileTop(of, c, ast.Val, lv)
			//val := compileTop(of, c, ast.Val, nullspot)
			if !val.same(&lv) {
				move(of, c, lv, val)
				val.free(of)
			}
			return nullspot
		}

		val := compileTop(of, c, ast.Val, nullspot)
		// if val.t.Size(c) != 8 {
		// 	panic(fmt.Sprintf("CANNOT MOVE TYPES THAT ARE NOT 64 BITS YET, but have %v %d\n", val.t.Size(c), val.t))
		// 	// TODO: Need to handle type sizes
		// }
		// if it's size == 8, it doesn't matter if it's a pointer or a value,
		// we can just copy it.
		move(of, c, lv, val)
		//fmt.Fprintf(of, "\tmov [%s] %s\n", lv.ref, val.ref)
		lv.free(of)
		val.free(of)
		return nullspot
	case *IfStmt:
		v := compileTop(of, c, ast.Cond, nullspot)
		labelse := c.Label("else")
		labend := c.Label("end")
		fmt.Fprintf(of, "\ttest %s %s\n", v.ref, v.ref)
		v.free(of)
		fmt.Fprintf(of, "\tjz %s\n", labelse)

		// Snapshot owned-binding move state before branches.
		snapBefore := c.OwnedBindingsSnapshot()

		v = compileTop(of, c, ast.Then, nullspot)
		if !v.empty() {
			v.free(of)
		}
		snapAfterThen := c.OwnedBindingsSnapshot()

		fmt.Fprintf(of, "\tjmp %s\n", labend)
		fmt.Fprintf(of, "\tlabel %s\n", labelse)

		// Restore to pre-branch state for the else branch.
		c.RestoreOwnedBindings(snapBefore)

		if ast.Else != nil {
			v = compileTop(of, c, ast.Else, nullspot)
			if !v.empty() {
				v.free(of)
			}
		}
		snapAfterElse := c.OwnedBindingsSnapshot()

		// Both branches must agree on which pre-existing owned vars are consumed.
		for name, movedInThen := range snapAfterThen {
			if _, existed := snapBefore[name]; !existed {
				continue // declared inside a branch, not our concern here
			}
			movedInElse := snapAfterElse[name]
			if movedInThen != movedInElse {
				CompileErrorF(a, "Owned binding \"%s\" is consumed on one branch but not the other", name)
			}
		}
		// Apply the agreed post-branch state.
		c.RestoreOwnedBindings(snapAfterThen)

		fmt.Fprintf(of, "\tlabel %s\n", labend)
		return nullspot
	case *For:
		v := compileTop(of, c, ast.Init, nullspot)
		if !v.empty() {
			v.free(of)
		}
		// Snapshot owned state before the loop body executes.
		// Owned vars in outer scopes must not be consumed inside the loop.
		snapBeforeLoop := c.OwnedBindingsSnapshot()

		start := c.Label("for")
		end := c.PushBreakLabel()
		cont := c.PushContLabel()
		fmt.Fprintf(of, "\tlabel %s\n", start)
		v = compileTop(of, c, ast.Cond, nullspot)
		fmt.Fprintf(of, "\ttest %s %s\n", v.ref, v.ref)
		fmt.Fprintf(of, "\tjz %s\n", end)
		v.free(of)
		v = compileTop(of, c, ast.Body, nullspot)
		if !v.empty() {
			v.free(of)
		}
		fmt.Fprintf(of, "\tlabel %s\n", cont)
		v = compileTop(of, c, ast.Step, nullspot)
		if !v.empty() {
			v.free(of)
		}
		fmt.Fprintf(of, "\tjmp %s\n", start)
		fmt.Fprintf(of, "\tlabel %s\n", end)
		c.PopBreakLabel()
		c.PopContLabel()

		// Check that no pre-loop owned var was consumed inside the loop body.
		// If it was, the second iteration would enter with an invalid variable.
		snapAfterLoop := c.OwnedBindingsSnapshot()
		for name, wasMoved := range snapBeforeLoop {
			if !wasMoved && snapAfterLoop[name] {
				CompileErrorF(a, "Owned binding \"%s\" is consumed inside a loop body; this would be invalid on the second iteration", name)
			}
		}
		return nullspot
	case *Op2:
		return doOp2(of, c, ast, dest)
	case *Return:
		valType := ast.Val.ASTType(c)
		if valType.Same(intlitASTType()) {
			valType = numASTType()
		}
		raxName := raxForType(valType)
		dest := newSpotWithReg(of, c, c.Temp(), valType, raxName)
		v := compileTop(of, c, ast.Val, dest)
		if !v.same(&dest) {
			dest.free(of)
			dest = v
			CompileErrorF(a, "Return not same as dest. Should this happen?")
		}
		fmt.Fprintf(of, "\tinreg %s %s\n", dest.ref, raxName)
		sz := valType.Size(c)
		if sz == 1 || sz == 2 {
			// Writing al/ax does not clear the upper bits of rax.
			// The SysV ABI requires rax to hold the sign/zero-extended return value.
			if valType.Signed {
				fmt.Fprintf(of, "\tmovsx rax %s\n", dest.ref)
			} else {
				fmt.Fprintf(of, "\tmovzx rax %s\n", dest.ref)
			}
		}
		// sz == 4: writing eax already zeros the upper 32 bits of rax automatically.
		dest.free(of)
		c.Return(of)

		return nullspot
	case *Dispose:
		t, ok := c.TypeForVar(ast.Var)
		if !ok {
			CompileErrorF(a, "dispose: \"%s\" is not declared", ast.Var)
		}
		if !t.HasOwned() {
			CompileErrorF(a, "dispose: \"%s\" has type %s which has no owned obligation", ast.Var, t)
		}
		if c.IsMoved(ast.Var) {
			CompileErrorF(a, "dispose: \"%s\" was already moved", ast.Var)
		}
		c.Move(ast.Var)
		note(of, "\t// dispose %s — obligation satisfied, no runtime effect\n", ast.Var)
		return nullspot
	case *Continue:
		c.Continue(a, of)
		return nullspot
	case *Break:
		c.Break(of)
		return nullspot
	case *Symbol:
		if c.IsMoved(ast.Name) {
			CompileErrorF(a, "Use of \"%s\" after it was moved", ast.Name)
		}
		s := spot{ref: ast.Name, t: ast.ASTType(c), nameIsAddress: c.NameIsAddress(ast.Name)}
		if dest.same(&nullspot) {
			return s
		}
		if s.same(&dest) {
			note(of, "\t// destination is already %s\n", dest.ref)
			return s
		}
		move(of, c, dest, s)
		return dest
	case *StructLiteral:
		if dest.empty() {
			dest = newSpot(of, c, c.Temp(), ast.Type)
		}
		def, ok := c.StructDeclForName(ast.Type.Name)
		if !ok {
			CompileErrorF(a, "No such structure \"%v\"", ast.Type.Name)
		}
		for _, f := range ast.Fields {
			v := compileTop(of, c, f.Val, nullspot)
			off, _ := def.ByteOffset(c, f.Name)
			if v.t.Indirection == 0 && v.t.Size(c) > 8 {
				panic("struct literal with nested structs not implemented yet.")
			}
			fmt.Fprintf(of, "\tmov [%s+%d] %s\n", dest.ref, off, v.ref)
		}
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
			// String literals have type byte[]: a fat pointer {data_ptr, length}.
			// dest is a bytes slot (16 bytes). Use qword[] to force 64-bit stores.
			s := c.String(v)
			reg := regSpot(of, "r10")
			fmt.Fprintf(of, "\tlea r10 %s\n", s)
			fmt.Fprintf(of, "\tmov qword[%s] r10\n", dest.ref)
			fmt.Fprintf(of, "\tmov qword[%s+8] %d\n", dest.ref, len(v))
			reg.free(of)
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
	// This double-commented code and all comments within are obsolete.
	// However, I still want to revisit how I'm setting up the call registers
	// and don't want to throw this old code away yet.
	//
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
	// //
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
		param := d.Args[i].Type
		if argt.Same(intlitASTType()) {
			argt = param
		} else if !param.OwnedCompatible(argt) {
			CompileErrorF(arg, "For argument %d, expected type %v but got %v",
				i, param, argt)
		}
		// If the parameter has owned, this is a move. Check for double-move now,
		// but defer the actual marking until after the argument is compiled.
		if param.HasOwned() {
			if sym, ok := arg.(*Symbol); ok {
				if c.IsMoved(sym.Name) {
					CompileErrorF(arg, "Cannot move \"%s\": it was already moved", sym.Name)
				}
			}
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
			panic("AAAH! NULLSPOT! This should not happen. An ast that's supposed to produce a value for the call produced a null spot.\n")
		}
		if !a.same(&dest) {
			dest.free(of)
			dest = a
		}
		argspots = append(argspots, dest)
	}
	// Now that all argument values are compiled, mark any consumed owned variables.
	for i := 0; i < len(f.Args); i++ {
		if d.Args[i].Type.HasOwned() {
			if sym, ok := f.Args[i].(*Symbol); ok {
				c.Move(sym.Name)
			}
		}
	}
	for i := 0; i < len(f.Args); i++ {
		note(of, "\t// Ensuring argument %d (%v) is in register %v for call.\n",
			i, argspots[i].ref, order[i])
		argt := argspots[i].t
		if argt.Size(c) >= PTR_SIZE {
			// The >= requires some explanation here.
			// It relies on the fact that for objects of size > PTR_SIZE,
			// they are held as pointers in register, meaning we can move
			// them around as if they were 64-bit values.
			fmt.Fprintf(of, "\tinreg %s %s\n", argspots[i].ref, order[i])
		} else {
			fmt.Fprintf(of, "\tacquire %s\n", order[i])
			if argt.Signed {
				fmt.Fprintf(of, "\tmovsx %s %s\n", order[i], argspots[i].ref)
			} else {
				fmt.Fprintf(of, "\tmovzx %s %s\n", order[i], argspots[i].ref)
			}
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

// raxForType returns the rax sub-register name appropriate for the given return type.
// i8/u8/bool/byte → "al", i16/u16 → "ax", i32/u32 → "eax", everything else → "rax".
func raxForType(t ASTType) string {
	if t.Indirection > 0 || t.IsSliceOrArray() {
		return "rax"
	}
	switch t.Name {
	case "i8", "u8", "byte", "bool":
		return "al"
	case "i16", "u16":
		return "ax"
	case "i32", "u32":
		return "eax"
	default:
		return "rax"
	}
}

// mulDivRegs returns the rax/rdx equivalents for a given type size in bits.
// Used to select the right register pair for mul/div at each integer width.
func mulDivRegs(bits int) (raxName, rdxName string) {
	switch bits {
	case 32:
		return "eax", "edx"
	case 16:
		return "ax", "dx"
	default:
		return "rax", "rdx"
	}
}

// EvalConst evaluates a pure integer literal expression (no variables) using
// arbitrary-precision arithmetic. Returns (nil, false) if the expression
// contains any non-literal sub-expressions.
func EvalConst(c *Context, a AST) (*big.Int, bool) {
	switch ast := a.(type) {
	case *Literal:
		if v, ok := ast.Val.(uint64); ok {
			return new(big.Int).SetUint64(v), true
		}
		return nil, false
	case *Op2:
		fv, fok := EvalConst(c, ast.First)
		sv, sok := EvalConst(c, ast.Second)
		if !fok || !sok {
			return nil, false
		}
		result := new(big.Int)
		switch ast.Type {
		case n_add:
			result.Add(fv, sv)
		case n_sub:
			result.Sub(fv, sv)
		case n_mul:
			result.Mul(fv, sv)
		case n_div:
			if sv.Sign() == 0 {
				return nil, false
			}
			result.Quo(fv, sv)
		default:
			return nil, false
		}
		return result, true
	}
	return nil, false
}

// compileCast compiles a type cast expression: destType(srcExpr).
// Handles integer literal coercion, same-size reinterpretation, widening, and narrowing.
func compileCast(of io.Writer, c *Context, src AST, destType ASTType, dest spot) spot {
	if dest.empty() {
		dest = newSpot(of, c, c.Temp(), destType)
	}

	srcType := src.ASTType(c)

	// Integer literal: compile directly into the destination type.
	if srcType.Same(intlitASTType()) {
		val, ok := EvalConst(c, src)
		if !ok {
			panic("compileCast: could not evaluate integer literal at compile time")
		}
		underlying := c.ResolveUnderlying(destType)
		if !litFitsIn(val, underlying) {
			panic(fmt.Sprintf("compileCast: literal %s does not fit in type %s", val, destType))
		}
		fmt.Fprintf(of, "\tmov %s %s\n", dest.ref, val.String())
		return dest
	}

	// Compile the source value.
	srcSpot := compileTop(of, c, src, nullspot)

	srcUnderlying := c.ResolveUnderlying(srcType)
	dstUnderlying := c.ResolveUnderlying(destType)
	srcSize := srcUnderlying.Size(c)
	dstSize := dstUnderlying.Size(c)

	switch {
	case srcSize == dstSize:
		// Same size: just copy and relabel the type. Zero cost for same-width aliases.
		fmt.Fprintf(of, "\tmov %s %s\n", dest.ref, srcSpot.ref)
	case dstSize > srcSize:
		// Widening: sign- or zero-extend.
		// Signed 32→64: MOVSX r64, r/m32 doesn't exist; use MOVSXD instead.
		// Unsigned 32→64: MOVZX r64, r/m32 doesn't exist either, but bas
		// recognizes it as a synthetic instruction and translates it.
		if srcUnderlying.Signed && srcSize == 4 && dstSize == 8 {
			fmt.Fprintf(of, "\tmovsxd %s %s\n", dest.ref, srcSpot.ref)
		} else if srcUnderlying.Signed {
			fmt.Fprintf(of, "\tmovsx %s %s\n", dest.ref, srcSpot.ref)
		} else {
			fmt.Fprintf(of, "\tmovzx %s %s\n", dest.ref, srcSpot.ref)
		}
	default:
		// Narrowing: use the partial-of-alloc syntax to take the low N bits
		// of the source, where N is the destination size in bits.
		fmt.Fprintf(of, "\tmov %s %s:%d\n", dest.ref, srcSpot.ref, dstSize*8)
	}
	srcSpot.free(of)
	return dest
}

// litFitsIn reports whether val is in the mathematical range for type t.
// Caller should pass c.ResolveUnderlying(t) when t may be a type alias.
func litFitsIn(val *big.Int, t ASTType) bool {
	var lo, hi *big.Int
	switch t.Name {
	case "i8":
		lo, hi = big.NewInt(-128), big.NewInt(127)
	case "u8", "byte":
		lo, hi = big.NewInt(0), big.NewInt(255)
	case "i16":
		lo, hi = big.NewInt(-32768), big.NewInt(32767)
	case "u16":
		lo, hi = big.NewInt(0), big.NewInt(65535)
	case "i32":
		lo, hi = big.NewInt(-2147483648), big.NewInt(2147483647)
	case "u32":
		lo, hi = big.NewInt(0), new(big.Int).SetUint64(4294967295)
	case "i64":
		lo = new(big.Int).Neg(new(big.Int).Lsh(big.NewInt(1), 63))
		hi = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 63), big.NewInt(1))
	case "u64":
		lo = big.NewInt(0)
		hi = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 64), big.NewInt(1))
	default:
		return false
	}
	return val.Cmp(lo) >= 0 && val.Cmp(hi) <= 0
}

// resolveOperandType returns the concrete type for a binary operator's operands.
// If one side is <intlit> and the other is concrete, the concrete type wins.
// If both are <intlit>, defaults to i64.
func resolveOperandType(ft, st ASTType) ASTType {
	intlit := intlitASTType()
	if !ft.Same(intlit) {
		return ft
	}
	if !st.Same(intlit) {
		return st
	}
	return numASTType()
}

func doOp2(of io.Writer, c *Context, o *Op2, dest spot) spot {
	//if dest.empty() {
	//dest = newSpot(of, c, c.Temp(), o.ASTType(c))
	//}
	// TODO: Validate operation types
	// if !o.First.ASTType(c).Same(numASTType()) {
	// 	panic("Cannot perform comparisons on non-numeric types.")
	// }
	switch o.Type {
	case n_add:
		fst := newSpot(of, c, c.Temp(), o.ASTType(c))
		first := compileTop(of, c, o.First, fst)
		var second spot
		if o.Second.ASTType(c).Same(o.ASTType(c)) {
			second = compileTop(of, c, o.Second, nullspot)
		} else {
			// If they are different types, we must create a spot
			// with the right type so the sub operands are of the right
			// size. This is for things like var n byte; n = n - 1
			snd := newSpot(of, c, c.Temp(), o.ASTType(c))
			second = compileTop(of, c, o.Second, snd)
		}
		fmt.Fprintf(of, "\tadd %s %s\n", first.ref, second.ref)
		if dest.empty() {
			dest = first
		} else {
			move(of, c, dest, first)
			first.free(of)
		}
		second.free(of)
		return dest
		// first := compileTop(of, c, o.First, dest)
		// if first == dest {
		// 	dest = newSpot(of, c, c.Temp(), o.ASTType(c))
		// }
		// second := compileTop(of, c, o.Second, dest)
		// fmt.Fprintf(of, "\tadd %s %s\n", first.ref, second.ref)
		// return first
	case n_sub:
		fst := newSpot(of, c, c.Temp(), o.ASTType(c))
		first := compileTop(of, c, o.First, fst)
		var second spot
		if o.Second.ASTType(c).Same(o.ASTType(c)) {
			second = compileTop(of, c, o.Second, nullspot)
		} else {
			// If they are different types, we must create a spot
			// with the right type so the sub operands are of the right
			// size. This is for things like var n byte; n = n - 1
			snd := newSpot(of, c, c.Temp(), o.ASTType(c))
			second = compileTop(of, c, o.Second, snd)
		}
		fmt.Fprintf(of, "\tsub %s %s\n", first.ref, second.ref)
		if dest.empty() {
			dest = first
		} else {
			move(of, c, dest, first)
			first.free(of)
		}
		second.free(of)
		return dest
		// first := compileTop(of, c, o.First, dest)
		// if first == dest {
		// 	dest = newSpot(of, c, c.Temp(), o.ASTType(c))
		// }
		// second := compileTop(of, c, o.Second, dest)
		// fmt.Fprintf(of, "\tsub %s %s\n", first.ref, second.ref)
		// return first
	case n_mul:
		ot := o.ASTType(c)
		sz := ot.Size(c)
		if sz == 1 {
			CompileErrorF(o.First, "8-bit multiplication not supported")
		}
		raxName, rdxName := mulDivRegs(sz * 8)
		signed := ot.Signed

		// Always compute into a fresh temporary, never into the caller's dest
		// directly. This avoids 'inreg' on user variables (which breaks volatile).
		// The result is moved into dest at the end if needed.
		var result spot
		if signed && sz == 8 {
			// 64-bit signed: two-operand IMUL r64, r/m64.
			// No implicit rax dependency, no inreg on user variables.
			// Second operand can be register or memory — bas picks the form.
			tmp := newSpot(of, c, c.Temp(), ot)
			first := compileTop(of, c, o.First, tmp)
			if !first.same(&tmp) {
				move(of, c, tmp, first)
				first.free(of)
			}
			second := compileTop(of, c, o.Second, nullspot)
			fmt.Fprintf(of, "\timul %s %s\n", tmp.ref, second.ref)
			second.free(of)
			result = tmp
		} else {
			// For sub-64-bit signed and all unsigned: one-operand form via rax.
			// Load first into a fresh rax-pinned temp (uses 'mov', not 'inreg').
			// Then inreg the temp (safe: fresh temp is never volatile).
			tmp := newSpotWithReg(of, c, c.Temp(), ot, raxName)
			first := compileTop(of, c, o.First, tmp)
			if !first.same(&tmp) {
				move(of, c, tmp, first)
				first.free(of)
			}
			second := compileTop(of, c, o.Second, nullspot)
			// Ensure first is in rax; compiling second may have evicted it.
			// Safe: tmp is a fresh Temp, never volatile.
			fmt.Fprintf(of, "\tinreg %s %s\n", tmp.ref, raxName)
			// Acquire the rdx-equivalent to protect any live variable there.
			// One-operand MUL/IMUL writes the high half to rdx as a side effect.
			rdx := regSpot(of, rdxName)
			if signed {
				fmt.Fprintf(of, "\timul %s\n", second.ref)
			} else {
				fmt.Fprintf(of, "\tmul %s\n", second.ref)
			}
			rdx.free(of)
			second.free(of)
			result = tmp
		}
		if !dest.empty() && !result.same(&dest) {
			move(of, c, dest, result)
			result.free(of)
			return dest
		}
		return result
	case n_div:
		ot := o.ASTType(c)
		sz := ot.Size(c)
		if sz == 1 {
			CompileErrorF(o.First, "8-bit division not supported")
		}
		raxName, rdxName := mulDivRegs(sz * 8)
		signed := ot.Signed

		// Always compute into a fresh rax-pinned temporary (never the caller's
		// dest). compileTop loads the dividend via 'mov rax src', which handles
		// the memory form for volatile variables without needing 'inreg'.
		tmp := newSpotWithReg(of, c, c.Temp(), ot, raxName)
		first := compileTop(of, c, o.First, tmp)
		if !first.same(&tmp) {
			move(of, c, tmp, first)
			first.free(of)
		}
		second := compileTop(of, c, o.Second, nullspot)
		// Ensure the dividend is in rax before the division instruction.
		// Compiling the second operand (e.g. a funcall) may have evicted tmp
		// from rax to its stack slot. inreg reloads it. tmp is always a fresh
		// Temp (never volatile), so this is safe.
		fmt.Fprintf(of, "\tinreg %s %s\n", tmp.ref, raxName)
		rdx := regSpot(of, rdxName)
		if signed {
			switch sz {
			case 8:
				fmt.Fprintf(of, "\tcqo\n")
			case 4:
				fmt.Fprintf(of, "\tcdq\n")
			case 2:
				fmt.Fprintf(of, "\tcwd\n")
			}
			fmt.Fprintf(of, "\tidiv %s\n", second.ref)
		} else {
			fmt.Fprintf(of, "\txor %s %s\n", rdxName, rdxName)
			fmt.Fprintf(of, "\tdiv %s\n", second.ref)
		}
		rdx.free(of)
		second.free(of)
		if !dest.empty() && !tmp.same(&dest) {
			move(of, c, dest, tmp)
			tmp.free(of)
			return dest
		}
		return tmp
	case n_lt:
		ft := resolveOperandType(o.First.ASTType(c), o.Second.ASTType(c))
		fst := newSpot(of, c, c.Temp(), ft)
		first := compileTop(of, c, o.First, fst)
		var second spot
		if o.Second.ASTType(c).Same(ft) {
			second = compileTop(of, c, o.Second, nullspot)
		} else {
			snd := newSpot(of, c, c.Temp(), ft)
			second = compileTop(of, c, o.Second, snd)
		}
		if dest.empty() {
			dest = newSpot(of, c, c.Temp(), boolASTType())
		}
		fmt.Fprintf(of, "\tcmp %s %s\n", first.ref, second.ref)
		if ft.Signed {
			fmt.Fprintf(of, "\tsetl %s\n", dest.ref)
		} else {
			fmt.Fprintf(of, "\tsetb %s\n", dest.ref)
		}
		return dest
	case n_le:
		ft := resolveOperandType(o.First.ASTType(c), o.Second.ASTType(c))
		fst := newSpot(of, c, c.Temp(), ft)
		first := compileTop(of, c, o.First, fst)
		var second spot
		if o.Second.ASTType(c).Same(ft) {
			second = compileTop(of, c, o.Second, nullspot)
		} else {
			snd := newSpot(of, c, c.Temp(), ft)
			second = compileTop(of, c, o.Second, snd)
		}
		if dest.empty() {
			dest = newSpot(of, c, c.Temp(), boolASTType())
		}
		fmt.Fprintf(of, "\tcmp %s %s\n", first.ref, second.ref)
		if ft.Signed {
			fmt.Fprintf(of, "\tsetle %s\n", dest.ref)
		} else {
			fmt.Fprintf(of, "\tsetbe %s\n", dest.ref)
		}
		return dest
	case n_gt:
		ft := resolveOperandType(o.First.ASTType(c), o.Second.ASTType(c))
		fst := newSpot(of, c, c.Temp(), ft)
		first := compileTop(of, c, o.First, fst)
		var second spot
		if o.Second.ASTType(c).Same(ft) {
			second = compileTop(of, c, o.Second, nullspot)
		} else {
			snd := newSpot(of, c, c.Temp(), ft)
			second = compileTop(of, c, o.Second, snd)
		}
		if dest.empty() {
			dest = newSpot(of, c, c.Temp(), boolASTType())
		}
		fmt.Fprintf(of, "\tcmp %s %s\n", first.ref, second.ref)
		if ft.Signed {
			fmt.Fprintf(of, "\tsetg %s\n", dest.ref)
		} else {
			fmt.Fprintf(of, "\tseta %s\n", dest.ref)
		}
		return dest
	case n_ge:
		ft := resolveOperandType(o.First.ASTType(c), o.Second.ASTType(c))
		fst := newSpot(of, c, c.Temp(), ft)
		first := compileTop(of, c, o.First, fst)
		var second spot
		if o.Second.ASTType(c).Same(ft) {
			second = compileTop(of, c, o.Second, nullspot)
		} else {
			snd := newSpot(of, c, c.Temp(), ft)
			second = compileTop(of, c, o.Second, snd)
		}
		if dest.empty() {
			dest = newSpot(of, c, c.Temp(), boolASTType())
		}
		fmt.Fprintf(of, "\tcmp %s %s\n", first.ref, second.ref)
		if ft.Signed {
			fmt.Fprintf(of, "\tsetge %s\n", dest.ref)
		} else {
			fmt.Fprintf(of, "\tsetae %s\n", dest.ref)
		}
		return dest
	case n_deq:
		ft := resolveOperandType(o.First.ASTType(c), o.Second.ASTType(c))
		fst := newSpot(of, c, c.Temp(), ft)
		first := compileTop(of, c, o.First, fst)
		var second spot
		if o.Second.ASTType(c).Same(ft) {
			snd := newSpot(of, c, c.Temp(), ft)
			second = compileTop(of, c, o.Second, snd)
		} else {
			snd := newSpot(of, c, c.Temp(), ft)
			second = compileTop(of, c, o.Second, snd)
		}
		if dest.empty() {
			dest = newSpot(of, c, c.Temp(), boolASTType())
		}
		fmt.Fprintf(of, "\tcmp %s %s\n", first.ref, second.ref)
		fmt.Fprintf(of, "\tsete %s\n", dest.ref)
		return dest
	case n_neq:
		ft := resolveOperandType(o.First.ASTType(c), o.Second.ASTType(c))
		fst := newSpot(of, c, c.Temp(), ft)
		first := compileTop(of, c, o.First, fst)
		var second spot
		if o.Second.ASTType(c).Same(ft) {
			second = compileTop(of, c, o.Second, nullspot)
		} else {
			snd := newSpot(of, c, c.Temp(), ft)
			second = compileTop(of, c, o.Second, snd)
		}
		if dest.empty() {
			dest = newSpot(of, c, c.Temp(), boolASTType())
		}
		fmt.Fprintf(of, "\tcmp %s %s\n", first.ref, second.ref)
		fmt.Fprintf(of, "\tsetne %s\n", dest.ref)
		return dest
	case n_booland:
		first := compileTop(of, c, o.First, nullspot)
		second := compileTop(of, c, o.Second, nullspot)
		if dest.empty() {
			dest = newSpot(of, c, c.Temp(), boolASTType())
		}
		fbool := newSpot(of, c, c.Temp(), byteASTType())
		sbool := newSpot(of, c, c.Temp(), byteASTType())
		fmt.Fprintf(of, "\ttest %s %s\n", first.ref, first.ref)
		fmt.Fprintf(of, "\tseta %s\n", fbool.ref)

		fmt.Fprintf(of, "\ttest %s %s\n", second.ref, second.ref)
		fmt.Fprintf(of, "\tseta %s\n", sbool.ref)

		fmt.Fprintf(of, "\tand %s %s\n", fbool.ref, sbool.ref)
		fmt.Fprintf(of, "\tseta %s\n", dest.ref)
		fbool.free(of)
		sbool.free(of)
		first.free(of)
		second.free(of)
		return dest
	case n_boolor:
		first := compileTop(of, c, o.First, nullspot)
		second := compileTop(of, c, o.Second, nullspot)
		if dest.empty() {
			dest = newSpot(of, c, c.Temp(), boolASTType())
		}
		fbool := newSpot(of, c, c.Temp(), byteASTType())
		sbool := newSpot(of, c, c.Temp(), byteASTType())
		fmt.Fprintf(of, "\ttest %s %s\n", first.ref, first.ref)
		fmt.Fprintf(of, "\tseta %s\n", fbool.ref)

		fmt.Fprintf(of, "\ttest %s %s\n", second.ref, second.ref)
		fmt.Fprintf(of, "\tseta %s\n", sbool.ref)

		fmt.Fprintf(of, "\tor %s %s\n", fbool.ref, sbool.ref)
		fmt.Fprintf(of, "\tseta %s\n", dest.ref)
		fbool.free(of)
		sbool.free(of)
		first.free(of)
		second.free(of)
		return dest
	}
	panic("Could not do op\n")
}
