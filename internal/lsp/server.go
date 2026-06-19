package lsp

import (
	"bufio"
	"compiler/internal/diagnostics"
	"compiler/internal/project"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
)

const LSP_VERSION = "0.0.1"

func Run(in io.Reader, out io.Writer) error {
	reader := bufio.NewReader(in)
	state := NewServerState()

	for {
		bytes, err := readMessage(reader)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}

		var req Request
		if err := json.Unmarshal(bytes, &req); err != nil {
			continue
		}

		var result any
		var respErr *ResponseError

		switch req.Method {
		case "initialize":
			var params InitializeParams
			if err := json.Unmarshal(req.Params, &params); err == nil {
				if params.RootURI != nil {
					state.RootDir = uriToPath(string(*params.RootURI))
				} else if params.RootPath != nil {
					state.RootDir = *params.RootPath
				}
				state.workspace = newWorkspaceIndex(state.RootDir)
			}
			result = InitializeResult{
				Capabilities: ServerCapabilities{
					TextDocumentSync:   1, // Full Sync
					HoverProvider:      true,
					DefinitionProvider: true,
					RenameProvider:     true,
				},
				ServerInfo: &ServerInfo{
					Name:    "Peeper Language Server",
					Version: LSP_VERSION,
				},
			}

		case "initialized":
			publishWorkspaceDiagnostics(out, state)
			continue

		case "textDocument/didOpen":
			var params DidOpenTextDocumentParams
			if err := json.Unmarshal(req.Params, &params); err == nil {
				path := uriToPath(string(params.TextDocument.URI))
				state.Cache[path] = params.TextDocument.Text
				publishComponentDiagnostics(out, state, path, nil)
			}
			continue

		case "textDocument/didChange":
			var params DidChangeTextDocumentParams
			if err := json.Unmarshal(req.Params, &params); err == nil && len(params.ContentChanges) > 0 {
				path := uriToPath(string(params.TextDocument.URI))
				// Under Full Sync, the first change has the entire file text
				state.Cache[path] = params.ContentChanges[0].Text
				publishComponentDiagnostics(out, state, path, nil)
			}
			continue

		case "textDocument/didClose":
			var params TextDocumentIdentifier
			if err := json.Unmarshal(req.Params, &params); err == nil {
				path := uriToPath(string(params.URI))
				delete(state.Cache, path)
				publishComponentDiagnostics(out, state, path, nil)
			}
			continue

		case "textDocument/hover":
			var params HoverParams
			if err := json.Unmarshal(req.Params, &params); err == nil {
				result, err = state.HandleHover(params)
				if err != nil {
					respErr = &ResponseError{Code: -32603, Message: err.Error()}
				}
			} else {
				respErr = &ResponseError{Code: -32602, Message: "Invalid params"}
			}

		case "textDocument/definition":
			var params DefinitionParams
			if err := json.Unmarshal(req.Params, &params); err == nil {
				result, err = state.HandleDefinition(params)
				if err != nil {
					respErr = &ResponseError{Code: -32603, Message: err.Error()}
				}
			} else {
				respErr = &ResponseError{Code: -32602, Message: "Invalid params"}
			}

		case "textDocument/rename":
			var params RenameParams
			if err := json.Unmarshal(req.Params, &params); err == nil {
				result, err = state.HandleRename(params)
				if err != nil {
					respErr = &ResponseError{Code: -32603, Message: err.Error()}
				}
			} else {
				respErr = &ResponseError{Code: -32602, Message: "Invalid params"}
			}

		case "shutdown":
			result = nil

		case "exit":
			return nil

		default:
			if req.ID != nil {
				respErr = &ResponseError{Code: -32601, Message: "Method not found"}
			}
		}

		if req.ID != nil {
			resp := Response{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result:  result,
				Error:   respErr,
			}
			_ = writeMessage(out, resp)
		}
	}
}

func publishWorkspaceDiagnostics(w io.Writer, state *ServerState) {
	if state == nil || state.RootDir == "" {
		return
	}
	if state.workspace == nil {
		state.workspace = newWorkspaceIndex(state.RootDir)
	}
	if err := state.workspace.rebuild(state.Cache); err != nil {
		return
	}
	for _, component := range state.workspace.components {
		if len(component.files) == 0 {
			continue
		}
		entry := component.files[0]
		if len(component.roots) > 0 {
			entry = component.roots[0]
		}
		publishComponentDiagnostics(w, state, entry, component.files)
	}
}

func componentFilesForPublish(state *ServerState, filePath string) []string {
	if state == nil || state.workspace == nil {
		return []string{filePath}
	}
	component, ok := state.workspace.componentForFile(filePath)
	if !ok || len(component.files) == 0 {
		return []string{filePath}
	}
	return append([]string(nil), component.files...)
}

func publishComponentDiagnostics(w io.Writer, state *ServerState, entryFile string, files []string) {
	if state == nil {
		return
	}
	ctx, _ := state.recompile(entryFile)
	if len(files) == 0 {
		files = componentFilesForPublish(state, entryFile)
	}
	sendDiagnosticsForFiles(w, ctx, files)
}

func uriToPath(uri string) string {
	if after, ok := strings.CutPrefix(uri, "file://"); ok {
		path := after
		if len(path) > 2 && path[0] == '/' && path[2] == ':' {
			path = path[1:]
		}
		return filepath.Clean(filepath.ToSlash(path))
	}
	return uri
}

func pathToURI(path string) string {
	clean := filepath.ToSlash(filepath.Clean(path))
	if len(clean) > 0 && clean[0] != '/' {
		return "file:///" + clean
	}
	return "file://" + clean
}

func sendDiagnosticsForFiles(w io.Writer, ctx *project.CompilerContext, files []string) {
	if ctx == nil || ctx.Diagnostics == nil {
		return
	}

	grouped := make(map[DocumentURI][]Diagnostic, len(files))
	for _, filePath := range files {
		uri := DocumentURI(pathToURI(filePath))
		grouped[uri] = []Diagnostic{}
	}

	for _, diag := range ctx.Diagnostics.Diagnostics() {
		filePath := diag.FilePath
		if filePath == "" {
			continue
		}
		uri := DocumentURI(pathToURI(filePath))
		if _, ok := grouped[uri]; !ok {
			continue
		}

		var r Range
		hasRange := false
		for _, label := range diag.Labels {
			if label.Location != nil && label.Location.Start != nil && label.Location.End != nil {
				r = Range{
					Start: Position{Line: label.Location.Start.Line - 1, Character: label.Location.Start.Column - 1},
					End:   Position{Line: label.Location.End.Line - 1, Character: label.Location.End.Column - 1},
				}
				hasRange = true
				break
			}
		}
		if !hasRange {
			r = Range{
				Start: Position{Line: 0, Character: 0},
				End:   Position{Line: 0, Character: 0},
			}
		}

		severity := 1
		switch diag.Severity {
		case diagnostics.Error:
			severity = 1
		case diagnostics.Warning:
			severity = 2
		case diagnostics.Info:
			severity = 3
		case diagnostics.Hint:
			severity = 4
		}

		var message strings.Builder
		message.WriteString(diag.Message)
		for _, extra := range diag.Extras {
			if extra.Kind != diagnostics.ExtraText || extra.Text.Message == "" {
				continue
			}
			switch extra.Text.Kind {
			case "help":
				message.WriteString("\nHelp: ")
				message.WriteString(extra.Text.Message)
			case "note":
				message.WriteString("\nNote: ")
				message.WriteString(extra.Text.Message)
			}
		}

		grouped[uri] = append(grouped[uri], Diagnostic{
			Range:    r,
			Severity: severity,
			Code:     diag.Code,
			Source:   "Peeper",
			Message:  message.String(),
		})
	}

	for uri, lspDiags := range grouped {
		_ = writeMessage(w, Notification{
			JSONRPC: "2.0",
			Method:  "textDocument/publishDiagnostics",
			Params: PublishDiagnosticsParams{
				URI:         uri,
				Diagnostics: lspDiags,
			},
		})
	}
}
