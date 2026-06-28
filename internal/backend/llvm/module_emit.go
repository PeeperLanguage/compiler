package llvm

import (
	"fmt"
	"strings"

	"compiler/internal/diagnostics"
	"compiler/internal/ir"
	"compiler/internal/ir/mir"
)

// GenerateLLVMIR is backend entrypoint.
// It emits module text in LLVM order: static data, helper itabs, declarations,
// thunks, then function bodies. It also keeps one emitter state object so type
// lowering failures and deferred external globals are reported consistently.
func GenerateLLVMIR(mod *mir.Module, diag *diagnostics.DiagnosticBag, targetTriple string, debugBuild bool, targetOS string) string {
	if mod == nil {
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
				hasItab = true

				b.WriteString(itabSym)
				fmt.Fprintf(&b, " = private constant [%d x i8*] [", len(makeVal.Slots))
				for i, slot := range makeVal.Slots {
					if i > 0 {
						b.WriteString(", ")
					}
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
	for _, fn := range mod.Funcs {
		if fn == nil || fn.Blocks != nil {
			continue
		}
		hasDecl = true
		b.WriteString("declare ")
		b.WriteString(emitter.llvmType(llvmFunctionReturnType(fn)))
		b.WriteString(" @")
		b.WriteString(ir.SanitizeSymbolName(fn.Name))
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
	if usesInterfaceBoxing(mod) {
		b.WriteString("declare i8* @malloc(i64)\n\n")
	}

	decls := collectCallDecls(mod)
	for _, decl := range decls {
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
	if len(mod.InterfaceThunks) > 0 {
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
			fmt.Fprintf(&b, "b%d:\n", block.ID)
			if block.ID == fn.EntryID {
				emitStackLocalSlots(lb, stackSlots)
			}
			for _, instr := range block.Instrs {
				lb.setLocation(mir.InstrLocation(instr))
				if assign, ok := instr.(*mir.Assign); ok && assign != nil {
					val := emitValueExpr(lb, assign.Value)
					valueType := mirValueType(assign.Value)
					if ptr, ok := lb.localPtrs[assign.Name]; ok && ptr != "" {
						llvmType := lb.types.llvmType(lb.localTypes[assign.Name])
						lb.line(fmt.Sprintf("store %s %s, %s* %s", llvmType, val, llvmType, ptr))
					} else {
						lb.locals[assign.Name] = val
						if valueType != "" {
							lb.localTypes[assign.Name] = valueType
						}
					}
					continue
				}
				if store, ok := instr.(*mir.StoreField); ok && store != nil {
					emitStoreField(lb, store)
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
	order := make([]string, 0)
	seen := make(map[string]bool)
	for _, block := range fn.Blocks {
		if block == nil {
			continue
		}
		for _, instr := range block.Instrs {
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
		if counts[name] > 1 || paramTypes[name] != "" {
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
		llvmType := b.types.llvmType(slot.Type)
		ptr := b.nextReg()
		b.line(fmt.Sprintf("%s = alloca %s", ptr, llvmType))
		if paramValue, ok := b.locals[slot.Name]; ok && paramValue != "" {
			b.line(fmt.Sprintf("store %s %s, %s* %s", llvmType, paramValue, llvmType, ptr))
		}
		b.localPtrs[slot.Name] = ptr
		b.localTypes[slot.Name] = slot.Type
	}
}
