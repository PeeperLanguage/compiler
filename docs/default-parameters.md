# Default Parameter Design

## Status

Proposed prerequisite for allocator provenance. No compiler behavior implements
this document yet.

## Goal

Allow callers to omit trailing arguments whose declarations provide defaults:

```peep
fn Retry(count: i32, delay: i32 = 100) {}

Retry(3)
Retry(3, 250)
```

Default arguments must preserve normal call ABI, evaluation order, ownership,
borrow checking, imported symbol behavior, and source diagnostics. They are
declaration conveniences, not optional values and not function overloading.

Allocator target surface becomes:

```peep
fn alloc<T>(
    value: T,
    allocator: Allocator = allocator::Default(),
) -> *T
```

Generic function execution remains separate work. Initial `alloc` compiler
operation may expose same one-or-two-argument call surface while inferring `T`
from `value`; it must use same default-argument expansion path.

## Syntax

Parameters with defaults require both name and explicit type:

```text
parameter = ["mut"] name ":" type ["=" expression]
```

Valid:

```peep
fn Open(path: cstr, flags: i32 = 0) {}
fn Update(mut count: i32 = 0) {}
```

Invalid:

```peep
fn Open(path: cstr = "data", flags: i32) {} // required after default
fn Open(path = "data") {}                  // missing explicit type
```

After first defaulted parameter, every later parameter must also have default.
This makes every valid call a positional prefix and avoids named arguments or
middle-argument omission.

Receivers cannot have defaults. Function-type syntax cannot contain defaults;
defaults belong to declarations, not callable type identity.

First implementation supports top-level functions, extern declarations in the
same semantic module, and concrete receiver methods. Imported defaults use the
same declaration metadata when their substituted expression names are visible
through the caller’s resolved module graph. Interface-method defaults are
deferred until interface call metadata has the same canonical expansion path.

## Semantics

### Call-site expansion

Compiler expands omitted suffix at call site. Callee always receives full
parameter list. HIR, MIR, LLVM signatures, function pointers, vtables, and
extern ABI do not gain optional parameters, presence flags, overload wrappers,
or alternate entrypoints.

Given:

```peep
fn F(a: i32, b: i32 = B(), c: i32 = C()) {}
F(A())
```

evaluation order is:

1. `A()`
2. `B()`
3. `C()`
4. enter `F`

Providing an argument suppresses its default completely. Defaults evaluate once
per omitted argument and once per call.

### Declaration context

Default expressions resolve and type-check in declaring module scope, not caller
scope. This keeps private helper references, imports, shadowing, and symbol
identity stable across local and imported calls.

Defaults may reference the receiver and earlier supplied parameters. Call-site
expansion substitutes those references with already-supplied argument
expressions, so the
callee never sees a caller-local parameter symbol. Move-only earlier parameters
are rejected when reused by a default; Copy values and reference parameters use
the same expression and borrow rules as explicit arguments. Function-body locals
remain unavailable.

Default expression must be assignable to declared parameter type under same
rules as explicit argument. Invalid default is diagnosed once at declaration,
even if every current call provides argument.

### Callable identity

Defaults do not participate in `FuncType` equality or ABI compatibility. These
declarations have same callable type:

```peep
fn A(value: i32) {}
fn B(value: i32 = 1) {}
```

Defaults are available only while call target still resolves to declaration
metadata. Converting function or method to plain function value loses defaults:

```peep
let callback = B
callback() // error: function value requires one argument
```

This prevents hidden declaration metadata from leaking into function-pointer
ABI.

### Public API surface

Default syntax and expression are part of declaration surface fingerprint.
Changing a public default must invalidate dependent workspace modules and LSP
snapshots even though function ABI is unchanged.

Exported defaults remain source-level API metadata. Future separately compiled
module interfaces must serialize their resolved default representation before
binary-only imports can support omission.

## Canonical Phase Model

### AST and parser

`ast.Param` gains optional default expression. AST inspection and parser surface
fingerprints traverse it. Parser enforces explicit parameter type and reports
recovery diagnostics; declaration validation enforces trailing-default rule.

### Resolution and declaration checking

Resolve default once against declaration module scope. Typecheck it with declared
parameter type as expected type. Move-only parameter reuse is rejected at this
declaration boundary; call-site ownership then validates substituted arguments
with normal move and borrow rules.

Do not add default expressions to `typeinfo.FuncType`. Declaration symbol already
owns AST node and therefore default metadata.

### Call argument plan

After callee resolution, expand one ordered argument list keyed by the call node.
Each omitted default is cloned with receiver/earlier-parameter references
substituted by already-evaluated argument expressions. Synthetic nodes receive
fresh IDs so semantic caches remain call-local.

All later call consumers use this plan:

- typechecker arity and assignability
- return-origin source mapping
- ownership call consumption and two-phase borrow ordering
- HIR call lowering
- LSP call diagnostics when applicable

Do not independently append defaults in each phase.

Explicit arguments belong to caller module. Default expressions are validated in
declaration scope, then lowered from their call-site substitution. This keeps
private declaration checks at the declaration boundary while making substituted
arguments visible to ownership and HIR in the caller.

### HIR and lower phases

HIR call contains full argument list after expansion. Default expressions lower
through normal expression lowering using declaring module semantic context.
MIR and backend remain unaware that argument was omitted in source.

This preserves existing direct call, method call, extern call, evaluation order,
drop, and ABI paths.

## Arity and Diagnostics

For `R` required parameters and `N` total parameters, valid explicit argument
count is `R..N`. Diagnostics report range when count is outside it:

```text
function expects 1 to 3 arguments, got 0
```

Other required diagnostics:

- `required parameter cannot follow parameter with default`
- `defaulted parameter requires explicit type`
- `receiver cannot have a default value`
- `function types cannot declare default values`
- `default value cannot reuse move-only parameter`

Default-expression type errors point at declaration. Call-site arity errors point
at call and may show full rendered signature with defaults.

## LSP and Text Rendering

Hover and completion render defaults from AST declaration:

```text
fn Retry(count: i32, delay: i32 = 100)
```

Semantic-only function values render only types because they do not carry
defaults. Rename and reference traversal include names inside default
expressions. Incremental workspace fingerprints include default expression.

## Validation

- parser: valid trailing defaults, recovery, missing type, required-after-default,
  receiver and function-type rejection
- semantics: omitted/provided arity, expected typing, declaration-scope lookup,
  imported private helper, invalid default without calls, function-value loss
- ownership: move-only default temporary, discarded internal owner cleanup,
  explicit/default evaluation order, two-phase calls unaffected
- origins: expanded argument positions remain correct; reference defaults follow
  existing borrow/origin rules
- HIR/MIR/backend: full argument list and unchanged function ABI
- LSP/workspace: hover text, rename traversal, fingerprint invalidation
- `x_test/`: positive runtime evaluation-order/default-suppression fixture and
  negative syntax/type/reference/function-value fixtures
- full capped Go suite, race suite, vet, bundle, bundled fixture validation,
  formatting, diff, and artifact checks

## Deferred Work

- inferred parameter types from defaults
- named arguments or middle omission
- move-only defaults referencing earlier parameters
- staged evaluation for effectful explicit arguments reused by defaults
- staged evaluation for one omitted default reused by a later omitted default
- reference-bearing defaults and returned-reference origin composition
- interface-method defaults
- preservation through function values
- generic function execution and monomorphization

These require separate design. None should introduce wrapper overloads or a
second call ABI.
