package llvm

import (
	"fmt"
	"strconv"

	"compiler/internal/ir"
	"compiler/internal/ir/mir"
	"compiler/internal/semantics/symbols"
)

func emitDynamicArrayAlloc(b *llvmBuilder, alloc *mir.DynamicArrayAlloc) llvmValue {
	if b == nil || alloc == nil {
		return llvmValue{}
	}
	if alloc.Length < 0 {
		b.emitter.markInvalid("dynamic array allocation has negative length")
		return b.zero(b.emitter.layout(alloc.Type))
	}
	arrayType, ok := b.emitter.mod.Types.Type(alloc.Type)
	if !ok || arrayType.Kind != ir.TypeArray || arrayType.Length != "" {
		b.emitter.markInvalid("dynamic array allocation has invalid type")
		return b.zero(b.emitter.layout(alloc.Type))
	}
	allocator := allocatorHandleFromRef(b, alloc.Allocator)
	indexLayout := b.emitter.layout(b.emitter.mod.Types.IndexType())
	length := b.value(strconv.Itoa(alloc.Length), indexLayout)
	if alloc.Length == 0 {
		dataLayout := llvmPointerLayout(b.emitter.layout(arrayType.Elem))
		return emitDynamicArrayHeader(b, alloc.Type, b.value("null", dataLayout), length, length, allocator)
	}
	data := emitDynamicArrayStorageAlloc(b, arrayType.Elem, length, allocator)
	return emitDynamicArrayHeader(b, alloc.Type, data, length, length, allocator)
}

func emitDynamicArrayStorageAlloc(b *llvmBuilder, elemType ir.TypeID, capacity, allocator llvmValue) llvmValue {
	size := emitAllocatorStorageSize(b, elemType, capacity)
	raw := emitAllocatorAllocate(b, allocator, size, b.value("8", llvmScalarLayout("i32")))
	return b.bitcast(raw, llvmPointerLayout(b.emitter.layout(elemType)))
}

func emitDynamicArrayHeader(b *llvmBuilder, arrayTypeID ir.TypeID, data, length, capacity, allocator llvmValue) llvmValue {
	header := b.zero(b.emitter.layout(arrayTypeID))
	header = b.insertField(header, data, llvmFieldData)
	header = b.insertField(header, length, llvmFieldLength)
	header = b.insertField(header, capacity, llvmFieldCapacity)
	return b.insertField(header, allocator, llvmFieldAllocator)
}

func emitAlloc(b *llvmBuilder, e *mir.Alloc) llvmValue {
	pointerType, ok := b.emitter.mod.Types.Type(e.Type)
	if !ok || pointerType.Kind != ir.TypeOwnedPtr {
		b.emitter.markInvalid("alloc has invalid result type")
		return b.zero(b.emitter.layout(e.Type))
	}
	allocReg := allocatorHandleFromRef(b, e.Allocator)
	targetLayout := b.emitter.layout(pointerType.Elem)
	sizeLayout := b.emitter.layout(b.emitter.mod.Types.IndexType())
	payloadEnd := b.value(fmt.Sprintf("getelementptr (%s, %s* null, i32 1)", targetLayout.Text, targetLayout.Text), llvmPointerLayout(targetLayout))
	size := b.cast("ptrtoint", payloadEnd, sizeLayout)
	zeroSize := b.compare("icmp", "eq", size, b.value("0", sizeLayout))
	normSize := b.selectValue(zeroSize, b.value("1", sizeLayout), size)
	raw := emitAllocatorAllocate(b, allocReg, normSize, b.value("8", llvmScalarLayout("i32")))

	dataPtr := b.bitcast(raw, llvmPointerLayout(targetLayout))
	b.store(b.pointerPlace(dataPtr), emitRef(b, e.Value))
	carrier := b.insertField(b.zero(b.emitter.layout(e.Type)), dataPtr, llvmFieldData)
	return b.insertField(carrier, allocReg, llvmFieldAllocator)
}

func emitDynamicArrayReserve(b *llvmBuilder, array llvmValue, typeID ir.TypeID, minimum llvmValue) llvmValue {
	elemTypeID, ok := dynamicArrayElementType(b.emitter.mod.Types, typeID)
	if !ok {
		b.emitter.markInvalid("dynamic array reserve has invalid type")
		return b.zero(b.emitter.layout(typeID))
	}
	oldData := b.extractField(array, llvmFieldData)
	length := b.extractField(array, llvmFieldLength)
	capacity := b.extractField(array, llvmFieldCapacity)
	allocator := b.extractField(array, llvmFieldAllocator)
	sufficient := b.compare("icmp", "uge", capacity, minimum)
	id := b.nextID
	b.nextID++
	reuseLabel := fmt.Sprintf("array_reserve_reuse_%d", id)
	growLabel := fmt.Sprintf("array_reserve_grow_%d", id)
	loopLabel := fmt.Sprintf("array_relocate_loop_%d", id)
	bodyLabel := fmt.Sprintf("array_relocate_body_%d", id)
	continueLabel := fmt.Sprintf("array_relocate_continue_%d", id)
	doneLabel := fmt.Sprintf("array_relocate_done_%d", id)
	mergeLabel := fmt.Sprintf("array_reserve_done_%d", id)
	b.condBranch(sufficient, reuseLabel, growLabel)
	b.namedLabel(reuseLabel)
	b.branch(mergeLabel)
	b.namedLabel(growLabel)
	newData := emitDynamicArrayStorageAlloc(b, elemTypeID, minimum, allocator)
	relocateEntry := b.currentLabel
	b.branch(loopLabel)
	b.namedLabel(loopLabel)
	nextIndex := b.nextValue(length.Layout)
	index := b.phi(length.Layout, llvmIncoming{Value: b.value("0", length.Layout), Label: relocateEntry}, llvmIncoming{Value: nextIndex, Label: continueLabel})
	more := b.compare("icmp", "ult", index, length)
	b.condBranch(more, bodyLabel, doneLabel)
	b.namedLabel(bodyLabel)
	item := b.load(b.gep(b.pointerPlace(oldData), index, false))
	b.store(b.gep(b.pointerPlace(newData), index, false), item)
	b.branch(continueLabel)
	b.namedLabel(continueLabel)
	b.defineArithmetic(nextIndex, "add", index, b.value("1", index.Layout))
	b.branch(loopLabel)
	b.namedLabel(doneLabel)
	oldIsNull := b.compare("icmp", "eq", oldData, b.value("null", oldData.Layout))
	releaseLabel := fmt.Sprintf("array_reserve_release_%d", id)
	releaseDoneLabel := fmt.Sprintf("array_reserve_release_done_%d", id)
	b.condBranch(oldIsNull, releaseDoneLabel, releaseLabel)
	b.namedLabel(releaseLabel)
	oldSize := emitAllocatorStorageSize(b, elemTypeID, capacity)
	oldRaw := b.bitcast(oldData, llvmPointerLayout(llvmScalarLayout("i8")))
	emitAllocatorDeallocate(b, allocator, oldRaw, oldSize, b.value("8", llvmScalarLayout("i32")))
	b.branch(releaseDoneLabel)
	b.namedLabel(releaseDoneLabel)
	resized := emitDynamicArrayHeader(b, typeID, newData, length, minimum, allocator)
	b.branch(mergeLabel)
	b.namedLabel(mergeLabel)
	return b.phi(array.Layout, llvmIncoming{Value: array, Label: reuseLabel}, llvmIncoming{Value: resized, Label: releaseDoneLabel})
}

func emitDynamicArrayOp(b *llvmBuilder, op *mir.DynamicArrayOp) {
	if b == nil || op == nil || op.Array == nil {
		return
	}
	elemTypeID, ok := dynamicArrayElementType(b.emitter.mod.Types, op.ArrayType)
	if !ok {
		b.emitter.markInvalid("dynamic array operation has invalid type")
		return
	}
	arrayPlace := b.pointerPlace(emitRef(b, op.Array))
	array := b.load(arrayPlace)
	var updated llvmValue
	switch op.Op {
	case symbols.CompilerOpReserve:
		if op.Length == nil {
			b.emitter.markInvalid("reserve requires a minimum capacity")
			return
		}
		minimum := emitCast(b, &mir.Cast{Arg: op.Length, Type: b.emitter.mod.Types.IndexType()})
		updated = emitDynamicArrayReserve(b, array, op.ArrayType, minimum)
	case symbols.CompilerOpAppend:
		updated = emitDynamicArrayAppend(b, op, array)
	case symbols.CompilerOpResize:
		updated = emitDynamicArrayResize(b, op, array)
	case symbols.CompilerOpShrink:
		updated = emitDynamicArrayShrink(b, op, array, elemTypeID)
	default:
		b.emitter.markInvalid("unknown dynamic array operation " + string(op.Op))
		return
	}
	b.store(arrayPlace, updated)
}

func emitDynamicArrayShrink(b *llvmBuilder, op *mir.DynamicArrayOp, array llvmValue, elemTypeID ir.TypeID) llvmValue {
	if op.Length == nil {
		b.emitter.markInvalid("shrink requires a length")
		return array
	}
	data := b.extractField(array, llvmFieldData)
	oldLength := b.extractField(array, llvmFieldLength)
	capacity := b.extractField(array, llvmFieldCapacity)
	allocator := b.extractField(array, llvmFieldAllocator)
	newLength := emitCast(b, &mir.Cast{Arg: op.Length, Type: b.emitter.mod.Types.IndexType()})
	shorter := b.compare("icmp", "ult", newLength, oldLength)
	id := b.nextID
	b.nextID++
	keepLabel := fmt.Sprintf("array_shrink_keep_%d", id)
	shrinkLabel := fmt.Sprintf("array_shrink_drop_%d", id)
	doneLabel := fmt.Sprintf("array_shrink_done_%d", id)
	b.condBranch(shorter, shrinkLabel, keepLabel)
	b.namedLabel(keepLabel)
	b.branch(doneLabel)
	b.namedLabel(shrinkLabel)
	emitDynamicArrayElementRangeDrop(b, data, elemTypeID, newLength, oldLength)
	shrunk := emitDynamicArrayHeader(b, op.ArrayType, data, newLength, capacity, allocator)
	shrinkDoneLabel := b.currentLabel
	b.branch(doneLabel)
	b.namedLabel(doneLabel)
	return b.phi(array.Layout, llvmIncoming{Value: array, Label: keepLabel}, llvmIncoming{Value: shrunk, Label: shrinkDoneLabel})
}

func emitDynamicArrayAppend(b *llvmBuilder, op *mir.DynamicArrayOp, array llvmValue) llvmValue {
	if op.Value == nil {
		b.emitter.markInvalid("append requires a value")
		return array
	}
	length := b.extractField(array, llvmFieldLength)
	capacity := b.extractField(array, llvmFieldCapacity)
	newLength := b.arithmetic("add", length, b.value("1", length.Layout))
	overflow := b.compare("icmp", "ult", newLength, length)
	id := b.nextID
	b.nextID++
	failLabel := fmt.Sprintf("array_append_fail_%d", id)
	capacityLabel := fmt.Sprintf("array_append_capacity_%d", id)
	keepLabel := fmt.Sprintf("array_append_keep_%d", id)
	growLabel := fmt.Sprintf("array_append_grow_%d", id)
	growReadyLabel := fmt.Sprintf("array_append_grow_ready_%d", id)
	readyLabel := fmt.Sprintf("array_append_ready_%d", id)
	b.condBranch(overflow, failLabel, capacityLabel)
	b.namedLabel(failLabel)
	b.trap()
	b.namedLabel(capacityLabel)
	hasSpace := b.compare("icmp", "ult", length, capacity)
	b.condBranch(hasSpace, keepLabel, growLabel)
	b.namedLabel(keepLabel)
	b.branch(readyLabel)
	b.namedLabel(growLabel)
	overflowLayout := llvmAggregateLayout([]*llvmLayout{capacity.Layout, llvmScalarLayout("i1")}, nil)
	overflowFn := b.value("@llvm.umul.with.overflow."+capacity.Layout.Text, llvmFunctionLayout(overflowLayout, []*llvmLayout{capacity.Layout, capacity.Layout}))
	doubledAndOverflow := b.call(overflowFn, []llvmValue{capacity, b.value("2", capacity.Layout)})
	doubled := b.extractIndex(doubledAndOverflow, 0)
	doubleOverflow := b.extractIndex(doubledAndOverflow, 1)
	b.condBranch(doubleOverflow, failLabel, growReadyLabel)
	b.namedLabel(growReadyLabel)
	tooSmall := b.compare("icmp", "ult", doubled, newLength)
	grownCapacity := b.selectValue(tooSmall, newLength, doubled)
	b.branch(readyLabel)
	b.namedLabel(readyLabel)
	desiredCapacity := b.phi(capacity.Layout, llvmIncoming{Value: capacity, Label: keepLabel}, llvmIncoming{Value: grownCapacity, Label: growReadyLabel})
	reserved := emitDynamicArrayReserve(b, array, op.ArrayType, desiredCapacity)
	data := b.extractField(reserved, llvmFieldData)
	finalCapacity := b.extractField(reserved, llvmFieldCapacity)
	b.store(b.gep(b.pointerPlace(data), length, false), emitRef(b, op.Value))
	allocator := b.extractField(reserved, llvmFieldAllocator)
	return emitDynamicArrayHeader(b, op.ArrayType, data, newLength, finalCapacity, allocator)
}

func emitDynamicArrayResize(b *llvmBuilder, op *mir.DynamicArrayOp, array llvmValue) llvmValue {
	if op.Length == nil || op.Value == nil {
		b.emitter.markInvalid("resize requires a length and fill value")
		return array
	}
	oldLength := b.extractField(array, llvmFieldLength)
	newLength := emitCast(b, &mir.Cast{Arg: op.Length, Type: b.emitter.mod.Types.IndexType()})
	resized := emitDynamicArrayReserve(b, array, op.ArrayType, newLength)
	data := b.extractField(resized, llvmFieldData)
	capacity := b.extractField(resized, llvmFieldCapacity)
	allocator := b.extractField(resized, llvmFieldAllocator)
	id := b.nextID
	b.nextID++
	entryLabel := b.currentLabel
	loopLabel := fmt.Sprintf("array_resize_loop_%d", id)
	bodyLabel := fmt.Sprintf("array_resize_body_%d", id)
	continueLabel := fmt.Sprintf("array_resize_continue_%d", id)
	doneLabel := fmt.Sprintf("array_resize_done_%d", id)
	b.branch(loopLabel)
	b.namedLabel(loopLabel)
	nextIndex := b.nextValue(oldLength.Layout)
	index := b.phi(oldLength.Layout, llvmIncoming{Value: oldLength, Label: entryLabel}, llvmIncoming{Value: nextIndex, Label: continueLabel})
	more := b.compare("icmp", "ult", index, newLength)
	b.condBranch(more, bodyLabel, doneLabel)
	b.namedLabel(bodyLabel)
	b.store(b.gep(b.pointerPlace(data), index, false), emitRef(b, op.Value))
	b.branch(continueLabel)
	b.namedLabel(continueLabel)
	b.defineArithmetic(nextIndex, "add", index, b.value("1", index.Layout))
	b.branch(loopLabel)
	b.namedLabel(doneLabel)
	return emitDynamicArrayHeader(b, op.ArrayType, data, newLength, capacity, allocator)
}
