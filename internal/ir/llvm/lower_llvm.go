package llvm

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"compiler/internal/ir/mir"
	"compiler/internal/tokens"
)

func GenerateLLVMIR(mod *mir.Module) string {
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
	if signed, bits, ok := tokens.ParseIntegerBuiltin(typeText); ok {
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

func emitCast(b *llvmBuilder, cast *mir.Cast) string {
	if b == nil || cast == nil || cast.Arg == nil {
		return "0"
	}

	argRef := emitRef(b, cast.Arg)
	fromType := mirRefType(cast.Arg)
	toType := cast.Type

	// If types are the same, no cast needed
	if fromType == toType {
		return argRef
	}

	// Handle numeric conversions
	if isMIRFloatType(fromType) && isMIRIntegerType(toType) {
		// Float to integer: use fptosi or fptoui
		out := b.nextReg()
		if isMIRSignedIntegerType(toType) {
			b.line(fmt.Sprintf("%s = fptosi %s %s to %s", out, mustLLVMType(fromType), argRef, mustLLVMType(toType)))
		} else {
			b.line(fmt.Sprintf("%s = fptoui %s %s to %s", out, mustLLVMType(fromType), argRef, mustLLVMType(toType)))
		}
		return out
	} else if isMIRIntegerType(fromType) && isMIRFloatType(toType) {
		// Integer to float: use sitofp or uitofp
		out := b.nextReg()
		if isMIRSignedIntegerType(fromType) {
			b.line(fmt.Sprintf("%s = sitofp %s %s to %s", out, mustLLVMType(fromType), argRef, mustLLVMType(toType)))
		} else {
			b.line(fmt.Sprintf("%s = uitofp %s %s to %s", out, mustLLVMType(fromType), argRef, mustLLVMType(toType)))
		}
		return out
	} else if isMIRFloatType(fromType) && isMIRFloatType(toType) {
		// Float to float: use fptrunc or fpext
		if fromType == "f64" && toType == "f32" {
			out := b.nextReg()
			b.line(fmt.Sprintf("%s = fptrunc double %s to float", out, argRef))
			return out
		} else if fromType == "f32" && toType == "f64" {
			out := b.nextReg()
			b.line(fmt.Sprintf("%s = fpext float %s to double", out, argRef))
			return out
		} else {
			// Same type, no conversion needed
			return argRef
		}
	} else if isMIRIntegerType(fromType) && isMIRIntegerType(toType) {
		// Integer to integer: use trunc, zext, or sext
		fromBits := mirParseIntegerTypeBits(fromType)
		toBits := mirParseIntegerTypeBits(toType)
		if fromBits < toBits {
			// Extend
			out := b.nextReg()
			if isMIRSignedIntegerType(fromType) {
				b.line(fmt.Sprintf("%s = sext %s %s to %s", out, mustLLVMType(fromType), argRef, mustLLVMType(toType)))
			} else {
				b.line(fmt.Sprintf("%s = zext %s %s to %s", out, mustLLVMType(fromType), argRef, mustLLVMType(toType)))
			}
			return out
		} else if fromBits > toBits {
			// Truncate
			out := b.nextReg()
			b.line(fmt.Sprintf("%s = trunc %s %s to %s", out, mustLLVMType(fromType), argRef, mustLLVMType(toType)))
			return out
		} else {
			// Same size, no conversion
			return argRef
		}
	} else {
		// Unsupported conversion, return default
		return argRef
	}
}

// Helper functions for type checking
func isMIRFloatType(typ string) bool {
	return typ == "f32" || typ == "f64"
}

func isMIRIntegerType(typ string) bool {
	return strings.HasPrefix(typ, "i") || strings.HasPrefix(typ, "u")
}

func isMIRSignedIntegerType(typ string) bool {
	return strings.HasPrefix(typ, "i")
}

func mirParseIntegerTypeBits(typ string) int {
	if isMIRIntegerType(typ) {
		if len(typ) > 1 {
			// Extract the bit width from the type name (e.g., "i32" -> 32)
			if bits, err := strconv.Atoi(typ[1:]); err == nil {
				return bits
			}
		}
	}
	return 0
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
	fmt.Fprintf(b.out, "b%d:\n", id)
}

func emitValueExpr(b *llvmBuilder, expr mir.ValueExpr) string {
	switch e := expr.(type) {
	case *mir.Move:
		return emitRef(b, e.Src)
	case *mir.Cast:
		return emitCast(b, e)
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
	case *mir.Call:
		// Emit function call
		callee := emitRef(b, e.Callee)
		llvmType := mustLLVMType(e.Type)
		out := b.nextReg()
		// Build argument list
		args := make([]string, 0, len(e.Args))
		for _, arg := range e.Args {
			args = append(args, mustLLVMType(mirRefType(arg))+" "+emitRef(b, arg))
		}
		b.line(fmt.Sprintf("%s = call %s %s(%s)", out, llvmType, callee, strings.Join(args, ", ")))
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
		if v.Type == "f32" {
			return llvmFloat32Const(v.Value)
		}
		return v.Value
	case *mir.RefName:
		if reg, ok := b.locals[v.Name]; ok {
			return reg
		}
		// Check if this is a function reference (symbol name with $ID suffix)
		// Strip the $ID suffix to get the function name
		if idx := strings.IndexByte(v.Name, '$'); idx >= 0 {
			funcName := v.Name[:idx]
			return "@" + strings.ReplaceAll(funcName, "::", "__")
		}
		return "0"
	default:
		return "0"
	}
}

func llvmFloat32Const(value string) string {
	parsed, err := strconv.ParseFloat(value, 32)
	if err != nil {
		return value
	}
	return fmt.Sprintf("0x%016X", math.Float64bits(float64(float32(parsed))))
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
	signed, _, ok := tokens.ParseIntegerBuiltin(typeText)
	return ok && !signed
}

func isLLVMFloatType(typeText string) bool {
	return typeText == "float" || typeText == "double"
}
