package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/knusbaum/gbasm"
)

// sourceScope controls source spelling only. Imported declarations are stored
// once under canonical package names, so signatures retain their identity when
// used from a sibling file that has no import for that package.
type sourceScope struct {
	imports    map[string]string // file-local name -> canonical package name
	localTypes map[string]bool
}

func (c *Context) sourceScope() *sourceScope {
	for ctx := c; ctx != nil; ctx = ctx.parent {
		if ctx.fileScope != nil {
			return ctx.fileScope
		}
	}
	return nil
}

func (c *Context) useSourceFile(name string) {
	root := c
	for root.parent != nil {
		root = root.parent
	}
	if scope := root.fileScopes[name]; scope != nil {
		c.fileScope = scope
	}
}

func (c *Context) importedPackageName(name string) (string, bool) {
	if scope := c.sourceScope(); scope != nil {
		canonical, ok := scope.imports[name]
		return canonical, ok
	}
	// Contexts made directly by front-end tests retain the legacy namespace.
	for ctx := c; ctx != nil; ctx = ctx.parent {
		if _, ok := ctx.imports[name]; ok {
			return name, true
		}
	}
	return "", false
}

func (s *sourceScope) typeName(name string) (string, error) {
	prefix, rest, qualified := strings.Cut(name, ".")
	if !qualified {
		return name, nil
	}
	if canonical, ok := s.imports[prefix]; ok {
		return canonical + "." + rest, nil
	}
	if s.localTypes[prefix] {
		return name, nil
	} // local values cases
	return "", fmt.Errorf("package %q is not imported in this file", prefix)
}

// sourceTypeByName is for an explicit source cast. Internal type lookups use
// TypeByName directly, since canonical types can travel across file scopes.
func (c *Context) sourceTypeByName(name string) (ASTType, bool) {
	// Prefix slice casts were already qualified and checked in qualifyNode.
	if strings.HasPrefix(name, "[]") {
		return c.TypeByName(name)
	}
	if scope := c.sourceScope(); scope != nil {
		var err error
		name, err = scope.typeName(name)
		if err != nil {
			return ASTType{}, false
		}
	}
	return c.TypeByName(name)
}

func (s *sourceScope) qualifyType(t *ASTType) error {
	var err error
	if t.Name != "" {
		t.Name, err = s.typeName(t.Name)
		if err != nil {
			return err
		}
	}
	if t.Element != nil {
		if err := s.qualifyType(t.Element); err != nil {
			return err
		}
	}
	for i := range t.AnonFields {
		if err := s.qualifyType(&t.AnonFields[i].Type); err != nil {
			return err
		}
	}
	if t.FuncSig != nil {
		for i := range t.FuncSig.Args {
			if err := s.qualifyType(&t.FuncSig.Args[i]); err != nil {
				return err
			}
		}
		return s.qualifyType(&t.FuncSig.Return)
	}
	return nil
}

// Qualify type syntax before ToAST, whose recursive conversions deliberately
// use throwaway contexts. Expression package names resolve in the file scope
// at compile time, preserving ordinary local-variable shadowing.
func (s *sourceScope) qualifyNode(n *Node) error {
	if n == nil {
		return nil
	}
	fail := func(err error) error { return &interpreterError{msg: err.Error(), p: n.p} }
	if n.t == n_typename {
		t := mkTypename(n)
		if err := s.qualifyType(&t); err != nil {
			return fail(err)
		}
		n.sval, n.prebuilt = "<prebuilt>", &t
		return nil
	}
	if n.t == n_stlit && strings.Contains(n.sval, ".") {
		name, err := s.typeName(n.sval)
		if err != nil {
			return fail(err)
		}
		n.sval, n.typeIdent = name, nil
	}
	// Slice projection casts have a complete type spelling in sval.
	if n.t == n_funcall && strings.HasPrefix(n.sval, "[]") {
		t, err := parseTypeString(n.sval)
		if err != nil {
			return fail(err)
		}
		if err := s.qualifyType(&t); err != nil {
			return fail(err)
		}
		n.sval = t.String()
	}
	for _, arg := range n.args {
		if err := s.qualifyNode(arg); err != nil {
			return err
		}
	}
	return nil
}

type packageSource struct {
	name   string
	reader io.Reader
}

type parsedSource struct {
	name, pkg string
	nodes     []*Node
	scope     *sourceScope
	asts      []AST
}

func parsePackageSource(src packageSource) (*parsedSource, error) {
	reader := bufio.NewReader(src.reader)
	var lines uint
	var pkg string
	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			lines++
		}
		text := strings.TrimSpace(line)
		if text != "" && !strings.HasPrefix(text, "//") {
			words := strings.Fields(text)
			if len(words) != 2 || words[0] != "package" {
				return nil, fmt.Errorf("%s:%d: expected package declaration", src.name, lines)
			}
			pkg = words[1]
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%s: missing package declaration", src.name)
		}
	}
	file := &parsedSource{name: src.name, pkg: pkg}
	parser := NewParserAt(src.name, reader, lines+1)
	for {
		n, err := parser.Next()
		if err != nil {
			return nil, err
		}
		if n == nil {
			break
		}
		file.nodes = append(file.nodes, n)
	}
	return file, nil
}

// compilePackage registers every file's declarations before compiling any
// body, emitting one package and one set of shared metadata for the invocation.
func compilePackage(out io.Writer, sources []packageSource, imports map[string]string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			if e, ok := r.(*interpreterError); ok {
				err = e
			} else {
				panic(r)
			}
		}
	}()
	if len(sources) == 0 {
		return fmt.Errorf("expected at least one source file")
	}
	var files []*parsedSource
	localTypes := make(map[string]bool)
	for _, src := range sources {
		file, err := parsePackageSource(src)
		if err != nil {
			return err
		}
		if len(files) > 0 && file.pkg != files[0].pkg {
			return fmt.Errorf("%s: package %q differs from package %q in %s", file.name, file.pkg, files[0].pkg, files[0].name)
		}
		files = append(files, file)
		for _, n := range file.nodes {
			switch n.t {
			case n_typedecl, n_typedecl_with_methods, n_interface_decl, n_valuesdecl:
				localTypes[n.sval] = true
			}
		}
	}
	ctx := NewContext()
	ctx.SetPkgname(files[0].pkg)
	ctx.fileScopes = make(map[string]*sourceScope)
	loaded := make(map[string]string)
	load := func(key string) (string, error) {
		path, ok := imports[key]
		if !ok {
			return "", fmt.Errorf("import %q: not found in importcfg", key)
		}
		if pkg, ok := loaded[path]; ok {
			return pkg, nil
		}
		obj, err := gbasm.ReadOFile(path)
		if err != nil {
			return "", err
		}
		if err := ctx.Import(key, path); err != nil {
			return "", err
		}
		loaded[path] = obj.Pkgname
		return obj.Pkgname, nil
	}
	var builtin string
	if ctx.Pkgname() != "builtin" {
		if _, ok := imports["builtin"]; ok {
			builtin, err = load("builtin")
			if err != nil {
				return err
			}
		}
	}
	for _, file := range files {
		scope := &sourceScope{imports: make(map[string]string), localTypes: localTypes}
		if builtin != "" {
			scope.imports["builtin"] = builtin
		}
		for _, n := range file.nodes {
			if n.t != n_import {
				continue
			}
			canonical, err := load(n.sval)
			if err != nil {
				return &interpreterError{msg: err.Error(), p: n.p}
			}
			alias := canonical
			if len(n.args) > 0 {
				alias = n.args[0].sval
			}
			if _, exists := scope.imports[alias]; exists {
				return &interpreterError{msg: fmt.Sprintf("import name %q already defined in this file", alias), p: n.p}
			}
			scope.imports[alias] = canonical
		}
		file.scope = scope
		ctx.fileScopes[file.name] = scope
	}
	for _, file := range files {
		ctx.fileScope = file.scope
		for _, n := range file.nodes {
			if n.t == n_import {
				continue
			}
			if err := file.scope.qualifyNode(n); err != nil {
				return err
			}
			a, err := n.ToAST(ctx)
			if err != nil {
				return err
			}
			file.asts = append(file.asts, a)
		}
	}
	var body bytes.Buffer
	for _, file := range files {
		ctx.fileScope = file.scope
		for _, a := range file.asts {
			if err := Compile(&body, ctx, a); err != nil {
				return err
			}
		}
	}
	var result bytes.Buffer
	fmt.Fprintf(&result, "package %s\n\n", ctx.Pkgname())
	if ctx.Pkgname() == "builtin" {
		emitBuiltinScalarTypedescs(&result)
	}
	ctx.WriteVtables(&result)
	ctx.WriteStrings(&result)
	ctx.WriteStrSliceHeaders(&result)
	result.Write(body.Bytes())
	_, err = io.Copy(out, &result)
	return err
}

func compilePackageFiles(out io.Writer, names []string, imports map[string]string) error {
	var sources []packageSource
	for _, name := range names {
		data, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		sources = append(sources, packageSource{name: name, reader: bytes.NewReader(data)})
	}
	return compilePackage(out, sources, imports)
}
