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
	if name := b.emitter.dropHelpers[typeID]; name != "" {
		void := &llvmLayout{Text: "void", Kind: llvmLayoutVoid}
		function := llvmFunctionLayout(void, []*llvmLayout{value.Layout})
		b.call(b.value(name, function), []llvmValue{value})
		return
	}
	emitDropValueInline(b, value, typeID)
}

func emitDropValueInline(b *llvmBuilder, value llvmValue, typeID ir.TypeID) {
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
	if typ.Kind == ir.TypeVariant {
		emitVariantDrop(b, value, typ)
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

func (e *llvmEmitter) emitNamedDropHelpers(out *strings.Builder) {
	if e == nil || e.mod == nil || e.mod.Types == nil || out == nil {
		return
	}
	e.dropHelpers = make(map[ir.TypeID]string)
	ids := e.mod.Types.NamedTypeIDs()
	for _, id := range ids {
		if typeNeedsDrop(e.mod.Types, id) {
			e.dropHelpers[id] = "@peeper_drop_" + strings.TrimPrefix(e.layout(id).Text, "%peeper.type.")
		}
	}
	if len(e.dropHelpers) == 0 {
		return
	}
	void := &llvmLayout{Text: "void", Kind: llvmLayoutVoid}
	for _, id := range ids {
		name := e.dropHelpers[id]
		if name == "" {
			continue
		}
		layout := e.layout(id)
		fmt.Fprintf(out, "define private void %s(%s %%value) {\n", name, layout.Text)
		builder := newLLVMBuilder(out, e, -1)
		builder.namedLabel("entry")
		emitDropValueInline(builder, builder.value("%value", layout), id)
		builder.retVoid(void)
		out.WriteString("}\n")
	}
	out.WriteString("\n")
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

func emitVariantDrop(b *llvmBuilder, value llvmValue, variant ir.Type) {
	dropCases := make([]int, 0, len(variant.Cases))
	for caseIndex, variantCase := range variant.Cases {
		if variantCase.Payload != ir.InvalidType && typeNeedsDrop(b.emitter.mod.Types, variantCase.Payload) {
			dropCases = append(dropCases, caseIndex)
		}
	}
	if len(dropCases) == 0 {
		return
	}
	id := b.nextID
	b.nextID++
	tag := b.variantTag(value)
	doneLabel := fmt.Sprintf("drop_variant_done_%d", id)
	switchCases := make([]llvmSwitchCase, len(dropCases))
	for i, caseIndex := range dropCases {
		switchCases[i] = llvmSwitchCase{
			Value: b.variantCaseTag(caseIndex, tag.Layout),
			Label: fmt.Sprintf("drop_variant_%d_case_%d", id, caseIndex),
		}
	}
	b.switchBranch(tag, doneLabel, switchCases)
	for i, caseIndex := range dropCases {
		b.namedLabel(switchCases[i].Label)
		variantCase := variant.Cases[caseIndex]
		emitDropValue(b, b.variantPayload(value, caseIndex), variantCase.Payload)
		b.branch(doneLabel)
	}
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

type runtimeTypeProperty uint8

const (
	typePropertyNeedsDrop runtimeTypeProperty = iota
	typePropertyCarriesAllocator
	typePropertyNeedsRawFree
)

// typeNeedsDrop is not a second implementation of the source-level drop
// obligation, and it must not become one.
//
// typeinfo.OwnershipCapabilityOf decides *whether a value is dropped at all*.
// That decision reaches here already made, as a mir.Drop instruction; emitDrop
// is only ever entered from one, and every other call below is this function
// recursing into a drop that was already ordered. What it answers is narrower:
// given that this value is being dropped, does its runtime representation hold
// anything worth walking into. It reads the lowered ir.TypeTable, so it can see
// representation choices that no source type mentions, and it cannot see source
// policy such as an explicit-copy class.
//
// The other caller decides an ABI shim rather than a drop: a declared-only
// function whose signature carries owned storage needs one. Also physical.
//
// So a type may be reachable here and not carry a source-level drop obligation
// without either side being wrong. If you ever need this to answer "should this
// be dropped", you are in the wrong phase: ownership owns that, and the answer
// belongs in the mir.Drop it emits.
func typeNeedsDrop(types *ir.TypeTable, id ir.TypeID) bool {
	return typeHasRuntimeProperty(types, id, typePropertyNeedsDrop, make(map[ir.TypeID]bool))
}

func typeCarriesAllocatorID(types *ir.TypeTable, id ir.TypeID) bool {
	return typeHasRuntimeProperty(types, id, typePropertyCarriesAllocator, make(map[ir.TypeID]bool))
}

func typeNeedsRawFreeID(types *ir.TypeTable, id ir.TypeID) bool {
	return typeHasRuntimeProperty(types, id, typePropertyNeedsRawFree, make(map[ir.TypeID]bool))
}

func typeHasRuntimeProperty(
	types *ir.TypeTable,
	id ir.TypeID,
	property runtimeTypeProperty,
	visiting map[ir.TypeID]bool,
) bool {
	if types == nil || id == ir.InvalidType || visiting[id] {
		return false
	}
	typ, ok := types.Type(id)
	if !ok {
		return false
	}
	visiting[id] = true
	defer delete(visiting, id)

	switch property {
	case typePropertyNeedsDrop:
		if typ.Kind == ir.TypeOwnedPtr || typ.Kind == ir.TypeString {
			return true
		}
	case typePropertyCarriesAllocator:
		if typ.Kind == ir.TypeString || typ.Kind == ir.TypeOwnedPtr && !isInterfaceType(types, typ.Elem) {
			return true
		}
	case typePropertyNeedsRawFree:
		if typ.Kind == ir.TypeOwnedPtr && isInterfaceType(types, typ.Elem) {
			return false
		}
	}

	switch typ.Kind {
	case ir.TypeOwnedPtr:
		if property == typePropertyNeedsRawFree {
			return typeHasRuntimeProperty(types, typ.Elem, property, visiting)
		}
		return false
	case ir.TypeReference:
		return false
	case ir.TypeVariant:
		for _, variantCase := range typ.Cases {
			if variantCase.Payload != ir.InvalidType && typeHasRuntimeProperty(types, variantCase.Payload, property, visiting) {
				return true
			}
		}
	case ir.TypeArray:
		if typ.Length == "" {
			return true
		}
		return typeHasRuntimeProperty(types, typ.Elem, property, visiting)
	case ir.TypeStruct:
		for _, field := range typ.Fields {
			if typeHasRuntimeProperty(types, field.Type, property, visiting) {
				return true
			}
		}
	}
	return false
}
