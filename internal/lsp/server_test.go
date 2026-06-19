package lsp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

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
	filePath := filepath.Join(tmpDir, "main.peep")
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

func TestLSPInitializedPublishesDiagnosticsForUnopenedWorkspaceFiles(t *testing.T) {
	root := t.TempDir()
	mainPath := filepath.Join(root, "main.peep")
	utilPath := filepath.Join(root, "util.peep")
	writeWorkspaceFile(t, mainPath, "import \"util\";\nfn main() -> i32 { return util::Helper(); }\n")
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

	reader := bufio.NewReader(bytes.NewReader(output.Bytes()))
	var sawUtilDiag bool
	for {
		msg, err := readMessage(reader)
		if err != nil {
			break
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
		if string(params.URI) != pathToURI(utilPath) {
			continue
		}
		if len(params.Diagnostics) == 0 {
			t.Fatalf("expected unopened util file diagnostics to be published")
		}
		sawUtilDiag = true
	}
	if !sawUtilDiag {
		t.Fatalf("expected diagnostics publish for unopened workspace file %s", utilPath)
	}
}
