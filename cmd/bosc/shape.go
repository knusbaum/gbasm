package main

import (
	"fmt"
	"strings"
)

// Shape word: a 64-bit canonical encoding of a type's shape modifiers as a
// bounded constructor stack applied to a named base type. Read
// innermost-first. The bottom of the stack is the named base type; each
// stack entry is one constructor over the level beneath it.
//
// Layout (deterministic, byte-identical across compilation units). The word is
// built with a left-to-right bit cursor; fields are VARIABLE width because
// array levels carry a length:
//
//   - bits [0..3]                : level count (0..maxShapeLevels)
//   - then, for each level i (innermost first), starting at bit 4:
//       * shapeKindBits (3) kind bits
//       * iff kind == shapeKindArray: shapeArrayLenBits (13) length bits
//
// Because the encoding is sequential, two distinct types never collide as long
// as the (count, per-level kind, per-array length) tuple differs — which is
// exactly the identity the assertion-time word equality and the runtime helper
// rely on. The runtime helper (_iface) only ever reads the count (bits 0..3)
// and, when count==1, the single level's kind at bits [4..6]; it never decodes
// array lengths, so widening the length field below does not affect it.
//
// Constructor kinds. Innermost (closest to the base) entries are emitted first
// (lowest bit positions).
const (
	shapeKindNone    = 0 // unused / padding
	shapeKindPtr     = 1 // *T
	shapeKindMutPtr  = 2 // *mut T
	shapeKindSlice   = 3 // T[]
	shapeKindMutSli  = 4 // mut T[]
	shapeKindArray   = 5 // [N]T (array length follows the kind bits)
	shapeKindNullPtr = 6 // *?T  (nullable pointer; mut handled via MutPtr where set)
	shapeKindNullMut = 7 // *?mut T
)

const (
	shapeCountBits = 4
	shapeKindBits  = 3
	// Array lengths up to 8191 encode directly; anything larger is a clean
	// compile error (shapeStackFor). 13 bits keeps the common cases (small
	// fixed buffers) well within range while leaving room for several
	// non-array constructor levels in the 60 payload bits.
	shapeArrayLenBits = 13
	shapeArrayLenMax  = (1 << shapeArrayLenBits) - 1
	shapeTotalBits    = 64
	// maxShapeLevels bounds stack depth assuming the worst case (all
	// non-array levels, 3 bits each). Array levels are wider, so a stack that
	// fits depth-wise can still overflow the 64-bit word; shapeWordFor checks
	// the actual bit cursor and errors on overflow.
	maxShapeLevels = (shapeTotalBits - shapeCountBits) / shapeKindBits
)

// shapeLevel is one constructor in the stack.
type shapeLevel struct {
	kind     int
	arrayLen int // only meaningful for shapeKindArray
}

// shapeStackFor builds the constructor stack (innermost-first) for t,
// after stripping owned bits. It returns an error if the type is deeper than
// the bound or has a constructor that cannot be encoded.
func shapeStackFor(t ASTType) ([]shapeLevel, error) {
	t = t.StripOwned()
	var levels []shapeLevel

	// Walk the type from the outside in, pushing constructors. We end up
	// with an outermost-first list, then reverse to innermost-first.
	var outer []shapeLevel
	cur := t
	for {
		// Outer pointer levels (Indirection) are the outermost wrappers.
		for i := cur.Indirection - 1; i >= 0; i-- {
			mut := cur.MutMask&(1<<uint(i+1)) != 0
			nul := cur.NilMask&(1<<uint(i)) != 0
			var k int
			switch {
			case nul && mut:
				k = shapeKindNullMut
			case nul:
				k = shapeKindNullPtr
			case mut:
				k = shapeKindMutPtr
			default:
				k = shapeKindPtr
			}
			outer = append(outer, shapeLevel{kind: k})
		}
		if cur.IsSliceOrArray() {
			if cur.IsArray() {
				if cur.ArraySize > shapeArrayLenMax {
					return nil, fmt.Errorf("array length %d exceeds the maximum shape-encodable length %d", cur.ArraySize, shapeArrayLenMax)
				}
				outer = append(outer, shapeLevel{kind: shapeKindArray, arrayLen: cur.ArraySize})
			} else {
				k := shapeKindSlice
				// The slice's own write-through bit sits just above the
				// pointer levels wrapping it: position Indirection+1. (Bits
				// [1..Indirection] are the per-pointer-level mut bits already
				// consumed by the outer-pointer loop above.)
				if cur.MutMask&(1<<uint(cur.Indirection+1)) != 0 {
					k = shapeKindMutSli
				}
				outer = append(outer, shapeLevel{kind: k})
			}
			cur = *cur.Element
			continue
		}
		break
	}
	// Reverse outer (outermost-first) into innermost-first.
	for i := len(outer) - 1; i >= 0; i-- {
		levels = append(levels, outer[i])
	}
	if len(levels) > maxShapeLevels {
		return nil, fmt.Errorf("type shape has %d constructor levels, exceeding the maximum of %d", len(levels), maxShapeLevels)
	}
	return levels, nil
}

// shapeWordFor computes the canonical 64-bit shape word for t using a
// left-to-right bit cursor (see the layout comment above). It errors if the
// encoded stack would not fit in 64 bits.
func shapeWordFor(t ASTType) (uint64, error) {
	levels, err := shapeStackFor(t)
	if err != nil {
		return 0, err
	}
	var w uint64 = uint64(len(levels)) & ((1 << shapeCountBits) - 1)
	cursor := uint(shapeCountBits)
	put := func(val uint64, width uint) error {
		if cursor+width > shapeTotalBits {
			return fmt.Errorf("type shape does not fit in a 64-bit shape word")
		}
		w |= (val & ((1 << width) - 1)) << cursor
		cursor += width
		return nil
	}
	for _, lvl := range levels {
		if err := put(uint64(lvl.kind), shapeKindBits); err != nil {
			return 0, err
		}
		if lvl.kind == shapeKindArray {
			if err := put(uint64(lvl.arrayLen), shapeArrayLenBits); err != nil {
				return 0, err
			}
		}
	}
	return w, nil
}

// shapeBaseName returns the bare base-type name for t (the bottom of the
// constructor stack): for a scalar it is t.Name; for a slice/array it is the
// base element's name.
func shapeBaseName(t ASTType) string {
	t = t.StripOwned()
	b := t.BaseType()
	return b.Name
}

// shapeMangle renders a shape word's constructor stack as a short
// deterministic string suitable for a symbol-name segment. Empty stack
// renders as "v" (value).
func shapeMangle(levels []shapeLevel) string {
	if len(levels) == 0 {
		return "v"
	}
	var sb strings.Builder
	for _, lvl := range levels {
		switch lvl.kind {
		case shapeKindPtr:
			sb.WriteString("p")
		case shapeKindMutPtr:
			sb.WriteString("pm")
		case shapeKindNullPtr:
			sb.WriteString("pn")
		case shapeKindNullMut:
			sb.WriteString("pnm")
		case shapeKindSlice:
			sb.WriteString("s")
		case shapeKindMutSli:
			sb.WriteString("sm")
		case shapeKindArray:
			sb.WriteString(fmt.Sprintf("a%d", lvl.arrayLen))
		}
	}
	return sb.String()
}

// Receiver-shape canonical encoding. Derived from a source's shape word: the
// expected receiver direction a coercion/assertion from that shape demands.
const (
	recvShapeValue   = 0 // self (bare value receiver)
	recvShapePtr     = 1 // *self
	recvShapeMutPtr  = 2 // *mut self
	recvShapeNone    = 0xFF // no compatible receiver (deeper/unsupported stacks)
)

// expectedReceiverShape maps a source shape word to the receiver *capability*
// the source provides. The result is the strongest receiver class the source
// can supply:
//
//   - empty stack (bare value)        -> recvShapeValue (a value receiver)
//   - top-of-stack `*T` / `*?T`       -> recvShapePtr   (read-only pointer)
//   - top-of-stack `*mut T` / `*?mut` -> recvShapeMutPtr (mutable pointer)
//   - deeper / unsupported tops       -> recvShapeNone
//
// Matching is NOT a plain equality against a method's declared receiver shape:
// a mutable source (`recvShapeMutPtr`) may also satisfy a `*self` method
// (the legal `*mut -> *` weakening), while a read-only source (`recvShapePtr`)
// must NOT satisfy a `*mut self` method (that would let a read-only view drive
// a mutating method). The asymmetric rule lives in receiverSatisfies.
//
// The "top of stack" is the outermost constructor, i.e. the last element of
// the innermost-first stack.
func expectedReceiverShape(levels []shapeLevel) int {
	if len(levels) == 0 {
		return recvShapeValue
	}
	if len(levels) == 1 {
		switch levels[0].kind {
		case shapeKindPtr, shapeKindNullPtr:
			return recvShapePtr
		case shapeKindMutPtr, shapeKindNullMut:
			return recvShapeMutPtr
		}
	}
	return recvShapeNone
}

// receiverShapeOf computes the canonical receiver-shape encoding of a method's
// declared receiver type (its first parameter). Three-class:
// value (`self`) / `*self` (recvShapePtr) / `*mut self` (recvShapeMutPtr). The
// mut axis is load-bearing: a `*mut self` method requires a mutable source.
func receiverShapeOf(recv ASTType) int {
	recv = recv.StripOwned()
	if recv.Indirection == 0 {
		return recvShapeValue
	}
	if recv.Indirection == 1 {
		// Bit 1 is the outermost (single) pointer level's write-through bit.
		if recv.MutMask&(1<<1) != 0 {
			return recvShapeMutPtr
		}
		return recvShapePtr
	}
	return recvShapeNone
}

// receiverSatisfies reports whether a method whose declared receiver shape is
// methodRecv can be invoked from a source whose receiver *capability* is
// expected (the result of expectedReceiverShape). The rule:
//
//   - value source       satisfies only a value receiver.
//   - `*T` source        satisfies only a `*self` receiver (no mut weakening
//                        up: a read-only view cannot drive a mutating method).
//   - `*mut T` source    satisfies BOTH `*self` and `*mut self` receivers
//                        (the legal `*mut -> *` weakening at the source).
//   - recvShapeNone on either side never matches.
//
// This is the single predicate consulted by both the compile-time satisfaction
// filter and (mirrored in assembly) the runtime helper _iface.lookup_method,
// so the two paths agree exactly.
func receiverSatisfies(methodRecv, expected int) bool {
	if expected == recvShapeNone || methodRecv == recvShapeNone {
		return false
	}
	switch expected {
	case recvShapeValue:
		return methodRecv == recvShapeValue
	case recvShapePtr:
		return methodRecv == recvShapePtr
	case recvShapeMutPtr:
		return methodRecv == recvShapePtr || methodRecv == recvShapeMutPtr
	}
	return false
}
