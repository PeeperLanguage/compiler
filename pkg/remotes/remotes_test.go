package remotes

import "testing"

func TestParse(t *testing.T) {
	tests := []struct {
		name         string
		path         string
		wantProvider Provider
		wantRepoPath string
		wantOK       bool
	}{
		{
			name:         "github",
			path:         "github.com/acme/json",
			wantProvider: ProviderGitHub,
			wantRepoPath: "acme/json",
			wantOK:       true,
		},
		{
			name:         "gitlab",
			path:         "gitlab.com/group/repo",
			wantProvider: ProviderGitLab,
			wantRepoPath: "group/repo",
			wantOK:       true,
		},
		{
			name:         "bitbucket",
			path:         "bitbucket.org/team/pkg",
			wantProvider: ProviderBitbucket,
			wantRepoPath: "team/pkg",
			wantOK:       true,
		},
		{
			name:   "unsupported provider",
			path:   "example.com/acme/json",
			wantOK: false,
		},
		{
			name:   "missing repo path",
			path:   "github.com/",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, repoPath, ok := Parse(tt.path)
			if ok != tt.wantOK {
				t.Fatalf("Parse() ok = %v, want %v", ok, tt.wantOK)
			}
			if provider != tt.wantProvider {
				t.Fatalf("Parse() provider = %q, want %q", provider, tt.wantProvider)
			}
			if repoPath != tt.wantRepoPath {
				t.Fatalf("Parse() repo path = %q, want %q", repoPath, tt.wantRepoPath)
			}
		})
	}
}

func TestIsRemotePath(t *testing.T) {
	if !IsRemotePath("gitlab.com/group/repo") {
		t.Fatal("expected supported remote path")
	}
	if IsRemotePath("./deps/ui") {
		t.Fatal("unexpected relative path classified as remote")
	}
}

func TestStripProviderPrefix(t *testing.T) {
	if got := StripProviderPrefix("bitbucket.org/team/pkg"); got != "team/pkg" {
		t.Fatalf("StripProviderPrefix() = %q, want %q", got, "team/pkg")
	}
	if got := StripProviderPrefix("example.com/team/pkg"); got != "example.com/team/pkg" {
		t.Fatalf("StripProviderPrefix() = %q, want unchanged path", got)
	}
}
