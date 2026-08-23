package lower

import (
	"compiler/internal/ir"
	"compiler/internal/project"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
)

func loweredTypeID(ctx *project.CompilerContext, module *project.Module, t typeinfo.Type) ir.TypeID {
	if ctx == nil || ctx.Types == nil || t == nil {
		return ir.InvalidType
	}
	return internRuntimeType(ctx.Types, loweredRuntimeType(module, t, nil))
}

func loweredReturnTypeID(ctx *project.CompilerContext, module *project.Module, t typeinfo.Type) ir.TypeID {
	if t == nil {
		return ctx.Types.Intern(ir.Type{Kind: ir.TypeVoid})
	}
	return loweredTypeID(ctx, module, t)
}

// internRuntimeType is the semantic-to-IR type boundary. It receives only
// runtime-normalized semantic types, so IR never reparses source type text.
func internRuntimeType(types *ir.TypeTable, t typeinfo.Type) ir.TypeID {
	if types == nil || t == nil {
		return ir.InvalidType
	}
	if descriptor, ok := typeinfo.VariantDescriptorOf(t); ok {
		cases := make([]ir.VariantCase, len(descriptor.Cases))
		for i, variantCase := range descriptor.Cases {
			cases[i].Name = variantCase.Name
			if variantCase.Payload != nil {
				cases[i].Payload = internRuntimeType(types, variantCase.Payload)
			}
		}
		if descriptor.Family == typeinfo.VariantFamilyOptional {
			return types.Intern(ir.OptionalVariant(cases[ir.OptionalPresentCase].Payload))
		}
		return types.Intern(ir.Type{
			Kind: ir.TypeVariant, Family: ir.VariantFamilyNamed, Name: t.Text(),
			Identity: descriptor.Identity, Cases: cases,
		})
	}
	switch typ := typeinfo.Underlying(t).(type) {
	case *typeinfo.InvalidType, *typeinfo.UnknownType:
		return ir.InvalidType
	case *typeinfo.IntegerType:
		if typ == nil {
			return ir.InvalidType
		}
		return types.Intern(ir.Type{Kind: ir.TypeInteger, Signed: typ.Signed, Bits: typ.Bits})
	case *typeinfo.ByteType:
		return types.Intern(ir.Type{Kind: ir.TypeByte})
	case *typeinfo.CharType:
		return types.Intern(ir.Type{Kind: ir.TypeChar})
	case *typeinfo.FloatType:
		if typ == nil {
			return ir.InvalidType
		}
		return types.Intern(ir.Type{Kind: ir.TypeFloat, Bits: typ.Bits})
	case *typeinfo.BoolType:
		return types.Intern(ir.Type{Kind: ir.TypeBool})
	case *typeinfo.CStrType:
		return types.Intern(ir.Type{Kind: ir.TypeCStr})
	case *typeinfo.StringType:
		return types.Intern(ir.Type{Kind: ir.TypeString})
	case *typeinfo.NoneType:
		return types.Intern(ir.Type{Kind: ir.TypeVoid})
	case *typeinfo.AllocatorType:
		return types.Intern(ir.Type{Kind: ir.TypeAllocator})
	case *typeinfo.NamedType:
		if typ == nil || typ.Name == "" {
			return ir.InvalidType
		}
		return types.Intern(ir.Type{Kind: ir.TypeNamed, Name: typ.Name})
	case *typeinfo.OwnedPtrType:
		if typ == nil {
			return ir.InvalidType
		}
		return types.Intern(ir.Type{Kind: ir.TypeOwnedPtr, Elem: internRuntimeType(types, typ.Target)})
	case *typeinfo.RawPtrType:
		return types.Intern(ir.Type{Kind: ir.TypeRawPtr})
	case *typeinfo.RefType:
		if typ == nil {
			return ir.InvalidType
		}
		return types.Intern(ir.Type{Kind: ir.TypeReference, Mutable: typ.Mutable, Elem: internRuntimeType(types, typ.Target)})
	case *typeinfo.ArrayType:
		if typ == nil {
			return ir.InvalidType
		}
		if typ.Shape == typeinfo.ArraySlice {
			return types.Intern(ir.Type{Kind: ir.TypeSlice, Elem: internRuntimeType(types, typ.Elem)})
		}
		return types.Intern(ir.Type{Kind: ir.TypeArray, Length: typ.Len, Elem: internRuntimeType(types, typ.Elem)})
	case *typeinfo.StructType:
		if typ == nil {
			return ir.InvalidType
		}
		fields := make([]ir.TypeField, 0, len(typ.Fields))
		for _, field := range typ.Fields {
			fields = append(fields, ir.TypeField{Name: field.Name, Type: internRuntimeType(types, field.Type)})
		}
		return types.Intern(ir.Type{Kind: ir.TypeStruct, Fields: fields})
	case *typeinfo.InterfaceType:
		if typ == nil {
			return ir.InvalidType
		}
		methods := make([]ir.TypeMethod, 0, len(typ.Methods))
		for _, method := range typ.Methods {
			params := make([]ir.TypeField, 0, len(method.Params))
			for _, param := range method.Params {
				params = append(params, ir.TypeField{Name: param.Name, Type: internRuntimeType(types, param.Type)})
			}
			returnType := internRuntimeType(types, method.Return)
			if returnType == ir.InvalidType {
				returnType = types.Intern(ir.Type{Kind: ir.TypeVoid})
			}
			methods = append(methods, ir.TypeMethod{Name: method.Name, Params: params, Return: returnType})
		}
		return types.Intern(ir.Type{Kind: ir.TypeInterface, Methods: methods})
	case *typeinfo.FuncType:
		if typ == nil {
			return ir.InvalidType
		}
		params := make([]ir.TypeID, 0, len(typ.Params))
		for _, param := range typ.Params {
			params = append(params, internRuntimeType(types, param))
		}
		returnType := internRuntimeType(types, typ.Return)
		if returnType == ir.InvalidType {
			returnType = types.Intern(ir.Type{Kind: ir.TypeVoid})
		}
		return types.Intern(ir.Type{Kind: ir.TypeFunction, Params: params, Return: returnType})
	default:
		return ir.InvalidType
	}
}

// resolveNamedType performs a single-hop scope lookup for a NamedType so the
// lowerer can collapse source-level aliases before runtime layout work.
// Called only from loweredRuntimeType; lives here to avoid importing table
// from the leaf typeinfo package.
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
