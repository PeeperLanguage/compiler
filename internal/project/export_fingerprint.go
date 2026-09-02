package project

import (
	"fmt"
	"strings"

	"compiler/internal/constvalue"
	"compiler/internal/frontend/ast"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
)

// SemanticExportFingerprint identifies compiler-visible semantic facts exported by module.
// Constant values resolve through their defining module, so an exported default that
// references an imported constant still changes when that constant changes.
func SemanticExportFingerprint(ctx *CompilerContext, module *Module) string {
	if module == nil || module.ModuleScope == nil {
		return ast.FingerprintParts(nil)
	}
	parts := make([]string, 0)
	for _, sym := range module.ModuleScope.Symbols() {
		if sym == nil || !sym.IsPub {
			continue
		}
		part := string(sym.Kind) + ":" + sym.Name + ":" + semanticTypeKey(sym.Type, make(map[typeinfo.Type]bool))
		if sym.Kind == symbols.SymbolVar {
			part += fmt.Sprintf(":mutable=%t", sym.IsMutable())
		}
		part += semanticExportMetadata(ctx, module, sym)
		if sym.Kind == symbols.SymbolConst {
			part += ":value=" + constantKey(ctx.PublishedConstant(module, sym))
		}
		parts = append(parts, part)
	}
	if module.Bindings != nil {
		for receiver, methods := range module.Bindings.MethodsByReceiver {
			for _, method := range methods {
				if method == nil || !method.IsPub {
					continue
				}
				parts = append(parts, "method:"+receiver+":"+method.Name+":"+
					semanticTypeKey(method.Type, make(map[typeinfo.Type]bool))+semanticExportMetadata(ctx, module, method))
			}
		}
	}
	return ast.FingerprintParts(parts)
}

func semanticExportMetadata(ctx *CompilerContext, module *Module, sym *symbols.Symbol) string {
	decl, ok := sym.ASTNode.(ast.Decl)
	if !ok || decl == nil {
		return ""
	}
	metadata := ":syntax=" + decl.GetDeclSurface()
	if attributed, ok := decl.(ast.AttributedNode); ok {
		attributes := make([]string, 0)
		for _, attribute := range attributed.GetAttributes() {
			args := make([]string, len(attribute.Args))
			for index, arg := range attribute.Args {
				args[index] = ast.ExprText(arg)
			}
			attributes = append(attributes, attribute.Name+"("+strings.Join(args, ",")+")")
		}
		metadata += ":attributes=" + ast.FingerprintParts(attributes)
	}
	fn, ok := decl.(*ast.FnDecl)
	if !ok || fn == nil {
		return metadata
	}
	if linkName, external := ast.FunctionLinkName(fn, sym.Name); external {
		metadata += ":link=" + linkName
	}
	for index, param := range fn.Params {
		if param.Default == nil {
			continue
		}
		metadata += fmt.Sprintf(":default[%d]=%s", index, ast.ExprText(param.Default))
		facts := make([]string, 0)
		ast.Inspect(param.Default, func(node ast.Node) bool {
			ident, ok := node.(*ast.Ident)
			if !ok || ident == nil || module.Bindings == nil {
				return true
			}
			resolved := module.Bindings.NodeSymbols[ident.ID()]
			if resolved == nil {
				return true
			}
			fact := resolved.Name + ":" + semanticTypeKey(resolved.Type, make(map[typeinfo.Type]bool))
			if resolved.Kind == symbols.SymbolConst {
				fact += "=" + constantKey(ctx.PublishedConstant(module, resolved))
			}
			facts = append(facts, fact)
			return true
		})
		metadata += ":facts=" + ast.FingerprintParts(facts)
	}
	return metadata
}

func semanticTypeKey(typ symbols.Type, visiting map[typeinfo.Type]bool) string {
	semantic, ok := typ.(typeinfo.Type)
	if !ok || semantic == nil {
		return ""
	}
	if visiting[semantic] {
		return "recursive(" + typeinfo.TypeText(semantic) + ")"
	}
	visiting[semantic] = true
	defer delete(visiting, semantic)

	switch node := semantic.(type) {
	case *typeinfo.DefinedType:
		parameters := make([]string, len(node.TypeParameters))
		for index, parameter := range node.TypeParameters {
			parameters[index] = semanticTypeKey(parameter, visiting)
		}
		arguments := make([]string, len(node.TypeArguments))
		for index, argument := range node.TypeArguments {
			arguments[index] = semanticTypeKey(argument, visiting)
		}
		return fmt.Sprintf("defined(%d:%s:%s<%s>[%s]:%s)", node.Kind, node.Identity, node.Name,
			strings.Join(parameters, ","), strings.Join(arguments, ","), semanticTypeKey(node.Underlying, visiting))
	case *typeinfo.TypeParameterType:
		return fmt.Sprintf("parameter(%s:%d:%s)", node.OwnerIdentity, node.Index, node.Name)
	case *typeinfo.OwnedPtrType:
		return "owned(" + semanticTypeKey(node.Target, visiting) + ")"
	case *typeinfo.RefType:
		return fmt.Sprintf("ref(%t:%s)", node.Mutable, semanticTypeKey(node.Target, visiting))
	case *typeinfo.OptionalType:
		return "optional(" + semanticTypeKey(node.Inner, visiting) + ")"
	case *typeinfo.ArrayType:
		return fmt.Sprintf("array(%d:%s:%s)", node.Shape, node.Len, semanticTypeKey(node.Elem, visiting))
	case *typeinfo.FuncType:
		params := make([]string, len(node.Params))
		for index, param := range node.Params {
			name := ""
			if index < len(node.ParamNames) {
				name = node.ParamNames[index]
			}
			params[index] = name + ":" + semanticTypeKey(param, visiting)
		}
		origins := ""
		if node.ReturnOrigins != nil {
			origins = fmt.Sprint(node.ReturnOrigins.Sources)
		}
		return "fn(" + strings.Join(params, ",") + ")->" + semanticTypeKey(node.Return, visiting) + ":from=" + origins
	case *typeinfo.StructType:
		fields := make([]string, len(node.Fields))
		for index, field := range node.Fields {
			fields[index] = field.Name + ":" + semanticTypeKey(field.Type, visiting)
		}
		return "struct(" + strings.Join(fields, ",") + ")"
	case *typeinfo.InterfaceType:
		methods := make([]string, len(node.Methods))
		for index, method := range node.Methods {
			methods[index] = method.Name + ":" + semanticTypeKey(method.CallableType(), visiting)
		}
		return "interface(" + strings.Join(methods, ";") + ")"
	case *typeinfo.EnumType:
		cases := make([]string, len(node.Cases))
		for index, variant := range node.Cases {
			cases[index] = variant.Name + ":" + semanticTypeKey(variant.Payload, visiting)
		}
		return "enum(" + strings.Join(cases, ",") + ")"
	case *typeinfo.InvalidType, *typeinfo.UnknownType, *typeinfo.IntegerType,
		*typeinfo.ByteType, *typeinfo.CharType, *typeinfo.FloatType, *typeinfo.BoolType,
		*typeinfo.CStrType, *typeinfo.StringType, *typeinfo.NoneType, *typeinfo.AllocatorType,
		*typeinfo.NamedType, *typeinfo.RawPtrType:
		return typeinfo.TypeText(semantic)
	default:
		panic(fmt.Sprintf("export fingerprint: unhandled semantic type %T", semantic))
	}
}

func constantKey(value constvalue.Value) string {
	if value == nil {
		return ""
	}
	switch node := value.(type) {
	case *constvalue.IntConst:
		return node.TypeText() + ":" + node.Text()
	case *constvalue.FloatConst:
		return node.TypeText() + ":" + node.Text()
	case *constvalue.BoolConst:
		return fmt.Sprintf("bool:%t", node.Bool())
	case *constvalue.StringConst:
		return node.TypeText() + ":" + fmt.Sprintf("%q", node.Text())
	case *constvalue.VariantConst:
		values := node.FieldValues()
		fields := make([]string, len(values))
		for index, field := range values {
			fields[index] = constantKey(field)
		}
		return fmt.Sprintf("variant(%s:%s:%d:%s)", node.NominalIdentity(), node.TypeText(), node.CaseIndex(), strings.Join(fields, ","))
	default:
		panic(fmt.Sprintf("export fingerprint: unhandled constant %T", value))
	}
}
