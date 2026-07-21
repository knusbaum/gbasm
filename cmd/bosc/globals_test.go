package main

import (
	"encoding/binary"
	"math"
	"testing"
)

// TestEncodeFloatStaticInit pins the IEEE-754 encoding of file-scope float
// initializers. These cannot be covered by the .bos integration suite yet:
// that harness runs the program and diffs stdout, and printing a float needs
// `ftoa`, which is Stage 6. Until then this is the only observable output of
// float support, so it is asserted here.
//
// Expected values are derived independently via math.Float{32,64}bits rather
// than by recording what the encoder produced.
func TestEncodeFloatStaticInit(t *testing.T) {
	f64t := ASTType{Name: "f64"}
	f32t := ASTType{Name: "f32"}

	le64 := func(f float64) []byte {
		b := make([]byte, 8)
		binary.LittleEndian.PutUint64(b, math.Float64bits(f))
		return b
	}
	le32 := func(f float32) []byte {
		b := make([]byte, 4)
		binary.LittleEndian.PutUint32(b, math.Float32bits(f))
		return b
	}

	for _, tt := range []struct {
		name string
		dst  ASTType
		init AST
		want []byte
	}{
		{"f64 literal", f64t, &Literal{Val: 3.14159265358979}, le64(3.14159265358979)},
		{"f64 zero", f64t, &Literal{Val: 0.0}, le64(0)},
		{"f32 literal", f32t, &Literal{Val: 0.5}, le32(0.5)},

		// An untyped integer literal is meaningful in a float slot and
		// becomes that float type rather than being rejected.
		{"int literal into f64", f64t, &Literal{Val: uint64(3)}, le64(3)},
		{"int literal into f32", f32t, &Literal{Val: uint64(3)}, le32(3)},

		// Negation is parsed as Op2{Sub, 0, N} and folded here.
		{
			"negated f64",
			f64t,
			&Op2{Type: n_sub, First: &Literal{Val: uint64(0)}, Second: &Literal{Val: 1.5}},
			le64(-1.5),
		},
		{
			"negated int into f64",
			f64t,
			&Op2{Type: n_sub, First: &Literal{Val: uint64(0)}, Second: &Literal{Val: uint64(2)}},
			le64(-2),
		},

		// An f32 destination narrows through float32 — the same rounding an
		// explicit cast performs. 1e-10 is not exactly representable, so this
		// pins that the narrowing happens rather than the f64 bits being
		// truncated.
		{"f32 narrowing", f32t, &Literal{Val: 1.0e-10}, le32(float32(1.0e-10))},

		// Signed zero is distinct from zero and must survive.
		{
			"negative zero",
			f64t,
			&Op2{Type: n_sub, First: &Literal{Val: uint64(0)}, Second: &Literal{Val: 0.0}},
			le64(math.Copysign(0, -1)),
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, relocs, err := encodeStaticInit(nil, tt.dst, tt.init)
			if err != nil {
				t.Fatalf("encodeStaticInit: %v", err)
			}
			if len(relocs) != 0 {
				t.Errorf("got %d relocations, want none", len(relocs))
			}
			if string(got) != string(tt.want) {
				t.Errorf("got % x, want % x", got, tt.want)
			}
		})
	}
}

// TestEncodeFloatStaticInitRejects covers the destinations a float literal
// must not initialize. <intlit> is accepted anywhere numeric, but <floatlit>
// only by a float type — silently truncating 2.5 to 2 is the behavior this
// work removed from the lexer, and it must not reappear in static init.
func TestEncodeFloatStaticInitRejects(t *testing.T) {
	for _, tt := range []struct {
		name string
		dst  ASTType
	}{
		{"i64", ASTType{Name: "i64", Signed: true}},
		{"u8", ASTType{Name: "u8"}},
		{"bool", ASTType{Name: "bool"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, err := encodeStaticInit(nil, tt.dst, &Literal{Val: 2.5}); err == nil {
				t.Fatalf("encoding 2.5 into %s succeeded; want an error", tt.dst.Name)
			}
		})
	}
}
