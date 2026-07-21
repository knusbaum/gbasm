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
			in: "b = 'h'\nputc(b)",
			out: []token{
				token{t: tok_ident, sval: "b"},
				token{t: tok_eq},
				token{t: tok_byte, nval: uint64('h')},
				token{t: tok_semicolon}, // auto-inserted after the byte literal
				token{t: tok_ident, sval: "putc"},
				token{t: tok_lparen},
				token{t: tok_ident, sval: "b"},
				token{t: tok_rparen},
			},
		},
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
			in: "1-1",
			out: []token{
				token{t: tok_number, nval: 1},
				token{t: tok_minus},
				token{t: tok_number, nval: 1},
			},
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
		{
			// Variadic spread / forwarding token: "..." lexes to a single
			// tok_ellipsis.
			in: "f(xs...)",
			out: []token{
				token{t: tok_ident, sval: "f"},
				token{t: tok_lparen},
				token{t: tok_ident, sval: "xs"},
				token{t: tok_ellipsis},
				token{t: tok_rparen},
			},
		},
		{
			// "..." after a type name parses as a single ellipsis.
			in: "i64...",
			out: []token{
				token{t: tok_ident, sval: "i64"},
				token{t: tok_ellipsis},
			},
		},
		{
			// New binary operators: %, ^, and the shifts (which must not be
			// mistaken for comparison tokens).
			in: "a % b ^ c << d >> e",
			out: []token{
				token{t: tok_ident, sval: "a"}, token{t: tok_percent},
				token{t: tok_ident, sval: "b"}, token{t: tok_caret},
				token{t: tok_ident, sval: "c"}, token{t: tok_shl},
				token{t: tok_ident, sval: "d"}, token{t: tok_shr},
				token{t: tok_ident, sval: "e"},
			},
		},
		{
			// Comparison vs shift disambiguation: <, <=, <<, >, >=, >>.
			in: "a < b <= c << d > e >= f >> g",
			out: []token{
				token{t: tok_ident, sval: "a"}, token{t: tok_lt},
				token{t: tok_ident, sval: "b"}, token{t: tok_le},
				token{t: tok_ident, sval: "c"}, token{t: tok_shl},
				token{t: tok_ident, sval: "d"}, token{t: tok_gt},
				token{t: tok_ident, sval: "e"}, token{t: tok_ge},
				token{t: tok_ident, sval: "f"}, token{t: tok_shr},
				token{t: tok_ident, sval: "g"},
			},
		},
		{
			// Unary bitwise not.
			in: "~a",
			out: []token{token{t: tok_tilde}, token{t: tok_ident, sval: "a"}},
		},
		{
			// All ten compound-assignment tokens.
			in: "a += b -= c *= d /= e %= f &= g |= h ^= i <<= j >>= k",
			out: []token{
				token{t: tok_ident, sval: "a"}, token{t: tok_pluseq},
				token{t: tok_ident, sval: "b"}, token{t: tok_minuseq},
				token{t: tok_ident, sval: "c"}, token{t: tok_stareq},
				token{t: tok_ident, sval: "d"}, token{t: tok_slasheq},
				token{t: tok_ident, sval: "e"}, token{t: tok_percenteq},
				token{t: tok_ident, sval: "f"}, token{t: tok_ampeq},
				token{t: tok_ident, sval: "g"}, token{t: tok_pipeeq},
				token{t: tok_ident, sval: "h"}, token{t: tok_careteq},
				token{t: tok_ident, sval: "i"}, token{t: tok_shleq},
				token{t: tok_ident, sval: "j"}, token{t: tok_shreq},
				token{t: tok_ident, sval: "k"},
			},
		},
		{
			// && vs & vs &=, and || vs | vs |= — each prefix must win the
			// longest match without swallowing the next token.
			in: "a && b & c &= d || e | f |= g",
			out: []token{
				token{t: tok_ident, sval: "a"}, token{t: tok_booland},
				token{t: tok_ident, sval: "b"}, token{t: tok_amp},
				token{t: tok_ident, sval: "c"}, token{t: tok_ampeq},
				token{t: tok_ident, sval: "d"}, token{t: tok_boolor},
				token{t: tok_ident, sval: "e"}, token{t: tok_pipe},
				token{t: tok_ident, sval: "f"}, token{t: tok_pipeeq},
				token{t: tok_ident, sval: "g"},
			},
		},
		{
			// `*?` lexes as two tokens (star, question) — prefix type syntax needs
			// `?*T` and `*?T` to differ. Also * vs *= and <<= vs <<.
			in: "*? * *= a <<= b << c",
			out: []token{
				token{t: tok_star}, token{t: tok_question}, token{t: tok_star}, token{t: tok_stareq},
				token{t: tok_ident, sval: "a"}, token{t: tok_shleq},
				token{t: tok_ident, sval: "b"}, token{t: tok_shl},
				token{t: tok_ident, sval: "c"},
			},
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

// TestLexerEllipsisError pins the directed error for a bare ".." (the lexer
// suggests the intended "..."). The bare ".." appears after a valid token, so
// it can't ride the TestLexer table (whose harness expects the error on the
// very first Next()).
func TestLexerEllipsisError(t *testing.T) {
	nopos = true
	defer func() { nopos = false }()
	l := NewLexer("", strings.NewReader("x.."))
	n, err := l.Next()
	assert.NoError(t, err)
	assert.Equal(t, token{t: tok_ident, sval: "x"}, n)
	_, err = l.Next()
	if assert.Error(t, err) {
		assert.True(t, strings.HasSuffix(err.Error(), "unexpected '..'; did you mean '...'?"),
			"got %q", err.Error())
	}
}

// TestLexerFloat covers the floating-point literal forms and the boundaries
// between a float literal and its neighbours: the selector dot, an integer
// that merely sits next to one, and the exponent marker.
func TestLexerFloat(t *testing.T) {
	nopos = true
	defer func() { nopos = false }()
	for _, tt := range []struct {
		in  string
		out []token
		err string
	}{
		// Fractional part.
		{in: "3.14", out: []token{token{t: tok_float, fval: 3.14}}},
		{in: "0.5", out: []token{token{t: tok_float, fval: 0.5}}},
		{in: "0.0", out: []token{token{t: tok_float, fval: 0}}},
		{in: "1.0", out: []token{token{t: tok_float, fval: 1}}},

		// Exponents, with and without a fractional part. `1e10` must not lex
		// as the integer 1 followed by the identifier `e10`.
		{in: "1e10", out: []token{token{t: tok_float, fval: 1e10}}},
		{in: "1E10", out: []token{token{t: tok_float, fval: 1e10}}},
		{in: "2.5e-3", out: []token{token{t: tok_float, fval: 2.5e-3}}},
		{in: "2.5E+3", out: []token{token{t: tok_float, fval: 2500}}},
		{in: "0e0", out: []token{token{t: tok_float, fval: 0}}},

		// Integers are unaffected, and still parsed as uint64 so that values
		// near UINT64_MAX keep every bit rather than rounding through a float.
		{in: "42", out: []token{token{t: tok_number, nval: 42}}},
		{in: "0", out: []token{token{t: tok_number, nval: 0}}},
		{
			in:  "18446744073709551615",
			out: []token{token{t: tok_number, nval: 18446744073709551615}},
		},
		{in: "0xFF", out: []token{token{t: tok_number, nval: 255}}},
		{in: "0b101", out: []token{token{t: tok_number, nval: 5}}},
		{in: "0o17", out: []token{token{t: tok_number, nval: 15}}},

		// A selector following something that is not a numeric literal still
		// lexes as a dot. This is the case that made a float literal
		// unambiguous without two runes of lookahead.
		{
			in: "x.y",
			out: []token{
				token{t: tok_ident, sval: "x"},
				token{t: tok_dot},
				token{t: tok_ident, sval: "y"},
			},
		},
		{
			in: "ps[0].x",
			out: []token{
				token{t: tok_ident, sval: "ps"},
				token{t: tok_lsquare},
				token{t: tok_number, nval: 0},
				token{t: tok_rsquare},
				token{t: tok_dot},
				token{t: tok_ident, sval: "x"},
			},
		},

		// Arithmetic around floats: the '-' is an operator, not a sign, just
		// as it is for integers.
		{
			in: "1.5-0.5",
			out: []token{
				token{t: tok_float, fval: 1.5},
				token{t: tok_minus},
				token{t: tok_float, fval: 0.5},
			},
		},

		// Malformed literals are rejected rather than silently truncated.
		{in: "1.", err: "no digits after the decimal point"},
		{in: "1.foo", err: "no digits after the decimal point"},
		{in: "1e", err: "no digits in its exponent"},
		{in: "1e+", err: "no digits in its exponent"},
	} {
		t.Run(tt.in, func(t *testing.T) {
			l := NewLexer("", strings.NewReader(tt.in))
			var toks []token
			var err error
			for {
				var tok token
				tok, err = l.Next()
				if err != nil || tok.t == tok_none {
					break
				}
				toks = append(toks, tok)
			}
			if tt.err != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got tokens %v", tt.err, toks)
				}
				assert.Contains(t, err.Error(), tt.err)
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			assert.Equal(t, tt.out, toks)
		})
	}
}
