//! Checking expressions into THIR. Each expression gets its type, and an
//! operation on values known at compile time is folded to its value.

mod borrows;
mod calls;
mod literals;
mod operators;
mod places;

use arena::Id;
use diagnostics::Code;
use diagnostics::Diagnostic;
use diagnostics::fix::Fix;
use model::ast;
use model::constant::Value;
use model::operators::Precedence;
use model::semantic_types::Conversion;
use model::semantic_types::Type;
use model::semantic_types::TypeId;
use model::thir::Expr;
use model::thir::ExprKind;
use source::Span;

use crate::builder::ThirBuilder;

/// What the context of an expression needs of its type.
#[derive(Clone, Copy)]
pub(crate) enum TypeExpectation {
    /// The type a number literal without a suffix takes, if any; the value
    /// may have another type.
    Hint(Option<TypeId>),
    /// The value must be of this type, or convert to it without `as`
    /// (`coerce`); a mismatch is reported.
    Required(TypeId),
}

impl ThirBuilder<'_, '_> {
    /// `expression` (`check_expr`), added to the body.
    pub(crate) fn add_expr(
        &mut self,
        expression: Id<ast::Expr>,
        expectation: TypeExpectation,
    ) -> Id<Expr> {
        let checked = self.check_expr(expression, expectation);
        self.body
            .add(checked, self.checker.module().ast.exprs.span(expression))
    }

    /// `expr`, checked from `expression`, as a value of the type `to`, which
    /// its type converts to without `as` (`Types::conversion`), else the
    /// mismatch to report. An error already reported on either side gives an
    /// invalid expression and nothing to report.
    fn coerce(
        &mut self,
        expr: Expr,
        expression: Id<ast::Expr>,
        to: TypeId,
    ) -> Result<Expr, Diagnostic> {
        if expr.ty == TypeId::INVALID || to == TypeId::INVALID {
            return Ok(Expr::INVALID);
        }
        if expr.ty == to {
            return Ok(expr); // most values already have the type: skip the lookup
        }
        let span = self.checker.module().ast.exprs.span(expression);
        let found = expr.ty;
        let conversion = self.checker.types.conversion(found, to);
        if conversion != Conversion::NeedsCast
            && let Some(converted) = self.convert(expr, span, to, conversion)
        {
            return Ok(converted);
        }
        let (expected, actual) = self.checker.type_texts(to, found);
        let message = format!("mismatched types: expected `{expected}`, found `{actual}`");
        let types = &self.checker.types;
        Err(self.type_error(
            diagnostics::TYPE_MISMATCH,
            message,
            span,
            expression,
            found,
            |ty| {
                matches!(
                    types.conversion(ty, to),
                    Conversion::Same | Conversion::Implicit
                )
            },
        ))
    }

    /// `mismatch`, made by `coerce` for `expression` of the type `found`,
    /// with a guess that converts it with `as` when only `as` converts it to
    /// `to`. Its callers add it, not `coerce`: a mismatched left literal has
    /// a better fix, which keeps the value (`1i64 + x`).
    fn help_cast(
        &self,
        mismatch: Diagnostic,
        expression: Id<ast::Expr>,
        found: TypeId,
        to: TypeId,
    ) -> Diagnostic {
        let types = &self.checker.types;
        if types.conversion(found, to) != Conversion::NeedsCast {
            return mismatch;
        }
        // The type itself, as an alias's text is not a type to write.
        let target = self.checker.type_text(types.real(to));
        let suffix = format!(" as {target}");
        let fixes = self.append_fixes(expression, &suffix, Precedence::Cast);
        let help = format!("add `as {target}` to convert, which can change the value");
        mismatch.guess(help, fixes)
    }

    /// `expr`, written at `span`, as a value of the type `to` by
    /// `conversion` (`Types::conversion`): given the type, converted (folded
    /// when it is a constant) or lent again as a `&`. `None` when it never
    /// converts.
    fn convert(
        &mut self,
        expr: Expr,
        span: Span,
        to: TypeId,
        conversion: Conversion,
    ) -> Option<Expr> {
        let kind = match conversion {
            Conversion::Same => expr.kind,
            Conversion::Implicit | Conversion::NeedsCast => match expr.kind {
                ExprKind::Constant(constant) => {
                    let types = &self.checker.types;
                    ExprKind::Constant(eval::convert(&types[expr.ty], &types[to], &constant))
                }
                _ => ExprKind::Convert(self.body.add(expr, span)),
            },
            Conversion::Reborrow => return Some(self.reborrow(expr, span, to)),
            Conversion::Never => return None,
        };
        Some(Expr { ty: to, kind })
    }

    /// The THIR of `expression`, as `expectation` needs it, not yet added to
    /// the body, so that a folded operation leaves no nodes for its operands.
    pub(crate) fn check_expr(
        &mut self,
        expression: Id<ast::Expr>,
        expectation: TypeExpectation,
    ) -> Expr {
        let expected = match expectation {
            TypeExpectation::Hint(hint) => hint,
            TypeExpectation::Required(ty) => Some(ty),
        };
        let tree = &self.checker.module().ast;
        let span = tree.exprs.span(expression);
        let checked = 'checked: {
            let what = match tree.exprs[expression] {
                ast::Expr::Name => {
                    let symbol = self.resolve(span);
                    break 'checked self.check_symbol_value(symbol, span);
                }
                ast::Expr::Number => break 'checked self.check_number(span, None, expected),
                ast::Expr::Bool(value) => {
                    break 'checked Expr {
                        ty: TypeId::BOOL,
                        kind: ExprKind::Constant(Value::Bool(value)),
                    };
                }
                ast::Expr::String { c_string: true } => break 'checked self.c_string(span),
                ast::Expr::Parentheses(inner) => {
                    break 'checked self.check_expr(inner, TypeExpectation::Hint(expected));
                }
                ast::Expr::Unary { op, value } => {
                    break 'checked self.check_unary(op, value, span, expected);
                }
                ast::Expr::Binary { op, left, right } => {
                    break 'checked self.check_binary(op, left, right, span, expected);
                }
                ast::Expr::Invalid => break 'checked Expr::INVALID, // the parser reported it
                ast::Expr::Path(path) => {
                    let symbol = self.checker.resolve_path(path);
                    break 'checked self.check_symbol_value(symbol, span);
                }
                ast::Expr::Call { callee, arguments } => {
                    break 'checked self.check_call(callee, arguments, span);
                }
                ast::Expr::StructLiteral { ty, fields } => {
                    break 'checked self.check_struct_literal(ty, fields, span, expected);
                }
                ast::Expr::Selector { value, field } => {
                    break 'checked self.check_field(value, field);
                }
                ast::Expr::ArrayLiteral { ty, entries } => {
                    break 'checked self.check_array_literal(ty, entries, span);
                }
                ast::Expr::Range { .. } => break 'checked self.check_range(expression, expected),
                ast::Expr::Index { value, index } => {
                    break 'checked self.check_index(value, index, span);
                }
                ast::Expr::Dereference(value) => {
                    break 'checked self.check_dereference(value, span);
                }
                ast::Expr::Reference { mutable, value } => {
                    if let ast::Expr::Index { index, .. } = tree.exprs[value]
                        && let ast::Expr::Range { .. } = tree.exprs[index]
                    {
                        break 'checked self.check_slice(mutable, value);
                    }
                    // `&5` where a `&i64` is wanted lends an `i64`.
                    let target = expected.and_then(|ty| self.checker.types[ty].pointee());
                    let place = self.add_expr(value, TypeExpectation::Hint(target));
                    // A constant has no storage: each use is a new value, so a
                    // write through the reference would not change it.
                    let named = tree.unparenthesized(value);
                    if mutable
                        && let ast::Expr::Name | ast::Expr::Path(_) = tree.exprs[named]
                        && let ExprKind::Constant(_) = self.body.exprs[place].kind
                    {
                        let name = tree.file.slice(tree.exprs.span(named));
                        let message = format!("cannot mutably borrow constant `{name}`");
                        self.checker
                            .error(diagnostics::CONSTANT_REASSIGNMENT, message, span);
                        break 'checked Expr::INVALID;
                    }
                    break 'checked self.check_borrow(mutable, place, span);
                }
                ast::Expr::Cast { value, ty } => break 'checked self.check_cast(value, ty, span),
                ast::Expr::None => break 'checked self.check_none(span, expected),
                ast::Expr::RawAddress(value) => {
                    let place = self.add_expr(value, TypeExpectation::Hint(None));
                    break 'checked self.check_address(place, span);
                }
                ast::Expr::Print { newline, values } => {
                    break 'checked self.check_print(newline, values);
                }
                ast::Expr::String { c_string: false } => "string literals",
                ast::Expr::Char { .. } => "character literals",
                ast::Expr::Free(_) => "`free` calls",
                ast::Expr::VariantLiteral { .. } => "variant literals",
                ast::Expr::Is { .. } => "`is` tests",
                ast::Expr::OrElse { .. } => "`orelse` fallbacks",
                ast::Expr::Block(_) => "block expressions",
                ast::Expr::Pipe { .. } => "pipes",
            };
            self.checker.not_supported(span, what);
            Expr::INVALID
        };
        match expectation {
            TypeExpectation::Required(ty) => self.require(checked, expression, ty),
            TypeExpectation::Hint(_) => checked,
        }
    }

    /// `checked`, from `expression`, as a value of the type `ty` (`coerce`);
    /// a mismatch is reported, with a guess that adds `as` when only `as`
    /// converts it.
    pub(crate) fn require(&mut self, checked: Expr, expression: Id<ast::Expr>, ty: TypeId) -> Expr {
        let found = checked.ty;
        self.coerce(checked, expression, ty)
            .unwrap_or_else(|mismatch| {
                let mismatch = self.help_cast(mismatch, expression, found, ty);
                self.checker.report(mismatch);
                Expr::INVALID
            })
    }

    /// The error `message` with `code` at `span`, for `operand` of the type
    /// `found`, which is not what is needed there. When `found` is a
    /// reference to a value of a type `accepts` takes, it helps to read that
    /// value with `.*`. An error that a value's type is not one its place
    /// takes is made here, so each such error gets the same help.
    fn type_error(
        &self,
        code: Code,
        message: String,
        span: Span,
        operand: Id<ast::Expr>,
        found: TypeId,
        accepts: impl Fn(TypeId) -> bool,
    ) -> Diagnostic {
        let tree = &self.checker.module().ast;
        let diagnostic = Diagnostic::new(code, message).label(&tree.file, span, "");
        // Not a raw pointer's: it may be `none`, so `.*` is no safe fix.
        let Type::Reference { target, .. } = self.checker.types[found] else {
            return diagnostic;
        };
        if !accepts(target) {
            return diagnostic;
        }
        let fixes = self.append_fixes(operand, ".*", Precedence::Call);
        diagnostic.help("add `.*` to use the value it points to", fixes)
    }

    /// The fixes that write `suffix`, an operator of the precedence
    /// `precedence`, after `operand`, in parentheses when it would join only
    /// the operand's last part: `&n.*` reads `n`, so `&n` needs `(&n).*`.
    fn append_fixes(
        &self,
        operand: Id<ast::Expr>,
        suffix: &str,
        precedence: Precedence,
    ) -> Vec<Fix> {
        let tree = &self.checker.module().ast;
        let span = tree.exprs.span(operand);
        let insert = |at: u32, new_text: String| Fix {
            file: tree.file.clone(),
            span: Span { start: at, end: at },
            new_text,
        };
        if tree.exprs[operand].precedence() >= precedence {
            vec![insert(span.end, suffix.to_string())]
        } else {
            vec![
                insert(span.start, "(".to_string()),
                insert(span.end, format!("){suffix}")),
            ]
        }
    }
}

/// The text of `expression` if it is a number literal without a suffix.
pub(crate) fn unsuffixed_literal(tree: &ast::Tree, expression: Id<ast::Expr>) -> Option<&str> {
    let text = tree.file.slice(tree.exprs.span(expression));
    let number = matches!(tree.exprs[expression], ast::Expr::Number);
    (number && literal::numeric::split(text).suffix.is_empty()).then_some(text)
}
