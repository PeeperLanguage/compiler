//! The runtime functions are spelled in three places: the runtime library
//! (runtime/peeper_rt.rs), `core`'s `#[extern]` declarations, and `symbols`
//! for those the backend calls by itself. A name or a signature that differs
//! would only show when a program links or runs, so this reads all three.

use std::collections::BTreeMap;
use std::path::Path;

use runtime_abi::symbols;
use runtime_abi::symbols::Carrier;
use runtime_abi::symbols::Signature;

/// A function's parameters and result, as C passes them.
type Carriers = (Vec<Carrier>, Option<Carrier>);

const REPOSITORY: &str = concat!(env!("CARGO_MANIFEST_DIR"), "/../../..");

/// How C passes the runtime's Rust type `ty`.
fn rust_carrier(ty: &str) -> Carrier {
    match ty.trim() {
        "i32" => Carrier::I32,
        "usize" => Carrier::Usize,
        "f64" => Carrier::F64,
        ty if ty.starts_with('*') => Carrier::Pointer,
        ty => panic!("the runtime passes `{ty}`, which this test does not know"),
    }
}

/// How C passes `core`'s Peeper type `ty`.
fn peeper_carrier(ty: &str) -> Carrier {
    match ty.trim() {
        "i32" => Carrier::I32,
        "usize" => Carrier::Usize,
        "f64" => Carrier::F64,
        "rawptr" | "&cstr" | "&mut cstr" => Carrier::Pointer,
        ty if ty.starts_with('*') => Carrier::Pointer,
        ty => panic!("`core` passes `{ty}`, which this test does not know"),
    }
}

/// The parameter types of `name: type, ...`, or of bare `type, ...`.
fn parameter_types(list: &str) -> impl Iterator<Item = &str> {
    (list
        .split(',')
        .map(str::trim)
        .filter(|parameter| !parameter.is_empty()))
    .map(|parameter| parameter.rsplit_once(':').map_or(parameter, |(_, ty)| ty))
}

/// Each `peeper_rt_*` function the runtime library defines, with its
/// signature.
fn runtime_functions() -> BTreeMap<String, Carriers> {
    let text = std::fs::read_to_string(format!("{REPOSITORY}/runtime/peeper_rt.rs")).unwrap();
    let mut functions = BTreeMap::new();
    for (at, _) in text.match_indices("extern \"C\" fn peeper_rt_") {
        let rest = &text[at + "extern \"C\" fn ".len()..];
        let (name, rest) = rest.split_once('(').unwrap();
        let (parameters, rest) = rest.split_once(')').unwrap();
        let result = rest.split_once('{').unwrap().0.trim();
        let result = result.strip_prefix("->").map(str::trim);
        let result = result.filter(|&ty| ty != "!").map(rust_carrier);
        let parameters = parameter_types(parameters).map(rust_carrier).collect();
        functions.insert(name.to_string(), (parameters, result));
    }
    functions
}

/// Each `#[extern("peeper_rt_*")]` function of the bundled libraries, with
/// its signature.
fn core_functions() -> BTreeMap<String, Carriers> {
    let mut functions = BTreeMap::new();
    let mut folders = vec![Path::new(REPOSITORY).join("_builtin_library")];
    while let Some(folder) = folders.pop() {
        for entry in std::fs::read_dir(folder).unwrap() {
            let path = entry.unwrap().path();
            if path.is_dir() {
                folders.push(path);
                continue;
            }
            if path.extension().is_none_or(|extension| extension != "peep") {
                continue;
            }
            let text = std::fs::read_to_string(&path).unwrap();
            for (at, _) in text.match_indices("#[extern(\"peeper_rt_") {
                let rest = &text[at + "#[extern(\"".len()..];
                let (symbol, rest) = rest.split_once('"').unwrap();
                let rest = rest.split_once('(').unwrap().1;
                let (parameters, rest) = rest.split_once(')').unwrap();
                let result = rest.split_once(';').unwrap().0.trim();
                let result = result.strip_prefix("->").map(peeper_carrier);
                let parameters = parameter_types(parameters).map(peeper_carrier).collect();
                functions.insert(symbol.to_string(), (parameters, result));
            }
        }
    }
    functions
}

#[test]
fn the_runtime_core_and_the_backend_agree() {
    let runtime = runtime_functions();
    let core = core_functions();
    for (symbol, signature) in &core {
        assert_eq!(
            runtime.get(symbol),
            Some(signature),
            "`core` declares `{symbol}` as the runtime does not define it"
        );
    }
    let called_by_backend: [&Signature; 8] = [
        &symbols::PANIC_WRITE,
        &symbols::PANIC_EXIT,
        &symbols::PRINT_TEXT,
        &symbols::PRINT_BYTES,
        &symbols::PRINT_INT,
        &symbols::PRINT_FLOAT,
        &symbols::PRINT_POINTER,
        &symbols::FLUSH,
    ];
    for signature in called_by_backend {
        let carriers = (signature.parameters.to_vec(), signature.result);
        assert_eq!(
            runtime.get(signature.symbol),
            Some(&carriers),
            "the backend calls `{}` as the runtime does not define it",
            signature.symbol
        );
    }
    let interpreted = [
        symbols::ALLOC,
        symbols::REALLOC,
        symbols::FREE,
        symbols::WRITE,
        symbols::READ,
        symbols::EXIT,
    ];
    for symbol in interpreted {
        assert!(
            core.contains_key(symbol),
            "no `core` function is `{symbol}`"
        );
    }
    for symbol in runtime.keys() {
        let called = called_by_backend
            .iter()
            .any(|signature| signature.symbol == symbol);
        assert!(
            called || core.contains_key(symbol),
            "nothing calls the runtime's `{symbol}`"
        );
    }
}

/// `core`'s streams are the descriptors the runtime and the interpreter use
/// (`runtime_abi::abi`); a program that interprets a different one writes
/// to the wrong stream.
#[test]
fn core_streams_are_the_runtimes() {
    let path = format!("{REPOSITORY}/_builtin_library/core/src/global.peep");
    let text = std::fs::read_to_string(&path).unwrap();
    let stream = |name: &str| -> i32 {
        let line = text
            .lines()
            .find(|line| line.starts_with(&format!("const {name}:")))
            .unwrap_or_else(|| panic!("{path} has no `const {name}`"));
        let (_, value) = line.split_once('=').expect("a constant has a value");
        value.trim().trim_end_matches(';').parse().unwrap()
    };
    assert_eq!(stream("Stdin"), runtime_abi::abi::STDIN);
    assert_eq!(stream("Stdout"), runtime_abi::abi::STDOUT);
    assert_eq!(stream("Stderr"), runtime_abi::abi::STDERR);
}
