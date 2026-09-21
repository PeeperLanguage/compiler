package typelower

import (
	"compiler/internal/ir"
	"compiler/internal/semantics/typeinfo"
)

type runtimeTypeVisitor struct {
	lowerer *runtimeTypeInterner
	defined *typeinfo.DefinedType
	result  ir.TypeID
}

func (v *runtimeTypeVisitor) VisitInvalid(*typeinfo.InvalidType) { v.result = ir.InvalidType }
func (v *runtimeTypeVisitor) VisitUnknown(*typeinfo.UnknownType) { v.result = ir.InvalidType }
func (v *runtimeTypeVisitor) VisitTypeParameter(*typeinfo.TypeParameterType) {
	v.result = ir.InvalidType
}
func (v *runtimeTypeVisitor) VisitByte(*typeinfo.ByteType) {
	v.result = v.lowerer.ctx.types.Intern(ir.Type{Kind: ir.TypeByte})
}
func (v *runtimeTypeVisitor) VisitChar(*typeinfo.CharType) {
	v.result = v.lowerer.ctx.types.Intern(ir.Type{Kind: ir.TypeChar})
}
func (v *runtimeTypeVisitor) VisitBool(*typeinfo.BoolType) {
	v.result = v.lowerer.ctx.types.Intern(ir.Type{Kind: ir.TypeBool})
}
func (v *runtimeTypeVisitor) VisitCStr(*typeinfo.CStrType) {
	v.result = v.lowerer.ctx.types.Intern(ir.Type{Kind: ir.TypeCStr})
}
func (v *runtimeTypeVisitor) VisitString(*typeinfo.StringType) {
	v.result = v.lowerer.ctx.types.Intern(ir.Type{Kind: ir.TypeString})
}
func (v *runtimeTypeVisitor) VisitNone(*typeinfo.NoneType) {
	v.result = v.lowerer.ctx.types.Intern(ir.Type{Kind: ir.TypeVoid})
}
func (v *runtimeTypeVisitor) VisitAllocator(*typeinfo.AllocatorType) {
	v.result = v.lowerer.ctx.types.Intern(ir.Type{Kind: ir.TypeAllocator})
}
func (v *runtimeTypeVisitor) VisitRawPointer(*typeinfo.RawPtrType) {
	v.result = v.lowerer.ctx.types.Intern(ir.Type{Kind: ir.TypeRawPtr})
}
func (v *runtimeTypeVisitor) VisitNamed(*typeinfo.NamedType) {
	v.result = v.lowerer.invalid("unresolved named type reached IR lowering")
}
func (v *runtimeTypeVisitor) VisitEnum(t *typeinfo.EnumType) {
	if t == nil || v.defined == nil {
		v.result = v.lowerer.invalid("named variant reached IR lowering without declaration identity")
		return
	}
	descriptor, ok := typeinfo.VariantDescriptorOf(v.defined)
	if !ok {
		v.result = v.lowerer.invalid("invalid named variant reached IR lowering")
		return
	}
	shell := ir.Type{Kind: ir.TypeVariant, Family: ir.VariantFamilyNamed, Name: v.defined.Name, Identity: descriptor.Identity}
	v.result = v.lowerer.internNamed(shell, func() (ir.Type, bool) {
		cases := make([]ir.VariantCase, len(descriptor.Cases))
		valid := true
		for index, variantCase := range descriptor.Cases {
			cases[index].Name = variantCase.Name
			if variantCase.Payload != nil {
				payload := v.lowerer.intern(variantCase.Payload)
				valid = valid && payload != ir.InvalidType
				cases[index].Payload = payload
			}
		}
		shell.Cases = cases
		return shell, valid
	})
}

func (v *runtimeTypeVisitor) VisitInteger(t *typeinfo.IntegerType) {
	if t != nil {
		v.result = v.lowerer.ctx.types.Intern(ir.Type{Kind: ir.TypeInteger, Signed: t.Signed, Bits: t.Bits})
	}
}
func (v *runtimeTypeVisitor) VisitFloat(t *typeinfo.FloatType) {
	if t != nil {
		v.result = v.lowerer.ctx.types.Intern(ir.Type{Kind: ir.TypeFloat, Bits: t.Bits})
	}
}
func (v *runtimeTypeVisitor) VisitDefined(t *typeinfo.DefinedType) {
	if t == nil || t.Underlying == nil {
		v.result = v.lowerer.invalid("incomplete named type reached IR lowering")
		return
	}
	nested := runtimeTypeVisitor{lowerer: v.lowerer, defined: t, result: ir.InvalidType}
	t.Underlying.VisitRuntimeType(&nested)
	v.result = nested.result
}
func (v *runtimeTypeVisitor) VisitOwnedPointer(t *typeinfo.OwnedPtrType) {
	if t != nil {
		v.result = v.lowerer.ctx.types.Intern(ir.Type{Kind: ir.TypeOwnedPtr, Elem: v.lowerer.intern(t.Target)})
	}
}
func (v *runtimeTypeVisitor) VisitReference(t *typeinfo.RefType) {
	if t != nil {
		v.result = v.lowerer.ctx.types.Intern(ir.Type{Kind: ir.TypeReference, Mutable: t.Mutable, Elem: v.lowerer.intern(t.Target)})
	}
}
func (v *runtimeTypeVisitor) VisitOptional(t *typeinfo.OptionalType) {
	if t != nil {
		v.result = v.lowerer.ctx.types.Intern(ir.OptionalVariant(v.lowerer.intern(t.Inner)))
	}
}
func (v *runtimeTypeVisitor) VisitArray(t *typeinfo.ArrayType) {
	if t == nil {
		return
	}
	elem := v.lowerer.intern(t.Elem)
	if t.Shape == typeinfo.ArraySlice {
		v.result = v.lowerer.ctx.types.Intern(ir.Type{Kind: ir.TypeSlice, Elem: elem})
		return
	}
	v.result = v.lowerer.ctx.types.Intern(ir.Type{Kind: ir.TypeArray, Length: t.Len, Elem: elem})
}
func (v *runtimeTypeVisitor) VisitStruct(t *typeinfo.StructType) {
	if t == nil {
		return
	}
	if v.defined != nil {
		identity := v.defined.Identity
		if identity == "" {
			identity = v.defined.Name
		}
		shell := ir.Type{Kind: ir.TypeStruct, Name: v.defined.Name, Identity: identity}
		v.result = v.lowerer.internNamed(shell, func() (ir.Type, bool) {
			fields := make([]ir.TypeField, 0, len(t.Fields))
			valid := true
			for _, field := range t.Fields {
				fieldType := v.lowerer.intern(field.Type)
				valid = valid && fieldType != ir.InvalidType
				fields = append(fields, ir.TypeField{Name: field.Name, Type: fieldType})
			}
			shell.Fields = fields
			return shell, valid
		})
		return
	}
	fields := make([]ir.TypeField, 0, len(t.Fields))
	for _, field := range t.Fields {
		fields = append(fields, ir.TypeField{Name: field.Name, Type: v.lowerer.intern(field.Type)})
	}
	v.result = v.lowerer.ctx.types.Intern(ir.Type{Kind: ir.TypeStruct, Fields: fields})
}
func (v *runtimeTypeVisitor) VisitInterface(t *typeinfo.InterfaceType) {
	if t == nil {
		return
	}
	methods := make([]ir.TypeMethod, 0, len(t.Methods))
	for _, method := range t.Methods {
		receiver := ir.MethodReceiverInvalid
		switch method.Receiver {
		case typeinfo.MethodReceiverValue:
			receiver = ir.MethodReceiverValue
		case typeinfo.MethodReceiverShared:
			receiver = ir.MethodReceiverShared
		case typeinfo.MethodReceiverMutable:
			receiver = ir.MethodReceiverMutable
		}
		if receiver == ir.MethodReceiverInvalid {
			v.result = v.lowerer.invalid("interface method reached IR lowering with invalid receiver")
			return
		}
		params := make([]ir.TypeField, 0, len(method.Params))
		slotParams := []ir.TypeID{v.lowerer.ctx.types.Intern(ir.Type{Kind: ir.TypeRawPtr})}
		for _, param := range method.Params {
			paramType := v.lowerer.intern(param.Type)
			if paramType == ir.InvalidType {
				v.result = v.lowerer.invalid("interface method reached IR lowering with invalid parameter type")
				return
			}
			params = append(params, ir.TypeField{Name: param.Name, Type: paramType})
			slotParams = append(slotParams, paramType)
		}
		returnType := v.lowerer.intern(method.Return)
		if returnType == ir.InvalidType {
			returnType = v.lowerer.ctx.types.Intern(ir.Type{Kind: ir.TypeVoid})
		}
		slotType := v.lowerer.ctx.types.Intern(ir.Type{Kind: ir.TypeFunction, Params: slotParams, Return: returnType})
		methods = append(methods, ir.TypeMethod{Name: method.Name, Receiver: receiver, Params: params, Return: returnType, SlotType: slotType})
	}
	v.result = v.lowerer.ctx.types.Intern(ir.Type{Kind: ir.TypeInterface, Methods: methods})
}
func (v *runtimeTypeVisitor) VisitFunction(t *typeinfo.FuncType) {
	if t == nil {
		return
	}
	params := make([]ir.TypeID, 0, len(t.Params))
	for _, param := range t.Params {
		params = append(params, v.lowerer.intern(param))
	}
	returnType := v.lowerer.intern(t.Return)
	if returnType == ir.InvalidType {
		returnType = v.lowerer.ctx.types.Intern(ir.Type{Kind: ir.TypeVoid})
	}
	v.result = v.lowerer.ctx.types.Intern(ir.Type{Kind: ir.TypeFunction, Params: params, Return: returnType})
}
