//! The parsing engine, which is core: the tokens, the lexer, the shape of a
//! parse rule, the table of every feature's rules, and the Pratt loop. A
//! feature brings rules (`Feature::rules`); the core reads names, blocks and
//! expression statements itself.

use model::span::Span;

use crate::check::check_expression_statement;
use crate::check::check_name;
use crate::check::named_type;
use crate::feature::Feature;
use crate::tree::BlockNode;
use crate::tree::DeclNode;
use crate::tree::ExprNode;
use crate::tree::Kind;
use crate::tree::NodeId;
use crate::tree::StmtNode;
use crate::tree::Tree;
use crate::tree::TypeNode;

/// Every token. A keyword is a word some feature lists
/// (`Feature::keywords`); which one it is, is the lexeme's `keyword`.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum Token {
    End,
    Number,
    Name,
    Plus,
    Minus,
    Star,
    Less,
    Dot,
    DotDot,
    Comma,
    Colon,
    ColonColon,
    Semicolon,
    Arrow,
    Equal,
    Ampersand,
    LeftParen,
    RightParen,
    LeftBracket,
    RightBracket,
    LeftBrace,
    RightBrace,
    Keyword,
}

const TOKENS: usize = Token::Keyword as usize + 1;

/// The most keywords all features together may list.
const KEYWORDS: usize = 32;

#[derive(Clone, Copy, Debug)]
pub struct Lexeme {
    pub token: Token,
    /// For a `Token::Keyword`, its index in the rule table's keywords.
    pub keyword: u8,
    pub span: Span,
}

#[derive(Debug)]
pub struct ParseError {
    pub span: Span,
    pub message: String,
}

pub type Parsed<T> = Result<T, ParseError>;

pub fn scan(source: &str, rules: &RuleTable) -> Parsed<Vec<Lexeme>> {
    let bytes = source.as_bytes();
    let mut lexemes = Vec::new();
    let mut at = 0;
    while at < bytes.len() {
        let start = at;
        let byte = bytes[at];
        if byte.is_ascii_whitespace() {
            at += 1;
            continue;
        }
        if byte == b'/' && bytes.get(at + 1) == Some(&b'/') {
            while at < bytes.len() && bytes[at] != b'\n' {
                at += 1;
            }
            continue;
        }
        let mut keyword = 0;
        let token = if byte.is_ascii_digit() {
            while at < bytes.len() && bytes[at].is_ascii_digit() {
                at += 1;
            }
            Token::Number
        } else if byte.is_ascii_alphabetic() || byte == b'_' {
            while at < bytes.len() && (bytes[at].is_ascii_alphanumeric() || bytes[at] == b'_') {
                at += 1;
            }
            match rules.keyword(&source[start..at]) {
                Some(index) => {
                    keyword = index;
                    Token::Keyword
                }
                None => Token::Name,
            }
        } else {
            let (token, width) = match (byte, bytes.get(at + 1)) {
                (b'.', Some(b'.')) => (Token::DotDot, 2),
                (b':', Some(b':')) => (Token::ColonColon, 2),
                (b'-', Some(b'>')) => (Token::Arrow, 2),
                (b'+', _) => (Token::Plus, 1),
                (b'-', _) => (Token::Minus, 1),
                (b'*', _) => (Token::Star, 1),
                (b'<', _) => (Token::Less, 1),
                (b'.', _) => (Token::Dot, 1),
                (b',', _) => (Token::Comma, 1),
                (b':', _) => (Token::Colon, 1),
                (b';', _) => (Token::Semicolon, 1),
                (b'=', _) => (Token::Equal, 1),
                (b'&', _) => (Token::Ampersand, 1),
                (b'(', _) => (Token::LeftParen, 1),
                (b')', _) => (Token::RightParen, 1),
                (b'[', _) => (Token::LeftBracket, 1),
                (b']', _) => (Token::RightBracket, 1),
                (b'{', _) => (Token::LeftBrace, 1),
                (b'}', _) => (Token::RightBrace, 1),
                _ => {
                    return Err(ParseError {
                        span: span(start, start + 1),
                        message: "this character is not part of the language".to_string(),
                    });
                }
            };
            at += width;
            token
        };
        lexemes.push(Lexeme {
            token,
            keyword,
            span: span(start, at),
        });
    }
    lexemes.push(Lexeme {
        token: Token::End,
        keyword: 0,
        span: span(at, at),
    });
    Ok(lexemes)
}

fn span(start: usize, end: usize) -> Span {
    Span {
        start: start as u32,
        end: end as u32,
    }
}

/// Binding strength of an infix rule. A rule reads its right side with
/// `parser.expression(Precedence(own + 1))`, so equal ones group leftward.
#[derive(Clone, Copy, PartialEq, Eq, PartialOrd, Ord, Debug)]
pub struct Precedence(pub u8);

/// How a rule reads the rest, which also says where the rule may start.
#[derive(Clone, Copy)]
pub enum Read {
    /// It begins an expression: a literal, `[`, `&`.
    Prefix(fn(&mut Parser<'_>) -> Parsed<ExprNode>),
    /// It continues one: `+`, `.field`, `(arguments)`.
    Infix(
        Precedence,
        fn(&mut Parser<'_>, ExprNode) -> Parsed<ExprNode>,
    ),
    Statement(fn(&mut Parser<'_>) -> Parsed<StmtNode>),
    Declaration(fn(&mut Parser<'_>) -> Parsed<DeclNode>),
    Type(fn(&mut Parser<'_>) -> Parsed<TypeNode>),
}

/// Where in the grammar a rule starts; each has its own rules.
#[derive(Clone, Copy)]
enum Position {
    Prefix,
    Infix,
    Statement,
    Declaration,
    Type,
}

const POSITIONS: usize = Position::Type as usize + 1;

impl Read {
    const fn position(&self) -> Position {
        match self {
            Read::Prefix(_) => Position::Prefix,
            Read::Infix(..) => Position::Infix,
            Read::Statement(_) => Position::Statement,
            Read::Declaration(_) => Position::Declaration,
            Read::Type(_) => Position::Type,
        }
    }
}

/// What a rule starts with: a token, or a keyword its feature lists.
#[derive(Clone, Copy)]
pub enum Start {
    Token(Token),
    Keyword(&'static str),
}

/// One rule: what starts it and, when two features start with the same
/// token, the token after it (`&[]T` is slices, `&T` references).
#[derive(Clone, Copy)]
pub struct Rule {
    pub start: Start,
    pub then: Option<Token>,
    pub read: Read,
}

/// Every feature's keywords, and the rule for each position, start and
/// next token. A start is a token, or `TOKENS` plus a keyword's index.
/// Index `TOKENS` of the last dimension is a rule with no `then`, for any
/// next token.
pub struct RuleTable {
    keywords: [&'static str; KEYWORDS],
    keyword_count: usize,
    reads: [[[Option<Read>; TOKENS + 1]; TOKENS + KEYWORDS]; POSITIONS],
}

const fn same(first: &str, second: &str) -> bool {
    let (first, second) = (first.as_bytes(), second.as_bytes());
    if first.len() != second.len() {
        return false;
    }
    let mut index = 0;
    while index < first.len() {
        if first[index] != second[index] {
            return false;
        }
        index += 1;
    }
    true
}

impl RuleTable {
    /// The table of every feature's keywords and rules. Run in a `static`,
    /// so a clash is a build error, never a rule silently replaced.
    pub const fn build(features: &[&Feature]) -> RuleTable {
        let mut table = RuleTable {
            keywords: [""; KEYWORDS],
            keyword_count: 0,
            reads: [[[None; TOKENS + 1]; TOKENS + KEYWORDS]; POSITIONS],
        };
        let mut feature = 0;
        while feature < features.len() {
            let mut index = 0;
            while index < features[feature].keywords.len() {
                let word = features[feature].keywords[index];
                if table.keyword(word).is_some() {
                    panic!("two features claim the same keyword");
                }
                if table.keyword_count == KEYWORDS {
                    panic!("more keywords than the table holds");
                }
                table.keywords[table.keyword_count] = word;
                table.keyword_count += 1;
                index += 1;
            }
            feature += 1;
        }
        let mut feature = 0;
        while feature < features.len() {
            let mut index = 0;
            while index < features[feature].rules.len() {
                let rule = features[feature].rules[index];
                let start = match rule.start {
                    Start::Token(token) => token as usize,
                    Start::Keyword(word) => match table.keyword(word) {
                        Some(keyword) => TOKENS + keyword as usize,
                        None => panic!("a rule starts with a word no feature lists as a keyword"),
                    },
                };
                let then = match rule.then {
                    Some(token) => token as usize,
                    None => TOKENS,
                };
                let slot = &mut table.reads[rule.read.position() as usize][start][then];
                if slot.is_some() {
                    panic!("two features claim the same tokens in the same position");
                }
                *slot = Some(rule.read);
                index += 1;
            }
            feature += 1;
        }
        table
    }

    /// The index of `word` among the keywords; also run by the lexer.
    const fn keyword(&self, word: &str) -> Option<u8> {
        let mut index = 0;
        while index < self.keyword_count {
            if same(self.keywords[index], word) {
                return Some(index as u8);
            }
            index += 1;
        }
        None
    }

    fn find(&self, position: Position, start: Lexeme, next: Token) -> Option<Read> {
        let start = match start.token {
            Token::Keyword => TOKENS + start.keyword as usize,
            token => token as usize,
        };
        let reads = &self.reads[position as usize][start];
        reads[next as usize].or(reads[TOKENS])
    }
}

pub struct Parser<'a> {
    pub source: &'a str,
    lexemes: Vec<Lexeme>,
    position: usize,
    pub tree: Tree,
    rules: &'static RuleTable,
}

impl<'a> Parser<'a> {
    pub fn new(source: &'a str, rules: &'static RuleTable) -> Parsed<Parser<'a>> {
        Ok(Parser {
            source,
            lexemes: scan(source, rules)?,
            position: 0,
            tree: Tree::default(),
            rules,
        })
    }

    pub fn peek(&self) -> Token {
        self.lexemes[self.position].token
    }

    fn current(&self) -> Lexeme {
        self.lexemes[self.position]
    }

    pub fn at_keyword(&self, word: &str) -> bool {
        let lexeme = self.current();
        lexeme.token == Token::Keyword && self.rules.keywords[lexeme.keyword as usize] == word
    }

    pub fn eat_keyword(&mut self, word: &str) -> bool {
        let found = self.at_keyword(word);
        if found {
            self.advance();
        }
        found
    }

    pub fn expect_keyword(&mut self, word: &str) -> Parsed<Span> {
        if self.at_keyword(word) {
            return Ok(self.advance());
        }
        Err(self.error(&format!("expected `{word}`")))
    }

    fn after_next(&self) -> Token {
        self.lexemes
            .get(self.position + 1)
            .map_or(Token::End, |lexeme| lexeme.token)
    }

    pub fn span(&self) -> Span {
        self.lexemes[self.position].span
    }

    /// The span from `start` to the end of the last token read.
    pub fn since(&self, start: Span) -> Span {
        let end = self.lexemes[self.position.saturating_sub(1)].span.end;
        Span {
            start: start.start,
            end: end.max(start.end),
        }
    }

    pub fn advance(&mut self) -> Span {
        let span = self.span();
        if self.peek() != Token::End {
            self.position += 1;
        }
        span
    }

    pub fn eat(&mut self, token: Token) -> bool {
        let found = self.peek() == token;
        if found {
            self.advance();
        }
        found
    }

    pub fn expect(&mut self, token: Token, what: &str) -> Parsed<Span> {
        if self.peek() == token {
            return Ok(self.advance());
        }
        Err(self.error(&format!("expected {what}")))
    }

    pub fn error(&self, message: &str) -> ParseError {
        ParseError {
            span: self.span(),
            message: message.to_string(),
        }
    }

    pub fn name(&mut self) -> Parsed<NodeId> {
        let span = self.expect(Token::Name, "a name")?;
        Ok(self.tree.push(Kind::Name, span, &[]))
    }

    /// A name that must be `word`, such as `self`, which is not a keyword.
    pub fn exact_name(&mut self, word: &str) -> Parsed<NodeId> {
        let span = self.span();
        let text = &self.source[span.start as usize..span.end as usize];
        if self.peek() != Token::Name || text != word {
            return Err(self.error(&format!("expected `{word}`")));
        }
        self.name()
    }

    pub fn missing(&mut self) -> NodeId {
        let span = self.span();
        self.tree.push(Kind::Missing, span, &[])
    }

    /// Items separated by commas up to `close`, which it reads too.
    pub fn list(
        &mut self,
        close: Token,
        mut item: impl FnMut(&mut Self) -> Parsed<()>,
    ) -> Parsed<()> {
        while !self.eat(close) {
            item(self)?;
            if !self.eat(Token::Comma) {
                self.expect(close, "`,` or the end of the list")?;
                break;
            }
        }
        Ok(())
    }

    /// An expression whose infix operators bind at least `at_least`,
    /// whichever features they belong to.
    pub fn expression(&mut self, at_least: Precedence) -> Parsed<ExprNode> {
        let mut left = match self.peek() {
            Token::Name => {
                let span = self.advance();
                ExprNode(self.tree.push(Kind::Expression(check_name), span, &[]))
            }
            _ => match self
                .rules
                .find(Position::Prefix, self.current(), self.after_next())
            {
                Some(Read::Prefix(read)) => read(self)?,
                _ => return Err(self.error("expected an expression")),
            },
        };
        while let Some(Read::Infix(precedence, read)) =
            self.rules
                .find(Position::Infix, self.current(), self.after_next())
        {
            if precedence < at_least {
                break;
            }
            left = read(self, left)?;
        }
        Ok(left)
    }

    pub fn statement(&mut self) -> Parsed<StmtNode> {
        if let Some(Read::Statement(read)) =
            self.rules
                .find(Position::Statement, self.current(), self.after_next())
        {
            return read(self);
        }
        let start = self.span();
        let expression = self.expression(Precedence(0))?;
        self.expect(Token::Semicolon, "`;`")?;
        let span = self.since(start);
        Ok(StmtNode(self.tree.push(
            Kind::Statement(check_expression_statement),
            span,
            &[expression.0],
        )))
    }

    pub fn block(&mut self) -> Parsed<BlockNode> {
        let start = self.expect(Token::LeftBrace, "`{`")?;
        let mut statements = Vec::new();
        while !self.eat(Token::RightBrace) {
            if self.peek() == Token::End {
                return Err(self.error("expected `}`"));
            }
            statements.push(self.statement()?.0);
        }
        let span = self.since(start);
        Ok(BlockNode(self.tree.push(Kind::Block, span, &statements)))
    }

    pub fn ty(&mut self) -> Parsed<TypeNode> {
        if self.peek() == Token::Name {
            let span = self.advance();
            return Ok(TypeNode(self.tree.push(Kind::Type(named_type), span, &[])));
        }
        match self
            .rules
            .find(Position::Type, self.current(), self.after_next())
        {
            Some(Read::Type(read)) => read(self),
            _ => Err(self.error("expected a type")),
        }
    }

    pub fn program(&mut self) -> Parsed<Vec<DeclNode>> {
        let mut declarations = Vec::new();
        while self.peek() != Token::End {
            match self
                .rules
                .find(Position::Declaration, self.current(), self.after_next())
            {
                Some(Read::Declaration(read)) => declarations.push(read(self)?),
                _ => return Err(self.error("expected a declaration")),
            }
        }
        Ok(declarations)
    }
}
