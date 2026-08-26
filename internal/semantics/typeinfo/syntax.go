package typeinfo

import (
	"compiler/internal/frontend/ast"
	"compiler/internal/frontend/token"
	"compiler/internal/target"
	"compiler/pkg/numeric"
)

type SyntaxOptions struct {
	Target             target.Info
	SelfType           Type
	AllowAbstractSelf  bool
	TypeParameters     map[string]Type
	ResolveNamed       func(name string) (Type, bool)
	ResolveQualified   func(moduleName, memberName string) (Type, bool)
	Instantiate        func(base *DefinedType, arguments []Type, node ast.TypeExpr) Type
	InvalidSelf        func(node *ast.NamedType) Type
	InvalidArrayLen    func(node *ast.NumberLit) Type
	InvalidApplication func(node ast.TypeExpr, name string, want, got int) Type
}

func TypeFromSyntax(node ast.TypeExpr, opts SyntaxOptions) Type {
	if node == nil {
		return nil
	}
	if !opts.Target.Valid() {
		opts.Target = target.Host()
	}
	switch typ := node.(type) {
	case *ast.NamedType:
		if typ == nil {
			return nil
		}
		if typ.Name == "Self" {
			if opts.SelfType != nil {
				return opts.SelfType
			}
			if opts.AllowAbstractSelf {
				return &NamedType{Name: "Self"}
			}
			if opts.InvalidSelf != nil {
				return opts.InvalidSelf(typ)
			}
			return &InvalidType{}
		}
		if parameter := opts.TypeParameters[typ.Name]; parameter != nil {
			return parameter
		}
		return applyTypeArguments(typ, resolveTypeName(typ.Name, opts), nil, opts)
	case *ast.AppliedType:
		if typ == nil || typ.Name == nil {
			return nil
		}
		arguments := make([]Type, len(typ.TypeArgs))
		for index, argument := range typ.TypeArgs {
			arguments[index] = TypeFromSyntax(argument, opts)
		}
		if opts.TypeParameters[typ.Name.Name] != nil {
			return applyTypeArguments(typ, &NamedType{Name: typ.Name.Name}, arguments, opts)
		}
		return applyTypeArguments(typ, resolveTypeName(typ.Name.Name, opts), arguments, opts)
	case *ast.ScopeResolution:
		if typ == nil {
			return nil
		}
		qualifier, member, imported := typ.ImportMember()
		if imported && opts.ResolveQualified != nil {
			if resolved, ok := opts.ResolveQualified(qualifier.Name, member.Name); ok && resolved != nil {
				arguments := make([]Type, len(typ.Segments[1].TypeArgs))
				for index, argument := range typ.Segments[1].TypeArgs {
					arguments[index] = TypeFromSyntax(argument, opts)
				}
				return applyTypeArguments(typ, resolved, arguments, opts)
			}
		}
		return &NamedType{Name: typ.TypeText()}
	case *ast.OwnedPtrType:
		if typ == nil {
			return nil
		}
		return &OwnedPtrType{Target: TypeFromSyntax(typ.Target, opts)}
	case *ast.RawPtrType:
		if typ == nil {
			return nil
		}
		return &RawPtrType{}
	case *ast.RefType:
		if typ == nil {
			return nil
		}
		return &RefType{Mutable: typ.Mutable, Target: TypeFromSyntax(typ.Target, opts)}
	case *ast.OptionalType:
		if typ == nil {
			return nil
		}
		return &OptionalType{Inner: TypeFromSyntax(typ.Inner, opts)}
	case *ast.ArrayType:
		if typ == nil {
			return nil
		}
		length := ""
		if typ.Len != nil {
			lengthType := DefaultNumberType(typ.Len.Value)
			if typ.Len.ExplicitType != "" {
				var ok bool
				lengthType, ok = NumericTypeFromName(typ.Len.ExplicitType, opts.Target)
				if !ok {
					if opts.InvalidArrayLen != nil {
						return opts.InvalidArrayLen(typ.Len)
					}
					return &InvalidType{}
				}
			}
			indexType, indexTypeOK := NumericTypeFromName("usize", opts.Target)
			if !IsIntegral(lengthType) || !LiteralFitsType(typ.Len.Value, lengthType) ||
				!indexTypeOK || !LiteralFitsType(typ.Len.Value, indexType) {
				if opts.InvalidArrayLen != nil {
					return opts.InvalidArrayLen(typ.Len)
				}
				return &InvalidType{}
			}
			canonical, err := numeric.CanonicalizeIntegerLiteral(typ.Len.Value)
			if err != nil {
				if opts.InvalidArrayLen != nil {
					return opts.InvalidArrayLen(typ.Len)
				}
				return &InvalidType{}
			}
			length = canonical
		}
		shape := ArrayFixed
		switch typ.Shape {
		case ast.ArrayOwner:
			shape = ArrayOwner
		case ast.ArraySlice:
			shape = ArraySlice
		}
		return &ArrayType{Len: length, Shape: shape, Elem: TypeFromSyntax(typ.Elem, opts)}
	case *ast.FuncType:
		if typ == nil {
			return nil
		}
		params := make([]Type, 0, len(typ.Params))
		paramNames := make([]string, 0, len(typ.Params))
		for _, param := range typ.Params {
			params = append(params, TypeFromSyntax(param.Type, opts))
			paramNames = append(paramNames, parameterName(param))
		}
		return &FuncType{
			Params:        params,
			ParamNames:    paramNames,
			Return:        TypeFromSyntax(typ.Return, opts),
			ReturnOrigins: returnOriginContract(typ.ReturnOrigins, typ.Params, false),
		}
	case *ast.StructType:
		if typ == nil {
			return nil
		}
		fields := make([]Field, 0, len(typ.Fields))
		for _, field := range typ.Fields {
			name := ""
			if field.Name != nil {
				name = field.Name.Name
			}
			fields = append(fields, Field{
				Name: name,
				Type: TypeFromSyntax(field.Type, opts),
			})
		}
		return &StructType{Fields: fields}
	case *ast.InterfaceType:
		if typ == nil {
			return nil
		}
		receiverOpts := opts
		receiverOpts.AllowAbstractSelf = true
		methodOpts := opts
		methodOpts.AllowAbstractSelf = false
		methods := make([]Method, 0, len(typ.Methods))
		for _, method := range typ.Methods {
			params := make([]Field, 0, len(method.Params)+1)
			originParams := make([]ast.Param, 0, len(method.Params)+1)
			if method.Receiver != nil {
				params = append(params, Field{Name: "self", Type: TypeFromSyntax(method.Receiver.Type, receiverOpts)})
				originParams = append(originParams, *method.Receiver)
			}
			for _, param := range method.Params {
				name := ""
				if param.Name != nil {
					name = param.Name.Name
				}
				params = append(params, Field{
					Name: name,
					Type: TypeFromSyntax(param.Type, methodOpts),
				})
				originParams = append(originParams, param)
			}
			name := ""
			if method.Name != nil {
				name = method.Name.Name
			}
			methods = append(methods, Method{
				Name:          name,
				Params:        params,
				Return:        TypeFromSyntax(method.ReturnType, methodOpts),
				ReturnOrigins: returnOriginContract(method.ReturnOrigins, originParams, method.Receiver != nil),
			})
		}
		return &InterfaceType{Methods: methods}
	case *ast.EnumType:
		if typ == nil {
			return nil
		}
		cases := make([]VariantCase, 0, len(typ.Variants))
		for _, variant := range typ.Variants {
			if variant.Name == nil {
				continue
			}
			semanticCase := VariantCase{Name: variant.Name.Name}
			if variant.Payload != nil {
				semanticCase.Payload = TypeFromSyntax(variant.Payload, opts)
			}
			cases = append(cases, semanticCase)
		}
		return &EnumType{Cases: cases}
	default:
		return nil
	}
}

func resolveTypeName(name string, opts SyntaxOptions) Type {
	if opts.ResolveNamed != nil {
		if resolved, ok := opts.ResolveNamed(name); ok && resolved != nil {
			return resolved
		}
	}
	switch name {
	case "bool":
		return &BoolType{}
	case "byte":
		return &ByteType{}
	case "char":
		return &CharType{}
	case "cstr":
		return &CStrType{}
	case "str", "string":
		return &StringType{}
	case "f32":
		return &FloatType{Bits: 32}
	case "f64":
		return &FloatType{Bits: 64}
	case "Allocator":
		return &AllocatorType{}
	}
	if signed, bits, ok := token.ParseIntegerBuiltin(name, opts.Target); ok {
		return &IntegerType{Signed: signed, Bits: bits}
	}
	return &NamedType{Name: name}
}

func applyTypeArguments(node ast.TypeExpr, base Type, arguments []Type, opts SyntaxOptions) Type {
	defined, named := base.(*DefinedType)
	want := 0
	if named && defined != nil {
		want = len(defined.TypeParameters)
	}
	got := len(arguments)
	if want != got || got > 0 && !named {
		name := TypeText(base)
		if opts.InvalidApplication != nil {
			return opts.InvalidApplication(node, name, want, got)
		}
		return &InvalidType{}
	}
	if got == 0 {
		return base
	}
	if opts.Instantiate != nil {
		return opts.Instantiate(defined, arguments, node)
	}
	return &InvalidType{}
}

func FuncTypeFromDeclWithOptions(decl *ast.FnDecl, opts SyntaxOptions) *FuncType {
	if decl == nil {
		return nil
	}
	params := make([]Type, 0, len(decl.ParamsWithReceiver()))
	paramNames := make([]string, 0, len(decl.ParamsWithReceiver()))
	allParams := decl.ParamsWithReceiver()
	for i, param := range allParams {
		params = append(params, TypeFromSyntax(param.Type, opts))
		name := parameterName(param)
		if decl.Receiver != nil && i == 0 {
			name = "self"
		}
		paramNames = append(paramNames, name)
	}
	return &FuncType{
		Params:        params,
		ParamNames:    paramNames,
		Return:        TypeFromSyntax(decl.ReturnType, opts),
		ReturnOrigins: returnOriginContract(decl.ReturnOrigins, allParams, decl.Receiver != nil),
	}
}

func parameterName(param ast.Param) string {
	if param.Name == nil {
		return ""
	}
	return param.Name.Name
}

func returnOriginContract(clause *ast.ReturnOriginClause, params []ast.Param, hasReceiver bool) *ReturnOriginContract {
	if clause == nil {
		return nil
	}
	contract := &ReturnOriginContract{Sources: make([]int, 0, len(clause.Sources))}
	for _, source := range clause.Sources {
		slot := -1
		if source != nil {
			if hasReceiver && source.Name == "self" {
				slot = 0
			} else {
				for i, param := range params {
					if (!hasReceiver || i != 0) && param.Name != nil && param.Name.Name == source.Name {
						slot = i
						break
					}
				}
			}
		}
		contract.Sources = append(contract.Sources, slot)
	}
	return contract
}

func ReturnOriginSources(call *ast.CallExpr, fn *FuncType) []ast.Expr {
	if call == nil || call.Callee == nil || fn == nil || fn.ReturnOrigins == nil {
		return nil
	}
	selector, methodCall := call.Callee.(*ast.SelectorExpr)
	sources := make([]ast.Expr, 0, len(fn.ReturnOrigins.Sources))
	for _, slot := range fn.ReturnOrigins.Sources {
		if methodCall {
			if slot == 0 {
				sources = append(sources, selector.Expr)
			} else if slot > 0 && slot <= len(call.Args) {
				sources = append(sources, call.Args[slot-1])
			}
		} else if slot >= 0 && slot < len(call.Args) {
			sources = append(sources, call.Args[slot])
		}
	}
	return sources
}
