package lsp

import (
	"path/filepath"
	"strings"
	"testing"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/project"
	"compiler/internal/semantics/bindingresult"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
	"compiler/pkg/peeper"
)

func TestHoverAndCompletionDoNotConsumeUsageEvidence(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "main"+peeper.SourceExt)
	state := NewServerState()
	state.RootDir = root

	assertUnused := func(query string) {
		t.Helper()
		ctx := state.LastCtx
		module, ok := ctx.ModuleByFile(filePath)
		if !ok || module == nil || module.ModuleScope == nil {
			t.Fatalf("%s compilation did not retain module", query)
		}
		sym, ok := module.ModuleScope.LookupLocal("unused")
		if !ok || sym == nil {
			t.Fatalf("%s compilation did not retain unused function symbol", query)
		}
		if sym.IsUsed() {
			t.Fatalf("%s marked unused function as used", query)
		}
	}

	completionAtSource(t, state, filePath, "fn unused() {}\nfn main() { __CURSOR__ }\n")
	assertUnused("completion")
	if hover := hoverAtSource(t, state, filePath, "fn __CURSOR__unused() {}\nfn main() {}\n"); hover == nil {
		t.Fatal("expected hover for unused function")
	}
	assertUnused("hover")

	for run := 0; run < 2; run++ {
		compiled, _ := state.recompile(filePath)
		found := false
		for _, item := range compiled.Diagnostics.Diagnostics() {
			found = found || item.Code == diagnostics.WarnUnusedPrivateFunction
		}
		if !found {
			t.Fatalf("compile %d lost unused-function warning after queries", run+1)
		}
	}
}

func TestTypeHoverShowsInvalidInsteadOfDeclarationType(t *testing.T) {
	ctx := project.New(".", peeper.SourceExt, nil)
	module := &project.Module{ModuleScope: symbols.NewScope(nil), Bindings: bindingresult.New()}
	base := &typeinfo.DefinedType{
		Name: "Box", Identity: "main::Box", Kind: typeinfo.DefinedKindStruct,
		TypeParameters: []*typeinfo.TypeParameterType{{Name: "T", OwnerIdentity: "main::Box", Index: 0}},
	}
	baseSymbol := symbols.New("Box", symbols.SymbolType, nil, nil)
	baseSymbol.BindType(base)
	if err := module.ModuleScope.Declare(baseSymbol); err != nil {
		t.Fatalf("declare generic type: %v", err)
	}

	typeNode := &ast.NamedType{NodeIDHolder: ast.NodeIDHolder{NodeID: 1}, Name: "Box"}
	name := &ast.Ident{NodeIDHolder: ast.NodeIDHolder{NodeID: 2}, Name: "Alias"}
	declaration := &ast.TypeAliasDecl{NodeIDHolder: ast.NodeIDHolder{NodeID: 3}, Name: name, Type: typeNode}
	aliasSymbol := symbols.New("Alias", symbols.SymbolType, declaration, nil)
	aliasSymbol.BindType(&typeinfo.DefinedType{Name: "Alias", Identity: "main::Alias"})
	module.Bindings.Bind(name, aliasSymbol)

	subject := resolveTypeHoverSubject(&cursorContext{
		ctx:     ctx,
		module:  module,
		node:    typeNode,
		parents: map[ast.NodeID]ast.Node{typeNode.ID(): declaration},
	})
	if subject == nil || subject.TypeQueryStatus != project.TypeQueryInvalid {
		t.Fatalf("invalid declaration hover subject = %#v", subject)
	}
	if rendered := renderHoverSubject(subject); !strings.Contains(rendered, "<invalid>") || strings.Contains(rendered, "Alias") {
		t.Fatalf("invalid declaration hover = %q", rendered)
	}
}

func TestTypeHoverShowsLoadingForUnavailableGenericInstance(t *testing.T) {
	ctx := project.New(".", peeper.SourceExt, nil)
	module := &project.Module{ModuleScope: symbols.NewScope(nil)}
	base := &typeinfo.DefinedType{
		Name: "Box", Identity: "main::Box", Kind: typeinfo.DefinedKindStruct,
		TypeParameters: []*typeinfo.TypeParameterType{{Name: "T", OwnerIdentity: "main::Box", Index: 0}},
	}
	symbol := symbols.New("Box", symbols.SymbolType, nil, nil)
	symbol.BindType(base)
	if err := module.ModuleScope.Declare(symbol); err != nil {
		t.Fatalf("declare generic type: %v", err)
	}
	typeNode := &ast.AppliedType{
		NodeIDHolder: ast.NodeIDHolder{NodeID: 1},
		Name:         &ast.Ident{NodeIDHolder: ast.NodeIDHolder{NodeID: 2}, Name: "Box"},
		TypeArgs: []ast.TypeExpr{
			&ast.NamedType{NodeIDHolder: ast.NodeIDHolder{NodeID: 3}, Name: "i32"},
		},
	}
	declaration := &ast.LetDecl{NodeIDHolder: ast.NodeIDHolder{NodeID: 4}, Type: typeNode}
	context := &cursorContext{
		ctx:     ctx,
		module:  module,
		node:    typeNode,
		parents: map[ast.NodeID]ast.Node{typeNode.ID(): declaration},
	}

	subject := resolveTypeHoverSubject(context)
	if subject == nil || subject.TypeQueryStatus != project.TypeQueryLoading {
		t.Fatalf("unavailable generic hover subject = %#v, want loading", subject)
	}
	rendered := renderHoverSubject(subject)
	if !strings.Contains(rendered, "<loading...>") || strings.Contains(rendered, "<invalid>") {
		t.Fatalf("loading hover = %q", rendered)
	}
	if symbol.IsUsed() {
		t.Fatal("generic hover query marked type symbol used")
	}
}
