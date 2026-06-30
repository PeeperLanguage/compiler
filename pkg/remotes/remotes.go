package remotes

import "strings"

type Provider string

const (
	ProviderGitHub    Provider = "github.com"
	ProviderGitLab    Provider = "gitlab.com"
	ProviderBitbucket Provider = "bitbucket.org"
)

var supportedProviders = [...]Provider{
	ProviderGitHub,
	ProviderGitLab,
	ProviderBitbucket,
}

// Parse splits a supported remote repo path into provider host and provider-local
// repo path. Unsupported hosts and empty repo paths return ok=false.
func Parse(path string) (provider Provider, repoPath string, ok bool) {
	path = strings.TrimSpace(path)
	for _, provider := range supportedProviders {
		prefix := string(provider) + "/"
		if after, matched := strings.CutPrefix(path, prefix); matched && after != "" {
			return provider, after, true
		}
	}
	return "", "", false
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
