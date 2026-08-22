package binder

import (
	"testing"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/lexer"
	"compiler/internal/frontend/parser"
	"compiler/internal/project"
	"compiler/internal/semantics/collector"
	"compiler/pkg/peeper"
)

func TestBindBuildsSortedOperationFunctionCatalog(t *testing.T) {
	const filePath = "binder_operation_catalog_test" + peeper.SourceExt
	const src = `struct Value {}
fn Zero() {}
fn Zebra(value: &Value) {}
fn (self: &Value) Method() {}
fn Alpha(value: Value, extra: i32) {}`
	diag := diagnostics.NewDiagnosticBag()
	ctx := project.New(".", peeper.SourceExt, diag)
	module := &project.Module{
		Key:      project.ModuleKeyFor(project.ModuleOriginLocal, filePath),
		FilePath: filePath,
		Content:  src,
		AST:      parser.New(filePath, lexer.New(filePath, src, diag).Tokenize(), diag).ParseModule(),
		Imports:  make(map[string]project.ResolvedImport),
	}
	collector.Collect(ctx, module)
	Bind(ctx, module)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	functions := module.Semantics.OperationFunctions
	if len(functions) != 2 || functions[0].Name != "Alpha" || functions[1].Name != "Zebra" {
		t.Fatalf("operation functions = %#v, want [Alpha Zebra]", functions)
	}
}

func TestBindValidatesTypeDeclarationCycles(t *testing.T) {
	tests := []struct {
		name      string
		source    string
		wantCycle bool
	}{
		{
			name: "direct value cycle",
			source: `struct A { b: B }
struct B { a: A }`,
			wantCycle: true,
		},
		{
			name:      "self value cycle",
			source:    `struct Node { next: Node }`,
			wantCycle: true,
		},
		{
			name:   "pointer recursion",
			source: `struct Node { next: *Node }`,
		},
		{
			name:   "raw pointer leaf",
			source: `type Address = rawptr;`,
		},
		{
			name:   "enum leaf",
			source: `enum State { Ready }`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			const filePath = "binder_type_cycle_test" + peeper.SourceExt
			diag := diagnostics.NewDiagnosticBag()
			ctx := project.New(".", peeper.SourceExt, diag)
			module := &project.Module{
				Key:      project.ModuleKeyFor(project.ModuleOriginLocal, filePath),
				FilePath: filePath,
				Content:  test.source,
				AST:      parser.New(filePath, lexer.New(filePath, test.source, diag).Tokenize(), diag).ParseModule(),
				Imports:  make(map[string]project.ResolvedImport),
			}
			collector.Collect(ctx, module)
			Bind(ctx, module)

			foundCycle := false
			for _, item := range diag.Diagnostics() {
				if item != nil && item.Code == diagnostics.ErrCircularDependency {
					foundCycle = true
					break
				}
			}
			if foundCycle != test.wantCycle {
				t.Fatalf("cycle diagnostic = %v, want %v:\n%s", foundCycle, test.wantCycle, diag.EmitAllToString())
			}
		})
	}
}
