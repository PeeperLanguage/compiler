// Package diag defines compiler diagnostics. Each compiler step returns the
// diagnostics it finds together with its result; nothing collects them in a
// shared place.
package diag

import "compiler/internal/source"

type Severity uint8

const (
	Error Severity = iota
	Warning
	Info
)

// Diagnostic is one message about a module's source. Its spans refer to that
// module's file.
type Diagnostic struct {
	Severity Severity
	Code     string
	Message  string
	Labels   []Label // the first label marks the primary place
}

// Label marks a span with a short message.
type Label struct {
	Span    source.Span
	Message string
}

// Codes.
const (
	UnexpectedCharacter = "L0001"
)
