//! Copies of a declaration with type or `const` parameters, a function's
//! (`fn Max<T>`) or a type's (`struct Box<T>`): each list of arguments
//! makes its own copy, checked once, with what its parameters are given.
//! What a copy is, each kind of declaration checks itself: a function its
//! signature, then its body (`next_copy`); a type its declaration.

use std::sync;

use arena::Id;
use arena::List;
use diagnostics::Diagnostic;
use model::ConstArguments;
use model::DeclarationCopy;
use model::DefId;
use model::ModuleId;
use model::semantic_types;
use model::thir;
use source::Span;
use tree::ast;

use crate::COPY_DEPTH_LIMIT;
use crate::Checker;
use crate::Checking;
use crate::Copied;

impl Checker<'_> {
    /// The type of `copy`, asked for at `used_at`: given nothing, its
    /// declaration's own (`function_type`, `declared_type`); else the type
    /// of its copy for what it is given, made the first time it is asked
    /// for, and for a function kept for the compile to build its body
    /// (`next_copy`). Invalid when the declaration's own check, or the
    /// copy's, has an error, or when copies are made inside each other too
    /// deep (`COPY_DEPTH_LIMIT`), reported at `used_at`. A copy is made only
    /// for known types: one given an open type is not (`open_struct`,
    /// `Types::substitute`).
    pub fn copy_type(&mut self, copy: DeclarationCopy, used_at: Span) -> semantic_types::TypeId {
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
            semantic_types::TypeId::INVALID,
            check,
        );
        if copy.arguments == ConstArguments::NONE || own == semantic_types::TypeId::INVALID {
            return own;
        }
        if let Some(&Copied { ty, .. }) = self.internals.copies.get(&copy) {
            // No type yet: asked for again inside its own check, as a
            // `where` or a field can.
            return ty.unwrap_or_else(|| {
                let mut checking = self.internals.declarations_being_checked.iter();
                let start = (checking.position(|checking| checking.copy() == copy))
                    .expect("a copy without a type is being checked");
                self.report_cycle(start, used_at);
                semantic_types::TypeId::INVALID
            });
        }
        let made_inside = self
            .internals
            .declarations_being_checked
            .last()
            .expect("a use is being checked")
            .copy();
        let (depth, reported_in) = match self.internals.copies.get(&made_inside) {
            Some(inside) => (inside.depth + 1, inside.reported_in),
            None => (1, self.current_module()),
        };
        if depth > COPY_DEPTH_LIMIT {
            // Reported once: the declaration that made the copy has the error.
            if std::mem::replace(&mut self.internals.copies_too_deep, true) {
                return semantic_types::TypeId::INVALID;
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
                ty: None,
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
        if ty == semantic_types::TypeId::INVALID {
            // Not kept: another use that asks for it is told why as well.
            self.internals.copies.remove(&copy);
            return ty;
        }
        let copied = (self.internals.copies.get_mut(&copy)).expect("the copy was just made");
        copied.ty = Some(ty);
        if has_body {
            self.internals.copies_to_build.push(copy);
        }
        ty
    }

    /// `named`, the struct `definition` given the open `arguments`
    /// (`Types::is_open`), held by value at `span`. No copy is made: its
    /// size and checks need what each copy gives. Its own check is, and
    /// what its fields hold given `arguments` (`field_types_given`), so a
    /// struct that holds itself through them, as `Two` holds `Box<Two<[2]T>>`,
    /// is found once, in its own check, whatever `T` is. Invalid when either
    /// has an error, reported.
    pub(crate) fn open_struct(
        &mut self,
        named: semantic_types::TypeId,
        definition: DefId,
        arguments: ConstArguments,
        span: Span,
    ) -> semantic_types::TypeId {
        let own = DeclarationCopy {
            definition,
            arguments: ConstArguments::NONE,
        };
        if self.copy_type(own, span) == semantic_types::TypeId::INVALID {
            return semantic_types::TypeId::INVALID;
        }
        let fields = self.field_types_given(definition, arguments, span);
        if fields.contains(&semantic_types::TypeId::INVALID) {
            return semantic_types::TypeId::INVALID;
        }
        named
    }

    /// The types of the fields of the struct `definition`, in order, given
    /// `arguments`, asked for at `span`: each as its declaration writes it,
    /// read again with them (`resolve_value_type`), recording nothing, once
    /// for each `arguments`. For a struct given an open type, which has no
    /// copy to hold them.
    pub(crate) fn field_types_given(
        &mut self,
        definition: DefId,
        arguments: ConstArguments,
        span: Span,
    ) -> &[semantic_types::TypeId] {
        let given = DeclarationCopy {
            definition,
            arguments,
        };
        if self.internals.open_struct_fields.contains_key(&given) {
            return &self.internals.open_struct_fields[&given];
        }
        let tree = &self.modules[definition.module.index()].ast;
        let ast::Decl::Type(written) = tree.decls[tree.declaration(definition.index)] else {
            unreachable!("only a struct declaration is given type arguments");
        };
        let ast::Type::Struct { fields } = tree.types[written.ty] else {
            unreachable!("only a struct declaration is given type arguments");
        };
        let written: Vec<Id<ast::Type>> = (tree.children.lists.items(fields))
            .map(|field| tree.children.fields[field].ty)
            .collect();
        let checking = Checking {
            definition,
            arguments,
            used_at: span,
            records_facts: false,
        };
        let fields = self.checking_in(checking, |checker| {
            (written.into_iter())
                .map(|ty| checker.resolve_value_type(ty))
                .collect()
        });
        self.internals
            .open_struct_fields
            .entry(given)
            .or_insert(fields)
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

    /// What the declaration `definition` is checked with outside any copy
    /// (`declaration_copy_parameters`): each parameter that stands for a
    /// type, `<T>` or `const T: type`, is itself (`Type::Parameter`), so
    /// what depends on it each copy decides; a `const` value's place holds
    /// no value (`declare_parameters`). Nothing when it has none.
    pub(crate) fn own_arguments(&mut self, definition: DefId) -> ConstArguments {
        let parameters: Vec<Option<Span>> =
            model::program::declaration_copy_parameters(self.modules, definition).collect();
        if parameters.is_empty() {
            return ConstArguments::NONE;
        }
        let given: Vec<semantic_types::ConstArgument> = (parameters.iter().zip(0..))
            .map(|(parameter, place)| {
                let ty = match parameter {
                    Some(_) => {
                        (self.types).intern(semantic_types::Type::Parameter { definition, place })
                    }
                    None => semantic_types::TypeId::INVALID,
                };
                semantic_types::ConstArgument::Type(ty)
            })
            .collect();
        self.types.const_arguments(&given)
    }

    /// The types `written` at `span` after `name`, the type declaration
    /// `definition`: `Box<i32>`, or none, as a bare `Box` is written. `None`
    /// after reporting that their count is not its type parameters' count
    /// (`declaration_copy_parameters`).
    pub(crate) fn written_type_arguments(
        &mut self,
        definition: DefId,
        name: &str,
        written: List<ast::Type>,
        span: Span,
    ) -> Option<Vec<Id<ast::Type>>> {
        let count = model::program::declaration_copy_parameters(self.modules, definition).count();
        let written: Vec<Id<ast::Type>> = self.module().ast.children.lists.items(written).collect();
        if written.len() == count {
            return Some(written);
        }
        let message = if count == 0 {
            format!("`{name}` takes no type arguments")
        } else {
            crate::argument_count_message(name, "type argument", count, false, written.len())
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
                .any(|label| sync::Arc::ptr_eq(&label.file, file) && label.span == used_at);
            into.push(if at_use {
                diagnostic
            } else {
                let message = format!("in `{name}`, the copy made here");
                diagnostic.label(file, used_at, message)
            });
        }
    }
}

impl Checker<'_> {
    /// Makes the fact that the name ending at `name_end` is a function
    /// (`record`) the fact that its call there made `copy`, of the type
    /// `ty`. Only a body checked once has it: a copy has no facts, and a
    /// function's own body (`check_body`) makes no copy, so it would have
    /// only those that other calls happened to make.
    pub(crate) fn record_copy(
        &mut self,
        name_end: u32,
        copy: DeclarationCopy,
        ty: semantic_types::TypeId,
    ) {
        let checking = self
            .internals
            .declarations_being_checked
            .last()
            .expect("a declaration is being checked");
        if checking.arguments != ConstArguments::NONE {
            return;
        }
        let definition = checking.definition;
        let names_it = |kind| matches!(kind, thir::FactKind::Name(thir::Symbol::Function(named)) if named == copy.definition);
        let facts = &mut self.module_checks[definition.module.index()].facts;
        let Some(facts) = facts.get_mut(&definition) else {
            return; // none are recorded
        };
        let mut facts = facts.iter_mut().rev();
        // The facts of the call's arguments were recorded after its name's.
        if let Some(fact) = facts.find(|fact| fact.span.end == name_end && names_it(fact.kind)) {
            fact.kind = thir::FactKind::Copy { function: copy, ty };
        }
    }
}
