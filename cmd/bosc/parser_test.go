package main

import (
	"strings"
	"testing"

	"github.com/davecgh/go-spew/spew"
	"github.com/stretchr/testify/assert"
)

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
			in:  "foo()",
			out: &Node{t: n_funcall, sval: "foo"},
		},
		{
			in: "foo(1, 2, 3)",
			out: &Node{t: n_funcall, sval: "foo", args: []*Node{
				&Node{t: n_number, nval: 1},
				&Node{t: n_number, nval: 2},
				&Node{t: n_number, nval: 3},
			}},
		},
		{
			in: "1 + 2 * (3 + 4) / 5",
			out: &Node{t: n_add, args: []*Node{
				&Node{t: n_number, nval: 1},
				&Node{t: n_div, args: []*Node{
					&Node{t: n_mul, args: []*Node{
						&Node{t: n_number, nval: 2},
						&Node{t: n_add, args: []*Node{
							&Node{t: n_number, nval: 3},
							&Node{t: n_number, nval: 4},
						}},
					}},
					&Node{t: n_number, nval: 5},
				}},
			}},
		},
		{
			in: "1 + 2 * (bar(\"a\", \"b\", \"c\") + 4) / 5",
			out: &Node{t: n_add, args: []*Node{
				&Node{t: n_number, nval: 1},
				&Node{t: n_div, args: []*Node{
					&Node{t: n_mul, args: []*Node{
						&Node{t: n_number, nval: 2},
						&Node{t: n_add, args: []*Node{
							&Node{t: n_funcall, sval: "bar", args: []*Node{
								&Node{t: n_str, sval: "a"},
								&Node{t: n_str, sval: "b"},
								&Node{t: n_str, sval: "c"},
							}},
							&Node{t: n_number, nval: 4},
						}},
					}},
					&Node{t: n_number, nval: 5},
				}},
			}},
		},
		{
			in: "1 + 2 + 3 + 4",
			out: &Node{t: n_add, args: []*Node{
				&Node{t: n_add, args: []*Node{
					&Node{t: n_add, args: []*Node{
						&Node{t: n_number, nval: 1},
						&Node{t: n_number, nval: 2},
					}},
					&Node{t: n_number, nval: 3},
				}},
				&Node{t: n_number, nval: 4},
			}},
		},
		{
			in: "1 * 2 * 3 * 4",
			out: &Node{t: n_mul, args: []*Node{
				&Node{t: n_mul, args: []*Node{
					&Node{t: n_mul, args: []*Node{
						&Node{t: n_number, nval: 1},
						&Node{t: n_number, nval: 2},
					}},
					&Node{t: n_number, nval: 3},
				}},
				&Node{t: n_number, nval: 4},
			}},
		},
		{
			// Ensure we parse only one expression.
			in: "1 * 2 \n 3 * 4 \n 5 * 6",
			out: &Node{t: n_mul, args: []*Node{
				&Node{t: n_number, nval: 1},
				&Node{t: n_number, nval: 2},
			}},
		},
		{
			in: "foo{ field: \"somestring\", field2: 1234.5, field3: bar(), field4: baz(1, 2, 3), field5: quux }",
			out: &Node{t: n_stlit, sval: "foo", args: []*Node{
				&Node{t: n_stfield, sval: "field", args: []*Node{&Node{t: n_str, sval: "somestring"}}},
				&Node{t: n_stfield, sval: "field2", args: []*Node{&Node{t: n_number, nval: 1234.5}}},
				&Node{t: n_stfield, sval: "field3", args: []*Node{&Node{t: n_funcall, sval: "bar"}}},
				&Node{t: n_stfield, sval: "field4", args: []*Node{&Node{t: n_funcall, sval: "baz", args: []*Node{
					&Node{t: n_number, nval: 1},
					&Node{t: n_number, nval: 2},
					&Node{t: n_number, nval: 3},
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
					&Node{t: n_number, nval: 12},
				}},
				// Then
				&Node{t: n_block, args: []*Node{
					&Node{t: n_eq, args: []*Node{
						&Node{t: n_symbol, sval: "x"},
						&Node{t: n_sub, args: []*Node{
							&Node{t: n_symbol, sval: "x"},
							&Node{t: n_number, nval: 1},
						}},
					}},
				}},
				// Else
				&Node{t: n_if, args: []*Node{
					// Condition
					&Node{t: n_lt, args: []*Node{
						&Node{t: n_symbol, sval: "x"},
						&Node{t: n_number, nval: 0},
					}},
					// Then
					&Node{t: n_block, args: []*Node{
						&Node{t: n_eq, args: []*Node{
							&Node{t: n_symbol, sval: "x"},
							&Node{t: n_number, nval: 0},
						}},
					}},
					// Else
					&Node{t: n_block, args: []*Node{
						&Node{t: n_eq, args: []*Node{
							&Node{t: n_symbol, sval: "x"},
							&Node{t: n_number, nval: 100},
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
					&Node{t: n_number, nval: 1},
				}},
				&Node{t: n_lt, args: []*Node{
					&Node{t: n_symbol, sval: "x"},
					&Node{t: n_number, nval: 10},
				}},
				&Node{t: n_eq, args: []*Node{
					&Node{t: n_symbol, sval: "x"},
					&Node{t: n_add, args: []*Node{
						&Node{t: n_symbol, sval: "x"},
						&Node{t: n_number, nval: 1},
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
						&Node{t: n_number, nval: 1},
					}},
				}},
				&Node{t: n_if, args: []*Node{
					// Condition
					&Node{t: n_ge, args: []*Node{
						&Node{t: n_symbol, sval: "x"},
						&Node{t: n_number, nval: 10},
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
							&Node{t: n_number, nval: 1},
						}},
					}},
					&Node{t: n_if, args: []*Node{
						// Condition
						&Node{t: n_ge, args: []*Node{
							&Node{t: n_symbol, sval: "x"},
							&Node{t: n_number, nval: 10},
						}},
						// Then
						&Node{t: n_break},
					}},
				}},
			}},
		},
		{
			in: "fn foo (x num, y num) num { printf(\"X is %v\\nY is %v\\n\", x, y) return 0  }",
			out: &Node{t: n_fn, nval: 2, sval: "foo", args: []*Node{
				&Node{t: n_arg, sval: "x", args: []*Node{&Node{t: n_symbol, sval: "num"}}},
				&Node{t: n_arg, sval: "y", args: []*Node{&Node{t: n_symbol, sval: "num"}}},
				&Node{t: n_symbol, sval: "num"},
				&Node{t: n_block, args: []*Node{
					&Node{t: n_funcall, sval: "printf", args: []*Node{
						&Node{t: n_str, sval: "X is %v\nY is %v\n"},
						&Node{t: n_symbol, sval: "x"},
						&Node{t: n_symbol, sval: "y"},
					}},
					&Node{t: n_return, args: []*Node{&Node{t: n_number, nval: 0}}},
				}},
			}},
		},
		{
			in: "printxy = fn bar (x num, y num) num { printf(\"X is %v\\nY is %v\\n\", x, y); return 0  }",
			out: &Node{t: n_eq, args: []*Node{
				&Node{t: n_symbol, sval: "printxy"},
				&Node{t: n_fn, nval: 2, sval: "bar", args: []*Node{
					&Node{t: n_arg, sval: "x", args: []*Node{&Node{t: n_symbol, sval: "num"}}},
					&Node{t: n_arg, sval: "y", args: []*Node{&Node{t: n_symbol, sval: "num"}}},
					&Node{t: n_symbol, sval: "num"},
					&Node{t: n_block, args: []*Node{
						&Node{t: n_funcall, sval: "printf", args: []*Node{
							&Node{t: n_str, sval: "X is %v\nY is %v\n"},
							&Node{t: n_symbol, sval: "x"},
							&Node{t: n_symbol, sval: "y"},
						}},
						&Node{},
						&Node{t: n_return, args: []*Node{&Node{t: n_number, nval: 0}}},
					}},
				}},
			}},
		},
		{
			in: "struct rect { x num y num w num h num in *screen}",
			out: &Node{t: n_struct, sval: "rect", args: []*Node{
				&Node{t: n_stfield, sval: "x", args: []*Node{&Node{t: n_typename, sval: "num"}}},
				&Node{t: n_stfield, sval: "y", args: []*Node{&Node{t: n_typename, sval: "num"}}},
				&Node{t: n_stfield, sval: "w", args: []*Node{&Node{t: n_typename, sval: "num"}}},
				&Node{t: n_stfield, sval: "h", args: []*Node{&Node{t: n_typename, sval: "num"}}},
				&Node{t: n_stfield, sval: "in", args: []*Node{&Node{t: n_typename, sval: "screen", nval: 1}}},
			}},
		},
		{
			in: "x = y = 10",
			out: &Node{t: n_eq, args: []*Node{
				&Node{t: n_symbol, sval: "x"},
				&Node{t: n_eq, args: []*Node{
					&Node{t: n_symbol, sval: "y"},
					&Node{t: n_number, nval: 10},
				}},
			}},
		},
		{
			in:  "var x int",
			out: &Node{t: n_var, sval: "x", args: []*Node{&Node{t: n_typename, sval: "int"}}},
		},
		{
			in:  "var x *int",
			out: &Node{t: n_var, sval: "x", args: []*Node{&Node{t: n_typename, sval: "int", nval: 1}}},
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
