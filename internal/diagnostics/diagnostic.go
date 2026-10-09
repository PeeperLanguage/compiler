package diagnostics

import (
	"strings"

	"compiler/internal/source"
	"compiler/pkg/colors"
)

type Severity int

const (
	Error Severity = iota
	Warning
	Info
	Hint
)

func (s Severity) String() string {
	switch s {
	case Error:
		return "error"
	case Warning:
		return "warning"
	case Info:
		return "info"
	case Hint:
		return "hint"
	default:
		return "unknown"
	}
}

type Label struct {
	Location *source.Location
	Message  string
	Style    LabelStyle
}

type LabelStyle int

const (
	Primary LabelStyle = iota
	Secondary
)

type DiagnosticExtraKind int

const (
	ExtraText DiagnosticExtraKind = iota
)

type DiagnosticText struct {
	Kind    string
	Message string
	Color   colors.COLOR
	// Fixes are the source edits this text proposes, shown beneath it.
	Fixes []CodeFix
}

// CodeFix replaces the source at Location with NewText. An empty Location
// inserts; an empty NewText removes. Build one with Fix.Remove, Fix.Insert or
// Fix.Replace.
type CodeFix struct {
	Location *source.Location
	NewText  string
}

// Fix groups the ways to build a CodeFix, so a call site reads as the edit it
// proposes: Fix.Remove(where), Fix.Insert(where, text), Fix.Replace(where, text).
var Fix fixBuilder

type fixBuilder struct{}

// Remove deletes the source at where.
func (fixBuilder) Remove(where *source.Location) CodeFix {
	return CodeFix{Location: where}
}

// Insert adds text at the start of where and keeps everything already there.
func (fixBuilder) Insert(where *source.Location, text string) CodeFix {
	if where == nil {
		return CodeFix{NewText: text}
	}
	point := *where
	point.End = where.Start
	return CodeFix{Location: &point, NewText: text}
}

// Replace swaps the source at where for newText.
func (fixBuilder) Replace(where *source.Location, newText string) CodeFix {
	return CodeFix{Location: where, NewText: newText}
}

type DiagnosticExtra struct {
	Kind DiagnosticExtraKind
	Text DiagnosticText
}

type Diagnostic struct {
	Severity Severity
	Message  string
	Code     string
	FilePath string
	Labels   []Label
	Extras   []DiagnosticExtra
}

const internalCompilerErrorCode = "ICE0001"

func NewError(message string) *Diagnostic {
	return &Diagnostic{Severity: Error, Message: message}
}

func NewWarning(message string) *Diagnostic {
	return &Diagnostic{Severity: Warning, Message: message}
}

func NewInfo(message string) *Diagnostic {
	return &Diagnostic{Severity: Info, Message: message}
}

func (d *Diagnostic) WithCode(code string) *Diagnostic {
	d.Code = code
	return d
}

func (d *Diagnostic) WithLabel(loc *source.Location, message string, style LabelStyle) *Diagnostic {
	if loc == nil {
		return d
	}
	d.setFilePath(loc)
	d.Labels = append(d.Labels, Label{
		Location: loc,
		Message:  message,
		Style:    style,
	})
	return d
}

func (d *Diagnostic) setFilePath(loc *source.Location) {
	if d.FilePath == "" && loc.Filename != nil {
		d.FilePath = *loc.Filename
	}
}

func (d *Diagnostic) At(loc *source.Location) *Diagnostic {
	if loc == nil {
		return d
	}
	d.FilePath = *loc.Filename
	return d
}

// WithPrimaryLabel attaches the primary source location to this diagnostic.
// A diagnostic can have at most one primary label; subsequent calls overwrite.
func (d *Diagnostic) WithPrimaryLabel(loc *source.Location, message string) *Diagnostic {
	if loc == nil {
		return d
	}
	// A diagnostic can only have ONE origin. If it exists, overwrite it safely.
	if len(d.Labels) > 0 && d.Labels[0].Style == Primary {
		d.Labels[0] = Label{Location: loc, Message: message, Style: Primary}
		d.setFilePath(loc)
		return d
	}
	// Otherwise, prepend it
	d.Labels = append([]Label{{Location: loc, Message: message, Style: Primary}}, d.Labels...)
	d.setFilePath(loc)
	return d
}

// WithSecondaryLabel attaches historical or contextual locations to the diagnostic.
func (d *Diagnostic) WithSecondaryLabel(loc *source.Location, message string) *Diagnostic {
	if len(d.Labels) == 0 || d.Labels[0].Style != Primary {
		d.markInternalCompilerError("secondary label added without primary label")
		d.WithPrimaryLabel(loc, "missing primary label context")
	}
	return d.WithLabel(loc, message, Secondary)
}

func (d *Diagnostic) markInternalCompilerError(message string) *Diagnostic {
	if d == nil {
		return d
	}
	d.Severity = Error
	if d.Code == "" {
		d.Code = internalCompilerErrorCode
	}
	if message != "" {
		d.WithText("internal", message, colors.RED)
	}
	return d
}

// WithText adds a `= kind: message` line. Fixes are the source edits the
// message proposes; each fixed line is shown beneath it.
func (d *Diagnostic) WithText(kind, message string, color colors.COLOR, fixes ...CodeFix) *Diagnostic {
	if message == "" {
		return d
	}
	if color == "" {
		color = colors.WHITE
	}
	d.Extras = append(d.Extras, DiagnosticExtra{
		Kind: ExtraText,
		Text: DiagnosticText{Kind: kind, Message: message, Color: color, Fixes: fixes},
	})
	return d
}

func (d *Diagnostic) Note(message string, fixes ...CodeFix) *Diagnostic {
	return d.WithText("note", message, colors.CYAN, fixes...)
}

func (d *Diagnostic) Help(help string, fixes ...CodeFix) *Diagnostic {
	return d.WithText("help", help, colors.GREEN, fixes...)
}

// Choice is one of several texts that would each repair the code when
// inserted at the same place, and the condition that makes it the right one.
type Choice struct {
	If     string
	Insert string
}

// HelpWithChoices adds a help line for each choice, with the line as that
// choice would leave it. It is for code that is wrong where the author's
// intent cannot be told, so no single fix may be offered as the answer.
// Insert may carry the spacing the insertion needs; the help names it bare.
func (d *Diagnostic) HelpWithChoices(at *source.Location, choices ...Choice) *Diagnostic {
	for _, choice := range choices {
		d.Help("if "+choice.If+", add `"+strings.TrimSpace(choice.Insert)+"`", Fix.Insert(at, choice.Insert))
	}
	return d
}

const internalCompilerFailureMessage = "internal compiler failure"

type PresentationOptions struct {
	ShowInternalErrors bool
}

// ForPresentation returns the diagnostic as it should be shown to a user.
// Internal diagnostics retain their complete compiler detail in storage; the
// default presentation only hides that detail at the output boundary.
func ForPresentation(diag *Diagnostic, options PresentationOptions) *Diagnostic {
	if diag == nil || options.ShowInternalErrors || !strings.HasPrefix(diag.Code, "ICE") {
		return diag
	}
	presented := *diag
	presented.Message = internalCompilerFailureMessage
	presented.Labels = nil
	presented.Extras = []DiagnosticExtra{{
		Kind: ExtraText,
		Text: DiagnosticText{
			Kind:    "help",
			Message: "rerun with --show-internal-errors to include compiler-internal details when reporting this issue",
			Color:   colors.GREEN,
		},
	}}
	return &presented
}
