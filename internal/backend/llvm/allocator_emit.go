package llvm

import (
	"fmt"

	"compiler/internal/ir"
	"compiler/internal/ir/mir"
)

func emitDefaultAllocatorHandle(b *llvmBuilder) llvmValue {
	rawPointer := llvmPointerLayout(llvmScalarLayout("i8"))
	descriptor := &llvmLayout{Text: "[3 x i8*]", Kind: llvmLayoutArray, Element: rawPointer}
	return b.bitcast(b.value("@peeper_default_alloc", llvmPointerLayout(descriptor)), rawPointer)
}

func emitAllocatorAllocate(b *llvmBuilder, handle, size, alignment llvmValue) llvmValue {
	rawPointer := llvmPointerLayout(llvmScalarLayout("i8"))
	desc := b.bitcast(handle, llvmPointerLayout(rawPointer))
	ctx := b.load(b.pointerPlace(desc))
	allocSlot := b.gep(b.pointerPlace(desc), b.value("1", llvmScalarLayout("i32")), false)
	allocRaw := b.load(allocSlot)
	allocLayout := llvmFunctionLayout(rawPointer, []*llvmLayout{rawPointer, size.Layout, alignment.Layout})
	allocFn := b.bitcast(allocRaw, allocLayout)
	allocated := b.call(allocFn, []llvmValue{ctx, size, alignment})
	missing := b.compare("icmp", "eq", allocated, b.value("null", allocated.Layout))
	id := b.nextID
	b.nextID++
	failLabel := fmt.Sprintf("allocator_allocate_fail_%d", id)
	readyLabel := fmt.Sprintf("allocator_allocate_ready_%d", id)
	b.condBranch(missing, failLabel, readyLabel)
	b.namedLabel(failLabel)
	b.trap()
	b.namedLabel(readyLabel)
	return allocated
}

func emitAllocatorDeallocate(b *llvmBuilder, handle, raw, size, alignment llvmValue) {
	rawPointer := llvmPointerLayout(llvmScalarLayout("i8"))
	desc := b.bitcast(handle, llvmPointerLayout(rawPointer))
	ctx := b.load(b.pointerPlace(desc))
	deallocSlot := b.gep(b.pointerPlace(desc), b.value("2", llvmScalarLayout("i32")), false)
	deallocRaw := b.load(deallocSlot)
	deallocLayout := llvmFunctionLayout(&llvmLayout{Text: "void", Kind: llvmLayoutVoid}, []*llvmLayout{rawPointer, rawPointer, size.Layout, alignment.Layout})
	deallocFn := b.bitcast(deallocRaw, deallocLayout)
	b.call(deallocFn, []llvmValue{ctx, raw, size, alignment})
}

func emitAllocatorTypeLayout(b *llvmBuilder, typeID ir.TypeID) (llvmValue, llvmValue) {
	size, alignment := emitTypeSizeAndAlignment(b, typeID)
	return normalizeAllocatorSize(b, size), alignment
}

func emitAllocatorArrayLayout(b *llvmBuilder, elemType ir.TypeID, capacity llvmValue) (llvmValue, llvmValue) {
	elemSize, alignment := emitTypeSizeAndAlignment(b, elemType)
	sizeLayout := elemSize.Layout
	id := b.nextID
	b.nextID++
	failLabel := fmt.Sprintf("allocator_size_fail_%d", id)
	sizeReadyLabel := fmt.Sprintf("allocator_size_ready_%d", id)
	overflowLayout := llvmAggregateLayout([]*llvmLayout{sizeLayout, llvmScalarLayout("i1")}, nil)
	overflowFn := b.value("@llvm.umul.with.overflow."+sizeLayout.Text, llvmFunctionLayout(overflowLayout, []*llvmLayout{sizeLayout, sizeLayout}))
	sizeAndOverflow := b.call(overflowFn, []llvmValue{elemSize, capacity})
	size := b.extractIndex(sizeAndOverflow, 0)
	overflow := b.extractIndex(sizeAndOverflow, 1)
	b.condBranch(overflow, failLabel, sizeReadyLabel)
	b.namedLabel(failLabel)
	b.trap()
	b.namedLabel(sizeReadyLabel)
	return normalizeAllocatorSize(b, size), alignment
}

func emitTypeSizeAndAlignment(b *llvmBuilder, typeID ir.TypeID) (llvmValue, llvmValue) {
	layout := b.emitter.layout(typeID)
	sizeLayout := b.emitter.layout(b.emitter.mod.Types.IndexType())
	end := b.value(fmt.Sprintf("getelementptr (%s, %s* null, i32 1)", layout.Text, layout.Text), llvmPointerLayout(layout))
	size := b.cast("ptrtoint", end, sizeLayout)

	probe := llvmAggregateLayout([]*llvmLayout{llvmScalarLayout("i8"), layout}, nil)
	aligned := b.value(fmt.Sprintf("getelementptr (%s, %s* null, i32 0, i32 1)", probe.Text, probe.Text), llvmPointerLayout(layout))
	alignment := b.cast("ptrtoint", aligned, llvmScalarLayout("i32"))
	return size, alignment
}

func normalizeAllocatorSize(b *llvmBuilder, size llvmValue) llvmValue {
	zero := b.compare("icmp", "eq", size, b.value("0", size.Layout))
	return b.selectValue(zero, b.value("1", size.Layout), size)
}

func allocatorHandleFromRef(b *llvmBuilder, ref mir.ValueRef) llvmValue {
	if ref == nil {
		return emitDefaultAllocatorHandle(b)
	}
	handle := emitRef(b, ref)
	if handle.Layout.Text == "i8*" {
		return handle
	}
	return b.bitcast(handle, llvmPointerLayout(llvmScalarLayout("i8")))
}
