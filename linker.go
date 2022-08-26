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

func LinkExe(exename string, p platform, os []*OFile) error {
	switch p {
	case MACHO:
		panic("MACH NOT IMPLEMENTED.\n")
	case ELF:
		bin := Link(os, ENTRY_ADDR)
		return WriteELF(exename, bin)
	default:
		return fmt.Errorf("Cannot write executable for platform %s", p)
	}
}

type Section struct {
	Name   string
	Offset uint64
	val    []byte
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
	//datalocs := make(map[string]uint32)

	var fnbs, varbs bytes.Buffer
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
				}
			} else if _, ok := data[r.symbol]; ok {
				// Data relocation
				log.Printf("LINKER FOUND RELOCATION AT OFFSET %d to data %s", r.offset, r.symbol)
				log.Fatalf("DATA LINKS NOT SUPPORTED YET.\n")
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
	varoff := (textoff + uint64(len(text)) + 0x1000) & 0xFFFFFFFFFFFFF000
	for _, r := range relocations {
		if value, ok := funclocs[r.symbol]; ok {
			log.Printf("APPLYING RELOCATION AT OFFSET 0x%02x to symbol %s at offset 0x%02x", r.offset, r.symbol, value)
			r.Apply(text, int32(value))
		} else if _, ok := varlocs[r.symbol]; ok {
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
			&Section{Name: "text", Offset: textoff, val: text},
			&Section{Name: "var", Offset: varoff, val: varbs.Bytes()},
		},
	}
}
