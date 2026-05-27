package resolver

import (
	"fmt"
	"strings"

	"compiler/core/diagnostics"
	"compiler/internal/analysis/semantics/declinfo"
	"compiler/internal/analysis/semantics/table"
	"compiler/internal/context"
	"compiler/internal/frontend/ast"
)

func reportUnresolved(module *context.Module, decls *declinfo.ModuleInfo, fn *declinfo.Function, scope *table.Scope, node *ast.Ident, diag *diagnostics.DiagnosticBag) bool {
	if module == nil || node == nil || diag == nil {
		return false
	}
	if later, ok := nearestLaterDecl(node, fn); ok {
		msg := fmt.Sprintf("symbol `%s` used before declaration", node.Name)
		d := diagnostics.NewError(msg).
			WithCode(diagnostics.ErrUseBeforeDecl).
			WithPrimaryLabel(new(node.Loc()), "used here before declaration").
			WithSecondaryLabel(new(later.Loc), "declared later here").
			WithHelp("move declaration before first use")
		diag.Add(d)
		return false
	}
	if match, ok := nearestSymbolName(node.Name, decls, fn, scope); ok {
		msg := "unknown identifier `" + node.Name + "`"
		d := diagnostics.NewError(msg).
			WithCode(diagnostics.ErrUndefinedSymbol).
			WithPrimaryLabel(new(node.Loc()), msg).
			WithHelp("did you mean `" + match + "`?")
		diag.Add(d)
		return false
	}
	msg := "unknown identifier `" + node.Name + "`"
	diag.Add(diagnostics.NewError(msg).WithCode(diagnostics.ErrUndefinedSymbol).WithPrimaryLabel(new(node.Loc()), msg))
	return false
}

func nearestLaterDecl(node *ast.Ident, fn *declinfo.Function) (declinfo.LocalDecl, bool) {
	if node == nil || fn == nil {
		return declinfo.LocalDecl{}, false
	}
	candidates := fn.LocalNames[node.Name]
	if len(candidates) == 0 {
		return declinfo.LocalDecl{}, false
	}
	useLoc := node.Loc()
	if useLoc.Start == nil {
		return declinfo.LocalDecl{}, false
	}
	best := declinfo.LocalDecl{}
	found := false
	bestIndex := 0
	for _, site := range candidates {
		if site.Loc.Start == nil || site.Loc.Start.Index <= useLoc.Start.Index {
			continue
		}
		if !found || site.Loc.Start.Index < bestIndex {
			best = site
			bestIndex = site.Loc.Start.Index
			found = true
		}
	}
	return best, found
}

func nearestSymbolName(name string, decls *declinfo.ModuleInfo, fn *declinfo.Function, scope *table.Scope) (string, bool) {
	candidates := make([]rankedCandidate, 0)
	seen := make(map[string]struct{})
	scopeDepth := 0
	for sc := scope; sc != nil; sc = sc.Parent() {
		for _, sym := range sc.Symbols() {
			if sym == nil || sym.Name == "" {
				continue
			}
			if _, ok := seen[sym.Name]; ok {
				continue
			}
			seen[sym.Name] = struct{}{}
			candidates = append(candidates, rankedCandidate{Name: sym.Name, ScopeDepth: scopeDepth})
		}
		scopeDepth++
	}
	if fn != nil {
		for candidate := range fn.LocalNames {
			if _, ok := seen[candidate]; ok {
				continue
			}
			seen[candidate] = struct{}{}
			candidates = append(candidates, rankedCandidate{Name: candidate, ScopeDepth: scopeDepth})
		}
	}
	if decls != nil {
		for candidate := range decls.NameIndex {
			if _, ok := seen[candidate]; ok {
				continue
			}
			seen[candidate] = struct{}{}
			candidates = append(candidates, rankedCandidate{Name: candidate, ScopeDepth: 1 << 20})
		}
	}
	best := rankedCandidate{}
	found := false
	query := strings.ToLower(name)
	for _, candidate := range candidates {
		candidate.Score = rankCandidate(query, candidate)
		if !found || candidate.Score.less(best.Score) {
			best = candidate
			found = true
		}
	}
	if !found {
		return "", false
	}
	limit := 3
	if len(name) <= 2 {
		limit = 1
	}
	return best.Name, best.Score.Distance <= limit
}

type rankedCandidate struct {
	Name       string
	ScopeDepth int
	Score      candidateScore
}

type candidateScore struct {
	Distance        int
	SharedPrefixLen int
	LengthDiff      int
	ScopeDepth      int
	Name            string
}

func (s candidateScore) less(other candidateScore) bool {
	if s.Distance != other.Distance {
		return s.Distance < other.Distance
	}
	if s.SharedPrefixLen != other.SharedPrefixLen {
		return s.SharedPrefixLen > other.SharedPrefixLen
	}
	if s.LengthDiff != other.LengthDiff {
		return s.LengthDiff < other.LengthDiff
	}
	if s.ScopeDepth != other.ScopeDepth {
		return s.ScopeDepth < other.ScopeDepth
	}
	return s.Name < other.Name
}

func rankCandidate(query string, candidate rankedCandidate) candidateScore {
	lower := strings.ToLower(candidate.Name)
	return candidateScore{
		Distance:        levenshtein(query, lower),
		SharedPrefixLen: sharedPrefixLen(query, lower),
		LengthDiff:      abs(len(query) - len(lower)),
		ScopeDepth:      candidate.ScopeDepth,
		Name:            candidate.Name,
	}
}

func sharedPrefixLen(a, b string) int {
	limit := len(a)
	if len(b) < limit {
		limit = len(b)
	}
	n := 0
	for i := 0; i < limit; i++ {
		if a[i] != b[i] {
			break
		}
		n++
	}
	return n
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func levenshtein(a, b string) int {
	if a == b {
		return 0
	}
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := 0; j <= len(b); j++ {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 0
			if a[i-1] != b[j-1] {
				cost = 1
			}
			cur[j] = min3(
				prev[j]+1,
				cur[j-1]+1,
				prev[j-1]+cost,
			)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

func min3(a, b, c int) int {
	return min(min(a, b), c)
}
