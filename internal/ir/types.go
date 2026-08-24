package ir

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
)

// TypeID identifies one runtime type in a compilation's TypeTable. IDs never
// cross compilation contexts; semantic types are interned when HIR is formed.
type TypeID uint32

const InvalidType TypeID = 0

type TypeKind uint8

const (
	TypeVoid TypeKind = iota
	TypeInteger
	TypeFloat
	TypeBool
	TypeByte
	TypeChar
	TypeCStr
	TypeString
	TypeAllocator
	TypeRawPtr
	TypeOwnedPtr
	TypeReference
	TypeVariant
	TypeArray
	TypeSlice
	TypeStruct
	TypeInterface
	TypeFunction
	TypeNamed
)

type TypeField struct {
	Name string
	Type TypeID
}

type TypeMethod struct {
	Name   string
	Params []TypeField
	Return TypeID
}

type VariantFamily uint8

const (
	VariantFamilyInvalid VariantFamily = iota
	VariantFamilyOptional
	VariantFamilyNamed
)

const (
	OptionalAbsentCase = iota
	OptionalPresentCase
)

type VariantCase struct {
	Name    string
	Payload TypeID
}

// Type is a backend-independent runtime descriptor. Source-only aliases are
// resolved before interning, so every child directly describes its ABI shape.
type Type struct {
	Kind     TypeKind
	Signed   bool
	Bits     int
	Mutable  bool
	Length   string
	Elem     TypeID
	Fields   []TypeField
	Methods  []TypeMethod
	Params   []TypeID
	Return   TypeID
	Name     string
	Family   VariantFamily
	Identity string
	Cases    []VariantCase
}

// OptionalVariant owns optional's fixed case order. Flow facts, lowering, and
// backends use these case indexes instead of rebuilding optional conventions.
func OptionalVariant(payload TypeID) Type {
	return Type{
		Kind: TypeVariant, Family: VariantFamilyOptional,
		Cases: []VariantCase{{Name: "Absent"}, {Name: "Present", Payload: payload}},
	}
}

func (t Type) VariantCase(index int) (VariantCase, bool) {
	if t.Kind != TypeVariant || index < 0 || index >= len(t.Cases) {
		return VariantCase{}, false
	}
	return t.Cases[index], true
}

func (t Type) OptionalPayload() (TypeID, bool) {
	if t.Kind != TypeVariant || t.Family != VariantFamilyOptional || len(t.Cases) != 2 {
		return InvalidType, false
	}
	present := t.Cases[OptionalPresentCase]
	if t.Cases[OptionalAbsentCase].Payload != InvalidType || present.Payload == InvalidType {
		return InvalidType, false
	}
	return present.Payload, true
}

// TypeTable is owned by one CompilerContext. It is canonical storage for IR
// types, their diagnostics text, and ABI identity.
type TypeTable struct {
	mu        sync.RWMutex
	types     []Type
	ids       map[string]TypeID
	abiKeys   map[string]TypeID
	indexType TypeID
}

func NewTypeTable() *TypeTable {
	return &TypeTable{
		types:   []Type{{Name: "<invalid>"}},
		ids:     make(map[string]TypeID),
		abiKeys: make(map[string]TypeID),
	}
}

func (t *TypeTable) Intern(typ Type) TypeID {
	if t == nil {
		panic("interning IR type without a type table")
	}
	key := t.key(typ)
	t.mu.Lock()
	defer t.mu.Unlock()
	if id, ok := t.ids[key]; ok {
		return id
	}
	id := TypeID(len(t.types))
	t.types = append(t.types, cloneType(typ))
	t.ids[key] = id
	t.abiKeys[t.abiKeyLocked(id)] = id
	return id
}

// LookupABIKey bridges finalized semantic identity into an already-interned IR
// type. It never parses source text or creates a type; HIR lowering remains the
// only semantic-to-IR type construction boundary.
func (t *TypeTable) LookupABIKey(key string) (TypeID, bool) {
	if t == nil {
		return InvalidType, false
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	id, ok := t.abiKeys[key]
	return id, ok
}

func (t *TypeTable) SetIndexType(id TypeID) {
	if t == nil {
		panic("setting invalid IR index type")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if id == InvalidType || int(id) >= len(t.types) {
		panic("setting invalid IR index type")
	}
	t.indexType = id
}

func (t *TypeTable) IndexType() TypeID {
	if t == nil {
		return InvalidType
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.indexType
}

func (t *TypeTable) Type(id TypeID) (Type, bool) {
	if t == nil {
		return Type{}, false
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	if id == InvalidType || int(id) >= len(t.types) {
		return Type{}, false
	}
	return t.types[id], true
}

func (t *TypeTable) Text(id TypeID) string {
	if t == nil {
		return "<invalid>"
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.textLocked(id)
}

// textLocked keeps recursive formatting under one read or write lock. Calling
// public Type or Text here would deadlock when Intern holds the write lock.
func (t *TypeTable) textLocked(id TypeID) string {
	if id == InvalidType || int(id) >= len(t.types) {
		return "<invalid>"
	}
	typ := t.types[id]
	switch typ.Kind {
	case TypeVoid:
		return "void"
	case TypeInteger:
		if typ.Signed {
			return "i" + strconv.Itoa(typ.Bits)
		}
		return "u" + strconv.Itoa(typ.Bits)
	case TypeFloat:
		return "f" + strconv.Itoa(typ.Bits)
	case TypeBool:
		return "bool"
	case TypeByte:
		return "byte"
	case TypeChar:
		return "char"
	case TypeCStr:
		return "cstr"
	case TypeString:
		return "str"
	case TypeAllocator:
		return "Allocator"
	case TypeRawPtr:
		return "rawptr"
	case TypeOwnedPtr:
		return "*" + t.textLocked(typ.Elem)
	case TypeReference:
		prefix := "&"
		if typ.Mutable {
			prefix = "&mut "
		}
		return prefix + t.textLocked(typ.Elem)
	case TypeVariant:
		if payload, optional := typ.OptionalPayload(); optional {
			return "?" + t.textLocked(payload)
		}
		if typ.Family == VariantFamilyNamed && typ.Name != "" {
			return typ.Name
		}
		return "<invalid>"
	case TypeArray:
		if typ.Length == "" {
			return "[]" + t.textLocked(typ.Elem)
		}
		return "[" + typ.Length + "]" + t.textLocked(typ.Elem)
	case TypeSlice:
		return "[..]" + t.textLocked(typ.Elem)
	case TypeStruct:
		return "struct{" + t.fieldsTextLocked(typ.Fields, ';') + "}"
	case TypeInterface:
		methods := make([]string, 0, len(typ.Methods))
		for _, method := range typ.Methods {
			params := make([]string, 0, len(method.Params))
			for _, param := range method.Params {
				params = append(params, param.Name+": "+t.textLocked(param.Type))
			}
			text := method.Name + "(" + strings.Join(params, ", ") + ")"
			if method.Return != InvalidType && t.textLocked(method.Return) != "void" {
				text += " -> " + t.textLocked(method.Return)
			}
			methods = append(methods, text)
		}
		return "iface{" + strings.Join(methods, "; ") + "}"
	case TypeFunction:
		params := make([]string, 0, len(typ.Params))
		for _, param := range typ.Params {
			params = append(params, t.textLocked(param))
		}
		text := "fn(" + strings.Join(params, ", ") + ")"
		if typ.Return != InvalidType && t.textLocked(typ.Return) != "void" {
			text += " -> " + t.textLocked(typ.Return)
		}
		return text
	case TypeNamed:
		return typ.Name
	default:
		return "<invalid>"
	}
}

// ABIKey is stable only inside the compiler ABI model. Backends may use it for
// symbol identity, never raw TypeID values.
func (t *TypeTable) ABIKey(id TypeID) string {
	if t == nil {
		return "<invalid>"
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	if id == InvalidType || int(id) >= len(t.types) {
		return "<invalid>"
	}
	return t.abiKeyLocked(id)
}

func (t *TypeTable) abiKeyLocked(id TypeID) string {
	typ := t.types[id]
	if typ.Kind == TypeVariant && typ.Family == VariantFamilyNamed {
		return "variant:" + typ.Identity
	}
	return t.textLocked(id)
}

func (t *TypeTable) key(typ Type) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d|%t|%d|%t|%q|%d|%d|%q|%d|%q", typ.Kind, typ.Signed, typ.Bits, typ.Mutable, typ.Length, typ.Elem, typ.Return, typ.Name, typ.Family, typ.Identity)
	for _, variant := range typ.Cases {
		fmt.Fprintf(&b, "|v:%q:%d", variant.Name, variant.Payload)
	}
	for _, field := range typ.Fields {
		fmt.Fprintf(&b, "|f:%q:%d", field.Name, field.Type)
	}
	for _, method := range typ.Methods {
		fmt.Fprintf(&b, "|m:%q:%d", method.Name, method.Return)
		for _, param := range method.Params {
			fmt.Fprintf(&b, ":%q:%d", param.Name, param.Type)
		}
	}
	for _, param := range typ.Params {
		fmt.Fprintf(&b, "|p:%d", param)
	}
	return b.String()
}

func (t *TypeTable) fieldsTextLocked(fields []TypeField, separator byte) string {
	parts := make([]string, 0, len(fields))
	for _, field := range fields {
		parts = append(parts, field.Name+": "+t.textLocked(field.Type))
	}
	return strings.Join(parts, string(separator)+" ")
}

func cloneType(typ Type) Type {
	typ.Fields = append([]TypeField(nil), typ.Fields...)
	typ.Params = append([]TypeID(nil), typ.Params...)
	typ.Cases = append([]VariantCase(nil), typ.Cases...)
	if len(typ.Methods) == 0 {
		return typ
	}
	typ.Methods = append([]TypeMethod(nil), typ.Methods...)
	for i := range typ.Methods {
		typ.Methods[i].Params = append([]TypeField(nil), typ.Methods[i].Params...)
	}
	return typ
}
