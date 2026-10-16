// Package diag defines compiler diagnostics and prints them. Each compiler step
// returns the diagnostics it finds together with its result; nothing collects
// them in a shared place.
package diag

import "compiler/internal/source"

// Diagnostic is one message about the source.
type Diagnostic struct {
	Code    Code
	Message string
	Labels  []Label // the first is the primary place
	Notes   []Note
}

// Label marks a span of a file with a short message.
type Label struct {
	File    *source.File
	Span    source.Span
	Message string
}

// Note is a note or help line. A help line may carry fixes; all of them are
// applied together, and they make exactly the text the line suggests.
type Note struct {
	Help  bool
	Text  string
	Fixes []Fix
}

// Fix replaces a span of a file with new text: an empty span inserts, empty
// text removes.
type Fix struct {
	File    *source.File
	Span    source.Span
	NewText string
}

type Severity uint8

const (
	Error Severity = iota
	Warning
	Info
)

// Code identifies a kind of diagnostic. Each code is declared once, below,
// together with its severity, so changing an ID cannot change a severity.
type Code struct {
	ID       string
	Severity Severity
}

// New starts a diagnostic; add its places with Label.
func New(code Code, message string) *Diagnostic {
	return &Diagnostic{Code: code, Message: message}
}

// Label adds a place; the first place added is the primary one.
func (d *Diagnostic) Label(file *source.File, span source.Span, message string) *Diagnostic {
	d.Labels = append(d.Labels, Label{File: file, Span: span, Message: message})
	return d
}

func (d *Diagnostic) Note(text string) *Diagnostic {
	d.Notes = append(d.Notes, Note{Text: text})
	return d
}

func (d *Diagnostic) Help(text string, fixes ...Fix) *Diagnostic {
	d.Notes = append(d.Notes, Note{Help: true, Text: text, Fixes: fixes})
	return d
}

// Codes.
var (
	UnexpectedCharacter = Code{"L0001", Error}
	UnterminatedString  = Code{"L0002", Error}
	UnterminatedComment = Code{"L0005", Error}
	ExpectedToken       = Code{"P0002", Error}
	InvalidDeclaration  = Code{"P0005", Error}

	ModuleNotFound         = Code{"M0001", Error}
	CyclicImport           = Code{"M0002", Error}
	InvalidImportPath      = Code{"M0003", Error}
	AliasInUse             = Code{"M0005", Error}
	RedundantPreludeImport = Code{"S0004", Info}
)
