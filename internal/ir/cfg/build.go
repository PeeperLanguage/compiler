package cfg

import (
	"fmt"

	graphcore "compiler/internal/graph"
	"compiler/internal/ir"
	"compiler/internal/ir/thir"
	"compiler/internal/source"
	"compiler/pkg/typednil"
)

type builder struct {
	fn      *ControlFlowGraph
	current *Block
	nextID  int
	scopes  []*thir.Block
	loops   []loopContext
}

type loopContext struct {
	continueTarget *Block
	breakTarget    *Block
	scopeDepth     int
}

// BuildModule creates immutable control-flow topology from THIR.
func BuildModule(source *thir.Module) *Module {
	if source == nil {
		return nil
	}
	module := &Module{
		Functions: make([]*ControlFlowGraph, 0, len(source.Functions)),
	}
	for _, function := range source.Functions {
		if function == nil || function.Body == nil {
			continue
		}
		graph := buildFunction(function)
		finalizeGraph(graph)
		if graph.FunctionID == "" {
			panic(fmt.Sprintf("CFG construction: function %q has no stable identity", graph.Name))
		}
		if module.FunctionByID(graph.FunctionID) != nil {
			panic("CFG construction: duplicate stable function identity")
		}
		module.Functions = append(module.Functions, graph)
	}
	return module
}

func buildFunction(source *thir.Function) *ControlFlowGraph {
	fn := &ControlFlowGraph{
		FunctionID:     source.Identity,
		Name:           source.Name,
		Location:       source.Source.Location,
		ReturnTypeText: source.ReturnTypeText,
		HasReturnValue: source.HasReturnValue,
		Blocks:         make([]*Block, 0),
	}
	b := &builder{fn: fn}
	fn.Entry = b.newBlock(BlockNormal, source.Body.Source.Location)
	fn.Exit = b.newBlock(BlockNormal, source.Source.Location)
	b.current = fn.Entry
	b.buildBlockBody(source.Body)
	if b.current != nil && b.current.Terminator == nil {
		b.current.Terminator = &Jump{Target: fn.Exit}
	}
	return fn
}

func (b *builder) newBlock(origin BlockOrigin, location *source.Location) *Block {
	block := &Block{ID: b.nextID, Origin: origin, Location: location, Sites: make([]*Site, 0)}
	b.nextID++
	b.fn.Blocks = append(b.fn.Blocks, block)
	return block
}

// buildBlockBody preserves lexical scope entry/exit around a block while
// allowing terminated paths to continue as disconnected recovery blocks.
func (b *builder) buildBlockBody(block *thir.Block) {
	if b == nil || block == nil {
		return
	}
	b.scopes = append(b.scopes, block)
	defer func() { b.scopes = b.scopes[:len(b.scopes)-1] }()

	for _, statement := range block.Stmts {
		if b.current == nil {
			b.current = b.newBlock(BlockNormal, statement.SourceInfo().Location)
		}
		statement.BuildControlFlow(b)
	}
	if b.current != nil && block.Source.NodeID != 0 {
		b.current.Sites = append(b.current.Sites, &Site{
			Kind:     SiteScopeExit,
			NodeID:   block.Source.NodeID,
			ScopeID:  block.Source.NodeID,
			Location: block.Source.Location,
		})
	}
}

func (b *builder) BuildBlock(block *thir.Block) {
	b.buildBlockBody(block)
	if b.current == nil {
		return
	}
	continuation := b.newBlock(BlockNormal, block.Source.Location)
	b.current.Terminator = &Jump{Target: continuation}
	b.current = continuation
}

func (b *builder) BuildBinding(statement *thir.Binding) {
	b.appendStatement(statement.Source)
}

func (b *builder) BuildExprStmt(statement *thir.ExprStmt) {
	b.appendStatement(statement.Source)
}

func (b *builder) BuildAssign(statement *thir.Assign) {
	b.appendStatement(statement.Source)
}

func (b *builder) BuildInvalidStmt(statement *thir.InvalidStmt) {
	b.appendStatement(statement.Source)
}

func (b *builder) BuildReturn(statement *thir.Return) {
	b.appendStatement(statement.Source)
	b.current.Terminator = &Return{NodeID: statement.Source.NodeID}
	b.current = nil
}

func (b *builder) BuildBreak(statement *thir.Break) {
	b.buildLoopJump(statement.Source, true)
}

func (b *builder) BuildContinue(statement *thir.Continue) {
	b.buildLoopJump(statement.Source, false)
}

func (b *builder) buildLoopJump(source ir.SourceInfo, breaking bool) {
	b.appendStatement(source)
	if len(b.loops) == 0 {
		return
	}
	loop := b.loops[len(b.loops)-1]
	b.appendLoopScopeExits(b.current, loop.scopeDepth)
	target := loop.continueTarget
	if breaking {
		target = loop.breakTarget
	}
	b.current.Terminator = &Jump{Target: target}
	b.current = nil
}

func (b *builder) BuildIf(statement *thir.If) {
	source := statement.Source
	scopeID := b.currentScopeID()
	thenBlock := b.newBlock(BlockThen, source.Location)
	elseBlock := b.newBlock(BlockElse, source.Location)
	join := b.newBlock(BlockNormal, source.Location)
	conditionID := ir.NodeID(0)
	if !typednil.IsNil(statement.Condition) {
		conditionID = statement.Condition.SourceInfo().NodeID
	}
	b.current.Terminator = &Branch{
		NodeID:      source.NodeID,
		ConditionID: conditionID,
		ScopeID:     scopeID,
		Location:    source.Location,
		TrueTarget:  thenBlock,
		FalseTarget: elseBlock,
	}

	b.current = thenBlock
	b.buildBlockBody(statement.Then)
	thenEnd := b.current
	if thenEnd != nil && thenEnd.Terminator == nil {
		thenEnd.Terminator = &Jump{Target: join}
	}

	b.current = elseBlock
	if !typednil.IsNil(statement.Else) {
		statement.Else.BuildControlFlow(b)
	}
	elseEnd := b.current
	if elseEnd != nil && elseEnd.Terminator == nil {
		elseEnd.Terminator = &Jump{Target: join}
	}

	b.current = join
	if thenEnd == nil && elseEnd == nil {
		b.current = nil
	}
}

func (b *builder) BuildFor(statement *thir.For) {
	if statement.Checked != nil {
		b.BuildBlock(statement.Checked)
		return
	}

	source := statement.Source
	scopeID := b.currentScopeID()
	init := b.newBlock(BlockLoopInit, source.Location)
	bodyBlock := b.newBlock(BlockLoopBody, source.Location)
	latch := b.newBlock(BlockLoopLatch, source.Location)
	exit := b.newBlock(BlockLoopExit, source.Location)
	init.NodeID = source.NodeID
	bodyBlock.NodeID = source.NodeID
	latch.NodeID = source.NodeID
	exit.NodeID = source.NodeID
	b.current.Terminator = &Jump{Target: init}

	latchTarget := bodyBlock
	if !typednil.IsNil(statement.Condition) || !typednil.IsNil(statement.Iterable) {
		header := b.newBlock(BlockLoop, source.Location)
		header.NodeID = source.NodeID
		conditionID := ir.NodeID(0)
		if !typednil.IsNil(statement.Condition) {
			conditionID = statement.Condition.SourceInfo().NodeID
		}
		initTarget := header
		if statement.Iteration != nil && statement.Iteration.IsGuaranteedEntry() {
			initTarget = bodyBlock
		}
		init.Terminator = &Jump{Target: initTarget}
		header.Terminator = &Branch{
			NodeID:      source.NodeID,
			ConditionID: conditionID,
			ScopeID:     scopeID,
			Location:    source.Location,
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
	b.current = bodyBlock
	b.buildBlockBody(statement.Body)
	b.loops = b.loops[:len(b.loops)-1]
	if b.current != nil && b.current.Terminator == nil {
		b.current.Terminator = &Jump{Target: latch}
	}
	latch.Terminator = &Jump{Target: latchTarget}
	b.current = exit
}

func (b *builder) BuildMatch(statement *thir.Match) {
	valid := statement.EnumType != nil && statement.CaseCount > 0 && len(statement.Arms) > 0
	for _, arm := range statement.Arms {
		valid = valid && arm.Case >= 0 && arm.Case < statement.CaseCount && arm.Body != nil
	}
	if !valid {
		// Invalid source may not have complete semantic evidence. Preserve one
		// recovery site so diagnostics can be published without inventing tags.
		b.appendStatement(statement.Source)
		return
	}

	scopeID := b.currentScopeID()
	entry := b.current
	join := b.newBlock(BlockNormal, statement.Source.Location)
	targets := make([]VariantTarget, 0, len(statement.Arms))
	fallsThrough := false
	for _, arm := range statement.Arms {
		armBlock := b.newBlock(BlockNormal, arm.Source.Location)
		targets = append(targets, VariantTarget{Case: arm.Case, Target: armBlock})
		b.current = armBlock
		b.buildBlockBody(arm.Body)
		if b.current != nil {
			fallsThrough = true
			if b.current.Terminator == nil {
				b.current.Terminator = &Jump{Target: join}
			}
		}
	}
	entry.Terminator = &SwitchVariant{
		NodeID: statement.Source.NodeID, ScopeID: scopeID,
		Location: statement.Source.Location, Targets: targets,
	}
	if fallsThrough {
		b.current = join
	} else {
		b.current = nil
	}
}

func (b *builder) appendStatement(source ir.SourceInfo) {
	b.current.Sites = append(b.current.Sites, &Site{
		Kind:     SiteStatement,
		NodeID:   source.NodeID,
		ScopeID:  b.currentScopeID(),
		Location: source.Location,
	})
}

func (b *builder) currentScopeID() ir.NodeID {
	if len(b.scopes) == 0 {
		return 0
	}
	return b.scopes[len(b.scopes)-1].Source.NodeID
}

func (b *builder) appendLoopScopeExits(current *Block, scopeDepth int) {
	for index := len(b.scopes) - 1; index >= scopeDepth; index-- {
		scope := b.scopes[index]
		if scope.Source.NodeID == 0 {
			continue
		}
		current.Sites = append(current.Sites, &Site{
			Kind:     SiteScopeExit,
			NodeID:   scope.Source.NodeID,
			ScopeID:  scope.Source.NodeID,
			Location: scope.Source.Location,
		})
	}
}

func finalizeGraph(fn *ControlFlowGraph) {
	if fn == nil || fn.Entry == nil {
		return
	}
	for _, block := range fn.Blocks {
		if block != nil {
			block.IsReachable = false
		}
	}
	markReachable(fn.Entry, make(map[int]bool))
	rebuildBlockTopology(fn)
	finalizeSites(fn)
}

func finalizeSites(fn *ControlFlowGraph) {
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

func connectBlockSite(fn *ControlFlowGraph, from *Site, target *Block, kind EdgeKind, caseIndex int) {
	if target == nil || len(target.Sites) == 0 {
		return
	}
	connectSites(fn, from, target.Sites[0], kind, caseIndex)
}

func connectSites(fn *ControlFlowGraph, from, to *Site, kind EdgeKind, caseIndex int) {
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
	block.IsReachable = true
	if block.Terminator == nil {
		return
	}
	for _, successor := range block.Terminator.Successors() {
		markReachable(successor, seen)
	}
}

func rebuildBlockTopology(fn *ControlFlowGraph) {
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
