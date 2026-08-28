# Type Support Audit TODO

Audit date: 2026-08-28
Audited revision: `7582038` (`main`, equal to `origin/main`)

Canonical language behavior remains in [`docs/language-spec.md`](docs/language-spec.md). This file tracks verified gaps and issue cleanup; it does not redefine language semantics.

## Current support

| Type family | Foundation status | Verified support | Known boundary |
| --- | --- | --- | --- |
| Primitive | Implemented | `bool`, `byte`, `char`, `cstr`, `rawptr`, `isize`/`usize`, `f32`/`f64`, and `iN`/`uN` widths from 1 through 2^23; literals, conversions, arithmetic, comparisons, bitwise operations, constants, HIR/MIR, and LLVM lowering | `print` rejects integers wider than 64 bits and `char`; arbitrary `fN` and compound bitwise assignments are not language surface |
| `str` | Foundation implemented | Owned immutable literals, content equality/inequality, moves and drops, allocator-aware carrier, `print`/`println`, `len`, borrowed `&str` ranges, `as_bytes`, `as_chars`, UTF-8 boundary traps, temporary-borrow checks | Runtime construction/concatenation and explicit C/FFI bridges remain open |
| Optional | Foundation implemented | `?T`, `none`, one-layer promotion, full-CFG narrowing, stable-place invalidation, nested optionals, ownership-safe payload use, explicit HIR/MIR variant operations, tagged LLVM layout and drop | Niche layouts remain open; optional-to-optional equality, fallback, unwrap, chaining, and optional patterns are intentionally absent |
| Enum | V1 baseline implemented | Named/generic/imported enums, payloadless and payload cases, canonical `with` construction, `is` narrowing, exhaustive statement `match`, payload binding/destructuring, ownership cleanup, constants, aliases, recursive indirection, tagged LLVM layout | Advanced patterns, match expressions, contextual construction, protocols, compact layout, and foreign ABI remain open |
| Struct | Core implemented | Named and anonymous structural types/literals, field access and mutation, methods, nominal/structural conversion rules, ownership/drop, generics, recursive indirection, HIR/MIR/LLVM | Open issue #100 is stale: its exact `struct {}` plus `.{}` example already builds and runs; dedicated edge-matrix coverage may still be useful |
| Interface | Runtime-carrier baseline implemented | Contracts, implicit satisfaction, shared/mutable/owned carriers, dispatch, consuming receivers, allocator-preserving owned conversion, generic named interfaces, returned-reference `from` contracts, LSP rendering | Generic constraints/methods and interface-method defaults remain deferred; bare interfaces intentionally stay unsized |

## P0: correctness

- [x] Fix owned `str` equality compiler panic.
  - Implemented exact byte-content equality/inequality without consuming owners.
  - Added semantic capability validation, target-width LLVM loops, empty/ASCII/UTF-8,
    float, bool, char, and rejected-comparison coverage.
  - GitHub issue creation was attempted but unavailable in this environment.

## P1: tracked remaining work

### `str`

- [ ] [#81 Add runtime string construction and concatenation](https://github.com/PeeperLanguage/compiler/issues/81).
- [ ] [#82 Define explicit string C and FFI bridge](https://github.com/PeeperLanguage/compiler/issues/82).

### Optional

- [ ] [#30 Add optional niche layouts](https://github.com/PeeperLanguage/compiler/issues/30). Keep source semantics unchanged and preserve tagged fallback.

### Enum

- [ ] [#89 Add value-producing match expressions](https://github.com/PeeperLanguage/compiler/issues/89).
- [ ] [#91 Add enum protocols and metadata operations](https://github.com/PeeperLanguage/compiler/issues/91).
- [ ] [#92 Compact tagged variants into shared union payload storage](https://github.com/PeeperLanguage/compiler/issues/92).
- [ ] [#94 Add contextual enum variant construction and inference](https://github.com/PeeperLanguage/compiler/issues/94).
- [ ] [#95 Add advanced enum match patterns](https://github.com/PeeperLanguage/compiler/issues/95).
- [ ] [#96 Define explicit enum representation and foreign ABI](https://github.com/PeeperLanguage/compiler/issues/96).

### Struct, enum, and interface generics

- [ ] [#90 Add advanced generics for functions and methods](https://github.com/PeeperLanguage/compiler/issues/90): constraints, defaults, inference, generic functions/methods, and monomorphization. Explicit generic named structs, enums, interfaces, and aliases already work.

### Interface defaults

- [ ] Design interface-method default parameters. `docs/default-parameters.md` explicitly defers them; no dedicated GitHub issue exists.

## P1: stale tracking cleanup

- [ ] Reconcile [#100 Define zero-sized empty structs and anonymous structural values](https://github.com/PeeperLanguage/compiler/issues/100).
  - Core goal is already implemented: `let marker: struct {} = .{};` checks, lowers, links, and runs with bundled compiler.
  - Existing runtime coverage also instantiates named empty `ConsumerImpl` in `x_test/runtime_interface_nested`.
  - Close issue if those semantics are accepted. Otherwise rewrite body to list only missing ABI/ownership/container edge coverage instead of claiming empty structs are unsupported.
- [ ] Correct stale `docs/language-spec.md` remaining-work section.
  - Direct borrowed and owned interface carriers are implemented in HIR/MIR/LLVM and runtime fixtures.
  - Interface returned-reference `from` contracts are implemented and covered by `x_test/type_reference_return_contracts`.
  - `println` is implemented and covered by `x_test/runtime_println`, despite older text saying it is outside first output slice.

## P2: untracked language-surface decisions

These are unsupported by design today. Create separate issues only when product direction adopts them.

- [ ] Decide primitive formatting expansion: `char` output and integers wider than 64 bits.
- [ ] Decide whether arbitrary-width floats beyond `f32`/`f64` enter language surface.
- [ ] Decide compound bitwise assignment operators.
- [ ] Decide optional-to-optional equality, fallback/unwrap syntax, optional chaining, and optional patterns.
- [ ] Decide borrowed `&str` comparison semantics. Direct `str` indexing should remain rejected unless indexing unit changes from explicit byte/character views.

## Solved or current issues

- [x] [#28 Add optional narrowing and payload access](https://github.com/PeeperLanguage/compiler/issues/28): closed and verified in source/fixtures.
- [x] [#29 Define string runtime semantics](https://github.com/PeeperLanguage/compiler/issues/29): foundation closed correctly; #81 and #82 hold extensions.
- [x] [#99 Add `with` variant construction syntax](https://github.com/PeeperLanguage/compiler/issues/99): closed and verified.
- [x] [#101 Allow dead enum match carriers after mixed arm moves](https://github.com/PeeperLanguage/compiler/issues/101): closed and verified.
- [x] Listed open issues remain materially accurate. #100 needs closure or scope rewrite because its core example already works.

## Audit validation

- `GOCACHE=/tmp/peeper-go-cache CCACHE_DISABLE=1 PEEPER_BIN="$PWD/build/bin/peeper" go test -count=1 ./x_test` — pass.
- `GOCACHE=/tmp/peeper-go-cache CCACHE_DISABLE=1 go test -count=1 ./internal/frontend/parser ./internal/semantics/typeinfo ./internal/semantics/typechecker ./internal/semantics/ownership ./internal/ir/... ./internal/backend/llvm ./internal/pipeline` — pass.
- Bundled compiler anonymous empty-struct repro — build and runtime pass.
- Bundled compiler owned-string equality repro — compiler panic reproduced.
