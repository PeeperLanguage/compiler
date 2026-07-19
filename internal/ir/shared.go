package ir

import (
	"fmt"
	"strings"

	"compiler/internal/semantics/symbols"
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

type BoolLit struct {
	Value    bool
	Location *source.Location
}

type ZeroValue struct {
	Type     string
	Location *source.Location
}

type OptionalSome struct {
	Value    Expr
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

type PlaceProjectionKind uint8

const (
	PlaceProjectionDeref PlaceProjectionKind = iota
	PlaceProjectionField
	PlaceProjectionIndex
)

type PlaceProjection struct {
	Kind       PlaceProjectionKind
	FieldIndex int
	Index      Expr
	Type       string
	Location   *source.Location
}

type Place struct {
	Root        Expr
	Projections []PlaceProjection
	Type        string
	Location    *source.Location
}

type Load struct {
	Place    *Place
	DropRoot bool
	Location *source.Location
}

type AddrOf struct {
	Place    *Place
	Type     string
	Location *source.Location
}

type TempBorrow struct {
	Value    Expr
	Slice    bool
	Type     string
	Location *source.Location
}

// SliceView shapes array storage into a non-owning reference value.
type SliceView struct {
	Source       *Place
	Start        Expr
	End          Expr
	EndExclusive bool
	Type         string
	Location     *source.Location
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
	Consumes bool
	Type     string
	Location *source.Location
}

type Field struct {
	Base     Expr
	Index    int
	DropBase bool
	Type     string
	Location *source.Location
}

type StructLit struct {
	Fields   []Expr
	Type     string
	Location *source.Location
}

type ArrayLit struct {
	Values   []Expr
	Dynamic  bool
	Type     string
	Location *source.Location
}

type DynamicArrayOp struct {
	Op       symbols.CompilerOp
	Array    Expr
	Length   Expr
	Value    Expr
	Type     string
	Location *source.Location
}

type Cast struct {
	Expr     Expr
	Type     string
	Location *source.Location
}

type Print struct {
	Value    Expr
	Location *source.Location
}

type Drop struct {
	Value    Expr
	Location *source.Location
}

func (*InvalidExpr) exprNode()    {}
func (*IntLit) exprNode()         {}
func (*FloatLit) exprNode()       {}
func (*StringLit) exprNode()      {}
func (*BoolLit) exprNode()        {}
func (*ZeroValue) exprNode()      {}
func (*OptionalSome) exprNode()   {}
func (*Ident) exprNode()          {}
func (*Unary) exprNode()          {}
func (*Binary) exprNode()         {}
func (*Call) exprNode()           {}
func (*Load) exprNode()           {}
func (*AddrOf) exprNode()         {}
func (*TempBorrow) exprNode()     {}
func (*SliceView) exprNode()      {}
func (*InterfaceMake) exprNode()  {}
func (*InterfaceCall) exprNode()  {}
func (*Field) exprNode()          {}
func (*StructLit) exprNode()      {}
func (*ArrayLit) exprNode()       {}
func (*DynamicArrayOp) exprNode() {}
func (*Cast) exprNode()           {}
func (*Print) exprNode()          {}
func (*Drop) exprNode()           {}

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
	case *BoolLit:
		return node.Location
	case *ZeroValue:
		return node.Location
	case *OptionalSome:
		return node.Location
	case *Ident:
		return node.Location
	case *Unary:
		return node.Location
	case *Binary:
		return node.Location
	case *Call:
		return node.Location
	case *Load:
		return node.Location
	case *AddrOf:
		return node.Location
	case *TempBorrow:
		return node.Location
	case *SliceView:
		return node.Location
	case *InterfaceMake:
		return node.Location
	case *InterfaceCall:
		return node.Location
	case *Field:
		return node.Location
	case *StructLit:
		return node.Location
	case *ArrayLit:
		return node.Location
	case *DynamicArrayOp:
		return node.Location
	case *Cast:
		return node.Location
	case *Print:
		return node.Location
	case *Drop:
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
func (e *BoolLit) String() string {
	if e != nil && e.Value {
		return "true"
	}
	return "false"
}
func (e *BoolLit) TypeText() string { return "bool" }
func (e *ZeroValue) String() string {
	if e == nil || e.Type == "" {
		return "zero"
	}
	return "zero(" + e.Type + ")"
}
func (e *ZeroValue) TypeText() string {
	if e == nil {
		return ""
	}
	return e.Type
}
func (e *OptionalSome) String() string {
	if e == nil || e.Value == nil {
		return "some(<nil>)"
	}
	return "some(" + e.Value.String() + ")"
}
func (e *OptionalSome) TypeText() string {
	if e == nil {
		return ""
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

func (e *Print) String() string {
	if e == nil || e.Value == nil {
		return "print(<nil>)"
	}
	return "print(" + e.Value.String() + ")"
}

func (*Print) TypeText() string { return "" }

func (e *Drop) String() string {
	if e == nil || e.Value == nil {
		return "drop(<nil>)"
	}
	return "drop(" + e.Value.String() + ")"
}

func (*Drop) TypeText() string { return "" }

func (p *Place) String() string {
	if p == nil || p.Root == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(p.Root.String())
	for _, projection := range p.Projections {
		switch projection.Kind {
		case PlaceProjectionDeref:
			root := b.String()
			b.Reset()
			b.WriteString("deref(")
			b.WriteString(root)
			b.WriteString(")")
		case PlaceProjectionField:
			fmt.Fprintf(&b, ".%d", projection.FieldIndex)
		case PlaceProjectionIndex:
			b.WriteString("[")
			if projection.Index != nil {
				b.WriteString(projection.Index.String())
			}
			b.WriteString("]")
		}
	}
	return b.String()
}

func (p *Place) TypeText() string {
	if p == nil {
		return ""
	}
	return p.Type
}

func (e *Load) String() string {
	if e == nil || e.Place == nil {
		return ""
	}
	return "load(" + e.Place.String() + ")"
}

func (e *Load) TypeText() string {
	if e == nil || e.Place == nil {
		return ""
	}
	return e.Place.TypeText()
}

func (e *AddrOf) String() string {
	if e == nil || e.Place == nil {
		return ""
	}
	return "^(" + e.Place.String() + ")"
}

func (e *AddrOf) TypeText() string {
	if e == nil {
		return ""
	}
	return e.Type
}

func (e *TempBorrow) String() string {
	if e == nil || e.Value == nil {
		return "borrowtemp(<nil>)"
	}
	return "borrowtemp(" + e.Value.String() + ")"
}

func (e *TempBorrow) TypeText() string {
	if e == nil {
		return ""
	}
	return e.Type
}

func (e *SliceView) String() string {
	if e == nil || e.Source == nil {
		return ""
	}
	return "view(" + e.Source.String() + ")"
}

func (e *SliceView) TypeText() string {
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

func (e *ArrayLit) String() string {
	if e == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("[")
	for i, value := range e.Values {
		if i > 0 {
			b.WriteString(", ")
		}
		if value != nil {
			b.WriteString(value.String())
		}
	}
	b.WriteString("]")
	return b.String()
}

func (e *ArrayLit) TypeText() string {
	if e == nil {
		return ""
	}
	return e.Type
}

func (e *DynamicArrayOp) String() string {
	if e == nil {
		return ""
	}
	args := make([]string, 0, 3)
	if e.Array != nil {
		args = append(args, e.Array.String())
	}
	if e.Length != nil {
		args = append(args, e.Length.String())
	}
	if e.Value != nil {
		args = append(args, e.Value.String())
	}
	return string(e.Op) + "(" + strings.Join(args, ", ") + ")"
}

func (e *DynamicArrayOp) TypeText() string {
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

func ArrayTypeParts(typeText string) (string, string, bool) {
	typeText = strings.TrimSpace(typeText)
	if !strings.HasPrefix(typeText, "[") {
		return "", "", false
	}
	close := strings.IndexByte(typeText, ']')
	if close <= 1 || close == len(typeText)-1 {
		return "", "", false
	}
	return strings.TrimSpace(typeText[1:close]), strings.TrimSpace(typeText[close+1:]), true
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
