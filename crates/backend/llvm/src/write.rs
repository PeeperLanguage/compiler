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
use model::mir::DebugTypes;
use model::semantic_types::Types;
use rustc_hash::FxHashMap;

use crate::BuildMode;
use crate::Module;
use crate::body::Defined;
use crate::text::copy_symbol;

/// One module's functions, with the symbol of each function they define or
/// name and, in a debug build, the debug types they name.
struct Batch {
    module: ModuleId,
    functions: Vec<Defined>,
    /// Made here, not by a worker: a copy's symbol names its `const`
    /// arguments, which only the program's types tell.
    symbols: FxHashMap<FunctionCopy, String>,
    debug_types: DebugTypes,
}

/// Where a compile puts each function it checked, to be written in its
/// module's LLVM text.
pub struct FunctionQueue<'p> {
    program: &'p model::program::Program,
    mode: BuildMode,
    /// The module whose functions are coming, `None` before the first, and
    /// those so far, with the symbols they need (`Batch`).
    module: Option<ModuleId>,
    functions: Vec<Defined>,
    symbols: FxHashMap<FunctionCopy, String>,
    sent: Vec<bool>, // by module: whether its functions went to a worker
    batches: mpsc::Sender<Batch>,
    /// In a debug build, a copy of the program's debug types so far, sent
    /// with each batch: the compile's table is gone when the last one goes.
    debug_types: DebugTypes,
}

impl FunctionQueue<'_> {
    /// Adds the function `copy`, whose MIR is `body`, to the LLVM module of
    /// `module`, the module whose compile built it: its own, or the first
    /// to call a copy of another module's function. `types` and
    /// `debug_types` are the program's so far. A program's functions come
    /// module by module, so a function of another module sends the one
    /// before to a worker.
    pub fn add(
        &mut self,
        module: ModuleId,
        copy: FunctionCopy,
        body: mir::Body,
        types: &Types,
        debug_types: &DebugTypes,
    ) {
        if self.module != Some(module) {
            self.send();
            assert!(
                !self.sent[module.index()],
                "a module's functions come together"
            );
            self.module = Some(module);
        }
        let program = self.program;
        let symbols = &mut self.symbols;
        let mut name = |copy: FunctionCopy| {
            symbols
                .entry(copy)
                .or_insert_with(|| copy_symbol(program, types, copy));
        };
        name(copy);
        body.named_functions().for_each(name);
        let shown_name = match self.mode {
            BuildMode::Debug => {
                self.debug_types.copy_new(debug_types);
                let modules = &program.modules;
                let declared = model::program::declared_path(modules, copy.definition);
                let name_of = |definition| model::program::declared_text(modules, definition);
                types.copy_text(declared, copy.arguments, name_of)
            }
            BuildMode::Release => String::new(),
        };
        self.functions.push(Defined {
            copy,
            shown_name,
            body,
        });
    }

    /// Sends the functions kept so far, with what they name, to a worker.
    fn send(&mut self) {
        let Some(module) = self.module.filter(|_| !self.functions.is_empty()) else {
            return;
        };
        self.sent[module.index()] = true;
        // A release build adds none, so this is empty.
        let batch = Batch {
            module,
            functions: std::mem::take(&mut self.functions),
            symbols: std::mem::take(&mut self.symbols),
            debug_types: self.debug_types.clone(),
        };
        _ = self.batches.send(batch);
    }
}

/// The LLVM text of each module of `program`, built in `mode` for `target`,
/// in the program's order, and what `compile` gave.
/// `compile` runs on this thread and puts each function it checks in the
/// queue; up to one worker per core writes the modules meanwhile.
pub fn write_modules<'p, T>(
    program: &'p model::program::Program,
    target: &'static Machine,
    mode: BuildMode,
    compile: impl FnOnce(&mut FunctionQueue<'p>) -> T,
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
                        let mut module = Module::new(program, batch.module, target, mode);
                        for defined in &batch.functions {
                            module.add_body(defined, &batch.symbols, &batch.debug_types);
                        }
                        written.push(module);
                    }
                })
            })
            .collect(); // every worker starts before any is waited for
        let mut queue = FunctionQueue {
            program,
            mode,
            module: None,
            functions: Vec::new(),
            symbols: FxHashMap::default(),
            sent: vec![false; count],
            batches: sender,
            debug_types: DebugTypes::default(),
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
