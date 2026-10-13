package thir

import (
	"fmt"
	"strconv"
	"strings"

	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
)

// Text renders the module for compiler dumps. Types use
// typeinfo.QualifiedTypeText, the same names the MIR and LLVM dumps use.
func (m *Module) Text() string {
	if m == nil {
		return ""
	}
	p := textPrinter{}
	p.b.WriteString("; thir module ")
	p.b.WriteString(m.Name)
	p.b.WriteString("\n")
	for _, function := range m.Functions {
		if function != nil {
			p.function(function)
		}
	}
	return p.b.String()
}

type textPrinter struct {
	b      strings.Builder
	indent int
}

func (p *textPrinter) function(function *Function) {
	p.b.WriteString("\nfn ")
	p.b.WriteString(function.Name)
	p.b.WriteString("(")
	for index, param := range function.Params {
		if index > 0 {
			p.b.WriteString(", ")
		}
		p.b.WriteString(symbolName(param.Symbol))
		p.b.WriteString(": ")
		p.b.WriteString(typeinfo.QualifiedTypeText(param.Type()))
	}
	p.b.WriteString(")")
	if function.HasReturnValue {
		p.b.WriteString(" -> ")
		p.b.WriteString(typeinfo.QualifiedTypeText(function.ReturnType))
	}
	if function.Body == nil {
		p.b.WriteString("\n")
		return
	}
	p.b.WriteString(" ")
	p.block(function.Body)
	p.b.WriteString("\n")
}

// block writes "{", the statements one level deeper, and "}" without a
// trailing newline, so callers can continue the line with "else".
func (p *textPrinter) block(block *Block) {
	p.b.WriteString("{\n")
	p.indent++
	if block != nil {
		for _, stmt := range block.Stmts {
			p.stmt(stmt)
		}
	}
	p.indent--
	p.line()
	p.b.WriteString("}")
}

func (p *textPrinter) line() {
	p.b.WriteString(strings.Repeat("  ", p.indent))
}

func (p *textPrinter) stmt(stmt Stmt) {
	if block, ok := stmt.(*Block); ok {
		p.line()
		p.block(block)
		p.b.WriteString("\n")
		return
	}
	p.line()
	switch s := stmt.(type) {
	case *Binding:
		keyword := "let "
		if s.IsConstant {
			keyword = "const "
		}
		p.b.WriteString(keyword)
		p.b.WriteString(symbolName(s.Symbol))
		if s.Symbol != nil {
			p.b.WriteString(": ")
			p.b.WriteString(typeinfo.QualifiedTypeText(s.Symbol.Type))
		}
		if s.Value != nil {
			p.b.WriteString(" = ")
			p.b.WriteString(p.expr(s.Value))
		}
	case *Destructure:
		strct, _ := typeinfo.Underlying(s.Value.ExprType()).(*typeinfo.StructType)
		fields := make([]string, len(s.Bindings))
		for index, binding := range s.Bindings {
			fields[index] = strct.Fields[binding.Field].Name + " as " + symbolName(binding.Symbol) + ": " + typeinfo.QualifiedTypeText(binding.Type)
		}
		p.b.WriteString("let .{" + strings.Join(fields, ", ") + "} = " + p.expr(s.Value))
	case *ExprStmt:
		p.b.WriteString(p.expr(s.Value))
	case *Assign:
		p.b.WriteString(p.expr(s.Target))
		p.b.WriteString(" = ")
		p.b.WriteString(p.expr(s.Value))
	case *Return:
		p.b.WriteString("return")
		if s.Value != nil {
			p.b.WriteString(" ")
			p.b.WriteString(p.expr(s.Value))
		}
	case *If:
		p.ifChain(s)
	case *For:
		p.b.WriteString("for ")
		if s.Checked != nil {
			// Checked iteration is published as its generated expansion.
			p.b.WriteString("checked ")
			p.block(s.Checked)
			break
		}
		if s.Index != nil {
			p.b.WriteString(symbolName(s.Index))
			p.b.WriteString(", ")
		}
		if s.Value != nil {
			p.b.WriteString(symbolName(s.Value))
			p.b.WriteString(" in ")
		}
		if s.Iterable != nil {
			p.b.WriteString(p.expr(s.Iterable))
			p.b.WriteString(" ")
		}
		if s.Condition != nil {
			p.b.WriteString(p.expr(s.Condition))
			p.b.WriteString(" ")
		}
		p.block(s.Body)
	case *Break:
		p.b.WriteString("break")
	case *Continue:
		p.b.WriteString("continue")
	case *Match:
		p.match(s)
	case *InvalidStmt:
		p.b.WriteString("invalid ")
		p.b.WriteString(strconv.Quote(s.Message))
	default:
		panic(fmt.Sprintf("unhandled THIR statement %T", stmt))
	}
	p.b.WriteString("\n")
}

func (p *textPrinter) ifChain(s *If) {
	p.b.WriteString("if ")
	p.b.WriteString(p.expr(s.Condition))
	p.b.WriteString(" ")
	p.block(s.Then)
	switch elseStmt := s.Else.(type) {
	case nil:
	case *If:
		p.b.WriteString(" else ")
		p.ifChain(elseStmt)
	case *Block:
		p.b.WriteString(" else ")
		p.block(elseStmt)
	default:
		panic(fmt.Sprintf("unhandled THIR else statement %T", s.Else))
	}
}

func (p *textPrinter) match(s *Match) {
	p.b.WriteString("match ")
	p.b.WriteString(p.expr(s.Subject))
	p.b.WriteString(" {\n")
	p.indent++
	for _, arm := range s.Arms {
		p.line()
		for index, armCase := range arm.Cases {
			if index > 0 {
				p.b.WriteString(", ")
			}
			p.b.WriteString(caseName(s.EnumType, armCase.Case))
			if armCase.TestsPayload {
				descriptor, _ := typeinfo.VariantDescriptorOf(s.EnumType)
				p.b.WriteString(" is " + caseName(descriptor.Cases[armCase.Case].Payload, armCase.PayloadCase))
			}
		}
		p.b.WriteString(" => ")
		p.block(arm.Body)
		p.b.WriteString("\n")
	}
	p.indent--
	p.line()
	p.b.WriteString("}")
}

func (p *textPrinter) expr(expr Expr) string {
	switch e := expr.(type) {
	case nil:
		return "<nil>"
	case *InvalidExpr:
		return "invalid " + strconv.Quote(e.Message)
	case *NumberLiteral:
		return e.Value + e.ExplicitType
	case *StringLiteral:
		if e.IsCString {
			return "c" + strconv.Quote(e.Value)
		}
		return strconv.Quote(e.Value)
	case *ByteLiteral:
		return e.Value
	case *CharLiteral:
		return e.Value
	case *BoolLiteral:
		return strconv.FormatBool(e.Value)
	case *NoneLiteral:
		return "none"
	case *Ident:
		return e.Name
	case *QualifiedIdent:
		return e.Name
	case *Field:
		return p.expr(e.Base) + "." + e.Name
	case *Index:
		return p.expr(e.Base) + "[" + p.expr(e.Index) + "]"
	case *Range:
		operator := "..="
		if e.IsEndExclusive {
			operator = ".."
		}
		return p.expr(e.Start) + operator + p.expr(e.End)
	case *StructLiteral:
		fields := make([]string, len(e.Fields))
		for index, field := range e.Fields {
			fields[index] = field.Name + ": " + p.expr(field.Value)
		}
		return typeinfo.QualifiedTypeText(e.Type) + "{" + strings.Join(fields, ", ") + "}"
	case *Variant:
		text := typeinfo.QualifiedTypeText(e.Type) + "." + caseName(e.Type, e.Case)
		if e.Payload != nil {
			text += "(" + p.expr(e.Payload) + ")"
		}
		return text
	case *ArrayLiteral:
		return "[" + p.exprList(e.Values) + "]"
	case *Address:
		switch e.Mode {
		case AddressRaw:
			return "@" + p.expr(e.Value)
		case AddressShared:
			return "&" + p.expr(e.Value)
		case AddressMutable:
			return "&mut " + p.expr(e.Value)
		default:
			panic(fmt.Sprintf("unhandled THIR address mode %d", e.Mode))
		}
	case *Unary:
		return e.Op + p.expr(e.Value)
	case *Binary:
		return "(" + p.expr(e.Left) + " " + e.Op + " " + p.expr(e.Right) + ")"
	case *Is:
		return p.expr(e.Value) + " is " + caseName(e.Value.ExprType(), e.Test.Case)
	case *Call:
		text := p.expr(e.Callee) + "(" + p.exprList(e.Args) + ")"
		if result := typeinfo.QualifiedTypeText(e.Type); result != "" {
			text += ": " + result
		}
		return text
	case *Free:
		return "free(" + p.expr(e.Value) + ")"
	case *Print:
		if e.AppendsNewline {
			return "println(" + p.expr(e.Value) + ")"
		}
		return "print(" + p.expr(e.Value) + ")"
	case *Cast:
		return "(" + p.expr(e.Value) + " as " + typeinfo.QualifiedTypeText(e.Type) + ")"
	default:
		panic(fmt.Sprintf("unhandled THIR expression %T", expr))
	}
}

func (p *textPrinter) exprList(exprs []Expr) string {
	parts := make([]string, len(exprs))
	for index, expr := range exprs {
		parts[index] = p.expr(expr)
	}
	return strings.Join(parts, ", ")
}

func symbolName(symbol *symbols.Symbol) string {
	if symbol == nil {
		return "_"
	}
	return symbol.Name
}

// caseName names a variant case of typ, falling back to its index when typ
// carries no case list.
func caseName(typ typeinfo.Type, index int) string {
	if descriptor, ok := typeinfo.VariantDescriptorOf(typ); ok && index >= 0 && index < len(descriptor.Cases) {
		return descriptor.Cases[index].Name
	}
	return "case#" + strconv.Itoa(index)
}
