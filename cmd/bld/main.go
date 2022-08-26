package main

import (
	"fmt"
	"log"
	"os"

	"github.com/knusbaum/gbasm"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Printf("Fatal: Expected file name to open.\n")
		os.Exit(1)
	}

	var ofs []*gbasm.OFile
	pkgs := make(map[string]*gbasm.OFile)
	for i := 1; i < len(os.Args); i++ {
		o, err := gbasm.ReadOFile(os.Args[i])
		if err != nil {
			fmt.Printf("Failed to read object file %s: %s\n", os.Args[i], err)
			os.Exit(1)
		}
		if o1, ok := pkgs[o.Pkgname]; ok {
			fmt.Printf("Found duplicate package %s in object files %s and %s\n", o.Pkgname, o1.Filename, o.Filename)
			os.Exit(1)
		}
		pkgs[o.Pkgname] = o
		ofs = append(ofs, o)
	}

	err := gbasm.LinkExe("out.o", gbasm.ELF, ofs)
	if err != nil {
		log.Fatalf("Failed to write exe: %s", err)
	}

	// 	// This part should be moved to the linker, but for now we'll put it here for testing.
	// 	text := gbasm.Link(ofs)
	// 	// 	for _, b := range text {
	// 	// 		fmt.Printf("%02x ", b)
	// 	// 	}
	// 	// 	fmt.Printf("\n")
	// 	err := gbasm.WriteExe("out.o", gbasm.ELF, text)
	// 	if err != nil {
	// 		log.Fatalf("Failed to write exe: %s", err)
	// 	}
}
