package project

import (
	"testing"

	"compiler/internal/constvalue"
	"compiler/internal/frontend/ast"
	"compiler/internal/module"
	"compiler/internal/moduleid"
	"compiler/internal/semantics/bindingresult"
	"compiler/internal/semantics/constantresult"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
)

func fingerprintModule(
	t *testing.T,
	exported *symbols.Symbol,
	bindings *bindingresult.Result,
	constValues map[symbols.SymbolID]constvalue.Value,
) *module.Module {
	t.Helper()
	scope := symbols.NewScope(nil)
	if err := scope.Declare(exported); err != nil {
		t.Fatalf("declare export: %v", err)
	}
	if bindings == nil {
		bindings = bindingresult.New()
	}
	constants := constantresult.New()
	for id, value := range constValues {
		constants.Publish(id, value)
	}
	return &module.Module{ModuleScope: scope, Bindings: bindings, Constants: constants}
}

func TestSemanticExportFingerprintChangesWithInferredTypeAndValue(t *testing.T) {
	makeConst := func(typ typeinfo.Type, value string) string {
		decl := &ast.ConstDecl{Name: &ast.Ident{Name: "Value"}}
		decl.SetDeclSurface("const:Value::number")
		sym := symbols.New("Value", symbols.SymbolConst, decl, nil)
		sym.Type = typ
		constValues := make(map[symbols.SymbolID]constvalue.Value)
		constValues[sym.ID], _ = constvalue.NewIntText(value, typeinfo.TypeText(typ))
		return SemanticExportFingerprint(nil, fingerprintModule(t, sym, nil, constValues))
	}

	i32One := makeConst(&typeinfo.IntegerType{IsSigned: true, Bits: 32}, "1")
	i64One := makeConst(&typeinfo.IntegerType{IsSigned: true, Bits: 64}, "1")
	i32Two := makeConst(&typeinfo.IntegerType{IsSigned: true, Bits: 32}, "2")
	if i32One == i64One {
		t.Fatal("inferred export width did not change semantic fingerprint")
	}
	if i32One == i32Two {
		t.Fatal("exported const value did not change semantic fingerprint")
	}
}

func TestSemanticExportFingerprintIncludesConstValueWithoutBindings(t *testing.T) {
	fingerprint := func(value string) string {
		decl := &ast.ConstDecl{Name: &ast.Ident{Name: "Value"}}
		decl.SetDeclSurface("const:Value::number")
		sym := symbols.New("Value", symbols.SymbolConst, decl, nil)
		sym.Type = &typeinfo.IntegerType{IsSigned: true, Bits: 32}
		scope := symbols.NewScope(nil)
		if err := scope.Declare(sym); err != nil {
			t.Fatalf("declare export: %v", err)
		}
		constant, _ := constvalue.NewIntText(value, "i32")
		constants := constantresult.New()
		constants.Publish(sym.ID, constant)
		return SemanticExportFingerprint(nil, &module.Module{ModuleScope: scope, Constants: constants})
	}
	if fingerprint("1") == fingerprint("2") {
		t.Fatal("binding-independent const value did not change semantic fingerprint")
	}
}

func TestSemanticExportFingerprintIgnoresQueryCache(t *testing.T) {
	fingerprint := func(value string) string {
		decl := &ast.ConstDecl{Name: &ast.Ident{Name: "Value"}}
		decl.SetDeclSurface("const:Value::number")
		sym := symbols.New("Value", symbols.SymbolConst, decl, nil)
		sym.Type = &typeinfo.IntegerType{IsSigned: true, Bits: 32}
		scope := symbols.NewScope(nil)
		if err := scope.Declare(sym); err != nil {
			t.Fatalf("declare export: %v", err)
		}
		constant, _ := constvalue.NewIntText(value, "i32")
		constants := constantresult.New()
		constants.Cache(sym.ID, constant)
		return SemanticExportFingerprint(nil, &module.Module{ModuleScope: scope, Constants: constants})
	}
	if fingerprint("1") != fingerprint("2") {
		t.Fatal("query-cache-only value changed semantic fingerprint")
	}
}

func TestSemanticExportFingerprintIgnoresFunctionBodyChanges(t *testing.T) {
	makeFunction := func(body *ast.BlockStmt) string {
		decl := &ast.FnDecl{Name: &ast.Ident{Name: "Read"}, Body: body}
		decl.SetDeclSurface("fn::Read:::")
		sym := symbols.New("Read", symbols.SymbolFunc, decl, nil)
		sym.Type = &typeinfo.FuncType{Return: &typeinfo.IntegerType{IsSigned: true, Bits: 32}}
		return SemanticExportFingerprint(nil, fingerprintModule(t, sym, nil, nil))
	}
	first := makeFunction(&ast.BlockStmt{})
	second := makeFunction(&ast.BlockStmt{Stmts: []ast.Stmt{&ast.ReturnStmt{Value: &ast.NumberLit{Value: "1"}}}})
	if first != second {
		t.Fatal("body-only change altered semantic export fingerprint")
	}
}

func TestSemanticExportFingerprintIncludesPrivateFactsUsedByPublicDefault(t *testing.T) {
	makeFunction := func(value string) string {
		defaultIdent := &ast.Ident{NodeIDHolder: ast.NodeIDHolder{NodeID: 20}, Name: "limit"}
		decl := &ast.FnDecl{
			Name:   &ast.Ident{Name: "Read"},
			Params: []ast.Param{{Name: &ast.Ident{Name: "value"}, Default: defaultIdent}},
		}
		decl.SetDeclSurface("fn::Read::value:i32=limit:")
		fn := symbols.New("Read", symbols.SymbolFunc, decl, nil)
		i32 := &typeinfo.IntegerType{IsSigned: true, Bits: 32}
		fn.Type = &typeinfo.FuncType{Params: []typeinfo.Type{i32}, ParamNames: []string{"value"}}
		private := symbols.New("limit", symbols.SymbolConst, nil, nil)
		private.Type = i32
		bindings := bindingresult.New()
		bindings.Bind(defaultIdent, private)
		constValues := make(map[symbols.SymbolID]constvalue.Value)
		constValues[private.ID], _ = constvalue.NewIntText(value, "i32")
		return SemanticExportFingerprint(nil, fingerprintModule(t, fn, bindings, constValues))
	}
	if makeFunction("1") == makeFunction("2") {
		t.Fatal("private const used by public default did not change fingerprint")
	}
}

func TestSemanticExportFingerprintTracksImportedConstantInDefault(t *testing.T) {
	// An exported default referencing an imported constant must still change when
	// that constant changes. Foreign values live only in the owning module, so the
	// fingerprint has to resolve them through the defining identity.
	makeFingerprint := func(value string) string {
		ctx := New(".", ".peep", nil)
		i32 := &typeinfo.IntegerType{IsSigned: true, Bits: 32}
		ownerID := moduleid.ID{Origin: string(ModuleOriginLocal), ImportPath: "lib"}
		imported := symbols.New("K", symbols.SymbolConst, nil, nil)
		imported.Type = i32
		imported.DefiningModule = ownerID
		ownerConstants := constantresult.New()
		published, _ := constvalue.NewIntText(value, "i32")
		ownerConstants.Publish(imported.ID, published)
		ctx.AddModule(&module.Module{ID: ownerID, FilePath: "lib.peep", Constants: ownerConstants})

		defaultIdent := &ast.Ident{Name: "K"}
		decl := &ast.FnDecl{
			Name:   &ast.Ident{Name: "Read"},
			Params: []ast.Param{{Name: &ast.Ident{Name: "value"}, Default: defaultIdent}},
		}
		decl.SetDeclSurface("fn::Read::value:i32=K:")
		fn := symbols.New("Read", symbols.SymbolFunc, decl, nil)
		fn.Type = &typeinfo.FuncType{Params: []typeinfo.Type{i32}, ParamNames: []string{"value"}}
		bindings := bindingresult.New()
		bindings.Bind(defaultIdent, imported)

		consumer := fingerprintModule(t, fn, bindings, nil)
		consumer.ID = moduleid.ID{Origin: string(ModuleOriginLocal), ImportPath: "app"}
		consumer.FilePath = "app.peep"
		return SemanticExportFingerprint(ctx, consumer)
	}
	if makeFingerprint("1") == makeFingerprint("2") {
		t.Fatal("imported const used by public default did not change fingerprint")
	}
}

func TestSemanticExportFingerprintChangesWithPublicMethodSignature(t *testing.T) {
	makeMethod := func(returnType typeinfo.Type) string {
		method := symbols.New("Read", symbols.SymbolMethod, nil, nil)
		method.Type = &typeinfo.FuncType{Return: returnType}
		receiver := &typeinfo.DefinedType{Name: "Buffer", Identity: "test::Buffer", Kind: typeinfo.DefinedKindStruct, Underlying: &typeinfo.StructType{}}
		bindings := bindingresult.New()
		bindings.RegisterMethod(receiver, method)
		typeSymbol := symbols.New("Buffer", symbols.SymbolType, nil, nil)
		typeSymbol.Type = receiver
		return SemanticExportFingerprint(nil, fingerprintModule(t, typeSymbol, bindings, nil))
	}
	i32 := &typeinfo.IntegerType{IsSigned: true, Bits: 32}
	i64 := &typeinfo.IntegerType{IsSigned: true, Bits: 64}
	if makeMethod(i32) == makeMethod(i64) {
		t.Fatal("public method signature did not change semantic fingerprint")
	}
}

func TestSemanticExportFingerprintHandlesRecursiveTypesDeterministically(t *testing.T) {
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
		return SemanticExportFingerprint(nil, fingerprintModule(t, sym, nil, nil))
	}
	if first, second := makeType(), makeType(); first == "" || first != second {
		t.Fatalf("recursive fingerprints unstable: %q, %q", first, second)
	}
}
