package llvm

import (
	"fmt"

	"compiler/internal/ir"
	"compiler/internal/ir/mir"
)

func emitDefaultAllocatorHandle(b *llvmBuilder) string {
	handle := b.nextReg()
	b.line(fmt.Sprintf("%s = bitcast [3 x i8*]* @peeper_default_alloc to i8*", handle))
	return handle
}

func emitAllocatorAllocate(b *llvmBuilder, handle, size, alignment string) string {
	sizeType := b.emitter.llvmType(b.emitter.mod.Types.IndexType)
	desc := b.nextReg()
	b.line(fmt.Sprintf("%s = bitcast i8* %s to i8**", desc, handle))
	ctx := b.nextReg()
	b.line(fmt.Sprintf("%s = load i8*, i8** %s", ctx, desc))
	allocSlot := b.nextReg()
	b.line(fmt.Sprintf("%s = getelementptr i8*, i8** %s, i32 1", allocSlot, desc))
	allocRaw := b.nextReg()
	b.line(fmt.Sprintf("%s = load i8*, i8** %s", allocRaw, allocSlot))
	allocFn := b.nextReg()
	b.line(fmt.Sprintf("%s = bitcast i8* %s to i8* (i8*, %s, i32)*", allocFn, allocRaw, sizeType))
	raw := b.nextReg()
	b.line(fmt.Sprintf("%s = call i8* %s(i8* %s, %s %s, i32 %s)", raw, allocFn, ctx, sizeType, size, alignment))
	return raw
}

func emitAllocatorDeallocate(b *llvmBuilder, handle, raw, size, alignment string) {
	sizeType := b.emitter.llvmType(b.emitter.mod.Types.IndexType)
	desc := b.nextReg()
	b.line(fmt.Sprintf("%s = bitcast i8* %s to i8**", desc, handle))
	ctx := b.nextReg()
	b.line(fmt.Sprintf("%s = load i8*, i8** %s", ctx, desc))
	deallocSlot := b.nextReg()
	b.line(fmt.Sprintf("%s = getelementptr i8*, i8** %s, i32 2", deallocSlot, desc))
	deallocRaw := b.nextReg()
	b.line(fmt.Sprintf("%s = load i8*, i8** %s", deallocRaw, deallocSlot))
	deallocFn := b.nextReg()
	b.line(fmt.Sprintf("%s = bitcast i8* %s to void (i8*, i8*, %s, i32)*", deallocFn, deallocRaw, sizeType))
	b.line(fmt.Sprintf("call void %s(i8* %s, i8* %s, %s %s, i32 %s)", deallocFn, ctx, raw, sizeType, size, alignment))
}

func emitAllocatorStorageSize(b *llvmBuilder, elemType ir.TypeID, capacity string) string {
	sizeType := b.emitter.llvmType(b.emitter.mod.Types.IndexType)
	id := b.nextID
	b.nextID++
	failLabel := fmt.Sprintf("allocator_size_fail_%d", id)
	capacityReadyLabel := fmt.Sprintf("allocator_capacity_ready_%d", id)
	sizeReadyLabel := fmt.Sprintf("allocator_size_ready_%d", id)
	if sizeType == "i32" {
		tooLarge := b.nextReg()
		b.line(fmt.Sprintf("%s = icmp ugt i64 %s, 4294967295", tooLarge, capacity))
		b.line(fmt.Sprintf("br i1 %s, label %%%s, label %%%s", tooLarge, failLabel, capacityReadyLabel))
		b.namedLabel(capacityReadyLabel)
		narrowed := b.nextReg()
		b.line(fmt.Sprintf("%s = trunc i64 %s to i32", narrowed, capacity))
		capacity = narrowed
	}
	elemLLVMType := b.emitter.llvmType(elemType)
	elemSize := b.nextReg()
	b.line(fmt.Sprintf("%s = ptrtoint %s* getelementptr (%s, %s* null, i32 1) to %s", elemSize, elemLLVMType, elemLLVMType, elemLLVMType, sizeType))
	sizeAndOverflow := b.nextReg()
	b.line(fmt.Sprintf("%s = call { %s, i1 } @llvm.umul.with.overflow.%s(%s %s, %s %s)", sizeAndOverflow, sizeType, sizeType, sizeType, elemSize, sizeType, capacity))
	size := b.nextReg()
	b.line(fmt.Sprintf("%s = extractvalue { %s, i1 } %s, 0", size, sizeType, sizeAndOverflow))
	overflow := b.nextReg()
	b.line(fmt.Sprintf("%s = extractvalue { %s, i1 } %s, 1", overflow, sizeType, sizeAndOverflow))
	b.line(fmt.Sprintf("br i1 %s, label %%%s, label %%%s", overflow, failLabel, sizeReadyLabel))
	b.namedLabel(failLabel)
	b.line("call void @llvm.trap()")
	b.line("unreachable")
	b.namedLabel(sizeReadyLabel)
	zero := b.nextReg()
	b.line(fmt.Sprintf("%s = icmp eq %s %s, 0", zero, sizeType, size))
	normalized := b.nextReg()
	b.line(fmt.Sprintf("%s = select i1 %s, %s 1, %s %s", normalized, zero, sizeType, sizeType, size))
	return normalized
}

func allocatorHandleFromRef(b *llvmBuilder, ref mir.ValueRef) string {
	if ref == nil {
		return emitDefaultAllocatorHandle(b)
	}
	handle := emitRef(b, ref)
	if b.emitter.llvmType(mirRefType(ref)) == "i8*" {
		return handle
	}
	cast := b.nextReg()
	b.line(fmt.Sprintf("%s = bitcast %s %s to i8*", cast, b.emitter.llvmType(mirRefType(ref)), handle))
	return cast
}
