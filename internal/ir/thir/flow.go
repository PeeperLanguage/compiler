package thir

import "compiler/internal/semantics/typeinfo"

// AnalyzeFlow dispatches closed THIR node kinds to flow-owned semantics. These
// methods intentionally contain no policy; their interface requirement makes a
// new node fail compilation until flow analysis handles it explicitly.
func (s *Block) AnalyzeFlow(analyzer FlowAnalyzer)       { analyzer.AnalyzeBlock(s) }
func (s *Binding) AnalyzeFlow(analyzer FlowAnalyzer)     { analyzer.AnalyzeBinding(s) }
func (s *ExprStmt) AnalyzeFlow(analyzer FlowAnalyzer)    { analyzer.AnalyzeExprStmt(s) }
func (s *Assign) AnalyzeFlow(analyzer FlowAnalyzer)      { analyzer.AnalyzeAssign(s) }
func (s *Return) AnalyzeFlow(analyzer FlowAnalyzer)      { analyzer.AnalyzeReturn(s) }
func (s *If) AnalyzeFlow(analyzer FlowAnalyzer)          { analyzer.AnalyzeIf(s) }
func (s *For) AnalyzeFlow(analyzer FlowAnalyzer)         { analyzer.AnalyzeFor(s) }
func (s *Break) AnalyzeFlow(analyzer FlowAnalyzer)       { analyzer.AnalyzeBreak(s) }
func (s *Continue) AnalyzeFlow(analyzer FlowAnalyzer)    { analyzer.AnalyzeContinue(s) }
func (s *Match) AnalyzeFlow(analyzer FlowAnalyzer)       { analyzer.AnalyzeMatch(s) }
func (s *InvalidStmt) AnalyzeFlow(analyzer FlowAnalyzer) { analyzer.AnalyzeInvalidStmt(s) }

func (e *InvalidExpr) AnalyzeFlow(analyzer FlowAnalyzer) typeinfo.Type {
	return analyzer.AnalyzeInvalidExpr(e)
}
func (e *NumberLiteral) AnalyzeFlow(analyzer FlowAnalyzer) typeinfo.Type {
	return analyzer.AnalyzeNumberLiteral(e)
}
func (e *StringLiteral) AnalyzeFlow(analyzer FlowAnalyzer) typeinfo.Type {
	return analyzer.AnalyzeStringLiteral(e)
}
func (e *ByteLiteral) AnalyzeFlow(analyzer FlowAnalyzer) typeinfo.Type {
	return analyzer.AnalyzeByteLiteral(e)
}
func (e *CharLiteral) AnalyzeFlow(analyzer FlowAnalyzer) typeinfo.Type {
	return analyzer.AnalyzeCharLiteral(e)
}
func (e *BoolLiteral) AnalyzeFlow(analyzer FlowAnalyzer) typeinfo.Type {
	return analyzer.AnalyzeBoolLiteral(e)
}
func (e *NoneLiteral) AnalyzeFlow(analyzer FlowAnalyzer) typeinfo.Type {
	return analyzer.AnalyzeNoneLiteral(e)
}
func (e *Ident) AnalyzeFlow(analyzer FlowAnalyzer) typeinfo.Type {
	return analyzer.AnalyzeIdent(e)
}
func (e *QualifiedIdent) AnalyzeFlow(analyzer FlowAnalyzer) typeinfo.Type {
	return analyzer.AnalyzeQualifiedIdent(e)
}
func (e *Field) AnalyzeFlow(analyzer FlowAnalyzer) typeinfo.Type {
	return analyzer.AnalyzeField(e)
}
func (e *Index) AnalyzeFlow(analyzer FlowAnalyzer) typeinfo.Type {
	return analyzer.AnalyzeIndex(e)
}
func (e *Range) AnalyzeFlow(analyzer FlowAnalyzer) typeinfo.Type {
	return analyzer.AnalyzeRange(e)
}
func (e *StructLiteral) AnalyzeFlow(analyzer FlowAnalyzer) typeinfo.Type {
	return analyzer.AnalyzeStructLiteral(e)
}
func (e *Variant) AnalyzeFlow(analyzer FlowAnalyzer) typeinfo.Type {
	return analyzer.AnalyzeVariant(e)
}
func (e *ArrayLiteral) AnalyzeFlow(analyzer FlowAnalyzer) typeinfo.Type {
	return analyzer.AnalyzeArrayLiteral(e)
}
func (e *Address) AnalyzeFlow(analyzer FlowAnalyzer) typeinfo.Type {
	return analyzer.AnalyzeAddress(e)
}
func (e *Unary) AnalyzeFlow(analyzer FlowAnalyzer) typeinfo.Type {
	return analyzer.AnalyzeUnary(e)
}
func (e *Binary) AnalyzeFlow(analyzer FlowAnalyzer) typeinfo.Type {
	return analyzer.AnalyzeBinary(e)
}
func (e *Is) AnalyzeFlow(analyzer FlowAnalyzer) typeinfo.Type {
	return analyzer.AnalyzeIs(e)
}
func (e *Call) AnalyzeFlow(analyzer FlowAnalyzer) typeinfo.Type {
	return analyzer.AnalyzeCall(e)
}
func (e *Free) AnalyzeFlow(analyzer FlowAnalyzer) typeinfo.Type {
	return analyzer.AnalyzeFree(e)
}
func (e *Print) AnalyzeFlow(analyzer FlowAnalyzer) typeinfo.Type {
	return analyzer.AnalyzePrint(e)
}
func (e *Cast) AnalyzeFlow(analyzer FlowAnalyzer) typeinfo.Type {
	return analyzer.AnalyzeCast(e)
}
