package gbasm

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"
)

type opType int

const (
	DIR_FROM_REG opType = iota
	DIR_TO_REG
	UINT
	INT
)

func (d opType) String() string {
	switch d {
	case DIR_FROM_REG:
		return "DIR_FROM_REG"
	case DIR_TO_REG:
		return "DIR_TO_REG"
	case UINT:
		return "UINT"
	case INT:
		return "INT"
	default:
		return "UNKNOWN"
	}
}

func (d opType) Reverse() opType {
	switch d {
	case DIR_FROM_REG:
		return DIR_TO_REG
	case DIR_TO_REG:
		return DIR_FROM_REG
	default:
		return d
	}
}

func assemble(is []*Instruction) ([]byte, error) {
	var b bytes.Buffer
	for _, i := range is {
		err := assembleI(&b, i)
		if err != nil {
			return nil, err
		}
	}
	return b.Bytes(), nil
}

// Mod Reg R/M:
// Reading:
// https://www.codeproject.com/Articles/662301/x86-Instruction-Encoding-Revealed-Bit-Twiddling-fo
// https://wiki.osdev.org/X86-64_Instruction_Encoding#ModR.2FM
//
// Mod Bits:
// 00 - Indirect addressing mode: Fetch the contents of the address found within the register
// specified in the R/M section. For example, when the Mod bits are set to 00 and the R/M bits are
// set to 000, the addressing mode is [eax] (dereference the address at eax). Two exceptions to
// this rule are when the R/M bits are set to 100 - that's when the processor switches to SIB
// addressing and reads the SIB byte, treated next - or 101, when the processor switches to 32-bit
// displacement mode, which basically means that a 32 bit number is read from the displacement
// bytes (see figure 1) and then dereferenced.
//
// 01 - This is essentialy the same as 00, except that an 8-bit displacement is added to the value
// before dereferencing.
//
// 10 - The same as the above, except that a 32-bit displacement is added to the value.
//
// 11 - Direct addressing mode. Move the value in the source register to the destination register
// (the Reg and R/M byte will each refer to a register).
//
//
// Reg and sometimes the R/M bits:
// 000 (decimal 0) - EAX (AX if data size is 16 bits, AL if data size is 8 bits)
// 001 (1) - ECX/CX/CL
// 010 (2) - EDX/DX/DL
// 011 (3) - EBX/BX/BL
// 100 (4) - ESP/SP (AH if data size is defined as 8 bits)
// 101 (5) - EBP/BP (CH if data size is defined as 8 bits)
// 110 (6) - ESI/SI (DH if data size is defined as 8 bits)
// 111 (7) - EDI/DI (BH if data size is defined as 8 bits)

// https://www.intel.com/content/dam/www/public/us/en/documents/manuals/64-ia-32-architectures-software-developer-instruction-set-reference-manual-325383.pdf
// 2.1.1 Instruction Prefixes
const (
	MOD_INDIRECT = 0b00
	MOD_DISP8    = 0b01
	MOD_DISP32   = 0b10
	MOD_DIRECT   = 0b11

	REG_EAX = 0b000
	REG_ECX = 0b001
	REG_EDX = 0b010
	REG_EBX = 0b011
	REG_ESP = 0b100
	REG_EBP = 0b101
	REG_ESI = 0b110
	REG_EDI = 0b111

	RM_SIB    = 0b100
	RM_DISP32 = 0b101

	REX_PFX = 0b01000000
	REX_B   = 0b0001
	REX_X   = 0b0010
	REX_R   = 0b0100
	REX_W   = 0b1000
)

func writeByte(w io.Writer, b byte) error {
	var bs [1]byte
	bs[0] = b
	_, err := w.Write(bs[:])
	return err
}

func assembleI(w io.Writer, i *Instruction) error {
	switch i.instr {
	case MOV:
		return assembleMovI(w, i)
	case ADD:
		fallthrough
	case SUB:
		fallthrough
	case MUL:
		fallthrough
	case DIV:
		return assembleArithI(w, i)
	case RET:
		return writeByte(w, 0xC3)
	default:
		return fmt.Errorf("Failed to assemble instruction %#v", i)
	}
}

func regAndSize(r Register) (byte, byte, error) {
	switch r {
	case AL:
		return REG_EAX, 8, nil
	case AH:
		return REG_ESP, 8, nil
	case AX:
		return REG_EAX, 16, nil
	case EAX:
		return REG_EAX, 32, nil
	case RAX:
		return REG_EAX, 64, nil
	case BL:
		return REG_EBX, 8, nil
	case BH:
		return REG_EDI, 8, nil
	case BX:
		return REG_EBX, 16, nil
	case EBX:
		return REG_EBX, 32, nil
	case RBX:
		return REG_EBX, 64, nil
	case CL:
		return REG_ECX, 8, nil
	case CH:
		return REG_EBP, 8, nil
	case CX:
		return REG_ECX, 16, nil
	case ECX:
		return REG_ECX, 32, nil
	case RCX:
		return REG_ECX, 64, nil
	case DL:
		return REG_EDX, 8, nil
	case DH:
		return REG_ESI, 8, nil
	case DX:
		return REG_EDX, 16, nil
	case EDX:
		return REG_EDX, 32, nil
	case RDX:
		return REG_EDX, 64, nil
	case CS:
		return 0, 0, fmt.Errorf("Cannot encode register CS in MODREGR/M")
	case DS:
		return 0, 0, fmt.Errorf("Cannot encode register DS in MODREGR/M")
	case ES:
		return 0, 0, fmt.Errorf("Cannot encode register ES in MODREGR/M")
	case FS:
		return 0, 0, fmt.Errorf("Cannot encode register FS in MODREGR/M")
	case GS:
		return 0, 0, fmt.Errorf("Cannot encode register GS in MODREGR/M")
	case SS:
		return 0, 0, fmt.Errorf("Cannot encode register SS in MODREGR/M")
	case SI:
		return REG_ESI, 16, nil
	case ESI:
		return REG_ESI, 32, nil
	case RSI:
		return REG_ESI, 64, nil
	case DI:
		return REG_EDI, 16, nil
	case EDI:
		return REG_EDI, 32, nil
	case RDI:
		return REG_EDI, 64, nil
	case BP:
		return REG_EBP, 16, nil
	case EBP:
		return REG_EBP, 32, nil
	case RBP:
		return REG_EBP, 64, nil
	case IP:
		return 0, 0, fmt.Errorf("Cannot encode register IP in MODREGR/M")
	case EIP:
		return 0, 0, fmt.Errorf("Cannot encode register EIP in MODREGR/M")
	case RIP:
		return 0, 0, fmt.Errorf("Cannot encode register RIP in MODREGR/M")
	case SP:
		return REG_ESP, 16, nil
	case ESP:
		return REG_ESP, 32, nil
	case RSP:
		return REG_ESP, 64, nil
	case EFLAGS:
		return 0, 0, fmt.Errorf("Cannot encode register EFLAGS in MODREGR/M")
	default:
		return 0, 0, fmt.Errorf(fmt.Sprintf("Cannot encode unknown register %d", r))
	}
}

func modRegRM(mod, reg, rm byte) byte {
	return ((mod & 0b11) << 6) |
		((reg & 0b111) << 3) |
		(rm & 0b111)
}

func sib(scale, index, base byte) byte {
	return ((scale & 0b11) << 6) |
		((index & 0b111) << 3) |
		(base & 0b111)
}

func writeBs(w io.Writer, bs ...byte) error {
	_, err := w.Write(bs)
	return err
}

// Based on the Intel terminology in the instruction set reference, use the opcodes with the Op/En
// type listed below:
// DIR_FROM_REG -> MR
// DIR_TO_REG -> RM
var opcodes map[Instr]map[opType]map[byte]byte = map[Instr]map[opType]map[byte]byte{
	MOV: map[opType]map[byte]byte{
		DIR_FROM_REG: map[byte]byte{
			8:  0x88,
			16: 0x89,
			32: 0x89,
			64: 0x89,
		},
		DIR_TO_REG: map[byte]byte{
			8:  0x8A,
			16: 0x8B,
			32: 0x8B,
			64: 0x8B,
		},
		INT: map[byte]byte{
			8:  0xB0,
			16: 0xB8,
			32: 0xB8,
			64: 0xB8,
		},
	},
	ADD: map[opType]map[byte]byte{
		DIR_FROM_REG: map[byte]byte{
			8:  0x00,
			16: 0x01,
			32: 0x01,
			64: 0x01,
		},
		DIR_TO_REG: map[byte]byte{
			8:  0x02,
			16: 0x03,
			32: 0x03,
			64: 0x03,
		},
		INT: map[byte]byte{
			8:  0x80,
			16: 0x81,
			32: 0x81,
			64: 0x81,
		},
	},
	SUB: map[opType]map[byte]byte{
		DIR_FROM_REG: map[byte]byte{
			8:  0x28,
			16: 0x29,
			32: 0x29,
			64: 0x29,
		},
		DIR_TO_REG: map[byte]byte{
			8:  0x2a,
			16: 0x2b,
			32: 0x2b,
			64: 0x2b,
		},
		INT: map[byte]byte{
			8:  0x80,
			16: 0x81,
			32: 0x81,
			64: 0x81,
		},
	},
}

// arithIntReg provides the Reg value for the ModRegRM byte in arithmetic instructions.
var arithIntReg map[Instr]byte = map[Instr]byte{
	ADD: 0,
	SUB: 5,
}

func getOpcode(i Instr, dir opType, size byte) (byte, error) {
	dm := opcodes[i]
	if dm == nil {
		return 0, fmt.Errorf("No such opcode for %s Type %s Size %d", i, dir, size)
	}
	sm := dm[dir]
	if sm == nil {
		return 0, fmt.Errorf("No such opcode for %s Type %s Size %d", i, dir, size)
	}
	op, ok := sm[size]
	if !ok {
		return 0, fmt.Errorf("No such opcode for %s Type %s Size %d", i, dir, size)
	}
	return op, nil
}

func opRegRM(w io.Writer, i Instr, dir opType, mod, reg, rm, size byte) error {
	op, err := getOpcode(i, dir, size)
	if err != nil {
		return err
	}
	if size == 8 {
		return writeBs(w, op, modRegRM(mod, reg, rm))
	} else if size == 16 {
		// 0x66 select 16 bit mode from 32 bit mode default.
		return writeBs(w, 0x66, op, modRegRM(mod, reg, rm))
	} else if size == 32 {
		return writeBs(w, op, modRegRM(mod, reg, rm))
	} else if size == 64 {
		return writeBs(w, REX_PFX|REX_W, op, modRegRM(mod, reg, rm))
	} else {
		return fmt.Errorf("Invalid size %d", size)
	}
}

func opIntReg(w io.Writer, inst Instr, o2 interface{}, o1 Register) (ok bool, err error) {
	switch i := o2.(type) {
	case uint8:
		return true, opUint8Reg(w, inst, i, o1)
	case uint16:
		return true, opUint16Reg(w, inst, i, o1)
	case uint32:
		return true, opUint32Reg(w, inst, i, o1)
	case uint64:
		return true, fmt.Errorf("Cannot %s unsigned 64-bit immediate value %X", inst, i)
	case int8:
		return true, opInt8Reg(w, inst, i, o1)
	case int16:
		return true, opInt16Reg(w, inst, i, o1)
	case int32:
		return true, opInt32Reg(w, inst, i, o1)
	case int64:
		return true, fmt.Errorf("Cannot %s signed 64-bit immediate value %X", inst, i)
	case int:
		if i < math.MinInt32 {
			return true, fmt.Errorf("Cannot %s signed int immediate value %X", inst, i)
		}
		if i > math.MaxUint32 {
			return true, fmt.Errorf("Cannot %s signed int immediate value %X", inst, i)
		}
		if i > math.MaxInt32 {
			// For signed ints > 32 bits but less than max unsigned int32, we can
			// convert to unsigned since the math works out the same way.
			return true, opUint32Reg(w, inst, uint32(i), o1)
		}
		return true, opInt32Reg(w, inst, int32(i), o1)
	case uint:
		if i > math.MaxUint32 {
			return true, fmt.Errorf("Cannot %s signed int immediate value %X", inst, i)
		}
		return true, opInt32Reg(w, inst, int32(i), o1)
	}
	return false, nil
}

func opUint8Reg(w io.Writer, i Instr, o2 uint8, o1 Register) error {
	o1Reg, o1Size, err := regAndSize(o1)
	if err != nil {
		return err
	}
	modReg, ok := arithIntReg[i]
	if !ok {
		return fmt.Errorf("No defined Reg value for ModRegRM byte for %s", i)
	}
	err = opRegRM(w, i, INT, MOD_DIRECT, modReg, o1Reg, o1Size)
	if err != nil {
		return err
	}
	switch o1Size {
	case 8:
		return binary.Write(w, binary.LittleEndian, o2)
	case 16:
		return binary.Write(w, binary.LittleEndian, uint16(o2))
	case 32:
		return binary.Write(w, binary.LittleEndian, uint32(o2))
	case 64:
		return binary.Write(w, binary.LittleEndian, uint32(o2))
	default:
		return fmt.Errorf("Invalid size %d", o1Size)
	}
}
func opUint16Reg(w io.Writer, i Instr, o2 uint16, o1 Register) error {
	o1Reg, o1Size, err := regAndSize(o1)
	if err != nil {
		return err
	}
	modReg, ok := arithIntReg[i]
	if !ok {
		return fmt.Errorf("No defined Reg value for ModRegRM byte for %s", i)
	}
	err = opRegRM(w, i, INT, MOD_DIRECT, modReg, o1Reg, o1Size)
	if err != nil {
		return err
	}
	switch o1Size {
	case 8:
		if o2 > math.MaxUint8 {
			return fmt.Errorf("Cannot encode 16-bit value %d into 8-bit register %s", o2, o1)
		}
		return binary.Write(w, binary.LittleEndian, uint8(o2))
	case 16:
		return binary.Write(w, binary.LittleEndian, o2)
	case 32:
		return binary.Write(w, binary.LittleEndian, uint32(o2))
	case 64:
		return binary.Write(w, binary.LittleEndian, uint32(o2))
	default:
		return fmt.Errorf("Invalid size %d", o1Size)
	}
}
func opUint32Reg(w io.Writer, i Instr, o2 uint32, o1 Register) error {
	o1Reg, o1Size, err := regAndSize(o1)
	if err != nil {
		return err
	}
	modReg, ok := arithIntReg[i]
	if !ok {
		return fmt.Errorf("No defined Reg value for ModRegRM byte for %s", i)
	}
	err = opRegRM(w, i, INT, MOD_DIRECT, modReg, o1Reg, o1Size)
	if err != nil {
		return err
	}
	switch o1Size {
	case 8:
		if o2 > math.MaxUint8 {
			return fmt.Errorf("Cannot encode 16-bit value %d into 8-bit register %s", o2, o1)
		}
		return binary.Write(w, binary.LittleEndian, uint8(o2))
	case 16:
		if o2 > math.MaxUint16 {
			return fmt.Errorf("Cannot encode 32-bit value %d into 16-bit register %s", o2, o1)
		}
		return binary.Write(w, binary.LittleEndian, uint16(o2))
	case 32:
		return binary.Write(w, binary.LittleEndian, o2)
	case 64:
		return binary.Write(w, binary.LittleEndian, o2)
	default:
		return fmt.Errorf("Invalid size %d", o1Size)
	}
}

func opInt8Reg(w io.Writer, i Instr, o2 int8, o1 Register) error {
	o1Reg, o1Size, err := regAndSize(o1)
	if err != nil {
		return err
	}
	modReg, ok := arithIntReg[i]
	if !ok {
		return fmt.Errorf("No defined Reg value for ModRegRM byte for %s", i)
	}
	err = opRegRM(w, i, INT, MOD_DIRECT, modReg, o1Reg, o1Size)
	if err != nil {
		return err
	}
	switch o1Size {
	case 8:
		return binary.Write(w, binary.LittleEndian, o2)
	case 16:
		return binary.Write(w, binary.LittleEndian, int16(o2))
	case 32:
		return binary.Write(w, binary.LittleEndian, int32(o2))
	case 64:
		return binary.Write(w, binary.LittleEndian, int32(o2))
	default:
		return fmt.Errorf("Invalid size %d", o1Size)
	}
}
func opInt16Reg(w io.Writer, i Instr, o2 int16, o1 Register) error {
	o1Reg, o1Size, err := regAndSize(o1)
	if err != nil {
		return err
	}
	modReg, ok := arithIntReg[i]
	if !ok {
		return fmt.Errorf("No defined Reg value for ModRegRM byte for %s", i)
	}
	err = opRegRM(w, i, INT, MOD_DIRECT, modReg, o1Reg, o1Size)
	if err != nil {
		return err
	}
	switch o1Size {
	case 8:
		if o2 > math.MaxInt8 || o2 < math.MinInt8 {
			return fmt.Errorf("Cannot encode 16-bit value %d into 8-bit register %s", o2, o1)
		}
		return binary.Write(w, binary.LittleEndian, int8(o2))
	case 16:
		return binary.Write(w, binary.LittleEndian, o2)
	case 32:
		return binary.Write(w, binary.LittleEndian, int32(o2))
	case 64:
		return binary.Write(w, binary.LittleEndian, int32(o2))
	default:
		return fmt.Errorf("Invalid size %d", o1Size)
	}
}
func opInt32Reg(w io.Writer, i Instr, o2 int32, o1 Register) error {
	o1Reg, o1Size, err := regAndSize(o1)
	if err != nil {
		return err
	}
	modReg, ok := arithIntReg[i]
	if !ok {
		return fmt.Errorf("No defined Reg value for ModRegRM byte for %s", i)
	}
	err = opRegRM(w, i, INT, MOD_DIRECT, modReg, o1Reg, o1Size)
	if err != nil {
		return err
	}
	switch o1Size {
	case 8:
		if o2 > math.MaxInt8 || o2 < math.MinInt8 {
			return fmt.Errorf("Cannot encode 16-bit value %d into 8-bit register %s", o2, o1)
		}
		return binary.Write(w, binary.LittleEndian, int8(o2))
	case 16:
		if o2 > math.MaxInt16 || o2 < math.MinInt16 {
			return fmt.Errorf("Cannot encode 32-bit value %d into 16-bit register %s", o2, o1)
		}
		return binary.Write(w, binary.LittleEndian, int16(o2))
	case 32:
		return binary.Write(w, binary.LittleEndian, o2)
	case 64:
		return binary.Write(w, binary.LittleEndian, o2)
	default:
		return fmt.Errorf("Invalid size %d", o1Size)
	}
}

func opRegReg(w io.Writer, i Instr, dir opType, o1 Register, o2 Register) error {
	o1Reg, o1Size, err := regAndSize(o1)
	if err != nil {
		return err
	}
	o2Reg, o2Size, err := regAndSize(o2)
	if err != nil {
		return err
	}
	if o1Size != o2Size {
		return fmt.Errorf("Cannot move between %s(%d bits) and %s(%d bits)", o1, o1Size, o2, o2Size)
	}
	if o2Reg == RM_SIB || o2Reg == RM_DISP32 {
		return opRegRM(w, i, dir.Reverse(), MOD_DIRECT, o2Reg, o1Reg, o1Size)
	} else {
		return opRegRM(w, i, dir, MOD_DIRECT, o1Reg, o2Reg, o1Size)
	}
}

func opRegIndirect(w io.Writer, i Instr, dir opType, o1 Register, o2 Indirect) error {
	if o2.Reg == DS {
		// special case indirects from ds
		o1Reg, o1Size, err := regAndSize(o1)
		if err != nil {
			return err
		}
		err = opRegRM(w, i, dir, MOD_INDIRECT, o1Reg, RM_SIB, o1Size)
		if err != nil {
			return err
		}
		// Magic number 0x25. See Intel Table 2-3.  32-Bit Addressing Forms with the SIB Byte
		err = writeBs(w, sib(0, RM_SIB, 0x25))
		if err != nil {
			return err
		}
		return binary.Write(w, binary.LittleEndian, uint32(o2.Off))
	}
	o1Reg, o1Size, err := regAndSize(o1)
	if err != nil {
		return err
	}
	o2Reg, o2Size, err := regAndSize(o2.Reg)
	if err != nil {
		return err
	}
	if o2Size != 64 {
		return fmt.Errorf("Cannot %s offset from register %s (%d bits) Expected 64-bit register for addressing.", i, o2.Reg, o2Size)
	}
	if o2.Off == 0 {
		if o2Reg == RM_SIB || o2Reg == RM_DISP32 {
			if o1Reg == RM_SIB || o1Reg == RM_DISP32 {
				if o2Reg == RM_DISP32 {
					err := opRegRM(w, i, dir, MOD_DISP8, o1Reg, o2Reg, o1Size)
					if err != nil {
						return err
					}
					return writeBs(w, 0)
				} else {
					// o2Reg == RM_SIB
					err := opRegRM(w, i, dir, MOD_INDIRECT, o1Reg, RM_SIB, o1Size)
					if err != nil {
						return err
					}
					err = writeBs(w, sib(0, o2Reg, o2Reg))
					if err != nil {
						return err
					}
					return nil
				}
			}
			return opRegRM(w, i, dir.Reverse(), MOD_INDIRECT, o2Reg, o1Reg, o1Size)
		} else {
			return opRegRM(w, i, dir, MOD_INDIRECT, o1Reg, o2Reg, o1Size)
		}
	} else if o2.Off < math.MaxUint8 {
		err := opRegRM(w, i, dir, MOD_DISP8, o1Reg, o2Reg, o1Size)
		if err != nil {
			return err
		}
		return writeBs(w, byte(o2.Off))
	} else {
		err := opRegRM(w, i, dir, MOD_DISP32, o1Reg, o2Reg, o1Size)
		if err != nil {
			return err
		}
		return binary.Write(w, binary.LittleEndian, uint32(o2.Off))
	}
}

func calculateScale(scale byte) (byte, error) {
	switch scale {
	case 1:
		return 0b00, nil
	case 2:
		return 0b01, nil
	case 4:
		return 0b10, nil
	case 8:
		return 0b11, nil
	}
	return 0, fmt.Errorf("Invalid scale %d", scale)
}

func opRegBIS(w io.Writer, i Instr, dir opType, o1 Register, o2 BaseIndexScale) error {
	o1Reg, o1Size, err := regAndSize(o1)
	if err != nil {
		return err
	}
	baseReg, baseSize, err := regAndSize(o2.Base)
	if err != nil {
		return err
	}
	indexReg, indexSize, err := regAndSize(o2.Index)
	if err != nil {
		return err
	}
	if baseSize != 64 || indexSize != 64 {
		return fmt.Errorf("Cannot %s base-index-scale with registers Base: %s(size %d), Index: %s(size %d)", i, o2.Base, baseSize, o2.Index, indexSize)
	}
	if baseReg == RM_SIB || baseReg == RM_DISP32 {
		return fmt.Errorf("Cannot %s into Index Reg %s", i, o2.Base)
	}
	if indexReg == RM_SIB || indexReg == RM_DISP32 {
		return fmt.Errorf("Cannot %s into Index Reg %s", i, o2.Index)
	}

	err = opRegRM(w, i, dir, MOD_INDIRECT, o1Reg, RM_SIB, o1Size)
	if err != nil {
		return err
	}

	scaleBits, err := calculateScale(o2.Scale)
	if err != nil {
		return err
	}
	err = writeBs(w, sib(scaleBits, indexReg, baseReg))
	if err != nil {
		return err
	}
	return nil
}
