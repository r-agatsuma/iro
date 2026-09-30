package iro

import (
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestAgentSelectionRejectsUnsupportedBeforeExecution(t *testing.T) {
	runner := &fakeCommandRunner{}
	service := newTestService(t, runner, t.TempDir())
	if err := service.requireAgent("copilot", ""); err == nil {
		t.Fatal("unsupported agent passed preflight")
	}
	for _, invoke := range []func() CommandResult{
		func() CommandResult {
			return service.runAuthor("copilot", "", RepositoryIdentity{}, issue{}, workerOptions{})
		},
		func() CommandResult {
			return service.runReviewer("copilot", "", RepositoryIdentity{}, reviewPullRequest{}, issue{}, nil, nil, reviewContext{}, workerOptions{})
		},
		func() CommandResult {
			return service.runRevisionAuthor("copilot", "", RepositoryIdentity{}, reviewPullRequest{}, issue{}, nil, nil, reviewContext{}, workerOptions{})
		},
		func() CommandResult {
			return service.runUnmanagedRevisionAuthor("copilot", "", RepositoryIdentity{}, reviewPullRequest{}, issue{}, reviewContext{}, workerOptions{})
		},
	} {
		if result := invoke(); commandSucceeded(result) || result.Err == nil {
			t.Fatalf("unsupported worker was accepted: %+v", result)
		}
	}
	if len(runner.calls) != 0 {
		t.Fatalf("unsupported agent executed commands: %+v", runner.calls)
	}
}

func TestDoctorContinuesBackendDiagnosticsWithInvalidConfig(t *testing.T) {
	for _, config := range []string{
		"",
		strings.Replace(configTemplate, `type = "github"`, `type = "gitea"`, 1),
		strings.Replace(configTemplate, `type = "codex"`, `type = "copilot"`, 1),
	} {
		t.Run(config, func(t *testing.T) {
			root := t.TempDir()
			writeProjectFiles(t, root)
			if err := os.WriteFile(filepath.Join(root, "iro.toml"), []byte(config), 0644); err != nil {
				t.Fatal(err)
			}
			runner := &fakeCommandRunner{}
			runner.fn = func(spec CommandSpec) CommandResult {
				if reflect.DeepEqual(spec.Args, []string{"--version"}) {
					return CommandResult{Stdout: "test version"}
				}
				if spec.Name == "git" && reflect.DeepEqual(spec.Args, []string{"rev-parse", "--show-toplevel"}) {
					return CommandResult{Stdout: root}
				}
				if spec.Name == "codex" && reflect.DeepEqual(spec.Args, []string{"login", "status"}) {
					return CommandResult{}
				}
				t.Fatalf("unexpected command with invalid config: %+v", spec)
				return CommandResult{}
			}
			var out strings.Builder
			err := newTestService(t, runner, root).Doctor(&out)
			if err == nil || err.Error() != "doctor found 4 failing check(s)" {
				t.Fatalf("unexpected diagnostic result: %v\n%s", err, out.String())
			}
			for _, want := range []string{
				"gh executable path: /fake/bin/gh", "gh version: test version",
				"codex executable path: /fake/bin/codex", "codex version: test version",
				"FAIL: iro.toml validity:", "FAIL: configured GitHub remote:",
				"OK: gh executable", "OK: codex executable", "OK: Codex authentication",
			} {
				if !strings.Contains(out.String(), want) {
					t.Errorf("missing %q in %s", want, out.String())
				}
			}
		})
	}
}

func TestAgentPreflightSelectsCodex(t *testing.T) {
	runner := &fakeCommandRunner{}
	service := newTestService(t, runner, t.TempDir())
	if err := service.requireAgent("codex", "/project"); err != nil {
		t.Fatal(err)
	}
	want := []CommandSpec{{Name: "codex", Args: []string{"login", "status"}, Dir: "/project"}}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("commands = %+v; want %+v", runner.calls, want)
	}
	// Backend diagnostics must select the same concrete runtime.
	service.agentToolDiagnostics(io.Discard, "codex")
	if call := runner.calls[len(runner.calls)-1]; call.Name != "codex" || !reflect.DeepEqual(call.Args, []string{"--version"}) {
		t.Fatalf("version command = %+v", call)
	}
}
