package main

import (
	"fmt"
	"io"
)

type nodetype int

const (
	n_none nodetype = iota
	n_number
	n_byte
	n_str
	n_symbol
	n_funcall
	n_var
	n_typename
	n_index   // Indexing Operation
	n_slice   // Slicing Operation
	n_struct  // Structure Type Declaration
	n_stlit   // Structure Literal
	n_stfield // Structure Field
	n_fn      // Function
	n_arg     // Function Argument
	n_dot
	n_mul
	n_div
	n_add
	n_sub
	n_neg // negate (-1)
	n_eq  // Assignment (=)
	n_deq // Comparison equal (==)
	n_neq // Not equal (!=)
	n_lt  // Less than
	n_gt  // Greater than
	n_le  // Less or equal
	n_ge  // Greater or equal
	n_booland
	n_boolor
	n_if
	n_for
	n_block
	n_break
	n_continue
	n_return
	n_import
	n_address
	n_deref
	n_dispose
	n_typedecl
)

func (t nodetype) String() string {
	switch t {
	case n_none:
		return "n_none"
	case n_number:
		return "n_number"
	case n_byte:
		return "n_byte"
	case n_str:
		return "n_str"
	case n_symbol:
		return "n_symbol"
	case n_funcall:
		return "n_funcall"
	case n_var:
		return "n_var"
	case n_typename:
		return "n_typename"
	case n_index:
		return "n_index"
	case n_slice:
		return "n_slice"
	case n_struct:
		return "n_struct"
	case n_stlit:
		return "n_stlit"
	case n_stfield:
		return "n_stfield"
	case n_fn:
		return "n_fn"
	case n_arg:
		return "n_arg"
	case n_dot:
		return "n_dot"
	case n_mul:
		return "n_mul"
	case n_div:
		return "n_div"
	case n_add:
		return "n_add"
	case n_sub:
		return "n_sub"
	case n_neg:
		return "n_neg"
	case n_eq:
		return "n_eq"
	case n_deq:
		return "n_deq"
	case n_neq:
		return "n_deq"
	case n_lt:
		return "n_lt"
	case n_gt:
		return "n_gt"
	case n_le:
		return "n_le"
	case n_ge:
		return "n_ge"
	case n_booland:
		return "n_booland"
	case n_boolor:
		return "n_boolor"
	case n_if:
		return "n_if"
	case n_for:
		return "n_for"
	case n_block:
		return "n_block"
	case n_break:
		return "n_break"
	case n_continue:
		return "n_continue"
	case n_return:
		return "n_return"
	case n_import:
		return "n_import"
	case n_address:
		return "n_address"
	case n_deref:
		return "n_deref"
	case n_dispose:
		return "n_dispose"
	case n_typedecl:
		return "n_typedecl"
	}
	return "UNKNOWN"
}

type Parser struct {
	l     *lexer
	curr  *token
	fname string
}

type Node struct {
	t         nodetype
	ival      uint64
	mutmask   uint64 // for n_typename: bit N = mut at pointer/slice level N
	ownedmask uint64 // for n_typename: bit N = owned at pointer/slice level N
	//fval float64 // we'll add floats later.
	sval string
	args []*Node
	p    position
}

func NewParser(fname string, r io.Reader) *Parser {
	return &Parser{l: NewLexer(fname, r), fname: fname}
}

func (p *Parser) ParseFunctype() (FuncDecl, error) {
	p.expect(tok_fn)
	p.expect(tok_lparen)
	var args []Binding
	for p.current().t != tok_rparen {
		t := mkTypename(p.parseTypeName())
		b := Binding{Type: t}
		args = append(args, b)
		if p.current().t == tok_comma {
			p.advance()
		}
	}
	p.expect(tok_rparen)
	rettype := voidASTType()
	if p.current().t != tok_lcurly {
		rettype = mkTypename(p.parseTypeName())
	}
	return FuncDecl{
		Args:   args,
		Return: rettype,
	}, nil
}

func (p *Parser) current() token {
	if p.curr == nil {
		next, err := p.l.Next()
		if err != nil {
			panic(err)
		}
		p.curr = &next
	}
	return *p.curr
}

func (p *Parser) advance() {
	p.curr = nil
}

// expect ensures that the current token matches the specified toktype and avances to the next one.
func (p *Parser) expect(t toktype) {
	if p.current().t != t {
		defer p.advance()
		panic(&interpreterError{fmt.Sprintf("Expected a token of type %s but found %s", t, p.current().t), p.current().p})
	}
	p.advance()
}

func (p *Parser) parseArgs() []*Node {
	var ret []*Node
	for p.current().t != tok_rparen {
		v := p.parseExpression()
		ret = append(ret, v)
		if p.current().t != tok_comma {
			return ret
		}
		p.advance()
	}
	return ret
}

func (p *Parser) parseStructLiteral() []*Node {
	var ret []*Node
	for p.current().t != tok_rcurly {
		v := p.parseValue()
		if v.t != n_symbol {
			panic(&interpreterError{fmt.Sprintf("Expected field name, but found: %v\n", v), v.p})
		}
		p.expect(tok_colon)
		v2 := p.parseExpression()
		ret = append(ret, &Node{t: n_stfield, p: v.p, sval: v.sval, args: []*Node{v2}})
		if p.current().t != tok_comma {
			break
		}
		p.advance()
	}
	return ret
}

func (p *Parser) parseTok() *Node {
	c := p.current()
	if c.t == tok_number {
		p.advance()
		return &Node{t: n_number, p: c.p, ival: uint64(c.nval)}
	} else if c.t == tok_str {
		p.advance()
		return &Node{t: n_str, p: c.p, sval: c.sval}
	} else if c.t == tok_byte {
		p.advance()
		return &Node{t: n_byte, p: c.p, ival: uint64(c.nval)}
	} else if c.t == tok_ident {
		p.advance()
		return &Node{t: n_symbol, p: c.p, sval: c.sval}
	} else if c.t == tok_semicolon {
		p.advance()
		return &Node{p: c.p}
	}
	p.advance()
	panic(&interpreterError{fmt.Sprintf("Expected number, string, identifier or semicolon, but found: %s\n", c.t), c.p})
	//panic(fmt.Sprintf("Expected number, string, identifier or semicolon, but found: %s (%v)\n", c.t, c.p))
}

func (p *Parser) parseTypeName() *Node {
	var indirection uint64
	var mutmask uint64
	var ownedmask uint64

	// Bit numbering convention (bit 0 = outermost, bit N = after N dereferences):
	//   owned/mut before '*' at level N  → applies to the pointer itself  → bit N
	//   owned/mut after  '*' (before next '*' or base type) → applies to what the '*' points to → bit N+1
	//   owned/mut before a base type (no '*') → bit N (the value itself)
	//   mut before a slice base type (T[]) → bit N+1 (elements, like *mut T for pointers)
	//
	// 'mut' before a '*' is illegal — 'mut' on a pointer means write-through,
	// which is expressed as '*mut T'. Binding mutability is expressed with var/const.
	//
	// Examples:
	//   owned i64       → OwnedMask bit 0  (the i64 itself is owned)
	//   owned *T        → OwnedMask bit 0  (the pointer is owned)
	//   *mut T          → MutMask   bit 1  (write-through to T, symmetric with *owned T)
	//   *owned T        → OwnedMask bit 1  (T is owned, accessed through the pointer)
	//   owned *owned T  → OwnedMask bits 0+1 (two independent obligations)
	//   mut byte[]      → MutMask   bit 1  (elements writable, analogous to *mut byte)
	for {
		// Read qualifiers at the current level (before the next '*' or base type).
		for p.current().t == tok_owned || p.current().t == tok_mut {
			if p.current().t == tok_owned {
				ownedmask |= 1 << indirection
				p.advance()
			} else {
				// 'mut' before a '*' is illegal.
				if p.current().t == tok_mut {
					mutPos := p.current().p
					p.advance()
					if p.current().t == tok_star {
						panic(&interpreterError{"'mut' before '*' is not valid; use '*mut T' for write-through", mutPos})
					}
					// 'mut' before base type (non-pointer): recorded at current level.
					// For slices (T[]) this will be bumped to +1 below.
					mutmask |= 1 << indirection
				}
			}
		}
		if p.current().t != tok_star {
			break
		}
		p.advance() // consume '*'
		indirection++
		// Qualifiers after the '*' apply to what it points to (bit = indirection, i.e. N+1).
		// Both 'mut' and 'owned' are symmetric here.
		for p.current().t == tok_owned || p.current().t == tok_mut {
			if p.current().t == tok_owned {
				ownedmask |= 1 << indirection
			} else {
				mutmask |= 1 << indirection
			}
			p.advance()
		}
	}

	c := p.current()
	if c.t != tok_ident {
		panic(&interpreterError{fmt.Sprintf("Expected a type name, but found: %s\n", c.t), c.p})
	}
	typename := c.sval
	p.advance()

	if p.current().t == tok_lsquare {
		p.advance()
		if p.current().t == tok_number {
			arrsize := uint64(p.current().nval)
			p.advance()
			p.expect(tok_rsquare)
			if mutmask&(1<<indirection) != 0 || ownedmask&(1<<indirection) != 0 {
				panic(&interpreterError{"'mut'/'owned' is only valid on slice types, not fixed-size arrays", c.p})
			}
			return &Node{
				t:         n_typename,
				p:         c.p,
				sval:      typename,
				ival:      indirection,
				mutmask:   mutmask,
				ownedmask: ownedmask,
				args:      []*Node{{t: n_index, p: c.p, ival: arrsize}},
			}
		} else if p.current().t == tok_rsquare {
			p.advance()
			// mut/owned before a slice base type apply to the element level (bit+1),
			// analogous to *mut T where mut is after the '*'.
			if mutmask&(1<<indirection) != 0 {
				mutmask &^= 1 << indirection
				mutmask |= 1 << (indirection + 1)
			}
			if ownedmask&(1<<indirection) != 0 {
				ownedmask &^= 1 << indirection
				ownedmask |= 1 << (indirection + 1)
			}
			return &Node{
				t:         n_typename,
				p:         c.p,
				sval:      typename,
				ival:      indirection,
				mutmask:   mutmask,
				ownedmask: ownedmask,
				args:      []*Node{{t: n_slice, p: c.p}},
			}
		}
		panic(&interpreterError{fmt.Sprintf("Expected a number or ']', but found: %s\n", p.current().t), c.p})
	}

	// 'mut' on a plain non-pointer, non-slice type is meaningless.
	// Only fires when indirection==0 (no pointer stars): any mut bits must have
	// come from a leading qualifier directly before the base type.
	if indirection == 0 && mutmask != 0 {
		panic(&interpreterError{fmt.Sprintf("'mut' on non-reference type '%s' has no effect; 'mut' is only valid on pointer or slice types", typename), c.p})
	}
	return &Node{t: n_typename, p: c.p, sval: typename, ival: indirection, mutmask: mutmask, ownedmask: ownedmask}
}

func (p *Parser) parseValue() *Node {
	v := p.parseTok()
	if v.t == n_symbol {
		if p.current().t == tok_lparen {
			// we have a function call
			p.advance()
			args := p.parseArgs()
			p.expect(tok_rparen)
			return &Node{t: n_funcall, p: v.p, sval: v.sval, args: args}
		} else if p.current().t == tok_lsquare {
			p.advance()
			if p.current().t == tok_colon {
				// [:
				p.advance()
				if p.current().t == tok_rsquare {
					// [:]
					p.advance()
					return &Node{t: n_slice, p: v.p, sval: v.sval, args: []*Node{nil, nil}}
				}
				// [: X]
				v3 := p.parseExpression()
				p.expect(tok_rsquare)
				return &Node{t: n_slice, p: v.p, sval: v.sval, args: []*Node{nil, v3}}
			}
			v2 := p.parseExpression()

			if p.current().t == tok_colon {
				// [ X :
				p.advance()
				if p.current().t == tok_rsquare {
					// [X:]
					p.advance()
					return &Node{t: n_slice, p: v.p, sval: v.sval, args: []*Node{v2, nil}}
				}
				v3 := p.parseExpression()
				p.expect(tok_rsquare)
				return &Node{t: n_slice, p: v.p, sval: v.sval, args: []*Node{v2, v3}}
			}

			p.expect(tok_rsquare)
			return &Node{t: n_index, p: v.p, sval: v.sval, args: []*Node{v2}}
		} else if p.current().t == tok_lcurly {
			p.advance()
			v2 := p.parseStructLiteral()
			p.expect(tok_rcurly)
			return &Node{t: n_stlit, p: v.p, sval: v.sval, args: v2}
		}
	}
	return v
}

func (p *Parser) parseIf() *Node {
	var args []*Node
	ifpos := p.current().p
	p.expect(tok_if)
	p.expect(tok_lparen)
	condition := p.parseExpression()
	args = append(args, condition)
	p.expect(tok_rparen)
	then := p.parseExpression()
	args = append(args, then)
	if p.current().t == tok_else {
		p.advance()
		els := p.parseExpression()
		args = append(args, els)
	}
	return &Node{t: n_if, p: ifpos, args: args}
}

func (p *Parser) parseFor() *Node {
	var args []*Node
	forpos := p.current().p
	p.expect(tok_for)
	p.expect(tok_lparen)
	if p.current().t != tok_semicolon {
		init := p.parseExpression()
		args = append(args, init)
	} else {
		args = append(args, &Node{p: p.current().p})
	}
	p.expect(tok_semicolon)
	if p.current().t != tok_semicolon {
		condition := p.parseExpression()
		args = append(args, condition)
	} else {
		args = append(args, &Node{p: p.current().p})
	}
	p.expect(tok_semicolon)
	if p.current().t != tok_rparen {
		iter := p.parseExpression()
		args = append(args, iter)
	} else {
		args = append(args, &Node{p: p.current().p})
	}
	p.expect(tok_rparen)
	body := p.parseExpression()
	args = append(args, body)
	return &Node{t: n_for, p: forpos, args: args}
}

func (p *Parser) parseParams() []*Node {
	var ret []*Node
	for p.current().t != tok_rparen {
		// Optional 'var' makes the parameter rebindable inside the function body.
		// Parameters are const by default; ival=0 means const, ival=1 means var.
		isVar := uint64(0)
		if p.current().t == tok_var {
			isVar = 1
			p.advance()
		}
		v := p.parseTok()
		if v.t != n_symbol {
			panic(&interpreterError{fmt.Sprintf("Expected variable name, but found: %v\n", v), v.p})
		}
		v2 := p.parseTypeName()
		if v2.t != n_typename {
			panic(&interpreterError{fmt.Sprintf("Expected type name, but found: %v\n", v), v.p})
		}
		ret = append(ret, &Node{t: n_arg, p: v.p, sval: v.sval, ival: isVar, args: []*Node{v2}})
		if p.current().t != tok_comma {
			break
		}
		p.advance()
	}
	return ret
}

func (p *Parser) parseFn() *Node {
	fnpos := p.current().p
	p.expect(tok_fn)
	fname := p.parseTok()
	if fname.t != n_symbol {
		panic(&interpreterError{fmt.Sprintf("Expected function name, but found: %v\n", fname), fname.p})
	}
	p.expect(tok_lparen)
	params := p.parseParams()
	p.expect(tok_rparen)
	//fmt.Printf("############################################ Parsing RetType\n")
	rettype := &Node{t: n_typename, p: p.current().p, sval: "void"}
	if p.current().t != tok_lcurly {
		// This function has a listed return type.
		rettype = p.parseTypeName()
		//rettype = &Node{t: n_symbol, sval: p.current().sval}
		//p.advance()
	}

	//fmt.Printf("GOT %#v\n", rettype)
	// 	if rettype.t != n_symbol {
	// 		panic(&interpreterError{fmt.Sprintf("Expected type name, but found: %v\n", rettype), rettype.p})
	// 	}
	args := append(params, rettype)
	body := p.parseExpression()
	args = append(args, body)
	return &Node{t: n_fn, p: fnpos, ival: uint64(len(params)), sval: fname.sval, args: args}
}

func (p *Parser) parseParens() *Node {
	c := p.current()
	if c.t == tok_lparen {
		p.advance()
		v := p.parseExpression()
		p.expect(tok_rparen)
		return v
	} else if c.t == tok_lcurly {
		p.advance()
		var block []*Node
		for p.current().t != tok_rcurly {
			v := p.parseExpression()
			block = append(block, v)
		}
		p.expect(tok_rcurly)
		return &Node{t: n_block, p: c.p, args: block}
	} else if c.t == tok_if {
		return p.parseIf()
	} else if c.t == tok_for {
		return p.parseFor()
	} else if c.t == tok_break {
		p.advance()
		return &Node{t: n_break, p: c.p}
	} else if c.t == tok_continue {
		p.advance()
		return &Node{t: n_continue, p: c.p}
	} else if c.t == tok_return {
		p.advance()
		if p.current().t == tok_rcurly {
			return &Node{t: n_return, p: c.p, args: []*Node{}}
		}
		val := p.parseExpression()
		return &Node{t: n_return, p: c.p, args: []*Node{val}}
	} else if c.t == tok_fn {
		return p.parseFn()
	} else if c.t == tok_dispose {
		p.advance()
		p.expect(tok_lparen)
		if p.current().t != tok_ident {
			panic(&interpreterError{fmt.Sprintf("dispose requires a variable name, but found: %s\n", p.current().t), p.current().p})
		}
		name := p.current().sval
		namepos := p.current().p
		p.advance()
		p.expect(tok_rparen)
		return &Node{t: n_dispose, p: namepos, sval: name}
	}
	pv := p.parseValue()
	//fmt.Printf("PARSED VALUE: %#v\n", pv)
	return pv
}

// parseUnary handles prefix unary operators: *, -, &.
// It sits above parseSubexpr (which handles . and []) so that
// *x.y parses as *(x.y), and below parseAddSub so that *p + 1 = (*p) + 1.
func (p *Parser) parseUnary() *Node {
	c := p.current()
	if c.t == tok_star {
		p.advance()
		operand := p.parseUnary() // right-associative: **p = *(*p)
		return &Node{t: n_deref, p: c.p, args: []*Node{operand}}
	} else if c.t == tok_minus {
		p.advance()
		operand := p.parseUnary()
		return &Node{t: n_neg, p: c.p, args: []*Node{operand}}
	} else if c.t == tok_amp {
		p.advance()
		if p.current().t != tok_ident {
			panic(&interpreterError{fmt.Sprintf("Expected an identifier in address operation, but found: %s\n", p.current().t), p.current().p})
		}
		name := p.current().sval
		p.advance()
		return &Node{t: n_address, p: c.p, sval: name}
	}
	return p.parseSubexpr()
}

func (p *Parser) parseSubexpr() *Node {
	//fmt.Printf("PARSING PARENS\n")
	v := p.parseParens()
	//fmt.Printf("GOT PARSE PARENS: %#v\n", v)
	for {
		c := p.current()
		switch c.t {
		case tok_dot:
			for p.current().t == tok_dot {
				p.advance()
				v2 := p.parseValue()
				v = &Node{t: n_dot, p: c.p, args: []*Node{v, v2}}
			}
		case tok_star:
			p.advance()
			v2 := p.parseParens()
			v = &Node{t: n_mul, p: c.p, args: []*Node{v, v2}}
			continue
		case tok_fslash:
			p.advance()
			v2 := p.parseParens()
			v = &Node{t: n_div, p: c.p, args: []*Node{v, v2}}
			continue
		}
		break
	}
	return v
}

func (p *Parser) parseAddSub() *Node {
	v := p.parseUnary()
	//fmt.Printf("GOT SUBEXPR: %#v\n", v)
	for {
		c := p.current()
		switch c.t {
		case tok_plus:
			p.advance()
			v2 := p.parseSubexpr()
			v = &Node{t: n_add, p: c.p, args: []*Node{v, v2}}
			continue
		case tok_minus:
			p.advance()
			v2 := p.parseSubexpr()
			v = &Node{t: n_sub, p: c.p, args: []*Node{v, v2}}
			continue
		}
		break
	}
	return v
}

// parseBindingDecl parses a var or const declaration after the keyword has been consumed.
// isConst is encoded in the node via ival: 0 = var, 1 = const.
func (p *Parser) parseBindingDecl(isConst bool) *Node {
	if p.current().t != tok_ident {
		panic(&interpreterError{fmt.Sprintf("Expected an identifier in binding declaration, but found: %s\n", p.current().t), p.current().p})
	}
	pos := p.current().p
	name := p.current().sval
	p.advance()
	v2 := p.parseTypeName()
	constVal := uint64(0)
	if isConst {
		constVal = 1
	}
	return &Node{t: n_var, p: pos, sval: name, ival: constVal, args: []*Node{v2}}
}

func (p *Parser) parseBoolOp() *Node {
	v := p.parseCompare()
	for {
		c := p.current()
		switch c.t {
		case tok_booland:
			p.advance()
			v2 := p.parseCompare()
			v = &Node{t: n_booland, p: c.p, args: []*Node{v, v2}}
			continue
		case tok_boolor:
			p.advance()
			v2 := p.parseCompare()
			v = &Node{t: n_boolor, p: c.p, args: []*Node{v, v2}}
			continue
		}
		break
	}
	return v
}

func (p *Parser) parseCompare() *Node {
	v := p.parseAddSub()
	for {
		c := p.current()
		switch c.t {
		case tok_deq:
			p.advance()
			v2 := p.parseAddSub()
			v = &Node{t: n_deq, p: c.p, args: []*Node{v, v2}}
			continue
		case tok_neq:
			p.advance()
			v2 := p.parseAddSub()
			v = &Node{t: n_neq, p: c.p, args: []*Node{v, v2}}
			continue
		case tok_lt:
			p.advance()
			v2 := p.parseAddSub()
			v = &Node{t: n_lt, p: c.p, args: []*Node{v, v2}}
			continue
		case tok_gt:
			p.advance()
			v2 := p.parseAddSub()
			v = &Node{t: n_gt, p: c.p, args: []*Node{v, v2}}
			continue
		case tok_le:
			p.advance()
			v2 := p.parseAddSub()
			v = &Node{t: n_le, p: c.p, args: []*Node{v, v2}}
			continue
		case tok_ge:
			p.advance()
			v2 := p.parseAddSub()
			v = &Node{t: n_ge, p: c.p, args: []*Node{v, v2}}
			continue
		}
		break
	}
	return v
}

func (p *Parser) parseExpression() (r *Node) {
	//fmt.Printf("[Start parseExpression] %#v\n", p.current())
	//defer func() { fmt.Printf("[Finish parseExpression]: %#v\n", r) }()
	if p.current().t == tok_semicolon {
		p.advance()
		return &Node{p: p.current().p}
	} else if p.current().t == tok_var {
		p.advance()
		return p.parseBindingDecl(false)
	} else if p.current().t == tok_const {
		p.advance()
		return p.parseBindingDecl(true)
	} else if p.current().t == tok_none {
		p.advance()
		panic(eofError(0))
	}

	v := p.parseBoolOp()
	for {
		c := p.current()
		switch c.t {
		case tok_eq:
			// 			if v.t != n_symbol {
			// 				panic(&interpreterError{fmt.Sprintf("Cannot assign to non-variable %v\n", v), v.p})
			// 			}
			p.advance()
			v2 := p.parseExpression()
			v = &Node{t: n_eq, p: c.p, args: []*Node{v, v2}}
			continue
		}
		break
	}
	return v
}

func (p *Parser) parseStruct() *Node {
	structpos := p.current().p
	p.expect(tok_struct)
	name := p.parseTok()
	if name.t != n_symbol {
		panic(&interpreterError{fmt.Sprintf("Expected type name, but found %#v\n", name), name.p})
	}
	p.expect(tok_lcurly)

	var ret []*Node
	for p.current().t != tok_rcurly {
		fieldpos := p.current().p
		v := p.parseTok()
		if v.t != n_symbol {
			panic(&interpreterError{fmt.Sprintf("Expected field name, but found %#v\n", v), v.p})
		}
		v2 := p.parseTypeName()
		if v2.t != n_typename {
			panic(&interpreterError{fmt.Sprintf("Expected type name, but found %#v\n", v2), v2.p})
		}
		ret = append(ret, &Node{t: n_stfield, p: fieldpos, sval: v.sval, args: []*Node{v2}})
	}
	p.expect(tok_rcurly)
	return &Node{t: n_struct, p: structpos, sval: name.sval, args: ret}
}

func (p *Parser) parseImport() *Node {
	importpos := p.current().p
	p.expect(tok_import)
	path := p.parseTok()
	if path.t != n_str {
		panic(&interpreterError{fmt.Sprintf("Expected a string path, but found %#v\n", path), path.p})
	}
	return &Node{t: n_import, p: importpos, sval: path.sval}
}

func (p *Parser) parseTypeDecl() *Node {
	pos := p.current().p
	p.expect(tok_type)
	name := p.parseTok()
	if name.t != n_symbol {
		panic(&interpreterError{fmt.Sprintf("Expected type name, but found: %v\n", name), name.p})
	}
	base := p.parseTypeName()
	return &Node{t: n_typedecl, p: pos, sval: name.sval, args: []*Node{base}}
}

func (p *Parser) parseTopLevel() *Node {
	if p.current().t == tok_struct {
		return p.parseStruct()
	} else if p.current().t == tok_import {
		return p.parseImport()
	} else if p.current().t == tok_type {
		return p.parseTypeDecl()
	}
	return p.parseExpression()
}

type eofError int

func (p *Parser) Next() (n *Node, e error) {
	defer func() {
		if err := recover(); err != nil {
			if le, ok := err.(*interpreterError); ok {
				n = nil
				e = le
				return
			} else if _, ok := err.(eofError); ok {
				n = nil
				e = nil
				return
			}
			panic(e)
		}
	}()
	return p.parseTopLevel(), nil
}
