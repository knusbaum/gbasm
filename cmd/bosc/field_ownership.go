package main

import "github.com/knusbaum/gbasm/cmd/bosc/flow"

// Aliases of the same struct share field consumption facts. Retain the
// binding-based representation when the origin isn't a visible binding.
func (c *Context) ownedFieldPath(path FlowPath) FlowPath {
	t, ok := c.DeclaredTypeForVar(path.Root)
	if !ok || t.Indirection == 0 {
		return path
	}
	ptr := c.PointerFlow().Pointer(flow.Binding(path.Root))
	if ptr.KnownOrigin {
		origin := FlowPathFromKey(string(ptr.Origin))
		if _, ok := c.DeclaredTypeForVar(origin.Root); ok {
			if path.Fields != "" {
				origin = origin.Append(path.Fields)
			}
			path = origin
		}
	}
	return path
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

// Borrowed aggregate parameters keep consumption facts across opaque writes.
// A call cannot be assumed to have restored a field it couldn't safely read.
func (c *Context) borrowedAggregateRoot(name string) bool {
	for ctx := c; ctx != nil; ctx = ctx.parent {
		if ctx.borrowedAggregateParams[name] {
			return true
		}
	}
	return false
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
