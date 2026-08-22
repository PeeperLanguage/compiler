package cfg

import (
	"reflect"
	"testing"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/ir"
	"compiler/internal/source"
)

func testModule(body *ast.BlockStmt, returnType ast.TypeExpr) *ast.Module {
	location := source.NewLocation("cfg_test.peep", source.Position{Line: 1, Column: 1}, source.Position{Line: 1, Column: 10})
	fn := &ast.FnDecl{
		NodeIDHolder: ast.NodeIDHolder{NodeID: 1},
		Name:         &ast.Ident{NodeIDHolder: ast.NodeIDHolder{NodeID: 2}, Name: "main", Location: location},
		ReturnType:   returnType,
		Body:         body,
		Location:     location,
	}
	return &ast.Module{Stmts: []ast.Stmt{fn}}
}

func TestModuleIndexesFunctionBySourceIdentity(t *testing.T) {
	body := &ast.BlockStmt{NodeIDHolder: ast.NodeIDHolder{NodeID: 10}}
	module := BuildModule(testModule(body, nil))
	if len(module.Functions) != 1 || module.Function(ir.NodeID(1)) != module.Functions[0] {
		t.Fatalf("CFG function index = %#v, want source NodeID lookup", module)
	}
	if _, found := reflect.TypeOf(Graph{}).FieldByName("Cleanup"); found {
		t.Fatal("CFG graph retains ownership cleanup output")
	}
}

func TestBuildModulePreservesLexicalScopeExits(t *testing.T) {
	nested := &ast.BlockStmt{NodeIDHolder: ast.NodeIDHolder{NodeID: 20}}
	body := &ast.BlockStmt{NodeIDHolder: ast.NodeIDHolder{NodeID: 10}, Stmts: []ast.Stmt{nested}}
	graph := BuildModule(testModule(body, nil)).Functions[0]
	if graph.Entry == nil || len(graph.Entry.Sites) != 1 || graph.Entry.Sites[0].Kind != SiteScopeExit || graph.Entry.Sites[0].NodeID != 20 {
		t.Fatalf("entry sites = %#v, want nested scope exit", graph.Entry.Sites)
	}
	if len(graph.Blocks) < 3 {
		t.Fatalf("CFG blocks = %#v, want nested continuation", graph.Blocks)
	}
	continuation := graph.Blocks[2]
	if len(continuation.Sites) != 1 || continuation.Sites[0].Kind != SiteScopeExit || continuation.Sites[0].NodeID != 10 {
		t.Fatalf("continuation sites = %#v, want function-body scope exit", continuation.Sites)
	}
}

func TestBuildModulePreservesTerminatorSourceIdentity(t *testing.T) {
	location := source.NewLocation("cfg_test.peep", source.Position{Line: 2, Column: 1}, source.Position{Line: 2, Column: 10})
	branch := &ast.IfStmt{
		NodeIDHolder: ast.NodeIDHolder{NodeID: 30},
		Cond:         &ast.BoolLit{NodeIDHolder: ast.NodeIDHolder{NodeID: 31}, Value: true, Location: location},
		Then:         &ast.BlockStmt{NodeIDHolder: ast.NodeIDHolder{NodeID: 32}},
		Location:     location,
	}
	ret := &ast.ReturnStmt{NodeIDHolder: ast.NodeIDHolder{NodeID: 40}, Location: location}
	body := &ast.BlockStmt{NodeIDHolder: ast.NodeIDHolder{NodeID: 10}, Stmts: []ast.Stmt{branch, ret}}
	graph := BuildModule(testModule(body, nil)).Functions[0]
	branchTerm, ok := graph.Entry.Terminator.(*Branch)
	if !ok || branchTerm.NodeID != 30 || branchTerm.ConditionID != 31 {
		t.Fatalf("branch = %#v, want source nodes 30 and 31", graph.Entry.Terminator)
	}
	for _, block := range graph.Blocks {
		if returnTerm, ok := block.Terminator.(*Return); ok {
			if returnTerm.NodeID != 40 {
				t.Fatalf("return = %#v, want source node 40", returnTerm)
			}
			return
		}
	}
	t.Fatal("return terminator missing")
}

func TestBuildModuleCreatesCanonicalSiteAdjacency(t *testing.T) {
	branch := &ast.IfStmt{
		NodeIDHolder: ast.NodeIDHolder{NodeID: 30},
		Cond:         &ast.BoolLit{NodeIDHolder: ast.NodeIDHolder{NodeID: 31}, Value: true},
		Then:         &ast.BlockStmt{NodeIDHolder: ast.NodeIDHolder{NodeID: 32}},
		Else:         &ast.BlockStmt{NodeIDHolder: ast.NodeIDHolder{NodeID: 33}},
	}
	body := &ast.BlockStmt{NodeIDHolder: ast.NodeIDHolder{NodeID: 10}, Stmts: []ast.Stmt{branch}}
	graph := BuildModule(testModule(body, nil)).Functions[0]
	if len(graph.Entry.Sites) != 1 {
		t.Fatalf("entry sites = %#v, want one branch site", graph.Entry.Sites)
	}
	branchSite := graph.Entry.Sites[0]
	if branchSite.Kind != SiteTerminator || branchSite.NodeID != 30 || len(branchSite.Successors) != 2 {
		t.Fatalf("branch site = %#v, want two branch successors", branchSite)
	}
	wantKinds := map[EdgeKind]bool{EdgeTrue: false, EdgeFalse: false}
	for _, successor := range branchSite.Successors {
		wantKinds[successor.Kind] = true
		site := graph.Blocks[successor.To.Block].Sites[successor.To.Index]
		if len(site.Predecessors) != 1 || site.Predecessors[0].From != branchSite.ID || site.Predecessors[0].Kind != successor.Kind {
			t.Fatalf("site %#v predecessors = %v, want branch %#v", site.ID, site.Predecessors, branchSite.ID)
		}
	}
	if !wantKinds[EdgeTrue] || !wantKinds[EdgeFalse] {
		t.Fatalf("branch edge kinds = %#v, want true and false", branchSite.Successors)
	}
}

func TestBuildModulePreservesDisconnectedStatementsAfterReturn(t *testing.T) {
	location := source.NewLocation("cfg_test.peep", source.Position{Line: 2, Column: 1}, source.Position{Line: 2, Column: 10})
	body := &ast.BlockStmt{NodeIDHolder: ast.NodeIDHolder{NodeID: 10}, Stmts: []ast.Stmt{
		&ast.ReturnStmt{NodeIDHolder: ast.NodeIDHolder{NodeID: 40}, Location: location},
		&ast.ExprStmt{NodeIDHolder: ast.NodeIDHolder{NodeID: 41}, Expr: &ast.NumberLit{Value: "1"}, Location: location},
	}}
	module := BuildModule(testModule(body, nil))
	graph := module.Functions[0]
	found := false
	for _, block := range graph.Blocks {
		for _, site := range block.Sites {
			if !block.Reachable && site.Kind == SiteStatement && site.NodeID == 41 {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("CFG blocks = %#v, want disconnected statement after return", graph.Blocks)
	}
	diag := diagnostics.NewDiagnosticBag()
	Analyze(module, diag, nil)
	if !hasDiagnosticCode(diag, diagnostics.WarnUnreachableCode) {
		t.Fatalf("diagnostics = %#v, want unreachable warning", diag.Diagnostics())
	}
}

func TestAnalyzeDoesNotRebuildFinalizedTopology(t *testing.T) {
	body := &ast.BlockStmt{NodeIDHolder: ast.NodeIDHolder{NodeID: 10}}
	module := BuildModule(testModule(body, nil))
	graph := module.Functions[0]
	before := append([]*Block(nil), graph.Entry.Predecessors...)
	graph.Entry.Sites = nil
	Analyze(module, diagnostics.NewDiagnosticBag(), nil)
	if graph.Entry.Sites != nil || !reflect.DeepEqual(graph.Entry.Predecessors, before) {
		t.Fatalf("Analyze mutated finalized topology: entry = %#v", graph.Entry)
	}
}

func TestAnalyzeReportsMissingReturn(t *testing.T) {
	body := &ast.BlockStmt{NodeIDHolder: ast.NodeIDHolder{NodeID: 10}}
	returnType := &ast.NamedType{NodeIDHolder: ast.NodeIDHolder{NodeID: 11}, Name: "i32"}
	diag := diagnostics.NewDiagnosticBag()
	Analyze(BuildModule(testModule(body, returnType)), diag, nil)
	if !hasDiagnosticCode(diag, diagnostics.ErrMissingReturn) {
		t.Fatalf("diagnostics = %#v, want missing return", diag.Diagnostics())
	}
}

func TestAnalyzeReportsConstantIfCondition(t *testing.T) {
	location := source.NewLocation("cfg_test.peep", source.Position{Line: 2, Column: 1}, source.Position{Line: 4, Column: 2})
	body := &ast.BlockStmt{NodeIDHolder: ast.NodeIDHolder{NodeID: 10}, Stmts: []ast.Stmt{&ast.IfStmt{
		NodeIDHolder: ast.NodeIDHolder{NodeID: 30},
		Cond:         &ast.BoolLit{NodeIDHolder: ast.NodeIDHolder{NodeID: 31}, Value: false, Location: location},
		Then:         &ast.BlockStmt{NodeIDHolder: ast.NodeIDHolder{NodeID: 32}},
		Location:     location,
	}}}
	diag := diagnostics.NewDiagnosticBag()
	Analyze(BuildModule(testModule(body, nil)), diag, func(conditionID, scopeID ir.NodeID) (bool, bool) {
		if conditionID != 31 || scopeID != 10 {
			t.Fatalf("constant condition query = (%d, %d), want (31, 10)", conditionID, scopeID)
		}
		return false, true
	})
	if !hasDiagnosticCode(diag, diagnostics.WarnConstantConditionFalse) {
		t.Fatalf("diagnostics = %#v, want constant-false warning", diag.Diagnostics())
	}
}

func TestAnalyzeDoesNotReportConstantLoopCondition(t *testing.T) {
	location := source.NewLocation("cfg_test.peep", source.Position{Line: 2, Column: 1}, source.Position{Line: 4, Column: 2})
	body := &ast.BlockStmt{NodeIDHolder: ast.NodeIDHolder{NodeID: 10}, Stmts: []ast.Stmt{&ast.ForStmt{
		NodeIDHolder: ast.NodeIDHolder{NodeID: 30},
		Cond:         &ast.BoolLit{NodeIDHolder: ast.NodeIDHolder{NodeID: 31}, Value: false, Location: location},
		Body:         &ast.BlockStmt{NodeIDHolder: ast.NodeIDHolder{NodeID: 32}},
		Location:     location,
	}}}
	diag := diagnostics.NewDiagnosticBag()
	queries := 0
	Analyze(BuildModule(testModule(body, nil)), diag, func(ir.NodeID, ir.NodeID) (bool, bool) {
		queries++
		return false, true
	})
	if queries != 0 || hasDiagnosticCode(diag, diagnostics.WarnConstantConditionFalse) {
		t.Fatalf("loop condition produced if-only diagnostic: queries=%d diagnostics=%#v", queries, diag.Diagnostics())
	}
}

func hasDiagnosticCode(diag *diagnostics.DiagnosticBag, code string) bool {
	for _, item := range diag.Diagnostics() {
		if item != nil && item.Code == code {
			return true
		}
	}
	return false
}
