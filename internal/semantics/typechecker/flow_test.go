package typechecker

import (
	"testing"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/frontend/lexer"
	"compiler/internal/frontend/parser"
	"compiler/internal/ir/cfg"
	"compiler/internal/project"
	"compiler/internal/semantics/binder"
	"compiler/internal/semantics/collector"
	"compiler/internal/semantics/flowresult"
	"compiler/internal/semantics/place"
	"compiler/internal/semantics/resolver"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
	"compiler/pkg/peeper"
)

func checkFlowSource(t *testing.T, src string) (*project.Module, *diagnostics.DiagnosticBag) {
	t.Helper()
	const filePath = "flow_test" + peeper.SourceExt
	diag := diagnostics.NewDiagnosticBag()
	diag.AddSourceContent(filePath, src)
	ctx := project.New(".", peeper.SourceExt, diag)
	module := &project.Module{
		Key:        project.ModuleKeyFor(project.ModuleOriginLocal, filePath),
		ImportPath: "flow_test",
		FilePath:   filePath,
		Content:    src,
		AST:        parser.New(filePath, lexer.New(filePath, src, diag).Tokenize(), diag).ParseModule(),
		Imports:    make(map[string]project.ResolvedImport),
	}
	ctx.AddModule(module)
	collector.Collect(ctx, module)
	binder.Bind(ctx, module)
	resolver.Resolve(ctx, module)
	Check(ctx, module)
	module.TypedASTNodes = ast.Index(module.AST)
	module.CFG = cfg.BuildModule(module.AST, module.Semantics.MatchCases)
	module.Flow = CheckFlow(ctx, module)
	return module, diag
}

func TestNamedEnumCaseTestsRefineExactFields(t *testing.T) {
	module, diag := checkFlowSource(t, `enum Choice {
	Left: { value: i32 },
	Right: { value: i32 },
	Pending,
}

fn Read(choice: Choice) -> i32 {
	if choice is Choice::Left {
		return choice.value;
	}
	if !(choice is Choice::Right) {
		return 0;
	}
	return choice.value;
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	fn := module.AST.Stmts[1].(*ast.FnDecl)
	leftBranch := fn.Body.Stmts[0].(*ast.IfStmt)
	leftTest := leftBranch.Cond.(*ast.IsExpr)
	baseTest, baseFound := module.Semantics.CaseTests[leftTest.ID()]
	flowTest, flowFound := module.Flow.CaseTests[leftTest.ID()]
	if !baseFound || !flowFound || baseTest.Case != 0 || flowTest.Case != baseTest.Case ||
		flowTest.SubjectID != baseTest.SubjectID || flowTest.CaseCount != baseTest.CaseCount {
		t.Fatalf("case-test evidence = base %#v, flow %#v", baseTest, flowTest)
	}
	leftField := leftBranch.Then.Stmts[0].(*ast.ReturnStmt).Value.(*ast.SelectorExpr)
	rightField := fn.Body.Stmts[2].(*ast.ReturnStmt).Value.(*ast.SelectorExpr)
	for _, field := range []*ast.SelectorExpr{leftField, rightField} {
		if typ := module.EffectiveExprType(field.ID()); typeinfo.TypeText(typ) != "i32" {
			t.Fatalf("refined field type = %s, want i32", typeinfo.TypeText(typ))
		}
		payload := module.Flow.Payloads[field.ID()]
		if len(payload.Cases) != 1 {
			t.Fatalf("field payload evidence = %#v, want one exact case", payload)
		}
	}
}

func TestNamedEnumMatchCaseEdgeRefinesExactField(t *testing.T) {
	module, diag := checkFlowSource(t, `enum Result {
	Ok: { value: i32 },
	Error: { message: str },
	Pending,
}

fn Read(result: Result) -> i32 {
	match result {
		Result::Ok with {} => {
			return result.value;
		}
		Result::Error with {} => {
			return 1;
		}
		Result::Pending => {
			return 0;
		}
	}
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected match flow diagnostics:\n%s", diag.EmitAllToString())
	}
	fn := module.AST.Stmts[1].(*ast.FnDecl)
	match := fn.Body.Stmts[0].(*ast.MatchStmt)
	selector := match.Arms[0].Body.Stmts[0].(*ast.ReturnStmt).Value.(*ast.SelectorExpr)
	fieldType := module.EffectiveExprType(selector.ID())
	access, found := module.Flow.VariantFields[selector.ID()]
	if !found || access.Case != 0 || typeinfo.TypeText(fieldType) != "i32" || typeinfo.TypeText(access.Type) != "i32" {
		t.Fatalf("match field type = %s, access = %#v", typeinfo.TypeText(fieldType), access)
	}
}

func TestNamedEnumCaseProofSelectsFieldType(t *testing.T) {
	_, diag := checkFlowSource(t, `enum Choice {
	Number: { value: i32 },
	Text: { value: str }
}

fn Read(choice: Choice) -> str {
	if choice is Choice::Text {
		let value = choice.value;
		return value;
	}
	return "";
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected differing-field diagnostic:\n%s", diag.EmitAllToString())
	}
}

func TestNamedEnumCaseTestInsideVariantPayload(t *testing.T) {
	_, diag := checkFlowSource(t, `enum Inner {
	Left: { value: i32 },
	Right
}

enum Outer {
	Wrapped: { inner: Inner },
	Empty
}

fn Read(outer: Outer) -> i32 {
	if outer is Outer::Wrapped {
		if outer.inner is Inner::Left {
			return outer.inner.value;
		}
	}
	return 0;
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected nested-payload diagnostic:\n%s", diag.EmitAllToString())
	}
}

func TestNamedEnumFieldRequiresExactCaseProof(t *testing.T) {
	_, diag := checkFlowSource(t, `enum Choice {
	Left: { value: i32 },
	Right: { value: i32 },
}

fn Read(choice: Choice) -> i32 {
	return choice.value;
}`)
	if !diag.HasErrors() {
		t.Fatal("expected exact-case field diagnostic")
	}
}

func TestNamedEnumCarrierMutationInvalidatesCaseProof(t *testing.T) {
	_, diag := checkFlowSource(t, `enum Choice {
	Left: { value: i32 },
	Right: { value: i32 },
}

fn Read(mut choice: Choice) -> i32 {
	if choice is Choice::Left {
		choice = Choice::Right with .{ value = 0 };
		return choice.value;
	}
	return 0;
}`)
	if !diag.HasErrors() {
		t.Fatal("expected invalidated exact-case field diagnostic")
	}
}

func TestNamedEnumCaseFactsSupportStableProjectionsLoopsAndJoins(t *testing.T) {
	_, diag := checkFlowSource(t, `enum Choice {
	Left: { value: i32 },
	Right: { value: i32 },
}

struct Holder { choice: Choice }

fn Field(holder: Holder) -> i32 {
	if holder.choice is Choice::Left {
		return holder.choice.value;
	}
	return 0;
}

fn Index(values: [1]Choice, index: usize) -> i32 {
	if values[index] is Choice::Left {
		return values[index].value;
	}
	return 0;
}

fn Loop(choice: Choice) -> i32 {
	for choice is Choice::Left {
		return choice.value;
	}
	return 0;
}

fn Join(choice: Choice, flag: bool) -> i32 {
	if flag {
		if !(choice is Choice::Left) { return 0; }
	} else {
		if !(choice is Choice::Left) { return 0; }
	}
	return choice.value;
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected projection/CFG diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestNamedEnumCaseFactsInvalidateThroughAliasAndIndexDependency(t *testing.T) {
	tests := []struct {
		name string
		src  string
	}{
		{name: "mutable alias", src: `enum Choice { Left: { value: i32 }, Right: { value: i32 } }
struct Holder { choice: Choice }
fn Clear(holder: &mut Holder) { holder.choice = Choice::Right with .{ value = 0 }; }
fn Read(mut holder: Holder) -> i32 {
	if holder.choice is Choice::Left {
		Clear(&mut holder);
		return holder.choice.value;
	}
	return 0;
}`},
		{name: "index binding", src: `enum Choice { Left: { value: i32 }, Right: { value: i32 } }
fn Read(values: [2]Choice, mut index: usize) -> i32 {
	if values[index] is Choice::Left {
		index = 1;
		return values[index].value;
	}
	return 0;
}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, diag := checkFlowSource(t, test.src)
			if !diag.HasErrors() {
				t.Fatal("expected invalidated exact-case field diagnostic")
			}
		})
	}
}

func TestUnstableNamedEnumCaseTestDoesNotCreateReusableFact(t *testing.T) {
	_, diag := checkFlowSource(t, `enum Choice { Left: { value: i32 }, Right: { value: i32 } }
fn Make() -> Choice { return Choice::Left with .{ value = 1 }; }
fn IsLeft() -> bool { return Make() is Choice::Left; }`)
	if diag.HasErrors() {
		t.Fatalf("unstable case test should remain valid without refinement:\n%s", diag.EmitAllToString())
	}
}

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
		references: []originStateFact{
			{storage: outerOrigins, value: outerOrigins},
			{storage: innerOrigins, value: innerOrigins},
		},
		rawPointers: []originStateFact{
			{storage: outerOrigins, value: outerOrigins},
			{storage: innerOrigins, value: innerOrigins},
		},
	}

	clearFlowScope(innerScope, &state)

	if len(state.variants) != 1 || state.variants[0].origins[0].Root != outer {
		t.Fatalf("variant facts after scope exit = %#v, want only outer fact", state.variants)
	}
	if len(originValues(state.references, innerOrigins)) != 0 {
		t.Fatal("scope exit retained inner reference origin")
	}
	if len(originValues(state.rawPointers, innerOrigins)) != 0 {
		t.Fatal("scope exit retained inner raw-pointer origin")
	}
	if len(originValues(state.references, outerOrigins)) != 1 || len(originValues(state.rawPointers, outerOrigins)) != 1 {
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

func TestMergeFlowStatesTreatsMissingOriginAsUnknown(t *testing.T) {
	pointer := symbols.New("pointer", symbols.SymbolVar, nil, nil)
	left := symbols.New("left", symbols.SymbolVar, nil, nil)
	right := symbols.New("right", symbols.SymbolVar, nil, nil)
	pointerOrigins := []place.Origin{{Root: pointer}}
	leftState := newFlowState()
	leftState.rawPointers = setOriginFact(leftState.rawPointers, pointerOrigins, []place.Origin{{Root: left}})

	unknown := mergeFlowStates(leftState, newFlowState())
	if known := originValues(unknown.rawPointers, pointerOrigins); len(known) != 0 {
		t.Fatalf("one unknown predecessor retained raw-pointer origins: %#v", known)
	}

	rightState := newFlowState()
	rightState.rawPointers = setOriginFact(rightState.rawPointers, pointerOrigins, []place.Origin{{Root: right}})
	known := mergeFlowStates(leftState, rightState)
	want := []place.Origin{{Root: left}, {Root: right}}
	if got := originValues(known.rawPointers, pointerOrigins); !place.SameOrigins(got, want) {
		t.Fatalf("known predecessor origins = %#v, want %#v", got, want)
	}
}

func TestInvalidateVariantOriginsClearsIndexDependencies(t *testing.T) {
	values := symbols.New("values", symbols.SymbolParam, nil, nil)
	index := symbols.New("index", symbols.SymbolVar, nil, nil)
	state := flowState{variants: []variantStateFact{{
		origins: []place.Origin{{Root: values, Projections: []place.OriginProjection{{
			Kind: place.OriginBindingIndex, Binding: index,
		}}}},
		cases:        []int{1},
		caseCount:    2,
		dependencies: []*symbols.Symbol{index},
	}}}

	invalidateVariantOrigins(&state, []place.Origin{{Root: index}})

	if len(state.variants) != 0 {
		t.Fatalf("index mutation retained dependent variant fact: %#v", state.variants)
	}
}
