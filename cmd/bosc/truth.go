package main

import (
	"fmt"
	"io"
)

// compileTruthTest evaluates value exactly once and sets ZF for the following
// branch or setcc. Scalars and pointers test their bits. An interface value is
// true iff its vtable is nonzero: the data word can legitimately be zero, and a
// failed assertion can retain data while producing a zero vtable.
func compileTruthTest(of io.Writer, c *Context, value AST) {
	checkZeroTestOperand(c, value, "Condition")
	v := compileTop(of, c, value, nullspot)
	t := c.ResolveUnderlying(value.ASTType(c))
	if t.Indirection == 0 && c.IsInterfaceType(t) {
		vtable := newSpot(of, c, c.Temp(), ASTType{Name: "uintptr"})
		fmt.Fprintf(of, "\tmov %s [%s+8]\n", vtable.ref, v.ref)
		v.free(of)
		v = vtable
	}
	fmt.Fprintf(of, "\ttest %s %s\n", v.ref, v.ref)
	v.free(of)
}
