package gbasm

import (
	"fmt"
	"io"
	"reflect"
)

func assembleArithI(w io.Writer, i *Instruction) error {
	if len(i.args) != 2 {
		return fmt.Errorf("%s expects 2 arguments, but got %d", i.instr, len(i.args))
	}
	src := i.args[0]
	dst := i.args[1]
	if rsrc, ok := src.(Register); ok {
		switch rdst := dst.(type) {
		case Register:
			return opRegReg(w, i.instr, DIR_FROM_REG, rsrc, rdst)
		case Indirect:
			return opRegIndirect(w, i.instr, DIR_FROM_REG, rsrc, rdst)
		default:
			return fmt.Errorf("Cannot %s a register into a %v", i.instr, reflect.TypeOf(dst))
		}
	} else if rsrc, ok := src.(Indirect); ok {
		switch rdst := dst.(type) {
		case Register:
			return opRegIndirect(w, i.instr, DIR_TO_REG, rdst, rsrc)
		default:
			return fmt.Errorf("Cannot %s an indirect to a %v", i.instr, reflect.TypeOf(dst))
		}
	} else {
		if rdst, ok := dst.(Register); ok {
			ok, err := opIntReg(w, i.instr, src, rdst)
			if ok {
				return err
			}
		}
		return fmt.Errorf("Cannot %s a %v", i.instr, reflect.TypeOf(src))
	}
}
