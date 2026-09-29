package thir

// BuildEffects dispatches closed THIR nodes to effect-owned semantics. The
// dispatch carries no policy; adding a THIR node requires an explicit effect
// implementation before the compiler can build effects.
func (s *Block) BuildEffects(builder EffectBuilder)       { builder.BuildBlockEffects(s) }
func (s *Binding) BuildEffects(builder EffectBuilder)     { builder.BuildBindingEffects(s) }
func (s *ExprStmt) BuildEffects(builder EffectBuilder)    { builder.BuildExprStmtEffects(s) }
func (s *Assign) BuildEffects(builder EffectBuilder)      { builder.BuildAssignEffects(s) }
func (s *Return) BuildEffects(builder EffectBuilder)      { builder.BuildReturnEffects(s) }
func (s *If) BuildEffects(builder EffectBuilder)          { builder.BuildIfEffects(s) }
func (s *For) BuildEffects(builder EffectBuilder)         { builder.BuildForEffects(s) }
func (s *Break) BuildEffects(builder EffectBuilder)       { builder.BuildBreakEffects(s) }
func (s *Continue) BuildEffects(builder EffectBuilder)    { builder.BuildContinueEffects(s) }
func (s *Match) BuildEffects(builder EffectBuilder)       { builder.BuildMatchEffects(s) }
func (s *InvalidStmt) BuildEffects(builder EffectBuilder) { builder.BuildInvalidStmtEffects(s) }

func (e *InvalidExpr) BuildEffects(builder EffectBuilder)    { builder.BuildInvalidExprEffects(e) }
func (e *NumberLiteral) BuildEffects(builder EffectBuilder)  { builder.BuildNumberLiteralEffects(e) }
func (e *StringLiteral) BuildEffects(builder EffectBuilder)  { builder.BuildStringLiteralEffects(e) }
func (e *ByteLiteral) BuildEffects(builder EffectBuilder)    { builder.BuildByteLiteralEffects(e) }
func (e *CharLiteral) BuildEffects(builder EffectBuilder)    { builder.BuildCharLiteralEffects(e) }
func (e *BoolLiteral) BuildEffects(builder EffectBuilder)    { builder.BuildBoolLiteralEffects(e) }
func (e *NoneLiteral) BuildEffects(builder EffectBuilder)    { builder.BuildNoneLiteralEffects(e) }
func (e *Ident) BuildEffects(builder EffectBuilder)          { builder.BuildIdentEffects(e) }
func (e *QualifiedIdent) BuildEffects(builder EffectBuilder) { builder.BuildQualifiedIdentEffects(e) }
func (e *Field) BuildEffects(builder EffectBuilder)          { builder.BuildFieldEffects(e) }
func (e *Index) BuildEffects(builder EffectBuilder)          { builder.BuildIndexEffects(e) }
func (e *Range) BuildEffects(builder EffectBuilder)          { builder.BuildRangeEffects(e) }
func (e *StructLiteral) BuildEffects(builder EffectBuilder)  { builder.BuildStructLiteralEffects(e) }
func (e *Variant) BuildEffects(builder EffectBuilder)        { builder.BuildVariantEffects(e) }
func (e *ArrayLiteral) BuildEffects(builder EffectBuilder)   { builder.BuildArrayLiteralEffects(e) }
func (e *Address) BuildEffects(builder EffectBuilder)        { builder.BuildAddressEffects(e) }
func (e *Unary) BuildEffects(builder EffectBuilder)          { builder.BuildUnaryEffects(e) }
func (e *Binary) BuildEffects(builder EffectBuilder)         { builder.BuildBinaryEffects(e) }
func (e *Is) BuildEffects(builder EffectBuilder)             { builder.BuildIsEffects(e) }
func (e *Call) BuildEffects(builder EffectBuilder)           { builder.BuildCallEffects(e) }
func (e *Free) BuildEffects(builder EffectBuilder)           { builder.BuildFreeEffects(e) }
func (e *Print) BuildEffects(builder EffectBuilder)          { builder.BuildPrintEffects(e) }
func (e *Cast) BuildEffects(builder EffectBuilder)           { builder.BuildCastEffects(e) }
