//! Pipes: `value |> f(arguments)` is checked as the call `f(value, arguments)`.

use crate::builder;
use arena::Id;
use model::thir;
use source::Span;
use tree::ast;

/// `value |> target` at `span`: its target must be a call, which is checked
/// with the value as its first written argument, so a method takes it after
/// its `self`.
pub(crate) fn check_pipe(
    builder: &mut builder::ThirBuilder<'_, '_>,
    value: Id<ast::Expr>,
    target: Id<ast::Expr>,
    span: Span,
) -> thir::Expr {
    let tree = &builder.checker.module().ast;
    let ast::Expr::Call {
        callee,
        type_arguments,
        arguments,
    } = tree.exprs[target]
    else {
        let message = "a pipe's target must be a call";
        builder.checker.error(
            diagnostics::INVALID_OPERATION,
            message,
            tree.exprs.span(target),
        );
        builder.add_expr(value, None); // for its own errors
        return thir::Expr::INVALID;
    };
    let mut given = vec![value];
    given.extend(tree.children.lists.items(arguments));
    builder.check_call(callee, type_arguments, &given, span)
}

#[cfg(test)]
mod tests {
    //! Checking pipes as calls.

    use crate::testing;

    /// A pipe is checked as the call of its target with the value first.
    #[test]
    fn checked_as_calls() {
        let (found, reported) =
            testing::bodies("fn g(a: i32, b: i32) {}\nfn f(a: i32) { a |> g(2); }");
        assert!(reported.is_empty(), "{reported:?}");
        assert!(found[1].contains("(a.0, 2:i32)"), "{found:?}");
        let (_, reported) = testing::bodies(
            "fn g(a: i32) -> i32 { return a; }\nfn f(a: bool) { a |> g(); 1 |> g(2); 1 |> g; 1 |> g().x; }",
        );
        let found: Vec<(&str, &str)> = (reported.iter())
            .map(|(_, message, text)| (message.as_str(), text.as_str()))
            .collect();
        assert_eq!(
            found,
            [
                ("mismatched types: expected `i32`, found `bool`", "a"),
                ("`g` takes 1 argument, but 2 were given", "1 |> g(2)"),
                ("a pipe's target must be a call", "g"),
                ("a pipe's target must be a call", "g().x"),
            ]
        );
    }
}
