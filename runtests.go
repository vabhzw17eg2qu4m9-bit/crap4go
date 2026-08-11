package main

import (
	"os"
	"os/exec"
)

// RunTests executes "go test ./... -coverprofile=coverage.out -covermode=atomic"
// in root, wiring stdout and stderr through. Returns the command's error on a
// non-zero exit so callers can surface it.
func RunTests(root string) error {
	cmd := exec.Command("go", "test", "./...", "-coverprofile=coverage.out", "-covermode=atomic")
	cmd.Dir = root
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
