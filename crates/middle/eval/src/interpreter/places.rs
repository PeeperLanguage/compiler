//! Where a `Place` is: walking its steps to an address, with the checks the
//! compiled program makes, and borrowing what is found there.

use arena::Id;
use model::constant::Value;
use model::layout::struct_layout;
use model::mir;
use model::mir::Operand;
use model::mir::Projection;
use model::semantic_types::Type;
use model::semantic_types::TypeId;
use model::semantic_types::range_fields;
use source::Span;

use super::Failure;
use super::Interpreter;
use super::Located;
use super::Register;
use crate::messages;

impl<'b> Interpreter<'b, '_> {
    /// Where field `index` of the struct or range type `ty` is, from the
    /// value's start, and its type, worked out once for each type.
    pub(super) fn field_place(&mut self, ty: TypeId, index: usize) -> (u64, TypeId) {
        let (types, target) = (self.types, self.target);
        let fields = self.fields.entry(ty).or_insert_with(|| {
            let fields = match types[ty] {
                Type::Struct { ref fields, .. } => fields.to_vec(),
                Type::Range { element, step } => {
                    range_fields(element, step).map(|(_, ty)| ty).to_vec()
                }
                _ => unreachable!("only a struct and a range have fields"),
            };
            let (offsets, ..) = struct_layout(types, &fields, target);
            offsets.into_iter().zip(fields).collect()
        });
        fields[index]
    }

    /// The index `index` means as an element's position: `None` for a value
    /// below zero or past every length.
    fn position(&mut self, index: &Operand) -> Option<u64> {
        let ty = self.frame().body.operand_type(*index);
        let Type::Integer(integer) = self.types[ty] else {
            unreachable!("check takes only an integer index")
        };
        let Register::Scalar(Value::Integer(limbs)) = self.operand(index) else {
            unreachable!("an integer's register is a scalar")
        };
        let limbs = limbs.as_slice();
        let top = (integer.bits - 1) as usize;
        let negative = integer.signed && limbs[top / 64] >> (top % 64) & 1 == 1;
        (!negative && limbs[1..].iter().all(|&limb| limb == 0)).then_some(limbs[0])
    }

    /// Walks `place`, as the backend's `write_place_address` does, with the
    /// same checks of each index. An address may be any number a raw pointer
    /// held, so sums wrap: the access that follows is what refuses it.
    pub(super) fn locate(&mut self, place: &mir::Place, span: Span) -> Result<Located, Failure> {
        let frame = self.frame();
        let mut ty = frame.body.variables[place.variable].ty;
        let mut address = frame.start + frame.slots.variables[place.variable.index()];
        let mut length = None;
        for &step in &place.projections {
            match (step, &self.types[ty]) {
                (Projection::Field(index), Type::Struct { .. }) => {
                    let (offset, field) = self.field_place(ty, index as usize);
                    address = address.wrapping_add(offset);
                    ty = field;
                }
                (Projection::Dereference, Type::Slice { .. }) => {
                    let second = address.wrapping_add(self.pointer_bytes());
                    length = Some(self.read_address(second, span)?);
                    address = self.read_address(address, span)?;
                }
                (Projection::Dereference, pointer) => {
                    let target = pointer
                        .pointee()
                        .expect("a dereference has a typed pointer");
                    address = self.read_address(address, span)?;
                    ty = target;
                }
                (
                    Projection::Index(ref index),
                    &Type::Array {
                        element,
                        length: bound,
                    },
                ) => {
                    let inside = self.position(index).filter(|&at| at < bound);
                    let Some(at) = inside else {
                        return Err(self.fail(messages::array_index_message(bound), span));
                    };
                    address = address.wrapping_add(at * self.size_of(element));
                    ty = element;
                }
                (Projection::Index(ref index), &Type::Slice { element, .. }) => {
                    let bound = length.expect("a slice's elements follow its dereference");
                    let inside = self.position(index).filter(|&at| at < bound);
                    let Some(at) = inside else {
                        return Err(self.fail(messages::SLICE_INDEX_MESSAGE, span));
                    };
                    address = address.wrapping_add(at * self.size_of(element));
                    ty = element;
                }
                (
                    Projection::Subslice(subslice),
                    &(Type::Array { element, .. } | Type::Slice { element, .. }),
                ) => {
                    let start = self.frame().body.subslices[subslice.index()].start;
                    let at = self.position(&start).expect("lowering checked the start");
                    address = address.wrapping_add(at * self.size_of(element));
                    ty = element;
                }
                _ => unreachable!("a field is taken of a struct, an element of an array or slice"),
            }
        }
        Ok(Located {
            address,
            ty,
            length,
        })
    }

    /// The value of the reference, slice or pointer to `place` that the
    /// instruction `id` gives.
    pub(super) fn borrow(
        &mut self,
        id: Id<mir::Instruction>,
        place: &mir::Place,
    ) -> Result<Register, Failure> {
        let body = self.frame().body;
        let span = body.instructions.span(id);
        let located = self.locate(place, span)?;
        if !matches!(self.types[body.instructions[id].ty], Type::Slice { .. }) {
            return Ok(Register::Address(located.address));
        }
        let length = match (place.projections.last(), &self.types[located.ty]) {
            (Some(&Projection::Subslice(subslice)), _) => {
                let count = body.subslices[subslice.index()].count;
                self.position(&count).expect("lowering checked the count")
            }
            (_, &Type::Array { length, .. }) => length,
            (_, Type::Slice { .. }) => located.length.expect("a slice's elements have a length"),
            _ => unreachable!("a slice is taken of an array or a slice's elements"),
        };
        Ok(Register::Slice {
            address: located.address,
            length,
        })
    }
}
