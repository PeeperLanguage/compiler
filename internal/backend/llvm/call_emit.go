package llvm

import (
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
	actualLayout := emitter.layout(thunk.FuncType)
	if actualLayout == nil || actualLayout.Kind != llvmLayoutFunction || len(actualLayout.Parameters) == 0 {
		emitter.markInvalid("unsupported interface thunk function type: " + emitter.mod.Types.Text(thunk.FuncType))
		return
	}
	if emitter.layout(thunk.DataType) == nil {
		emitter.markInvalid("unsupported interface thunk data type: " + emitter.mod.Types.Text(thunk.DataType))
		return
	}
	slotLayout := emitter.layout(thunk.SlotType)
	if slotLayout == nil || slotLayout.Kind != llvmLayoutFunction || len(slotLayout.Parameters) == 0 {
		emitter.markInvalid("failed to lower interface thunk slot type: " + emitter.mod.Types.Text(thunk.SlotType))
		return
	}
	if slotLayout.Parameters[0].Text != "i8*" {
		emitter.markInvalid("interface thunk receiver slot must use rawptr: " + emitter.mod.Types.Text(thunk.SlotType))
		return
	}
	out.WriteString("define ")
	out.WriteString(slotLayout.Return.Text)
	out.WriteString(" @")
	out.WriteString(thunk.Name)
	out.WriteString("(")
	for i, param := range slotLayout.Parameters {
		if i > 0 {
			out.WriteString(", ")
		}
		out.WriteString(param.Text)
		out.WriteString(" %p")
		out.WriteString(strconv.Itoa(i))
	}
	out.WriteString(") {\n")
	builder := newLLVMBuilder(out, emitter, -1)
	rawReceiver := builder.value("%p0", slotLayout.Parameters[0])
	callArgs := make([]llvmValue, 0, len(actualLayout.Parameters))
	if llvmPointerLike(actualLayout.Parameters[0]) {
		callArgs = append(callArgs, builder.bitcast(rawReceiver, actualLayout.Parameters[0]))
	} else {
		receiverPtr := builder.bitcast(rawReceiver, llvmPointerLayout(actualLayout.Parameters[0]))
		callArgs = append(callArgs, builder.load(builder.pointerPlace(receiverPtr)))
	}
	for i := 1; i < len(slotLayout.Parameters); i++ {
		if i >= len(actualLayout.Parameters) || !llvmLayoutsMatch(slotLayout.Parameters[i], actualLayout.Parameters[i]) {
			emitter.markInvalid("interface thunk parameter layout mismatch: " + emitter.mod.Types.Text(thunk.SlotType))
			return
		}
		callArgs = append(callArgs, builder.value("%p"+strconv.Itoa(i), actualLayout.Parameters[i]))
	}
	callee := builder.value("@"+ir.SanitizeSymbolName(ir.StripSymbolInstance(thunk.FuncName)), actualLayout)
	result := builder.call(callee, callArgs)
	if actualLayout.Return.Kind == llvmLayoutVoid {
		builder.retVoid(actualLayout.Return)
	} else {
		builder.ret(result, actualLayout.Return)
	}
	out.WriteString("}\n")
}

// emitInterfaceCallTarget performs interface dispatch lookup.
// It extracts data pointer and itab pointer from interface value, loads
// function pointer from requested slot, then bitcasts it to callable LLVM type.
// Callers reuse this for both expression-form and discarded-result calls.
func emitInterfaceCallTarget(b *llvmBuilder, base mir.ValueRef, slot int) (llvmValue, llvmValue, bool) {
	if b == nil || base == nil {
		return llvmValue{}, llvmValue{}, false
	}
	baseValue := emitRef(b, base)
	data := b.extractField(baseValue, llvmFieldData)
	itab := b.extractField(baseValue, llvmFieldDispatch)
	slotLayout, ok := interfaceSlotLLVMLayout(b.emitter.mod.Types, mirRefType(base), slot)
	if !ok {
		return llvmValue{}, llvmValue{}, false
	}
	rawPointer := llvmPointerLayout(llvmScalarLayout("i8"))
	vtable := b.bitcast(itab, llvmPointerLayout(rawPointer))
	methodOffset := interfaceMethodVtableSlotID(b.emitter.mod.Types, mirRefType(base), slot)
	fnPtrPtr := b.gep(b.pointerPlace(vtable), b.value(strconv.Itoa(methodOffset), llvmScalarLayout("i32")), true)
	fnI8 := b.load(fnPtrPtr)
	fn := b.bitcast(fnI8, slotLayout)
	return data, fn, true
}

// emitDiscardedCall handles statement-form direct calls such as `foo();`.
// MIR represents these as plain call instructions, not assignments, so backend
// must emit the side effect even though no SSA result is bound.
func emitDiscardedCall(b *llvmBuilder, call *mir.Call) {
	if b == nil || call == nil {
		return
	}
	args := make([]llvmValue, len(call.Args))
	for i, arg := range call.Args {
		args[i] = emitRef(b, arg)
	}
	callee := emitRef(b, call.Callee)
	if callee.Layout.Kind != llvmLayoutFunction {
		b.emitter.markInvalid("call reached LLVM without function type")
		return
	}
	b.call(callee, args)
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
	args := make([]llvmValue, 1, len(call.Args)+1)
	args[0] = data
	for _, arg := range call.Args {
		args = append(args, emitRef(b, arg))
	}
	b.call(fn, args)
	if consumesOwnedInterfaceStorage(b.emitter.mod.Types, call) {
		emitInterfaceStorageRelease(b, mirRefType(call.Base), emitRef(b, call.Base), data)
	}
}

func consumesOwnedInterfaceStorage(types *ir.TypeTable, call *mir.InterfaceCall) bool {
	if call == nil || !call.Consumes {
		return false
	}
	return isOwnedInterfaceType(types, mirRefType(call.Base))
}
