package ir

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"

	"compiler/internal/semantics/typeinfo"
)

// TypeID identifies one runtime type in a compilation's TypeTable. IDs never
// cross compilation contexts; semantic types are interned during runtime type lowering.
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
)

type TypeField struct {
	Name string
	Type TypeID
}

type MethodReceiver uint8

const (
	MethodReceiverInvalid MethodReceiver = iota
	MethodReceiverValue
	MethodReceiverShared
	MethodReceiverMutable
)

type TypeMethod struct {
	Name     string
	Receiver MethodReceiver
	Params   []TypeField
	Return   TypeID
	SlotType TypeID
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
	Kind      TypeKind
	IsSigned  bool
	Bits      int
	IsMutable bool
	Length    string
	Elem      TypeID
	Fields    []TypeField
	Methods   []TypeMethod
	Params    []TypeID
	Return    TypeID
	Name      string
	Family    VariantFamily
	Identity  string
	Cases     []VariantCase
	// DisplayName is the module-qualified name of a named composite, written
	// in compiler dumps. It derives from Identity, so it is not part of type
	// identity or descriptor equality.
	DisplayName string
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
	mu         sync.RWMutex
	types      []Type
	ids        map[string]TypeID
	isComplete []bool
	indexType  TypeID
	// lowered remembers each semantic type's ID: lowering one walks the whole
	// type, and every expression of that type asks again.
	lowered map[typeinfo.Type]TypeID
}

func NewTypeTable() *TypeTable {
	return &TypeTable{
		types:      []Type{{Name: "<invalid>"}},
		ids:        make(map[string]TypeID),
		isComplete: []bool{false},
		lowered:    make(map[typeinfo.Type]TypeID),
	}
}

// Lower returns the ID semantic was lowered to before, or runs lower and
// remembers a valid result. An invalid one is not kept, so its diagnostics are
// reported again where it is asked for again.
func (t *TypeTable) Lower(semantic typeinfo.Type, lower func() TypeID) TypeID {
	t.mu.RLock()
	id, found := t.lowered[semantic]
	t.mu.RUnlock()
	if found {
		return id
	}
	// lower interns through t, so it runs without the lock; two modules racing
	// on one type intern the same ID.
	if id = lower(); id != InvalidType {
		t.mu.Lock()
		t.lowered[semantic] = id
		t.mu.Unlock()
	}
	return id
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
	t.isComplete = append(t.isComplete, true)
	t.ids[key] = id
	return id
}

// ReserveNamed allocates stable identity before named composite children are
// interned. Recursive child paths reuse this TypeID until CompleteNamed fills
// the descriptor. displayName runs only when the type is new, so repeated
// lowering of a known type does not rebuild its name; nil keeps the identity.
func (t *TypeTable) ReserveNamed(shell Type, displayName func() string) (TypeID, error) {
	if t == nil {
		return InvalidType, fmt.Errorf("reserving named IR type without a type table")
	}
	key, ok := identifiedTypeKey(shell)
	if !ok || shell.Name == "" {
		return InvalidType, fmt.Errorf("reserving IR type without named composite identity")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if id, found := t.ids[key]; found {
		return id, nil
	}
	if displayName != nil {
		shell.DisplayName = displayName()
	}
	id := TypeID(len(t.types))
	t.types = append(t.types, cloneType(shell))
	t.isComplete = append(t.isComplete, false)
	t.ids[key] = id
	return id, nil
}

// CompleteNamed atomically publishes children for a reserved named composite.
// Repeating the same completion is valid; conflicting descriptors are rejected.
func (t *TypeTable) CompleteNamed(id TypeID, typ Type) error {
	if t == nil {
		return fmt.Errorf("completing named IR type without a type table")
	}
	key, ok := identifiedTypeKey(typ)
	if !ok || typ.Name == "" {
		return fmt.Errorf("completing IR type without named composite identity")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if id == InvalidType || int(id) >= len(t.types) {
		return fmt.Errorf("completing invalid named IR TypeID %d", id)
	}
	shellKey, shellOK := identifiedTypeKey(t.types[id])
	if !shellOK || shellKey != key {
		return fmt.Errorf("completing named IR TypeID %d with mismatched identity", id)
	}
	if t.isComplete[id] {
		if descriptorKey(t.types[id]) != descriptorKey(typ) {
			return fmt.Errorf("named IR type %q completed with conflicting descriptor", typ.Name)
		}
		return nil
	}
	typ.DisplayName = t.types[id].DisplayName
	t.types[id] = cloneType(typ)
	t.isComplete[id] = true
	return nil
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

// Type returns a detached snapshot of one completed runtime descriptor.
// Callers may inspect or modify returned slices without mutating table storage.
func (t *TypeTable) Type(id TypeID) (Type, bool) {
	if t == nil {
		return Type{}, false
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	if id == InvalidType || int(id) >= len(t.types) || !t.isComplete[id] {
		return Type{}, false
	}
	return cloneType(t.types[id]), true
}

// InterfaceMethod returns one published interface method descriptor from an
// interface value or carrier type. The descriptor owns the method slot ABI
// TypeID, so consumers never reconstruct slot function shape.
func (t *TypeTable) InterfaceMethod(id TypeID, slot int) (TypeMethod, bool) {
	typ, ok := t.interfaceType(id)
	if !ok || slot < 0 || slot >= len(typ.Methods) {
		return TypeMethod{}, false
	}
	return typ.Methods[slot], true
}

func (t *TypeTable) InterfaceMethodCount(id TypeID) (int, bool) {
	typ, ok := t.interfaceType(id)
	if !ok {
		return 0, false
	}
	return len(typ.Methods), true
}

func (t *TypeTable) interfaceType(id TypeID) (Type, bool) {
	typ, ok := t.Type(id)
	if !ok {
		return Type{}, false
	}
	if typ.Kind == TypeReference || typ.Kind == TypeOwnedPtr {
		typ, ok = t.Type(typ.Elem)
	}
	return typ, ok && typ.Kind == TypeInterface
}

// NamedTypeIDs returns completed identified composites ordered by display
// name. TypeIDs follow intern order, which parallel module lowering makes
// differ between runs; the display name derives from identity, so output
// built in this order is the same every run.
func (t *TypeTable) NamedTypeIDs() []TypeID {
	if t == nil {
		return nil
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	ids := make([]TypeID, 0)
	for index := 1; index < len(t.types); index++ {
		if !t.isComplete[index] {
			continue
		}
		if _, isNamed := identifiedTypeKey(t.types[index]); isNamed {
			ids = append(ids, TypeID(index))
		}
	}
	slices.SortFunc(ids, func(left, right TypeID) int {
		return strings.Compare(namedText(t.types[left], true), namedText(t.types[right], true))
	})
	for index := 1; index < len(ids); index++ {
		if name := namedText(t.types[ids[index]], true); name == namedText(t.types[ids[index-1]], true) {
			panic("named IR types share display name " + name)
		}
	}
	return ids
}

func (t *TypeTable) Text(id TypeID) string {
	if t == nil {
		return "<invalid>"
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.textLocked(id, false)
}

// DisplayName is Text with named types written by their DisplayName, so the
// same type reads the same in every compiler dump.
func (t *TypeTable) DisplayName(id TypeID) string {
	if t == nil {
		return "<invalid>"
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.textLocked(id, true)
}

// textLocked keeps recursive formatting under one read or write lock. Calling
// public Type or Text here would deadlock when Intern holds the write lock.
// qualified writes named types by DisplayName instead of their bare Name.
func (t *TypeTable) textLocked(id TypeID, qualified bool) string {
	if id == InvalidType || int(id) >= len(t.types) {
		return "<invalid>"
	}
	typ := t.types[id]
	switch typ.Kind {
	case TypeVoid:
		return "void"
	case TypeInteger:
		if typ.IsSigned {
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
		return "*" + t.textLocked(typ.Elem, qualified)
	case TypeReference:
		prefix := "&"
		if typ.IsMutable {
			prefix = "&mut "
		}
		return prefix + t.textLocked(typ.Elem, qualified)
	case TypeVariant:
		if payload, isOptional := typ.OptionalPayload(); isOptional {
			return "?" + t.textLocked(payload, qualified)
		}
		if typ.Family == VariantFamilyNamed && typ.Name != "" {
			return namedText(typ, qualified)
		}
		return "<invalid>"
	case TypeArray:
		if typ.Length == "" {
			return "[]" + t.textLocked(typ.Elem, qualified)
		}
		return "[" + typ.Length + "]" + t.textLocked(typ.Elem, qualified)
	case TypeSlice:
		return "[..]" + t.textLocked(typ.Elem, qualified)
	case TypeStruct:
		if typ.Name != "" && typ.Identity != "" {
			return namedText(typ, qualified)
		}
		return "struct{" + t.fieldsTextLocked(typ.Fields, ';', qualified) + "}"
	case TypeInterface:
		methods := make([]string, 0, len(typ.Methods))
		for _, method := range typ.Methods {
			params := make([]string, 0, len(method.Params)+1)
			switch method.Receiver {
			case MethodReceiverValue:
				params = append(params, "Self")
			case MethodReceiverShared:
				params = append(params, "&Self")
			case MethodReceiverMutable:
				params = append(params, "&mut Self")
			}
			for _, param := range method.Params {
				params = append(params, param.Name+": "+t.textLocked(param.Type, qualified))
			}
			text := method.Name + "(" + strings.Join(params, ", ") + ")"
			if method.Return != InvalidType && t.textLocked(method.Return, qualified) != "void" {
				text += " -> " + t.textLocked(method.Return, qualified)
			}
			methods = append(methods, text)
		}
		return "iface{" + strings.Join(methods, "; ") + "}"
	case TypeFunction:
		params := make([]string, 0, len(typ.Params))
		for _, param := range typ.Params {
			params = append(params, t.textLocked(param, qualified))
		}
		text := "fn(" + strings.Join(params, ", ") + ")"
		if typ.Return != InvalidType && t.textLocked(typ.Return, qualified) != "void" {
			text += " -> " + t.textLocked(typ.Return, qualified)
		}
		return text
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
	if key, ok := identifiedTypeKey(t.types[id]); ok {
		return key
	}
	return t.textLocked(id, false)
}

func (t *TypeTable) key(typ Type) string {
	if key, ok := identifiedTypeKey(typ); ok {
		return key
	}
	return descriptorKey(typ)
}

func identifiedTypeKey(typ Type) (string, bool) {
	if typ.Identity == "" {
		return "", false
	}
	switch typ.Kind {
	case TypeStruct:
		return "struct:" + typ.Identity, true
	case TypeVariant:
		if typ.Family == VariantFamilyNamed {
			return "variant:" + typ.Identity, true
		}
	}
	return "", false
}

func descriptorKey(typ Type) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d|%t|%d|%t|%q|%d|%d|%q|%d|%q", typ.Kind, typ.IsSigned, typ.Bits, typ.IsMutable, typ.Length, typ.Elem, typ.Return, typ.Name, typ.Family, typ.Identity)
	for _, variant := range typ.Cases {
		fmt.Fprintf(&b, "|v:%q:%d", variant.Name, variant.Payload)
	}
	for _, field := range typ.Fields {
		fmt.Fprintf(&b, "|f:%q:%d", field.Name, field.Type)
	}
	for _, method := range typ.Methods {
		fmt.Fprintf(&b, "|m:%q:%d:%d:%d", method.Name, method.Receiver, method.Return, method.SlotType)
		for _, param := range method.Params {
			fmt.Fprintf(&b, ":%q:%d", param.Name, param.Type)
		}
	}
	for _, param := range typ.Params {
		fmt.Fprintf(&b, "|p:%d", param)
	}
	return b.String()
}

func (t *TypeTable) fieldsTextLocked(fields []TypeField, separator byte, qualified bool) string {
	parts := make([]string, 0, len(fields))
	for _, field := range fields {
		parts = append(parts, field.Name+": "+t.textLocked(field.Type, qualified))
	}
	return strings.Join(parts, string(separator)+" ")
}

// namedText is the text of a named composite. A named type without a
// DisplayName shows its identity, which is just as unique and stable.
func namedText(typ Type, qualified bool) string {
	if !qualified {
		return typ.Name
	}
	if typ.DisplayName != "" {
		return typ.DisplayName
	}
	return typ.Identity
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
