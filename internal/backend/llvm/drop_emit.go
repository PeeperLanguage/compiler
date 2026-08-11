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

func emitDropValue(b *llvmBuilder, value llvmValue, typeID ir.TypeID) {
	if b == nil || b.emitter == nil || b.emitter.mod == nil {
		return
	}
	typ, ok := b.emitter.mod.Types.Type(typeID)
	if !ok {
		return
	}
	if isOwnedInterfaceType(b.emitter.mod.Types, typeID) {
		data := b.extractField(value, llvmFieldData)
		itab := b.extractField(value, llvmFieldDispatch)
		rawPointer := llvmPointerLayout(llvmScalarLayout("i8"))
		vtable := b.bitcast(itab, llvmPointerLayout(rawPointer))
		dropSlot := b.load(b.pointerPlace(vtable))
		dropFn := b.bitcast(dropSlot, llvmFunctionLayout(&llvmLayout{Text: "void", Kind: llvmLayoutVoid}, []*llvmLayout{rawPointer}))
		b.call(dropFn, []llvmValue{data})
		emitInterfaceStorageRelease(b, typeID, value, data)
		return
	}
	if typ.Kind == ir.TypeString {
		data := b.extractField(value, llvmFieldData)
		length := b.extractField(value, llvmFieldLength)
		allocator := b.extractField(value, llvmFieldAllocator)
		nonNull := b.compare("icmp", "ne", data, b.value("null", data.Layout))
		allocatorPresent := b.compare("icmp", "ne", allocator, b.value("null", allocator.Layout))
		canRelease := b.arithmetic("and", nonNull, allocatorPresent)
		id := b.nextID
		b.nextID++
		releaseLabel := fmt.Sprintf("drop_string_release_%d", id)
		doneLabel := fmt.Sprintf("drop_string_done_%d", id)
		b.condBranch(canRelease, releaseLabel, doneLabel)
		b.namedLabel(releaseLabel)
		byteType := b.emitter.mod.Types.Intern(ir.Type{Kind: ir.TypeByte})
		size := emitAllocatorStorageSize(b, byteType, length)
		emitAllocatorDeallocate(b, allocator, data, size, b.value("1", llvmScalarLayout("i32")))
		b.branch(doneLabel)
		b.namedLabel(doneLabel)
		return
	}
	if typ.Kind == ir.TypeOwnedPtr {
		data := b.extractField(value, llvmFieldData)
		if typeNeedsDrop(b.emitter.mod.Types, typ.Elem) {
			if b.emitter.layout(typ.Elem) == nil {
				b.emitter.markInvalid("owned pointer payload has unsupported drop layout")
				return
			}
			payload := b.load(b.pointerPlace(data))
			emitDropValue(b, payload, typ.Elem)
		}
		emitOwnedPointerFree(b, value, typ.Elem)
		return
	}
	if typ.Kind == ir.TypeOptional {
		emitOptionalDrop(b, value, typ.Elem)
		return
	}
	if typ.Kind == ir.TypeArray && typ.Length == "" {
		emitDynamicArrayDrop(b, value, typ.Elem)
		return
	}
	if typ.Kind == ir.TypeArray {
		length, err := strconv.Atoi(typ.Length)
		if err != nil {
			b.emitter.markInvalid("fixed array drop has invalid length: " + typ.Length)
			return
		}
		for index := length - 1; index >= 0; index-- {
			if !typeNeedsDrop(b.emitter.mod.Types, typ.Elem) {
				break
			}
			item := b.extractIndex(value, index)
			emitDropValue(b, item, typ.Elem)
		}
		return
	}
	if typ.Kind == ir.TypeStruct {
		for index := len(typ.Fields) - 1; index >= 0; index-- {
			fieldType := typ.Fields[index].Type
			if !typeNeedsDrop(b.emitter.mod.Types, fieldType) {
				continue
			}
			field := b.extractIndex(value, index)
			emitDropValue(b, field, fieldType)
		}
	}
}

func emitInterfacePayloadDropThunk(out *strings.Builder, emitter *llvmEmitter, makeVal *mir.InterfaceMake) {
	if out == nil || emitter == nil || makeVal == nil {
		return
	}
	dataLayout := emitter.layout(makeVal.DataType)
	if dataLayout == nil {
		emitter.markInvalid("unsupported interface payload drop type: " + emitter.mod.Types.Text(makeVal.DataType))
		return
	}
	fmt.Fprintf(out, "define void %s(i8* %%data) {\n", interfaceSymbolName("iface_drop", emitter.mod.Types, makeVal.Type, makeVal.DataType))
	builder := newLLVMBuilder(out, emitter, -1)
	builder.namedLabel("entry")
	rawPointer := llvmPointerLayout(llvmScalarLayout("i8"))
	typed := builder.bitcast(builder.value("%data", rawPointer), llvmPointerLayout(dataLayout))
	value := builder.load(builder.pointerPlace(typed))
	emitDropValue(builder, value, makeVal.DataType)
	builder.retVoid(&llvmLayout{Text: "void", Kind: llvmLayoutVoid})
	out.WriteString("}\n")
}

func emitInterfacePayloadReleaseThunk(out *strings.Builder, emitter *llvmEmitter, makeVal *mir.InterfaceMake) {
	if out == nil || emitter == nil || makeVal == nil {
		return
	}
	dataLayout := emitter.layout(makeVal.DataType)
	if dataLayout == nil {
		emitter.markInvalid("unsupported interface payload release type: " + emitter.mod.Types.Text(makeVal.DataType))
		return
	}
	fmt.Fprintf(out, "define void %s(i8* %%allocator, i8* %%data) {\n", interfaceSymbolName("iface_release", emitter.mod.Types, makeVal.Type, makeVal.DataType))
	builder := newLLVMBuilder(out, emitter, -1)
	builder.namedLabel("entry")
	rawPointer := llvmPointerLayout(llvmScalarLayout("i8"))
	sizeLayout := emitter.layout(emitter.mod.Types.IndexType())
	payloadEnd := builder.value(fmt.Sprintf("getelementptr (%s, %s* null, i32 1)", dataLayout.Text, dataLayout.Text), llvmPointerLayout(dataLayout))
	size := builder.cast("ptrtoint", payloadEnd, sizeLayout)
	emitAllocatorDeallocate(builder, builder.value("%allocator", rawPointer), builder.value("%data", rawPointer), size, builder.value("8", llvmScalarLayout("i32")))
	builder.retVoid(&llvmLayout{Text: "void", Kind: llvmLayoutVoid})
	out.WriteString("}\n")
}

func emitInterfaceStorageRelease(b *llvmBuilder, interfaceType ir.TypeID, interfaceValue, data llvmValue) {
	if b == nil {
		return
	}
	if !isOwnedInterfaceType(b.emitter.mod.Types, interfaceType) {
		return
	}
	itab := b.extractField(interfaceValue, llvmFieldDispatch)
	allocator := b.extractField(interfaceValue, llvmFieldAllocator)
	rawPointer := llvmPointerLayout(llvmScalarLayout("i8"))
	vtable := b.bitcast(itab, llvmPointerLayout(rawPointer))
	releaseSlot := b.gep(b.pointerPlace(vtable), b.value(strconv.Itoa(interfaceReleaseVtableSlot), llvmScalarLayout("i32")), true)
	releaseI8 := b.load(releaseSlot)
	releaseFn := b.bitcast(releaseI8, llvmFunctionLayout(&llvmLayout{Text: "void", Kind: llvmLayoutVoid}, []*llvmLayout{rawPointer, rawPointer}))
	b.call(releaseFn, []llvmValue{allocator, data})
}

func emitOptionalDrop(b *llvmBuilder, value llvmValue, inner ir.TypeID) {
	if !typeNeedsDrop(b.emitter.mod.Types, inner) {
		return
	}
	present := b.extractField(value, llvmFieldPresent)
	payload := b.extractField(value, llvmFieldValue)
	emitConditionalDrop(b, present, payload, inner)
}

func emitConditionalDrop(b *llvmBuilder, condition, value llvmValue, typeID ir.TypeID) {
	id := b.nextID
	b.nextID++
	dropLabel := fmt.Sprintf("drop_some_%d", id)
	doneLabel := fmt.Sprintf("drop_done_%d", id)
	b.condBranch(condition, dropLabel, doneLabel)
	b.namedLabel(dropLabel)
	emitDropValue(b, value, typeID)
	b.branch(doneLabel)
	b.namedLabel(doneLabel)
}

func emitDynamicArrayDrop(b *llvmBuilder, value llvmValue, elem ir.TypeID) {
	data := b.extractField(value, llvmFieldData)
	length := b.extractField(value, llvmFieldLength)
	capacity := b.extractField(value, llvmFieldCapacity)
	allocator := b.extractField(value, llvmFieldAllocator)
	emitDynamicArrayElementRangeDrop(b, data, elem, b.value("0", length.Layout), length)
	nonNull := b.compare("icmp", "ne", data, b.value("null", data.Layout))
	id := b.nextID
	b.nextID++
	releaseLabel := fmt.Sprintf("drop_array_release_%d", id)
	doneLabel := fmt.Sprintf("drop_array_done_%d", id)
	b.condBranch(nonNull, releaseLabel, doneLabel)
	b.namedLabel(releaseLabel)
	size := emitAllocatorStorageSize(b, elem, capacity)
	rawData := b.bitcast(data, llvmPointerLayout(llvmScalarLayout("i8")))
	emitAllocatorDeallocate(b, allocator, rawData, size, b.value("8", llvmScalarLayout("i32")))
	b.branch(doneLabel)
	b.namedLabel(doneLabel)
}

func emitDynamicArrayElementRangeDrop(b *llvmBuilder, data llvmValue, elem ir.TypeID, start, end llvmValue) {
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
	b.branch(loopLabel)
	b.namedLabel(loopLabel)
	index := b.nextValue(end.Layout)
	remaining := b.nextValue(end.Layout)
	b.definePhi(remaining, llvmIncoming{Value: end, Label: entryLabel}, llvmIncoming{Value: index, Label: continueLabel})
	more := b.compare("icmp", "ugt", remaining, start)
	b.condBranch(more, bodyLabel, doneLabel)
	b.namedLabel(bodyLabel)
	b.defineArithmetic(index, "sub", remaining, b.value("1", end.Layout))
	ptr := b.gep(b.pointerPlace(data), index, false)
	item := b.load(ptr)
	emitDropValue(b, item, elem)
	b.branch(continueLabel)
	b.namedLabel(continueLabel)
	b.branch(loopLabel)
	b.namedLabel(doneLabel)
}

func emitOwnedPointerFree(b *llvmBuilder, value llvmValue, targetType ir.TypeID) {
	data := b.extractField(value, llvmFieldData)
	desc := b.extractField(value, llvmFieldAllocator)
	targetLayout := b.emitter.layout(targetType)
	rawData := b.bitcast(data, llvmPointerLayout(llvmScalarLayout("i8")))
	payloadEnd := b.value(fmt.Sprintf("getelementptr (%s, %s* null, i32 1)", targetLayout.Text, targetLayout.Text), llvmPointerLayout(targetLayout))
	size := b.cast("ptrtoint", payloadEnd, b.emitter.layout(b.emitter.mod.Types.IndexType()))
	emitAllocatorDeallocate(b, desc, rawData, size, b.value("8", llvmScalarLayout("i32")))
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
