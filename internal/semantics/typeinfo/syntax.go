package typeinfo

import (
	"compiler/internal/frontend/ast"
	"compiler/internal/frontend/token"
	"compiler/internal/target"
	"compiler/pkg/numeric"
)

// SyntaxResolver supplies project-owned name lookup and generic instantiation
// while typeinfo owns the single AST-to-semantic-type mapping.
type SyntaxResolver interface {
	ResolveNamed(node ast.TypeExpr) (Type, bool)
	ResolveQualified(node *ast.ScopeResolution) (Type, bool)
	Instantiate(base *DefinedType, arguments []Type, node ast.TypeExpr) Type
}

type SyntaxIssueKind uint8

const (
	SyntaxInvalidSelf SyntaxIssueKind = iota
	SyntaxInvalidArrayLength
	SyntaxInvalidApplication
	SyntaxAnonymousInterface
)

type SyntaxIssue struct {
	Kind SyntaxIssueKind
	Node ast.Node
	Name string
	Want int
	Got  int
}

type SyntaxContext struct {
	Target            target.Info
	SelfType          Type
	AllowAbstractSelf bool
	TypeParameters    map[string]Type
	// NamedInterfaceRoot identifies exact syntax owned by an interface
	// declaration. Nested interface syntax remains anonymous.
	NamedInterfaceRoot ast.TypeExpr
	Resolver           SyntaxResolver
	Issues             *[]SyntaxIssue
}

func (context SyntaxContext) recordIssue(kind SyntaxIssueKind, node ast.Node, name string, want, got int) {
	if context.Issues != nil {
		*context.Issues = append(*context.Issues, SyntaxIssue{Kind: kind, Node: node, Name: name, Want: want, Got: got})
	}
}

func TypeFromSyntax(node ast.TypeExpr, context SyntaxContext) Type {
	if node == nil {
		return nil
	}
	if !context.Target.Valid() {
		context.Target = target.Host()
	}
	switch typ := node.(type) {
	case *ast.NamedType:
		if typ == nil {
			return nil
		}
		if typ.Name == "Self" {
			if context.SelfType != nil {
				return context.SelfType
			}
			if context.AllowAbstractSelf {
				return &NamedType{Name: "Self"}
			}
			context.recordIssue(SyntaxInvalidSelf, typ, "", 0, 0)
			return &InvalidType{}
		}
		if parameter := context.TypeParameters[typ.Name]; parameter != nil {
			return parameter
		}
		return applyTypeArguments(typ, resolveTypeName(typ, context), nil, context)
	case *ast.AppliedType:
		if typ == nil || typ.Name == nil {
			return nil
		}
		arguments := make([]Type, len(typ.TypeArgs))
		for index, argument := range typ.TypeArgs {
			arguments[index] = TypeFromSyntax(argument, context)
		}
		if context.TypeParameters[typ.Name.Name] != nil {
			return applyTypeArguments(typ, &NamedType{Name: typ.Name.Name}, arguments, context)
		}
		return applyTypeArguments(typ, resolveTypeName(typ, context), arguments, context)
	case *ast.ScopeResolution:
		if typ == nil {
			return nil
		}
		if _, _, imported := typ.ImportMember(); imported && context.Resolver != nil {
			if resolved, ok := context.Resolver.ResolveQualified(typ); ok && resolved != nil {
				arguments := make([]Type, len(typ.Segments[1].TypeArgs))
				for index, argument := range typ.Segments[1].TypeArgs {
					arguments[index] = TypeFromSyntax(argument, context)
				}
				return applyTypeArguments(typ, resolved, arguments, context)
			}
		}
		return &NamedType{Name: typ.TypeText()}
	case *ast.OwnedPtrType:
		if typ == nil {
			return nil
		}
		return &OwnedPtrType{Target: TypeFromSyntax(typ.Target, context)}
	case *ast.RawPtrType:
		if typ == nil {
			return nil
		}
		return &RawPtrType{}
	case *ast.RefType:
		if typ == nil {
			return nil
		}
		return &RefType{Mutable: typ.Mutable, Target: TypeFromSyntax(typ.Target, context)}
	case *ast.OptionalType:
		if typ == nil {
			return nil
		}
		return NewOptional(TypeFromSyntax(typ.Inner, context))
	case *ast.ArrayType:
		if typ == nil {
			return nil
		}
		length := ""
		if typ.Len != nil {
			lengthType := DefaultNumberType(typ.Len.Value)
			if typ.Len.ExplicitType != "" {
				var ok bool
				lengthType, ok = NumericTypeFromName(typ.Len.ExplicitType, context.Target)
				if !ok {
					context.recordIssue(SyntaxInvalidArrayLength, typ.Len, "", 0, 0)
					return &InvalidType{}
				}
			}
			indexType, indexTypeOK := NumericTypeFromName("usize", context.Target)
			if !IsIntegral(lengthType) || !LiteralFitsType(typ.Len.Value, lengthType) ||
				!indexTypeOK || !LiteralFitsType(typ.Len.Value, indexType) {
				context.recordIssue(SyntaxInvalidArrayLength, typ.Len, "", 0, 0)
				return &InvalidType{}
			}
			canonical, err := numeric.CanonicalizeIntegerLiteral(typ.Len.Value)
			if err != nil {
				context.recordIssue(SyntaxInvalidArrayLength, typ.Len, "", 0, 0)
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
		return &ArrayType{Len: length, Shape: shape, Elem: TypeFromSyntax(typ.Elem, context)}
	case *ast.FuncType:
		if typ == nil {
			return nil
		}
		params := make([]Type, 0, len(typ.Params))
		paramNames := make([]string, 0, len(typ.Params))
		for _, param := range typ.Params {
			params = append(params, TypeFromSyntax(param.Type, context))
			paramNames = append(paramNames, parameterName(param))
		}
		return &FuncType{
			Params:        params,
			ParamNames:    paramNames,
			Return:        TypeFromSyntax(typ.Return, context),
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
				Type: TypeFromSyntax(field.Type, context),
			})
		}
		return &StructType{Fields: fields}
	case *ast.InterfaceType:
		if typ == nil {
			return nil
		}
		if context.NamedInterfaceRoot != typ {
			context.recordIssue(SyntaxAnonymousInterface, typ, "", 0, 0)
			return &InvalidType{}
		}
		receiverContext := context
		receiverContext.AllowAbstractSelf = true
		methodContext := context
		methodContext.AllowAbstractSelf = false
		methods := make([]Method, 0, len(typ.Methods))
		for _, method := range typ.Methods {
			params := make([]Field, 0, len(method.Params)+1)
			originParams := make([]ast.Param, 0, len(method.Params)+1)
			if method.Receiver != nil {
				params = append(params, Field{Name: "self", Type: TypeFromSyntax(method.Receiver.Type, receiverContext)})
				originParams = append(originParams, *method.Receiver)
			}
			for _, param := range method.Params {
				name := ""
				if param.Name != nil {
					name = param.Name.Name
				}
				params = append(params, Field{
					Name: name,
					Type: TypeFromSyntax(param.Type, methodContext),
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
				Return:        TypeFromSyntax(method.ReturnType, methodContext),
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
				semanticCase.Payload = TypeFromSyntax(variant.Payload, context)
			}
			cases = append(cases, semanticCase)
		}
		return &EnumType{Cases: cases}
	default:
		return nil
	}
}

func resolveTypeName(node ast.TypeExpr, context SyntaxContext) Type {
	name := node.TypeText()
	if applied, ok := node.(*ast.AppliedType); ok && applied.Name != nil {
		name = applied.Name.Name
	}
	if context.Resolver != nil {
		if resolved, ok := context.Resolver.ResolveNamed(node); ok && resolved != nil {
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
	if signed, bits, ok := token.ParseIntegerBuiltin(name, context.Target); ok {
		return &IntegerType{Signed: signed, Bits: bits}
	}
	return &NamedType{Name: name}
}

func applyTypeArguments(node ast.TypeExpr, base Type, arguments []Type, context SyntaxContext) Type {
	defined, named := base.(*DefinedType)
	want := 0
	if named && defined != nil {
		want = len(defined.TypeParameters)
	}
	got := len(arguments)
	if want != got || got > 0 && !named {
		name := TypeText(base)
		context.recordIssue(SyntaxInvalidApplication, node, name, want, got)
		return &InvalidType{}
	}
	if got == 0 {
		return base
	}
	if context.Resolver != nil {
		return context.Resolver.Instantiate(defined, arguments, node)
	}
	return &InvalidType{}
}

func FuncTypeFromDecl(decl *ast.FnDecl, context SyntaxContext) *FuncType {
	if decl == nil {
		return nil
	}
	params := make([]Type, 0, len(decl.ParamsWithReceiver()))
	paramNames := make([]string, 0, len(decl.ParamsWithReceiver()))
	allParams := decl.ParamsWithReceiver()
	for i, param := range allParams {
		params = append(params, TypeFromSyntax(param.Type, context))
		name := parameterName(param)
		if decl.Receiver != nil && i == 0 {
			name = "self"
		}
		paramNames = append(paramNames, name)
	}
	return &FuncType{
		Params:        params,
		ParamNames:    paramNames,
		Return:        TypeFromSyntax(decl.ReturnType, context),
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

func ReturnOriginSources(call *ast.CallExpr, args []ast.Expr, fn *FuncType) []ast.Expr {
	if call == nil || call.Callee == nil || fn == nil || fn.ReturnOrigins == nil {
		return nil
	}
	selector, methodCall := call.Callee.(*ast.SelectorExpr)
	sources := make([]ast.Expr, 0, len(fn.ReturnOrigins.Sources))
	for _, slot := range fn.ReturnOrigins.Sources {
		if methodCall {
			if slot == 0 {
				sources = append(sources, selector.Expr)
			} else if slot > 0 && slot <= len(args) {
				sources = append(sources, args[slot-1])
			}
		} else if slot >= 0 && slot < len(args) {
			sources = append(sources, args[slot])
		}
	}
	return sources
}
