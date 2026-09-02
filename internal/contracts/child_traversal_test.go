package contracts

// Child traversal is handwritten. Interface satisfaction cannot see inside a
// valid method, so adding a node-bearing field and forgetting to visit it
// compiles, passes the node-dispatch contract, and silently hides the field
// from every ast.Inspect consumer.
//
// This reads the AST package and requires each forEachChild implementation to
// mention every node-bearing field of its own type. Fields whose type carries
// no AST node need no visit. A composite field the classifier cannot judge is
// reported rather than assumed inert, so an unfamiliar shape fails loudly.

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
	"Attributed":   "attribute metadata",
	"Attribute":    "attribute metadata",
	"Location":     "source position",
	"Position":     "source position",
	"Token":        "lexical token",
}

type astPackage struct {
	structs    map[string][]*ast.Field // type name -> fields
	traversals map[string][]string     // type name -> field names visited
	nodeTypes  map[string]bool         // type names that are AST nodes
	// visitedAnywhere holds every field name named inside a traversal context:
	// a forEachChild body, or a helper that expands a sub-structure by calling
	// visit. Sub-structures such as Param are not nodes and have no traversal of
	// their own, so this is where their expansion is observed.
	visitedAnywhere map[string]bool
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
		structs:         map[string][]*ast.Field{},
		traversals:      map[string][]string{},
		nodeTypes:       map[string]bool{},
		visitedAnywhere: map[string]bool{},
	}
	// Node interfaces are node-bearing by definition; a field typed as one of
	// them holds a child.
	for _, name := range []string{"Node", "Decl", "TypeDecl", "Stmt", "Expr", "TypeExpr"} {
		pkg.nodeTypes[name] = true
	}
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
					if callsVisit(typed) {
						for _, name := range allSelectorNames(typed) {
							pkg.visitedAnywhere[name] = true
						}
					}
					owner, ok := receiverTypeName(typed)
					if !ok {
						continue
					}
					if typed.Name.Name == "forEachChild" {
						pkg.nodeTypes[owner] = true
						pkg.traversals[owner] = visitedFields(typed)
					}
				}
			}
		}
	}
	if len(pkg.traversals) == 0 {
		t.Fatalf("no forEachChild implementations found in %s", dir)
	}
	return pkg
}

// callsVisit reports whether fn performs traversal, either a forEachChild body
// or a helper that expands a sub-structure.
func callsVisit(fn *ast.FuncDecl) bool {
	found := false
	ast.Inspect(fn, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if name, ok := call.Fun.(*ast.Ident); ok && strings.HasPrefix(name.Name, "visit") {
			found = true
		}
		return true
	})
	return found || fn.Name.Name == "forEachChild"
}

func allSelectorNames(fn *ast.FuncDecl) []string {
	names := make([]string, 0)
	ast.Inspect(fn, func(node ast.Node) bool {
		if selector, ok := node.(*ast.SelectorExpr); ok {
			names = append(names, selector.Sel.Name)
		}
		return true
	})
	return names
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
// field typed []Param counts even though Param is not itself a node.
func (p *astPackage) bearing(name string, seen map[string]bool) bool {
	if p.nodeTypes[name] {
		return true
	}
	if _, inert := nonNodeComposites[name]; inert {
		return false
	}
	fields, ok := p.structs[name]
	if !ok || seen[name] {
		return false
	}
	seen[name] = true
	for _, field := range fields {
		for _, inner := range typeNames(field.Type) {
			if inner != name && p.bearing(inner, seen) {
				return true
			}
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
// field must be named in some traversal context, or it is unreachable.
//
// Evidence is matched by field name across all traversal contexts, so a field
// sharing a name with a traversed field of another type can read as covered.
// Removing that needs go/types resolution; the check still catches a newly
// added field whose name appears in no traversal at all.
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
		if !held[name] || !pkg.bearing(name, map[string]bool{}) {
			continue
		}
		t.Run(name, func(t *testing.T) {
			for _, field := range fields {
				if len(field.Names) == 0 {
					continue
				}
				carries := false
				for _, inner := range typeNames(field.Type) {
					if pkg.bearing(inner, map[string]bool{}) {
						carries = true
						break
					}
				}
				if !carries {
					continue
				}
				if !pkg.visitedAnywhere[field.Names[0].Name] {
					t.Errorf("%s.%s carries an AST node but is never expanded in any traversal; ast.Inspect cannot reach it",
						name, field.Names[0].Name)
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
				bearing := false
				unknown := ""
				for _, name := range names {
					if pkg.bearing(name, map[string]bool{}) {
						bearing = true
						break
					}
					// Composite types must be classified rather than assumed inert.
					if _, known := nonNodeComposites[name]; known {
						continue
					}
					if _, isStruct := pkg.structs[name]; isStruct {
						unknown = name
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
				if !bearing || len(field.Names) == 0 {
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
