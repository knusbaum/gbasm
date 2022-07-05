package gbasm

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
)

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
	val   []byte
}

type Symbol struct {
	name   string
	offset uint32
}

type reltype int

const (
	REL_NONE    reltype = iota
	R_386_PC32          // Calculate relative offset to location
	RA_386_PC32         // Calculate relative offset to location plus addend
)

type Relocation struct {
	offset   uint32
	rel_type reltype
	symbol   string
	addend   int32
}

func (r *Relocation) Apply(bs []byte, value int32) {
	target := value - int32(r.offset) - 4
	if r.rel_type == RA_386_PC32 {
		target += r.addend
	}
	bs = bs[r.offset:]
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
	exeformat string
	types     map[string]*TypeDescr
	data      map[string]*Var
	vars      map[string]*Var
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
		types:    make(map[string]*TypeDescr),
		data:     make(map[string]*Var),
		vars:     make(map[string]*Var),
		Funcs:    make(map[string]*Function),
		a:        a, // TODO: Hard coded for now. This should be a parameter and written to the ofile.
	}, nil
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

// Var declares a mutable variable of type vtype at package scope.
func (o *OFile) Var(name, vtype string, val interface{}) error {
	if o.vars[name] != nil || o.data[name] != nil || o.Funcs[name] != nil {
		return fmt.Errorf("Name %s already declared.", name)
	}
	switch v := val.(type) {
	case string:
		val = []byte(v)
	}
	var bs bytes.Buffer
	binary.Write(&bs, binary.LittleEndian, val)
	o.vars[name] = &Var{name, vtype, bs.Bytes()}
	return nil
}

// Data declares a piece of immutable data of type vtype at package scope.
func (o *OFile) Data(name, vtype string, val interface{}) error {
	if o.vars[name] != nil || o.data[name] != nil || o.Funcs[name] != nil {
		return fmt.Errorf("Name %s already declared.", name)
	}
	switch v := val.(type) {
	case string:
		val = []byte(v)
	}
	var bs bytes.Buffer
	binary.Write(&bs, binary.LittleEndian, val)
	o.data[name] = &Var{name, vtype, bs.Bytes()}
	return nil
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
