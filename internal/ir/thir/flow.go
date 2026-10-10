package thir

// AnalyzeFlow dispatches closed THIR node kinds to flow-owned semantics. These
// methods intentionally contain no policy; their interface requirement makes a
// new node fail compilation until flow analysis handles it explicitly.
func (s *Block) AnalyzeFlow(analyzer FlowAnalyzer)       { analyzer.AnalyzeBlock(s) }
func (s *Binding) AnalyzeFlow(analyzer FlowAnalyzer)     { analyzer.AnalyzeBinding(s) }
func (s *Destructure) AnalyzeFlow(analyzer FlowAnalyzer) { analyzer.AnalyzeDestructure(s) }
func (s *ExprStmt) AnalyzeFlow(analyzer FlowAnalyzer)    { analyzer.AnalyzeExprStmt(s) }
func (s *Assign) AnalyzeFlow(analyzer FlowAnalyzer)      { analyzer.AnalyzeAssign(s) }
func (s *Return) AnalyzeFlow(analyzer FlowAnalyzer)      { analyzer.AnalyzeReturn(s) }
func (s *If) AnalyzeFlow(analyzer FlowAnalyzer)          { analyzer.AnalyzeIf(s) }
func (s *For) AnalyzeFlow(analyzer FlowAnalyzer)         { analyzer.AnalyzeFor(s) }
func (s *Break) AnalyzeFlow(analyzer FlowAnalyzer)       { analyzer.AnalyzeBreak(s) }
func (s *Continue) AnalyzeFlow(analyzer FlowAnalyzer)    { analyzer.AnalyzeContinue(s) }
func (s *Match) AnalyzeFlow(analyzer FlowAnalyzer)       { analyzer.AnalyzeMatch(s) }
func (s *InvalidStmt) AnalyzeFlow(analyzer FlowAnalyzer) { analyzer.AnalyzeInvalidStmt(s) }

func (e *InvalidExpr) AnalyzeFlow(analyzer FlowAnalyzer)    { analyzer.AnalyzeInvalidExpr(e) }
func (e *NumberLiteral) AnalyzeFlow(analyzer FlowAnalyzer)  { analyzer.AnalyzeNumberLiteral(e) }
func (e *StringLiteral) AnalyzeFlow(analyzer FlowAnalyzer)  { analyzer.AnalyzeStringLiteral(e) }
func (e *ByteLiteral) AnalyzeFlow(analyzer FlowAnalyzer)    { analyzer.AnalyzeByteLiteral(e) }
func (e *CharLiteral) AnalyzeFlow(analyzer FlowAnalyzer)    { analyzer.AnalyzeCharLiteral(e) }
func (e *BoolLiteral) AnalyzeFlow(analyzer FlowAnalyzer)    { analyzer.AnalyzeBoolLiteral(e) }
func (e *NoneLiteral) AnalyzeFlow(analyzer FlowAnalyzer)    { analyzer.AnalyzeNoneLiteral(e) }
func (e *Ident) AnalyzeFlow(analyzer FlowAnalyzer)          { analyzer.AnalyzeIdent(e) }
func (e *QualifiedIdent) AnalyzeFlow(analyzer FlowAnalyzer) { analyzer.AnalyzeQualifiedIdent(e) }
func (e *Field) AnalyzeFlow(analyzer FlowAnalyzer)          { analyzer.AnalyzeField(e) }
func (e *Index) AnalyzeFlow(analyzer FlowAnalyzer)          { analyzer.AnalyzeIndex(e) }
func (e *Range) AnalyzeFlow(analyzer FlowAnalyzer)          { analyzer.AnalyzeRange(e) }
func (e *StructLiteral) AnalyzeFlow(analyzer FlowAnalyzer)  { analyzer.AnalyzeStructLiteral(e) }
func (e *Variant) AnalyzeFlow(analyzer FlowAnalyzer)        { analyzer.AnalyzeVariant(e) }
func (e *ArrayLiteral) AnalyzeFlow(analyzer FlowAnalyzer)   { analyzer.AnalyzeArrayLiteral(e) }
func (e *Address) AnalyzeFlow(analyzer FlowAnalyzer)        { analyzer.AnalyzeAddress(e) }
func (e *Unary) AnalyzeFlow(analyzer FlowAnalyzer)          { analyzer.AnalyzeUnary(e) }
func (e *Binary) AnalyzeFlow(analyzer FlowAnalyzer)         { analyzer.AnalyzeBinary(e) }
func (e *Is) AnalyzeFlow(analyzer FlowAnalyzer)             { analyzer.AnalyzeIs(e) }
func (e *Call) AnalyzeFlow(analyzer FlowAnalyzer)           { analyzer.AnalyzeCall(e) }
func (e *Free) AnalyzeFlow(analyzer FlowAnalyzer)           { analyzer.AnalyzeFree(e) }
func (e *Print) AnalyzeFlow(analyzer FlowAnalyzer)          { analyzer.AnalyzePrint(e) }
func (e *Cast) AnalyzeFlow(analyzer FlowAnalyzer)           { analyzer.AnalyzeCast(e) }
