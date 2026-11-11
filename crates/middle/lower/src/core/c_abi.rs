//! How C passes the values of a call, as clang lowers C's integers on each
//! machine (`CAbi`, measured with clang 23): lowering works it out from the
//! types, so the MIR carries it and no backend reads a type.

use model::core_mir::CPassing;
use model::core_mir::CSignature;
use model::core_mir::Extension;
use model::machine::CAbi;
use model::machine::Machine;
use model::semantic_types::Integer;
use model::semantic_types::Type;
use model::semantic_types::TypeId;
use model::semantic_types::Types;

/// How C on `target` passes a call's arguments, of the types `parameters`,
/// and its result of the type `result`. x86-64 System V passes the first
/// six integer words in registers and two pieces that no longer fit there
/// whole on the stack, so a parameter's way depends on those before it.
pub(super) fn c_signature(
    types: &Types,
    target: &Machine,
    result: TypeId,
    parameters: impl Iterator<Item = TypeId>,
) -> CSignature {
    let result =
        (types.real(result) != TypeId::VOID).then(|| c_passing(types, target, result, true));
    let mut registers = if let Some(CPassing::Memory { .. }) = result {
        5
    } else {
        6
    };
    let parameters = parameters.map(|ty| {
        let passing = c_passing(types, target, ty, false);
        if target.c_abi != CAbi::SystemV {
            return passing;
        }
        let needed = match (passing, &types[types.real(ty)]) {
            (CPassing::Pieces { count, .. }, _) => count,
            (CPassing::Register { .. }, Type::Integer(integer)) if integer.bits == 128 => 2,
            (CPassing::Register { .. }, Type::Float(_)) | (CPassing::Memory { .. }, _) => 0,
            _ => 1,
        };
        match passing {
            _ if needed <= registers => {
                registers -= needed;
                passing
            }
            CPassing::Pieces { .. } => CPassing::Memory {
                size: 16,
                align: 8,
                by_value: true,
            },
            // An `i128` goes on the stack by itself.
            _ => passing,
        }
    });
    CSignature {
        parameters: parameters.collect(),
        result,
    }
}

/// How C on `target` passes a value of the type `ty` as a parameter, or
/// else as a `result`, while registers are left (`c_signature`). Only
/// scalars pass to C (`resolve_signature_type`). An integer in memory takes
/// whole words: 4 bytes on i386, else 8.
fn c_passing(types: &Types, target: &Machine, ty: TypeId, result: bool) -> CPassing {
    let abi = target.c_abi;
    let direct = |extension| CPassing::Register { extension };
    let Integer { signed, bits } = match types[types.real(ty)] {
        Type::Bool if abi == CAbi::Aapcs64 => return direct(None),
        Type::Bool => return direct(Some(Extension::Zero)),
        Type::Integer(integer) => integer,
        _ => return direct(None),
    };
    let memory = |align: u32, by_value| CPassing::Memory {
        size: u64::from(bits.div_ceil(align * 8) * align),
        align: u64::from(align),
        by_value,
    };
    let extended = direct(Some(if signed {
        Extension::Sign
    } else {
        Extension::Zero
    }));
    match abi {
        CAbi::SystemV | CAbi::I386 | CAbi::Darwin64 if bits < 32 => extended,
        CAbi::SystemV | CAbi::I386 | CAbi::Windows64 if bits <= 64 => direct(None),
        CAbi::SystemV if bits < 128 => CPassing::Pieces { count: 2, bits: 64 },
        CAbi::SystemV if bits == 128 => direct(None),
        CAbi::SystemV => memory(8, true),
        CAbi::I386 => memory(4, false),
        // As `__int128`, not `_BitInt(128)` (owner, 2026-10-29).
        CAbi::Windows64 if bits == 128 && result => CPassing::Vector { count: 2, bits: 64 },
        CAbi::Windows64 if bits == 128 => CPassing::Memory {
            size: 16,
            align: 16,
            by_value: false,
        },
        CAbi::Windows64 => memory(8, false),
        CAbi::Aapcs64 | CAbi::Darwin64 if bits <= target.widest_c_integer() => direct(None),
        CAbi::Aapcs64 | CAbi::Darwin64 => {
            unreachable!("the checker rejects a C integer wider than the target's")
        }
    }
}
