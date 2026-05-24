package gbasm

import (
	"bytes"
	"fmt"
	"log"
	"strings"
	//"github.com/knusbaum/gbasm/elf"
)

type platform int

func (p platform) String() string {
	switch p {
	case MACHO:
		return "MACH-O"
	default:
		return "UNKNOWN"
	}
}

const (
	MACHO platform = iota
	ELF
)

// func WriteExe(exename string, p platform, text []byte) error {
// 	switch p {
// 	case MACHO:
// 		return macho.WriteMacho(exename, text)
// 	case ELF:
// 		return elf.WriteELF(exename, text)
// 	default:
// 		return fmt.Errorf("Cannot write executable for platform %s", p)
// 	}
// }

func SectSymsToElf64_Symbols(syms []SectSym) []Elf64_Symbol {
	var ret []Elf64_Symbol
	for _, s := range syms {
		ret = append(ret, Elf64_Symbol{
			Name:    s.Name,
			Type:    s.Type,
			Address: s.Address,
			Size:    s.Size,
		})
	}
	return ret
}

func LinkedBinToElfSections(b LinkedBin) []Elf64_Section {
	var ret []Elf64_Section
	for _, s := range b.Sections {
		es := Elf64_Section{
			name:     s.Name,
			s_type:   SHT_PROGBITS,
			flags:    SHF_ALLOC,
			addr:     Elf64_Addr(s.Offset),
			data:     s.val,
			loadable: true,
			syms:     SectSymsToElf64_Symbols(s.symbols),
		}
		switch s.permission {
		case F_WRITE:
			es.flags |= SHF_WRITE
		case F_EXEC:
			es.flags |= SHF_EXECINSTR
		}
		ret = append(ret, es)
	}
	return ret
}

func LinkExe(exename string, p platform, os []*OFile) error {
	switch p {
	case MACHO:
		panic("MACH NOT IMPLEMENTED.\n")
	case ELF:
		bin := Link(os, ENTRY_ADDR)

		//return WriteELF(exename, bin)
		WriteElf(exename, LinkedBinToElfSections(bin))
		return nil
	default:
		return fmt.Errorf("Cannot write executable for platform %s", p)
	}
}

// F_READ allows reading.
// F_WRITE allows writing and implies F_READ
// F_EXEC allows executing and implies F_READ
const (
	F_READ = iota
	F_WRITE
	F_EXEC
)

const (
	SYM_FUNC = iota
	SYM_OBJECT
)

type SectSym struct {
	Name    string
	Type    int
	Address uint64 // This should be the final virtual address for the object
	Size    int
}

type Section struct {
	Name       string
	Offset     uint64
	val        []byte
	permission int
	symbols    []SectSym
}

type LinkedBin struct {
	Sections []*Section
}

// qualify returns "<pkg>.<name>" for non-empty pkg, otherwise just name.
func qualify(pkg, name string) string {
	if pkg == "" {
		return name
	}
	return pkg + "." + name
}

// qualifyDataRelocs rewrites bare-name targets in v.Relocs to be
// package-qualified, mirroring the qualification that function.go
// applies to code-side Relocation Symbols. A target containing a
// '.' is assumed to already be qualified (cross-package reference)
// and left untouched.
func qualifyDataRelocs(v *Var, pkgname string) {
	for i := range v.Relocs {
		s := v.Relocs[i].Symbol
		if !strings.ContainsRune(s, '.') {
			v.Relocs[i].Symbol = qualify(pkgname, s)
		}
	}
}

func Link(os []*OFile, textoff uint64) LinkedBin {
	funcs := make(map[string]*Function)
	data := make(map[string]*Var)
	vars := make(map[string]*Var)
	for _, o := range os {
		for fname, f := range o.Funcs {
			// All defined functions live under their qualified name (pkg.func).
			// The compiler always emits fully-qualified call symbols, so the
			// linker never needs to resolve a bare function name.
			if o.Pkgname == "" {
				log.Fatalf("object file %s has no package name", o.Filename)
			}
			qname := qualify(o.Pkgname, fname)
			if _, ok := funcs[qname]; ok {
				log.Fatalf("Duplicate definitions of function %s", qname)
			}
			funcs[qname] = f
		}
		for dname, v := range o.Data {
			qname := qualify(o.Pkgname, dname)
			if _, ok := data[qname]; ok {
				log.Fatalf("Duplicate definitions of data %s", qname)
			}
			qualifyDataRelocs(v, o.Pkgname)
			data[qname] = v
		}
		for vname, v := range o.Vars {
			qname := qualify(o.Pkgname, vname)
			if _, ok := vars[qname]; ok {
				log.Fatalf("Duplicate definitions of data %s", qname)
			}
			qualifyDataRelocs(v, o.Pkgname)
			vars[qname] = v
		}
	}

	needfnm := make(map[*Function]struct{})
	needfn := make([]*Function, 1)

	addNeeded := func(f *Function) {
		if _, ok := needfnm[f]; ok {
			// Already have f
			return
		}
		needfnm[f] = struct{}{}
		needfn = append(needfn, f)
	}

	//needvar := make([]*Var, 0)
	// The ELF entry point. The init runtime package _init exports `start`,
	// which calls the user's main and exits.
	main, ok := funcs["_init.start"]
	if !ok {
		log.Fatalf("No such function _init.start (the entry point must be defined in package _init)")
	}
	needfn[0] = main
	relocations := make([]Relocation, 0)
	funclocs := make(map[string]uint32)
	varlocs := make(map[string]uint32)
	datalocs := make(map[string]uint32)

	funcsyms := make([]SectSym, 0)
	varsyms := make([]SectSym, 0)
	datasyms := make([]SectSym, 0)

	var fnbs, varbs, databs bytes.Buffer

	// addVar / addData place a var/data block into the appropriate
	// section if not already present, and recursively follow any
	// data-relocation targets so that everything a placed var points
	// to is itself placed. Data relocations can target functions
	// (function-pointer init) too; those go through addNeeded.
	var addVar, addData func(string)
	addNeededDataReloc := func(target string) {
		if _, ok := funcs[target]; ok {
			if _, placed := funclocs[target]; !placed {
				addNeeded(funcs[target])
			}
			return
		}
		if _, ok := vars[target]; ok {
			addVar(target)
			return
		}
		if _, ok := data[target]; ok {
			addData(target)
			return
		}
		log.Fatalf("No such symbol %s referenced by data relocation", target)
	}
	addVar = func(name string) {
		if _, placed := varlocs[name]; placed {
			return
		}
		v := vars[name]
		loc := uint32(varbs.Len())
		varbs.Write(v.Val)
		varlocs[name] = loc
		varsyms = append(varsyms, SectSym{
			Name:    name,
			Type:    SYM_OBJECT,
			Address: uint64(loc),
			Size:    len(v.Val),
		})
		for _, dr := range v.Relocs {
			addNeededDataReloc(dr.Symbol)
		}
	}
	addData = func(name string) {
		if _, placed := datalocs[name]; placed {
			return
		}
		v := data[name]
		loc := uint32(databs.Len())
		databs.Write(v.Val)
		datalocs[name] = loc
		datasyms = append(datasyms, SectSym{
			Name:    name,
			Type:    SYM_OBJECT,
			Address: uint64(loc),
			Size:    len(v.Val),
		})
		for _, dr := range v.Relocs {
			addNeededDataReloc(dr.Symbol)
		}
	}

	for len(needfn) > 0 {
		current := needfn[0]
		needfn = needfn[1:]
		//fmt.Printf("Adding function [%s]\n", current.Name)
		fbs, err := current.Body()
		if err != nil {
			log.Fatalf("Failed to resolve function body: %s", err)
		}
		foffset := uint32(fnbs.Len())
		// All relocations are qualified, so funclocs uses qualified names.
		qname := qualify(current.Pkgname, current.Name)
		funclocs[qname] = foffset
		funcsyms = append(funcsyms, SectSym{
			Name:    qname,
			Type:    SYM_FUNC,
			Address: uint64(foffset),
			Size:    len(fbs),
		})
		for _, r := range current.Relocations {
			if fn, ok := funcs[r.Symbol]; ok {
				if _, ok := funclocs[r.Symbol]; !ok {
					addNeeded(fn)
				}
			} else if _, ok := vars[r.Symbol]; ok {
				addVar(r.Symbol)
			} else if _, ok := data[r.Symbol]; ok {
				addData(r.Symbol)
			} else {
				log.Fatalf("No such symbol %s", r.Symbol)
			}
			r.Offset += foffset
			relocations = append(relocations, r)
		}
		_, err = fnbs.Write(fbs)
		if err != nil {
			log.Fatalf("Failed to write body: %s", err)
		}
	}
	text := fnbs.Bytes()
	vardat := varbs.Bytes()
	datadat := databs.Bytes()
	varoff := (textoff + uint64(len(text)) + 0x1000) & 0xFFFFFFFFFFFFF000
	dataoff := (varoff + uint64(len(vardat)) + 0x1000) & 0xFFFFFFFFFFFFF000

	for i := range funcsyms {
		funcsyms[i].Address += textoff
	}
	for i := range varsyms {
		varsyms[i].Address += varoff
	}
	for i := range datasyms {
		datasyms[i].Address += dataoff
	}

	for _, r := range relocations {
		if value, ok := funclocs[r.Symbol]; ok {
			//log.Printf("APPLYING RELOCATION AT OFFSET 0x%02x to symbol %s at offset 0x%02x", r.offset, r.symbol, value)
			r.Apply(text, int32(value))
		} else if value, ok := varlocs[r.Symbol]; ok {
			value += uint32(varoff - textoff)
			//log.Printf("APPLYING RELOCATION AT OFFSET 0x%02x to symbol %s at offset 0x%02x", r.offset, r.symbol, value)
			r.Apply(text, int32(value))
		} else if value, ok := datalocs[r.Symbol]; ok {
			value += uint32(dataoff - textoff)
			//log.Printf("APPLYING RELOCATION AT OFFSET 0x%02x to symbol %s at offset 0x%02x", r.offset, r.symbol, value)
			r.Apply(text, int32(value))
		} else {
			log.Fatalf("THIS SHOULD NEVER HAPPEN. WE CHECKED ABOVE.")
		}
	}

	// Data-section relocations: for each placed var (and data block),
	// walk its Relocs and write the 8-byte absolute VA of each target
	// into the appropriate pointer slot.
	resolveTargetVA := func(target string) uint64 {
		if off, ok := funclocs[target]; ok {
			return textoff + uint64(off)
		}
		if off, ok := varlocs[target]; ok {
			return varoff + uint64(off)
		}
		if off, ok := datalocs[target]; ok {
			return dataoff + uint64(off)
		}
		log.Fatalf("Data relocation target %s was not placed (linker bug — addVar should have followed the reloc)", target)
		return 0
	}
	for name, loc := range varlocs {
		v := vars[name]
		for _, dr := range v.Relocs {
			dr.Apply(vardat[loc:], resolveTargetVA(dr.Symbol))
		}
	}
	for name, loc := range datalocs {
		v := data[name]
		for _, dr := range v.Relocs {
			dr.Apply(datadat[loc:], resolveTargetVA(dr.Symbol))
		}
	}
	//return text
	return LinkedBin{
		Sections: []*Section{
			&Section{Name: ".text", Offset: textoff, permission: F_EXEC, symbols: funcsyms, val: text},
			&Section{Name: ".data", Offset: varoff, permission: F_WRITE, symbols: varsyms, val: vardat},
			&Section{Name: ".bss", Offset: dataoff, permission: F_READ, symbols: datasyms, val: datadat},
		},
	}
}
