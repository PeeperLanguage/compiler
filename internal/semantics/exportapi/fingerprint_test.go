package exportapi

import (
	"testing"

	"compiler/internal/constvalue"
	"compiler/internal/frontend/ast"
	"compiler/internal/project"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/table"
	"compiler/internal/semantics/typeinfo"
)

func fingerprintModule(t *testing.T, exported *symbols.Symbol, semantics *project.SemanticInfo) *project.Module {
	t.Helper()
	scope := table.New(nil)
	if err := scope.Declare(exported); err != nil {
		t.Fatalf("declare export: %v", err)
	}
	if semantics == nil {
		semantics = project.NewSemanticInfo()
	}
	return &project.Module{ModuleScope: scope, Semantics: semantics}
}

func TestFingerprintChangesWithInferredExportTypeAndValue(t *testing.T) {
	makeConst := func(typ typeinfo.Type, value string) string {
		decl := &ast.ConstDecl{Name: &ast.Ident{Name: "Value"}}
		decl.SetDeclSurface("const:Value::number")
		sym := symbols.New("Value", symbols.SymbolConst, decl, nil)
		sym.Type = typ
		semantic := project.NewSemanticInfo()
		semantic.ConstValues[sym.ID], _ = constvalue.NewIntText(value, typeinfo.TypeText(typ))
		return Fingerprint(fingerprintModule(t, sym, semantic))
	}

	i32One := makeConst(&typeinfo.IntegerType{Signed: true, Bits: 32}, "1")
	i64One := makeConst(&typeinfo.IntegerType{Signed: true, Bits: 64}, "1")
	i32Two := makeConst(&typeinfo.IntegerType{Signed: true, Bits: 32}, "2")
	if i32One == i64One {
		t.Fatal("inferred export width did not change semantic fingerprint")
	}
	if i32One == i32Two {
		t.Fatal("exported const value did not change semantic fingerprint")
	}
}

func TestFingerprintIgnoresFunctionBodyChanges(t *testing.T) {
	makeFunction := func(body *ast.BlockStmt) string {
		decl := &ast.FnDecl{Name: &ast.Ident{Name: "Read"}, Body: body}
		decl.SetDeclSurface("fn::Read:::")
		sym := symbols.New("Read", symbols.SymbolFunc, decl, nil)
		sym.Type = &typeinfo.FuncType{Return: &typeinfo.IntegerType{Signed: true, Bits: 32}}
		return Fingerprint(fingerprintModule(t, sym, nil))
	}
	first := makeFunction(&ast.BlockStmt{})
	second := makeFunction(&ast.BlockStmt{Stmts: []ast.Stmt{&ast.ReturnStmt{Value: &ast.NumberLit{Value: "1"}}}})
	if first != second {
		t.Fatal("body-only change altered semantic export fingerprint")
	}
}

func TestFingerprintIncludesPrivateFactsUsedByPublicDefault(t *testing.T) {
	makeFunction := func(value string) string {
		defaultIdent := &ast.Ident{NodeIDHolder: ast.NodeIDHolder{NodeID: 20}, Name: "limit"}
		decl := &ast.FnDecl{
			Name:   &ast.Ident{Name: "Read"},
			Params: []ast.Param{{Name: &ast.Ident{Name: "value"}, Default: defaultIdent}},
		}
		decl.SetDeclSurface("fn::Read::value:i32=limit:")
		fn := symbols.New("Read", symbols.SymbolFunc, decl, nil)
		i32 := &typeinfo.IntegerType{Signed: true, Bits: 32}
		fn.Type = &typeinfo.FuncType{Params: []typeinfo.Type{i32}, ParamNames: []string{"value"}}
		private := symbols.New("limit", symbols.SymbolConst, nil, nil)
		private.Type = i32
		semantic := project.NewSemanticInfo()
		semantic.ResolvedSymbols[defaultIdent.ID()] = private
		semantic.ConstValues[private.ID], _ = constvalue.NewIntText(value, "i32")
		return Fingerprint(fingerprintModule(t, fn, semantic))
	}
	if makeFunction("1") == makeFunction("2") {
		t.Fatal("private const used by public default did not change fingerprint")
	}
}

func TestFingerprintChangesWithPublicMethodSignature(t *testing.T) {
	makeMethod := func(returnType typeinfo.Type) string {
		method := symbols.New("Read", symbols.SymbolMethod, nil, nil)
		method.Type = &typeinfo.FuncType{Return: returnType}
		semantics := project.NewSemanticInfo()
		semantics.MethodSets["Buffer"] = []*symbols.Symbol{method}
		return Fingerprint(fingerprintModule(t,
			symbols.New("Buffer", symbols.SymbolType, nil, nil), semantics))
	}
	i32 := &typeinfo.IntegerType{Signed: true, Bits: 32}
	i64 := &typeinfo.IntegerType{Signed: true, Bits: 64}
	if makeMethod(i32) == makeMethod(i64) {
		t.Fatal("public method signature did not change semantic fingerprint")
	}
}

func TestFingerprintHandlesRecursiveExportTypesDeterministically(t *testing.T) {
	makeType := func() string {
		defined := &typeinfo.DefinedType{Name: "Node"}
		defined.Underlying = &typeinfo.StructType{Fields: []typeinfo.Field{{
			Name: "next",
			Type: &typeinfo.RefType{Target: defined},
		}}}
		decl := &ast.TypeAliasDecl{Name: &ast.Ident{Name: "Node"}}
		decl.SetDeclSurface("type:Node:recursive")
		sym := symbols.New("Node", symbols.SymbolType, decl, nil)
		sym.Type = defined
		return Fingerprint(fingerprintModule(t, sym, nil))
	}
	if first, second := makeType(), makeType(); first == "" || first != second {
		t.Fatalf("recursive fingerprints unstable: %q, %q", first, second)
	}
}
