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
	Instrs     []Instr
}

type Instr interface {
	Text() string
}

type Assign struct {
	Name  string
	Value ValueExpr
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
	Value int32
}

type RefName struct {
	Name string
}

type Unary struct {
	Op  string
	Arg ValueRef
}

type Binary struct {
	Op    string
	Left  ValueRef
	Right ValueRef
}

type Move struct {
	Src ValueRef
}

func (i *Assign) Text() string {
	return fmt.Sprintf("%s = %s", i.Name, i.Value.Text())
}

func (i *Ret) Text() string {
	return "ret " + i.Value.Text()
}

func (*Unary) valueExprNode()   {}
func (*Binary) valueExprNode()  {}
func (*Move) valueExprNode()    {}
func (*RefConst) valueRefNode() {}
func (*RefName) valueRefNode()  {}

func (r *RefConst) Text() string { return fmt.Sprintf("%d", r.Value) }
func (r *RefName) Text() string  { return r.Name }
func (v *Move) Text() string     { return v.Src.Text() }
func (v *Unary) Text() string    { return fmt.Sprintf("%s %s", v.Op, v.Arg.Text()) }
func (v *Binary) Text() string   { return fmt.Sprintf("%s %s, %s", v.Op, v.Left.Text(), v.Right.Text()) }

func LowerHIR(in *hir.Module) *Module {
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
			Instrs:     make([]Instr, 0),
		}
		tmp := 0
		if !appendHIRBlock(hirFn.Body, &tmp, &fn.Instrs) {
			return nil
		}
		out.Funcs = append(out.Funcs, fn)
	}
	return out
}

func appendHIRBlock(block *hir.Block, tmp *int, out *[]Instr) bool {
	if block == nil {
		return true
	}
	for _, stmt := range block.Stmts {
		if !appendHIRStmt(stmt, tmp, out) {
			return false
		}
	}
	return true
}

func appendHIRStmt(stmt hir.Stmt, tmp *int, out *[]Instr) bool {
	switch node := stmt.(type) {
	case *hir.Block:
		return appendHIRBlock(node, tmp, out)
	case *hir.Binding:
		ref := lowerExpr(node.Value, tmp, out)
		if refName, ok := ref.(*RefName); ok && refName.Name == node.Name {
			return true
		}
		*out = append(*out, &Assign{Name: node.Name, Value: asValueExpr(ref)})
		return true
	case *hir.Return:
		retRef := lowerExpr(node.Value, tmp, out)
		*out = append(*out, &Ret{Value: retRef})
		return true
	case *hir.If:
		return false
	default:
		return false
	}
}

func lowerExpr(expr ir.Expr, tmp *int, out *[]Instr) ValueRef {
	switch e := expr.(type) {
	case *ir.IntLit:
		return &RefConst{Value: e.Value}
	case *ir.Ident:
		return &RefName{Name: e.Name}
	case *ir.Unary:
		arg := lowerExpr(e.Arg, tmp, out)
		name := nextTemp(tmp)
		*out = append(*out, &Assign{Name: name, Value: &Unary{Op: e.Op, Arg: arg}})
		return &RefName{Name: name}
	case *ir.Binary:
		left := lowerExpr(e.Left, tmp, out)
		right := lowerExpr(e.Right, tmp, out)
		name := nextTemp(tmp)
		*out = append(*out, &Assign{Name: name, Value: &Binary{Op: e.Op, Left: left, Right: right}})
		return &RefName{Name: name}
	default:
		return &RefConst{Value: 0}
	}
}

func nextTemp(tmp *int) string {
	*tmp = *tmp + 1
	return fmt.Sprintf("t%d", *tmp)
}

func asValueExpr(ref ValueRef) ValueExpr {
	return &Move{Src: ref}
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
		for _, instr := range fn.Instrs {
			b.WriteString("  ")
			b.WriteString(instr.Text())
			b.WriteString("\n")
		}
		b.WriteString("}\n")
	}
	return b.String()
}
