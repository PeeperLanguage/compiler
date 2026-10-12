package mir

import (
	"compiler/internal/ir/cfg"
	"compiler/internal/moduleid"
	"compiler/internal/semantics/symbols"
	"compiler/internal/source"
)

// CleanupPlan answers where source values are dropped; analysis.Module is the
// plan. MIR asks through this interface so it does not import the analysis,
// which leaves the analysis free to read MIR.
type CleanupPlan interface {
	DropsAfterSite(fn moduleid.FunctionID, site cfg.SiteID) []symbols.SymbolID
	DropsBeforeReturn(fn moduleid.FunctionID, id source.NodeID) []symbols.SymbolID
	DropsBeforeAssign(fn moduleid.FunctionID, id source.NodeID) bool
	DiscardedValue(fn moduleid.FunctionID, id source.NodeID) bool
	ProjectionBase(fn moduleid.FunctionID, id source.NodeID) bool
}

// ResolveDrops replaces every DropPoint lowering left with the drops the plan
// decides, or removes it. It runs after the checks that make the plan; a nil
// plan drops nothing.
func ResolveDrops(m *Module, plan CleanupPlan) {
	if m == nil {
		return
	}
	for _, fn := range m.Funcs {
		if fn == nil {
			continue
		}
		locals := make(map[symbols.SymbolID]Local, len(fn.Locals))
		for _, local := range fn.Locals {
			locals[local.Symbol] = local
		}
		for _, block := range fn.Blocks {
			if block == nil {
				continue
			}
			// A block is copied only from its first drop point on.
			var resolved []Instr
			for index, instr := range block.Instrs {
				point, isPoint := instr.(*DropPoint)
				if isPoint && resolved == nil {
					resolved = append(make([]Instr, 0, len(block.Instrs)), block.Instrs[:index]...)
				}
				switch {
				case isPoint:
					resolved = resolveDropPoint(resolved, fn, point, plan, locals)
				case resolved != nil:
					resolved = append(resolved, instr)
				}
			}
			if resolved != nil {
				block.Instrs = resolved
			}
		}
	}
}

func resolveDropPoint(out []Instr, fn *Function, point *DropPoint, plan CleanupPlan, locals map[symbols.SymbolID]Local) []Instr {
	drop := func(value ValueRef) []Instr {
		return append(out, &Drop{Value: value, Location: point.Location})
	}
	var dropped []symbols.SymbolID
	switch point.Kind {
	case DropAfterSite:
		if plan != nil {
			dropped = plan.DropsAfterSite(fn.Identity, point.Site)
		}
	case DropBeforeReturn:
		if plan != nil {
			dropped = plan.DropsBeforeReturn(fn.Identity, point.Node)
		}
	case DropBeforeAssign:
		if plan == nil || !plan.DropsBeforeAssign(fn.Identity, point.Node) {
			return out
		}
		if point.Place == nil {
			return drop(point.Value)
		}
		// The old value is loaded as lowering loads a place: the assignment
		// takes the statement's position when the place has none.
		location := point.Place.Location
		assigned := location
		if assigned == nil {
			assigned = point.Location
		}
		load := &Load{Place: point.Place, Type: point.Place.Type, Location: location}
		out = append(out, &Assign{Name: point.Temporary, Value: load, Location: assigned})
		return drop(&RefName{Name: point.Temporary, Type: point.Place.Type, Location: location})
	case DropDiscarded:
		if plan != nil && plan.DiscardedValue(fn.Identity, point.Node) {
			return drop(point.Value)
		}
		return keepCallBare(out, point)
	case DropProjectionBase:
		if plan != nil && plan.ProjectionBase(fn.Identity, point.Node) {
			return drop(point.Value)
		}
		return out
	}
	// A scope or return drop reads the variable where the drop happens.
	for _, id := range dropped {
		if local, ok := locals[id]; ok {
			out = drop(&RefName{Name: local.Name, Type: local.Type, Location: point.Location})
		}
	}
	return out
}

// keepCallBare undoes the temporary lowering gave a statement's call result
// when nothing drops it, so the call stands alone as a discarded call does.
func keepCallBare(out []Instr, point *DropPoint) []Instr {
	result, isName := point.Value.(*RefName)
	if !isName {
		return out
	}
	for index := len(out) - 1; index >= 0; index-- {
		assign, isAssign := out[index].(*Assign)
		if !isAssign || assign.Name != result.Name {
			continue
		}
		switch call := assign.Value.(type) {
		case *Call:
			call.Location = point.Location
			out[index] = call
		case *InterfaceCall:
			call.Location = point.Location
			out[index] = call
		}
		return out
	}
	return out
}
