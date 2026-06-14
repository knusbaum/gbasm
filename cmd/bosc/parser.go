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
	n_not // logical not (!x)
	n_eq  // Assignment (=)
	n_deq // Comparison equal (==)
	n_neq // Not equal (!=)
	n_lt  // Less than
	n_gt  // Greater than
	n_le  // Less or equal
	n_ge  // Greater or equal
	n_booland
	n_boolor
	n_bitand
	n_bitor
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
	n_ownedpromo
	n_arrlit
	n_nonnull
	n_typedecl_with_methods // type TypeName Base { method... }
	n_interface_decl        // interface Name { sig... }
	n_interface_method      // method signature (no body)
	n_multibind             // multi-value destructuring bind: var a T1, const b T2 = expr
	n_valuesdecl            // type TypeName values [(projections)] { cases } [{ methods }]
	n_valuescase            // single case in a values decl: NAME[: e1, e2, ...]
	n_variadic_forward      // trailing `slice...` forwarding at a call site
	n_typeassert            // x.(T) — runtime type assertion
	n_typeswitch            // switch (v x.(type)) { case T {...} ... }
	n_typecase              // single case in a type switch
	n_from                  // `from(name, ...)` borrow clause on an interface return slot
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
	case n_not:
		return "n_not"
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
	case n_bitand:
		return "n_bitand"
	case n_bitor:
		return "n_bitor"
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
	case n_ownedpromo:
		return "n_ownedpromo"
	case n_arrlit:
		return "n_arrlit"
	case n_nonnull:
		return "n_nonnull"
	case n_typedecl_with_methods:
		return "n_typedecl_with_methods"
	case n_interface_decl:
		return "n_interface_decl"
	case n_interface_method:
		return "n_interface_method"
	case n_multibind:
		return "n_multibind"
	case n_valuesdecl:
		return "n_valuesdecl"
	case n_valuescase:
		return "n_valuescase"
	case n_from:
		return "n_from"
	}
	return "UNKNOWN"
}

type Parser struct {
	l      *lexer
	curr   *token
	pushed *token // one extra look-ahead slot for pushback
	fname  string
	// inTypeSwitchHead suppresses `.(type)` being parsed as a type
	// assertion while parsing the switched value in a type-switch head.
	inTypeSwitchHead bool
}

type Node struct {
	t         nodetype
	ival      uint64
	mutmask   uint64 // for n_typename: bit N = mut at pointer/slice level N
	ownedmask uint64 // for n_typename: bit N = owned at pointer/slice level N
	nilmask   uint64 // for n_typename: bit N = nullable pointer at pointer level N
	//fval float64 // we'll add floats later.
	sval  string
	args  []*Node
	isPub bool
	// typeIdent is the raw type-identifier node for n_stlit when the
	// literal was written with a leading type (`Name{...}` or
	// `pkg.Type{...}`). ToAST routes it through the shared selector
	// resolver to determine the literal's type. Nil for anonymous
	// `{ field: val, ... }` literals whose type is inferred from
	// context.
	typeIdent *Node
	// prebuilt carries an already-assembled ASTType for the
	// "<prebuilt>" n_typename sentinel produced by a parenthesized type
	// group (`*(byte[])`). mkTypename returns it verbatim. Nil otherwise.
	prebuilt *ASTType
	p        position
}

func NewParser(fname string, r io.Reader) *Parser {
	return &Parser{l: NewLexer(fname, r), fname: fname}
}

func NewParserAt(fname string, r io.Reader, startLine uint) *Parser {
	return &Parser{l: NewLexerAt(fname, r, startLine), fname: fname}
}

func (p *Parser) ParseFunctype() (FuncDecl, error) {
	p.expect(tok_fn)
	p.expect(tok_lparen)
	var args []Binding
	isVariadic := false
	for p.current().t != tok_rparen {
		// A leading `...` marks this (necessarily last) parameter as
		// variadic. The rendered signature emits `...<element>`; here we
		// parse the element type and re-wrap it as the slice the body sees,
		// setting Variadic so call sites accept zero-or-more trailing args.
		variadic := false
		if p.current().t == tok_ellipsis {
			p.advance()
			variadic = true
		}
		t := mkTypename(p.parseTypeName())
		if variadic {
			elem := t
			t = ASTType{Element: &elem}
			isVariadic = true
		}
		b := Binding{Type: t, Variadic: variadic}
		args = append(args, b)
		if p.current().t == tok_comma {
			p.advance()
		}
	}
	p.expect(tok_rparen)
	rettype := voidASTType()
	if p.current().t != tok_lcurly && p.current().t != tok_none {
		rettype = mkTypename(p.parseTypeName())
	}
	return FuncDecl{
		Args:     args,
		Return:   rettype,
		Variadic: isVariadic,
	}, nil
}

func (p *Parser) current() token {
	if p.curr == nil {
		if p.pushed != nil {
			p.curr = p.pushed
			p.pushed = nil
		} else {
			next, err := p.l.Next()
			if err != nil {
				panic(err)
			}
			p.curr = &next
		}
	}
	return *p.curr
}

func (p *Parser) advance() {
	p.curr = nil
}

// pushback inserts t before whatever token is currently pending.
// After this call, the next current() returns t; the token that was
// pending (if any) is returned after t. Requires pushed == nil (only
// one look-ahead slot is available).
func (p *Parser) pushback(t token) {
	if p.pushed != nil {
		panic("pushback: look-ahead slots exhausted")
	}
	p.pushed = p.curr
	p.curr = &t
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
		// Trailing `slice...` forwarding: only valid as the final argument.
		if p.current().t == tok_ellipsis {
			fpos := p.current().p
			p.advance()
			v = &Node{t: n_variadic_forward, p: fpos, args: []*Node{v}}
			ret = append(ret, v)
			if p.current().t != tok_rparen {
				panic(&interpreterError{"`...` forwarding must be the last argument", fpos})
			}
			return ret
		}
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
		// ';' (auto-inserted by the lexer on a newline after a
		// statement-ending field value) is treated the same as ','
		// — both separate fields. Trailing ';' or ',' before '}' is
		// also fine; the next iteration sees '}' and exits.
		if p.current().t == tok_semicolon {
			p.advance()
			continue
		}
		v := p.parseValue()
		if v.t != n_symbol {
			panic(&interpreterError{fmt.Sprintf("Expected field name, but found: %v\n", v), v.p})
		}
		p.expect(tok_colon)
		v2 := p.parseExpression()
		ret = append(ret, &Node{t: n_stfield, p: v.p, sval: v.sval, args: []*Node{v2}})
		if p.current().t != tok_comma && p.current().t != tok_semicolon {
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
		return &Node{t: n_number, p: c.p, ival: c.nval}
	} else if c.t == tok_str {
		p.advance()
		return &Node{t: n_str, p: c.p, sval: c.sval}
	} else if c.t == tok_byte {
		p.advance()
		return &Node{t: n_byte, p: c.p, ival: c.nval}
	} else if c.t == tok_ident {
		p.advance()
		return &Node{t: n_symbol, p: c.p, sval: c.sval}
	}
	p.advance()
	panic(&interpreterError{fmt.Sprintf("Expected number, string, or identifier, but found: %s", c.t), c.p})
	//panic(fmt.Sprintf("Expected number, string, identifier or semicolon, but found: %s (%v)\n", c.t, c.p))
}

// isTypeStart reports whether a token type can validly begin a type
// expression. Used by parseTypeName when reading a function-type
// return clause to decide between "explicit return type follows" and
// "no return type, infer void."
func isTypeStart(t toktype) bool {
	switch t {
	case tok_ident, tok_fn, tok_star, tok_maybe_ptr, tok_owned, tok_mut, tok_struct:
		return true
	}
	return false
}

func (p *Parser) parseTypeName() *Node {
	var indirection uint64
	var mutmask uint64
	var ownedmask uint64
	var nilmask uint64

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
		if p.current().t != tok_star && p.current().t != tok_maybe_ptr {
			break
		}
		nullable := p.current().t == tok_maybe_ptr
		p.advance() // consume '*' or '*?'
		indirection++
		if nullable {
			nilmask |= 1 << (indirection - 1)
		}
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

	// Nullable base type (`?T`): the value itself may be null, recorded at the
	// current level (bit `indirection`, = 0 for a bare `?T`). Currently only
	// meaningful for interface types — a nullable interface whose vtable may be
	// 0 — mirroring `*?T` for pointers; ToAST rejects `?` on non-interfaces.
	if p.current().t == tok_question {
		p.advance()
		nilmask |= 1 << indirection
	}

	c := p.current()

	// Anonymous struct type: `struct { field Type, ... }`.
	// Packed into an n_typename with sval="<struct>" and args = n_stfield nodes,
	// recognized by mkTypename to assemble an ASTType with AnonFields.
	// Also handles `multiretu{...}` — the serialized form of a multi-value return
	// type (ASTType.String() emits this when MultiReturn=true) — producing a
	// "<multiretu>" sentinel instead so mkTypename sets MultiReturn=true on reload.
	if c.t == tok_struct || (c.t == tok_ident && c.sval == "multiretu") {
		isMultiRetu := c.t == tok_ident && c.sval == "multiretu"
		structpos := c.p
		p.advance()
		p.expect(tok_lcurly)
		var fieldNodes []*Node
		for p.current().t != tok_rcurly {
			if p.current().t == tok_semicolon || p.current().t == tok_comma {
				p.advance()
				continue
			}
			fieldpos := p.current().p
			fname := p.parseTok()
			if fname.t != n_symbol {
				panic(&interpreterError{fmt.Sprintf("Expected field name, but found: %v", fname), fname.p})
			}
			// Accept an optional ':' between name and type (emitted by
			// ASTType.String() in the .bo type annotation round-trip).
			if p.current().t == tok_colon {
				p.advance()
			}
			ftypeNode := p.parseTypeName()
			fieldNodes = append(fieldNodes, &Node{t: n_stfield, p: fieldpos, sval: fname.sval, args: []*Node{ftypeNode}})
		}
		p.expect(tok_rcurly)
		sval := "<struct>"
		if isMultiRetu {
			sval = "<multiretu>"
		}
		return &Node{
			t:         n_typename,
			p:         structpos,
			sval:      sval,
			mutmask:   mutmask,
			ownedmask: ownedmask,
			nilmask:   nilmask,
			ival:      indirection,
			args:      fieldNodes,
		}
	}

	// Function-pointer type: `fn(t1, t2, ...) [ret]`. The leading `fn`
	// disambiguates it from a regular identifier-typed form. Parses
	// the parameter type list and an optional return type, packs them
	// into a synthetic n_typename whose args carry the signature for
	// mkTypename to assemble.
	if c.t == tok_fn {
		fnpos := c.p
		p.advance()
		p.expect(tok_lparen)
		var argTypes []*Node
		for p.current().t != tok_rparen {
			if p.current().t == tok_semicolon {
				p.advance()
				continue
			}
			at := p.parseTypeName()
			argTypes = append(argTypes, at)
			if p.current().t != tok_comma && p.current().t != tok_semicolon {
				break
			}
			p.advance()
		}
		p.expect(tok_rparen)
		// Optional return type; absent means void.
		var retType *Node
		if isTypeStart(p.current().t) {
			retType = p.parseTypeName()
		} else {
			retType = &Node{t: n_typename, p: fnpos, sval: "void"}
		}
		// Pack into an n_typename with sval="fn" as a sentinel
		// recognized by mkTypename. ival carries argument count;
		// args holds the arg types followed by the return type at
		// the end (the apply-wrappers loop in mkTypename skips this
		// shape because sval == "fn").
		args := append([]*Node{}, argTypes...)
		args = append(args, retType)
		return &Node{
			t:    n_typename,
			p:    fnpos,
			sval: "fn",
			ival: uint64(len(argTypes)),
			args: args,
		}
	}

	// Parenthesized type group: `(T)`. The only reason to write one is to
	// make the leading `*` prefixes bind to the whole group rather than to
	// the base type — e.g. `*(byte[])` is a pointer to a byte slice, as
	// opposed to `*byte[]` which is a slice of byte-pointers. The inner type
	// is parsed recursively; the accumulated outer indirection / mut / owned
	// / nullable masks are then layered on top of the inner ASTType. This is
	// the inverse of ASTType.String(), which already emits `*(byte[])` for
	// pointer-to-slice. Without it, such types are constructible (`&s` where
	// `s byte[]`) but not nameable, which blocks `%s`-style assertions and
	// the `*(byte[])` parameter form.
	if c.t == tok_lparen {
		parenPos := c.p
		p.advance()
		innerNode := p.parseTypeName()
		p.expect(tok_rparen)
		inner := mkTypename(innerNode)
		// Layer the outer pointer levels (collected above) on top of the
		// inner type. Each `*` shifts the inner's existing mut/owned/nil
		// bits up one position, then sets the new outer level's bits from
		// the masks parseTypeName accumulated for that level.
		built := inner
		for i := 0; i < int(indirection); i++ {
			built.MutMask <<= 1
			built.OwnedMask <<= 1
			built.NilMask <<= 1
			built.Indirection++
		}
		// Apply the outer-level qualifier bits the prefix loop recorded.
		// indirection masks occupy bits [0..indirection]; merge them in.
		built.MutMask |= mutmask
		built.OwnedMask |= ownedmask
		built.NilMask |= nilmask
		return &Node{t: n_typename, p: parenPos, sval: "<prebuilt>", prebuilt: &built}
	}

	if c.t != tok_ident {
		panic(&interpreterError{fmt.Sprintf("Expected a type name, but found: %s\n", c.t), c.p})
	}
	typename := c.sval
	p.advance()

	// Optional package-qualified form: `pkg.Type` becomes a single
	// type name "pkg.Type" — the consumer-side StructDeclForName
	// registry stores imported structs under that qualified key.
	if p.current().t == tok_dot {
		p.advance()
		if p.current().t != tok_ident {
			panic(&interpreterError{fmt.Sprintf("Expected a type name after '.', but found: %s", p.current().t), p.current().p})
		}
		typename = typename + "." + p.current().sval
		p.advance()
	}

	// Collect a chain of [...] / [N] wrappers. The first wrapper (innermost) is
	// the one that hugs the base type; successive ones wrap. The compiler
	// applies them in args-order to build a nested Element chain.
	var wrappers []*Node
	firstWrapperPos := c.p
	for p.current().t == tok_lsquare {
		p.advance()
		if p.current().t == tok_number {
			arrsize := p.current().nval
			p.advance()
			p.expect(tok_rsquare)
			wrappers = append(wrappers, &Node{t: n_index, p: c.p, ival: arrsize})
		} else if p.current().t == tok_rsquare {
			p.advance()
			wrappers = append(wrappers, &Node{t: n_slice, p: c.p})
		} else {
			panic(&interpreterError{fmt.Sprintf("Expected a number or ']', but found: %s\n", p.current().t), c.p})
		}
	}

	if len(wrappers) > 0 {
		// Outermost wrapper determines slice-vs-array semantics for the
		// mut/owned check on the level just above the base type.
		outer := wrappers[len(wrappers)-1]
		if outer.t == n_slice {
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
		} else { // outermost is a fixed array
			if mutmask&(1<<indirection) != 0 || ownedmask&(1<<indirection) != 0 {
				panic(&interpreterError{"'mut'/'owned' is only valid on slice types, not fixed-size arrays", firstWrapperPos})
			}
		}
		return &Node{
			t:         n_typename,
			p:         c.p,
			sval:      typename,
			ival:      indirection,
			mutmask:   mutmask,
			ownedmask: ownedmask,
			nilmask:   nilmask,
			args:      wrappers,
		}
	}

	// 'mut' on a plain non-pointer, non-slice type is meaningless.
	// Only fires when indirection==0 (no pointer stars): any mut bits must have
	// come from a leading qualifier directly before the base type.
	if indirection == 0 && mutmask != 0 {
		panic(&interpreterError{fmt.Sprintf("'mut' on non-reference type '%s' has no effect; 'mut' is only valid on pointer or slice types", typename), c.p})
	}
	return &Node{t: n_typename, p: c.p, sval: typename, ival: indirection, mutmask: mutmask, ownedmask: ownedmask, nilmask: nilmask}
}

func (p *Parser) parseValue() *Node {
	// An '[' at value-start position is an array literal '[e1, e2, ...]'.
	// (An '[' after a value is the postfix index/slice form, handled by
	// parsePostfix.) The two contexts don't overlap because parseValue
	// is what's called when we need a new value, not when we're chaining
	// off an existing one.
	if p.current().t == tok_lsquare {
		v := p.parseArrayLiteral()
		return p.parsePostfix(v)
	}
	v := p.parseTok()
	return p.parsePostfix(v)
}

// parseArrayLiteral consumes '[' through ']', collecting a comma-
// separated list of expressions. ';' is accepted as an alternative to
// ',' so multi-line literals work without trailing commas under
// automatic semicolon insertion.
func (p *Parser) parseArrayLiteral() *Node {
	pos := p.current().p
	p.expect(tok_lsquare)
	var elements []*Node
	for p.current().t != tok_rsquare {
		if p.current().t == tok_semicolon {
			p.advance()
			continue
		}
		elements = append(elements, p.parseExpression())
		if p.current().t != tok_comma && p.current().t != tok_semicolon {
			break
		}
		p.advance()
	}
	p.expect(tok_rsquare)
	return &Node{t: n_arrlit, p: pos, args: elements}
}

// parsePostfix wraps v in zero or more postfix operators:
//
//   - `.member`     produces n_dot{args: [v, member-symbol]}
//   - `[index]`     produces n_index{args: [v, index-expr]}
//   - `[lo:hi]`     produces n_slice{args: [v, lo|nil, hi|nil]}
//   - `(args)`      produces n_funcall (only when v is a bare symbol;
//     calls through expressions aren't supported)
//   - `{fields}`    produces n_stlit (only when v is a bare symbol; the
//     type name lives in v.sval)
//   - `?`           produces n_nonnull{args: [v]}
//
// The loop terminates as soon as the current token isn't a postfix
// operator, so chains like `a.b[i].c[lo:hi]` work naturally.
func (p *Parser) parsePostfix(v *Node) *Node {
	for {
		c := p.current()
		switch c.t {
		case tok_dot:
			// In a type-switch head, leave `.(type)` for parseTypeSwitch:
			// stop the postfix chain without consuming the dot.
			if p.inTypeSwitchHead {
				return v
			}
			p.advance()
			// Type assertion: `x.(T)`.
			if p.current().t == tok_lparen {
				dotpos := c.p
				p.advance()
				tn := p.parseTypeName()
				p.expect(tok_rparen)
				v = &Node{t: n_typeassert, p: dotpos, args: []*Node{v, tn}}
				continue
			}
			v2 := p.parseTok()
			if v2.t != n_symbol {
				panic(&interpreterError{fmt.Sprintf("Expected a member name after '.', but found %s", v2.t), v2.p})
			}
			v = &Node{t: n_dot, p: c.p, args: []*Node{v, v2}}
		case tok_lsquare:
			p.advance()
			// `T[](e)` cast: a slice-type expression in call position.
			// The proposal's projection-cast syntax (`byte[](err)`) needs
			// this; without it the parser would push the empty brackets
			// into parseIndexOrSlice which then expects an index
			// expression. We only fold the form when v is a bare symbol
			// or pkg.Type — anything else (`x[](...)` on a value) keeps
			// today's "no index expression" error.
			if p.current().t == tok_rsquare {
				p.advance() // consume `]`
				if p.current().t == tok_lparen {
					var typeName string
					switch {
					case v.t == n_symbol:
						typeName = v.sval
					case v.t == n_dot && len(v.args) == 2 &&
						v.args[0].t == n_symbol && v.args[1].t == n_symbol:
						typeName = v.args[0].sval + "." + v.args[1].sval
					default:
						panic(&interpreterError{"`[]` in expression position requires a bare type identifier (e.g. `byte[](e)`)", c.p})
					}
					p.advance() // consume `(`
					args := p.parseArgs()
					p.expect(tok_rparen)
					v = &Node{t: n_funcall, p: v.p, sval: typeName + "[]", args: args}
					continue
				}
				panic(&interpreterError{"`[]` not allowed in expression position; bare type expressions are only valid as casts (`T[](e)`)", c.p})
			}
			v = p.parseIndexOrSlice(v, c.p)
		case tok_lparen:
			// Function-call form: direct `name(args)` or qualified
			// `pkg.name(args)`. The qualified shape stays as
			// Dot(symbol_pkg, funcall_fn) — that's what n_dot's ToAST
			// handler recognizes and lowers to a Funcall whose Callee is
			// a Dot selector.
			switch {
			case v.t == n_symbol:
				p.advance()
				args := p.parseArgs()
				p.expect(tok_rparen)
				v = &Node{t: n_funcall, p: v.p, sval: v.sval, args: args}
			case v.t == n_dot && len(v.args) == 2 && v.args[1].t == n_symbol:
				p.advance()
				args := p.parseArgs()
				p.expect(tok_rparen)
				rhs := v.args[1]
				v.args[1] = &Node{t: n_funcall, p: rhs.p, sval: rhs.sval, args: args}
			default:
				return v
			}
		case tok_lcurly:
			// Struct-literal form: either bare 'Name{...}' or qualified
			// 'pkg.Name{...}'. The leading type identifier (v) is kept
			// verbatim on the n_stlit node as typeIdent so that ToAST
			// can route it through the shared selector resolver. The
			// flat sval below preserves the historical key format that
			// the importer uses for cross-package structs (StructDecl
			// names are registered as "pkg.Type") and that codegen
			// consults at compile.go's struct lookup sites; ToAST may
			// rewrite sval after resolving if a future selector kind
			// needs a different identity.
			var typeName string
			var typePos position
			switch {
			case v.t == n_symbol:
				typeName = v.sval
				typePos = v.p
			case v.t == n_dot && len(v.args) == 2 &&
				v.args[0].t == n_symbol && v.args[1].t == n_symbol:
				typeName = v.args[0].sval + "." + v.args[1].sval
				typePos = v.p
			default:
				return v
			}
			typeIdent := v
			p.advance()
			fields := p.parseStructLiteral()
			p.expect(tok_rcurly)
			v = &Node{t: n_stlit, p: typePos, sval: typeName, args: fields, typeIdent: typeIdent}
		case tok_question:
			p.advance()
			v = &Node{t: n_nonnull, p: c.p, args: []*Node{v}}
		default:
			return v
		}
	}
}

// parseIndexOrSlice handles the `[...]` body after the opening bracket
// has been consumed. The base expression to index/slice into is v, and
// pos is the bracket position for diagnostics.
func (p *Parser) parseIndexOrSlice(v *Node, pos position) *Node {
	if p.current().t == tok_colon {
		// [: ...
		p.advance()
		if p.current().t == tok_rsquare {
			// [:]
			p.advance()
			return &Node{t: n_slice, p: pos, args: []*Node{v, nil, nil}}
		}
		// [: hi]
		hi := p.parseExpression()
		p.expect(tok_rsquare)
		return &Node{t: n_slice, p: pos, args: []*Node{v, nil, hi}}
	}
	lo := p.parseExpression()
	if p.current().t == tok_colon {
		// [lo : ...
		p.advance()
		if p.current().t == tok_rsquare {
			// [lo:]
			p.advance()
			return &Node{t: n_slice, p: pos, args: []*Node{v, lo, nil}}
		}
		hi := p.parseExpression()
		p.expect(tok_rsquare)
		return &Node{t: n_slice, p: pos, args: []*Node{v, lo, hi}}
	}
	p.expect(tok_rsquare)
	// Plain [expr] — lo is the index expression.
	return &Node{t: n_index, p: pos, args: []*Node{v, lo}}
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
		args = append(args, &Node{t: n_none, p: p.current().p})
	}
	p.expect(tok_semicolon)
	if p.current().t != tok_semicolon {
		condition := p.parseExpression()
		args = append(args, condition)
	} else {
		args = append(args, &Node{t: n_none, p: p.current().p})
	}
	p.expect(tok_semicolon)
	if p.current().t != tok_rparen {
		iter := p.parseExpression()
		args = append(args, iter)
	} else {
		args = append(args, &Node{t: n_none, p: p.current().p})
	}
	p.expect(tok_rparen)
	body := p.parseExpression()
	args = append(args, body)
	return &Node{t: n_for, p: forpos, args: args}
}

// parseTypeSwitch parses `switch (v x.(type)) { case T {...} ... default {...} }`.
// The head binds `v` to the switched value `x`, narrowed to each case's type.
func (p *Parser) parseTypeSwitch() *Node {
	swpos := p.current().p
	p.expect(tok_switch)
	p.expect(tok_lparen)
	bind := p.parseTok()
	if bind.t != n_symbol {
		panic(&interpreterError{fmt.Sprintf("Expected a binding name in type switch, but found %v", bind), bind.p})
	}
	p.inTypeSwitchHead = true
	val := p.parseSubexpr()
	p.inTypeSwitchHead = false
	p.expect(tok_dot)
	p.expect(tok_lparen)
	if p.current().t != tok_type {
		panic(&interpreterError{"expected `type` in `switch (v x.(type))`", p.current().p})
	}
	p.advance() // consume `type`
	p.expect(tok_rparen) // close `(type)`
	p.expect(tok_rparen) // close switch head
	p.expect(tok_lcurly)
	args := []*Node{val}
	for p.current().t != tok_rcurly {
		if p.current().t == tok_semicolon {
			p.advance()
			continue
		}
		if p.current().t == tok_case {
			casepos := p.current().p
			p.advance()
			tn := p.parseTypeName()
			body := p.parseExpression()
			args = append(args, &Node{t: n_typecase, p: casepos, ival: 0, args: []*Node{tn, body}})
		} else if p.current().t == tok_default {
			casepos := p.current().p
			p.advance()
			body := p.parseExpression()
			args = append(args, &Node{t: n_typecase, p: casepos, ival: 1, args: []*Node{body}})
		} else {
			panic(&interpreterError{fmt.Sprintf("Expected `case` or `default` in type switch, but found %s", p.current().t), p.current().p})
		}
	}
	p.expect(tok_rcurly)
	return &Node{t: n_typeswitch, p: swpos, sval: bind.sval, args: args}
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
		// Variadic parameter: `name ...T`. The element type T follows the
		// ellipsis; the parameter binds inside the body as a T[] slice. We
		// mark the n_arg node via ival bit 1 and wrap the element type in a
		// slice typename so downstream code sees args = T[].
		variadic := uint64(0)
		if p.current().t == tok_ellipsis {
			p.advance()
			variadic = 2
		}
		v2 := p.parseTypeName()
		if v2.t != n_typename {
			panic(&interpreterError{fmt.Sprintf("Expected type name, but found: %v\n", v), v.p})
		}
		if variadic != 0 {
			// Desugar `...T` to a `T[]` slice parameter: append a slice
			// wrapper to the element typename's wrapper chain.
			if v2.sval == "fn" || v2.sval == "<struct>" || v2.sval == "<multiretu>" {
				panic(&interpreterError{"variadic element type must be a simple type", v2.p})
			}
			v2.args = append(v2.args, &Node{t: n_slice, p: v2.p})
		}
		ret = append(ret, &Node{t: n_arg, p: v.p, sval: v.sval, ival: isVar | variadic, args: []*Node{v2}})
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
		// Multi-value return: `T1, T2, ...` — synthesize a "<multiretu>" struct type.
		if p.current().t == tok_comma {
			rettypePos := rettype.p
			fieldNodes := []*Node{
				{t: n_stfield, p: rettype.p, sval: "_0", args: []*Node{rettype}},
			}
			i := 1
			for p.current().t == tok_comma {
				p.advance()
				tn := p.parseTypeName()
				fieldNodes = append(fieldNodes, &Node{t: n_stfield, p: tn.p, sval: fmt.Sprintf("_%d", i), args: []*Node{tn}})
				i++
			}
			rettype = &Node{t: n_typename, p: rettypePos, sval: "<multiretu>", args: fieldNodes}
		}
	}

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
		return p.parsePostfix(v)
	} else if c.t == tok_lcurly {
		p.advance()
		// Skip leading semicolons (blank lines, auto-inserted ';' before first statement).
		for p.current().t == tok_semicolon {
			p.advance()
		}
		// Two-token lookahead: `ident :` → bare anonymous struct literal `{ field: val, ... }`.
		// Any other opener (keyword, number, non-colon after ident, etc.) → block.
		// In Boson, `ident :` never starts a valid statement, so this is unambiguous.
		if p.current().t == tok_ident {
			identTok := p.current()
			p.advance()
			if p.current().t == tok_colon {
				// Anonymous struct literal: parse first field, then the rest via parseStructLiteral.
				p.advance() // consume ':'
				firstVal := p.parseExpression()
				fields := []*Node{
					{t: n_stfield, p: identTok.p, sval: identTok.sval, args: []*Node{firstVal}},
				}
				if p.current().t == tok_comma || p.current().t == tok_semicolon {
					p.advance()
					fields = append(fields, p.parseStructLiteral()...)
				}
				p.expect(tok_rcurly)
				return &Node{t: n_stlit, p: c.p, sval: "", args: fields}
			}
			// Not a struct literal: restore the identifier so block parsing sees it.
			p.pushback(identTok)
		}
		// Parse as a block.
		var block []*Node
		for p.current().t != tok_rcurly {
			if p.current().t == tok_semicolon {
				p.advance()
				continue
			}
			v := p.parseStatement()
			block = append(block, v)
		}
		p.expect(tok_rcurly)
		return &Node{t: n_block, p: c.p, args: block}
	} else if c.t == tok_if {
		return p.parseIf()
	} else if c.t == tok_for {
		return p.parseFor()
	} else if c.t == tok_switch {
		return p.parseTypeSwitch()
	} else if c.t == tok_break {
		p.advance()
		return &Node{t: n_break, p: c.p}
	} else if c.t == tok_continue {
		p.advance()
		return &Node{t: n_continue, p: c.p}
	} else if c.t == tok_return {
		p.advance()
		if p.current().t == tok_rcurly || p.current().t == tok_semicolon {
			return &Node{t: n_return, p: c.p, args: []*Node{}}
		}
		val := p.parseExpression()
		// Multi-value return: `return e1, e2, ...` — args holds all values.
		if p.current().t == tok_comma {
			vals := []*Node{val}
			for p.current().t == tok_comma {
				p.advance()
				vals = append(vals, p.parseExpression())
			}
			return &Node{t: n_return, p: c.p, args: vals}
		}
		return &Node{t: n_return, p: c.p, args: []*Node{val}}
	} else if c.t == tok_fn {
		return p.parseFn()
	} else if c.t == tok_owned {
		// owned(expr) — unsafe ownership promotion expression.
		p.advance()
		if p.current().t != tok_lparen {
			panic(&interpreterError{fmt.Sprintf("'owned' in expression context must be followed by '(', but found %s", p.current().t), c.p})
		}
		p.advance()
		inner := p.parseExpression()
		p.expect(tok_rparen)
		return &Node{t: n_ownedpromo, p: c.p, args: []*Node{inner}}
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
	} else if c.t == tok_not {
		p.advance()
		operand := p.parseUnary()
		return &Node{t: n_not, p: c.p, args: []*Node{operand}}
	} else if c.t == tok_amp {
		p.advance()
		// `&` parses two shapes today:
		//   - `&name`         → address-of-variable (sval = name)
		//   - `&Type{ ... }`  → address-of-literal (args[0] = the literal)
		// The literal form is only valid in static-init context; runtime
		// codegen rejects it. We distinguish the shapes by what parseValue
		// returns. parseValue calls parsePostfix, which folds `Type{...}`
		// into an n_stlit, so this works without any special casing on
		// the `&` side.
		inner := p.parseValue()
		if inner.t == n_symbol {
			return &Node{t: n_address, p: c.p, sval: inner.sval}
		}
		return &Node{t: n_address, p: c.p, args: []*Node{inner}}
	}
	return p.parseSubexpr()
}

func (p *Parser) parseSubexpr() *Node {
	v := p.parseParens()
	// parseParens returns either a literal/symbol (via parseValue, which
	// has already chewed through any postfix chain) or a sub-expression
	// from inside parens (which is fully formed). Either way the dot/
	// index/slice/call chain has already been applied; here we only
	// handle mul/div at this precedence level.
	for {
		c := p.current()
		switch c.t {
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

// parseBindingSpec parses just the name and optional type of a binding, stopping
// before any `=` initializer. Used inside multi-bind to parse each individual binding.
func (p *Parser) parseBindingSpec(isConst bool) *Node {
	if p.current().t != tok_ident {
		panic(&interpreterError{fmt.Sprintf("Expected an identifier in binding declaration, but found: %s\n", p.current().t), p.current().p})
	}
	pos := p.current().p
	name := p.current().sval
	p.advance()
	var typeNode *Node
	if p.current().t == tok_eq || p.current().t == tok_comma {
		typeNode = &Node{t: n_typename, p: pos, sval: "<infer>"}
	} else {
		typeNode = p.parseTypeName()
	}
	constVal := uint64(0)
	if isConst {
		constVal = 1
	}
	return &Node{t: n_var, p: pos, sval: name, ival: constVal, args: []*Node{typeNode}}
}

// parseBindingDecl parses a var or const declaration after the keyword has been consumed.
// Form: `<kw> name TypeName` or `<kw> name TypeName = expr` or `<kw> name = expr` (inferred).
// isConst is encoded in the node via ival: 0 = var, 1 = const.
// args[0] is the typename node (sval="<infer>" when type is omitted); args[1] (if present) is the initializer expression.
func (p *Parser) parseBindingDecl(isConst bool) *Node {
	if p.current().t != tok_ident {
		panic(&interpreterError{fmt.Sprintf("Expected an identifier in binding declaration, but found: %s\n", p.current().t), p.current().p})
	}
	pos := p.current().p
	name := p.current().sval
	p.advance()
	var typeNode *Node
	if p.current().t == tok_eq || p.current().t == tok_decl || p.current().t == tok_comma {
		// No explicit type: use sentinel for type inference.
		typeNode = &Node{t: n_typename, p: pos, sval: "<infer>"}
	} else {
		typeNode = p.parseTypeName()
	}
	constVal := uint64(0)
	if isConst {
		constVal = 1
	}
	args := []*Node{typeNode}
	// Accept both the legacy '=' and the new ':=' declaration operator
	// (phase-1 migration: both spellings parse to the same node).
	if p.current().t == tok_eq || p.current().t == tok_decl {
		p.advance()
		args = append(args, p.parseExpression())
	}
	return &Node{t: n_var, p: pos, sval: name, ival: constVal, args: args}
}

func (p *Parser) parseBoolOp() *Node {
	v := p.parseBitOr()
	for {
		c := p.current()
		switch c.t {
		case tok_booland:
			p.advance()
			v2 := p.parseBitOr()
			v = &Node{t: n_booland, p: c.p, args: []*Node{v, v2}}
			continue
		case tok_boolor:
			p.advance()
			v2 := p.parseBitOr()
			v = &Node{t: n_boolor, p: c.p, args: []*Node{v, v2}}
			continue
		}
		break
	}
	return v
}

func (p *Parser) parseBitOr() *Node {
	v := p.parseBitAnd()
	for {
		c := p.current()
		if c.t != tok_pipe {
			break
		}
		p.advance()
		v2 := p.parseBitAnd()
		v = &Node{t: n_bitor, p: c.p, args: []*Node{v, v2}}
	}
	return v
}

func (p *Parser) parseBitAnd() *Node {
	v := p.parseCompare()
	for {
		c := p.current()
		if c.t != tok_amp {
			break
		}
		p.advance()
		v2 := p.parseCompare()
		v = &Node{t: n_bitand, p: c.p, args: []*Node{v, v2}}
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

// parseMultiBind continues parsing a multi-bind declaration after the first
// binding has been parsed. `first` is the first n_var node (no initializer).
// Subsequent bindings may be prefixed with `var` or `const`; the shared `= expr`
// follows the last binding. Returns an n_multibind node whose args are the
// individual n_var binding nodes followed by the shared initializer.
func (p *Parser) parseMultiBind(first *Node) *Node {
	pos := first.p
	bindings := []*Node{first}
	for p.current().t == tok_comma {
		p.advance()
		isConst := false
		if p.current().t == tok_var {
			p.advance()
		} else if p.current().t == tok_const {
			p.advance()
			isConst = true
		}
		b := p.parseBindingSpec(isConst)
		bindings = append(bindings, b)
	}
	if p.current().t != tok_eq && p.current().t != tok_decl {
		panic(&interpreterError{"multi-bind: expected '=' or ':=' after binding list", p.current().p})
	}
	p.advance()
	init := p.parseExpression()
	args := append(bindings, init)
	return &Node{t: n_multibind, p: pos, args: args}
}

// parseStatement parses one statement in a block body. It is identical to
// parseExpression except that it additionally recognizes the comma-LHS
// multi-value re-assignment form `lv0, lv1, ... = rhs` (e.g. `v, ok = x.(T)`).
//
// This handling lives here, NOT in parseExpression, on purpose: parseExpression
// is reused inside argument lists, struct literals, index/slice expressions and
// binding lists, where a trailing comma belongs to the *caller* and must not be
// consumed. At statement position a top-level comma after a parsed expression
// is unambiguous — no plain statement otherwise contains one — so it can only
// begin a multi-assignment LHS continuation. parseExpression already consumes
// the `var`/`const`-prefixed multi-*bind* forms, so by the time we see a bare
// comma here the LHS is a list of existing lvalues being re-assigned.
// startsBareDecl reports whether the current IDENT begins a keyword-less
// declaration — `x :=` or `x TYPE :=` — by peeking one token past the name
// and restoring it. A bare declaration is immutable (the `var`-prefixed
// forms are handled by parseExpression). Pointer-typed bare declarations
// (`x *foo :=`) are deferred until the effectful-statement rule lets the
// parser treat a leading `IDENT *` as unambiguously a type.
func (p *Parser) startsBareDecl() bool {
	name := p.current()
	p.advance()
	after := p.current().t
	p.pushback(name)
	switch after {
	case tok_decl: // x :=
		return true
	case tok_ident, tok_owned, tok_mut, tok_fn: // x TYPE := (named/qualified/fn type)
		return true
	}
	return false
}

func (p *Parser) parseStatement() *Node {
	// A bare declaration (`x := …`, `x i64 := …`) is immutable. Reuse the
	// binding-decl path with isConst=true; a trailing comma continues into a
	// multi-bind exactly as the var/const path does.
	if p.current().t == tok_ident && p.startsBareDecl() {
		first := p.parseBindingDecl(true)
		if len(first.args) == 1 && p.current().t == tok_comma {
			return p.parseMultiBind(first)
		}
		return first
	}
	first := p.parseExpression()
	if p.current().t != tok_comma {
		return first
	}
	// Comma-LHS re-assignment: collect the remaining lvalue targets, then the
	// shared `= rhs`. Reuses the n_multibind node shape with ival==1 (the
	// re-assignment marker consumed by ToAST -> MultiAssign).
	pos := first.p
	targets := []*Node{first}
	for p.current().t == tok_comma {
		p.advance()
		targets = append(targets, p.parseBoolOp())
	}
	if p.current().t != tok_eq {
		panic(&interpreterError{"multi-assignment: expected '=' after the comma-separated target list", p.current().p})
	}
	p.advance()
	init := p.parseExpression()
	args := append(targets, init)
	return &Node{t: n_multibind, p: pos, ival: 1, args: args}
}

func (p *Parser) parseExpression() (r *Node) {
	//fmt.Printf("[Start parseExpression] %#v\n", p.current())
	//defer func() { fmt.Printf("[Finish parseExpression]: %#v\n", r) }()
	if p.current().t == tok_pub {
		panic(&interpreterError{"pub is only valid as a top-level declaration modifier", p.current().p})
	}
	if p.current().t == tok_var {
		p.advance()
		first := p.parseBindingDecl(false)
		// Multi-bind: `var a T1, const b T2 = expr` — first binding has no initializer
		// (args len == 1) and the next token is ','.
		if len(first.args) == 1 && p.current().t == tok_comma {
			return p.parseMultiBind(first)
		}
		return first
	} else if p.current().t == tok_const {
		p.advance()
		first := p.parseBindingDecl(true)
		if len(first.args) == 1 && p.current().t == tok_comma {
			return p.parseMultiBind(first)
		}
		return first
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

func (p *Parser) parseImport() *Node {
	importpos := p.current().p
	p.expect(tok_import)
	path := p.parseTok()
	if path.t != n_str {
		panic(&interpreterError{fmt.Sprintf("Expected a string path, but found %#v\n", path), path.p})
	}
	return &Node{t: n_import, p: importpos, sval: path.sval}
}

// parseMethodDef parses a method definition inside a type block:
// name(params) [rettype] { body }
// Returns an n_fn node, same structure as parseFn but without the `fn` keyword.
func (p *Parser) parseMethodDef() *Node {
	fnpos := p.current().p
	fname := p.parseTok()
	if fname.t != n_symbol {
		panic(&interpreterError{fmt.Sprintf("Expected method name, but found: %v", fname), fname.p})
	}
	p.expect(tok_lparen)
	params := p.parseParams()
	p.expect(tok_rparen)
	rettype := &Node{t: n_typename, p: p.current().p, sval: "void"}
	if p.current().t != tok_lcurly {
		rettype = p.parseTypeName()
		if p.current().t == tok_comma {
			rettypePos := rettype.p
			fieldNodes := []*Node{
				{t: n_stfield, p: rettype.p, sval: "_0", args: []*Node{rettype}},
			}
			i := 1
			for p.current().t == tok_comma {
				p.advance()
				tn := p.parseTypeName()
				fieldNodes = append(fieldNodes, &Node{t: n_stfield, p: tn.p, sval: fmt.Sprintf("_%d", i), args: []*Node{tn}})
				i++
			}
			rettype = &Node{t: n_typename, p: rettypePos, sval: "<multiretu>", args: fieldNodes}
		}
	}
	args := append(params, rettype)
	body := p.parseExpression()
	args = append(args, body)
	return &Node{t: n_fn, p: fnpos, ival: uint64(len(params)), sval: fname.sval, args: args}
}

// parseInterfaceMethodSig parses an interface method signature: name(params) [rettype]
// Returns an n_interface_method node.
func (p *Parser) parseInterfaceMethodSig() *Node {
	sigpos := p.current().p
	fname := p.parseTok()
	if fname.t != n_symbol {
		panic(&interpreterError{fmt.Sprintf("Expected method name, but found: %v", fname), fname.p})
	}
	p.expect(tok_lparen)
	params := p.parseParams()
	p.expect(tok_rparen)
	var rettype *Node
	// One n_from clause per return slot, parsed *interleaved* with the slot
	// types (the `from` must be consumed before the multi-return comma test,
	// or `T from(x), U` never sees the comma). Emitted as trailing args only
	// when some slot actually declares `from`, so no-from sigs are unchanged.
	var fromClauses []*Node
	anyFrom := false
	if isTypeStart(p.current().t) {
		rettype = p.parseTypeName()
		fc := p.parseFromClause()
		fromClauses = append(fromClauses, fc)
		anyFrom = anyFrom || len(fc.args) > 0
		if p.current().t == tok_comma {
			rettypePos := rettype.p
			fieldNodes := []*Node{
				{t: n_stfield, p: rettype.p, sval: "_0", args: []*Node{rettype}},
			}
			i := 1
			for p.current().t == tok_comma {
				p.advance()
				tn := p.parseTypeName()
				fieldNodes = append(fieldNodes, &Node{t: n_stfield, p: tn.p, sval: fmt.Sprintf("_%d", i), args: []*Node{tn}})
				fc := p.parseFromClause()
				fromClauses = append(fromClauses, fc)
				anyFrom = anyFrom || len(fc.args) > 0
				i++
			}
			rettype = &Node{t: n_typename, p: rettypePos, sval: "<multiretu>", args: fieldNodes}
		}
	} else {
		rettype = &Node{t: n_typename, p: p.current().p, sval: "void"}
	}
	args := append(params, rettype)
	if anyFrom {
		// Positional per-slot borrow clauses trail the rettype; ToAST reads
		// them as args[nparams+1:].
		args = append(args, fromClauses...)
	}
	return &Node{t: n_interface_method, p: sigpos, ival: uint64(len(params)), sval: fname.sval, args: args}
}

// parseFromClause parses an optional `from(name, ...)` borrow clause following
// an interface-method return-slot type, returning an n_from node whose args
// are the named-parameter symbols (empty args = no clause present). `from` is
// contextual — recognized only in this position, an ordinary identifier
// everywhere else.
func (p *Parser) parseFromClause() *Node {
	cur := p.current()
	if cur.t != tok_ident || cur.sval != "from" {
		return &Node{t: n_from, p: cur.p}
	}
	p.advance()
	p.expect(tok_lparen)
	var names []*Node
	for p.current().t != tok_rparen {
		nm := p.parseTok()
		if nm.t != n_symbol {
			panic(&interpreterError{fmt.Sprintf("Expected a parameter name in from(...), but found: %v", nm), nm.p})
		}
		names = append(names, nm)
		if p.current().t != tok_comma {
			break
		}
		p.advance()
	}
	p.expect(tok_rparen)
	return &Node{t: n_from, p: cur.p, args: names}
}

// parseInterfaceDecl parses: interface Name { sig1 sig2 ... }
func (p *Parser) parseInterfaceDecl() *Node {
	pos := p.current().p
	p.expect(tok_interface)
	name := p.parseTok()
	if name.t != n_symbol {
		panic(&interpreterError{fmt.Sprintf("Expected interface name, but found: %v", name), name.p})
	}
	p.expect(tok_lcurly)
	var sigs []*Node
	for p.current().t != tok_rcurly {
		for p.current().t == tok_semicolon {
			p.advance()
		}
		if p.current().t == tok_rcurly {
			break
		}
		sigs = append(sigs, p.parseInterfaceMethodSig())
	}
	p.expect(tok_rcurly)
	return &Node{t: n_interface_decl, p: pos, sval: name.sval, args: sigs}
}

func (p *Parser) parseTypeDecl() *Node {
	pos := p.current().p
	p.expect(tok_type)
	name := p.parseTok()
	if name.t != n_symbol {
		panic(&interpreterError{fmt.Sprintf("Expected type name, but found: %v\n", name), name.p})
	}
	if p.current().t == tok_values {
		return p.parseValuesDecl(pos, name)
	}
	base := p.parseTypeName()
	if p.current().t == tok_lcurly {
		p.advance() // consume '{'
		var methods []*Node
		for p.current().t != tok_rcurly {
			for p.current().t == tok_semicolon {
				p.advance()
			}
			if p.current().t == tok_rcurly {
				break
			}
			methods = append(methods, p.parseMethodDef())
		}
		p.expect(tok_rcurly)
		args := append([]*Node{base}, methods...)
		return &Node{t: n_typedecl_with_methods, p: pos, sval: name.sval, args: args}
	}
	return &Node{t: n_typedecl, p: pos, sval: name.sval, args: []*Node{base}}
}

// parseValuesDecl parses the body of `type Name values ...`:
//
//	type Name values { CASE_A; CASE_B }
//	type Name values { CASE_A; CASE_B } { methods... }
//	type Name values (T1, T2) { CASE_A: e1, e2; CASE_B: e3, e4 }
//	type Name values (T1, T2) { CASE_A: e1, e2; CASE_B: e3, e4 } { methods... }
//
// Returns an n_valuesdecl node:
//   - sval: type name
//   - ival: number of projection types (so ToAST can slice args)
//   - args: [projection-typename × ival, case × N, method × M] in this order
//
// `pos` is the position of the leading `type` keyword and `name` is the
// already-parsed type-name token.
func (p *Parser) parseValuesDecl(pos position, name *Node) *Node {
	p.expect(tok_values)
	var projections []*Node
	if p.current().t == tok_lparen {
		p.advance()
		for p.current().t != tok_rparen {
			projections = append(projections, p.parseTypeName())
			if p.current().t == tok_comma {
				p.advance()
			}
		}
		p.expect(tok_rparen)
	}
	hasProjections := len(projections) > 0
	p.expect(tok_lcurly)
	var cases []*Node
	for p.current().t != tok_rcurly {
		if p.current().t == tok_semicolon {
			p.advance()
			continue
		}
		if p.current().t == tok_rcurly {
			break
		}
		caseTok := p.current()
		if caseTok.t != tok_ident {
			panic(&interpreterError{fmt.Sprintf("Expected a case name, but found: %s", caseTok.t), caseTok.p})
		}
		p.advance()
		caseNode := &Node{t: n_valuescase, p: caseTok.p, sval: caseTok.sval}
		if p.current().t == tok_colon {
			p.advance()
			caseNode.args = append(caseNode.args, p.parseExpression())
			for p.current().t == tok_comma {
				p.advance()
				caseNode.args = append(caseNode.args, p.parseExpression())
			}
		} else if hasProjections {
			panic(&interpreterError{
				fmt.Sprintf("Case %s missing projection initializers; type %s declares %d projection(s)",
					caseTok.sval, name.sval, len(projections)),
				caseTok.p,
			})
		}
		cases = append(cases, caseNode)
		if p.current().t != tok_semicolon && p.current().t != tok_comma {
			break
		}
		p.advance()
	}
	p.expect(tok_rcurly)
	var methods []*Node
	if p.current().t == tok_lcurly {
		p.advance()
		for p.current().t != tok_rcurly {
			if p.current().t == tok_semicolon {
				p.advance()
				continue
			}
			if p.current().t == tok_rcurly {
				break
			}
			methods = append(methods, p.parseMethodDef())
		}
		p.expect(tok_rcurly)
	}
	args := make([]*Node, 0, len(projections)+len(cases)+len(methods))
	args = append(args, projections...)
	args = append(args, cases...)
	args = append(args, methods...)
	return &Node{
		t:    n_valuesdecl,
		p:    pos,
		sval: name.sval,
		ival: uint64(len(projections)),
		args: args,
	}
}

func (p *Parser) parseTopLevel() *Node {
	isPub := false
	pubPos := p.current().p
	if p.current().t == tok_pub {
		isPub = true
		p.advance()
	}
	if p.current().t == tok_import {
		if isPub {
			panic(&interpreterError{"pub import is not supported", pubPos})
		}
		return p.parseImport()
	} else if p.current().t == tok_type {
		n := p.parseTypeDecl()
		n.isPub = isPub
		return n
	} else if p.current().t == tok_interface {
		n := p.parseInterfaceDecl()
		n.isPub = isPub
		return n
	} else if isPub {
		switch p.current().t {
		case tok_fn:
			n := p.parseFn()
			n.isPub = true
			return n
		case tok_var:
			p.advance()
			n := p.parseBindingDecl(false)
			if len(n.args) == 1 && p.current().t == tok_comma {
				ParseErrorF(n, "pub multi-bind declarations are not supported")
			}
			n.isPub = true
			return n
		case tok_const:
			p.advance()
			n := p.parseBindingDecl(true)
			if len(n.args) == 1 && p.current().t == tok_comma {
				ParseErrorF(n, "pub multi-bind declarations are not supported")
			}
			n.isPub = true
			return n
		default:
			panic(&interpreterError{"pub must be followed by fn, type, interface, var, or const", pubPos})
		}
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
	// Tolerate semicolons between top-level decls (auto-inserted by
	// the lexer after a statement-ending token, e.g. the '}' that
	// closes a previous fn body, or an explicit ';' the user typed).
	for p.current().t == tok_semicolon {
		p.advance()
	}
	return p.parseTopLevel(), nil
}
