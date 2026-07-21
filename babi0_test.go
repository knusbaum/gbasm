package gbasm

import (
	"encoding/binary"
	"testing"
)

// Spec tests for the assembler/core half of the Boson ABI, DESIGN.md § The
// Boson ABI (BABI-0). The compiler half lives in cmd/bosc/babi0_test.go.
//
// Every expected value is a literal transcribed from that section. In
// particular the register sequences below are written out independently of
// cmd/bosc's copy: bosc names caller-side argument registers itself while the
// core maps callee-side argument indices, so the same sequence is encoded
// twice in the tree with nothing connecting them. Asserting both against the
// same transcribed constants is what makes a divergence between them a test
// failure rather than a silent disagreement.

// BABI-0 § Function calls / Argument classification -- callee side.
//
// The width-dependent sub-registers matter as much as the order: a callee
// receiving an i32 argument reads EDI, not RDI, so the table is (index, width)
// -> register, not index -> register.
func TestBABI0CalleeArgumentRegisters(t *testing.T) {
	// Indexed by argument position, then by width: 8, 16, 32, 64.
	want := [][4]Register{
		0: {R_DIL, R_DI, R_EDI, R_RDI},
		1: {R_SIL, R_SI, R_ESI, R_RSI},
		2: {R_DL, R_DX, R_EDX, R_RDX},
		3: {R_CL, R_CX, R_ECX, R_RCX},
		4: {R8B, R8W, R8D, R8},
		5: {R9B, R9W, R9D, R9},
	}
	widths := [4]int{8, 16, 32, 64}

	for i, row := range want {
		for w, wantReg := range row {
			got := argRegForSize(i, widths[w])
			if got != wantReg {
				t.Errorf("argRegForSize(%d, %d) = %v, want %v (BABI-0 integer argument registers)",
					i, widths[w], got, wantReg)
			}
		}
	}
}

// BABI-0 § Function calls / Argument classification -- the SSE sequence.
func TestBABI0FloatArgumentRegisters(t *testing.T) {
	want := []Register{X_XMM0, X_XMM1, X_XMM2, X_XMM3, X_XMM4, X_XMM5, X_XMM6, X_XMM7}
	if len(float_arg_regs) != len(want) {
		t.Fatalf("float_arg_regs has %d entries, want %d (XMM0-XMM7)", len(float_arg_regs), len(want))
	}
	for i := range want {
		if float_arg_regs[i] != want[i] {
			t.Errorf("float_arg_regs[%d] = %v, want %v", i, float_arg_regs[i], want[i])
		}
	}
}

// BABI-0 § Data representation / Alignment: "Alignment is a property of
// addresses, not just offsets." Aligned field offsets only produce aligned
// field addresses if the aggregate's base is aligned, so stack slots are
// placed at offsets satisfying their own alignment.
//
// RBP is 8-aligned, so an offset that is a multiple of A yields an A-aligned
// address for any A <= 8.
func TestBABI0FrameSlotsAreAligned(t *testing.T) {
	fr := NewFrame()

	// space() returns the offset from RBP, growing downward, and the caller
	// passes sizes in bytes. A packed allocator would return -1, -9, -11.
	for _, tc := range []struct {
		size int32
		want int32
	}{
		{1, -1},  // 1-byte slot needs no alignment
		{8, -16}, // 8-byte slot must land on a multiple of 8
		{2, -18}, // 2-byte slot must land on a multiple of 2
		{4, -24}, // 4-byte slot must land on a multiple of 4
	} {
		got := fr.space(tc.size)
		if got != tc.want {
			t.Errorf("space(%d) = %d, want %d", tc.size, got, tc.want)
		}
		if a := slotAlign(tc.size); got%a != 0 {
			t.Errorf("space(%d) = %d, which is not %d-aligned", tc.size, got, a)
		}
	}
}

// BABI-0 § Stack frame / Stack alignment.
//
// A callee enters with RSP % 16 == 8 (the call pushed the return address) and
// must reach its own call sites with RSP % 16 == 0. The prologue's six
// callee-saved pushes are 48 bytes -- 0 mod 16 -- so the frame reservation
// must supply the missing 8: the prologue's total displacement has to be
// congruent to 8 mod 16.
//
// This is the static form of the invariant. A runtime probe can only observe
// the one frame size it happens to exercise; this holds it across every frame
// size, which is what makes it a spec test rather than a spot check.
func TestBABI0PrologueEstablishesStackAlignment(t *testing.T) {
	// BABI-0 § Stack frame: RBX, RBP, R12, R13, R14, R15 are pushed
	// unconditionally.
	const calleeSavedPushBytes = 6 * 8

	for _, localBytes := range []int{0, 1, 8, 9, 16, 17, 24, 100, 128, 129} {
		f := newTestFunction(t)
		if err := f.Prologue(); err != nil {
			t.Fatalf("Prologue: %v", err)
		}
		if localBytes > 0 {
			if _, err := f.AllocBytes("probe", localBytes); err != nil {
				t.Fatalf("AllocBytes(%d): %v", localBytes, err)
			}
		}
		if err := f.Epilogue(); err != nil {
			t.Fatalf("Epilogue: %v", err)
		}

		sub := int(binary.LittleEndian.Uint32(f.bs.Bytes()[f.localsLocation:]))
		total := calleeSavedPushBytes + sub

		if total%16 != 8 {
			t.Errorf("locals=%d: prologue displaces %d bytes (48 pushed + %d reserved), which is %d mod 16; want 8 so that RSP is 16-aligned at call sites",
				localBytes, total, sub, total%16)
		}
		if sub < localBytes {
			t.Errorf("locals=%d: reserved only %d bytes", localBytes, sub)
		}
	}
}

// newTestFunction builds a Function detached from any real object file.
func newTestFunction(t *testing.T) *Function {
	t.Helper()
	o, err := NewOFile("babi0_test", "main")
	if err != nil {
		t.Fatalf("NewOFile: %v", err)
	}
	f, err := o.NewFunction("babi0_test.bs", 1, "probe")
	if err != nil {
		t.Fatalf("NewFunction: %v", err)
	}
	return f
}
