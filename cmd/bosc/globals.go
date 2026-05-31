package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"strings"
)

// relocSpec is the bosc-internal description of a single data relocation
// returned by encodeStaticInit. emitGlobalVarDecl translates these into
// bas `reloc <off> <sym> <addend>` lines inside a block-form var
// directive.
type relocSpec struct {
	Offset int    // byte offset within the encoded payload
	Symbol string // target symbol name (unqualified — linker auto-qualifies)
	Addend int64
}

// emitGlobalVarDecl writes a top-level VarDecl as a bas-level global var
// directive. With no initializer, the global is zero-filled at its type's
// natural size. With an initializer, the value is encoded to raw bytes and
// any pointer slots are recorded as data relocations; emission switches
// from the single-line string-literal form to the block form whenever the
// payload carries one or more relocations.
func emitGlobalVarDecl(of io.Writer, c *Context, a AST, ast *VarDecl) {
	// Owned at file scope has no place to discharge the obligation —
	// a global never goes out of scope, and any dispose() inside a
	// function isn't visible elsewhere. Reject regardless of whether
	// an initializer is present so the zero-init path is covered too.
	if ast.Type.HasOwned() {
		CompileErrorF(a, "Top-level vars cannot carry owned types")
	}
	// Record the name as address-backed so codegen sites that build
	// indirect addressing know they're operating on a memory-resident
	// name. Without this mark, NameIsAddress would fall back to
	// type-based memory-backing — which is true for structs and large
	// values but not for scalar globals like 'var x i64'.
	c.MarkAddress(ast.Name)
	size := ast.Type.Size(c)
	if ast.Init == nil {
		if !ast.Type.ZeroInitializable(c) {
			CompileErrorF(a, "Variable \"%s\" of type %s requires an initializer", ast.Name, ast.Type)
		}
		// Zero-init form: bas allocates `size` zero bytes.
		fmt.Fprintf(of, "%svar %s %s %d\n", pubPrefix(ast.IsPub), ast.Name, ast.Type, size)
		return
	}

	dstt := ast.Type

	// Type-fit is decided by encodeStaticInit, which knows when a
	// literal can be coerced into a wider/narrower destination
	// (intlit into any integer, string literal into byte[N], etc.)
	// and produces specific diagnostics for mismatches.
	data, relocs, err := encodeStaticInit(c, dstt, ast.Init)
	if err != nil {
		CompileErrorF(a, "%s", err.Error())
	}
	if len(data) != size {
		// Shouldn't happen if encodeStaticInit is honest, but check
		// rather than emit a globally-misaligned variable.
		CompileErrorF(a, "internal: static initializer encoded %d bytes, but type %s has size %d", len(data), dstt, size)
	}
	emitVarBlock(of, ast.Name, ast.Type.String(), data, relocs, ast.IsPub)
	// Any `&literal` forms encountered during the encode queued
	// anonymous globals to back their pointer targets. Emit them now,
	// alongside the named global they're nested inside. Mark each as
	// memory-backed so subsequent codegen treats them like any other
	// file-scope name.
	for _, ag := range c.DrainAnonGlobals() {
		c.MarkAddress(ag.Name)
		emitVarBlock(of, ag.Name, ag.Type, ag.Bytes, ag.Relocs, false)
	}
}

// emitVarBlock writes either the single-line var directive (when there are
// no relocs) or the multi-line block form. Centralized so we don't repeat
// the choice and the formatting in callers that might emit anonymous
// globals later.
func emitVarBlock(of io.Writer, name, vtype string, data []byte, relocs []relocSpec, isPub bool) {
	if len(relocs) == 0 {
		fmt.Fprintf(of, "%svar %s %s \"%s\"\n", pubPrefix(isPub), name, vtype, bytesToBasStringLiteral(data))
		return
	}
	fmt.Fprintf(of, "%svar %s %s {\n", pubPrefix(isPub), name, vtype)
	fmt.Fprintf(of, "\tbytes \"%s\"\n", bytesToBasStringLiteral(data))
	for _, r := range relocs {
		fmt.Fprintf(of, "\treloc %d %s %d\n", r.Offset, r.Symbol, r.Addend)
	}
	fmt.Fprintf(of, "}\n")
}

func pubPrefix(isPub bool) string {
	if isPub {
		return "pub "
	}
	return ""
}

// encodeStaticInit serializes a compile-time constant AST node into a raw
// byte payload paired with a list of pointer-slot relocations. Returns an
// error if init is not a recognized compile-time constant form.
//
// Supported forms:
//   - *Literal with uint64 Val for integer types (any width, any signedness)
//   - *Literal with byte Val for byte / 8-bit integer types
//   - *Literal with string Val for byte[N] (inline copy) and byte[]
//     (16-byte slice header with a relocation to a string constant)
//   - *Op2{n_sub, *Literal{0}, *Literal{uint64}} for negative integers
//   - *StructLiteral for struct types, recursively, when every field's
//     initializer is itself a supported form; relocations from nested
//     fields are offset-adjusted into the outer payload's coordinates
//   - *Address with Val=Symbol — produces an 8-byte pointer slot with a
//     relocation pointing at the symbol
func encodeStaticInit(c *Context, dstt ASTType, init AST) ([]byte, []relocSpec, error) {
	switch v := init.(type) {
	case *Literal:
		return encodeLiteralBytes(c, dstt, v)
	case *StructLiteral:
		return encodeStructLiteralBytes(c, dstt, v)
	case *ArrayLiteral:
		return encodeArrayLiteralBytes(c, dstt, v)
	case *Address:
		return encodeAddressBytes(c, dstt, v)
	case *Op2:
		// -N is parsed as Op2{Sub, Literal{0}, Literal{N}}. Recognize and
		// fold; reject any other Op2 form (we don't do general constant
		// folding here).
		if v.Type == n_sub {
			z, zok := v.First.(*Literal)
			n, nok := v.Second.(*Literal)
			if zok && nok {
				if zv, ok := z.Val.(uint64); ok && zv == 0 {
					if nv, ok := n.Val.(uint64); ok {
						b, err := encodeIntBytes(dstt, -int64(nv))
						return b, nil, err
					}
				}
			}
		}
		return nil, nil, fmt.Errorf("initializer is not a compile-time constant")
	case *Funcall:
		// Type cast of a single constant argument: FD(0) or io.FD(0).
		// Fold by encoding the inner expression under the underlying type.
		// When a function of the same bare name exists the call form
		// wins, mirroring the runtime path in Funcall.ASTType and
		// compileTop. Static init can't actually execute the call, so we
		// fall through to the "not a compile-time constant" diagnostic
		// rather than producing a fake constant from the struct cast.
		if len(v.Args) == 1 {
			pkg, name := v.PkgAndName()
			castName := v.QualifiedName()
			if _, ok := c.TypeByName(castName); ok {
				if _, _, hasFn := c.FuncDeclForCall(pkg, name); !hasFn {
					castType := ASTType{Name: castName}
					underlying := c.ResolveUnderlying(castType)
					underlying.MutMask = 0
					underlying.OwnedMask = 0
					underlying.NilMask = 0
					return encodeStaticInit(c, underlying, v.Args[0])
				}
			}
		}
		return nil, nil, fmt.Errorf("initializer is not a compile-time constant")
	default:
		return nil, nil, fmt.Errorf("initializer is not a compile-time constant")
	}
}

func encodeLiteralBytes(c *Context, dstt ASTType, l *Literal) ([]byte, []relocSpec, error) {
	switch v := l.Val.(type) {
	case uint64:
		b, err := encodeIntBytes(dstt, int64(v))
		return b, nil, err
	case byte:
		size := dstt.Size(c)
		if size != 1 {
			return nil, nil, fmt.Errorf("byte literal cannot initialize %s (size %d)", dstt, size)
		}
		return []byte{v}, nil, nil
	case string:
		// byte[N] destination: copy the literal bytes inline and
		// zero-pad to the array size. No relocation needed because
		// the bytes live directly in the global, not behind a pointer.
		if dstt.IsArray() && dstt.Element != nil && dstt.Element.Name == "byte" {
			if len(v) > dstt.ArraySize {
				return nil, nil, fmt.Errorf("string literal of length %d does not fit in %s", len(v), dstt)
			}
			out := make([]byte, dstt.ArraySize)
			copy(out, v)
			return out, nil, nil
		}
		// byte[] (slice) destination: emit a 16-byte slice header.
		// First 8 bytes hold a pointer to a string constant (filled in
		// by the linker via the returned relocation). Last 8 bytes
		// hold the length, encoded little-endian here at compile time.
		if dstt.IsSlice() && dstt.Element != nil && dstt.Element.Name == "byte" {
			sym := c.String(v) // existing string-constants machinery
			out := make([]byte, 16)
			binary.LittleEndian.PutUint64(out[8:], uint64(len(v)))
			return out, []relocSpec{{Offset: 0, Symbol: sym, Addend: 0}}, nil
		}
		return nil, nil, fmt.Errorf("cannot initialize %s with a string literal", dstt)
	}
	return nil, nil, fmt.Errorf("unsupported literal type for static initializer: %T", l.Val)
}

// encodeAddressBytes handles `&...` in static-init context. Two forms:
//
//   - `&someGlobal` (a.Var set): the payload is an 8-byte zero slot
//     with a relocation pointing at the named global.
//
//   - `&someLiteral` (a.Lit set): recursively encode the inner literal,
//     queue an anonymous global with the encoded payload, and emit an
//     8-byte slot with a relocation pointing at the anonymous global.
//     Compositional: a literal containing another `&literal` produces
//     two anonymous globals, and so on, all queued in encoding order.
func encodeAddressBytes(c *Context, dstt ASTType, a *Address) ([]byte, []relocSpec, error) {
	// The destination must be pointer-sized to hold a relocated
	// address. Two valid shapes: a *T (Indirection > 0) or a
	// function-pointer (FuncSig != nil).
	if dstt.Indirection == 0 && dstt.FuncSig == nil {
		return nil, nil, fmt.Errorf("address-of initializer assigned to non-pointer type %s", dstt)
	}
	if a.Var != "" {
		// Function name: qualify with the current package so the
		// linker resolves it the same way it does for call sites.
		// Bare-name DataReloc symbols also get auto-qualified by the
		// linker, but emitting the qualified form here keeps the .bs
		// readable and avoids relying on that fallback.
		if decl, ok := c.FuncDeclForName(a.Var); ok {
			_ = decl
			pkg := c.Pkgname()
			out := make([]byte, 8)
			return out, []relocSpec{{Offset: 0, Symbol: pkg + "." + a.Var, Addend: 0}}, nil
		}
		out := make([]byte, 8)
		return out, []relocSpec{{Offset: 0, Symbol: a.Var, Addend: 0}}, nil
	}
	if a.Lit == nil {
		return nil, nil, fmt.Errorf("address-of with no target (internal: Address had neither Var nor Lit)")
	}
	// Address-of-index into a named global: `&globalArr[N]` for
	// compile-time-constant N. Encodes as a single relocation against
	// the array's symbol with Addend = N * elementSize. No anonymous
	// global needed; we're pointing into existing storage.
	if idx, ok := a.Lit.(*Index); ok {
		if sym, ok := idx.Val.(*Symbol); ok && idx.NAST == nil {
			et := idx.ASTType(c)
			elemSize := et.Size(c)
			out := make([]byte, 8)
			return out, []relocSpec{{
				Offset: 0,
				Symbol: sym.Name,
				Addend: int64(idx.N) * int64(elemSize),
			}}, nil
		}
		return nil, nil, fmt.Errorf("address-of-index in static init requires a named array and a compile-time-constant index")
	}
	// Address-of-literal: the inner is the value we want to give
	// storage to. Recursively encode it, then queue an anonymous
	// global of that type carrying the encoded bytes + relocs.
	innerType := a.Lit.ASTType(c)
	innerBytes, innerRelocs, err := encodeStaticInit(c, innerType, a.Lit)
	if err != nil {
		return nil, nil, fmt.Errorf("address-of-literal: %v", err)
	}
	name := c.AddAnonGlobal(innerType.String(), innerBytes, innerRelocs)
	out := make([]byte, 8)
	return out, []relocSpec{{Offset: 0, Symbol: name, Addend: 0}}, nil
}

// encodeArrayLiteralBytes serializes an array literal `[e1, e2, …]` for
// the destination type. Two destination shapes are supported:
//
//   - Fixed array `T[N]`: element count must match exactly; each
//     element encodes against T and the encoded bytes are concatenated.
//     Element relocations are shifted into the outer array's coordinates.
//
//   - Slice `T[]`: emit a 16-byte slice header. The backing storage is
//     a fresh anonymous fixed array `T[len(elements)]`, queued via
//     AddAnonGlobal; the header's ptr slot relocates to it, and the
//     length is encoded inline.
func encodeArrayLiteralBytes(c *Context, dstt ASTType, lit *ArrayLiteral) ([]byte, []relocSpec, error) {
	if dstt.Element == nil {
		return nil, nil, fmt.Errorf("array literal cannot initialize non-array type %s", dstt)
	}
	elemT := *dstt.Element
	switch {
	case dstt.IsArray():
		if dstt.ArraySize != len(lit.Elements) {
			return nil, nil, fmt.Errorf("array literal of length %d does not fit %s", len(lit.Elements), dstt)
		}
		return encodeArrayContent(c, elemT, lit.Elements)
	case dstt.IsSlice():
		backingBytes, backingRelocs, err := encodeArrayContent(c, elemT, lit.Elements)
		if err != nil {
			return nil, nil, err
		}
		backingT := ASTType{Element: &elemT, ArraySize: len(lit.Elements)}
		name := c.AddAnonGlobal(backingT.String(), backingBytes, backingRelocs)
		out := make([]byte, 16)
		binary.LittleEndian.PutUint64(out[8:], uint64(len(lit.Elements)))
		return out, []relocSpec{{Offset: 0, Symbol: name, Addend: 0}}, nil
	default:
		return nil, nil, fmt.Errorf("array literal cannot initialize %s (need a fixed-array or slice type)", dstt)
	}
}

// encodeArrayContent encodes a flat sequence of element expressions
// against a single element type. Shared by the fixed-array and slice
// branches of encodeArrayLiteralBytes (the slice branch uses it to
// build the anonymous backing array's bytes).
func encodeArrayContent(c *Context, elemT ASTType, elements []AST) ([]byte, []relocSpec, error) {
	elemSize := elemT.Size(c)
	var out []byte
	var relocs []relocSpec
	for i, e := range elements {
		eb, er, err := encodeStaticInit(c, elemT, e)
		if err != nil {
			return nil, nil, fmt.Errorf("element %d: %v", i, err)
		}
		if len(eb) != elemSize {
			return nil, nil, fmt.Errorf("element %d: encoded %d bytes, expected %d", i, len(eb), elemSize)
		}
		base := len(out)
		for _, r := range er {
			r.Offset += base
			relocs = append(relocs, r)
		}
		out = append(out, eb...)
	}
	return out, relocs, nil
}

// encodeStructLiteralBytes serializes a struct literal to bytes by walking
// the struct decl's field layout in declaration order, recursively encoding
// each field's initializer. Relocations from each field are offset-shifted
// by the field's position within the struct and concatenated into the
// returned reloc list.
//
// Requirements (clear errors on each):
//   - dstt names a struct visible in c.
//   - The literal's declared type name matches the destination's name.
//   - Every declared field has a matching entry in the literal (no
//     partial initializers — explicit beats implicit zero-init here).
//   - No extra fields appear in the literal that aren't in the decl.
//   - Every field's value is itself a compile-time constant supported
//     by encodeStaticInit.
func encodeStructLiteralBytes(c *Context, dstt ASTType, lit *StructLiteral) ([]byte, []relocSpec, error) {
	if dstt.Name != lit.Type.Name {
		return nil, nil, fmt.Errorf("struct literal type %s does not match destination type %s", lit.Type, dstt)
	}
	decl, ok := c.StructDeclForName(dstt.Name)
	if !ok {
		return nil, nil, fmt.Errorf("no such struct type %s", dstt.Name)
	}

	// Index literal fields by name so we can pull them in declaration order
	// regardless of source ordering, and detect extras after the walk.
	provided := make(map[string]AST, len(lit.Fields))
	for _, f := range lit.Fields {
		if _, dup := provided[f.Name]; dup {
			return nil, nil, fmt.Errorf("struct literal sets field %q more than once", f.Name)
		}
		provided[f.Name] = f.Val
	}

	var out []byte
	var relocs []relocSpec
	for _, fld := range decl.Fields {
		val, ok := provided[fld.Name]
		if !ok {
			if !fld.Type.ZeroInitializable(c) {
				return nil, nil, fmt.Errorf("struct literal is missing field %q of %s", fld.Name, dstt.Name)
			}
			out = append(out, make([]byte, fld.Type.Size(c))...)
			continue
		}
		fbytes, frelocs, err := encodeStaticInit(c, fld.Type, val)
		if err != nil {
			return nil, nil, fmt.Errorf("field %q: %v", fld.Name, err)
		}
		expected := fld.Type.Size(c)
		if len(fbytes) != expected {
			return nil, nil, fmt.Errorf("field %q: encoded %d bytes, expected %d", fld.Name, len(fbytes), expected)
		}
		// Shift each field's relocations into the outer payload's
		// coordinate system by adding the field's offset.
		base := len(out)
		for _, r := range frelocs {
			r.Offset += base
			relocs = append(relocs, r)
		}
		out = append(out, fbytes...)
		delete(provided, fld.Name)
	}
	for name := range provided {
		return nil, nil, fmt.Errorf("struct literal references unknown field %q of %s", name, dstt.Name)
	}
	return out, relocs, nil
}

// encodeIntBytes lays out n as little-endian bytes sized for dstt. The
// caller has already type-checked that dstt is an integer-shaped type;
// here we just need its width.
func encodeIntBytes(dstt ASTType, n int64) ([]byte, error) {
	// Compute the byte width directly from the type name so that any
	// alias-resolution / struct-field-lookup machinery isn't required.
	var width int
	switch dstt.Name {
	case "i8", "u8", "byte", "bool":
		width = 1
	case "i16", "u16":
		width = 2
	case "i32", "u32":
		width = 4
	case "i64", "u64":
		width = 8
	default:
		return nil, fmt.Errorf("integer initializer cannot be assigned to non-integer type %s", dstt)
	}
	out := make([]byte, 8)
	binary.LittleEndian.PutUint64(out, uint64(n))
	return out[:width], nil
}

// bytesToBasStringLiteral renders raw bytes as the payload of a bas string
// literal (the part between the surrounding quotes). Printable ASCII passes
// through directly; backslash and double-quote are backslash-escaped;
// everything else is emitted as \xHH so the bytes survive bas's lexer
// without ambiguity.
func bytesToBasStringLiteral(data []byte) string {
	var b strings.Builder
	for _, c := range data {
		switch {
		case c == '\\':
			b.WriteString(`\\`)
		case c == '"':
			b.WriteString(`\"`)
		case c >= 0x20 && c < 0x7f:
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, `\x%02x`, c)
		}
	}
	return b.String()
}
