package main

import "testing"

// Spec tests for the Boson ABI, DESIGN.md § The Boson ABI (BABI-0).
//
// Every expected value here is a literal transcribed from that section --
// never a value the implementation computed. A test that derives its
// expectation by calling the same function it is checking (or a sibling of
// it) proves only self-consistency, which is precisely the failure mode
// these tests exist to break: bosc and bas can agree perfectly and both be
// wrong about the standard.
//
// So when one of these fails, the question is not "what changed in the code"
// but "does the code or the spec have it right" -- and one of the two must be
// edited deliberately.

// --- Data representation -------------------------------------------------

// BABI-0 § Data representation / Scalars.
func TestBABI0ScalarSizes(t *testing.T) {
	ctx := parseAndCompileForTest(t, `fn main() i64 { return 0 }`)
	for _, tc := range []struct {
		typ  ASTType
		want int
	}{
		{ASTType{Name: "i8"}, 1},
		{ASTType{Name: "u8"}, 1},
		{ASTType{Name: "byte"}, 1},
		{ASTType{Name: "bool"}, 1},
		{ASTType{Name: "i16"}, 2},
		{ASTType{Name: "u16"}, 2},
		{ASTType{Name: "i32"}, 4},
		{ASTType{Name: "u32"}, 4},
		{ASTType{Name: "f32"}, 4},
		{ASTType{Name: "i64"}, 8},
		{ASTType{Name: "u64"}, 8},
		{ASTType{Name: "f64"}, 8},
		// A pointer is 8 bytes whatever it points at, at any depth.
		{ASTType{Name: "i8", Indirection: 1}, 8},
		{ASTType{Name: "i64", Indirection: 3}, 8},
	} {
		typ := tc.typ
		if got := typ.Size(ctx); got != tc.want {
			t.Errorf("Size(%v) = %d, want %d (BABI-0 scalar table)", typ, got, tc.want)
		}
	}
}

// BABI-0 § Data representation / Alignment.
//
// This is the doc's own worked example, and it is deliberately a layout where
// packed and aligned disagree at every field after the first -- so the test
// distinguishes the two schemes rather than merely exercising one.
//
// The constants below are external ground truth, not our own output: they were
// read from gcc via offsetof/sizeof on the equivalent C declaration
//
//	struct layout { uint8_t a; uint32_t b; uint8_t c; uint64_t d; };
//
// which reports offsets 0/4/8/16 and size 24. That is the point of matching C
// here -- a self-consistent layout nobody else agrees with would be no use at
// an FFI boundary.
func TestBABI0StructLayout(t *testing.T) {
	ctx := parseAndCompileForTest(t, `
type layout struct {
	a u8
	b u32
	c u8
	d u64
}
fn main() i64 { return 0 }`)

	def, ok := structDeclForTestType(ctx, "layout")
	if !ok {
		t.Fatalf("struct 'layout' not found")
	}

	// Each field starts at the next offset satisfying its own alignment;
	// declaration order is preserved and nothing is reordered. A packed
	// layout would say 0/1/5/6 with size 14.
	for _, tc := range []struct {
		field string
		want  int
	}{
		{"a", 0},  // u8  at 0
		{"b", 4},  // u32 needs 4-alignment; 1..3 is padding
		{"c", 8},  // u8  immediately after b
		{"d", 16}, // u64 needs 8-alignment; 9..15 is padding
	} {
		got, ftype := def.ByteOffset(ctx, tc.field)
		if got != tc.want {
			t.Errorf("offsetof(layout.%s) = %d, want %d (natural alignment)", tc.field, got, tc.want)
		}
		if ftype.Name == "" {
			t.Errorf("layout.%s: no type resolved", tc.field)
		}
	}

	// Size includes tail padding up to the struct's own alignment, so that
	// consecutive elements of an array stay aligned.
	if got := def.Size(ctx); got != 24 {
		t.Errorf("sizeof(layout) = %d, want 24 (d ends at 24, already 8-aligned)", got)
	}
	if got := def.Alignment(ctx); got != 8 {
		t.Errorf("alignof(layout) = %d, want 8 (strictest field is u64)", got)
	}
}

// Tail padding is only observable on a struct whose last field is narrower
// than its alignment -- the case an implementation that pads *between* fields
// but not at the end would get wrong.
func TestBABI0StructTailPadding(t *testing.T) {
	ctx := parseAndCompileForTest(t, `
type tailpad struct {
	a u64
	b u8
}
fn main() i64 { return 0 }`)

	def, ok := structDeclForTestType(ctx, "tailpad")
	if !ok {
		t.Fatalf("struct 'tailpad' not found")
	}
	if got := def.Size(ctx); got != 16 {
		t.Errorf("sizeof(tailpad) = %d, want 16 (9 bytes of content rounded up to the 8-byte alignment)", got)
	}
}

// BABI-0 § Data representation: slices, interfaces, arrays, values.
func TestBABI0AggregateSizes(t *testing.T) {
	ctx := parseAndCompileForTest(t, `
interface stringer { str(self *self) []byte }
type holder struct { a i64 } {
	str(h *holder) []byte { _ := h; return "x" }
}
fn main() i64 { return 0 }`)

	// A slice is a 16-byte {ptr, len} header regardless of element type --
	// two words, which is what rules out a capacity field.
	sliceOfByte := byteSliceASTType()
	if got := sliceOfByte.Size(ctx); got != 16 {
		t.Errorf("sizeof([]byte) = %d, want 16 ({ptr@0, len@8}, no capacity)", got)
	}

	// An interface value is a 16-byte fat pointer however many methods the
	// interface declares.
	iface := ASTType{Name: "stringer"}
	if got := iface.Size(ctx); got != 16 {
		t.Errorf("sizeof(stringer) = %d, want 16 ({data@0, vtable@8})", got)
	}

	// A fixed array is N contiguous elements with no header and no padding.
	arr := ASTType{Name: "i32", ArraySize: 5, Element: &ASTType{Name: "i32"}}
	if got := arr.Size(ctx); got != 20 {
		t.Errorf("sizeof([5]i32) = %d, want 20 (5 * 4, no header)", got)
	}
}

// BABI-0 § Data representation: "Type qualifiers have no representation."
func TestBABI0QualifiersAreFree(t *testing.T) {
	ctx := parseAndCompileForTest(t, `fn main() i64 { return 0 }`)

	plain := ASTType{Name: "i64", Indirection: 1}
	owned := ASTType{Name: "i64", Indirection: 1, OwnedMask: 1}
	mut := ASTType{Name: "i64", Indirection: 1, MutMask: 1}
	nilable := ASTType{Name: "i64", Indirection: 1, NilMask: 1}

	want := plain.Size(ctx)
	for name, typ := range map[string]ASTType{
		"owned": owned,
		"mut":   mut,
		"?":     nilable,
	} {
		typ := typ
		if got := typ.Size(ctx); got != want {
			t.Errorf("%s qualifier changed size: got %d, want %d (qualifiers are compile-time only)", name, got, want)
		}
	}
}

// --- Argument classification ---------------------------------------------

// BABI-0 § Function calls / Argument classification.
func TestBABI0ArgumentRegisters(t *testing.T) {
	for _, tc := range []struct {
		name string
		decl string
		want []string
	}{
		{
			// The INTEGER sequence, in order.
			name: "all integer",
			decl: `fn f(a i64, b i64, c i64, d i64, e i64, g i64) i64 { return a + b + c + d + e + g }`,
			want: []string{"rdi", "rsi", "rdx", "rcx", "r8", "r9"},
		},
		{
			// The SSE sequence, in order.
			name: "all float",
			decl: `fn f(a f64, b f64, c f64, d f64, e f64, g f64, h f64, i f64) f64 { return a + b + c + d + e + g + h + i }`,
			want: []string{"xmm0", "xmm1", "xmm2", "xmm3", "xmm4", "xmm5", "xmm6", "xmm7"},
		},
		{
			// The discriminating case: the two classes are counted
			// independently, so a parameter's register comes from its
			// position among parameters OF ITS OWN CLASS. A single
			// position-indexed allocator would put `q` in xmm1 (second
			// parameter) instead of xmm0 (first float). Only an interleaved
			// signature can tell the two schemes apart.
			name: "interleaved classes",
			decl: `fn f(p i64, q f64, r i64, s f64) i64 { _ := q; _ := s; return p + r }`,
			want: []string{"rdi", "xmm0", "rsi", "xmm1"},
		},
		{
			// Pointers and bool/byte are INTEGER class.
			name: "pointers and small scalars are integer class",
			decl: `fn f(p *i64, b bool, c byte) i64 { _ := b; return *p + i64(c) }`,
			want: []string{"rdi", "rsi", "rdx"},
		},
		{
			// "Class is a property of raw scalars only": an aggregate is
			// INTEGER class no matter what it contains. A struct of two
			// f64 travels by address in an integer register, never in XMM.
			// This is BABI-0's deliberate divergence from System V's
			// per-eightbyte classification.
			name: "float-only struct is integer class",
			decl: `
type twofloats struct { x f64; y f64 }
fn f(a twofloats, b f64) i64 { return i64(a.x + a.y + b) }`,
			want: []string{"rdi", "xmm0"},
		},
		{
			// A slice is a 16-byte aggregate: integer class, by address.
			name: "slice is integer class",
			decl: `fn f(s []byte, x f64) i64 { return len(s) + i64(x) }`,
			want: []string{"rdi", "xmm0"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := parseAndCompileForTest(t, tc.decl+"\nfn main() i64 { return 0 }")
			fn, ok := lookupFuncDeclForTest(ctx, "f")
			if !ok {
				t.Fatalf("f not found")
			}
			got, err := argRegisters(ctx, fn, len(fn.Args))
			if err != nil {
				t.Fatalf("argRegisters: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("argRegisters = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("arg %d in %q, want %q (full: %v, want %v)", i, got[i], tc.want[i], got, tc.want)
				}
			}
		})
	}
}

// BABI-0 § Function calls / Argument count limits. The limits are exactly the
// register counts of each class, and exceeding either is a compile error
// rather than a spill to the stack.
func TestBABI0ArgumentLimits(t *testing.T) {
	for _, tc := range []struct {
		name    string
		decl    string
		wantErr string
	}{
		{
			name:    "seven integer arguments",
			decl:    `fn f(a i64, b i64, c i64, d i64, e i64, g i64, h i64) i64 { return a + b + c + d + e + g + h }`,
			wantErr: "more than 6 integer arguments are not supported",
		},
		{
			name:    "nine float arguments",
			decl:    `fn f(a f64, b f64, c f64, d f64, e f64, g f64, h f64, i f64, j f64) f64 { return a + b + c + d + e + g + h + i + j }`,
			wantErr: "more than 8 floating-point arguments are not supported",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := parseAndCompileForTest(t, tc.decl+"\nfn main() i64 { return 0 }")
			fn, ok := lookupFuncDeclForTest(ctx, "f")
			if !ok {
				t.Fatalf("f not found")
			}
			_, err := argRegisters(ctx, fn, len(fn.Args))
			if err == nil {
				t.Fatalf("argRegisters accepted %d arguments; want error %q", len(fn.Args), tc.wantErr)
			}
			if err.Error() != tc.wantErr {
				t.Errorf("error = %q, want %q", err.Error(), tc.wantErr)
			}
		})
	}

	// The boundaries themselves must be accepted: exactly 6 integer and
	// exactly 8 float arguments are legal.
	ctx := parseAndCompileForTest(t, `
fn six(a i64, b i64, c i64, d i64, e i64, g i64) i64 { return a + b + c + d + e + g }
fn eight(a f64, b f64, c f64, d f64, e f64, g f64, h f64, i f64) f64 { return a + b + c + d + e + g + h + i }
fn main() i64 { return 0 }`)
	for _, name := range []string{"six", "eight"} {
		fn, ok := lookupFuncDeclForTest(ctx, name)
		if !ok {
			t.Fatalf("%s not found", name)
		}
		if _, err := argRegisters(ctx, fn, len(fn.Args)); err != nil {
			t.Errorf("argRegisters(%s) = %v, want success at the boundary", name, err)
		}
	}
}

// --- Return values -------------------------------------------------------

// BABI-0 § Function calls / Return values.
func TestBABI0ReturnRegisters(t *testing.T) {
	for _, tc := range []struct {
		typ  ASTType
		want string
	}{
		// Floats return in XMM0.
		{ASTType{Name: "f32"}, "xmm0"},
		{ASTType{Name: "f64"}, "xmm0"},
		// Narrow scalars return in the correspondingly-sized sub-register.
		{ASTType{Name: "i8"}, "al"},
		{ASTType{Name: "u8"}, "al"},
		{ASTType{Name: "byte"}, "al"},
		{ASTType{Name: "bool"}, "al"},
		{ASTType{Name: "i16"}, "ax"},
		{ASTType{Name: "u16"}, "ax"},
		{ASTType{Name: "i32"}, "eax"},
		{ASTType{Name: "u32"}, "eax"},
		{ASTType{Name: "i64"}, "rax"},
		{ASTType{Name: "u64"}, "rax"},
		// Pointers return in RAX.
		{ASTType{Name: "i64", Indirection: 1}, "rax"},
		// Aggregates return an ADDRESS in RAX.
		{byteSliceASTType(), "rax"},
	} {
		if got := raxForType(tc.typ); got != tc.want {
			t.Errorf("raxForType(%v) = %q, want %q (BABI-0 return table)", tc.typ, got, tc.want)
		}
	}
}

// structDeclForTestType resolves a struct declared in test source, tolerating
// whether the context stores it bare or package-qualified.
func structDeclForTestType(ctx *Context, name string) (*StructDecl, bool) {
	if def, ok := structDeclForType(ctx, ASTType{Name: name}); ok {
		return def, true
	}
	return structDeclForType(ctx, ASTType{Name: "main." + name})
}
