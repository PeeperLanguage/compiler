package diagnostics

import (
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
	case Error: return "error"
	case Warning: return "warning"
	case Info: return "info"
	case Hint: return "hint"
	default: return "unknown"
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
	ExtraCodeHint
)

type DiagnosticText struct {
	Kind    string
	Message string
	Color   colors.COLOR
}

type DiagnosticExtra struct {
	Kind     DiagnosticExtraKind
	Text     DiagnosticText
	CodeHint CodeHint
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

type CodeHintLine struct {
	Prefix    string
	Code      string
	BaseColor colors.COLOR
}

type CodeHint struct {
	Code        string
	Lines       []CodeHintLine
	Labels      []CodeHintLabel
	Location    *source.Location
	BaseColor   colors.COLOR
	GutterColor colors.COLOR
}

type CodeHintLabel struct {
	Line    int
	Column  int
	Length  int
	Message string
	Style   LabelStyle
}

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

// WithPrimaryLabel is intentionally unexported. Primary labels should be 
// attached via bag.AddError or bag.AddWarning, not chained arbitrarily.
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

func (d *Diagnostic) WithCodeInsertion(loc *source.Location, code string, labels ...CodeHintLabel) *Diagnostic {
	return d.WithCodeHintLines(loc, []CodeHintLine{
		{Prefix: "+", Code: code, BaseColor: colors.GREEN},
	}, labels...)
}

func (d *Diagnostic) WithCodeRemoval(loc *source.Location, code string, labels ...CodeHintLabel) *Diagnostic {
	return d.WithCodeHintLines(loc, []CodeHintLine{
		{Prefix: "-", Code: code, BaseColor: colors.RED},
	}, labels...)
}

func (d *Diagnostic) WithCodeReplacement(loc *source.Location, oldCode, newCode string, labels ...CodeHintLabel) *Diagnostic {
	return d.WithCodeHintLines(loc, []CodeHintLine{
		{Prefix: "-", Code: oldCode, BaseColor: colors.RED},
		{Prefix: "+", Code: newCode, BaseColor: colors.GREEN},
	}, labels...)
}

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

func (d *Diagnostic) WithNote(message string) *Diagnostic {
	return d.WithText("note", message, colors.CYAN)
}

func (d *Diagnostic) WithHelp(help string) *Diagnostic {
	return d.WithText("help", help, colors.GREEN)
}