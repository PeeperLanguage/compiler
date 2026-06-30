package registry

import "testing"

func Test_packageArchiveURL(t *testing.T) {
	tests := []struct {
		name string // description of this test case
		// Named input parameters for target function.
		repoName string
		version  string
		want     string
		wantErr  bool
	}{
		{
			name:     "github",
			repoName: "github.com/acme/json",
			version:  "v1.2.3",
			want:     "https://github.com/acme/json/archive/refs/tags/v1.2.3.tar.gz",
		},
		{
			name:     "gitlab",
			repoName: "gitlab.com/group/repo",
			version:  "v2.0.0",
			want:     "https://gitlab.com/group/repo/-/archive/v2.0.0/repo-v2.0.0.tar.gz",
		},
		{
			name:     "bitbucket",
			repoName: "bitbucket.org/team/pkg",
			version:  "v0.9.0",
			want:     "https://bitbucket.org/team/pkg/get/v0.9.0.tar.gz",
		},
		{
			name:     "unsupported provider",
			repoName: "example.com/acme/json",
			version:  "v1.0.0",
			wantErr:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, gotErr := packageArchiveURL(tt.repoName, tt.version)
			if gotErr != nil {
				if !tt.wantErr {
					t.Errorf("packageArchiveURL() failed: %v", gotErr)
				}
				return
			}
			if tt.wantErr {
				t.Fatal("packageArchiveURL() succeeded unexpectedly")
			}
			if got != tt.want {
				t.Errorf("packageArchiveURL() = %v, want %v", got, tt.want)
			}
		})
	}
}
