package iro

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const deliveryResponseForTest = `{"data":{"repository":{"defaultBranchRef":{"name":"main"},"pullRequests":{"nodes":[],"pageInfo":{"hasNextPage":false}}}}}`

func TestRunDeliveryStages(t *testing.T) {
	for _, commentFailure := range []bool{false, true} {
		for _, tc := range []struct {
			name, fail, want string
			success          bool
		}{
			{"success", "", "", true},
			{"no changes", "empty", "no committable changes", false},
			{"stage failure", "add", "stage worker changes", false},
			{"commit failure", "commit", "commit failed", false},
			{"push failure", "push", "local commit remains", false},
			{"create failure", "create", "remote branch", false},
			{"invalid response", "response", "a PR may exist", false},
			{"hint failure", "hint", "warning:", true},
		} {
			t.Run(fmt.Sprintf("%s/comment_failure=%t", tc.name, commentFailure), func(t *testing.T) {
				root := t.TempDir()
				writeProjectFiles(t, root)
				runner := &fakeCommandRunner{}
				var stages []string
				var failureComments []string
				runner.fn = func(spec CommandSpec) CommandResult {
					stage := ""
					if spec.Name == "gh" && containsArgs(spec.Args, "issue", "comment") {
						failureComments = append(failureComments, spec.Args[len(spec.Args)-1])
						if commentFailure {
							return CommandResult{ExitCode: 1, Stderr: "comment denied"}
						}
					}
					if spec.Name == "git" && spec.Args[0] == "commit" && !containsArgs(spec.Args, "-m", "Implement issue #123") {
						t.Fatal(spec.Args)
					}
					if spec.Name == "git" {
						switch spec.Args[0] {
						case "add", "commit", "push":
							stage = spec.Args[0]
						case "diff":
							stage = "empty"
						}
					}
					if spec.Name == "gh" && spec.Args[0] == "api" && spec.Args[1] != "graphql" {
						stage = "create"
						var body map[string]any
						if err := json.Unmarshal(spec.Stdin, &body); err != nil {
							t.Fatal(err)
						}
						if body["title"] != "Implement issue #123" || !validDeliveryHeadForTest(body["head"], 123) || body["base"] != "main" || body["draft"] != false || body["body"] != "Issue #123 の実装です。\n\nCloses #123\n" {
							t.Fatalf("invalid PR: %s", spec.Stdin)
						}
						if tc.fail == "response" {
							return CommandResult{Stdout: `{}`}
						}
					}
					if spec.Name == "gh" && spec.Args[0] == "pr" {
						stage = "hint"
						if !containsArgs(spec.Args, "--repo", "github.com/acme/iro") || !strings.Contains(spec.Args[len(spec.Args)-1], "iro land 456") || !strings.Contains(spec.Args[len(spec.Args)-1], "Author report (pre-delivery):\n\n"+standardFakeResult(CommandSpec{Name: "codex", Args: []string{"--cd"}}, root, "", false, false).Stdout) {
							t.Fatal(spec.Args)
						}
					}
					if stage != "" {
						stages = append(stages, stage)
					}
					if stage != "" && stage == tc.fail {
						if stage == "empty" {
							return CommandResult{}
						}
						return CommandResult{ExitCode: 1}
					}
					if stage == "push" && strings.Join(spec.Args, " ") != "push --no-follow-tags --no-recurse-submodules -- origin "+spec.Args[len(spec.Args)-1] {
						t.Fatal(spec.Args)
					}
					if spec.Name == "git" && len(spec.Args) > 1 && spec.Args[0] == "worktree" && spec.Args[1] == "add" && spec.Args[len(spec.Args)-1] != foundationHEAD {
						t.Fatal("wrong base", spec.Args)
					}
					return standardFakeResult(spec, root, "", false, false)
				}
				service := newTestService(t, runner, root)
				var out, errOut strings.Builder
				status := Execute([]string{"run", "123"}, &out, &errOut, service)
				if (status == 0) != tc.success {
					t.Fatalf("status %d: %s", status, errOut.String())
				}
				if tc.want != "" && !strings.Contains(errOut.String(), tc.want) {
					t.Fatal(errOut.String())
				}
				if tc.success && (!strings.Contains(out.String(), "PR #456") || !strings.Contains(out.String(), "iro land 456")) {
					t.Fatal(out.String())
				}
				if tc.success && len(failureComments) != 0 {
					t.Fatalf("successful delivery posted to Issue: %v", failureComments)
				}
				if !tc.success {
					if len(failureComments) != 1 || !strings.Contains(failureComments[0], tc.want) || !strings.Contains(failureComments[0], "delivery（Author は成功）") {
						t.Fatalf("missing delivery failure diagnostic: %v", failureComments)
					}
					if commentFailure && !strings.Contains(errOut.String(), "failure report comment failed") {
						t.Fatal(errOut.String())
					}
				}
				logs, err := filepath.Glob(filepath.Join(service.Dirs.StateRoot, "runs", identityKeyForTest(), "*.log"))
				if err != nil || len(logs) != 1 {
					t.Fatalf("missing local report: %v %v", logs, err)
				}
				data, err := os.ReadFile(logs[0])
				report := standardFakeResult(CommandSpec{Name: "codex", Args: []string{"--cd"}}, root, "", false, false).Stdout
				if err != nil || report == "" || !strings.Contains(string(data), report) || (!tc.success && !strings.Contains(failureComments[0], report)) {
					t.Fatalf("Author report was not retained: %s, %v", data, err)
				}
				expected := []string{"add", "empty", "commit", "push", "create", "hint"}
				stop := len(expected)
				for i, stage := range expected {
					if stage != "" && stage == tc.fail {
						stop = i + 1
						break
					}
				}
				if tc.fail == "response" {
					stop = 4
				}
				if strings.Join(stages, ",") != strings.Join(expected[:stop], ",") {
					t.Fatalf("stages: %v", stages)
				}
			})
		}
	}
}

func TestRunRejectsDeliveryPreconditionsBeforeWorker(t *testing.T) {
	for _, kind := range []string{"detached", "ahead", "behind", "diverged", "missing remote", "push mismatch", "remote auth"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			writeProjectFiles(t, root)
			runner := &fakeCommandRunner{}
			runner.fn = func(spec CommandSpec) CommandResult {
				if spec.Name == "git" {
					// Model A -> C for ahead/behind, and separate C / E tips
					// for divergence. Equality alone must reject all three.
					if spec.Args[0] == "rev-parse" && containsString(spec.Args, "HEAD") && (kind == "ahead" || kind == "diverged") {
						return CommandResult{Stdout: revisionCommit}
					}
					if spec.Args[0] == "symbolic-ref" && kind == "detached" {
						return CommandResult{ExitCode: 1}
					}
					if spec.Args[0] == "remote" && kind == "push mismatch" {
						return CommandResult{Stdout: "git@github.com:other/repo.git"}
					}
					if spec.Args[0] == "ls-remote" {
						if kind == "remote auth" {
							return CommandResult{ExitCode: 1}
						}
						if containsString(spec.Args, "refs/heads/main") {
							switch kind {
							case "missing remote":
								return CommandResult{}
							case "ahead":
								return CommandResult{Stdout: foundationHEAD + "\trefs/heads/main\n"}
							case "behind":
								return CommandResult{Stdout: revisionCommit + "\trefs/heads/main\n"}
							case "diverged":
								return CommandResult{Stdout: reviewBaseForTest + "\trefs/heads/main\n"}
							}
						}
					}
				}
				return standardFakeResult(spec, root, "", false, false)
			}
			service := newTestService(t, runner, root)
			if err := service.Run(123, io.Discard); err == nil {
				t.Fatal("unexpected success")
			}
			for _, call := range runner.calls {
				if call.Name == "codex" && containsString(call.Args, "--ephemeral") || call.Name == "git" && containsArgs(call.Args, "worktree", "add") {
					t.Fatal("side effect before rejection", call)
				}
				if call.Name == "git" {
					switch call.Args[0] {
					case "add", "commit", "push", "fetch", "pull", "reset", "rebase", "merge", "checkout", "switch":
						t.Fatal("source synchronization or mutation before rejection", call)
					}
				}
			}
		})
	}
}

func TestDeliveryRelationPagination(t *testing.T) {
	runner := &fakeCommandRunner{}
	pages := 0
	runner.fn = func(spec CommandSpec) CommandResult {
		pages++
		if pages == 1 {
			return CommandResult{Stdout: strings.Replace(deliveryResponseForTest, `"hasNextPage":false`, `"hasNextPage":true,"endCursor":"next"`, 1)}
		}
		if !containsArgs(spec.Args, "-f", "cursor=next") {
			t.Fatal(spec.Args)
		}
		return CommandResult{Stdout: deliveryResponseForTest}
	}
	service := newTestService(t, runner, t.TempDir())
	if base, err := service.deliveryBase("", RepositoryIdentity{Owner: "acme", Name: "iro"}, 123, "iro/issue-123"); err != nil || base != "main" || pages != 2 {
		t.Fatalf("%s %v %d", base, err, pages)
	}
}
