package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/davecgh/go-spew/spew"
	"github.com/knusbaum/gbasm"
)

type structField struct {
	typename string
	offset   int
}

type structType struct {
	T      string
	fields map[string]*structField
}

func (t *structType) FieldOffset(name string) (int, bool) {
	if f, ok := t.fields[name]; ok {
		return f.offset, true
	}
	return 0, false
}

type CompileContext struct {
	o *gbasm.OFile
	f *gbasm.Function

	strngs    map[string]string
	types     map[string]*structType
	registers *gbasm.Registers
	labeli    int
	tempi     int
	retlabs   []string
	bindings  map[string]*typename
}

func NewCompileContext() *CompileContext {
	return &CompileContext{
		strngs:    make(map[string]string),
		types:     make(map[string]*structType),
		registers: gbasm.NewRegisters(),
		bindings:  map[string]*typename{},
	}
}

// Returns the type of thing that's being
func (ctx *CompileContext) typeOf(n *Node) (*typename, bool) {
	if n.t == n_symbol {
		t, ok := ctx.bindings[n.sval]
		return t, ok
	}
	if n.t == n_dot {
		t, ok := ctx.typeOf(n.args[0])
		if !ok {
			return nil, false
		}
		// Have type of parent struct
		ts, ok := ctx.types[t.name]
		if !ok {
			panic(fmt.Sprintf("No such struct %s", t.name))
			return nil, false
		}
		field, ok := ts.fields[n.args[1].sval]
		if !ok {
			panic(fmt.Sprintf("No such field name %s for struct %s", t.name, n.args[1].sval))
			return nil, false
		}
		//TODO: This is wrong and won't work with indirection.
		return &typename{name: field.typename}, true
	}
	spew.Dump(n)
	panic(fmt.Sprintf("Cannot determine type for %#v\n", n))
}

func (c *CompileContext) WriteStrings(of io.Writer) {
	for k, s := range c.strngs {
		fmt.Fprintf(of, "var %s string \"%s\\0\"\n", s, unparseString(k))
	}
}

func (c *CompileContext) addStructDef(s *structType) error {
	if _, ok := c.types[s.T]; ok {
		return fmt.Errorf("struct %s already defined.\n", s.T)
	}
	c.types[s.T] = s
	return nil
}

func (c *CompileContext) getStructDef(name string) (*structType, bool) {
	v, ok := c.types[name]
	return v, ok
}

// returns the size in bytes of the type
func (c *CompileContext) typeSize(name string) (int, bool) {
	switch name {
	case "num":
		return 8, true
	case "str":
		return 8, true // pointer
	case "void":
		return 0, true
	default:
		if st, ok := c.types[name]; ok {
			size := 0
			for _, ft := range st.fields {
				s, ok := c.typeSize(ft.typename)
				if !ok {
					return 0, false
				}
				size += s
			}
			return size, true
		}
		return 0, false
	}
}

func (c *CompileContext) String(s string) string {
	r, ok := c.strngs[s]
	if !ok {
		i := len(c.strngs)
		r = fmt.Sprintf("__bstr%d", i)
		c.strngs[s] = r
	}
	return r
}

func (c *CompileContext) Label() string {
	c.labeli++
	return fmt.Sprintf("_LABEL%d", c.labeli)
}

// Temp declares a temporary 64-bit val
func (c *CompileContext) Temp(of io.Writer) val {
	c.tempi++
	t := fmt.Sprintf("_T%d", c.tempi)
	fmt.Fprintf(of, "\tlocal %s 64\n", t)
	return regval(t)
}

// TempB declares a temporary byte
func (c *CompileContext) TempB(of io.Writer) val {
	c.tempi++
	t := fmt.Sprintf("_T%d", c.tempi)
	fmt.Fprintf(of, "\tlocal %s 8\n", t)
	return regval(t)
}

func (c *CompileContext) TempBytes(size int, of io.Writer) val {
	c.tempi++
	t := fmt.Sprintf("_T%d", c.tempi)
	fmt.Fprintf(of, "\tbytes %s %d\n", t, size)
	return regval(t)
}

func (c *CompileContext) Return(of io.Writer) {
	fmt.Fprintf(of, "\tjmp %s\n", c.retlabs[len(c.retlabs)-1])
}

func release(ctx *CompileContext, of io.Writer, s val) val {
	s2 := strings.ToUpper(s.ref)
	if r, err := gbasm.ParseReg(s2); err == nil {
		ctx.registers.Release(r)
	} else if strings.HasPrefix(s2, "_T") {
		fmt.Fprintf(of, "\tforget %s\n", s2)
	}
	return s
}

func (n *Node) createStruct(ctx *CompileContext) Value {
	name := n.sval
	m := make(map[string]*structField)
	off := 0
	for _, field := range n.args {
		fieldname := field.sval
		fieldtype := field.args[0].sval
		s, ok := ctx.typeSize(fieldtype)
		if !ok {
			panic(fmt.Sprintf("Don't know the size of type %s", fieldtype))
		}
		m[fieldname] = &structField{typename: fieldtype, offset: off}
		off += s
	}
	t := structType{
		T:      name,
		fields: m,
	}
	err := ctx.addStructDef(&t)
	if err != nil {
		panic(&interpreterError{msg: err.Error(), p: n.p})
	}
	return Value{}
}

func (n *Node) replaceStringsStructLiteral(ctx *CompileContext) {
	for _, field := range n.args {
		field.args[0].replaceStrings(ctx)
	}
}

func (n *Node) replaceStrings(ctx *CompileContext) {
	if n.t == n_str {
		n.sval = ctx.String(n.sval)
	}
	for _, arg := range n.args {
		arg.replaceStrings(ctx)
	}
	return
}

func setupArgs(ctx *CompileContext, of io.Writer, args []*Node) {
	fmt.Fprintf(of, "\tevict\n")
	for i := 0; i < len(args); i++ {
		//a := args[i].compile(ctx, of)
		var a val
		switch i {
		case 0:
			a = args[i].compile(ctx, of, "rdi")
			if a.ref != "rdi" {
				//fmt.Fprintf(of, "//// CHECK THIS\n")
				a.LoadInto(ctx, of, "rdi")
			} else {
				//fmt.Fprintf(of, "//// EXCLUDED \n")
				//fmt.Fprintf(of, "//")
				//a.LoadInto(ctx, of, "rdi")
			}
		case 1:
			a = args[i].compile(ctx, of, "rsi")
			if a.ref != "rsi" {
				//fmt.Fprintf(of, "//// CHECK THIS\n")
				a.LoadInto(ctx, of, "rsi")
			} else {
				// 				fmt.Fprintf(of, "//// EXCLUDED \n")
				// 				fmt.Fprintf(of, "//")
				// 				a.LoadInto(ctx, of, "rsi")
			}
		case 2:
			a = args[i].compile(ctx, of, "rdx")
			if a.ref != "rdx" {
				// 				fmt.Fprintf(of, "//// CHECK THIS\n")
				a.LoadInto(ctx, of, "rdx")
			} else {
				// 				fmt.Fprintf(of, "//// EXCLUDED \n")
				// 				fmt.Fprintf(of, "//")
				// 				a.LoadInto(ctx, of, "rdx")
			}
		case 3:
			a = args[i].compile(ctx, of, "rcx")
			if a.ref != "rcx" {
				//				fmt.Fprintf(of, "//// CHECK THIS\n")
				a.LoadInto(ctx, of, "rcx")
			} else {
				// 				fmt.Fprintf(of, "//// EXCLUDED \n")
				// 				fmt.Fprintf(of, "//")
				// 				a.LoadInto(ctx, of, "rcx")
			}
		case 4:
			a = args[i].compile(ctx, of, "r8")
			if a.ref != "r8" {
				//				fmt.Fprintf(of, "//// CHECK THIS\n")
				a.LoadInto(ctx, of, "r8")
			} else {
				// 				fmt.Fprintf(of, "//// EXCLUDED \n")
				// 				fmt.Fprintf(of, "//")
				// 				a.LoadInto(ctx, of, "r8")
			}
		case 5:
			a = args[i].compile(ctx, of, "r9")
			if a.ref != "r9" {
				//				fmt.Fprintf(of, "//// CHECK THIS\n")
				a.LoadInto(ctx, of, "r9")
			} else {
				// 				fmt.Fprintf(of, "//// EXCLUDED \n")
				// 				fmt.Fprintf(of, "//")
				// 				a.LoadInto(ctx, of, "r9")
			}
		default:
			panic("TOO MANY ARGS\n")
		}
		release(ctx, of, a)
	}
}

// func (n *Node) printerVal() string {
// 	switch n.t {
// 	case n_number:
// 		return fmt.Sprintf("%d", int(n.nval))
// 	case n_str:
// 		return n.sval
// 	case n_symbol:
// 		return n.sval
// 	default:
// 		spew.Dump(n)
// 		panic(fmt.Sprintf("Cannot print value for node %v\n", n))
// 	}
// }

type vtype int

const (
	v_none vtype = iota
	v_num
	v_str
	v_reg
)

type val struct {
	ref string
	t   vtype
}

func (v val) LoadInto(ctx *CompileContext, of io.Writer, s string) {
	switch v.t {
	case v_str:
		fmt.Fprintf(of, "\tlea %s %s\n", s, v.ref)
	case v_num:
		fmt.Fprintf(of, "\tmov %s %s\n", s, v.ref)
	case v_reg:
		fmt.Fprintf(of, "\tmov %s %s\n", s, v.ref)
	default:
		panic(fmt.Sprintf("CANNOT LOAD %v\n", v))
	}
}

func numval(n int) val {
	return val{ref: fmt.Sprintf("%d", n), t: v_num}
}

func strval(s string) val {
	return val{ref: s, t: v_str}
}

func regval(s string) val {
	return val{ref: s, t: v_reg}
}

func noval() val {
	return val{}
}

func (n *Node) compileEQDOT(ctx *CompileContext, of io.Writer, prefreg string) val {
	// 	var t1 val
	// 	if prefreg != "" {
	// 		t1 = regval(prefreg)
	// 	} else {
	// 		t1 = ctx.Temp(of)
	// 	}
	// 	fmt.Fprintf(of, "\tmov %s %s\n", t1.ref, n.args[0].sval)
	// 	t, ok := ctx.bindings[n.args[0].sval]
	// 	if !ok {
	// 		panic(fmt.Sprintf("%s not bound to struct type.", n.args[0].sval))
	// 	}
	// 	st, ok := ctx.getStructDef(t.name)
	// 	if !ok {
	// 		panic(fmt.Sprintf("failed to find struct definition for %s", t.name))
	// 	}
	// 	ft, ok := st.fields[n.args[1].sval]
	// 	if !ok {
	// 		panic(fmt.Sprintf("failed to find field definition for %s.%s", t.name, n.args[1].sval))
	// 	}
	// 	//fmt.Printf("FT: %#v\n", ft)
	// 	fmt.Fprintf(of, "\tadd %s %d\n", t1.ref, ft.offset)
	// 	//panic("NOT IMPLEMENTED")
	// 	return t1

	// Get the struct type of the thing we're getting the field from
	t, ok := ctx.typeOf(n.args[0])
	if !ok {
		panic(fmt.Sprintf("%#v not bound to struct type.", n.args[0]))
	}

	// Get the struct definition and the field definition
	st, ok := ctx.getStructDef(t.name)
	if !ok {
		panic(fmt.Sprintf("failed to find struct definition for %s", t.name))
	}
	ft, ok := st.fields[n.args[1].sval]
	if !ok {
		panic(fmt.Sprintf("failed to find field definition for %s.%s", t.name, n.args[1].sval))
	}

	// Compile the thing we're getting the field from into a ref. This can be just a symbol if it's a variable, or an
	// expression that yields a struct.
	v1 := n.args[0].compile(ctx, of, "")
	if v1.ref == "" {
		panic("OK")
	}

	var t1 val
	if prefreg != "" {
		t1 = regval(prefreg)
	} else {
		t1 = ctx.Temp(of)
	}
	fmt.Printf("Assigning to field %s of type %s in struct %s\n", n.args[1].sval, ft.typename, t.name)
	// return a reference.
	fmt.Fprintf(of, "\tlea %s [%s+%d]\n", t1.ref, v1.ref, ft.offset)

	release(ctx, of, v1)
	return t1
}

func (n *Node) compile(ctx *CompileContext, of io.Writer, prefreg string) val {
	switch n.t {
	case n_number:
		return numval(int(n.nval)) //fmt.Sprintf("%d", int(n.nval))
	case n_str:
		//panic("NOT IMPLEMENTED")
		return strval(n.sval)
	case n_symbol:
		//panic("NOT IMPLEMENTED")
		return regval(n.sval)
	case n_funcall:
		setupArgs(ctx, of, n.args)
		if !ctx.registers.Use(gbasm.R_RAX) {
			panic(fmt.Sprintf("Could not acquire RAX for return value.\n"))
		}
		fmt.Fprintf(of, "\tcall %s\n", n.sval)
		return regval("rax")
	case n_index:
		panic("NOT IMPLEMENTED")
	case n_struct:
		n.createStruct(ctx)
		return noval()
	case n_stlit:
		//spew.Dump(n)
		//fmt.Printf("Compiling struct literal into %s\n", prefreg)
		if prefreg == "" {
			panic(fmt.Sprintf("Cannot compile struct literal into blank register/var"))
		}

		st, ok := ctx.getStructDef(n.sval)
		if !ok {
			panic(fmt.Sprintf("failed to find struct definition for %s", n.sval))
		}
		t1 := ctx.Temp(of)
		for _, f := range n.args {
			field, ok := st.fields[f.sval]
			if !ok {
				panic(fmt.Sprintf("no such field %s", f.sval))
			}
			// TODO: Can optimize away t1
			fmt.Fprintf(of, "\tlea %s [%s+%d]\n", t1.ref, prefreg, field.offset)
			v := f.args[0].compile(ctx, of, t1.ref)
			if v != t1 {
				//fmt.Fprintf(of, "\tmov [%s+%d] %s\n", prefreg, field.offset, v.ref)
				v.LoadInto(ctx, of, fmt.Sprintf("[%s+%d]", prefreg, field.offset))
				release(ctx, of, v)
			}
		}
		release(ctx, of, t1)
		return regval(prefreg)
		//panic(fmt.Sprintf("NOT IMPLEMENTED: instantiate struct literal %s into %s", n.sval, prefreg))
	case n_stfield:
		panic("n_stfield NOT IMPLEMENTED")
	case n_fn:
		retlab := ctx.Label()
		ctx.retlabs = append(ctx.retlabs, retlab)
		fmt.Fprintf(of, "function %s\n", n.sval)
		for i := 0; i < int(n.nval); i++ {
			fmt.Fprintf(of, "\targi %s %d\n", n.args[i].sval, i)
		}
		fmt.Fprintf(of, "\n\tprologue\n\n")
		for _, arg := range n.args[int(n.nval+1):] {
			release(ctx, of, arg.compile(ctx, of, ""))
		}
		fmt.Fprintf(of, "\n\tlabel %s\n", retlab)
		fmt.Fprintf(of, "\tepilogue\n")
		fmt.Fprintf(of, "\tret\n\n")
		ctx.retlabs = ctx.retlabs[:len(ctx.retlabs)-1]
		return noval()
	case n_arg:
		panic("NOT IMPLEMENTED")
	case n_dot:

		// Get the struct type of the thing we're getting the field from
		t, ok := ctx.typeOf(n.args[0])
		if !ok {
			panic(fmt.Sprintf("%#v not bound to struct type.", n.args[0]))
		}

		// Get the struct definition and the field definition
		st, ok := ctx.getStructDef(t.name)
		if !ok {
			panic(fmt.Sprintf("failed to find struct definition for %s", t.name))
		}
		ft, ok := st.fields[n.args[1].sval]
		if !ok {
			panic(fmt.Sprintf("failed to find field definition for %s.%s", t.name, n.args[1].sval))
		}

		// Compile the thing we're getting the field from into a ref. This can be just a symbol if it's a variable, or an
		// expression that yields a struct.
		v1 := n.args[0].compile(ctx, of, "")
		if v1.ref == "" {
			panic("OK")
		}

		var t1 val
		if prefreg != "" {
			t1 = regval(prefreg)
		} else {
			t1 = ctx.Temp(of)
		}
		fmt.Printf("Assigning to field %s of type %s in struct %s\n", n.args[1].sval, ft.typename, t.name)
		// TODO: much better handling of types here
		if _, ok := ctx.getStructDef(ft.typename); ok {
			// This field is another struct, return a reference.
			fmt.Fprintf(of, "\tlea %s [%s+%d]\n", t1.ref, v1.ref, ft.offset)
		} else {
			fmt.Fprintf(of, "\tmov %s [%s+%d]\n", t1.ref, v1.ref, ft.offset)
		}

		release(ctx, of, v1)
		return t1
	case n_mul:
		panic("NOT IMPLEMENTED")
	case n_div:
		panic("NOT IMPLEMENTED")
	case n_add:
		//r, ok := ctx.registers.Get(64)
		//if !ok {
		//	panic("Out of registers.\n")
		//}
		//rs := strings.ToLower(r.String())
		var t1 val
		if prefreg != "" {
			t1 = regval(prefreg)
		} else {
			t1 = ctx.Temp(of)
		}
		//t1 := ctx.Temp(of)
		one := n.args[0].compile(ctx, of, t1.ref)
		if one.t != v_num && one.t != v_reg {
			panic("CANNOT ADD NON-NUMBERS\n")
		}
		if one != t1 {
			fmt.Fprintf(of, "\tmov %s %s\n", t1.ref, one.ref)
		}
		//one.LoadInto(t1)
		release(ctx, of, one)

		two := n.args[1].compile(ctx, of, "")
		fmt.Fprintf(of, "\tadd %s %s\n", t1.ref, two.ref)
		release(ctx, of, two)
		return t1
	case n_sub:
		// 		r, ok := ctx.registers.Get(64)
		// 		if !ok {
		// 			panic("Out of registers.\n")
		// 		}
		// 		rs := strings.ToLower(r.String())
		var t1 val
		if prefreg != "" {
			t1 = regval(prefreg)
		} else {
			t1 = ctx.Temp(of)
		}
		one := n.args[0].compile(ctx, of, t1.ref)
		if one.t != v_num && one.t != v_reg {
			panic("CANNOT ADD NON-NUMBERS\n")
		}
		if one != t1 {
			fmt.Fprintf(of, "\tmov %s %s\n", t1.ref, one.ref)
		}
		release(ctx, of, one)

		two := n.args[1].compile(ctx, of, "")
		fmt.Fprintf(of, "\tsub %s %s\n", t1.ref, two.ref)
		release(ctx, of, two)
		return t1
	case n_eq:
		//panic("NOT IMPLEMENTED")
		//v := n.args[0].compile(ctx, of, "")
		if n.args[0].t != n_symbol && n.args[0].t != n_dot {
			panic("Can only assign to variables.\n")
		}
		//fmt.Fprintf(of, "\tlocal %s 64\n", n.args[0].sval)
		//fmt.Fprintf(of, "\tmov %s %s\n", t1.ref, v.ref)
		if n.args[0].t == n_dot {
			//v := n.args[1].compile(ctx, of, "")
			dst := n.args[0].compileEQDOT(ctx, of, "")
			fmt.Printf("Compiling %s.%s -> %s\n", n.args[0].args[0].sval, n.args[0].args[1].sval, dst.ref)
			v := n.args[1].compile(ctx, of, dst.ref)
			fmt.Printf("COMPILED RESULT INTO %s\n", v.ref)
			if v != dst {
				fmt.Fprintf(of, "\tmov [%s] %s\n", dst.ref, v.ref)
			}
			release(ctx, of, v)
			release(ctx, of, dst)
		} else if n.args[0].t == n_symbol {
			v := n.args[1].compile(ctx, of, n.args[0].sval)
			//fmt.Fprintf(of, "// COMPARE %#v, %s\n", v, n.args[0].sval)
			if v.ref != n.args[0].sval {
				v.LoadInto(ctx, of, n.args[0].sval)
				release(ctx, of, v)
			}
		}
		return noval()
	case n_deq:
		panic("NOT IMPLEMENTED")
	case n_lt:
		var t1 val
		if prefreg != "" {
			t1 = regval(prefreg)
		} else {
			t1 = ctx.Temp(of)
		}
		one := n.args[0].compile(ctx, of, t1.ref)
		//fmt.Fprintf(of, "//// CHECK THIS\n")
		fmt.Fprintf(of, "\tmov %s %s\n", t1.ref, one.ref)
		release(ctx, of, one)

		two := n.args[1].compile(ctx, of, "")
		fmt.Fprintf(of, "\tcmp %s %s\n", t1.ref, two.ref)
		release(ctx, of, two)
		sgt := ctx.TempB(of)
		fmt.Fprintf(of, "\tsetl %s\n", sgt.ref)
		fmt.Fprintf(of, "\tmovzx %s %s\n", t1.ref, sgt.ref)
		release(ctx, of, sgt)
		return t1
	case n_gt:
		var t1 val
		if prefreg != "" {
			t1 = regval(prefreg)
		} else {
			t1 = ctx.Temp(of)
		}
		one := n.args[0].compile(ctx, of, t1.ref)
		//fmt.Fprintf(of, "//// CHECK THIS\n")
		fmt.Fprintf(of, "\tmov %s %s\n", t1.ref, one.ref)
		release(ctx, of, one)

		two := n.args[1].compile(ctx, of, "")
		fmt.Fprintf(of, "\tcmp %s %s\n", t1.ref, two.ref)
		release(ctx, of, two)
		sgt := ctx.TempB(of)
		fmt.Fprintf(of, "\tsetg %s\n", sgt.ref)
		fmt.Fprintf(of, "\tmovzx %s %s\n", t1.ref, sgt.ref)
		release(ctx, of, sgt)
		return t1
	case n_le:
		var t1 val
		if prefreg != "" {
			t1 = regval(prefreg)
		} else {
			t1 = ctx.Temp(of)
		}
		one := n.args[0].compile(ctx, of, t1.ref)
		//fmt.Fprintf(of, "//// CHECK THIS\n")
		fmt.Fprintf(of, "\tmov %s %s\n", t1.ref, one.ref)
		release(ctx, of, one)

		two := n.args[1].compile(ctx, of, "")
		fmt.Fprintf(of, "\tcmp %s %s\n", t1.ref, two.ref)
		release(ctx, of, two)
		sgt := ctx.TempB(of)
		fmt.Fprintf(of, "\tsetle %s\n", sgt.ref)
		fmt.Fprintf(of, "\tmovzx %s %s\n", t1.ref, sgt.ref)
		release(ctx, of, sgt)
		return t1
	case n_ge:
		var t1 val
		if prefreg != "" {
			t1 = regval(prefreg)
		} else {
			t1 = ctx.Temp(of)
		}
		one := n.args[0].compile(ctx, of, t1.ref)
		if one != t1 {
			fmt.Fprintf(of, "\tmov %s %s\n", t1.ref, one.ref)
		}
		release(ctx, of, one)

		two := n.args[1].compile(ctx, of, "")
		fmt.Fprintf(of, "\tcmp %s %s\n", t1.ref, two.ref)
		release(ctx, of, two)
		sgt := ctx.TempB(of)
		fmt.Fprintf(of, "\tsetge %s\n", sgt.ref)
		fmt.Fprintf(of, "\tmovzx %s %s\n", t1.ref, sgt.ref)
		release(ctx, of, sgt)
		return t1
	case n_if:
		//panic("NOT IMPLEMENTED")
		els := ctx.Label()
		end := ctx.Label()
		e1 := n.args[0].compile(ctx, of, "")
		fmt.Fprintf(of, "\tcmp %s 0\n", e1.ref)
		release(ctx, of, e1)
		fmt.Fprintf(of, "\tje %s\n", els)
		release(ctx, of, n.args[1].compile(ctx, of, ""))
		if len(n.args) > 2 {
			fmt.Fprintf(of, "\tjmp %s\n", end)
		}
		fmt.Fprintf(of, "\tlabel %s\n", els)
		if len(n.args) > 2 {
			release(ctx, of, n.args[2].compile(ctx, of, ""))
			fmt.Fprintf(of, "\tlabel %s\n", end)
		}
		return noval()
	case n_for:
		panic("NOT IMPLEMENTED")
	case n_block:
		for _, arg := range n.args {
			release(ctx, of, arg.compile(ctx, of, ""))
		}
		return noval()
	case n_break:
		panic("NOT IMPLEMENTED")
	case n_return:
		//panic("NOT IMPLEMENTED")
		val := n.args[0].compile(ctx, of, "")
		//fmt.Fprintf(of, "\tmov rax %s\n", val)
		val.LoadInto(ctx, of, "rax")
		release(ctx, of, val)
		ctx.Return(of)
		return noval()
	case n_var:
		if n.nval > 0 {
			// This is a pointer
			fmt.Fprintf(of, "\tlocal %s 64\n", n.sval)
			fmt.Fprintf(of, "\tmov %s 0\n", n.sval)
		} else {
			s, ok := ctx.typeSize(n.args[0].sval)
			if !ok {
				panic(fmt.Sprintf("Don't know the size of type %s", n.args[0].sval))
			}
			if s <= 8 {
				// Value fits in a register
				fmt.Fprintf(of, "\tlocal %s %d\n", n.sval, s*8) // TODO: bit size
				fmt.Fprintf(of, "\tmov %s 0\n", n.sval)
			} else {
				// Value does not fit in a register. Must use a pointer.
				fmt.Fprintf(of, "\tbytes %s %d\n", n.sval, s)
				// 				if !ctx.registers.Use(gbasm.R_RCX) {
				// 					panic(fmt.Sprintf("Could not acquire RCX for rep counter.\n"))
				// 				}

				loop := ctx.Label()
				endloop := ctx.Label()
				fmt.Fprintf(of, "\tacquire rcx rax rdi\n") // TODO: Can be more efficient, don't need to evict all registers.
				fmt.Fprintf(of, "\tmov rcx %d\n", s)
				fmt.Fprintf(of, "\tmov rax 0\n")
				fmt.Fprintf(of, "\tmov rdi %s\n", n.sval)

				fmt.Fprintf(of, "\tlabel %s\n", loop)
				fmt.Fprintf(of, "\ttest rcx rcx\n")
				fmt.Fprintf(of, "\tjz %s\n", endloop)
				fmt.Fprintf(of, "\tmov [rdi] 0\n")
				fmt.Fprintf(of, "\tinc rdi\n")
				fmt.Fprintf(of, "\tdec rcx\n")
				fmt.Fprintf(of, "\tjmp %s\n", loop)
				fmt.Fprintf(of, "\tlabel %s\n", endloop)
			}
		}
		ctx.bindings[n.sval] = &typename{name: n.args[0].sval, ind: int(n.args[0].nval)}
		return noval()
	case n_none:
		//nic("NOT IMPLEMENTED")
		return noval()
	}
	panic("FOO")
}
