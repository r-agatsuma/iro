package iro

import (
	"io"
	"reflect"
	"strings"
	"testing"
)

func TestWorkerSandboxInvocations(t *testing.T) {
	for _, command := range []string{"run", "review", "revise"} {
		for _, tc := range []struct {
			name      string
			flags     []string
			noSandbox bool
			overrides bool
		}{
			{name: "default"},
			{name: "explicit", flags: []string{"--no-sandbox"}, noSandbox: true},
			{name: "default with overrides", flags: []string{"--model", "gpt-test", "--reasoning-effort", "low"}, overrides: true},
			{name: "sandbox first", flags: []string{"--no-sandbox", "--model", "gpt-test", "--reasoning-effort", "low"}, noSandbox: true, overrides: true},
			{name: "sandbox middle", flags: []string{"--reasoning-effort", "low", "--no-sandbox", "-m", "gpt-test"}, noSandbox: true, overrides: true},
			{name: "sandbox last", flags: []string{"-m", "gpt-test", "--reasoning-effort", "low", "--no-sandbox"}, noSandbox: true, overrides: true},
		} {
			t.Run(command+"/"+tc.name, func(t *testing.T) {
				var service *Service
				var runner *fakeCommandRunner
				number := "42"
				if command == "revise" {
					fixture := newReviseFixture(t, false)
					service, runner = fixture.service, fixture.runner
				} else {
					root := t.TempDir()
					writeProjectFiles(t, root)
					runner = &fakeCommandRunner{}
					runner.fn = func(spec CommandSpec) CommandResult {
						if command == "review" {
							return reviewFakeResult(spec, root, "review report")
						}
						return standardFakeResult(spec, root, "", false, false)
					}
					service = newTestService(t, runner, root)
					if command == "run" {
						number = "123"
					}
				}
				var stderr strings.Builder
				if status := Execute(append([]string{command, number}, tc.flags...), io.Discard, &stderr, service); status != 0 {
					t.Fatalf("Execute = %d: %s", status, stderr.String())
				}
				workers := 0
				for _, call := range runner.calls {
					if call.Name != "codex" || !containsString(call.Args, "--ephemeral") {
						continue
					}
					workers++
					want := []string{"--cd", call.Dir, "--sandbox", "workspace-write", "--ask-for-approval", "never"}
					if tc.noSandbox {
						want[3] = "danger-full-access"
					} else {
						want = append(want, "-c", "sandbox_workspace_write.network_access=true")
					}
					if len(call.Args) < len(want) || !reflect.DeepEqual(call.Args[:len(want)], want) {
						t.Fatalf("worker argv = %v, want prefix %v", call.Args, want)
					}
					if tc.noSandbox && containsString(call.Args, "sandbox_workspace_write.network_access=true") {
						t.Fatalf("unexpected workspace network override: %v", call.Args)
					}
				}
				if workers != 1 {
					t.Fatalf("worker invocations = %d, want 1", workers)
				}
				if tc.overrides {
					assertWorkerOptions(t, runner.calls, "gpt-test", "low")
				} else {
					assertWorkerHasNoModel(t, runner.calls)
					assertWorkerHasNoReasoningEffort(t, runner.calls)
				}
			})
		}
	}
}

func TestExecuteRejectsInvalidSandboxOptionsBeforeSideEffects(t *testing.T) {
	for _, command := range []string{"run", "review", "revise", "land"} {
		flags := [][]string{{"--no-sandbox", "--no-sandbox"}, {"--no-sandbox", "true"}, {"--no-sandbox=false"}}
		if command == "land" {
			flags = append(flags, []string{"--no-sandbox"})
		}
		for _, suffix := range flags {
			runner := &fakeCommandRunner{}
			service := NewService(runner, NewOSFileSystem())
			args := append([]string{command, "123"}, suffix...)
			if status := Execute(args, io.Discard, io.Discard, service); status != 2 {
				t.Fatalf("Execute(%v) = %d, want 2", args, status)
			}
			if len(runner.calls) != 0 {
				t.Fatalf("unexpected commands: %v", runner.calls)
			}
		}
	}
}
