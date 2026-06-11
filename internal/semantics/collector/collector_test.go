package collector

import (
	"testing"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/frontend/lexer"
	"compiler/internal/frontend/parser"
	"compiler/internal/project"
)

func TestImportSymbolsKeepSourceLocation(t *testing.T) {
	const filePath = "collector_import_test.em"
	src := `import "external";

fn main() -> i32 {
	return 0;
}`

	diag := diagnostics.NewDiagnosticBag(filePath)
	diag.AddSourceContent(filePath, src)
	ctx := project.New(".", ".em", diag)
	modAST := parser.New(filePath, lexer.New(filePath, src, diag).Tokenize(), diag).ParseModule()
	if len(modAST.Imports) != 1 || modAST.Imports[0] == nil {
		t.Fatalf("expected one parsed import decl")
	}

	module := &project.Module{
		Key:        project.ModuleKeyFor(project.ModuleOriginLocal, filePath),
		ImportPath: "collector_import_test",
		FilePath:   filePath,
		Content:    src,
		AST:        modAST,
		Imports: map[string]project.ResolvedImport{
			"external": {
				Key:        "local:external.em",
				ImportPath: "external",
				FilePath:   "external.em",
				Origin:     project.ModuleOriginLocal,
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
	if sym.Location != ast.LocOf(modAST.Imports[0]) {
		t.Fatalf("expected import symbol location to come from import decl")
	}
}
