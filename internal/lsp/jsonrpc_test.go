package lsp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

type rejectedBodyReader struct {
	reads int
}

type failingProtocolOutput struct {
	failAt int
	writes int
	err    error
}

func (w *failingProtocolOutput) Write(p []byte) (int, error) {
	w.writes++
	if w.writes == w.failAt {
		return 0, w.err
	}
	return len(p), nil
}

func (r *rejectedBodyReader) Read([]byte) (int, error) {
	r.reads++
	return 0, errors.New("body must not be read")
}

func TestReadMessageAcceptsMaximumBodySize(t *testing.T) {
	body := bytes.Repeat([]byte{'x'}, maxJSONRPCBodySize)
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body))
	message, err := readMessage(bufio.NewReader(io.MultiReader(strings.NewReader(header), bytes.NewReader(body))))
	if err != nil {
		t.Fatalf("read exact-limit body: %v", err)
	}
	if !bytes.Equal(message, body) {
		t.Fatalf("message length = %d, want %d", len(message), len(body))
	}
}

func TestReadMessageRejectsOversizedBodyBeforeRead(t *testing.T) {
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", maxJSONRPCBodySize+1)
	body := &rejectedBodyReader{}
	_, err := readMessage(bufio.NewReaderSize(io.MultiReader(strings.NewReader(header), body), len(header)))
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized read error = %v", err)
	}
	if body.reads != 0 {
		t.Fatalf("oversized body read %d times", body.reads)
	}
}

func TestReadMessageRejectsInvalidContentLength(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{name: "missing", input: "Content-Type: application/json\r\n\r\n"},
		{name: "empty", input: "Content-Length: \r\n\r\n"},
		{name: "nonnumeric", input: "Content-Length: nope\r\n\r\n"},
		{name: "zero", input: "Content-Length: 0\r\n\r\n"},
		{name: "negative", input: "Content-Length: -1\r\n\r\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := readMessage(bufio.NewReader(strings.NewReader(tt.input))); err == nil {
				t.Fatal("expected Content-Length error")
			}
		})
	}
}

func TestServerResponseResultAndErrorExclusivity(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		wantResult string
		wantError  bool
	}{
		{name: "shutdown null", method: "shutdown", wantResult: "null"},
		{name: "ordinary result", method: "initialize", wantResult: "object"},
		{name: "error", method: "unknown", wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id := json.RawMessage("1")
			params := json.RawMessage(nil)
			if tt.method == "initialize" {
				params = json.RawMessage(`{}`)
			}
			var input bytes.Buffer
			if err := writeMessage(&input, Request{JSONRPC: "2.0", ID: &id, Method: tt.method, Params: params}); err != nil {
				t.Fatalf("write request: %v", err)
			}
			var output bytes.Buffer
			if err := Run(io.NopCloser(&input), &output); err != nil {
				t.Fatalf("Run: %v", err)
			}
			message, err := readMessage(bufio.NewReader(&output))
			if err != nil {
				t.Fatalf("read response: %v", err)
			}
			var envelope map[string]json.RawMessage
			if err := json.Unmarshal(message, &envelope); err != nil {
				t.Fatalf("unmarshal response: %v", err)
			}
			result, hasResult := envelope["result"]
			_, hasError := envelope["error"]
			if tt.wantError {
				if hasResult || !hasError {
					t.Fatalf("error envelope has result=%v error=%v: %s", hasResult, hasError, message)
				}
				return
			}
			if !hasResult || hasError {
				t.Fatalf("success envelope has result=%v error=%v: %s", hasResult, hasError, message)
			}
			if tt.wantResult == "null" && string(result) != "null" {
				t.Fatalf("result = %s, want null", result)
			}
			if tt.wantResult == "object" && (len(result) == 0 || result[0] != '{') {
				t.Fatalf("result = %s, want object", result)
			}
		})
	}
}

func TestRunReturnsResponseWriteFailure(t *testing.T) {
	tests := []struct {
		name   string
		failAt int
	}{
		{name: "header", failAt: 1},
		{name: "body", failAt: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id := json.RawMessage("1")
			var input bytes.Buffer
			if err := writeMessage(&input, Request{JSONRPC: "2.0", ID: &id, Method: "shutdown"}); err != nil {
				t.Fatalf("write request: %v", err)
			}
			want := errors.New(tt.name + " write failed")
			output := &failingProtocolOutput{failAt: tt.failAt, err: want}
			if err := Run(io.NopCloser(&input), output); !errors.Is(err, want) {
				t.Fatalf("Run error = %v, want %v", err, want)
			}
		})
	}
}

func TestProtocolWriterStopsAfterFirstFailure(t *testing.T) {
	want := errors.New("header write failed")
	output := &failingProtocolOutput{failAt: 1, err: want}
	writer := newProtocolWriter(output)
	if err := writer.write(Notification{JSONRPC: "2.0", Method: "first"}); !errors.Is(err, want) {
		t.Fatalf("first write error = %v, want %v", err, want)
	}
	if err := writer.write(Notification{JSONRPC: "2.0", Method: "second"}); !errors.Is(err, want) {
		t.Fatalf("second write error = %v, want %v", err, want)
	}
	if output.writes != 1 {
		t.Fatalf("underlying writes = %d, want 1", output.writes)
	}
}
