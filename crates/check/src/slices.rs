//! Slices in the checker: `&array[start..end]` and `&mut array[start..end]`,
//! a view of an array's or another slice's elements. The checker gives a
//! `Type::Slice` and an `ExprKind::Slice`, so the phases after it know
//! nothing of this file. Which values can be sliced, the dispatch decides
//! (`expression/places.rs`).

use crate::builder;
use arena::Id;
use model::constant::Value;
use model::semantic_types;
use model::thir;
use tree::ast;

/// The slice, `&mut` when `mutable`, of the elements of `array`, a checked
/// array or slice in a place, of the type `viewed`, from `start` up to `end`,
/// `..=` when `inclusive`, written as the range `range`: each end left out
/// or an integer, as an index is (`check_slice_ends`).
pub(crate) fn check_slice(
    builder: &mut builder::ThirBuilder<'_, '_>,
    mutable: bool,
    array: Id<thir::Expr>,
    viewed: semantic_types::TypeId,
    [start, end]: [Option<Id<ast::Expr>>; 2],
    inclusive: bool,
    range: Id<ast::Expr>,
) -> thir::Expr {
    let (element, length) = builder.checker.types[viewed]
        .elements()
        .expect("only an array or a slice is sliced");
    let ends = check_slice_ends(builder, [start, end], inclusive, viewed, length, range);
    let Some([start, end]) = ends else {
        return thir::Expr::INVALID;
    };
    let slice = (builder.checker.types).intern(semantic_types::Type::Slice { mutable, element });
    builder.check_lending(slice, array);
    thir::Expr {
        ty: slice,
        kind: thir::ExprKind::Slice {
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
pub(crate) fn check_slice_ends(
    builder: &mut builder::ThirBuilder<'_, '_>,
    ends: [Option<Id<ast::Expr>>; 2],
    inclusive: bool,
    viewed: semantic_types::TypeId,
    length: Option<u64>,
    range: Id<ast::Expr>,
) -> Option<[Option<Id<thir::Expr>>; 2]> {
    // No slice is longer than the largest `usize`.
    let limit = length.unwrap_or(u64::MAX >> (64 - builder.checker.target.pointer_bits));
    let usize = Some(builder.checker.types.usize);
    let ends = match ends {
        [Some(start), Some(end)] => {
            let (start, end) = builder.check_range_ends(start, end, usize);
            [Some(start), Some(end)]
        }
        ends => ends.map(|end| end.map(|end| builder.add_expr(end, usize))),
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
        let integer = builder.integer_index(end)?;
        let thir::ExprKind::Constant(Value::Integer(value)) = &builder.body.exprs[end].kind else {
            *before = None;
            continue;
        };
        // A negative end, or one past `u64`, is outside every array.
        let known =
            bigint::to_u64(integer.bits, integer.signed, value).and_then(|at| at.checked_add(past));
        outside |= known.is_none_or(|known| known > limit);
        *before = known;
    }
    let tree = &builder.checker.module().ast;
    let range_span = tree.exprs.span(range);
    let written = tree.file.slice(range_span);
    let error = if outside {
        let viewed = builder.checker.type_text(viewed);
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
    builder
        .checker
        .error(diagnostics::INVALID_OPERATION, error, range_span);
    None
}

/// What to write instead of `.*` on the slice written `text`: a slice has
/// no bare element type for `.*` to give, so the help names what lends it
/// again.
pub(crate) fn lend_again_help(text: &str) -> String {
    format!("a slice is lent again as it is (`f({text})`), or as `&{text}[..]` / `&mut {text}[..]`")
}
