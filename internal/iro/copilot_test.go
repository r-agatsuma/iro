package iro

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

const copilotHelpForTest = `--no-auto-update --agent --model --reasoning-effort --allow-all-tools --allow-all-urls --available-tools --deny-tool --no-ask-user --disable-builtin-mcps --no-remote --no-remote-export --output-format --stream --secret-env-vars --no-experimental`

func copilotCompletionForTest(report string) string {
	message, _ := json.Marshal(map[string]any{"type": "assistant.message", "data": map[string]any{"content": report, "toolRequests": []any{}}})
	return `{"type":"assistant.turn_start","data":{}}` + "\n" + string(message) + "\n" + `{"type":"assistant.turn_end","data":{}}` + "\n" + `{"type":"assistant.idle","data":{}}` + "\n" + `{"type":"result","exitCode":0,"sessionId":"disposable"}` + "\n"
}

func copilotToolCompletionForTest(toolName, completion string) string {
	start, _ := json.Marshal(map[string]any{"type": "tool.execution_start", "data": map[string]any{"toolCallId": "x", "toolName": toolName}})
	return `{"type":"assistant.turn_start","data":{}}` + "\n" + string(start) + "\n" +
		`{"type":"tool.execution_complete","data":` + completion + "}\n" +
		`{"type":"assistant.turn_end","data":{}}` + "\n" + copilotCompletionForTest("完了")
}

func writeCopilotConfig(t *testing.T, root string) {
	t.Helper()
	data := strings.Replace(configTemplate, `type = "codex"`, `type = "copilot"`, 1)
	if err := os.WriteFile(filepath.Join(root, "iro.toml"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestConfigAcceptsExperimentalCopilotAndInitKeepsCodex(t *testing.T) {
	config, err := parseConfig([]byte(strings.Replace(configTemplate, `type = "codex"`, `type = "copilot"`, 1)))
	if err != nil || config.AgentType != "copilot" {
		t.Fatalf("config = %+v, %v", config, err)
	}
	root := t.TempDir()
	runner := &fakeCommandRunner{fn: func(spec CommandSpec) CommandResult {
		if spec.Name == "git" && reflect.DeepEqual(spec.Args, []string{"rev-parse", "--show-toplevel"}) {
			return CommandResult{Stdout: root}
		}
		t.Fatalf("unexpected init call: %+v", spec)
		return CommandResult{}
	}}
	if err := newTestService(t, runner, root).Init(io.Discard); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "iro.toml"))
	if err != nil {
		t.Fatal(err)
	}
	generated, err := parseConfig(data)
	if err != nil || generated.AgentType != "codex" {
		t.Fatalf("init = %+v, %v", generated, err)
	}
}

func TestCopilotUnsupportedOperationsAndOptionsRejectBeforeSideEffects(t *testing.T) {
	for _, args := range [][]string{{"review", "42"}, {"revise", "42"}, {"run", "123", "--no-sandbox"}, {"run", "123", "--reasoning-effort", "ultra"}} {
		t.Run(strings.Join(args, "/"), func(t *testing.T) {
			root := t.TempDir()
			writeProjectFiles(t, root)
			writeCopilotConfig(t, root)
			runner := &fakeCommandRunner{fn: func(spec CommandSpec) CommandResult {
				if spec.Name == "git" && reflect.DeepEqual(spec.Args, []string{"rev-parse", "--show-toplevel"}) {
					return CommandResult{Stdout: root}
				}
				t.Fatalf("rejection reached external operation: %+v", spec)
				return CommandResult{}
			}}
			service := newTestService(t, runner, root)
			var diagnostic strings.Builder
			if code := Execute(args, io.Discard, &diagnostic, service); code == 0 || !strings.Contains(diagnostic.String(), "copilot") && !strings.Contains(diagnostic.String(), "Copilot") {
				t.Fatalf("exit=%d diagnostic=%s", code, diagnostic.String())
			}
			for _, path := range []string{service.Dirs.DataRoot, service.Dirs.StateRoot} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("side effect: %s, %v", path, err)
				}
			}
		})
	}
}

func TestManagedCopilotRunUsesExistingDeliveryOwner(t *testing.T) {
	t.Setenv("COPILOT_HOME", t.TempDir())
	f := newManagedProducerFixture(t)
	writeCopilotConfig(t, f.root)
	var stages []string
	f.intercept = func(spec CommandSpec) (CommandResult, bool) {
		if spec.Name == "codex" {
			t.Fatal("Copilot Run invoked Codex")
		}
		if spec.Name == "copilot" {
			if containsString(spec.Args, "--version") {
				return CommandResult{Stdout: "GitHub Copilot CLI 1.0.92."}, true
			}
			if containsString(spec.Args, "--help") {
				return CommandResult{Stdout: copilotHelpForTest}, true
			}
			stages = append(stages, "worker")
			if spec.Dir != f.paths[0] || !strings.Contains(string(spec.Stdin), "Issue number: 123") || !strings.Contains(string(spec.Stdin), "Implement the task") {
				t.Fatalf("wrong worker context: %+v", spec)
			}
			return CommandResult{Stdout: copilotCompletionForTest("変更しました。テスト成功。")}, true
		}
		if spec.Name == "git" {
			if containsArgs(spec.Args, "worktree", "add") {
				stages = append(stages, "worktree")
			}
			if containsString([]string{"add", "commit", "push"}, spec.Args[0]) {
				stages = append(stages, spec.Args[0])
			}
		}
		if spec.Name == "gh" && containsArgs(spec.Args, "--method", "POST") {
			stages = append(stages, "PR")
		}
		if spec.Name == "gh" && containsArgs(spec.Args, "pr", "comment") {
			stages = append(stages, "report")
			if report := spec.Args[len(spec.Args)-1]; !strings.Contains(report, "変更しました。テスト成功。") || strings.Contains(report, "assistant.message") {
				t.Fatalf("raw stream forwarded: %s", report)
			}
		}
		return CommandResult{}, false
	}
	if err := f.service.Run(123, io.Discard); err != nil {
		t.Fatal(err)
	}
	want := []string{"worktree", "worker", "add", "commit", "push", "PR", "report"}
	if !reflect.DeepEqual(stages, want) {
		t.Fatalf("delivery ownership/order = %v", stages)
	}
	logs, _ := filepath.Glob(filepath.Join(f.service.Dirs.StateRoot, "runs", "*", "*.log"))
	if len(logs) != 1 {
		t.Fatal(logs)
	}
	data, err := os.ReadFile(logs[0])
	if err != nil || !strings.Contains(string(data), "copilot_exit_status: 0") || strings.Contains(string(data), "codex_exit_status:") {
		t.Fatalf("log = %s, %v", data, err)
	}
}

func TestCopilotWorkerFailuresNeverDeliverOrFallback(t *testing.T) {
	for name, result := range map[string]CommandResult{
		"provider":       {ExitCode: 1, Stderr: "provider denied"},
		"permission":     {Stdout: `{"type":"session.error","data":{"message":"permission denied"}}`},
		"empty":          {},
		"malformed":      {Stdout: "not JSON"},
		"timeout":        {Stdout: copilotCompletionForTest("response"), Err: context.DeadlineExceeded},
		"termination":    {ExitCode: -1, Err: context.Canceled},
		"incomplete":     {Stdout: `{"type":"assistant.message","data":{"content":"partial"}}`},
		"detached shell": {Stdout: copilotToolCompletionForTest("bash", `{"toolCallId":"x","success":true}`)},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("COPILOT_HOME", t.TempDir())
			f := newManagedProducerFixture(t)
			writeCopilotConfig(t, f.root)
			workers, comments := 0, 0
			f.intercept = func(spec CommandSpec) (CommandResult, bool) {
				if spec.Name == "codex" {
					t.Fatal("fallback")
				}
				if spec.Name == "copilot" {
					if containsString(spec.Args, "--version") {
						return CommandResult{Stdout: "GitHub Copilot CLI 1.0.91."}, true
					}
					if containsString(spec.Args, "--help") {
						return CommandResult{Stdout: copilotHelpForTest}, true
					}
					workers++
					return result, true
				}
				if spec.Name == "git" && containsString([]string{"add", "commit", "push"}, spec.Args[0]) || spec.Name == "gh" && containsArgs(spec.Args, "--method", "POST") {
					t.Fatalf("delivery after worker failure: %+v", spec)
				}
				if spec.Name == "gh" && containsArgs(spec.Args, "issue", "comment") {
					comments++
				}
				return CommandResult{}, false
			}
			if err := f.service.Run(123, io.Discard); err == nil {
				t.Fatal("worker failure accepted")
			}
			if workers != 1 || comments != 1 || len(f.paths) != 1 {
				t.Fatalf("workers=%d comments=%d paths=%v", workers, comments, f.paths)
			}
			if _, err := os.Stat(f.paths[0]); err != nil {
				t.Fatal("failed workspace was removed", err)
			}
		})
	}
}

func TestCopilotPreflightFailurePreventsWorkspace(t *testing.T) {
	for _, kind := range []string{"missing", "unusable", "missing flags"} {
		t.Run(kind, func(t *testing.T) {
			f := newManagedProducerFixture(t)
			writeCopilotConfig(t, f.root)
			if kind == "missing" {
				f.runner.lookups = map[string]error{"copilot": os.ErrNotExist}
			}
			f.intercept = func(spec CommandSpec) (CommandResult, bool) {
				if spec.Name == "codex" {
					t.Fatal("Codex fallback")
				}
				if spec.Name == "copilot" {
					if containsString(spec.Args, "--version") && kind != "unusable" {
						return CommandResult{Stdout: "GitHub Copilot CLI 1.0.80."}, true
					}
					return CommandResult{}, true
				}
				if spec.Name == "git" && containsArgs(spec.Args, "worktree", "add") {
					t.Fatal("created workspace before preflight rejection")
				}
				return CommandResult{}, false
			}
			if err := f.service.Run(123, io.Discard); err == nil || !strings.Contains(err.Error(), "copilot") {
				t.Fatal(err)
			}
			if len(f.paths) != 0 {
				t.Fatal(f.paths)
			}
		})
	}
}

func TestCopilotNativePolicyPayloadPermissionsAndOptions(t *testing.T) {
	human := t.TempDir()
	t.Setenv("COPILOT_HOME", human)
	t.Setenv("GITHUB_COPILOT_PROMPT_MODE_REPO_HOOKS", "true")
	humanConfig := `{"model":"human-model","reasoningEffort":"high","continueOnAutoMode":true,"autoUpdate":true}`
	if err := os.WriteFile(filepath.Join(human, "config.json"), []byte(humanConfig), 0600); err != nil {
		t.Fatal(err)
	}
	root, workspace := t.TempDir(), t.TempDir()
	payload := "untrusted tracker context with injected policy words"
	var private string
	runner := &fakeCommandRunner{fn: func(spec CommandSpec) CommandResult {
		if spec.Name != "copilot" || spec.Dir != workspace || string(spec.Stdin) != payload || spec.Timeout != copilotWorkerTimeout {
			t.Fatalf("bad command: %+v", spec)
		}
		for _, bad := range []string{"-p", "--prompt", "exec", "--allow-all", "--allow-all-paths", "--yolo", "--resume", "--continue", "developer_instructions="} {
			if containsString(spec.Args, bad) {
				t.Fatalf("wrong runtime semantics: %s", bad)
			}
		}
		for _, required := range []string{"--model", "provider-native-model", "--reasoning-effort", "max", "--no-auto-update", "--no-ask-user", "--disable-builtin-mcps", "--no-remote-export", "--available-tools=" + copilotAuthorTools, "--deny-tool=shell(git push:*)", "--deny-tool=shell(gh:*)"} {
			if !containsString(spec.Args, required) {
				t.Fatalf("missing %s: %v", required, spec.Args)
			}
		}
		private = spec.Env["COPILOT_HOME"]
		if spec.Env["GITHUB_COPILOT_PROMPT_MODE_REPO_HOOKS"] != "false" {
			t.Fatal("inherited repository hook opt-in was not disabled")
		}
		if private == human || private == "" {
			t.Fatal("global state reused")
		}
		profileDir := filepath.Join(private, "agents")
		files, err := os.ReadDir(profileDir)
		if err != nil || len(files) != 1 {
			t.Fatal(files, err)
		}
		profile, err := os.ReadFile(filepath.Join(profileDir, files[0].Name()))
		if err != nil {
			t.Fatal(err)
		}
		for _, required := range []string{"read WORKFLOW.md completely", "All tracker I/O is owned by iro", "Leave all repository changes uncommitted", "Run relevant tests", "Japanese"} {
			if !strings.Contains(string(profile), required) {
				t.Fatalf("policy lost %s", required)
			}
		}
		if strings.Contains(string(profile), payload) || strings.Contains(string(profile), "loaded by Codex") {
			t.Fatalf("policy/payload separation: %s", profile)
		}
		agentID := strings.TrimSuffix(files[0].Name(), ".agent.md")
		if !containsArgs(spec.Args, "--agent", agentID) {
			t.Fatal("private agent not explicitly selected")
		}
		data, _ := os.ReadFile(filepath.Join(private, "config.json"))
		var config map[string]any
		if json.Unmarshal(data, &config) != nil || config["model"] != "human-model" || config["continueOnAutoMode"] != false || config["autoUpdate"] != false || config["disableAllHooks"] != true {
			t.Fatalf("private native config: %s", data)
		}
		return CommandResult{Stdout: copilotCompletionForTest("日本語の報告")}
	}}
	c := copilotRuntime{runner: runner, files: NewOSFileSystem()}
	got := c.execute(root, workspace, filepath.Join(root, ".git"), copilotRunWorkerPolicy(), payload, copilotOptions{Model: "provider-native-model", ReasoningEffort: "max"})
	if !commandSucceeded(got) || got.Stdout != "日本語の報告" {
		t.Fatalf("result=%+v", got)
	}
	if _, err := os.Stat(private); !os.IsNotExist(err) {
		t.Fatalf("private profile retained: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(human, "config.json"))
	if string(data) != humanConfig {
		t.Fatal("Human configuration mutated")
	}
	if os.Getenv("GITHUB_COPILOT_PROMPT_MODE_REPO_HOOKS") != "true" {
		t.Fatal("Human repository hook environment mutated")
	}
	for _, effort := range []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"} {
		if err := validateCopilotRunOptions(workerOptions{ReasoningEffort: effort}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCopilotParserFailsClosed(t *testing.T) {
	valid := copilotCompletionForTest("完了")
	for name, stream := range map[string]string{
		"valid":                 valid,
		"empty response":        copilotCompletionForTest(" \n"),
		"missing terminal":      strings.Split(valid, `{"type":"result"`)[0],
		"missing exit code":     strings.Replace(valid, `"exitCode":0,`, "", 1),
		"terminal error":        strings.Replace(valid, `"exitCode":0`, `"exitCode":1`, 1),
		"after terminal":        valid + `{"type":"assistant.idle","data":{}}`,
		"unknown result schema": strings.Replace(valid, `"exitCode":0`, `"exitCode":0,"is_error":true`, 1),
		"missing start":         strings.Replace(valid, `{"type":"assistant.turn_start","data":{}}`+"\n", "", 1),
		"unknown event":         `{"type":"future.event","data":{}}` + "\n" + valid,
		"unknown schema":        `{"type":"assistant.message","data":{"content":9}}` + "\n" + valid,
		"tool denial":           `{"type":"tool.execution_start","data":{"toolCallId":"x","toolName":"bash"}}` + "\n" + `{"type":"tool.execution_complete","data":{"toolCallId":"x","success":false}}` + "\n" + valid,
		"shell failure":         `{"type":"tool.execution_start","data":{"toolCallId":"x","toolName":"bash"}}` + "\n" + `{"type":"tool.execution_complete","data":{"toolCallId":"x","success":true,"shellExecution":{"exitCode":9}}}` + "\n" + valid,
		"shell termination":     `{"type":"tool.execution_start","data":{"toolCallId":"x","toolName":"bash"}}` + "\n" + `{"type":"tool.execution_complete","data":{"toolCallId":"x","success":true,"shellExecution":{}}}` + "\n" + valid,
		"outstanding tool":      `{"type":"tool.execution_start","data":{"toolCallId":"x","toolName":"bash"}}` + "\n" + valid,
		"provider failure":      `{"type":"model.call_failure","data":{}}` + "\n" + valid,
		"failed model result":   `{"type":"model.call_final_result","data":{"result":"error"}}` + "\n" + valid,
	} {
		t.Run(name, func(t *testing.T) {
			report, err := parseCopilotOutput(stream)
			if name == "valid" {
				if err != nil || report != "完了" {
					t.Fatal(report, err)
				}
			} else if err == nil {
				t.Fatal("ambiguous stream accepted", report)
			}
		})
	}
}

func TestCopilotShellCompletionRequiresExitStatus(t *testing.T) {
	for _, toolName := range []string{"bash", "powershell"} {
		for _, shell := range []struct {
			name    string
			field   string
			success bool
		}{
			{"launch only", "", false},
			{"null execution", `,"shellExecution":null`, false},
			{"missing exit code", `,"shellExecution":{}`, false},
			{"null exit code", `,"shellExecution":{"exitCode":null}`, false},
			{"nonzero exit code", `,"shellExecution":{"exitCode":1}`, false},
			{"completed", `,"shellExecution":{"exitCode":0}`, true},
		} {
			t.Run(toolName+"/"+shell.name, func(t *testing.T) {
				stream := copilotToolCompletionForTest(toolName, `{"toolCallId":"x","success":true`+shell.field+`}`)
				report, err := parseCopilotOutput(stream)
				if shell.success {
					if err != nil || report != "完了" {
						t.Fatal(report, err)
					}
				} else if err == nil || report != "" {
					t.Fatalf("unconfirmed shell accepted: report=%q err=%v", report, err)
				}
			})
		}
	}
	// File and shell-management tools have no shellExecution of their own.
	for _, toolName := range []string{"view", "edit", "read_bash", "stop_bash", "read_powershell"} {
		t.Run(toolName, func(t *testing.T) {
			report, err := parseCopilotOutput(copilotToolCompletionForTest(toolName, `{"toolCallId":"x","success":true}`))
			if err != nil || report != "完了" {
				t.Fatal(report, err)
			}
		})
	}
	for _, toolName := range []string{"", "future_tool"} {
		if _, err := parseCopilotOutput(copilotToolCompletionForTest(toolName, `{"toolCallId":"x","success":true}`)); err == nil {
			t.Fatalf("unknown tool accepted: %q", toolName)
		}
	}
}

func TestCopilotConcurrentToolsPreserveShellIdentity(t *testing.T) {
	stream := `{"type":"assistant.turn_start","data":{}}
{"type":"tool.execution_start","data":{"toolCallId":"shell","toolName":"bash"}}
{"type":"tool.execution_start","data":{"toolCallId":"file","toolName":"edit"}}
{"type":"tool.execution_complete","data":{"toolCallId":"file","success":true}}
{"type":"tool.execution_complete","data":{"toolCallId":"shell","success":true}}
{"type":"assistant.turn_end","data":{}}
` + copilotCompletionForTest("完了")
	if report, err := parseCopilotOutput(stream); err == nil || report != "" {
		t.Fatal("unconfirmed concurrent shell accepted", report, err)
	}
	stream = strings.Replace(stream, `"toolCallId":"shell","success":true`, `"toolCallId":"shell","success":true,"shellExecution":{"exitCode":0}`, 1)
	if report, err := parseCopilotOutput(stream); err != nil || report != "完了" {
		t.Fatal(report, err)
	}
}

func TestCopilotPrivateProfileRejectsRepositoryTempDirectory(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TMPDIR", root)
	runner := &fakeCommandRunner{fn: func(spec CommandSpec) CommandResult { t.Fatalf("worker invoked: %+v", spec); return CommandResult{} }}
	c := copilotRuntime{runner: runner, files: NewOSFileSystem()}
	result := c.execute(root, t.TempDir(), filepath.Join(root, ".git"), copilotRunWorkerPolicy(), "task", copilotOptions{})
	if result.Err == nil {
		t.Fatal("profile created inside repository")
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatal(entries, err)
	}
}

func TestDoctorCopilotIsReadOnlyAndDoesNotRequireCodexOrEntitlement(t *testing.T) {
	root := t.TempDir()
	writeProjectFiles(t, root)
	writeCopilotConfig(t, root)
	runner := &fakeCommandRunner{lookups: map[string]error{"codex": os.ErrNotExist}}
	runner.fn = func(spec CommandSpec) CommandResult {
		if spec.Name == "codex" {
			t.Fatal("Doctor required unselected Codex")
		}
		if spec.Name == "copilot" {
			if containsString(spec.Args, "--version") {
				return CommandResult{Stdout: "GitHub Copilot CLI 1.0.91."}
			}
			if containsString(spec.Args, "--help") {
				return CommandResult{Stdout: copilotHelpForTest}
			}
			t.Fatalf("Doctor invoked Copilot auth/provider/worker: %+v", spec)
		}
		return standardFakeResult(spec, root, "", false, false)
	}
	var out strings.Builder
	if err := newTestService(t, runner, root).Doctor(&out); err != nil {
		t.Fatal(err, out.String())
	}
	for _, expected := range []string{"copilot executable path:", "copilot version: GitHub Copilot CLI 1.0.91.", "UNKNOWN: Copilot runtime/provider readiness", "GitHub-hosted Copilot service is unverified"} {
		if !strings.Contains(out.String(), expected) {
			t.Fatal(out.String())
		}
	}
	if strings.Contains(out.String(), "codex") {
		t.Fatal(out.String())
	}
}

func TestConfiguredCopilotLandRemainsAgentIndependent(t *testing.T) {
	f := newLandFixture(t)
	writeCopilotConfig(t, f.root)
	f.runner.lookups["copilot"] = os.ErrNotExist
	old := f.runner.fn
	f.runner.fn = func(spec CommandSpec) CommandResult {
		if spec.Name == "copilot" || spec.Name == "codex" {
			t.Fatal("Land invoked agent")
		}
		return old(spec)
	}
	if err := f.service.Land(42, io.Discard); err != nil {
		t.Fatal(err)
	}
	if f.mergeCalls != 1 {
		t.Fatal(f.mergeCalls)
	}
}

func TestLocalCommandsHaveNoCopilotDependency(t *testing.T) {
	for _, cleanup := range []bool{false, true} {
		f := newLocalLifecycleFixture(t)
		f.runner.lookups["copilot"] = os.ErrNotExist
		f.runner.lookups["codex"] = os.ErrNotExist
		var err error
		if cleanup {
			err = f.service.CleanupAll(io.Discard)
		} else {
			err = f.service.Status(io.Discard)
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, call := range f.runner.calls {
			if call.Name != "git" {
				t.Fatalf("local command dependency: %+v", call)
			}
		}
	}
}

func TestOSCommandRunnerTimeoutAndEnvironmentOverrides(t *testing.T) {
	t.Setenv("IRO_TEST_INHERITED", "inherited")
	runner := NewOSCommandRunner()
	result := runner.Run(CommandSpec{Name: "sh", Args: []string{"-c", `printf '%s:%s' "$IRO_TEST_INHERITED" "$IRO_TEST_OVERRIDE"`}, Env: map[string]string{"IRO_TEST_OVERRIDE": "value"}, Timeout: time.Second})
	if !commandSucceeded(result) || result.Stdout != "inherited:value" {
		t.Fatal(result)
	}
	result = runner.Run(CommandSpec{Name: "sh", Args: []string{"-c", "exec sleep 2"}, Timeout: 20 * time.Millisecond})
	if !errors.Is(result.Err, context.DeadlineExceeded) || commandSucceeded(result) {
		t.Fatal(result)
	}
}

func TestCopilotDisablesInheritedRepositoryExecutionOptIns(t *testing.T) {
	t.Setenv("COPILOT_HOME", t.TempDir())
	optIns := []string{
		"GITHUB_COPILOT_PROMPT_MODE_REPO_HOOKS",
		"GITHUB_COPILOT_PROMPT_MODE_EXTENSIONS",
		"GITHUB_COPILOT_PROMPT_MODE_WORKSPACE_MCP",
	}
	for _, name := range optIns {
		t.Setenv(name, "true")
	}
	runner := &fakeCommandRunner{fn: func(spec CommandSpec) CommandResult {
		// Exercise the actual environment merge at the process boundary, using
		// a local shell in place of Copilot and a valid CLI completion fixture.
		spec.Name = "sh"
		spec.Args = []string{"-c", `printf '%s\n' "$GITHUB_COPILOT_PROMPT_MODE_REPO_HOOKS" "$GITHUB_COPILOT_PROMPT_MODE_EXTENSIONS" "$GITHUB_COPILOT_PROMPT_MODE_WORKSPACE_MCP" >&2; cat`}
		spec.Stdin = []byte(copilotCompletionForTest("完了"))
		return NewOSCommandRunner().Run(spec)
	}}
	c := copilotRuntime{runner: runner, files: NewOSFileSystem()}
	result := c.execute(t.TempDir(), t.TempDir(), filepath.Join(t.TempDir(), ".git"), copilotRunWorkerPolicy(), "task", copilotOptions{})
	if !commandSucceeded(result) || result.Stdout != "完了" || result.Stderr != "false\nfalse\nfalse\n" {
		t.Fatalf("repository execution opt-ins reached child: %+v", result)
	}
	for _, name := range optIns {
		if os.Getenv(name) != "true" {
			t.Fatalf("Human environment changed: %s", name)
		}
	}
}

// Opt-in acceptance uses the installed CLI with a loopback BYOK provider only.
// No paid account, real provider credentials, GitHub calls or tracker mutation.
func TestCopilotLocalBYOKAcceptance(t *testing.T) {
	if os.Getenv("IRO_TEST_COPILOT_CLI") != "1" {
		t.Skip("set IRO_TEST_COPILOT_CLI=1 to exercise the installed CLI with a local BYOK provider")
	}
	t.Run("synchronous edit", func(t *testing.T) { testCopilotLocalBYOKAcceptance(t, false, "stop") })
	t.Run("detached shell rejected", func(t *testing.T) { testCopilotLocalBYOKAcceptance(t, true, "stop") })
	// These reasons are normalized away by CLI 1.0.91. They are fixture
	// provider inputs, never a second observation boundary in the connector.
	for _, reason := range []string{"content_filter", "length"} {
		t.Run("hidden provider finish reason/"+reason, func(t *testing.T) {
			testCopilotLocalBYOKAcceptance(t, false, reason)
		})
	}
}

func testCopilotLocalBYOKAcceptance(t *testing.T, detached bool, finishReason string) {
	t.Helper()
	for _, name := range []string{"COPILOT_PROVIDER_API_KEY", "COPILOT_PROVIDER_API_KEY_COMMAND", "COPILOT_PROVIDER_BEARER_TOKEN", "COPILOT_PROVIDER_HEADERS", "COPILOT_PROVIDER_WIRE_MODEL", "COPILOT_PROVIDER_MODEL_ID", "COPILOT_PROVIDER_WIRE_API", "COPILOT_PROVIDER_TRANSPORT", "COPILOT_GITHUB_TOKEN", "GH_TOKEN", "GITHUB_TOKEN"} {
		t.Setenv(name, "")
	}
	t.Setenv("COPILOT_HOME", t.TempDir())
	t.Setenv("COPILOT_OFFLINE", "true")
	t.Setenv("COPILOT_PROVIDER_TYPE", "openai")
	t.Setenv("COPILOT_MODEL", "iro-local-test")
	t.Setenv("GITHUB_COPILOT_PROMPT_MODE_REPO_HOOKS", "true")
	t.Setenv("GITHUB_COPILOT_PROMPT_MODE_EXTENSIONS", "true")
	t.Setenv("GITHUB_COPILOT_PROMPT_MODE_WORKSPACE_MCP", "true")
	root := t.TempDir()
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "WORKFLOW.md"), []byte("LOCAL_WORKFLOW_MARKER"), 0600); err != nil {
		t.Fatal(err)
	}
	// This inherited opt-in bypassed disableAllHooks in CLI 1.0.91. Each
	// lifecycle hook writes only a marker in the disposable fixture workspace.
	hookDir := filepath.Join(workspace, ".github", "hooks")
	if err := os.MkdirAll(hookDir, 0700); err != nil {
		t.Fatal(err)
	}
	hooks := map[string]any{}
	for _, event := range []string{"sessionStart", "preToolUse", "postToolUse", "sessionEnd"} {
		hooks[event] = []any{map[string]any{"type": "command", "bash": "printf 'hook ran\\n' >> hook-ran.txt", "cwd": workspace, "timeoutSec": 5}}
	}
	hookConfig, err := json.Marshal(map[string]any{"version": 1, "hooks": hooks})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hookDir, "acceptance.json"), hookConfig, 0600); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	calls := 0
	policySeen, taskSeen := false, false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		var request struct {
			Messages []struct {
				Role    string
				Content any
			}
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			http.Error(w, "invalid request", 400)
			return
		}
		for _, message := range request.Messages {
			content, _ := json.Marshal(message.Content)
			if message.Role == "system" && strings.Contains(string(content), "All tracker I/O is owned by iro") {
				policySeen = true
			}
			if message.Role == "user" && strings.Contains(string(content), "LOCAL_TASK_MARKER") {
				taskSeen = true
			}
		}
		calls++
		message := map[string]any{"role": "assistant", "content": "ローカル provider による検証に成功しました。"}
		if calls == 1 {
			arguments := map[string]any{"command": "cat WORKFLOW.md && printf 'fixture change' > result.txt", "description": "Read policy and edit a fixture", "timeout": 10000}
			if detached {
				arguments["mode"] = "async"
				arguments["detach"] = true
				arguments["command"] = `cat WORKFLOW.md; attempt=0; while [ ! -f release.txt ] && [ "$attempt" -lt 200 ]; do sleep 0.05; attempt=$((attempt+1)); done; printf 'fixture change' > result.txt; exit 1`
			}
			args, _ := json.Marshal(arguments)
			message = map[string]any{"role": "assistant", "content": "", "tool_calls": []any{map[string]any{"id": "local-edit", "type": "function", "function": map[string]any{"name": "bash", "arguments": string(args)}}}}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "local-response", "object": "chat.completion", "model": "iro-local-test", "choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": map[bool]string{true: "tool_calls", false: finishReason}[calls == 1]}}, "usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}})
	}))
	defer server.Close()
	t.Setenv("COPILOT_PROVIDER_BASE_URL", server.URL+"/v1")
	var raw string
	workerCalls := 0
	actualRunner := NewOSCommandRunner()
	capturing := &fakeCommandRunner{fn: func(spec CommandSpec) CommandResult {
		if len(spec.Stdin) > 0 {
			workerCalls++
			spec.Timeout = 30 * time.Second
		}
		result := actualRunner.Run(spec)
		if len(spec.Stdin) > 0 {
			raw = result.Stdout
		}
		return result
	}}
	c := copilotRuntime{runner: capturing, files: NewOSFileSystem()}
	if err := c.preflight(root); err != nil {
		t.Fatal(err)
	}
	if detached {
		// Keep the fixture shell unfinished until execute returns, then release
		// it even on test failure. It edits only this temporary workspace.
		defer func() {
			if err := os.WriteFile(filepath.Join(workspace, "release.txt"), nil, 0600); err != nil {
				t.Error(err)
				return
			}
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				if _, err := os.Stat(filepath.Join(workspace, "result.txt")); err == nil {
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
			t.Error("detached fixture did not finish after release")
		}()
	}
	result := c.execute(root, workspace, filepath.Join(root, ".git"), copilotRunWorkerPolicy(), "LOCAL_TASK_MARKER: Read WORKFLOW.md, edit result.txt, and return the Japanese report.", copilotOptions{Model: "iro-local-test", ReasoningEffort: "high"})
	if _, err := os.Stat(filepath.Join(workspace, "hook-ran.txt")); !os.IsNotExist(err) {
		t.Fatalf("repository hook executed despite connector hook disablement: %v", err)
	}
	if detached {
		launchOnly := false
		for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
			var event struct {
				Type string `json:"type"`
				Data struct {
					Success        bool            `json:"success"`
					ShellExecution json.RawMessage `json:"shellExecution"`
				} `json:"data"`
			}
			if json.Unmarshal([]byte(line), &event) == nil && event.Type == "tool.execution_complete" && event.Data.Success && len(event.Data.ShellExecution) == 0 {
				launchOnly = true
			}
		}
		if !launchOnly || result.ExitCode != 0 || result.Err == nil || result.Stdout != "" || !strings.Contains(raw, "検証に成功") {
			t.Fatalf("detached shell was not rejected after successful CLI completion: result=%+v; raw=%s", result, raw)
		}
		if _, err := os.Stat(filepath.Join(workspace, "result.txt")); !os.IsNotExist(err) {
			t.Fatal("detached fixture finished before release", err)
		}
	} else {
		if !commandSucceeded(result) || !strings.Contains(result.Stdout, "検証に成功") {
			t.Fatalf("result=%+v; raw=%s", result, raw)
		}
		data, err := os.ReadFile(filepath.Join(workspace, "result.txt"))
		if err != nil || string(data) != "fixture change" {
			t.Fatal(string(data), err)
		}
		if finishReason != "stop" && (strings.Contains(raw, "finish_reason") || strings.Contains(raw, finishReason)) {
			t.Fatalf("CLI exposed provider finish reason; reassess the observable completion contract: %s", raw)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	// CLI 1.0.91 may internally repeat a length-truncated provider request.
	// This must not become an iro retry or a second worker invocation.
	if workerCalls != 1 || calls < 2 || finishReason != "length" && calls != 2 || !policySeen || !taskSeen {
		t.Fatalf("workerCalls=%d providerCalls=%d policy=%t task=%t", workerCalls, calls, policySeen, taskSeen)
	}
}

func TestUnmanagedWorkersIgnoreCopilotProjectSelection(t *testing.T) {
	for _, operation := range []string{"run", "review", "revise"} {
		t.Run(operation, func(t *testing.T) {
			var service *Service
			var runner *fakeCommandRunner
			var root string
			var args []string
			switch operation {
			case "run":
				f := newUnmanagedFixture(t)
				service, runner, root = f.service, f.runner, f.root
				args = []string{"run", "123", "--unmanaged"}
			case "revise":
				f := newUnmanagedReviseFixture(t)
				service, runner, root = f.service, f.runner, f.root
				args = []string{"revise", "42", "--unmanaged", "--issue", "123"}
			case "review":
				root = t.TempDir()
				workspace := ""
				runner = &fakeCommandRunner{fn: func(spec CommandSpec) CommandResult {
					if spec.Name == "git" {
						if containsArgs(spec.Args, "worktree", "add") {
							workspace = spec.Args[3]
						}
						if spec.Args[0] == "remote" {
							return CommandResult{Stdout: "git@github.com:acme/iro.git"}
						}
						if spec.Args[0] == "symbolic-ref" {
							return CommandResult{ExitCode: 1}
						}
						if containsString(spec.Args, "--git-common-dir") {
							return CommandResult{Stdout: filepath.Join(root, ".git")}
						}
						if spec.Args[0] == "for-each-ref" {
							return CommandResult{}
						}
						if containsArgs(spec.Args, "worktree", "list") {
							return CommandResult{Stdout: worktreeRecord(root, "branch refs/heads/main") + worktreeRecord(workspace, "detached")}
						}
					}
					result := reviewFakeResult(spec, root, "review")
					result.Stdout = strings.ReplaceAll(result.Stdout, "contributor/iro", "acme/iro")
					return result
				}}
				service = newTestService(t, runner, root)
				args = []string{"review", "42", "--unmanaged", "--issue", "123"}
			}
			writeProjectFiles(t, root)
			writeCopilotConfig(t, root)
			service.FileSystem = unmanagedFileGuard{service.FileSystem, t}
			workers := 0
			previous := runner.fn
			runner.fn = func(spec CommandSpec) CommandResult {
				if spec.Name == "copilot" {
					t.Fatal("unmanaged selected Copilot")
				}
				if spec.Name == "codex" && containsString(spec.Args, "--ephemeral") {
					workers++
					if operation == "review" {
						return CommandResult{ExitCode: 1, Err: errors.New("stop after selection")}
					}
				}
				return previous(spec)
			}
			var diagnostic strings.Builder
			code := Execute(args, io.Discard, &diagnostic, service)
			if workers != 1 || operation != "review" && code != 0 {
				t.Fatalf("code=%d workers=%d diagnostic=%s", code, workers, diagnostic.String())
			}
		})
	}
	for _, operation := range []string{"run", "review", "revise"} {
		args := []string{operation, "123", "--unmanaged"}
		if operation != "run" {
			args = append(args, "--issue", "123")
		}
		args = append(args, "--agent", "copilot")
		runner := &fakeCommandRunner{}
		if code := Execute(args, io.Discard, io.Discard, NewService(runner, NewOSFileSystem())); code != 2 || len(runner.calls) != 0 {
			t.Fatalf("selector exposed for unmanaged %s", operation)
		}
	}
}

func TestCopilotOmittedOptionsAndSecretRedaction(t *testing.T) {
	t.Setenv("COPILOT_HOME", t.TempDir())
	t.Setenv("COPILOT_PROVIDER_API_KEY", "local-test-secret")
	runner := &fakeCommandRunner{fn: func(spec CommandSpec) CommandResult {
		if containsString(spec.Args, "--model") || containsString(spec.Args, "--reasoning-effort") {
			t.Fatalf("default override: %v", spec.Args)
		}
		return CommandResult{Stdout: copilotCompletionForTest("report local-test-secret"), Stderr: "diagnostic local-test-secret"}
	}}
	c := copilotRuntime{runner: runner, files: NewOSFileSystem()}
	got := c.execute(t.TempDir(), t.TempDir(), filepath.Join(t.TempDir(), ".git"), copilotRunWorkerPolicy(), "task", copilotOptions{})
	if !commandSucceeded(got) || strings.Contains(got.Stdout+got.Stderr, "local-test-secret") || !strings.Contains(got.Stdout, "[REDACTED]") {
		t.Fatal(got)
	}
}

func TestDoctorMissingCopilotDoesNotFallBack(t *testing.T) {
	root := t.TempDir()
	writeProjectFiles(t, root)
	writeCopilotConfig(t, root)
	runner := &fakeCommandRunner{lookups: map[string]error{"copilot": os.ErrNotExist}}
	runner.fn = func(spec CommandSpec) CommandResult {
		if spec.Name == "codex" || spec.Name == "copilot" {
			t.Fatalf("Doctor fallback or unavailable tool execution: %+v", spec)
		}
		return standardFakeResult(spec, root, "", false, false)
	}
	var output strings.Builder
	if err := newTestService(t, runner, root).Doctor(&output); err == nil || !strings.Contains(output.String(), "copilot executable is unavailable") {
		t.Fatal(err, output.String())
	}
	if strings.Contains(output.String(), "codex") {
		t.Fatal(output.String())
	}
}

func TestCopilotPrivateStateAvoidsMainCheckoutThroughSymlink(t *testing.T) {
	main := t.TempDir()
	common := filepath.Join(main, ".git")
	if err := os.Mkdir(common, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(main, alias); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", alias)
	c := copilotRuntime{runner: &fakeCommandRunner{fn: func(spec CommandSpec) CommandResult { t.Fatalf("worker invoked: %+v", spec); return CommandResult{} }}, files: NewOSFileSystem()}
	result := c.execute(t.TempDir(), t.TempDir(), common, copilotRunWorkerPolicy(), "task", copilotOptions{})
	if result.Err == nil {
		t.Fatal("private state created in main checkout")
	}
	entries, err := os.ReadDir(main)
	if err != nil || len(entries) != 1 || entries[0].Name() != ".git" {
		t.Fatal(entries, err)
	}
}
