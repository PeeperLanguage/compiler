package lsp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
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

func completionItemByKind(t *testing.T, items []CompletionItem, label string, kind int) CompletionItem {
	t.Helper()
	for _, item := range items {
		if item.Label == label && item.Kind == kind {
			return item
		}
	}
	t.Fatalf("completion items = %#v, want %q with kind %d", items, label, kind)
	return CompletionItem{}
}

func applyCompletionTextEdit(t *testing.T, text string, edit TextEdit) string {
	t.Helper()
	start, startOK := offsetAtPosition(text, edit.Range.Start)
	end, endOK := offsetAtPosition(text, edit.Range.End)
	if !startOK || !endOK || start > end {
		t.Fatalf("invalid completion edit range: %#v", edit.Range)
	}
	return text[:start] + edit.NewText + text[end:]
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
	if err := Run(io.NopCloser(bytes.NewReader(input.Bytes())), &output); err != nil {
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
	wantTriggers := []string{".", "|", ">", ":", "/", "\""}
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

func TestCompletionAndHoverUseSameFunctionHeader(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "main"+peeper.SourceExt)
	source := "fn factorial(value: i32) -> i32 {\n\tif value == 0 { return 1; }\n\treturn value * factorial(value - 1);\n}\n"
	state := NewServerState()
	state.RootDir = root

	items := completionAtSource(t, state, filePath, source+"fn use() { fact__CURSOR__ }\n")
	completion := completionItemByKind(t, items, "factorial", completionKindFunction)
	want := "(func) fn factorial(value: i32) -> i32"
	if completion.Detail != want {
		t.Fatalf("completion detail = %q, want %q", completion.Detail, want)
	}
	hover := hoverAtSource(t, state, filePath, strings.Replace(source, "factorial(value - 1)", "__CURSOR__factorial(value - 1)", 1))
	if hover == nil || !strings.Contains(hover.Contents.Value, completion.Detail) {
		t.Fatalf("hover = %#v, want shared header %q", hover, completion.Detail)
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
	if got := completionLabels(items); !slices.Equal(got, []string{"Name", "Normalize", "Number", "alloc", "inspect"}) {
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

func TestCompletionDotOffersCompilerFunctionsWithConcreteTypes(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "main"+peeper.SourceExt)
	source := "fn main() {\n\tlet text: str = \"a\";\n\ttext.__CURSOR__;\n}\n"
	state := NewServerState()
	state.RootDir = root
	items := completionAtSource(t, state, filePath, source)
	if got := completionLabels(items); !slices.Equal(got, []string{"alloc", "as_bytes", "as_chars", "len"}) {
		t.Fatalf("string completion labels = %v", got)
	}
	item := completionItemByKind(t, items, "len", completionKindFunction)
	if strings.Contains(item.Detail, "T") || !strings.Contains(item.Detail, "&str") {
		t.Fatalf("len completion detail = %q, want concrete string type", item.Detail)
	}
	if item.TextEdit.NewText != " |> len()" || item.InsertTextFormat != 2 {
		t.Fatalf("len completion edit = %#v, format = %d", item.TextEdit, item.InsertTextFormat)
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
	if !slices.Contains(completionLabels(items), "write") {
		t.Fatalf("interface completion labels = %v", completionLabels(items))
	}
	if item := completionItemByKind(t, items, "write", completionKindMethod); item.TextEdit.NewText != "write(${1:value})" {
		t.Fatalf("interface method edit = %#v", item.TextEdit)
	}
}

func TestCompletionPipeOffersMethodsAndFunctionsWithSyntaxEdits(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "main"+peeper.SourceExt)
	source := "struct Counter { value: i32, }\n" +
		"fn (self: &Counter) Read(delta: i32) -> i32 { return self.value + delta; }\n" +
		"fn Inspect(value: &Counter, fallback: i32) -> i32 { return value.value + fallback; }\n" +
		"fn use(counter: Counter) -> i32 { return counter |> __CURSOR__; }\n"
	state := NewServerState()
	state.RootDir = root
	items := completionAtSource(t, state, filePath, source)

	method := completionItemByKind(t, items, "Read", completionKindMethod)
	if method.TextEdit.NewText != ".Read(${1:delta})" || method.SortText != "1Read" || method.InsertTextFormat != 2 {
		t.Fatalf("method completion = %#v", method)
	}
	function := completionItemByKind(t, items, "Inspect", completionKindFunction)
	if function.TextEdit.NewText != "Inspect(${1:fallback})" || function.SortText != "0Inspect" || function.InsertTextFormat != 2 {
		t.Fatalf("function completion = %#v", function)
	}
	if method.TextEdit.Range.Start.Character != 48 || function.TextEdit.Range.Start.Character != 52 {
		t.Fatalf("method/function rewrite starts = %d/%d", method.TextEdit.Range.Start.Character, function.TextEdit.Range.Start.Character)
	}
	if slices.Contains(completionLabels(items), "value") {
		t.Fatalf("pipe completion exposed field: %v", completionLabels(items))
	}
}

func TestCompletionPreservesExistingCallArguments(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "main"+peeper.SourceExt)
	declarations := "struct Counter { value: i32, }\n" +
		"fn (self: &Counter) Read(delta: i32) -> i32 { return self.value + delta; }\n" +
		"fn Inspect(value: &Counter, fallback: i32) -> i32 { return value.value + fallback; }\n"
	tests := []struct {
		name       string
		expression string
		label      string
		kind       int
		want       string
	}{
		{name: "pipe function empty", expression: "counter |> Ins__CURSOR__( \t )", label: "Inspect", kind: completionKindFunction, want: "counter |> Inspect(${1:fallback})"},
		{name: "pipe function arguments", expression: "counter |> Ins__CURSOR__(3)", label: "Inspect", kind: completionKindFunction, want: "counter |> Inspect(3)"},
		{name: "pipe method empty", expression: "counter |> Re__CURSOR__()", label: "Read", kind: completionKindMethod, want: "counter.Read(${1:delta})"},
		{name: "pipe method arguments", expression: "counter |> Re__CURSOR__(3)", label: "Read", kind: completionKindMethod, want: "counter.Read(3)"},
		{name: "dot function empty", expression: "counter.Ins__CURSOR__()", label: "Inspect", kind: completionKindFunction, want: "counter |> Inspect(${1:fallback})"},
		{name: "dot function arguments", expression: "counter.Ins__CURSOR__(3)", label: "Inspect", kind: completionKindFunction, want: "counter |> Inspect(3)"},
		{name: "dot method empty after non BMP", expression: "counter.Re__CURSOR__()", label: "Read", kind: completionKindMethod, want: "counter.Read(${1:delta})"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			prefix := declarations + "fn use(counter: Counter) -> i32 { let text: cstr = \"🙂\"; return "
			source := prefix + test.expression + "; }\n"
			state := NewServerState()
			state.RootDir = root
			items := completionAtSource(t, state, filePath, source)
			item := completionItemByKind(t, items, test.label, test.kind)
			clean, _ := markerPosition(t, source)
			got := applyCompletionTextEdit(t, clean, item.TextEdit)
			if !strings.Contains(got, test.want) {
				t.Fatalf("applied completion = %q, want %q", got, test.want)
			}
		})
	}
}

func TestCompletionCallSuffixBalancesNestedLexicalContent(t *testing.T) {
	text := `Inspect(other(")"), /* ) */ nested(1))`
	kind, end := completionCallSuffix(text, len("Inspect"))
	if kind != completionCallArguments || end != len(text) {
		t.Fatalf("call suffix = %d, %d, want arguments ending at %d", kind, end, len(text))
	}
	kind, end = completionCallSuffix("Inspect( \t )", len("Inspect"))
	if kind != completionCallEmpty || end != len("Inspect( \t )") {
		t.Fatalf("empty call suffix = %d, %d", kind, end)
	}
}

func TestCompletionPipeIncludesApplicableImportedFunctions(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceProjectConfig(t, root, "app")
	mainPath := filepath.Join(root, peeper.SourceDirName, peeper.MainFileName)
	externalPath := filepath.Join(root, peeper.SourceDirName, "external"+peeper.SourceExt)
	writeWorkspaceFile(t, externalPath, "fn Measure(value: &i32, scale: i32) -> i32 { return *value * scale; }\nfn Wrong(value: bool) {}\n")
	source := "import \"app/external\";\nfn main() -> i32 { let value: i32 = 2; return value |> M__CURSOR__; }\n"
	state := NewServerState()
	state.RootDir = root
	items := completionAtSource(t, state, mainPath, source)
	item := completionItemByKind(t, items, "external::Measure", completionKindFunction)
	if item.TextEdit.NewText != "external::Measure(${1:scale})" {
		t.Fatalf("imported function completion = %#v", item)
	}
	if slices.Contains(completionLabels(items), "external::Wrong") {
		t.Fatalf("inapplicable imported function exposed: %v", completionLabels(items))
	}
}

func TestCompletionDoesNotLeakImportedMethodsWithSameReceiverName(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceProjectConfig(t, root, "app")
	mainPath := filepath.Join(root, peeper.SourceDirName, peeper.MainFileName)
	externalPath := filepath.Join(root, peeper.SourceDirName, "external"+peeper.SourceExt)
	writeWorkspaceFile(t, externalPath, `struct Value {}
fn (self: &Value) Foreign() {}
fn (self: &Value) privateForeign() {}
fn Inspect(value: &Value) {}
`)
	source := `import "app/external";
struct Value {}
fn (self: &Value) Local() {}
fn use(value: Value) { value |> __CURSOR__; }
`
	state := NewServerState()
	state.RootDir = root
	items := completionAtSource(t, state, mainPath, source)
	labels := completionLabels(items)
	if !slices.Contains(labels, "Local") || !slices.Contains(labels, "external::Inspect") {
		t.Fatalf("completion labels = %v, want current method and imported function", labels)
	}
	if slices.Contains(labels, "Foreign") || slices.Contains(labels, "privateForeign") {
		t.Fatalf("imported method leaked into completion: %v", labels)
	}
}

func TestCompletionPreservesMethodFunctionNameCollision(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "main"+peeper.SourceExt)
	source := "struct Value {}\n" +
		"fn (self: &Value) Act(amount: i32) {}\n" +
		"fn Act(value: &Value, amount: i32) {}\n" +
		"fn use(value: Value) { value |> Act__CURSOR__; }\n"
	state := NewServerState()
	state.RootDir = root
	items := completionAtSource(t, state, filePath, source)
	completionItemByKind(t, items, "Act", completionKindMethod)
	completionItemByKind(t, items, "Act", completionKindFunction)
}

func TestParseCompletionContextDuringNaturalPipeTyping(t *testing.T) {
	for _, source := range []string{
		"fn use(value: i32) { value |> __CURSOR__; }",
		"fn use(value: i32) { value |> le__CURSOR__; }",
		"fn use(value: i32) { value|>le__CURSOR__(); }",
	} {
		clean, position := markerPosition(t, source)
		parsed := parseCompletionContext(clean, position)
		if parsed.kind != completionOperation || !parsed.pipe || !strings.Contains(parsed.sentinel, completionSentinel+"(") {
			t.Fatalf("pipe context for %q = %#v", source, parsed)
		}
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

func TestCompletionQualifiedEnumListsVariants(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "main"+peeper.SourceExt)
	state := NewServerState()
	state.RootDir = root
	source := `enum Result<T> {
	Ok: { value: T },
	Pending,
}
fn main() {
	let value = Result<i32>::__CURSOR__;
}
`
	items := completionAtSource(t, state, filePath, source)
	if got := completionLabels(items); !slices.Equal(got, []string{"Ok", "Pending"}) {
		t.Fatalf("enum variant completion labels = %v", got)
	}
	for _, item := range items {
		if item.Kind != completionKindConstant {
			t.Fatalf("enum variant completion item = %#v, want constant kind", item)
		}
	}
}

func TestCompletionImportedQualifiedEnumListsVariants(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceProjectConfig(t, root, "app")
	mainPath := filepath.Join(root, peeper.SourceDirName, peeper.MainFileName)
	resultPath := filepath.Join(root, peeper.SourceDirName, "result"+peeper.SourceExt)
	writeWorkspaceFile(t, resultPath, "enum Result<T> { Ok: { value: T }, Pending }\n")
	state := NewServerState()
	state.RootDir = root
	items := completionAtSource(t, state, mainPath, `import "app/result";
fn main() {
	let value = result::Result<i32>::__CURSOR__;
}
`)
	if got := completionLabels(items); !slices.Equal(got, []string{"Ok", "Pending"}) {
		t.Fatalf("imported enum variant completion labels = %v", got)
	}
}

func TestCompletionMatchListsOnlyMissingArms(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "main"+peeper.SourceExt)
	state := NewServerState()
	state.RootDir = root
	items := completionAtSource(t, state, filePath, `enum Result {
	Ok: { value: i32 },
	Error: { message: cstr },
	Pending,
}
fn inspect(value: Result) {
	match value {
		Result::Ok{ value = _ } => {}
		__CURSOR__
	}
}
`)
	if got := completionLabels(items); !slices.Equal(got, []string{"Result::Error", "Result::Pending"}) {
		t.Fatalf("missing match-arm completion labels = %v", got)
	}
}

func TestCompletionRecompilesChangedEnumSchema(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "main"+peeper.SourceExt)
	state := NewServerState()
	state.RootDir = root
	initial := "enum Status { Ready }\nfn main() { let value = Status::__CURSOR__; }\n"
	if got := completionLabels(completionAtSource(t, state, filePath, initial)); !slices.Equal(got, []string{"Ready"}) {
		t.Fatalf("initial enum completion labels = %v", got)
	}
	before := state.LastCtx
	updated := "enum Status { Waiting }\nfn main() { let value = Status::__CURSOR__; }\n"
	if got := completionLabels(completionAtSource(t, state, filePath, updated)); !slices.Equal(got, []string{"Waiting"}) {
		t.Fatalf("updated enum completion labels = %v", got)
	}
	if state.LastCtx == before {
		t.Fatal("enum schema change reused stale compiler snapshot")
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
