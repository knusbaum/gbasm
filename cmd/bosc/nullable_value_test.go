package main

import "testing"

func TestNullableValueLayoutAndIndependentLayers(t *testing.T) {
	c := NewContext()
	for _, tc := range []struct {
		src         string
		size, align int
		tagged      bool
	}{
		{"i64", 8, 8, false}, {"?i64", 16, 8, true}, {"?u8", 16, 8, true}, {"?f64", 16, 8, true},
		{"?struct{x i64,y byte}", 24, 8, true}, {"?struct{}", 8, 8, true},
		{"?*?i64", 8, 8, false}, {"[2]?i64", 32, 8, false}, {"[]?i64", 24, 8, false},
	} {
		t.Run(tc.src, func(t *testing.T) {
			typ, err := parseTypeString(tc.src)
			if err != nil {
				t.Fatal(err)
			}
			if got := typ.Size(c); got != tc.size {
				t.Fatalf("size=%d, want %d", got, tc.size)
			}
			if got := typ.Alignment(c); got != tc.align {
				t.Fatalf("alignment=%d, want %d", got, tc.align)
			}
			if got := isTaggedNullable(c, typ); got != tc.tagged {
				t.Fatalf("tagged=%v", got)
			}
			round, err := parseTypeString(typ.String())
			if err != nil || !typ.SameExact(round) {
				t.Fatalf("roundtrip: %v", err)
			}
		})
	}
}
