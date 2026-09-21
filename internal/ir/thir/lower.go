package thir

import "compiler/internal/ir"

// ExpressionLowerer owns conversion from typed source expressions into shared
// lowered IR expressions. THIR nodes provide exhaustive dispatch only; lowering
// policy remains in consumer package.
type ExpressionLowerer interface {
	LowerInvalidExpr(*InvalidExpr) ir.Expr
	LowerNumberLiteral(*NumberLiteral) ir.Expr
	LowerStringLiteral(*StringLiteral) ir.Expr
	LowerByteLiteral(*ByteLiteral) ir.Expr
	LowerCharLiteral(*CharLiteral) ir.Expr
	LowerBoolLiteral(*BoolLiteral) ir.Expr
	LowerNoneLiteral(*NoneLiteral) ir.Expr
	LowerIdent(*Ident) ir.Expr
	LowerQualifiedIdent(*QualifiedIdent) ir.Expr
	LowerField(*Field) ir.Expr
	LowerIndex(*Index) ir.Expr
	LowerRange(*Range) ir.Expr
	LowerStructLiteral(*StructLiteral) ir.Expr
	LowerVariant(*Variant) ir.Expr
	LowerArrayLiteral(*ArrayLiteral) ir.Expr
	LowerAddress(*Address) ir.Expr
	LowerUnary(*Unary) ir.Expr
	LowerBinary(*Binary) ir.Expr
	LowerIs(*Is) ir.Expr
	LowerCall(*Call) ir.Expr
	LowerFree(*Free) ir.Expr
	LowerPrint(*Print) ir.Expr
	LowerCast(*Cast) ir.Expr
}

func (e *InvalidExpr) LowerExpression(l ExpressionLowerer) ir.Expr   { return l.LowerInvalidExpr(e) }
func (e *NumberLiteral) LowerExpression(l ExpressionLowerer) ir.Expr { return l.LowerNumberLiteral(e) }
func (e *StringLiteral) LowerExpression(l ExpressionLowerer) ir.Expr { return l.LowerStringLiteral(e) }
func (e *ByteLiteral) LowerExpression(l ExpressionLowerer) ir.Expr   { return l.LowerByteLiteral(e) }
func (e *CharLiteral) LowerExpression(l ExpressionLowerer) ir.Expr   { return l.LowerCharLiteral(e) }
func (e *BoolLiteral) LowerExpression(l ExpressionLowerer) ir.Expr   { return l.LowerBoolLiteral(e) }
func (e *NoneLiteral) LowerExpression(l ExpressionLowerer) ir.Expr   { return l.LowerNoneLiteral(e) }
func (e *Ident) LowerExpression(l ExpressionLowerer) ir.Expr         { return l.LowerIdent(e) }
func (e *QualifiedIdent) LowerExpression(l ExpressionLowerer) ir.Expr {
	return l.LowerQualifiedIdent(e)
}
func (e *Field) LowerExpression(l ExpressionLowerer) ir.Expr         { return l.LowerField(e) }
func (e *Index) LowerExpression(l ExpressionLowerer) ir.Expr         { return l.LowerIndex(e) }
func (e *Range) LowerExpression(l ExpressionLowerer) ir.Expr         { return l.LowerRange(e) }
func (e *StructLiteral) LowerExpression(l ExpressionLowerer) ir.Expr { return l.LowerStructLiteral(e) }
func (e *Variant) LowerExpression(l ExpressionLowerer) ir.Expr       { return l.LowerVariant(e) }
func (e *ArrayLiteral) LowerExpression(l ExpressionLowerer) ir.Expr  { return l.LowerArrayLiteral(e) }
func (e *Address) LowerExpression(l ExpressionLowerer) ir.Expr       { return l.LowerAddress(e) }
func (e *Unary) LowerExpression(l ExpressionLowerer) ir.Expr         { return l.LowerUnary(e) }
func (e *Binary) LowerExpression(l ExpressionLowerer) ir.Expr        { return l.LowerBinary(e) }
func (e *Is) LowerExpression(l ExpressionLowerer) ir.Expr            { return l.LowerIs(e) }
func (e *Call) LowerExpression(l ExpressionLowerer) ir.Expr          { return l.LowerCall(e) }
func (e *Free) LowerExpression(l ExpressionLowerer) ir.Expr          { return l.LowerFree(e) }
func (e *Print) LowerExpression(l ExpressionLowerer) ir.Expr         { return l.LowerPrint(e) }
func (e *Cast) LowerExpression(l ExpressionLowerer) ir.Expr          { return l.LowerCast(e) }
