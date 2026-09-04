package effect

import (
	"fmt"

	"compiler/internal/frontend/ast"
	"compiler/internal/ir/cfg"
	"compiler/internal/semantics/symbols"
)

// BuildQueries supplies the published facts this producer needs. Like
// cfg.BuildQueries, it declares narrow accessors so this package does not
// import the artifacts that own them.
type BuildQueries struct {
	// Symbols resolves a referenced identifier to its binding. The resolver
	// indexes references only, so a declaration name and an assignment target
	// are absent here and resolve through Scopes instead.
	Symbols map[ast.NodeID]*symbols.Symbol
	// Scopes resolves a CFG site's lexical scope, which is how a definition
	// reaches its symbol. Definitions and references genuinely use two different
	// mechanisms today; this producer reproduces that rather than changing it.
	Scopes map[ast.NodeID]*symbols.Scope
	// CallArguments returns a call's effective arguments, including any the
	// typechecker expanded from a default.
	CallArguments func(*ast.CallExpr) []ast.Expr
	// ArmBindings returns the payload symbols one match arm binds.
	ArmBindings func(match ast.NodeID, caseIndex int) []*symbols.Symbol
}

// Build publishes the semantic effects of every reachable CFG site.
//
// This is the only place that inspects syntax to decide what a construct does
// to a binding. It runs after flow typing so published evidence is final.
func Build(graphs *cfg.Module, nodes map[ast.NodeID]ast.Node, queries BuildQueries) Result {
	if graphs == nil || queries.Symbols == nil || queries.Scopes == nil {
		return nil
	}
	result := make(Result, len(graphs.Functions))
	for _, graph := range graphs.Functions {
		if graph == nil {
			continue
		}
		fn, _ := nodes[ast.NodeID(graph.NodeID)].(*ast.FnDecl)
		if fn == nil {
			continue
		}
		b := &builder{nodes: nodes, queries: queries, graph: graph, ops: make(map[cfg.SiteID][]Op)}
		b.buildFunction(fn)
		result[graph.NodeID] = b.ops
	}
	return result
}

type builder struct {
	nodes   map[ast.NodeID]ast.Node
	queries BuildQueries
	graph   *cfg.Graph
	ops     map[cfg.SiteID][]Op
}

func (b *builder) emit(site cfg.SiteID, op Op) {
	b.ops[site] = append(b.ops[site], op)
}

func (b *builder) symbolOf(id ast.NodeID) *symbols.Symbol {
	return b.queries.Symbols[id]
}

func (b *builder) buildFunction(fn *ast.FnDecl) {
	if b.graph.Entry == nil || len(b.graph.Entry.Sites) == 0 {
		return
	}
	entry := b.graph.Entry.Sites[0].ID
	if fn.Body != nil {
		functionScope := b.queries.Scopes[fn.Body.ID()]
		for _, param := range fn.ParamsWithReceiver() {
			if param.Name == nil {
				continue
			}
			sym, found := functionScope.Lookup(param.Name.Name)
			if !found || sym == nil {
				continue
			}
			b.emit(entry, Define{Symbol: sym, Node: param.Name.ID(), Initialized: true})
		}
	}
	for _, block := range b.graph.Blocks {
		if block == nil || !block.Reachable {
			continue
		}
		for _, site := range block.Sites {
			if site == nil {
				continue
			}
			b.buildSite(block, site)
		}
	}
}

func (b *builder) buildSite(block *cfg.Block, site *cfg.Site) {
	stmt, _ := b.nodes[ast.NodeID(site.NodeID)].(ast.Stmt)
	if stmt != nil {
		b.buildStmt(site.ID, b.queries.Scopes[ast.NodeID(site.ScopeID)], stmt)
	}
	if site.Kind != cfg.SiteTerminator {
		return
	}
	switch terminator := block.Terminator.(type) {
	case *cfg.Branch:
		if condition, ok := b.nodes[ast.NodeID(terminator.ConditionID)].(ast.Expr); ok {
			b.reads(site.ID, condition)
		}
	case *cfg.SwitchVariant:
		b.buildMatchArms(site, terminator)
	case *cfg.Jump, *cfg.Return:
		// A jump carries no condition, and a return's value is read at the
		// return statement's own site.
	default:
		panic(fmt.Sprintf("effect: unhandled CFG terminator %T", block.Terminator))
	}
}

// buildMatchArms publishes each arm's payload bindings at the first site of
// that arm's body. CFG construction gives every arm a fresh block reached only
// by its own case edge, so a site-keyed define is equivalent to attaching it to
// the edge.
func (b *builder) buildMatchArms(site *cfg.Site, terminator *cfg.SwitchVariant) {
	if b.queries.ArmBindings == nil {
		return
	}
	for _, edge := range site.Successors {
		if edge.Kind != cfg.EdgeVariantCase {
			continue
		}
		for _, sym := range b.queries.ArmBindings(ast.NodeID(terminator.NodeID), edge.Case) {
			if sym == nil {
				continue
			}
			b.emit(edge.To, Define{Symbol: sym, Node: ast.NodeID(terminator.NodeID), Initialized: true})
		}
	}
}

// buildStmt publishes one statement's effects in evaluation order.
//
// CFG construction panics on a statement it does not place, and this is now the
// single producer of statement meaning, so it takes the same policy: a new kind
// must be handled here or declared inert in internal/contracts.
func (b *builder) buildStmt(site cfg.SiteID, scope *symbols.Scope, stmt ast.Stmt) {
	switch node := stmt.(type) {
	case *ast.LetDecl:
		b.buildBinding(site, scope, node, node.Value)
	case *ast.ConstDecl:
		b.buildBinding(site, scope, node, node.Value)
	case *ast.AssignStmt:
		b.reads(site, node.Value)
		ident, direct := node.Target.(*ast.Ident)
		if !direct || ident == nil {
			b.reads(site, node.Target)
			return
		}
		sym, found := scope.Lookup(ident.Name)
		if found && sym != nil {
			b.emit(site, Write{Symbol: sym, Node: ident.ID()})
		}
	case *ast.ExprStmt:
		b.reads(site, node.Expr)
	case *ast.ReturnStmt:
		b.reads(site, node.Value)
	case *ast.IfStmt, *ast.ForStmt, *ast.MatchStmt:
		// Control-flow statements reach this producer at their terminator site.
		// The condition or subject is published there, from the terminator, so
		// that a site carries exactly the reads that happen at it.
	case *ast.BlockStmt:
		// Blocks are decomposed by CFG construction; a scope-exit site names one
		// but evaluates nothing.
	case *ast.BreakStmt, *ast.ContinueStmt:
		// A transfer is a CFG edge and evaluates no expression.
	case *ast.BadStmt, *ast.BadDecl:
		// Recovery nodes carry no evidence.
	case *ast.ImportDecl, *ast.FnDecl, *ast.TypeAliasDecl,
		*ast.StructDecl, *ast.InterfaceDecl, *ast.EnumDecl:
		// Declarations reach a CFG site after the resolver reports them as
		// unsupported statements. They evaluate nothing here.
	default:
		panic(fmt.Sprintf("effect: unhandled AST statement %T", stmt))
	}
}

// buildBinding publishes a declaration's initializer reads before the define
// they initialize, so `let x = x` reads an outer binding rather than itself.
func (b *builder) buildBinding(site cfg.SiteID, scope *symbols.Scope, decl ast.Stmt, value ast.Expr) {
	b.reads(site, value)
	if scope == nil {
		return
	}
	sym, found := scope.LookupNode(decl)
	if !found || sym == nil {
		return
	}
	b.emit(site, Define{Symbol: sym, Node: decl.ID(), Initialized: value != nil})
}

// reads publishes one Use per identifier the expression evaluates, in traversal
// order. Call arguments come from published call evidence so that arguments the
// typechecker expanded from defaults are covered.
func (b *builder) reads(site cfg.SiteID, expr ast.Expr) {
	if expr == nil {
		return
	}
	var walk func(ast.Expr)
	walk = func(current ast.Expr) {
		if current == nil {
			return
		}
		ast.Inspect(current, func(node ast.Node) bool {
			if call, ok := node.(*ast.CallExpr); ok && call != nil {
				walk(call.Callee)
				for _, arg := range b.callArguments(call) {
					walk(arg)
				}
				return false
			}
			ident, ok := node.(*ast.Ident)
			if !ok || ident == nil {
				return true
			}
			if sym := b.symbolOf(ident.ID()); sym != nil {
				b.emit(site, Use{Symbol: sym, Node: ident.ID()})
			}
			return true
		})
	}
	walk(expr)
}

func (b *builder) callArguments(call *ast.CallExpr) []ast.Expr {
	if b.queries.CallArguments == nil {
		return call.Args
	}
	return b.queries.CallArguments(call)
}
