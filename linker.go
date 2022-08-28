package gbasm

import (
	"bytes"
	"fmt"
	"log"
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

func Link(os []*OFile, textoff uint64) LinkedBin {
	funcs := make(map[string]*Function)
	data := make(map[string]*Var)
	vars := make(map[string]*Var)
	for _, o := range os {
		for fname, f := range o.Funcs {
			if _, ok := funcs[fname]; ok {
				log.Fatalf("Duplicate definitions of function %s", fname)
			}
			funcs[fname] = f
			fmt.Printf("FUNCTION %s.%s\n", o.Pkgname, fname)
		}
		for dname, v := range o.data {
			if _, ok := data[dname]; ok {
				log.Fatalf("Duplicate definitions of data %s", dname)
			}
			data[dname] = v
			fmt.Printf("DATA %s %s (%d bytes)\n", dname, v.vtype, len(v.val))
		}
		for vname, v := range o.vars {
			if _, ok := vars[vname]; ok {
				log.Fatalf("Duplicate definitions of data %s", vname)
			}
			vars[vname] = v
			fmt.Printf("VAR %s %s (%d bytes)\n", vname, v.vtype, len(v.val))
		}
	}

	needfn := make([]*Function, 1)
	//needvar := make([]*Var, 0)
	main, ok := funcs["start"]
	if !ok {
		log.Fatalf("No such function main")
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
	for len(needfn) > 0 {
		current := needfn[0]
		needfn = needfn[1:]
		fbs, err := current.Body()
		if err != nil {
			log.Fatalf("Failed to resolve function body: %s", err)
		}
		foffset := uint32(fnbs.Len())
		fmt.Printf("ADDING %s to funclocs.\n", current.name)
		funclocs[current.name] = foffset
		funcsyms = append(funcsyms, SectSym{
			Name:    current.name,
			Type:    SYM_FUNC,
			Address: uint64(foffset),
			Size:    len(fbs),
		})
		for _, r := range current.relocations {
			log.Printf("Found relocation for symbol %s\n", r.symbol)
			if fn, ok := funcs[r.symbol]; ok {
				log.Printf("Symbol %s is listed in the object funcs.\n", r.symbol)
				// Function relocation
				log.Printf("LINKER FOUND RELOCATION AT OFFSET %d to function %s", r.offset, r.symbol)
				if _, ok := funclocs[r.symbol]; !ok {
					//log.Printf("%s is *NOT* in funclocs. Appending function %#v to needfn.\n", r.symbol, fn)
					needfn = append(needfn, fn)
				}
			} else if v, ok := vars[r.symbol]; ok {
				// Variable relocation
				log.Printf("LINKER FOUND RELOCATION AT OFFSET %d to vars %s", r.offset, r.symbol)
				if _, ok := varlocs[r.symbol]; !ok {
					loc := uint32(varbs.Len())
					varbs.Write(v.val)
					varlocs[r.symbol] = loc
					varsyms = append(varsyms, SectSym{
						Name:    r.symbol,
						Type:    SYM_OBJECT,
						Address: uint64(loc),
						Size:    len(v.val),
					})
				}
			} else if v, ok := data[r.symbol]; ok {
				// Data relocation
				log.Printf("LINKER FOUND RELOCATION AT OFFSET %d to data %s", r.offset, r.symbol)
				if _, ok := datalocs[r.symbol]; !ok {
					loc := uint32(databs.Len())
					databs.Write(v.val)
					datalocs[r.symbol] = loc
					datasyms = append(datasyms, SectSym{
						Name:    r.symbol,
						Type:    SYM_OBJECT,
						Address: uint64(loc),
						Size:    len(v.val),
					})
				}
			} else {
				log.Fatalf("No such symbol %s", r.symbol)
			}
			r.offset += foffset
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
		if value, ok := funclocs[r.symbol]; ok {
			log.Printf("APPLYING RELOCATION AT OFFSET 0x%02x to symbol %s at offset 0x%02x", r.offset, r.symbol, value)
			r.Apply(text, int32(value))
		} else if value, ok := varlocs[r.symbol]; ok {
			value += uint32(varoff - textoff)
			log.Printf("APPLYING RELOCATION AT OFFSET 0x%02x to symbol %s at offset 0x%02x", r.offset, r.symbol, value)
			r.Apply(text, int32(value))
			//log.Fatalf("CANNOT RELOCATE SYMBOL %s! VAR RELOCATIONS NOT WORKING YET!\n", r.symbol)
		} else if value, ok := datalocs[r.symbol]; ok {
			value += uint32(dataoff - textoff)
			log.Printf("APPLYING RELOCATION AT OFFSET 0x%02x to symbol %s at offset 0x%02x", r.offset, r.symbol, value)
			r.Apply(text, int32(value))
			//log.Fatalf("CANNOT RELOCATE SYMBOL %s! VAR RELOCATIONS NOT WORKING YET!\n", r.symbol)
		} else {
			log.Fatalf("THIS SHOULD NEVER HAPPEN. WE CHECKED ABOVE.")
		}

		// 		value, ok := funclocs[r.symbol]
		// 		if !ok {
		// 			log.Fatalf("THIS SHOULD NEVER HAPPEN. WE CHECKED ABOVE.")
		// 		}
		// 		//log.Printf("APPLYING RELOCATION AT OFFSET 0x%02x to symbol %s at offset 0x%02x", r.offset, r.symbol, value)
		// 		r.Apply(text, int32(value))
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
