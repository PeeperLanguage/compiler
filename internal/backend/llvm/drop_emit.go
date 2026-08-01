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

func emitDropValue(b *llvmBuilder, value string, typeID ir.TypeID) {
	if b == nil || b.emitter == nil || b.emitter.mod == nil {
		return
	}
	typ, ok := b.emitter.mod.Types.Type(typeID)
	if !ok {
		return
	}
	if isOwnedInterfaceType(b.emitter.mod.Types, typeID) {
		interfaceType := b.emitter.llvmType(typeID)
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
		emitInterfaceStorageRelease(b, typeID, value, data)
		return
	}
	if typ.Kind == ir.TypeString {
		stringType := b.emitter.llvmType(typeID)
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
		byteType := b.emitter.mod.Types.Intern(ir.Type{Kind: ir.TypeByte})
		size := emitAllocatorStorageSize(b, byteType, length)
		emitAllocatorDeallocate(b, allocator, data, size, "1")
		b.line(fmt.Sprintf("br label %%%s", doneLabel))
		b.namedLabel(doneLabel)
		return
	}
	if typ.Kind == ir.TypeOwnedPtr {
		llvmStructType := b.emitter.llvmType(typeID)
		data := b.nextReg()
		b.line(fmt.Sprintf("%s = extractvalue %s %s, 0", data, llvmStructType, value))
		if typeNeedsDrop(b.emitter.mod.Types, typ.Elem) {
			targetType, lowerable := llvmTypeID(b.emitter.mod.Types, typ.Elem)
			if !lowerable {
				b.emitter.markInvalid("owned pointer payload has unsupported drop layout")
				return
			}
			payload := b.nextReg()
			b.line(fmt.Sprintf("%s = load %s, %s* %s", payload, targetType, targetType, data))
			emitDropValue(b, payload, typ.Elem)
		}
		emitOwnedPointerFree(b, value, typeID, typ.Elem)
		return
	}
	if typ.Kind == ir.TypeOptional {
		emitOptionalDrop(b, value, typeID, typ.Elem)
		return
	}
	if typ.Kind == ir.TypeArray && typ.Length == "" {
		emitDynamicArrayDrop(b, value, typeID, typ.Elem)
		return
	}
	if typ.Kind == ir.TypeArray {
		length, err := strconv.Atoi(typ.Length)
		if err != nil {
			b.emitter.markInvalid("fixed array drop has invalid length: " + typ.Length)
			return
		}
		arrayType := b.emitter.llvmType(typeID)
		for index := length - 1; index >= 0; index-- {
			if !typeNeedsDrop(b.emitter.mod.Types, typ.Elem) {
				break
			}
			item := b.nextReg()
			b.line(fmt.Sprintf("%s = extractvalue %s %s, %d", item, arrayType, value, index))
			emitDropValue(b, item, typ.Elem)
		}
		return
	}
	if typ.Kind == ir.TypeStruct {
		structType := b.emitter.llvmType(typeID)
		for index := len(typ.Fields) - 1; index >= 0; index-- {
			fieldType := typ.Fields[index].Type
			if !typeNeedsDrop(b.emitter.mod.Types, fieldType) {
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
	dataType, ok := llvmTypeID(emitter.mod.Types, makeVal.DataType)
	if !ok {
		emitter.markInvalid("unsupported interface payload drop type: " + emitter.mod.Types.Text(makeVal.DataType))
		return
	}
	fmt.Fprintf(out, "define void %s(i8* %%data) {\n", interfaceSymbolName("iface_drop", emitter.mod.Types, makeVal.Type, makeVal.DataType))
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
	dataType, ok := llvmTypeID(emitter.mod.Types, makeVal.DataType)
	if !ok {
		emitter.markInvalid("unsupported interface payload release type: " + emitter.mod.Types.Text(makeVal.DataType))
		return
	}
	fmt.Fprintf(out, "define void %s(i8* %%allocator, i8* %%data) {\n", interfaceSymbolName("iface_release", emitter.mod.Types, makeVal.Type, makeVal.DataType))
	builder := newLLVMBuilder(out, emitter, -1)
	builder.namedLabel("entry")
	size := builder.nextReg()
	builder.line(fmt.Sprintf("%s = ptrtoint %s* getelementptr (%s, %s* null, i32 1) to %s", size, dataType, dataType, dataType, emitter.llvmType(emitter.mod.Types.IndexType())))
	emitAllocatorDeallocate(builder, "%allocator", "%data", size, "8")
	builder.line("ret void")
	out.WriteString("}\n")
}

func emitInterfaceStorageRelease(b *llvmBuilder, interfaceType ir.TypeID, interfaceValue, data string) {
	if b == nil || interfaceValue == "" || data == "" {
		return
	}
	if !isOwnedInterfaceType(b.emitter.mod.Types, interfaceType) {
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

func emitOptionalDrop(b *llvmBuilder, value string, typeID, inner ir.TypeID) {
	if !typeNeedsDrop(b.emitter.mod.Types, inner) {
		return
	}
	optionalType := b.emitter.llvmType(typeID)
	present := b.nextReg()
	b.line(fmt.Sprintf("%s = extractvalue %s %s, 0", present, optionalType, value))
	payload := b.nextReg()
	b.line(fmt.Sprintf("%s = extractvalue %s %s, 1", payload, optionalType, value))
	emitConditionalDrop(b, present, payload, inner)
}

func emitConditionalDrop(b *llvmBuilder, condition, value string, typeID ir.TypeID) {
	id := b.nextID
	b.nextID++
	dropLabel := fmt.Sprintf("drop_some_%d", id)
	doneLabel := fmt.Sprintf("drop_done_%d", id)
	b.line(fmt.Sprintf("br i1 %s, label %%%s, label %%%s", condition, dropLabel, doneLabel))
	b.namedLabel(dropLabel)
	emitDropValue(b, value, typeID)
	b.line(fmt.Sprintf("br label %%%s", doneLabel))
	b.namedLabel(doneLabel)
}

func emitDynamicArrayDrop(b *llvmBuilder, value string, typeID, elem ir.TypeID) {
	arrayType := b.emitter.llvmType(typeID)
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

func emitDynamicArrayElementRangeDrop(b *llvmBuilder, data string, elem ir.TypeID, start, end string) {
	if !typeNeedsDrop(b.emitter.mod.Types, elem) {
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

func emitOwnedPointerFree(b *llvmBuilder, value string, typeID, targetType ir.TypeID) {
	llvmStructType := b.emitter.llvmType(typeID)
	sizeType := b.emitter.llvmType(b.emitter.mod.Types.IndexType())

	data := b.nextReg()
	b.line(fmt.Sprintf("%s = extractvalue %s %s, 0", data, llvmStructType, value))
	desc := b.nextReg()
	b.line(fmt.Sprintf("%s = extractvalue %s %s, 1", desc, llvmStructType, value))

	targetLLVM := b.emitter.llvmType(targetType)
	rawData := b.nextReg()
	b.line(fmt.Sprintf("%s = bitcast %s* %s to i8*", rawData, targetLLVM, data))

	size := b.nextReg()
	b.line(fmt.Sprintf("%s = ptrtoint %s* getelementptr (%s, %s* null, i32 1) to %s", size, targetLLVM, targetLLVM, targetLLVM, sizeType))

	emitAllocatorDeallocate(b, desc, rawData, size, "8")
}

func emitFreeCall(b *llvmBuilder, value string, typeID ir.TypeID) {
	llvmType := b.emitter.llvmType(typeID)
	if llvmType != "i8*" {
		cast := b.nextReg()
		b.line(fmt.Sprintf("%s = bitcast %s %s to i8*", cast, llvmType, value))
		value = cast
	}
	b.line(fmt.Sprintf("call void @free(i8* %s)", value))
}

func typeNeedsDrop(types *ir.TypeTable, id ir.TypeID) bool {
	typ, ok := types.Type(id)
	if !ok {
		return false
	}
	switch typ.Kind {
	case ir.TypeOwnedPtr, ir.TypeString:
		return true
	case ir.TypeOptional:
		return typeNeedsDrop(types, typ.Elem)
	case ir.TypeArray:
		return typ.Length == "" || typeNeedsDrop(types, typ.Elem)
	case ir.TypeStruct:
		for _, field := range typ.Fields {
			if typeNeedsDrop(types, field.Type) {
				return true
			}
		}
	}
	return false
}

func typeCarriesAllocatorID(types *ir.TypeTable, id ir.TypeID) bool {
	typ, ok := types.Type(id)
	if !ok {
		return false
	}
	switch typ.Kind {
	case ir.TypeString:
		return true
	case ir.TypeOwnedPtr:
		return !isInterfaceType(types, typ.Elem)
	case ir.TypeOptional:
		return typeCarriesAllocatorID(types, typ.Elem)
	case ir.TypeArray:
		return typ.Length == "" || typeCarriesAllocatorID(types, typ.Elem)
	case ir.TypeStruct:
		for _, field := range typ.Fields {
			if typeCarriesAllocatorID(types, field.Type) {
				return true
			}
		}
	}
	return false
}

func typeNeedsRawFreeID(types *ir.TypeTable, id ir.TypeID) bool {
	typ, ok := types.Type(id)
	if !ok {
		return false
	}
	switch typ.Kind {
	case ir.TypeOwnedPtr:
		return !isInterfaceType(types, typ.Elem) && typeNeedsRawFreeID(types, typ.Elem)
	case ir.TypeOptional:
		return typeNeedsRawFreeID(types, typ.Elem)
	case ir.TypeArray:
		return typ.Length == "" || typeNeedsRawFreeID(types, typ.Elem)
	case ir.TypeStruct:
		for _, field := range typ.Fields {
			if typeNeedsRawFreeID(types, field.Type) {
				return true
			}
		}
	}
	return false
}
