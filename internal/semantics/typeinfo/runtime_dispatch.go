package typeinfo

// RuntimeTypeVisitor is the operation-specific dispatch contract for runtime IR
// type lowering. Implementations own lowering policy; semantic types only make
// the concrete kind exhaustive without depending on an IR package.
type RuntimeTypeVisitor interface {
	VisitInvalid(*InvalidType)
	VisitUnknown(*UnknownType)
	VisitInteger(*IntegerType)
	VisitByte(*ByteType)
	VisitChar(*CharType)
	VisitFloat(*FloatType)
	VisitBool(*BoolType)
	VisitCStr(*CStrType)
	VisitString(*StringType)
	VisitNone(*NoneType)
	VisitAllocator(*AllocatorType)
	VisitNamed(*NamedType)
	VisitTypeParameter(*TypeParameterType)
	VisitDefined(*DefinedType)
	VisitOwnedPointer(*OwnedPtrType)
	VisitRawPointer(*RawPtrType)
	VisitReference(*RefType)
	VisitOptional(*OptionalType)
	VisitArray(*ArrayType)
	VisitFunction(*FuncType)
	VisitStruct(*StructType)
	VisitInterface(*InterfaceType)
	VisitEnum(*EnumType)
}

func (t *InvalidType) VisitRuntimeType(v RuntimeTypeVisitor)       { v.VisitInvalid(t) }
func (t *UnknownType) VisitRuntimeType(v RuntimeTypeVisitor)       { v.VisitUnknown(t) }
func (t *IntegerType) VisitRuntimeType(v RuntimeTypeVisitor)       { v.VisitInteger(t) }
func (t *ByteType) VisitRuntimeType(v RuntimeTypeVisitor)          { v.VisitByte(t) }
func (t *CharType) VisitRuntimeType(v RuntimeTypeVisitor)          { v.VisitChar(t) }
func (t *FloatType) VisitRuntimeType(v RuntimeTypeVisitor)         { v.VisitFloat(t) }
func (t *BoolType) VisitRuntimeType(v RuntimeTypeVisitor)          { v.VisitBool(t) }
func (t *CStrType) VisitRuntimeType(v RuntimeTypeVisitor)          { v.VisitCStr(t) }
func (t *StringType) VisitRuntimeType(v RuntimeTypeVisitor)        { v.VisitString(t) }
func (t *NoneType) VisitRuntimeType(v RuntimeTypeVisitor)          { v.VisitNone(t) }
func (t *AllocatorType) VisitRuntimeType(v RuntimeTypeVisitor)     { v.VisitAllocator(t) }
func (t *NamedType) VisitRuntimeType(v RuntimeTypeVisitor)         { v.VisitNamed(t) }
func (t *TypeParameterType) VisitRuntimeType(v RuntimeTypeVisitor) { v.VisitTypeParameter(t) }
func (t *DefinedType) VisitRuntimeType(v RuntimeTypeVisitor)       { v.VisitDefined(t) }
func (t *OwnedPtrType) VisitRuntimeType(v RuntimeTypeVisitor)      { v.VisitOwnedPointer(t) }
func (t *RawPtrType) VisitRuntimeType(v RuntimeTypeVisitor)        { v.VisitRawPointer(t) }
func (t *RefType) VisitRuntimeType(v RuntimeTypeVisitor)           { v.VisitReference(t) }
func (t *OptionalType) VisitRuntimeType(v RuntimeTypeVisitor)      { v.VisitOptional(t) }
func (t *ArrayType) VisitRuntimeType(v RuntimeTypeVisitor)         { v.VisitArray(t) }
func (t *FuncType) VisitRuntimeType(v RuntimeTypeVisitor)          { v.VisitFunction(t) }
func (t *StructType) VisitRuntimeType(v RuntimeTypeVisitor)        { v.VisitStruct(t) }
func (t *InterfaceType) VisitRuntimeType(v RuntimeTypeVisitor)     { v.VisitInterface(t) }
func (t *EnumType) VisitRuntimeType(v RuntimeTypeVisitor)          { v.VisitEnum(t) }
