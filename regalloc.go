package gbasm

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

var regs8 = []Register{R_AL, R_AH, R_BL, R_BH, R_CL, R_CH, R_DL, R_DH}
var regs64 = []Register{R_RBX, R_RCX, R_RDX, R_RSP, R_RBP, R_RSI, R_RDI, R8, R9, R10, R11, R12, R13, R14, R15, R_RAX}

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
