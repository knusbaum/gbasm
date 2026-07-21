package main

import (
	"strings"
	"testing"

	"github.com/davecgh/go-spew/spew"
	"github.com/stretchr/testify/assert"
)

// tn builds the <prebuilt> n_typename node that parseTypeName now returns for a
// parsed type. (Types are assembled into an ASTType at parse time and carried in
// the node's prebuilt field, rather than described by sval/ival/args.) Tests run
// with nopos=true, so the ASTType's zero position matches.
func tn(at ASTType) *Node {
	return &Node{t: n_typename, sval: "<prebuilt>", prebuilt: &at}
}

func TestParser(t *testing.T) {
	nopos = true
	defer func() { nopos = false }()
	for _, tt := range []struct {
		in  string
		out *Node
	}{
		{
			in:  `import "/some/path"`,
			out: &Node{t: n_import, sval: "/some/path"},
		},
		{
			in:  "foo",
			out: &Node{t: n_symbol, sval: "foo"},
		},
		{
			in: "x = 1",
			out: &Node{t: n_eq, args: []*Node{
				&Node{t: n_symbol, sval: "x"},
				&Node{t: n_number, ival: 1},
			}},
		},
		{
			in: "x = y",
			out: &Node{t: n_eq, args: []*Node{
				&Node{t: n_symbol, sval: "x"},
				&Node{t: n_symbol, sval: "y"},
			}},
		},
		{
			in:  "foo()",
			out: &Node{t: n_funcall, sval: "foo"},
		},
		{
			in: "foo(1, 2, 3)",
			out: &Node{t: n_funcall, sval: "foo", args: []*Node{
				&Node{t: n_number, ival: 1},
				&Node{t: n_number, ival: 2},
				&Node{t: n_number, ival: 3},
			}},
		},
		{
			in: "1 + 2 * (3 + 4) / 5",
			out: &Node{t: n_add, args: []*Node{
				&Node{t: n_number, ival: 1},
				&Node{t: n_div, args: []*Node{
					&Node{t: n_mul, args: []*Node{
						&Node{t: n_number, ival: 2},
						&Node{t: n_add, args: []*Node{
							&Node{t: n_number, ival: 3},
							&Node{t: n_number, ival: 4},
						}},
					}},
					&Node{t: n_number, ival: 5},
				}},
			}},
		},
		{
			in: "1 + 2 * (bar(\"a\", \"b\", \"c\") + 4) / 5",
			out: &Node{t: n_add, args: []*Node{
				&Node{t: n_number, ival: 1},
				&Node{t: n_div, args: []*Node{
					&Node{t: n_mul, args: []*Node{
						&Node{t: n_number, ival: 2},
						&Node{t: n_add, args: []*Node{
							&Node{t: n_funcall, sval: "bar", args: []*Node{
								&Node{t: n_str, sval: "a"},
								&Node{t: n_str, sval: "b"},
								&Node{t: n_str, sval: "c"},
							}},
							&Node{t: n_number, ival: 4},
						}},
					}},
					&Node{t: n_number, ival: 5},
				}},
			}},
		},
		{
			in: "1 + 2 + 3 + 4",
			out: &Node{t: n_add, args: []*Node{
				&Node{t: n_add, args: []*Node{
					&Node{t: n_add, args: []*Node{
						&Node{t: n_number, ival: 1},
						&Node{t: n_number, ival: 2},
					}},
					&Node{t: n_number, ival: 3},
				}},
				&Node{t: n_number, ival: 4},
			}},
		},
		{
			in: "1 * 2 * 3 * 4",
			out: &Node{t: n_mul, args: []*Node{
				&Node{t: n_mul, args: []*Node{
					&Node{t: n_mul, args: []*Node{
						&Node{t: n_number, ival: 1},
						&Node{t: n_number, ival: 2},
					}},
					&Node{t: n_number, ival: 3},
				}},
				&Node{t: n_number, ival: 4},
			}},
		},
		{
			// Ensure we parse only one expression.
			in: "1 * 2 \n 3 * 4 \n 5 * 6",
			out: &Node{t: n_mul, args: []*Node{
				&Node{t: n_number, ival: 1},
				&Node{t: n_number, ival: 2},
			}},
		},
		{
			in: "foo{ field: \"somestring\", field2: 1234.5, field3: bar(), field4: baz(1, 2, 3), field5: quux }",
			out: &Node{t: n_stlit, sval: "foo", typeIdent: &Node{t: n_symbol, sval: "foo"}, args: []*Node{
				&Node{t: n_stfield, sval: "field", args: []*Node{&Node{t: n_str, sval: "somestring"}}},
				&Node{t: n_stfield, sval: "field2", args: []*Node{&Node{t: n_float, fval: 1234.5}}},
				&Node{t: n_stfield, sval: "field3", args: []*Node{&Node{t: n_funcall, sval: "bar"}}},
				&Node{t: n_stfield, sval: "field4", args: []*Node{&Node{t: n_funcall, sval: "baz", args: []*Node{
					&Node{t: n_number, ival: 1},
					&Node{t: n_number, ival: 2},
					&Node{t: n_number, ival: 3},
				}}}},
				&Node{t: n_stfield, sval: "field5", args: []*Node{&Node{t: n_symbol, sval: "quux"}}},
			}},
		},
		{
			in: "if (x == 12) { x = x - 1 } else if (x < 0) { x = 0 } else { x = 100 }",
			out: &Node{t: n_if, args: []*Node{
				// Condition
				&Node{t: n_deq, args: []*Node{
					&Node{t: n_symbol, sval: "x"},
					&Node{t: n_number, ival: 12},
				}},
				// Then
				&Node{t: n_block, args: []*Node{
					&Node{t: n_eq, args: []*Node{
						&Node{t: n_symbol, sval: "x"},
						&Node{t: n_sub, args: []*Node{
							&Node{t: n_symbol, sval: "x"},
							&Node{t: n_number, ival: 1},
						}},
					}},
				}},
				// Else
				&Node{t: n_if, args: []*Node{
					// Condition
					&Node{t: n_lt, args: []*Node{
						&Node{t: n_symbol, sval: "x"},
						&Node{t: n_number, ival: 0},
					}},
					// Then
					&Node{t: n_block, args: []*Node{
						&Node{t: n_eq, args: []*Node{
							&Node{t: n_symbol, sval: "x"},
							&Node{t: n_number, ival: 0},
						}},
					}},
					// Else
					&Node{t: n_block, args: []*Node{
						&Node{t: n_eq, args: []*Node{
							&Node{t: n_symbol, sval: "x"},
							&Node{t: n_number, ival: 100},
						}},
					}},
				}},
			}},
		},
		{
			in: "for (x = 1; x < 10; x = x + 1) { printf(\"X is %v\\n\", x) }",
			out: &Node{t: n_for, args: []*Node{
				&Node{t: n_eq, args: []*Node{
					&Node{t: n_symbol, sval: "x"},
					&Node{t: n_number, ival: 1},
				}},
				&Node{t: n_lt, args: []*Node{
					&Node{t: n_symbol, sval: "x"},
					&Node{t: n_number, ival: 10},
				}},
				&Node{t: n_eq, args: []*Node{
					&Node{t: n_symbol, sval: "x"},
					&Node{t: n_add, args: []*Node{
						&Node{t: n_symbol, sval: "x"},
						&Node{t: n_number, ival: 1},
					}},
				}},
				&Node{t: n_block, args: []*Node{
					&Node{t: n_funcall, sval: "printf", args: []*Node{
						&Node{t: n_str, sval: "X is %v\n"},
						&Node{t: n_symbol, sval: "x"},
					}},
				}},
			}},
		},
		{
			in: "for (; ; ) { printf(\"X is %v\\n\", x) }",
			out: &Node{t: n_for, args: []*Node{
				&Node{},
				&Node{},
				&Node{},
				&Node{t: n_block, args: []*Node{
					&Node{t: n_funcall, sval: "printf", args: []*Node{
						&Node{t: n_str, sval: "X is %v\n"},
						&Node{t: n_symbol, sval: "x"},
					}},
				}},
			}},
		},
		{
			in: "{ printf(\"X is %v\\n\", x) x = x + 1 if (x >= 10) break }",
			out: &Node{t: n_block, args: []*Node{
				&Node{t: n_funcall, sval: "printf", args: []*Node{
					&Node{t: n_str, sval: "X is %v\n"},
					&Node{t: n_symbol, sval: "x"},
				}},
				&Node{t: n_eq, args: []*Node{
					&Node{t: n_symbol, sval: "x"},
					&Node{t: n_add, args: []*Node{
						&Node{t: n_symbol, sval: "x"},
						&Node{t: n_number, ival: 1},
					}},
				}},
				&Node{t: n_if, args: []*Node{
					// Condition
					&Node{t: n_ge, args: []*Node{
						&Node{t: n_symbol, sval: "x"},
						&Node{t: n_number, ival: 10},
					}},
					// Then
					&Node{t: n_break},
				}},
			}},
		},
		{
			in: "for (;;) { printf(\"X is %v\\n\", x) x = x + 1 if (x >= 10) break }",
			out: &Node{t: n_for, args: []*Node{
				&Node{},
				&Node{},
				&Node{},
				&Node{t: n_block, args: []*Node{
					&Node{t: n_funcall, sval: "printf", args: []*Node{
						&Node{t: n_str, sval: "X is %v\n"},
						&Node{t: n_symbol, sval: "x"},
					}},
					&Node{t: n_eq, args: []*Node{
						&Node{t: n_symbol, sval: "x"},
						&Node{t: n_add, args: []*Node{
							&Node{t: n_symbol, sval: "x"},
							&Node{t: n_number, ival: 1},
						}},
					}},
					&Node{t: n_if, args: []*Node{
						// Condition
						&Node{t: n_ge, args: []*Node{
							&Node{t: n_symbol, sval: "x"},
							&Node{t: n_number, ival: 10},
						}},
						// Then
						&Node{t: n_break},
					}},
				}},
			}},
		},
		{
			in: "fn foo () { }",
			out: &Node{t: n_fn, ival: 0, sval: "foo", args: []*Node{
				&Node{t: n_typename, sval: "void"},
				&Node{t: n_block, args: nil},
			}},
		},
		{
			in: "fn foo (x num, y *num, z **num) ***num { return 0  }",
			out: &Node{t: n_fn, ival: 3, sval: "foo", args: []*Node{
				&Node{t: n_arg, sval: "x", args: []*Node{tn(ASTType{Name: "num"})}},
				&Node{t: n_arg, sval: "y", args: []*Node{tn(ASTType{Name: "num", Indirection: 1})}},
				&Node{t: n_arg, sval: "z", args: []*Node{tn(ASTType{Name: "num", Indirection: 2})}},
				tn(ASTType{Name: "num", Indirection: 3}),
				&Node{t: n_block, args: []*Node{
					&Node{t: n_return, args: []*Node{&Node{t: n_number, ival: 0}}},
				}},
			}},
		},
		{
			in: "fn foo (x num, y num) num { printf(\"X is %v\\nY is %v\\n\", x, y) return 0  }",
			out: &Node{t: n_fn, ival: 2, sval: "foo", args: []*Node{
				&Node{t: n_arg, sval: "x", args: []*Node{tn(ASTType{Name: "num"})}},
				&Node{t: n_arg, sval: "y", args: []*Node{tn(ASTType{Name: "num"})}},
				tn(ASTType{Name: "num"}),
				&Node{t: n_block, args: []*Node{
					&Node{t: n_funcall, sval: "printf", args: []*Node{
						&Node{t: n_str, sval: "X is %v\nY is %v\n"},
						&Node{t: n_symbol, sval: "x"},
						&Node{t: n_symbol, sval: "y"},
					}},
					&Node{t: n_return, args: []*Node{&Node{t: n_number, ival: 0}}},
				}},
			}},
		},
		{
			in: "printxy = fn bar (x num, y num) num { printf(\"X is %v\\nY is %v\\n\", x, y) return 0  }",
			out: &Node{t: n_eq, args: []*Node{
				&Node{t: n_symbol, sval: "printxy"},
				&Node{t: n_fn, ival: 2, sval: "bar", args: []*Node{
					&Node{t: n_arg, sval: "x", args: []*Node{tn(ASTType{Name: "num"})}},
					&Node{t: n_arg, sval: "y", args: []*Node{tn(ASTType{Name: "num"})}},
					tn(ASTType{Name: "num"}),
					&Node{t: n_block, args: []*Node{
						&Node{t: n_funcall, sval: "printf", args: []*Node{
							&Node{t: n_str, sval: "X is %v\nY is %v\n"},
							&Node{t: n_symbol, sval: "x"},
							&Node{t: n_symbol, sval: "y"},
						}},
						&Node{t: n_return, args: []*Node{&Node{t: n_number, ival: 0}}},
					}},
				}},
			}},
		},
		{
			in: "type rect struct { x num y num w num h num in *screen}",
			out: &Node{t: n_typedecl, sval: "rect", args: []*Node{
				tn(ASTType{AnonFields: []Binding{
					{Name: "x", Type: ASTType{Name: "num"}},
					{Name: "y", Type: ASTType{Name: "num"}},
					{Name: "w", Type: ASTType{Name: "num"}},
					{Name: "h", Type: ASTType{Name: "num"}},
					{Name: "in", Type: ASTType{Name: "screen", Indirection: 1}},
				}}),
			}},
		},
		{
			in: "x = y = 10",
			out: &Node{t: n_eq, args: []*Node{
				&Node{t: n_symbol, sval: "x"},
				&Node{t: n_eq, args: []*Node{
					&Node{t: n_symbol, sval: "y"},
					&Node{t: n_number, ival: 10},
				}},
			}},
		},
		{
			in:  "var x int",
			out: &Node{t: n_var, sval: "x", args: []*Node{tn(ASTType{Name: "int"})}},
		},
		{
			in:  "var x *int",
			out: &Node{t: n_var, sval: "x", args: []*Node{tn(ASTType{Name: "int", Indirection: 1})}},
		},
		{
			in: "var x [10]int",
			out: &Node{t: n_var, sval: "x", args: []*Node{
				tn(ASTType{ArraySize: 10, Element: &ASTType{Name: "int"}}),
			}},
		},
		{
			// Prefix `[10]*int` is unambiguously an array of 10 pointers — the
			// old postfix `*int[10]` meant the same but read like a pointer.
			in: "var x [10]*int",
			out: &Node{t: n_var, sval: "x", args: []*Node{
				tn(ASTType{ArraySize: 10, Element: &ASTType{Name: "int", Indirection: 1}}),
			}},
		},
		{
			in: "a.b",
			out: &Node{t: n_dot, args: []*Node{
				&Node{t: n_symbol, sval: "a"},
				&Node{t: n_symbol, sval: "b"},
			}},
		},
		{
			in: "a.b.c.d",
			out: &Node{t: n_dot, args: []*Node{
				&Node{t: n_dot, args: []*Node{
					&Node{t: n_dot, args: []*Node{
						&Node{t: n_symbol, sval: "a"},
						&Node{t: n_symbol, sval: "b"},
					}},
					&Node{t: n_symbol, sval: "c"},
				}},
				&Node{t: n_symbol, sval: "d"},
			}},
		},
		{
			in: "*a",
			out: &Node{t: n_deref, args: []*Node{
				&Node{t: n_symbol, sval: "a"},
			}},
		},
		{
			in: "*x.y",
			out: &Node{t: n_deref, args: []*Node{
				&Node{t: n_dot, args: []*Node{
					&Node{t: n_symbol, sval: "x"},
					&Node{t: n_symbol, sval: "y"},
				}},
			}},
		},
		{
			in:  "&x",
			out: &Node{t: n_address, sval: "x"},
		},
		{
			in: "fn main() {\nvar b byte\nb = 'h'\nputc(b)\n}\n",
			out: &Node{t: n_fn, ival: 0, sval: "main", args: []*Node{
				&Node{t: n_typename, sval: "void"},
				&Node{t: n_block, args: []*Node{
					&Node{t: n_var, sval: "b", args: []*Node{tn(ASTType{Name: "byte"})}},
					&Node{t: n_eq, args: []*Node{
						&Node{t: n_symbol, sval: "b"},
						&Node{t: n_byte, ival: uint64('h')},
					}},
					&Node{t: n_funcall, sval: "putc", args: []*Node{
						&Node{t: n_symbol, sval: "b"},
					}},
				}},
			},
			},
		},
		{
			in: "a[10]",
			out: &Node{t: n_index, args: []*Node{
				&Node{t: n_symbol, sval: "a"},
				&Node{t: n_number, ival: 10},
			}},
		},
		{
			in: "a[x]",
			out: &Node{t: n_index, args: []*Node{
				&Node{t: n_symbol, sval: "a"},
				&Node{t: n_symbol, sval: "x"},
			}},
		},
		{
			in: "x = -x",
			out: &Node{t: n_eq, args: []*Node{
				&Node{t: n_symbol, sval: "x"},
				&Node{t: n_neg, args: []*Node{
					&Node{t: n_symbol, sval: "x"},
				}},
			}},
		},
		{
			// Type-assertion postfix: `x.(T)` -> n_typeassert{[symbol, type]}.
			in: "x.(i64)",
			out: &Node{t: n_typeassert, args: []*Node{
				&Node{t: n_symbol, sval: "x"},
				tn(ASTType{Name: "i64", Signed: true}),
			}},
		},
		{
			// Pointer-target assertion: `x.(*T)` carries the indirection on the
			// typename (ival=1).
			in: "x.(*foo)",
			out: &Node{t: n_typeassert, args: []*Node{
				&Node{t: n_symbol, sval: "x"},
				tn(ASTType{Name: "foo", Indirection: 1}),
			}},
		},
		{
			// Variadic last parameter `a ...i64`: the n_arg carries ival bit 1
			// (variadic) and its element type is wrapped in a slice (n_slice).
			in: "fn f(a ...i64) { }",
			out: &Node{t: n_fn, ival: 1, sval: "f", args: []*Node{
				&Node{t: n_arg, sval: "a", ival: 2, args: []*Node{
					tn(ASTType{Element: &ASTType{Name: "i64", Signed: true}}),
				}},
				&Node{t: n_typename, sval: "void"},
				&Node{t: n_block, args: nil},
			}},
		},
		{
			// Trailing `slice...` forwarding argument wraps the spread value in
			// an n_variadic_forward node.
			in: "f(x, xs...)",
			out: &Node{t: n_funcall, sval: "f", args: []*Node{
				&Node{t: n_symbol, sval: "x"},
				&Node{t: n_variadic_forward, args: []*Node{
					&Node{t: n_symbol, sval: "xs"},
				}},
			}},
		},
		{
			// A block body containing the comma-LHS multi-value re-assignment
			// `v, ok = x.(T)`. parseStatement (used by the block loop) turns the
			// comma-LHS into an n_multibind with ival==1 (the re-assignment
			// marker), whose args are the target lvalues followed by the RHS.
			in: "{ v, ok = x.(i64) }",
			out: &Node{t: n_block, args: []*Node{
				&Node{t: n_multibind, ival: 1, args: []*Node{
					&Node{t: n_symbol, sval: "v"},
					&Node{t: n_symbol, sval: "ok"},
					&Node{t: n_typeassert, args: []*Node{
						&Node{t: n_symbol, sval: "x"},
						tn(ASTType{Name: "i64", Signed: true}),
					}},
				}},
			}},
		},
		{
			// % shares the multiplicative level with * / and is left-assoc:
			// a % b * c == (a % b) * c.
			in: "a % b * c",
			out: &Node{t: n_mul, args: []*Node{
				&Node{t: n_mod, args: []*Node{
					&Node{t: n_symbol, sval: "a"},
					&Node{t: n_symbol, sval: "b"},
				}},
				&Node{t: n_symbol, sval: "c"},
			}},
		},
		{
			// + binds tighter than <<: 1 + 2 << 3 == (1 + 2) << 3.
			in: "1 + 2 << 3",
			out: &Node{t: n_shl, args: []*Node{
				&Node{t: n_add, args: []*Node{
					&Node{t: n_number, ival: 1},
					&Node{t: n_number, ival: 2},
				}},
				&Node{t: n_number, ival: 3},
			}},
		},
		{
			// Bitwise precedence & tighter than ^ tighter than |:
			// 1 | 2 ^ 3 & 4 == 1 | (2 ^ (3 & 4)).
			in: "1 | 2 ^ 3 & 4",
			out: &Node{t: n_bitor, args: []*Node{
				&Node{t: n_number, ival: 1},
				&Node{t: n_xor, args: []*Node{
					&Node{t: n_number, ival: 2},
					&Node{t: n_bitand, args: []*Node{
						&Node{t: n_number, ival: 3},
						&Node{t: n_number, ival: 4},
					}},
				}},
			}},
		},
		{
			// && binds tighter than ||: a && b || c && d ==
			// (a && b) || (c && d).
			in: "a && b || c && d",
			out: &Node{t: n_boolor, args: []*Node{
				&Node{t: n_booland, args: []*Node{
					&Node{t: n_symbol, sval: "a"},
					&Node{t: n_symbol, sval: "b"},
				}},
				&Node{t: n_booland, args: []*Node{
					&Node{t: n_symbol, sval: "c"},
					&Node{t: n_symbol, sval: "d"},
				}},
			}},
		},
		{
			// Comparison binds tighter than & (C-style): a == b & c parses as
			// (a == b) & c. (This is the footgun that makes `flag & MASK == 0`
			// a type error in Boson — recorded deliberately.)
			in: "a == b & c",
			out: &Node{t: n_bitand, args: []*Node{
				&Node{t: n_deq, args: []*Node{
					&Node{t: n_symbol, sval: "a"},
					&Node{t: n_symbol, sval: "b"},
				}},
				&Node{t: n_symbol, sval: "c"},
			}},
		},
		{
			// Unary bitwise not.
			in: "~a",
			out: &Node{t: n_bitnot, args: []*Node{
				&Node{t: n_symbol, sval: "a"},
			}},
		},
		{
			// Compound assignment desugars to `lhs = lhs op rhs`.
			in: "x += 1",
			out: &Node{t: n_eq, args: []*Node{
				&Node{t: n_symbol, sval: "x"},
				&Node{t: n_add, args: []*Node{
					&Node{t: n_symbol, sval: "x"},
					&Node{t: n_number, ival: 1},
				}},
			}},
		},
	} {
		t.Run("", func(t *testing.T) {
			p := NewParser("", strings.NewReader(tt.in))
			n, err := p.Next()
			if !assert.NoError(t, err) {
				return
			}
			if !assert.Equal(t, tt.out, n) {
				spew.Dump(n)
			}
		})
	}
}

// TestParsePrefixReferenceTypes exercises the pointer/slice ordering that
// prefix syntax makes unambiguous — the whole reason for the migration. In
// postfix, `*byte[]` (slice of byte-pointers) vs pointer-to-slice needed a
// paren group `*(byte[])`; in prefix the two are just `[]*byte` and `*[]byte`.
func TestParsePrefixReferenceTypes(t *testing.T) {
	nopos = true
	defer func() { nopos = false }()
	for _, tt := range []struct {
		in   string
		want ASTType
	}{
		{
			// pointer to a byte slice.
			in: "*[]byte",
			want: ASTType{
				Indirection: 1,
				Element:     &ASTType{Name: "byte"},
			},
		},
		{
			// write-through (mut) pointer to a byte slice: the mut bit lands
			// on the outer pointer level (bit 1), not on the slice element.
			in: "*mut []byte",
			want: ASTType{
				Indirection: 1,
				MutMask:     1 << 1,
				Element:     &ASTType{Name: "byte"},
			},
		},
		{
			// Contrast: a slice of byte-pointers. `[]*byte` reads outer-to-inner,
			// so the `*` binds to the element (Element.Indirection==1).
			in: "[]*byte",
			want: ASTType{
				Element: &ASTType{Name: "byte", Indirection: 1},
			},
		},
	} {
		t.Run(tt.in, func(t *testing.T) {
			p := NewParser("", strings.NewReader(tt.in))
			got := mkTypename(p.parseTypeName())
			assert.Equal(t, tt.want, got)
		})
	}
}
