package cfg

import (
	"compiler/core/source"
	"compiler/internal/ir"
	"compiler/internal/ir/hir"
)

type Module struct {
	Functions []*Function
}

type Function struct {
	Name       string
	ReturnType string
	Source     *hir.Function
	Entry      *Block
	Exit       *Block
	Blocks     []*Block
}

type Block struct {
	ID           int
	Location     *source.Location
	BranchKind   string
	Stmts        []hir.Stmt
	Terminator   Terminator
	Predecessors []*Block
	Reachable    bool
	Returns      bool
}

type Terminator interface {
	termNode()
	Successors() []*Block
}

type JumpTerm struct {
	Target *Block
}

type BranchTerm struct {
	Cond        ir.Expr
	TrueTarget  *Block
	FalseTarget *Block
}

type ReturnTerm struct {
	Value ir.Expr
}

func (*JumpTerm) termNode()   {}
func (*BranchTerm) termNode() {}
func (*ReturnTerm) termNode() {}

func (t *JumpTerm) Successors() []*Block {
	if t == nil || t.Target == nil {
		return nil
	}
	return []*Block{t.Target}
}

func (t *BranchTerm) Successors() []*Block {
	if t == nil {
		return nil
	}
	out := make([]*Block, 0, 2)
	if t.TrueTarget != nil {
		out = append(out, t.TrueTarget)
	}
	if t.FalseTarget != nil {
		out = append(out, t.FalseTarget)
	}
	return out
}

func (*ReturnTerm) Successors() []*Block { return nil }
