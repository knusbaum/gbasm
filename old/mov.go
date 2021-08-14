package gbasm

import (
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"math"
	"reflect"
)

func opPlus(w io.Writer, i Instr, dir opType, opplus, size byte) error {
	op, err := getOpcode(i, dir, size)
	if err != nil {
		return err
	}
	op += opplus
	if size == 8 {
		return writeBs(w, op)
	} else if size == 16 {
		// 0x66 select 16 bit mode from 32 bit mode default.
		return writeBs(w, 0x66, op)
	} else if size == 32 {
		return writeBs(w, op)
	} else if size == 64 {
		return writeBs(w, REX_PFX|REX_W, op)
	} else {
		return fmt.Errorf("Invalid size %d", size)
	}
}

func movRegInt(w io.Writer, inst Instr, o2 interface{}, o1 Register) (ok bool, err error) {
	switch i := o2.(type) {
	case uint8:
		return true, movUint8Reg(w, inst, i, o1)
	case uint16:
		return true, movUint16Reg(w, inst, i, o1)
	case uint32:
		return true, movUint32Reg(w, inst, i, o1)
	case uint64:
		//return true, fmt.Errorf("Cannot %s unsigned 64-bit immediate value %X", inst, i)
		return true, movUint64Reg(w, inst, i, o1)
	case int8:
		return true, movInt8Reg(w, inst, i, o1)
	case int16:
		return true, movInt16Reg(w, inst, i, o1)
	case int32:
		return true, movInt32Reg(w, inst, i, o1)
	case int64:
		//return true, fmt.Errorf("Cannot %s signed 64-bit immediate value %X", inst, i)
		return true, movInt64Reg(w, inst, i, o1)
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
			return true, movUint32Reg(w, inst, uint32(i), o1)
		}
		return true, movInt32Reg(w, inst, int32(i), o1)
	case uint:
		if i > math.MaxUint32 {
			return true, fmt.Errorf("Cannot %s signed int immediate value %X", inst, i)
		}
		return true, movInt32Reg(w, inst, int32(i), o1)
	}
	return false, nil
}

func movUint8Reg(w io.Writer, i Instr, o2 uint8, o1 Register) error {
	o1Reg, o1Size, err := regAndSize(o1)
	if err != nil {
		return err
	}
	// o1 is at least uint8. don't need to check.
	log.Printf("I: %s, INT", i)
	//err = opRegRM(w, i, INT, MOD_DIRECT, 0x00, o1Reg, o1Size)
	err = opPlus(w, i, INT, o1Reg, o1Size)
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

func movUint16Reg(w io.Writer, i Instr, o2 uint16, o1 Register) error {
	o1Reg, o1Size, err := regAndSize(o1)
	if err != nil {
		return err
	}
	// o1 is at least uint8. don't need to check.
	err = opPlus(w, i, INT, o1Reg, o1Size)
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

func movUint32Reg(w io.Writer, i Instr, o2 uint32, o1 Register) error {
	o1Reg, o1Size, err := regAndSize(o1)
	if err != nil {
		return err
	}
	// o1 is at least uint8. don't need to check.
	err = opPlus(w, i, INT, o1Reg, o1Size)
	if err != nil {
		return err
	}
	switch o1Size {
	case 8:
		if o2 > math.MaxUint8 {
			return fmt.Errorf("Cannot encode 32-bit value %d into 8-bit register %s", o2, o1)
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

func movUint64Reg(w io.Writer, i Instr, o2 uint64, o1 Register) error {
	o1Reg, o1Size, err := regAndSize(o1)
	if err != nil {
		return err
	}
	// o1 is at least uint8. don't need to check.
	err = opPlus(w, i, INT, o1Reg, o1Size)
	if err != nil {
		return err
	}
	switch o1Size {
	case 8:
		if o2 > math.MaxUint8 {
			return fmt.Errorf("Cannot encode 64-bit value %d into 8-bit register %s", o2, o1)
		}
		return binary.Write(w, binary.LittleEndian, uint8(o2))
	case 16:
		if o2 > math.MaxUint16 {
			return fmt.Errorf("Cannot encode 64-bit value %d into 16-bit register %s", o2, o1)
		}
		return binary.Write(w, binary.LittleEndian, uint16(o2))
	case 32:
		if o2 > math.MaxUint32 {
			return fmt.Errorf("Cannot encode 64-bit value %d into 16-bit register %s", o2, o1)
		}
		return binary.Write(w, binary.LittleEndian, uint32(o2))
	case 64:
		return binary.Write(w, binary.LittleEndian, o2)
	default:
		return fmt.Errorf("Invalid size %d", o1Size)
	}
}

func movInt8Reg(w io.Writer, i Instr, o2 int8, o1 Register) error {
	o1Reg, o1Size, err := regAndSize(o1)
	if err != nil {
		return err
	}
	// o1 is at least uint8. don't need to check.
	err = opPlus(w, i, INT, o1Reg, o1Size)
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

func movInt16Reg(w io.Writer, i Instr, o2 int16, o1 Register) error {
	o1Reg, o1Size, err := regAndSize(o1)
	if err != nil {
		return err
	}
	// o1 is at least uint8. don't need to check.
	err = opPlus(w, i, INT, o1Reg, o1Size)
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

func movInt32Reg(w io.Writer, i Instr, o2 int32, o1 Register) error {
	o1Reg, o1Size, err := regAndSize(o1)
	if err != nil {
		return err
	}
	// o1 is at least uint8. don't need to check.
	err = opPlus(w, i, INT, o1Reg, o1Size)
	if err != nil {
		return err
	}
	switch o1Size {
	case 8:
		if o2 > math.MaxInt8 || o2 < math.MinInt8 {
			return fmt.Errorf("Cannot encode 32-bit value %d into 8-bit register %s", o2, o1)
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

func movInt64Reg(w io.Writer, i Instr, o2 int64, o1 Register) error {
	o1Reg, o1Size, err := regAndSize(o1)
	if err != nil {
		return err
	}
	// o1 is at least uint8. don't need to check.
	err = opPlus(w, i, INT, o1Reg, o1Size)
	if err != nil {
		return err
	}
	switch o1Size {
	case 8:
		if o2 > math.MaxInt8 || o2 < math.MinInt8 {
			return fmt.Errorf("Cannot encode 64-bit value %d into 8-bit register %s", o2, o1)
		}
		return binary.Write(w, binary.LittleEndian, int8(o2))
	case 16:
		if o2 > math.MaxInt16 || o2 < math.MinInt16 {
			return fmt.Errorf("Cannot encode 64-bit value %d into 16-bit register %s", o2, o1)
		}
		return binary.Write(w, binary.LittleEndian, int16(o2))
	case 32:
		if o2 > math.MaxInt32 || o2 < math.MinInt32 {
			return fmt.Errorf("Cannot encode 64-bit value %d into 32-bit register %s", o2, o1)
		}
		return binary.Write(w, binary.LittleEndian, int32(o2))
	case 64:
		return binary.Write(w, binary.LittleEndian, o2)
	default:
		return fmt.Errorf("Invalid size %d", o1Size)
	}
}

func assembleMovI(w io.Writer, i *Instruction) error {
	if len(i.args) != 2 {
		return fmt.Errorf("MOV expects 2 arguments, but got %d", len(i.args))
	}
	src := i.args[0]
	dst := i.args[1]
	if rsrc, ok := src.(Register); ok {
		switch rdst := dst.(type) {
		case Register:
			return opRegReg(w, MOV, DIR_FROM_REG, rsrc, rdst)
		case Indirect:
			return opRegIndirect(w, MOV, DIR_FROM_REG, rsrc, rdst)
		case BaseIndexScale:
			return opRegBIS(w, MOV, DIR_FROM_REG, rsrc, rdst)
		default:
			return fmt.Errorf("Cannot MOV a register to a %v", reflect.TypeOf(dst))
		}
	} else if rsrc, ok := src.(Indirect); ok {
		switch rdst := dst.(type) {
		case Register:
			return opRegIndirect(w, MOV, DIR_TO_REG, rdst, rsrc)
		default:
			return fmt.Errorf("Cannot MOV an indirect to a %v", reflect.TypeOf(dst))
		}
	} else if rsrc, ok := src.(BaseIndexScale); ok {
		switch rdst := dst.(type) {
		case Register:
			return opRegBIS(w, MOV, DIR_TO_REG, rdst, rsrc)
		default:
			return fmt.Errorf("Cannot MOV a base-index-scale to a %v", reflect.TypeOf(dst))
		}
	} else {
		if rdst, ok := dst.(Register); ok {
			ok, err := movRegInt(w, MOV, src, rdst)
			if ok {
				return err
			}
		}
		return fmt.Errorf("Cannot MOV a %v", reflect.TypeOf(src))
	}
}
