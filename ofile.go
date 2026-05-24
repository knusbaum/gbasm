package gbasm

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
)

type TypeDescr struct {
	Name string
	// Properties should be used to distinguish things
	// like level of indirection, constantness, etc.
	// Unlike description, these Properties must match in order for
	// one TypeDescr to be considered equal to another.
	Properties  []string
	Description []byte
}

type Var struct {
	Name string
	// VType is a string and must be parsed by the compiler/linker to ensure it matches some
	// TypeDescr.
	VType string
	Val   []byte
	// Relocs is a list of pointer-slot fixups within Val. Each entry
	// asks the linker to write the absolute virtual address of Symbol
	// (plus Addend) into the 8-byte slot at Offset. Used to express
	// pointers in static data — slice headers, struct fields holding
	// addresses of other globals, and so on.
	Relocs []DataReloc
}

// DataReloc is a per-Var pointer-slot fixup, applied by the linker
// when the var is placed in the data section. Distinct from
// Relocation, which encodes a 32-bit PC-relative displacement
// emitted by code; DataReloc writes an 8-byte absolute virtual
// address into Val[Offset:Offset+8].
type DataReloc struct {
	Offset uint32 // within the owning Var.Val
	Symbol string // target var/data name
	Addend int64  // added to the resolved address
}

// Apply patches the 8-byte pointer slot at Offset with the absolute
// virtual address. The linker computes targetVA from the symbol's
// final placement; bs is the byte slice the Var was written into.
func (r *DataReloc) Apply(bs []byte, targetVA uint64) {
	v := uint64(int64(targetVA) + r.Addend)
	binary.LittleEndian.PutUint64(bs[r.Offset:], v)
}

// func (v *Var) Offset() int32 {
// 	panic("VAR OFFSET")
// 	//return 0x0CAFEF0D
// }

type Symbol struct {
	Name   string
	Offset uint32
}

type reltype int

const (
	REL_NONE    reltype = iota
	R_386_PC32          // Calculate relative offset to location
	RA_386_PC32         // Calculate relative offset to location plus addend
)

type Relocation struct {
	Offset uint32
	Symbol string
	// Addend is added to the computed PC-relative displacement before
	// it's written into the 4-byte slot. Used for forms like
	// `[symbol+N]` where the encoder records the relocation against
	// the bare symbol and stashes N in the addend so the linker can
	// produce sym + N − pc.
	Addend int32
}

func (r *Relocation) Apply(bs []byte, value int32) {
	target := value - int32(r.Offset) - 4 + r.Addend
	bs = bs[r.Offset:]
	bss := bytes.NewBuffer(bs)
	bss.Truncate(0)
	err := binary.Write(bss, binary.LittleEndian, target)
	if err != nil {
		panic(err) // This shouldn't happen. We're writing to a memory buffer.
	}
}

type OFile struct {
	Filename  string
	Pkgname   string
	ExeFormat string
	Types     map[string]*TypeDescr
	Data      map[string]*Var
	Vars      map[string]*Var
	Funcs     map[string]*Function
	// Structs are Boson-level struct definitions exported by this
	// package. Each StructShape stores the field names paired with
	// their rendered type strings (parseable by bosc on import).
	// bas populates this via the `struct` directive; bosc reads it
	// during Context.Import to register cross-package struct types.
	Structs map[string]*StructShape

	// Not written
	a *Asm
}

// StructShape is the wire-level description of a Boson struct: an
// ordered list of named fields, each with a rendered type string.
// The type string is whatever ASTType.String() emitted on the
// producer side; the importer reparses it with parseTypeString.
type StructShape struct {
	Name   string
	Fields []FieldShape
}

type FieldShape struct {
	Name string
	Type string
}

func NewOFile(name string, pkgname string) (*OFile, error) {
	a, err := LoadAsm(AMD64)
	if err != nil {
		return nil, err
	}
	return &OFile{
		Filename: name,
		Pkgname:  pkgname,
		Types:    make(map[string]*TypeDescr),
		Data:     make(map[string]*Var),
		Vars:     make(map[string]*Var),
		Funcs:    make(map[string]*Function),
		Structs:  make(map[string]*StructShape),
		a:        a, // TODO: Hard coded for now. This should be a parameter and written to the ofile.
	}, nil
}

// AddStruct registers a Boson struct definition for export. Returns
// an error if the name is already in use by any kind of declaration.
func (o *OFile) AddStruct(name string, fields []FieldShape) error {
	if o.Structs[name] != nil {
		return fmt.Errorf("Struct %s already declared.", name)
	}
	if o.Vars[name] != nil || o.Data[name] != nil || o.Funcs[name] != nil {
		return fmt.Errorf("Name %s already declared.", name)
	}
	o.Structs[name] = &StructShape{Name: name, Fields: fields}
	return nil
}

func (o *OFile) Type(name string, properties []string, description []byte) error {
	if o.Types[name] != nil {
		return fmt.Errorf("Type %s already declared.", name)
	}
	o.Types[name] = &TypeDescr{
		Name:        name,
		Properties:  properties,
		Description: description,
	}
	return nil
}

// AddVar declares a mutable variable of type vtype at package scope.
func (o *OFile) AddVar(name, vtype string, val interface{}) error {
	if o.Vars[name] != nil || o.Data[name] != nil || o.Funcs[name] != nil {
		return fmt.Errorf("Name %s already declared.", name)
	}
	switch v := val.(type) {
	case string:
		val = []byte(v)
	}
	var bs bytes.Buffer
	binary.Write(&bs, binary.LittleEndian, val)
	o.Vars[name] = &Var{Name: name, VType: vtype, Val: bs.Bytes()}
	return nil
}

// AddData declares a piece of immutable data of type vtype at package scope.
func (o *OFile) AddData(name, vtype string, val interface{}) error {
	if o.Vars[name] != nil || o.Data[name] != nil || o.Funcs[name] != nil {
		return fmt.Errorf("Name %s already declared.", name)
	}
	switch v := val.(type) {
	case string:
		val = []byte(v)
	}
	var bs bytes.Buffer
	binary.Write(&bs, binary.LittleEndian, val)
	o.Data[name] = &Var{Name: name, VType: vtype, Val: bs.Bytes()}
	return nil
}

func (o *OFile) VarFor(name string) *Var {
	if v := o.Vars[name]; v != nil {
		return v
	}
	return o.Data[name]
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
	o.Filename = filename
	// Stamp every function with its owning package so the linker can namespace.
	for _, fn := range o.Funcs {
		fn.Pkgname = o.Pkgname
	}
	return o, nil
}

func (o *OFile) Output() error {
	f, err := os.Create(o.Filename)
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
