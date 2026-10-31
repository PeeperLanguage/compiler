//! Checking literals that build a value: strings, `none`, structs, arrays,
//! their spreads, and ranges.

use std::num::NonZeroU32;

use arena::Id;
use arena::List;
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
use model::thir::FactKind;
use model::thir::FieldValue;
use source::Span;

use crate::builder::ThirBuilder;
use crate::expression::TypeExpectation;
use crate::expression::unsuffixed_literal;

impl ThirBuilder<'_, '_> {
    /// The string literal at `span` as a `cstr`: its bytes.
    pub(crate) fn c_string(&mut self, span: Span) -> Expr {
        self.checker.record(span, FactKind::Literal(TypeId::CSTR));
        let bytes = literal::value(self.checker.module().ast.file.slice(span));
        Expr {
            ty: TypeId::CSTR,
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

    /// `Type.{ name = value, ... }`, written at `span`, or `.{ ... }` taking
    /// the struct type `expected`: a struct value with each field set once.
    /// A field that is unknown, set twice or missing is reported.
    pub(super) fn check_struct_literal(
        &mut self,
        written: Option<Id<ast::Type>>,
        fields: List<ast::FieldValue>,
        span: Span,
        expected: Option<TypeId>,
    ) -> Expr {
        let tree = &self.checker.module().ast;
        let ty = match (written, expected) {
            (Some(written), _) => self.checker.resolve_value_type(written),
            (None, Some(expected))
                if matches!(self.checker.types[expected], Type::Struct { .. }) =>
            {
                expected
            }
            (None, _) => {
                let message = "this struct literal needs its type: write it before `.{`";
                self.checker.error(diagnostics::MISSING_TYPE, message, span)
            }
        };
        let Type::Struct {
            definition,
            fields: ref field_types,
        } = self.checker.types[ty]
        else {
            if ty != TypeId::INVALID {
                let message = format!(
                    "expected a struct type, found `{}`",
                    self.checker.type_text(ty)
                );
                let span = written.map_or(span, |written| tree.types.span(written));
                self.checker
                    .error(diagnostics::TYPE_MISMATCH, message, span);
            }
            for field in tree.children.lists.items(fields) {
                self.add_expr(
                    tree.children.field_values[field].value,
                    TypeExpectation::Hint(None),
                ); // for its own errors
            }
            return Expr::INVALID;
        };
        let field_types = field_types.clone();
        // Where each field is set, by its index.
        let mut set: Vec<Option<Span>> = vec![None; field_types.len()];
        let mut checked: Vec<NonZeroU32> = Vec::with_capacity(fields.len());
        for field in tree.children.lists.items(fields) {
            let ast::FieldValue { name, value } = tree.children.field_values[field];
            let Some(index) = self.field_index(ty, name) else {
                self.add_expr(value, TypeExpectation::Hint(None)); // for its own errors
                continue;
            };
            if let Some(first) = set[index] {
                let text = name.text(&tree.file);
                self.checker.report(
                    Diagnostic::new(
                        diagnostics::DUPLICATE_FIELD,
                        format!("field `{text}` is set twice"),
                    )
                    .label(&tree.file, name.span, "")
                    .label(&tree.file, first, "first set here"),
                );
                self.add_expr(value, TypeExpectation::Hint(None));
                continue;
            }
            set[index] = Some(name.span);
            let value = self.add_expr(value, TypeExpectation::Required(field_types[index]));
            let index = index as u32;
            checked.push(self.body.add(FieldValue { index, value }, name.span).into());
        }
        let missing: Vec<String> = project::field_names(self.checker.modules, definition)
            .zip(&set)
            .filter(|(_, set)| set.is_none())
            .map(|(name, _)| format!("`{name}`"))
            .collect();
        if !missing.is_empty() {
            let fields = if missing.len() == 1 {
                "field"
            } else {
                "fields"
            };
            let message = format!(
                "missing {fields} {} in `{}`",
                missing.join(", "),
                self.checker.type_text(ty)
            );
            self.checker
                .error(diagnostics::MISSING_FIELD, message, span);
        }
        Expr {
            ty,
            kind: ExprKind::StructLiteral(self.body.children.lists.add(&checked)),
        }
    }

    /// `[N]T{ entries }`, written at `span`, or `[_]T{ entries }`, whose
    /// length is the number of values its entries give: each entry a `T`, or
    /// a spread of a range, an array or a slice (`count_spread`). They must
    /// give exactly its length. A slice, or a range whose count is not known
    /// now, is counted when the program runs, so its literal needs a written
    /// length, which the entries known now must not already pass.
    pub(super) fn check_array_literal(
        &mut self,
        written: Id<ast::Type>,
        entries: List<ast::Expr>,
        span: Span,
    ) -> Expr {
        let tree = &self.checker.module().ast;
        let (element, length) = match tree.types[written] {
            ast::Type::FixedArray {
                length: ast::Length::Inferred(_),
                element,
            } => (self.checker.resolve_value_type(element), None),
            _ => match self.checker.resolve_value_type(written) {
                ty if ty == TypeId::INVALID => (TypeId::INVALID, None),
                ty => match self.checker.types[ty] {
                    Type::Array { element, length } => (element, Some((ty, length))),
                    _ => unreachable!("an array literal's written type is an array type"),
                },
            },
        };
        let (parts, known, later) =
            self.check_entries(tree.children.lists.items(entries), element, None);
        let Some(known) = known.filter(|_| element != TypeId::INVALID) else {
            return Expr::INVALID;
        };
        let written = tree.types.span(written);
        let Some(ty) = self.array_literal_type(written, element, length, (known, later), span)
        else {
            return Expr::INVALID;
        };
        Expr {
            ty,
            kind: ExprKind::ArrayLiteral(self.body.children.lists.add(&parts)),
        }
    }

    /// The `entries` of an array of `element`s, each a value or a spread
    /// (`count_spread`), checked: with how many values are known now, `None`
    /// after an error in a spread, and the first spread not counted until the
    /// program runs, with why. A value that mismatches is reported, with how
    /// to spread it when it holds `element`s (`help_spread`, given `alone_in`).
    /// Without an element type, each entry is checked for its own errors.
    fn check_entries(
        &mut self,
        entries: impl Iterator<Item = Id<ast::Expr>>,
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
                let value = self.add_expr(entry, TypeExpectation::Hint(hint));
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
        written: Id<ast::Expr>,
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
        values: &[Id<ast::Expr>],
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
    /// (`check_array_literal`).
    pub(super) fn array_literal_type(
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
    fn add_spread(&mut self, values: Id<ast::Expr>, element: TypeId) -> Id<Expr> {
        // Only an integer element has ranges, whose ends then take its type.
        let hint = self.checker.types.range(element);
        self.add_expr(values, TypeExpectation::Hint(hint))
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
                // `[&mut]` in a variable is usable after.
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
        let constant = |id: Id<Expr>| match exprs[id].kind {
            ExprKind::Constant(Value::Integer(ref limbs)) => Some(limbs),
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

    /// The range `start..end`, or `..=`, stepping by `start..end:step`, at
    /// `range`: a `range[T]` of its ends' integer type `T`. A literal end
    /// without a suffix takes the other end's type, or `T` when a
    /// `range[T]` is `expected`; the step is of the range type's step type
    /// and not a constant zero (`check_range_step`). A range without both
    /// ends is not supported yet.
    pub(super) fn check_range(&mut self, range: Id<ast::Expr>, expected: Option<TypeId>) -> Expr {
        let tree = &self.checker.module().ast;
        let span = tree.exprs.span(range);
        let ast::Expr::Range {
            inclusive,
            start: Some(start),
            end: Some(end),
            step,
        } = tree.exprs[range]
        else {
            self.checker.not_supported(span, "ranges without both ends");
            return Expr::INVALID;
        };
        let hint = expected.and_then(|ty| match self.checker.types[ty] {
            Type::Range { element, .. } => Some(element),
            _ => None,
        });
        let (start, end) = self.check_range_ends(start, end, hint);
        let ty = self.checker.range_type(self.body.exprs[start].ty, span);
        let step_type = match self.checker.types[ty] {
            Type::Range { step, .. } => step,
            _ => TypeId::INVALID,
        };
        let step = self.check_range_step(step, step_type, span);
        if ty == TypeId::INVALID {
            return Expr::INVALID;
        }
        Expr {
            ty,
            kind: ExprKind::Range {
                start,
                end,
                step,
                inclusive,
            },
        }
    }

    /// A range's or slice's ends `start` and `end`, of one type: a literal
    /// without a suffix takes the other end's type, or `hint` when both are
    /// such literals.
    pub(super) fn check_range_ends(
        &mut self,
        start: Id<ast::Expr>,
        end: Id<ast::Expr>,
        hint: Option<TypeId>,
    ) -> (Id<Expr>, Id<Expr>) {
        let tree = &self.checker.module().ast;
        if unsuffixed_literal(tree, start).is_some() {
            let end = self.add_expr(end, TypeExpectation::Hint(hint));
            let ty = self.body.exprs[end].ty;
            (self.add_expr(start, TypeExpectation::Required(ty)), end)
        } else {
            let start = self.add_expr(start, TypeExpectation::Hint(hint));
            let ty = self.body.exprs[start].ty;
            (start, self.add_expr(end, TypeExpectation::Required(ty)))
        }
    }
}
