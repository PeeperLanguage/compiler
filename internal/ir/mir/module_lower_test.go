package mir

import (
	"testing"

	"compiler/internal/constvalue"
	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/ir"
	"compiler/internal/ir/cfg"
	"compiler/internal/ir/exprlower"
	"compiler/internal/ir/thir"
	"compiler/internal/moduleid"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
	"compiler/internal/source"
)

func TestExternalFunctionSignatureUsesPublishedLinkName(t *testing.T) {
	module := moduleid.ID{Origin: "local", ImportPath: "main"}
	declaration := &ast.FnDecl{Attributed: ast.Attributed{Attributes: []ast.Attribute{{
		Name: ast.AttributeExtern, Args: []ast.Expr{&ast.StringLit{Value: "native_name"}},
	}}}}
	sym := symbols.New(symbols.ProjectedSymbolID(symbols.SymbolFunc, "external"), "external", symbols.SymbolFunc, declaration, nil)
	sym.DefiningModule = module
	sym.ASTNode = nil
	input := LoweringInput{ModuleID: module, Types: ir.NewTypeTable()}
	signature := functionSignature(input, &thir.Function{Symbol: sym}, nil)
	if signature.Name != "native_name" || exprlower.SymbolName(module, false, sym) != signature.Name {
		t.Fatalf("external definition %q differs from reference %q", signature.Name, exprlower.SymbolName(module, false, sym))
	}
}

func TestFunctionSignatureMatchesCallableReferences(t *testing.T) {
	module := moduleid.ID{Origin: "local", ImportPath: "main"}
	input := LoweringInput{ModuleID: module, IsEntryModule: true, Types: ir.NewTypeTable()}
	for _, test := range []struct {
		name string
		kind symbols.Kind
		want string
	}{
		{name: "helper", kind: symbols.SymbolFunc},
		{name: "read", kind: symbols.SymbolMethod},
		{name: "main", kind: symbols.SymbolFunc, want: "main"},
	} {
		t.Run(test.name, func(t *testing.T) {
			identity := symbols.ProjectedSymbolID(test.kind, test.name)
			sym := symbols.New(identity, test.name, test.kind, nil, nil)
			sym.DefiningModule = module
			fn := functionSignature(input, &thir.Function{Symbol: sym}, nil)
			reference := exprlower.SymbolName(module, true, sym)
			if fn.Name != reference {
				t.Fatalf("definition %q does not match reference %q", fn.Name, reference)
			}
			other := symbols.New(identity, test.name, test.kind, nil, nil)
			other.DefiningModule = module
			if other.ID != sym.ID || exprlower.SymbolName(module, true, other) != reference {
				t.Fatalf("callable reference depends on symbol allocation: %q", reference)
			}
			if test.want != "" && fn.Name != test.want {
				t.Fatalf("definition = %q, want %q", fn.Name, test.want)
			}
		})
	}
}

func TestGenerateMIREmitsStaticConstantFromSymbolIndex(t *testing.T) {
	integer := &typeinfo.IntegerType{IsSigned: true, Bits: 32}
	sym := symbols.New(symbols.ProjectedSymbolID(symbols.SymbolConst, "Value"), "Value", symbols.SymbolConst, nil, nil)
	sym.Type = integer
	scope := symbols.NewScope(nil)
	if err := scope.Declare(sym); err != nil {
		t.Fatalf("declare constant: %v", err)
	}
	index := symbols.NewIndex()
	value, ok := constvalue.NewIntText("7", "i32")
	if !ok {
		t.Fatal("failed to construct constant")
	}
	index.PublishConstant(sym.ID, value)
	types := ir.NewTypeTable()
	sourceModule := thir.NewModule("test", "test.peep", nil)
	out := GenerateMIR(LoweringInput{
		Types: types, Source: sourceModule, CFG: &cfg.Module{}, Scope: scope, SymbolIndex: index,
	})
	if out == nil || len(out.StaticData) != 1 {
		t.Fatalf("static data = %#v, want one entry", out)
	}
	if out.StaticData[0].Constant != value {
		t.Fatalf("static constant = %#v, want %#v", out.StaticData[0].Constant, value)
	}
}

func TestGenerateMIRRejectsUnmaterializableModuleConstants(t *testing.T) {
	for _, test := range []struct {
		name, valueType, code string
		internValueType       bool
	}{
		{name: "missing value", code: diagnostics.ErrInvalidOperation},
		{name: "mismatched value", valueType: "i8", code: diagnostics.ErrInvalidEvidence},
		{name: "mismatched interned value", valueType: "i8", code: diagnostics.ErrInvalidEvidence, internValueType: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			symbol := symbols.New(symbols.ProjectedSymbolID(symbols.SymbolConst, "Value"), "Value", symbols.SymbolConst, nil, nil)
			symbol.BindType(&typeinfo.IntegerType{IsSigned: true, Bits: 64})
			symbol.Location = source.NewLocation("test.peep", source.Position{Line: 1, Column: 1}, source.Position{Line: 1, Column: 6})
			scope := symbols.NewScope(nil)
			if err := scope.Declare(symbol); err != nil {
				t.Fatal(err)
			}
			index := symbols.NewIndex()
			if test.valueType != "" {
				value, ok := constvalue.NewIntText("2", test.valueType)
				if !ok {
					t.Fatal("cannot construct test constant")
				}
				index.PublishConstant(symbol.ID, value)
			}
			types := ir.NewTypeTable()
			if test.internValueType {
				types.Intern(ir.Type{Kind: ir.TypeInteger, IsSigned: true, Bits: 8})
			}
			diag := diagnostics.NewDiagnosticBag()
			out := GenerateMIR(LoweringInput{
				Types: types, Diagnostics: diag, Source: thir.NewModule("test", "test.peep", nil),
				CFG: &cfg.Module{}, Scope: scope, SymbolIndex: index,
			})
			if out != nil || !diag.HasErrors() {
				t.Fatalf("MIR = %#v, diagnostics = %s; want materialization rejection", out, diag.EmitAllToString())
			}
			items := diag.Diagnostics()
			if len(items) != 1 || items[0].Code != test.code || len(items[0].Labels) != 1 || items[0].Labels[0].Location != symbol.Location {
				t.Fatalf("diagnostics = %#v, want %s at constant declaration", items, test.code)
			}
		})
	}
}
