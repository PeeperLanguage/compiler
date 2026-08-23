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
