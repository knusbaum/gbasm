package main

import (
	"fmt"
	"io"
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

	parent    *CompileContext
	strngs    map[string]string
	types     map[string]BType
	registers *gbasm.Registers
	labeli    int
	tempi     int
	retlabs   []string
	bindings  map[string]*BType
}

func (c *CompileContext) subContext() *CompileContext {
	nc := NewCompileContext()
	nc.parent = c
	return nc
}

func NewCompileContext() *CompileContext {
	return &CompileContext{
		strngs: make(map[string]string),
		types: map[string]BType{
			"void": voidType(),
			"num":  numType(),
			"str":  strType(),
			"byte": byteType(),
		},
		registers: gbasm.NewRegisters(),
		bindings:  map[string]*BType{},
	}
}

// Returns the type of thing that's being
func (ctx *CompileContext) typeOf(n *Node) (BType, bool) {
	if n.t == n_symbol {
		t, ok := ctx.bindings[n.sval]
		return *t, ok
	}
	if n.t == n_dot {
		t, ok := ctx.typeOf(n.args[0])
		if !ok {
			return BType{}, false
		}
		// Have type of parent struct
		ts, ok := ctx.types[t.name]
		if !ok {
			panic(fmt.Sprintf("No such struct %s", t.name))
			return BType{}, false
		}
		field, ok := ts.fields[n.args[1].sval]
		if !ok {
			panic(fmt.Sprintf("No such field name %s for struct %s", t.name, n.args[1].sval))
			return BType{}, false
		}
		//TODO: This is wrong and won't work with indirection.
		//return &BType{name: field.typename}, true

		return field.t, true
	}
	spew.Dump(n)
	panic(fmt.Sprintf("Cannot determine type for %#v\n", n))
}

func (c *CompileContext) WriteStrings(of io.Writer) {
	for k, s := range c.strngs {
		fmt.Fprintf(of, "var %s string \"%s\\0\"\n", s, unparseString(k))
	}
}

func (c *CompileContext) defType(n string, t BType) bool {
	if ot, ok := c.typeByName(n); ok {
		if !t.Equal(ot) {
			return false
		}
		return true
	}
	c.types[n] = t
	return true
}

func (c *CompileContext) typeByName(name string) (BType, bool) {
	v, ok := c.types[name]
	return v, ok
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

// Temp declares a temporary val
func (c *CompileContext) Temp(t BType, of io.Writer) valnew {
	c.tempi++
	tmp := fmt.Sprintf("_T%d", c.tempi)
	switch t.RefType() {
	case rt_direct:
		fmt.Fprintf(of, "\tlocal %s %d\n", tmp, t.Size()*8) // TODO: size in bytes not bits
	case rt_indirect:
		fmt.Fprintf(of, "\tbytes %s %d\n", tmp, t.Size())
	default:
		panic(fmt.Sprintf("Invalid type has no reftype: %#v\n", t))
	}
	return Value(tmp, t)
}

// TODO: unit test
func (c *CompileContext) functionReturnType(tn BType) (BType, error) {
	if !strings.HasPrefix(tn.String(), "fn(") {
		panic(fmt.Sprintf("%s is not a function type name.", tn.String()))
	}
	rname := strings.Split(tn.String(), " ")[1]
	t, ok := c.typeByName(rname)
	if !ok {
		return t, fmt.Errorf("No such type %s", rname)
	}
	return t, nil
}

func (c *CompileContext) Import(f string) error {
	o, err := gbasm.ReadOFile(f)
	if err != nil {
		return err
	}
	for _, t := range o.Funcs {
		if t.Type != "" {
			c.bindings[t.Name] = &BType{name: t.Type}
			//fmt.Printf("(CompileContext) IMPORTED %s %s\n", t.Name, t.Type)
		}
	}
	return nil
}

func (c *CompileContext) Return(of io.Writer) {
	fmt.Fprintf(of, "\tjmp %s\n", c.retlabs[len(c.retlabs)-1])
}

func (c *CompileContext) release(of io.Writer, s valnew) valnew {
	//fmt.Printf("2RELEASING %s\n", s.ref)
	if strings.HasPrefix(s.ref, "&") {
		panic("OK")
	}
	s2 := strings.ToUpper(s.ref)
	if r, err := gbasm.ParseReg(s2); err == nil {
		//fmt.Printf("1RELEASING %s\n", r.String())
		c.registers.Release(r)
	} else if strings.HasPrefix(s2, "_T") {
		fmt.Fprintf(of, "\tforget %s\n", s2)
	}
	return s
}

func (n *Node) createStruct(ctx *CompileContext) {
	// This is duplicated from validate's n_struct. We should reuse the validation pass's data instead.
	for _, field := range n.args {
		if _, ok := ctx.typeByName(field.args[0].sval); !ok {
			panic(fmt.Sprintf("Type %s undefined.", field.args[0].sval))
		}
	}

	sd := BType{name: n.sval, rt: rt_indirect}
	for _, field := range n.args {
		//fmt.Printf("FIELD ### %s field %d -> name: %s, type: %s\n", n.sval, i, field.sval, field.args[0].sval)
		t, ok := ctx.typeByName(field.args[0].sval)
		if !ok {
			panic(&interpreterError{msg: fmt.Sprintf("No such type %s in field", field.args[0].sval), p: field.args[0].p})
		}
		sd.addField(field.sval, t)
	}
	//fmt.Printf("DECLARING STRUCT %s\n", n.sval)
	if ok := ctx.defType(n.sval, sd); !ok {
		panic(fmt.Sprintf("Type %s already exists.", n.sval))
	}
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
		var a valnew
		switch i {
		case 0:
			rv := RegVal("rdi", 8)
			a = args[i].compile(ctx, of, rv)
			if !a.Same(rv) {
				//fmt.Fprintf(of, "//// CHECK THIS\n")
				a.LoadInto(ctx, of, rv)
			} else {
				//fmt.Fprintf(of, "//// EXCLUDED \n")
				//fmt.Fprintf(of, "// mov %s %s\n", rv.ref, a.ref)
			}
		case 1:
			rv := RegVal("rsi", 8)
			a = args[i].compile(ctx, of, rv)
			if !a.Same(rv) {
				//fmt.Fprintf(of, "//// CHECK THIS\n")
				a.LoadInto(ctx, of, rv)
			} else {
				// 				fmt.Fprintf(of, "//// EXCLUDED \n")
				// 				fmt.Fprintf(of, "//")
				// 				a.LoadInto(ctx, of, "rsi")
			}
		case 2:
			rv := RegVal("rdx", 8)
			a = args[i].compile(ctx, of, rv)
			if !a.Same(rv) {
				// 				fmt.Fprintf(of, "//// CHECK THIS\n")
				a.LoadInto(ctx, of, rv)
			} else {
				// 				fmt.Fprintf(of, "//// EXCLUDED \n")
				// 				fmt.Fprintf(of, "//")
				// 				a.LoadInto(ctx, of, "rdx")
			}
		case 3:
			rv := RegVal("rcx", 8)
			a = args[i].compile(ctx, of, rv)
			if !a.Same(rv) {
				//				fmt.Fprintf(of, "//// CHECK THIS\n")
				a.LoadInto(ctx, of, rv)
			} else {
				// 				fmt.Fprintf(of, "//// EXCLUDED \n")
				// 				fmt.Fprintf(of, "//")
				// 				a.LoadInto(ctx, of, "rcx")
			}
		case 4:
			rv := RegVal("r8", 8)
			a = args[i].compile(ctx, of, rv)
			if !a.Same(rv) {
				//				fmt.Fprintf(of, "//// CHECK THIS\n")
				a.LoadInto(ctx, of, rv)
			} else {
				// 				fmt.Fprintf(of, "//// EXCLUDED \n")
				// 				fmt.Fprintf(of, "//")
				// 				a.LoadInto(ctx, of, "r8")
			}
		case 5:
			rv := RegVal("r9", 8)
			a = args[i].compile(ctx, of, rv)
			if !a.Same(rv) {
				//				fmt.Fprintf(of, "//// CHECK THIS\n")
				a.LoadInto(ctx, of, rv)
			} else {
				// 				fmt.Fprintf(of, "//// EXCLUDED \n")
				// 				fmt.Fprintf(of, "//")
				// 				a.LoadInto(ctx, of, "r9")
			}
		default:
			panic("TOO MANY ARGS\n")
		}
		ctx.release(of, a)
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
	v_value
)

// type val struct {
// 	ref  string
// 	t    vtype
// 	size int
// }
//
// func (v val) LoadInto(ctx *CompileContext, of io.Writer, s string) {
// 	switch v.t {
// 	case v_str:
// 		fmt.Fprintf(of, "\t//v_str\n")
// 		fmt.Fprintf(of, "\tlea %s %s\n", s, v.ref)
// 	case v_num:
// 		fmt.Fprintf(of, "\t//v_num\n")
// 		fmt.Fprintf(of, "\tmov %s %s\n", s, v.ref)
// 	case v_value:
// 		if v.size < 8 {
// 			// EXTEND ZEROS!
// 			fmt.Fprintf(of, "\t//v_reg %#v (extend)\n", v)
// 			fmt.Fprintf(of, "\tmovzx %s %s\n", s, v.ref)
// 		} else {
// 			fmt.Fprintf(of, "\t//v_reg %#v\n", v)
// 			fmt.Fprintf(of, "\tmov %s %s\n", s, v.ref)
// 		}
// 	default:
// 		panic(fmt.Sprintf("CANNOT LOAD %v\n", v))
// 	}
// }
//
// func numval(n int) val {
// 	return val{ref: fmt.Sprintf("%d", n), t: v_num}
// }
//
// func strval(s string) val {
// 	return val{ref: s, t: v_str}
// }
//
// func regval(s string, size int) val {
// 	return val{ref: s, t: v_value, size: size}
// }
//
// func valnew{} val {
// 	return val{}
// }

func (n *Node) compileEQDOT(ctx *CompileContext, of io.Writer, prefreg valnew) valnew {
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
	st, ok := ctx.typeByName(t.name)
	if !ok {
		panic(fmt.Sprintf("failed to find struct definition for %s", t.name))
	}
	ft, ok := st.fields[n.args[1].sval]
	if !ok {
		panic(fmt.Sprintf("failed to find field definition for %s.%s", t.name, n.args[1].sval))
	}

	// Compile the thing we're getting the field from into a ref. This can be just a symbol if it's a variable, or an
	// expression that yields a struct.
	v1 := n.args[0].compile(ctx, of, valnew{})
	if v1.ref == "" {
		panic("OK")
	}

	var t1 valnew
	if !prefreg.Same(valnew{}) {
		t1 = prefreg
	} else {
		fmt.Fprintf(of, "// temp for %s\n", v1.t.PointerTo())
		t1 = ctx.Temp(v1.t.PointerTo(), of)
	}
	//fmt.Printf("Assigning to field %s of type %s in struct %s\n", n.args[1].sval, ft.t.name, t.name)
	// return a reference.
	fmt.Fprintf(of, "\t//loading address for %s.%s\n", t.name, n.args[1].sval)
	fmt.Fprintf(of, "\tlea %s [%s+%d]\n", t1.ref, v1.ref, ft.offset)

	ctx.release(of, v1)
	return t1
}

// valnew will replace val
type valnew struct {
	// This will be a variable name or a register name
	ref string
	// This is either a none, string literal, number literal, or value. If this is a vt_value, then t
	// must be set.
	vt vtype

	// The name of the type of this value
	t BType
}

func (v valnew) LoadInto(ctx *CompileContext, of io.Writer, ov valnew) {
	switch v.t.RefType() {
	case rt_indirect:
		fmt.Fprintf(of, "\t//rt_indirect\n")
		fmt.Fprintf(of, "\tlea %s %s\n", ov.ref, v.ref)
	case rt_direct:
		if v.t.Size() < ov.t.Size() {
			// EXTEND ZEROS!
			fmt.Fprintf(of, "\t//rt_direct %#v (extend)\n", v)
			fmt.Fprintf(of, "\tmovzx %s %s\n", ov.ref, v.ref)
		} else if v.t.Size() > 8 {
			fmt.Fprintf(of, "\t//TODO: COPY\n")
			fmt.Fprintf(of, "\tcopy %s %s\n", ov.ref, v.ref)
		} else {
			fmt.Fprintf(of, "\t//rt_direct\n")
			fmt.Fprintf(of, "\tmov %s %s\n", ov.ref, v.ref)
		}
		// 	case v_value:
		// 		if v.size < 8 {
		// 			// EXTEND ZEROS!
		// 			fmt.Fprintf(of, "\t//v_reg %#v (extend)\n", v)
		// 			fmt.Fprintf(of, "\tmovzx %s %s\n", ov.ref, v.ref)
		// 		} else {
		// 			fmt.Fprintf(of, "\t//v_reg %#v\n", v)
		// 			fmt.Fprintf(of, "\tmov %s %s\n", ov.ref, v.ref)
		// 		}
	default:
		panic(fmt.Sprintf("CANNOT LOAD %#v into %#v\n", v, ov))
	}
}

func (v valnew) Same(v2 valnew) bool {
	if v.ref != v2.ref {
		return false
	}
	if v.vt != v2.vt {
		return false
	}
	return v.t.Equal(v2.t)
}

func Value(ref string, t BType) valnew {
	return valnew{ref: ref, vt: v_value, t: t}
}

// create a new register val with size in bytes.
// size must correctly correlate to the actual size of r and r must be a real register.
// This should ONLY be used as a dest argument to compile, since it does not carry
// type information.
func RegVal(r string, size int) valnew {
	return valnew{ref: r, vt: v_value, t: BType{name: "*untyped-register*", size: size}}
}

// compile compiles causes the node to be recursively compiled, and the val returned. dest is an
// optional val where compile should try to place any result of the compilation, although this is
// not required or guaranteed. Callers of compile should check that the return value is indeed in
// dest if that is necessary.
func (n *Node) compile(ctx *CompileContext, of io.Writer, dest valnew) valnew {
	switch n.t {
	case n_number:
		//return numval(int(n.nval)) //fmt.Sprintf("%d", int(n.nval))
		return valnew{ref: fmt.Sprintf("%d", int(n.nval)), t: numType()}
	case n_str:
		var ret valnew
		if dest.Same(valnew{}) {
			tmp := ctx.Temp(strType(), of)
			fmt.Fprintf(of, "\tlea %s %s\n", tmp.ref, n.sval)
			ret = tmp
		} else {
			fmt.Fprintf(of, "\tlea %s %s\n", dest.ref, n.sval)
			ret = dest
		}
		return ret
	case n_symbol:
		t, ok := ctx.bindings[n.sval]
		if !ok {
			panic(fmt.Sprintf("No type name for %#v\n", n))
		}
		return Value(n.sval, *t)
	case n_funcall:
		setupArgs(ctx, of, n.args)
		if !ctx.registers.Use(gbasm.R_RAX) {
			panic(fmt.Sprintf("Could not acquire RAX for return value.\n"))
		}
		fmt.Fprintf(of, "\tcall %s\n", n.sval)
		t, ok := ctx.bindings[n.sval]
		if !ok {
			panic(fmt.Sprintf("No Function Type for %s", n.sval))
		}
		//panic(fmt.Sprintf("FUNCTION RETURN TYPE: %#v\n", t))
		return Value("rax", *t)
	case n_index:
		t, ok := ctx.bindings[n.sval]
		if !ok {
			panic(fmt.Sprintf("No type name for %#v\n", n))
		}

		// Currently only string indexing is supported.
		var t1 valnew
		if !dest.Same(valnew{}) {
			t1 = dest
		} else {
			t1 = ctx.Temp(*t, of)
		}
		if n.sval != t1.ref {
			fmt.Fprintf(of, "\tmov %s [%s+%d]\n", t1.ref, n.sval, int(n.args[0].nval))
		} else {
			panic("IS THIS EVER USED?")
			fmt.Fprintf(of, "\tadd %s %d\n", t1.ref, int(n.args[0].nval))
		}
		// 		fmt.Printf("%v\n", t1)
		// 		spew.Dump(n)
		//panic("NOT IMPLEMENTED")
		return t1
	case n_struct:
		n.createStruct(ctx)
		return valnew{}
	case n_stlit:
		if dest.Same(valnew{}) {
			panic(fmt.Sprintf("Cannot compile struct literal into blank register/var"))
		}
		st, ok := ctx.typeByName(n.sval)
		if !ok {
			panic(fmt.Sprintf("failed to find struct definition for %s", n.sval))
		}

		for _, f := range n.args {
			fmt.Fprintf(of, "\t//STLIT_FIELD %s.%s\n", n.sval, f.sval)
			field, ok := st.fields[f.sval]
			if !ok {
				panic(fmt.Sprintf("no such field %s", f.sval))
			}
			t1 := ctx.Temp(field.t, of)
			if field.t.RefType() == rt_indirect && field.t.Size() > 8 {
				fmt.Fprintf(of, "\t//this is a struct. Pass on a ref to the field in %s\n", t1.ref)
				fmt.Fprintf(of, "\tlea %s [%s+%d]\n", t1.ref, dest.ref, field.offset)
			}
			v := f.args[0].compile(ctx, of, t1)
			if !v.Same(t1) {
				ctx.release(of, t1)
			}

			if !(field.t.RefType() == rt_indirect && v.t.Size() > 8) {
				// if it's not a struct, then load it into dest.
				fmt.Fprintf(of, "\t//LOAD INTO\n")
				v.LoadInto(ctx, of, valnew{ref: fmt.Sprintf("[%s+%d]", dest.ref, field.offset), vt: v_value, t: v.t})
			}
			ctx.release(of, v)
		}
		fmt.Fprintf(of, "\t//STLIT_DONE\n")
		return dest
	case n_stfield:
		panic("n_stfield NOT IMPLEMENTED")
	case n_fn:
		retlab := ctx.Label()
		ctx.retlabs = append(ctx.retlabs, retlab)
		fmt.Fprintf(of, "function %s\n", n.sval)

		ftype := functionTypeName(n)
		rtype, err := ctx.functionReturnType(ftype)
		if err != nil {
			panic(err.Error())
		}
		ctx.bindings[n.sval] = &rtype

		obindings := ctx.bindings
		nbindings := make(map[string]*BType)
		for k, v := range obindings {
			nbindings[k] = v
		}
		for i := 0; i < int(n.nval); i++ {
			//spew.Dump(n.args[i])

			fmt.Fprintf(of, "\targi %s %d\n", n.args[i].sval, i)
			t, ok := ctx.typeByName(n.args[i].args[0].sval)
			if !ok {
				panic(fmt.Sprintf("No such type %s", n.args[i].args[0].sval))
			}
			nbindings[n.args[i].sval] = &t //&BType{name: n.args[i].args[0].sval}
		}

		ctx.bindings = nbindings
		defer func() { ctx.bindings = obindings }()

		fmt.Fprintf(of, "\n\tprologue\n\n")
		for _, arg := range n.args[int(n.nval+1):] {
			ctx.release(of, arg.compile(ctx, of, valnew{}))
		}
		fmt.Fprintf(of, "\n\tlabel %s\n", retlab)
		fmt.Fprintf(of, "\tepilogue\n")
		fmt.Fprintf(of, "\tret\n\n")
		ctx.retlabs = ctx.retlabs[:len(ctx.retlabs)-1]

		return valnew{}
	case n_arg:
		panic("NOT IMPLEMENTED")
	case n_dot:

		// Get the struct type of the thing we're getting the field from
		t, ok := ctx.typeOf(n.args[0])
		if !ok {
			panic(fmt.Sprintf("%#v not bound to struct type.", n.args[0]))
		}

		// Get the struct definition and the field definition
		st, ok := ctx.typeByName(t.name)
		if !ok {
			panic(fmt.Sprintf("failed to find struct definition for %s", t.name))
		}
		ft, ok := st.fields[n.args[1].sval]
		if !ok {
			panic(fmt.Sprintf("failed to find field definition for %s.%s", t.name, n.args[1].sval))
		}

		// Compile the thing we're getting the field from into a ref. This can be just a symbol if it's a variable, or an
		// expression that yields a struct.
		v1 := n.args[0].compile(ctx, of, valnew{})
		if v1.ref == "" {
			panic("OK")
		}

		var t1 valnew
		if !dest.Same(valnew{}) {
			t1 = dest
		} else {
			fmt.Fprintf(of, "// temp for %s\n", v1.t.PointerTo())
			t1 = ctx.Temp(v1.t.PointerTo(), of)
		}
		//fmt.Printf("Assigning to field %s of type %s at offset %d in struct %s\n", n.args[1].sval, ft.t.name, ft.offset, t.name)
		// TODO: much better handling of types here
		//if _, ok := ctx.typeByName(ft.t.name); ok {
		if ft.t.rt == rt_indirect {
			// This field is another struct, return a reference.
			fmt.Fprintf(of, "\t//Type %s is rt_indirect\n", ft.t.name)
			fmt.Fprintf(of, "\tlea %s [%s+%d]\n", t1.ref, v1.ref, ft.offset)
		} else if ft.t.rt == rt_direct {
			fmt.Fprintf(of, "\t//Type %s is rt_direct\n", ft.t.name)
			fmt.Fprintf(of, "\tmov %s [%s+%d]\n", t1.ref, v1.ref, ft.offset)
		} else {
			panic(fmt.Sprintf("WE HAVE A TYPE WITHOUT A REF TYPE: %#v", ft.t))
		}

		ctx.release(of, v1)
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
		var t1 valnew
		if !dest.Same(valnew{}) {
			t1 = dest
		} else {
			t1 = valnew{} //ctx.Temp(of)
		}
		//
		one := n.args[0].compile(ctx, of, t1)
		// 		if !one.t.Equal(numType()) {
		// 			panic(fmt.Sprintf("CANNOT ADD NON-NUMBERS %#v, %#v\n", one.t, numType()))
		// 		}

		if t1.Same(valnew{}) {
			t1 = ctx.Temp(one.t, of)
		}
		if !one.Same(t1) {
			fmt.Fprintf(of, "\tmov %s %s\n", t1.ref, one.ref)
			ctx.release(of, one)
		}
		//one.LoadInto(t1)

		two := n.args[1].compile(ctx, of, valnew{})
		fmt.Fprintf(of, "\tadd %s %s\n", t1.ref, two.ref)
		ctx.release(of, two)
		return t1
	case n_sub:
		// 		r, ok := ctx.registers.Get(64)
		// 		if !ok {
		// 			panic("Out of registers.\n")
		// 		}
		// 		rs := strings.ToLower(r.String())
		var t1 valnew
		if !dest.Same(valnew{}) {
			t1 = dest
		} else {
			t1 = valnew{} //ctx.Temp(of)
		}
		one := n.args[0].compile(ctx, of, t1)
		if !one.t.Equal(numType()) {
			panic(fmt.Sprintf("CANNOT ADD NON-NUMBERS %#v, %#v\n", one.t, numType()))
		}
		if t1.Same(valnew{}) {
			t1 = ctx.Temp(one.t, of)
		}
		if !one.Same(t1) {
			fmt.Fprintf(of, "\tmov %s %s\n", t1.ref, one.ref)
			ctx.release(of, one)
		}

		two := n.args[1].compile(ctx, of, valnew{})
		fmt.Fprintf(of, "\tsub %s %s\n", t1.ref, two.ref)
		ctx.release(of, two)
		return t1
	case n_eq:
		if n.args[0].t != n_symbol && n.args[0].t != n_dot {
			panic("Can only assign to variables.\n")
		}

		if n.args[0].t == n_dot {
			dst := n.args[0].compileEQDOT(ctx, of, valnew{})
			v := n.args[1].compile(ctx, of, valnew{})
			v.LoadInto(ctx, of, valnew{ref: fmt.Sprintf("[%s]", dst.ref)})
			ctx.release(of, v)
			ctx.release(of, dst)
		} else if n.args[0].t == n_symbol {
			t, ok := ctx.typeOf(n.args[0])
			if !ok {
				panic(fmt.Sprintf("No type %s\n", n.args[0].sval))
			}
			target := Value(n.args[0].sval, t)
			v := n.args[1].compile(ctx, of, target)
			if v.ref != n.args[0].sval {
				v.LoadInto(ctx, of, target)
				ctx.release(of, v)
			}
		}
		return valnew{}
	case n_deq:
		panic("NOT IMPLEMENTED")
	case n_lt:
		var t1 valnew
		if !dest.Same(valnew{}) {
			t1 = dest
		} else {
			t1 = valnew{} //ctx.Temp(of)
		}
		one := n.args[0].compile(ctx, of, t1)
		if t1.Same(valnew{}) {
			t1 = ctx.Temp(one.t, of)
		}
		if !one.Same(t1) {
			fmt.Fprintf(of, "\tmov %s %s\n", t1.ref, one.ref)
			ctx.release(of, one)
		}

		two := n.args[1].compile(ctx, of, valnew{})
		fmt.Fprintf(of, "\tcmp %s %s\n", t1.ref, two.ref)
		ctx.release(of, two)
		sgt := ctx.Temp(byteType(), of)
		fmt.Fprintf(of, "\tsetl %s\n", sgt.ref)
		fmt.Fprintf(of, "\tmovzx %s %s\n", t1.ref, sgt.ref)
		ctx.release(of, sgt)
		return t1
	case n_gt:
		var t1 valnew
		if !dest.Same(valnew{}) {
			t1 = dest
		} else {
			t1 = valnew{} //ctx.Temp(of)
		}
		one := n.args[0].compile(ctx, of, t1)
		if t1.Same(valnew{}) {
			t1 = ctx.Temp(one.t, of)
		}
		if !one.Same(t1) {
			fmt.Fprintf(of, "\tmov %s %s\n", t1.ref, one.ref)
			ctx.release(of, one)
		}

		two := n.args[1].compile(ctx, of, valnew{})
		fmt.Fprintf(of, "\tcmp %s %s\n", t1.ref, two.ref)
		ctx.release(of, two)
		sgt := ctx.Temp(byteType(), of)
		fmt.Fprintf(of, "\tsetg %s\n", sgt.ref)
		fmt.Fprintf(of, "\tmovzx %s %s\n", t1.ref, sgt.ref)
		ctx.release(of, sgt)
		return t1
	case n_le:
		var t1 valnew
		if !dest.Same(valnew{}) {
			t1 = dest
		} else {
			t1 = valnew{} //ctx.Temp(of)
		}
		one := n.args[0].compile(ctx, of, t1)
		if t1.Same(valnew{}) {
			t1 = ctx.Temp(one.t, of)
		}
		if !one.Same(t1) {
			fmt.Fprintf(of, "\tmov %s %s\n", t1.ref, one.ref)
			ctx.release(of, one)
		}

		two := n.args[1].compile(ctx, of, valnew{})
		fmt.Fprintf(of, "\tcmp %s %s\n", t1.ref, two.ref)
		ctx.release(of, two)
		sgt := ctx.Temp(byteType(), of)
		fmt.Fprintf(of, "\tsetle %s\n", sgt.ref)
		fmt.Fprintf(of, "\tmovzx %s %s\n", t1.ref, sgt.ref)
		ctx.release(of, sgt)
		return t1
	case n_ge:
		var t1 valnew
		if !dest.Same(valnew{}) {
			t1 = dest
		} else {
			t1 = valnew{} //ctx.Temp(of)
		}
		one := n.args[0].compile(ctx, of, t1)
		if t1.Same(valnew{}) {
			t1 = ctx.Temp(one.t, of)
		}
		if !one.Same(t1) {
			fmt.Fprintf(of, "\tmov %s %s\n", t1.ref, one.ref)
			ctx.release(of, one)
		}

		two := n.args[1].compile(ctx, of, valnew{})
		fmt.Fprintf(of, "\tcmp %s %s\n", t1.ref, two.ref)
		ctx.release(of, two)
		sgt := ctx.Temp(byteType(), of)
		fmt.Fprintf(of, "\tsetge %s\n", sgt.ref)
		fmt.Fprintf(of, "\tmovzx %s %s\n", t1.ref, sgt.ref)
		ctx.release(of, sgt)
		return t1
	case n_if:
		//panic("NOT IMPLEMENTED")
		els := ctx.Label()
		end := ctx.Label()
		e1 := n.args[0].compile(ctx, of, valnew{})
		fmt.Fprintf(of, "\tcmp %s 0\n", e1.ref)
		ctx.release(of, e1)
		fmt.Fprintf(of, "\tje %s\n", els)
		ctx.release(of, n.args[1].compile(ctx, of, valnew{}))
		if len(n.args) > 2 {
			fmt.Fprintf(of, "\tjmp %s\n", end)
		}
		fmt.Fprintf(of, "\tlabel %s\n", els)
		if len(n.args) > 2 {
			ctx.release(of, n.args[2].compile(ctx, of, valnew{}))
			fmt.Fprintf(of, "\tlabel %s\n", end)
		}
		return valnew{}
	case n_for:
		panic("NOT IMPLEMENTED")
	case n_block:
		for _, arg := range n.args {
			ctx.release(of, arg.compile(ctx, of, valnew{}))
		}
		return valnew{}
	case n_break:
		panic("NOT IMPLEMENTED")
	case n_return:
		//panic("NOT IMPLEMENTED")
		val := n.args[0].compile(ctx, of, valnew{})
		//fmt.Fprintf(of, "\tmov rax %s\n", val)
		val.LoadInto(ctx, of, Value("rax", val.t))
		ctx.release(of, val)
		ctx.Return(of)
		return valnew{}
	case n_var:
		if n.nval > 0 {
			// This is a pointer
			fmt.Fprintf(of, "\tlocal %s 64\n", n.sval)
			fmt.Fprintf(of, "\tmov %s 0\n", n.sval)
		} else {
			t, ok := ctx.typeByName(n.args[0].sval)
			if !ok {
				panic(fmt.Sprintf("Don't know the size of type %s", n.args[0].sval))
			}
			if s := t.Size(); s <= 8 {
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
		t, ok := ctx.typeByName(n.args[0].sval)
		if !ok {
			panic(fmt.Sprintf("No such type %s", n.args[0].sval))
		}
		ctx.bindings[n.sval] = &t // &BType{name: n.args[0].sval, ind: int(n.args[0].nval)}
		return valnew{}
	case n_none:
		//nic("NOT IMPLEMENTED")
		return valnew{}
	}
	panic("FOO")
}
