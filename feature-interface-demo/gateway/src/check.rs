//! The checker, which is core: names and scopes, the phase order, and
//! conversion at every place a type is expected. It checks each function
//! body into THIR, and reaches a feature only through the kind a syntax
//! node or a type holds.

use std::collections::HashMap;
use std::collections::HashSet;

use model::facts::Facts;
use model::facts::Target;
use model::mir::Variable;
use model::mir::VariableId;
use model::span::Span;
use model::types::TypeId;

use crate::feature::Code;
use crate::feature::Refusal;
use crate::feature::TypeName;
use crate::thir;
use crate::thir::Data;
use crate::thir::Thir;
use crate::thir::ThirId;
use crate::thir::ThirKind;
use crate::tree::BlockNode;
use crate::tree::DeclNode;
use crate::tree::ExprNode;
use crate::tree::Kind;
use crate::tree::NodeId;
use crate::tree::StmtNode;
use crate::tree::Tree;
use crate::tree::TypeNode;
use crate::types::Field;
use crate::types::Pointee;
use crate::types::TypeKind;
use crate::types::Types;

/// A declaration is checked in three passes over all of them: `declare`
/// adds its names, `define` gives its types their parts, `check` checks
/// bodies. A declared type is defined early when another needs its facts.
pub struct DeclarationKind {
    pub declare: fn(&mut Checker<'_>, DeclNode),
    pub define: fn(&mut Checker<'_>, DeclNode),
    pub check: fn(&mut Checker<'_>, DeclNode),
}

pub const UNKNOWN_NAME: Code = Code(1);
pub const MISMATCH: Code = Code(2);
pub const HOLDS_ITSELF: Code = Code(3);
pub const SYNTAX: Code = Code(4);
/// A place that is set, or borrowed `&mut`, through no `mut`.
pub const NOT_MUTABLE: Code = Code(5);
/// The core's own codes; the language stops the build if a feature reuses one.
pub const CODES: &[Code] = &[UNKNOWN_NAME, MISMATCH, HOLDS_ITSELF, SYNTAX, NOT_MUTABLE];

#[derive(Clone, Copy, PartialEq, Eq, Hash, Debug)]
pub struct FunctionId(pub u32);

/// A function's checked body: all lowering builds its MIR from.
pub struct FunctionBody {
    /// Where its name is written.
    pub name: Span,
    /// Its parameters first, then every `let`, in the order declared: the
    /// variables of its MIR, with the same ids.
    pub variables: Vec<Variable>,
    pub parameters: usize,
    /// A block, whose span ends at the function's closing brace.
    pub block: ThirId,
    pub result: TypeId,
}

pub struct Checker<'a> {
    pub source: &'a str,
    pub tree: &'a Tree,
    /// Read by features through `types()`; changed only through the
    /// checker, which keeps every type's facts known after the define pass.
    pub(crate) types: Types,
    pub target: Target,
    thir: Thir,
    scopes: Vec<Vec<(&'a str, VariableId)>>,
    /// The variables of the function being checked.
    variables: Vec<Variable>,
    type_names: HashMap<&'a str, TypeId>,
    /// Each declared type's declaration and name.
    declared: HashMap<TypeId, (DeclNode, NodeId)>,
    defined: HashSet<DeclNode>,
    /// Types whose facts are being computed, to find one that holds itself.
    computing: Vec<TypeId>,
    /// Each function's type by `FunctionId`, once defined.
    function_types: Vec<TypeId>,
    function_ids: HashMap<DeclNode, FunctionId>,
    function_names: HashMap<&'a str, FunctionId>,
    methods: HashMap<(TypeId, &'a str), FunctionId>,
    /// By `FunctionId`, once checked.
    pub(crate) bodies: Vec<Option<FunctionBody>>,
    /// Whether the define pass is over, so every part's facts are known.
    facts_known: bool,
    /// What the function being checked returns, for `return`.
    pub(crate) return_type: TypeId,
    /// How many loops the code being checked is in, for `break`.
    pub(crate) loops: u32,
    pub refusals: Vec<Refusal>,
}

/// Checks a parsed program: the phase order.
pub fn check<'a>(
    source: &'a str,
    tree: &'a Tree,
    declarations: &[DeclNode],
    type_names: &[&'static TypeName],
    target: Target,
) -> Checker<'a> {
    let mut cx = Checker {
        source,
        tree,
        types: Types::default(),
        target,
        thir: Thir::default(),
        scopes: Vec::new(),
        variables: Vec::new(),
        type_names: HashMap::new(),
        declared: HashMap::new(),
        defined: HashSet::new(),
        computing: Vec::new(),
        function_types: Vec::new(),
        function_ids: HashMap::new(),
        function_names: HashMap::new(),
        methods: HashMap::new(),
        bodies: Vec::new(),
        facts_known: false,
        return_type: TypeId::VOID,
        loops: 0,
        refusals: Vec::new(),
    };
    for name in type_names {
        let ty = cx.types.make(name.kind, Box::new([]), name.detail);
        cx.type_names.insert(name.text, ty);
    }
    for &declaration in declarations {
        (declaration_kind(tree, declaration).declare)(&mut cx, declaration);
    }
    for &declaration in declarations {
        cx.define_declaration(declaration);
    }
    for index in 0..cx.types.len() {
        cx.facts(TypeId(index as u32));
    }
    cx.facts_known = true;
    for &declaration in declarations {
        (declaration_kind(tree, declaration).check)(&mut cx, declaration);
    }
    cx
}

fn declaration_kind(tree: &Tree, declaration: DeclNode) -> &'static DeclarationKind {
    match tree.node(declaration.0).kind {
        Kind::Declaration(kind) => kind,
        _ => unreachable!("a DeclNode is a declaration"),
    }
}

impl<'a> Checker<'a> {
    pub fn types(&self) -> &Types {
        &self.types
    }

    pub fn thir(&self) -> &Thir {
        &self.thir
    }

    pub fn text(&self, node: NodeId) -> &'a str {
        let span = self.span(node);
        &self.source[span.start as usize..span.end as usize]
    }

    pub fn span(&self, node: NodeId) -> Span {
        self.tree.node(node).span
    }

    pub fn children(&self, node: NodeId) -> &'a [NodeId] {
        self.tree.children(node)
    }

    pub fn refuse(&mut self, code: Code, node: NodeId, message: String) {
        let span = self.span(node);
        self.refusals.push(Refusal {
            code,
            message,
            span,
        });
    }

    pub fn push(
        &mut self,
        kind: &'static ThirKind,
        ty: TypeId,
        span: Span,
        data: Data,
        children: &[ThirId],
    ) -> ThirId {
        self.thir.push(kind, ty, span, data, children)
    }

    /// What stands for a refused `node`, so its feature still gives a node.
    pub fn invalid(&mut self, node: NodeId) -> ThirId {
        let span = self.span(node);
        self.push(&thir::INVALID, TypeId::INVALID, span, Data::None, &[])
    }

    /// Checks `node` and, when a type is expected there, converts it to that
    /// type by asking the expected type's kind.
    pub fn check_expression(&mut self, node: ExprNode, expected: Option<TypeId>) -> ThirId {
        let Kind::Expression(check) = self.tree.node(node.0).kind else {
            unreachable!("an ExprNode is an expression");
        };
        let found = check(self, node, expected);
        let ty = self.thir.node(found).ty;
        let Some(expected) = expected else {
            return found;
        };
        if ty == expected || ty == TypeId::INVALID || expected == TypeId::INVALID {
            return found;
        }
        let span = self.span(node.0);
        match (self.types.entry(expected).kind.convert)(self, ty, expected) {
            Ok(converter) => self.push(
                &thir::CONVERT,
                expected,
                span,
                Data::Converter(converter),
                &[found],
            ),
            Err(message) => {
                self.refuse(MISMATCH, node.0, message);
                // Of the expected type, so what uses it refuses nothing more.
                self.push(&thir::INVALID, expected, span, Data::None, &[])
            }
        }
    }

    pub fn is_place(&self, value: ThirId) -> bool {
        self.thir.node(value).kind.place.is_some()
    }

    /// Whether `value` is a place that may be set or borrowed `&mut`.
    pub fn is_mutable(&self, value: ThirId) -> bool {
        (self.thir.node(value).kind.place.as_ref())
            .is_some_and(|place| (place.mutable)(self, value))
    }

    /// The variable `value` is, when it is a whole variable.
    pub fn whole_variable(&self, value: ThirId) -> Option<VariableId> {
        let node = self.thir.node(value);
        match node.data {
            Data::Variable(variable) if std::ptr::eq(node.kind, &thir::LOCAL) => Some(variable),
            _ => None,
        }
    }

    /// What `value` points at, as a place, when it is a reference.
    pub fn dereference(&mut self, value: ThirId, span: Span) -> Option<ThirId> {
        let Pointee { ty, mutable } = self.pointee(self.thir.node(value).ty)?;
        Some(self.push(&thir::DEREF, ty, span, Data::Mutable(mutable), &[value]))
    }

    pub fn check_statement(&mut self, node: StmtNode) -> ThirId {
        let Kind::Statement(check) = self.tree.node(node.0).kind else {
            unreachable!("a StmtNode is a statement");
        };
        check(self, node)
    }

    pub fn check_block(&mut self, block: BlockNode) -> ThirId {
        self.scopes.push(Vec::new());
        let statements: Vec<ThirId> = (self.children(block.0).iter())
            .map(|&statement| self.check_statement(StmtNode(statement)))
            .collect();
        self.scopes.pop();
        let span = self.span(block.0);
        self.push(&thir::BLOCK, TypeId::VOID, span, Data::None, &statements)
    }

    pub fn resolve_type(&mut self, node: TypeNode) -> TypeId {
        let Kind::Type(resolve) = self.tree.node(node.0).kind else {
            unreachable!("a TypeNode is a type");
        };
        resolve(self, node)
    }

    /// Starts checking a function's body, which returns `result`; its
    /// parameters are declared next.
    pub fn begin_body(&mut self, result: TypeId) {
        self.variables.clear();
        self.scopes = vec![Vec::new()];
        self.return_type = result;
        self.loops = 0;
    }

    pub fn finish_body(
        &mut self,
        function: FunctionId,
        name: NodeId,
        parameters: usize,
        block: ThirId,
    ) {
        self.scopes.clear();
        self.bodies[function.0 as usize] = Some(FunctionBody {
            name: self.span(name),
            variables: std::mem::take(&mut self.variables),
            parameters,
            block,
            result: self.return_type,
        });
    }

    /// `initialized` when it is set where declared: a parameter, or a
    /// `let` with a value.
    pub fn declare_variable(
        &mut self,
        name: NodeId,
        ty: TypeId,
        mutable: bool,
        initialized: bool,
    ) -> VariableId {
        let variable = VariableId(self.variables.len() as u32);
        self.variables.push(Variable {
            ty,
            mutable,
            initialized,
            span: self.span(name),
            hidden: false,
        });
        let text = self.text(name);
        self.scopes
            .last_mut()
            .expect("variables are declared in a scope")
            .push((text, variable));
        variable
    }

    /// A variable of the function being checked.
    pub fn variable(&self, variable: VariableId) -> &Variable {
        &self.variables[variable.0 as usize]
    }

    pub fn declare_type(
        &mut self,
        name: NodeId,
        kind: &'static TypeKind,
        declaration: DeclNode,
    ) -> TypeId {
        let ty = self.types.declare(kind);
        self.type_names.insert(self.text(name), ty);
        self.declared.insert(ty, (declaration, name));
        ty
    }

    /// The declaration of a declared type, such as a struct.
    pub fn declaration(&self, ty: TypeId) -> DeclNode {
        self.declared[&ty].0
    }

    pub fn define_type(&mut self, ty: TypeId, parts: Box<[TypeId]>) {
        self.types.define(ty, parts);
    }

    /// Runs a declaration's `define` once, in the define pass or earlier
    /// when another declaration needs its type's facts.
    fn define_declaration(&mut self, declaration: DeclNode) {
        if self.defined.insert(declaration) {
            (declaration_kind(self.tree, declaration).define)(self, declaration);
        }
    }

    pub fn type_named(&mut self, name: NodeId) -> TypeId {
        let text = self.text(name);
        match self.type_names.get(text) {
            Some(&ty) => ty,
            None => {
                self.refuse(UNKNOWN_NAME, name, format!("no type is named `{text}`"));
                TypeId::INVALID
            }
        }
    }

    pub fn make_type(
        &mut self,
        kind: &'static TypeKind,
        parts: Box<[TypeId]>,
        detail: u32,
    ) -> TypeId {
        let ty = self.types.make(kind, parts, detail);
        if self.facts_known {
            self.facts(ty);
        }
        ty
    }

    /// The facts of `ty`, computed the first time from its parts' facts. A
    /// declared type is defined first if it was not yet; one that holds
    /// itself is refused.
    pub fn facts(&mut self, ty: TypeId) -> Facts {
        if let Some(facts) = self.types.entry(ty).facts {
            return facts;
        }
        if self.computing.contains(&ty) {
            let text = self.type_text(ty);
            let (_, name) = self.declared[&ty];
            self.refuse(
                HOLDS_ITSELF,
                name,
                format!("{text} holds itself, so it has no size"),
            );
            self.types.set_facts(ty, Facts::NOTHING);
            return Facts::NOTHING;
        }
        self.computing.push(ty);
        if let Some(&(declaration, _)) = self.declared.get(&ty) {
            self.define_declaration(declaration);
        }
        let entry = self.types.entry(ty);
        let (kind, detail, parts) = (entry.kind, entry.detail, entry.parts.clone());
        let part_facts: Vec<Facts> = parts.iter().map(|&part| self.facts(part)).collect();
        let facts = (kind.facts)(detail, &part_facts, self.target);
        self.computing.pop();
        if self.types.entry(ty).facts.is_none() {
            self.types.set_facts(ty, facts);
        }
        self.types.entry(ty).facts.expect("just set")
    }

    /// The facts of a type, once every type's facts are known (after the
    /// define pass).
    pub fn known_facts(&self, ty: TypeId) -> Facts {
        self.types
            .entry(ty)
            .facts
            .expect("every type's facts are known after the define pass")
    }

    pub fn type_text(&self, ty: TypeId) -> String {
        (self.types.entry(ty).kind.text)(self, ty)
    }

    pub fn field(&self, ty: TypeId, name: &str) -> Option<Field> {
        (self.types.entry(ty).kind.field)(self, ty, name)
    }

    pub fn pointee(&self, ty: TypeId) -> Option<Pointee> {
        (self.types.entry(ty).kind.pointee)(self, ty)
    }

    /// Adds the function `declaration` makes, named `name` when it is not
    /// a method; a method is added to its owner once that is known.
    pub fn add_function(&mut self, declaration: DeclNode, name: Option<NodeId>) {
        let id = FunctionId(self.function_types.len() as u32);
        self.function_types.push(TypeId::INVALID);
        self.bodies.push(None);
        self.function_ids.insert(declaration, id);
        if let Some(name) = name {
            self.function_names.insert(self.text(name), id);
        }
    }

    pub fn add_method(&mut self, owner: TypeId, name: NodeId, function: FunctionId) {
        self.methods.insert((owner, self.text(name)), function);
    }

    pub fn function_named(&self, name: &str) -> Option<FunctionId> {
        self.function_names.get(name).copied()
    }

    pub fn function_type(&self, function: FunctionId) -> TypeId {
        self.function_types[function.0 as usize]
    }

    pub fn set_function_type(&mut self, function: FunctionId, ty: TypeId) {
        self.function_types[function.0 as usize] = ty;
    }

    pub fn method(&self, owner: TypeId, name: &str) -> Option<FunctionId> {
        self.methods.get(&(owner, name)).copied()
    }

    /// The function a declaration added.
    pub fn function_of(&self, declaration: DeclNode) -> FunctionId {
        self.function_ids[&declaration]
    }
}

// The core's own syntax: names, written type names, expression statements.

/// `x`: a variable, newest first (inner scopes, and in one scope the latest
/// `let`), or a function.
pub(crate) fn check_name(cx: &mut Checker<'_>, node: ExprNode, _: Option<TypeId>) -> ThirId {
    let text = cx.text(node.0);
    let span = cx.span(node.0);
    let variable = (cx.scopes.iter().rev())
        .flat_map(|scope| scope.iter().rev())
        .find(|(name, _)| *name == text)
        .map(|&(_, variable)| variable);
    if let Some(variable) = variable {
        let ty = cx.variable(variable).ty;
        return cx.push(&thir::LOCAL, ty, span, Data::Variable(variable), &[]);
    }
    if let Some(&function) = cx.function_names.get(text) {
        let ty = cx.function_type(function);
        return cx.push(&thir::FUNCTION, ty, span, Data::Function(function), &[]);
    }
    cx.refuse(
        UNKNOWN_NAME,
        node.0,
        format!("nothing is named `{text}` here"),
    );
    cx.invalid(node.0)
}

/// `i32`, or a declared struct's name: a type named by a feature or a
/// declaration.
pub(crate) fn named_type(cx: &mut Checker<'_>, node: TypeNode) -> TypeId {
    cx.type_named(node.0)
}

/// `expression;`, whose value is not used.
pub(crate) fn check_expression_statement(cx: &mut Checker<'_>, node: StmtNode) -> ThirId {
    let expression = ExprNode(cx.children(node.0)[0]);
    cx.check_expression(expression, None)
}
