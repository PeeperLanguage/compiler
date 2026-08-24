package lower

import (
	"compiler/internal/diagnostics"
	"compiler/internal/ir"
	"compiler/internal/project"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
)

func loweredTypeID(ctx *project.CompilerContext, module *project.Module, t typeinfo.Type) ir.TypeID {
	if ctx == nil || ctx.Types == nil || t == nil {
		return ir.InvalidType
	}
	interner := runtimeTypeInterner{
		ctx: ctx, module: module, active: make(map[string]ir.TypeID),
	}
	return interner.intern(t)
}

func loweredReturnTypeID(ctx *project.CompilerContext, module *project.Module, t typeinfo.Type) ir.TypeID {
	if t == nil {
		return ctx.Types.Intern(ir.Type{Kind: ir.TypeVoid})
	}
	return loweredTypeID(ctx, module, t)
}

// runtimeTypeInterner is semantic-to-IR type construction state. Named
// composites reserve identity before child descent so legal pointer/reference
// recursion closes on one canonical TypeID.
type runtimeTypeInterner struct {
	ctx    *project.CompilerContext
	module *project.Module
	active map[string]ir.TypeID
}

func (l *runtimeTypeInterner) intern(t typeinfo.Type) ir.TypeID {
	if l == nil || l.ctx == nil || l.ctx.Types == nil || t == nil {
		return ir.InvalidType
	}
	if l.module != nil {
		t = resolveNamedType(l.module.ModuleScope, t)
	}
	if defined, ok := t.(*typeinfo.DefinedType); ok {
		return l.internDefined(defined)
	}
	if descriptor, ok := typeinfo.VariantDescriptorOf(t); ok {
		cases := make([]ir.VariantCase, len(descriptor.Cases))
		for i, variantCase := range descriptor.Cases {
			cases[i].Name = variantCase.Name
			if variantCase.Payload != nil {
				cases[i].Payload = l.intern(variantCase.Payload)
			}
		}
		if descriptor.Family == typeinfo.VariantFamilyOptional {
			return l.ctx.Types.Intern(ir.OptionalVariant(cases[ir.OptionalPresentCase].Payload))
		}
		return l.invalid("named variant reached IR lowering without declaration identity")
	}
	switch typ := typeinfo.Underlying(t).(type) {
	case *typeinfo.InvalidType, *typeinfo.UnknownType:
		return ir.InvalidType
	case *typeinfo.IntegerType:
		if typ == nil {
			return ir.InvalidType
		}
		return l.ctx.Types.Intern(ir.Type{Kind: ir.TypeInteger, Signed: typ.Signed, Bits: typ.Bits})
	case *typeinfo.ByteType:
		return l.ctx.Types.Intern(ir.Type{Kind: ir.TypeByte})
	case *typeinfo.CharType:
		return l.ctx.Types.Intern(ir.Type{Kind: ir.TypeChar})
	case *typeinfo.FloatType:
		if typ == nil {
			return ir.InvalidType
		}
		return l.ctx.Types.Intern(ir.Type{Kind: ir.TypeFloat, Bits: typ.Bits})
	case *typeinfo.BoolType:
		return l.ctx.Types.Intern(ir.Type{Kind: ir.TypeBool})
	case *typeinfo.CStrType:
		return l.ctx.Types.Intern(ir.Type{Kind: ir.TypeCStr})
	case *typeinfo.StringType:
		return l.ctx.Types.Intern(ir.Type{Kind: ir.TypeString})
	case *typeinfo.NoneType:
		return l.ctx.Types.Intern(ir.Type{Kind: ir.TypeVoid})
	case *typeinfo.AllocatorType:
		return l.ctx.Types.Intern(ir.Type{Kind: ir.TypeAllocator})
	case *typeinfo.NamedType:
		return l.invalid("unresolved named type reached IR lowering")
	case *typeinfo.OwnedPtrType:
		if typ == nil {
			return ir.InvalidType
		}
		return l.ctx.Types.Intern(ir.Type{Kind: ir.TypeOwnedPtr, Elem: l.intern(typ.Target)})
	case *typeinfo.RawPtrType:
		return l.ctx.Types.Intern(ir.Type{Kind: ir.TypeRawPtr})
	case *typeinfo.RefType:
		if typ == nil {
			return ir.InvalidType
		}
		return l.ctx.Types.Intern(ir.Type{Kind: ir.TypeReference, Mutable: typ.Mutable, Elem: l.intern(typ.Target)})
	case *typeinfo.ArrayType:
		if typ == nil {
			return ir.InvalidType
		}
		if typ.Shape == typeinfo.ArraySlice {
			return l.ctx.Types.Intern(ir.Type{Kind: ir.TypeSlice, Elem: l.intern(typ.Elem)})
		}
		return l.ctx.Types.Intern(ir.Type{Kind: ir.TypeArray, Length: typ.Len, Elem: l.intern(typ.Elem)})
	case *typeinfo.StructType:
		if typ == nil {
			return ir.InvalidType
		}
		fields := make([]ir.TypeField, 0, len(typ.Fields))
		for _, field := range typ.Fields {
			fields = append(fields, ir.TypeField{Name: field.Name, Type: l.intern(field.Type)})
		}
		return l.ctx.Types.Intern(ir.Type{Kind: ir.TypeStruct, Fields: fields})
	case *typeinfo.InterfaceType:
		if typ == nil {
			return ir.InvalidType
		}
		methods := make([]ir.TypeMethod, 0, len(typ.Methods))
		for _, method := range typ.Methods {
			if len(method.Params) == 0 {
				return l.invalid("interface method reached IR lowering without receiver")
			}
			receiver := ir.MethodReceiverInvalid
			switch semanticReceiver := typeinfo.Underlying(method.Params[0].Type).(type) {
			case *typeinfo.NamedType:
				if semanticReceiver != nil && semanticReceiver.Name == "Self" {
					receiver = ir.MethodReceiverValue
				}
			case *typeinfo.RefType:
				if semanticReceiver == nil {
					break
				}
				self, selfOK := typeinfo.Underlying(semanticReceiver.Target).(*typeinfo.NamedType)
				if selfOK && self != nil && self.Name == "Self" {
					receiver = ir.MethodReceiverShared
					if semanticReceiver.Mutable {
						receiver = ir.MethodReceiverMutable
					}
				}
			}
			if receiver == ir.MethodReceiverInvalid {
				return l.invalid("interface method reached IR lowering with invalid receiver")
			}
			params := make([]ir.TypeField, 0, len(method.Params)-1)
			for _, param := range method.Params[1:] {
				params = append(params, ir.TypeField{Name: param.Name, Type: l.intern(param.Type)})
			}
			returnType := l.intern(method.Return)
			if returnType == ir.InvalidType {
				returnType = l.ctx.Types.Intern(ir.Type{Kind: ir.TypeVoid})
			}
			methods = append(methods, ir.TypeMethod{Name: method.Name, Receiver: receiver, Params: params, Return: returnType})
		}
		return l.ctx.Types.Intern(ir.Type{Kind: ir.TypeInterface, Methods: methods})
	case *typeinfo.FuncType:
		if typ == nil {
			return ir.InvalidType
		}
		params := make([]ir.TypeID, 0, len(typ.Params))
		for _, param := range typ.Params {
			params = append(params, l.intern(param))
		}
		returnType := l.intern(typ.Return)
		if returnType == ir.InvalidType {
			returnType = l.ctx.Types.Intern(ir.Type{Kind: ir.TypeVoid})
		}
		return l.ctx.Types.Intern(ir.Type{Kind: ir.TypeFunction, Params: params, Return: returnType})
	default:
		return ir.InvalidType
	}
}

func (l *runtimeTypeInterner) internDefined(defined *typeinfo.DefinedType) ir.TypeID {
	if defined == nil || defined.Underlying == nil {
		return l.invalid("incomplete named type reached IR lowering")
	}
	identity := defined.Identity
	if identity == "" {
		identity = defined.Name
	}
	switch underlying := defined.Underlying.(type) {
	case *typeinfo.StructType:
		shell := ir.Type{Kind: ir.TypeStruct, Name: defined.Name, Identity: identity}
		return l.internNamed(shell, func() (ir.Type, bool) {
			fields := make([]ir.TypeField, 0, len(underlying.Fields))
			valid := true
			for _, field := range underlying.Fields {
				fieldType := l.intern(field.Type)
				valid = valid && fieldType != ir.InvalidType
				fields = append(fields, ir.TypeField{Name: field.Name, Type: fieldType})
			}
			shell.Fields = fields
			return shell, valid
		})
	case *typeinfo.EnumType:
		descriptor, ok := typeinfo.VariantDescriptorOf(defined)
		if !ok {
			return l.invalid("invalid named variant reached IR lowering")
		}
		shell := ir.Type{
			Kind: ir.TypeVariant, Family: ir.VariantFamilyNamed,
			Name: defined.Name, Identity: descriptor.Identity,
		}
		return l.internNamed(shell, func() (ir.Type, bool) {
			cases := make([]ir.VariantCase, len(descriptor.Cases))
			valid := true
			for index, variantCase := range descriptor.Cases {
				cases[index].Name = variantCase.Name
				if variantCase.Payload != nil {
					payload := l.intern(variantCase.Payload)
					valid = valid && payload != ir.InvalidType
					cases[index].Payload = payload
				}
			}
			shell.Cases = cases
			return shell, valid
		})
	default:
		return l.intern(defined.Underlying)
	}
}

func (l *runtimeTypeInterner) internNamed(shell ir.Type, descriptor func() (ir.Type, bool)) ir.TypeID {
	id, err := l.ctx.Types.ReserveNamed(shell)
	if err != nil {
		return l.invalid(err.Error())
	}
	if _, complete := l.ctx.Types.Type(id); complete {
		return id
	}
	key := l.ctx.Types.ABIKey(id)
	if activeID, active := l.active[key]; active {
		return activeID
	}
	l.active[key] = id
	defer delete(l.active, key)
	typ, valid := descriptor()
	if !valid {
		return l.invalid("named type " + shell.Name + " has invalid runtime descriptor")
	}
	if err := l.ctx.Types.CompleteNamed(id, typ); err != nil {
		return l.invalid(err.Error())
	}
	return id
}

func (l *runtimeTypeInterner) invalid(message string) ir.TypeID {
	if l != nil && l.ctx != nil && l.ctx.Diagnostics != nil {
		l.ctx.Diagnostics.Add(diagnostics.NewError(message).WithCode(diagnostics.ErrInvalidType))
	}
	return ir.InvalidType
}

// resolveNamedType performs a single-hop scope lookup for a NamedType so the
// lowerer can collapse source-level aliases before runtime layout work.
// Runtime normalization and IR interning share it here to avoid importing
// symbol tables from the leaf typeinfo package.
func resolveNamedType(scope *symbols.Scope, t typeinfo.Type) typeinfo.Type {
	if scope == nil || t == nil {
		return t
	}
	named, ok := t.(*typeinfo.NamedType)
	if !ok || named == nil {
		return t
	}
	sym, found := scope.Lookup(named.Name)
	if found && sym != nil && sym.Kind == symbols.SymbolType {
		if resolved, ok := symbols.GetSymbolType(sym); ok && resolved != nil {
			return resolved
		}
	}
	return t
}

// loweredRuntimeType strips semantic-only named layers and preserves recursive
// shells so MIR sees runtime layout, not source-level aliases.
func loweredRuntimeType(module *project.Module, t typeinfo.Type, seen map[*typeinfo.DefinedType]struct{}) typeinfo.Type {
	if seen == nil {
		seen = make(map[*typeinfo.DefinedType]struct{})
	}
	if t == nil {
		return nil
	}
	if module != nil {
		t = resolveNamedType(module.ModuleScope, t)
	}
	switch typ := t.(type) {
	case *typeinfo.DefinedType:
		if typ == nil {
			return nil
		}
		if _, ok := seen[typ]; ok {
			// Stop self-recursive expansion once shell already seen.
			return &typeinfo.NamedType{Name: typ.Name}
		}
		seen[typ] = struct{}{}
		defer delete(seen, typ)
		if enum, ok := typeinfo.Underlying(typ.Underlying).(*typeinfo.EnumType); ok {
			return &typeinfo.DefinedType{Name: typ.Name, Identity: typ.Identity, Underlying: enum}
		}
		return loweredRuntimeType(module, typ.Underlying, seen)
	case *typeinfo.OwnedPtrType:
		if typ == nil {
			return nil
		}
		return &typeinfo.OwnedPtrType{Target: loweredRuntimeType(module, typ.Target, seen)}
	case *typeinfo.RawPtrType:
		if typ == nil {
			return nil
		}
		return &typeinfo.RawPtrType{}
	case *typeinfo.RefType:
		if typ == nil {
			return nil
		}
		return &typeinfo.RefType{Mutable: typ.Mutable, Target: loweredRuntimeType(module, typ.Target, seen)}
	case *typeinfo.OptionalType:
		if typ == nil {
			return nil
		}
		return &typeinfo.OptionalType{Inner: loweredRuntimeType(module, typ.Inner, seen)}
	case *typeinfo.ArrayType:
		if typ == nil {
			return nil
		}
		return &typeinfo.ArrayType{Len: typ.Len, Shape: typ.Shape, Elem: loweredRuntimeType(module, typ.Elem, seen)}
	case *typeinfo.StructType:
		if typ == nil {
			return nil
		}
		fields := make([]typeinfo.Field, 0, len(typ.Fields))
		for _, field := range typ.Fields {
			fields = append(fields, typeinfo.Field{Name: field.Name, Type: loweredRuntimeType(module, field.Type, seen)})
		}
		return &typeinfo.StructType{Fields: fields}
	case *typeinfo.InterfaceType:
		if typ == nil {
			return nil
		}
		methods := make([]typeinfo.Method, 0, len(typ.Methods))
		for _, method := range typ.Methods {
			params := make([]typeinfo.Field, 0, len(method.Params))
			for _, param := range method.Params {
				params = append(params, typeinfo.Field{
					Name: param.Name,
					Type: loweredRuntimeType(module, param.Type, seen),
				})
			}
			methods = append(methods, typeinfo.Method{
				Name:   method.Name,
				Params: params,
				Return: loweredRuntimeType(module, method.Return, seen),
			})
		}
		return &typeinfo.InterfaceType{Methods: methods}
	case *typeinfo.FuncType:
		if typ == nil {
			return nil
		}
		params := make([]typeinfo.Type, 0, len(typ.Params))
		for _, param := range typ.Params {
			params = append(params, loweredRuntimeType(module, param, seen))
		}
		// defensive slice copy to prevent sharing original backing array
		return &typeinfo.FuncType{Params: params, Return: loweredRuntimeType(module, typ.Return, seen)}
	default:
		return typeinfo.Underlying(t)
	}
}
