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
    /// A `&place` or `&mut place` reference to the checked place `place`,
    /// reported at `span`. `&mut` needs a place that can be written.
    pub(super) fn check_borrow(&mut self, mutable: bool, place: Id<Expr>, span: Span) -> Expr {
        let target = self.body.exprs[place].ty;
        let ty = self.checker.reference_type(mutable, target, span);
        if ty == TypeId::INVALID {
            return Expr::INVALID;
        }
        self.borrow_as(ty, place)
    }

    /// A borrow of the checked place `place` as `ty`, a reference or slice
    /// (`check_lending`).
    pub(super) fn borrow_as(&mut self, ty: TypeId, place: Id<Expr>) -> Expr {
        self.check_lending(ty, place);
        Expr {
            ty,
            kind: ExprKind::Borrow(place),
        }
    }

    /// Makes the checked place `place` one that can be lent as `ty`, a
    /// reference or slice (`make_place`). A `&mut` or `[&mut]` needs a place
    /// that can be written, else that is reported but the place is still
    /// lent, so checking goes on; a slice place is lent again, so its own
    /// type decides, never the variable holding it.
    pub(super) fn check_lending(&mut self, ty: TypeId, place: Id<Expr>) {
        let (variable, decider) = self.make_place(place);
        let types = &self.checker.types;
        let decider = match types[self.body.exprs[place].ty] {
            Type::Slice { .. } => Some(place),
            _ => decider,
        };
        if types[ty].borrows() == Some(true) {
            self.report_unwritable(place, variable, decider, "mutably borrow");
        }
    }

    /// `@place`, written at `span`: the address of the checked place
    /// `place`, which must be a place that can be written and not in a
    /// temporary's own storage.
    pub(super) fn check_address(&mut self, place: Id<Expr>, span: Span) -> Expr {
        let target = self.body.exprs[place].ty;
        let ty = self.checker.raw_pointer_type(target, span);
        if ty == TypeId::INVALID {
            return Expr::INVALID;
        }
        let (start, decider) = self.place_root(place);
        let variable = match self.body.exprs[start].kind {
            ExprKind::Variable(variable) => variable,
            // Behind a reference or pointer that a temporary holds.
            ExprKind::Temporary { variable, .. } if decider.is_some() => variable,
            // A temporary is gone when its statement ends, and no loan follows
            // a raw pointer to report a later use (Rust's E0745).
            _ => {
                let message = "cannot take address of a temporary value";
                self.checker
                    .error(diagnostics::INVALID_OPERATION, message, span);
                return Expr::INVALID;
            }
        };
        self.report_unwritable(place, variable, decider, "take the address of");
        Expr {
            ty,
            kind: ExprKind::Address(place),
        }
    }

    /// The variable the checked place `place` starts from and the pointer
    /// that decides whether it can be written (`place_root`), first storing
    /// a value it starts from that is not in a variable (a call's result, a
    /// literal) in a temporary: only a place can be lent, indexed or gone
    /// through.
    pub(super) fn make_place(&mut self, place: Id<Expr>) -> (Id<Variable>, Option<Id<Expr>>) {
        let (start, decider) = self.place_root(place);
        if let Some(variable) = self.start_variable(start) {
            return (variable, decider);
        }
        // The value moves to a new id and its own becomes the temporary, so
        // each expression holding it now holds the temporary.
        let value = self.body.exprs[start].clone();
        let (ty, span) = (value.ty, self.body.exprs.span(start));
        let value = self.body.add(value, span);
        let variable = self.add_hidden_variable(ty, true, span);
        self.body.exprs[start] = Expr {
            ty,
            kind: ExprKind::Temporary {
                variable,
                value,
                kept: false,
            },
        };
        (variable, decider)
    }

    /// The variable that `start`, where a place starts (`place_root`), is
    /// in: its own or a temporary's; `None` for a value in neither.
    pub(crate) fn start_variable(&self, start: Id<Expr>) -> Option<Id<Variable>> {
        match self.body.exprs[start].kind {
            ExprKind::Variable(variable) | ExprKind::Temporary { variable, .. } => Some(variable),
            _ => None,
        }
    }

    /// Where the place `place` starts: a variable, a temporary or a value in
    /// neither; and the pointer on its path that decides whether it can be
    /// written: up to the nearest raw pointer, a shared reference or slice if
    /// any, else the nearest reference, slice or that raw pointer. A slice's
    /// element is behind the slice, as a reference's target is behind it.
    pub(crate) fn place_root(&self, place: Id<Expr>) -> (Id<Expr>, Option<Id<Expr>>) {
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
                _ => return (root, deciding),
            }
        }
    }

    /// Why `expr`, when it reads a field that is no place, an array's or a
    /// slice's `len` (`check_field`), cannot be written.
    pub(crate) fn fixed_length(&self, expr: Id<Expr>) -> Option<&'static str> {
        match self.body.exprs[expr].kind {
            ExprKind::Length(value)
                if matches!(
                    self.checker.types[self.body.exprs[value].ty],
                    Type::Slice { .. }
                ) =>
            {
                Some("a slice's `len` cannot be set")
            }
            ExprKind::Constant(_) | ExprKind::Length(_) => Some("an array's `len` cannot be set"),
            _ => None,
        }
    }

    /// Whether the `decider` of a place (`place_root`) lets it be written: a
    /// `&mut`, a `[&mut]` or a raw pointer.
    pub(super) fn writes_through(&self, decider: Id<Expr>) -> bool {
        let types = &self.checker.types;
        let ty = self.body.exprs[decider].ty;
        types[ty].borrows() == Some(true) || matches!(types[ty], Type::RawPointer { .. })
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
        let (written, declared, help) = if let Some(decider) = decider {
            if self.writes_through(decider) {
                return;
            }
            let ty = self.body.exprs[decider].ty;
            let types = &self.checker.types;
            let holder = file.slice(self.body.exprs.span(decider));
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
            // Only a variable is declared; another holder (a temporary, a
            // pointer's target) is named in the message, inside the place.
            let declared = match self.body.exprs[decider].kind {
                ExprKind::Variable(variable) => Some((
                    self.body.variables.span(variable),
                    format!("declared as a shared {kind} here"),
                )),
                _ => None,
            };
            (written, declared, help)
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
            (written, Some((declared, label)), help)
        };
        let message = format!("cannot {action} {written}");
        let mut error =
            Diagnostic::new(diagnostics::INVALID_ASSIGNMENT, message).label(file, place_span, "");
        if let Some((declared, label)) = declared {
            error = error.label(file, declared, label);
        }
        self.checker.report(error.help(help, Vec::new()));
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
                self.body.add(referred, span)
            }
            _ => unreachable!("only a reference or slice is lent again"),
        };
        self.borrow_as(to, place)
    }
}
