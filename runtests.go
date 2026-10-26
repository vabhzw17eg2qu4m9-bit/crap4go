package main

import (
	"io"
	"os"
	"os/exec"
)

// The `go test` invocation vocabulary shared by the commands that shell
// out to the test tool.
const (
	goTestSubcommand  = "test"
	goTestAllPackages = "./..."
)

// RunTests executes "go test ./... -coverprofile=coverage.out -covermode=atomic"
// in root, wiring stdout and stderr through. Returns the command's error on a
// non-zero exit so callers can surface it.
func RunTests(root string) error {
	return runGoTests(root,
		[]string{goTestSubcommand, goTestAllPackages, "-coverprofile=coverage.out", "-covermode=atomic"},
		nil, os.Stdout, os.Stderr)
}

// runGoTests executes `go` with args in dir, wiring stdout and stderr
// through. A non-nil env replaces the process environment. Returns the
// command's error on a non-zero exit.
func runGoTests(dir string, args, env []string, stdout, stderr io.Writer) error {
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	if env != nil {
		cmd.Env = env
	}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}
