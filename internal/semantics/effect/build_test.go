package effect_test

import (
	"testing"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/frontend/lexer"
	"compiler/internal/frontend/parser"
	"compiler/internal/ir"
	"compiler/internal/ir/cfg"
	"compiler/internal/moduleid"
	"compiler/internal/project"
	"compiler/internal/semantics/binder"
	"compiler/internal/semantics/collector"
	"compiler/internal/semantics/effect"
	"compiler/internal/semantics/place"
	"compiler/internal/semantics/resolver"
	"compiler/internal/semantics/typechecker"
	"compiler/pkg/peeper"
)

func buildEffects(t *testing.T, source string) (effect.Result, *project.Module) {
	t.Helper()
	const filePath = "effect_test" + peeper.SourceExt
	diag := diagnostics.NewDiagnosticBag()
	diag.AddSourceContent(filePath, source)
	ctx := project.New(".", peeper.SourceExt, diag)
	module := &project.Module{
		ID:       moduleid.ID{Origin: string(project.ModuleOriginLocal), ImportPath: "effect_test"},
		FilePath: filePath,
		Content:  source,
		AST:      parser.New(filePath, lexer.New(filePath, source, diag).Tokenize(), diag).ParseModule(),
		Imports:  make(map[string]project.ResolvedImport),
	}
	ctx.AddModule(module)
	collector.Collect(ctx, module)
	binder.Bind(ctx, module)
	resolver.Resolve(ctx, module)
	typechecker.Check(ctx, module)
	module.RebuildTypedASTIndex()
	module.CFG = cfg.BuildModule(module.AST, cfg.BuildQueries{
		MatchCases:          module.Typechecking.MatchCases,
		LoopGuaranteedEntry: module.Typechecking.ForLoopGuaranteedEntry,
	})
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	result := effect.Build(module.CFG, module.TypedASTNodes, effect.BuildQueries{
		Symbols:             module.Bindings.NodeSymbols,
		Scopes:              module.Bindings.BlockScopes,
		CallArguments:       module.Typechecking.CallArgumentsOrSource,
		ArmBindings:         module.Typechecking.ArmBindings,
		StringConcatenation: module.Typechecking.StringConcatenation,
		ValueUse:            module.Typechecking.ValueUse,
		ExprType:            module.EffectiveExprType,
		ReferenceArgument:   module.Typechecking.ReferenceArgument,
		SequenceCarrier:     module.Typechecking.SequenceCarrier,
	})
	if result == nil {
		t.Fatal("Build published no result")
	}
	if err := module.CFG.Validate(); err != nil {
		t.Fatalf("constructed CFG rejected: %v", err)
	}
	if err := result.Validate(module.CFG, module.TypedASTNodes); err != nil {
		t.Fatalf("published effects rejected: %v", err)
	}
	return result, module
}

// publishedOps flattens one function's effects in site order so a test can
// state the published sequence without naming SiteIDs.
func publishedOps(t *testing.T, result effect.Result, module *project.Module, name string) []string {
	t.Helper()
	symbol, found := module.ModuleScope.Lookup(name)
	if !found || symbol == nil {
		t.Fatalf("function %q missing", name)
	}
	fn, ok := symbol.ASTNode.(*ast.FnDecl)
	if !ok || fn == nil {
		t.Fatalf("function %q has no declaration", name)
	}
	graph := module.CFG.Function(ir.NodeID(fn.ID()))
	if graph == nil {
		t.Fatalf("function %q has no CFG", name)
	}
	published := make([]string, 0)
	for _, block := range graph.Blocks {
		if block == nil || !block.Reachable {
			continue
		}
		for _, site := range block.Sites {
			if site == nil {
				continue
			}
			for _, op := range result.At(graph.NodeID, site.ID) {
				published = append(published, describe(op))
			}
		}
	}
	return published
}

func describe(op effect.Op) string {
	switch op := op.(type) {
	case effect.Define:
		if op.Initialized {
			return "define " + op.Symbol.Name
		}
		return "declare " + op.Symbol.Name
	case effect.Write:
		return "write " + op.Place.Root.Name
	case effect.Use:
		if op.Place.Root == nil {
			return "use temporary"
		}
		return "use " + op.Place.Root.Name
	case effect.Borrow:
		if op.Place.Root == nil {
			return "borrow temporary"
		}
		return "borrow " + op.Place.Root.Name
	case effect.CallBegin:
		return "call"
	case effect.CallEnd:
		return "end"
	case effect.Discard:
		return "discard"
	case effect.Iterate:
		if op.Place.Root == nil {
			return "iterate temporary"
		}
		return "iterate " + op.Place.Root.Name
	}
	return "unknown"
}

func TestBuildPublishesProjectionOperands(t *testing.T) {
	for _, test := range []struct {
		name   string
		source string
		want   []string
	}{
		{
			name:   "nested read",
			source: `fn probe(matrix: [2][2]i32, i: i32, j: i32) -> i32 { return matrix[i][j]; }`,
			want:   []string{"define matrix", "define i", "define j", "use i", "use j", "use matrix"},
		},
		{
			name: "field after index",
			source: `struct Row { field: i32 }
fn probe(rows: [2]Row, i: i32) -> i32 { return rows[i].field; }`,
			want: []string{"define rows", "define i", "use i", "use rows"},
		},
		{
			name:   "write after RHS",
			source: `fn probe(mut matrix: [2][2]i32, i: i32, j: i32, value: i32) { matrix[i][j] = value; }`,
			want:   []string{"define matrix", "define i", "define j", "define value", "use value", "use i", "use j", "write matrix"},
		},
		{
			name:   "explicit borrow",
			source: `fn probe(matrix: [2][2]i32, i: i32, j: i32) { let view = &matrix[i][j]; }`,
			want:   []string{"define matrix", "define i", "define j", "use i", "use j", "borrow matrix", "define view"},
		},
		{
			name: "implicit borrow",
			source: `struct Cell { value: i32 }
fn (cell: &Cell) take() -> i32 { return cell.value; }
fn probe(matrix: [2][2]Cell, i: i32, j: i32) -> i32 { return matrix[i][j].take(); }`,
			want: []string{"define matrix", "define i", "define j", "call", "use i", "use j", "borrow matrix", "end"},
		},
		{
			name:   "slice bounds after base",
			source: `fn probe(matrix: [2][2]i32, i: i32, start: i32, end: i32) { let view = matrix[i][start..end]; }`,
			want:   []string{"define matrix", "define i", "define start", "define end", "use i", "use start", "use end", "borrow matrix", "define view"},
		},
		{
			name:   "full slice",
			source: `fn probe(matrix: [2][2]i32, i: i32) { let view = matrix[i][..]; }`,
			want:   []string{"define matrix", "define i", "use i", "borrow matrix", "define view"},
		},
		{
			name: "calls in indexes",
			source: `fn first() -> i32 { return 0; }
fn second() -> i32 { return 1; }
fn probe(matrix: [2][2]i32) -> i32 { return matrix[first()][second()]; }`,
			want: []string{"define matrix", "call", "use first", "end", "call", "use second", "end", "use matrix"},
		},
		{
			name: "temporary receiver",
			source: `struct Cell { value: i32 }
fn (cell: &Cell) take() -> i32 { return cell.value; }
fn make() -> Cell { return .Cell{value = 1}; }
fn probe() -> i32 { return make().take(); }`,
			want: []string{"call", "call", "use make", "end", "borrow temporary", "end"},
		},
		{
			name: "temporary base",
			source: `fn make() -> [2]i32 { return [2]i32{1, 2}; }
fn probe(i: i32) -> i32 { return make()[i]; }`,
			want: []string{"define i", "call", "use make", "end", "use i", "use temporary"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, module := buildEffects(t, test.source)
			got := publishedOps(t, result, module, "probe")
			if !sameOps(got, test.want) {
				t.Fatalf("published %v, want %v", got, test.want)
			}
		})
	}
}

func TestBuildPublishesSequenceIterationLifetime(t *testing.T) {
	result, module := buildEffects(t, `fn walk(values: [2]i32) {
	for value in values {}
}`)
	got := publishedOps(t, result, module, "walk")
	want := []string{"define values", "use values", "iterate values"}
	if !sameOps(got, want) {
		t.Fatalf("published %v, want %v", got, want)
	}
}

func sameOps(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// Parameters are initialized on entry, and a binding's initializer is read
// before the binding it defines. That order is what makes `let x = x` resolve
// against an outer binding rather than itself.
func TestBuildPublishesReadsBeforeTheDefineTheyInitialize(t *testing.T) {
	result, module := buildEffects(t, `fn add(a: i32, b: i32) -> i32 {
	let total = a + b;
	return total;
}`)
	got := publishedOps(t, result, module, "add")
	want := []string{
		"define a", "define b",
		"use a", "use b", "define total",
		"use total",
	}
	if !sameOps(got, want) {
		t.Fatalf("published %v, want %v", got, want)
	}
}

// An assignment reads its value before it writes its target.
func TestBuildPublishesAssignmentAsReadThenWrite(t *testing.T) {
	result, module := buildEffects(t, `fn bump(start: i32) -> i32 {
	let mut count = start;
	count = count + 1;
	return count;
}`)
	got := publishedOps(t, result, module, "bump")
	want := []string{
		"define start",
		"use start", "define count",
		"use count", "write count",
		"use count",
	}
	if !sameOps(got, want) {
		t.Fatalf("published %v, want %v", got, want)
	}
}

// A declaration with no initializer declares storage without initializing it.
// That distinction is the whole basis of definite initialization.
func TestBuildDistinguishesUninitializedDeclaration(t *testing.T) {
	result, module := buildEffects(t, `fn choose(flag: bool) -> i32 {
	let mut value: i32;
	if flag {
		value = 7;
	} else {
		value = 3;
	}
	return value;
}`)
	got := publishedOps(t, result, module, "choose")
	want := []string{
		"define flag",
		"declare value",
		"use flag",
		"write value",
		"write value",
		"use value",
	}
	if !sameOps(got, want) {
		t.Fatalf("published %v, want %v", got, want)
	}
}

// A branch condition belongs to the terminator site, not to the statement, so
// each site carries exactly the reads that happen at it.
func TestBuildPublishesBranchConditionAtTerminatorSite(t *testing.T) {
	result, module := buildEffects(t, `fn gate(flag: bool) -> i32 {
	if flag {
		return 1;
	}
	return 0;
}`)
	got := publishedOps(t, result, module, "gate")
	want := []string{"define flag", "use flag"}
	if !sameOps(got, want) {
		t.Fatalf("published %v, want %v", got, want)
	}
}

// An arm's payload binding is published before that arm body's own effects,
// because the body may read the payload it binds. The binding is published at
// the arm block's first site rather than on the case edge, which is equivalent
// because CFG construction gives every arm a fresh block reached only by its
// own case edge.
func TestBuildPublishesArmBindingBeforeArmBodyEffects(t *testing.T) {
	result, module := buildEffects(t, `enum Result {
	Ok: { value: i32 },
	Pending,
}

fn choose(outcome: Result) -> i32 {
	match outcome {
		Result::Ok with { value = payload } => {
			return payload;
		}
		Result::Pending => {
			return 0;
		}
	}
}`)
	got := publishedOps(t, result, module, "choose")
	// The subject is published at the match's own site. Ownership's liveness
	// needs it, and publishing it also closed the gap where definite
	// initialization never checked a match subject for initialization.
	want := []string{
		"define outcome",
		"use outcome",
		"define payload", "use payload",
	}
	if !sameOps(got, want) {
		t.Fatalf("published %v, want %v", got, want)
	}
}

// A use of a field or an element names that place, not the whole aggregate.
// Ownership needs the distinction: moving out of `pair.left` is a different
// decision from moving `pair`, and its diagnostics say so.
func TestBuildPublishesProjectedPlaces(t *testing.T) {
	result, module := buildEffects(t, `struct Pair { left: i32, right: i32 }

fn read(values: [3]i32, pair: Pair, index: i32) -> i32 {
	return pair.left + values[index];
}`)
	symbol, found := module.ModuleScope.Lookup("read")
	if !found {
		t.Fatal("function read missing")
	}
	fn := symbol.ASTNode.(*ast.FnDecl)
	graph := module.CFG.Function(ir.NodeID(fn.ID()))
	if graph == nil {
		t.Fatal("function read has no CFG")
	}

	projected := make([]string, 0)
	for _, block := range graph.Blocks {
		for _, site := range block.Sites {
			for _, op := range result.At(graph.NodeID, site.ID) {
				use, ok := op.(effect.Use)
				if !ok || len(use.Place.Projections) == 0 {
					continue
				}
				for _, step := range use.Place.Projections {
					switch step.Kind {
					case place.OriginField:
						projected = append(projected, use.Place.Root.Name+"."+step.Field)
					case place.OriginIndex:
						projected = append(projected, use.Place.Root.Name+"[]")
					}
				}
			}
		}
	}
	want := []string{"pair.left", "values[]"}
	if !sameOps(projected, want) {
		t.Fatalf("projected places = %v, want %v", projected, want)
	}
}

// A method callee names a method, not storage. Publishing `point.copy` as a
// field projection of point would claim a field that does not exist, and would
// hand ownership a projected place where the real effect is a use of the
// receiver.
//
// The receiver is also where an implicit borrow lives: nothing in this source
// says `&`, but the receiver parameter is `&Point`, so passing `point` borrows
// it. The typechecker records that in ImplicitCallArguments.
func TestBuildPublishesMethodReceiverAsWholeUse(t *testing.T) {
	result, module := buildEffects(t, `struct Point { x: i32, y: i32 }

fn (self: &Point) copy() -> Point {
	return .{x = self.x, y = self.y};
}

fn choose(point: Point) -> i32 {
	let duplicate = point.copy();
	return duplicate.x;
}`)
	symbol, found := module.ModuleScope.Lookup("choose")
	if !found {
		t.Fatal("function choose missing")
	}
	fn := symbol.ASTNode.(*ast.FnDecl)
	graph := module.CFG.Function(ir.NodeID(fn.ID()))

	var receiver *effect.Borrow
	var field *effect.Use
	for _, block := range graph.Blocks {
		for _, site := range block.Sites {
			for _, op := range result.At(graph.NodeID, site.ID) {
				switch op := op.(type) {
				case effect.Borrow:
					if op.Place.Root != nil && op.Place.Root.Name == "point" {
						copied := op
						receiver = &copied
					}
				case effect.Use:
					if op.Place.Root != nil && op.Place.Root.Name == "duplicate" {
						copied := op
						field = &copied
					}
				}
			}
		}
	}
	// The receiver parameter is `&Point`, so the call borrows the receiver
	// rather than reading it, and it borrows the whole binding.
	if receiver == nil || len(receiver.Place.Projections) != 0 || receiver.Mutable {
		t.Fatalf("receiver borrow = %+v, want a shared borrow of a whole binding", receiver)
	}
	if field == nil || len(field.Place.Projections) != 1 {
		t.Fatalf("field use = %+v, want one projection", field)
	}
	// The receiver parameter is a reference, so the typechecker recorded the
	// adaptation. That evidence is what a future consumer reads to know the
	// call borrows rather than moves.
	if len(module.Typechecking.ImplicitCallArguments) == 0 {
		t.Fatal("expected the implicit receiver borrow to be published")
	}
}
