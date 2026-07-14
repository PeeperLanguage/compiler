package mir

import (
	"fmt"
	"strings"

	"compiler/internal/ir"
	"compiler/internal/semantics/symbols"
	"compiler/internal/source"
)

type Module struct {
	FilePath        string
	Name            string
	StaticData      []*StaticEntry
	InterfaceThunks []*InterfaceThunk
	Funcs           []*Function
}

type InterfaceThunk struct {
	Name     string
	SlotType string
	FuncName string
	FuncType string
	DataType string
}

type StaticEntry struct {
	Name  string
	Type  string
	Value string
	Align int
}

func (m *Module) InternStatic(value string, elemType string, align int) string {
	for _, entry := range m.StaticData {
		if entry.Value == value && entry.Type == elemType && entry.Align == align {
			return entry.Name
		}
	}
	name := fmt.Sprintf("@.data.%d", len(m.StaticData))
	m.StaticData = append(m.StaticData, &StaticEntry{
		Name:  name,
		Type:  elemType,
		Value: value,
		Align: align,
	})
	return name
}

type Function struct {
	Name       string
	Params     []ir.Param
	ReturnType string
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
}

type Terminator interface {
	Text() string
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
	Location *source.Location
}

type Drop struct {
	Value    ValueRef
	Location *source.Location
}

type ValueExpr interface {
	valueExprNode()
	Text() string
}

type ValueRef interface {
	valueRefNode()
	Text() string
}

type RefConst struct {
	Value    string
	Type     string
	Location *source.Location
}

type RefName struct {
	Name     string
	Type     string
	Location *source.Location
}

type Unary struct {
	Op       string
	Arg      ValueRef
	Type     string
	Location *source.Location
}

type Binary struct {
	Op       string
	Left     ValueRef
	Right    ValueRef
	Type     string
	Location *source.Location
}

type Move struct {
	Src      ValueRef
	Type     string
	Location *source.Location
}

type Cast struct {
	Arg      ValueRef
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
	Index      ValueRef
	Type       string
	Location   *source.Location
}

type Place struct {
	Root        ValueRef
	Projections []PlaceProjection
	Type        string
	Location    *source.Location
}

type AddrOf struct {
	Place    *Place
	Type     string
	Location *source.Location
}

type SliceView struct {
	Source       *Place
	Start        ValueRef
	End          ValueRef
	EndExclusive bool
	Type         string
	Location     *source.Location
}

type Load struct {
	Place    *Place
	Type     string
	Location *source.Location
}

type Field struct {
	Base     ValueRef
	Index    int
	Type     string
	Location *source.Location
}

type StructLit struct {
	Fields   []ValueRef
	Type     string
	Location *source.Location
}

type ArrayLit struct {
	Values   []ValueRef
	Type     string
	Location *source.Location
}

type DynamicArrayAlloc struct {
	Length   int
	Type     string
	Location *source.Location
}

type DynamicArrayOp struct {
	Op       symbols.CompilerOp
	Array    ValueRef
	Length   ValueRef
	Value    ValueRef
	Type     string
	Location *source.Location
}

type ZeroValue struct {
	Type     string
	Location *source.Location
}

type OptionalSome struct {
	Value    ValueRef
	Type     string
	Location *source.Location
}

type InterfaceMake struct {
	Value    ValueRef
	DataType string
	Slots    []ValueRef
	Type     string
	Location *source.Location
}

type InterfaceCall struct {
	Base     ValueRef
	Slot     int
	Args     []ValueRef
	Consumes bool
	Type     string
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
func (*Field) valueExprNode()             {}
func (*StructLit) valueExprNode()         {}
func (*ArrayLit) valueExprNode()          {}
func (*DynamicArrayAlloc) valueExprNode() {}
func (*DynamicArrayOp) valueExprNode()    {}
func (*ZeroValue) valueExprNode()         {}
func (*OptionalSome) valueExprNode()      {}
func (*InterfaceMake) valueExprNode()     {}
func (*InterfaceCall) valueExprNode()     {}
func (*RefConst) valueRefNode()           {}
func (*RefName) valueRefNode()            {}

func (r *RefConst) Text() string { return r.Value }
func (r *RefName) Text() string  { return r.Name }
func (v *Move) Text() string     { return v.Src.Text() }
func (v *Unary) Text() string    { return fmt.Sprintf("%s %s", v.Op, v.Arg.Text()) }
func (v *Binary) Text() string   { return fmt.Sprintf("%s %s, %s", v.Op, v.Left.Text(), v.Right.Text()) }
func (v *Cast) Text() string     { return fmt.Sprintf("cast %s to %s", v.Arg.Text(), v.Type) }
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
		}
	}
	return b.String()
}

func (v *AddrOf) Text() string { return fmt.Sprintf("addr %s", v.Place.Text()) }
func (v *SliceView) Text() string {
	return fmt.Sprintf("view %s", v.Source.Text())
}
func (v *Load) Text() string  { return fmt.Sprintf("load %s", v.Place.Text()) }
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

func (v *ZeroValue) Text() string {
	if v == nil || v.Type == "" {
		return "zero"
	}
	return "zero(" + v.Type + ")"
}
func (v *OptionalSome) Text() string {
	if v == nil || v.Value == nil {
		return "some(<nil>)"
	}
	return "some(" + v.Value.Text() + ")"
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

func InstrLocation(instr Instr) *source.Location {
	switch node := instr.(type) {
	case *Assign:
		return node.Location
	case *Store:
		return node.Location
	case *Print:
		return node.Location
	case *Drop:
		return node.Location
	case *Call:
		return node.Location
	case *InterfaceCall:
		return node.Location
	default:
		return nil
	}
}

func TerminatorLocation(term Terminator) *source.Location {
	switch node := term.(type) {
	case *Ret:
		return node.Location
	case *Branch:
		return node.Location
	case *Jump:
		return node.Location
	default:
		return nil
	}
}

func ValueExprLocation(expr ValueExpr) *source.Location {
	switch node := expr.(type) {
	case *Unary:
		return node.Location
	case *Binary:
		return node.Location
	case *Move:
		return node.Location
	case *Cast:
		return node.Location
	case *AddrOf:
		return node.Location
	case *SliceView:
		return node.Location
	case *Load:
		return node.Location
	case *Field:
		return node.Location
	case *StructLit:
		return node.Location
	case *ArrayLit:
		return node.Location
	case *DynamicArrayAlloc:
		return node.Location
	case *DynamicArrayOp:
		return node.Location
	case *ZeroValue:
		return node.Location
	case *OptionalSome:
		return node.Location
	case *InterfaceMake:
		return node.Location
	case *InterfaceCall:
		return node.Location
	case *Call:
		return node.Location
	default:
		return nil
	}
}

func ValueRefLocation(ref ValueRef) *source.Location {
	switch node := ref.(type) {
	case *RefConst:
		return node.Location
	case *RefName:
		return node.Location
	default:
		return nil
	}
}

// Call represents a function call in MIR
type Call struct {
	Callee   ValueRef
	Args     []ValueRef
	Type     string
	Location *source.Location
}

func (c *Call) valueExprNode() {}
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
		fmt.Fprintf(&b, "%s = constant %s %q, align %d\n", data.Name, data.Type, data.Value, data.Align)
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
			b.WriteString(ir.SignatureText(fn.Params, fn.ReturnType))
			b.WriteString("\n")
		} else {
			b.WriteString("fn ")
			b.WriteString(fn.Name)
			b.WriteString(ir.SignatureText(fn.Params, fn.ReturnType))
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
