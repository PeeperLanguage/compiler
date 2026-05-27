package pipeline

import (
	"fmt"
	"strings"

	"compiler/internal/ir/mir"
)

func lowerLLVMFromMIR(mod *mir.Module) string {
	if mod == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("source_filename = \"")
	b.WriteString(mod.Name)
	b.WriteString("\"\n")
	b.WriteString("target triple = \"x86_64-pc-linux-gnu\"\n\n")
	for _, fn := range mod.Externs {
		b.WriteString("declare ")
		b.WriteString(mustLLVMType(fn.ReturnType))
		b.WriteString(" @")
		b.WriteString(strings.ReplaceAll(fn.Name, "::", "__"))
		b.WriteString("(")
		for i, param := range fn.Params {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(mustLLVMType(param.Type))
		}
		b.WriteString(")\n")
	}
	if len(mod.Externs) > 0 {
		b.WriteString("\n")
	}
	if len(mod.Funcs) == 0 {
		return b.String()
	}
	for _, fn := range mod.Funcs {
		if fn == nil {
			continue
		}
		b.WriteString("define ")
		b.WriteString(mustLLVMType(fn.ReturnType))
		b.WriteString(" @")
		b.WriteString(strings.ReplaceAll(fn.Name, "::", "__"))
		b.WriteString("(")
		for i, param := range fn.Params {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(mustLLVMType(param.Type))
			b.WriteString(" %")
			b.WriteString(param.Name)
		}
		b.WriteString(") {\n")
		lb := newLLVMBuilder(&b)
		for _, param := range fn.Params {
			lb.locals[param.Name] = "%" + param.Name
		}
		for _, block := range fn.Blocks {
			if block == nil {
				continue
			}
			lb.label(block.ID)
			for _, instr := range block.Instrs {
				switch in := instr.(type) {
				case *mir.Assign:
					lb.locals[in.Name] = emitValueExpr(lb, in.Value)
				}
			}
			switch term := block.Term.(type) {
			case *mir.Jump:
				lb.line(fmt.Sprintf("br label %%b%d", term.TargetID))
			case *mir.Branch:
				cond := emitCondRef(lb, term.Cond)
				lb.line(fmt.Sprintf("br i1 %s, label %%b%d, label %%b%d", cond, term.ThenID, term.ElseID))
			case *mir.Ret:
				val := emitRef(lb, term.Value)
				lb.line("ret i32 " + val)
			}
		}
		b.WriteString("}\n")
	}
	return b.String()
}

func mustLLVMType(typeText string) string {
	switch typeText {
	case "i32":
		return "i32"
	default:
		panic("unsupported llvm type: " + typeText)
	}
}

type llvmBuilder struct {
	out    *strings.Builder
	nextID int
	locals map[string]string
}

func newLLVMBuilder(out *strings.Builder) *llvmBuilder {
	return &llvmBuilder{out: out, nextID: 1, locals: make(map[string]string)}
}

func (b *llvmBuilder) nextReg() string {
	name := fmt.Sprintf("%%t%d", b.nextID)
	b.nextID++
	return name
}

func (b *llvmBuilder) line(text string) {
	b.out.WriteString("  ")
	b.out.WriteString(text)
	b.out.WriteString("\n")
}

func (b *llvmBuilder) label(id int) {
	b.out.WriteString(fmt.Sprintf("b%d:\n", id))
}

func emitValueExpr(b *llvmBuilder, expr mir.ValueExpr) string {
	switch e := expr.(type) {
	case *mir.Move:
		return emitRef(b, e.Src)
	case *mir.Unary:
		arg := emitRef(b, e.Arg)
		switch e.Op {
		case "-":
			out := b.nextReg()
			b.line(fmt.Sprintf("%s = sub i32 0, %s", out, arg))
			return out
		case "!":
			cmp := b.nextReg()
			b.line(fmt.Sprintf("%s = icmp eq i32 %s, 0", cmp, arg))
			out := b.nextReg()
			b.line(fmt.Sprintf("%s = zext i1 %s to i32", out, cmp))
			return out
		default:
			return arg
		}
	case *mir.Binary:
		left := emitRef(b, e.Left)
		right := emitRef(b, e.Right)
		out := b.nextReg()
		switch e.Op {
		case "+":
			b.line(fmt.Sprintf("%s = add nsw i32 %s, %s", out, left, right))
		case "-":
			b.line(fmt.Sprintf("%s = sub nsw i32 %s, %s", out, left, right))
		case "*":
			b.line(fmt.Sprintf("%s = mul nsw i32 %s, %s", out, left, right))
		case "/":
			b.line(fmt.Sprintf("%s = sdiv i32 %s, %s", out, left, right))
		case "%":
			b.line(fmt.Sprintf("%s = srem i32 %s, %s", out, left, right))
		case "==", "!=", "<", "<=", ">", ">=":
			pred := map[string]string{"==": "eq", "!=": "ne", "<": "slt", "<=": "sle", ">": "sgt", ">=": "sge"}[e.Op]
			cmp := b.nextReg()
			b.line(fmt.Sprintf("%s = icmp %s i32 %s, %s", cmp, pred, left, right))
			b.line(fmt.Sprintf("%s = zext i1 %s to i32", out, cmp))
		case "&&", "||":
			lc := b.nextReg()
			b.line(fmt.Sprintf("%s = icmp ne i32 %s, 0", lc, left))
			rc := b.nextReg()
			b.line(fmt.Sprintf("%s = icmp ne i32 %s, 0", rc, right))
			merged := b.nextReg()
			if e.Op == "&&" {
				b.line(fmt.Sprintf("%s = and i1 %s, %s", merged, lc, rc))
			} else {
				b.line(fmt.Sprintf("%s = or i1 %s, %s", merged, lc, rc))
			}
			b.line(fmt.Sprintf("%s = zext i1 %s to i32", out, merged))
		default:
			return left
		}
		return out
	default:
		return "0"
	}
}

func emitRef(b *llvmBuilder, ref mir.ValueRef) string {
	switch v := ref.(type) {
	case *mir.RefConst:
		return fmt.Sprintf("%d", v.Value)
	case *mir.RefName:
		if reg, ok := b.locals[v.Name]; ok {
			return reg
		}
		return "0"
	default:
		return "0"
	}
}

func emitCondRef(b *llvmBuilder, ref mir.ValueRef) string {
	val := emitRef(b, ref)
	switch v := ref.(type) {
	case *mir.RefConst:
		if v.Value == 0 {
			return "false"
		}
		return "true"
	default:
		out := b.nextReg()
		b.line(fmt.Sprintf("%s = icmp ne i32 %s, 0", out, val))
		return out
	}
}
