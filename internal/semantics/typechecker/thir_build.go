package typechecker

import (
	"fmt"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/ir"
	"compiler/internal/ir/thir"
	"compiler/internal/semantics/place"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
	sourceid "compiler/internal/source"
	"compiler/pkg/typednil"
)

func (c *checker) buildTHIR() *thir.Module {
	if c == nil || c.module == nil || c.module.AST == nil {
		return nil
	}
	constantCondition := func(expr ast.Expr) (*bool, []*diagnostics.Diagnostic) {
		pending := diagnostics.NewDiagnosticBag()
		value, ok := c.evaluateConstant(c.ctx.WithDiagnostics(pending), expr, &typeinfo.BoolType{})
		if !ok {
			return nil, pending.Diagnostics()
		}
		truth := value != nil && value.Truthy()
		return &truth, pending.Diagnostics()
	}
	builder := &thirBuilder{symbolIndex: c.module.SymbolIndex, typing: c.evidence, constantCondition: constantCondition}
	functions := make([]*thir.Function, 0)
	ast.ForEachDecl(c.module.AST, func(declaration ast.Decl) bool {
		function, ok := declaration.(*ast.FnDecl)
		if !ok || function == nil {
			return true
		}
		functions = append(functions, builder.function(function))
		return true
	})
	return thir.NewModule(c.module.ID.ImportPath, c.module.FilePath, functions)
}

type thirBuilder struct {
	symbolIndex       *symbols.Index
	typing            *evidence
	constantCondition func(ast.Expr) (*bool, []*diagnostics.Diagnostic)
}

func (b *thirBuilder) function(source *ast.FnDecl) *thir.Function {
	function := &thir.Function{
		Identity:          source.ID().Function(),
		Source:            sourceInfo(source),
		IsEntrypointShape: source.Receiver == nil && source.Body != nil && len(source.TypeParams) == 0,
		ReturnTypeText:    ast.TypeText(source.ReturnType),
		HasReturnValue:    source.ReturnType != nil,
	}
	if source.ReturnOrigins != nil {
		function.ReturnOriginsLocation = source.ReturnOrigins.Location
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
		param := thir.Param{Source: sourceInfo(parameter.Name), Symbol: b.symbol(parameter.Name)}
		if param.Symbol != nil {
			param.Type, _ = symbols.GetSymbolType(param.Symbol)
		}
		function.Params = append(function.Params, param)
	}
	function.Body = b.block(source.Body)
	return function
}

func (b *thirBuilder) block(source *ast.BlockStmt) *thir.Block {
	if source == nil {
		return nil
	}
	block := &thir.Block{StmtInfo: stmtInfo(source), Scope: b.scope(source), Stmts: make([]thir.Stmt, 0, len(source.Stmts))}
	for _, statement := range source.Stmts {
		if lowered := b.statement(statement); lowered != nil {
			block.Stmts = append(block.Stmts, lowered)
		}
	}
	return block
}

func (b *thirBuilder) statement(statement ast.Stmt) thir.Stmt {
	switch node := statement.(type) {
	case nil:
		return nil
	case *ast.BlockStmt:
		return b.block(node)
	case *ast.LetDecl:
		return &thir.Binding{StmtInfo: stmtInfo(node), Symbol: b.symbol(node.Name), IsInferred: node.Type == nil, Value: b.expression(node.Value)}
	case *ast.ConstDecl:
		return &thir.Binding{StmtInfo: stmtInfo(node), Symbol: b.symbol(node.Name), IsConstant: true, IsInferred: node.Type == nil, Value: b.expression(node.Value)}
	case *ast.ExprStmt:
		return &thir.ExprStmt{StmtInfo: stmtInfo(node), Value: b.expression(node.Expr)}
	case *ast.AssignStmt:
		return &thir.Assign{StmtInfo: stmtInfo(node), Target: b.expression(node.Target), Value: b.expression(node.Value)}
	case *ast.ReturnStmt:
		return &thir.Return{StmtInfo: stmtInfo(node), Value: b.expression(node.Value)}
	case *ast.IfStmt:
		statement := &thir.If{StmtInfo: stmtInfo(node), Condition: b.expression(node.Cond)}
		if b.constantCondition != nil {
			statement.ConstantCondition, statement.ConditionDiagnostics = b.constantCondition(node.Cond)
		}
		statement.Then = b.block(node.Then)
		statement.Else = b.statement(node.Else)
		return statement
	case *ast.ForStmt:
		return b.forStatement(node)
	case *ast.BreakStmt:
		return &thir.Break{StmtInfo: stmtInfo(node)}
	case *ast.ContinueStmt:
		return &thir.Continue{StmtInfo: stmtInfo(node)}
	case *ast.MatchStmt:
		return b.matchStatement(node)
	case *ast.BadStmt, *ast.BadDecl:
		return &thir.InvalidStmt{StmtInfo: stmtInfo(node), Message: "invalid source statement"}
	case *ast.ImportDecl, *ast.FnDecl, *ast.TypeAliasDecl, *ast.StructDecl, *ast.InterfaceDecl, *ast.EnumDecl:
		return &thir.InvalidStmt{StmtInfo: stmtInfo(node), Message: "declaration is not executable in function body"}
	default:
		panic(fmt.Sprintf("THIR: unhandled statement %T", statement))
	}
}

func (b *thirBuilder) forStatement(source *ast.ForStmt) *thir.For {
	loop := &thir.For{StmtInfo: stmtInfo(source)}
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
	case *RangeIteration:
		loop.Iteration = &thir.RangeIteration{
			ElementType: evidence.ElementType, Cursor: evidence.Cursor, Limit: plan.Limit,
			Ordinal: plan.Ordinal, HasGuaranteedEntry: evidence.HasGuaranteedEntry,
		}
	case *SequenceIteration:
		loop.Iteration = &thir.SequenceIteration{
			ElementType: evidence.ElementType, Cursor: evidence.Cursor, Value: evidence.Value,
			Index: evidence.Index, Carrier: plan.Carrier, CarrierType: plan.CarrierType,
			HasGuaranteedEntry: evidence.HasGuaranteedEntry,
		}
	}
	return loop
}

func (b *thirBuilder) matchStatement(source *ast.MatchStmt) thir.Stmt {
	match := &thir.Match{StmtInfo: stmtInfo(source), Subject: b.expression(source.Subject)}
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
		lowered := thir.MatchArm{
			Source: sourceInfo(sourceArm), Case: arm.Case, Payload: arm.Payload,
			CarrierUse: arm.CarrierUse, Body: b.block(sourceArm.Body),
		}
		for _, binding := range arm.Bindings {
			var bindingSource ir.SourceInfo
			if binding.Binding != nil {
				bindingSource = sourceInfo(binding.Binding.ASTNode)
			}
			projection := thir.MatchPayloadField
			if binding.Projection == MatchWholePayload {
				projection = thir.MatchWholePayload
			}
			lowered.Bindings = append(lowered.Bindings, thir.MatchBinding{
				Source: bindingSource, Projection: projection, Field: binding.Field, Type: binding.Type,
				Symbol: binding.Binding, IsDiscard: binding.IsDiscard,
			})
		}
		match.Arms = append(match.Arms, lowered)
	}
	return match
}

func (b *thirBuilder) expression(expression ast.Expr) thir.Expr {
	if expression == nil {
		return nil
	}
	info := b.expressionInfo(expression)
	if b.typing != nil {
		if construction, found := b.typing.VariantConstruction(expression.ID()); found {
			if info.Type == nil {
				info.Type = construction.EnumType
			}
			return &thir.Variant{
				ExprInfo: info, Case: construction.Case,
				Payload: b.expression(construction.Value), PayloadType: construction.Payload,
			}
		}
	}

	var result thir.Expr
	switch node := expression.(type) {
	case *ast.NumberLit:
		result = &thir.NumberLiteral{ExprInfo: info, Value: node.Value, ExplicitType: node.ExplicitType}
	case *ast.StringLit:
		result = &thir.StringLiteral{ExprInfo: info, Value: node.Value, IsCString: node.IsCString}
	case *ast.ByteLit:
		result = &thir.ByteLiteral{ExprInfo: info, Value: node.Value}
	case *ast.CharLit:
		result = &thir.CharLiteral{ExprInfo: info, Value: node.Value}
	case *ast.BoolLit:
		result = &thir.BoolLiteral{ExprInfo: info, Value: node.Value}
	case *ast.NoneLit:
		result = &thir.NoneLiteral{ExprInfo: info}
	case *ast.Ident:
		symbol := b.symbol(node)
		if info.Type == nil && symbol != nil {
			info.Type, _ = symbols.GetSymbolType(symbol)
		}
		if isStorageSymbol(symbol) {
			info.Place = &thir.Place{Root: symbol, Type: info.Type}
		}
		result = &thir.Ident{ExprInfo: info, Name: node.Name, Symbol: symbol, IsExpandedDefaultBinding: b.typing != nil && b.typing.ExpandedDefaultBinding(node.ID())}
	case *ast.ScopeResolution:
		symbol := b.symbol(node)
		if info.Type == nil && symbol != nil {
			info.Type, _ = symbols.GetSymbolType(symbol)
		}
		if isStorageSymbol(symbol) {
			info.Place = &thir.Place{Root: symbol, Type: info.Type}
		}
		result = &thir.QualifiedIdent{ExprInfo: info, Name: node.TypeText(), Symbol: symbol}
	case *ast.SelectorExpr:
		result = b.fieldExpression(node, info)
	case *ast.IndexExpr:
		result = b.indexExpression(node, info)
	case *ast.RangeExpr:
		result = &thir.Range{ExprInfo: info, Start: b.expression(node.Start), End: b.expression(node.End), IsEndExclusive: node.IsEndExclusive}
	case *ast.StructLit:
		result = b.structLiteral(node, info)
	case *ast.VariantLit:
		result = &thir.InvalidExpr{ExprInfo: info, Message: "variant construction missing semantic evidence"}
	case *ast.ArrayLit:
		values := make([]thir.Expr, 0, len(node.Values))
		for _, value := range node.Values {
			values = append(values, b.expression(value))
		}
		result = &thir.ArrayLiteral{ExprInfo: info, Values: values}
	case *ast.AddressExpr:
		mode := thir.AddressRaw
		switch node.Mode {
		case ast.AddressShared:
			mode = thir.AddressShared
		case ast.AddressMutable:
			mode = thir.AddressMutable
		}
		result = &thir.Address{ExprInfo: info, Mode: mode, Value: b.expression(node.Expr)}
	case *ast.UnaryExpr:
		result = &thir.Unary{ExprInfo: info, Op: node.Op, Value: b.expression(node.Expr)}
	case *ast.BinaryExpr:
		isStringConcatenation := b.typing != nil && b.typing.StringConcatenation(node.ID())
		result = &thir.Binary{
			ExprInfo: info, Left: b.expression(node.Left), Op: node.Op,
			Right: b.expression(node.Right), IsStringConcatenation: isStringConcatenation, Test: b.caseTest(node.ID()),
		}
	case *ast.IsExpr:
		result = &thir.Is{ExprInfo: info, Value: b.expression(node.Value), Test: b.caseTest(node.ID())}
	case *ast.CallExpr:
		result = b.callExpression(node, info)
	case *ast.FreeExpr:
		result = &thir.Free{ExprInfo: info, Value: b.expression(node.Expr)}
	case *ast.PrintExpr:
		result = &thir.Print{ExprInfo: info, Value: b.expression(node.Expr), AppendsNewline: node.AppendsNewline}
	case *ast.AsExpr:
		result = &thir.Cast{ExprInfo: info, Value: b.expression(node.Expr), TargetType: info.Type}
	case *ast.BadExpr:
		result = &thir.InvalidExpr{ExprInfo: info, Message: "invalid source expression"}
	default:
		panic(fmt.Sprintf("THIR: unhandled expression %T", expression))
	}
	return result
}

func (b *thirBuilder) fieldExpression(source *ast.SelectorExpr, info thir.ExprInfo) thir.Expr {
	field := &thir.Field{ExprInfo: info, Base: b.expression(source.Expr)}
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
			field.Access = &thir.FieldAccess{Field: access.Field, DereferenceType: access.DereferenceType}
		}
	}
	if projection, projected := place.Project(source); projected {
		fieldIndex := -1
		var dereferenceType typeinfo.Type
		if field.Access != nil {
			fieldIndex = field.Access.Field
			dereferenceType = field.Access.DereferenceType
		}
		field.ExprInfo.Place = projectPlace(field.Base, thir.PlaceProjection{
			Source: field.Source, BaseSource: field.Base.SourceInfo(),
			Kind: thir.PlaceField, Name: projection.Step.Field, Field: fieldIndex,
			DereferenceType: dereferenceType, Type: field.ExprInfo.Type,
		}, field.ExprInfo.Type)
	}
	return field
}

func (b *thirBuilder) indexExpression(source *ast.IndexExpr, info thir.ExprInfo) thir.Expr {
	index := &thir.Index{ExprInfo: info, Base: b.expression(source.Expr), Index: b.expression(source.Index)}
	if b.typing != nil {
		if constant, found := b.typing.ConstantIndex(source.ID()); found {
			index.Constant = &thir.ConstantIndex{Text: constant.Text, Type: constant.Type}
		}
	}
	if _, ranged := source.Index.(*ast.RangeExpr); !ranged && index.Index != nil {
		if _, projected := place.Project(source); projected {
			index.ExprInfo.Place = projectPlace(index.Base, thir.PlaceProjection{
				Source: index.Source, BaseSource: index.Base.SourceInfo(),
				Kind: thir.PlaceIndex, Index: index.Index, ConstantIndex: index.Constant, Type: index.ExprInfo.Type,
			}, index.ExprInfo.Type)
		}
	}
	return index
}

func (b *thirBuilder) structLiteral(source *ast.StructLit, info thir.ExprInfo) thir.Expr {
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
	fields := make([]thir.StructField, 0, len(values))
	semantic, _ := typeinfo.Underlying(info.Type).(*typeinfo.StructType)
	for index, value := range values {
		name := ""
		if semantic != nil && index < len(semantic.Fields) {
			name = semantic.Fields[index].Name
		}
		fields = append(fields, thir.StructField{Name: name, Index: index, Value: b.expression(value)})
	}
	return &thir.StructLiteral{ExprInfo: info, Fields: fields}
}

func (b *thirBuilder) callExpression(source *ast.CallExpr, info thir.ExprInfo) thir.Expr {
	call := &thir.Call{ExprInfo: info, Callee: b.expression(source.Callee), IsPiped: source.IsPiped}
	arguments := source.Args
	if b.typing != nil {
		arguments = b.typing.CallArgumentsOrSource(source)
		if compilerCall, found := b.typing.CompilerCall(source.ID()); found {
			call.CompilerCall = &thir.CompilerCall{Operation: compilerCall.Operation, Kind: compilerCall.Kind}
		}
	}
	for _, argument := range arguments {
		call.Args = append(call.Args, b.expression(argument))
	}
	return call
}

func (b *thirBuilder) expressionInfo(expression ast.Expr) thir.ExprInfo {
	info := thir.ExprInfo{Source: sourceInfo(expression)}
	if b.typing == nil {
		return info
	}
	info.Type = b.typing.ExprType(expression.ID())
	if conversion, found := b.typing.ImplicitConversion(expression.ID()); found {
		copied := conversion
		info.ConversionInfo = &copied
	}
	if use, found := b.typing.ValueUse(expression.ID()); found {
		info.Use = use
		info.HasUse = true
	}
	if isMutable, found := b.typing.ReferenceArgument(expression.ID()); found {
		info.HasReferenceArgument = true
		info.IsReferenceArgumentMutable = isMutable
	}
	info.ImplicitReference = b.typing.ImplicitCallArgument(expression.ID())
	for _, implementation := range b.typing.InterfaceImplementations(expression.ID()) {
		info.Implementations = append(info.Implementations, thir.InterfaceImplementation{
			Symbol: implementation.Symbol, CallableType: implementation.CallableType,
		})
	}
	return info
}

func (b *thirBuilder) caseTest(id sourceid.NodeID) *thir.CaseTest {
	if b.typing == nil {
		return nil
	}
	test, found := b.typing.CaseTest(id)
	if !found {
		return nil
	}
	return &thir.CaseTest{
		SubjectID: test.SubjectID, Case: test.Case, MatchesWhenTrue: test.MatchesWhenTrue,
		CaseCount: test.CaseCount, Family: test.Family,
	}
}

func projectPlace(base thir.Expr, projection thir.PlaceProjection, typ typeinfo.Type) *thir.Place {
	if base == nil {
		return nil
	}
	if existing := base.ExprPlace(); existing != nil {
		projected := &thir.Place{
			Root: existing.Root, Temporary: existing.Temporary, Type: typ,
			Projections: append([]thir.PlaceProjection(nil), existing.Projections...),
		}
		projected.Projections = append(projected.Projections, projection)
		return projected
	}
	return &thir.Place{Temporary: base, Projections: []thir.PlaceProjection{projection}, Type: typ}
}

func (b *thirBuilder) symbol(node ast.Node) *symbols.Symbol {
	if b.symbolIndex == nil || typednil.IsNil(node) {
		return nil
	}
	return b.symbolIndex.Symbol(node)
}

func (b *thirBuilder) scope(node ast.Node) *symbols.Scope {
	if b.symbolIndex == nil || typednil.IsNil(node) {
		return nil
	}
	return b.symbolIndex.Scope(node)
}

func sourceInfo(node ast.Node) ir.SourceInfo {
	if typednil.IsNil(node) {
		return ir.SourceInfo{}
	}
	return ir.SourceInfo{NodeID: node.ID(), Location: ast.LocOf(node)}
}

func stmtInfo(node ast.Node) thir.StmtInfo { return thir.StmtInfo{Source: sourceInfo(node)} }

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
