package gbasm

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

type Ralloc struct {
	sym     string   // Name of the allocation
	size    int      // Size of the allocation in bits
	inreg   bool     // Whether or not this allocation is is a register
	reg     Register // The register the allocation is in.
	local   bool     // Whether or not this allocation is function-local
	addr    uint64   // if not local, the global address
	offset  int32    // if local, the offset from RBP
	rallocs *Rallocs // reference to ralloc to maintain LRU
}

func (r *Ralloc) String() string {
	return fmt.Sprintf("%s.%s", r.rallocs.f.name, r.sym)
}

func (r *Ralloc) Register() Register {
	if r.inreg {
		r.rallocs.updateLRU(r.reg)
		return r.reg
	}
	reg, ok := r.rallocs.rs.Get(r.size)
	if !ok {
		reg, ok = r.rallocs.Evict(r.size)
		if !ok {
			panic("Failed to load register") // TODO: Better error handling
			return 0
		}
		//log.Printf("HAD TO EVICT REGISTER. EVICTED REGISTER %v", reg)
	} else {
		//log.Printf("RALLOCS ALLOCATED REGISTER %v", reg)
	}
	r.rallocs.f.Instr("MOV", reg, Indirect{Reg: R_RBP, Off: r.offset})
	r.reg = reg
	r.rallocs.regs[reg] = r
	r.rallocs.updateLRU(reg)
	r.inreg = true
	return reg
}

func (r *Ralloc) Evict() Register {
	if !r.inreg {
		panic("Already evicted")
	}
	if r.local {
		r.rallocs.f.Instr("MOV", Indirect{Reg: R_RBP, Off: r.offset}, r.reg)
		r.inreg = false
		r.rallocs.rs.Release(r.reg)
		r.rallocs.removeLRU(r.reg)
		delete(r.rallocs.regs, r.reg)
		return r.reg
	} else {
		panic("TODO: non-local allocations")
	}
}

func (r *Ralloc) MarkNotInreg() {
	if !r.inreg {
		return
	}
	r.inreg = false
	r.rallocs.rs.Release(r.reg)
	r.rallocs.removeLRU(r.reg)
	delete(r.rallocs.regs, r.reg)
}

type Rallocs struct {
	regs     map[Register]*Ralloc
	names    map[string]*Ralloc
	rs       *Registers
	f        *Function
	lru      []Register
	localoff int32
}

func NewRallocs(rs *Registers, f *Function) *Rallocs {
	return &Rallocs{
		regs:  make(map[Register]*Ralloc),
		names: make(map[string]*Ralloc),
		rs:    rs,
		f:     f,
	}
}

func (ra *Rallocs) NewLocal(name string, size int) (*Ralloc, error) {
	if _, ok := ra.names[name]; ok {
		return nil, fmt.Errorf("Ralloc %s already declared.", name)
	}
	r := &Ralloc{
		sym:     name,
		size:    size,
		local:   true,
		offset:  -(ra.localoff + (int32(size) / 8)),
		rallocs: ra,
	}
	ra.localoff += int32(size) / 8
	ra.names[name] = r
	return r, nil
}

func (ra *Rallocs) AllocFor(name string) *Ralloc {
	return ra.names[name]
}

func (ra *Rallocs) updateLRU(r Register) {
	var k int
	for i := range ra.lru {
		if ra.lru[i] == r {
			continue
		}
		ra.lru[k] = ra.lru[i]
		k++
	}
	ra.lru = append(ra.lru[:k], r)
}

func (ra *Rallocs) removeLRU(r Register) {
	var k int
	for i := range ra.lru {
		if ra.lru[i] == r {
			continue
		}
		ra.lru[k] = ra.lru[i]
		k++
	}
	ra.lru = ra.lru[:k]
}

func (ra *Rallocs) Evict(size int) (Register, bool) {
	for _, reg := range ra.lru {
		if reg.width() >= size {
			ra.regs[reg].Evict()
			return reg, true
		}
	}
	return 0, false
}

func (ra *Rallocs) EvictAll() {
	lru := make([]Register, len(ra.lru))
	copy(lru, ra.lru)
	for _, reg := range lru {
		ra.regs[reg].Evict()
	}
}

func (ra *Rallocs) MarkAllNotInreg() {
	lru := make([]Register, len(ra.lru))
	copy(lru, ra.lru)
	for _, reg := range lru {
		ra.regs[reg].MarkNotInreg()
	}
}

// func (ra *Rallocs) NewGlobal(name string, size int, addr uint64) (*Ralloc, error) {
// 	if _, ok := ra.names[name]; ok {
// 		return nil, fmt.Errorf("Ralloc %s already declared.", name)
// 	}
// 	r := &Ralloc{
// 		sym:     name,
// 		size:    size,
// 		local:   false,
// 		addr:    addr,
// 		rallocs: ra,
// 	}
// 	ra.names[name] = r
// 	return r, nil
// }

type Function struct {
	name        string
	srcFile     string
	srcLine     int
	args        []*Var
	symbols     []Symbol
	relocations []Relocation
	bodyBs      []byte

	// The following fields are used to resolve jumps and labels within a function.
	// These are *NOT* written or read to/from object files.
	// Jumps need to be resolved with resolve() before being written to an object file
	// or the jumps will not be correct.
	bs             bytes.Buffer
	labels         map[string]int
	jumps          []Relocation
	errors         []error
	localsLocation uint32

	a  *Asm
	rs *Registers
	*Rallocs
}

func (o *OFile) NewFunction(srcFile string, srcLine int, name string, args ...*Var) (*Function, error) {
	if f, ok := o.Funcs[name]; ok {
		return nil, fmt.Errorf("Function %s declared at %s:%d\n\tPreviously declared here: %s:%d",
			name, srcFile, srcLine, f.srcFile, f.srcLine)
	} else if o.vars[name] != nil || o.data[name] != nil {
		return nil, fmt.Errorf("Name %s already declared.", name)
	}

	f := &Function{
		srcFile: srcFile,
		srcLine: srcLine,
		name:    name,
		args:    args,
		labels:  make(map[string]int),
		a:       o.a,
		rs:      NewRegisters(),
	}
	f.Rallocs = NewRallocs(f.rs, f)
	o.Funcs[name] = f
	return f, nil
}

// Use marks a register as in-use. It will not be allocated by the register allocator. Returns true
// if the register could be allocated and false if it is already in use.
func (f *Function) Use(r Register) bool {
	return f.rs.Use(r)
}

// Release returns a register to the pool for use by the register allocator. Registers that are
// marked as in-use with the Use method should be Released when they are no longer needed.
func (f *Function) Release(r Register) {
	f.rs.Release(r)
}

// Get finds an unused register of size and marks it as in-use. When the register is no longer
// needed it should be Released.
func (f *Function) Get(size int) (Register, bool) {
	return f.rs.Get(size)
}

// Functions assume System V x86_64 calling convention.
func (f *Function) Prologue() error {
	f.rs.Use(R_RBP)
	f.rs.Use(R_RSP)
	// TODO: If we get smarter about encoding, we can determine which of these registers we *need* to save
	// based on which ones the function uses instead of just saving all of them.
	// However, for now, this is simple and gives us safe access to any register in a function.
	f.Instr("PUSH", R_RBP)
	f.Instr("PUSH", R_RBX)
	f.Instr("PUSH", R12)
	f.Instr("PUSH", R13)
	f.Instr("PUSH", R14)
	f.Instr("PUSH", R15)
	f.Instr("MOV", R_RBP, R_RSP)
	f.Instr("SUB", R_RSP, uint32(0))
	f.localsLocation = uint32(f.bs.Len() - 4)
	return nil // TODO: Fix errors
}

func (f *Function) Epilogue() error {
	f.rs.Release(R_RBP)
	f.rs.Release(R_RSP)
	bs := f.bs.Bytes()
	bs = bs[f.localsLocation:]
	bss := bytes.NewBuffer(bs)
	bss.Truncate(0)
	binary.Write(bss, binary.LittleEndian, f.Rallocs.localoff)
	// This un-does the "SUB" in the prologue, but is unnecessary since we
	// immediately load RSP from RBP.
	//f.f.Instr("ADD", R_RSP, uint32(f.Rallocs.localoff))
	f.Instr("MOV", R_RSP, R_RBP)
	f.Instr("POP", R15)
	f.Instr("POP", R14)
	f.Instr("POP", R13)
	f.Instr("POP", R12)
	f.Instr("POP", R_RBX)
	return f.Instr("POP", R_RBP)
}

func (f *Function) Label(l string) error {
	// When creating a label, we need to make sure all locals get reloaded. We don't know where we're jumping from.
	f.EvictAll()
	if _, ok := f.labels[l]; ok {
		err := fmt.Errorf("Label %s already exists.", l)
		if err != nil {
			f.errors = append(f.errors, err)
		}
		return err
	}
	f.labels[l] = f.bs.Len()
	return nil
}

// Instr should be one of the jump instructions like JMP, JNE, JGT, CALL etc.
func (f *Function) Jump(instr string, label string) error {
	// When jumping, we need to make sure all locals are saved. We don't know the state where we're jumping.
	f.EvictAll()
	// TODO: If there is already a label, we could apply the jump here rather than creating a
	// relocation and doing it later. It's not clear if that is a worthwhile optimization or not.
	// 	if loff, ok := f.labels[label]; ok {
	// 		log.Printf("WE HAVE LABEL %s -> %d", label, loff)
	// 		joff := f.bs.Len() + 6 //(jump imm32 is 6 bytes)
	// 		jmp := joff - loff
	// 		err := f.a.Encode(&f.bs, instr, int32(-jmp))
	// 		if err != nil {
	// 			f.errors = append(f.errors, err)
	// 		}
	// 		return err
	// 	}
	_, err := f.a.Encode(&f.bs, instr, int32(0))
	if err != nil {
		f.errors = append(f.errors, err)
		return err
	}
	f.jumps = append(f.jumps, Relocation{offset: uint32(f.bs.Len() - 4), symbol: label})
	return nil
}

func (f *Function) Instr(instr string, ops ...interface{}) error {
	fmt.Printf("INSTRUCTION [%#v] OPS [%#v]\n", instr, ops)
	for i := range ops {
		switch v := ops[i].(type) {
		case string:
			fmt.Printf("GOT A STRING ARG: %v\n", v)
		}
	}
	rs, err := f.a.Encode(&f.bs, instr, ops...)
	if err != nil {
		f.errors = append(f.errors, err)
	}
	f.relocations = append(f.relocations, rs...)
	return err
}

func (f *Function) Resolve() error {
	if f.bodyBs != nil {
		//log.Printf("Function %s already resolved.", f.name)
		return nil
	}
	bs := f.bs.Bytes()
	for _, rel := range f.jumps {
		if loff, ok := f.labels[rel.symbol]; ok {
			//log.Printf("APPLYING RELOCATION AT OFFSET 0x%02x to symbol %s at offset 0x%02x", rel.offset, rel.symbol, loff)
			rel.Apply(bs, int32(loff))
		} else {
			//log.Printf("Adding Relocation for symbol %s at offset 0x%02x", rel.symbol, rel.offset)
			//rel.rel_type = R_386_PC32
			f.relocations = append(f.relocations, rel)
		}
	}
	f.jumps = make([]Relocation, 0)
	f.bodyBs = bs
	//log.Printf("FUNCTION %s BODYBS LEN: %d", f.name, len(f.bodyBs))
	return nil
}

func (f *Function) Body() ([]byte, error) {
	//log.Printf("RESOLVING.")
	f.Resolve()
	//log.Printf("RESOLVED.")
	if len(f.errors) != 0 {
		if len(f.errors) == 1 {
			return nil, f.errors[0]
		}
		return nil, fmt.Errorf("Multiple errors: %v", f.errors)
	}
	//log.Printf("RETURNING BODY LEN: %d", len(f.bodyBs))
	return f.bodyBs, nil
}
