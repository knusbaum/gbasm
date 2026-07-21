package main

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode"
)

type toktype int

const (
	tok_none toktype = iota
	tok_lparen
	tok_rparen
	tok_lcurly
	tok_rcurly
	tok_lsquare
	tok_rsquare
	tok_colon
	tok_decl // ':=' declaration operator
	tok_semicolon
	tok_comma
	tok_ident
	tok_number
	tok_float
	tok_str
	tok_byte
	tok_dot
	tok_plus
	tok_minus
	tok_star
	tok_maybe_ptr
	tok_question
	tok_amp
	tok_fslash
	tok_eq
	tok_deq // Double-equals
	tok_neq
	tok_not
	tok_lt
	tok_gt
	tok_le
	tok_ge
	tok_booland
	tok_boolor
	tok_pipe
	tok_percent
	tok_caret
	tok_shl
	tok_shr
	tok_tilde
	tok_pluseq    // +=
	tok_minuseq   // -=
	tok_stareq    // *=
	tok_slasheq   // /=
	tok_percenteq // %=
	tok_ampeq     // &=
	tok_pipeeq    // |=
	tok_careteq   // ^=
	tok_shleq     // <<=
	tok_shreq     // >>=
	tok_ellipsis

	// Keywords
	tok_if
	tok_else
	tok_return
	tok_for
	tok_fn
	tok_break
	tok_continue
	tok_struct
	tok_import
	tok_var
	tok_mut
	tok_owned
	tok_dispose
	tok_type
	tok_interface
	tok_values
	tok_pub
	tok_switch
	tok_case
	tok_default
	tok_true
	tok_false
)

var keywords map[string]toktype = map[string]toktype{
	"if":        tok_if,
	"else":      tok_else,
	"return":    tok_return,
	"for":       tok_for,
	"fn":        tok_fn,
	"break":     tok_break,
	"continue":  tok_continue,
	"struct":    tok_struct,
	"import":    tok_import,
	"var":       tok_var,
	"mut":       tok_mut,
	"owned":     tok_owned,
	"dispose":   tok_dispose,
	"type":      tok_type,
	"interface": tok_interface,
	"values":    tok_values,
	"pub":       tok_pub,
	"switch":    tok_switch,
	"case":      tok_case,
	"default":   tok_default,
	"true":      tok_true,
	"false":     tok_false,
}

const (
	symbolset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-+*#%/.'&:><=$~"
	numberset = "0123456789."
)

type token struct {
	t    toktype
	sval string
	nval uint64
	fval float64 // set for tok_float
	p    position
}

func (t toktype) String() string {
	switch t {
	case tok_none:
		return "tok_none"
	case tok_lparen:
		return "tok_lparen"
	case tok_rparen:
		return "tok_rparen"
	case tok_lcurly:
		return "tok_lcurly"
	case tok_rcurly:
		return "tok_rcurly"
	case tok_lsquare:
		return "tok_lsquare"
	case tok_rsquare:
		return "tok_rsquare"
	case tok_colon:
		return "tok_colon"
	case tok_decl:
		return "tok_decl"
	case tok_semicolon:
		return "tok_semicolon"
	case tok_comma:
		return "tok_comma"
	case tok_ident:
		return "tok_ident"
	case tok_number:
		return "tok_number"
	case tok_float:
		return "tok_float"
	case tok_str:
		return "tok_str"
	case tok_byte:
		return "tok_byte"
	case tok_dot:
		return "tok_dot"
	case tok_plus:
		return "tok_plus"
	case tok_minus:
		return "tok_minus"
	case tok_star:
		return "tok_star"
	case tok_maybe_ptr:
		return "tok_maybe_ptr"
	case tok_question:
		return "tok_question"
	case tok_amp:
		return "tok_amp"
	case tok_fslash:
		return "tok_fslash"
	case tok_eq:
		return "tok_eq"
	case tok_deq:
		return "tok_deq"
	case tok_neq:
		return "tok_neq"
	case tok_not:
		return "tok_not"
	case tok_lt:
		return "tok_lt"
	case tok_gt:
		return "tok_gt"
	case tok_le:
		return "tok_le"
	case tok_ge:
		return "tok_ge"
	case tok_booland:
		return "tok_booland"
	case tok_boolor:
		return "tok_boolor"
	case tok_pipe:
		return "tok_pipe"
	case tok_percent:
		return "tok_percent"
	case tok_caret:
		return "tok_caret"
	case tok_shl:
		return "tok_shl"
	case tok_shr:
		return "tok_shr"
	case tok_tilde:
		return "tok_tilde"
	case tok_pluseq:
		return "tok_pluseq"
	case tok_minuseq:
		return "tok_minuseq"
	case tok_stareq:
		return "tok_stareq"
	case tok_slasheq:
		return "tok_slasheq"
	case tok_percenteq:
		return "tok_percenteq"
	case tok_ampeq:
		return "tok_ampeq"
	case tok_pipeeq:
		return "tok_pipeeq"
	case tok_careteq:
		return "tok_careteq"
	case tok_shleq:
		return "tok_shleq"
	case tok_shreq:
		return "tok_shreq"
	case tok_if:
		return "tok_if"
	case tok_else:
		return "tok_else"
	case tok_return:
		return "tok_return"
	case tok_for:
		return "tok_for"
	case tok_fn:
		return "tok_fn"
	case tok_break:
		return "tok_break"
	case tok_continue:
		return "tok_continue"
	case tok_struct:
		return "tok_struct"
	case tok_var:
		return "tok_var"
	case tok_mut:
		return "tok_mut"
	case tok_owned:
		return "tok_owned"
	case tok_dispose:
		return "tok_dispose"
	case tok_type:
		return "tok_type"
	case tok_interface:
		return "tok_interface"
	case tok_values:
		return "tok_values"
	case tok_pub:
		return "tok_pub"
	case tok_ellipsis:
		return "tok_ellipsis"
	case tok_switch:
		return "tok_switch"
	case tok_case:
		return "tok_case"
	case tok_default:
		return "tok_default"
	case tok_true:
		return "tok_true"
	case tok_false:
		return "tok_false"
	}
	return "UNKNOWN"
}

type position struct {
	fname       string
	charoff     uint
	lineoff     uint
	linecharoff uint
}

func (p position) String() string {
	return fmt.Sprintf("%s:%d:%d", p.fname, p.lineoff, p.linecharoff)
	//return fmt.Sprintf("(line: %d, position %d)", p.lineoff, p.linecharoff)
}

type lexer struct {
	r io.RuneScanner
	p position
	// prevTok is the type of the most recently returned token, used
	// by automatic semicolon insertion to decide whether a newline
	// should terminate a statement.
	prevTok toktype
	// parenDepth counts open '(' and '[' nestings. Auto-insertion is
	// suppressed when depth > 0 so multi-line argument lists, slice
	// expressions, etc. don't get rogue statement terminators in the
	// middle of them.
	parenDepth int
}

// isStatementEnder reports whether t can validly end a statement, in
// the sense used by Go-style automatic semicolon insertion. When a
// newline follows a token in this set (at paren/bracket depth 0),
// the lexer synthesizes a tok_semicolon before continuing.
func isStatementEnder(t toktype) bool {
	switch t {
	case tok_ident, tok_number, tok_float, tok_str, tok_byte,
		tok_true, tok_false,
		tok_rparen, tok_rsquare, tok_rcurly,
		tok_break, tok_continue, tok_return:
		return true
	}
	return false
}

func (l *lexer) readRune() rune {
	r, _, err := l.r.ReadRune()
	if err != nil {
		if err != io.EOF {
			panic(&interpreterError{fmt.Sprintf("unexpected read error: %v", err), l.p})
		}
		return 0
	}
	l.p.charoff += 1
	l.p.linecharoff += 1
	if r == '\n' {
		l.p.lineoff += 1
		l.p.linecharoff = 1
	}
	return r
}

func (l *lexer) headRune() rune {
	r, _, err := l.r.ReadRune()
	if err != nil {
		if err != io.EOF {
			panic(&interpreterError{fmt.Sprintf("unexpected read error: %v", err), l.p})
		}
		return 0
	}
	l.r.UnreadRune()
	return r
}

func (l *lexer) nextRune() {
	r, _, _ := l.r.ReadRune()
	l.p.charoff += 1
	l.p.linecharoff += 1
	if r == '\n' {
		l.p.lineoff += 1
		l.p.linecharoff = 1
	}
}

func (l *lexer) hasNextRune() bool {
	_, _, err := l.r.ReadRune()
	if err != nil {
		return false
	}
	l.r.UnreadRune()
	return true
}

func (l *lexer) consumeWhitespace() {
	for {
		r, _, err := l.r.ReadRune()
		if err != nil {
			if err == io.EOF {
				return
			} else {
				panic(&interpreterError{fmt.Sprintf("unexpected read error: %v", err), l.p})
			}
		}

		if !unicode.IsSpace(r) {
			break
		}
		l.p.charoff += 1
		l.p.linecharoff += 1
		if r == '\n' {
			l.p.lineoff += 1
			l.p.linecharoff = 1
		}
	}
	l.r.UnreadRune()
}

// consumeLine eats characters up to (but not including) the next '\n'.
// The newline itself is left for the whitespace/auto-semicolon path
// in Next() so that a comment after a statement-ending token still
// triggers semicolon insertion.
func (l *lexer) consumeLine() {
	for {
		r, _, err := l.r.ReadRune()
		if err != nil {
			if err == io.EOF {
				return
			}
			panic(&interpreterError{fmt.Sprintf("unexpected read error: %v", err), l.p})
		}
		if r == '\n' {
			l.r.UnreadRune()
			return
		}
		l.p.charoff += 1
		l.p.linecharoff += 1
	}
}

func isHexDigit(r rune) bool {
	return (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
}

func (l *lexer) parseNumber(p position) token {
	// Prefixed literals: 0x (hex), 0b (binary), 0o (octal).
	if l.headRune() == '0' {
		l.nextRune()
		switch l.headRune() {
		case 'x', 'X':
			l.nextRune()
			var digits []rune
			for r := l.headRune(); isHexDigit(r); r = l.headRune() {
				digits = append(digits, r)
				l.nextRune()
			}
			if len(digits) == 0 {
				panic(&interpreterError{"hex literal has no digits after 0x", p})
			}
			n, err := strconv.ParseUint(string(digits), 16, 64)
			if err != nil {
				panic(&interpreterError{fmt.Sprintf("parsing hex literal: %v", err), p})
			}
			return token{t: tok_number, nval: n, p: p}
		case 'b', 'B':
			l.nextRune()
			var digits []rune
			for r := l.headRune(); r == '0' || r == '1'; r = l.headRune() {
				digits = append(digits, r)
				l.nextRune()
			}
			if len(digits) == 0 {
				panic(&interpreterError{"binary literal has no digits after 0b", p})
			}
			n, err := strconv.ParseUint(string(digits), 2, 64)
			if err != nil {
				panic(&interpreterError{fmt.Sprintf("parsing binary literal: %v", err), p})
			}
			return token{t: tok_number, nval: n, p: p}
		case 'o', 'O':
			l.nextRune()
			var digits []rune
			for r := l.headRune(); r >= '0' && r <= '7'; r = l.headRune() {
				digits = append(digits, r)
				l.nextRune()
			}
			if len(digits) == 0 {
				panic(&interpreterError{"octal literal has no digits after 0o", p})
			}
			n, err := strconv.ParseUint(string(digits), 8, 64)
			if err != nil {
				panic(&interpreterError{fmt.Sprintf("parsing octal literal: %v", err), p})
			}
			return token{t: tok_number, nval: n, p: p}
		}
		// Plain decimal starting with 0 (e.g. "0", "0.5", "0e3").
		return l.parseDecimal(p, []rune{'0'})
	}

	// A lone '.' is the selector token, not a number. Boson requires a digit
	// on both sides of a decimal point, so a leading '.' is never a literal.
	if l.headRune() == '.' {
		l.nextRune()
		return token{t: tok_dot, p: p}
	}

	// Standard decimal literal (does not start with 0).
	return l.parseDecimal(p, nil)
}

// parseDecimal scans a decimal literal, integer or floating-point, with
// `lead` already consumed. It is the shared tail of both decimal paths.
//
// The literal is a float if it has a fractional part or an exponent:
//
//	42        integer
//	4.2       float
//	4e2 4E-2  float, no fractional part needed
//
// Once digits have been seen, a following '.' always begins a fractional
// part. That is unambiguous because a selector never follows a numeric
// literal — there are no methods on an untyped numeric constant — and it
// avoids needing two runes of lookahead, which io.RuneScanner does not
// offer. `1.foo` is therefore a malformed float rather than a field access,
// which is also what it was before floats existed, except that it now says
// so instead of silently truncating to 1 and swallowing the dot.
//
// Integers are still parsed as uint64 rather than routed through float64,
// so literals near UINT64_MAX keep every bit.
func (l *lexer) parseDecimal(p position, lead []rune) token {
	ret := lead
	isFloat := false

	for r := l.headRune(); r >= '0' && r <= '9'; r = l.headRune() {
		ret = append(ret, r)
		l.nextRune()
	}

	if l.headRune() == '.' {
		isFloat = true
		ret = append(ret, '.')
		l.nextRune()
		var frac int
		for r := l.headRune(); r >= '0' && r <= '9'; r = l.headRune() {
			ret = append(ret, r)
			l.nextRune()
			frac++
		}
		if frac == 0 {
			panic(&interpreterError{fmt.Sprintf("float literal %q has no digits after the decimal point", string(ret)), p})
		}
	}

	if r := l.headRune(); r == 'e' || r == 'E' {
		isFloat = true
		ret = append(ret, r)
		l.nextRune()
		if sr := l.headRune(); sr == '+' || sr == '-' {
			ret = append(ret, sr)
			l.nextRune()
		}
		var exp int
		for r := l.headRune(); r >= '0' && r <= '9'; r = l.headRune() {
			ret = append(ret, r)
			l.nextRune()
			exp++
		}
		if exp == 0 {
			panic(&interpreterError{fmt.Sprintf("float literal %q has no digits in its exponent", string(ret)), p})
		}
	}

	if isFloat {
		f, err := strconv.ParseFloat(string(ret), 64)
		if err != nil {
			panic(&interpreterError{fmt.Sprintf("parsing float literal %q: %v", string(ret), err), p})
		}
		return token{t: tok_float, fval: f, p: p}
	}

	n, err := strconv.ParseUint(string(ret), 10, 64)
	if err != nil {
		panic(&interpreterError{fmt.Sprintf("parsing number %q: %v", string(ret), err), p})
	}
	return token{t: tok_number, nval: n, p: p}
}

func (l *lexer) parseIdent(p position) token {
	var ret []rune
	for r := l.headRune(); unicode.IsLetter(r) || unicode.IsNumber(r) || r == '_'; r = l.headRune() {
		ret = append(ret, r)
		l.nextRune()
	}
	id := string(ret)
	if t, ok := keywords[id]; ok {
		return token{t: t, p: p}
	}
	return token{t: tok_ident, sval: string(ret), p: p}
}

func (l *lexer) parseString() token {
	p := l.p
	l.nextRune()
	var ret []rune
	for r := l.readRune(); r != '"'; r = l.readRune() {
		// readRune returns 0 on EOF (no error), so a missing closing quote
		// would otherwise spin here forever.
		if r == 0 {
			panic(&interpreterError{"Unterminated string literal", p})
		}
		if r == '\\' {
			r = l.readRune()
			if r == 0 {
				panic(&interpreterError{"Unterminated string literal after escape", p})
			}
			switch r {
			case 'n':
				ret = append(ret, '\n')
			case 't':
				ret = append(ret, '\t')
			case '\\':
				ret = append(ret, '\\')
			case '"':
				ret = append(ret, '"')
			case '0':
				ret = append(ret, 0)
			case 'x':
				h1 := l.readRune()
				h2 := l.readRune()
				v, err := strconv.ParseUint(string([]rune{h1, h2}), 16, 8)
				if err != nil {
					panic(&interpreterError{fmt.Sprintf("invalid \\x escape: \\x%c%c", h1, h2), p})
				}
				ret = append(ret, rune(v))
			default:
				ret = append(ret, r)
			}
		} else {
			ret = append(ret, r)
		}
	}
	return token{t: tok_str, sval: string(ret), p: p}
}

func (l *lexer) parseChar() token {
	p := l.p
	l.nextRune()
	var ret rune
	r := l.readRune()
	if r == '\\' {
		r = l.readRune()
		switch r {
		case 'n':
			ret = '\n'
		case 't':
			ret = '\t'
		case '\\':
			ret = '\\'
		case '"':
			ret = '"'
		case '0':
			ret = 0
		case 'x':
			h1 := l.readRune()
			h2 := l.readRune()
			v, err := strconv.ParseUint(string([]rune{h1, h2}), 16, 8)
			if err != nil {
				panic(&interpreterError{fmt.Sprintf("invalid \\x escape: \\x%c%c", h1, h2), p})
			}
			ret = rune(v)
		default:
			ret = r
		}
	} else {
		ret = r
	}
	r = l.readRune()
	if r != '\'' {
		panic(&interpreterError{fmt.Sprintf("too many characters in char literal"), l.p})
	}
	return token{t: tok_byte, nval: uint64(ret), p: p}
}

func unparseString(s string) string {
	rs := []rune(s)
	var ret strings.Builder
	for _, r := range rs {
		switch r {
		case '\n':
			ret.WriteString(`\n`)
		case '"':
			ret.WriteString(`\"`)
		default:
			ret.WriteRune(r)
		}
	}
	return ret.String()
}

func NewLexer(fname string, r io.Reader) *lexer {
	return NewLexerAt(fname, r, 2)
}

// NewLexerAt creates a lexer whose first token reports line startLine.
// Use this when the caller has already consumed lines from the reader
// (e.g. the `package` preamble in main.go) so that error positions
// reflect actual line numbers in the source file.
func NewLexerAt(fname string, r io.Reader, startLine uint) *lexer {
	return &lexer{r: bufio.NewReader(r), p: position{fname: fname, lineoff: startLine, linecharoff: 1}}
}

// This is a variable that controls the setting of position values in tokens.
// It is used by the tests to disable position values to make comparison possible.
var nopos = false

func (l *lexer) Next() (rt token, re error) {
	defer func() {
		if e := recover(); e != nil {
			if le, ok := e.(*interpreterError); ok {
				rt = token{}
				re = le
				return
			}
			panic(e)
		}
		if re == nil {
			// Track paren depth so auto-semicolon insertion can be
			// suppressed inside multi-line argument lists and slice
			// expressions, where bare newlines don't end statements.
			switch rt.t {
			case tok_lparen, tok_lsquare:
				l.parenDepth++
			case tok_rparen, tok_rsquare:
				if l.parenDepth > 0 {
					l.parenDepth--
				}
			}
			l.prevTok = rt.t
		}
		if nopos {
			rt.p = position{}
		}
	}()

	// Whitespace + automatic semicolon insertion. We can't use
	// consumeWhitespace here because we need to notice when we cross
	// a newline and react before discarding it.
	crossedNewline := false
	for {
		r := l.headRune()
		if r == 0 || !unicode.IsSpace(r) {
			break
		}
		if r == '\n' {
			crossedNewline = true
		}
		l.nextRune()
	}
	if crossedNewline && l.parenDepth == 0 && isStatementEnder(l.prevTok) {
		return token{t: tok_semicolon, p: l.p}, nil
	}

	p := l.p // start position of this token, before any consumption
	r := l.headRune()
	switch r {
	case '(':
		l.nextRune()
		return token{t: tok_lparen, p: p}, nil
	case ')':
		l.nextRune()
		return token{t: tok_rparen, p: p}, nil
	case '{':
		l.nextRune()
		return token{t: tok_lcurly, p: p}, nil
	case '}':
		l.nextRune()
		return token{t: tok_rcurly, p: p}, nil
	case '[':
		l.nextRune()
		return token{t: tok_lsquare, p: p}, nil
	case ']':
		l.nextRune()
		return token{t: tok_rsquare, p: p}, nil
	case '-':
		l.nextRune()
		if l.headRune() == '=' {
			l.nextRune()
			return token{t: tok_minuseq, p: p}, nil
		}
		return token{t: tok_minus, p: p}, nil
	case ':':
		l.nextRune()
		if l.headRune() == '=' {
			l.nextRune()
			return token{t: tok_decl, p: p}, nil
		}
		return token{t: tok_colon, p: p}, nil
	case ';':
		l.nextRune()
		return token{t: tok_semicolon, p: p}, nil
	case ',':
		l.nextRune()
		return token{t: tok_comma, p: p}, nil
	case '"':
		return l.parseString(), nil
	case '\'':
		return l.parseChar(), nil
	case '.':
		l.nextRune()
		if l.headRune() == '.' {
			l.nextRune()
			if l.headRune() == '.' {
				l.nextRune()
				return token{t: tok_ellipsis, p: p}, nil
			}
			return token{}, &interpreterError{"unexpected '..'; did you mean '...'?", p}
		}
		return token{t: tok_dot, p: p}, nil
	case '+':
		l.nextRune()
		if l.headRune() == '=' {
			l.nextRune()
			return token{t: tok_pluseq, p: p}, nil
		}
		return token{t: tok_plus, p: p}, nil
	case '*':
		l.nextRune()
		// Note: `*?` is NOT a combined token — prefix type syntax needs `?*T`
		// (nullable pointer) and `*?T` (pointer to nullable) to differ, so `?`
		// lexes on its own (tok_question) and the parser orders them.
		if l.headRune() == '=' {
			l.nextRune()
			return token{t: tok_stareq, p: p}, nil
		}
		return token{t: tok_star, p: p}, nil
	case '?':
		l.nextRune()
		return token{t: tok_question, p: p}, nil
	case '&':
		l.nextRune()
		if l.headRune() == '&' {
			l.nextRune()
			return token{t: tok_booland, p: p}, nil
		}
		if l.headRune() == '=' {
			l.nextRune()
			return token{t: tok_ampeq, p: p}, nil
		}
		return token{t: tok_amp, p: p}, nil
	case '|':
		l.nextRune()
		if l.headRune() == '|' {
			l.nextRune()
			return token{t: tok_boolor, p: p}, nil
		}
		if l.headRune() == '=' {
			l.nextRune()
			return token{t: tok_pipeeq, p: p}, nil
		}
		return token{t: tok_pipe, p: p}, nil
	case '/':
		l.nextRune()
		if l.headRune() == '/' {
			// Comment
			l.consumeLine()
			return l.Next()
		}
		if l.headRune() == '=' {
			l.nextRune()
			return token{t: tok_slasheq, p: p}, nil
		}
		return token{t: tok_fslash, p: p}, nil
	case '!':
		l.nextRune()
		if l.headRune() == '=' {
			l.nextRune()
			return token{t: tok_neq, p: p}, nil
		}
		return token{t: tok_not, p: p}, nil
	case '=':
		l.nextRune()
		if l.headRune() == '=' {
			l.nextRune()
			return token{t: tok_deq, p: p}, nil
		}
		return token{t: tok_eq, p: p}, nil
	case '<':
		l.nextRune()
		if l.headRune() == '=' {
			l.nextRune()
			return token{t: tok_le, p: p}, nil
		}
		if l.headRune() == '<' {
			l.nextRune()
			if l.headRune() == '=' {
				l.nextRune()
				return token{t: tok_shleq, p: p}, nil
			}
			return token{t: tok_shl, p: p}, nil
		}
		return token{t: tok_lt, p: p}, nil
	case '>':
		l.nextRune()
		if l.headRune() == '=' {
			l.nextRune()
			return token{t: tok_ge, p: p}, nil
		}
		if l.headRune() == '>' {
			l.nextRune()
			if l.headRune() == '=' {
				l.nextRune()
				return token{t: tok_shreq, p: p}, nil
			}
			return token{t: tok_shr, p: p}, nil
		}
		return token{t: tok_gt, p: p}, nil
	case '%':
		l.nextRune()
		if l.headRune() == '=' {
			l.nextRune()
			return token{t: tok_percenteq, p: p}, nil
		}
		return token{t: tok_percent, p: p}, nil
	case '^':
		l.nextRune()
		if l.headRune() == '=' {
			l.nextRune()
			return token{t: tok_careteq, p: p}, nil
		}
		return token{t: tok_caret, p: p}, nil
	case '~':
		l.nextRune()
		return token{t: tok_tilde, p: p}, nil
	}
	if strings.ContainsRune(numberset, r) {
		return l.parseNumber(p), nil
	} else if unicode.IsLetter(r) || r == '_' {
		return l.parseIdent(p), nil
	}
	if r == 0 {
		return token{t: tok_none, p: l.p}, nil
	}

	return token{}, &interpreterError{fmt.Sprintf("Unidentified token: [%v]", r), l.p}
}
