package main

import "fmt"

type ValType int

const (
	vt_none ValType = iota
	vt_num
	vt_str
	vt_fn
	vt_builtin
	vt_struct
)

type Value struct {
	T       ValType
	nval    float64
	sval    string
	fnval   *Node
	builtin interface{}
	s       *structVal
}

type structVal struct {
	T    string
	vals map[string]Value
}

//
//func (v *Value) Trueish() bool {
//	switch v.T {
//	case vt_num:
//		return v.nval != 0
//	case vt_str:
//		return v.sval != ""
//	case vt_fn:
//		return true
//	}
//	return true
//}
//
//func (v Value) String() string {
//	switch v.T {
//	case vt_none:
//		return "void"
//	case vt_num:
//		return fmt.Sprintf("%v", v.nval)
//	case vt_str:
//		return v.sval
//	case vt_fn:
//		return fmt.Sprintf("function of %v args", v.fnval.nval)
//	case vt_builtin:
//		return "built-in function"
//	case vt_struct:
//		var bs strings.Builder
//		fmt.Fprintf(&bs, "%s{", v.s.T)
//		for k, v := range v.s.vals {
//			fmt.Fprintf(&bs, "%s: %s, ", k, v)
//		}
//		bs.WriteString("}")
//		return bs.String()
//	}
//	return "[UNKNOWN VALUE]"
//}
//
//type Return struct {
//	v Value
//}
//
//type Break struct{}

type interpreterError struct {
	msg string
	p   position
}

func (e *interpreterError) Error() string {
	return fmt.Sprintf("at %s: %s", e.p, e.msg)
}

//type ExecContext struct {
//	global   *ExecContext
//	parent   *ExecContext
//	bindings map[string]*Value
//	types    map[string]*structType
//}
//
//func NewExecContext() *ExecContext {
//	return &ExecContext{bindings: make(map[string]*Value), types: make(map[string]*structType)}
//}
//
//func subExecContext(ctx *ExecContext) *ExecContext {
//	if ctx.global == nil {
//		// We're global
//		return &ExecContext{global: ctx, bindings: make(map[string]*Value)}
//	}
//	return &ExecContext{global: ctx.global, parent: ctx, bindings: make(map[string]*Value)}
//}
//
//func funcExecContext(ctx *ExecContext) *ExecContext {
//	// For this we want to eliminate any local lexical context but keep the global context.
//	if ctx.global == nil {
//		// We're global
//		return &ExecContext{global: ctx, bindings: make(map[string]*Value)}
//	}
//	return &ExecContext{global: ctx.global, bindings: make(map[string]*Value)}
//}
//
//func (c *ExecContext) binding(n string) (*Value, bool) {
//	if v, ok := c.bindings[n]; ok {
//		return v, ok
//	}
//	if c.parent != nil {
//		return c.parent.binding(n)
//	}
//	if c.global != nil {
//		return c.global.binding(n)
//	}
//	return nil, false
//}
//
//func (c *ExecContext) bind(n string, v Value) {
//	if _, ok := c.bindings[n]; ok {
//		c.bindings[n] = &v
//	}
//	if c.parent != nil {
//		c.parent.bind(n, v)
//	}
//	if c.global != nil {
//		c.global.bind(n, v)
//	}
//	c.bindings[n] = &v
//}
//
//func (c *ExecContext) localBind(n string, v Value) {
//	c.bindings[n] = &v
//}
//
//func (c *ExecContext) addStructDef(s *structType) error {
//	if c.global != nil {
//		return c.global.addStructDef(s)
//	}
//	if _, ok := c.types[s.T]; ok {
//		return fmt.Errorf("struct %s already defined.\n", s.T)
//	}
//	c.types[s.T] = s
//	return nil
//}
//
//func (c *ExecContext) getStructDef(name string) (*structType, bool) {
//	if c.global != nil {
//		return c.global.getStructDef(name)
//	}
//	v, ok := c.types[name]
//	return v, ok
//}
//
//func (n *Node) execBuiltin(ctx *ExecContext, fn Value) Value {
//	v := reflect.ValueOf(fn.builtin)
//	if v.Kind() != reflect.Func {
//		panic(&interpreterError{msg: fmt.Sprintf("built-in %s is not actually a function. This is an interpreter bug.", n.sval), p: n.p})
//	}
//	if expect := v.Type().NumIn(); expect != len(n.args)+1 {
//		panic(&interpreterError{msg: fmt.Sprintf("function %s expects %d args, but %d were passed.", n.sval, expect-1, len(n.args)), p: n.p})
//	}
//
//	ec := funcExecContext(ctx)
//	args := []reflect.Value{reflect.ValueOf(ec)}
//	for _, arg := range n.args {
//		v := arg.exec(ctx)
//		args = append(args, reflect.ValueOf(v))
//	}
//	ret := v.Call(args)
//	if len(ret) > 1 {
//		panic(&interpreterError{msg: fmt.Sprintf("expected single return value from built-in %s but found %d", n.sval, len(ret)), p: n.p})
//	}
//	r := ret[0]
//	rv, ok := r.Interface().(Value)
//	if !ok {
//		panic(&interpreterError{msg: fmt.Sprintf("expected built-in %s to return a value but found %#v", n.sval, r.Interface()), p: n.p})
//	}
//	return rv
//}
//
//func (n *Node) execFuncall(ctx *ExecContext) (v Value) {
//	fn, ok := ctx.binding(n.sval)
//	if !ok {
//		panic(&interpreterError{msg: fmt.Sprintf("%s is not bound", n.sval), p: n.p})
//	}
//	if fn.T == vt_builtin {
//		return n.execBuiltin(ctx, *fn)
//	}
//	if fn.T != vt_fn {
//		panic(&interpreterError{msg: fmt.Sprintf("cannot call non-function %s: (%s)", n.sval, fn), p: n.p})
//	}
//
//	// Structure of an n_fn node args:
//	// arg 0-n.nval parameters
//	// arg n.nval - return type
//	// arg n.nval+1 - body nodes
//	if len(n.args) != int(fn.fnval.nval) {
//		panic(&interpreterError{msg: fmt.Sprintf("function %s expects %v args, but %d were passed.", n.sval, fn.fnval.nval, len(n.args)), p: n.p})
//	}
//
//	ec := funcExecContext(ctx)
//	for i, arg := range n.args {
//		v := arg.exec(ctx)
//		ec.localBind(fn.fnval.args[i].sval, v)
//	}
//	body := fn.fnval.args[int(fn.fnval.nval)+1]
//	defer func() {
//		if err := recover(); err != nil {
//			if ee, ok := err.(*Return); ok {
//				v = ee.v
//				return
//			}
//			panic(err)
//		}
//	}()
//	ret := body.exec(ec)
//	return ret
//}
//
//func (n *Node) execStruct(ctx *ExecContext) Value {
//	name := n.sval
//	m := make(map[string]string)
//	for _, field := range n.args {
//		fieldname := field.sval
//		fieldtype := field.args[0].sval
//		m[fieldname] = fieldtype
//	}
//	t := structType{
//		T:      name,
//		fields: m,
//	}
//	err := ctx.addStructDef(&t)
//	if err != nil {
//		panic(&interpreterError{msg: err.Error(), p: n.p})
//	}
//	return Value{}
//}
//
//func (n *Node) execStructLiteral(ctx *ExecContext) Value {
//	typename := n.sval
//	t, ok := ctx.getStructDef(typename)
//	if !ok {
//		panic(&interpreterError{msg: fmt.Sprintf("No such struct type %s", typename), p: n.p})
//	}
//	m := make(map[string]Value)
//	for _, field := range n.args {
//		fieldname := field.sval
//		if _, ok := t.fields[fieldname]; !ok {
//			panic(&interpreterError{msg: fmt.Sprintf("Struct type %s does not contain field %s", typename, fieldname), p: n.p})
//		}
//		fieldval := field.args[0].exec(ctx)
//		m[fieldname] = fieldval
//	}
//	return Value{T: vt_struct, s: &structVal{T: typename, vals: m}}
//}
//
//func (n *Node) execIndex(ctx *ExecContext) Value {
//	v1, ok := ctx.binding(n.sval)
//	if !ok {
//		panic(&interpreterError{msg: fmt.Sprintf("%s is not bound.", n.sval), p: n.p})
//	}
//	if v1.T != vt_struct {
//		panic(&interpreterError{msg: fmt.Sprintf("index operator is only implemented for structs, but have %s", v1), p: n.p})
//	}
//	v2 := n.args[0].exec(ctx)
//	if v2.T != vt_str {
//		panic(&interpreterError{msg: fmt.Sprintf("structs only accept strings for indexing, but have %s", v2), p: n.p})
//	}
//	if v, ok := v1.s.vals[v2.sval]; ok {
//		return v
//	}
//	panic(&interpreterError{msg: fmt.Sprintf("no such field %s in struct %s", v2.sval, v1.s.T), p: n.p})
//}
//
//func (n *Node) exec(ctx *ExecContext) Value {
//	switch n.t {
//	case n_number:
//		return Value{T: vt_num, nval: n.nval}
//	case n_str:
//		return Value{T: vt_str, sval: n.sval}
//	case n_symbol:
//		if v, ok := ctx.binding(n.sval); ok {
//			return *v
//		} else {
//			panic(&interpreterError{msg: fmt.Sprintf("%s is not bound", n.sval), p: n.p})
//		}
//	case n_funcall:
//		return n.execFuncall(ctx)
//	case n_index:
//		return n.execIndex(ctx)
//	case n_struct:
//		return n.execStruct(ctx)
//	case n_stlit:
//		return n.execStructLiteral(ctx)
//	case n_stfield:
//		panic("n_stfield NOT IMPLEMENTED")
//	case n_fn:
//		// TODO: Validate the function types, body, etc.
//		return Value{T: vt_fn, fnval: n}
//	case n_arg:
//		panic("n_arg NOT IMPLEMENTED")
//	case n_dot:
//		v1 := n.args[0].exec(ctx)
//		if v1.T != vt_struct {
//			panic(&interpreterError{msg: fmt.Sprintf("we can only get fields from a struct, but have %s", v1), p: n.p})
//		}
//		name := n.args[1].sval
//		if v, ok := v1.s.vals[name]; ok {
//			return v
//		}
//		panic(&interpreterError{msg: fmt.Sprintf("no such field %s in struct %s", name, v1.s.T), p: n.p})
//	case n_mul:
//		v1 := n.args[0].exec(ctx)
//		v2 := n.args[1].exec(ctx)
//		if v1.T != vt_num || v2.T != vt_num {
//			panic(&interpreterError{msg: fmt.Sprintf("we can only multiply numbers, but have %s, %s", v1, v2), p: n.p})
//		}
//		return Value{T: vt_num, nval: v1.nval * v2.nval}
//	case n_div:
//		v1 := n.args[0].exec(ctx)
//		v2 := n.args[1].exec(ctx)
//		if v1.T != vt_num || v2.T != vt_num {
//			panic(&interpreterError{msg: fmt.Sprintf("we can only divide numbers, but have %s, %s", v1, v2), p: n.p})
//		}
//		return Value{T: vt_num, nval: v1.nval / v2.nval}
//	case n_add:
//		v1 := n.args[0].exec(ctx)
//		v2 := n.args[1].exec(ctx)
//		if v1.T != vt_num || v2.T != vt_num {
//			panic(&interpreterError{msg: fmt.Sprintf("we can only add numbers, but have %s, %s", v1, v2), p: n.p})
//		}
//		return Value{T: vt_num, nval: v1.nval + v2.nval}
//	case n_sub:
//		v1 := n.args[0].exec(ctx)
//		v2 := n.args[1].exec(ctx)
//		if v1.T != vt_num || v2.T != vt_num {
//			panic(&interpreterError{msg: fmt.Sprintf("we can only subtract numbers, but have %s, %s", v1, v2), p: n.p})
//		}
//		return Value{T: vt_num, nval: v1.nval - v2.nval}
//	case n_eq:
//		if n.args[0].t != n_symbol {
//			panic(&interpreterError{msg: fmt.Sprintf("cannot assign to non-symbol: %v", n.args[0]), p: n.p})
//		}
//		v := n.args[1].exec(ctx)
//		ctx.bind(n.args[0].sval, v)
//		return v
//	case n_deq:
//		v1 := n.args[0].exec(ctx)
//		v2 := n.args[1].exec(ctx)
//		if v1.T != v2.T {
//			panic(&interpreterError{msg: fmt.Sprintf("we can only compare values of the same type, but have %s, %s", v1, v2), p: n.p})
//		}
//		if v1.T == vt_num {
//			if v1.nval == v2.nval {
//				return Value{T: vt_num, nval: 1}
//			}
//			return Value{T: vt_num, nval: 0}
//		} else if v1.T == vt_str {
//			if v1.sval == v2.sval {
//				return Value{T: vt_num, nval: 1}
//			}
//			return Value{T: vt_num, nval: 0}
//		} else {
//			if v1.fnval == v2.fnval {
//				return Value{T: vt_num, nval: 1}
//			}
//			return Value{T: vt_num, nval: 0}
//		}
//	case n_lt:
//		v1 := n.args[0].exec(ctx)
//		v2 := n.args[1].exec(ctx)
//		if v1.T != v2.T {
//			panic(&interpreterError{msg: fmt.Sprintf("we can only compare values of the same type, but have %s, %s", v1, v2), p: n.p})
//		}
//		if v1.T == vt_num {
//			if v1.nval < v2.nval {
//				return Value{T: vt_num, nval: 1}
//			}
//			return Value{T: vt_num, nval: 0}
//		} else if v1.T == vt_str {
//			if v1.sval < v2.sval {
//				return Value{T: vt_num, nval: 1}
//			}
//			return Value{T: vt_num, nval: 0}
//		} else if v1.T == vt_fn {
//			panic(&interpreterError{msg: fmt.Sprintf("we cannot compare functions for order: %s, %s", v1, v2), p: n.p})
//			if v1.fnval == v2.fnval {
//				return Value{T: vt_num, nval: 1}
//			}
//			return Value{T: vt_num, nval: 0}
//		}
//		panic(&interpreterError{msg: fmt.Sprintf("we cannot compare for order: %s, %s", v1, v2), p: n.p})
//	case n_gt:
//		v1 := n.args[0].exec(ctx)
//		v2 := n.args[1].exec(ctx)
//		if v1.T != v2.T {
//			panic(&interpreterError{msg: fmt.Sprintf("we can only compare values of the same type, but have %s, %s", v1, v2), p: n.p})
//		}
//		if v1.T == vt_num {
//			if v1.nval > v2.nval {
//				return Value{T: vt_num, nval: 1}
//			}
//			return Value{T: vt_num, nval: 0}
//		} else if v1.T == vt_str {
//			if v1.sval > v2.sval {
//				return Value{T: vt_num, nval: 1}
//			}
//			return Value{T: vt_num, nval: 0}
//		} else if v1.T == vt_fn {
//			panic(&interpreterError{msg: fmt.Sprintf("we cannot compare functions for order: %s, %s", v1, v2), p: n.p})
//			if v1.fnval == v2.fnval {
//				return Value{T: vt_num, nval: 1}
//			}
//			return Value{T: vt_num, nval: 0}
//		}
//		panic(&interpreterError{msg: fmt.Sprintf("we cannot compare for order: %s, %s", v1, v2), p: n.p})
//	case n_le:
//		v1 := n.args[0].exec(ctx)
//		v2 := n.args[1].exec(ctx)
//		if v1.T != v2.T {
//			panic(&interpreterError{msg: fmt.Sprintf("we can only compare values of the same type, but have %s, %s", v1, v2), p: n.p})
//		}
//		if v1.T == vt_num {
//			if v1.nval <= v2.nval {
//				return Value{T: vt_num, nval: 1}
//			}
//			return Value{T: vt_num, nval: 0}
//		} else if v1.T == vt_str {
//			if v1.sval <= v2.sval {
//				return Value{T: vt_num, nval: 1}
//			}
//			return Value{T: vt_num, nval: 0}
//		} else if v1.T == vt_fn {
//			panic(&interpreterError{msg: fmt.Sprintf("we cannot compare functions for order: %s, %s", v1, v2), p: n.p})
//			if v1.fnval == v2.fnval {
//				return Value{T: vt_num, nval: 1}
//			}
//			return Value{T: vt_num, nval: 0}
//		}
//		panic(&interpreterError{msg: fmt.Sprintf("we cannot compare for order: %s, %s", v1, v2), p: n.p})
//	case n_ge:
//		v1 := n.args[0].exec(ctx)
//		v2 := n.args[1].exec(ctx)
//		if v1.T != v2.T {
//			panic(&interpreterError{msg: fmt.Sprintf("we can only compare values of the same type, but have %s, %s", v1, v2), p: n.p})
//		}
//		if v1.T == vt_num {
//			if v1.nval >= v2.nval {
//				return Value{T: vt_num, nval: 1}
//			}
//			return Value{T: vt_num, nval: 0}
//		} else if v1.T == vt_str {
//			if v1.sval >= v2.sval {
//				return Value{T: vt_num, nval: 1}
//			}
//			return Value{T: vt_num, nval: 0}
//		} else if v1.T == vt_fn {
//			panic(&interpreterError{msg: fmt.Sprintf("we cannot compare functions for order: %s, %s", v1, v2), p: n.p})
//			if v1.fnval == v2.fnval {
//				return Value{T: vt_num, nval: 1}
//			}
//			return Value{T: vt_num, nval: 0}
//		}
//		panic(&interpreterError{msg: fmt.Sprintf("we cannot compare for order: %s, %s", v1, v2), p: n.p})
//	case n_if:
//		// Structure of if args:
//		// args[0] = condition
//		// args[1] = then
//		// args[2] *optional* = else
//		cond := n.args[0].exec(ctx)
//		if cond.Trueish() {
//			v := n.args[1].exec(ctx)
//			return v
//		} else if len(n.args) > 2 {
//			v := n.args[2].exec(ctx)
//			return v
//		} else {
//			// TODO: Figure out types and return an appropriate type
//			return Value{}
//		}
//	case n_for:
//		panic("n_for NOT IMPLEMENTED")
//	case n_block:
//		ec := subExecContext(ctx)
//		var ret Value
//		for _, arg := range n.args {
//			ret = arg.exec(ec)
//		}
//		return ret
//	case n_break:
//		panic("n_break NOT IMPLEMENTED")
//		panic(&Break{})
//	case n_return:
//		v := n.args[0].exec(ctx)
//		panic(&Return{v})
//	case n_none:
//		return Value{}
//	}
//	panic("FOO")
//}
//
//func (n *Node) Execute(ctx *ExecContext) (v Value, e error) {
//	defer func() {
//		if err := recover(); err != nil {
//			if ee, ok := err.(*interpreterError); ok {
//				v = Value{}
//				e = ee
//				return
//			} else if ee, ok := err.(*Return); ok {
//				v = ee.v
//				e = nil
//				return
//			} else if _, ok := err.(*Break); ok {
//				v = Value{}
//				// TODO: pass position
//				e = fmt.Errorf("Break outside loop")
//				return
//			}
//			panic(err)
//		}
//	}()
//	return n.exec(ctx), nil
//}
//
//func print(ctx *ExecContext, v Value) Value {
//	fmt.Printf("%s", v)
//	return Value{}
//}
//
//func ExecIO(r io.Reader) {
//	p := NewParser("", r)
//	ctx := NewExecContext()
//	ctx.bind("print", Value{T: vt_builtin, builtin: print})
//	for {
//		fmt.Printf("\n@ ")
//		p.l.p = position{lineoff: 1}
//		n, err := p.Next()
//		if err != nil {
//			fmt.Printf("!!! %s\n", err)
//			continue
//		}
//		v, err := n.Execute(ctx)
//		if err != nil {
//			fmt.Printf("!!! %s\n", err)
//		} else if v.T != vt_none {
//			fmt.Printf("> %s\n", v)
//		}
//	}
//}
//
//func ExecF(fname string) {
//	f, err := os.Open(fname)
//	if err != nil {
//		fmt.Printf("Failed to open %s: %s\n", fname, err)
//		return
//	}
//	defer f.Close()
//	p := NewParser("", f)
//	ctx := NewExecContext()
//	ctx.bind("print", Value{T: vt_builtin, builtin: print})
//	for {
//		n, err := p.Next()
//		if err != nil {
//			fmt.Printf("ERROR (%s) %s\n", fname, err)
//			return
//		}
//		if n == nil {
//			fmt.Printf("Reached EOF\n")
//			return
//		}
//		v, err := n.Execute(ctx)
//		if err != nil {
//			fmt.Printf("ERROR (%s) %s\n", fname, err)
//		} else if v.T != vt_none {
//			fmt.Printf("%s\n", v)
//		}
//	}
//}
//
