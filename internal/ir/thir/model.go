// Package thir defines the canonical typed source representation consumed after
// base typechecking. THIR retains source structure where later analyses need it,
// but replaces semantic side-table joins with direct types, symbols, and plans.
package thir

import (
	"compiler/internal/diagnostics"
	"compiler/internal/ir"
	"compiler/internal/moduleid"
	"compiler/internal/semantics/intrinsics"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
	"compiler/internal/source"
)

// Module contains every callable declaration in one typed source module.
type Module struct {
	Name          string
	FilePath      string
	Functions     []*Function
	byNodeID      map[source.NodeID]Node
	functionIndex map[moduleid.FunctionID]int
}

// NewModule publishes a complete typed module and builds its private lookup
// indexes. Callers provide semantic functions; THIR owns indexing details.
func NewModule(name, filePath string, functions []*Function) *Module {
	m := &Module{
		Name:          name,
		FilePath:      filePath,
		Functions:     append([]*Function(nil), functions...),
		byNodeID:      make(map[source.NodeID]Node),
		functionIndex: make(map[moduleid.FunctionID]int),
	}
	for index, function := range m.Functions {
		if function == nil {
			continue
		}
		if function.Identity != "" {
			m.functionIndex[function.Identity] = index
		}
		if function.Body == nil {
			continue
		}
		Inspect(function.Body, func(node Node) bool {
			if id := node.SourceInfo().NodeID; id.IsValid() {
				m.byNodeID[id] = node
			}
			return true
		})
	}
	return m
}

// Node returns typed source node with source identity id.
func (m *Module) Node(id source.NodeID) Node {
	if m == nil || !id.IsValid() {
		return nil
	}
	return m.byNodeID[id]
}

// Function returns function declaration with source identity id.
func (m *Module) Function(id source.NodeID) *Function {
	if m == nil || !id.IsValid() {
		return nil
	}
	for _, function := range m.Functions {
		if function != nil && function.Source.NodeID == id {
			return function
		}
	}
	return nil
}

// FunctionByID returns one declaration by stable module/function identity.
func (m *Module) FunctionByID(id moduleid.FunctionID) *Function {
	if m == nil || id == "" {
		return nil
	}
	index, found := m.functionIndex[id]
	if !found || index < 0 || index >= len(m.Functions) {
		return nil
	}
	function := m.Functions[index]
	if function == nil || function.Identity != id {
		return nil
	}
	return function
}

// Function retains semantic signature and lexical body. A nil Body denotes an
// external declaration, not an incomplete function.
type Function struct {
	Identity              moduleid.FunctionID
	Name                  string
	Symbol                *symbols.Symbol
	IsEntrypointShape     bool
	Params                []Param
	ReturnType            typeinfo.Type
	ReturnOriginsLocation *source.Location
	ReturnTypeText        string
	HasReturnValue        bool
	Body                  *Block
	Source                ir.SourceInfo
}

type Param struct {
	Symbol *symbols.Symbol
	Source ir.SourceInfo
}

// Type is the parameter's semantic type, held by its symbol.
func (p Param) Type() typeinfo.Type {
	if p.Symbol == nil {
		return nil
	}
	return p.Symbol.Type
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
	BuildDestructure(*Destructure)
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
	BuildDestructureEffects(*Destructure)
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
	AnalyzeDestructure(*Destructure)
	AnalyzeExprStmt(*ExprStmt)
	AnalyzeAssign(*Assign)
	AnalyzeReturn(*Return)
	AnalyzeIf(*If)
	AnalyzeFor(*For)
	AnalyzeBreak(*Break)
	AnalyzeContinue(*Continue)
	AnalyzeMatch(*Match)
	AnalyzeInvalidStmt(*InvalidStmt)

	AnalyzeInvalidExpr(*InvalidExpr)
	AnalyzeNumberLiteral(*NumberLiteral)
	AnalyzeStringLiteral(*StringLiteral)
	AnalyzeByteLiteral(*ByteLiteral)
	AnalyzeCharLiteral(*CharLiteral)
	AnalyzeBoolLiteral(*BoolLiteral)
	AnalyzeNoneLiteral(*NoneLiteral)
	AnalyzeIdent(*Ident)
	AnalyzeQualifiedIdent(*QualifiedIdent)
	AnalyzeField(*Field)
	AnalyzeIndex(*Index)
	AnalyzeRange(*Range)
	AnalyzeStructLiteral(*StructLiteral)
	AnalyzeVariant(*Variant)
	AnalyzeArrayLiteral(*ArrayLiteral)
	AnalyzeAddress(*Address)
	AnalyzeUnary(*Unary)
	AnalyzeBinary(*Binary)
	AnalyzeIs(*Is)
	AnalyzeCall(*Call)
	AnalyzeFree(*Free)
	AnalyzePrint(*Print)
	AnalyzeCast(*Cast)
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
	CaseCarrierType() typeinfo.Type
	PayloadPath() []int
	Conversion() *typeinfo.Conversion
	ImplicitReferenceType() typeinfo.Type
	InterfaceImplementations() []InterfaceImplementation
	UseKind() (typeinfo.UseKind, bool)
	ReferenceArgInfo() (bool, bool)
	LowerExpression(ExpressionLowerer) ir.Expr
	AnalyzeFlow(FlowAnalyzer)
	BuildEffects(EffectBuilder)
}

type StmtInfo struct {
	Source ir.SourceInfo
}

func (s StmtInfo) thirNode()                 {}
func (s StmtInfo) stmtNode()                 {}
func (s StmtInfo) SourceInfo() ir.SourceInfo { return s.Source }

type ExprInfo struct {
	Source ir.SourceInfo
	Type   typeinfo.Type
	// CaseCarrier is set when the expression reads the payload of a place
	// that holds one case: Type is that payload, CaseCarrier the place's own
	// type, and CasePath the case read through at each level from one to the
	// other. Flow analysis verifies the cases at this use.
	CaseCarrier                typeinfo.Type
	CasePath                   []int
	ConversionInfo             *typeinfo.Conversion
	Use                        typeinfo.UseKind
	HasUse                     bool
	HasReferenceArgument       bool
	IsReferenceArgumentMutable bool
	ImplicitReference          typeinfo.Type
	Implementations            []InterfaceImplementation
	Place                      *Place
}

func (e ExprInfo) thirNode()                            {}
func (e ExprInfo) exprNode()                            {}
func (e ExprInfo) SourceInfo() ir.SourceInfo            { return e.Source }
func (e ExprInfo) ExprType() typeinfo.Type              { return e.Type }
func (e ExprInfo) ExprPlace() *Place                    { return e.Place }
func (e ExprInfo) CaseCarrierType() typeinfo.Type       { return e.CaseCarrier }
func (e ExprInfo) PayloadPath() []int                   { return e.CasePath }
func (e ExprInfo) Conversion() *typeinfo.Conversion     { return e.ConversionInfo }
func (e ExprInfo) ImplicitReferenceType() typeinfo.Type { return e.ImplicitReference }

// StoredType is the type of the storage the expression names: the carrier
// when the expression reads a narrowed place as its payload.
func (e ExprInfo) StoredType() typeinfo.Type {
	if e.CaseCarrier != nil {
		return e.CaseCarrier
	}
	return e.Type
}
func (e ExprInfo) InterfaceImplementations() []InterfaceImplementation {
	return e.Implementations
}
func (e ExprInfo) UseKind() (typeinfo.UseKind, bool) {
	return e.Use, e.HasUse
}
func (e ExprInfo) ReferenceArgInfo() (bool, bool) {
	return e.IsReferenceArgumentMutable, e.HasReferenceArgument
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
}

type PlaceProjectionKind uint8

const (
	PlaceField PlaceProjectionKind = iota
	PlaceIndex
)

type PlaceProjection struct {
	Source     ir.SourceInfo
	BaseSource ir.SourceInfo
	// BasePath is the payload path of the base: the projection starts inside
	// the payload the base reads.
	BasePath        []int
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
	Symbol     *symbols.Symbol
	IsConstant bool
	IsInferred bool
	Value      Expr
}

// Destructure moves Value whole and defines one local per binding. Fields of
// Value without a binding are discarded.
type Destructure struct {
	StmtInfo
	Value    Expr
	Bindings []DestructureBinding
}

type DestructureBinding struct {
	Source ir.SourceInfo
	Field  int
	Type   typeinfo.Type
	Symbol *symbols.Symbol
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
	Condition            Expr
	Then                 *Block
	Else                 Stmt
	ConstantCondition    *bool
	ConditionDiagnostics []*diagnostics.Diagnostic
}

type For struct {
	StmtInfo
	Index     *symbols.Symbol
	Value     *symbols.Symbol
	Iterable  Expr
	Condition Expr
	// ConstantCondition is the value of a condition known at compile time.
	ConstantCondition *bool
	Body              *Block
	Iteration         IterationPlan
	Checked           *Block
}

// ConditionIsTrueLiteral reports a loop written `for true`, which is the
// endless loop `for` with a longer spelling: it has no way to finish.
func (s *For) ConditionIsTrueLiteral() bool {
	literal, ok := s.Condition.(*BoolLiteral)
	return ok && literal.Value
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

// ArmCase is what a match arm selects: one case of the subject and, when
// TestsPayload, one case of that case's payload (`Case != none`,
// `Case is Enum::Inner`).
type ArmCase struct {
	Case         int
	TestsPayload bool
	PayloadCase  int
}

// PayloadPath is the case to read through at each level to reach the payload
// of what an arm with these cases selects: its case, and the payload case too
// when it selects exactly one.
func PayloadPath(cases []ArmCase) []int {
	if len(cases) == 0 {
		return nil
	}
	path := []int{cases[0].Case}
	if len(cases) == 1 && cases[0].TestsPayload {
		path = append(path, cases[0].PayloadCase)
	}
	return path
}

// MatchArm runs Body for each of Cases: one for a named arm, everything no
// other arm selects for the fallback arm `_`.
type MatchArm struct {
	Source ir.SourceInfo
	Cases  []ArmCase
	Body   *Block
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

// RangeIteration is the state of a `for i in a..b` loop. Limit holds the
// evaluated exclusive upper bound; Ordinal counts iterations and is set
// exactly when the loop binds an index name.
type RangeIteration struct {
	ElementType        typeinfo.Type
	Cursor             *symbols.Symbol
	Limit              *symbols.Symbol
	Ordinal            *symbols.Symbol
	HasGuaranteedEntry bool
}

func (*RangeIteration) iterationPlan() {}
func (p *RangeIteration) IsGuaranteedEntry() bool {
	return p != nil && p.HasGuaranteedEntry
}

// SequenceIteration is the state of a `for v in seq` loop. Carrier holds the
// iterated storage for the loop's lifetime, borrowed when CarrierType is a
// reference; the cursor indexes through it.
type SequenceIteration struct {
	ElementType        typeinfo.Type
	Cursor             *symbols.Symbol
	Carrier            *symbols.Symbol
	CarrierType        typeinfo.Type
	HasGuaranteedEntry bool
}

func (*SequenceIteration) iterationPlan() {}
func (p *SequenceIteration) IsGuaranteedEntry() bool {
	return p != nil && p.HasGuaranteedEntry
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
	Value     string
	IsCString bool
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
	Name                     string
	Symbol                   *symbols.Symbol
	IsExpandedDefaultBinding bool
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
	Start          Expr
	End            Expr
	IsEndExclusive bool
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
	Left                  Expr
	Op                    string
	Right                 Expr
	IsStringConcatenation bool
	Test                  *CaseTest
}

type Is struct {
	ExprInfo
	Value Expr
	Test  *CaseTest
}

type CaseTest struct {
	SubjectID       source.NodeID
	Case            int
	MatchesWhenTrue bool
	CaseCount       int
	Family          typeinfo.VariantFamily
}

// CaseTestOf returns the case test expr makes, or nil for one that tests no
// case. Binary and Is are the only expressions that carry one.
func CaseTestOf(expr Expr) *CaseTest {
	switch tested := expr.(type) {
	case *Binary:
		return tested.Test
	case *Is:
		return tested.Test
	}
	return nil
}

type Call struct {
	ExprInfo
	Callee       Expr
	Args         []Expr
	IsPiped      bool
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
	Value          Expr
	AppendsNewline bool
}

type Cast struct {
	ExprInfo
	Value Expr
}
