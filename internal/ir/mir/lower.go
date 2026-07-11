package mir

import (
	"fmt"
	"strings"

	"compiler/internal/constvalue"
	"compiler/internal/ir"
	"compiler/internal/ir/hir"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/table"
	"compiler/internal/semantics/typeinfo"
	"compiler/internal/source"
)

type lowerer struct {
	module      *Module
	fn          *Function
	tmp         int
	nextBlockID int
	current     *Block
	location    *source.Location
}

func GenerateMIR(in *hir.Module, scope *table.Scope, constValues map[symbols.SymbolID]constvalue.Value) *Module {
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
			if sym.Kind == symbols.SymbolConst {
				entry, ok := staticEntryForConst(sym, constValues[sym.ID])
				if ok {
					out.StaticData = append(out.StaticData, entry)
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

func staticEntryForConst(sym *symbols.Symbol, value constvalue.Value) (*StaticEntry, bool) {
	if sym == nil || value == nil {
		return nil, false
	}
	valueText, ok := constStaticValueText(value)
	if !ok {
		return nil, false
	}
	typeText := value.TypeText()
	if sym.Type != nil {
		typeText = typeinfo.TypeText(typeinfo.Underlying(sym.Type))
	}
	align := 4
	if typeText == "cstr" {
		align = 8
	}
	return &StaticEntry{
		Name:  fmt.Sprintf("@%s$%d", sym.Name, sym.ID),
		Type:  typeText,
		Value: valueText,
		Align: align,
	}, true
}

func constStaticValueText(value constvalue.Value) (string, bool) {
	switch v := value.(type) {
	case *constvalue.IntConst:
		if v == nil {
			return "", false
		}
		return v.Value, true
	case *constvalue.FloatConst:
		if v == nil {
			return "", false
		}
		return llvmFloatConstText(v.Value), true
	case *constvalue.BoolConst:
		if v == nil {
			return "", false
		}
		if v.Value {
			return "true", true
		}
		return "false", true
	case *constvalue.StringConst:
		if v == nil {
			return "", false
		}
		return v.Value, true
	default:
		return "", false
	}
}

func llvmFloatConstText(value string) string {
	if strings.ContainsAny(value, ".eE") {
		return value
	}
	return value + ".0"
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
			ptr := l.projectField(&l.current.Instrs, base, target.Index, "*"+target.TypeText(), ir.ExprLocation(target))
			l.appendInstr(&l.current.Instrs, &Store{Ptr: ptr, Value: value})
			return true
		case *ir.Index:
			base := l.lowerExpr(target.Base, &l.current.Instrs)
			index := l.lowerExpr(target.Index, &l.current.Instrs)
			ptr := l.projectIndex(&l.current.Instrs, base, index, "*"+target.TypeText(), ir.ExprLocation(target))
			l.appendInstr(&l.current.Instrs, &Store{Ptr: ptr, Value: value})
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
	case *Store:
		node.Location = l.location
	case *Call:
		node.Location = l.location
	case *InterfaceCall:
		node.Location = l.location
	}
	*out = append(*out, instr)
}

func (l *lowerer) projectField(out *[]Instr, base ValueRef, index int, pointerType string, loc *source.Location) ValueRef {
	name := l.nextTemp()
	l.appendInstr(out, &Assign{Name: name, Value: &ProjectField{Base: base, Index: index, Type: pointerType, Location: loc}})
	return &RefName{Name: name, Type: pointerType, Location: loc}
}

func (l *lowerer) projectIndex(out *[]Instr, base ValueRef, index ValueRef, pointerType string, loc *source.Location) ValueRef {
	name := l.nextTemp()
	l.appendInstr(out, &Assign{Name: name, Value: &ProjectIndex{Base: base, Index: index, Type: pointerType, Location: loc}})
	return &RefName{Name: name, Type: pointerType, Location: loc}
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
			return l.projectField(out, base, field.Index, e.TypeText(), ir.ExprLocation(e))
		}
		base := l.lowerExpr(e.Expr, out)
		name := l.nextTemp()
		l.appendInstr(out, &Assign{Name: name, Value: &AddrOf{Base: base, Type: e.TypeText(), Location: ir.ExprLocation(e)}})
		return &RefName{Name: name, Type: e.TypeText(), Location: ir.ExprLocation(e)}
	case *ir.SliceView:
		source := l.lowerExpr(e.Source, out)
		name := l.nextTemp()
		l.appendInstr(out, &Assign{Name: name, Value: &SliceView{Source: source, Type: e.TypeText(), Location: ir.ExprLocation(e)}})
		return &RefName{Name: name, Type: e.TypeText(), Location: ir.ExprLocation(e)}
	case *ir.Field:
		base := l.lowerExpr(e.Base, out)
		if e.ThroughPtr {
			ptr := l.projectField(out, base, e.Index, "*"+e.TypeText(), ir.ExprLocation(e))
			name := l.nextTemp()
			l.appendInstr(out, &Assign{Name: name, Value: &Load{Ptr: ptr, Type: e.TypeText(), Location: ir.ExprLocation(e)}})
			return &RefName{Name: name, Type: e.TypeText(), Location: ir.ExprLocation(e)}
		}
		name := l.nextTemp()
		l.appendInstr(out, &Assign{Name: name, Value: &Field{Base: base, Index: e.Index, ThroughPtr: e.ThroughPtr, Type: e.TypeText(), Location: ir.ExprLocation(e)}})
		return &RefName{Name: name, Type: e.TypeText(), Location: ir.ExprLocation(e)}
	case *ir.Index:
		base := l.lowerExpr(e.Base, out)
		index := l.lowerExpr(e.Index, out)
		ptr := l.projectIndex(out, base, index, "*"+e.TypeText(), ir.ExprLocation(e))
		name := l.nextTemp()
		l.appendInstr(out, &Assign{Name: name, Value: &Load{Ptr: ptr, Type: e.TypeText(), Location: ir.ExprLocation(e)}})
		return &RefName{Name: name, Type: e.TypeText(), Location: ir.ExprLocation(e)}
	case *ir.StructLit:
		fields := make([]ValueRef, 0, len(e.Fields))
		for _, field := range e.Fields {
			fields = append(fields, l.lowerExpr(field, out))
		}
		name := l.nextTemp()
		l.appendInstr(out, &Assign{Name: name, Value: &StructLit{Fields: fields, Type: e.TypeText(), Location: ir.ExprLocation(e)}})
		return &RefName{Name: name, Type: e.TypeText(), Location: ir.ExprLocation(e)}
	case *ir.ArrayLit:
		values := make([]ValueRef, 0, len(e.Values))
		for _, value := range e.Values {
			values = append(values, l.lowerExpr(value, out))
		}
		name := l.nextTemp()
		l.appendInstr(out, &Assign{Name: name, Value: &ArrayLit{Values: values, Type: e.TypeText(), Location: ir.ExprLocation(e)}})
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
	if remainder, ok := strings.CutPrefix(typeText, "&mut "); ok {
		return remainder, false
	}
	if remainder, ok := strings.CutPrefix(typeText, "&"); ok {
		return remainder, false
	}
	if remainder, ok := strings.CutPrefix(typeText, "*"); ok {
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
			} else if store, ok := instr.(*Store); ok && store != nil {
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
	case *SliceView:
		return []ValueRef{node.Source}
	case *Load:
		return []ValueRef{node.Ptr}
	case *ProjectField:
		return []ValueRef{node.Base}
	case *ProjectIndex:
		return []ValueRef{node.Base, node.Index}
	case *Field:
		return []ValueRef{node.Base}
	case *StructLit:
		return append([]ValueRef(nil), node.Fields...)
	case *ArrayLit:
		return append([]ValueRef(nil), node.Values...)
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
