//! Checking literals that build a value: strings, `none`, and the entries
//! and spreads of arrays.

use std::num::NonZeroU32;

use arena::Id;
use diagnostics::Diagnostic;
use diagnostics::fix::Fix;
use model::constant::Value;
use model::semantic_types::Conversion;
use model::semantic_types::Type;
use model::semantic_types::TypeId;
use model::thir::Expr;
use model::thir::ExprKind;
use model::thir::FactKind;
use source::Span;
use syntax::ast;
use syntax::operators::Precedence;

use crate::Checked;
use crate::builder::ThirBuilder;
use crate::expression::unsuffixed_literal;

impl<L: Checked> ThirBuilder<'_, '_, L> {
    /// The string literal at `span`, a `&str` or with its `c` a `&cstr` (`ty`):
    /// its bytes.
    pub(crate) fn string_literal(&mut self, span: Span, ty: TypeId) -> Expr {
        self.checker.record(span, FactKind::Literal(ty));
        let bytes = literal::value(self.checker.module().ast.file.slice(span));
        Expr {
            ty,
            kind: ExprKind::Constant(Value::Bytes(bytes.into())),
        }
    }

    /// `none`, written at `span`: the null raw pointer of the type
    /// `expected`, which must be a raw pointer type.
    pub(super) fn check_none(&mut self, span: Span, expected: Option<TypeId>) -> Expr {
        match expected.map(|ty| (ty, &self.checker.types[ty])) {
            Some((ty, Type::RawPointer { .. })) => Expr {
                ty,
                kind: ExprKind::Constant(Value::Null),
            },
            Some((_, Type::Invalid)) => Expr::INVALID,
            Some((ty, _)) => {
                let message = format!(
                    "mismatched types: expected `{}`, found `none`",
                    self.checker.type_text(ty)
                );
                self.checker
                    .error(diagnostics::TYPE_MISMATCH, message, span);
                Expr::INVALID
            }
            None => {
                let message =
                    "`none` needs its type from where it is used: write `let p: *T = none`";
                self.checker.error(diagnostics::MISSING_TYPE, message, span);
                Expr::INVALID
            }
        }
    }

    /// The `entries` of an array of `element`s, each a value or a spread
    /// (`count_spread`), checked: with how many values are known now, `None`
    /// after an error in a spread, and the first spread not counted until the
    /// program runs, with why. A value that mismatches is reported, with how
    /// to spread it when it holds `element`s (`help_spread`, given `alone_in`).
    /// Without an element type, each entry is checked for its own errors.
    pub fn check_entries(
        &mut self,
        entries: impl Iterator<Item = Id<ast::Expr<L>>>,
        element: TypeId,
        alone_in: Option<TypeId>,
    ) -> (Vec<NonZeroU32>, Option<u128>, Option<(Span, &'static str)>) {
        let tree = &self.checker.module().ast;
        let hint = (element != TypeId::INVALID).then_some(element);
        let mut parts: Vec<NonZeroU32> = Vec::with_capacity(entries.size_hint().0);
        // Each count fits a `usize`, so their sum fits a `u128`.
        let (mut known, mut later) = (Some(0u128), None);
        for entry in entries {
            let (part, count) = if let ast::Expr::Spread(values) = tree.exprs[entry] {
                let spread = self.add_spread(tree.unparenthesized(values), element);
                let Some(checked) = self.count_spread(spread, element) else {
                    known = None;
                    continue;
                };
                checked
            } else {
                let value = self.add_expr(entry, hint);
                let found = self.body.exprs[value].ty;
                if found != element {
                    let checked = self.body.exprs[value].clone();
                    let coerced = self
                        .coerce(checked, entry, element)
                        .unwrap_or_else(|mismatch| {
                            let types = &self.checker.types;
                            let viewed = match types[found] {
                                Type::Reference { target, .. } => target,
                                _ => found,
                            };
                            let holds_elements =
                                types[viewed].elements().is_some_and(|(from, _)| {
                                    types.conversion(from, element) == Conversion::Same
                                });
                            let mismatch = if holds_elements {
                                self.help_spread(mismatch, entry, value, alone_in)
                            } else {
                                self.help_cast(mismatch, entry, found, element)
                            };
                            self.checker.report(mismatch);
                            Expr::INVALID
                        });
                    self.body.exprs[value] = coerced;
                }
                (value, Ok(1))
            };
            match count {
                Ok(count) => known = known.map(|known| known + u128::from(count)),
                Err(reason) => later = later.or(Some((tree.exprs.span(entry), reason))),
            }
            parts.push(part.into());
        }
        (parts, known, later)
    }

    /// `mismatch`, for `written`, the array, slice or reference to one `value`
    /// where one of its elements goes, with how to spread it: a sure fix only
    /// when `value` is alone in the `...` parameter `alone_in` and nothing is
    /// lost, else a guess, or nothing when `...` would mismatch too, as a
    /// reference does unless it converts to the parameter. A writing parameter
    /// lends what can be written, so writes reach it, and takes a value in no
    /// variable, which nothing else can see; a reading one needs a slice, or
    /// an array that copies, so its array is not moved.
    fn help_spread(
        &self,
        mismatch: Diagnostic,
        written: Id<ast::Expr<L>>,
        value: Id<Expr>,
        alone_in: Option<TypeId>,
    ) -> Diagnostic {
        let types = &self.checker.types;
        let spread = "add `...` to spread its values";
        // `...` takes the whole expression before it, so nothing is wrapped.
        let fixes = self.append_fixes(written, "...", Precedence::Lowest);
        let found = self.body.exprs[value].ty;
        if let Type::Reference { .. } = types[found] {
            let lends = alone_in.is_some_and(|parameter| {
                types.conversion(found, parameter) == Conversion::Reborrow
            });
            return if lends {
                mismatch.help(spread, fixes)
            } else {
                mismatch
            };
        }
        let Some(parameter) = alone_in else {
            return mismatch.guess(spread, fixes);
        };
        let writes = types[parameter].borrows() == Some(true);
        match (&types[found], writes) {
            (Type::Slice { mutable: false, .. }, true) => mismatch,
            (Type::Slice { .. }, _) => mismatch.help(spread, fixes),
            (_, false) if types.is_copy(found) => mismatch.help(spread, fixes),
            (_, false) => mismatch.guess(spread, fixes),
            (_, true) => {
                let (start, decider) = self.place_root(value);
                let writable = match (decider, &self.body.exprs[start].kind) {
                    (Some(decider), _) => self.writes_through(decider),
                    (None, &ExprKind::Variable(variable)) => self.body.variables[variable].mutable,
                    (None, _) => return mismatch.help(spread, fixes),
                };
                if !writable {
                    return mismatch.guess(spread, fixes);
                }
                let tree = &self.checker.module().ast;
                let span = tree.exprs.span(written);
                let lend = Fix {
                    file: tree.file.clone(),
                    span,
                    new_text: format!("&mut {}...", tree.file.slice(span)),
                };
                mismatch.help("add `&mut` and `...` to write to it", vec![lend])
            }
        }
    }

    /// The slice a `...` `parameter` takes from `values`, the last arguments
    /// of the call written at `span`: a slice spread alone, passed as it is,
    /// else a slice of an array made from them, as `[_]T{ values }` would be.
    /// That array holds what is listed, so writes through a `...mut T` never
    /// reach the variables named.
    pub(super) fn check_variadic(
        &mut self,
        values: &[Id<ast::Expr<L>>],
        parameter: TypeId,
        span: Span,
    ) -> Id<Expr> {
        let tree = &self.checker.module().ast;
        let element = self.checker.types[parameter]
            .elements()
            .map_or(TypeId::INVALID, |(element, _)| element);
        let (parts, known, later) = if let [only] = *values
            && let ast::Expr::Spread(spread) = tree.exprs[only]
        {
            let written = tree.unparenthesized(spread);
            let spread = self.add_spread(written, element);
            if matches!(
                self.checker.types[self.body.exprs[spread].ty],
                Type::Slice { .. } | Type::Reference { .. }
            ) {
                let slice = self.body.exprs[spread].clone();
                self.body.exprs[spread] = self.require(slice, written, parameter);
                return spread;
            }
            match self.count_spread(spread, element) {
                Some((part, Ok(count))) => (vec![part.into()], Some(u128::from(count)), None),
                Some((_, Err(reason))) => {
                    let span = tree.exprs.span(written);
                    self.checker.error(diagnostics::TYPE_MISMATCH, reason, span);
                    (Vec::new(), None, None)
                }
                None => (Vec::new(), None, None),
            }
        } else {
            let alone_in = (values.len() == 1).then_some(parameter);
            self.check_entries(values.iter().copied(), element, alone_in)
        };
        if let Some((spread, reason)) = later {
            let message = format!(
                "{reason}, so a call cannot build an array around it; pass a slice as the \
                 only argument for `...`"
            );
            self.checker
                .error(diagnostics::TYPE_MISMATCH, message, spread);
            return self.body.add(Expr::INVALID, span);
        }
        let Some(known) = known.filter(|_| element != TypeId::INVALID) else {
            return self.body.add(Expr::INVALID, span);
        };
        let Some(ty) = self.array_literal_type(span, element, None, (known, None), span) else {
            return self.body.add(Expr::INVALID, span);
        };
        let kind = ExprKind::ArrayLiteral(self.body.children.lists.add(&parts));
        let array = self.body.add(Expr { ty, kind }, span);
        let slice = self.borrow_as(parameter, array);
        self.body.add(slice, span)
    }

    /// The type of the array literal written at `span`, its type at `written`,
    /// of `element`s, with `length` when written (`[N]`, as its type), whose
    /// entries give `known` values now and more when the program runs if a
    /// spread, first at `later` with why `[_]` cannot count it, is counted
    /// then; `None` after reporting that they do not fit it
    /// (`arrays::check::check_literal`).
    pub fn array_literal_type(
        &mut self,
        written: Span,
        element: TypeId,
        length: Option<(TypeId, u64)>,
        (known, later): (u128, Option<(Span, &'static str)>),
        span: Span,
    ) -> Option<TypeId> {
        match (length, later) {
            (None, Some((spread, reason))) => {
                let message = format!("{reason}; write the length");
                self.checker
                    .error(diagnostics::TYPE_MISMATCH, message, spread);
                None
            }
            (None, None) => {
                if !self
                    .checker
                    .length_fits(&[known as u64, (known >> 64) as u64], span)
                {
                    return None;
                }
                Some(self.checker.array_type(element, known as u64, written))
            }
            (Some((ty, length)), later) => {
                let length = u128::from(length);
                if known == length || later.is_some() && known < length {
                    return Some(ty);
                }
                let found = if later.is_some() { "at least " } else { "" };
                let message = format!(
                    "`{}` needs {length} values, found {found}{known}",
                    self.checker.type_text(ty)
                );
                self.checker
                    .error(diagnostics::TYPE_MISMATCH, message, span);
                None
            }
        }
    }

    /// The written `values` of a spread into `element`s, checked.
    fn add_spread(&mut self, values: Id<ast::Expr<L>>, element: TypeId) -> Id<Expr> {
        // Only an integer element has ranges, whose ends then take its type.
        let hint = self.checker.types.range(element);
        self.add_expr(values, hint)
    }

    /// `spread`, the values of a `values...` going into an array of
    /// `element`s, with how many it gives when that is known now, else why
    /// it cannot be counted; `None` after an error. An array gives its
    /// elements, which must be `element`s; a slice copies its elements, which
    /// must copy; a range its values, counted now when it is written with
    /// constant ends and step; a count no array can hold is reported.
    fn count_spread(
        &mut self,
        spread: Id<Expr>,
        element: TypeId,
    ) -> Option<(Id<Expr>, Result<u64, &'static str>)> {
        let span = self.body.exprs.span(spread);
        let ty = self.body.exprs[spread].ty;
        let types = &self.checker.types;
        match types[ty] {
            Type::Invalid => return None,
            Type::Array {
                element: from,
                length,
                ..
            } if element == TypeId::INVALID
                || types.conversion(from, element) == Conversion::Same =>
            {
                return Some((spread, Ok(length)));
            }
            Type::Slice {
                mutable,
                element: from,
            } if element == TypeId::INVALID
                || types.conversion(from, element) == Conversion::Same =>
            {
                if !types.is_copy(from) {
                    let file = &self.checker.module().ast.file;
                    let slice_text = self.checker.type_text(ty);
                    let element_text = self.checker.type_text(from);
                    let diagnostic = Diagnostic::new(
                        diagnostics::PARTIAL_MOVE,
                        "cannot move an element out of a slice",
                    )
                    .label(
                        file,
                        span,
                        format!(
                            "`...` copies each element of this `{slice_text}`, and \
                             `{element_text}` moves"
                        ),
                    );
                    self.checker.report(diagnostic);
                    return None;
                }
                // Read only, as Rust's `<[T; N]>::try_from(&*s)`, so a
                // `&mut []` in a variable is usable after.
                if mutable && let ExprKind::Variable(_) = self.body.exprs[spread].kind {
                    let shared = self.checker.types.intern(Type::Slice {
                        mutable: false,
                        element: from,
                    });
                    let reference = self.body.exprs[spread].clone();
                    self.body.exprs[spread] = self.reborrow(reference, span, shared);
                }
                let reason = "a slice has a length known only when the program runs";
                return Some((spread, Err(reason)));
            }
            Type::Range { element: from, .. }
                if element == TypeId::INVALID || types.same(from, element) => {}
            _ => {
                let (expected, found) = self.checker.type_texts(element, ty);
                let message = format!(
                    "`...` here spreads a range, an array or a slice of `{expected}`, \
                     found `{found}`"
                );
                self.checker
                    .error(diagnostics::TYPE_MISMATCH, message, span);
                return None;
            }
        }
        let uncounted = "a range needs constant ends and step";
        let exprs = &self.body.exprs;
        let constant = |id: Id<Expr>| match &exprs[id].kind {
            ExprKind::Constant(Value::Integer(limbs)) => Some(limbs),
            _ => None,
        };
        let ExprKind::Range {
            start,
            end,
            step,
            inclusive,
        } = exprs[spread].kind
        else {
            return Some((spread, Err(uncounted))); // a range value
        };
        let (Some(first), Some(last), Some(by)) = (constant(start), constant(end), constant(step))
        else {
            return Some((spread, Err(uncounted)));
        };
        let Type::Integer(integer) = types[exprs[start].ty] else {
            unreachable!("a range is of integers")
        };
        let count = integer.range_count(first, last, by, inclusive)?; // a zero step was reported
        if !self.checker.length_fits(count.as_slice(), span) {
            return None;
        }
        Some((spread, Ok(count.as_slice()[0]))) // it fits a `usize`
    }

    /// A range's or slice's ends `start` and `end`, of one type: a literal
    /// without a suffix takes the other end's type, or `hint` when both are
    /// such literals.
    pub fn check_range_ends(
        &mut self,
        start: Id<ast::Expr<L>>,
        end: Id<ast::Expr<L>>,
        hint: Option<TypeId>,
    ) -> (Id<Expr>, Id<Expr>) {
        let tree = &self.checker.module().ast;
        if unsuffixed_literal(tree, start).is_some() {
            let end = self.add_expr(end, hint);
            let ty = self.body.exprs[end].ty;
            (self.add_required(start, ty), end)
        } else {
            let start = self.add_expr(start, hint);
            let ty = self.body.exprs[start].ty;
            (start, self.add_required(end, ty))
        }
    }
}
