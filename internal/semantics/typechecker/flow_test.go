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
		variants: []variantStateFact{
			{origins: outerOrigins, cases: []int{1}, caseCount: 2},
			{origins: innerOrigins, cases: []int{1}, caseCount: 2},
			{origins: outerOrigins, cases: []int{1}, caseCount: 2, dependencies: []*symbols.Symbol{inner}},
		},
		references:  map[*symbols.Symbol][]place.Origin{outer: outerOrigins, inner: innerOrigins},
		rawPointers: map[*symbols.Symbol][]place.Origin{outer: outerOrigins, inner: innerOrigins},
	}

	clearFlowScope(innerScope, &state)

	if len(state.variants) != 1 || state.variants[0].origins[0].Root != outer {
		t.Fatalf("variant facts after scope exit = %#v, want only outer fact", state.variants)
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
	state := flowState{variants: []variantStateFact{{origins: []place.Origin{{Root: global}}, cases: []int{1}, caseCount: 2}}}
	analyzer := flowAnalyzer{
		module: &project.Module{ModuleScope: moduleScope, Semantics: project.NewSemanticInfo()},
		result: &flowresult.Result{ExprTypes: make(map[ast.NodeID]typeinfo.Type)},
	}

	analyzer.invalidateCall(&checker{}, nil, &ast.CallExpr{Callee: &ast.Ident{Name: "Touch"}}, &state)

	if len(state.variants) != 0 {
		t.Fatalf("variant facts after call = %#v, want mutable module fact invalidated", state.variants)
	}
}

func TestMergeVariantFactsUnionsPossibleCases(t *testing.T) {
	root := symbols.New("value", symbols.SymbolVar, nil, nil)
	origins := []place.Origin{{Root: root}}
	left := flowState{reachable: true, variants: []variantStateFact{{origins: origins, cases: []int{0}, caseCount: 3}}}
	right := flowState{reachable: true, variants: []variantStateFact{{origins: origins, cases: []int{1}, caseCount: 3}}}

	merged := mergeFlowStates(left, right)
	if !merged.reachable || len(merged.variants) != 1 || !sameCaseSet(merged.variants[0].cases, []int{0, 1}) {
		t.Fatalf("merged variant facts = %#v", merged)
	}
}

func TestInvalidateVariantFactsPreservesCaseForPayloadDescendant(t *testing.T) {
	root := symbols.New("value", symbols.SymbolVar, nil, nil)
	carrier := []place.Origin{{Root: root}}
	state := flowState{variants: []variantStateFact{{origins: carrier, cases: []int{1}, caseCount: 2}}}
	mutated := place.VariantPayloadOrigins(carrier, []int{1})
	mutated[0].Projections = append(mutated[0].Projections, place.OriginProjection{Kind: place.OriginField, Field: "field"})

	invalidateVariantOrigins(&state, mutated)
	if len(state.variants) != 1 || !sameCaseSet(state.variants[0].cases, []int{1}) {
		t.Fatalf("payload mutation invalidated carrier case = %#v", state.variants)
	}
}
