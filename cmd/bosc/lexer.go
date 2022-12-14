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
	tok_semicolon
	tok_comma
	tok_ident
	tok_number
	tok_str
	tok_dot
	tok_plus
	tok_minus
	tok_star
	tok_fslash
	tok_eq
	tok_deq // Double-equals
	tok_lt
	tok_gt
	tok_le
	tok_ge

	// Keywords
	tok_if
	tok_else
	tok_return
	tok_for
	tok_fn
	tok_break
	tok_struct
	tok_import
)

var keywords map[string]toktype = map[string]toktype{
	"if":     tok_if,
	"else":   tok_else,
	"return": tok_return,
	"for":    tok_for,
	"fn":     tok_fn,
	"break":  tok_break,
	"struct": tok_struct,
	"import": tok_import,
}

const (
	symbolset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-+*#%/.'&:><=$~"
	numberset = "0123456789.-"
)

type token struct {
	t    toktype
	sval string
	nval float64
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
	case tok_semicolon:
		return "tok_semicolon"
	case tok_comma:
		return "tok_comma"
	case tok_ident:
		return "tok_ident"
	case tok_number:
		return "tok_number"
	case tok_str:
		return "tok_str"
	case tok_dot:
		return "tok_dot"
	case tok_plus:
		return "tok_plus"
	case tok_minus:
		return "tok_minus"
	case tok_star:
		return "tok_star"
	case tok_fslash:
		return "tok_fslash"
	case tok_eq:
		return "tok_eq"
	case tok_deq:
		return "tok_deq"
	case tok_lt:
		return "tok_lt"
	case tok_gt:
		return "tok_gt"
	case tok_le:
		return "tok_le"
	case tok_ge:
		return "tok_ge"
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
	case tok_struct:
		return "tok_struct"
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

func (l *lexer) consumeLine() {
	for {
		r, _, err := l.r.ReadRune()
		if err != nil {
			if err == io.EOF {
				return
			} else {
				panic(&interpreterError{fmt.Sprintf("unexpected read error: %v", err), l.p})
			}
		}
		l.p.charoff += 1
		l.p.linecharoff += 1
		if r == '\n' {
			l.p.lineoff += 1
			l.p.linecharoff = 1
			break
		}
	}
}

func (l *lexer) parseNumber(head *rune) token {
	var ret []rune
	if head != nil {
		ret = append(ret, *head)
	}
	for r := l.headRune(); strings.ContainsRune(numberset, r); r = l.headRune() {
		ret = append(ret, r)
		l.nextRune()
	}
	if string(ret) == "." {
		return token{t: tok_dot, p: l.p}
	}
	f, err := strconv.ParseFloat(string(ret), 64)
	if err != nil {
		panic(&interpreterError{fmt.Sprintf("parsing number: %v", err), l.p})
	}
	return token{t: tok_number, nval: f, p: l.p}
}

func (l *lexer) parseIdent() token {
	var ret []rune
	for r := l.headRune(); unicode.IsLetter(r) || unicode.IsNumber(r); r = l.headRune() {
		ret = append(ret, r)
		l.nextRune()
	}
	id := string(ret)
	if t, ok := keywords[id]; ok {
		return token{t: t, p: l.p}
	}
	return token{t: tok_ident, sval: string(ret), p: l.p}
}

func (l *lexer) parseString() token {
	l.nextRune()
	var ret []rune
	for r := l.readRune(); r != '"'; r = l.readRune() {
		if r == '\\' {
			r = l.readRune()
			switch r {
			case 'n':
				ret = append(ret, '\n')
			case '"':
				ret = append(ret, '"')
			default:
				ret = append(ret, r)
			}
		} else {
			ret = append(ret, r)
		}
	}
	return token{t: tok_str, sval: string(ret), p: l.p}
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
	return &lexer{r: bufio.NewReader(r), p: position{fname: fname, lineoff: 2, linecharoff: 1}}
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
		if nopos {
			rt.p = position{}
		}
	}()

	l.consumeWhitespace()

	r := l.headRune()
	switch r {
	case '(':
		l.nextRune()
		return token{t: tok_lparen, p: l.p}, nil
	case ')':
		l.nextRune()
		return token{t: tok_rparen, p: l.p}, nil
	case '{':
		l.nextRune()
		return token{t: tok_lcurly, p: l.p}, nil
	case '}':
		l.nextRune()
		return token{t: tok_rcurly, p: l.p}, nil
	case '[':
		l.nextRune()
		return token{t: tok_lsquare, p: l.p}, nil
	case ']':
		l.nextRune()
		return token{t: tok_rsquare, p: l.p}, nil
	case '-':
		l.nextRune()
		if strings.ContainsRune(numberset, l.headRune()) {
			return l.parseNumber(&r), nil
		}
		return token{t: tok_minus, p: l.p}, nil
	case ':':
		l.nextRune()
		return token{t: tok_colon, p: l.p}, nil
	case ';':
		l.nextRune()
		return token{t: tok_semicolon, p: l.p}, nil
	case ',':
		l.nextRune()
		return token{t: tok_comma, p: l.p}, nil
	case '"':
		return l.parseString(), nil
	case '.':
		l.nextRune()
		return token{t: tok_dot, p: l.p}, nil
	case '+':
		l.nextRune()
		return token{t: tok_plus, p: l.p}, nil
	case '*':
		l.nextRune()
		return token{t: tok_star, p: l.p}, nil
	case '/':
		l.nextRune()
		if l.headRune() == '/' {
			// Comment
			l.consumeLine()
			return l.Next()
		}
		return token{t: tok_fslash, p: l.p}, nil
	case '=':
		l.nextRune()
		if l.headRune() == '=' {
			l.nextRune()
			return token{t: tok_deq, p: l.p}, nil
		}
		return token{t: tok_eq, p: l.p}, nil
	case '<':
		l.nextRune()
		if l.headRune() == '=' {
			l.nextRune()
			return token{t: tok_le, p: l.p}, nil
		}
		return token{t: tok_lt, p: l.p}, nil
	case '>':
		l.nextRune()
		if l.headRune() == '=' {
			l.nextRune()
			return token{t: tok_ge, p: l.p}, nil
		}
		return token{t: tok_gt, p: l.p}, nil
	}
	if strings.ContainsRune(numberset, r) {
		return l.parseNumber(nil), nil
	} else if unicode.IsLetter(r) {
		return l.parseIdent(), nil
	}
	if r == 0 {
		return token{t: tok_none, p: l.p}, nil
	}

	return token{}, &interpreterError{fmt.Sprintf("Unidentified token: [%v]", r), l.p}
}
