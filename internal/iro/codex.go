package iro

import (
	"fmt"
	"strconv"
)

// codexRuntime implements the concrete Codex CLI connector. It does not acquire
// tracker data, resolve Issue/PR relations, select worker policy, or own delivery.
type codexRuntime struct {
	runner CommandRunner
}

// codexOptions are requested Codex configuration, not resolved runtime facts.
// Operation mode and specification selectors never enter the connector.
type codexOptions struct {
	NoSandbox       bool
	Model           string
	ReasoningEffort string
}

func (c codexRuntime) requireExecutable() error {
	if _, err := c.runner.LookPath("codex"); err != nil {
		return fmt.Errorf("codex executable is unavailable; install it and retry")
	}
	return nil
}

func (c codexRuntime) checkAuth(root string) error {
	result := c.runner.Run(CommandSpec{Name: "codex", Args: []string{"login", "status"}, Dir: root})
	if !commandSucceeded(result) {
		return fmt.Errorf("codex authentication check failed")
	}
	return nil
}

func (c codexRuntime) preflight(root string) error {
	if err := c.requireExecutable(); err != nil {
		return err
	}
	return c.checkAuth(root)
}

// The CLI exposes no stable pre-invocation resolved model identity. Requested
// options are not provenance; do not infer from config or scrape CLI output.
func (c codexRuntime) resolvedModelIdentity() string {
	return "(unknown; not exposed by runtime)"
}

// execute transports iro control instructions separately from tracker-rendered
// task input. The prompt and stdin have no required tracker schema. The raw
// stdout/final response, stderr, exit status and error are returned unchanged;
// report validation, logging, forwarding and cleanup belong to the operation.
func (c codexRuntime) execute(workspace string, control workerInstructions, prompt, input string, options codexOptions) CommandResult {
	return c.runner.Run(CommandSpec{
		Name: "codex",
		Args: withCodexOptions(append(codexWorkerArgs(workspace, options),
			"-c", "developer_instructions="+strconv.Quote(string(control)),
			"exec", "--ephemeral", prompt), options),
		Dir:   workspace,
		Stdin: []byte(input),
	})
}

func codexWorkerArgs(workspace string, options codexOptions) []string {
	sandbox := "workspace-write"
	if options.NoSandbox {
		sandbox = "danger-full-access"
	}
	args := []string{"--cd", workspace, "--sandbox", sandbox, "--ask-for-approval", "never"}
	if !options.NoSandbox {
		args = append(args, "-c", "sandbox_workspace_write.network_access=true")
	}
	return args
}

func withCodexModel(args []string, model string) []string {
	return withCodexOptions(args, codexOptions{Model: model})
}

func withCodexOptions(args []string, options codexOptions) []string {
	if options.Model == "" && options.ReasoningEffort == "" {
		return args
	}
	result := make([]string, 0, len(args)+4)
	for _, arg := range args {
		if arg == "exec" {
			if options.Model != "" {
				result = append(result, "--model", options.Model)
			}
			if options.ReasoningEffort != "" {
				result = append(result, "-c", "model_reasoning_effort="+strconv.Quote(options.ReasoningEffort))
			}
		}
		result = append(result, arg)
	}
	return result
}
