package gbasm

import (
	"fmt"
)

type rstate struct {
	inuse bool
	size  int
}

type Regval struct {
	name     string
	localoff uint32
	absoff   uint64
}

type Registers struct {
	rs map[Register]*rstate
}

func NewRegisters() *Registers {
	rs := &Registers{
		rs: make(map[Register]*rstate),
	}
	rs.rs[R_AL] = &rstate{}
	rs.rs[R_AH] = &rstate{}
	rs.rs[R_RAX] = &rstate{}
	rs.rs[R_BL] = &rstate{}
	rs.rs[R_BH] = &rstate{}
	rs.rs[R_RBX] = &rstate{}
	rs.rs[R_CL] = &rstate{}
	rs.rs[R_CH] = &rstate{}
	rs.rs[R_RCX] = &rstate{}
	rs.rs[R_DL] = &rstate{}
	rs.rs[R_DH] = &rstate{}
	rs.rs[R_RDX] = &rstate{}
	rs.rs[R_RSP] = &rstate{}
	rs.rs[R_RBP] = &rstate{}
	rs.rs[R_RSI] = &rstate{}
	rs.rs[R_RDI] = &rstate{}
	rs.rs[R8] = &rstate{}
	rs.rs[R9] = &rstate{}
	rs.rs[R10] = &rstate{}
	rs.rs[R11] = &rstate{}
	rs.rs[R12] = &rstate{}
	rs.rs[R13] = &rstate{}
	rs.rs[R14] = &rstate{}
	rs.rs[R15] = &rstate{}
	return rs
}

// var regs8 = []Register{R_AL, R_AH, R_BL, R_BH, R_CL, R_CH, R_DL, R_DH}
// var regs64 = []Register{R_RBX, R_RCX, R_RDX, R_RSP, R_RBP, R_RSI, R_RDI, R8, R9, R10, R11, R12, R13, R14, R15, R_RAX}

// These lists of registers are sorted in priority order for allocation.
// Registers earlier in the list will be allocated sooner.
// This can be useful to avoid allocating registers like RAX that will need to be saved for every call.
// This assembler assumes a System V Amd64 ABI.
// (See: https://en.wikipedia.org/wiki/X86_calling_conventions#System_V_AMD64_ABI )
var regs8 = []Register{R_BL, R_BH, R_CL, R_CH, R_DL, R_DH, R_AL, R_AH}

//var regs64 = []Register{R_RBX, R_RCX, R_RDX, R_RSP, R_RBP, R_RSI, R_RDI, R8, R9, R10, R11, R12, R13, R14, R15, R_RAX}

// In least-used order
// R9, R8, R_RCX, R_RDX, R_RSI, R_RDI, R_RAX
var func_arg_regs = []Register{R9, R8, R_RCX, R_RDX, R_RSI, R_RDI, R_RAX}

// Registers that are callee-saved.
// R_RBX, R12, R13, R14, R15, R_RBP, R_RSP
var callee_saved = []Register{R_RBX, R12, R13, R14, R15, R_RBP, R_RSP}

// All remaining registers
var other_regs = []Register{R10, R11}

// All registers that need to be saved by a caller before calling.
var caller_saved = []Register{R_RAX, R_RCX, R_RDX, R_RSI, R_RDI, R8, R9, R10, R11}

var regs64_prefer_callee_saved = append(append(callee_saved, other_regs...), func_arg_regs...)

var regs64_prefer_caller_saved = append(append(other_regs, callee_saved...), func_arg_regs...)

// We can switch this or pick one depending on the situation.
// For now, we're going te default to caller-saved.
var regs64 = regs64_prefer_caller_saved

// unused8 is special, because multiple 8-bit registers can exist in a single higher-level register.
func (rs *Registers) unused8() (Register, bool) {
	for _, r := range regs8 {
		if !rs.rs[r].inuse {
			full := rs.rs[r.fullReg()]
			if !full.inuse || (full.inuse && full.size == 8) {
				return r, true
			}
		}
	}
	return 0, false
}

// Get requests the use of a register of a particular size.
// Registers should be Released when they are no longer needed.
func (rs *Registers) Get(size int) (Register, bool) {
	if size == 8 {
		r, ok := rs.unused8()
		if !ok {
			return 0, false
		}
		rs.rs[r].inuse = true
		rs.rs[r].size = size
		rs.rs[r.fullReg()].inuse = true
		rs.rs[r.fullReg()].size = size
		return r, true
	} else {
		for _, r := range regs64 {
			if !rs.rs[r].inuse {
				if pr, ok := r.partial(size); ok {
					rs.rs[r].inuse = true
					rs.rs[r].size = size
					return pr, true
				}
			}
		}
		return 0, false
	}
}

// InUse returns whether or not a register is currently in use.
// This includes parent/sub registers.
// For instance, if rax is in use, this will return true for rax, eax, ax, al, ah.
// And if al is in use, this will return true for rax, eax, and ax (but not ah).
func (rs *Registers) InUse(r Register) bool {
	fullRegState := rs.rs[r.fullReg()]
	if r.Width() == 8 {
		// either this register is in use, *or* a higher register is.
		// This should return false if fullreg is in use, but size == 8, meaning
		// the brother 8-bit register is in use, but this one is not.
		return rs.rs[r].inuse ||
			(fullRegState.inuse && fullRegState.size != 8)
	}
	return fullRegState.inuse
}

// Conflicts returns the set of registers currently in use that would prevent r
// from being used.
func (rs *Registers) Conflicts(r Register) []Register {
	var ret []Register
	fullRegState := rs.rs[r.fullReg()]
	if !fullRegState.inuse {
		return nil
	}
	if fullRegState.size == 8 {
		// register r is in use, and it's one or both of the 8-bit subregisters.
		subrs, ok := r.subRegisters8()
		if !ok {
			panic(fmt.Sprintf("Assembler error: Found full register %v in use with size of 8 bits, but no 8-bit subregisters exist for %v\n", r, r))
		}
		for _, subr := range subrs {
			if rs.rs[subr].inuse {
				ret = append(ret, subr)
			}
		}
		return ret
	}
	// let's find the subregister in use, based on the size set for the full register.
	preg, ok := r.fullReg().partial(fullRegState.size)
	if !ok {
		panic(fmt.Sprintf("Assembler error: Found full register %v in use with size of %d bits, but no %d-bit subregisters exist for %v\n", r, fullRegState.size, fullRegState.size, r))
	}
	return []Register{preg}
}

// Release returns a register to the pool.
func (rs *Registers) Release(r Register) {
	if r.Width() == 8 {
		rs.rs[r].inuse = false
		if !rs.rs[r.brother8()].inuse {
			// If the other 8-bit register that's part of the full register isn't in use, the full register becomes free.
			rs.rs[r.fullReg()].inuse = false
		}
	} else {
		rs.rs[r.fullReg()].inuse = false
	}
}

// Use requests the use of a specific register. Used registers should be Released when they are no
// longer needed.
func (rs *Registers) Use(r Register) bool {
	if r.Width() == 8 {
		if !rs.rs[r].inuse {
			full := rs.rs[r.fullReg()]
			if !full.inuse || (full.inuse && full.size == 8) {
				rs.rs[r].inuse = true
				rs.rs[r].size = r.Width()
				rs.rs[r.fullReg()].inuse = true
				rs.rs[r.fullReg()].size = r.Width()
				return true
			}
		}
	} else {
		if !rs.rs[r.fullReg()].inuse {
			rs.rs[r.fullReg()].inuse = true
			rs.rs[r.fullReg()].size = r.Width()
			return true
		}
	}
	return false
}
