package diagnostics

import "strings"

type NameCandidate struct {
	Name     string
	Priority int
}

type nameMatch struct {
	Name            string
	Distance        int
	SharedPrefixLen int
	LengthDiff      int
	Priority        int
	Order           int
}

// NearestName finds the closest match to query among candidates and only
// suggests it when the typo signal is strong enough and not ambiguous.
func NearestName(query string, candidates []string) (string, bool) {
	ranked := make([]NameCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		ranked = append(ranked, NameCandidate{Name: candidate})
	}
	return NearestNameWithPriority(query, ranked)
}

// NearestNameWithPriority finds the closest match to query and uses Priority
// as a stable semantic tie-break when candidates are otherwise equally close.
func NearestNameWithPriority(query string, candidates []NameCandidate) (string, bool) {
	if len(candidates) == 0 || query == "" {
		return "", false
	}

	queryLower := strings.ToLower(query)
	best, second, found := rankNearestName(queryLower, candidates)
	if !found || !acceptNearestName(queryLower, best, second) {
		return "", false
	}

	return best.Name, true
}

func rankNearestName(query string, candidates []NameCandidate) (nameMatch, nameMatch, bool) {
	best := nameMatch{}
	second := nameMatch{}
	found := false
	secondFound := false
	for i, candidate := range candidates {
		if strings.TrimSpace(candidate.Name) == "" {
			continue
		}
		match := scoreNameMatch(query, candidate, i)
		if !found || match.less(best) {
			if found {
				second = best
				secondFound = true
			}
			best = match
			found = true
			continue
		}
		if !secondFound || match.less(second) {
			second = match
			secondFound = true
		}
	}
	if !secondFound {
		second = nameMatch{Distance: -1}
	}
	return best, second, found
}

func scoreNameMatch(query string, candidate NameCandidate, order int) nameMatch {
	lower := strings.ToLower(candidate.Name)
	return nameMatch{
		Name:            candidate.Name,
		Distance:        Levenshtein(query, lower),
		SharedPrefixLen: sharedPrefixLen(query, lower),
		LengthDiff:      abs(len(query) - len(lower)),
		Priority:        candidate.Priority,
		Order:           order,
	}
}

func acceptNearestName(query string, best, second nameMatch) bool {
	if best.Distance < 0 {
		return false
	}
	queryLen := len(query)
	if queryLen == 0 {
		return false
	}

	switch {
	case queryLen <= 2:
		if best.Distance == 0 {
			return true
		}
		if best.Distance != 1 || best.SharedPrefixLen == 0 {
			return false
		}
	case queryLen <= 4:
		if best.Distance > 1 {
			return false
		}
	case queryLen <= 7:
		if best.Distance > 2 || best.Distance*5 > max(queryLen, len(best.Name))*2 {
			return false
		}
	default:
		if best.Distance > 3 || best.Distance*3 > max(queryLen, len(best.Name)) {
			return false
		}
	}

	if second.Distance >= 0 && !best.clearlyBetterThan(second) {
		return false
	}
	return true
}

func (m nameMatch) less(other nameMatch) bool {
	if m.Distance != other.Distance {
		return m.Distance < other.Distance
	}
	if m.SharedPrefixLen != other.SharedPrefixLen {
		return m.SharedPrefixLen > other.SharedPrefixLen
	}
	if m.Priority != other.Priority {
		return m.Priority < other.Priority
	}
	if m.LengthDiff != other.LengthDiff {
		return m.LengthDiff < other.LengthDiff
	}
	return m.Order < other.Order
}

func (m nameMatch) clearlyBetterThan(other nameMatch) bool {
	if other.Distance < 0 {
		return true
	}
	if m.Distance+1 < other.Distance {
		return true
	}
	if m.Distance == other.Distance {
		if m.SharedPrefixLen >= other.SharedPrefixLen+2 {
			return true
		}
		return m.Priority < other.Priority
	}
	if m.Distance+1 == other.Distance {
		if m.SharedPrefixLen > other.SharedPrefixLen {
			return true
		}
		return m.Priority < other.Priority && m.SharedPrefixLen >= other.SharedPrefixLen
	}
	return false
}

func sharedPrefixLen(a, b string) int {
	limit := min(len(a), len(b))
	n := 0
	for i := range limit {
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

// Levenshtein computes the edit distance between two strings.
func Levenshtein(a, b string) int {
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
			cur[j] = min(
				min(prev[j]+1, cur[j-1]+1),
				prev[j-1]+cost,
			)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}
