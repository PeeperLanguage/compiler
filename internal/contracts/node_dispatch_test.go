// Package contracts owns the phase-coverage contract for AST node handling.
//
// Structural traversal is already centralized in ast.Inspect and friends, which
// prevents a forgotten child walk. It cannot prevent a forgotten *semantic*
// decision: adding a statement kind and never teaching a phase about it. These
// tests read the real sources and compare every declared statement kind against
// every phase that dispatches on statements, so an omission fails immediately
// instead of surfacing as wrong output much later.
package contracts

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// decision is the classification a phase must make for a node kind it does not
// implement a case for. A kind with a case is handled by definition. Whether a
// case body handles or rejects is not distinguishable from the source shape at
// this tier; compile-time visitor interfaces are needed to separate those.
type decision uint8

const (
	// traverse: canonical child walk is sufficient, no phase-specific semantics.
	traverse decision = iota
	// ignore: node is intentionally irrelevant at this site.
	ignore
	// reject: node is invalid here and another phase reports it.
	reject
)

func (d decision) String() string {
	switch d {
	case traverse:
		return "traverse"
	case ignore:
		return "ignore"
	case reject:
		return "reject"
	}
	return "unknown"
}

type classification struct {
	decision decision
	reason   string
}

// dispatchSite is one function that switches over an AST family.
type dispatchSite struct {
	file string
	fn   string
	// omitted classifies kinds this site implements no case for. An empty map
	// means the site is fully exhaustive.
	omitted map[string]classification
	// inertDeclarations adds the shared declaration-statement classification.
	// Set it on CFG-site consumers rather than repeating one rule at each.
	inertDeclarations bool
}

// Declarations all implement stmtNode, and parseStmt really does build FnDecl,
// StructDecl, InterfaceDecl, EnumDecl and TypeAliasDecl nodes inside a block.
// The resolver reports them as unsupported statements and HIR lowers them to
// hir.Invalid, but CFG through ownership are not error-gated, so the nodes do
// reach those sites. They are skipped because a declaration evaluates no
// expression and performs no assignment at a CFG site, not because they are
// absent.
var declarationStatements = []string{
	"ImportDecl", "FnDecl", "TypeAliasDecl", "StructDecl",
	"InterfaceDecl", "EnumDecl", "BadDecl",
}

const (
	decomposedByCFGReason = "blocks are decomposed by CFG construction and are never a site statement"
	elsePositionReason    = "parseIfStmt produces only a block or else-if in else position; the default rejects anything else"
	noConditionReason     = "carries no branch condition"
)

const declarationStatementReason = "reaches this site after the resolver reports an unsupported statement, and evaluates no expression here"

// omissions returns the site's declared classifications including the shared
// declaration-statement rule.
func (d dispatchSite) omissions() map[string]classification {
	merged := make(map[string]classification, len(d.omitted)+len(declarationStatements))
	for kind, entry := range d.omitted {
		merged[kind] = entry
	}
	if d.inertDeclarations {
		for _, kind := range declarationStatements {
			if _, declared := merged[kind]; !declared {
				merged[kind] = classification{decision: ignore, reason: declarationStatementReason}
			}
		}
	}
	return merged
}

// Sites are exhaustive unless they declare a reason per omitted kind. Adding a
// statement kind must either add a case here or record why the kind is inert.
var statementSites = []dispatchSite{
	{file: "semantics/resolver/resolver.go", fn: "resolveStmt"},
	{file: "semantics/typechecker/check_stmt.go", fn: "checkStmt"},
	{file: "ir/cfg/build.go", fn: "buildStmt"},
	{file: "ir/hir/lower/module_lower.go", fn: "appendStmt"},
	{
		file: "ir/hir/lower/module_lower.go",
		fn:   "lowerElse",
		omitted: map[string]classification{
			"BreakStmt":    {reject, elsePositionReason},
			"ContinueStmt": {reject, elsePositionReason},
			"MatchStmt":    {reject, elsePositionReason},
		},
	},
	// The remaining sites run per CFG site, where control flow is already
	// decomposed into blocks and edges. They extract the expressions a statement
	// evaluates at that site, so statements carrying no expression are inert.
	{
		file:              "semantics/ownership/ownership.go",
		fn:                "applyStmt",
		inertDeclarations: true,
		omitted: map[string]classification{
			"BlockStmt":    {ignore, decomposedByCFGReason},
			"BadStmt":      {ignore, "recovery node carries no ownership effect"},
			"BreakStmt":    {ignore, "transfer is a CFG edge, not a site-level ownership effect"},
			"ContinueStmt": {ignore, "transfer is a CFG edge, not a site-level ownership effect"},
		},
	},
	{
		file:              "semantics/ownership/reference.go",
		fn:                "symbolUseSequence",
		inertDeclarations: true,
		omitted: map[string]classification{
			"BlockStmt":    {ignore, decomposedByCFGReason},
			"BadStmt":      {ignore, "recovery node evaluates no expression"},
			"BreakStmt":    {ignore, "evaluates no expression"},
			"ContinueStmt": {ignore, "evaluates no expression"},
		},
	},
	{
		file:              "semantics/definiteinit/initialization.go",
		fn:                "checkReads",
		inertDeclarations: true,
		omitted: map[string]classification{
			"BlockStmt":    {ignore, decomposedByCFGReason},
			"BadStmt":      {ignore, "recovery node reads nothing"},
			"IfStmt":       {ignore, "condition arrives separately through the CFG site condition"},
			"ForStmt":      {ignore, "condition arrives separately through the CFG site condition"},
			"MatchStmt":    {ignore, "subject arrives separately through the CFG site condition"},
			"BreakStmt":    {ignore, "reads nothing"},
			"ContinueStmt": {ignore, "reads nothing"},
		},
	},
	{
		file:              "semantics/typechecker/flow.go",
		fn:                "applyConditionEdge",
		inertDeclarations: true,
		omitted: map[string]classification{
			"BlockStmt":    {ignore, noConditionReason},
			"ExprStmt":     {ignore, noConditionReason},
			"AssignStmt":   {ignore, noConditionReason},
			"ReturnStmt":   {ignore, noConditionReason},
			"BadStmt":      {ignore, noConditionReason},
			"BreakStmt":    {ignore, noConditionReason},
			"ContinueStmt": {ignore, noConditionReason},
			"MatchStmt":    {ignore, "match narrowing uses case tests, not a true/false condition edge"},
			"LetDecl":      {ignore, noConditionReason},
			"ConstDecl":    {ignore, noConditionReason},
		},
	},
}

func internalDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate contracts package source")
	}
	return filepath.Dir(filepath.Dir(thisFile))
}

// declaredKinds scans the whole AST package for types implementing marker, so
// the contract tracks the real node set. Scanning the package rather than one
// file matters: every declaration also implements stmtNode, so statement kinds
// are split across stmt.go and decl.go.
func declaredKinds(t *testing.T, marker string) []string {
	t.Helper()
	dir := filepath.Join(internalDir(t), "frontend", "ast")
	pkgs, err := parser.ParseDir(token.NewFileSet(), dir, func(info os.FileInfo) bool {
		return !strings.HasSuffix(info.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", dir, err)
	}
	kinds := make([]string, 0)
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Name.Name != marker || fn.Recv == nil || len(fn.Recv.List) != 1 {
					continue
				}
				star, ok := fn.Recv.List[0].Type.(*ast.StarExpr)
				if !ok {
					continue
				}
				if name, ok := star.X.(*ast.Ident); ok {
					kinds = append(kinds, name.Name)
				}
			}
		}
	}
	if len(kinds) == 0 {
		t.Fatalf("no types implement %s in %s", marker, dir)
	}
	slices.Sort(kinds)
	return kinds
}

// handledKinds returns the ast.X kinds named by type-switch cases in fn.
func handledKinds(t *testing.T, file, fn string) []string {
	t.Helper()
	path := filepath.Join(internalDir(t), filepath.FromSlash(file))
	parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	handled := make([]string, 0)
	found := false
	ast.Inspect(parsed, func(node ast.Node) bool {
		decl, ok := node.(*ast.FuncDecl)
		if !ok || decl.Name.Name != fn {
			return true
		}
		found = true
		ast.Inspect(decl, func(inner ast.Node) bool {
			clause, ok := inner.(*ast.CaseClause)
			if !ok {
				return true
			}
			for _, expr := range clause.List {
				star, ok := expr.(*ast.StarExpr)
				if !ok {
					continue
				}
				selector, ok := star.X.(*ast.SelectorExpr)
				if !ok {
					continue
				}
				if pkg, ok := selector.X.(*ast.Ident); ok && pkg.Name == "ast" {
					handled = append(handled, selector.Sel.Name)
				}
			}
			return true
		})
		return false
	})
	if !found {
		t.Fatalf("function %s not found in %s", fn, file)
	}
	return handled
}

// Expression dispatch is split between sites that must be total and narrow
// queries such as "which expressions can hold a reference". Only the total sites
// belong in this contract: demanding a declared reason from every predicate for
// all 23 kinds would be pure boilerplate, and a no-op default there defeats the
// exhaustiveness it pretends to add.
var expressionSites = []dispatchSite{
	{file: "semantics/resolver/resolver.go", fn: "resolveExpr"},
	{file: "semantics/typechecker/check_expr.go", fn: "typeExprBase"},
	{file: "semantics/ownership/expr.go", fn: "checkExpr"},
	{
		file: "ir/hir/lower/module_lower.go",
		fn:   "lowerASTExpr",
		omitted: map[string]classification{
			"RangeExpr": {traverse, "a range is only a loop iterable or a slice index, lowered by its parent, never a standalone value"},
		},
	},
}

func assertSitesDecide(t *testing.T, sites []dispatchSite, kinds []string) {
	t.Helper()
	for _, site := range sites {
		t.Run(site.fn, func(t *testing.T) {
			handled := handledKinds(t, site.file, site.fn)
			omitted := site.omissions()
			for _, kind := range kinds {
				if slices.Contains(handled, kind) {
					if entry, declared := omitted[kind]; declared {
						t.Errorf("%s handles %s but still classifies it %s (%q); delete the entry",
							site.fn, kind, entry.decision, entry.reason)
					}
					continue
				}
				if _, declared := omitted[kind]; !declared {
					t.Errorf("%s makes no decision about ast.%s; add a case or declare why the kind is inert",
						site.fn, kind)
				}
			}
		})
	}
}

func TestEveryStatementKindHasAPhaseDecision(t *testing.T) {
	assertSitesDecide(t, statementSites, declaredKinds(t, "stmtNode"))
}

func TestEveryExpressionKindHasAPhaseDecision(t *testing.T) {
	assertSitesDecide(t, expressionSites, declaredKinds(t, "exprNode"))
}

// A reason that no longer names a real statement kind is stale and must not
// silently excuse a future kind of the same name.
func TestOmissionReasonsNameRealNodeKinds(t *testing.T) {
	kinds := append(declaredKinds(t, "stmtNode"), declaredKinds(t, "exprNode")...)
	for _, site := range append(slices.Clone(statementSites), expressionSites...) {
		for kind, entry := range site.omissions() {
			if !slices.Contains(kinds, kind) {
				t.Errorf("%s classifies kind %s that no longer exists", site.fn, kind)
			}
			if strings.TrimSpace(entry.reason) == "" {
				t.Errorf("%s classifies %s as %s without a reason", site.fn, kind, entry.decision)
			}
			if entry.decision.String() == "unknown" {
				t.Errorf("%s classifies %s with an invalid decision", site.fn, kind)
			}
		}
	}
}
