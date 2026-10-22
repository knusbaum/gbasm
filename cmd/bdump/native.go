package main

import (
	"bytes"
	"debug/macho"
	"fmt"
)

func dumpNative(image []byte) {
	f, err := macho.NewFile(bytes.NewReader(image))
	if err != nil {
		fmt.Printf("\tInvalid Mach-O payload: %v\n", err)
		return
	}
	defer f.Close()
	fmt.Printf("\tNative Mach-O: %d bytes, CPU %s\n", len(image), f.Cpu)
	for _, s := range f.Sections {
		fmt.Printf("\t\t%s,%s: %d bytes, %d relocations\n", s.Seg, s.Name, s.Size, len(s.Relocs))
		for _, r := range s.Relocs {
			name := fmt.Sprintf("section/%d", r.Value)
			if r.Extern && f.Symtab != nil && int(r.Value) < len(f.Symtab.Syms) {
				name = f.Symtab.Syms[r.Value].Name
			}
			fmt.Printf("\t\t\t+%#x kind=%d pcrel=%t width=%d %s\n", r.Addr, r.Type, r.Pcrel, 1<<r.Len, name)
		}
	}
}
