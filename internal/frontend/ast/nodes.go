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
