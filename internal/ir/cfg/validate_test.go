package cfg

import (
	"strings"
	"testing"

	"compiler/internal/frontend/ast"
	graphcore "compiler/internal/graph"
	"compiler/internal/source"
)

// branchingModule builds a graph with every terminator kind reachable from one
// function, so a break in any check group has something real to break.
func branchingModule(t *testing.T) *Module {
	t.Helper()
	location := source.NewLocation("validate_test.peep", source.Position{Line: 1, Column: 1}, source.Position{Line: 1, Column: 10})
	branch := &ast.IfStmt{
		NodeIDHolder: ast.NodeIDHolder{NodeID: 30},
		Cond:         &ast.BoolLit{NodeIDHolder: ast.NodeIDHolder{NodeID: 31}, Value: true, Location: location},
		Then: &ast.BlockStmt{
			NodeIDHolder: ast.NodeIDHolder{NodeID: 32},
			Stmts:        []ast.Stmt{&ast.ReturnStmt{NodeIDHolder: ast.NodeIDHolder{NodeID: 33}, Location: location}},
		},
		Else:     &ast.BlockStmt{NodeIDHolder: ast.NodeIDHolder{NodeID: 34}},
		Location: location,
	}
	body := &ast.BlockStmt{NodeIDHolder: ast.NodeIDHolder{NodeID: 10}, Stmts: []ast.Stmt{branch}}
	module := BuildModule(testModule(body, nil), BuildQueries{})
	if module == nil || len(module.Functions) != 1 {
		t.Fatalf("test fixture built %#v, want one function graph", module)
	}
	if module.Functions[0] == nil {
		t.Fatal("test fixture built a nil graph")
	}
	return module
}

// A graph the builder produced must validate. Without this the negative tests
// below could all pass against a fixture that was malformed to begin with.
func TestValidateAcceptsConstructedTopology(t *testing.T) {
	if err := branchingModule(t).Validate(); err != nil {
		t.Fatalf("constructed topology rejected: %v", err)
	}
}

func TestValidateRejectsTopologyDefects(t *testing.T) {
	tests := []struct {
		name   string
		damage func(*Graph)
		want   string
	}{
		{
			name:   "block identity does not match its index",
			damage: func(fn *Graph) { fn.Blocks[2].ID = 99 },
			want:   "identifies as b99",
		},
		{
			name:   "entry belongs to another graph",
			damage: func(fn *Graph) { fn.Entry = &Block{ID: 0} },
			want:   "entry b0 is not one of its blocks",
		},
		{
			name:   "exit is missing",
			damage: func(fn *Graph) { fn.Exit = nil },
			want:   "has no exit block",
		},
		{
			name: "a reachable block leaves control nowhere",
			damage: func(fn *Graph) {
				fn.Entry.Terminator = nil
			},
			want: "reachable block b0 has no terminator",
		},
		{
			name: "the exit block terminates",
			damage: func(fn *Graph) {
				fn.Exit.Terminator = &Jump{Target: fn.Entry}
			},
			want: "carries a terminator",
		},
		{
			name: "a transfer leaves the graph",
			damage: func(fn *Graph) {
				fn.Entry.Terminator = &Jump{Target: &Block{ID: 7}}
			},
			want: "is not one of its blocks",
		},
		{
			name: "a variant switch claims one case twice",
			damage: func(fn *Graph) {
				fn.Entry.Terminator = &SwitchVariant{Targets: []VariantTarget{
					{Case: 0, Target: fn.Exit}, {Case: 0, Target: fn.Exit},
				}}
			},
			want: "switches twice on case 0",
		},
		{
			name: "a predecessor records a transfer that does not exist",
			damage: func(fn *Graph) {
				fn.BlockEdges.AddEdge(BlockEdge{From: fn.Blocks[2].ID, To: fn.Exit.ID})
			},
			want: "but the terminator does not",
		},
		{
			name: "a transfer goes unrecorded by its target",
			damage: func(fn *Graph) {
				fn.BlockEdges = graphcore.NewDirected(func(edge BlockEdge) (int, int) { return edge.From, edge.To })
			},
			want: "absent from block topology",
		},
		{
			name: "a site carries the wrong identity",
			damage: func(fn *Graph) {
				fn.Entry.Sites[0].ID = SiteID{Block: 4, Index: 6}
			},
			want: "identifies as b4[6]",
		},
		{
			name: "a scope exit names no scope",
			damage: func(fn *Graph) {
				for _, block := range fn.Blocks {
					for _, site := range block.Sites {
						if site.Kind == SiteScopeExit {
							site.ScopeID = 0
							return
						}
					}
				}
				t.Fatal("fixture has no scope exit site to damage")
			},
			want: "names no scope",
		},
		{
			name: "a site edge points at no site",
			damage: func(fn *Graph) {
				rewriteFirstSiteEdge(fn, func(edge Edge) Edge {
					edge.To = SiteID{Block: 42, Index: 0}
					return edge
				})
			},
			want: "which is not a site",
		},
		{
			name: "a branch leaves on a plain sequence edge",
			damage: func(fn *Graph) {
				rewriteFirstSiteEdge(fn, func(edge Edge) Edge {
					edge.Kind = EdgeNormal
					return edge
				})
			},
			want: "a branch leaves only on a true or false edge",
		},
		{
			name: "reachability disagrees with entry traversal",
			damage: func(fn *Graph) {
				fn.Blocks[len(fn.Blocks)-1].Reachable = !fn.Blocks[len(fn.Blocks)-1].Reachable
			},
			want: "entry traversal says",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			module := branchingModule(t)
			test.damage(module.Functions[0])
			err := module.Validate()
			if err == nil {
				t.Fatalf("damaged topology accepted: %s", test.name)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %q, want it to mention %q", err.Error(), test.want)
			}
		})
	}
}

// Problems are collected by walking maps, so an unsorted report would name
// different defects on different runs and make an internal error unreproducible.
func TestValidateReportsDefectsDeterministically(t *testing.T) {
	first := ""
	for attempt := 0; attempt < 8; attempt++ {
		module := branchingModule(t)
		fn := module.Functions[0]
		fn.BlockEdges = graphcore.NewDirected(func(edge BlockEdge) (int, int) { return edge.From, edge.To })
		for _, block := range fn.Blocks {
			block.Reachable = !block.Reachable
		}
		err := module.Validate()
		if err == nil {
			t.Fatal("damaged topology accepted")
		}
		if attempt == 0 {
			first = err.Error()
			continue
		}
		if err.Error() != first {
			t.Fatalf("report %d = %q, want %q", attempt, err.Error(), first)
		}
	}
}

func rewriteFirstSiteEdge(fn *Graph, rewrite func(Edge) Edge) {
	replacement := graphcore.NewDirected(func(edge Edge) (SiteID, SiteID) { return edge.From, edge.To })
	rewritten := false
	for _, block := range fn.Blocks {
		for _, site := range block.Sites {
			for _, edge := range fn.SiteEdges.OutEdges(site.ID) {
				if !rewritten {
					edge = rewrite(edge)
					rewritten = true
				}
				replacement.AddEdge(edge)
			}
		}
	}
	fn.SiteEdges = replacement
}

func TestValidateAcceptsAnEmptyModule(t *testing.T) {
	var missing *Module
	if err := missing.Validate(); err != nil {
		t.Fatalf("nil module rejected: %v", err)
	}
	if err := (&Module{}).Validate(); err != nil {
		t.Fatalf("empty module rejected: %v", err)
	}
}
