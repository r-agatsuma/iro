package iro

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"time"
)

// CommandSpec describes one external command invocation.
type CommandSpec struct {
	Name  string
	Args  []string
	Dir   string
	Stdin []byte
	// Env overrides only named entries; other Human environment is inherited.
	Env     map[string]string
	Timeout time.Duration
}

// CommandResult contains the complete result of an external command.
type CommandResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
	Err      error
}

// CommandRunner is the testable boundary for subprocess execution.
type CommandRunner interface {
	LookPath(name string) (string, error)
	Run(spec CommandSpec) CommandResult
}

// OSCommandRunner invokes commands on the local operating system.
type OSCommandRunner struct{}

func NewOSCommandRunner() CommandRunner {
	return OSCommandRunner{}
}

func (OSCommandRunner) LookPath(name string) (string, error) {
	return exec.LookPath(name)
}

func (OSCommandRunner) Run(spec CommandSpec) CommandResult {
	ctx := context.Background()
	cancel := func() {}
	if spec.Timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, spec.Timeout)
	}
	defer cancel()
	cmd := exec.CommandContext(ctx, spec.Name, spec.Args...)
	if spec.Timeout > 0 {
		cmd.WaitDelay = 5 * time.Second
	}
	if len(spec.Env) > 0 {
		cmd.Env = make([]string, 0, len(os.Environ())+len(spec.Env))
		for _, entry := range os.Environ() {
			name, _, _ := strings.Cut(entry, "=")
			if _, overridden := spec.Env[name]; !overridden {
				cmd.Env = append(cmd.Env, entry)
			}
		}
		for name, value := range spec.Env {
			cmd.Env = append(cmd.Env, name+"="+value)
		}
	}
	cmd.Dir = spec.Dir
	cmd.Stdin = bytes.NewReader(spec.Stdin)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		err = ctx.Err()
	}

	result := CommandResult{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: 0,
		Err:      err,
	}
	if err == nil {
		return result
	}

	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		result.ExitCode = exitError.ExitCode()
	} else {
		result.ExitCode = -1
	}
	return result
}
