//! Checking expressions into THIR. Each expression gets its type, and an
//! operation on values known at compile time is folded to its value.

use std::num::NonZeroU32;

use arena::Id;
use arena::List;
use bigint::Limbs;
use diagnostics::Diagnostic;
use diagnostics::suggest::nearest_name;
use model::BinaryOp;
use model::Local;
use model::Symbol;
use model::UnaryOp;
use model::ast;
use model::ast::Ident;
use model::constant;
use model::constant::Constant;
use model::constant::Value;
use model::semantic_types::Float;
use model::semantic_types::Type;
use model::semantic_types::TypeId;
use model::thir::Expr;
use model::thir::ExprKind;
use model::thir::FieldValue;
use source::Span;

use crate::builtin::UNSUPPORTED_FUNCTIONS;
use crate::scope;
use crate::statement::ThirBuilder;

impl ThirBuilder<'_, '_> {
    /// `expression` (`expr`), added to the body.
    pub(crate) fn add_expr(&mut self, expression: Id<ast::Expr>) -> Id<Expr> {
        let checked = self.check_expr(expression, None);
        self.body
            .add(checked, self.checker.module().ast.exprs.span(expression))
    }

    /// `expression` as a value of the type `ty` (`expr_as`), added to the
    /// body.
    pub(crate) fn add_expr_as(&mut self, expression: Id<ast::Expr>, ty: TypeId) -> Id<Expr> {
        let checked = self.check_expr_as(expression, ty);
        self.body
            .add(checked, self.checker.module().ast.exprs.span(expression))
    }

    /// `expression` as a value of the type `ty`, not yet added to the body;
    /// a mismatch is reported.
    pub(crate) fn check_expr_as(&mut self, expression: Id<ast::Expr>, ty: TypeId) -> Expr {
        let span = self.checker.module().ast.exprs.span(expression);
        let checked = self.check_expr(expression, Some(ty));
        self.coerce(checked, span, ty).unwrap_or_else(|mismatch| {
            self.checker.report(mismatch);
            Expr::INVALID
        })
    }

    /// `expr`, written at `span`, as a value of the type `to`: unchanged when
    /// it has that type, widened when its type widens to it
    /// (`Type::widens_to`), else the mismatch to report. An error already
    /// reported on either side gives an invalid expression and nothing to
    /// report.
    fn coerce(&mut self, expr: Expr, span: Span, to: TypeId) -> Result<Expr, Diagnostic> {
        if expr.ty == TypeId::INVALID || to == TypeId::INVALID {
            return Ok(Expr::INVALID);
        }
        let checker = &self.checker;
        // An alias is its type under another name.
        if checker.types.same(expr.ty, to) {
            return Ok(expr);
        }
        let (from_type, to_type) = (checker.types[expr.ty].clone(), checker.types[to].clone());
        if !from_type.widens_to(&to_type) {
            let (expected, found) = (checker.type_text(to), checker.type_text(expr.ty));
            return Err(Diagnostic::new(
                diagnostics::TYPE_MISMATCH,
                format!("mismatched types: expected `{expected}`, found `{found}`"),
            )
            .label(&self.checker.module().ast.file, span, ""));
        }
        let kind = match expr.kind {
            ExprKind::Constant(constant) => {
                ExprKind::Constant(constant::widen(&from_type, &to_type, &constant))
            }
            _ => ExprKind::Widen(self.body.add(expr, span)),
        };
        Ok(Expr { ty: to, kind })
    }

    /// The THIR of `expression`, not yet added to the body, so that a folded
    /// operation leaves no nodes for its operands. `expected` is the type the
    /// context expects, which a number literal without a suffix takes; where
    /// the context requires that type, its caller coerces the result to it.
    pub(crate) fn check_expr(
        &mut self,
        expression: Id<ast::Expr>,
        expected: Option<TypeId>,
    ) -> Expr {
        let tree = &self.checker.module().ast;
        let span = tree.exprs.span(expression);
        let what = match tree.exprs[expression] {
            ast::Expr::Name => {
                let symbol = self.resolve(span);
                return self.check_symbol_value(symbol, span);
            }
            ast::Expr::Number => return self.check_number(span, None, expected),
            ast::Expr::Bool(value) => {
                return Expr {
                    ty: TypeId::BOOL,
                    kind: ExprKind::Constant(Value::Bool(value)),
                };
            }
            ast::Expr::String { c_string: true } => {
                let bytes = literal::value(tree.file.slice(span));
                return Expr {
                    ty: TypeId::CSTR,
                    kind: ExprKind::Constant(Value::Bytes(bytes.into())),
                };
            }
            ast::Expr::Parentheses(inner) => return self.check_expr(inner, expected),
            ast::Expr::Unary { op, value } => return self.check_unary(op, value, span, expected),
            ast::Expr::Binary { op, left, right } => {
                return self.check_binary(op, left, right, span, expected);
            }
            ast::Expr::Invalid => return Expr::INVALID, // the parser reported it
            ast::Expr::Path(path) => {
                let symbol = self.resolve_path(path);
                return self.check_symbol_value(symbol, span);
            }
            ast::Expr::Call { callee, arguments } => {
                return self.check_call(callee, arguments, span);
            }
            ast::Expr::StructLiteral { ty, fields } => {
                return self.check_struct_literal(ty, fields, span, expected);
            }
            ast::Expr::Selector { value, field } => return self.check_field(value, field),
            ast::Expr::String { c_string: false } => "string literals",
            ast::Expr::Char { .. } => "character literals",
            ast::Expr::None => "`none` values",
            ast::Expr::Reference { .. } => "references",
            ast::Expr::RawAddress(_) => "raw addresses",
            ast::Expr::Free(_) => "`free` calls",
            ast::Expr::Print { .. } => "`print` calls",
            ast::Expr::Index { .. } => "index expressions",
            ast::Expr::Range { .. } => "ranges",
            ast::Expr::VariantLiteral { .. } => "variant literals",
            ast::Expr::ArrayLiteral { .. } => "array literals",
            ast::Expr::Cast { .. } => "casts",
            ast::Expr::Is { .. } => "`is` tests",
            ast::Expr::OrElse { .. } => "`orelse` fallbacks",
            ast::Expr::Block(_) => "block expressions",
            ast::Expr::Pipe { .. } => "pipes",
        };
        self.checker.not_supported(span, what);
        Expr::INVALID
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
            Some(Symbol::Type(_)) => "a type",
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
                self.add_expr(tree.field_values[field].value); // for its own errors
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
                self.add_expr(value); // for its own errors
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
                self.add_expr(value);
                continue;
            }
            set[index] = Some(name.span);
            let value = self.add_expr_as(value, field_types[index]);
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
            kind: ExprKind::StructLiteral(self.body.lists.add(&checked)),
        }
    }

    /// `value.field`: the field of a struct value.
    fn check_field(&mut self, value: Id<ast::Expr>, field: Ident) -> Expr {
        let value = self.add_expr(value);
        let ty = self.body.exprs[value].ty;
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
                self.record(module_span, symbol);
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
        let members = &self.checker.scopes[imported.index()];
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
        self.record(member_span, symbol);
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
        let Some((function, Type::Function { parameters, result })) = signature else {
            for argument in tree.lists.items(arguments) {
                self.add_expr(argument); // for its own errors
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
                Some(&parameter) => self.add_expr_as(argument, parameter),
                None => self.add_expr(argument),
            };
            checked.push(argument.into());
        }
        if !arity_matches {
            return Expr::INVALID;
        }
        Expr {
            ty: result,
            kind: ExprKind::Call {
                function,
                arguments: self.body.lists.add(&checked),
            },
        }
    }

    /// The number literal at `number`, or its negation written at `negated`
    /// (`-128`, which fits `i8` where `128` does not). It takes its suffix's
    /// type, else the type `expected` if it can (`takes_type`), else `i32` or
    /// `f64`.
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
        let negative = negated.is_some();
        let value = match types[ty] {
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
            UnaryOp::LogicalNot => self.check_expr(operand, Some(TypeId::BOOL)),
            _ => self.check_expr(operand, expected),
        };
        if checked.ty == TypeId::INVALID {
            return Expr::INVALID;
        }
        let ty = self.checker.types[checked.ty].clone();
        let accepted = match op {
            UnaryOp::Negate => matches!(ty, Type::Integer(_) | Type::Float(_)),
            UnaryOp::LogicalNot => ty == Type::Bool,
            UnaryOp::BitNot => matches!(ty, Type::Integer(_)),
        };
        if !accepted {
            return self.report_invalid_operand(op.symbol(), checked.ty, span);
        }
        let result = checked.ty;
        let kind = match checked.kind {
            ExprKind::Constant(constant) => ExprKind::Constant(
                constant::unary(op, &ty, &constant).expect("unary operators do not trap"),
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
        left: Id<ast::Expr>,
        right: Id<ast::Expr>,
        span: Span,
        expected: Option<TypeId>,
    ) -> Expr {
        let tree = &self.checker.module().ast;
        let (left_span, right_span) = (tree.exprs.span(left), tree.exprs.span(right));
        let comparison = matches!(
            op,
            BinaryOp::EqualTo
                | BinaryOp::NotEqualTo
                | BinaryOp::LessThan
                | BinaryOp::LessOrEqual
                | BinaryOp::GreaterThan
                | BinaryOp::GreaterOrEqual
        );
        let shift = matches!(op, BinaryOp::ShiftLeft | BinaryOp::ShiftRight);
        let left_literal = unsuffixed_literal(tree, left);
        let left = self.check_expr(left, if comparison { None } else { expected });
        let right = if shift {
            self.check_expr(right, None)
        } else {
            let checked = self.check_expr(right, Some(left.ty));
            let found = checked.ty;
            match self.coerce(checked, right_span, left.ty) {
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
                    }
                    self.checker.report(mismatch);
                    Expr::INVALID
                }
            }
        };
        if left.ty == TypeId::INVALID || right.ty == TypeId::INVALID {
            return Expr::INVALID;
        }
        let (left_type, right_type) = {
            let types = &mut self.checker.types;
            (types[left.ty].clone(), types[right.ty].clone())
        };
        let accepted = match op {
            BinaryOp::Add
            | BinaryOp::Subtract
            | BinaryOp::Multiply
            | BinaryOp::Divide
            | BinaryOp::Remainder
            | BinaryOp::LessThan
            | BinaryOp::LessOrEqual
            | BinaryOp::GreaterThan
            | BinaryOp::GreaterOrEqual => matches!(left_type, Type::Integer(_) | Type::Float(_)),
            BinaryOp::ShiftLeft
            | BinaryOp::ShiftRight
            | BinaryOp::BitAnd
            | BinaryOp::BitOr
            | BinaryOp::BitXor => matches!(left_type, Type::Integer(_)),
            BinaryOp::EqualTo | BinaryOp::NotEqualTo => {
                matches!(left_type, Type::Integer(_) | Type::Float(_) | Type::Bool)
            }
            BinaryOp::LogicalAnd | BinaryOp::LogicalOr => left_type == Type::Bool,
        };
        if !accepted {
            return self.report_invalid_operand(op.symbol(), left.ty, span);
        }
        if shift && !matches!(right_type, Type::Integer(_)) {
            let count = self.checker.type_text(right.ty);
            let message = format!("a shift count must be an integer, found `{count}`");
            self.checker
                .error(diagnostics::INVALID_OPERATION, message, right_span);
            return Expr::INVALID;
        }
        if let Some(message) = known_trap(op, &left_type, &right_type, &right) {
            self.checker
                .error(diagnostics::INVALID_OPERATION, message, right_span);
            return Expr::INVALID;
        }
        let ty = if comparison { TypeId::BOOL } else { left.ty };
        if let (ExprKind::Constant(left_value), ExprKind::Constant(right_value)) =
            (&left.kind, &right.kind)
        {
            let folded = constant::binary(op, &left_type, left_value, &right_type, right_value)
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

    /// Reports `op` applied to an operand of the type `ty`, at `span`.
    fn report_invalid_operand(&mut self, op: &str, ty: TypeId, span: Span) -> Expr {
        let ty = self.checker.type_text(ty);
        let message = format!("operator `{op}` cannot be applied to `{ty}`");
        self.checker
            .error(diagnostics::INVALID_OPERATION, message, span);
        Expr::INVALID
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

/// Why `op` on a left operand of `left_type` and `right` of `right_type`
/// traps whatever the left value is: a division by a constant zero, or a
/// shift by a constant count out of range.
fn known_trap(op: BinaryOp, left_type: &Type, right_type: &Type, right: &Expr) -> Option<String> {
    let (Type::Integer(shifted), Type::Integer(count_type)) = (left_type, right_type) else {
        return None; // float division gives an infinity or NaN
    };
    let ExprKind::Constant(Value::Integer(right)) = &right.kind else {
        return None;
    };
    match op {
        BinaryOp::Divide | BinaryOp::Remainder
            if right.as_slice().iter().all(|&limb| limb == 0) =>
        {
            Some("division by zero".to_string())
        }
        BinaryOp::ShiftLeft | BinaryOp::ShiftRight => {
            constant::shift_count(*shifted, *count_type, right)
                .is_err()
                .then(|| format!("a shift count must be from 0 to {}", shifted.bits - 1))
        }
        _ => None,
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
fn unsuffixed_literal(tree: &ast::Tree, expression: Id<ast::Expr>) -> Option<&str> {
    let text = tree.file.slice(tree.exprs.span(expression));
    let number = matches!(tree.exprs[expression], ast::Expr::Number);
    (number && literal::numeric::split(text).suffix.is_empty()).then_some(text)
}
