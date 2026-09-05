package cfg

import (
	"fmt"

	"compiler/internal/frontend/ast"
	graphcore "compiler/internal/graph"
	"compiler/internal/ir"
	"compiler/internal/source"
)

type builder struct {
	fn      *Graph
	queries BuildQueries
	nextID  int
	scopes  []*ast.BlockStmt
	loops   []loopContext
}

type loopContext struct {
	continueTarget *Block
	breakTarget    *Block
	scopeDepth     int
}

// MatchCaseQuery supplies typechecker-resolved case indexes in source arm order.
type MatchCaseQuery func(ast.NodeID) ([]int, bool)

// LoopEntryQuery supplies typechecker proof that a loop executes its body before
// its first condition check.
type LoopEntryQuery func(ast.NodeID) bool

// BuildQueries are semantic facts required to construct truthful CFG topology.
type BuildQueries struct {
	MatchCases          MatchCaseQuery
	LoopGuaranteedEntry LoopEntryQuery
}

// BuildModule creates immutable control-flow topology from typed source syntax.
func BuildModule(source *ast.Module, queries BuildQueries) *Module {
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
		graph := buildFunction(fn, queries)
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

func buildFunction(source *ast.FnDecl, queries BuildQueries) *Graph {
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
	b := &builder{fn: fn, queries: queries}
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
	b.scopes = append(b.scopes, block)
	defer func() { b.scopes = b.scopes[:len(b.scopes)-1] }()

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
	case *ast.BreakStmt, *ast.ContinueStmt:
		current.Sites = append(current.Sites, statementSite(node, scopeID))
		if len(b.loops) == 0 {
			return current
		}
		loop := b.loops[len(b.loops)-1]
		b.appendLoopScopeExits(current, loop.scopeDepth)
		if _, ok := node.(*ast.BreakStmt); ok {
			current.Terminator = &Jump{Target: loop.breakTarget}
		} else {
			current.Terminator = &Jump{Target: loop.continueTarget}
		}
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
		loopID := ir.NodeID(node.ID())
		init := b.newBlock(BlockLoopInit, ast.LocOf(node))
		bodyBlock := b.newBlock(BlockLoopBody, ast.LocOf(node))
		latch := b.newBlock(BlockLoopLatch, ast.LocOf(node))
		exit := b.newBlock(BlockLoopExit, ast.LocOf(node))
		init.NodeID = loopID
		bodyBlock.NodeID = loopID
		latch.NodeID = loopID
		exit.NodeID = loopID
		current.Terminator = &Jump{Target: init}

		latchTarget := bodyBlock
		if node.Cond != nil || node.Iterable != nil {
			header := b.newBlock(BlockLoop, ast.LocOf(node))
			header.NodeID = loopID
			conditionID := ir.NodeID(0)
			if node.Cond != nil {
				conditionID = ir.NodeID(node.Cond.ID())
			}
			initTarget := header
			if b.queries.LoopGuaranteedEntry != nil && b.queries.LoopGuaranteedEntry(node.ID()) {
				initTarget = bodyBlock
			}
			init.Terminator = &Jump{Target: initTarget}
			header.Terminator = &Branch{
				NodeID:      loopID,
				ConditionID: conditionID,
				ScopeID:     scopeID,
				Location:    ast.LocOf(node),
				TrueTarget:  bodyBlock,
				FalseTarget: exit,
			}
			latchTarget = header
		} else {
			init.Terminator = &Jump{Target: bodyBlock}
		}

		b.loops = append(b.loops, loopContext{
			continueTarget: latch,
			breakTarget:    exit,
			scopeDepth:     len(b.scopes),
		})
		bodyEnd := b.buildBlock(node.Body, bodyBlock)
		b.loops = b.loops[:len(b.loops)-1]
		if bodyEnd != nil && bodyEnd.Terminator == nil {
			bodyEnd.Terminator = &Jump{Target: latch}
		}
		latch.Terminator = &Jump{Target: latchTarget}
		return exit
	case *ast.MatchStmt:
		var cases []int
		found := false
		if b.queries.MatchCases != nil {
			cases, found = b.queries.MatchCases(node.ID())
		}
		if !found || len(cases) != len(node.Arms) {
			// Invalid source may not have complete semantic evidence. Preserve one
			// recovery site so diagnostics can be published without inventing tags.
			current.Sites = append(current.Sites, statementSite(node, scopeID))
			return current
		}
		join := b.newBlock(BlockNormal, ast.LocOf(node))
		targets := make([]VariantTarget, 0, len(node.Arms))
		fallsThrough := false
		for armIndex, arm := range node.Arms {
			armBlock := b.newBlock(BlockNormal, ast.LocOf(arm))
			targets = append(targets, VariantTarget{Case: cases[armIndex], Target: armBlock})
			armEnd := b.buildBlock(arm.Body, armBlock)
			if armEnd != nil {
				fallsThrough = true
				if armEnd.Terminator == nil {
					armEnd.Terminator = &Jump{Target: join}
				}
			}
		}
		current.Terminator = &SwitchVariant{
			NodeID: ir.NodeID(node.ID()), ScopeID: scopeID,
			Location: ast.LocOf(node), Targets: targets,
		}
		if !fallsThrough {
			return nil
		}
		return join
	case *ast.BadDecl, *ast.ImportDecl, *ast.FnDecl, *ast.TypeAliasDecl,
		*ast.StructDecl, *ast.InterfaceDecl, *ast.EnumDecl:
		current.Sites = append(current.Sites, statementSite(node, scopeID))
		return current
	default:
		panic(fmt.Sprintf("CFG construction: unhandled AST statement %T", stmt))
	}
}

func (b *builder) appendLoopScopeExits(current *Block, scopeDepth int) {
	for index := len(b.scopes) - 1; index >= scopeDepth; index-- {
		scope := b.scopes[index]
		if scope.ID() == 0 {
			continue
		}
		current.Sites = append(current.Sites, &Site{
			Kind:     SiteScopeExit,
			NodeID:   ir.NodeID(scope.ID()),
			ScopeID:  ir.NodeID(scope.ID()),
			Location: ast.LocOf(scope),
		})
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
	rebuildBlockTopology(fn)
	finalizeSites(fn)
}

func finalizeSites(fn *Graph) {
	if fn == nil {
		return
	}
	fn.SiteEdges = graphcore.NewDirected(func(edge Edge) (SiteID, SiteID) { return edge.From, edge.To })
	for _, block := range fn.Blocks {
		if block == nil {
			continue
		}
		switch term := block.Terminator.(type) {
		case *Branch:
			block.Sites = append(block.Sites, &Site{
				Kind:     SiteTerminator,
				NodeID:   term.NodeID,
				ScopeID:  term.ScopeID,
				Location: term.Location,
			})
		case *SwitchVariant:
			block.Sites = append(block.Sites, &Site{
				Kind:     SiteTerminator,
				NodeID:   term.NodeID,
				ScopeID:  term.ScopeID,
				Location: term.Location,
			})
		}
		if len(block.Sites) == 0 {
			block.Sites = append(block.Sites, &Site{Kind: SiteJoin})
		}
		for index, site := range block.Sites {
			site.ID = SiteID{Block: block.ID, Index: index}
		}
	}
	for _, block := range fn.Blocks {
		if block == nil || len(block.Sites) == 0 {
			continue
		}
		for index := 0; index+1 < len(block.Sites); index++ {
			connectSites(fn, block.Sites[index], block.Sites[index+1], EdgeNormal, 0)
		}
		last := block.Sites[len(block.Sites)-1]
		switch term := block.Terminator.(type) {
		case *Jump:
			connectBlockSite(fn, last, term.Target, EdgeNormal, 0)
		case *Branch:
			connectBlockSite(fn, last, term.TrueTarget, EdgeTrue, 0)
			connectBlockSite(fn, last, term.FalseTarget, EdgeFalse, 0)
		case *Return:
			connectBlockSite(fn, last, fn.Exit, EdgeReturn, 0)
		case *SwitchVariant:
			for _, target := range term.Targets {
				connectBlockSite(fn, last, target.Target, EdgeVariantCase, target.Case)
			}
		case nil:
		default:
			panic(fmt.Sprintf("CFG finalization: unhandled terminator %T", block.Terminator))
		}
	}
}

func connectBlockSite(fn *Graph, from *Site, target *Block, kind EdgeKind, caseIndex int) {
	if target == nil || len(target.Sites) == 0 {
		return
	}
	connectSites(fn, from, target.Sites[0], kind, caseIndex)
}

func connectSites(fn *Graph, from, to *Site, kind EdgeKind, caseIndex int) {
	if fn == nil || fn.SiteEdges == nil || from == nil || to == nil {
		return
	}
	fn.SiteEdges.AddEdge(Edge{From: from.ID, To: to.ID, Kind: kind, Case: caseIndex})
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

func rebuildBlockTopology(fn *Graph) {
	if fn == nil {
		return
	}
	fn.BlockEdges = graphcore.NewDirected(func(edge BlockEdge) (int, int) { return edge.From, edge.To })
	for _, block := range fn.Blocks {
		if block == nil || block.Terminator == nil {
			continue
		}
		for _, successor := range block.Terminator.Successors() {
			if successor != nil {
				fn.BlockEdges.AddEdge(BlockEdge{From: block.ID, To: successor.ID})
			}
		}
	}
}
