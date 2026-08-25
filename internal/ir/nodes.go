package ir

import (
	"fmt"
	"reflect"
	"strings"

	"compiler/internal/semantics/symbols"
	"compiler/internal/source"
	"compiler/pkg/ascii"
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

func (info SourceInfo) Origin() SourceInfo { return info }

func (info *SourceInfo) setOrigin(origin SourceInfo) {
	if info != nil {
		*info = origin
	}
}

type Param struct {
	Name     string
	Type     TypeID
	SymbolID symbols.SymbolID
}

type Expr interface {
	exprNode()
	forEachChild(func(Expr))
	String() string
	TypeID() TypeID
	Origin() SourceInfo
	setOrigin(SourceInfo)
}

type InvalidExpr struct {
	SourceInfo
	Message string
	Type    TypeID
}

type IntLit struct {
	SourceInfo
	Value string
	Type  TypeID
}

type FloatLit struct {
	SourceInfo
	Value string
	Type  TypeID
}

type StringLit struct {
	SourceInfo
	Value string
	Type  TypeID
}

type BoolLit struct {
	SourceInfo
	Value bool
	Type  TypeID
}

type ZeroValue struct {
	SourceInfo
	Type TypeID
}

type OptionalSome struct {
	SourceInfo
	Value Expr
	Type  TypeID
}

type OptionalPresent struct {
	SourceInfo
	Value Expr
	Type  TypeID
}

type Ident struct {
	SourceInfo
	Name     string
	Type     TypeID
	SymbolID symbols.SymbolID
}

type Unary struct {
	SourceInfo
	Op   string
	Arg  Expr
	Type TypeID
}

type Binary struct {
	SourceInfo
	Op    string
	Left  Expr
	Right Expr
	Type  TypeID
}

type Call struct {
	SourceInfo
	Callee Expr
	Args   []Expr
	Type   TypeID
}

type PlaceProjectionKind uint8

const (
	PlaceProjectionDeref PlaceProjectionKind = iota
	PlaceProjectionField
	PlaceProjectionIndex
	PlaceProjectionOptionalPayload
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
	SourceInfo
	Place    *Place
	DropRoot bool
}

type AddrOf struct {
	SourceInfo
	Place *Place
	Type  TypeID
}

type TempBorrow struct {
	SourceInfo
	Value Expr
	Slice bool
	Type  TypeID
}

type Len struct {
	SourceInfo
	Value Expr
	Type  TypeID
}

// StringChars decodes a borrowed string into an owned dynamic char array.
type StringChars struct {
	SourceInfo
	Value Expr
	Type  TypeID
}

// SliceView shapes array storage into a non-owning reference value.
type SliceView struct {
	SourceInfo
	Source       *Place
	Start        Expr
	End          Expr
	EndExclusive bool
	Type         TypeID
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
	SourceInfo
	Value Expr
	Slots []InterfaceSlot
	Type  TypeID
}

type InterfaceCall struct {
	SourceInfo
	Base     Expr
	Slot     int
	Args     []Expr
	Consumes bool
	Type     TypeID
}

type Field struct {
	SourceInfo
	Base     Expr
	Index    int
	DropBase bool
	Type     TypeID
}

type StructLit struct {
	SourceInfo
	Fields []Expr
	Type   TypeID
}

type ArrayLit struct {
	SourceInfo
	Values  []Expr
	Dynamic bool
	Type    TypeID
}

type DynamicArrayOp struct {
	SourceInfo
	Op        symbols.CompilerOp
	Array     Expr
	Length    Expr
	Value     Expr
	ArrayType TypeID
	Type      TypeID
}

type AllocExpr struct {
	SourceInfo
	Value     Expr
	Allocator Expr
	Type      TypeID
}

type Cast struct {
	SourceInfo
	Expr Expr
	Type TypeID
}

type Print struct {
	SourceInfo
	Value   Expr
	Newline bool
}

type Drop struct {
	SourceInfo
	Value Expr
}

var (
	_ Expr = (*InvalidExpr)(nil)
	_ Expr = (*IntLit)(nil)
	_ Expr = (*FloatLit)(nil)
	_ Expr = (*StringLit)(nil)
	_ Expr = (*BoolLit)(nil)
	_ Expr = (*ZeroValue)(nil)
	_ Expr = (*OptionalSome)(nil)
	_ Expr = (*OptionalPresent)(nil)
	_ Expr = (*Ident)(nil)
	_ Expr = (*Unary)(nil)
	_ Expr = (*Binary)(nil)
	_ Expr = (*Call)(nil)
	_ Expr = (*Load)(nil)
	_ Expr = (*AddrOf)(nil)
	_ Expr = (*TempBorrow)(nil)
	_ Expr = (*Len)(nil)
	_ Expr = (*StringChars)(nil)
	_ Expr = (*SliceView)(nil)
	_ Expr = (*InterfaceMake)(nil)
	_ Expr = (*InterfaceCall)(nil)
	_ Expr = (*Field)(nil)
	_ Expr = (*StructLit)(nil)
	_ Expr = (*ArrayLit)(nil)
	_ Expr = (*DynamicArrayOp)(nil)
	_ Expr = (*AllocExpr)(nil)
	_ Expr = (*Cast)(nil)
	_ Expr = (*Print)(nil)
	_ Expr = (*Drop)(nil)
)

func (*InvalidExpr) exprNode()                           {}
func (*InvalidExpr) forEachChild(func(Expr))             {}
func (*IntLit) exprNode()                                {}
func (*IntLit) forEachChild(func(Expr))                  {}
func (*FloatLit) exprNode()                              {}
func (*FloatLit) forEachChild(func(Expr))                {}
func (*StringLit) exprNode()                             {}
func (*StringLit) forEachChild(func(Expr))               {}
func (*BoolLit) exprNode()                               {}
func (*BoolLit) forEachChild(func(Expr))                 {}
func (*ZeroValue) exprNode()                             {}
func (*ZeroValue) forEachChild(func(Expr))               {}
func (*OptionalSome) exprNode()                          {}
func (e *OptionalSome) forEachChild(visit func(Expr))    { visit(e.Value) }
func (*OptionalPresent) exprNode()                       {}
func (e *OptionalPresent) forEachChild(visit func(Expr)) { visit(e.Value) }
func (*Ident) exprNode()                                 {}
func (*Ident) forEachChild(func(Expr))                   {}
func (*Unary) exprNode()                                 {}
func (e *Unary) forEachChild(visit func(Expr))           { visit(e.Arg) }
func (*Binary) exprNode()                                {}
func (e *Binary) forEachChild(visit func(Expr)) {
	visit(e.Left)
	visit(e.Right)
}
func (*Call) exprNode() {}
func (e *Call) forEachChild(visit func(Expr)) {
	visit(e.Callee)
	for _, arg := range e.Args {
		visit(arg)
	}
}
func (*Load) exprNode()                              {}
func (e *Load) forEachChild(visit func(Expr))        { e.Place.forEachChild(visit) }
func (*AddrOf) exprNode()                            {}
func (e *AddrOf) forEachChild(visit func(Expr))      { e.Place.forEachChild(visit) }
func (*TempBorrow) exprNode()                        {}
func (e *TempBorrow) forEachChild(visit func(Expr))  { visit(e.Value) }
func (*Len) exprNode()                               {}
func (e *Len) forEachChild(visit func(Expr))         { visit(e.Value) }
func (*StringChars) exprNode()                       {}
func (e *StringChars) forEachChild(visit func(Expr)) { visit(e.Value) }
func (*SliceView) exprNode()                         {}
func (e *SliceView) forEachChild(visit func(Expr)) {
	e.Source.forEachChild(visit)
	visit(e.Start)
	visit(e.End)
}
func (*InterfaceMake) exprNode()                       {}
func (e *InterfaceMake) forEachChild(visit func(Expr)) { visit(e.Value) }
func (*InterfaceCall) exprNode()                       {}
func (e *InterfaceCall) forEachChild(visit func(Expr)) {
	visit(e.Base)
	for _, arg := range e.Args {
		visit(arg)
	}
}
func (*Field) exprNode()                       {}
func (e *Field) forEachChild(visit func(Expr)) { visit(e.Base) }
func (*StructLit) exprNode()                   {}
func (e *StructLit) forEachChild(visit func(Expr)) {
	for _, field := range e.Fields {
		visit(field)
	}
}
func (*ArrayLit) exprNode() {}
func (e *ArrayLit) forEachChild(visit func(Expr)) {
	for _, value := range e.Values {
		visit(value)
	}
}
func (*DynamicArrayOp) exprNode() {}
func (e *DynamicArrayOp) forEachChild(visit func(Expr)) {
	visit(e.Array)
	visit(e.Length)
	visit(e.Value)
}
func (*AllocExpr) exprNode() {}
func (e *AllocExpr) forEachChild(visit func(Expr)) {
	visit(e.Value)
	visit(e.Allocator)
}
func (*Cast) exprNode()                        {}
func (e *Cast) forEachChild(visit func(Expr))  { visit(e.Expr) }
func (*Print) exprNode()                       {}
func (e *Print) forEachChild(visit func(Expr)) { visit(e.Value) }
func (*Drop) exprNode()                        {}
func (e *Drop) forEachChild(visit func(Expr))  { visit(e.Value) }

func (p *Place) forEachChild(visit func(Expr)) {
	if p == nil {
		return
	}
	visit(p.Root)
	for _, projection := range p.Projections {
		visit(projection.Index)
	}
}

// WithOrigin applies provenance at compiler phase boundaries, including
// synthetic expressions returned by helper lowerers.
func WithOrigin(expr Expr, info SourceInfo) Expr {
	if expr == nil {
		return nil
	}
	value := reflect.ValueOf(expr)
	if value.Kind() == reflect.Pointer && value.IsNil() {
		return expr
	}
	expr.setOrigin(info)
	return expr
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
func (e *OptionalPresent) String() string {
	if e == nil || e.Value == nil {
		return "present(<nil>)"
	}
	return "present(" + e.Value.String() + ")"
}
func (e *OptionalPresent) TypeID() TypeID {
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
	name := "print"
	if e.Newline {
		name = "println"
	}
	return name + "(" + e.Value.String() + ")"
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
		case PlaceProjectionOptionalPayload:
			b.WriteString(".value")
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

func (e *Len) String() string {
	if e == nil || e.Value == nil {
		return "len(<nil>)"
	}
	return "len(" + e.Value.String() + ")"
}

func (e *Len) TypeID() TypeID {
	if e == nil {
		return InvalidType
	}
	return e.Type
}

func (e *StringChars) String() string {
	if e == nil || e.Value == nil {
		return "chars(<nil>)"
	}
	return "chars(" + e.Value.String() + ")"
}

func (e *StringChars) TypeID() TypeID {
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

// ir/nodes.go
func SanitizeSymbolName(text string) string {
	if text == "" {
		return "unknown"
	}

	var b strings.Builder
	for _, r := range text {
		if ascii.IsAlnum(r) {
			b.WriteRune(r)
		} else {
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
