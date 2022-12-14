package main

import (
	"fmt"
	"strings"

	"github.com/davecgh/go-spew/spew"
	"github.com/knusbaum/gbasm"
)

type typename struct {
	name string
	ind  int
}

func (tn typename) String() string {
	var b strings.Builder
	for i := 0; i < tn.ind; i++ {
		fmt.Fprintf(&b, "*")
	}
	b.WriteString(tn.name)
	return b.String()
}

func tn_void() typename {
	return typename{name: "void"}
}

func tn_num() typename {
	return typename{name: "num"}
}

func tn_str() typename {
	return typename{name: "str"}
}

type structDef struct {
	fields map[string]typename
}

func (s *structDef) addField(name string, t typename) error {
	if s.fields == nil {
		s.fields = make(map[string]typename)
	}
	if _, ok := s.fields[name]; ok {
		return fmt.Errorf("field %s already defined.", name)
	}
	s.fields[name] = t
	return nil
}

type VContext struct {
	parent     *VContext
	bindType   map[string]typename
	validTypes map[string]typename
	structs    map[typename]*structDef
}

func NewVContext() *VContext {
	return &VContext{
		bindType: make(map[string]typename),
		validTypes: map[string]typename{
			"void": tn_void(),
			"num":  tn_num(),
			"str":  tn_str(),
		},
		structs: make(map[typename]*structDef),
	}
}

func (c *VContext) subVContext() *VContext {
	nv := NewVContext()
	nv.parent = c
	return nv
}

func (c *VContext) binding(n string) (typename, bool) {
	if tn, ok := c.bindType[n]; ok {
		return tn, ok
	}
	if c.parent != nil {
		return c.parent.binding(n)
	}
	return tn_void(), false
}

func (c *VContext) bind(n string, t typename) bool {
	if ot, ok := c.bindType[n]; ok {
		if t != ot {
			return false
		}
		return true
	}
	c.bindType[n] = t
	return true
}

func (c *VContext) structFor(t typename) (*structDef, bool) {
	//fmt.Printf("STRUCTS: %v\n", c.structs)
	if d, ok := c.structs[t]; ok {
		return d, ok
	}
	if c.parent != nil {
		return c.parent.structFor(t)
	}
	return nil, false
}

func (c *VContext) declStruct(t typename, d *structDef) error {
	if _, ok := c.structs[t]; ok {
		return fmt.Errorf("struct %s already declared.", t)
	}
	c.structs[t] = d
	//fmt.Printf("STRUCTS: %v\n", c.structs)
	return nil
}

func (c *VContext) typeFor(t string) (typename, bool) {
	if tn, ok := c.validTypes[t]; ok {
		return tn, ok
	}
	if c.parent != nil {
		return c.parent.typeFor(t)
	}
	return tn_void(), false
}

func (c *VContext) defType(n string, t typename) bool {
	if ot, ok := c.typeFor(n); ok {
		if t != ot {
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
			if !c.bind(t.Name, typename{name: t.Type}) {
				return fmt.Errorf("%s already defined.", v)
			}
			fmt.Printf("IMPORTED %s %s\n", t.Name, t.Type)
		}
	}
	return nil
}

// TODO: unit test
func functionTypeName(n *Node) typename {
	if n.t != n_fn {
		panic(&interpreterError{msg: fmt.Sprintf("cannot determine function type name of non-function %#v. This is a compiler error.", n), p: n.p})
	}
	var b strings.Builder
	b.WriteString("fn(")
	for i := 0; i < int(n.nval); i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(n.args[i].args[0].sval)
	}
	b.WriteString(") ")
	//spew.Dump(n.args[0])
	b.WriteString(n.args[int(n.nval)].sval)
	return typename{name: b.String()}
}

// TODO: unit test
func functionTypeNameFromCall(n *Node, c *VContext) typename {
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
	return typename{name: b.String()}
}

// TODO: unit test
func functionReturnType(n *Node, tn typename) typename {
	if !strings.HasPrefix(tn.String(), "fn(") {
		panic(&interpreterError{msg: fmt.Sprintf("cannot determine function type name of non-function %#v. This is a compiler error.", n), p: n.p})
	}
	return typename{name: strings.Split(tn.String(), " ")[1]}
}

// TODO: unit test
func functionCallType(n *Node, tn typename) typename {
	if !strings.HasPrefix(tn.String(), "fn(") {
		panic(&interpreterError{msg: fmt.Sprintf("cannot determine function type name of non-function %#v. This is a compiler error.", n), p: n.p})
	}
	return typename{name: strings.Split(tn.String(), " ")[0]}
}

func validateReturns(n *Node, c *VContext, t typename) typename {
	switch n.t {
	case n_return:
		rt := validate(n.args[0], c)
		if rt != t {
			panic(&interpreterError{msg: fmt.Sprintf("Expected a return of type %s but found %s.", t, rt), p: n.p})
		}
		return rt
	case n_if:
		//TODO: Validate the condition?
		t1 := validateReturns(n.args[1], c, t)
		if len(n.args) > 2 {
			t2 := validateReturns(n.args[2], c, t)
			if t1 == t2 {
				return t1
			} else {
				panic(&interpreterError{msg: fmt.Sprintf("If branches return different types %s and %s", t1, t2), p: n.p})
			}
		}
		return t1
	case n_block:
		for _, n := range n.args {
			if brv := validateReturns(n, c, t); brv != tn_void() {
				return brv
			}
		}
		return tn_void()
	default:
		return tn_void()
	}
}

// validate recursively checks the AST rooted at *Node and returns resulting Value Type (encoded in
// a Value) for the Node. The return type is a Value as opposed to a ValType because the ValType is
// not sufficient to describe complex types such as functions and structs. To make sure a function
// or struct matches another, we need a Value with the fnval or s fields set, containing the
// details (parameters, return types, fields) of the function or struct.
func validate(n *Node, c *VContext) typename {
	switch n.t {
	case n_none:
		return tn_void()
	case n_number:
		return tn_num()
	case n_str:
		return tn_str()
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
		if ct != fct {
			spew.Dump(n)
			panic(&interpreterError{msg: fmt.Sprintf("Called function '%s' as %s, but function is type %s", n.sval, ct, fct), p: n.p})
		}
		rt := functionReturnType(n, v)
		//fmt.Printf("Function Return Type: %v\n", rt)
		return rt
	case n_eq:
		sym := n.args[0]
		if sym.t != n_symbol && sym.t != n_dot {
			panic(&interpreterError{msg: fmt.Sprintf("cannot assign to non-variable %s", sym.t), p: n.p})
		}
		val := n.args[1]
		v := validate(val, c)
		if v == tn_void() {
			panic(&interpreterError{msg: fmt.Sprintf("cannot assign value of type %s.", v), p: n.p})
		}
		sv := validate(sym, c)
		if sv != v {
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
		ftype := functionTypeName(n)
		body := n.args[int(n.nval)+1]
		if _, ok := c.binding(n.sval); ok {
			panic(&interpreterError{msg: fmt.Sprintf("function '%s' already defined.", n.sval), p: n.p})
		} else {
			if !c.bind(n.sval, ftype) {
				panic(&interpreterError{msg: fmt.Sprintf("failed to bind '%s' to type %s. This is likely a compiler bug.", n.sval, ftype), p: n.p})
			}
		}
		c := c.subVContext()
		for i := 0; i < int(n.nval); i++ {
			c.bind(n.args[i].sval, typename{name: n.args[i].args[0].sval})
		}

		validate(body, c)
		// 		bodyType := validate(body, c)
		// 		if bodyType != functionReturnType(n, ftype) {
		// 			panic(&interpreterError{msg: fmt.Sprintf("body returns %s but function declares return type %s", bodyType, functionReturnType(n, ftype)), p: n.p})
		// 		}
		bodyType := validateReturns(body, c, functionReturnType(n, ftype))
		if bodyType != functionReturnType(n, ftype) {
			panic(&interpreterError{msg: fmt.Sprintf("body returns %s but function declares return type %s", bodyType, functionReturnType(n, ftype)), p: n.p})
		}

		return ftype
	case n_block:
		for _, n := range n.args {
			validate(n, c)
		}
		return tn_void()
	case n_return:
		return validate(n.args[0], c)
	case n_if:
		//TODO: Validate the condition type?
		validate(n.args[0], c)
		validate(n.args[1], c)
		if len(n.args) > 2 {
			validate(n.args[2], c)
		}
		return tn_void()
	case n_add:
		t1 := validate(n.args[0], c)
		if t1 != tn_num() {
			panic(&interpreterError{msg: fmt.Sprintf("Only numbers can be added, but found %s", t1), p: n.args[0].p})
		}
		t2 := validate(n.args[1], c)
		if t2 != tn_num() {
			panic(&interpreterError{msg: fmt.Sprintf("Only numbers can be added, but found %s", t2), p: n.args[1].p})
		}
		return tn_num()
	case n_sub:
		t1 := validate(n.args[0], c)
		if t1 != tn_num() {
			panic(&interpreterError{msg: fmt.Sprintf("Only numbers can be subtracted, but found %s", t1), p: n.args[0].p})
		}
		t2 := validate(n.args[1], c)
		if t2 != tn_num() {
			panic(&interpreterError{msg: fmt.Sprintf("Only numbers can be subtracted, but found %s", t2), p: n.args[1].p})
		}
		return tn_num()
	case n_mul:
		t1 := validate(n.args[0], c)
		if t1 != tn_num() {
			panic(&interpreterError{msg: fmt.Sprintf("Only numbers can be multiplied, but found %s", t1), p: n.args[0].p})
		}
		t2 := validate(n.args[1], c)
		if t2 != tn_num() {
			panic(&interpreterError{msg: fmt.Sprintf("Only numbers can be multiplied, but found %s", t2), p: n.args[1].p})
		}
		return tn_num()
	case n_div:
		t1 := validate(n.args[0], c)
		if t1 != tn_num() {
			panic(&interpreterError{msg: fmt.Sprintf("Only numbers can be divided, but found %s", t1), p: n.args[0].p})
		}
		t2 := validate(n.args[1], c)
		if t2 != tn_num() {
			panic(&interpreterError{msg: fmt.Sprintf("Only numbers can be divided, but found %s", t2), p: n.args[1].p})
		}
		return tn_num()
	case n_lt:
		t1 := validate(n.args[0], c)
		if t1 != tn_num() {
			panic(&interpreterError{msg: fmt.Sprintf("Only numbers can be divided, but found %s", t1), p: n.args[0].p})
		}
		t2 := validate(n.args[1], c)
		if t2 != tn_num() {
			panic(&interpreterError{msg: fmt.Sprintf("Only numbers can be divided, but found %s", t2), p: n.args[1].p})
		}
		return tn_num()
	case n_le:
		t1 := validate(n.args[0], c)
		if t1 != tn_num() {
			panic(&interpreterError{msg: fmt.Sprintf("Only numbers can be compared, but found %s", t1), p: n.args[0].p})
		}
		t2 := validate(n.args[1], c)
		if t2 != tn_num() {
			panic(&interpreterError{msg: fmt.Sprintf("Only numbers can be compared, but found %s", t2), p: n.args[1].p})
		}
		return tn_num()
	case n_gt:
		t1 := validate(n.args[0], c)
		if t1 != tn_num() {
			panic(&interpreterError{msg: fmt.Sprintf("Only numbers can be compared, but found %s", t1), p: n.args[0].p})
		}
		t2 := validate(n.args[1], c)
		if t2 != tn_num() {
			panic(&interpreterError{msg: fmt.Sprintf("Only numbers can be compared, but found %s", t2), p: n.args[1].p})
		}
		return tn_num()
	case n_ge:
		t1 := validate(n.args[0], c)
		if t1 != tn_num() {
			panic(&interpreterError{msg: fmt.Sprintf("Only numbers can be compared, but found %s", t1), p: n.args[0].p})
		}
		t2 := validate(n.args[1], c)
		if t2 != tn_num() {
			panic(&interpreterError{msg: fmt.Sprintf("Only numbers can be compared, but found %s", t2), p: n.args[1].p})
		}
		return tn_num()
	case n_struct:
		for _, field := range n.args {
			if _, ok := c.typeFor(field.args[0].sval); !ok {
				panic(&interpreterError{msg: fmt.Sprintf("Type %s undefined.", field.args[0].sval), p: field.args[0].p})
			}
		}
		fmt.Printf("DELCALING TYPE %s", n.sval)
		if !c.defType(n.sval, typename{name: n.sval}) {
			panic(&interpreterError{msg: fmt.Sprintf("Type %s already defined.", n.sval), p: n.p})
		}
		var sd structDef
		for i, field := range n.args {
			fmt.Printf("FIELD ### %s field %d -> name: %s, type: %s\n", n.sval, i, field.sval, field.args[0].sval)
			sd.addField(field.sval, typename{name: field.args[0].sval})
		}
		fmt.Printf("DECLARING STRUCT %s\n", n.sval)
		if err := c.declStruct(typename{name: n.sval}, &sd); err != nil {
			panic(&interpreterError{msg: err.Error(), p: n.p})
		}
		return tn_void()
	case n_stlit:
		t, ok := c.typeFor(n.sval)
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
		return tn_void()
	case n_typename:
		t, ok := c.typeFor(n.sval)
		if !ok {
			panic(&interpreterError{msg: fmt.Sprintf("no such type '%s'.", n.sval), p: n.p})
		}
		t.ind = int(n.nval)
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
		st, ok := c.structFor(vo)
		if !ok {
			panic(&interpreterError{msg: fmt.Sprintf("%s is not a struct type", vo), p: n.p})
		}
		//fmt.Printf("TYPE: %#v\n", st)
		ft, ok := st.fields[n.args[1].sval]
		if !ok {
			panic(&interpreterError{msg: fmt.Sprintf("%s is not a field ", n.args[1].sval), p: n.p})
		}
		return ft
	default:
		fmt.Printf("NOT IMPLEMENTED FOR:\n")
		spew.Dump(n)
		panic("NOT IMPLEMENTED")
	}
	panic("FORGOT TO RETURN")
	return tn_void()
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
