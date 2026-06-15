package typeinfo

import "compiler/internal/frontend/ast"

type SyntaxOptions struct {
	SelfType          Type
	AllowAbstractSelf bool
	ResolveNamed      func(name string) (Type, bool)
	ResolveQualified  func(moduleName, memberName string) (Type, bool)
	InvalidSelf       func(node *ast.NamedType) Type
}

func ASTTypeWithOptions(node ast.TypeExpr, opts SyntaxOptions) Type {
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
		base := TypeFromSyntax(typ)
		if opts.ResolveNamed != nil {
			if resolved, ok := opts.ResolveNamed(typ.Name); ok && resolved != nil {
				return resolved
			}
		}
		return base
	case *ast.ScopeResolution:
		if opts.ResolveQualified != nil {
			if resolved, ok := opts.ResolveQualified(typ.Module.Name, typ.Name.Name); ok && resolved != nil {
				return resolved
			}
		}
		return TypeFromSyntax(typ)
	case *ast.RawPtrType:
		if typ == nil {
			return nil
		}
		return &RawPtrType{
			Mutable: typ.Mutable,
			Target:  ASTTypeWithOptions(typ.Target, opts),
		}
	case *ast.FuncType:
		if typ == nil {
			return nil
		}
		params := make([]Type, 0, len(typ.Params))
		for _, param := range typ.Params {
			params = append(params, ASTTypeWithOptions(param, opts))
		}
		return &FuncType{
			Params: params,
			Return: ASTTypeWithOptions(typ.Return, opts),
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
				Type: ASTTypeWithOptions(field.Type, opts),
			})
		}
		return &StructType{Fields: fields}
	case *ast.InterfaceType:
		if typ == nil {
			return nil
		}
		methodOpts := opts
		methodOpts.AllowAbstractSelf = true
		methods := make([]Method, 0, len(typ.Methods))
		for _, method := range typ.Methods {
			params := make([]Field, 0, len(method.Params))
			for _, param := range method.Params {
				name := ""
				if param.Name != nil {
					name = param.Name.Name
				}
				params = append(params, Field{
					Name: name,
					Type: ASTTypeWithOptions(param.Type, methodOpts),
				})
			}
			name := ""
			if method.Name != nil {
				name = method.Name.Name
			}
			methods = append(methods, Method{
				Name:   name,
				Params: params,
				Return: ASTTypeWithOptions(method.ReturnType, methodOpts),
			})
		}
		return &InterfaceType{Methods: methods}
	default:
		return TypeFromSyntax(node)
	}
}

func FuncTypeFromDeclWithOptions(decl *ast.FnDecl, opts SyntaxOptions) *FuncType {
	if decl == nil {
		return nil
	}
	params := make([]Type, 0, len(decl.Params))
	for _, param := range decl.Params {
		params = append(params, ASTTypeWithOptions(param.Type, opts))
	}
	return &FuncType{
		Params: params,
		Return: ASTTypeWithOptions(decl.ReturnType, opts),
	}
}
