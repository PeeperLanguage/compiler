package lsp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"compiler/pkg/peeper"
)

const hoverMarker = "__CURSOR__"

func collectPublishedDiagnostics(t *testing.T, payload []byte) map[string][][]Diagnostic {
	t.Helper()
	reader := bufio.NewReader(bytes.NewReader(payload))
	out := make(map[string][][]Diagnostic)
	for {
		msg, err := readMessage(reader)
		if err != nil {
			return out
		}
		var envelope struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(msg, &envelope); err != nil {
			continue
		}
		if envelope.Method != "textDocument/publishDiagnostics" {
			continue
		}
		var params PublishDiagnosticsParams
		if err := json.Unmarshal(envelope.Params, &params); err != nil {
			t.Fatalf("unmarshal diagnostics params: %v", err)
		}
		out[string(params.URI)] = append(out[string(params.URI)], params.Diagnostics)
	}
}

func markerPosition(t *testing.T, src string) (string, Position) {
	t.Helper()
	index := strings.Index(src, hoverMarker)
	if index < 0 {
		t.Fatalf("missing hover marker %q", hoverMarker)
	}
	clean := strings.Replace(src, hoverMarker, "", 1)
	line := strings.Count(src[:index], "\n")
	lastNewline := strings.LastIndex(src[:index], "\n")
	column := index
	if lastNewline >= 0 {
		column = index - lastNewline - 1
	}
	return clean, Position{Line: line, Character: column}
}

func hoverAtSource(t *testing.T, state *ServerState, filePath, src string) *Hover {
	t.Helper()
	clean, pos := markerPosition(t, src)
	state.Cache[filePath] = clean
	if _, mod := state.recompile(filePath); mod == nil {
		t.Fatalf("expected compiled module for %s", filePath)
	}
	hover, err := state.HandleHover(HoverParams{
		TextDocumentPositionParams: TextDocumentPositionParams{
			TextDocument: TextDocumentIdentifier{URI: DocumentURI(pathToURI(filePath))},
			Position:     pos,
		},
	})
	if err != nil {
		t.Fatalf("HandleHover failed: %v", err)
	}
	return hover
}

func TestJSONRPCFraming(t *testing.T) {
	inputMsg := `{"jsonrpc":"2.0","id":1,"method":"test","params":{}}`
	formatted := "Content-Length: " + strconv.Itoa(len(inputMsg)) + "\r\n\r\n" + inputMsg

	r := bufio.NewReader(strings.NewReader(formatted))
	out, err := readMessage(r)
	if err != nil {
		t.Fatalf("unexpected error reading message: %v", err)
	}
	if string(out) != inputMsg {
		t.Errorf("got %q, want %q", string(out), inputMsg)
	}

	var buf bytes.Buffer
	err = writeMessage(&buf, Request{
		JSONRPC: "2.0",
		Method:  "test",
	})
	if err != nil {
		t.Fatalf("unexpected error writing message: %v", err)
	}
	expectedPrefix := "Content-Length: "
	if !strings.HasPrefix(buf.String(), expectedPrefix) {
		t.Errorf("expected output to start with %q, got %q", expectedPrefix, buf.String())
	}
}

func TestLSPServerLifecycleAndHandlers(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "main"+peeper.SourceExt)
	fileURI := pathToURI(filePath)

	content := `fn main() -> i32 {
	let x = 42;
	let y = x + 1;
	return 0;
}
`

	// 1. Initialize
	state := NewServerState()
	state.RootDir = tmpDir
	state.Cache[filePath] = content

	// Run compilation
	ctx, mod := state.recompile(filePath)
	if mod == nil {
		t.Fatalf("expected compiled module, got nil")
	}
	if ctx.Diagnostics.HasErrors() {
		diags := ctx.Diagnostics.Diagnostics()
		t.Fatalf("compilation failed with diagnostics: %v", diags[0].Message)
	}

	// 2. Test Hover on 'x' in 'let y = x + 1'
	// Position of 'x' in 'let y = x + 1':
	// Line 2 (0-indexed), Character 9 (0-indexed)
	hoverParams := HoverParams{
		TextDocumentPositionParams: TextDocumentPositionParams{
			TextDocument: TextDocumentIdentifier{URI: DocumentURI(fileURI)},
			Position:     Position{Line: 2, Character: 9},
		},
	}
	hover, err := state.HandleHover(hoverParams)
	if err != nil {
		t.Fatalf("HandleHover failed: %v", err)
	}
	if hover == nil {
		t.Fatalf("expected hover result, got nil")
	}
	if !strings.Contains(hover.Contents.Value, "x") || !strings.Contains(hover.Contents.Value, "i32") {
		t.Errorf("unexpected hover contents: %q", hover.Contents.Value)
	}

	// 3. Test Definition on 'x' in 'let y = x + 1'
	defParams := DefinitionParams{
		TextDocumentPositionParams: TextDocumentPositionParams{
			TextDocument: TextDocumentIdentifier{URI: DocumentURI(fileURI)},
			Position:     Position{Line: 2, Character: 9},
		},
	}
	locs, err := state.HandleDefinition(defParams)
	if err != nil {
		t.Fatalf("HandleDefinition failed: %v", err)
	}
	if len(locs) != 1 {
		t.Fatalf("expected 1 definition location, got %d", len(locs))
	}
	// Expected definition on 'let x = 42': Line 1, Char 5
	startLine := locs[0].Range.Start.Line
	if startLine != 1 {
		t.Errorf("expected definition on line 1, got line %d", startLine)
	}

	// 4. Test Rename 'x' to 'new_var'
	renameParams := RenameParams{
		TextDocument: TextDocumentIdentifier{URI: DocumentURI(fileURI)},
		Position:     Position{Line: 2, Character: 9},
		NewName:      "new_var",
	}
	edit, err := state.HandleRename(renameParams)
	if err != nil {
		t.Fatalf("HandleRename failed: %v", err)
	}
	if edit == nil || len(edit.Changes) == 0 {
		t.Fatalf("expected rename edits, got none")
	}

	edits := edit.Changes[DocumentURI(fileURI)]
	if len(edits) != 2 {
		t.Fatalf("expected 2 rename edits, got %d", len(edits))
	}
	// Edits should be: declaration (line 1) and reference (line 2)
	lines := map[int]bool{
		edits[0].Range.Start.Line: true,
		edits[1].Range.Start.Line: true,
	}
	if !lines[1] || !lines[2] {
		t.Errorf("expected rename edits on lines 1 and 2, got lines %v", lines)
	}
	if edits[0].NewText != "new_var" || edits[1].NewText != "new_var" {
		t.Errorf("unexpected rename text: %q and %q", edits[0].NewText, edits[1].NewText)
	}
}

func TestHoverReusesFreshCompiledSnapshot(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "main"+peeper.SourceExt)
	content := "fn main() -> i32 {\n\tlet x = 42;\n\treturn x;\n}\n"

	state := NewServerState()
	state.RootDir = tmpDir
	state.Cache[filePath] = content

	if _, mod := state.recompile(filePath); mod == nil {
		t.Fatalf("expected compiled module, got nil")
	}
	before := state.LastCtx
	if before == nil {
		t.Fatalf("expected cached compiler context")
	}

	hover, err := state.HandleHover(HoverParams{
		TextDocumentPositionParams: TextDocumentPositionParams{
			TextDocument: TextDocumentIdentifier{URI: DocumentURI(pathToURI(filePath))},
			Position:     Position{Line: 2, Character: 9},
		},
	})
	if err != nil {
		t.Fatalf("HandleHover failed: %v", err)
	}
	if hover == nil {
		t.Fatalf("expected hover result, got nil")
	}
	if state.LastCtx != before {
		t.Fatalf("hover replaced fresh compiled snapshot")
	}
}

func TestScheduleDiagnosticRefreshCoalescesRapidChanges(t *testing.T) {
	state := NewServerState()
	filePath := "/tmp/main.peep"

	var mu sync.Mutex
	calls := 0
	done := make(chan struct{}, 2)
	publish := func() {
		mu.Lock()
		calls++
		mu.Unlock()
		done <- struct{}{}
	}

	state.scheduleDiagnosticRefresh(filePath, 20*time.Millisecond, publish)
	state.scheduleDiagnosticRefresh(filePath, 20*time.Millisecond, publish)

	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("timed out waiting for debounced publish")
	}
	time.Sleep(40 * time.Millisecond)

	mu.Lock()
	got := calls
	mu.Unlock()
	if got != 1 {
		t.Fatalf("debounced publish count = %d, want 1", got)
	}
}

func TestHoverShowsExplicitTypeForImportedCallBinding(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceProjectConfig(t, root, "app")
	mainPath := filepath.Join(root, peeper.SourceDirName, peeper.MainFileName)
	externalPath := filepath.Join(root, peeper.SourceDirName, "external"+peeper.SourceExt)
	writeWorkspaceFile(t, externalPath, "fn GetValue() -> i32 { return 69; }\n")
	writeWorkspaceFile(t, mainPath, "import \"app/external\";\nfn main() -> i32 {\n\tlet myval: i32 = external::GetValue();\n\treturn myval;\n}\n")

	state := NewServerState()
	state.RootDir = root
	if _, mod := state.recompile(mainPath); mod == nil {
		t.Fatalf("expected compiled module, got nil")
	}

	hover, err := state.HandleHover(HoverParams{
		TextDocumentPositionParams: TextDocumentPositionParams{
			TextDocument: TextDocumentIdentifier{URI: DocumentURI(pathToURI(mainPath))},
			Position:     Position{Line: 2, Character: 6},
		},
	})
	if err != nil {
		t.Fatalf("HandleHover failed: %v", err)
	}
	if hover == nil {
		t.Fatalf("expected hover result, got nil")
	}
	if !strings.Contains(hover.Contents.Value, "myval") || !strings.Contains(hover.Contents.Value, "i32") {
		t.Fatalf("unexpected hover contents: %q", hover.Contents.Value)
	}
	if strings.Contains(hover.Contents.Value, "<invalid>") {
		t.Fatalf("hover should keep explicit type, got %q", hover.Contents.Value)
	}
}

func TestHoverShowsDocCommentOnDeclarationName(t *testing.T) {
	root := t.TempDir()
	mainPath := filepath.Join(root, "main"+peeper.SourceExt)
	src := "// main docs\nfn __CURSOR__main() -> i32 {\n\treturn 0;\n}\n"

	state := NewServerState()
	state.RootDir = root
	hover := hoverAtSource(t, state, mainPath, src)
	if hover == nil {
		t.Fatalf("expected hover result, got nil")
	}
	if !strings.Contains(hover.Contents.Value, "```\n\n---\n\nmain docs") {
		t.Fatalf("expected doc comment in hover, got %q", hover.Contents.Value)
	}
}

func TestHoverShowsImportQualifier(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceProjectConfig(t, root, "app")
	mainPath := filepath.Join(root, peeper.SourceDirName, peeper.MainFileName)
	externalPath := filepath.Join(root, peeper.SourceDirName, "external"+peeper.SourceExt)
	writeWorkspaceFile(t, externalPath, "fn GetValue() -> i32 { return 69; }\n")
	src := "import \"app/external\";\nfn main() -> i32 {\n\treturn __CURSOR__external::GetValue();\n}\n"

	state := NewServerState()
	state.RootDir = root
	hover := hoverAtSource(t, state, mainPath, src)
	if hover == nil {
		t.Fatalf("expected hover result, got nil")
	}
	if !strings.Contains(hover.Contents.Value, "(import) external -> app/external") {
		t.Fatalf("unexpected hover contents: %q", hover.Contents.Value)
	}
}

func TestHoverShowsQualifiedTypeMemberAsType(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceProjectConfig(t, root, "app")
	mainPath := filepath.Join(root, peeper.SourceDirName, peeper.MainFileName)
	externalPath := filepath.Join(root, peeper.SourceDirName, "external"+peeper.SourceExt)
	writeWorkspaceFile(t, externalPath, "struct MyType {}\n")
	src := "import \"app/external\";\nfn main() -> i32 {\n\tlet value: external::__CURSOR__MyType;\n\treturn 0;\n}\n"

	state := NewServerState()
	state.RootDir = root
	hover := hoverAtSource(t, state, mainPath, src)
	if hover == nil {
		t.Fatalf("expected hover result, got nil")
	}
	if !strings.Contains(hover.Contents.Value, "(type) MyType") {
		t.Fatalf("unexpected hover contents: %q", hover.Contents.Value)
	}
}

func TestHoverShowsSelectorMemberFieldType(t *testing.T) {
	root := t.TempDir()
	mainPath := filepath.Join(root, "main"+peeper.SourceExt)
	src := "struct Point {\n\tx: i32,\n}\n\nfn main() -> i32 {\n\tlet p: Point;\n\treturn p.__CURSOR__x;\n}\n"

	state := NewServerState()
	state.RootDir = root
	hover := hoverAtSource(t, state, mainPath, src)
	if hover == nil {
		t.Fatalf("expected hover result, got nil")
	}
	if !strings.Contains(hover.Contents.Value, "(field) x: i32") {
		t.Fatalf("unexpected hover contents: %q", hover.Contents.Value)
	}
}

func TestHoverShowsSelectorMethodSignature(t *testing.T) {
	root := t.TempDir()
	mainPath := filepath.Join(root, "main"+peeper.SourceExt)
	src := "impl i32 {\n\tfn abs(self: Self) -> Self {\n\t\treturn self;\n\t}\n}\n\nfn main() -> i32 {\n\tlet x: i32 = 1;\n\treturn x.__CURSOR__abs();\n}\n"

	state := NewServerState()
	state.RootDir = root
	hover := hoverAtSource(t, state, mainPath, src)
	if hover == nil {
		t.Fatalf("expected hover result, got nil")
	}
	if !strings.Contains(hover.Contents.Value, "(method) abs: fn(i32) -> i32") {
		t.Fatalf("unexpected hover contents: %q", hover.Contents.Value)
	}
}

func TestHoverShowsImplMethodNameSignature(t *testing.T) {
	root := t.TempDir()
	mainPath := filepath.Join(root, "main"+peeper.SourceExt)
	src := "struct Point {\n\tx: i32,\n\ty: i32,\n}\n\nimpl Point {\n\tfn __CURSOR__sum(self: Self) -> i32 {\n\t\treturn self.x + self.y;\n\t}\n}\n"

	state := NewServerState()
	state.RootDir = root
	hover := hoverAtSource(t, state, mainPath, src)
	if hover == nil {
		t.Fatalf("expected hover result, got nil")
	}
	if !strings.Contains(hover.Contents.Value, "(method) sum: fn(Point) -> i32") {
		t.Fatalf("unexpected hover contents: %q", hover.Contents.Value)
	}
}

func TestHoverShowsSelfTypeInsideImplMethodSignature(t *testing.T) {
	root := t.TempDir()
	mainPath := filepath.Join(root, "main"+peeper.SourceExt)
	src := "struct Point {\n\tx: i32,\n\ty: i32,\n}\n\nimpl Point {\n\tfn sum(self: __CURSOR__Self) -> i32 {\n\t\treturn self.x + self.y;\n\t}\n}\n"

	state := NewServerState()
	state.RootDir = root
	hover := hoverAtSource(t, state, mainPath, src)
	if hover == nil {
		t.Fatalf("expected hover result, got nil")
	}
	if strings.Contains(hover.Contents.Value, "<invalid>") {
		t.Fatalf("expected concrete Self hover, got %q", hover.Contents.Value)
	}
	if !strings.Contains(hover.Contents.Value, "(type) Point") {
		t.Fatalf("unexpected hover contents: %q", hover.Contents.Value)
	}
}

func TestHoverShowsInterfaceMethodSignature(t *testing.T) {
	root := t.TempDir()
	mainPath := filepath.Join(root, "main"+peeper.SourceExt)
	src := "interface SummerConsumer {\n\t__CURSOR__consume(Self, val: Summer) -> i32,\n}\n"

	state := NewServerState()
	state.RootDir = root
	hover := hoverAtSource(t, state, mainPath, src)
	if hover == nil {
		t.Fatalf("expected hover result, got nil")
	}
	if !strings.Contains(hover.Contents.Value, "(method) consume: fn(Self, Summer) -> i32") {
		t.Fatalf("unexpected hover contents: %q", hover.Contents.Value)
	}
	if strings.Contains(hover.Contents.Value, "val:") {
		t.Fatalf("interface method hover should omit param names, got %q", hover.Contents.Value)
	}
}

func TestHoverShowsInterfaceTypeWithMultilineMethods(t *testing.T) {
	root := t.TempDir()
	mainPath := filepath.Join(root, "main"+peeper.SourceExt)
	src := "__CURSOR__interface SummerConsumer {\n\tconsume(Self, val: Summer) -> i32,\n}\n"

	state := NewServerState()
	state.RootDir = root
	hover := hoverAtSource(t, state, mainPath, src)
	if hover == nil {
		t.Fatalf("expected hover result, got nil")
	}
	if !strings.Contains(hover.Contents.Value, "interface{\n  consume(Self, Summer) -> i32\n}") {
		t.Fatalf("unexpected hover contents: %q", hover.Contents.Value)
	}
	if strings.Contains(hover.Contents.Value, "val:") {
		t.Fatalf("interface type hover should omit param names, got %q", hover.Contents.Value)
	}
}

func TestHoverShowsTypeMethodsOnNamedType(t *testing.T) {
	root := t.TempDir()
	mainPath := filepath.Join(root, "main"+peeper.SourceExt)
	src := "struct Point {\n\tx: i32,\n\ty: i32,\n}\n\nimpl Point {\n\tfn sum(self: Self) -> i32 {\n\t\treturn self.x + self.y;\n\t}\n}\n\nfn main() -> i32 {\n\tlet p: __CURSOR__Point;\n\treturn 0;\n}\n"

	state := NewServerState()
	state.RootDir = root
	hover := hoverAtSource(t, state, mainPath, src)
	if hover == nil {
		t.Fatalf("expected hover result, got nil")
	}
	if !strings.Contains(hover.Contents.Value, "struct{\n  x: i32\n  y: i32\n}") {
		t.Fatalf("unexpected hover contents: %q", hover.Contents.Value)
	}
	if !strings.Contains(hover.Contents.Value, "// methods\n  sum: fn(Point) -> i32") {
		t.Fatalf("expected method list in type hover, got %q", hover.Contents.Value)
	}
}

func TestHoverShowsBinaryExpressionType(t *testing.T) {
	root := t.TempDir()
	mainPath := filepath.Join(root, "main"+peeper.SourceExt)
	src := "fn main() -> i32 {\n\tlet x: i32 = 1;\n\treturn x __CURSOR__+ 1;\n}\n"

	state := NewServerState()
	state.RootDir = root
	hover := hoverAtSource(t, state, mainPath, src)
	if hover == nil {
		t.Fatalf("expected hover result, got nil")
	}
	if !strings.Contains(hover.Contents.Value, "(expr): i32") {
		t.Fatalf("unexpected hover contents: %q", hover.Contents.Value)
	}
}

func TestHoverShowsDeclarationNodeSignature(t *testing.T) {
	root := t.TempDir()
	mainPath := filepath.Join(root, "main"+peeper.SourceExt)
	src := "__CURSOR__fn main() -> i32 {\n\treturn 0;\n}\n"

	state := NewServerState()
	state.RootDir = root
	hover := hoverAtSource(t, state, mainPath, src)
	if hover == nil {
		t.Fatalf("expected hover result, got nil")
	}
	if !strings.Contains(hover.Contents.Value, "(func) main: fn() -> i32") {
		t.Fatalf("unexpected hover contents: %q", hover.Contents.Value)
	}
}

func TestHoverShowsInvalidExpressionType(t *testing.T) {
	root := t.TempDir()
	mainPath := filepath.Join(root, "main"+peeper.SourceExt)
	src := "fn main() -> i32 {\n\treturn 1 __CURSOR__+ true;\n}\n"

	state := NewServerState()
	state.RootDir = root
	hover := hoverAtSource(t, state, mainPath, src)
	if hover == nil {
		t.Fatalf("expected hover result, got nil")
	}
	if !strings.Contains(hover.Contents.Value, "<invalid>") {
		t.Fatalf("expected invalid hover, got %q", hover.Contents.Value)
	}
}

func TestHoverReturnsNilOnBlankLine(t *testing.T) {
	root := t.TempDir()
	mainPath := filepath.Join(root, "main"+peeper.SourceExt)
	src := "fn main() -> i32 {\n__CURSOR__\treturn 0;\n}\n"

	state := NewServerState()
	state.RootDir = root
	hover := hoverAtSource(t, state, mainPath, src)
	if hover != nil {
		t.Fatalf("expected nil hover on blank line, got %q", hover.Contents.Value)
	}
}

func TestLSPInitializedPublishesDiagnosticsForUnopenedWorkspaceFiles(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceProjectConfig(t, root, "app")
	mainPath := filepath.Join(root, peeper.SourceDirName, peeper.MainFileName)
	utilPath := filepath.Join(root, peeper.SourceDirName, "util"+peeper.SourceExt)
	writeWorkspaceFile(t, mainPath, "import \"app/util\";\nfn main() -> i32 { return util::Helper(); }\n")
	writeWorkspaceFile(t, utilPath, "fn Helper() -> i32 { return missing; }\n")

	rootURI := DocumentURI(pathToURI(root))
	initParams, err := json.Marshal(InitializeParams{RootURI: &rootURI})
	if err != nil {
		t.Fatalf("marshal initialize params: %v", err)
	}
	initID := json.RawMessage([]byte("1"))

	var input bytes.Buffer
	if err := writeMessage(&input, Request{
		JSONRPC: "2.0",
		ID:      &initID,
		Method:  "initialize",
		Params:  initParams,
	}); err != nil {
		t.Fatalf("write initialize: %v", err)
	}
	if err := writeMessage(&input, Request{
		JSONRPC: "2.0",
		Method:  "initialized",
	}); err != nil {
		t.Fatalf("write initialized: %v", err)
	}

	var output bytes.Buffer
	if err := Run(bytes.NewReader(input.Bytes()), &output); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	published := collectPublishedDiagnostics(t, output.Bytes())
	utilPublished := published[pathToURI(utilPath)]
	if len(utilPublished) == 0 || len(utilPublished[0]) == 0 {
		t.Fatalf("expected diagnostics publish for unopened workspace file %s", utilPath)
	}
}

func TestLSPDidChangeClearsDiagnosticsForFixedComponentFile(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceProjectConfig(t, root, "app")
	mainPath := filepath.Join(root, peeper.SourceDirName, peeper.MainFileName)
	utilPath := filepath.Join(root, peeper.SourceDirName, "util"+peeper.SourceExt)
	mainSrc := "import \"app/util\";\nfn main() -> i32 { return util::Helper(); }\n"
	writeWorkspaceFile(t, mainPath, mainSrc)
	writeWorkspaceFile(t, utilPath, "fn Helper() -> i32 { return missing; }\n")

	rootURI := DocumentURI(pathToURI(root))
	initParams, err := json.Marshal(InitializeParams{RootURI: &rootURI})
	if err != nil {
		t.Fatalf("marshal initialize params: %v", err)
	}
	openParams, err := json.Marshal(DidOpenTextDocumentParams{
		TextDocument: TextDocumentItem{
			URI:  DocumentURI(pathToURI(mainPath)),
			Text: mainSrc,
		},
	})
	if err != nil {
		t.Fatalf("marshal open params: %v", err)
	}
	changeParams, err := json.Marshal(DidChangeTextDocumentParams{
		TextDocument: VersionedTextDocumentIdentifier{URI: DocumentURI(pathToURI(utilPath)), Version: 2},
		ContentChanges: []TextDocumentContentChangeEvent{
			{Text: "fn Helper() -> i32 { return 1; }\n"},
		},
	})
	if err != nil {
		t.Fatalf("marshal change params: %v", err)
	}
	initID := json.RawMessage([]byte("1"))

	var input bytes.Buffer
	for _, req := range []Request{
		{JSONRPC: "2.0", ID: &initID, Method: "initialize", Params: initParams},
		{JSONRPC: "2.0", Method: "didOpen", Params: openParams},
		{JSONRPC: "2.0", Method: "didChange", Params: changeParams},
	} {
		method := req.Method
		if strings.HasPrefix(method, "did") {
			method = "textDocument/" + strings.ToLower(method[:1]) + method[1:]
			req.Method = method
		}
		if err := writeMessage(&input, req); err != nil {
			t.Fatalf("write %s: %v", req.Method, err)
		}
	}

	var output bytes.Buffer
	if err := Run(bytes.NewReader(input.Bytes()), &output); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	published := collectPublishedDiagnostics(t, output.Bytes())
	utilPublished := published[pathToURI(utilPath)]
	if len(utilPublished) < 2 {
		t.Fatalf("expected util diagnostics before and after fix, got %d publishes", len(utilPublished))
	}
	if len(utilPublished[0]) == 0 {
		t.Fatalf("expected first util publish to carry diagnostics")
	}
	last := utilPublished[len(utilPublished)-1]
	if len(last) != 0 {
		t.Fatalf("expected final util publish to clear diagnostics, got %d entries", len(last))
	}
}
