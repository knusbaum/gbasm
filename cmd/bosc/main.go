package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
)

var out = flag.String("o", "", "Write the linked executable to this file")
var help = flag.Bool("h", false, "Print this help message.")
var importcfg = flag.String("importcfg", "", "Path to importcfg file mapping package names to .bo paths")

// loadImportcfg reads a file with lines of the form `name=path/to/file.bo` and
// returns a map from package name to file path. Blank lines and lines beginning
// with '#' are skipped.
func loadImportcfg(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	m := make(map[string]string)
	scanner := bufio.NewScanner(f)
	lineno := 0
	for scanner.Scan() {
		lineno++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			return nil, fmt.Errorf("%s:%d: expected name=path, got %q", path, lineno, line)
		}
		name := strings.TrimSpace(line[:eq])
		fpath := strings.TrimSpace(line[eq+1:])
		if name == "" || fpath == "" {
			return nil, fmt.Errorf("%s:%d: empty name or path", path, lineno)
		}
		m[name] = fpath
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return m, nil
}

func main() {
	flag.Parse()

	if *help {
		fmt.Printf("HELP MESSAGE\n")
		flag.PrintDefaults()
		return
	}

	if flag.NArg() < 1 {
		fmt.Printf("Fatal: Expected file name to open.\n")
		os.Exit(1)
	}

	imports := map[string]string{}
	if *importcfg != "" {
		m, err := loadImportcfg(*importcfg)
		if err != nil {
			log.Fatalf("Failed to load importcfg %s: %s", *importcfg, err)
		}
		imports = m
	}

	var pkgname string
	var of *os.File
	var wrotePkg bool

	if out != nil && *out != "" {
		f, err := os.OpenFile(*out, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0755)
		if err != nil {
			log.Fatalf("Failed to create file %s: %s", *out, err)
		}
		defer f.Close()
		//fmt.Printf("WRITING FILE %s\n", out)
		of = f
	}

	for fi := 0; fi < flag.NArg(); fi++ {
		fmt.Printf("Compiling %s\n", flag.Arg(fi))
		file, err := os.Open(flag.Arg(fi))
		if err != nil {
			fmt.Printf("Fatal: %s", err)
		}
		defer file.Close()

		reader := bufio.NewReader(file)

		var ln []byte
		for ln, _, err = reader.ReadLine(); err == nil; ln, _, err = reader.ReadLine() {
			//fmt.Printf("LINE: %s\n", ln)
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
					//fmt.Printf("WRITING FILE %s\n", out)
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
		p := NewParser(flag.Arg(fi), reader)
		//c := NewVContext()
		//ctx := NewCompileContext()
		var bs bytes.Buffer
		var asts []AST
		actx := NewContext()
		actx.SetPkgname(pkgname)
		for {
			n, err := p.Next()
			if err != nil {
				log.Fatalf("Parse Error: %v\n", err)
			}
			if n == nil {
				break
			}
			if n.t == n_import {
				// n.sval is the package name from `import "name"`. Look up its
				// .bo path in the importcfg.
				pkgName := n.sval
				path, ok := imports[pkgName]
				if !ok {
					log.Fatalf("import %q: not found in importcfg\n", pkgName)
				}
				err = actx.Import(pkgName, path)
				if err != nil {
					log.Fatalf("%v\n", err)
				}
				continue
			}
			a, err := n.ToAST(actx)
			if err != nil {
				fmt.Printf("Failed to parse: %v\n", err)
				os.Exit(1)
			}
			asts = append(asts, a)
			// err = Validate(n, c)
			// if err != nil {
			// 	log.Fatalf("Validation error: %v\n", err)
			// 	return
			// }
			// //fmt.Printf("WRITING %#v\n", n)
			//n.replaceStrings(ctx)
			// n.compile(ctx, &bs, valnew{})
		}

		for _, a := range asts {
			err := Compile(&bs, actx, a)
			if err != nil {
				fmt.Printf("Fatal: %v\n", err)
				os.Exit(1)
			}
		}

		// for name, b := range actx.bindings {
		// 	fmt.Printf("var %v: %#v\n", name, b)
		// }
		// for name, s := range actx.structs {
		// 	fmt.Printf("struct %v: %#v\n", name, s)
		// }
		// for name, f := range actx.funcs {
		// 	fmt.Printf("func %v: %#v\n", name, f)
		// }
		actx.WriteStrings(of)
		io.Copy(of, &bs)
	}
}
