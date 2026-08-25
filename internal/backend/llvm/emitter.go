package llvm

import (
	"fmt"
	"strings"

	"compiler/internal/constvalue"
	"compiler/internal/diagnostics"
	"compiler/internal/ir"
	"compiler/internal/ir/mir"
	"compiler/internal/semantics/symbols"
	"compiler/internal/target"
)

// GenerateLLVMIR is backend entrypoint.
// It emits module text in LLVM order: static data, helper itabs, declarations,
// thunks, then function bodies. It also keeps one emitter state object so type
// lowering failures and deferred external globals are reported consistently.
func GenerateLLVMIR(mod *mir.Module, diag *diagnostics.DiagnosticBag, targetInfo target.Info, debugBuild bool) string {
	if mod == nil {
		return ""
	}
	if !targetInfo.Valid() {
		if diag != nil {
			diag.Add(diagnostics.NewError("invalid LLVM target").WithCode(diagnostics.ErrInvalidType))
		}
		return ""
	}
	if !ValidateRuntimeSymbols([]*mir.Module{mod}, diag) {
		return ""
	}

	emitter := &llvmEmitter{
		mod:             mod,
		diag:            diag,
		target:          targetInfo,
		badTypes:        make(map[string]struct{}),
		externalGlobals: make(map[string]ir.TypeID),
		debug:           newLLVMDebugEmitter(mod, targetInfo.OS, debugBuild),
	}
	var b strings.Builder
	b.WriteString("source_filename = \"")
	b.WriteString(mod.Name)
	b.WriteString("\"\n")
	b.WriteString("target triple = \"")
	b.WriteString(targetInfo.LLVMTriple)
	b.WriteString("\"\n\n")
	if !emitter.emitNamedTypeDefinitions(&b) {
		return ""
	}
	printUsed, _, allocUsed, allocatorRuntimeUsed, freeRuntimeUsed := moduleRuntimeOperations(mod)
	if printUsed {
		b.WriteString("@.print.signed = private unnamed_addr constant [5 x i8] c\"%lld\\00\", align 1\n")
		b.WriteString("@.print.unsigned = private unnamed_addr constant [5 x i8] c\"%llu\\00\", align 1\n")
		b.WriteString("@.print.float = private unnamed_addr constant [3 x i8] c\"%g\\00\", align 1\n")
		b.WriteString("@.print.string = private unnamed_addr constant [3 x i8] c\"%s\\00\", align 1\n")
		b.WriteString("@.print.str = private unnamed_addr constant [5 x i8] c\"%.*s\\00\", align 1\n")
		b.WriteString("@.print.pointer = private unnamed_addr constant [3 x i8] c\"%p\\00\", align 1\n")
		b.WriteString("@.print.true = private unnamed_addr constant [5 x i8] c\"true\\00\", align 1\n")
		b.WriteString("@.print.false = private unnamed_addr constant [6 x i8] c\"false\\00\", align 1\n\n")
		b.WriteString("@.print.newline = private unnamed_addr constant [2 x i8] c\"\\0A\\00\", align 1\n\n")
	}

	for _, entry := range mod.StaticData {
		if entry.Constant == nil {
			escaped := llvmEscapeString(entry.Bytes)
			fmt.Fprintf(&b, "%s = private unnamed_addr constant [%d x i8] c\"%s\", align %d\n", entry.Name, len(entry.Bytes)+1, escaped, entry.Align)
			continue
		}
		value, ok := emitter.staticConstant(entry.Constant, entry.Type)
		if !ok {
			emitter.markInvalid("cannot lower typed static constant " + entry.Name)
			continue
		}
		llvmType := emitter.layout(entry.Type).Text
		fmt.Fprintf(&b, "%s = constant %s %s\n", entry.Name, llvmType, value)
	}
	if len(mod.StaticData) > 0 {
		b.WriteString("\n")
	}

	emittedItabs := make(map[string]bool)
	interfaceMakes := make([]*mir.InterfaceMake, 0)
	hasItab := false
	for _, fn := range mod.Funcs {
		if fn == nil || fn.Blocks == nil {
			continue
		}
		for _, block := range fn.Blocks {
			if block == nil {
				continue
			}
			for _, instr := range block.Instrs {
				assign, ok := instr.(*mir.Assign)
				if !ok || assign == nil || assign.Value == nil {
					continue
				}
				makeVal, ok := assign.Value.(*mir.InterfaceMake)
				if !ok || makeVal == nil {
					continue
				}
				itabSym := interfaceSymbolName("itab", mod.Types, makeVal.Type, makeVal.DataType)
				if emittedItabs[itabSym] {
					continue
				}
				emittedItabs[itabSym] = true
				interfaceMakes = append(interfaceMakes, makeVal)
				hasItab = true
				b.WriteString(itabSym)
				fmt.Fprintf(&b, " = private constant [%d x i8*] [", interfaceVtableLength(mod.Types, makeVal.Type, len(makeVal.Slots)))
				fmt.Fprintf(&b, "i8* bitcast (void (i8*)* %s to i8*)", interfaceSymbolName("iface_drop", mod.Types, makeVal.Type, makeVal.DataType))
				if isOwnedInterfaceType(mod.Types, makeVal.Type) {
					fmt.Fprintf(&b, ", i8* bitcast (void (i8*, i8*)* %s to i8*)", interfaceSymbolName("iface_release", mod.Types, makeVal.Type, makeVal.DataType))
				}
				for i, slot := range makeVal.Slots {
					b.WriteString(", ")
					refName, ok := slot.(*mir.RefName)
					slotName := ""
					if ok && refName != nil {
						slotName = "@" + ir.SanitizeSymbolName(ir.StripSymbolInstance(refName.Name))
					} else {
						slotName = "null"
					}
					slotLayout, ok := emitter.interfaceSlotLayout(makeVal.Type, i)
					if !ok {
						slotLayout = llvmPointerLayout(llvmScalarLayout("i8"))
					}
					if slotName == "null" {
						b.WriteString("i8* null")
					} else {
						fmt.Fprintf(&b, "i8* bitcast (%s %s to i8*)", slotLayout.Text, slotName)
					}
				}
				b.WriteString("], align 8\n")
			}
		}
	}
	if hasItab {
		b.WriteString("\n")
	}

	hasDecl := false
	freeDeclared := false
	mallocDeclared := false
	for _, fn := range mod.Funcs {
		if fn == nil {
			continue
		}
		name := ir.SanitizeSymbolName(fn.Name)
		if name == "free" {
			freeDeclared = freeDeclared || runtimeFreeDeclaration(mod.Types, fn)
		}
		if name == "malloc" {
			mallocDeclared = mallocDeclared || runtimeMallocDeclaration(mod.Types, fn)
		}
		if fn.Blocks != nil {
			continue
		}
		hasDecl = true
		b.WriteString("declare ")
		b.WriteString(emitter.layout(llvmFunctionReturnType(mod.Types, fn)).Text)
		b.WriteString(" @")
		b.WriteString(name)
		b.WriteString("(")
		for i, param := range fn.Params {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(emitter.layout(param.Type).Text)
		}
		b.WriteString(")\n")
	}
	if hasDecl {
		b.WriteString("\n")
	}
	if printUsed {
		b.WriteString("declare i32 @printf(i8*, ...)\n\n")
	}
	if (allocatorRuntimeUsed || freeRuntimeUsed) && !freeDeclared {
		b.WriteString("declare void @free(i8*)\n\n")
	}
	if allocUsed || allocatorRuntimeUsed {
		sizeType := emitter.layout(mod.Types.IndexType()).Text
		if !mallocDeclared {
			fmt.Fprintf(&b, "declare i8* @malloc(%s)\n", sizeType)
		}
		if allocUsed {
			fmt.Fprintf(&b, "declare { %s, i1 } @llvm.umul.with.overflow.%s(%s, %s)\n\n", sizeType, sizeType, sizeType, sizeType)
		} else {
			b.WriteString("\n")
		}
	}

	if allocatorRuntimeUsed {
		emitDefaultDescriptorThunks(&b, emitter)
	}

	decls := collectCallDecls(mod)
	for _, decl := range decls {
		if (allocUsed || allocatorRuntimeUsed) && ir.SanitizeSymbolName(decl.Name) == "malloc" {
			continue
		}
		b.WriteString("declare ")
		b.WriteString(emitter.layout(decl.ReturnType).Text)
		b.WriteString(" @")
		b.WriteString(ir.SanitizeSymbolName(decl.Name))
		b.WriteString("(")
		for i, param := range decl.Params {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(emitter.layout(param).Text)
		}
		b.WriteString(")\n")
	}
	if len(decls) > 0 {
		b.WriteString("\n")
	}

	hasDefine := false
	for _, fn := range mod.Funcs {
		if fn != nil && fn.Blocks != nil {
			hasDefine = true
			break
		}
	}
	if !hasDefine {
		return finalLLVMText(&b, emitter)
	}
	emitter.emitNamedDropHelpers(&b)
	for _, thunk := range mod.InterfaceThunks {
		emitInterfaceThunk(&b, emitter, thunk)
	}
	for _, makeVal := range interfaceMakes {
		emitInterfacePayloadDropThunk(&b, emitter, makeVal)
		if isOwnedInterfaceType(mod.Types, makeVal.Type) {
			emitInterfacePayloadReleaseThunk(&b, emitter, makeVal)
		}
	}
	if len(mod.InterfaceThunks) > 0 || len(interfaceMakes) > 0 {
		b.WriteString("\n")
	}
	for _, fn := range mod.Funcs {
		if fn == nil || fn.Blocks == nil {
			continue
		}
		debugScopeID := -1
		if emitter.debug != nil {
			debugScopeID = emitter.debug.functionID(fn)
		}
		b.WriteString("define ")
		b.WriteString(emitter.layout(llvmFunctionReturnType(mod.Types, fn)).Text)
		b.WriteString(" @")
		b.WriteString(ir.SanitizeSymbolName(fn.Name))
		b.WriteString("(")
		for i, param := range fn.Params {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(emitter.layout(param.Type).Text)
			b.WriteString(" %")
			b.WriteString(param.Name)
		}
		b.WriteString(")")
		if debugScopeID >= 0 {
			fmt.Fprintf(&b, " !dbg !%d", debugScopeID)
		}
		b.WriteString(" {\n")
		lb := newLLVMBuilder(&b, emitter, debugScopeID)
		stackSlots := stackLocalSlots(mod.Types, fn)
		for _, param := range fn.Params {
			lb.locals[param.Name] = lb.value("%"+param.Name, emitter.layout(param.Type))
		}
		for _, block := range fn.Blocks {
			if block == nil {
				continue
			}
			lb.label(block.ID)
			if block.ID == fn.EntryID {
				emitStackLocalSlots(lb, stackSlots)
			}
			for _, instr := range block.Instrs {
				if instr == nil {
					continue
				}
				lb.setLocation(instr.SourceLocation())
				if assign, ok := instr.(*mir.Assign); ok && assign != nil {
					val := emitValueExpr(lb, assign.Value)
					if ptr, ok := lb.localPtrs[assign.Name]; ok {
						lb.store(ptr, val)
					} else {
						lb.locals[assign.Name] = val
					}
					continue
				}
				if store, ok := instr.(*mir.Store); ok && store != nil {
					emitStore(lb, store)
					continue
				}
				if printInstr, ok := instr.(*mir.Print); ok && printInstr != nil {
					emitPrint(lb, printInstr)
					continue
				}
				if dropInstr, ok := instr.(*mir.Drop); ok && dropInstr != nil {
					emitDrop(lb, dropInstr)
					continue
				}
				if operation, ok := instr.(*mir.DynamicArrayOp); ok && operation != nil {
					emitDynamicArrayOp(lb, operation)
					continue
				}
				if call, ok := instr.(*mir.Call); ok && call != nil {
					emitDiscardedCall(lb, call)
					continue
				}
				if call, ok := instr.(*mir.InterfaceCall); ok && call != nil {
					emitDiscardedInterfaceCall(lb, call)
				}
			}
			if block.Term != nil {
				returnLayout := emitter.layout(llvmFunctionReturnType(mod.Types, fn))
				lb.setLocation(block.Term.SourceLocation())
				switch term := block.Term.(type) {
				case *mir.Jump:
					lb.branch(fmt.Sprintf("b%d", term.TargetID))
				case *mir.Branch:
					cond := emitCondRef(lb, term.Cond)
					lb.condBranch(cond, fmt.Sprintf("b%d", term.ThenID), fmt.Sprintf("b%d", term.ElseID))
				case *mir.SwitchVariant:
					emitVariantSwitch(lb, term)
				case *mir.Ret:
					if term.Value == nil || isVoidType(mod.Types, fn.ReturnType) {
						if returnLayout.Kind != llvmLayoutVoid {
							lb.ret(lb.value("0", returnLayout), returnLayout)
						} else {
							lb.retVoid(returnLayout)
						}
						continue
					}
					val := emitRef(lb, term.Value)
					lb.ret(val, returnLayout)
				}
			}
			lb.setLocation(nil)
		}
		b.WriteString("}\n")
	}
	return finalLLVMText(&b, emitter)
}

func (e *llvmEmitter) staticConstant(value constvalue.Value, typeID ir.TypeID) (string, bool) {
	if e == nil || e.mod == nil || e.mod.Types == nil || value == nil {
		return "", false
	}
	typ, ok := e.mod.Types.Type(typeID)
	if !ok {
		return "", false
	}
	switch constant := value.(type) {
	case *constvalue.IntConst:
		if constant == nil || (typ.Kind != ir.TypeInteger && typ.Kind != ir.TypeByte && typ.Kind != ir.TypeChar) {
			return "", false
		}
		return constant.Text(), true
	case *constvalue.FloatConst:
		if constant == nil || typ.Kind != ir.TypeFloat {
			return "", false
		}
		return llvmFloatConst(constant.Text(), typ.Bits), true
	case *constvalue.BoolConst:
		if constant == nil || typ.Kind != ir.TypeBool {
			return "", false
		}
		return fmt.Sprintf("%t", constant.Bool()), true
	case *constvalue.StringConst:
		if constant == nil || typ.Kind != ir.TypeCStr {
			return "", false
		}
		for _, entry := range e.mod.StaticData {
			if entry.Constant == nil && entry.Bytes == constant.Text() {
				arrayType := fmt.Sprintf("[%d x i8]", len(entry.Bytes)+1)
				return fmt.Sprintf("getelementptr inbounds (%s, %s* %s, i64 0, i64 0)", arrayType, arrayType, entry.Name), true
			}
		}
		return "", false
	case *constvalue.VariantConst:
		return e.staticVariantConstant(constant, typeID, typ)
	default:
		return "", false
	}
}

func (e *llvmEmitter) staticVariantConstant(value *constvalue.VariantConst, typeID ir.TypeID, typ ir.Type) (string, bool) {
	if value == nil || typ.Kind != ir.TypeVariant || (value.NominalIdentity() != "" && value.NominalIdentity() != typ.Identity) {
		return "", false
	}
	variantCase, ok := typ.VariantCase(value.CaseIndex())
	if !ok {
		return "", false
	}
	layout, ok := e.layoutType(typeID, false)
	if !ok || layout.VariantTag < 0 || layout.VariantTag >= len(layout.Elements) {
		return "", false
	}
	elements := make([]string, len(layout.Elements))
	for index, element := range layout.Elements {
		elements[index] = element.Text + " zeroinitializer"
	}
	tag := fmt.Sprintf("%d", value.CaseIndex())
	if layout.Elements[layout.VariantTag].Text == "i1" {
		tag = fmt.Sprintf("%t", value.CaseIndex() != 0)
	}
	elements[layout.VariantTag] = layout.Elements[layout.VariantTag].Text + " " + tag
	fields := value.FieldValues()
	if variantCase.Payload == ir.InvalidType {
		if len(fields) != 0 {
			return "", false
		}
		return "{ " + strings.Join(elements, ", ") + " }", true
	}
	payloadIndex, found := layout.VariantPayloads[value.CaseIndex()]
	if !found || payloadIndex < 0 || payloadIndex >= len(elements) {
		return "", false
	}
	payload, ok := e.staticVariantPayload(fields, variantCase.Payload)
	if !ok {
		return "", false
	}
	elements[payloadIndex] = layout.Elements[payloadIndex].Text + " " + payload
	return "{ " + strings.Join(elements, ", ") + " }", true
}

func (e *llvmEmitter) staticVariantPayload(fields []constvalue.Value, typeID ir.TypeID) (string, bool) {
	typ, ok := e.mod.Types.Type(typeID)
	if !ok {
		return "", false
	}
	if typ.Kind != ir.TypeStruct {
		if len(fields) != 1 {
			return "", false
		}
		return e.staticConstant(fields[0], typeID)
	}
	if len(fields) != len(typ.Fields) {
		return "", false
	}
	elements := make([]string, len(fields))
	for index, field := range fields {
		text, ok := e.staticConstant(field, typ.Fields[index].Type)
		if !ok {
			return "", false
		}
		elements[index] = e.layout(typ.Fields[index].Type).Text + " " + text
	}
	return "{ " + strings.Join(elements, ", ") + " }", true
}

func emitVariantSwitch(b *llvmBuilder, term *mir.SwitchVariant) {
	if b == nil || term == nil || term.Value == nil || len(term.Targets) == 0 {
		if b != nil {
			b.emitter.markInvalid("variant switch requires subject and targets")
		}
		return
	}
	variant, ok := b.emitter.mod.Types.Type(mirRefType(term.Value))
	if !ok || variant.Kind != ir.TypeVariant {
		b.emitter.markInvalid("variant switch requires variant subject")
		return
	}
	if len(term.Targets) != len(variant.Cases) {
		b.emitter.markInvalid("variant switch must cover every case")
		return
	}
	value := emitRef(b, term.Value)
	tag := b.variantTag(value)
	cases := make([]llvmSwitchCase, len(term.Targets))
	seen := make(map[int]struct{}, len(term.Targets))
	for i, target := range term.Targets {
		if _, caseOK := variant.VariantCase(target.Case); !caseOK {
			b.emitter.markInvalid(fmt.Sprintf("variant switch has invalid case %d", target.Case))
			return
		}
		if _, duplicate := seen[target.Case]; duplicate {
			b.emitter.markInvalid(fmt.Sprintf("variant switch repeats case %d", target.Case))
			return
		}
		seen[target.Case] = struct{}{}
		cases[i] = llvmSwitchCase{
			Value: b.variantCaseTag(target.Case, tag.Layout),
			Label: fmt.Sprintf("b%d", target.TargetID),
		}
	}
	invalidLabel := fmt.Sprintf("invalid_variant_%d", b.nextID)
	b.nextID++
	b.switchBranch(tag, invalidLabel, cases)
	b.namedLabel(invalidLabel)
	b.trap()
}

// ValidateRuntimeSymbols checks runtime ABI reservations after ownership and
// lowering have made actual print, allocation, and destruction use explicit.
func ValidateRuntimeSymbols(modules []*mir.Module, diag *diagnostics.DiagnosticBag) bool {
	printUsed := false
	dropUsed := false
	allocUsed := false
	allocatorRuntimeUsed := false
	freeRuntimeUsed := false
	externsValid := true
	for _, mod := range modules {
		if !validateExternOwnership(mod, diag) {
			externsValid = false
		}
		usesPrint, usesDrop, usesAlloc, usesAllocatorRuntime, usesFreeRuntime := moduleRuntimeOperations(mod)
		printUsed = printUsed || usesPrint
		dropUsed = dropUsed || usesDrop
		allocUsed = allocUsed || usesAlloc
		allocatorRuntimeUsed = allocatorRuntimeUsed || usesAllocatorRuntime
		freeRuntimeUsed = freeRuntimeUsed || usesFreeRuntime
	}
	if !externsValid {
		return false
	}
	if !printUsed && !dropUsed && !allocUsed && !allocatorRuntimeUsed && !freeRuntimeUsed {
		return true
	}

	valid := true
	for _, mod := range modules {
		if mod == nil {
			continue
		}
		for _, fn := range mod.Funcs {
			if fn == nil {
				continue
			}
			switch ir.SanitizeSymbolName(fn.Name) {
			case "printf":
				if printUsed {
					valid = false
					if diag != nil {
						diag.AddError(diagnostics.ErrRedeclaredSymbol,
							"runtime symbol `printf` is reserved when print is used", fn.Location,
							"conflicts with print runtime")
					}
				}
			case "free":
				if (allocatorRuntimeUsed || freeRuntimeUsed) && !runtimeFreeDeclaration(mod.Types, fn) {
					valid = false
					if diag != nil {
						diag.AddError(diagnostics.ErrRedeclaredSymbol,
							"runtime symbol `free` must have signature fn(rawptr) -> void", fn.Location,
							"runtime requires fn(rawptr) -> void")
					}
				}
			case "malloc":
				if (allocUsed || allocatorRuntimeUsed) && !runtimeMallocDeclaration(mod.Types, fn) {
					valid = false
					if diag != nil {
						diag.AddError(diagnostics.ErrRedeclaredSymbol,
							"runtime symbol `malloc` must have signature fn(usize) -> rawptr", fn.Location,
							"runtime requires fn(usize) -> rawptr")
					}
				}
			}
		}
	}
	return valid
}

func validateExternOwnership(mod *mir.Module, diag *diagnostics.DiagnosticBag) bool {
	valid := true
	if mod == nil {
		return valid
	}
	for _, fn := range mod.Funcs {
		if fn == nil || fn.Blocks != nil {
			continue
		}
		owned := typeNeedsDrop(mod.Types, fn.ReturnType)
		for _, param := range fn.Params {
			owned = owned || typeNeedsDrop(mod.Types, param.Type)
		}
		if !owned {
			continue
		}
		valid = false
		if diag != nil {
			diag.AddError(diagnostics.ErrInvalidType,
				"extern function cannot use allocation-owning types; use rawptr at foreign boundary",
				fn.Location,
				"foreign ownership must be adopted explicitly")
		}
	}
	return valid
}

func runtimeFreeDeclaration(types *ir.TypeTable, fn *mir.Function) bool {
	if fn == nil || fn.Blocks != nil || len(fn.Params) != 1 {
		return false
	}
	return isVoidType(types, fn.ReturnType) && isTypeKind(types, fn.Params[0].Type, ir.TypeRawPtr)
}

func runtimeMallocDeclaration(types *ir.TypeTable, fn *mir.Function) bool {
	if fn == nil || fn.Blocks != nil || len(fn.Params) != 1 {
		return false
	}
	return isTypeKind(types, fn.ReturnType, ir.TypeRawPtr) && fn.Params[0].Type == types.IndexType()
}

func moduleRuntimeOperations(mod *mir.Module) (printUsed bool, dropUsed bool, allocUsed bool, allocatorRuntimeUsed bool, freeRuntimeUsed bool) {
	if mod == nil {
		return false, false, false, false, false
	}
	for _, fn := range mod.Funcs {
		if fn == nil {
			continue
		}
		for _, block := range fn.Blocks {
			if block == nil {
				continue
			}
			for _, instr := range block.Instrs {
				if _, ok := instr.(*mir.Print); ok {
					printUsed = true
				}
				if drop, ok := instr.(*mir.Drop); ok && drop != nil {
					dropUsed = true
					freeRuntimeUsed = freeRuntimeUsed || typeNeedsRawFreeID(mod.Types, mirRefType(drop.Value))
					if typeCarriesAllocatorID(mod.Types, mirRefType(drop.Value)) {
						allocUsed = true
						allocatorRuntimeUsed = true
					}
				}
				if call, ok := instr.(*mir.InterfaceCall); ok && consumesOwnedInterfaceStorage(mod.Types, call) {
					dropUsed = true
					allocatorRuntimeUsed = true
				}
				if operation, ok := instr.(*mir.DynamicArrayOp); ok && operation != nil {
					if operation.Op == symbols.CompilerOpShrink {
						elem, ok := dynamicArrayElementType(mod.Types, operation.ArrayType)
						dropUsed = dropUsed || (ok && typeNeedsDrop(mod.Types, elem))
						allocUsed = allocUsed || (ok && typeCarriesAllocatorID(mod.Types, elem))
						allocatorRuntimeUsed = allocatorRuntimeUsed || (ok && typeCarriesAllocatorID(mod.Types, elem))
						freeRuntimeUsed = freeRuntimeUsed || (ok && typeNeedsRawFreeID(mod.Types, elem))
					} else {
						allocUsed = true
						dropUsed = true
						allocatorRuntimeUsed = true
						freeRuntimeUsed = true
					}
					continue
				}
				if assign, ok := instr.(*mir.Assign); ok && assign != nil {
					if _, ok := assign.Value.(*mir.Alloc); ok {
						allocatorRuntimeUsed = true
						dropUsed = true
						continue
					}
					if alloc, ok := assign.Value.(*mir.DynamicArrayAlloc); ok && alloc != nil && alloc.Length > 0 {
						allocUsed = true
						allocatorRuntimeUsed = true
					} else if _, ok := assign.Value.(*mir.DynamicArrayAlloc); ok {
						allocatorRuntimeUsed = true
					}
					if makeVal, ok := assign.Value.(*mir.InterfaceMake); ok && makeVal != nil {
						if isOwnedInterfaceType(mod.Types, makeVal.Type) {
							allocatorRuntimeUsed = true
						}
						if typeNeedsDrop(mod.Types, makeVal.DataType) {
							dropUsed = true
							allocatorRuntimeUsed = true
							freeRuntimeUsed = freeRuntimeUsed || typeNeedsRawFreeID(mod.Types, makeVal.DataType)
						}
					}
					if call, ok := assign.Value.(*mir.InterfaceCall); ok && consumesOwnedInterfaceStorage(mod.Types, call) {
						dropUsed = true
						allocatorRuntimeUsed = true
					}
				}
				if printUsed && dropUsed && allocUsed && allocatorRuntimeUsed && freeRuntimeUsed {
					return true, true, true, true, true
				}
			}
		}
	}
	return printUsed, dropUsed, allocUsed, allocatorRuntimeUsed, freeRuntimeUsed
}

func emitDefaultDescriptorThunks(b *strings.Builder, emitter *llvmEmitter) {
	sizeLayout := emitter.layout(emitter.mod.Types.IndexType())
	sizeType := sizeLayout.Text
	i8 := llvmScalarLayout("i8")
	rawPointer := llvmPointerLayout(i8)
	void := &llvmLayout{Text: "void", Kind: llvmLayoutVoid}
	b.WriteString("\n")
	b.WriteString("@peeper_default_alloc = private constant [3 x i8*] [i8* null, i8* bitcast (i8* (i8*, ")
	b.WriteString(sizeType)
	b.WriteString(", i32)* @peeper_default_alloc_fn to i8*), i8* bitcast (void (i8*, i8*, ")
	b.WriteString(sizeType)
	b.WriteString(", i32)* @peeper_default_free_fn to i8*)]\n\n")

	fmt.Fprintf(b, "define internal i8* @peeper_default_alloc_fn(i8* %%ctx, %s %%size, i32 %%align) {\n", sizeType)
	lb := newLLVMBuilder(b, emitter, -1)
	lb.namedLabel("entry")
	ptr := lb.call(lb.value("@malloc", llvmFunctionLayout(rawPointer, []*llvmLayout{sizeLayout})), []llvmValue{lb.value("%size", sizeLayout)})
	isNull := lb.compare("icmp", "eq", ptr, lb.value("null", rawPointer))
	lb.condBranch(isNull, "trap", "done")
	lb.namedLabel("trap")
	lb.trap()
	lb.namedLabel("done")
	lb.ret(ptr, rawPointer)
	b.WriteString("}\n\n")

	fmt.Fprintf(b, "define internal void @peeper_default_free_fn(i8* %%ctx, i8* %%ptr, %s %%size, i32 %%align) {\n", sizeType)
	lb = newLLVMBuilder(b, emitter, -1)
	lb.namedLabel("entry")
	lb.call(lb.value("@free", llvmFunctionLayout(void, []*llvmLayout{rawPointer})), []llvmValue{lb.value("%ptr", rawPointer)})
	lb.retVoid(void)
	b.WriteString("}\n\n")
}

// finalLLVMText appends globals discovered late during instruction emission.
// External globals are collected while lowering refs, so they cannot be emitted
// earlier with full type information.
func finalLLVMText(b *strings.Builder, emitter *llvmEmitter) string {
	if emitter != nil && emitter.invalid {
		return ""
	}
	if b == nil {
		return ""
	}
	if emitter != nil && len(emitter.externalGlobals) > 0 {
		b.WriteString("\n; external globals\n")
		for name, typeID := range emitter.externalGlobals {
			llvmType := emitter.layout(typeID).Text
			fmt.Fprintf(b, "%s = external global %s\n", name, llvmType)
		}
	}
	if emitter != nil && emitter.debug != nil {
		emitter.debug.appendModuleMetadata(b)
	}
	return b.String()
}

// llvmFunctionReturnType applies ABI-only return adjustments.
// Most functions keep their MIR return type. `main` is special: source-level
// no-value `main` is represented internally as `void`, but native process entry
// still needs an `i32` return in LLVM, so backend converts only that case.
func llvmFunctionReturnType(types *ir.TypeTable, fn *mir.Function) ir.TypeID {
	if fn == nil {
		return ir.InvalidType
	}
	if fn.Name == "main" && isVoidType(types, fn.ReturnType) {
		return types.Intern(ir.Type{Kind: ir.TypeInteger, Signed: true, Bits: 32})
	}
	return fn.ReturnType
}

type stackLocalSlot struct {
	Name string
	Type ir.TypeID
}

func stackLocalSlots(typeTable *ir.TypeTable, fn *mir.Function) []stackLocalSlot {
	if fn == nil {
		return nil
	}
	paramTypes := make(map[string]ir.TypeID, len(fn.Params))
	for _, param := range fn.Params {
		paramTypes[param.Name] = param.Type
	}
	counts := make(map[string]int)
	types := make(map[string]ir.TypeID)
	addressed := make(map[string]bool)
	order := make([]string, 0)
	seen := make(map[string]bool)
	recordPlace := func(place *mir.Place) {
		if !placeNeedsRootAddr(typeTable, place) {
			return
		}
		root, ok := place.Root.(*mir.RefName)
		if !ok || root == nil || root.Name == "" {
			return
		}
		addressed[root.Name] = true
		if !seen[root.Name] {
			seen[root.Name] = true
			order = append(order, root.Name)
		}
	}
	for _, block := range fn.Blocks {
		if block == nil {
			continue
		}
		for _, instr := range block.Instrs {
			if store, ok := instr.(*mir.Store); ok && store != nil {
				recordPlace(store.Place)
				continue
			}
			assign, ok := instr.(*mir.Assign)
			if !ok || assign == nil || assign.Name == "" {
				continue
			}
			if !seen[assign.Name] {
				seen[assign.Name] = true
				order = append(order, assign.Name)
			}
			counts[assign.Name]++
			if typ := mirValueType(assign.Value); typ != ir.InvalidType {
				types[assign.Name] = typ
			}
			switch value := assign.Value.(type) {
			case *mir.AddrOf:
				recordPlace(value.Place)
			case *mir.Load:
				recordPlace(value.Place)
			case *mir.SliceView:
				if sliceViewUsesPlacePtr(typeTable, value.Source) {
					recordPlace(value.Source)
				}
			}
		}
	}
	slots := make([]stackLocalSlot, 0)
	for _, name := range order {
		typ := types[name]
		if typ == ir.InvalidType {
			typ = paramTypes[name]
		}
		if typ == ir.InvalidType {
			continue
		}
		if counts[name] > 1 || paramTypes[name] != ir.InvalidType || addressed[name] {
			slots = append(slots, stackLocalSlot{Name: name, Type: typ})
		}
	}
	return slots
}

func emitStackLocalSlots(b *llvmBuilder, slots []stackLocalSlot) {
	if b == nil {
		return
	}
	for _, slot := range slots {
		ptr := b.alloca(b.emitter.layout(slot.Type))
		if paramValue, ok := b.locals[slot.Name]; ok {
			b.store(ptr, paramValue)
		}
		b.localPtrs[slot.Name] = ptr
	}
}
