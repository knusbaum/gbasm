package main

import (
	"fmt"

	"github.com/knusbaum/gbasm/cmd/bosc/flow"
)

func borrowCallSite(call *Funcall, destination string) string {
	return fmt.Sprintf("%s:%d:%d:%s", call.p.fname, call.p.lineoff, call.p.linecharoff, destination)
}

// An interface value carries the mutation capabilities of its methods even
// when passed by value; its data pointer can grant mutable access to X.
func paramGrantsMutation(c *Context, t ASTType) bool {
	if t.IsUnion() {
		for _, a := range t.Union {
			if paramGrantsMutation(c, a) {
				return true
			}
		}
		return false
	}

	if t.HasOwned() {
		return false
	}
	t = c.ResolveUnderlying(t)
	if t.Indirection > 0 {
		return t.MutMask != 0
	}
	if c.IsInterfaceType(t) {
		decl, ok := c.InterfaceForName(t.Name)
		if ok {
			for _, method := range decl.Methods {
				if len(method.Params) > 0 && !method.Params[0].Type.HasOwned() && method.Params[0].Type.MutMask != 0 {
					return true
				}
			}
		}
	}
	return false
}

func invalidateCallBorrows(c *Context, args []AST, params []Binding) {
	var mutable []flow.PointerExpr
	for i, arg := range args {
		if i < len(params) && paramGrantsMutation(c, params[i].Type) {
			mutable = append(mutable, mutationAuthority(c, arg))
		}
	}
	c.PointerFlow().InvalidateBorrows(mutable)
}

func returnedBorrow(c *Context, call *Funcall, destination string, ptr flow.PointerExpr, aliases []int, args []AST) flow.PointerExpr {
	var dependencies []flow.PointerExpr
	for _, i := range aliases {
		if i >= 0 && i < len(args) {
			ptr := argAliasProvenance(c, args[i])
			dependencies = append(dependencies, ptr)
			dependencies = append(dependencies, borrowStorageDependencies(c, args[i], ptr)...)
		}
	}
	return c.PointerFlow().BorrowView(ptr, borrowCallSite(call, destination), dependencies...)
}

// The address of a named global object is precise even though ordinary global
// pointer VALUES remain opaque. In particular stdout is not an alias of every
// local buffer just because both can be passed to a writer interface.
func mutationAuthority(c *Context, arg AST) flow.PointerExpr {
	ptr := argAliasProvenance(c, arg)
	if ptr.KnownOrigin {
		return ptr
	}
	address, ok := arg.(*Address)
	if !ok {
		return ptr
	}
	if address.Var != "" && c.IsGlobalBinding(address.Var) {
		if t, ok := c.TypeForVar(address.Var); ok && t.Indirection == 0 {
			return c.PointerFlow().NewObject(flow.Binding("__global:" + c.Pkgname() + ":" + address.Var))
		}
	}
	if address.Lit != nil {
		resolved := ResolveSelector(c, address.Lit)
		if resolved.Kind == ResolvedRuntimeValue && resolved.Pkg != "" && resolved.Type.Indirection == 0 {
			return c.PointerFlow().NewObject(flow.Binding("__global:" + resolved.Pkg + ":" + resolved.Name))
		}
	}
	return ptr
}

func argumentBorrow(c *Context, arg AST, param ASTType) flow.PointerExpr {
	if param.IsUnion() && unionHasBorrowedPayload(c, param) {
		return argAliasProvenance(c, arg)
	}
	if param.HasOwned() {
		return flow.PointerExpr{}
	}
	t := c.ResolveUnderlying(arg.ASTType(c))
	_, aggregate := structDeclForType(c, t)
	if t.Indirection == 0 && !t.IsSlice() && !c.IsInterfaceType(t) && !aggregate {
		return flow.PointerExpr{}
	}
	return argAliasProvenance(c, arg)
}

func checkCallBorrows(c *Context, args []AST, borrows []flow.PointerExpr) {
	for i, borrow := range borrows {
		if !c.PointerFlow().IsBorrow(borrow) {
			continue
		}
		if ok, reason := c.PointerFlow().CheckDerefValidity(borrow); !ok {
			CompileErrorF(args[i], "%s", reason)
		}
	}
}

func copiedBorrow(c *Context, destination ASTType, value AST, site string, ptr flow.PointerExpr) flow.PointerExpr {
	if destination.HasOwned() || c.PointerFlow().IsBorrow(ptr) {
		return ptr
	}
	t := c.ResolveUnderlying(destination)
	source := value
	for {
		if slice, ok := source.(*SliceOp); ok {
			source = slice.Val
			continue
		}
		if nonNull, ok := source.(*NonNullAssert); ok {
			source = nonNull.Val
			continue
		}
		break
	}
	path, field := ProvenancePathForExpr(source)
	field = field && path.Fields != ""
	if !t.IsSlice() && !(field && (t.Indirection > 0 || c.IsInterfaceType(t))) {
		return ptr
	}
	var dependencies []flow.PointerExpr
	if field && !c.IsGlobalBinding(path.Root) {
		if root, ok := c.TypeForVar(path.Root); ok {
			owner := c.PointerFlow().Pointer(flow.Binding(path.Root))
			if !owner.KnownOrigin && root.Indirection == 0 {
				kind := c.PointerFlow().OriginKindOf(flow.Origin(path.Root))
				if kind != flow.OriginUnknown {
					owner = flow.PointerExpr{Origin: flow.Origin(path.Root), KnownOrigin: true}
				} else if root.HasOwned() {
					owner = c.PointerFlow().NewLocalOrigin(flow.Binding(path.Root))
				}
			}
			if owner.KnownOrigin {
				dependencies = append(dependencies, owner)
			}
		}
	}
	return c.PointerFlow().BorrowView(ptr, site, dependencies...)
}

// Inferred from(X) contracts describe the object, not a specific field. Capture
// its currently known owned backing allocations too, so a local free/replacement
// remains visible without requiring a mutable call boundary.
func borrowStorageDependencies(c *Context, arg AST, source flow.PointerExpr) []flow.PointerExpr {
	if !source.KnownOrigin {
		return nil
	}
	t := c.ResolveUnderlying(arg.ASTType(c))
	def, ok := structDeclForType(c, t)
	if !ok {
		return nil
	}
	root := string(c.PointerFlow().AllocationOrigin(source.Origin))
	var out []flow.PointerExpr
	for _, field := range def.Fields {
		ft := c.ResolveUnderlying(field.Type)
		if !field.Type.HasOwned() || (ft.Indirection == 0 && !ft.IsSlice()) {
			continue
		}
		key := root + "." + field.Name
		ptr := c.PointerFlow().GetPathPointer(key)
		if !ptr.KnownOrigin {
			ptr = c.PointerFlow().NewOwnedOrigin(flow.Binding(key))
			c.PointerFlow().SetPathPointer(key, ptr)
		}
		out = append(out, ptr)
	}
	return out
}
