package gbasm

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"
)

// TestSSEEncoding pins the encoding of the scalar SSE forms the float backend
// depends on. Every expected byte string below was produced by GNU as and read
// back with objdump, so these are ground truth rather than self-consistent
// output:
//
//	as --64 -o sse.o sse.s && objdump -d -M intel sse.o
//
// The cases deliberately cover all three mandatory prefixes (F2 for *sd, F3 for
// *ss, 66 for ucomisd/movq) and, for each shape, both a low register and a
// high one — XMM8-15 need the fourth register bit routed through REX.R/REX.B,
// which is invisible if a test only ever names XMM0-7.
func TestSSEEncoding(t *testing.T) {
	a, err := ParseFile("x86_64.xml")
	if err != nil {
		t.Fatalf("failed to parse instruction database: %s", err)
	}

	// -200 rather than -8: this encoder always emits a 32-bit displacement,
	// while `as` shortens to disp8 when it fits. Both are valid encodings of
	// the same instruction, but choosing an offset too large for disp8 makes
	// the assembler's output directly comparable to ours.
	mem := Indirect{Reg: R_RBP, Off: -200, Size: 64}
	mem32 := Indirect{Reg: R_RBP, Off: -200, Size: 32}

	tests := []struct {
		want string // hex, from objdump
		op   string
		ops  []interface{}
	}{
		// Scalar moves, register to register. REX appears only for XMM8-15.
		{"f20f10cb", "MOVSD", []interface{}{X_XMM1, X_XMM3}},
		{"f2440f10e3", "MOVSD", []interface{}{X_XMM12, X_XMM3}},
		{"f2410f10dc", "MOVSD", []interface{}{X_XMM3, X_XMM12}},
		{"f2450f10e5", "MOVSD", []interface{}{X_XMM12, X_XMM13}},
		{"f30f10cb", "MOVSS", []interface{}{X_XMM1, X_XMM3}},
		{"f3450f10e5", "MOVSS", []interface{}{X_XMM12, X_XMM13}},

		// Arithmetic.
		{"f20f58cb", "ADDSD", []interface{}{X_XMM1, X_XMM3}},
		{"f2450f58e5", "ADDSD", []interface{}{X_XMM12, X_XMM13}},
		{"f20f5ccb", "SUBSD", []interface{}{X_XMM1, X_XMM3}},
		{"f20f59cb", "MULSD", []interface{}{X_XMM1, X_XMM3}},
		{"f20f5ecb", "DIVSD", []interface{}{X_XMM1, X_XMM3}},
		{"f30f58cb", "ADDSS", []interface{}{X_XMM1, X_XMM3}},

		// Comparison. 66 prefix; ucomiss has none at all.
		{"660f2ecb", "UCOMISD", []interface{}{X_XMM1, X_XMM3}},
		{"66450f2ee5", "UCOMISD", []interface{}{X_XMM12, X_XMM13}},
		{"0f2ecb", "UCOMISS", []interface{}{X_XMM1, X_XMM3}},

		// Class-crossing bit moves. REX.W is mandatory and comes from the
		// instruction database, not from the operand sizes.
		{"66480f7ed8", "MOVQ", []interface{}{R_RAX, X_XMM3}},
		{"664d0f7ee6", "MOVQ", []interface{}{R14, X_XMM12}},
		{"66480f6ed8", "MOVQ", []interface{}{X_XMM3, R_RAX}},
		{"664d0f6ee6", "MOVQ", []interface{}{X_XMM12, R14}},

		// Int <-> float conversion.
		{"f2480f2ac8", "CVTSI2SD", []interface{}{X_XMM1, R_RAX}},
		{"f24d0f2ae6", "CVTSI2SD", []interface{}{X_XMM12, R14}},
		{"f2480f2dc1", "CVTSD2SI", []interface{}{R_RAX, X_XMM1}},
		{"f24d0f2df4", "CVTSD2SI", []interface{}{R14, X_XMM12}},

		// Float width conversion.
		{"f30f5acb", "CVTSS2SD", []interface{}{X_XMM1, X_XMM3}},
		{"f20f5acb", "CVTSD2SS", []interface{}{X_XMM1, X_XMM3}},

		// Bitwise, used for negation via a sign-bit mask.
		{"0f57cb", "XORPS", []interface{}{X_XMM1, X_XMM3}},

		// Register/memory forms. These reuse the existing m32/m64 operand
		// matching, so only the xmm half is new.
		{"f20f108d38ffffff", "MOVSD", []interface{}{X_XMM1, mem}},
		{"f20f118d38ffffff", "MOVSD", []interface{}{mem, X_XMM1}},
		{"f2440f10a538ffffff", "MOVSD", []interface{}{X_XMM12, mem}},
		{"f20f588d38ffffff", "ADDSD", []interface{}{X_XMM1, mem}},
		{"f30f108d38ffffff", "MOVSS", []interface{}{X_XMM1, mem32}},
		{"660f2e8d38ffffff", "UCOMISD", []interface{}{X_XMM1, mem}},
	}

	for _, tt := range tests {
		var bs bytes.Buffer
		if _, err := a.Encode(&bs, tt.op, tt.ops...); err != nil {
			t.Errorf("%s %v: encode failed: %s", tt.op, tt.ops, err)
			continue
		}
		got := hex.EncodeToString(bs.Bytes())
		if got != tt.want {
			t.Errorf("%s %v:\n\tgot  %s\n\twant %s", tt.op, tt.ops, spaced(got), spaced(tt.want))
		}
	}
}

// spaced renders a hex string as space-separated bytes, matching objdump's
// layout so a mismatch can be eyeballed against the disassembler directly.
func spaced(h string) string {
	var b strings.Builder
	for i := 0; i < len(h); i += 2 {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(h[i : i+2])
	}
	return b.String()
}

// TestXMMRegisterMetadata covers the reg.go helpers directly. These feed the
// encoder (byte/needREX) and, later, the float register allocator (Class,
// fullReg, partial); getting fullReg or partial wrong on XMM is a panic rather
// than a wrong byte, so they are worth asserting apart from the encodings.
func TestXMMRegisterMetadata(t *testing.T) {
	for i := 0; i < 16; i++ {
		r := X_XMM0 + Register(i)

		if got, want := r.Class(), ClassFloat; got != want {
			t.Errorf("XMM%d.Class() = %v, want %v", i, got, want)
		}
		// byte() yields the full four-bit index; ModRM/SIB mask to three bits
		// and the REX encoder takes bit 3 for R/X/B.
		if got, want := r.byte(), byte(i); got != want {
			t.Errorf("XMM%d.byte() = %d, want %d", i, got, want)
		}
		if got, want := r.needREX(), i >= 8; got != want {
			t.Errorf("XMM%d.needREX() = %t, want %t", i, got, want)
		}
		if got, want := r.Width(), 128; got != want {
			t.Errorf("XMM%d.Width() = %d, want %d", i, got, want)
		}
		if got := r.fullReg(); got != r {
			t.Errorf("XMM%d.fullReg() = %v, want itself", i, got)
		}
		if _, ok := r.subRegisters8(); ok {
			t.Errorf("XMM%d.subRegisters8() reported sub-registers; XMM has none", i)
		}
		// Width selects the instruction, not a narrower register, so every
		// requested size resolves back to the same register.
		for _, size := range []int{32, 64, 128} {
			if got, ok := r.partial(size); !ok || got != r {
				t.Errorf("XMM%d.partial(%d) = %v, %t; want itself, true", i, size, got, ok)
			}
		}

		name := r.String()
		if want := "XMM" + itoa(i); name != want {
			t.Errorf("XMM%d.String() = %q, want %q", i, name, want)
		}
		parsed, err := ParseReg(strings.ToLower(name))
		if err != nil {
			t.Errorf("ParseReg(%q): %s", name, err)
		} else if parsed != r {
			t.Errorf("ParseReg(%q) = %v, want %v", name, parsed, r)
		}
	}

	// A general-purpose register must not be mistaken for a float one.
	if got := R_RAX.Class(); got != ClassInt {
		t.Errorf("RAX.Class() = %v, want %v", got, ClassInt)
	}
	// XMM16+ is AVX-512 territory and not part of this register file.
	if _, err := ParseReg("xmm16"); err == nil {
		t.Errorf("ParseReg(\"xmm16\") succeeded; only XMM0-15 exist")
	}
}

func itoa(i int) string {
	if i < 10 {
		return string(rune('0' + i))
	}
	return string(rune('0'+i/10)) + string(rune('0'+i%10))
}
