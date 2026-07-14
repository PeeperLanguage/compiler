package mir

import (
	"fmt"
	"testing"

	"compiler/internal/constvalue"
	"compiler/internal/ir"
	"compiler/internal/ir/hir"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/table"
	"compiler/internal/semantics/typeinfo"
	"compiler/internal/source"
	"compiler/pkg/peeper"
)

func TestGenerateMIRAddsImplicitVoidReturn(t *testing.T) {
	mod := &hir.Module{
		Name: "test",
		Funcs: []*hir.Function{
			{
				Name:       "main",
				ReturnType: "void",
				Body: &hir.Block{
					Stmts: []hir.Stmt{
						&hir.ExprStmt{Value: &ir.IntLit{Value: "1", Type: "i32"}},
					},
				},
			},
		},
	}

	out := GenerateMIR(mod, nil, nil)
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

func TestGenerateMIRLowersReturnCleanupBeforeTerminator(t *testing.T) {
	mod := &hir.Module{
		Name: "test",
		Funcs: []*hir.Function{{
			Name:       "release",
			ReturnType: "i32",
			Body: &hir.Block{Stmts: []hir.Stmt{&hir.Return{
				Value:   &ir.IntLit{Value: "7", Type: "i32"},
				Cleanup: []ir.Expr{&ir.Drop{Value: &ir.Ident{Name: "owner", Type: "*i32"}}},
			}}},
		}},
	}
	out := GenerateMIR(mod, nil, nil)
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

func TestGenerateMIRStaticDataUsesSemanticConstValues(t *testing.T) {
	mod := &hir.Module{Name: "test"}
	scope := table.New(nil)
	sym := symbols.New("Name", symbols.SymbolConst, nil, nil)
	sym.BindType(&typeinfo.CStrType{})
	if err := scope.Declare(sym); err != nil {
		t.Fatalf("declare const: %v", err)
	}

	out := GenerateMIR(mod, scope, map[symbols.SymbolID]constvalue.Value{
		sym.ID: &constvalue.StringConst{Value: "puts", TypeID: "cstr"},
	})
	if out == nil || len(out.StaticData) != 1 {
		t.Fatalf("expected one static entry, got %#v", out)
	}
	entry := out.StaticData[0]
	if entry.Name != fmt.Sprintf("@Name$%d", sym.ID) || entry.Type != "cstr" || entry.Value != "puts" || entry.Align != 8 {
		t.Fatalf("unexpected static entry: %#v", entry)
	}
}

func TestGenerateMIRStaticDataFormatsFloatConstValues(t *testing.T) {
	mod := &hir.Module{Name: "test"}
	scope := table.New(nil)
	sym := symbols.New("X", symbols.SymbolConst, nil, nil)
	sym.BindType(&typeinfo.FloatType{Bits: 64})
	if err := scope.Declare(sym); err != nil {
		t.Fatalf("declare const: %v", err)
	}

	out := GenerateMIR(mod, scope, map[symbols.SymbolID]constvalue.Value{
		sym.ID: &constvalue.FloatConst{Value: "3", TypeID: "f64"},
	})
	if out == nil || len(out.StaticData) != 1 {
		t.Fatalf("expected one static entry, got %#v", out)
	}
	entry := out.StaticData[0]
	if entry.Type != "f64" || entry.Value != "3.0" {
		t.Fatalf("unexpected float static entry: %#v", entry)
	}
}

func TestGenerateMIRLowersDiscardedValueCallAsPlainCall(t *testing.T) {
	mod := &hir.Module{
		Name: "test",
		Funcs: []*hir.Function{
			{
				Name:       "main",
				ReturnType: "i32",
				Body: &hir.Block{
					Stmts: []hir.Stmt{
						&hir.ExprStmt{
							Value: &ir.Call{
								Callee: &ir.Ident{Name: "Ping", Type: "fn() -> i32"},
								Type:   "i32",
							},
						},
						&hir.Return{Value: &ir.IntLit{Value: "0", Type: "i32"}},
					},
				},
			},
		},
	}

	out := GenerateMIR(mod, nil, nil)
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
	if call.Type != "i32" {
		t.Fatalf("expected preserved call return type, got %q", call.Type)
	}
}

func TestGenerateMIRLowersZeroValue(t *testing.T) {
	mod := &hir.Module{
		Name: "test",
		Funcs: []*hir.Function{
			{
				Name:       "maybe",
				ReturnType: "?i32",
				Body: &hir.Block{
					Stmts: []hir.Stmt{
						&hir.Return{Value: &ir.ZeroValue{Type: "?i32"}},
					},
				},
			},
		},
	}

	out := GenerateMIR(mod, nil, nil)
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
	if !ok || zero.Type != "?i32" {
		t.Fatalf("expected ?i32 zero value, got %#v", assign.Value)
	}
}

func TestGenerateMIRLowersOptionalSome(t *testing.T) {
	mod := &hir.Module{
		Name: "test",
		Funcs: []*hir.Function{
			{
				Name:       "maybe",
				ReturnType: "?i32",
				Body: &hir.Block{
					Stmts: []hir.Stmt{
						&hir.Return{Value: &ir.OptionalSome{Value: &ir.IntLit{Value: "7", Type: "i32"}, Type: "?i32"}},
					},
				},
			},
		},
	}

	out := GenerateMIR(mod, nil, nil)
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
	if !ok || some.Type != "?i32" {
		t.Fatalf("expected ?i32 optional some, got %#v", assign.Value)
	}
}

func TestGenerateMIRLowersProjectedRawAddressWithCast(t *testing.T) {
	mod := &hir.Module{
		Name: "test",
		Funcs: []*hir.Function{
			{
				Name:       "main",
				ReturnType: "i32",
				Body: &hir.Block{
					Stmts: []hir.Stmt{
						&hir.Binding{
							Name: "ptr",
							Value: &ir.AddrOf{
								Expr: &ir.Field{
									Base:       &ir.Ident{Name: "boxptr", Type: "*struct{value: i32}"},
									Index:      0,
									ThroughPtr: true,
									Type:       "i32",
								},
								Type: "rawptr",
							},
						},
						&hir.Return{Value: &ir.IntLit{Value: "0", Type: "i32"}},
					},
				},
			},
		},
	}

	out := GenerateMIR(mod, nil, nil)
	if len(out.Funcs) != 1 || len(out.Funcs[0].Blocks) != 1 {
		t.Fatalf("unexpected MIR shape: %#v", out)
	}
	instrs := out.Funcs[0].Blocks[0].Instrs
	assign, ok := instrs[0].(*Assign)
	if !ok {
		t.Fatalf("expected first instruction assignment, got %#v", instrs)
	}
	projected, ok := assign.Value.(*ProjectField)
	if !ok || projected.Type != "&mut i32" {
		t.Fatalf("expected address-of field to lower as ProjectField, got %#v", assign.Value)
	}
	castAssign, ok := instrs[1].(*Assign)
	if !ok {
		t.Fatalf("expected second instruction assignment, got %#v", instrs)
	}
	cast, ok := castAssign.Value.(*Cast)
	if !ok || cast.Type != "rawptr" {
		t.Fatalf("expected projected field address to cast to rawptr, got %#v", castAssign.Value)
	}
}

func TestGenerateMIRLowersIndexedRawAddressWithCast(t *testing.T) {
	mod := &hir.Module{
		Name: "test",
		Funcs: []*hir.Function{
			{
				Name:       "main",
				ReturnType: "i32",
				Body: &hir.Block{
					Stmts: []hir.Stmt{
						&hir.Binding{
							Name: "ptr",
							Value: &ir.AddrOf{
								Expr: &ir.Index{
									Base:  &ir.Ident{Name: "values", Type: "[]i32"},
									Index: &ir.IntLit{Value: "0", Type: "i32"},
									Type:  "i32",
								},
								Type: "rawptr",
							},
						},
						&hir.Return{Value: &ir.IntLit{Value: "0", Type: "i32"}},
					},
				},
			},
		},
	}

	out := GenerateMIR(mod, nil, nil)
	if len(out.Funcs) != 1 || len(out.Funcs[0].Blocks) != 1 {
		t.Fatalf("unexpected MIR shape: %#v", out)
	}
	instrs := out.Funcs[0].Blocks[0].Instrs
	assign, ok := instrs[0].(*Assign)
	if !ok {
		t.Fatalf("expected first instruction assignment, got %#v", instrs)
	}
	projected, ok := assign.Value.(*ProjectIndex)
	if !ok || projected.Type != "&mut i32" {
		t.Fatalf("expected address-of element to lower as ProjectIndex, got %#v", assign.Value)
	}
	castAssign, ok := instrs[1].(*Assign)
	if !ok {
		t.Fatalf("expected second instruction assignment, got %#v", instrs)
	}
	cast, ok := castAssign.Value.(*Cast)
	if !ok || cast.Type != "rawptr" {
		t.Fatalf("expected projected element address to cast to rawptr, got %#v", castAssign.Value)
	}
}

func TestGenerateMIRLowersSliceView(t *testing.T) {
	mod := &hir.Module{
		Name: "test",
		Funcs: []*hir.Function{{
			Name:       "borrow",
			Params:     []ir.Param{{Name: "xs", Type: "[]i32"}},
			ReturnType: "i32",
			Body: &hir.Block{Stmts: []hir.Stmt{
				&hir.Binding{Name: "view", Value: &ir.SliceView{
					Source: &ir.Ident{Name: "xs", Type: "[]i32"},
					Type:   "&[]i32",
				}},
				&hir.Return{Value: &ir.IntLit{Value: "0", Type: "i32"}},
			}},
		}},
	}

	out := GenerateMIR(mod, nil, nil)
	instrs := out.Funcs[0].Blocks[0].Instrs
	if len(instrs) != 2 {
		t.Fatalf("expected slice-view and binding assignments, got %#v", instrs)
	}
	assign, ok := instrs[0].(*Assign)
	if !ok {
		t.Fatalf("expected assignment, got %#v", instrs[0])
	}
	view, ok := assign.Value.(*SliceView)
	if !ok || view.Type != "&[]i32" || view.Source.Text() != "xs" {
		t.Fatalf("expected MIR SliceView, got %#v", assign.Value)
	}
}

func TestGenerateMIRPreservesSliceViewRange(t *testing.T) {
	mod := &hir.Module{
		Name: "test",
		Funcs: []*hir.Function{{
			Name:       "slice",
			ReturnType: "i32",
			Body: &hir.Block{Stmts: []hir.Stmt{
				&hir.Binding{Name: "view", Value: &ir.SliceView{
					Source:       &ir.Ident{Name: "xs", Type: "&[]i32"},
					Start:        &ir.IntLit{Value: "1", Type: "i32"},
					End:          &ir.IntLit{Value: "3", Type: "i32"},
					EndExclusive: true,
					Type:         "&[]i32",
				}},
				&hir.Return{Value: &ir.IntLit{Value: "0", Type: "i32"}},
			}},
		}},
	}

	out := GenerateMIR(mod, nil, nil)
	assign := out.Funcs[0].Blocks[0].Instrs[0].(*Assign)
	view, ok := assign.Value.(*SliceView)
	if !ok || !view.EndExclusive || view.Start.Text() != "1" || view.End.Text() != "3" {
		t.Fatalf("expected preserved MIR range, got %#v", assign.Value)
	}
}

func TestGenerateMIRLowersIndexReadAsProjectIndexLoad(t *testing.T) {
	mod := &hir.Module{
		Name: "test",
		Funcs: []*hir.Function{
			{
				Name:       "first",
				Params:     []ir.Param{{Name: "xs", Type: "[4]i32"}},
				ReturnType: "i32",
				Body: &hir.Block{
					Stmts: []hir.Stmt{
						&hir.Return{Value: &ir.Index{
							Base:  &ir.Ident{Name: "xs", Type: "[4]i32"},
							Index: &ir.IntLit{Value: "0", Type: "i32"},
							Type:  "i32",
						}},
					},
				},
			},
		},
	}

	out := GenerateMIR(mod, nil, nil)
	if out == nil || len(out.Funcs) != 1 || len(out.Funcs[0].Blocks) != 1 {
		t.Fatalf("unexpected MIR shape: %#v", out)
	}
	instrs := out.Funcs[0].Blocks[0].Instrs
	if len(instrs) != 2 {
		t.Fatalf("expected project index and load instructions, got %#v", instrs)
	}
	project, ok := instrs[0].(*Assign)
	if !ok {
		t.Fatalf("expected first instruction assignment, got %#v", instrs[0])
	}
	if _, ok := project.Value.(*ProjectIndex); !ok {
		t.Fatalf("expected ProjectIndex, got %#v", project.Value)
	}
	load, ok := instrs[1].(*Assign)
	if !ok {
		t.Fatalf("expected second instruction assignment, got %#v", instrs[1])
	}
	if _, ok := load.Value.(*Load); !ok {
		t.Fatalf("expected Load after ProjectIndex, got %#v", load.Value)
	}
}

func TestGenerateMIRDropsOwnerBearingTemporaryAfterFieldProjection(t *testing.T) {
	ownerType := "struct{value: i32, ptr: *i32}"
	mod := &hir.Module{Name: "test", Funcs: []*hir.Function{{
		Name: "read", ReturnType: "i32", Body: &hir.Block{Stmts: []hir.Stmt{&hir.Return{Value: &ir.Field{
			Base:  &ir.Call{Callee: &ir.Ident{Name: "make", Type: "fn() -> " + ownerType}, Type: ownerType},
			Index: 0, DropBase: true, Type: "i32",
		}}}},
	}}}
	out := GenerateMIR(mod, nil, nil)
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

func TestGenerateMIRLowersSliceViewIndexReadAsProjectIndexLoad(t *testing.T) {
	mod := &hir.Module{Name: "test", Funcs: []*hir.Function{{
		Name: "first", Params: []ir.Param{{Name: "xs", Type: "&[]i32"}}, ReturnType: "i32",
		Body: &hir.Block{Stmts: []hir.Stmt{&hir.Return{Value: &ir.Index{
			Base: &ir.Ident{Name: "xs", Type: "&[]i32"}, Index: &ir.IntLit{Value: "0", Type: "i32"}, Type: "i32",
		}}}},
	}}}
	out := GenerateMIR(mod, nil, nil)
	instrs := out.Funcs[0].Blocks[0].Instrs
	if len(instrs) != 2 {
		t.Fatalf("expected project index and load instructions, got %#v", instrs)
	}
	project, projectOK := instrs[0].(*Assign)
	load, loadOK := instrs[1].(*Assign)
	if !projectOK || !loadOK {
		t.Fatalf("expected assignments, got %#v", instrs)
	}
	if _, ok := project.Value.(*ProjectIndex); !ok {
		t.Fatalf("expected ProjectIndex, got %#v", project.Value)
	}
	if _, ok := load.Value.(*Load); !ok {
		t.Fatalf("expected Load, got %#v", load.Value)
	}
}

func TestGenerateMIRLowersIndexAssignmentAsProjectIndexStore(t *testing.T) {
	mod := &hir.Module{
		Name: "test",
		Funcs: []*hir.Function{
			{
				Name: "set_first",
				Params: []ir.Param{
					{Name: "xs", Type: "[4]i32"},
					{Name: "value", Type: "i32"},
				},
				Body: &hir.Block{
					Stmts: []hir.Stmt{
						&hir.Assign{
							Target: &ir.Index{
								Base:  &ir.Ident{Name: "xs", Type: "[4]i32"},
								Index: &ir.IntLit{Value: "0", Type: "i32"},
								Type:  "i32",
							},
							Value: &ir.Ident{Name: "value", Type: "i32"},
						},
					},
				},
			},
		},
	}

	out := GenerateMIR(mod, nil, nil)
	if out == nil || len(out.Funcs) != 1 || len(out.Funcs[0].Blocks) != 1 {
		t.Fatalf("unexpected MIR shape: %#v", out)
	}
	instrs := out.Funcs[0].Blocks[0].Instrs
	if len(instrs) != 2 {
		t.Fatalf("expected project index and store instructions, got %#v", instrs)
	}
	project, ok := instrs[0].(*Assign)
	if !ok {
		t.Fatalf("expected first instruction assignment, got %#v", instrs[0])
	}
	if _, ok := project.Value.(*ProjectIndex); !ok {
		t.Fatalf("expected ProjectIndex, got %#v", project.Value)
	}
	if _, ok := instrs[1].(*Store); !ok {
		t.Fatalf("expected Store, got %#v", instrs[1])
	}
}

func TestGenerateMIRLowersMutableSliceViewIndexAssignmentAsProjectIndexStore(t *testing.T) {
	mod := &hir.Module{Name: "test", Funcs: []*hir.Function{{
		Name: "set_first", Params: []ir.Param{{Name: "xs", Type: "&mut []i32"}, {Name: "value", Type: "i32"}},
		Body: &hir.Block{Stmts: []hir.Stmt{&hir.Assign{
			Target: &ir.Index{Base: &ir.Ident{Name: "xs", Type: "&mut []i32"}, Index: &ir.IntLit{Value: "0", Type: "i32"}, Type: "i32"},
			Value:  &ir.Ident{Name: "value", Type: "i32"},
		}}},
	}}}
	out := GenerateMIR(mod, nil, nil)
	instrs := out.Funcs[0].Blocks[0].Instrs
	if len(instrs) != 2 {
		t.Fatalf("expected project index and store instructions, got %#v", instrs)
	}
	project, projectOK := instrs[0].(*Assign)
	if !projectOK {
		t.Fatalf("expected project assignment, got %#v", instrs[0])
	}
	if _, ok := project.Value.(*ProjectIndex); !ok {
		t.Fatalf("expected ProjectIndex, got %#v", project.Value)
	}
	if _, ok := instrs[1].(*Store); !ok {
		t.Fatalf("expected Store, got %#v", instrs[1])
	}
}

func TestGenerateMIRLowersArrayLiteral(t *testing.T) {
	mod := &hir.Module{
		Name: "test",
		Funcs: []*hir.Function{
			{
				Name:       "first",
				ReturnType: "[3]i32",
				Body: &hir.Block{
					Stmts: []hir.Stmt{
						&hir.Return{Value: &ir.ArrayLit{
							Values: []ir.Expr{
								&ir.IntLit{Value: "1", Type: "i32"},
								&ir.IntLit{Value: "2", Type: "i32"},
								&ir.IntLit{Value: "3", Type: "i32"},
							},
							Type: "[3]i32",
						}},
					},
				},
			},
		},
	}

	out := GenerateMIR(mod, nil, nil)
	instrs := out.Funcs[0].Blocks[0].Instrs
	if len(instrs) != 1 {
		t.Fatalf("expected array literal assign, got %#v", instrs)
	}
	assign, ok := instrs[0].(*Assign)
	if !ok {
		t.Fatalf("expected assign, got %#v", instrs[0])
	}
	lit, ok := assign.Value.(*ArrayLit)
	if !ok || lit.Type != "[3]i32" || len(lit.Values) != 3 {
		t.Fatalf("expected MIR array literal, got %#v", assign.Value)
	}
}

func TestGenerateMIRAllocatesDynamicArrayBeforeInitializers(t *testing.T) {
	mod := &hir.Module{
		Name: "test",
		Funcs: []*hir.Function{
			{
				Name:       "values",
				ReturnType: "[]i32",
				Body: &hir.Block{Stmts: []hir.Stmt{
					&hir.Return{Value: &ir.ArrayLit{
						Values: []ir.Expr{
							&ir.IntLit{Value: "1", Type: "i32"},
							&ir.IntLit{Value: "2", Type: "i32"},
						},
						Dynamic: true,
						Type:    "[]i32",
					}},
				}},
			},
		},
	}

	out := GenerateMIR(mod, nil, nil)
	block := out.Funcs[0].Blocks[0]
	if len(block.Instrs) != 5 {
		t.Fatalf("expected allocation and two indexed stores, got %#v", block.Instrs)
	}
	assign, ok := block.Instrs[0].(*Assign)
	if !ok {
		t.Fatalf("expected leading allocation assignment, got %#v", block.Instrs[0])
	}
	alloc, allocOK := assign.Value.(*DynamicArrayAlloc)
	if !allocOK || alloc.Length != 2 || alloc.Type != "[]i32" {
		t.Fatalf("expected leading dynamic allocation, got %#v", block.Instrs[0])
	}
	for _, index := range []int{1, 3} {
		project, ok := block.Instrs[index].(*Assign)
		if !ok {
			t.Fatalf("expected indexed projection at %d, got %#v", index, block.Instrs[index])
		}
		if _, ok := project.Value.(*ProjectIndex); !ok {
			t.Fatalf("expected ProjectIndex at %d, got %#v", index, project.Value)
		}
		if _, ok := block.Instrs[index+1].(*Store); !ok {
			t.Fatalf("expected Store at %d, got %#v", index+1, block.Instrs[index+1])
		}
	}
}

func TestGenerateMIRLowersDynamicArrayOwnerOperations(t *testing.T) {
	for _, op := range []symbols.CompilerOp{symbols.CompilerOpAppend, symbols.CompilerOpReserve, symbols.CompilerOpResize} {
		t.Run(string(op), func(t *testing.T) {
			expr := &ir.DynamicArrayOp{
				Op:     op,
				Array:  &ir.Ident{Name: "values", Type: "[]i32"},
				Length: &ir.IntLit{Value: "8", Type: "usize"},
				Value:  &ir.IntLit{Value: "1", Type: "i32"},
				Type:   "[]i32",
			}
			if op == symbols.CompilerOpAppend {
				expr.Length = nil
			}
			if op == symbols.CompilerOpReserve {
				expr.Value = nil
			}
			mod := &hir.Module{
				Name: "test",
				Funcs: []*hir.Function{{
					Name:       "grow",
					Params:     []ir.Param{{Name: "values", Type: "[]i32"}},
					ReturnType: "[]i32",
					Body:       &hir.Block{Stmts: []hir.Stmt{&hir.Return{Value: expr}}},
				}},
			}
			out := GenerateMIR(mod, nil, nil)
			assign, ok := out.Funcs[0].Blocks[0].Instrs[0].(*Assign)
			if !ok {
				t.Fatalf("expected operation assignment, got %#v", out.Funcs[0].Blocks[0].Instrs)
			}
			got, ok := assign.Value.(*DynamicArrayOp)
			if !ok || got.Op != op || got.Type != "[]i32" {
				t.Fatalf("operation = %#v, want %s []i32", assign.Value, op)
			}
		})
	}
}

func TestGenerateMIRPreservesNestedExpressionLocations(t *testing.T) {
	testPath := "test" + peeper.SourceExt
	mod := &hir.Module{
		Name: "test",
		Funcs: []*hir.Function{
			{
				Name:       "main",
				ReturnType: "i32",
				Body: &hir.Block{
					Stmts: []hir.Stmt{
						&hir.Return{
							Value: &ir.Binary{
								Op: "*",
								Left: &ir.Binary{
									Op:       "+",
									Left:     &ir.IntLit{Value: "1", Type: "i32", Location: source.NewLocation(testPath, source.Position{Line: 2, Column: 2}, source.Position{Line: 2, Column: 3})},
									Right:    &ir.IntLit{Value: "2", Type: "i32", Location: source.NewLocation(testPath, source.Position{Line: 2, Column: 6}, source.Position{Line: 2, Column: 7})},
									Type:     "i32",
									Location: source.NewLocation(testPath, source.Position{Line: 2, Column: 2}, source.Position{Line: 2, Column: 7}),
								},
								Right:    &ir.IntLit{Value: "3", Type: "i32", Location: source.NewLocation(testPath, source.Position{Line: 3, Column: 2}, source.Position{Line: 3, Column: 3})},
								Type:     "i32",
								Location: source.NewLocation(testPath, source.Position{Line: 3, Column: 2}, source.Position{Line: 3, Column: 7}),
							},
							Location: source.NewLocation(testPath, source.Position{Line: 4, Column: 2}, source.Position{Line: 4, Column: 8}),
						},
					},
				},
			},
		},
	}

	out := GenerateMIR(mod, nil, nil)
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
		Name: "test",
		Funcs: []*hir.Function{
			{
				Name:       "main",
				ReturnType: "void",
				Body: &hir.Block{
					Stmts: []hir.Stmt{
						&hir.For{
							Cond: &ir.IntLit{Value: "1", Type: "bool"},
							Body: &hir.Block{
								Stmts: []hir.Stmt{
									&hir.ExprStmt{
										Value: &ir.Call{
											Callee: &ir.Ident{Name: "Ping", Type: "fn() -> i32"},
											Type:   "i32",
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

	out := GenerateMIR(mod, nil, nil)
	if out == nil || len(out.Funcs) != 1 {
		t.Fatalf("expected one MIR function, got %#v", out)
	}
	fn := out.Funcs[0]
	if len(fn.Blocks) != 4 {
		t.Fatalf("expected four blocks for loop, got %#v", fn.Blocks)
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
