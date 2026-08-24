package mir

import (
	"fmt"
	"strings"

	"compiler/internal/constvalue"
	"compiler/internal/ir"
	"compiler/internal/semantics/symbols"
	"compiler/internal/source"
)

type Module struct {
	FilePath        string
	Name            string
	Types           *ir.TypeTable
	StaticData      []*StaticEntry
	InterfaceThunks []*InterfaceThunk
	Funcs           []*Function
}

type InterfaceThunk struct {
	Name     string
	SlotType ir.TypeID
	FuncName string
	FuncType ir.TypeID
	DataType ir.TypeID
}

type StaticEntry struct {
	Name string
	Type ir.TypeID
	// Bytes and Align describe raw byte storage when Constant is nil. Typed
	// constants use target-natural ABI alignment selected by LLVM.
	Bytes    string
	Constant constvalue.Value
	Align    int
}

func (m *Module) InternString(value string, align int) string {
	for _, entry := range m.StaticData {
		if entry.Constant == nil && entry.Bytes == value && entry.Align == align {
			return entry.Name
		}
	}
	name := fmt.Sprintf("@.data.%d", len(m.StaticData))
	m.StaticData = append(m.StaticData, &StaticEntry{
		Name:  name,
		Bytes: value,
		Align: align,
	})
	return name
}

type Function struct {
	Name       string
	Params     []ir.Param
	ReturnType ir.TypeID
	EntryID    int
	Blocks     []*Block
	Location   *source.Location
}

type Block struct {
	ID     int
	Instrs []Instr
	Term   Terminator
}

type Instr interface {
	Text() string
	SourceLocation() *source.Location
}

type Terminator interface {
	Text() string
	SourceLocation() *source.Location
}

type Assign struct {
	Name     string
	Value    ValueExpr
	Location *source.Location
}

type Jump struct {
	TargetID int
	Location *source.Location
}

type Branch struct {
	Cond     ValueRef
	ThenID   int
	ElseID   int
	Location *source.Location
}

type VariantTarget struct {
	Case     int
	TargetID int
}

type SwitchVariant struct {
	Value    ValueRef
	Targets  []VariantTarget
	Location *source.Location
}

type Ret struct {
	Value    ValueRef
	Location *source.Location
}

type Store struct {
	Place    *Place
	Value    ValueRef
	Location *source.Location
}

type Print struct {
	Value    ValueRef
	Newline  bool
	Location *source.Location
}

type Drop struct {
	Value    ValueRef
	Location *source.Location
}

type ValueExpr interface {
	valueExprNode()
	Text() string
	SourceLocation() *source.Location
}

type ValueRef interface {
	valueRefNode()
	Text() string
	SourceLocation() *source.Location
}

type RefConst struct {
	Value    string
	Type     ir.TypeID
	Location *source.Location
}

type RefName struct {
	Name     string
	Type     ir.TypeID
	Location *source.Location
}

type StringLiteral struct {
	Name     string
	Length   int
	Type     ir.TypeID
	Location *source.Location
}

type Unary struct {
	Op       string
	Arg      ValueRef
	Type     ir.TypeID
	Location *source.Location
}

type Binary struct {
	Op       string
	Left     ValueRef
	Right    ValueRef
	Type     ir.TypeID
	Location *source.Location
}

type Move struct {
	Src      ValueRef
	Type     ir.TypeID
	Location *source.Location
}

type Cast struct {
	Arg      ValueRef
	Type     ir.TypeID
	Location *source.Location
}

type PlaceProjectionKind uint8

const (
	PlaceProjectionDeref PlaceProjectionKind = iota
	PlaceProjectionField
	PlaceProjectionIndex
	PlaceProjectionVariantPayload
)

type PlaceProjection struct {
	Kind       PlaceProjectionKind
	FieldIndex int
	Index      ValueRef
	Case       int
	Type       ir.TypeID
	Location   *source.Location
}

type Place struct {
	Root        ValueRef
	Projections []PlaceProjection
	Type        ir.TypeID
	Location    *source.Location
}

type AddrOf struct {
	Place    *Place
	Type     ir.TypeID
	Location *source.Location
}

type SliceView struct {
	Source       *Place
	Start        ValueRef
	End          ValueRef
	EndExclusive bool
	Type         ir.TypeID
	Location     *source.Location
}

type Load struct {
	Place    *Place
	Type     ir.TypeID
	Location *source.Location
}

type Len struct {
	Value    ValueRef
	Type     ir.TypeID
	Location *source.Location
}

type StringChars struct {
	Value    ValueRef
	Type     ir.TypeID
	Location *source.Location
}

type Field struct {
	Base     ValueRef
	Index    int
	Type     ir.TypeID
	Location *source.Location
}

type StructLit struct {
	Fields   []ValueRef
	Type     ir.TypeID
	Location *source.Location
}

type ArrayLit struct {
	Values   []ValueRef
	Type     ir.TypeID
	Location *source.Location
}

type DynamicArrayAlloc struct {
	Length    int
	Allocator ValueRef
	Type      ir.TypeID
	Location  *source.Location
}

type DynamicArrayOp struct {
	Op        symbols.CompilerOp
	Array     ValueRef
	Length    ValueRef
	Value     ValueRef
	ArrayType ir.TypeID
	Location  *source.Location
}

type Alloc struct {
	Value     ValueRef
	Allocator ValueRef
	Type      ir.TypeID
	Location  *source.Location
}

type ZeroValue struct {
	Type     ir.TypeID
	Location *source.Location
}

type VariantMake struct {
	Case     int
	Payload  ValueRef
	Type     ir.TypeID
	Location *source.Location
}

type VariantIs struct {
	Value    ValueRef
	Case     int
	Type     ir.TypeID
	Location *source.Location
}

type InterfaceMake struct {
	Value    ValueRef
	DataType ir.TypeID
	Slots    []ValueRef
	Type     ir.TypeID
	Location *source.Location
}

type InterfaceCall struct {
	Base     ValueRef
	Slot     int
	Args     []ValueRef
	Consumes bool
	Type     ir.TypeID
	Location *source.Location
}

func (i *Assign) Text() string {
	return fmt.Sprintf("%s = %s", i.Name, i.Value.Text())
}

func (i *Store) Text() string {
	return fmt.Sprintf("store %s, %s", i.Place.Text(), i.Value.Text())
}

func (i *Print) Text() string {
	return "print " + i.Value.Text()
}

func (i *Drop) Text() string {
	return "drop " + i.Value.Text()
}

func (i *Jump) Text() string {
	return fmt.Sprintf("jmp b%d", i.TargetID)
}

func (i *Branch) Text() string {
	return fmt.Sprintf("br %s, b%d, b%d", i.Cond.Text(), i.ThenID, i.ElseID)
}

func (i *SwitchVariant) Text() string {
	var b strings.Builder
	b.WriteString("switch-variant ")
	b.WriteString(i.Value.Text())
	for _, target := range i.Targets {
		fmt.Fprintf(&b, ", case %d: b%d", target.Case, target.TargetID)
	}
	return b.String()
}

func (i *Ret) Text() string {
	if i == nil || i.Value == nil {
		return "ret"
	}
	return "ret " + i.Value.Text()
}

func (*Unary) valueExprNode()             {}
func (*Binary) valueExprNode()            {}
func (*Move) valueExprNode()              {}
func (*Cast) valueExprNode()              {}
func (*AddrOf) valueExprNode()            {}
func (*SliceView) valueExprNode()         {}
func (*Load) valueExprNode()              {}
func (*Len) valueExprNode()               {}
func (*StringChars) valueExprNode()       {}
func (*Field) valueExprNode()             {}
func (*StructLit) valueExprNode()         {}
func (*ArrayLit) valueExprNode()          {}
func (*DynamicArrayAlloc) valueExprNode() {}
func (*Alloc) valueExprNode()             {}
func (*ZeroValue) valueExprNode()         {}
func (*VariantMake) valueExprNode()       {}
func (*VariantIs) valueExprNode()         {}
func (*InterfaceMake) valueExprNode()     {}
func (*InterfaceCall) valueExprNode()     {}
func (*StringLiteral) valueExprNode()     {}
func (*RefConst) valueRefNode()           {}
func (*RefName) valueRefNode()            {}

func (i *Assign) SourceLocation() *source.Location            { return i.Location }
func (i *Store) SourceLocation() *source.Location             { return i.Location }
func (i *Print) SourceLocation() *source.Location             { return i.Location }
func (i *Drop) SourceLocation() *source.Location              { return i.Location }
func (i *Jump) SourceLocation() *source.Location              { return i.Location }
func (i *Branch) SourceLocation() *source.Location            { return i.Location }
func (i *SwitchVariant) SourceLocation() *source.Location     { return i.Location }
func (i *Ret) SourceLocation() *source.Location               { return i.Location }
func (r *RefConst) SourceLocation() *source.Location          { return r.Location }
func (r *RefName) SourceLocation() *source.Location           { return r.Location }
func (v *StringLiteral) SourceLocation() *source.Location     { return v.Location }
func (v *Unary) SourceLocation() *source.Location             { return v.Location }
func (v *Binary) SourceLocation() *source.Location            { return v.Location }
func (v *Move) SourceLocation() *source.Location              { return v.Location }
func (v *Cast) SourceLocation() *source.Location              { return v.Location }
func (v *AddrOf) SourceLocation() *source.Location            { return v.Location }
func (v *SliceView) SourceLocation() *source.Location         { return v.Location }
func (v *Load) SourceLocation() *source.Location              { return v.Location }
func (v *Len) SourceLocation() *source.Location               { return v.Location }
func (v *StringChars) SourceLocation() *source.Location       { return v.Location }
func (v *Field) SourceLocation() *source.Location             { return v.Location }
func (v *StructLit) SourceLocation() *source.Location         { return v.Location }
func (v *ArrayLit) SourceLocation() *source.Location          { return v.Location }
func (v *DynamicArrayAlloc) SourceLocation() *source.Location { return v.Location }
func (v *DynamicArrayOp) SourceLocation() *source.Location    { return v.Location }
func (v *Alloc) SourceLocation() *source.Location             { return v.Location }
func (v *ZeroValue) SourceLocation() *source.Location         { return v.Location }
func (v *VariantMake) SourceLocation() *source.Location       { return v.Location }
func (v *VariantIs) SourceLocation() *source.Location         { return v.Location }
func (v *InterfaceMake) SourceLocation() *source.Location     { return v.Location }
func (v *InterfaceCall) SourceLocation() *source.Location     { return v.Location }

func (r *RefConst) Text() string { return r.Value }
func (r *RefName) Text() string  { return r.Name }
func (v *StringLiteral) Text() string {
	if v == nil {
		return "string-literal"
	}
	return v.Name
}
func (v *Move) Text() string   { return v.Src.Text() }
func (v *Unary) Text() string  { return fmt.Sprintf("%s %s", v.Op, v.Arg.Text()) }
func (v *Binary) Text() string { return fmt.Sprintf("%s %s, %s", v.Op, v.Left.Text(), v.Right.Text()) }
func (v *Cast) Text() string   { return fmt.Sprintf("cast %s to type#%d", v.Arg.Text(), v.Type) }
func (p *Place) Text() string {
	if p == nil || p.Root == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(p.Root.Text())
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
				b.WriteString(projection.Index.Text())
			}
			b.WriteString("]")
		case PlaceProjectionVariantPayload:
			fmt.Fprintf(&b, ".variant%d", projection.Case)
		}
	}
	return b.String()
}

func (v *AddrOf) Text() string { return fmt.Sprintf("addr %s", v.Place.Text()) }
func (v *SliceView) Text() string {
	return fmt.Sprintf("view %s", v.Source.Text())
}
func (v *Load) Text() string { return fmt.Sprintf("load %s", v.Place.Text()) }
func (v *Len) Text() string  { return fmt.Sprintf("len %s", v.Value.Text()) }
func (v *StringChars) Text() string {
	return fmt.Sprintf("chars %s", v.Value.Text())
}
func (v *Field) Text() string { return fmt.Sprintf("field %s, %d", v.Base.Text(), v.Index) }

func (v *StructLit) Text() string {
	var b strings.Builder
	b.WriteString("struct(")
	for i, field := range v.Fields {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(field.Text())
	}
	b.WriteString(")")
	return b.String()
}

func (v *ArrayLit) Text() string {
	var b strings.Builder
	b.WriteString("array(")
	for i, value := range v.Values {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(value.Text())
	}
	b.WriteString(")")
	return b.String()
}

func (v *DynamicArrayAlloc) Text() string {
	if v == nil {
		return ""
	}
	if v.Allocator != nil {
		return fmt.Sprintf("allocarray %d, %s", v.Length, v.Allocator.Text())
	}
	return fmt.Sprintf("allocarray %d", v.Length)
}

func (v *DynamicArrayOp) Text() string {
	if v == nil {
		return ""
	}
	args := []string{v.Array.Text()}
	if v.Length != nil {
		args = append(args, v.Length.Text())
	}
	if v.Value != nil {
		args = append(args, v.Value.Text())
	}
	return string(v.Op) + " " + strings.Join(args, ", ")
}

func (v *Alloc) Text() string {
	if v == nil {
		return ""
	}
	if v.Allocator != nil {
		return "alloc " + v.Value.Text() + ", " + v.Allocator.Text()
	}
	return "alloc " + v.Value.Text()
}

func (v *ZeroValue) Text() string {
	if v == nil || v.Type == ir.InvalidType {
		return "zero"
	}
	return "zero"
}
func (v *VariantMake) Text() string {
	if v == nil {
		return "variant(<nil>)"
	}
	if v.Payload == nil {
		return fmt.Sprintf("variant %d", v.Case)
	}
	return fmt.Sprintf("variant %d, %s", v.Case, v.Payload.Text())
}
func (v *VariantIs) Text() string {
	if v == nil || v.Value == nil {
		return "is-variant(<nil>)"
	}
	return fmt.Sprintf("is-variant %s, %d", v.Value.Text(), v.Case)
}

func (v *InterfaceMake) Text() string {
	if v == nil {
		return "iface()"
	}
	return "iface(" + v.Value.Text() + ")"
}

func (v *InterfaceCall) Text() string {
	if v == nil {
		return "ifacecall()"
	}
	var b strings.Builder
	b.WriteString("ifacecall ")
	b.WriteString(v.Base.Text())
	b.WriteString("(")
	for i, arg := range v.Args {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(arg.Text())
	}
	b.WriteString(")")
	return b.String()
}

// Call represents a function call in MIR
type Call struct {
	Callee   ValueRef
	Args     []ValueRef
	Type     ir.TypeID
	Location *source.Location
}

func (c *Call) valueExprNode()                   {}
func (c *Call) SourceLocation() *source.Location { return c.Location }
func (c *Call) Text() string {
	var b strings.Builder
	b.WriteString("call ")
	b.WriteString(c.Callee.Text())
	b.WriteString("(")
	for i, arg := range c.Args {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(arg.Text())
	}
	b.WriteString(")")
	return b.String()
}

func (m *Module) Text() string {
	if m == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("; mir module ")
	b.WriteString(m.Name)
	b.WriteString("\n")
	for _, data := range m.StaticData {
		if data.Constant != nil {
			fmt.Fprintf(&b, "%s = constant type#%d %q\n", data.Name, data.Type, data.Constant.TypeText())
			continue
		}
		fmt.Fprintf(&b, "%s = constant type#%d %q, align %d\n", data.Name, data.Type, data.Bytes, data.Align)
	}
	if len(m.StaticData) > 0 {
		b.WriteString("\n")
	}
	if len(m.Funcs) == 0 {
		return b.String()
	}
	for _, fn := range m.Funcs {
		if fn.Blocks == nil {
			b.WriteString("extern fn ")
			b.WriteString(fn.Name)
			b.WriteString(ir.SignatureText(m.Types, fn.Params, fn.ReturnType))
			b.WriteString("\n")
		} else {
			b.WriteString("fn ")
			b.WriteString(fn.Name)
			b.WriteString(ir.SignatureText(m.Types, fn.Params, fn.ReturnType))
			b.WriteString(" {\n")
			for _, block := range fn.Blocks {
				if block == nil {
					continue
				}
				b.WriteString("  b")
				fmt.Fprintf(&b, "%d", block.ID)
				b.WriteString(":\n")
				for _, instr := range block.Instrs {
					b.WriteString("    ")
					b.WriteString(instr.Text())
					b.WriteString("\n")
				}
				if block.Term != nil {
					b.WriteString("    ")
					b.WriteString(block.Term.Text())
					b.WriteString("\n")
				}
			}
			b.WriteString("}\n")
		}
	}
	return b.String()
}
