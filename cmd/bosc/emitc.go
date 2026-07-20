package main

import (
	"bytes"
	"fmt"
	"io"
	"sort"
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
	needStdlib  bool
	needStddef  bool
	needString  bool               // <string.h> (memcpy) is used
	needOob     bool               // the boson_oob bounds-trap helper is used
	needNil     bool               // the boson_nil_assert helper is used
	sliceElems  map[string]ASTType // slice type name -> element type (used types only)
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
	u := &cUnit{c: c, sliceElems: map[string]ASTType{}}
	var structFwd, structBodies, globals, protos, defs bytes.Buffer
	// Body passes fill the buffers and, as a side effect of `ctype`, register
	// every slice type actually used into u.sliceElems.
	for _, a := range asts {
		sd, ok := a.(*StructDecl)
		if !ok {
			continue
		}
		if err := emitCStruct(&structFwd, &structBodies, u, sd); err != nil {
			fatalCtx("Fatal: %v", err)
		}
	}
	// File-scope variables become C globals, emitted before functions.
	for _, a := range asts {
		if v, ok := a.(*VarDecl); ok {
			if err := emitCGlobal(&globals, u, v); err != nil {
				fatalCtx("Fatal: %v", err)
			}
		}
	}
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
	if u.needStdio || u.needOob || u.needNil {
		fmt.Fprintf(of, "#include <stdio.h>\n")
	}
	if u.needStdlib || u.needOob || u.needNil {
		fmt.Fprintf(of, "#include <stdlib.h>\n")
	}
	if u.needString {
		fmt.Fprintf(of, "#include <string.h>\n")
	}
	if u.needStddef && !(u.needStdlib || u.needOob || u.needStdio || u.needNil) {
		fmt.Fprintf(of, "#include <stddef.h>\n")
	}
	fmt.Fprintf(of, "\n")
	if u.needOob {
		fmt.Fprintf(of, "static void boson_oob(int64_t i, int64_t n) {\n")
		fmt.Fprintf(of, "\tprintf(\"Fatal: Index Out Of Bounds: index[%%lld] for object with length: %%lld\\n\", (long long)i, (long long)n);\n")
		fmt.Fprintf(of, "\texit(1);\n")
		fmt.Fprintf(of, "}\n\n")
	}
	if u.needNil {
		fmt.Fprintf(of, "static void boson_nil_assert(void) {\n")
		fmt.Fprintf(of, "\tprintf(\"Fatal: nullable pointer assertion failed\\n\");\n")
		fmt.Fprintf(of, "\texit(1);\n")
		fmt.Fprintf(of, "}\n\n")
	}

	// Forward typedefs — structs, then slices (each slice field is a pointer,
	// so a forward declaration of its element suffices).
	sliceNames := make([]string, 0, len(u.sliceElems))
	for name := range u.sliceElems {
		sliceNames = append(sliceNames, name)
	}
	sort.Strings(sliceNames)
	if structFwd.Len() > 0 || len(sliceNames) > 0 {
		io.Copy(of, &structFwd)
		for _, name := range sliceNames {
			fmt.Fprintf(of, "typedef struct %s %s;\n", name, name)
		}
		fmt.Fprintf(of, "\n")
	}
	// Slice bodies.
	if len(sliceNames) > 0 {
		em := &cEmitter{u: u}
		for _, name := range sliceNames {
			ptrT := u.sliceElems[name]
			ptrT.Indirection++
			fmt.Fprintf(of, "struct %s { %s; int64_t len; };\n", name, em.cDecl(ptrT, "ptr"))
		}
		fmt.Fprintf(of, "\n")
	}

	io.Copy(of, &structBodies)
	if globals.Len() > 0 {
		io.Copy(of, &globals)
		fmt.Fprintf(of, "\n")
	}
	if protos.Len() > 0 {
		io.Copy(of, &protos)
		fmt.Fprintf(of, "\n")
	}
	io.Copy(of, &defs)
}

// emitCGlobal emits a file-scope variable as a C global (static unless pub).
func emitCGlobal(w io.Writer, u *cUnit, v *VarDecl) (err error) {
	defer func() {
		if r := recover(); r != nil {
			if ie, ok := r.(*interpreterError); ok {
				err = ie
				return
			}
			panic(r)
		}
	}()
	em := &cEmitter{u: u, locals: map[string]ASTType{}}
	t := em.declType(v.Type, v.Init)
	linkage := "static "
	if v.IsPub {
		linkage = ""
	}
	switch init := v.Init.(type) {
	case *ArrayLiteral:
		parts := make([]string, len(init.Elements))
		for i, e := range init.Elements {
			parts[i] = em.expr(e)
		}
		fmt.Fprintf(w, "%s%s = {%s};\n", linkage, em.cDecl(t, cIdent(v.Name)), strings.Join(parts, ", "))
	case nil:
		fmt.Fprintf(w, "%s%s;\n", linkage, em.cDecl(t, cIdent(v.Name)))
	default:
		fmt.Fprintf(w, "%s%s = %s;\n", linkage, em.cDecl(t, cIdent(v.Name)), em.exprInto(v.Init, t))
	}
	return nil
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
	switch n := a.(type) {
	case *FuncDecl:
		if n.Variadic {
			return nil
		}
		if u.c.Pkgname() == "main" && n.Name == "main" {
			return nil
		}
		em := &cEmitter{u: u, locals: map[string]ASTType{}}
		linkage, head := em.cFuncHead(n, false)
		fmt.Fprintf(w, "%s%s;\n", linkage, head)
	case *TypeWithMethodsDecl:
		for _, m := range n.Methods {
			emitCMethodProto(w, u, n.Name, m)
		}
	case *StructDecl:
		// A struct with a method block lowers to a bare StructDecl; its methods
		// live in the context's type-method table.
		if ms, ok := u.c.TypeMethodsFor(n.TName); ok {
			for _, m := range ms {
				emitCMethodProto(w, u, n.TName, m)
			}
		}
	}
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
	case *TypeWithMethodsDecl:
		for _, m := range n.Methods {
			emitCMethod(w, u, n.Name, m)
		}
	case *StructDecl:
		// The struct body is emitted in emitCFile's struct pass; its methods
		// (registered in the context) are emitted here.
		if ms, ok := u.c.TypeMethodsFor(n.TName); ok {
			for _, m := range ms {
				emitCMethod(w, u, n.TName, m)
			}
		}
	case *VarDecl:
		// Emitted in emitCFile's globals pass.
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
	// A user function whose name matches a C standard library function we may
	// pull in (stdio/stdlib/string) would clash with that declaration. Defer
	// rather than emit a conflicting definition. (Proper fix: package-prefixed
	// or otherwise disambiguated names, with the cross-package naming pass.)
	if !isEntry && cLibcNames[cIdent(f.Name)] {
		cUnsupported(f.p, fmt.Sprintf("function name %q collides with a C standard library function", f.Name))
	}
	params := make([]string, 0, len(f.Args))
	for _, arg := range f.Args {
		// A C array parameter decays to a pointer (pass-by-reference), which
		// would break Boson's by-value array semantics — defer it rather than
		// emit wrong C. (A struct wrapper is the eventual fix.)
		if at := em.u.c.ResolveUnderlying(arg.Type); at.IsArray() {
			cUnsupported(arg.p, "array parameter (by-value passing deferred; pass a slice)")
		}
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

// cMethodName is a type's method as a C function name: TypeName_method. This is
// the recognizable C convention for "methods" on a struct (see e.g. the way C
// libraries prefix operations with their type). Static methods (no receiver)
// share the scheme.
func cMethodName(typeName, method string) string {
	return cIdent(typeName) + "_" + cIdent(method)
}

// cMethodHead builds the linkage, declarator, and receiver-reconstruction
// prefix for an instance method. Following the kernel/COM idiom (see
// PROPOSAL_c_transpiler.md), a *pointer* receiver becomes a `void *self`
// parameter with the typed receiver reconstructed as the method's first line
// (`FD *fd = self;`) — so the method's own type matches the interface vtable
// slot with no cast. A *value* receiver is a plain typed parameter.
func (em *cEmitter) cMethodHead(typeName string, m *FuncDecl) (linkage, head, prefix string) {
	recv := m.Args[0]
	params := make([]string, 0, len(m.Args))
	if em.u.c.ResolveUnderlying(recv.Type).Indirection > 0 {
		// The void* parameter must not collide with the receiver's own name
		// (often `self`) nor any later parameter; pick a distinct spelling.
		taken := map[string]bool{cIdent(recv.Name): true}
		for _, arg := range m.Args[1:] {
			taken[cIdent(arg.Name)] = true
		}
		self := "self"
		for taken[self] {
			self += "_"
		}
		params = append(params, "void *"+self)
		em.locals[recv.Name] = recv.Type
		prefix = fmt.Sprintf("\t%s = %s;\n", em.cDecl(recv.Type, cIdent(recv.Name)), self)
	} else {
		em.locals[recv.Name] = recv.Type
		params = append(params, em.cDecl(recv.Type, cIdent(recv.Name)))
	}
	for _, arg := range m.Args[1:] {
		if at := em.u.c.ResolveUnderlying(arg.Type); at.IsArray() {
			cUnsupported(arg.p, "array parameter (by-value passing deferred; pass a slice)")
		}
		em.locals[arg.Name] = arg.Type
		params = append(params, em.cDecl(arg.Type, cIdent(arg.Name)))
	}
	head = em.ctype(m.Return) + " " + cMethodName(typeName, m.Name) + "(" + strings.Join(params, ", ") + ")"
	linkage = "static "
	if m.IsPub {
		linkage = ""
	}
	return linkage, head, prefix
}

// emitCMethodProto writes an instance method's forward declaration. Static
// methods (no receiver) and variadics are deferred.
func emitCMethodProto(w io.Writer, u *cUnit, typeName string, m *FuncDecl) {
	if m.Variadic || len(m.Args) == 0 {
		return
	}
	em := &cEmitter{u: u, locals: map[string]ASTType{}}
	linkage, head, _ := em.cMethodHead(typeName, m)
	fmt.Fprintf(w, "%s%s;\n", linkage, head)
}

// emitCMethod writes an instance method as a C function TypeName_method(...).
func emitCMethod(w io.Writer, u *cUnit, typeName string, m *FuncDecl) {
	if m.Variadic {
		cUnsupported(m.p, "variadic method")
	}
	if len(m.Args) == 0 {
		cUnsupported(m.p, "static method (no receiver)")
	}
	em := &cEmitter{u: u, locals: make(map[string]ASTType, len(m.Args))}
	linkage, head, prefix := em.cMethodHead(typeName, m)
	fmt.Fprintf(w, "%s%s {\n", linkage, head)
	fmt.Fprint(w, prefix)
	if m.Body != nil {
		for _, s := range m.Body.Body {
			em.stmt(w, s, 1)
		}
	}
	fmt.Fprintf(w, "}\n\n")
}

func (em *cEmitter) stmt(w io.Writer, a AST, ind int) {
	tab := cTab(ind)
	switch n := a.(type) {
	case *Return:
		if n.Val == nil {
			fmt.Fprintf(w, "%sreturn;\n", tab)
		} else {
			em.hoistGuards(w, n.Val, ind)
			fmt.Fprintf(w, "%sreturn %s;\n", tab, em.expr(n.Val))
		}
	case *Funcall:
		em.hoistGuards(w, n, ind)
		fmt.Fprintf(w, "%s%s;\n", tab, em.call(n))
	case *VarDecl:
		t := em.declType(n.Type, n.Init)
		em.locals[n.Name] = t
		if at := em.u.c.ResolveUnderlying(t); at.IsArray() && !isArrayInitLiteral(n.Init) {
			cUnsupported(posOf(n), "copying an array value in an initializer (needs memcpy)")
		}
		if nf, ok := isNewCall(n.Init); ok {
			em.emitNewInto(w, ind, t, cIdent(n.Name), true, nf)
		} else if al, ok := n.Init.(*ArrayLiteral); ok {
			parts := make([]string, len(al.Elements))
			for i, e := range al.Elements {
				em.hoistGuards(w, e, ind)
				parts[i] = em.expr(e)
			}
			fmt.Fprintf(w, "%s%s = {%s};\n", tab, em.cDecl(t, cIdent(n.Name)), strings.Join(parts, ", "))
		} else if n.Init != nil {
			em.hoistGuards(w, n.Init, ind)
			fmt.Fprintf(w, "%s%s = %s;\n", tab, em.cDecl(t, cIdent(n.Name)), em.exprInto(n.Init, t))
		} else {
			// No initializer: Boson gives it the zero value; C would leave a
			// local uninitialized, so emit the zero explicitly.
			fmt.Fprintf(w, "%s%s = %s;\n", tab, em.cDecl(t, cIdent(n.Name)), em.zeroValue(t))
		}
	case *Assignment:
		if at := em.u.c.ResolveUnderlying(em.inferType(n.Target)); at.IsArray() {
			cUnsupported(posOf(n.Target), "whole-array assignment (needs memcpy)")
		}
		em.hoistGuards(w, n.Target, ind)
		if nf, ok := isNewCall(n.Val); ok {
			em.emitNewInto(w, ind, em.inferType(n.Target), em.expr(n.Target), false, nf)
		} else {
			em.hoistGuards(w, n.Val, ind)
			fmt.Fprintf(w, "%s%s = %s;\n", tab, em.expr(n.Target), em.expr(n.Val))
		}
	case *IfStmt:
		em.hoistGuards(w, n.Cond, ind)
		em.ifStmt(w, n, ind, true)
	case *For:
		if hasIndex(n.Cond) || hasIndex(n.Step) {
			cUnsupported(posOf(a), "indexing in a for-loop condition or step")
		}
		switch fc := n.Init.(type) {
		case *VarDecl:
			em.hoistGuards(w, fc.Init, ind)
		case *Assignment:
			em.hoistGuards(w, fc.Val, ind)
		}
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
	case *Dispose:
		// Compile-time only — dispose discharges the ownership obligation in
		// the type system and has no runtime effect, so it emits nothing.
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

// zeroValue is the C zero initializer for a type — Boson gives an
// uninitialized `var` its zero value, and C zero-inits globals but not locals.
func (em *cEmitter) zeroValue(t ASTType) string {
	rt := em.u.c.ResolveUnderlying(t)
	if rt.Indirection > 0 {
		em.u.needStddef = true
		return "NULL"
	}
	if rt.Element == nil && rt.AnonFields == nil {
		switch rt.Name {
		case "bool":
			em.u.needStdbool = true
			return "false"
		case "i8", "i16", "i32", "i64", "u8", "u16", "u32", "u64", "byte":
			return "0"
		}
	}
	return "{0}" // struct, array, slice
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
		if em.u.c.IsImportedPackage(n.Name) {
			cUnsupported(posOf(n), fmt.Sprintf("reference to imported package %q (cross-package access)", n.Name))
		}
		// A file-scope global (or other Context-resolvable name).
		return em.u.c.ResolveUnderlying(n.ASTType(em.u.c))
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
	case *Index:
		bt := em.u.c.ResolveUnderlying(em.inferType(n.Val))
		if bt.Element != nil {
			return *bt.Element
		}
	case *ArrayLiteral:
		if len(n.Elements) > 0 {
			elem := em.inferType(n.Elements[0])
			return ASTType{ArraySize: len(n.Elements), Element: &elem}
		}
	case *SliceOp:
		bt := em.u.c.ResolveUnderlying(em.inferType(n.Val))
		if bt.Element != nil {
			elem := *bt.Element
			return ASTType{Element: &elem}
		}
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
	case *Index:
		// Clean access; the bounds check is hoisted ahead of the statement.
		base := em.postfixBase(n.Val)
		bt := em.u.c.ResolveUnderlying(em.inferType(n.Val))
		if bt.IsSlice() {
			base += ".ptr"
		}
		return base + "[" + em.indexExpr(n) + "]"
	case *ArrayLiteral:
		// Only valid as an initializer (handled in VarDecl); a brace-init is
		// not a general C expression.
		cUnsupported(n.p, "array literal outside an initializer")
		return ""
	case *SliceOp:
		return em.sliceOp(n)
	case *Alloc:
		// alloc(T): zero-initialized allocation (the mmap heap zeroes).
		em.u.needStdlib = true
		ct := em.ctype(n.Type)
		return "(" + ct + " *)calloc(1, sizeof(" + ct + "))"
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
	decl, _ := structDeclForType(em.u.c, em.u.c.ResolveUnderlying(s.Type))
	parts := make([]string, 0, len(s.Fields))
	for _, f := range s.Fields {
		parts = append(parts, "."+cIdent(f.Name)+" = "+em.exprInto(f.Val, em.fieldType(decl, f.Name)))
	}
	return "(" + name + "){ " + strings.Join(parts, ", ") + " }"
}

func (em *cEmitter) fieldType(decl *StructDecl, name string) ASTType {
	if decl != nil {
		for _, f := range decl.Fields {
			if f.Name == name {
				return f.Type
			}
		}
	}
	return ASTType{}
}

// exprInto emits an expression targeting a known destination type. Its one job
// today: a string literal assigned to a [N]byte array stays a C string literal
// (uint8_t a[N] = "hi"), rather than a slice literal.
func (em *cEmitter) exprInto(a AST, dest ASTType) string {
	if lit, ok := a.(*Literal); ok {
		if s, ok := lit.Val.(string); ok {
			if rt := em.u.c.ResolveUnderlying(dest); rt.IsArray() {
				return cStringLit(s)
			}
		}
	}
	return em.expr(a)
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
	// (expr).method(args): the receiver is an arbitrary expression.
	if recv := f.ReceiverExpr(); recv != nil {
		return em.methodCall(f, recv, em.inferType(recv), f.FName())
	}
	if pkg := f.PkgName(); pkg != "" {
		// pkg is a bare Symbol before `.`: an imported package, or a value
		// variable whose method is being called (v.method(args)).
		if em.u.c.IsImportedPackage(pkg) {
			// Qualified call. Phase 1 maps the runtime output primitives to libc
			// (what a C author writes); general cross-package calls come later.
			return em.runtimeCall(f, pkg, f.FName())
		}
		if t, ok := em.locals[pkg]; ok {
			return em.methodCall(f, &Symbol{Name: pkg}, t, f.FName())
		}
		// A type name (static method) or a global-var receiver: not yet.
		cUnsupported(f.p, fmt.Sprintf("call to %s.%s (static method or package-qualified)", pkg, f.FName()))
	}
	name := f.FName()
	if name == "len" && len(f.Args) == 1 {
		return em.lenBuiltin(f.Args[0])
	}
	if name == "free" && len(f.Args) == 1 {
		em.u.needStdlib = true
		return "free(" + em.expr(f.Args[0]) + ")"
	}
	if name == "new" && len(f.Args) == 1 {
		// Nested new(expr): allocate and copy inline, one expression, no helper.
		// (In a var-init/assign position, VarDecl/Assignment emit the cleaner
		// two-line malloc + assignment instead.)
		em.u.needStdlib = true
		em.u.needString = true
		ct := em.ctype(em.inferType(f.Args[0]))
		return "(" + ct + " *)memcpy(malloc(sizeof(" + ct + ")), &" + em.unaryOperand(f.Args[0]) + ", sizeof(" + ct + "))"
	}
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

// methodCall emits a concrete method call `recv.method(args)` as a direct call
// to the type's method function, TypeName_method(<recv>, args). The receiver is
// adapted to the method's declared shape: a pointer receiver takes the address
// of a value source (or the pointer as-is); a value receiver takes the value
// (dereferencing a pointer source).
func (em *cEmitter) methodCall(f *Funcall, recv AST, recvType ASTType, method string) string {
	typeName := recvType.Name
	// A cross-package receiver type (pkg.Type) needs cross-package name
	// qualification, which the C backend doesn't do yet.
	if strings.Contains(typeName, ".") {
		cUnsupported(f.p, fmt.Sprintf("cross-package method %s.%s", typeName, method))
	}
	m, ok := em.u.c.MethodForType(typeName, method)
	if !ok {
		cUnsupported(f.p, fmt.Sprintf("method %q on type %s", method, typeName))
	}
	if len(m.Args) == 0 {
		cUnsupported(f.p, fmt.Sprintf("static method %s.%s (no receiver)", typeName, method))
	}
	recvIsPtr := em.u.c.ResolveUnderlying(recvType).Indirection > 0
	wantPtr := em.u.c.ResolveUnderlying(m.Args[0].Type).Indirection > 0
	var recvArg string
	switch {
	case wantPtr && !recvIsPtr:
		recvArg = "&" + em.unaryOperand(recv)
	case !wantPtr && recvIsPtr:
		recvArg = "*" + em.unaryOperand(recv)
	default:
		recvArg = em.expr(recv)
	}
	args := make([]string, 0, len(f.Args)+1)
	args = append(args, recvArg)
	for _, arg := range f.Args {
		args = append(args, em.expr(arg))
	}
	return cMethodName(typeName, method) + "(" + strings.Join(args, ", ") + ")"
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

// isArrayInitLiteral reports whether an initializer is one C can use for an
// array (a brace `{…}` array literal or a string literal), vs. a general array
// value (which C cannot copy in an initializer — that needs memcpy).
func isArrayInitLiteral(a AST) bool {
	if a == nil {
		return true
	}
	if _, ok := a.(*ArrayLiteral); ok {
		return true
	}
	if lit, ok := a.(*Literal); ok {
		if _, ok := lit.Val.(string); ok {
			return true
		}
	}
	return false
}

// isNewCall reports whether a is a `new(expr)` builtin call.
func isNewCall(a AST) (*Funcall, bool) {
	if f, ok := a.(*Funcall); ok && f.PkgName() == "" && f.FName() == "new" && len(f.Args) == 1 {
		return f, true
	}
	return nil, false
}

// emitNewInto emits the natural inline form of new(expr) into a pointer target:
//
//	[T *]target = malloc(sizeof(T));
//	*target = <expr>;
//
// `ptrType` is the target's pointer type; `declare` prefixes it (a var decl vs
// an assignment).
func (em *cEmitter) emitNewInto(w io.Writer, ind int, ptrType ASTType, target string, declare bool, nf *Funcall) {
	em.u.needStdlib = true
	pt := em.u.c.ResolveUnderlying(ptrType)
	pt.Indirection--
	ct := em.ctype(pt)
	lhs := target
	if declare {
		lhs = em.cDecl(ptrType, target)
	}
	fmt.Fprintf(w, "%s%s = malloc(sizeof(%s));\n", cTab(ind), lhs, ct)
	em.hoistGuards(w, nf.Args[0], ind)
	fmt.Fprintf(w, "%s*%s = %s;\n", cTab(ind), target, em.expr(nf.Args[0]))
}

// lenBuiltin lowers len(x): a slice's stored length, or a fixed array's size.
func (em *cEmitter) lenBuiltin(a AST) string {
	t := em.u.c.ResolveUnderlying(em.inferType(a))
	if t.IsSlice() {
		return em.postfixBase(a) + ".len"
	}
	if t.IsArray() {
		return fmt.Sprintf("%d", t.ArraySize)
	}
	cUnsupported(posOf(a), "len of a non-array, non-slice")
	return ""
}

// sliceOp lowers x[lo:hi] to a slice compound literal. The pointer is element-
// typed, so the arithmetic is plain (ptr + lo); an array base decays to its
// element pointer. Bounds are checked by emitSliceCheck (hoisted).
func (em *cEmitter) sliceOp(n *SliceOp) string {
	bt := em.u.c.ResolveUnderlying(em.inferType(n.Val))
	if bt.Element == nil {
		cUnsupported(posOf(n.Val), "slice of a non-array, non-slice")
	}
	elem := *bt.Element
	name := em.ctype(ASTType{Element: &elem}) // registers the slice type; returns its name
	base := em.postfixBase(n.Val)
	ptr := base
	if bt.IsSlice() {
		ptr = base + ".ptr"
	}
	lo := ""
	if n.Lower != nil {
		lo = em.expr(n.Lower)
	}
	hi := ""
	switch {
	case n.Upper != nil:
		hi = em.expr(n.Upper)
	case bt.IsSlice():
		hi = base + ".len"
	default:
		hi = fmt.Sprintf("%d", bt.ArraySize)
	}
	if lo != "" {
		ptr += " + " + lo
		return "(" + name + "){ " + ptr + ", " + hi + " - " + lo + " }"
	}
	return "(" + name + "){ " + ptr + ", " + hi + " }"
}

func (em *cEmitter) emitSliceCheck(w io.Writer, n *SliceOp, ind int) {
	bt := em.u.c.ResolveUnderlying(em.inferType(n.Val))
	base := em.postfixBase(n.Val)
	baselen := fmt.Sprintf("%d", bt.ArraySize)
	if bt.IsSlice() {
		baselen = base + ".len"
	}
	if n.Upper != nil {
		if exprHasCall(n.Upper) {
			cUnsupported(posOf(n.Upper), "a side-effecting slice bound")
		}
		hi := em.expr(n.Upper)
		fmt.Fprintf(w, "%sif ((uint64_t)(%s) > (uint64_t)(%s)) boson_oob(%s, %s);\n", cTab(ind), hi, baselen, hi, baselen)
		em.u.needOob = true
	}
	if n.Lower != nil {
		if exprHasCall(n.Lower) {
			cUnsupported(posOf(n.Lower), "a side-effecting slice bound")
		}
		lo := em.expr(n.Lower)
		hi := baselen
		if n.Upper != nil {
			hi = em.expr(n.Upper)
		}
		fmt.Fprintf(w, "%sif ((uint64_t)(%s) > (uint64_t)(%s)) boson_oob(%s, %s);\n", cTab(ind), lo, hi, lo, hi)
		em.u.needOob = true
	}
}

// runtimeCall maps a recognized runtime output primitive to its idiomatic libc
// equivalent. The print helpers write raw bytes / a value with no added
// newline, matching the Boson semantics.
func (em *cEmitter) runtimeCall(f *Funcall, pkg, name string) string {
	switch pkg + "." + name {
	case "string.puts":
		// puts([]byte): a string literal via fputs, or a runtime byte slice via
		// fwrite of its bytes.
		if len(f.Args) == 1 {
			if lit, ok := f.Args[0].(*Literal); ok {
				if s, ok := lit.Val.(string); ok {
					em.u.needStdio = true
					return "fputs(" + cStringLit(s) + ", stdout)"
				}
			}
			em.u.needStdio = true
			base := em.postfixBase(f.Args[0])
			return "fwrite(" + base + ".ptr, 1, " + base + ".len, stdout)"
		}
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
	case byte:
		return fmt.Sprintf("%d", v)
	case string:
		// A Boson string literal is a []byte value, so it lowers to a slice
		// literal over the (read-only) C string. (string.puts of a literal
		// still uses fputs directly — that path checks for a *Literal first.)
		name := em.ctype(byteSliceASTType())
		return "(" + name + "){ (uint8_t *)" + cStringLit(v) + ", " + fmt.Sprintf("%d", len(v)) + " }"
	case bool:
		em.u.needStdbool = true
		if v {
			return "true"
		}
		return "false"
	case nil:
		em.u.needStddef = true
		return "NULL"
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
	// Slice: []T -> a per-element-type struct, registered lazily and emitted
	// once at the top of the unit. Access stays clean (s.ptr[i]).
	if rt.IsSlice() {
		em.ctype(*rt.Element) // ensure the element's own types (e.g. nested slices) register
		name := "slice_" + cTypeTag(em, *rt.Element)
		em.u.sliceElems[name] = *rt.Element
		return name
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
			// A cross-package struct type resolves to a qualified name
			// (pkg.Type), which is not a valid C identifier. Cross-package
			// support (name qualification, shared type emission) comes later.
			if strings.Contains(rt.Name, ".") {
				cUnsupported(t.p, fmt.Sprintf("cross-package struct type %q", rt.Name))
			}
			return cStructName(rt.Name)
		}
	}
	cUnsupported(t.p, fmt.Sprintf("type %q", t.String()))
	return ""
}

func cStructName(name string) string { return cIdent(name) }

// cTypeTag builds an identifier fragment naming a type, used to name slice
// types deterministically from their element (slice_int64_t, slice_Point,
// slice_p_Point for []*Point, slice_slice_uint8_t for [][]byte).
func cTypeTag(em *cEmitter, t ASTType) string {
	rt := em.u.c.ResolveUnderlying(t)
	if rt.Indirection > 0 {
		elem := rt
		elem.Indirection--
		return "p_" + cTypeTag(em, elem)
	}
	if rt.IsSlice() {
		return "slice_" + cTypeTag(em, *rt.Element)
	}
	if rt.IsArray() {
		return fmt.Sprintf("arr%d_", rt.ArraySize) + cTypeTag(em, *rt.Element)
	}
	return em.ctype(rt) // scalar or struct: a valid identifier already
}

// cDecl renders a declaration "<type> <name>", attaching a pointer's `*` to the
// name (`int64_t *x`) and an array's `[N]` after it (`int64_t a[3]`) as a human
// would.
func (em *cEmitter) cDecl(t ASTType, name string) string {
	rt := em.u.c.ResolveUnderlying(t)
	// A real array (not a pointer-to-array, whose C syntax is `T (*p)[N]` and
	// which we don't yet emit — that falls through to ctype -> unsupported).
	if rt.Element != nil && rt.ArraySize > 0 && rt.Indirection == 0 {
		if elem := em.u.c.ResolveUnderlying(*rt.Element); elem.Element != nil && elem.ArraySize > 0 {
			cUnsupported(rt.p, "multidimensional array")
		}
		return em.cDecl(*rt.Element, name) + fmt.Sprintf("[%d]", rt.ArraySize)
	}
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

// cLibcNames are C standard library function names that could be declared by a
// header the emitter includes; a user function sharing one is deferred.
var cLibcNames = map[string]bool{
	"printf": true, "fprintf": true, "puts": true, "fputs": true, "putchar": true,
	"fputc": true, "fwrite": true, "fread": true, "malloc": true, "calloc": true,
	"realloc": true, "free": true, "memcpy": true, "memset": true, "memmove": true,
	"strlen": true, "strcmp": true, "strcpy": true, "atoi": true, "atol": true,
	"abs": true, "labs": true, "exit": true, "abort": true, "qsort": true,
	"getchar": true, "main": true,
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

// indexExpr renders an index — a dynamic expression or a constant.
func (em *cEmitter) indexExpr(n *Index) string {
	if n.NAST != nil {
		return em.expr(n.NAST)
	}
	return fmt.Sprintf("%d", n.N)
}

// hoistGuards emits the bounds checks an expression needs as guard statements
// ahead of the statement containing it, so the accesses themselves stay clean.
// Nested indices are checked innermost-first, in evaluation order.
func (em *cEmitter) hoistGuards(w io.Writer, a AST, ind int) {
	if a == nil {
		return
	}
	switch n := a.(type) {
	case *Op2:
		em.hoistGuards(w, n.First, ind)
		em.hoistGuards(w, n.Second, ind)
	case *Funcall:
		for _, arg := range n.Args {
			em.hoistGuards(w, arg, ind)
		}
	case *Dot:
		em.hoistGuards(w, n.Val, ind)
	case *Deref:
		em.hoistGuards(w, n.Val, ind)
	case *Not:
		em.hoistGuards(w, n.Val, ind)
	case *BitNot:
		em.hoistGuards(w, n.Val, ind)
	case *NonNullAssert:
		em.hoistGuards(w, n.Val, ind)
		if !exprHasCall(n.Val) {
			fmt.Fprintf(w, "%sif ((%s) == NULL) boson_nil_assert();\n", cTab(ind), em.expr(n.Val))
			em.u.needNil = true
			em.u.needStddef = true
		}
	case *StructLiteral:
		for _, f := range n.Fields {
			em.hoistGuards(w, f.Val, ind)
		}
	case *Index:
		em.hoistGuards(w, n.Val, ind)
		em.hoistGuards(w, n.NAST, ind)
		em.emitBoundsCheck(w, n, ind)
	case *SliceOp:
		em.hoistGuards(w, n.Val, ind)
		em.hoistGuards(w, n.Lower, ind)
		em.hoistGuards(w, n.Upper, ind)
		em.emitSliceCheck(w, n, ind)
	}
}

func (em *cEmitter) emitBoundsCheck(w io.Writer, n *Index, ind int) {
	bt := em.u.c.ResolveUnderlying(em.inferType(n.Val))
	// A constant index into a fixed array is verified in-bounds at compile time
	// (the native path rejects an out-of-range constant), so no runtime check.
	if n.NAST == nil && bt.IsArray() {
		return
	}
	if n.NAST != nil && exprHasCall(n.NAST) {
		cUnsupported(posOf(n.NAST), "a side-effecting index expression")
	}
	idx := em.indexExpr(n)
	var length string
	switch {
	case bt.IsSlice():
		length = em.postfixBase(n.Val) + ".len"
	case bt.IsArray():
		length = fmt.Sprintf("%d", bt.ArraySize)
	default:
		cUnsupported(posOf(n), "index of a non-array, non-slice")
	}
	fmt.Fprintf(w, "%sif ((uint64_t)(%s) >= (uint64_t)(%s)) boson_oob(%s, %s);\n",
		cTab(ind), idx, length, idx, length)
	em.u.needOob = true
}

// exprHasCall reports whether an expression contains a function call — used to
// avoid double-evaluating a hoisted sub-expression. Unlike compile.go's
// containsFuncall it handles every emittable node and never panics.
func exprHasCall(a AST) bool {
	switch n := a.(type) {
	case nil:
		return false
	case *Funcall:
		return true
	case *Op2:
		return exprHasCall(n.First) || exprHasCall(n.Second)
	case *Dot:
		return exprHasCall(n.Val)
	case *Deref:
		return exprHasCall(n.Val)
	case *Not:
		return exprHasCall(n.Val)
	case *BitNot:
		return exprHasCall(n.Val)
	case *NonNullAssert:
		return exprHasCall(n.Val)
	case *Index:
		return exprHasCall(n.Val) || exprHasCall(n.NAST)
	case *SliceOp:
		return exprHasCall(n.Val) || exprHasCall(n.Lower) || exprHasCall(n.Upper)
	case *StructLiteral:
		for _, f := range n.Fields {
			if exprHasCall(f.Val) {
				return true
			}
		}
	case *ArrayLiteral:
		for _, e := range n.Elements {
			if exprHasCall(e) {
				return true
			}
		}
	}
	return false
}

// hasIndex reports whether an expression contains any indexing (used to defer
// for-loop conditions/steps, where a hoisted check would run at the wrong time).
func hasIndex(a AST) bool {
	switch n := a.(type) {
	case *Index, *SliceOp:
		return true
	case *Op2:
		return hasIndex(n.First) || hasIndex(n.Second)
	case *Funcall:
		for _, arg := range n.Args {
			if hasIndex(arg) {
				return true
			}
		}
	case *Dot:
		return hasIndex(n.Val)
	case *Deref:
		return hasIndex(n.Val)
	case *Not:
		return hasIndex(n.Val)
	case *BitNot:
		return hasIndex(n.Val)
	case *NonNullAssert:
		return hasIndex(n.Val)
	}
	return false
}

func cUnsupported(p position, what string) {
	panic(&interpreterError{msg: fmt.Sprintf("c backend (phase 1): %s not yet supported", what), p: p})
}

func posOf(a AST) position {
	if p, ok := a.(interface{ Pos() position }); ok {
		return p.Pos()
	}
	return position{}
}
