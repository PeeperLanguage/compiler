//! Runs MIR on a byte array. It depends on `model` only, so it knows a type
//! by its facts alone: a wrong size or offset in any type's facts gives a
//! wrong result, not a lucky one.

use model::facts::Carrier;
use model::facts::Facts;
use model::mir::InstructionKind;
use model::mir::Operand;
use model::mir::Operator;
use model::mir::Place;
use model::mir::Program;
use model::mir::Projection;
use model::mir::Terminator;

/// What the function `function`, which takes no arguments, returns.
pub fn run(program: &Program, function: u32) -> u64 {
    let mut machine = Machine {
        program,
        memory: Vec::new(),
    };
    machine.call(function, &[])
}

struct Machine<'a> {
    program: &'a Program,
    memory: Vec<u8>,
}

/// `value` read as a signed number of `bits`.
fn signed(value: u64, bits: u64) -> i64 {
    let unused = 64 - bits.min(64);
    ((value << unused) as i64) >> unused
}

fn truncate(value: u64, bits: u64) -> u64 {
    match bits {
        64.. => value,
        bits => value & ((1 << bits) - 1),
    }
}

impl Machine<'_> {
    fn read(&self, address: u64, bytes: u64) -> u64 {
        let at = address as usize;
        (0..bytes as usize).fold(0, |value, index| {
            value | u64::from(self.memory[at + index]) << (8 * index)
        })
    }

    fn write(&mut self, address: u64, value: u64, bytes: u64) {
        let at = address as usize;
        for index in 0..bytes as usize {
            self.memory[at + index] = (value >> (8 * index)) as u8;
        }
    }

    /// New storage for a value with `facts`, until the function returns.
    // shortcut: a loop's turns each take new storage for the values in
    // memory they read, kept to the function's end; fine for these tests,
    // a long loop over structs would want storage per instruction.
    fn allocate(&mut self, facts: Facts) -> u64 {
        let at = self
            .memory
            .len()
            .next_multiple_of(facts.align.max(1) as usize);
        self.memory.resize(at + facts.size as usize, 0);
        at as u64
    }

    /// The value at `at`: a scalar's bits, or the address of a new copy of
    /// a value in memory.
    fn get(&mut self, at: u64, facts: Facts) -> u64 {
        match facts.carrier {
            Carrier::Nothing => 0,
            Carrier::Scalar => self.read(at, facts.size),
            Carrier::Pair | Carrier::Memory => {
                let copy = self.allocate(facts);
                let from = at as usize;
                (self.memory).copy_within(from..from + facts.size as usize, copy as usize);
                copy
            }
        }
    }

    fn put(&mut self, at: u64, value: u64, facts: Facts) {
        match facts.carrier {
            Carrier::Nothing => {}
            Carrier::Scalar => self.write(at, value, facts.size),
            Carrier::Pair | Carrier::Memory => {
                let from = value as usize;
                (self.memory).copy_within(from..from + facts.size as usize, at as usize);
            }
        }
    }

    fn address(&self, place: &Place, variables: &[u64]) -> u64 {
        let mut at = variables[place.variable.0 as usize];
        for projection in &place.projections {
            at = match *projection {
                Projection::Field { offset, .. } => at + offset,
                Projection::Dereference => self.read(at, self.program.pointer_bytes),
            };
        }
        at
    }

    fn call(&mut self, id: u32, arguments: &[u64]) -> u64 {
        let program = self.program;
        let body = &program.functions[id as usize];
        let previous = self.memory.len();
        let variables: Vec<u64> = (body.variables.iter())
            .map(|variable| self.allocate(program.facts(variable.ty)))
            .collect();
        let mut values = vec![0u64; body.instructions.len()];
        let value = |values: &[u64], operand: Operand| match operand {
            Operand::Constant(value) => value,
            Operand::Argument(index) => arguments[index as usize],
            Operand::Value(id) => values[id.0 as usize],
            Operand::Function(function) => u64::from(function),
        };
        let mut block = 0;
        loop {
            for &id in &body.blocks[block].instructions {
                let instruction = &body.instructions[id.0 as usize];
                let facts = program.facts(instruction.ty);
                values[id.0 as usize] = match &instruction.kind {
                    InstructionKind::Load(place) | InstructionKind::Move(place) => {
                        let at = self.address(place, &variables);
                        self.get(at, facts)
                    }
                    InstructionKind::Store {
                        place,
                        value: stored,
                        ty,
                    } => {
                        let at = self.address(place, &variables);
                        self.put(at, value(&values, *stored), program.facts(*ty));
                        0
                    }
                    InstructionKind::Borrow { place, .. } => self.address(place, &variables),
                    InstructionKind::Binary {
                        operator,
                        left,
                        right,
                        operands,
                    } => {
                        let bits = program.facts(*operands).size * 8;
                        let left = signed(value(&values, *left), bits);
                        let right = signed(value(&values, *right), bits);
                        let result = match operator {
                            Operator::Add => left.wrapping_add(right) as u64,
                            Operator::Subtract => left.wrapping_sub(right) as u64,
                            Operator::Multiply => left.wrapping_mul(right) as u64,
                            Operator::LessThan => u64::from(left < right),
                        };
                        truncate(result, facts.size * 8)
                    }
                    InstructionKind::Call {
                        function,
                        arguments,
                    } => {
                        let arguments: Vec<u64> = (arguments.iter())
                            .map(|&argument| value(&values, argument))
                            .collect();
                        self.call(*function, &arguments)
                    }
                    InstructionKind::Aggregate(parts) => {
                        let at = self.allocate(facts);
                        for part in parts {
                            let part_facts = program.facts(part.ty);
                            self.put(at + part.offset, value(&values, part.value), part_facts);
                        }
                        at
                    }
                    InstructionKind::ScopeEnd(_) => 0,
                };
            }
            match &body.blocks[block].terminator {
                Terminator::Goto(next) => block = next.0 as usize,
                Terminator::Branch {
                    condition,
                    then,
                    otherwise,
                    ..
                } => {
                    block = match value(&values, *condition) {
                        0 => otherwise.0 as usize,
                        _ => then.0 as usize,
                    };
                }
                Terminator::Return { value: result, .. } => {
                    let result = result.map_or(0, |result| value(&values, result));
                    self.memory.truncate(previous);
                    return result;
                }
                Terminator::FallOff(_) => {
                    self.memory.truncate(previous);
                    return 0;
                }
            }
        }
    }
}
