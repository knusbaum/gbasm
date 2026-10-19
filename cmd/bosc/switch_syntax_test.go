package main

import (
	"bufio"
	"strings"
	"testing"
)

func TestSwitchHeadSyntax(t *testing.T) {
	for _, tt := range []struct {
		source string
		kind   nodetype
		valid  bool
	}{
		{`switch (x) {case A {}}`, n_variantswitch, true},
		{`switch (x := f()) {case A {}}`, n_variantswitch, true},
		{`switch (var x := f()) {case A {}}`, n_variantswitch, true},
		{`switch (V.A{x: 1}) {case A {}}`, n_variantswitch, true},
		{`switch (v := x.(type)) {case i64 {}}`, n_typeswitch, true},
		{`switch x {case A {}}`, n_none, false},
		{`switch x := f() {case A {}}`, n_none, false},
		{`switch (v x.(type)) {case i64 {}}`, n_none, false},
		{`switch (x`, n_none, false},
	} {
		t.Run(tt.source, func(t *testing.T) {
			p := NewParser("switch.bos", bufio.NewReader(strings.NewReader(tt.source)))
			n, err := p.Next()
			if !tt.valid {
				if err == nil {
					t.Fatalf("expected syntax error, got %v", n)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if n.t != tt.kind {
				t.Fatalf("got %v, want %v", n.t, tt.kind)
			}
		})
	}
}
