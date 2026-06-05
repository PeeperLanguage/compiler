package pipeline

import (
	"strings"
	"testing"

	"compiler/core/diagnostics"
	"compiler/internal/context"
	"compiler/internal/frontend/lexer"
	"compiler/internal/frontend/parser"
)

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

	prelude := &context.Module{
		Key:        "core:prelude/global",
		ImportPath: "prelude/global",
		FilePath:   preludePath,
		Origin:     context.ModuleOriginStdlib,
		AST:        parser.ParseModule(preludePath, lexer.Lex(preludePath, preludeSrc, diag), diag),
		Imports:    make(map[string]context.ResolvedImport),
	}
	entry := &context.Module{
		Key:        context.ModuleKeyFor(context.ModuleOriginLocal, entryPath),
		ImportPath: "entry",
		FilePath:   entryPath,
		Origin:     context.ModuleOriginLocal,
		AST:        parser.ParseModule(entryPath, lexer.Lex(entryPath, entrySrc, diag), diag),
		Imports:    make(map[string]context.ResolvedImport),
	}
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
