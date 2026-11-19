//! The dispatch for what reaches into a value: `value.field`,
//! `array[index]`, `&array[start..end]` and `reference.*`. Each looks at the
//! type of the value and hands the work to the concept that owns that type
//! (`arrays.rs`, `slices.rs`, `structs.rs`), reporting a
//! type that none takes. A value whose type each copy decides gives a value
//! each copy computes (`Expr::UNDECIDED`).

use arena::Id;
use diagnostics::Diagnostic;
use model::semantic_types::Integer;
use model::semantic_types::Type;
use model::semantic_types::TypeId;
use model::thir::Expr;
use model::thir::ExprKind;
use source::Span;
use tree::ast;
use tree::ast::Ident;
use tree::ast::WrittenRange;

use crate::arrays;
use crate::builder::ThirBuilder;
use crate::slices;
use crate::structs;

impl ThirBuilder<'_, '_> {
    /// `value.field`: a field of a struct, or of the struct a reference or
    /// typed raw pointer points to; or `len`, an array's, a slice's or a
    /// `&str`'s, read without going through a reference to it.
    #[deny(
        clippy::wildcard_enum_match_arm,
        clippy::match_wildcard_for_single_variants
    )]
    pub(super) fn check_field(&mut self, value: Id<ast::Expr>, field: Ident) -> Expr {
        let value = self.add_expr(value, None);
        let ty = self.body.exprs[value].ty;
        let viewed = self.checker.types[ty].pointee().unwrap_or(ty);
        let span = self.body.exprs.span(value);
        match self.checker.types[viewed] {
            Type::Array { .. } | Type::Slice { .. } | Type::Str => {
                arrays::check_length(self, value, viewed, field)
            }
            Type::Struct { .. } | Type::Named { .. } => {
                let place = self.through_pointer(value, span);
                structs::check_field(self, place, field)
            }
            Type::Parameter { .. } | Type::Undecided => Expr::UNDECIDED,
            Type::Invalid => Expr::INVALID,
            Type::Void
            | Type::Bool
            | Type::Integer(_)
            | Type::Float(_)
            | Type::CString
            | Type::Function { .. }
            | Type::Alias { .. }
            | Type::Range { .. }
            | Type::Reference { .. }
            | Type::Interface { .. }
            | Type::InterfaceReference { .. }
            | Type::RawPointer { .. } => {
                // Through a raw pointer, what it reaches is checked first.
                let place = self.through_pointer(value, span);
                let ty = self.body.exprs[place].ty;
                if ty != TypeId::INVALID {
                    let message = format!("`{}` has no fields", self.checker.type_text(ty));
                    self.checker
                        .error(diagnostics::INVALID_OPERATION, message, field.span);
                }
                Expr::INVALID
            }
        }
    }

    /// `array[index]`, written at `span`: an element of an array or slice
    /// in a place, or of one a reference or typed raw pointer points to. A
    /// written range slices, which only `&` does.
    #[deny(
        clippy::wildcard_enum_match_arm,
        clippy::match_wildcard_for_single_variants
    )]
    pub(super) fn check_index(
        &mut self,
        array: Id<ast::Expr>,
        index: Id<ast::Expr>,
        span: Span,
    ) -> Expr {
        let tree = &self.checker.module().ast;
        if tree.written_range(index).is_some() {
            let message = format!("slicing borrows; write `&{}`", tree.file.slice(span));
            self.checker
                .error(diagnostics::INVALID_OPERATION, message, span);
            return Expr::INVALID;
        }
        let array = self.add_expr(array, None);
        let array = self.through_pointer(array, self.body.exprs.span(array));
        let ty = self.body.exprs[array].ty;
        let index = self.add_expr(index, Some(self.checker.types.usize));
        match self.checker.types[ty] {
            Type::Array { .. } | Type::Slice { .. } => {
                if let Type::Range { .. } = self.checker.types[self.body.exprs[index].ty] {
                    let index_span = self.body.exprs.span(index);
                    self.checker
                        .not_supported(index_span, "range values as indexes");
                    return Expr::INVALID;
                }
                arrays::check_element(self, array, index)
            }
            Type::Parameter { .. } | Type::Undecided => Expr::UNDECIDED,
            Type::Invalid => Expr::INVALID,
            Type::Void
            | Type::Bool
            | Type::Integer(_)
            | Type::Float(_)
            | Type::CString
            | Type::Str
            | Type::Function { .. }
            | Type::Alias { .. }
            | Type::Named { .. }
            | Type::Struct { .. }
            | Type::Range { .. }
            | Type::Reference { .. }
            | Type::Interface { .. }
            | Type::InterfaceReference { .. }
            | Type::RawPointer { .. } => {
                let message = format!("`{}` cannot be indexed", self.checker.type_text(ty));
                let span = self.body.exprs.span(array);
                self.checker
                    .error(diagnostics::INVALID_OPERATION, message, span);
                Expr::INVALID
            }
        }
    }

    /// The integer type of the checked index `index`; `None` after an error,
    /// reported here for a type that is not an integer.
    pub(crate) fn integer_index(&mut self, index: Id<Expr>) -> Option<Integer> {
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

    /// `&array[start..end]`, `&mut` when `mutable`, where `indexed` is
    /// `array[start..end]`, its index a written range: a slice of an array
    /// or slice in a place, or of one a reference points to
    /// (`slices::check_slice`). A step is an error.
    #[deny(
        clippy::wildcard_enum_match_arm,
        clippy::match_wildcard_for_single_variants
    )]
    pub(super) fn check_slice(&mut self, mutable: bool, indexed: Id<ast::Expr>) -> Expr {
        let tree = &self.checker.module().ast;
        let ast::Expr::Index {
            value: array,
            index: range,
        } = tree.exprs[indexed]
        else {
            unreachable!("the caller found an index")
        };
        let Some(WrittenRange {
            inclusive,
            start,
            end,
            step,
        }) = tree.written_range(range)
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
        let array = self.add_expr(array, None);
        let array_span = self.body.exprs.span(array);
        // shortcut: a slice through a raw pointer holds no loan; allow it,
        // as `&p.*` is, when a program needs it.
        if let Type::RawPointer { .. } = self.checker.types[self.body.exprs[array].ty] {
            self.checker
                .not_supported(array_span, "slices taken through raw pointers");
            return Expr::INVALID;
        }
        // Written `r[i..j]`, so a message names that, as `r[i]`.
        let array = self.through_pointer(array, tree.exprs.span(indexed));
        let ty = self.body.exprs[array].ty;
        match self.checker.types[ty] {
            Type::Array { .. } | Type::Slice { .. } => {
                slices::check_slice(self, mutable, array, ty, [start, end], inclusive, range)
            }
            // The ends are checked as any slice's.
            Type::Parameter { .. } | Type::Undecided => {
                slices::check_slice_ends(self, [start, end], inclusive, ty, None, range);
                Expr::UNDECIDED
            }
            Type::Invalid => Expr::INVALID,
            Type::Void
            | Type::Bool
            | Type::Integer(_)
            | Type::Float(_)
            | Type::CString
            | Type::Str
            | Type::Function { .. }
            | Type::Alias { .. }
            | Type::Named { .. }
            | Type::Struct { .. }
            | Type::Range { .. }
            | Type::Reference { .. }
            | Type::Interface { .. }
            | Type::InterfaceReference { .. }
            | Type::RawPointer { .. } => {
                let message = format!("`{}` cannot be sliced", self.checker.type_text(ty));
                self.checker
                    .error(diagnostics::INVALID_OPERATION, message, array_span);
                Expr::INVALID
            }
        }
    }

    /// `reference.*`, written at `span`: the value a reference, an interface
    /// reference or a typed raw pointer points to.
    #[deny(
        clippy::wildcard_enum_match_arm,
        clippy::match_wildcard_for_single_variants
    )]
    pub(super) fn check_dereference(&mut self, reference: Id<ast::Expr>, span: Span) -> Expr {
        let reference = self.add_expr(reference, None);
        let ty = self.body.exprs[reference].ty;
        let help = match self.checker.types[ty] {
            Type::Reference { target, .. }
            | Type::InterfaceReference {
                interface: target, ..
            }
            | Type::RawPointer {
                target: Some(target),
            } => return self.dereference(reference, target),
            Type::Parameter { .. } | Type::Undecided => return Expr::UNDECIDED,
            Type::Invalid => return Expr::INVALID,
            Type::Slice { .. } => {
                let file = &self.checker.module().ast.file;
                Some(slices::lend_again_help(
                    file.slice(self.body.exprs.span(reference)),
                ))
            }
            Type::Void
            | Type::Bool
            | Type::Integer(_)
            | Type::Float(_)
            | Type::CString
            | Type::Str
            | Type::Function { .. }
            | Type::Alias { .. }
            | Type::Named { .. }
            | Type::Struct { .. }
            | Type::Array { .. }
            | Type::Range { .. }
            | Type::Interface { .. }
            | Type::RawPointer { target: None } => None,
        };
        let message = format!(
            "`.*` needs a reference or a typed raw pointer, found `{}`",
            self.checker.type_text(ty)
        );
        let file = &self.checker.module().ast.file;
        let mut error =
            Diagnostic::new(diagnostics::INVALID_OPERATION, message).label(file, span, "");
        if let Some(help) = help {
            error = error.help(help, Vec::new());
        }
        self.checker.report(error);
        Expr::INVALID
    }

    /// `value`, or what it refers to when it is a reference or a typed raw
    /// pointer, added at `span`: a field or an element is reached through
    /// the pointer, and a reference lent again lends what it refers to.
    pub(crate) fn through_pointer(&mut self, value: Id<Expr>, span: Span) -> Id<Expr> {
        let Some(target) = self.checker.types[self.body.exprs[value].ty].pointee() else {
            return value;
        };
        let referred = self.dereference(value, target);
        self.body.add(referred, span)
    }

    /// What `reference`, whose type refers to `target`, refers to: a place.
    fn dereference(&mut self, reference: Id<Expr>, target: TypeId) -> Expr {
        let span = self.body.exprs.span(reference);
        // A pointer only names its target, which may hold it (`*Node` in
        // `Node`): reaching through it uses the target as a value, invalid
        // for a struct with an error, where lowering stops.
        let target = self.checker.value_type_at(target, span);
        let ty = match self.checker.types[self.body.exprs[reference].ty] {
            Type::RawPointer { .. } if target != TypeId::INVALID => {
                let held = self.checker.reached_through_pointer(target, span);
                held.unwrap_or(TypeId::INVALID)
            }
            _ => target,
        };
        // Lowering takes a place only from a variable or a temporary.
        self.make_place(reference);
        Expr {
            ty,
            kind: ExprKind::Dereference(reference),
        }
    }
}
