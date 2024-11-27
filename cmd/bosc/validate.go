package main

import (
	"fmt"
	"strings"

	"github.com/davecgh/go-spew/spew"
	"github.com/knusbaum/gbasm"
)

type reftype int

const (
	rt_none reftype = iota
	rt_direct
	rt_indirect
)

type Field struct {
	t      BType
	offset int
}

// BType is a boson type
type BType struct {
	name   string
	ind    int
	size   int     // Size in bytes. Required for rt_direct, and when rt_direct, must be <=8
	rt     reftype // Whether registers hold the value or a pointer to the value.
	fields map[string]Field
}

func (t BType) String() string {
	var b strings.Builder
	for i := 0; i < t.ind; i++ {
		fmt.Fprintf(&b, "*")
	}
	b.WriteString(t.name)
	return b.String()
}

// Size returns the size in bytes of instances of this type.
func (t BType) Size() int {
	if t.rt == rt_direct && t.size > 8 {
		panic("Size must be <= 8 bytes for direct-reference types")
	}
	return t.size
}

func (t BType) RefType() reftype {
	return t.rt
}

func (t BType) Equal(ot BType) bool {
	if t.name != ot.name {
		return false
	}
	if t.ind != ot.ind {
		return false
	}
	if t.size != ot.size {
		return false
	}
	if t.rt != ot.rt {
		return false
	}
	return true
}

func (t BType) PointerTo() BType {
	return BType{
		name:   t.name,
		ind:    t.ind + 1,
		size:   8,
		rt:     rt_direct, // counter-intuitively, pointers are referenced directly (copied into registers)
		fields: make(map[string]Field),
	}
}

// Built-in types
func voidType() BType {
	return BType{name: "void"}
}

func numType() BType {
	return BType{name: "num", size: 8, rt: rt_direct} // Nums are 64-bit signed integers.
}

func strType() BType {
	return BType{name: "str", size: 8, rt: rt_direct} // strs are pointers.
}

func strLitType() BType {
	return BType{name: "strlit", rt: rt_indirect} // strs are pointers.
}

func byteType() BType {
	return BType{name: "byte", size: 1, rt: rt_direct}
}

func (t *BType) addField(name string, ft BType) error {
	if t.fields == nil {
		t.fields = make(map[string]Field)
	}
	if _, ok := t.fields[name]; ok {
		return fmt.Errorf("field %s already defined.", name)
	}
	t.fields[name] = Field{t: ft, offset: t.size}
	//fmt.Printf("ADDED FIELD %s(%s) OF SIZE %d AT OFFSET %d TO STRUCT %s\n",
	//	name, ft.name, ft.Size(), t.size, t.name)
	t.size += ft.Size()
	return nil
}

type VContext struct {
	parent     *VContext
	bindType   map[string]BType
	validTypes map[string]BType
	//structs    map[BType]*structDef
}

func NewVContext() *VContext {
	return &VContext{
		bindType: make(map[string]BType),
		validTypes: map[string]BType{
			"void":   voidType(),
			"num":    numType(),
			"str":    strType(),
			"strlit": strLitType(),
			"byte":   byteType(),
		},
		//structs: make(map[BType]*structDef),
	}
}

func (c *VContext) subVContext() *VContext {
	nv := NewVContext()
	nv.parent = c
	return nv
}

func (c *VContext) binding(n string) (BType, bool) {
	if tn, ok := c.bindType[n]; ok {
		return tn, ok
	}
	if c.parent != nil {
		return c.parent.binding(n)
	}
	return voidType(), false
}

func (c *VContext) bind(n string, t BType) bool {
	if ot, ok := c.bindType[n]; ok {
		if !t.Equal(ot) {
			return false
		}
		return true
	}
	c.bindType[n] = t
	return true
}

// func (c *VContext) structFor(t BType) (*structDef, bool) {
// 	//fmt.Printf("STRUCTS: %v\n", c.structs)
// 	if d, ok := c.structs[t]; ok {
// 		return d, ok
// 	}
// 	if c.parent != nil {
// 		return c.parent.structFor(t)
// 	}
// 	return nil, false
// }
//
// func (c *VContext) declStruct(t BType, d *structDef) error {
// 	if _, ok := c.structs[t]; ok {
// 		return fmt.Errorf("struct %s already declared.", t)
// 	}
// 	c.structs[t] = d
// 	//fmt.Printf("STRUCTS: %v\n", c.structs)
// 	return nil
// }

func (c *VContext) typeByName(name string) (BType, bool) {
	if tn, ok := c.validTypes[name]; ok {
		return tn, ok
	}
	if c.parent != nil {
		return c.parent.typeByName(name)
	}
	return voidType(), false
}

func (c *VContext) defType(n string, t BType) bool {
	if ot, ok := c.typeByName(n); ok {
		if !t.Equal(ot) {
			fmt.Printf("ALREADY HAVE TYPE: %#v\n", ot)
			fmt.Printf("NEW TYPE: %#v\n", t)
			return false
		}
		return true
	}
	c.validTypes[n] = t
	return true
}

func (c *VContext) Import(f string) error {
	o, err := gbasm.ReadOFile(f)
	if err != nil {
		return err
	}
	for v, t := range o.Funcs {
		if t.Type != "" {
			if !c.bind(t.Name, BType{name: t.Type}) {
				return fmt.Errorf("%s already defined.", v)
			}
			//fmt.Printf("IMPORTED %s %s\n", t.Name, t.Type)
		}
	}
	return nil
}

// TODO: unit test
func functionTypeName(n *Node, c interface{ typeByName(string) (BType, bool) }) BType {
	if n.t != n_fn {
		panic(&interpreterError{msg: fmt.Sprintf("cannot determine function type name of non-function %#v. This is a compiler error.", n), p: n.p})
	}
	var b strings.Builder
	b.WriteString("fn(")
	for i := 0; i < int(n.fval); i++ {
		if i > 0 {
			b.WriteString(",")
		}
		argtype := n.args[i].args[0].sval
		argindir := n.args[i].args[0].ival
		_, ok := c.typeByName(argtype)
		if !ok {
			panic(&interpreterError{msg: fmt.Sprintf("no such type '%s'.", argtype), p: n.p})
		}
		for i := uint64(0); i < argindir; i++ {
			b.WriteString("*")
		}
		b.WriteString(argtype)
	}
	b.WriteString(") ")
	//spew.Dump(n.args[0])
	if n.args[int(n.fval)].sval == "none" {
		panic("WOAH!")
	}
	b.WriteString(n.args[int(n.fval)].sval)
	return BType{name: b.String()}
}

// TODO: unit test
func functionTypeNameFromCall(n *Node, c *VContext) BType {
	if n.t != n_funcall {
		panic(&interpreterError{msg: fmt.Sprintf("cannot determine function type name of non-function-call %#v. This is a compiler error.", n), p: n.p})
	}
	var b strings.Builder
	b.WriteString("fn(")
	for i := 0; i < len(n.args); i++ {
		if i > 0 {
			b.WriteString(",")
		}
		//fmt.Printf("Validating argument %d\n", i)
		//spew.Dump(n.args[i])
		tn := validate(n.args[i], c)
		//fmt.Printf("Validate returned: %s\n", tn)
		//fmt.Printf("ARG %d\n", i)
		//spew.Dump(tn)
		b.WriteString(tn.String())
	}
	b.WriteString(")")
	return BType{name: b.String()}
}

// TODO: unit test
func (c *VContext) functionReturnType(n *Node, tn BType) (BType, error) {
	if !strings.HasPrefix(tn.String(), "fn(") {
		panic(&interpreterError{msg: fmt.Sprintf("cannot determine function type name of non-function %#v. This is a compiler error.", n), p: n.p})
	}
	rname := strings.Split(tn.String(), " ")[1]
	//return BType{name: rname}
	t, ok := c.typeByName(rname)
	if !ok {
		fmt.Printf("RNAME IS %#v\n", rname)
		return BType{}, fmt.Errorf("No such type %s", rname)
	}
	return t, nil
}

// TODO: unit test
func functionCallType(n *Node, tn BType) BType {
	if !strings.HasPrefix(tn.String(), "fn(") {
		panic(&interpreterError{msg: fmt.Sprintf("cannot determine function type name of non-function %#v. This is a compiler error.", n), p: n.p})
	}
	return BType{name: strings.Split(tn.String(), " ")[0]}
}

func validateReturns(n *Node, c *VContext, t BType) (rt BType) {
	// 	fmt.Printf("validateReturns()\n")
	// 	defer func() { fmt.Printf("validateReturns() -> %#v\n", rt) }()
	switch n.t {
	case n_return:
		//		fmt.Printf("Found Return\n")
		rt := validate(n.args[0], c)
		//		fmt.Printf("Return Returned: %#v\n", rt)
		if !rt.Equal(t) {
			panic(&interpreterError{msg: fmt.Sprintf("Expected a return of type %s but found %s.", t, rt), p: n.p})
		}
		return rt
	case n_if:
		//TODO: Validate the condition?
		//		fmt.Printf("Found If Condition.\n")
		t1 := validateReturns(n.args[1], c, t)
		//		fmt.Printf("If Returned: %#v\n", t1)
		if len(n.args) > 2 {
			//			fmt.Printf("Found Else\n")
			t2 := validateReturns(n.args[2], c, t)
			//			fmt.Printf("Else Returned: %#v\n", t2)
			if t1.Equal(t2) {
				return t1
			} else {
				panic(&interpreterError{msg: fmt.Sprintf("If branches return different types %s and %s", t1, t2), p: n.p})
			}
		}
		return t1
	case n_block:
		//		fmt.Printf("Fould Block.\n")
		for _, n := range n.args {
			if brv := validateReturns(n, c, t); !brv.Equal(voidType()) {
				return brv
			}
		}
		return voidType()
	default:
		return voidType()
	}
}

// validate recursively checks the AST rooted at *Node and returns resulting Value Type (encoded in
// a Value) for the Node. The return type is a Value as opposed to a ValType because the ValType is
// not sufficient to describe complex types such as functions and structs. To make sure a function
// or struct matches another, we need a Value with the fnval or s fields set, containing the
// details (parameters, return types, fields) of the function or struct.
func validate(n *Node, c *VContext) BType {
	switch n.t {
	case n_none:
		return voidType()
	case n_number:
		return numType()
	case n_str:
		return strType()
	case n_symbol:
		if v, ok := c.binding(n.sval); ok {
			return v
		}
		panic(&interpreterError{msg: fmt.Sprintf("%s is not bound", n.sval), p: n.p})
	case n_funcall:
		v, ok := c.binding(n.sval)
		if !ok {
			panic(&interpreterError{msg: fmt.Sprintf("%s is not bound", n.sval), p: n.p})
		}
		ct := functionTypeNameFromCall(n, c)
		fct := functionCallType(n, v)
		// 		fmt.Printf("Expected function type: %v\n", ct)
		// 		fmt.Printf("Function Call Type: %v\n", fct)
		if !ct.Equal(fct) {
			spew.Dump(n)
			panic(&interpreterError{msg: fmt.Sprintf("Called function '%s' as %s, but function is type %s", n.sval, ct, fct), p: n.p})
		}
		rt, err := c.functionReturnType(n, v)
		if err != nil {
			panic(&interpreterError{msg: err.Error(), p: n.p})
		}
		//fmt.Printf("Function Return Type: %v\n", rt)
		return rt
	case n_eq:
		sym := n.args[0]
		if sym.t != n_symbol && sym.t != n_dot {
			panic(&interpreterError{msg: fmt.Sprintf("cannot assign to non-variable %s", sym.t), p: n.p})
		}
		val := n.args[1]
		v := validate(val, c)
		if v.Equal(voidType()) {
			panic(&interpreterError{msg: fmt.Sprintf("cannot assign value of type %s.", v), p: n.p})
		}
		sv := validate(sym, c)
		fmt.Printf("Comparing assignment of %#v to %#v\n", sv, v)
		if !sv.Equal(v) {
			panic(&interpreterError{msg: fmt.Sprintf("cannot assign value of type %s to variable of type %s.", v, sv), p: n.p})
		}

		// 		if vo, ok := c.binding(sym.sval); ok {
		// 			if vo != v {
		// 				panic(&interpreterError{msg: fmt.Sprintf("cannot assign value of type %s to variable '%s' of type %s.", v, sym.sval, vo), p: n.p})
		// 			}
		// 		} else {
		// 			panic(&interpreterError{msg: fmt.Sprintf("variable '%s' is not declared.", sym.sval), p: n.p})
		// 			// No longer allow implicit declarations.
		// 			// 			if !c.bind(sym.sval, v) {
		// 			// 				panic(&interpreterError{msg: fmt.Sprintf("failed to bind '%s' to type %s. This is likely a compiler bug.", sym.sval, v), p: n.p})
		// 			// 			}
		// 		}
		return v
	case n_fn:
		ftype := functionTypeName(n, c)
		body := n.args[int(n.fval)+1]
		if _, ok := c.binding(n.sval); ok {
			panic(&interpreterError{msg: fmt.Sprintf("function '%s' already defined.", n.sval), p: n.p})
		} else {
			if !c.bind(n.sval, ftype) {
				panic(&interpreterError{msg: fmt.Sprintf("failed to bind '%s' to type %s. This is likely a compiler bug.", n.sval, ftype), p: n.p})
			}
		}
		c := c.subVContext()
		for i := 0; i < int(n.fval); i++ {
			t, ok := c.typeByName(n.args[i].args[0].sval)
			t.ind = int(n.args[i].args[0].ival)
			if !ok {
				panic(&interpreterError{msg: fmt.Sprintf("No such type %s.", n.args[i].args[0].sval), p: n.args[i].args[0].p})
			}
			c.bind(n.args[i].sval, t)
		}

		validate(body, c)
		// 		bodyType := validate(body, c)
		// 		if bodyType != functionReturnType(n, ftype) {
		// 			panic(&interpreterError{msg: fmt.Sprintf("body returns %s but function declares return type %s", bodyType, functionReturnType(n, ftype)), p: n.p})
		// 		}
		rt, err := c.functionReturnType(n, ftype)
		if err != nil {
			panic(&interpreterError{msg: err.Error(), p: n.p})
		}
		bodyType := validateReturns(body, c, rt)
		if !bodyType.Equal(rt) {
			panic(&interpreterError{msg: fmt.Sprintf("body returns %s but function declares return type %s", bodyType, rt), p: n.p})
		}

		return ftype
	case n_block:
		for _, n := range n.args {
			validate(n, c)
		}
		return voidType()
	case n_return:
		return validate(n.args[0], c)
	case n_if:
		//TODO: Validate the condition type?
		validate(n.args[0], c)
		validate(n.args[1], c)
		if len(n.args) > 2 {
			validate(n.args[2], c)
		}
		return voidType()
	case n_add:
		t1 := validate(n.args[0], c)
		if !t1.Equal(numType()) {
			panic(&interpreterError{msg: fmt.Sprintf("Only numbers can be added, but found %s", t1), p: n.args[0].p})
		}
		t2 := validate(n.args[1], c)
		if !t2.Equal(numType()) {
			panic(&interpreterError{msg: fmt.Sprintf("Only numbers can be added, but found %s", t2), p: n.args[1].p})
		}
		return numType()
	case n_sub:
		t1 := validate(n.args[0], c)
		if !t1.Equal(numType()) {
			panic(&interpreterError{msg: fmt.Sprintf("Only numbers can be subtracted, but found %s", t1), p: n.args[0].p})
		}
		t2 := validate(n.args[1], c)
		if !t2.Equal(numType()) {
			panic(&interpreterError{msg: fmt.Sprintf("Only numbers can be subtracted, but found %s", t2), p: n.args[1].p})
		}
		return numType()
	case n_mul:
		t1 := validate(n.args[0], c)
		if !t1.Equal(numType()) {
			panic(&interpreterError{msg: fmt.Sprintf("Only numbers can be multiplied, but found %s", t1), p: n.args[0].p})
		}
		t2 := validate(n.args[1], c)
		if !t2.Equal(numType()) {
			panic(&interpreterError{msg: fmt.Sprintf("Only numbers can be multiplied, but found %s", t2), p: n.args[1].p})
		}
		return numType()
	case n_div:
		t1 := validate(n.args[0], c)
		if !t1.Equal(numType()) {
			panic(&interpreterError{msg: fmt.Sprintf("Only numbers can be divided, but found %s", t1), p: n.args[0].p})
		}
		t2 := validate(n.args[1], c)
		if !t2.Equal(numType()) {
			panic(&interpreterError{msg: fmt.Sprintf("Only numbers can be divided, but found %s", t2), p: n.args[1].p})
		}
		return numType()
	case n_lt:
		t1 := validate(n.args[0], c)
		if !t1.Equal(numType()) {
			panic(&interpreterError{msg: fmt.Sprintf("Only numbers can be compared, but found %s", t1), p: n.args[0].p})
		}
		t2 := validate(n.args[1], c)
		if !t2.Equal(numType()) {
			panic(&interpreterError{msg: fmt.Sprintf("Only numbers can be compared, but found %s", t2), p: n.args[1].p})
		}
		return numType()
	case n_le:
		t1 := validate(n.args[0], c)
		if !t1.Equal(numType()) {
			panic(&interpreterError{msg: fmt.Sprintf("Only numbers can be compared, but found %s", t1), p: n.args[0].p})
		}
		t2 := validate(n.args[1], c)
		if !t2.Equal(numType()) {
			panic(&interpreterError{msg: fmt.Sprintf("Only numbers can be compared, but found %s", t2), p: n.args[1].p})
		}
		return numType()
	case n_gt:
		t1 := validate(n.args[0], c)
		if !t1.Equal(numType()) {
			panic(&interpreterError{msg: fmt.Sprintf("Only numbers can be compared, but found %s", t1), p: n.args[0].p})
		}
		t2 := validate(n.args[1], c)
		if !t2.Equal(numType()) {
			panic(&interpreterError{msg: fmt.Sprintf("Only numbers can be compared, but found %s", t2), p: n.args[1].p})
		}
		return numType()
	case n_ge:
		t1 := validate(n.args[0], c)
		if !t1.Equal(numType()) {
			panic(&interpreterError{msg: fmt.Sprintf("Only numbers can be compared, but found %s", t1), p: n.args[0].p})
		}
		t2 := validate(n.args[1], c)
		if !t2.Equal(numType()) {
			panic(&interpreterError{msg: fmt.Sprintf("Only numbers can be compared, but found %s", t2), p: n.args[1].p})
		}
		return numType()
	case n_struct:
		for _, field := range n.args {
			if _, ok := c.typeByName(field.args[0].sval); !ok {
				panic(&interpreterError{msg: fmt.Sprintf("Type %s undefined.", field.args[0].sval), p: field.args[0].p})
			}
		}
		// 		fmt.Printf("DECLARING TYPE %s", n.sval)
		// 		if !c.defType(n.sval, BType{name: n.sval}) {
		// 			panic(&interpreterError{msg: fmt.Sprintf("Type %s already defined.", n.sval), p: n.p})
		// 		}
		sd := BType{name: n.sval}
		for _, field := range n.args {
			//fmt.Printf("FIELD ### %s field %d -> name: %s, type: %s\n", n.sval, i, field.sval, field.args[0].sval)
			t, ok := c.typeByName(field.args[0].sval)
			if !ok {
				panic(&interpreterError{msg: fmt.Sprintf("No such type %s in field", field.args[0].sval), p: field.args[0].p})
			}
			sd.addField(field.sval, t)
		}
		//fmt.Printf("DECLARING STRUCT %s\n", n.sval)
		if ok := c.defType(n.sval, sd); !ok {
			panic(&interpreterError{msg: fmt.Sprintf("Type %s already exists.", n.sval), p: n.p})
		}
		return voidType()
	case n_stlit:
		t, ok := c.typeByName(n.sval)
		if !ok {
			panic(&interpreterError{msg: fmt.Sprintf("No such type %s.", n.sval)})
		}
		return t
	case n_var:
		if vo, ok := c.binding(n.sval); ok {
			panic(&interpreterError{msg: fmt.Sprintf("var %s already declared with type %s.", n.sval, vo), p: n.p})
		}
		// No longer allow implicit declarations.
		v := validate(n.args[0], c)
		if !c.bind(n.sval, v) {
			panic(&interpreterError{msg: fmt.Sprintf("failed to bind '%s' to type %s. This is likely a compiler bug.", n.sval, v), p: n.p})
		}
		return voidType()
	case n_typename:
		t, ok := c.typeByName(n.sval)
		if !ok {
			panic(&interpreterError{msg: fmt.Sprintf("no such type '%s'.", n.sval), p: n.p})
		}
		t.ind = int(n.ival)
		if len(n.args) > 0 {
			idx := n.args[0].args[0]
			if idx.t != n_index {
				panic(fmt.Sprintf("Bad variable declaration with argument %#v", idx))
			}
			fmt.Printf("Variable %v is an array of %d elements.\n", n.sval, idx.ival)
			if idx.ival > 0 {
				t.ind++
			}
		}
		return t
	case n_dot:
		//fmt.Printf("##### N_DOT #####\n")
		v := validate(n.args[0], c)
		//fmt.Printf("V: ")
		//spew.Dump(v)
		//fmt.Printf("N: ")
		//spew.Dump(n)
		// 		vo, ok := c.binding(n.args[0].sval)
		// 		if !ok {
		// 			panic(&interpreterError{msg: fmt.Sprintf("%s not declared", n.args[0].sval), p: n.p})
		// 		}
		vo := v
		// 		st, ok := c.structFor(vo)
		// 		if !ok {
		// 			panic(&interpreterError{msg: fmt.Sprintf("%s is not a struct type", vo), p: n.p})
		// 		}
		st := vo
		//fmt.Printf("TYPE: %#v\n", st)
		ft, ok := st.fields[n.args[1].sval]
		if !ok {
			panic(&interpreterError{msg: fmt.Sprintf("%s is not a field ", n.args[1].sval), p: n.p})
		}
		return ft.t
	case n_index:
		t, ok := c.binding(n.sval)
		if !ok {
			panic(&interpreterError{msg: fmt.Sprintf("%s is not bound to a type.", n.sval), p: n.p})
		}
		if t.ind != 0 {
			panic(&interpreterError{msg: fmt.Sprintf("cannot yet index into pointers (%s).", n.sval), p: n.p})
		}
		if t.name != "str" {
			panic(&interpreterError{msg: fmt.Sprintf("can only index into strings for now (%s).", n.sval), p: n.p})
		}
		return byteType()
	case n_address:
		t, ok := c.binding(n.sval)
		if !ok {
			panic(&interpreterError{msg: fmt.Sprintf("%s is not bound to a type.", n.sval), p: n.p})
		}
		t.ind++
		return t
	case n_deref:
		t, ok := c.binding(n.sval)
		if !ok {
			panic(&interpreterError{msg: fmt.Sprintf("%s is not bound to a type.", n.sval), p: n.p})
		}
		if t.ind == 0 {
			panic(&interpreterError{msg: fmt.Sprintf("%s is not a pointer type.", n.sval), p: n.p})
		}
		t.ind--
		return t
	default:
		fmt.Printf("NOT IMPLEMENTED FOR:\n")
		spew.Dump(n)
		panic("NOT IMPLEMENTED")
	}
	panic("FORGOT TO RETURN")
	return voidType()
	// 		return "n_funcall"
	// 	case n_index:
	// 		return "n_index"
	// 	case n_struct:
	// 		return "n_struct"
	// 	case n_stlit:
	// 		return "n_stlit"
	// 	case n_stfield:
	// 		return "n_stfield"
	// 	case n_fn:
	// 		return "n_fn"
	// 	case n_arg:
	// 		return "n_arg"
	// 	case n_dot:
	// 		return "n_dot"
	// 	case n_mul:
	// 		return "n_mul"
	// 	case n_div:
	// 		return "n_div"
	// 	case n_add:
	// 		return "n_add"
	// 	case n_sub:
	// 		return "n_sub"
	// 	case n_eq:
	// 		return "n_eq"
	// 	case n_deq:
	// 		return "n_deq"
	// 	case n_lt:
	// 		return "n_lt"
	// 	case n_gt:
	// 		return "n_gt"
	// 	case n_le:
	// 		return "n_le"
	// 	case n_ge:
	// 		return "n_ge"
	// 	case n_if:
	// 		return "n_if"
	// 	case n_for:
	// 		return "n_for"
	//case n_block:
	//
	// 	case n_break:
	// 		return "n_break"
	// 	case n_return:
	// 		return "n_return"
	// 	}
	// 	return "UNKNOWN"
}

func Validate(n *Node, c *VContext) (e error) {
	defer func() {
		if err := recover(); err != nil {
			if ee, ok := err.(*interpreterError); ok {
				e = ee
				return
			}
			panic(err)
		}
	}()
	validate(n, c)
	return nil
}
