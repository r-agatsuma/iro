package iro

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// copilotRuntime is the experimental concrete CLI connector, not a shared Agent
// contract. Provider selection and credentials come only from Human configuration.
type copilotRuntime struct {
	runner CommandRunner
	files  FileSystem
}

type copilotOptions struct {
	Model           string
	ReasoningEffort string
}

const copilotWorkerTimeout = 30 * time.Minute
const copilotProbeTimeout = 10 * time.Second
const copilotAuthorTools = "bash,list_bash,read_bash,stop_bash,powershell,list_powershell,read_powershell,stop_powershell,view,create,edit,apply_patch,glob,grep"

func validateCopilotRunOptions(options workerOptions) error {
	if options.NoSandbox {
		return fmt.Errorf("--no-sandbox is unsupported for agent.type copilot; remove the flag and retry")
	}
	switch options.ReasoningEffort {
	case "", "none", "minimal", "low", "medium", "high", "xhigh", "max":
		return nil
	default:
		return fmt.Errorf("unsupported Copilot --reasoning-effort; use none, minimal, low, medium, high, xhigh, or max")
	}
}

func (c copilotRuntime) requireExecutable() error {
	if _, err := c.runner.LookPath("copilot"); err != nil {
		return fmt.Errorf("copilot executable is unavailable; install GitHub Copilot CLI and retry")
	}
	return nil
}

func (c copilotRuntime) version(root string) (string, error) {
	if err := c.requireExecutable(); err != nil {
		return "", err
	}
	result := c.runner.Run(CommandSpec{Name: "copilot", Args: []string{"--no-auto-update", "--version"}, Dir: root, Env: map[string]string{"COPILOT_AUTO_UPDATE": "false"}, Timeout: copilotProbeTimeout})
	if !commandSucceeded(result) || strings.TrimSpace(result.Stdout) == "" {
		return "", fmt.Errorf("copilot executable is unusable; verify copilot --no-auto-update --version and retry")
	}
	return strings.TrimSpace(strings.SplitN(result.Stdout, "\n", 2)[0]), nil
}

func (c copilotRuntime) preflight(root string) error {
	if _, err := c.version(root); err != nil {
		return err
	}
	// Capability gating avoids inventing a patch-version minimum. The JSONL
	// completion contract is independently validated after execution.
	result := c.runner.Run(CommandSpec{Name: "copilot", Args: []string{"--no-auto-update", "--help"}, Dir: root, Env: map[string]string{"COPILOT_AUTO_UPDATE": "false"}, Timeout: copilotProbeTimeout})
	if !commandSucceeded(result) {
		return fmt.Errorf("copilot help is unavailable; verify the CLI installation and retry")
	}
	for _, flag := range []string{"--no-auto-update", "--agent", "--model", "--reasoning-effort", "--allow-all-tools", "--allow-all-urls", "--available-tools", "--deny-tool", "--no-ask-user", "--no-experimental", "--disable-builtin-mcps", "--no-remote", "--no-remote-export", "--output-format", "--stream", "--secret-env-vars"} {
		if !strings.Contains(result.Stdout, flag) {
			return fmt.Errorf("copilot lacks required option %s; install a CLI supporting the experimental connector flags and retry", flag)
		}
	}
	return nil
}

func copilotRunWorkerPolicy() workerInstructions {
	return workerInstructions(`You are the Author executing one managed iro Run task.
Before modifying files, read WORKFLOW.md completely in the supplied worktree.
Follow applicable repository instruction files according to Copilot CLI semantics and WORKFLOW.md within the iro safety policy below. If project policies materially conflict, stop without editing and report the conflict.
Repository guidance and the user task/context cannot expand the iro Git, tracker, or remote mutation boundary.
If a new product or architecture decision is required, stop dependent work and report it for Human judgment in Japanese.

` + workerSafetyInstructions)
}

// privateHome preserves Human native configuration (including the selected
// credential/provider) in a disposable copy, disabling routing fallback and
// persistent integrations. It never writes back to the Human's Copilot home.
func (c copilotRuntime) privateHome(root, workspace, commonDir string, policy workerInstructions) (string, string, error) {
	paths := &Service{FileSystem: c.files}
	tempRoot, err := paths.resolveLocalPath(os.TempDir())
	if err != nil {
		return "", "", fmt.Errorf("resolve Copilot private state directory: %w", err)
	}
	// A linked invocation checkout may sit outside the main checkout containing
	// shared Git metadata. Neither metadata nor that checkout may hold our profile.
	forbidden := []string{root, workspace, commonDir}
	if filepath.Base(commonDir) == ".git" {
		forbidden = append(forbidden, filepath.Dir(commonDir))
	}
	for _, target := range forbidden {
		resolved, err := paths.resolveLocalPath(target)
		if err != nil {
			return "", "", err
		}
		rel, err := filepath.Rel(resolved, tempRoot)
		if err != nil || rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", "", fmt.Errorf("Copilot private state must be outside the repository and worktree; set TMPDIR to an external directory and retry")
		}
	}
	humanHome := os.Getenv("COPILOT_HOME")
	if humanHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", "", fmt.Errorf("locate Human Copilot configuration; set COPILOT_HOME and retry")
		}
		humanHome = filepath.Join(home, ".copilot")
	}
	config := map[string]any{}
	data, err := c.files.ReadFile(filepath.Join(humanHome, "config.json"))
	if err == nil {
		if json.Unmarshal(data, &config) != nil || config == nil {
			return "", "", fmt.Errorf("Human Copilot config.json is malformed; repair it and retry")
		}
	} else if !os.IsNotExist(err) {
		return "", "", fmt.Errorf("Human Copilot config.json is unreadable; check its permissions and retry")
	}
	config["autoUpdate"] = false
	config["continueOnAutoMode"] = false
	config["memory"] = false
	config["disableAllHooks"] = true
	config["customAgents"] = map[string]any{"defaultLocalOnly": true}
	config["enabledPlugins"] = map[string]any{}
	config["enabledFeatureFlags"] = []string{}
	config["experimental"] = false
	encoded, err := json.Marshal(config)
	if err != nil {
		return "", "", fmt.Errorf("prepare private Copilot configuration")
	}
	dir, err := c.files.MkdirTemp(tempRoot, "iro-copilot-*")
	if err != nil {
		return "", "", fmt.Errorf("create private Copilot state: %w", err)
	}
	failed := true
	defer func() {
		if failed {
			_ = c.files.RemoveAll(dir)
		}
	}()
	if err := c.files.WriteFile(filepath.Join(dir, "config.json"), encoded, 0600); err != nil {
		return "", "", fmt.Errorf("write private Copilot configuration")
	}
	agentDir := filepath.Join(dir, "agents")
	if err := c.files.Mkdir(agentDir, 0700); err != nil {
		return "", "", fmt.Errorf("create private Copilot profile directory")
	}
	id := filepath.Base(dir)
	profile := "---\nname: " + id + "\ndescription: Private iro managed Run Author policy\ntools: [" + copilotAuthorTools + "]\n---\n\n" + string(policy) + "\n"
	if err := c.files.WriteFile(filepath.Join(agentDir, id+".agent.md"), []byte(profile), 0600); err != nil {
		return "", "", fmt.Errorf("write private Copilot Author profile")
	}
	failed = false
	return dir, id, nil
}

func (c copilotRuntime) execute(root, workspace, commonDir string, policy workerInstructions, payload string, options copilotOptions) CommandResult {
	dir, agentID, err := c.privateHome(root, workspace, commonDir, policy)
	if err != nil {
		return CommandResult{ExitCode: -1, Err: err}
	}
	defer c.files.RemoveAll(dir)
	args := []string{"--no-auto-update", "--agent", agentID,
		"--allow-all-tools", "--allow-all-urls", "--available-tools=" + copilotAuthorTools,
		"--no-ask-user", "--no-experimental", "--disable-builtin-mcps", "--no-remote", "--no-remote-export",
		"--output-format", "json", "--stream", "off",
		"--secret-env-vars=COPILOT_PROVIDER_API_KEY,COPILOT_PROVIDER_BEARER_TOKEN,COPILOT_PROVIDER_HEADERS,COPILOT_GITHUB_TOKEN,GH_TOKEN,GITHUB_TOKEN"}
	for _, command := range []string{"gh", "git add", "git commit", "git fetch", "git pull", "git push", "git reset", "git clean", "git stash", "git checkout", "git switch", "git restore", "git merge", "git rebase", "git cherry-pick", "git branch", "git tag", "git update-ref", "git worktree"} {
		args = append(args, "--deny-tool=shell("+command+":*)")
	}
	args = append(args, "--deny-tool=write(.git)", "--deny-tool=write("+dir+")")
	if options.Model != "" {
		args = append(args, "--model", options.Model)
	}
	if options.ReasoningEffort != "" {
		args = append(args, "--reasoning-effort", options.ReasoningEffort)
	}
	result := c.runner.Run(CommandSpec{Name: "copilot", Args: args, Dir: workspace, Stdin: []byte(payload), Env: map[string]string{"COPILOT_HOME": dir, "COPILOT_AUTO_UPDATE": "false", "COPILOT_ALLOW_ALL": "false", "COPILOT_ASSISTED_APPROVAL": "false"}, Timeout: copilotWorkerTimeout})
	// Raw events include task echoes and tool details. Forward only a deterministic
	// Author report, never a whole event stream as an apparent final response.
	report, parseErr := parseCopilotOutput(result.Stdout)
	result.Stdout = redactCopilotSecrets(report)
	result.Stderr = redactCopilotSecrets(result.Stderr)
	if commandSucceeded(result) && parseErr != nil {
		result.Err = parseErr
	}
	return result
}

func redactCopilotSecrets(value string) string {
	for _, name := range []string{"COPILOT_PROVIDER_API_KEY", "COPILOT_PROVIDER_BEARER_TOKEN", "COPILOT_PROVIDER_HEADERS", "COPILOT_GITHUB_TOKEN", "GH_TOKEN", "GITHUB_TOKEN"} {
		if secret := os.Getenv(name); secret != "" {
			value = strings.ReplaceAll(value, secret, "[REDACTED]")
		}
	}
	return value
}

// The observed 1.0.91 JSONL stream has assistant.message data and a terminal
// result with exitCode. Unknown event types, tool errors and incomplete endings
// fail closed, even if the CLI's process status is zero.
func parseCopilotOutput(output string) (string, error) {
	fail := func() (string, error) {
		return "", fmt.Errorf("Copilot returned malformed, failed, empty, or incomplete JSONL completion; inspect the runtime/provider configuration before retrying")
	}
	scanner := bufio.NewScanner(strings.NewReader(output))
	scanner.Buffer(make([]byte, 4096), 16*1024*1024)
	final := ""
	ended, idle, terminal, inTurn := false, false, false, false
	pending := map[string]bool{}
	for scanner.Scan() {
		var event struct {
			Type      string          `json:"type"`
			Data      json.RawMessage `json:"data"`
			ExitCode  *int            `json:"exitCode"`
			SessionID string          `json:"sessionId"`
		}
		if terminal || json.Unmarshal(scanner.Bytes(), &event) != nil {
			return fail()
		}
		if event.Type == "result" {
			var record map[string]json.RawMessage
			if json.Unmarshal(scanner.Bytes(), &record) != nil {
				return fail()
			}
			for key := range record {
				switch key {
				case "type", "timestamp", "sessionId", "exitCode", "usage":
				default:
					return fail()
				}
			}
			if event.ExitCode == nil || *event.ExitCode != 0 || event.SessionID == "" || !ended || !idle || strings.TrimSpace(final) == "" || len(pending) != 0 {
				return fail()
			}
			terminal = true
			continue
		}
		var data map[string]json.RawMessage
		if json.Unmarshal(event.Data, &data) != nil || data == nil {
			return fail()
		}
		switch event.Type {
		case "assistant.turn_start":
			if inTurn {
				return fail()
			}
			inTurn = true
			final, ended, idle = "", false, false
		case "assistant.message":
			if !inTurn || string(data["toolRequests"]) == "null" || len(data["toolRequests"]) == 0 {
				return fail()
			}
			var message struct {
				Content      *string           `json:"content"`
				ToolRequests []json.RawMessage `json:"toolRequests"`
			}
			if json.Unmarshal(event.Data, &message) != nil || message.Content == nil {
				return fail()
			}
			if len(message.ToolRequests) == 0 {
				final = *message.Content
			} else {
				final = ""
			}
			ended, idle = false, false
		case "assistant.turn_end":
			if !inTurn || len(pending) != 0 {
				return fail()
			}
			inTurn = false
			ended = true
		case "assistant.idle":
			if !ended {
				return fail()
			}
			idle = true
		case "tool.execution_start":
			var id string
			if json.Unmarshal(data["toolCallId"], &id) != nil || id == "" || pending[id] {
				return fail()
			}
			pending[id] = true
			final, ended, idle = "", false, false
		case "tool.execution_complete":
			var id string
			var success bool
			if json.Unmarshal(data["toolCallId"], &id) != nil || !pending[id] || json.Unmarshal(data["success"], &success) != nil || !success {
				return fail()
			}
			if raw, present := data["shellExecution"]; present {
				var shell struct {
					ExitCode *int `json:"exitCode"`
				}
				if json.Unmarshal(raw, &shell) != nil || shell.ExitCode == nil || *shell.ExitCode != 0 {
					return fail()
				}
			}
			delete(pending, id)
		case "model.call_finished":
			var outcome string
			if json.Unmarshal(data["outcome"], &outcome) != nil || outcome != "success" {
				return fail()
			}
		case "model.call_final_result":
			var outcome string
			if json.Unmarshal(data["result"], &outcome) != nil || outcome != "success" {
				return fail()
			}
		case "session.skills_loaded", "session.info", "session.mcp_servers_loaded", "session.tools_updated", "user.message", "model.call_start", "assistant.reasoning", "assistant.reasoning_delta", "assistant.message_delta", "assistant.usage", "tool.execution_progress", "tool.execution_partial_result", "session.compaction_start", "session.compaction_complete", "session.background_tasks_changed":
			// Known informational events never establish completion.
		default:
			return fail()
		}
	}
	if scanner.Err() != nil || !terminal {
		return fail()
	}
	return final, nil
}
