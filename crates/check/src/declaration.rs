//! Module-scope declarations, without their bodies, one check per category:
//! a type declaration's type and a constant's type and value. A function's
//! type is `functions`'s.

use arena::Id;
use arena::List;
use diagnostics::Diagnostic;
use model::ConstArguments;
use model::DeclarationCopy;
use model::DefId;
use model::layout::layout;
use model::program::BUILTIN_ATTRIBUTE;
use model::program::EXTERN_ATTRIBUTE;
use model::semantic_types;
use model::semantic_types::ConstArgument;
use model::semantic_types::Constant;
use model::semantic_types::Integer;
use model::semantic_types::Type;
use model::semantic_types::TypeId;
use model::semantic_types::Types;
use model::thir::Body;
use model::thir::CheckedConstant;
use model::thir::FactKind;
use model::thir::Symbol;
use source::Span;
use tree::ast;

use crate::Checker;
use crate::Checking;
use crate::builder::ThirBuilder;
use crate::scope;

/// The most references and slices a struct or an array holds, however deep.
// shortcut: the loan check keeps each one apart, so a struct that holds the
// last one twice costs twice as much at each level, and 40 of them need 2^39
// holders; an array keeps one per element. Real structs and arrays of
// references hold a few; lift this if the check ever tracks them by
// lifetime, as Rust does.
const MOST_HOLDERS: usize = 4_096;

impl Checker<'_> {
    /// The type the module's type declaration or alias `definition` declares:
    /// a struct, interface or enum (`declare_written_type`), or the type an
    /// alias names. One with
    /// type parameters, `struct Box<T>`, declares a type in each copy
    /// (`copy_type`); its own check, its `T` a type parameter
    /// (`own_arguments`), reports what is wrong whatever `T` is and gives
    /// its name given nothing (`Type::Named`), which no value has; invalid
    /// when that check reported an error.
    pub(crate) fn check_type_declaration(&mut self, definition: DefId) -> TypeId {
        let tree = &self.module().ast;
        let (written, is_alias) = match tree.decls[tree.declaration(definition.index)] {
            ast::Decl::Type(written) => (written, false),
            ast::Decl::Alias(written) => (written, true),
            _ => unreachable!("`{definition:?}` is a type declaration"),
        };
        if is_alias {
            if !written.type_parameters.is_empty() {
                return self.not_supported(written.name.span, "generic aliases");
            }
            return self.resolve_value_type(written.ty);
        }
        if written.type_parameters.is_empty() || self.in_copy() {
            return self.declare_written_type(definition, written);
        }
        if self.declare_written_type(definition, written) == TypeId::INVALID {
            return TypeId::INVALID;
        }
        self.types.intern(Type::Named {
            definition,
            arguments: ConstArguments::NONE,
        })
    }

    /// The type the module's type declaration `definition`, written
    /// `written`, declares, by the concept its written type belongs to.
    #[deny(
        clippy::wildcard_enum_match_arm,
        clippy::match_wildcard_for_single_variants
    )]
    fn declare_written_type(&mut self, definition: DefId, written: ast::TypeDecl) -> TypeId {
        match self.module().ast.types[written.ty] {
            ast::Type::Enum { .. } => crate::enums::declare_enum_type(self, written),
            ast::Type::Interface { .. } => {
                crate::interfaces::declare_interface_type(self, definition)
            }
            ast::Type::Struct { fields } => {
                crate::structs::declare_struct_type(self, fields, definition, written)
            }
            ast::Type::Name
            | ast::Type::Path(_)
            | ast::Type::Generic { .. }
            | ast::Type::Bracketed { .. }
            | ast::Type::Reference { .. }
            | ast::Type::Slice { .. }
            | ast::Type::Variadic { .. }
            | ast::Type::Function(_)
            | ast::Type::Owner
            | ast::Type::Type
            | ast::Type::Array { .. }
            | ast::Type::Optional(_)
            | ast::Type::Pointer { .. }
            | ast::Type::Invalid => {
                unreachable!("only `struct`, `iface` and `enum` begin a type declaration")
            }
        }
    }

    /// Reports each attribute of the declaration `definition` but an
    /// `extern` or a `builtin` on a function, the attributes there are;
    /// `check_function` checks their shape.
    pub(crate) fn check_attributes(&mut self, definition: DefId) {
        let tree = &self.module().ast;
        let declaration = &tree.decls[tree.declaration(definition.index)];
        let function = matches!(declaration, ast::Decl::Function(_));
        for attribute in tree.children.lists.items(declaration.metadata().attributes) {
            let name = tree.children.attributes[attribute].name.text(&tree.file);
            let message = match name {
                EXTERN_ATTRIBUTE | BUILTIN_ATTRIBUTE if function => continue,
                EXTERN_ATTRIBUTE => {
                    "`extern` names a C function, so only a function has it".to_owned()
                }
                BUILTIN_ATTRIBUTE => {
                    "`builtin` marks a function the compiler answers, so only a function has it"
                        .to_owned()
                }
                _ => format!("unknown attribute `{name}`"),
            };
            let span = tree.children.attributes.span(attribute);
            self.error(diagnostics::INVALID_DECLARATION, message, span);
        }
    }

    /// The type of the `const` value parameter written `ty` of the function
    /// `definition`, for its call at `used_at`. The function's own signature
    /// read it with no error and no type parameter (`signature_type`), so
    /// reading it again reports nothing, and records nothing: its facts are
    /// the signature's.
    pub(crate) fn const_value_type(
        &mut self,
        definition: DefId,
        ty: Id<ast::Type>,
        used_at: Span,
    ) -> TypeId {
        let checking = Checking {
            definition,
            arguments: self.own_arguments(definition),
            used_at,
            records_facts: false,
        };
        self.checking_in(checking, |checker| checker.resolve_value_type(ty))
    }

    /// The type and value of the module's constant `definition`; `None` when
    /// it had an error. Its value is checked as a body's `const` is, with no
    /// variables in scope.
    pub(crate) fn check_constant(&mut self, definition: DefId) -> Option<Constant> {
        let declaration = self.module().ast.declaration(definition.index);
        let mut body = Body::default();
        let checked =
            ThirBuilder::new(self, &mut body, TypeId::VOID).check_const_statement(declaration);
        match checked? {
            CheckedConstant::Known(constant) => Some(constant),
            CheckedConstant::PerCopy(_) => {
                unreachable!("a module's constant is in no generic declaration's own check")
            }
        }
    }

    /// The type of a value whose type is written `ty`: a named type is the
    /// type its declaration declares, so a value's type is complete (an
    /// alias names a complete type already).
    pub(crate) fn resolve_value_type(&mut self, ty: Id<ast::Type>) -> TypeId {
        let resolved = self.resolve_type(ty);
        self.value_type(resolved, ty)
    }

    /// `resolve_value_type` of `resolved`, the type written `ty`. An
    /// interface method's `self` taken whole is the interface being checked,
    /// so it stays a name.
    fn value_type(&mut self, resolved: TypeId, ty: Id<ast::Type>) -> TypeId {
        let tree = &self.module().ast;
        if let ast::Type::Owner = tree.types[ty]
            && self.is_interface(resolved)
        {
            return resolved;
        }
        let span = tree.types.span(ty);
        self.value_type_at(resolved, span)
    }

    /// `resolved`, written at `span`, as a value's type: a named type, or
    /// copy of one, is checked (`copy_type`); one given an open type
    /// (`open_struct`) is not copied.
    pub(crate) fn value_type_at(&mut self, resolved: TypeId, span: Span) -> TypeId {
        match self.types[resolved] {
            Type::Named {
                definition,
                arguments,
            } if self.types.is_open(resolved) => {
                self.open_struct(resolved, definition, arguments, span)
            }
            Type::Named {
                definition,
                arguments,
            } => self.copy_type(
                DeclarationCopy {
                    definition,
                    arguments,
                },
                span,
            ),
            _ => resolved,
        }
    }

    /// Whether `ty` is an interface, checked or only named.
    pub(crate) fn is_interface(&self, ty: TypeId) -> bool {
        match self.types[ty] {
            Type::Interface { .. } => true,
            Type::Named { definition, .. } => {
                model::program::interface_methods(self.modules, definition).is_some()
            }
            _ => false,
        }
    }

    /// The real type of `element`, for an array, slice, reference or raw
    /// pointer that holds or reaches it, so `[2]Meters` is `[2]f64` under an
    /// alias; `None` when it is invalid.
    pub(crate) fn element_type(&self, element: TypeId) -> Option<TypeId> {
        if self.types[element] == Type::Invalid {
            return None;
        }
        Some(self.types.real(element))
    }

    /// The target of a raw pointer type `*target` written at `span`: no
    /// reference, slice or interface reference, as nothing follows a pointer
    /// to say what they point into. A struct or an array holding one is
    /// refused only where it is reached (`reached_through_pointer`).
    pub(crate) fn written_pointer_target(&mut self, target: TypeId, span: Span) -> Option<TypeId> {
        self.pointer_target(target, span, |types, target| types[target].borrows())
    }

    /// What a raw pointer reaches at `span`, with `.*` or `@`: nothing that
    /// holds a loan, for the reason `written_pointer_target` gives.
    pub(crate) fn reached_through_pointer(&mut self, target: TypeId, span: Span) -> Option<TypeId> {
        self.pointer_target(target, span, Types::holds_loans)
    }

    /// `target` through a raw pointer at `span`, refused when `loans` says
    /// it holds one.
    fn pointer_target(
        &mut self,
        target: TypeId,
        span: Span,
        loans: impl Fn(&Types, TypeId) -> Option<bool>,
    ) -> Option<TypeId> {
        let target = self.element_type(target)?;
        if loans(&self.types, target).is_none() {
            return Some(target);
        }
        let what = self.loan_holder_text(target);
        self.not_supported(span, &format!("raw pointers to {what}"));
        None
    }

    /// What `ty`, which holds loans, is, in a message: "slices".
    pub(crate) fn loan_holder_text(&self, ty: TypeId) -> &'static str {
        match self.types[ty] {
            Type::Reference { .. } => "references",
            Type::Slice { .. } => "slices",
            Type::InterfaceReference { .. } => "interface references",
            Type::Interface { .. } => "owned interfaces",
            Type::Array { .. } => "arrays holding references",
            _ => "structs holding references",
        }
    }

    /// `&target`, or `&mut target` when `mutable` (`element_type`); an
    /// interface reference to an interface.
    pub(crate) fn reference_type(&mut self, mutable: bool, target: TypeId) -> TypeId {
        if self.is_interface(target) {
            return self.types.intern(Type::InterfaceReference {
                mutable,
                interface: target,
            });
        }
        match self.element_type(target) {
            Some(target) => self.types.intern(Type::Reference { mutable, target }),
            None => TypeId::INVALID,
        }
    }

    /// `[length]element`, written at `span` (`element_type`), holding at
    /// most `MOST_HOLDERS` references and slices and fitting the target;
    /// an open one's copies are checked for both.
    pub(crate) fn array_type(&mut self, element: TypeId, length: u64, span: Span) -> TypeId {
        let Some(element) = self.element_type(element) else {
            return TypeId::INVALID;
        };
        let ty = self.types.array(element, length);
        if self.types.is_open(ty) {
            return ty;
        }
        if self.reports_too_many_holders(ty, self.types.holders_in(ty), span) {
            return TypeId::INVALID;
        }
        let (size, _) = layout(&self.types, ty, self.target);
        self.fits_target(ty, size, span)
    }

    /// Whether `ty`, written at `span` and holding `holders` references and
    /// slices, holds more than `MOST_HOLDERS`, after reporting it there.
    pub(crate) fn reports_too_many_holders(
        &mut self,
        ty: TypeId,
        holders: usize,
        span: Span,
    ) -> bool {
        if holders <= MOST_HOLDERS {
            return false;
        }
        let text = self.type_text(ty);
        let message = format!("`{text}` holds more than {MOST_HOLDERS} references and slices");
        self.error(diagnostics::INVALID_TYPE, message, span);
        true
    }

    /// `ty`, made at `span`, or invalid after an error there when its `size`
    /// is more bytes than the target allows for a value, which no copy of it
    /// could count.
    pub(crate) fn fits_target(&mut self, ty: TypeId, size: u64, span: Span) -> TypeId {
        let largest = self.target.largest_object();
        if size <= largest {
            return ty;
        }
        let (text, target) = (self.type_text(ty), self.target.name);
        let message = format!(
            "`{text}` is too big for {target}, where a value takes at most {largest} bytes"
        );
        self.error(diagnostics::INVALID_TYPE, message, span)
    }

    /// `range[element]`, written or made at `span`: invalid when `element`
    /// is, reported when it is not an integer.
    pub(crate) fn range_type(&mut self, element: TypeId, span: Span) -> TypeId {
        if element == TypeId::INVALID {
            return element;
        }
        if let Some(range) = self.types.range(element) {
            return range;
        }
        let found = self.type_text(element);
        let message = format!("a range's values must be integers, found `{found}`");
        self.error(diagnostics::INVALID_TYPE, message, span)
    }

    /// Whether an array may have `length` elements, as its `len` is a
    /// `usize` (`check_field`); reported at `span` when not.
    pub(crate) fn length_fits(&mut self, length: &[u64], span: Span) -> bool {
        let usize = Integer {
            signed: false,
            bits: self.target.pointer_bits,
        };
        let fits = usize.fits(length, false);
        if !fits {
            let bits = self.target.pointer_bits;
            let message = format!("an array length must fit `usize`, of {bits} bits");
            self.error(diagnostics::INVALID_TYPE, message, span);
        }
        fits
    }

    /// The type `symbol`, written at `span`, stands for, however it was
    /// reached: every kind of type declaration is `Symbol::Type`, in this
    /// module or another. An error when it is not a type, or a generic
    /// struct named without what it is given (`resolve_type` reads `Box<T>`).
    pub(crate) fn type_named_by(&mut self, symbol: Symbol, span: Span) -> TypeId {
        let what = match symbol {
            Symbol::Builtin(ty) | Symbol::TypeArgument { ty, .. } => return ty,
            Symbol::Type(definition) => {
                let name = self.module().ast.file.slice(span);
                return match self.written_type_arguments(definition, name, List::EMPTY, span) {
                    Some(_) => self.named_type(definition, span),
                    None => TypeId::INVALID, // reported
                };
            }
            Symbol::Function(_) | Symbol::Constant(_) | Symbol::Local(_) => "a value",
            Symbol::Module(_) => "a module",
        };
        let name = self.module().ast.file.slice(span);
        let message = format!("`{name}` is {what}, not a type");
        self.error(diagnostics::NOT_A_TYPE, message, span)
    }

    /// The type the name written at `span` stands for, its name recorded: a
    /// built-in type or a top-level declaration (`resolve_type`).
    fn named_type_at(&mut self, span: Span) -> TypeId {
        let tree = &self.module().ast;
        let name = tree.file.slice(span);
        let symbol = self.resolve_global(name);
        if let Some(symbol) = symbol {
            self.record(span, FactKind::Name(symbol));
        }
        match symbol {
            // Text alone has no fixed size: only a reference to it is a
            // value (`text_view`). A `const` type parameter given `&str`
            // stands for the reference, which is the same type.
            Some(Symbol::Builtin(text))
                if matches!(self.types[text], Type::Str | Type::CString) =>
            {
                let (what, view) = match self.types[text] {
                    Type::Str => ("text", "&str"),
                    _ => ("C text", "&cstr"),
                };
                let message = format!("`{name}` is {what} of any length, so it has no fixed size");
                let label = format!("use `{view}` for {what}");
                let diagnostic = Diagnostic::new(diagnostics::INVALID_TYPE, message)
                    .label(&tree.file, span, label);
                self.report(diagnostic);
                TypeId::INVALID
            }
            Some(symbol) => self.type_named_by(symbol, span),
            None if name == semantic_types::builtin::RANGE => {
                let message = format!("`{name}` needs the type of its values: `{name}[T]`");
                self.error(diagnostics::INVALID_TYPE, message, span)
            }
            None if let Some(what) = unsupported_type(name) => self.not_supported(span, what),
            None => {
                let symbols = self.visible_symbols();
                let types = symbols.filter(|&(_, symbol)| matches!(symbol, Symbol::Type(_)));
                let builtin = semantic_types::builtin::builtin_names();
                let meant = scope::nearest(name, builtin, types.map(|(declared, _)| declared));
                let message = format!("unknown type `{name}`");
                self.report_unknown(diagnostics::UNDEFINED_NAME, span, message, meant);
                TypeId::INVALID
            }
        }
    }

    /// `&text` or `&mut text`, written at `span`, when `target` names `str`
    /// or `cstr`: a reference to text is its own type (`Type::Str`,
    /// `Type::CString`), as it holds a count or reads up to a zero byte, and
    /// is read only. `None` when `target` names other than text, as a `const`
    /// type parameter does, even one named `str`: given `&str`, `&T` is a
    /// reference to the view.
    fn text_view(&mut self, mutable: bool, target: Span, span: Span) -> Option<TypeId> {
        let tree = &self.module().ast;
        let name = tree.file.slice(target);
        let Some(Symbol::Builtin(text)) = self.resolve_global(name) else {
            return None;
        };
        if !matches!(self.types[text], Type::Str | Type::CString) {
            return None;
        }
        self.record(target, FactKind::Name(Symbol::Builtin(text)));
        if !mutable {
            return Some(text);
        }
        let message = format!("`&mut {name}` is not a type: text views are read only");
        let diagnostic = Diagnostic::new(diagnostics::INVALID_TYPE, message).label(
            &tree.file,
            span,
            format!("use `&{name}`"),
        );
        self.report(diagnostic);
        Some(TypeId::INVALID)
    }

    /// `&target` or `&mut target`, of `target` resolved and written at
    /// `target_span`: an interface is referred to as it is, any other type as
    /// a value's type.
    fn reference_to(&mut self, mutable: bool, target: TypeId, target_span: Span) -> TypeId {
        let target = if self.is_interface(target) {
            target
        } else {
            self.value_type_at(target, target_span)
        };
        self.reference_type(mutable, target)
    }

    /// `resolve_type_argument` before it is made a value's type, as
    /// `resolve_type` is.
    pub(crate) fn expression_type(&mut self, expression: Id<ast::Expr>) -> TypeId {
        source::grow_stack(|| {
            let tree = &self.module().ast;
            let span = tree.exprs.span(expression);
            match tree.exprs[expression] {
                ast::Expr::Type(ty) | ast::Expr::Path(ty) => self.resolve_type(ty),
                ast::Expr::Name => self.named_type_at(span),
                ast::Expr::Parentheses(inner) => self.expression_type(inner),
                ast::Expr::Reference { mutable, value } => {
                    let target = tree.exprs.span(value);
                    if let ast::Expr::Name = tree.exprs[value]
                        && let Some(view) = self.text_view(mutable, target, span)
                    {
                        return view;
                    }
                    let resolved = self.expression_type(value);
                    self.reference_to(mutable, resolved, target)
                }
                ast::Expr::Index { value, index }
                    if let ast::Expr::Name = tree.exprs[value]
                        && tree.file.slice(tree.exprs.span(value))
                            == semantic_types::builtin::RANGE =>
                {
                    let element = self.resolve_type_argument(index);
                    let range = self.range_type(element, span);
                    let name = tree.exprs.span(value);
                    self.record(name, FactKind::Name(Symbol::Builtin(range)));
                    range
                }
                ast::Expr::Invalid => TypeId::INVALID, // the parser reported it
                _ => {
                    let text = tree.file.slice(span);
                    let message = format!("`{text}` is a value, not a type");
                    self.error(diagnostics::NOT_A_TYPE, message, span)
                }
            }
        })
    }

    /// The type a type expression of the module's tree stands for, its names
    /// recorded (`record`). A named type stays a name (`Type::Named`) until
    /// its declaration is checked; a value's type is `resolve_value_type`. A
    /// local never names a type
    /// (`ThirBuilder::declare`), so only top-level names are looked up.
    #[deny(
        clippy::wildcard_enum_match_arm,
        clippy::match_wildcard_for_single_variants
    )]
    pub(crate) fn resolve_type(&mut self, ty: Id<ast::Type>) -> TypeId {
        // Types written in types, `& & ... i32`, are a deep recursion.
        source::grow_stack(|| {
            let tree = &self.module().ast;
            let span = tree.types.span(ty);
            let what = match tree.types[ty] {
                ast::Type::Array { length, element } => {
                    return crate::arrays::resolve_array_type(self, length, element, span);
                }
                ast::Type::Enum { .. } => return crate::enums::resolve_enum_type(self, span),
                ast::Type::Interface { .. } => {
                    return crate::interfaces::resolve_interface_type(self, span);
                }
                ast::Type::Optional(_) => {
                    return crate::optionals::resolve_optional_type(self, span);
                }
                ast::Type::Pointer { target } => {
                    return crate::raw_pointers::resolve_pointer_type(self, target, span);
                }
                ast::Type::Struct { .. } => return crate::structs::resolve_struct_type(self, span),
                // `self`'s type: the owner of the function being checked, which
                // has one (`check_function`). An owner that is not a struct
                // was reported by `scope::collect`.
                ast::Type::Owner => {
                    let checking = self
                        .internals
                        .declarations_being_checked
                        .last()
                        .expect("a declaration is being checked");
                    let declaration =
                        model::program::declaration(self.modules, checking.definition);
                    let owner = match declaration {
                        ast::Decl::Function(ast::Function {
                            owner: Some(owner), ..
                        }) => owner,
                        // An interface's method: `self` is the interface.
                        ast::Decl::Type(_) => {
                            return self.types.intern(Type::Named {
                                definition: checking.definition,
                                arguments: ConstArguments::NONE,
                            });
                        }
                        ast::Decl::Function(_) | ast::Decl::Const { .. } | ast::Decl::Alias(_) => {
                            unreachable!("only a type's function and an interface name `self`")
                        }
                    };
                    let owner = self
                        .scope()
                        .owner_named(self.modules, owner.text(&tree.file));
                    return owner.map_or(TypeId::INVALID, |owner| self.named_type(owner, span));
                }
                ast::Type::Name => return self.named_type_at(span),
                ast::Type::Invalid => return TypeId::INVALID, // the parser reported it
                ast::Type::Type => {
                    let message = "`type` is the type of a `const` parameter only: `const T: type`";
                    return self.error(diagnostics::INVALID_TYPE, message, span);
                }
                ast::Type::Path(_) => {
                    return match self.resolve_path(ty) {
                        Some(symbol) => self.type_named_by(symbol, span),
                        None => TypeId::INVALID, // reported
                    };
                }
                // A generic struct's copy, only named, as any struct is: its
                // arguments are named too, so `*Box<Node>` in `Node` is no
                // cycle. A named struct's id becomes its type's once
                // checked, so the copy is the same however it is reached.
                ast::Type::Generic { name, arguments } => {
                    let text = name.text(&tree.file);
                    let definition = match self.resolve_global(text) {
                        Some(Symbol::Type(definition)) => definition,
                        None => return self.named_type_at(name.span), // reports it
                        Some(_) => {
                            let message = format!("`{text}` takes no type arguments");
                            return self.error(diagnostics::WRONG_ARGUMENT_COUNT, message, span);
                        }
                    };
                    self.record(name.span, FactKind::Name(Symbol::Type(definition)));
                    let Some(written) =
                        self.written_type_arguments(definition, text, arguments, span)
                    else {
                        return TypeId::INVALID; // reported
                    };
                    let mut given = Vec::with_capacity(written.len());
                    for argument in written {
                        let resolved = self.resolve_type(argument);
                        let span = self.module().ast.types.span(argument);
                        let resolved = self.type_argument(resolved, span);
                        given.push(ConstArgument::Type(self.types.real(resolved)));
                    }
                    if given.contains(&ConstArgument::Type(TypeId::INVALID)) {
                        return TypeId::INVALID; // reported
                    }
                    let arguments = self.types.const_arguments(&given);
                    return self.types.intern(Type::Named {
                        definition,
                        arguments,
                    });
                }
                ast::Type::Bracketed { name, inner } => {
                    let written = tree.file.slice(name.span);
                    if written != semantic_types::builtin::RANGE {
                        let message = format!("`{written}` takes no type in brackets");
                        return self.error(diagnostics::INVALID_TYPE, message, span);
                    }
                    let element = self.resolve_value_type(inner);
                    let range = self.range_type(element, span);
                    self.record(name.span, FactKind::Name(Symbol::Builtin(range)));
                    return range;
                }
                ast::Type::Reference { mutable, target } => {
                    if let ast::Type::Name = tree.types[target]
                        && let Some(view) = self.text_view(mutable, tree.types.span(target), span)
                    {
                        return view;
                    }
                    let resolved = self.resolve_type(target);
                    return self.reference_to(mutable, resolved, tree.types.span(target));
                }
                ast::Type::Slice { mutable, element }
                | ast::Type::Variadic { mutable, element } => {
                    let element = self.resolve_value_type(element);
                    return match self.element_type(element) {
                        Some(element) => self.types.intern(Type::Slice { mutable, element }),
                        None => TypeId::INVALID,
                    };
                }
                ast::Type::Function(_) => "function types",
            };
            self.not_supported(span, what)
        })
    }
}

/// What to call the built-in type `name` in a "not supported yet" report, if
/// it is one the checker does not handle yet.
pub(crate) fn unsupported_type(name: &str) -> Option<&'static str> {
    match name {
        "string" => Some("string types"),
        "byte" | "char" => Some("byte and char types"),
        _ => None,
    }
}
