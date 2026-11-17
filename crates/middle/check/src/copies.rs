//! Copies of a declaration with type or `const` parameters, a function's
//! (`fn Max<T>`) or a type's (`struct Box<T>`): each list of arguments
//! makes its own copy, checked once, with what its parameters are given.
//! What a copy is, each kind of declaration checks itself: a function its
//! signature, then its body (`next_copy`); a type its declaration.

use std::sync::Arc;

use arena::Id;
use arena::List;
use diagnostics::Diagnostic;
use model::ConstArguments;
use model::DeclarationCopy;
use model::DefId;
use model::ModuleId;
use model::semantic_types::ConstArgument;
use model::semantic_types::Type;
use model::semantic_types::TypeId;
use source::Span;
use syntax::ast;

use crate::COPY_DEPTH_LIMIT;
use crate::Checked;
use crate::Checker;
use crate::Checking;
use crate::Copied;
use crate::expression::calls;

impl<L: Checked> Checker<'_, L> {
    /// The type of `copy`, asked for at `used_at`: given nothing, its
    /// declaration's own (`function_type`, `declared_type`); else the type
    /// of its copy for what it is given, made the first time it is asked
    /// for, and for a function kept for the compile to build its body
    /// (`next_copy`). Invalid when the declaration's own check, or the
    /// copy's, has an error, or when copies are made inside each other too
    /// deep (`COPY_DEPTH_LIMIT`), reported at `used_at`.
    pub fn copy_type(&mut self, copy: DeclarationCopy, used_at: Span) -> TypeId {
        let has_body = matches!(
            model::program::declaration(self.modules, copy.definition),
            ast::Decl::Function(_)
        );
        let check = if has_body {
            Checker::check_function
        } else {
            Checker::check_type_declaration
        };
        let own = self.check_once(
            copy.definition,
            used_at,
            |module| &mut module.declared_types,
            TypeId::INVALID,
            check,
        );
        if copy.arguments == ConstArguments::NONE || own == TypeId::INVALID {
            return own;
        }
        // Named in a type's own check by what names `T`
        // (`named_without_types`): its own check's name, which no value has.
        if copy.arguments == self.own_arguments(copy.definition) {
            return TypeId::INVALID;
        }
        if let Some(&Copied { ty, .. }) = self.internals.copies.get(&copy) {
            // Invalid only while it is checked, as one with an error is not
            // kept: asked for again there, as a `where` or a field can.
            if ty == TypeId::INVALID {
                let mut checking = self.internals.declarations_being_checked.iter();
                let start = (checking.position(|checking| checking.copy() == copy))
                    .expect("a copy without a type is being checked");
                self.report_cycle(start, used_at);
            }
            return ty;
        }
        let made_inside = self
            .internals
            .declarations_being_checked
            .last()
            .expect("a use is being checked")
            .copy();
        // A function's own signature and body, with no type for its `T`,
        // are checked for their names only, so they make no copy. A type's
        // own check does: it finds what its fields hold.
        let made_by_function = matches!(
            model::program::declaration(self.modules, made_inside.definition),
            ast::Decl::Function(_)
        );
        if made_by_function
            && made_inside.arguments != ConstArguments::NONE
            && !self.internals.copies.contains_key(&made_inside)
        {
            return TypeId::INVALID;
        }
        let (depth, reported_in) = match self.internals.copies.get(&made_inside) {
            Some(inside) => (inside.depth + 1, inside.reported_in),
            None => (1, self.current_module()),
        };
        if depth > COPY_DEPTH_LIMIT {
            // Reported once: the declaration that made the copy has the error.
            if std::mem::replace(&mut self.internals.copies_too_deep, true) {
                return TypeId::INVALID;
            }
            let name = model::program::declared_path(self.modules, copy.definition);
            let message = format!(
                "`{name}` makes a new copy of itself inside each copy, without end: \
                 copies nest {COPY_DEPTH_LIMIT} deep here"
            );
            return self.error(diagnostics::CIRCULAR_DEPENDENCY, message, used_at);
        }
        self.internals.copies.insert(
            copy,
            Copied {
                ty: TypeId::INVALID,
                depth,
                reported_in,
                used_in: self.current_module(),
                used_at,
            },
        );
        let module = copy.definition.module.index();
        let reported = self.module_checks[module].diagnostics.len();
        let checking = Checking {
            definition: copy.definition,
            arguments: copy.arguments,
            used_at,
            records_facts: true,
        };
        // A type's copies are checked inside each other's fields.
        let ty = source::grow_stack(|| {
            self.checking_in(checking, |checker| check(checker, copy.definition))
        });
        let user = self.current_module();
        self.report_from_copy(copy, reported, user);
        if ty == TypeId::INVALID {
            // Not kept: another use that asks for it is told why as well.
            self.internals.copies.remove(&copy);
            return ty;
        }
        let copied = (self.internals.copies.get_mut(&copy)).expect("the copy was just made");
        copied.ty = ty;
        if has_body {
            self.internals.copies_to_build.push(copy);
        }
        ty
    }

    /// What `definition` named with an argument that has no type stands
    /// for: one reported, or one that names a `T` no copy gives yet.
    /// Invalid, but in a type's own check, which keeps the declaration
    /// named, as in its own check: what holds it by value checks it
    /// (`copy_type`), so a type that holds itself, as `Two<[2]T>` in `Two`,
    /// is found once, whatever `T` is.
    pub(crate) fn named_without_types(&mut self, definition: DefId) -> TypeId {
        let checking = (self.internals.declarations_being_checked.last())
            .expect("a declaration is being checked");
        let in_own_type_check = checking.arguments != ConstArguments::NONE
            && !self.in_copy()
            && matches!(
                model::program::declaration(self.modules, checking.definition),
                ast::Decl::Type(_)
            );
        if !in_own_type_check {
            return TypeId::INVALID;
        }
        let arguments = self.own_arguments(definition);
        self.types.intern(Type::Named {
            definition,
            arguments,
        })
    }

    /// A copy of a function whose body the compile has not built
    /// (`build_function`), the newest first; building one may make more.
    /// None once copies nested too deep.
    pub fn next_copy(&mut self) -> Option<DeclarationCopy> {
        self.internals
            .copies_to_build
            .pop()
            .filter(|_| !self.internals.copies_too_deep)
    }

    /// What the declaration `definition` is checked with outside any copy:
    /// an invalid type for each of its type and `const` parameters
    /// (`declaration_copy_parameters`), so only a type one is read as one
    /// (`resolve_global`) and reports nothing; nothing when it has none.
    pub(crate) fn own_arguments(&mut self, definition: DefId) -> ConstArguments {
        let count = model::program::declaration_copy_parameters(self.modules, definition).count();
        if count == 0 {
            return ConstArguments::NONE;
        }
        let invalid = ConstArgument::Type(TypeId::INVALID);
        self.types.const_arguments(&vec![invalid; count])
    }

    /// The types `written` at `span` after `name`, the type declaration
    /// `definition`: `Box<i32>`, or none, as a bare `Box` is written. `None`
    /// after reporting that their count is not its type parameters' count
    /// (`declaration_copy_parameters`).
    pub(crate) fn written_type_arguments(
        &mut self,
        definition: DefId,
        name: &str,
        written: List<ast::Type<L>>,
        span: Span,
    ) -> Option<Vec<Id<ast::Type<L>>>> {
        let count = model::program::declaration_copy_parameters(self.modules, definition).count();
        let written: Vec<Id<ast::Type<L>>> =
            self.module().ast.children.lists.items(written).collect();
        if written.len() == count {
            return Some(written);
        }
        let message = if count == 0 {
            format!("`{name}` takes no type arguments")
        } else {
            calls::argument_count_message(name, "type argument", count, false, written.len())
        };
        self.error(diagnostics::WRONG_ARGUMENT_COUNT, message, span);
        None
    }

    /// Moves what checking `copy` reported in its declaration's module,
    /// past the first `reported` diagnostics, to the module `into`, each
    /// marking the use it was made for. Another copy's check may find the
    /// same in the declaration's text: the module keeps the first.
    pub(crate) fn report_from_copy(
        &mut self,
        copy: DeclarationCopy,
        reported: usize,
        into: ModuleId,
    ) {
        let modules = self.modules;
        let definition = copy.definition;
        let found = &mut self.module_checks[definition.module.index()].diagnostics;
        if found.len() == reported {
            return;
        }
        let found: Vec<Diagnostic> = found.drain(reported..).collect();
        let Copied {
            used_in, used_at, ..
        } = self.internals.copies[&copy];
        let name = model::program::copy_name(modules, &self.types, copy);
        let file = &modules[used_in.index()].ast.file;
        let into = &mut self.module_checks[into.index()].diagnostics;
        for diagnostic in found {
            if into.iter().any(|earlier| earlier.reports_same(&diagnostic)) {
                continue;
            }
            // One that marks the use already, as one about the use itself
            // (`constraints_hold`) or a cycle through the copy, says where.
            let at_use = (diagnostic.labels.iter())
                .any(|label| Arc::ptr_eq(&label.file, file) && label.span == used_at);
            into.push(if at_use {
                diagnostic
            } else {
                let message = format!("in `{name}`, the copy made here");
                diagnostic.label(file, used_at, message)
            });
        }
    }
}
