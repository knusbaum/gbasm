package main

import (
	"fmt"
	"io"
)

type nodetype int

const (
	n_none nodetype = iota
	n_number
	n_str
	n_symbol
	n_funcall
	n_var
	n_typename
	n_index   // Indexing Operation
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
	n_eq  // Assignment equal
	n_deq // Comparison equal
	n_lt  // Less than
	n_gt  // Greater than
	n_le  // Less or equal
	n_ge  // Greater or equal
	n_if
	n_for
	n_block
	n_break
	n_return
	n_import
	n_address
	n_deref
)

func (t nodetype) String() string {
	switch t {
	case n_none:
		return "n_none"
	case n_number:
		return "n_number"
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
	case n_eq:
		return "n_eq"
	case n_deq:
		return "n_deq"
	case n_lt:
		return "n_lt"
	case n_gt:
		return "n_gt"
	case n_le:
		return "n_le"
	case n_ge:
		return "n_ge"
	case n_if:
		return "n_if"
	case n_for:
		return "n_for"
	case n_block:
		return "n_block"
	case n_break:
		return "n_break"
	case n_return:
		return "n_return"
	case n_import:
		return "n_import"
	case n_address:
		return "n_address"
	case n_deref:
		return "n_deref"
	}
	return "UNKNOWN"
}

type Parser struct {
	l     *lexer
	curr  *token
	fname string
}

type Node struct {
	t    nodetype
	ival uint64
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
		ret = append(ret, &Node{t: n_stfield, sval: v.sval, p: v.p, args: []*Node{v2}})
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
		return &Node{t: n_number, ival: uint64(c.nval), p: c.p}
	} else if c.t == tok_str {
		p.advance()
		return &Node{t: n_str, sval: c.sval, p: c.p}
	} else if c.t == tok_ident {
		p.advance()
		return &Node{t: n_symbol, sval: c.sval, p: c.p}
	} else if c.t == tok_semicolon {
		p.advance()
		return &Node{p: c.p}
	}
	p.advance()
	panic(&interpreterError{fmt.Sprintf("Expected number, string, identifier or semicolon, but found: %s\n", c.t), c.p})
	//panic(fmt.Sprintf("Expected number, string, identifier or semicolon, but found: %s\n", c.t))
}

func (p *Parser) parseTypeName() *Node {
	//c := p.current()
	indirection := uint64(0)
	for c := p.current(); c.t == tok_star; c = p.current() {
		indirection++
		p.advance()
	}
	c := p.current()
	if c.t != tok_ident {
		panic(&interpreterError{fmt.Sprintf("Expected a type name, but found: %s\n", c.t), c.p})
	}
	typename := c.sval
	p.advance()
	//fmt.Printf("NEXT TOKEN: %v\n", p.current())
	if p.current().t == tok_lsquare {
		p.advance()
		if p.current().t != tok_number {
			panic(&interpreterError{fmt.Sprintf("Expected a number, but found: %s\n", c.t), c.p})
		}
		arrsize := uint64(p.current().nval)
		p.advance()
		p.expect(tok_rsquare)
		return &Node{
			t:    n_typename,
			sval: typename,
			ival: indirection,
			args: []*Node{&Node{t: n_index, ival: arrsize}},
		}
	}
	return &Node{t: n_typename, sval: c.sval, ival: indirection}
}

func (p *Parser) parseValue() *Node {
	v := p.parseTok()
	if v.t == n_symbol {
		if p.current().t == tok_lparen {
			// we have a function call
			p.advance()
			args := p.parseArgs()
			p.expect(tok_rparen)
			return &Node{t: n_funcall, sval: v.sval, p: v.p, args: args}
		} else if p.current().t == tok_lsquare {
			p.advance()
			v2 := p.parseExpression()
			p.expect(tok_rsquare)
			return &Node{t: n_index, sval: v.sval, p: v.p, args: []*Node{v2}}
		} else if p.current().t == tok_lcurly {
			p.advance()
			v2 := p.parseStructLiteral()
			p.expect(tok_rcurly)
			return &Node{t: n_stlit, sval: v.sval, p: v.p, args: v2}
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
	//fmt.Printf("Parsing params...\n")
	//defer fmt.Printf("Parsing params done.\n")
	var ret []*Node
	for p.current().t != tok_rparen {
		v := p.parseTok()
		if v.t != n_symbol {
			panic(&interpreterError{fmt.Sprintf("Expected variable name, but found: %v\n", v), v.p})
		}
		v2 := p.parseTypeName()
		if v2.t != n_typename {
			panic(&interpreterError{fmt.Sprintf("Expected type name, but found: %v\n", v), v.p})
		}
		ret = append(ret, &Node{t: n_arg, sval: v.sval, p: v.p, args: []*Node{v2}})
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
	rettype := &Node{t: n_typename, sval: "void"}
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
	return &Node{t: n_fn, ival: uint64(len(params)), sval: fname.sval, p: fnpos, args: args}
}

func (p *Parser) parseParens() *Node {
	c := p.current()
	//fmt.Printf("CURRENT TOKEN IS %#v\n", c)
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
	} else if c.t == tok_return {
		p.advance()
		val := p.parseExpression()
		return &Node{t: n_return, p: c.p, args: []*Node{val}}
	} else if c.t == tok_fn {
		return p.parseFn()
	}
	//fmt.Printf("PARSING VALUE\n")
	pv := p.parseValue()
	//fmt.Printf("PARSED VALUE: %#v\n", pv)
	return pv
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
	//fmt.Printf("Parsing Subexpr\n")
	v := p.parseSubexpr()
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

func (p *Parser) parseVarDecl() *Node {
	if p.current().t != tok_ident {
		panic(&interpreterError{fmt.Sprintf("Expected an identifier in var declaration, but found: %s\n", p.current().t), p.current().p})
	}
	name := p.current().sval
	p.advance()
	v2 := p.parseTypeName()
	return &Node{t: n_var, sval: name, args: []*Node{v2}}
}

func (p *Parser) parseExpression() (r *Node) {
	//fmt.Printf("[Start parseExpression] %#v\n", p.current())
	//defer func() { fmt.Printf("[Finish parseExpression]: %#v\n", r) }()
	if p.current().t == tok_semicolon {
		p.advance()
		return &Node{p: p.current().p}
	} else if p.current().t == tok_ident && p.current().sval == "var" {
		p.advance()
		return p.parseVarDecl()
	} else if p.current().t == tok_amp {
		p.advance()
		if p.current().t != tok_ident {
			panic(&interpreterError{fmt.Sprintf("Expected an identifier in address operation, but found: %s\n", p.current().t), p.current().p})
		}
		name := p.current().sval
		p.advance()
		return &Node{t: n_address, sval: name, p: p.current().p}
	} else if p.current().t == tok_star {
		p.advance()
		n := p.parseExpression()
		return &Node{t: n_deref, args: []*Node{n}, p: p.current().p}
		// if p.current().t != tok_ident {
		// 	panic(&interpreterError{fmt.Sprintf("Expected an identifier in deref operation, but found: %s\n", p.current().t), p.current().p})
		// }
		// name := p.current().sval
		// p.advance()
		// return &Node{t: n_deref, sval: name, p: p.current().p}
	} else if p.current().t == tok_none {
		p.advance()
		panic(eofError(0))
	}

	v := p.parseAddSub()
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
		case tok_deq:
			p.advance()
			v2 := p.parseAddSub()
			v = &Node{t: n_deq, p: c.p, args: []*Node{v, v2}}
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
			//case tok_semicolon:
			//	p.advance()
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
		ret = append(ret, &Node{t: n_stfield, sval: v.sval, p: fieldpos, args: []*Node{v2}})
	}
	p.expect(tok_rcurly)
	return &Node{t: n_struct, sval: name.sval, p: structpos, args: ret}
}

func (p *Parser) parseImport() *Node {
	importpos := p.current().p
	p.expect(tok_import)
	path := p.parseTok()
	if path.t != n_str {
		panic(&interpreterError{fmt.Sprintf("Expected a string path, but found %#v\n", path), path.p})
	}
	return &Node{t: n_import, sval: path.sval, p: importpos}
}

func (p *Parser) parseTopLevel() *Node {
	if p.current().t == tok_struct {
		return p.parseStruct()
	} else if p.current().t == tok_import {
		return p.parseImport()
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
