package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
)

func isNullableValues(c *Context, t ASTType) bool {
	if c == nil || t.Indirection != 0 || t.IsSliceOrArray() || t.NilMask&1 == 0 {
		return false
	}
	_, ok := c.ValuesDeclForName(t.Name)
	return ok
}

func nilRepresentation(c *Context, t ASTType) int {
	if isNullableValues(c, t) {
		return -1
	}
	return 0
}

func reverseProjectionSymbol(name string, index int) string {
	return fmt.Sprintf("__reverse_projection_%s__%d", name, index)
}

// Reverse lookup uses numeric equality for scalars and content equality for
// byte strings. Other aggregate projections have no language-defined equality.
func reverseProjectionSupported(c *Context, t ASTType) bool {
	u := c.ResolveUnderlying(t)
	if t.HasOwned() || u.Indirection != 0 || u.FuncSig != nil || u.NilMask != 0 {
		return false
	}
	return isIntegerType(c, u) || isFloatType(c, u) || u.Same(boolASTType()) || u.Same(byteSliceASTType())
}

// Check injectivity using the same constant encoder as the forward table.
// Strings are interned, so identical contents have identical relocations.
// Numeric equality identifies +0 and -0; NaN has no inverse under equality.
func reverseProjectionProblem(c *Context, vd *ValuesDecl, index int) string {
	pt := vd.Projections[index]
	if !reverseProjectionSupported(c, pt) {
		return "projection type has no supported reverse equality"
	}
	seen := make(map[string]string)
	for _, vc := range vd.Cases {
		data, relocs, err := encodeStaticInit(c, pt, vc.Expr[index])
		if err != nil {
			return err.Error()
		}
		if isFloatType(c, pt) {
			var n float64
			if len(data) == 4 {
				n = float64(math.Float32frombits(binary.LittleEndian.Uint32(data)))
			} else {
				n = math.Float64frombits(binary.LittleEndian.Uint64(data))
			}
			if math.IsNaN(n) {
				return "projection contains NaN, which cannot be matched by equality"
			}
			if n == 0 {
				clear(data)
			}
		}
		key := fmt.Sprintf("%x/%v", data, relocs)
		if previous, ok := seen[key]; ok {
			return fmt.Sprintf("projection is not injective: cases %s and %s have the same projected value", previous, vc.Name)
		}
		seen[key] = vc.Name
	}
	return ""
}

// A generated ordinary function carries both the implementation and its
// availability through .bo metadata. Non-injective projections get no helper;
// they remain perfectly valid forward projections.
func reverseProjectionDecl(vd *ValuesDecl, index int) *FuncDecl {
	result := ASTType{Name: vd.Name, NilMask: 1}
	search := &reverseProjectionSearch{Decl: vd, Index: index}
	return &FuncDecl{Name: reverseProjectionSymbol(vd.Name, index), IsPub: vd.IsPub,
		Args:   []Binding{{Name: "value", Type: vd.Projections[index], IsConst: true, p: vd.p}},
		Return: result, Body: &Block{Body: []AST{&Return{Val: search, p: vd.p}}, p: vd.p}, p: vd.p}
}

type reverseProjectionSearch struct {
	Decl  *ValuesDecl
	Index int
}

func (r *reverseProjectionSearch) ASTType(*Context) ASTType {
	return ASTType{Name: r.Decl.Name, NilMask: 1}
}
func (r *reverseProjectionSearch) Note() string  { return "<reverse projection>" }
func (r *reverseProjectionSearch) Pos() position { return r.Decl.p }

func compileReverseProjectionCast(of io.Writer, c *Context, src AST, vd *ValuesDecl, dst ASTType, dest spot) spot {
	if vd.IsPayloadBearing() {
		CompileErrorF(src, "Cannot reverse-project payload-bearing values type %s", vd.Name)
	}
	st := src.ASTType(c)
	index := -1
	for i, pt := range vd.Projections {
		if pt.Same(st) {
			index = i
			break
		}
	}
	// Untyped numeric literals need exactly one compatible projection. Never
	// silently choose an integer width when the declaration provides several.
	if index < 0 && isPendingLit(st) {
		for i, pt := range vd.Projections {
			compatible := (st.Same(intlitASTType()) && isNumericType(c, pt)) || (st.Same(floatlitASTType()) && isFloatType(c, pt))
			if compatible {
				if index >= 0 {
					CompileErrorF(src, "Cannot reverse-project %s from %s: ambiguous projection; give the argument an explicit type", dst, st)
				}
				index = i
			}
		}
	}
	if index < 0 {
		CompileErrorF(src, "Cannot reverse-project %s from %s: no matching projection", dst, st)
	}
	pt := vd.Projections[index]
	if !reverseProjectionSupported(c, pt) {
		CompileErrorF(src, "Cannot reverse-project %s from %s: projection type has no supported reverse equality", dst, pt)
	}
	pkg, leaf := splitQualifiedName(vd.Name)
	helper := reverseProjectionSymbol(leaf, index)
	var decl *FuncDecl
	if pkg == "" {
		if problem := reverseProjectionProblem(c, vd, index); problem != "" {
			CompileErrorF(src, "Cannot reverse-project %s from %s: %s", dst, pt, problem)
		}
		if _, exists := c.FuncDeclForName(helper); !exists {
			c.DefineFunc(helper, reverseProjectionDecl(vd, index))
		}
		decl, _ = c.FuncDeclForName(helper)
	} else {
		for ctx := c; ctx != nil; ctx = ctx.parent {
			if found, ok := ctx.imports[pkg][helper]; ok {
				decl = found
				break
			}
		}
		if decl == nil {
			CompileErrorF(src, "Cannot reverse-project %s from %s: projection is not injective", dst, pt)
		}
	}
	var callee AST = &Symbol{Name: helper, p: src.Pos()}
	if pkg != "" {
		callee = &Dot{Val: &Symbol{Name: pkg, p: src.Pos()}, Member: helper}
	}
	return compileTop(of, c, &Funcall{Callee: callee, Args: []AST{src}, p: src.Pos(), resolvedDecl: decl, resolvedPkg: pkg}, dest)
}

func compileReverseProjectionSearch(of io.Writer, c *Context, search *reverseProjectionSearch, dest spot) spot {
	vd, index := search.Decl, search.Index
	pt := vd.Projections[index]
	input := compileTop(of, c, &Symbol{Name: "value", p: vd.p}, nullspot)
	if dest.empty() {
		dest = newSpot(of, c, c.Temp(), search.ASTType(c))
	}
	done := c.Label("reverse_done")
	for _, vc := range vd.Cases {
		next := c.Label("reverse_next")
		selector := &Dot{Val: &Symbol{Name: vd.Name, p: vd.p}, Member: vc.Name}
		candidate := newSpot(of, c, c.Temp(), pt)
		compileProjectionCast(of, c, selector, ASTType{Name: vd.Name}, index, pt, candidate)
		u := c.ResolveUnderlying(pt)
		if u.IsSlice() {
			emitByteStringMismatch(of, c, input, candidate, next)
		} else if isFloatType(c, pt) {
			op := "ucomisd"
			if pt.Size(c) == 4 {
				op = "ucomiss"
			}
			fmt.Fprintf(of, "\t%s %s %s\n\tjp %s\n\tjne %s\n", op, input.ref, candidate.ref, next, next)
		} else {
			fmt.Fprintf(of, "\tcmp %s %s\n\tjne %s\n", input.ref, candidate.ref, next)
		}
		fmt.Fprintf(of, "\tmov %s %d\n\tjmp %s\n\tlabel %s\n", dest.ref, vc.Tag, done, next)
		candidate.free(of)
	}
	fmt.Fprintf(of, "\tmov %s -1\n\tlabel %s\n", dest.ref, done)
	input.free(of)
	return dest
}

func emitByteStringMismatch(of io.Writer, c *Context, a, b spot, mismatch string) {
	ptrA := newSpot(of, c, c.Temp(), numASTType())
	ptrB := newSpot(of, c, c.Temp(), numASTType())
	count := newSpot(of, c, c.Temp(), numASTType())
	otherCount := newSpot(of, c, c.Temp(), numASTType())
	index := newSpot(of, c, c.Temp(), numASTType())
	byteA := newSpot(of, c, c.Temp(), byteASTType())
	byteB := newSpot(of, c, c.Temp(), byteASTType())
	loop, matched := c.Label("reverse_bytes"), c.Label("reverse_matched")
	fmt.Fprintf(of, "\tmov %s [%s]\n\tmov %s [%s]\n", ptrA.ref, a.ref, ptrB.ref, b.ref)
	fmt.Fprintf(of, "\tmov %s [%s+8]\n\tmov %s [%s+8]\n", count.ref, a.ref, otherCount.ref, b.ref)
	fmt.Fprintf(of, "\tcmp %s %s\n\tjne %s\n", count.ref, otherCount.ref, mismatch)
	fmt.Fprintf(of, "\tmov %s 0\n\tlabel %s\n", index.ref, loop)
	fmt.Fprintf(of, "\tcmp %s %s\n\tje %s\n", index.ref, count.ref, matched)
	fmt.Fprintf(of, "\tmov %s [%s+%s]\n\tmov %s [%s+%s]\n", byteA.ref, ptrA.ref, index.ref, byteB.ref, ptrB.ref, index.ref)
	fmt.Fprintf(of, "\tcmp %s %s\n\tjne %s\n", byteA.ref, byteB.ref, mismatch)
	fmt.Fprintf(of, "\tinc %s\n\tjmp %s\n\tlabel %s\n", index.ref, loop, matched)
	for _, s := range []spot{ptrA, ptrB, count, otherCount, index, byteA, byteB} {
		s.free(of)
	}
}

// Nullable values keep the enum's one-word representation. The sentinel is
// independent of the projection values, which may themselves include -1.
func nullableValuesOffsets(c *Context, t ASTType, base int) []int {
	if t.Indirection != 0 || t.IsSlice() || t.FuncSig != nil {
		return nil
	}
	if isNullableValues(c, t) {
		return []int{base}
	}
	if t.IsArray() {
		inner := nullableValuesOffsets(c, *t.Element, 0)
		if len(inner) == 0 {
			return nil
		}
		var offsets []int
		for i := 0; i < t.ArraySize; i++ {
			for _, off := range inner {
				offsets = append(offsets, base+i*t.Element.Size(c)+off)
			}
		}
		return offsets
	}
	if sd, ok := structDeclForType(c, t); ok {
		var offsets []int
		for _, f := range sd.Fields {
			off, ft := sd.ByteOffset(c, f.Name)
			offsets = append(offsets, nullableValuesOffsets(c, ft, base+off)...)
		}
		return offsets
	}
	return nil
}

func defaultValueBytes(c *Context, t ASTType) []byte {
	data := make([]byte, t.Size(c))
	for _, off := range nullableValuesOffsets(c, t, 0) {
		binary.LittleEndian.PutUint64(data[off:], ^uint64(0))
	}
	return data
}

func emitNullableValuesDefaults(of io.Writer, c *Context, t ASTType, address string) {
	for _, off := range nullableValuesOffsets(c, t, 0) {
		fmt.Fprintf(of, "\tmov qword[%s+%d] -1\n", address, off)
	}
}
