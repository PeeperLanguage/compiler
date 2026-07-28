package llvm

import (
	"fmt"
	"strings"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/token"
	"compiler/internal/ir"
	"compiler/internal/ir/mir"
)

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
	if typeText == "rawptr" {
		return "i8*", true
	}
	if typeText == "Allocator" {
		return "i8*", true
	}
	if _, ok := ownedInterfaceTypeText(typeText); ok {
		return "{ i8*, i8*, i8* }", true
	}
	if remainder, ok := pointerTypeTextTarget(typeText); ok {
		target, ok := llvmTypeName(remainder)
		if !ok {
			// Unknown pointee still lowers as pointer-sized storage.
			return "{ i8*, i8* }", true
		}
		return "{ " + target + "*, i8* }", true
	}
	if innerTypeText, ok := optionalInnerTypeText(typeText); ok {
		if niche, ok := optionalNicheLayout(innerTypeText); ok {
			return niche.llvmType, true
		}
		inner, ok := llvmTypeName(innerTypeText)
		if !ok {
			return "", false
		}
		return "{ i1, " + inner + " }", true
	}
	if remainder, ok := strings.CutPrefix(typeText, "[]"); ok {
		elem, ok := llvmTypeName(strings.TrimSpace(remainder))
		if !ok {
			return "", false
		}
		return "{ " + elem + "*, i64, i64, i8* }", true
	}
	if remainder, ok := referenceTypeTextTarget(typeText); ok {
		return llvmRefTypeName(remainder)
	}
	if length, elemTypeText, ok := ir.ArrayTypeParts(typeText); ok {
		elem, ok := llvmTypeName(elemTypeText)
		if !ok {
			return "", false
		}
		return "[" + length + " x " + elem + "]", true
	}
	if strings.HasPrefix(typeText, "iface{") && strings.HasSuffix(typeText, "}") {
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
	if _, bits, ok := mirIntegerInfo(typeText); ok {
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
	case "string":
		return "{ i8*, i64, i8* }", true
	default:
		return "", false
	}
}

func mirIntegerInfo(typeText string) (signed bool, bits int, ok bool) {
	if typeText == "byte" {
		return false, 8, true
	}
	return token.ParseIntegerBuiltin(typeText)
}

func optionalInnerTypeText(typeText string) (string, bool) {
	inner, ok := strings.CutPrefix(strings.TrimSpace(typeText), "?")
	if !ok {
		return "", false
	}
	inner = strings.TrimSpace(inner)
	return inner, inner != ""
}

type optionalNiche struct {
	llvmType string
	none     string
}

func optionalNicheLayout(typeText string) (optionalNiche, bool) {
	// TODO(#26): Owned pointers (*T) no longer qualify — their runtime layout is
	// {T*, i8*} (struct), not T* (null-safe pointer). Future types with invalid
	// bit patterns (e.g. non-null pointers after ZST optimization, enums with
	// discriminant gaps) may restore niche usage.
	return optionalNiche{}, false
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
	parts := []string{"rawptr"}
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

func itabSymbolName(interfaceType, dataType string) string {
	raw := fmt.Sprintf("__itab__%s__%s", interfaceType, dataType)
	return "@" + ir.SanitizeSymbolName(raw)
}

func interfaceDropSymbolName(interfaceType, dataType string) string {
	raw := fmt.Sprintf("__iface_drop__%s__%s", interfaceType, dataType)
	return "@" + ir.SanitizeSymbolName(raw)
}

func interfaceReleaseSymbolName(interfaceType, dataType string) string {
	raw := fmt.Sprintf("__iface_release__%s__%s", interfaceType, dataType)
	return "@" + ir.SanitizeSymbolName(raw)
}

const interfaceReleaseVtableSlot = 1

func interfaceMethodVtableSlot(interfaceTypeText string, methodSlot int) int {
	methodOffset := interfaceReleaseVtableSlot
	if _, owned := ownedInterfaceTypeText(interfaceTypeText); owned {
		methodOffset++
	}
	return methodOffset + methodSlot
}

func interfaceVtableLength(interfaceTypeText string, methodCount int) int {
	return interfaceMethodVtableSlot(interfaceTypeText, methodCount)
}

func interfaceSlotLLVMTypeFromInterface(interfaceTypeText string, slot int) (string, bool) {
	interfaceTypeText, ok := runtimeInterfaceTypeText(interfaceTypeText)
	if !ok {
		return "", false
	}
	body := strings.TrimSuffix(strings.TrimPrefix(interfaceTypeText, "iface{"), "}")
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

func llvmRefTypeName(target string) (string, bool) {
	target = strings.TrimSpace(target)
	if elemTypeText, ok := strings.CutPrefix(target, "[]"); ok {
		elemTypeText = strings.TrimSpace(elemTypeText)
		elem, ok := llvmTypeName(elemTypeText)
		if !ok {
			return "", false
		}
		return "{ " + elem + "*, i64 }", true
	}
	if strings.HasPrefix(target, "iface{") && strings.HasSuffix(target, "}") {
		return llvmTypeName(target)
	}
	elem, ok := llvmTypeName(target)
	if !ok {
		return "", false
	}
	return elem + "*", true
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
	case *mir.SliceView:
		return v.Type
	case *mir.Load:
		return v.Type
	case *mir.Field:
		return v.Type
	case *mir.StructLit:
		return v.Type
	case *mir.ArrayLit:
		return v.Type
	case *mir.DynamicArrayAlloc:
		return v.Type
	case *mir.DynamicArrayOp:
		return v.Type
	case *mir.ZeroValue:
		return v.Type
	case *mir.OptionalSome:
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

func pointerTypeTextTarget(typeText string) (string, bool) {
	typeText = strings.TrimSpace(typeText)
	if remainder, ok := strings.CutPrefix(typeText, "*"); ok {
		remainder = strings.TrimSpace(remainder)
		return remainder, remainder != ""
	}
	return "", false
}

func ownedInterfaceTypeText(typeText string) (string, bool) {
	target, ok := pointerTypeTextTarget(typeText)
	if !ok {
		return "", false
	}
	return runtimeInterfaceTypeText(target)
}

func runtimeInterfaceTypeText(typeText string) (string, bool) {
	typeText = strings.TrimSpace(typeText)
	if target, ok := referenceTypeTextTarget(typeText); ok {
		typeText = target
	} else if target, ok := pointerTypeTextTarget(typeText); ok {
		typeText = target
	}
	if strings.HasPrefix(typeText, "iface{") && strings.HasSuffix(typeText, "}") {
		return typeText, true
	}
	return "", false
}

func referenceTypeTextTarget(typeText string) (string, bool) {
	typeText = strings.TrimSpace(typeText)
	for _, prefix := range []string{"&mut ", "&"} {
		if remainder, ok := strings.CutPrefix(typeText, prefix); ok {
			remainder = strings.TrimSpace(remainder)
			return remainder, remainder != ""
		}
	}
	return "", false
}
