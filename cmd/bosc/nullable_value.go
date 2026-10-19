package main

import (
	"fmt"
	"io"
)

// Tagged nullable values keep their source type and qualifiers. Only their
// storage differs: an i64 tag (-1 absent, 0 present), followed by aligned T.
// Pointer, interface and tag-only values null representations stay unchanged.
func isTaggedNullable(c *Context, t ASTType) bool {
	if c == nil || t.NilMask&1 == 0 || t.Indirection != 0 || t.IsSliceOrArray() || t.FuncSig != nil || t.IsUnion() {
		return false
	}
	u := c.ResolveUnderlying(t)
	if u.Indirection != 0 || u.IsSliceOrArray() || u.FuncSig != nil || u.IsUnion() || c.IsInterfaceType(u) {
		return false
	}
	if _, ok := c.ValuesDeclForName(u.Name); ok {
		return false
	}
	return u.Name != "void" && u.Name != "<nil>" && u.Name != "<intlit>" && u.Name != "<floatlit>"
}
func nullablePayloadType(t ASTType) ASTType { t.NilMask &^= 1; return t }
func nullableValueType(c *Context, a AST) (ASTType, bool) {
	if t, ok := declaredPathType(c, a); ok && isTaggedNullable(c, t) {
		return t, true
	}
	t := a.ASTType(c)
	return t, isTaggedNullable(c, t)
}

type nullableInjection struct {
	Val       AST
	Type      ASTType
	Returning bool
}

func (v *nullableInjection) ASTType(*Context) ASTType { return v.Type }
func (v *nullableInjection) Pos() position            { return v.Val.Pos() }
func (v *nullableInjection) Note() string             { return "nullable value injection" }

func compileNullableInjection(of io.Writer, c *Context, v *nullableInjection, dest spot) spot {
	if dest.empty() {
		dest = newSpot(of, c, c.Temp(), v.Type)
	}
	if litIsNil(v.Val) {
		emitNullableNil(of, c, dest)
		return dest
	}
	payload := nullablePayloadType(v.Type)
	_, literal := v.Val.(*StructLiteral)
	if _, reason := coerceType(c, c.ResolveUnderlying(payload), c.ResolveUnderlying(v.Val.ASTType(c))); reason != coerceOK && !(literal && sameIgnoringOwned(payload, v.Val.ASTType(c))) {
		reportCoerceFailure(v, payload, v.Val.ASTType(c), reason, "Cannot initialize %s from %s", payload, v.Val.ASTType(c))
	}
	if v.Returning && v.Val.ASTType(c).HasOwned() && !payload.HasOwned() {
		CompileErrorF(v, "Cannot return %s as non-owned %s; ownership would be dropped", v.Val.ASTType(c), v.Type)
	}
	ptr := newSpot(of, c, c.Temp(), ASTType{Name: "uintptr"})
	fmt.Fprintf(of, "\tlea %s [%s+8]\n", ptr.ref, dest.ref)
	slot := spot{ref: ptr.ref, t: payload, nameIsAddress: true}
	if typeIsMemoryBacked(c, payload) {
		compileTop(of, c, v.Val, slot)
	} else {
		val := compileTop(of, c, v.Val, newSpot(of, c, c.Temp(), payload))
		fmt.Fprintf(of, "\t%s [%s] %s\n", scalarMov(c, payload), ptr.ref, val.ref)
		val.free(of)
	}
	fmt.Fprintf(of, "\tmov qword[%s] 0\n", dest.ref)
	ptr.free(of)
	return dest
}
func emitNullableNil(of io.Writer, c *Context, dest spot) {
	// Clear the inactive bytes too, so copies never expose uninitialized storage.
	z := newSpot(of, c, c.Temp(), numASTType())
	fmt.Fprintf(of, "\tmov %s 0\n", z.ref)
	for i := 0; i < dest.t.Size(c); i += 8 {
		fmt.Fprintf(of, "\tmov [%s+%d] %s\n", dest.ref, i, z.ref)
	}
	fmt.Fprintf(of, "\tmov qword[%s] -1\n", dest.ref)
	z.free(of)
}

// A raw view suppresses flow-narrowed payload extraction while reading the tag.
type nullableStorage struct {
	Val  AST
	Type ASTType
}

func (v *nullableStorage) ASTType(*Context) ASTType { return v.Type }
func (v *nullableStorage) Pos() position            { return v.Val.Pos() }
func (v *nullableStorage) Note() string             { return "nullable storage" }
func compileNullableStorage(of io.Writer, c *Context, v *nullableStorage, dest spot) spot {
	var s spot
	if sym, ok := v.Val.(*Symbol); ok {
		if c.IsMoved(sym.Name) {
			CompileErrorF(sym, "Use of %q after it was moved", sym.Name)
		}
		c.markUsed(sym.Name)
		checkReadable(c, sym)
		s = spot{ref: sym.Name, t: v.Type, nameIsAddress: true}
	} else if _, ok := v.Val.(*Dot); ok {
		s = compileLval(of, c, v.Val, nullspot)
		s.t = v.Type
		s.nameIsAddress = true
	} else if d, ok := v.Val.(*Deref); ok {
		s = compileTop(of, c, d.Val, nullspot)
		s.t = v.Type
		s.nameIsAddress = true
	} else {
		return compileTop(of, c, v.Val, dest)
	}
	if !dest.empty() {
		move(of, c, dest, s)
		return dest
	}
	return s
}
func compileNullablePayload(of io.Writer, c *Context, value AST, t ASTType, dest spot, assert bool) spot {
	storage := compileNullableStorage(of, c, &nullableStorage{Val: value, Type: t}, nullspot)
	if path, ok := FlowPathForExpr(value); ok && c.NullFact(path) == NullKnownNonNull {
		assert = false
	}
	if assert {
		live := c.Label("nonnull")
		fmt.Fprintf(of, "\tcmp qword[%s] -1\n\tjne %s\n\tcall _init.nil_assert\n\tlabel %s\n", storage.ref, live, live)
	}
	payload := nullablePayloadType(t)
	if dest.empty() {
		dest = newSpot(of, c, c.Temp(), payload)
	}
	ptr := newSpot(of, c, c.Temp(), ASTType{Name: "uintptr"})
	fmt.Fprintf(of, "\tlea %s [%s+8]\n", ptr.ref, storage.ref)
	if typeIsMemoryBacked(c, payload) {
		spot_memcpy(of, c, dest, ptr, payload.Size(c))
	} else {
		fmt.Fprintf(of, "\t%s %s [%s]\n", scalarMov(c, payload), dest.ref, ptr.ref)
	}
	ptr.free(of)
	storage.free(of)
	return dest
}
func compileNullableTag(of io.Writer, c *Context, value AST, t ASTType) spot {
	storage := compileNullableStorage(of, c, &nullableStorage{Val: value, Type: t}, nullspot)
	tag := newSpot(of, c, c.Temp(), numASTType())
	fmt.Fprintf(of, "\tmov %s [%s]\n", tag.ref, storage.ref)
	storage.free(of)
	return tag
}

func nullablePayloadLval(of io.Writer, c *Context, value AST, t ASTType) spot {
	storage := compileNullableStorage(of, c, &nullableStorage{Val: value, Type: t}, nullspot)
	payload := nullablePayloadType(t)
	ptr := newSpot(of, c, c.Temp(), ASTType{Name: "uintptr"})
	fmt.Fprintf(of, "\tlea %s [%s+8]\n", ptr.ref, storage.ref)
	ptr.t = payload
	ptr.nameIsAddress = true
	if !typeIsMemoryBacked(c, payload) {
		ptr.t.Indirection++
		ptr.t.NilMask <<= 1
		ptr.t.OwnedMask <<= 1
		ptr.t.MutMask <<= 1
	}
	return ptr
}

// Slice element sizes may exceed x86's scale field once an element is tagged.
func sliceElementAddress(of io.Writer, c *Context, ptr, index spot, size int) spot {
	address := newSpot(of, c, c.Temp(), ASTType{Name: "uintptr"})
	if sibScaleOK(size) {
		fmt.Fprintf(of, "\tlea %s [%s+%s*%d]\n", address.ref, ptr.ref, index.ref, size)
	} else {
		offset := newSpot(of, c, c.Temp(), numASTType())
		factor := newSpot(of, c, c.Temp(), numASTType())
		fmt.Fprintf(of, "\tmov %s %d\n", factor.ref, size)
		fmt.Fprintf(of, "\tmov %s %s\n\timul %s %s\n\tlea %s [%s+%s]\n", offset.ref, index.ref, offset.ref, factor.ref, address.ref, ptr.ref, offset.ref)
		factor.free(of)
		offset.free(of)
	}
	return address
}
