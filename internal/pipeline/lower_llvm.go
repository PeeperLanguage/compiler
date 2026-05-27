package pipeline

import (
	"fmt"
	"strings"

	"compiler/internal/ir"
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
				lb.line("ret " + mustLLVMType(fn.ReturnType) + " " + val)
			}
		}
		b.WriteString("}\n")
	}
	return b.String()
}

func mustLLVMType(typeText string) string {
	if signed, bits, ok := mirParseIntegerType(typeText); ok {
		_ = signed
		return fmt.Sprintf("i%d", bits)
	}
	switch typeText {
	case "f32":
		return "float"
	case "f64":
		return "double"
	case "bool":
		return "i1"
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
		typ := mustLLVMType(e.Type)
		switch e.Op {
		case "-":
			out := b.nextReg()
			if isLLVMFloatType(typ) {
				b.line(fmt.Sprintf("%s = fsub %s 0.0, %s", out, typ, arg))
			} else {
				b.line(fmt.Sprintf("%s = sub %s 0, %s", out, typ, arg))
			}
			return out
		case "!":
			return emitLogicalNot(b, arg, e.Arg)
		default:
			return arg
		}
	case *mir.Binary:
		left := emitRef(b, e.Left)
		right := emitRef(b, e.Right)
		out := b.nextReg()
		leftType := mustLLVMType(mirRefType(e.Left))
		switch e.Op {
		case "+":
			if isLLVMFloatType(leftType) {
				b.line(fmt.Sprintf("%s = fadd %s %s, %s", out, leftType, left, right))
			} else {
				b.line(fmt.Sprintf("%s = add %s %s, %s", out, leftType, left, right))
			}
		case "-":
			if isLLVMFloatType(leftType) {
				b.line(fmt.Sprintf("%s = fsub %s %s, %s", out, leftType, left, right))
			} else {
				b.line(fmt.Sprintf("%s = sub %s %s, %s", out, leftType, left, right))
			}
		case "*":
			if isLLVMFloatType(leftType) {
				b.line(fmt.Sprintf("%s = fmul %s %s, %s", out, leftType, left, right))
			} else {
				b.line(fmt.Sprintf("%s = mul %s %s, %s", out, leftType, left, right))
			}
		case "/":
			if isLLVMFloatType(leftType) {
				b.line(fmt.Sprintf("%s = fdiv %s %s, %s", out, leftType, left, right))
			} else if isUnsignedMIRType(mirRefType(e.Left)) {
				b.line(fmt.Sprintf("%s = udiv %s %s, %s", out, leftType, left, right))
			} else {
				b.line(fmt.Sprintf("%s = sdiv %s %s, %s", out, leftType, left, right))
			}
		case "%":
			if isLLVMFloatType(leftType) {
				b.line(fmt.Sprintf("%s = frem %s %s, %s", out, leftType, left, right))
			} else if isUnsignedMIRType(mirRefType(e.Left)) {
				b.line(fmt.Sprintf("%s = urem %s %s, %s", out, leftType, left, right))
			} else {
				b.line(fmt.Sprintf("%s = srem %s %s, %s", out, leftType, left, right))
			}
		case "==", "!=", "<", "<=", ">", ">=":
			cmp := b.nextReg()
			if isLLVMFloatType(leftType) {
				pred := map[string]string{"==": "oeq", "!=": "one", "<": "olt", "<=": "ole", ">": "ogt", ">=": "oge"}[e.Op]
				b.line(fmt.Sprintf("%s = fcmp %s %s %s, %s", cmp, pred, leftType, left, right))
			} else {
				pred := integerComparePred(e.Op, mirRefType(e.Left))
				b.line(fmt.Sprintf("%s = icmp %s %s %s, %s", cmp, pred, leftType, left, right))
			}
			return cmp
		case "&&", "||":
			lc := emitCondRef(b, e.Left)
			rc := emitCondRef(b, e.Right)
			merged := b.nextReg()
			if e.Op == "&&" {
				b.line(fmt.Sprintf("%s = and i1 %s, %s", merged, lc, rc))
			} else {
				b.line(fmt.Sprintf("%s = or i1 %s, %s", merged, lc, rc))
			}
			return merged
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
		if v.Type == "bool" {
			if v.Value == "0" {
				return "false"
			}
			return "true"
		}
		return v.Value
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
	refType := mirRefType(ref)
	if refType == "bool" {
		return val
	}
	switch v := ref.(type) {
	case *mir.RefConst:
		if v.Type == "f32" || v.Type == "f64" {
			if v.Value == "0" || v.Value == "0.0" {
				return "false"
			}
			return "true"
		}
		if v.Value == "0" {
			return "false"
		}
		return "true"
	default:
		out := b.nextReg()
		llvmType := mustLLVMType(refType)
		if isLLVMFloatType(llvmType) {
			zero := "0.0"
			b.line(fmt.Sprintf("%s = fcmp one %s %s, %s", out, llvmType, val, zero))
		} else {
			b.line(fmt.Sprintf("%s = icmp ne %s %s, 0", out, llvmType, val))
		}
		return out
	}
}

func mirRefType(ref mir.ValueRef) string {
	switch v := ref.(type) {
	case *mir.RefConst:
		return v.Type
	case *mir.RefName:
		return v.Type
	default:
		return "i32"
	}
}

func emitLogicalNot(b *llvmBuilder, arg string, ref mir.ValueRef) string {
	if mirRefType(ref) == "bool" {
		out := b.nextReg()
		b.line(fmt.Sprintf("%s = xor i1 %s, true", out, arg))
		return out
	}
	cmp := emitCondRef(b, ref)
	out := b.nextReg()
	b.line(fmt.Sprintf("%s = xor i1 %s, true", out, cmp))
	return out
}

func integerComparePred(op string, typeText string) string {
	unsigned := isUnsignedMIRType(typeText)
	switch op {
	case "==":
		return "eq"
	case "!=":
		return "ne"
	case "<":
		if unsigned {
			return "ult"
		}
		return "slt"
	case "<=":
		if unsigned {
			return "ule"
		}
		return "sle"
	case ">":
		if unsigned {
			return "ugt"
		}
		return "sgt"
	case ">=":
		if unsigned {
			return "uge"
		}
		return "sge"
	default:
		return "eq"
	}
}

func isUnsignedMIRType(typeText string) bool {
	signed, _, ok := mirParseIntegerType(typeText)
	return ok && !signed
}

func mirParseIntegerType(typeText string) (bool, int, bool) {
	return mirParseIntegerTypeImpl(typeText)
}

func mirParseIntegerTypeImpl(typeText string) (bool, int, bool) {
	return ir.ParseIntegerType(typeText)
}

func isLLVMFloatType(typeText string) bool {
	return typeText == "float" || typeText == "double"
}
