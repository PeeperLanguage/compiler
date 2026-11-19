# Generics: open types and copies

A generic declaration (`fn Max<T>`, `struct Box<T>`, a `const T: type`
parameter) is checked twice over:

1. **Its own check**, once, with no call: each type parameter stands for
   itself, `Type::Parameter { definition, place }`. It reports what is
   wrong whatever the parameters are, and an editor reads its types
   (`given: T`). Its body is never lowered.
2. **Each copy**, one per list of arguments (`Max<i32>`), re-reads the
   written declaration with the parameters bound (`resolve_global` gives
   `Symbol::TypeArgument`). A copy is checked, laid out and lowered like
   any declaration.

## Open types

A type that holds a `Type::Parameter` or `Type::Undecided` is **open**
(`Types::is_open`, stored at intern: it is asked of every type checked, and
a type never changes once interned). An open type means "each copy
decides". `Type::Invalid` keeps one meaning: an error was reported.

What depends on an open type is left to each copy, silently, in the own
check:

| Case | Own check gives |
| --- | --- |
| `t[0]`, `t.*`, `t.x` on a bare `T` | `Type::Undecided`, `ExprKind::Undecided` |
| `t.Name()` on a bare `T` | the arguments are checked; the call is `Undecided` |
| a conversion to or from an open type | `Conversion::Undecided` |
| an operator on an open operand | the operator's own result type |
| printing an open value | the value is left out |
| a `where` constraint on an open subject, a call run at compile time given one | not known (`None`) |
| a field of `Box<T>` | the written field type, re-read with `T` bound (`field_types_given`) |

An open struct, such as `Box<T>` inside a generic body, stays
`Type::Named`: it has no copy and no layout, since its size is each copy's.

## Finding type arguments

A call finds `<T>` by matching types, not syntax: each parameter's type in
the function's own type is the pattern, the argument's type the actual
(`Types::find_parameters`). `Box<T>` against `Box<i32>`, `&[]T` against
`&[3]i32` and nested copies all find `T` the same way.

Given an open argument (a generic body calling another generic function),
no copy is made: the call's type is the callee's own type with the
arguments substituted (`Types::substitute`).
