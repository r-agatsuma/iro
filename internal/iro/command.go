package iro

import (
	"bytes"
	"errors"
	"os/exec"
)

// CommandSpec describes one external command invocation.
type CommandSpec struct {
	Name  string
	Args  []string
	Dir   string
	Stdin []byte
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
	cmd := exec.Command(spec.Name, spec.Args...)
	cmd.Dir = spec.Dir
	cmd.Stdin = bytes.NewReader(spec.Stdin)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()

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
