// This file owns the phase-coverage contract for the lowered representations.
//
// node_dispatch_test.go covers AST syntax and type_dispatch_test.go covers the
// semantic type model. This covers what those lower into: hir.Stmt, mir.Instr
// and mir.Terminator.
//
// Membership in those sets is already sealed at compile time by the marker
// methods and by ir/mir/model_membership_test.go. Membership is not coverage:
// a node can be a full member of mir.Instr and still be dropped silently by a
// lowering pass or the backend. That is what this file holds.
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

// irFamily is one lowered node set and the sites that must answer for it.
type irFamily struct {
	// name appears in failures, so a contributor knows which set they extended.
	name string
	// declared is the file declaring the set, and marker the method that marks
	// membership in it.
	declared string
	marker   string
	// pkgPath and pkgName resolve how a site names these kinds: unqualified
	// when the site lives in the declaring package, through the import's local
	// alias otherwise.
	pkgPath string
	pkgName string
	sites   []irDispatchSite
}

// Control flow is already blocks and terminators by the time MIR lowering walks
// CFG sites, so a structured statement never arrives at one. lowerCFGStmt's
// default panics rather than skipping, which is what makes that an invariant
// instead of an assumption.
const decomposedBeforeMIRReason = "control flow is decomposed into CFG blocks and terminators before MIR " +
	"lowering, so this never reaches a CFG site; the default panics if it does"

type irDispatchSite struct {
	file string
	fn   string
	why  string
	// omitted classifies kinds this site implements no case for.
	omitted map[string]classification
}

var irFamilies = []irFamily{
	{
		name:     "hir.Stmt",
		declared: "ir/hir/model.go",
		marker:   "stmtNode",
		pkgPath:  "compiler/internal/ir/hir",
		pkgName:  "hir",
		sites: []irDispatchSite{
			{
				file: "ir/mir/module_lower.go",
				fn:   "lowerCFGStmt",
				why:  "how the statement becomes MIR",
				omitted: map[string]classification{
					"Block":         {contextual, decomposedBeforeMIRReason},
					"For":           {contextual, decomposedBeforeMIRReason},
					"If":            {contextual, decomposedBeforeMIRReason},
					"SwitchVariant": {contextual, decomposedBeforeMIRReason},
				},
			},
			{
				file: "ir/hir/fold/fold.go",
				fn:   "foldStmt",
				why:  "how constant folding rewrites the statement",
			},
		},
	},
	{
		name:     "mir.Instr",
		declared: "ir/mir/model.go",
		marker:   "instrNode",
		pkgPath:  "compiler/internal/ir/mir",
		pkgName:  "mir",
		sites: []irDispatchSite{
			{
				file: "ir/mir/module_lower.go",
				fn:   "appendInstr",
				why:  "which source location the instruction carries",
				omitted: map[string]classification{
					"Print": {ignore, "constructed with its own expression location, which is more precise " +
						"than the statement location this stamps"},
					"DynamicArrayOp": {ignore, "constructed with its own expression location, which is more " +
						"precise than the statement location this stamps"},
				},
			},
			{
				file: "backend/llvm/emitter.go",
				fn:   "GenerateLLVMIR",
				why:  "what the backend emits for the instruction",
			},
		},
	},
	{
		name:     "mir.Terminator",
		declared: "ir/mir/model.go",
		marker:   "termNode",
		pkgPath:  "compiler/internal/ir/mir",
		pkgName:  "mir",
		sites: []irDispatchSite{
			{
				file: "ir/mir/module_lower.go",
				fn:   "setBlockTerm",
				why:  "which source location the terminator carries",
			},
			{
				file: "backend/llvm/emitter.go",
				fn:   "GenerateLLVMIR",
				why:  "what the backend emits for the terminator",
			},
		},
	},
}

func TestEveryLoweredNodeKindHasAPhaseDecision(t *testing.T) {
	for _, family := range irFamilies {
		kinds := declaredMarkerKinds(t, family.declared, family.marker)
		if len(kinds) == 0 {
			t.Fatalf("%s: no kinds found via %s in %s", family.name, family.marker, family.declared)
		}
		for _, site := range family.sites {
			t.Run(family.name+"/"+site.fn, func(t *testing.T) {
				handled := handledKindsIn(t, site.file, site.fn, family.pkgPath, family.pkgName, kinds)
				for _, kind := range kinds {
					entry, classified := site.omitted[kind]
					if slices.Contains(handled, kind) {
						if classified {
							t.Errorf("%s handles %s.%s but still classifies it %s (%q); delete the entry",
								site.fn, family.pkgName, kind, entry.decision, entry.reason)
						}
						continue
					}
					if !classified {
						t.Errorf("%s makes no decision about %s.%s; it decides %s, so add a case or declare why the kind is inert",
							site.fn, family.pkgName, kind, site.why)
					}
				}
			})
		}
	}
}

func TestLoweredOmissionReasonsNameRealKinds(t *testing.T) {
	for _, family := range irFamilies {
		kinds := declaredMarkerKinds(t, family.declared, family.marker)
		for _, site := range family.sites {
			for kind, entry := range site.omitted {
				if !slices.Contains(kinds, kind) {
					t.Errorf("%s classifies %s kind %s that is no longer a member",
						site.fn, family.name, kind)
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
}

// declaredMarkerKinds returns every type in one file that implements a marker
// method, which is how each node family in this repository declares membership.
func declaredMarkerKinds(t *testing.T, file, marker string) []string {
	t.Helper()
	path := filepath.Join(internalDir(t), filepath.FromSlash(file))
	parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	kinds := make([]string, 0)
	for _, decl := range parsed.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != marker {
			continue
		}
		if name, ok := receiverTypeName(fn); ok && !slices.Contains(kinds, name) {
			kinds = append(kinds, name)
		}
	}
	slices.Sort(kinds)
	return kinds
}

// handledKindsIn collects the kinds a function distinguishes, from type switch
// cases and from single type assertions.
//
// It is looser than the AST contract, which requires the switch operand to be a
// parameter: these sites switch on derived values, and some peel one kind with
// an assertion before switching. Filtering names to declared kinds is what keeps
// it honest — a switch over anything else contributes nothing. A site holding
// two switches over disjoint families, as the backend does for instructions and
// terminators, is separated by that same filter.
func handledKindsIn(t *testing.T, file, fn, pkgPath, pkgName string, kinds []string) []string {
	t.Helper()
	path := filepath.Join(internalDir(t), filepath.FromSlash(file))
	parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	decl := findFuncDecl(parsed, fn)
	if decl == nil {
		t.Fatalf("function %s not found in %s", fn, file)
	}
	// A site inside the declaring package names kinds unqualified; one outside
	// names them through the import's local alias.
	own := parsed.Name != nil && parsed.Name.Name == pkgName
	local := importLocalName(parsed, pkgPath)
	handled := make([]string, 0)
	record := func(expr ast.Expr) {
		star, ok := expr.(*ast.StarExpr)
		if !ok {
			return
		}
		var name string
		switch target := star.X.(type) {
		case *ast.Ident:
			if !own {
				return
			}
			name = target.Name
		case *ast.SelectorExpr:
			if own {
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
