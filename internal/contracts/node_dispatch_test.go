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

// dispatchSite is one function that switches over ast.Stmt.
type dispatchSite struct {
	file string
	fn   string
	// omitted lists statement kinds this site intentionally does not handle,
	// each with the reason it is safe. An empty list means fully exhaustive.
	omitted map[string]string
	// inertDeclarations adds the shared parser-rejected declaration reason. Set
	// it on sites that only ever see statements the parser actually produces
	// inside a block, rather than repeating one rule at every such site.
	inertDeclarations bool
}

// Declarations all implement stmtNode, but the parser rejects every one except
// a binding inside a block with "unsupported statement" (P0004). They therefore
// never reach a CFG site or a lowering position in a well-formed tree.
var parserRejectedInStatementPosition = []string{
	"ImportDecl", "FnDecl", "TypeAliasDecl", "StructDecl",
	"InterfaceDecl", "EnumDecl", "BadDecl",
}

const parserRejectedReason = "parser rejects this declaration in statement position (P0004)"

// omissions returns the site's declared omissions including the shared rule.
func (d dispatchSite) omissions() map[string]string {
	merged := make(map[string]string, len(d.omitted)+len(parserRejectedInStatementPosition))
	for kind, reason := range d.omitted {
		merged[kind] = reason
	}
	if d.inertDeclarations {
		for _, kind := range parserRejectedInStatementPosition {
			if _, declared := merged[kind]; !declared {
				merged[kind] = parserRejectedReason
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
		omitted: map[string]string{
			"BreakStmt":    "parser only produces a block or else-if in else position",
			"ContinueStmt": "parser only produces a block or else-if in else position",
			"MatchStmt":    "parser only produces a block or else-if in else position",
		},
	},
	// The remaining sites run per CFG site, where control flow is already
	// decomposed into blocks and edges. They extract the expressions a statement
	// evaluates at that site, so statements carrying no expression are inert.
	{
		file:              "semantics/ownership/ownership.go",
		fn:                "applyStmt",
		inertDeclarations: true,
		omitted: map[string]string{
			"BlockStmt":    "blocks are decomposed by CFG construction and are never a site statement",
			"BadStmt":      "recovery node carries no ownership effect",
			"BreakStmt":    "transfer is a CFG edge, not a site-level ownership effect",
			"ContinueStmt": "transfer is a CFG edge, not a site-level ownership effect",
		},
	},
	{
		file:              "semantics/ownership/reference.go",
		fn:                "symbolUseSequence",
		inertDeclarations: true,
		omitted: map[string]string{
			"BlockStmt":    "blocks are decomposed by CFG construction and are never a site statement",
			"BadStmt":      "recovery node evaluates no expression",
			"BreakStmt":    "evaluates no expression",
			"ContinueStmt": "evaluates no expression",
		},
	},
	{
		file:              "semantics/definiteinit/initialization.go",
		fn:                "checkReads",
		inertDeclarations: true,
		omitted: map[string]string{
			"BlockStmt":    "blocks are decomposed by CFG construction and are never a site statement",
			"BadStmt":      "recovery node reads nothing",
			"IfStmt":       "condition arrives separately through the CFG site condition",
			"ForStmt":      "condition arrives separately through the CFG site condition",
			"MatchStmt":    "subject arrives separately through the CFG site condition",
			"BreakStmt":    "reads nothing",
			"ContinueStmt": "reads nothing",
		},
	},
	{
		file:              "semantics/typechecker/flow.go",
		fn:                "applyConditionEdge",
		inertDeclarations: true,
		omitted: map[string]string{
			"BlockStmt":    "carries no branch condition",
			"ExprStmt":     "carries no branch condition",
			"AssignStmt":   "carries no branch condition",
			"ReturnStmt":   "carries no branch condition",
			"BadStmt":      "carries no branch condition",
			"BreakStmt":    "carries no branch condition",
			"ContinueStmt": "carries no branch condition",
			"MatchStmt":    "match narrowing uses case tests, not a true/false condition edge",
			"LetDecl":      "carries no branch condition",
			"ConstDecl":    "carries no branch condition",
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

func declaredStatementKinds(t *testing.T) []string {
	t.Helper()
	return declaredKinds(t, "stmtNode")
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
		omitted: map[string]string{
			"RangeExpr": "a range is only a loop iterable or a slice index, lowered by its parent, never a standalone value",
		},
	},
}

func declaredExpressionKinds(t *testing.T) []string {
	t.Helper()
	return declaredKinds(t, "exprNode")
}

func assertSitesDecide(t *testing.T, sites []dispatchSite, kinds []string) {
	t.Helper()
	for _, site := range sites {
		t.Run(site.fn, func(t *testing.T) {
			handled := handledKinds(t, site.file, site.fn)
			omitted := site.omissions()
			for _, kind := range kinds {
				if slices.Contains(handled, kind) {
					if reason, declared := omitted[kind]; declared {
						t.Errorf("%s handles %s but still declares it omitted (%q); delete the entry",
							site.fn, kind, reason)
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
	assertSitesDecide(t, statementSites, declaredStatementKinds(t))
}

func TestEveryExpressionKindHasAPhaseDecision(t *testing.T) {
	assertSitesDecide(t, expressionSites, declaredExpressionKinds(t))
}

// A reason that no longer names a real statement kind is stale and must not
// silently excuse a future kind of the same name.
func TestOmissionReasonsNameRealNodeKinds(t *testing.T) {
	kinds := append(declaredStatementKinds(t), declaredExpressionKinds(t)...)
	for _, site := range append(slices.Clone(statementSites), expressionSites...) {
		for kind, reason := range site.omissions() {
			if !slices.Contains(kinds, kind) {
				t.Errorf("%s declares omitted kind %s that no longer exists", site.fn, kind)
			}
			if strings.TrimSpace(reason) == "" {
				t.Errorf("%s omits %s without a reason", site.fn, kind)
			}
		}
	}
}
