# Ember Compiler TODO

## Parser

- [x] Module parsing
- [x] Imports
- [x] Binding declarations
  - [x] `let`
  - [x] `let mut`
  - [x] `const`
  - [x] Optional type annotation
  - [x] Required initializer
- [x] Function declarations
  - [x] Name
  - [x] `Type::method` name
  - [x] Receiver
  - [x] Type parameters
  - [x] Parameters
  - [x] Return type
  - [x] Body block
  - [x] Bodyless declaration with `;`
  - [x] Simple attribute syntax
- [x] Statements
  - [x] Block
  - [x] Binding
  - [x] Return
  - [x] Expression statement
- [x] Expressions
  - [x] Identifier
  - [x] Number literal
  - [x] String literal
  - [x] Unary
  - [x] Binary
  - [x] Call
  - [x] Parenthesized
- [x] Type syntax
  - [x] Named type
  - [x] `::` path type
  - [x] Function type
  - [x] Struct type
  - [x] Interface type
  - [x] Enum type
- [x] Basic recovery
- [ ] Control-flow statements
- [ ] Assignment and field/index expressions
- [ ] Struct/enum/array literals
- [ ] Pointer/reference/optional/error types
- [ ] Attribute arguments and AST attachment
- [ ] `test` declarations

## Analyzer

- [x] Scope table
- [x] Symbol model
- [x] Predeclared constants: `true`, `false`, `none`
- [x] Builtin type recognition
- [x] Analyzer pipeline hook
- [ ] Collector pass
- [ ] Resolver pass
- [ ] Typechecker pass
- [ ] Const evaluation
- [ ] Usage analysis
- [ ] Ownership analysis

## HIR

- [x] Pipeline phase hook
- [x] Placeholder HIR text artifact
- [ ] HIR data model
- [ ] AST to HIR lowering
- [ ] Typed HIR
- [ ] HIR-local lowering/desugaring

## MIR

- [x] Pipeline phase hook
- [x] Placeholder MIR text artifact
- [ ] MIR data model
- [ ] HIR to MIR lowering
- [ ] CFG-backed control flow
- [ ] Ownership-ready places/temps

## Codegen

- [x] LLVM backend option
- [x] LLVM pipeline phase hook
- [x] Placeholder LLVM IR artifact
- [x] `-keep-gen` writes backend IR artifacts
- [ ] Real LLVM IR lowering
- [ ] Object/executable generation
- [ ] Link step
- [ ] Real `run`
- [ ] Real `test`
- [ ] WASM backend
