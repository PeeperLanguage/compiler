//! Two-phase `&mut` borrows: a loan reserved until the call it is an
//! argument of.

use arena::Id;
use model::mir;
use model::mir::InstructionKind;

use crate::loans::Holder;
use crate::loans::Holders;
use crate::loans::places::names_exactly;

/// A `&mut` loan reserved until a call (two-phase).
#[derive(Clone, Copy)]
pub(super) struct Reservation {
    pub(super) call: Id<mir::Instruction>,
    /// The holder of the call's memory argument the reference was stored
    /// in, if it was passed in memory.
    pub(super) carrier: Option<Id<Holder>>,
}

impl<'b> Holders<'b> {
    /// The `&mut` loans whose reference is used once, by a call: as its
    /// argument, or stored in a memory argument's copy used only by it.
    pub(super) fn reserved_loans(&self) -> Vec<Option<Reservation>> {
        let holder_count = self.holder_count();
        let mut use_counts = vec![0_u32; holder_count];
        let mut users = vec![None; holder_count]; // the last user of each
        for id in self.body.instructions.ids() {
            if let InstructionKind::Call {
                signature,
                arguments,
                ..
            } = &self.body.instructions[id].kind
            {
                // Used again through the returned reference, so its loan
                // outlives the call.
                for holders in self.origin_holders(*signature, arguments) {
                    holders.for_each(|holder| use_counts[holder] += 1);
                }
            }
            self.uses(id, |holder| {
                use_counts[holder.index()] += 1;
                users[holder.index()] = Some(id);
            });
        }
        let used_once_by = |holder: usize| {
            let user = users[holder].filter(|_| use_counts[holder] == 1)?;
            Some((user, &self.body.instructions[user].kind))
        };
        let reservation = |borrow: Id<mir::Instruction>| {
            let holder = self.value_holder(borrow).index();
            match used_once_by(holder)? {
                (call, InstructionKind::Call { .. }) => Some(Reservation {
                    call,
                    carrier: None,
                }),
                (_, InstructionKind::Store { class, place, .. })
                    if names_exactly(place.as_place_ref()) =>
                {
                    let carrier = self
                        .place_holders(place.as_place_ref(), Some(class.bytes(self.pointer_bytes)));
                    if carrier.len() != 1 {
                        return None;
                    }
                    let (call, InstructionKind::Call { .. }) = used_once_by(carrier.start)? else {
                        return None;
                    };
                    Some(Reservation {
                        call,
                        carrier: Some(Id::from_index(carrier.start)),
                    })
                }
                _ => None,
            }
        };
        (self.borrows.iter())
            .map(|&borrow| match self.body.instructions[borrow].kind {
                InstructionKind::Borrow { mutable: true, .. } => reservation(borrow),
                _ => None,
            })
            .collect()
    }
}
