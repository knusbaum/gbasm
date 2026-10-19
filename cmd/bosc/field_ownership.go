package main

import (
	"strings"

	"github.com/knusbaum/gbasm/cmd/bosc/flow"
)

// ownedFieldPaths resolves aliases to the slots they can access. A joined
// pointer may name several allocations; reads must check every possibility.
// Field-pointer facts also cover references stored inside another aggregate.
func (c *Context) ownedFieldPaths(path FlowPath) []FlowPath {
	return c.resolveFieldPaths(path, false)
}

func (c *Context) resolveFieldPaths(path FlowPath, followLeaf bool) []FlowPath {
	var resolve func(FlowPath, map[string]bool) []FlowPath
	resolve = func(path FlowPath, seen map[string]bool) []FlowPath {
		if seen[path.Key()] {
			return []FlowPath{path}
		}
		nextSeen := make(map[string]bool, len(seen)+1)
		for key, value := range seen {
			nextSeen[key] = value
		}
		nextSeen[path.Key()] = true
		t, ok := c.DeclaredTypeForVar(path.Root)
		if !ok {
			return []FlowPath{path}
		}
		follow := func(ptr flow.PointerExpr, rest string) []FlowPath {
			if !ptr.KnownOrigin {
				return nil
			}
			var out []FlowPath
			for _, member := range c.PointerFlow().JoinMembers(ptr.Origin) {
				origin := FlowPathFromKey(string(c.PointerFlow().AllocationOrigin(member)))
				if _, known := c.DeclaredTypeForVar(origin.Root); !known {
					continue
				}
				if rest != "" {
					origin = origin.Append(rest)
				}
				if origin == path {
					continue
				}
				out = append(out, resolve(origin, nextSeen)...)
			}
			return out
		}
		if t.Indirection > 0 || c.IsInterfaceType(t) {
			if out := follow(c.PointerFlow().Pointer(flow.Binding(path.Root)), path.Fields); len(out) > 0 {
				return out
			}
		}
		parts := strings.Split(path.Fields, ".")
		limit := len(parts)
		if !followLeaf {
			limit--
		}
		for i := 1; i <= limit; i++ {
			prefix := strings.Join(parts[:i], ".")
			ft, ok := walkLeafType(c, t, prefix)
			if !ok || ft.Indirection == 0 {
				continue
			}
			key := VarFlowPath(path.Root).Append(prefix).Key()
			if out := follow(c.PointerFlow().GetPathPointer(key), strings.Join(parts[i:], ".")); len(out) > 0 {
				return out
			}
		}
		return []FlowPath{path}
	}
	return resolve(path, nil)
}

// Writes require a single identified slot; callers reject ambiguous ownership
// writes. Reads use ownedFieldPaths and check all possible slots instead.
func (c *Context) ownedFieldPath(path FlowPath) FlowPath {
	return c.ownedFieldPaths(path)[0]
}

// A mutable borrow permits temporary consumption, not returning an
// uninitialized non-nullable owned field to the caller.
func checkBorrowedFieldsRestored(c *Context, at AST) {
	for ctx := c; ctx != nil; ctx = ctx.parent {
		for name := range ctx.borrowedAggregateParams {
			t, _ := ctx.DeclaredTypeForVar(name)
			if field, bad := inconsistentOwnedField(c, VarFlowPath(name), t); bad {
				CompileErrorF(at, "Cannot return while borrowed owned field %q is moved out; re-initialize it first", name+"."+field)
			}
		}
	}
}

// Consumed slots in a freed struct no longer participate in loop or branch
// consistency. Pointer fields may have transferred their pointees elsewhere;
// retain facts about those pointees and retire only slots in this allocation.
func retireFreedFieldFacts(c *Context, path FlowPath, t ASTType) {
	def, ok := structDeclForType(c, t)
	if !ok {
		return
	}
	for _, field := range def.Fields {
		fieldPath := path.Append(field.Name)
		c.SetOwnedFieldConsumed(fieldPath, false)
		if field.Type.Indirection == 0 && !field.Type.IsSliceOrArray() {
			retireFreedFieldFacts(c, fieldPath, field.Type)
		}
	}
}

// A callee cannot see this function's consumed-slot facts. Resolve a whole
// aggregate argument (including a field or interface alias) before allowing it
// across that boundary.
func checkAggregateExprMayEscape(c *Context, e AST, verb string) {
	source := e
	if address, ok := e.(*Address); ok {
		if name, named := address.NamedTarget(); named {
			source = &Symbol{Name: name, p: address.Pos()}
		} else {
			source = address.Lit
		}
	}
	path, ok := FlowPathForExpr(source)
	if !ok {
		return
	}
	for _, resolved := range c.resolveFieldPaths(path, true) {
		t, ok := c.DeclaredTypeForVar(resolved.Root)
		if !ok {
			continue
		}
		if resolved.Fields != "" {
			t, ok = walkLeafType(c, t, resolved.Fields)
		}
		if !ok {
			continue
		}
		if field, bad := inconsistentOwnedField(c, resolved, t); bad {
			CompileErrorF(e, "Cannot %s %q while owned field %q is moved out; only dispose/free are allowed on a partially-consumed aggregate — consume or re-initialize %q first", verb, path.Key(), path.Append(field).Key(), path.Append(field).Key())
		}
	}
}

// Copying an entire struct would copy invalid slots too. Local pointer aliases
// remain legal because they refer to the slots whose state we already track.
func checkAggregateValueCopy(c *Context, e AST) {
	if _, tracked := FlowPathForExpr(e); !tracked {
		return
	}
	t := c.ResolveUnderlying(e.ASTType(c))
	if t.Indirection == 0 {
		if _, ok := structDeclForType(c, t); ok {
			checkAggregateExprMayEscape(c, e, "copy")
		}
	}
}
