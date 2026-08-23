package mir

import (
	"fmt"
	"testing"

	"compiler/internal/constvalue"
	"compiler/internal/frontend/ast"
	"compiler/internal/ir"
	"compiler/internal/ir/cfg"
	"compiler/internal/ir/hir"
	"compiler/internal/semantics/ownershipresult"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
	"compiler/internal/source"
	"compiler/pkg/peeper"
)

type mirTypeFixture struct {
	table                                      *ir.TypeTable
	void, boolType, cstr, f64, i32             ir.TypeID
	rawptr, usize, ownedI32, optionalI32       ir.TypeID
	valueStruct, ownerStruct, ownedValueStruct ir.TypeID
	fixed3I32, fixed4I32, dynamicI32           ir.TypeID
	refI32, mutRefI32, refBox, refDynamicI32   ir.TypeID
	mutRefDynamicI32                           ir.TypeID
	fnI32, fnVoid, fnBox, fnOwner, fnTwoBox    ir.TypeID
}

var mirTypes = func() mirTypeFixture {
	table := ir.NewTypeTable()
	void := table.Intern(ir.Type{Kind: ir.TypeVoid})
	i32 := table.Intern(ir.Type{Kind: ir.TypeInteger, Signed: true, Bits: 32})
	boolType := table.Intern(ir.Type{Kind: ir.TypeBool})
	cstr := table.Intern(ir.Type{Kind: ir.TypeCStr})
	f64 := table.Intern(ir.Type{Kind: ir.TypeFloat, Bits: 64})
	rawptr := table.Intern(ir.Type{Kind: ir.TypeRawPtr})
	usize := table.Intern(ir.Type{Kind: ir.TypeInteger, Bits: 64})
	table.SetIndexType(usize)
	valueStruct := table.Intern(ir.Type{Kind: ir.TypeStruct, Fields: []ir.TypeField{{Name: "value", Type: i32}}})
	ownerStruct := table.Intern(ir.Type{Kind: ir.TypeStruct, Fields: []ir.TypeField{{Name: "value", Type: i32}, {Name: "ptr", Type: table.Intern(ir.Type{Kind: ir.TypeOwnedPtr, Elem: i32})}}})
	dynamicI32 := table.Intern(ir.Type{Kind: ir.TypeArray, Elem: i32})
	return mirTypeFixture{
		table:            table,
		void:             void,
		boolType:         boolType,
		cstr:             cstr,
		f64:              f64,
		i32:              i32,
		rawptr:           rawptr,
		usize:            usize,
		ownedI32:         table.Intern(ir.Type{Kind: ir.TypeOwnedPtr, Elem: i32}),
		optionalI32:      table.Intern(ir.Type{Kind: ir.TypeOptional, Elem: i32}),
		valueStruct:      valueStruct,
		ownerStruct:      ownerStruct,
		ownedValueStruct: table.Intern(ir.Type{Kind: ir.TypeOwnedPtr, Elem: valueStruct}),
		fixed3I32:        table.Intern(ir.Type{Kind: ir.TypeArray, Elem: i32, Length: "3"}),
		fixed4I32:        table.Intern(ir.Type{Kind: ir.TypeArray, Elem: i32, Length: "4"}),
		dynamicI32:       dynamicI32,
		refI32:           table.Intern(ir.Type{Kind: ir.TypeReference, Elem: i32}),
		mutRefI32:        table.Intern(ir.Type{Kind: ir.TypeReference, Mutable: true, Elem: i32}),
		refBox:           table.Intern(ir.Type{Kind: ir.TypeReference, Elem: valueStruct}),
		refDynamicI32:    table.Intern(ir.Type{Kind: ir.TypeReference, Elem: dynamicI32}),
		mutRefDynamicI32: table.Intern(ir.Type{Kind: ir.TypeReference, Mutable: true, Elem: dynamicI32}),
		fnI32:            table.Intern(ir.Type{Kind: ir.TypeFunction, Return: i32}),
		fnVoid:           table.Intern(ir.Type{Kind: ir.TypeFunction, Return: void}),
		fnBox:            table.Intern(ir.Type{Kind: ir.TypeFunction, Return: valueStruct}),
		fnOwner:          table.Intern(ir.Type{Kind: ir.TypeFunction, Return: ownerStruct}),
		fnTwoBox:         table.Intern(ir.Type{Kind: ir.TypeFunction, Params: []ir.TypeID{table.Intern(ir.Type{Kind: ir.TypeReference, Elem: valueStruct}), table.Intern(ir.Type{Kind: ir.TypeReference, Elem: valueStruct})}, Return: void}),
	}
}()

// cfgForHIR gives synthetic HIR unit fixtures source-shaped control flow.
// Production always builds CFG from typed AST before HIR exists.
func cfgForHIR(module *hir.Module) *cfg.Module {
	if module == nil {
		return nil
	}
	nextID := ir.NodeID(1)
	for _, fn := range module.Funcs {
		if fn == nil {
			continue
		}
		if fn.NodeID >= nextID {
			nextID = fn.NodeID + 1
		}
		hir.InspectStmt(fn.Body, func(stmt hir.Stmt) bool {
			if id := hir.NodeIDOf(stmt); id >= nextID {
				nextID = id + 1
			}
			return true
		})
	}
	assignID := func(id ir.NodeID) ir.NodeID {
		if id != 0 {
			return id
		}
		id = nextID
		nextID++
		return id
	}
	var sourceStmt func(hir.Stmt) ast.Stmt
	var sourceBlock func(*hir.Block) *ast.BlockStmt
	sourceBlock = func(block *hir.Block) *ast.BlockStmt {
		if block == nil {
			return nil
		}
		block.NodeID = assignID(block.NodeID)
		out := &ast.BlockStmt{NodeIDHolder: ast.NodeIDHolder{NodeID: ast.NodeID(block.NodeID)}, Location: block.Location}
		out.Stmts = make([]ast.Stmt, 0, len(block.Stmts))
		for _, stmt := range block.Stmts {
			out.Stmts = append(out.Stmts, sourceStmt(stmt))
		}
		return out
	}
	sourceStmt = func(stmt hir.Stmt) ast.Stmt {
		switch node := stmt.(type) {
		case nil:
			return nil
		case *hir.Block:
			return sourceBlock(node)
		case *hir.Binding:
			node.NodeID = assignID(node.NodeID)
			return &ast.BadStmt{NodeIDHolder: ast.NodeIDHolder{NodeID: ast.NodeID(node.NodeID)}, Location: node.Location}
		case *hir.ExprStmt:
			node.NodeID = assignID(node.NodeID)
			return &ast.BadStmt{NodeIDHolder: ast.NodeIDHolder{NodeID: ast.NodeID(node.NodeID)}, Location: node.Location}
		case *hir.Assign:
			node.NodeID = assignID(node.NodeID)
			return &ast.BadStmt{NodeIDHolder: ast.NodeIDHolder{NodeID: ast.NodeID(node.NodeID)}, Location: node.Location}
		case *hir.Invalid:
			node.NodeID = assignID(node.NodeID)
			return &ast.BadStmt{NodeIDHolder: ast.NodeIDHolder{NodeID: ast.NodeID(node.NodeID)}, Location: node.Location}
		case *hir.Return:
			node.NodeID = assignID(node.NodeID)
			return &ast.ReturnStmt{NodeIDHolder: ast.NodeIDHolder{NodeID: ast.NodeID(node.NodeID)}, Location: node.Location}
		case *hir.If:
			node.NodeID = assignID(node.NodeID)
			conditionID := assignID(0)
			return &ast.IfStmt{
				NodeIDHolder: ast.NodeIDHolder{NodeID: ast.NodeID(node.NodeID)},
				Cond:         &ast.BoolLit{NodeIDHolder: ast.NodeIDHolder{NodeID: ast.NodeID(conditionID)}, Value: true, Location: node.Location},
				Then:         sourceBlock(node.Then),
				Else:         sourceStmt(node.Else),
				Location:     node.Location,
			}
		case *hir.For:
			node.NodeID = assignID(node.NodeID)
			var condition ast.Expr
			if node.Cond != nil {
				conditionID := assignID(0)
				condition = &ast.BoolLit{NodeIDHolder: ast.NodeIDHolder{NodeID: ast.NodeID(conditionID)}, Value: true, Location: node.Location}
			}
			return &ast.ForStmt{
				NodeIDHolder: ast.NodeIDHolder{NodeID: ast.NodeID(node.NodeID)},
				Cond:         condition,
				Body:         sourceBlock(node.Body),
				Location:     node.Location,
			}
		default:
			panic(fmt.Sprintf("MIR test CFG fixture: unhandled HIR statement %T", stmt))
		}
	}
	source := &ast.Module{Stmts: make([]ast.Stmt, 0, len(module.Funcs))}
	for _, fn := range module.Funcs {
		if fn == nil {
			continue
		}
		fn.NodeID = assignID(fn.NodeID)
		var returnType ast.TypeExpr
		if typ, ok := module.Types.Type(fn.ReturnType); !ok || typ.Kind != ir.TypeVoid {
			returnType = &ast.NamedType{Name: module.Types.Text(fn.ReturnType)}
		}
		source.Stmts = append(source.Stmts, &ast.FnDecl{
			NodeIDHolder: ast.NodeIDHolder{NodeID: ast.NodeID(fn.NodeID)},
			Name:         &ast.Ident{Name: fn.Name},
			ReturnType:   returnType,
			Body:         sourceBlock(fn.Body),
			Location:     fn.Location,
		})
	}
	return cfg.BuildModule(source)
}

func TestGenerateMIRAddsImplicitVoidReturn(t *testing.T) {
	mod := &hir.Module{
		Name: "test", Types: mirTypes.table,
		Funcs: []*hir.Function{
			{
				Name:       "main",
				ReturnType: mirTypes.void,
				Body: &hir.Block{
					Stmts: []hir.Stmt{
						&hir.ExprStmt{Value: &ir.IntLit{Value: "1", Type: mirTypes.i32}},
					},
				},
			},
		},
	}

	graphs := cfgForHIR(mod)
	out := GenerateMIR(mod, graphs, nil, nil, nil)
	if out == nil || len(out.Funcs) != 1 {
		t.Fatalf("expected one MIR function, got %#v", out)
	}
	fn := out.Funcs[0]
	if fn == nil || len(fn.Blocks) != 1 {
		t.Fatalf("expected one MIR block, got %#v", fn)
	}
	if _, ok := fn.Blocks[0].Term.(*Ret); !ok {
		t.Fatalf("expected implicit ret terminator, got %#v", fn.Blocks[0].Term)
	}
}

func TestGenerateMIRDoesNotAssignUninitializedBinding(t *testing.T) {
	mod := &hir.Module{
		Name: "test", Types: mirTypes.table,
		Funcs: []*hir.Function{{
			Name:       "main",
			ReturnType: mirTypes.i32,
			Body: &hir.Block{Stmts: []hir.Stmt{
				&hir.Binding{Name: "value", Type: mirTypes.i32},
				&hir.Assign{
					Target: &ir.Place{Root: &ir.Ident{Name: "value", Type: mirTypes.i32}, Type: mirTypes.i32},
					Value:  &ir.IntLit{Value: "7", Type: mirTypes.i32},
				},
				&hir.Return{Value: &ir.Ident{Name: "value", Type: mirTypes.i32}},
			}},
		}},
	}

	out := GenerateMIR(mod, cfgForHIR(mod), nil, nil, nil)
	if out == nil || len(out.Funcs) != 1 || len(out.Funcs[0].Blocks) != 1 {
		t.Fatalf("unexpected MIR shape: %#v", out)
	}
	instrs := out.Funcs[0].Blocks[0].Instrs
	if len(instrs) != 1 {
		t.Fatalf("instructions = %#v, want assignment only", instrs)
	}
	assign, ok := instrs[0].(*Assign)
	if !ok || assign.Name != "value" {
		t.Fatalf("instruction = %#v, want value assignment", instrs[0])
	}
}

func TestGenerateMIRLowersReturnCleanupBeforeTerminator(t *testing.T) {
	mod := &hir.Module{
		Name: "test", Types: mirTypes.table,
		Funcs: []*hir.Function{{
			Name:       "release",
			ReturnType: mirTypes.i32,
			Body: &hir.Block{Stmts: []hir.Stmt{&hir.Return{
				Value:   &ir.IntLit{Value: "7", Type: mirTypes.i32},
				Cleanup: []ir.Expr{&ir.Drop{Value: &ir.Ident{Name: "owner", Type: mirTypes.ownedI32}}},
			}}},
		}},
	}
	out := GenerateMIR(mod, cfgForHIR(mod), nil, nil, nil)
	block := out.Funcs[0].Blocks[0]
	if len(block.Instrs) != 1 {
		t.Fatalf("expected one cleanup instruction, got %#v", block.Instrs)
	}
	if _, ok := block.Instrs[0].(*Drop); !ok {
		t.Fatalf("expected MIR drop, got %#v", block.Instrs[0])
	}
	ret, ok := block.Term.(*Ret)
	if !ok || ret.Value.Text() != "7" {
		t.Fatalf("expected preserved return value, got %#v", block.Term)
	}
}

func TestGenerateMIRAppliesOwnershipCleanupPlan(t *testing.T) {
	tests := []struct {
		name string
		body *hir.Block
		plan *ownershipresult.CleanupPlan
		want int
	}{
		{
			name: "scope exit",
			body: &hir.Block{NodeID: 10},
			plan: &ownershipresult.CleanupPlan{AfterScope: map[ir.NodeID][]symbols.SymbolID{10: {1}}},
			want: 1,
		},
		{
			name: "return",
			body: &hir.Block{Stmts: []hir.Stmt{&hir.Return{NodeID: 20}}},
			plan: &ownershipresult.CleanupPlan{BeforeReturn: map[ir.NodeID][]symbols.SymbolID{20: {1}}},
			want: 1,
		},
		{
			name: "assignment",
			body: &hir.Block{Stmts: []hir.Stmt{&hir.Assign{
				NodeID: 30,
				Target: &ir.Place{Root: &ir.Ident{Name: "owner", Type: mirTypes.ownedI32}, Type: mirTypes.ownedI32},
				Value:  &ir.ZeroValue{Type: mirTypes.ownedI32},
			}}},
			plan: &ownershipresult.CleanupPlan{BeforeAssign: map[ir.NodeID]struct{}{30: {}}},
			want: 3,
		},
		{
			name: "discarded value",
			body: &hir.Block{Stmts: []hir.Stmt{&hir.ExprStmt{
				ValueNodeID: 40,
				Value:       &ir.Call{Callee: &ir.Ident{Name: "acquire", Type: mirTypes.fnOwner}, Type: mirTypes.ownedI32},
			}}},
			plan: &ownershipresult.CleanupPlan{DiscardedValue: map[ir.NodeID]struct{}{40: {}}},
			want: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mod := &hir.Module{Name: "test", Types: mirTypes.table, Funcs: []*hir.Function{{
				Name:       "main",
				Params:     []ir.Param{{Name: "owner", Type: mirTypes.ownedI32, SymbolID: 1}},
				ReturnType: mirTypes.void,
				Body:       tt.body,
			}}}
			graphs := cfgForHIR(mod)
			plans := ownershipresult.Result{mod.Funcs[0].NodeID: tt.plan}
			out := GenerateMIR(mod, graphs, plans, nil, nil)
			if out == nil || len(out.Funcs) != 1 || len(out.Funcs[0].Blocks) == 0 {
				t.Fatalf("MIR = %#v", out)
			}
			instrs := out.Funcs[0].Blocks[0].Instrs
			if len(instrs) != tt.want {
				t.Fatalf("instructions = %#v, want %d", instrs, tt.want)
			}
			hasDrop := false
			for _, instr := range instrs {
				if _, ok := instr.(*Drop); ok {
					hasDrop = true
					break
				}
			}
			if !hasDrop {
				t.Fatalf("instructions = %#v, want cleanup drop", instrs)
			}
		})
	}
}

func TestGenerateMIRStaticDataUsesSemanticConstValues(t *testing.T) {
	mod := &hir.Module{Name: "test", Types: mirTypes.table}
	scope := symbols.NewScope(nil)
	sym := symbols.New("Name", symbols.SymbolConst, nil, nil)
	sym.BindType(&typeinfo.CStrType{})
	if err := scope.Declare(sym); err != nil {
		t.Fatalf("declare const: %v", err)
	}

	value, ok := constvalue.NewString("puts", "cstr")
	if !ok {
		t.Fatal("NewString failed")
	}

	out := GenerateMIR(mod, cfgForHIR(mod), nil, scope, map[symbols.SymbolID]constvalue.Value{
		sym.ID: value,
	})
	if out == nil || len(out.StaticData) != 1 {
		t.Fatalf("expected one static entry, got %#v", out)
	}
	entry := out.StaticData[0]
	if entry.Name != fmt.Sprintf("@Name$%d", sym.ID) || entry.Type != mirTypes.cstr || entry.Value != "puts" || entry.Align != 8 {
		t.Fatalf("unexpected static entry: %#v", entry)
	}
}

func TestGenerateMIRStaticDataFormatsFloatConstValues(t *testing.T) {
	mod := &hir.Module{Name: "test", Types: mirTypes.table}
	scope := symbols.NewScope(nil)
	sym := symbols.New("X", symbols.SymbolConst, nil, nil)
	sym.BindType(&typeinfo.FloatType{Bits: 64})
	if err := scope.Declare(sym); err != nil {
		t.Fatalf("declare const: %v", err)
	}

	value, ok := constvalue.NewFloatText("3", "f64")
	if !ok {
		t.Fatal("NewFloatText failed")
	}

	out := GenerateMIR(mod, cfgForHIR(mod), nil, scope, map[symbols.SymbolID]constvalue.Value{
		sym.ID: value,
	})
	if out == nil || len(out.StaticData) != 1 {
		t.Fatalf("expected one static entry, got %#v", out)
	}
	entry := out.StaticData[0]
	if entry.Type != mirTypes.f64 || entry.Value != "3.0" {
		t.Fatalf("unexpected float static entry: %#v", entry)
	}
}

func TestGenerateMIRLowersDiscardedValueCallAsPlainCall(t *testing.T) {
	mod := &hir.Module{
		Name: "test", Types: mirTypes.table,
		Funcs: []*hir.Function{
			{
				Name:       "main",
				ReturnType: mirTypes.i32,
				Body: &hir.Block{
					Stmts: []hir.Stmt{
						&hir.ExprStmt{
							Value: &ir.Call{
								Callee: &ir.Ident{Name: "Ping", Type: mirTypes.fnI32},
								Type:   mirTypes.i32,
							},
						},
						&hir.Return{Value: &ir.IntLit{Value: "0", Type: mirTypes.i32}},
					},
				},
			},
		},
	}

	graphs := cfgForHIR(mod)
	out := GenerateMIR(mod, graphs, nil, nil, nil)
	if out == nil || len(out.Funcs) != 1 {
		t.Fatalf("expected one MIR function, got %#v", out)
	}
	fn := out.Funcs[0]
	if fn == nil || len(fn.Blocks) != 1 {
		t.Fatalf("expected one MIR block, got %#v", fn)
	}
	if len(fn.Blocks[0].Instrs) != 1 {
		t.Fatalf("expected one MIR instruction, got %#v", fn.Blocks[0].Instrs)
	}
	call, ok := fn.Blocks[0].Instrs[0].(*Call)
	if !ok {
		t.Fatalf("expected plain call instruction, got %#v", fn.Blocks[0].Instrs[0])
	}
	if call.Type != mirTypes.i32 {
		t.Fatalf("expected preserved call return type, got %d", call.Type)
	}
}

func TestGenerateMIRLowersZeroValue(t *testing.T) {
	mod := &hir.Module{
		Name: "test", Types: mirTypes.table,
		Funcs: []*hir.Function{
			{
				Name:       "maybe",
				ReturnType: mirTypes.optionalI32,
				Body: &hir.Block{
					Stmts: []hir.Stmt{
						&hir.Return{Value: &ir.ZeroValue{Type: mirTypes.optionalI32}},
					},
				},
			},
		},
	}

	out := GenerateMIR(mod, cfgForHIR(mod), nil, nil, nil)
	if out == nil || len(out.Funcs) != 1 || len(out.Funcs[0].Blocks) != 1 {
		t.Fatalf("unexpected MIR shape: %#v", out)
	}
	block := out.Funcs[0].Blocks[0]
	if len(block.Instrs) != 1 {
		t.Fatalf("expected zero value assign, got %#v", block.Instrs)
	}
	assign, ok := block.Instrs[0].(*Assign)
	if !ok {
		t.Fatalf("expected assign, got %#v", block.Instrs[0])
	}
	zero, ok := assign.Value.(*ZeroValue)
	if !ok || zero.Type != mirTypes.optionalI32 {
		t.Fatalf("expected ?i32 zero value, got %#v", assign.Value)
	}
}

func TestGenerateMIRLowersOptionalSome(t *testing.T) {
	mod := &hir.Module{
		Name: "test", Types: mirTypes.table,
		Funcs: []*hir.Function{
			{
				Name:       "maybe",
				ReturnType: mirTypes.optionalI32,
				Body: &hir.Block{
					Stmts: []hir.Stmt{
						&hir.Return{Value: &ir.OptionalSome{Value: &ir.IntLit{Value: "7", Type: mirTypes.i32}, Type: mirTypes.optionalI32}},
					},
				},
			},
		},
	}

	out := GenerateMIR(mod, cfgForHIR(mod), nil, nil, nil)
	if out == nil || len(out.Funcs) != 1 || len(out.Funcs[0].Blocks) != 1 {
		t.Fatalf("unexpected MIR shape: %#v", out)
	}
	block := out.Funcs[0].Blocks[0]
	if len(block.Instrs) != 1 {
		t.Fatalf("expected optional some assign, got %#v", block.Instrs)
	}
	assign, ok := block.Instrs[0].(*Assign)
	if !ok {
		t.Fatalf("expected assign, got %#v", block.Instrs[0])
	}
	some, ok := assign.Value.(*OptionalSome)
	if !ok || some.Type != mirTypes.optionalI32 {
		t.Fatalf("expected ?i32 optional some, got %#v", assign.Value)
	}
}

func TestGenerateMIRLowersProjectedRawAddressDirectly(t *testing.T) {
	mod := &hir.Module{
		Name: "test", Types: mirTypes.table,
		Funcs: []*hir.Function{
			{
				Name:       "main",
				ReturnType: mirTypes.i32,
				Body: &hir.Block{
					Stmts: []hir.Stmt{
						&hir.Binding{
							Name: "ptr",
							Value: &ir.AddrOf{
								Place: &ir.Place{
									Root: &ir.Ident{Name: "boxptr", Type: mirTypes.ownedValueStruct},
									Projections: []ir.PlaceProjection{
										{Kind: ir.PlaceProjectionDeref, Type: mirTypes.valueStruct},
										{Kind: ir.PlaceProjectionField, FieldIndex: 0, Type: mirTypes.i32},
									},
									Type: mirTypes.i32,
								},
								Type: mirTypes.rawptr,
							},
						},
						&hir.Return{Value: &ir.IntLit{Value: "0", Type: mirTypes.i32}},
					},
				},
			},
		},
	}

	out := GenerateMIR(mod, cfgForHIR(mod), nil, nil, nil)
	if len(out.Funcs) != 1 || len(out.Funcs[0].Blocks) != 1 {
		t.Fatalf("unexpected MIR shape: %#v", out)
	}
	instrs := out.Funcs[0].Blocks[0].Instrs
	if len(instrs) != 2 {
		t.Fatalf("projected raw address instructions = %d, want address plus binding: %#v", len(instrs), instrs)
	}
	assign, ok := instrs[0].(*Assign)
	if !ok {
		t.Fatalf("expected first instruction assignment, got %#v", instrs)
	}
	address, ok := assign.Value.(*AddrOf)
	if !ok || address.Type != mirTypes.rawptr || address.Place == nil || len(address.Place.Projections) != 2 {
		t.Fatalf("expected direct raw address of field place, got %#v", assign.Value)
	}
	if binding, ok := instrs[1].(*Assign); !ok {
		t.Fatalf("expected raw address binding, got %#v", instrs[1])
	} else if _, ok := binding.Value.(*Cast); ok {
		t.Fatalf("raw address must not use MIR cast, got %#v", binding.Value)
	}
}

func TestGenerateMIRLowersIndexedRawAddressDirectly(t *testing.T) {
	mod := &hir.Module{
		Name: "test", Types: mirTypes.table,
		Funcs: []*hir.Function{
			{
				Name:       "main",
				ReturnType: mirTypes.i32,
				Body: &hir.Block{
					Stmts: []hir.Stmt{
						&hir.Binding{
							Name: "ptr",
							Value: &ir.AddrOf{
								Place: &ir.Place{
									Root: &ir.Ident{Name: "values", Type: mirTypes.dynamicI32},
									Projections: []ir.PlaceProjection{
										{Kind: ir.PlaceProjectionIndex, Index: &ir.IntLit{Value: "0", Type: mirTypes.i32}, Type: mirTypes.i32},
									},
									Type: mirTypes.i32,
								},
								Type: mirTypes.rawptr,
							},
						},
						&hir.Return{Value: &ir.IntLit{Value: "0", Type: mirTypes.i32}},
					},
				},
			},
		},
	}

	out := GenerateMIR(mod, cfgForHIR(mod), nil, nil, nil)
	if len(out.Funcs) != 1 || len(out.Funcs[0].Blocks) != 1 {
		t.Fatalf("unexpected MIR shape: %#v", out)
	}
	instrs := out.Funcs[0].Blocks[0].Instrs
	if len(instrs) != 2 {
		t.Fatalf("indexed raw address instructions = %d, want address plus binding: %#v", len(instrs), instrs)
	}
	assign, ok := instrs[0].(*Assign)
	if !ok {
		t.Fatalf("expected first instruction assignment, got %#v", instrs)
	}
	address, ok := assign.Value.(*AddrOf)
	if !ok || address.Type != mirTypes.rawptr || address.Place == nil || len(address.Place.Projections) != 1 {
		t.Fatalf("expected direct raw address of indexed place, got %#v", assign.Value)
	}
	if binding, ok := instrs[1].(*Assign); !ok {
		t.Fatalf("expected raw address binding, got %#v", instrs[1])
	} else if _, ok := binding.Value.(*Cast); ok {
		t.Fatalf("raw address must not use MIR cast, got %#v", binding.Value)
	}
}

func TestGenerateMIRLowersSliceView(t *testing.T) {
	mod := &hir.Module{
		Name: "test", Types: mirTypes.table,
		Funcs: []*hir.Function{{
			Name:       "borrow",
			Params:     []ir.Param{{Name: "xs", Type: mirTypes.dynamicI32}},
			ReturnType: mirTypes.i32,
			Body: &hir.Block{Stmts: []hir.Stmt{
				&hir.Binding{Name: "view", Value: &ir.SliceView{
					Source: &ir.Place{Root: &ir.Ident{Name: "xs", Type: mirTypes.dynamicI32}, Type: mirTypes.dynamicI32},
					Type:   mirTypes.refDynamicI32,
				}},
				&hir.Return{Value: &ir.IntLit{Value: "0", Type: mirTypes.i32}},
			}},
		}},
	}

	out := GenerateMIR(mod, cfgForHIR(mod), nil, nil, nil)
	instrs := out.Funcs[0].Blocks[0].Instrs
	if len(instrs) != 2 {
		t.Fatalf("expected slice-view and binding assignments, got %#v", instrs)
	}
	assign, ok := instrs[0].(*Assign)
	if !ok {
		t.Fatalf("expected assignment, got %#v", instrs[0])
	}
	view, ok := assign.Value.(*SliceView)
	if !ok || view.Type != mirTypes.refDynamicI32 || view.Source.Text() != "xs" {
		t.Fatalf("expected MIR SliceView, got %#v", assign.Value)
	}
}

func TestGenerateMIRLowersStringChars(t *testing.T) {
	charType := mirTypes.table.Intern(ir.Type{Kind: ir.TypeChar})
	dynamicChar := mirTypes.table.Intern(ir.Type{Kind: ir.TypeArray, Elem: charType})
	stringType := mirTypes.table.Intern(ir.Type{Kind: ir.TypeString})
	refString := mirTypes.table.Intern(ir.Type{Kind: ir.TypeReference, Elem: stringType})
	mod := &hir.Module{
		Name: "test", Types: mirTypes.table,
		Funcs: []*hir.Function{{
			Name:       "chars",
			Params:     []ir.Param{{Name: "text", Type: refString}},
			ReturnType: mirTypes.i32,
			Body: &hir.Block{Stmts: []hir.Stmt{
				&hir.Binding{Name: "chars", Value: &ir.StringChars{
					Value: &ir.Ident{Name: "text", Type: refString}, Type: dynamicChar,
				}},
				&hir.Return{Value: &ir.IntLit{Value: "0", Type: mirTypes.i32}},
			}},
		}},
	}
	out := GenerateMIR(mod, cfgForHIR(mod), nil, nil, nil)
	assign, ok := out.Funcs[0].Blocks[0].Instrs[0].(*Assign)
	if !ok {
		t.Fatalf("expected StringChars assignment, got %#v", out.Funcs[0].Blocks[0].Instrs)
	}
	chars, ok := assign.Value.(*StringChars)
	if !ok || chars.Type != dynamicChar || chars.Value.Text() != "text" {
		t.Fatalf("MIR StringChars = %#v, want text -> []char", assign.Value)
	}
}

func TestGenerateMIRPreservesSliceViewRange(t *testing.T) {
	mod := &hir.Module{
		Name: "test", Types: mirTypes.table,
		Funcs: []*hir.Function{{
			Name:       "slice",
			ReturnType: mirTypes.i32,
			Body: &hir.Block{Stmts: []hir.Stmt{
				&hir.Binding{Name: "view", Value: &ir.SliceView{
					Source:       &ir.Place{Root: &ir.Ident{Name: "xs", Type: mirTypes.refDynamicI32}, Type: mirTypes.refDynamicI32},
					Start:        &ir.IntLit{Value: "1", Type: mirTypes.i32},
					End:          &ir.IntLit{Value: "3", Type: mirTypes.i32},
					EndExclusive: true,
					Type:         mirTypes.refDynamicI32,
				}},
				&hir.Return{Value: &ir.IntLit{Value: "0", Type: mirTypes.i32}},
			}},
		}},
	}

	out := GenerateMIR(mod, cfgForHIR(mod), nil, nil, nil)
	assign := out.Funcs[0].Blocks[0].Instrs[0].(*Assign)
	view, ok := assign.Value.(*SliceView)
	if !ok || !view.EndExclusive || view.Start.Text() != "1" || view.End.Text() != "3" {
		t.Fatalf("expected preserved MIR range, got %#v", assign.Value)
	}
}

func TestGenerateMIRLowersIndexReadAsPlaceLoad(t *testing.T) {
	place := &ir.Place{
		Root: &ir.Ident{Name: "xs", Type: mirTypes.fixed4I32},
		Projections: []ir.PlaceProjection{
			{Kind: ir.PlaceProjectionIndex, Index: &ir.IntLit{Value: "0", Type: mirTypes.i32}, Type: mirTypes.i32},
		},
		Type: mirTypes.i32,
	}
	mod := &hir.Module{
		Name: "test", Types: mirTypes.table,
		Funcs: []*hir.Function{
			{
				Name:       "first",
				Params:     []ir.Param{{Name: "xs", Type: mirTypes.fixed4I32}},
				ReturnType: mirTypes.i32,
				Body: &hir.Block{
					Stmts: []hir.Stmt{
						&hir.Return{Value: &ir.Load{Place: place}},
					},
				},
			},
		},
	}

	out := GenerateMIR(mod, cfgForHIR(mod), nil, nil, nil)
	if out == nil || len(out.Funcs) != 1 || len(out.Funcs[0].Blocks) != 1 {
		t.Fatalf("unexpected MIR shape: %#v", out)
	}
	instrs := out.Funcs[0].Blocks[0].Instrs
	if len(instrs) != 1 {
		t.Fatalf("expected one place load instruction, got %#v", instrs)
	}
	assign, ok := instrs[0].(*Assign)
	if !ok {
		t.Fatalf("expected first instruction assignment, got %#v", instrs[0])
	}
	load, ok := assign.Value.(*Load)
	if !ok || load.Place == nil || len(load.Place.Projections) != 1 || load.Place.Projections[0].Kind != PlaceProjectionIndex {
		t.Fatalf("expected indexed place Load, got %#v", assign.Value)
	}
}

func TestGenerateMIRDropsOwnerBearingTemporaryAfterFieldProjection(t *testing.T) {
	mod := &hir.Module{Name: "test", Types: mirTypes.table, Funcs: []*hir.Function{{
		Name: "read", ReturnType: mirTypes.i32, Body: &hir.Block{Stmts: []hir.Stmt{&hir.Return{Value: &ir.Field{
			Base:  &ir.Call{Callee: &ir.Ident{Name: "make", Type: mirTypes.fnOwner}, Type: mirTypes.ownerStruct},
			Index: 0, DropBase: true, Type: mirTypes.i32,
		}}}},
	}}}
	out := GenerateMIR(mod, cfgForHIR(mod), nil, nil, nil)
	instrs := out.Funcs[0].Blocks[0].Instrs
	if len(instrs) != 3 {
		t.Fatalf("expected call, projection, and drop, got %#v", instrs)
	}
	if _, ok := instrs[0].(*Assign); !ok {
		t.Fatalf("expected call assignment, got %#v", instrs[0])
	}
	if _, ok := instrs[1].(*Assign); !ok {
		t.Fatalf("expected field projection assignment, got %#v", instrs[1])
	}
	if _, ok := instrs[2].(*Drop); !ok {
		t.Fatalf("expected temporary drop after projection, got %#v", instrs[2])
	}
}

func TestGenerateMIRLowersSliceViewIndexReadAsPlaceLoad(t *testing.T) {
	place := &ir.Place{
		Root: &ir.Ident{Name: "xs", Type: mirTypes.refDynamicI32},
		Projections: []ir.PlaceProjection{
			{Kind: ir.PlaceProjectionIndex, Index: &ir.IntLit{Value: "0", Type: mirTypes.i32}, Type: mirTypes.i32},
		},
		Type: mirTypes.i32,
	}
	mod := &hir.Module{Name: "test", Types: mirTypes.table, Funcs: []*hir.Function{{
		Name: "first", Params: []ir.Param{{Name: "xs", Type: mirTypes.refDynamicI32}}, ReturnType: mirTypes.i32,
		Body: &hir.Block{Stmts: []hir.Stmt{&hir.Return{Value: &ir.Load{Place: place}}}},
	}}}
	out := GenerateMIR(mod, cfgForHIR(mod), nil, nil, nil)
	instrs := out.Funcs[0].Blocks[0].Instrs
	if len(instrs) != 1 {
		t.Fatalf("expected one place load instruction, got %#v", instrs)
	}
	assign, ok := instrs[0].(*Assign)
	if !ok {
		t.Fatalf("expected assignment, got %#v", instrs)
	}
	load, ok := assign.Value.(*Load)
	if !ok || load.Place == nil || len(load.Place.Projections) != 1 {
		t.Fatalf("expected indexed place Load, got %#v", assign.Value)
	}
}

func TestGenerateMIRLowersIndexAssignmentAsPlaceStore(t *testing.T) {
	mod := &hir.Module{
		Name: "test", Types: mirTypes.table,
		Funcs: []*hir.Function{
			{
				Name: "set_first",
				Params: []ir.Param{
					{Name: "xs", Type: mirTypes.fixed4I32},
					{Name: "value", Type: mirTypes.i32},
				},
				Body: &hir.Block{
					Stmts: []hir.Stmt{
						&hir.Assign{
							Target: &ir.Place{
								Root: &ir.Ident{Name: "xs", Type: mirTypes.fixed4I32},
								Projections: []ir.PlaceProjection{
									{Kind: ir.PlaceProjectionIndex, Index: &ir.IntLit{Value: "0", Type: mirTypes.i32}, Type: mirTypes.i32},
								},
								Type: mirTypes.i32,
							},
							Value: &ir.Ident{Name: "value", Type: mirTypes.i32},
						},
					},
				},
			},
		},
	}

	out := GenerateMIR(mod, cfgForHIR(mod), nil, nil, nil)
	if out == nil || len(out.Funcs) != 1 || len(out.Funcs[0].Blocks) != 1 {
		t.Fatalf("unexpected MIR shape: %#v", out)
	}
	instrs := out.Funcs[0].Blocks[0].Instrs
	if len(instrs) != 1 {
		t.Fatalf("expected one place store instruction, got %#v", instrs)
	}
	store, ok := instrs[0].(*Store)
	if !ok || store.Place == nil || len(store.Place.Projections) != 1 {
		t.Fatalf("expected indexed place Store, got %#v", instrs[0])
	}
}

func TestGenerateMIRLowersMutableSliceViewIndexAssignmentAsPlaceStore(t *testing.T) {
	mod := &hir.Module{Name: "test", Types: mirTypes.table, Funcs: []*hir.Function{{
		Name: "set_first", Params: []ir.Param{{Name: "xs", Type: mirTypes.mutRefDynamicI32}, {Name: "value", Type: mirTypes.i32}},
		Body: &hir.Block{Stmts: []hir.Stmt{&hir.Assign{
			Target: &ir.Place{
				Root: &ir.Ident{Name: "xs", Type: mirTypes.mutRefDynamicI32},
				Projections: []ir.PlaceProjection{
					{Kind: ir.PlaceProjectionIndex, Index: &ir.IntLit{Value: "0", Type: mirTypes.i32}, Type: mirTypes.i32},
				},
				Type: mirTypes.i32,
			},
			Value: &ir.Ident{Name: "value", Type: mirTypes.i32},
		}}},
	}}}
	out := GenerateMIR(mod, cfgForHIR(mod), nil, nil, nil)
	instrs := out.Funcs[0].Blocks[0].Instrs
	if len(instrs) != 1 {
		t.Fatalf("expected one place store instruction, got %#v", instrs)
	}
	store, ok := instrs[0].(*Store)
	if !ok || store.Place == nil || len(store.Place.Projections) != 1 {
		t.Fatalf("expected indexed place Store, got %#v", instrs[0])
	}
}

func TestGenerateMIRLowersArrayLiteral(t *testing.T) {
	mod := &hir.Module{
		Name: "test", Types: mirTypes.table,
		Funcs: []*hir.Function{
			{
				Name:       "first",
				ReturnType: mirTypes.fixed3I32,
				Body: &hir.Block{
					Stmts: []hir.Stmt{
						&hir.Return{Value: &ir.ArrayLit{
							Values: []ir.Expr{
								&ir.IntLit{Value: "1", Type: mirTypes.i32},
								&ir.IntLit{Value: "2", Type: mirTypes.i32},
								&ir.IntLit{Value: "3", Type: mirTypes.i32},
							},
							Type: mirTypes.fixed3I32,
						}},
					},
				},
			},
		},
	}

	out := GenerateMIR(mod, cfgForHIR(mod), nil, nil, nil)
	instrs := out.Funcs[0].Blocks[0].Instrs
	if len(instrs) != 1 {
		t.Fatalf("expected array literal assign, got %#v", instrs)
	}
	assign, ok := instrs[0].(*Assign)
	if !ok {
		t.Fatalf("expected assign, got %#v", instrs[0])
	}
	lit, ok := assign.Value.(*ArrayLit)
	if !ok || lit.Type != mirTypes.fixed3I32 || len(lit.Values) != 3 {
		t.Fatalf("expected MIR array literal, got %#v", assign.Value)
	}
}

func TestGenerateMIRAllocatesDynamicArrayBeforeInitializers(t *testing.T) {
	mod := &hir.Module{
		Name: "test", Types: mirTypes.table,
		Funcs: []*hir.Function{
			{
				Name:       "values",
				ReturnType: mirTypes.dynamicI32,
				Body: &hir.Block{Stmts: []hir.Stmt{
					&hir.Return{Value: &ir.ArrayLit{
						Values: []ir.Expr{
							&ir.IntLit{Value: "1", Type: mirTypes.i32},
							&ir.IntLit{Value: "2", Type: mirTypes.i32},
						},
						Dynamic: true,
						Type:    mirTypes.dynamicI32,
					}},
				}},
			},
		},
	}

	out := GenerateMIR(mod, cfgForHIR(mod), nil, nil, nil)
	block := out.Funcs[0].Blocks[0]
	if len(block.Instrs) != 3 {
		t.Fatalf("expected allocation and two indexed stores, got %#v", block.Instrs)
	}
	assign, ok := block.Instrs[0].(*Assign)
	if !ok {
		t.Fatalf("expected leading allocation assignment, got %#v", block.Instrs[0])
	}
	alloc, allocOK := assign.Value.(*DynamicArrayAlloc)
	if !allocOK || alloc.Length != 2 || alloc.Type != mirTypes.dynamicI32 {
		t.Fatalf("expected leading dynamic allocation, got %#v", block.Instrs[0])
	}
	for _, index := range []int{1, 2} {
		store, ok := block.Instrs[index].(*Store)
		if !ok || store.Place == nil || len(store.Place.Projections) != 1 {
			t.Fatalf("expected indexed place Store at %d, got %#v", index, block.Instrs[index])
		}
	}
}

func TestGenerateMIRLowersDynamicArrayOwnerOperations(t *testing.T) {
	for _, op := range []symbols.CompilerOp{symbols.CompilerOpAppend, symbols.CompilerOpReserve, symbols.CompilerOpResize, symbols.CompilerOpShrink} {
		t.Run(string(op), func(t *testing.T) {
			expr := &ir.DynamicArrayOp{
				Op:        op,
				Array:     &ir.Ident{Name: "values", Type: mirTypes.mutRefDynamicI32},
				Length:    &ir.IntLit{Value: "8", Type: mirTypes.usize},
				Value:     &ir.IntLit{Value: "1", Type: mirTypes.i32},
				ArrayType: mirTypes.dynamicI32,
				Type:      mirTypes.void,
			}
			if op == symbols.CompilerOpAppend {
				expr.Length = nil
			}
			if op == symbols.CompilerOpReserve || op == symbols.CompilerOpShrink {
				expr.Value = nil
			}
			mod := &hir.Module{
				Name: "test", Types: mirTypes.table,
				Funcs: []*hir.Function{{
					Name:       "grow",
					Params:     []ir.Param{{Name: "values", Type: mirTypes.mutRefDynamicI32}},
					ReturnType: mirTypes.void,
					Body:       &hir.Block{Stmts: []hir.Stmt{&hir.ExprStmt{Value: expr}}},
				}},
			}
			out := GenerateMIR(mod, cfgForHIR(mod), nil, nil, nil)
			got, ok := out.Funcs[0].Blocks[0].Instrs[0].(*DynamicArrayOp)
			if !ok || got.Op != op || got.ArrayType != mirTypes.dynamicI32 {
				t.Fatalf("operation = %#v, want %s []i32 mutation", out.Funcs[0].Blocks[0].Instrs, op)
			}
		})
	}
}

func TestGenerateMIRPreservesNestedExpressionLocations(t *testing.T) {
	testPath := "test" + peeper.SourceExt
	mod := &hir.Module{
		Name: "test", Types: mirTypes.table,
		Funcs: []*hir.Function{
			{
				Name:       "main",
				ReturnType: mirTypes.i32,
				Body: &hir.Block{
					Stmts: []hir.Stmt{
						&hir.Return{
							Value: &ir.Binary{
								Op: "*",
								Left: &ir.Binary{
									Op:       "+",
									Left:     &ir.IntLit{Value: "1", Type: mirTypes.i32, Location: source.NewLocation(testPath, source.Position{Line: 2, Column: 2}, source.Position{Line: 2, Column: 3})},
									Right:    &ir.IntLit{Value: "2", Type: mirTypes.i32, Location: source.NewLocation(testPath, source.Position{Line: 2, Column: 6}, source.Position{Line: 2, Column: 7})},
									Type:     mirTypes.i32,
									Location: source.NewLocation(testPath, source.Position{Line: 2, Column: 2}, source.Position{Line: 2, Column: 7}),
								},
								Right:    &ir.IntLit{Value: "3", Type: mirTypes.i32, Location: source.NewLocation(testPath, source.Position{Line: 3, Column: 2}, source.Position{Line: 3, Column: 3})},
								Type:     mirTypes.i32,
								Location: source.NewLocation(testPath, source.Position{Line: 3, Column: 2}, source.Position{Line: 3, Column: 7}),
							},
							Location: source.NewLocation(testPath, source.Position{Line: 4, Column: 2}, source.Position{Line: 4, Column: 8}),
						},
					},
				},
			},
		},
	}

	out := GenerateMIR(mod, cfgForHIR(mod), nil, nil, nil)
	if out == nil || len(out.Funcs) != 1 || len(out.Funcs[0].Blocks) != 1 {
		t.Fatalf("unexpected MIR shape: %#v", out)
	}
	instrs := out.Funcs[0].Blocks[0].Instrs
	if len(instrs) != 2 {
		t.Fatalf("expected two lowered binary instructions, got %#v", instrs)
	}
	first, ok := instrs[0].(*Assign)
	if !ok || first.Location == nil || first.Location.Start == nil || first.Location.Start.Line != 2 {
		t.Fatalf("expected child expression location on first assign, got %#v", instrs[0])
	}
	second, ok := instrs[1].(*Assign)
	if !ok || second.Location == nil || second.Location.Start == nil || second.Location.Start.Line != 3 {
		t.Fatalf("expected parent expression location on second assign, got %#v", instrs[1])
	}
}

func TestGenerateMIRLowersForLoop(t *testing.T) {
	mod := &hir.Module{
		Name: "test", Types: mirTypes.table,
		Funcs: []*hir.Function{
			{
				Name:       "main",
				ReturnType: mirTypes.void,
				Body: &hir.Block{
					Stmts: []hir.Stmt{
						&hir.For{
							Cond: &ir.IntLit{Value: "1", Type: mirTypes.boolType},
							Body: &hir.Block{
								Stmts: []hir.Stmt{
									&hir.ExprStmt{
										Value: &ir.Call{
											Callee: &ir.Ident{Name: "Ping", Type: mirTypes.fnI32},
											Type:   mirTypes.i32,
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	graphs := cfgForHIR(mod)
	out := GenerateMIR(mod, graphs, nil, nil, nil)
	if out == nil || len(out.Funcs) != 1 {
		t.Fatalf("expected one MIR function, got %#v", out)
	}
	fn := out.Funcs[0]
	if len(fn.Blocks) != 4 {
		t.Fatalf("expected four blocks for loop, got %#v", fn.Blocks)
	}
	index := 0
	for _, block := range graphs.Functions[0].Blocks {
		if block == nil || block == graphs.Functions[0].Exit || !block.Reachable {
			continue
		}
		if fn.Blocks[index].ID != block.ID {
			t.Fatalf("MIR block %d ID = %d, want CFG ID %d", index, fn.Blocks[index].ID, block.ID)
		}
		index++
	}
	entry := fn.Blocks[0]
	entryJump, ok := entry.Term.(*Jump)
	if !ok {
		t.Fatalf("expected entry jump terminator, got %#v", entry.Term)
	}
	header := fn.Blocks[1]
	if entryJump.TargetID != header.ID {
		t.Fatalf("expected jump to loop header, got %#v", entry.Term)
	}
	term, ok := header.Term.(*Branch)
	if !ok {
		t.Fatalf("expected header branch terminator, got %#v", header.Term)
	}
	if term.ThenID != fn.Blocks[2].ID || term.ElseID != fn.Blocks[3].ID {
		t.Fatalf("unexpected loop targets: %#v", term)
	}
	bodyTerm, ok := fn.Blocks[2].Term.(*Jump)
	if !ok || bodyTerm.TargetID != header.ID {
		t.Fatalf("expected backedge to header block, got %#v", fn.Blocks[2].Term)
	}
}

func TestGenerateMIRDropsBorrowedTemporariesAfterCallInReverseOrder(t *testing.T) {
	mod := &hir.Module{
		Name: "test", Types: mirTypes.table,
		Funcs: []*hir.Function{{
			Name:       "main",
			ReturnType: mirTypes.void,
			Body: &hir.Block{Stmts: []hir.Stmt{
				&hir.ExprStmt{Value: &ir.Call{
					Callee: &ir.Ident{Name: "Both", Type: mirTypes.fnTwoBox},
					Args: []ir.Expr{
						&ir.TempBorrow{Value: &ir.Call{Callee: &ir.Ident{Name: "MakeFirst", Type: mirTypes.fnBox}, Type: mirTypes.valueStruct}, Type: mirTypes.refBox},
						&ir.TempBorrow{Value: &ir.Call{Callee: &ir.Ident{Name: "MakeSecond", Type: mirTypes.fnBox}, Type: mirTypes.valueStruct}, Type: mirTypes.refBox},
					},
				}},
			},
			},
		}},
	}
	out := GenerateMIR(mod, cfgForHIR(mod), nil, nil, nil)
	instrs := out.Funcs[0].Blocks[0].Instrs
	if len(instrs) != 7 {
		t.Fatalf("temporary borrow instructions = %d, want 7: %#v", len(instrs), instrs)
	}
	if _, ok := instrs[4].(*Call); !ok {
		t.Fatalf("expected outer call before cleanup, got %#v", instrs[4])
	}
	secondDrop, secondOK := instrs[5].(*Drop)
	firstDrop, firstOK := instrs[6].(*Drop)
	secondValue := instrs[2].(*Assign)
	firstValue := instrs[0].(*Assign)
	if !secondOK || !firstOK || secondDrop.Value.Text() != secondValue.Name || firstDrop.Value.Text() != firstValue.Name {
		t.Fatalf("expected reverse temporary cleanup, got %#v, %#v", instrs[5], instrs[6])
	}
}

func TestGenerateMIRDropsTemporaryStringOwnerAfterViewUse(t *testing.T) {
	stringType := mirTypes.table.Intern(ir.Type{Kind: ir.TypeString})
	refString := mirTypes.table.Intern(ir.Type{Kind: ir.TypeReference, Elem: stringType})
	fnString := mirTypes.table.Intern(ir.Type{Kind: ir.TypeFunction, Return: stringType})
	mod := &hir.Module{
		Name: "test", Types: mirTypes.table,
		Funcs: []*hir.Function{{
			Name:       "main",
			ReturnType: mirTypes.void,
			Body: &hir.Block{Stmts: []hir.Stmt{&hir.ExprStmt{Value: &ir.Len{
				Value: &ir.SliceView{
					Source: &ir.Place{
						Root: &ir.TempBorrow{
							Value: &ir.Call{Callee: &ir.Ident{Name: "Make", Type: fnString}, Type: stringType},
							Type:  refString,
						},
						Type: refString,
					},
					Type: refString,
				},
				Type: mirTypes.usize,
			}}}},
		}},
	}
	out := GenerateMIR(mod, cfgForHIR(mod), nil, nil, nil)
	instrs := out.Funcs[0].Blocks[0].Instrs
	if len(instrs) != 5 {
		t.Fatalf("temporary string view instructions = %d, want 5: %#v", len(instrs), instrs)
	}
	if assign, ok := instrs[3].(*Assign); !ok {
		t.Fatalf("expected view use before cleanup, got %#v", instrs[3])
	} else if _, ok := assign.Value.(*Len); !ok {
		t.Fatalf("expected length read before cleanup, got %#v", assign.Value)
	}
	drop, ok := instrs[4].(*Drop)
	if !ok || drop.Value.Text() != instrs[0].(*Assign).Name {
		t.Fatalf("expected one owner drop after view use, got %#v", instrs[4])
	}
}
