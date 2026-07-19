package main

import (
	"bytes"
	"fmt"
	"io"
	"strings"
)

// emitc.go — the Boson→C transpiler backend (phase 1).
//
// The goal is idiomatic, human-readable C: a Boson module should transpile to
// C that looks hand-written, so it can be dropped into a real C project with
// nobody the wiser. Boson's value is the *authoring-time* layer — ownership,
// mutability, bounds/nil checking, and (later) vtable/header generation — not
// exotic runtime semantics smuggled into the output. So the emitter uses real
// C type names, natural identifiers, and natural operators, and keeps the
// structure of the source. See docs/proposed/PROPOSAL_c_transpiler.md.
//
// Phase 1 scope: a single `main` package; integer scalars; function
// definitions and calls; `return`; integer arithmetic; string literals; and
// output via the runtime print primitives, which map to libc (fputs/printf/
// putchar) — what a C author would write. Anything outside phase 1 raises a
// clear "not yet supported" error, never silent wrong output.
//
// NOT yet wired (follow-on increments): the validation oracle, general
// cross-package calls, aggregates, control flow, interfaces/ADTs.

// cUnit is per-translation-unit emit state: the compile Context and which
// standard headers the emitted code turned out to need (so the include block
// is exactly what a human would write — no unused includes).
type cUnit struct {
	c           *Context
	needStdio   bool
	needStdbool bool
}

// cEmitter is per-function state: the parameters/locals in scope, plus the
// enclosing unit. Locals are tracked here rather than in the Context (whose
// function-scope bindings are not established on the C path).
type cEmitter struct {
	u      *cUnit
	locals map[string]ASTType
}

// emitCFile writes a complete C translation unit for one source file. The body
// is emitted first (into a buffer) so the include block can list exactly the
// headers that were actually used. Errors carry a source position and are
// reported via fatalCtx, matching the .bs path.
func emitCFile(of io.Writer, c *Context, pkgname string, asts []AST) {
	u := &cUnit{c: c}
	var body bytes.Buffer
	for _, a := range asts {
		if err := emitCDecl(&body, u, a); err != nil {
			fatalCtx("Fatal: %v", err)
		}
	}
	fmt.Fprintf(of, "#include <stdint.h>\n")
	if u.needStdbool {
		fmt.Fprintf(of, "#include <stdbool.h>\n")
	}
	if u.needStdio {
		fmt.Fprintf(of, "#include <stdio.h>\n")
	}
	fmt.Fprintf(of, "\n")
	io.Copy(of, &body)
}

// emitCDecl lowers one top-level declaration to C. interpreterError panics
// (which carry a source position) are converted to a returned error so the
// driver can print source context.
func emitCDecl(w io.Writer, u *cUnit, a AST) (err error) {
	defer func() {
		if r := recover(); r != nil {
			if ie, ok := r.(*interpreterError); ok {
				err = ie
				return
			}
			panic(r)
		}
	}()
	switch n := a.(type) {
	case *FuncDecl:
		emitCFunc(w, u, n)
	default:
		cUnsupported(posOf(a), fmt.Sprintf("top-level declaration %T", a))
	}
	return nil
}

func emitCFunc(w io.Writer, u *cUnit, f *FuncDecl) {
	if f.Variadic {
		cUnsupported(f.p, "variadic functions")
	}
	// `main` in package main becomes the C entry point directly — no wrapper,
	// no mangling. Its return value (if any) is the process exit code.
	isEntry := u.c.Pkgname() == "main" && f.Name == "main"
	if isEntry && len(f.Args) != 0 {
		cUnsupported(f.p, "main with parameters (argv)")
	}

	em := &cEmitter{u: u, locals: make(map[string]ASTType, len(f.Args))}
	params := make([]string, 0, len(f.Args))
	for _, arg := range f.Args {
		em.locals[arg.Name] = arg.Type
		params = append(params, em.ctype(arg.Type)+" "+cIdent(arg.Name))
	}
	sig := "void"
	if len(params) > 0 {
		sig = strings.Join(params, ", ")
	}

	switch {
	case isEntry:
		fmt.Fprintf(w, "int main(%s) {\n", sig)
	case f.IsPub:
		// Exported: visible across translation units. (Package-prefixed
		// naming for cross-package linkage arrives with cross-package calls.)
		fmt.Fprintf(w, "%s %s(%s) {\n", em.ctype(f.Return), cIdent(f.Name), sig)
	default:
		// File-local: `static`, exactly as a hand-written C module marks its
		// internal helpers.
		fmt.Fprintf(w, "static %s %s(%s) {\n", em.ctype(f.Return), cIdent(f.Name), sig)
	}
	if f.Body != nil {
		for _, stmt := range f.Body.Body {
			em.stmt(w, stmt)
		}
	}
	if isEntry && isVoidType(u.c, f.Return) {
		fmt.Fprintf(w, "\treturn 0;\n")
	}
	fmt.Fprintf(w, "}\n\n")
}

func (em *cEmitter) stmt(w io.Writer, a AST) {
	switch n := a.(type) {
	case *Return:
		if n.Val == nil {
			fmt.Fprintf(w, "\treturn;\n")
		} else {
			fmt.Fprintf(w, "\treturn %s;\n", em.expr(n.Val))
		}
	case *Funcall:
		fmt.Fprintf(w, "\t%s;\n", em.call(n))
	default:
		cUnsupported(posOf(a), fmt.Sprintf("statement %T", a))
	}
}

func (em *cEmitter) expr(a AST) string {
	switch n := a.(type) {
	case *Literal:
		return em.literal(n)
	case *Symbol:
		return cIdent(n.Name)
	case *Funcall:
		return em.call(n)
	case *Op2:
		return em.op2(n)
	default:
		cUnsupported(posOf(a), fmt.Sprintf("expression %T", a))
		return ""
	}
}

func (em *cEmitter) call(f *Funcall) string {
	if pkg := f.PkgName(); pkg != "" {
		// Qualified call. Phase 1 maps the runtime output primitives to libc
		// (what a C author writes); general cross-package calls come later.
		return em.runtimeCall(f, pkg, f.FName())
	}
	args := make([]string, 0, len(f.Args))
	for _, arg := range f.Args {
		args = append(args, em.expr(arg))
	}
	return cIdent(f.FName()) + "(" + strings.Join(args, ", ") + ")"
}

// runtimeCall maps a recognized runtime output primitive to its idiomatic libc
// equivalent. The print helpers write raw bytes / a value with no added
// newline, matching the Boson semantics.
func (em *cEmitter) runtimeCall(f *Funcall, pkg, name string) string {
	switch pkg + "." + name {
	case "string.puts":
		// puts([]byte). Phase 1 handles a string-literal argument (the common
		// case) via fputs; general byte-slice output waits on slices.
		if len(f.Args) == 1 {
			if lit, ok := f.Args[0].(*Literal); ok {
				if s, ok := lit.Val.(string); ok {
					em.u.needStdio = true
					return "fputs(" + cStringLit(s) + ", stdout)"
				}
			}
		}
		cUnsupported(f.p, "string.puts of a non-literal byte slice")
	case "string.puti":
		if len(f.Args) == 1 {
			em.u.needStdio = true
			return "printf(\"%lld\", (long long)(" + em.expr(f.Args[0]) + "))"
		}
	case "string.putc":
		if len(f.Args) == 1 {
			em.u.needStdio = true
			return "putchar(" + em.expr(f.Args[0]) + ")"
		}
	}
	cUnsupported(f.p, fmt.Sprintf("call to %s.%s", pkg, name))
	return ""
}

// op2 emits a binary operation with natural C operators and precedence-aware
// parentheses (only where the tree structure requires them), so the output
// reads like hand-written C rather than fully-parenthesized machine output.
func (em *cEmitter) op2(o *Op2) string {
	prec := cPrec(o.Type)
	return em.operand(o.First, prec, false) + " " + cBinSym(o.Type) + " " + em.operand(o.Second, prec, true)
}

func (em *cEmitter) operand(a AST, parentPrec int, isRight bool) string {
	s := em.expr(a)
	if child, ok := a.(*Op2); ok {
		cp := cPrec(child.Type)
		// Left operand needs parens only when it binds looser; the right
		// operand also needs them at equal precedence (left-associativity).
		if cp < parentPrec || (isRight && cp == parentPrec) {
			return "(" + s + ")"
		}
	}
	return s
}

func (em *cEmitter) literal(l *Literal) string {
	switch v := l.Val.(type) {
	case uint64:
		return fmt.Sprintf("%d", v)
	case string:
		return cStringLit(v)
	case bool:
		em.u.needStdbool = true
		if v {
			return "true"
		}
		return "false"
	default:
		cUnsupported(l.p, fmt.Sprintf("literal of type %T", l.Val))
		return ""
	}
}

// ---- type mapping --------------------------------------------------------

// ctype maps a Boson type to its idiomatic C spelling — the standard
// <stdint.h> names, exactly what a careful C programmer writes. Phase 1:
// integer scalars, bool, and void.
func (em *cEmitter) ctype(t ASTType) string {
	rt := em.u.c.ResolveUnderlying(t)
	if isScalarLeaf(rt) {
		switch rt.Name {
		case "void", "":
			return "void"
		case "bool":
			em.u.needStdbool = true
			return "bool"
		case "i8":
			return "int8_t"
		case "i16":
			return "int16_t"
		case "i32":
			return "int32_t"
		case "i64":
			return "int64_t"
		case "u8", "byte":
			return "uint8_t"
		case "u16":
			return "uint16_t"
		case "u32":
			return "uint32_t"
		case "u64":
			return "uint64_t"
		}
	}
	cUnsupported(t.p, fmt.Sprintf("type %q", t.String()))
	return ""
}

func isScalarLeaf(t ASTType) bool {
	return t.Indirection == 0 && t.Element == nil && t.FuncSig == nil && t.AnonFields == nil
}

func isVoidType(c *Context, t ASTType) bool {
	rt := c.ResolveUnderlying(t)
	return isScalarLeaf(rt) && (rt.Name == "void" || rt.Name == "")
}

// ---- operators -----------------------------------------------------------

func cBinSym(t nodetype) string {
	switch t {
	case n_add:
		return "+"
	case n_sub:
		return "-"
	case n_mul:
		return "*"
	case n_div:
		return "/"
	case n_mod:
		return "%"
	case n_bitand:
		return "&"
	case n_bitor:
		return "|"
	case n_xor:
		return "^"
	case n_shl:
		return "<<"
	case n_shr:
		return ">>"
	case n_lt:
		return "<"
	case n_le:
		return "<="
	case n_gt:
		return ">"
	case n_ge:
		return ">="
	case n_deq:
		return "=="
	case n_neq:
		return "!="
	}
	return "?"
}

// cPrec is the C operator precedence (higher binds tighter), used only to
// decide when a sub-expression needs parentheses.
func cPrec(t nodetype) int {
	switch t {
	case n_mul, n_div, n_mod:
		return 10
	case n_add, n_sub:
		return 9
	case n_shl, n_shr:
		return 8
	case n_lt, n_le, n_gt, n_ge:
		return 7
	case n_deq, n_neq:
		return 6
	case n_bitand:
		return 5
	case n_xor:
		return 4
	case n_bitor:
		return 3
	}
	return 0
}

// ---- identifiers & literals ----------------------------------------------

// cIdent renders a Boson identifier as a C identifier. Boson names are already
// valid C identifier syntax; the only adjustment is suffixing `_` when a name
// collides with a C keyword, matching what a human would do.
func cIdent(name string) string {
	if cKeywords[name] {
		return name + "_"
	}
	return name
}

// cStringLit renders a Go string (a decoded Boson string literal) as a C
// string literal, re-escaping the characters C needs escaped. Non-printable
// bytes use 3-digit octal (\NNN), which C reads unambiguously.
func cStringLit(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		switch ch := s[i]; ch {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\t':
			b.WriteString(`\t`)
		case '\r':
			b.WriteString(`\r`)
		default:
			if ch < 0x20 || ch >= 0x7f {
				fmt.Fprintf(&b, `\%03o`, ch)
			} else {
				b.WriteByte(ch)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

var cKeywords = map[string]bool{
	"auto": true, "break": true, "case": true, "char": true, "const": true,
	"continue": true, "default": true, "do": true, "double": true, "else": true,
	"enum": true, "extern": true, "float": true, "for": true, "goto": true,
	"if": true, "inline": true, "int": true, "long": true, "register": true,
	"restrict": true, "return": true, "short": true, "signed": true,
	"sizeof": true, "static": true, "struct": true, "switch": true,
	"typedef": true, "union": true, "unsigned": true, "void": true,
	"volatile": true, "while": true, "bool": true, "_Bool": true,
}

// ---- helpers -------------------------------------------------------------

func cUnsupported(p position, what string) {
	panic(&interpreterError{msg: fmt.Sprintf("c backend (phase 1): %s not yet supported", what), p: p})
}

func posOf(a AST) position {
	if p, ok := a.(interface{ Pos() position }); ok {
		return p.Pos()
	}
	return position{}
}
