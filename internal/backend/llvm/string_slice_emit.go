package llvm

import (
	"fmt"
	"strconv"

	"compiler/internal/ir"
	"compiler/internal/ir/mir"
)

func emitTargetIndexAsI64(b *llvmBuilder, value llvmValue) llvmValue {
	if value.Layout.Text == "i64" {
		return value
	}
	if value.Layout.Text != "i32" {
		b.emitter.markInvalid("unsupported target index type " + value.Layout.Text)
		return value
	}
	return b.cast("zext", value, llvmScalarLayout("i64"))
}

func normalizeIndexForLength(b *llvmBuilder, indexRef mir.ValueRef, lengthI64 llvmValue) (compareIndex, compareLength, indexI64 llvmValue, ok bool) {
	if b == nil || indexRef == nil {
		return llvmValue{}, llvmValue{}, llvmValue{}, false
	}
	indexType := mirRefType(indexRef)
	_, indexBits, ok := integerInfoID(b.emitter.mod.Types, indexType)
	if !ok {
		b.emitter.markInvalid("indexed access lowering requires integral index")
		return llvmValue{}, llvmValue{}, llvmValue{}, false
	}
	compareIndex = emitRef(b, indexRef)
	compareLength = lengthI64
	indexI64 = compareIndex
	if indexBits < 64 {
		u64 := b.emitter.mod.Types.Intern(ir.Type{Kind: ir.TypeInteger, Bits: 64})
		compareIndex = emitCast(b, &mir.Cast{Arg: indexRef, Type: u64})
		indexI64 = compareIndex
	} else if indexBits > 64 {
		compareLength = b.cast("zext", lengthI64, compareIndex.Layout)
		indexI64 = b.cast("trunc", compareIndex, llvmScalarLayout("i64"))
	}
	return compareIndex, compareLength, indexI64, true
}

func emitBoundsCheckedIndex(b *llvmBuilder, indexRef mir.ValueRef, length llvmValue) (llvmValue, bool) {
	compareIndex, compareLength, index, ok := normalizeIndexForLength(b, indexRef, length)
	if !ok {
		return llvmValue{}, false
	}
	// Unsigned comparison also rejects negative signed indexes after sign extension.
	outOfBounds := b.compare("icmp", "uge", compareIndex, compareLength)
	boundsID := b.nextID
	b.nextID++
	failLabel := fmt.Sprintf("bounds_fail_%d", boundsID)
	okLabel := fmt.Sprintf("bounds_ok_%d", boundsID)
	b.condBranch(outOfBounds, failLabel, okLabel)
	b.namedLabel(failLabel)
	b.trap()
	b.namedLabel(okLabel)
	return index, true
}

func emitSliceBounds(b *llvmBuilder, view *mir.SliceView, lengthI64 llvmValue) (llvmValue, llvmValue, bool) {
	i64 := llvmScalarLayout("i64")
	startI64 := b.value("0", i64)
	endI64 := lengthI64
	var invalid llvmValue
	if view.Start != nil {
		start, compareLength, normalized, ok := normalizeIndexForLength(b, view.Start, lengthI64)
		if !ok {
			return llvmValue{}, llvmValue{}, false
		}
		startI64 = normalized
		invalid = b.compare("icmp", "ugt", start, compareLength)
	}
	if view.End != nil {
		end, compareLength, normalized, ok := normalizeIndexForLength(b, view.End, lengthI64)
		if !ok {
			return llvmValue{}, llvmValue{}, false
		}
		endI64 = normalized
		predicate := "ugt"
		if !view.EndExclusive {
			predicate = "uge"
		}
		endInvalid := b.compare("icmp", predicate, end, compareLength)
		if invalid.Layout == nil {
			invalid = endInvalid
		} else {
			invalid = b.arithmetic("or", invalid, endInvalid)
		}
	}

	boundsID := b.nextID
	b.nextID++
	failLabel := fmt.Sprintf("slice_bounds_fail_%d", boundsID)
	normalizedLabel := fmt.Sprintf("slice_bounds_normalized_%d", boundsID)
	readyLabel := fmt.Sprintf("slice_bounds_ready_%d", boundsID)
	failEmitted := false
	if invalid.Layout != nil {
		b.condBranch(invalid, failLabel, normalizedLabel)
		b.namedLabel(failLabel)
		b.trap()
		b.namedLabel(normalizedLabel)
		failEmitted = true
	}
	if view.End != nil && !view.EndExclusive {
		endI64 = b.arithmetic("add", endI64, b.value("1", i64))
	}
	reversed := b.compare("icmp", "ugt", startI64, endI64)
	b.condBranch(reversed, failLabel, readyLabel)
	if !failEmitted {
		b.namedLabel(failLabel)
		b.trap()
	}
	b.namedLabel(readyLabel)
	return startI64, endI64, true
}

func emitSliceView(b *llvmBuilder, view *mir.SliceView) llvmValue {
	if b == nil || view == nil {
		return llvmValue{}
	}
	resultLayout := b.emitter.layout(view.Type)
	if view.Source == nil {
		return b.zero(resultLayout)
	}
	sourceTypeID := view.Source.Type
	targetTypeID := sourceTypeID
	if sourceType, ok := b.emitter.mod.Types.Type(sourceTypeID); ok && sourceType.Kind == ir.TypeReference {
		targetTypeID = sourceType.Elem
	}
	targetType, ok := b.emitter.mod.Types.Type(targetTypeID)
	if ok && targetType.Kind == ir.TypeString {
		return emitStringSliceView(b, view)
	}
	if !ok || (targetType.Kind != ir.TypeArray && targetType.Kind != ir.TypeSlice) {
		b.emitter.markInvalid("slice view source shape is not lowerable in current compiler stage")
		return b.zero(resultLayout)
	}
	var data, length llvmValue
	var fixedArrayPlace llvmPlace
	if targetType.Kind == ir.TypeSlice || targetType.Length == "" {
		var source llvmValue
		if sliceViewUsesPlacePtr(b.emitter.mod.Types, view.Source) {
			ptr, ok := emitPlacePtr(b, view.Source)
			if !ok {
				return b.zero(resultLayout)
			}
			source = b.load(ptr)
		} else {
			source = emitRef(b, view.Source.Root)
			// Dynamic-owner references are pointers to carrier headers. Slice
			// references are already carrier aggregates and must stay unloaded.
			if source.Layout.Kind == llvmLayoutPointer {
				source = b.load(b.pointerPlace(source))
			}
		}
		data = b.extractField(source, llvmFieldData)
		length = b.extractField(source, llvmFieldLength)
	} else {
		length = b.value(targetType.Length, llvmScalarLayout("i64"))
		if sliceViewUsesPlacePtr(b.emitter.mod.Types, view.Source) {
			ptr, found := emitPlacePtr(b, view.Source)
			if found {
				fixedArrayPlace = ptr
			}
		} else {
			root := emitRef(b, view.Source.Root)
			if root.Layout.Kind == llvmLayoutPointer {
				fixedArrayPlace = b.pointerPlace(root)
			}
		}
		if fixedArrayPlace.Pointee == nil {
			b.emitter.markInvalid("fixed-array slicing requires addressable storage")
			return b.zero(resultLayout)
		}
	}

	indexLayout := b.emitter.layout(b.emitter.mod.Types.IndexType())
	lengthI64 := length
	if fixedArrayPlace.Pointee == nil {
		lengthI64 = emitTargetIndexAsI64(b, length)
	}
	startI64, endI64, ok := emitSliceBounds(b, view, lengthI64)
	if !ok {
		return b.zero(resultLayout)
	}

	if fixedArrayPlace.Pointee != nil {
		data = b.pointerValue(b.arrayElement(fixedArrayPlace, b.value("0", llvmScalarLayout("i32")), false))
	}
	adjustedData := b.pointerValue(b.gep(b.pointerPlace(data), startI64, false))
	viewLength := b.arithmetic("sub", endI64, startI64)
	if indexLayout.Text != "i64" {
		viewLength = b.cast("trunc", viewLength, indexLayout)
	}
	result := b.insertField(b.zero(resultLayout), adjustedData, llvmFieldData)
	return b.insertField(result, viewLength, llvmFieldLength)
}

func emitStringDataAndLength(b *llvmBuilder, value llvmValue) (llvmValue, llvmValue) {
	return b.extractField(value, llvmFieldData), b.extractField(value, llvmFieldLength)
}

func emitStringEqual(b *llvmBuilder, left, right llvmValue) llvmValue {
	leftData, leftLength := emitStringDataAndLength(b, left)
	rightData, rightLength := emitStringDataAndLength(b, right)

	id := b.nextID
	b.nextID++
	lengthMatchLabel := fmt.Sprintf("string_equal_length_match_%d", id)
	loopLabel := fmt.Sprintf("string_equal_loop_%d", id)
	bodyLabel := fmt.Sprintf("string_equal_body_%d", id)
	continueLabel := fmt.Sprintf("string_equal_continue_%d", id)
	falseLabel := fmt.Sprintf("string_equal_false_%d", id)
	trueLabel := fmt.Sprintf("string_equal_true_%d", id)
	mergeLabel := fmt.Sprintf("string_equal_merge_%d", id)

	lengthEqual := b.compare("icmp", "eq", leftLength, rightLength)
	b.condBranch(lengthEqual, lengthMatchLabel, falseLabel)
	b.namedLabel(falseLabel)
	b.branch(mergeLabel)

	b.namedLabel(lengthMatchLabel)
	zero := b.value("0", leftLength.Layout)
	empty := b.compare("icmp", "eq", leftLength, zero)
	b.condBranch(empty, trueLabel, loopLabel)

	b.namedLabel(loopLabel)
	index := b.nextValue(leftLength.Layout)
	nextIndex := b.nextValue(leftLength.Layout)
	b.definePhi(index,
		llvmIncoming{Value: zero, Label: lengthMatchLabel},
		llvmIncoming{Value: nextIndex, Label: continueLabel},
	)
	more := b.compare("icmp", "ult", index, leftLength)
	b.condBranch(more, bodyLabel, trueLabel)

	b.namedLabel(bodyLabel)
	leftByte := b.load(b.gep(b.pointerPlace(leftData), index, false))
	rightByte := b.load(b.gep(b.pointerPlace(rightData), index, false))
	bytesEqual := b.compare("icmp", "eq", leftByte, rightByte)
	b.condBranch(bytesEqual, continueLabel, falseLabel)

	b.namedLabel(continueLabel)
	b.defineArithmetic(nextIndex, "add", index, b.value("1", index.Layout))
	b.branch(loopLabel)

	b.namedLabel(trueLabel)
	b.branch(mergeLabel)
	b.namedLabel(mergeLabel)
	return b.phi(llvmScalarLayout("i1"),
		llvmIncoming{Value: b.value("false", llvmScalarLayout("i1")), Label: falseLabel},
		llvmIncoming{Value: b.value("true", llvmScalarLayout("i1")), Label: trueLabel},
	)
}

func emitStringSliceView(b *llvmBuilder, view *mir.SliceView) llvmValue {
	if b == nil || view == nil {
		return llvmValue{}
	}
	resultLayout := b.emitter.layout(view.Type)
	if view.Source == nil {
		return b.zero(resultLayout)
	}
	_, ok := b.emitter.mod.Types.Type(view.Source.Type)
	if !ok {
		b.emitter.markInvalid("string slice view has invalid source type")
		return b.zero(resultLayout)
	}
	var source llvmValue
	if len(view.Source.Projections) > 0 {
		ptr, ok := emitPlacePtr(b, view.Source)
		if !ok {
			return b.zero(resultLayout)
		}
		source = b.load(ptr)
	} else {
		source = emitRef(b, view.Source.Root)
	}
	data, length := emitStringDataAndLength(b, source)

	indexLayout := b.emitter.layout(b.emitter.mod.Types.IndexType())
	lengthI64 := emitTargetIndexAsI64(b, length)
	startI64, endI64, ok := emitSliceBounds(b, view, lengthI64)
	if !ok {
		return b.zero(resultLayout)
	}

	resultType, ok := b.emitter.mod.Types.Type(view.Type)
	if !ok || resultType.Kind != ir.TypeReference {
		b.emitter.markInvalid("string slice view has invalid result type")
		return b.zero(resultLayout)
	}
	resultTarget, ok := b.emitter.mod.Types.Type(resultType.Elem)
	if !ok {
		b.emitter.markInvalid("string slice view has invalid result target")
		return b.zero(resultLayout)
	}
	if resultTarget.Kind == ir.TypeString {
		var boundaryValid llvmValue
		for _, index := range []llvmValue{startI64, endI64} {
			boundary := emitUTF8BoundaryCheck(b, data, index, lengthI64)
			if boundaryValid.Layout == nil {
				boundaryValid = boundary
			} else {
				boundaryValid = b.arithmetic("and", boundaryValid, boundary)
			}
		}
		boundaryID := b.nextID
		b.nextID++
		boundaryFail := fmt.Sprintf("string_boundary_fail_%d", boundaryID)
		boundaryReady := fmt.Sprintf("string_boundary_ready_%d", boundaryID)
		b.condBranch(boundaryValid, boundaryReady, boundaryFail)
		b.namedLabel(boundaryFail)
		b.trap()
		b.namedLabel(boundaryReady)
	}

	adjustedData := b.pointerValue(b.gep(b.pointerPlace(data), startI64, false))
	viewLength := b.arithmetic("sub", endI64, startI64)
	if indexLayout.Text != "i64" {
		viewLength = b.cast("trunc", viewLength, indexLayout)
	}
	result := b.insertField(b.zero(resultLayout), adjustedData, llvmFieldData)
	return b.insertField(result, viewLength, llvmFieldLength)
}

func emitUTF8BoundaryCheck(b *llvmBuilder, data, index, length llvmValue) llvmValue {
	atEnd := b.compare("icmp", "eq", index, length)
	id := b.nextID
	b.nextID++
	loadLabel := fmt.Sprintf("utf8_boundary_load_%d", id)
	endLabel := fmt.Sprintf("utf8_boundary_end_%d", id)
	mergeLabel := fmt.Sprintf("utf8_boundary_merge_%d", id)
	b.condBranch(atEnd, endLabel, loadLabel)
	b.namedLabel(loadLabel)
	value := b.load(b.gep(b.pointerPlace(data), index, false))
	masked := b.arithmetic("and", value, b.value("-64", value.Layout))
	continuation := b.compare("icmp", "eq", masked, b.value("-128", value.Layout))
	notContinuation := b.arithmetic("xor", continuation, b.value("true", continuation.Layout))
	b.branch(mergeLabel)
	b.namedLabel(endLabel)
	b.branch(mergeLabel)
	b.namedLabel(mergeLabel)
	return b.phi(llvmScalarLayout("i1"),
		llvmIncoming{Value: b.value("true", llvmScalarLayout("i1")), Label: endLabel},
		llvmIncoming{Value: notContinuation, Label: loadLabel},
	)
}

func emitStringChars(b *llvmBuilder, chars *mir.StringChars) llvmValue {
	if b == nil || chars == nil {
		return llvmValue{}
	}
	resultLayout := b.emitter.layout(chars.Type)
	if chars.Value == nil {
		return b.zero(resultLayout)
	}
	refType, ok := b.emitter.mod.Types.Type(mirRefType(chars.Value))
	if !ok || refType.Kind != ir.TypeReference {
		b.emitter.markInvalid("string character conversion requires a string reference")
		return b.zero(resultLayout)
	}
	stringType, ok := b.emitter.mod.Types.Type(refType.Elem)
	if !ok || stringType.Kind != ir.TypeString {
		b.emitter.markInvalid("string character conversion requires a string reference")
		return b.zero(resultLayout)
	}
	arrayType, ok := b.emitter.mod.Types.Type(chars.Type)
	if !ok || arrayType.Kind != ir.TypeArray || arrayType.Length != "" || arrayType.Elem == ir.InvalidType {
		b.emitter.markInvalid("string character conversion has invalid result type")
		return b.zero(resultLayout)
	}
	if elemType, ok := b.emitter.mod.Types.Type(arrayType.Elem); !ok || elemType.Kind != ir.TypeChar {
		b.emitter.markInvalid("string character conversion result must be a char array")
		return b.zero(resultLayout)
	}

	data, length := emitStringDataAndLength(b, emitRef(b, chars.Value))
	lengthI64 := emitTargetIndexAsI64(b, length)
	indexLayout := b.emitter.layout(b.emitter.mod.Types.IndexType())
	count := emitUTF8CodepointCount(b, data, lengthI64)
	id := b.nextID
	b.nextID++
	countForHeader := count
	if indexLayout.Text != "i64" {
		tooLarge := b.compare("icmp", "ugt", count, b.value("4294967295", count.Layout))
		trapLabel := fmt.Sprintf("string_chars_length_fail_%d", id)
		lengthReady := fmt.Sprintf("string_chars_length_ready_%d", id)
		b.condBranch(tooLarge, trapLabel, lengthReady)
		b.namedLabel(trapLabel)
		b.trap()
		b.namedLabel(lengthReady)
		countForHeader = b.cast("trunc", count, indexLayout)
	}
	countValue := countForHeader
	allocator := emitDefaultAllocatorHandle(b)
	zero := b.compare("icmp", "eq", count, b.value("0", count.Layout))
	emptyLabel := fmt.Sprintf("string_chars_empty_%d", id)
	allocateLabel := fmt.Sprintf("string_chars_allocate_%d", id)
	readyLabel := fmt.Sprintf("string_chars_ready_%d", id)
	b.condBranch(zero, emptyLabel, allocateLabel)
	b.namedLabel(emptyLabel)
	b.branch(readyLabel)
	emptyBlock := b.currentLabel
	b.namedLabel(allocateLabel)
	allocated := emitDynamicArrayStorageAlloc(b, arrayType.Elem, countValue, allocator)
	b.branch(readyLabel)
	allocatedBlock := b.currentLabel
	b.namedLabel(readyLabel)
	charData := b.phi(allocated.Layout, llvmIncoming{Value: b.value("null", allocated.Layout), Label: emptyBlock}, llvmIncoming{Value: allocated, Label: allocatedBlock})
	return emitStringCharsFill(b, data, lengthI64, charData, countValue, chars.Type, allocator)
}

func emitStringCharsFill(b *llvmBuilder, data, length, charData, count llvmValue, arrayType ir.TypeID, allocator llvmValue) llvmValue {
	id := b.nextID
	b.nextID++
	entryLabel := b.currentLabel
	loopLabel := fmt.Sprintf("string_chars_fill_loop_%d", id)
	bodyLabel := fmt.Sprintf("string_chars_fill_body_%d", id)
	continueLabel := fmt.Sprintf("string_chars_fill_continue_%d", id)
	doneLabel := fmt.Sprintf("string_chars_fill_done_%d", id)
	b.branch(loopLabel)
	b.namedLabel(loopLabel)
	i64 := llvmScalarLayout("i64")
	nextByteIndex := b.nextValue(i64)
	nextCharIndex := b.nextValue(i64)
	byteIndex := b.nextValue(i64)
	charIndex := b.nextValue(i64)
	b.definePhi(byteIndex, llvmIncoming{Value: b.value("0", i64), Label: entryLabel}, llvmIncoming{Value: nextByteIndex, Label: continueLabel})
	b.definePhi(charIndex, llvmIncoming{Value: b.value("0", i64), Label: entryLabel}, llvmIncoming{Value: nextCharIndex, Label: continueLabel})
	more := b.compare("icmp", "ult", byteIndex, length)
	b.condBranch(more, bodyLabel, doneLabel)
	b.namedLabel(bodyLabel)
	next, codepoint := emitUTF8DecodeStep(b, data, byteIndex, length)
	b.store(b.gep(b.pointerPlace(charData), charIndex, false), codepoint)
	b.branch(continueLabel)
	b.namedLabel(continueLabel)
	b.defineArithmetic(nextByteIndex, "add", next, b.value("0", i64))
	b.defineArithmetic(nextCharIndex, "add", charIndex, b.value("1", i64))
	b.branch(loopLabel)
	b.namedLabel(doneLabel)
	return emitDynamicArrayHeader(b, arrayType, charData, count, count, allocator)
}

func emitUTF8CodepointCount(b *llvmBuilder, data, length llvmValue) llvmValue {
	id := b.nextID
	b.nextID++
	entryLabel := b.currentLabel
	loopLabel := fmt.Sprintf("utf8_count_loop_%d", id)
	bodyLabel := fmt.Sprintf("utf8_count_body_%d", id)
	continueLabel := fmt.Sprintf("utf8_count_continue_%d", id)
	doneLabel := fmt.Sprintf("utf8_count_done_%d", id)
	b.branch(loopLabel)
	b.namedLabel(loopLabel)
	i64 := llvmScalarLayout("i64")
	nextIndex := b.nextValue(i64)
	nextCount := b.nextValue(i64)
	index := b.nextValue(i64)
	count := b.nextValue(i64)
	b.definePhi(index, llvmIncoming{Value: b.value("0", i64), Label: entryLabel}, llvmIncoming{Value: nextIndex, Label: continueLabel})
	b.definePhi(count, llvmIncoming{Value: b.value("0", i64), Label: entryLabel}, llvmIncoming{Value: nextCount, Label: continueLabel})
	more := b.compare("icmp", "ult", index, length)
	b.condBranch(more, bodyLabel, doneLabel)
	b.namedLabel(bodyLabel)
	decodedNext, _ := emitUTF8DecodeStep(b, data, index, length)
	b.defineArithmetic(nextCount, "add", count, b.value("1", i64))
	b.branch(continueLabel)
	b.namedLabel(continueLabel)
	b.defineArithmetic(nextIndex, "add", decodedNext, b.value("0", i64))
	b.branch(loopLabel)
	b.namedLabel(doneLabel)
	return count
}

func emitUTF8DecodeStep(b *llvmBuilder, data, index, length llvmValue) (llvmValue, llvmValue) {
	id := b.nextID
	b.nextID++
	invalidLabel := fmt.Sprintf("utf8_decode_invalid_%d", id)
	asciiLabel := fmt.Sprintf("utf8_decode_ascii_%d", id)
	kindLabel := fmt.Sprintf("utf8_decode_kind_%d", id)
	twoLabel := fmt.Sprintf("utf8_decode_two_%d", id)
	threeOrFourLabel := fmt.Sprintf("utf8_decode_three_or_four_%d", id)
	threeLabel := fmt.Sprintf("utf8_decode_three_%d", id)
	fourLabel := fmt.Sprintf("utf8_decode_four_%d", id)
	mergeLabel := fmt.Sprintf("utf8_decode_merge_%d", id)

	i64 := llvmScalarLayout("i64")
	lead := b.load(b.gep(b.pointerPlace(data), index, false))
	isASCII := b.compare("icmp", "ule", lead, b.value("127", lead.Layout))
	twoLow := b.compare("icmp", "uge", lead, b.value("-62", lead.Layout))
	twoHigh := b.compare("icmp", "ule", lead, b.value("-33", lead.Layout))
	isTwo := b.arithmetic("and", twoLow, twoHigh)
	threeLow := b.compare("icmp", "uge", lead, b.value("-32", lead.Layout))
	threeHigh := b.compare("icmp", "ule", lead, b.value("-17", lead.Layout))
	isThree := b.arithmetic("and", threeLow, threeHigh)
	fourLow := b.compare("icmp", "uge", lead, b.value("-16", lead.Layout))
	fourHigh := b.compare("icmp", "ule", lead, b.value("-12", lead.Layout))
	isFour := b.arithmetic("and", fourLow, fourHigh)
	validTwoThree := b.arithmetic("or", isTwo, isThree)
	validLead := b.arithmetic("or", validTwoThree, isFour)
	b.condBranch(isASCII, asciiLabel, kindLabel)
	b.namedLabel(kindLabel)
	b.condBranch(validLead, kindLabel+"_valid", invalidLabel)
	b.namedLabel(kindLabel + "_valid")
	b.condBranch(isTwo, twoLabel, threeOrFourLabel)
	b.namedLabel(threeOrFourLabel)
	b.condBranch(isThree, threeLabel, fourLabel)
	b.namedLabel(invalidLabel)
	b.trap()

	b.namedLabel(asciiLabel)
	asciiNext := b.arithmetic("add", index, b.value("1", i64))
	asciiRune := emitUTF8ByteI32(b, lead, 127)
	b.branch(mergeLabel)

	b.namedLabel(twoLabel)
	emitUTF8WidthCheck(b, index, length, 2, invalidLabel)
	secondIndex := b.arithmetic("add", index, b.value("1", i64))
	second := emitUTF8ContinuationByte(b, data, secondIndex, invalidLabel)
	twoNext := b.arithmetic("add", index, b.value("2", i64))
	twoRuneLead := emitUTF8ByteI32(b, lead, 31)
	twoRuneSecond := emitUTF8ByteI32(b, second, 63)
	twoShifted := b.arithmetic("shl", twoRuneLead, b.value("6", twoRuneLead.Layout))
	twoRune := b.arithmetic("or", twoShifted, twoRuneSecond)
	twoPred := b.currentLabel
	b.branch(mergeLabel)

	b.namedLabel(threeLabel)
	emitUTF8WidthCheck(b, index, length, 3, invalidLabel)
	threeSecondIndex := b.arithmetic("add", index, b.value("1", i64))
	threeSecond := emitUTF8ContinuationByte(b, data, threeSecondIndex, invalidLabel)
	e0 := b.compare("icmp", "eq", lead, b.value("-32", lead.Layout))
	ed := b.compare("icmp", "eq", lead, b.value("-19", lead.Layout))
	notE0 := b.arithmetic("xor", e0, b.value("true", e0.Layout))
	notED := b.arithmetic("xor", ed, b.value("true", ed.Layout))
	e0OK := b.compare("icmp", "uge", threeSecond, b.value("-96", threeSecond.Layout))
	edOK := b.compare("icmp", "ule", threeSecond, b.value("-97", threeSecond.Layout))
	lowOK := b.arithmetic("or", notE0, e0OK)
	highOK := b.arithmetic("or", notED, edOK)
	threeSecondOK := b.arithmetic("and", lowOK, highOK)
	threeReady := fmt.Sprintf("utf8_decode_three_ready_%d", id)
	b.condBranch(threeSecondOK, threeReady, invalidLabel)
	b.namedLabel(threeReady)
	threeThirdIndex := b.arithmetic("add", index, b.value("2", i64))
	threeThird := emitUTF8ContinuationByte(b, data, threeThirdIndex, invalidLabel)
	threeNext := b.arithmetic("add", index, b.value("3", i64))
	threeLeadRune := emitUTF8ByteI32(b, lead, 15)
	threeSecondRune := emitUTF8ByteI32(b, threeSecond, 63)
	threeThirdRune := emitUTF8ByteI32(b, threeThird, 63)
	threeLeadShift := b.arithmetic("shl", threeLeadRune, b.value("12", threeLeadRune.Layout))
	threeSecondShift := b.arithmetic("shl", threeSecondRune, b.value("6", threeSecondRune.Layout))
	threeFirstCombine := b.arithmetic("or", threeLeadShift, threeSecondShift)
	threeRune := b.arithmetic("or", threeFirstCombine, threeThirdRune)
	threePred := b.currentLabel
	b.branch(mergeLabel)

	b.namedLabel(fourLabel)
	emitUTF8WidthCheck(b, index, length, 4, invalidLabel)
	fourSecondIndex := b.arithmetic("add", index, b.value("1", i64))
	fourSecond := emitUTF8ContinuationByte(b, data, fourSecondIndex, invalidLabel)
	f0 := b.compare("icmp", "eq", lead, b.value("-16", lead.Layout))
	f4 := b.compare("icmp", "eq", lead, b.value("-12", lead.Layout))
	notF0 := b.arithmetic("xor", f0, b.value("true", f0.Layout))
	notF4 := b.arithmetic("xor", f4, b.value("true", f4.Layout))
	f0OK := b.compare("icmp", "uge", fourSecond, b.value("-112", fourSecond.Layout))
	f4OK := b.compare("icmp", "ule", fourSecond, b.value("-113", fourSecond.Layout))
	fourLowOK := b.arithmetic("or", notF0, f0OK)
	fourHighOK := b.arithmetic("or", notF4, f4OK)
	fourSecondOK := b.arithmetic("and", fourLowOK, fourHighOK)
	fourReady := fmt.Sprintf("utf8_decode_four_ready_%d", id)
	b.condBranch(fourSecondOK, fourReady, invalidLabel)
	b.namedLabel(fourReady)
	fourThirdIndex := b.arithmetic("add", index, b.value("2", i64))
	fourThird := emitUTF8ContinuationByte(b, data, fourThirdIndex, invalidLabel)
	fourFourthIndex := b.arithmetic("add", index, b.value("3", i64))
	fourFourth := emitUTF8ContinuationByte(b, data, fourFourthIndex, invalidLabel)
	fourNext := b.arithmetic("add", index, b.value("4", i64))
	fourLeadRune := emitUTF8ByteI32(b, lead, 7)
	fourSecondRune := emitUTF8ByteI32(b, fourSecond, 63)
	fourThirdRune := emitUTF8ByteI32(b, fourThird, 63)
	fourFourthRune := emitUTF8ByteI32(b, fourFourth, 63)
	fourLeadShift := b.arithmetic("shl", fourLeadRune, b.value("18", fourLeadRune.Layout))
	fourSecondShift := b.arithmetic("shl", fourSecondRune, b.value("12", fourSecondRune.Layout))
	fourThirdShift := b.arithmetic("shl", fourThirdRune, b.value("6", fourThirdRune.Layout))
	fourFirstCombine := b.arithmetic("or", fourLeadShift, fourSecondShift)
	fourSecondCombine := b.arithmetic("or", fourFirstCombine, fourThirdShift)
	fourRune := b.arithmetic("or", fourSecondCombine, fourFourthRune)
	fourPred := b.currentLabel
	b.branch(mergeLabel)

	b.namedLabel(mergeLabel)
	next := b.phi(i64, llvmIncoming{Value: asciiNext, Label: asciiLabel}, llvmIncoming{Value: twoNext, Label: twoPred}, llvmIncoming{Value: threeNext, Label: threePred}, llvmIncoming{Value: fourNext, Label: fourPred})
	runeValue := b.phi(llvmScalarLayout("i32"), llvmIncoming{Value: asciiRune, Label: asciiLabel}, llvmIncoming{Value: twoRune, Label: twoPred}, llvmIncoming{Value: threeRune, Label: threePred}, llvmIncoming{Value: fourRune, Label: fourPred})
	return next, runeValue
}

func emitUTF8WidthCheck(b *llvmBuilder, index, length llvmValue, width int, invalidLabel string) {
	remaining := b.arithmetic("sub", length, index)
	enough := b.compare("icmp", "uge", remaining, b.value(strconv.Itoa(width), remaining.Layout))
	id := b.nextID
	b.nextID++
	readyLabel := fmt.Sprintf("utf8_width_ready_%d", id)
	b.condBranch(enough, readyLabel, invalidLabel)
	b.namedLabel(readyLabel)
}

func emitUTF8ContinuationByte(b *llvmBuilder, data, index llvmValue, invalidLabel string) llvmValue {
	value := b.load(b.gep(b.pointerPlace(data), index, false))
	low := b.compare("icmp", "uge", value, b.value("-128", value.Layout))
	high := b.compare("icmp", "ule", value, b.value("-65", value.Layout))
	valid := b.arithmetic("and", low, high)
	id := b.nextID
	b.nextID++
	readyLabel := fmt.Sprintf("utf8_continuation_ready_%d", id)
	b.condBranch(valid, readyLabel, invalidLabel)
	b.namedLabel(readyLabel)
	return value
}

func emitUTF8ByteI32(b *llvmBuilder, value llvmValue, mask int) llvmValue {
	wide := b.cast("zext", value, llvmScalarLayout("i32"))
	return b.arithmetic("and", wide, b.value(strconv.Itoa(mask), wide.Layout))
}

func emitLen(b *llvmBuilder, value mir.ValueRef) llvmValue {
	if b == nil || value == nil {
		return llvmValue{}
	}
	indexLayout := b.emitter.layout(b.emitter.mod.Types.IndexType())
	refType, ok := b.emitter.mod.Types.Type(mirRefType(value))
	if !ok || refType.Kind != ir.TypeReference {
		b.emitter.markInvalid("len requires a reference value")
		return b.value("0", indexLayout)
	}
	target, ok := b.emitter.mod.Types.Type(refType.Elem)
	if !ok {
		b.emitter.markInvalid("len has invalid reference target")
		return b.value("0", indexLayout)
	}
	switch target.Kind {
	case ir.TypeString:
		return b.extractField(emitRef(b, value), llvmFieldLength)
	case ir.TypeArray:
		if target.Length != "" {
			if _, err := strconv.ParseUint(target.Length, 10, 64); err != nil {
				b.emitter.markInvalid("fixed array has invalid length")
				return b.value("0", indexLayout)
			}
			return b.value(target.Length, indexLayout)
		}
		ownerRef := emitRef(b, value)
		return b.extractField(b.load(b.pointerPlace(ownerRef)), llvmFieldLength)
	case ir.TypeSlice:
		return b.extractField(emitRef(b, value), llvmFieldLength)
	default:
		b.emitter.markInvalid("len requires a string or array reference")
		return b.value("0", indexLayout)
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
