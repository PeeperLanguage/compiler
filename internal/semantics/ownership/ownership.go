package ownership

import (
	"fmt"

	"compiler/internal/frontend/ast"
	"compiler/internal/graph"
	"compiler/internal/project"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/table"
	"compiler/internal/semantics/typeinfo"
)

type nodeKind uint8

const (
	nodeEntry nodeKind = iota
	nodeStmt
	nodeExit
)

const (
	graphNodeFlow graph.NodeKind = "ownership_flow"
	graphEdgeFlow graph.EdgeKind = "ownership_flow"
)

type flowNode struct {
	id    graph.NodeID
	kind  nodeKind
	stmt  ast.Stmt
	scope *table.Scope
}

type flow struct {
	graph *graph.Graph
	nodes map[graph.NodeID]*flowNode
	order []graph.NodeID
	next  int
	entry graph.NodeID
	exit  graph.NodeID
}

type builder struct {
	module *project.Module
	flow   *flow
}

type analyzer struct {
	ctx    *project.CompilerContext
	module *project.Module
	flow   *flow
}

type state map[*symbols.Symbol]ast.Node

// CheckFunction runs ownership flow after ordinary typechecking has populated
// expression types and scopes. It keeps move state local to this pass so the
// typechecker remains a type phase, not a data-flow engine.
func CheckFunction(ctx *project.CompilerContext, module *project.Module, fn *ast.FnDecl, scope *table.Scope, returnType typeinfo.Type) {
	if ctx == nil || module == nil || module.Semantics == nil || fn == nil || fn.Body == nil || scope == nil {
		return
	}
	f := build(module, fn.Body, scope)
	(&analyzer{ctx: ctx, module: module, flow: f}).run()
}

func build(module *project.Module, body *ast.BlockStmt, scope *table.Scope) *flow {
	f := &flow{
		graph: graph.New(graphNodeFlow, graphEdgeFlow),
		nodes: make(map[graph.NodeID]*flowNode),
		order: make([]graph.NodeID, 0),
	}
	b := &builder{module: module, flow: f}
	entry := b.newNode(nodeEntry, nil, scope)
	exit := b.newNode(nodeExit, nil, scope)
	f.entry = entry.id
	f.exit = exit.id
	tails := b.buildBlock([]graph.NodeID{entry.id}, body, scope)
	b.connectAll(tails, exit.id)
	return f
}

func (b *builder) newNode(kind nodeKind, stmt ast.Stmt, scope *table.Scope) *flowNode {
	id := graph.NodeID(fmt.Sprintf("ownership:%d", b.flow.next))
	b.flow.next++
	node := &flowNode{id: id, kind: kind, stmt: stmt, scope: scope}
	b.flow.graph.AddNode(id)
	b.flow.nodes[id] = node
	b.flow.order = append(b.flow.order, id)
	return node
}

func (b *builder) connect(from, to graph.NodeID) {
	if from == "" || to == "" {
		return
	}
	b.flow.graph.AddEdge(from, to)
}

func (b *builder) connectAll(from []graph.NodeID, to graph.NodeID) {
	for _, id := range from {
		b.connect(id, to)
	}
}

func (b *builder) blockScope(block *ast.BlockStmt, fallback *table.Scope) *table.Scope {
	if b == nil || b.module == nil || b.module.Semantics == nil || block == nil {
		return fallback
	}
	if scope, ok := b.module.Semantics.BlockScopes[block.ID()]; ok && scope != nil {
		return scope
	}
	return fallback
}

func (b *builder) buildBlock(in []graph.NodeID, block *ast.BlockStmt, fallback *table.Scope) []graph.NodeID {
	if block == nil {
		return in
	}
	scope := b.blockScope(block, fallback)
	tails := in
	for _, stmt := range block.Stmts {
		tails = b.buildStmt(tails, stmt, scope)
		if len(tails) == 0 {
			break
		}
	}
	return tails
}

func (b *builder) buildStmt(in []graph.NodeID, stmt ast.Stmt, scope *table.Scope) []graph.NodeID {
	if stmt == nil {
		return in
	}
	switch s := stmt.(type) {
	case *ast.BlockStmt:
		return b.buildBlock(in, s, scope)
	case *ast.IfStmt:
		node := b.newNode(nodeStmt, stmt, scope)
		b.connectAll(in, node.id)
		join := b.newNode(nodeStmt, nil, scope)
		thenTails := b.buildBlock([]graph.NodeID{node.id}, s.Then, scope)
		b.connectAll(thenTails, join.id)
		if s.Else != nil {
			elseTails := b.buildStmt([]graph.NodeID{node.id}, s.Else, scope)
			b.connectAll(elseTails, join.id)
		} else {
			b.connect(node.id, join.id)
		}
		return []graph.NodeID{join.id}
	case *ast.ForStmt:
		header := b.newNode(nodeStmt, stmt, scope)
		b.connectAll(in, header.id)
		after := b.newNode(nodeStmt, nil, scope)
		bodyTails := b.buildBlock([]graph.NodeID{header.id}, s.Body, scope)
		b.connectAll(bodyTails, header.id)
		b.connect(header.id, after.id)
		return []graph.NodeID{after.id}
	case *ast.ReturnStmt:
		node := b.newNode(nodeStmt, stmt, scope)
		b.connectAll(in, node.id)
		b.connect(node.id, b.flow.exit)
		return nil
	default:
		node := b.newNode(nodeStmt, stmt, scope)
		b.connectAll(in, node.id)
		return []graph.NodeID{node.id}
	}
}

func (a *analyzer) run() {
	if a == nil || a.flow == nil || a.flow.graph == nil || a.flow.entry == "" {
		return
	}
	in := map[graph.NodeID]state{a.flow.entry: make(state)}
	queue := []graph.NodeID{a.flow.entry}
	queued := map[graph.NodeID]bool{a.flow.entry: true}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		queued[id] = false
		node := a.flow.nodes[id]
		next := copyState(in[id])
		if node != nil && node.kind == nodeStmt && node.stmt != nil {
			a.applyStmt(node.scope, node.stmt, next)
		}
		for _, succ := range a.flow.graph.Successors(id) {
			merged, changed := mergeState(in[succ], next)
			if !changed {
				continue
			}
			in[succ] = merged
			if !queued[succ] {
				queue = append(queue, succ)
				queued[succ] = true
			}
		}
	}
}

func copyState(src state) state {
	dst := make(state, len(src))
	for sym, site := range src {
		dst[sym] = site
	}
	return dst
}

func mergeState(dst, src state) (state, bool) {
	if dst == nil {
		if len(src) == 0 {
			return make(state), true
		}
		return copyState(src), true
	}
	changed := false
	for sym, site := range src {
		if _, ok := dst[sym]; ok {
			continue
		}
		dst[sym] = site
		changed = true
	}
	return dst, changed
}

func (a *analyzer) applyStmt(scope *table.Scope, stmt ast.Stmt, st state) {
	switch s := stmt.(type) {
	case *ast.LetDecl:
		a.checkExpr(scope, s.Value, st, useCopy)
	case *ast.ConstDecl:
		a.checkExpr(scope, s.Value, st, useCopy)
	case *ast.AssignStmt:
		if _, ok := s.Target.(*ast.Ident); !ok {
			a.checkExpr(scope, s.Target, st, useRead)
		}
		a.checkExpr(scope, s.Value, st, useCopy)
		if target, ok := s.Target.(*ast.Ident); ok && scope != nil {
			if sym, found := scope.Lookup(target.Name); found && ownershipTrackedSymbol(sym) {
				delete(st, sym)
			}
		}
	case *ast.ReturnStmt:
		a.checkExpr(scope, s.Value, st, useConsume)
	case *ast.ExprStmt:
		a.checkExpr(scope, s.Expr, st, useRead)
	case *ast.IfStmt:
		a.checkExpr(scope, s.Cond, st, useRead)
	case *ast.ForStmt:
		a.checkExpr(scope, s.Cond, st, useRead)
	}
}
