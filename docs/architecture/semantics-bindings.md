# Semantics: bindings, types, and constants

This map records binding, type, place, intrinsic, and constant implementation observed in source. Verify mutable details against linked symbols. It describes current sequencing and ownership without prescribing future boundaries; see [`compiler-architecture.md`](../compiler-architecture.md) for broader context.

## Scope and phase position

- `internal/pipeline/pipeline.go` schedules semantic work per module.
- Relevant order is: collect -> bind -> resolve -> constants -> typecheck.
- Typechecking may query constants before final types; `consteval.FinalizeValues`
  republishes authoritative module constants after typechecking.
- Later phases consume published evidence; they do not rediscover names, types,
  variant cases, or call adaptation from syntax.
- `project.Module` stores staged results for one semantic generation.

## AST identity and NodeID evidence

`internal/frontend/ast/node.go` defines `ast.NodeID` as `uint32`.

- Every AST `Node` exposes `ID()` and `SetID()`.
- Semantic side tables use `NodeID`, not AST pointer identity, as their key.
- `bindingresult.Result` owns syntax-occurrence-to-symbol identity behind `Bind` / `Symbol`.
- The same result owns block-to-scope identity behind `SetScope` / `Scope`.
- `typecheckresult.Result` owns base expression, call, and control evidence behind
  semantic operations such as `RecordExprType` / `ExprType`, `RecordMatch` /
  `Match`, and `RecordForIteration` / `ForIteration`.
- Default-binding provenance, call expansion, conversions, interface proofs,
  intrinsic dispatch, and selector decisions are likewise published and queried
  through methods; their backing NodeID indexes are private.
- Generated AST nodes are reindexed by `Module.RebuildTypedASTIndex`.
- Default-expression cloning assigns fresh IDs while preserving whether an
  occurrence came from the default declaration or caller argument.
- Evidence keyed by `NodeID` lets CFG, flow, effects, ownership, and lowering
  consume one decision without repeating syntax resolution.

## Symbol IDs and symbol records

`internal/semantics/symbols/symbol.go` owns symbol identity.

- `symbols.SymbolID` is `uint64`.
- `symbols.New` allocates IDs from a process-wide atomic counter.
- IDs are unique for symbol objects created during the compiler process.
- Symbol identity is pointer-stable through semantic handoff; consumers compare
  symbol pointers or IDs rather than names when identity matters.
- A `symbols.Symbol` contains name, kind, semantic type, visibility, mutability,
  usage state, compiler operation, defining module, source location, AST node,
  and an optional child scope.
- `Symbol.Kind` includes import, variable, constant, type, function, method,
  parameter, field, static, variant, error member, and unknown.
- `Symbol.IsPub` is derived from the first rune of the name being uppercase.
- `Symbol.BindType` writes the semantic type once a phase has one.
- `symbols.GetSymbolType` is the shared type read path; absent type returns false.
- `Symbol.IsMutable` uses parameter state for parameters and `LetDecl` state for
  other let bindings.
- `DefiningModule` distinguishes declaration-module symbols from caller symbols.
- `CompilerOp` identifies compiler-owned functions such as `alloc`, `append`,
  `reserve`, `resize`, `shrink`, `len`, `as_bytes`, `as_chars`, and `from_bytes`.

## Scope chains

`internal/semantics/symbols/scope.go` owns lexical lookup.

- A `Scope` stores parent, name -> `SymbolID`, ID -> symbol, and declaration order.
- `NewScope(parent)` links one lexical scope to its parent.
- `Declare` rejects duplicate non-underscore names; `_` stays ordered/by ID but is
  not name-addressable.
- `LookupLocal` is current-scope only; `Lookup` walks parents, so nearest scope
  wins and shadowing is lexical.
- `Symbols` preserves declaration order; declaration identity comes from the binding result rather than scanning symbol AST pointers.
- `IsMutableBinding` combines lookup, kind, and `Symbol.IsMutable`.
- `Parent` supports bounded analyses such as `place.LocalRoot`.

## Binding result

`internal/semantics/bindingresult/result.go` owns the staged symbol/scope graph behind
semantic operations. `Bind` / `Symbol` publish and query syntax identity; `SetScope` /
`Scope` publish and query lexical scopes. `RegisterMethod` / `Methods` own receiver
method membership using semantic nominal declaration identity, not display text, and
`AddOperationFunction` owns the completion catalog. The backing indexes are private,
so collection, binding, resolution, typechecking, HIR, and LSP depend on meaning rather
than map layout.

## Collection

`internal/semantics/collector/collector.go` owns the declaration catalog.

### Entry points and state

- `collector` holds compiler context and current module.
- `collector.Collect(ctx, module)` creates a collector and calls `collectModule`.
- `collectModule` creates `ModuleScope` with `ctx.GlobalScope` as parent.
- It resets semantic data before collecting the current AST.
- Import aliases are declared as `SymbolImport` with `UnknownType`.
- Duplicate imports produce `ErrAmbiguousImport`.
- Top-level statements are scanned through the AST declaration list.

### Declaration symbols

- `collectNode` recognizes type declarations, functions, top-level lets, and
  top-level constants.
- `collectFnDecl` creates `SymbolFunc` for ordinary functions.
- A method gets `SymbolMethod`, a child scope parented by module scope, and its
  declaration name is published through `Bindings.Bind`.
- Collection does not invent a textual receiver key. Binder resolves the semantic
  receiver type and then calls `Bindings.RegisterMethod`.
- Duplicate methods for one semantic receiver/name pair produce a redeclaration
  diagnostic after receiver binding.
- Ordinary functions are declared in module scope; duplicate names are diagnosed.
- `collectModuleBinding` creates `SymbolVar` or `SymbolConst` with
  `UnknownType`; binder later supplies an explicit type.

### Named type shells

- `collectConcreteTypeDecl` creates `SymbolType` in module scope.
- It assigns module-qualified nominal identity through
  `Module.TypeDeclarationIdentity`.
- Declaration parameters become `typeinfo.TypeParameterType` values carrying
  parameter name, owner identity, and declaration index.
- Type aliases, structs, interfaces, and enums receive distinct
  `typeinfo.DefinedKind` values.
- The symbol's `Type` is a `*typeinfo.DefinedType` shell.
- Enum declarations get a separate child scope with `SymbolVariant` entries.
- Each variant symbol points at the enum defined type.
- Variant declaration identifiers are published through `Bindings.Bind` immediately.
- `ctx.RegisterTypeDeclaration` retains declaration syntax and shell for generic
  substitution.
- Underlying type structure is intentionally not completed by collection.

## Binding

`internal/semantics/binder/binder.go` fills collected type state and signatures.

- `binder` holds compiler context and module.
- `binder.Bind` requires AST and module scope, then calls `bindModule`.
- `bindModule` computes type declaration order before binding definitions.
- It binds ordered declarations, then performs one legal completion-cycle pass.
- It calls `ctx.CompleteTypeInstances` for completed generic bases.
- It then binds functions and top-level value declaration types.
- Operation functions are sorted by name after binding.

### Function and value types

- `bindFunctionDecl` uses `typeinfo.FuncTypeFromDeclWithOptions`.
- Method and ordinary-function declarations resolve through the same `Bindings.Symbol`
  declaration identity. Method signatures are bound before receiver registration.
- Ordinary function signatures are written to the module-scope symbol.
- Functions with parameters are added to `OperationFunctions`.
- `bindModuleBinding` binds an explicit source type through `TypeFromSyntax`.
- Missing top-level types retain `UnknownType` for later inference/recovery.
- `moduleScopeSymbol`, `bindModuleScopeType`, and
  `bindModuleScopeTypeIfUnset` centralize module-local symbol access and writes.

### Named type binding

- `bindTypeDecl` reuses the collected `DefinedType` shell when present.
- Reusing the shell preserves identity for recursive self-references.
- It refreshes name and module-qualified identity.
- Type parameters are installed in a `TypeParameterBindings` environment.
- `TypeFromSyntax` builds the underlying semantic type with those parameters.
- The defined type remains nominal; `Underlying` is its representation/query path.

### Dependency ordering

`internal/semantics/binder/type_decl_cycles.go` owns named-type dependencies.

- `typeDeclarationOrder` creates one graph node per collected type declaration.
- `typeDeclNodeID` formats nodes as `type:<module-id>:<name>`.
- `addTypeDeclEdges` walks type syntax and records value, indirect, and completion
  dependencies.
- Inline value references participate in illegal cycle detection.
- Pointers, references, function signatures, and generic argument positions are
  treated as indirect for layout-cycle purposes.
- Alias and applied declarations also receive completion edges.
- `RawPtrType` adds no pointee layout dependency.
- `OwnedPtrType` and `RefType` recurse as indirect references.
- Optional and array shape preserve the surrounding indirectness rules.
- Struct fields and enum payloads recurse according to storage position.
- Unknown type syntax is a panic at this exhaustive semantic boundary.
- Value-layout cycles produce `ErrCircularDependency`.
- Completion cycles are legal and are returned for bounded completion passes.
- Imported type references use `project.LookupImportedSymbol`.

## Generic type cache

`internal/project/generic_types.go` owns generic declaration registration and
instantiation. It is part of binding/type construction, not resolver lookup.

- `RegisterTypeDeclaration` records declaration syntax by base nominal identity.
- `instantiateType` requires argument count equal to parameter count.
- Each argument is canonicalized with `typeinfo.Unalias`.
- Invalid arguments reject the instance.
- Declaration-parameter arguments return the base shell directly.
- Other arguments form a cache identity from base identity plus canonical argument
  identities.
- Defined types use their own identity; parameters use owner, index, and text;
  other types use the semantic type key.
- The cache stores `namedTypeInstance` entries with base, instance, readiness,
  owner module, and completion state.
- A provisional `DefinedType` shell is cached before substituting its underlying
  type.
- Recursive pointer/reference applications can therefore resolve to the same
  instance object.
- A second request for an incomplete instance waits on its readiness channel.
- Invalid construction removes its cache entry and wakes waiters.
- Exact repeated recursive application returns the cached shell.
- A recursive application that changes canonical type arguments is invalid and
  receives a diagnostic.
- `typeInstanceUnderlying` substitutes parameters and recursively instantiates
  nested applications through the same cache.
- `CompleteTypeInstances` rebuilds legal cached instances in place after binder
  fills declaration shells; pointer identity remains stable.
- This cache is separate from constant evaluation's lazy query cache.

## Resolver

`internal/semantics/resolver/resolver.go` owns lexical, import, and variant path
resolution. `resolver/suggest.go` supplies resolver suggestions; it does not own
binding state or type construction.

### Module and function entry

- `Resolve(ctx, module)` creates a resolver and calls `resolveModule`.
- `resolveModule` creates a binding result if collection did not create one.
- Resolver records top-level variable and constant symbol IDs in its local pending
  set before values resolve, then removes each ID after its initializer.
- Functions resolve after top-level bindings.
- Method and ordinary-function symbols are found through declaration identity; ordinary functions also remain in
  module scope.
- Function parameters are declared in the function-owned symbol scope.
- A default parameter expression resolves before that parameter is declared.
- This makes receiver and earlier parameters visible, while rejecting self and
  later-parameter references at the declaration boundary.
- Receiver is represented as the first parameter and marked `IsReceiver`.
- Parameter declaration identifiers and return-origin identifiers are recorded in
  `NodeSymbols`; return origins also mark their source parameter used.
- Function bodies resolve using the function scope.

### Blocks and declarations

- `resolveBlock` records the block ID -> scope mapping.
- Explicit nested blocks receive `NewScope(parent)`.
- Local lets and constants are declared by `resolveLocalBinding` before resolving
  their initializer. Their declaration-name ID maps to the symbol in `NodeSymbols`,
  while their symbol ID is held in resolver's pending set during the initializer.
- Loop index/value bindings are declared in a body scope.
- Match arm bindings and field bindings are declared in arm scopes.
- Unsupported declaration statements inside blocks produce a diagnostic.
- Statement dispatch resolves expressions in source evaluation order.

### Expression names and paths

- Literals need no symbol lookup.
- An identifier uses `Scope.Lookup`, records its symbol under the identifier ID,
  and marks the symbol used.
- Import aliases cannot be used as unqualified values.
- A symbol found in resolver's pending set produces `ErrUseBeforeDecl`.
- Missing names go through unresolved-symbol reporting.
- Selectors resolve their base; indexes resolve base then index; calls resolve
  callee then arguments.
- `resolveScopeResolution` handles imported `module::member` paths and records
  the resolved symbol under the path ID.
- Qualified value paths reject type arguments.
- Imported symbols must be public; module and export diagnostics are emitted here.
- `reportGlobalQualifier` compares symbol IDs, not names, for redundant aliases.

### Enum variants

- `resolveVariantPath` separates enum qualifier and case name.
- Local, applied, and imported enum qualifiers are supported by their path shape.
- `project.CanonicalEnumDeclaration` supplies the declared enum symbol.
- Variant lookup uses the enum symbol's child scope.
- Qualifier, enum, and variant identifiers/path IDs receive binding evidence.
- Qualifier, enum, and variant symbols are marked used.
- The resolver records names and paths; typechecker later records case and payload
  typing evidence.

## Type information

`internal/semantics/typeinfo` is a sealed semantic type model. Its non-test files
are `types.go`, `syntax.go`, `relations.go`, `compatibility.go`, `lookup.go`,
`structure.go`, `semantic_key.go`, `equality.go`, `representation.go`,
`capabilities.go`, and `ownership.go`.

### Type nodes

- `Type` requires human-facing `Text`, compiler-facing `structure`, and intrinsic
  equality, representation, and ownership operations.
- Primitive nodes include invalid, unknown, integer, byte, char, float, bool,
  cstr, string, none, and allocator types.
- `NamedType` represents unresolved or builtin-like names.
- `TypeParameterType` identifies a generic declaration parameter.
- `DefinedType` carries name, nominal identity, defined kind, parameters,
  concrete arguments, and underlying type.
- Composite nodes include owned pointer, raw pointer, reference, optional, array,
  function, struct, interface, and enum types.
- `FuncType` carries parameter types/names, return type, and return-origin contract.
- `VariantDescriptor` classifies optional and named enum case sets.
- `ArrayShape` distinguishes fixed, owner, and slice arrays.

### Syntax conversion

- `TypeFromSyntax` converts AST type syntax using `SyntaxOptions`.
- Options provide target, self type, parameter bindings, named/qualified lookup,
  generic instantiation, and invalid-input callbacks.
- `Self` resolves to receiver type, abstract `Self`, or invalid type according to
  options.
- Named and applied types resolve through callbacks and enforce argument arity.
- Optional syntax uses `NewOptional`.
- Fixed array lengths are canonicalized and checked against declared and target
  `usize` representability.
- Function, struct, interface, and enum syntax recursively construct semantic
  children.
- Interface receiver syntax permits abstract `Self`; ordinary method syntax does not.
- Interface methods publish value/shared/mutable `MethodReceiver` evidence instead
  of retaining an encoded `Self` type in ordinary parameters.
- `Method.CallableTypeFor` materializes concrete receiver as parameter zero.
- `returnOriginContract` converts source names to parameter slots.
- `ReturnOriginSources` maps a call's contract slots back to receiver/argument AST.

### Aliases, nominal types, and optionals

- `Unalias` follows transparent alias underlying types and detects alias cycles.
- It does not erase nominal structs, interfaces, or enums.
- `Underlying` follows all defined-type underlying links without alias-only limits.
- `NewOptional` unwraps aliases and collapses consecutive optional layers.
- Optional collapse stops at pointer, array, or nominal boundaries.
- A detected optional cycle returns `InvalidType`.
- Optional source semantics are structural; named enums preserve nominal identity.
- `VariantDescriptorOf` returns `Absent` and `Present(payload)` for optionals.
- Named enum descriptors retain declaration identity and declaration-ordered cases.
- `SameType` checks nominal enum identity before structural comparison.
- Named structs compare by nominal identity when both operands are nominal.
- Other composite comparisons recurse over their semantic fields and children.

### Structural traversal and capability queries

- Each type's private structure owns local semantic attributes and ordered child
  slots once. `SemanticKey` serializes that structure with collision-safe framing.
- `ForEachChild` exposes present structure children with `TypeChildRelation`.
- Relations distinguish underlying, owned, borrowed, optional, array, field,
  enum payload, parameter, return, generic-parameter, and generic-argument edges.
- Consumers choose recursion policy; type structure is not reimplemented in each
  analysis.
- `IsSizedType`, `IsLowerableType`, and `OwnershipCapabilityOf` delegate to
  required per-type operations with independent query-owned cycle state.
- Primitive scalar values are implicitly copyable; strings and owned storage are
  not implicitly copyable and require drop handling.
- Optional, array, struct, interface, and enum ownership methods compose child
  capabilities through canonical `ForEachChild` structure.
- `UseKind` is the per-expression ownership classification published by typecheck.
- Capability results constrain but do not replace per-use `ValueUses` evidence.

### Lookup and compatibility

- `ReceiverTarget`, `PointerTarget`, `ReferenceTarget`, and
  `ReferenceValueTarget` answer receiver/pointer/reference shape questions.
- Optional wrappers are transparent to `ReferenceValueTarget` but not to direct
  receiver and method lookup rules.
- `ReceiverIdentity` normalizes aliases and pointer/reference carriers to the nominal
  declaration identity that owns the method set.
- `LookupStructField` centralizes field type and index lookup.
- `LookupVariantCase` centralizes case lookup over a descriptor.
- `CheckCompatibility` returns conversion kind and compatibility classification.
- Compatible conversions include identity and approved widening/optional/reference
  cases; explicit casts and incompatible cases remain distinct.
- `typecheckresult.ImplicitConversions` stores selected conversion evidence by ID.

## Places and origins

`internal/semantics/place/addressable.go` and `origin.go` own storage identity.

- A place root is a symbol plus ordered projections.
- `Project` recognizes one selector or non-slicing index projection.
- `Decompose` returns root expression and projections in source order.
- Slices are not independently addressable places; range indexes become wildcard
  origin projections in `Resolve`.
- `Addressable` answers whether an identifier or projection names addressable
  storage, including pointer/reference bases.
- `MutableAddressable` also reports shared immutable reference targets and mutable
  binding symbols.
- `LocalRoot` finds a caller-local root below module scope and stops at pointer
  indirection.
- `Binding` carries a symbol and a transient `Local` flag.
- `BindingResolver` supplies symbols for identifiers injected into cloned defaults.
- Expanded defaults use `Local=false`; declaration-module storage must not become a
  caller pointer-escape source.
- `Resolve` is the canonical place walk and returns storage origins, value origins,
  dependencies, and stability.
- Identifiers start with a symbol root.
- Selectors append field projections.
- Constant indexes append stable index projections.
- Integral identifier indexes append binding-index projections and dependencies.
- Unknown indexes and range indexes append wildcard projections.
- Owned pointers append pointee projections for the projected base.
- Proven variant cases append variant-payload projections.
- Reference and raw-pointer value origins can be normalized through callbacks.
- `OriginsOverlap` is conservative; concrete different fields/fixed indexes prove
  disjointness, while prefixes, wildcards, and symbolic indexes may overlap.

## Intrinsics

`internal/semantics/intrinsics/functions.go` defines compiler-owned functions.

- `FunctionDefinition` pairs a `CompilerOp`, `FunctionKind`, and signature factory.
- `Operations` returns the sorted operation list.
- `PredeclaredSymbols` creates unbound compiler-owned function symbols.
- `ApplicableFunctionSymbols` filters operations by base type.
- `LookupFunction` resolves an operation definition.
- Intrinsic symbols carry `CompilerOp`, public visibility, and a semantic function
  type; they have no source AST node.
- Dynamic-array operations require an owner array and mutable reference receiver.
- `alloc` rejects types containing stored references and returns an owned pointer.
- Collection `len` accepts strings and arrays and returns target `usize`.
- `as_bytes` returns a byte slice with return origin from its input.
- `as_chars` returns an owned character array.
- `from_bytes` accepts a byte slice plus allocator and returns a string.
- Typechecking records chosen intrinsic dispatch in `CompilerCalls` by call ID.
- Lowering consumes that evidence rather than repeating intrinsic applicability.

## Constant evaluation

`internal/semantics/consteval/consteval.go` evaluates semantic constants.

- `EvaluateExpr` evaluates one expression with optional expected type context.
- `FinalizeValues` recomputes module constants after final symbol types exist.
- `evaluator.inProgress` is keyed by `symbols.SymbolID` and detects cycles.
- A module constant read from another module uses `ctx.PublishedConstant`.
- Local module constants query published values first, then the lazy query cache.
- Top-level authoritative values are published only during finalization.
- Expected numeric types influence literal construction and identifier adaptation.
- Numeric literals use default or explicit numeric types and target-aware parsing.
- Boolean and string literals produce typed constant values.
- Constant identifiers must resolve to `SymbolConst`.
- Unary and binary operations delegate folding to `constvalue`.
- Variant constructions use typechecker `VariantConstructions` evidence.
- Non-copyable variant types are not evaluated as constants.
- Struct payloads are evaluated by declaration field order.
- Named enum constants retain descriptor identity and case index.
- Constant enum `is` tests use typechecker `CaseTests` evidence.
- A failed fold is not silently treated as a constant.

`internal/semantics/constantresult/result.go` separates the two lifetimes behind
behavioral operations:

- `Publish` / `Published` own authoritative module values exported through the
  declaring module and included in semantic export facts.
- `Cache` / `Cached` own lazy or expected-type-sensitive results for current analysis.
- Publishing a symbol removes its provisional cached value, so one declaration cannot
  remain represented in both lifetimes after finalization.
- Query-cache-only changes do not alter semantic export fingerprints.
- Storage is private and keyed by `symbols.SymbolID`.

## Constant values

`internal/constvalue/value.go` defines sealed `Value` nodes: `IntConst` (`big.Int`
and canonical type), `FloatConst` (`f32`/`f64`), `BoolConst`, `StringConst`
(`str`/`cstr`), and `VariantConst` (nominal identity, type ID, case index, ordered
payloads). Constructors validate type IDs and payloads; integers normalize finite
width and floats round `f32`. `FoldUnary`/`FoldBinary` implement supported folds,
reject invalid shifts and zero integer divisors, and return typed booleans for
comparisons/logicals. Accessors copy integer/payload state; variant truthiness is
false.

## Typecheck result

`internal/semantics/typecheckresult/result.go` is the published base semantic
contract for later phases.

- `Result` maps stable `NodeID` values to facts: interface implementations,
  case tests, matches/arms/bindings, loop plans, variant constructions, compiler
  calls, checked-loop expansions, expression types, value uses, and reference
  argument mutability.
- `ForIteration` carries typed symbols, element type, plan, and guaranteed entry;
  `RangeIteration` carries limit/ordinal, while `SequenceIteration` carries the
  hidden carrier and carrier type.
- `MatchBinding` carries projection, field, type, symbol, and discard state.
- Query methods (`MatchCases`, `ArmBindings`, `SequenceCarrier`, and others) hide
  representation from consumers.
- `CallArgumentsOrSource` returns expanded arguments when published, otherwise
  source arguments during recovery. Successful loop evidence is complete; consumers
  do not re-check its source syntax.

## End-to-end data flow

1. Parser creates AST nodes with stable `NodeID` values.
2. Collector creates module scope, declaration symbols, type shells, enum variant
   symbols, and generic declaration registry entries.
3. Binder orders named type declarations, detects illegal value cycles, fills
   underlying types and function/value signatures, and completes legal generic
   cache cycles.
4. Resolver creates lexical child scopes, declares local symbols, resolves names,
   imports, enum paths, defaults, and initialization boundaries.
5. Typechecking writes expression types and all later-phase decisions into
   `typecheckresult.Result` keyed by `NodeID`, performing lazy constant queries only
   where a typing decision needs one.
6. Constant finalization recomputes typed module constants and publishes authoritative
   values for cross-module use.
7. Place resolution, CFG, flow, effects, ownership, HIR, MIR, and backend consume
   these artifacts through explicit queries.
8. No downstream phase should perform a second independent name lookup, type
   adaptation, variant-case discovery, intrinsic dispatch, or constant fold when
   the corresponding result already exists.
