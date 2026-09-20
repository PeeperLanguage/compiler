package thir

// BuildControlFlow dispatches each closed statement kind to CFG-owned topology
// logic. This method is intentionally required by Stmt: adding a statement
// cannot silently omit control-flow behavior.
func (s *Block) BuildControlFlow(builder ControlFlowBuilder)       { builder.BuildBlock(s) }
func (s *Binding) BuildControlFlow(builder ControlFlowBuilder)     { builder.BuildBinding(s) }
func (s *ExprStmt) BuildControlFlow(builder ControlFlowBuilder)    { builder.BuildExprStmt(s) }
func (s *Assign) BuildControlFlow(builder ControlFlowBuilder)      { builder.BuildAssign(s) }
func (s *Return) BuildControlFlow(builder ControlFlowBuilder)      { builder.BuildReturn(s) }
func (s *If) BuildControlFlow(builder ControlFlowBuilder)          { builder.BuildIf(s) }
func (s *For) BuildControlFlow(builder ControlFlowBuilder)         { builder.BuildFor(s) }
func (s *Break) BuildControlFlow(builder ControlFlowBuilder)       { builder.BuildBreak(s) }
func (s *Continue) BuildControlFlow(builder ControlFlowBuilder)    { builder.BuildContinue(s) }
func (s *Match) BuildControlFlow(builder ControlFlowBuilder)       { builder.BuildMatch(s) }
func (s *InvalidStmt) BuildControlFlow(builder ControlFlowBuilder) { builder.BuildInvalidStmt(s) }
