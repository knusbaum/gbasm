package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLexer(t *testing.T) {
	nopos = true
	defer func() { nopos = false }()
	for _, tt := range []struct {
		in  string
		out []token
		err string
	}{
		{
			in:  `import "/some/path/to/some/package"`,
			out: []token{token{t: tok_import}, token{t: tok_str, sval: `/some/path/to/some/package`}},
		},
		{
			in:  "foo",
			out: []token{token{t: tok_ident, sval: "foo"}},
		},
		{
			in:  `"hello\n world!"`,
			out: []token{token{t: tok_str, sval: "hello\n world!"}},
		},
		{
			in: "5 + 6 + 7 * 8 + foo() - bar() / baz",
			out: []token{
				token{t: tok_number, nval: 5},
				token{t: tok_plus},
				token{t: tok_number, nval: 6},
				token{t: tok_plus},
				token{t: tok_number, nval: 7},
				token{t: tok_star},
				token{t: tok_number, nval: 8},
				token{t: tok_plus},
				token{t: tok_ident, sval: "foo"},
				token{t: tok_lparen},
				token{t: tok_rparen},
				token{t: tok_minus},
				token{t: tok_ident, sval: "bar"},
				token{t: tok_lparen},
				token{t: tok_rparen},
				token{t: tok_fslash},
				token{t: tok_ident, sval: "baz"},
			},
		},
		{
			in: "if (x == 12) { x = x - 1 } else if (x < 0) { x = 0 } else { x = 100 }",
			out: []token{
				token{t: tok_if},
				token{t: tok_lparen},
				token{t: tok_ident, sval: "x"},
				token{t: tok_deq},
				token{t: tok_number, nval: 12},
				token{t: tok_rparen},
				token{t: tok_lcurly},
				token{t: tok_ident, sval: "x"},
				token{t: tok_eq},
				token{t: tok_ident, sval: "x"},
				token{t: tok_minus},
				token{t: tok_number, nval: 1},
				token{t: tok_rcurly},
				token{t: tok_else},
				token{t: tok_if},
				token{t: tok_lparen},
				token{t: tok_ident, sval: "x"},
				token{t: tok_lt},
				token{t: tok_number, nval: 0},
				token{t: tok_rparen},
				token{t: tok_lcurly},
				token{t: tok_ident, sval: "x"},
				token{t: tok_eq},
				token{t: tok_number, nval: 0},
				token{t: tok_rcurly},
				token{t: tok_else},
				token{t: tok_lcurly},
				token{t: tok_ident, sval: "x"},
				token{t: tok_eq},
				token{t: tok_number, nval: 100},
				token{t: tok_rcurly},
			},
		},
		{
			in: "for (x = 1; x < 10; x = x + 1) { printf(\"X is %v\\n\", x) }",
			out: []token{
				token{t: tok_for},
				token{t: tok_lparen},
				token{t: tok_ident, sval: "x"},
				token{t: tok_eq},
				token{t: tok_number, nval: 1},
				token{t: tok_semicolon},
				token{t: tok_ident, sval: "x"},
				token{t: tok_lt},
				token{t: tok_number, nval: 10},
				token{t: tok_semicolon},
				token{t: tok_ident, sval: "x"},
				token{t: tok_eq},
				token{t: tok_ident, sval: "x"},
				token{t: tok_plus},
				token{t: tok_number, nval: 1},
				token{t: tok_rparen},
				token{t: tok_lcurly},
				token{t: tok_ident, sval: "printf"},
				token{t: tok_lparen},
				token{t: tok_str, sval: "X is %v\n"},
				token{t: tok_comma},
				token{t: tok_ident, sval: "x"},
				token{t: tok_rparen},
				token{t: tok_rcurly},
			},
		},
		{
			in: "for (;;) { printf(\"X is %v\\n\", x)  }",
			out: []token{
				token{t: tok_for},
				token{t: tok_lparen},
				token{t: tok_semicolon},
				token{t: tok_semicolon},
				token{t: tok_rparen},
				token{t: tok_lcurly},
				token{t: tok_ident, sval: "printf"},
				token{t: tok_lparen},
				token{t: tok_str, sval: "X is %v\n"},
				token{t: tok_comma},
				token{t: tok_ident, sval: "x"},
				token{t: tok_rparen},
				token{t: tok_rcurly},
			},
		},
		{
			in:  "1.3.4.5.6",
			err: "at (character offset: 9, line: 1, position 9): parsing number: strconv.ParseFloat: parsing \"1.3.4.5.6\": invalid syntax",
		},
	} {
		t.Run("", func(t *testing.T) {
			p := NewLexer("", strings.NewReader(tt.in))
			for _, outn := range tt.out {
				n, err := p.Next()
				if tt.err != "" {
					assert.Equal(t, tt.err, err.Error())
					return
				}
				assert.NoError(t, err)
				assert.Equal(t, outn, n)
			}
		})
	}
}
