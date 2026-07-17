package typeinfo

import (
	"compiler/internal/frontend/ast"
	"compiler/internal/frontend/token"
	"compiler/pkg/numeric"
)

type SyntaxOptions struct {
	SelfType          Type
	AllowAbstractSelf bool
	ResolveNamed      func(name string) (Type, bool)
	ResolveQualified  func(moduleName, memberName string) (Type, bool)
	InvalidSelf       func(node *ast.NamedType) Type
	InvalidArrayLen   func(node *ast.NumberLit) Type
}

func TypeFromSyntax(node ast.TypeExpr, opts SyntaxOptions) Type {
	if node == nil {
		return nil
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
		if opts.ResolveNamed != nil {
			if resolved, ok := opts.ResolveNamed(typ.Name); ok && resolved != nil {
				return resolved
			}
		}
		switch typ.Name {
		case "bool":
			return &BoolType{}
		case "byte":
			return &ByteType{}
		case "cstr":
			return &CStrType{}
		case "string":
			return &StringType{}
		case "f32":
			return &FloatType{Bits: 32}
		case "f64":
			return &FloatType{Bits: 64}
		}
		if signed, bits, ok := token.ParseIntegerBuiltin(typ.Name); ok {
			return &IntegerType{Signed: signed, Bits: bits}
		}
		return &NamedType{Name: typ.Name}
	case *ast.ScopeResolution:
		if typ == nil {
			return nil
		}
		if opts.ResolveQualified != nil {
			if resolved, ok := opts.ResolveQualified(typ.Module.Name, typ.Name.Name); ok && resolved != nil {
				return resolved
			}
		}
		return &NamedType{Name: typ.Module.Name + "::" + typ.Name.Name}
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
				lengthType, ok = NumericTypeFromName(typ.Len.ExplicitType)
				if !ok {
					if opts.InvalidArrayLen != nil {
						return opts.InvalidArrayLen(typ.Len)
					}
					return &InvalidType{}
				}
			}
			if !IsIntegral(lengthType) || !LiteralFitsType(typ.Len.Value, lengthType) {
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
		return &ArrayType{Len: length, Dynamic: typ.Dynamic, Elem: TypeFromSyntax(typ.Elem, opts)}
	case *ast.FuncType:
		if typ == nil {
			return nil
		}
		params := make([]Type, 0, len(typ.Params))
		for _, param := range typ.Params {
			params = append(params, TypeFromSyntax(param, opts))
		}
		return &FuncType{
			Params: params,
			Return: TypeFromSyntax(typ.Return, opts),
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
			if method.Receiver != nil {
				params = append(params, Field{Type: TypeFromSyntax(method.Receiver.Type, receiverOpts)})
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
			}
			name := ""
			if method.Name != nil {
				name = method.Name.Name
			}
			methods = append(methods, Method{
				Name:   name,
				Params: params,
				Return: TypeFromSyntax(method.ReturnType, methodOpts),
			})
		}
		return &InterfaceType{Methods: methods}
	case *ast.EnumType:
		if typ == nil {
			return nil
		}
		variants := make([]string, 0, len(typ.Variants))
		for _, variant := range typ.Variants {
			if variant.Name != nil {
				variants = append(variants, variant.Name.Name)
			}
		}
		return &EnumType{Variants: variants}
	default:
		return nil
	}
}

func FuncTypeFromDeclWithOptions(decl *ast.FnDecl, opts SyntaxOptions) *FuncType {
	if decl == nil {
		return nil
	}
	params := make([]Type, 0, len(decl.ParamsWithReceiver()))
	for _, param := range decl.ParamsWithReceiver() {
		params = append(params, TypeFromSyntax(param.Type, opts))
	}
	return &FuncType{
		Params: params,
		Return: TypeFromSyntax(decl.ReturnType, opts),
	}
}
