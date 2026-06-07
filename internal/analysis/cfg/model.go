package cfg

import (
	"compiler/internal/ir"
	"compiler/internal/ir/hir"
	"compiler/internal/source"
)

type Graph struct {
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

type Jump struct {
	Target *Block
}

type Branch struct {
	Cond        ir.Expr
	TrueTarget  *Block
	FalseTarget *Block
}

type Return struct {
	Value ir.Expr
}

func (*Jump) termNode()   {}
func (*Branch) termNode() {}
func (*Return) termNode() {}

func (t *Jump) Successors() []*Block {
	if t == nil || t.Target == nil {
		return nil
	}
	return []*Block{t.Target}
}

func (t *Branch) Successors() []*Block {
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

func (*Return) Successors() []*Block { return nil }
