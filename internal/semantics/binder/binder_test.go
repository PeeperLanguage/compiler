package binder

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/lexer"
	"compiler/internal/frontend/parser"
	"compiler/internal/project"
	"compiler/internal/semantics/collector"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
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

func TestBindInstantiatesGenericNamedTypes(t *testing.T) {
	const filePath = "binder_generic_instances_test" + peeper.SourceExt
	const src = `struct Box<T> { value: T }
struct Node<T> { next: *Node<T> }
type Maybe<T> = ?T;
iface Reader<T> { fn (&Self) read() -> T }
enum Choice<T> { Left, Right }
fn Use(box: Box<i32>, again: Box<i32>, other: Box<i64>, nested: Box<Box<i32>>, node: Node<i32>, maybe: Maybe<i32>, reader: Reader<i32>, choice: Choice<i32>) {}`
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

	use, ok := module.ModuleScope.LookupLocal("Use")
	if !ok || use == nil || use.Kind != symbols.SymbolFunc {
		t.Fatal("missing Use function")
	}
	fn, ok := use.Type.(*typeinfo.FuncType)
	if !ok || len(fn.Params) != 8 {
		t.Fatalf("Use type = %#v", use.Type)
	}
	box, ok := fn.Params[0].(*typeinfo.DefinedType)
	if !ok || box.Text() != "Box<i32>" {
		t.Fatalf("Box<i32> instance = %#v", fn.Params[0])
	}
	if fn.Params[1] != box {
		t.Fatal("repeated Box<i32> applications must reuse one cached instance")
	}
	other, ok := fn.Params[2].(*typeinfo.DefinedType)
	if !ok || other == box || other.Identity == box.Identity {
		t.Fatalf("Box<i64> instance = %#v, want distinct semantic identity", fn.Params[2])
	}
	nested, ok := fn.Params[3].(*typeinfo.DefinedType)
	if !ok {
		t.Fatalf("nested Box instance = %#v", fn.Params[3])
	}
	nestedStruct, ok := typeinfo.Underlying(nested).(*typeinfo.StructType)
	if !ok || len(nestedStruct.Fields) != 1 || nestedStruct.Fields[0].Type != box {
		t.Fatalf("nested Box payload = %#v, want cached Box<i32>", nestedStruct)
	}
	node, ok := fn.Params[4].(*typeinfo.DefinedType)
	if !ok {
		t.Fatalf("Node<i32> instance = %#v", fn.Params[4])
	}
	nodeStruct, ok := typeinfo.Underlying(node).(*typeinfo.StructType)
	if !ok || len(nodeStruct.Fields) != 1 {
		t.Fatalf("Node<i32> payload = %#v", nodeStruct)
	}
	next, ok := nodeStruct.Fields[0].Type.(*typeinfo.OwnedPtrType)
	if !ok || next.Target != node {
		t.Fatalf("recursive Node<i32> target = %#v, want provisional instance", nodeStruct.Fields[0].Type)
	}
	maybe, ok := typeinfo.Underlying(fn.Params[5]).(*typeinfo.OptionalType)
	if !ok || !typeinfo.SameType(maybe.Inner, &typeinfo.IntegerType{Signed: true, Bits: 32}) {
		t.Fatalf("Maybe<i32> payload = %#v", fn.Params[5])
	}
	reader, ok := typeinfo.Underlying(fn.Params[6]).(*typeinfo.InterfaceType)
	if !ok || len(reader.Methods) != 1 || !typeinfo.SameType(reader.Methods[0].Return, &typeinfo.IntegerType{Signed: true, Bits: 32}) {
		t.Fatalf("Reader<i32> payload = %#v", fn.Params[6])
	}
	choice, ok := fn.Params[7].(*typeinfo.DefinedType)
	if !ok || choice.Text() != "Choice<i32>" {
		t.Fatalf("Choice<i32> instance = %#v", fn.Params[7])
	}
}

func TestBindCanonicalizesTransparentGenericArguments(t *testing.T) {
	const filePath = "binder_generic_alias_identity_test" + peeper.SourceExt
	const src = `type BaseInt = i32;
type MyInt = BaseInt;
enum Choice<T> { Left, Right }
fn Use(alias: Choice<MyInt>, canonical: Choice<i32>) {}`
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

	use, ok := module.ModuleScope.LookupLocal("Use")
	if !ok || use == nil {
		t.Fatal("missing Use function")
	}
	fn, ok := use.Type.(*typeinfo.FuncType)
	if !ok || len(fn.Params) != 2 {
		t.Fatalf("Use type = %#v", use.Type)
	}
	if fn.Params[0] != fn.Params[1] {
		t.Fatalf("transparent alias split generic identity: %#v != %#v", fn.Params[0], fn.Params[1])
	}
}

func TestBindRejectsExpandingGenericRecursion(t *testing.T) {
	if os.Getenv("PEEPER_TEST_EXPANDING_GENERIC_RECURSION") == "1" {
		tests := []struct {
			name   string
			source string
		}{
			{
				name: "expanding",
				source: `struct Loop<T> { next: *Loop<Loop<T>> }
fn Use(value: &Loop<i32>) {}`,
			},
			{
				name: "transformed",
				source: `struct Swap<A, B> { next: *Swap<B, A> }
fn Use(value: &Swap<i32, str>) {}`,
			},
		}
		for _, test := range tests {
			filePath := "binder_" + test.name + "_generic_recursion_test" + peeper.SourceExt
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
			out := diag.EmitAllToString()
			if !diag.HasErrors() || !strings.Contains(out, diagnostics.ErrInvalidType) ||
				!strings.Contains(out, "recursive generic applications must preserve exact type arguments") {
				t.Fatalf("%s: expected regular-recursion diagnostic, got:\n%s", test.name, out)
			}
		}
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestBindRejectsExpandingGenericRecursion$")
	cmd.Env = append(os.Environ(), "PEEPER_TEST_EXPANDING_GENERIC_RECURSION=1")
	output, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatal("expanding generic recursion did not terminate")
	}
	if err != nil {
		t.Fatalf("expanding generic recursion child failed: %v\n%s", err, output)
	}
}

func TestBindRequiresExactNamedTypeArguments(t *testing.T) {
	tests := []struct {
		name   string
		source string
		want   string
	}{
		{name: "missing", source: `struct Box<T> { value: T } fn Use(value: Box) {}`, want: "expects 1 type argument"},
		{name: "extra", source: `struct Box<T> { value: T } fn Use(value: Box<i32, i64>) {}`, want: "expects 1 type argument"},
		{name: "nongeneric", source: `struct Plain {} fn Use(value: Plain<i32>) {}`, want: "expects 0 type arguments"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			const filePath = "binder_generic_arity_test" + peeper.SourceExt
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
			if !diag.HasErrors() || !strings.Contains(diag.EmitAllToString(), test.want) {
				t.Fatalf("expected %q diagnostic, got:\n%s", test.want, diag.EmitAllToString())
			}
		})
	}
}
