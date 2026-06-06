package mir

import (
	"fmt"
	"strings"

	"compiler/internal/analysis/semantics/symbols"
	"compiler/internal/analysis/semantics/table"
	"compiler/internal/analysis/semantics/typeinfo"
	"compiler/internal/frontend/ast"
	"compiler/internal/ir"
	"compiler/internal/ir/hir"
)

type Module struct {
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
	Name  string
	Value ValueExpr
}

type StoreField struct {
	Base  ValueRef
	Index int
	Value ValueRef
}

type Jump struct {
	TargetID int
}

type Branch struct {
	Cond   ValueRef
	ThenID int
	ElseID int
}

type Ret struct {
	Value ValueRef
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
	Value string
	Type  string
}

type RefName struct {
	Name string
	Type string
}

type Unary struct {
	Op   string
	Arg  ValueRef
	Type string
}

type Binary struct {
	Op    string
	Left  ValueRef
	Right ValueRef
	Type  string
}

type Move struct {
	Src  ValueRef
	Type string
}

type Cast struct {
	Arg  ValueRef
	Type string
}

type AddrOf struct {
	Base ValueRef
	Type string
}

type Field struct {
	Base       ValueRef
	Index      int
	ThroughPtr bool
	Type       string
}

type StructLit struct {
	Fields []ValueRef
	Type   string
}

type InterfaceMake struct {
	Value    ValueRef
	DataType string
	BoxValue bool
	StackBox bool
	Slots    []ValueRef
	Type     string
}

type InterfaceCall struct {
	Base ValueRef
	Slot int
	Args []ValueRef
	Type string
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
	return "ret " + i.Value.Text()
}

func (*Unary) valueExprNode()         {}
func (*Binary) valueExprNode()        {}
func (*Move) valueExprNode()          {}
func (*Cast) valueExprNode()          {}
func (*AddrOf) valueExprNode()        {}
func (*Field) valueExprNode()         {}
func (*StructLit) valueExprNode()     {}
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

type lowerer struct {
	module      *Module
	fn          *Function
	tmp         int
	nextBlockID int
	current     *Block
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
		}
		l := &lowerer{module: out, fn: fn}
		l.current = l.newBlock()
		fn.EntryID = l.current.ID
		if !l.appendBlock(hirFn.Body) {
			return nil
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
		l.current.Instrs = append(l.current.Instrs, &Assign{Name: node.Name, Value: asValueExpr(ref)})
		return true
	case *hir.Return:
		if l.current == nil {
			return true
		}
		retRef := l.lowerExpr(node.Value, &l.current.Instrs)
		l.current.Term = &Ret{Value: retRef}
		l.current = nil
		return true
	case *hir.ExprStmt:
		if l.current == nil {
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
			l.current.Instrs = append(l.current.Instrs, &Assign{Name: target.Name, Value: asValueExpr(value)})
			return true
		case *ir.Field:
			if !target.ThroughPtr {
				return false
			}
			base := l.lowerExpr(target.Base, &l.current.Instrs)
			l.current.Instrs = append(l.current.Instrs, &StoreField{Base: base, Index: target.Index, Value: value})
			return true
		default:
			return false
		}
	case *hir.If:
		return l.appendIf(node)
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
	condBlock.Term = &Branch{Cond: condRef, ThenID: thenBlock.ID, ElseID: elseBlock.ID}

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
		thenFall.Term = &Jump{TargetID: join.ID}
	}
	if elseFall != nil && elseFall.Term == nil {
		elseFall.Term = &Jump{TargetID: join.ID}
	}
	l.current = join
	return true
}

// Call represents a function call in MIR
type Call struct {
	Callee ValueRef
	Args   []ValueRef
	Type   string
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
		return &RefConst{Value: e.Value, Type: e.TypeText()}
	case *ir.FloatLit:
		return &RefConst{Value: e.Value, Type: e.TypeText()}
	case *ir.StringLit:
		var name string
		if l.module != nil {
			elemType := fmt.Sprintf("[%d x i8]", len(e.Value)+1)
			name = l.module.InternStatic(e.Value, elemType, 1)
		} else {
			name = "@.str.unknown"
		}
		return &RefName{Name: name, Type: "cstr"}
	case *ir.Ident:
		return &RefName{Name: e.Name, Type: e.TypeText()}
	case *ir.Unary:
		arg := l.lowerExpr(e.Arg, out)
		name := l.nextTemp()
		*out = append(*out, &Assign{Name: name, Value: &Unary{Op: e.Op, Arg: arg, Type: e.TypeText()}})
		return &RefName{Name: name, Type: e.TypeText()}
	case *ir.Binary:
		left := l.lowerExpr(e.Left, out)
		right := l.lowerExpr(e.Right, out)
		name := l.nextTemp()
		*out = append(*out, &Assign{Name: name, Value: &Binary{Op: e.Op, Left: left, Right: right, Type: e.TypeText()}})
		return &RefName{Name: name, Type: e.TypeText()}
	case *ir.Call:
		callee := l.lowerExpr(e.Callee, out)
		args := make([]ValueRef, 0, len(e.Args))
		for _, arg := range e.Args {
			args = append(args, l.lowerExpr(arg, out))
		}
		name := l.nextTemp()
		*out = append(*out, &Assign{Name: name, Value: &Call{Callee: callee, Args: args, Type: e.TypeText()}})
		return &RefName{Name: name, Type: e.TypeText()}
	case *ir.AddrOf:
		base := l.lowerExpr(e.Expr, out)
		name := l.nextTemp()
		*out = append(*out, &Assign{Name: name, Value: &AddrOf{Base: base, Type: e.TypeText()}})
		return &RefName{Name: name, Type: e.TypeText()}
	case *ir.Field:
		base := l.lowerExpr(e.Base, out)
		name := l.nextTemp()
		*out = append(*out, &Assign{Name: name, Value: &Field{Base: base, Index: e.Index, ThroughPtr: e.ThroughPtr, Type: e.TypeText()}})
		return &RefName{Name: name, Type: e.TypeText()}
	case *ir.StructLit:
		fields := make([]ValueRef, 0, len(e.Fields))
		for _, field := range e.Fields {
			fields = append(fields, l.lowerExpr(field, out))
		}
		name := l.nextTemp()
		*out = append(*out, &Assign{Name: name, Value: &StructLit{Fields: fields, Type: e.TypeText()}})
		return &RefName{Name: name, Type: e.TypeText()}
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
		*out = append(*out, &Assign{Name: name, Value: &InterfaceMake{
			Value:    value,
			DataType: dataType,
			BoxValue: boxValue,
			Slots:    slots,
			Type:     e.TypeText(),
		}})
		return &RefName{Name: name, Type: e.TypeText()}
	case *ir.InterfaceCall:
		base := l.lowerExpr(e.Base, out)
		args := make([]ValueRef, 0, len(e.Args))
		for _, arg := range e.Args {
			args = append(args, l.lowerExpr(arg, out))
		}
		name := l.nextTemp()
		*out = append(*out, &Assign{Name: name, Value: &InterfaceCall{Base: base, Slot: e.Slot, Args: args, Type: e.TypeText()}})
		return &RefName{Name: name, Type: e.TypeText()}
	case *ir.Cast:
		arg := l.lowerExpr(e.Expr, out)
		name := l.nextTemp()
		*out = append(*out, &Assign{Name: name, Value: &Cast{Arg: arg, Type: e.TypeText()}})
		return &RefName{Name: name, Type: e.TypeText()}
	default:
		return &RefConst{Value: "0", Type: "i32"}
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
			assign, ok := instr.(*Assign)
			if !ok || assign == nil || assign.Value == nil {
				continue
			}
			switch value := assign.Value.(type) {
			case *Move:
				// pure alias, safe
			case *InterfaceCall:
				// local dispatch is safe; boxed payload only needs to live for this function
			case *Call:
				for _, arg := range value.Args {
					markEscape(arg)
				}
			default:
				for _, ref := range valueRefsOf(value) {
					markEscape(ref)
				}
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
	case *StructLit:
		return append([]ValueRef(nil), node.Fields...)
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
		b.WriteString(fmt.Sprintf("%s = constant %s %q, align %d\n", data.Name, data.Type, data.Value, data.Align))
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
				b.WriteString(fmt.Sprintf("%d", block.ID))
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
