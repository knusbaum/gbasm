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
	var fwd, bodies, protos, defs bytes.Buffer
	// Struct forward typedefs first (so fields and signatures can reference any
	// struct, including recursive and mutual references), then the bodies.
	for _, a := range asts {
		sd, ok := a.(*StructDecl)
		if !ok {
			continue
		}
		if err := emitCStruct(&fwd, &bodies, u, sd); err != nil {
			fatalCtx("Fatal: %v", err)
		}
	}
	// Function forward declarations — Boson permits calling a function before
	// its definition, but C needs a prior declaration — then the definitions.
	for _, a := range asts {
		if err := emitCProto(&protos, u, a); err != nil {
			fatalCtx("Fatal: %v", err)
		}
	}
	for _, a := range asts {
		if err := emitCDecl(&defs, u, a); err != nil {
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
	if fwd.Len() > 0 {
		io.Copy(of, &fwd)
		fmt.Fprintf(of, "\n")
	}
	io.Copy(of, &bodies)
	if protos.Len() > 0 {
		io.Copy(of, &protos)
		fmt.Fprintf(of, "\n")
	}
	io.Copy(of, &defs)
}

// emitCStruct writes a struct body definition. Its forward typedef is emitted
// separately (in emitCFile) so struct fields and function signatures can name
// any struct regardless of declaration order.
func emitCStruct(fwd, bodies io.Writer, u *cUnit, sd *StructDecl) (err error) {
	defer func() {
		if r := recover(); r != nil {
			if ie, ok := r.(*interpreterError); ok {
				err = ie
				return
			}
			panic(r)
		}
	}()
	// Boson gives structs and functions separate namespaces; C's typedef puts a
	// struct in the same namespace as functions, so a struct that shares a name
	// with a function can't be represented this way yet.
	if _, ok := u.c.FuncDeclForName(sd.TName); ok {
		cUnsupported(sd.p, fmt.Sprintf("struct name %q also names a function", sd.TName))
	}
	fmt.Fprintf(fwd, "typedef struct %s %s;\n", cStructName(sd.TName), cStructName(sd.TName))
	em := &cEmitter{u: u, locals: map[string]ASTType{}}
	fmt.Fprintf(bodies, "struct %s {\n", cStructName(sd.TName))
	for _, f := range sd.Fields {
		fmt.Fprintf(bodies, "\t%s;\n", em.cDecl(f.Type, cIdent(f.Name)))
	}
	fmt.Fprintf(bodies, "};\n\n")
	return nil
}

// emitCProto writes a forward declaration for a function so it can be called
// before its definition. main is the entry point and needs none; variadics and
// non-functions are left to the definition pass to handle or reject.
func emitCProto(w io.Writer, u *cUnit, a AST) (err error) {
	defer func() {
		if r := recover(); r != nil {
			if ie, ok := r.(*interpreterError); ok {
				err = ie
				return
			}
			panic(r)
		}
	}()
	f, ok := a.(*FuncDecl)
	if !ok || f.Variadic {
		return nil
	}
	if u.c.Pkgname() == "main" && f.Name == "main" {
		return nil
	}
	em := &cEmitter{u: u, locals: map[string]ASTType{}}
	linkage, head := em.cFuncHead(f, false)
	fmt.Fprintf(w, "%s%s;\n", linkage, head)
	return nil
}

// cOracleGate runs the native lowering as a semantic gate for the C backend.
//
// emitC does no checking of its own — Boson's ownership/move/escape enforcement
// lives in Compile, not ToAST — so a program the front end and emitC both
// accept can still be semantically invalid (e.g. a use after dispose). The gate
// re-derives a fresh AST + Context from the parse nodes (so Compile's Context
// mutations never touch the emit pass) and runs Compile to a discard writer
// purely for its checks; any rejection aborts the build with the same
// diagnostic the .bs path would give. Emit C only after this returns.
//
// It mirrors the .bs driver: ToAST every declaration first (so forward
// references resolve), then Compile each. Running the gate AFTER emitC makes it
// a ratchet on check segregation — a gate rejection of a program emitC emitted
// is a check that lives only in compile.go and should be hoisted into ToAST
// (unless it is a genuine .bs backend limitation). See
// docs/proposed/PROPOSAL_c_transpiler.md and
// docs/reference/AUDIT_codegen_check_segregation.md.
func cOracleGate(pkgname string, parseNodes []*Node, imports map[string]string) {
	ctx := NewContext()
	ctx.SetPkgname(pkgname)
	// Auto-import builtin into every package except builtin itself — matching
	// the main driver, so the re-derived context is identical.
	if pkgname != "builtin" {
		if builtinPath, ok := imports["builtin"]; ok {
			if err := ctx.Import("builtin", builtinPath); err != nil {
				fatalCtx("Fatal: c-backend gate: auto-import builtin: %v", err)
			}
		}
	}
	var gateAsts []AST
	for _, n := range parseNodes {
		if n.t == n_import {
			if err := ctx.Import(n.sval, imports[n.sval]); err != nil {
				fatalCtx("Fatal: c-backend gate: %v", err)
			}
			continue
		}
		a, err := n.ToAST(ctx)
		if err != nil {
			fatalCtx("Fatal: %v", err)
		}
		gateAsts = append(gateAsts, a)
	}
	for _, a := range gateAsts {
		if err := Compile(io.Discard, ctx, a); err != nil {
			fatalCtx("Fatal: %v", err)
		}
	}
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
	case *StructDecl:
		// Emitted in emitCFile's struct pass (forward typedef + body).
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
	linkage, head := em.cFuncHead(f, isEntry)
	fmt.Fprintf(w, "%s%s {\n", linkage, head)
	if f.Body != nil {
		for _, stmt := range f.Body.Body {
			em.stmt(w, stmt, 1)
		}
	}
	if isEntry && isVoidType(u.c, f.Return) {
		fmt.Fprintf(w, "\treturn 0;\n")
	}
	fmt.Fprintf(w, "}\n\n")
}

// cFuncHead builds the linkage keyword and C declarator (return type, name,
// parameter list) for a function — shared by its prototype and its definition
// so the two never diverge. `main` in package main is the C entry point;
// non-`pub` functions are file-local `static`, exactly as a hand-written C
// module marks its internal helpers. (Package-prefixed naming for cross-package
// linkage arrives with cross-package calls.)
func (em *cEmitter) cFuncHead(f *FuncDecl, isEntry bool) (linkage, head string) {
	params := make([]string, 0, len(f.Args))
	for _, arg := range f.Args {
		em.locals[arg.Name] = arg.Type
		params = append(params, em.cDecl(arg.Type, cIdent(arg.Name)))
	}
	sig := "void"
	if len(params) > 0 {
		sig = strings.Join(params, ", ")
	}
	if isEntry {
		return "", "int main(" + sig + ")"
	}
	head = em.ctype(f.Return) + " " + cIdent(f.Name) + "(" + sig + ")"
	if f.IsPub {
		return "", head
	}
	return "static ", head
}

func (em *cEmitter) stmt(w io.Writer, a AST, ind int) {
	tab := cTab(ind)
	switch n := a.(type) {
	case *Return:
		if n.Val == nil {
			fmt.Fprintf(w, "%sreturn;\n", tab)
		} else {
			fmt.Fprintf(w, "%sreturn %s;\n", tab, em.expr(n.Val))
		}
	case *Funcall:
		fmt.Fprintf(w, "%s%s;\n", tab, em.call(n))
	case *VarDecl:
		t := em.declType(n.Type, n.Init)
		em.locals[n.Name] = t
		if n.Init != nil {
			fmt.Fprintf(w, "%s%s = %s;\n", tab, em.cDecl(t, cIdent(n.Name)), em.expr(n.Init))
		} else {
			fmt.Fprintf(w, "%s%s;\n", tab, em.cDecl(t, cIdent(n.Name)))
		}
	case *Assignment:
		fmt.Fprintf(w, "%s%s = %s;\n", tab, em.expr(n.Target), em.expr(n.Val))
	case *IfStmt:
		em.ifStmt(w, n, ind, true)
	case *For:
		fmt.Fprintf(w, "%sfor (%s; %s; %s) ", tab, em.forClause(n.Init), em.condExpr(n.Cond), em.forClause(n.Step))
		em.block(w, n.Body, ind)
		fmt.Fprintf(w, "\n")
	case *Loop:
		fmt.Fprintf(w, "%sfor (;;) ", tab)
		em.block(w, n.Body, ind)
		fmt.Fprintf(w, "\n")
	case *Break:
		fmt.Fprintf(w, "%sbreak;\n", tab)
	case *Continue:
		fmt.Fprintf(w, "%scontinue;\n", tab)
	case *Block:
		em.block(w, n, ind)
		fmt.Fprintf(w, "\n")
	default:
		cUnsupported(posOf(a), fmt.Sprintf("statement %T", a))
	}
}

func cTab(n int) string { return strings.Repeat("\t", n) }

// block emits a braced compound statement (no trailing newline, so the caller
// can append ` else …`).
func (em *cEmitter) block(w io.Writer, a AST, ind int) {
	fmt.Fprintf(w, "{\n")
	if b, ok := a.(*Block); ok {
		for _, s := range b.Body {
			em.stmt(w, s, ind+1)
		}
	} else if a != nil {
		em.stmt(w, a, ind+1)
	}
	fmt.Fprintf(w, "%s}", cTab(ind))
}

// ifStmt emits an if/else, chaining `else if` inline for readability.
func (em *cEmitter) ifStmt(w io.Writer, n *IfStmt, ind int, leadTab bool) {
	if leadTab {
		fmt.Fprintf(w, "%s", cTab(ind))
	}
	fmt.Fprintf(w, "if (%s) ", em.expr(n.Cond))
	em.block(w, n.Then, ind)
	if n.Else == nil {
		fmt.Fprintf(w, "\n")
		return
	}
	fmt.Fprintf(w, " else ")
	if ei, ok := n.Else.(*IfStmt); ok {
		em.ifStmt(w, ei, ind, false)
	} else {
		em.block(w, n.Else, ind)
		fmt.Fprintf(w, "\n")
	}
}

func (em *cEmitter) condExpr(a AST) string {
	if a == nil {
		return ""
	}
	return em.expr(a)
}

// forClause emits a for-loop init/step clause (a declaration or assignment)
// with no trailing semicolon.
func (em *cEmitter) forClause(a AST) string {
	if a == nil {
		return ""
	}
	switch n := a.(type) {
	case *VarDecl:
		t := em.declType(n.Type, n.Init)
		em.locals[n.Name] = t
		if n.Init != nil {
			return em.cDecl(t, cIdent(n.Name)) + " = " + em.expr(n.Init)
		}
		return em.cDecl(t, cIdent(n.Name))
	case *Assignment:
		return em.expr(n.Target) + " = " + em.expr(n.Val)
	case *Funcall:
		return em.call(n)
	default:
		cUnsupported(posOf(a), fmt.Sprintf("for-clause %T", a))
		return ""
	}
}

// declType resolves a binding's C-relevant type: its written type, or — for a
// `:=` binding whose type is inferred — the type of its initializer.
func (em *cEmitter) declType(t ASTType, init AST) ASTType {
	if t.Name == "<infer>" {
		return em.inferType(init)
	}
	return t
}

// inferType computes the type of an initializer expression, for `:=` bindings.
// It answers from the in-scope locals (not the Context, whose function-scope
// bindings aren't established on the C path) and from node-local information.
func (em *cEmitter) inferType(a AST) ASTType {
	switch n := a.(type) {
	case *Literal:
		switch n.Val.(type) {
		case uint64:
			return numASTType() // untyped integer literals infer as i64
		case bool:
			return boolASTType()
		case string:
			return byteSliceASTType()
		}
	case *Symbol:
		if t, ok := em.locals[n.Name]; ok {
			return t
		}
	case *Op2:
		switch n.Type {
		case n_lt, n_le, n_gt, n_ge, n_deq, n_neq, n_booland, n_boolor:
			return boolASTType()
		default:
			return em.inferType(n.First) // arithmetic result takes the operand type
		}
	case *Not:
		return boolASTType()
	case *BitNot:
		return em.inferType(n.Val)
	case *Dot:
		bt := em.u.c.ResolveUnderlying(em.inferType(n.Val))
		if decl, ok := structDeclForType(em.u.c, bt); ok {
			for _, f := range decl.Fields {
				if f.Name == n.Member {
					return f.Type
				}
			}
		}
	case *Deref:
		t := em.inferType(n.Val)
		if t.Indirection > 0 {
			t.Indirection--
		}
		return t
	case *Address:
		if n.Var != "" {
			if t, ok := em.locals[n.Var]; ok {
				t.Indirection++
				return t
			}
		}
	case *NonNullAssert:
		return em.inferType(n.Val)
	case *StructLiteral:
		return n.Type
	case *Funcall:
		// The callee's return type does not depend on argument types, so this
		// resolves through the Context even when arguments are locals.
		return em.u.c.ResolveUnderlying(n.ASTType(em.u.c))
	}
	cUnsupported(posOf(a), fmt.Sprintf("type inference for a %T initializer", a))
	return ASTType{}
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
	case *Not:
		return "!" + em.unaryOperand(n.Val)
	case *BitNot:
		return "~" + em.unaryOperand(n.Val)
	case *Dot:
		return em.dot(n)
	case *StructLiteral:
		return em.structLit(n)
	case *Address:
		if n.Var != "" {
			return "&" + cIdent(n.Var)
		}
		cUnsupported(posOf(a), "address-of a literal at runtime")
		return ""
	case *Deref:
		return "*" + em.unaryOperand(n.Val)
	case *NonNullAssert:
		// The checker proved non-null; a null deref crashes in C much as the
		// Boson nil trap would exit. Pass the value through.
		return em.expr(n.Val)
	default:
		cUnsupported(posOf(a), fmt.Sprintf("expression %T", a))
		return ""
	}
}

// dot emits field access, auto-dereferencing a pointer base with `->` exactly
// as Boson's `.` does.
func (em *cEmitter) dot(d *Dot) string {
	sep := "."
	if em.u.c.ResolveUnderlying(em.inferType(d.Val)).Indirection > 0 {
		sep = "->"
	}
	return em.postfixBase(d.Val) + sep + cIdent(d.Member)
}

// postfixBase renders the base of a `.`/`->`/`[]` postfix, parenthesizing a
// base that would otherwise mis-associate.
func (em *cEmitter) postfixBase(a AST) string {
	s := em.expr(a)
	switch a.(type) {
	case *Symbol, *Dot, *Funcall, *NonNullAssert:
		return s
	}
	return "(" + s + ")"
}

// structLit emits a struct literal as a C compound literal with designated
// initializers — readable and order-independent.
func (em *cEmitter) structLit(s *StructLiteral) string {
	name := em.ctype(s.Type)
	if len(s.Fields) == 0 {
		return "(" + name + "){0}"
	}
	parts := make([]string, 0, len(s.Fields))
	for _, f := range s.Fields {
		parts = append(parts, "."+cIdent(f.Name)+" = "+em.expr(f.Val))
	}
	return "(" + name + "){ " + strings.Join(parts, ", ") + " }"
}

// unaryOperand renders the operand of a unary operator, parenthesizing a
// binary sub-expression (unary binds tighter than any binary operator).
func (em *cEmitter) unaryOperand(a AST) string {
	s := em.expr(a)
	if _, ok := a.(*Op2); ok {
		return "(" + s + ")"
	}
	return s
}

func (em *cEmitter) call(f *Funcall) string {
	if pkg := f.PkgName(); pkg != "" {
		// Qualified call. Phase 1 maps the runtime output primitives to libc
		// (what a C author writes); general cross-package calls come later.
		return em.runtimeCall(f, pkg, f.FName())
	}
	name := f.FName()
	// A type name applied to one argument is a cast, not a call.
	if len(f.Args) == 1 && em.isTypeName(name) {
		return "(" + em.ctypeName(name) + ")(" + em.expr(f.Args[0]) + ")"
	}
	// Only a genuine user function becomes a C call. Builtins (new, free, len,
	// …) and anything unrecognized are not yet supported — and must never be
	// emitted as a bogus C call.
	if _, ok := em.u.c.FuncDeclForName(name); ok {
		args := make([]string, 0, len(f.Args))
		for _, arg := range f.Args {
			args = append(args, em.expr(arg))
		}
		return cIdent(name) + "(" + strings.Join(args, ", ") + ")"
	}
	cUnsupported(f.p, fmt.Sprintf("call to %q (builtin, cast, or unresolved)", name))
	return ""
}

// isTypeName reports whether name denotes a type (so `name(x)` is a cast).
func (em *cEmitter) isTypeName(name string) bool {
	switch name {
	case "i8", "i16", "i32", "i64", "u8", "u16", "u32", "u64", "byte", "bool":
		return true
	}
	_, ok := em.u.c.StructDeclForName(name)
	return ok
}

func (em *cEmitter) ctypeName(name string) string {
	return em.ctype(ASTType{Name: name})
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
	// `-x` lowers to `0 - x` in the AST; re-sugar it to a readable unary minus.
	if o.Type == n_sub {
		if lit, ok := o.First.(*Literal); ok {
			if v, ok := lit.Val.(uint64); ok && v == 0 {
				return "-" + em.unaryOperand(o.Second)
			}
		}
	}
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
	// Pointer: *T -> "<T> *" (mut/const is best-effort and deferred).
	if rt.Indirection > 0 {
		elem := rt
		elem.Indirection--
		inner := em.ctype(elem)
		if strings.HasSuffix(inner, "*") {
			return inner + "*"
		}
		return inner + " *"
	}
	if rt.Element == nil && rt.AnonFields == nil {
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
		if _, ok := structDeclForType(em.u.c, rt); ok {
			return cStructName(rt.Name)
		}
	}
	cUnsupported(t.p, fmt.Sprintf("type %q", t.String()))
	return ""
}

func cStructName(name string) string { return cIdent(name) }

// cDecl renders a declaration "<type> <name>", attaching a pointer's `*` to the
// name (`int64_t *x`) as a human would.
func (em *cEmitter) cDecl(t ASTType, name string) string {
	ct := em.ctype(t)
	if strings.HasSuffix(ct, "*") {
		return ct + name
	}
	return ct + " " + name
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
	case n_booland:
		return "&&"
	case n_boolor:
		return "||"
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
	case n_booland:
		return 2
	case n_boolor:
		return 1
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
