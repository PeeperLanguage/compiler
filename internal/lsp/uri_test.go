package lsp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"testing"
)

func TestFileURIToPath(t *testing.T) {
	tests := []struct {
		name string
		uri  string
		want string
	}{
		{name: "unix escapes", uri: "file:///tmp/A%20B/%C3%A9%23%25.peep", want: "/tmp/A B/é#%.peep"},
		{name: "decode once", uri: "file:///tmp/%2520.peep", want: "/tmp/%20.peep"},
		{name: "localhost", uri: "file://localhost/tmp/A%20B.peep", want: "/tmp/A B.peep"},
		{name: "windows drive", uri: "file:///C:/Work%20Dir/main.peep", want: "C:/Work Dir/main.peep"},
		{name: "unc authority", uri: "file://server/share/A%20B.peep", want: "//server/share/A B.peep"},
		{name: "clean path", uri: "file:///tmp/one/../two.peep", want: "/tmp/two.peep"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := uriToPath(tt.uri)
			if err != nil {
				t.Fatalf("uriToPath: %v", err)
			}
			if got != tt.want {
				t.Fatalf("uriToPath(%q) = %q, want %q", tt.uri, got, tt.want)
			}
		})
	}
}

func TestFileURIToPathRejectsInvalidInput(t *testing.T) {
	for _, uri := range []string{
		"https://example.com/main.peep",
		"file:///tmp/%zz.peep",
		"file:///tmp/main.peep?version=1",
		"file:///tmp/main.peep#section",
		"file:///tmp/%00.peep",
		"file://localhost",
	} {
		t.Run(uri, func(t *testing.T) {
			if _, err := uriToPath(uri); err == nil {
				t.Fatalf("uriToPath(%q) succeeded", uri)
			}
		})
	}
}

func TestPathToFileURI(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "unix", path: "/tmp/A B/é#%.peep", want: "file:///tmp/A%20B/%C3%A9%23%25.peep"},
		{name: "windows drive", path: `C:\Work Dir\é#%.peep`, want: "file:///C:/Work%20Dir/%C3%A9%23%25.peep"},
		{name: "unc", path: `\\server\share\A B#%.peep`, want: "file://server/share/A%20B%23%25.peep"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pathToURI(tt.path); got != tt.want {
				t.Fatalf("pathToURI(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestRenameRejectsInvalidSymbolNamesBeforeCompilation(t *testing.T) {
	state := NewServerState()
	for _, name := range []string{"", "_", "123name", "two words", "éclair", "fn", "let"} {
		t.Run(name, func(t *testing.T) {
			_, err := state.HandleRename(RenameParams{
				TextDocument: TextDocumentIdentifier{URI: "file:///missing.peep"},
				NewName:      name,
			})
			var responseErr *ResponseError
			if !errors.As(err, &responseErr) || responseErr.Code != -32602 {
				t.Fatalf("rename error = %v, want invalid params", err)
			}
			if state.LastCtx != nil {
				t.Fatal("invalid rename compiled source")
			}
		})
	}
}

func TestMalformedRequestURIMapsToInvalidParams(t *testing.T) {
	invalidRoot := DocumentURI("https://example.com")
	tests := []struct {
		method string
		params any
	}{
		{method: "initialize", params: InitializeParams{RootURI: &invalidRoot}},
		{method: "textDocument/hover", params: HoverParams{TextDocumentPositionParams: TextDocumentPositionParams{TextDocument: TextDocumentIdentifier{URI: "https://example.com/main.peep"}}}},
		{method: "textDocument/definition", params: DefinitionParams{TextDocumentPositionParams: TextDocumentPositionParams{TextDocument: TextDocumentIdentifier{URI: "https://example.com/main.peep"}}}},
		{method: "textDocument/completion", params: CompletionParams{TextDocumentPositionParams: TextDocumentPositionParams{TextDocument: TextDocumentIdentifier{URI: "https://example.com/main.peep"}}}},
		{method: "textDocument/rename", params: RenameParams{TextDocument: TextDocumentIdentifier{URI: "https://example.com/main.peep"}, NewName: "valid_name"}},
	}
	for _, tt := range tests {
		t.Run(tt.method, func(t *testing.T) {
			params, err := json.Marshal(tt.params)
			if err != nil {
				t.Fatalf("marshal params: %v", err)
			}
			id := json.RawMessage("1")
			var input bytes.Buffer
			if err := writeMessage(&input, Request{JSONRPC: "2.0", ID: &id, Method: tt.method, Params: params}); err != nil {
				t.Fatalf("write request: %v", err)
			}
			var output bytes.Buffer
			if err := Run(&input, &output); err != nil {
				t.Fatalf("Run: %v", err)
			}
			message, err := readMessage(bufio.NewReader(&output))
			if err != nil {
				t.Fatalf("read response: %v", err)
			}
			var response Response
			if err := json.Unmarshal(message, &response); err != nil {
				t.Fatalf("unmarshal response: %v", err)
			}
			if response.Error == nil || response.Error.Code != -32602 || response.Result != nil {
				t.Fatalf("response = %+v, want invalid params without result", response)
			}
		})
	}
}

func TestResponseErrorMappingPreservesProtocolErrors(t *testing.T) {
	protocolErr := invalidParams("bad input")
	if got := responseErrorFrom(protocolErr); got != protocolErr {
		t.Fatalf("typed protocol error replaced: got %+v", got)
	}
	if got := responseErrorFrom(errors.New("boom")); got.Code != -32603 || got.Message != "boom" {
		t.Fatalf("internal error mapping = %+v", got)
	}
}

func TestMalformedNotificationURIDoesNotPublishOrMutateProtocolState(t *testing.T) {
	params, err := json.Marshal(DidOpenTextDocumentParams{TextDocument: TextDocumentItem{
		URI:  "https://example.com/main.peep",
		Text: "fn main() {}",
	}})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	id := json.RawMessage("1")
	var input bytes.Buffer
	for _, request := range []Request{
		{JSONRPC: "2.0", Method: "textDocument/didOpen", Params: params},
		{JSONRPC: "2.0", ID: &id, Method: "shutdown"},
	} {
		if err := writeMessage(&input, request); err != nil {
			t.Fatalf("write request: %v", err)
		}
	}
	var output bytes.Buffer
	if err := Run(&input, &output); err != nil {
		t.Fatalf("Run: %v", err)
	}
	reader := bufio.NewReader(&output)
	if _, err := readMessage(reader); err != nil {
		t.Fatalf("read shutdown response: %v", err)
	}
	if _, err := readMessage(reader); err == nil {
		t.Fatal("malformed notification URI produced extra output")
	}
}
