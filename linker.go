package gbasm

import (
	"bytes"
	"fmt"
	"log"

	"github.com/knusbaum/gbasm/elf"
	"github.com/knusbaum/gbasm/macho"
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

func WriteExe(exename string, p platform, text []byte) error {
	switch p {
	case MACHO:
		return macho.WriteMacho(exename, text)
	case ELF:
		return elf.WriteELF(exename, text)
	default:
		return fmt.Errorf("Cannot write executable for platform %s", p)
	}
}

func Link(os []*OFile) []byte {
	funcs := make(map[string]*Function)
	for _, o := range os {
		for fname, f := range o.Funcs {
			if _, ok := funcs[fname]; ok {
				log.Fatalf("Duplicate definitions of function %s", fname)
			}
			funcs[fname] = f
			fmt.Printf("FUNCTION %s.%s\n", o.Pkgname, fname)
		}
		for dname, v := range o.data {
			fmt.Printf("DATA %s %s (%d bytes)\n", dname, v.vtype, len(v.val))
		}
	}

	to_write := make([]*Function, 1)
	main, ok := funcs["start"]
	if !ok {
		log.Fatalf("No such function main")
	}
	to_write[0] = main
	relocations := make([]Relocation, 0)
	funclocs := make(map[string]uint32)
	var bs bytes.Buffer
	for len(to_write) > 0 {
		current := to_write[0]
		to_write = to_write[1:]
		fbs, err := current.Body()
		if err != nil {
			log.Fatalf("Failed to resolve function body: %s", err)
		}
		foffset := uint32(bs.Len())
		funclocs[current.name] = foffset
		for _, r := range current.relocations {
			log.Printf("LINKER FOUND RELOCATION AT OFFSET %d to symbol %s", r.offset, r.symbol)
			r.offset += foffset
			relocations = append(relocations, r)
			if _, ok := funclocs[r.symbol]; !ok {
				depf, ok := funcs[r.symbol]
				if !ok {
					log.Fatalf("No such function %s", r.symbol)
				}
				to_write = append(to_write, depf)
			}
		}
		_, err = bs.Write(fbs)
		if err != nil {
			log.Fatalf("Failed to write body: %s", err)
		}
	}
	text := bs.Bytes()
	for _, r := range relocations {
		value, ok := funclocs[r.symbol]
		if !ok {
			log.Fatalf("THIS SHOULD NEVER HAPPEN. WE CHECKED ABOVE.")
		}
		//log.Printf("APPLYING RELOCATION AT OFFSET 0x%02x to symbol %s at offset 0x%02x", r.offset, r.symbol, value)
		r.Apply(text, int32(value))
	}
	return text
}
