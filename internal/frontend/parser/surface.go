// This file builds syntax-level module surfaces used by incremental compilation.
// A surface describes imports and declaration shapes that can affect another
// module: names, type parameters, parameter and return types, fields, enum
// payloads, and default expressions. Function bodies and other implementation
// details are deliberately excluded, so a body-only edit invalidates its module
// without needlessly invalidating dependents.
//
// Parsing owns this first fingerprint layer because ParseModule already visits
// every top-level import and declaration exactly once. Each declaration keeps
// its serialized surface on its AST node; moduleSurface collects those surfaces
// into stable ImportFingerprint and ExportFingerprint values. LSP workspace
// compares these fingerprints when deciding whether change must propagate
// through reverse dependency graph. Later semantic analysis combines same declaration surface
// with resolved types, constants, attributes, and link metadata to form semantic
// export fingerprint. Keeping syntax and semantic layers separate lets editor
// reuse parsed modules while still detecting API changes that require rechecking
// dependents.

package parser

import (
	"fmt"
	"strings"

	"compiler/internal/frontend/ast"
)

type moduleSurface struct {
	imports []string
	exports []string
}

func (s *moduleSurface) addImport(path string) {
	if path == "" {
		return
	}
	s.imports = append(s.imports, path)
}

func (s *moduleSurface) addDecl(decl ast.Decl) {
	if s == nil || decl == nil {
		return
	}
	if surface := decl.GetDeclSurface(); surface != "" {
		s.exports = append(s.exports, surface)
	}
}

// Parser owns source-surface shape because it already touches each top-level
// declaration exactly once while building the AST.
func (s *moduleSurface) finish(mod *ast.Module) {
	if s == nil || mod == nil {
		return
	}
	mod.ImportFingerprint = ast.FingerprintParts(s.imports)
	mod.ExportFingerprint = ast.FingerprintParts(s.exports)
}

func setDeclSurface[T ast.Decl](decl T, surface string) T {
	decl.SetDeclSurface(surface)
	return decl
}

func fnDeclSurface(prefix string, fn *ast.FnDecl) string {
	if fn == nil || fn.Name == nil {
		return prefix + ":"
	}
	receiver := ""
	if fn.Receiver != nil {
		receiver = paramSurface([]ast.Param{*fn.Receiver})[0]
	}
	return prefix + ":" + receiver + ":" + fn.Name.Name + ":" + strings.Join(typeParamNames(fn.TypeParams), ",") + ":" + strings.Join(paramSurface(fn.Params), ",") + ":" + ast.TypeText(fn.ReturnType) + fn.ReturnOrigins.Text()
}

func structDeclSurface(decl *ast.StructDecl) string {
	if decl == nil || decl.Name == nil {
		return ""
	}
	structType, ok := decl.Type.(*ast.StructType)
	if !ok || structType == nil {
		return ""
	}
	fields := make([]string, 0, len(structType.Fields))
	for _, field := range structType.Fields {
		if field.Name == nil {
			continue
		}
		fields = append(fields, field.Name.Name+":"+ast.TypeText(field.Type))
	}
	return "struct:" + decl.Name.Name + ":" + strings.Join(fields, ",")
}

func interfaceDeclSurface(decl *ast.InterfaceDecl) string {
	if decl == nil || decl.Name == nil {
		return ""
	}
	ifaceType, ok := decl.Type.(*ast.InterfaceType)
	if !ok || ifaceType == nil {
		return ""
	}
	methods := make([]string, 0, len(ifaceType.Methods))
	for _, method := range ifaceType.Methods {
		methods = append(methods, typeMethodSurface("method", method))
	}
	return "iface:" + decl.Name.Name + ":" + strings.Join(methods, ";")
}

func enumDeclSurface(decl *ast.EnumDecl) string {
	if decl == nil || decl.Name == nil {
		return ""
	}
	enumType, ok := decl.Type.(*ast.EnumType)
	if !ok || enumType == nil {
		return ""
	}
	variants := make([]string, 0, len(enumType.Variants))
	for _, variant := range enumType.Variants {
		if variant.Name == nil {
			continue
		}
		if variant.Payload == nil {
			variants = append(variants, variant.Name.Name)
			continue
		}
		variants = append(variants, variant.Name.Name+":"+ast.TypeText(variant.Payload))
	}
	return "enum:" + decl.Name.Name + ":" + strings.Join(variants, ",")
}

func typeAliasDeclSurface(decl *ast.TypeAliasDecl) string {
	if decl == nil || decl.Name == nil {
		return ""
	}
	return "type:" + decl.Name.Name + ":" + ast.TypeText(decl.Type)
}

func constDeclSurface(decl *ast.ConstDecl) string {
	if decl == nil || decl.Name == nil {
		return ""
	}
	valueShape := ""
	if decl.Type == nil {
		valueShape = fmt.Sprintf(":%T", decl.Value)
	}
	return "const:" + decl.Name.Name + ":" + ast.TypeText(decl.Type) + valueShape
}

func letDeclSurface(decl *ast.LetDecl) string {
	if decl == nil || decl.Name == nil || !decl.IsModuleVar {
		return ""
	}
	valueShape := ""
	if decl.Type == nil {
		valueShape = fmt.Sprintf(":%T", decl.Value)
	}
	return "let:" + decl.Name.Name + ":" + ast.TypeText(decl.Type) + valueShape
}

func typeMethodSurface(prefix string, method ast.TypeMethod) string {
	name := ""
	if method.Name != nil {
		name = method.Name.Name
	}
	receiver := ""
	if method.Receiver != nil {
		receiver = paramSurface([]ast.Param{*method.Receiver})[0]
	}
	return prefix + ":" + receiver + ":" + name + ":" + strings.Join(typeParamNames(method.TypeParams), ",") + ":" + strings.Join(paramSurface(method.Params), ",") + ":" + ast.TypeText(method.ReturnType) + method.ReturnOrigins.Text()
}

func typeParamNames(typeParams []ast.TypeParam) []string {
	names := make([]string, 0, len(typeParams))
	for _, tp := range typeParams {
		if tp.Name != nil {
			names = append(names, tp.Name.Name)
		}
	}
	return names
}

func paramSurface(params []ast.Param) []string {
	out := make([]string, 0, len(params))
	for _, param := range params {
		name := ""
		if param.Name != nil {
			name = param.Name.Name
		}
		prefix := ""
		if param.IsMutable {
			prefix = "mut "
		}
		text := prefix + name + ":" + ast.TypeText(param.Type)
		if param.Default != nil {
			text += "=" + ast.ExprText(param.Default)
		}
		out = append(out, text)
	}
	return out
}
