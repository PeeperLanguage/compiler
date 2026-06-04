# Ownership Foundation Plan

## Goal

Establish Ember's ownership, reference, and unsafe boundary model before full struct and method support lands.

The design target is:

- safe by default
- value-oriented
- deterministic destruction
- explicit aliasing and mutation
- low-level escape hatches through narrow `unsafe` features

## Semantic Direction

### Value Categories

Every type belongs to one of these categories:

- `Copy`
- `Move`
- `Ref`
- `RawPtr`

Rules:

- builtin scalars are `Copy`
- safe references are `Copy`
- raw pointers are `Copy`
- structs derive behavior from their fields
- owning resource types are `Move`

### References

Safe references:

- `&T`
- `&mut T`

Properties:

- non-owning
- non-null
- no arithmetic
- shared/exclusive aliasing model

### Raw Pointers

Unsafe pointers:

- `*const T`
- `*mut T`

Properties:

- outside safe alias guarantees
- used for FFI, allocators, intrusive structures, MMIO, and low-level runtime work

### Mutation

- bindings are immutable by default
- `let mut` enables mutation of the binding
- mutating through a reference requires `&mut`

### Destruction

- deterministic destruction at scope end
- reverse declaration order
- moved values are not destroyed twice

## Phased Implementation

### Phase 1: Ownership Surface

- add AST nodes for ref/raw-pointer types
- add AST nodes for borrow expressions
- parse `&T`, `&mut T`, `*const T`, `*mut T`
- parse `&expr`, `&mut expr`
- represent these in semantic `typeinfo`

### Phase 2: Value Semantics

- define `Copy` vs `Move`
- add move tracking for locals
- diagnose use-after-move
- teach struct value semantics to derive from fields

### Phase 3: Borrow Checking

- validate borrowable lvalues
- enforce shared vs exclusive local borrow rules
- reject obvious escaping references
- restrict reference returns to provable cases

### Phase 4: Unsafe Boundary

- gate raw-pointer dereference and pointer arithmetic behind `unsafe`
- add unsafe call boundary rules
- define safety contract for unsafe APIs

### Phase 5: Struct Integration

- field access on named struct types
- borrowing fields through aggregate paths
- method receivers with value / `&self` / `&mut self` semantics

## Current Slice

This change starts Phase 1 only:

- ownership syntax in parser
- ownership shapes in semantic types
- focused tests for the new forms

Move analysis, aliasing enforcement, and unsafe-only operations remain follow-up work.
