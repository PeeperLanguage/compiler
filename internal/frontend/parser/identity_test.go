package parser

import (
	"slices"
	"testing"

	"compiler/internal/frontend/ast"
	"compiler/internal/moduleid"
	"compiler/internal/source"
)

func publishedFunction(t *testing.T, text, name string) *ast.FnDecl {
	t.Helper()
	module, diagnostics := parseTestModule(text)
	if diagnostics.HasErrors() {
		t.Fatalf("parse diagnostics:\n%s", diagnostics.EmitAllToString())
	}
	ast.PublishFunctionIdentities(moduleid.ID{Origin: "local", ImportPath: "app/main"}, module)
	for _, statement := range module.Stmts {
		function, ok := statement.(*ast.FnDecl)
		if ok && function.Name != nil && function.Name.Name == name {
			return function
		}
	}
	t.Fatalf("function %q missing", name)
	return nil
}

func functionNodeIDs(function *ast.FnDecl) []source.NodeID {
	ids := make([]source.NodeID, 0)
	ast.Inspect(function, func(node ast.Node) bool {
		if node != nil {
			ids = append(ids, node.ID())
		}
		return true
	})
	return ids
}

func TestPublishedFunctionNodeIDsIgnoreUnrelatedEdits(t *testing.T) {
	baseline := "fn Prep() {}\nfn main() -> i32 { return 7; }\nfn Tail() {}\n"
	before := "fn Prep() {\n let x = 1;\n let y = 2;\n}\nfn main() -> i32 { return 7; }\nfn Tail() {}\n"
	after := "fn Prep() {}\nfn main() -> i32 { return 7; }\nfn Tail() { let changed = 1; }\n"

	baseMain := publishedFunction(t, baseline, "main")
	beforeMain := publishedFunction(t, before, "main")
	afterMain := publishedFunction(t, after, "main")
	baseIDs := functionNodeIDs(baseMain)
	if !slices.Equal(baseIDs, functionNodeIDs(beforeMain)) || !slices.Equal(baseIDs, functionNodeIDs(afterMain)) {
		t.Fatal("unrelated edit changed main function node identities")
	}
	if baseMain.Location == nil || beforeMain.Location == nil || baseMain.Location.Start.Line == beforeMain.Location.Start.Line {
		t.Fatal("line-shifting edit did not update current source location")
	}
	functionID := baseMain.ID().Function()
	for _, id := range baseIDs {
		if !id.IsValid() || id.Function() != functionID {
			t.Fatalf("node identity %v is not owned by %q", id, functionID)
		}
	}
}

func TestPublishedFunctionNodeIDsShiftOnlyAfterInternalEdit(t *testing.T) {
	baseline := publishedFunction(t, "fn main() -> i32 { return 7; }", "main")
	changed := publishedFunction(t, "fn main() -> i32 { let value = 1; return 7; }", "main")
	baselineFunctionID := baseline.ID().Function()
	changedFunctionID := changed.ID().Function()
	if baselineFunctionID == "" || baselineFunctionID != changedFunctionID {
		t.Fatalf("function identity changed from %q to %q", baselineFunctionID, changedFunctionID)
	}
	if baseline.ID() != changed.ID() || baseline.Body.ID() != changed.Body.ID() {
		t.Fatal("function declaration or body-root identity changed after body edit")
	}
	baselineReturn := baseline.Body.Stmts[0].(*ast.ReturnStmt)
	changedReturn := changed.Body.Stmts[1].(*ast.ReturnStmt)
	if baselineReturn.ID() == changedReturn.ID() {
		t.Fatal("node after inserted syntax retained old function-local ordinal")
	}
}
