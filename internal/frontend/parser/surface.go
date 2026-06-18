package parser

import (
	"fmt"
	"strings"

	"compiler/internal/frontend/ast"
	ir "compiler/internal/ir"
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
	return prefix + ":" + fn.Name.Name + ":" + strings.Join(typeParamNames(fn.TypeParams), ",") + ":" + strings.Join(paramSurface(fn.Params), ",") + ":" + ir.TypeText(fn.ReturnType)
}

func structDeclSurface(decl *ast.StructDecl) string {
	if decl == nil || decl.Name == nil {
		return ""
	}
	fields := make([]string, 0, len(decl.Fields))
	for _, field := range decl.Fields {
		if field.Name == nil {
			continue
		}
		fields = append(fields, field.Name.Name+":"+ir.TypeText(field.Type))
	}
	return "struct:" + decl.Name.Name + ":" + strings.Join(fields, ",")
}

func interfaceDeclSurface(decl *ast.InterfaceDecl) string {
	if decl == nil || decl.Name == nil {
		return ""
	}
	methods := make([]string, 0, len(decl.Methods))
	for _, method := range decl.Methods {
		methods = append(methods, typeMethodSurface("method", method))
	}
	return "interface:" + decl.Name.Name + ":" + strings.Join(methods, ";")
}

func enumDeclSurface(decl *ast.EnumDecl) string {
	if decl == nil || decl.Name == nil {
		return ""
	}
	variants := make([]string, 0, len(decl.Variants))
	for _, variant := range decl.Variants {
		if variant.Name == nil {
			continue
		}
		variants = append(variants, variant.Name.Name)
	}
	return "enum:" + decl.Name.Name + ":" + strings.Join(variants, ",")
}

func typeAliasDeclSurface(decl *ast.TypeAliasDecl) string {
	if decl == nil || decl.Name == nil {
		return ""
	}
	return "type:" + decl.Name.Name + ":" + ir.TypeText(decl.Type)
}

func constDeclSurface(decl *ast.ConstDecl) string {
	if decl == nil || decl.Name == nil {
		return ""
	}
	valueShape := ""
	if decl.Type == nil {
		valueShape = fmt.Sprintf(":%T", decl.Value)
	}
	return "const:" + decl.Name.Name + ":" + ir.TypeText(decl.Type) + valueShape
}

func letDeclSurface(decl *ast.LetDecl) string {
	if decl == nil || decl.Name == nil || !decl.IsModuleVar {
		return ""
	}
	valueShape := ""
	if decl.Type == nil {
		valueShape = fmt.Sprintf(":%T", decl.Value)
	}
	return "let:" + decl.Name.Name + ":" + ir.TypeText(decl.Type) + valueShape
}

func implDeclSurface(decl *ast.ImplDecl) string {
	if decl == nil {
		return ""
	}
	methods := make([]string, 0, len(decl.Methods))
	for _, method := range decl.Methods {
		methods = append(methods, fnDeclSurface("method", method))
	}
	return "impl:" + ir.TypeText(decl.Target) + ":" + strings.Join(methods, ";")
}

func typeMethodSurface(prefix string, method ast.TypeMethod) string {
	name := ""
	if method.Name != nil {
		name = method.Name.Name
	}
	return prefix + ":" + name + ":" + strings.Join(typeParamNames(method.TypeParams), ",") + ":" + strings.Join(paramSurface(method.Params), ",") + ":" + ir.TypeText(method.ReturnType)
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
		out = append(out, name+":"+ir.TypeText(param.Type))
	}
	return out
}
