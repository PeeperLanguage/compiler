package diagnostics

import "strings"

// NearestName finds the closest match to query among candidates using
// Levenshtein distance. Returns the best match and true if the distance
// is within an acceptable threshold (3 for names >2 chars, 1 otherwise).
func NearestName(query string, candidates []string) (string, bool) {
	if len(candidates) == 0 || query == "" {
		return "", false
	}
	limit := 3
	if len(query) <= 2 {
		limit = 1
	}
	queryLower := strings.ToLower(query)
	best := ""
	bestDist := limit + 1
	for _, c := range candidates {
		dist := Levenshtein(queryLower, strings.ToLower(c))
		if dist < bestDist {
			bestDist = dist
			best = c
		}
	}
	if bestDist > limit {
		return "", false
	}
	return best, true
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
