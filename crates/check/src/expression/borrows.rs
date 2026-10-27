//! Checking what lends a place: `&` and `&mut`, `@`, a reference lent
//! again, and whether a place can be lent or written.

use arena::Id;
use diagnostics::Diagnostic;
use model::semantic_types::Type;
use model::semantic_types::TypeId;
use model::thir::Expr;
use model::thir::ExprKind;
use model::thir::Variable;
use source::Span;

use crate::builder::ThirBuilder;

impl ThirBuilder<'_, '_> {
    /// A `&place` or `&mut place` reference to the checked place `place`, in a
    /// variable, reported at `span`. `&mut` needs a place that can be written.
    pub(super) fn check_borrow(&mut self, mutable: bool, place: Id<Expr>, span: Span) -> Expr {
        let target = self.body.exprs[place].ty;
        let ty = self.checker.reference_type(mutable, target, span);
        if ty == TypeId::INVALID {
            return Expr::INVALID;
        }
        self.borrow_as(ty, place, span)
    }

    /// A borrow of the checked place `place` as `ty`, a reference or slice,
    /// reported at `span` (`check_lending`).
    pub(super) fn borrow_as(&mut self, ty: TypeId, place: Id<Expr>, span: Span) -> Expr {
        if !self.check_lending(ty, place, span) {
            return Expr::INVALID;
        }
        Expr {
            ty,
            kind: ExprKind::Borrow(place),
        }
    }

    /// Whether the checked place `place` can be lent as `ty`, a reference or
    /// slice, written at `span`: only a place in a variable can. A `&mut` or
    /// `[&mut]` needs a place that can be written, else that is reported but
    /// the place is still lent, so checking goes on; a slice place is lent
    /// again, so its own type decides, never the variable holding it.
    pub(super) fn check_lending(&mut self, ty: TypeId, place: Id<Expr>, span: Span) -> bool {
        let Some((variable, decider)) = self.place_root(place) else {
            self.checker
                .not_supported(span, "borrows of values that are not in a variable");
            return false;
        };
        let types = &self.checker.types;
        let decider = match types[self.body.exprs[place].ty] {
            Type::Slice { .. } => Some(place),
            _ => decider,
        };
        if types[ty].borrows() == Some(true) {
            self.report_unwritable(place, variable, decider, "mutably borrow");
        }
        true
    }

    /// `@place`, written at `span`: the address of the checked place
    /// `place`, in a variable, which must be a place that can be written.
    pub(super) fn check_address(&mut self, place: Id<Expr>, span: Span) -> Expr {
        let target = self.body.exprs[place].ty;
        let ty = self.checker.raw_pointer_type(target, span);
        if ty == TypeId::INVALID {
            return Expr::INVALID;
        }
        let Some((variable, decider)) = self.place_root(place) else {
            self.checker
                .not_supported(span, "addresses of values that are not in a variable");
            return Expr::INVALID;
        };
        self.report_unwritable(place, variable, decider, "take the address of");
        Expr {
            ty,
            kind: ExprKind::Address(place),
        }
    }

    /// The variable that the place `place` starts from, and the pointer on
    /// its path that decides whether it can be written: up to the nearest
    /// raw pointer, a shared reference or slice if any, else the nearest
    /// reference, slice or that raw pointer. `None` for a value not in a
    /// variable. A slice's element is behind the slice, as a reference's
    /// target is behind it.
    pub(crate) fn place_root(&self, place: Id<Expr>) -> Option<(Id<Variable>, Option<Id<Expr>>)> {
        let pointer = |reference: Id<Expr>| &self.checker.types[self.body.exprs[reference].ty];
        let shared = |reference| pointer(reference).borrows() == Some(false);
        let mut root = place;
        let mut deciding = None;
        // Behind a raw pointer, which can always be written, what holds the
        // pointer decides nothing.
        let mut behind_raw = false;
        loop {
            match self.body.exprs[root].kind {
                ExprKind::Index { array: slice, .. }
                    if matches!(pointer(slice), Type::Slice { .. }) =>
                {
                    if !behind_raw && deciding.is_none_or(|kept| !shared(kept) && shared(slice)) {
                        deciding = Some(slice);
                    }
                    root = slice;
                }
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
    /// place: through `decider` (`place_root`) only when that is a `&mut`,
    /// a `[&mut]` or a raw pointer, else in the variable's own storage only
    /// when it is `mut`.
    pub(crate) fn report_unwritable(
        &mut self,
        place: Id<Expr>,
        variable: Id<Variable>,
        decider: Option<Id<Expr>>,
        action: &str,
    ) {
        let file = &self.checker.module().ast.file;
        let place_span = self.body.exprs.span(place);
        let place_text = file.slice(place_span);
        let (written, declared, label, help) = if let Some(decider) = decider {
            let ty = self.body.exprs[decider].ty;
            let types = &self.checker.types;
            if types[ty].borrows() == Some(true) || matches!(types[ty], Type::RawPointer { .. }) {
                return;
            }
            let span = self.body.exprs.span(decider);
            let declared = match self.body.exprs[decider].kind {
                ExprKind::Variable(variable) => self.body.variables.span(variable),
                _ => span,
            };
            let holder = file.slice(span);
            let (kind, writable) = match types[ty] {
                Type::Slice { .. } => ("slice", "`[&mut]` slice"),
                _ => ("reference", "`&mut` reference"),
            };
            // Only a slice lends itself again.
            let written = if decider == place {
                format!("the elements of shared slice `{holder}`")
            } else {
                format!("`{place_text}` through shared {kind} `{holder}`")
            };
            let help = format!("make `{holder}` a {writable}");
            let label = format!("declared as a shared {kind} here");
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
            let label = "declared without `mut` here".to_string();
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

    /// Makes `expr`, when it is a `&mut` or `[&mut]` in a variable, lend it
    /// again, so it is not moved: the variable stays usable after the call
    /// or loop that takes `expr`.
    pub(crate) fn lend_variable_again(&mut self, expr: Id<Expr>) {
        if let Expr {
            ty,
            kind: ExprKind::Variable(_),
        } = self.body.exprs[expr]
            && self.checker.types[ty].borrows() == Some(true)
        {
            let span = self.body.exprs.span(expr);
            let reference = self.body.exprs[expr].clone();
            self.body.exprs[expr] = self.reborrow(reference, span, ty);
        }
    }

    /// `reference`, a reference or slice checked at `span`, lent again as
    /// `to` (`Conversion::Reborrow`, or its own type), so a `&mut` or
    /// `[&mut]` itself is not moved. A written `&place` or `&a[i..j]`,
    /// already checked, borrows its place itself as `to`; a slice lends its
    /// own elements.
    pub(super) fn reborrow(&mut self, reference: Expr, span: Span, to: TypeId) -> Expr {
        let place = match (&reference.kind, &self.checker.types[reference.ty]) {
            (ExprKind::Borrow(_) | ExprKind::Slice { .. }, _) => {
                return Expr {
                    ty: to,
                    ..reference
                };
            }
            (_, Type::Slice { .. }) => self.body.add(reference, span),
            (_, &Type::Reference { target, .. }) => {
                let reference = self.body.add(reference, span);
                let referred = self.dereference(reference, target);
                if referred.ty == TypeId::INVALID {
                    return Expr::INVALID;
                }
                self.body.add(referred, span)
            }
            _ => unreachable!("only a reference or slice is lent again"),
        };
        self.borrow_as(to, place, span)
    }
}
