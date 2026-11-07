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

impl ThirBuilder<'_, '_> {
    /// `expression` (`check_expr`), added to the body.
    pub(crate) fn add_expr(
        &mut self,
        expression: Id<ast::Expr>,
        expected: Option<TypeId>,
    ) -> Id<Expr> {
        let checked = self.check_expr(expression, expected);
        self.body
            .add(checked, self.checker.module().ast.exprs.span(expression))
    }

    /// `expression` (`check_required`), added to the body.
    pub(crate) fn add_required(&mut self, expression: Id<ast::Expr>, ty: TypeId) -> Id<Expr> {
        let checked = self.check_required(expression, ty);
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
                    ExprKind::Constant(eval::conversions::convert(
                        &types[expr.ty],
                        &types[to],
                        &constant,
                    ))
                }
                _ => ExprKind::Convert(self.body.add(expr, span)),
            },
            Conversion::Reborrow => return Some(self.reborrow(expr, span, to)),
            Conversion::ToInterface => {
                let types = &self.checker.types;
                let (
                    &Type::Reference {
                        mutable: from_mutable,
                        target,
                    },
                    &Type::InterfaceReference { mutable, interface },
                ) = (&types[expr.ty], &types[to])
                else {
                    unreachable!("a reference converts to an interface reference")
                };
                let Some(implementation) = self.checker.implementation(target, interface, span)
                else {
                    return Some(Expr::INVALID); // reported
                };
                // A `&mut T` made a `&I` is lent as `&T`, so it can be lent twice.
                let reference = if from_mutable && !mutable {
                    let shared = self.checker.types.intern(Type::Reference {
                        mutable: false,
                        target,
                    });
                    let expr = self.reborrow(expr, span, shared);
                    self.body.add(expr, span)
                } else {
                    let reference = self.body.add(expr, span);
                    self.reborrow_in_place(reference);
                    reference
                };
                ExprKind::ToInterface {
                    reference,
                    implementation,
                }
            }
            Conversion::Never => return None,
        };
        Some(Expr { ty: to, kind })
    }

    /// The THIR of `expression`, as a value of the type `ty`, or that
    /// converts to it without `as` (`coerce`); a mismatch is reported.
    pub(crate) fn check_required(&mut self, expression: Id<ast::Expr>, ty: TypeId) -> Expr {
        let checked = self.check_expr(expression, Some(ty));
        self.require(checked, expression, ty)
    }

    /// The THIR of `expression`, not yet added to the body, so that a folded
    /// operation leaves no nodes for its operands. `expected` is the type a
    /// number literal without a suffix takes, if any; the value may have
    /// another type.
    pub(crate) fn check_expr(
        &mut self,
        expression: Id<ast::Expr>,
        expected: Option<TypeId>,
    ) -> Expr {
        // A long chain of operators, calls or parentheses is a deep recursion.
        source::grow_stack(|| {
            let tree = &self.checker.module().ast;
            let span = tree.exprs.span(expression);
            match tree.exprs[expression] {
                ast::Expr::Name => {
                    let symbol = self.resolve(span);
                    self.check_symbol_value(symbol, span)
                }
                ast::Expr::Number => self.check_number(span, None, expected),
                ast::Expr::Bool(value) => Expr {
                    ty: TypeId::BOOL,
                    kind: ExprKind::Constant(Value::Bool(value)),
                },
                ast::Expr::String { c_string: true } => self.string_literal(span, TypeId::CSTR),
                ast::Expr::String { c_string: false } => self.string_literal(span, TypeId::STR),
                ast::Expr::Parentheses(inner) => self.check_expr(inner, expected),
                ast::Expr::Unary { op, value } => self.check_unary(op, value, span, expected),
                ast::Expr::Binary { op, left, right } => {
                    self.check_binary(op, left, right, span, expected)
                }
                ast::Expr::Invalid => Expr::INVALID, // the parser reported it
                // Only an argument that takes a type reads one.
                ast::Expr::Type(ty) => {
                    let message = format!("`{}` is a type, not a value", tree.file.slice(span));
                    let mut diagnostic = Diagnostic::new(diagnostics::INVALID_OPERATION, message)
                        .label(&tree.file, span, "");
                    // `*p` of a variable `p` reads as C's dereference, which
                    // is written `p.*`.
                    if let ast::Type::Pointer(target) = tree.types[ty]
                        && let ast::Type::Name = tree.types[target]
                        && let name = tree.file.slice(tree.types.span(target))
                        && self.innermost.contains_key(name)
                    {
                        let help = format!("write `{name}.*` to use the value `{name}` points to");
                        diagnostic = diagnostic.help(help, Vec::new());
                    }
                    self.checker.report(diagnostic);
                    Expr::INVALID
                }
                ast::Expr::Path(path) => {
                    let symbol = self.checker.resolve_path(path);
                    self.check_symbol_value(symbol, span)
                }
                ast::Expr::Call { callee, arguments } => self.check_call(callee, arguments, span),
                ast::Expr::StructLiteral { ty, fields } => {
                    self.check_struct_literal(ty, fields, span, expected)
                }
                ast::Expr::Selector { value, field } => self.check_field(value, field),
                ast::Expr::ArrayLiteral { ty, entries } => {
                    self.check_array_literal(ty, entries, span)
                }
                ast::Expr::Spread(values) => {
                    self.add_expr(values, None); // for its own errors
                    let message = "`...` spreads into an array literal or a `...` parameter";
                    self.checker
                        .error(diagnostics::INVALID_OPERATION, message, span);
                    Expr::INVALID
                }
                ast::Expr::Range { .. } => self.check_range(expression, expected),
                ast::Expr::Index { value, index } => self.check_index(value, index, span),
                ast::Expr::Dereference(value) => self.check_dereference(value, span),
                ast::Expr::Reference { mutable, value } => {
                    self.check_reference(mutable, value, span, expected)
                }
                ast::Expr::Cast { value, ty } => self.check_cast(value, ty, span),
                ast::Expr::None => self.check_none(span, expected),
                ast::Expr::RawAddress(value) => {
                    let place = self.add_expr(value, None);
                    self.check_address(place, span)
                }
                ast::Expr::Print { newline, values } => self.check_print(newline, values),
                ast::Expr::Char { .. } => self.unsupported_expr(span, "character literals"),
                ast::Expr::VariantLiteral { .. } => self.unsupported_expr(span, "variant literals"),
                ast::Expr::Is { .. } => self.unsupported_expr(span, "`is` tests"),
                ast::Expr::OrElse { .. } => self.unsupported_expr(span, "`orelse` fallbacks"),
                ast::Expr::Block(_) => self.unsupported_expr(span, "block expressions"),
                ast::Expr::Pipe { .. } => self.unsupported_expr(span, "pipes"),
            }
        })
    }

    /// The expression `&value`, or `&mut value` when `mutable`, at `span`.
    fn check_reference(
        &mut self,
        mutable: bool,
        value: Id<ast::Expr>,
        span: Span,
        expected: Option<TypeId>,
    ) -> Expr {
        let tree = &self.checker.module().ast;
        if let ast::Expr::Index { index, .. } = tree.exprs[value]
            && let ast::Expr::Range { .. } = tree.exprs[index]
        {
            return self.check_slice(mutable, value);
        }
        // `&5` where a `&i64` is wanted lends an `i64`.
        let target = expected.and_then(|ty| self.checker.types[ty].pointee());
        let place = self.add_expr(value, target);
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
            return Expr::INVALID;
        }
        // `len` has no storage either.
        if mutable
            && let ast::Expr::Selector { .. } = tree.exprs[named]
            && let Some(message) = self.fixed_length(place)
        {
            self.checker
                .error(diagnostics::INVALID_ASSIGNMENT, message, span);
            return Expr::INVALID;
        }
        self.check_borrow(mutable, place, span)
    }

    /// An expression of a kind that `what` names, at `span`, which no check
    /// exists for yet.
    fn unsupported_expr(&mut self, span: Span, what: &str) -> Expr {
        self.checker.not_supported(span, what);
        Expr::INVALID
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
