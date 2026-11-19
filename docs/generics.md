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
a type never changes once interned). An open type has no copy of its
own: no layout, and a call given one makes none. `Type::Invalid` keeps
one meaning: an error was reported.

What depends on a type each copy decides (a bare `T`, or `Undecided`,
`Type::is_decided_per_copy`) is left to each copy, silently, in the own
check. A type that only holds one, as `[2]T` or `Box<T>`, is still an
array or a struct, and what is wrong for it whatever `T` is, is reported:
`-pair` or `let flag: bool = pair` for `pair: [2]T`.

| Case | Own check gives |
| --- | --- |
| `t[0]`, `t.*`, `t.x`, `t.Name()` on a bare `T` | `Expr::UNDECIDED` (a call's arguments are checked) |
| a conversion that some copy makes and another refuses: `T` to `i64`, `&mut T` to `&i32`, `&Box<T>` to an interface reference | `Conversion::Undecided` (`Types::could_be_same`) |
| an operator on a bare `T` | the operator's own result type |
| printing a bare `T`, or a reference to one | the value is left out |
| `for` over a bare `T`, or a reference to one | the body is checked as a range's, its value undecided |
| a `where` constraint on a bare `T`, a call run at compile time given an open type | not known (`None`) |
| `x...` of a bare `T` in `[_]E{ ... }` or a `...` argument | the array literal is of `Type::Undecided`: its length is each copy's count; a `...` argument still lends it as the parameter's slice; `[N]E{ ... }` keeps its type, and more than `N` values counted now is reported |
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
