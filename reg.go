package gbasm

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
	R8B
	R8W
	R8D

	R9
	R9B
	R9W
	R9D

	R10
	R10B
	R10W
	R10D

	R11
	R11B
	R11W
	R11D

	R12
	R12B
	R12W
	R12D

	R13
	R13B
	R13W
	R13D

	R14
	R14B
	R14W
	R14D

	R15
	R15B
	R15W
	R15D

	R_RIP

	R_DIL // 8-bit sub-register of RDI (requires REX prefix)
	R_SIL // 8-bit sub-register of RSI (requires REX prefix)
)

func (r Register) String() string {
	switch r {
	case R_AL:
		return "AL"
	case R_AH:
		return "AH"
	case R_AX:
		return "AX"
	case R_EAX:
		return "EAX"
	case R_RAX:
		return "RAX"

	case R_BL:
		return "BL"
	case R_BH:
		return "BH"
	case R_BX:
		return "BX"
	case R_EBX:
		return "EBX"
	case R_RBX:
		return "RBX"

	case R_CL:
		return "CL"
	case R_CH:
		return "CH"
	case R_CX:
		return "CX"
	case R_ECX:
		return "ECX"
	case R_RCX:
		return "RCX"

	case R_DL:
		return "DL"
	case R_DH:
		return "DH"
	case R_DX:
		return "DX"
	case R_EDX:
		return "EDX"
	case R_RDX:
		return "RDX"

	case R_SP:
		return "SP"
	case R_ESP:
		return "ESP"
	case R_RSP:
		return "RSP"

	case R_BP:
		return "BP"
	case R_EBP:
		return "EBP"
	case R_RBP:
		return "RBP"

	case R_SI:
		return "SI"
	case R_ESI:
		return "ESI"
	case R_RSI:
		return "RSI"

	case R_DI:
		return "DI"
	case R_EDI:
		return "EDI"
	case R_RDI:
		return "RDI"

	case R8:
		return "R8"
	case R8B:
		return "R8B"
	case R8W:
		return "R8W"
	case R8D:
		return "R8D"

	case R9:
		return "R9"
	case R9B:
		return "R9B"
	case R9W:
		return "R9W"
	case R9D:
		return "R9D"

	case R10:
		return "R10"
	case R10B:
		return "R10B"
	case R10W:
		return "R10W"
	case R10D:
		return "R10D"

	case R11:
		return "R11"
	case R11B:
		return "R11B"
	case R11W:
		return "R11W"
	case R11D:
		return "R11D"

	case R12:
		return "R12"
	case R12B:
		return "R12B"
	case R12W:
		return "R12W"
	case R12D:
		return "R12D"

	case R13:
		return "R13"
	case R13B:
		return "R13B"
	case R13W:
		return "R13W"
	case R13D:
		return "R13D"

	case R14:
		return "R14"
	case R14B:
		return "R14B"
	case R14W:
		return "R14W"
	case R14D:
		return "R14D"

	case R15:
		return "R15"
	case R15B:
		return "R15B"
	case R15W:
		return "R15W"
	case R15D:
		return "R15D"

	case R_RIP:
		return "RIP"

	case R_DIL:
		return "DIL"
	case R_SIL:
		return "SIL"
	}
	return "UNKNOWN"
}

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
	case "R8B":
		return R8B, nil
	case "R8W":
		return R8W, nil
	case "R8D":
		return R8D, nil

	case "R9":
		return R9, nil
	case "R9B":
		return R9B, nil
	case "R9W":
		return R9W, nil
	case "R9D":
		return R9D, nil

	case "R10":
		return R10, nil
	case "R10B":
		return R10B, nil
	case "R10W":
		return R10W, nil
	case "R10D":
		return R10D, nil

	case "R11":
		return R11, nil
	case "R11B":
		return R11B, nil
	case "R11W":
		return R11W, nil
	case "R11D":
		return R11D, nil

	case "R12":
		return R12, nil
	case "R12B":
		return R12B, nil
	case "R12W":
		return R12W, nil
	case "R12D":
		return R12D, nil

	case "R13":
		return R13, nil
	case "R13B":
		return R13B, nil
	case "R13W":
		return R13W, nil
	case "R13D":
		return R13D, nil

	case "R14":
		return R14, nil
	case "R14B":
		return R14B, nil
	case "R14W":
		return R14W, nil
	case "R14D":
		return R14D, nil

	case "R15":
		return R15, nil
	case "R15B":
		return R15B, nil
	case "R15W":
		return R15W, nil
	case "R15D":
		return R15D, nil

	case "DIL":
		return R_DIL, nil
	case "SIL":
		return R_SIL, nil

	default:
		return 0, fmt.Errorf("No such register: %s", r)
	}
}

func (r Register) needREX() bool {
	if r == R_DIL || r == R_SIL {
		return true
	}
	switch r.fullReg() {
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

	case R_SIL:
		fallthrough
	case R_SI:
		fallthrough
	case R_ESI:
		fallthrough
	case R_RSI:
		return 0b110

	case R_DIL:
		fallthrough
	case R_DI:
		fallthrough
	case R_EDI:
		fallthrough
	case R_RDI:
		return 0b111

	case R8, R8B, R8W, R8D:
		return 0b1000
	case R9, R9B, R9W, R9D:
		return 0b1001
	case R10, R10B, R10W, R10D:
		return 0b1010
	case R11, R11B, R11W, R11D:
		return 0b1011
	case R12, R12B, R12W, R12D:
		return 0b1100
	case R13, R13B, R13W, R13D:
		return 0b1101
	case R14, R14B, R14W, R14D:
		return 0b1110
	case R15, R15B, R15W, R15D:
		return 0b1111
	case R_RIP:
		return 0b101
	default:
		log.Fatalf("No such register: %d", r) // TODO: better error handling
		return 0
	}
}

func (r Register) Width() int {
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

	case R_SIL:
		return 8
	case R_SI:
		return 16
	case R_ESI:
		return 32
	case R_RSI:
		return 64

	case R_DIL:
		return 8
	case R_DI:
		return 16
	case R_EDI:
		return 32
	case R_RDI:
		return 64

	case R8B:
		return 8
	case R8W:
		return 16
	case R8D:
		return 32
	case R8:
		return 64

	case R9B:
		return 8
	case R9W:
		return 16
	case R9D:
		return 32
	case R9:
		return 64

	case R10B:
		return 8
	case R10W:
		return 16
	case R10D:
		return 32
	case R10:
		return 64

	case R11B:
		return 8
	case R11W:
		return 16
	case R11D:
		return 32
	case R11:
		return 64

	case R12B:
		return 8
	case R12W:
		return 16
	case R12D:
		return 32
	case R12:
		return 64

	case R13B:
		return 8
	case R13W:
		return 16
	case R13D:
		return 32
	case R13:
		return 64

	case R14B:
		return 8
	case R14W:
		return 16
	case R14D:
		return 32
	case R14:
		return 64

	case R15B:
		return 8
	case R15W:
		return 16
	case R15D:
		return 32
	case R15:
		return 64

	default:
		log.Fatalf("No such register: %d", r) // TODO: better error handling
		return 0
	}
}

// Only safe to call on 8-bit registers.
func (r Register) brother8() (Register, bool) {
	switch r {
	case R_AL:
		return R_AH, true
	case R_AH:
		return R_AL, true
	case R_BL:
		return R_BH, true
	case R_BH:
		return R_BL, true
	case R_CL:
		return R_CH, true
	case R_CH:
		return R_CL, true
	case R_DL:
		return R_DH, true
	case R_DH:
		return R_DL, true
	}
	return 0, false
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

	case R_SIL:
		fallthrough
	case R_SI:
		fallthrough
	case R_ESI:
		fallthrough
	case R_RSI:
		return R_RSI

	case R_DIL:
		fallthrough
	case R_DI:
		fallthrough
	case R_EDI:
		fallthrough
	case R_RDI:
		return R_RDI

	case R8, R8B, R8W, R8D:
		return R8
	case R9, R9B, R9W, R9D:
		return R9
	case R10, R10B, R10W, R10D:
		return R10
	case R11, R11B, R11W, R11D:
		return R11
	case R12, R12B, R12W, R12D:
		return R12
	case R13, R13B, R13W, R13D:
		return R13
	case R14, R14B, R14W, R14D:
		return R14
	case R15, R15B, R15W, R15D:
		return R15
	default:
		panic("No such register")
	}
}

// subRegisters8 returns the set of 8-bit registers that belong to r
func (r Register) subRegisters8() ([]Register, bool) {
	if r.Width() == 8 {
		return []Register{r}, true
	}
	r = r.fullReg()
	switch r {
	case R_RAX:
		return []Register{R_AL, R_AH}, true
	case R_RBX:
		return []Register{R_BL, R_BH}, true
	case R_RCX:
		return []Register{R_CL, R_CH}, true
	case R_RDX:
		return []Register{R_DL, R_DH}, true
	case R_RDI:
		return []Register{R_DIL}, true
	case R_RSI:
		return []Register{R_SIL}, true
	case R8:
		return []Register{R8B}, true
	case R9:
		return []Register{R9B}, true
	case R10:
		return []Register{R10B}, true
	case R11:
		return []Register{R11B}, true
	case R12:
		return []Register{R12B}, true
	case R13:
		return []Register{R13B}, true
	case R14:
		return []Register{R14B}, true
	case R15:
		return []Register{R15B}, true
	}
	return nil, false
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
		if size == 8 {
			return R_SIL, true
		} else if size == 16 {
			return R_SI, true
		} else if size == 32 {
			return R_ESI, true
		} else if size == 64 {
			return R_RSI, true
		}
	case R_RDI:
		if size == 8 {
			return R_DIL, true
		} else if size == 16 {
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
		if size == 8 {
			return R8B, true
		}
		if size == 16 {
			return R8W, true
		}
		if size == 32 {
			return R8D, true
		}
		return R8, true
	case R9:
		if size == 8 {
			return R9B, true
		}
		if size == 16 {
			return R9W, true
		}
		if size == 32 {
			return R9D, true
		}
		return R9, true
	case R10:
		if size == 8 {
			return R10B, true
		}
		if size == 16 {
			return R10W, true
		}
		if size == 32 {
			return R10D, true
		}
		return R10, true
	case R11:
		if size == 8 {
			return R11B, true
		}
		if size == 16 {
			return R11W, true
		}
		if size == 32 {
			return R11D, true
		}
		return R11, true
	case R12:
		if size == 8 {
			return R12B, true
		}
		if size == 16 {
			return R12W, true
		}
		if size == 32 {
			return R12D, true
		}
		return R12, true
	case R13:
		if size == 8 {
			return R13B, true
		}
		if size == 16 {
			return R13W, true
		}
		if size == 32 {
			return R13D, true
		}
		return R13, true
	case R14:
		if size == 8 {
			return R14B, true
		}
		if size == 16 {
			return R14W, true
		}
		if size == 32 {
			return R14D, true
		}
		return R14, true
	case R15:
		if size == 8 {
			return R15B, true
		}
		if size == 16 {
			return R15W, true
		}
		if size == 32 {
			return R15D, true
		}
		return R15, true
	default:
		panic("No such register")
	}
	return 0, false
}
