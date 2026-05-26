package semmeta

type BindingFlags uint8

const (
	FlagMutable BindingFlags = 1 << iota
)

func (f BindingFlags) Mutable() bool {
	return f&FlagMutable != 0
}

type ParamFlags uint8

const (
	FlagVariadic ParamFlags = 1 << iota
)

func (f ParamFlags) Variadic() bool {
	return f&FlagVariadic != 0
}

type ParamSpec[T any] struct {
	Name       string
	Type       T
	Flags      ParamFlags
	HasDefault bool
}

type ReceiverKind uint8

const (
	ReceiverValue ReceiverKind = iota
	ReceiverRef
	ReceiverRefMut
	ReceiverPtr
	ReceiverPtrMut
)

func ReceiverKindFromSyntax(receiver string) ReceiverKind {
	switch receiver {
	case "&":
		return ReceiverRef
	case "&mut ":
		return ReceiverRefMut
	case "*mut":
		return ReceiverPtrMut
	case "*":
		return ReceiverPtr
	default:
		return ReceiverValue
	}
}

func (k ReceiverKind) Prefix() string {
	switch k {
	case ReceiverRef:
		return "&"
	case ReceiverRefMut:
		return "&mut "
	case ReceiverPtrMut:
		return "*mut "
	case ReceiverPtr:
		return "*"
	default:
		return ""
	}
}

type ReceiverKey struct {
	Kind     ReceiverKind
	TypeName string
}

func (k ReceiverKey) String() string {
	return k.Kind.Prefix() + k.TypeName
}
