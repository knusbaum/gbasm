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
	// IsPub marks this top-level var/data as importable from .bos source in
	// other packages. The linker does not consult this bit.
	IsPub bool
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
	// Kind tags structured records (typedesc / iface_desc / typedesc_cache)
	// so bdump can pretty-print them. Empty for ordinary data/var blocks.
	// The linker ignores it; placement is purely Data-vs-Vars-map driven.
	Kind string
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

	// TypeAliases are Boson-level named type aliases exported by this
	// package (e.g. `type FD i64 { ... }`). bas populates this via the
	// `typealias` directive; bosc reads it during Context.Import to
	// register the alias and reconstruct its method table.
	TypeAliases map[string]*TypeAliasShape

	// Interfaces are Boson-level interface declarations exported by this
	// package (e.g. `interface reader { ... }`). bas populates this via
	// the `interface` directive; bosc reads it during Context.Import to
	// register the interface so cross-package code can declare values of
	// the qualified interface type.
	Interfaces map[string]*InterfaceShape

	// Values is the exported values-type declarations defined in this
	// package (e.g. `type io_error values (i64, byte[]) { ... }`). bas
	// populates this via the `values` directive; bosc reads it during
	// Context.Import to register the values type so cross-package code
	// can reference cases (io.io_error.NOT_FOUND) and projection casts
	// (i64(err), byte[](err)).
	Values map[string]*ValuesShape

	// Not written
	a *Asm
}

// StructShape is the wire-level description of a Boson struct: an
// ordered list of named fields, each with a rendered type string.
// The type string is whatever ASTType.String() emitted on the
// producer side; the importer reparses it with parseTypeString.
type StructShape struct {
	Name   string
	IsPub  bool
	Fields []FieldShape
	// MethodNames lists the bare method names declared on the struct so a
	// cross-package importer can reconstruct the method table from the
	// already-imported function records (parallel to TypeAliasShape).
	MethodNames []string
}

type FieldShape struct {
	Name string
	Type string
}

// TypeAliasShape is the wire-level description of a Boson type alias
// (e.g. `type FD i64 { ... }`). Underlying is the ASTType.String()
// form of the underlying type. MethodNames lists the bare method names
// so the importer can reconstruct the method table from the already-
// imported function signatures.
type TypeAliasShape struct {
	Name        string
	IsPub       bool
	Underlying  string
	MethodNames []string
}

// InterfaceShape is the wire-level description of a Boson interface
// declaration. Each method records its name, the ordered parameter
// list (the first param is conventionally the receiver, with type
// "*self" or similar), and the return type. Param/Return type strings
// are ASTType.String() output, reparsed by the importer.
type InterfaceShape struct {
	Name    string
	IsPub   bool
	Methods []InterfaceMethodShape
}

// InterfaceMethodShape captures one method signature inside an
// InterfaceShape.
type InterfaceMethodShape struct {
	Name   string
	Params []FieldShape // FieldShape{Name: paramName, Type: paramType}
	Return string
}

// ValuesShape is the wire-level description of a Boson values type.
// TagType records the private tag's representation (v1 is always
// "i64"). Cases lists symbolic names in declaration order with their
// compiler-private dense tag values, so importers can resolve
// `pkg.io_error.NOT_FOUND` to the right tag without re-running the
// producer's ToAST. Projections lists the projection-target types in
// the declared signature order; the projection table symbol an
// importer constructs from the index, mirroring the producer's
// projectionSymbolName scheme (pkg.__projection_<type>__<index>).
// MethodNames lists the bare method names so the importer can
// reconstitute the method table from the already-imported function
// signatures (same pattern as TypeAliasShape).
type ValuesShape struct {
	Name        string
	IsPub       bool
	TagType     string
	Cases       []ValuesCaseShape
	Projections []ProjectionShape
	MethodNames []string
}

// ValuesCaseShape is one entry in a values type's case list.
type ValuesCaseShape struct {
	Name string
	Tag  int64
}

// ProjectionShape is one entry in a values type's projection
// signature. TargetType is the declared destination type (ASTType.String()
// form, reparsed on import). The table symbol is derived from
// (pkg, values-type-name, index) at use time.
type ProjectionShape struct {
	TargetType string
}

func NewOFile(name string, pkgname string) (*OFile, error) {
	a, err := LoadAsm(AMD64)
	if err != nil {
		return nil, err
	}
	return &OFile{
		Filename:    name,
		Pkgname:     pkgname,
		Types:       make(map[string]*TypeDescr),
		Data:        make(map[string]*Var),
		Vars:        make(map[string]*Var),
		Funcs:       make(map[string]*Function),
		Structs:     make(map[string]*StructShape),
		TypeAliases: make(map[string]*TypeAliasShape),
		Interfaces:  make(map[string]*InterfaceShape),
		Values:      make(map[string]*ValuesShape),
		a:           a, // TODO: Hard coded for now. This should be a parameter and written to the ofile.
	}, nil
}

// AddValues registers a Boson values type for export.
func (o *OFile) AddValues(name, tagType string, cases []ValuesCaseShape, projections []ProjectionShape, methodNames []string, isPub bool) error {
	if o.Values[name] != nil {
		return fmt.Errorf("Values type %s already declared.", name)
	}
	if o.Structs[name] != nil || o.TypeAliases[name] != nil || o.Interfaces[name] != nil {
		return fmt.Errorf("Name %s already declared.", name)
	}
	o.Values[name] = &ValuesShape{
		Name:        name,
		IsPub:       isPub,
		TagType:     tagType,
		Cases:       cases,
		Projections: projections,
		MethodNames: methodNames,
	}
	return nil
}

// AddStruct registers a Boson struct definition for export. Returns
// an error if the name is already in use by any kind of declaration.
func (o *OFile) AddStruct(name string, fields []FieldShape, methodNames []string, isPub bool) error {
	if o.Structs[name] != nil {
		return fmt.Errorf("Struct %s already declared.", name)
	}
	if o.Vars[name] != nil || o.Data[name] != nil || o.Funcs[name] != nil {
		return fmt.Errorf("Name %s already declared.", name)
	}
	o.Structs[name] = &StructShape{Name: name, IsPub: isPub, Fields: fields, MethodNames: methodNames}
	return nil
}

// AddTypeAlias registers a Boson type alias for export.
func (o *OFile) AddTypeAlias(name, underlying string, methodNames []string, isPub bool) error {
	if o.TypeAliases[name] != nil {
		return fmt.Errorf("TypeAlias %s already declared.", name)
	}
	o.TypeAliases[name] = &TypeAliasShape{Name: name, IsPub: isPub, Underlying: underlying, MethodNames: methodNames}
	return nil
}

// AddInterface registers a Boson interface declaration for export.
func (o *OFile) AddInterface(name string, methods []InterfaceMethodShape, isPub bool) error {
	if o.Interfaces[name] != nil {
		return fmt.Errorf("Interface %s already declared.", name)
	}
	o.Interfaces[name] = &InterfaceShape{Name: name, IsPub: isPub, Methods: methods}
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
func (o *OFile) AddVar(name, vtype string, val interface{}, isPub bool) error {
	if o.Vars[name] != nil || o.Data[name] != nil || o.Funcs[name] != nil {
		return fmt.Errorf("Name %s already declared.", name)
	}
	switch v := val.(type) {
	case string:
		val = []byte(v)
	}
	var bs bytes.Buffer
	binary.Write(&bs, binary.LittleEndian, val)
	o.Vars[name] = &Var{Name: name, IsPub: isPub, VType: vtype, Val: bs.Bytes()}
	return nil
}

// AddData declares a piece of immutable data of type vtype at package scope.
func (o *OFile) AddData(name, vtype string, val interface{}, isPub bool) error {
	if o.Vars[name] != nil || o.Data[name] != nil || o.Funcs[name] != nil {
		return fmt.Errorf("Name %s already declared.", name)
	}
	switch v := val.(type) {
	case string:
		val = []byte(v)
	}
	var bs bytes.Buffer
	binary.Write(&bs, binary.LittleEndian, val)
	o.Data[name] = &Var{Name: name, IsPub: isPub, VType: vtype, Val: bs.Bytes()}
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
