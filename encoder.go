package gbasm2

import (
	"bytes"
	_ "embed"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"reflect"
	"strconv"
)

const (
	MODE_LITERAL byte = 0x80
)

const (
	O0 byte = iota
	O1
	O2
	O3
	O4
)

type Indirect struct {
	Reg Register
	Off int32
}

type Instruction struct {
	Summary string
	Forms   []IForm
}

func (i *Instruction) Encode(w io.Writer, os ...interface{}) error {
forms:
	for _, f := range i.Forms {
		if f.opcount != len(os) {
			continue
		}
		var i int
		for _, fop := range f.ops {
			if fop.Implicit {
				continue
			}
			o := os[i]
			//log.Printf("Checking %#v matches %#v: %t\n", fop, o, fop.Match(o))
			if !fop.Match(o) {
				continue forms
			}
			i++
		}
		//log.Printf("Encoding form %#v\n", f.ops)
		return f.Encode(w, os...)
	}
	return fmt.Errorf("Failed to find an instruction for %s %#v", i.Summary, os)
}

const (
	OP_TYPE_R8 = "r8"
)

func writeByte(w io.Writer, b byte) error {
	var ba [1]byte
	bs := ba[:]
	bs[0] = b
	_, err := w.Write(bs)
	return err
}

type Encoder interface {
	Encode(w io.Writer, os ...interface{}) error
}

func toOp(o string) (byte, error) {
	switch o {
	case "#0":
		return O0, nil
	case "#1":
		return O1, nil
	case "#2":
		return O2, nil
	case "#3":
		return O3, nil
	case "#4":
		return O4, nil
	}
	return 0, fmt.Errorf("Cannot convert %s to an operand number.", o)
}

type prefix struct {
	b byte
}

func (x *prefix) Encode(w io.Writer, os ...interface{}) error {
	return writeByte(w, x.b)
}

// See: 2.2.1.2 (https://www.intel.com/content/dam/www/public/us/en/documents/manuals/64-ia-32-architectures-software-developer-instruction-set-reference-manual-325383.pdf)
// Table 2-4. REX Prefix Fields [BITS: 0100WRXB]
// Field Name		Bit Position		Definition
// - 				7:4 				0100
// W 				3 					0 = Operand size determined by CS.D
// 									1 = 64 Bit Operand Size
// R 				2					Extension of the ModR/M reg field
// X 				1 					Extension of the SIB index field
// B 				0 					Extension of the ModR/M r/m field, SIB base field, or Opcode reg field
type rex struct {
	mandatory bool
	w         byte
	r         byte
	x         byte
	b         byte
}

func (x *rex) Encode(w io.Writer, os ...interface{}) error {
	needed := x.mandatory
	xw := x.w
	//log.Printf("[REX] GETTING BYTE FOR OS: %#v\n", os)
	xr, _ := getRegister(x.r, os)
	// Not all args are registers.
	// 	if err != nil {
	// 		return err
	// 	}
	needed = needed || xr.needREX()
	xx, _ := getRegister(x.x, os)
	// 	if err != nil {
	// 		//return err
	//
	// 	}
	needed = needed || xr.needREX()
	xb, _ := getRegister(x.b, os)
	// 	if err != nil {
	// 		return err
	// 	}
	needed = needed || xr.needREX()
	b := 0b01000000 |
		((xw & 0b1) << 3) |
		(((xr.byte() >> 3) & 0b1) << 2) |
		(((xx.byte() >> 3) & 0b1) << 1) |
		((xb.byte() >> 3) & 0b1)
	if !needed {
		return nil
	}
	return writeByte(w, b)
}

type immediate struct {
	size  int
	value byte
}

func (x *immediate) Encode(w io.Writer, os ...interface{}) error {

	if int(x.value) >= len(os) {
		return fmt.Errorf("[immediate] Not enough args. Expected at least %d\n", x.value)
	}
	var (
		b16 uint16
		b32 uint32
		b64 uint64
	)
	switch x.size {
	case 1:
		b, ok := os[x.value].(uint8)
		if !ok {
			return fmt.Errorf("Expected op %d to be a uint8, but found %v\n", x.value, os[x.value])
		}
		return writeByte(w, b)
	case 2:
		if b, ok := os[x.value].(uint16); ok {
			b16 = b
		} else if b, ok := os[x.value].(uint8); ok {
			b16 = uint16(b)
		} else {
			return fmt.Errorf("Expected op %d to be a uint16 or uint8, but found %v\n", x.value, os[x.value])
		}
		return binary.Write(w, binary.LittleEndian, b16)
	case 4:
		if b, ok := os[x.value].(uint32); ok {
			b32 = b
		} else if b, ok := os[x.value].(uint16); ok {
			b32 = uint32(b)
		} else if b, ok := os[x.value].(uint8); ok {
			b32 = uint32(b)
		} else {
			return fmt.Errorf("Expected op %d to be a uint32, uint16 or uint8, but found %v\n", x.value, os[x.value])
		}
		return binary.Write(w, binary.LittleEndian, b32)
	case 8:
		if b, ok := os[x.value].(uint64); ok {
			b64 = b
		} else if b, ok := os[x.value].(uint32); ok {
			b64 = uint64(b)
		} else if b, ok := os[x.value].(uint16); ok {
			b64 = uint64(b)
		} else if b, ok := os[x.value].(uint8); ok {
			b64 = uint64(b)
		} else {
			return fmt.Errorf("Expected op %d to be a uint64, uint32, uint16 or uint8, but found %v\n", x.value, os[x.value])
		}
		return binary.Write(w, binary.LittleEndian, b64)
	default:
		return fmt.Errorf("Cannot encode immediate of size %d", x.size)
	}
}

type codeOffset struct {
	size  int
	value byte
}

func (x *codeOffset) Encode(w io.Writer, os ...interface{}) error {
	if int(x.value) >= len(os) {
		return fmt.Errorf("[codeOffset] Not enough args. Expected at least %d\n", x.value)
	}
	switch x.size {
	case 1:
		b, ok := os[x.value].(int8)
		if !ok {
			return fmt.Errorf("Expected op %d to be a uint8, but found %v\n", x.value, os[x.value])
		}
		return binary.Write(w, binary.LittleEndian, b)
	case 2:
		b, ok := os[x.value].(int16)
		if !ok {
			return fmt.Errorf("Expected op %d to be a uint16, but found %v\n", x.value, os[x.value])
		}
		return binary.Write(w, binary.LittleEndian, b)
	case 4:
		b, ok := os[x.value].(int32)
		if !ok {
			return fmt.Errorf("Expected op %d to be a uint32, but found %v\n", x.value, os[x.value])
		}
		return binary.Write(w, binary.LittleEndian, b)
	case 8:
		b, ok := os[x.value].(int64)
		if !ok {
			return fmt.Errorf("Expected op %d to be a uint64, but found %v\n", x.value, os[x.value])
		}
		return binary.Write(w, binary.LittleEndian, b)
	default:
		return fmt.Errorf("Cannot encode immediate of size %d", x.size)
	}
}

type dataOffset struct {
	size  int
	value byte
}

func (x *dataOffset) Encode(w io.Writer, os ...interface{}) error {
	if int(x.value) >= len(os) {
		return fmt.Errorf("[dataOffset] Not enough args. Expected at least %d\n", x.value)
	}
	switch x.size {
	case 1:
		b, ok := os[x.value].(uint8)
		if !ok {
			return fmt.Errorf("Expected op %d to be a uint8, but found %v\n", x.value, os[x.value])
		}
		return writeByte(w, b)
	case 2:
		b, ok := os[x.value].(uint16)
		if !ok {
			return fmt.Errorf("Expected op %d to be a uint16, but found %v\n", x.value, os[x.value])
		}
		return binary.Write(w, binary.LittleEndian, b)
	case 4:
		b, ok := os[x.value].(uint32)
		if !ok {
			return fmt.Errorf("Expected op %d to be a uint32, but found %v\n", x.value, os[x.value])
		}
		return binary.Write(w, binary.LittleEndian, b)
	case 8:
		b, ok := os[x.value].(uint64)
		if !ok {
			return fmt.Errorf("Expected op %d to be a uint64, but found %v\n", x.value, os[x.value])
		}
		return binary.Write(w, binary.LittleEndian, b)
	default:
		return fmt.Errorf("Cannot encode immediate of size %d", x.size)
	}
}

type opcode struct {
	op        byte
	hasAddend bool
	addend    byte
}

func getByte(i byte, os []interface{}) (byte, error) {
	if int(i) >= len(os) {
		panic(fmt.Sprintf("booo I: %d, OS: %#v, len(os): %d\n", i, os, len(os)))
		return 0, fmt.Errorf("[getByte] Not enough args. Expected at least %d\n", i)
	}
	b, ok := os[i].(byte)
	if !ok {
		panic("BYTE")
		return 0, fmt.Errorf("Expected op %d to be a byte, but found %v\n", i, reflect.TypeOf(os[i]))
	}
	return b, nil
}

func getRegister(i byte, os []interface{}) (Register, error) {
	if int(i) >= len(os) {
		panic(fmt.Sprintf("booo I: %d, OS: %#v, len(os): %d\n", i, os, len(os)))
		return 0, fmt.Errorf("[getByte] Not enough args. Expected at least %d\n", i)
	}
	b, ok := os[i].(Register)
	if !ok {
		return 0, fmt.Errorf("Expected op %d to be a register, but found %v\n", i, reflect.TypeOf(os[i]))
	}
	return b, nil
}

func (x *opcode) Encode(w io.Writer, os ...interface{}) error {
	var add Register
	var err error
	b := x.op
	if x.hasAddend {
		add, err = getRegister(x.addend, os)
		if err != nil {
			return err
		}
	}
	b += (add.byte() & 0b111)
	//log.Printf("[OPCODE] Writing byte %v\n", b)
	return writeByte(w, b)
}

type modrm struct {
	mod byte
	reg byte
	rm  byte
}

// This logic is a mess and needs to be streamlined.
func (x *modrm) Encode(w io.Writer, os ...interface{}) error {
	var doSib bool
	var mustDisp bool
	var indirect *Indirect
	var xmod byte
	var xreg byte
	var err error
	if x.mod&MODE_LITERAL != 0 {
		xmod = x.mod & ^MODE_LITERAL
	} else {
		if int(x.mod) >= len(os) {
			return fmt.Errorf("[getByte] Not enough args. Expected at least %d\n", x.mod)
		}
		o := os[x.mod]
		switch ot := o.(type) {
		case byte:
			xmod = ot
		case Indirect:
			if ot.Off != 0 {
				xmod = 0b10
			} else {
				xmod = 0b00
			}
			if ot.Reg.byte() == R_RSP.byte() {
				doSib = true
			}
			if ot.Reg.byte() == R_RBP.byte() {
				mustDisp = true
			}
			indirect = &ot
		default:
			return fmt.Errorf("Expected operand %d to be a byte or an indirect.")
		}
	}
	if x.reg&MODE_LITERAL != 0 {
		xreg = x.reg & ^MODE_LITERAL
	} else {
		xregr, err := getRegister(x.reg, os)
		if err != nil {
			return err
		}
		xreg = xregr.byte()
	}
	if !doSib {
		var xrm Register
		if indirect != nil {
			xrm = indirect.Reg
		} else {
			xrm, err = getRegister(x.rm, os)
			if err != nil {
				return err
			}
		}
		if indirect != nil && xmod != 0 {
			b := ((xmod & 0b11) << 6) |
				((xreg & 0b111) << 3) |
				(xrm.byte() & 0b111)
			err := writeByte(w, b)
			if err != nil {
				return err
			}
			// 32-bit displacement
			return binary.Write(w, binary.LittleEndian, indirect.Off)
		} else if mustDisp {
			// optimization to avoid 32-bit offsets when offset is 0
			xmod = 0b01
			b := ((xmod & 0b11) << 6) |
				((xreg & 0b111) << 3) |
				(xrm.byte() & 0b111)
			err := writeByte(w, b)
			if err != nil {
				return err
			}
			return binary.Write(w, binary.LittleEndian, byte(0))
		} else {
			b := ((xmod & 0b11) << 6) |
				((xreg & 0b111) << 3) |
				(xrm.byte() & 0b111)
			return writeByte(w, b)
		}
		return nil
	} else {
		var xrm byte = 0b100
		b := ((xmod & 0b11) << 6) |
			((xreg & 0b111) << 3) |
			(xrm & 0b111)
		err := writeByte(w, b)
		if err != nil {
			return err
		}

		// For now, the only valid SIB is an indirect into RSP:
		// SIB Scale: 00, Index: 100 (RSP) Base: 100 (RSP)
		err = writeByte(w, 0b00100100)
		if err != nil {
			return err
		}

		if xmod != 0 {
			// 32-bit displacement
			return binary.Write(w, binary.LittleEndian, indirect.Off)
		}
		return nil
	}
}

type Op struct {
	// TN holds either the type or name of the operand If the op is implicit, this is the reg name. If
	// the op is explicit, it is the type.
	TN       string
	Output   bool
	Implicit bool
}

func (o *Op) Match(op interface{}) bool {
	if o.Implicit {
		return false
	}
	switch o.TN {
	case "imm4":
		_, ok := op.(uint8)
		return ok
	case "imm8":
		_, ok := op.(uint8)
		return ok
	case "imm16":
		if _, ok := op.(uint8); ok {
			return ok
		}
		_, ok := op.(uint16)
		return ok
	case "imm32":
		if _, ok := op.(uint8); ok {
			return ok
		}
		if _, ok := op.(uint16); ok {
			return ok
		}
		_, ok := op.(uint32)
		return ok
	case "imm64":
		if _, ok := op.(uint8); ok {
			return ok
		}
		if _, ok := op.(uint16); ok {
			return ok
		}
		if _, ok := op.(uint32); ok {
			return ok
		}
		_, ok := op.(uint64)
		return ok
	//case "al":
	//case "cl":
	case "r8":
		if r, ok := op.(Register); ok {
			return r == R_AL || r == R_AH || r == R_BL || r == R_BH || r == R_CL || r == R_CH || r == R_DL || r == R_DH
		}
	//case "r8l":
	//case "ax":
	case "r16":
		if r, ok := op.(Register); ok {
			return r == R_AX || r == R_BX || r == R_CX || r == R_DX || r == R_SP || r == R_BP || r == R_SI || r == R_DI
		}
	//case "r16l":
	//case "eax":
	case "r32":
		if r, ok := op.(Register); ok {
			return r == R_EAX || r == R_EBX || r == R_ECX || r == R_EDX || r == R_ESP || r == R_EBP || r == R_ESI || r == R_EDI
		}
	//case "r32l":
	//case "rax":
	case "r64":
		if r, ok := op.(Register); ok {
			return r == R_RAX || r == R_RBX || r == R_RCX || r == R_RDX || r == R_RSP || r == R_RBP || r == R_RSI || r == R_RDI ||
				r == R8 || r == R9 || r == R10 || r == R11 || r == R12 || r == R13 || r == R14 || r == R15
		}
		// 	case "mm":
		// 	case "xmm0":
		// 	case "xmm":
		// 	case "xmm{k}":
		// 	case "xmm{k}{z}":
		// 	case "ymm":
		// 	case "ymm{k}":
		// 	case "ymm{k}{z}":
		// 	case "zmm":
		// 	case "zmm{k}":
		// 	case "zmm{k}{z}":
		// 	case "k":
		// 	case "k{k}":
	case "moffs32":
		if mo, ok := op.(Indirect); ok {
			return mo.Reg.width() == 64
		}
	case "moffs64":
		if mo, ok := op.(Indirect); ok {
			return mo.Reg.width() == 64
		}
	case "m":
		if mo, ok := op.(Indirect); ok {
			return mo.Reg.width() == 64
		}
	case "m8":
		if mo, ok := op.(Indirect); ok {
			return mo.Reg.width() == 64
		}
	case "m16":
		if mo, ok := op.(Indirect); ok {
			return mo.Reg.width() == 64
		}
	//case "m16{k}{z}":
	case "m32":
		if mo, ok := op.(Indirect); ok {
			return mo.Reg.width() == 64
		}
	//case "m32{k}":
	//case "m32{k}{z}":
	case "m64":
		if mo, ok := op.(Indirect); ok {
			return mo.Reg.width() == 64
		}
	//case "m64{k}":
	//case "m64{k}{z}":
	case "m128":
	//case "m128{k}{z}":
	case "m256":
	//case "m256{k}{z}":
	case "m512":
	//case "m512{k}{z}":
	// 	case "m64/m32bcst":
	// 	case "m128/m32bcst":
	// 	case "m256/m32bcst":
	// 	case "m512/m32bcst":
	// 	case "m128/m64bcst":
	// 	case "m256/m64bcst":
	// 	case "m512/m64bcst":
	// 	case "vm32x":
	// 	case "vm32x{k}":
	// 	case "vm64x":
	// 	case "vm64x{k}":
	// 	case "vm32y":
	// 	case "vm32y{k}":
	// 	case "vm64y":
	// 	case "vm64y{k}":
	// 	case "vm32z":
	// 	case "vm32z{k}":
	// 	case "vm64z":
	// 	case "vm64z{k}":
	case "rel8":
		_, ok := op.(int8)
		return ok
	case "rel32":
		_, ok := op.(int32)
		return ok
		// 	case "{er}":
		// 	case "{sae}":
	default:
		return false
	}
	return false
}

type IForm struct {
	opcount int
	ops     []Op
	enc     [][]Encoder
}

func (f *IForm) Encode(w io.Writer, os ...interface{}) error {
	var err error
encodings:
	for _, es := range f.enc {
		for _, e := range es {
			//log.Printf("Encoding %#v\n", os)
			err = e.Encode(w, os...)
			if err != nil {
				log.Printf("Failed one encoding: %s", err)
				continue encodings
			}
		}
		return nil
	}
	return err
}

type Asm struct {
	ArchName string
	instrs   map[string]*Instruction
}

func (a *Asm) Encode(w io.Writer, instr string, os ...interface{}) error {
	inst, ok := a.instrs[instr]
	if !ok {
		return fmt.Errorf("No such instruction: %s", instr)
	}
	//log.Printf("Encoding instruction %s\n", instr)
	return inst.Encode(w, os...)
}

func parseForm(xform *XForm) (IForm, error) {
	var (
		f   IForm
		err error
	)
	for _, o := range xform.Operands {
		switch o.XMLName.Local {
		case "ISA":
			// Ignore. ISA specifies cpu extension requirements.
		case "Operand":
			f.opcount++
			f.ops = append(f.ops, Op{TN: o.Type, Output: o.Output})
		case "ImplicitOperand":
			f.ops = append(f.ops, Op{TN: o.ID, Output: o.Output, Implicit: true})
		default:
			return IForm{}, fmt.Errorf("Don't know how to encode operand type %s", o.XMLName.Local)
		}
	}
	for _, es := range xform.Encodings {
		var encs []Encoder
		// 		fmt.Printf("ADDING ENCODINGS: \n")
		// 		for _, e := range es.Encodings {
		// 			fmt.Printf("\t%#v [", e.XMLName)
		// 			for _, a := range e.Attrs {
		// 				fmt.Printf("%s -> %s,", a.Name.Local, a.Value)
		// 			}
		// 			fmt.Printf("]\n")
		// 		}
		// 		fmt.Printf("\n")
		for _, e := range es.Encodings {
			switch e.XMLName.Local {
			case "Opcode":
				var addend byte
				var hasAddend bool
				op, ok := e.GetAttr("byte")
				if !ok {
					return IForm{}, errors.New("Opcode has no byte attribute.")
				}
				opbyte, err := strconv.ParseUint(op, 16, 8)
				if err != nil {
					return IForm{}, fmt.Errorf("Failed to encode %s: %s", e.XMLName.Local, err)
				}
				if add, ok := e.GetAttr("addend"); ok {
					addend, err = toOp(add)
					if err != nil {
						return IForm{}, fmt.Errorf("Failed to encode %s: %s", e.XMLName.Local, err)
					}
					hasAddend = true
				}
				//fmt.Printf("Adding opcode: %d, %d\n", opbyte, addend)
				encs = append(encs, &opcode{op: byte(opbyte), hasAddend: hasAddend, addend: addend})
			case "Immediate":
				sz, ok := e.GetAttr("size")
				if !ok {
					return IForm{}, errors.New("Immediate has no size attribute.")
				}
				isz, err := strconv.Atoi(sz)
				if err != nil {
					return IForm{}, fmt.Errorf("Failed to encode %s: %s", e.XMLName.Local, err)
				}
				val, ok := e.GetAttr("value")
				if !ok {
					return IForm{}, errors.New("Immediate has no value attribute.")
				}
				valb, err := toOp(val)
				if err != nil {
					return IForm{}, fmt.Errorf("Failed to encode %s: %s", e.XMLName.Local, err)
				}
				encs = append(encs, &immediate{size: isz, value: valb})
			case "REX":
				var (
					mandatory bool
					w         byte
					r         byte
					x         byte
					b         byte
				)
				if mandatorys, ok := e.GetAttr("mandatory"); ok {
					mandatory, err = strconv.ParseBool(mandatorys)
					if err != nil {
						return IForm{}, fmt.Errorf("Failed to encode %s: %s", e.XMLName.Local, err)
					}
				}
				if ws, ok := e.GetAttr("W"); ok {
					wi, err := strconv.ParseUint(ws, 10, 8)
					if err != nil {
						return IForm{}, fmt.Errorf("Failed to encode %s: %s", e.XMLName.Local, err)
					}
					w = byte(wi)
				}
				if rs, ok := e.GetAttr("R"); ok {
					r, err = toOp(rs)
					if err != nil {
						return IForm{}, fmt.Errorf("Failed to encode %s: %s", e.XMLName.Local, err)
					}
				}
				if xs, ok := e.GetAttr("X"); ok {
					x, err = toOp(xs)
					if err != nil {
						return IForm{}, fmt.Errorf("Failed to encode %s: %s", e.XMLName.Local, err)
					}
				}
				if bs, ok := e.GetAttr("B"); ok {
					b, err = toOp(bs)
					if err != nil {
						return IForm{}, fmt.Errorf("Failed to encode %s: %s", e.XMLName.Local, err)
					}
				}
				//fmt.Printf("Adding rex: w: %d, r: %d, x: %d, b: %d\n", w, r, x, b)
				encs = append(encs, &rex{mandatory: mandatory, w: w, r: r, x: x, b: b})
			case "ModRM":
				var (
					mode byte
					reg  byte
					rm   byte
				)
				if modes, ok := e.GetAttr("mode"); ok {
					modesi, err := strconv.ParseUint(modes, 2, 8)
					if err == nil {
						mode = byte(modesi) | MODE_LITERAL
					} else {
						mode, err = toOp(modes)
						if err != nil {
							return IForm{}, fmt.Errorf("Failed to encode ModRM Mod: %s", err)
						}
					}
				}
				if regs, ok := e.GetAttr("reg"); ok {
					regi, err := strconv.ParseUint(regs, 10, 8)
					if err == nil {
						reg = byte(regi) | MODE_LITERAL

					} else {
						reg, err = toOp(regs)
						if err != nil {
							return IForm{}, fmt.Errorf("Failed to encode ModRM Reg: %s", err)
						}
					}
				}
				if rms, ok := e.GetAttr("rm"); ok {
					rm, err = toOp(rms)
					if err != nil {
						return IForm{}, fmt.Errorf("Failed to encode ModRM RM: %s", err)
					}
				}
				//fmt.Printf("Adding modrm: mode: %d, reg: %d, rm: %d\n", mode, reg, rm)
				encs = append(encs, &modrm{mod: mode, reg: reg, rm: rm})
			case "Prefix":
				bs, ok := e.GetAttr("byte")
				if !ok {
					return IForm{}, errors.New("Prefix has no byte attribute.")
				}
				bi, err := strconv.ParseUint(bs, 16, 8)
				if err != nil {
					return IForm{}, fmt.Errorf("Failed to encode %s: %s", e.XMLName.Local, err)
				}
				encs = append(encs, &prefix{b: byte(bi)})
			case "CodeOffset":
				sz, ok := e.GetAttr("size")
				if !ok {
					return IForm{}, errors.New("Immediate has no size attribute.")
				}
				isz, err := strconv.Atoi(sz)
				if err != nil {
					return IForm{}, fmt.Errorf("Failed to encode %s: %s", e.XMLName.Local, err)
				}
				val, ok := e.GetAttr("value")
				if !ok {
					return IForm{}, errors.New("Immediate has no value attribute.")
				}
				valb, err := toOp(val)
				if err != nil {
					return IForm{}, fmt.Errorf("Failed to encode %s: %s", e.XMLName.Local, err)
				}
				encs = append(encs, &codeOffset{size: isz, value: valb})
			case "DataOffset":
				sz, ok := e.GetAttr("size")
				if !ok {
					return IForm{}, errors.New("Immediate has no size attribute.")
				}
				isz, err := strconv.Atoi(sz)
				if err != nil {
					return IForm{}, fmt.Errorf("Failed to encode %s: %s", e.XMLName.Local, err)
				}
				val, ok := e.GetAttr("value")
				if !ok {
					return IForm{}, errors.New("Immediate has no value attribute.")
				}
				valb, err := toOp(val)
				if err != nil {
					return IForm{}, fmt.Errorf("Failed to encode %s: %s", e.XMLName.Local, err)
				}
				encs = append(encs, &dataOffset{size: isz, value: valb})
			case "VEX":
				fallthrough
			case "EVEX":
				return IForm{}, fmt.Errorf("Cannot encode %s instructions.", e.XMLName.Local)
			default:
				return IForm{}, fmt.Errorf("Don't know how to encode type %s", e.XMLName.Local)
			}
		}
		f.enc = append(f.enc, encs)
	}
	return f, nil
}

func ParseFile(fname string) (*Asm, error) {
	xis, err := DecodeFile(fname)
	if err != nil {
		return nil, err
	}

	a := &Asm{ArchName: xis.Name, instrs: make(map[string]*Instruction)}
	for _, xi := range xis.XInstructions {
		instr := &Instruction{Summary: xi.Summary}
		a.instrs[xi.Name] = instr
		//log.Printf("INSTR: %s", xi.Name)

		for _, xform := range xi.Forms {
			f, err := parseForm(xform)
			if err != nil {
				//log.Printf("Failed to parse a form of %s: %s", xi.Name, err)
				continue
			}
			instr.Forms = append(instr.Forms, f)
		}
	}
	return a, nil
}

func parse(r io.Reader) (*Asm, error) {
	xis, err := decode(r)
	if err != nil {
		return nil, err
	}

	a := &Asm{ArchName: xis.Name, instrs: make(map[string]*Instruction)}
	for _, xi := range xis.XInstructions {
		instr := &Instruction{Summary: xi.Summary}
		a.instrs[xi.Name] = instr
		//log.Printf("INSTR: %s", xi.Name)

		for _, xform := range xi.Forms {
			f, err := parseForm(xform)
			if err != nil {
				//log.Printf("Failed to parse a form of %s: %s", xi.Name, err)
				continue
			}
			instr.Forms = append(instr.Forms, f)
		}
	}
	return a, nil
}

type Arch int

const (
	AMD64 Arch = iota
)

//go:embed x86_64.xml
var amd64 []byte

func LoadAsm(a Arch) (*Asm, error) {
	switch a {
	case AMD64:
		return parse(bytes.NewBuffer(amd64))
	default:
		return nil, fmt.Errorf("No such achitecture")
	}
}
