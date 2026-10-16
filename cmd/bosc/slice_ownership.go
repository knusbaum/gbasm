package main

// prepareSliceOwnership checks an owning destination before type compatibility
// and escape checking. A prefix view can carry an existing backing obligation;
// this is a transfer, not creation of ownership from an arbitrary borrow.
// The annotation only selects the expression's type. The ordinary move hook
// transfers the obligation after the expression has been evaluated.
func prepareSliceOwnership(c *Context, value AST, destination ASTType) {
	if value == nil {
		return
	}
	dst := c.ResolveUnderlying(destination)
	if literal, ok := value.(*StructLiteral); ok && dst.MultiReturn {
		for _, field := range literal.Fields {
			slot := slotIndexFromFieldName(field.Name)
			if slot >= 0 && slot < len(dst.AnonFields) {
				prepareSliceOwnership(c, field.Val, dst.AnonFields[slot].Type)
			}
		}
		return
	}
	if !dst.IsSlice() || dst.OwnedMask&1 == 0 {
		return
	}
	// owned(expr) remains the explicit unsafe assertion escape hatch.
	if _, unsafe := value.(*OwnedPromotion); unsafe {
		return
	}
	if slice, ok := value.(*SliceOp); ok {
		sourceType := slice.Val.ASTType(c)
		if sourceType.IsSlice() && sourceType.OwnedMask&2 != 0 {
			CompileErrorF(value, "Cannot transfer an element-owned slice through slicing; move the whole slice instead")
		}
		slice.transferOwner = ""
		owner := prefixSliceOwner(c, slice)
		if owner == "" {
			CompileErrorF(value, "Cannot transfer slice backing ownership: source must be an owned slice or a zero-offset prefix of an owner")
		}
		checkOwnedSourceAvailable(c, &Symbol{Name: owner, p: value.Pos()})
		slice.transferOwner = owner
		return
	}
	src := c.ResolveUnderlying(value.ASTType(c))
	if src.IsSlice() && src.OwnedMask&1 == 0 {
		CompileErrorF(value, "Cannot transfer slice backing ownership: source has no owned backing")
	}
}

// Only syntactically traceable prefixes can transfer: a borrowed binding has
// no obligation, and its origin alone cannot prove its data pointer is still
// the owner's base. Nested prefixes must each preserve the starting pointer.
func prefixSliceOwner(c *Context, value AST) string {
	switch v := value.(type) {
	case *SliceOp:
		if v.Lower != nil {
			lo, constant := EvalConst(c, v.Lower)
			if !constant || lo.Sign() != 0 {
				return ""
			}
		}
		return prefixSliceOwner(c, v.Val)
	case *Symbol:
		t := c.ResolveUnderlying(v.ASTType(c))
		if t.IsSlice() && t.OwnedMask&1 != 0 {
			return v.Name
		}
	case *Deref:
		// A slice of an array reached through an owning pointer keeps the
		// same resource address, so it can take that pointer's obligation.
		if owner, ok := v.Val.(*Symbol); ok {
			t := c.ResolveUnderlying(owner.ASTType(c))
			array := v.ASTType(c)
			if t.Indirection == 1 && t.OwnedMask == 1 && array.IsArray() {
				return owner.Name
			}
		}
	}
	return ""
}
