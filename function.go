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
	inmem   bool     // Whether or not this allocation is live in memory
	reg     Register // The register the allocation is in.
	regable bool     // Whether or not this allocation can fit in a register (int vs struct{})
	addr    uint64   // if not local, the global address
	offset  int32    // if local, the offset from RBP
	rallocs *Rallocs // reference to ralloc to maintain LRU
}

// size of the data held in the register in bits. For ints/other regable types, this is the size of the data.
// For non-regable types, this is 64-bits (size of a pointer.
func (r *Ralloc) RegSize() int {
	if r.regable {
		return r.size
	}
	return 64
}

func (r *Ralloc) String() string {
	return fmt.Sprintf("%s.%s", r.rallocs.f.Name, r.sym)
}

// Location returns a MOV-able location for the allocation.
func (r *Ralloc) Location(preferRegister bool) interface{} {
	if r.inreg {
		r.rallocs.updateLRU(r.reg)
		return r.reg
	}

	if !r.regable || preferRegister {
		fmt.Printf("[Location] Preferring Register.\n")
		return r.Register()
	}
	if r.inmem {
		//fmt.Printf("%s not in register. Allocated register %s\n", r.sym, reg)
		return Indirect{Reg: R_RBP, Off: r.offset, Size: r.RegSize()}
	}
	r.inmem = true // We must mark this as inmem, since something may be loading data into this alloc.
	// TODO: minor optimization possible for uninitialized variables
	return Indirect{Reg: R_RBP, Off: r.offset, Size: r.RegSize()}
}

func (r *Ralloc) Register() Register {
	if r.inreg {
		r.rallocs.updateLRU(r.reg)
		return r.reg
	}
	if !r.regable {
		reg, ok := r.rallocs.rs.Get(64)
		if !ok {
			reg, ok = r.rallocs.Evict(64)
			if !ok {
				panic("Failed to load register") // TODO: Better error handling
				return 0
			}
		}
		r.rallocs.f.Instr("LEA", reg, Indirect{Reg: R_RBP, Off: r.offset}) // TODO: Fix size?, Size: r.RegSize()})
		r.reg = reg
		r.rallocs.regs[reg] = r
		r.rallocs.updateLRU(reg)
		r.inreg = true
		return reg
		//panic(fmt.Sprintf("Cannot load variable %s into register.", r.sym)) // TODO: Better error handling
	}
	reg, ok := r.rallocs.rs.Get(r.size)
	if !ok {
		reg, ok = r.rallocs.Evict(r.size)
		if !ok {
			panic("Failed to load register") // TODO: Better error handling
			return 0
		}
		//log.Printf("HAD TO EVICT REGISTER. EVICTED REGISTER %v", reg)
	}
	fmt.Printf("[Register] took register %v\n", reg)
	if r.inmem {
		//fmt.Printf("%s not in register. Allocated register %s\n", r.sym, reg)
		r.rallocs.f.Instr("MOV", reg, Indirect{Reg: R_RBP, Off: r.offset, Size: r.RegSize()})
		//r.inmem = false
	}
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
	if r.regable {
		r.rallocs.f.Instr("MOV", Indirect{Reg: R_RBP, Off: r.offset, Size: r.RegSize()}, r.reg)
		r.inreg = false
		r.inmem = true
		r.rallocs.rs.Release(r.reg)
		r.rallocs.removeLRU(r.reg)
		delete(r.rallocs.regs, r.reg)
		return r.reg
	} else {
		//panic("TODO: non-local allocations")
		r.inreg = false
		r.rallocs.rs.Release(r.reg)
		r.rallocs.removeLRU(r.reg)
		delete(r.rallocs.regs, r.reg)
		return r.reg
	}
}

// func (r *Ralloc) MarkNotInreg() {
// 	if !r.inreg {
// 		return
// 	}
// 	r.inreg = false
// 	r.rallocs.rs.Release(r.reg)
// 	r.rallocs.removeLRU(r.reg)
// 	delete(r.rallocs.regs, r.reg)
// }

type Rallocs struct {
	regs      map[Register]*Ralloc
	names     map[string]*Ralloc
	rs        *Registers
	f         *Function
	lru       []Register
	localoff  int32 // Current local offset in bytes from base pointer for new allocations
	freeSpace []struct {
		size   int32
		offset int32
	}
}

func NewRallocs(rs *Registers, f *Function) *Rallocs {
	return &Rallocs{
		regs:  make(map[Register]*Ralloc),
		names: make(map[string]*Ralloc),
		rs:    rs,
		f:     f,
	}
}

// space allocates 'size' bytes for a local object and returns the offset from the base pointer.
func (ra *Rallocs) space(size int32) int32 {
	out := -1
	for i := 0; i < len(ra.freeSpace); i++ {
		//fmt.Printf("Looking for size %d at 0x%x with size %d\n", size, ra.freeSpace[i].offset, ra.freeSpace[i].size)
		if ra.freeSpace[i].size == size {
			out = i
			break
		}
	}
	if out >= 0 {
		ret := ra.freeSpace[out].offset
		ra.freeSpace = append(ra.freeSpace[:out], ra.freeSpace[out+1:]...)
		return ret
	} else {
		ra.localoff += size
		return -ra.localoff
	}
}

// returnSpace returs 'size' bytes at 'offset' from the base pointer to the allocator.
func (ra *Rallocs) returnSpace(size int32, offset int32) {
	ra.freeSpace = append(ra.freeSpace, struct {
		size   int32
		offset int32
	}{
		size:   size,
		offset: offset,
	})
}

// NewLocal allocates a new local variable of size bits. Locals created with
// this function may have 'Forget' called on them to relinquish their storage.
func (ra *Rallocs) NewLocal(name string, size int) (*Ralloc, error) {
	//fmt.Printf("NewLocal %s(%d)\n", name, size)
	if _, ok := ra.names[name]; ok {
		return nil, fmt.Errorf("Ralloc %s already declared.", name)
	}
	r := &Ralloc{
		sym:     name,
		size:    size,
		regable: true,
		offset:  ra.space(int32(size) / 8),
		rallocs: ra,
	}
	ra.names[name] = r
	return r, nil
}

// func (ra *Rallocs) Temp(name string, size int) (*Ralloc, error) {
// 	//fmt.Printf("Temp %s(%d)\n", name, size)
// 	if _, ok := ra.names[name]; ok {
// 		return nil, fmt.Errorf("Ralloc %s already declared.", name)
// 	}
// 	r := &Ralloc{
// 		sym:     name,
// 		size:    size,
// 		regable: true,
// 		offset:  ra.space(int32(size) / 8),
// 		rallocs: ra,
// 	}
// 	ra.names[name] = r
// 	return r, nil
// }

// Forget forgets a local value named name, giving back its storage to the register
// and stack pool.
func (ra *Rallocs) Forget(name string) error {
	r, ok := ra.names[name]
	if !ok {
		return fmt.Errorf("Ralloc %s not declared.", name)
	}
	if r.inreg {
		r.rallocs.removeLRU(r.reg)
		delete(r.rallocs.regs, r.reg)
	}
	delete(ra.names, name)
	//fmt.Printf("Forgetting %s(%d) at offset 0x%x\n", name, r.size, r.offset)
	ra.returnSpace(int32(r.size)/8, r.offset)
	return nil
}

// Allocates 'size' bytes for variable 'name'
func (ra *Rallocs) AllocBytes(name string, size int) (*Ralloc, error) {
	if _, ok := ra.names[name]; ok {
		return nil, fmt.Errorf("Ralloc %s already declared.", name)
	}
	r := &Ralloc{
		sym:     name,
		size:    size * 8, // TODO: THIS IS A HACK. We should just be using byte size, not bit size.
		regable: false,
		offset:  ra.space(int32(size)),
		rallocs: ra,
	}
	ra.names[name] = r
	return r, nil
}

func (ra *Rallocs) AllocFor(name string) *Ralloc {
	return ra.names[name]
}

// Arg creates a new local variable for an argument passed in register r.
//
// AMD64 Calling Conventions:
// %rdi, %rsi, %rdx, %rcx, %r8, %r9, stack
func (ra *Rallocs) Arg(name string, reg Register) (*Ralloc, error) {
	if _, ok := ra.names[name]; ok {
		return nil, fmt.Errorf("Ralloc %s already declared.", name)
	}
	r := &Ralloc{
		sym:     name,
		size:    reg.Width(),
		inreg:   true,
		reg:     reg,
		regable: true,
		//offset:  -(ra.localoff + (int32(reg.width()) / 8)),
		offset:  ra.space(int32(reg.Width()) / 8),
		rallocs: ra,
	}
	//fmt.Printf("Argument %s in register %s and offset 0x%x\n", name, reg, r.offset)
	//ra.localoff += int32(reg.width()) / 8
	ra.names[name] = r
	ra.regs[reg] = r
	ra.updateLRU(reg)
	ra.rs.Use(reg)
	return r, nil
}

// Arg creates a new local variable for an argument passed on the stack at offset stacki.
//
// AMD64 Calling Conventions:
// %rdi, %rsi, %rdx, %rcx, %r8, %r9, stack
func (f *Function) StackArg(name string, stacki int) (*Ralloc, error) {
	if _, ok := f.names[name]; ok {
		return nil, fmt.Errorf("Ralloc %s already declared.", name)
	}
	r := &Ralloc{
		sym:     name,
		size:    64,
		inmem:   true,
		regable: true,
		offset:  int32((stacki+1)*8) + f.basePointerOff, // Skip over return pointer and base pointer.
		rallocs: f.Rallocs,
	}
	//fmt.Printf("STACK Argument %s at offset 0x%x\n", name, r.offset)
	f.localoff += 8
	f.names[name] = r
	return r, nil
}

func (f *Function) ArgI(name string, i int) (*Ralloc, error) {
	switch i {
	case 0:
		return f.Arg(name, R_RDI)
	case 1:
		return f.Arg(name, R_RSI)
	case 2:
		return f.Arg(name, R_RDX)
	case 3:
		return f.Arg(name, R_RCX)
	case 4:
		return f.Arg(name, R8)
	case 5:
		return f.Arg(name, R9)
	}
	return f.StackArg(name, i-6)
}

// This causes the local variable 'name' to take over register 'reg', meaning 'name' will
// immediately take on the value currently in 'reg'. Any other variable currently in 'reg' will be
// evicted.
func (ra *Rallocs) takeoverRegister(name string, reg Register) (*Ralloc, error) {
	//fmt.Printf("Took over register %s for %s\n", reg, name)
	r, ok := ra.names[name]
	if !ok {
		return nil, fmt.Errorf("No such Ralloc %s.", name)
	}

	if alloc, ok := ra.regs[reg]; ok {
		alloc.Evict()
	}
	ra.rs.Use(reg)
	r.inreg = true
	r.reg = reg
	ra.regs[reg] = r
	ra.updateLRU(reg)
	return r, nil
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
		if reg.Width() >= size {
			ra.regs[reg].Evict()
			return reg, true
		}
	}
	return 0, false
}

// EvictReg evicts whatever variable is in a register, if there is one. It does *NOT* mark the register in use
// or perform any other bookkeeping. This is mostly useful for when one wants to temporarily use a specific register
// for some calculation.
func (ra *Rallocs) EvictReg(r Register) {
	fmt.Printf("[EvictReg] In Use:\n")
	for r := range ra.regs {
		fmt.Printf("\t%v\n", r)
	}
	conflicts := ra.rs.Conflicts(r)
	fmt.Printf("[EvictReg] Evicting %v, conflicts: %v\n", r, conflicts)
	for _, cr := range conflicts {
		a, ok := ra.regs[cr]
		if !ok {
			// WARNING! Caller-saved register in use, but not as a variable.
			// It may have been USE'd manually.
			//panic(fmt.Sprintf("Registers thinks %v is in use, but Rallocs does not have a record of it.\n", cr))
			continue
		}
		a.Evict()
	}
	return
}

func (ra *Rallocs) EvictAll() {
	lru := make([]Register, len(ra.lru))
	copy(lru, ra.lru)
	for _, reg := range lru {
		ra.regs[reg].Evict()
	}
}

// EvictForCall evicts all of the caller-saved registers in preparation for a
// call
func (ra *Rallocs) EvictForCall() {
	fmt.Printf("[EVICT FOR CALL]\n")
	for _, r := range caller_saved {
		ra.EvictReg(r)
	}
}

// Acquire evicts whatever variable is in a register, if there is one. And marks the register in use.
// Registers that are Acquired must be Released, just like Use'd registers.
func (ra *Rallocs) Acquire(r Register) {
	conflicts := ra.rs.Conflicts(r)
	for _, cr := range conflicts {
		a, ok := ra.regs[cr]
		if !ok {
			panic(fmt.Sprintf("Registers thinks %v is in use, but Rallocs does not have a record of it. Has it already been acquired or used?\n", cr))
		}
		a.Evict()
	}

	if !ra.rs.Use(r) {
		panic(fmt.Sprintf("Failed to use register %v\n", r))
	}
	return

	// fmt.Printf("Acquiring register %v\n", r)
	// //fmt.Printf("REGS: %#v\n", ra.regs)
	// for k, v := range ra.regs {
	// 	fmt.Printf("REG: %v, %v\n", k, v)
	// }
	// if alloc, ok := ra.regs[r]; ok {
	// 	fmt.Printf("REG: %v in use.\n", r)
	// 	alloc.Evict()
	// } else {
	// 	fmt.Printf("REG: %v NOT in use.\n", r)
	// }
	// fmt.Printf("Resgisters says of %v: %v\n", r, ra.rs.InUse(r))

	// conflicts := ra.rs.Conflicts(r)
	// fmt.Printf("Registers says these are in use: %v\n", conflicts)

	// for _, cr := range conflicts {
	// 	fmt.Printf("Evicting %v\n", cr)
	// 	a, ok := ra.regs[cr]
	// 	if !ok {
	// 		panic(fmt.Sprintf("Registers thinks %v is in use, but Rallocs does not have a record of it.\n", cr))
	// 	}
	// 	a.Evict()
	// }

	// if !ra.rs.Use(r) {
	// 	panic(fmt.Sprintf("Failed to use register %v\n", r))
	// }
}

// func (ra *Rallocs) MarkAllNotInreg() {
// 	lru := make([]Register, len(ra.lru))
// 	copy(lru, ra.lru)
// 	for _, reg := range lru {
// 		ra.regs[reg].MarkNotInreg()
// 	}
// }

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
	Name        string
	Type        string
	SrcFile     string
	SrcLine     int
	Args        []*Var
	Symbols     []Symbol
	Relocations []Relocation
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
	basePointerOff int32

	a  *Asm
	rs *Registers
	*Rallocs
}

func (o *OFile) NewFunction(srcFile string, srcLine int, name string, args ...*Var) (*Function, error) {
	if f, ok := o.Funcs[name]; ok {
		return nil, fmt.Errorf("Function %s declared at %s:%d\n\tPreviously declared here: %s:%d",
			name, srcFile, srcLine, f.SrcFile, f.SrcLine)
	} else if o.Vars[name] != nil || o.Data[name] != nil {
		return nil, fmt.Errorf("Name %s already declared.", name)
	}

	f := &Function{
		SrcFile:        srcFile,
		SrcLine:        srcLine,
		Name:           name,
		Args:           args,
		labels:         make(map[string]int),
		a:              o.a,
		rs:             NewRegisters(),
		basePointerOff: (6 * 8), // Determined in Prologue. This is the amount of "stuff" pushed on top of any arguments before the base pointer is established.
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
	_, err := f.NewLocal("__retvalue", 64)
	if err != nil {
		return err
	}
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
	if instr == "CALL" {
		// For call, we only need to save the caller-saved registers according to
		// System V Amd64 ABI
		f.EvictForCall()
	} else {
		// When jumping, we need to make sure all locals are saved. We don't know
		// the state where we're jumping.
		f.EvictAll()
	}
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
	if instr == "CALL" {
		//fmt.Printf("###################INSTR IS [%s]\n", instr)
		f.takeoverRegister("__retvalue", R_RAX)
		// 		if err != nil {
		// 			panic(err)
		// 		}
	}
	_, err := f.a.Encode(&f.bs, instr, int32(0))
	if err != nil {
		f.errors = append(f.errors, err)
		return err
	}
	f.jumps = append(f.jumps, Relocation{Offset: uint32(f.bs.Len() - 4), Symbol: label})
	return nil
}

func (f *Function) fixLEAVar(ops []interface{}) (string, []interface{}) {
	//fmt.Printf("FIX LEA VAR\n")
	// TODO: Is this really a good idea?
	// This catches LEA's of vars into indirects, basically
	// LEA [REG+I] VAR
	// and turns it into
	// LEA TMP VAR
	// LEA [REG+I] TMP
	// because VARs are literals (addresses) and can't be
	// LEAed into memory (no LEA m64 imm64)
	if len(ops) != 2 {
		//fmt.Printf("1RETURNING %#v\n", ops)
		return "LEA", ops
	}
	ind, ok := ops[0].(Indirect)
	if !ok {
		//fmt.Printf("2RETURNING %#v\n", ops)
		return "LEA", ops
	}
	v, ok := ops[1].(*Var)
	if !ok {
		//fmt.Printf("3RETURNING %#v\n", ops)
		return "LEA", ops
	}

	tmp, err := f.NewLocal("__movvar", 64)
	if err != nil {
		panic(err)
	}
	// reg should be safe to use at least until the next instruction.
	reg := tmp.Register()
	f.Forget("__movvar")
	//fmt.Printf("v: %#v\n", v)
	f.Instr("LEA", reg, v)

	//fmt.Printf("RETURNING: %#v\n", []interface{}{ind, reg})
	return "MOV", []interface{}{ind, reg}
}

func (f *Function) Instr(instr string, ops ...interface{}) error {
	// 	fmt.Printf("INSTRUCTION [%#v] OPS [", instr)
	// 	for i := range ops {
	// 		fmt.Printf("(%s) ", ops[i])
	// 	}
	// 	fmt.Printf("]\n")

	if instr == "LEA" {
		instr, ops = f.fixLEAVar(ops)
	}

	rs, err := f.a.Encode(&f.bs, instr, ops...)
	if err != nil {
		f.errors = append(f.errors, err)
	}
	f.Relocations = append(f.Relocations, rs...)
	return err
}

func (f *Function) Resolve() error {
	if f.bodyBs != nil {
		//log.Printf("Function %s already resolved.", f.name)
		return nil
	}
	bs := f.bs.Bytes()
	for _, rel := range f.jumps {
		if loff, ok := f.labels[rel.Symbol]; ok {
			//log.Printf("APPLYING RELOCATION AT OFFSET 0x%02x to symbol %s at offset 0x%02x", rel.offset, rel.symbol, loff)
			rel.Apply(bs, int32(loff))
		} else {
			//log.Printf("Adding Relocation for symbol %s at offset 0x%02x", rel.symbol, rel.offset)
			//rel.rel_type = R_386_PC32
			f.Relocations = append(f.Relocations, rel)
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
