package llvm

import (
	"fmt"
	"strconv"
	"strings"

	"compiler/internal/ir"
	"compiler/internal/ir/mir"
)

func emitDrop(b *llvmBuilder, instr *mir.Drop) {
	if b == nil || instr == nil || instr.Value == nil {
		return
	}
	emitDropValue(b, emitRef(b, instr.Value), mirRefType(instr.Value))
}

func emitDropValue(b *llvmBuilder, value, typeText string) {
	typeText = strings.TrimSpace(typeText)
	if _, ok := ownedInterfaceTypeText(typeText); ok {
		interfaceType := b.emitter.llvmType(typeText)
		data := b.nextReg()
		b.line(fmt.Sprintf("%s = extractvalue %s %s, 0", data, interfaceType, value))
		itab := b.nextReg()
		b.line(fmt.Sprintf("%s = extractvalue %s %s, 1", itab, interfaceType, value))
		vtable := b.nextReg()
		b.line(fmt.Sprintf("%s = bitcast i8* %s to i8**", vtable, itab))
		dropSlot := b.nextReg()
		b.line(fmt.Sprintf("%s = load i8*, i8** %s", dropSlot, vtable))
		dropFn := b.nextReg()
		b.line(fmt.Sprintf("%s = bitcast i8* %s to void (i8*)*", dropFn, dropSlot))
		b.line(fmt.Sprintf("call void %s(i8* %s)", dropFn, data))
		emitFreeCall(b, data, "rawptr")
		return
	}
	if target, ok := pointerTypeTextTarget(typeText); ok {
		if typeTextNeedsDrop(target) {
			targetType, lowerable := llvmTypeName(target)
			if !lowerable {
				b.emitter.markInvalid("owned pointer payload has unsupported drop layout: " + target)
				return
			}
			payload := b.nextReg()
			b.line(fmt.Sprintf("%s = load %s, %s* %s", payload, targetType, targetType, value))
			emitDropValue(b, payload, target)
		}
		emitFreeCall(b, value, typeText)
		return
	}
	if typeText == "string" {
		data := b.nextReg()
		b.line(fmt.Sprintf("%s = extractvalue { i8*, i64 } %s, 0", data, value))
		b.line(fmt.Sprintf("call void @free(i8* %s)", data))
		return
	}
	if inner, ok := optionalInnerTypeText(typeText); ok {
		emitOptionalDrop(b, value, typeText, inner)
		return
	}
	if elem, ok := strings.CutPrefix(typeText, "[]"); ok {
		emitDynamicArrayDrop(b, value, typeText, strings.TrimSpace(elem))
		return
	}
	if lengthText, elem, ok := ir.ArrayTypeParts(typeText); ok {
		length, err := strconv.Atoi(lengthText)
		if err != nil {
			b.emitter.markInvalid("fixed array drop has invalid length: " + lengthText)
			return
		}
		arrayType := b.emitter.llvmType(typeText)
		for index := length - 1; index >= 0; index-- {
			if !typeTextNeedsDrop(elem) {
				break
			}
			item := b.nextReg()
			b.line(fmt.Sprintf("%s = extractvalue %s %s, %d", item, arrayType, value, index))
			emitDropValue(b, item, elem)
		}
		return
	}
	if strings.HasPrefix(typeText, "struct{") && strings.HasSuffix(typeText, "}") {
		fields := structFieldTypeTexts(typeText)
		structType := b.emitter.llvmType(typeText)
		for index := len(fields) - 1; index >= 0; index-- {
			if !typeTextNeedsDrop(fields[index]) {
				continue
			}
			field := b.nextReg()
			b.line(fmt.Sprintf("%s = extractvalue %s %s, %d", field, structType, value, index))
			emitDropValue(b, field, fields[index])
		}
	}
}

func emitInterfacePayloadDropThunk(out *strings.Builder, emitter *llvmEmitter, makeVal *mir.InterfaceMake) {
	if out == nil || emitter == nil || makeVal == nil {
		return
	}
	dataType, ok := llvmTypeName(makeVal.DataType)
	if !ok {
		emitter.markInvalid("unsupported interface payload drop type: " + makeVal.DataType)
		return
	}
	fmt.Fprintf(out, "define void %s(i8* %%data) {\n", interfaceDropSymbolName(makeVal.Type, makeVal.DataType))
	builder := newLLVMBuilder(out, emitter, -1)
	builder.namedLabel("entry")
	typed := builder.nextReg()
	builder.line(fmt.Sprintf("%s = bitcast i8* %%data to %s*", typed, dataType))
	value := builder.nextReg()
	builder.line(fmt.Sprintf("%s = load %s, %s* %s", value, dataType, dataType, typed))
	emitDropValue(builder, value, makeVal.DataType)
	builder.line("ret void")
	out.WriteString("}\n")
}

func emitOptionalDrop(b *llvmBuilder, value, typeText, inner string) {
	if !typeTextNeedsDrop(inner) {
		return
	}
	if _, niche := optionalNicheLayout(inner); niche {
		llvmType := b.emitter.llvmType(typeText)
		present := b.nextReg()
		b.line(fmt.Sprintf("%s = icmp ne %s %s, null", present, llvmType, value))
		emitConditionalDrop(b, present, value, inner)
		return
	}
	optionalType := b.emitter.llvmType(typeText)
	present := b.nextReg()
	b.line(fmt.Sprintf("%s = extractvalue %s %s, 0", present, optionalType, value))
	payload := b.nextReg()
	b.line(fmt.Sprintf("%s = extractvalue %s %s, 1", payload, optionalType, value))
	emitConditionalDrop(b, present, payload, inner)
}

func emitConditionalDrop(b *llvmBuilder, condition, value, typeText string) {
	id := b.nextID
	b.nextID++
	dropLabel := fmt.Sprintf("drop_some_%d", id)
	doneLabel := fmt.Sprintf("drop_done_%d", id)
	b.line(fmt.Sprintf("br i1 %s, label %%%s, label %%%s", condition, dropLabel, doneLabel))
	b.namedLabel(dropLabel)
	emitDropValue(b, value, typeText)
	b.line(fmt.Sprintf("br label %%%s", doneLabel))
	b.namedLabel(doneLabel)
}

func emitDynamicArrayDrop(b *llvmBuilder, value, typeText, elem string) {
	arrayType := b.emitter.llvmType(typeText)
	data := b.nextReg()
	b.line(fmt.Sprintf("%s = extractvalue %s %s, 0", data, arrayType, value))
	length := b.nextReg()
	b.line(fmt.Sprintf("%s = extractvalue %s %s, 1", length, arrayType, value))
	if typeTextNeedsDrop(elem) {
		id := b.nextID
		b.nextID++
		entryLabel := b.currentLabel
		loopLabel := fmt.Sprintf("drop_array_loop_%d", id)
		bodyLabel := fmt.Sprintf("drop_array_body_%d", id)
		continueLabel := fmt.Sprintf("drop_array_continue_%d", id)
		doneLabel := fmt.Sprintf("drop_array_done_%d", id)
		b.line(fmt.Sprintf("br label %%%s", loopLabel))
		b.namedLabel(loopLabel)
		remaining := b.nextReg()
		index := b.nextReg()
		b.line(fmt.Sprintf("%s = phi i64 [ %s, %%%s ], [ %s, %%%s ]", remaining, length, entryLabel, index, continueLabel))
		more := b.nextReg()
		b.line(fmt.Sprintf("%s = icmp ugt i64 %s, 0", more, remaining))
		b.line(fmt.Sprintf("br i1 %s, label %%%s, label %%%s", more, bodyLabel, doneLabel))
		b.namedLabel(bodyLabel)
		b.line(fmt.Sprintf("%s = sub i64 %s, 1", index, remaining))
		elemType := b.emitter.llvmType(elem)
		ptr := b.nextReg()
		b.line(fmt.Sprintf("%s = getelementptr %s, %s* %s, i64 %s", ptr, elemType, elemType, data, index))
		item := b.nextReg()
		b.line(fmt.Sprintf("%s = load %s, %s* %s", item, elemType, elemType, ptr))
		emitDropValue(b, item, elem)
		b.line(fmt.Sprintf("br label %%%s", continueLabel))
		b.namedLabel(continueLabel)
		b.line(fmt.Sprintf("br label %%%s", loopLabel))
		b.namedLabel(doneLabel)
	}
	emitFreeCall(b, data, "rawptr")
}

func emitFreeCall(b *llvmBuilder, value, typeText string) {
	llvmType := b.emitter.llvmType(typeText)
	if llvmType != "i8*" {
		cast := b.nextReg()
		b.line(fmt.Sprintf("%s = bitcast %s %s to i8*", cast, llvmType, value))
		value = cast
	}
	b.line(fmt.Sprintf("call void @free(i8* %s)", value))
}

func typeTextNeedsDrop(typeText string) bool {
	typeText = strings.TrimSpace(typeText)
	if _, ok := pointerTypeTextTarget(typeText); ok || typeText == "string" {
		return true
	}
	if inner, ok := optionalInnerTypeText(typeText); ok {
		return typeTextNeedsDrop(inner)
	}
	if _, ok := strings.CutPrefix(typeText, "[]"); ok {
		return true
	}
	if _, elem, ok := ir.ArrayTypeParts(typeText); ok {
		return typeTextNeedsDrop(elem)
	}
	for _, field := range structFieldTypeTexts(typeText) {
		if typeTextNeedsDrop(field) {
			return true
		}
	}
	return false
}

func structFieldTypeTexts(typeText string) []string {
	if !strings.HasPrefix(typeText, "struct{") || !strings.HasSuffix(typeText, "}") {
		return nil
	}
	body := strings.TrimSuffix(strings.TrimPrefix(typeText, "struct{"), "}")
	fields := splitTopLevel(body, ';')
	types := make([]string, 0, len(fields))
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		if _, remainder, ok := strings.Cut(field, ":"); ok {
			field = strings.TrimSpace(remainder)
		}
		types = append(types, field)
	}
	return types
}
