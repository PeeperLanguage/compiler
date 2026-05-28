package ir

import (
	"fmt"
	"strings"

	"compiler/internal/frontend/ast"
	"compiler/internal/tokens"
	"compiler/internal/utils/numeric"
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
	TypeText() string
}

type InvalidExpr struct {
	Message string
	Type    string
}

type IntLit struct {
	Value string
	Type  string
}

type FloatLit struct {
	Value string
	Type  string
}

type Ident struct {
	Name string
	Type string
}

type Unary struct {
	Op   string
	Arg  Expr
	Type string
}

type Binary struct {
	Op    string
	Left  Expr
	Right Expr
	Type  string
}

type Call struct {
	Callee Expr
	Args   []Expr
	Type   string
}

type Cast struct {
	Expr Expr
	Type string
}

func (*InvalidExpr) exprNode() {}
func (*IntLit) exprNode()      {}
func (*FloatLit) exprNode()    {}
func (*Ident) exprNode()       {}
func (*Unary) exprNode()       {}
func (*Binary) exprNode()      {}
func (*Call) exprNode()        {}
func (*Cast) exprNode()        {}

func (e *InvalidExpr) String() string {
	if e == nil || e.Message == "" {
		return "<invalid>"
	}
	return "<invalid: " + e.Message + ">"
}
func (e *InvalidExpr) TypeText() string {
	if e == nil || e.Type == "" {
		return "<invalid>"
	}
	return e.Type
}

func (e *IntLit) String() string {
	if e == nil {
		return "0"
	}
	return e.Value
}
func (e *IntLit) TypeText() string {
	if e == nil || e.Type == "" {
		return "i32"
	}
	return e.Type
}
func (e *FloatLit) String() string {
	if e == nil {
		return "0.0"
	}
	return e.Value
}
func (e *FloatLit) TypeText() string {
	if e == nil || e.Type == "" {
		return "f64"
	}
	return e.Type
}
func (e *Ident) String() string { return e.Name }
func (e *Ident) TypeText() string {
	if e == nil {
		return ""
	}
	return e.Type
}
func (e *Unary) String() string { return fmt.Sprintf("(%s %s)", e.Op, e.Arg.String()) }
func (e *Unary) TypeText() string {
	if e == nil {
		return ""
	}
	if e.Type != "" {
		return e.Type
	}
	if e.Arg != nil {
		return e.Arg.TypeText()
	}
	return ""
}
func (e *Binary) String() string {
	return fmt.Sprintf("(%s %s %s)", e.Op, e.Left.String(), e.Right.String())
}
func (e *Binary) TypeText() string {
	if e == nil {
		return ""
	}
	return e.Type
}

func (e *Call) String() string {
	if e == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(e.Callee.String())
	b.WriteString("(")
	for i, arg := range e.Args {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(arg.String())
	}
	b.WriteString(")")
	return b.String()
}
func (e *Call) TypeText() string {
	if e == nil {
		return ""
	}
	return e.Type
}

func (e *Cast) String() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("(%s as %s)", e.Expr.String(), e.Type)
}

func (e *Cast) TypeText() string {
	if e == nil {
		return ""
	}
	return e.Type
}

func IsFloatType(name string) bool {
	return name == "f32" || name == "f64"
}

func IsIntegerType(name string) bool {
	_, _, ok := ParseIntegerType(name)
	return ok
}

func IsBoolType(name string) bool {
	return name == "bool"
}

func ParseIntegerType(name string) (signed bool, bits int, ok bool) {
	return tokens.ParseIntegerBuiltin(name)
}

func IsScalarType(name string) bool {
	return IsIntegerType(name) || IsFloatType(name) || IsBoolType(name)
}

func IsFloatLiteral(text string) bool {
	return numeric.LooksFloatLike(text)
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
