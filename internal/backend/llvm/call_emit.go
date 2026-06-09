package llvm

import (
	"fmt"
	"strconv"
	"strings"

	"compiler/internal/ir"
	"compiler/internal/ir/mir"
)

// emitInterfaceThunk builds adapter function used by interface dispatch tables.
// Interface slots always receive `i8*` data first, but concrete methods may
// expect either pointer receiver or loaded value receiver. This thunk reshapes
// that ABI once, so runtime dispatch can call every implementation uniformly.
func emitInterfaceThunk(out *strings.Builder, emitter *llvmEmitter, thunk *mir.InterfaceThunk) {
	if out == nil || emitter == nil || thunk == nil {
		return
	}
	actualLLVMType, ok := llvmTypeName(thunk.FuncType)
	if !ok {
		emitter.markInvalid("unsupported interface thunk function type: " + thunk.FuncType)
		return
	}
	actualFn, actualRet, actualParams, ok := parseFunctionTypeText(thunk.FuncType)
	if !ok || actualFn != actualLLVMType {
		emitter.markInvalid("failed to parse interface thunk function type: " + thunk.FuncType)
		return
	}
	dataLLVMType, ok := llvmTypeName(thunk.DataType)
	if !ok {
		emitter.markInvalid("unsupported interface thunk data type: " + thunk.DataType)
		return
	}
	_, slotRet, slotParams, ok := parseFunctionTypeText(thunk.SlotType)
	if !ok || len(slotParams) == 0 || len(actualParams) == 0 {
		emitter.markInvalid("failed to parse interface thunk slot type: " + thunk.SlotType)
		return
	}
	out.WriteString("define ")
	out.WriteString(slotRet)
	out.WriteString(" @")
	out.WriteString(thunk.Name)
	out.WriteString("(")
	for i, param := range slotParams {
		if i > 0 {
			out.WriteString(", ")
		}
		out.WriteString(param)
		out.WriteString(" %p")
		out.WriteString(strconv.Itoa(i))
	}
	out.WriteString(") {\n")
	builder := newLLVMBuilder(out, emitter)
	if strings.HasSuffix(actualParams[0], "*") {
		cast := builder.nextReg()
		builder.line(fmt.Sprintf("%s = bitcast i8* %%p0 to %s", cast, actualParams[0]))
		callArgs := []string{actualParams[0] + " " + cast}
		for i := 1; i < len(slotParams); i++ {
			callArgs = append(callArgs, actualParams[i]+" %p"+strconv.Itoa(i))
		}
		emitThunkTargetCall(builder, actualRet, thunk.FuncName, callArgs)
	} else {
		cast := builder.nextReg()
		builder.line(fmt.Sprintf("%s = bitcast i8* %%p0 to %s*", cast, dataLLVMType))
		loaded := builder.nextReg()
		builder.line(fmt.Sprintf("%s = load %s, %s* %s", loaded, dataLLVMType, dataLLVMType, cast))
		callArgs := []string{actualParams[0] + " " + loaded}
		for i := 1; i < len(slotParams); i++ {
			callArgs = append(callArgs, actualParams[i]+" %p"+strconv.Itoa(i))
		}
		emitThunkTargetCall(builder, actualRet, thunk.FuncName, callArgs)
	}
	out.WriteString("}\n")
}

// emitThunkTargetCall finishes thunk body after receiver reshaping.
// Thunks are single-call adapter functions, so this helper emits both the call
// and the exact return form that matches target function result type.
func emitThunkTargetCall(builder *llvmBuilder, returnType string, funcName string, callArgs []string) {
	if builder == nil {
		return
	}
	callee := "@" + ir.SanitizeSymbolName(ir.StripSymbolInstance(funcName))
	result := ""
	if returnType != "void" {
		result = builder.nextReg()
	}
	emitCall(builder, result, returnType, callee, callArgs)
	if returnType == "void" {
		builder.line("ret void")
		return
	}
	builder.line("ret " + returnType + " " + result)
}

// llvmCallArgs converts MIR operands into `type value` pairs expected by LLVM
// call syntax. Centralizing this avoids repeating operand formatting in direct,
// interface, and thunk call paths.
func llvmCallArgs(b *llvmBuilder, args []mir.ValueRef) []string {
	callArgs := make([]string, 0, len(args))
	for _, arg := range args {
		callArgs = append(callArgs, b.types.llvmType(mirRefType(arg))+" "+emitRef(b, arg))
	}
	return callArgs
}

// emitCall emits both call forms used in this backend:
// `call ...` for side-effect-only statements and `%t = call ...` for
// value-producing expressions. Empty result means statement form.
func emitCall(b *llvmBuilder, result string, returnType string, callee string, callArgs []string) {
	if b == nil {
		return
	}
	callText := fmt.Sprintf("call %s %s(%s)", returnType, callee, strings.Join(callArgs, ", "))
	if result == "" {
		b.line(callText)
		return
	}
	b.line(result + " = " + callText)
}

// emitInterfaceCallTarget performs interface dispatch lookup.
// It extracts boxed data pointer and itab pointer from interface value, loads
// function pointer from requested slot, then bitcasts it to callable LLVM type.
// Callers reuse this for both expression-form and discarded-result calls.
func emitInterfaceCallTarget(b *llvmBuilder, base mir.ValueRef, slot int) (data string, fn string, ok bool) {
	if b == nil || base == nil {
		return "", "", false
	}
	baseValue := emitRef(b, base)
	baseType := b.types.llvmType(mirRefType(base))
	data = b.nextReg()
	b.line(fmt.Sprintf("%s = extractvalue %s %s, 0", data, baseType, baseValue))
	itab := b.nextReg()
	b.line(fmt.Sprintf("%s = extractvalue %s %s, 1", itab, baseType, baseValue))
	slotType, ok := interfaceSlotLLVMTypeFromInterface(mirRefType(base), slot)
	if !ok {
		return "", "", false
	}
	vtable := b.nextReg()
	b.line(fmt.Sprintf("%s = bitcast i8* %s to i8**", vtable, itab))
	fnPtrPtr := b.nextReg()
	b.line(fmt.Sprintf("%s = getelementptr inbounds i8*, i8** %s, i32 %d", fnPtrPtr, vtable, slot))
	fnI8 := b.nextReg()
	b.line(fmt.Sprintf("%s = load i8*, i8** %s", fnI8, fnPtrPtr))
	fn = b.nextReg()
	b.line(fmt.Sprintf("%s = bitcast i8* %s to %s", fn, fnI8, slotType))
	return data, fn, true
}

// emitDiscardedCall handles statement-form direct calls such as `foo();`.
// MIR represents these as plain call instructions, not assignments, so backend
// must emit the side effect even though no SSA result is bound.
func emitDiscardedCall(b *llvmBuilder, call *mir.Call) {
	if b == nil || call == nil {
		return
	}
	emitCall(b, "", b.types.llvmType(call.Type), emitRef(b, call.Callee), llvmCallArgs(b, call.Args))
}

// emitDiscardedInterfaceCall handles statement-form interface calls such as
// `writer.write(msg);` where dispatch side effects matter but result is unused.
func emitDiscardedInterfaceCall(b *llvmBuilder, call *mir.InterfaceCall) {
	if b == nil || call == nil {
		return
	}
	data, fn, ok := emitInterfaceCallTarget(b, call.Base, call.Slot)
	if !ok {
		return
	}
	args := append([]string{"i8* " + data}, llvmCallArgs(b, call.Args)...)
	emitCall(b, "", b.types.llvmType(call.Type), fn, args)
}
