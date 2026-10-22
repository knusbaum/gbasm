// Package arm64 lowers Boson metaassembly to native Apple ARM64 assembly.
// Named locals have stable frame slots; explicit inreg bindings are temporary
// caches flushed at calls, labels, and evictions. x16/x17 are reserved scratch.
package arm64

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/knusbaum/gbasm"
	"github.com/knusbaum/gbasm/macho"
)

var savedPairs = []string{"x19, x20", "x21, x22", "x23, x24", "x25, x26", "x27, x28", "d8, d9", "d10, d11", "d12, d13", "d14, d15"}

type slot struct {
	offset, bits, size int
	address, volatile  bool
	reg                string
}
type function struct {
	o       *gbasm.OFile
	name    string
	lines   []string
	slots   map[string]*slot
	frame   int
	out     strings.Builder
	args    []string
	labels  map[string]bool
	started bool
	pending []string
}

func symbol(pkg, name string) string {
	if !strings.Contains(name, ".") {
		name = pkg + "." + name
	}
	return "_" + name
}
func sorted[V any](m map[string]V) []string {
	var n []string
	for k := range m {
		n = append(n, k)
	}
	sort.Strings(n)
	return n
}

// Assemble retains metadata in o and attaches a native relocatable image.
func Assemble(o *gbasm.OFile, bodies map[string][]string) error {
	source, err := Lower(o, bodies)
	if err != nil {
		return err
	}
	image, err := macho.AssembleARM64(source)
	if err != nil {
		return err
	}
	o.NativeObject = image
	return nil
}

// Lower expands storage, frame and instruction pseudo-ops without invoking any
// platform tools. Its output is native ARM64 assembly with Mach-O directives.
func Lower(o *gbasm.OFile, bodies map[string][]string) (string, error) {
	var out strings.Builder
	out.WriteString(".build_version macos, 11, 0\n.subsections_via_symbols\n")
	for _, group := range []map[string]*gbasm.Var{o.Data, o.Vars} {
		// Pointer-bearing records must live in a relocatable, writable data segment
		// under PIE; __DATA_CONST can be adopted when readonly fixups are supported.
		out.WriteString(".data\n")
		for _, n := range sorted(group) {
			v := group[n]
			fmt.Fprintf(&out, ".p2align 3\n.globl %s\n%s:\n", "_"+o.Pkgname+"."+n, "_"+o.Pkgname+"."+n)
			rel := map[int]gbasm.DataReloc{}
			for _, r := range v.Relocs {
				rel[int(r.Offset)] = r
			}
			for i := 0; i < len(v.Val); {
				if r, ok := rel[i]; ok {
					fmt.Fprintf(&out, ".quad %s%+d\n", symbol(o.Pkgname, r.Symbol), r.Addend)
					i += 8
				} else {
					fmt.Fprintf(&out, ".byte %d\n", v.Val[i])
					i++
				}
			}
		}
	}
	out.WriteString(".text\n")
	for _, n := range sorted(bodies) {
		f := &function{o: o, name: n, lines: bodies[n], slots: map[string]*slot{}, labels: map[string]bool{}}
		if err := f.compile(); err != nil {
			return "", fmt.Errorf("function %s: %w", n, err)
		}
		fmt.Fprintf(&out, ".p2align 2\n.globl %s\n%s:\n%s", "_"+o.Pkgname+"."+n, "_"+o.Pkgname+"."+n, f.out.String())
	}
	return out.String(), nil
}
func (f *function) emit(format string, args ...any) { fmt.Fprintf(&f.out, format+"\n", args...) }
func fields(line string) []string {
	line = strings.SplitN(line, "//", 2)[0]
	return strings.Fields(strings.ReplaceAll(line, ",", " "))
}
func (f *function) alloc(name string, bits, size int, address bool) error {
	if old, ok := f.slots[name]; ok {
		// Lexically disjoint bindings may reuse the same source name. Reserve
		// enough storage for every declaration; declaration emission sets its kind.
		if size > old.size {
			f.frame = (f.frame + 7) &^ 7
			f.frame += (max(size, 8) + 7) &^ 7
			old.offset = f.frame
			old.size = size
		}
		return nil
	}
	if size < 0 || size > 1<<24 {
		return fmt.Errorf("invalid storage size %d", size)
	}
	f.frame = (f.frame + 7) &^ 7
	f.frame += (max(size, 8) + 7) &^ 7
	f.slots[name] = &slot{offset: f.frame, bits: bits, size: size, address: address}
	return nil
}
func (f *function) compile() error {
	// All slots are reserved before emission, including declarations in branches.
	// Forget directives do not reuse storage in this conservative implementation.
	for _, line := range f.lines {
		p := fields(line)
		if len(p) == 0 {
			continue
		}
		if len(p) == 1 && strings.HasSuffix(p[0], ":") {
			p = []string{"label", strings.TrimSuffix(p[0], ":")}
		}
		switch p[0] {
		case "label":
			if len(p) != 2 {
				return fmt.Errorf("invalid label")
			}
			if f.labels[p[1]] {
				return fmt.Errorf("duplicate label %s", p[1])
			}
			f.labels[p[1]] = true
		case "local", "bytes", "argi", "arg":
			if len(p) < 3 {
				return fmt.Errorf("invalid %s directive", p[0])
			}
			bits, size := 64, 8
			address := p[0] == "bytes"
			if p[0] == "local" || address {
				n, err := strconv.Atoi(p[2])
				if err != nil {
					if p[2] == "f32" {
						n = 32
					} else if p[2] == "f64" {
						n = 64
					} else {
						return err
					}
				}
				if address {
					size = n
				} else {
					bits = n
					size = (n + 7) / 8
				}
			} else if len(p) > 3 {
				if p[3] == "f32" {
					bits = 32
				} else if p[3] == "f64" {
					bits = 64
				} else {
					n, err := strconv.Atoi(p[3])
					if err != nil {
						return err
					}
					bits = n
				}
			}
			if (p[0] == "argi" || p[0] == "arg") && bits > 64 {
				bits = 64
			}
			if !address && bits != 8 && bits != 16 && bits != 32 && bits != 64 {
				return fmt.Errorf("unsupported width %d", bits)
			}
			if err := f.alloc(p[1], bits, size, address); err != nil {
				return err
			}
			if p[0] == "argi" || p[0] == "arg" {
				f.args = append(f.args, line)
			}
		}
	}
	f.frame = (f.frame + 15) &^ 15
	for _, line := range f.lines {
		if err := f.line(line); err != nil {
			return fmt.Errorf("%s: %w", line, err)
		}
	}
	if len(f.pending) > 0 {
		return fmt.Errorf("named storage requires prologue")
	}
	return nil
}
func isreg(s string) bool {
	if s == "sp" || s == "xzr" || s == "wzr" {
		return true
	}
	if len(s) < 2 {
		return false
	}
	if !strings.ContainsRune("xwsd", rune(s[0])) {
		return false
	}
	n, err := strconv.Atoi(s[1:])
	return err == nil && n >= 0 && n <= 31
}
func isfloatreg(r string) bool {
	return len(r) > 1 && r[1] >= '0' && r[1] <= '9' && (r[0] == 's' || r[0] == 'd') && isreg(r)
}
func widthreg(r string, bits int) string {
	if bits == 32 && strings.HasPrefix(r, "d") && isfloatreg(r) {
		return "s" + r[1:]
	}
	if bits == 64 && strings.HasPrefix(r, "s") && isfloatreg(r) {
		return "d" + r[1:]
	}
	if bits <= 32 && strings.HasPrefix(r, "x") {
		return "w" + r[1:]
	}
	if bits > 32 && strings.HasPrefix(r, "w") {
		return "x" + r[1:]
	}
	return r
}
func operandWidth(s string, slots map[string]*slot) int {
	for _, p := range []struct {
		s string
		n int
	}{{"qword", 64}, {"dword", 32}, {"word", 16}, {"byte", 8}} {
		if strings.HasPrefix(s, p.s+"[") {
			return p.n
		}
	}
	if name, n, ok := strings.Cut(s, ":"); ok {
		if bits, e := strconv.Atoi(n); e == nil && slots[name] != nil {
			return bits
		}
	}
	if l := slots[s]; l != nil {
		return l.bits
	}
	if strings.HasPrefix(s, "w") && isreg(s) {
		return 32
	}
	return 64
}
func (f *function) immediate(r string, v uint64) {
	f.emit("movz %s, #%d", r, v&65535)
	for i := 1; i < 4; i++ {
		if x := (v >> uint(i*16)) & 65535; x != 0 {
			f.emit("movk %s, #%d, lsl #%d", r, x, i*16)
		}
	}
}
func (f *function) slotaddr(r string, s *slot) {
	f.immediate(r, uint64(s.offset))
	f.emit("sub %s, x29, %s", r, r)
}
func (f *function) globaladdr(r, name string) {
	sym := symbol(f.o.Pkgname, name)
	f.emit("adrp %s, %s@PAGE", r, sym)
	f.emit("add %s, %s, %s@PAGEOFF", r, r, sym)
}
func loadop(bits int) string {
	switch bits {
	case 8:
		return "ldrb"
	case 16:
		return "ldrh"
	}
	return "ldr"
}
func storeop(bits int) string {
	switch bits {
	case 8:
		return "strb"
	case 16:
		return "strh"
	}
	return "str"
}
func (f *function) flush(reg string) {
	for _, n := range sorted(f.slots) {
		s := f.slots[n]
		if s.reg != "" && (reg == "" || reg == s.reg) {
			if s.address {
				s.reg = ""
				continue
			}
			f.slotaddr("x17", s)
			f.emit("%s %s, [x17]", storeop(s.bits), widthreg(s.reg, s.bits))
			s.reg = ""
		}
	}
}
func (f *function) read(src, r string, bits int) error {
	if isfloatreg(r) {
		if err := f.read(src, "x16", bits); err != nil {
			return err
		}
		f.emit("fmov %s, %s", widthreg(r, bits), widthreg("x16", bits))
		return nil
	}
	r = widthreg(r, 64)
	src = strings.TrimPrefix(src, "#")
	if isreg(src) {
		if src != r {
			op := "mov"
			if isfloatreg(src) {
				op = "fmov"
			}
			f.emit("%s %s, %s", op, widthreg(r, bits), widthreg(src, bits))
		}
		return nil
	}
	base := src
	if name, _, ok := strings.Cut(src, ":"); ok {
		base = name
	}
	if s := f.slots[base]; s != nil {
		if !s.address && bits > s.bits {
			bits = s.bits
		}
		if s.address {
			f.slotaddr(r, s)
			return nil
		}
		if s.reg != "" {
			op := "mov"
			if isfloatreg(s.reg) {
				op = "fmov"
			}
			f.emit("%s %s, %s", op, widthreg(r, bits), widthreg(s.reg, bits))
			return nil
		}
		f.slotaddr(r, s)
		f.emit("%s %s, [%s]", loadop(bits), widthreg(r, bits), r)
		return nil
	}
	if strings.Contains(src, "[") {
		if err := f.address(src, r); err != nil {
			return err
		}
		f.emit("%s %s, [%s]", loadop(bits), widthreg(r, bits), r)
		return nil
	}
	if n, e := strconv.ParseInt(src, 0, 64); e == nil {
		f.immediate(r, uint64(n))
		return nil
	}
	if n, e := strconv.ParseUint(src, 0, 64); e == nil {
		f.immediate(r, n)
		return nil
	}
	f.globaladdr(r, src)
	f.emit("%s %s, [%s]", loadop(bits), widthreg(r, bits), r)
	return nil
}
func (f *function) write(dst, r string, bits int) error {
	if isreg(dst) {
		if dst != r {
			op := "mov"
			if isfloatreg(dst) {
				op = "fmov"
			}
			f.emit("%s %s, %s", op, widthreg(dst, bits), widthreg(r, bits))
		}
		return nil
	}
	base := dst
	if name, _, ok := strings.Cut(dst, ":"); ok {
		base = name
	}
	if s := f.slots[base]; s != nil {
		if s.address {
			return fmt.Errorf("cannot assign to bytes address %s", base)
		}
		if s.reg != "" {
			op := "mov"
			if isfloatreg(s.reg) {
				op = "fmov"
			}
			f.emit("%s %s, %s", op, widthreg(s.reg, bits), widthreg(r, bits))
		}
		f.slotaddr("x17", s)
		f.emit("%s %s, [x17]", storeop(bits), widthreg(r, bits))
		return nil
	}
	if !strings.Contains(dst, "[") {
		f.globaladdr("x17", dst)
		f.emit("%s %s, [x17]", storeop(bits), widthreg(r, bits))
		return nil
	}
	if err := f.address(dst, "x17"); err != nil {
		return err
	}
	f.emit("%s %s, [x17]", storeop(bits), widthreg(r, bits))
	return nil
}

// address handles Boson's base+index*scale addressing, independent of native
// load/store immediate restrictions. x14 is reserved for address arithmetic.
func (f *function) address(src, r string) error {
	start := strings.IndexByte(src, '[')
	end := strings.LastIndexByte(src, ']')
	if start < 0 {
		if s := f.slots[src]; s != nil {
			f.slotaddr(r, s)
		} else {
			f.globaladdr(r, src)
		}
		return nil
	}
	if end < start {
		return fmt.Errorf("invalid address %s", src)
	}
	expr := src[start+1 : end]
	expr = strings.ReplaceAll(expr, "-", "+-")
	var parts []string
	depth, startPart := 0, 0
	for i, c := range expr {
		switch c {
		case '[':
			depth++
		case ']':
			depth--
		case '+':
			if depth == 0 {
				parts = append(parts, expr[startPart:i])
				startPart = i + 1
			}
		}
	}
	parts = append(parts, expr[startPart:])
	if len(parts) == 0 {
		return fmt.Errorf("empty address")
	}
	if s := f.slots[parts[0]]; s != nil && s.address {
		f.slotaddr(r, s)
	} else if f.slots[parts[0]] == nil && !isreg(parts[0]) && !strings.Contains(parts[0], "[") {
		f.globaladdr(r, parts[0])
	} else if err := f.read(parts[0], r, 64); err != nil {
		return err
	}
	for _, p := range parts[1:] {
		if p == "" {
			continue
		}
		if n, e := strconv.ParseInt(p, 0, 64); e == nil {
			f.immediate("x14", uint64(n))
		} else {
			ix, scale, ok := strings.Cut(p, "*")
			if !ok {
				ix = p
				scale = "1"
			}
			if err := f.read(ix, "x14", 64); err != nil {
				return err
			}
			n, e := strconv.Atoi(scale)
			if e != nil || (n != 1 && n != 2 && n != 4 && n != 8) {
				return fmt.Errorf("invalid address scale %s", scale)
			}
			shift := 0
			for n > 1 {
				shift++
				n /= 2
			}
			if shift > 0 {
				f.emit("lsl x14, x14, #%d", shift)
			}
		}
		f.emit("add %s, %s, x14", r, r)
	}
	return nil
}

func (f *function) line(line string) error {
	p := fields(line)
	if len(p) == 0 {
		return nil
	}
	op := strings.ToLower(p[0])
	a := p[1:]
	if len(a) == 0 && strings.HasSuffix(op, ":") {
		return f.line("label " + strings.TrimSuffix(p[0], ":"))
	}
	need := func(n int) error {
		if len(a) != n {
			return fmt.Errorf("%s requires %d operands", op, n)
		}
		return nil
	}
	switch op {
	case "local", "bytes":
		if !f.started {
			f.pending = append(f.pending, line)
			return nil
		}
		s := f.slots[a[0]]
		s.reg = ""
		s.volatile = false
		s.address = op == "bytes"
		if !s.address {
			n, e := strconv.Atoi(a[1])
			if e != nil {
				if a[1] == "f32" {
					n = 32
				} else {
					n = 64
				}
			}
			s.bits = n
		}
		f.slotaddr("x17", s)
		if s.address {
			f.immediate("x16", uint64((s.size+7)&^7))
			if s.size > 0 {
				label := fmt.Sprintf("Lzero_%s_%d", f.name, f.out.Len())
				f.emit("%s:", label)
				f.emit("str xzr, [x17], #8")
				f.emit("sub x16, x16, #8")
				f.emit("cbnz x16, %s", label)
			}
		} else {
			f.emit("str xzr, [x17]")
		}
		if len(a) == 3 {
			f.flush(a[2])
			s.reg = a[2]
			if s.address {
				f.slotaddr(a[2], s)
			}
		}
		return nil
	case "argi", "arg", "type", "retaliases":
		return nil
	case "prologue":
		f.started = true
		f.emit("stp x29, x30, [sp, #-16]!")
		f.emit("mov x29, sp")
		f.immediate("x16", uint64(f.frame))
		f.emit("sub sp, sp, x16")
		// Save the callee-saved registers used by the compiler in a separate area.
		for _, pair := range savedPairs {
			f.emit("stp %s, [sp, #-16]!", pair)
		}
		for _, line := range f.args {
			q := fields(line)
			s := f.slots[q[1]]
			var reg string
			if q[0] == "arg" && isreg(q[2]) {
				reg = q[2]
			} else {
				idx, e := strconv.Atoi(q[2])
				if e != nil || idx < 0 {
					return fmt.Errorf("invalid argument index")
				}
				if len(q) > 3 && strings.HasPrefix(q[3], "f") {
					if idx >= 8 {
						return fmt.Errorf("too many floating arguments")
					}
					reg = fmt.Sprintf("%s%d", map[int]string{32: "s", 64: "d"}[s.bits], idx)
				} else if q[0] == "arg" {
					f.emit("ldr x16, [x29, #%d]", idx)
					reg = "x16"
				} else if idx < 6 {
					reg = fmt.Sprintf("x%d", idx)
				} else {
					f.emit("ldr x16, [x29, #%d]", 16+(idx-6)*16)
					reg = "x16"
				}
			}
			f.slotaddr("x17", s)
			f.emit("%s %s, [x17]", storeop(s.bits), widthreg(reg, s.bits))
		}
		for _, decl := range f.pending {
			if err := f.line(decl); err != nil {
				return err
			}
		}
		f.pending = nil
		return nil
	case "epilogue":
		f.flush("")
		f.immediate("x16", uint64(f.frame+16*len(savedPairs)))
		f.emit("sub sp, x29, x16")
		for i := len(savedPairs) - 1; i >= 0; i-- {
			f.emit("ldp %s, [sp], #16", savedPairs[i])
		}
		f.emit("mov sp, x29")
		f.emit("ldp x29, x30, [sp], #16")
		return nil
	case "ret":
		if len(a) > 1 {
			return fmt.Errorf("ret takes at most one register")
		}
		if len(a) == 0 {
			f.emit("ret")
		} else {
			f.emit("ret %s", a[0])
		}
		return nil
	case "label":
		if err := need(1); err != nil {
			return err
		}
		f.flush("")
		f.emit("L%s_%s:", f.name, a[0])
		return nil
	case "evict":
		if len(a) == 0 {
			f.flush("")
		} else {
			for _, r := range a {
				f.flush(r)
			}
		}
		return nil
	case "acquire", "release", "use":
		for _, r := range a {
			if !isreg(r) {
				return fmt.Errorf("invalid register %s", r)
			}
			f.flush(r)
		}
		return nil
	case "forget":
		if err := need(1); err != nil {
			return err
		}
		if s := f.slots[a[0]]; s != nil {
			s.reg = ""
		}
		return nil
	case "forgetall":
		for _, s := range f.slots {
			s.reg = ""
		}
		return nil
	case "volatile":
		if err := need(1); err != nil {
			return err
		}
		s := f.slots[a[0]]
		if s == nil {
			return fmt.Errorf("unknown local %s", a[0])
		}
		if s.reg != "" {
			f.flush(s.reg)
		}
		s.volatile = true
		return nil
	case "inreg":
		if err := need(2); err != nil {
			return err
		}
		s := f.slots[a[0]]
		if s == nil {
			return fmt.Errorf("unknown local %s", a[0])
		}
		if s.volatile {
			return fmt.Errorf("cannot put volatile local %s in a register", a[0])
		}
		f.flush(a[1])
		if err := f.read(a[0], a[1], s.bits); err != nil {
			return err
		}
		s.reg = a[1]
		return nil
	case "call":
		if err := need(1); err != nil {
			return err
		}
		f.flush("")
		if isreg(a[0]) {
			f.emit("blr %s", a[0])
		} else if f.slots[a[0]] != nil {
			if err := f.read(a[0], "x16", 64); err != nil {
				return err
			}
			f.emit("blr x16")
		} else {
			f.emit("bl %s", symbol(f.o.Pkgname, a[0]))
		}
		f.emit("mov x8, x0")
		return nil
	case "push":
		if err := need(1); err != nil {
			return err
		}
		if err := f.read(a[0], "x16", 64); err != nil {
			return err
		}
		f.emit("str x16, [sp, #-16]!")
		return nil
	case "pop":
		if err := need(1); err != nil {
			return err
		}
		f.emit("ldr x16, [sp], #16")
		return f.write(a[0], "x16", 64)
	case "lea":
		if err := need(2); err != nil {
			return err
		}
		if err := f.address(a[1], "x16"); err != nil {
			return err
		}
		return f.write(a[0], "x16", 64)
	case "mov", "movb", "movzx", "movsx", "movsxd", "movzx8", "movzx16", "movsx8", "movsx16", "movq", "movd":
		if err := need(2); err != nil {
			return err
		}
		bits := operandWidth(a[0], f.slots)
		if strings.Contains(a[0], "[") && bits == 64 {
			bits = operandWidth(a[1], f.slots)
		}
		if op == "movq" {
			bits = 64
		}
		if op == "movd" {
			bits = 32
		}
		if strings.HasSuffix(op, "8") {
			bits = 8
		}
		if strings.HasSuffix(op, "16") {
			bits = 16
		}
		if op == "movb" {
			bits = 8
		}
		if op == "movzx" || op == "movsx" || op == "movsxd" {
			bits = operandWidth(a[1], f.slots)
			if op == "movsxd" {
				bits = 32
			}
		}
		if err := f.read(a[1], "x16", bits); err != nil {
			return err
		}
		if strings.HasPrefix(op, "movsx") {
			f.emit("sxt%s x16, w16", map[int]string{8: "b", 16: "h", 32: "w"}[bits])
		}
		if strings.HasPrefix(op, "movzx") || strings.HasPrefix(op, "movsx") {
			bits = operandWidth(a[0], f.slots)
		}
		return f.write(a[0], "x16", bits)
	case "movss", "movsd", "addss", "addsd", "subss", "subsd", "mulss", "mulsd", "divss", "divsd", "ucomiss", "ucomisd", "cvttss2si", "cvttsd2si", "cvtsi2ss", "cvtsi2sd", "cvtss2sd", "cvtsd2ss":
		if err := need(2); err != nil {
			return err
		}
		bits := 64
		if strings.HasSuffix(op, "ss") || op == "cvttss2si" || op == "cvtss2sd" {
			bits = 32
		}
		if op == "movss" || op == "movsd" {
			if err := f.read(a[1], "x16", bits); err != nil {
				return err
			}
			return f.write(a[0], "x16", bits)
		}
		fr := widthreg("d16", bits)
		fs := widthreg("d17", bits)
		if strings.HasPrefix(op, "cvtt") {
			if err := f.read(a[1], fr, bits); err != nil {
				return err
			}
			f.emit("fcvtzs x16, %s", fr)
			return f.write(a[0], "x16", operandWidth(a[0], f.slots))
		}
		if strings.HasPrefix(op, "cvtsi") {
			if err := f.read(a[1], "x16", 64); err != nil {
				return err
			}
			f.emit("scvtf %s, x16", fr)
			f.emit("fmov %s, %s", widthreg("x16", bits), fr)
			return f.write(a[0], "x16", bits)
		}
		if op == "cvtss2sd" || op == "cvtsd2ss" {
			if err := f.read(a[1], fr, bits); err != nil {
				return err
			}
			destbits := 96 - bits
			fd := widthreg("d17", destbits)
			f.emit("fcvt %s, %s", fd, fr)
			f.emit("fmov %s, %s", widthreg("x16", destbits), fd)
			return f.write(a[0], "x16", destbits)
		}
		if err := f.read(a[0], fr, bits); err != nil {
			return err
		}
		if err := f.read(a[1], fs, bits); err != nil {
			return err
		}
		if strings.HasPrefix(op, "ucomi") {
			f.emit("fcmp %s, %s", fr, fs)
			// Boson's existing comparison lowering expects unordered to set the
			// equal, below and parity conditions simultaneously, as UCOMIS does.
			f.emit("mrs x15, nzcv")
			f.immediate("x14", 0x50000000)
			f.emit("csel x15, x15, x14, vc")
			f.emit("msr nzcv, x15")
			return nil
		}
		native := "f" + op[:3]
		f.emit("%s %s, %s, %s", native, fr, fr, fs)
		f.emit("fmov %s, %s", widthreg("x16", bits), fr)
		return f.write(a[0], "x16", bits)
	case "cmp", "test":
		if err := need(2); err != nil {
			return err
		}
		bits := operandWidth(a[0], f.slots)
		if err := f.read(a[0], "x16", bits); err != nil {
			return err
		}
		if err := f.read(a[1], "x15", bits); err != nil {
			return err
		}
		if op == "cmp" && bits < 32 {
			suffix := map[int]string{8: "b", 16: "h"}[bits]
			f.emit("sxt%s w16, w16", suffix)
			f.emit("sxt%s w15, w15", suffix)
		}
		native := "cmp"
		if op == "test" {
			native = "tst"
		}
		f.emit("%s %s, %s", native, widthreg("x16", bits), widthreg("x15", bits))
		return nil
	case "add", "sub", "and", "or", "xor", "imul", "shl", "shr", "sar":
		if op == "imul" && len(a) == 1 {
			if err := f.read(a[0], "x15", 64); err != nil {
				return err
			}
			f.emit("smulh x2, x8, x15")
			f.emit("mul x8, x8, x15")
			return nil
		}
		if len(a) == 1 && (op == "shl" || op == "shr" || op == "sar") {
			a = append(a, "x3")
		}
		if len(a) == 3 {
			f.emit("%s %s", op, strings.Join(a, ", "))
			return nil
		}
		if err := need(2); err != nil {
			return err
		}
		if a[0] == "sp" {
			n, e := strconv.Atoi(a[1])
			if e != nil {
				return fmt.Errorf("stack adjustment requires constant")
			}
			f.immediate("x16", uint64(n*2))
			f.emit("%s sp, sp, x16", op)
			return nil
		}
		bits := operandWidth(a[0], f.slots)
		if err := f.read(a[0], "x16", bits); err != nil {
			return err
		}
		if err := f.read(a[1], "x15", bits); err != nil {
			return err
		}
		native := map[string]string{"or": "orr", "xor": "eor", "imul": "mul", "shl": "lsl", "shr": "lsr", "sar": "asr"}[op]
		if native == "" {
			native = op
		}
		f.emit("%s %s, %s, %s", native, widthreg("x16", bits), widthreg("x16", bits), widthreg("x15", bits))
		return f.write(a[0], "x16", bits)
	case "neg", "not", "inc", "dec":
		if len(a) == 2 {
			f.emit("%s %s", op, strings.Join(a, ", "))
			return nil
		}
		if err := need(1); err != nil {
			return err
		}
		bits := operandWidth(a[0], f.slots)
		if err := f.read(a[0], "x16", bits); err != nil {
			return err
		}
		r := widthreg("x16", bits)
		switch op {
		case "neg":
			f.emit("neg %s, %s", r, r)
		case "not":
			f.emit("mvn %s, %s", r, r)
		case "inc":
			f.emit("add %s, %s, #1", r, r)
		case "dec":
			f.emit("sub %s, %s, #1", r, r)
		}
		return f.write(a[0], "x16", bits)
	case "mul":
		if len(a) == 3 {
			f.emit("mul %s", strings.Join(a, ", "))
			return nil
		}
		if err := need(1); err != nil {
			return err
		}
		if err := f.read(a[0], "x15", 64); err != nil {
			return err
		}
		f.emit("umulh x2, x8, x15")
		f.emit("mul x8, x8, x15")
		return nil
	case "idiv", "div":
		if err := need(1); err != nil {
			return err
		}
		if err := f.read(a[0], "x15", 64); err != nil {
			return err
		}
		bits := operandWidth(a[0], f.slots)
		if op == "idiv" && bits < 64 {
			suffix := map[int]string{8: "b", 16: "h", 32: "w"}[bits]
			f.emit("sxt%s x15, w15", suffix)
		}
		native := "sdiv"
		if op == "div" {
			native = "udiv"
		}
		f.emit("%s x16, x8, x15", native)
		f.emit("msub x2, x16, x15, x8")
		f.emit("mov x8, x16")
		return nil
	case "cwd":
		f.emit("sxth x8, w8")
		return nil
	case "cdq":
		f.emit("sxtw x8, w8")
		return nil
	case "cqo":
		return nil
	}
	if op == "b" || strings.HasPrefix(op, "b.") || op == "bl" {
		if err := need(1); err != nil {
			return err
		}
		dest := a[0]
		if f.labels[dest] {
			dest = "L" + f.name + "_" + dest
		} else if !strings.HasPrefix(dest, "_") {
			dest = symbol(f.o.Pkgname, dest)
		}
		f.emit("%s %s", op, dest)
		return nil
	}
	conditions := map[string]string{"e": "eq", "z": "eq", "ne": "ne", "nz": "ne", "l": "lt", "le": "le", "g": "gt", "ge": "ge", "b": "lo", "be": "ls", "a": "hi", "ae": "hs", "s": "mi", "ns": "pl", "p": "vs", "np": "vc"}
	if strings.HasPrefix(op, "j") {
		if err := need(1); err != nil {
			return err
		}
		f.flush("")
		dest := symbol(f.o.Pkgname, a[0])
		if f.labels[a[0]] {
			dest = "L" + f.name + "_" + a[0]
		}
		if op == "jmp" {
			f.emit("b %s", dest)
			return nil
		}
		cond := conditions[op[1:]]
		if cond == "" {
			return fmt.Errorf("unsupported branch %s", op)
		}
		f.emit("b.%s %s", cond, dest)
		return nil
	}
	if strings.HasPrefix(op, "set") {
		if err := need(1); err != nil {
			return err
		}
		cond := conditions[op[3:]]
		if cond == "" {
			return fmt.Errorf("unsupported condition %s", op)
		}
		f.emit("cset w16, %s", cond)
		return f.write(a[0], "x16", 8)
	}
	if op == "cbz" || op == "cbnz" || op == "tbz" || op == "tbnz" || op == "adr" {
		if len(a) > 0 && f.labels[a[len(a)-1]] {
			a[len(a)-1] = "L" + f.name + "_" + a[len(a)-1]
		}
	}
	// Native instructions have register/immediate operands, never named locals.
	// The platform assembler validates all native opcode and operand combinations.
	f.emit("%s %s", op, strings.Join(a, ", "))
	return nil
}
