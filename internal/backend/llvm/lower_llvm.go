package llvm

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"compiler/core/diagnostics"
	"compiler/internal/ir/mir"
	"compiler/internal/tokens"
)

type llvmEmitter struct {
	diag     *diagnostics.DiagnosticBag
	badTypes map[string]struct{}
	invalid  bool
}

func GenerateLLVMIR(mod *mir.Module, diag *diagnostics.DiagnosticBag) string {
	if mod == nil {
		return ""
	}
	emitter := &llvmEmitter{diag: diag, badTypes: make(map[string]struct{})}
	var b strings.Builder
	b.WriteString("source_filename = \"")
	b.WriteString(mod.Name)
	b.WriteString("\"\n")
	b.WriteString("target triple = \"x86_64-pc-linux-gnu\"\n\n")

	decls := collectCallDecls(mod, emitter)
	for _, fn := range mod.Externs {
		b.WriteString("declare ")
		b.WriteString(emitter.llvmType(fn.ReturnType))
		b.WriteString(" @")
		b.WriteString(strings.ReplaceAll(fn.Name, "::", "__"))
		b.WriteString("(")
		for i, param := range fn.Params {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(emitter.llvmType(param.Type))
		}
		b.WriteString(")\n")
	}
	for _, decl := range decls {
		b.WriteString("declare ")
		b.WriteString(emitter.llvmType(decl.ReturnType))
		b.WriteString(" @")
		b.WriteString(strings.ReplaceAll(decl.Name, "::", "__"))
		b.WriteString("(")
		for i, param := range decl.Params {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(emitter.llvmType(param))
		}
		b.WriteString(")\n")
	}
	if len(mod.Externs) > 0 || len(decls) > 0 {
		b.WriteString("\n")
	}
	if len(mod.Funcs) == 0 {
		return finalLLVMText(&b, emitter)
	}
	for _, fn := range mod.Funcs {
		if fn == nil {
			continue
		}
		b.WriteString("define ")
		b.WriteString(emitter.llvmType(fn.ReturnType))
		b.WriteString(" @")
		b.WriteString(strings.ReplaceAll(fn.Name, "::", "__"))
		b.WriteString("(")
		for i, param := range fn.Params {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(emitter.llvmType(param.Type))
			b.WriteString(" %")
			b.WriteString(param.Name)
		}
		b.WriteString(") {\n")
		lb := newLLVMBuilder(&b, emitter)
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
				lb.line("ret " + emitter.llvmType(fn.ReturnType) + " " + val)
			}
		}
		b.WriteString("}\n")
	}
	return finalLLVMText(&b, emitter)
}

func finalLLVMText(b *strings.Builder, emitter *llvmEmitter) string {
	if emitter != nil && emitter.invalid {
		return ""
	}
	if b == nil {
		return ""
	}
	return b.String()
}

func (e *llvmEmitter) llvmType(typeText string) string {
	if mapped, ok := llvmTypeName(typeText); ok {
		return mapped
	}
	if e != nil {
		e.invalid = true
		if e.badTypes == nil {
			e.badTypes = make(map[string]struct{})
		}
		if _, ok := e.badTypes[typeText]; !ok {
			e.badTypes[typeText] = struct{}{}
			if e.diag != nil {
				msg := "unsupported llvm type"
				if typeText != "" {
					msg = msg + ": " + typeText
				}
				e.diag.Add(diagnostics.NewError(msg).WithCode(diagnostics.ErrInvalidType))
			}
		}
	}
	return "i32"
}

func llvmTypeName(typeText string) (string, bool) {
	if signed, bits, ok := tokens.ParseIntegerBuiltin(typeText); ok {
		_ = signed
		return fmt.Sprintf("i%d", bits), true
	}
	switch typeText {
	case "f32":
		return "float", true
	case "f64":
		return "double", true
	case "bool":
		return "i1", true
	case "void":
		return "void", true
	default:
		return "", false
	}
}

type callDecl struct {
	Name       string
	ReturnType string
	Params     []string
}

func collectCallDecls(mod *mir.Module, emitter *llvmEmitter) []callDecl {
	if mod == nil {
		return nil
	}
	defined := make(map[string]struct{})
	for _, ex := range mod.Externs {
		if ex.Name != "" {
			defined[ex.Name] = struct{}{}
		}
	}
	for _, fn := range mod.Funcs {
		if fn != nil && fn.Name != "" {
			defined[fn.Name] = struct{}{}
		}
	}
	decls := make(map[string]callDecl)
	for _, fn := range mod.Funcs {
		if fn == nil {
			continue
		}
		for _, block := range fn.Blocks {
			if block == nil {
				continue
			}
			for _, instr := range block.Instrs {
				assign, ok := instr.(*mir.Assign)
				if !ok || assign == nil {
					continue
				}
				call, ok := assign.Value.(*mir.Call)
				if !ok || call == nil {
					continue
				}
				name, ok := functionNameFromRef(call.Callee)
				if !ok || name == "" {
					continue
				}
				if _, ok := defined[name]; ok {
					continue
				}
				params := make([]string, 0, len(call.Args))
				for _, arg := range call.Args {
					params = append(params, mirRefType(arg))
				}
				if existing, ok := decls[name]; ok {
					if !sameDeclSignature(existing, call.Type, params) {
						if emitter != nil && emitter.diag != nil {
							msg := "conflicting call signatures for " + name
							emitter.diag.Add(diagnostics.NewError(msg).WithCode(diagnostics.ErrInvalidType))
							emitter.invalid = true
						}
					}
					continue
				}
				decls[name] = callDecl{Name: name, ReturnType: call.Type, Params: params}
			}
		}
	}
	if len(decls) == 0 {
		return nil
	}
	out := make([]callDecl, 0, len(decls))
	for _, decl := range decls {
		out = append(out, decl)
	}
	return out
}

func functionNameFromRef(ref mir.ValueRef) (string, bool) {
	nameRef, ok := ref.(*mir.RefName)
	if !ok || nameRef == nil || nameRef.Name == "" {
		return "", false
	}
	name := nameRef.Name
	if idx := strings.IndexByte(name, '$'); idx >= 0 {
		name = name[:idx]
	}
	if name == "" {
		return "", false
	}
	return name, true
}

func sameDeclSignature(existing callDecl, ret string, params []string) bool {
	if existing.ReturnType != ret {
		return false
	}
	if len(existing.Params) != len(params) {
		return false
	}
	for i, param := range params {
		if existing.Params[i] != param {
			return false
		}
	}
	return true
}

type llvmBuilder struct {
	out    *strings.Builder
	nextID int
	locals map[string]string
	types  *llvmEmitter
}

func emitCast(b *llvmBuilder, cast *mir.Cast) string {
	if b == nil || cast == nil || cast.Arg == nil {
		return "0"
	}

	argRef := emitRef(b, cast.Arg)
	fromType := mirRefType(cast.Arg)
	toType := cast.Type

	if fromType == toType {
		return argRef
	}

	if isMIRFloatType(fromType) && isMIRIntegerType(toType) {
		out := b.nextReg()
		fromLLVM := b.types.llvmType(fromType)
		toLLVM := b.types.llvmType(toType)
		if isMIRSignedIntegerType(toType) {
			b.line(fmt.Sprintf("%s = fptosi %s %s to %s", out, fromLLVM, argRef, toLLVM))
		} else {
			b.line(fmt.Sprintf("%s = fptoui %s %s to %s", out, fromLLVM, argRef, toLLVM))
		}
		return out
	} else if isMIRIntegerType(fromType) && isMIRFloatType(toType) {
		out := b.nextReg()
		fromLLVM := b.types.llvmType(fromType)
		toLLVM := b.types.llvmType(toType)
		if isMIRSignedIntegerType(fromType) {
			b.line(fmt.Sprintf("%s = sitofp %s %s to %s", out, fromLLVM, argRef, toLLVM))
		} else {
			b.line(fmt.Sprintf("%s = uitofp %s %s to %s", out, fromLLVM, argRef, toLLVM))
		}
		return out
	} else if isMIRFloatType(fromType) && isMIRFloatType(toType) {
		if fromType == "f64" && toType == "f32" {
			out := b.nextReg()
			b.line(fmt.Sprintf("%s = fptrunc double %s to float", out, argRef))
			return out
		} else if fromType == "f32" && toType == "f64" {
			out := b.nextReg()
			b.line(fmt.Sprintf("%s = fpext float %s to double", out, argRef))
			return out
		}
		return argRef
	} else if isMIRIntegerType(fromType) && isMIRIntegerType(toType) {
		fromBits := mirParseIntegerTypeBits(fromType)
		toBits := mirParseIntegerTypeBits(toType)
		fromLLVM := b.types.llvmType(fromType)
		toLLVM := b.types.llvmType(toType)
		if fromBits < toBits {
			out := b.nextReg()
			if isMIRSignedIntegerType(fromType) {
				b.line(fmt.Sprintf("%s = sext %s %s to %s", out, fromLLVM, argRef, toLLVM))
			} else {
				b.line(fmt.Sprintf("%s = zext %s %s to %s", out, fromLLVM, argRef, toLLVM))
			}
			return out
		} else if fromBits > toBits {
			out := b.nextReg()
			b.line(fmt.Sprintf("%s = trunc %s %s to %s", out, fromLLVM, argRef, toLLVM))
			return out
		}
		return argRef
	}
	return argRef
}

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
			if bits, err := strconv.Atoi(typ[1:]); err == nil {
				return bits
			}
		}
	}
	return 0
}

func newLLVMBuilder(out *strings.Builder, types *llvmEmitter) *llvmBuilder {
	return &llvmBuilder{out: out, nextID: 1, locals: make(map[string]string), types: types}
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
		typ := b.types.llvmType(e.Type)
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
		leftType := b.types.llvmType(mirRefType(e.Left))
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
		callee := emitRef(b, e.Callee)
		llvmType := b.types.llvmType(e.Type)
		out := b.nextReg()
		args := make([]string, 0, len(e.Args))
		for _, arg := range e.Args {
			args = append(args, b.types.llvmType(mirRefType(arg))+" "+emitRef(b, arg))
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
		llvmType := b.types.llvmType(refType)
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
