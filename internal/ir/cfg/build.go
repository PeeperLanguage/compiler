package cfg

import (
	"fmt"

	"compiler/internal/frontend/ast"
	"compiler/internal/ir"
	"compiler/internal/source"
)

type builder struct {
	fn     *Graph
	nextID int
}

// BuildModule creates immutable control-flow topology from typed source syntax.
func BuildModule(source *ast.Module) *Module {
	if source == nil {
		return nil
	}
	module := &Module{
		Functions: make([]*Graph, 0),
		byNodeID:  make(map[ir.NodeID]*Graph),
	}
	ast.ForEachDecl(source, func(decl ast.Decl) bool {
		fn, ok := decl.(*ast.FnDecl)
		if !ok || fn == nil || fn.Body == nil {
			return true
		}
		graph := buildFunction(fn)
		finalizeGraph(graph)
		if module.byNodeID[graph.NodeID] != nil {
			panic(fmt.Sprintf("CFG construction: duplicate function NodeID %d", graph.NodeID))
		}
		module.Functions = append(module.Functions, graph)
		module.byNodeID[graph.NodeID] = graph
		return true
	})
	return module
}

func buildFunction(source *ast.FnDecl) *Graph {
	name := ""
	if source.Name != nil {
		name = source.Name.Name
	}
	fn := &Graph{
		NodeID:         ir.NodeID(source.ID()),
		Name:           name,
		Location:       ast.LocOf(source),
		ReturnTypeText: ast.TypeText(source.ReturnType),
		ReturnsValue:   source.ReturnType != nil,
		Blocks:         make([]*Block, 0),
	}
	b := &builder{fn: fn}
	fn.Entry = b.newBlock(BlockNormal, ast.LocOf(source.Body))
	fn.Exit = b.newBlock(BlockNormal, ast.LocOf(source))
	next := b.buildBlock(source.Body, fn.Entry)
	if next != nil && next.Terminator == nil {
		next.Terminator = &Jump{Target: fn.Exit}
	}
	return fn
}

func (b *builder) newBlock(origin BlockOrigin, location *source.Location) *Block {
	block := &Block{ID: b.nextID, Origin: origin, Location: location, Sites: make([]*Site, 0)}
	b.nextID++
	b.fn.Blocks = append(b.fn.Blocks, block)
	return block
}

func (b *builder) buildBlock(block *ast.BlockStmt, current *Block) *Block {
	if b == nil || block == nil {
		return current
	}
	next := current
	scopeID := ir.NodeID(block.ID())
	for _, stmt := range block.Stmts {
		if next == nil {
			next = b.newBlock(BlockNormal, ast.LocOf(stmt))
		}
		next = b.buildStmt(stmt, next, scopeID)
	}
	if next != nil && block.ID() != 0 {
		next.Sites = append(next.Sites, &Site{
			Kind:     SiteScopeExit,
			NodeID:   ir.NodeID(block.ID()),
			ScopeID:  scopeID,
			Location: ast.LocOf(block),
		})
	}
	return next
}

func (b *builder) buildStmt(stmt ast.Stmt, current *Block, scopeID ir.NodeID) *Block {
	switch node := stmt.(type) {
	case nil:
		return current
	case *ast.BlockStmt:
		end := b.buildBlock(node, current)
		if end == nil {
			return nil
		}
		continuation := b.newBlock(BlockNormal, ast.LocOf(node))
		end.Terminator = &Jump{Target: continuation}
		return continuation
	case *ast.LetDecl, *ast.ConstDecl, *ast.ExprStmt, *ast.AssignStmt, *ast.BadStmt:
		current.Sites = append(current.Sites, statementSite(node, scopeID))
		return current
	case *ast.ReturnStmt:
		current.Sites = append(current.Sites, statementSite(node, scopeID))
		current.Terminator = &Return{NodeID: ir.NodeID(node.ID())}
		return nil
	case *ast.IfStmt:
		thenBlock := b.newBlock(BlockThen, ast.LocOf(node))
		elseBlock := b.newBlock(BlockElse, ast.LocOf(node))
		join := b.newBlock(BlockNormal, ast.LocOf(node))
		conditionID := ir.NodeID(0)
		if node.Cond != nil {
			conditionID = ir.NodeID(node.Cond.ID())
		}
		current.Terminator = &Branch{
			NodeID:      ir.NodeID(node.ID()),
			ConditionID: conditionID,
			ScopeID:     scopeID,
			Location:    ast.LocOf(node),
			TrueTarget:  thenBlock,
			FalseTarget: elseBlock,
		}

		thenEnd := b.buildBlock(node.Then, thenBlock)
		thenFallsThrough := thenEnd != nil
		if thenEnd != nil && thenEnd.Terminator == nil {
			thenEnd.Terminator = &Jump{Target: join}
		}

		elseEnd := b.buildStmt(node.Else, elseBlock, scopeID)
		elseFallsThrough := elseEnd != nil
		if elseEnd != nil && elseEnd.Terminator == nil {
			elseEnd.Terminator = &Jump{Target: join}
		}

		if !thenFallsThrough && !elseFallsThrough {
			return nil
		}
		return join
	case *ast.ForStmt:
		if node.Cond == nil {
			bodyBlock := b.newBlock(BlockLoopBody, ast.LocOf(node))
			current.Terminator = &Jump{Target: bodyBlock}
			bodyEnd := b.buildBlock(node.Body, bodyBlock)
			if bodyEnd != nil && bodyEnd.Terminator == nil {
				bodyEnd.Terminator = &Jump{Target: bodyBlock}
			}
			return nil
		}
		header := b.newBlock(BlockLoop, ast.LocOf(node))
		bodyBlock := b.newBlock(BlockLoopBody, ast.LocOf(node))
		exit := b.newBlock(BlockNormal, ast.LocOf(node))
		current.Terminator = &Jump{Target: header}
		header.Terminator = &Branch{
			NodeID:      ir.NodeID(node.ID()),
			ConditionID: ir.NodeID(node.Cond.ID()),
			ScopeID:     scopeID,
			Location:    ast.LocOf(node),
			TrueTarget:  bodyBlock,
			FalseTarget: exit,
		}
		bodyEnd := b.buildBlock(node.Body, bodyBlock)
		if bodyEnd != nil && bodyEnd.Terminator == nil {
			bodyEnd.Terminator = &Jump{Target: header}
		}
		return exit
	case *ast.BadDecl, *ast.ImportDecl, *ast.FnDecl, *ast.TypeAliasDecl,
		*ast.StructDecl, *ast.InterfaceDecl, *ast.EnumDecl:
		current.Sites = append(current.Sites, statementSite(node, scopeID))
		return current
	default:
		panic(fmt.Sprintf("CFG construction: unhandled AST statement %T", stmt))
	}
}

func statementSite(node ast.Node, scopeID ir.NodeID) *Site {
	return &Site{
		Kind:     SiteStatement,
		NodeID:   ir.NodeID(node.ID()),
		ScopeID:  scopeID,
		Location: ast.LocOf(node),
	}
}

func finalizeGraph(fn *Graph) {
	if fn == nil || fn.Entry == nil {
		return
	}
	for _, block := range fn.Blocks {
		if block != nil {
			block.Reachable = false
		}
	}
	markReachable(fn.Entry, make(map[int]bool))
	rebuildPredecessors(fn)
	finalizeSites(fn)
}

func finalizeSites(fn *Graph) {
	for _, block := range fn.Blocks {
		if block == nil {
			continue
		}
		if branch, ok := block.Terminator.(*Branch); ok {
			block.Sites = append(block.Sites, &Site{
				Kind:     SiteTerminator,
				NodeID:   branch.NodeID,
				ScopeID:  branch.ScopeID,
				Location: branch.Location,
			})
		}
		if len(block.Sites) == 0 {
			block.Sites = append(block.Sites, &Site{Kind: SiteJoin})
		}
		for index, site := range block.Sites {
			site.ID = SiteID{Block: block.ID, Index: index}
			site.Successors = nil
			site.Predecessors = nil
		}
	}
	for _, block := range fn.Blocks {
		if block == nil || len(block.Sites) == 0 {
			continue
		}
		for index := 0; index+1 < len(block.Sites); index++ {
			connectSites(block.Sites[index], block.Sites[index+1], EdgeNormal)
		}
		last := block.Sites[len(block.Sites)-1]
		switch term := block.Terminator.(type) {
		case *Jump:
			connectBlockSite(last, term.Target, EdgeNormal)
		case *Branch:
			connectBlockSite(last, term.TrueTarget, EdgeTrue)
			connectBlockSite(last, term.FalseTarget, EdgeFalse)
		case *Return:
			connectBlockSite(last, fn.Exit, EdgeReturn)
		case nil:
		default:
			panic(fmt.Sprintf("CFG finalization: unhandled terminator %T", block.Terminator))
		}
	}
}

func connectBlockSite(from *Site, target *Block, kind EdgeKind) {
	if target == nil || len(target.Sites) == 0 {
		return
	}
	connectSites(from, target.Sites[0], kind)
}

func connectSites(from, to *Site, kind EdgeKind) {
	if from == nil || to == nil {
		return
	}
	for _, existing := range from.Successors {
		if existing.To == to.ID && existing.Kind == kind {
			return
		}
	}
	edge := Edge{From: from.ID, To: to.ID, Kind: kind}
	from.Successors = append(from.Successors, edge)
	to.Predecessors = append(to.Predecessors, edge)
}

func markReachable(block *Block, seen map[int]bool) {
	if block == nil || seen[block.ID] {
		return
	}
	seen[block.ID] = true
	block.Reachable = true
	if block.Terminator == nil {
		return
	}
	for _, successor := range block.Terminator.Successors() {
		markReachable(successor, seen)
	}
}

func rebuildPredecessors(fn *Graph) {
	for _, block := range fn.Blocks {
		if block != nil {
			block.Predecessors = nil
		}
	}
	for _, block := range fn.Blocks {
		if block == nil || block.Terminator == nil {
			continue
		}
		for _, successor := range block.Terminator.Successors() {
			if successor != nil {
				successor.Predecessors = append(successor.Predecessors, block)
			}
		}
	}
}
