package hir

import (
	"strconv"
	"strings"

	"compiler/internal/ir"
	"compiler/internal/semantics/symbols"
	"compiler/internal/source"
)

// NodeID identifies source AST node that produced this HIR node. It remains
// valid across HIR transformations without retaining AST objects.
type NodeID = ir.NodeID

type Module struct {
	Name     string
	FilePath string
	Types    *ir.TypeTable
	Externs  []Extern
	Funcs    []*Function
}

type Extern struct {
	Name       string
	Params     []ir.Param
	ReturnType ir.TypeID
	NodeID     NodeID
	SymbolID   symbols.SymbolID
	Location   *source.Location
}

type Function struct {
	Name       string
	Params     []ir.Param
	ReturnType ir.TypeID
	Body       *Block
	NodeID     NodeID
	SymbolID   symbols.SymbolID
	Location   *source.Location
}

type Stmt interface {
	stmtNode()
	forEachChild(func(Stmt))
	appendText(*strings.Builder, int)
	sourceInfo() ir.SourceInfo
}

func SourceInfoOf(node Stmt) ir.SourceInfo {
	if node == nil {
		return ir.SourceInfo{}
	}
	return node.sourceInfo()
}

func LocOf(node Stmt) *source.Location {
	return SourceInfoOf(node).Location
}

func NodeIDOf(node Stmt) NodeID {
	return SourceInfoOf(node).NodeID
}

type Block struct {
	Stmts    []Stmt
	NodeID   NodeID
	Location *source.Location
}

type Binding struct {
	Name     string
	Constant bool
	Type     ir.TypeID
	Value    ir.Expr
	NodeID   NodeID
	SymbolID symbols.SymbolID
	Location *source.Location
}

type ExprStmt struct {
	Value       ir.Expr
	NodeID      NodeID
	ValueNodeID NodeID
	Location    *source.Location
}

type Assign struct {
	Target     *ir.Place
	Value      ir.Expr
	DropTarget bool
	NodeID     NodeID
	Location   *source.Location
}

type Invalid struct {
	Message  string
	NodeID   NodeID
	Location *source.Location
}

type Return struct {
	Value    ir.Expr
	Cleanup  []ir.Expr
	NodeID   NodeID
	Location *source.Location
}

type If struct {
	Cond     ir.Expr
	Then     *Block
	Else     Stmt
	NodeID   NodeID
	Location *source.Location
}

type For struct {
	Cond     ir.Expr
	Body     *Block
	NodeID   NodeID
	Location *source.Location
}

type VariantCaseBlock struct {
	Case        int
	PayloadType ir.TypeID
	Bindings    []VariantBinding
	Body        *Block
}

type VariantBinding struct {
	FieldIndex int
	Name       string
	Type       ir.TypeID
	SymbolID   symbols.SymbolID
}

// SwitchVariant owns semantic subject and case bodies; CFG owns target edges.
type SwitchVariant struct {
	Value    ir.Expr
	Cases    []VariantCaseBlock
	NodeID   NodeID
	Location *source.Location
}

func (*Block) stmtNode()         {}
func (*Binding) stmtNode()       {}
func (*ExprStmt) stmtNode()      {}
func (*Assign) stmtNode()        {}
func (*Invalid) stmtNode()       {}
func (*Return) stmtNode()        {}
func (*If) stmtNode()            {}
func (*For) stmtNode()           {}
func (*SwitchVariant) stmtNode() {}

func (s *Block) forEachChild(visit func(Stmt)) {
	for _, stmt := range s.Stmts {
		visit(stmt)
	}
}
func (*Binding) forEachChild(func(Stmt))  {}
func (*ExprStmt) forEachChild(func(Stmt)) {}
func (*Assign) forEachChild(func(Stmt))   {}
func (*Invalid) forEachChild(func(Stmt))  {}
func (*Return) forEachChild(func(Stmt))   {}
func (s *If) forEachChild(visit func(Stmt)) {
	visit(s.Then)
	visit(s.Else)
}
func (s *For) forEachChild(visit func(Stmt)) { visit(s.Body) }
func (s *SwitchVariant) forEachChild(visit func(Stmt)) {
	for _, variantCase := range s.Cases {
		visit(variantCase.Body)
	}
}

// InspectStmt traverses structured HIR in depth-first preorder.
func InspectStmt(stmt Stmt, visit func(Stmt) bool) {
	if stmt == nil || !visit(stmt) {
		return
	}
	stmt.forEachChild(func(child Stmt) { InspectStmt(child, visit) })
}

func (b *Block) sourceInfo() ir.SourceInfo {
	return ir.SourceInfo{NodeID: b.NodeID, Location: b.Location}
}
func (b *Binding) sourceInfo() ir.SourceInfo {
	return ir.SourceInfo{NodeID: b.NodeID, Location: b.Location}
}
func (e *ExprStmt) sourceInfo() ir.SourceInfo {
	return ir.SourceInfo{NodeID: e.NodeID, Location: e.Location}
}
func (a *Assign) sourceInfo() ir.SourceInfo {
	return ir.SourceInfo{NodeID: a.NodeID, Location: a.Location}
}
func (i *Invalid) sourceInfo() ir.SourceInfo {
	return ir.SourceInfo{NodeID: i.NodeID, Location: i.Location}
}
func (r *Return) sourceInfo() ir.SourceInfo {
	return ir.SourceInfo{NodeID: r.NodeID, Location: r.Location}
}
func (f *If) sourceInfo() ir.SourceInfo {
	return ir.SourceInfo{NodeID: f.NodeID, Location: f.Location}
}
func (f *For) sourceInfo() ir.SourceInfo {
	return ir.SourceInfo{NodeID: f.NodeID, Location: f.Location}
}
func (s *SwitchVariant) sourceInfo() ir.SourceInfo {
	return ir.SourceInfo{NodeID: s.NodeID, Location: s.Location}
}

func (m *Module) Text() string {
	if m == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("; hir module ")
	b.WriteString(m.Name)
	b.WriteString("\n")
	for _, ex := range m.Externs {
		b.WriteString("extern fn ")
		b.WriteString(ex.Name)
		b.WriteString(ir.SignatureText(m.Types, ex.Params, ex.ReturnType))
		b.WriteString("\n")
	}
	if len(m.Externs) > 0 {
		b.WriteString("\n")
	}
	if len(m.Funcs) == 0 {
		return b.String()
	}
	for _, fn := range m.Funcs {
		b.WriteString("fn ")
		b.WriteString(fn.Name)
		b.WriteString(ir.SignatureText(m.Types, fn.Params, fn.ReturnType))
		b.WriteString(" {\n")
		appendBlockText(&b, fn.Body, 1)
		b.WriteString("}\n")
	}
	return b.String()
}

func appendBlockText(b *strings.Builder, block *Block, indent int) {
	if b == nil || block == nil {
		return
	}
	for _, stmt := range block.Stmts {
		if stmt == nil {
			continue
		}
		stmt.appendText(b, indent)
	}
}

func writeIndent(b *strings.Builder, indent int) {
	for range indent {
		b.WriteString("  ")
	}
}

func (s *Block) appendText(b *strings.Builder, indent int) {
	writeIndent(b, indent)
	b.WriteString("{\n")
	appendBlockText(b, s, indent+1)
	writeIndent(b, indent)
	b.WriteString("}\n")
}

func (s *Binding) appendText(b *strings.Builder, indent int) {
	writeIndent(b, indent)
	if s.Constant {
		b.WriteString("const ")
	} else {
		b.WriteString("let ")
	}
	b.WriteString(s.Name)
	if s.Value != nil {
		b.WriteString(" = ")
		b.WriteString(s.Value.String())
	}
	b.WriteString("\n")
}

func (s *ExprStmt) appendText(b *strings.Builder, indent int) {
	writeIndent(b, indent)
	b.WriteString(s.Value.String())
	b.WriteString("\n")
}

func (s *Assign) appendText(b *strings.Builder, indent int) {
	writeIndent(b, indent)
	if s.DropTarget {
		b.WriteString("drop-before ")
	}
	b.WriteString(s.Target.String())
	b.WriteString(" = ")
	b.WriteString(s.Value.String())
	b.WriteString("\n")
}

func (s *Invalid) appendText(b *strings.Builder, indent int) {
	writeIndent(b, indent)
	b.WriteString("invalid")
	if s != nil && s.Message != "" {
		b.WriteString(" ")
		b.WriteString(s.Message)
	}
	b.WriteString("\n")
}

func (s *Return) appendText(b *strings.Builder, indent int) {
	for _, cleanup := range s.Cleanup {
		writeIndent(b, indent)
		b.WriteString(cleanup.String())
		b.WriteString("\n")
	}
	writeIndent(b, indent)
	b.WriteString("return")
	if s != nil && s.Value != nil {
		b.WriteString(" ")
		b.WriteString(s.Value.String())
	}
	b.WriteString("\n")
}

func (s *If) appendText(b *strings.Builder, indent int) {
	writeIndent(b, indent)
	b.WriteString("if ")
	b.WriteString(s.Cond.String())
	b.WriteString(" {\n")
	appendBlockText(b, s.Then, indent+1)
	writeIndent(b, indent)
	b.WriteString("}")
	if s.Else == nil {
		b.WriteString("\n")
		return
	}
	b.WriteString(" else ")
	s.Else.appendText(b, indent)
}

func (s *For) appendText(b *strings.Builder, indent int) {
	writeIndent(b, indent)
	b.WriteString("for")
	if s != nil && s.Cond != nil {
		b.WriteString(" ")
		b.WriteString(s.Cond.String())
	}
	b.WriteString(" {\n")
	appendBlockText(b, s.Body, indent+1)
	writeIndent(b, indent)
	b.WriteString("}\n")
}

func (s *SwitchVariant) appendText(b *strings.Builder, indent int) {
	writeIndent(b, indent)
	b.WriteString("switch-variant ")
	b.WriteString(s.Value.String())
	b.WriteString(" {\n")
	for _, variantCase := range s.Cases {
		writeIndent(b, indent+1)
		b.WriteString("case ")
		b.WriteString(strconv.Itoa(variantCase.Case))
		b.WriteString(" {\n")
		appendBlockText(b, variantCase.Body, indent+2)
		writeIndent(b, indent+1)
		b.WriteString("}\n")
	}
	writeIndent(b, indent)
	b.WriteString("}\n")
}
