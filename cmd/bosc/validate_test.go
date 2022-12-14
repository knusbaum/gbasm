package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidate(t *testing.T) {
	setup := `
		struct foo {
			x num
			s str
		}
		fn printi(i num) void {}
		fn print(s str) void {}
		fn foo (x num, y str, z str) num { if (10 > 12) return 10 else return 12}
		var y num
		y = foo( 1, "baz", "quux")
		var x num
		x = 10
		if ( x > 0 ) {
			print("HELLO\n")
		}
		var f foo
		printi(f.x)
		print(f.s)
		f.x = 10
	`
	p := NewParser("", strings.NewReader(setup))
	c := NewVContext()
	for {
		n, err := p.Next()
		if !assert.NoError(t, err) {
			return
		}
		if n == nil {
			break
		}
		//fmt.Printf("N: %v, ERR: %v\n", n, err)
		//spew.Dump(n)
		err = Validate(n, c)
		if err != nil {
			t.Error(err)
			return
		}
	}
	p = NewParser("", strings.NewReader(`y = 10`))
	n, err := p.Next()
	if !assert.NoError(t, err) {
		return
	}
	err = Validate(n, c)
	if err != nil {
		t.Error(err)
	}
}
