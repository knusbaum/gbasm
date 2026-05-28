package main

import (
	"bufio"
	"os"
	"strings"
)

// Decl is a single top-level declaration extracted from a source file.
type Decl struct {
	Kind      DeclKind // function, type, interface, var, ...
	Name      string   // declared name
	Signature string   // header line for funcs/types/interfaces/vars
	Body      string   // for struct types: the `struct { ... }` block (verbatim)
	Doc       string   // joined comment block immediately preceding the decl
	SrcFile   string   // file in which this decl appears (relative to package dir)
	SrcLine   int      // 1-based line number of the declaration's first line
	Methods   []Decl   // for DeclType: method definitions; for DeclInterface: method signatures
}

type DeclKind int

const (
	DeclFunc DeclKind = iota
	DeclType      // `type Name X [{ methods }]` and `type Name struct { ... } [{ methods }]`
	DeclInterface // `interface Name { sigs }`
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
	case DeclType:
		return "type"
	case DeclInterface:
		return "interface"
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

		switch lineKind(line) {
		case lineBlank:
			pending = nil
			continue
		case lineComment:
			pending = append(pending, commentText(line))
			continue
		}

		// `package <name>` — the comment block above it (if any) is the
		// package's doc comment.
		if strings.HasPrefix(line, "package") && !sawDecl {
			ps.PkgName = strings.TrimSpace(strings.TrimPrefix(line, "package"))
			if ps.DocComment == "" {
				ps.DocComment = flushPending()
			} else {
				flushPending()
			}
			sawDecl = true
			continue
		}

		// `import ...` — ignored for doc purposes
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

// lineKind / commentText centralise the comment vs blank vs code classification
// so the file-level loop and the inside-method-block loop agree on what
// counts as a doc comment.
type lineClass int

const (
	lineCode lineClass = iota
	lineBlank
	lineComment
)

func lineKind(line string) lineClass {
	if line == "" {
		return lineBlank
	}
	if strings.HasPrefix(line, "//") {
		return lineComment
	}
	return lineCode
}

func commentText(line string) string {
	return strings.TrimSpace(strings.TrimPrefix(line, "//"))
}

// tryBosDecl recognizes top-level .bos declarations.
func tryBosDecl(s *bufio.Scanner, head string, lineno *int, srcfile string) (Decl, bool) {
	startLine := *lineno

	// fn name(...) ret { ... }
	if strings.HasPrefix(head, "fn ") {
		name := extractName(head[3:], "(")
		sig := head
		if i := strings.Index(sig, "{"); i >= 0 {
			sig = strings.TrimSpace(sig[:i])
		}
		skipBracedBody(s, head, lineno)
		return Decl{
			Kind:      DeclFunc,
			Name:      name,
			Signature: sig,
			SrcFile:   srcfile,
			SrcLine:   startLine,
		}, true
	}

	// type Name <underlying> [{ methods }]
	// type Name struct { ... } [{ methods }]
	if strings.HasPrefix(head, "type ") {
		return parseBosType(s, head, lineno, srcfile, startLine)
	}

	// interface Name { sigs }
	if strings.HasPrefix(head, "interface ") || head == "interface" {
		return parseBosInterface(s, head, lineno, srcfile, startLine)
	}

	// const NAME T = expr / var NAME T = expr  (file scope)
	if strings.HasPrefix(head, "const ") || strings.HasPrefix(head, "var ") {
		kw := "const "
		if strings.HasPrefix(head, "var ") {
			kw = "var "
		}
		rest := strings.TrimSpace(strings.TrimPrefix(head, kw))
		name := extractName(rest, " ")
		return Decl{
			Kind:      DeclVar,
			Name:      name,
			Signature: head,
			SrcFile:   srcfile,
			SrcLine:   startLine,
		}, true
	}

	return Decl{}, false
}

// parseBosType handles `type Name ...` decls. The underlying type is either
// a single token (e.g. `i64`) or a `struct { ... }` block. Either form may
// be followed by a method block `{ methods }`.
//
// Edge case to handle:
//
//	type other_thingy struct{ x i64, y i64} {
//
// Two `{`s on one line; brace-depth tracking finds the boundary between the
// struct body and the method block.
func parseBosType(s *bufio.Scanner, head string, lineno *int, srcfile string, startLine int) (Decl, bool) {
	rest := strings.TrimSpace(strings.TrimPrefix(head, "type"))
	if rest == "" {
		return Decl{}, false
	}
	name := extractName(rest, " \t{")
	if name == "" {
		return Decl{}, false
	}
	afterName := strings.TrimSpace(rest[len(name):])

	d := Decl{
		Kind:    DeclType,
		Name:    name,
		SrcFile: srcfile,
		SrcLine: startLine,
	}

	// Detect a struct underlying. Either:
	//   "struct {"  — body opens on this line; may continue on next lines
	//   "struct {...}"  — body opens and closes on this line
	//   "struct {...} {"  — body + method block opener on this line
	if afterName == "struct" || strings.HasPrefix(afterName, "struct{") || strings.HasPrefix(afterName, "struct ") {
		body, methodOpen := consumeStructBody(s, afterName, lineno)
		d.Body = body
		d.Signature = "type " + name + " " + collapseBody(body)
		if methodOpen {
			d.Methods = parseBosMethodList(s, lineno)
		}
		return d, true
	}

	// Non-struct underlying. The underlying text runs up to the first '{' or
	// end of line. Anything after that '{' is the method block opener.
	var underlying string
	methodOpen := false
	if i := strings.IndexByte(afterName, '{'); i >= 0 {
		underlying = strings.TrimSpace(afterName[:i])
		// Anything past the '{' on the same line is unexpected for plain types;
		// treat it as opening the method block. The braces will balance via
		// parseBosMethodList's depth tracking.
		methodOpen = true
	} else {
		underlying = afterName
	}
	d.Signature = "type " + name
	if underlying != "" {
		d.Signature += " " + underlying
	}
	if methodOpen {
		d.Methods = parseBosMethodList(s, lineno)
	}
	return d, true
}

// consumeStructBody captures the `struct { ... }` body starting from text that
// begins with `struct`. Returns the body verbatim (`struct { ... }`) and a
// bool indicating whether a method-block opener `{` follows on the same line
// as the struct body's closing `}`.
//
// `headRest` is the head line with the `type Name ` prefix already stripped.
// If the body spans multiple lines, the scanner is advanced to the line that
// contains the closing `}`.
func consumeStructBody(s *bufio.Scanner, headRest string, lineno *int) (body string, methodBlockOpens bool) {
	// Append lines until the brace depth of the struct's `{...}` returns to 0.
	// We need character-level depth tracking on the FIRST line so we can find
	// where the struct's closing `}` is, in case a method-block `{` follows
	// on the same line.

	// Walk headRest one char at a time, tracking depth. Once we see the first
	// '{' (which opens the struct body) and a later '}' returns to depth 0,
	// we've found the boundary on this line.
	if open := strings.IndexByte(headRest, '{'); open >= 0 {
		depth := 0
		for i := open; i < len(headRest); i++ {
			switch headRest[i] {
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					// Struct body ends here. Check whether anything past
					// position i (on this same line) is a method-block opener.
					body = strings.TrimSpace(headRest[:i+1])
					trailing := strings.TrimSpace(headRest[i+1:])
					if strings.Contains(trailing, "{") {
						methodBlockOpens = true
					}
					return
				}
			}
		}
		// Struct body did not close on this line — read more lines.
		var b strings.Builder
		b.WriteString(headRest)
		for s.Scan() {
			*lineno++
			l := s.Text()
			b.WriteByte('\n')
			b.WriteString(l)
			for i := 0; i < len(l); i++ {
				switch l[i] {
				case '{':
					depth++
				case '}':
					depth--
					if depth == 0 {
						// Struct body closes on this line at position i.
						trailing := strings.TrimSpace(l[i+1:])
						body = strings.TrimSpace(b.String())
						if strings.Contains(trailing, "{") {
							methodBlockOpens = true
						}
						return
					}
				}
			}
		}
		// EOF mid-body — return what we have.
		body = strings.TrimSpace(b.String())
		return
	}
	// headRest is just "struct" (no `{` yet) — body opens on next line(s).
	body = "struct"
	depth := 0
	for s.Scan() {
		*lineno++
		l := s.Text()
		body += "\n" + l
		for i := 0; i < len(l); i++ {
			switch l[i] {
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					trailing := strings.TrimSpace(l[i+1:])
					if strings.Contains(trailing, "{") {
						methodBlockOpens = true
					}
					return strings.TrimSpace(body), methodBlockOpens
				}
			}
		}
	}
	return strings.TrimSpace(body), false
}

// collapseBody renders a multi-line struct body as a single-line summary for
// the signature header. Multi-line bodies are summarised as `struct { ... }`.
func collapseBody(body string) string {
	if strings.Contains(body, "\n") {
		return "struct { ... }"
	}
	return body
}

// parseBosMethodList reads method definitions inside a `type Name X { ... }`
// block. Called immediately after the opening `{` has been consumed (the
// outer brace block is at depth 1). Stops when the closing `}` returns to
// depth 0.
//
// Each method has the form `name(params) [rettype] { body }`. The body is
// skipped via brace tracking; only the header line becomes the method's
// signature.
func parseBosMethodList(s *bufio.Scanner, lineno *int) []Decl {
	var methods []Decl
	var pending []string
	for s.Scan() {
		*lineno++
		raw := s.Text()
		line := strings.TrimSpace(raw)

		switch lineKind(line) {
		case lineBlank:
			pending = nil
			continue
		case lineComment:
			pending = append(pending, commentText(line))
			continue
		}

		// A line that is just `}` (or starts with `}`) at outer depth 0
		// closes the method block.
		if strings.HasPrefix(line, "}") {
			return methods
		}

		// Otherwise, this line opens a method. Header up to the first '{' is
		// the signature; the rest of the body is skipped.
		startLine := *lineno
		sig := line
		if i := strings.IndexByte(sig, '{'); i >= 0 {
			sig = strings.TrimSpace(sig[:i])
		}
		name := extractName(line, "(")

		// Skip method body — depth starts at 1 (the outer { of the method
		// block) but for THIS method we just need its own braces to balance.
		skipBracedBody(s, raw, lineno)

		methods = append(methods, Decl{
			Kind:      DeclFunc,
			Name:      name,
			Signature: sig,
			Doc:       strings.Join(pending, "\n"),
			SrcFile:   "",
			SrcLine:   startLine,
		})
		pending = nil
	}
	return methods
}

// parseBosInterface handles `interface Name { sig sig ... }`. Each sig is a
// single line `name(params) [rettype]` with no body.
func parseBosInterface(s *bufio.Scanner, head string, lineno *int, srcfile string, startLine int) (Decl, bool) {
	rest := strings.TrimSpace(strings.TrimPrefix(head, "interface"))
	name := extractName(rest, " \t{")
	if name == "" {
		return Decl{}, false
	}

	d := Decl{
		Kind:      DeclInterface,
		Name:      name,
		Signature: "interface " + name,
		SrcFile:   srcfile,
		SrcLine:   startLine,
	}

	// If the head line doesn't contain `{`, look ahead.
	if !strings.Contains(head, "{") {
		// Skip blank / comment lines until the opening `{`.
		for s.Scan() {
			*lineno++
			line := strings.TrimSpace(s.Text())
			if line == "" || strings.HasPrefix(line, "//") {
				continue
			}
			if strings.Contains(line, "{") {
				break
			}
			break // unexpected — bail
		}
	}

	// Read sigs until the closing `}`.
	var pending []string
	for s.Scan() {
		*lineno++
		raw := s.Text()
		line := strings.TrimSpace(raw)
		switch lineKind(line) {
		case lineBlank:
			pending = nil
			continue
		case lineComment:
			pending = append(pending, commentText(line))
			continue
		}
		if strings.HasPrefix(line, "}") {
			return d, true
		}
		methodName := extractName(line, "(")
		d.Methods = append(d.Methods, Decl{
			Kind:      DeclFunc,
			Name:      methodName,
			Signature: line,
			Doc:       strings.Join(pending, "\n"),
			SrcLine:   *lineno,
		})
		pending = nil
	}
	return d, true
}

// tryBsDecl recognizes top-level .bs declarations.
func tryBsDecl(s *bufio.Scanner, head string, lineno *int, srcfile string) (Decl, bool) {
	startLine := *lineno

	// function name
	if strings.HasPrefix(head, "function ") {
		name := strings.TrimSpace(strings.TrimPrefix(head, "function"))
		sig := "function " + name
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

	// data <name> ...
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

	// var <name> ...
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
		for s.Scan() {
			*lineno++
			l := s.Text()
			depth += countBraces(l)
			if strings.Contains(l, "{") && depth == 0 {
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

// countBraces returns the net change in brace depth over the line: +1 for each
// '{', -1 for each '}'. Naive — does not track string literals or comments,
// which is acceptable for the doc scanner.
func countBraces(line string) int {
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
