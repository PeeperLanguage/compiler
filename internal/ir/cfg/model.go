package cfg

import (
	graphcore "compiler/internal/graph"
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

// Graph is finalized by BuildModule. Terminators and ordered block sites define
// control flow; BlockEdges and SiteEdges are derived traversal indexes. Consumers
// must not mutate topology after publication: rebuild the CFG before publishing
// a new generation, since site IDs and downstream evidence depend on it.
type Graph struct {
	NodeID         ir.NodeID
	Name           string
	Location       *source.Location
	ReturnTypeText string
	ReturnsValue   bool
	Entry          *Block
	Exit           *Block
	Blocks         []*Block
	// SiteEdges is the canonical ordered site topology. Edge values retain CFG
	// branch meaning; the shared graph kernel owns forward/reverse adjacency.
	SiteEdges *graphcore.Directed[SiteID, Edge]
	// BlockEdges is the canonical block topology derived from terminators.
	BlockEdges *graphcore.Directed[int, BlockEdge]
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
	EdgeVariantCase
)

// Edge preserves branch meaning independently from adjacency ordering.
type Edge struct {
	From SiteID
	To   SiteID
	Kind EdgeKind
	Case int
}

type BlockEdge struct {
	From int
	To   int
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
	ID       SiteID
	Kind     SiteKind
	NodeID   ir.NodeID
	ScopeID  ir.NodeID
	Location *source.Location
}

type BlockOrigin uint8

const (
	BlockNormal BlockOrigin = iota
	BlockThen
	BlockElse
	BlockLoopInit
	BlockLoop
	BlockLoopBody
	BlockLoopLatch
	// BlockLoopExit is the continuation a loop leaves to. It carries the loop's
	// NodeID like the roles above, so a consumer can ask which loop it exits,
	// but it is not part of the loop's structure: the code after the loop lives
	// here. Before it existed, exiting a loop was identified by the absence of a
	// role plus a NodeID that happened to name one.
	BlockLoopExit
)

type Block struct {
	ID         int
	NodeID     ir.NodeID
	Origin     BlockOrigin
	Location   *source.Location
	Sites      []*Site
	Terminator Terminator
	Reachable  bool
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

type VariantTarget struct {
	Case   int
	Target *Block
}

type SwitchVariant struct {
	NodeID   ir.NodeID
	ScopeID  ir.NodeID
	Location *source.Location
	Targets  []VariantTarget
}

func (*Jump) termNode()          {}
func (*Branch) termNode()        {}
func (*Return) termNode()        {}
func (*SwitchVariant) termNode() {}

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

func (t *SwitchVariant) Successors() []*Block {
	if t == nil {
		return nil
	}
	out := make([]*Block, 0, len(t.Targets))
	for _, target := range t.Targets {
		if target.Target != nil {
			out = append(out, target.Target)
		}
	}
	return out
}
