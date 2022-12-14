package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCompile(t *testing.T) {
	for _, tt := range []struct {
		in string
		//out *Node
	}{
		// 		{
		// 			in: "fn foo (x num, y num) num { printf(\"X is %v\\nY is %v\\n\", x, y) return 0  }",
		// 		},
		// 		{
		// 			in: "if (b) { return 1 } else { return 2 } }",
		// 		},
		{
			in: "fn bosonfib(n num) num { if (n < 3) return 1 else return bosonfib(n - 1) + bosonfib(n - 2) }",
		},
		{
			in: "fn bosonfib(n num) { if (n < 3) 1 else {bosonfib(n - 1) bosonfib(n - 2)} }",
		},
	} {
		t.Run("", func(t *testing.T) {
			p := NewParser("", strings.NewReader(tt.in))
			n, err := p.Next()
			if !assert.NoError(t, err) {
				return
			}
			ctx := NewCompileContext()
			n.replaceStrings(ctx)
			var out bytes.Buffer
			n.compile(ctx, &out, "")
		})
	}
}
