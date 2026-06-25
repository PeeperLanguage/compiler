package cfg

import (
	"compiler/internal/ir/hir"
)

type builder struct {
	fn     *Graph
	nextID int
}

// buildCFGFunction lowers one HIR function body into a control-flow graph.
//
// The CFG normalizes structured HIR into basic blocks with explicit
// terminators so later analyses can reason about reachability,
// fallthrough, and return paths without reinterpreting syntax trees.
func buildCFGFunction(sourceFn *hir.Function) *Graph {
	if sourceFn == nil {
		return nil
	}
	fn := &Graph{
		Name:       sourceFn.Name,
		ReturnType: sourceFn.ReturnType,
		Source:     sourceFn,
		Blocks:     make([]*Block, 0),
	}
	b := &builder{fn: fn}
	fn.Entry = b.newBlock()
	fn.Exit = b.newBlock()
	next := b.buildBlock(sourceFn.Body, fn.Entry)
	if next != nil && next.Terminator == nil {
		next.Terminator = &Jump{Target: fn.Exit}
	}
	return fn
}

// newBlock allocates a basic block, assigns it a stable ID, and registers it
// on the owning CFG function in creation order.
func (b *builder) newBlock() *Block {
	block := &Block{ID: b.nextID, Stmts: make([]hir.Stmt, 0)}
	b.nextID++
	b.fn.Blocks = append(b.fn.Blocks, block)
	return block
}

// buildBlock appends each HIR statement into the current CFG region until a
// statement terminates control flow and returns nil.
func (b *builder) buildBlock(block *hir.Block, current *Block) *Block {
	if b == nil || block == nil {
		return current
	}
	next := current
	for _, stmt := range block.Stmts {
		if next == nil {
			return nil
		}
		next = b.buildStmt(stmt, next)
	}
	return next
}

// buildStmt maps one HIR statement onto CFG blocks.
//
// Most statements stay in the current block. Control-flow statements create
// explicit terminators and successor blocks so the resulting graph has no
// implicit branch or fallthrough edges.
func (b *builder) buildStmt(stmt hir.Stmt, current *Block) *Block {
	switch s := stmt.(type) {
	case nil:
		return current
	case *hir.Block:
		return b.buildBlock(s, current)
	case *hir.Binding:
		current.Stmts = append(current.Stmts, s)
		return current
	case *hir.ExprStmt:
		current.Stmts = append(current.Stmts, s)
		return current
	case *hir.Invalid:
		current.Stmts = append(current.Stmts, s)
		return current
	case *hir.Return:
		current.Stmts = append(current.Stmts, s)
		current.Terminator = &Return{Value: s.Value}
		current.Returns = true
		return nil
	case *hir.If:
		// Structured HIR "if" becomes three CFG regions:
		//   1. then branch
		//   2. else branch
		//   3. join block for paths that continue after the conditional
		thenBlock := b.newBlock()
		elseBlock := b.newBlock()
		join := b.newBlock()
		thenBlock.Location = s.Location
		thenBlock.BranchKind = "if"
		elseBlock.Location = s.Location
		elseBlock.BranchKind = "else"
		current.Terminator = &Branch{Cond: s.Cond, TrueTarget: thenBlock, FalseTarget: elseBlock}

		thenEnd := b.buildBlock(s.Then, thenBlock)
		thenFallsThrough := thenEnd != nil
		if thenEnd != nil && thenEnd.Terminator == nil {
			// A branch with no explicit terminator continues into the join block.
			thenEnd.Terminator = &Jump{Target: join}
		}

		elseEnd := b.buildStmt(s.Else, elseBlock)
		elseFallsThrough := elseEnd != nil
		if elseEnd != nil && elseEnd.Terminator == nil {
			// Same rule for the else branch: make fallthrough explicit.
			elseEnd.Terminator = &Jump{Target: join}
		}

		if !thenFallsThrough && !elseFallsThrough {
			// Both branches terminate, so there is no live successor block after
			// this conditional.
			return nil
		}
		return join
	case *hir.For:
		if s.Cond == nil {
			bodyBlock := b.newBlock()
			bodyBlock.Location = s.Location
			bodyBlock.BranchKind = "for"
			current.Terminator = &Jump{Target: bodyBlock}
			bodyEnd := b.buildBlock(s.Body, bodyBlock)
			if bodyEnd != nil && bodyEnd.Terminator == nil {
				bodyEnd.Terminator = &Jump{Target: bodyBlock}
			}
			return nil
		}
		header := b.newBlock()
		header.Location = s.Location
		header.BranchKind = "for"
		bodyBlock := b.newBlock()
		bodyBlock.Location = s.Location
		bodyBlock.BranchKind = "for-body"
		exit := b.newBlock()
		current.Terminator = &Jump{Target: header}
		header.Terminator = &Branch{Cond: s.Cond, TrueTarget: bodyBlock, FalseTarget: exit}
		bodyEnd := b.buildBlock(s.Body, bodyBlock)
		if bodyEnd != nil && bodyEnd.Terminator == nil {
			bodyEnd.Terminator = &Jump{Target: header}
		}
		return exit
	default:
		current.Stmts = append(current.Stmts, stmt)
		return current
	}
}
