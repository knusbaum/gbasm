package main

import (
	"bufio"
	"os"
	"strings"
)

// Decl is a single top-level declaration extracted from a source file.
type Decl struct {
	Kind      DeclKind // function, struct, typealias, var, data, etc.
	Name      string   // declared name
	Signature string   // raw signature line (for funcs: "fn foo(...) bar"; for structs: "struct foo {...}", etc.)
	Body      string   // for structs/types: the full multi-line body (verbatim)
	Doc       string   // joined comment block immediately preceding the decl
	SrcFile   string   // file in which this decl appears (relative to package dir)
	SrcLine   int      // 1-based line number of the declaration's first line
}

type DeclKind int

const (
	DeclFunc DeclKind = iota
	DeclStruct
	DeclTypeAlias
	DeclVar
	DeclData
	DeclAsmFunc // .bs `function` declaration
	DeclAsmVar
	DeclAsmData
)

func (k DeclKind) String() string {
	switch k {
	case DeclFunc:
		return "fn"
	case DeclStruct:
		return "struct"
	case DeclTypeAlias:
		return "type"
	case DeclVar:
		return "var"
	case DeclData:
		return "data"
	case DeclAsmFunc:
		return "function"
	case DeclAsmVar:
		return "var"
	case DeclAsmData:
		return "data"
	}
	return "?"
}

// PackageScan is the result of scanning one package directory.
type PackageScan struct {
	ImportPath string // e.g. "stdlib/io", "string", "_init"
	Dir        string // absolute filesystem path
	PkgName    string // declared package name (from `package <name>`)
	DocComment string // top-of-file comment block, if any
	Decls      []Decl
	SrcFiles   []string // relative file names in this package
}

// ScanPackage scans every .bos and .bs file under dir and returns a unified
// view of the package. importPath is the BOSONPATH-relative import string
// for display purposes; it is not used by the scanner itself.
func ScanPackage(dir, importPath string) (*PackageScan, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	ps := &PackageScan{Dir: dir, ImportPath: importPath}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		ext := ""
		switch {
		case strings.HasSuffix(name, ".bos"):
			ext = ".bos"
		case strings.HasSuffix(name, ".bs"):
			ext = ".bs"
		default:
			continue
		}
		path := dir + "/" + name
		if err := scanFile(path, name, ext, ps); err != nil {
			return nil, err
		}
		ps.SrcFiles = append(ps.SrcFiles, name)
	}

	return ps, nil
}

// scanFile reads a single source file and appends its decls (and possibly a
// top-of-file doc comment) to ps.
func scanFile(path, relname, ext string, ps *PackageScan) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	var pending []string // accumulated comment lines immediately preceding the next decl
	var sawDecl bool     // have we seen any decl in this file yet?
	lineno := 0

	flushPending := func() string {
		if len(pending) == 0 {
			return ""
		}
		s := strings.Join(pending, "\n")
		pending = nil
		return s
	}

	for scanner.Scan() {
		lineno++
		raw := scanner.Text()
		line := strings.TrimSpace(raw)

		// Blank line resets the pending comment block (godoc semantics: only
		// comments *immediately* above a decl attach to it).
		if line == "" {
			pending = nil
			continue
		}

		// Comment: accumulate.
		if strings.HasPrefix(line, "//") {
			text := strings.TrimSpace(strings.TrimPrefix(line, "//"))
			pending = append(pending, text)
			continue
		}

		// package <name>
		if strings.HasPrefix(line, "package") && !sawDecl {
			ps.PkgName = strings.TrimSpace(strings.TrimPrefix(line, "package"))
			// The pending block above the package declaration is the package's
			// doc comment.
			if ps.DocComment == "" {
				ps.DocComment = flushPending()
			} else {
				flushPending()
			}
			sawDecl = true
			continue
		}

		// import "..."  — ignored for doc purposes
		if strings.HasPrefix(line, "import") {
			flushPending()
			continue
		}

		if ext == ".bos" {
			if d, ok := tryBosDecl(scanner, line, &lineno, relname); ok {
				d.Doc = flushPending()
				ps.Decls = append(ps.Decls, d)
				continue
			}
		} else { // .bs
			if d, ok := tryBsDecl(scanner, line, &lineno, relname); ok {
				d.Doc = flushPending()
				ps.Decls = append(ps.Decls, d)
				continue
			}
		}

		// Non-blank, non-comment, non-decl line. Drop pending and move on.
		pending = nil
	}
	return scanner.Err()
}

// tryBosDecl recognizes top-level .bos declarations and consumes any
// additional lines they span. Returns (decl, true) on match.
func tryBosDecl(s *bufio.Scanner, head string, lineno *int, srcfile string) (Decl, bool) {
	startLine := *lineno

	// fn name(...) ret { ... }
	if strings.HasPrefix(head, "fn ") {
		name := extractName(head[3:], "(")
		// Signature is the header up to but not including the opening brace.
		sig := head
		if i := strings.Index(sig, "{"); i >= 0 {
			sig = strings.TrimSpace(sig[:i])
		}
		// Skip the body so we resume at the next top-level decl.
		skipBracedBody(s, head, lineno)
		return Decl{
			Kind:      DeclFunc,
			Name:      name,
			Signature: sig,
			SrcFile:   srcfile,
			SrcLine:   startLine,
		}, true
	}

	// struct Name { ... }
	if strings.HasPrefix(head, "struct ") {
		name := extractName(head[7:], "{")
		body := collectBracedBody(s, head, lineno)
		return Decl{
			Kind:      DeclStruct,
			Name:      name,
			Signature: "struct " + name + " { ... }",
			Body:      body,
			SrcFile:   srcfile,
			SrcLine:   startLine,
		}, true
	}

	// type Name <underlying>
	if strings.HasPrefix(head, "type ") {
		// Parse "type Name Underlying" on one line.
		parts := strings.Fields(head)
		if len(parts) >= 3 {
			return Decl{
				Kind:      DeclTypeAlias,
				Name:      parts[1],
				Signature: head,
				SrcFile:   srcfile,
				SrcLine:   startLine,
			}, true
		}
	}

	// const NAME T = expr  (top-level)  — not currently supported by the
	// compiler at top level, but cheap to be ready for it.
	if strings.HasPrefix(head, "const ") || strings.HasPrefix(head, "var ") {
		// Strip the keyword.
		kw := "const "
		kind := DeclVar
		if strings.HasPrefix(head, "var ") {
			kw = "var "
		}
		rest := strings.TrimSpace(strings.TrimPrefix(head, kw))
		name := extractName(rest, " ")
		return Decl{
			Kind:      kind,
			Name:      name,
			Signature: head,
			SrcFile:   srcfile,
			SrcLine:   startLine,
		}, true
	}

	return Decl{}, false
}

// tryBsDecl recognizes top-level .bs declarations.
func tryBsDecl(s *bufio.Scanner, head string, lineno *int, srcfile string) (Decl, bool) {
	startLine := *lineno

	// function name
	if strings.HasPrefix(head, "function ") {
		name := strings.TrimSpace(strings.TrimPrefix(head, "function"))
		sig := "function " + name
		// Look ahead one line for a `type fn(...) ret` annotation.
		annot := peekTypeAnnotation(s, lineno)
		if annot != "" {
			sig = annot + "   // " + name
		}
		return Decl{
			Kind:      DeclAsmFunc,
			Name:      name,
			Signature: sig,
			SrcFile:   srcfile,
			SrcLine:   startLine,
		}, true
	}

	// data <name> ... / var <name> ...
	if strings.HasPrefix(head, "data ") {
		parts := strings.Fields(head)
		if len(parts) >= 2 {
			return Decl{
				Kind:      DeclAsmData,
				Name:      parts[1],
				Signature: head,
				SrcFile:   srcfile,
				SrcLine:   startLine,
			}, true
		}
	}
	if strings.HasPrefix(head, "var ") {
		parts := strings.Fields(head)
		if len(parts) >= 2 {
			return Decl{
				Kind:      DeclAsmVar,
				Name:      parts[1],
				Signature: head,
				SrcFile:   srcfile,
				SrcLine:   startLine,
			}, true
		}
	}

	return Decl{}, false
}

// extractName returns the identifier at the start of s, up to (but excluding)
// any of the bytes in stopChars. Whitespace is treated as a stop character.
func extractName(s, stopChars string) string {
	s = strings.TrimSpace(s)
	for i, c := range s {
		if c == ' ' || c == '\t' || strings.ContainsRune(stopChars, c) {
			return s[:i]
		}
	}
	return s
}

// skipBracedBody consumes lines from s until the brace depth (starting at
// whatever is in head) returns to zero. Increments *lineno per consumed line.
// Assumes the open brace is on or after head.
func skipBracedBody(s *bufio.Scanner, head string, lineno *int) {
	depth := countBraces(head)
	if depth == 0 && !strings.Contains(head, "{") {
		// Header line has no '{' yet; the body is on a following line.
		for s.Scan() {
			*lineno++
			l := s.Text()
			depth += countBraces(l)
			if strings.Contains(l, "{") && depth == 0 {
				// open and close on the same line; we're done
				return
			}
			if depth > 0 {
				break
			}
		}
	}
	for depth > 0 && s.Scan() {
		*lineno++
		depth += countBraces(s.Text())
	}
}

// collectBracedBody consumes the braced body (including the header line) and
// returns it joined as a single string. Used to capture struct definitions.
func collectBracedBody(s *bufio.Scanner, head string, lineno *int) string {
	var b strings.Builder
	b.WriteString(head)
	b.WriteByte('\n')
	depth := countBraces(head)
	if depth == 0 && !strings.Contains(head, "{") {
		// brace on a later line
		for s.Scan() {
			*lineno++
			l := s.Text()
			b.WriteString(l)
			b.WriteByte('\n')
			depth += countBraces(l)
			if depth > 0 {
				break
			}
		}
	}
	for depth > 0 && s.Scan() {
		*lineno++
		l := s.Text()
		b.WriteString(l)
		b.WriteByte('\n')
		depth += countBraces(l)
	}
	return b.String()
}

// countBraces returns the net change in brace depth over the line: +1 for each
// '{', -1 for each '}'. Naive — does not track string literals or comments,
// which is acceptable for the doc scanner.
func countBraces(line string) int {
	// Strip line comments before counting.
	if i := strings.Index(line, "//"); i >= 0 {
		line = line[:i]
	}
	d := 0
	for _, c := range line {
		switch c {
		case '{':
			d++
		case '}':
			d--
		}
	}
	return d
}

// peekTypeAnnotation looks at the next non-blank, non-comment line; if it is
// a `type fn(...) ret` annotation, consumes it and returns its content.
// Otherwise returns "" and consumes nothing useful.
func peekTypeAnnotation(s *bufio.Scanner, lineno *int) string {
	if !s.Scan() {
		return ""
	}
	*lineno++
	line := strings.TrimSpace(s.Text())
	if strings.HasPrefix(line, "type ") {
		return line
	}
	return ""
}
