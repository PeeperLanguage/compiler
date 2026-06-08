package diagnostics

import (
	"compiler/internal/source"
	"compiler/pkg/colors"
)

// Severity represents the severity level of a diagnostic
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

// Label represents a labeled section of code in a diagnostic
type Label struct {
	Location *source.Location
	Message  string
	Style    LabelStyle
}

type LabelStyle int

const (
	Primary   LabelStyle = iota // The main error location (uses ~~~)
	Secondary                   // Additional context (uses ---)
)


type DiagnosticExtraKind int

const (
	ExtraText DiagnosticExtraKind = iota
	ExtraCodeHint
)

// DiagnosticText represents an ordered text entry (e.g. help/note) rendered with custom color.
type DiagnosticText struct {
	Kind    string
	Message string
	Color   colors.COLOR
}

// DiagnosticExtra preserves user-facing diagnostic output order across text and code hints.
type DiagnosticExtra struct {
	Kind     DiagnosticExtraKind
	Text     DiagnosticText
	CodeHint CodeHint
}

// Diagnostic represents a compiler diagnostic (error, warning, etc.)
type Diagnostic struct {
	Severity  Severity
	Message   string
	Code      string // Error code like "E0001"
	FilePath  string // Source file for this diagnostic
	Labels    []Label
	Extras    []DiagnosticExtra
}

const internalCompilerErrorCode = "ICE0001"

// CodeHintLine represents one rendered hint line with an optional diff prefix.
// Prefix supports:
//   - "+" for inserted code
//   - "-" for removed code
//   - " " or "" for neutral/context lines
type CodeHintLine struct {
	Prefix    string
	Code      string
	BaseColor colors.COLOR
}

// CodeHint renders extra lines after the primary label.
type CodeHint struct {
	Code        string
	Lines       []CodeHintLine
	Labels      []CodeHintLabel
	Location    *source.Location
	BaseColor   colors.COLOR
	GutterColor colors.COLOR
}

// CodeHintLabel represents a label within a code hint snippet.
type CodeHintLabel struct {
	Line    int
	Column  int
	Length  int
	Message string
	Style   LabelStyle
}

func newDiagnostic(sev Severity, message string) *Diagnostic {
	return &Diagnostic{Severity: sev, Message: message}
}

func NewError(message string) *Diagnostic   { return newDiagnostic(Error, message) }
func NewWarning(message string) *Diagnostic { return newDiagnostic(Warning, message) }
func NewInfo(message string) *Diagnostic    { return newDiagnostic(Info, message) }

// WithCode sets the error code
func (d *Diagnostic) WithCode(code string) *Diagnostic {
	d.Code = code
	return d
}

// WithLabel adds a labeled location to the diagnostic
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

// setFilePath sets FilePath from loc if not already set.
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

// WithPrimaryLabel adds a primary labeled location
// Must be called before any WithSecondaryLabel calls
func (d *Diagnostic) WithPrimaryLabel(loc *source.Location, message string) *Diagnostic {
	if loc == nil {
		return d
	}
	// Ensure primary label is always first
	if len(d.Labels) > 0 {
		// Already have a primary — don't add another
		if d.Labels[0].Style == Primary {
			return d
		}
		// Secondary labels exist but no primary — insert at beginning
		d.Labels = append([]Label{{Location: loc, Message: message, Style: Primary}}, d.Labels...)
		d.setFilePath(loc)
		return d
	}
	return d.WithLabel(loc, message, Primary)
}

// WithSecondaryLabel adds a secondary labeled location
// Can be called multiple times to add multiple context labels
// Primary label must exist before adding secondary labels
func (d *Diagnostic) WithSecondaryLabel(loc *source.Location, message string) *Diagnostic {
	// Primary is always at index 0 per the invariant enforced by WithPrimaryLabel
	if len(d.Labels) == 0 || d.Labels[0].Style != Primary {
		d.markInternalCompilerError("secondary label added without primary label; inserted fallback primary label")
		d.WithPrimaryLabel(loc, "internal compiler error: missing primary label")
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

// WithCodeHint adds a primary label and attaches a code hint to display.
func (d *Diagnostic) WithCodeHint(loc *source.Location, code string, labels ...CodeHintLabel) *Diagnostic {
	if loc == nil {
		return d
	}
	d.WithPrimaryLabel(loc, "")
	d.Extras = append(d.Extras, DiagnosticExtra{
		Kind: ExtraCodeHint,
		CodeHint: CodeHint{
			Code:        code,
			Labels:      labels,
			Location:    loc,
			GutterColor: colors.GREEN,
		},
	})
	return d
}

// WithCodeHintLines adds a diff-style code hint snippet with explicit line prefixes.
func (d *Diagnostic) WithCodeHintLines(loc *source.Location, lines []CodeHintLine, labels ...CodeHintLabel) *Diagnostic {
	if loc == nil {
		return d
	}
	d.WithPrimaryLabel(loc, "")
	d.Extras = append(d.Extras, DiagnosticExtra{
		Kind: ExtraCodeHint,
		CodeHint: CodeHint{
			Lines:       append([]CodeHintLine(nil), lines...),
			Labels:      labels,
			Location:    loc,
			GutterColor: colors.GREEN,
		},
	})
	return d
}

// WithCodeInsertion adds a one-line insertion hint (green '+' line).
func (d *Diagnostic) WithCodeInsertion(loc *source.Location, code string, labels ...CodeHintLabel) *Diagnostic {
	return d.WithCodeHintLines(loc, []CodeHintLine{
		{Prefix: "+", Code: code, BaseColor: colors.GREEN},
	}, labels...)
}

// WithCodeRemoval adds a one-line removal hint (red '-' line).
func (d *Diagnostic) WithCodeRemoval(loc *source.Location, code string, labels ...CodeHintLabel) *Diagnostic {
	return d.WithCodeHintLines(loc, []CodeHintLine{
		{Prefix: "-", Code: code, BaseColor: colors.RED},
	}, labels...)
}

// WithCodeReplacement adds a two-line replacement hint (red '-' then green '+').
func (d *Diagnostic) WithCodeReplacement(loc *source.Location, oldCode, newCode string, labels ...CodeHintLabel) *Diagnostic {
	return d.WithCodeHintLines(loc, []CodeHintLine{
		{Prefix: "-", Code: oldCode, BaseColor: colors.RED},
		{Prefix: "+", Code: newCode, BaseColor: colors.GREEN},
	}, labels...)
}

// WithText appends an ordered diagnostic text entry.
// kind controls the label after '=' (e.g. "help", "note", "suggestion").
func (d *Diagnostic) WithText(kind, message string, color colors.COLOR) *Diagnostic {
	if message == "" {
		return d
	}
	if color == "" {
		color = colors.WHITE
	}
	d.Extras = append(d.Extras, DiagnosticExtra{
		Kind: ExtraText,
		Text: DiagnosticText{Kind: kind, Message: message, Color: color},
	})
	return d
}

// WithNote adds a note to the diagnostic
func (d *Diagnostic) WithNote(message string) *Diagnostic {
	return d.WithText("note", message, colors.CYAN)
}

// WithHelp adds a help suggestion to the diagnostic
func (d *Diagnostic) WithHelp(help string) *Diagnostic {
	return d.WithText("help", help, colors.GREEN)
}
