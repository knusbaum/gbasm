package main

import (
	"fmt"
	"io"
	"strconv"
	"strings"
)

// emitc.go — the Boson→C transpiler backend (phase 1).
//
// A high-level, *structured* C emitter that is a sibling of compile.go: it
// walks the same typed AST but emits C (keeping functions, control flow, and
// expressions intact) rather than lowering to .bs, letting `cc` do register
// allocation and optimization. See docs/proposed/PROPOSAL_c_transpiler.md.
//
// Phase 1 scope: a single `main` package, integer scalars, function
// definitions and calls, `return`, and integer arithmetic. Signed +,-,* go
// through the wrapping helpers in runtime/c/boson.h so the emitted C is
// well-formed (no signed-overflow UB) while reproducing Boson's defined
// two's-complement wrap. Anything outside phase 1 raises a clear
// "not yet supported" error — never silent wrong output.
//
// NOT yet wired (follow-on increments): the validation oracle (run the .bs
// path to a discard writer as a semantic gate before emitting), aggregates,
// control flow, interfaces/ADTs, cross-package calls, and the boson.mmk rule.

// cEmitter carries per-function emit state — the parameters/locals in scope —
// alongside the compile Context. Expression emission answers type questions
// (e.g. operand signedness, to pick the wrap helper) from `locals` rather than
// re-resolving names through the Context, whose function-scope bindings are
// not established on the C path.
type cEmitter struct {
	c      *Context
	locals map[string]ASTType
}

// emitCFile writes a complete C translation unit for one source file: the
// boson.h include, each top-level declaration, and — for package main — the
// int-main entry point. Errors carry a source position and are reported via
// fatalCtx, matching the .bs path. (The validation oracle is a follow-on
// increment; for now emitC's own "not yet supported" errors are the guard.)
func emitCFile(of io.Writer, c *Context, pkgname string, asts []AST) {
	fmt.Fprintf(of, "#include \"boson.h\"\n\n")
	for _, a := range asts {
		if err := emitC(of, c, a); err != nil {
			fatalCtx("Fatal: %v", err)
		}
	}
	if pkgname == "main" {
		for _, a := range asts {
			if fd, ok := a.(*FuncDecl); ok && fd.Name == "main" {
				if err := emitCEntry(of, c, fd); err != nil {
					fatalCtx("Fatal: %v", err)
				}
				break
			}
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

// emitCEntry writes the C `int main` the process starts at, calling the
// mangled Boson main and using its return value (if any) as the exit code.
func emitCEntry(w io.Writer, c *Context, f *FuncDecl) (err error) {
	defer func() {
		if r := recover(); r != nil {
			if ie, ok := r.(*interpreterError); ok {
				err = ie
				return
			}
			panic(r)
		}
	}()
	if len(f.Args) != 0 {
		cUnsupported(f.p, "main with parameters (argv)")
	}
	name := cFuncName(c.Pkgname(), f.Name)
	fmt.Fprintf(w, "int main(void) {\n")
	if isIntScalar(c, f.Return) {
		fmt.Fprintf(w, "\treturn (int)%s();\n", name)
	} else {
		fmt.Fprintf(w, "\t%s();\n\treturn 0;\n", name)
	}
	fmt.Fprintf(w, "}\n")
	return nil
}

func emitCFunc(w io.Writer, c *Context, f *FuncDecl) {
	if f.Variadic {
		cUnsupported(f.p, "variadic functions")
	}
	em := &cEmitter{c: c, locals: make(map[string]ASTType, len(f.Args))}
	params := make([]string, 0, len(f.Args))
	for _, arg := range f.Args {
		em.locals[arg.Name] = arg.Type
		params = append(params, cType(c, arg.Type)+" "+cLocalName(arg.Name))
	}
	sig := "void"
	if len(params) > 0 {
		sig = strings.Join(params, ", ")
	}
	fmt.Fprintf(w, "static %s %s(%s) {\n", cType(c, f.Return), cFuncName(c.Pkgname(), f.Name), sig)
	if f.Body != nil {
		for _, stmt := range f.Body.Body {
			em.stmt(w, stmt)
		}
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
		return cLocalName(n.Name)
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
	return cFuncName(em.c.Pkgname(), sym.Name) + "(" + strings.Join(args, ", ") + ")"
}

func (em *cEmitter) op2(o *Op2) string {
	a := em.expr(o.First)
	b := em.expr(o.Second)
	switch o.Type {
	case n_add, n_sub, n_mul:
		// Signed +,-,* wrap via the boson.h helpers (well-formed C, no
		// signed-overflow UB); unsigned is already modular-defined and stays
		// a bare operator.
		if s, ok := signedScalarSuffix(em.c, em.typeOf(o.First)); ok {
			return cWrapHelper(o.Type, s) + "(" + a + ", " + b + ")"
		}
		return "(" + a + " " + opSym(o.Type) + " " + b + ")"
	case n_div, n_mod, n_bitand, n_bitor, n_xor, n_shl, n_shr:
		return "(" + a + " " + opSym(o.Type) + " " + b + ")"
	case n_lt, n_le, n_gt, n_ge, n_deq, n_neq:
		return "(" + a + " " + cCmpSym(o.Type) + " " + b + ")"
	default:
		cUnsupported(posOf(o.First), fmt.Sprintf("operator %q", opSym(o.Type)))
		return ""
	}
}

// typeOf returns the Boson type of an expression for phase-1 emit decisions
// (operand signedness). It answers from the in-scope locals rather than the
// Context, since function-scope bindings are not established on the C path.
// Unknown expressions return the zero ASTType (treated as non-signed), which
// only affects the wrap-helper choice for constructs phase 1 doesn't yet emit.
func (em *cEmitter) typeOf(a AST) ASTType {
	switch n := a.(type) {
	case *Symbol:
		if t, ok := em.locals[n.Name]; ok {
			return t
		}
	case *Literal:
		return n.ASTType(em.c)
	case *Op2:
		return em.typeOf(n.First)
	}
	return ASTType{}
}

func cLiteral(l *Literal) string {
	switch v := l.Val.(type) {
	case uint64:
		return strconv.FormatUint(v, 10)
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

// cType maps a Boson type to its C spelling. Phase 1: integer scalars, bool,
// and void.
func cType(c *Context, t ASTType) string {
	rt := c.ResolveUnderlying(t)
	if isScalarLeaf(rt) {
		switch rt.Name {
		case "void", "":
			return "void"
		case "bool":
			return "bo_bool"
		case "i8", "i16", "i32", "i64", "u8", "u16", "u32", "u64", "byte":
			return "bo_" + rt.Name
		}
	}
	cUnsupported(t.p, fmt.Sprintf("type %q", t.String()))
	return ""
}

func isScalarLeaf(t ASTType) bool {
	return t.Indirection == 0 && t.Element == nil && t.FuncSig == nil && t.AnonFields == nil
}

// isIntScalar reports whether t is a (non-void) integer/bool scalar — used to
// decide whether `main`'s return value becomes the process exit code.
func isIntScalar(c *Context, t ASTType) bool {
	rt := c.ResolveUnderlying(t)
	return isScalarLeaf(rt) && rt.Name != "void" && rt.Name != ""
}

// signedScalarSuffix returns the width suffix (e.g. "i64") if t is a signed
// integer scalar, so the wrapping helper can be named.
func signedScalarSuffix(c *Context, t ASTType) (string, bool) {
	rt := c.ResolveUnderlying(t)
	if isScalarLeaf(rt) && rt.Signed {
		return rt.Name, true
	}
	return "", false
}

// ---- name mangling -------------------------------------------------------
//
// Injective length-prefixed scheme under the reserved-namespace-safe `bo_`
// prefix (see the proposal): each segment is <len><segment>, so `.`-joined
// qualified names can't collide with underscore-containing identifiers, and a
// leading-underscore package (`_init`) becomes interior and thus non-reserved.

func cFuncName(pkg, name string) string {
	return "bo_" + cSeg(pkg) + cSeg(name)
}

func cSeg(s string) string {
	return strconv.Itoa(len(s)) + s
}

// cLocalName mangles a local/parameter name. The `bl_` prefix keeps Boson
// names that happen to be C keywords (`int`, `return`, …) from colliding.
func cLocalName(name string) string {
	return "bl_" + name
}

func cWrapHelper(op nodetype, suffix string) string {
	switch op {
	case n_add:
		return "bo_add_" + suffix
	case n_sub:
		return "bo_sub_" + suffix
	case n_mul:
		return "bo_mul_" + suffix
	}
	return "bo_op_" + suffix
}

func cCmpSym(t nodetype) string {
	switch t {
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
