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
	return b.call(allocFn, []llvmValue{ctx, size, alignment})
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

func emitAllocatorStorageSize(b *llvmBuilder, elemType ir.TypeID, capacity llvmValue) llvmValue {
	sizeLayout := b.emitter.layout(b.emitter.mod.Types.IndexType())
	id := b.nextID
	b.nextID++
	failLabel := fmt.Sprintf("allocator_size_fail_%d", id)
	sizeReadyLabel := fmt.Sprintf("allocator_size_ready_%d", id)
	elemLayout := b.emitter.layout(elemType)
	elemPtr := b.value(fmt.Sprintf("getelementptr (%s, %s* null, i32 1)", elemLayout.Text, elemLayout.Text), llvmPointerLayout(elemLayout))
	elemSize := b.cast("ptrtoint", elemPtr, sizeLayout)
	overflowLayout := llvmAggregateLayout([]*llvmLayout{sizeLayout, llvmScalarLayout("i1")}, nil)
	overflowFn := b.value("@llvm.umul.with.overflow."+sizeLayout.Text, llvmFunctionLayout(overflowLayout, []*llvmLayout{sizeLayout, sizeLayout}))
	sizeAndOverflow := b.call(overflowFn, []llvmValue{elemSize, capacity})
	size := b.extractIndex(sizeAndOverflow, 0)
	overflow := b.extractIndex(sizeAndOverflow, 1)
	b.condBranch(overflow, failLabel, sizeReadyLabel)
	b.namedLabel(failLabel)
	b.trap()
	b.namedLabel(sizeReadyLabel)
	zero := b.compare("icmp", "eq", size, b.value("0", sizeLayout))
	return b.selectValue(zero, b.value("1", sizeLayout), size)
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
