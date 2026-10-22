package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/knusbaum/gbasm"
)

var out = flag.String("o", "b.out", "Write the linked executable to this file")
var targetFlag = flag.String("target", "linux/amd64", "Target: linux/amd64 or darwin/arm64")

var help = flag.Bool("h", false, "Print this help message.")

func main() {
	flag.Parse()

	if *help {
		fmt.Printf("HELP MESSAGE\n")
		flag.PrintDefaults()
		return
	}

	if flag.NArg() <= 0 {
		fmt.Printf("Fatal: Expected file name to open.\n")
		os.Exit(1)
	}

	target, err := gbasm.ParseTarget(*targetFlag)
	if err != nil {
		log.Fatal(err)
	}
	var ofs []*gbasm.OFile
	pkgs := make(map[string]*gbasm.OFile)
	for i := 0; i < flag.NArg(); i++ {
		arg := flag.Arg(i)
		o, err := gbasm.ReadOFile(arg)
		if err != nil {
			fmt.Printf("Failed to read object file %s: %s\n", arg, err)
			os.Exit(1)
		}
		if o1, ok := pkgs[o.Pkgname]; ok {
			fmt.Printf("Found duplicate package %s in object files %s and %s\n", o.Pkgname, o1.Filename, o.Filename)
			os.Exit(1)
		}
		pkgs[o.Pkgname] = o
		ofs = append(ofs, o)
	}

	err = gbasm.LinkTarget(*out, target, ofs)
	if err != nil {
		log.Fatalf("Failed to write exe: %s", err)
	}
}
