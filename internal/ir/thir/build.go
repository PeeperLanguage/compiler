package thir

import (
	"fmt"

	"compiler/internal/frontend/ast"
	"compiler/internal/ir"
	"compiler/internal/semantics/bindingresult"
	"compiler/internal/semantics/place"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typecheckresult"
	"compiler/internal/semantics/typeinfo"
	"compiler/pkg/typednil"
)

// Build materializes base-typechecked syntax into one self-contained semantic
// tree. It does not resolve names or infer types: missing published evidence is
// represented explicitly and rejected by Validate for otherwise-clean source.
func Build(name, filePath string, source *ast.Module, bindings *bindingresult.Result, typing *typecheckresult.Result) *Module {
	if source == nil {
		return nil
	}
	builder := &builder{bindings: bindings, typing: typing}
	module := &Module{Name: name, FilePath: filePath, Functions: make([]*Function, 0), byNodeID: make(map[ir.NodeID]Node)}
	ast.ForEachDecl(source, func(declaration ast.Decl) bool {
		function, ok := declaration.(*ast.FnDecl)
		if !ok || function == nil {
			return true
		}
		module.Functions = append(module.Functions, builder.function(function))
		return true
	})
	for _, function := range module.Functions {
		if function == nil || function.Body == nil {
			continue
		}
		Inspect(function.Body, func(node Node) bool {
			if id := node.SourceInfo().NodeID; id != 0 {
				module.byNodeID[id] = node
			}
			return true
		})
	}
	return module
}

type builder struct {
	bindings *bindingresult.Result
	typing   *typecheckresult.Result
}

func (b *builder) function(source *ast.FnDecl) *Function {
	function := &Function{
		Source:         sourceInfo(source),
		ReturnTypeText: ast.TypeText(source.ReturnType),
		ReturnsValue:   source.ReturnType != nil,
	}
	if source.Name != nil {
		function.Name = source.Name.Name
		function.Symbol = b.symbol(source.Name)
	}
	if function.Symbol != nil {
		if typ, ok := symbols.GetSymbolType(function.Symbol); ok {
			if callable, ok := typ.(*typeinfo.FuncType); ok && callable != nil {
				function.ReturnType = callable.Return
			}
		}
	}
	for _, parameter := range source.ParamsWithReceiver() {
		param := Param{Source: sourceInfo(parameter.Name), Symbol: b.symbol(parameter.Name)}
		if param.Symbol != nil {
			param.Type, _ = symbols.GetSymbolType(param.Symbol)
		}
		function.Params = append(function.Params, param)
	}
	function.Body = b.block(source.Body)
	return function
}

func (b *builder) block(source *ast.BlockStmt) *Block {
	if source == nil {
		return nil
	}
	block := &Block{StmtInfo: stmtInfo(source), Scope: b.scope(source), Stmts: make([]Stmt, 0, len(source.Stmts))}
	for _, statement := range source.Stmts {
		if lowered := b.statement(statement); lowered != nil {
			block.Stmts = append(block.Stmts, lowered)
		}
	}
	return block
}

func (b *builder) statement(statement ast.Stmt) Stmt {
	switch node := statement.(type) {
	case nil:
		return nil
	case *ast.BlockStmt:
		return b.block(node)
	case *ast.LetDecl:
		return &Binding{StmtInfo: stmtInfo(node), Symbol: b.symbol(node.Name), Inferred: node.Type == nil, Value: b.expression(node.Value)}
	case *ast.ConstDecl:
		return &Binding{StmtInfo: stmtInfo(node), Symbol: b.symbol(node.Name), Constant: true, Inferred: node.Type == nil, Value: b.expression(node.Value)}
	case *ast.ExprStmt:
		return &ExprStmt{StmtInfo: stmtInfo(node), Value: b.expression(node.Expr)}
	case *ast.AssignStmt:
		return &Assign{StmtInfo: stmtInfo(node), Target: b.expression(node.Target), Value: b.expression(node.Value)}
	case *ast.ReturnStmt:
		return &Return{StmtInfo: stmtInfo(node), Value: b.expression(node.Value)}
	case *ast.IfStmt:
		return &If{StmtInfo: stmtInfo(node), Condition: b.expression(node.Cond), Then: b.block(node.Then), Else: b.statement(node.Else)}
	case *ast.ForStmt:
		return b.forStatement(node)
	case *ast.BreakStmt:
		return &Break{StmtInfo: stmtInfo(node)}
	case *ast.ContinueStmt:
		return &Continue{StmtInfo: stmtInfo(node)}
	case *ast.MatchStmt:
		return b.matchStatement(node)
	case *ast.BadStmt, *ast.BadDecl:
		return &InvalidStmt{StmtInfo: stmtInfo(node), Message: "invalid source statement"}
	case *ast.ImportDecl, *ast.FnDecl, *ast.TypeAliasDecl, *ast.StructDecl, *ast.InterfaceDecl, *ast.EnumDecl:
		return &InvalidStmt{StmtInfo: stmtInfo(node), Message: "declaration is not executable in function body"}
	default:
		panic(fmt.Sprintf("THIR: unhandled statement %T", statement))
	}
}

func (b *builder) forStatement(source *ast.ForStmt) *For {
	loop := &For{StmtInfo: stmtInfo(source)}
	if b.typing != nil {
		if checked := b.typing.CheckedIteration(source.ID()); checked != nil {
			loop.Checked = b.block(checked)
			return loop
		}
	}
	loop.Index = b.symbol(source.Index)
	loop.Value = b.symbol(source.Value)
	loop.Iterable = b.expression(source.Iterable)
	loop.Condition = b.expression(source.Cond)
	loop.Body = b.block(source.Body)
	if b.typing == nil {
		return loop
	}
	evidence, found := b.typing.ForIteration(source.ID())
	if !found {
		return loop
	}
	switch plan := evidence.Plan.(type) {
	case *typecheckresult.RangeIteration:
		loop.Iteration = &RangeIteration{
			ElementType: evidence.ElementType, Cursor: evidence.Cursor, Limit: plan.Limit,
			Ordinal: plan.Ordinal, GuaranteedEntry: evidence.GuaranteedEntry,
		}
	case *typecheckresult.SequenceIteration:
		loop.Iteration = &SequenceIteration{
			ElementType: evidence.ElementType, Cursor: evidence.Cursor, Value: evidence.Value,
			Index: evidence.Index, Carrier: plan.Carrier, CarrierType: plan.CarrierType,
			GuaranteedEntry: evidence.GuaranteedEntry,
		}
	}
	return loop
}

func (b *builder) matchStatement(source *ast.MatchStmt) Stmt {
	match := &Match{StmtInfo: stmtInfo(source), Subject: b.expression(source.Subject)}
	if b.typing == nil {
		return match
	}
	evidence, found := b.typing.Match(source.ID())
	if !found {
		return match
	}
	match.EnumType = evidence.EnumType
	match.CaseCount = evidence.CaseCount
	for index, arm := range evidence.Arms {
		if index >= len(source.Arms) || source.Arms[index] == nil {
			break
		}
		sourceArm := source.Arms[index]
		lowered := MatchArm{
			Source: sourceInfo(sourceArm), Case: arm.Case, Payload: arm.Payload,
			CarrierUse: arm.CarrierUse, Body: b.block(sourceArm.Body),
		}
		for _, binding := range arm.Bindings {
			projection := MatchPayloadField
			if binding.Projection == typecheckresult.MatchWholePayload {
				projection = MatchWholePayload
			}
			lowered.Bindings = append(lowered.Bindings, MatchBinding{
				Projection: projection, Field: binding.Field, Type: binding.Type,
				Symbol: binding.Binding, Discard: binding.Discard,
			})
		}
		match.Arms = append(match.Arms, lowered)
	}
	return match
}

func (b *builder) expression(expression ast.Expr) Expr {
	if expression == nil {
		return nil
	}
	info := b.expressionInfo(expression)
	if b.typing != nil {
		if construction, found := b.typing.VariantConstruction(expression.ID()); found {
			if info.Type == nil {
				info.Type = construction.EnumType
			}
			return &Variant{ExprInfo: info, Case: construction.Case, Payload: b.expression(construction.Value)}
		}
	}

	var result Expr
	switch node := expression.(type) {
	case *ast.NumberLit:
		result = &NumberLiteral{ExprInfo: info, Value: node.Value, ExplicitType: node.ExplicitType}
	case *ast.StringLit:
		result = &StringLiteral{ExprInfo: info, Value: node.Value, CString: node.CString}
	case *ast.ByteLit:
		result = &ByteLiteral{ExprInfo: info, Value: node.Value}
	case *ast.CharLit:
		result = &CharLiteral{ExprInfo: info, Value: node.Value}
	case *ast.BoolLit:
		result = &BoolLiteral{ExprInfo: info, Value: node.Value}
	case *ast.NoneLit:
		result = &NoneLiteral{ExprInfo: info}
	case *ast.Ident:
		symbol := b.symbol(node)
		if info.Type == nil && symbol != nil {
			info.Type, _ = symbols.GetSymbolType(symbol)
		}
		if isStorageSymbol(symbol) {
			info.Place = &Place{Root: symbol, Type: info.Type}
		}
		result = &Ident{ExprInfo: info, Name: node.Name, Symbol: symbol}
	case *ast.ScopeResolution:
		symbol := b.symbol(node)
		if info.Type == nil && symbol != nil {
			info.Type, _ = symbols.GetSymbolType(symbol)
		}
		if isStorageSymbol(symbol) {
			info.Place = &Place{Root: symbol, Type: info.Type}
		}
		result = &QualifiedIdent{ExprInfo: info, Name: node.TypeText(), Symbol: symbol}
	case *ast.SelectorExpr:
		result = b.fieldExpression(node, info)
	case *ast.IndexExpr:
		result = b.indexExpression(node, info)
	case *ast.RangeExpr:
		result = &Range{ExprInfo: info, Start: b.expression(node.Start), End: b.expression(node.End), EndExclusive: node.EndExclusive}
	case *ast.StructLit:
		result = b.structLiteral(node, info)
	case *ast.VariantLit:
		result = &InvalidExpr{ExprInfo: info, Message: "variant construction missing semantic evidence"}
	case *ast.ArrayLit:
		values := make([]Expr, 0, len(node.Values))
		for _, value := range node.Values {
			values = append(values, b.expression(value))
		}
		result = &ArrayLiteral{ExprInfo: info, Values: values}
	case *ast.AddressExpr:
		mode := AddressRaw
		switch node.Mode {
		case ast.AddressShared:
			mode = AddressShared
		case ast.AddressMutable:
			mode = AddressMutable
		}
		result = &Address{ExprInfo: info, Mode: mode, Value: b.expression(node.Expr)}
	case *ast.UnaryExpr:
		result = &Unary{ExprInfo: info, Op: node.Op, Value: b.expression(node.Expr)}
	case *ast.BinaryExpr:
		concat := b.typing != nil && b.typing.StringConcatenation(node.ID())
		result = &Binary{
			ExprInfo: info, Left: b.expression(node.Left), Op: node.Op,
			Right: b.expression(node.Right), StringConcat: concat, Test: b.caseTest(node.ID()),
		}
	case *ast.IsExpr:
		result = &Is{ExprInfo: info, Value: b.expression(node.Value), Test: b.caseTest(node.ID())}
	case *ast.CallExpr:
		result = b.callExpression(node, info)
	case *ast.FreeExpr:
		result = &Free{ExprInfo: info, Value: b.expression(node.Expr)}
	case *ast.PrintExpr:
		result = &Print{ExprInfo: info, Value: b.expression(node.Expr), Newline: node.Newline}
	case *ast.AsExpr:
		result = &Cast{ExprInfo: info, Value: b.expression(node.Expr), TargetType: info.Type}
	case *ast.BadExpr:
		result = &InvalidExpr{ExprInfo: info, Message: "invalid source expression"}
	default:
		panic(fmt.Sprintf("THIR: unhandled expression %T", expression))
	}
	return result
}

func (b *builder) fieldExpression(source *ast.SelectorExpr, info ExprInfo) Expr {
	field := &Field{ExprInfo: info, Base: b.expression(source.Expr)}
	if source.Name != nil {
		field.Name = source.Name.Name
		field.Symbol = b.symbol(source.Name)
		if info.Type == nil && field.Symbol != nil {
			info.Type, _ = symbols.GetSymbolType(field.Symbol)
			field.ExprInfo.Type = info.Type
		}
	}
	if b.typing != nil {
		if access, found := b.typing.StructField(source.ID()); found {
			if field.ExprInfo.Type == nil {
				field.ExprInfo.Type = access.Type
			}
			field.Access = &FieldAccess{Field: access.Field, DereferenceType: access.DereferenceType}
		}
	}
	if projection, projected := place.Project(source); projected {
		fieldIndex := -1
		if field.Access != nil {
			fieldIndex = field.Access.Field
		}
		field.ExprInfo.Place = projectPlace(field.Base, PlaceProjection{
			Kind: PlaceField, Name: projection.Step.Field, Field: fieldIndex, Type: field.ExprInfo.Type,
		}, field.ExprInfo.Type)
	}
	return field
}

func (b *builder) indexExpression(source *ast.IndexExpr, info ExprInfo) Expr {
	index := &Index{ExprInfo: info, Base: b.expression(source.Expr), Index: b.expression(source.Index)}
	if b.typing != nil {
		if constant, found := b.typing.ConstantIndex(source.ID()); found {
			index.Constant = &ConstantIndex{Text: constant.Text, Type: constant.Type}
		}
	}
	if _, ranged := source.Index.(*ast.RangeExpr); !ranged && index.Index != nil {
		if _, projected := place.Project(source); projected {
			index.ExprInfo.Place = projectPlace(index.Base, PlaceProjection{
				Kind: PlaceIndex, Index: index.Index, ConstantIndex: index.Constant, Type: index.ExprInfo.Type,
			}, index.ExprInfo.Type)
		}
	}
	return index
}

func (b *builder) structLiteral(source *ast.StructLit, info ExprInfo) Expr {
	values := make([]ast.Expr, 0, len(source.Fields))
	if b.typing != nil {
		if ordered, found := b.typing.StructLiteralFields(source.ID()); found {
			values = ordered
		}
	}
	if values == nil || len(values) == 0 && len(source.Fields) != 0 {
		for _, field := range source.Fields {
			values = append(values, field.Value)
		}
	}
	fields := make([]StructField, 0, len(values))
	semantic, _ := typeinfo.Underlying(info.Type).(*typeinfo.StructType)
	for index, value := range values {
		name := ""
		if semantic != nil && index < len(semantic.Fields) {
			name = semantic.Fields[index].Name
		}
		fields = append(fields, StructField{Name: name, Index: index, Value: b.expression(value)})
	}
	return &StructLiteral{ExprInfo: info, Fields: fields}
}

func (b *builder) callExpression(source *ast.CallExpr, info ExprInfo) Expr {
	call := &Call{ExprInfo: info, Callee: b.expression(source.Callee), Piped: source.Piped}
	arguments := source.Args
	if b.typing != nil {
		arguments = b.typing.CallArgumentsOrSource(source)
		call.ImplicitArgument = b.typing.ImplicitCallArgument(source.ID())
		if compilerCall, found := b.typing.CompilerCall(source.ID()); found {
			call.CompilerCall = &CompilerCall{Operation: compilerCall.Operation, Kind: compilerCall.Kind}
		}
	}
	for _, argument := range arguments {
		call.Args = append(call.Args, b.expression(argument))
	}
	return call
}

func (b *builder) expressionInfo(expression ast.Expr) ExprInfo {
	info := ExprInfo{Source: sourceInfo(expression)}
	if b.typing == nil {
		return info
	}
	info.Type = b.typing.ExprType(expression.ID())
	if conversion, found := b.typing.ImplicitConversion(expression.ID()); found {
		copied := conversion
		info.Conversion = &copied
	}
	if use, found := b.typing.ValueUse(expression.ID()); found {
		info.Use = use
		info.HasUse = true
	}
	if mutable, found := b.typing.ReferenceArgument(expression.ID()); found {
		info.ReferenceArgument = true
		info.ReferenceArgumentMutable = mutable
	}
	for _, implementation := range b.typing.InterfaceImplementations(expression.ID()) {
		info.InterfaceImplementations = append(info.InterfaceImplementations, InterfaceImplementation{
			Symbol: implementation.Symbol, CallableType: implementation.CallableType,
		})
	}
	return info
}

func (b *builder) caseTest(id ast.NodeID) *CaseTest {
	if b.typing == nil {
		return nil
	}
	test, found := b.typing.CaseTest(id)
	if !found {
		return nil
	}
	return &CaseTest{
		SubjectID: ir.NodeID(test.SubjectID), Case: test.Case, CaseWhenTrue: test.CaseWhenTrue,
		CaseCount: test.CaseCount, Family: test.Family,
	}
}

func projectPlace(base Expr, projection PlaceProjection, typ typeinfo.Type) *Place {
	if base == nil {
		return nil
	}
	if existing := base.ExprPlace(); existing != nil {
		projected := &Place{
			Root: existing.Root, Temporary: existing.Temporary, Type: typ,
			Projections: append([]PlaceProjection(nil), existing.Projections...),
		}
		projected.Projections = append(projected.Projections, projection)
		return projected
	}
	return &Place{Temporary: base, Projections: []PlaceProjection{projection}, Type: typ}
}

func (b *builder) symbol(node ast.Node) *symbols.Symbol {
	if b.bindings == nil || typednil.IsNil(node) {
		return nil
	}
	return b.bindings.Symbol(node)
}

func (b *builder) scope(node ast.Node) *symbols.Scope {
	if b.bindings == nil || typednil.IsNil(node) {
		return nil
	}
	return b.bindings.Scope(node)
}

func sourceInfo(node ast.Node) ir.SourceInfo {
	if typednil.IsNil(node) {
		return ir.SourceInfo{}
	}
	return ir.SourceInfo{NodeID: ir.NodeID(node.ID()), Location: ast.LocOf(node)}
}

func stmtInfo(node ast.Node) StmtInfo { return StmtInfo{Source: sourceInfo(node)} }

func isStorageSymbol(symbol *symbols.Symbol) bool {
	if symbol == nil {
		return false
	}
	switch symbol.Kind {
	case symbols.SymbolVar, symbols.SymbolConst, symbols.SymbolParam:
		return true
	default:
		return false
	}
}
