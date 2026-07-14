package llvm

import (
	"fmt"
	"strings"

	"compiler/internal/diagnostics"
	"compiler/internal/ir"
	"compiler/internal/ir/mir"
	"compiler/internal/semantics/symbols"
)

// GenerateLLVMIR is backend entrypoint.
// It emits module text in LLVM order: static data, helper itabs, declarations,
// thunks, then function bodies. It also keeps one emitter state object so type
// lowering failures and deferred external globals are reported consistently.
func GenerateLLVMIR(mod *mir.Module, diag *diagnostics.DiagnosticBag, targetTriple string, debugBuild bool, targetOS string) string {
	if mod == nil {
		return ""
	}
	if !ValidateRuntimeSymbols([]*mir.Module{mod}, diag) {
		return ""
	}

	emitter := &llvmEmitter{
		mod:             mod,
		diag:            diag,
		badTypes:        make(map[string]struct{}),
		externalGlobals: make(map[string]string),
		debug:           newLLVMDebugEmitter(mod, targetOS, debugBuild),
	}
	var b strings.Builder
	b.WriteString("source_filename = \"")
	b.WriteString(mod.Name)
	b.WriteString("\"\n")
	b.WriteString("target triple = \"")
	b.WriteString(targetTriple)
	b.WriteString("\"\n\n")
	printUsed, dropUsed, allocUsed := moduleRuntimeOperations(mod)
	if printUsed {
		b.WriteString("@.print.signed = private unnamed_addr constant [5 x i8] c\"%lld\\00\", align 1\n")
		b.WriteString("@.print.unsigned = private unnamed_addr constant [5 x i8] c\"%llu\\00\", align 1\n")
		b.WriteString("@.print.float = private unnamed_addr constant [3 x i8] c\"%g\\00\", align 1\n")
		b.WriteString("@.print.string = private unnamed_addr constant [3 x i8] c\"%s\\00\", align 1\n")
		b.WriteString("@.print.pointer = private unnamed_addr constant [3 x i8] c\"%p\\00\", align 1\n")
		b.WriteString("@.print.true = private unnamed_addr constant [5 x i8] c\"true\\00\", align 1\n")
		b.WriteString("@.print.false = private unnamed_addr constant [6 x i8] c\"false\\00\", align 1\n\n")
	}

	for _, entry := range mod.StaticData {
		isStr := entry.Type == "cstr" || (strings.HasPrefix(entry.Type, "[") && strings.HasSuffix(entry.Type, " x i8]"))
		if isStr {
			escaped := llvmEscapeString(entry.Value)
			fmt.Fprintf(&b, "%s = private unnamed_addr constant %s c\"%s\", align %d\n", entry.Name, entry.Type, escaped, entry.Align)
		} else {
			llvmType := emitter.llvmType(entry.Type)
			fmt.Fprintf(&b, "%s = constant %s %s, align %d\n", entry.Name, llvmType, entry.Value, entry.Align)
		}
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
				itabSym := itabSymbolName(makeVal.Type, makeVal.DataType)
				if emittedItabs[itabSym] {
					continue
				}
				emittedItabs[itabSym] = true
				interfaceMakes = append(interfaceMakes, makeVal)
				hasItab = true

				b.WriteString(itabSym)
				fmt.Fprintf(&b, " = private constant [%d x i8*] [", len(makeVal.Slots)+1)
				fmt.Fprintf(&b, "i8* bitcast (void (i8*)* %s to i8*)", interfaceDropSymbolName(makeVal.Type, makeVal.DataType))
				for i, slot := range makeVal.Slots {
					b.WriteString(", ")
					refName, ok := slot.(*mir.RefName)
					slotName := ""
					if ok && refName != nil {
						slotName = "@" + ir.SanitizeSymbolName(ir.StripSymbolInstance(refName.Name))
					} else {
						slotName = "null"
					}
					slotType, ok := interfaceSlotLLVMTypeFromInterface(makeVal.Type, i)
					if !ok {
						slotType = "i8*"
					}
					if slotName == "null" {
						b.WriteString("i8* null")
					} else {
						fmt.Fprintf(&b, "i8* bitcast (%s %s to i8*)", slotType, slotName)
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
			freeDeclared = freeDeclared || runtimeFreeDeclaration(fn)
		}
		if name == "malloc" {
			mallocDeclared = mallocDeclared || runtimeMallocDeclaration(fn)
		}
		if fn.Blocks != nil {
			continue
		}
		hasDecl = true
		b.WriteString("declare ")
		b.WriteString(emitter.llvmType(llvmFunctionReturnType(fn)))
		b.WriteString(" @")
		b.WriteString(name)
		b.WriteString("(")
		for i, param := range fn.Params {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(emitter.llvmType(param.Type))
		}
		b.WriteString(")\n")
	}
	if hasDecl {
		b.WriteString("\n")
	}
	if printUsed {
		b.WriteString("declare i32 @printf(i8*, ...)\n\n")
	}
	if dropUsed && !freeDeclared {
		b.WriteString("declare void @free(i8*)\n\n")
	}
	if allocUsed {
		sizeType := emitter.llvmType("usize")
		if !mallocDeclared {
			fmt.Fprintf(&b, "declare i8* @malloc(%s)\n", sizeType)
		}
		fmt.Fprintf(&b, "declare { %s, i1 } @llvm.umul.with.overflow.%s(%s, %s)\n\n", sizeType, sizeType, sizeType, sizeType)
	}

	decls := collectCallDecls(mod)
	for _, decl := range decls {
		if allocUsed && ir.SanitizeSymbolName(decl.Name) == "malloc" {
			continue
		}
		b.WriteString("declare ")
		b.WriteString(emitter.llvmType(decl.ReturnType))
		b.WriteString(" @")
		b.WriteString(ir.SanitizeSymbolName(decl.Name))
		b.WriteString("(")
		for i, param := range decl.Params {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(emitter.llvmType(param))
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
	for _, thunk := range mod.InterfaceThunks {
		emitInterfaceThunk(&b, emitter, thunk)
	}
	for _, makeVal := range interfaceMakes {
		emitInterfacePayloadDropThunk(&b, emitter, makeVal)
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
		b.WriteString(emitter.llvmType(llvmFunctionReturnType(fn)))
		b.WriteString(" @")
		b.WriteString(ir.SanitizeSymbolName(fn.Name))
		b.WriteString("(")
		for i, param := range fn.Params {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(emitter.llvmType(param.Type))
			b.WriteString(" %")
			b.WriteString(param.Name)
		}
		b.WriteString(")")
		if debugScopeID >= 0 {
			fmt.Fprintf(&b, " !dbg !%d", debugScopeID)
		}
		b.WriteString(" {\n")
		lb := newLLVMBuilder(&b, emitter, debugScopeID)
		stackSlots := stackLocalSlots(fn)
		for _, param := range fn.Params {
			lb.locals[param.Name] = "%" + param.Name
			lb.localTypes[param.Name] = param.Type
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
				lb.setLocation(mir.InstrLocation(instr))
				if assign, ok := instr.(*mir.Assign); ok && assign != nil {
					val := emitValueExpr(lb, assign.Value)
					valueType := mirValueType(assign.Value)
					if ptr, ok := lb.localPtrs[assign.Name]; ok && ptr != "" {
						llvmType := lb.emitter.llvmType(lb.localTypes[assign.Name])
						lb.line(fmt.Sprintf("store %s %s, %s* %s", llvmType, val, llvmType, ptr))
					} else {
						lb.locals[assign.Name] = val
						if valueType != "" {
							lb.localTypes[assign.Name] = valueType
						}
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
				if call, ok := instr.(*mir.Call); ok && call != nil {
					emitDiscardedCall(lb, call)
					continue
				}
				if call, ok := instr.(*mir.InterfaceCall); ok && call != nil {
					emitDiscardedInterfaceCall(lb, call)
				}
			}
			if block.Term != nil {
				lb.setLocation(mir.TerminatorLocation(block.Term))
				switch term := block.Term.(type) {
				case *mir.Jump:
					lb.line(fmt.Sprintf("br label %%b%d", term.TargetID))
				case *mir.Branch:
					cond := emitCondRef(lb, term.Cond)
					lb.line(fmt.Sprintf("br i1 %s, label %%b%d, label %%b%d", cond, term.ThenID, term.ElseID))
				case *mir.Ret:
					if term.Value == nil || fn.ReturnType == "void" {
						if llvmFunctionReturnType(fn) == "i32" {
							lb.line("ret i32 0")
						} else {
							lb.line("ret void")
						}
						continue
					}
					val := emitRef(lb, term.Value)
					lb.line("ret " + emitter.llvmType(fn.ReturnType) + " " + val)
				}
			}
			lb.setLocation(nil)
		}
		b.WriteString("}\n")
	}
	return finalLLVMText(&b, emitter)
}

// ValidateRuntimeSymbols checks runtime ABI reservations after ownership and
// lowering have made actual print, allocation, and destruction use explicit.
func ValidateRuntimeSymbols(modules []*mir.Module, diag *diagnostics.DiagnosticBag) bool {
	printUsed := false
	dropUsed := false
	allocUsed := false
	for _, mod := range modules {
		usesPrint, usesDrop, usesAlloc := moduleRuntimeOperations(mod)
		printUsed = printUsed || usesPrint
		dropUsed = dropUsed || usesDrop
		allocUsed = allocUsed || usesAlloc
	}
	if !printUsed && !dropUsed && !allocUsed {
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
				if dropUsed && !runtimeFreeDeclaration(fn) {
					valid = false
					if diag != nil {
						diag.AddError(diagnostics.ErrRedeclaredSymbol,
							"runtime symbol `free` must have signature fn(rawptr) -> void", fn.Location,
							"runtime requires fn(rawptr) -> void")
					}
				}
			case "malloc":
				if allocUsed && !runtimeMallocDeclaration(fn) {
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

func runtimeFreeDeclaration(fn *mir.Function) bool {
	return fn != nil && fn.Blocks == nil && fn.ReturnType == "void" &&
		len(fn.Params) == 1 && fn.Params[0].Type == "rawptr"
}

func runtimeMallocDeclaration(fn *mir.Function) bool {
	if fn == nil || fn.Blocks != nil || fn.ReturnType != "rawptr" || len(fn.Params) != 1 {
		return false
	}
	paramType, paramOK := llvmTypeName(fn.Params[0].Type)
	sizeType, sizeOK := llvmTypeName("usize")
	return paramOK && sizeOK && paramType == sizeType
}

func moduleRuntimeOperations(mod *mir.Module) (printUsed bool, dropUsed bool, allocUsed bool) {
	if mod == nil {
		return false, false, false
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
				if _, ok := instr.(*mir.Drop); ok {
					dropUsed = true
				}
				if call, ok := instr.(*mir.InterfaceCall); ok && consumesOwnedInterfaceStorage(call) {
					dropUsed = true
				}
				if assign, ok := instr.(*mir.Assign); ok && assign != nil {
					if alloc, ok := assign.Value.(*mir.DynamicArrayAlloc); ok && alloc != nil && alloc.Length > 0 {
						allocUsed = true
					}
					if operation, ok := assign.Value.(*mir.DynamicArrayOp); ok {
						if operation.Op == symbols.CompilerOpShrink {
							elem := strings.TrimSpace(strings.TrimPrefix(operation.Type, "[]"))
							dropUsed = dropUsed || typeTextNeedsDrop(elem)
						} else {
							allocUsed = true
							dropUsed = true
						}
					}
					if makeVal, ok := assign.Value.(*mir.InterfaceMake); ok && makeVal != nil && typeTextNeedsDrop(makeVal.DataType) {
						dropUsed = true
					}
					if call, ok := assign.Value.(*mir.InterfaceCall); ok && consumesOwnedInterfaceStorage(call) {
						dropUsed = true
					}
				}
				if printUsed && dropUsed && allocUsed {
					return true, true, true
				}
			}
		}
	}
	return printUsed, dropUsed, allocUsed
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
		for name, typeText := range emitter.externalGlobals {
			llvmType := emitter.llvmType(typeText)
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
func llvmFunctionReturnType(fn *mir.Function) string {
	if fn == nil {
		return ""
	}
	if fn.Name == "main" && fn.ReturnType == "void" {
		return "i32"
	}
	return fn.ReturnType
}

type stackLocalSlot struct {
	Name string
	Type string
}

func stackLocalSlots(fn *mir.Function) []stackLocalSlot {
	if fn == nil {
		return nil
	}
	paramTypes := make(map[string]string, len(fn.Params))
	for _, param := range fn.Params {
		paramTypes[param.Name] = param.Type
	}
	counts := make(map[string]int)
	types := make(map[string]string)
	addressed := make(map[string]bool)
	order := make([]string, 0)
	seen := make(map[string]bool)
	recordPlace := func(place *mir.Place) {
		if !placeNeedsRootAddr(place) {
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
			if typ := mirValueType(assign.Value); typ != "" {
				types[assign.Name] = typ
			}
			switch value := assign.Value.(type) {
			case *mir.AddrOf:
				recordPlace(value.Place)
			case *mir.Load:
				recordPlace(value.Place)
			case *mir.SliceView:
				if value.Source == nil {
					continue
				}
				if len(value.Source.Projections) > 0 {
					recordPlace(value.Source)
					continue
				}
				if _, reference := referenceTypeTextTarget(value.Source.Type); reference {
					continue
				}
				if _, _, fixed := ir.ArrayTypeParts(value.Source.Type); fixed {
					recordPlace(value.Source)
				}
			}
		}
	}
	slots := make([]stackLocalSlot, 0)
	for _, name := range order {
		typ := types[name]
		if typ == "" {
			typ = paramTypes[name]
		}
		if typ == "" {
			continue
		}
		if counts[name] > 1 || paramTypes[name] != "" || addressed[name] {
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
		llvmType := b.emitter.llvmType(slot.Type)
		ptr := b.nextReg()
		b.line(fmt.Sprintf("%s = alloca %s", ptr, llvmType))
		if paramValue, ok := b.locals[slot.Name]; ok && paramValue != "" {
			b.line(fmt.Sprintf("store %s %s, %s* %s", llvmType, paramValue, llvmType, ptr))
		}
		b.localPtrs[slot.Name] = ptr
		b.localTypes[slot.Name] = slot.Type
	}
}
