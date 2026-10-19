package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Union alternatives are structural type metadata, not a separately qualified
// wrapper. Their order is canonical rather than the order in source code.
func (t ASTType) IsUnion() bool { return t.Union != nil }

func unionType(alternatives []ASTType, p position) ASTType {
	var flat []ASTType
	for _, a := range alternatives {
		if a.IsUnion() {
			flat = append(flat, a.Union...)
		} else {
			flat = append(flat, a)
		}
	}
	seen := make(map[string]ASTType)
	for _, a := range flat {
		if a.Same(voidASTType()) {
			panic(&interpreterError{msg: "void cannot be a union alternative", p: p})
		}
		key := canonicalTypeNode(a, true)
		if old, ok := seen[key]; ok {
			panic(&interpreterError{msg: fmt.Sprintf("Union alternatives %s and %s differ only in ownership, mutability, or nullability (or are duplicates)", old, a), p: p})
		}
		seen[key] = a
	}
	sort.Slice(flat, func(i, j int) bool { return canonicalTypeNode(flat[i], false) < canonicalTypeNode(flat[j], false) })
	return ASTType{Union: flat, p: p}
}

// canonicalTypeNode implements the version-1 length-prefixed structural
// encoding in PROPOSAL_union_types.md. The outer version prefix is omitted
// for children. Named types must be qualified before comparing package types.
func canonicalTypeNode(t ASTType, stripQualifiers bool) string {
	field := func(s string) string { return strconv.Itoa(len(s)) + ":" + s }
	node := func(kind string, fields ...string) string {
		flags := []byte{'0', '0', '0'}
		if !stripQualifiers {
			if t.OwnedMask&1 != 0 {
				flags[0] = '1'
			}
			if t.MutMask&1 != 0 {
				flags[1] = '1'
			}
			if t.NilMask&1 != 0 {
				flags[2] = '1'
			}
		}
		s := field(kind) + string(flags)
		for _, f := range fields {
			s += field(f)
		}
		return s
	}
	child := func(a ASTType) string { return canonicalTypeNode(a, stripQualifiers) }
	if t.Indirection > 0 {
		inner := t
		inner.Indirection--
		inner.OwnedMask >>= 1
		inner.MutMask >>= 1
		inner.NilMask >>= 1
		return node("pointer", child(inner))
	}
	if t.Element != nil {
		inner := *t.Element
		if t.IsArray() {
			inner.OwnedMask |= t.OwnedMask &^ 1
			inner.MutMask |= t.MutMask
			return node("array", strconv.Itoa(t.ArraySize), child(inner))
		}
		inner.OwnedMask |= t.OwnedMask >> 1
		inner.MutMask |= t.MutMask >> 1
		inner.NilMask |= t.NilMask >> 1
		return node("slice", child(inner))
	}
	if t.IsUnion() {
		children := make([]string, len(t.Union))
		for i, a := range t.Union {
			children[i] = child(a)
		}
		sort.Strings(children)
		return node("union", append([]string{strconv.Itoa(len(children))}, children...)...)
	}
	if t.FuncSig != nil {
		fields := []string{"0", strconv.Itoa(len(t.FuncSig.Args))}
		for _, a := range t.FuncSig.Args {
			fields = append(fields, child(a))
		}
		r := t.FuncSig.Return
		switch {
		case r.Same(voidASTType()):
			fields = append(fields, "0")
		case r.MultiReturn:
			fields = append(fields, strconv.Itoa(len(r.AnonFields)))
			for _, f := range r.AnonFields {
				fields = append(fields, child(f.Type))
			}
		default:
			fields = append(fields, "1", child(r))
		}
		return node("function", fields...)
	}
	if t.AnonFields != nil {
		fields := []string{strconv.Itoa(len(t.AnonFields))}
		for _, f := range t.AnonFields {
			fields = append(fields, f.Name, child(f.Type))
		}
		return node("struct", fields...)
	}
	pkg, name, ok := strings.Cut(t.Name, ".")
	if !ok {
		pkg, name = "", t.Name
	}
	if isBuiltinTypeName(name) && pkg == "" {
		pkg = "builtin"
	}
	return node("named", pkg, name)
}

func unionTypesEqual(a, b ASTType, stripQualifiers bool) bool {
	return a.IsUnion() && b.IsUnion() && canonicalTypeNode(a, stripQualifiers) == canonicalTypeNode(b, stripQualifiers)
}

func isBuiltinTypeName(s string) bool {
	switch s {
	case "i8", "i16", "i32", "i64", "u8", "u16", "u32", "u64", "f32", "f64", "byte", "bool", "uintptr", "void":
		return true
	}
	return false
}

func rejectUnionWrapper(t ASTType, p position) {
	if t.IsUnion() {
		panic(&interpreterError{msg: "Union containers cannot have indirection or qualifiers", p: p})
	}
}

// Re-normalization is necessary after source/import name qualification: the
// canonical name, not an import alias or an unqualified spelling, orders tags.
func normalizeUnion(t *ASTType) {
	if t.IsUnion() {
		*t = unionType(t.Union, t.p)
	}
}

// Resolve union aliases without erasing the nominal identity of ordinary
// payload types. Qualifiers on a union alias remain forbidden.
func resolveUnionAliases(c *Context, t ASTType) ASTType {
	if c == nil {
		return t
	}
	if t.Name != "" {
		if underlying, ok := namedUnionAlias(c, t.Name, make(map[string]bool)); ok {
			if t.Indirection != 0 || t.MutMask != 0 || t.OwnedMask != 0 || t.NilMask != 0 {
				rejectUnionWrapper(underlying, t.p)
			}
			t = underlying
		}
	}
	if t.IsUnion() {
		parts := make([]ASTType, len(t.Union))
		for i, a := range t.Union {
			parts[i] = resolveUnionAliases(c, a)
		}
		return unionType(parts, t.p)
	}
	if t.Element != nil {
		e := resolveUnionAliases(c, *t.Element)
		rejectUnionWrapper(e, t.p)
		t.Element = &e
	}
	if t.FuncSig != nil {
		sig := *t.FuncSig
		sig.Args = append([]ASTType(nil), sig.Args...)
		for i := range sig.Args {
			sig.Args[i] = resolveUnionAliases(c, sig.Args[i])
		}
		sig.Return = resolveUnionAliases(c, sig.Return)
		t.FuncSig = &sig
	}
	if t.AnonFields != nil {
		t.AnonFields = append([]Binding(nil), t.AnonFields...)
		for i := range t.AnonFields {
			t.AnonFields[i].Type = resolveUnionAliases(c, t.AnonFields[i].Type)
		}
	}
	return t
}

func unionContextNode(c *Context, t ASTType, strip bool) string {
	// Canonical package names are used for ordering even while the ordinary
	// resolver keeps a local named payload's source spelling unqualified.
	var qualify func(ASTType) ASTType
	qualify = func(a ASTType) ASTType {
		if a.Name != "" && !isBuiltinTypeName(a.Name) && !strings.Contains(a.Name, ".") {
			if iface, ok := c.InterfaceForName(a.Name); ok && strings.Contains(iface.Name, ".") {
				a.Name = iface.Name
			} else if c.Pkgname() != "" {
				a.Name = c.Pkgname() + "." + a.Name
			}
		}
		if a.Element != nil {
			e := qualify(*a.Element)
			a.Element = &e
		}
		if a.Union != nil {
			a.Union = append([]ASTType(nil), a.Union...)
			for i := range a.Union {
				a.Union[i] = qualify(a.Union[i])
			}
		}
		if a.AnonFields != nil {
			a.AnonFields = append([]Binding(nil), a.AnonFields...)
			for i := range a.AnonFields {
				a.AnonFields[i].Type = qualify(a.AnonFields[i].Type)
			}
		}
		if a.FuncSig != nil {
			sig := *a.FuncSig
			sig.Args = append([]ASTType(nil), sig.Args...)
			for i := range sig.Args {
				sig.Args[i] = qualify(sig.Args[i])
			}
			sig.Return = qualify(sig.Return)
			a.FuncSig = &sig
		}
		return a
	}
	return canonicalTypeNode(qualify(t), strip)
}

func unionAlternatives(c *Context, t ASTType) []ASTType {
	alternatives := append([]ASTType(nil), t.Union...)
	sort.Slice(alternatives, func(i, j int) bool {
		return unionContextNode(c, alternatives[i], false) < unionContextNode(c, alternatives[j], false)
	})
	seen := map[string]bool{}
	for _, a := range alternatives {
		key := unionContextNode(c, a, true)
		if seen[key] {
			panic(&interpreterError{msg: "Union alternatives differ only in qualifiers or are duplicates after name resolution", p: t.p})
		}
		seen[key] = true
	}
	return alternatives
}

func unionHasBorrowedPayload(c *Context, t ASTType) bool {
	seen := map[string]bool{}
	var has func(ASTType) bool
	has = func(a ASTType) bool {
		if a.IsUnion() {
			for _, part := range a.Union {
				if has(part) {
					return true
				}
			}
			return false
		}
		a = c.ResolveUnderlying(a)
		if a.Indirection > 0 || a.IsSlice() || c.IsInterfaceType(a) {
			return a.OwnedMask&1 == 0
		}
		if a.FuncSig != nil {
			return false
		}
		if seen[a.Name] {
			return false
		}
		if a.Name != "" {
			seen[a.Name] = true
		}
		if a.Element != nil {
			return has(*a.Element)
		}
		if sd, ok := structDeclForType(c, a); ok {
			for _, f := range sd.Fields {
				if has(f.Type) {
					return true
				}
			}
		}
		return false
	}
	return has(t)
}

func sameUnionInContext(c *Context, a, b ASTType) bool {
	return a.IsUnion() && b.IsUnion() && unionContextNode(c, a, false) == unionContextNode(c, b, false)
}

func resolveFunctionUnions(c *Context, f *FuncDecl) {
	f.Return = resolveUnionAliases(c, f.Return)
	for i := range f.Args {
		f.Args[i].Type = resolveUnionAliases(c, f.Args[i].Type)
	}
}

func unionTransfersResource(t ASTType) bool {
	for _, a := range t.Union {
		if a.OwnedMask&1 != 0 && (a.Indirection > 0 || a.IsSlice()) {
			return true
		}
	}
	return false
}

// Aliases of aliases remain structural when the chain ends at a union.
// Ordinary nominal payload aliases must keep their existing identities.
func namedUnionAlias(c *Context, name string, seen map[string]bool) (ASTType, bool) {
	if seen[name] {
		return ASTType{}, false
	}
	seen[name] = true
	underlying, ok := c.TypeAliasFor(name)
	if !ok {
		return ASTType{}, false
	}
	if underlying.IsUnion() {
		return underlying, true
	}
	if underlying.Name == "" {
		return ASTType{}, false
	}
	target, ok := namedUnionAlias(c, underlying.Name, seen)
	if ok && (underlying.Indirection != 0 || underlying.OwnedMask != 0 || underlying.MutMask != 0 || underlying.NilMask != 0) {
		rejectUnionWrapper(target, underlying.p)
	}
	return target, ok
}
