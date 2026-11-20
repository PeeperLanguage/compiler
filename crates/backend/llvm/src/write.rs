//! Writing a program's modules on worker threads while the compile that
//! gives their functions goes on: a module's functions are kept until the
//! next module's start, then one worker writes them while the compile checks
//! the next module.

use std::num::NonZero;
use std::path::Path;
use std::sync::Arc;
use std::sync::Mutex;
use std::sync::mpsc;

use model::DeclarationCopy;
use model::ModuleId;
use model::machine::Machine;
use model::mir;
use model::mir::DebugTypes;
use rustc_hash::FxHashMap;
use source::SourceFile;
use source::Span;

use crate::BuildMode;
use crate::Module;
use crate::body::Defined;

/// One module's functions, with the symbol of each function they define or
/// name and, in a debug build, the debug types they name.
struct Batch {
    module: ModuleId,
    functions: Vec<Defined>,
    /// Made on the compile's thread, not by a worker: a copy's symbol names
    /// its `const` arguments, which only the program's types tell.
    symbols: FxHashMap<DeclarationCopy, String>,
    debug_types: DebugTypes,
}

/// Where a compile puts each function it checked, to be written in its
/// module's LLVM text.
pub struct FunctionQueue {
    mode: BuildMode,
    /// The module whose functions are coming, `None` before the first, and
    /// those so far, with the symbols they need (`Batch`).
    module: Option<ModuleId>,
    functions: Vec<Defined>,
    symbols: FxHashMap<DeclarationCopy, String>,
    sent: Vec<bool>, // by module: whether its functions went to a worker
    batches: mpsc::Sender<Batch>,
    /// In a debug build, a copy of the program's debug types so far, sent
    /// with each batch: the compile's table is gone when the last one goes.
    debug_types: DebugTypes,
}

impl FunctionQueue {
    /// Adds the function `copy`, whose MIR is `body`, to the LLVM module of
    /// `module`, the module whose compile built it: its own, or the first
    /// to call a copy of another module's function. `symbol_of` names a
    /// copy for the linker; `debug_name` gives, for a debugger, the name
    /// this one is shown by and where its name is declared; `debug_types`
    /// are the program's so far. A program's functions come
    /// module by module, so a function of another module sends the one
    /// before to a worker.
    pub fn add(
        &mut self,
        module: ModuleId,
        copy: DeclarationCopy,
        body: mir::Body,
        symbol_of: impl Fn(DeclarationCopy) -> String,
        debug_name: impl FnOnce() -> (String, Span),
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
        let symbols = &mut self.symbols;
        let mut name = |copy: DeclarationCopy| {
            symbols.entry(copy).or_insert_with(|| symbol_of(copy));
        };
        name(copy);
        body.named_functions().for_each(name);
        let (shown_name, declared_at) = match self.mode {
            BuildMode::Debug => {
                self.debug_types.copy_new(debug_types);
                debug_name()
            }
            BuildMode::Release => (String::new(), Span::default()),
        };
        self.functions.push(Defined {
            copy,
            shown_name,
            declared_at,
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

/// The LLVM text of each module of the project in `root`, whose source
/// files are `files` in the program's order, built in `mode` for `target`,
/// in that order, and what `compile` gave.
/// `compile` runs on this thread and puts each function it checks in the
/// queue; up to one worker per core writes the modules meanwhile.
pub fn write_modules<'p, T>(
    root: &'p Path,
    files: &[Arc<SourceFile>],
    target: &'static Machine,
    mode: BuildMode,
    compile: impl FnOnce(&mut FunctionQueue) -> T,
) -> (Vec<Module<'p>>, T) {
    let (sender, receiver) = mpsc::channel::<Batch>();
    let receiver = Mutex::new(receiver);
    let count = files.len();
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
                        let file = files[batch.module.index()].clone();
                        let mut module = Module::new(root, file, batch.module, target, mode);
                        for defined in &batch.functions {
                            let file = &files[defined.copy.definition.module.index()];
                            module.add_body(defined, file, &batch.symbols, &batch.debug_types);
                        }
                        written.push(module);
                    }
                })
            })
            .collect(); // every worker starts before any is waited for
        let mut queue = FunctionQueue {
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
    let mut modules: Vec<Module> = (files.iter().enumerate())
        .map(|(index, file)| {
            Module::new(
                root,
                file.clone(),
                ModuleId::from_index(index),
                target,
                mode,
            )
        })
        .collect();
    for module in written {
        let index = module.module.index();
        modules[index] = module;
    }
    (modules, compiled)
}
