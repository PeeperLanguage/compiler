// Package contracts owns the phase-coverage contract for AST node handling.
//
// Child-field completeness is enforced separately in child_traversal_test.go.
// These tests cover the semantic layer: they read the real sources and compare
// every declared statement and expression kind against every phase that
// dispatches on them, so an omission fails immediately instead of surfacing as
// wrong output much later.
package contracts

import (
	"go/ast"
	"go/parser"
	"go/token"
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
	// contextual: the parent construct owns the node's handling; reaching this
	// dispatcher directly is an internal invariant violation.
	contextual
)

func (d decision) String() string {
	switch d {
	case traverse:
		return "traverse"
	case ignore:
		return "ignore"
	case reject:
		return "reject"
	case contextual:
		return "contextual"
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
	// inertDeclarations adds the shared declaration-statement classifications.
	// Set it on CFG-site consumers rather than repeating one rule at each.
	inertDeclarations bool
}

// Declarations all implement stmtNode, and parseStmt really does build FnDecl,
// StructDecl, InterfaceDecl, EnumDecl and TypeAliasDecl nodes inside a block.
// The resolver reports them as unsupported statements and HIR lowers them to
// hir.Invalid, but CFG through ownership are not error-gated, so the nodes do
// reach those sites. They are skipped because a declaration evaluates no
// expression and performs no assignment at a CFG site, not because they are
// absent. ImportDecl is only produced at module level and BadDecl is never
// produced by the parser; both are tolerated for synthetic trees.
var declarationStatements = map[string]classification{
	"FnDecl":        {ignore, "reaches this site after the resolver reports an unsupported statement, and evaluates no expression here"},
	"TypeAliasDecl": {ignore, "reaches this site after the resolver reports an unsupported statement, and evaluates no expression here"},
	"StructDecl":    {ignore, "reaches this site after the resolver reports an unsupported statement, and evaluates no expression here"},
	"InterfaceDecl": {ignore, "reaches this site after the resolver reports an unsupported statement, and evaluates no expression here"},
	"EnumDecl":      {ignore, "reaches this site after the resolver reports an unsupported statement, and evaluates no expression here"},
	"ImportDecl":    {ignore, "the parser only produces import declarations at module level, never as a block statement"},
	"BadDecl":       {ignore, "the parser never produces BadDecl; it is tolerated for synthetic trees"},
}

const (
	decomposedByCFGReason = "blocks are decomposed by CFG construction and are never a site statement"
	elsePositionReason    = "parseIfStmt produces only a block or else-if in else position; anything else is an internal invariant violation (the default panics)"
	noConditionReason     = "carries no branch condition"
)

// omissions returns the site's declared classifications including the shared
// declaration-statement rule.
func (d dispatchSite) omissions() map[string]classification {
	merged := make(map[string]classification, len(d.omitted)+len(declarationStatements))
	for kind, entry := range d.omitted {
		merged[kind] = entry
	}
	if d.inertDeclarations {
		for kind, entry := range declarationStatements {
			if _, declared := merged[kind]; !declared {
				merged[kind] = entry
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
	// The effect producer replaced definiteinit.checkReads as the site that reads
	// meaning out of a statement. It is exhaustive: every kind has a case, so it
	// declares no omissions, and a new kind fails here first.
	{file: "semantics/effect/build.go", fn: "publishStmt"},
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

// declaredKinds returns every AST type implementing the marker method, tracked
// through the shared AST package loader so both contracts see one node set.
func declaredKinds(t *testing.T, marker string) []string {
	t.Helper()
	pkg := loadASTPackage(t)
	kinds := slices.Clone(pkg.markers[marker])
	if len(kinds) == 0 {
		t.Fatalf("no types implement %s in the ast package", marker)
	}
	slices.Sort(kinds)
	return kinds
}

// handledKinds returns the ast.X kinds named by type-switch cases whose operand
// is the function's statement/expression input. That input is either a direct
// parameter, a CFG-site payload selected from a parameter (node.stmt), or a
// local extracted from a site through a comma-ok assertion to ast.Stmt.
// Switches on other derived values (a callee extracted from an expression, a
// loop variable) are a different decision and are excluded, so an unrelated
// nested switch cannot fake coverage.
func handledKinds(t *testing.T, file, fn string) []string {
	t.Helper()
	path := filepath.Join(internalDir(t), filepath.FromSlash(file))
	parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	decl := findFuncDecl(parsed, fn)
	if decl == nil {
		t.Fatalf("function %s not found in %s", fn, file)
	}
	params := paramNames(decl)
	stmtLocals := stmtAssertionLocals(decl)
	astLocal := importLocalName(parsed, "compiler/internal/frontend/ast")
	handled := make([]string, 0)
	ast.Inspect(decl.Body, func(node ast.Node) bool {
		switchStmt, ok := node.(*ast.TypeSwitchStmt)
		if !ok {
			return true
		}
		// Do not descend: switches on derived values inside this switch are not
		// part of this function's dispatch contract.
		if isDispatchOperand(switchOperandExpr(switchStmt), params, stmtLocals) {
			for _, stmt := range switchStmt.Body.List {
				clause, ok := stmt.(*ast.CaseClause)
				if !ok {
					continue
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
					if pkg, ok := selector.X.(*ast.Ident); ok && pkg.Name == astLocal {
						handled = append(handled, selector.Sel.Name)
					}
				}
			}
		}
		return false
	})
	return handled
}

// isDispatchOperand reports whether a type-switch operand is the function's
// statement/expression input under the rule documented on handledKinds.
func isDispatchOperand(operand ast.Expr, params, stmtLocals map[string]bool) bool {
	if operand == nil {
		return false
	}
	switch typed := operand.(type) {
	case *ast.Ident:
		return params[typed.Name] || stmtLocals[typed.Name]
	case *ast.SelectorExpr:
		base, ok := typed.X.(*ast.Ident)
		return ok && params[base.Name] && typed.Sel.Name == "stmt"
	}
	return false
}

// stmtAssertionLocals returns locals assigned through a comma-ok assertion to
// ast.Stmt, the canonical CFG-site statement extraction.
func stmtAssertionLocals(fn *ast.FuncDecl) map[string]bool {
	locals := map[string]bool{}
	ast.Inspect(fn.Body, func(node ast.Node) bool {
		assign, ok := node.(*ast.AssignStmt)
		if !ok || len(assign.Lhs) != 2 || len(assign.Rhs) != 1 {
			return true
		}
		assert, ok := assign.Rhs[0].(*ast.TypeAssertExpr)
		if !ok {
			return true
		}
		selector, ok := assert.Type.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := selector.X.(*ast.Ident)
		if !ok || pkg.Name != "ast" || selector.Sel.Name != "Stmt" {
			return true
		}
		if ident, ok := assign.Lhs[0].(*ast.Ident); ok {
			locals[ident.Name] = true
		}
		return true
	})
	return locals
}

func findFuncDecl(file *ast.File, name string) *ast.FuncDecl {
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == name {
			return fn
		}
	}
	return nil
}

func paramNames(fn *ast.FuncDecl) map[string]bool {
	params := map[string]bool{}
	if fn.Type.Params == nil {
		return params
	}
	for _, field := range fn.Type.Params.List {
		for _, name := range field.Names {
			params[name.Name] = true
		}
	}
	return params
}

// switchOperandExpr returns the expression a type switch asserts on, for both
// `switch x := y.(type)` and `switch y.(type)` forms.
func switchOperandExpr(switchStmt *ast.TypeSwitchStmt) ast.Expr {
	switch assign := switchStmt.Assign.(type) {
	case *ast.AssignStmt:
		if len(assign.Rhs) != 1 {
			return nil
		}
		assert, ok := assign.Rhs[0].(*ast.TypeAssertExpr)
		if !ok {
			return nil
		}
		return assert.X
	case *ast.ExprStmt:
		assert, ok := assign.X.(*ast.TypeAssertExpr)
		if !ok {
			return nil
		}
		return assert.X
	}
	return nil
}

// importLocalName resolves the local name the file uses for the package at the
// given import path, so the case-type filter does not hardcode an alias.
func importLocalName(file *ast.File, importPath string) string {
	for _, imp := range file.Imports {
		if strings.Trim(imp.Path.Value, `"`) != importPath {
			continue
		}
		if imp.Name != nil {
			return imp.Name.Name
		}
		break
	}
	// Unaliased imports bind the package name, which is the path's last segment.
	if index := strings.LastIndex(importPath, "/"); index >= 0 {
		return importPath[index+1:]
	}
	return importPath
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
			"RangeExpr": {contextual, "a range is lowered by its parent construct (loop iterable or slice index); this dispatcher must never receive one directly"},
		},
	},
}

// Type syntax reaches two phases that must decide per kind, and both failures
// are silent: an unresolved kind becomes an invalid type rather than an error,
// and a kind missing from declaration-cycle detection hides a recursive type
// until layout recurses forever. Parsing and printing walk type syntax too, but
// they consume the node's own TypeText rather than dispatching on its kind.
var typeSites = []dispatchSite{
	{file: "semantics/typeinfo/syntax.go", fn: "TypeFromSyntax"},
	{file: "semantics/binder/type_decl_cycles.go", fn: "addTypeDeclEdges"},
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

func TestEveryTypeKindHasAPhaseDecision(t *testing.T) {
	assertSitesDecide(t, typeSites, declaredKinds(t, "typeNode"))
}

// A reason that no longer names a real statement kind is stale and must not
// silently excuse a future kind of the same name.
func TestOmissionReasonsNameRealNodeKinds(t *testing.T) {
	kinds := append(declaredKinds(t, "stmtNode"), declaredKinds(t, "exprNode")...)
	kinds = append(kinds, declaredKinds(t, "typeNode")...)
	sites := slices.Concat(statementSites, expressionSites, typeSites)
	for _, site := range sites {
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
