//! Checking literals that build a value: strings, `none`, structs, arrays,
//! their spreads, and ranges.

use std::num::NonZeroU32;

use arena::Id;
use arena::List;
use diagnostics::Diagnostic;
use model::ast;
use model::constant::Value;
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
    /// a spread of a range, an array or a slice (`check_spread`). They must
    /// give exactly its length. A slice, or a range whose count is not known
    /// now, is counted when the program runs, so its literal needs a written
    /// length, which the entries known now must not already pass.
    pub(super) fn check_array_literal(
        &mut self,
        written: Id<ast::Type>,
        entries: List<ast::Entry>,
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
        // Without an element type, each part is checked for its own errors.
        let expectation = match element {
            TypeId::INVALID => TypeExpectation::Hint(None),
            element => TypeExpectation::Required(element),
        };
        let mut parts: Vec<NonZeroU32> = Vec::with_capacity(entries.len());
        // The values known now, and the first spread not counted until the
        // program runs, with why `[_]` cannot count it; `None` after an error
        // in a spread. Each count fits a `usize`, so their sum fits a `u128`.
        let (mut known, mut later) = (Some(0u128), None);
        for entry in tree.children.lists.items(entries) {
            let entry_span = tree.children.entries.span(entry);
            let (part, count) = match tree.children.entries[entry] {
                ast::Entry::Value(value) => (self.add_expr(value, expectation), Ok(1)),
                ast::Entry::Spread(values) => {
                    let Some(checked) = self.check_spread(values, element) else {
                        known = None;
                        continue;
                    };
                    checked
                }
            };
            match count {
                Ok(count) => known = known.map(|known| known + u128::from(count)),
                Err(reason) => later = later.or(Some((entry_span, reason))),
            }
            parts.push(part.into());
        }
        let Some(known) = known.filter(|_| element != TypeId::INVALID) else {
            return Expr::INVALID;
        };
        let Some(ty) = self.array_literal_type(written, element, length, (known, later), span)
        else {
            return Expr::INVALID;
        };
        Expr {
            ty,
            kind: ExprKind::ArrayLiteral(self.body.children.lists.add(&parts)),
        }
    }

    /// The type of the array literal written at `span` as `written`, of
    /// `element`s, with `length` when written (`[N]`, as its type), whose
    /// entries give `known` values now and more when the program runs if a
    /// spread, first at `later` with why `[_]` cannot count it, is counted
    /// then; `None` after reporting that they do not fit it
    /// (`check_array_literal`).
    pub(super) fn array_literal_type(
        &mut self,
        written: Id<ast::Type>,
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
                let written = self.checker.module().ast.types.span(written);
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

    /// The spread `values...` in an array literal of `element`s, checked,
    /// with how many values it gives when that is known now, else why `[_]`
    /// cannot count it; `None` after an error. An array gives its elements,
    /// which must be `element`s; a slice copies its elements, which must
    /// copy; a range its values, counted now when it is written with
    /// constant ends and step; a count no array can hold is reported.
    pub(super) fn check_spread(
        &mut self,
        values: Id<ast::Expr>,
        element: TypeId,
    ) -> Option<(Id<Expr>, Result<u64, &'static str>)> {
        let tree = &self.checker.module().ast;
        let values = tree.unparenthesized(values);
        let span = tree.exprs.span(values);
        // Only an integer element has ranges, whose ends then take its type.
        let hint = self.checker.types.range(element);
        let spread = self.add_expr(values, TypeExpectation::Hint(hint));
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
                let reason = "a slice in `[_]` has a length known only when the program runs";
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
        let uncounted = "a range in `[_]` needs constant ends and step";
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
