package main

import (
	"fmt"
	"io"
)

// compileTruthTest evaluates value exactly once and sets ZF for the following
// branch or setcc. Scalars and pointers test their bits. An interface value is
// true iff its vtable is nonzero: the data word can legitimately be zero, and a
// failed assertion can retain data while producing a zero vtable. Values
// types test against tag -1, so tag zero remains true after narrowing.
func compileTruthTest(of io.Writer, c *Context, value AST) {
	checkZeroTestOperand(c, value, "Condition")
	if t, ok := nullableValueType(c, value); ok {
		tag := compileNullableTag(of, c, value, t)
		fmt.Fprintf(of, "\tcmp %s -1\n", tag.ref)
		tag.free(of)
		return
	}
	t := c.ResolveUnderlying(value.ASTType(c))
	var v spot
	if _, values := c.ValuesDeclForName(t.Name); values && t.Indirection == 0 && !t.IsSliceOrArray() {
		// Use the values sentinel even after narrowing removes the nullable
		// bit. In particular the first declared case (tag zero) is true.
		v = compileTop(of, c, value, newSpot(of, c, c.Temp(), t))
		fmt.Fprintf(of, "\tcmp %s -1\n", v.ref)
		v.free(of)
		return
	}
	v = compileTop(of, c, value, nullspot)
	if t.Indirection > 0 {
		v = materializePointerValue(of, c, v)
	}
	if t.Indirection == 0 && c.IsInterfaceType(t) {
		vtable := newSpot(of, c, c.Temp(), ASTType{Name: "uintptr"})
		fmt.Fprintf(of, "\tmov %s [%s+8]\n", vtable.ref, v.ref)
		v.free(of)
		v = vtable
	}
	fmt.Fprintf(of, "\ttest %s %s\n", v.ref, v.ref)
	v.free(of)
}
