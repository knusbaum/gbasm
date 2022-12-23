package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/knusbaum/gbasm"
)

func main() {
	flag.Parse()

	// 	if *help {
	// 		fmt.Printf("HELP MESSAGE\n")
	// 		flag.PrintDefaults()
	// 		return
	// 	}

	if flag.NArg() <= 0 {
		fmt.Printf("Fatal: Expected file name to open.\n")
		os.Exit(1)
	}

	for i := 0; i < flag.NArg(); i++ {
		arg := flag.Arg(i)
		o, err := gbasm.ReadOFile(arg)
		if err != nil {
			fmt.Printf("Failed to read object file %s: %s\n", arg, err)
			os.Exit(1)
		}

		fmt.Printf("Read from %s\n", arg)
		fmt.Printf("\tFilename: %s\n", o.Filename)
		fmt.Printf("\tPkgname: %s\n", o.Pkgname)
		fmt.Printf("\tExeFormat: %s\n", o.ExeFormat)
		fmt.Printf("\tTypes:\n")
		for v, t := range o.Types {
			fmt.Printf("\t\t%s :: %s\n", v, t.Name)
			for _, p := range t.Properties {
				fmt.Printf("\t\t\t%s\n", p)
			}
			if len(t.Description) > 0 {
				fmt.Printf("\t\t\tDescription: %v\n", t.Description)
			}
		}
		fmt.Printf("\tData:\n")
		for d, v := range o.Data {
			fmt.Printf("\t\t%s :: %s = %v\n", d, v.VType, v.Val)
			if d != v.Name {
				fmt.Printf("\t\t\tWARNING: Data name [%s] does not match variable name [%s].\n", d, v.Name)
			}
		}
		fmt.Printf("\tVars:\n")
		for d, v := range o.Vars {
			fmt.Printf("\t\t%s :: %s = %v\n", d, v.VType, v.Val)
			if d != v.Name {
				fmt.Printf("\t\t\tWARNING: Data name [%s] does not match variable name [%s].\n", d, v.Name)
			}
		}
		fmt.Printf("\tFunctions:\n")
		for f, v := range o.Funcs {
			fmt.Printf("\t\t%s\n", f)
			if f != v.Name {
				fmt.Printf("\t\t\tWARNING: Bound name [%s] does not match function name [%s].\n", f, v.Name)
			}
			fmt.Printf("\t\t\tType: %s\n", v.Type)
			fmt.Printf("\t\t\tSrcFile: %s\n", v.SrcFile)
			fmt.Printf("\t\t\tSrcLine: %d\n", v.SrcLine)
			fmt.Printf("\t\t\tArgs:\n")
			for _, a := range v.Args {
				fmt.Printf("\t\t\t\t%s :: %s = %v\n", a.Name, a.VType, a.Val)
			}
			fmt.Printf("\t\t\tSymbols:\n")
			for _, s := range v.Symbols {
				fmt.Printf("\t\t\t\t%s @ %d\n", s.Name, s.Offset)
			}
			fmt.Printf("\t\t\tRelocations:\n")
			for _, s := range v.Relocations {
				fmt.Printf("\t\t\t\t0x%X -> %s\n", s.Offset, s.Symbol)
			}
			fmt.Printf("\t\t\t\tBODY:\n")
			bs, err := v.Body()
			if err != nil {
				fmt.Printf("\t\t\t\tError resolving body: %v\n", err)
			} else {
				for _, b := range bs {
					fmt.Printf("%X ", b)
				}
				fmt.Printf("\n")
			}
		}
	}
}
