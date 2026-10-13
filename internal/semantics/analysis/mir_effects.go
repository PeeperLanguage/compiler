package analysis

import (
	"compiler/internal/ir/mir"
	"compiler/internal/ir/thir"
	"compiler/internal/semantics/place"
	"compiler/internal/semantics/symbols"
)

// effectsFromMIR reads, from MIR, the effects definite initialization needs:
// defines, writes, uses and borrows of variables, keyed by CFG site in
// instruction order. MIR states each of them as an ordinary instruction, so
// no syntax is read: a variable is read by `move x` or as a place root,
// borrowed by `&place`, and set by an assignment or a store.
func effectsFromMIR(module *mir.Module, source *thir.Module, index *symbols.Index) effectStreams {
	if module == nil {
		return nil
	}
	table := symbolsByID(source, index)
	streams := make(effectStreams, len(module.Funcs))
	for _, fn := range module.Funcs {
		if fn == nil || fn.Blocks == nil {
			continue
		}
		streams[fn.Identity] = functionEffectsFromMIR(fn, table)
	}
	return streams
}

// symbolsByID collects the symbols a function body can name: everything
// declared in a block scope, which includes the compiler's own locals, and
// every parameter. Loop cursors the type checker makes up are in no scope;
// the effect stream has never tracked them.
func symbolsByID(source *thir.Module, index *symbols.Index) map[symbols.SymbolID]*symbols.Symbol {
	table := make(map[symbols.SymbolID]*symbols.Symbol)
	index.ForEachScope(func(scope *symbols.Scope) {
		for _, symbol := range scope.Symbols() {
			table[symbol.ID] = symbol
		}
	})
	if source != nil {
		for _, function := range source.Functions {
			for _, parameter := range function.Params {
				if parameter.Symbol != nil {
					table[parameter.Symbol.ID] = parameter.Symbol
				}
			}
		}
	}
	return table
}

func functionEffectsFromMIR(fn *mir.Function, table map[symbols.SymbolID]*symbols.Symbol) effectSiteOps {
	ops := make(effectSiteOps)
	variables := make(map[string]*symbols.Symbol, len(fn.Locals))
	for _, local := range fn.Locals {
		if symbol := table[local.Symbol]; symbol != nil {
			variables[local.Name] = symbol
		}
	}
	// ZeroValue has one producer, a binding without a value, so a variable
	// set from it is declared, not initialized.
	zero := make(map[string]bool)
	defined := make(map[string]bool)
	for _, block := range fn.Blocks {
		if block != nil && block.ID == fn.EntryID && len(block.Sites) > 0 {
			entry := block.Sites[0].ID
			for _, parameter := range fn.Params {
				if symbol := variables[parameter.Name]; symbol != nil {
					ops[entry] = append(ops[entry], effectDefine{Symbol: symbol, IsInitialized: true, IsOnEntry: true})
					defined[parameter.Name] = true
				}
			}
		}
	}
	for _, block := range fn.Blocks {
		if block == nil || len(block.Sites) == 0 {
			continue
		}
		next := 0
		for index, instruction := range block.Instrs {
			for next+1 < len(block.Sites) && block.Sites[next+1].Start <= index {
				next++
			}
			site := block.Sites[next].ID
			emit := func(op effectOp) { ops[site] = append(ops[site], op) }
			root := func(p *mir.Place) (effectPlace, bool) {
				ref, isName := p.Root.(*mir.RefName)
				if !isName || variables[ref.Name] == nil {
					return effectPlace{}, false
				}
				return effectPlace{Root: variables[ref.Name], Projections: make([]place.OriginProjection, len(p.Projections))}, true
			}
			switch node := instruction.(type) {
			case *mir.Assign:
				switch value := node.Value.(type) {
				case *mir.Move:
					if ref, isName := value.Src.(*mir.RefName); isName && variables[ref.Name] != nil {
						emit(effectUse{Place: effectPlace{Root: variables[ref.Name]}, Location: ref.Location})
					}
				case *mir.Load:
					if at, ok := root(value.Place); ok {
						emit(effectUse{Place: at, Location: value.Place.Location})
					}
				case *mir.AddrOf:
					if at, ok := root(value.Place); ok {
						emit(effectBorrow{Place: at, Location: value.Location})
					}
				case *mir.SliceView:
					if at, ok := root(value.Source); ok {
						emit(effectBorrow{Place: at, Location: value.Location})
					}
				case *mir.ZeroValue:
					zero[node.Name] = true
				}
				symbol := variables[node.Name]
				if symbol == nil {
					continue
				}
				if defined[node.Name] {
					emit(effectWrite{Place: effectPlace{Root: symbol}, Location: node.Location})
					continue
				}
				defined[node.Name] = true
				declared := false
				if move, isMove := node.Value.(*mir.Move); isMove {
					from, isName := move.Src.(*mir.RefName)
					declared = isName && zero[from.Name]
				}
				emit(effectDefine{Symbol: symbol, IsInitialized: !declared})
			case *mir.Store:
				if at, ok := root(node.Place); ok {
					emit(effectWrite{Place: at, Location: node.Place.Location})
				}
			}
		}
	}
	return ops
}
