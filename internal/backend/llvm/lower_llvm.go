package llvm

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"compiler/internal/diagnostics"
	"compiler/internal/ir"
	"compiler/internal/ir/mir"
	"compiler/internal/problems"
	"compiler/internal/semantics/symbols"
	"compiler/internal/source"
	"compiler/internal/target"
)

type llvmEmitter struct {
	mod             *mir.Module
	diag            *diagnostics.DiagnosticBag
	target          target.Info
	badTypes        map[string]struct{}
	invalid         bool
	externalGlobals map[string]ir.TypeID
	debug           *llvmDebugEmitter
}

func emitStore(b *llvmBuilder, store *mir.Store) {
	if b == nil || store == nil || store.Place == nil || store.Value == nil {
		return
	}
	ptr := emitPlacePtr(b, store.Place)
	if ptr == "" {
		return
	}
	value := emitRef(b, store.Value)
	valueType := b.emitter.llvmType(mirRefType(store.Value))
	b.line(fmt.Sprintf("store %s %s, %s* %s", valueType, value, valueType, ptr))
}

func emitPrint(b *llvmBuilder, printInstr *mir.Print) {
	if b == nil || printInstr == nil || printInstr.Value == nil {
		return
	}
	typeID := mirRefType(printInstr.Value)
	typ, typeOK := b.emitter.mod.Types.Type(typeID)
	if !typeOK {
		b.emitter.markInvalid("print reached LLVM with invalid type")
		return
	}
	value := emitRef(b, printInstr.Value)
	formatName := ""
	formatSize := 0
	argument := ""
	switch {
	case typ.Kind == ir.TypeBool:
		selected := b.nextReg()
		b.line(fmt.Sprintf("%s = select i1 %s, i8* getelementptr inbounds ([5 x i8], [5 x i8]* @.print.true, i32 0, i32 0), i8* getelementptr inbounds ([6 x i8], [6 x i8]* @.print.false, i32 0, i32 0)", selected, value))
		formatName, formatSize, argument = "string", 3, "i8* "+selected
	case typ.Kind == ir.TypeCStr:
		formatName, formatSize, argument = "string", 3, "i8* "+value
	case typ.Kind == ir.TypeString:
		llvmType := b.emitter.llvmType(typeID)
		data := b.nextReg()
		b.line(fmt.Sprintf("%s = extractvalue %s %s, 0", data, llvmType, value))
		length := b.nextReg()
		b.line(fmt.Sprintf("%s = extractvalue %s %s, 1", length, llvmType, value))
		lengthType := b.emitter.llvmType(b.emitter.mod.Types.IndexType())
		precision := length
		switch lengthType {
		case "i32":
		case "i64":
			precision = b.nextReg()
			b.line(fmt.Sprintf("%s = trunc i64 %s to i32", precision, length))
		default:
			b.emitter.markInvalid("print reached LLVM with unsupported string length type " + lengthType)
			return
		}
		formatName, formatSize, argument = "str", 5, "i32 "+precision+", i8* "+data
	case typ.Kind == ir.TypeRawPtr:
		formatName, formatSize, argument = "pointer", 3, "i8* "+value
	case typ.Kind == ir.TypeFloat:
		if typ.Bits == 32 {
			f64 := b.emitter.mod.Types.Intern(ir.Type{Kind: ir.TypeFloat, Bits: 64})
			value = emitCast(b, &mir.Cast{Arg: printInstr.Value, Type: f64})
		}
		formatName, formatSize, argument = "float", 3, "double "+value
	default:
		signed, _, ok := integerInfoID(b.emitter.mod.Types, typeID)
		if !ok {
			b.emitter.markInvalid("print reached LLVM with unsupported type " + b.emitter.mod.Types.Text(typeID))
			return
		}
		promotedType := b.emitter.mod.Types.Intern(ir.Type{Kind: ir.TypeInteger, Bits: 64})
		formatName = "unsigned"
		if signed {
			promotedType = b.emitter.mod.Types.Intern(ir.Type{Kind: ir.TypeInteger, Signed: true, Bits: 64})
			formatName = "signed"
		}
		value = emitCast(b, &mir.Cast{Arg: printInstr.Value, Type: promotedType})
		formatSize, argument = 5, "i64 "+value
	}
	format := fmt.Sprintf("getelementptr inbounds ([%d x i8], [%d x i8]* @.print.%s, i32 0, i32 0)", formatSize, formatSize, formatName)
	b.line(fmt.Sprintf("call i32 (i8*, ...) @printf(i8* %s, %s)", format, argument))
	if printInstr.Newline {
		newline := "getelementptr inbounds ([2 x i8], [2 x i8]* @.print.newline, i32 0, i32 0)"
		b.line(fmt.Sprintf("call i32 (i8*, ...) @printf(i8* %s)", newline))
	}
}

func emitFieldPtr(b *llvmBuilder, base string, structType ir.TypeID, index int) string {
	if b == nil || base == "" || structType == ir.InvalidType {
		return ""
	}
	llvmStructType, ok := llvmTypeID(b.emitter.mod.Types, structType)
	if !ok {
		return ""
	}
	ptr := b.nextReg()
	b.line(fmt.Sprintf("%s = getelementptr inbounds %s, %s* %s, i32 0, i32 %d", ptr, llvmStructType, llvmStructType, base, index))
	return ptr
}

func normalizeIndexForLength(b *llvmBuilder, indexRef mir.ValueRef, length string) (compareIndex, compareLength, compareType, indexI64 string, ok bool) {
	if b == nil || indexRef == nil {
		return "", "", "", "", false
	}
	indexType := mirRefType(indexRef)
	_, indexBits, ok := integerInfoID(b.emitter.mod.Types, indexType)
	if !ok {
		b.emitter.markInvalid("indexed access lowering requires integral index")
		return "", "", "", "", false
	}
	compareIndex = emitRef(b, indexRef)
	compareLength = length
	compareType = b.emitter.llvmType(indexType)
	indexI64 = compareIndex
	if indexBits < 64 {
		u64 := b.emitter.mod.Types.Intern(ir.Type{Kind: ir.TypeInteger, Bits: 64})
		compareIndex = emitCast(b, &mir.Cast{Arg: indexRef, Type: u64})
		compareType = "i64"
		indexI64 = compareIndex
	} else if indexBits > 64 {
		compareLength = b.nextReg()
		b.line(fmt.Sprintf("%s = zext i64 %s to %s", compareLength, length, compareType))
		indexI64 = b.nextReg()
		b.line(fmt.Sprintf("%s = trunc %s %s to i64", indexI64, compareType, compareIndex))
	}
	return compareIndex, compareLength, compareType, indexI64, true
}

func emitBoundsCheckedIndex(b *llvmBuilder, indexRef mir.ValueRef, length string) (string, bool) {
	compareIndex, compareLength, compareType, index, ok := normalizeIndexForLength(b, indexRef, length)
	if !ok {
		return "", false
	}
	outOfBounds := b.nextReg()
	// Unsigned comparison also rejects negative signed indexes after sign extension.
	b.line(fmt.Sprintf("%s = icmp uge %s %s, %s", outOfBounds, compareType, compareIndex, compareLength))
	boundsID := b.nextID
	b.nextID++
	failLabel := fmt.Sprintf("bounds_fail_%d", boundsID)
	okLabel := fmt.Sprintf("bounds_ok_%d", boundsID)
	b.line(fmt.Sprintf("br i1 %s, label %%%s, label %%%s", outOfBounds, failLabel, okLabel))
	b.namedLabel(failLabel)
	b.line("call void @llvm.trap()")
	b.line("unreachable")
	b.namedLabel(okLabel)
	return index, true
}

func emitSliceView(b *llvmBuilder, view *mir.SliceView) string {
	if b == nil || view == nil || view.Source == nil {
		return "0"
	}
	sourceTypeID := view.Source.Type
	targetTypeID := sourceTypeID
	if sourceType, ok := b.emitter.mod.Types.Type(sourceTypeID); ok && sourceType.Kind == ir.TypeReference {
		targetTypeID = sourceType.Elem
	}
	targetType, ok := b.emitter.mod.Types.Type(targetTypeID)
	if !ok || targetType.Kind != ir.TypeArray {
		b.emitter.markInvalid("slice view source shape is not lowerable in current compiler stage")
		return "0"
	}
	var data, fixedArrayPtr, length string
	elemTypeID := targetType.Elem
	if targetType.Length == "" {
		sourceType := b.emitter.llvmType(sourceTypeID)
		source := ""
		if sliceViewUsesPlacePtr(b.emitter.mod.Types, view.Source) {
			ptr := emitPlacePtr(b, view.Source)
			if ptr == "" {
				return "0"
			}
			source = b.nextReg()
			b.line(fmt.Sprintf("%s = load %s, %s* %s", source, sourceType, sourceType, ptr))
		} else {
			source = emitRef(b, view.Source.Root)
		}
		data = b.nextReg()
		b.line(fmt.Sprintf("%s = extractvalue %s %s, 0", data, sourceType, source))
		length = b.nextReg()
		b.line(fmt.Sprintf("%s = extractvalue %s %s, 1", length, sourceType, source))
	} else {
		length = targetType.Length
		if sliceViewUsesPlacePtr(b.emitter.mod.Types, view.Source) {
			fixedArrayPtr = emitPlacePtr(b, view.Source)
		} else {
			fixedArrayPtr = emitRef(b, view.Source.Root)
		}
		if fixedArrayPtr == "" {
			b.emitter.markInvalid("fixed-array slicing requires addressable storage")
			return "0"
		}
	}

	indexType := b.emitter.llvmType(b.emitter.mod.Types.IndexType())
	lengthI64 := length
	if indexType != "i64" && fixedArrayPtr == "" {
		lengthI64 = b.nextReg()
		b.line(fmt.Sprintf("%s = zext %s %s to i64", lengthI64, indexType, length))
	}
	startI64 := "0"
	endI64 := lengthI64
	invalid := ""
	if view.Start != nil {
		start, compareLength, compareType, normalized, ok := normalizeIndexForLength(b, view.Start, length)
		if !ok {
			return "0"
		}
		startI64 = normalized
		invalid = b.nextReg()
		b.line(fmt.Sprintf("%s = icmp ugt %s %s, %s", invalid, compareType, start, compareLength))
	}
	if view.End != nil {
		end, compareLength, compareType, normalized, ok := normalizeIndexForLength(b, view.End, length)
		if !ok {
			return "0"
		}
		endI64 = normalized
		endInvalid := b.nextReg()
		predicate := "ugt"
		if !view.EndExclusive {
			predicate = "uge"
		}
		b.line(fmt.Sprintf("%s = icmp %s %s %s, %s", endInvalid, predicate, compareType, end, compareLength))
		if invalid == "" {
			invalid = endInvalid
		} else {
			combined := b.nextReg()
			b.line(fmt.Sprintf("%s = or i1 %s, %s", combined, invalid, endInvalid))
			invalid = combined
		}
	}

	boundsID := b.nextID
	b.nextID++
	failLabel := fmt.Sprintf("slice_bounds_fail_%d", boundsID)
	normalizedLabel := fmt.Sprintf("slice_bounds_normalized_%d", boundsID)
	readyLabel := fmt.Sprintf("slice_bounds_ready_%d", boundsID)
	failEmitted := false
	if invalid != "" {
		b.line(fmt.Sprintf("br i1 %s, label %%%s, label %%%s", invalid, failLabel, normalizedLabel))
		b.namedLabel(failLabel)
		b.line("call void @llvm.trap()")
		b.line("unreachable")
		b.namedLabel(normalizedLabel)
		failEmitted = true
	}
	if view.End != nil && !view.EndExclusive {
		inclusiveEnd := b.nextReg()
		b.line(fmt.Sprintf("%s = add i64 %s, 1", inclusiveEnd, endI64))
		endI64 = inclusiveEnd
	}
	reversed := b.nextReg()
	b.line(fmt.Sprintf("%s = icmp ugt i64 %s, %s", reversed, startI64, endI64))
	b.line(fmt.Sprintf("br i1 %s, label %%%s, label %%%s", reversed, failLabel, readyLabel))
	if !failEmitted {
		b.namedLabel(failLabel)
		b.line("call void @llvm.trap()")
		b.line("unreachable")
	}
	b.namedLabel(readyLabel)

	elemType := b.emitter.llvmType(elemTypeID)
	if fixedArrayPtr != "" {
		arrayType := b.emitter.llvmType(targetTypeID)
		data = b.nextReg()
		b.line(fmt.Sprintf("%s = getelementptr %s, %s* %s, i32 0, i32 0", data, arrayType, arrayType, fixedArrayPtr))
	}
	adjustedData := b.nextReg()
	b.line(fmt.Sprintf("%s = getelementptr %s, %s* %s, i64 %s", adjustedData, elemType, elemType, data, startI64))
	viewLength := b.nextReg()
	b.line(fmt.Sprintf("%s = sub i64 %s, %s", viewLength, endI64, startI64))
	if indexType != "i64" {
		narrowed := b.nextReg()
		b.line(fmt.Sprintf("%s = trunc i64 %s to %s", narrowed, viewLength, indexType))
		viewLength = narrowed
	}
	viewType := b.emitter.llvmType(view.Type)
	withData := b.nextReg()
	b.line(fmt.Sprintf("%s = insertvalue %s zeroinitializer, %s* %s, 0", withData, viewType, elemType, adjustedData))
	withLength := b.nextReg()
	b.line(fmt.Sprintf("%s = insertvalue %s %s, %s %s, 1", withLength, viewType, withData, indexType, viewLength))
	return withLength
}

func emitLen(b *llvmBuilder, value mir.ValueRef) string {
	if b == nil || value == nil {
		return "0"
	}
	refType, ok := b.emitter.mod.Types.Type(mirRefType(value))
	if !ok || refType.Kind != ir.TypeReference {
		b.emitter.markInvalid("len requires a reference value")
		return "0"
	}
	target, ok := b.emitter.mod.Types.Type(refType.Elem)
	if !ok {
		b.emitter.markInvalid("len has invalid reference target")
		return "0"
	}
	switch target.Kind {
	case ir.TypeString:
		carrierType := b.emitter.llvmType(refType.Elem)
		loaded := b.nextReg()
		b.line(fmt.Sprintf("%s = load %s, %s* %s", loaded, carrierType, carrierType, emitRef(b, value)))
		length := b.nextReg()
		b.line(fmt.Sprintf("%s = extractvalue %s %s, 1", length, carrierType, loaded))
		return length
	case ir.TypeArray:
		if target.Length != "" {
			if _, err := strconv.ParseUint(target.Length, 10, 64); err != nil {
				b.emitter.markInvalid("fixed array has invalid length")
				return "0"
			}
			return target.Length
		}
		arrayLLVMType := b.emitter.llvmType(mirRefType(value))
		length := b.nextReg()
		b.line(fmt.Sprintf("%s = extractvalue %s %s, 1", length, arrayLLVMType, emitRef(b, value)))
		return length
	default:
		b.emitter.markInvalid("len requires a string or array reference")
		return "0"
	}
}

func sliceViewUsesPlacePtr(types *ir.TypeTable, source *mir.Place) bool {
	if source == nil {
		return false
	}
	if len(source.Projections) > 0 {
		return true
	}
	if typ, ok := types.Type(source.Type); ok && typ.Kind == ir.TypeReference {
		return false
	}
	typ, ok := types.Type(source.Type)
	return ok && typ.Kind == ir.TypeArray && typ.Length != ""
}

func emitDynamicArrayAlloc(b *llvmBuilder, alloc *mir.DynamicArrayAlloc) string {
	if b == nil || alloc == nil {
		return "zeroinitializer"
	}
	if alloc.Length < 0 {
		b.emitter.markInvalid("dynamic array allocation has negative length")
		return "zeroinitializer"
	}
	arrayType, ok := b.emitter.mod.Types.Type(alloc.Type)
	if !ok || arrayType.Kind != ir.TypeArray || arrayType.Length != "" {
		b.emitter.markInvalid("dynamic array allocation has invalid type")
		return "zeroinitializer"
	}
	allocator := allocatorHandleFromRef(b, alloc.Allocator)
	if alloc.Length == 0 {
		return emitDynamicArrayHeader(b, alloc.Type, arrayType.Elem, "null", "0", "0", allocator)
	}
	data := emitDynamicArrayStorageAlloc(b, arrayType.Elem, strconv.Itoa(alloc.Length), allocator)
	return emitDynamicArrayHeader(b, alloc.Type, arrayType.Elem, data, strconv.Itoa(alloc.Length), strconv.Itoa(alloc.Length), allocator)
}

func emitDynamicArrayStorageAlloc(b *llvmBuilder, elemType ir.TypeID, capacity, allocator string) string {
	size := emitAllocatorStorageSize(b, elemType, capacity)
	raw := emitAllocatorAllocate(b, allocator, size, "8")
	missing := b.nextReg()
	b.line(fmt.Sprintf("%s = icmp eq i8* %s, null", missing, raw))
	id := b.nextID
	b.nextID++
	failLabel := fmt.Sprintf("array_alloc_fail_%d", id)
	readyLabel := fmt.Sprintf("array_alloc_ready_%d", id)
	b.line(fmt.Sprintf("br i1 %s, label %%%s, label %%%s", missing, failLabel, readyLabel))
	b.namedLabel(failLabel)
	b.line("call void @llvm.trap()")
	b.line("unreachable")
	b.namedLabel(readyLabel)
	data := b.nextReg()
	elemLLVMType := b.emitter.llvmType(elemType)
	b.line(fmt.Sprintf("%s = bitcast i8* %s to %s*", data, raw, elemLLVMType))
	return data
}

func emitDynamicArrayHeader(b *llvmBuilder, arrayTypeID, elemTypeID ir.TypeID, data, length, capacity, allocator string) string {
	arrayType := b.emitter.llvmType(arrayTypeID)
	elemType := b.emitter.llvmType(elemTypeID)
	indexType := b.emitter.llvmType(b.emitter.mod.Types.IndexType())
	withData := b.nextReg()
	b.line(fmt.Sprintf("%s = insertvalue %s zeroinitializer, %s* %s, 0", withData, arrayType, elemType, data))
	withLength := b.nextReg()
	b.line(fmt.Sprintf("%s = insertvalue %s %s, %s %s, 1", withLength, arrayType, withData, indexType, length))
	withCapacity := b.nextReg()
	b.line(fmt.Sprintf("%s = insertvalue %s %s, %s %s, 2", withCapacity, arrayType, withLength, indexType, capacity))
	withAllocator := b.nextReg()
	b.line(fmt.Sprintf("%s = insertvalue %s %s, i8* %s, 3", withAllocator, arrayType, withCapacity, allocator))
	return withAllocator
}

func emitAlloc(b *llvmBuilder, e *mir.Alloc) string {
	pointerType, ok := b.emitter.mod.Types.Type(e.Type)
	if !ok || pointerType.Kind != ir.TypeOwnedPtr {
		b.emitter.markInvalid("alloc has invalid result type")
		return "zeroinitializer"
	}
	llvmStructType := b.emitter.llvmType(e.Type)
	targetLLVM := b.emitter.llvmType(pointerType.Elem)
	sizeType := b.emitter.llvmType(b.emitter.mod.Types.IndexType())

	allocReg := allocatorHandleFromRef(b, e.Allocator)

	size := b.nextReg()
	b.line(fmt.Sprintf("%s = ptrtoint %s* getelementptr (%s, %s* null, i32 1) to %s", size, targetLLVM, targetLLVM, targetLLVM, sizeType))

	zeroSize := b.nextReg()
	b.line(fmt.Sprintf("%s = icmp eq %s %s, 0", zeroSize, sizeType, size))
	normSize := b.nextReg()
	b.line(fmt.Sprintf("%s = select i1 %s, %s 1, %s %s", normSize, zeroSize, sizeType, sizeType, size))

	raw := emitAllocatorAllocate(b, allocReg, normSize, "8")

	isNull := b.nextReg()
	b.line(fmt.Sprintf("%s = icmp eq i8* %s, null", isNull, raw))
	id := b.nextID
	b.nextID++
	failLabel := fmt.Sprintf("alloc_fail_%d", id)
	doneLabel := fmt.Sprintf("alloc_done_%d", id)
	b.line(fmt.Sprintf("br i1 %s, label %%%s, label %%%s", isNull, failLabel, doneLabel))
	b.namedLabel(failLabel)
	b.line("call void @llvm.trap()")
	b.line("unreachable")
	b.namedLabel(doneLabel)

	dataPtr := b.nextReg()
	b.line(fmt.Sprintf("%s = bitcast i8* %s to %s*", dataPtr, raw, targetLLVM))

	valueLLVMType := b.emitter.llvmType(mirRefType(e.Value))
	valueReg := emitRef(b, e.Value)
	b.line(fmt.Sprintf("store %s %s, %s* %s", valueLLVMType, valueReg, targetLLVM, dataPtr))

	carrier := b.nextReg()
	b.line(fmt.Sprintf("%s = insertvalue %s zeroinitializer, %s* %s, 0", carrier, llvmStructType, targetLLVM, dataPtr))
	final := b.nextReg()
	b.line(fmt.Sprintf("%s = insertvalue %s %s, i8* %s, 1", final, llvmStructType, carrier, allocReg))
	return final
}

func emitDynamicArrayReserve(b *llvmBuilder, array string, typeID ir.TypeID, minimum string) string {
	elemTypeID, ok := dynamicArrayElementType(b.emitter.mod.Types, typeID)
	if !ok {
		b.emitter.markInvalid("dynamic array reserve has invalid type")
		return "zeroinitializer"
	}
	arrayType := b.emitter.llvmType(typeID)
	elemType := b.emitter.llvmType(elemTypeID)
	indexType := b.emitter.llvmType(b.emitter.mod.Types.IndexType())
	oldData := b.nextReg()
	b.line(fmt.Sprintf("%s = extractvalue %s %s, 0", oldData, arrayType, array))
	length := b.nextReg()
	b.line(fmt.Sprintf("%s = extractvalue %s %s, 1", length, arrayType, array))
	capacity := b.nextReg()
	b.line(fmt.Sprintf("%s = extractvalue %s %s, 2", capacity, arrayType, array))
	allocator := b.nextReg()
	b.line(fmt.Sprintf("%s = extractvalue %s %s, 3", allocator, arrayType, array))
	sufficient := b.nextReg()
	b.line(fmt.Sprintf("%s = icmp uge %s %s, %s", sufficient, indexType, capacity, minimum))
	id := b.nextID
	b.nextID++
	reuseLabel := fmt.Sprintf("array_reserve_reuse_%d", id)
	growLabel := fmt.Sprintf("array_reserve_grow_%d", id)
	loopLabel := fmt.Sprintf("array_relocate_loop_%d", id)
	bodyLabel := fmt.Sprintf("array_relocate_body_%d", id)
	continueLabel := fmt.Sprintf("array_relocate_continue_%d", id)
	doneLabel := fmt.Sprintf("array_relocate_done_%d", id)
	mergeLabel := fmt.Sprintf("array_reserve_done_%d", id)
	b.line(fmt.Sprintf("br i1 %s, label %%%s, label %%%s", sufficient, reuseLabel, growLabel))
	b.namedLabel(reuseLabel)
	b.line(fmt.Sprintf("br label %%%s", mergeLabel))
	b.namedLabel(growLabel)
	newData := emitDynamicArrayStorageAlloc(b, elemTypeID, minimum, allocator)
	relocateEntry := b.currentLabel
	b.line(fmt.Sprintf("br label %%%s", loopLabel))
	b.namedLabel(loopLabel)
	index := b.nextReg()
	nextIndex := b.nextReg()
	b.line(fmt.Sprintf("%s = phi %s [ 0, %%%s ], [ %s, %%%s ]", index, indexType, relocateEntry, nextIndex, continueLabel))
	more := b.nextReg()
	b.line(fmt.Sprintf("%s = icmp ult %s %s, %s", more, indexType, index, length))
	b.line(fmt.Sprintf("br i1 %s, label %%%s, label %%%s", more, bodyLabel, doneLabel))
	b.namedLabel(bodyLabel)
	oldPtr := b.nextReg()
	b.line(fmt.Sprintf("%s = getelementptr %s, %s* %s, %s %s", oldPtr, elemType, elemType, oldData, indexType, index))
	item := b.nextReg()
	b.line(fmt.Sprintf("%s = load %s, %s* %s", item, elemType, elemType, oldPtr))
	newPtr := b.nextReg()
	b.line(fmt.Sprintf("%s = getelementptr %s, %s* %s, %s %s", newPtr, elemType, elemType, newData, indexType, index))
	b.line(fmt.Sprintf("store %s %s, %s* %s", elemType, item, elemType, newPtr))
	b.line(fmt.Sprintf("br label %%%s", continueLabel))
	b.namedLabel(continueLabel)
	b.line(fmt.Sprintf("%s = add %s %s, 1", nextIndex, indexType, index))
	b.line(fmt.Sprintf("br label %%%s", loopLabel))
	b.namedLabel(doneLabel)
	oldIsNull := b.nextReg()
	b.line(fmt.Sprintf("%s = icmp eq %s* %s, null", oldIsNull, elemType, oldData))
	releaseLabel := fmt.Sprintf("array_reserve_release_%d", id)
	releaseDoneLabel := fmt.Sprintf("array_reserve_release_done_%d", id)
	b.line(fmt.Sprintf("br i1 %s, label %%%s, label %%%s", oldIsNull, releaseDoneLabel, releaseLabel))
	b.namedLabel(releaseLabel)
	oldSize := emitAllocatorStorageSize(b, elemTypeID, capacity)
	oldRaw := b.nextReg()
	b.line(fmt.Sprintf("%s = bitcast %s* %s to i8*", oldRaw, elemType, oldData))
	emitAllocatorDeallocate(b, allocator, oldRaw, oldSize, "8")
	b.line(fmt.Sprintf("br label %%%s", releaseDoneLabel))
	b.namedLabel(releaseDoneLabel)
	resized := emitDynamicArrayHeader(b, typeID, elemTypeID, newData, length, minimum, allocator)
	b.line(fmt.Sprintf("br label %%%s", mergeLabel))
	b.namedLabel(mergeLabel)
	result := b.nextReg()
	b.line(fmt.Sprintf("%s = phi %s [ %s, %%%s ], [ %s, %%%s ]", result, arrayType, array, reuseLabel, resized, releaseDoneLabel))
	return result
}

func emitDynamicArrayOp(b *llvmBuilder, op *mir.DynamicArrayOp) string {
	if b == nil || op == nil || op.Array == nil {
		return "zeroinitializer"
	}
	elemTypeID, ok := dynamicArrayElementType(b.emitter.mod.Types, op.Type)
	if !ok {
		b.emitter.markInvalid("dynamic array operation has invalid type")
		return "zeroinitializer"
	}
	array := emitRef(b, op.Array)
	switch op.Op {
	case symbols.CompilerOpReserve:
		if op.Length == nil {
			b.emitter.markInvalid("reserve requires a minimum capacity")
			return array
		}
		minimum := emitCast(b, &mir.Cast{Arg: op.Length, Type: b.emitter.mod.Types.IndexType()})
		return emitDynamicArrayReserve(b, array, op.Type, minimum)
	case symbols.CompilerOpAppend:
		return emitDynamicArrayAppend(b, op, array, elemTypeID)
	case symbols.CompilerOpResize:
		return emitDynamicArrayResize(b, op, array, elemTypeID)
	case symbols.CompilerOpShrink:
		return emitDynamicArrayShrink(b, op, array, elemTypeID)
	default:
		b.emitter.markInvalid("unknown dynamic array operation " + string(op.Op))
		return array
	}
}

func emitDynamicArrayShrink(b *llvmBuilder, op *mir.DynamicArrayOp, array string, elemTypeID ir.TypeID) string {
	if op.Length == nil {
		b.emitter.markInvalid("shrink requires a length")
		return array
	}
	arrayType := b.emitter.llvmType(op.Type)
	indexType := b.emitter.llvmType(b.emitter.mod.Types.IndexType())
	data := b.nextReg()
	b.line(fmt.Sprintf("%s = extractvalue %s %s, 0", data, arrayType, array))
	oldLength := b.nextReg()
	b.line(fmt.Sprintf("%s = extractvalue %s %s, 1", oldLength, arrayType, array))
	capacity := b.nextReg()
	b.line(fmt.Sprintf("%s = extractvalue %s %s, 2", capacity, arrayType, array))
	allocator := b.nextReg()
	b.line(fmt.Sprintf("%s = extractvalue %s %s, 3", allocator, arrayType, array))
	newLength := emitCast(b, &mir.Cast{Arg: op.Length, Type: b.emitter.mod.Types.IndexType()})
	shorter := b.nextReg()
	b.line(fmt.Sprintf("%s = icmp ult %s %s, %s", shorter, indexType, newLength, oldLength))
	id := b.nextID
	b.nextID++
	keepLabel := fmt.Sprintf("array_shrink_keep_%d", id)
	shrinkLabel := fmt.Sprintf("array_shrink_drop_%d", id)
	doneLabel := fmt.Sprintf("array_shrink_done_%d", id)
	b.line(fmt.Sprintf("br i1 %s, label %%%s, label %%%s", shorter, shrinkLabel, keepLabel))
	b.namedLabel(keepLabel)
	b.line(fmt.Sprintf("br label %%%s", doneLabel))
	b.namedLabel(shrinkLabel)
	emitDynamicArrayElementRangeDrop(b, data, elemTypeID, newLength, oldLength)
	shrunk := emitDynamicArrayHeader(b, op.Type, elemTypeID, data, newLength, capacity, allocator)
	shrinkDoneLabel := b.currentLabel
	b.line(fmt.Sprintf("br label %%%s", doneLabel))
	b.namedLabel(doneLabel)
	result := b.nextReg()
	b.line(fmt.Sprintf("%s = phi %s [ %s, %%%s ], [ %s, %%%s ]", result, arrayType, array, keepLabel, shrunk, shrinkDoneLabel))
	return result
}

func emitDynamicArrayAppend(b *llvmBuilder, op *mir.DynamicArrayOp, array string, elemTypeID ir.TypeID) string {
	if op.Value == nil {
		b.emitter.markInvalid("append requires a value")
		return array
	}
	arrayType := b.emitter.llvmType(op.Type)
	indexType := b.emitter.llvmType(b.emitter.mod.Types.IndexType())
	length := b.nextReg()
	b.line(fmt.Sprintf("%s = extractvalue %s %s, 1", length, arrayType, array))
	capacity := b.nextReg()
	b.line(fmt.Sprintf("%s = extractvalue %s %s, 2", capacity, arrayType, array))
	newLength := b.nextReg()
	b.line(fmt.Sprintf("%s = add %s %s, 1", newLength, indexType, length))
	overflow := b.nextReg()
	b.line(fmt.Sprintf("%s = icmp ult %s %s, %s", overflow, indexType, newLength, length))
	id := b.nextID
	b.nextID++
	failLabel := fmt.Sprintf("array_append_fail_%d", id)
	capacityLabel := fmt.Sprintf("array_append_capacity_%d", id)
	keepLabel := fmt.Sprintf("array_append_keep_%d", id)
	growLabel := fmt.Sprintf("array_append_grow_%d", id)
	growReadyLabel := fmt.Sprintf("array_append_grow_ready_%d", id)
	readyLabel := fmt.Sprintf("array_append_ready_%d", id)
	b.line(fmt.Sprintf("br i1 %s, label %%%s, label %%%s", overflow, failLabel, capacityLabel))
	b.namedLabel(failLabel)
	b.line("call void @llvm.trap()")
	b.line("unreachable")
	b.namedLabel(capacityLabel)
	hasSpace := b.nextReg()
	b.line(fmt.Sprintf("%s = icmp ult %s %s, %s", hasSpace, indexType, length, capacity))
	b.line(fmt.Sprintf("br i1 %s, label %%%s, label %%%s", hasSpace, keepLabel, growLabel))
	b.namedLabel(keepLabel)
	b.line(fmt.Sprintf("br label %%%s", readyLabel))
	b.namedLabel(growLabel)
	doubledAndOverflow := b.nextReg()
	b.line(fmt.Sprintf("%s = call { %s, i1 } @llvm.umul.with.overflow.%s(%s %s, %s 2)", doubledAndOverflow, indexType, indexType, indexType, capacity, indexType))
	doubled := b.nextReg()
	b.line(fmt.Sprintf("%s = extractvalue { %s, i1 } %s, 0", doubled, indexType, doubledAndOverflow))
	doubleOverflow := b.nextReg()
	b.line(fmt.Sprintf("%s = extractvalue { %s, i1 } %s, 1", doubleOverflow, indexType, doubledAndOverflow))
	b.line(fmt.Sprintf("br i1 %s, label %%%s, label %%%s", doubleOverflow, failLabel, growReadyLabel))
	b.namedLabel(growReadyLabel)
	tooSmall := b.nextReg()
	b.line(fmt.Sprintf("%s = icmp ult %s %s, %s", tooSmall, indexType, doubled, newLength))
	grownCapacity := b.nextReg()
	b.line(fmt.Sprintf("%s = select i1 %s, %s %s, %s %s", grownCapacity, tooSmall, indexType, newLength, indexType, doubled))
	b.line(fmt.Sprintf("br label %%%s", readyLabel))
	b.namedLabel(readyLabel)
	desiredCapacity := b.nextReg()
	b.line(fmt.Sprintf("%s = phi %s [ %s, %%%s ], [ %s, %%%s ]", desiredCapacity, indexType, capacity, keepLabel, grownCapacity, growReadyLabel))
	reserved := emitDynamicArrayReserve(b, array, op.Type, desiredCapacity)
	data := b.nextReg()
	b.line(fmt.Sprintf("%s = extractvalue %s %s, 0", data, arrayType, reserved))
	finalCapacity := b.nextReg()
	b.line(fmt.Sprintf("%s = extractvalue %s %s, 2", finalCapacity, arrayType, reserved))
	ptr := b.nextReg()
	elemType := b.emitter.llvmType(elemTypeID)
	b.line(fmt.Sprintf("%s = getelementptr %s, %s* %s, %s %s", ptr, elemType, elemType, data, indexType, length))
	b.line(fmt.Sprintf("store %s %s, %s* %s", elemType, emitRef(b, op.Value), elemType, ptr))
	allocator := b.nextReg()
	b.line(fmt.Sprintf("%s = extractvalue %s %s, 3", allocator, arrayType, reserved))
	return emitDynamicArrayHeader(b, op.Type, elemTypeID, data, newLength, finalCapacity, allocator)
}

func emitDynamicArrayResize(b *llvmBuilder, op *mir.DynamicArrayOp, array string, elemTypeID ir.TypeID) string {
	if op.Length == nil || op.Value == nil {
		b.emitter.markInvalid("resize requires a length and fill value")
		return array
	}
	arrayType := b.emitter.llvmType(op.Type)
	indexType := b.emitter.llvmType(b.emitter.mod.Types.IndexType())
	oldLength := b.nextReg()
	b.line(fmt.Sprintf("%s = extractvalue %s %s, 1", oldLength, arrayType, array))
	newLength := emitCast(b, &mir.Cast{Arg: op.Length, Type: b.emitter.mod.Types.IndexType()})
	resized := emitDynamicArrayReserve(b, array, op.Type, newLength)
	data := b.nextReg()
	b.line(fmt.Sprintf("%s = extractvalue %s %s, 0", data, arrayType, resized))
	capacity := b.nextReg()
	b.line(fmt.Sprintf("%s = extractvalue %s %s, 2", capacity, arrayType, resized))
	allocator := b.nextReg()
	b.line(fmt.Sprintf("%s = extractvalue %s %s, 3", allocator, arrayType, resized))
	id := b.nextID
	b.nextID++
	entryLabel := b.currentLabel
	loopLabel := fmt.Sprintf("array_resize_loop_%d", id)
	bodyLabel := fmt.Sprintf("array_resize_body_%d", id)
	continueLabel := fmt.Sprintf("array_resize_continue_%d", id)
	doneLabel := fmt.Sprintf("array_resize_done_%d", id)
	b.line(fmt.Sprintf("br label %%%s", loopLabel))
	b.namedLabel(loopLabel)
	index := b.nextReg()
	nextIndex := b.nextReg()
	b.line(fmt.Sprintf("%s = phi %s [ %s, %%%s ], [ %s, %%%s ]", index, indexType, oldLength, entryLabel, nextIndex, continueLabel))
	more := b.nextReg()
	b.line(fmt.Sprintf("%s = icmp ult %s %s, %s", more, indexType, index, newLength))
	b.line(fmt.Sprintf("br i1 %s, label %%%s, label %%%s", more, bodyLabel, doneLabel))
	b.namedLabel(bodyLabel)
	ptr := b.nextReg()
	elemType := b.emitter.llvmType(elemTypeID)
	b.line(fmt.Sprintf("%s = getelementptr %s, %s* %s, %s %s", ptr, elemType, elemType, data, indexType, index))
	b.line(fmt.Sprintf("store %s %s, %s* %s", elemType, emitRef(b, op.Value), elemType, ptr))
	b.line(fmt.Sprintf("br label %%%s", continueLabel))
	b.namedLabel(continueLabel)
	b.line(fmt.Sprintf("%s = add %s %s, 1", nextIndex, indexType, index))
	b.line(fmt.Sprintf("br label %%%s", loopLabel))
	b.namedLabel(doneLabel)
	return emitDynamicArrayHeader(b, op.Type, elemTypeID, data, newLength, capacity, allocator)
}

func emitIndexPtr(b *llvmBuilder, base string, baseType ir.TypeID, addressed bool, indexRef mir.ValueRef) string {
	if b == nil || base == "" || baseType == ir.InvalidType || indexRef == nil {
		return ""
	}
	targetID := baseType
	pointed := false
	referenced := false
	if typ, ok := b.emitter.mod.Types.Type(targetID); ok {
		switch typ.Kind {
		case ir.TypeOwnedPtr:
			targetID, pointed = typ.Elem, true
		case ir.TypeReference:
			targetID, referenced = typ.Elem, true
		}
	}
	target, ok := b.emitter.mod.Types.Type(targetID)
	if !ok || target.Kind != ir.TypeArray {
		return ""
	}
	if target.Length == "" {
		arrayType := b.emitter.llvmType(baseType)
		elemType := b.emitter.llvmType(target.Elem)
		if addressed || pointed {
			loaded := b.nextReg()
			b.line(fmt.Sprintf("%s = load %s, %s* %s", loaded, arrayType, arrayType, base))
			base = loaded
		}
		data := b.nextReg()
		b.line(fmt.Sprintf("%s = extractvalue %s %s, 0", data, arrayType, base))
		length := b.nextReg()
		b.line(fmt.Sprintf("%s = extractvalue %s %s, 1", length, arrayType, base))
		index, ok := emitBoundsCheckedIndex(b, indexRef, length)
		if !ok {
			return ""
		}
		ptr := b.nextReg()
		b.line(fmt.Sprintf("%s = getelementptr %s, %s* %s, i64 %s", ptr, elemType, elemType, data, index))
		return ptr
	}
	length, lengthErr := strconv.Atoi(target.Length)
	index := ""
	indexType := "i64"
	if indexConst, constant := indexRef.(*mir.RefConst); constant {
		indexValue, indexErr := strconv.Atoi(indexConst.Value)
		if lengthErr != nil || indexErr != nil || indexValue < 0 || indexValue >= length {
			b.emitter.invalid = true
			if b.emitter.diag != nil {
				b.emitter.diag.Add(problems.ArrayIndexOutOfBounds(indexConst.Value, target.Length, nil))
			}
			return ""
		}
		index = emitRef(b, indexRef)
		indexType = b.emitter.llvmType(mirRefType(indexRef))
	} else {
		if lengthErr != nil {
			return ""
		}
		index, ok = emitBoundsCheckedIndex(b, indexRef, target.Length)
		if !ok {
			return ""
		}
	}
	arrayType := b.emitter.llvmType(targetID)
	if !addressed && !pointed && !referenced {
		b.emitter.markInvalid("fixed-array index place requires addressable storage")
		return ""
	}
	ptr := b.nextReg()
	b.line(fmt.Sprintf("%s = getelementptr inbounds %s, %s* %s, i32 0, %s %s", ptr, arrayType, arrayType, base, indexType, index))
	return ptr
}

// Directly addressed roots need entry-block storage so one pointer dominates every place use.
func placeNeedsRootAddr(types *ir.TypeTable, place *mir.Place) bool {
	if place == nil || place.Root == nil || len(place.Projections) == 0 {
		return place != nil && place.Root != nil
	}
	projection := place.Projections[0]
	switch projection.Kind {
	case mir.PlaceProjectionDeref:
		return false
	case mir.PlaceProjectionField:
		return true
	case mir.PlaceProjectionIndex:
		rootType, ok := types.Type(mirRefType(place.Root))
		if !ok {
			return false
		}
		if rootType.Kind == ir.TypeOwnedPtr || rootType.Kind == ir.TypeReference {
			return false
		}
		return rootType.Kind == ir.TypeArray && rootType.Length != ""
	default:
		return false
	}
}

func emitPlaceRootAddr(b *llvmBuilder, root mir.ValueRef) string {
	if b == nil || root == nil {
		return ""
	}
	if ref, ok := root.(*mir.RefName); ok && ref != nil {
		if ptr := ensureLocalAddr(b, ref); ptr != "" {
			return ptr
		}
	}
	typeID := mirRefType(root)
	llvmType := b.emitter.llvmType(typeID)
	ptr := b.nextReg()
	b.line(fmt.Sprintf("%s = alloca %s", ptr, llvmType))
	b.line(fmt.Sprintf("store %s %s, %s* %s", llvmType, emitRef(b, root), llvmType, ptr))
	return ptr
}

func emitPlacePtr(b *llvmBuilder, place *mir.Place) string {
	if b == nil || place == nil || place.Root == nil {
		return ""
	}
	previousLocation := b.debugLocationID
	defer func() { b.debugLocationID = previousLocation }()

	addressed := placeNeedsRootAddr(b.emitter.mod.Types, place)
	current := ""
	if addressed {
		current = emitPlaceRootAddr(b, place.Root)
	}
	currentType := mirRefType(place.Root)
	for _, projection := range place.Projections {
		b.setLocation(projection.Location)
		switch projection.Kind {
		case mir.PlaceProjectionDeref:
			if addressed {
				llvmType := b.emitter.llvmType(currentType)
				loaded := b.nextReg()
				b.line(fmt.Sprintf("%s = load %s, %s* %s", loaded, llvmType, llvmType, current))
				current = loaded
			} else {
				current = emitRef(b, place.Root)
			}
			if !isOwnedInterfaceType(b.emitter.mod.Types, currentType) {
				if typ, ok := b.emitter.mod.Types.Type(currentType); ok && typ.Kind == ir.TypeOwnedPtr {
					llvmStructType := b.emitter.llvmType(currentType)
					data := b.nextReg()
					b.line(fmt.Sprintf("%s = extractvalue %s %s, 0", data, llvmStructType, current))
					current = data
				}
			}
			addressed = true
		case mir.PlaceProjectionField:
			current = emitFieldPtr(b, current, currentType, projection.FieldIndex)
		case mir.PlaceProjectionIndex:
			if current == "" {
				current = emitRef(b, place.Root)
			}
			current = emitIndexPtr(b, current, currentType, addressed, projection.Index)
			addressed = true
		default:
			b.emitter.markInvalid(fmt.Sprintf("unsupported MIR place projection %d", projection.Kind))
			return ""
		}
		if current == "" {
			return ""
		}
		currentType = projection.Type
	}
	if addressed {
		return current
	}
	b.setLocation(place.Location)
	return emitPlaceRootAddr(b, place.Root)
}

type llvmBuilder struct {
	out             *strings.Builder
	nextID          int
	locals          map[string]string
	localPtrs       map[string]string
	localTypes      map[string]ir.TypeID
	emitter         *llvmEmitter
	debug           *llvmDebugEmitter
	debugScopeID    int
	debugLocationID int
	currentLabel    string
}

func emitCast(b *llvmBuilder, cast *mir.Cast) string {
	if b == nil || cast == nil || cast.Arg == nil {
		return "0"
	}

	argRef := emitRef(b, cast.Arg)
	fromType := mirRefType(cast.Arg)
	toType := cast.Type
	from, fromOK := b.emitter.mod.Types.Type(fromType)
	to, toOK := b.emitter.mod.Types.Type(toType)
	if !fromOK || !toOK {
		return argRef
	}

	if fromType == toType {
		return argRef
	}
	if to.Kind == ir.TypeRawPtr {
		pointer := from.Kind == ir.TypeOwnedPtr || from.Kind == ir.TypeReference
		if pointer {
			fromLLVM := b.emitter.llvmType(fromType)
			if fromLLVM == "i8*" {
				return argRef
			}
			out := b.nextReg()
			b.line(fmt.Sprintf("%s = bitcast %s %s to i8*", out, fromLLVM, argRef))
			return out
		}
	}

	if to.Kind == ir.TypeBool {
		out := b.nextReg()
		if from.Kind == ir.TypeFloat {
			fromLLVM := b.emitter.llvmType(fromType)
			b.line(fmt.Sprintf("%s = fcmp one %s %s, 0.0", out, fromLLVM, argRef))
			return out
		}
		if _, _, ok := integerInfoID(b.emitter.mod.Types, fromType); ok {
			fromLLVM := b.emitter.llvmType(fromType)
			b.line(fmt.Sprintf("%s = icmp ne %s %s, 0", out, fromLLVM, argRef))
			return out
		}
		return argRef
	}

	if toSigned, _, ok := integerInfoID(b.emitter.mod.Types, toType); from.Kind == ir.TypeFloat && ok {
		out := b.nextReg()
		fromLLVM := b.emitter.llvmType(fromType)
		toLLVM := b.emitter.llvmType(toType)
		if toSigned {
			b.line(fmt.Sprintf("%s = fptosi %s %s to %s", out, fromLLVM, argRef, toLLVM))
		} else {
			b.line(fmt.Sprintf("%s = fptoui %s %s to %s", out, fromLLVM, argRef, toLLVM))
		}
		return out
	} else if fromSigned, _, ok := integerInfoID(b.emitter.mod.Types, fromType); ok && to.Kind == ir.TypeFloat {
		out := b.nextReg()
		fromLLVM := b.emitter.llvmType(fromType)
		toLLVM := b.emitter.llvmType(toType)
		if fromSigned {
			b.line(fmt.Sprintf("%s = sitofp %s %s to %s", out, fromLLVM, argRef, toLLVM))
		} else {
			b.line(fmt.Sprintf("%s = uitofp %s %s to %s", out, fromLLVM, argRef, toLLVM))
		}
		return out
	} else if from.Kind == ir.TypeFloat && to.Kind == ir.TypeFloat {
		if from.Bits == 64 && to.Bits == 32 {
			out := b.nextReg()
			b.line(fmt.Sprintf("%s = fptrunc double %s to float", out, argRef))
			return out
		} else if from.Bits == 32 && to.Bits == 64 {
			out := b.nextReg()
			b.line(fmt.Sprintf("%s = fpext float %s to double", out, argRef))
			return out
		}
		return argRef
	} else if fromSigned, fromBits, ok := integerInfoID(b.emitter.mod.Types, fromType); ok {
		_, toBits, ok := integerInfoID(b.emitter.mod.Types, toType)
		if !ok {
			return argRef
		}
		fromLLVM := b.emitter.llvmType(fromType)
		toLLVM := b.emitter.llvmType(toType)
		if fromBits < toBits {
			out := b.nextReg()
			if fromSigned {
				b.line(fmt.Sprintf("%s = sext %s %s to %s", out, fromLLVM, argRef, toLLVM))
			} else {
				b.line(fmt.Sprintf("%s = zext %s %s to %s", out, fromLLVM, argRef, toLLVM))
			}
			return out
		} else if fromBits > toBits {
			out := b.nextReg()
			b.line(fmt.Sprintf("%s = trunc %s %s to %s", out, fromLLVM, argRef, toLLVM))
			return out
		}
		return argRef
	}
	return argRef
}

func isMIRFloatType(typ string) bool {
	return typ == "f32" || typ == "f64"
}

func newLLVMBuilder(out *strings.Builder, emitter *llvmEmitter, debugScopeID int) *llvmBuilder {
	debug := (*llvmDebugEmitter)(nil)
	if emitter != nil {
		debug = emitter.debug
	}
	return &llvmBuilder{
		out:             out,
		nextID:          1,
		locals:          make(map[string]string),
		localPtrs:       make(map[string]string),
		localTypes:      make(map[string]ir.TypeID),
		emitter:         emitter,
		debug:           debug,
		debugScopeID:    debugScopeID,
		debugLocationID: -1,
	}
}

func (b *llvmBuilder) nextReg() string {
	name := fmt.Sprintf("%%t%d", b.nextID)
	b.nextID++
	return name
}

func (b *llvmBuilder) line(text string) {
	b.out.WriteString("  ")
	b.out.WriteString(text)
	if b.debugLocationID >= 0 {
		fmt.Fprintf(b.out, ", !dbg !%d", b.debugLocationID)
	}
	b.out.WriteString("\n")
}

func (b *llvmBuilder) label(id int) {
	b.namedLabel(fmt.Sprintf("b%d", id))
}

func (b *llvmBuilder) namedLabel(name string) {
	fmt.Fprintf(b.out, "%s:\n", name)
	b.currentLabel = name
}

func (b *llvmBuilder) setLocation(loc *source.Location) {
	if b == nil {
		return
	}
	if b.debug == nil {
		b.debugLocationID = -1
		return
	}
	b.debugLocationID = b.debug.locationID(loc, b.debugScopeID)
}

func (b *llvmBuilder) withLocation(loc *source.Location, emit func() string) string {
	if b == nil || emit == nil {
		return "0"
	}
	prev := b.debugLocationID
	b.setLocation(loc)
	out := emit()
	b.debugLocationID = prev
	return out
}

func emitValueExpr(b *llvmBuilder, expr mir.ValueExpr) string {
	return b.withLocation(mir.ValueExprLocation(expr), func() string {
		switch e := expr.(type) {
		case *mir.Move:
			return emitRef(b, e.Src)
		case *mir.Len:
			return emitLen(b, e.Value)
		case *mir.StringLiteral:
			llvmType := b.emitter.llvmType(e.Type)
			dataType := fmt.Sprintf("[%d x i8]", e.Length+1)
			data := fmt.Sprintf("getelementptr inbounds (%s, %s* %s, i64 0, i64 0)", dataType, dataType, e.Name)
			ptr := b.nextReg()
			b.line(fmt.Sprintf("%s = bitcast i8* %s to i8*", ptr, data))
			indexType := b.emitter.llvmType(b.emitter.mod.Types.IndexType())
			withData := b.nextReg()
			b.line(fmt.Sprintf("%s = insertvalue %s zeroinitializer, i8* %s, 0", withData, llvmType, ptr))
			withLength := b.nextReg()
			b.line(fmt.Sprintf("%s = insertvalue %s %s, %s %d, 1", withLength, llvmType, withData, indexType, e.Length))
			withAllocator := b.nextReg()
			b.line(fmt.Sprintf("%s = insertvalue %s %s, i8* null, 2", withAllocator, llvmType, withLength))
			return withAllocator
		case *mir.Cast:
			return emitCast(b, e)
		case *mir.Unary:
			arg := emitRef(b, e.Arg)
			typ := b.emitter.llvmType(e.Type)
			switch e.Op {
			case "-":
				out := b.nextReg()
				if isFloatType(b.emitter.mod.Types, e.Type) {
					b.line(fmt.Sprintf("%s = fsub %s 0.0, %s", out, typ, arg))
				} else {
					b.line(fmt.Sprintf("%s = sub %s 0, %s", out, typ, arg))
				}
				return out
			case "!":
				return emitLogicalNot(b, arg, e.Arg)
			case "~":
				out := b.nextReg()
				b.line(fmt.Sprintf("%s = xor %s %s, -1", out, typ, arg))
				return out
			default:
				return arg
			}
		case *mir.Binary:
			left := emitRef(b, e.Left)
			right := emitRef(b, e.Right)
			out := b.nextReg()
			leftType := b.emitter.llvmType(mirRefType(e.Left))
			switch e.Op {
			case "+":
				if isFloatType(b.emitter.mod.Types, mirRefType(e.Left)) {
					b.line(fmt.Sprintf("%s = fadd %s %s, %s", out, leftType, left, right))
				} else {
					b.line(fmt.Sprintf("%s = add %s %s, %s", out, leftType, left, right))
				}
			case "-":
				if isFloatType(b.emitter.mod.Types, mirRefType(e.Left)) {
					b.line(fmt.Sprintf("%s = fsub %s %s, %s", out, leftType, left, right))
				} else {
					b.line(fmt.Sprintf("%s = sub %s %s, %s", out, leftType, left, right))
				}
			case "*":
				if isFloatType(b.emitter.mod.Types, mirRefType(e.Left)) {
					b.line(fmt.Sprintf("%s = fmul %s %s, %s", out, leftType, left, right))
				} else {
					b.line(fmt.Sprintf("%s = mul %s %s, %s", out, leftType, left, right))
				}
			case "/":
				if isFloatType(b.emitter.mod.Types, mirRefType(e.Left)) {
					b.line(fmt.Sprintf("%s = fdiv %s %s, %s", out, leftType, left, right))
				} else if isUnsignedTypeID(b.emitter.mod.Types, mirRefType(e.Left)) {
					b.line(fmt.Sprintf("%s = udiv %s %s, %s", out, leftType, left, right))
				} else {
					b.line(fmt.Sprintf("%s = sdiv %s %s, %s", out, leftType, left, right))
				}
			case "%":
				if isFloatType(b.emitter.mod.Types, mirRefType(e.Left)) {
					b.line(fmt.Sprintf("%s = frem %s %s, %s", out, leftType, left, right))
				} else if isUnsignedTypeID(b.emitter.mod.Types, mirRefType(e.Left)) {
					b.line(fmt.Sprintf("%s = urem %s %s, %s", out, leftType, left, right))
				} else {
					b.line(fmt.Sprintf("%s = srem %s %s, %s", out, leftType, left, right))
				}
			case "&":
				b.line(fmt.Sprintf("%s = and %s %s, %s", out, leftType, left, right))
			case "|":
				b.line(fmt.Sprintf("%s = or %s %s, %s", out, leftType, left, right))
			case "^":
				b.line(fmt.Sprintf("%s = xor %s %s, %s", out, leftType, left, right))
			case "<<", ">>":
				_, bits, ok := integerInfoID(b.emitter.mod.Types, mirRefType(e.Left))
				if !ok {
					b.emitter.markInvalid("shift lowering requires integral operands")
					return left
				}
				invalid := b.nextReg()
				b.line(fmt.Sprintf("%s = icmp uge %s %s, %d", invalid, leftType, right, bits))
				shiftID := b.nextID
				b.nextID++
				failLabel := fmt.Sprintf("shift_fail_%d", shiftID)
				readyLabel := fmt.Sprintf("shift_ready_%d", shiftID)
				b.line(fmt.Sprintf("br i1 %s, label %%%s, label %%%s", invalid, failLabel, readyLabel))
				b.namedLabel(failLabel)
				b.line("call void @llvm.trap()")
				b.line("unreachable")
				b.namedLabel(readyLabel)
				opcode := "shl"
				if e.Op == ">>" {
					opcode = "ashr"
					if isUnsignedTypeID(b.emitter.mod.Types, mirRefType(e.Left)) {
						opcode = "lshr"
					}
				}
				b.line(fmt.Sprintf("%s = %s %s %s, %s", out, opcode, leftType, left, right))
			case "==", "!=", "<", "<=", ">", ">=":
				if result, ok := emitOptionalNoneCompare(b, e.Op, e.Left, e.Right, left, right); ok {
					return result
				}
				cmp := b.nextReg()
				if isFloatType(b.emitter.mod.Types, mirRefType(e.Left)) {
					pred := map[string]string{"==": "oeq", "!=": "one", "<": "olt", "<=": "ole", ">": "ogt", ">=": "oge"}[e.Op]
					b.line(fmt.Sprintf("%s = fcmp %s %s %s, %s", cmp, pred, leftType, left, right))
				} else {
					pred := integerComparePredID(b.emitter.mod.Types, e.Op, mirRefType(e.Left))
					b.line(fmt.Sprintf("%s = icmp %s %s %s, %s", cmp, pred, leftType, left, right))
				}
				return cmp
			case "&&", "||":
				lc := emitCondRef(b, e.Left)
				rc := emitCondRef(b, e.Right)
				merged := b.nextReg()
				if e.Op == "&&" {
					b.line(fmt.Sprintf("%s = and i1 %s, %s", merged, lc, rc))
				} else {
					b.line(fmt.Sprintf("%s = or i1 %s, %s", merged, lc, rc))
				}
				return merged
			default:
				return left
			}
			return out
		case *mir.Call:
			out := b.nextReg()
			emitCall(b, out, b.emitter.llvmType(e.Type), emitRef(b, e.Callee), llvmCallArgs(b, e.Args))
			return out
		case *mir.AddrOf:
			return emitPlacePtr(b, e.Place)
		case *mir.SliceView:
			return emitSliceView(b, e)
		case *mir.Load:
			ptr := emitPlacePtr(b, e.Place)
			if ptr == "" {
				return "0"
			}
			llvmType := b.emitter.llvmType(e.Type)
			out := b.nextReg()
			b.line(fmt.Sprintf("%s = load %s, %s* %s", out, llvmType, llvmType, ptr))
			return out
		case *mir.Field:
			base := emitRef(b, e.Base)
			baseType := mirRefType(e.Base)
			llvmBaseType, ok := llvmTypeID(b.emitter.mod.Types, baseType)
			if !ok {
				return "0"
			}
			out := b.nextReg()
			b.line(fmt.Sprintf("%s = extractvalue %s %s, %d", out, llvmBaseType, base, e.Index))
			return out
		case *mir.StructLit:
			llvmType := b.emitter.llvmType(e.Type)
			current := "zeroinitializer"
			for i, field := range e.Fields {
				value := emitRef(b, field)
				next := b.nextReg()
				fieldType := b.emitter.llvmType(mirRefType(field))
				b.line(fmt.Sprintf("%s = insertvalue %s %s, %s %s, %d", next, llvmType, current, fieldType, value, i))
				current = next
			}
			return current
		case *mir.ArrayLit:
			llvmType := b.emitter.llvmType(e.Type)
			current := "zeroinitializer"
			for i, item := range e.Values {
				value := emitRef(b, item)
				next := b.nextReg()
				itemType := b.emitter.llvmType(mirRefType(item))
				b.line(fmt.Sprintf("%s = insertvalue %s %s, %s %s, %d", next, llvmType, current, itemType, value, i))
				current = next
			}
			return current
		case *mir.DynamicArrayAlloc:
			return emitDynamicArrayAlloc(b, e)
		case *mir.DynamicArrayOp:
			return emitDynamicArrayOp(b, e)
		case *mir.Alloc:
			return emitAlloc(b, e)
		case *mir.ZeroValue:
			return "zeroinitializer"
		case *mir.OptionalSome:
			optional, ok := b.emitter.mod.Types.Type(e.Type)
			if !ok || optional.Kind != ir.TypeOptional {
				return "0"
			}
			value := emitRef(b, e.Value)
			llvmType := b.emitter.llvmType(e.Type)
			valueType := b.emitter.llvmType(mirRefType(e.Value))
			withTag := b.nextReg()
			b.line(fmt.Sprintf("%s = insertvalue %s zeroinitializer, i1 true, 0", withTag, llvmType))
			withValue := b.nextReg()
			b.line(fmt.Sprintf("%s = insertvalue %s %s, %s %s, 1", withValue, llvmType, withTag, valueType, value))
			return withValue
		case *mir.InterfaceMake:
			llvmType := b.emitter.llvmType(e.Type)
			value := emitRef(b, e.Value)
			valueType := b.emitter.llvmType(mirRefType(e.Value))
			dataPtr := value
			dataLlvmType := valueType
			allocator := ""
			if valueTypeInfo, isOwned := b.emitter.mod.Types.Type(mirRefType(e.Value)); isOwned && valueTypeInfo.Kind == ir.TypeOwnedPtr {
				if !isOwnedInterfaceType(b.emitter.mod.Types, mirRefType(e.Value)) {
					dataPtr = b.nextReg()
					b.line(fmt.Sprintf("%s = extractvalue %s %s, 0", dataPtr, valueType, value))
					allocator = b.nextReg()
					b.line(fmt.Sprintf("%s = extractvalue %s %s, 1", allocator, valueType, value))
					targetLLVM, _ := llvmTypeID(b.emitter.mod.Types, valueTypeInfo.Elem)
					dataLlvmType = targetLLVM + "*"
				}
			}
			dataBytePtr := b.nextReg()
			b.line(fmt.Sprintf("%s = bitcast %s %s to i8*", dataBytePtr, dataLlvmType, dataPtr))
			itabSym := interfaceSymbolName("itab", b.emitter.mod.Types, e.Type, e.DataType)
			itabPtr := b.nextReg()
			b.line(fmt.Sprintf("%s = bitcast [%d x i8*]* %s to i8*", itabPtr, interfaceVtableLength(b.emitter.mod.Types, e.Type, len(e.Slots)), itabSym))
			current := "zeroinitializer"
			reg1 := b.nextReg()
			b.line(fmt.Sprintf("%s = insertvalue %s %s, i8* %s, 0", reg1, llvmType, current, dataBytePtr))
			reg2 := b.nextReg()
			b.line(fmt.Sprintf("%s = insertvalue %s %s, i8* %s, 1", reg2, llvmType, reg1, itabPtr))
			if allocator == "" {
				return reg2
			}
			reg3 := b.nextReg()
			b.line(fmt.Sprintf("%s = insertvalue %s %s, i8* %s, 2", reg3, llvmType, reg2, allocator))
			return reg3
		case *mir.InterfaceCall:
			data, fn, ok := emitInterfaceCallTarget(b, e.Base, e.Slot)
			if !ok {
				return "0"
			}
			out := b.nextReg()
			args := append([]string{"i8* " + data}, llvmCallArgs(b, e.Args)...)
			emitCall(b, out, b.emitter.llvmType(e.Type), fn, args)
			if consumesOwnedInterfaceStorage(b.emitter.mod.Types, e) {
				emitInterfaceStorageRelease(b, mirRefType(e.Base), emitRef(b, e.Base), data)
			}
			return out
		default:
			return "0"
		}
	})
}

func emitOptionalNoneCompare(b *llvmBuilder, op string, leftRef, rightRef mir.ValueRef, leftValue, rightValue string) (string, bool) {
	if op != "==" && op != "!=" {
		return "", false
	}
	leftType, leftOK := b.emitter.mod.Types.Type(mirRefType(leftRef))
	rightType, rightOK := b.emitter.mod.Types.Type(mirRefType(rightRef))
	leftOptional := leftOK && leftType.Kind == ir.TypeOptional
	rightOptional := rightOK && rightType.Kind == ir.TypeOptional
	if !leftOptional && !rightOptional {
		return "", false
	}
	leftNone := leftValue == "zeroinitializer"
	rightNone := rightValue == "zeroinitializer"
	if leftNone && rightNone {
		if op == "==" {
			return "true", true
		}
		return "false", true
	}
	var valueRef mir.ValueRef
	var value string
	if leftNone {
		valueRef = rightRef
		value = rightValue
	} else if rightNone {
		valueRef = leftRef
		value = leftValue
	} else {
		if b != nil && b.emitter != nil {
			b.emitter.markInvalid("optional equality currently requires `none` on one side")
		}
		return "false", true
	}
	llvmType := b.emitter.llvmType(mirRefType(valueRef))
	tag := b.nextReg()
	b.line(fmt.Sprintf("%s = extractvalue %s %s, 0", tag, llvmType, value))
	cmp := b.nextReg()
	pred := "eq"
	if op == "!=" {
		pred = "ne"
	}
	b.line(fmt.Sprintf("%s = icmp %s i1 %s, false", cmp, pred, tag))
	return cmp, true
}

func emitRef(b *llvmBuilder, ref mir.ValueRef) string {
	return b.withLocation(mir.ValueRefLocation(ref), func() string {
		switch v := ref.(type) {
		case *mir.RefConst:
			typ, ok := b.emitter.mod.Types.Type(v.Type)
			if !ok {
				return "0"
			}
			if typ.Kind == ir.TypeBool && v.Value != "false" && v.Value != "true" {
				if b.emitter != nil {
					b.emitter.markInvalid("invalid boolean constant: " + v.Value)
				}
				return "false"
			}
			if typ.Kind == ir.TypeFloat {
				return llvmFloatConst(v.Value, typ.Bits)
			}
			if typ.Kind == ir.TypeCStr {
				return "null"
			}
			return v.Value
		case *mir.RefName:
			typ, _ := b.emitter.mod.Types.Type(v.Type)
			isFunc := typ.Kind == ir.TypeFunction
			if ptr, ok := b.localPtrs[v.Name]; ok && ptr != "" {
				reg := b.nextReg()
				llvmType := b.emitter.llvmType(b.localTypes[v.Name])
				b.line(fmt.Sprintf("%s = load %s, %s* %s", reg, llvmType, llvmType, ptr))
				return reg
			}
			if reg, ok := b.locals[v.Name]; ok {
				return reg
			}
			if isFunc {
				return "@" + ir.SanitizeSymbolName(ir.StripSymbolInstance(v.Name))
			}

			isLocalStatic := false
			var localEntry *mir.StaticEntry
			if b.emitter != nil && b.emitter.mod != nil {
				for _, entry := range b.emitter.mod.StaticData {
					eName := strings.TrimPrefix(entry.Name, "@")
					vName := strings.TrimPrefix(v.Name, "@")
					if eName == vName {
						isLocalStatic = true
						localEntry = entry
						break
					}
				}
			}

			if isLocalStatic && localEntry != nil {
				if localEntry.Bytes {
					arrayType := fmt.Sprintf("[%d x i8]", len(localEntry.Value)+1)
					return fmt.Sprintf("getelementptr inbounds (%s, %s* %s, i64 0, i64 0)", arrayType, arrayType, localEntry.Name)
				}
				reg := b.nextReg()
				llvmType := b.emitter.llvmType(localEntry.Type)
				b.line(fmt.Sprintf("%s = load %s, %s* %s, align %d", reg, llvmType, llvmType, localEntry.Name, localEntry.Align))
				return reg
			}

			if idx := strings.IndexByte(v.Name, '$'); idx >= 0 {
				name := "@" + v.Name
				if b.emitter.externalGlobals == nil {
					b.emitter.externalGlobals = make(map[string]ir.TypeID)
				}
				b.emitter.externalGlobals[name] = v.Type

				reg := b.nextReg()
				llvmType := b.emitter.llvmType(v.Type)
				b.line(fmt.Sprintf("%s = load %s, %s* %s, align 4", reg, llvmType, llvmType, name))
				return reg
			}

			if strings.HasPrefix(v.Name, "@") {
				return v.Name
			}
			return "0"
		default:
			return "0"
		}
	})
}

func ensureLocalAddr(b *llvmBuilder, ref *mir.RefName) string {
	if b == nil || ref == nil {
		return ""
	}
	if ptr, ok := b.localPtrs[ref.Name]; ok && ptr != "" {
		return ptr
	}
	reg, ok := b.locals[ref.Name]
	if !ok || reg == "" {
		return ""
	}
	typeText := b.localTypes[ref.Name]
	if typeText == ir.InvalidType {
		typeText = ref.Type
	}
	if typeText == ir.InvalidType {
		return ""
	}
	llvmType := b.emitter.llvmType(typeText)
	ptr := b.nextReg()
	b.line(fmt.Sprintf("%s = alloca %s", ptr, llvmType))
	b.line(fmt.Sprintf("store %s %s, %s* %s", llvmType, reg, llvmType, ptr))
	b.localPtrs[ref.Name] = ptr
	b.localTypes[ref.Name] = typeText
	return ptr
}

func llvmFloatConst(value string, bits int) string {
	parsed, err := strconv.ParseFloat(value, bits)
	if err != nil {
		return value
	}
	if bits == 32 {
		parsed = float64(float32(parsed))
	}
	return fmt.Sprintf("0x%016X", math.Float64bits(parsed))
}

func emitCondRef(b *llvmBuilder, ref mir.ValueRef) string {
	return b.withLocation(mir.ValueRefLocation(ref), func() string {
		val := emitRef(b, ref)
		refType := mirRefType(ref)
		if typ, ok := b.emitter.mod.Types.Type(refType); ok && typ.Kind == ir.TypeBool {
			return val
		}
		if b != nil && b.emitter != nil {
			b.emitter.markInvalid("non-bool condition reached llvm lowering: " + b.emitter.mod.Types.Text(refType))
		}
		return "false"
	})
}

func mirRefType(ref mir.ValueRef) ir.TypeID {
	switch v := ref.(type) {
	case *mir.RefConst:
		return v.Type
	case *mir.RefName:
		return v.Type
	default:
		return ir.InvalidType
	}
}

func emitLogicalNot(b *llvmBuilder, arg string, ref mir.ValueRef) string {
	if typ, ok := b.emitter.mod.Types.Type(mirRefType(ref)); ok && typ.Kind == ir.TypeBool {
		out := b.nextReg()
		b.line(fmt.Sprintf("%s = xor i1 %s, true", out, arg))
		return out
	}
	cmp := emitCondRef(b, ref)
	out := b.nextReg()
	b.line(fmt.Sprintf("%s = xor i1 %s, true", out, cmp))
	return out
}

func isFloatType(types *ir.TypeTable, id ir.TypeID) bool {
	typ, ok := types.Type(id)
	return ok && typ.Kind == ir.TypeFloat
}

func llvmEscapeString(s string) string {
	var sb strings.Builder
	for i := range len(s) {
		b := s[i]
		if b == '\\' {
			sb.WriteString(`\5C`)
		} else if b == '"' {
			sb.WriteString(`\22`)
		} else if b >= 32 && b <= 126 {
			sb.WriteByte(b)
		} else {
			fmt.Fprintf(&sb, "\\%02X", b)
		}
	}
	sb.WriteString(`\00`)
	return sb.String()
}

type callDecl struct {
	Name       string
	ReturnType ir.TypeID
	Params     []ir.TypeID
}

func collectCallDecls(mod *mir.Module) []callDecl {
	if mod == nil {
		return nil
	}
	defined := make(map[string]struct{})
	for _, fn := range mod.Funcs {
		if fn != nil && fn.Name != "" {
			defined[fn.Name] = struct{}{}
		}
	}
	decls := make(map[string]callDecl)
	for _, fn := range mod.Funcs {
		if fn == nil || fn.Blocks == nil {
			continue
		}
		for _, block := range fn.Blocks {
			if block == nil {
				continue
			}
			for _, instr := range block.Instrs {
				switch callInstr := instr.(type) {
				case *mir.Assign:
					call, ok := callInstr.Value.(*mir.Call)
					if !ok || call == nil {
						continue
					}
					recordCallDecl(decls, defined, call)
				case *mir.Call:
					recordCallDecl(decls, defined, callInstr)
				}
			}
		}
	}
	out := make([]callDecl, 0, len(decls))
	for _, decl := range decls {
		out = append(out, decl)
	}
	return out
}

func recordCallDecl(decls map[string]callDecl, defined map[string]struct{}, call *mir.Call) {
	if call == nil {
		return
	}
	nameRef, ok := call.Callee.(*mir.RefName)
	if !ok || nameRef == nil {
		return
	}
	name := nameRef.Name
	if idx := strings.IndexByte(name, '$'); idx >= 0 {
		name = name[:idx]
	}
	if _, ok := defined[name]; ok {
		return
	}
	params := make([]ir.TypeID, 0, len(call.Args))
	for _, arg := range call.Args {
		params = append(params, mirRefType(arg))
	}
	decls[name] = callDecl{Name: name, ReturnType: call.Type, Params: params}
}
