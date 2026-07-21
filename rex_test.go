package gbasm

import (
	"bytes"
	"encoding/hex"
	"testing"
)

// TestREXPrefix pins the REX byte against GNU as. Expected bytes come from:
//
//	as --64 -o rex.o rex.s && objdump -d -M intel rex.o
//
// The interesting cases are the ones where the instruction database omits a
// REX field. An absent R/X/B attribute means the form has no such field, but
// it used to be read as "field 0", which made the bit track whichever register
// happened to be operand 0. `add r14, rbx` (R="#0" B="#1", no X) therefore set
// REX.X and emitted 4B 01 DE instead of 49 01 DE. Register-direct forms carry
// no SIB byte, so the CPU ignores X and the programs still ran — which is
// exactly why this needs a byte-level test rather than an execution test.
func TestREXPrefix(t *testing.T) {
	a, err := ParseFile("x86_64.xml")
	if err != nil {
		t.Fatalf("failed to parse instruction database: %s", err)
	}

	tests := []struct {
		want string
		op   string
		ops  []interface{}
	}{
		// Forms with no X field: X must stay 0 regardless of the operands.
		{"4901de", "ADD", []interface{}{R14, R_RBX}},
		{"4c01f3", "ADD", []interface{}{R_RBX, R14}},
		{"4d89fe", "MOV", []interface{}{R14, R15}},
		{"4889d8", "MOV", []interface{}{R_RAX, R_RBX}},
		{"4d01fe", "ADD", []interface{}{R14, R15}},
		{"4929de", "SUB", []interface{}{R14, R_RBX}},

		// Narrower operand sizes still need REX purely for the register bit.
		{"4189de", "MOV", []interface{}{R14D, R_EBX}},
		{"664189de", "MOV", []interface{}{R14W, R_BX}},
		{"4188de", "MOV", []interface{}{R14B, R_BL}},

		// Memory operands. B extends the base register and X must stay 0:
		// there is no SIB index in a plain [base + disp]. The offsets are
		// large enough to force disp32, which is all this encoder emits —
		// `as` would otherwise shorten them to disp8 and the bytes would not
		// be comparable.
		{"498b86c8000000", "MOV", []interface{}{R_RAX, Indirect{Reg: R14, Off: 200, Size: 64}}},
		// R12 and RSP as a base force a SIB byte with the "no index"
		// encoding, which also requires X = 0.
		{"498b8424c8000000", "MOV", []interface{}{R_RAX, Indirect{Reg: R12, Off: 200, Size: 64}}},
		{"488b8424c8000000", "MOV", []interface{}{R_RAX, Indirect{Reg: R_RSP, Off: 200, Size: 64}}},
		// Here X genuinely extends the SIB index, and must be set.
		{"4a8b04f3", "MOV", []interface{}{R_RAX, IndirectBaseIndexScale{Base: R_RBX, Index: R14, Scale: 8}}},

		// Opcode-embedded register forms (B extends the opcode reg field).
		{"4156", "PUSH", []interface{}{R14}},
		{"415e", "POP", []interface{}{R14}},

		// Single-operand and two-byte-opcode forms.
		{"49ffc6", "INC", []interface{}{R14}},
		{"49f7de", "NEG", []interface{}{R14}},
		{"4c0faff3", "IMUL", []interface{}{R14, R_RBX}},
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
