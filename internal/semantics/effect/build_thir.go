package effect

import (
	"fmt"

	"compiler/internal/frontend/ast"

	"compiler/internal/ir"
	"compiler/internal/ir/cfg"
	"compiler/internal/ir/thir"
	"compiler/internal/semantics/place"
	"compiler/internal/semantics/typeinfo"
)

// BuildTHIR publishes effects from canonical THIR and CFG. THIR owns source
// meaning and resolved symbols; this package owns how that meaning becomes the
// ordered operation stream consumed by later analyses.
func BuildTHIR(source *thir.Module, graphs *cfg.Module) Result {
	if source == nil || graphs == nil {
		return nil
	}
	result := make(Result, len(graphs.Functions))
	for _, graph := range graphs.Functions {
		if graph == nil {
			continue
		}
		function := source.Function(graph.NodeID)
		if function == nil {
			continue
		}
		builder := &thirBuilder{source: source, graph: graph, ops: make(SiteOps)}
		builder.buildFunction(function)
		result[graph.NodeID] = builder.ops
	}
	return result
}

type thirBuilder struct {
	source *thir.Module
	graph  *cfg.ControlFlowGraph
	ops    SiteOps
	site   cfg.SiteID
	use    typeinfo.UseKind
}

func (b *thirBuilder) emit(op Op) { b.ops[b.site] = append(b.ops[b.site], op) }

func (b *thirBuilder) buildFunction(function *thir.Function) {
	if b.graph.Entry == nil || len(b.graph.Entry.Sites) == 0 {
		return
	}
	b.site = b.graph.Entry.Sites[0].ID
	for _, parameter := range function.Params {
		if parameter.Symbol != nil {
			b.emit(Define{Symbol: parameter.Symbol, Node: nodeID(parameter.Source.NodeID), Initialized: true, OnEntry: true})
		}
	}
	for _, block := range b.graph.Blocks {
		if block == nil || !block.Reachable {
			continue
		}
		for _, site := range block.Sites {
			if site == nil {
				continue
			}
			b.site = site.ID
			b.buildSite(block, site)
		}
	}
}

func (b *thirBuilder) buildSite(block *cfg.Block, site *cfg.Site) {
	node := b.source.Node(site.NodeID)
	if statement, ok := node.(thir.Stmt); ok && statement != nil {
		statement.BuildEffects(b)
	}
	if site.Kind != cfg.SiteTerminator {
		return
	}
	switch terminator := block.Terminator.(type) {
	case *cfg.Branch:
		if condition, ok := b.source.Node(terminator.ConditionID).(thir.Expr); ok {
			b.expression(condition, typeinfo.UseRead)
		}
	case *cfg.SwitchVariant, *cfg.Jump, *cfg.Return:
	default:
		panic(fmt.Sprintf("effect: unhandled CFG terminator %T", block.Terminator))
	}
	if terminator, ok := block.Terminator.(*cfg.SwitchVariant); ok {
		b.buildMatchBindings(terminator)
	}
}

func (b *thirBuilder) buildMatchBindings(terminator *cfg.SwitchVariant) {
	match, _ := b.source.Node(terminator.NodeID).(*thir.Match)
	if match == nil {
		return
	}
	for _, edge := range b.graph.SiteEdges.OutEdges(b.site) {
		if edge.Kind != cfg.EdgeVariantCase {
			continue
		}
		for _, arm := range match.Arms {
			if arm.Case != edge.Case {
				continue
			}
			for _, binding := range arm.Bindings {
				if binding.Symbol != nil {
					b.ops[edge.To] = append(b.ops[edge.To], Define{
						Symbol: binding.Symbol, Node: nodeID(terminator.NodeID), Initialized: true, OnEntry: true,
					})
				}
			}
		}
	}
}

func (b *thirBuilder) BuildBlockEffects(*thir.Block)             {}
func (b *thirBuilder) BuildBreakEffects(*thir.Break)             {}
func (b *thirBuilder) BuildContinueEffects(*thir.Continue)       {}
func (b *thirBuilder) BuildInvalidStmtEffects(*thir.InvalidStmt) {}

func (b *thirBuilder) BuildBindingEffects(statement *thir.Binding) {
	b.expression(statement.Value, typeinfo.UseMove)
	if statement.Symbol != nil {
		value := astNodeID(statement.Value)
		b.emit(Define{Symbol: statement.Symbol, Node: nodeID(statement.Source.NodeID), Value: value, Initialized: statement.Value != nil})
	}
}

func (b *thirBuilder) BuildExprStmtEffects(statement *thir.ExprStmt) {
	b.expression(statement.Value, typeinfo.UseRead)
	if statement.Value != nil {
		b.emit(Discard{Place: b.place(statement.Value), Node: nodeID(statement.Value.SourceInfo().NodeID), Location: statement.Value.SourceInfo().Location})
	}
}

func (b *thirBuilder) BuildAssignEffects(statement *thir.Assign) {
	b.expression(statement.Value, typeinfo.UseMove)
	if statement.Target == nil {
		return
	}
	b.placeOperands(statement.Target)
	b.emit(Write{Place: b.place(statement.Target), Node: nodeID(statement.Target.SourceInfo().NodeID), Owner: nodeID(statement.Source.NodeID), Value: astNodeID(statement.Value), Location: statement.Target.SourceInfo().Location})
}

func (b *thirBuilder) BuildReturnEffects(statement *thir.Return) {
	b.expression(statement.Value, typeinfo.UseMove)
}
func (b *thirBuilder) BuildIfEffects(*thir.If) {}

func (b *thirBuilder) BuildForEffects(statement *thir.For) {
	if statement.Checked != nil || statement.Iterable == nil {
		return
	}
	b.expression(statement.Iterable, typeinfo.UseRead)
	if sequence, ok := statement.Iteration.(*thir.SequenceIteration); ok && sequence.Carrier != nil {
		b.emit(Iterate{Loop: nodeID(statement.Source.NodeID), Place: b.place(statement.Iterable), Node: nodeID(statement.Iterable.SourceInfo().NodeID), Carrier: sequence.Carrier, Location: statement.Iterable.SourceInfo().Location})
	}
}
func (b *thirBuilder) BuildMatchEffects(statement *thir.Match) {
	b.expression(statement.Subject, typeinfo.UseRead)
}

func (b *thirBuilder) BuildInvalidExprEffects(*thir.InvalidExpr)       {}
func (b *thirBuilder) BuildNumberLiteralEffects(*thir.NumberLiteral)   {}
func (b *thirBuilder) BuildStringLiteralEffects(*thir.StringLiteral)   {}
func (b *thirBuilder) BuildByteLiteralEffects(*thir.ByteLiteral)       {}
func (b *thirBuilder) BuildCharLiteralEffects(*thir.CharLiteral)       {}
func (b *thirBuilder) BuildBoolLiteralEffects(*thir.BoolLiteral)       {}
func (b *thirBuilder) BuildNoneLiteralEffects(*thir.NoneLiteral)       {}
func (b *thirBuilder) BuildQualifiedIdentEffects(*thir.QualifiedIdent) {}

func (b *thirBuilder) BuildIdentEffects(expr *thir.Ident) {
	if expr.Symbol != nil {
		b.emit(Use{Place: b.place(expr), Node: nodeID(expr.SourceInfo().NodeID), Location: expr.SourceInfo().Location, Kind: b.use})
	}
}

func (b *thirBuilder) BuildFieldEffects(expr *thir.Field) { b.project(expr, b.use) }

func (b *thirBuilder) BuildIndexEffects(expr *thir.Index) {
	if expr.Index != nil {
		if _, ranged := expr.Index.(*thir.Range); ranged {
			b.borrow(expr, expr.Base, expr.Index, b.mutableReference(expr), false)
			return
		}
	}
	b.project(expr, b.use)
}

func (b *thirBuilder) BuildRangeEffects(expr *thir.Range) {
	b.expression(expr.Start, typeinfo.UseRead)
	b.expression(expr.End, typeinfo.UseRead)
}
func (b *thirBuilder) BuildStructLiteralEffects(expr *thir.StructLiteral) {
	for _, field := range expr.Fields {
		b.expression(field.Value, typeinfo.UseMove)
	}
}
func (b *thirBuilder) BuildVariantEffects(expr *thir.Variant) {
	b.expression(expr.Payload, typeinfo.UseMove)
}
func (b *thirBuilder) BuildArrayLiteralEffects(expr *thir.ArrayLiteral) {
	for _, value := range expr.Values {
		b.expression(value, typeinfo.UseMove)
	}
}

func (b *thirBuilder) BuildAddressEffects(expr *thir.Address) {
	if expr.Mode == thir.AddressRaw {
		b.borrow(expr, expr.Value, nil, false, true)
		return
	}
	b.borrow(expr, expr.Value, nil, expr.Mode == thir.AddressMutable, false)
}

func (b *thirBuilder) BuildUnaryEffects(expr *thir.Unary) { b.expression(expr.Value, typeinfo.UseRead) }
func (b *thirBuilder) BuildBinaryEffects(expr *thir.Binary) {
	b.expression(expr.Left, ternaryUse(expr.StringConcat, typeinfo.UseMove, typeinfo.UseRead))
	b.expression(expr.Right, typeinfo.UseRead)
}
func (b *thirBuilder) BuildIsEffects(expr *thir.Is) { b.expression(expr.Value, typeinfo.UseRead) }

func (b *thirBuilder) BuildCallEffects(expr *thir.Call) {
	b.emit(CallBegin{Node: nodeID(expr.SourceInfo().NodeID), Location: expr.SourceInfo().Location})
	if field, method := expr.Callee.(*thir.Field); method {
		b.argument(field.Base)
	} else {
		b.expression(expr.Callee, typeinfo.UseRead)
	}
	for _, argument := range expr.Args {
		b.argument(argument)
	}
	b.emit(CallEnd{Node: nodeID(expr.SourceInfo().NodeID)})
}
func (b *thirBuilder) BuildFreeEffects(expr *thir.Free)   { b.expression(expr.Value, typeinfo.UseMove) }
func (b *thirBuilder) BuildPrintEffects(expr *thir.Print) { b.expression(expr.Value, typeinfo.UseRead) }
func (b *thirBuilder) BuildCastEffects(expr *thir.Cast)   { b.expression(expr.Value, typeinfo.UseMove) }

func (b *thirBuilder) expression(expr thir.Expr, kind typeinfo.UseKind) {
	if expr == nil {
		return
	}
	if use, hasUse := expr.UseKind(); hasUse {
		kind = use
	}
	previous := b.use
	b.use = kind
	expr.BuildEffects(b)
	b.use = previous
}

func (b *thirBuilder) argument(expr thir.Expr) {
	if expr == nil {
		return
	}
	if mutable, reference := expr.ReferenceArgInfo(); reference {
		operand := expr
		if address, ok := expr.(*thir.Address); ok {
			operand = address.Value
		}
		b.placeOperands(operand)
		b.emit(Borrow{Place: b.place(operand), Node: nodeID(expr.SourceInfo().NodeID), Operand: nodeID(operand.SourceInfo().NodeID), Location: expr.SourceInfo().Location, Mutable: mutable, Argument: true})
		return
	}
	use, _ := expr.UseKind()
	b.expression(expr, use)
}

func (b *thirBuilder) project(expr thir.Expr, kind typeinfo.UseKind) {
	b.placeOperands(expr)
	b.emit(Use{Place: b.place(expr), Node: nodeID(expr.SourceInfo().NodeID), Location: expr.SourceInfo().Location, Kind: kind})
}

func (b *thirBuilder) placeOperands(expr thir.Expr) {
	if expr == nil {
		return
	}
	switch node := expr.(type) {
	case *thir.Field:
		b.placeOperands(node.Base)
	case *thir.Index:
		b.placeOperands(node.Base)
		b.expression(node.Index, typeinfo.UseRead)
	default:
		if place := expr.ExprPlace(); place != nil && place.Temporary != nil {
			b.expression(place.Temporary, typeinfo.UseRead)
			return
		}
		if _, binding := expr.(*thir.Ident); !binding {
			b.expression(expr, typeinfo.UseRead)
		}
	}
}

func (b *thirBuilder) place(expr thir.Expr) Place {
	if expr == nil {
		return Place{}
	}
	if ident, ok := expr.(*thir.Ident); ok && ident.Symbol != nil {
		return Place{Root: ident.Symbol}
	}
	if qualified, ok := expr.(*thir.QualifiedIdent); ok && qualified.Symbol != nil {
		return Place{Root: qualified.Symbol}
	}
	if semantic := expr.ExprPlace(); semantic != nil {
		out := Place{Root: semantic.Root, Temporary: nodeIDExpr(semantic.Temporary), Projections: make([]place.OriginProjection, 0, len(semantic.Projections))}
		for _, projection := range semantic.Projections {
			out.Projections = append(out.Projections, originProjection(projection))
		}
		return out
	}
	return Place{Temporary: nodeID(expr.SourceInfo().NodeID)}
}

func (b *thirBuilder) borrow(expr thir.Expr, operand, bounds thir.Expr, mutable, raw bool) {
	b.placeOperands(operand)
	if bounds != nil {
		b.expression(bounds, typeinfo.UseRead)
	}
	b.emit(Borrow{Place: b.place(operand), Node: nodeID(expr.SourceInfo().NodeID), Operand: nodeID(operand.SourceInfo().NodeID), Location: expr.SourceInfo().Location, Mutable: mutable, Raw: raw})
}

func (b *thirBuilder) mutableReference(expr thir.Expr) bool {
	if expr == nil || expr.ExprType() == nil {
		return false
	}
	_, mutable, reference := typeinfo.ReferenceTarget(typeinfo.Underlying(expr.ExprType()))
	return reference && mutable
}

func ternaryUse(condition bool, yes, no typeinfo.UseKind) typeinfo.UseKind {
	if condition {
		return yes
	}
	return no
}
func nodeID(id ir.NodeID) ast.NodeID { return ast.NodeID(id) }
func astNodeID(expr thir.Expr) ast.NodeID {
	if expr == nil {
		return 0
	}
	return nodeID(expr.SourceInfo().NodeID)
}
func nodeIDExpr(expr thir.Expr) ast.NodeID { return astNodeID(expr) }
func originProjection(projection thir.PlaceProjection) place.OriginProjection {
	if projection.Kind == thir.PlaceField {
		return place.OriginProjection{Kind: place.OriginField, Field: projection.Name}
	}
	if projection.ConstantIndex != nil {
		return place.OriginProjection{Kind: place.OriginIndex, Index: projection.ConstantIndex.Text}
	}
	return place.OriginProjection{Kind: place.OriginIndex}
}
