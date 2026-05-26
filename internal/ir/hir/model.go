package hir

import (
	"strings"

	"compiler/internal/ir"
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
	Bindings   []Binding
	Returns    []ir.Expr
}

type Binding struct {
	Name  string
	Value ir.Expr
}

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
		for _, bind := range fn.Bindings {
			b.WriteString("  let ")
			b.WriteString(bind.Name)
			b.WriteString(" = ")
			b.WriteString(bind.Value.String())
			b.WriteString("\n")
		}
		for _, ret := range fn.Returns {
			b.WriteString("  return ")
			b.WriteString(ret.String())
			b.WriteString("\n")
		}
		b.WriteString("}\n")
	}
	return b.String()
}
