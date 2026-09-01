package lsp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"compiler/internal/diagnostics"
	"compiler/internal/driver"
	"compiler/internal/prelude"
	"compiler/internal/project"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
	"compiler/pkg/manifest"
	"compiler/pkg/peeper"
)

const hoverMarker = "__CURSOR__"

func collectPublishedDiagnostics(t *testing.T, payload []byte) map[string][][]Diagnostic {
	notifications := collectDiagnosticNotifications(t, payload)
	out := make(map[string][][]Diagnostic, len(notifications))
	for uri, params := range notifications {
		for _, param := range params {
			out[uri] = append(out[uri], param.Diagnostics)
		}
	}
	return out
}

func collectDiagnosticNotifications(t *testing.T, payload []byte) map[string][]PublishDiagnosticsParams {
	t.Helper()
	reader := bufio.NewReader(bytes.NewReader(payload))
	out := make(map[string][]PublishDiagnosticsParams)
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
		out[string(params.URI)] = append(out[string(params.URI)], params)
	}
}

func runTimedLSPChanges(t *testing.T, root, filePath, initial string, changes []string) []PublishDiagnosticsParams {
	t.Helper()
	inputReader, inputWriter := io.Pipe()
	var output bytes.Buffer
	runDone := make(chan error, 1)
	go func() {
		runDone <- Run(inputReader, &output)
	}()

	send := func(method string, params any) {
		payload, err := json.Marshal(params)
		if err != nil {
			t.Fatalf("marshal %s: %v", method, err)
		}
		if err := writeMessage(inputWriter, Request{JSONRPC: "2.0", Method: method, Params: payload}); err != nil {
			t.Fatalf("write %s: %v", method, err)
		}
	}
	rootURI := DocumentURI(pathToURI(root))
	send("initialize", InitializeParams{RootURI: &rootURI})
	send("initialized", nil)
	send("textDocument/didOpen", DidOpenTextDocumentParams{TextDocument: TextDocumentItem{
		URI:     DocumentURI(pathToURI(filePath)),
		Version: 1,
		Text:    initial,
	}})
	for index, text := range changes {
		send("textDocument/didChange", DidChangeTextDocumentParams{
			TextDocument: VersionedTextDocumentIdentifier{
				URI:     DocumentURI(pathToURI(filePath)),
				Version: index + 2,
			},
			ContentChanges: []TextDocumentContentChangeEvent{{Text: text}},
		})
		time.Sleep(diagnosticsDebounceDelay + 25*time.Millisecond)
	}
	if err := inputWriter.Close(); err != nil {
		t.Fatalf("close LSP input: %v", err)
	}
	if err := <-runDone; err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	return collectDiagnosticNotifications(t, output.Bytes())[pathToURI(filePath)]
}

func diagnosticForVersion(published []PublishDiagnosticsParams, version int) (PublishDiagnosticsParams, bool) {
	for _, params := range published {
		if params.Version != nil && *params.Version == version {
			return params, true
		}
	}
	return PublishDiagnosticsParams{}, false
}

func publishCurrentDiagnostics(t *testing.T, state *ServerState, filePath string) PublishDiagnosticsParams {
	t.Helper()
	var output bytes.Buffer
	if err := publishDiagnosticSnapshot(newProtocolWriter(&output), state, state.diagnosticSnapshot(filePath, nil)); err != nil {
		t.Fatalf("publish diagnostics: %v", err)
	}
	message, err := readMessage(bufio.NewReader(&output))
	if err != nil {
		t.Fatalf("read published diagnostics: %v", err)
	}
	var notification struct {
		Params PublishDiagnosticsParams `json:"params"`
	}
	if err := json.Unmarshal(message, &notification); err != nil {
		t.Fatalf("unmarshal published diagnostics: %v", err)
	}
	return notification.Params
}

func hasErrorDiagnostic(diagnostics []Diagnostic) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Severity == 1 {
			return true
		}
	}
	return false
}

type blockingDiagnosticWriter struct {
	entered chan struct{}
	release chan struct{}
}

func (w *blockingDiagnosticWriter) Write(data []byte) (int, error) {
	select {
	case w.entered <- struct{}{}:
	default:
	}
	<-w.release
	return len(data), nil
}

func markerPosition(t *testing.T, src string) (string, Position) {
	t.Helper()
	index := strings.Index(src, hoverMarker)
	if index < 0 {
		t.Fatalf("missing hover marker %q", hoverMarker)
	}
	clean := strings.Replace(src, hoverMarker, "", 1)
	return clean, positionAtOffset(clean, index)
}

func hoverAtSource(t *testing.T, state *ServerState, filePath, src string) *Hover {
	t.Helper()
	clean, pos := markerPosition(t, src)
	state.applyDocumentSnapshot(filePath, &clean, nil)
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

func renameAtSource(t *testing.T, state *ServerState, filePath, src, newName string) *WorkspaceEdit {
	t.Helper()
	clean, pos := markerPosition(t, src)
	state.applyDocumentSnapshot(filePath, &clean, nil)
	if _, mod := state.recompile(filePath); mod == nil {
		t.Fatalf("expected compiled module for %s", filePath)
	}
	edit, err := state.HandleRename(RenameParams{
		TextDocument: TextDocumentIdentifier{URI: DocumentURI(pathToURI(filePath))},
		Position:     pos,
		NewName:      newName,
	})
	if err != nil {
		t.Fatalf("HandleRename failed: %v", err)
	}
	return edit
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
	state.applyDocumentSnapshot(filePath, &content, nil)

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

func TestParseBundledPreludeFileKeepsStdlibIdentity(t *testing.T) {
	root := t.TempDir()
	libraryBase := filepath.Join(root, "libs")
	globalPath := filepath.Join(libraryBase, "core", peeper.SourceDirName, "global"+peeper.SourceExt)
	writeWorkspaceProjectConfig(t, root, "app")
	writeWorkspaceFile(t, globalPath, "const stdout: i32 = 1;\n")

	ctx := compiler.NewCompilerContext(project.Config{
		RootDir:        root,
		ProjectName:    "app",
		Extension:      peeper.SourceExt,
		LibraryBaseDir: libraryBase,
	}, nil)
	content := "const stdout: i32 = 1;\n"
	mod := compiler.CompileFile(ctx, globalPath, &content)
	if mod == nil {
		t.Fatalf("expected compiled bundled library module")
	}
	if mod.ID != prelude.ModuleID() {
		t.Fatalf("module ID = %#v, want canonical prelude ID %#v", mod.ID, prelude.ModuleID())
	}
}

func TestHandleRenameMatchesImportedQualifiedValue(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceProjectConfig(t, root, "app")
	mainPath := filepath.Join(root, peeper.SourceDirName, peeper.MainFileName)
	externalPath := filepath.Join(root, peeper.SourceDirName, "external"+peeper.SourceExt)
	writeWorkspaceFile(t, externalPath, "fn GetValue() -> i32 { return 69; }\n")
	src := "import \"app/external\";\nfn main() -> i32 {\n\treturn external::__CURSOR__GetValue();\n}\n"

	state := NewServerState()
	state.RootDir = root
	edit := renameAtSource(t, state, mainPath, src, "ReadValue")
	if edit == nil || len(edit.Changes) != 2 {
		t.Fatalf("expected cross-workspace rename edits, got %#v", edit)
	}

	mainEdits := edit.Changes[DocumentURI(pathToURI(mainPath))]
	if len(mainEdits) != 1 || mainEdits[0].Range.Start.Line != 2 {
		t.Fatalf("main edits = %#v, want one edit on line 2", mainEdits)
	}
	externalEdits := edit.Changes[DocumentURI(pathToURI(externalPath))]
	if len(externalEdits) != 1 || externalEdits[0].Range.Start.Line != 0 {
		t.Fatalf("external edits = %#v, want one edit on line 0", externalEdits)
	}
}

func TestHandleRenameMatchesQualifiedTypeMember(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceProjectConfig(t, root, "app")
	mainPath := filepath.Join(root, peeper.SourceDirName, peeper.MainFileName)
	externalPath := filepath.Join(root, peeper.SourceDirName, "external"+peeper.SourceExt)
	writeWorkspaceFile(t, externalPath, "struct MyType {}\n")
	src := "import \"app/external\";\nfn main() -> i32 {\n\tlet value: external::__CURSOR__MyType;\n\treturn 0;\n}\n"

	state := NewServerState()
	state.RootDir = root
	edit := renameAtSource(t, state, mainPath, src, "YourType")
	if edit == nil || len(edit.Changes) != 2 {
		t.Fatalf("expected cross-workspace type rename edits, got %#v", edit)
	}

	mainEdits := edit.Changes[DocumentURI(pathToURI(mainPath))]
	if len(mainEdits) != 1 || mainEdits[0].Range.Start.Line != 2 {
		t.Fatalf("main edits = %#v, want one edit on line 2", mainEdits)
	}
	externalEdits := edit.Changes[DocumentURI(pathToURI(externalPath))]
	if len(externalEdits) != 1 || externalEdits[0].Range.Start.Line != 0 {
		t.Fatalf("external edits = %#v, want one edit on line 0", externalEdits)
	}
}

func TestVariantDefinitionAndRenameUseChildSymbol(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "main"+peeper.SourceExt)
	src := `enum Result {
	Ok,
	Pending,
}
fn inspect(value: Result) {
	let made = Result::__CURSOR__Ok;
	if value is Result::Ok {}
	match value {
		Result::Ok => {}
		Result::Pending => {}
	}
}
`
	clean, position := markerPosition(t, src)
	state := NewServerState()
	state.RootDir = root
	state.applyDocumentSnapshot(filePath, &clean, nil)
	if _, module := state.recompile(filePath); module == nil {
		t.Fatal("expected compiled enum module")
	}
	locations, err := state.HandleDefinition(DefinitionParams{TextDocumentPositionParams: TextDocumentPositionParams{
		TextDocument: TextDocumentIdentifier{URI: DocumentURI(pathToURI(filePath))},
		Position:     position,
	}})
	if err != nil {
		t.Fatalf("HandleDefinition failed: %v", err)
	}
	if len(locations) != 1 || locations[0].Range.Start.Line != 1 {
		t.Fatalf("variant definition locations = %#v, want declaration line 1", locations)
	}
	edit, err := state.HandleRename(RenameParams{
		TextDocument: TextDocumentIdentifier{URI: DocumentURI(pathToURI(filePath))},
		Position:     position,
		NewName:      "Success",
	})
	if err != nil {
		t.Fatalf("HandleRename failed: %v", err)
	}
	edits := edit.Changes[DocumentURI(pathToURI(filePath))]
	if len(edits) != 4 {
		t.Fatalf("variant rename edits = %#v, want declaration plus three uses", edits)
	}
}

func TestImportedAliasVariantDefinitionAndRenameUseCanonicalSymbol(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceProjectConfig(t, root, "app")
	mainPath := filepath.Join(root, peeper.SourceDirName, peeper.MainFileName)
	resultPath := filepath.Join(root, peeper.SourceDirName, "result"+peeper.SourceExt)
	writeWorkspaceFile(t, resultPath, "enum Result { Ok, Pending }\ntype Alias = Result;\n")
	source := `import "app/result";
fn inspect(value: result::Alias) {
	let made = result::Alias::__CURSOR__Ok;
	if value is result::Alias::Ok {}
}
`
	clean, position := markerPosition(t, source)
	state := NewServerState()
	state.RootDir = root
	state.applyDocumentSnapshot(mainPath, &clean, nil)
	if _, module := state.recompile(mainPath); module == nil {
		t.Fatal("expected compiled enum alias module")
	}
	locations, err := state.HandleDefinition(DefinitionParams{TextDocumentPositionParams: TextDocumentPositionParams{
		TextDocument: TextDocumentIdentifier{URI: DocumentURI(pathToURI(mainPath))},
		Position:     position,
	}})
	if err != nil || len(locations) != 1 || locations[0].URI != DocumentURI(pathToURI(resultPath)) || locations[0].Range.Start.Line != 0 {
		t.Fatalf("alias variant definition = %#v, err = %v", locations, err)
	}
	edit, err := state.HandleRename(RenameParams{
		TextDocument: TextDocumentIdentifier{URI: DocumentURI(pathToURI(mainPath))},
		Position:     position,
		NewName:      "Success",
	})
	if err != nil {
		t.Fatalf("HandleRename failed: %v", err)
	}
	if got := len(edit.Changes[DocumentURI(pathToURI(resultPath))]); got != 1 {
		t.Fatalf("canonical declaration edits = %#v, want one", edit.Changes)
	}
	if got := len(edit.Changes[DocumentURI(pathToURI(mainPath))]); got != 2 {
		t.Fatalf("alias-qualified use edits = %#v, want two", edit.Changes)
	}
}

func TestHoverShowsExactCaseFieldType(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "main"+peeper.SourceExt)
	src := `enum Choice {
	Number: { value: i32 },
	Text: { value: cstr },
}
fn read(choice: Choice) -> cstr {
	if choice is Choice::Text {
		return choice.__CURSOR__value;
	}
	return c"";
}
`
	state := NewServerState()
	state.RootDir = root
	hover := hoverAtSource(t, state, filePath, src)
	if hover == nil || !strings.Contains(hover.Contents.Value, "(expr): cstr") {
		t.Fatalf("exact-case field hover = %#v, want cstr", hover)
	}
}

func TestHandleRenameMatchesSelectorField(t *testing.T) {
	root := t.TempDir()
	mainPath := filepath.Join(root, "main"+peeper.SourceExt)
	src := "struct Point {\n\tx: i32,\n}\n\nfn main() -> i32 {\n\tlet p: Point;\n\treturn p.__CURSOR__x;\n}\n"

	state := NewServerState()
	state.RootDir = root
	edit := renameAtSource(t, state, mainPath, src, "coordX")
	if edit == nil || len(edit.Changes) != 1 {
		t.Fatalf("expected selector field rename edits, got %#v", edit)
	}

	edits := edit.Changes[DocumentURI(pathToURI(mainPath))]
	if len(edits) != 2 {
		t.Fatalf("expected 2 field rename edits, got %#v", edits)
	}
	lines := map[int]bool{}
	for _, edit := range edits {
		lines[edit.Range.Start.Line] = true
	}
	if !lines[1] || !lines[6] || len(lines) != 2 {
		t.Fatalf("field rename touched unexpected lines: %v", lines)
	}
}

func TestHandleRenameMatchesSelectorMethod(t *testing.T) {
	root := t.TempDir()
	mainPath := filepath.Join(root, "main"+peeper.SourceExt)
	src := "struct Point {\n\tx: i32,\n\ty: i32,\n}\n\nfn (self: Point) sum() -> i32 {\n\t\treturn self.x + self.y;\n}\n\nfn main() -> i32 {\n\tlet p: Point;\n\treturn p.__CURSOR__sum();\n}\n"

	state := NewServerState()
	state.RootDir = root
	edit := renameAtSource(t, state, mainPath, src, "total")
	if edit == nil || len(edit.Changes) != 1 {
		t.Fatalf("expected selector method rename edits, got %#v", edit)
	}

	edits := edit.Changes[DocumentURI(pathToURI(mainPath))]
	if len(edits) != 2 {
		t.Fatalf("expected 2 method rename edits, got %#v", edits)
	}
	lines := map[int]bool{}
	for _, edit := range edits {
		lines[edit.Range.Start.Line] = true
	}
	if !lines[5] || !lines[11] || len(lines) != 2 {
		t.Fatalf("method rename touched unexpected lines: %v", lines)
	}
}

func TestHandleRenameMatchesReferenceReturnSource(t *testing.T) {
	root := t.TempDir()
	mainPath := filepath.Join(root, "main"+peeper.SourceExt)
	src := "fn Identity(value: &i32) -> &i32 from __CURSOR__value {\n\treturn value;\n}\n"

	state := NewServerState()
	state.RootDir = root
	edit := renameAtSource(t, state, mainPath, src, "source")
	if edit == nil || len(edit.Changes) != 1 {
		t.Fatalf("expected reference-return source rename edits, got %#v", edit)
	}

	edits := edit.Changes[DocumentURI(pathToURI(mainPath))]
	if len(edits) != 3 {
		t.Fatalf("expected parameter, contract, and return rename edits, got %#v", edits)
	}
}

func TestHandleRenamePreservesReceiverReturnSource(t *testing.T) {
	root := t.TempDir()
	mainPath := filepath.Join(root, "main"+peeper.SourceExt)
	src := `struct Box { value: i32 }

fn (self: &Box) identity() -> &Box from self {
	return __CURSOR__self;
}
`

	state := NewServerState()
	state.RootDir = root
	edit := renameAtSource(t, state, mainPath, src, "receiver")
	if edit == nil || len(edit.Changes) != 1 {
		t.Fatalf("expected receiver rename edits, got %#v", edit)
	}

	edits := edit.Changes[DocumentURI(pathToURI(mainPath))]
	if len(edits) != 2 {
		t.Fatalf("expected receiver declaration and body edits, got %#v", edits)
	}
	for _, edit := range edits {
		if edit.Range.Start.Line == 2 && edit.Range.Start.Character > 35 {
			t.Fatalf("receiver rename touched fixed return source: %#v", edit)
		}
	}
}

func TestHandleRenameIgnoresReceiverReturnSourceToken(t *testing.T) {
	root := t.TempDir()
	mainPath := filepath.Join(root, "main"+peeper.SourceExt)
	src := `struct Box { value: i32 }

fn (self: &Box) identity() -> &Box from __CURSOR__self {
	return self;
}
`

	state := NewServerState()
	state.RootDir = root
	edit := renameAtSource(t, state, mainPath, src, "receiver")
	if edit == nil || len(edit.Changes) != 0 {
		t.Fatalf("expected no edits for fixed receiver source, got %#v", edit)
	}
}

func TestHoverReusesFreshCompiledSnapshot(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "main"+peeper.SourceExt)
	content := "fn main() -> i32 {\n\tlet x = 42;\n\treturn x;\n}\n"

	state := NewServerState()
	state.RootDir = tmpDir
	state.applyDocumentSnapshot(filePath, &content, nil)

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
			Position:     Position{Line: 2, Character: 8},
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

func TestHoverRecompilesDirtySnapshot(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "main"+peeper.SourceExt)
	initial := "fn main() -> i32 {\n\tlet x = 42;\n\treturn x;\n}\n"
	updated := "fn main() -> i32 {\n\tlet renamed: i32 = 42;\n\treturn __CURSOR__renamed;\n}\n"

	state := NewServerState()
	state.RootDir = tmpDir
	state.applyDocumentSnapshot(filePath, &initial, nil)

	if _, mod := state.recompile(filePath); mod == nil {
		t.Fatalf("expected compiled module, got nil")
	}
	before := state.LastCtx
	if before == nil {
		t.Fatalf("expected cached compiler context")
	}

	clean, pos := markerPosition(t, updated)
	state.applyDocumentSnapshot(filePath, &clean, nil)
	hover, err := state.HandleHover(HoverParams{
		TextDocumentPositionParams: TextDocumentPositionParams{
			TextDocument: TextDocumentIdentifier{URI: DocumentURI(pathToURI(filePath))},
			Position:     pos,
		},
	})
	if err != nil {
		t.Fatalf("HandleHover failed: %v", err)
	}
	if hover == nil {
		t.Fatalf("expected hover result, got nil")
	}
	if !strings.Contains(hover.Contents.Value, "renamed") {
		t.Fatalf("expected hover for renamed binding, got %q", hover.Contents.Value)
	}
	if state.LastCtx == before {
		t.Fatalf("hover should recompile when buffer content changed")
	}
}

func TestHoverRecompilesWhenLastSnapshotOnlyHasOverlayStub(t *testing.T) {
	root := t.TempDir()
	firstPath := filepath.Join(root, "first"+peeper.SourceExt)
	secondPath := filepath.Join(root, "second"+peeper.SourceExt)
	firstSrc := "fn main() -> i32 {\n\tlet value: i32 = 1;\n\treturn __CURSOR__value;\n}\n"
	secondSrc := "fn main() -> i32 {\n\treturn 0;\n}\n"

	state := NewServerState()
	state.RootDir = root

	cleanFirst, pos := markerPosition(t, firstSrc)
	state.applyDocumentSnapshot(firstPath, &cleanFirst, nil)
	if _, mod := state.recompile(firstPath); mod == nil {
		t.Fatalf("expected compiled module for %s", firstPath)
	}

	state.applyDocumentSnapshot(secondPath, &secondSrc, nil)
	if _, mod := state.recompile(secondPath); mod == nil {
		t.Fatalf("expected compiled module for %s", secondPath)
	}

	// Recompiling second file leaves first file as an overlay stub in LastCtx.
	if mod, ok := state.LastCtx.ModuleByFile(firstPath); !ok || mod == nil || mod.AST != nil {
		t.Fatalf("expected last snapshot to hold first file as unparsed overlay stub")
	}

	hover, err := state.HandleHover(HoverParams{
		TextDocumentPositionParams: TextDocumentPositionParams{
			TextDocument: TextDocumentIdentifier{URI: DocumentURI(pathToURI(firstPath))},
			Position:     pos,
		},
	})
	if err != nil {
		t.Fatalf("HandleHover failed: %v", err)
	}
	if hover == nil {
		t.Fatalf("expected hover result, got nil")
	}
	if !strings.Contains(hover.Contents.Value, "value") {
		t.Fatalf("unexpected hover contents: %q", hover.Contents.Value)
	}
}

func TestScheduleDiagnosticRefreshCoalescesRapidChanges(t *testing.T) {
	state := NewServerState()
	filePath := "/tmp/main.peep"

	var mu sync.Mutex
	calls := 0
	done := make(chan struct{}, 2)
	publish := func() error {
		mu.Lock()
		calls++
		mu.Unlock()
		done <- struct{}{}
		return nil
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

func TestScheduledDiagnosticFailureIsReturned(t *testing.T) {
	state := NewServerState()
	want := errors.New("diagnostic write failed")
	state.scheduleDiagnosticRefresh("/tmp/main.peep", 0, func() error { return want })
	if err := state.waitForScheduledDiagnostics(); !errors.Is(err, want) {
		t.Fatalf("scheduled diagnostic error = %v, want %v", err, want)
	}
}

func TestRunReturnsSynchronousDiagnosticWriteFailure(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "main"+peeper.SourceExt)
	openParams, err := json.Marshal(DidOpenTextDocumentParams{TextDocument: TextDocumentItem{
		URI:  DocumentURI(pathToURI(filePath)),
		Text: "fn main() {}\n",
	}})
	if err != nil {
		t.Fatalf("marshal open params: %v", err)
	}
	var input bytes.Buffer
	if err := writeMessage(&input, Request{JSONRPC: "2.0", Method: "textDocument/didOpen", Params: openParams}); err != nil {
		t.Fatalf("write open request: %v", err)
	}
	want := errors.New("diagnostic header failed")
	output := &failingProtocolOutput{failAt: 1, err: want}
	if err := Run(io.NopCloser(&input), output); !errors.Is(err, want) {
		t.Fatalf("Run error = %v, want %v", err, want)
	}
}

func TestRunReturnsDebouncedDiagnosticWriteFailureOnProtocolEnd(t *testing.T) {
	tests := []struct {
		name string
		exit bool
	}{
		{name: "EOF"},
		{name: "exit", exit: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			filePath := filepath.Join(root, "main"+peeper.SourceExt)
			openParams, err := json.Marshal(DidOpenTextDocumentParams{TextDocument: TextDocumentItem{
				URI:  DocumentURI(pathToURI(filePath)),
				Text: "fn main() {}\n",
			}})
			if err != nil {
				t.Fatalf("marshal open params: %v", err)
			}
			changeParams, err := json.Marshal(DidChangeTextDocumentParams{
				TextDocument:   VersionedTextDocumentIdentifier{URI: DocumentURI(pathToURI(filePath)), Version: 2},
				ContentChanges: []TextDocumentContentChangeEvent{{Text: "fn main() -> i32 { return 0; }\n"}},
			})
			if err != nil {
				t.Fatalf("marshal change params: %v", err)
			}
			var input bytes.Buffer
			for _, request := range []Request{
				{JSONRPC: "2.0", Method: "textDocument/didOpen", Params: openParams},
				{JSONRPC: "2.0", Method: "textDocument/didChange", Params: changeParams},
			} {
				if err := writeMessage(&input, request); err != nil {
					t.Fatalf("write %s request: %v", request.Method, err)
				}
			}
			if tt.exit {
				if err := writeMessage(&input, Request{JSONRPC: "2.0", Method: "exit"}); err != nil {
					t.Fatalf("write exit request: %v", err)
				}
			}
			want := errors.New("debounced diagnostic header failed")
			output := &failingProtocolOutput{failAt: 3, err: want}
			if err := Run(io.NopCloser(&input), output); !errors.Is(err, want) {
				t.Fatalf("Run error = %v, want %v", err, want)
			}
		})
	}
}

func TestRunReturnsDebouncedDiagnosticWriteFailureWithInputOpen(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "main"+peeper.SourceExt)
	changeParams, err := json.Marshal(DidChangeTextDocumentParams{
		TextDocument: VersionedTextDocumentIdentifier{
			URI:     DocumentURI(pathToURI(filePath)),
			Version: 1,
		},
		ContentChanges: []TextDocumentContentChangeEvent{{Text: "fn main() {}\n"}},
	})
	if err != nil {
		t.Fatalf("marshal change params: %v", err)
	}

	inputReader, inputWriter := io.Pipe()
	defer inputWriter.Close()
	want := errors.New("debounced diagnostic write failed")
	runDone := make(chan error, 1)
	go func() {
		runDone <- Run(inputReader, &failingProtocolOutput{failAt: 1, err: want})
	}()
	if err := writeMessage(inputWriter, Request{
		JSONRPC: "2.0",
		Method:  "textDocument/didChange",
		Params:  changeParams,
	}); err != nil {
		t.Fatalf("write change request: %v", err)
	}

	select {
	case err := <-runDone:
		if !errors.Is(err, want) {
			t.Fatalf("Run error = %v, want %v", err, want)
		}
	case <-time.After(diagnosticsDebounceDelay + time.Second):
		_ = inputWriter.Close()
		err := <-runDone
		t.Fatalf("Run remained blocked with input open; after close error = %v", err)
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
	src := "/// main docs\nfn __CURSOR__main() -> i32 {\n\treturn 0;\n}\n"

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

func TestHoverShowsDocCommentAcrossGapAndSkippedComment(t *testing.T) {
	root := t.TempDir()
	mainPath := filepath.Join(root, "main"+peeper.SourceExt)
	src := "/// first docs\n\n/// second docs\n// note\nfn __CURSOR__main() -> i32 {\n\treturn 0;\n}\n"

	state := NewServerState()
	state.RootDir = root
	hover := hoverAtSource(t, state, mainPath, src)
	if hover == nil {
		t.Fatalf("expected hover result, got nil")
	}
	if !strings.Contains(hover.Contents.Value, "first docs  \nsecond docs") {
		t.Fatalf("expected merged doc comment in hover, got %q", hover.Contents.Value)
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

func TestHoverShowsInlineTypeSyntax(t *testing.T) {
	cases := []struct {
		name   string
		syntax string
	}{
		{name: "primitive", syntax: "i32"},
		{name: "owned pointer", syntax: "*i32"},
		{name: "optional", syntax: "?i32"},
		{name: "dynamic array", syntax: "[]i32"},
		{name: "mutable reference", syntax: "&mut i32"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			mainPath := filepath.Join(root, "main"+peeper.SourceExt)
			src := "fn use(value: __CURSOR__" + tc.syntax + ") {}\n"

			state := NewServerState()
			state.RootDir = root
			hover := hoverAtSource(t, state, mainPath, src)
			if hover == nil {
				t.Fatalf("expected hover result for %s, got nil", tc.syntax)
			}
			if !strings.Contains(hover.Contents.Value, "(type) "+tc.syntax) {
				t.Fatalf("unexpected hover contents for %s: %q", tc.syntax, hover.Contents.Value)
			}
		})
	}
}

func TestHoverShowsAppliedNamedType(t *testing.T) {
	root := t.TempDir()
	mainPath := filepath.Join(root, "main"+peeper.SourceExt)
	src := "struct Box<T> { value: T }\nfn use(value: __CURSOR__Box<i32>) {}\n"

	state := NewServerState()
	state.RootDir = root
	hover := hoverAtSource(t, state, mainPath, src)
	if hover == nil {
		t.Fatal("expected applied type hover")
	}
	if !strings.Contains(hover.Contents.Value, "(type) Box<i32>") {
		t.Fatalf("unexpected applied type hover: %q", hover.Contents.Value)
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
	src := "struct Counter {\n\tvalue: i32,\n}\n\nfn (self: &mut Counter) write(val: i32) {\n\tself.value = val;\n}\n\nfn main() {\n\tlet mut counter: Counter;\n\tcounter.__CURSOR__write(7);\n}\n"

	state := NewServerState()
	state.RootDir = root
	hover := hoverAtSource(t, state, mainPath, src)
	if hover == nil {
		t.Fatalf("expected hover result, got nil")
	}
	if !strings.Contains(hover.Contents.Value, "(method) fn (self: &mut Counter) write(val: i32)") {
		t.Fatalf("unexpected hover contents: %q", hover.Contents.Value)
	}
}

func TestHoverRejectsCompilerOwnedFunctionAsMethod(t *testing.T) {
	root := t.TempDir()
	mainPath := filepath.Join(root, "main"+peeper.SourceExt)
	src := "fn main() {\n\tlet text: str = \"a\";\n\ttext.__CURSOR__len();\n}\n"

	state := NewServerState()
	state.RootDir = root
	hover := hoverAtSource(t, state, mainPath, src)
	if hover != nil {
		t.Fatalf("compiler function selector hover = %#v, want nil", hover)
	}
}

func TestHoverDoesNotShowCompilerFunctionsAsTypeMethods(t *testing.T) {
	root := t.TempDir()
	mainPath := filepath.Join(root, "main"+peeper.SourceExt)
	src := "fn use(value: __CURSOR__str) {}\n"

	state := NewServerState()
	state.RootDir = root
	hover := hoverAtSource(t, state, mainPath, src)
	if hover == nil {
		t.Fatalf("expected hover result, got nil")
	}
	for _, excluded := range []string{
		" len(",
		" as_bytes(",
		" as_chars(",
	} {
		if strings.Contains(hover.Contents.Value, excluded) {
			t.Fatalf("unexpected compiler function %q in type hover: %q", excluded, hover.Contents.Value)
		}
	}
}

func TestHoverSyntheticMethodUsesSemanticParameterNames(t *testing.T) {
	sym := symbols.New("contains", symbols.SymbolMethod, nil, nil)
	sym.Type = &typeinfo.FuncType{
		Params: []typeinfo.Type{
			&typeinfo.RefType{Target: &typeinfo.StringType{}},
			&typeinfo.ByteType{},
		},
		ParamNames: []string{"self", "needle"},
		Return:     &typeinfo.BoolType{},
	}

	got := renderSymbol(sym, symbolRenderContext{Embedded: true})
	want := "fn (self: &str) contains(needle: byte) -> bool"
	if got != want {
		t.Fatalf("synthetic method hover = %q, want %q", got, want)
	}
}

func TestHoverShowsDefaultParameterExpression(t *testing.T) {
	root := t.TempDir()
	mainPath := filepath.Join(root, "main"+peeper.SourceExt)
	src := "fn Add(base: i32, step: i32 = base + 1) -> i32 { return base + step; }\n\nfn main() -> i32 { return __CURSOR__Add(4); }\n"

	state := NewServerState()
	state.RootDir = root
	hover := hoverAtSource(t, state, mainPath, src)
	if hover == nil {
		t.Fatalf("expected hover result, got nil")
	}
	if !strings.Contains(hover.Contents.Value, "(func) fn Add(base: i32, step: i32 = (base + 1)) -> i32") {
		t.Fatalf("expected default expression in hover contents: %q", hover.Contents.Value)
	}
}

func TestHoverShowsReferenceReturnContract(t *testing.T) {
	root := t.TempDir()
	mainPath := filepath.Join(root, "main"+peeper.SourceExt)
	src := "fn Identity(value: &i32) -> &i32 from value {\n\treturn value;\n}\n\nfn Use(value: &i32) -> &i32 from value {\n\treturn __CURSOR__Identity(value);\n}\n"

	state := NewServerState()
	state.RootDir = root
	hover := hoverAtSource(t, state, mainPath, src)
	if hover == nil {
		t.Fatalf("expected hover result, got nil")
	}
	if !strings.Contains(hover.Contents.Value, "(func) fn Identity(value: &i32) -> &i32 from value") {
		t.Fatalf("unexpected hover contents: %q", hover.Contents.Value)
	}
}

func TestHoverResolvesBodylessReferenceReturnSource(t *testing.T) {
	root := t.TempDir()
	mainPath := filepath.Join(root, "main"+peeper.SourceExt)
	src := "#[extern]\nfn External(value: &i32) -> &i32 from __CURSOR__value;\n"

	state := NewServerState()
	state.RootDir = root
	hover := hoverAtSource(t, state, mainPath, src)
	if hover == nil {
		t.Fatalf("expected hover result, got nil")
	}
	if !strings.Contains(hover.Contents.Value, "(param) value: &i32") {
		t.Fatalf("unexpected hover contents: %q", hover.Contents.Value)
	}
}

func TestHoverShowsInterfaceReferenceReturnContract(t *testing.T) {
	root := t.TempDir()
	mainPath := filepath.Join(root, "main"+peeper.SourceExt)
	src := "iface Reader {\n\tfn (&Self) Current(fallback: &i32) -> &i32 from(self, fallback)\n}\n\nfn Use(reader: &Reader, fallback: &i32) -> &i32 from(reader, fallback) {\n\treturn reader.__CURSOR__Current(fallback);\n}\n"

	state := NewServerState()
	state.RootDir = root
	hover := hoverAtSource(t, state, mainPath, src)
	if hover == nil {
		t.Fatalf("expected hover result, got nil")
	}
	if !strings.Contains(hover.Contents.Value, "(method) fn (self: &Self) Current(fallback: &i32) -> &i32 from (self, fallback)") {
		t.Fatalf("unexpected hover contents: %q", hover.Contents.Value)
	}
}

func TestHoverShowsConsumingFunctionParameter(t *testing.T) {
	root := t.TempDir()
	mainPath := filepath.Join(root, "main"+peeper.SourceExt)
	src := "fn consume(mut value: *i32) {}\n\nfn main() {\n\tlet value: *i32;\n\t__CURSOR__consume(value);\n}\n"

	state := NewServerState()
	state.RootDir = root
	hover := hoverAtSource(t, state, mainPath, src)
	if hover == nil {
		t.Fatalf("expected hover result, got nil")
	}
	if !strings.Contains(hover.Contents.Value, "(func) fn consume(mut value: *i32)") {
		t.Fatalf("expected consuming parameter, got %q", hover.Contents.Value)
	}
}

func TestHoverPreservesMutableInterfaceReceiver(t *testing.T) {
	root := t.TempDir()
	mainPath := filepath.Join(root, "main"+peeper.SourceExt)
	src := "iface Writer {\n\tfn (&mut Self) write(val: i32)\n}\n\nfn use(mut writer: Writer) {\n\twriter.__CURSOR__write(7);\n}\n"

	state := NewServerState()
	state.RootDir = root
	hover := hoverAtSource(t, state, mainPath, src)
	if hover == nil {
		t.Fatalf("expected hover result, got nil")
	}
	if !strings.Contains(hover.Contents.Value, "(method) fn (self: &mut Self) write(val: i32)") {
		t.Fatalf("expected mutable interface receiver, got %q", hover.Contents.Value)
	}
}

func TestHoverShowsReceiverMethodNameSignature(t *testing.T) {
	root := t.TempDir()
	mainPath := filepath.Join(root, "main"+peeper.SourceExt)
	src := "struct Point {\n\tx: i32,\n\ty: i32,\n}\n\nfn (self: Point) __CURSOR__sum() -> i32 {\n\t\treturn self.x + self.y;\n}\n"

	state := NewServerState()
	state.RootDir = root
	hover := hoverAtSource(t, state, mainPath, src)
	if hover == nil {
		t.Fatalf("expected hover result, got nil")
	}
	if !strings.Contains(hover.Contents.Value, "(method) fn (self: Point) sum() -> i32") {
		t.Fatalf("unexpected hover contents: %q", hover.Contents.Value)
	}
}

func TestHoverShowsReceiverTypeInsideMethodSignature(t *testing.T) {
	root := t.TempDir()
	mainPath := filepath.Join(root, "main"+peeper.SourceExt)
	src := "struct Point {\n\tx: i32,\n\ty: i32,\n}\n\nfn (self: __CURSOR__Point) sum() -> i32 {\n\t\treturn self.x + self.y;\n}\n"

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
	src := "iface SummerConsumer {\n\tfn (Self) __CURSOR__consume(val: Summer) -> i32,\n}\n"

	state := NewServerState()
	state.RootDir = root
	hover := hoverAtSource(t, state, mainPath, src)
	if hover == nil {
		t.Fatalf("expected hover result, got nil")
	}
	if !strings.Contains(hover.Contents.Value, "(method) fn (Self) consume(val: Summer) -> i32") {
		t.Fatalf("unexpected hover contents: %q", hover.Contents.Value)
	}
}

func TestHoverShowsBareInterfaceReceiverType(t *testing.T) {
	root := t.TempDir()
	mainPath := filepath.Join(root, "main"+peeper.SourceExt)
	src := "iface Consumer {\n\tfn (__CURSOR__Self) consume(),\n}\n"

	state := NewServerState()
	state.RootDir = root
	hover := hoverAtSource(t, state, mainPath, src)
	if hover == nil {
		t.Fatalf("expected hover result, got nil")
	}
	if !strings.Contains(hover.Contents.Value, "(type) Self") {
		t.Fatalf("unexpected hover contents: %q", hover.Contents.Value)
	}
}

func TestHoverShowsInterfaceTypeWithMultilineMethods(t *testing.T) {
	root := t.TempDir()
	mainPath := filepath.Join(root, "main"+peeper.SourceExt)
	src := "__CURSOR__iface SummerConsumer {\n\tfn (Self) consume(val: Summer) -> i32,\n}\n"

	state := NewServerState()
	state.RootDir = root
	hover := hoverAtSource(t, state, mainPath, src)
	if hover == nil {
		t.Fatalf("expected hover result, got nil")
	}
	if !strings.Contains(hover.Contents.Value, "iface {\n  fn (self: Self) consume(val: Summer) -> i32,\n}") {
		t.Fatalf("unexpected hover contents: %q", hover.Contents.Value)
	}
}

func TestHoverShowsEnumTypeWithMultilineVariants(t *testing.T) {
	root := t.TempDir()
	mainPath := filepath.Join(root, "main"+peeper.SourceExt)
	src := "__CURSOR__enum Color {\n\tRgb: { red: u8, green: u8, blue: u8 },\n\tTransparent,\n}\n"

	state := NewServerState()
	state.RootDir = root
	hover := hoverAtSource(t, state, mainPath, src)
	if hover == nil {
		t.Fatalf("expected hover result, got nil")
	}
	if !strings.Contains(hover.Contents.Value, "enum {\n  Rgb: {red: u8, green: u8, blue: u8},\n  Transparent,\n}") {
		t.Fatalf("unexpected hover contents: %q", hover.Contents.Value)
	}
}

func TestHoverShowsTypeMethodsOnNamedType(t *testing.T) {
	root := t.TempDir()
	mainPath := filepath.Join(root, "main"+peeper.SourceExt)
	src := "struct Point {\n\tx: i32,\n\ty: i32,\n}\n\nfn (self: Point) sum() -> i32 {\n\t\treturn self.x + self.y;\n}\n\nfn main() -> i32 {\n\tlet p: __CURSOR__Point;\n\treturn 0;\n}\n"

	state := NewServerState()
	state.RootDir = root
	hover := hoverAtSource(t, state, mainPath, src)
	if hover == nil {
		t.Fatalf("expected hover result, got nil")
	}
	if !strings.Contains(hover.Contents.Value, "struct {\n  x: i32,\n  y: i32,\n}") {
		t.Fatalf("unexpected hover contents: %q", hover.Contents.Value)
	}
	if !strings.Contains(hover.Contents.Value, "// methods\nfn (self: Point) sum() -> i32") {
		t.Fatalf("expected method list in type hover, got %q", hover.Contents.Value)
	}
}

func TestHoverShowsTypeMethodsInsideNamedStructSyntax(t *testing.T) {
	root := t.TempDir()
	mainPath := filepath.Join(root, "main"+peeper.SourceExt)
	src := "__CURSOR__struct Point {\n\tx: i32,\n\ty: i32,\n}\n\nfn (self: Point) sum() -> i32 {\n\t\treturn self.x + self.y;\n}\n"

	state := NewServerState()
	state.RootDir = root
	hover := hoverAtSource(t, state, mainPath, src)
	if hover == nil {
		t.Fatalf("expected hover result, got nil")
	}
	if !strings.Contains(hover.Contents.Value, "struct {\n  x: i32,\n  y: i32,\n}") {
		t.Fatalf("unexpected hover contents: %q", hover.Contents.Value)
	}
	if !strings.Contains(hover.Contents.Value, "// methods\nfn (self: Point) sum() -> i32") {
		t.Fatalf("expected method list in named struct syntax hover, got %q", hover.Contents.Value)
	}
}

func TestHoverDoesNotLeakMethodsFromUnrelatedComponents(t *testing.T) {
	root := t.TempDir()
	firstPath := filepath.Join(root, "first"+peeper.SourceExt)
	secondPath := filepath.Join(root, "second"+peeper.SourceExt)
	firstSrc := "struct Point {\n\tx: i32,\n}\n\nfn (self: Point) sum() -> i32 {\n\t\treturn self.x;\n}\n"
	secondSrc := "struct Point {\n\ty: i32,\n}\n\nfn (self: Point) sum() -> i32 {\n\t\treturn self.y;\n}\n\nfn main() -> i32 {\n\tlet p: __CURSOR__Point;\n\treturn 0;\n}\n"

	state := NewServerState()
	state.RootDir = root
	state.applyDocumentSnapshot(firstPath, &firstSrc, nil)
	if _, mod := state.recompile(firstPath); mod == nil {
		t.Fatalf("expected compiled module for %s", firstPath)
	}
	hover := hoverAtSource(t, state, secondPath, secondSrc)
	if hover == nil {
		t.Fatalf("expected hover result, got nil")
	}
	if strings.Count(hover.Contents.Value, "fn (self: Point) sum() -> i32") != 1 {
		t.Fatalf("expected exactly one method in hover, got %q", hover.Contents.Value)
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

func TestHoverShowsFlowRefinedOptionalUseType(t *testing.T) {
	root := t.TempDir()
	mainPath := filepath.Join(root, "main"+peeper.SourceExt)
	state := NewServerState()
	state.RootDir = root

	outside := hoverAtSource(t, state, mainPath, `fn read(value: ?i32) -> i32 {
	let carrier: ?i32 = __CURSOR__value;
	if value == none {
		return 0;
	}
	return value;
}`)
	if outside == nil || !strings.Contains(outside.Contents.Value, "(param) value: ?i32") {
		t.Fatalf("outside-proof hover = %#v, want ?i32", outside)
	}

	inside := hoverAtSource(t, state, mainPath, `fn read(value: ?i32) -> i32 {
	let carrier: ?i32 = value;
	if value == none {
		return 0;
	}
	return __CURSOR__value;
}`)
	if inside == nil || !strings.Contains(inside.Contents.Value, "(param) value: i32") {
		t.Fatalf("inside-proof hover = %#v, want i32", inside)
	}
}

func TestLSPRefreshesOptionalFlowDiagnosticsAfterEdit(t *testing.T) {
	tests := []struct {
		name    string
		code    string
		invalid string
		valid   string
	}{
		{
			name: "missing proof",
			code: diagnostics.ErrOptionalPayloadProof,
			invalid: `fn read(value: ?i32) -> i32 {
	return value;
}`,
			valid: `fn read(value: ?i32) -> i32 {
	if value == none {
		return 0;
	}
	return value;
}`,
		},
		{
			name: "unstable index",
			code: diagnostics.ErrUnstableNarrowing,
			invalid: `fn read(values: [2]?i32, index: usize) -> i32 {
	if values[index + 1] == none {
		return 0;
	}
	return values[index + 1];
}`,
			valid: `fn read(values: [2]?i32, index: usize) -> i32 {
	if values[index] == none {
		return 0;
	}
	return values[index];
}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			filePath := filepath.Join(root, "main"+peeper.SourceExt)
			state := NewServerState()
			state.RootDir = root
			version := 1
			state.applyDocumentSnapshot(filePath, &test.invalid, &version)
			params := publishCurrentDiagnostics(t, state, filePath)
			found := false
			for _, diagnostic := range params.Diagnostics {
				if diagnostic.Code == test.code {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("version %d diagnostics = %#v, want %s", version, params.Diagnostics, test.code)
			}

			version++
			state.applyDocumentSnapshot(filePath, &test.valid, &version)
			params = publishCurrentDiagnostics(t, state, filePath)
			if params.Version == nil || *params.Version != version {
				t.Fatalf("recovered diagnostic version = %v, want %d", params.Version, version)
			}
			if hasErrorDiagnostic(params.Diagnostics) {
				t.Fatalf("optional flow diagnostics remained after edit: %#v", params.Diagnostics)
			}
		})
	}
}

func TestHoverShowsDeclarationNodeSignature(t *testing.T) {
	root := t.TempDir()
	mainPath := filepath.Join(root, "main"+peeper.SourceExt)
	src := "__CURSOR__fn identity<T>(value: T) -> T {\n\treturn value;\n}\n"

	state := NewServerState()
	state.RootDir = root
	hover := hoverAtSource(t, state, mainPath, src)
	if hover == nil {
		t.Fatalf("expected hover result, got nil")
	}
	if !strings.Contains(hover.Contents.Value, "(func) fn identity<T>(value: T) -> T") {
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

func TestHoverShowsAttributeDoc(t *testing.T) {
	root := t.TempDir()
	mainPath := filepath.Join(root, "main"+peeper.SourceExt)
	src := `#[__CURSOR__extern("puts")]
fn puts(msg: cstr) -> i32;
`

	state := NewServerState()
	state.RootDir = root
	hover := hoverAtSource(t, state, mainPath, src)
	if hover == nil {
		t.Fatalf("expected hover result, got nil")
	}
	if !strings.Contains(hover.Contents.Value, "(attribute) #[extern]") || !strings.Contains(hover.Contents.Value, "Optional string argument overrides") {
		t.Fatalf("unexpected hover contents: %q", hover.Contents.Value)
	}
}

func TestHoverShowsSecondAttributeDoc(t *testing.T) {
	root := t.TempDir()
	mainPath := filepath.Join(root, "main"+peeper.SourceExt)
	src := `#[extern("puts")]
#[__CURSOR__target_os("linux")]
fn puts(msg: cstr) -> i32;
`

	state := NewServerState()
	state.RootDir = root
	hover := hoverAtSource(t, state, mainPath, src)
	if hover == nil {
		t.Fatalf("expected hover result, got nil")
	}
	if !strings.Contains(hover.Contents.Value, "(attribute) #[target_os]") || !strings.Contains(hover.Contents.Value, "currently ignored with a warning") {
		t.Fatalf("unexpected hover contents: %q", hover.Contents.Value)
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
	if err := Run(io.NopCloser(bytes.NewReader(input.Bytes())), &output); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	published := collectPublishedDiagnostics(t, output.Bytes())
	utilPublished := published[pathToURI(utilPath)]
	if len(utilPublished) == 0 || len(utilPublished[0]) == 0 {
		t.Fatalf("expected diagnostics publish for unopened workspace file %s", utilPath)
	}
}

func TestManifestLoadFailuresPublishOnSourceURI(t *testing.T) {
	tests := []struct {
		name     string
		manifest string
		want     string
	}{
		{name: "malformed", manifest: "not valid toml", want: "parse manifest"},
		{name: "incompatible", manifest: "name = \"app\"\ncompiler = \">=0.2.0\"\nbuild = \"program\"\n", want: "requires compiler"},
		{name: "compatible", manifest: "name = \"app\"\ncompiler = \"<=0.1.0\"\nbuild = \"program\"\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			mainPath := filepath.Join(root, peeper.SourceDirName, peeper.MainFileName)
			writeWorkspaceFile(t, filepath.Join(root, manifest.FileName), test.manifest)
			writeWorkspaceFile(t, mainPath, "fn main() {}\n")

			state := NewServerState()
			state.RootDir = root
			published := publishCurrentDiagnostics(t, state, mainPath)
			if string(published.URI) != pathToURI(mainPath) {
				t.Fatalf("diagnostic URI = %q, want %q", published.URI, pathToURI(mainPath))
			}
			if test.want == "" && len(published.Diagnostics) != 0 {
				t.Fatalf("compatible manifest diagnostics = %#v, want none", published.Diagnostics)
			}
			if test.want != "" && (len(published.Diagnostics) != 1 || !strings.Contains(published.Diagnostics[0].Message, test.want)) {
				t.Fatalf("diagnostics = %#v, want one containing %q", published.Diagnostics, test.want)
			}
		})
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
	if err := Run(io.NopCloser(bytes.NewReader(input.Bytes())), &output); err != nil {
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

func TestLSPDidChangePublishesSyntaxErrorsAfterDebounce(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "main"+peeper.SourceExt)
	initial := "fn main() -> i32 {\n\treturn 0;\n}\n"
	invalid := "fn main() -> i32 {\n\tlet x = ;\n\treturn 0;\n}\n"

	rootURI := DocumentURI(pathToURI(root))
	initParams, err := json.Marshal(InitializeParams{RootURI: &rootURI})
	if err != nil {
		t.Fatalf("marshal initialize params: %v", err)
	}
	openParams, err := json.Marshal(DidOpenTextDocumentParams{
		TextDocument: TextDocumentItem{
			URI:  DocumentURI(pathToURI(filePath)),
			Text: initial,
		},
	})
	if err != nil {
		t.Fatalf("marshal open params: %v", err)
	}
	changeParams, err := json.Marshal(DidChangeTextDocumentParams{
		TextDocument: VersionedTextDocumentIdentifier{URI: DocumentURI(pathToURI(filePath)), Version: 2},
		ContentChanges: []TextDocumentContentChangeEvent{
			{Text: invalid},
		},
	})
	if err != nil {
		t.Fatalf("marshal change params: %v", err)
	}
	initID := json.RawMessage([]byte("1"))

	var input bytes.Buffer
	for _, req := range []Request{
		{JSONRPC: "2.0", ID: &initID, Method: "initialize", Params: initParams},
		{JSONRPC: "2.0", Method: "textDocument/didOpen", Params: openParams},
		{JSONRPC: "2.0", Method: "textDocument/didChange", Params: changeParams},
	} {
		if err := writeMessage(&input, req); err != nil {
			t.Fatalf("write %s: %v", req.Method, err)
		}
	}

	var output bytes.Buffer
	if err := Run(io.NopCloser(bytes.NewReader(input.Bytes())), &output); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	published := collectPublishedDiagnostics(t, output.Bytes())
	filePublished := published[pathToURI(filePath)]
	if len(filePublished) < 2 {
		t.Fatalf("expected diagnostics before and after invalid edit, got %d publishes", len(filePublished))
	}
	last := filePublished[len(filePublished)-1]
	if len(last) == 0 {
		t.Fatalf("expected syntax diagnostics after invalid edit")
	}
}

func TestDiagnosticSnapshotDiscardsStaleGenerationAndPublishesVersion(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "main"+peeper.SourceExt)
	first := "fn main() -> i32 { return 0; }\n"
	second := "fn main() -> i32 { return 1; }\n"
	firstVersion := 4
	secondVersion := 5
	state := NewServerState()
	state.RootDir = root
	state.applyDocumentSnapshot(filePath, &first, &firstVersion)
	stale := state.diagnosticSnapshot(filePath, nil)
	state.applyDocumentSnapshot(filePath, &second, &secondVersion)

	var output bytes.Buffer
	writer := newProtocolWriter(&output)
	if err := publishDiagnosticSnapshot(writer, state, stale); err != nil {
		t.Fatalf("publish stale diagnostics: %v", err)
	}
	if output.Len() != 0 {
		t.Fatalf("stale snapshot published %d bytes", output.Len())
	}

	if err := publishDiagnosticSnapshot(writer, state, state.diagnosticSnapshot(filePath, nil)); err != nil {
		t.Fatalf("publish current diagnostics: %v", err)
	}
	message, err := readMessage(bufio.NewReader(&output))
	if err != nil {
		t.Fatalf("read current diagnostics: %v", err)
	}
	var notification struct {
		Params PublishDiagnosticsParams `json:"params"`
	}
	if err := json.Unmarshal(message, &notification); err != nil {
		t.Fatalf("unmarshal current diagnostics: %v", err)
	}
	if notification.Params.Version == nil || *notification.Params.Version != secondVersion {
		t.Fatalf("diagnostic version = %v, want %d", notification.Params.Version, secondVersion)
	}
}

func TestDocumentMutationWaitsForCheckedDiagnosticPublication(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "main"+peeper.SourceExt)
	first := "fn main() -> i32 { return 0; }\n"
	second := "fn main() -> i32 { return 1; }\n"
	firstVersion := 1
	secondVersion := 2
	state := NewServerState()
	state.RootDir = root
	state.applyDocumentSnapshot(filePath, &first, &firstVersion)
	snapshot := state.diagnosticSnapshot(filePath, nil)

	writer := &blockingDiagnosticWriter{
		entered: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	published := make(chan error, 1)
	go func() {
		published <- publishDiagnosticSnapshot(newProtocolWriter(writer), state, snapshot)
	}()
	select {
	case <-writer.entered:
	case <-time.After(time.Second):
		t.Fatal("diagnostic publication did not reach protocol write")
	}

	mutated := make(chan struct{})
	go func() {
		state.applyDocumentSnapshot(filePath, &second, &secondVersion)
		close(mutated)
	}()
	select {
	case <-mutated:
		t.Fatal("document mutation completed while checked diagnostics were writing")
	case <-time.After(20 * time.Millisecond):
	}

	close(writer.release)
	select {
	case err := <-published:
		if err != nil {
			t.Fatalf("publish diagnostics: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("diagnostic publication did not complete")
	}
	select {
	case <-mutated:
	case <-time.After(time.Second):
		t.Fatal("document mutation did not complete")
	}

	state.mu.Lock()
	gotGeneration := state.diagGeneration
	state.mu.Unlock()
	if gotGeneration != snapshot.generation+1 {
		t.Fatalf("generation = %d, want %d", gotGeneration, snapshot.generation+1)
	}
}

func TestLSPTypingRecoversDiagnosticsWithoutRestart(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "main"+peeper.SourceExt)
	valid := `struct Point { x: i32, }
fn main() {
	let point = .Point{x = 1};
	print(point.x);
}
`
	tests := []struct {
		name    string
		invalid string
	}{
		{name: "unterminated string inserted at start", invalid: `"` + valid},
		{name: "malformed struct literal inserted in middle", invalid: strings.Replace(valid, ".Point{x = 1};", ".Point{x = 1;", 1)},
		{name: "unclosed parenthesis inserted in middle", invalid: strings.Replace(valid, "print(point.x);", "print((point.x);", 1)},
		{name: "missing semicolon in middle", invalid: strings.Replace(valid, "print(point.x);", "print(point.x)", 1)},
		{name: "unclosed block at end", invalid: strings.TrimSuffix(valid, "}\n")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := NewServerState()
			state.RootDir = root
			version := 1
			state.applyDocumentSnapshot(filePath, &valid, &version)
			if params := publishCurrentDiagnostics(t, state, filePath); hasErrorDiagnostic(params.Diagnostics) {
				t.Fatalf("valid source diagnostics = %#v", params.Diagnostics)
			}

			version++
			state.applyDocumentSnapshot(filePath, &test.invalid, &version)
			params := publishCurrentDiagnostics(t, state, filePath)
			if params.Version == nil || *params.Version != version {
				t.Fatalf("invalid diagnostic version = %v, want %d", params.Version, version)
			}
			if !hasErrorDiagnostic(params.Diagnostics) {
				t.Fatal("expected diagnostics while source is incomplete")
			}

			version++
			state.applyDocumentSnapshot(filePath, &valid, &version)
			params = publishCurrentDiagnostics(t, state, filePath)
			if params.Version == nil || *params.Version != version {
				t.Fatalf("recovered diagnostic version = %v, want %d", params.Version, version)
			}
			if hasErrorDiagnostic(params.Diagnostics) {
				t.Fatalf("diagnostics remained after recovery: %#v", params.Diagnostics)
			}
		})
	}
}

func TestLSPNaturalTypingAndDeletionRefreshDiagnostics(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceProjectConfig(t, root, "app")
	filePath := filepath.Join(root, peeper.SourceDirName, peeper.MainFileName)
	existing := `fn main() -> i32 {
	let counter: i32 = 20;
	return counter;
}
`
	writeWorkspaceFile(t, filePath, existing)
	typedDeclaration := "struct Player {\n}\n"
	changes := make([]string, 0, len(typedDeclaration)+1)
	for index := range typedDeclaration {
		changes = append(changes, typedDeclaration[:index+1]+existing)
	}
	changes = append(changes, existing)

	published := runTimedLSPChanges(t, root, filePath, existing, changes)
	finalVersion := len(changes) + 1
	final, ok := diagnosticForVersion(published, finalVersion)
	if !ok {
		t.Fatalf("missing diagnostics publish for typed document version %d", finalVersion)
	}
	if hasErrorDiagnostic(final.Diagnostics) {
		t.Fatalf("stale diagnostics after deletion: %#v", final.Diagnostics)
	}
}

func TestLSPNaturalStructLiteralTypingRecoversWithoutRestart(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceProjectConfig(t, root, "app")
	filePath := filepath.Join(root, peeper.SourceDirName, peeper.MainFileName)
	existing := `struct Player {
	value: i32,
}

fn main() -> i32 {
	return 0;
}
`
	writeWorkspaceFile(t, filePath, existing)
	insertBefore := "\treturn 0;\n"
	typedLiteral := "\tlet player = .{value = 20};\n"
	changes := make([]string, 0, len(typedLiteral)+1)
	for index := range typedLiteral {
		changes = append(changes, strings.Replace(existing, insertBefore, typedLiteral[:index+1]+insertBefore, 1))
	}
	changes = append(changes, existing)

	published := runTimedLSPChanges(t, root, filePath, existing, changes)
	finalVersion := len(changes) + 1
	final, ok := diagnosticForVersion(published, finalVersion)
	if !ok {
		t.Fatalf("missing diagnostics publish for typed document version %d", finalVersion)
	}
	if hasErrorDiagnostic(final.Diagnostics) {
		t.Fatalf("stale diagnostics after struct literal deletion: %#v", final.Diagnostics)
	}
}

func TestLSPRecoversAfterTransientSyntaxEditsWithoutRestart(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceProjectConfig(t, root, "app")
	filePath := filepath.Join(root, peeper.SourceDirName, peeper.MainFileName)
	valid := `fn main() -> i32 {
	let value: i32 = 20;
	return value;
}
`
	writeWorkspaceFile(t, filePath, valid)
	changes := []string{
		strings.Replace(valid, "20;", "\"unfinished;", 1),
		valid,
		strings.Replace(valid, "value;", "(value;", 1),
		valid,
		strings.Replace(valid, "20;", "20", 1),
		valid,
	}

	published := runTimedLSPChanges(t, root, filePath, valid, changes)
	finalVersion := len(changes) + 1
	final, ok := diagnosticForVersion(published, finalVersion)
	if !ok {
		t.Fatalf("missing diagnostics publish for document version %d", finalVersion)
	}
	if hasErrorDiagnostic(final.Diagnostics) {
		t.Fatalf("stale diagnostics after recovery: %#v", final.Diagnostics)
	}
}

func TestConcurrentDifferentFileDiagnosticSnapshotsBothPublish(t *testing.T) {
	root := t.TempDir()
	firstPath := filepath.Join(root, "first"+peeper.SourceExt)
	secondPath := filepath.Join(root, "second"+peeper.SourceExt)
	first := "fn first() {}\n"
	second := "fn second() {}\n"
	version := 1
	state := NewServerState()
	state.RootDir = root
	state.applyDocumentSnapshot(firstPath, &first, &version)
	state.applyDocumentSnapshot(secondPath, &second, &version)
	firstSnapshot := state.diagnosticSnapshot(firstPath, []string{firstPath})
	secondSnapshot := state.diagnosticSnapshot(secondPath, []string{secondPath})

	var output bytes.Buffer
	writer := newProtocolWriter(&output)
	var workers sync.WaitGroup
	errs := make(chan error, 2)
	workers.Add(2)
	go func() {
		defer workers.Done()
		errs <- publishDiagnosticSnapshot(writer, state, firstSnapshot)
	}()
	go func() {
		defer workers.Done()
		errs <- publishDiagnosticSnapshot(writer, state, secondSnapshot)
	}()
	workers.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("publish diagnostics: %v", err)
		}
	}

	published := collectPublishedDiagnostics(t, output.Bytes())
	if len(published[pathToURI(firstPath)]) != 1 || len(published[pathToURI(secondPath)]) != 1 {
		t.Fatalf("published diagnostics = %#v, want one notification per file", published)
	}
}

func TestDiagnosticSnapshotCopiesComponentFiles(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceProjectConfig(t, root, "app")
	mainPath := filepath.Join(root, peeper.SourceDirName, peeper.MainFileName)
	utilPath := filepath.Join(root, peeper.SourceDirName, "util"+peeper.SourceExt)
	writeWorkspaceFile(t, mainPath, "import \"app/util\";\nfn main() { util::Use(); }\n")
	writeWorkspaceFile(t, utilPath, "fn Use() {}\n")
	state := NewServerState()
	state.RootDir = root
	snapshot := state.diagnosticSnapshot(mainPath, nil)
	if len(snapshot.files) != 2 {
		t.Fatalf("snapshot files = %v, want component pair", snapshot.files)
	}
	state.mu.Lock()
	for index := range state.workspace.components {
		if len(state.workspace.components[index].files) > 0 {
			state.workspace.components[index].files[0] = "mutated"
		}
	}
	state.mu.Unlock()
	if snapshot.files[0] == "mutated" {
		t.Fatal("snapshot files alias mutable workspace component")
	}
}

func TestLSPDidChangePublishesInterfaceSeparatorErrorsAfterDebounce(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "main"+peeper.SourceExt)
	initial := "iface SummerConsumer {\n\tfn (Self) consume(val: i32) -> i32,\n}\n"
	invalid := "iface SummerConsumer {\n\tfn (Self) consume(val: i32) -> i32;\n}\n"

	rootURI := DocumentURI(pathToURI(root))
	initParams, err := json.Marshal(InitializeParams{RootURI: &rootURI})
	if err != nil {
		t.Fatalf("marshal initialize params: %v", err)
	}
	openParams, err := json.Marshal(DidOpenTextDocumentParams{
		TextDocument: TextDocumentItem{
			URI:  DocumentURI(pathToURI(filePath)),
			Text: initial,
		},
	})
	if err != nil {
		t.Fatalf("marshal open params: %v", err)
	}
	changeParams, err := json.Marshal(DidChangeTextDocumentParams{
		TextDocument: VersionedTextDocumentIdentifier{URI: DocumentURI(pathToURI(filePath)), Version: 2},
		ContentChanges: []TextDocumentContentChangeEvent{
			{Text: invalid},
		},
	})
	if err != nil {
		t.Fatalf("marshal change params: %v", err)
	}
	initID := json.RawMessage([]byte("1"))

	var input bytes.Buffer
	for _, req := range []Request{
		{JSONRPC: "2.0", ID: &initID, Method: "initialize", Params: initParams},
		{JSONRPC: "2.0", Method: "textDocument/didOpen", Params: openParams},
		{JSONRPC: "2.0", Method: "textDocument/didChange", Params: changeParams},
	} {
		if err := writeMessage(&input, req); err != nil {
			t.Fatalf("write %s: %v", req.Method, err)
		}
	}

	var output bytes.Buffer
	if err := Run(io.NopCloser(bytes.NewReader(input.Bytes())), &output); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	published := collectPublishedDiagnostics(t, output.Bytes())
	filePublished := published[pathToURI(filePath)]
	if len(filePublished) < 2 {
		t.Fatalf("expected diagnostics before and after invalid edit, got %d publishes", len(filePublished))
	}
	last := filePublished[len(filePublished)-1]
	if len(last) == 0 {
		t.Fatalf("expected syntax diagnostics after invalid interface edit")
	}
}
