#!/usr/bin/env python3
"""Counts, in each concept file of the checker, the decisions it makes about
another concept: matching its checked types (`Type::Struct`) or syntax nodes
(`ast::Type::Struct`), calling its own functions, or deciding for open types
outside generics (docs/compiler-layout.md: one file per concept, one
dispatch). THIR nodes are core ideas (rule 6) and the type table's
questions, such as `elements()`, are the base API (rule 4): neither counts,
nor do core kinds (integers, bool, void, invalid, functions, calls,
variables), which no feature owns. Each use in a code line counts once.
Files not in `FILES` are skipped: the engine (`lib.rs`, `builder.rs`,
`scope.rs`, `testing.rs`) and the dispatch files, which name every kind to
pick its owner (`expression.rs`, `statement.rs`, `declaration.rs`, `fold.rs`,
`expression/places.rs`).

    python3 scripts/cross-concepts.py [list.md]

prints the count per file; with a path, also writes every decision, by file
and function, there. It matches text, so a local named like a concept's
function counts too.
"""
import re
import sys
from collections import Counter, defaultdict
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent / "crates/check/src"

# What each concept owns: a pattern per kind or question.
OWNERS = {
    "generics": [r"Type::Parameter\b", r"Type::Undecided\b", r"TypeId::UNDECIDED\b",
                 r"ExprKind::Undecided\b", r"Conversion::Undecided\b", r"\bis_open\(",
                 r"\barguments_open\(", r"\bin_own_check\(", r"\bholds_undecided\(",
                 r"\bopen_struct\(", r"\bcopy_type\(",
                 r"\bown_arguments\("],
    "text": [r"Type::CString\b", r"Type::Str\b", r"TypeId::STR\b", r"TypeId::CSTR\b"],
    "aliases": [r"Type::Alias\b"],
    "structs": [r"Type::Struct\b", r"Type::Named\b", r"\bfield_index\(",
                r"\bfield_types_given\("],
    "arrays": [r"Type::Array\b", r"\btypes\.array\("],
    "slices": [r"Type::Slice\b"],
    "ranges": [r"Type::Range\b", r"\brange_step\(", r"\brange_fields\(", r"\btypes\.range\("],
    "references": [r"Type::Reference\b", r"\breborrow"],
    "interfaces": [r"Type::Interface\b", r"Type::InterfaceReference\b", r"\bis_interface\(",
                   r"\bimplementation\(", r"\binterface_method\("],
    "raw_pointers": [r"Type::RawPointer\b", r"\breached_through_pointer\("],
    "printing": [r"\bprintable\(", r"\bcheck_print\("],
    "constants": [r"\bknown_constant\(", r"\bcheck_constant_value\(", r"\bin_constant\b"],
}
# The syntax kinds each concept owns.
SYNTAX = {
    "generics": ["Type::Generic", "Type::Type"],
    "ranges": ["Type::Bracketed", "Expr::Range"],
    "references": ["Type::Reference", "Expr::Reference", "Expr::Dereference"],
    "slices": ["Type::Slice"],
    "arrays": ["Type::Array", "Expr::Index", "Expr::Spread", "Expr::ArrayLiteral"],
    "enums": ["Type::Enum", "Expr::CaseTest", "Expr::VariantLiteral", "Stmt::Match"],
    "interfaces": ["Type::Interface"],
    "optionals": ["Type::Optional", "Expr::OrElse", "Expr::None"],
    "raw_pointers": ["Type::Pointer", "Expr::RawAddress"],
    "structs": ["Type::Struct", "Expr::StructLiteral", "Expr::Selector"],
    "operators": ["Expr::Binary", "Expr::Unary"],
    "text": ["Expr::String", "Expr::Char"],
    "pipes": ["Expr::Pipe"],
    "printing": ["Expr::Print"],
    "loops": ["Stmt::Loop", "Stmt::ForIn"],
}
for owner, kinds in SYNTAX.items():
    OWNERS.setdefault(owner, []).extend(r"ast::" + kind + r"\b" for kind in kinds)

# Each concept file and the concepts it owns.
FILES = {
    "arrays.rs": {"arrays"},
    "chars.rs": {"text"},
    "conformance.rs": {"interfaces"},
    "enums.rs": {"enums"},
    "expression/borrows.rs": {"references"},
    "expression/literals.rs": {"literals"},
    "expression/names.rs": set(),
    "expression/operators.rs": {"operators"},
    "fold/conversions.rs": set(),
    "fold/operators.rs": {"operators"},
    "functions.rs": set(),
    "functions/bodies.rs": set(),
    "functions/builtin.rs": set(),
    "functions/calls.rs": set(),
    "generics.rs": {"generics"},
    "generics/constraints.rs": {"generics"},
    "generics/copies.rs": {"generics"},
    "interfaces.rs": {"interfaces"},
    "locals.rs": {"constants"},
    "optionals.rs": {"optionals"},
    "pipes.rs": {"pipes"},
    "printing.rs": {"printing"},
    "ranges.rs": {"ranges"},
    "raw_pointers.rs": {"raw_pointers"},
    "slices.rs": {"slices"},
    "statement/assignments.rs": set(),
    "statement/loops.rs": {"loops"},
    "structs.rs": {"structs"},
}

FN = re.compile(r"^\s*(?:pub(?:\([a-z]+\))?\s+)?fn\s+([a-z_0-9]+)")
compiled = [(owner, re.compile(p)) for owner, ps in OWNERS.items() for p in ps]

per_file = {}
for name, own in FILES.items():
    path = ROOT / name
    function = "(top)"
    found = []
    in_tests = False
    for number, line in enumerate(path.read_text().splitlines(), 1):
        if re.match(r"^\s*#\[cfg\(test\)\]", line):
            in_tests = True
        if in_tests:
            continue
        code = line.split("//")[0]
        match = FN.match(code)
        if match:
            function = match.group(1)
        for owner, pattern in compiled:
            if owner in own:
                continue
            for hit in pattern.finditer(code):
                found.append((number, function, hit.group(0).strip("(."), owner))
    per_file[name] = found

total = sum(len(found) for found in per_file.values())
print(f"total cross decisions: {total}\n")
print(f"{'file':28} {'count':>5}  by owner")
for name, found in sorted(per_file.items(), key=lambda item: -len(item[1])):
    if not found:
        continue
    owners = Counter(owner for *_, owner in found)
    text = ", ".join(f"{owner} {count}" for owner, count in owners.most_common())
    print(f"{name:28} {len(found):>5}  {text}")
print("\nclean:", ", ".join(name for name, found in per_file.items() if not found) or "none")

if len(sys.argv) > 1:
    with open(sys.argv[1], "w") as out:
        for name, found in sorted(per_file.items(), key=lambda item: -len(item[1])):
            if not found:
                continue
            out.write(f"\n## {name} ({len(found)})\n")
            by_function = defaultdict(list)
            for number, function, what, owner in found:
                by_function[function].append(f"{owner}: {what} (line {number})")
            for function, items in by_function.items():
                out.write(f"- `{function}`: " + "; ".join(items) + "\n")
