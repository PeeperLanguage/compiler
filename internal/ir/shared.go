package ir

import (
	"fmt"
	"strings"

	"compiler/internal/frontend/ast"
	"compiler/internal/frontend/token"
	"compiler/internal/source"
)

type Param struct {
	Name string
	Type string
}

type Module interface {
	Text() string
}

type Expr interface {
	exprNode()
	String() string
	TypeText() string
}

type InvalidExpr struct {
	Message  string
	Type     string
	Location *source.Location
}

type IntLit struct {
	Value    string
	Type     string
	Location *source.Location
}

type FloatLit struct {
	Value    string
	Type     string
	Location *source.Location
}

type StringLit struct {
	Value    string
	Type     string
	Location *source.Location
}

type Ident struct {
	Name     string
	Type     string
	Location *source.Location
}

type Unary struct {
	Op       string
	Arg      Expr
	Type     string
	Location *source.Location
}

type Binary struct {
	Op       string
	Left     Expr
	Right    Expr
	Type     string
	Location *source.Location
}

type Call struct {
	Callee   Expr
	Args     []Expr
	Type     string
	Location *source.Location
}

type AddrOf struct {
	Expr     Expr
	Type     string
	Location *source.Location
}

type InterfaceSlot struct {
	InterfaceType string
	MethodName    string
	WrapperName   string
	SlotType      string
	FuncName      string
	FuncType      string
	DataType      string
}

type InterfaceMake struct {
	Value    Expr
	Slots    []InterfaceSlot
	Type     string
	Location *source.Location
}

type InterfaceCall struct {
	Base     Expr
	Slot     int
	Args     []Expr
	Type     string
	Location *source.Location
}

type Field struct {
	Base       Expr
	Index      int
	ThroughPtr bool
	Type       string
	Location   *source.Location
}

type StructLit struct {
	Fields   []Expr
	Type     string
	Location *source.Location
}

type Cast struct {
	Expr     Expr
	Type     string
	Location *source.Location
}

func (*InvalidExpr) exprNode()   {}
func (*IntLit) exprNode()        {}
func (*FloatLit) exprNode()      {}
func (*StringLit) exprNode()     {}
func (*Ident) exprNode()         {}
func (*Unary) exprNode()         {}
func (*Binary) exprNode()        {}
func (*Call) exprNode()          {}
func (*AddrOf) exprNode()        {}
func (*InterfaceMake) exprNode() {}
func (*InterfaceCall) exprNode() {}
func (*Field) exprNode()         {}
func (*StructLit) exprNode()     {}
func (*Cast) exprNode()          {}

func ExprLocation(expr Expr) *source.Location {
	switch node := expr.(type) {
	case *InvalidExpr:
		return node.Location
	case *IntLit:
		return node.Location
	case *FloatLit:
		return node.Location
	case *StringLit:
		return node.Location
	case *Ident:
		return node.Location
	case *Unary:
		return node.Location
	case *Binary:
		return node.Location
	case *Call:
		return node.Location
	case *AddrOf:
		return node.Location
	case *InterfaceMake:
		return node.Location
	case *InterfaceCall:
		return node.Location
	case *Field:
		return node.Location
	case *StructLit:
		return node.Location
	case *Cast:
		return node.Location
	default:
		return nil
	}
}

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
func (e *StringLit) String() string {
	if e == nil {
		return `""`
	}
	return fmt.Sprintf("%q", e.Value)
}
func (e *StringLit) TypeText() string {
	if e == nil || e.Type == "" {
		return "cstr"
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

func (e *AddrOf) String() string {
	if e == nil || e.Expr == nil {
		return ""
	}
	return "^(" + e.Expr.String() + ")"
}

func (e *AddrOf) TypeText() string {
	if e == nil {
		return ""
	}
	return e.Type
}

func (e *InterfaceMake) String() string {
	if e == nil || e.Value == nil {
		return "<iface>"
	}
	return fmt.Sprintf("iface(%s)", e.Value.String())
}

func (e *InterfaceMake) TypeText() string {
	if e == nil {
		return ""
	}
	return e.Type
}

func (e *InterfaceCall) String() string {
	if e == nil || e.Base == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("ifacecall(")
	b.WriteString(e.Base.String())
	for _, arg := range e.Args {
		b.WriteString(", ")
		if arg != nil {
			b.WriteString(arg.String())
		}
	}
	b.WriteString(")")
	return b.String()
}

func (e *InterfaceCall) TypeText() string {
	if e == nil {
		return ""
	}
	return e.Type
}

func (e *Field) String() string {
	if e == nil || e.Base == nil {
		return ""
	}
	return fmt.Sprintf("%s.%d", e.Base.String(), e.Index)
}

func (e *Field) TypeText() string {
	if e == nil {
		return ""
	}
	return e.Type
}

func (e *StructLit) String() string {
	if e == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(".{")
	for i, field := range e.Fields {
		if i > 0 {
			b.WriteString(", ")
		}
		if field != nil {
			b.WriteString(field.String())
		}
	}
	b.WriteString("}")
	return b.String()
}

func (e *StructLit) TypeText() string {
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
	_, _, ok := token.ParseIntegerBuiltin(name)
	return ok
}

func IsBoolType(name string) bool {
	return name == "bool"
}

func TypeText(typ ast.TypeExpr) string {
	switch node := typ.(type) {
	case nil:
		return ""
	case *ast.NamedType:
		return node.Name
	case *ast.RawPtrType:
		return "^" + TypeText(node.Target)
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

func SanitizeSymbolName(text string) string {
	if text == "" {
		return "unknown"
	}
	var b strings.Builder
	for _, r := range text {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

func StripSymbolInstance(text string) string {
	if before, _, ok := strings.Cut(text, "$"); ok {
		return before
	}
	return text
}

func InterfaceThunkName(interfaceTypeText, dataType, methodName string, index int) string {
	return fmt.Sprintf("__ifacethunk__%s__%s__%s__%d",
		SanitizeSymbolName(interfaceTypeText),
		SanitizeSymbolName(dataType),
		SanitizeSymbolName(methodName),
		index)
}
