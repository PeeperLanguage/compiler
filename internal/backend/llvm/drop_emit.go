package llvm

import (
	"fmt"
	"slices"
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
		emitInterfaceStorageRelease(b, typeText, value, data)
		return
	}
	if typeText == "string" {
		stringType := b.emitter.llvmType(typeText)
		data := b.nextReg()
		b.line(fmt.Sprintf("%s = extractvalue %s %s, 0", data, stringType, value))
		length := b.nextReg()
		b.line(fmt.Sprintf("%s = extractvalue %s %s, 1", length, stringType, value))
		allocator := b.nextReg()
		b.line(fmt.Sprintf("%s = extractvalue %s %s, 2", allocator, stringType, value))
		nonNull := b.nextReg()
		b.line(fmt.Sprintf("%s = icmp ne i8* %s, null", nonNull, data))
		id := b.nextID
		b.nextID++
		releaseLabel := fmt.Sprintf("drop_string_release_%d", id)
		doneLabel := fmt.Sprintf("drop_string_done_%d", id)
		b.line(fmt.Sprintf("br i1 %s, label %%%s, label %%%s", nonNull, releaseLabel, doneLabel))
		b.namedLabel(releaseLabel)
		size := emitAllocatorStorageSize(b, "byte", length)
		emitAllocatorDeallocate(b, allocator, data, size, "1")
		b.line(fmt.Sprintf("br label %%%s", doneLabel))
		b.namedLabel(doneLabel)
		return
	}
	if target, ok := pointerTypeTextTarget(typeText); ok {
		llvmStructType := b.emitter.llvmType(typeText)
		data := b.nextReg()
		b.line(fmt.Sprintf("%s = extractvalue %s %s, 0", data, llvmStructType, value))
		if typeTextNeedsDrop(target) {
			targetType, lowerable := llvmTypeName(target)
			if !lowerable {
				b.emitter.markInvalid("owned pointer payload has unsupported drop layout: " + target)
				return
			}
			payload := b.nextReg()
			b.line(fmt.Sprintf("%s = load %s, %s* %s", payload, targetType, targetType, data))
			emitDropValue(b, payload, target)
		}
		emitOwnedPointerFree(b, value, typeText, target)
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
		for index, fieldType := range slices.Backward(fields) {
			if !typeTextNeedsDrop(fieldType) {
				continue
			}
			field := b.nextReg()
			b.line(fmt.Sprintf("%s = extractvalue %s %s, %d", field, structType, value, index))
			emitDropValue(b, field, fieldType)
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

func emitInterfacePayloadReleaseThunk(out *strings.Builder, emitter *llvmEmitter, makeVal *mir.InterfaceMake) {
	if out == nil || emitter == nil || makeVal == nil {
		return
	}
	dataType, ok := llvmTypeName(makeVal.DataType)
	if !ok {
		emitter.markInvalid("unsupported interface payload release type: " + makeVal.DataType)
		return
	}
	fmt.Fprintf(out, "define void %s(i8* %%allocator, i8* %%data) {\n", interfaceReleaseSymbolName(makeVal.Type, makeVal.DataType))
	builder := newLLVMBuilder(out, emitter, -1)
	builder.namedLabel("entry")
	size := builder.nextReg()
	builder.line(fmt.Sprintf("%s = ptrtoint %s* getelementptr (%s, %s* null, i32 1) to %s", size, dataType, dataType, dataType, emitter.llvmType("usize")))
	emitAllocatorDeallocate(builder, "%allocator", "%data", size, "8")
	builder.line("ret void")
	out.WriteString("}\n")
}

func emitInterfaceStorageRelease(b *llvmBuilder, interfaceType, interfaceValue, data string) {
	if b == nil || interfaceValue == "" || data == "" {
		return
	}
	if _, owned := ownedInterfaceTypeText(interfaceType); !owned {
		return
	}
	llvmType := b.emitter.llvmType(interfaceType)
	itab := b.nextReg()
	b.line(fmt.Sprintf("%s = extractvalue %s %s, 1", itab, llvmType, interfaceValue))
	allocator := b.nextReg()
	b.line(fmt.Sprintf("%s = extractvalue %s %s, 2", allocator, llvmType, interfaceValue))
	vtable := b.nextReg()
	b.line(fmt.Sprintf("%s = bitcast i8* %s to i8**", vtable, itab))
	releaseSlot := b.nextReg()
	b.line(fmt.Sprintf("%s = getelementptr inbounds i8*, i8** %s, i32 %d", releaseSlot, vtable, interfaceReleaseVtableSlot))
	releaseI8 := b.nextReg()
	b.line(fmt.Sprintf("%s = load i8*, i8** %s", releaseI8, releaseSlot))
	releaseFn := b.nextReg()
	b.line(fmt.Sprintf("%s = bitcast i8* %s to void (i8*, i8*)*", releaseFn, releaseI8))
	b.line(fmt.Sprintf("call void %s(i8* %s, i8* %s)", releaseFn, allocator, data))
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
	capacity := b.nextReg()
	b.line(fmt.Sprintf("%s = extractvalue %s %s, 2", capacity, arrayType, value))
	allocator := b.nextReg()
	b.line(fmt.Sprintf("%s = extractvalue %s %s, 3", allocator, arrayType, value))
	emitDynamicArrayElementRangeDrop(b, data, elem, "0", length)
	nonNull := b.nextReg()
	b.line(fmt.Sprintf("%s = icmp ne %s* %s, null", nonNull, b.emitter.llvmType(elem), data))
	id := b.nextID
	b.nextID++
	releaseLabel := fmt.Sprintf("drop_array_release_%d", id)
	doneLabel := fmt.Sprintf("drop_array_done_%d", id)
	b.line(fmt.Sprintf("br i1 %s, label %%%s, label %%%s", nonNull, releaseLabel, doneLabel))
	b.namedLabel(releaseLabel)
	size := emitAllocatorStorageSize(b, elem, capacity)
	rawData := b.nextReg()
	b.line(fmt.Sprintf("%s = bitcast %s* %s to i8*", rawData, b.emitter.llvmType(elem), data))
	emitAllocatorDeallocate(b, allocator, rawData, size, "8")
	b.line(fmt.Sprintf("br label %%%s", doneLabel))
	b.namedLabel(doneLabel)
}

func emitDynamicArrayElementRangeDrop(b *llvmBuilder, data, elem, start, end string) {
	if !typeTextNeedsDrop(elem) {
		return
	}
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
	b.line(fmt.Sprintf("%s = phi i64 [ %s, %%%s ], [ %s, %%%s ]", remaining, end, entryLabel, index, continueLabel))
	more := b.nextReg()
	b.line(fmt.Sprintf("%s = icmp ugt i64 %s, %s", more, remaining, start))
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

func emitOwnedPointerFree(b *llvmBuilder, value, typeText, targetTypeText string) {
	llvmStructType := b.emitter.llvmType(typeText)
	sizeType := b.emitter.llvmType("usize")

	data := b.nextReg()
	b.line(fmt.Sprintf("%s = extractvalue %s %s, 0", data, llvmStructType, value))
	desc := b.nextReg()
	b.line(fmt.Sprintf("%s = extractvalue %s %s, 1", desc, llvmStructType, value))

	targetLLVM := b.emitter.llvmType(targetTypeText)
	rawData := b.nextReg()
	b.line(fmt.Sprintf("%s = bitcast %s* %s to i8*", rawData, targetLLVM, data))

	size := b.nextReg()
	b.line(fmt.Sprintf("%s = ptrtoint %s* getelementptr (%s, %s* null, i32 1) to %s", size, targetLLVM, targetLLVM, targetLLVM, sizeType))

	emitAllocatorDeallocate(b, desc, rawData, size, "8")
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
	return slices.ContainsFunc(structFieldTypeTexts(typeText), typeTextNeedsDrop)
}

func typeCarriesAllocator(typeText string) bool {
	typeText = strings.TrimSpace(typeText)
	if typeText == "string" {
		return true
	}
	if _, ok := ownedInterfaceTypeText(typeText); ok {
		return true
	}
	if inner, ok := optionalInnerTypeText(typeText); ok {
		return typeCarriesAllocator(inner)
	}
	if _, ok := strings.CutPrefix(typeText, "[]"); ok {
		return true
	}
	if target, ok := pointerTypeTextTarget(typeText); ok {
		_, isInterface := ownedInterfaceTypeText(typeText)
		return !isInterface && target != ""
	}
	if _, elem, ok := ir.ArrayTypeParts(typeText); ok {
		return typeCarriesAllocator(elem)
	}
	return slices.ContainsFunc(structFieldTypeTexts(typeText), typeCarriesAllocator)
}

func typeTextNeedsRawFree(typeText string) bool {
	typeText = strings.TrimSpace(typeText)
	if _, ok := ownedInterfaceTypeText(typeText); ok {
		return false
	}
	if target, ok := pointerTypeTextTarget(typeText); ok {
		return typeTextNeedsRawFree(target)
	}
	if inner, ok := optionalInnerTypeText(typeText); ok {
		return typeTextNeedsRawFree(inner)
	}
	if elem, ok := strings.CutPrefix(typeText, "[]"); ok {
		return typeTextNeedsRawFree(elem)
	}
	if _, elem, ok := ir.ArrayTypeParts(typeText); ok {
		return typeTextNeedsRawFree(elem)
	}
	return slices.ContainsFunc(structFieldTypeTexts(typeText), typeTextNeedsRawFree)
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
