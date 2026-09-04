// This file owns the phase-coverage contract for semantic type kinds.
//
// node_dispatch_test.go covers AST *syntax*: statements, expressions and type
// syntax. This covers the semantic types those lower into — the typeinfo.Type
// family. Adding one and forgetting that it must answer a capability, lower to
// a runtime type, or compare equal used to compile clean and fail much later,
// or silently produce a wrong answer.
package contracts

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// typeDispatchSite is one function that must answer for every semantic type.
//
// Only sites that must be *total* belong here. A narrow predicate whose default
// is a legitimate answer does not: classifying kinds there would be ceremony
// around a decision that is already correct.
type typeDispatchSite struct {
	file string
	fn   string
	// why records what this site answers, so a contributor reading a failure
	// knows what decision they owe rather than only that one is missing.
	why string
	// omitted classifies kinds this site implements no case for.
	omitted map[string]classification
}

// A missed kind at each of these is silently wrong rather than loudly rejected,
// which is what earns them a contract. IsSizedType and IsLowerableType are
// deliberately absent: their default rejects, so a forgotten kind produces a
// diagnostic rather than a wrong answer.
var typeKindSites = []typeDispatchSite{
	{
		file: "semantics/typeinfo/capability_walk.go",
		fn:   "ownershipCapability",
		why:  "how the type copies and whether scope cleanup must destroy it",
		omitted: map[string]classification{
			"InvalidType": {ignore, "a recovery type makes no capability claim; invalid source never reaches ownership"},
			"UnknownType": {ignore, "an unresolved type makes no capability claim; resolution replaces it first"},
			"NamedType":   {ignore, "a bare name carries no structure to classify; it is replaced by the type it names"},
			"TypeParameterType": {ignore, "conservatively move-on-use with no drop until instantiation-aware " +
				"queries arrive with generic support, as OwnershipCapability documents"},
			"FuncType": {ignore, "a function value is a code pointer owning no storage, so the walk's default of " +
				"move-on-use with no drop is safe. Whether it should copy implicitly is an open language " +
				"question, not a missing case"},
		},
	},
	{
		file: "semantics/typeinfo/relations.go",
		fn:   "SameType",
		why:  "whether two types are the same type",
		omitted: map[string]classification{
			"DefinedType": {contextual, "nominal identity is settled before the switch by sameNominalStruct, and " +
				"Underlying peels the definition away for the structural comparison that follows"},
		},
	},
	{
		file: "ir/hir/lower/lower_types.go",
		fn:   "intern",
		why:  "which runtime type the backend materializes",
		omitted: map[string]classification{
			"EnumType":     {contextual, "a named enum is interned through internDefined, which owns variant layout"},
			"OptionalType": {contextual, "an optional is shaped by loweredRuntimeType before it reaches interning"},
			"TypeParameterType": {reject, "a type parameter has no runtime representation; instantiation must " +
				"substitute it before lowering, and reaching here yields ir.InvalidType"},
		},
	},
}

func TestEverySemanticTypeKindHasAPhaseDecision(t *testing.T) {
	kinds := declaredTypeKinds(t)
	if len(kinds) < 10 {
		t.Fatalf("found %d semantic type kinds, expected the full family", len(kinds))
	}
	for _, site := range typeKindSites {
		t.Run(site.fn, func(t *testing.T) {
			handled := handledTypeKinds(t, site.file, site.fn, kinds)
			for _, kind := range kinds {
				entry, classified := site.omitted[kind]
				if slices.Contains(handled, kind) {
					if classified {
						t.Errorf("%s handles %s but still classifies it %s (%q); delete the entry",
							site.fn, kind, entry.decision, entry.reason)
					}
					continue
				}
				if !classified {
					t.Errorf("%s makes no decision about typeinfo.%s; it decides %s, so add a case or declare why the kind is inert",
						site.fn, kind, site.why)
				}
			}
		})
	}
}

func TestSemanticTypeOmissionReasonsNameRealKinds(t *testing.T) {
	kinds := declaredTypeKinds(t)
	for _, site := range typeKindSites {
		for kind, entry := range site.omitted {
			if !slices.Contains(kinds, kind) {
				t.Errorf("%s classifies type kind %s that no longer exists", site.fn, kind)
			}
			if strings.TrimSpace(entry.reason) == "" {
				t.Errorf("%s classifies %s as %s without a reason", site.fn, kind, entry.decision)
			}
			if entry.decision.String() == "unknown" {
				t.Errorf("%s classifies %s with an invalid decision", site.fn, kind)
			}
		}
	}
}

// declaredTypeKinds returns every type implementing typeinfo.Type, found by its
// TypeNode marker method exactly as the AST families are found by theirs.
func declaredTypeKinds(t *testing.T) []string {
	t.Helper()
	path := filepath.Join(internalDir(t), "semantics", "typeinfo", "types.go")
	parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	kinds := make([]string, 0)
	for _, decl := range parsed.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "TypeNode" {
			continue
		}
		if name, ok := receiverTypeName(fn); ok {
			kinds = append(kinds, name)
		}
	}
	slices.Sort(kinds)
	return kinds
}

// handledTypeKinds collects the kinds a function distinguishes, from both type
// switch cases and single type assertions.
//
// This is looser than the AST contract, which requires the switch operand to be
// a parameter. These functions switch on a derived value — Underlying(t) — and
// some peel DefinedType with an assertion before switching. Filtering the names
// to declared kinds is what keeps it honest: a switch over anything else
// contributes nothing.
func handledTypeKinds(t *testing.T, file, fn string, kinds []string) []string {
	t.Helper()
	path := filepath.Join(internalDir(t), filepath.FromSlash(file))
	parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	decl := findFuncDecl(parsed, fn)
	if decl == nil {
		t.Fatalf("function %s not found in %s", fn, file)
	}
	// A site inside package typeinfo names its kinds unqualified; one outside it
	// names them through the import's local alias.
	inTypeinfo := parsed.Name != nil && parsed.Name.Name == "typeinfo"
	local := importLocalName(parsed, "compiler/internal/semantics/typeinfo")
	handled := make([]string, 0)
	record := func(expr ast.Expr) {
		star, ok := expr.(*ast.StarExpr)
		if !ok {
			return
		}
		var name string
		switch target := star.X.(type) {
		case *ast.Ident:
			if !inTypeinfo {
				return
			}
			name = target.Name
		case *ast.SelectorExpr:
			if inTypeinfo {
				return
			}
			pkg, ok := target.X.(*ast.Ident)
			if !ok || pkg.Name != local {
				return
			}
			name = target.Sel.Name
		default:
			return
		}
		if slices.Contains(kinds, name) && !slices.Contains(handled, name) {
			handled = append(handled, name)
		}
	}
	ast.Inspect(decl.Body, func(node ast.Node) bool {
		switch current := node.(type) {
		case *ast.TypeSwitchStmt:
			for _, stmt := range current.Body.List {
				clause, ok := stmt.(*ast.CaseClause)
				if !ok {
					continue
				}
				for _, expr := range clause.List {
					record(expr)
				}
			}
		case *ast.TypeAssertExpr:
			if current.Type != nil {
				record(current.Type)
			}
		}
		return true
	})
	return handled
}
