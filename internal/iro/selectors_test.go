package iro

import (
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func trackerOperations() map[string]func(*Service, Config) error {
	return map[string]func(*Service, Config) error{
		"run": func(s *Service, config Config) error {
			return s.runTracker("", config, 123, workerOptions{}, io.Discard, io.Discard)
		},
		"review": func(s *Service, config Config) error {
			return s.reviewTracker("", config, 42, nil, nil, workerOptions{}, io.Discard)
		},
		"revise": func(s *Service, config Config) error {
			return s.reviseTracker("", config, 42, nil, workerOptions{}, io.Discard)
		},
		"land": func(s *Service, config Config) error {
			return s.landTracker("", config, 42, io.Discard)
		},
	}
}

// Exercise dispatch independently of config validation so a future schema
// expansion cannot accidentally enable a fallback into GitHub or Codex.
func TestTrackerDispatchRejectsUnsupportedBeforeProviderAccess(t *testing.T) {
	for name, operation := range trackerOperations() {
		for _, value := range []string{"gitea", "future", "", "GitHub"} {
			t.Run(name+"/"+value, func(t *testing.T) {
				// No subprocess or filesystem boundary is available before selection.
				err := operation(&Service{}, Config{TrackerType: value, AgentType: "codex"})
				if err == nil || err.Error() != unsupportedTracker(value).Error() {
					t.Fatalf("dispatch error = %v", err)
				}
			})
		}
	}
}

func TestAgentDispatchRejectsUnsupportedBeforeWorkerDependentOperations(t *testing.T) {
	for name, operation := range trackerOperations() {
		if name == "land" {
			continue
		}
		for _, value := range []string{"copilot", "future", "", "Codex"} {
			t.Run(name+"/"+value, func(t *testing.T) {
				err := operation(&Service{}, Config{TrackerType: "github", AgentType: value})
				if err == nil || err.Error() != unsupportedAgent(value).Error() {
					t.Fatalf("dispatch error = %v", err)
				}
			})
		}
	}
}

func TestManagedCommandsRejectFutureSelectorsBeforeRemoteOrWorkspaceAccess(t *testing.T) {
	for _, command := range []string{"run", "review", "revise", "land"} {
		for _, selector := range []struct{ current, future, key string }{
			{"github", "gitea", "tracker.type"},
			{"codex", "copilot", "agent.type"},
		} {
			t.Run(command+"/"+selector.key, func(t *testing.T) {
				root := t.TempDir()
				writeProjectFiles(t, root)
				config := strings.Replace(configTemplate, `type = "`+selector.current+`"`, `type = "`+selector.future+`"`, 1)
				if err := os.WriteFile(filepath.Join(root, "iro.toml"), []byte(config), 0600); err != nil {
					t.Fatal(err)
				}
				runner := &fakeCommandRunner{fn: func(spec CommandSpec) CommandResult {
					if spec.Name == "git" && reflect.DeepEqual(spec.Args, []string{"rev-parse", "--show-toplevel"}) {
						return CommandResult{Stdout: root}
					}
					t.Fatalf("unsupported selector reached external operation: %+v", spec)
					return CommandResult{}
				}}
				service := newTestService(t, runner, root)
				var out, errOut strings.Builder
				if code := Execute([]string{command, "123"}, &out, &errOut, service); code == 0 || !strings.Contains(errOut.String(), "unsupported "+selector.key) {
					t.Fatalf("exit = %d, diagnostic = %s", code, errOut.String())
				}
				for _, path := range []string{service.Dirs.DataRoot, service.Dirs.StateRoot} {
					if _, err := os.Stat(path); !os.IsNotExist(err) {
						t.Fatalf("runtime artifacts created before rejection: %s (%v)", path, err)
					}
				}
			})
		}
	}
}

func TestSelectedCodexPreservesWorkerResults(t *testing.T) {
	for name, worker := range map[string]func(codexRuntime) CommandResult{
		"run": func(c codexRuntime) CommandResult {
			return c.runAuthor("workspace", RepositoryIdentity{}, issue{}, workerOptions{})
		},
		"review": func(c codexRuntime) CommandResult {
			return c.runReviewer("workspace", RepositoryIdentity{}, reviewPullRequest{}, issue{}, nil, nil, reviewContext{}, workerOptions{})
		},
		"revise": func(c codexRuntime) CommandResult {
			return c.runRevisionAuthor("workspace", RepositoryIdentity{}, reviewPullRequest{}, issue{}, nil, nil, reviewContext{}, workerOptions{})
		},
		"unmanaged-revise": func(c codexRuntime) CommandResult {
			return c.runUnmanagedRevisionAuthor("workspace", RepositoryIdentity{}, reviewPullRequest{}, issue{}, reviewContext{}, workerOptions{})
		},
	} {
		t.Run(name, func(t *testing.T) {
			want := CommandResult{Stdout: "opaque report\n", Stderr: "runtime diagnostic\n", ExitCode: 7, Err: io.ErrUnexpectedEOF}
			runner := &fakeCommandRunner{fn: func(spec CommandSpec) CommandResult {
				if spec.Name != "codex" || spec.Dir != "workspace" || !containsString(spec.Args, "--ephemeral") || !containsString(spec.Args, "exec") {
					t.Fatalf("unexpected worker command: %+v", spec)
				}
				return want
			}}
			service := &Service{Runner: runner}
			agent, err := service.selectAgent("codex")
			if err != nil {
				t.Fatal(err)
			}
			if got := worker(agent); !reflect.DeepEqual(got, want) || len(runner.calls) != 1 {
				t.Fatalf("result = %+v; calls = %d", got, len(runner.calls))
			}
		})
	}
}

func TestLandTrackerDoesNotSelectAgent(t *testing.T) {
	f := newLandFixture(t)
	// Bypass schema validation to verify Land's dispatch has only a tracker axis.
	config := Config{TrackerType: "github", TrackerRemote: "origin", AgentType: "copilot"}
	if err := f.service.landTracker(f.root, config, 42, io.Discard); err != nil {
		t.Fatal(err)
	}
	if f.mergeCalls != 1 {
		t.Fatalf("merge calls = %d", f.mergeCalls)
	}
}
