package hir

import (
	"strings"

	"compiler/internal/ir"
	"compiler/internal/source"
)

type Module struct {
	Name    string
	Externs []Extern
	Funcs   []*Function
}

type Extern struct {
	Name       string
	Params     []ir.Param
	ReturnType string
}

type Function struct {
	Name       string
	Params     []ir.Param
	ReturnType string
	Body       *Block
	Location   *source.Location
}

type Stmt interface {
	stmtNode()
	appendText(*strings.Builder, int)
	appendInlineText(*strings.Builder, int)
}

type Block struct {
	Stmts    []Stmt
	Location *source.Location
}

type Binding struct {
	Name     string
	Constant bool
	Value    ir.Expr
	Location *source.Location
}

type ExprStmt struct {
	Value    ir.Expr
	Location *source.Location
}

type Assign struct {
	Target   ir.Expr
	Value    ir.Expr
	Location *source.Location
}

type Invalid struct {
	Message  string
	Location *source.Location
}

type Return struct {
	Value    ir.Expr
	Location *source.Location
}

type If struct {
	Cond     ir.Expr
	Then     *Block
	Else     Stmt
	Location *source.Location
}

func (*Block) stmtNode()    {}
func (*Binding) stmtNode()  {}
func (*ExprStmt) stmtNode() {}
func (*Assign) stmtNode()   {}
func (*Invalid) stmtNode()  {}
func (*Return) stmtNode()   {}
func (*If) stmtNode()       {}

func (m *Module) Text() string {
	if m == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("; hir module ")
	b.WriteString(m.Name)
	b.WriteString("\n")
	for _, ex := range m.Externs {
		b.WriteString("extern fn ")
		b.WriteString(ex.Name)
		b.WriteString(ir.SignatureText(ex.Params, ex.ReturnType))
		b.WriteString("\n")
	}
	if len(m.Externs) > 0 {
		b.WriteString("\n")
	}
	if len(m.Funcs) == 0 {
		return b.String()
	}
	for _, fn := range m.Funcs {
		b.WriteString("fn ")
		b.WriteString(fn.Name)
		b.WriteString(ir.SignatureText(fn.Params, fn.ReturnType))
		b.WriteString(" {\n")
		appendBlockText(&b, fn.Body, 1)
		b.WriteString("}\n")
	}
	return b.String()
}

func appendBlockText(b *strings.Builder, block *Block, indent int) {
	if b == nil || block == nil {
		return
	}
	for _, stmt := range block.Stmts {
		if stmt == nil {
			continue
		}
		stmt.appendText(b, indent)
	}
}

func writeIndent(b *strings.Builder, indent int) {
	for range indent {
		b.WriteString("  ")
	}
}

func (s *Block) appendText(b *strings.Builder, indent int) {
	writeIndent(b, indent)
	b.WriteString("{\n")
	appendBlockText(b, s, indent+1)
	writeIndent(b, indent)
	b.WriteString("}\n")
}

func (s *Block) appendInlineText(b *strings.Builder, indent int) {
	b.WriteString("{\n")
	appendBlockText(b, s, indent+1)
	writeIndent(b, indent)
	b.WriteString("}\n")
}

func (s *Binding) appendText(b *strings.Builder, indent int) {
	writeIndent(b, indent)
	if s.Constant {
		b.WriteString("const ")
	} else {
		b.WriteString("let ")
	}
	b.WriteString(s.Name)
	b.WriteString(" = ")
	b.WriteString(s.Value.String())
	b.WriteString("\n")
}

func (s *Binding) appendInlineText(b *strings.Builder, indent int) {
	s.appendText(b, indent)
}

func (s *ExprStmt) appendText(b *strings.Builder, indent int) {
	writeIndent(b, indent)
	b.WriteString(s.Value.String())
	b.WriteString("\n")
}

func (s *ExprStmt) appendInlineText(b *strings.Builder, indent int) {
	s.appendText(b, indent)
}

func (s *Assign) appendText(b *strings.Builder, indent int) {
	writeIndent(b, indent)
	b.WriteString(s.Target.String())
	b.WriteString(" = ")
	b.WriteString(s.Value.String())
	b.WriteString("\n")
}

func (s *Assign) appendInlineText(b *strings.Builder, indent int) {
	s.appendText(b, indent)
}

func (s *Invalid) appendText(b *strings.Builder, indent int) {
	writeIndent(b, indent)
	b.WriteString("invalid")
	if s != nil && s.Message != "" {
		b.WriteString(" ")
		b.WriteString(s.Message)
	}
	b.WriteString("\n")
}

func (s *Invalid) appendInlineText(b *strings.Builder, indent int) {
	s.appendText(b, indent)
}

func (s *Return) appendText(b *strings.Builder, indent int) {
	writeIndent(b, indent)
	b.WriteString("return ")
	b.WriteString(s.Value.String())
	b.WriteString("\n")
}

func (s *Return) appendInlineText(b *strings.Builder, indent int) {
	s.appendText(b, indent)
}

func (s *If) appendText(b *strings.Builder, indent int) {
	writeIndent(b, indent)
	s.appendInlineText(b, indent)
}

func (s *If) appendInlineText(b *strings.Builder, indent int) {
	b.WriteString("if ")
	b.WriteString(s.Cond.String())
	b.WriteString(" {\n")
	appendBlockText(b, s.Then, indent+1)
	writeIndent(b, indent)
	b.WriteString("}")
	if s.Else == nil {
		b.WriteString("\n")
		return
	}
	b.WriteString(" else ")
	s.Else.appendInlineText(b, indent)
}
