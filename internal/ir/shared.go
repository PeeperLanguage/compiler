package ir

import (
	"fmt"
	"strings"

	"compiler/internal/semantics/symbols"
	"compiler/internal/source"
)

// NodeID identifies source syntax without retaining an AST object in IR.
type NodeID uint32

// SourceInfo keeps semantic identity and source provenance together while IR
// remains independent from AST objects. NodeID is stable across lowering;
// Location is the current diagnostic/debug projection of that identity.
type SourceInfo struct {
	NodeID   NodeID
	Location *source.Location
}

type Param struct {
	Name     string
	Type     TypeID
	SymbolID symbols.SymbolID
}

type Module interface {
	Text() string
}

type Expr interface {
	exprNode()
	String() string
	TypeID() TypeID
}

type InvalidExpr struct {
	Message  string
	Type     TypeID
	Location *source.Location
}

type IntLit struct {
	Value    string
	Type     TypeID
	Location *source.Location
}

type FloatLit struct {
	Value    string
	Type     TypeID
	Location *source.Location
}

type StringLit struct {
	Value    string
	Type     TypeID
	Location *source.Location
}

type BoolLit struct {
	Value    bool
	Type     TypeID
	Location *source.Location
}

type ZeroValue struct {
	Type     TypeID
	Location *source.Location
}

type OptionalSome struct {
	Value    Expr
	Type     TypeID
	Location *source.Location
}

type Ident struct {
	Name     string
	Type     TypeID
	SymbolID symbols.SymbolID
	Location *source.Location
}

type Unary struct {
	Op       string
	Arg      Expr
	Type     TypeID
	Location *source.Location
}

type Binary struct {
	Op       string
	Left     Expr
	Right    Expr
	Type     TypeID
	Location *source.Location
}

type Call struct {
	Callee   Expr
	Args     []Expr
	Type     TypeID
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
	Type       TypeID
	Location   *source.Location
}

type Place struct {
	Root        Expr
	Projections []PlaceProjection
	Type        TypeID
	Location    *source.Location
}

type Load struct {
	Place    *Place
	DropRoot bool
	NodeID   NodeID
	Location *source.Location
}

type AddrOf struct {
	Place    *Place
	Type     TypeID
	Location *source.Location
}

type TempBorrow struct {
	Value    Expr
	Slice    bool
	Type     TypeID
	Location *source.Location
}

// SliceView shapes array storage into a non-owning reference value.
type SliceView struct {
	Source       *Place
	Start        Expr
	End          Expr
	EndExclusive bool
	Type         TypeID
	Location     *source.Location
}

type InterfaceSlot struct {
	InterfaceType TypeID
	MethodName    string
	WrapperName   string
	SlotType      TypeID
	FuncName      string
	FuncType      TypeID
	DataType      TypeID
}

type InterfaceMake struct {
	Value    Expr
	Slots    []InterfaceSlot
	Type     TypeID
	Location *source.Location
}

type InterfaceCall struct {
	Base     Expr
	Slot     int
	Args     []Expr
	Consumes bool
	Type     TypeID
	Location *source.Location
}

type Field struct {
	Base     Expr
	Index    int
	DropBase bool
	NodeID   NodeID
	Type     TypeID
	Location *source.Location
}

type StructLit struct {
	Fields   []Expr
	Type     TypeID
	Location *source.Location
}

type ArrayLit struct {
	Values   []Expr
	Dynamic  bool
	Type     TypeID
	Location *source.Location
}

type DynamicArrayOp struct {
	Op       symbols.CompilerOp
	Array    Expr
	Length   Expr
	Value    Expr
	Type     TypeID
	Location *source.Location
}

type AllocExpr struct {
	Value     Expr
	Allocator Expr
	Type      TypeID
	Location  *source.Location
}

type Cast struct {
	Expr     Expr
	Type     TypeID
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
func (*AllocExpr) exprNode()      {}
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
	case *AllocExpr:
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
func (e *InvalidExpr) TypeID() TypeID {
	if e == nil || e.Type == InvalidType {
		return InvalidType
	}
	return e.Type
}

func (e *IntLit) String() string {
	if e == nil {
		return "0"
	}
	return e.Value
}
func (e *IntLit) TypeID() TypeID {
	if e == nil || e.Type == InvalidType {
		return InvalidType
	}
	return e.Type
}
func (e *FloatLit) String() string {
	if e == nil {
		return "0.0"
	}
	return e.Value
}
func (e *FloatLit) TypeID() TypeID {
	if e == nil || e.Type == InvalidType {
		return InvalidType
	}
	return e.Type
}
func (e *StringLit) String() string {
	if e == nil {
		return `""`
	}
	return fmt.Sprintf("%q", e.Value)
}
func (e *StringLit) TypeID() TypeID {
	if e == nil || e.Type == InvalidType {
		return InvalidType
	}
	return e.Type
}
func (e *BoolLit) String() string {
	if e != nil && e.Value {
		return "true"
	}
	return "false"
}
func (e *BoolLit) TypeID() TypeID {
	if e == nil {
		return InvalidType
	}
	return e.Type
}
func (e *ZeroValue) String() string {
	if e == nil || e.Type == InvalidType {
		return "zero"
	}
	return "zero"
}
func (e *ZeroValue) TypeID() TypeID {
	if e == nil {
		return InvalidType
	}
	return e.Type
}
func (e *OptionalSome) String() string {
	if e == nil || e.Value == nil {
		return "some(<nil>)"
	}
	return "some(" + e.Value.String() + ")"
}
func (e *OptionalSome) TypeID() TypeID {
	if e == nil {
		return InvalidType
	}
	return e.Type
}
func (e *Ident) String() string { return e.Name }
func (e *Ident) TypeID() TypeID {
	if e == nil {
		return InvalidType
	}
	return e.Type
}
func (e *Unary) String() string { return fmt.Sprintf("(%s %s)", e.Op, e.Arg.String()) }
func (e *Unary) TypeID() TypeID {
	if e == nil {
		return InvalidType
	}
	if e.Type != InvalidType {
		return e.Type
	}
	if e.Arg != nil {
		return e.Arg.TypeID()
	}
	return InvalidType
}
func (e *Binary) String() string {
	return fmt.Sprintf("(%s %s %s)", e.Op, e.Left.String(), e.Right.String())
}
func (e *Binary) TypeID() TypeID {
	if e == nil {
		return InvalidType
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
func (e *Call) TypeID() TypeID {
	if e == nil {
		return InvalidType
	}
	return e.Type
}

func (e *Print) String() string {
	if e == nil || e.Value == nil {
		return "print(<nil>)"
	}
	return "print(" + e.Value.String() + ")"
}

func (*Print) TypeID() TypeID { return InvalidType }

func (e *Drop) String() string {
	if e == nil || e.Value == nil {
		return "drop(<nil>)"
	}
	return "drop(" + e.Value.String() + ")"
}

func (*Drop) TypeID() TypeID { return InvalidType }

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

func (p *Place) TypeID() TypeID {
	if p == nil {
		return InvalidType
	}
	return p.Type
}

func (e *Load) String() string {
	if e == nil || e.Place == nil {
		return ""
	}
	return "load(" + e.Place.String() + ")"
}

func (e *Load) TypeID() TypeID {
	if e == nil || e.Place == nil {
		return InvalidType
	}
	return e.Place.TypeID()
}

func (e *AddrOf) String() string {
	if e == nil || e.Place == nil {
		return ""
	}
	return "^(" + e.Place.String() + ")"
}

func (e *AddrOf) TypeID() TypeID {
	if e == nil {
		return InvalidType
	}
	return e.Type
}

func (e *TempBorrow) String() string {
	if e == nil || e.Value == nil {
		return "borrowtemp(<nil>)"
	}
	return "borrowtemp(" + e.Value.String() + ")"
}

func (e *TempBorrow) TypeID() TypeID {
	if e == nil {
		return InvalidType
	}
	return e.Type
}

func (e *SliceView) String() string {
	if e == nil || e.Source == nil {
		return ""
	}
	return "view(" + e.Source.String() + ")"
}

func (e *SliceView) TypeID() TypeID {
	if e == nil {
		return InvalidType
	}
	return e.Type
}

func (e *InterfaceMake) String() string {
	if e == nil || e.Value == nil {
		return "<iface>"
	}
	return fmt.Sprintf("iface(%s)", e.Value.String())
}

func (e *InterfaceMake) TypeID() TypeID {
	if e == nil {
		return InvalidType
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

func (e *InterfaceCall) TypeID() TypeID {
	if e == nil {
		return InvalidType
	}
	return e.Type
}

func (e *Field) String() string {
	if e == nil || e.Base == nil {
		return ""
	}
	return fmt.Sprintf("%s.%d", e.Base.String(), e.Index)
}

func (e *Field) TypeID() TypeID {
	if e == nil {
		return InvalidType
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

func (e *StructLit) TypeID() TypeID {
	if e == nil {
		return InvalidType
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

func (e *ArrayLit) TypeID() TypeID {
	if e == nil {
		return InvalidType
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

func (e *DynamicArrayOp) TypeID() TypeID {
	if e == nil {
		return InvalidType
	}
	return e.Type
}

func (e *AllocExpr) String() string {
	if e == nil {
		return ""
	}
	if e.Allocator != nil {
		return "alloc(" + e.Value.String() + ", " + e.Allocator.String() + ")"
	}
	return "alloc(" + e.Value.String() + ")"
}

func (e *AllocExpr) TypeID() TypeID {
	if e == nil {
		return InvalidType
	}
	return e.Type
}

func (e *Cast) String() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("cast(%s)", e.Expr.String())
}

func (e *Cast) TypeID() TypeID {
	if e == nil {
		return InvalidType
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

func SignatureText(types *TypeTable, params []Param, returnType TypeID) string {
	if types == nil {
		panic("formatting IR signature without a type table")
	}
	var b strings.Builder
	b.WriteString("(")
	for i, param := range params {
		if i > 0 {
			b.WriteString(", ")
		}
		if param.Name != "" {
			b.WriteString(param.Name)
			if param.Type != InvalidType {
				b.WriteString(": ")
			}
		}
		b.WriteString(types.Text(param.Type))
	}
	b.WriteString(")")
	if returnType != InvalidType && types.Text(returnType) != "void" {
		b.WriteString(" -> ")
		b.WriteString(types.Text(returnType))
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
