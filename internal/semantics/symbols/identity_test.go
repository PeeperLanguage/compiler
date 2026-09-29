package symbols

import (
	"testing"

	"compiler/internal/moduleid"
	"compiler/internal/source"
)

func TestSymbolIDDomainsAreDeterministicAndDistinct(t *testing.T) {
	module := moduleid.ID{Origin: "local", ImportPath: "app/main"}
	node := source.FunctionNodeID(moduleid.FunctionID("function"), 7)
	ids := []SymbolID{
		SourceSymbolID(node),
		ModuleSymbolID(module, SymbolVar, "value", 0),
		GeneratedSymbolID(node, GeneratedForCursor),
		CompilerSymbolID(SymbolVar, "value"),
		ProjectedSymbolID(SymbolVar, "value"),
	}
	seen := make(map[SymbolID]struct{}, len(ids))
	for _, id := range ids {
		if !id.IsValid() || id.String() == "" || id.String() == "invalid" {
			t.Fatalf("invalid symbol identity %v", id)
		}
		if _, duplicate := seen[id]; duplicate {
			t.Fatalf("duplicate symbol identity %v", id)
		}
		seen[id] = struct{}{}
	}
	sourceID := SourceSymbolID(node)
	moduleID := ModuleSymbolID(module, SymbolVar, "value", 0)
	if sourceID != SourceSymbolID(node) || moduleID != ModuleSymbolID(module, SymbolVar, "value", 0) {
		t.Fatal("equivalent symbol inputs produced different identities")
	}
	if ModuleSymbolID(module, SymbolVar, "value", 0) == ModuleSymbolID(module, SymbolVar, "value", 1) {
		t.Fatal("recovery occurrences share symbol identity")
	}
}

func TestGeneratedSymbolIDRejectsUnknownRoleAndUnownedNode(t *testing.T) {
	if id := GeneratedSymbolID(source.ParsedNodeID(1), GeneratedForCursor); id.IsValid() {
		t.Fatalf("parsed owner produced generated symbol identity %v", id)
	}
	owner := source.FunctionNodeID(moduleid.FunctionID("function"), 1)
	if id := GeneratedSymbolID(owner, GeneratedSymbolRole(255)); id.IsValid() {
		t.Fatalf("unknown generated role produced symbol identity %v", id)
	}
}

func TestNewRejectsInvalidSymbolID(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("New accepted invalid SymbolID")
		}
	}()
	New(SymbolID{}, "value", SymbolVar, nil, nil)
}
