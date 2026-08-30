package main

import (
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"compiler/internal/installer"
)

var (
	releaseManifestURL  = ""
	releasePublicKeyHex = ""
)

func main() {
	defaultRoot, err := defaultInstallRoot()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	installRoot := flag.String("install-dir", defaultRoot, "installation directory")
	manifestURL := flag.String("manifest-url", releaseManifestURL, "signed release manifest URL")
	publicKeyHex := flag.String("public-key", releasePublicKeyHex, "Ed25519 release public key in hexadecimal")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "installer accepts flags only")
		os.Exit(2)
	}
	publicKey, err := hex.DecodeString(*publicKeyHex)
	if err != nil || len(publicKey) == 0 {
		fmt.Fprintln(os.Stderr, "installer has no valid release public key")
		os.Exit(1)
	}
	if *manifestURL == "" {
		fmt.Fprintln(os.Stderr, "installer has no release manifest URL")
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	result, err := installer.Install(ctx, installer.Config{
		Client: &http.Client{Timeout: 30 * time.Minute}, ManifestURL: *manifestURL, SignatureURL: *manifestURL + ".sig",
		PublicKey: publicKey, HostOS: runtime.GOOS, HostArch: runtime.GOARCH, InstallRoot: *installRoot, Progress: os.Stderr,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("Installed Peeper %s in %s\n", result.Version, result.InstallRoot)
	fmt.Printf("Add %s to PATH.\n", filepath.Dir(result.Executable))
}

func defaultInstallRoot() (string, error) {
	if runtime.GOOS == "windows" {
		if localAppData := os.Getenv("LOCALAPPDATA"); localAppData != "" {
			return filepath.Join(localAppData, "Peeper"), nil
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home for installation: %w", err)
	}
	return filepath.Join(home, ".peeper"), nil
}
