package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
)

var out = flag.String("o", "", "Write the linked executable to this file")
var help = flag.Bool("h", false, "Print this help message.")
var importcfg = flag.String("importcfg", "", "Path to importcfg file mapping package names to .bo paths")
var listImports = flag.Bool("listimports", false, "Print all import paths from the input files (one per line) and exit. No compilation is performed.")

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

// runListImports prints the import paths declared in each input file, one per
// line, deduplicated across all inputs. Used by the build system to determine
// the dependency set of a package without compiling it.
//
// "builtin" is always emitted first (before any source-declared imports) unless
// the input files themselves belong to the builtin package — that would create
// a circular dependency.
func runListImports() {
	seen := make(map[string]bool)
	var order []string
	isBuiltinPkg := false
	for fi := 0; fi < flag.NArg(); fi++ {
		fname := flag.Arg(fi)
		file, err := os.Open(fname)
		if err != nil {
			log.Fatalf("Failed to open %s: %s", fname, err)
		}
		reader := bufio.NewReader(file)

		// Consume the leading 'package <name>' line, matching the main
		// flow's expectation that bosc files start with a package decl.
		var ln []byte
		var rerr error
		for ln, _, rerr = reader.ReadLine(); rerr == nil; ln, _, rerr = reader.ReadLine() {
			line := strings.TrimSpace(string(ln))
			if line == "" || strings.HasPrefix(line, "//") {
				continue
			}
			if strings.HasPrefix(line, "package") {
				pkgname := strings.TrimSpace(strings.TrimPrefix(line, "package"))
				if pkgname == "builtin" {
					isBuiltinPkg = true
				}
				break
			}
			log.Fatalf("%s: must start with a package name, but found %s\n", fname, line)
		}

		p := NewParser(fname, reader)
		// Peek the next top-level token and only consume it if it's `import`.
		// We avoid calling p.Next() unconditionally because parseTopLevel will
		// dispatch tok_fn into parseFn, which eagerly parses the entire function
		// body — meaning a syntax error inside any function would abort dep
		// discovery here, even though body content is irrelevant to imports.
		// Imports must precede every other top-level form, so once we see a
		// non-import token we know there are no more imports to find.
		// Skip semicolons before each check: the lexer auto-inserts ';' after
		// each import string literal, so p.current() is ';' between imports.
		for {
			for p.current().t == tok_semicolon {
				p.advance()
			}
			if p.current().t != tok_import {
				break
			}
			n, err := p.Next()
			if err != nil {
				fatalCtx("%s: parse error: %v", fname, err)
			}
			if n == nil {
				break
			}
			if !seen[n.sval] {
				seen[n.sval] = true
				order = append(order, n.sval)
			}
		}
		file.Close()
	}
	// Emit "builtin" first so the build system always depends on it, unless
	// we are compiling builtin itself (which would create a cycle).
	if !isBuiltinPkg {
		fmt.Println("builtin")
	}
	for _, p := range order {
		fmt.Println(p)
	}
}

func main() {
	// Errors carry their own file:line:col, so log's date/time prefix
	// just adds noise.
	log.SetFlags(0)
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

	if *listImports {
		runListImports()
		return
	}

	imports := map[string]string{}
	if *importcfg != "" {
		m, err := loadImportcfg(*importcfg)
		if err != nil {
			log.Fatalf("Failed to load importcfg %s: %s", *importcfg, err)
		}
		imports = m
	}

	var assembly bytes.Buffer
	for _, name := range flag.Args() {
		fmt.Printf("Compiling %s\n", name)
	}
	if err := compilePackageFiles(&assembly, flag.Args(), imports); err != nil {
		fatalCtx("Fatal: %v", err)
	}
	name := *out
	if name == "" {
		name = strings.Fields(assembly.String())[1] + ".bs"
	}
	if err := os.WriteFile(name, assembly.Bytes(), 0755); err != nil {
		log.Fatalf("Failed to write %s: %v", name, err)
	}
}
