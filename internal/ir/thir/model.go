// Package thir defines the canonical typed source representation consumed after
// base typechecking. THIR retains source structure where later analyses need it,
// but replaces semantic side-table joins with direct types, symbols, and plans.
package thir

import (
	"compiler/internal/ir"
	"compiler/internal/semantics/intrinsics"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
)

// Module contains every callable declaration in one typed source module.
type Module struct {
	Name      string
	FilePath  string
	Functions []*Function
	byNodeID  map[ir.NodeID]Node
}

// Node returns typed source node with source identity id.
func (m *Module) Node(id ir.NodeID) Node {
	if m == nil || id == 0 {
		return nil
	}
	return m.byNodeID[id]
}

// Function returns function declaration with source identity id.
func (m *Module) Function(id ir.NodeID) *Function {
	if m == nil || id == 0 {
		return nil
	}
	for _, function := range m.Functions {
		if function != nil && function.Source.NodeID == id {
			return function
		}
	}
	return nil
}

// Function retains semantic signature and lexical body. A nil Body denotes an
// external declaration, not an incomplete function.
type Function struct {
	Name           string
	Symbol         *symbols.Symbol
	Params         []Param
	ReturnType     typeinfo.Type
	ReturnTypeText string
	ReturnsValue   bool
	Body           *Block
	Source         ir.SourceInfo
}

type Param struct {
	Symbol *symbols.Symbol
	Type   typeinfo.Type
	Source ir.SourceInfo
}

// Node is sealed so every THIR kind must declare traversal and validation
// locally before it can enter the canonical tree.
type Node interface {
	thirNode()
	SourceInfo() ir.SourceInfo
	forEachChild(func(Node))
	validateSelf() error
}

// ControlFlowBuilder is implemented by CFG construction. Statement-owned
// dispatch keeps that operation exhaustive without moving topology policy into
// THIR or requiring a central concrete-statement switch.
type ControlFlowBuilder interface {
	BuildBlock(*Block)
	BuildBinding(*Binding)
	BuildExprStmt(*ExprStmt)
	BuildAssign(*Assign)
	BuildReturn(*Return)
	BuildIf(*If)
	BuildFor(*For)
	BuildBreak(*Break)
	BuildContinue(*Continue)
	BuildMatch(*Match)
	BuildInvalidStmt(*InvalidStmt)
}

// FlowAnalyzer is implemented by flow typing. Node-owned dispatch makes every
// THIR statement and expression an explicit part of flow semantics while the
// analysis package retains all refinement and transfer policy.
type EffectBuilder interface {
	BuildBlockEffects(*Block)
	BuildBindingEffects(*Binding)
	BuildExprStmtEffects(*ExprStmt)
	BuildAssignEffects(*Assign)
	BuildReturnEffects(*Return)
	BuildIfEffects(*If)
	BuildForEffects(*For)
	BuildBreakEffects(*Break)
	BuildContinueEffects(*Continue)
	BuildMatchEffects(*Match)
	BuildInvalidStmtEffects(*InvalidStmt)

	BuildInvalidExprEffects(*InvalidExpr)
	BuildNumberLiteralEffects(*NumberLiteral)
	BuildStringLiteralEffects(*StringLiteral)
	BuildByteLiteralEffects(*ByteLiteral)
	BuildCharLiteralEffects(*CharLiteral)
	BuildBoolLiteralEffects(*BoolLiteral)
	BuildNoneLiteralEffects(*NoneLiteral)
	BuildIdentEffects(*Ident)
	BuildQualifiedIdentEffects(*QualifiedIdent)
	BuildFieldEffects(*Field)
	BuildIndexEffects(*Index)
	BuildRangeEffects(*Range)
	BuildStructLiteralEffects(*StructLiteral)
	BuildVariantEffects(*Variant)
	BuildArrayLiteralEffects(*ArrayLiteral)
	BuildAddressEffects(*Address)
	BuildUnaryEffects(*Unary)
	BuildBinaryEffects(*Binary)
	BuildIsEffects(*Is)
	BuildCallEffects(*Call)
	BuildFreeEffects(*Free)
	BuildPrintEffects(*Print)
	BuildCastEffects(*Cast)
}

type FlowAnalyzer interface {
	AnalyzeBlock(*Block)
	AnalyzeBinding(*Binding)
	AnalyzeExprStmt(*ExprStmt)
	AnalyzeAssign(*Assign)
	AnalyzeReturn(*Return)
	AnalyzeIf(*If)
	AnalyzeFor(*For)
	AnalyzeBreak(*Break)
	AnalyzeContinue(*Continue)
	AnalyzeMatch(*Match)
	AnalyzeInvalidStmt(*InvalidStmt)

	AnalyzeInvalidExpr(*InvalidExpr) typeinfo.Type
	AnalyzeNumberLiteral(*NumberLiteral) typeinfo.Type
	AnalyzeStringLiteral(*StringLiteral) typeinfo.Type
	AnalyzeByteLiteral(*ByteLiteral) typeinfo.Type
	AnalyzeCharLiteral(*CharLiteral) typeinfo.Type
	AnalyzeBoolLiteral(*BoolLiteral) typeinfo.Type
	AnalyzeNoneLiteral(*NoneLiteral) typeinfo.Type
	AnalyzeIdent(*Ident) typeinfo.Type
	AnalyzeQualifiedIdent(*QualifiedIdent) typeinfo.Type
	AnalyzeField(*Field) typeinfo.Type
	AnalyzeIndex(*Index) typeinfo.Type
	AnalyzeRange(*Range) typeinfo.Type
	AnalyzeStructLiteral(*StructLiteral) typeinfo.Type
	AnalyzeVariant(*Variant) typeinfo.Type
	AnalyzeArrayLiteral(*ArrayLiteral) typeinfo.Type
	AnalyzeAddress(*Address) typeinfo.Type
	AnalyzeUnary(*Unary) typeinfo.Type
	AnalyzeBinary(*Binary) typeinfo.Type
	AnalyzeIs(*Is) typeinfo.Type
	AnalyzeCall(*Call) typeinfo.Type
	AnalyzeFree(*Free) typeinfo.Type
	AnalyzePrint(*Print) typeinfo.Type
	AnalyzeCast(*Cast) typeinfo.Type
}

type Stmt interface {
	Node
	stmtNode()
	BuildControlFlow(ControlFlowBuilder)
	AnalyzeFlow(FlowAnalyzer)
	BuildEffects(EffectBuilder)
}

type Expr interface {
	Node
	exprNode()
	ExprType() typeinfo.Type
	ExprPlace() *Place
	Conversion() *typeinfo.Conversion
	ImplicitReferenceType() typeinfo.Type
	InterfaceImplementations() []InterfaceImplementation
	UseKind() (typeinfo.UseKind, bool)
	ReferenceArgInfo() (bool, bool)
	LowerExpression(ExpressionLowerer) ir.Expr
	AnalyzeFlow(FlowAnalyzer) typeinfo.Type
	BuildEffects(EffectBuilder)
}

type StmtInfo struct {
	Source ir.SourceInfo
}

func (s StmtInfo) thirNode()                 {}
func (s StmtInfo) stmtNode()                 {}
func (s StmtInfo) SourceInfo() ir.SourceInfo { return s.Source }

type ExprInfo struct {
	Source                   ir.SourceInfo
	Type                     typeinfo.Type
	conversion               *typeinfo.Conversion
	Use                      typeinfo.UseKind
	HasUse                   bool
	ReferenceArgument        bool
	ReferenceArgumentMutable bool
	ImplicitReference        typeinfo.Type
	interfaceImplementations []InterfaceImplementation
	Place                    *Place
}

func (e ExprInfo) thirNode()                            {}
func (e ExprInfo) exprNode()                            {}
func (e ExprInfo) SourceInfo() ir.SourceInfo            { return e.Source }
func (e ExprInfo) ExprType() typeinfo.Type              { return e.Type }
func (e ExprInfo) ExprPlace() *Place                    { return e.Place }
func (e ExprInfo) Conversion() *typeinfo.Conversion     { return e.conversion }
func (e ExprInfo) ImplicitReferenceType() typeinfo.Type { return e.ImplicitReference }
func (e ExprInfo) InterfaceImplementations() []InterfaceImplementation {
	return e.interfaceImplementations
}
func (e ExprInfo) UseKind() (typeinfo.UseKind, bool) {
	return e.Use, e.HasUse
}
func (e ExprInfo) ReferenceArgInfo() (bool, bool) {
	return e.ReferenceArgumentMutable, e.ReferenceArgument
}

type InterfaceImplementation struct {
	Symbol       *symbols.Symbol
	CallableType *typeinfo.FuncType
}

// Place names addressable storage without retaining syntax. Temporary is set
// only when a projection starts from a computed value rather than a symbol.
type Place struct {
	Root        *symbols.Symbol
	Temporary   Expr
	Projections []PlaceProjection
	Type        typeinfo.Type
}

type PlaceProjectionKind uint8

const (
	PlaceField PlaceProjectionKind = iota
	PlaceIndex
)

type PlaceProjection struct {
	Source          ir.SourceInfo
	BaseSource      ir.SourceInfo
	Kind            PlaceProjectionKind
	Name            string
	Field           int
	Index           Expr
	ConstantIndex   *ConstantIndex
	DereferenceType typeinfo.Type
	Type            typeinfo.Type
}

type ConstantIndex struct {
	Text string
	Type typeinfo.Type
}

type Block struct {
	StmtInfo
	Scope *symbols.Scope
	Stmts []Stmt
}

type Binding struct {
	StmtInfo
	Symbol   *symbols.Symbol
	Constant bool
	Inferred bool
	Value    Expr
}

type ExprStmt struct {
	StmtInfo
	Value Expr
}

type Assign struct {
	StmtInfo
	Target Expr
	Value  Expr
}

type Return struct {
	StmtInfo
	Value Expr
}

type If struct {
	StmtInfo
	Condition Expr
	Then      *Block
	Else      Stmt
}

type For struct {
	StmtInfo
	Index     *symbols.Symbol
	Value     *symbols.Symbol
	Iterable  Expr
	Condition Expr
	Body      *Block
	Iteration IterationPlan
	Checked   *Block
}

type Break struct{ StmtInfo }
type Continue struct{ StmtInfo }

type Match struct {
	StmtInfo
	Subject   Expr
	EnumType  typeinfo.Type
	CaseCount int
	Arms      []MatchArm
}

type MatchArm struct {
	Source     ir.SourceInfo
	Case       int
	Payload    typeinfo.Type
	CarrierUse typeinfo.UseKind
	Bindings   []MatchBinding
	Body       *Block
}

type MatchProjection uint8

const (
	MatchPayloadField MatchProjection = iota
	MatchWholePayload
)

type MatchBinding struct {
	Projection MatchProjection
	Field      int
	Type       typeinfo.Type
	Symbol     *symbols.Symbol
	Discard    bool
}

type InvalidStmt struct {
	StmtInfo
	Message string
}

// IterationPlan is closed: later phases can exhaustively distinguish built-in
// range and sequence iteration without consulting typechecker evidence.
type IterationPlan interface {
	iterationPlan()
	IsGuaranteedEntry() bool
}

type RangeIteration struct {
	ElementType     typeinfo.Type
	Cursor          *symbols.Symbol
	Limit           *symbols.Symbol
	Ordinal         *symbols.Symbol
	GuaranteedEntry bool
}

func (*RangeIteration) iterationPlan() {}
func (p *RangeIteration) IsGuaranteedEntry() bool {
	return p != nil && p.GuaranteedEntry
}

type SequenceIteration struct {
	ElementType     typeinfo.Type
	Cursor          *symbols.Symbol
	Value           *symbols.Symbol
	Index           *symbols.Symbol
	Carrier         *symbols.Symbol
	CarrierType     typeinfo.Type
	GuaranteedEntry bool
}

func (*SequenceIteration) iterationPlan() {}
func (p *SequenceIteration) IsGuaranteedEntry() bool {
	return p != nil && p.GuaranteedEntry
}

type InvalidExpr struct {
	ExprInfo
	Message string
}

type NumberLiteral struct {
	ExprInfo
	Value        string
	ExplicitType string
}

type StringLiteral struct {
	ExprInfo
	Value   string
	CString bool
}

type ByteLiteral struct {
	ExprInfo
	Value string
}

type CharLiteral struct {
	ExprInfo
	Value string
}

type BoolLiteral struct {
	ExprInfo
	Value bool
}

type NoneLiteral struct{ ExprInfo }

type Ident struct {
	ExprInfo
	Name   string
	Symbol *symbols.Symbol
}

type QualifiedIdent struct {
	ExprInfo
	Name   string
	Symbol *symbols.Symbol
}

type Field struct {
	ExprInfo
	Base   Expr
	Name   string
	Symbol *symbols.Symbol
	Access *FieldAccess
}

type FieldAccess struct {
	Field           int
	DereferenceType typeinfo.Type
}

type Index struct {
	ExprInfo
	Base     Expr
	Index    Expr
	Constant *ConstantIndex
}

type Range struct {
	ExprInfo
	Start        Expr
	End          Expr
	EndExclusive bool
}

type StructLiteral struct {
	ExprInfo
	Fields []StructField
}

type StructField struct {
	Name  string
	Index int
	Value Expr
}

type Variant struct {
	ExprInfo
	Case        int
	Payload     Expr
	PayloadType typeinfo.Type
}

type ArrayLiteral struct {
	ExprInfo
	Values []Expr
}

type AddressMode uint8

const (
	AddressRaw AddressMode = iota
	AddressShared
	AddressMutable
)

type Address struct {
	ExprInfo
	Mode  AddressMode
	Value Expr
}

type Unary struct {
	ExprInfo
	Op    string
	Value Expr
}

type Binary struct {
	ExprInfo
	Left         Expr
	Op           string
	Right        Expr
	StringConcat bool
	Test         *CaseTest
}

type Is struct {
	ExprInfo
	Value Expr
	Test  *CaseTest
}

type CaseTest struct {
	SubjectID    ir.NodeID
	Case         int
	CaseWhenTrue bool
	CaseCount    int
	Family       typeinfo.VariantFamily
}

type Call struct {
	ExprInfo
	Callee       Expr
	Args         []Expr
	Piped        bool
	CompilerCall *CompilerCall
}

type CompilerCall struct {
	Operation symbols.CompilerOp
	Kind      intrinsics.FunctionKind
}

type Free struct {
	ExprInfo
	Value Expr
}

type Print struct {
	ExprInfo
	Value   Expr
	Newline bool
}

type Cast struct {
	ExprInfo
	Value      Expr
	TargetType typeinfo.Type
}
