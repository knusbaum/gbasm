package main

import (
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Spec tests for BABI-0 rules that are properties of the *instruction stream*
// rather than of a value some function returns: store widths, argument
// extension, and how an aggregate reaches its register.
//
// These assert on the emitted .bs for a whole compiled function rather than on
// an internal helper. That is deliberate. A helper-level test says nothing
// about a second site that does the same job, and that is exactly how the
// narrow-field store bug survived: `v.b = 5` (the assignment path) had always
// emitted a correctly-sized store, while struct-literal initialization emitted
// an oversized one, and no test looked at what actually came out.

// compileToBS runs the front end over a top-level body and returns the emitted
// .bs text.
func compileToBS(t *testing.T, body string) string {
	t.Helper()
	ctx := NewContext()
	ctx.SetPkgname("main")
	parser := NewParserAt("babi0_emit_test.bos", strings.NewReader(strings.TrimSpace(body)+"\n"), 1)
	var asts []AST
	for {
		n, err := parser.Next()
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if n == nil {
			break
		}
		a, err := n.ToAST(ctx)
		if err != nil {
			t.Fatalf("ToAST: %v", err)
		}
		if a != nil {
			asts = append(asts, a)
		}
	}
	var out bytes.Buffer
	for _, a := range asts {
		if err := Compile(&out, ctx, a); err != nil {
			t.Fatalf("compile: %v", err)
		}
	}
	return out.String()
}

var (
	// `local <name> <bits>` or `local <name> f32` / `f64`.
	localDeclRe = regexp.MustCompile(`(?m)^\s*local\s+(\S+)\s+(\S+)`)
	// `mov [<base>+<off>] <src>` / `movss` / `movsd`.
	storeRe = regexp.MustCompile(`(?m)^\s*(mov|movss|movsd)\s+\[(\w+)\+(\d+)\]\s+(\S+)\s*$`)
)

// localWidths maps each `local` name in the emitted .bs to its width in bits.
func localWidths(bs string) map[string]int {
	widths := map[string]int{}
	for _, m := range localDeclRe.FindAllStringSubmatch(bs, -1) {
		name, spec := m[1], m[2]
		switch spec {
		case "f32":
			widths[name] = 32
		case "f64":
			widths[name] = 64
		default:
			if n, err := strconv.Atoi(spec); err == nil {
				widths[name] = n
			}
		}
	}
	return widths
}

// A field store must be exactly as wide as the field. bas takes an unsized
// operand's width from its source, so the source register's width IS the
// store's width.
//
// Natural alignment inserts padding between some fields, which can absorb a
// modest overrun -- but only by luck of the layout, and never for a field at
// the end of a struct or one immediately followed by an aligned neighbour.
// This test does not rely on that: it pins each store to its field's exact
// width, so an oversized store fails whether or not padding would have hidden
// the damage at run time.
func TestBABI0StructLiteralStoreWidths(t *testing.T) {
	bs := compileToBS(t, `
type widths struct {
	a i8
	b i16
	c i32
	d i64
	e f32
	f byte
	g bool
}
fn main() i64 {
	v widths := widths{a: 1, b: 2, c: 3, d: 4, e: 5.0, f: 6, g: true}
	_ := v
	return 0
}`)

	// Naturally-aligned offsets: each field starts at the next offset
	// satisfying its own alignment. Cross-checked against gcc, which reports
	// the same 0/2/4/8/16/20/21 and size 24 for the equivalent C struct.
	wantWidthAtOffset := map[int]int{
		0:  8,  // a i8   @0
		2:  16, // b i16  @2  (2-aligned)
		4:  32, // c i32  @4  (4-aligned)
		8:  64, // d i64  @8  (8-aligned)
		16: 32, // e f32  @16 (4-aligned)
		20: 8,  // f byte @20
		21: 8,  // g bool @21
	}

	widths := localWidths(bs)
	seen := map[int]bool{}
	for _, m := range storeRe.FindAllStringSubmatch(bs, -1) {
		base, offStr, src := m[2], m[3], m[4]
		if base != "v" {
			continue
		}
		off, err := strconv.Atoi(offStr)
		if err != nil {
			continue
		}
		want, ok := wantWidthAtOffset[off]
		if !ok {
			t.Errorf("store into v+%d, which is not a field offset of `widths` (aligned offsets: 0,2,4,8,16,20,21)", off)
			continue
		}
		got, ok := widths[src]
		if !ok {
			t.Errorf("store into v+%d from %q, which is not a declared local", off, src)
			continue
		}
		if got != want {
			t.Errorf("store into v+%d uses a %d-bit source (%s); want %d-bit. A wider store runs past the field's own bytes.",
				off, got, src, want)
		}
		seen[off] = true
	}

	for off := range wantWidthAtOffset {
		if !seen[off] {
			t.Errorf("no store into v+%d; the literal initializes every field", off)
		}
	}
}

// The same rule for the assignment path, which has always been correct --
// pinned so a future refactor cannot regress it into the literal path's old
// behaviour.
func TestBABI0FieldAssignStoreWidth(t *testing.T) {
	bs := compileToBS(t, `
type pair struct {
	a i32
	b i32
}
fn main() i64 {
	var v pair := pair{a: 1, b: 2}
	v.b = 5
	_ := v
	return 0
}`)

	widths := localWidths(bs)
	found := false
	for _, m := range storeRe.FindAllStringSubmatch(bs, -1) {
		if m[2] != "v" || m[3] != "4" {
			continue
		}
		found = true
		if w := widths[m[4]]; w != 32 {
			t.Errorf("assignment to v.b stores from a %d-bit source (%s); want 32", w, m[4])
		}
	}
	if !found {
		t.Errorf("no store into v+4 found for `v.b = 5`")
	}
}

// BABI-0 § Function calls / Scalar arguments: a scalar narrower than 8 bytes
// is extended to the full 64-bit register -- sign-extended if the type is
// signed, zero-extended otherwise. This is stricter than System V, which
// leaves the upper bits unspecified.
func TestBABI0NarrowArgumentExtension(t *testing.T) {
	for _, tc := range []struct {
		name    string
		typ     string
		wantOp  string
		wrongOp string
	}{
		{"signed byte", "i8", "movsx", "movzx"},
		{"unsigned byte", "u8", "movzx", "movsx"},
		{"signed half", "i16", "movsx", "movzx"},
		{"unsigned half", "u16", "movzx", "movsx"},
		{"signed word", "i32", "movsx", "movzx"},
		{"unsigned word", "u32", "movzx", "movsx"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bs := compileToBS(t, fmt.Sprintf(`
fn takes(x %s) i64 { return i64(x) }
fn main() i64 {
	v %s := 1
	_ := takes(v)
	return 0
}`, tc.typ, tc.typ))

			// The extension targets the full 64-bit argument register, not a
			// sub-register: that is what makes the upper bits defined.
			want := regexp.MustCompile(`(?m)^\s*` + tc.wantOp + `\s+rdi\s+`)
			if !want.MatchString(bs) {
				t.Errorf("no `%s rdi ...` for a %s argument.\n%s", tc.wantOp, tc.typ, indentBS(bs))
			}
			wrong := regexp.MustCompile(`(?m)^\s*` + tc.wrongOp + `\s+rdi\s+`)
			if wrong.MatchString(bs) {
				t.Errorf("%s argument extended with %s; want %s (signedness decides)", tc.typ, tc.wrongOp, tc.wantOp)
			}
		})
	}
}

// BABI-0 § Function calls / Aggregates: an aggregate of size 1/2/4/8 is loaded
// into the argument register by value at its own width; anything larger is
// copied into a fresh caller slot whose ADDRESS goes in the register.
func TestBABI0AggregateArgumentPassing(t *testing.T) {
	t.Run("small aggregate passed by value", func(t *testing.T) {
		bs := compileToBS(t, `
type small struct {
	a i32
	b i32
}
fn takes(s small) i64 { return i64(s.a) }
fn main() i64 {
	v small := small{a: 1, b: 2}
	_ := takes(v)
	return 0
}`)
		// The 8-byte aggregate is loaded whole into rdi -- by value, as bits.
		if !regexp.MustCompile(`(?m)^\s*mov\s+rdi\s+qword\[`).MatchString(bs) {
			t.Errorf("expected an 8-byte aggregate to be loaded by value with `mov rdi qword[...]`\n%s", indentBS(bs))
		}
	})

	t.Run("large aggregate passed as address of a copy", func(t *testing.T) {
		bs := compileToBS(t, `
type big struct {
	a i64
	b i64
	c i64
}
fn takes(b big) i64 { return b.a }
fn main() i64 {
	v big := big{a: 1, b: 2, c: 3}
	_ := takes(v)
	return 0
}`)
		// A caller-side temp of the aggregate's size must be allocated: this
		// is the copy that preserves by-value semantics. Passing the caller's
		// own storage instead would let the callee mutate it.
		copyRe := regexp.MustCompile(`(?m)^\s*bytes\s+(Temp_\d+)\s+24\s*$`)
		m := copyRe.FindStringSubmatch(bs)
		if m == nil {
			t.Fatalf("no 24-byte caller-side copy allocated for a by-value 24-byte struct\n%s", indentBS(bs))
		}
		tmp := m[1]
		// ...and that copy's ADDRESS is what lands in the argument register.
		if !regexp.MustCompile(`(?m)^\s*inreg\s+` + tmp + `\s+rdi\s*$`).MatchString(bs) {
			t.Errorf("the 24-byte copy %s is not placed in rdi as an address\n%s", tmp, indentBS(bs))
		}
		// The copy must actually be filled from the source.
		if !strings.Contains(bs, "["+tmp+"+0]") && !strings.Contains(bs, "["+tmp+"]") {
			t.Errorf("the 24-byte copy %s is never written to\n%s", tmp, indentBS(bs))
		}
	})
}

func indentBS(bs string) string {
	var b strings.Builder
	for _, line := range strings.Split(bs, "\n") {
		b.WriteString("    | ")
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}
