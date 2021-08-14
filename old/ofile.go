package gbasm

import (
	"fmt"
	"os"
)

type Instr int

const (
	MOV Instr = iota
	ADD
	SUB
	MUL
	DIV
	PUSH
	POP
	CALL
	RET
)

func (i Instr) String() string {
	switch i {
	case MOV:
		return "MOV"
	case ADD:
		return "ADD"
	case SUB:
		return "SUB"
	case MUL:
		return "MUL"
	case DIV:
		return "DIV"
	case PUSH:
		return "PUSH"
	case POP:
		return "POP"
	case CALL:
		return "CALL"
	case RET:
		return "RET"
	}
	return "UNKNOWN"
}

type Register int

const (
	// General Purpose
	AL Register = iota
	AH
	AX
	EAX
	RAX
	BL
	BH
	BX
	EBX
	RBX
	CL
	CH
	CX
	ECX
	RCX
	DL
	DH
	DX
	EDX
	RDX

	// Segment registers
	CS
	DS
	ES
	FS
	GS
	SS

	// Index and pointers
	SI
	ESI
	RSI
	DI
	EDI
	RDI
	BP
	EBP
	RBP
	IP
	EIP
	RIP
	SP
	ESP
	RSP

	// Indicator
	EFLAGS
)

func (r Register) String() string {
	switch r {
	case AL:
		return "AL"
	case AH:
		return "AH"
	case AX:
		return "AX"
	case EAX:
		return "EAX"
	case RAX:
		return "RAX"
	case BL:
		return "BL"
	case BH:
		return "BH"
	case BX:
		return "BX"
	case EBX:
		return "EBX"
	case RBX:
		return "RBX"
	case CL:
		return "CL"
	case CH:
		return "CH"
	case CX:
		return "CX"
	case ECX:
		return "ECX"
	case RCX:
		return "RCX"
	case DL:
		return "DL"
	case DH:
		return "DH"
	case DX:
		return "DX"
	case EDX:
		return "EDX"
	case RDX:
		return "RDX"
	case CS:
		return "CS"
	case DS:
		return "DS"
	case ES:
		return "ES"
	case FS:
		return "FS"
	case GS:
		return "GS"
	case SS:
		return "SS"
	case SI:
		return "SI"
	case ESI:
		return "ESI"
	case RSI:
		return "RSI"
	case DI:
		return "DI"
	case EDI:
		return "EDI"
	case RDI:
		return "RDI"
	case BP:
		return "BP"
	case EBP:
		return "EBP"
	case RBP:
		return "RBP"
	case IP:
		return "IP"
	case EIP:
		return "EIP"
	case RIP:
		return "RIP"
	case SP:
		return "SP"
	case ESP:
		return "ESP"
	case RSP:
		return "RSP"
	case EFLAGS:
		return "EFLAGS"
	default:
		return "UNKNOWN"
	}
}

type Indirect struct {
	Reg Register
	Off int32
}

type BaseIndexScale struct {
	Base  Register
	Index Register
	Scale byte
}

type MemLoc uint64

type TypeDescr struct {
	name string
	// Properties should be used to distinguish things
	// like level of indirection, constantness, etc.
	// Unlike description, these properties must match in order for
	// one TypeDescr to be considered equal to another.
	properties  []string
	description []byte
}

type Var struct {
	name string
	// vtype is a string and must be parsed by the compiler/linker to ensure it matches some
	// TypeDescr.
	vtype string
}

type Instruction struct {
	instr Instr
	args  []interface{}
}

type Function struct {
	name    string
	srcFile string
	srcLine int
	args    []*Var
	body    []*Instruction
	bodyBs  []byte
}

type OFile struct {
	filename  string
	pkgname   string
	exeformat string
	types     map[string]*TypeDescr
	data      map[string]*Var
	vars      map[string]*Var
	funcs     map[string]*Function
}

func NewOFile(name string, pkgname string) *OFile {
	return &OFile{
		filename: name,
		pkgname:  pkgname,
		types:    make(map[string]*TypeDescr),
		data:     make(map[string]*Var),
		vars:     make(map[string]*Var),
		funcs:    make(map[string]*Function),
	}
}

func (o *OFile) Type(name string, properties []string, description []byte) error {
	if o.types[name] != nil {
		return fmt.Errorf("Type %s already declared.", name)
	}
	o.types[name] = &TypeDescr{
		name:        name,
		properties:  properties,
		description: description,
	}
	return nil
}

func (o *OFile) Var(name, vtype string) error {
	if o.vars[name] != nil || o.data[name] != nil || o.funcs[name] != nil {
		return fmt.Errorf("Name %s already declared.", name)
	}
	o.vars[name] = &Var{name, vtype}
	return nil
}

func (o *OFile) Data(name, vtype string) error {
	if o.vars[name] != nil || o.data[name] != nil || o.funcs[name] != nil {
		return fmt.Errorf("Name %s already declared.", name)
	}
	o.data[name] = &Var{name, vtype}
	return nil
}

func (o *OFile) NewFunction(srcFile string, srcLine int, name string, args ...*Var) (*Function, error) {
	if f, ok := o.funcs[name]; ok {
		return nil, fmt.Errorf("Function %s declared at %s:%d\n\tPreviously declared here: %s:%d",
			name, srcFile, srcLine, f.srcFile, f.srcLine)
	}
	f := &Function{srcFile: srcFile, srcLine: srcLine, name: name, args: args}
	o.funcs[name] = f
	return f, nil
}

func (f *Function) Move(src, dst interface{}) {
	f.body = append(f.body, &Instruction{MOV, []interface{}{src, dst}})
}

func (f *Function) Add(o1, o2 interface{}) {
	f.body = append(f.body, &Instruction{ADD, []interface{}{o1, o2}})
}

func (f *Function) Sub(o1, o2 interface{}) {
	f.body = append(f.body, &Instruction{SUB, []interface{}{o1, o2}})
}

func (f *Function) Mul(o1, o2 interface{}) {
	f.body = append(f.body, &Instruction{MUL, []interface{}{o1, o2}})
}

func (f *Function) Div(o1, o2 interface{}) {
	f.body = append(f.body, &Instruction{DIV, []interface{}{o1, o2}})
}

func (f *Function) Push(o1 Register) {
	f.body = append(f.body, &Instruction{PUSH, []interface{}{o1}})
}

func (f *Function) Pop(o1 Register) {
	f.body = append(f.body, &Instruction{POP, []interface{}{o1}})
}

func (f *Function) Call(o1 interface{}) {
	f.body = append(f.body, &Instruction{CALL, []interface{}{o1}})
}

func (f *Function) Ret() {
	f.body = append(f.body, &Instruction{RET, []interface{}{}})
}

func ReadOFile(filename string) (*OFile, error) {
	f, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	o, err := readOFile(f)
	if err != nil {
		return nil, err
	}
	o.filename = filename
	return o, nil
}

func (o *OFile) Output() error {
	f, err := os.Create(o.filename)
	if err != nil {
		return err
	}
	defer f.Close()
	err = writeOFile(f, o)
	if err != nil {
		return err
	}
	return nil
}
