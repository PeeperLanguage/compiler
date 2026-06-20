package lsp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"compiler/pkg/peeper"
)

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
