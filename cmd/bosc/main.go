package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Printf("Fatal: Expected file name to open.\n")
		os.Exit(1)
	}

	var pkgname string
	var of *os.File
	var wrotePkg bool
	for fi := 1; fi < len(os.Args); fi++ {
		fmt.Printf("Opening %s\n", os.Args[fi])
		file, err := os.Open(os.Args[fi])
		if err != nil {
			fmt.Printf("Fatal: %s", err)
		}
		defer file.Close()

		reader := bufio.NewReader(file)

		var ln []byte
		for ln, _, err = reader.ReadLine(); err == nil; ln, _, err = reader.ReadLine() {
			fmt.Printf("LINE: %s\n", ln)
			line := strings.TrimSpace(string(ln))
			if line == "" {
				continue
			}
			if strings.HasPrefix(line, "//") {
				continue
			}
			if strings.HasPrefix(line, "package") {
				pn := strings.TrimSpace(strings.TrimPrefix(line, "package"))
				if pkgname != "" && pkgname != pn {
					log.Fatalf("Found more than one package in input file: %s, %s\n", pkgname, pn)
				}
				pkgname = pn
				if of == nil {
					out := pn + ".bs"
					f, err := os.OpenFile(out, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0755)
					if err != nil {
						log.Fatalf("Failed to create file %s: %s", out, err)
					}
					defer f.Close()
					fmt.Printf("WRITING FILE %s\n", out)
					of = f
				}
				break
			}
			log.Fatalf("bosc files must start with a package name, but found %s\n", line)
		}
		if of == nil {
			log.Fatalf("No out file.\n")
		}
		if !wrotePkg {
			fmt.Fprintf(of, "package %s\n\n", pkgname)
		}
		p := NewParser(os.Args[fi], reader)
		c := NewVContext()
		ctx := NewCompileContext()
		var bs bytes.Buffer
		for {
			n, err := p.Next()
			if err != nil {
				log.Fatalf("Parse Error: %v\n", err)
			}
			if n == nil {
				break
			}
			if n.t == n_import {
				err := c.Import(n.sval)
				if err != nil {
					log.Fatalf("%v\n", err)
				}
				continue
			}
			err = Validate(n, c)
			if err != nil {
				log.Fatalf("Validation error: %v\n", err)
				return
			}
			//fmt.Printf("WRITING %#v\n", n)
			n.replaceStrings(ctx)
			n.compile(ctx, &bs, "")
		}
		ctx.WriteStrings(of)
		io.Copy(of, &bs)
	}
}
