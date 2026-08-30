package main

import (
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"strings"

	"compiler/pkg/distribution"
)

func main() {
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: sign-release <release-manifest.json>")
		os.Exit(2)
	}
	privateKey, err := hex.DecodeString(strings.TrimSpace(os.Getenv("PEEPER_RELEASE_PRIVATE_KEY")))
	if err != nil {
		fmt.Fprintln(os.Stderr, "decode PEEPER_RELEASE_PRIVATE_KEY:", err)
		os.Exit(1)
	}
	data, err := os.ReadFile(flag.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, "read release manifest:", err)
		os.Exit(1)
	}
	signature, err := distribution.SignReleaseManifest(data, privateKey)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(string(signature))
}
