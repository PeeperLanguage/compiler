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
	module := BuildModule(testModule(body, nil), BuildQueries{})
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
	graph := BuildModule(testModule(body, nil), BuildQueries{}).Functions[0]
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
	graph := BuildModule(testModule(body, nil), BuildQueries{}).Functions[0]
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
	graph := BuildModule(testModule(body, nil), BuildQueries{}).Functions[0]
	if len(graph.Entry.Sites) != 1 {
		t.Fatalf("entry sites = %#v, want one branch site", graph.Entry.Sites)
	}
	branchSite := graph.Entry.Sites[0]
	branchEdges := graph.SiteEdges.OutEdges(branchSite.ID)
	if branchSite.Kind != SiteTerminator || branchSite.NodeID != 30 || len(branchEdges) != 2 {
		t.Fatalf("branch site = %#v, want two branch successors", branchSite)
	}
	wantKinds := map[EdgeKind]bool{EdgeTrue: false, EdgeFalse: false}
	for _, successor := range branchEdges {
		wantKinds[successor.Kind] = true
		site := graph.Blocks[successor.To.Block].Sites[successor.To.Index]
		predecessors := graph.SiteEdges.InEdges(site.ID)
		if len(predecessors) != 1 || predecessors[0].From != branchSite.ID || predecessors[0].Kind != successor.Kind {
			t.Fatalf("site %#v predecessors = %v, want branch %#v", site.ID, predecessors, branchSite.ID)
		}
	}
	if !wantKinds[EdgeTrue] || !wantKinds[EdgeFalse] {
		t.Fatalf("branch edge kinds = %#v, want true and false", branchEdges)
	}
}

func TestFinalizeSitesLabelsVariantCaseEdges(t *testing.T) {
	first := &Block{ID: 1}
	second := &Block{ID: 2}
	entry := &Block{ID: 0, Terminator: &SwitchVariant{
		NodeID: 41,
		Targets: []VariantTarget{
			{Case: 0, Target: first},
			{Case: 1, Target: second},
		},
	}}
	graph := &Graph{Entry: entry, Exit: &Block{ID: 3}, Blocks: []*Block{entry, first, second}}
	finalizeSites(graph)
	edges := graph.SiteEdges.OutEdges(entry.Sites[0].ID)
	if len(entry.Sites) != 1 || len(edges) != 2 {
		t.Fatalf("switch sites = %#v", entry.Sites)
	}
	for caseIndex, edge := range edges {
		if edge.Kind != EdgeVariantCase || edge.Case != caseIndex {
			t.Fatalf("switch edge %d = %#v", caseIndex, edge)
		}
	}
}

func TestBuildModuleCreatesSemanticVariantSwitchAndSharedJoin(t *testing.T) {
	match := &ast.MatchStmt{
		NodeIDHolder: ast.NodeIDHolder{NodeID: 30},
		Subject:      &ast.Ident{NodeIDHolder: ast.NodeIDHolder{NodeID: 31}, Name: "value"},
		Arms: []*ast.MatchArm{
			{NodeIDHolder: ast.NodeIDHolder{NodeID: 32}, Body: &ast.BlockStmt{NodeIDHolder: ast.NodeIDHolder{NodeID: 33}}},
			{NodeIDHolder: ast.NodeIDHolder{NodeID: 34}, Body: &ast.BlockStmt{NodeIDHolder: ast.NodeIDHolder{NodeID: 35}}},
		},
	}
	after := &ast.ExprStmt{NodeIDHolder: ast.NodeIDHolder{NodeID: 40}, Expr: &ast.NumberLit{NodeIDHolder: ast.NodeIDHolder{NodeID: 41}, Value: "1"}}
	body := &ast.BlockStmt{NodeIDHolder: ast.NodeIDHolder{NodeID: 10}, Stmts: []ast.Stmt{match, after}}
	graph := BuildModule(testModule(body, nil), BuildQueries{MatchCases: func(matchID ast.NodeID) ([]int, bool) {
		if matchID != 30 {
			t.Fatalf("match evidence query = %d, want 30", matchID)
		}
		return []int{1, 0}, true
	}}).Functions[0]
	switchTerm, ok := graph.Entry.Terminator.(*SwitchVariant)
	if !ok || switchTerm.NodeID != 30 || len(switchTerm.Targets) != 2 {
		t.Fatalf("match terminator = %#v", graph.Entry.Terminator)
	}
	if switchTerm.Targets[0].Case != 1 || switchTerm.Targets[1].Case != 0 {
		t.Fatalf("switch targets = %#v, want semantic case indexes [1, 0]", switchTerm.Targets)
	}
	firstJump, firstFallsThrough := switchTerm.Targets[0].Target.Terminator.(*Jump)
	secondJump, secondFallsThrough := switchTerm.Targets[1].Target.Terminator.(*Jump)
	if !firstFallsThrough || !secondFallsThrough || firstJump.Target != secondJump.Target {
		t.Fatalf("match arms do not share join: first=%#v second=%#v", firstJump, secondJump)
	}
	join := firstJump.Target
	if len(join.Sites) == 0 || join.Sites[0].NodeID != 40 {
		t.Fatalf("match join sites = %#v, want following statement", join.Sites)
	}
	caseEdges := graph.SiteEdges.OutEdges(graph.Entry.Sites[0].ID)
	if len(graph.Entry.Sites) != 1 || len(caseEdges) != 2 || caseEdges[0].Case != 1 || caseEdges[1].Case != 0 {
		t.Fatalf("match case edges = %#v", graph.Entry.Sites)
	}
}

func TestBuildModulePreservesDisconnectedStatementsAfterReturn(t *testing.T) {
	location := source.NewLocation("cfg_test.peep", source.Position{Line: 2, Column: 1}, source.Position{Line: 2, Column: 10})
	body := &ast.BlockStmt{NodeIDHolder: ast.NodeIDHolder{NodeID: 10}, Stmts: []ast.Stmt{
		&ast.ReturnStmt{NodeIDHolder: ast.NodeIDHolder{NodeID: 40}, Location: location},
		&ast.ExprStmt{NodeIDHolder: ast.NodeIDHolder{NodeID: 41}, Expr: &ast.NumberLit{Value: "1"}, Location: location},
	}}
	module := BuildModule(testModule(body, nil), BuildQueries{})
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

func TestBuildModuleCreatesForInLoopBlocksWithSynthesizedCondition(t *testing.T) {
	loop := &ast.ForStmt{
		NodeIDHolder: ast.NodeIDHolder{NodeID: 30},
		Iterable:     &ast.Ident{NodeIDHolder: ast.NodeIDHolder{NodeID: 31}, Name: "items"},
		Body:         &ast.BlockStmt{NodeIDHolder: ast.NodeIDHolder{NodeID: 32}},
	}
	body := &ast.BlockStmt{NodeIDHolder: ast.NodeIDHolder{NodeID: 10}, Stmts: []ast.Stmt{loop}}
	graph := BuildModule(testModule(body, nil), BuildQueries{}).Functions[0]
	init := loopBlock(t, graph, 30, BlockLoopInit)
	header := loopBlock(t, graph, 30, BlockLoop)
	loopBody := loopBlock(t, graph, 30, BlockLoopBody)
	latch := loopBlock(t, graph, 30, BlockLoopLatch)
	exit := loopBlock(t, graph, 30, BlockLoopExit)

	entryJump, ok := graph.Entry.Terminator.(*Jump)
	if !ok || entryJump.Target != init {
		t.Fatalf("entry terminator = %#v, want loop init", graph.Entry.Terminator)
	}
	initJump, ok := init.Terminator.(*Jump)
	if !ok || initJump.Target != header {
		t.Fatalf("init terminator = %#v, want loop header", init.Terminator)
	}
	branch, ok := header.Terminator.(*Branch)
	if !ok || branch.NodeID != 30 || branch.ConditionID != 0 || branch.TrueTarget != loopBody || branch.FalseTarget != exit {
		t.Fatalf("for-in header = %#v, want synthesized branch", header.Terminator)
	}
	latchJump, ok := latch.Terminator.(*Jump)
	if !ok || latchJump.Target != header {
		t.Fatalf("latch terminator = %#v, want loop header", latch.Terminator)
	}
}

func TestBuildModuleUsesGuaranteedLoopEntryEvidence(t *testing.T) {
	loop := &ast.ForStmt{
		NodeIDHolder: ast.NodeIDHolder{NodeID: 30},
		Iterable:     &ast.Ident{NodeIDHolder: ast.NodeIDHolder{NodeID: 31}, Name: "items"},
		Body:         &ast.BlockStmt{NodeIDHolder: ast.NodeIDHolder{NodeID: 32}},
	}
	body := &ast.BlockStmt{NodeIDHolder: ast.NodeIDHolder{NodeID: 10}, Stmts: []ast.Stmt{loop}}
	for _, test := range []struct {
		name       string
		guaranteed bool
		wantOrigin BlockOrigin
	}{
		{name: "maybe empty", wantOrigin: BlockLoop},
		{name: "guaranteed entry", guaranteed: true, wantOrigin: BlockLoopBody},
	} {
		t.Run(test.name, func(t *testing.T) {
			graph := BuildModule(testModule(body, nil), BuildQueries{
				LoopGuaranteedEntry: func(loopID ast.NodeID) bool {
					if loopID != 30 {
						t.Fatalf("loop evidence query = %d, want 30", loopID)
					}
					return test.guaranteed
				},
			}).Functions[0]
			init := loopBlock(t, graph, 30, BlockLoopInit)
			header := loopBlock(t, graph, 30, BlockLoop)
			loopBody := loopBlock(t, graph, 30, BlockLoopBody)
			latch := loopBlock(t, graph, 30, BlockLoopLatch)
			initJump, ok := init.Terminator.(*Jump)
			if !ok || initJump.Target.Origin != test.wantOrigin {
				t.Fatalf("init terminator = %#v, want target origin %d", init.Terminator, test.wantOrigin)
			}
			if test.guaranteed && initJump.Target != loopBody {
				t.Fatalf("init target = %#v, want loop body", initJump.Target)
			}
			latchJump, ok := latch.Terminator.(*Jump)
			if !ok || latchJump.Target != header {
				t.Fatalf("latch terminator = %#v, want loop header", latch.Terminator)
			}
		})
	}
}

func TestBuildModuleContinueTargetsLatchAndPreservesUnreachableBody(t *testing.T) {
	loop := &ast.ForStmt{
		NodeIDHolder: ast.NodeIDHolder{NodeID: 30},
		Cond:         &ast.BoolLit{NodeIDHolder: ast.NodeIDHolder{NodeID: 31}, Value: true},
		Body: &ast.BlockStmt{NodeIDHolder: ast.NodeIDHolder{NodeID: 32}, Stmts: []ast.Stmt{
			&ast.ContinueStmt{NodeIDHolder: ast.NodeIDHolder{NodeID: 40}},
			&ast.ExprStmt{NodeIDHolder: ast.NodeIDHolder{NodeID: 41}, Expr: &ast.NumberLit{Value: "1"}},
		}},
	}
	body := &ast.BlockStmt{NodeIDHolder: ast.NodeIDHolder{NodeID: 10}, Stmts: []ast.Stmt{loop}}
	graph := BuildModule(testModule(body, nil), BuildQueries{}).Functions[0]
	latch := loopBlock(t, graph, 30, BlockLoopLatch)
	header := loopBlock(t, graph, 30, BlockLoop)
	foundContinue := false
	foundUnreachable := false
	for _, block := range graph.Blocks {
		for _, site := range block.Sites {
			switch site.NodeID {
			case 40:
				jump, ok := block.Terminator.(*Jump)
				if !ok || jump.Target != latch {
					t.Fatalf("continue terminator = %#v, want latch", block.Terminator)
				}
				foundContinue = true
			case 41:
				foundUnreachable = !block.Reachable
			}
		}
	}
	latchJump, ok := latch.Terminator.(*Jump)
	if !ok || latchJump.Target != header {
		t.Fatalf("latch terminator = %#v, want condition header", latch.Terminator)
	}
	if !foundContinue || !foundUnreachable {
		t.Fatalf("continue=%v unreachable-following-statement=%v", foundContinue, foundUnreachable)
	}
}

func TestBuildModuleNestedLoopJumpsUseInnermostTargets(t *testing.T) {
	inner := &ast.ForStmt{
		NodeIDHolder: ast.NodeIDHolder{NodeID: 40},
		Cond:         &ast.BoolLit{NodeIDHolder: ast.NodeIDHolder{NodeID: 41}, Value: true},
		Body: &ast.BlockStmt{NodeIDHolder: ast.NodeIDHolder{NodeID: 42}, Stmts: []ast.Stmt{
			&ast.IfStmt{
				NodeIDHolder: ast.NodeIDHolder{NodeID: 43},
				Cond:         &ast.BoolLit{NodeIDHolder: ast.NodeIDHolder{NodeID: 44}, Value: true},
				Then: &ast.BlockStmt{NodeIDHolder: ast.NodeIDHolder{NodeID: 45}, Stmts: []ast.Stmt{
					&ast.ContinueStmt{NodeIDHolder: ast.NodeIDHolder{NodeID: 52}},
				}},
			},
			&ast.BreakStmt{NodeIDHolder: ast.NodeIDHolder{NodeID: 50}},
		}},
	}
	outer := &ast.ForStmt{
		NodeIDHolder: ast.NodeIDHolder{NodeID: 30},
		Cond:         &ast.BoolLit{NodeIDHolder: ast.NodeIDHolder{NodeID: 31}, Value: true},
		Body: &ast.BlockStmt{NodeIDHolder: ast.NodeIDHolder{NodeID: 32}, Stmts: []ast.Stmt{
			inner,
			&ast.ContinueStmt{NodeIDHolder: ast.NodeIDHolder{NodeID: 51}},
		}},
	}
	body := &ast.BlockStmt{NodeIDHolder: ast.NodeIDHolder{NodeID: 10}, Stmts: []ast.Stmt{outer}}
	graph := BuildModule(testModule(body, nil), BuildQueries{}).Functions[0]
	innerExit := loopBlock(t, graph, 40, BlockLoopExit)
	innerLatch := loopBlock(t, graph, 40, BlockLoopLatch)
	outerLatch := loopBlock(t, graph, 30, BlockLoopLatch)
	foundBreak := false
	foundInnerContinue := false
	foundOuterContinue := false
	for _, block := range graph.Blocks {
		for _, site := range block.Sites {
			jump, ok := block.Terminator.(*Jump)
			if !ok {
				continue
			}
			if site.NodeID == 50 {
				foundBreak = jump.Target == innerExit
			}
			if site.NodeID == 51 {
				foundOuterContinue = jump.Target == outerLatch
			}
			if site.NodeID == 52 {
				foundInnerContinue = jump.Target == innerLatch
			}
		}
	}
	if !foundBreak || !foundInnerContinue || !foundOuterContinue {
		t.Fatalf("nested targets: inner break=%v inner continue=%v outer continue=%v", foundBreak, foundInnerContinue, foundOuterContinue)
	}
}

func TestBuildModuleLoopJumpExitsOnlyLoopScopesInnermostFirst(t *testing.T) {
	nested := &ast.BlockStmt{NodeIDHolder: ast.NodeIDHolder{NodeID: 33}, Stmts: []ast.Stmt{
		&ast.BreakStmt{NodeIDHolder: ast.NodeIDHolder{NodeID: 40}},
	}}
	loop := &ast.ForStmt{
		NodeIDHolder: ast.NodeIDHolder{NodeID: 30},
		Cond:         &ast.BoolLit{NodeIDHolder: ast.NodeIDHolder{NodeID: 31}, Value: true},
		Body:         &ast.BlockStmt{NodeIDHolder: ast.NodeIDHolder{NodeID: 32}, Stmts: []ast.Stmt{nested}},
	}
	body := &ast.BlockStmt{NodeIDHolder: ast.NodeIDHolder{NodeID: 10}, Stmts: []ast.Stmt{loop}}
	graph := BuildModule(testModule(body, nil), BuildQueries{}).Functions[0]
	for _, block := range graph.Blocks {
		if len(block.Sites) < 3 || block.Sites[0].NodeID != 40 {
			continue
		}
		if block.Sites[1].Kind != SiteScopeExit || block.Sites[1].NodeID != 33 ||
			block.Sites[2].Kind != SiteScopeExit || block.Sites[2].NodeID != 32 {
			t.Fatalf("break sites = %#v, want scope exits [33, 32]", block.Sites)
		}
		for _, site := range block.Sites[1:] {
			if site.Kind == SiteScopeExit && site.NodeID == 10 {
				t.Fatalf("break sites = %#v, must retain enclosing scope 10", block.Sites)
			}
		}
		return
	}
	t.Fatal("break scope-exit sites missing")
}

func TestBuildModuleRecoversLoopJumpsOutsideLoop(t *testing.T) {
	body := &ast.BlockStmt{NodeIDHolder: ast.NodeIDHolder{NodeID: 10}, Stmts: []ast.Stmt{
		&ast.BreakStmt{NodeIDHolder: ast.NodeIDHolder{NodeID: 20}},
		&ast.ExprStmt{NodeIDHolder: ast.NodeIDHolder{NodeID: 21}, Expr: &ast.NumberLit{Value: "1"}},
		&ast.ContinueStmt{NodeIDHolder: ast.NodeIDHolder{NodeID: 22}},
		&ast.ExprStmt{NodeIDHolder: ast.NodeIDHolder{NodeID: 23}, Expr: &ast.NumberLit{Value: "2"}},
	}}
	graph := BuildModule(testModule(body, nil), BuildQueries{}).Functions[0]
	want := []ir.NodeID{20, 21, 22, 23}
	if len(graph.Entry.Sites) < len(want) {
		t.Fatalf("entry sites = %#v, want recovered statements", graph.Entry.Sites)
	}
	for index, nodeID := range want {
		if graph.Entry.Sites[index].Kind != SiteStatement || graph.Entry.Sites[index].NodeID != nodeID {
			t.Fatalf("entry site %d = %#v, want statement %d", index, graph.Entry.Sites[index], nodeID)
		}
	}
}

func TestBuildModuleInfiniteLoopBreakMakesExitReachable(t *testing.T) {
	loop := &ast.ForStmt{
		NodeIDHolder: ast.NodeIDHolder{NodeID: 30},
		Body: &ast.BlockStmt{NodeIDHolder: ast.NodeIDHolder{NodeID: 32}, Stmts: []ast.Stmt{
			&ast.BreakStmt{NodeIDHolder: ast.NodeIDHolder{NodeID: 40}},
		}},
	}
	after := &ast.ExprStmt{NodeIDHolder: ast.NodeIDHolder{NodeID: 50}, Expr: &ast.NumberLit{Value: "1"}}
	body := &ast.BlockStmt{NodeIDHolder: ast.NodeIDHolder{NodeID: 10}, Stmts: []ast.Stmt{loop, after}}
	graph := BuildModule(testModule(body, nil), BuildQueries{}).Functions[0]
	init := loopBlock(t, graph, 30, BlockLoopInit)
	loopBody := loopBlock(t, graph, 30, BlockLoopBody)
	latch := loopBlock(t, graph, 30, BlockLoopLatch)
	exit := loopBlock(t, graph, 30, BlockLoopExit)
	initJump, initOK := init.Terminator.(*Jump)
	latchJump, latchOK := latch.Terminator.(*Jump)
	if !initOK || initJump.Target != loopBody || !latchOK || latchJump.Target != loopBody {
		t.Fatalf("infinite loop topology: init=%#v latch=%#v, want body", init.Terminator, latch.Terminator)
	}
	if !exit.Reachable {
		t.Fatal("infinite-loop exit unreachable despite break")
	}
	foundAfter := false
	for _, site := range exit.Sites {
		if site.Kind == SiteStatement && site.NodeID == 50 {
			foundAfter = true
		}
	}
	if !foundAfter {
		t.Fatalf("loop exit sites = %#v, want post-loop statement", exit.Sites)
	}
}

func TestAnalyzeDoesNotRebuildFinalizedTopology(t *testing.T) {
	body := &ast.BlockStmt{NodeIDHolder: ast.NodeIDHolder{NodeID: 10}}
	module := BuildModule(testModule(body, nil), BuildQueries{})
	graph := module.Functions[0]
	before := graph.BlockEdges.InEdges(graph.Entry.ID)
	graph.Entry.Sites = nil
	Analyze(module, diagnostics.NewDiagnosticBag(), nil)
	if graph.Entry.Sites != nil || !reflect.DeepEqual(graph.BlockEdges.InEdges(graph.Entry.ID), before) {
		t.Fatalf("Analyze mutated finalized topology: entry = %#v", graph.Entry)
	}
}

func TestAnalyzeReportsMissingReturn(t *testing.T) {
	body := &ast.BlockStmt{NodeIDHolder: ast.NodeIDHolder{NodeID: 10}}
	returnType := &ast.NamedType{NodeIDHolder: ast.NodeIDHolder{NodeID: 11}, Name: "i32"}
	diag := diagnostics.NewDiagnosticBag()
	Analyze(BuildModule(testModule(body, returnType), BuildQueries{}), diag, nil)
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
	Analyze(BuildModule(testModule(body, nil), BuildQueries{}), diag, func(conditionID, scopeID ir.NodeID) (bool, bool) {
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
	Analyze(BuildModule(testModule(body, nil), BuildQueries{}), diag, func(ir.NodeID, ir.NodeID) (bool, bool) {
		queries++
		return false, true
	})
	if queries != 0 || hasDiagnosticCode(diag, diagnostics.WarnConstantConditionFalse) {
		t.Fatalf("loop condition produced if-only diagnostic: queries=%d diagnostics=%#v", queries, diag.Diagnostics())
	}
}

func loopBlock(t *testing.T, graph *Graph, loopID ir.NodeID, origin BlockOrigin) *Block {
	t.Helper()
	var found *Block
	for _, block := range graph.Blocks {
		if block.NodeID != loopID || block.Origin != origin {
			continue
		}
		if found != nil {
			t.Fatalf("multiple loop blocks for NodeID %d and origin %d", loopID, origin)
		}
		found = block
	}
	if found == nil {
		t.Fatalf("loop block missing for NodeID %d and origin %d", loopID, origin)
	}
	return found
}

func hasDiagnosticCode(diag *diagnostics.DiagnosticBag, code string) bool {
	for _, item := range diag.Diagnostics() {
		if item != nil && item.Code == code {
			return true
		}
	}
	return false
}

// Missing-return reporting walks back to the nearest structured-control block
// to name the branch that falls through, so this classification decides which
// span a user sees. A loop exit carries a loop role but is a continuation: the
// code after the loop lives there.
func TestStructuredControlClassifiesEveryBlockOrigin(t *testing.T) {
	for _, test := range []struct {
		origin BlockOrigin
		name   string
		want   bool
	}{
		{BlockNormal, "normal", false},
		{BlockLoopExit, "loop exit", false},
		{BlockThen, "then", true},
		{BlockElse, "else", true},
		{BlockLoopInit, "loop init", true},
		{BlockLoop, "loop header", true},
		{BlockLoopBody, "loop body", true},
		{BlockLoopLatch, "loop latch", true},
	} {
		if got := structuredControl(test.origin); got != test.want {
			t.Errorf("structuredControl(%s) = %t, want %t", test.name, got, test.want)
		}
	}
}
