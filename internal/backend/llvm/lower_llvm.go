package llvm

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"compiler/pkg/diagnostics"
	"compiler/internal/ir"
	"compiler/internal/ir/mir"
	"compiler/internal/tokens"
)

type llvmEmitter struct {
	mod             *mir.Module
	diag            *diagnostics.DiagnosticBag
	badTypes        map[string]struct{}
	invalid         bool
	externalGlobals map[string]string
}

func GenerateLLVMIR(mod *mir.Module, diag *diagnostics.DiagnosticBag) string {
	if mod == nil {
		return ""
	}

	emitter := &llvmEmitter{
		mod:             mod,
		diag:            diag,
		badTypes:        make(map[string]struct{}),
		externalGlobals: make(map[string]string),
	}
	var b strings.Builder
	b.WriteString("source_filename = \"")
	b.WriteString(mod.Name)
	b.WriteString("\"\n")
	b.WriteString("target triple = \"x86_64-pc-linux-gnu\"\n\n")

	for _, entry := range mod.StaticData {
		isStr := entry.Type == "cstr" || (strings.HasPrefix(entry.Type, "[") && strings.HasSuffix(entry.Type, " x i8]"))
		if isStr {
			escaped := llvmEscapeString(entry.Value)
			b.WriteString(fmt.Sprintf("%s = private unnamed_addr constant %s c\"%s\", align %d\n", entry.Name, entry.Type, escaped, entry.Align))
		} else {
			llvmType := emitter.llvmType(entry.Type)
			b.WriteString(fmt.Sprintf("%s = constant %s %s, align %d\n", entry.Name, llvmType, entry.Value, entry.Align))
		}
	}
	if len(mod.StaticData) > 0 {
		b.WriteString("\n")
	}

	// Collect and emit unique static itabs used by InterfaceMake instructions
	emittedItabs := make(map[string]bool)
	hasItab := false
	for _, fn := range mod.Funcs {
		if fn == nil || fn.Blocks == nil {
			continue
		}
		for _, block := range fn.Blocks {
			if block == nil {
				continue
			}
			for _, instr := range block.Instrs {
				assign, ok := instr.(*mir.Assign)
				if !ok || assign == nil || assign.Value == nil {
					continue
				}
				makeVal, ok := assign.Value.(*mir.InterfaceMake)
				if !ok || makeVal == nil {
					continue
				}
				itabSym := itabSymbolName(makeVal.Type, makeVal.DataType)
				if emittedItabs[itabSym] {
					continue
				}
				emittedItabs[itabSym] = true
				hasItab = true

				b.WriteString(itabSym)
				b.WriteString(fmt.Sprintf(" = private constant [%d x i8*] [", len(makeVal.Slots)))
				for i, slot := range makeVal.Slots {
					if i > 0 {
						b.WriteString(", ")
					}
					refName, ok := slot.(*mir.RefName)
					slotName := ""
					if ok && refName != nil {
						slotName = "@" + ir.SanitizeSymbolName(ir.StripSymbolInstance(refName.Name))
					} else {
						slotName = "null"
					}
					slotType, ok := interfaceSlotLLVMTypeFromInterface(makeVal.Type, i)
					if !ok {
						slotType = "i8*"
					}
					if slotName == "null" {
						b.WriteString("i8* null")
					} else {
						b.WriteString(fmt.Sprintf("i8* bitcast (%s %s to i8*)", slotType, slotName))
					}
				}
				b.WriteString("], align 8\n")
			}
		}
	}
	if hasItab {
		b.WriteString("\n")
	}

	hasDecl := false
	for _, fn := range mod.Funcs {
		if fn == nil || fn.Blocks != nil {
			continue
		}
		hasDecl = true
		b.WriteString("declare ")
		b.WriteString(emitter.llvmType(fn.ReturnType))
		b.WriteString(" @")
		b.WriteString(ir.SanitizeSymbolName(fn.Name))
		b.WriteString("(")
		for i, param := range fn.Params {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(emitter.llvmType(param.Type))
		}
		b.WriteString(")\n")
	}
	if hasDecl {
		b.WriteString("\n")
	}
	if usesInterfaceBoxing(mod) {
		b.WriteString("declare i8* @malloc(i64)\n\n")
	}

	decls := collectCallDecls(mod)
	for _, decl := range decls {
		b.WriteString("declare ")
		b.WriteString(emitter.llvmType(decl.ReturnType))
		b.WriteString(" @")
		b.WriteString(ir.SanitizeSymbolName(decl.Name))
		b.WriteString("(")
		for i, param := range decl.Params {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(emitter.llvmType(param))
		}
		b.WriteString(")\n")
	}
	if len(decls) > 0 {
		b.WriteString("\n")
	}

	hasDefine := false
	for _, fn := range mod.Funcs {
		if fn != nil && fn.Blocks != nil {
			hasDefine = true
			break
		}
	}
	if !hasDefine {
		return finalLLVMText(&b, emitter)
	}
	for _, thunk := range mod.InterfaceThunks {
		emitInterfaceThunk(&b, emitter, thunk)
	}
	if len(mod.InterfaceThunks) > 0 {
		b.WriteString("\n")
	}
	for _, fn := range mod.Funcs {
		if fn == nil || fn.Blocks == nil {
			continue
		}
		b.WriteString("define ")
		b.WriteString(emitter.llvmType(fn.ReturnType))
		b.WriteString(" @")
		b.WriteString(ir.SanitizeSymbolName(fn.Name))
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
			lb.localTypes[param.Name] = param.Type
		}
		for _, block := range fn.Blocks {
			if block == nil {
				continue
			}
			b.WriteString(fmt.Sprintf("b%d:\n", block.ID))
			for _, instr := range block.Instrs {
				if assign, ok := instr.(*mir.Assign); ok && assign != nil {
					val := emitValueExpr(lb, assign.Value)
					valueType := mirValueType(assign.Value)
					if ptr, ok := lb.localPtrs[assign.Name]; ok && ptr != "" {
						llvmType := lb.types.llvmType(lb.localTypes[assign.Name])
						lb.line(fmt.Sprintf("store %s %s, %s* %s", llvmType, val, llvmType, ptr))
					} else {
						lb.locals[assign.Name] = val
						if valueType != "" {
							lb.localTypes[assign.Name] = valueType
						}
					}
					continue
				}
				if store, ok := instr.(*mir.StoreField); ok && store != nil {
					emitStoreField(lb, store)
				}
			}
			if block.Term != nil {
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
	if emitter != nil && len(emitter.externalGlobals) > 0 {
		b.WriteString("\n; external globals\n")
		for name, typeText := range emitter.externalGlobals {
			llvmType := emitter.llvmType(typeText)
			b.WriteString(fmt.Sprintf("%s = external global %s\n", name, llvmType))
		}
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

func (e *llvmEmitter) markInvalid(msg string) {
	if e == nil {
		return
	}
	e.invalid = true
	if e.diag != nil {
		e.diag.Add(diagnostics.NewError(msg).WithCode(diagnostics.ErrInvalidType))
	}
}

func llvmTypeName(typeText string) (string, bool) {
	typeText = strings.TrimSpace(typeText)
	if strings.HasPrefix(typeText, "fn(") {
		return llvmFunctionPtrType(typeText)
	}
	if remainder, ok := strings.CutPrefix(typeText, "^"); ok {
		target, ok := llvmTypeName(strings.TrimSpace(remainder))
		if !ok {
			return "", false
		}
		return target + "*", true
	}
	if strings.HasPrefix(typeText, "interface{") && strings.HasSuffix(typeText, "}") {
		return "{ i8*, i8* }", true
	}
	if strings.HasPrefix(typeText, "struct{") && strings.HasSuffix(typeText, "}") {
		body := strings.TrimSuffix(strings.TrimPrefix(typeText, "struct{"), "}")
		fields := splitTopLevel(body, ';')
		parts := make([]string, 0, len(fields))
		for _, field := range fields {
			field = strings.TrimSpace(field)
			if field == "" {
				continue
			}
			fieldTypeText := field
			if _, remainder, ok := strings.Cut(field, ":"); ok {
				fieldTypeText = strings.TrimSpace(remainder)
			}
			fieldType, ok := llvmTypeName(fieldTypeText)
			if !ok {
				return "", false
			}
			parts = append(parts, fieldType)
		}
		return "{ " + strings.Join(parts, ", ") + " }", true
	}
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
	case "cstr":
		return "i8*", true
	default:
		return "", false
	}
}

func llvmFunctionPtrType(typeText string) (string, bool) {
	typeText = strings.TrimSpace(typeText)
	if !strings.HasPrefix(typeText, "fn(") {
		return "", false
	}
	start := strings.Index(typeText, "(")
	end := matchingParenIndex(typeText, start)
	if start < 0 || end < 0 {
		return "", false
	}
	paramsText := strings.TrimSpace(typeText[start+1 : end])
	returnText := "void"
	remainder := strings.TrimSpace(typeText[end+1:])
	if after, ok := strings.CutPrefix(remainder, "->"); ok {
		ret, ok := llvmTypeName(strings.TrimSpace(after))
		if !ok {
			return "", false
		}
		returnText = ret
	}
	params := splitTopLevel(paramsText, ',')
	llvmParams := make([]string, 0, len(params))
	for _, param := range params {
		param = strings.TrimSpace(param)
		if param == "" {
			continue
		}
		if idx := topLevelColonIndex(param); idx >= 0 {
			param = strings.TrimSpace(param[idx+1:])
		}
		llvmParam, ok := llvmTypeName(param)
		if !ok {
			return "", false
		}
		llvmParams = append(llvmParams, llvmParam)
	}
	return returnText + " (" + strings.Join(llvmParams, ", ") + ")*", true
}

func interfaceMethodSlotTypeText(methodText string) (string, bool) {
	open := strings.Index(methodText, "(")
	if open < 0 {
		return "", false
	}
	close := matchingParenIndex(methodText, open)
	if close < 0 {
		return "", false
	}
	paramsText := strings.TrimSpace(methodText[open+1 : close])
	parts := []string{"^u8"}
	params := splitTopLevel(paramsText, ',')
	for i, param := range params {
		if i == 0 {
			continue
		}
		param = strings.TrimSpace(param)
		if param == "" {
			continue
		}
		if idx := topLevelColonIndex(param); idx >= 0 {
			param = strings.TrimSpace(param[idx+1:])
		}
		parts = append(parts, param)
	}
	var b strings.Builder
	b.WriteString("fn(")
	b.WriteString(strings.Join(parts, ", "))
	b.WriteString(")")
	remainder := strings.TrimSpace(methodText[close+1:])
	if strings.HasPrefix(remainder, ":") {
		b.WriteString(" -> ")
		b.WriteString(strings.TrimSpace(strings.TrimPrefix(remainder, ":")))
	}
	return b.String(), true
}

func matchingParenIndex(text string, open int) int {
	if open < 0 || open >= len(text) || text[open] != '(' {
		return -1
	}
	depth := 0
	for i := open; i < len(text); i++ {
		switch text[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func topLevelColonIndex(text string) int {
	depth := 0
	for i, r := range text {
		switch r {
		case '{', '(', '[':
			depth++
		case '}', ')', ']':
			if depth > 0 {
				depth--
			}
		case ':':
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func splitTopLevel(text string, sep rune) []string {
	if text == "" {
		return nil
	}
	parts := make([]string, 0, 4)
	depth := 0
	start := 0
	for i, r := range text {
		switch r {
		case '{', '(', '[':
			depth++
		case '}', ')', ']':
			if depth > 0 {
				depth--
			}
		default:
			if r == sep && depth == 0 {
				parts = append(parts, text[start:i])
				start = i + 1
			}
		}
	}
	parts = append(parts, text[start:])
	return parts
}

func usesInterfaceBoxing(mod *mir.Module) bool {
	if mod == nil {
		return false
	}
	for _, fn := range mod.Funcs {
		if fn == nil || fn.Blocks == nil {
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
				makeVal, ok := assign.Value.(*mir.InterfaceMake)
				if ok && makeVal != nil && makeVal.BoxValue && !makeVal.StackBox {
					return true
				}
			}
		}
	}
	return false
}

func itabSymbolName(interfaceType, dataType string) string {
	raw := fmt.Sprintf("__itab__%s__%s", interfaceType, dataType)
	return "@" + ir.SanitizeSymbolName(raw)
}

func interfaceSlotLLVMTypeFromInterface(interfaceTypeText string, slot int) (string, bool) {
	interfaceTypeText = strings.TrimSpace(interfaceTypeText)
	if !strings.HasPrefix(interfaceTypeText, "interface{") || !strings.HasSuffix(interfaceTypeText, "}") {
		return "", false
	}
	body := strings.TrimSuffix(strings.TrimPrefix(interfaceTypeText, "interface{"), "}")
	methods := splitTopLevel(body, ';')
	if slot < 0 || slot >= len(methods) {
		return "", false
	}
	slotTypeText, ok := interfaceMethodSlotTypeText(strings.TrimSpace(methods[slot]))
	if !ok {
		return "", false
	}
	return llvmTypeName(slotTypeText)
}

func emitInterfaceThunk(out *strings.Builder, emitter *llvmEmitter, thunk *mir.InterfaceThunk) {
	if out == nil || emitter == nil || thunk == nil {
		return
	}
	actualLLVMType, ok := llvmTypeName(thunk.FuncType)
	if !ok {
		emitter.markInvalid("unsupported interface thunk function type: " + thunk.FuncType)
		return
	}
	actualFn, actualRet, actualParams, ok := parseFunctionTypeText(thunk.FuncType)
	if !ok || actualFn != actualLLVMType {
		emitter.markInvalid("failed to parse interface thunk function type: " + thunk.FuncType)
		return
	}
	dataLLVMType, ok := llvmTypeName(thunk.DataType)
	if !ok {
		emitter.markInvalid("unsupported interface thunk data type: " + thunk.DataType)
		return
	}
	_, slotRet, slotParams, ok := parseFunctionTypeText(thunk.SlotType)
	if !ok || len(slotParams) == 0 || len(actualParams) == 0 {
		emitter.markInvalid("failed to parse interface thunk slot type: " + thunk.SlotType)
		return
	}
	out.WriteString("define ")
	out.WriteString(slotRet)
	out.WriteString(" @")
	out.WriteString(thunk.Name)
	out.WriteString("(")
	for i, param := range slotParams {
		if i > 0 {
			out.WriteString(", ")
		}
		out.WriteString(param)
		out.WriteString(" %p")
		out.WriteString(strconv.Itoa(i))
	}
	out.WriteString(") {\n")
	builder := newLLVMBuilder(out, emitter)
	if strings.HasSuffix(actualParams[0], "*") {
		cast := builder.nextReg()
		builder.line(fmt.Sprintf("%s = bitcast i8* %%p0 to %s", cast, actualParams[0]))
		callArgs := []string{actualParams[0] + " " + cast}
		for i := 1; i < len(slotParams); i++ {
			callArgs = append(callArgs, actualParams[i]+" %p"+strconv.Itoa(i))
		}
		emitThunkTargetCall(builder, actualRet, thunk.FuncName, callArgs)
	} else {
		cast := builder.nextReg()
		builder.line(fmt.Sprintf("%s = bitcast i8* %%p0 to %s*", cast, dataLLVMType))
		loaded := builder.nextReg()
		builder.line(fmt.Sprintf("%s = load %s, %s* %s", loaded, dataLLVMType, dataLLVMType, cast))
		callArgs := []string{actualParams[0] + " " + loaded}
		for i := 1; i < len(slotParams); i++ {
			callArgs = append(callArgs, actualParams[i]+" %p"+strconv.Itoa(i))
		}
		emitThunkTargetCall(builder, actualRet, thunk.FuncName, callArgs)
	}
	out.WriteString("}\n")
}

func emitThunkTargetCall(builder *llvmBuilder, returnType string, funcName string, callArgs []string) {
	if builder == nil {
		return
	}
	symbol := ir.SanitizeSymbolName(ir.StripSymbolInstance(funcName))
	if returnType == "void" {
		builder.line(fmt.Sprintf("call %s @%s(%s)", returnType, symbol, strings.Join(callArgs, ", ")))
		builder.line("ret void")
		return
	}
	result := builder.nextReg()
	builder.line(fmt.Sprintf("%s = call %s @%s(%s)", result, returnType, symbol, strings.Join(callArgs, ", ")))
	builder.line("ret " + returnType + " " + result)
}

func emitStoreField(b *llvmBuilder, store *mir.StoreField) {
	if b == nil || store == nil || store.Base == nil || store.Value == nil {
		return
	}
	base := emitRef(b, store.Base)
	baseType := mirRefType(store.Base)
	llvmPtrType, ok := llvmTypeName(baseType)
	if !ok {
		return
	}
	structTypeText, ok := pointedTypeText(baseType)
	if !ok {
		return
	}
	llvmStructType, ok := llvmTypeName(structTypeText)
	if !ok {
		return
	}
	ptr := b.nextReg()
	b.line(fmt.Sprintf("%s = getelementptr inbounds %s, %s %s, i32 0, i32 %d", ptr, llvmStructType, llvmPtrType, base, store.Index))
	value := emitRef(b, store.Value)
	valueType := b.types.llvmType(mirRefType(store.Value))
	b.line(fmt.Sprintf("store %s %s, %s* %s", valueType, value, valueType, ptr))
}

func mirValueType(expr mir.ValueExpr) string {
	switch v := expr.(type) {
	case *mir.Move:
		return mirRefType(v.Src)
	case *mir.Unary:
		return v.Type
	case *mir.Binary:
		return v.Type
	case *mir.Cast:
		return v.Type
	case *mir.AddrOf:
		return v.Type
	case *mir.Field:
		return v.Type
	case *mir.StructLit:
		return v.Type
	case *mir.InterfaceMake:
		return v.Type
	case *mir.InterfaceCall:
		return v.Type
	case *mir.Call:
		return v.Type
	default:
		return ""
	}
}

func parseFunctionTypeText(typeText string) (string, string, []string, bool) {
	fnType, ok := llvmTypeName(typeText)
	if !ok {
		return "", "", nil, false
	}
	open := strings.Index(fnType, "(")
	close := matchingParenIndex(fnType, open)
	if open < 0 || close < 0 || !strings.HasSuffix(fnType, "*") {
		return "", "", nil, false
	}
	ret := strings.TrimSpace(fnType[:open])
	paramsText := strings.TrimSpace(fnType[open+1 : close])
	params := splitTopLevel(paramsText, ',')
	out := make([]string, 0, len(params))
	for _, param := range params {
		param = strings.TrimSpace(param)
		if param != "" {
			out = append(out, param)
		}
	}
	return fnType, ret, out, true
}

func emitInterfaceBoxedData(b *llvmBuilder, value mir.ValueRef, dataType string, stackBox bool) string {
	if b == nil || value == nil {
		return "null"
	}
	llvmType := b.types.llvmType(dataType)
	if stackBox {
		typed := b.nextReg()
		b.line(fmt.Sprintf("%s = alloca %s", typed, llvmType))
		val := emitRef(b, value)
		b.line(fmt.Sprintf("store %s %s, %s* %s", llvmType, val, llvmType, typed))
		cast := b.nextReg()
		b.line(fmt.Sprintf("%s = bitcast %s* %s to i8*", cast, llvmType, typed))
		return cast
	}
	sizePtr := b.nextReg()
	b.line(fmt.Sprintf("%s = getelementptr %s, %s* null, i32 1", sizePtr, llvmType, llvmType))
	size := b.nextReg()
	b.line(fmt.Sprintf("%s = ptrtoint %s* %s to i64", size, llvmType, sizePtr))
	mem := b.nextReg()
	b.line(fmt.Sprintf("%s = call i8* @malloc(i64 %s)", mem, size))
	typed := b.nextReg()
	b.line(fmt.Sprintf("%s = bitcast i8* %s to %s*", typed, mem, llvmType))
	val := emitRef(b, value)
	b.line(fmt.Sprintf("store %s %s, %s* %s", llvmType, val, llvmType, typed))
	return mem
}

func pointedTypeText(typeText string) (string, bool) {
	typeText = strings.TrimSpace(typeText)
	if remainder, ok := strings.CutPrefix(typeText, "^"); ok {
		return strings.TrimSpace(remainder), true
	}
	return "", false
}

// collectCallDecls deleted

type llvmBuilder struct {
	out        *strings.Builder
	nextID     int
	locals     map[string]string
	localPtrs  map[string]string
	localTypes map[string]string
	types      *llvmEmitter
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
	return &llvmBuilder{
		out:        out,
		nextID:     1,
		locals:     make(map[string]string),
		localPtrs:  make(map[string]string),
		localTypes: make(map[string]string),
		types:      types,
	}
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
	case *mir.AddrOf:
		if ref, ok := e.Base.(*mir.RefName); ok && ref != nil {
			if ptr := ensureLocalAddr(b, ref); ptr != "" {
				return ptr
			}
		}
		baseType := mirRefType(e.Base)
		llvmBaseType := b.types.llvmType(baseType)
		ptr := b.nextReg()
		b.line(fmt.Sprintf("%s = alloca %s", ptr, llvmBaseType))
		value := emitRef(b, e.Base)
		b.line(fmt.Sprintf("store %s %s, %s* %s", llvmBaseType, value, llvmBaseType, ptr))
		return ptr
	case *mir.Field:
		base := emitRef(b, e.Base)
		baseType := mirRefType(e.Base)
		if e.ThroughPtr {
			llvmPtrType, ok := llvmTypeName(baseType)
			if !ok {
				return "0"
			}
			structTypeText, ok := pointedTypeText(baseType)
			if !ok {
				return "0"
			}
			llvmStructType, ok := llvmTypeName(structTypeText)
			if !ok {
				return "0"
			}
			ptr := b.nextReg()
			b.line(fmt.Sprintf("%s = getelementptr inbounds %s, %s %s, i32 0, i32 %d", ptr, llvmStructType, llvmPtrType, base, e.Index))
			out := b.nextReg()
			llvmFieldType := b.types.llvmType(e.Type)
			b.line(fmt.Sprintf("%s = load %s, %s* %s", out, llvmFieldType, llvmFieldType, ptr))
			return out
		}
		llvmBaseType, ok := llvmTypeName(baseType)
		if !ok {
			return "0"
		}
		out := b.nextReg()
		b.line(fmt.Sprintf("%s = extractvalue %s %s, %d", out, llvmBaseType, base, e.Index))
		return out
	case *mir.StructLit:
		llvmType := b.types.llvmType(e.Type)
		current := "zeroinitializer"
		for i, field := range e.Fields {
			value := emitRef(b, field)
			next := b.nextReg()
			fieldType := b.types.llvmType(mirRefType(field))
			b.line(fmt.Sprintf("%s = insertvalue %s %s, %s %s, %d", next, llvmType, current, fieldType, value, i))
			current = next
		}
		return current
	case *mir.InterfaceMake:
		llvmType := b.types.llvmType(e.Type)
		dataPtr := "null"
		if e.BoxValue {
			dataPtr = emitInterfaceBoxedData(b, e.Value, e.DataType, e.StackBox)
		} else {
			value := emitRef(b, e.Value)
			valueType := b.types.llvmType(mirRefType(e.Value))
			cast := b.nextReg()
			b.line(fmt.Sprintf("%s = bitcast %s %s to i8*", cast, valueType, value))
			dataPtr = cast
		}
		itabPtr := "null"
		if len(e.Slots) > 0 {
			itabSym := itabSymbolName(e.Type, e.DataType)
			itabCast := b.nextReg()
			b.line(fmt.Sprintf("%s = bitcast [%d x i8*]* %s to i8*", itabCast, len(e.Slots), itabSym))
			itabPtr = itabCast
		}
		current := "zeroinitializer"
		reg1 := b.nextReg()
		b.line(fmt.Sprintf("%s = insertvalue %s %s, i8* %s, 0", reg1, llvmType, current, dataPtr))
		reg2 := b.nextReg()
		b.line(fmt.Sprintf("%s = insertvalue %s %s, i8* %s, 1", reg2, llvmType, reg1, itabPtr))
		return reg2
	case *mir.InterfaceCall:
		base := emitRef(b, e.Base)
		baseType := b.types.llvmType(mirRefType(e.Base))
		data := b.nextReg()
		b.line(fmt.Sprintf("%s = extractvalue %s %s, 0", data, baseType, base))
		itab := b.nextReg()
		b.line(fmt.Sprintf("%s = extractvalue %s %s, 1", itab, baseType, base))
		slotType, ok := interfaceSlotLLVMTypeFromInterface(mirRefType(e.Base), e.Slot)
		if !ok {
			return "0"
		}
		vtable := b.nextReg()
		b.line(fmt.Sprintf("%s = bitcast i8* %s to i8**", vtable, itab))
		fnPtrPtr := b.nextReg()
		b.line(fmt.Sprintf("%s = getelementptr inbounds i8*, i8** %s, i32 %d", fnPtrPtr, vtable, e.Slot))
		fnI8 := b.nextReg()
		b.line(fmt.Sprintf("%s = load i8*, i8** %s", fnI8, fnPtrPtr))
		fn := b.nextReg()
		b.line(fmt.Sprintf("%s = bitcast i8* %s to %s", fn, fnI8, slotType))
		out := b.nextReg()
		args := []string{"i8* " + data}
		for _, arg := range e.Args {
			args = append(args, b.types.llvmType(mirRefType(arg))+" "+emitRef(b, arg))
		}
		b.line(fmt.Sprintf("%s = call %s %s(%s)", out, b.types.llvmType(e.Type), fn, strings.Join(args, ", ")))
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
		if v.Type == "cstr" {
			return "null"
		}
		return v.Value
	case *mir.RefName:
		isFunc := strings.HasPrefix(v.Type, "fn(") || strings.Contains(v.Type, "->")
		if ptr, ok := b.localPtrs[v.Name]; ok && ptr != "" {
			reg := b.nextReg()
			llvmType := b.types.llvmType(b.localTypes[v.Name])
			b.line(fmt.Sprintf("%s = load %s, %s* %s", reg, llvmType, llvmType, ptr))
			return reg
		}
		if reg, ok := b.locals[v.Name]; ok {
			return reg
		}
		if isFunc {
			return "@" + ir.SanitizeSymbolName(ir.StripSymbolInstance(v.Name))
		}

		isLocalStatic := false
		var localEntry *mir.StaticEntry
		if b.types != nil && b.types.mod != nil {
			for _, entry := range b.types.mod.StaticData {
				eName := strings.TrimPrefix(entry.Name, "@")
				vName := strings.TrimPrefix(v.Name, "@")
				if eName == vName {
					isLocalStatic = true
					localEntry = entry
					break
				}
			}
		}

		if isLocalStatic && localEntry != nil {
			if strings.HasPrefix(localEntry.Type, "[") && strings.HasSuffix(localEntry.Type, " x i8]") {
				return fmt.Sprintf("getelementptr inbounds (%s, %s* %s, i64 0, i64 0)", localEntry.Type, localEntry.Type, localEntry.Name)
			}
			reg := b.nextReg()
			llvmType := b.types.llvmType(localEntry.Type)
			b.line(fmt.Sprintf("%s = load %s, %s* %s, align %d", reg, llvmType, llvmType, localEntry.Name, localEntry.Align))
			return reg
		}

		if idx := strings.IndexByte(v.Name, '$'); idx >= 0 {
			name := "@" + v.Name
			if b.types.externalGlobals == nil {
				b.types.externalGlobals = make(map[string]string)
			}
			b.types.externalGlobals[name] = v.Type

			reg := b.nextReg()
			llvmType := b.types.llvmType(v.Type)
			b.line(fmt.Sprintf("%s = load %s, %s* %s, align 4", reg, llvmType, llvmType, name))
			return reg
		}

		if strings.HasPrefix(v.Name, "@") {
			return v.Name
		}
		return "0"
	default:
		return "0"
	}
}

func ensureLocalAddr(b *llvmBuilder, ref *mir.RefName) string {
	if b == nil || ref == nil {
		return ""
	}
	if ptr, ok := b.localPtrs[ref.Name]; ok && ptr != "" {
		return ptr
	}
	reg, ok := b.locals[ref.Name]
	if !ok || reg == "" {
		return ""
	}
	typeText := b.localTypes[ref.Name]
	if typeText == "" {
		typeText = ref.Type
	}
	if typeText == "" {
		return ""
	}
	llvmType := b.types.llvmType(typeText)
	ptr := b.nextReg()
	b.line(fmt.Sprintf("%s = alloca %s", ptr, llvmType))
	b.line(fmt.Sprintf("store %s %s, %s* %s", llvmType, reg, llvmType, ptr))
	b.localPtrs[ref.Name] = ptr
	b.localTypes[ref.Name] = typeText
	return ptr
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

func llvmEscapeString(s string) string {
	var sb strings.Builder
	for i := 0; i < len(s); i++ {
		b := s[i]
		if b == '\\' {
			sb.WriteString(`\5C`)
		} else if b == '"' {
			sb.WriteString(`\22`)
		} else if b >= 32 && b <= 126 {
			sb.WriteByte(b)
		} else {
			sb.WriteString(fmt.Sprintf("\\%02X", b))
		}
	}
	sb.WriteString(`\00`)
	return sb.String()
}

type callDecl struct {
	Name       string
	ReturnType string
	Params     []string
}

func collectCallDecls(mod *mir.Module) []callDecl {
	if mod == nil {
		return nil
	}
	defined := make(map[string]struct{})
	for _, fn := range mod.Funcs {
		if fn != nil && fn.Name != "" {
			defined[fn.Name] = struct{}{}
		}
	}
	decls := make(map[string]callDecl)
	for _, fn := range mod.Funcs {
		if fn == nil || fn.Blocks == nil {
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
				nameRef, ok := call.Callee.(*mir.RefName)
				if !ok || nameRef == nil {
					continue
				}
				name := nameRef.Name
				if idx := strings.IndexByte(name, '$'); idx >= 0 {
					name = name[:idx]
				}
				if _, ok := defined[name]; ok {
					continue
				}
				params := make([]string, 0, len(call.Args))
				for _, arg := range call.Args {
					params = append(params, mirRefType(arg))
				}
				decls[name] = callDecl{Name: name, ReturnType: call.Type, Params: params}
			}
		}
	}
	out := make([]callDecl, 0, len(decls))
	for _, decl := range decls {
		out = append(out, decl)
	}
	return out
}
