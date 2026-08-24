package typechecker

import (
	"testing"

	"compiler/internal/frontend/ast"
	"compiler/internal/project"
	"compiler/internal/semantics/flowresult"
	"compiler/internal/semantics/place"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
)

func TestClearFlowScopeRemovesOnlyExitedBindingFacts(t *testing.T) {
	outerScope := symbols.NewScope(nil)
	innerScope := symbols.NewScope(outerScope)
	outer := symbols.New("outer", symbols.SymbolVar, &ast.LetDecl{IsMutable: true}, nil)
	inner := symbols.New("inner", symbols.SymbolVar, &ast.LetDecl{IsMutable: true}, nil)
	if err := outerScope.Declare(outer); err != nil {
		t.Fatal(err)
	}
	if err := innerScope.Declare(inner); err != nil {
		t.Fatal(err)
	}
	outerOrigins := []place.Origin{{Root: outer}}
	innerOrigins := []place.Origin{{Root: inner}}
	state := flowState{
		presence: []presenceStateFact{
			{origins: outerOrigins, depth: 1},
			{origins: innerOrigins, depth: 1},
			{origins: outerOrigins, depth: 1, dependencies: []*symbols.Symbol{inner}},
		},
		references:  map[*symbols.Symbol][]place.Origin{outer: outerOrigins, inner: innerOrigins},
		rawPointers: map[*symbols.Symbol][]place.Origin{outer: outerOrigins, inner: innerOrigins},
	}

	clearFlowScope(innerScope, &state)

	if len(state.presence) != 1 || state.presence[0].origins[0].Root != outer {
		t.Fatalf("presence after scope exit = %#v, want only outer fact", state.presence)
	}
	if _, exists := state.references[inner]; exists {
		t.Fatal("scope exit retained inner reference origin")
	}
	if _, exists := state.rawPointers[inner]; exists {
		t.Fatal("scope exit retained inner raw-pointer origin")
	}
	if len(state.references[outer]) != 1 || len(state.rawPointers[outer]) != 1 {
		t.Fatal("scope exit removed outer origin evidence")
	}
}

func TestInvalidateCallClearsMutableModuleVariableFacts(t *testing.T) {
	moduleScope := symbols.NewScope(nil)
	global := symbols.New("maybe", symbols.SymbolVar, &ast.LetDecl{IsMutable: true, IsModuleVar: true}, nil)
	if err := moduleScope.Declare(global); err != nil {
		t.Fatal(err)
	}
	state := flowState{presence: []presenceStateFact{{origins: []place.Origin{{Root: global}}, depth: 1}}}
	analyzer := flowAnalyzer{
		module: &project.Module{ModuleScope: moduleScope, Semantics: project.NewSemanticInfo()},
		result: &flowresult.Result{ExprTypes: make(map[ast.NodeID]typeinfo.Type)},
	}

	analyzer.invalidateCall(&checker{}, nil, &ast.CallExpr{Callee: &ast.Ident{Name: "Touch"}}, &state)

	if len(state.presence) != 0 {
		t.Fatalf("presence after call = %#v, want mutable module fact invalidated", state.presence)
	}
}

func TestMergeFlowStatesTreatsMissingOriginAsUnknown(t *testing.T) {
	pointer := symbols.New("pointer", symbols.SymbolVar, nil, nil)
	left := symbols.New("left", symbols.SymbolVar, nil, nil)
	right := symbols.New("right", symbols.SymbolVar, nil, nil)
	leftState := newFlowState()
	leftState.rawPointers[pointer] = []place.Origin{{Root: left}}

	unknown := mergeFlowStates(leftState, newFlowState())
	if _, known := unknown.rawPointers[pointer]; known {
		t.Fatalf("one unknown predecessor retained raw-pointer origins: %#v", unknown.rawPointers[pointer])
	}

	rightState := newFlowState()
	rightState.rawPointers[pointer] = []place.Origin{{Root: right}}
	known := mergeFlowStates(leftState, rightState)
	want := []place.Origin{{Root: left}, {Root: right}}
	if !place.SameOrigins(known.rawPointers[pointer], want) {
		t.Fatalf("known predecessor origins = %#v, want %#v", known.rawPointers[pointer], want)
	}
}

func TestInvalidatePresenceOriginsClearsIndexDependencies(t *testing.T) {
	values := symbols.New("values", symbols.SymbolParam, nil, nil)
	index := symbols.New("index", symbols.SymbolVar, nil, nil)
	state := flowState{presence: []presenceStateFact{{
		origins: []place.Origin{{Root: values, Projections: []place.OriginProjection{{
			Kind: place.OriginBindingIndex, Binding: index,
		}}}},
		depth:        1,
		dependencies: []*symbols.Symbol{index},
	}}}

	invalidatePresenceOrigins(&state, []place.Origin{{Root: index}})

	if len(state.presence) != 0 {
		t.Fatalf("index mutation retained dependent presence: %#v", state.presence)
	}
}
