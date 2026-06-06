package main

import (
	"fmt"
	"os"
	"strings"
)

func main() {
	exe, _ := os.Executable()
	if strings.Contains(exe, "go-build") {
		fmt.Println("run compiled program instead of 'go run'")
		os.Exit(1)
	}

	if parseAndRunCommand(os.Args[1:]) {
		return
	}

	printUsageAndExit(2)
}
