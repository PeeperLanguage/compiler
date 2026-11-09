//! Functions: `fn name(parameters) -> type { body }`, `fn Owner::name(&self)`
//! for a method, and calls. A method is a function with a receiver;
//! nothing else about it is special.

use gateway::check::Checker;
use gateway::check::DeclarationKind;
use gateway::check::FunctionId;
use gateway::feature::Code;
use gateway::feature::Feature;
use gateway::syntax::Parsed;
use gateway::syntax::Parser;
use gateway::syntax::Precedence;
use gateway::syntax::Read;
use gateway::syntax::Rule;
use gateway::syntax::Start;
use gateway::syntax::Token;
use gateway::thir::Data;
use gateway::thir::ThirId;
use gateway::thir::ThirKind;
use gateway::tree::BlockNode;
use gateway::tree::DeclNode;
use gateway::tree::ExprNode;
use gateway::tree::Kind;
use gateway::tree::NodeId;
use gateway::tree::Tree;
use gateway::tree::TypeNode;
use gateway::types::TypeKind;
use gateway::types::no_conversions;
use gateway::types::no_fields;
use gateway::types::no_operators;
use gateway::types::no_pointee;
use model::facts::Carrier;
use model::facts::Facts;
use model::mir::InstructionKind;
use model::types::TypeId;

const NOT_CALLABLE: Code = Code(500);
const ARGUMENT_COUNT: Code = Code(501);
const RETURNS_MEMORY: Code = Code(502);
const SELF_OUTSIDE_METHOD: Code = Code(503);
const METHOD_WITHOUT_SELF: Code = Code(504);
const NOT_NAMED: Code = Code(505);

/// `parts` are the parameter types, then the return type.
pub static FUNCTION: TypeKind = TypeKind {
    facts: |_, _, target| Facts::scalar(target.pointer_bytes),
    text: |cx, ty| {
        let (parameters, result) = signature(cx, ty);
        let parameters: Vec<String> = parameters.iter().map(|&ty| cx.type_text(ty)).collect();
        format!("fn({}) -> {}", parameters.join(", "), cx.type_text(result))
    },
    convert: no_conversions,
    operator: no_operators,
    field: no_fields,
    pointee: no_pointee,
};

/// The method `name` of `owner`: its id, parameter types and return type.
pub fn method<'a>(
    cx: &'a Checker<'_>,
    owner: TypeId,
    name: &str,
) -> Option<(FunctionId, &'a [TypeId], TypeId)> {
    let function = cx.method(owner, name)?;
    let (parameters, result) = signature(cx, cx.function_type(function));
    Some((function, parameters, result))
}

/// The parameter types and the return type of the function type `ty`.
pub fn signature<'a>(cx: &'a Checker<'_>, ty: TypeId) -> (&'a [TypeId], TypeId) {
    let parts = &cx.types().entry(ty).parts;
    let (result, parameters) = parts
        .split_last()
        .expect("a function type has a return type");
    (parameters, *result)
}

// A function's children: owner, name, return type, body, receiver (the
// `self` of `&self`), each missing when not written; then a name and a
// type for each other parameter.
const OWNER: usize = 0;
const NAME: usize = 1;
const RETURN: usize = 2;
const BODY: usize = 3;
const RECEIVER: usize = 4;
const PARAMETERS: usize = 5;

/// The names a function's parameters are declared by: `self` when it has a
/// receiver, then the others.
fn parameter_names<'a>(
    tree: &'a Tree,
    children: &'a [NodeId],
) -> impl Iterator<Item = NodeId> + 'a {
    let receiver = Some(children[RECEIVER]).filter(|&receiver| !tree.is_missing(receiver));
    receiver
        .into_iter()
        .chain(children[PARAMETERS..].iter().step_by(2).copied())
}

static FUNCTION_DECLARATION: DeclarationKind = DeclarationKind {
    declare: |cx, node| {
        let children = cx.children(node.0);
        let free = cx
            .tree
            .is_missing(children[OWNER])
            .then_some(children[NAME]);
        cx.add_function(node, free);
    },
    define: define_function,
    check: check_function,
};

fn read_function(parser: &mut Parser<'_>) -> Parsed<DeclNode> {
    let start = parser.advance();
    let first = parser.name()?;
    let (owner, name) = match parser.eat(Token::ColonColon) {
        true => (first, parser.name()?),
        false => (parser.missing(), first),
    };
    parser.expect(Token::LeftParen, "`(`")?;
    let receiver = match parser.eat(Token::Ampersand) {
        true => {
            let receiver = parser.exact_name("self")?;
            if parser.peek() != Token::RightParen {
                parser.expect(Token::Comma, "`,` or `)`")?;
            }
            receiver
        }
        false => parser.missing(),
    };
    let mut parameters = Vec::new();
    parser.list(Token::RightParen, |parser| {
        parameters.push(parser.name()?);
        parser.expect(Token::Colon, "`:`")?;
        parameters.push(parser.ty()?.0);
        Ok(())
    })?;
    let result = match parser.eat(Token::Arrow) {
        true => parser.ty()?.0,
        false => parser.missing(),
    };
    let body = parser.block()?;
    let span = parser.since(start);
    let mut children = vec![owner, name, result, body.0, receiver];
    children.extend(parameters);
    Ok(DeclNode(parser.tree.push(
        Kind::Declaration(&FUNCTION_DECLARATION),
        span,
        &children,
    )))
}

fn define_function(cx: &mut Checker<'_>, node: DeclNode) {
    let children = cx.children(node.0);
    let owner = (!cx.tree.is_missing(children[OWNER])).then(|| cx.type_named(children[OWNER]));
    let id = cx.function_of(node);
    let receiver = !cx.tree.is_missing(children[RECEIVER]);
    let mut parts = Vec::new();
    // Only a function with a receiver is added as a method, so whatever
    // finds a method can count on its `&self`.
    match (owner, receiver) {
        (Some(owner), true) => {
            cx.add_method(owner, children[NAME], id);
            parts.push(references::reference(cx, owner, false));
        }
        (Some(_), false) => {
            // shortcut: a method is called only as `value.name()` in this
            // demo, so it takes `&self`; the real compiler also has
            // `Owner::name()` for functions of a type without a receiver.
            let message = "a method takes `&self` first".to_string();
            cx.refuse(METHOD_WITHOUT_SELF, children[NAME], message);
        }
        (None, true) => {
            let message = "`&self` is only for a method".to_string();
            cx.refuse(SELF_OUTSIDE_METHOD, children[RECEIVER], message);
            parts.push(TypeId::INVALID);
        }
        (None, false) => {}
    }
    for pair in children[PARAMETERS..].chunks(2) {
        parts.push(cx.resolve_type(TypeNode(pair[1])));
    }
    let result = match cx.tree.is_missing(children[RETURN]) {
        true => TypeId::VOID,
        false => cx.resolve_type(TypeNode(children[RETURN])),
    };
    if !matches!(cx.facts(result).carrier, Carrier::Scalar | Carrier::Nothing) {
        // shortcut: values in memory are not returned in this demo; the real
        // compiler returns them through a place the caller gives.
        cx.refuse(
            RETURNS_MEMORY,
            children[RETURN],
            "this demo returns only scalars".to_string(),
        );
    }
    parts.push(result);
    let ty = cx.make_type(&FUNCTION, parts.into(), 0);
    cx.set_function_type(id, ty);
}

fn check_function(cx: &mut Checker<'_>, node: DeclNode) {
    let children = cx.children(node.0);
    let id = cx.function_of(node);
    let (parameters, result) = signature(cx, cx.function_type(id));
    let parameters = parameters.to_vec();
    cx.begin_body(result);
    for (name, &ty) in parameter_names(cx.tree, children).zip(&parameters) {
        // Set by the caller, and never `mut`.
        cx.declare_variable(name, ty, false, true);
    }
    let block = cx.check_block(BlockNode(children[BODY]));
    cx.finish_body(id, children[NAME], parameters.len(), block);
}

/// A call of a function named where it is called.
static CALL: ThirKind = ThirKind {
    lower: |lw, id| {
        let checker = lw.checker;
        let node = checker.thir().node(id);
        let Data::Function(function) = node.data else {
            unreachable!("a call names its function");
        };
        let arguments = (checker.thir().children(id).iter())
            .map(|&argument| lw.value(argument))
            .collect();
        let call = InstructionKind::Call {
            function: function.0,
            arguments,
        };
        lw.emit(call, node.ty)
    },
    place: None,
};

pub fn read_arguments(parser: &mut Parser<'_>, children: &mut Vec<NodeId>) -> Parsed<()> {
    parser.expect(Token::LeftParen, "`(`")?;
    parser.list(Token::RightParen, |parser| {
        children.push(parser.expression(Precedence(0))?.0);
        Ok(())
    })
}

fn read_call(parser: &mut Parser<'_>, callee: ExprNode) -> Parsed<ExprNode> {
    let mut children = vec![callee.0];
    read_arguments(parser, &mut children)?;
    let span = parser.since(parser.tree.node(callee.0).span);
    Ok(ExprNode(parser.tree.push(
        Kind::Expression(check_call),
        span,
        &children,
    )))
}

fn check_call(cx: &mut Checker<'_>, node: ExprNode, _: Option<TypeId>) -> ThirId {
    let children = cx.children(node.0);
    let callee = cx.check_expression(ExprNode(children[0]), None);
    let callee = cx.thir().node(callee);
    if callee.ty == TypeId::INVALID {
        return cx.invalid(node.0);
    }
    if !cx.types().is(callee.ty, &FUNCTION) {
        let text = cx.type_text(callee.ty);
        let message = format!("a value of {text} cannot be called");
        cx.refuse(NOT_CALLABLE, children[0], message);
        return cx.invalid(node.0);
    }
    let Data::Function(function) = callee.data else {
        // shortcut: a function held in a variable is not called in this
        // demo; the real compiler calls through the value.
        let message = "only a function named here can be called".to_string();
        cx.refuse(NOT_NAMED, children[0], message);
        return cx.invalid(node.0);
    };
    call(cx, node.0, function, Vec::new(), &children[1..])
}

/// The call `call` of `function`, whose first parameters take the checked
/// `given` (a method's receiver) and the rest the `written` arguments. A
/// `&mut` argument held in a place is borrowed again, so it is not moved.
pub fn call(
    cx: &mut Checker<'_>,
    call: NodeId,
    function: FunctionId,
    mut given: Vec<ThirId>,
    written: &[NodeId],
) -> ThirId {
    let (parameters, result) = signature(cx, cx.function_type(function));
    let parameters = parameters[given.len()..].to_vec();
    if parameters.len() != written.len() {
        let message = format!(
            "expected {} arguments, found {}",
            parameters.len(),
            written.len()
        );
        cx.refuse(ARGUMENT_COUNT, call, message);
    }
    for (&argument, &parameter) in written.iter().zip(&parameters) {
        let value = cx.check_expression(ExprNode(argument), Some(parameter));
        given.push(references::reborrow(cx, value));
    }
    let span = cx.span(call);
    cx.push(&CALL, result, span, Data::Function(function), &given)
}

pub static FEATURE: Feature = Feature {
    keywords: &["fn"],
    rules: &[
        Rule {
            start: Start::Keyword("fn"),
            then: None,
            read: Read::Declaration(read_function),
        },
        Rule {
            start: Start::Token(Token::LeftParen),
            then: None,
            read: Read::Infix(Precedence(90), read_call),
        },
    ],
    codes: &[
        NOT_CALLABLE,
        ARGUMENT_COUNT,
        RETURNS_MEMORY,
        SELF_OUTSIDE_METHOD,
        METHOD_WITHOUT_SELF,
        NOT_NAMED,
    ],
    type_names: &[],
};
