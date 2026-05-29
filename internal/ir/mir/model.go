package mir

import (
	"fmt"
	"strings"

	"compiler/internal/ir"
	"compiler/internal/ir/hir"
)

type Module struct {
	Name    string
	Externs []Extern
	Funcs   []*Function
}

type Extern struct {
	Name       string
	Params     []ir.Param
	ReturnType string
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

func (i *Assign) Text() string {
	return fmt.Sprintf("%s = %s", i.Name, i.Value.Text())
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

func (*Unary) valueExprNode()   {}
func (*Binary) valueExprNode()  {}
func (*Move) valueExprNode()    {}
func (*Cast) valueExprNode()    {}
func (*RefConst) valueRefNode() {}
func (*RefName) valueRefNode()  {}

func (r *RefConst) Text() string { return r.Value }
func (r *RefName) Text() string  { return r.Name }
func (v *Move) Text() string     { return v.Src.Text() }
func (v *Unary) Text() string    { return fmt.Sprintf("%s %s", v.Op, v.Arg.Text()) }
func (v *Binary) Text() string   { return fmt.Sprintf("%s %s, %s", v.Op, v.Left.Text(), v.Right.Text()) }
func (v *Cast) Text() string     { return fmt.Sprintf("cast %s to %s", v.Arg.Text(), v.Type) }

type lowerer struct {
	fn          *Function
	tmp         int
	nextBlockID int
	current     *Block
}

func GenerateMIR(in *hir.Module) *Module {
	if in == nil {
		return nil
	}
	out := &Module{
		Name:    in.Name,
		Externs: make([]Extern, 0, len(in.Externs)),
		Funcs:   make([]*Function, 0, len(in.Funcs)),
	}
	for _, ex := range in.Externs {
		out.Externs = append(out.Externs, Extern{
			Name:       ex.Name,
			Params:     append([]ir.Param(nil), ex.Params...),
			ReturnType: ex.ReturnType,
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
		l := &lowerer{fn: fn}
		l.current = l.newBlock()
		fn.EntryID = l.current.ID
		if !l.appendBlock(hirFn.Body) {
			return nil
		}
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
		ref := lowerExpr(node.Value, &l.tmp, &l.current.Instrs)
		if refName, ok := ref.(*RefName); ok && refName.Name == node.Name {
			return true
		}
		l.current.Instrs = append(l.current.Instrs, &Assign{Name: node.Name, Value: asValueExpr(ref)})
		return true
	case *hir.Return:
		if l.current == nil {
			return true
		}
		retRef := lowerExpr(node.Value, &l.tmp, &l.current.Instrs)
		l.current.Term = &Ret{Value: retRef}
		l.current = nil
		return true
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
	condRef := lowerExpr(node.Cond, &l.tmp, &l.current.Instrs)
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

func lowerExpr(expr ir.Expr, tmp *int, out *[]Instr) ValueRef {
	switch e := expr.(type) {
	case *ir.IntLit:
		return &RefConst{Value: e.Value, Type: e.TypeText()}
	case *ir.FloatLit:
		return &RefConst{Value: e.Value, Type: e.TypeText()}
	case *ir.Ident:
		return &RefName{Name: e.Name, Type: e.TypeText()}
	case *ir.Unary:
		arg := lowerExpr(e.Arg, tmp, out)
		name := nextTemp(tmp)
		*out = append(*out, &Assign{Name: name, Value: &Unary{Op: e.Op, Arg: arg, Type: e.TypeText()}})
		return &RefName{Name: name, Type: e.TypeText()}
	case *ir.Binary:
		left := lowerExpr(e.Left, tmp, out)
		right := lowerExpr(e.Right, tmp, out)
		name := nextTemp(tmp)
		*out = append(*out, &Assign{Name: name, Value: &Binary{Op: e.Op, Left: left, Right: right, Type: e.TypeText()}})
		return &RefName{Name: name, Type: e.TypeText()}
	case *ir.Call:
		callee := lowerExpr(e.Callee, tmp, out)
		args := make([]ValueRef, 0, len(e.Args))
		for _, arg := range e.Args {
			args = append(args, lowerExpr(arg, tmp, out))
		}
		name := nextTemp(tmp)
		*out = append(*out, &Assign{Name: name, Value: &Call{Callee: callee, Args: args, Type: e.TypeText()}})
		return &RefName{Name: name, Type: e.TypeText()}
	case *ir.Cast:
		// Lower cast expression
		arg := lowerExpr(e.Expr, tmp, out)
		name := nextTemp(tmp)
		*out = append(*out, &Assign{Name: name, Value: &Cast{Arg: arg, Type: e.TypeText()}})
		return &RefName{Name: name, Type: e.TypeText()}
	default:
		return &RefConst{Value: "0", Type: "i32"}
	}
}

func nextTemp(tmp *int) string {
	*tmp = *tmp + 1
	return fmt.Sprintf("t%d", *tmp)
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
	for _, ex := range m.Externs {
		b.WriteString("extern fn ")
		b.WriteString(ex.Name)
		b.WriteString(ir.SignatureText(ex.Params, ex.ReturnType))
		b.WriteString("\n")
	}
	if len(m.Externs) > 0 {
		b.WriteString("\n")
	}
	if len(m.Funcs) == 0 {
		return b.String()
	}
	for _, fn := range m.Funcs {
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
	return b.String()
}
