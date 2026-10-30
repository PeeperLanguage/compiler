//! Checking places: fields, indexes, slices and dereferences.

use arena::Id;
use bigint::Limbs;
use diagnostics::Diagnostic;
use diagnostics::suggest::nearest_name;
use model::ast;
use model::ast::Ident;
use model::constant::Value;
use model::semantic_types::Integer;
use model::semantic_types::Type;
use model::semantic_types::TypeId;
use model::thir::Expr;
use model::thir::ExprKind;
use source::Span;

use crate::builder::ThirBuilder;
use crate::expression::TypeExpectation;

impl ThirBuilder<'_, '_> {
    /// `value.field`: the field of a struct value, or of the struct a
    /// reference or typed raw pointer points to; or `len`, an array's or a
    /// slice's only field. An array's type gives it, so the array is not
    /// read.
    pub(super) fn check_field(&mut self, value: Id<ast::Expr>, field: Ident) -> Expr {
        let mut value = self.add_expr(value, TypeExpectation::Hint(None));
        let mut ty = self.body.exprs[value].ty;
        let pointee = self.checker.types[ty].pointee();
        let viewed = pointee.unwrap_or(ty);
        // `Some(length)` for an array, `Some(None)` for a slice, whose
        // length is known only when the program runs.
        let length = self.checker.types[viewed].elements();
        let length = length.map(|(_, length)| length);
        // shortcut: `len` as a field until the language can write it as a
        // free function over any array; then move it there.
        if let Some(length) = length {
            let text = field.text(&self.checker.module().ast.file);
            if text != "len" {
                let viewed = self.checker.type_text(viewed);
                let message = format!("`{viewed}` has no field `{text}`");
                let meant = nearest_name(text, [("len", 0)]).map(str::to_string);
                self.checker
                    .report_unknown(diagnostics::UNKNOWN_FIELD, field.span, message, meant);
                return Expr::INVALID;
            }
            let usize = self.checker.usize_type();
            let Some(length) = length.filter(|_| !self.body.calls(value)) else {
                return Expr {
                    ty: usize,
                    kind: ExprKind::Length(value),
                };
            };
            let length = Limbs::new(self.checker.target.pointer_bits, &[length]);
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

    /// `array[index]`: the element at an integer index of an array in a
    /// place, of the array a reference or typed raw pointer points to, or of
    /// a slice, written at `span`. A constant index must be inside an array;
    /// a slice's must be at least 0 and fit a `u64`, and is checked when the
    /// program runs. A written range slices, which only `&` does.
    pub(super) fn check_index(
        &mut self,
        array: Id<ast::Expr>,
        index: Id<ast::Expr>,
        span: Span,
    ) -> Expr {
        let tree = &self.checker.module().ast;
        if let ast::Expr::Range { .. } = tree.exprs[index] {
            let message = format!("slicing borrows; write `&{}`", tree.file.slice(span));
            self.checker
                .error(diagnostics::INVALID_OPERATION, message, span);
            return Expr::INVALID;
        }
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
        let elements = self.checker.types[ty].elements();
        let (element, length) = elements.unwrap_or((TypeId::INVALID, None));
        if element == TypeId::INVALID {
            if ty != TypeId::INVALID {
                let message = format!("`{}` cannot be indexed", self.checker.type_text(ty));
                let span = self.body.exprs.span(array);
                self.checker
                    .error(diagnostics::INVALID_OPERATION, message, span);
            }
            return Expr::INVALID;
        }
        if let Type::Range { .. } = self.checker.types[index_type] {
            self.checker
                .not_supported(index_span, "range values as indexes");
            return Expr::INVALID;
        }
        let Some(integer) = self.integer_index(index) else {
            return Expr::INVALID;
        };
        // A negative constant, or one past `u64`, is outside every slice.
        if let ExprKind::Constant(Value::Integer(ref value)) = self.body.exprs[index].kind
            && bigint::to_u64(integer.bits, integer.signed, value)
                .is_none_or(|at| length.is_some_and(|length| at >= length))
        {
            let (written, viewed) = (tree.file.slice(index_span), self.checker.type_text(ty));
            let message = match length {
                Some(length) => {
                    format!("index `{written}` is outside `{viewed}`, whose length is {length}")
                }
                None => format!("index `{written}` is outside every `{viewed}`"),
            };
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

    /// The integer type of the checked index `index`; `None` after an error,
    /// reported here for a type that is not an integer.
    pub(super) fn integer_index(&mut self, index: Id<Expr>) -> Option<Integer> {
        let ty = self.body.exprs[index].ty;
        match self.checker.types[ty] {
            Type::Integer(integer) => Some(integer),
            Type::Invalid => None,
            _ => {
                let message = format!(
                    "an index must be an integer, found `{}`",
                    self.checker.type_text(ty)
                );
                let span = self.body.exprs.span(index);
                self.checker
                    .error(diagnostics::TYPE_MISMATCH, message, span);
                None
            }
        }
    }

    /// `&array[start..end]`, `&mut` when `mutable`, at `span`, where
    /// `indexed` is `array[start..end]`, its index a written range: a slice
    /// of the elements of an array in a place, of one a reference points
    /// to, or of a slice's, from `start` up to `end`, each left out or an
    /// integer as an index is (`ExprKind::Slice`). Ends known at compile
    /// time must be inside the array and in order; others, and every end of
    /// a slice's, are checked when the program runs. A step is an error.
    pub(super) fn check_slice(
        &mut self,
        mutable: bool,
        indexed: Id<ast::Expr>,
        span: Span,
    ) -> Expr {
        let tree = &self.checker.module().ast;
        let ast::Expr::Index {
            value: array,
            index: range,
        } = tree.exprs[indexed]
        else {
            unreachable!("the caller found an index")
        };
        let ast::Expr::Range {
            inclusive,
            start,
            end,
            step,
        } = tree.exprs[range]
        else {
            unreachable!("the caller found a written range")
        };
        if let Some(step) = step {
            let step_span = tree.exprs.span(step);
            self.checker.error(
                diagnostics::INVALID_OPERATION,
                "a slice has no step",
                step_span,
            );
            return Expr::INVALID;
        }
        let mut array = self.add_expr(array, TypeExpectation::Hint(None));
        let mut ty = self.body.exprs[array].ty;
        let array_span = self.body.exprs.span(array);
        match self.checker.types[ty] {
            // shortcut: a slice through a raw pointer holds no loan; allow
            // it, as `&p.*` is, when a program needs it.
            Type::RawPointer { .. } => {
                self.checker
                    .not_supported(array_span, "slices taken through raw pointers");
                return Expr::INVALID;
            }
            // Written `r[i..j]`, so a message names that, as `r[i]`.
            Type::Reference { target, .. } => {
                let referred = self.dereference(array, target);
                ty = referred.ty;
                array = self.body.add(referred, tree.exprs.span(indexed));
            }
            _ => {}
        }
        let Some((element, length)) = self.checker.types[ty].elements() else {
            if ty != TypeId::INVALID {
                let message = format!("`{}` cannot be sliced", self.checker.type_text(ty));
                self.checker
                    .error(diagnostics::INVALID_OPERATION, message, array_span);
            }
            return Expr::INVALID;
        };
        let ends = self.check_slice_ends([start, end], inclusive, ty, length, range);
        let Some([start, end]) = ends else {
            return Expr::INVALID;
        };
        let slice = self.checker.types.intern(Type::Slice { mutable, element });
        if !self.check_lending(slice, array, span) {
            return Expr::INVALID;
        }
        Expr {
            ty: slice,
            kind: ExprKind::Slice {
                array,
                start,
                end,
                inclusive,
            },
        }
    }

    /// The ends, each left out or written, of the slice of `viewed`, an
    /// array type of `length` elements or a slice type (no `length`), by
    /// the written range `range`, `..=` when `inclusive`: integers of one
    /// type, `usize` for literals without a suffix. Ends known at compile
    /// time must be inside the array, or fit a `usize` for a slice, and in
    /// order when both are; `None` after an error.
    pub(super) fn check_slice_ends(
        &mut self,
        ends: [Option<Id<ast::Expr>>; 2],
        inclusive: bool,
        viewed: TypeId,
        length: Option<u64>,
        range: Id<ast::Expr>,
    ) -> Option<[Option<Id<Expr>>; 2]> {
        let usize_type = self.checker.usize_type();
        let Type::Integer(size) = self.checker.types[usize_type] else {
            unreachable!("`usize` is an integer")
        };
        // No slice is longer than the largest `usize`.
        let limit = length.unwrap_or(u64::MAX >> (64 - size.bits));
        let usize = Some(usize_type);
        let ends = match ends {
            [Some(start), Some(end)] => {
                let (start, end) = self.check_range_ends(start, end, usize);
                [Some(start), Some(end)]
            }
            ends => ends.map(|end| end.map(|end| self.add_expr(end, TypeExpectation::Hint(usize)))),
        };
        // How many elements come before each end when known at compile
        // time: a missing start is 0, a missing end the length; a written
        // `..=` end has one more, past it.
        let mut before_ends = [Some(0), length];
        let mut outside = false;
        let past = [0, u64::from(inclusive)];
        for ((before, end), past) in before_ends.iter_mut().zip(ends).zip(past) {
            let Some(end) = end else {
                continue;
            };
            let integer = self.integer_index(end)?;
            let ExprKind::Constant(Value::Integer(ref value)) = self.body.exprs[end].kind else {
                *before = None;
                continue;
            };
            // A negative end, or one past `u64`, is outside every array.
            let known = bigint::to_u64(integer.bits, integer.signed, value)
                .and_then(|at| at.checked_add(past));
            outside |= known.is_none_or(|known| known > limit);
            *before = known;
        }
        let tree = &self.checker.module().ast;
        let range_span = tree.exprs.span(range);
        let written = tree.file.slice(range_span);
        let error = if outside {
            let viewed = self.checker.type_text(viewed);
            match length {
                Some(length) => {
                    format!("slice `{written}` is outside `{viewed}`, whose length is {length}")
                }
                None => format!("slice `{written}` is outside every `{viewed}`"),
            }
        } else if let [Some(first), Some(last)] = before_ends
            && first > last
        {
            format!("slice `{written}` starts after it ends")
        } else {
            return Some(ends);
        };
        self.checker
            .error(diagnostics::INVALID_OPERATION, error, range_span);
        None
    }

    /// `reference.*`, written at `span`: the value a reference or a typed
    /// raw pointer points to.
    pub(super) fn check_dereference(&mut self, reference: Id<ast::Expr>, span: Span) -> Expr {
        let reference = self.add_expr(reference, TypeExpectation::Hint(None));
        let ty = self.body.exprs[reference].ty;
        match self.checker.types[ty] {
            ref pointer if let Some(target) = pointer.pointee() => {
                self.dereference(reference, target)
            }
            Type::Invalid => Expr::INVALID,
            ref found => {
                let message = format!(
                    "`.*` needs a reference or a typed raw pointer, found `{}`",
                    self.checker.type_text(ty)
                );
                let file = &self.checker.module().ast.file;
                let mut error =
                    Diagnostic::new(diagnostics::INVALID_OPERATION, message).label(file, span, "");
                // Rust writes `&*s` to lend a slice again; Peeper has no bare
                // element type for `.*` to give, so the help names what does.
                if let Type::Slice { .. } = found {
                    let text = file.slice(self.body.exprs.span(reference));
                    let help = format!(
                        "a slice is lent again as it is (`f({text})`), or as `&{text}[..]` / \
                         `&mut {text}[..]`"
                    );
                    error = error.help(help, Vec::new());
                }
                self.checker.report(error);
                Expr::INVALID
            }
        }
    }

    /// What `reference`, whose type refers to `target`, refers to: a place.
    pub(super) fn dereference(&mut self, reference: Id<Expr>, target: TypeId) -> Expr {
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

    /// The index of the field `name` of the struct type `ty`, in declaration
    /// order; `None` after reporting that the struct has no such field, with
    /// its nearest field name.
    pub(super) fn field_index(&mut self, ty: TypeId, name: Ident) -> Option<usize> {
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
}
