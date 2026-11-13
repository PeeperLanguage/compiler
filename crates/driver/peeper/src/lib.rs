//! Building a program: what `peeper build` and `peeper run` do between
//! loading and linking. It is here, not in the command, so the compile
//! benchmark times the work a build does.

use compile::language::Peeper;
use llvm::BuildMode;
use model::FunctionCopy;
use model::machine::Machine;
use model::mir;
use model::program::Program;
use model::semantic_types::Types;
use model::thir;

/// Checks and lowers `program` for `target` and writes each module's LLVM
/// text, in the program's order, built in `mode`. With `require_main`, the
/// entry file must declare a `main` (`compile::run`), and the module of the
/// entry file gets the C `main` that starts it. Each function lowered with no
/// error goes to `each_function`, with its THIR and its MIR, before it is
/// written. Gives the modules, which leave out each function with an error,
/// and what the compile found.
pub fn write_program<'p>(
    program: &'p Program<Peeper>,
    target: &'static Machine,
    mode: BuildMode,
    require_main: bool,
    mut each_function: impl FnMut(FunctionCopy, &thir::Body, &mir::Body, &Types),
) -> (Vec<llvm::Module<'p>>, compile::Results) {
    let files: Vec<_> = (program.modules.iter())
        .map(|module| module.ast.file.clone())
        .collect();
    let (mut modules, results) =
        llvm::write::write_modules(&program.root, &files, target, mode, |queue| {
            compile::run(
                program,
                target,
                compile::Results::default(),
                compile::Options {
                    require_main,
                    ..compile::Options::default()
                },
                |output| {
                    let compile::Output::Function(checked) = output else {
                        return; // constants and names are for editors
                    };
                    let compile::CheckedFunction {
                        function,
                        module,
                        thir,
                        mir,
                        types,
                        debug_types,
                        ..
                    } = checked;
                    let Some(body) = mir else {
                        return; // it has errors
                    };
                    each_function(function, thir, &body, types);
                    queue.add(
                        module,
                        function,
                        body,
                        |copy| model::program::copy_symbol(&program.modules, types, copy),
                        || {
                            let (name, _) = model::program::declared_name(
                                &program.modules,
                                function.definition,
                            );
                            (
                                model::program::copy_name(&program.modules, types, function),
                                name.span,
                            )
                        },
                        debug_types,
                    );
                },
            )
        });
    // `run` finds the entry point only when asked for it.
    if let (Some(entry), Some((main, exit_code))) = (program.entry, results.entry_point()) {
        let main = model::program::symbol(&program.modules, main);
        modules[entry.index()].add_entry_point(&main, exit_code);
    }
    (modules, results)
}
