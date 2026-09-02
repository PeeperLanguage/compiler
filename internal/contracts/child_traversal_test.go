package contracts

// Child traversal is handwritten. Interface satisfaction cannot see inside a
// valid method, so adding a node-bearing field and forgetting to visit it
// compiles, passes the node-dispatch contract, and silently hides the field
// from every ast.Inspect consumer.
//
// This reads the AST package and enforces two completeness rules:
//
//   - every node-bearing field of a traversable node is visited by that node's
//     own forEachChild. An embedded composite that carries nodes must own a
//     traversal, and the embedding node must chain into it by type name.
//   - every node-bearing field of a non-node sub-structure (Param,
//     StructLitField, ...) is expanded by some traversal context that actually
//     handles that sub-structure. Evidence is matched by owner, not by bare
//     field name, so a field name visited in an unrelated context cannot
//     cover a forgotten field.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// nonNodeComposites are composite field types that carry no AST node. Each entry
// is a deliberate classification, not a default.
var nonNodeComposites = map[string]string{
	"CommentGroup": "documentation text, not part of the syntax tree",
	"Comment":      "documentation text, not part of the syntax tree",
	"NodeIDHolder": "embedded node identity",
	"NodeMeta":     "embedded identity and position metadata",
	"Documented":   "doc comment and declaration surface text",
	"Location":     "source position",
	"Position":     "source position",
	"Token":        "lexical token",
}

type astPackage struct {
	structs    map[string][]*ast.Field // type name -> fields
	traversals map[string][]string     // type name -> receiver field names visited
	nodeTypes  map[string]bool         // type names that are AST nodes
	markers    map[string][]string     // marker method -> implementing type names
	// expansions holds every function participating in traversal: forEachChild
	// methods and helpers accepting a visit func(Node) parameter. Coverage and
	// fields are transitive over package-level helper calls, so a chain such as
	// FnDecl.forEachChild -> inspectParams -> inspectParam attributes Param
	// fields to every owner that expands params.
	expansions map[string]*expansion
}

// expansion is one traversal context. owner is the receiver type for methods
// and empty for package-level helpers. mentions lists struct type names written
// anywhere in the function (signature included), so inspectParam(param Param,
// ...) mentions Param. fields lists every selector field name reachable through
// the helper-call closure. live records whether visit is actually invoked,
// directly or through helpers; a helper that never calls visit proves nothing.
type expansion struct {
	owner    string
	mentions map[string]bool
	fields   map[string]bool
	live     bool
}

func loadASTPackage(t *testing.T) *astPackage {
	t.Helper()
	dir := filepath.Join(internalDir(t), "frontend", "ast")
	pkgs, err := parser.ParseDir(token.NewFileSet(), dir, func(info os.FileInfo) bool {
		return !strings.HasSuffix(info.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", dir, err)
	}
	pkg := &astPackage{
		structs:    map[string][]*ast.Field{},
		traversals: map[string][]string{},
		nodeTypes:  map[string]bool{},
		markers:    map[string][]string{},
		expansions: map[string]*expansion{},
	}
	// Node interfaces are node-bearing by definition; a field typed as one of
	// them holds a child.
	for _, name := range []string{"Node", "Decl", "TypeDecl", "Stmt", "Expr", "TypeExpr"} {
		pkg.nodeTypes[name] = true
	}
	packageFuncs := map[string]*ast.FuncDecl{}
	for _, parsed := range pkgs {
		for _, file := range parsed.Files {
			for _, decl := range file.Decls {
				switch typed := decl.(type) {
				case *ast.GenDecl:
					for _, spec := range typed.Specs {
						spec, ok := spec.(*ast.TypeSpec)
						if !ok {
							continue
						}
						if structType, ok := spec.Type.(*ast.StructType); ok && structType.Fields != nil {
							pkg.structs[spec.Name.Name] = structType.Fields.List
						}
					}
				case *ast.FuncDecl:
					if marker := markerName(typed); marker != "" {
						if owner, ok := receiverTypeName(typed); ok {
							pkg.markers[marker] = append(pkg.markers[marker], owner)
						}
						continue
					}
					owner, isMethod := receiverTypeName(typed)
					if isMethod && typed.Name.Name == "forEachChild" {
						pkg.nodeTypes[owner] = true
						pkg.traversals[owner] = visitedFields(typed)
					}
					if isMethod || hasVisitParam(typed) {
						key := expansionKey(owner, typed.Name.Name)
						visitName, _ := visitParamName(typed)
						pkg.expansions[key] = &expansion{
							owner:    owner,
							mentions: mentionedStructs(pkg, typed),
							fields:   allSelectorNames(typed),
							live:     callsIdent(typed, visitName),
						}
						if !isMethod {
							packageFuncs[typed.Name.Name] = typed
						}
					}
				}
			}
		}
	}
	pkg.closeOverHelpers(packageFuncs)
	if len(pkg.traversals) == 0 {
		t.Fatalf("no forEachChild implementations found in %s", dir)
	}
	return pkg
}

// closeOverHelpers merges fields, mentions, and liveness transitively through
// package-level helper calls so inspectParam evidence reaches FnDecl.forEachChild.
func (p *astPackage) closeOverHelpers(packageFuncs map[string]*ast.FuncDecl) {
	callees := map[string][]string{}
	for key, fn := range packageFuncs {
		for _, name := range calledPackageFuncs(fn, packageFuncs) {
			callees[key] = append(callees[key], name)
		}
	}
	for changed := true; changed; {
		changed = false
		for key, targets := range callees {
			target := p.expansions[key]
			if target == nil {
				continue
			}
			for _, callee := range targets {
				source := p.expansions[callee]
				if source == nil {
					continue
				}
				for field := range source.fields {
					if !target.fields[field] {
						target.fields[field] = true
						changed = true
					}
				}
				for mention := range source.mentions {
					if !target.mentions[mention] {
						target.mentions[mention] = true
						changed = true
					}
				}
				if source.live && !target.live {
					target.live = true
					changed = true
				}
			}
		}
	}
}

func expansionKey(owner, name string) string {
	if owner == "" {
		return name
	}
	return owner + "." + name
}

// markerName returns the marker method name (stmtNode, exprNode, ...) or "".
func markerName(fn *ast.FuncDecl) string {
	switch fn.Name.Name {
	case "stmtNode", "exprNode", "declNode", "typeNode":
		if fn.Recv != nil {
			return fn.Name.Name
		}
	}
	return ""
}

// hasVisitParam reports whether the function accepts a visit func(Node)
// parameter, making it a traversal helper regardless of its name.
func hasVisitParam(fn *ast.FuncDecl) bool {
	_, ok := visitParamName(fn)
	return ok
}

// visitParamName returns the name of the func(Node) parameter, which is the
// identifier the body must call to expand children.
func visitParamName(fn *ast.FuncDecl) (string, bool) {
	if fn == nil || fn.Type.Params == nil {
		return "", false
	}
	for _, field := range fn.Type.Params.List {
		funcType, ok := field.Type.(*ast.FuncType)
		if !ok || funcType.Params == nil || len(funcType.Params.List) != 1 {
			continue
		}
		nodeType, ok := funcType.Params.List[0].Type.(*ast.Ident)
		if !ok || nodeType.Name != "Node" || len(field.Names) == 0 {
			continue
		}
		return field.Names[0].Name, true
	}
	return "", false
}

// callsIdent reports whether the function body invokes the given identifier.
func callsIdent(fn *ast.FuncDecl, name string) bool {
	if name == "" {
		return false
	}
	found := false
	ast.Inspect(fn.Body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == name {
			found = true
		}
		return true
	})
	return found
}

// calledPackageFuncs lists package-level helper functions the body calls.
func calledPackageFuncs(fn *ast.FuncDecl, packageFuncs map[string]*ast.FuncDecl) []string {
	names := make([]string, 0)
	ast.Inspect(fn.Body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		ident, ok := call.Fun.(*ast.Ident)
		if !ok {
			return true
		}
		if _, known := packageFuncs[ident.Name]; known {
			names = append(names, ident.Name)
		}
		return true
	})
	return names
}

func mentionedStructs(pkg *astPackage, fn *ast.FuncDecl) map[string]bool {
	mentions := map[string]bool{}
	ast.Inspect(fn, func(node ast.Node) bool {
		ident, ok := node.(*ast.Ident)
		if !ok {
			return true
		}
		if _, isStruct := pkg.structs[ident.Name]; isStruct {
			mentions[ident.Name] = true
		}
		return true
	})
	return mentions
}

func allSelectorNames(fn *ast.FuncDecl) map[string]bool {
	fields := map[string]bool{}
	ast.Inspect(fn, func(node ast.Node) bool {
		if selector, ok := node.(*ast.SelectorExpr); ok {
			fields[selector.Sel.Name] = true
		}
		return true
	})
	return fields
}

func receiverTypeName(fn *ast.FuncDecl) (string, bool) {
	if fn.Recv == nil || len(fn.Recv.List) != 1 {
		return "", false
	}
	expr := fn.Recv.List[0].Type
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	name, ok := expr.(*ast.Ident)
	if !ok {
		return "", false
	}
	return name.Name, true
}

// visitedFields returns every field of the receiver named anywhere in fn, which
// covers direct visits, range loops, and conditional visits alike.
func visitedFields(fn *ast.FuncDecl) []string {
	receiver := ""
	if len(fn.Recv.List[0].Names) == 1 {
		receiver = fn.Recv.List[0].Names[0].Name
	}
	fields := make([]string, 0)
	ast.Inspect(fn, func(node ast.Node) bool {
		selector, ok := node.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if base, ok := selector.X.(*ast.Ident); ok && base.Name == receiver {
			fields = append(fields, selector.Sel.Name)
		}
		return true
	})
	return fields
}

// typeNames returns every identifier naming a type inside a field type.
func typeNames(expr ast.Expr) []string {
	names := make([]string, 0)
	ast.Inspect(expr, func(node ast.Node) bool {
		if ident, ok := node.(*ast.Ident); ok {
			names = append(names, ident.Name)
		}
		if selector, ok := node.(*ast.SelectorExpr); ok {
			names = append(names, selector.Sel.Name)
			return false
		}
		return true
	})
	return names
}

// bearing reports whether a type name transitively carries an AST node, so a
// field typed []Param counts even though Param is not itself a node. The
// visited set is recursion-internal cycle protection, not caller state.
func (p *astPackage) bearing(name string) bool {
	seen := map[string]bool{}
	var walk func(string) bool
	walk = func(current string) bool {
		if p.nodeTypes[current] {
			return true
		}
		if _, inert := nonNodeComposites[current]; inert {
			return false
		}
		fields, ok := p.structs[current]
		if !ok || seen[current] {
			return false
		}
		seen[current] = true
		for _, field := range fields {
			for _, inner := range typeNames(field.Type) {
				if inner != current && walk(inner) {
					return true
				}
			}
		}
		return false
	}
	return walk(name)
}

// covers reports whether the expansion context handles the sub-structure: it
// either mentions the type (a helper parameter typed Param) or its owner struct
// holds it directly (StructLit holds []StructLitField).
func (p *astPackage) covers(e *expansion, sub string) bool {
	if e.mentions[sub] {
		return true
	}
	if e.owner == "" {
		return false
	}
	return slices.ContainsFunc(p.structs[e.owner], func(field *ast.Field) bool {
		return slices.Contains(typeNames(field.Type), sub)
	})
}

// expandsField reports whether some live traversal context that handles sub
// touches the given field name.
func (p *astPackage) expandsField(sub, field string) bool {
	for _, e := range p.expansions {
		if e.live && p.covers(e, sub) && e.fields[field] {
			return true
		}
	}
	return false
}

// typesHeldByNodes returns every type named by a field of a traversable node.
func (p *astPackage) typesHeldByNodes() map[string]bool {
	held := map[string]bool{}
	var walk func(string)
	walk = func(name string) {
		for _, field := range p.structs[name] {
			for _, inner := range typeNames(field.Type) {
				if _, ok := p.structs[inner]; !ok || held[inner] {
					continue
				}
				held[inner] = true
				walk(inner)
			}
		}
	}
	for name := range p.traversals {
		walk(name)
	}
	return held
}

// Sub-structures such as Param carry nodes but have no traversal of their own;
// their fields are expanded by a helper or an inline loop. Every node-bearing
// field must be expanded by a traversal context that handles that specific
// sub-structure, so a same-named field visited in an unrelated context cannot
// cover a forgotten one.
func TestEverySubStructureFieldIsExpanded(t *testing.T) {
	pkg := loadASTPackage(t)
	held := pkg.typesHeldByNodes()
	for name, fields := range pkg.structs {
		if _, isNode := pkg.traversals[name]; isNode {
			continue
		}
		// A sub-structure only needs expansion if some node actually holds it.
		// Root aggregates such as Module are held by nobody and are walked by
		// their consumers directly, not through ast.Inspect.
		if !held[name] || !pkg.bearing(name) {
			continue
		}
		t.Run(name, func(t *testing.T) {
			for _, field := range fields {
				if len(field.Names) == 0 {
					continue
				}
				if !slices.ContainsFunc(typeNames(field.Type), pkg.bearing) {
					continue
				}
				fieldName := field.Names[0].Name
				if !pkg.expandsField(name, fieldName) {
					t.Errorf("%s.%s carries an AST node but no traversal context handling %s expands it; ast.Inspect cannot reach it",
						name, fieldName, name)
				}
			}
		})
	}
}

func TestEveryNodeBearingFieldIsTraversed(t *testing.T) {
	pkg := loadASTPackage(t)
	for owner, visited := range pkg.traversals {
		t.Run(owner, func(t *testing.T) {
			for _, field := range pkg.structs[owner] {
				names := typeNames(field.Type)
				bearing := slices.ContainsFunc(names, pkg.bearing)
				unknown := ""
				if !bearing {
					// Composite types must be classified rather than assumed inert.
					for _, name := range names {
						if _, known := nonNodeComposites[name]; known {
							continue
						}
						if _, isStruct := pkg.structs[name]; isStruct {
							unknown = name
							break
						}
					}
				}
				label := "embedded " + strings.Join(names, ".")
				if len(field.Names) > 0 {
					label = field.Names[0].Name
				}
				if unknown != "" && !bearing {
					t.Errorf("%s.%s has unclassified composite type %s; classify it in nonNodeComposites or give it a traversal",
						owner, label, unknown)
					continue
				}
				if !bearing {
					continue
				}
				if len(field.Names) == 0 {
					// Embedded node-bearing composite: it must own a traversal and
					// the owner must chain into it, otherwise its children are only
					// reachable if every consumer knows to expand it manually.
					embedded := names[0]
					if _, hasTraversal := pkg.traversals[embedded]; !hasTraversal {
						t.Errorf("%s embeds node-bearing %s without a traversal; give it a forEachChild",
							owner, embedded)
						continue
					}
					if !slices.Contains(visited, embedded) {
						t.Errorf("%s.forEachChild never chains into embedded %s; call its forEachChild or the subtree is invisible to ast.Inspect",
							owner, embedded)
					}
					continue
				}
				if !slices.Contains(visited, field.Names[0].Name) {
					t.Errorf("%s.forEachChild never visits node-bearing field %s; add it or the field is invisible to ast.Inspect",
						owner, field.Names[0].Name)
				}
			}
		})
	}
}
