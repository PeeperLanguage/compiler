package registry

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	pathpkg "path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"compiler/pkg/manifest"
	"compiler/pkg/remotes"
	"compiler/pkg/semver"
)

const (
	maxPackageArchiveBytes   = 64 << 20
	maxPackageExtractedBytes = 256 << 20
	maxPackageEntries        = 50_000
	maxTagResponseBytes      = 4 << 20
	maxTagPages              = 100
	tagRequestTimeout        = 30 * time.Second
	archiveRequestTimeout    = 5 * time.Minute
)

func DownloadRemotePackage(httpClient *http.Client, cachePath, repoName, version string, devConfig *manifest.DevConfig) error {
	version = strings.TrimSpace(version)
	if _, err := semver.Parse(version); err != nil {
		return fmt.Errorf("invalid package version %q: %w", version, err)
	}
	modulePath, err := GetModulePath(cachePath, repoName, version)
	if err != nil {
		return err
	}
	if devConfig != nil && devConfig.MockRemote && devConfig.MockPath != "" {
		return downloadFromMock(modulePath, repoName, version, devConfig.MockPath)
	}
	ctx, cancel := context.WithTimeout(context.Background(), archiveRequestTimeout)
	defer cancel()
	return downloadFromGit(ctx, httpClient, modulePath, repoName, version)
}

func ListAvailableVersions(httpClient *http.Client, repoName string, devConfig *manifest.DevConfig) ([]string, error) {
	provider, repoPath, ok := remotes.Parse(repoName)
	if !ok {
		return nil, fmt.Errorf("unsupported remote host for %s", repoName)
	}
	if devConfig != nil && devConfig.MockRemote && devConfig.MockPath != "" {
		return listMockVersions(repoName, devConfig.MockPath)
	}
	ctx, cancel := context.WithTimeout(context.Background(), tagRequestTimeout)
	defer cancel()
	remaining := int64(maxTagResponseBytes)
	switch provider {
	case remotes.ProviderGitHub:
		return fetchGitHubVersions(ctx, httpClient, repoName, repoPath, &remaining)
	case remotes.ProviderGitLab:
		return fetchGitLabVersions(ctx, httpClient, repoName, repoPath, &remaining)
	case remotes.ProviderBitbucket:
		return fetchBitbucketVersions(ctx, httpClient, repoName, repoPath, &remaining)
	default:
		panic(fmt.Sprintf("unsupported remote provider %q", provider))
	}
}

func downloadFromGit(ctx context.Context, httpClient *http.Client, modulePath, repoName, version string) error {
	archiveURL, err := packageArchiveURL(repoName, version)
	if err != nil {
		return err
	}
	return stageModule(modulePath, func(dest string) error {
		archivePath, err := downloadFile(ctx, httpClient, archiveURL)
		if err != nil {
			return err
		}
		defer os.Remove(archivePath)
		return extractTarGz(archivePath, dest)
	})
}

func stageModule(dest string, populate func(string) error) error {
	if isModuleCached(dest) {
		return nil
	}
	if _, err := os.Lstat(dest); err == nil {
		if err := os.RemoveAll(dest); err != nil {
			return fmt.Errorf("remove incomplete module cache: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect module cache: %w", err)
	}
	parent := filepath.Dir(dest)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("create module cache parent: %w", err)
	}
	temp, err := os.MkdirTemp(parent, "."+filepath.Base(dest)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary module cache: %w", err)
	}
	defer os.RemoveAll(temp)
	if err := populate(temp); err != nil {
		return err
	}
	if _, err := manifest.Load(filepath.Join(temp, manifest.FileName)); err != nil {
		return fmt.Errorf("invalid package manifest: %w", err)
	}
	if err := os.Rename(temp, dest); err != nil {
		if isModuleCached(dest) {
			return nil
		}
		return fmt.Errorf("publish module cache: %w", err)
	}
	return nil
}

func packageArchiveURL(repoName, version string) (string, error) {
	provider, repoPath, ok := remotes.Parse(repoName)
	if !ok {
		return "", fmt.Errorf("unsupported remote host for %s", repoName)
	}
	switch provider {
	case remotes.ProviderGitHub:
		return fmt.Sprintf("https://github.com/%s/archive/refs/tags/%s.tar.gz", repoPath, version), nil
	case remotes.ProviderGitLab:
		repoBase := filepath.Base(repoPath)
		return fmt.Sprintf("https://gitlab.com/%s/-/archive/%s/%s-%s.tar.gz", repoPath, version, repoBase, version), nil
	case remotes.ProviderBitbucket:
		return fmt.Sprintf("https://bitbucket.org/%s/get/%s.tar.gz", repoPath, version), nil
	default:
		panic(fmt.Sprintf("unsupported remote provider %q", provider))
	}
}

func downloadFromMock(modulePath, repoName, version, mockBasePath string) error {
	mockBasePath, err := filepath.Abs(mockBasePath)
	if err != nil {
		return fmt.Errorf("resolve mock path: %w", err)
	}
	repoPath := remotes.StripProviderPrefix(repoName)
	packageName := filepath.Base(repoPath)
	packageDir := filepath.Dir(repoPath)
	versionedDir := packageName + "-" + version

	source := filepath.Join(mockBasePath, filepath.Dir(repoName), versionedDir)
	if _, err := os.Stat(source); os.IsNotExist(err) {
		source = filepath.Join(mockBasePath, packageDir, versionedDir)
	}
	if _, err := os.Stat(source); os.IsNotExist(err) {
		source = filepath.Join(mockBasePath, repoPath)
	}
	if _, err := os.Stat(source); err != nil {
		return fmt.Errorf("mock package not found for %s", repoName)
	}
	return stageModule(modulePath, func(dest string) error {
		return copyDir(source, dest)
	})
}

func listMockVersions(repoName, mockBasePath string) ([]string, error) {
	mockBasePath, err := filepath.Abs(mockBasePath)
	if err != nil {
		return nil, fmt.Errorf("resolve mock path: %w", err)
	}
	repoPath := remotes.StripProviderPrefix(repoName)
	baseDir := filepath.Join(mockBasePath, filepath.Dir(repoPath))
	packageName := filepath.Base(repoPath)

	entries, err := os.ReadDir(baseDir)
	if err != nil {
		return nil, fmt.Errorf("read mock directory: %w", err)
	}
	versions := make([]string, 0)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if after, ok := strings.CutPrefix(entry.Name(), packageName+"-"); ok {
			versions = append(versions, after)
		}
	}
	if len(versions) == 0 {
		return nil, fmt.Errorf("no mock versions found for %s", repoName)
	}
	return versions, nil
}

type versionTag struct {
	Name string `json:"name"`
}

type bitbucketTagResponse struct {
	Values []versionTag `json:"values"`
	Next   string       `json:"next"`
}

func getJSON(ctx context.Context, httpClient *http.Client, requestURL, statusLabel string, target any, remaining *int64) (http.Header, error) {
	if httpClient == nil {
		return nil, fmt.Errorf("registry client is not initialized")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, err
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s status %d", statusLabel, response.StatusCode)
	}
	if remaining == nil || *remaining <= 0 || response.ContentLength > *remaining {
		return nil, fmt.Errorf("%s response exceeds %d byte limit", statusLabel, maxTagResponseBytes)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, *remaining+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > *remaining {
		return nil, fmt.Errorf("%s response exceeds %d byte limit", statusLabel, maxTagResponseBytes)
	}
	*remaining -= int64(len(data))
	if err := json.Unmarshal(data, target); err != nil {
		return nil, err
	}
	return response.Header, nil
}

func collectVersionNames(repoName string, tags []versionTag) ([]string, error) {
	versions := make([]string, 0, len(tags))
	for _, tag := range tags {
		versions = append(versions, tag.Name)
	}
	if len(versions) == 0 {
		return nil, fmt.Errorf("no versions found for %s", repoName)
	}
	return versions, nil
}

func fetchGitHubVersions(ctx context.Context, httpClient *http.Client, repoName, repoPath string, remaining *int64) ([]string, error) {
	requestURL := fmt.Sprintf("https://api.github.com/repos/%s/tags?per_page=100", repoPath)
	var all []versionTag
	for range maxTagPages {
		var tags []versionTag
		header, err := getJSON(ctx, httpClient, requestURL, "github tags API", &tags, remaining)
		if err != nil {
			return nil, err
		}
		all = append(all, tags...)
		next, err := githubNextPage(header.Get("Link"))
		if err != nil {
			return nil, err
		}
		if next == "" {
			return collectVersionNames(repoName, all)
		}
		requestURL, err = checkedNextPage(requestURL, next)
		if err != nil {
			return nil, err
		}
	}
	return nil, fmt.Errorf("github tags API exceeds %d page limit", maxTagPages)
}

func githubNextPage(linkHeader string) (string, error) {
	for _, part := range strings.Split(linkHeader, ",") {
		part = strings.TrimSpace(part)
		if !strings.Contains(part, `rel="next"`) {
			continue
		}
		start := strings.IndexByte(part, '<')
		end := strings.IndexByte(part, '>')
		if start < 0 || end <= start+1 {
			return "", fmt.Errorf("invalid github pagination link %q", part)
		}
		return part[start+1 : end], nil
	}
	return "", nil
}

func fetchGitLabVersions(ctx context.Context, httpClient *http.Client, repoName, repoPath string, remaining *int64) ([]string, error) {
	requestURL := fmt.Sprintf("https://gitlab.com/api/v4/projects/%s/repository/tags?per_page=100", url.PathEscape(repoPath))
	var all []versionTag
	for range maxTagPages {
		var tags []versionTag
		header, err := getJSON(ctx, httpClient, requestURL, "gitlab tags API", &tags, remaining)
		if err != nil {
			return nil, err
		}
		all = append(all, tags...)
		nextPage := header.Get("X-Next-Page")
		if nextPage == "" {
			return collectVersionNames(repoName, all)
		}
		page, err := strconv.Atoi(nextPage)
		if err != nil || page <= 0 {
			return nil, fmt.Errorf("invalid gitlab next page %q", nextPage)
		}
		parsed, err := url.Parse(requestURL)
		if err != nil {
			return nil, err
		}
		query := parsed.Query()
		query.Set("page", strconv.Itoa(page))
		parsed.RawQuery = query.Encode()
		requestURL = parsed.String()
	}
	return nil, fmt.Errorf("gitlab tags API exceeds %d page limit", maxTagPages)
}

func fetchBitbucketVersions(ctx context.Context, httpClient *http.Client, repoName, repoPath string, remaining *int64) ([]string, error) {
	requestURL := fmt.Sprintf("https://api.bitbucket.org/2.0/repositories/%s/refs/tags?pagelen=100", repoPath)
	var all []versionTag
	for range maxTagPages {
		var payload bitbucketTagResponse
		if _, err := getJSON(ctx, httpClient, requestURL, "bitbucket tags API", &payload, remaining); err != nil {
			return nil, err
		}
		all = append(all, payload.Values...)
		if payload.Next == "" {
			return collectVersionNames(repoName, all)
		}
		next, err := checkedNextPage(requestURL, payload.Next)
		if err != nil {
			return nil, err
		}
		requestURL = next
	}
	return nil, fmt.Errorf("bitbucket tags API exceeds %d page limit", maxTagPages)
}

func checkedNextPage(current, next string) (string, error) {
	currentURL, err := url.Parse(current)
	if err != nil {
		return "", err
	}
	nextURL, err := url.Parse(next)
	if err != nil {
		return "", err
	}
	if nextURL.Scheme != currentURL.Scheme || nextURL.Host != currentURL.Host {
		return "", fmt.Errorf("pagination changed origin from %s to %s", currentURL.Host, nextURL.Host)
	}
	return nextURL.String(), nil
}

func downloadFile(ctx context.Context, httpClient *http.Client, requestURL string) (string, error) {
	if httpClient == nil {
		return "", fmt.Errorf("registry client is not initialized")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return "", err
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download failed with status %d", response.StatusCode)
	}
	if response.ContentLength > maxPackageArchiveBytes {
		return "", fmt.Errorf("package archive exceeds %d byte limit", maxPackageArchiveBytes)
	}

	tempFile, err := os.CreateTemp("", "peeper-download-*.tar.gz")
	if err != nil {
		return "", err
	}
	tempPath := tempFile.Name()
	remove := true
	defer func() {
		_ = tempFile.Close()
		if remove {
			_ = os.Remove(tempPath)
		}
	}()
	written, err := io.Copy(tempFile, io.LimitReader(response.Body, maxPackageArchiveBytes+1))
	if err != nil {
		return "", err
	}
	if written > maxPackageArchiveBytes {
		return "", fmt.Errorf("package archive exceeds %d byte limit", maxPackageArchiveBytes)
	}
	if err := tempFile.Close(); err != nil {
		return "", err
	}
	remove = false
	return tempPath, nil
}

func extractTarGz(archivePath, destPath string) error {
	archiveFile, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer archiveFile.Close()
	gzipReader, err := gzip.NewReader(archiveFile)
	if err != nil {
		return err
	}
	defer gzipReader.Close()
	destPath, err = filepath.Abs(destPath)
	if err != nil {
		return err
	}

	var extracted int64
	entries := 0
	tarReader := tar.NewReader(gzipReader)
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		entries++
		if entries > maxPackageEntries {
			return fmt.Errorf("package archive exceeds %d entry limit", maxPackageEntries)
		}
		target, ok, err := archiveTarget(destPath, header.Name)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if header.Size < 0 || header.Size > maxPackageExtractedBytes-extracted {
				return fmt.Errorf("package archive exceeds %d extracted byte limit", maxPackageExtractedBytes)
			}
			extracted += header.Size
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			mode := os.FileMode(header.Mode).Perm()
			if mode == 0 {
				mode = 0o644
			}
			outFile, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
			if err != nil {
				return err
			}
			if _, err := io.CopyN(outFile, tarReader, header.Size); err != nil {
				_ = outFile.Close()
				return err
			}
			if err := outFile.Close(); err != nil {
				return err
			}
			if err := os.Chmod(target, mode); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported archive entry %q with type %d", header.Name, header.Typeflag)
		}
	}
}

func archiveTarget(destPath, name string) (string, bool, error) {
	if strings.ContainsRune(name, '\x00') || strings.ContainsRune(name, '\\') {
		return "", false, fmt.Errorf("unsafe archive path %q", name)
	}
	cleaned := pathpkg.Clean(name)
	if pathpkg.IsAbs(cleaned) || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", false, fmt.Errorf("archive path escapes destination: %q", name)
	}
	relative := name
	if _, after, ok := strings.Cut(relative, "/"); ok {
		relative = after
	}
	relative = pathpkg.Clean(relative)
	if relative == "." {
		return "", false, nil
	}
	if pathpkg.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, "../") {
		return "", false, fmt.Errorf("archive path escapes destination: %q", name)
	}
	target := filepath.Join(destPath, filepath.FromSlash(relative))
	contained, err := filepath.Rel(destPath, target)
	if err != nil {
		return "", false, err
	}
	if contained == ".." || strings.HasPrefix(contained, ".."+string(filepath.Separator)) || filepath.IsAbs(contained) {
		return "", false, fmt.Errorf("archive path escapes destination: %q", name)
	}
	return target, true, nil
}

func copyDir(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, info.Mode()); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())
		if entry.IsDir() {
			if err := copyDir(srcPath, dstPath); err != nil {
				return err
			}
			continue
		}
		if err := copyFile(srcPath, dstPath); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()
	dstFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer dstFile.Close()
	if _, err := io.Copy(dstFile, srcFile); err != nil {
		return err
	}
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	return os.Chmod(dst, info.Mode())
}
