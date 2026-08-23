package lsp

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"strings"
	"time"

	"compiler/internal/diagnostics"
	"compiler/internal/project"
)

const LSP_VERSION = "0.0.1"
const diagnosticsDebounceDelay = 150 * time.Millisecond

func Run(in io.ReadCloser, out io.Writer) error {
	reader := bufio.NewReader(in)
	state := NewServerState()
	writer := newProtocolWriter(out)
	sessionDone := make(chan struct{})
	defer close(sessionDone)
	go func() {
		select {
		case <-writer.failureCh:
			_ = in.Close()
		case <-sessionDone:
		}
	}()

	for {
		if err := writer.writeError(); err != nil {
			return err
		}
		bytes, err := readMessage(reader)
		if err != nil {
			if writeErr := writer.writeError(); writeErr != nil {
				return writeErr
			}
			if errors.Is(err, io.EOF) {
				if err := state.waitForScheduledDiagnostics(); err != nil {
					return err
				}
				return writer.writeError()
			}
			return err
		}
		if err := writer.writeError(); err != nil {
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
			if err := json.Unmarshal(req.Params, &params); err != nil {
				respErr = invalidParams("Invalid params")
				break
			}
			rootDir := state.RootDir
			if params.RootURI != nil {
				rootDir, err = uriToPath(string(*params.RootURI))
				if err != nil {
					respErr = invalidParams(err.Error())
					break
				}
			} else if params.RootPath != nil {
				rootDir = *params.RootPath
			}
			state.RootDir = rootDir
			state.workspace = newWorkspaceIndex(state.RootDir)
			result = InitializeResult{
				Capabilities: ServerCapabilities{
					TextDocumentSync:   1, // Full Sync
					HoverProvider:      true,
					DefinitionProvider: true,
					RenameProvider:     true,
					CompletionProvider: &CompletionOptions{
						TriggerCharacters: []string{".", "|", ">", ":", "/", "\""},
					},
				},
				ServerInfo: &ServerInfo{
					Name:    "Peeper Language Server",
					Version: LSP_VERSION,
				},
			}

		case "initialized":
			if err := publishWorkspaceDiagnostics(writer, state); err != nil {
				return err
			}
			continue

		case "textDocument/didOpen":
			var params DidOpenTextDocumentParams
			if err := json.Unmarshal(req.Params, &params); err == nil {
				filePath, uriErr := uriToPath(string(params.TextDocument.URI))
				if uriErr != nil {
					continue
				}
				state.applyDocumentSnapshot(filePath, &params.TextDocument.Text, &params.TextDocument.Version)
				if err := publishComponentDiagnostics(writer, state, filePath, nil); err != nil {
					return err
				}
			}
			continue

		case "textDocument/didChange":
			var params DidChangeTextDocumentParams
			if err := json.Unmarshal(req.Params, &params); err == nil && len(params.ContentChanges) > 0 {
				filePath, uriErr := uriToPath(string(params.TextDocument.URI))
				if uriErr != nil {
					continue
				}
				// Under Full Sync, the first change has the entire file text
				state.applyDocumentSnapshot(filePath, &params.ContentChanges[0].Text, &params.TextDocument.Version)
				state.scheduleDiagnosticRefresh(filePath, diagnosticsDebounceDelay, func() error {
					return publishComponentDiagnostics(writer, state, filePath, nil)
				})
			}
			continue

		case "textDocument/didClose":
			var params TextDocumentIdentifier
			if err := json.Unmarshal(req.Params, &params); err == nil {
				filePath, uriErr := uriToPath(string(params.URI))
				if uriErr != nil {
					continue
				}
				state.applyDocumentSnapshot(filePath, nil, nil)
				if err := publishComponentDiagnostics(writer, state, filePath, nil); err != nil {
					return err
				}
			}
			continue

		case "textDocument/hover":
			var params HoverParams
			if err := json.Unmarshal(req.Params, &params); err == nil {
				result, err = state.HandleHover(params)
				if err != nil {
					respErr = responseErrorFrom(err)
				}
			} else {
				respErr = invalidParams("Invalid params")
			}

		case "textDocument/definition":
			var params DefinitionParams
			if err := json.Unmarshal(req.Params, &params); err == nil {
				result, err = state.HandleDefinition(params)
				if err != nil {
					respErr = responseErrorFrom(err)
				}
			} else {
				respErr = invalidParams("Invalid params")
			}

		case "textDocument/completion":
			var params CompletionParams
			if err := json.Unmarshal(req.Params, &params); err == nil {
				result, err = state.HandleCompletion(params)
				if err != nil {
					respErr = responseErrorFrom(err)
				}
			} else {
				respErr = invalidParams("Invalid params")
			}

		case "textDocument/rename":
			var params RenameParams
			if err := json.Unmarshal(req.Params, &params); err == nil {
				result, err = state.HandleRename(params)
				if err != nil {
					respErr = responseErrorFrom(err)
				}
			} else {
				respErr = invalidParams("Invalid params")
			}

		case "shutdown":
			result = nil

		case "exit":
			if err := state.waitForScheduledDiagnostics(); err != nil {
				return err
			}
			return writer.writeError()

		default:
			if req.ID != nil {
				respErr = &ResponseError{Code: -32601, Message: "Method not found"}
			}
		}

		if req.ID != nil {
			resp := Response{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error:   respErr,
			}
			if respErr == nil {
				resp.Result = &result
			}
			if err := writer.write(resp); err != nil {
				return err
			}
		}
	}
}

func publishWorkspaceDiagnostics(writer *protocolWriter, state *ServerState) error {
	for _, snapshot := range state.workspaceDiagnosticSnapshots() {
		if err := publishDiagnosticSnapshot(writer, state, snapshot); err != nil {
			return err
		}
	}
	return nil
}

func publishComponentDiagnostics(writer *protocolWriter, state *ServerState, entryFile string, files []string) error {
	if state == nil {
		return nil
	}
	return publishDiagnosticSnapshot(writer, state, state.diagnosticSnapshot(entryFile, files))
}

func uriToPath(rawURI string) (string, error) {
	parsed, err := url.Parse(rawURI)
	if err != nil {
		return "", fmt.Errorf("invalid file URI: %w", err)
	}
	if !strings.EqualFold(parsed.Scheme, "file") || parsed.Opaque != "" {
		return "", fmt.Errorf("invalid file URI scheme %q", parsed.Scheme)
	}
	if parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || strings.Contains(rawURI, "#") {
		return "", fmt.Errorf("file URI must not contain query or fragment")
	}
	escapedPath := parsed.EscapedPath()
	if escapedPath == "" {
		return "", fmt.Errorf("file URI path is empty")
	}
	decodedPath, err := url.PathUnescape(escapedPath)
	if err != nil {
		return "", fmt.Errorf("invalid file URI path: %w", err)
	}
	if strings.ContainsRune(decodedPath, '\x00') {
		return "", fmt.Errorf("file URI path contains NUL")
	}
	decodedPath = strings.ReplaceAll(decodedPath, `\`, "/")
	if parsed.Host != "" && !strings.EqualFold(parsed.Host, "localhost") {
		return "//" + parsed.Host + path.Clean("/"+strings.TrimPrefix(decodedPath, "/")), nil
	}
	clean := path.Clean(decodedPath)
	if len(clean) >= 3 && clean[0] == '/' && isWindowsDrivePath(clean[1:]) {
		clean = clean[1:]
	}
	return clean, nil
}

func pathToURI(filePath string) string {
	slashPath := strings.ReplaceAll(filePath, `\`, "/")
	if strings.HasPrefix(slashPath, "//") {
		authority, rest, _ := strings.Cut(strings.TrimPrefix(slashPath, "//"), "/")
		return (&url.URL{Scheme: "file", Host: authority, Path: path.Clean("/" + rest)}).String()
	}
	clean := path.Clean(slashPath)
	if isWindowsDrivePath(clean) || !strings.HasPrefix(clean, "/") {
		clean = "/" + clean
	}
	return (&url.URL{Scheme: "file", Path: clean}).String()
}

func isWindowsDrivePath(filePath string) bool {
	if len(filePath) < 3 || filePath[1] != ':' || filePath[2] != '/' {
		return false
	}
	drive := filePath[0]
	return drive >= 'A' && drive <= 'Z' || drive >= 'a' && drive <= 'z'
}

func publishDiagnosticSnapshot(writer *protocolWriter, state *ServerState, snapshot *diagnosticSnapshot) error {
	if state == nil || snapshot == nil || snapshot.ctx == nil || snapshot.ctx.Diagnostics == nil {
		return nil
	}
	notifications := diagnosticNotifications(snapshot)
	state.publishMu.Lock()
	defer state.publishMu.Unlock()
	state.mu.Lock()
	stale := state.diagGeneration != snapshot.generation
	state.mu.Unlock()
	if stale {
		return nil
	}
	for _, notification := range notifications {
		if err := writer.write(notification); err != nil {
			return err
		}
	}
	return nil
}

func diagnosticNotifications(snapshot *diagnosticSnapshot) []Notification {
	grouped := make(map[DocumentURI][]Diagnostic, len(snapshot.files))
	for _, filePath := range snapshot.files {
		uri := DocumentURI(pathToURI(filePath))
		grouped[uri] = []Diagnostic{}
	}

	for _, diag := range snapshot.ctx.Diagnostics.Diagnostics() {
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
		text, hasText := sourceTextForFile(snapshot.ctx, filePath)
		for _, label := range diag.Labels {
			if labelRange, ok := rangeAtLocation(text, label.Location); hasText && ok {
				r = labelRange
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

	notifications := make([]Notification, 0, len(snapshot.files))
	for _, filePath := range snapshot.files {
		uri := DocumentURI(pathToURI(filePath))
		params := PublishDiagnosticsParams{
			URI:         uri,
			Diagnostics: grouped[uri],
		}
		if version, ok := snapshot.versions[project.CanonicalPath(filePath)]; ok {
			params.Version = &version
		}
		notifications = append(notifications, Notification{
			JSONRPC: "2.0",
			Method:  "textDocument/publishDiagnostics",
			Params:  params,
		})
	}
	return notifications
}
