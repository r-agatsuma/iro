package iro

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

type unmanagedReviseFixture struct {
	t                              *testing.T
	service                        *Service
	runner                         *fakeCommandRunner
	root, workspace, head, refHead string
	branch, base, repository       string
	registered, dirty              bool
	pr                             map[string]any
	sharedPR                       map[string]any
	worker                         CommandResult
	workers, commits, pushes       int
	cleanups, inspections          int
	intercept                      func(CommandSpec) (CommandResult, bool)
}

func newUnmanagedReviseFixture(t *testing.T) *unmanagedReviseFixture {
	t.Helper()
	f := &unmanagedReviseFixture{t: t, root: t.TempDir(), head: revisionHead, refHead: revisionHead, branch: "human/topic", base: "release/v2", repository: "acme/iro", worker: CommandResult{Stdout: "変更とテスト完了。"}}
	f.pr = map[string]any{"number": 42, "title": "Human PR", "body": "PR implementation feedback", "url": "https://github.com/acme/iro/pull/42", "state": "OPEN", "isDraft": true, "baseRefName": f.base, "baseRefOid": reviewBaseForTest, "headRefName": f.branch, "headRefOid": f.refHead, "headRepository": map[string]any{"nameWithOwner": f.repository}}
	f.runner = &fakeCommandRunner{fn: f.respond}
	f.service = newTestService(t, f.runner, f.root)
	f.service.FileSystem = unmanagedFileGuard{f.service.FileSystem, t}
	return f
}

// Commands never reach Git, GitHub, or Codex. Filesystem effects are limited to
// test-owned temporary paths; unknown commands and all tracker writes fail.
func (f *unmanagedReviseFixture) respond(spec CommandSpec) CommandResult {
	f.t.Helper()
	args := strings.Join(spec.Args, " ")
	if spec.Name == "git" {
		switch spec.Args[0] {
		case "commit":
			f.commits++
		case "push":
			f.pushes++
		case "worktree":
			if spec.Args[1] == "remove" {
				f.cleanups++
			}
		}
	}
	if spec.Name == "codex" && containsString(spec.Args, "--ephemeral") {
		f.workers++
	}
	if spec.Name == "gh" {
		assertExplicitGitHubTarget(f.t, spec)
		if containsString(spec.Args, "graphql") {
			f.inspections++
			for _, forbidden := range []string{"closingIssuesReferences", "defaultBranchRef", "pullRequests(", "states:[OPEN]"} {
				if strings.Contains(args, forbidden) {
					f.t.Fatalf("unmanaged revision inspected managed relations: %s", args)
				}
			}
		}
		if containsString(spec.Args, "--method") || containsString(spec.Args, "comment") || containsString(spec.Args, "mutation") {
			f.t.Fatalf("unexpected tracker mutation: %s", args)
		}
	}
	if f.intercept != nil {
		if result, ok := f.intercept(spec); ok {
			return result
		}
	}
	switch spec.Name {
	case "git":
		switch args {
		case "rev-parse --show-toplevel":
			return CommandResult{Stdout: f.root}
		case "config --get-all remote.origin.url", "remote get-url --all origin", "remote get-url --push --all origin":
			return CommandResult{Stdout: "git@github.com:" + f.repository + ".git\n"}
		case "check-ref-format refs/heads/" + f.branch:
			return CommandResult{}
		case "ls-remote --heads -- origin refs/heads/" + f.branch:
			return CommandResult{Stdout: f.refHead + "\trefs/heads/" + f.branch + "\n"}
		case "fetch --no-tags --no-write-fetch-head --refmap= -- origin " + revisionHead:
			return CommandResult{}
		case "cat-file -t " + revisionHead:
			return CommandResult{Stdout: "commit\n"}
		case "worktree list --porcelain":
			registration := "worktree " + f.root + "\nbranch refs/heads/main\n\n"
			if f.registered {
				registration += "worktree " + f.workspace + "\nHEAD " + f.head + "\ndetached\n\n"
			}
			return CommandResult{Stdout: registration}
		case "rev-parse --path-format=absolute --git-common-dir":
			return CommandResult{Stdout: filepath.Join(f.root, ".git")}
		case "rev-parse --absolute-git-dir":
			return CommandResult{Stdout: filepath.Join(f.root, ".git", "worktrees", filepath.Base(f.workspace))}
		case "symbolic-ref --quiet HEAD":
			return CommandResult{ExitCode: 1}
		case "rev-parse HEAD":
			return CommandResult{Stdout: f.head}
		case "status --porcelain --untracked-files=all":
			if f.dirty {
				return CommandResult{Stdout: " M implementation.txt\n"}
			}
			return CommandResult{}
		case "diff --check", "add --all", "diff --cached --check":
			return CommandResult{}
		case "diff --cached --quiet --exit-code":
			return CommandResult{ExitCode: 1}
		case "write-tree", "rev-parse HEAD^{tree}":
			return CommandResult{Stdout: reviewBaseForTest}
		case "commit -m Revise issue #123 for PR #42":
			f.head, f.dirty = revisionCommit, false
			return CommandResult{}
		case "rev-list --parents -n 1 HEAD":
			return CommandResult{Stdout: revisionCommit + " " + revisionHead}
		case "push --no-follow-tags --no-recurse-submodules -- origin " + revisionCommit + ":refs/heads/" + f.branch:
			f.refHead = f.head
			f.pr["headRefOid"] = f.refHead
			if f.sharedPR != nil {
				f.sharedPR["headRefOid"] = f.refHead
			}
			return CommandResult{}
		case "worktree remove -- " + f.workspace:
			if err := os.RemoveAll(f.workspace); err != nil {
				f.t.Fatal(err)
			}
			f.registered = false
			return CommandResult{}
		}
		if len(spec.Args) == 5 && containsArgs(spec.Args, "worktree", "add") && spec.Args[2] == "--detach" && spec.Args[4] == revisionHead {
			f.workspace, f.registered = spec.Args[3], true
			return CommandResult{}
		}
	case "gh":
		if containsString(spec.Args, "graphql") {
			owner, name, _ := strings.Cut(f.repository, "/")
			if !containsString(spec.Args, "owner="+owner) || !containsString(spec.Args, "name="+name) || !containsString(spec.Args, "number=42") {
				f.t.Fatalf("wrong selected repository/PR: %s", args)
			}
			data, _ := json.Marshal(map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": f.pr}}})
			return CommandResult{Stdout: string(data)}
		}
		if args == "auth status --hostname github.com" || (containsArgs(spec.Args, "issue", "view") && spec.Args[2] == "123") || (containsArgs(spec.Args, "pr", "diff") && spec.Args[2] == "42") || (containsArgs(spec.Args, "pr", "view") && spec.Args[2] == "42") || containsArgs(spec.Args, "api", "--paginate") {
			return reviewFakeResult(spec, f.root, "")
		}
	case "codex":
		if args == "login status" {
			return CommandResult{}
		}
		if containsString(spec.Args, "--ephemeral") {
			if spec.Dir != f.workspace || !containsArgs(spec.Args, "--cd", f.workspace) {
				f.t.Fatalf("wrong Author workspace: %+v", spec)
			}
			f.dirty = true
			if err := os.WriteFile(filepath.Join(f.workspace, "implementation.txt"), []byte("useful worker changes"), 0600); err != nil {
				f.t.Fatal(err)
			}
			return f.worker
		}
	}
	f.t.Fatalf("unexpected command: %+v", spec)
	return CommandResult{ExitCode: 1}
}

func (f *unmanagedReviseFixture) execute(options ...string) (int, string, string) {
	f.t.Helper()
	var out, errOut strings.Builder
	code := Execute(append([]string{"revise", "42", "--unmanaged", "--issue", "123"}, options...), &out, &errOut, f.service)
	return code, out.String(), errOut.String()
}

func TestUnmanagedReviseCLIRejectsBeforeIO(t *testing.T) {
	for _, suffix := range [][]string{
		{"--unmanaged"}, {"--issue", "123"}, {"--unmanaged", "--issue"},
		{"--unmanaged", "--issue", ""}, {"--unmanaged", "--issue", " "}, {"--unmanaged", "--issue", "0"},
		{"--unmanaged", "--issue", "-1"}, {"--unmanaged", "--issue", "+1"}, {"--unmanaged", "--issue", "1.2"},
		{"--unmanaged", "--issue", "abc"}, {"--unmanaged", "--issue", "999999999999999999999999"},
		{"--unmanaged", "--issue", "123", "--issue", "123"}, {"--unmanaged", "--issue", "123", "--unmanaged"},
		{"--unmanaged", "--issue", "123", "extra"}, {"--unmanaged", "true", "--issue", "123"},
		{"--unmanaged=true", "--issue", "123"}, {"--unmanaged", "--issue=123"},
		{"--unmanaged", "--issue", "123", "--unknown"}, {"--unmanaged", "--issue", "123", "--model"},
	} {
		runner := &fakeCommandRunner{}
		if code := Execute(append([]string{"revise", "42"}, suffix...), io.Discard, io.Discard, NewService(runner, NewOSFileSystem())); code != 2 || len(runner.calls) != 0 {
			t.Fatalf("%v: code=%d calls=%v", suffix, code, runner.calls)
		}
	}
	for _, args := range [][]string{{"revise", "--unmanaged", "42", "--issue", "123"}, {"revise", "--issue", "123", "42", "--unmanaged"}} {
		if code := Execute(args, io.Discard, io.Discard, nil); code != 2 {
			t.Fatalf("options before operand accepted: %v", args)
		}
	}
	for _, args := range [][]string{nil, {"revise"}} {
		var usage strings.Builder
		Execute(args, io.Discard, &usage, nil)
		if !strings.Contains(usage.String(), "revise <pr-number> [--unmanaged --issue <issue-number>]") {
			t.Fatal(usage.String())
		}
	}
}

func TestUnmanagedReviseScenarioCAndWorkerOptions(t *testing.T) {
	for _, options := range [][]string{nil, {"--model", "custom", "--reasoning-effort", "high", "--no-sandbox"}, {"-m", "custom"}} {
		t.Run(strings.Join(options, " "), func(t *testing.T) {
			f := newUnmanagedReviseFixture(t)
			if code, out, diagnostic := f.execute(options...); code != 0 || diagnostic != "" || !strings.Contains(out, "Updated existing PR #42 via human/topic") {
				t.Fatalf("code=%d out=%s diagnostic=%s", code, out, diagnostic)
			}
			if f.head != revisionCommit || f.refHead != revisionCommit || f.workers != 1 || f.commits != 1 || f.pushes != 1 || f.cleanups != 1 || f.registered {
				t.Fatalf("delivery/cleanup incomplete: %+v", f)
			}
			if _, err := os.Stat(f.workspace); !os.IsNotExist(err) {
				t.Fatalf("workspace remains: %v", err)
			}
			if _, err := os.Stat(filepath.Join(f.service.Dirs.StateRoot, "ownership")); !os.IsNotExist(err) {
				t.Fatalf("managed ownership created: %v", err)
			}
			assertNormalizedFeedbackInput(t, f.runner.calls, 1)
			for _, call := range f.runner.calls {
				if call.Name != "codex" || !containsString(call.Args, "--ephemeral") {
					continue
				}
				if !containsArgs(call.Args, "-c", "developer_instructions="+strconv.Quote(unmanagedReviseDeveloperInstructions)) || !strings.Contains(string(call.Stdin), "Human-selected specification Issue:\nNumber: 123") || !strings.Contains(string(call.Stdin), "Head OID: "+revisionHead) || strings.Contains(string(call.Stdin), "Project configuration (iro.toml):") {
					t.Fatalf("incorrect unmanaged input: %+v", call)
				}
				if !containsArgs(call.Args, "--ask-for-approval", "never") {
					t.Fatal(call.Args)
				}
				if len(options) == 0 {
					assertWorkerHasNoModel(t, f.runner.calls)
					assertWorkerHasNoReasoningEffort(t, f.runner.calls)
					if !containsArgs(call.Args, "--sandbox", "workspace-write") {
						t.Fatal(call.Args)
					}
				} else if !containsArgs(call.Args, "--model", "custom") {
					t.Fatal(call.Args)
				}
				if containsString(options, "--no-sandbox") && (!containsArgs(call.Args, "--sandbox", "danger-full-access") || !containsArgs(call.Args, "-c", `model_reasoning_effort="high"`) || containsString(call.Args, "sandbox_workspace_write.network_access=true")) {
					t.Fatal(call.Args)
				}
			}
			logs, err := filepath.Glob(filepath.Join(f.service.Dirs.StateRoot, "unmanaged-revisions", "*", "*.log"))
			if err != nil || len(logs) != 1 {
				t.Fatalf("missing Author log: %v %v", logs, err)
			}
			data, err := os.ReadFile(logs[0])
			if err != nil || !strings.Contains(string(data), f.worker.Stdout) || !strings.Contains(string(data), "specification_issue: 123") {
				t.Fatalf("incomplete log: %s %v", data, err)
			}
		})
	}
	options, err := parseWorkerOptions([]string{"revise", "42", "--issue", "123", "-m", "custom", "--unmanaged", "--reasoning-effort", "high"}, "revise", "pr-number")
	if err != nil || !options.Unmanaged || options.SpecificationIssue != 123 || options.Model != "custom" || options.ReasoningEffort != "high" {
		t.Fatalf("option ordering: %+v %v", options, err)
	}
}

func TestUnmanagedReviseEligibility(t *testing.T) {
	for _, failure := range []string{"missing origin", "multiple fetch URLs", "fetch rewrite", "multiple push URLs", "wrong push repository", "closed", "merged", "missing PR", "fork", "missing head repository", "invalid HEAD", "missing head", "missing base", "remote HEAD mismatch", "missing Issue", "invalid feedback", "fetch", "not a commit"} {
		t.Run(failure, func(t *testing.T) {
			f := newUnmanagedReviseFixture(t)
			switch failure {
			case "closed", "merged":
				f.pr["state"] = strings.ToUpper(failure)
			case "missing PR":
				f.pr = nil
			case "fork":
				f.pr["headRepository"] = map[string]any{"nameWithOwner": "fork/iro"}
			case "missing head repository":
				delete(f.pr, "headRepository")
			case "invalid HEAD":
				f.pr["headRefOid"] = "bad"
			case "missing head":
				delete(f.pr, "headRefName")
			case "missing base":
				delete(f.pr, "baseRefName")
			case "remote HEAD mismatch":
				f.refHead = revisionCommit
			}
			f.intercept = func(spec CommandSpec) (CommandResult, bool) {
				args := strings.Join(spec.Args, " ")
				if spec.Name == "git" {
					switch {
					case failure == "missing origin" && args == "config --get-all remote.origin.url":
						return CommandResult{ExitCode: 1}, true
					case failure == "multiple fetch URLs" && args == "config --get-all remote.origin.url", failure == "multiple push URLs" && args == "remote get-url --push --all origin":
						return CommandResult{Stdout: "git@github.com:acme/iro.git\ngit@github.com:acme/iro.git\n"}, true
					case failure == "fetch rewrite" && args == "remote get-url --all origin", failure == "wrong push repository" && args == "remote get-url --push --all origin":
						return CommandResult{Stdout: "git@github.com:other/repo.git\n"}, true
					case failure == "fetch" && spec.Args[0] == "fetch":
						return CommandResult{ExitCode: 1}, true
					case failure == "not a commit" && spec.Args[0] == "cat-file":
						return CommandResult{Stdout: "blob"}, true
					}
				}
				if spec.Name == "gh" && failure == "missing Issue" && containsArgs(spec.Args, "issue", "view") {
					return CommandResult{ExitCode: 1}, true
				}
				if spec.Name == "gh" && failure == "invalid feedback" && containsArgs(spec.Args, "api", "--paginate") {
					return CommandResult{Stdout: "bad JSON"}, true
				}
				return CommandResult{}, false
			}
			if code, _, diagnostic := f.execute(); code != 1 || diagnostic == "" || f.workspace != "" || f.workers != 0 || f.commits != 0 || f.pushes != 0 {
				t.Fatalf("code=%d diagnostic=%s fixture=%+v", code, diagnostic, f)
			}
		})
	}
}

func TestUnmanagedReviseScenarioDRevalidatesBeforeCommitAndPush(t *testing.T) {
	for _, inspection := range []int{2, 3, 4} {
		for _, drift := range []string{"closed", "head repository", "head ref", "PR HEAD", "remote ref", "base name", "origin", "push destination"} {
			t.Run(fmt.Sprintf("inspection=%d/%s", inspection, drift), func(t *testing.T) {
				f := newUnmanagedReviseFixture(t)
				f.intercept = func(spec CommandSpec) (CommandResult, bool) {
					if spec.Name == "gh" && containsString(spec.Args, "graphql") && f.inspections == inspection {
						switch drift {
						case "closed":
							f.pr["state"] = "CLOSED"
						case "head repository":
							f.pr["headRepository"] = map[string]any{"nameWithOwner": "fork/iro"}
						case "head ref":
							f.pr["headRefName"] = "other/ref"
						case "PR HEAD":
							f.pr["headRefOid"] = revisionCommit
						case "remote ref":
							f.refHead = revisionCommit
						case "base name":
							f.pr["baseRefName"] = "different/base"
						}
					}
					if spec.Name == "git" && f.inspections == inspection-1 && spec.Dir == f.workspace {
						args := strings.Join(spec.Args, " ")
						if drift == "origin" && args == "config --get-all remote.origin.url" || drift == "push destination" && args == "remote get-url --push --all origin" {
							return CommandResult{Stdout: "git@github.com:other/repo.git\n"}, true
						}
					}
					return CommandResult{}, false
				}
				code, _, diagnostic := f.execute()
				wantCommit, wantLocal := 0, "none beyond starting HEAD"
				if inspection == 4 {
					wantCommit, wantLocal = 1, revisionCommit
				}
				stage := map[int]string{2: "materialization", 3: "before commit", 4: "before push"}[inspection]
				if code != 1 || f.commits != wantCommit || f.pushes != 0 || f.cleanups != 0 || !f.registered {
					t.Fatalf("code=%d diagnostic=%s fixture=%+v", code, diagnostic, f)
				}
				for _, want := range []string{"stopped at " + stage, f.workspace, "local commit: " + wantLocal, "remote mutation attempted: false"} {
					if !strings.Contains(diagnostic, want) {
						t.Fatalf("missing %q: %s", want, diagnostic)
					}
				}
				if _, err := os.Stat(f.workspace); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestUnmanagedReviseScenarioESharedHeadAndMutableRelations(t *testing.T) {
	for _, arrival := range []string{"existing", "materialization", "Author", "after commit"} {
		t.Run(arrival, func(t *testing.T) {
			f := newUnmanagedReviseFixture(t)
			addSharedPR := func() {
				f.sharedPR = map[string]any{"number": 43, "state": "OPEN", "headRefName": f.branch, "headRefOid": f.refHead, "headRepository": f.repository, "baseRefName": "another/base"}
			}
			if arrival == "existing" {
				addSharedPR()
			}
			f.intercept = func(spec CommandSpec) (CommandResult, bool) {
				if arrival == "materialization" && spec.Name == "git" && spec.Args[0] == "fetch" || arrival == "Author" && spec.Name == "codex" && containsString(spec.Args, "--ephemeral") || arrival == "after commit" && f.commits == 1 && f.sharedPR == nil {
					addSharedPR()
				}
				if spec.Name == "gh" && containsString(spec.Args, "graphql") && f.inspections > 1 {
					f.pr["baseRefOid"] = revisionCommit
					f.pr["closingIssuesReferences"] = map[string]any{"totalCount": 2, "nodes": []any{map[string]any{"number": 456}, map[string]any{"number": 789}}}
				}
				return CommandResult{}, false
			}
			if code, _, diagnostic := f.execute(); code != 0 || diagnostic != "" {
				t.Fatalf("%d %s", code, diagnostic)
			}
			if f.sharedPR == nil || f.sharedPR["headRefOid"] != revisionCommit || f.pr["headRefOid"] != revisionCommit || f.sharedPR["state"] != "OPEN" || f.sharedPR["baseRefName"] != "another/base" {
				t.Fatalf("shared ref consequence or PR integrity: M=%v K=%v", f.pr, f.sharedPR)
			}
		})
	}
}

func TestUnmanagedReviseFailureRetentionAndCleanup(t *testing.T) {
	for _, failure := range []string{"Author", "empty report", "log", "diff check", "staging", "cached check", "empty diff", "diff unreadable", "write tree", "commit", "parent", "merge parent", "tree", "dirty", "attached", "HEAD", "registration", "common directory", "push rejected", "push ambiguous", "cleanup rejected", "cleanup directory remains", "cleanup registration remains"} {
		t.Run(failure, func(t *testing.T) {
			f := newUnmanagedReviseFixture(t)
			if failure == "Author" {
				f.worker = CommandResult{ExitCode: 1, Stdout: "partial report", Stderr: "worker diagnostic"}
			}
			if failure == "empty report" {
				f.worker = CommandResult{Stdout: " \n"}
			}
			if failure == "log" {
				f.service.FileSystem = unmanagedLogFailure{f.service.FileSystem}
			}
			f.intercept = func(spec CommandSpec) (CommandResult, bool) {
				if spec.Name != "git" {
					return CommandResult{}, false
				}
				args := strings.Join(spec.Args, " ")
				if failure == "diff check" && args == "diff --check" || failure == "staging" && args == "add --all" || failure == "cached check" && args == "diff --cached --check" || failure == "write tree" && args == "write-tree" || failure == "commit" && spec.Args[0] == "commit" {
					return CommandResult{ExitCode: 1}, true
				}
				if failure == "empty diff" && args == "diff --cached --quiet --exit-code" {
					return CommandResult{}, true
				}
				if failure == "diff unreadable" && args == "diff --cached --quiet --exit-code" {
					return CommandResult{ExitCode: 2}, true
				}
				if failure == "parent" && spec.Args[0] == "rev-list" {
					return CommandResult{Stdout: revisionCommit + " " + reviewBaseForTest}, true
				}
				if failure == "merge parent" && spec.Args[0] == "rev-list" {
					return CommandResult{Stdout: revisionCommit + " " + revisionHead + " " + reviewBaseForTest}, true
				}
				if failure == "tree" && args == "rev-parse HEAD^{tree}" {
					return CommandResult{Stdout: revisionCommit}, true
				}
				if f.commits > 0 && failure == "dirty" && spec.Args[0] == "status" {
					return CommandResult{Stdout: " M unexpected.txt\n"}, true
				}
				if f.workers > 0 {
					if failure == "attached" && spec.Args[0] == "symbolic-ref" {
						return CommandResult{Stdout: "refs/heads/human/topic"}, true
					}
					if failure == "HEAD" && args == "rev-parse HEAD" {
						return CommandResult{Stdout: reviewBaseForTest}, true
					}
					if failure == "registration" && args == "worktree list --porcelain" {
						return CommandResult{}, true
					}
					if failure == "common directory" && strings.Contains(args, "--git-common-dir") && spec.Dir == f.workspace {
						return CommandResult{Stdout: filepath.Join(f.root, "other", ".git")}, true
					}
				}
				if spec.Args[0] == "push" {
					if failure == "push rejected" {
						return CommandResult{ExitCode: 1, Stderr: "non-fast-forward"}, true
					}
					if failure == "push ambiguous" {
						f.refHead = revisionCommit // Remote accepted the write before the connection broke.
						return CommandResult{Err: errors.New("connection lost")}, true
					}
				}
				if containsArgs(spec.Args, "worktree", "remove") {
					switch failure {
					case "cleanup rejected":
						return CommandResult{ExitCode: 1}, true
					case "cleanup directory remains":
						f.registered = false
						return CommandResult{}, true
					case "cleanup registration remains":
						if err := os.RemoveAll(f.workspace); err != nil {
							t.Fatal(err)
						}
						return CommandResult{}, true
					}
				}
				return CommandResult{}, false
			}
			code, out, diagnostic := f.execute()
			if strings.HasPrefix(failure, "cleanup") {
				if code != 0 || f.pushes != 1 || f.cleanups != 1 || !strings.Contains(out, "Updated existing PR #42") || !strings.Contains(diagnostic, "delivery confirmed, but cleanup failed") || !strings.Contains(diagnostic, f.workspace) || !strings.Contains(diagnostic, "do not retry delivery") {
					t.Fatalf("code=%d out=%s diagnostic=%s fixture=%+v", code, out, diagnostic, f)
				}
				return
			}
			if code != 1 || f.cleanups != 0 || !f.registered || !strings.Contains(diagnostic, "stopped at ") || !strings.Contains(diagnostic, f.workspace) || !strings.Contains(diagnostic, "local commit: ") {
				t.Fatalf("code=%d diagnostic=%s fixture=%+v", code, diagnostic, f)
			}
			if _, err := os.Stat(filepath.Join(f.workspace, "implementation.txt")); err != nil {
				t.Fatalf("useful worker state lost: %v", err)
			}
			if strings.HasPrefix(failure, "push") {
				if f.pushes != 1 || !strings.Contains(diagnostic, "remote branch may have been updated") || !strings.Contains(diagnostic, "local commit: "+revisionCommit) || !strings.Contains(diagnostic, "remote mutation attempted: true") {
					t.Fatal(diagnostic)
				}
			} else if f.pushes != 0 || !strings.Contains(diagnostic, "remote mutation attempted: false") {
				t.Fatal(diagnostic)
			}
			if failure == "log" && !strings.Contains(diagnostic, f.worker.Stdout) {
				t.Fatal("Author report lost on log failure")
			}
		})
	}
}

func TestReviseModeSpecificIdentity(t *testing.T) {
	for _, mode := range []string{"managed", "managed mismatched context", "unmanaged mismatched context", "unmanaged malformed context"} {
		t.Run(mode, func(t *testing.T) {
			f := newUnmanagedReviseFixture(t)
			// Configured upstream is R1; origin is the independent R2 (acme/iro).
			if err := os.WriteFile(filepath.Join(f.root, "iro.toml"), []byte(strings.Replace(configTemplate, `remote = "origin"`, `remote = "upstream"`, 1)), 0600); err != nil {
				t.Fatal(err)
			}
			unmanaged := strings.HasPrefix(mode, "unmanaged")
			if strings.Contains(mode, "mismatched") {
				t.Setenv("GH_REPO", "third/repository")
				t.Setenv("GH_HOST", "github.com")
			} else if strings.Contains(mode, "malformed") {
				t.Setenv("GH_REPO", "malformed selector")
				t.Setenv("GH_HOST", "invalid host")
			}
			if !unmanaged {
				f.service.FileSystem = NewOSFileSystem()
			}
			configuredReads, configuredQueries := 0, 0
			f.runner.fn = func(spec CommandSpec) CommandResult {
				if spec.Name == "git" && containsString(spec.Args, "remote.upstream.url") {
					if unmanaged {
						t.Fatal("unmanaged consulted configured repository")
					}
					configuredReads++
					return CommandResult{Stdout: "git@github.com:other/repo.git\n"}
				}
				if !unmanaged && spec.Name == "gh" && containsString(spec.Args, "graphql") {
					configuredQueries++
					if !containsString(spec.Args, "owner=other") || !containsString(spec.Args, "name=repo") || !containsString(spec.Args, "query="+reviewPreflightQuery) {
						t.Fatalf("managed did not target R1 with native relation checks: %v", spec.Args)
					}
					return CommandResult{ExitCode: 1}
				}
				return f.respond(spec)
			}
			args := []string{"revise", "42"}
			if unmanaged {
				args = append(args, "--unmanaged", "--issue", "123")
			}
			var diagnostic strings.Builder
			code := Execute(args, io.Discard, &diagnostic, f.service)
			if unmanaged {
				if code != 0 || f.pushes != 1 || configuredReads != 0 || configuredQueries != 0 {
					t.Fatalf("unmanaged R2 dispatch: %d %s", code, diagnostic.String())
				}
			} else {
				wantQueries := 1
				if strings.Contains(mode, "mismatched") {
					wantQueries = 0
					for _, call := range f.runner.calls {
						if call.Name == "gh" {
							t.Fatal("GitHub IO before managed context rejection")
						}
					}
				}
				if code != 1 || configuredReads != 1 || configuredQueries != wantQueries || f.workers != 0 {
					t.Fatalf("managed R1 dispatch: %d reads=%d queries=%d %s", code, configuredReads, configuredQueries, diagnostic.String())
				}
			}
		})
	}
}

func TestUnmanagedReviseG1PreservesManagedOwnershipAndRejectsStaleLocalState(t *testing.T) {
	managed := newReviseFixture(t, true)
	unmanaged := newUnmanagedReviseFixture(t)
	unmanaged.root = managed.root
	unmanaged.service.Dirs = managed.service.Dirs
	unmanaged.branch, unmanaged.base = "iro/issue-123", "main"
	unmanaged.pr["headRefName"], unmanaged.pr["baseRefName"] = unmanaged.branch, unmanaged.base
	mappingPath := ownershipPath(managed.service.Dirs, managed.identity, 123)
	before, err := os.ReadFile(mappingPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(managed.workspace, "human.txt"), []byte("managed file"), 0600); err != nil {
		t.Fatal(err)
	}
	if code, _, diagnostic := unmanaged.execute(); code != 0 {
		t.Fatal(diagnostic)
	}
	after, err := os.ReadFile(mappingPath)
	if err != nil || string(after) != string(before) || managed.head != revisionHead {
		t.Fatalf("managed ownership or HEAD changed: %s %v", after, err)
	}
	if data, err := os.ReadFile(filepath.Join(managed.workspace, "human.txt")); err != nil || string(data) != "managed file" {
		t.Fatalf("managed workspace changed: %s %v", data, err)
	}
	managed.target = strings.ReplaceAll(managed.target, revisionHead, unmanaged.refHead)
	managed.runner.fn = func(spec CommandSpec) CommandResult {
		if spec.Name == "git" && strings.Join(spec.Args, " ") == "ls-remote --heads -- origin refs/heads/iro/issue-123" {
			return CommandResult{Stdout: unmanaged.refHead + "\trefs/heads/iro/issue-123\n"}
		}
		return managed.respond(spec)
	}
	var diagnostic strings.Builder
	if code := Execute([]string{"revise", "42"}, io.Discard, &diagnostic, managed.service); code != 1 || !strings.Contains(diagnostic.String(), "differs from expected") {
		t.Fatalf("managed stale state accepted: %d %s", code, diagnostic.String())
	}
	if stages := revisionMutations(managed.runner.calls); len(stages) != 0 {
		t.Fatalf("managed repaired stale state: %v", stages)
	}
}

func TestUnmanagedReviseG3ExplicitIssueDoesNotReplaceManagedOrigin(t *testing.T) {
	managed := newReviseFixture(t, false)
	unmanaged := newUnmanagedReviseFixture(t)
	unmanaged.root = managed.root
	unmanaged.service.Dirs = managed.service.Dirs
	unmanaged.branch, unmanaged.base = "iro/issue-123", "main"
	unmanaged.pr["headRefName"], unmanaged.pr["baseRefName"] = unmanaged.branch, unmanaged.base
	unmanaged.pr["closingIssuesReferences"] = map[string]any{"totalCount": 1, "nodes": []any{map[string]any{"number": 123, "repository": map[string]any{"nameWithOwner": "acme/iro"}}}}
	unmanaged.intercept = func(spec CommandSpec) (CommandResult, bool) {
		if spec.Name == "gh" && containsArgs(spec.Args, "issue", "view") {
			if spec.Args[2] != "456" {
				t.Fatal("unmanaged reconciled explicit Issue B with native Issue A")
			}
			return CommandResult{Stdout: `{"number":456,"title":"Explicit B","body":"Use B","url":"https://github.com/acme/iro/issues/456","comments":[]}`}, true
		}
		if spec.Name == "codex" && containsString(spec.Args, "--ephemeral") && !strings.Contains(string(spec.Stdin), "Human-selected specification Issue:\nNumber: 456") {
			t.Fatal("explicit Issue not supplied to Author")
		}
		if spec.Name == "git" && strings.Join(spec.Args, " ") == "commit -m Revise issue #456 for PR #42" {
			unmanaged.head, unmanaged.dirty = revisionCommit, false
			return CommandResult{}, true
		}
		return CommandResult{}, false
	}
	var diagnostic strings.Builder
	if code := Execute([]string{"revise", "42", "--unmanaged", "--issue", "456"}, io.Discard, &diagnostic, unmanaged.service); code != 0 {
		t.Fatal(diagnostic.String())
	}
	prepareManagedAfterUnmanaged(t, managed, unmanaged.refHead, workflowTemplate)
	if code := Execute([]string{"revise", "42"}, io.Discard, &diagnostic, managed.service); code != 0 {
		t.Fatal(diagnostic.String())
	}
	found := false
	for _, call := range managed.runner.calls {
		if call.Name == "gh" && containsArgs(call.Args, "issue", "view") && call.Args[2] != "123" {
			t.Fatal("managed reconciled native Issue A with unmanaged Issue B")
		}
		if call.Name == "codex" && containsString(call.Args, "--ephemeral") {
			found = strings.Contains(string(call.Stdin), "Origin Issue:\nNumber: 123") && !strings.Contains(string(call.Stdin), "Explicit B")
		}
	}
	if !found {
		t.Fatal("managed Author did not use native Issue A")
	}
}

// Start a later managed invocation from the delivered remote commit, while all
// managed local resources remain absent. The managed path must materialize them.
func prepareManagedAfterUnmanaged(t *testing.T, f *reviseFixture, start, policy string) {
	t.Helper()
	f.target = strings.ReplaceAll(f.target, revisionHead, start)
	f.head = start
	next := strings.Repeat("b", 40)
	f.runner.fn = func(spec CommandSpec) CommandResult {
		if spec.Name == "git" {
			switch strings.Join(spec.Args, " ") {
			case "ls-remote --heads -- origin refs/heads/iro/issue-123":
				return CommandResult{Stdout: start + "\trefs/heads/iro/issue-123\n"}
			case "cat-file -t " + start:
				return CommandResult{Stdout: "commit\n"}
			case "worktree add -b iro/issue-123 " + f.workspace + " " + start:
				f.createWorktree()
				return CommandResult{}
			case "ls-tree -z " + start + " -- WORKFLOW.md":
				return CommandResult{Stdout: "100644 blob " + start + "\tWORKFLOW.md\x00"}
			case "cat-file blob " + start:
				return CommandResult{Stdout: policy}
			case "commit -m Revise issue #123 for PR #42":
				f.head, f.dirty = next, false
				return CommandResult{}
			case "rev-list --parents -n 1 HEAD":
				return CommandResult{Stdout: next + " " + start}
			}
		}
		return f.respond(spec)
	}
}

func TestUnmanagedReviseG5BuiltInPolicyThenManagedStartingPolicy(t *testing.T) {
	managed := newReviseFixture(t, false)
	unmanaged := newUnmanagedReviseFixture(t)
	unmanaged.root = managed.root
	unmanaged.service.Dirs = managed.service.Dirs
	unmanaged.branch, unmanaged.base = "iro/issue-123", "main"
	unmanaged.pr["headRefName"], unmanaged.pr["baseRefName"] = unmanaged.branch, unmanaged.base
	const p1, p2 = "WORKFLOW policy P1", "WORKFLOW policy P2 after revision"
	if err := os.WriteFile(filepath.Join(managed.root, "WORKFLOW.md"), []byte(p1), 0600); err != nil {
		t.Fatal(err)
	}
	unmanaged.intercept = func(spec CommandSpec) (CommandResult, bool) {
		if spec.Name == "codex" && containsString(spec.Args, "--ephemeral") {
			if !strings.Contains(string(spec.Stdin), "Built-in unmanaged Author policy") || strings.Contains(string(spec.Stdin), p1) || strings.Contains(string(spec.Stdin), p2) {
				t.Fatal("unmanaged loaded WORKFLOW as authority")
			}
			if err := os.WriteFile(filepath.Join(unmanaged.workspace, "WORKFLOW.md"), []byte(p2), 0600); err != nil {
				t.Fatal(err)
			}
		}
		return CommandResult{}, false
	}
	if code, _, diagnostic := unmanaged.execute(); code != 0 {
		t.Fatal(diagnostic)
	}
	prepareManagedAfterUnmanaged(t, managed, unmanaged.refHead, p2)
	managed.service.FileSystem = revisePolicyFiles{managed.service.FileSystem, t, managed.root}
	var diagnostic strings.Builder
	if code := Execute([]string{"revise", "42"}, io.Discard, &diagnostic, managed.service); code != 0 {
		t.Fatal(diagnostic.String())
	}
	policyReads, workers := 0, 0
	for _, call := range managed.runner.calls {
		if call.Name == "git" && containsArgs(call.Args, "cat-file", "blob") {
			policyReads++
		}
		if call.Name == "codex" && containsString(call.Args, "--ephemeral") {
			workers++
			if !strings.Contains(string(call.Stdin), "Fixed starting PR HEAD "+revisionCommit+" worker policy (WORKFLOW.md):\n"+p2) || strings.Contains(string(call.Stdin), p1) || strings.Contains(string(call.Stdin), "Built-in unmanaged Author policy") {
				t.Fatalf("later managed Author did not use P2 at delivered HEAD: %s", call.Stdin)
			}
		}
	}
	if policyReads != 1 || workers != 1 {
		t.Fatalf("policyReads=%d workers=%d", policyReads, workers)
	}
}

func TestManagedReviseStillRejectsSharedHead(t *testing.T) {
	f := newReviseFixture(t, false)
	other := strings.Replace(activeRevisionPR, `"number":42`, `"number":43`, 1)
	f.active = strings.Replace(f.active, activeRevisionPR, activeRevisionPR+","+other, 1)
	var diagnostic strings.Builder
	if code := Execute([]string{"revise", "42"}, io.Discard, &diagnostic, f.service); code != 1 {
		t.Fatal("managed shared-head PR unexpectedly accepted")
	}
	if stages := revisionMutations(f.runner.calls); len(stages) != 0 {
		t.Fatalf("managed proceeded with shared-head PR: %v %s", stages, diagnostic.String())
	}
}

func TestUnmanagedReviseLocalIntegrityAtEachBoundary(t *testing.T) {
	for _, boundary := range []string{"materialization", "before commit", "before push"} {
		for _, failure := range []string{"HEAD", "attached", "missing registration", "duplicate registration", "attached registration", "common directory", "Git directory", "missing directory"} {
			t.Run(boundary+"/"+failure, func(t *testing.T) {
				f := newUnmanagedReviseFixture(t)
				active := func() bool {
					switch boundary {
					case "materialization":
						return f.inspections >= 2
					case "before commit":
						return f.workers > 0
					default:
						return f.commits > 0
					}
				}
				f.intercept = func(spec CommandSpec) (CommandResult, bool) {
					if !active() {
						return CommandResult{}, false
					}
					if failure == "missing directory" && spec.Name == "git" {
						if err := os.RemoveAll(f.workspace); err != nil {
							t.Fatal(err)
						}
					}
					if spec.Name != "git" {
						return CommandResult{}, false
					}
					args := strings.Join(spec.Args, " ")
					switch {
					case failure == "HEAD" && args == "rev-parse HEAD":
						return CommandResult{Stdout: reviewBaseForTest}, true
					case failure == "attached" && spec.Args[0] == "symbolic-ref":
						return CommandResult{Stdout: "refs/heads/human/topic"}, true
					case failure == "common directory" && strings.Contains(args, "--git-common-dir") && spec.Dir == f.workspace:
						return CommandResult{Stdout: filepath.Join(f.root, "other", ".git")}, true
					case failure == "Git directory" && args == "rev-parse --absolute-git-dir":
						return CommandResult{Stdout: filepath.Join(f.root, ".git", "worktrees", "another-detached-worktree")}, true
					}
					if args == "worktree list --porcelain" {
						registration := "worktree " + f.workspace + "\nHEAD " + f.head + "\ndetached\n\n"
						switch failure {
						case "missing registration":
							return CommandResult{}, true
						case "duplicate registration":
							return CommandResult{Stdout: registration + registration}, true
						case "attached registration":
							return CommandResult{Stdout: strings.Replace(registration, "detached", "branch refs/heads/human/topic", 1)}, true
						}
					}
					return CommandResult{}, false
				}
				code, _, diagnostic := f.execute()
				wantCommits := 0
				if boundary == "before push" {
					wantCommits = 1
				}
				if code != 1 || f.commits != wantCommits || f.pushes != 0 || f.cleanups != 0 || !strings.Contains(diagnostic, "stopped at "+boundary) {
					t.Fatalf("code=%d diagnostic=%s fixture=%+v", code, diagnostic, f)
				}
			})
		}
	}
}

func TestUnmanagedReviseMaterializationAndFailedCommitDiagnostics(t *testing.T) {
	for _, failure := range []string{"worktree add", "unreadable Git directory", "initial dirty", "commit created but failed", "HEAD unreadable after commit"} {
		t.Run(failure, func(t *testing.T) {
			f := newUnmanagedReviseFixture(t)
			f.intercept = func(spec CommandSpec) (CommandResult, bool) {
				if spec.Name != "git" {
					return CommandResult{}, false
				}
				args := strings.Join(spec.Args, " ")
				switch {
				case failure == "worktree add" && containsArgs(spec.Args, "worktree", "add"):
					f.workspace = spec.Args[3]
					return CommandResult{ExitCode: 1}, true
				case failure == "unreadable Git directory" && args == "rev-parse --absolute-git-dir":
					return CommandResult{ExitCode: 1}, true
				case failure == "initial dirty" && spec.Args[0] == "status":
					return CommandResult{Stdout: "?? unexpected.txt\n"}, true
				case failure == "commit created but failed" && spec.Args[0] == "commit":
					f.head = revisionCommit
					return CommandResult{ExitCode: 1}, true
				case failure == "HEAD unreadable after commit" && f.commits > 0 && (spec.Args[0] == "rev-list" || args == "rev-parse HEAD"):
					return CommandResult{ExitCode: 1}, true
				}
				return CommandResult{}, false
			}
			code, _, diagnostic := f.execute()
			if code != 1 || f.pushes != 0 || f.cleanups != 0 || !strings.Contains(diagnostic, f.workspace) || !strings.Contains(diagnostic, "remote mutation attempted: false") {
				t.Fatalf("code=%d diagnostic=%s fixture=%+v", code, diagnostic, f)
			}
			if failure == "commit created but failed" && !strings.Contains(diagnostic, "local commit: "+revisionCommit) || failure == "HEAD unreadable after commit" && !strings.Contains(diagnostic, "local commit: unknown") {
				t.Fatal(diagnostic)
			}
			if strings.Contains(diagnostic, "stopped at materialization") && f.workers != 0 {
				t.Fatal("Author ran in invalid workspace")
			}
		})
	}
}
