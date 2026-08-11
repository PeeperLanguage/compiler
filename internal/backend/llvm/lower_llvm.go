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
	layouts         map[ir.TypeID]*llvmLayout
	invalid         bool
	externalGlobals map[string]ir.TypeID
	debug           *llvmDebugEmitter
}

func emitStore(b *llvmBuilder, store *mir.Store) {
	if b == nil || store == nil || store.Place == nil || store.Value == nil {
		return
	}
	ptr, ok := emitPlacePtr(b, store.Place)
	if !ok {
		return
	}
	b.store(ptr, emitRef(b, store.Value))
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
	arguments := make([]llvmValue, 0, 2)
	i8 := llvmScalarLayout("i8")
	i8Pointer := llvmPointerLayout(i8)
	switch {
	case typ.Kind == ir.TypeBool:
		trueText := "getelementptr inbounds ([5 x i8], [5 x i8]* @.print.true, i32 0, i32 0)"
		falseText := "getelementptr inbounds ([6 x i8], [6 x i8]* @.print.false, i32 0, i32 0)"
		selected := b.selectValue(value, b.value(trueText, i8Pointer), b.value(falseText, i8Pointer))
		formatName, formatSize, arguments = "string", 3, []llvmValue{selected}
	case typ.Kind == ir.TypeCStr:
		formatName, formatSize, arguments = "string", 3, []llvmValue{value}
	case typ.Kind == ir.TypeString:
		data, length := emitStringDataAndLength(b, value)
		precision := length
		switch length.Layout.Text {
		case "i32":
		case "i64":
			precision = b.cast("trunc", length, llvmScalarLayout("i32"))
		default:
			b.emitter.markInvalid("print reached LLVM with unsupported string length type " + length.Layout.Text)
			return
		}
		formatName, formatSize, arguments = "str", 5, []llvmValue{precision, data}
	case typ.Kind == ir.TypeRawPtr:
		formatName, formatSize, arguments = "pointer", 3, []llvmValue{value}
	case typ.Kind == ir.TypeFloat:
		if typ.Bits == 32 {
			f64 := b.emitter.mod.Types.Intern(ir.Type{Kind: ir.TypeFloat, Bits: 64})
			value = emitCast(b, &mir.Cast{Arg: printInstr.Value, Type: f64})
		}
		formatName, formatSize, arguments = "float", 3, []llvmValue{value}
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
		formatSize, arguments = 5, []llvmValue{value}
	}
	formatText := fmt.Sprintf("getelementptr inbounds ([%d x i8], [%d x i8]* @.print.%s, i32 0, i32 0)", formatSize, formatSize, formatName)
	printf := b.value("@printf", llvmFunctionLayout(llvmScalarLayout("i32"), []*llvmLayout{i8Pointer}))
	b.variadicCall(printf, []llvmValue{b.value(formatText, i8Pointer)}, arguments)
	if printInstr.Newline {
		newline := b.value("getelementptr inbounds ([2 x i8], [2 x i8]* @.print.newline, i32 0, i32 0)", i8Pointer)
		b.variadicCall(printf, []llvmValue{newline}, nil)
	}
}

// emitTargetIndexAsI64 widens a target-sized length before it reaches lowering
// paths whose arithmetic and comparisons are intentionally i64.
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
	if !ok || targetType.Kind != ir.TypeArray {
		b.emitter.markInvalid("slice view source shape is not lowerable in current compiler stage")
		return b.zero(resultLayout)
	}
	var data, length llvmValue
	var fixedArrayPlace llvmPlace
	if targetType.Length == "" {
		var source llvmValue
		if sliceViewUsesPlacePtr(b.emitter.mod.Types, view.Source) {
			ptr, ok := emitPlacePtr(b, view.Source)
			if !ok {
				return b.zero(resultLayout)
			}
			source = b.load(ptr)
		} else {
			source = emitRef(b, view.Source.Root)
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
	missing := b.compare("icmp", "eq", raw, b.value("null", raw.Layout))
	id := b.nextID
	b.nextID++
	failLabel := fmt.Sprintf("array_alloc_fail_%d", id)
	readyLabel := fmt.Sprintf("array_alloc_ready_%d", id)
	b.condBranch(missing, failLabel, readyLabel)
	b.namedLabel(failLabel)
	b.trap()
	b.namedLabel(readyLabel)
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
	isNull := b.compare("icmp", "eq", raw, b.value("null", raw.Layout))
	id := b.nextID
	b.nextID++
	failLabel := fmt.Sprintf("alloc_fail_%d", id)
	doneLabel := fmt.Sprintf("alloc_done_%d", id)
	b.condBranch(isNull, failLabel, doneLabel)
	b.namedLabel(failLabel)
	b.trap()
	b.namedLabel(doneLabel)

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

func emitDynamicArrayOp(b *llvmBuilder, op *mir.DynamicArrayOp) llvmValue {
	if b == nil || op == nil || op.Array == nil {
		return llvmValue{}
	}
	elemTypeID, ok := dynamicArrayElementType(b.emitter.mod.Types, op.Type)
	if !ok {
		b.emitter.markInvalid("dynamic array operation has invalid type")
		return b.zero(b.emitter.layout(op.Type))
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
		return emitDynamicArrayAppend(b, op, array)
	case symbols.CompilerOpResize:
		return emitDynamicArrayResize(b, op, array)
	case symbols.CompilerOpShrink:
		return emitDynamicArrayShrink(b, op, array, elemTypeID)
	default:
		b.emitter.markInvalid("unknown dynamic array operation " + string(op.Op))
		return array
	}
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
	shrunk := emitDynamicArrayHeader(b, op.Type, data, newLength, capacity, allocator)
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
	reserved := emitDynamicArrayReserve(b, array, op.Type, desiredCapacity)
	data := b.extractField(reserved, llvmFieldData)
	finalCapacity := b.extractField(reserved, llvmFieldCapacity)
	b.store(b.gep(b.pointerPlace(data), length, false), emitRef(b, op.Value))
	allocator := b.extractField(reserved, llvmFieldAllocator)
	return emitDynamicArrayHeader(b, op.Type, data, newLength, finalCapacity, allocator)
}

func emitDynamicArrayResize(b *llvmBuilder, op *mir.DynamicArrayOp, array llvmValue) llvmValue {
	if op.Length == nil || op.Value == nil {
		b.emitter.markInvalid("resize requires a length and fill value")
		return array
	}
	oldLength := b.extractField(array, llvmFieldLength)
	newLength := emitCast(b, &mir.Cast{Arg: op.Length, Type: b.emitter.mod.Types.IndexType()})
	resized := emitDynamicArrayReserve(b, array, op.Type, newLength)
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
	return emitDynamicArrayHeader(b, op.Type, data, newLength, capacity, allocator)
}

func emitIndexPtr(b *llvmBuilder, base llvmValue, baseType ir.TypeID, addressed bool, indexRef mir.ValueRef) (llvmPlace, bool) {
	if b == nil || base.Layout == nil || baseType == ir.InvalidType || indexRef == nil {
		return llvmPlace{}, false
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
		return llvmPlace{}, false
	}
	if target.Length == "" {
		header := base
		if addressed || pointed {
			header = b.load(b.pointerPlace(base))
		}
		data := b.extractField(header, llvmFieldData)
		length := b.extractField(header, llvmFieldLength)
		index, ok := emitBoundsCheckedIndex(b, indexRef, emitTargetIndexAsI64(b, length))
		if !ok {
			return llvmPlace{}, false
		}
		return b.gep(b.pointerPlace(data), index, false), true
	}
	length, lengthErr := strconv.Atoi(target.Length)
	var index llvmValue
	if indexConst, constant := indexRef.(*mir.RefConst); constant {
		parsedIndex, indexErr := strconv.Atoi(indexConst.Value)
		if lengthErr != nil || indexErr != nil || parsedIndex < 0 || parsedIndex >= length {
			b.emitter.invalid = true
			if b.emitter.diag != nil {
				b.emitter.diag.Add(problems.ArrayIndexOutOfBounds(indexConst.Value, target.Length, nil))
			}
			return llvmPlace{}, false
		}
		index = emitRef(b, indexRef)
	} else {
		if lengthErr != nil {
			return llvmPlace{}, false
		}
		index, ok = emitBoundsCheckedIndex(b, indexRef, b.value(target.Length, llvmScalarLayout("i64")))
		if !ok {
			return llvmPlace{}, false
		}
	}
	if !addressed && !pointed && !referenced {
		b.emitter.markInvalid("fixed-array index place requires addressable storage")
		return llvmPlace{}, false
	}
	arrayPlace := b.pointerPlace(base)
	return b.arrayElement(arrayPlace, index, true), true
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

func emitPlaceRootAddr(b *llvmBuilder, root mir.ValueRef) (llvmPlace, bool) {
	if b == nil || root == nil {
		return llvmPlace{}, false
	}
	if ref, ok := root.(*mir.RefName); ok && ref != nil {
		if ptr, found := ensureLocalAddr(b, ref); found {
			return ptr, true
		}
	}
	value := emitRef(b, root)
	ptr := b.alloca(value.Layout)
	b.store(ptr, value)
	return ptr, true
}

func emitPlacePtr(b *llvmBuilder, place *mir.Place) (llvmPlace, bool) {
	if b == nil || place == nil || place.Root == nil {
		return llvmPlace{}, false
	}
	previousLocation := b.debugLocationID
	defer func() { b.debugLocationID = previousLocation }()

	addressed := placeNeedsRootAddr(b.emitter.mod.Types, place)
	current := llvmPlace{}
	hasCurrent := false
	if addressed {
		current, hasCurrent = emitPlaceRootAddr(b, place.Root)
	}
	currentType := mirRefType(place.Root)
	for _, projection := range place.Projections {
		b.setLocation(projection.Location)
		switch projection.Kind {
		case mir.PlaceProjectionDeref:
			var value llvmValue
			if hasCurrent {
				value = b.load(current)
			} else {
				value = emitRef(b, place.Root)
			}
			if !isOwnedInterfaceType(b.emitter.mod.Types, currentType) {
				if typ, ok := b.emitter.mod.Types.Type(currentType); ok && typ.Kind == ir.TypeOwnedPtr {
					value = b.extractField(value, llvmFieldData)
				}
			}
			current = b.pointerPlace(value)
			hasCurrent = true
			addressed = true
		case mir.PlaceProjectionField:
			if !hasCurrent {
				b.emitter.markInvalid("field place requires addressable storage")
				return llvmPlace{}, false
			}
			current = b.fieldPlace(current, projection.FieldIndex)
		case mir.PlaceProjectionIndex:
			base := emitRef(b, place.Root)
			if hasCurrent {
				base = b.pointerValue(current)
			}
			var ok bool
			current, ok = emitIndexPtr(b, base, currentType, addressed, projection.Index)
			if !ok {
				return llvmPlace{}, false
			}
			hasCurrent = true
			addressed = true
		default:
			b.emitter.markInvalid(fmt.Sprintf("unsupported MIR place projection %d", projection.Kind))
			return llvmPlace{}, false
		}
		currentType = projection.Type
	}
	if addressed && hasCurrent {
		return current, true
	}
	b.setLocation(place.Location)
	return emitPlaceRootAddr(b, place.Root)
}

type llvmBuilder struct {
	out             *strings.Builder
	nextID          int
	locals          map[string]llvmValue
	localPtrs       map[string]llvmPlace
	emitter         *llvmEmitter
	debug           *llvmDebugEmitter
	debugScopeID    int
	debugLocationID int
	currentLabel    string
}

func emitCast(b *llvmBuilder, cast *mir.Cast) llvmValue {
	if b == nil || cast == nil || cast.Arg == nil {
		if b != nil {
			b.invariant("cast requires MIR argument")
		}
		return llvmValue{}
	}

	argRef := emitRef(b, cast.Arg)
	fromType := mirRefType(cast.Arg)
	toType := cast.Type
	from, fromOK := b.emitter.mod.Types.Type(fromType)
	to, toOK := b.emitter.mod.Types.Type(toType)
	if !fromOK || !toOK {
		return argRef
	}
	toLayout := b.emitter.layout(toType)
	if toLayout == nil {
		b.invariant("cast target has no LLVM layout")
	}

	if fromType == toType {
		return argRef
	}
	if to.Kind == ir.TypeBool {
		if from.Kind == ir.TypeFloat {
			return b.compare("fcmp", "one", argRef, b.value("0.0", argRef.Layout))
		}
		if _, _, ok := integerInfoID(b.emitter.mod.Types, fromType); ok {
			return b.compare("icmp", "ne", argRef, b.value("0", argRef.Layout))
		}
		return argRef
	}

	if toSigned, _, ok := integerInfoID(b.emitter.mod.Types, toType); from.Kind == ir.TypeFloat && ok {
		if toSigned {
			return b.cast("fptosi", argRef, toLayout)
		}
		return b.cast("fptoui", argRef, toLayout)
	} else if fromSigned, _, ok := integerInfoID(b.emitter.mod.Types, fromType); ok && to.Kind == ir.TypeFloat {
		if fromSigned {
			return b.cast("sitofp", argRef, toLayout)
		}
		return b.cast("uitofp", argRef, toLayout)
	} else if from.Kind == ir.TypeFloat && to.Kind == ir.TypeFloat {
		if from.Bits == 64 && to.Bits == 32 {
			return b.cast("fptrunc", argRef, toLayout)
		} else if from.Bits == 32 && to.Bits == 64 {
			return b.cast("fpext", argRef, toLayout)
		}
		return argRef
	} else if fromSigned, fromBits, ok := integerInfoID(b.emitter.mod.Types, fromType); ok {
		_, toBits, ok := integerInfoID(b.emitter.mod.Types, toType)
		if !ok {
			return argRef
		}
		if fromBits < toBits {
			if fromSigned {
				return b.cast("sext", argRef, toLayout)
			}
			return b.cast("zext", argRef, toLayout)
		} else if fromBits > toBits {
			return b.cast("trunc", argRef, toLayout)
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
		locals:          make(map[string]llvmValue),
		localPtrs:       make(map[string]llvmPlace),
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

func withLLVMLocation[T any](b *llvmBuilder, loc *source.Location, emit func() T) T {
	if b == nil || emit == nil {
		var zero T
		return zero
	}
	prev := b.debugLocationID
	b.setLocation(loc)
	out := emit()
	b.debugLocationID = prev
	return out
}

func emitValueExpr(b *llvmBuilder, expr mir.ValueExpr) llvmValue {
	return withLLVMLocation(b, mir.ValueExprLocation(expr), func() llvmValue {
		switch e := expr.(type) {
		case *mir.Move:
			return emitRef(b, e.Src)
		case *mir.Len:
			return emitLen(b, e.Value)
		case *mir.StringLiteral:
			layout := b.emitter.layout(e.Type)
			rawPointer := layout.Elements[layout.Fields[llvmFieldData]]
			dataType := fmt.Sprintf("[%d x i8]", e.Length+1)
			data := b.value(fmt.Sprintf("getelementptr inbounds (%s, %s* %s, i64 0, i64 0)", dataType, dataType, e.Name), rawPointer)
			value := b.insertField(b.zero(layout), data, llvmFieldData)
			value = b.insertField(value, b.value(strconv.Itoa(e.Length), layout.Elements[layout.Fields[llvmFieldLength]]), llvmFieldLength)
			return b.insertField(value, b.value("null", rawPointer), llvmFieldAllocator)
		case *mir.Cast:
			return emitCast(b, e)
		case *mir.Unary:
			arg := emitRef(b, e.Arg)
			switch e.Op {
			case "-":
				if isFloatType(b.emitter.mod.Types, e.Type) {
					return b.arithmetic("fsub", b.value("0.0", arg.Layout), arg)
				}
				return b.arithmetic("sub", b.value("0", arg.Layout), arg)
			case "!":
				return emitLogicalNot(b, arg, e.Arg)
			case "~":
				return b.arithmetic("xor", arg, b.value("-1", arg.Layout))
			default:
				return arg
			}
		case *mir.Binary:
			left := emitRef(b, e.Left)
			right := emitRef(b, e.Right)
			opcode := ""
			switch e.Op {
			case "+":
				if isFloatType(b.emitter.mod.Types, mirRefType(e.Left)) {
					opcode = "fadd"
				} else {
					opcode = "add"
				}
			case "-":
				if isFloatType(b.emitter.mod.Types, mirRefType(e.Left)) {
					opcode = "fsub"
				} else {
					opcode = "sub"
				}
			case "*":
				if isFloatType(b.emitter.mod.Types, mirRefType(e.Left)) {
					opcode = "fmul"
				} else {
					opcode = "mul"
				}
			case "/":
				if isFloatType(b.emitter.mod.Types, mirRefType(e.Left)) {
					opcode = "fdiv"
				} else if isUnsignedTypeID(b.emitter.mod.Types, mirRefType(e.Left)) {
					opcode = "udiv"
				} else {
					opcode = "sdiv"
				}
			case "%":
				if isFloatType(b.emitter.mod.Types, mirRefType(e.Left)) {
					opcode = "frem"
				} else if isUnsignedTypeID(b.emitter.mod.Types, mirRefType(e.Left)) {
					opcode = "urem"
				} else {
					opcode = "srem"
				}
			case "&":
				opcode = "and"
			case "|":
				opcode = "or"
			case "^":
				opcode = "xor"
			case "<<", ">>":
				_, bits, ok := integerInfoID(b.emitter.mod.Types, mirRefType(e.Left))
				if !ok {
					b.emitter.markInvalid("shift lowering requires integral operands")
					return left
				}
				invalid := b.compare("icmp", "uge", right, b.value(strconv.Itoa(bits), right.Layout))
				shiftID := b.nextID
				b.nextID++
				failLabel := fmt.Sprintf("shift_fail_%d", shiftID)
				readyLabel := fmt.Sprintf("shift_ready_%d", shiftID)
				b.condBranch(invalid, failLabel, readyLabel)
				b.namedLabel(failLabel)
				b.trap()
				b.namedLabel(readyLabel)
				opcode := "shl"
				if e.Op == ">>" {
					opcode = "ashr"
					if isUnsignedTypeID(b.emitter.mod.Types, mirRefType(e.Left)) {
						opcode = "lshr"
					}
				}
				return b.arithmetic(opcode, left, right)
			case "==", "!=", "<", "<=", ">", ">=":
				if result, ok := emitOptionalNoneCompare(b, e.Op, e.Left, e.Right, left, right); ok {
					return result
				}
				if isFloatType(b.emitter.mod.Types, mirRefType(e.Left)) {
					pred := map[string]string{"==": "oeq", "!=": "one", "<": "olt", "<=": "ole", ">": "ogt", ">=": "oge"}[e.Op]
					return b.compare("fcmp", pred, left, right)
				}
				return b.compare("icmp", integerComparePredID(b.emitter.mod.Types, e.Op, mirRefType(e.Left)), left, right)
			case "&&", "||":
				lc := emitCondRef(b, e.Left)
				rc := emitCondRef(b, e.Right)
				if e.Op == "&&" {
					return b.arithmetic("and", lc, rc)
				}
				return b.arithmetic("or", lc, rc)
			default:
				return left
			}
			return b.arithmetic(opcode, left, right)
		case *mir.Call:
			args := make([]llvmValue, len(e.Args))
			for i, arg := range e.Args {
				args[i] = emitRef(b, arg)
			}
			callee := emitRef(b, e.Callee)
			if callee.Layout.Kind != llvmLayoutFunction {
				b.emitter.markInvalid("call reached LLVM without function type")
				return b.value("0", b.emitter.layout(e.Type))
			}
			return b.call(callee, args)
		case *mir.AddrOf:
			place, ok := emitPlacePtr(b, e.Place)
			if !ok {
				return b.value("0", b.emitter.layout(e.Type))
			}
			pointer := b.pointerValue(place)
			resultType, ok := b.emitter.mod.Types.Type(e.Type)
			if !ok || resultType.Kind != ir.TypeRawPtr {
				return pointer
			}
			if place.Pointee.Text == "i8" {
				return pointer
			}
			return b.bitcast(pointer, b.emitter.layout(e.Type))
		case *mir.SliceView:
			return emitSliceView(b, e)
		case *mir.StringChars:
			return emitStringChars(b, e)
		case *mir.Load:
			ptr, ok := emitPlacePtr(b, e.Place)
			if !ok {
				return b.value("0", b.emitter.layout(e.Type))
			}
			return b.load(ptr)
		case *mir.Field:
			return b.extractIndex(emitRef(b, e.Base), e.Index)
		case *mir.StructLit:
			current := b.zero(b.emitter.layout(e.Type))
			for i, field := range e.Fields {
				current = b.insertIndex(current, emitRef(b, field), i)
			}
			return current
		case *mir.ArrayLit:
			current := b.zero(b.emitter.layout(e.Type))
			for i, item := range e.Values {
				current = b.insertIndex(current, emitRef(b, item), i)
			}
			return current
		case *mir.DynamicArrayAlloc:
			return emitDynamicArrayAlloc(b, e)
		case *mir.DynamicArrayOp:
			return emitDynamicArrayOp(b, e)
		case *mir.Alloc:
			return emitAlloc(b, e)
		case *mir.ZeroValue:
			return b.zero(b.emitter.layout(e.Type))
		case *mir.OptionalSome:
			optional, ok := b.emitter.mod.Types.Type(e.Type)
			if !ok || optional.Kind != ir.TypeOptional {
				return b.value("0", b.emitter.layout(e.Type))
			}
			value := b.insertField(b.zero(b.emitter.layout(e.Type)), b.value("true", llvmScalarLayout("i1")), llvmFieldPresent)
			return b.insertField(value, emitRef(b, e.Value), llvmFieldValue)
		case *mir.InterfaceMake:
			value := emitRef(b, e.Value)
			dataPtr := value
			var allocator llvmValue
			if valueTypeInfo, isOwned := b.emitter.mod.Types.Type(mirRefType(e.Value)); isOwned && valueTypeInfo.Kind == ir.TypeOwnedPtr {
				if !isOwnedInterfaceType(b.emitter.mod.Types, mirRefType(e.Value)) {
					dataPtr = b.extractField(value, llvmFieldData)
					allocator = b.extractField(value, llvmFieldAllocator)
				}
			}
			rawPointer := llvmPointerLayout(llvmScalarLayout("i8"))
			dataBytePtr := b.bitcast(dataPtr, rawPointer)
			itabSym := interfaceSymbolName("itab", b.emitter.mod.Types, e.Type, e.DataType)
			itabPtr := b.value(fmt.Sprintf("bitcast ([%d x i8*]* %s to i8*)", interfaceVtableLength(b.emitter.mod.Types, e.Type, len(e.Slots)), itabSym), rawPointer)
			current := b.insertField(b.zero(b.emitter.layout(e.Type)), dataBytePtr, llvmFieldData)
			current = b.insertField(current, itabPtr, llvmFieldDispatch)
			if allocator.Layout == nil {
				return current
			}
			return b.insertField(current, allocator, llvmFieldAllocator)
		case *mir.InterfaceCall:
			data, fn, ok := emitInterfaceCallTarget(b, e.Base, e.Slot)
			if !ok {
				return b.value("0", b.emitter.layout(e.Type))
			}
			args := make([]llvmValue, 1, len(e.Args)+1)
			args[0] = data
			for _, arg := range e.Args {
				args = append(args, emitRef(b, arg))
			}
			result := b.call(fn, args)
			if consumesOwnedInterfaceStorage(b.emitter.mod.Types, e) {
				emitInterfaceStorageRelease(b, mirRefType(e.Base), emitRef(b, e.Base), data)
			}
			return result
		default:
			b.invariant("unsupported MIR value expression %T", expr)
			return llvmValue{}
		}
	})
}

func emitOptionalNoneCompare(b *llvmBuilder, op string, leftRef, rightRef mir.ValueRef, leftValue, rightValue llvmValue) (llvmValue, bool) {
	if op != "==" && op != "!=" {
		return llvmValue{}, false
	}
	leftType, leftOK := b.emitter.mod.Types.Type(mirRefType(leftRef))
	rightType, rightOK := b.emitter.mod.Types.Type(mirRefType(rightRef))
	leftOptional := leftOK && leftType.Kind == ir.TypeOptional
	rightOptional := rightOK && rightType.Kind == ir.TypeOptional
	if !leftOptional && !rightOptional {
		return llvmValue{}, false
	}
	leftNone := leftValue.Text == "zeroinitializer"
	rightNone := rightValue.Text == "zeroinitializer"
	if leftNone && rightNone {
		if op == "==" {
			return b.value("true", llvmScalarLayout("i1")), true
		}
		return b.value("false", llvmScalarLayout("i1")), true
	}
	var value llvmValue
	if leftNone {
		value = rightValue
	} else if rightNone {
		value = leftValue
	} else {
		if b != nil && b.emitter != nil {
			b.emitter.markInvalid("optional equality currently requires `none` on one side")
		}
		return b.value("false", llvmScalarLayout("i1")), true
	}
	tag := b.extractField(value, llvmFieldPresent)
	pred := "eq"
	if op == "!=" {
		pred = "ne"
	}
	return b.compare("icmp", pred, tag, b.value("false", tag.Layout)), true
}

func emitRef(b *llvmBuilder, ref mir.ValueRef) llvmValue {
	return withLLVMLocation(b, mir.ValueRefLocation(ref), func() llvmValue {
		if ref == nil {
			b.invariant("reference emission requires MIR value")
		}
		layout := b.emitter.layout(mirRefType(ref))
		if layout == nil {
			b.invariant("reference has unsupported type %s", b.emitter.mod.Types.Text(mirRefType(ref)))
		}
		switch v := ref.(type) {
		case *mir.RefConst:
			typ, ok := b.emitter.mod.Types.Type(v.Type)
			if !ok {
				return b.value("0", layout)
			}
			if typ.Kind == ir.TypeBool && v.Value != "false" && v.Value != "true" {
				if b.emitter != nil {
					b.emitter.markInvalid("invalid boolean constant: " + v.Value)
				}
				return b.value("false", layout)
			}
			if typ.Kind == ir.TypeFloat {
				return b.value(llvmFloatConst(v.Value, typ.Bits), layout)
			}
			if typ.Kind == ir.TypeCStr {
				return b.value("null", layout)
			}
			return b.value(v.Value, layout)
		case *mir.RefName:
			typ, _ := b.emitter.mod.Types.Type(v.Type)
			isFunc := typ.Kind == ir.TypeFunction
			if ptr, ok := b.localPtrs[v.Name]; ok {
				return b.load(ptr)
			}
			if reg, ok := b.locals[v.Name]; ok {
				return reg
			}
			if isFunc {
				return b.value("@"+ir.SanitizeSymbolName(ir.StripSymbolInstance(v.Name)), layout)
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
					return b.value(fmt.Sprintf("getelementptr inbounds (%s, %s* %s, i64 0, i64 0)", arrayType, arrayType, localEntry.Name), layout)
				}
				staticLayout := b.emitter.layout(localEntry.Type)
				return b.alignedLoad(b.place(localEntry.Name, staticLayout), localEntry.Align)
			}

			if idx := strings.IndexByte(v.Name, '$'); idx >= 0 {
				name := "@" + v.Name
				if b.emitter.externalGlobals == nil {
					b.emitter.externalGlobals = make(map[string]ir.TypeID)
				}
				b.emitter.externalGlobals[name] = v.Type

				return b.alignedLoad(b.place(name, layout), 4)
			}

			if strings.HasPrefix(v.Name, "@") {
				return b.value(v.Name, layout)
			}
			return b.value("0", layout)
		default:
			b.invariant("unsupported MIR reference %T", ref)
			return llvmValue{}
		}
	})
}

func ensureLocalAddr(b *llvmBuilder, ref *mir.RefName) (llvmPlace, bool) {
	if b == nil || ref == nil {
		return llvmPlace{}, false
	}
	if ptr, ok := b.localPtrs[ref.Name]; ok {
		return ptr, true
	}
	reg, ok := b.locals[ref.Name]
	if !ok {
		return llvmPlace{}, false
	}
	ptr := b.alloca(reg.Layout)
	b.store(ptr, reg)
	b.localPtrs[ref.Name] = ptr
	return ptr, true
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

func emitCondRef(b *llvmBuilder, ref mir.ValueRef) llvmValue {
	return withLLVMLocation(b, mir.ValueRefLocation(ref), func() llvmValue {
		val := emitRef(b, ref)
		refType := mirRefType(ref)
		if typ, ok := b.emitter.mod.Types.Type(refType); ok && typ.Kind == ir.TypeBool {
			return val
		}
		if b != nil && b.emitter != nil {
			b.emitter.markInvalid("non-bool condition reached llvm lowering: " + b.emitter.mod.Types.Text(refType))
		}
		return b.value("false", llvmScalarLayout("i1"))
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

func emitLogicalNot(b *llvmBuilder, arg llvmValue, ref mir.ValueRef) llvmValue {
	if typ, ok := b.emitter.mod.Types.Type(mirRefType(ref)); ok && typ.Kind == ir.TypeBool {
		return b.arithmetic("xor", arg, b.value("true", arg.Layout))
	}
	cmp := emitCondRef(b, ref)
	return b.arithmetic("xor", cmp, b.value("true", cmp.Layout))
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
