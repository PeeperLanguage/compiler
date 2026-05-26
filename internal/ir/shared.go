package ir

import (
	"fmt"
	"strings"

	"compiler/internal/frontend/ast"
)

type Param struct {
	Name string
	Type string
}

type Module interface {
	Text() string
}

type Slots struct {
	HIR Module
	MIR Module
}

type Expr interface {
	exprNode()
	String() string
}

type IntLit struct {
	Value int32
}

type Ident struct {
	Name string
}

type Unary struct {
	Op  string
	Arg Expr
}

type Binary struct {
	Op    string
	Left  Expr
	Right Expr
}

func (*IntLit) exprNode() {}
func (*Ident) exprNode()  {}
func (*Unary) exprNode()  {}
func (*Binary) exprNode() {}

func (e *IntLit) String() string { return fmt.Sprintf("%d", e.Value) }
func (e *Ident) String() string  { return e.Name }
func (e *Unary) String() string  { return fmt.Sprintf("(%s %s)", e.Op, e.Arg.String()) }
func (e *Binary) String() string {
	return fmt.Sprintf("(%s %s %s)", e.Op, e.Left.String(), e.Right.String())
}

func TypeText(typ ast.TypeExpr) string {
	switch node := typ.(type) {
	case nil:
		return ""
	case *ast.NamedType:
		return node.Name
	case *ast.FuncType:
		var b strings.Builder
		b.WriteString("fn(")
		for i, param := range node.Params {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(TypeText(param))
		}
		b.WriteString(")")
		if ret := TypeText(node.Return); ret != "" {
			b.WriteString(" -> ")
			b.WriteString(ret)
		}
		return b.String()
	case *ast.StructType:
		var b strings.Builder
		b.WriteString("struct {")
		for i, field := range node.Fields {
			if i > 0 {
				b.WriteString(", ")
			}
			if field.Name != nil {
				b.WriteString(field.Name.Name)
				b.WriteString(": ")
			}
			b.WriteString(TypeText(field.Type))
		}
		b.WriteString("}")
		return b.String()
	case *ast.InterfaceType:
		var b strings.Builder
		b.WriteString("interface {")
		for i, method := range node.Methods {
			if i > 0 {
				b.WriteString(", ")
			}
			if method.Name != nil {
				b.WriteString(method.Name.Name)
			}
			b.WriteString("(")
			for j, param := range method.Params {
				if j > 0 {
					b.WriteString(", ")
				}
				if param.Name != nil {
					b.WriteString(param.Name.Name)
					b.WriteString(": ")
				}
				b.WriteString(TypeText(param.Type))
			}
			b.WriteString(")")
			if ret := TypeText(method.ReturnType); ret != "" {
				b.WriteString(" -> ")
				b.WriteString(ret)
			}
		}
		b.WriteString("}")
		return b.String()
	case *ast.EnumType:
		var b strings.Builder
		b.WriteString("enum {")
		for i, variant := range node.Variants {
			if i > 0 {
				b.WriteString(", ")
			}
			if variant.Name != nil {
				b.WriteString(variant.Name.Name)
			}
		}
		b.WriteString("}")
		return b.String()
	default:
		return ""
	}
}

func SignatureText(params []Param, returnType string) string {
	var b strings.Builder
	b.WriteString("(")
	for i, param := range params {
		if i > 0 {
			b.WriteString(", ")
		}
		if param.Name != "" {
			b.WriteString(param.Name)
			if param.Type != "" {
				b.WriteString(": ")
			}
		}
		b.WriteString(param.Type)
	}
	b.WriteString(")")
	if returnType != "" {
		b.WriteString(" -> ")
		b.WriteString(returnType)
	}
	return b.String()
}
