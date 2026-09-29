package typechecker

import (
	"compiler/internal/source"
	"testing"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/frontend/lexer"
	"compiler/internal/frontend/parser"
	"compiler/internal/ir/cfg"
	"compiler/internal/ir/thir"
	"compiler/internal/module"
	"compiler/internal/moduleid"
	"compiler/internal/project"
	"compiler/internal/semantics/analysis"
	"compiler/internal/semantics/binder"
	"compiler/internal/semantics/collector"
	"compiler/internal/semantics/resolver"
	"compiler/internal/semantics/typeinfo"
	"compiler/pkg/peeper"
)

func checkFlowSource(t *testing.T, src string) (*module.Module, *diagnostics.DiagnosticBag) {
	t.Helper()
	const filePath = "flow_test" + peeper.SourceExt
	diag := diagnostics.NewDiagnosticBag()
	diag.AddSourceContent(filePath, src)
	ctx := project.New(".", peeper.SourceExt, diag)
	module := &module.Module{
		ID:       moduleid.ID{Origin: string(project.ModuleOriginLocal), ImportPath: "flow_test"},
		FilePath: filePath,
		Content:  src,
		AST:      parser.New(filePath, lexer.New(filePath, src, diag).Tokenize(), diag).ParseModule(),
		Imports:  make(map[string]module.ResolvedImport),
	}
	ctx.AddModule(module)

	collector.Collect(ctx, module)
	binder.Bind(ctx, module)
	resolver.Resolve(ctx, module)
	module.THIR = checkWithEvidence(t, ctx, module)
	module.CFG = cfg.BuildModule(module.THIR)
	module.Analysis = analysis.Run(diag, analysis.Input{Source: module.THIR, CFG: module.CFG, Scope: module.ModuleScope, SymbolIndex: module.SymbolIndex})
	return module, diag
}

func TestCallIterationPublishesCheckedOperations(t *testing.T) {
	module, diag := checkFlowSource(t, `struct Cursor { value: i32, limit: i32 }
fn (self: &mut Cursor) Next() -> ?i32 { return none; }
fn main() {
	let mut cursor = Cursor.{ value = 0, limit = 3 };
	for cursor in cursor.Next() {
		let item: i32 = cursor;
		let mut inner = Cursor.{ value = item, limit = 3 };
		for value in inner.Next() { if value == 1 { continue; } }
	}
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	if err := module.CFG.Validate(); err != nil {
		t.Fatal(err)
	}
	checkedCount := 0
	forEachCheckedIterationForTests(testEvidence(module), func(id source.NodeID, expansion *ast.BlockStmt) {
		checkedCount++
		if _, found := testEvidence(module).ForIteration(id); found {
			t.Errorf("source loop %v has both checked and ordinary iteration evidence", id)
		}
		checked := expansion.Stmts[len(expansion.Stmts)-1].(*ast.ForStmt)
		var sourceLoop *ast.ForStmt
		ast.Inspect(module.AST.Stmts[len(module.AST.Stmts)-1], func(node ast.Node) bool {
			if loop, ok := node.(*ast.ForStmt); ok && loop.ID() == id {
				sourceLoop = loop
			}
			return true
		})
		if sourceLoop == nil {
			t.Fatalf("source loop %v missing", id)
		}
		if _, ok := module.THIR.Node(expansion.ID()).(*thir.Block); !ok {
			t.Fatal("checked expansion missing from THIR")
		}
		if checked.ID() == id || checked.Iterable != nil || checked.Cond != nil {
			t.Fatalf("checked loop identity not isolated: %#v", checked)
		}
		if _, ok := module.THIR.Node(checked.ID()).(*thir.For); !ok {
			t.Fatal("checked loop missing from THIR")
		}
		if sourceLoop.ID() != id || sourceLoop.Iterable == nil {
			t.Fatalf("source loop index replaced by checked loop: %#v", sourceLoop)
		}
		expansionScope := module.SymbolIndex.Scope(expansion)
		checkedScope := module.SymbolIndex.Scope(checked.Body)
		sourceBodyScope := module.SymbolIndex.Scope(sourceLoop.Body)
		if sourceBodyScope == nil || sourceBodyScope.Parent() == expansionScope || sourceBodyScope.Parent() == checkedScope {
			t.Fatal("checked iteration mutated source body scope parent")
		}
		result := checked.Body.Stmts[0].(*ast.LetDecl)
		call := result.Value.(*ast.CallExpr)
		selector := call.Callee.(*ast.SelectorExpr)
		if module.SymbolIndex.Symbol(selector.Name) == nil {
			t.Fatal("missing static method evidence")
		}
		if mutable, found := testEvidence(module).ReferenceArgument(selector.Expr.ID()); !found || !mutable {
			t.Fatal("generated receiver missing ordinary mutable-reference evidence")
		}
		item := checked.Body.Stmts[2].(*ast.LetDecl)
		if item.Name == sourceLoop.Value || item.Name.ID() == sourceLoop.Value.ID() {
			t.Fatal("checked iteration reused source binding syntax")
		}
		if got := typeinfo.TypeText(module.SymbolIndex.Symbol(item.Name).Type); got != "i32" {
			t.Fatalf("item type = %s", got)
		}
	})
	if checkedCount != 2 {
		t.Fatalf("checked iterations = %v, want 2", checkedCount)
	}
}
