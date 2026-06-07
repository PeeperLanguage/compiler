package manifest

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
)

const (
	LockfileName       = "ember.lock"
	lockfileVersion   = "1.0"
	lockfileCurrentVer = lockfileVersion
)

type LockfileEntry struct {
	Version      string   `json:"version"`
	ResolvedURL  string   `json:"resolved_url,omitempty"`
	Checksum     string   `json:"checksum,omitempty"`
	Direct       bool     `json:"direct,omitempty"`
	Description  string   `json:"description,omitempty"`
	Dependencies []string `json:"dependencies,omitempty"`
	UsedBy       []string `json:"used_by,omitempty"`
	DownloadedAt string   `json:"downloaded_at,omitempty"`
}

type Lockfile struct {
	Version      string                   `json:"version"`
	DirectDeps   map[string]string        `json:"direct_deps,omitempty"`
	Packages     map[string]LockfileEntry `json:"packages,omitempty"`
	Dependencies map[string]LockfileEntry `json:"dependencies,omitempty"`
	GeneratedAt  string                   `json:"generated_at,omitempty"`
}

func NewLockfile() *Lockfile {
	return &Lockfile{
		Version:      lockfileCurrentVer,
		DirectDeps:   map[string]string{},
		Packages:     map[string]LockfileEntry{},
		Dependencies: map[string]LockfileEntry{},
		GeneratedAt:  time.Now().Format(time.RFC3339),
	}
}

func LoadLockfile(projectRoot string) (*Lockfile, error) {
	path := filepath.Join(projectRoot, LockfileName)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return NewLockfile(), nil
		}
		return nil, fmt.Errorf("read lockfile: %w", err)
	}

	raw, err := parseRawLockfile(data)
	if err != nil {
		return nil, err
	}

	lock := &Lockfile{
		Version:      raw.Version,
		DirectDeps:   raw.DirectDeps,
		Packages:     normalizePackageEntries(raw.Packages),
		Dependencies: normalizePackageEntries(raw.Dependencies),
		GeneratedAt:  raw.GeneratedAt,
	}
	normalizeLockfileShape(lock)
	return lock, nil
}

// parseRawLockfile reads a lockfile from disk and decodes its raw structure.
func parseRawLockfile(data []byte) (*rawLockfile, error) {
	type rawLockfileJSON struct {
		Version      string                   `json:"version"`
		DirectDeps   json.RawMessage          `json:"direct_deps"`
		Packages     map[string]LockfileEntry `json:"packages"`
		Dependencies map[string]LockfileEntry `json:"dependencies"`
		GeneratedAt  string                   `json:"generated_at,omitempty"`
	}
	var raw rawLockfileJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse lockfile: %w", err)
	}
	directDeps, err := decodeDirectDeps(raw.DirectDeps)
	if err != nil {
		return nil, fmt.Errorf("parse lockfile direct_deps: %w", err)
	}
	return &rawLockfile{
		Version:      raw.Version,
		DirectDeps:   directDeps,
		Packages:     raw.Packages,
		Dependencies: raw.Dependencies,
		GeneratedAt:  raw.GeneratedAt,
	}, nil
}

// rawLockfile holds the decoded lockfile data before normalization.
type rawLockfile struct {
	Version      string
	DirectDeps   map[string]string
	Packages     map[string]LockfileEntry
	Dependencies map[string]LockfileEntry
	GeneratedAt  string
}

// normalizeLockfileShape applies version defaults, map-nil guards,
// and the Packages/Dependencies sync rules used by both Load and Save.
func normalizeLockfileShape(lock *Lockfile) {
	if lock.Version == "" || lock.Version == lockfileVersion {
		lock.Version = lockfileCurrentVer
	}
	if lock.Packages == nil {
		lock.Packages = map[string]LockfileEntry{}
	}
	if lock.Dependencies == nil {
		lock.Dependencies = map[string]LockfileEntry{}
	}
	if len(lock.Packages) == 0 && len(lock.Dependencies) > 0 {
		lock.Packages = copyEntries(lock.Dependencies)
	}
	if len(lock.Dependencies) == 0 && len(lock.Packages) > 0 {
		lock.Dependencies = copyEntries(lock.Packages)
	}
	lock.DirectDeps = normalizeDirectDeps(lock.DirectDeps, lock.Packages)
	reconcileDirectFlags(lock)
	if lock.DirectDeps == nil {
		lock.DirectDeps = map[string]string{}
	}
}

func SaveLockfile(projectRoot string, lock *Lockfile) error {
	if lock == nil {
		return fmt.Errorf("nil lockfile")
	}
	normalizeLockfileShape(lock)
	lock.GeneratedAt = time.Now().Format(time.RFC3339)

	out := &Lockfile{
		Version:     lock.Version,
		DirectDeps:  sortStringMap(lock.DirectDeps),
		Packages:    sortEntriesByKey(lock.Packages),
		GeneratedAt: lock.GeneratedAt,
	}

	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal lockfile: %w", err)
	}
	path := filepath.Join(projectRoot, LockfileName)
	return os.WriteFile(path, data, 0o644)
}

// sortEntriesByKey returns a new map whose iteration order is alphabetical by key.
// Used for deterministic JSON output.
func sortEntriesByKey(entries map[string]LockfileEntry) map[string]LockfileEntry {
	keys := sortedKeys(entries)
	sorted := make(map[string]LockfileEntry, len(entries))
	for _, key := range keys {
		sorted[key] = entries[key]
	}
	return sorted
}

// sortStringMap returns a new map whose iteration order is alphabetical by key.
func sortStringMap(m map[string]string) map[string]string {
	keys := sortedKeys(m)
	sorted := make(map[string]string, len(m))
	for _, key := range keys {
		sorted[key] = m[key]
	}
	return sorted
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func (l *Lockfile) SetDependency(key string, entry LockfileEntry) {
	if l == nil {
		return
	}
	ensureEntryMaps(l)
	l.Packages[key] = entry
	l.Dependencies[key] = entry
}

func (l *Lockfile) GetDependency(key string) (LockfileEntry, bool) {
	if l == nil {
		return LockfileEntry{}, false
	}
	if entry, ok := l.Packages[key]; ok {
		return entry, true
	}
	entry, ok := l.Dependencies[key]
	return entry, ok
}

func (l *Lockfile) RemoveDependency(key string) {
	if l == nil {
		return
	}
	entry, found := l.GetDependency(key)
	if found {
		for _, child := range entry.Dependencies {
			l.RemoveUsedBy(child, key)
		}
	}
	ensureEntryMaps(l)
	delete(l.Packages, key)
	delete(l.Dependencies, key)
	l.unlinkKeyFromAllUsedBy(key)
	l.unlinkKeyFromDirectDeps(key)
}

func (l *Lockfile) unlinkKeyFromAllUsedBy(key string) {
	for depKey, depEntry := range l.Packages {
		depChanged := false
		depEntry.Dependencies = filterOut(depEntry.Dependencies, key, &depChanged)
		depEntry.UsedBy = filterOut(depEntry.UsedBy, key, &depChanged)
		if depChanged {
			l.SetDependency(depKey, depEntry)
		}
	}
}

func (l *Lockfile) unlinkKeyFromDirectDeps(key string) {
	if l.DirectDeps == nil {
		return
	}
	for alias, dep := range l.DirectDeps {
		if dep == key || alias == key {
			delete(l.DirectDeps, alias)
		}
	}
}

func (l *Lockfile) RemoveDirectDependency(alias string) {
	if l == nil || l.DirectDeps == nil {
		return
	}
	packageID, ok := l.DirectDeps[alias]
	if !ok {
		return
	}
	delete(l.DirectDeps, alias)
	entry, found := l.GetDependency(packageID)
	if !found {
		return
	}
	if l.isStillDirect(packageID) {
		return
	}
	entry.Direct = false
	l.SetDependency(packageID, entry)
}

func (l *Lockfile) isStillDirect(packageID string) bool {
	for _, depID := range l.DirectDeps {
		if depID == packageID {
			return true
		}
	}
	return false
}

func (l *Lockfile) AddDirectDependency(key string) {
	if l == nil {
		return
	}
	ensureDirectDepsMap(l)
	l.DirectDeps[key] = key
}

func (l *Lockfile) SetDirectDependency(alias, packageID string) {
	if l == nil || alias == "" || packageID == "" {
		return
	}
	ensureDirectDepsMap(l)
	if previous, ok := l.DirectDeps[alias]; ok && previous != packageID {
		if entry, found := l.GetDependency(previous); found {
			entry.Direct = false
			l.SetDependency(previous, entry)
		}
	}
	l.DirectDeps[alias] = packageID
	if entry, found := l.GetDependency(packageID); found {
		entry.Direct = true
		l.SetDependency(packageID, entry)
	}
}

func (l *Lockfile) GetDirectDependency(alias string) (string, bool) {
	if l == nil || alias == "" {
		return "", false
	}
	packageID, ok := l.DirectDeps[alias]
	return packageID, ok
}

func (l *Lockfile) FindPackageIDsByRepo(repo string) []string {
	if l == nil || repo == "" {
		return nil
	}
	out := make([]string, 0)
	seen := make(map[string]struct{})
	for key, entry := range l.Packages {
		if entryRepoOf(key, entry) != repo {
			continue
		}
		packageID := canonicalPackageID(key, entry)
		if packageID == "" {
			continue
		}
		if _, ok := seen[packageID]; ok {
			continue
		}
		seen[packageID] = struct{}{}
		out = append(out, packageID)
	}
	sort.Strings(out)
	return out
}

func (l *Lockfile) AddUsedBy(depKey, parentKey string) {
	entry, ok := l.GetDependency(depKey)
	if !ok {
		return
	}
	if slices.Contains(entry.UsedBy, parentKey) {
		return
	}
	entry.UsedBy = append(entry.UsedBy, parentKey)
	l.SetDependency(depKey, entry)
}

func (l *Lockfile) RemoveUsedBy(depKey, parentKey string) {
	entry, ok := l.GetDependency(depKey)
	if !ok {
		return
	}
	filtered := make([]string, 0, len(entry.UsedBy))
	for _, usedBy := range entry.UsedBy {
		if usedBy != parentKey {
			filtered = append(filtered, usedBy)
		}
	}
	entry.UsedBy = filtered
	l.SetDependency(depKey, entry)
}

func (l *Lockfile) GetUnusedDependencies() []string {
	if l == nil {
		return nil
	}
	unused := make([]string, 0)
	for key, entry := range l.Packages {
		if !entry.Direct && len(entry.UsedBy) == 0 {
			unused = append(unused, key)
		}
	}
	sort.Strings(unused)
	return unused
}

func (l *Lockfile) UpdateDependencyEdges(parentKey string, dependencies []string) {
	if l == nil || parentKey == "" {
		return
	}
	parentEntry, found := l.GetDependency(parentKey)
	if !found {
		return
	}
	prev := stringSetFromSlice(parentEntry.Dependencies)
	next := uniqueStrings(dependencies)
	nextSet := stringSetFromSlice(next)
	for dep := range prev {
		if _, keep := nextSet[dep]; !keep {
			l.RemoveUsedBy(dep, parentKey)
		}
	}
	for dep := range nextSet {
		if _, had := prev[dep]; !had {
			l.AddUsedBy(dep, parentKey)
		}
	}
	parentEntry.Dependencies = next
	l.SetDependency(parentKey, parentEntry)
}

func filterOut(values []string, target string, changed *bool) []string {
	if len(values) == 0 {
		return values
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == target {
			*changed = true
			continue
		}
		out = append(out, value)
	}
	return out
}

func uniqueStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func decodeDirectDeps(raw json.RawMessage) (map[string]string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return map[string]string{}, nil
	}

	var byAlias map[string]string
	if err := json.Unmarshal(raw, &byAlias); err == nil {
		return byAlias, nil
	}

	var asList []string
	if err := json.Unmarshal(raw, &asList); err == nil {
		converted := make(map[string]string, len(asList))
		for _, dep := range asList {
			converted[dep] = dep
		}
		return converted, nil
	}
	return nil, fmt.Errorf("expected object or array")
}

func copyEntries(src map[string]LockfileEntry) map[string]LockfileEntry {
	if src == nil {
		return nil
	}
	dst := make(map[string]LockfileEntry, len(src))
	maps.Copy(dst, src)
	return dst
}

func normalizePackageEntries(src map[string]LockfileEntry) map[string]LockfileEntry {
	if src == nil {
		return nil
	}
	dst := make(map[string]LockfileEntry, len(src))
	for key, entry := range src {
		normalizedKey := key
		if _, _, ok := SplitPackageID(key); !ok {
			repo := entry.ResolvedURL
			if repo == "" {
				repo = key
			}
			if entry.Version != "" {
				normalizedKey = PackageID(repo, entry.Version)
			}
		}
		if entry.ResolvedURL == "" {
			entry.ResolvedURL = repoFromPackageKey(normalizedKey)
		}
		dst[normalizedKey] = entry
	}
	return dst
}

func normalizeDirectDeps(src map[string]string, packages map[string]LockfileEntry) map[string]string {
	if src == nil {
		return nil
	}
	out := make(map[string]string, len(src))
	for key, value := range src {
		alias := key
		packageID := value
		if packageID == "" {
			packageID = key
		}
		if _, _, ok := SplitPackageID(packageID); !ok {
			if entry, ok := packages[packageID]; ok && entry.Version != "" {
				repo := entry.ResolvedURL
				if repo == "" {
					repo = repoFromPackageKey(packageID)
				}
				packageID = PackageID(repo, entry.Version)
			} else if resolved, ok := pickPackageIDForRepo(packages, packageID); ok {
				packageID = resolved
			}
		}
		out[alias] = packageID
	}
	return out
}

func PackageID(repo, version string) string {
	return strings.TrimSpace(repo) + "@" + strings.TrimSpace(version)
}

func SplitPackageID(packageID string) (repo, version string, ok bool) {
	trimmed := strings.TrimSpace(packageID)
	idx := strings.LastIndex(trimmed, "@")
	if idx <= 0 || idx >= len(trimmed)-1 {
		return "", "", false
	}
	return trimmed[:idx], trimmed[idx+1:], true
}

func repoFromPackageKey(key string) string {
	repo, _, ok := SplitPackageID(key)
	if ok {
		return repo
	}
	return key
}

func pickPackageIDForRepo(packages map[string]LockfileEntry, repo string) (string, bool) {
	best := ""
	for key, entry := range packages {
		if entryRepoOf(key, entry) != repo {
			continue
		}
		packageID := canonicalPackageID(key, entry)
		if packageID == "" {
			continue
		}
		if packageID > best {
			best = packageID
		}
	}
	if best == "" {
		return "", false
	}
	return best, true
}

func reconcileDirectFlags(lock *Lockfile) {
	if lock == nil {
		return
	}
	ensureEntryMaps(lock)
	for key, entry := range lock.Packages {
		entry.Direct = false
		lock.Packages[key] = entry
		lock.Dependencies[key] = entry
	}
	for _, packageID := range lock.DirectDeps {
		entry, ok := lock.Packages[packageID]
		if !ok {
			continue
		}
		entry.Direct = true
		lock.Packages[packageID] = entry
		lock.Dependencies[packageID] = entry
	}
}

// ensureEntryMap guarantees that the Packages/Dependencies maps are non-nil.
func ensureEntryMaps(l *Lockfile) {
	if l.Packages == nil {
		l.Packages = make(map[string]LockfileEntry)
	}
	if l.Dependencies == nil {
		l.Dependencies = make(map[string]LockfileEntry)
	}
}

// ensureDirectDepsMap guarantees that the DirectDeps map is non-nil.
func ensureDirectDepsMap(l *Lockfile) {
	if l.DirectDeps == nil {
		l.DirectDeps = make(map[string]string)
	}
}

// entryRepoOf returns the canonical repo name for a lockfile entry,
// preferring ResolvedURL and falling back to the parsed package key.
func entryRepoOf(key string, entry LockfileEntry) string {
	if entry.ResolvedURL != "" {
		return entry.ResolvedURL
	}
	return repoFromPackageKey(key)
}

// canonicalPackageID returns a fully-qualified packageID for an entry,
// or "" if the entry cannot be canonically identified.
func canonicalPackageID(key string, entry LockfileEntry) string {
	if _, _, ok := SplitPackageID(key); ok {
		return key
	}
	if entry.Version == "" {
		return ""
	}
	return PackageID(entryRepoOf(key, entry), entry.Version)
}

func stringSetFromSlice(values []string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, v := range values {
		set[v] = struct{}{}
	}
	return set
}
