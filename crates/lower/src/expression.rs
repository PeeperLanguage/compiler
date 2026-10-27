//! Lowering expressions: each value kind, array literals, places, and the
//! short-circuit `and` and `or`.

use arena::Id;
use arena::List;
use model::constant::Constant;
use model::constant::Value;
use model::mir;
use model::operators::BinaryOp;
use model::semantic_types::Type;
use model::semantic_types::TypeId;
use model::thir;
use source::Span;

use crate::Lowering;
use crate::ranges::RangeWalk;
use crate::slices::slice_elements;

impl Lowering<'_> {
    /// The instruction that makes the array literal of `parts`, of the
    /// array type `ty`: an `Aggregate` when each part is one element, as a
    /// struct literal; else `ArrayParts`.
    pub(super) fn lower_array_literal(
        &mut self,
        parts: List<thir::Expr>,
        ty: TypeId,
    ) -> Option<mir::InstructionKind> {
        let Type::Array { element, .. } = self.types[ty] else {
            unreachable!("an array literal makes an array")
        };
        let mut spread = false;
        let mut operands = Vec::with_capacity(parts.len());
        for part in self.thir.children.lists.items(parts) {
            spread |= !self.types.same(self.thir.exprs[part].ty, element);
            operands.push(self.lower_expr(part)?);
        }
        Some(if spread {
            mir::InstructionKind::ArrayParts(operands.into())
        } else {
            mir::InstructionKind::Aggregate(operands.into())
        })
    }

    /// The value of `expression`, computed in the block being filled.
    pub(super) fn lower_expr(&mut self, expression: Id<thir::Expr>) -> Option<mir::Operand> {
        let span = self.thir.exprs.span(expression);
        let thir::Expr { ty, ref kind } = self.thir.exprs[expression];
        let kind = match *kind {
            _ if ty == TypeId::INVALID => return None,
            thir::ExprKind::Invalid => return None,
            thir::ExprKind::Constant(ref value) => {
                let value = value.clone();
                return Some(self.add_constant(Constant { ty, value }));
            }
            // An array's length is its type's: the array is computed for the
            // call in it and never read, so no element moves out and no large
            // array is copied. A slice's is in it: the slice is read, never
            // moved, so a `[&mut]` stays usable.
            thir::ExprKind::Length(value) => {
                let value_type = self.thir.exprs[value].ty;
                if !matches!(self.types[value_type], Type::Slice { .. }) {
                    _ = self.place(value)?;
                    let array = self.types[value_type].pointee().unwrap_or(value_type);
                    let Type::Array { length, .. } = self.types[array] else {
                        unreachable!("`.len` is the length of an array or slice")
                    };
                    return Some(self.integer_constant(ty, length));
                }
                let slice = match self.thir.exprs[value].kind {
                    thir::ExprKind::Variable(variable) => {
                        let place = mir::Place::whole(Id::from_index(variable.index()));
                        self.emit(mir::InstructionKind::Load(place), value_type, span)
                    }
                    _ => self.lower_expr(value)?,
                };
                return Some(self.slice_length(slice, span));
            }
            thir::ExprKind::Binary {
                op: op @ (BinaryOp::LogicalAnd | BinaryOp::LogicalOr),
                left,
                right,
            } => return self.lower_short_circuit(op, left, right, span),
            thir::ExprKind::Binary { op, left, right } => {
                let left = self.lower_expr(left)?;
                let right = self.lower_expr(right)?;
                mir::InstructionKind::Binary { op, left, right }
            }
            thir::ExprKind::Unary { op, value } => mir::InstructionKind::Unary {
                op,
                value: self.lower_expr(value)?,
            },
            thir::ExprKind::Convert(value) => {
                mir::InstructionKind::Convert(self.lower_expr(value)?)
            }
            thir::ExprKind::Call {
                function,
                arguments,
            } => {
                let arguments = self
                    .thir
                    .children
                    .lists
                    .items(arguments)
                    .map(|argument| self.lower_expr(argument))
                    .collect::<Option<_>>()?;
                mir::InstructionKind::Call {
                    function,
                    arguments,
                }
            }
            thir::ExprKind::Print { newline, values } => mir::InstructionKind::Print {
                newline,
                values: self
                    .thir
                    .children
                    .lists
                    .items(values)
                    .map(|value| self.lower_expr(value))
                    .collect::<Option<_>>()?,
            },
            thir::ExprKind::StructLiteral(fields) => {
                // Computed in the order written, given in declaration order.
                let mut values = vec![None; fields.len()];
                for field in self.thir.children.lists.items(fields) {
                    let thir::FieldValue { index, value } = self.thir.children.field_values[field];
                    values[index as usize] = Some(self.lower_expr(value)?);
                }
                let values = values
                    .into_iter()
                    .map(|value| value.expect("check sets each field"));
                mir::InstructionKind::Aggregate(values.collect())
            }
            thir::ExprKind::ArrayLiteral(parts) => self.lower_array_literal(parts, ty)?,
            thir::ExprKind::Range { .. } => {
                let RangeWalk {
                    start,
                    left,
                    step,
                    delta,
                    inclusive,
                } = self.walk_range(expression)?;
                // In the order of `range_fields`.
                mir::InstructionKind::Aggregate(Box::new([start, left, step, delta, inclusive]))
            }
            thir::ExprKind::Variable(_)
            | thir::ExprKind::Field { .. }
            | thir::ExprKind::Dereference(_)
            | thir::ExprKind::Index { .. } => {
                // A variable, the most read place, skips the walk, which
                // cost a compile 10 M instructions (rust-style.md).
                let place = match *kind {
                    thir::ExprKind::Variable(variable) => {
                        Ok(mir::Place::whole(Id::from_index(variable.index())))
                    }
                    _ => self.place(expression)?,
                };
                match place {
                    Ok(place) if self.types.is_copy(ty) => mir::InstructionKind::Load(place),
                    Ok(place) => mir::InstructionKind::Move(place),
                    Err(field) => field,
                }
            }
            thir::ExprKind::Borrow(borrowed) => {
                let Some(mutable) = self.types[ty].borrows() else {
                    unreachable!("a borrow's type is a reference or slice")
                };
                let Ok(mut place) = self.place(borrowed)? else {
                    unreachable!("the checker borrows only a place in a variable")
                };
                // Borrowing a slice lends its elements again.
                if let Type::Slice { .. } = self.types[self.thir.exprs[borrowed].ty] {
                    place = slice_elements(place);
                }
                mir::InstructionKind::Borrow { place, mutable }
            }
            thir::ExprKind::Slice { .. } => return self.lower_slice(expression),
            thir::ExprKind::Address(place) => {
                let Ok(place) = self.place(place)? else {
                    unreachable!("the checker takes only the address of a place in a variable")
                };
                mir::InstructionKind::Address(place)
            }
        };
        Some(self.emit(kind, ty, span))
    }

    /// The place `expression` names: a variable, then each field,
    /// dereference and element from it (`s.start.x` is one place, so one
    /// load or store), each element's index computed here, in the order
    /// written. For fields of a value not in a variable, the `Field`
    /// instruction reading them from that value, computed here.
    pub(super) fn place(
        &mut self,
        expression: Id<thir::Expr>,
    ) -> Option<Result<mir::Place, mir::InstructionKind>> {
        // From the last step: each one, or the index expression of an element.
        let mut steps = Vec::new();
        let mut outer = expression;
        let variable = loop {
            match self.thir.exprs[outer].kind {
                thir::ExprKind::Field { value, index } => {
                    steps.push(Ok(mir::Projection::Field(index)));
                    outer = value;
                }
                thir::ExprKind::Dereference(reference) => {
                    steps.push(Ok(mir::Projection::Dereference));
                    outer = reference;
                }
                thir::ExprKind::Index { array, index } => {
                    steps.push(Err(index));
                    // A slice's elements are behind it.
                    if let Type::Slice { .. } = self.types[self.thir.exprs[array].ty] {
                        steps.push(Ok(mir::Projection::Dereference));
                    }
                    outer = array;
                }
                thir::ExprKind::Variable(variable) => break variable,
                _ => {
                    let fields = steps.iter().rev().map(|&step| match step {
                        Ok(mir::Projection::Field(index)) => index,
                        _ => unreachable!("the checker dereferences and indexes only a place"),
                    });
                    let fields = fields.collect();
                    let value = self.lower_expr(outer)?;
                    return Some(Err(mir::InstructionKind::Field { value, fields }));
                }
            }
        };
        let projections = steps.into_iter().rev().map(|step| match step {
            Ok(step) => Some(step),
            Err(index) => Some(mir::Projection::Index(self.lower_expr(index)?)),
        });
        Some(Ok(mir::Place {
            variable: Id::from_index(variable.index()),
            projections: projections.collect::<Option<_>>()?,
        }))
    }

    /// `left && right` or `left || right`: `right` runs only when `left` does
    /// not decide the result, and a `phi` picks the result where both meet.
    pub(super) fn lower_short_circuit(
        &mut self,
        op: BinaryOp,
        left: Id<thir::Expr>,
        right: Id<thir::Expr>,
        span: Span,
    ) -> Option<mir::Operand> {
        let left_span = self.thir.exprs.span(left);
        let left = self.lower_expr(left)?;
        let left_end = self.block;
        let right_block = self.new_block(self.thir.exprs.span(right));
        let join = self.new_block(span);
        let (then, otherwise) = match op {
            BinaryOp::LogicalAnd => (right_block, join),
            _ => (join, right_block),
        };
        self.finish(mir::Terminator::Branch {
            condition: left,
            span: left_span,
            then,
            otherwise,
        });
        self.block = right_block;
        let right = self.lower_expr(right)?;
        let right_end = self.block;
        self.finish(mir::Terminator::Goto(join));
        self.block = join;
        // When `left` decides, `&&` is false and `||` is true.
        let decided = self.add_constant(Constant {
            ty: TypeId::BOOL,
            value: Value::Bool(op == BinaryOp::LogicalOr),
        });
        let phi = mir::InstructionKind::Phi([(left_end, decided), (right_end, right)].into());
        Some(self.emit(phi, TypeId::BOOL, span))
    }
}
