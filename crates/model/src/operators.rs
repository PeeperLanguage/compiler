//! Operator kinds, shared by the syntax tree, THIR, MIR and constant folding.

/// Declares an operator enum once, each operator with its spelling and, as
/// its doc comment, what it does (as the `eval` crate defines it): the enum,
/// `symbol` and `doc`.
macro_rules! operators {
    (
        $(#[doc = $enum_doc:literal])*
        pub enum $enum:ident {
            $(
                #[doc = $doc:literal]
                $(#[doc = $doc_more:literal])*
                $op:ident = $symbol:literal,
            )*
        }
    ) => {
        $(#[doc = $enum_doc])*
        #[derive(Clone, Copy, Debug, PartialEq, Eq)]
        pub enum $enum {
            $(
                #[doc = $doc]
                $(#[doc = $doc_more])*
                $op,
            )*
        }

        impl $enum {
            /// How the operator is written, for messages and dumps.
            pub const fn symbol(self) -> &'static str {
                match self {
                    $($enum::$op => $symbol,)*
                }
            }

            /// What the operator does, as Markdown. Each line keeps the
            /// space after its `///`, which Markdown drops.
            pub const fn doc(self) -> &'static str {
                match self {
                    $($enum::$op => concat!($doc $(, "\n", $doc_more)*),)*
                }
            }
        }
    };
}

operators! {
    /// A binary operator, named after what it does.
    pub enum BinaryOp {
        /// Adds.
        ///
        /// Integers wrap to the type's width; floats round to their own precision (IEEE 754).
        ///
        /// ```peeper
        /// let sum = 2 + 3;           // 5
        /// let wrapped = 127i8 + 1i8; // -128
        /// ```
        Add = "+",
        /// Subtracts.
        ///
        /// Integers wrap to the type's width; floats round to their own precision (IEEE 754).
        ///
        /// ```peeper
        /// let difference = 5 - 8;  // -3
        /// let wrapped = 0u8 - 1u8; // 255
        /// ```
        Subtract = "-",
        /// Multiplies.
        ///
        /// Integers wrap to the type's width; floats round to their own precision (IEEE 754).
        ///
        /// ```peeper
        /// let area = 4 * 5;          // 20
        /// let wrapped = 200u8 * 2u8; // 144
        /// ```
        Multiply = "*",
        /// Divides.
        ///
        /// Integers round toward zero, and dividing by zero stops the program; the lowest signed value divided by `-1` wraps to itself. Floats follow IEEE 754: dividing by zero gives an infinity, or NaN for `0.0 / 0.0`.
        ///
        /// ```peeper
        /// let whole = 7 / 2;    // 3
        /// let toward = -7 / 2;  // -3
        /// let half = 7.0 / 2.0; // 3.5
        /// ```
        Divide = "/",
        /// The remainder of the division.
        ///
        /// It has the left operand's sign, as the division rounds toward zero. An integer remainder by zero stops the program; a float one is NaN.
        ///
        /// ```peeper
        /// let left = 7 % 3;     // 1
        /// let signed = -7 % 3;  // -1
        /// let part = 7.5 % 2.0; // 1.5
        /// ```
        Remainder = "%",
        /// Shifts the bits left by the count, bringing in zeros.
        ///
        /// A count below 0 or not below the width stops the program.
        ///
        /// ```peeper
        /// let sixteen = 1 << 4; // 16
        /// ```
        ShiftLeft = "<<",
        /// Shifts the bits right by the count.
        ///
        /// A signed integer brings in its sign bit, an unsigned one zeros. A count below 0 or not below the width stops the program.
        ///
        /// ```peeper
        /// let quarter = -16 >> 2; // -4
        /// let high = 240u8 >> 4;  // 15
        /// ```
        ShiftRight = ">>",
        /// Keeps each bit set in both operands.
        ///
        /// ```peeper
        /// let both = 0b1100 & 0b1010; // 0b1000
        /// ```
        BitAnd = "&",
        /// Keeps each bit set in either operand.
        ///
        /// ```peeper
        /// let either = 0b1100 | 0b1010; // 0b1110
        /// ```
        BitOr = "|",
        /// Keeps each bit set in exactly one operand.
        ///
        /// ```peeper
        /// let one = 0b1100 ^ 0b1010; // 0b0110
        /// ```
        BitXor = "^",
        /// Whether the operands are equal.
        ///
        /// On floats, false when either is NaN, and `0.0 == -0.0`.
        ///
        /// ```peeper
        /// let same = 2 + 2 == 4;  // true
        /// let nan = 0.0 / 0.0;
        /// let never = nan == nan; // false
        /// ```
        EqualTo = "==",
        /// Whether the operands differ.
        ///
        /// On floats, true when either is NaN.
        ///
        /// ```peeper
        /// let differs = 3 != 4; // true
        /// ```
        NotEqualTo = "!=",
        /// Whether the left operand is less than the right.
        ///
        /// On floats, false when either is NaN.
        ///
        /// ```peeper
        /// let smaller = 3 < 4; // true
        /// ```
        LessThan = "<",
        /// Whether the left operand is less than or equal to the right.
        ///
        /// On floats, false when either is NaN.
        ///
        /// ```peeper
        /// let within = 4 <= 4; // true
        /// ```
        LessOrEqual = "<=",
        /// Whether the left operand is greater than the right.
        ///
        /// On floats, false when either is NaN.
        ///
        /// ```peeper
        /// let larger = 5 > 4; // true
        /// ```
        GreaterThan = ">",
        /// Whether the left operand is greater than or equal to the right.
        ///
        /// On floats, false when either is NaN.
        ///
        /// ```peeper
        /// let enough = 4 >= 5; // false
        /// ```
        GreaterOrEqual = ">=",
        /// Whether both operands are true.
        ///
        /// The right one runs only when the left is true.
        ///
        /// ```peeper
        /// // never divides by zero
        /// let large = count > 0 && 10 / count > 1;
        /// ```
        LogicalAnd = "&&",
        /// Whether either operand is true.
        ///
        /// The right one runs only when the left is false.
        ///
        /// ```peeper
        /// // never divides by zero
        /// let small = count == 0 || 10 / count > 1;
        /// ```
        LogicalOr = "||",
    }
}

impl BinaryOp {
    /// How tightly the operator binds.
    pub const fn precedence(self) -> Precedence {
        match self {
            BinaryOp::Add | BinaryOp::Subtract => Precedence::Sum,
            BinaryOp::Multiply | BinaryOp::Divide | BinaryOp::Remainder => Precedence::Product,
            BinaryOp::ShiftLeft | BinaryOp::ShiftRight => Precedence::Shift,
            BinaryOp::BitAnd => Precedence::BitAnd,
            BinaryOp::BitOr => Precedence::BitOr,
            BinaryOp::BitXor => Precedence::BitXor,
            BinaryOp::EqualTo | BinaryOp::NotEqualTo => Precedence::Equality,
            BinaryOp::LessThan
            | BinaryOp::LessOrEqual
            | BinaryOp::GreaterThan
            | BinaryOp::GreaterOrEqual => Precedence::Compare,
            BinaryOp::LogicalAnd => Precedence::LogicalAnd,
            BinaryOp::LogicalOr => Precedence::LogicalOr,
        }
    }
}

/// How tightly an operator binds, from loosest to tightest.
#[derive(Clone, Copy, PartialEq, Eq, PartialOrd, Ord)]
pub enum Precedence {
    Lowest,
    Range,
    OrElse,
    Pipe,
    LogicalOr,
    LogicalAnd,
    BitOr,
    BitXor,
    BitAnd,
    Equality,
    Compare,
    Shift,
    Sum,
    Product,
    Cast,
    Prefix,
    Call,
}

operators! {
    /// A unary operator. There is no `+x`: it would change nothing, so the
    /// parser reports it.
    pub enum UnaryOp {
        /// Negates.
        ///
        /// Integers wrap: the lowest signed value stays itself, and an unsigned value other than 0 wraps around. A float flips its sign, of zero and NaN too.
        ///
        /// ```peeper
        /// let n = 5;
        /// let below = -n; // -5
        /// ```
        Negate = "-",
        /// Whether the operand is false.
        ///
        /// ```peeper
        /// let empty = !(n > 0);
        /// ```
        LogicalNot = "!",
        /// Flips each bit.
        ///
        /// ```peeper
        /// let flipped = ~240u8; // 15
        /// ```
        BitNot = "~",
    }
}
