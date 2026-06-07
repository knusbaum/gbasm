package main

import "testing"

// ptrType builds *T / *mut T (Indirection 1) for receiver-shape tests.
func ptrType(name string, mut bool) ASTType {
	t := ASTType{Name: name, Indirection: 1}
	if mut {
		t.MutMask = 1 << 1 // outermost pointer level's write-through bit
	}
	return t
}

// TestReceiverShapeOf pins the three-class receiver encoding, including the mut
// axis that M1 restored.
func TestReceiverShapeOf(t *testing.T) {
	cases := []struct {
		recv ASTType
		want int
	}{
		{ASTType{Name: "self"}, recvShapeValue},
		{ptrType("self", false), recvShapePtr},
		{ptrType("self", true), recvShapeMutPtr},
		{ASTType{Name: "self", Indirection: 2}, recvShapeNone},
	}
	for _, c := range cases {
		if got := receiverShapeOf(c.recv); got != c.want {
			t.Errorf("receiverShapeOf(%s) = %d, want %d", c.recv, got, c.want)
		}
	}
}

// TestReceiverSatisfiesMatrix enumerates (source capability) x (method
// receiver) and pins the sound subset: a value source needs a value receiver;
// a `*T` (read-only) source matches only a `*self` method; a `*mut T` source
// matches BOTH `*self` and `*mut self` (the legal `*mut -> *` weakening). The
// unsound direction (read-only source driving a `*mut self` method) must be
// rejected.
func TestReceiverSatisfiesMatrix(t *testing.T) {
	const V, P, M, N = recvShapeValue, recvShapePtr, recvShapeMutPtr, recvShapeNone
	type row struct {
		expected, method int
		want             bool
	}
	rows := []row{
		{V, V, true},
		{V, P, false},
		{V, M, false},
		{P, V, false},
		{P, P, true},
		{P, M, false}, // read-only source must NOT satisfy a *mut self method
		{M, V, false},
		{M, P, true}, // *mut source weakens to a *self method
		{M, M, true},
		{N, P, false},
		{P, N, false},
	}
	for _, r := range rows {
		if got := receiverSatisfies(r.method, r.expected); got != r.want {
			t.Errorf("receiverSatisfies(method=%d, expected=%d) = %v, want %v",
				r.method, r.expected, got, r.want)
		}
	}
}

// TestShapeWordArrayDistinct pins that array lengths beyond the old 4-bit cap
// (m2) encode and remain distinct, and that a length over the new cap is a
// clean error rather than a silent collision.
func TestShapeWordArrayDistinct(t *testing.T) {
	byteT := ASTType{Name: "byte"}
	mk := func(n int) ASTType {
		return ASTType{Element: &byteT, ArraySize: n, Indirection: 1, MutMask: 1 << 1} // *(byte[n])
	}
	w16, err := shapeWordFor(mk(16))
	if err != nil {
		t.Fatalf("byte[16]: unexpected error %v", err)
	}
	w32, err := shapeWordFor(mk(32))
	if err != nil {
		t.Fatalf("byte[32]: unexpected error %v", err)
	}
	w4096, err := shapeWordFor(mk(4096))
	if err != nil {
		t.Fatalf("byte[4096]: unexpected error %v", err)
	}
	if w16 == w32 || w16 == w4096 || w32 == w4096 {
		t.Errorf("array shape words collided: 16=%d 32=%d 4096=%d", w16, w32, w4096)
	}
	if _, err := shapeWordFor(mk(shapeArrayLenMax + 1)); err == nil {
		t.Errorf("array length %d should overflow the shape encoding", shapeArrayLenMax+1)
	}
}

// TestExpectedReceiverShapeFromSource checks the source-shape -> capability
// mapping the runtime helper mirrors: *T -> ptr, *mut T -> mutptr.
func TestExpectedReceiverShapeFromSource(t *testing.T) {
	value := ASTType{Name: "byte"}
	if lv, _ := shapeStackFor(value); expectedReceiverShape(lv) != recvShapeValue {
		t.Error("bare value should expect a value receiver")
	}
	if lv, _ := shapeStackFor(ptrType("foo", false)); expectedReceiverShape(lv) != recvShapePtr {
		t.Error("*T should expect *self capability")
	}
	if lv, _ := shapeStackFor(ptrType("foo", true)); expectedReceiverShape(lv) != recvShapeMutPtr {
		t.Error("*mut T should expect *mut self capability")
	}
}
