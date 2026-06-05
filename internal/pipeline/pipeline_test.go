package pipeline

import (
	"strings"
	"testing"

	"compiler/core/diagnostics"
	"compiler/internal/context"
	"compiler/internal/frontend/lexer"
	"compiler/internal/frontend/parser"
)

func parseModuleForTest(filePath, src string, origin context.ModuleOrigin, diag *diagnostics.DiagnosticBag) *context.Module {
	return &context.Module{
		Key:        context.ModuleKeyFor(origin, filePath),
		ImportPath: strings.TrimSuffix(filePath, ".em"),
		FilePath:   filePath,
		Origin:     origin,
		AST:        parser.ParseModule(filePath, lexer.Lex(filePath, src, diag), diag),
		Imports:    make(map[string]context.ResolvedImport),
	}
}

func TestRunOrderedProcessesPreludeBeforeEntry(t *testing.T) {
	const preludePath = "_builtin_library/global.em"
	const entryPath = "entry.em"

	preludeSrc := `let stdin:  i32 = 0;
let stdout: i32 = 1;
let stderr: i32 = 2;

#[extern]
fn write(fd: i32, buf: cstr, n: i32) -> i32;
`
	entrySrc := `fn main() -> i32 {
	let msg: cstr = "Hello from Ember runtime ABI!\n";
	let _ = write(stdout, msg, 30);
	return 0;
}`

	diag := diagnostics.NewDiagnosticBag(entryPath)
	diag.AddSourceContent(preludePath, preludeSrc)
	diag.AddSourceContent(entryPath, entrySrc)
	ctx := context.New(".", ".em", diag)

	prelude := parseModuleForTest(preludePath, preludeSrc, context.ModuleOriginStdlib, diag)
	prelude.Key = "core:prelude/global"
	prelude.ImportPath = "prelude/global"
	entry := parseModuleForTest(entryPath, entrySrc, context.ModuleOriginLocal, diag)
	ctx.AddModule(prelude)
	ctx.AddModule(entry)

	New(ctx).runOrdered([]*context.Module{entry, prelude}, diag)

	for _, item := range diag.Diagnostics() {
		if item == nil {
			continue
		}
		if item.Code == diagnostics.ErrUndefinedSymbol && strings.Contains(item.Message, "write") {
			t.Fatalf("unexpected undefined prelude symbol for write: %s", diag.EmitAllToString())
		}
		if item.Code == diagnostics.ErrUndefinedSymbol && strings.Contains(item.Message, "stdout") {
			t.Fatalf("unexpected undefined prelude symbol for stdout: %s", diag.EmitAllToString())
		}
	}
}

func TestRunOrderedAllowsExpressionStatements(t *testing.T) {
	const preludePath = "_builtin_library/global.em"
	const entryPath = "entry.em"

	preludeSrc := `let stdout: i32 = 1;

#[extern]
fn write(fd: i32, buf: cstr, n: i32) -> i32;
`
	entrySrc := `fn main() -> i32 {
	let msg: cstr = "Hello from Ember runtime ABI!\n";
	write(stdout, msg, 30);
	return 0;
}`

	diag := diagnostics.NewDiagnosticBag(entryPath)
	diag.AddSourceContent(preludePath, preludeSrc)
	diag.AddSourceContent(entryPath, entrySrc)
	ctx := context.New(".", ".em", diag)

	prelude := parseModuleForTest(preludePath, preludeSrc, context.ModuleOriginStdlib, diag)
	prelude.Key = "core:prelude/global"
	prelude.ImportPath = "prelude/global"
	entry := parseModuleForTest(entryPath, entrySrc, context.ModuleOriginLocal, diag)
	ctx.AddModule(prelude)
	ctx.AddModule(entry)

	New(ctx).runOrdered([]*context.Module{entry, prelude}, diag)

	for _, item := range diag.Diagnostics() {
		if item == nil {
			continue
		}
		if item.Code == diagnostics.ErrInvalidStatement && strings.Contains(item.Message, "expression statements") {
			t.Fatalf("unexpected invalid expression statement diagnostic: %s", diag.EmitAllToString())
		}
	}
}
