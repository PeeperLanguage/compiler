//! The tokens the lexer produces.

use source::Span;

/// One token: its kind and where its text is. The text itself is the file's
/// text at `span`; literal values are decoded later.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Token {
    pub kind: TokenKind,
    pub span: Span,
}

/// Declares every token kind once, with its text: the `TokenKind` enum,
/// `TokenKind::text`, and the lexer's `OPERATORS` and `KEYWORDS` lists, which
/// hold the kinds after the `#[operators]` and `#[keywords]` headers, in the
/// order written. Each keyword has a doc comment, and a named kind or an
/// operator may have one, which `TokenKind::doc` gives.
macro_rules! tokens {
    (pub enum TokenKind {
        $(
            $(#[doc = $named_doc:literal] $(#[doc = $named_doc_more:literal])*)?
            $named:ident = $named_text:literal,
        )*
        #[operators]
        $(
            $(#[doc = $operator_doc:literal] $(#[doc = $operator_doc_more:literal])*)?
            $operator:ident = $operator_text:literal,
        )*
        #[keywords]
        $(
            #[doc = $keyword_doc:literal]
            $(#[doc = $keyword_doc_more:literal])*
            $keyword:ident = $keyword_text:literal,
        )*
    }) => {
        #[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
        #[repr(u8)] // parser tables are indexed by kind
        pub enum TokenKind {
            $(
                $(#[doc = $named_doc] $(#[doc = $named_doc_more])*)?
                $named,
            )*
            $(
                $(#[doc = $operator_doc] $(#[doc = $operator_doc_more])*)?
                $operator,
            )*
            $(
                #[doc = $keyword_doc]
                $(#[doc = $keyword_doc_more])*
                $keyword,
            )*
        }

        impl TokenKind {
            /// The text of this kind: a keyword's or operator's spelling, or for
            /// a named kind the words messages use for it.
            pub const fn text(self) -> &'static str {
                match self {
                    $(TokenKind::$named => $named_text,)*
                    $(TokenKind::$operator => $operator_text,)*
                    $(TokenKind::$keyword => $keyword_text,)*
                }
            }

            /// What this keyword, operator or literal is, as Markdown: a first
            /// line that says it, then more and an example; `None` for a token
            /// without a doc. Each line keeps the space after its `///`, which
            /// Markdown drops.
            pub const fn doc(self) -> Option<&'static str> {
                match self {
                    $($(
                        TokenKind::$named => {
                            Some(concat!($named_doc $(, "\n", $named_doc_more)*))
                        }
                    )?)*
                    $($(
                        TokenKind::$operator => {
                            Some(concat!($operator_doc $(, "\n", $operator_doc_more)*))
                        }
                    )?)*
                    $(
                        TokenKind::$keyword => {
                            Some(concat!($keyword_doc $(, "\n", $keyword_doc_more)*))
                        }
                    )*
                    _ => None,
                }
            }
        }

        pub const OPERATORS: &[TokenKind] = &[$(TokenKind::$operator,)*];
        pub const KEYWORDS: &[TokenKind] = &[$(TokenKind::$keyword,)*];
    };
}

tokens! {
    pub enum TokenKind {

        // Kinds whose text varies: `text` describes them.
        Eof = "end of file",
        Ident = "identifier",
        /// A number literal.
        ///
        /// Without a suffix it takes the type its place expects, if it can: a whole number an integer type, and a base-10 number a float type; elsewhere it is `i32`, or `f64` with a `.` or an exponent. A suffix names the type, as in `0xFFu8` or `7f32`. The value must fit the type, and a `-` right before the literal counts: `-128` fits `i8`.
        ///
        /// `0x`, `0o` and `0b` start a base-16, base-8 and base-2 number, and `_` may separate digits.
        ///
        /// ```peeper
        /// let count = 42;      // i32
        /// let half = 2.5;      // f64
        /// let byte = 0xFFu8;   // 255
        /// let small: u8 = 200; // u8
        /// let whole: f32 = 2;  // 2.0
        /// let big = 1_000;     // 1000
        /// ```
        Number = "number",
        /// A string literal: text in double quotes.
        ///
        /// For now only `print`, `println` and `panic` take one, as a `cstr`; other string literals are not supported yet. Its escapes are a c string's.
        ///
        /// ```peeper
        /// println("hello");
        /// panic "stop";
        /// ```
        String = "string",
        /// A c string literal: its bytes and a zero byte after them, as a `cstr`.
        ///
        /// Each escape is one byte: `\n`, `\r`, `\t`, `\0`, `\\`, `\"`, or `\xHH` for the byte with the hex value `HH`.
        ///
        /// ```peeper
        /// let greeting = c"hi\n";
        /// let letters = c"\x41\x42"; // AB
        /// ```
        CString = "c string",
        /// A character literal: one character in single quotes.
        ///
        /// Not supported yet.
        ///
        /// ```peeper
        /// let letter = 'a';
        /// ```
        Char = "character",
        /// A byte literal: one byte in single quotes, after `b`.
        ///
        /// Not supported yet.
        ///
        /// ```peeper
        /// let letter = b'a';
        /// ```
        ByteChar = "byte character",
        DocComment = "document comment",


        // Longest text first within each leading character, so the lexer's
        // first match is the right one.
        #[operators]
        ColonColon = "::",
        Colon = ":",
        EqualEqual = "==",
        FatArrow = "=>",
        Assign = "=",
        BangEqual = "!=",
        BangBang = "!!",
        Bang = "!",
        LessEqual = "<=",
        ShiftLeft = "<<",
        Less = "<",
        GreaterEqual = ">=",
        ShiftRight = ">>",
        Greater = ">",
        AndAnd = "&&",
        /// Refers to a value without copying it.
        ///
        /// `&x` borrows `x` to read it, and `&mut x` to change it, while the reference is used. `&T` and `&mut T` are the types of these references; `.*` reads or writes the value through one.
        ///
        /// ```peeper
        /// fn double(n: &mut i32) {
        ///     n.* = n.* * 2;
        /// }
        ///
        /// let mut n = 5;
        /// double(&mut n); // n is 10
        /// ```
        Ampersand = "&",
        OrOr = "||",
        /// Passes the value on its left to the call on its right, as its first argument.
        ///
        /// `x |> f(a)` calls `f(x, a)`. The call must be of a function, not a method.
        ///
        /// Not supported yet.
        ///
        /// ```peeper
        /// let doubled = 21 |> times(2); // times(21, 2)
        /// ```
        PipeArrow = "|>",
        Pipe = "|",
        QuestionQuestion = "??",
        Question = "?",
        Arrow = "->",
        MinusMinus = "--",
        MinusAssign = "-=",
        Minus = "-",
        PlusPlus = "++",
        PlusAssign = "+=",
        Plus = "+",
        AsteriskAssign = "*=",
        /// Points at a value: `*T` is a pointer type, and `.*` reads through a reference or pointer.
        ///
        /// A `*T` points at a `T`, as `@x` and C functions give. After a reference or pointer, `.*` is the value it refers to: `r.*` reads it, and `r.* = v` writes it through a `&mut` reference or a pointer.
        ///
        /// ```peeper
        /// fn set(p: *i32, value: i32) {
        ///     p.* = value;
        /// }
        ///
        /// let n = 5;
        /// let r = &n;
        /// let copy = r.*; // 5
        /// ```
        Asterisk = "*",
        SlashAssign = "/=",
        Slash = "/",
        PercentAssign = "%=",
        Percent = "%",
        /// Spreads the values on its left into an array literal.
        ///
        /// `a...` gives each element of the array `a`, which must hold the literal's element type, and `(0..3)...` each value of the range, as a `for` loop counts it; the parentheses may be left out. Under `[_]`, a spread range's ends and step must be known at compile time; under `[N]T`, a range known only at run time is counted then, and the program stops unless the entries give exactly `N` values. Spreading anywhere else is not supported yet.
        ///
        /// ```peeper
        /// let a = [_]i32{1, 2};
        /// let b = [_]i32{a..., (5..=7)..., 0}; // [1, 2, 5, 6, 7, 0]
        /// ```
        Ellipsis = "...",
        /// A range from the left value up to the right one, with it.
        ///
        /// A `for` loop counts each value of it, even the type's largest, and counts down when the left value is larger. `a..=b:step` steps by more than one. In an array literal, `(1..=3)...` spreads those values (`...`). A range is a value of the type `range[T]`, `T` the integer type of its values: it can be kept, passed and returned, and it copies.
        ///
        /// ```peeper
        /// for b in 250u8..=255 {
        ///     print(b); // 250 up to 255
        /// }
        /// ```
        DotDotEqual = "..=",
        /// A range from the left value up to the right one, without it.
        ///
        /// A `for` loop counts each value of it, and counts down when the left value is larger. `a..b:step` steps by more than one. In an array literal, `(0..3)...` spreads those values (`...`). A range is a value of the type `range[T]`, `T` the integer type of its values: it can be kept, passed and returned, and it copies. In a type, `[..]T` is a slice. Slices are not supported yet.
        ///
        /// ```peeper
        /// for i in 0..3 {
        ///     print(i); // 0, 1, 2
        /// }
        /// ```
        DotDot = "..",
        /// Reaches into the value on its left.
        ///
        /// `p.x` is the field `x` of the struct `p`, or of the struct a reference `p` refers to, and `a.len` is the length of the array `a`, a `usize`. `r.*` is the value the reference or pointer `r` refers to. Before `{`, as in `.{x = 1}`, it starts a struct literal whose type its place gives. Methods are not supported yet.
        ///
        /// ```peeper
        /// let p: Point = .{x = 3, y = 4};
        /// let r = &p;
        /// let a = p.x;              // 3
        /// let b = r.*.y;            // 4
        /// let n = [2]i32{1, 2}.len; // 2
        /// ```
        Dot = ".",
        /// The address of a variable's value, as a pointer.
        ///
        /// `@x` gives a `*T` that points at `x`, which must be a place that can be written. A write through it is a write to `x`.
        ///
        /// ```peeper
        /// let mut x = 1;
        /// let p = @x;
        /// p.* = 7; // x is 7
        /// ```
        At = "@",
        Caret = "^",
        Tilde = "~",
        Comma = ",",
        Hash = "#",
        Semicolon = ";",
        LeftParen = "(",
        RightParen = ")",
        LeftBrace = "{",
        RightBrace = "}",
        /// Indexes an array, or writes an array type.
        ///
        /// `a[i]` is the value at index `i`, counted from 0. An index outside the array stops the program, or is an error when it is a constant. In a type, `[N]T` is an array of `N` values of type `T`.
        ///
        /// ```peeper
        /// let squares = [3]i32{1, 4, 9};
        /// let middle = squares[1]; // 4
        /// ```
        LeftBracket = "[",
        RightBracket = "]",

        // In the order of their text, so the lexer finds the keywords starting
        // with each letter together.
        #[keywords]
        /// Leaves out something the compiler works out.
        ///
        /// As an array literal's length, `_` is the number of values written.
        ///
        /// ```peeper
        /// let squares = [_]i32{1, 4, 9}; // a [3]i32
        /// ```
        Underscore = "_",
        /// Converts a value to another type.
        ///
        /// A conversion that keeps every value, such as `i32` to `i64`, needs no `as`. One that can change the value must be written with `as`: a float to an integer drops the fraction, toward zero.
        ///
        /// ```peeper
        /// let whole = 3.7 as i32; // 3
        /// let wide: i64 = whole;  // no `as` needed
        /// ```
        As = "as",
        /// Leaves the innermost loop at once.
        ///
        /// ```peeper
        /// for {
        ///     if done {
        ///         break;
        ///     }
        /// }
        /// ```
        Break = "break",
        /// Names a value known while compiling.
        ///
        /// A constant is declared in a module or in a function body. Its value may use other constants, operators and calls: a call whose arguments are constants runs while compiling, and the program gets only its result.
        ///
        /// ```peeper
        /// const LIMIT = 10 * 2;
        /// const FIB_20 = fib(20);
        /// ```
        Const = "const",
        /// Skips the rest of the loop's block and goes on with the next pass of the innermost loop.
        ///
        /// ```peeper
        /// for v in 0..10 {
        ///     if v == 3 {
        ///         continue;
        ///     }
        ///     println(v);
        /// }
        /// ```
        Continue = "continue",
        /// Runs its block when the condition of the `if` before it is false. `else if` tests another condition.
        ///
        /// ```peeper
        /// if n > 0 {
        ///     println(1);
        /// } else if n < 0 {
        ///     println(-1);
        /// } else {
        ///     println(0);
        /// }
        /// ```
        Else = "else",
        /// Declares a type whose value is one of its cases.
        ///
        /// Not supported yet.
        ///
        /// ```peeper
        /// enum Shape {
        ///     Circle,
        ///     Square,
        /// }
        /// ```
        Enum = "enum",
        /// The `bool` value false.
        ///
        /// ```peeper
        /// let done = false;
        /// ```
        False = "false",
        /// Declares a function.
        ///
        /// A function takes parameters, each with its type, and gives a result of the type after `->`; without `->` it gives nothing. A function whose name starts with an upper case letter is exported: other modules can call it.
        ///
        /// ```peeper
        /// fn add(a: i32, b: i32) -> i32 {
        ///     return a + b;
        /// }
        /// ```
        Fn = "fn",
        /// Repeats its block.
        ///
        /// - `for { }` repeats until `break`.
        /// - `for condition { }` repeats while the condition is true.
        /// - `for v in a..b { }` runs once for each value from `a` up to, but not including, `b`; `a..=b` includes `b`, and `a..b:step` steps by more than one. A range whose start is past its end counts down.
        /// - `for v in array { }` runs once for each element.
        /// - `for i, v in ... { }` also counts the passes, from 0.
        ///
        /// ```peeper
        /// for v in 0..10:2 {
        ///     println(v); // 0, 2, 4, 6, 8
        /// }
        /// ```
        For = "for",
        /// Gives back memory that was allocated.
        ///
        /// Not supported yet.
        ///
        /// ```peeper
        /// free(p);
        /// ```
        Free = "free",
        /// Runs its block when its condition, a `bool`, is true. Parentheses around the condition are allowed but not needed.
        ///
        /// ```peeper
        /// if n > 0 {
        ///     println(n);
        /// }
        /// ```
        If = "if",
        /// Declares an interface: the methods a type must have.
        ///
        /// Not supported yet.
        ///
        /// ```peeper
        /// iface Shape {
        /// }
        /// ```
        Iface = "iface",
        /// Makes another module's exported names usable here, through the module's name.
        ///
        /// ```peeper
        /// import "app:utils";
        ///
        /// fn main() {
        ///     let sum = utils::Add(1, 2);
        /// }
        /// ```
        Import = "import",
        /// Names what a `for` loop goes over.
        ///
        /// After a function's result type, `in` names the parameters that a returned reference may point into.
        ///
        /// ```peeper
        /// for v in values {
        /// }
        ///
        /// fn slot(holder: &mut Holder) -> &mut i32 in holder
        /// ```
        In = "in",
        /// Tests whether a value is a given enum case.
        ///
        /// Not supported yet.
        ///
        /// ```peeper
        /// if shape is Shape::Circle {
        /// }
        /// ```
        Is = "is",
        /// Declares a variable.
        ///
        /// Its type comes from its value, unless written after `:`. Without `mut` it cannot change once it is set. `let x: T;` declares it without a value: its first assignment sets it.
        ///
        /// ```peeper
        /// let count = 0;
        /// let mut total: i64 = 0;
        /// total = total + 1;
        /// ```
        Let = "let",
        /// Picks a block by which case a value is.
        ///
        /// Not supported yet.
        Match = "match",
        /// Lets something change.
        ///
        /// `let mut` declares a variable that can be assigned again. `&mut T` is a reference through which the value can be changed.
        ///
        /// ```peeper
        /// let mut n = 0;
        /// n = n + 1;
        /// ```
        Mut = "mut",
        /// The null address: a pointer that points nowhere.
        ///
        /// It takes its pointer type from where it is used.
        ///
        /// ```peeper
        /// let p: rawptr = none;
        /// ```
        None = "none",
        /// Gives the value on its left, or the one on its right when the left one is `none`.
        ///
        /// Not supported yet.
        Orelse = "orelse",
        /// Stops the program.
        ///
        /// It writes `panic:` and the message to the standard error, with where the `panic` is written, and exits with code 101. Code after it does not run.
        ///
        /// ```peeper
        /// if index >= length {
        ///     panic "index out of range";
        /// }
        /// ```
        Panic = "panic",
        /// Writes its values to the standard output, separated by spaces, with no new line after them.
        ///
        /// ```peeper
        /// print(n, total);
        /// ```
        Print = "print",
        /// Writes its values to the standard output, separated by spaces, then a new line.
        ///
        /// ```peeper
        /// println(n, total);
        /// ```
        Println = "println",
        /// A pointer that does not say what it points to, like C's `void *`.
        ///
        /// Convert it with `as` to a typed pointer before reading through it.
        ///
        /// ```peeper
        /// fn read(p: rawptr) -> i32 {
        ///     let typed = p as *i32;
        ///     return typed.*;
        /// }
        /// ```
        Rawptr = "rawptr",
        /// Ends the function. In a function with a result, it gives the value after it to the caller.
        ///
        /// ```peeper
        /// fn square(n: i32) -> i32 {
        ///     return n * n;
        /// }
        /// ```
        Return = "return",
        /// Declares a type made of named fields.
        ///
        /// A struct literal sets each field.
        ///
        /// ```peeper
        /// struct Point {
        ///     x: i32,
        ///     y: i32,
        /// }
        ///
        /// let origin = Point.{ x = 0, y = 0 };
        /// ```
        Struct = "struct",
        /// The `bool` value true.
        ///
        /// ```peeper
        /// let done = true;
        /// ```
        True = "true",
        /// Gives another name to a type. The two names stand for the same type.
        ///
        /// ```peeper
        /// type Meters = i64;
        /// ```
        Type = "type",
        /// Gives an enum case its payload, in a value or a pattern.
        ///
        /// Not supported yet.
        ///
        /// ```peeper
        /// let shape = Shape::Circle with 2.0;
        /// ```
        With = "with",
    }
}

#[cfg(test)]
mod tests {
    use super::TokenKind;

    #[test]
    fn text() {
        assert_eq!(TokenKind::Fn.text(), "fn");
        assert_eq!(TokenKind::ColonColon.text(), "::");
        assert_eq!(TokenKind::Ident.text(), "identifier");
    }
}
