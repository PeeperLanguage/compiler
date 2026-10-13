package mir

import (
	"fmt"
	"strings"

	"compiler/internal/constvalue"
	"compiler/internal/ir"
	"compiler/internal/ir/cfg"
	"compiler/internal/moduleid"
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
	Name          string
	InterfaceType ir.TypeID
	Slot          int
	SlotType      ir.TypeID
	FuncName      string
	FuncType      ir.TypeID
	DataType      ir.TypeID
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
	// Identity keys the cleanup plan's answers for this function.
	Identity moduleid.FunctionID
	// Locals lists the function's variables, parameters first, in declaration
	// order. A MIR name not listed here is a temporary.
	Locals []Local
}

// Local ties a MIR name to the variable it stores, so a drop names it by
// symbol and the debugger shows it by source name.
type Local struct {
	Name string
	// SourceName is empty for a variable with no name to show: one the
	// compiler made up, or a `_` parameter.
	SourceName string
	Symbol     symbols.SymbolID
	Type       ir.TypeID
	// Arg is the 1-based parameter position, or 0 for a local binding.
	Arg      int
	Location *source.Location
}

type Block struct {
	ID     int
	Instrs []Instr
	Term   Terminator
	// Sites says where each of the block's CFG sites starts in Instrs, in
	// site order. Instructions before the first start, such as a loop's
	// cursor steps, belong to the first site; the terminator to the last.
	Sites []SiteStart
}

type SiteStart struct {
	ID    cfg.SiteID
	Start int
}

// Instr and Terminator are sealed by their marker methods. Without them the two
// interfaces are structurally identical, so a terminator satisfies Instr and an
// instruction satisfies Terminator, and lowering can put either in the other's
// position with nothing to catch it until the backend panics. The markers also
// keep the set closed to this package, so the backend's exhaustive switches
// cannot be outflanked by a node declared elsewhere.
type Instr interface {
	instrNode()
	Text(types *ir.TypeTable) string
	SourceLocation() *source.Location
}

type Terminator interface {
	termNode()
	Text(types *ir.TypeTable) string
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
	Value          ValueRef
	AppendsNewline bool
	Location       *source.Location
}

type Drop struct {
	Value    ValueRef
	Location *source.Location
}

// DropPointKind names the source event a DropPoint stands for. Each kind
// matches one question of CleanupPlan.
type DropPointKind uint8

const (
	DropAfterSite DropPointKind = iota
	DropBeforeReturn
	DropBeforeAssign
	DropDiscarded
	DropProjectionBase
)

// DropPoint marks where lowering may drop source values. Lowering does not
// know the cleanup plan; ResolveDrops turns each point into the drops the plan
// decides, or removes it, before MIR is validated.
type DropPoint struct {
	Kind DropPointKind
	Site cfg.SiteID
	Node source.NodeID
	// Value is what a before-assign, discarded or projection-base drop drops.
	Value ValueRef
	// Place, for a before-assign drop through a projection, is loaded into
	// Temporary and the loaded value dropped.
	Place     *Place
	Temporary string
	Location  *source.Location
}

type ValueExpr interface {
	valueExprNode()
	TypeID() ir.TypeID
	Text(types *ir.TypeTable) string
	SourceLocation() *source.Location
}

type ValueRef interface {
	valueRefNode()
	TypeID() ir.TypeID
	Text(types *ir.TypeTable) string
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

type StringConcat struct {
	Left     ValueRef
	Right    ValueRef
	Type     ir.TypeID
	Location *source.Location
}

type Move struct {
	Src      ValueRef
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
	Source         *Place
	Start          ValueRef
	End            ValueRef
	IsEndExclusive bool
	Type           ir.TypeID
	Location       *source.Location
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

type StringFromBytes struct {
	Bytes     ValueRef
	Allocator ValueRef
	Type      ir.TypeID
	Location  *source.Location
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
	Base         ValueRef
	Slot         int
	SlotType     ir.TypeID
	Args         []ValueRef
	ConsumesBase bool
	Type         ir.TypeID
	Location     *source.Location
}

func (i *Assign) Text(types *ir.TypeTable) string {
	return fmt.Sprintf("%s = %s", i.Name, i.Value.Text(types))
}

func (i *Store) Text(types *ir.TypeTable) string {
	return fmt.Sprintf("store %s, %s", i.Place.Text(types), i.Value.Text(types))
}

func (i *Print) Text(types *ir.TypeTable) string {
	return "print " + i.Value.Text(types)
}

func (i *Drop) Text(types *ir.TypeTable) string {
	return "drop " + i.Value.Text(types)
}

func (i *DropPoint) Text(types *ir.TypeTable) string {
	return fmt.Sprintf("droppoint %d", i.Kind)
}

func (i *Jump) Text(types *ir.TypeTable) string {
	return fmt.Sprintf("jmp b%d", i.TargetID)
}

func (i *Branch) Text(types *ir.TypeTable) string {
	return fmt.Sprintf("br %s, b%d, b%d", i.Cond.Text(types), i.ThenID, i.ElseID)
}

func (i *SwitchVariant) Text(types *ir.TypeTable) string {
	var b strings.Builder
	b.WriteString("switch-variant ")
	b.WriteString(i.Value.Text(types))
	for _, target := range i.Targets {
		fmt.Fprintf(&b, ", case %d: b%d", target.Case, target.TargetID)
	}
	return b.String()
}

func (i *Ret) Text(types *ir.TypeTable) string {
	if i == nil || i.Value == nil {
		return "ret"
	}
	return "ret " + i.Value.Text(types)
}

func (*Assign) instrNode()         {}
func (*Store) instrNode()          {}
func (*Print) instrNode()          {}
func (*Drop) instrNode()           {}
func (*DropPoint) instrNode()      {}
func (*DynamicArrayOp) instrNode() {}
func (*Call) instrNode()           {}
func (*InterfaceCall) instrNode()  {}

func (*Jump) termNode()          {}
func (*Branch) termNode()        {}
func (*SwitchVariant) termNode() {}
func (*Ret) termNode()           {}

func (*Unary) valueExprNode()             {}
func (*Binary) valueExprNode()            {}
func (*StringConcat) valueExprNode()      {}
func (*Move) valueExprNode()              {}
func (*Cast) valueExprNode()              {}
func (*AddrOf) valueExprNode()            {}
func (*SliceView) valueExprNode()         {}
func (*Load) valueExprNode()              {}
func (*Len) valueExprNode()               {}
func (*StringChars) valueExprNode()       {}
func (*StringFromBytes) valueExprNode()   {}
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

func (r *RefConst) TypeID() ir.TypeID      { return r.Type }
func (r *RefName) TypeID() ir.TypeID       { return r.Type }
func (v *StringLiteral) TypeID() ir.TypeID { return v.Type }
func (v *Move) TypeID() ir.TypeID {
	if v == nil || v.Src == nil {
		return ir.InvalidType
	}
	return v.Src.TypeID()
}
func (v *Unary) TypeID() ir.TypeID             { return v.Type }
func (v *Binary) TypeID() ir.TypeID            { return v.Type }
func (v *StringConcat) TypeID() ir.TypeID      { return v.Type }
func (v *Cast) TypeID() ir.TypeID              { return v.Type }
func (v *AddrOf) TypeID() ir.TypeID            { return v.Type }
func (v *SliceView) TypeID() ir.TypeID         { return v.Type }
func (v *Load) TypeID() ir.TypeID              { return v.Type }
func (v *Len) TypeID() ir.TypeID               { return v.Type }
func (v *StringChars) TypeID() ir.TypeID       { return v.Type }
func (v *StringFromBytes) TypeID() ir.TypeID   { return v.Type }
func (v *Field) TypeID() ir.TypeID             { return v.Type }
func (v *StructLit) TypeID() ir.TypeID         { return v.Type }
func (v *ArrayLit) TypeID() ir.TypeID          { return v.Type }
func (v *DynamicArrayAlloc) TypeID() ir.TypeID { return v.Type }
func (v *Alloc) TypeID() ir.TypeID             { return v.Type }
func (v *ZeroValue) TypeID() ir.TypeID         { return v.Type }
func (v *VariantMake) TypeID() ir.TypeID       { return v.Type }
func (v *VariantIs) TypeID() ir.TypeID         { return v.Type }
func (v *InterfaceMake) TypeID() ir.TypeID     { return v.Type }
func (v *InterfaceCall) TypeID() ir.TypeID     { return v.Type }

func (i *Assign) SourceLocation() *source.Location            { return i.Location }
func (i *Store) SourceLocation() *source.Location             { return i.Location }
func (i *Print) SourceLocation() *source.Location             { return i.Location }
func (i *Drop) SourceLocation() *source.Location              { return i.Location }
func (i *DropPoint) SourceLocation() *source.Location         { return i.Location }
func (i *Jump) SourceLocation() *source.Location              { return i.Location }
func (i *Branch) SourceLocation() *source.Location            { return i.Location }
func (i *SwitchVariant) SourceLocation() *source.Location     { return i.Location }
func (i *Ret) SourceLocation() *source.Location               { return i.Location }
func (r *RefConst) SourceLocation() *source.Location          { return r.Location }
func (r *RefName) SourceLocation() *source.Location           { return r.Location }
func (v *StringLiteral) SourceLocation() *source.Location     { return v.Location }
func (v *Unary) SourceLocation() *source.Location             { return v.Location }
func (v *Binary) SourceLocation() *source.Location            { return v.Location }
func (v *StringConcat) SourceLocation() *source.Location      { return v.Location }
func (v *Move) SourceLocation() *source.Location              { return v.Location }
func (v *Cast) SourceLocation() *source.Location              { return v.Location }
func (v *AddrOf) SourceLocation() *source.Location            { return v.Location }
func (v *SliceView) SourceLocation() *source.Location         { return v.Location }
func (v *Load) SourceLocation() *source.Location              { return v.Location }
func (v *Len) SourceLocation() *source.Location               { return v.Location }
func (v *StringChars) SourceLocation() *source.Location       { return v.Location }
func (v *StringFromBytes) SourceLocation() *source.Location   { return v.Location }
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

func (r *RefConst) Text(types *ir.TypeTable) string { return r.Value }
func (r *RefName) Text(types *ir.TypeTable) string  { return r.Name }
func (v *StringLiteral) Text(types *ir.TypeTable) string {
	if v == nil {
		return "string-literal"
	}
	return v.Name
}
func (v *Move) Text(types *ir.TypeTable) string { return v.Src.Text(types) }
func (v *Unary) Text(types *ir.TypeTable) string {
	return fmt.Sprintf("%s %s", v.Op, v.Arg.Text(types))
}
func (v *Binary) Text(types *ir.TypeTable) string {
	return fmt.Sprintf("%s %s, %s", v.Op, v.Left.Text(types), v.Right.Text(types))
}
func (v *StringConcat) Text(types *ir.TypeTable) string {
	return fmt.Sprintf("concat %s, %s", v.Left.Text(types), v.Right.Text(types))
}
func (v *Cast) Text(types *ir.TypeTable) string {
	return fmt.Sprintf("cast %s to %s", v.Arg.Text(types), types.DisplayName(v.Type))
}
func (p *Place) Text(types *ir.TypeTable) string {
	if p == nil || p.Root == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(p.Root.Text(types))
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
				b.WriteString(projection.Index.Text(types))
			}
			b.WriteString("]")
		case PlaceProjectionVariantPayload:
			fmt.Fprintf(&b, ".variant%d", projection.Case)
		}
	}
	return b.String()
}

func (v *AddrOf) Text(types *ir.TypeTable) string { return fmt.Sprintf("addr %s", v.Place.Text(types)) }
func (v *SliceView) Text(types *ir.TypeTable) string {
	return fmt.Sprintf("view %s", v.Source.Text(types))
}
func (v *Load) Text(types *ir.TypeTable) string { return fmt.Sprintf("load %s", v.Place.Text(types)) }
func (v *Len) Text(types *ir.TypeTable) string  { return fmt.Sprintf("len %s", v.Value.Text(types)) }
func (v *StringChars) Text(types *ir.TypeTable) string {
	return fmt.Sprintf("chars %s", v.Value.Text(types))
}
func (v *StringFromBytes) Text(types *ir.TypeTable) string {
	if v.Allocator != nil {
		return fmt.Sprintf("from_bytes %s, %s", v.Bytes.Text(types), v.Allocator.Text(types))
	}
	return "from_bytes " + v.Bytes.Text(types)
}
func (v *Field) Text(types *ir.TypeTable) string {
	return fmt.Sprintf("field %s, %d", v.Base.Text(types), v.Index)
}

func (v *StructLit) Text(types *ir.TypeTable) string {
	var b strings.Builder
	b.WriteString("struct(")
	for i, field := range v.Fields {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(field.Text(types))
	}
	b.WriteString(")")
	return b.String()
}

func (v *ArrayLit) Text(types *ir.TypeTable) string {
	var b strings.Builder
	b.WriteString("array(")
	for i, value := range v.Values {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(value.Text(types))
	}
	b.WriteString(")")
	return b.String()
}

func (v *DynamicArrayAlloc) Text(types *ir.TypeTable) string {
	if v == nil {
		return ""
	}
	if v.Allocator != nil {
		return fmt.Sprintf("allocarray %d, %s", v.Length, v.Allocator.Text(types))
	}
	return fmt.Sprintf("allocarray %d", v.Length)
}

func (v *DynamicArrayOp) Text(types *ir.TypeTable) string {
	if v == nil {
		return ""
	}
	args := []string{v.Array.Text(types)}
	if v.Length != nil {
		args = append(args, v.Length.Text(types))
	}
	if v.Value != nil {
		args = append(args, v.Value.Text(types))
	}
	return string(v.Op) + " " + strings.Join(args, ", ")
}

func (v *Alloc) Text(types *ir.TypeTable) string {
	if v == nil {
		return ""
	}
	if v.Allocator != nil {
		return "alloc " + v.Value.Text(types) + ", " + v.Allocator.Text(types)
	}
	return "alloc " + v.Value.Text(types)
}

func (v *ZeroValue) Text(types *ir.TypeTable) string {
	if v == nil || v.Type == ir.InvalidType {
		return "zero"
	}
	return "zero"
}
func (v *VariantMake) Text(types *ir.TypeTable) string {
	if v == nil {
		return "variant(<nil>)"
	}
	if v.Payload == nil {
		return fmt.Sprintf("variant %d", v.Case)
	}
	return fmt.Sprintf("variant %d, %s", v.Case, v.Payload.Text(types))
}
func (v *VariantIs) Text(types *ir.TypeTable) string {
	if v == nil || v.Value == nil {
		return "is-variant(<nil>)"
	}
	return fmt.Sprintf("is-variant %s, %d", v.Value.Text(types), v.Case)
}

func (v *InterfaceMake) Text(types *ir.TypeTable) string {
	if v == nil {
		return "iface()"
	}
	return "iface(" + v.Value.Text(types) + ")"
}

func (v *InterfaceCall) Text(types *ir.TypeTable) string {
	if v == nil {
		return "ifacecall()"
	}
	var b strings.Builder
	b.WriteString("ifacecall ")
	b.WriteString(v.Base.Text(types))
	b.WriteString("(")
	for i, arg := range v.Args {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(arg.Text(types))
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
func (c *Call) TypeID() ir.TypeID                { return c.Type }
func (c *Call) SourceLocation() *source.Location { return c.Location }
func (c *Call) Text(types *ir.TypeTable) string {
	var b strings.Builder
	b.WriteString("call ")
	b.WriteString(c.Callee.Text(types))
	b.WriteString("(")
	for i, arg := range c.Args {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(arg.Text(types))
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
			fmt.Fprintf(&b, "%s = constant %s %q\n", data.Name, m.Types.DisplayName(data.Type), data.Constant.TypeText())
			continue
		}
		fmt.Fprintf(&b, "%s = constant %s %q, align %d\n", data.Name, m.Types.DisplayName(data.Type), data.Bytes, data.Align)
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
					b.WriteString(instr.Text(m.Types))
					b.WriteString("\n")
				}
				if block.Term != nil {
					b.WriteString("    ")
					b.WriteString(block.Term.Text(m.Types))
					b.WriteString("\n")
				}
			}
			b.WriteString("}\n")
		}
	}
	return b.String()
}
