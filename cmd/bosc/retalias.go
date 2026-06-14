package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/knusbaum/gbasm/cmd/bosc/flow"
)

// retalias.go — shared helpers for inferred return-parameter aliasing.
//
// The summary ENGINE (how a function's per-slot alias set is computed)
// lives in retalias_engine.go: it runs the real compile to a discard
// writer and reads provenance out of the one true flow tracker at each
// return. This file holds what surrounds the engine: the cycle guard,
// slot/index helpers, callee resolution, the summary-driven call-result
// provenance recording, the interface-coercion guard with its directed
// diagnostic, and small utilities. See DESIGN_return_alias_engine.md.

// aliasInProgressSet returns the cycle-guard set, lazily created on the
// root context.
func (c *Context) aliasInProgressSet() map[*FuncDecl]bool {
	root := c
	for root.parent != nil {
		root = root.parent
	}
	if root.aliasInProgress == nil {
		root.aliasInProgress = make(map[*FuncDecl]bool)
	}
	return root.aliasInProgress
}

// returnSlotCount returns the number of return slots for a function's
// declared return type. A multi-return signature carries its value types
// in Return.AnonFields with MultiReturn=true; a single non-void return is
// one slot; void is zero slots.
func returnSlotCount(ret ASTType) int {
	if ret.MultiReturn {
		return len(ret.AnonFields)
	}
	if ret.Same(voidASTType()) {
		return 0
	}
	return 1
}

// returnSlotType returns the declared type of return slot s.
func returnSlotType(ret ASTType, s int) ASTType {
	if ret.MultiReturn {
		if s >= 0 && s < len(ret.AnonFields) {
			return ret.AnonFields[s].Type
		}
		return ASTType{}
	}
	return ret
}

// paramIndexOf returns the parameter index of binding name in f, or -1 if
// name is not a parameter.
func paramIndexOf(f *FuncDecl, name string) int {
	for i, a := range f.Args {
		if a.Name == name {
			return i
		}
	}
	return -1
}

// slotIndexFromFieldName maps a multi-return StructLiteral's positional
// field name (_0, _1, ...) to its slot index, or -1.
func slotIndexFromFieldName(name string) int {
	if len(name) < 2 || name[0] != '_' {
		return -1
	}
	n := 0
	for _, ch := range name[1:] {
		if ch < '0' || ch > '9' {
			return -1
		}
		n = n*10 + int(ch-'0')
	}
	return n
}

// structReturnAliasFieldKey is the synthetic field-provenance key suffix
// used to record a struct-by-value call result's borrowed-argument origins
// onto its destination binding. The alias set is coarse (slot → param, no
// real field name), so we cannot reconstruct which struct field borrows;
// a single sentinel sub-key under the destination's "binding." prefix
// carries the merged origin. Both consumers — CheckStructFieldEscapeLocal
// (live per-site reject) and EscapingFieldOrigins (summary read) — are
// prefix-scans over "binding.", so the sentinel participates uniformly.
const structReturnAliasFieldKey = "__callret"

// recordStructReturnCallFieldFacts populates the destination struct
// binding's field provenance from a struct-by-value call result.
//
// A struct returned BY VALUE from a call (`var b B = mk(arg)`, `b = mk(arg)`)
// carries no single Origin — its borrow lives in one of its fields, which
// pointerExprForAST's single-Origin call-expansion cannot represent. This
// expands the callee's summary for return slot 0 onto the call's argument
// origins (via pointerExprForAST), unions them through the same join-origin
// mechanism the branch merge uses, and records the result under the
// destination's sentinel field key. The call-bound case then behaves
// identically to the direct field assignment: a local arg yields a local
// field origin (per-site reject), a borrowed arg yields a borrowed field
// origin (read back by the summary engine for the destination's own
// callers). This is Gap 2's struct-result wiring: call-result provenance
// populated from the callee's real summary.
func recordStructReturnCallFieldFacts(c *Context, destBinding string, destType ASTType, init AST) {
	call, ok := init.(*Funcall)
	if !ok {
		return
	}
	recordStructCallResultAtPath(c, destBinding, destType, call)
}

// argAliasProvenance reads one call argument's provenance for alias
// expansion. For most arguments this is the expression's own tracked
// origin (pointerExprForAST). A STRUCT-VALUED symbol argument carries its
// borrows in its FIELDS — its own origin is unknown — so its contribution
// is the union of its field origins (joined; the most-restrictive member
// governs, so a local field surfaces). Without this, `pass(b)` where
// b.buf borrows local storage contributed nothing and the local escaped
// through the call.
func argAliasProvenance(c *Context, arg AST) flow.PointerExpr {
	ap := pointerExprForAST(c, arg, "")
	if ap.KnownOrigin {
		return ap
	}
	if sym, ok := unwrapReturnExpr(arg).(*Symbol); ok && !c.IsGlobalBinding(sym.Name) {
		if t, exists := c.TypeForVar(sym.Name); exists && t.Indirection == 0 && !t.IsSlice() {
			// Struct values carry borrows in their fields; interface values
			// carry theirs in the fat pointer's "data" field fact (set at
			// the coercion site by emitInterfaceFatPtr). Both read back as
			// the union of the binding's field origins.
			_, isStruct := structDeclForType(c, t)
			if isStruct || c.IsInterfaceType(t) {
				var merged flow.PointerExpr
				for _, origin := range c.PointerFlow().FieldOrigins(flow.Binding(sym.Name)) {
					op := flow.PointerExpr{KnownOrigin: true, Origin: origin}
					if !merged.KnownOrigin {
						merged = op
					} else if merged.Origin != op.Origin {
						merged = c.PointerFlow().JoinOrigins(merged, op)
					}
				}
				if merged.KnownOrigin {
					return merged
				}
			}
		}
	}
	return ap
}

// recordMultiReturnSlotProvenance propagates a multi-return call's
// per-slot alias provenance onto a destructured binding. Without this,
// `var v byte[], var n i64 = mkslice(loc[:])` bound v with NO provenance —
// the callee's summary said slot 0 aliases its param, but the destructuring
// dropped the fact, so `return v` escaped a local uncaught.
//
// For a slice/pointer slot the merged contributing-argument origin is
// assigned to the binding directly; for a struct-valued slot it lands on
// the binding's __callret sentinel (same shape as the single-return
// struct-call path).
func recordMultiReturnSlotProvenance(c *Context, dest string, destType ASTType, init AST, slot int) {
	call, ok := init.(*Funcall)
	if !ok {
		return
	}
	// The destination is being overwritten by this slot's fresh value:
	// whatever provenance it had is stale and must be REPLACED, not left
	// in place. (Leaving it produced a false reject: `v, n = mkglob()`
	// after `var v,n = mkslice(loc[:])` kept the old local fact.) The
	// new fact is the merged contributing-argument provenance — or
	// Unknown if the callee/summary yields nothing.
	clear := func() {
		rt := c.ResolveUnderlying(destType)
		if rt.Indirection > 0 || rt.IsSlice() {
			c.PointerFlow().AssignPointer(flow.Binding(dest), c.PointerFlow().UnknownPointer())
		} else if _, isStruct := structDeclForType(c, rt); isStruct {
			key := fmt.Sprintf("%s.%s", dest, structReturnAliasFieldKey)
			c.PointerFlow().SetPathPointer(key, c.PointerFlow().UnknownPointer())
		}
	}
	callee, args := resolveCalleeForAlias(c, call)
	if callee == nil {
		clear()
		return
	}
	aliases := aliasSet(c, callee)
	if slot < 0 || slot >= len(aliases) || len(aliases[slot]) == 0 {
		clear()
		return
	}
	var merged flow.PointerExpr
	for _, p := range aliases[slot] {
		if p < 0 || p >= len(args) {
			continue
		}
		ap := argAliasProvenance(c, args[p])
		if !ap.KnownOrigin {
			continue
		}
		if !merged.KnownOrigin {
			merged = ap
			continue
		}
		if merged.Origin == ap.Origin {
			continue
		}
		merged = c.PointerFlow().JoinOrigins(merged, ap)
	}
	if !merged.KnownOrigin {
		clear()
		return
	}
	rt := c.ResolveUnderlying(destType)
	if rt.Indirection > 0 || rt.IsSlice() {
		c.PointerFlow().AssignPointer(flow.Binding(dest), merged)
		return
	}
	if _, isStruct := structDeclForType(c, rt); isStruct {
		key := fmt.Sprintf("%s.%s", dest, structReturnAliasFieldKey)
		c.PointerFlow().SetPathPointer(key, merged)
	}
}

// recordStructCallResultAtPath records a struct-by-value call result's
// borrowed-argument provenance under an arbitrary destination path (a
// binding name, or a nested field path like "o.inner"). The merged origin
// lands at "<destPath>.__callret".
func recordStructCallResultAtPath(c *Context, destPath string, destType ASTType, call *Funcall) {
	rt := c.ResolveUnderlying(destType)
	if rt.Indirection > 0 || rt.IsSlice() {
		return
	}
	if _, ok := structDeclForType(c, rt); !ok {
		return
	}
	callee, args := resolveCalleeForAlias(c, call)
	if callee == nil {
		return
	}
	aliases := aliasSet(c, callee)
	// A struct-by-value return occupies slot 0 (multi-return does not return
	// a single struct binding).
	if len(aliases) == 0 || len(aliases[0]) == 0 {
		return
	}
	key := fmt.Sprintf("%s.%s", destPath, structReturnAliasFieldKey)
	// Clear any stale sentinel fact before recording — the destination is
	// being (re)initialized from this call.
	c.PointerFlow().SetPathPointer(key, c.PointerFlow().UnknownPointer())
	var merged flow.PointerExpr
	for _, p := range aliases[0] {
		if p < 0 || p >= len(args) {
			continue
		}
		ap := argAliasProvenance(c, args[p])
		if !ap.KnownOrigin {
			continue
		}
		if !merged.KnownOrigin {
			merged = ap
			continue
		}
		if merged.Origin == ap.Origin {
			continue
		}
		// Union two distinct argument origins via a join origin, so a later
		// EscapingFieldOrigins expands every contributing param and a local
		// in either position still surfaces. The join is itself escape-
		// restricted, so the most-restrictive member governs the live reject.
		merged = c.PointerFlow().JoinOrigins(merged, ap)
	}
	if merged.KnownOrigin {
		c.PointerFlow().SetPathPointer(key, merged)
	}
}

// resolveCalleeForAlias resolves a Funcall to its callee FuncDecl and the
// positional argument list (desugaring a concrete method call so the
// receiver occupies argument index 0, matching the param-index convention).
// Returns (nil, nil) for casts, builtins, indirect calls, and interface
// dispatch — none of which carry a recordable alias set in v1.
func resolveCalleeForAlias(ic *Context, call *Funcall) (*FuncDecl, []AST) {
	pkg, fname := call.PkgAndName()
	// Builtins and casts carry no alias set.
	if pkg == "" && (fname == "alloc" || fname == "new" || fname == "free" || fname == "len") {
		return nil, nil
	}
	// A type-cast Funcall (T(expr)) carries no alias set. The call form
	// wins when a function of the same name also exists, so only treat it
	// as a cast when no matching function is declared.
	if _, ok := ic.TypeByName(call.QualifiedName()); ok {
		if _, _, hasFn := ic.FuncDeclForCall(pkg, fname); !hasFn {
			return nil, nil
		}
	}
	// Expression-receiver method call: (expr).method(args).
	if recv := call.ReceiverExpr(); recv != nil {
		rt := recv.ASTType(ic)
		if ic.IsInterfaceType(rt) {
			return nil, nil
		}
		if method, ok := ic.MethodForType(rt.Name, fname); ok {
			args := make([]AST, 0, 1+len(call.Args))
			args = append(args, recv)
			args = append(args, call.Args...)
			return method, args
		}
		return nil, nil
	}
	decl, _, ok := ic.FuncDeclForCall(pkg, fname)
	if ok {
		return decl, call.Args
	}
	// v.method(args) where v is a variable with a concrete method.
	if pkg != "" {
		if vt, vok := ic.TypeForVar(pkg); vok {
			if ic.IsInterfaceType(vt) {
				return nil, nil
			}
			if method, mok := ic.MethodForType(vt.Name, fname); mok {
				var recv AST
				if vt.Indirection > 0 || len(method.Args) == 0 || method.Args[0].Type.Indirection == 0 {
					recv = &Symbol{Name: pkg}
				} else {
					recv = &Address{Var: pkg}
				}
				args := make([]AST, 0, 1+len(call.Args))
				args = append(args, recv)
				args = append(args, call.Args...)
				return method, args
			}
		}
	}
	return nil, nil
}

// methodAliasesSatisfy reports whether concrete method m conforms to an
// interface method's declared borrow contract: for every return slot, m's
// *inferred* ReturnAliases[slot] must be ⊆ the interface's *declared* set
// (declared nil, or a slot absent from it, means "borrows nothing"). This is
// the static half of the interface borrow-contract gate — the ⊆ ceiling rule
// folded into TypeSatisfiesInterfaceAs; the runtime half is the per-slot mask
// check in _iface.assert_to. Demand-drives alias_set on m (memoized into
// m.ReturnAliases). Subsumes the old whole-type coarse guard: a type with a
// borrowing method the interface does not *require* is unconstrained here (it
// is reachable only via assertion, which the runtime gate covers).
func methodAliasesSatisfy(c *Context, m *FuncDecl, declared [][]int) bool {
	for slot, implSet := range aliasSet(c, m) {
		for _, p := range implSet {
			if !slotDeclaresParam(declared, slot, p) {
				return false
			}
		}
	}
	return true
}

// slotDeclaresParam reports whether declared[slot] lists parameter index p.
func slotDeclaresParam(declared [][]int, slot, p int) bool {
	if slot < 0 || slot >= len(declared) {
		return false
	}
	for _, d := range declared[slot] {
		if d == p {
			return true
		}
	}
	return false
}

// methodSignatureRendering renders a short method signature for the
// diagnostic, e.g. `bytes(b *Builder) byte[]`.
func methodSignatureRendering(c *Context, typeName string, m *FuncDecl) string {
	var b strings.Builder
	b.WriteString(bareMethodName(m.Name))
	b.WriteByte('(')
	for i, a := range m.Args {
		if i > 0 {
			b.WriteString(", ")
		}
		if a.Name != "" {
			b.WriteString(a.Name)
			b.WriteByte(' ')
		}
		b.WriteString(a.Type.String())
	}
	b.WriteByte(')')
	if !m.Return.Same(voidASTType()) {
		b.WriteByte(' ')
		b.WriteString(m.Return.String())
	}
	return b.String()
}

// bareMethodName strips a "Type." qualifier from a method name.
func bareMethodName(name string) string {
	if dot := strings.LastIndex(name, "."); dot >= 0 {
		return name[dot+1:]
	}
	return name
}

// borrowDescription renders a short phrase describing what a method
// borrows, derived from its inferred ReturnAliases. Index 0 is the
// receiver; index k>0 is the (k)-th declared parameter.
func borrowDescription(m *FuncDecl) string {
	borrowsReceiver := false
	var params []string
	for _, slot := range m.ReturnAliases {
		for _, p := range slot {
			if p == 0 {
				borrowsReceiver = true
			} else if p > 0 && p < len(m.Args) {
				params = append(params, m.Args[p].Name)
			}
		}
	}
	switch {
	case borrowsReceiver && len(params) > 0:
		return "returns a borrow of its receiver and parameter(s) " + strings.Join(params, ", ")
	case borrowsReceiver:
		return "returns a borrow of its receiver"
	case len(params) > 0:
		return "returns a borrow of parameter(s) " + strings.Join(params, ", ")
	default:
		return "returns a borrow"
	}
}

// methodDefinitionSite renders the method's definition site for the
// diagnostic. A locally-defined method's FuncDecl carries a usable
// position; an imported method may carry a position threaded from the
// .bo's SrcFile/SrcLine, otherwise it degrades to "defined in package X".
func methodDefinitionSite(concreteTypeName string, m *FuncDecl) string {
	p := m.Pos()
	if p.fname != "" && p.lineoff != 0 {
		return fmt.Sprintf("defined at %s:%d", p.fname, p.lineoff)
	}
	if dot := strings.Index(concreteTypeName, "."); dot >= 0 {
		return fmt.Sprintf("defined in package %s", concreteTypeName[:dot])
	}
	return ""
}

// sortDedup sorts and removes duplicate parameter indices.
func sortDedup(in []int) []int {
	if len(in) == 0 {
		return nil
	}
	sort.Ints(in)
	out := in[:1]
	for _, v := range in[1:] {
		if v != out[len(out)-1] {
			out = append(out, v)
		}
	}
	return out
}
