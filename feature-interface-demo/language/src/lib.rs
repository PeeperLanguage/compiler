//! The language: the one list of features. The core reaches a feature only
//! through kinds on nodes and types, so this list feeds just the keyword
//! and parse table, the type names and the code check, all at compile time.

use gateway::feature::Code;
use gateway::feature::Feature;
use gateway::feature::TypeName;
use gateway::syntax::RuleTable;

/// The core's control flow comes first, as a feature's rules: it is parsed
/// like one, though every phase after checking follows it.
pub const FEATURES: &[&Feature] = &[
    &gateway::control::FEATURE,
    &bools::FEATURE,
    &integers::FEATURE,
    &operators::FEATURE,
    &bindings::FEATURE,
    &references::FEATURE,
    &functions::FEATURE,
    &members::FEATURE,
    &structs::FEATURE,
];

/// Every feature's keywords and rules. Two features claiming one keyword,
/// or the same tokens in the same position, stop the build here.
pub static RULES: RuleTable = RuleTable::build(FEATURES);

/// No two features, nor a feature and the core, own one diagnostic code.
const _: () = {
    let mut lists: [&[Code]; FEATURES.len() + 1] = [gateway::check::CODES; FEATURES.len() + 1];
    let mut index = 0;
    while index < FEATURES.len() {
        lists[index + 1] = FEATURES[index].codes;
        index += 1;
    }
    let mut first = 0;
    while first < lists.len() {
        let mut second = first + 1;
        while second < lists.len() {
            let mut code = 0;
            while code < lists[first].len() {
                let mut other = 0;
                while other < lists[second].len() {
                    if lists[first][code].0 == lists[second][other].0 {
                        panic!("two features own the same diagnostic code");
                    }
                    other += 1;
                }
                code += 1;
            }
            second += 1;
        }
        first += 1;
    }
};

pub fn type_names() -> Vec<&'static TypeName> {
    FEATURES
        .iter()
        .flat_map(|feature| feature.type_names)
        .collect()
}
