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
/// order written.
macro_rules! tokens {
    (pub enum TokenKind {
        $($named:ident = $named_text:literal,)*
        #[operators]
        $($operator:ident = $operator_text:literal,)*
        #[keywords]
        $($keyword:ident = $keyword_text:literal,)*
    }) => {
        #[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
        #[repr(u8)] // parser tables are indexed by kind
        pub enum TokenKind {
            $($named,)*
            $($operator,)*
            $($keyword,)*
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
        Number = "number",
        String = "string",
        CString = "c string",
        Char = "character",
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
        Ampersand = "&",
        OrOr = "||",
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
        Asterisk = "*",
        SlashAssign = "/=",
        Slash = "/",
        PercentAssign = "%=",
        Percent = "%",
        Ellipsis = "...",
        DotDotEqual = "..=",
        DotDot = "..",
        Dot = ".",
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
        LeftBracket = "[",
        RightBracket = "]",

        // In the order of their text, so the lexer finds the keywords starting
        // with each letter together.
        #[keywords]
        Underscore = "_",
        As = "as",
        Break = "break",
        Const = "const",
        Continue = "continue",
        Else = "else",
        Enum = "enum",
        False = "false",
        Fn = "fn",
        For = "for",
        Free = "free",
        If = "if",
        Iface = "iface",
        Import = "import",
        In = "in",
        Is = "is",
        Let = "let",
        Match = "match",
        Mut = "mut",
        None = "none",
        Orelse = "orelse",
        Panic = "panic",
        Print = "print",
        Println = "println",
        Rawptr = "rawptr",
        Return = "return",
        Struct = "struct",
        True = "true",
        Type = "type",
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
