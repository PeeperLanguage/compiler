//! Lowering slices: the length of what a slice is taken of, the checks on
//! its bounds, and the subslice itself.

use arena::Id;
use model::constant::Value;
use model::mir;
use model::operators::BinaryOp;
use model::semantic_types::Type;
use model::semantic_types::TypeId;
use model::thir;
use source::Span;

use crate::Lowering;

/// The length of what a slice is taken of: an array's, from its type, or
/// a slice's, read when the program runs.
#[derive(Clone, Copy)]
pub(crate) enum Length {
    Known(u64),
    Read(mir::Operand),
}

impl Lowering<'_> {
    /// The slice `&array[start..end]` (`thir::ExprKind::Slice`) at
    /// `expression`, of an array or a slice: the borrow of the elements from
    /// the start, after stopping the program when an end is outside them or
    /// the ends are out of order. The checker rejected such ends known at
    /// compile time for an array, so they get no check; a slice's length
    /// is known only when the program runs, so each of its ends but a start
    /// of 0 is checked.
    // Takes only the id and gives the operand: an arm of `lower_expr` that
    // passed the parts and took the instruction back cost a compile 9 M
    // instructions, with no slice in it.
    /// The length of `slice`, its second field.
    pub(super) fn slice_length(&mut self, slice: mir::Operand, span: Span) -> mir::Operand {
        let fields = Box::new([1]);
        let length = mir::InstructionKind::Field {
            value: slice,
            fields,
        };
        self.emit(length, self.usize, span)
    }

    pub(super) fn lower_slice(&mut self, expression: Id<thir::Expr>) -> Option<mir::Operand> {
        let span = self.thir.exprs.span(expression);
        let thir::Expr {
            ty,
            kind:
                thir::ExprKind::Slice {
                    array,
                    start,
                    end,
                    inclusive,
                },
        } = self.thir.exprs[expression]
        else {
            unreachable!("the caller found a slice")
        };
        let Some(mutable) = self.types[ty].borrows() else {
            unreachable!("a slice's type is a slice")
        };
        let Ok(mut place) = self.place(array)? else {
            unreachable!("the checker slices only a place")
        };
        let viewed = self.thir.exprs[array].ty;
        let length = match self.types[viewed] {
            Type::Array { length, .. } => Length::Known(length),
            // Read once, which leaves a `[&mut]` usable; its elements are
            // behind it.
            Type::Slice { .. } => {
                let slice = mir::InstructionKind::Load(place.clone());
                let slice = self.emit(slice, viewed, span);
                place = slice_elements(place);
                Length::Read(self.slice_length(slice, span))
            }
            _ => unreachable!("the checker slices only an array or a slice"),
        };
        let known_length = match length {
            Length::Known(length) => Some(length),
            Length::Read(_) => None,
        };
        // How many elements come before each end when known at compile
        // time: a missing start is 0, a missing end the length.
        let before = |end: Id<thir::Expr>| match self.thir.exprs[end].kind {
            thir::ExprKind::Constant(Value::Integer(ref value)) => {
                let Type::Integer(integer) = self.types[self.thir.exprs[end].ty] else {
                    unreachable!("a slice's end is an integer")
                };
                let before = bigint::to_u64(integer.bits, integer.signed, value);
                Some(before.expect("the checker took only ends that fit a `usize`"))
            }
            _ => None,
        };
        let known_start = start.map_or(Some(0), before);
        let known_end = end.map_or(known_length, |end| {
            before(end).map(|end| end + u64::from(inclusive))
        });
        // A written end is checked unless it is known inside the array; no
        // start of 0 is outside.
        let checked =
            |known: Option<u64>| known.is_none() || known_length.is_none() && known != Some(0);
        let start_operand = match start {
            Some(start) if checked(known_start) => {
                self.lower_slice_end(start, BinaryOp::GreaterThan, length)?
            }
            _ => self.integer_constant(
                self.usize,
                known_start.expect("an unchecked start is known"),
            ),
        };
        let end_operand = match (end, known_end) {
            (Some(end), known) if checked(known) => {
                // A `..=` end is below the length, so one past it fits.
                let op = if inclusive {
                    BinaryOp::GreaterOrEqual
                } else {
                    BinaryOp::GreaterThan
                };
                let end = self.lower_slice_end(end, op, length)?;
                if inclusive {
                    let one = self.integer_constant(self.usize, 1);
                    self.binary(BinaryOp::Add, end, one, self.usize, span)
                } else {
                    end
                }
            }
            (_, Some(known)) => self.integer_constant(self.usize, known),
            // A slice's missing end is its length.
            (_, None) => match length {
                Length::Read(length) => length,
                Length::Known(_) => unreachable!("an array's missing end is known"),
            },
        };
        // Each end is inside the elements, so a start of 0 or an end at the
        // length is in order with any other.
        let at_length = end.is_none() || known_end.is_some() && known_end == known_length;
        let in_order =
            known_start == Some(0) || at_length || known_start.is_some() && known_end.is_some();
        if !in_order {
            let failed = self.binary(
                BinaryOp::GreaterThan,
                start_operand,
                end_operand,
                TypeId::BOOL,
                span,
            );
            let message = "slice starts after it ends";
            self.check(failed, message, span);
        }
        let count = match (known_start, known_end) {
            (Some(start), Some(end)) => self.integer_constant(self.usize, end - start),
            (Some(0), None) => end_operand,
            _ => self.binary(
                BinaryOp::Subtract,
                end_operand,
                start_operand,
                self.usize,
                span,
            ),
        };
        let subslice = Id::from_index(self.mir.subslices.len());
        self.mir.subslices.push(mir::Subslice {
            start: start_operand,
            count,
        });
        let mut projections = place.projections.into_vec();
        projections.push(mir::Projection::Subslice(subslice));
        place.projections = projections.into();
        let borrow = mir::InstructionKind::Borrow { place, mutable };
        Some(self.emit(borrow, ty, span))
    }

    /// The end `end` of a slice of `length` elements, an integer, as a
    /// `usize`, after stopping the program when it is negative or `op` the
    /// length. One no wider than a `usize` is converted first, a negative
    /// one, extended, above any length; a wider one is compared in its own
    /// type before it is cut to one.
    pub(super) fn lower_slice_end(
        &mut self,
        end: Id<thir::Expr>,
        op: BinaryOp,
        length: Length,
    ) -> Option<mir::Operand> {
        let span = self.thir.exprs.span(end);
        let ty = self.types.real(self.thir.exprs[end].ty);
        let value = self.lower_expr(end)?;
        let (&Type::Integer(integer), &Type::Integer(size)) =
            (&self.types[ty], &self.types[self.usize])
        else {
            unreachable!("a slice's end and `usize` are integers")
        };
        let wide = integer.bits > size.bits;
        let message = "slice out of bounds";
        if wide && integer.signed {
            let zero = self.integer_constant(ty, 0);
            let failed = self.binary(BinaryOp::LessThan, value, zero, TypeId::BOOL, span);
            self.check(failed, message, span);
        }
        let compared = if wide || ty == self.usize {
            value
        } else {
            self.emit(mir::InstructionKind::Convert(value), self.usize, span)
        };
        let compared_type = if wide { ty } else { self.usize };
        let limit = match length {
            Length::Known(length) => self.integer_constant(compared_type, length),
            Length::Read(length) if wide => {
                self.emit(mir::InstructionKind::Convert(length), ty, span)
            }
            Length::Read(length) => length,
        };
        let failed = self.binary(op, compared, limit, TypeId::BOOL, span);
        self.check(failed, message, span);
        Some(if wide {
            self.emit(mir::InstructionKind::Convert(value), self.usize, span)
        } else {
            compared
        })
    }
}

/// The elements of the slice at `place`, which are behind it.
pub(crate) fn slice_elements(mut place: mir::Place) -> mir::Place {
    let mut projections = place.projections.into_vec();
    projections.push(mir::Projection::Dereference);
    place.projections = projections.into();
    place
}
