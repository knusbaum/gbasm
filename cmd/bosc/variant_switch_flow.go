package main

// Variant arms share ordinary field-consumption facts. These small frames
// reconcile the inferred wrapper operation across both fallthrough and exits.
type variantSwitchMode uint8

const (
	variantNeutral variantSwitchMode = iota // no active owned payload fields
	variantInspect
	variantConsume
)

type variantSwitchFlow struct {
	path FlowPath
	mode variantSwitchMode
}

type variantSwitchArm struct {
	flow      *variantSwitchFlow
	variants  []*ValuesCase
	loopDepth int
}

func contextLoopDepth(c *Context) int {
	for c.parent != nil {
		c = c.parent
	}
	return len(c.contlabs)
}

func (arm *variantSwitchArm) state(c *Context, at AST) variantSwitchMode {
	p := arm.flow.path
	if p.Fields == "" && c.IsMoved(p.Root) || p.Fields != "" && c.OwnedFieldConsumed(p) {
		return variantConsume
	}
	mode := variantNeutral
	for _, vc := range arm.variants {
		total, consumed := 0, 0
		for _, field := range vc.Payload {
			if !field.Type.HasOwned() {
				continue
			}
			total++
			if !c.OwnedFieldConsumed(p.Append(field.Name)) {
				if _, partial := inconsistentOwnedField(c, p.Append(field.Name), field.Type); partial {
					CompileErrorF(at, "Switch case %s partially consumes %q; consume all owned payload fields or restore them", vc.Name, p.Key())
				}
			}
			if c.OwnedFieldConsumed(p.Append(field.Name)) {
				consumed++
			}
		}
		if consumed > 0 && consumed < total {
			CompileErrorF(at, "Switch case %s partially consumes %q; consume all owned payload fields or restore them", vc.Name, p.Key())
		}
		next := variantNeutral
		if total > 0 {
			next = variantInspect
			if consumed == total {
				next = variantConsume
			}
		}
		if mode != variantNeutral && next != variantNeutral && mode != next {
			CompileErrorF(at, "Switch cases for %q must either all consume their owned payloads or all preserve them", p.Key())
		}
		if next != variantNeutral {
			mode = next
		}
	}
	return mode
}

func (f *variantSwitchFlow) record(mode variantSwitchMode, at AST) {
	if mode != variantNeutral {
		if f.mode != variantNeutral && f.mode != mode {
			CompileErrorF(at, "Switch cases for %q must either all consume their owned payloads or all preserve them", f.path.Key())
		}
		f.mode = mode
	}
}

func (f *variantSwitchFlow) discharge(c *Context) {
	if f.path.Fields == "" {
		if !c.IsMoved(f.path.Root) {
			c.MoveConsume(f.path.Root)
		}
	} else if !c.OwnedFieldConsumed(f.path) {
		c.SetOwnedFieldConsumed(f.path, true)
	}
	// Payload slots belong to the discharged wrapper, not to the next loop
	// iteration or a sibling variant that happens to reuse a field name.
	prefix := c.ownedFieldPath(f.path).Key() + "."
	for ctx := c; ctx != nil; ctx = ctx.parent {
		for key := range ctx.checker.ownedFieldFacts {
			if len(key) >= len(prefix) && key[:len(prefix)] == prefix {
				delete(ctx.checker.ownedFieldFacts, key)
			}
		}
	}
}

// Called before return checks and before recording break/continue snapshots.
// A jump inside a loop nested in this arm does not leave the arm.
func settleVariantSwitchExits(c *Context, at AST, returning bool) {
	for i := len(c.variantArms) - 1; i >= 0; i-- {
		arm := c.variantArms[i]
		if !returning && contextLoopDepth(c) > arm.loopDepth {
			continue
		}
		mode := arm.state(c, at)
		arm.flow.record(mode, at)
		if mode == variantConsume || mode == variantNeutral {
			arm.flow.discharge(c)
		}
	}
}
