package collector

import (
	"testing"

	"compiler/pkg/diagnostics"
	"compiler/internal/context"
	"compiler/internal/frontend/lexer"
	"compiler/internal/frontend/parser"
)

func TestImportSymbolsKeepSourceLocation(t *testing.T) {
	const filePath = "collector_import_test.em"
	src := `import "external";

fn main() -> i32 {
	return 0;
}`

	diag := diagnostics.NewDiagnosticBag(filePath)
	diag.AddSourceContent(filePath, src)
	ctx := context.New(".", ".em", diag)
	modAST := parser.ParseModule(filePath, lexer.Lex(filePath, src, diag), diag)
	if len(modAST.Imports) != 1 || modAST.Imports[0] == nil {
		t.Fatalf("expected one parsed import decl")
	}

	module := &context.Module{
		Key:        context.ModuleKeyFor(context.ModuleOriginLocal, filePath),
		ImportPath: "collector_import_test",
		FilePath:   filePath,
		Content:    src,
		AST:        modAST,
		Imports: map[string]context.ResolvedImport{
			"external": {
				Key:        "local:external.em",
				ImportPath: "external",
				FilePath:   "external.em",
				Origin:     context.ModuleOriginLocal,
				Decl:       modAST.Imports[0],
			},
		},
	}

	Collect(ctx, module)

	sym, ok := module.ModuleScope.LookupLocal("external")
	if !ok || sym == nil {
		t.Fatalf("expected import symbol to be declared")
	}
	if sym.Location == nil {
		t.Fatalf("expected import symbol location to be preserved")
	}
	if sym.Location != modAST.Imports[0].Loc() {
		t.Fatalf("expected import symbol location to come from import decl")
	}
}
