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
	//rel_type reltype
	Symbol string
	//addend int32
}

func (r *Relocation) Apply(bs []byte, value int32) {
	target := value - int32(r.Offset) - 4
	//if r.rel_type == RA_386_PC32 {
	//	target += r.addend
	//}
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

	// Not written
	a *Asm
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
		a:        a, // TODO: Hard coded for now. This should be a parameter and written to the ofile.
	}, nil
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
	o.Vars[name] = &Var{name, vtype, bs.Bytes()}
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
	o.Data[name] = &Var{name, vtype, bs.Bytes()}
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
