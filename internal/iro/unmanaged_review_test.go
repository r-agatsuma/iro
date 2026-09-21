package iro

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnmanagedReviewCLIRejectsBeforeIO(t *testing.T) {
	for _, suffix := range [][]string{
		{"--unmanaged"}, {"--issue", "123"}, {"--unmanaged", "--issue"},
		{"--unmanaged", "--issue", ""}, {"--unmanaged", "--issue", "0"}, {"--unmanaged", "--issue", "-1"},
		{"--unmanaged", "--issue", "1.2"}, {"--unmanaged", "--issue", "+1"}, {"--unmanaged", "--issue", "999999999999999999999999"},
		{"--unmanaged", "--issue", "123", "--issue", "123"}, {"--unmanaged", "--issue", "123", "--unmanaged"},
		{"--unmanaged", "--issue", "123", "extra"}, {"--unmanaged=true", "--issue", "123"}, {"--unmanaged", "--issue", "123", "--unknown"},
	} {
		runner := &fakeCommandRunner{}
		if code := Execute(append([]string{"review", "42"}, suffix...), io.Discard, io.Discard, NewService(runner, NewOSFileSystem())); code != 2 || len(runner.calls) != 0 {
			t.Fatalf("%v: %d %v", suffix, code, runner.calls)
		}
	}
	var usage strings.Builder
	Execute([]string{"review"}, io.Discard, &usage, nil)
	if !strings.Contains(usage.String(), "--unmanaged --issue <issue-number>") {
		t.Fatal(usage.String())
	}
}

func TestUnmanagedReviewScenarioBAndFailureMatrix(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		worker                 CommandResult
		comment                CommandResult
		cleanupFail            bool
		wantCode, wantComments int
	}{
		{"success", CommandResult{Stdout: "opaque FINDING\n"}, CommandResult{Stdout: "HTTP/2.0 201 Created\r\nContent-Type: application/json\r\n\r\n{\"id\":1}"}, false, 0, 1},
		{"success cleanup failure", CommandResult{Stdout: "opaque"}, CommandResult{Stdout: "HTTP/2.0 201 Created\n\n{\"id\":1}"}, true, 0, 1},
		{"worker failure", CommandResult{Stdout: "partial", ExitCode: 1}, CommandResult{}, false, 1, 0},
		{"empty", CommandResult{}, CommandResult{}, true, 1, 0},
		{"definite failure", CommandResult{Stdout: "review"}, CommandResult{Stdout: "HTTP/2.0 403 Forbidden\n\n{}", ExitCode: 1}, false, 1, 1},
		{"definite failure cleanup failure", CommandResult{Stdout: "review"}, CommandResult{Stdout: "HTTP/2.0 422 Unprocessable Entity\n\n{}", ExitCode: 1}, true, 1, 1},
		{"ambiguous", CommandResult{Stdout: "review"}, CommandResult{ExitCode: 1}, false, 1, 1},
		{"ambiguous cleanup failure", CommandResult{Stdout: "review"}, CommandResult{Stdout: "HTTP/2.0 201 Created\n\n{}"}, true, 1, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			// Invalid project files must never be loaded as configuration or policy.
			os.WriteFile(filepath.Join(root, "iro.toml"), []byte("invalid config sentinel"), 0600)
			os.WriteFile(filepath.Join(root, "WORKFLOW.md"), []byte("policy sentinel"), 0600)
			t.Setenv("GH_HOST", "invalid host")
			t.Setenv("GH_REPO", "unrelated/repository")
			runner := &fakeCommandRunner{}
			workspace := ""
			comments, cleanups, inspections := 0, 0, 0
			runner.fn = func(spec CommandSpec) CommandResult {
				args := strings.Join(spec.Args, " ")
				if spec.Name == "git" {
					switch {
					case strings.HasPrefix(args, "remote get-url --all origin"):
						return CommandResult{Stdout: "git@github.com:acme/iro.git\n"}
					case strings.Contains(args, "--push"), strings.Contains(args, "upstream"):
						t.Fatalf("unexpected identity precondition: %s", args)
					case strings.HasPrefix(args, "fetch "):
						if !strings.HasSuffix(args, reviewHeadForTest) {
							t.Fatal(args)
						}
						return CommandResult{}
					case strings.HasPrefix(args, "worktree add --detach"):
						workspace = spec.Args[3]
						if spec.Args[4] != reviewHeadForTest || !strings.Contains(workspace, "review-pr-42-") {
							t.Fatal(args)
						}
						return CommandResult{}
					case args == "symbolic-ref --quiet HEAD":
						return CommandResult{ExitCode: 1}
					case strings.HasPrefix(args, "worktree remove"):
						cleanups++
						if comments != tc.wantComments {
							t.Fatal("cleanup before comment")
						}
						if tc.cleanupFail {
							return CommandResult{ExitCode: 1}
						}
						os.RemoveAll(workspace)
						return CommandResult{}
					case strings.HasPrefix(args, "worktree list"):
						return CommandResult{}
					}
				}
				if spec.Name == "gh" && containsString(spec.Args, "graphql") {
					inspections++
					if strings.Contains(args, "closingIssuesReferences") || strings.Contains(args, "defaultBranchRef") {
						t.Fatal(args)
					}
					response := strings.ReplaceAll(reviewResponseForTest, "contributor/iro", "acme/iro")
					response = strings.ReplaceAll(response, `"defaultBranchRef":{"name":"main"},`, "")
					response = strings.ReplaceAll(response, `"baseRefName":"main"`, `"baseRefName":"release"`)
					response = strings.ReplaceAll(response, `"totalCount":1`, `"totalCount":0`)
					return CommandResult{Stdout: response}
				}
				if spec.Name == "gh" && containsArgs(spec.Args, "--method", "POST") {
					comments++
					var body struct{ Body string }
					if json.Unmarshal(spec.Stdin, &body) != nil || body.Body != tc.worker.Stdout {
						t.Fatal(string(spec.Stdin))
					}
					return tc.comment
				}
				if spec.Name == "codex" && containsString(spec.Args, "--ephemeral") {
					if inspections != 1 || spec.Dir != workspace {
						t.Fatal(spec)
					}
					if strings.Contains(string(spec.Stdin), "sentinel") {
						t.Fatal("read project policy")
					}
					if !strings.Contains(args, "Do not read iro.toml or WORKFLOW.md") || !strings.Contains(args, "Do not edit source files") || !strings.Contains(args, reviewHeadForTest) {
						t.Fatal(args)
					}
					// The remote may now advance. There must be no second PR inspection.
					return tc.worker
				}
				return reviewFakeResult(spec, root, "")
			}
			service := newTestService(t, runner, root)
			t.Setenv("GH_HOST", "invalid host")
			t.Setenv("GH_REPO", "unrelated/repository")
			var out, diagnostic strings.Builder
			code := Execute([]string{"review", "42", "--issue", "123", "-m", "model", "--unmanaged", "--reasoning-effort", "high", "--no-sandbox"}, &out, &diagnostic, service)
			if code != tc.wantCode || comments != tc.wantComments || cleanups != 1 || inspections != 1 {
				t.Fatalf("code=%d comments=%d cleanup=%d inspections=%d: %s", code, comments, cleanups, inspections, diagnostic.String())
			}
			assertWorkerOptions(t, runner.calls, "model", "high")
			if tc.cleanupFail && (!strings.Contains(diagnostic.String(), workspace) || !strings.Contains(diagnostic.String(), "warning:")) {
				t.Fatal(diagnostic.String())
			}
			if strings.HasPrefix(tc.name, "ambiguous") && !strings.Contains(diagnostic.String(), "inspect PR #42 before retrying") {
				t.Fatal(diagnostic.String())
			}
			if code != 0 && strings.Contains(out.String(), "Posted") {
				t.Fatal(out.String())
			}
		})
	}
}

func TestReviewModeSpecificIdentity(t *testing.T) {
	for _, unmanaged := range []bool{false, true} {
		t.Run(map[bool]string{false: "managed", true: "unmanaged"}[unmanaged], func(t *testing.T) {
			root := t.TempDir()
			writeProjectFiles(t, root)
			// Managed configuration uses upstream R1; origin names R2.
			data, err := os.ReadFile(filepath.Join(root, "iro.toml"))
			if err != nil {
				t.Fatal(err)
			}
			os.WriteFile(filepath.Join(root, "iro.toml"), []byte(strings.ReplaceAll(string(data), `remote = "origin"`, `remote = "upstream"`)), 0600)
			t.Setenv("GH_REPO", "")
			t.Setenv("GH_HOST", "")
			if unmanaged {
				t.Setenv("GH_REPO", "malformed selector")
				t.Setenv("GH_HOST", "other.example")
			}
			runner := &fakeCommandRunner{}
			inspected := false
			runner.fn = func(spec CommandSpec) CommandResult {
				if spec.Name == "git" && containsString(spec.Args, "remote.origin.url") {
					return CommandResult{Stdout: "git@github.com:other/repo.git\n"}
				}
				if spec.Name == "git" && strings.Join(spec.Args, " ") == "remote get-url --all origin" {
					return CommandResult{Stdout: "git@github.com:other/repo.git\n"}
				}
				if spec.Name == "gh" && containsString(spec.Args, "graphql") {
					inspected = true
					owner, name := "owner=acme", "name=iro"
					if unmanaged {
						owner, name = "owner=other", "name=repo"
					}
					if !containsString(spec.Args, owner) || !containsString(spec.Args, name) {
						t.Fatal(spec.Args)
					}
					return CommandResult{ExitCode: 1}
				}
				return reviewFakeResult(spec, root, "")
			}
			service := newTestService(t, runner, root)
			if unmanaged {
				t.Setenv("GH_REPO", "malformed selector")
				t.Setenv("GH_HOST", "other.example")
			}
			args := []string{"review", "42"}
			if unmanaged {
				args = append(args, "--unmanaged", "--issue", "123")
			}
			var diagnostic strings.Builder
			if code := Execute(args, io.Discard, &diagnostic, service); code != 1 || !inspected {
				t.Fatalf("%d %t %s", code, inspected, diagnostic.String())
			}
		})
	}
}

func TestUnmanagedReviewEligibilityAndSnapshotFailures(t *testing.T) {
	for _, stage := range []string{"fork", "closed", "missing PR", "missing Issue", "invalid HEAD", "fetch", "mismatched HEAD", "attached HEAD"} {
		t.Run(stage, func(t *testing.T) {
			root := t.TempDir()
			runner := &fakeCommandRunner{}
			workspace := ""
			cleanup := false
			runner.fn = func(spec CommandSpec) CommandResult {
				args := strings.Join(spec.Args, " ")
				if spec.Name == "gh" && containsString(spec.Args, "graphql") {
					response := strings.ReplaceAll(reviewResponseForTest, "contributor/iro", "acme/iro")
					switch stage {
					case "fork":
						response = reviewResponseForTest
					case "closed":
						response = strings.ReplaceAll(response, `"OPEN"`, `"CLOSED"`)
					case "missing PR":
						response = `{"data":{"repository":{"pullRequest":null}}}`
					case "invalid HEAD":
						response = strings.ReplaceAll(response, reviewHeadForTest, "invalid")
					}
					return CommandResult{Stdout: response}
				}
				if stage == "missing Issue" && spec.Name == "gh" && containsArgs(spec.Args, "issue", "view") {
					return CommandResult{ExitCode: 1}
				}
				if spec.Name == "git" {
					switch {
					case strings.HasPrefix(args, "remote get-url"):
						return CommandResult{Stdout: "git@github.com:acme/iro.git\n"}
					case strings.HasPrefix(args, "fetch "):
						if stage == "fetch" {
							return CommandResult{ExitCode: 1}
						}
						return CommandResult{}
					case strings.HasPrefix(args, "worktree add"):
						workspace = spec.Args[3]
						return CommandResult{}
					case args == "symbolic-ref --quiet HEAD":
						if stage == "attached HEAD" {
							return CommandResult{Stdout: "refs/heads/main"}
						}
						return CommandResult{ExitCode: 1}
					case args == "rev-parse HEAD" && stage == "mismatched HEAD":
						return CommandResult{Stdout: reviewBaseForTest}
					case strings.HasPrefix(args, "worktree remove"):
						cleanup = true
						os.RemoveAll(workspace)
						return CommandResult{}
					case strings.HasPrefix(args, "worktree list"):
						return CommandResult{}
					}
				}
				if (spec.Name == "codex" && containsString(spec.Args, "--ephemeral")) || containsString(spec.Args, "POST") {
					t.Fatalf("unexpected side effect: %v", spec)
				}
				return reviewFakeResult(spec, root, "")
			}
			service := newTestService(t, runner, root)
			if err := service.reviewUnmanaged(42, 123, workerOptions{}, io.Discard, io.Discard); err == nil {
				t.Fatal("expected failure")
			}
			if workspace != "" && !cleanup {
				t.Fatal("missing cleanup")
			}
		})
	}
}

type reviewCleanupFailureFS struct{ FileSystem }

func (reviewCleanupFailureFS) RemoveAll(string) error { return os.ErrPermission }

func TestManagedReviewCleanupFailurePreventsComment(t *testing.T) {
	root := t.TempDir()
	writeProjectFiles(t, root)
	runner := &fakeCommandRunner{}
	workspace := ""
	runner.fn = func(spec CommandSpec) CommandResult {
		if spec.Name == "codex" && containsString(spec.Args, "--ephemeral") {
			workspace = spec.Dir
		}
		if spec.Name == "gh" && containsArgs(spec.Args, "pr", "comment") {
			t.Fatal("managed cleanup failure must prevent comment")
		}
		return reviewFakeResult(spec, root, "review")
	}
	service := newTestService(t, runner, root)
	service.FileSystem = reviewCleanupFailureFS{service.FileSystem}
	if err := service.Review(42, io.Discard); err == nil || !strings.Contains(err.Error(), "could not remove") {
		t.Fatal(err)
	}
	if workspace == "" {
		t.Fatal("Reviewer not invoked")
	}
	os.RemoveAll(workspace)
}

func TestUnmanagedReviewCleanupConfirmsDirectoryAndRegistration(t *testing.T) {
	for _, state := range []string{"removed", "directory remains", "registration remains"} {
		t.Run(state, func(t *testing.T) {
			root := t.TempDir()
			workspace := filepath.Join(root, "review-workspace")
			os.Mkdir(workspace, 0700)
			runner := &fakeCommandRunner{}
			runner.fn = func(spec CommandSpec) CommandResult {
				if containsArgs(spec.Args, "worktree", "remove") {
					if state != "directory remains" {
						os.RemoveAll(workspace)
					}
					return CommandResult{}
				}
				if containsArgs(spec.Args, "worktree", "list") && state == "registration remains" {
					return CommandResult{Stdout: "worktree " + workspace + "\nHEAD " + reviewHeadForTest + "\ndetached\n\n"}
				}
				return CommandResult{}
			}
			service := newTestService(t, runner, root)
			err := service.removeUnmanagedWorktree(root, workspace)
			if (err == nil) != (state == "removed") {
				t.Fatalf("%s: %v", state, err)
			}
		})
	}
}
