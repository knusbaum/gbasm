package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"strings"
)

// emitGlobalVarDecl writes a top-level VarDecl as a bas-level global var
// directive. With no initializer, the global is zero-filled at its type's
// natural size. With an initializer, the value is encoded to raw bytes at
// compile time and emitted as a string-literal payload using \xHH escapes
// for non-printable bytes.
//
// Phase 1 accepts only pointer-free compile-time literals (integers, bytes,
// and the 0-N negation pattern). Anything that would need a relocation
// (slice headers, pointer initializers) is rejected; relocation support is
// scheduled for a follow-up phase.
func emitGlobalVarDecl(of io.Writer, c *Context, a AST, ast *VarDecl) {
	size := ast.Type.Size(c)
	if ast.Init == nil {
		// Zero-init form: bas allocates `size` zero bytes.
		fmt.Fprintf(of, "var %s %s %d\n", ast.Name, ast.Type, size)
		return
	}

	// Type-check the initializer against the declared type, mirroring the
	// function-local *VarDecl handler so the same diagnostics fire for
	// the same mistakes regardless of scope.
	srct := ast.Init.ASTType(c)
	dstt := ast.Type
	if !srct.Same(intlitASTType()) && !srct.Same(dstt) && !dstt.MutCompatible(srct) {
		CompileErrorF(a, "Cannot initialize %s with value of type %s", dstt, srct)
	}
	if ast.Type.HasOwned() {
		CompileErrorF(a, "Top-level vars cannot carry owned types")
	}

	data, err := encodeStaticInit(c, dstt, ast.Init)
	if err != nil {
		CompileErrorF(a, "%s", err.Error())
	}
	if len(data) != size {
		// Shouldn't happen if encodeStaticInit is honest, but check
		// rather than emit a globally-misaligned variable.
		CompileErrorF(a, "internal: static initializer encoded %d bytes, but type %s has size %d", len(data), dstt, size)
	}
	fmt.Fprintf(of, "var %s %s \"%s\"\n", ast.Name, ast.Type, bytesToBasStringLiteral(data))
}

// encodeStaticInit serializes a compile-time constant AST node into the raw
// byte payload that a global variable of type dstt should hold. Returns an
// error if init is not a recognized compile-time constant form.
//
// Supported forms:
//   - *Literal with uint64 Val for integer types (any width, any signedness)
//   - *Literal with byte Val for byte / 8-bit integer types
//   - *Op2{n_sub, *Literal{0}, *Literal{uint64}} for negative integers
//   - *StructLiteral for struct types, recursively, when every field's
//     initializer is itself a supported form
func encodeStaticInit(c *Context, dstt ASTType, init AST) ([]byte, error) {
	switch v := init.(type) {
	case *Literal:
		return encodeLiteralBytes(c, dstt, v)
	case *StructLiteral:
		return encodeStructLiteralBytes(c, dstt, v)
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
						return encodeIntBytes(dstt, -int64(nv))
					}
				}
			}
		}
		return nil, fmt.Errorf("initializer is not a compile-time constant")
	default:
		return nil, fmt.Errorf("initializer is not a compile-time constant")
	}
}

func encodeLiteralBytes(c *Context, dstt ASTType, l *Literal) ([]byte, error) {
	switch v := l.Val.(type) {
	case uint64:
		return encodeIntBytes(dstt, int64(v))
	case byte:
		size := dstt.Size(c)
		if size != 1 {
			return nil, fmt.Errorf("byte literal cannot initialize %s (size %d)", dstt, size)
		}
		return []byte{v}, nil
	case string:
		// String literals at top level would need a slice-header layout
		// with a pointer relocation into the string-constants area.
		// That's a Phase-2-with-relocations feature, not Phase 1.
		return nil, fmt.Errorf("string-literal initializers for top-level vars need pointer relocations (not yet implemented)")
	}
	return nil, fmt.Errorf("unsupported literal type for static initializer: %T", l.Val)
}

// encodeStructLiteralBytes serializes a struct literal to bytes by walking
// the struct decl's field layout in declaration order, recursively encoding
// each field's initializer.
//
// Requirements (clear errors on each):
//   - dstt names a struct visible in c.
//   - The literal's declared type name matches the destination's name.
//   - Every declared field has a matching entry in the literal (no
//     partial initializers — explicit beats implicit zero-init here).
//   - No extra fields appear in the literal that aren't in the decl.
//   - Every field's value is itself a compile-time constant supported
//     by encodeStaticInit.
func encodeStructLiteralBytes(c *Context, dstt ASTType, lit *StructLiteral) ([]byte, error) {
	if dstt.Name != lit.Type.Name {
		return nil, fmt.Errorf("struct literal type %s does not match destination type %s", lit.Type, dstt)
	}
	decl, ok := c.StructDeclForName(dstt.Name)
	if !ok {
		return nil, fmt.Errorf("no such struct type %s", dstt.Name)
	}

	// Index literal fields by name so we can pull them in declaration order
	// regardless of source ordering, and detect extras after the walk.
	provided := make(map[string]AST, len(lit.Fields))
	for _, f := range lit.Fields {
		if _, dup := provided[f.Name]; dup {
			return nil, fmt.Errorf("struct literal sets field %q more than once", f.Name)
		}
		provided[f.Name] = f.Val
	}

	var out []byte
	for _, fld := range decl.Fields {
		val, ok := provided[fld.Name]
		if !ok {
			return nil, fmt.Errorf("struct literal is missing field %q of %s", fld.Name, dstt.Name)
		}
		fbytes, err := encodeStaticInit(c, fld.Type, val)
		if err != nil {
			return nil, fmt.Errorf("field %q: %v", fld.Name, err)
		}
		expected := fld.Type.Size(c)
		if len(fbytes) != expected {
			return nil, fmt.Errorf("field %q: encoded %d bytes, expected %d", fld.Name, len(fbytes), expected)
		}
		out = append(out, fbytes...)
		delete(provided, fld.Name)
	}
	// Anything remaining in `provided` is an unrecognized field name.
	for name := range provided {
		return nil, fmt.Errorf("struct literal references unknown field %q of %s", name, dstt.Name)
	}
	return out, nil
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
