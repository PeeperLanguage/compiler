//! Writing a program's modules on worker threads while the compile that
//! gives their functions goes on: a module's functions are kept until the
//! next module's start, then one worker writes them while the compile checks
//! the next module.

use std::num::NonZero;
use std::sync::Mutex;
use std::sync::mpsc;

use model::FunctionCopy;
use model::ModuleId;
use model::machine::Machine;
use model::mir;
use model::semantic_types::Types;

use crate::BuildMode;
use crate::Module;

/// One module's functions, each with its MIR, and the types they name.
struct Batch {
    module: Option<ModuleId>, // `None` before its first function
    functions: Vec<(FunctionCopy, mir::Body)>,
    types: Types,
}

/// Where a compile puts each function it checked, to be written in its
/// module's LLVM text.
pub struct FunctionQueue {
    batch: Batch,    // the module whose functions are coming
    sent: Vec<bool>, // by module: whether its functions went to a worker
    batches: mpsc::Sender<Batch>,
}

impl FunctionQueue {
    /// Adds the function `copy`, whose MIR is `body`, to the LLVM module of
    /// `module`, the module whose compile built it: its own, or the first
    /// to call a copy of another module's function. `types` is the
    /// program's types so far. A program's functions come module by module,
    /// so a function of another module sends the one before to a worker.
    pub fn add(&mut self, module: ModuleId, copy: FunctionCopy, body: mir::Body, types: &Types) {
        if self.batch.module != Some(module) {
            self.send();
            assert!(
                !self.sent[module.index()],
                "a module's functions come together"
            );
            self.batch.module = Some(module);
        }
        self.batch.types.extend_from(types);
        self.batch.functions.push((copy, body));
    }

    /// Sends the functions kept so far, with a copy of their types, to a
    /// worker.
    fn send(&mut self) {
        let Some(module) = self
            .batch
            .module
            .filter(|_| !self.batch.functions.is_empty())
        else {
            return;
        };
        self.sent[module.index()] = true;
        let batch = Batch {
            module: Some(module),
            functions: std::mem::take(&mut self.batch.functions),
            types: self.batch.types.clone(),
        };
        _ = self.batches.send(batch);
    }
}

/// The LLVM text of each module of `program`, built in `mode` for `target`,
/// in the program's order, and what `compile` gave.
/// `compile` runs on this thread and puts each function it checks in the
/// queue; up to one worker per core writes the modules meanwhile.
pub fn write_modules<'p, T>(
    program: &'p project::Program,
    target: &'static Machine,
    mode: BuildMode,
    compile: impl FnOnce(&mut FunctionQueue) -> T,
) -> (Vec<Module<'p>>, T) {
    let (sender, receiver) = mpsc::channel::<Batch>();
    let receiver = Mutex::new(receiver);
    let count = program.modules.len();
    let workers = std::thread::available_parallelism().map_or(1, NonZero::get);
    let (written, compiled) = std::thread::scope(|scope| {
        let handles: Vec<_> = (0..workers.min(count))
            .map(|_| {
                scope.spawn(|| {
                    let mut written = Vec::new();
                    loop {
                        // The lock is held only to take a batch, not to write it.
                        let next = receiver.lock().expect("no worker panics").recv();
                        let Ok(batch) = next else {
                            return written; // the channel is closed and empty
                        };
                        let module = batch.module.expect("a batch sent has a module");
                        let mut module = Module::new(program, module, target, mode);
                        for (copy, body) in &batch.functions {
                            module.add_function(*copy, body, &batch.types);
                        }
                        written.push(module);
                    }
                })
            })
            .collect(); // every worker starts before any is waited for
        let mut queue = FunctionQueue {
            batch: Batch {
                module: None,
                functions: Vec::new(),
                types: Types::new(target.pointer_bits),
            },
            sent: vec![false; count],
            batches: sender,
        };
        let compiled = compile(&mut queue);
        queue.send();
        drop(queue); // closes the channel, so each worker stops when it is empty
        let written: Vec<Module> = handles
            .into_iter()
            .flat_map(|handle| handle.join().expect("a writing worker does not panic"))
            .collect();
        (written, compiled)
    });
    // A module with no function to write is empty.
    let mut modules: Vec<Module> = (0..count)
        .map(|index| Module::new(program, ModuleId::from_index(index), target, mode))
        .collect();
    for module in written {
        let index = module.module.index();
        modules[index] = module;
    }
    (modules, compiled)
}
