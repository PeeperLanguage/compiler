package cfg

import (
	"compiler/internal/ir/hir"
)

type builder struct {
	fn     *Function
	nextID int
}

func buildFunction(sourceFn *hir.Function) *Function {
	if sourceFn == nil {
		return nil
	}
	fn := &Function{
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
		next.Terminator = &JumpTerm{Target: fn.Exit}
	}
	return fn
}

func (b *builder) newBlock() *Block {
	block := &Block{ID: b.nextID, Stmts: make([]hir.Stmt, 0)}
	b.nextID++
	b.fn.Blocks = append(b.fn.Blocks, block)
	return block
}

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

func (b *builder) buildStmt(stmt hir.Stmt, current *Block) *Block {
	switch s := stmt.(type) {
	case nil:
		return current
	case *hir.Block:
		return b.buildBlock(s, current)
	case *hir.Binding:
		current.Stmts = append(current.Stmts, s)
		return current
	case *hir.Invalid:
		current.Stmts = append(current.Stmts, s)
		return current
	case *hir.Return:
		current.Stmts = append(current.Stmts, s)
		current.Terminator = &ReturnTerm{Value: s.Value}
		current.Returns = true
		return nil
	case *hir.If:
		thenBlock := b.newBlock()
		elseBlock := b.newBlock()
		join := b.newBlock()
		thenBlock.Location = s.Location
		thenBlock.BranchKind = "if"
		elseBlock.Location = s.Location
		elseBlock.BranchKind = "else"
		current.Terminator = &BranchTerm{Cond: s.Cond, TrueTarget: thenBlock, FalseTarget: elseBlock}

		thenEnd := b.buildBlock(s.Then, thenBlock)
		thenFallsThrough := thenEnd != nil
		if thenEnd != nil && thenEnd.Terminator == nil {
			thenEnd.Terminator = &JumpTerm{Target: join}
		}

		elseEnd := b.buildStmt(s.Else, elseBlock)
		elseFallsThrough := elseEnd != nil
		if elseEnd != nil && elseEnd.Terminator == nil {
			elseEnd.Terminator = &JumpTerm{Target: join}
		}

		if !thenFallsThrough && !elseFallsThrough {
			return nil
		}
		return join
	default:
		current.Stmts = append(current.Stmts, stmt)
		return current
	}
}
