package mir

import (
	"fmt"
	"strings"

	"compiler/internal/frontend/ast"
	"compiler/internal/ir"
	"compiler/internal/ir/hir"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/table"
	"compiler/internal/semantics/typeinfo"
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

type StoreField struct {
	Base     ValueRef
	Index    int
	Value    ValueRef
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

type AddrOf struct {
	Base     ValueRef
	Type     string
	Location *source.Location
}

type Field struct {
	Base       ValueRef
	Index      int
	ThroughPtr bool
	Type       string
	Location   *source.Location
}

type FieldAddr struct {
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
	BoxValue bool
	StackBox bool
	Slots    []ValueRef
	Type     string
	Location *source.Location
}

type InterfaceCall struct {
	Base     ValueRef
	Slot     int
	Args     []ValueRef
	Type     string
	Location *source.Location
}

func (i *Assign) Text() string {
	return fmt.Sprintf("%s = %s", i.Name, i.Value.Text())
}

func (i *StoreField) Text() string {
	return fmt.Sprintf("storefield %s, %d, %s", i.Base.Text(), i.Index, i.Value.Text())
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

func (*Unary) valueExprNode()         {}
func (*Binary) valueExprNode()        {}
func (*Move) valueExprNode()          {}
func (*Cast) valueExprNode()          {}
func (*AddrOf) valueExprNode()        {}
func (*Field) valueExprNode()         {}
func (*FieldAddr) valueExprNode()     {}
func (*StructLit) valueExprNode()     {}
func (*ZeroValue) valueExprNode()     {}
func (*OptionalSome) valueExprNode()  {}
func (*InterfaceMake) valueExprNode() {}
func (*InterfaceCall) valueExprNode() {}
func (*RefConst) valueRefNode()       {}
func (*RefName) valueRefNode()        {}

func (r *RefConst) Text() string { return r.Value }
func (r *RefName) Text() string  { return r.Name }
func (v *Move) Text() string     { return v.Src.Text() }
func (v *Unary) Text() string    { return fmt.Sprintf("%s %s", v.Op, v.Arg.Text()) }
func (v *Binary) Text() string   { return fmt.Sprintf("%s %s, %s", v.Op, v.Left.Text(), v.Right.Text()) }
func (v *Cast) Text() string     { return fmt.Sprintf("cast %s to %s", v.Arg.Text(), v.Type) }
func (v *AddrOf) Text() string   { return fmt.Sprintf("addr %s", v.Base.Text()) }
func (v *Field) Text() string    { return fmt.Sprintf("field %s, %d", v.Base.Text(), v.Index) }
func (v *FieldAddr) Text() string {
	return fmt.Sprintf("fieldaddr %s, %d", v.Base.Text(), v.Index)
}
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
	case *StoreField:
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
	case *Field:
		return node.Location
	case *FieldAddr:
		return node.Location
	case *StructLit:
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

type lowerer struct {
	module      *Module
	fn          *Function
	tmp         int
	nextBlockID int
	current     *Block
	location    *source.Location
}

func evalASTLiteral(expr ast.Expr) (string, bool) {
	if expr == nil {
		return "", false
	}
	switch e := expr.(type) {
	case *ast.NumberLit:
		return e.Value, true
	case *ast.StringLit:
		return e.Value, true
	}
	return "", false
}

func GenerateMIR(in *hir.Module, scope *table.Scope) *Module {
	if in == nil {
		return nil
	}
	out := &Module{
		FilePath:        in.FilePath,
		Name:            in.Name,
		StaticData:      make([]*StaticEntry, 0),
		InterfaceThunks: make([]*InterfaceThunk, 0),
		Funcs:           make([]*Function, 0, len(in.Externs)+len(in.Funcs)),
	}

	if scope != nil {
		for _, sym := range scope.Symbols() {
			if sym == nil {
				continue
			}
			if sym.Kind == symbols.SymbolVar || sym.Kind == symbols.SymbolConst {
				var valExpr ast.Expr
				if letDecl, ok := sym.ASTNode.(*ast.LetDecl); ok && letDecl != nil {
					valExpr = letDecl.Value
				} else if constDecl, ok := sym.ASTNode.(*ast.ConstDecl); ok && constDecl != nil {
					valExpr = constDecl.Value
				}
				if valStr, ok := evalASTLiteral(valExpr); ok {
					var typText string
					if sym.Type != nil {
						typText = typeinfo.TypeText(typeinfo.Underlying(sym.Type))
					} else {
						typText = "i32"
					}
					align := 4
					if typText == "cstr" {
						align = 8
					}
					name := fmt.Sprintf("@%s$%d", sym.Name, sym.ID)
					out.StaticData = append(out.StaticData, &StaticEntry{
						Name:  name,
						Type:  typText,
						Value: valStr,
						Align: align,
					})
				}
			}
		}
	}

	for _, ex := range in.Externs {
		out.Funcs = append(out.Funcs, &Function{
			Name:       ex.Name,
			Params:     append([]ir.Param(nil), ex.Params...),
			ReturnType: ex.ReturnType,
			Blocks:     nil,
		})
	}
	for _, hirFn := range in.Funcs {
		if hirFn == nil {
			continue
		}
		fn := &Function{
			Name:       hirFn.Name,
			Params:     append([]ir.Param(nil), hirFn.Params...),
			ReturnType: hirFn.ReturnType,
			EntryID:    0,
			Blocks:     make([]*Block, 0),
			Location:   hirFn.Location,
		}
		l := &lowerer{module: out, fn: fn}
		l.current = l.newBlock()
		fn.EntryID = l.current.ID
		if !l.appendBlock(hirFn.Body) {
			return nil
		}
		if l.current != nil && l.current.Term == nil && fn.ReturnType == "void" {
			l.setBlockTerm(l.current, &Ret{})
			l.current = nil
		}
		markLocalInterfaceBoxing(fn)
		out.Funcs = append(out.Funcs, fn)
	}
	return out
}

func (l *lowerer) newBlock() *Block {
	block := &Block{
		ID:     l.nextBlockID,
		Instrs: make([]Instr, 0),
	}
	l.nextBlockID++
	l.fn.Blocks = append(l.fn.Blocks, block)
	return block
}

func (l *lowerer) appendBlock(block *hir.Block) bool {
	if block == nil {
		return true
	}
	for _, stmt := range block.Stmts {
		if !l.appendStmt(stmt) {
			return false
		}
		if l.current == nil {
			break
		}
	}
	return true
}

func (l *lowerer) appendStmt(stmt hir.Stmt) bool {
	if l == nil || stmt == nil {
		return true
	}
	prevLoc := l.location
	l.location = hir.LocOf(stmt)
	defer func() {
		l.location = prevLoc
	}()
	switch node := stmt.(type) {
	case *hir.Block:
		return l.appendBlock(node)
	case *hir.Binding:
		if l.current == nil {
			return true
		}
		ref := l.lowerExpr(node.Value, &l.current.Instrs)
		if refName, ok := ref.(*RefName); ok && refName.Name == node.Name {
			return true
		}
		l.appendInstr(&l.current.Instrs, &Assign{Name: node.Name, Value: asValueExpr(ref)})
		return true
	case *hir.Return:
		if l.current == nil {
			return true
		}
		retRef := l.lowerExpr(node.Value, &l.current.Instrs)
		l.setBlockTerm(l.current, &Ret{Value: retRef})
		l.current = nil
		return true
	case *hir.ExprStmt:
		if l.current == nil {
			return true
		}
		if l.lowerDiscardedExpr(node.Value, &l.current.Instrs) {
			return true
		}
		l.lowerExpr(node.Value, &l.current.Instrs)
		return true
	case *hir.Assign:
		if l.current == nil {
			return true
		}
		value := l.lowerExpr(node.Value, &l.current.Instrs)
		switch target := node.Target.(type) {
		case *ir.Ident:
			l.appendInstr(&l.current.Instrs, &Assign{Name: target.Name, Value: asValueExpr(value)})
			return true
		case *ir.Field:
			if !target.ThroughPtr {
				return false
			}
			base := l.lowerExpr(target.Base, &l.current.Instrs)
			l.appendInstr(&l.current.Instrs, &StoreField{Base: base, Index: target.Index, Value: value})
			return true
		default:
			return false
		}
	case *hir.If:
		return l.appendIf(node)
	case *hir.For:
		return l.appendFor(node)
	default:
		return false
	}
}

func (l *lowerer) appendIf(node *hir.If) bool {
	if l.current == nil || node == nil {
		return true
	}
	condRef := l.lowerExpr(node.Cond, &l.current.Instrs)
	condBlock := l.current
	thenBlock := l.newBlock()
	elseBlock := l.newBlock()
	l.setBlockTerm(condBlock, &Branch{Cond: condRef, ThenID: thenBlock.ID, ElseID: elseBlock.ID})

	l.current = thenBlock
	if !l.appendBlock(node.Then) {
		return false
	}
	thenFall := l.current

	l.current = elseBlock
	if node.Else != nil {
		if !l.appendStmt(node.Else) {
			return false
		}
	}
	elseFall := l.current

	if thenFall == nil && elseFall == nil {
		l.current = nil
		return true
	}

	join := l.newBlock()
	if thenFall != nil && thenFall.Term == nil {
		l.setBlockTerm(thenFall, &Jump{TargetID: join.ID})
	}
	if elseFall != nil && elseFall.Term == nil {
		l.setBlockTerm(elseFall, &Jump{TargetID: join.ID})
	}
	l.current = join
	return true
}

func (l *lowerer) appendFor(node *hir.For) bool {
	if l.current == nil || node == nil || node.Body == nil {
		return true
	}
	if node.Cond == nil {
		bodyBlock := l.newBlock()
		l.setBlockTerm(l.current, &Jump{TargetID: bodyBlock.ID})
		l.current = bodyBlock
		if !l.appendBlock(node.Body) {
			return false
		}
		if l.current != nil && l.current.Term == nil {
			l.setBlockTerm(l.current, &Jump{TargetID: bodyBlock.ID})
		}
		l.current = nil
		return true
	}
	headerBlock := l.newBlock()
	bodyBlock := l.newBlock()
	exitBlock := l.newBlock()
	l.setBlockTerm(l.current, &Jump{TargetID: headerBlock.ID})

	l.current = headerBlock
	condRef := l.lowerExpr(node.Cond, &l.current.Instrs)
	l.setBlockTerm(headerBlock, &Branch{Cond: condRef, ThenID: bodyBlock.ID, ElseID: exitBlock.ID})

	l.current = bodyBlock
	if !l.appendBlock(node.Body) {
		return false
	}
	if l.current != nil && l.current.Term == nil {
		l.setBlockTerm(l.current, &Jump{TargetID: headerBlock.ID})
	}
	l.current = exitBlock
	return true
}

func (l *lowerer) appendInstr(out *[]Instr, instr Instr) {
	if out == nil || instr == nil {
		return
	}
	switch node := instr.(type) {
	case *Assign:
		node.Location = l.location
		if exprLoc := ValueExprLocation(node.Value); exprLoc != nil {
			node.Location = exprLoc
		}
	case *StoreField:
		node.Location = l.location
	case *Call:
		node.Location = l.location
	case *InterfaceCall:
		node.Location = l.location
	}
	*out = append(*out, instr)
}

func (l *lowerer) setBlockTerm(block *Block, term Terminator) {
	if block == nil || term == nil {
		return
	}
	switch node := term.(type) {
	case *Ret:
		node.Location = l.location
	case *Branch:
		node.Location = l.location
	case *Jump:
		node.Location = l.location
	}
	block.Term = term
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

func (l *lowerer) lowerExpr(expr ir.Expr, out *[]Instr) ValueRef {
	switch e := expr.(type) {
	case *ir.IntLit:
		return &RefConst{Value: e.Value, Type: e.TypeText(), Location: ir.ExprLocation(e)}
	case *ir.FloatLit:
		return &RefConst{Value: e.Value, Type: e.TypeText(), Location: ir.ExprLocation(e)}
	case *ir.BoolLit:
		return &RefConst{Value: e.String(), Type: e.TypeText(), Location: ir.ExprLocation(e)}
	case *ir.StringLit:
		var name string
		if l.module != nil {
			elemType := fmt.Sprintf("[%d x i8]", len(e.Value)+1)
			name = l.module.InternStatic(e.Value, elemType, 1)
		} else {
			name = "@.str.unknown"
		}
		return &RefName{Name: name, Type: "cstr", Location: ir.ExprLocation(e)}
	case *ir.ZeroValue:
		name := l.nextTemp()
		l.appendInstr(out, &Assign{Name: name, Value: &ZeroValue{Type: e.TypeText(), Location: ir.ExprLocation(e)}})
		return &RefName{Name: name, Type: e.TypeText(), Location: ir.ExprLocation(e)}
	case *ir.OptionalSome:
		value := l.lowerExpr(e.Value, out)
		name := l.nextTemp()
		l.appendInstr(out, &Assign{Name: name, Value: &OptionalSome{Value: value, Type: e.TypeText(), Location: ir.ExprLocation(e)}})
		return &RefName{Name: name, Type: e.TypeText(), Location: ir.ExprLocation(e)}
	case *ir.Ident:
		return &RefName{Name: e.Name, Type: e.TypeText(), Location: ir.ExprLocation(e)}
	case *ir.Unary:
		arg := l.lowerExpr(e.Arg, out)
		name := l.nextTemp()
		l.appendInstr(out, &Assign{Name: name, Value: &Unary{Op: e.Op, Arg: arg, Type: e.TypeText(), Location: ir.ExprLocation(e)}})
		return &RefName{Name: name, Type: e.TypeText(), Location: ir.ExprLocation(e)}
	case *ir.Binary:
		left := l.lowerExpr(e.Left, out)
		right := l.lowerExpr(e.Right, out)
		name := l.nextTemp()
		l.appendInstr(out, &Assign{Name: name, Value: &Binary{Op: e.Op, Left: left, Right: right, Type: e.TypeText(), Location: ir.ExprLocation(e)}})
		return &RefName{Name: name, Type: e.TypeText(), Location: ir.ExprLocation(e)}
	case *ir.Call:
		callee := l.lowerExpr(e.Callee, out)
		args := make([]ValueRef, 0, len(e.Args))
		for _, arg := range e.Args {
			args = append(args, l.lowerExpr(arg, out))
		}
		call := &Call{Callee: callee, Args: args, Type: e.TypeText(), Location: ir.ExprLocation(e)}
		if call.Type == "" {
			call.Type = "void"
			l.appendInstr(out, call)
			return nil
		}
		name := l.nextTemp()
		l.appendInstr(out, &Assign{Name: name, Value: call})
		return &RefName{Name: name, Type: e.TypeText(), Location: ir.ExprLocation(e)}
	case *ir.AddrOf:
		if field, ok := e.Expr.(*ir.Field); ok && field != nil && field.ThroughPtr {
			base := l.lowerExpr(field.Base, out)
			name := l.nextTemp()
			l.appendInstr(out, &Assign{Name: name, Value: &FieldAddr{Base: base, Index: field.Index, Type: e.TypeText(), Location: ir.ExprLocation(e)}})
			return &RefName{Name: name, Type: e.TypeText(), Location: ir.ExprLocation(e)}
		}
		base := l.lowerExpr(e.Expr, out)
		name := l.nextTemp()
		l.appendInstr(out, &Assign{Name: name, Value: &AddrOf{Base: base, Type: e.TypeText(), Location: ir.ExprLocation(e)}})
		return &RefName{Name: name, Type: e.TypeText(), Location: ir.ExprLocation(e)}
	case *ir.Field:
		base := l.lowerExpr(e.Base, out)
		name := l.nextTemp()
		l.appendInstr(out, &Assign{Name: name, Value: &Field{Base: base, Index: e.Index, ThroughPtr: e.ThroughPtr, Type: e.TypeText(), Location: ir.ExprLocation(e)}})
		return &RefName{Name: name, Type: e.TypeText(), Location: ir.ExprLocation(e)}
	case *ir.StructLit:
		fields := make([]ValueRef, 0, len(e.Fields))
		for _, field := range e.Fields {
			fields = append(fields, l.lowerExpr(field, out))
		}
		name := l.nextTemp()
		l.appendInstr(out, &Assign{Name: name, Value: &StructLit{Fields: fields, Type: e.TypeText(), Location: ir.ExprLocation(e)}})
		return &RefName{Name: name, Type: e.TypeText(), Location: ir.ExprLocation(e)}
	case *ir.InterfaceMake:
		value := l.lowerExpr(e.Value, out)
		dataType, boxValue := interfaceStorageFor(e.Value.TypeText())

		slots := make([]ValueRef, 0, len(e.Slots))
		for index, slot := range e.Slots {
			wrapperName := ir.InterfaceThunkName(slot.InterfaceType, dataType, slot.MethodName, index)
			slot.WrapperName = wrapperName
			slot.DataType = dataType
			l.registerInterfaceThunk(slot)
			slots = append(slots, &RefName{Name: wrapperName, Type: slot.SlotType})
		}
		name := l.nextTemp()
		l.appendInstr(out, &Assign{Name: name, Value: &InterfaceMake{
			Value:    value,
			DataType: dataType,
			BoxValue: boxValue,
			Slots:    slots,
			Type:     e.TypeText(),
			Location: ir.ExprLocation(e),
		}})
		return &RefName{Name: name, Type: e.TypeText(), Location: ir.ExprLocation(e)}
	case *ir.InterfaceCall:
		base := l.lowerExpr(e.Base, out)
		args := make([]ValueRef, 0, len(e.Args))
		for _, arg := range e.Args {
			args = append(args, l.lowerExpr(arg, out))
		}
		call := &InterfaceCall{Base: base, Slot: e.Slot, Args: args, Type: e.TypeText(), Location: ir.ExprLocation(e)}
		if call.Type == "" {
			call.Type = "void"
			l.appendInstr(out, call)
			return nil
		}
		name := l.nextTemp()
		l.appendInstr(out, &Assign{Name: name, Value: call})
		return &RefName{Name: name, Type: e.TypeText(), Location: ir.ExprLocation(e)}
	case *ir.Cast:
		arg := l.lowerExpr(e.Expr, out)
		name := l.nextTemp()
		l.appendInstr(out, &Assign{Name: name, Value: &Cast{Arg: arg, Type: e.TypeText(), Location: ir.ExprLocation(e)}})
		return &RefName{Name: name, Type: e.TypeText(), Location: ir.ExprLocation(e)}
	default:
		return &RefConst{Value: "0", Type: "i32"}
	}
}

func (l *lowerer) lowerDiscardedExpr(expr ir.Expr, out *[]Instr) bool {
	if l == nil || out == nil || expr == nil {
		return false
	}
	switch e := expr.(type) {
	case *ir.Call:
		callee := l.lowerExpr(e.Callee, out)
		args := make([]ValueRef, 0, len(e.Args))
		for _, arg := range e.Args {
			args = append(args, l.lowerExpr(arg, out))
		}
		call := &Call{Callee: callee, Args: args, Type: e.TypeText()}
		if call.Type == "" {
			call.Type = "void"
		}
		l.appendInstr(out, call)
		return true
	case *ir.InterfaceCall:
		base := l.lowerExpr(e.Base, out)
		args := make([]ValueRef, 0, len(e.Args))
		for _, arg := range e.Args {
			args = append(args, l.lowerExpr(arg, out))
		}
		call := &InterfaceCall{Base: base, Slot: e.Slot, Args: args, Type: e.TypeText()}
		if call.Type == "" {
			call.Type = "void"
		}
		l.appendInstr(out, call)
		return true
	default:
		return false
	}
}

func interfaceStorageFor(typeText string) (string, bool) {
	if remainder, ok := strings.CutPrefix(typeText, "^"); ok {
		return remainder, false
	}
	return typeText, true
}

func markLocalInterfaceBoxing(fn *Function) {
	if fn == nil || fn.Blocks == nil {
		return
	}
	producers := make(map[string]ValueExpr)
	for _, block := range fn.Blocks {
		if block == nil {
			continue
		}
		for _, instr := range block.Instrs {
			assign, ok := instr.(*Assign)
			if !ok || assign == nil || assign.Name == "" || assign.Value == nil {
				continue
			}
			producers[assign.Name] = assign.Value
		}
	}

	rootCache := make(map[string]map[string]struct{})
	var rootsOfName func(string, map[string]struct{}) map[string]struct{}
	rootsOfName = func(name string, seen map[string]struct{}) map[string]struct{} {
		if cached, ok := rootCache[name]; ok {
			return cached
		}
		if _, ok := seen[name]; ok {
			return nil
		}
		seen[name] = struct{}{}
		value := producers[name]
		switch node := value.(type) {
		case *InterfaceMake:
			if node != nil && node.BoxValue {
				out := map[string]struct{}{name: {}}
				rootCache[name] = out
				return out
			}
		case *Move:
			out := rootsOfRef(node.Src, rootsOfName, seen)
			rootCache[name] = out
			return out
		}
		rootCache[name] = nil
		return nil
	}

	escapes := make(map[string]bool)
	markEscape := func(ref ValueRef) {
		for root := range rootsOfRef(ref, rootsOfName, nil) {
			escapes[root] = true
		}
	}

	for _, block := range fn.Blocks {
		if block == nil {
			continue
		}
		for _, instr := range block.Instrs {
			if assign, ok := instr.(*Assign); ok && assign != nil {
				if assign.Value == nil {
					continue
				}
				switch value := assign.Value.(type) {
				case *Move:
					// pure alias, safe
				case *InterfaceCall:
					// The dispatch on the receiver is safe, but arguments passed can escape
					for _, arg := range value.Args {
						markEscape(arg)
					}
				case *Call:
					for _, arg := range value.Args {
						markEscape(arg)
					}
				default:
					for _, ref := range valueRefsOf(value) {
						markEscape(ref)
					}
				}
			} else if store, ok := instr.(*StoreField); ok && store != nil {
				// Interface values stored in struct fields escape
				markEscape(store.Value)
			}
		}
		if term, ok := block.Term.(*Ret); ok && term != nil {
			markEscape(term.Value)
		}
	}

	for name, value := range producers {
		makeVal, ok := value.(*InterfaceMake)
		if !ok || makeVal == nil || !makeVal.BoxValue {
			continue
		}
		if !escapes[name] {
			makeVal.StackBox = true
		}
	}
}

func rootsOfRef(ref ValueRef, rootsOfName func(string, map[string]struct{}) map[string]struct{}, seen map[string]struct{}) map[string]struct{} {
	nameRef, ok := ref.(*RefName)
	if !ok || nameRef == nil || rootsOfName == nil {
		return nil
	}
	if seen == nil {
		seen = make(map[string]struct{})
	}
	return rootsOfName(nameRef.Name, seen)
}

func valueRefsOf(expr ValueExpr) []ValueRef {
	switch node := expr.(type) {
	case *Move:
		return []ValueRef{node.Src}
	case *Unary:
		return []ValueRef{node.Arg}
	case *Binary:
		return []ValueRef{node.Left, node.Right}
	case *Cast:
		return []ValueRef{node.Arg}
	case *AddrOf:
		return []ValueRef{node.Base}
	case *Field:
		return []ValueRef{node.Base}
	case *FieldAddr:
		return []ValueRef{node.Base}
	case *StructLit:
		return append([]ValueRef(nil), node.Fields...)
	case *ZeroValue:
		return nil
	case *OptionalSome:
		return []ValueRef{node.Value}
	case *InterfaceMake:
		refs := make([]ValueRef, 0, len(node.Slots)+1)
		refs = append(refs, node.Value)
		refs = append(refs, node.Slots...)
		return refs
	case *InterfaceCall:
		refs := make([]ValueRef, 0, len(node.Args)+1)
		refs = append(refs, node.Base)
		refs = append(refs, node.Args...)
		return refs
	case *Call:
		refs := make([]ValueRef, 0, len(node.Args)+1)
		refs = append(refs, node.Callee)
		refs = append(refs, node.Args...)
		return refs
	default:
		return nil
	}
}

func (l *lowerer) registerInterfaceThunk(slot ir.InterfaceSlot) {
	if l == nil || l.module == nil || slot.WrapperName == "" {
		return
	}
	for _, w := range l.module.InterfaceThunks {
		if w.Name == slot.WrapperName {
			return
		}
	}
	thunk := &InterfaceThunk{
		Name:     slot.WrapperName,
		SlotType: slot.SlotType,
		FuncName: slot.FuncName,
		FuncType: slot.FuncType,
		DataType: slot.DataType,
	}
	l.module.InterfaceThunks = append(l.module.InterfaceThunks, thunk)
}

func (l *lowerer) nextTemp() string {
	l.tmp++
	return fmt.Sprintf("t%d", l.tmp)
}

func asValueExpr(ref ValueRef) ValueExpr {
	switch node := ref.(type) {
	case *RefConst:
		return &Move{Src: ref, Type: node.Type}
	case *RefName:
		return &Move{Src: ref, Type: node.Type}
	default:
		return &Move{Src: ref, Type: "i32"}
	}
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
