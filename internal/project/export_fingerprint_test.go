package project

import (
	"testing"

	"compiler/internal/constvalue"
	"compiler/internal/frontend/ast"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
)

type unexpectedSemanticType struct{}

func (*unexpectedSemanticType) TypeNode()    {}
func (*unexpectedSemanticType) Text() string { return "unexpected" }

func fingerprintModule(t *testing.T, exported *symbols.Symbol, semantics *SemanticInfo) *Module {
	t.Helper()
	scope := symbols.NewScope(nil)
	if err := scope.Declare(exported); err != nil {
		t.Fatalf("declare export: %v", err)
	}
	if semantics == nil {
		semantics = NewSemanticInfo()
	}
	return &Module{ModuleScope: scope, Semantics: semantics}
}

func TestSemanticExportFingerprintChangesWithInferredTypeAndValue(t *testing.T) {
	makeConst := func(typ typeinfo.Type, value string) string {
		decl := &ast.ConstDecl{Name: &ast.Ident{Name: "Value"}}
		decl.SetDeclSurface("const:Value::number")
		sym := symbols.New("Value", symbols.SymbolConst, decl, nil)
		sym.Type = typ
		semantic := NewSemanticInfo()
		semantic.ConstValues[sym.ID], _ = constvalue.NewIntText(value, typeinfo.TypeText(typ))
		return SemanticExportFingerprint(fingerprintModule(t, sym, semantic))
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

func TestSemanticExportFingerprintIgnoresFunctionBodyChanges(t *testing.T) {
	makeFunction := func(body *ast.BlockStmt) string {
		decl := &ast.FnDecl{Name: &ast.Ident{Name: "Read"}, Body: body}
		decl.SetDeclSurface("fn::Read:::")
		sym := symbols.New("Read", symbols.SymbolFunc, decl, nil)
		sym.Type = &typeinfo.FuncType{Return: &typeinfo.IntegerType{Signed: true, Bits: 32}}
		return SemanticExportFingerprint(fingerprintModule(t, sym, nil))
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
		i32 := &typeinfo.IntegerType{Signed: true, Bits: 32}
		fn.Type = &typeinfo.FuncType{Params: []typeinfo.Type{i32}, ParamNames: []string{"value"}}
		private := symbols.New("limit", symbols.SymbolConst, nil, nil)
		private.Type = i32
		semantic := NewSemanticInfo()
		semantic.ResolvedSymbols[defaultIdent.ID()] = private
		semantic.ConstValues[private.ID], _ = constvalue.NewIntText(value, "i32")
		return SemanticExportFingerprint(fingerprintModule(t, fn, semantic))
	}
	if makeFunction("1") == makeFunction("2") {
		t.Fatal("private const used by public default did not change fingerprint")
	}
}

func TestSemanticExportFingerprintChangesWithPublicMethodSignature(t *testing.T) {
	makeMethod := func(returnType typeinfo.Type) string {
		method := symbols.New("Read", symbols.SymbolMethod, nil, nil)
		method.Type = &typeinfo.FuncType{Return: returnType}
		semantics := NewSemanticInfo()
		semantics.MethodSets["Buffer"] = []*symbols.Symbol{method}
		return SemanticExportFingerprint(fingerprintModule(t,
			symbols.New("Buffer", symbols.SymbolType, nil, nil), semantics))
	}
	i32 := &typeinfo.IntegerType{Signed: true, Bits: 32}
	i64 := &typeinfo.IntegerType{Signed: true, Bits: 64}
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
		return SemanticExportFingerprint(fingerprintModule(t, sym, nil))
	}
	if first, second := makeType(), makeType(); first == "" || first != second {
		t.Fatalf("recursive fingerprints unstable: %q, %q", first, second)
	}
}

func TestSemanticTypeKeyIncludesCallableMetadata(t *testing.T) {
	i32 := &typeinfo.IntegerType{Signed: true, Bits: 32}
	left := &typeinfo.FuncType{
		Params:        []typeinfo.Type{i32},
		ParamNames:    []string{"left"},
		Return:        i32,
		ReturnOrigins: &typeinfo.ReturnOriginContract{Sources: []int{0}},
	}
	right := &typeinfo.FuncType{
		Params:        []typeinfo.Type{i32},
		ParamNames:    []string{"right"},
		Return:        i32,
		ReturnOrigins: &typeinfo.ReturnOriginContract{Sources: []int{0}},
	}
	withoutOrigin := &typeinfo.FuncType{Params: []typeinfo.Type{i32}, ParamNames: []string{"left"}, Return: i32}

	if semanticTypeKey(left, make(map[typeinfo.Type]bool)) == semanticTypeKey(right, make(map[typeinfo.Type]bool)) {
		t.Fatal("parameter name did not change semantic type key")
	}
	if semanticTypeKey(left, make(map[typeinfo.Type]bool)) == semanticTypeKey(withoutOrigin, make(map[typeinfo.Type]bool)) {
		t.Fatal("return-origin contract did not change semantic type key")
	}
}

func TestSemanticTypeKeyIncludesEnumCaseSchemas(t *testing.T) {
	i32 := &typeinfo.IntegerType{Signed: true, Bits: 32}
	i64 := &typeinfo.IntegerType{Signed: true, Bits: 64}
	makeEnum := func(fieldName string, fieldType typeinfo.Type) *typeinfo.EnumType {
		return &typeinfo.EnumType{Cases: []typeinfo.VariantCase{
			{Name: "Ready", Payload: &typeinfo.StructType{Fields: []typeinfo.Field{{Name: fieldName, Type: fieldType}}}},
			{Name: "Pending"},
		}}
	}
	base := semanticTypeKey(makeEnum("value", i32), make(map[typeinfo.Type]bool))
	if base == semanticTypeKey(makeEnum("code", i32), make(map[typeinfo.Type]bool)) {
		t.Fatal("enum payload field name did not change semantic type key")
	}
	if base == semanticTypeKey(makeEnum("value", i64), make(map[typeinfo.Type]bool)) {
		t.Fatal("enum payload field type did not change semantic type key")
	}
	if base == semanticTypeKey(&typeinfo.EnumType{Cases: []typeinfo.VariantCase{{Name: "Waiting"}}}, make(map[typeinfo.Type]bool)) {
		t.Fatal("enum case name did not change semantic type key")
	}
}

func TestSemanticTypeKeyIncludesNamedEnumIdentity(t *testing.T) {
	schema := &typeinfo.EnumType{Cases: []typeinfo.VariantCase{{Name: "Ready"}, {Name: "Waiting"}}}
	left := &typeinfo.DefinedType{
		Name: "Status", Identity: "left::Status", Kind: typeinfo.DefinedKindEnum, Underlying: schema,
	}
	right := &typeinfo.DefinedType{
		Name: "Status", Identity: "right::Status", Kind: typeinfo.DefinedKindEnum, Underlying: schema,
	}
	if semanticTypeKey(left, make(map[typeinfo.Type]bool)) == semanticTypeKey(right, make(map[typeinfo.Type]bool)) {
		t.Fatal("named enum identity did not change semantic type key")
	}
}

func TestSemanticTypeKeyRejectsUnknownType(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("semanticTypeKey accepted unknown semantic type")
		}
	}()
	semanticTypeKey(&unexpectedSemanticType{}, make(map[typeinfo.Type]bool))
}

func TestConstantKeyIncludesTypeAndValue(t *testing.T) {
	i32, _ := constvalue.NewIntText("1", "i32")
	i32Two, _ := constvalue.NewIntText("2", "i32")
	i64, _ := constvalue.NewIntText("1", "i64")
	text, _ := constvalue.NewString("a:b", "str")
	leftReady, _ := constvalue.NewVariant("left::Status", "Status", 0, []constvalue.Value{i32})
	leftWaiting, _ := constvalue.NewVariant("left::Status", "Status", 1, nil)
	rightReady, _ := constvalue.NewVariant("right::Status", "Status", 0, []constvalue.Value{i32})
	leftReadyTwo, _ := constvalue.NewVariant("left::Status", "Status", 0, []constvalue.Value{i32Two})

	if constantKey(i32) == constantKey(i64) {
		t.Fatal("integer type did not change constant key")
	}
	if got := constantKey(text); got != `str:"a:b"` {
		t.Fatalf("string constant key = %q", got)
	}
	base := constantKey(leftReady)
	for name, value := range map[string]constvalue.Value{
		"case":     leftWaiting,
		"identity": rightReady,
		"field":    leftReadyTwo,
	} {
		if base == constantKey(value) {
			t.Fatalf("variant %s did not change constant key", name)
		}
	}
}
