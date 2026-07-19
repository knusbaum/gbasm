package main

import (
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
// structure of the source (functions, control flow, expressions). See
// docs/proposed/PROPOSAL_c_transpiler.md.
//
// Phase 1 scope: a single `main` package, integer scalars, function
// definitions and calls, `return`, and integer arithmetic. Anything outside
// phase 1 raises a clear "not yet supported" error — never silent wrong output.
//
// NOT yet wired (follow-on increments): the validation oracle, a cc-driven
// test harness, generated module headers, aggregates, control flow,
// interfaces/ADTs, and cross-package calls (which introduce the package-prefix
// naming scheme; phase 1 uses bare names within the single package).

// cEmitter carries per-function emit state — the parameters/locals in scope —
// alongside the compile Context, so expression emission can answer type
// questions without re-resolving names through the Context (whose
// function-scope bindings are not established on the C path).
type cEmitter struct {
	c      *Context
	locals map[string]ASTType
}

// emitCFile writes a complete C translation unit for one source file. Errors
// carry a source position and are reported via fatalCtx, matching the .bs
// path. (The validation oracle — running the .bs path to a discard writer as a
// semantic gate — is a follow-on increment.)
func emitCFile(of io.Writer, c *Context, pkgname string, asts []AST) {
	fmt.Fprintf(of, "#include <stdint.h>\n\n")
	for _, a := range asts {
		if err := emitC(of, c, a); err != nil {
			fatalCtx("Fatal: %v", err)
		}
	}
}

// emitC lowers one top-level declaration to C, mirroring Compile's per-decl
// contract. interpreterError panics (which carry a source position) are
// converted to a returned error so the driver can print source context.
func emitC(w io.Writer, c *Context, a AST) (err error) {
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
		emitCFunc(w, c, n)
	default:
		cUnsupported(posOf(a), fmt.Sprintf("top-level declaration %T", a))
	}
	return nil
}

func emitCFunc(w io.Writer, c *Context, f *FuncDecl) {
	if f.Variadic {
		cUnsupported(f.p, "variadic functions")
	}
	// `main` in package main becomes the C entry point directly — no wrapper,
	// no mangling. Its return value (if any) is the process exit code.
	isEntry := c.Pkgname() == "main" && f.Name == "main"
	if isEntry && len(f.Args) != 0 {
		cUnsupported(f.p, "main with parameters (argv)")
	}

	em := &cEmitter{c: c, locals: make(map[string]ASTType, len(f.Args))}
	params := make([]string, 0, len(f.Args))
	for _, arg := range f.Args {
		em.locals[arg.Name] = arg.Type
		params = append(params, cType(c, arg.Type)+" "+cIdent(arg.Name))
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
		fmt.Fprintf(w, "%s %s(%s) {\n", cType(c, f.Return), cIdent(f.Name), sig)
	default:
		// File-local: `static`, exactly as a hand-written C module marks its
		// internal helpers.
		fmt.Fprintf(w, "static %s %s(%s) {\n", cType(c, f.Return), cIdent(f.Name), sig)
	}
	if f.Body != nil {
		for _, stmt := range f.Body.Body {
			em.stmt(w, stmt)
		}
	}
	if isEntry && isVoidType(c, f.Return) {
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
		return cLiteral(n)
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
	sym, ok := f.Callee.(*Symbol)
	if !ok {
		cUnsupported(f.p, "qualified or method calls")
	}
	args := make([]string, 0, len(f.Args))
	for _, arg := range f.Args {
		args = append(args, em.expr(arg))
	}
	return cIdent(sym.Name) + "(" + strings.Join(args, ", ") + ")"
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

func cLiteral(l *Literal) string {
	switch v := l.Val.(type) {
	case uint64:
		return fmt.Sprintf("%d", v)
	case bool:
		if v {
			return "1"
		}
		return "0"
	default:
		cUnsupported(l.p, fmt.Sprintf("literal of type %T", l.Val))
		return ""
	}
}

// ---- type mapping --------------------------------------------------------

// cType maps a Boson type to its idiomatic C spelling — the standard
// <stdint.h> names, exactly what a careful C programmer writes. Phase 1:
// integer scalars, bool, and void.
func cType(c *Context, t ASTType) string {
	rt := c.ResolveUnderlying(t)
	if isScalarLeaf(rt) {
		switch rt.Name {
		case "void", "":
			return "void"
		case "bool":
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

// ---- identifiers ---------------------------------------------------------

// cIdent renders a Boson identifier as a C identifier. Boson names are already
// valid C identifier syntax; the only adjustment is suffixing `_` when a name
// collides with a C keyword, matching what a human would do.
func cIdent(name string) string {
	if cKeywords[name] {
		return name + "_"
	}
	return name
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
