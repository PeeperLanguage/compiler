package typeinfo

import (
	"strconv"

	"compiler/internal/moduleid"
	"compiler/pkg/typednil"
)

// SemanticKey returns a deterministic semantic identity for typ. Each concrete
// type owns one description shared by structural traversal and fingerprinting,
// so fingerprint consumers never switch on concrete type implementations.
func SemanticKey(typ Type) string {
	return semanticKey(typ, make(map[Type]bool))
}

func semanticKey(typ Type, visiting map[Type]bool) string {
	if typ == nil || typednil.IsNil(typ) {
		return ""
	}
	if visiting[typ] {
		return moduleid.Frame("recursive", TypeText(typ))
	}
	visiting[typ] = true
	defer delete(visiting, typ)

	description := typ.description()
	components := make([]string, 0, 3+len(description.attributes)+3*len(description.children))
	components = append(components, description.kind, strconv.Itoa(len(description.attributes)))
	components = append(components, description.attributes...)
	components = append(components, strconv.Itoa(len(description.children)))
	for _, child := range description.children {
		components = append(components, strconv.Itoa(int(child.Relation)))
		if child.Type == nil || typednil.IsNil(child.Type) {
			components = append(components, "absent", "")
			continue
		}
		components = append(components, "present", semanticKey(child.Type, visiting))
	}
	return moduleid.Frame(components...)
}
