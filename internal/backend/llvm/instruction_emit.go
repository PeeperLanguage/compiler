package llvm

import (
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"

	"compiler/internal/diagnostics"
	"compiler/internal/ir"
	"compiler/internal/ir/mir"
	"compiler/internal/problems"
	"compiler/internal/source"
	"compiler/internal/target"
)

type llvmEmitter struct {
	mod             *mir.Module
	diag            *diagnostics.DiagnosticBag
	target          target.Info
	badTypes        map[string]struct{}
	layouts         map[ir.TypeID]*llvmLayout
	layoutBuilding  map[ir.TypeID]bool
	dropHelpers     map[ir.TypeID]string
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
	if !ok || (target.Kind != ir.TypeArray && target.Kind != ir.TypeSlice) {
		return llvmPlace{}, false
	}
	if target.Kind == ir.TypeSlice || target.Length == "" {
		header := base
		// Dynamic-owner references lower as pointers to their carrier header;
		// slice references lower as the carrier aggregate itself.
		if addressed || pointed || referenced && base.Layout.Kind == llvmLayoutPointer {
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
	case mir.PlaceProjectionVariantPayload:
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
		case mir.PlaceProjectionVariantPayload:
			if !hasCurrent {
				b.emitter.markInvalid("variant payload place requires addressable storage")
				return llvmPlace{}, false
			}
			index, ok := current.Pointee.VariantPayloads[projection.Case]
			if !ok {
				b.emitter.markInvalid(fmt.Sprintf("variant case %d has no payload", projection.Case))
				return llvmPlace{}, false
			}
			current = b.fieldPlace(current, index)
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

// emitIntegerDivRem prevents LLVM's undefined integer division cases from
// executing while preserving Peeper's finite-width arithmetic contract.
func emitIntegerDivRem(b *llvmBuilder, op string, typeID ir.TypeID, left, right llvmValue) llvmValue {
	signed, bits, ok := integerInfoID(b.emitter.mod.Types, typeID)
	if !ok {
		b.emitter.markInvalid("integer division lowering requires integral operands")
		return left
	}

	id := b.nextID
	b.nextID++
	failLabel := fmt.Sprintf("divrem_zero_fail_%d", id)
	nonzeroLabel := fmt.Sprintf("divrem_nonzero_%d", id)
	zero := b.value("0", right.Layout)
	b.condBranch(b.compare("icmp", "eq", right, zero), failLabel, nonzeroLabel)
	b.namedLabel(failLabel)
	b.trap()
	b.namedLabel(nonzeroLabel)

	opcode := "udiv"
	if op == "%" {
		opcode = "urem"
	}
	if !signed {
		return b.arithmetic(opcode, left, right)
	}

	opcode = "sdiv"
	minValue := b.value(new(big.Int).Neg(new(big.Int).Lsh(big.NewInt(1), uint(bits-1))).String(), left.Layout)
	overflowValue := minValue
	if op == "%" {
		opcode = "srem"
		overflowValue = b.value("0", left.Layout)
	}

	leftIsMin := b.compare("icmp", "eq", left, minValue)
	rightIsNegativeOne := b.compare("icmp", "eq", right, b.value("-1", right.Layout))
	overflow := b.arithmetic("and", leftIsMin, rightIsNegativeOne)
	overflowLabel := fmt.Sprintf("divrem_overflow_%d", id)
	computeLabel := fmt.Sprintf("divrem_compute_%d", id)
	readyLabel := fmt.Sprintf("divrem_ready_%d", id)
	b.condBranch(overflow, overflowLabel, computeLabel)
	b.namedLabel(overflowLabel)
	b.branch(readyLabel)
	b.namedLabel(computeLabel)
	computed := b.arithmetic(opcode, left, right)
	b.branch(readyLabel)
	b.namedLabel(readyLabel)
	return b.phi(left.Layout,
		llvmIncoming{Value: overflowValue, Label: overflowLabel},
		llvmIncoming{Value: computed, Label: computeLabel},
	)
}

func emitValueExpr(b *llvmBuilder, expr mir.ValueExpr) llvmValue {
	if expr == nil {
		b.invariant("value expression emission requires MIR value")
	}
	return withLLVMLocation(b, expr.SourceLocation(), func() llvmValue {
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
			case "+":
				return arg
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
				b.invariant("unsupported MIR unary operator %q", e.Op)
				return llvmValue{}
			}
		case *mir.Binary:
			left := emitRef(b, e.Left)
			right := emitRef(b, e.Right)
			leftType := mirRefType(e.Left)
			opcode := ""
			switch e.Op {
			case "+":
				if isFloatType(b.emitter.mod.Types, leftType) {
					opcode = "fadd"
				} else {
					opcode = "add"
				}
			case "-":
				if isFloatType(b.emitter.mod.Types, leftType) {
					opcode = "fsub"
				} else {
					opcode = "sub"
				}
			case "*":
				if isFloatType(b.emitter.mod.Types, leftType) {
					opcode = "fmul"
				} else {
					opcode = "mul"
				}
			case "/":
				if isFloatType(b.emitter.mod.Types, leftType) {
					opcode = "fdiv"
				} else {
					return emitIntegerDivRem(b, e.Op, leftType, left, right)
				}
			case "%":
				if isFloatType(b.emitter.mod.Types, leftType) {
					opcode = "frem"
				} else {
					return emitIntegerDivRem(b, e.Op, leftType, left, right)
				}
			case "&":
				opcode = "and"
			case "|":
				opcode = "or"
			case "^":
				opcode = "xor"
			case "<<", ">>":
				_, bits, ok := integerInfoID(b.emitter.mod.Types, leftType)
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
					if isUnsignedTypeID(b.emitter.mod.Types, leftType) {
						opcode = "lshr"
					}
				}
				shiftCount := right
				if mirRefType(e.Right) != mirRefType(e.Left) {
					shiftCount = emitCast(b, &mir.Cast{Arg: e.Right, Type: mirRefType(e.Left), Location: e.Right.SourceLocation()})
				}
				return b.arithmetic(opcode, left, shiftCount)
			case "==", "!=", "<", "<=", ">", ">=":
				if isTypeKind(b.emitter.mod.Types, leftType, ir.TypeString) {
					if e.Op != "==" && e.Op != "!=" {
						b.invariant("unsupported string comparison %q", e.Op)
					}
					equal := emitStringEqual(b, left, right)
					if e.Op == "!=" {
						return b.arithmetic("xor", equal, b.value("true", equal.Layout))
					}
					return equal
				}
				if isFloatType(b.emitter.mod.Types, leftType) {
					pred := map[string]string{"==": "oeq", "!=": "une", "<": "olt", "<=": "ole", ">": "ogt", ">=": "oge"}[e.Op]
					return b.compare("fcmp", pred, left, right)
				}
				return b.compare("icmp", integerComparePredID(b.emitter.mod.Types, e.Op, leftType), left, right)
			case "&&", "||":
				lc := emitCondRef(b, e.Left)
				rc := emitCondRef(b, e.Right)
				if e.Op == "&&" {
					return b.arithmetic("and", lc, rc)
				}
				return b.arithmetic("or", lc, rc)
			default:
				b.invariant("unsupported MIR binary operator %q", e.Op)
				return llvmValue{}
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
		case *mir.Alloc:
			return emitAlloc(b, e)
		case *mir.ZeroValue:
			return b.zero(b.emitter.layout(e.Type))
		case *mir.VariantMake:
			variant, ok := b.emitter.mod.Types.Type(e.Type)
			variantCase, caseOK := variant.VariantCase(e.Case)
			if !ok || variant.Kind != ir.TypeVariant || !caseOK {
				b.emitter.markInvalid("variant construction has invalid type or case")
				return b.value("0", b.emitter.layout(e.Type))
			}
			layout := b.emitter.layout(e.Type)
			value := b.zero(layout)
			tagLayout := layout.Elements[layout.VariantTag]
			value = b.insertIndex(value, b.variantCaseTag(e.Case, tagLayout), layout.VariantTag)
			if variantCase.Payload == ir.InvalidType {
				if e.Payload != nil {
					b.emitter.markInvalid("payloadless variant case has payload")
				}
				return value
			}
			if e.Payload == nil {
				b.emitter.markInvalid("variant data case requires payload")
				return value
			}
			return b.insertVariantPayload(value, emitRef(b, e.Payload), e.Case)
		case *mir.VariantIs:
			value := emitRef(b, e.Value)
			variant, ok := b.emitter.mod.Types.Type(mirRefType(e.Value))
			if _, caseOK := variant.VariantCase(e.Case); !ok || variant.Kind != ir.TypeVariant || !caseOK {
				b.emitter.markInvalid("variant test has invalid type or case")
				return b.value("false", llvmScalarLayout("i1"))
			}
			tag := b.variantTag(value)
			if tag.Layout.Text == "i1" && e.Case == ir.OptionalPresentCase {
				return tag
			}
			return b.compare("icmp", "eq", tag, b.variantCaseTag(e.Case, tag.Layout))
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

func emitRef(b *llvmBuilder, ref mir.ValueRef) llvmValue {
	if ref == nil {
		b.invariant("reference emission requires MIR value")
	}
	return withLLVMLocation(b, ref.SourceLocation(), func() llvmValue {
		refType := mirRefType(ref)
		layout := b.emitter.layout(refType)
		if layout == nil {
			b.invariant("reference has unsupported type %s", b.emitter.mod.Types.Text(refType))
		}
		typ, ok := b.emitter.mod.Types.Type(refType)
		if !ok {
			if refType == ir.InvalidType {
				return b.value("0", layout)
			}
			b.invariant("reference has unknown type %d", refType)
		}
		switch v := ref.(type) {
		case *mir.RefConst:
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
				if localEntry.Constant == nil {
					arrayType := fmt.Sprintf("[%d x i8]", len(localEntry.Bytes)+1)
					return b.value(fmt.Sprintf("getelementptr inbounds (%s, %s* %s, i64 0, i64 0)", arrayType, arrayType, localEntry.Name), layout)
				}
				staticLayout := b.emitter.layout(localEntry.Type)
				return b.load(b.place(localEntry.Name, staticLayout))
			}

			if idx := strings.IndexByte(v.Name, '$'); idx >= 0 {
				name := "@" + v.Name
				if b.emitter.externalGlobals == nil {
					b.emitter.externalGlobals = make(map[string]ir.TypeID)
				}
				b.emitter.externalGlobals[name] = v.Type

				return b.load(b.place(name, layout))
			}

			if strings.HasPrefix(v.Name, "@") {
				return b.value(v.Name, layout)
			}
			if refType == ir.InvalidType {
				return b.value("0", layout)
			}
			b.invariant("unresolved MIR reference %q", v.Name)
			return llvmValue{}
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
	if ref == nil {
		b.invariant("condition emission requires MIR value")
	}
	return withLLVMLocation(b, ref.SourceLocation(), func() llvmValue {
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
