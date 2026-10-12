package mir

import (
	"strings"
	"testing"

	"compiler/internal/ir"
	"compiler/internal/ir/cfg"
	"compiler/internal/moduleid"
	"compiler/internal/semantics/symbols"
	"compiler/internal/source"
)

// plannedDrops is a cleanup plan answering yes for the listed keys.
type plannedDrops struct {
	afterSite  []symbols.SymbolID
	assign     bool
	discarded  bool
	projection bool
}

func (p plannedDrops) DropsAfterSite(moduleid.FunctionID, cfg.SiteID) []symbols.SymbolID {
	return p.afterSite
}
func (p plannedDrops) DropsBeforeReturn(moduleid.FunctionID, source.NodeID) []symbols.SymbolID {
	return p.afterSite
}
func (p plannedDrops) DropsBeforeAssign(moduleid.FunctionID, source.NodeID) bool { return p.assign }
func (p plannedDrops) DiscardedValue(moduleid.FunctionID, source.NodeID) bool    { return p.discarded }
func (p plannedDrops) ProjectionBase(moduleid.FunctionID, source.NodeID) bool    { return p.projection }

func TestResolveDropsFollowsThePlan(t *testing.T) {
	types := ir.NewTypeTable()
	i32 := types.Intern(ir.Type{Kind: ir.TypeInteger, IsSigned: true, Bits: 32})
	fnType := types.Intern(ir.Type{Kind: ir.TypeFunction, Return: i32})
	item := symbols.ProjectedSymbolID(symbols.SymbolVar, "item")
	at := &source.Location{Start: &source.Position{Line: 4, Column: 1}}
	place := &Place{Root: &RefName{Name: "pair$2", Type: i32}, Type: i32}
	block := func() []Instr {
		return []Instr{
			&DropPoint{Kind: DropAfterSite, Location: at},
			&DropPoint{Kind: DropBeforeReturn, Location: at},
			&DropPoint{Kind: DropBeforeAssign, Value: &RefName{Name: "item$1", Type: i32}, Location: at},
			&DropPoint{Kind: DropBeforeAssign, Place: place, Temporary: "t9", Location: at},
			&Assign{Name: "t1", Value: &Call{Callee: &RefName{Name: "make", Type: fnType}, Type: i32}},
			&DropPoint{Kind: DropDiscarded, Value: &RefName{Name: "t1", Type: i32}, Location: at},
			&DropPoint{Kind: DropProjectionBase, Value: &RefName{Name: "t2", Type: i32}, Location: at},
		}
	}
	resolve := func(plan CleanupPlan) string {
		fn := &Function{Locals: []Local{{Name: "item$1", Symbol: item, Type: i32}}, Blocks: []*Block{{Instrs: block()}}}
		ResolveDrops(&Module{Funcs: []*Function{fn}}, plan)
		texts := make([]string, 0)
		for _, instr := range fn.Blocks[0].Instrs {
			texts = append(texts, instr.Text(types))
		}
		return strings.Join(texts, "; ")
	}

	if got, want := resolve(plannedDrops{}), "call make()"; got != want {
		t.Fatalf("nothing planned: got %q, want %q", got, want)
	}
	if got, want := resolve(nil), "call make()"; got != want {
		t.Fatalf("no plan: got %q, want %q", got, want)
	}
	all := plannedDrops{afterSite: []symbols.SymbolID{item}, assign: true, discarded: true, projection: true}
	want := "drop item$1; drop item$1; drop item$1; t9 = load pair$2; drop t9; t1 = call make(); drop t1; drop t2"
	if got := resolve(all); got != want {
		t.Fatalf("everything planned:\n got  %q\n want %q", got, want)
	}
}

func TestValidateRejectsUnresolvedDropPoint(t *testing.T) {
	m := wellFormed()
	m.Funcs[0].Blocks[1].Instrs = []Instr{&DropPoint{Kind: DropAfterSite}}
	err := m.Validate()
	if err == nil || !strings.Contains(err.Error(), "drop point ResolveDrops never resolved") {
		t.Fatalf("Validate() = %v, want unresolved drop point", err)
	}
}
