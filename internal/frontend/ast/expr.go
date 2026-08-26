package ast

import (
	"compiler/internal/source"
	"strconv"
	"strings"
)

type Ident struct {
	NodeIDHolder
	Name     string
	Location *source.Location
}

func (*Ident) exprNode()               {}
func (*Ident) forEachChild(func(Node)) {}
func (e *Ident) loc() *source.Location { return e.Location }

func (e *Ident) exprText() string {
	if e == nil {
		return ""
	}
	return e.Name
}

func (e *Ident) copyExpr(substitutions map[string]Expr, newID func(NodeID, bool) NodeID, fromArgument bool) Expr {
	if e == nil {
		return nil
	}
	if replacement := substitutions[e.Name]; replacement != nil {
		return replacement.copyExpr(nil, newID, true)
	}
	id := newID(e.ID(), fromArgument)
	return &Ident{NodeIDHolder: NodeIDHolder{NodeID: id}, Name: e.Name, Location: e.Location}
}

type ScopeResolution struct {
	NodeIDHolder
	Segments []PathSegment
	Location *source.Location
}

type PathSegment struct {
	Name     *Ident
	TypeArgs []TypeExpr
	Location *source.Location
}

func (*ScopeResolution) exprNode() {}
func (*ScopeResolution) typeNode() {}
func (e *ScopeResolution) forEachChild(visit func(Node)) {
	for _, segment := range e.Segments {
		visit(segment.Name)
		for _, arg := range segment.TypeArgs {
			visit(arg)
		}
	}
}
func (e *ScopeResolution) loc() *source.Location { return e.Location }
func (e *ScopeResolution) exprText() string {
	if e == nil {
		return ""
	}
	return e.TypeText()
}
func (e *ScopeResolution) TypeText() string {
	if e == nil {
		return ""
	}
	parts := make([]string, 0, len(e.Segments))
	for _, segment := range e.Segments {
		parts = append(parts, appliedTypeText(segment.Name, segment.TypeArgs))
	}
	return strings.Join(parts, "::")
}

// ImportMember accepts only paths whose first segment is an import qualifier
// and whose second segment is the imported symbol. Longer paths belong to
// enum/type member resolution in later semantic work.
func (e *ScopeResolution) ImportMember() (qualifier, member *Ident, ok bool) {
	if e == nil || len(e.Segments) != 2 || len(e.Segments[0].TypeArgs) != 0 ||
		e.Segments[0].Name == nil || e.Segments[1].Name == nil {
		return nil, nil, false
	}
	return e.Segments[0].Name, e.Segments[1].Name, true
}

// ImportValueMember excludes applied final segments because generic functions
// and values are not part of named-type application support.
func (e *ScopeResolution) ImportValueMember() (qualifier, member *Ident, ok bool) {
	qualifier, member, ok = e.ImportMember()
	if !ok || len(e.Segments[1].TypeArgs) != 0 {
		return nil, nil, false
	}
	return qualifier, member, true
}

// EnumVariantMember splits fully qualified variant syntax into its enum type
// and final case name. Variant names cannot carry type arguments.
func (e *ScopeResolution) EnumVariantMember() (TypeExpr, *Ident, bool) {
	if e == nil || len(e.Segments) < 2 || len(e.Segments) > 3 {
		return nil, nil, false
	}
	caseSegment := e.Segments[len(e.Segments)-1]
	if caseSegment.Name == nil || len(caseSegment.TypeArgs) != 0 {
		return nil, nil, false
	}
	if len(e.Segments) == 2 {
		typeSegment := e.Segments[0]
		if typeSegment.Name == nil {
			return nil, nil, false
		}
		if len(typeSegment.TypeArgs) == 0 {
			return &NamedType{
				NodeIDHolder: NodeIDHolder{NodeID: typeSegment.Name.ID()},
				Name:         typeSegment.Name.Name,
				Location:     typeSegment.Location,
			}, caseSegment.Name, true
		}
		return &AppliedType{
			NodeIDHolder: NodeIDHolder{NodeID: typeSegment.Name.ID()},
			Name:         typeSegment.Name,
			TypeArgs:     typeSegment.TypeArgs,
			Location:     typeSegment.Location,
		}, caseSegment.Name, true
	}
	if e.Segments[0].Name == nil || len(e.Segments[0].TypeArgs) != 0 || e.Segments[1].Name == nil {
		return nil, nil, false
	}
	return &ScopeResolution{
		NodeIDHolder: NodeIDHolder{NodeID: e.Segments[1].Name.ID()},
		Segments:     e.Segments[:2],
		Location:     e.Location,
	}, caseSegment.Name, true
}

func (e *ScopeResolution) copyExpr(substitutions map[string]Expr, newID func(NodeID, bool) NodeID, fromArgument bool) Expr {
	if e == nil {
		return nil
	}
	id := newID(e.ID(), fromArgument)
	return &ScopeResolution{NodeIDHolder: NodeIDHolder{NodeID: id}, Segments: clonePathSegments(e.Segments, newID, fromArgument), Location: e.Location}
}

func appliedTypeText(name *Ident, args []TypeExpr) string {
	if name == nil {
		return ""
	}
	if len(args) == 0 {
		return name.Name
	}
	parts := make([]string, len(args))
	for index, arg := range args {
		parts[index] = TypeText(arg)
	}
	return name.Name + "<" + strings.Join(parts, ", ") + ">"
}

type SelectorExpr struct {
	NodeIDHolder
	Expr     Expr
	Name     *Ident
	Location *source.Location
}

func (*SelectorExpr) exprNode() {}
func (e *SelectorExpr) forEachChild(visit func(Node)) {
	visit(e.Expr)
	visit(e.Name)
}
func (e *SelectorExpr) loc() *source.Location { return e.Location }
func (e *SelectorExpr) exprText() string {
	if e == nil {
		return ""
	}
	return ExprText(e.Expr) + "." + identText(e.Name)
}

func (e *SelectorExpr) copyExpr(substitutions map[string]Expr, newID func(NodeID, bool) NodeID, fromArgument bool) Expr {
	if e == nil {
		return nil
	}
	id := newID(e.ID(), fromArgument)
	return &SelectorExpr{NodeIDHolder: NodeIDHolder{NodeID: id}, Expr: e.Expr.copyExpr(substitutions, newID, fromArgument), Name: cloneIdent(e.Name, newID, fromArgument), Location: e.Location}
}

type IndexExpr struct {
	NodeIDHolder
	Expr     Expr
	Index    Expr
	Location *source.Location
}

func (*IndexExpr) exprNode() {}
func (e *IndexExpr) forEachChild(visit func(Node)) {
	visit(e.Expr)
	visit(e.Index)
}
func (e *IndexExpr) loc() *source.Location { return e.Location }
func (e *IndexExpr) exprText() string {
	if e == nil {
		return ""
	}
	return ExprText(e.Expr) + "[" + ExprText(e.Index) + "]"
}

func (e *IndexExpr) copyExpr(substitutions map[string]Expr, newID func(NodeID, bool) NodeID, fromArgument bool) Expr {
	if e == nil {
		return nil
	}
	id := newID(e.ID(), fromArgument)
	return &IndexExpr{NodeIDHolder: NodeIDHolder{NodeID: id}, Expr: e.Expr.copyExpr(substitutions, newID, fromArgument), Index: e.Index.copyExpr(substitutions, newID, fromArgument), Location: e.Location}
}

type RangeExpr struct {
	NodeIDHolder
	Start        Expr
	End          Expr
	EndExclusive bool
	Location     *source.Location
}

func (*RangeExpr) exprNode() {}
func (e *RangeExpr) forEachChild(visit func(Node)) {
	visit(e.Start)
	visit(e.End)
}
func (e *RangeExpr) loc() *source.Location { return e.Location }
func (e *RangeExpr) exprText() string {
	if e == nil {
		return ""
	}
	op := ".."
	if !e.EndExclusive {
		op = "..="
	}
	return ExprText(e.Start) + op + ExprText(e.End)
}

func (e *RangeExpr) copyExpr(substitutions map[string]Expr, newID func(NodeID, bool) NodeID, fromArgument bool) Expr {
	if e == nil {
		return nil
	}
	id := newID(e.ID(), fromArgument)
	cloned := &RangeExpr{NodeIDHolder: NodeIDHolder{NodeID: id}, EndExclusive: e.EndExclusive, Location: e.Location}
	if e.Start != nil {
		cloned.Start = e.Start.copyExpr(substitutions, newID, fromArgument)
	}
	if e.End != nil {
		cloned.End = e.End.copyExpr(substitutions, newID, fromArgument)
	}
	return cloned
}

type StructLitField struct {
	Name     *Ident
	Value    Expr
	Location *source.Location
}

type StructLit struct {
	NodeIDHolder
	Type     TypeExpr
	Fields   []StructLitField
	Location *source.Location
}

func (*StructLit) exprNode() {}
func (e *StructLit) forEachChild(visit func(Node)) {
	visit(e.Type)
	for _, field := range e.Fields {
		visit(field.Name)
		visit(field.Value)
	}
}
func (e *StructLit) loc() *source.Location { return e.Location }
func (e *StructLit) exprText() string {
	if e == nil {
		return ""
	}
	var b strings.Builder
	b.WriteByte('.')
	if e.Type != nil {
		b.WriteString(TypeText(e.Type))
	}
	b.WriteByte('{')
	for i, field := range e.Fields {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(identText(field.Name))
		b.WriteString(" = ")
		b.WriteString(ExprText(field.Value))
	}
	b.WriteByte('}')
	return b.String()
}

func (e *StructLit) copyExpr(substitutions map[string]Expr, newID func(NodeID, bool) NodeID, fromArgument bool) Expr {
	if e == nil {
		return nil
	}
	id := newID(e.ID(), fromArgument)
	fields := make([]StructLitField, len(e.Fields))
	for i, field := range e.Fields {
		fields[i] = StructLitField{Name: cloneIdent(field.Name, newID, fromArgument), Value: field.Value.copyExpr(substitutions, newID, fromArgument), Location: field.Location}
	}
	return &StructLit{NodeIDHolder: NodeIDHolder{NodeID: id}, Type: cloneTypeExpr(e.Type, newID, fromArgument), Fields: fields, Location: e.Location}
}

type VariantLit struct {
	NodeIDHolder
	Case     *ScopeResolution
	Payload  Expr
	Location *source.Location
}

func (*VariantLit) exprNode() {}
func (e *VariantLit) forEachChild(visit func(Node)) {
	visit(e.Case)
	visit(e.Payload)
}
func (e *VariantLit) loc() *source.Location { return e.Location }
func (e *VariantLit) exprText() string {
	if e == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(ExprText(e.Case))
	b.WriteString(" with ")
	b.WriteString(ExprText(e.Payload))
	return b.String()
}

func (e *VariantLit) copyExpr(substitutions map[string]Expr, newID func(NodeID, bool) NodeID, fromArgument bool) Expr {
	if e == nil {
		return nil
	}
	var payload Expr
	if e.Payload != nil {
		payload = e.Payload.copyExpr(substitutions, newID, fromArgument)
	}
	return &VariantLit{
		NodeIDHolder: NodeIDHolder{NodeID: newID(e.ID(), fromArgument)},
		Case:         e.Case.copyExpr(substitutions, newID, fromArgument).(*ScopeResolution),
		Payload:      payload,
		Location:     e.Location,
	}
}

type ArrayLit struct {
	NodeIDHolder
	Type        TypeExpr
	Values      []Expr
	InferredLen bool
	Location    *source.Location
}

func (*ArrayLit) exprNode() {}
func (e *ArrayLit) forEachChild(visit func(Node)) {
	visit(e.Type)
	for _, value := range e.Values {
		visit(value)
	}
}
func (e *ArrayLit) loc() *source.Location { return e.Location }
func (e *ArrayLit) exprText() string {
	if e == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(TypeText(e.Type))
	b.WriteByte('{')
	for i, v := range e.Values {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(ExprText(v))
	}
	b.WriteByte('}')
	return b.String()
}

func (e *ArrayLit) copyExpr(substitutions map[string]Expr, newID func(NodeID, bool) NodeID, fromArgument bool) Expr {
	if e == nil {
		return nil
	}
	id := newID(e.ID(), fromArgument)
	values := make([]Expr, len(e.Values))
	for i, v := range e.Values {
		values[i] = v.copyExpr(substitutions, newID, fromArgument)
	}
	return &ArrayLit{NodeIDHolder: NodeIDHolder{NodeID: id}, Type: cloneTypeExpr(e.Type, newID, fromArgument), Values: values, InferredLen: e.InferredLen, Location: e.Location}
}

type BadExpr struct {
	NodeIDHolder
	Location *source.Location
}

func (*BadExpr) exprNode()               {}
func (*BadExpr) forEachChild(func(Node)) {}
func (e *BadExpr) loc() *source.Location { return e.Location }
func (e *BadExpr) exprText() string      { return "<bad-expr>" }

func (e *BadExpr) copyExpr(substitutions map[string]Expr, newID func(NodeID, bool) NodeID, fromArgument bool) Expr {
	if e == nil {
		return nil
	}
	id := newID(e.ID(), fromArgument)
	return &BadExpr{NodeIDHolder: NodeIDHolder{NodeID: id}, Location: e.Location}
}

type NumberLit struct {
	NodeIDHolder
	Value        string
	ExplicitType string
	Location     *source.Location
}

func (*NumberLit) exprNode()               {}
func (*NumberLit) forEachChild(func(Node)) {}
func (e *NumberLit) loc() *source.Location { return e.Location }
func (e *NumberLit) exprText() string {
	if e == nil {
		return ""
	}
	return e.Value + e.ExplicitType
}

func (e *NumberLit) copyExpr(substitutions map[string]Expr, newID func(NodeID, bool) NodeID, fromArgument bool) Expr {
	if e == nil {
		return nil
	}
	id := newID(e.ID(), fromArgument)
	return &NumberLit{NodeIDHolder: NodeIDHolder{NodeID: id}, Value: e.Value, ExplicitType: e.ExplicitType, Location: e.Location}
}

type StringLit struct {
	NodeIDHolder
	Value    string
	CString  bool
	Location *source.Location
}

func (*StringLit) exprNode()               {}
func (*StringLit) forEachChild(func(Node)) {}
func (e *StringLit) loc() *source.Location { return e.Location }
func (e *StringLit) exprText() string {
	if e == nil {
		return ""
	}
	if e.CString {
		return "c" + strconv.Quote(e.Value)
	}
	return strconv.Quote(e.Value)
}

func (e *StringLit) copyExpr(substitutions map[string]Expr, newID func(NodeID, bool) NodeID, fromArgument bool) Expr {
	if e == nil {
		return nil
	}
	id := newID(e.ID(), fromArgument)
	return &StringLit{NodeIDHolder: NodeIDHolder{NodeID: id}, Value: e.Value, CString: e.CString, Location: e.Location}
}

type ByteLit struct {
	NodeIDHolder
	Value    string
	Location *source.Location
}

func (*ByteLit) exprNode()               {}
func (*ByteLit) forEachChild(func(Node)) {}
func (e *ByteLit) loc() *source.Location { return e.Location }
func (e *ByteLit) exprText() string {
	if e == nil {
		return ""
	}
	return "b'" + e.Value + "'"
}

func (e *ByteLit) copyExpr(substitutions map[string]Expr, newID func(NodeID, bool) NodeID, fromArgument bool) Expr {
	if e == nil {
		return nil
	}
	id := newID(e.ID(), fromArgument)
	return &ByteLit{NodeIDHolder: NodeIDHolder{NodeID: id}, Value: e.Value, Location: e.Location}
}

type CharLit struct {
	NodeIDHolder
	Value    string
	Location *source.Location
}

func (*CharLit) exprNode()               {}
func (*CharLit) forEachChild(func(Node)) {}
func (e *CharLit) loc() *source.Location { return e.Location }
func (e *CharLit) exprText() string {
	if e == nil {
		return ""
	}
	return "'" + e.Value + "'"
}

func (e *CharLit) copyExpr(substitutions map[string]Expr, newID func(NodeID, bool) NodeID, fromArgument bool) Expr {
	if e == nil {
		return nil
	}
	id := newID(e.ID(), fromArgument)
	return &CharLit{NodeIDHolder: NodeIDHolder{NodeID: id}, Value: e.Value, Location: e.Location}
}

type BoolLit struct {
	NodeIDHolder
	Value    bool
	Location *source.Location
}

func (*BoolLit) exprNode()               {}
func (*BoolLit) forEachChild(func(Node)) {}
func (e *BoolLit) loc() *source.Location { return e.Location }
func (e *BoolLit) exprText() string {
	if e == nil {
		return ""
	}
	if e.Value {
		return "true"
	}
	return "false"
}

func (e *BoolLit) copyExpr(substitutions map[string]Expr, newID func(NodeID, bool) NodeID, fromArgument bool) Expr {
	if e == nil {
		return nil
	}
	id := newID(e.ID(), fromArgument)
	return &BoolLit{NodeIDHolder: NodeIDHolder{NodeID: id}, Value: e.Value, Location: e.Location}
}

type NoneLit struct {
	NodeIDHolder
	Location *source.Location
}

func (*NoneLit) exprNode()               {}
func (*NoneLit) forEachChild(func(Node)) {}
func (e *NoneLit) loc() *source.Location { return e.Location }
func (e *NoneLit) exprText() string      { return "none" }

func (e *NoneLit) copyExpr(substitutions map[string]Expr, newID func(NodeID, bool) NodeID, fromArgument bool) Expr {
	if e == nil {
		return nil
	}
	id := newID(e.ID(), fromArgument)
	return &NoneLit{NodeIDHolder: NodeIDHolder{NodeID: id}, Location: e.Location}
}

type AddressMode uint8

const (
	AddressRaw AddressMode = iota
	AddressShared
	AddressMutable
)

type AddressExpr struct {
	NodeIDHolder
	Mode     AddressMode
	Expr     Expr
	Location *source.Location
}

func (*AddressExpr) exprNode()                       {}
func (e *AddressExpr) forEachChild(visit func(Node)) { visit(e.Expr) }
func (e *AddressExpr) loc() *source.Location         { return e.Location }
func (e *AddressExpr) exprText() string {
	if e == nil {
		return ""
	}
	prefix := "@"
	switch e.Mode {
	case AddressShared:
		prefix = "&"
	case AddressMutable:
		prefix = "&mut "
	}
	return prefix + ExprText(e.Expr)
}

func (e *AddressExpr) copyExpr(substitutions map[string]Expr, newID func(NodeID, bool) NodeID, fromArgument bool) Expr {
	if e == nil {
		return nil
	}
	id := newID(e.ID(), fromArgument)
	return &AddressExpr{NodeIDHolder: NodeIDHolder{NodeID: id}, Mode: e.Mode, Expr: e.Expr.copyExpr(substitutions, newID, fromArgument), Location: e.Location}
}

type UnaryExpr struct {
	NodeIDHolder
	Op       string
	Expr     Expr
	Location *source.Location
}

func (*UnaryExpr) exprNode()                       {}
func (e *UnaryExpr) forEachChild(visit func(Node)) { visit(e.Expr) }
func (e *UnaryExpr) loc() *source.Location         { return e.Location }
func (e *UnaryExpr) exprText() string {
	if e == nil {
		return ""
	}
	return "(" + e.Op + ExprText(e.Expr) + ")"
}

func (e *UnaryExpr) copyExpr(substitutions map[string]Expr, newID func(NodeID, bool) NodeID, fromArgument bool) Expr {
	if e == nil {
		return nil
	}
	id := newID(e.ID(), fromArgument)
	return &UnaryExpr{NodeIDHolder: NodeIDHolder{NodeID: id}, Op: e.Op, Expr: e.Expr.copyExpr(substitutions, newID, fromArgument), Location: e.Location}
}

type BinaryExpr struct {
	NodeIDHolder
	Left     Expr
	Op       string
	Right    Expr
	Location *source.Location
}

type IsExpr struct {
	NodeIDHolder
	Value    Expr
	Case     *ScopeResolution
	Location *source.Location
}

func (*IsExpr) exprNode() {}
func (e *IsExpr) forEachChild(visit func(Node)) {
	visit(e.Value)
	visit(e.Case)
}
func (e *IsExpr) loc() *source.Location { return e.Location }
func (e *IsExpr) exprText() string {
	if e == nil {
		return ""
	}
	return "(" + ExprText(e.Value) + " is " + ExprText(e.Case) + ")"
}

func (e *IsExpr) copyExpr(substitutions map[string]Expr, newID func(NodeID, bool) NodeID, fromArgument bool) Expr {
	if e == nil {
		return nil
	}
	return &IsExpr{
		NodeIDHolder: NodeIDHolder{NodeID: newID(e.ID(), fromArgument)},
		Value:        e.Value.copyExpr(substitutions, newID, fromArgument),
		Case:         e.Case.copyExpr(substitutions, newID, fromArgument).(*ScopeResolution),
		Location:     e.Location,
	}
}

func (*BinaryExpr) exprNode() {}
func (e *BinaryExpr) forEachChild(visit func(Node)) {
	visit(e.Left)
	visit(e.Right)
}
func (e *BinaryExpr) loc() *source.Location { return e.Location }
func (e *BinaryExpr) exprText() string {
	if e == nil {
		return ""
	}
	return "(" + ExprText(e.Left) + " " + e.Op + " " + ExprText(e.Right) + ")"
}

func (e *BinaryExpr) copyExpr(substitutions map[string]Expr, newID func(NodeID, bool) NodeID, fromArgument bool) Expr {
	if e == nil {
		return nil
	}
	id := newID(e.ID(), fromArgument)
	return &BinaryExpr{NodeIDHolder: NodeIDHolder{NodeID: id}, Left: e.Left.copyExpr(substitutions, newID, fromArgument), Op: e.Op, Right: e.Right.copyExpr(substitutions, newID, fromArgument), Location: e.Location}
}

type CallExpr struct {
	NodeIDHolder
	Callee   Expr
	Args     []Expr
	Piped    bool
	Location *source.Location
}

func (*CallExpr) exprNode() {}
func (e *CallExpr) forEachChild(visit func(Node)) {
	visit(e.Callee)
	for _, arg := range e.Args {
		visit(arg)
	}
}
func (e *CallExpr) loc() *source.Location { return e.Location }
func (e *CallExpr) exprText() string {
	if e == nil {
		return ""
	}
	var b strings.Builder
	if e.Piped && len(e.Args) > 0 {
		b.WriteString(ExprText(e.Args[0]))
		b.WriteString(" |> ")
	}
	b.WriteString(ExprText(e.Callee))
	b.WriteByte('(')
	start := 0
	if e.Piped {
		start = 1
	}
	for i, arg := range e.Args[start:] {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(ExprText(arg))
	}
	b.WriteByte(')')
	return b.String()
}

func (e *CallExpr) copyExpr(substitutions map[string]Expr, newID func(NodeID, bool) NodeID, fromArgument bool) Expr {
	if e == nil {
		return nil
	}
	id := newID(e.ID(), fromArgument)
	args := make([]Expr, len(e.Args))
	for i, arg := range e.Args {
		args[i] = arg.copyExpr(substitutions, newID, fromArgument)
	}
	return &CallExpr{NodeIDHolder: NodeIDHolder{NodeID: id}, Callee: e.Callee.copyExpr(substitutions, newID, fromArgument), Args: args, Piped: e.Piped, Location: e.Location}
}

type FreeExpr struct {
	NodeIDHolder
	Expr     Expr
	Location *source.Location
}

func (*FreeExpr) exprNode()                       {}
func (e *FreeExpr) forEachChild(visit func(Node)) { visit(e.Expr) }
func (e *FreeExpr) loc() *source.Location         { return e.Location }
func (e *FreeExpr) exprText() string {
	if e == nil {
		return ""
	}
	return "free(" + ExprText(e.Expr) + ")"
}

func (e *FreeExpr) copyExpr(substitutions map[string]Expr, newID func(NodeID, bool) NodeID, fromArgument bool) Expr {
	if e == nil {
		return nil
	}
	id := newID(e.ID(), fromArgument)
	return &FreeExpr{NodeIDHolder: NodeIDHolder{NodeID: id}, Expr: e.Expr.copyExpr(substitutions, newID, fromArgument), Location: e.Location}
}

type PrintExpr struct {
	NodeIDHolder
	Expr     Expr
	Newline  bool
	Location *source.Location
}

func (*PrintExpr) exprNode()                       {}
func (e *PrintExpr) forEachChild(visit func(Node)) { visit(e.Expr) }
func (e *PrintExpr) loc() *source.Location         { return e.Location }
func (e *PrintExpr) exprText() string {
	if e == nil {
		return ""
	}
	name := "print"
	if e.Newline {
		name = "println"
	}
	return name + "(" + ExprText(e.Expr) + ")"
}

func (e *PrintExpr) copyExpr(substitutions map[string]Expr, newID func(NodeID, bool) NodeID, fromArgument bool) Expr {
	if e == nil {
		return nil
	}
	id := newID(e.ID(), fromArgument)
	return &PrintExpr{NodeIDHolder: NodeIDHolder{NodeID: id}, Expr: e.Expr.copyExpr(substitutions, newID, fromArgument), Newline: e.Newline, Location: e.Location}
}

type AsExpr struct {
	NodeIDHolder
	Expr     Expr
	TypeExpr TypeExpr
	Location *source.Location
}

func (*AsExpr) exprNode() {}
func (e *AsExpr) forEachChild(visit func(Node)) {
	visit(e.Expr)
	visit(e.TypeExpr)
}
func (e *AsExpr) loc() *source.Location { return e.Location }
func (e *AsExpr) exprText() string {
	if e == nil {
		return ""
	}
	return "(" + ExprText(e.Expr) + " as " + TypeText(e.TypeExpr) + ")"
}

func (e *AsExpr) copyExpr(substitutions map[string]Expr, newID func(NodeID, bool) NodeID, fromArgument bool) Expr {
	if e == nil {
		return nil
	}
	id := newID(e.ID(), fromArgument)
	return &AsExpr{NodeIDHolder: NodeIDHolder{NodeID: id}, Expr: e.Expr.copyExpr(substitutions, newID, fromArgument), TypeExpr: cloneTypeExpr(e.TypeExpr, newID, fromArgument), Location: e.Location}
}

func identText(ident *Ident) string {
	if ident == nil {
		return ""
	}
	return ident.Name
}

// ExprText is a nil-safe wrapper around Expr.exprText().
func ExprText(expr Expr) string {
	if expr == nil {
		return ""
	}
	return expr.exprText()
}
