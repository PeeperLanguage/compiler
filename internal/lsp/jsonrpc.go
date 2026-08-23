package lsp

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Maximum JSON-RPC message body accepted by the language server: 16 MiB.
const maxJSONRPCBodySize = 16 << 20

type Request struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string           `json:"method"`
	Params  json.RawMessage  `json:"params,omitempty"`
}

type Response struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id"`
	Result  *any             `json:"result,omitempty"`
	Error   *ResponseError   `json:"error,omitempty"`
}

type ResponseError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (e *ResponseError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

func invalidParams(message string) *ResponseError {
	return &ResponseError{Code: -32602, Message: message}
}

func responseErrorFrom(err error) *ResponseError {
	var protocolErr *ResponseError
	if errors.As(err, &protocolErr) {
		return protocolErr
	}
	return &ResponseError{Code: -32603, Message: err.Error()}
}

type Notification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

func readMessage(r *bufio.Reader) ([]byte, error) {
	var contentLength int
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			break
		}
		const prefix = "Content-Length: "
		if after, ok := strings.CutPrefix(line, prefix); ok {
			val := after
			cl, err := strconv.Atoi(val)
			if err != nil {
				return nil, fmt.Errorf("invalid Content-Length: %w", err)
			}
			if cl > maxJSONRPCBodySize {
				return nil, fmt.Errorf("Content-Length %d exceeds %d-byte limit", cl, maxJSONRPCBodySize)
			}
			contentLength = cl
		}
	}
	if contentLength <= 0 {
		return nil, fmt.Errorf("missing or invalid Content-Length")
	}
	buf := make([]byte, contentLength)
	_, err := io.ReadFull(r, buf)
	if err != nil {
		return nil, err
	}
	return buf, nil
}

func writeMessage(w io.Writer, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(data))
	if _, err := io.WriteString(w, header); err != nil {
		return err
	}
	if _, err := w.Write(data); err != nil {
		return err
	}
	return nil
}
