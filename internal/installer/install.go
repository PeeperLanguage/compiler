package installer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"compiler/internal/target"
	"compiler/internal/toolchain"
	"compiler/pkg/distribution"
)

const (
	maxReleaseManifestBytes  = int64(2 << 20)
	maxReleaseSignatureBytes = int64(4 << 10)
	maxRedirects             = 5
)

type Config struct {
	Client       *http.Client
	ManifestURL  string
	SignatureURL string
	PublicKey    []byte
	HostOS       string
	HostArch     string
	InstallRoot  string
	// Progress receives human-readable download progress. Nil disables it.
	Progress io.Writer
}

type Result struct {
	Version     string
	InstallRoot string
	Executable  string
}

func Install(ctx context.Context, config Config) (Result, error) {
	if config.Client == nil {
		return Result{}, fmt.Errorf("installer HTTP client is unavailable")
	}
	host, err := target.New(config.HostOS, config.HostArch)
	if err != nil {
		return Result{}, err
	}
	installRoot, err := filepath.Abs(config.InstallRoot)
	if err != nil {
		return Result{}, fmt.Errorf("resolve installation root: %w", err)
	}
	if filepath.Dir(installRoot) == installRoot {
		return Result{}, fmt.Errorf("refusing filesystem root as installation root")
	}
	client, err := secureHTTPClient(config.Client)
	if err != nil {
		return Result{}, err
	}
	manifestData, err := downloadBytes(ctx, client, config.ManifestURL, maxReleaseManifestBytes, "release manifest")
	if err != nil {
		return Result{}, err
	}
	signatureData, err := downloadBytes(ctx, client, config.SignatureURL, maxReleaseSignatureBytes, "release signature")
	if err != nil {
		return Result{}, err
	}
	manifest, components, err := distribution.VerifyReleaseManifest(manifestData, signatureData, config.PublicKey, host.OS, host.Arch)
	if err != nil {
		return Result{}, err
	}
	parent := filepath.Dir(installRoot)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return Result{}, fmt.Errorf("create installation parent: %w", err)
	}
	transaction, err := os.MkdirTemp(parent, ".peeper-install-*")
	if err != nil {
		return Result{}, fmt.Errorf("create installation transaction: %w", err)
	}
	defer os.RemoveAll(transaction)
	payload := filepath.Join(transaction, "payload")
	if err := os.Mkdir(payload, 0o755); err != nil {
		return Result{}, fmt.Errorf("create installation staging root: %w", err)
	}
	totalSize := int64(0)
	for _, component := range components {
		totalSize += component.Size
	}
	progress := newProgressWriter(config.Progress, "components", totalSize)
	archives := make([]string, len(components))
	downloadErrs := make([]error, len(components))
	downloadCtx, cancelDownloads := context.WithCancel(ctx)
	defer cancelDownloads()
	var downloads sync.WaitGroup
	for i, component := range components {
		downloads.Add(1)
		go func(i int, component distribution.ReleaseComponent) {
			defer downloads.Done()
			archivePath, err := downloadComponent(downloadCtx, client, transaction, component, progress)
			archives[i], downloadErrs[i] = archivePath, err
			if err != nil {
				cancelDownloads()
			}
		}(i, component)
	}
	downloads.Wait()
	for _, err := range downloadErrs {
		if err != nil {
			return Result{}, err
		}
	}
	if progress != nil {
		progress.finish()
	}
	for i, component := range components {
		expected := distribution.Metadata{Kind: component.Kind, ID: component.ID, Version: component.Version, OS: component.OS, Arch: component.Arch}
		if _, err := distribution.ExtractPack(archives[i], component.Format, payload, expected); err != nil {
			return Result{}, fmt.Errorf("extract component %q: %w", component.ID, err)
		}
	}
	if err := validateStagedInstallation(payload, host); err != nil {
		return Result{}, err
	}
	if err := activateInstallation(payload, installRoot); err != nil {
		return Result{}, err
	}
	executable := filepath.Join(installRoot, "bin", "peeper"+target.ExecutableExt(host.OS))
	return Result{Version: manifest.Version, InstallRoot: installRoot, Executable: executable}, nil
}

func secureHTTPClient(source *http.Client) (*http.Client, error) {
	if source == nil {
		return nil, fmt.Errorf("installer HTTP client is unavailable")
	}
	client := *source
	priorRedirectCheck := source.CheckRedirect
	client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) >= maxRedirects {
			return fmt.Errorf("installer download exceeded %d redirects", maxRedirects)
		}
		if request.URL.Scheme != "https" || request.URL.Host == "" || request.URL.User != nil {
			return fmt.Errorf("installer redirect uses unsafe URL")
		}
		if priorRedirectCheck != nil {
			return priorRedirectCheck(request, via)
		}
		return nil
	}
	return &client, nil
}

func downloadBytes(ctx context.Context, client *http.Client, requestURL string, limit int64, label string) ([]byte, error) {
	if err := validateHTTPSURL(requestURL); err != nil {
		return nil, fmt.Errorf("%s URL: %w", label, err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create %s request: %w", label, err)
	}
	request.Header.Set("Accept-Encoding", "identity")
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", label, err)
	}
	defer response.Body.Close()
	if response.Request.URL.Scheme != "https" {
		return nil, fmt.Errorf("download %s ended at unsafe URL", label)
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s returned HTTP %d", label, response.StatusCode)
	}
	if response.ContentLength > limit {
		return nil, fmt.Errorf("%s exceeds %d byte limit", label, limit)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", label, err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s exceeds %d byte limit", label, limit)
	}
	return data, nil
}

func downloadComponent(ctx context.Context, client *http.Client, transaction string, component distribution.ReleaseComponent, progress *progressWriter) (string, error) {
	if err := validateHTTPSURL(component.URL); err != nil {
		return "", fmt.Errorf("component %q URL: %w", component.ID, err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, component.URL, nil)
	if err != nil {
		return "", fmt.Errorf("create component %q request: %w", component.ID, err)
	}
	request.Header.Set("Accept-Encoding", "identity")
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("download component %q: %w", component.ID, err)
	}
	defer response.Body.Close()
	if response.Request.URL.Scheme != "https" {
		return "", fmt.Errorf("component %q download ended at unsafe URL", component.ID)
	}
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("component %q download returned HTTP %d", component.ID, response.StatusCode)
	}
	if response.ContentLength >= 0 && response.ContentLength != component.Size {
		return "", fmt.Errorf("component %q content length does not match manifest", component.ID)
	}
	archive, err := os.CreateTemp(transaction, "."+component.ID+"-*"+component.Format.Extension())
	if err != nil {
		return "", fmt.Errorf("create component %q download: %w", component.ID, err)
	}
	archivePath := archive.Name()
	hash := sha256.New()
	destination := io.Writer(io.MultiWriter(archive, hash))
	if progress != nil {
		destination = progress.writer(destination)
	}
	written, copyErr := io.Copy(destination, io.LimitReader(response.Body, component.Size+1))
	closeErr := archive.Close()
	if copyErr != nil {
		return "", fmt.Errorf("download component %q: %w", component.ID, copyErr)
	}
	if closeErr != nil {
		return "", fmt.Errorf("close component %q download: %w", component.ID, closeErr)
	}
	if written != component.Size {
		return "", fmt.Errorf("component %q size does not match manifest", component.ID)
	}
	if hex.EncodeToString(hash.Sum(nil)) != component.SHA256 {
		return "", fmt.Errorf("component %q SHA-256 does not match manifest", component.ID)
	}
	return archivePath, nil
}

// progressWriter reports streamed download progress as one in-place terminal
// line, throttled so terminals and CI logs are not flooded. It is safe for
// concurrent component downloads, which share one aggregate report.
type progressWriter struct {
	destination io.Writer
	label       string
	total       int64
	mutex       sync.Mutex
	written     int64
	lastReport  time.Time
}

func newProgressWriter(destination io.Writer, label string, total int64) *progressWriter {
	if destination == nil {
		return nil
	}
	return &progressWriter{destination: destination, label: label, total: total, lastReport: time.Now()}
}

func (p *progressWriter) writer(destination io.Writer) io.Writer {
	return writerFunc(func(chunk []byte) (int, error) {
		if _, err := p.Write(chunk); err != nil {
			return 0, err
		}
		return destination.Write(chunk)
	})
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(chunk []byte) (int, error) { return f(chunk) }

func (p *progressWriter) Write(chunk []byte) (int, error) {
	p.mutex.Lock()
	p.written += int64(len(chunk))
	report := time.Since(p.lastReport) >= 200*time.Millisecond
	if report {
		p.lastReport = time.Now()
	}
	written := p.written
	p.mutex.Unlock()
	if report {
		p.render(written)
	}
	return len(chunk), nil
}

func (p *progressWriter) finish() {
	p.mutex.Lock()
	written := p.written
	p.mutex.Unlock()
	p.render(written)
	fmt.Fprintln(p.destination)
}

func (p *progressWriter) render(written int64) {
	if p.total > 0 {
		fmt.Fprintf(p.destination, "\rDownloading %s: %.1f/%.1f MB (%d%%)   ",
			p.label, float64(written)/(1<<20), float64(p.total)/(1<<20), written*100/p.total)
		return
	}
	fmt.Fprintf(p.destination, "\rDownloading %s: %.1f MB   ", p.label, float64(written)/(1<<20))
}

func validateHTTPSURL(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return fmt.Errorf("unsafe HTTPS URL")
	}
	return nil
}

func validateStagedInstallation(root string, host target.Info) error {
	executable := filepath.Join(root, "bin", "peeper"+target.ExecutableExt(host.OS))
	info, err := os.Stat(executable)
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("staged compiler executable is missing")
	}
	if host.OS != "windows" && info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("staged compiler is not executable")
	}
	if info, err := os.Stat(filepath.Join(root, "libs", "core")); err != nil || !info.IsDir() {
		return fmt.Errorf("staged core library is missing")
	}
	profile, err := toolchain.Load(filepath.Join(root, "toolchains", "native", "profile.json"), root, host)
	if err != nil {
		return fmt.Errorf("validate staged toolchain profile: %w", err)
	}
	if profile.RuntimeABI != toolchain.RuntimeABIVersion || profile.RuntimeArchive == "" {
		return fmt.Errorf("staged toolchain profile has incompatible runtime ABI")
	}
	for _, required := range [][2]string{{"compiler", profile.ClangPath}, {"linker", profile.LinkerPath}, {"runtime", profile.RuntimeArchive}} {
		if info, err := os.Stat(required[1]); err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("staged %s is missing", required[0])
		}
	}
	if profile.Sysroot != "" {
		if info, err := os.Stat(profile.Sysroot); err != nil || !info.IsDir() {
			return fmt.Errorf("staged sysroot is missing")
		}
	}
	return nil
}

func activateInstallation(staged, destination string) error {
	if _, err := os.Lstat(destination); os.IsNotExist(err) {
		if err := os.Rename(staged, destination); err != nil {
			return fmt.Errorf("activate installation: %w", err)
		}
		return nil
	} else if err != nil {
		return fmt.Errorf("inspect existing installation: %w", err)
	}
	backup, err := os.MkdirTemp(filepath.Dir(destination), ".peeper-backup-*")
	if err != nil {
		return fmt.Errorf("reserve installation backup: %w", err)
	}
	if err := os.Remove(backup); err != nil {
		return fmt.Errorf("prepare installation backup: %w", err)
	}
	if err := os.Rename(destination, backup); err != nil {
		return fmt.Errorf("backup existing installation: %w", err)
	}
	if err := os.Rename(staged, destination); err != nil {
		activateErr := fmt.Errorf("activate installation: %w", err)
		if rollbackErr := os.Rename(backup, destination); rollbackErr != nil {
			return errors.Join(activateErr, fmt.Errorf("restore existing installation: %w", rollbackErr))
		}
		return activateErr
	}
	if err := os.RemoveAll(backup); err != nil {
		return fmt.Errorf("remove installation backup: %w", err)
	}
	return nil
}
