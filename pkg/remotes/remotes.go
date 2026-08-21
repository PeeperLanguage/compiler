package remotes

import (
	"compiler/pkg/ascii"
	"strings"
)

type Provider string

const (
	ProviderGitHub    Provider = "github.com"
	ProviderGitLab    Provider = "gitlab.com"
	ProviderBitbucket Provider = "bitbucket.org"
)

// spec describes how a provider's repo paths are structured.
type spec struct {
	host         Provider
	allowNesting bool // e.g. GitLab subgroups: org/subgroup/.../repo
}

var providers = [...]spec{
	{ProviderGitHub, false},
	{ProviderGitLab, true},
	{ProviderBitbucket, false},
}

// Parse splits a supported remote repo path into provider host and provider-local
// repo path. Unsupported hosts and empty repo paths return ok=false.
func Parse(path string) (provider Provider, repoPath string, ok bool) {
	path = strings.TrimSpace(path)
	for _, p := range providers {
		after, matched := strings.CutPrefix(path, string(p.host)+"/")
		if !matched || after == "" {
			continue
		}
		segments := strings.Split(after, "/")
		if len(segments) < 2 || (!p.allowNesting && len(segments) != 2) {
			return "", "", false
		}
		if !validSegments(segments) {
			return "", "", false
		}
		return p.host, after, true
	}
	return "", "", false
}

func validSegments(segments []string) bool {
	for _, s := range segments {
		if s == "" || s == "." || s == ".." {
			return false
		}
		for _, c := range s {
			if !validPathChar(c) {
				return false
			}
		}
	}
	return true
}

func validPathChar(c rune) bool {
	return ascii.IsAlnum(c) || c == '-' || c == '_' || c == '.'
}

func IsRemotePath(path string) bool {
	_, _, ok := Parse(path)
	return ok
}

func StripProviderPrefix(path string) string {
	_, repoPath, ok := Parse(path)
	if ok {
		return repoPath
	}
	return path
}
