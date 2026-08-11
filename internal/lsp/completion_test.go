package lsp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"compiler/pkg/peeper"
)

func completionAtSource(t *testing.T, state *ServerState, filePath, src string) []CompletionItem {
	t.Helper()
	clean, position := markerPosition(t, src)
	state.Cache[filePath] = clean
	items, err := state.HandleCompletion(CompletionParams{
		TextDocumentPositionParams: TextDocumentPositionParams{
			TextDocument: TextDocumentIdentifier{URI: DocumentURI(pathToURI(filePath))},
			Position:     position,
		},
	})
	if err != nil {
		t.Fatalf("HandleCompletion failed: %v", err)
	}
	return items
}

func completionLabels(items []CompletionItem) []string {
	labels := make([]string, len(items))
	for i, item := range items {
		labels[i] = item.Label
	}
	return labels
}

func TestCompletionAdvertisesTriggersAndDispatchesRequest(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "main"+peeper.SourceExt)
	fileURI := DocumentURI(pathToURI(filePath))
	rootURI := DocumentURI(pathToURI(root))
	initializeParams, err := json.Marshal(InitializeParams{RootURI: &rootURI})
	if err != nil {
		t.Fatalf("marshal initialize params: %v", err)
	}
	openParams, err := json.Marshal(DidOpenTextDocumentParams{TextDocument: TextDocumentItem{
		URI: fileURI, Text: "fn Visible() {}\nfn main() { Vis }\n",
	}})
	if err != nil {
		t.Fatalf("marshal open params: %v", err)
	}
	completionParams, err := json.Marshal(CompletionParams{TextDocumentPositionParams: TextDocumentPositionParams{
		TextDocument: TextDocumentIdentifier{URI: fileURI},
		Position:     Position{Line: 1, Character: 15},
	}})
	if err != nil {
		t.Fatalf("marshal completion params: %v", err)
	}
	initID := json.RawMessage("1")
	completionID := json.RawMessage("2")
	var input bytes.Buffer
	for _, request := range []Request{
		{JSONRPC: "2.0", ID: &initID, Method: "initialize", Params: initializeParams},
		{JSONRPC: "2.0", Method: "textDocument/didOpen", Params: openParams},
		{JSONRPC: "2.0", ID: &completionID, Method: "textDocument/completion", Params: completionParams},
		{JSONRPC: "2.0", Method: "exit"},
	} {
		if err := writeMessage(&input, request); err != nil {
			t.Fatalf("write %s: %v", request.Method, err)
		}
	}

	var output bytes.Buffer
	if err := Run(bytes.NewReader(input.Bytes()), &output); err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	reader := bufio.NewReader(bytes.NewReader(output.Bytes()))
	responses := make(map[string]json.RawMessage)
	for {
		message, err := readMessage(reader)
		if err != nil {
			break
		}
		var response struct {
			ID     json.RawMessage `json:"id"`
			Result json.RawMessage `json:"result"`
		}
		if json.Unmarshal(message, &response) == nil && len(response.ID) > 0 {
			responses[string(response.ID)] = response.Result
		}
	}

	var initialized InitializeResult
	if err := json.Unmarshal(responses["1"], &initialized); err != nil {
		t.Fatalf("unmarshal initialize result: %v", err)
	}
	wantTriggers := []string{".", ":", "/", "\""}
	if initialized.Capabilities.CompletionProvider == nil || !slices.Equal(initialized.Capabilities.CompletionProvider.TriggerCharacters, wantTriggers) {
		t.Fatalf("completion triggers = %#v, want %#v", initialized.Capabilities.CompletionProvider, wantTriggers)
	}
	var items []CompletionItem
	if err := json.Unmarshal(responses["2"], &items); err != nil {
		t.Fatalf("unmarshal completion result: %v", err)
	}
	if !slices.Contains(completionLabels(items), "Visible") {
		t.Fatalf("completion labels = %v, want Visible", completionLabels(items))
	}
}

func TestCompletionLexicalScopeAndDeclarationOrder(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "main"+peeper.SourceExt)
	source := "const ModuleValue: i32 = 1;\n" +
		"fn ModuleFunction() {}\n" +
		"fn run(parameter: i32) {\n" +
		"\tlet outer: i32 = 1;\n" +
		"\t{\n" +
		"\t\tlet before: i32 = outer;\n" +
		"\t\tbe__CURSOR__\n" +
		"\t\tlet below: i32 = parameter;\n" +
		"\t}\n" +
		"}\n"

	state := NewServerState()
	state.RootDir = root
	items := completionAtSource(t, state, filePath, source)
	if got := completionLabels(items); !slices.Equal(got, []string{"before"}) {
		t.Fatalf("completion labels = %v, want [before]", got)
	}
	if edit := items[0].TextEdit; edit.Range.Start.Character != 2 || edit.Range.End.Character != 4 || edit.NewText != "before" {
		t.Fatalf("unexpected text edit: %#v", edit)
	}

	items = completionAtSource(t, state, filePath, strings.Replace(source, "be__CURSOR__", "par__CURSOR__", 1))
	if got := completionLabels(items); !slices.Equal(got, []string{"parameter"}) {
		t.Fatalf("parameter completion labels = %v", got)
	}
	items = completionAtSource(t, state, filePath, strings.Replace(source, "be__CURSOR__", "Module__CURSOR__", 1))
	if got := completionLabels(items); !slices.Equal(got, []string{"ModuleFunction", "ModuleValue"}) {
		t.Fatalf("module completion labels = %v", got)
	}
}

func TestCompletionSelectorUsesFieldsMethodsAndFullIdentifierRange(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "main"+peeper.SourceExt)
	source := "struct Point {\n\tName: i32,\n\tNumber: i32,\n}\n" +
		"fn (self: &Point) Normalize() -> i32 { return self.Number; }\n" +
		"fn inspect(point: &Point) -> i32 {\n\treturn point.Na__CURSOR__me;\n}\n"
	state := NewServerState()
	state.RootDir = root
	items := completionAtSource(t, state, filePath, source)
	if got := completionLabels(items); !slices.Equal(got, []string{"Name"}) {
		t.Fatalf("field completion labels = %v", got)
	}
	if edit := items[0].TextEdit; edit.Range.Start.Character != 14 || edit.Range.End.Character != 18 {
		t.Fatalf("selector replacement range = %#v, want characters 14..18", edit.Range)
	}

	items = completionAtSource(t, state, filePath, strings.Replace(source, "Na__CURSOR__me", "__CURSOR__", 1))
	if got := completionLabels(items); !slices.Equal(got, []string{"Name", "Normalize", "Number"}) {
		t.Fatalf("selector completion labels = %v", got)
	}
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		if _, duplicate := seen[item.Label]; duplicate {
			t.Fatalf("duplicate selector completion %q", item.Label)
		}
		seen[item.Label] = struct{}{}
	}
}

func TestCompletionSelectorIncludesStringIntrinsics(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "main"+peeper.SourceExt)
	source := "fn main() {\n\tlet text: str = \"a\";\n\ttext.__CURSOR__;\n}\n"
	state := NewServerState()
	state.RootDir = root
	items := completionAtSource(t, state, filePath, source)
	labels := completionLabels(items)
	for _, want := range []string{"as_bytes", "as_chars", "len"} {
		if !slices.Contains(labels, want) {
			t.Fatalf("string completion labels = %v, missing %q", labels, want)
		}
	}
}

func TestCompletionSelectorIncludesInterfaceMethods(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "main"+peeper.SourceExt)
	source := "iface Writer {\n\tfn (&mut Self) write(value: i32)\n}\n" +
		"fn use(writer: Writer) {\n\twriter.__CURSOR__;\n}\n"
	state := NewServerState()
	state.RootDir = root
	items := completionAtSource(t, state, filePath, source)
	if got := completionLabels(items); !slices.Equal(got, []string{"write"}) {
		t.Fatalf("interface completion labels = %v", got)
	}
	if items[0].Kind != completionKindMethod {
		t.Fatalf("interface completion kind = %d, want method", items[0].Kind)
	}
}

func TestCompletionInnerScopeShadowsOuterBinding(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "main"+peeper.SourceExt)
	source := "fn inspect() {\n" +
		"\tlet value: i32 = 1;\n" +
		"\t{\n" +
		"\t\tlet value: bool = true;\n" +
		"\t\tval__CURSOR__\n" +
		"\t}\n" +
		"}\n"
	state := NewServerState()
	state.RootDir = root
	items := completionAtSource(t, state, filePath, source)
	if got := completionLabels(items); !slices.Equal(got, []string{"value"}) {
		t.Fatalf("shadowed completion labels = %v", got)
	}
	if !strings.Contains(items[0].Detail, "bool") {
		t.Fatalf("shadowed completion detail = %q, want inner bool binding", items[0].Detail)
	}
}

func TestCompletionQualifiedImportOnlyExposesExports(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceProjectConfig(t, root, "app")
	mainPath := filepath.Join(root, peeper.SourceDirName, peeper.MainFileName)
	externalPath := filepath.Join(root, peeper.SourceDirName, "external"+peeper.SourceExt)
	writeWorkspaceFile(t, externalPath, "fn PublicFunction() -> i32 { return 1; }\nfn privateFunction() {}\nstruct PublicType {}\nconst PublicValue: i32 = 1;\n")
	source := "import \"app/external\";\nfn main() -> i32 { return external::Pub__CURSOR__; }\n"
	state := NewServerState()
	state.RootDir = root
	items := completionAtSource(t, state, mainPath, source)
	if got := completionLabels(items); !slices.Equal(got, []string{"PublicFunction", "PublicType", "PublicValue"}) {
		t.Fatalf("qualified completion labels = %v", got)
	}
	if slices.Contains(completionLabels(items), "privateFunction") {
		t.Fatal("private symbol leaked into qualified completion")
	}
}

func TestCompletionImportPathsAndReplacementRanges(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceProjectConfig(t, root, "app")
	mainPath := filepath.Join(root, peeper.SourceDirName, peeper.MainFileName)
	utilPath := filepath.Join(root, peeper.SourceDirName, "util"+peeper.SourceExt)
	writeWorkspaceFile(t, utilPath, "fn Helper() {}\n")
	writeWorkspaceFile(t, filepath.Join(root, peeper.SourceDirName, "nested", "child"+peeper.SourceExt), "fn Child() {}\n")

	state := NewServerState()
	state.RootDir = root
	items := completionAtSource(t, state, mainPath, "import \"app/u__CURSOR__\";\nfn main() {}\n")
	if got := completionLabels(items); !slices.Equal(got, []string{"app/util"}) {
		t.Fatalf("import completion labels = %v", got)
	}
	if edit := items[0].TextEdit; edit.Range.Start != (Position{Line: 0, Character: 8}) || edit.Range.End != (Position{Line: 0, Character: 13}) {
		t.Fatalf("import replacement range = %#v", edit.Range)
	}

	items = completionAtSource(t, state, mainPath, "import \"__CURSOR__\nfn main() {}\n")
	if got := completionLabels(items); !slices.Equal(got, []string{"app/"}) {
		t.Fatalf("unfinished import labels = %v", got)
	}
	items = completionAtSource(t, state, mainPath, "import \"app/__CURSOR__\";\nfn main() {}\n")
	if got := completionLabels(items); !slices.Equal(got, []string{"app/nested/", "app/util"}) {
		t.Fatalf("root import labels = %v", got)
	}
	if items[0].Kind != completionKindFolder || items[1].Kind != completionKindFile {
		t.Fatalf("import kinds = %d, %d", items[0].Kind, items[1].Kind)
	}
}

func TestCompletionReturnsEmptyInCommentsUnrelatedStringsAndUnknownContexts(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "main"+peeper.SourceExt)
	state := NewServerState()
	state.RootDir = root
	for _, source := range []string{
		"fn main() { // val__CURSOR__\n}\n",
		"fn main() { let text: cstr = \"val__CURSOR__\"; }\n",
		"fn value() {}\nfn main() { let char = 'val__CURSOR__'; }\n",
		"fn value() {}\nfn main() { let byte = b'val__CURSOR__'; }\n",
		"fn main() { 1+__CURSOR__2; }\n",
		"fn main() { unknown.__CURSOR__; }\n",
		"fn main() { missing::__CURSOR__; }\n",
	} {
		if items := completionAtSource(t, state, filePath, source); len(items) != 0 {
			t.Fatalf("completion for %q = %v, want empty", source, completionLabels(items))
		}
	}
}

func TestCompletionUsesCompilerColumnAfterNonBMPText(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "main"+peeper.SourceExt)
	source := "fn inspect() { let text: cstr = \"🙂🙂🙂🙂\"; { let inner: i32 = 1; inn__CURSOR__; } }\n"
	state := NewServerState()
	state.RootDir = root
	items := completionAtSource(t, state, filePath, source)
	if got := completionLabels(items); !slices.Equal(got, []string{"inner"}) {
		t.Fatalf("non-BMP scope completion labels = %v", got)
	}
}

func TestCompletionPositionConversionUsesUTF16(t *testing.T) {
	text := "🙂value\nnext"
	offset, ok := offsetAtPosition(text, Position{Line: 0, Character: 2})
	if !ok || offset != len("🙂") {
		t.Fatalf("offsetAtPosition = %d, %v", offset, ok)
	}
	if got := positionAtOffset(text, len("🙂value\n")+2); got != (Position{Line: 1, Character: 2}) {
		t.Fatalf("positionAtOffset = %#v", got)
	}
}
