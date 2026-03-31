package shell

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"time"
)

// Result holds the output of a command execution.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// Run executes a command with a timeout and returns the result.
func Run(timeout time.Duration, name string, args ...string) (*Result, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	r := &Result{
		Stdout: stdout.String(),
		Stderr: stderr.String(),
	}

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			r.ExitCode = exitErr.ExitCode()
		} else {
			r.ExitCode = -1
		}
		return r, fmt.Errorf("%s: %w\n%s", name, err, r.Stderr)
	}
	return r, nil
}

// RunSimple executes a command and returns combined output or error.
func RunSimple(name string, args ...string) (string, error) {
	r, err := Run(5*time.Minute, name, args...)
	if err != nil {
		return "", err
	}
	return r.Stdout, nil
}
