//! Checking `for` loops: over a range, an array or a slice, with their
//! loop variables and bodies.

use std::num::NonZeroU32;

use arena::Id;
use arena::List;
use bigint::Limbs;
use diagnostics::Diagnostic;
use model::ast;
use model::ast::Ident;
use model::constant::Value;
use model::semantic_types::Type;
use model::semantic_types::TypeId;
use model::thir::Block;
use model::thir::Expr;
use model::thir::ExprKind;
use model::thir::Local;
use model::thir::Stmt;
use model::thir::Variable;
use source::Span;

use crate::builder::ThirBuilder;
use crate::expression::TypeExpectation;

impl ThirBuilder<'_, '_> {
    /// `close_block` for a loop's `body`, in which `break` and `continue`
    /// are allowed.
    pub(super) fn check_loop_body(
        &mut self,
        body: Id<ast::Block>,
        enclosing: (Id<Block>, usize),
        first: Option<Id<Stmt>>,
    ) -> Id<Block> {
        self.loops += 1;
        let body = self.close_block(body, enclosing, first);
        self.loops -= 1;
        body
    }

    /// A loop's variable `name`, set each pass, declared in the current
    /// block.
    pub(super) fn declare_loop_variable(&mut self, ty: TypeId, name: Ident) -> Id<Variable> {
        let variable = Variable {
            ty,
            mutable: false,
            initialized: true,
            scope: self.block,
            hidden: false,
        };
        let variable = self.body.add(variable, name.span);
        self.declare(name, Local::Variable(variable));
        variable
    }

    /// `for value in range { body }` or `for index, value in range`, or
    /// `for start..end { body }` without a `value`, over the checked
    /// `range`, a `range[T]` (invalid after an error): `value` is a `T`.
    pub(super) fn check_for_range(
        &mut self,
        index: Option<Ident>,
        value: Option<Ident>,
        range: Id<Expr>,
        body: Id<ast::Block>,
    ) -> Stmt {
        let element = match self.checker.types[self.body.exprs[range].ty] {
            Type::Range { element, .. } => element,
            _ => TypeId::INVALID,
        };
        let enclosing = self.open_block(body);
        let index = index.map(|index| {
            let usize = self.checker.usize_type();
            self.declare_loop_variable(usize, index)
        });
        let value = value.map(|value| self.declare_loop_variable(element, value));
        let body = self.check_loop_body(body, enclosing, None);
        Stmt::ForRange {
            index,
            value,
            range,
            body,
        }
    }

    /// The step of a range, written `step` or else 1 at `range_span`, of
    /// the range type's step type `step_type` (`Types::range`), and not a
    /// constant zero. With an invalid `step_type`, after an error in the
    /// ends, it is checked only for its own errors.
    pub(crate) fn check_range_step(
        &mut self,
        step: Option<Id<ast::Expr>>,
        step_type: TypeId,
        range_span: Span,
    ) -> Id<Expr> {
        let Some(step) = step else {
            let one = match self.checker.types[step_type] {
                Type::Integer(unsigned) => Expr {
                    ty: step_type,
                    kind: ExprKind::Constant(Value::Integer(Limbs::new(unsigned.bits, &[1]))),
                },
                _ => Expr::INVALID,
            };
            return self.body.add(one, range_span);
        };
        let expectation = match step_type {
            TypeId::INVALID => TypeExpectation::Hint(None),
            ty => TypeExpectation::Required(ty),
        };
        let step = self.add_expr(step, expectation);
        if let ExprKind::Constant(Value::Integer(ref limbs)) = self.body.exprs[step].kind
            && limbs.as_slice().iter().all(|&limb| limb == 0)
        {
            let span = self.body.exprs.span(step);
            let message = "a range's step must not be zero";
            self.checker
                .error(diagnostics::INVALID_OPERATION, message, span);
        }
        step
    }

    /// `for value in items { body }` or `for index, value in items`, over an
    /// array, a reference to one or a slice, computed once before the first
    /// pass. Written as the code that runs it, a block of
    ///
    /// ```text
    /// let items = <iterable>; // unnamed; ends after the loop
    /// for index in 0..N { let value = items[index]; body }
    /// ```
    ///
    /// where `N` is the array's length or `items.len`, and `value` is a copy
    /// of the element, which must copy, or `&items.*[index]` or
    /// `&mut items.*[index]` when `items` is a `&` or `&mut` reference
    /// (`&items[index]` or `&mut items[index]` for a slice), which keeps the
    /// elements borrowed for the whole loop. A `[&mut]` variable is lent
    /// again, so it is usable after the loop. Without `index`, the counter
    /// is unnamed.
    pub(super) fn check_for_array(
        &mut self,
        index: Option<Ident>,
        value: Ident,
        iterable: Id<Expr>,
        body: Id<ast::Block>,
    ) -> Option<Stmt> {
        self.lend_variable_again(iterable);
        let (items_type, span) = (self.body.exprs[iterable].ty, self.body.exprs.span(iterable));
        let types = &self.checker.types;
        let (array, borrow) = match types[items_type] {
            Type::Reference { target, mutable } => (target, Some(mutable)),
            Type::Slice { mutable, .. } => (items_type, Some(mutable)),
            _ => (items_type, None),
        };
        // After an error, the body is still checked, with an invalid `value`.
        // A slice's length is read when the loop starts.
        let (element, length) = types[array].elements().unwrap_or_else(|| {
            if items_type != TypeId::INVALID {
                self.checker.not_supported(
                    span,
                    "`for` over values other than ranges, arrays and slices",
                );
            }
            (TypeId::INVALID, Some(0))
        });
        if borrow.is_none() && !self.checker.types.is_copy(element) {
            let file = &self.checker.module().ast.file;
            let array_text = self.checker.type_text(array);
            let element_text = self.checker.type_text(element);
            let diagnostic = Diagnostic::new(
                diagnostics::PARTIAL_MOVE,
                "cannot move an element out of its array",
            )
            .label(
                file,
                span,
                format!(
                    "`for` takes each element of this `{array_text}`, and `{element_text}` moves"
                ),
            )
            .help(
                format!(
                    "loop over `&{}` for a reference to each element",
                    file.slice(span)
                ),
                Vec::new(),
            );
            self.checker.report(diagnostic);
        }
        // The unnamed `items`, in a block of its own that ends after the loop.
        let body_span = self.checker.module().ast.blocks.span(body);
        let whole = Span::new(span.start as usize, body_span.end as usize);
        let holder = Block {
            parent: Some(self.block),
            statements: List::EMPTY,
        };
        let holder = self.body.add(holder, whole);
        let outer = std::mem::replace(&mut self.block, holder);
        let items = self.add_hidden_variable(items_type, span);
        let let_items = Stmt::Let {
            variable: items,
            value: Some(iterable),
        };
        let let_items = self.body.add(let_items, span);
        // `for index in 0..N`, each pass first setting `value`.
        let usize = self.checker.usize_type();
        let Type::Integer(integer) = self.checker.types[usize] else {
            unreachable!("`usize` is an integer")
        };
        let mut constant = |number: u64| {
            let value = Value::Integer(Limbs::new(integer.bits, &[number]));
            let constant = Expr {
                ty: usize,
                kind: ExprKind::Constant(value),
            };
            self.body.add(constant, span)
        };
        let start = constant(0);
        let step = constant(1); // `usize` is its own step type
        let end = if let Some(length) = length {
            constant(length)
        } else {
            let read = Expr {
                ty: items_type,
                kind: ExprKind::Variable(items),
            };
            let read = self.body.add(read, span);
            let length = Expr {
                ty: usize,
                kind: ExprKind::Length(read),
            };
            self.body.add(length, span)
        };
        let range = Expr {
            ty: (self.checker.types.range(usize)).expect("`usize` is an integer"),
            kind: ExprKind::Range {
                start,
                end,
                step,
                inclusive: false,
            },
        };
        let range = self.body.add(range, span);
        let enclosing = self.open_block(body);
        let counter = match index {
            Some(index) => self.declare_loop_variable(usize, index),
            None => self.add_hidden_variable(usize, span),
        };
        let (element_value, value_type) = self.array_element(items, counter, element, borrow, span);
        let variable = self.declare_loop_variable(value_type, value);
        let let_value = Stmt::Let {
            variable,
            value: Some(element_value),
        };
        let let_value = self.body.add(let_value, value.span);
        let body = self.check_loop_body(body, enclosing, Some(let_value));
        let range = Stmt::ForRange {
            index: None,
            value: Some(counter),
            range,
            body,
        };
        let range = self.body.add(range, whole);
        self.block = outer;
        let statements: [NonZeroU32; 2] = [let_items.into(), range.into()];
        self.body.blocks[holder].statements = self.body.children.lists.add(&statements);
        Some(Stmt::Block(holder))
    }

    /// The value `check_for_array` gives `value` each pass, and its type:
    /// `items[counter]`, or `&items.*[counter]` or `&mut items.*[counter]`
    /// when `borrow` says `items` is a `&` or `&mut` reference to an array
    /// of `element` (`&items[counter]` or `&mut items[counter]` for a
    /// slice, as `s[i]` is written).
    pub(super) fn array_element(
        &mut self,
        items: Id<Variable>,
        counter: Id<Variable>,
        element: TypeId,
        borrow: Option<bool>,
        span: Span,
    ) -> (Id<Expr>, TypeId) {
        let read = Expr {
            ty: self.body.variables[items].ty,
            kind: ExprKind::Variable(items),
        };
        let mut place = self.body.add(read, span);
        if let Type::Reference { target, .. } = self.checker.types[self.body.variables[items].ty] {
            let referred = Expr {
                ty: target,
                kind: ExprKind::Dereference(place),
            };
            place = self.body.add(referred, span);
        }
        let at = Expr {
            ty: self.body.variables[counter].ty,
            kind: ExprKind::Variable(counter),
        };
        let at = self.body.add(at, span);
        let read = Expr {
            ty: element,
            kind: ExprKind::Index {
                array: place,
                index: at,
            },
        };
        let read = self.body.add(read, span);
        let Some(mutable) = borrow else {
            return (read, element);
        };
        let ty = self.checker.reference_type(mutable, element, span);
        let reference = Expr {
            ty,
            kind: ExprKind::Borrow(read),
        };
        (self.body.add(reference, span), ty)
    }
}
