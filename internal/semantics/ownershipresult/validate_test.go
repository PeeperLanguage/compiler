package ownershipresult

import (
	"compiler/internal/source"
	"strings"
	"testing"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/frontend/lexer"
	"compiler/internal/frontend/parser"
	"compiler/internal/ir"
	"compiler/internal/ir/cfg"
	"compiler/internal/ir/thir"
	"compiler/internal/moduleid"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
)

const validationGraphSource = "fn main() -> i32 {\n\treturn 0;\n}\n"

// buildGraph produces real CFG topology so the validator is checked against the
// artifact it actually receives, not a hand-shaped stand-in.
func buildGraph(t *testing.T, sourceText string) (*cfg.Module, moduleid.FunctionID) {
	t.Helper()
	const file = "validate_test.peep"
	diag := diagnostics.NewDiagnosticBag()
	syntax := parser.New(file, lexer.New(file, sourceText, diag).Tokenize(), diag).ParseModule()
	owner := moduleid.ID{Origin: "local", ImportPath: "test"}
	ast.PublishFunctionIdentities(owner, syntax)
	functions := make([]*thir.Function, 0)
	ast.ForEachDecl(syntax, func(decl ast.Decl) bool {
		fn, ok := decl.(*ast.FnDecl)
		if !ok || fn == nil || fn.Body == nil {
			return true
		}
		body := &thir.Block{StmtInfo: thir.StmtInfo{Source: ir.SourceInfo{NodeID: fn.Body.ID(), Location: ast.LocOf(fn.Body)}}}
		for _, stmt := range fn.Body.Stmts {
			switch node := stmt.(type) {
			case *ast.ReturnStmt:
				body.Stmts = append(body.Stmts, &thir.Return{StmtInfo: thir.StmtInfo{Source: ir.SourceInfo{NodeID: node.ID(), Location: ast.LocOf(node)}}})
			case *ast.ExprStmt:
				body.Stmts = append(body.Stmts, &thir.ExprStmt{StmtInfo: thir.StmtInfo{Source: ir.SourceInfo{NodeID: node.ID(), Location: ast.LocOf(node)}}})
			}
		}
		name := ""
		if fn.Name != nil {
			name = fn.Name.Name
		}
		functions = append(functions, &thir.Function{Identity: fn.ID().Function(), Name: name, Source: ir.SourceInfo{NodeID: fn.ID(), Location: ast.LocOf(fn)}, Body: body})
		return true
	})
	typed := thir.NewModule(owner.ImportPath, file, functions)
	graphs := cfg.BuildModule(typed)
	if graphs == nil || len(graphs.Functions) == 0 {
		t.Fatalf("no CFG built: %s", diag.EmitAllToString())
	}
	return graphs, graphs.Functions[0].FunctionID
}

func validationSource(exprs ...thir.Expr) *thir.Module {
	const fnID = moduleid.FunctionID("validation::function")
	stmts := make([]thir.Stmt, 0, len(exprs))
	for index, expr := range exprs {
		id := source.ParsedNodeID(uint64(1000 + index))
		stmts = append(stmts, &thir.ExprStmt{StmtInfo: thir.StmtInfo{Source: ir.SourceInfo{NodeID: id}}, Value: expr})
	}
	body := &thir.Block{StmtInfo: thir.StmtInfo{Source: ir.SourceInfo{NodeID: source.ParsedNodeID(900)}}, Stmts: stmts}
	return thir.NewModule("validation", "", []*thir.Function{{Identity: fnID, Name: "validation", Body: body, Source: ir.SourceInfo{NodeID: source.ParsedNodeID(899)}}})
}

func validationExpr(id source.NodeID, typ typeinfo.Type, use typeinfo.UseKind, hasUse bool) thir.Expr {
	return &thir.NumberLiteral{ExprInfo: thir.ExprInfo{Source: ir.SourceInfo{NodeID: id}, Type: typ, Use: use, HasUse: hasUse}, Value: "1"}
}

func emptyPlan() *CleanupPlan {
	return &CleanupPlan{
		AfterScope:             make(map[cfg.SiteID][]symbols.SymbolID),
		BeforeReturn:           make(map[source.NodeID][]symbols.SymbolID),
		BeforeAssign:           make(map[source.NodeID]struct{}),
		DiscardedValue:         make(map[source.NodeID]struct{}),
		ProjectionBase:         make(map[source.NodeID]struct{}),
		MatchFieldDrops:        make(map[source.NodeID][]int),
		MatchWholePayloadDrops: make(map[source.NodeID]struct{}),
	}
}

func TestValidateAcceptsConsistentEvidence(t *testing.T) {
	graphs, fnID := buildGraph(t, validationGraphSource)
	result := Result{fnID: emptyPlan()}
	if err := result.Validate(validationSource(), symbols.NewIndex(), graphs); err != nil {
		t.Fatalf("consistent evidence rejected: %v", err)
	}
}

func TestValidateRejectsEvidenceGaps(t *testing.T) {
	graphs, fnID := buildGraph(t, validationGraphSource)
	tests := []struct {
		name, want string
		typed      *thir.Module
		mutate     func(*CleanupPlan)
	}{
		{name: "use kind without a type", want: "no expression type", typed: validationSource(validationExpr(source.ParsedNodeID(7), nil, typeinfo.UseMove, true))},
		{name: "copy of a type with no copy operation", want: "no copy operation", typed: validationSource(validationExpr(source.ParsedNodeID(7), &typeinfo.StringType{}, typeinfo.UseCopy, true))},
		{name: "call argument with no use kind", want: "no published use kind", typed: validationSource(&thir.Call{ExprInfo: thir.ExprInfo{Source: ir.SourceInfo{NodeID: source.ParsedNodeID(5)}, Type: &typeinfo.IntegerType{IsSigned: true, Bits: 32}}, Args: []thir.Expr{validationExpr(source.ParsedNodeID(41), &typeinfo.IntegerType{IsSigned: true, Bits: 32}, typeinfo.UseRead, false)}})},
		{name: "drop at a site that is not a scope exit", want: "not a scope exit", typed: validationSource(), mutate: func(plan *CleanupPlan) {
			plan.AfterScope[cfg.SiteID{}] = []symbols.SymbolID{symbols.ProjectedSymbolID(symbols.SymbolVar, "value")}
		}},
		{name: "return drop at an unknown node", want: "not a site in its CFG", typed: validationSource(), mutate: func(plan *CleanupPlan) {
			plan.BeforeReturn[source.ParsedNodeID(9999)] = []symbols.SymbolID{symbols.ProjectedSymbolID(symbols.SymbolVar, "value")}
		}},
		{name: "unidentified drop target", want: "unidentified", typed: validationSource(), mutate: func(plan *CleanupPlan) { plan.BeforeReturn[source.ParsedNodeID(9999)] = []symbols.SymbolID{{}} }},
		{name: "projection base with no type", want: "no expression type", typed: validationSource(), mutate: func(plan *CleanupPlan) { plan.ProjectionBase[source.ParsedNodeID(8888)] = struct{}{} }},
		{name: "match drop outside a block", want: "not a block", typed: validationSource(), mutate: func(plan *CleanupPlan) { plan.MatchWholePayloadDrops[source.ParsedNodeID(7777)] = struct{}{} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := emptyPlan()
			if tt.mutate != nil {
				tt.mutate(plan)
			}
			err := Result{fnID: plan}.Validate(tt.typed, symbols.NewIndex(), graphs)
			if err == nil {
				t.Fatal("inconsistent evidence accepted")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %q, want it to mention %q", err, tt.want)
			}
		})
	}
}

func TestValidateRejectsPlanWithoutCFG(t *testing.T) {
	graphs, _ := buildGraph(t, validationGraphSource)
	err := Result{moduleid.FunctionID("missing-function"): emptyPlan()}.Validate(validationSource(), symbols.NewIndex(), graphs)
	if err == nil || !strings.Contains(err.Error(), "no CFG") {
		t.Fatalf("error = %v, want a missing-CFG report", err)
	}
}

func TestValidateRejectsMissingFunctionPlan(t *testing.T) {
	graphs, firstID := buildGraph(t, `fn first() {}
fn second() {}`)
	if len(graphs.Functions) != 2 {
		t.Fatalf("CFG functions = %d, want 2", len(graphs.Functions))
	}
	if err := (Result{firstID: emptyPlan()}).Validate(validationSource(), symbols.NewIndex(), graphs); err == nil || !strings.Contains(err.Error(), "no published cleanup plan") {
		t.Fatalf("error = %v, want missing-function evidence error", err)
	}
}

func TestValidateRejectsNilPlan(t *testing.T) {
	graphs, fnID := buildGraph(t, validationGraphSource)
	err := Result{fnID: nil}.Validate(validationSource(), symbols.NewIndex(), graphs)
	if err == nil || !strings.Contains(err.Error(), "nil cleanup plan") {
		t.Fatalf("error = %v, want a nil-plan report", err)
	}
}

// Plans and evidence are maps, so an unsorted report would name different
// problems on different runs for one broken module.
func TestValidateReportsProblemsDeterministically(t *testing.T) {
	graphs, fnID := buildGraph(t, validationGraphSource)
	first := ""
	for attempt := 0; attempt < 8; attempt++ {
		plan := emptyPlan()
		exprs := make([]thir.Expr, 0, 40)
		for ordinal := uint64(1); ordinal <= 40; ordinal++ {
			id := source.ParsedNodeID(ordinal)
			exprs = append(exprs, validationExpr(id, nil, typeinfo.UseMove, true))
			plan.ProjectionBase[id] = struct{}{}
		}
		err := Result{fnID: plan}.Validate(validationSource(exprs...), symbols.NewIndex(), graphs)
		if err == nil {
			t.Fatal("inconsistent evidence accepted")
		}
		if attempt == 0 {
			first = err.Error()
			continue
		}
		if err.Error() != first {
			t.Fatalf("report changed between runs:\n%s\n%s", first, err.Error())
		}
	}
	if !strings.Contains(first, "more)") {
		t.Fatalf("report = %q, want a truncated sample", first)
	}
}
