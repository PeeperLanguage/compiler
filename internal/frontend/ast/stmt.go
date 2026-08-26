package ast

import "compiler/internal/source"

type BlockStmt struct {
	NodeIDHolder
	Documented
	Stmts    []Stmt
	Location *source.Location
}

func (*BlockStmt) stmtNode() {}
func (s *BlockStmt) forEachChild(visit func(Node)) {
	for _, stmt := range s.Stmts {
		visit(stmt)
	}
}
func (s *BlockStmt) loc() *source.Location { return s.Location }

type ExprStmt struct {
	NodeIDHolder
	Documented
	Expr     Expr
	Location *source.Location
}

func (*ExprStmt) stmtNode()                       {}
func (s *ExprStmt) forEachChild(visit func(Node)) { visit(s.Expr) }
func (s *ExprStmt) loc() *source.Location         { return s.Location }

type AssignStmt struct {
	NodeIDHolder
	Documented
	Target   Expr
	Value    Expr
	Location *source.Location
}

func (*AssignStmt) stmtNode() {}
func (s *AssignStmt) forEachChild(visit func(Node)) {
	visit(s.Target)
	visit(s.Value)
}
func (s *AssignStmt) loc() *source.Location { return s.Location }

type ReturnStmt struct {
	NodeIDHolder
	Documented
	Value    Expr
	Location *source.Location
}

func (*ReturnStmt) stmtNode()                       {}
func (s *ReturnStmt) forEachChild(visit func(Node)) { visit(s.Value) }
func (s *ReturnStmt) loc() *source.Location         { return s.Location }

type BadStmt struct {
	NodeIDHolder
	Location *source.Location
}

func (*BadStmt) stmtNode()               {}
func (*BadStmt) forEachChild(func(Node)) {}
func (s *BadStmt) loc() *source.Location { return s.Location }

type IfStmt struct {
	NodeIDHolder
	Documented
	Cond     Expr
	Then     *BlockStmt
	Else     Stmt
	Location *source.Location
}

func (*IfStmt) stmtNode() {}
func (s *IfStmt) forEachChild(visit func(Node)) {
	visit(s.Cond)
	visit(s.Then)
	visit(s.Else)
}
func (s *IfStmt) loc() *source.Location { return s.Location }

type ForStmt struct {
	NodeIDHolder
	Documented
	Cond     Expr
	Body     *BlockStmt
	Location *source.Location
}

func (*ForStmt) stmtNode() {}
func (s *ForStmt) forEachChild(visit func(Node)) {
	visit(s.Cond)
	visit(s.Body)
}
func (s *ForStmt) loc() *source.Location { return s.Location }

type MatchPatternField struct {
	Name     *Ident
	Binding  *Ident
	Discard  bool
	Location *source.Location
}

type MatchArm struct {
	NodeIDHolder
	Case     *ScopeResolution
	Binding  *Ident
	Discard  bool
	Fields   []MatchPatternField
	HasData  bool
	Body     *BlockStmt
	Location *source.Location
}

func (a *MatchArm) forEachChild(visit func(Node)) {
	visit(a.Case)
	visit(a.Binding)
	for _, field := range a.Fields {
		visit(field.Name)
		visit(field.Binding)
	}
	visit(a.Body)
}
func (a *MatchArm) loc() *source.Location { return a.Location }

type MatchStmt struct {
	NodeIDHolder
	Documented
	Subject         Expr
	Arms            []*MatchArm
	ArmListLocation *source.Location
	Location        *source.Location
}

func (*MatchStmt) stmtNode() {}
func (s *MatchStmt) forEachChild(visit func(Node)) {
	visit(s.Subject)
	for _, arm := range s.Arms {
		visit(arm)
	}
}
func (s *MatchStmt) loc() *source.Location { return s.Location }
