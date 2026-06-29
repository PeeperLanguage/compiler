package ast

import "compiler/internal/source"

type CommentGroup struct {
	Text     string
	Location *source.Location
}

type Documented struct {
	Doc         *CommentGroup
	DeclSurface string
}

func (d *Documented) SetDocComment(doc *CommentGroup) {
	if d == nil {
		return
	}
	d.Doc = doc
}

func (d *Documented) GetDocComment() *CommentGroup {
	if d == nil {
		return nil
	}
	return d.Doc
}

func (d *Documented) SetDeclSurface(surface string) {
	if d == nil {
		return
	}
	d.DeclSurface = surface
}

func (d *Documented) GetDeclSurface() string {
	if d == nil {
		return ""
	}
	return d.DeclSurface
}

type Attribute struct {
	Name     string
	Args     []Expr
	Location *source.Location
}

const (
	AttributeExtern    = "extern"
	AttributeTest      = "test"
	AttributeTargetOS  = "target_os"
	AttributeMaxCalls  = "max_calls"
	AttributeNoCopy    = "no_copy"
	AttributeAllowCopy = "allow_copy"
	AttributeNoMangle  = "no_mangle"
)

type AttributeDefinition struct {
	Args    []AttributeArgSpec
	Targets AttributeTarget
	Doc     string
}

type AttributeArgSpec struct {
	Type     TypeExpr
	Optional bool
}

type AttributeTarget uint8

const (
	AttributeTargetFunc AttributeTarget = 1 << iota
	AttributeTargetType
)

var AttributeDefinitions = map[string]AttributeDefinition{
	AttributeExtern: {
		Args:    []AttributeArgSpec{{Type: &NamedType{Name: "cstr"}, Optional: true}},
		Targets: AttributeTargetFunc,
		Doc:     "Declare an external function. Optional string argument overrides the linked symbol name.",
	},
	AttributeTest: {
		Targets: AttributeTargetFunc,
		Doc:     "Mark a function as a test entry.",
	},
	AttributeTargetOS: {
		Args:    []AttributeArgSpec{{Type: &NamedType{Name: "cstr"}}},
		Targets: AttributeTargetFunc | AttributeTargetType,
		Doc:     "Restrict a declaration to a target operating system.",
	},
	AttributeMaxCalls: {
		Args:    []AttributeArgSpec{{Type: &NamedType{Name: "i32"}}},
		Targets: AttributeTargetFunc,
		Doc:     "Limit how many calls to a function are expected or permitted by later analysis.",
	},
	AttributeNoCopy: {
		Targets: AttributeTargetType,
		Doc:     "Mark a named type as move-only.",
	},
	AttributeAllowCopy: {
		Targets: AttributeTargetType,
		Doc:     "Force a named type to remain copyable.",
	},
	AttributeNoMangle: {
		Targets: AttributeTargetFunc,
		Doc:     "Keep a function's emitted name unmangled.",
	},
}

type Attributed struct {
	Attributes map[string]Attribute
}

func (a *Attributed) SetAttributes(attrs []Attribute) {
	if a == nil {
		return
	}
	a.Attributes = make(map[string]Attribute, len(attrs))
	for _, attr := range attrs {
		a.Attributes[attr.Name] = attr
	}
}

func (a *Attributed) GetAttributes() map[string]Attribute {
	if a == nil {
		return nil
	}
	return a.Attributes
}

func (a *Attributed) GetAttribute(name string) (Attribute, bool) {
	if a == nil || a.Attributes == nil {
		return Attribute{}, false
	}
	attr, ok := a.Attributes[name]
	return attr, ok
}
