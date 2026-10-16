package ast

// The children of each kind of node, in source order. The ast:node directive
// names the kinds a struct describes. A field tagged ast:"A" or ast:"B" is kept
// in that slot of the node; every other field is kept in Extra, in field order,
// with the node's B slot holding where they start. A List field is a list.
// A child that is absent is None.
//
// gen reads these structs and writes, in nodes_gen.go, a reader and a packer
// for each and Tree.Children; after changing one, run go generate.

//ast:node KindModule
type Module struct {
	Imports      List `ast:"A"`
	Declarations List `ast:"B"`
}

//ast:node KindImportDecl
type ImportDecl struct {
	Path  Index `ast:"A"` // a StringLiteral
	Alias Index `ast:"B"` // a DeclaredName
}

//ast:node KindFunctionDecl
type FunctionDecl struct {
	Metadata       Index
	Receiver       Index // a Parameter
	Name           Index `ast:"A"`
	TypeParameters List  // DeclaredNames
	Parameters     List
	Result         Index
	Origins        List // NameReferences: what a returned reference may point into
	Body           Index
}

//ast:node KindConstDecl
type ConstDecl struct {
	Metadata Index
	Name     Index `ast:"A"`
	Type     Index
	Value    Index
}

//ast:node KindStructDecl KindEnumDecl KindInterfaceDecl KindTypeAliasDecl
type TypeDecl struct {
	Metadata       Index
	Name           Index `ast:"A"`
	TypeParameters List  // DeclaredNames
	Type           Index // a StructType, EnumType or InterfaceType, or any type for an alias
}

//ast:node KindMetadata
type Metadata struct {
	DocComment Index `ast:"A"`
	Attributes List  `ast:"B"`
}

//ast:node KindAttribute
type Attribute struct {
	Name      Index `ast:"A"` // a NameReference
	Arguments List  `ast:"B"`
}

//ast:node KindParameter
type Parameter struct {
	Name    Index `ast:"A"`
	Type    Index
	Default Index
}

//ast:node KindPath
type Path struct {
	Segments List `ast:"A"` // TypeNames and GenericTypes
}

//ast:node KindGenericType
type GenericType struct {
	Name      Index `ast:"A"`
	Arguments List  `ast:"B"`
}

//ast:node KindReferenceType KindOwnedPointerType
type PointerType struct {
	Target Index `ast:"A"`
}

//ast:node KindOptionalType
type OptionalType struct {
	Inner Index `ast:"A"`
}

//ast:node KindFixedArrayType
type FixedArrayType struct {
	Length  Index `ast:"A"` // a NumberLiteral
	Element Index `ast:"B"`
}

//ast:node KindDynamicArrayType KindSliceType
type ElementType struct {
	Element Index `ast:"A"`
}

//ast:node KindFunctionType
type FunctionType struct {
	Parameters List `ast:"A"`
	Result     Index
	Origins    List
}

//ast:node KindStructType
type StructType struct {
	Fields List `ast:"A"`
}

//ast:node KindField
type Field struct {
	Name Index `ast:"A"`
	Type Index `ast:"B"`
}

//ast:node KindEnumType
type EnumType struct {
	Variants List `ast:"A"`
}

//ast:node KindVariant
type Variant struct {
	Name    Index `ast:"A"`
	Payload Index `ast:"B"`
}

//ast:node KindInterfaceType
type InterfaceType struct {
	Methods List `ast:"A"` // FunctionDecls without bodies
}

//ast:node KindBlock
type Block struct {
	Statements List `ast:"A"`
}

//ast:node KindLetStatement
type LetStatement struct {
	Name  Index `ast:"A"`
	Type  Index
	Value Index
}

//ast:node KindReturnStatement
type ReturnStatement struct {
	Value Index `ast:"A"`
}

//ast:node KindExpressionStatement
type ExpressionStatement struct {
	Expression Index `ast:"A"`
}

//ast:node KindBinaryExpression
type BinaryExpression struct {
	Left  Index `ast:"A"`
	Right Index `ast:"B"`
}

//ast:node KindCallExpression
type CallExpression struct {
	Callee    Index `ast:"A"`
	Arguments List  `ast:"B"`
}

//ast:node KindParentheses
type Parentheses struct {
	Inner Index `ast:"A"`
}

//ast:node KindUnaryExpression KindReferenceExpression KindRawAddressExpression KindFreeExpression KindPrintExpression
type Operand struct {
	Value Index `ast:"A"`
}

//ast:node KindSelectorExpression
type SelectorExpression struct {
	Value Index `ast:"A"`
	Field Index `ast:"B"` // a FieldName
}

//ast:node KindIndexExpression
type IndexExpression struct {
	Value Index `ast:"A"`
	Index Index `ast:"B"` // an expression or a Range
}

//ast:node KindRange
type Range struct {
	Start Index `ast:"A"`
	End   Index `ast:"B"`
}

//ast:node KindStructLiteral
type StructLiteral struct {
	Type   Index `ast:"A"` // None in `.{ ... }`
	Fields List  `ast:"B"` // FieldValues
}

//ast:node KindFieldValue
type FieldValue struct {
	Name  Index `ast:"A"` // a FieldName
	Value Index `ast:"B"`
}

//ast:node KindVariantLiteral
type VariantLiteral struct {
	Case    Index `ast:"A"` // a Path
	Payload Index `ast:"B"`
}

//ast:node KindArrayLiteral
type ArrayLiteral struct {
	Type   Index `ast:"A"` // a FixedArrayType, DynamicArrayType or SliceType
	Values List  `ast:"B"`
}

//ast:node KindCastExpression
type CastExpression struct {
	Value Index `ast:"A"`
	Type  Index `ast:"B"`
}

//ast:node KindIsExpression
type IsExpression struct {
	Value Index `ast:"A"`
	Case  Index `ast:"B"` // a Path
}

//ast:node KindOrElseExpression
type OrElseExpression struct {
	Value    Index `ast:"A"`
	Fallback Index `ast:"B"` // an expression or a Block
}

//ast:node KindPipeExpression
type PipeExpression struct {
	Value Index `ast:"A"`
	Call  Index `ast:"B"` // a CallExpression
}

//ast:node KindAssignStatement
type AssignStatement struct {
	Target Index `ast:"A"`
	Value  Index `ast:"B"`
}

//ast:node KindDestructureStatement
type DestructureStatement struct {
	Pattern Index `ast:"A"` // a StructPattern
	Value   Index `ast:"B"`
}

//ast:node KindIfStatement
type IfStatement struct {
	Condition Index `ast:"A"`
	Then      Index // a Block
	Else      Index // a Block or an IfStatement
}

//ast:node KindLoopStatement
type LoopStatement struct {
	Condition Index `ast:"A"`
	Body      Index `ast:"B"`
}

//ast:node KindForInStatement
type ForInStatement struct {
	Index    Index // a DeclaredName
	Value    Index `ast:"A"` // a DeclaredName
	Iterable Index // an expression or a Range
	Body     Index
}

//ast:node KindMatchStatement
type MatchStatement struct {
	Subject Index `ast:"A"`
	Arms    List  `ast:"B"` // MatchArms
}

//ast:node KindMatchArm
type MatchArm struct {
	Pattern Index `ast:"A"` // a WildcardPattern or a CasePattern
	Body    Index `ast:"B"`
}

//ast:node KindCasePattern
type CasePattern struct {
	Case  Index `ast:"A"` // a Path
	Inner Index `ast:"B"` // the pattern after `with`: a NonePattern, a DeclaredName (a binding), a StructPattern or a CasePattern
}

//ast:node KindStructPattern
type StructPattern struct {
	Fields List `ast:"A"` // FieldPatterns, then perhaps a RestPattern
}

//ast:node KindFieldPattern
type FieldPattern struct {
	Field   Index `ast:"A"` // a FieldName
	Binding Index `ast:"B"` // the DeclaredName after `as`; None binds the field's own name
}
