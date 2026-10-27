//! Checking expressions into THIR. Each expression gets its type, and an
//! operation on values known at compile time is folded to its value.

use std::num::NonZeroU32;

use arena::Id;
use arena::List;
use bigint::Limbs;
use diagnostics::Code;
use diagnostics::Diagnostic;
use diagnostics::fix::Fix;
use diagnostics::suggest::nearest_name;
use model::ast;
use model::ast::Ident;
use model::constant::Constant;
use model::constant::Value;
use model::operators::BinaryOp;
use model::operators::Precedence;
use model::operators::UnaryOp;
use model::semantic_types::Conversion;
use model::semantic_types::Float;
use model::semantic_types::Type;
use model::semantic_types::TypeId;
use model::semantic_types::Types;
use model::thir::Expr;
use model::thir::ExprKind;
use model::thir::FactKind;
use model::thir::FieldValue;
use model::thir::Local;
use model::thir::Symbol;
use model::thir::Variable;
use source::Span;

use crate::builtin::UNSUPPORTED_FUNCTIONS;
use crate::scope;
use crate::statement::ThirBuilder;

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
        let checker = &self.checker;
        let message = format!(
            "mismatched types: expected `{}`, found `{}`",
            checker.type_text(to),
            checker.type_text(found)
        );
        let types = &checker.types;
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
            Conversion::Reborrow => return Some(self.reborrow(expr, span, false)),
            Conversion::Never => return None,
        };
        Some(Expr { ty: to, kind })
    }

    /// The string literal at `span` as a `cstr`: its bytes.
    pub(crate) fn c_string(&mut self, span: Span) -> Expr {
        self.checker.record(span, FactKind::Literal(TypeId::CSTR));
        let bytes = literal::value(self.checker.module().ast.file.slice(span));
        Expr {
            ty: TypeId::CSTR,
            kind: ExprKind::Constant(Value::Bytes(bytes.into())),
        }
    }

    /// `print(values)`, or `println(values)` when `newline`: each value of a
    /// type `printable` takes. A plain string literal is printed as a `cstr`,
    /// though it is not one anywhere else.
    // shortcut: a built-in until a library function takes any number of
    // values of any printable type; then delete it with THIR's and MIR's
    // `Print`.
    fn check_print(&mut self, newline: bool, values: List<ast::Expr>) -> Expr {
        let tree = &self.checker.module().ast;
        let mut checked: Vec<NonZeroU32> = Vec::with_capacity(values.len());
        for value in tree.lists.items(values) {
            let span = tree.exprs.span(value);
            let expr = match tree.exprs[value] {
                ast::Expr::String { c_string: false } => self.c_string(span),
                _ => self.check_expr(value, TypeExpectation::Hint(None)),
            };
            let ty = expr.ty;
            let types = &self.checker.types;
            // On an error the others are still checked, for their own.
            if !printable(types, ty) {
                if ty != TypeId::INVALID {
                    let message = format!("`{}` cannot be printed", self.checker.type_text(ty));
                    self.checker
                        .error(diagnostics::INVALID_OPERATION, message, span);
                }
                continue;
            }
            let mut printed = self.body.add(expr, span);
            // Printing reads a value, never takes it: one that moves, in a
            // place, is lent instead, a `&mut` reference as `&`.
            if !types.is_copy(ty) && self.place_root(printed).is_some() {
                let lent = match types[ty] {
                    Type::Reference { .. } => {
                        let reference = self.body.exprs[printed].clone();
                        self.reborrow(reference, span, false)
                    }
                    _ => self.check_borrow(false, printed, span),
                };
                printed = self.body.add(lent, span);
            }
            checked.push(printed.into());
        }
        if checked.len() < values.len() {
            return Expr::INVALID;
        }
        Expr {
            ty: TypeId::VOID,
            kind: ExprKind::Print {
                newline,
                values: self.body.children.lists.add(&checked),
            },
        }
    }

    /// `value as ty`: `value` as a value of the type `ty`, which its type
    /// converts to with `as` or without it (`Types::conversion`). A number
    /// literal takes the type `ty` if it can, so `300 as u8` is an error.
    fn check_cast(&mut self, value: Id<ast::Expr>, ty: Id<ast::Type>, span: Span) -> Expr {
        let to = self.checker.resolve_value_type(ty);
        let checked = self.check_expr(value, TypeExpectation::Hint(Some(to)));
        if checked.ty == TypeId::INVALID || to == TypeId::INVALID {
            return Expr::INVALID;
        }
        let value_span = self.checker.module().ast.exprs.span(value);
        let found = checked.ty;
        let conversion = self.checker.types.conversion(found, to);
        if let Some(converted) = self.convert(checked, value_span, to, conversion) {
            return converted;
        }
        let checker = &self.checker;
        let message = format!(
            "`{}` cannot be converted to `{}`",
            checker.type_text(found),
            checker.type_text(to)
        );
        let types = &checker.types;
        let diagnostic = self.type_error(
            diagnostics::INVALID_CAST,
            message,
            span,
            value,
            found,
            |ty| types.conversion(ty, to) != Conversion::Never,
        );
        self.checker.report(diagnostic);
        Expr::INVALID
    }

    /// `none`, written at `span`: the null raw pointer of the type
    /// `expected`, which must be a raw pointer type.
    fn check_none(&mut self, span: Span, expected: Option<TypeId>) -> Expr {
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
                    let symbol = self.resolve_path(path);
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
                ast::Expr::Index { value, index } => break 'checked self.check_index(value, index),
                ast::Expr::Dereference(value) => {
                    break 'checked self.check_dereference(value, span);
                }
                ast::Expr::Reference { mutable, value } => {
                    let place = self.add_expr(value, TypeExpectation::Hint(None));
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
        let TypeExpectation::Required(ty) = expectation else {
            return checked;
        };
        let found = checked.ty;
        self.coerce(checked, expression, ty)
            .unwrap_or_else(|mismatch| {
                let mismatch = self.help_cast(mismatch, expression, found, ty);
                self.checker.report(mismatch);
                Expr::INVALID
            })
    }

    /// The name or path written at `span`, which stands for `symbol`, as a
    /// value. `None` when finding the symbol reported an error.
    fn check_symbol_value(&mut self, symbol: Option<Symbol>, span: Span) -> Expr {
        let what = match symbol {
            None => return Expr::INVALID, // finding it reported the error
            Some(Symbol::Local(Local::Variable(variable))) => {
                return Expr {
                    ty: self.body.variables[variable].ty,
                    kind: ExprKind::Variable(variable),
                };
            }
            Some(Symbol::Local(Local::Constant(constant))) => {
                return constant_expr(self.body.constants[constant].clone());
            }
            Some(Symbol::Constant(constant)) => {
                return constant_expr(self.checker.constant(constant, span));
            }
            Some(Symbol::Function(_)) => {
                self.checker.not_supported(span, "functions as values");
                return Expr::INVALID;
            }
            Some(Symbol::Type(_) | Symbol::Builtin(_)) => "a type",
            Some(Symbol::Module(_)) => "a module",
        };
        let text = self.checker.module().ast.file.slice(span);
        let message = format!("`{text}` is {what}, not a value");
        self.checker
            .error(diagnostics::INVALID_OPERATION, message, span);
        Expr::INVALID
    }

    /// Reports `name`, written at `span`, which nothing in scope declares:
    /// a built-in function, not supported yet, or an unknown name, with the
    /// nearest name that is declared.
    pub(crate) fn report_undeclared(&mut self, name: &str, span: Span) {
        if UNSUPPORTED_FUNCTIONS.contains(&name) {
            self.checker.not_supported(span, "built-in functions");
            return;
        }
        let locals = self.names.iter().map(|&(declared, _)| declared);
        let globals = self.checker.visible_symbols().map(|(declared, _)| declared);
        let meant = scope::nearest(name, locals, globals);
        let message = format!("unknown name `{name}`");
        self.checker
            .report_unknown(diagnostics::UNDEFINED_NAME, span, message, meant);
    }

    /// `Type.{ name = value, ... }`, written at `span`, or `.{ ... }` taking
    /// the struct type `expected`: a struct value with each field set once.
    /// A field that is unknown, set twice or missing is reported.
    fn check_struct_literal(
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
            for field in tree.lists.items(fields) {
                self.add_expr(tree.field_values[field].value, TypeExpectation::Hint(None)); // for its own errors
            }
            return Expr::INVALID;
        };
        let field_types = field_types.clone();
        // Where each field is set, by its index.
        let mut set: Vec<Option<Span>> = vec![None; field_types.len()];
        let mut checked: Vec<NonZeroU32> = Vec::with_capacity(fields.len());
        for field in tree.lists.items(fields) {
            let ast::FieldValue { name, value } = tree.field_values[field];
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

    /// `value.field`: the field of a struct value, or of the struct a
    /// reference or typed raw pointer points to; or `len`, an array's only
    /// field, which its type gives, so the array is not read.
    fn check_field(&mut self, value: Id<ast::Expr>, field: Ident) -> Expr {
        let mut value = self.add_expr(value, TypeExpectation::Hint(None));
        let mut ty = self.body.exprs[value].ty;
        let pointee = self.checker.types[ty].pointee();
        // shortcut: `len` as a field until the language can write it as a
        // free function over any array; then move it there.
        if let Type::Array { length, .. } = self.checker.types[pointee.unwrap_or(ty)] {
            let text = field.text(&self.checker.module().ast.file);
            if text != "len" {
                let array = self.checker.type_text(pointee.unwrap_or(ty));
                let message = format!("`{array}` has no field `{text}`");
                let meant = nearest_name(text, [("len", 0)]).map(str::to_string);
                self.checker
                    .report_unknown(diagnostics::UNKNOWN_FIELD, field.span, message, meant);
                return Expr::INVALID;
            }
            let usize = self.checker.usize_type();
            if self.body.calls(value) {
                return Expr {
                    ty: usize,
                    kind: ExprKind::Length(value),
                };
            }
            let length = Limbs::new(self.checker.pointer_bits, &[length]);
            return Expr {
                ty: usize,
                kind: ExprKind::Constant(Value::Integer(length)),
            };
        }
        if let Some(target) = pointee {
            let referred = self.dereference(value, target);
            ty = referred.ty;
            value = self.body.add(referred, self.body.exprs.span(value));
        }
        let Type::Struct { ref fields, .. } = self.checker.types[ty] else {
            if ty != TypeId::INVALID {
                let message = format!("`{}` has no fields", self.checker.type_text(ty));
                self.checker
                    .error(diagnostics::INVALID_OPERATION, message, field.span);
            }
            return Expr::INVALID;
        };
        let fields = fields.clone();
        let Some(index) = self.field_index(ty, field) else {
            return Expr::INVALID;
        };
        Expr {
            ty: fields[index],
            kind: ExprKind::Field {
                value,
                index: index as u32,
            },
        }
    }

    /// `[N]T{ entries }`, written at `span`, or `[_]T{ entries }`, whose
    /// length is the number of values its entries give: each entry a `T`, or
    /// a spread of a range or an array (`check_spread`). They must give
    /// exactly its length. A range whose count is not known now is counted
    /// when the program runs, so its literal needs a written length, which
    /// the entries known now must not already pass.
    fn check_array_literal(
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
        // The values known now, and the first range not counted until the
        // program runs; `None` after an error in a spread. Each count fits a
        // `usize`, so their sum fits a `u128`.
        let (mut known, mut later) = (Some(0u128), None);
        for entry in tree.lists.items(entries) {
            let entry_span = tree.entries.span(entry);
            let (part, count) = match tree.entries[entry] {
                ast::Entry::Value(value) => (self.add_expr(value, expectation), Some(1)),
                ast::Entry::Spread(values) => {
                    let Some(checked) = self.check_spread(values, element) else {
                        known = None;
                        continue;
                    };
                    checked
                }
            };
            match count {
                Some(count) => known = known.map(|known| known + u128::from(count)),
                None => later = later.or(Some(entry_span)),
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
    /// range, first at `later`, is counted then; `None` after reporting
    /// that they do not fit it (`check_array_literal`).
    fn array_literal_type(
        &mut self,
        written: Id<ast::Type>,
        element: TypeId,
        length: Option<(TypeId, u64)>,
        (known, later): (u128, Option<Span>),
        span: Span,
    ) -> Option<TypeId> {
        match (length, later) {
            (None, Some(range)) => {
                let message = "a range in `[_]` needs constant ends and step; write the length";
                self.checker
                    .error(diagnostics::TYPE_MISMATCH, message, range);
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
    /// with how many values it gives when that is known now; `None` after
    /// an error. An array gives its elements, which must be
    /// `element`s; a range its values, counted now when it is written with
    /// constant ends and step; a count no array can hold is reported.
    fn check_spread(
        &mut self,
        values: Id<ast::Expr>,
        element: TypeId,
    ) -> Option<(Id<Expr>, Option<u64>)> {
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
                return Some((spread, Some(length)));
            }
            Type::Range { element: from, .. }
                if element == TypeId::INVALID || types.same(from, element) => {}
            _ => {
                let message = format!(
                    "`...` here spreads a range or an array of `{}`, found `{}`",
                    self.checker.type_text(element),
                    self.checker.type_text(ty)
                );
                self.checker
                    .error(diagnostics::TYPE_MISMATCH, message, span);
                return None;
            }
        }
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
            return Some((spread, None)); // a range value, counted when the program runs
        };
        let (Some(first), Some(last), Some(by)) = (constant(start), constant(end), constant(step))
        else {
            return Some((spread, None));
        };
        let Type::Integer(integer) = types[exprs[start].ty] else {
            unreachable!("a range is of integers")
        };
        let count = integer.range_count(first, last, by, inclusive)?; // a zero step was reported
        if !self.checker.length_fits(count.as_slice(), span) {
            return None;
        }
        Some((spread, Some(count.as_slice()[0]))) // it fits a `usize`
    }

    /// The range `start..end`, or `..=`, stepping by `start..end:step`, at
    /// `range`: a `range[T]` of its ends' integer type `T`. A literal end
    /// without a suffix takes the other end's type, or `T` when a
    /// `range[T]` is `expected`; the step is of the range type's step type
    /// and not a constant zero (`check_range_step`). A range without both
    /// ends is not supported yet.
    fn check_range(&mut self, range: Id<ast::Expr>, expected: Option<TypeId>) -> Expr {
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
        let (start, end) = if unsuffixed_literal(tree, start).is_some() {
            let end = self.add_expr(end, TypeExpectation::Hint(hint));
            let ty = self.body.exprs[end].ty;
            (self.add_expr(start, TypeExpectation::Required(ty)), end)
        } else {
            let start = self.add_expr(start, TypeExpectation::Hint(hint));
            let ty = self.body.exprs[start].ty;
            (start, self.add_expr(end, TypeExpectation::Required(ty)))
        };
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

    /// `array[index]`: the element at an integer index of an array in a
    /// place, or of the array a reference or typed raw pointer points to. A
    /// constant index must be inside the array.
    fn check_index(&mut self, array: Id<ast::Expr>, index: Id<ast::Expr>) -> Expr {
        let mut array = self.add_expr(array, TypeExpectation::Hint(None));
        let mut ty = self.body.exprs[array].ty;
        if let Some(target) = self.checker.types[ty].pointee() {
            let referred = self.dereference(array, target);
            ty = referred.ty;
            array = self.body.add(referred, self.body.exprs.span(array));
        }
        let usize = self.checker.usize_type();
        let index = self.add_expr(index, TypeExpectation::Hint(Some(usize)));
        let index_type = self.body.exprs[index].ty;
        let tree = &self.checker.module().ast;
        let index_span = self.body.exprs.span(index);
        let Type::Array { element, length } = self.checker.types[ty] else {
            if ty != TypeId::INVALID {
                let message = format!("`{}` cannot be indexed", self.checker.type_text(ty));
                let span = self.body.exprs.span(array);
                self.checker
                    .error(diagnostics::INVALID_OPERATION, message, span);
            }
            return Expr::INVALID;
        };
        let Type::Integer(integer) = self.checker.types[index_type] else {
            if index_type != TypeId::INVALID {
                let message = format!(
                    "an index must be an integer, found `{}`",
                    self.checker.type_text(index_type)
                );
                self.checker
                    .error(diagnostics::TYPE_MISMATCH, message, index_span);
            }
            return Expr::INVALID;
        };
        if let ExprKind::Constant(Value::Integer(ref value)) = self.body.exprs[index].kind
            && bigint::to_u64(integer.bits, integer.signed, value).is_none_or(|at| at >= length)
        {
            let message = format!(
                "index `{}` is outside `{}`, whose length is {length}",
                tree.file.slice(index_span),
                self.checker.type_text(ty)
            );
            self.checker
                .error(diagnostics::INVALID_OPERATION, message, index_span);
            return Expr::INVALID;
        }
        if self.place_root(array).is_none() {
            // shortcut: an element is read through the array's address, so
            // from a place; an array value gets one when a temporary can.
            let span = self.body.exprs.span(array);
            self.checker
                .not_supported(span, "indexes of arrays that are not in a variable");
            return Expr::INVALID;
        }
        Expr {
            ty: element,
            kind: ExprKind::Index { array, index },
        }
    }

    /// `reference.*`, written at `span`: the value a reference or a typed
    /// raw pointer points to.
    fn check_dereference(&mut self, reference: Id<ast::Expr>, span: Span) -> Expr {
        let reference = self.add_expr(reference, TypeExpectation::Hint(None));
        let ty = self.body.exprs[reference].ty;
        match self.checker.types[ty] {
            ref pointer if let Some(target) = pointer.pointee() => {
                self.dereference(reference, target)
            }
            Type::Invalid => Expr::INVALID,
            _ => {
                let message = format!(
                    "`.*` needs a reference or a typed raw pointer, found `{}`",
                    self.checker.type_text(ty)
                );
                self.checker
                    .error(diagnostics::INVALID_OPERATION, message, span);
                Expr::INVALID
            }
        }
    }

    /// What `reference`, whose type refers to `target`, refers to: a place.
    fn dereference(&mut self, reference: Id<Expr>, target: TypeId) -> Expr {
        if self.place_root(reference).is_none() {
            // shortcut: lowering takes a place only from a variable; a call's
            // returned reference needs one when references are returned.
            let span = self.body.exprs.span(reference);
            self.checker
                .not_supported(span, "references that are not in a variable");
            return Expr::INVALID;
        }
        Expr {
            ty: target,
            kind: ExprKind::Dereference(reference),
        }
    }

    /// A `&place` or `&mut place` reference to the checked place `place`, in a
    /// variable, reported at `span`. `&mut` needs a place that can be written.
    fn check_borrow(&mut self, mutable: bool, place: Id<Expr>, span: Span) -> Expr {
        let target = self.body.exprs[place].ty;
        let ty = self.checker.reference_type(mutable, target, span);
        if ty == TypeId::INVALID {
            return Expr::INVALID;
        }
        let Some((variable, reference)) = self.place_root(place) else {
            self.checker
                .not_supported(span, "borrows of values that are not in a variable");
            return Expr::INVALID;
        };
        if mutable {
            self.report_unwritable(place, variable, reference, "mutably borrow");
        }
        Expr {
            ty,
            kind: ExprKind::Borrow(place),
        }
    }

    /// `@place`, written at `span`: the address of the checked place
    /// `place`, in a variable, which must be a place that can be written.
    fn check_address(&mut self, place: Id<Expr>, span: Span) -> Expr {
        let target = self.body.exprs[place].ty;
        let ty = self.checker.raw_pointer_type(target, span);
        if ty == TypeId::INVALID {
            return Expr::INVALID;
        }
        let Some((variable, reference)) = self.place_root(place) else {
            self.checker
                .not_supported(span, "addresses of values that are not in a variable");
            return Expr::INVALID;
        };
        self.report_unwritable(place, variable, reference, "take the address of");
        Expr {
            ty,
            kind: ExprKind::Address(place),
        }
    }

    /// The variable that the place `place` starts from, and the pointer on
    /// its path that decides whether it can be written: up to the nearest
    /// raw pointer, a shared reference if any, else the nearest reference or
    /// that raw pointer. `None` for a value not in a variable.
    pub(crate) fn place_root(&self, place: Id<Expr>) -> Option<(Id<Variable>, Option<Id<Expr>>)> {
        let pointer = |reference: Id<Expr>| &self.checker.types[self.body.exprs[reference].ty];
        let shared =
            |reference| matches!(pointer(reference), Type::Reference { mutable: false, .. });
        let mut root = place;
        let mut deciding = None;
        // Behind a raw pointer, which can always be written, what holds the
        // pointer decides nothing.
        let mut behind_raw = false;
        loop {
            match self.body.exprs[root].kind {
                ExprKind::Field { value, .. } | ExprKind::Index { array: value, .. } => {
                    root = value;
                }
                ExprKind::Dereference(reference) => {
                    if !behind_raw && deciding.is_none_or(|kept| !shared(kept) && shared(reference))
                    {
                        deciding = Some(reference);
                    }
                    behind_raw |= matches!(pointer(reference), Type::RawPointer { .. });
                    root = reference;
                }
                ExprKind::Variable(variable) => return Some((variable, deciding)),
                _ => return None,
            }
        }
    }

    /// Reports `action` (`"assign to"`, `"mutably borrow"`) on the place
    /// `place`, which starts from `variable`, where it cannot write the
    /// place: through `reference` only when that is a `&mut`, else in the
    /// variable's own storage only when it is `mut`.
    pub(crate) fn report_unwritable(
        &mut self,
        place: Id<Expr>,
        variable: Id<Variable>,
        reference: Option<Id<Expr>>,
        action: &str,
    ) {
        let file = &self.checker.module().ast.file;
        let place_span = self.body.exprs.span(place);
        let place_text = file.slice(place_span);
        let (written, declared, label, help) = if let Some(reference) = reference {
            let ty = self.body.exprs[reference].ty;
            if let Type::Reference { mutable: true, .. } | Type::RawPointer { .. } =
                self.checker.types[ty]
            {
                return;
            }
            let span = self.body.exprs.span(reference);
            let declared = match self.body.exprs[reference].kind {
                ExprKind::Variable(variable) => self.body.variables.span(variable),
                _ => span,
            };
            let holder = file.slice(span);
            let written = format!("`{place_text}` through shared reference `{holder}`");
            let help = format!("make `{holder}` a `&mut` reference");
            let label = "declared as a shared reference here";
            (written, declared, label, help)
        } else {
            if self.body.variables[variable].mutable {
                return;
            }
            let declared = self.body.variables.span(variable);
            let holder = file.slice(declared);
            let written = match self.body.exprs[place].kind {
                ExprKind::Variable(_) => format!("immutable `{holder}`"),
                _ => format!("`{place_text}` of immutable `{holder}`"),
            };
            let help = format!("write `let mut {holder}`");
            let label = "declared without `mut` here";
            (written, declared, label, help)
        };
        let message = format!("cannot {action} {written}");
        self.checker.report(
            Diagnostic::new(diagnostics::INVALID_ASSIGNMENT, message)
                .label(file, place_span, "")
                .label(file, declared, label)
                .help(help, Vec::new()),
        );
    }

    /// The index of the field `name` of the struct type `ty`, in declaration
    /// order; `None` after reporting that the struct has no such field, with
    /// its nearest field name.
    fn field_index(&mut self, ty: TypeId, name: Ident) -> Option<usize> {
        let Type::Struct { definition, .. } = self.checker.types[ty] else {
            unreachable!("only a struct type has fields")
        };
        let file = &self.checker.module().ast.file;
        let text = name.text(file);
        let modules = self.checker.modules;
        let found = project::field_names(modules, definition).position(|field| field == text);
        if found.is_none() {
            let message = format!("`{}` has no field `{text}`", self.checker.type_text(ty));
            let fields = project::field_names(modules, definition).map(|field| (field, 0));
            let meant = nearest_name(text, fields).map(str::to_string);
            self.checker
                .report_unknown(diagnostics::UNKNOWN_FIELD, name.span, message, meant);
        }
        found
    }

    /// What the path `module::name` written as the type expression `path`
    /// stands for, reporting a path that names nothing or is not supported.
    /// Another module's name is reachable only if it starts with an upper case
    /// letter, except in the prelude.
    fn resolve_path(&mut self, path: Id<ast::Type>) -> Option<Symbol> {
        let tree = &self.checker.module().ast;
        let span = tree.types.span(path);
        // A `Type::Generic` alone, as `Box<T>`, is one segment.
        let segments: Vec<Id<ast::Type>> = match tree.types[path] {
            ast::Type::Path(segments) => tree.lists.items(segments).collect(),
            _ => vec![path],
        };
        if !segments
            .iter()
            .all(|&segment| matches!(tree.types[segment], ast::Type::Name))
        {
            self.checker
                .not_supported(span, "paths with type arguments");
            return None;
        }
        let [module, member] = segments[..] else {
            self.checker
                .not_supported(span, "paths of more than two names");
            return None;
        };
        let (module_span, member_span) = (tree.types.span(module), tree.types.span(member));
        let (module_name, member_name) =
            (tree.file.slice(module_span), tree.file.slice(member_span));
        let imported = match self.checker.global(module_name) {
            Some(symbol @ Symbol::Module(imported)) => {
                self.checker.record(module_span, FactKind::Name(symbol));
                imported
            }
            Some(Symbol::Type(_)) => {
                self.checker.not_supported(span, "paths into types");
                return None;
            }
            Some(_) => {
                let message = format!("`{module_name}` is not a module");
                self.checker
                    .error(diagnostics::INVALID_OPERATION, message, module_span);
                return None;
            }
            None => {
                self.report_undeclared(module_name, module_span);
                return None;
            }
        };
        let members = &self.checker.module_checks[imported.index()].scope;
        let Some(symbol) = members.get(member_name) else {
            let exported = members.symbols().map(|(declared, _)| declared);
            let meant = scope::nearest(
                member_name,
                std::iter::empty(),
                exported.filter(|declared| scope::is_exported(declared)),
            );
            let message = format!("unknown name `{member_name}` in module `{module_name}`");
            self.checker
                .report_unknown(diagnostics::UNDEFINED_NAME, member_span, message, meant);
            return None;
        };
        self.checker.record(member_span, FactKind::Name(symbol));
        if !scope::is_exported(member_name) {
            let file = &tree.file;
            self.checker.report(
                Diagnostic::new(
                    diagnostics::NOT_EXPORTED,
                    format!("`{member_name}` is not exported from `{module_name}`"),
                )
                .label(file, member_span, "")
                .note(
                    "names that start with an upper case letter are exported; others are private",
                ),
            );
            return None;
        }
        Some(symbol)
    }

    /// `callee(arguments)`, written at `span`. Only a module's function, by
    /// its name or its path, can be called.
    fn check_call(
        &mut self,
        callee: Id<ast::Expr>,
        arguments: List<ast::Expr>,
        span: Span,
    ) -> Expr {
        let tree = &self.checker.module().ast;
        let callee_span = tree.exprs.span(callee);
        let text = tree.file.slice(callee_span);
        let symbol = match tree.exprs[callee] {
            ast::Expr::Name => match self.resolve(callee_span) {
                // Their errors were reported.
                Some(Symbol::Local(Local::Constant(constant)))
                    if self.body.constants[constant].is_none() =>
                {
                    None
                }
                Some(Symbol::Local(Local::Variable(variable)))
                    if self.body.variables[variable].ty == TypeId::INVALID =>
                {
                    None
                }
                symbol => symbol,
            },
            ast::Expr::Path(path) => self.resolve_path(path),
            ast::Expr::Selector { .. } => {
                self.checker.not_supported(callee_span, "method calls");
                None
            }
            _ => {
                self.checker
                    .not_supported(callee_span, "calls of computed functions");
                None
            }
        };
        let function = match symbol {
            Some(Symbol::Function(function)) => Some(function),
            Some(_) => {
                let message = format!("`{text}` is not a function");
                self.checker
                    .error(diagnostics::NOT_CALLABLE, message, callee_span);
                None
            }
            None => None,
        };
        let signature = function.map(|function| {
            let ty = self.checker.function_type(function, callee_span);
            (function, self.checker.types[ty].clone())
        });
        let Some((
            function,
            Type::Function {
                parameters, result, ..
            },
        )) = signature
        else {
            for argument in tree.lists.items(arguments) {
                self.add_expr(argument, TypeExpectation::Hint(None)); // for its own errors
            }
            return Expr::INVALID;
        };
        let arity_matches = parameters.len() == arguments.len();
        if !arity_matches {
            let message = format!(
                "`{text}` takes {} argument{}, but {} {} given",
                parameters.len(),
                if parameters.len() == 1 { "" } else { "s" },
                arguments.len(),
                if arguments.len() == 1 { "was" } else { "were" },
            );
            self.checker
                .error(diagnostics::WRONG_ARGUMENT_COUNT, message, span);
        }
        let mut checked: Vec<NonZeroU32> = Vec::with_capacity(arguments.len());
        for (index, argument) in tree.lists.items(arguments).enumerate() {
            let argument = match parameters.get(index) {
                Some(&parameter) => self.add_expr(argument, TypeExpectation::Required(parameter)),
                None => self.add_expr(argument, TypeExpectation::Hint(None)),
            };
            self.lend_variable_again(argument);
            checked.push(argument.into());
        }
        if !arity_matches {
            return Expr::INVALID;
        }
        let arguments = self.body.children.lists.add(&checked);
        // In a constant's value, a call on constants runs now.
        let constant_arguments = || {
            let values = self.body.children.lists.items(arguments).map(|argument| {
                match self.body.exprs[argument].kind {
                    ExprKind::Constant(ref value) => Some(value.clone()),
                    _ => None,
                }
            });
            values.collect::<Option<Vec<Value>>>()
        };
        if self.in_constant
            && result != TypeId::VOID
            && let Some(values) = constant_arguments()
        {
            return match self.checker.evaluate_call(function, &values, span) {
                Some(value) => Expr {
                    ty: result,
                    kind: ExprKind::Constant(value),
                },
                None => Expr::INVALID,
            };
        }
        Expr {
            ty: result,
            kind: ExprKind::Call {
                function,
                arguments,
            },
        }
    }

    /// The number literal at `number`, or its negation written at `negated`
    /// (`-128`, which fits `i8` where `128` does not). It takes its suffix's
    /// type, else the type `expected` if it can (`takes_type`), else `i32` or
    /// `f64`. Its type is recorded even when its value does not fit.
    fn check_number(
        &mut self,
        number: Span,
        negated: Option<Span>,
        expected: Option<TypeId>,
    ) -> Expr {
        let tree = &self.checker.module().ast;
        let literal = literal::numeric::split(tree.file.slice(number));
        let pointer_bits = self.checker.pointer_bits;
        let types = &mut self.checker.types;
        let ty = if literal.suffix.is_empty() {
            match expected {
                Some(ty) if takes_type(&literal, &types[ty]) => ty,
                _ if literal.is_float() => TypeId::F64,
                _ => TypeId::I32,
            }
        } else if let Some(ty) = types.builtin(literal.suffix, pointer_bits) {
            ty
        } else {
            let message = format!("unsupported numeric literal suffix `{}`", literal.suffix);
            self.checker
                .error(diagnostics::INVALID_NUMBER_SUFFIX, message, number);
            return Expr::INVALID;
        };
        self.checker.record(number, FactKind::Literal(ty));
        let negative = negated.is_some();
        let value = match self.checker.types[ty] {
            Type::Integer(integer) => {
                let magnitude = literal.integer_value();
                integer.fits(&magnitude, negative).then(|| {
                    let value = Limbs::new(integer.bits, &magnitude);
                    Value::Integer(if negative {
                        bigint::negate(integer.bits, &value)
                    } else {
                        value
                    })
                })
            }
            Type::Float(float) => {
                let value = match float {
                    Float::F32 => literal.float_value::<f32>().map(f64::from),
                    Float::F64 => literal.float_value::<f64>(),
                };
                value.map(|value| Value::Float(if negative { -value } else { value }.to_bits()))
            }
            _ => unreachable!("a number literal's type is a number type"),
        };
        let Some(value) = value else {
            let written = negated.unwrap_or(number);
            let message = format!(
                "literal `{}` does not fit `{}`",
                tree.file.slice(written),
                self.checker.type_text(ty)
            );
            self.checker
                .error(diagnostics::INVALID_NUMBER, message, written);
            return Expr::INVALID;
        };
        Expr {
            ty,
            kind: ExprKind::Constant(value),
        }
    }

    /// `op operand`, written at `span`.
    fn check_unary(
        &mut self,
        op: UnaryOp,
        operand: Id<ast::Expr>,
        span: Span,
        expected: Option<TypeId>,
    ) -> Expr {
        let tree = &self.checker.module().ast;
        let operand_span = tree.exprs.span(operand);
        // Only a literal right after `-` is a negative literal: `-(128)`
        // negates `128`, which does not fit `i8`.
        if op == UnaryOp::Negate && matches!(tree.exprs[operand], ast::Expr::Number) {
            return self.check_number(operand_span, Some(span), expected);
        }
        let checked = match op {
            UnaryOp::LogicalNot => {
                self.check_expr(operand, TypeExpectation::Hint(Some(TypeId::BOOL)))
            }
            _ => self.check_expr(operand, TypeExpectation::Hint(expected)),
        };
        if checked.ty == TypeId::INVALID {
            return Expr::INVALID;
        }
        let ty = self.checker.types[checked.ty].clone();
        let fits = |ty: &Type| match op {
            UnaryOp::Negate => ty.is_number(),
            UnaryOp::LogicalNot => *ty == Type::Bool,
            UnaryOp::BitNot => matches!(ty, Type::Integer(_)),
        };
        if !fits(&ty) {
            return self.report_invalid_operand(op.symbol(), checked.ty, operand, span, fits);
        }
        let result = checked.ty;
        let kind = match checked.kind {
            ExprKind::Constant(constant) => ExprKind::Constant(
                eval::unary(op, &ty, &constant).expect("unary operators do not trap"),
            ),
            kind => ExprKind::Unary {
                op,
                value: self.body.add(Expr { ty: result, kind }, operand_span),
            },
        };
        Expr { ty: result, kind }
    }

    /// `left op right`, written at `span`. The operation has the left
    /// operand's type, and the right operand is converted to it, except that a
    /// shift count keeps its own integer type. The left operand takes the type
    /// the whole expression is expected to have, unless the operator gives a
    /// `bool` from other types (`==`, `<`), and the right operand takes the
    /// left one's.
    fn check_binary(
        &mut self,
        op: BinaryOp,
        left_operand: Id<ast::Expr>,
        right_operand: Id<ast::Expr>,
        span: Span,
        expected: Option<TypeId>,
    ) -> Expr {
        let tree = &self.checker.module().ast;
        let (left_span, right_span) = (
            tree.exprs.span(left_operand),
            tree.exprs.span(right_operand),
        );
        let comparison = matches!(
            op,
            BinaryOp::EqualTo
                | BinaryOp::NotEqualTo
                | BinaryOp::LessThan
                | BinaryOp::LessOrEqual
                | BinaryOp::GreaterThan
                | BinaryOp::GreaterOrEqual
        );
        // `none` takes its type from the other operand, so `none == p` is
        // checked as `p == none`: the same result, and `none` does nothing.
        if matches!(op, BinaryOp::EqualTo | BinaryOp::NotEqualTo)
            && matches!(tree.exprs[left_operand], ast::Expr::None)
            && !matches!(tree.exprs[right_operand], ast::Expr::None)
        {
            return self.check_binary(op, right_operand, left_operand, span, expected);
        }
        let shift = matches!(op, BinaryOp::ShiftLeft | BinaryOp::ShiftRight);
        let left_literal = unsuffixed_literal(tree, left_operand);
        let left = self.check_expr(
            left_operand,
            TypeExpectation::Hint(if comparison { None } else { expected }),
        );
        let left_type = self.checker.types[left.ty].clone();
        let fits = |ty: &Type| match op {
            BinaryOp::Add
            | BinaryOp::Subtract
            | BinaryOp::Multiply
            | BinaryOp::Divide
            | BinaryOp::Remainder
            | BinaryOp::LessThan
            | BinaryOp::LessOrEqual
            | BinaryOp::GreaterThan
            | BinaryOp::GreaterOrEqual => ty.is_number(),
            BinaryOp::ShiftLeft
            | BinaryOp::ShiftRight
            | BinaryOp::BitAnd
            | BinaryOp::BitOr
            | BinaryOp::BitXor => matches!(ty, Type::Integer(_)),
            BinaryOp::EqualTo | BinaryOp::NotEqualTo => {
                ty.is_number() || matches!(ty, Type::Bool | Type::RawPointer { .. })
            }
            BinaryOp::LogicalAnd | BinaryOp::LogicalOr => *ty == Type::Bool,
        };
        let left_fits = fits(&left_type);
        // Matching the right operand to a left one the operator rejects would
        // blame the right one.
        let right = if shift || !left_fits {
            self.check_expr(right_operand, TypeExpectation::Hint(None))
        } else {
            let checked = self.check_expr(right_operand, TypeExpectation::Hint(Some(left.ty)));
            let found = checked.ty;
            match self.coerce(checked, right_operand, left.ty) {
                Ok(right) => right,
                Err(mut mismatch) => {
                    let checker = &self.checker;
                    let fixable = left_literal.filter(|&literal| {
                        takes_type(&literal::numeric::split(literal), &checker.types[found])
                    });
                    if let Some(literal) = fixable {
                        // The suffix names the type itself, not an alias.
                        let found = checker.type_text(checker.types.real(found));
                        let help =
                            format!("write `{literal}{found}` to give the left literal that type");
                        mismatch = mismatch.help(help, Vec::new());
                    } else {
                        mismatch = self.help_cast(mismatch, right_operand, found, left.ty);
                    }
                    self.checker.report(mismatch);
                    Expr::INVALID
                }
            }
        };
        if left.ty == TypeId::INVALID || right.ty == TypeId::INVALID {
            return Expr::INVALID;
        }
        if !left_fits {
            return self.report_invalid_operand(op.symbol(), left.ty, left_operand, span, fits);
        }
        let right_type = self.checker.types[right.ty].clone();
        if shift && !matches!(right_type, Type::Integer(_)) {
            let count = self.checker.type_text(right.ty);
            let message = format!("a shift count must be an integer, found `{count}`");
            let types = &self.checker.types;
            let diagnostic = self.type_error(
                diagnostics::INVALID_OPERATION,
                message,
                right_span,
                right_operand,
                right.ty,
                |ty| matches!(types[ty], Type::Integer(_)),
            );
            self.checker.report(diagnostic);
            return Expr::INVALID;
        }
        if let ExprKind::Constant(ref value) = right.kind
            && let Some(trap) = eval::known_trap(op, &left_type, &right_type, value)
        {
            let message = eval::trap_message(trap, &left_type);
            self.checker
                .error(diagnostics::INVALID_OPERATION, message, right_span);
            return Expr::INVALID;
        }
        let ty = if comparison { TypeId::BOOL } else { left.ty };
        if let (ExprKind::Constant(left_value), ExprKind::Constant(right_value)) =
            (&left.kind, &right.kind)
        {
            let folded = eval::binary(op, &left_type, left_value, &right_type, right_value)
                .expect("known traps were reported before folding");
            return Expr {
                ty,
                kind: ExprKind::Constant(folded),
            };
        }
        let left = self.body.add(left, left_span);
        let right = self.body.add(right, right_span);
        Expr {
            ty,
            kind: ExprKind::Binary { op, left, right },
        }
    }

    /// Reports `op` in the expression at `span` applied to `operand` of the
    /// type `ty`, which `fits` rejects.
    fn report_invalid_operand(
        &mut self,
        op: &str,
        ty: TypeId,
        operand: Id<ast::Expr>,
        span: Span,
        fits: impl Fn(&Type) -> bool,
    ) -> Expr {
        let message = format!(
            "operator `{op}` cannot be applied to `{}`",
            self.checker.type_text(ty)
        );
        let types = &self.checker.types;
        let diagnostic = self.type_error(
            diagnostics::INVALID_OPERATION,
            message,
            span,
            operand,
            ty,
            |ty| fits(&types[ty]),
        );
        self.checker.report(diagnostic);
        Expr::INVALID
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

    /// Makes `expr`, when it is a `&mut` in a variable, lend it again, so it
    /// is not moved: the variable stays usable after the call or loop that
    /// takes `expr`.
    pub(crate) fn lend_variable_again(&mut self, expr: Id<Expr>) {
        if let Expr {
            ty,
            kind: ExprKind::Variable(_),
        } = self.body.exprs[expr]
            && let Type::Reference { mutable: true, .. } = self.checker.types[ty]
        {
            let span = self.body.exprs.span(expr);
            let reference = self.body.exprs[expr].clone();
            self.body.exprs[expr] = self.reborrow(reference, span, true);
        }
    }

    /// `reference`, a `&mut` checked at `span`, lent again as a `&mut` when
    /// `mutable`, else as a `&`, to what it refers to, so the `&mut` itself
    /// is not moved. A written `&mut place` borrows `place` itself.
    fn reborrow(&mut self, reference: Expr, span: Span, mutable: bool) -> Expr {
        let place = if let ExprKind::Borrow(place) = reference.kind {
            place
        } else {
            let Type::Reference { target, .. } = self.checker.types[reference.ty] else {
                unreachable!("only a reference is lent again")
            };
            let reference = self.body.add(reference, span);
            let referred = self.dereference(reference, target);
            if referred.ty == TypeId::INVALID {
                return Expr::INVALID;
            }
            self.body.add(referred, span)
        };
        self.check_borrow(mutable, place, span)
    }
}

/// The value of a constant (`Checker::constant`) as an expression.
fn constant_expr(constant: Option<Constant>) -> Expr {
    match constant {
        Some(Constant { ty, value }) => Expr {
            ty,
            kind: ExprKind::Constant(value),
        },
        None => Expr::INVALID,
    }
}

/// Whether a number literal without a suffix can have the type `ty`: an
/// integer literal any number type, a decimal float literal a float type.
/// Hexadecimal, octal and binary literals are never floats.
fn takes_type(literal: &literal::numeric::Literal, ty: &Type) -> bool {
    match ty {
        Type::Integer(_) => !literal.is_float(),
        Type::Float(_) => literal.base == 10,
        _ => false,
    }
}

/// The text of `expression` if it is a number literal without a suffix.
pub(crate) fn unsuffixed_literal(tree: &ast::Tree, expression: Id<ast::Expr>) -> Option<&str> {
    let text = tree.file.slice(tree.exprs.span(expression));
    let number = matches!(tree.exprs[expression], ast::Expr::Number);
    (number && literal::numeric::split(text).suffix.is_empty()).then_some(text)
}

/// Whether `print` writes a value of the type `ty`: a number, `bool`,
/// `cstr` or raw pointer, a struct or array of such values, or a reference
/// to one.
// shortcut: structs and arrays print built in until a trait or interface
// lets each type say how it prints; then this rule and the backend's struct
// and array printing (`llvm` `write_printed`, `array_printer`) go.
fn printable(types: &Types, ty: TypeId) -> bool {
    match types[ty] {
        Type::Integer(_) | Type::Float(_) | Type::Bool | Type::CString => true,
        Type::RawPointer { .. } => true,
        Type::Reference { target, .. } => printable(types, target),
        Type::Struct { ref fields, .. } => fields.iter().all(|&field| printable(types, field)),
        Type::Array { element, .. } => printable(types, element),
        _ => false,
    }
}
