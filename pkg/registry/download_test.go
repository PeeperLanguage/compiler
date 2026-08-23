package registry

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"compiler/pkg/manifest"
)

type archiveEntry struct {
	name     string
	typeflag byte
	content  string
}

func writeTestArchive(t *testing.T, entries []archiveEntry) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "package.tar.gz")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)
	for _, entry := range entries {
		header := &tar.Header{
			Name:     entry.name,
			Mode:     0o644,
			Size:     int64(len(entry.content)),
			Typeflag: entry.typeflag,
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write([]byte(entry.content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

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

func TestExtractTarGzRejectsUnsafeEntries(t *testing.T) {
	tests := []struct {
		name  string
		entry archiveEntry
	}{
		{name: "parent traversal", entry: archiveEntry{name: "root/../../escaped", typeflag: tar.TypeReg, content: "bad"}},
		{name: "absolute path", entry: archiveEntry{name: "/etc/passwd", typeflag: tar.TypeReg, content: "bad"}},
		{name: "symbolic link", entry: archiveEntry{name: "root/link", typeflag: tar.TypeSymlink}},
		{name: "hard link", entry: archiveEntry{name: "root/link", typeflag: tar.TypeLink}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			dest := filepath.Join(root, "cache", "module")
			archive := writeTestArchive(t, []archiveEntry{test.entry})
			if err := extractTarGz(archive, dest); err == nil {
				t.Fatalf("unsafe archive entry accepted")
			}
			if _, err := os.Stat(filepath.Join(root, "escaped")); !os.IsNotExist(err) {
				t.Fatalf("archive wrote outside destination: %v", err)
			}
		})
	}
}

func TestExtractTarGzRequiresOneArchiveRoot(t *testing.T) {
	tests := []struct {
		name    string
		entries []archiveEntry
	}{
		{
			name: "unwrapped file",
			entries: []archiveEntry{
				{name: "peeper.toml", typeflag: tar.TypeReg, content: "name = \"pkg\"\n"},
			},
		},
		{
			name: "mixed roots",
			entries: []archiveEntry{
				{name: "first/peeper.toml", typeflag: tar.TypeReg, content: "name = \"pkg\"\n"},
				{name: "second/src/pkg.peep", typeflag: tar.TypeReg, content: "pub fn Value() int { return 1 }\n"},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			archive := writeTestArchive(t, test.entries)
			if err := extractTarGz(archive, filepath.Join(t.TempDir(), "module")); err == nil || !strings.Contains(err.Error(), "archive root") {
				t.Fatalf("archive root error = %v", err)
			}
		})
	}
}

func TestExtractTarGzEnforcesEntryLimit(t *testing.T) {
	entries := make([]archiveEntry, maxPackageEntries+1)
	for i := range entries {
		entries[i] = archiveEntry{name: "root/", typeflag: tar.TypeDir}
	}
	archive := writeTestArchive(t, entries)
	if err := extractTarGz(archive, filepath.Join(t.TempDir(), "module")); err == nil || !strings.Contains(err.Error(), "entry limit") {
		t.Fatalf("entry limit error = %v", err)
	}
}

func TestExtractTarGzEnforcesExtractedSizeLimit(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "oversized.tar.gz")
	file, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)
	if err := tarWriter.WriteHeader(&tar.Header{
		Name:     "root/large",
		Mode:     0o644,
		Size:     maxPackageExtractedBytes + 1,
		Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err == nil {
		t.Fatal("incomplete oversized archive closed without error")
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := extractTarGz(archive, filepath.Join(t.TempDir(), "module")); err == nil || !strings.Contains(err.Error(), "extracted byte limit") {
		t.Fatalf("extracted size limit error = %v", err)
	}
}

func TestExtractTarGzWritesRegularFiles(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "module")
	archive := writeTestArchive(t, []archiveEntry{
		{name: "root/", typeflag: tar.TypeDir},
		{name: "root/peeper.toml", typeflag: tar.TypeReg, content: "name = \"pkg\"\nbuild = \"lib\"\n"},
		{name: "root/src/pkg.peep", typeflag: tar.TypeReg, content: "pub fn Value() int { return 1 }\n"},
	})
	if err := extractTarGz(archive, dest); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dest, "src", "pkg.peep"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "fn Value") {
		t.Fatalf("unexpected extracted source: %q", data)
	}
	if _, err := os.Stat(filepath.Join(dest, "root")); !os.IsNotExist(err) {
		t.Fatalf("archive root directory was not stripped: %v", err)
	}
}

func TestListAvailableVersionsFollowsProviderPagination(t *testing.T) {
	tests := []struct {
		name         string
		repo         string
		firstBody    string
		secondBody   string
		setNext      func(http.Header)
		secondURLHas string
	}{
		{
			name:       "github",
			repo:       "github.com/acme/pkg",
			firstBody:  `[{"name":"v1.0.0"}]`,
			secondBody: `[{"name":"v1.1.0"}]`,
			setNext: func(header http.Header) {
				header.Set("Link", `<https://api.github.com/repos/acme/pkg/tags?page=2>; rel="next"`)
			},
			secondURLHas: "page=2",
		},
		{
			name:         "gitlab",
			repo:         "gitlab.com/acme/pkg",
			firstBody:    `[{"name":"v1.0.0"}]`,
			secondBody:   `[{"name":"v1.1.0"}]`,
			setNext:      func(header http.Header) { header.Set("X-Next-Page", "2") },
			secondURLHas: "page=2",
		},
		{
			name:         "bitbucket",
			repo:         "bitbucket.org/acme/pkg",
			firstBody:    `{"values":[{"name":"v1.0.0"}],"next":"https://api.bitbucket.org/2.0/repositories/acme/pkg/refs/tags?page=2"}`,
			secondBody:   `{"values":[{"name":"v1.1.0"}]}`,
			setNext:      func(http.Header) {},
			secondURLHas: "page=2",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requests := 0
			httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				requests++
				body := test.firstBody
				header := make(http.Header)
				if requests == 1 {
					test.setNext(header)
				} else {
					body = test.secondBody
					if !strings.Contains(request.URL.String(), test.secondURLHas) {
						t.Errorf("second request URL = %q", request.URL)
					}
				}
				return &http.Response{
					StatusCode:    http.StatusOK,
					Header:        header,
					Body:          io.NopCloser(strings.NewReader(body)),
					ContentLength: int64(len(body)),
					Request:       request,
				}, nil
			})}

			versions, err := ListAvailableVersions(httpClient, test.repo, nil)
			if err != nil {
				t.Fatal(err)
			}
			if requests != 2 || len(versions) != 2 || versions[0] != "v1.0.0" || versions[1] != "v1.1.0" {
				t.Fatalf("pagination result = %#v after %d request(s)", versions, requests)
			}
		})
	}
}

func TestDownloadFileRejectsOversizedArchive(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Body:          io.NopCloser(strings.NewReader("")),
			ContentLength: maxPackageArchiveBytes + 1,
			Request:       request,
		}, nil
	})}
	if _, err := downloadFile(context.Background(), httpClient, "https://github.com/acme/pkg/archive.tar.gz"); err == nil {
		t.Fatal("oversized archive accepted")
	}
}

func TestListAvailableVersionsRejectsOversizedResponse(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Header:        make(http.Header),
			Body:          io.NopCloser(strings.NewReader("")),
			ContentLength: maxTagResponseBytes + 1,
			Request:       request,
		}, nil
	})}
	if _, err := ListAvailableVersions(httpClient, "github.com/acme/pkg", nil); err == nil || !strings.Contains(err.Error(), "response exceeds") {
		t.Fatalf("tag response limit error = %v", err)
	}
}

func TestListAvailableVersionsRejectsCrossOriginPagination(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		header := make(http.Header)
		header.Set("Link", `<https://example.com/tags?page=2>; rel="next"`)
		body := `[{"name":"v1.0.0"}]`
		return &http.Response{
			StatusCode:    http.StatusOK,
			Header:        header,
			Body:          io.NopCloser(strings.NewReader(body)),
			ContentLength: int64(len(body)),
			Request:       request,
		}, nil
	})}
	if _, err := ListAvailableVersions(httpClient, "github.com/acme/pkg", nil); err == nil || !strings.Contains(err.Error(), "pagination changed origin") {
		t.Fatalf("pagination origin error = %v", err)
	}
}

func TestListAvailableVersionsRejectsTagWithoutName(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body := `[{"commit":{"sha":"abc"}}]`
		return &http.Response{
			StatusCode:    http.StatusOK,
			Header:        make(http.Header),
			Body:          io.NopCloser(strings.NewReader(body)),
			ContentLength: int64(len(body)),
			Request:       request,
		}, nil
	})}
	if _, err := ListAvailableVersions(httpClient, "github.com/acme/pkg", nil); err == nil || !strings.Contains(err.Error(), "tag name") {
		t.Fatalf("missing tag name error = %v", err)
	}
}

func TestDownloadRemotePackageRejectsNonStableVersion(t *testing.T) {
	if _, err := DownloadRemotePackage(http.DefaultClient, t.TempDir(), "github.com/acme/pkg", "tag?redirect", "", nil); err == nil {
		t.Fatal("non-stable package version accepted")
	}
}

func TestDownloadRemotePackageVerifiesBeforeReplacingCache(t *testing.T) {
	root := t.TempDir()
	cache := filepath.Join(root, "cache")
	mock := filepath.Join(root, "mock")
	dest, err := GetModulePath(cache, "github.com/acme/pkg", "v1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	writePackageTree(t, dest, "old")
	oldChecksum, err := ModuleChecksum(dest)
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(mock, "github.com", "acme", "pkg-v1.0.0")
	writePackageTree(t, source, "new")
	newChecksum, err := ModuleChecksum(source)
	if err != nil {
		t.Fatal(err)
	}
	dev := &manifest.DevConfig{MockRemote: true, MockPath: mock}

	if _, err := DownloadRemotePackage(http.DefaultClient, cache, "github.com/acme/pkg", "v1.0.0", oldChecksum, dev); err == nil {
		t.Fatal("moved package matched old checksum")
	}
	afterMismatch, err := ModuleChecksum(dest)
	if err != nil {
		t.Fatal(err)
	}
	if afterMismatch != oldChecksum {
		t.Fatalf("mismatch replaced cache: %q", afterMismatch)
	}

	uppercaseExpected := "sha256:" + strings.ToUpper(strings.TrimPrefix(newChecksum, "sha256:"))
	actual, err := DownloadRemotePackage(http.DefaultClient, cache, "github.com/acme/pkg", "v1.0.0", uppercaseExpected, dev)
	if err != nil {
		t.Fatal(err)
	}
	if actual != newChecksum {
		t.Fatalf("download checksum = %q, want %q", actual, newChecksum)
	}
	afterReplace, err := ModuleChecksum(dest)
	if err != nil {
		t.Fatal(err)
	}
	if afterReplace != newChecksum {
		t.Fatalf("replacement checksum = %q, want %q", afterReplace, newChecksum)
	}
}

func TestStageModulePublishesOnlyValidPackage(t *testing.T) {
	cache := t.TempDir()
	dest, err := GetModulePath(cache, "github.com/acme/pkg", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	checksum, err := stageModule(dest, "", func(temp string) error {
		return os.WriteFile(filepath.Join(temp, "peeper.toml"), []byte("name = \"pkg\"\nbuild = \"lib\"\n"), 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	if checksum == "" {
		t.Fatal("stageModule returned empty checksum")
	}
	published, err := ModuleChecksum(dest)
	if err != nil {
		t.Fatal(err)
	}
	if published != checksum {
		t.Fatalf("published checksum = %q, want %q", published, checksum)
	}
}

func TestStageModuleCleansFailedPublication(t *testing.T) {
	tests := []struct {
		name     string
		populate func(string) error
	}{
		{
			name: "populate failure",
			populate: func(string) error {
				return errors.New("download failed")
			},
		},
		{
			name: "invalid manifest",
			populate: func(temp string) error {
				return os.WriteFile(filepath.Join(temp, "peeper.toml"), []byte("invalid"), 0o644)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cache := t.TempDir()
			dest, err := GetModulePath(cache, "github.com/acme/pkg", "1.0.0")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := stageModule(dest, "", test.populate); err == nil {
				t.Fatal("invalid package published")
			}
			if _, err := os.Stat(dest); !os.IsNotExist(err) {
				t.Fatalf("destination remains after failure: %v", err)
			}
			temps, err := filepath.Glob(filepath.Join(filepath.Dir(dest), "."+filepath.Base(dest)+".tmp-*"))
			if err != nil {
				t.Fatal(err)
			}
			if len(temps) != 0 {
				t.Fatalf("temporary modules remain: %v", temps)
			}
		})
	}
}

func TestStageModulePreservesExistingCacheOnPopulateFailure(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "cache", "module")
	writePackageTree(t, dest, "old")
	before, err := ModuleChecksum(dest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stageModule(dest, "", func(string) error { return errors.New("download failed") }); err == nil {
		t.Fatal("stageModule ignored populate failure")
	}
	after, err := ModuleChecksum(dest)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("populate failure changed cache: %q != %q", after, before)
	}
}

func TestReplaceModuleCacheRollsBackFailedPublish(t *testing.T) {
	root := t.TempDir()
	dest := filepath.Join(root, "cache", "module")
	writePackageTree(t, dest, "old")
	before, err := ModuleChecksum(dest)
	if err != nil {
		t.Fatal(err)
	}

	if err := replaceModuleCache(filepath.Join(root, "missing-stage"), dest); err == nil {
		t.Fatal("replaceModuleCache accepted missing stage")
	}
	after, err := ModuleChecksum(dest)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("failed publish changed cache: %q != %q", after, before)
	}
}

func writePackageTree(t *testing.T, root, source string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "peeper.toml"), []byte("name = \"pkg\"\nbuild = \"lib\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "pkg.peep"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
}
