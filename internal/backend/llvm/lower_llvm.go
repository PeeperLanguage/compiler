package llvm

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/token"
	"compiler/internal/ir"
	"compiler/internal/ir/mir"
	"compiler/internal/problems"
	"compiler/internal/source"
)

type llvmEmitter struct {
	mod             *mir.Module
	diag            *diagnostics.DiagnosticBag
	badTypes        map[string]struct{}
	invalid         bool
	externalGlobals map[string]string
	debug           *llvmDebugEmitter
}

func emitStore(b *llvmBuilder, store *mir.Store) {
	if b == nil || store == nil || store.Ptr == nil || store.Value == nil {
		return
	}
	ptr := emitRef(b, store.Ptr)
	value := emitRef(b, store.Value)
	valueType := b.emitter.llvmType(mirRefType(store.Value))
	b.line(fmt.Sprintf("store %s %s, %s* %s", valueType, value, valueType, ptr))
}

func emitFieldPtr(b *llvmBuilder, baseRef mir.ValueRef, index int) string {
	if b == nil || baseRef == nil {
		return ""
	}
	base := emitRef(b, baseRef)
	baseType := mirRefType(baseRef)
	llvmPtrType, ok := llvmTypeName(baseType)
	if !ok {
		return ""
	}
	structTypeText, ok := pointerTypeTextTarget(baseType)
	if !ok {
		structTypeText, ok = referenceTypeTextTarget(baseType)
	}
	if !ok {
		return ""
	}
	llvmStructType, ok := llvmTypeName(structTypeText)
	if !ok {
		return ""
	}
	ptr := b.nextReg()
	b.line(fmt.Sprintf("%s = getelementptr inbounds %s, %s %s, i32 0, i32 %d", ptr, llvmStructType, llvmPtrType, base, index))
	return ptr
}

func emitIndexPtr(b *llvmBuilder, baseRef mir.ValueRef, indexRef mir.ValueRef) string {
	if b == nil || baseRef == nil || indexRef == nil {
		return ""
	}
	baseType := mirRefType(baseRef)
	targetType, pointed := pointerTypeTextTarget(baseType)
	if !pointed {
		targetType = baseType
	}
	if strings.HasPrefix(targetType, "[]") {
		arrayType, ok := llvmTypeName(targetType)
		if !ok {
			return ""
		}
		elemType, ok := llvmTypeName(strings.TrimSpace(strings.TrimPrefix(targetType, "[]")))
		if !ok {
			return ""
		}
		base := emitRef(b, baseRef)
		if pointed {
			loaded := b.nextReg()
			b.line(fmt.Sprintf("%s = load %s, %s* %s", loaded, arrayType, arrayType, base))
			base = loaded
		}
		data := b.nextReg()
		b.line(fmt.Sprintf("%s = extractvalue %s %s, 0", data, arrayType, base))
		length := b.nextReg()
		b.line(fmt.Sprintf("%s = extractvalue %s %s, 1", length, arrayType, base))
		index := emitRef(b, indexRef)
		indexTypeText := mirRefType(indexRef)
		_, indexBits, ok := token.ParseIntegerBuiltin(indexTypeText)
		if !ok {
			b.emitter.markInvalid("dynamic array index lowering requires integral index")
			return ""
		}
		indexType := b.emitter.llvmType(indexTypeText)
		compareIndex := index
		compareLength := length
		compareType := indexType
		if indexBits < 64 {
			compareIndex = emitCast(b, &mir.Cast{Arg: indexRef, Type: "u64"})
			compareType = "i64"
		} else if indexBits > 64 {
			compareLength = b.nextReg()
			b.line(fmt.Sprintf("%s = zext i64 %s to %s", compareLength, length, indexType))
		}
		outOfBounds := b.nextReg()
		// Unsigned comparison also rejects negative signed indexes after sign extension.
		b.line(fmt.Sprintf("%s = icmp uge %s %s, %s", outOfBounds, compareType, compareIndex, compareLength))
		boundsID := b.nextID
		b.nextID++
		failLabel := fmt.Sprintf("bounds_fail_%d", boundsID)
		okLabel := fmt.Sprintf("bounds_ok_%d", boundsID)
		b.line(fmt.Sprintf("br i1 %s, label %%%s, label %%%s", outOfBounds, failLabel, okLabel))
		fmt.Fprintf(b.out, "%s:\n", failLabel)
		b.line("call void @llvm.trap()")
		b.line("unreachable")
		fmt.Fprintf(b.out, "%s:\n", okLabel)
		ptr := b.nextReg()
		b.line(fmt.Sprintf("%s = getelementptr %s, %s* %s, %s %s", ptr, elemType, elemType, data, compareType, compareIndex))
		return ptr
	}
	if _, ok := indexRef.(*mir.RefConst); !ok {
		b.emitter.markInvalid("dynamic array index lowering requires bounds policy")
		return ""
	}
	lengthText, _, ok := ir.ArrayTypeParts(targetType)
	if !ok {
		return ""
	}
	length, lengthErr := strconv.Atoi(lengthText)
	indexConst := indexRef.(*mir.RefConst)
	indexValue, indexErr := strconv.Atoi(indexConst.Value)
	if lengthErr != nil || indexErr != nil || indexValue < 0 || indexValue >= length {
		b.emitter.invalid = true
		if b.emitter.diag != nil {
			b.emitter.diag.Add(problems.ArrayIndexOutOfBounds(indexConst.Value, lengthText, nil))
		}
		return ""
	}
	arrayType, ok := llvmTypeName(targetType)
	if !ok {
		return ""
	}
	basePtr := ""
	if pointed {
		basePtr = emitRef(b, baseRef)
	} else if ref, ok := baseRef.(*mir.RefName); ok && ref != nil {
		basePtr = ensureLocalAddr(b, ref)
	}
	if basePtr == "" {
		baseValue := emitRef(b, baseRef)
		basePtr = b.nextReg()
		b.line(fmt.Sprintf("%s = alloca %s", basePtr, arrayType))
		b.line(fmt.Sprintf("store %s %s, %s* %s", arrayType, baseValue, arrayType, basePtr))
	}
	index := emitRef(b, indexRef)
	indexType := b.emitter.llvmType(mirRefType(indexRef))
	ptr := b.nextReg()
	b.line(fmt.Sprintf("%s = getelementptr inbounds %s, %s* %s, i32 0, %s %s", ptr, arrayType, arrayType, basePtr, indexType, index))
	return ptr
}

func emitInterfaceBoxedData(b *llvmBuilder, value mir.ValueRef, dataType string, stackBox bool) string {
	if b == nil || value == nil {
		return "null"
	}
	llvmType := b.emitter.llvmType(dataType)
	if stackBox {
		typed := b.nextReg()
		b.line(fmt.Sprintf("%s = alloca %s", typed, llvmType))
		val := emitRef(b, value)
		b.line(fmt.Sprintf("store %s %s, %s* %s", llvmType, val, llvmType, typed))
		cast := b.nextReg()
		b.line(fmt.Sprintf("%s = bitcast %s* %s to i8*", cast, llvmType, typed))
		return cast
	}
	sizePtr := b.nextReg()
	b.line(fmt.Sprintf("%s = getelementptr %s, %s* null, i32 1", sizePtr, llvmType, llvmType))
	size := b.nextReg()
	b.line(fmt.Sprintf("%s = ptrtoint %s* %s to i64", size, llvmType, sizePtr))
	mem := b.nextReg()
	b.line(fmt.Sprintf("%s = call i8* @malloc(i64 %s)", mem, size))
	typed := b.nextReg()
	b.line(fmt.Sprintf("%s = bitcast i8* %s to %s*", typed, mem, llvmType))
	val := emitRef(b, value)
	b.line(fmt.Sprintf("store %s %s, %s* %s", llvmType, val, llvmType, typed))
	return mem
}

type llvmBuilder struct {
	out             *strings.Builder
	nextID          int
	locals          map[string]string
	localPtrs       map[string]string
	localTypes      map[string]string
	emitter         *llvmEmitter
	debug           *llvmDebugEmitter
	debugScopeID    int
	debugLocationID int
}

func emitCast(b *llvmBuilder, cast *mir.Cast) string {
	if b == nil || cast == nil || cast.Arg == nil {
		return "0"
	}

	argRef := emitRef(b, cast.Arg)
	fromType := mirRefType(cast.Arg)
	toType := cast.Type

	if fromType == toType {
		return argRef
	}

	if toType == "bool" {
		out := b.nextReg()
		if isMIRFloatType(fromType) {
			fromLLVM := b.emitter.llvmType(fromType)
			b.line(fmt.Sprintf("%s = fcmp one %s %s, 0.0", out, fromLLVM, argRef))
			return out
		}
		if _, _, ok := token.ParseIntegerBuiltin(fromType); ok {
			fromLLVM := b.emitter.llvmType(fromType)
			b.line(fmt.Sprintf("%s = icmp ne %s %s, 0", out, fromLLVM, argRef))
			return out
		}
		return argRef
	}

	if toSigned, _, ok := token.ParseIntegerBuiltin(toType); isMIRFloatType(fromType) && ok {
		out := b.nextReg()
		fromLLVM := b.emitter.llvmType(fromType)
		toLLVM := b.emitter.llvmType(toType)
		if toSigned {
			b.line(fmt.Sprintf("%s = fptosi %s %s to %s", out, fromLLVM, argRef, toLLVM))
		} else {
			b.line(fmt.Sprintf("%s = fptoui %s %s to %s", out, fromLLVM, argRef, toLLVM))
		}
		return out
	} else if fromSigned, _, ok := token.ParseIntegerBuiltin(fromType); ok && isMIRFloatType(toType) {
		out := b.nextReg()
		fromLLVM := b.emitter.llvmType(fromType)
		toLLVM := b.emitter.llvmType(toType)
		if fromSigned {
			b.line(fmt.Sprintf("%s = sitofp %s %s to %s", out, fromLLVM, argRef, toLLVM))
		} else {
			b.line(fmt.Sprintf("%s = uitofp %s %s to %s", out, fromLLVM, argRef, toLLVM))
		}
		return out
	} else if isMIRFloatType(fromType) && isMIRFloatType(toType) {
		if fromType == "f64" && toType == "f32" {
			out := b.nextReg()
			b.line(fmt.Sprintf("%s = fptrunc double %s to float", out, argRef))
			return out
		} else if fromType == "f32" && toType == "f64" {
			out := b.nextReg()
			b.line(fmt.Sprintf("%s = fpext float %s to double", out, argRef))
			return out
		}
		return argRef
	} else if fromSigned, fromBits, ok := token.ParseIntegerBuiltin(fromType); ok {
		_, toBits, ok := token.ParseIntegerBuiltin(toType)
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
		localTypes:      make(map[string]string),
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
	fmt.Fprintf(b.out, "b%d:\n", id)
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
		case *mir.Cast:
			return emitCast(b, e)
		case *mir.Unary:
			arg := emitRef(b, e.Arg)
			typ := b.emitter.llvmType(e.Type)
			switch e.Op {
			case "-":
				out := b.nextReg()
				if isLLVMFloatType(typ) {
					b.line(fmt.Sprintf("%s = fsub %s 0.0, %s", out, typ, arg))
				} else {
					b.line(fmt.Sprintf("%s = sub %s 0, %s", out, typ, arg))
				}
				return out
			case "!":
				return emitLogicalNot(b, arg, e.Arg)
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
				if isLLVMFloatType(leftType) {
					b.line(fmt.Sprintf("%s = fadd %s %s, %s", out, leftType, left, right))
				} else {
					b.line(fmt.Sprintf("%s = add %s %s, %s", out, leftType, left, right))
				}
			case "-":
				if isLLVMFloatType(leftType) {
					b.line(fmt.Sprintf("%s = fsub %s %s, %s", out, leftType, left, right))
				} else {
					b.line(fmt.Sprintf("%s = sub %s %s, %s", out, leftType, left, right))
				}
			case "*":
				if isLLVMFloatType(leftType) {
					b.line(fmt.Sprintf("%s = fmul %s %s, %s", out, leftType, left, right))
				} else {
					b.line(fmt.Sprintf("%s = mul %s %s, %s", out, leftType, left, right))
				}
			case "/":
				if isLLVMFloatType(leftType) {
					b.line(fmt.Sprintf("%s = fdiv %s %s, %s", out, leftType, left, right))
				} else if isUnsignedMIRType(mirRefType(e.Left)) {
					b.line(fmt.Sprintf("%s = udiv %s %s, %s", out, leftType, left, right))
				} else {
					b.line(fmt.Sprintf("%s = sdiv %s %s, %s", out, leftType, left, right))
				}
			case "%":
				if isLLVMFloatType(leftType) {
					b.line(fmt.Sprintf("%s = frem %s %s, %s", out, leftType, left, right))
				} else if isUnsignedMIRType(mirRefType(e.Left)) {
					b.line(fmt.Sprintf("%s = urem %s %s, %s", out, leftType, left, right))
				} else {
					b.line(fmt.Sprintf("%s = srem %s %s, %s", out, leftType, left, right))
				}
			case "==", "!=", "<", "<=", ">", ">=":
				if result, ok := emitOptionalNoneCompare(b, e.Op, e.Left, e.Right, left, right); ok {
					return result
				}
				cmp := b.nextReg()
				if isLLVMFloatType(leftType) {
					pred := map[string]string{"==": "oeq", "!=": "one", "<": "olt", "<=": "ole", ">": "ogt", ">=": "oge"}[e.Op]
					b.line(fmt.Sprintf("%s = fcmp %s %s %s, %s", cmp, pred, leftType, left, right))
				} else {
					pred := integerComparePred(e.Op, mirRefType(e.Left))
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
			if ref, ok := e.Base.(*mir.RefName); ok && ref != nil {
				if ptr := ensureLocalAddr(b, ref); ptr != "" {
					return ptr
				}
			}
			baseType := mirRefType(e.Base)
			llvmBaseType := b.emitter.llvmType(baseType)
			ptr := b.nextReg()
			b.line(fmt.Sprintf("%s = alloca %s", ptr, llvmBaseType))
			value := emitRef(b, e.Base)
			b.line(fmt.Sprintf("store %s %s, %s* %s", llvmBaseType, value, llvmBaseType, ptr))
			return ptr
		case *mir.SliceView:
			sourceTypeText := mirRefType(e.Source)
			elemTypeText, dynamicArray := strings.CutPrefix(sourceTypeText, "[]")
			if !dynamicArray {
				b.emitter.markInvalid("slice view source shape is not lowerable in current compiler stage")
				return "0"
			}
			sourceType := b.emitter.llvmType(sourceTypeText)
			viewType := b.emitter.llvmType(e.Type)
			elemType := b.emitter.llvmType(strings.TrimSpace(elemTypeText))
			source := emitRef(b, e.Source)
			data := b.nextReg()
			b.line(fmt.Sprintf("%s = extractvalue %s %s, 0", data, sourceType, source))
			length := b.nextReg()
			b.line(fmt.Sprintf("%s = extractvalue %s %s, 1", length, sourceType, source))
			withData := b.nextReg()
			b.line(fmt.Sprintf("%s = insertvalue %s zeroinitializer, %s* %s, 0", withData, viewType, elemType, data))
			withLength := b.nextReg()
			b.line(fmt.Sprintf("%s = insertvalue %s %s, i64 %s, 1", withLength, viewType, withData, length))
			return withLength
		case *mir.Load:
			ptr := emitRef(b, e.Ptr)
			llvmType := b.emitter.llvmType(e.Type)
			out := b.nextReg()
			b.line(fmt.Sprintf("%s = load %s, %s* %s", out, llvmType, llvmType, ptr))
			return out
		case *mir.ProjectField:
			if ptr := emitFieldPtr(b, e.Base, e.Index); ptr != "" {
				return ptr
			}
			return "0"
		case *mir.ProjectIndex:
			if ptr := emitIndexPtr(b, e.Base, e.Index); ptr != "" {
				return ptr
			}
			return "0"
		case *mir.Field:
			if e.ThroughPtr {
				ptr := emitFieldPtr(b, e.Base, e.Index)
				if ptr == "" {
					return "0"
				}
				out := b.nextReg()
				llvmFieldType := b.emitter.llvmType(e.Type)
				b.line(fmt.Sprintf("%s = load %s, %s* %s", out, llvmFieldType, llvmFieldType, ptr))
				return out
			}
			base := emitRef(b, e.Base)
			baseType := mirRefType(e.Base)
			llvmBaseType, ok := llvmTypeName(baseType)
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
		case *mir.ZeroValue:
			if innerTypeText, ok := optionalInnerTypeText(e.Type); ok {
				if niche, ok := optionalNicheLayout(innerTypeText); ok {
					return niche.none
				}
			}
			return "zeroinitializer"
		case *mir.OptionalSome:
			innerTypeText, ok := optionalInnerTypeText(e.Type)
			if !ok {
				return "0"
			}
			value := emitRef(b, e.Value)
			if _, ok := optionalNicheLayout(innerTypeText); ok {
				return value
			}
			llvmType := b.emitter.llvmType(e.Type)
			valueType := b.emitter.llvmType(mirRefType(e.Value))
			withTag := b.nextReg()
			b.line(fmt.Sprintf("%s = insertvalue %s zeroinitializer, i1 true, 0", withTag, llvmType))
			withValue := b.nextReg()
			b.line(fmt.Sprintf("%s = insertvalue %s %s, %s %s, 1", withValue, llvmType, withTag, valueType, value))
			return withValue
		case *mir.InterfaceMake:
			llvmType := b.emitter.llvmType(e.Type)
			dataPtr := "null"
			if e.BoxValue {
				dataPtr = emitInterfaceBoxedData(b, e.Value, e.DataType, e.StackBox)
			} else {
				value := emitRef(b, e.Value)
				valueType := b.emitter.llvmType(mirRefType(e.Value))
				cast := b.nextReg()
				b.line(fmt.Sprintf("%s = bitcast %s %s to i8*", cast, valueType, value))
				dataPtr = cast
			}
			itabPtr := "null"
			if len(e.Slots) > 0 {
				itabSym := itabSymbolName(e.Type, e.DataType)
				itabCast := b.nextReg()
				b.line(fmt.Sprintf("%s = bitcast [%d x i8*]* %s to i8*", itabCast, len(e.Slots), itabSym))
				itabPtr = itabCast
			}
			current := "zeroinitializer"
			reg1 := b.nextReg()
			b.line(fmt.Sprintf("%s = insertvalue %s %s, i8* %s, 0", reg1, llvmType, current, dataPtr))
			reg2 := b.nextReg()
			b.line(fmt.Sprintf("%s = insertvalue %s %s, i8* %s, 1", reg2, llvmType, reg1, itabPtr))
			return reg2
		case *mir.InterfaceCall:
			data, fn, ok := emitInterfaceCallTarget(b, e.Base, e.Slot)
			if !ok {
				return "0"
			}
			out := b.nextReg()
			args := append([]string{"i8* " + data}, llvmCallArgs(b, e.Args)...)
			emitCall(b, out, b.emitter.llvmType(e.Type), fn, args)
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
	leftInner, leftOptional := optionalInnerTypeText(mirRefType(leftRef))
	rightInner, rightOptional := optionalInnerTypeText(mirRefType(rightRef))
	if !leftOptional && !rightOptional {
		return "", false
	}
	if leftOptional {
		if _, niche := optionalNicheLayout(leftInner); niche {
			return "", false
		}
	}
	if rightOptional {
		if _, niche := optionalNicheLayout(rightInner); niche {
			return "", false
		}
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
			if v.Type == "bool" {
				if v.Value == "0" {
					return "false"
				}
				return "true"
			}
			if v.Type == "f32" {
				return llvmFloat32Const(v.Value)
			}
			if v.Type == "cstr" {
				return "null"
			}
			return v.Value
		case *mir.RefName:
			isFunc := strings.HasPrefix(v.Type, "fn(") || strings.Contains(v.Type, "->")
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
				if strings.HasPrefix(localEntry.Type, "[") && strings.HasSuffix(localEntry.Type, " x i8]") {
					return fmt.Sprintf("getelementptr inbounds (%s, %s* %s, i64 0, i64 0)", localEntry.Type, localEntry.Type, localEntry.Name)
				}
				reg := b.nextReg()
				llvmType := b.emitter.llvmType(localEntry.Type)
				b.line(fmt.Sprintf("%s = load %s, %s* %s, align %d", reg, llvmType, llvmType, localEntry.Name, localEntry.Align))
				return reg
			}

			if idx := strings.IndexByte(v.Name, '$'); idx >= 0 {
				name := "@" + v.Name
				if b.emitter.externalGlobals == nil {
					b.emitter.externalGlobals = make(map[string]string)
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
	if typeText == "" {
		typeText = ref.Type
	}
	if typeText == "" {
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

func llvmFloat32Const(value string) string {
	parsed, err := strconv.ParseFloat(value, 32)
	if err != nil {
		return value
	}
	return fmt.Sprintf("0x%016X", math.Float64bits(float64(float32(parsed))))
}

func emitCondRef(b *llvmBuilder, ref mir.ValueRef) string {
	return b.withLocation(mir.ValueRefLocation(ref), func() string {
		val := emitRef(b, ref)
		refType := mirRefType(ref)
		if refType == "bool" {
			return val
		}
		if b != nil && b.emitter != nil {
			b.emitter.markInvalid("non-bool condition reached llvm lowering: " + refType)
		}
		return "false"
	})
}

func mirRefType(ref mir.ValueRef) string {
	switch v := ref.(type) {
	case *mir.RefConst:
		return v.Type
	case *mir.RefName:
		return v.Type
	default:
		return "i32"
	}
}

func emitLogicalNot(b *llvmBuilder, arg string, ref mir.ValueRef) string {
	if mirRefType(ref) == "bool" {
		out := b.nextReg()
		b.line(fmt.Sprintf("%s = xor i1 %s, true", out, arg))
		return out
	}
	cmp := emitCondRef(b, ref)
	out := b.nextReg()
	b.line(fmt.Sprintf("%s = xor i1 %s, true", out, cmp))
	return out
}

func integerComparePred(op string, typeText string) string {
	unsigned := isUnsignedMIRType(typeText)
	switch op {
	case "==":
		return "eq"
	case "!=":
		return "ne"
	case "<":
		if unsigned {
			return "ult"
		}
		return "slt"
	case "<=":
		if unsigned {
			return "ule"
		}
		return "sle"
	case ">":
		if unsigned {
			return "ugt"
		}
		return "sgt"
	case ">=":
		if unsigned {
			return "uge"
		}
		return "sge"
	default:
		return "eq"
	}
}

func isUnsignedMIRType(typeText string) bool {
	signed, _, ok := token.ParseIntegerBuiltin(typeText)
	return ok && !signed
}

func isLLVMFloatType(typeText string) bool {
	return typeText == "float" || typeText == "double"
}

func llvmEscapeString(s string) string {
	var sb strings.Builder
	for i := 0; i < len(s); i++ {
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
	ReturnType string
	Params     []string
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
	params := make([]string, 0, len(call.Args))
	for _, arg := range call.Args {
		params = append(params, mirRefType(arg))
	}
	decls[name] = callDecl{Name: name, ReturnType: call.Type, Params: params}
}
