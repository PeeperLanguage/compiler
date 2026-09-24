package mir

import (
	"testing"

	"compiler/internal/frontend/ast"
	"compiler/internal/ir"
	"compiler/internal/ir/exprlower"
	"compiler/internal/ir/thir"
	"compiler/internal/moduleid"
	"compiler/internal/semantics/ownershipresult"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
)

func TestDiscardedCallDropsTemporariesBeforeResult(t *testing.T) {
	integer := &typeinfo.IntegerType{IsSigned: true, Bits: 32}
	borrow := &typeinfo.RefType{Target: integer}
	result := &typeinfo.OwnedPtrType{Target: integer}
	callable := &typeinfo.FuncType{Params: []typeinfo.Type{borrow, borrow}, Return: result}
	symbol := symbols.New("consume", symbols.SymbolFunc, nil, nil)
	symbol.Type = callable
	call := &thir.Call{
		ExprInfo: thir.ExprInfo{Source: ir.SourceInfo{NodeID: 3}, Type: result},
		Callee:   &thir.Ident{ExprInfo: thir.ExprInfo{Source: ir.SourceInfo{NodeID: 4}, Type: callable}, Symbol: symbol, Name: symbol.Name},
		Args: []thir.Expr{
			&thir.NumberLiteral{ExprInfo: thir.ExprInfo{Source: ir.SourceInfo{NodeID: 5}, Type: integer, ImplicitReference: borrow}, Value: "1"},
			&thir.NumberLiteral{ExprInfo: thir.ExprInfo{Source: ir.SourceInfo{NodeID: 6}, Type: integer, ImplicitReference: borrow}, Value: "2"},
		},
	}
	types := ir.NewTypeTable()
	block := &Block{}
	l := &lowerer{
		input: LoweringInput{Types: types}, module: &Module{Types: types}, current: block,
		cleanup: &ownershipresult.CleanupPlan{DiscardedValue: map[ir.NodeID]struct{}{3: {}}},
	}
	if !l.lowerExprStatement(call) {
		t.Fatal("discarded call failed to lower")
	}
	if len(block.Instrs) != 6 {
		t.Fatalf("instructions = %#v, want two borrows, call, and three drops", block.Instrs)
	}
	firstBorrow, ok := block.Instrs[0].(*Assign)
	if !ok {
		t.Fatalf("first instruction = %T, want borrow assignment", block.Instrs[0])
	}
	secondBorrow, ok := block.Instrs[1].(*Assign)
	if !ok {
		t.Fatalf("second instruction = %T, want borrow assignment", block.Instrs[1])
	}
	firstAddress, ok := firstBorrow.Value.(*AddrOf)
	if !ok {
		t.Fatalf("first value = %T, want address", firstBorrow.Value)
	}
	secondAddress, ok := secondBorrow.Value.(*AddrOf)
	if !ok {
		t.Fatalf("second value = %T, want address", secondBorrow.Value)
	}
	called, ok := block.Instrs[2].(*Assign)
	if !ok {
		t.Fatalf("third instruction = %T, want call assignment", block.Instrs[2])
	}
	invocation, ok := called.Value.(*Call)
	if !ok || len(invocation.Args) != 2 {
		t.Fatalf("invocation = %#v, want two arguments", called.Value)
	}
	if firstArg, ok := invocation.Args[0].(*RefName); !ok || firstArg.Name != firstBorrow.Name {
		t.Fatalf("first argument = %#v, want first borrowed temporary", invocation.Args[0])
	}
	if secondArg, ok := invocation.Args[1].(*RefName); !ok || secondArg.Name != secondBorrow.Name {
		t.Fatalf("second argument = %#v, want second borrowed temporary", invocation.Args[1])
	}
	for index, want := range []ValueRef{secondAddress.Place.Root, firstAddress.Place.Root} {
		drop, ok := block.Instrs[index+3].(*Drop)
		if !ok || drop.Value != want {
			t.Fatalf("temporary cleanup %d = %#v, want borrowed owner %#v", index, block.Instrs[index+3], want)
		}
	}
	resultDrop, ok := block.Instrs[5].(*Drop)
	if !ok {
		t.Fatalf("result cleanup = %T, want drop", block.Instrs[5])
	}
	value, ok := resultDrop.Value.(*RefName)
	if !ok || value.Name != called.Name {
		t.Fatalf("result cleanup = %#v, want %s", resultDrop.Value, called.Name)
	}
	if len(l.temporaryDrops) != 0 {
		t.Fatalf("temporary owners retained after expression: %#v", l.temporaryDrops)
	}
}

func TestExternalFunctionSignatureUsesPublishedLinkName(t *testing.T) {
	module := moduleid.ID{Origin: "local", ImportPath: "main"}
	declaration := &ast.FnDecl{Attributed: ast.Attributed{Attributes: []ast.Attribute{{
		Name: ast.AttributeExtern, Args: []ast.Expr{&ast.StringLit{Value: "native_name"}},
	}}}}
	sym := symbols.New("external", symbols.SymbolFunc, declaration, nil)
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
			sym := symbols.New(test.name, test.kind, nil, nil)
			sym.DefiningModule = module
			fn := functionSignature(input, &thir.Function{Symbol: sym}, nil)
			reference := exprlower.SymbolName(module, true, sym)
			if fn.Name != reference {
				t.Fatalf("definition %q does not match reference %q", fn.Name, reference)
			}
			other := symbols.New(test.name, test.kind, nil, nil)
			other.DefiningModule = module
			if other.ID == sym.ID || exprlower.SymbolName(module, true, other) != reference {
				t.Fatalf("callable reference depends on process-local symbol ID: %q", reference)
			}
			if test.want != "" && fn.Name != test.want {
				t.Fatalf("definition = %q, want %q", fn.Name, test.want)
			}
		})
	}
}
