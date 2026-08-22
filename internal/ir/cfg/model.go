package cfg

import (
	"compiler/internal/ir"
	"compiler/internal/source"
)

// Module owns canonical function CFG identity for one source module.
type Module struct {
	Functions []*Graph
	byNodeID  map[ir.NodeID]*Graph
}

// Function returns one graph by source function identity.
func (m *Module) Function(id ir.NodeID) *Graph {
	if m == nil {
		return nil
	}
	return m.byNodeID[id]
}

type Graph struct {
	NodeID         ir.NodeID
	Name           string
	Location       *source.Location
	ReturnTypeText string
	ReturnsValue   bool
	Entry          *Block
	Exit           *Block
	Blocks         []*Block
}

// SiteID identifies one ordered semantic program point within a CFG block.
type SiteID struct {
	Block int
	Index int
}

type EdgeKind uint8

const (
	EdgeNormal EdgeKind = iota
	EdgeTrue
	EdgeFalse
	EdgeReturn
)

// Edge preserves branch meaning independently from adjacency ordering.
type Edge struct {
	From SiteID
	To   SiteID
	Kind EdgeKind
}

type SiteKind uint8

const (
	SiteStatement SiteKind = iota
	SiteScopeExit
	SiteTerminator
	SiteJoin
)

// Site records source identity and lexical scope at one CFG program point.
type Site struct {
	ID           SiteID
	Kind         SiteKind
	NodeID       ir.NodeID
	ScopeID      ir.NodeID
	Location     *source.Location
	Successors   []Edge
	Predecessors []Edge
}

type BlockOrigin uint8

const (
	BlockNormal BlockOrigin = iota
	BlockThen
	BlockElse
	BlockLoop
	BlockLoopBody
)

type Block struct {
	ID           int
	Origin       BlockOrigin
	Location     *source.Location
	Sites        []*Site
	Terminator   Terminator
	Predecessors []*Block
	Reachable    bool
}

type Terminator interface {
	termNode()
	Successors() []*Block
}

type Jump struct {
	Target *Block
}

type Branch struct {
	NodeID      ir.NodeID
	ConditionID ir.NodeID
	ScopeID     ir.NodeID
	Location    *source.Location
	TrueTarget  *Block
	FalseTarget *Block
}

type Return struct {
	NodeID ir.NodeID
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
