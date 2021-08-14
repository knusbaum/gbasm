package gbasm2

import (
	"fmt"
	"log"
	"strings"
)

type Register int

const (
	R_AL Register = iota
	R_AH
	R_AX
	R_EAX
	R_RAX

	R_BL
	R_BH
	R_BX
	R_EBX
	R_RBX

	R_CL
	R_CH
	R_CX
	R_ECX
	R_RCX

	R_DL
	R_DH
	R_DX
	R_EDX
	R_RDX

	R_SP
	R_ESP
	R_RSP

	R_BP
	R_EBP
	R_RBP

	R_SI
	R_ESI
	R_RSI

	R_DI
	R_EDI
	R_RDI

	R8
	R9
	R10
	R11
	R12
	R13
	R14
	R15
)

func ParseReg(r string) (Register, error) {
	r = strings.ToUpper(r)
	switch r {
	case "AL":
		return R_AL, nil
	case "AH":
		return R_AH, nil
	case "AX":
		return R_AX, nil
	case "EAX":
		return R_EAX, nil
	case "RAX":
		return R_RAX, nil

	case "BL":
		return R_BL, nil
	case "BH":
		return R_BH, nil
	case "BX":
		return R_BX, nil
	case "EBX":
		return R_EBX, nil
	case "RBX":
		return R_RBX, nil

	case "CL":
		return R_CL, nil
	case "CH":
		return R_CH, nil
	case "CX":
		return R_CX, nil
	case "ECX":
		return R_ECX, nil
	case "RCX":
		return R_RCX, nil

	case "DL":
		return R_DL, nil
	case "DH":
		return R_DH, nil
	case "DX":
		return R_DX, nil
	case "EDX":
		return R_EDX, nil
	case "RDX":
		return R_RDX, nil

	case "SP":
		return R_SP, nil
	case "ESP":
		return R_ESP, nil
	case "RSP":
		return R_RSP, nil

	case "BP":
		return R_BP, nil
	case "EBP":
		return R_EBP, nil
	case "RBP":
		return R_RBP, nil

	case "SI":
		return R_SI, nil
	case "ESI":
		return R_ESI, nil
	case "RSI":
		return R_RSI, nil

	case "DI":
		return R_DI, nil
	case "EDI":
		return R_EDI, nil
	case "RDI":
		return R_RDI, nil

	case "R8":
		return R8, nil
	case "R9":
		return R9, nil
	case "R10":
		return R10, nil
	case "R11":
		return R11, nil
	case "R12":
		return R12, nil
	case "R13":
		return R13, nil
	case "R14":
		return R14, nil
	case "R15":
		return R15, nil
	default:
		return 0, fmt.Errorf("No such register: %s", r)
	}
}

func (r Register) needREX() bool {
	switch r {
	// 	case R_RAX:
	// 		return true
	// 	case R_RBX:
	// 		return true
	// 	case R_RCX:
	// 		return true
	// 	case R_RDX:
	// 		return true
	// 	case R_RSP:
	// 		return true
	// 	case R_RBP:
	// 		return true
	// 	case R_RSI:
	// 		return true
	// 	case R_RDI:
	// 		return true
	case R8:
		return true
	case R9:
		return true
	case R10:
		return true
	case R11:
		return true
	case R12:
		return true
	case R13:
		return true
	case R14:
		return true
	case R15:
		return true
	default:
		return false
	}
}

func (r Register) byte() byte {
	switch r {
	case R_AL:
		fallthrough
	case R_AX:
		fallthrough
	case R_EAX:
		fallthrough
	case R_RAX:
		return 0b000
	case R_AH:
		return 0b100

	case R_BL:
		fallthrough
	case R_BX:
		fallthrough
	case R_EBX:
		fallthrough
	case R_RBX:
		return 0b011
	case R_BH:
		return 0b111

	case R_CL:
		fallthrough
	case R_CX:
		fallthrough
	case R_ECX:
		fallthrough
	case R_RCX:
		return 0b001
	case R_CH:
		return 0b101

	case R_DL:
		fallthrough
	case R_DX:
		fallthrough
	case R_EDX:
		fallthrough
	case R_RDX:
		return 0b010
	case R_DH:
		return 0b110

	case R_SP:
		fallthrough
	case R_ESP:
		fallthrough
	case R_RSP:
		return 0b100

	case R_BP:
		fallthrough
	case R_EBP:
		fallthrough
	case R_RBP:
		return 0b101

	case R_SI:
		fallthrough
	case R_ESI:
		fallthrough
	case R_RSI:
		return 0b110

	case R_DI:
		fallthrough
	case R_EDI:
		fallthrough
	case R_RDI:
		return 0b111

	case R8:
		return 0b1000
	case R9:
		return 0b1001
	case R10:
		return 0b1010
	case R11:
		return 0b1011
	case R12:
		return 0b1100
	case R13:
		return 0b1101
	case R14:
		return 0b1110
	case R15:
		return 0b1111
	default:
		log.Fatalf("No such register: %d", r) // TODO: better error handling
		return 0
	}
}

func (r Register) width() int {
	switch r {
	case R_AL:
		return 8
	case R_AX:
		return 16
	case R_EAX:
		return 32
	case R_RAX:
		return 64
	case R_AH:
		return 8

	case R_BL:
		return 8
	case R_BX:
		return 16
	case R_EBX:
		return 32
	case R_RBX:
		return 64
	case R_BH:
		return 8

	case R_CL:
		return 8
	case R_CX:
		return 16
	case R_ECX:
		return 32
	case R_RCX:
		return 64
	case R_CH:
		return 8

	case R_DL:
		return 8
	case R_DX:
		return 16
	case R_EDX:
		return 32
	case R_RDX:
		return 64
	case R_DH:
		return 8

	case R_SP:
		return 16
	case R_ESP:
		return 32
	case R_RSP:
		return 64

	case R_BP:
		return 16
	case R_EBP:
		return 32
	case R_RBP:
		return 64

	case R_SI:
		return 16
	case R_ESI:
		return 32
	case R_RSI:
		return 64

	case R_DI:
		return 16
	case R_EDI:
		return 32
	case R_RDI:
		return 64

	case R8:
		fallthrough
	case R9:
		fallthrough
	case R10:
		fallthrough
	case R11:
		fallthrough
	case R12:
		fallthrough
	case R13:
		fallthrough
	case R14:
		fallthrough
	case R15:
		return 64
	default:
		log.Fatalf("No such register: %d", r) // TODO: better error handling
		return 0
	}
}

// Only safe to call on 8-bit registers.
func (r Register) brother8() Register {
	switch r {
	case R_AL:
		return R_AH
	case R_AH:
		return R_AL
	case R_BL:
		return R_BH
	case R_BH:
		return R_BL
	case R_CL:
		return R_CH
	case R_CH:
		return R_CL
	case R_DL:
		return R_DH
	case R_DH:
		return R_DL
	}
	panic("No such 8-bit register.")
}

func (r Register) fullReg() Register {
	switch r {
	case R_AL:
		fallthrough
	case R_AH:
		fallthrough
	case R_AX:
		fallthrough
	case R_EAX:
		fallthrough
	case R_RAX:
		return R_RAX

	case R_BL:
		fallthrough
	case R_BH:
		fallthrough
	case R_BX:
		fallthrough
	case R_EBX:
		fallthrough
	case R_RBX:
		return R_RBX

	case R_CL:
		fallthrough
	case R_CH:
		fallthrough
	case R_CX:
		fallthrough
	case R_ECX:
		fallthrough
	case R_RCX:
		return R_RCX

	case R_DL:
		fallthrough
	case R_DH:
		fallthrough
	case R_DX:
		fallthrough
	case R_EDX:
		fallthrough
	case R_RDX:
		return R_RDX

	case R_SP:
		fallthrough
	case R_ESP:
		fallthrough
	case R_RSP:
		return R_RSP

	case R_BP:
		fallthrough
	case R_EBP:
		fallthrough
	case R_RBP:
		return R_RBP

	case R_SI:
		fallthrough
	case R_ESI:
		fallthrough
	case R_RSI:
		return R_RSI

	case R_DI:
		fallthrough
	case R_EDI:
		fallthrough
	case R_RDI:
		return R_RDI

	case R8:
		fallthrough
	case R9:
		fallthrough
	case R10:
		fallthrough
	case R11:
		fallthrough
	case R12:
		fallthrough
	case R13:
		fallthrough
	case R14:
		fallthrough
	case R15:
		return r
	default:
		panic("No such register")
	}
}

func (r Register) partial(size int) (Register, bool) {
	switch r {
	case R_RAX:
		if size == 16 {
			return R_AX, true
		} else if size == 32 {
			return R_EAX, true
		} else if size == 64 {
			return R_RAX, true
		}
	case R_RBX:
		if size == 16 {
			return R_BX, true
		} else if size == 32 {
			return R_EBX, true
		} else if size == 64 {
			return R_RBX, true
		}
	case R_RCX:
		if size == 16 {
			return R_CX, true
		} else if size == 32 {
			return R_ECX, true
		} else if size == 64 {
			return R_RCX, true
		}
	case R_RDX:
		if size == 16 {
			return R_DX, true
		} else if size == 32 {
			return R_EDX, true
		} else if size == 64 {
			return R_RDX, true
		}
	case R_RSP:
		if size == 16 {
			return R_SP, true
		} else if size == 32 {
			return R_ESP, true
		} else if size == 64 {
			return R_RSP, true
		}
	case R_RBP:
		if size == 16 {
			return R_BP, true
		} else if size == 32 {
			return R_EBP, true
		} else if size == 64 {
			return R_RBP, true
		}
	case R_RSI:
		if size == 16 {
			return R_SI, true
		} else if size == 32 {
			return R_ESI, true
		} else if size == 64 {
			return R_RSI, true
		}
	case R_RDI:
		if size == 16 {
			return R_DI, true
		} else if size == 32 {
			return R_EDI, true
		} else if size == 64 {
			return R_RDI, true
		}

	case R_BL:
	case R_BH:
	case R_BX:
	case R_EBX:
	case R_CL:
	case R_CH:
	case R_CX:
	case R_ECX:
	case R_DL:
	case R_DH:
	case R_DX:
	case R_EDX:
	case R_SP:
	case R_ESP:
	case R_BP:
	case R_EBP:
	case R_SI:
	case R_ESI:
	case R_DI:
	case R_EDI:
	case R_AL:
	case R_AH:
	case R_AX:
	case R_EAX:

	case R8:
		fallthrough
	case R9:
		fallthrough
	case R10:
		fallthrough
	case R11:
		fallthrough
	case R12:
		fallthrough
	case R13:
		fallthrough
	case R14:
		fallthrough
	case R15:
		if size == 64 {
			return r, true
		}
	default:
		panic("No such register")
	}
	return 0, false
}
