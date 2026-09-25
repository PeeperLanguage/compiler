# Frontend: source text to AST

This map records implementation observed in `internal/frontend/lexer`, `token`, `parser`, and `ast`. Verify mutable details against source. It describes the current source-to-AST path without prescribing future boundaries; see [`compiler-architecture.md`](../compiler-architecture.md) for broader context.

## Boundary and data flow

```text
source text
    │
    ▼
lexer.New(file, input, diagnostics)
    │
    ├─ Lexer.Tokenize() ──► []token.Token, ending in EOF
    │
    ▼
parser.New(file, stream, diagnostics)
    │
    └─ Parser.ParseModule() ──► *ast.Module
                                  │
                                  ├─ imports
                                  ├─ statements/declarations
                                  └─ syntax fingerprints
```

- `lexer` scans characters, decodes literals, tracks positions, and reports
  lexical diagnostics.
- `token` owns kinds, keyword/name rules, and built-in type predicates.
- `parser` consumes tokens, constructs syntax nodes, reports diagnostics,
  recovers, and assigns IDs.
- `ast` owns node types, traversal, locations, cloning, indexing, and surfaces.
- Frontend output is syntax; binding, type resolution, THIR, control flow,
  ownership, MIR, and backend work happen after parsing.

`Lexer.Tokenize` returns a copy of its token slice. `ParseModule` retains
parser-produced nodes, including recovery nodes.

## Token package

### `token/kinds.go`

`Kind` is a string-backed token category. Categories include:

- sentinels: `ILLEGAL`, `EOF`;
- values: `IDENT`, `NUMBER`, `STRING`, `CSTRING`, `CHAR`, `BYTE_CHAR`,
  `DOC_COMMENT`;
- operators and punctuation: assignment, arithmetic, comparison, logical,
  bitwise, pipe, range, delimiters, separators, and `HASH`;
- keywords: declarations (`import`, `const`, `type`, `struct`, `iface`, `enum`,
  `union`, `error`, `fn`, `test`), bindings (`let`, `mut`), control flow,
  ownership/runtime operations, type-related words, literals, and `unsafe`.

### `token/token.go`

`Token` contains:

- `Kind`: lexical category;
- `Literal`: decoded literal text for strings/chars, normalized doc-comment text,
  or the matched source spelling for other tokens;
- `Start` and `End`: `source.Position` values.


### `token/keywords.go`

The package-level keyword map is the sole keyword classification table.

- `LookupIdent` returns a keyword kind or `IDENT`.
- `IsKeyword` tests reserved words.
- `KeywordDocByKind` and `KeywordDoc` expose short descriptions from the same
  table.


### `token/identifier.go`

`IdentifierPattern` accepts an ASCII letter or underscore followed by ASCII
letters, digits, or underscores. `IsValidSymbolName` additionally rejects `_`
and all keywords. This is a name-validity predicate, not a lexer rule: keyword
classification still occurs through `LookupIdent`.

### `token/builtins.go`

`IsBuiltinType` recognizes `bool`, `byte`, `char`, `str`, `usize`, `isize`,
`f32`, `f64`, and integer type names accepted by `numeric.ParseIntegerTypeName`.
`ParseIntegerBuiltin` resolves `usize` and `isize` using target pointer width;
other integer names use the numeric package. These helpers classify and resolve
built-in names; they do not create AST nodes.

## Lexer

### State: `lexer/lexer.go`

`Lexer` stores:

- `file`: diagnostic filename;
- `input`: complete source string;
- `pos`: current `source.Position`;
- `diag`: shared diagnostic bag;
- `toks`: accumulated tokens.

`New` initializes `pos` with `source.NewPosition`, which is line 1, column 1,
index 0. A nil diagnostic bag is replaced with a new bag.

`Tokenize` repeatedly takes `remainder`, tries `regexPatterns` in declaration
order, and requires each match to begin at offset zero. The first matching
pattern runs its handler. If no pattern matches, the lexer decodes one UTF-8
rune, advances over it, and reports an illegal-character diagnostic. It then
continues scanning. After input is consumed, it appends one EOF token.

Pattern order is part of behavior. Longer or more specific forms precede their
prefixes: doc comments precede line comments, C strings precede strings,
compound operators precede single operators, and `...`, `..=`, and `..`
precede `.`. Whitespace and ordinary comments are skipped.

### Handlers and output

- `defaultHandler` emits a fixed kind with the matched source spelling.
- `identifierHandler` emits `LookupIdent(match)` and retains the spelling.
- `numberHandler` emits `NUMBER` without numeric interpretation.
- `docHandler` emits `DOC_COMMENT`; it removes the `///` prefix and trims the
  remaining text. Normal `//` and `/* ... */` comments are discarded.
- `stringHandler` and `cstringHandler` remove delimiters, decode escapes, and
  emit `STRING` or `CSTRING`.
- `charHandler` decodes a single-quoted value and requires exactly one valid
  UTF-8 rune.
- `byteCharHandler` decodes a `b'...'` value and requires exactly one byte.

String and character handlers advance over the complete matched source before
validating escapes. Invalid escapes report a diagnostic and emit no token.
Invalid character width reports a diagnostic and emits no token. The scanner
continues after both cases.

`unescapeQuoted` supports `\\`, `\n`, `\r`, `\t`, `\0`, quote escapes matching
the delimiter, and `\x` followed by exactly two hexadecimal digits. Unknown,
truncated, or delimiter-incompatible escapes return an error. `reportEscapeError`
turns that error into a diagnostic spanning the matched literal.

### Positions

`source.Position` has byte `Index`, one-based `Line`, and one-based `Column`.
`Position.Advance` iterates decoded runes: newline increments `Line`, resets
`Column` to 1, and increments `Index` by 1; tab advances one column and one
byte; other runes advance one column and add their UTF-8 byte width to `Index`.

Thus `Index` is a byte offset, while line and column count decoded characters;
tabs count as one column. Token spans are half-open in practice: handlers save
`Start` before advancing and store the resulting position as `End`. A one-token
span runs from the token's first position to the position immediately after its
source text.

`source.NewLocation` copies start and end positions and stores the filename.
AST locations use these token-derived positions. `Location.GetText` converts
line/column spans back to source text using rune slices, so AST and diagnostic
ranges must retain the same one-based convention.

## Parser

### Shared state: `parser/parser.go`

`Parser` stores the file path, token stream, diagnostic bag, current token index,
next AST node ID, a context stack, and the `controlHeader` flag. `New` receives
the already-tokenized stream; the lexer is not called by the parser.

`current`, `next`, and `prev` inspect the stream without advancing. `advance`
returns the current token and increments `pos`. `at` tests a kind. `match`
advances only when a kind matches. `consume` diagnoses a missing expected token
without advancing. `expectClose` handles a required closing delimiter, reporting
an unclosed-delimiter diagnostic at EOF or an expected-token diagnostic before
synchronizing.

`ParseModule` creates an `ast.Module` with file path, import slice, and statement
slice. It consumes redundant top-level semicolons, parses imports, then parses
module-level statements. Declarations enter `Module.Stmts`; top-level `let` is
diagnosed and omitted; non-declaration statements are diagnosed. Failed progress
at module scope triggers synchronization and, if necessary, one token advance.
The module surface is finalized after the loop.

### Declarations and metadata

`parseStmt` selects declaration, block, control-flow, jump, match, return, or
expression parsing from the current token. It parses leading doc comments and
attributes first, attaching them only to matching node interfaces.

`parseImport` accepts a string path, optional `as` identifier, and semicolon.
It produces `ImportDecl` and records the raw path for the module surface.

Declaration parsers produce these AST shapes:

- `parseFnDecl`: optional receiver, name, type parameters, parameters, return
  type, return-origin clause, and either a block body or nil body for a trailing
  semicolon;
- `parseLetDecl` and `parseConstDecl`: name, optional type, optional value,
  mutability/module-scope flags, and a semicolon span;
- `parseStructDecl`, `parseInterfaceDecl`, and `parseEnumDecl`: named wrappers
  whose `Type` field contains the canonical `StructType`, `InterfaceType`, or
  `EnumType` payload;
- `parseTypeAliasDecl`: name, type parameters, and underlying type.

`parseFnSignature`, `parseParam`, `parseParams`, and related routines are shared
by function declarations, function types, and interface methods. A return
origin clause uses `from name` or `from (name, ...)` and is represented by
`ReturnOriginClause`.

`parseAttributes` parses `#[name]` and `#[name(args)]`; one attribute per block
is enforced diagnostically. `parseLeadingMetadata` merges consecutive `///`
comments, ignores ordinary comments, and prevents a same-line preceding
non-doc token from attaching its doc comment to the next item.

### Types: `parser/parse_types.go`

`parseTypeExpr` dispatches on the leading token:

- `&` and optional `mut` -> `RefType`;
- `?` or `??` -> nested `OptionalType` construction, with redundant-marker info;
- `*` -> `OwnedPtrType`;
- `rawptr` -> `RawPtrType`;
- brackets -> fixed, owner/dynamic, or slice `ArrayType`;
- `fn` -> `FuncType`;
- `struct`, `iface`, `enum` -> anonymous type bodies;
- identifier paths -> `NamedType`, `AppliedType`, or `ScopeResolution`.

`parseTypeArguments` parses comma-separated type expressions. When the lexer
produces `SHR` for `>>`, `consumeTypeArgumentClose` splits it into two positioned
`>` tokens by mutating the current stream entry. This supports nested generic
syntax without changing lexer precedence.

Braced type lists use `parseBracedItemList`. It is shared by struct fields,
interface methods, enum variants, and match/other braced syntax where called.
It handles comments, redundant commas, missing separators, synchronization, and
closing delimiters. Missing type syntax reports parser diagnostics and returns
nil rather than inventing a type node.

### Expressions: `parser/parse_expr.go`

Expressions use a Pratt parser. `nudLookup` maps prefix kinds to null-denotation
handlers; `ledLookup` maps infix/postfix kinds to left-denotation handlers;
`precTable` contains binding powers. `parseExpr` parses one prefix expression,
then consumes operators whose precedence is above the requested threshold.
Semicolon, comma, right parenthesis, and right brace terminate the loop.

Prefix forms include numeric, string/C string, byte, char, boolean, `none`,
identifiers and paths, grouping, unary operators, address expressions,
`free`, `print`/`println`, composite literals, and array literals.

Infix and postfix forms include:

- pipe `|>`;
- logical, bitwise, equality, relational, shift, additive, and multiplicative
  operators;
- `as` casts;
- calls, indexes, and selectors.

The parser builds `BinaryExpr`, `UnaryExpr`, `CallExpr`, `IndexExpr`,
`SelectorExpr`, `AsExpr`, `RangeExpr`, `IsExpr`, `AddressExpr`, `FreeExpr`, and
`PrintExpr` as appropriate. Pipe parsing inserts the left expression as the
first call argument and marks the call `Piped`; it rejects method targets.

Identifier paths can represent scoped names, generic applications, composite
literals, and enum variants. `parseVariantCasePath` requires a fully named path
for enum cases. Control-header parsing uses `controlHeader` to distinguish a
following block from a composite literal. Array literals parse a type and a
braced value list; `_` infers length from the number of values.

Numeric interpretation is delegated to `numeric.ParseLiteral`. The parser adds
an invalid-number diagnostic on failure and a leading-zero warning for decimal
integer literals. Literal signs are included in the resulting `NumberLit.Value`
when the parser handles a signed numeric literal directly.

### Statements: `parser/parse_stmt.go`

`parseBlock` consumes `{`, repeatedly parses statements, and closes at `}` or
EOF. It registers a `BlockStmt` even when recovery leaves a partial span.

Supported statement shapes include:

- expression and assignment statements;
- `let` and `const` declarations;
- `if` with optional `else` or chained `else if`;
- `for` condition loops and `for ... in` loops with one or two bindings;
- `break` and `continue`;
- `match` with case paths, optional payload bindings/fields, and block arms;
- `return` with optional value;
- function/type declarations nested where the grammar permits them.

Missing bodies produce partial nodes with the parsed prefix preserved. Labeled
loop jumps are parsed far enough to diagnose that labels are unsupported.

### Recovery

Recovery is designed to preserve later syntax and produce a usable partial AST.

- `synchronize` advances until EOF, a delimiter, a declaration/control-flow
  boundary, or one of caller-provided kinds.
- `expectClose` reports the missing delimiter, synchronizes, and consumes it if
  found.
- `parseBracedItemList` synchronizes at commas or closing braces and explicitly
  advances an unexpected separator to avoid repeated diagnostics without progress.
- Module and block loops check whether `pos` changed. If not, they synchronize
  and force one token of progress when needed.
- Missing semicolons use a synthetic token position based on the last parsed
  child; the token is not inserted back into the stream.
- Invalid expressions and statements use `BadExpr` and `BadStmt` nodes where a
  node is needed to retain tree shape and source location.

Diagnostics are added to the shared `DiagnosticBag`; recovery does not turn a
syntax error into semantic acceptance. Downstream phases must account for
recovery nodes and nil optional children.

## AST

### Node contracts: `ast/node.go`

`Node` requires a private location accessor, child traversal, and `ID`/`SetID`.
The private methods keep node families closed to this package. `Stmt`, `Expr`,
and `TypeExpr` add family markers. `Expr` also requires `copyExpr` and stable
`exprText`; `TypeExpr` requires `TypeText`.

`Module` stores `FilePath`, optional module documentation, imports, statements,
and import/export fingerprints. `ForEachDecl` is the canonical iterator for
module declarations because recovery can leave non-declaration statements in
`Module.Stmts`.

### Node families and important types

`ast/decl.go` defines identifiers and type syntax plus declaration wrappers.
Important type nodes are `NamedType`, `AppliedType`, `OwnedPtrType`, `RawPtrType`,
`RefType`, `OptionalType`, `ArrayType`, `FuncType`, `StructType`,
`InterfaceType`, `EnumType`, and `ScopeResolution`. Their fields retain source
names, nested types, parameters, payloads, variants, and locations.

Declaration wrappers are `ImportDecl`, `LetDecl`, `ConstDecl`, `FnDecl`,
`TypeAliasDecl`, `StructDecl`, `InterfaceDecl`, `EnumDecl`, and `BadDecl`.
Named type declarations expose `DeclName`, declaration type parameters, and
`UnderlyingType`. Named struct/interface/enum declarations keep their canonical
payload in `Type`, rather than duplicating a second type representation.

`ast/stmt.go` defines `BlockStmt`, `ExprStmt`, `AssignStmt`, `ReturnStmt`,
`IfStmt`, `ForStmt`, `BreakStmt`, `ContinueStmt`, `MatchStmt`, `MatchArm`, and
`BadStmt`. `ForStmt` separates condition loops (`Cond`) from iterable loops
(`Index`, `Value`, `Iterable`). `MatchStmt` keeps an `ArmListLocation` in
addition to its full location.

`ast/expr.go` defines identifier/path expressions, selectors and indexes, ranges,
struct/variant/array literals, bad and scalar literals, address/unary/binary
expressions, calls, `free`, print expressions, casts, and `is` expressions.
Every expression supplies child traversal, location access, source-like text,
and expression cloning.

### Documentation and attributes: `ast/meta.go`

`CommentGroup` stores merged text and its source location. `Documented` stores a
comment and declaration surface. `Attributed` stores attributes and their
expression arguments. `AttributeDefinitions` describes known attribute targets
and argument shapes; frontend parsing stores syntax, while later validation owns
semantic enforcement. `FunctionLinkName` reads a valid `extern` attribute for an
extern function with no body.

### Locations and safe access: `ast/location.go`

`LocOf` is typed-nil safe and returns a node location. `StartOf` and `EndOf`
return copied positions or a fresh zero/default position when a node or location
is absent. Parsers use these helpers for spans whose end may be a child node,
closing delimiter, or recovery fallback.

AST locations are source spans, not semantic ranges. They commonly start at the
opening keyword/operator and end after the last consumed token. Partial nodes
may end at the last successfully parsed token. Synthetic fallback positions are
used only to keep diagnostics and recovery nodes located.

### IDs, traversal, and index: `ast/inspect.go`

`Parser.nextID` increments a parser-local `NodeID`; `reg` assigns that ID to
new non-nil nodes. IDs are assigned in construction order, beginning from the
parser's zero value. Parser-assigned IDs are the identity used by source AST
consumers.

`ast.Inspect` performs depth-first traversal. It calls the visitor on a node,
recurses through that node's `forEachChild` children when the visitor returns
true, then calls the visitor with nil after the children. Each AST type owns its
immediate child enumeration once; generic consumers should use `Inspect`.

`ast.Index` walks every module statement with `Inspect` and returns a
`map[NodeID]Node`. It indexes source nodes by their IDs, including recovery nodes
that implement `Node`; it does not index module-level fields outside statements.

### Clone and substitution: `ast/clone.go`

`SubstituteExpr` clones an expression for call-site/default-argument expansion.
Identifier nodes whose names occur in the substitutions map are replaced by a
clone of the corresponding argument expression. All other expression and
embedded type nodes are recursively cloned.

Each clone receives a fresh synthetic ID from `NewSyntheticNodeID`. Synthetic IDs
share one identity space and set the high bit, separating them from parser IDs.

`SubstituteExpr` returns the cloned expression plus maps from each new ID to its
original ID for default-derived and argument-derived clones.

Clone methods preserve values, flags, field order, and source locations while
allocating new node IDs. `cloneTypeExpr` covers named/applied
, pointer,
reference/optional, array, function, struct, interface, enum, and scope-resolution
types. It panics on an unhandled type expression so adding a type cannot silently
skip clone behavior. `copyExpr` is implemented by each expression type, making
new expression kinds explicit at compile time.

### Text and fingerprints: `fingerprint/fingerprint.go` and `parser/surface.go`

`ExprText` and each expression's `exprText` provide stable source-like text.
Type nodes provide `TypeText`. These forms are used when building declaration
surfaces, not for reparsing source.

`parser/surface.go` collects import paths and declaration shapes while
`ParseModule` already visits each top-level item. It serializes names, type
parameters, parameter/return types, fields, enum payloads, and default
expressions. Function bodies are excluded from declaration surfaces.

`fingerprint.Parts` sorts surface parts, length-prefixes each part, and hashes
them with SHA-256. `Module.ImportFingerprint` and
`ExportFingerprint` therefore represent syntax-level change inputs for
incremental compilation. Semantic phases may add later semantic evidence; the
syntax fingerprint does not replace that work.

## File map

Every non-test frontend file participates:

- `lexer/lexer.go`: regex scanner, literal decoding, diagnostics, positions;
  `token/{kinds,token,keywords,identifier,builtins}.go`: kinds, payloads,
  keyword/name rules, built-in type predicates.
- `parser/parser.go`: state, module/declaration parsing, recovery, IDs;
  `parser/{parse_expr,parse_stmt,parse_types,surface}.go`: expressions,
  statements/types, and syntax surfaces.
- `ast/node.go`: node interfaces and module contracts; `ast/{location,meta,decl,
  expr,stmt}.go`: locations, metadata, declarations, types, expressions,
  statements, and child traversal.
- `ast/{inspect,clone,import}.go`: traversal/indexing, synthetic cloning,
  and import-path extraction.
- `fingerprint/fingerprint.go`: shared content and unordered-part hashing.

## Cross-package invariants

1. Lexer positions advance from the `source.Position` state used for token and
   AST locations; parser consumes the EOF-terminated stream without rescanning.
2. Every parser-created non-nil AST node is registered before it is returned.
3. Recovery may produce partial nodes, nil children, `BadExpr`, or `BadStmt`, but
   loop boundaries must still make token progress.
4. Each node owns its `forEachChild` shape; generic traversal uses `ast.Inspect`.
5. Parser IDs identify source nodes; clone IDs are synthetic and non-colliding.
6. Syntax fingerprints describe imports and declaration surfaces only. Frontend
   preserves source structure and positions; later phases own semantic facts and
   consume the AST handoff in [`docs/compiler-architecture.md`](../compiler-architecture.md).
