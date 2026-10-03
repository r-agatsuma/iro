package iro

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const revisionHead = "0123456789abcdef0123456789abcdef01234567"
const revisionCommit = "abcdef0123456789abcdef0123456789abcdef01"
const activeRevisionPR = `{"number":42,"headRefName":"iro/issue-123","headRepository":{"nameWithOwner":"acme/iro"},"closingIssuesReferences":{"totalCount":1,"nodes":[{"number":123,"repository":{"nameWithOwner":"acme/iro"}}]}}`

type reviseFixture struct {
	t                  *testing.T
	service            *Service
	runner             *fakeCommandRunner
	root, workspace    string
	identity           RepositoryIdentity
	target, active     string
	head, registration string
	otherRegistrations string
	branchName         string
	detached           bool
	branch, dirty      bool
	worker             CommandResult
}

func newReviseFixture(t *testing.T, existing bool) *reviseFixture {
	t.Helper()
	f := &reviseFixture{t: t, root: t.TempDir(), identity: RepositoryIdentity{Owner: "acme", Name: "iro"}, head: revisionHead, branchName: "iro/issue-123", worker: CommandResult{Stdout: "修正しました。テスト成功。"}}
	writeProjectFiles(t, f.root)
	f.runner = &fakeCommandRunner{}
	f.service = newTestService(t, f.runner, f.root)
	f.service.newDeliveryID = func() (deliveryID, error) { return deliveryID(strings.Repeat("a", 32)), nil }
	f.workspace = deliveryWorktreePath(f.service.Dirs, githubRuntimeNamespace(f.identity), 123, deliveryID(strings.Repeat("a", 32)))
	f.target = strings.NewReplacer(`"human-feature"`, `"iro/issue-123"`, `"contributor/iro"`, `"acme/iro"`, reviewHeadForTest, revisionHead).Replace(reviewResponseForTest)
	f.active = strings.Replace(deliveryResponseForTest, `"nodes":[]`, `"nodes":[`+activeRevisionPR+`]`, 1)
	if existing {
		f.createWorktree()
		if err := f.service.writeOwnership(ownershipPath(f.service.Dirs, f.identity, 123), f.mapping()); err != nil {
			t.Fatal(err)
		}
	}
	f.runner.fn = f.respond
	return f
}

func (f *reviseFixture) mapping() ownershipMapping {
	return ownershipMapping{Version: 1, Repository: f.identity.Canonical(), IssueNumber: 123, Branch: "iro/issue-123", Worktree: f.workspace, CreatedAt: "2025-01-02T03:04:05Z"}
}

func (f *reviseFixture) createWorktree() {
	f.t.Helper()
	if err := os.MkdirAll(f.workspace, 0755); err != nil {
		f.t.Fatal(err)
	}
	f.branch = true
	mode := "branch refs/heads/" + f.branchName
	if f.detached {
		mode = "detached"
	}
	f.registration = fmt.Sprintf("worktree %s\nHEAD %s\n%s\n\n", f.workspace, f.head, mode)
}

func (f *reviseFixture) respond(spec CommandSpec) CommandResult {
	if spec.Name == "git" {
		switch strings.Join(spec.Args, " ") {
		case "rev-parse --show-toplevel":
			return CommandResult{Stdout: f.root}
		case "config --get-all remote.origin.url", "remote get-url --push --all origin":
			return CommandResult{Stdout: "git@github.com:acme/iro.git\n"}
		case "ls-remote -- origin", "ls-remote --heads -- origin refs/heads/" + f.branchName:
			return CommandResult{Stdout: revisionHead + "\trefs/heads/" + f.branchName + "\n"}
		case "show-ref --verify --quiet refs/heads/iro/issue-123":
			if f.branch {
				return CommandResult{}
			}
			return CommandResult{ExitCode: 1}
		case "for-each-ref --sort=refname --format=%(refname)%00%(objectname) refs/heads/iro/":
			if f.branch && !f.detached {
				return CommandResult{Stdout: "refs/heads/" + f.branchName + "\x00" + f.head + "\n"}
			}
			return CommandResult{}
		case "worktree list --porcelain -z":
			registration := strings.ReplaceAll(f.registration, revisionHead, f.head)
			return CommandResult{Stdout: strings.ReplaceAll("worktree "+f.root+"\nHEAD "+revisionHead+"\nbranch refs/heads/main\n\n"+registration, "\n", "\x00")}
		case "rev-parse --absolute-git-dir":
			return CommandResult{Stdout: filepath.Join(f.root, ".git", "worktrees", "revision")}
		case "rev-parse --path-format=absolute --git-common-dir":
			return CommandResult{Stdout: filepath.Join(f.root, ".git")}
		case "symbolic-ref --quiet HEAD":
			if f.detached {
				return CommandResult{ExitCode: 1}
			}
			return CommandResult{Stdout: "refs/heads/" + f.branchName + "\n"}
		case "ls-tree -z " + revisionHead + " -- WORKFLOW.md":
			return CommandResult{Stdout: "100644 blob " + revisionHead + "\tWORKFLOW.md\x00"}
		case "cat-file blob " + revisionHead:
			return CommandResult{Stdout: workflowTemplate}
		case "rev-parse HEAD":
			return CommandResult{Stdout: f.head}
		case "--no-optional-locks status --porcelain --untracked-files=all":
			if f.dirty {
				return CommandResult{Stdout: " M human.txt\n"}
			}
			return CommandResult{}
		case "fetch --no-tags --no-write-fetch-head --refmap= -- origin refs/heads/" + f.branchName:
			return CommandResult{}
		case "cat-file -t " + revisionHead, "cat-file -t " + f.head:
			return CommandResult{Stdout: "commit\n"}
		case "worktree add -b iro/issue-123 " + f.workspace + " " + revisionHead:
			f.createWorktree()
			return CommandResult{}
		case "diff --check", "diff --cached --check", "add --all":
			return CommandResult{}
		case "diff --cached --quiet --exit-code":
			return CommandResult{ExitCode: 1, Err: errors.New("diff exists")}
		case "commit -m Revise issue #123 for PR #42":
			f.head, f.dirty = revisionCommit, false
			return CommandResult{}
		case "rev-list --parents -n 1 HEAD":
			return CommandResult{Stdout: revisionCommit + " " + revisionHead}
		case "write-tree", "rev-parse HEAD^{tree}":
			return CommandResult{Stdout: revisionHead}
		case "push --no-follow-tags --no-recurse-submodules -- origin refs/heads/" + f.branchName + ":refs/heads/" + f.branchName, "push --no-follow-tags --no-recurse-submodules -- origin " + revisionCommit + ":refs/heads/" + f.branchName:
			return CommandResult{}
		}
	}
	if spec.Name == "git" && containsArgs(spec.Args, "worktree", "add") {
		f.workspace = spec.Args[len(spec.Args)-2]
		f.detached = containsString(spec.Args, "--detach")
		f.createWorktree()
		return CommandResult{}
	}
	if spec.Name == "gh" {
		if containsString(spec.Args, "graphql") {
			if containsString(spec.Args, "query="+managedReviewPreflightQuery) || containsString(spec.Args, "query="+revisePushTargetQuery) {
				return CommandResult{Stdout: f.target}
			}
			if containsString(spec.Args, "query="+githubOriginCandidateQuery) {
				return reviewCandidateForTest(123)
			}
			f.t.Fatalf("unexpected relation/topology query: %v", spec.Args)
		}
		if containsString(spec.Args, "comment") || containsString(spec.Args, "POST") || containsString(spec.Args, "checkout") || containsString(spec.Args, "clone") {
			f.t.Fatalf("unexpected tracker mutation or disposable workspace: %v", spec.Args)
		}
		return reviewFakeResult(spec, f.root, "unused")
	}
	if spec.Name == "codex" {
		if containsArgs(spec.Args, "login", "status") {
			return CommandResult{}
		}
		f.dirty = true
		return f.worker
	}
	f.t.Fatalf("unexpected command: %+v", spec)
	return CommandResult{ExitCode: 1}
}

func revisionMutations(calls []CommandSpec) []string {
	var stages []string
	for _, call := range calls {
		if call.Name == "codex" && containsString(call.Args, "--ephemeral") {
			stages = append(stages, "worker")
		}
		if call.Name == "git" && len(call.Args) > 0 {
			switch call.Args[0] {
			case "fetch", "add", "commit", "push", "reset", "clean", "stash", "checkout", "switch", "restore", "branch":
				stages = append(stages, call.Args[0])
			case "worktree":
				if call.Args[1] != "list" {
					stages = append(stages, "worktree "+call.Args[1])
				}
			}
		}
	}
	return stages
}

func TestRevisePassesNormalizedFeedbackToFreshAuthor(t *testing.T) {
	for name, pageCount := range map[string]int{"single page": 1, "multiple pages": 2} {
		t.Run(name, func(t *testing.T) {
			f := newReviseFixture(t, false)
			f.runner.fn = func(spec CommandSpec) CommandResult {
				for _, feedback := range reviewFeedbackPagesForTest {
					if spec.Name == "gh" && containsString(spec.Args, feedback.endpoint) {
						return CommandResult{Stdout: strings.Join(feedback.pages[:pageCount], "\n")}
					}
				}
				return f.respond(spec)
			}
			if err := f.service.Revise(42, io.Discard); err != nil {
				t.Fatal(err)
			}
			assertNormalizedFeedbackInput(t, f.runner.calls, pageCount)
		})
	}
}

func TestReviseRejectsInvalidFeedbackBeforeMutation(t *testing.T) {
	for _, feedback := range reviewFeedbackPagesForTest {
		for _, failure := range reviewFeedbackFailuresForTest {
			t.Run(feedback.label+"/"+failure.name, func(t *testing.T) {
				f := newReviseFixture(t, false)
				f.runner.fn = func(spec CommandSpec) CommandResult {
					if spec.Name == "gh" && containsString(spec.Args, feedback.endpoint) {
						return failure.result
					}
					return f.respond(spec)
				}
				want := failure.wantPrefix + feedback.label + " for PR #42"
				if err := f.service.Revise(42, io.Discard); err == nil || !strings.Contains(err.Error(), want) {
					t.Fatalf("Revise() error = %v, want %q", err, want)
				}
				if stages := revisionMutations(f.runner.calls); len(stages) != 0 {
					t.Fatal("mutation before context rejection", stages)
				}
				for _, path := range []string{f.workspace, ownershipPath(f.service.Dirs, f.identity, 123)} {
					if _, err := os.Stat(path); !os.IsNotExist(err) {
						t.Fatalf("invalid context created local state %s: %v", path, err)
					}
				}
			})
		}
	}
}

func TestReviseMaterializesOrReusesHumanPRAndPushesSameBranch(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprintf("existing=%t", existing), func(t *testing.T) {
			f := newReviseFixture(t, existing)
			mappingPath := ownershipPath(f.service.Dirs, f.identity, 123)
			before, _ := os.ReadFile(mappingPath)
			var stdout, stderr strings.Builder
			if status := Execute([]string{"revise", "42"}, &stdout, &stderr, f.service); status != 0 {
				t.Fatalf("status=%d: %s", status, stderr.String())
			}
			if !strings.Contains(stdout.String(), "Updated existing PR #42 via iro/issue-123") || !strings.Contains(stdout.String(), "Author report:") {
				t.Fatal(stdout.String())
			}
			mapping, present, err := f.service.readOwnership(mappingPath)
			if err != nil || present != existing {
				t.Fatalf("mapping=%+v, present=%t, error=%v", mapping, present, err)
			}
			after, _ := os.ReadFile(mappingPath)
			if string(before) != string(after) {
				t.Fatal("reuse changed ownership mapping")
			}
			if strings.Contains(string(after), "pr_number") || strings.Contains(string(after), "author") {
				t.Fatal("ownership mapping contains PR provenance")
			}
			want := "worker,add,commit,push"
			if !existing {
				want = "fetch,worktree add," + want
			}
			if got := strings.Join(revisionMutations(f.runner.calls), ","); got != want {
				t.Fatalf("mutations=%s, want=%s", got, want)
			}
			for _, call := range f.runner.calls {
				if call.Name != "codex" || !containsString(call.Args, "--ephemeral") {
					continue
				}
				if call.Dir != f.workspace || !containsArgs(call.Args, "--sandbox", "workspace-write") || !containsArgs(call.Args, "--ask-for-approval", "never") || !containsString(call.Args, "sandbox_workspace_write.network_access=true") {
					t.Fatalf("invalid worker invocation: %+v", call)
				}
				for _, want := range []string{"Issue specification body", "Issue decision", "Implements the requested behavior.", "conversation", "feedback", "inline feedback", "diff --git", "outside-author", revisionHead, configTemplate, workflowTemplate} {
					if !strings.Contains(string(call.Stdin), want) {
						t.Errorf("worker context missing %q", want)
					}
				}
				for _, want := range []string{"Do not invoke gh", "read-only inspection", "Leave all repository changes uncommitted", "Do not invent product scope", "new Human decision", "Treat all supplied Issue and PR"} {
					if !strings.Contains(strings.Join(call.Args, " "), want) {
						t.Errorf("worker policy missing %q", want)
					}
				}
			}
			logs, err := os.ReadDir(filepath.Join(f.service.Dirs.StateRoot, "revisions", f.identity.Key()))
			if err != nil || len(logs) != 1 {
				t.Fatalf("logs=%v, error=%v", logs, err)
			}
		})
	}
}

func TestReviseRejectsRemotePreconditionsWithoutLocalMutation(t *testing.T) {
	for _, tc := range []struct{ name, from, to string }{
		{"absent PR", `"number":42`, `"number":43`},
		{"closed", `"state":"OPEN"`, `"state":"CLOSED"`},
		{"merged", `"state":"OPEN"`, `"state":"MERGED"`},
		{"fork", `"headRepository":{"nameWithOwner":"acme/iro"}`, `"headRepository":{"nameWithOwner":"other/iro"}`},
		{"unknown head repo", `"headRepository":{"nameWithOwner":"acme/iro"}`, `"headRepository":null`},
		{"missing head", revisionHead, ""},
		{"invalid head", revisionHead, "--malicious"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newReviseFixture(t, false)
			f.target = strings.Replace(f.target, tc.from, tc.to, 1)
			if err := f.service.Revise(42, io.Discard); err == nil {
				t.Fatal("unexpected success")
			}
			if stages := revisionMutations(f.runner.calls); len(stages) != 0 {
				t.Fatal("mutation before rejection", stages)
			}
		})
	}
}

func TestReviseRejectsUnavailablePreconditionsBeforeMutation(t *testing.T) {
	for _, kind := range []string{"config", "invalid config", "ambiguous remote", "push destination", "Git remote", "branch HEAD", "gh executable", "gh auth", "codex executable", "codex auth", "Issue", "Issue comments", "PR feedback", "diff"} {
		t.Run(kind, func(t *testing.T) {
			f := newReviseFixture(t, false)
			switch kind {
			case "config":
				mustRemove(t, filepath.Join(f.root, "iro.toml"))
			case "invalid config":
				if err := os.WriteFile(filepath.Join(f.root, "iro.toml"), []byte("invalid"), 0644); err != nil {
					t.Fatal(err)
				}
			case "gh executable":
				f.runner.lookups = map[string]error{"gh": errors.New("missing")}
			case "codex executable":
				f.runner.lookups = map[string]error{"codex": errors.New("missing")}
			}
			f.runner.fn = func(spec CommandSpec) CommandResult {
				if spec.Name == "git" {
					switch {
					case kind == "ambiguous remote" && spec.Args[0] == "config":
						return CommandResult{Stdout: "git@github.com:acme/iro.git\ngit@github.com:other/iro.git\n"}
					case kind == "push destination" && spec.Args[0] == "remote":
						return CommandResult{Stdout: "git@github.com:other/iro.git\n"}
					case kind == "Git remote" && spec.Args[0] == "ls-remote":
						return CommandResult{ExitCode: 1}
					case kind == "branch HEAD" && containsString(spec.Args, "--heads"):
						return CommandResult{Stdout: revisionCommit + "\trefs/heads/iro/issue-123\n"}
					}
				}
				if kind == "gh auth" && spec.Name == "gh" && spec.Args[0] == "auth" || kind == "codex auth" && spec.Name == "codex" && spec.Args[0] == "login" || kind == "Issue" && spec.Name == "gh" && spec.Args[0] == "issue" || kind == "diff" && spec.Name == "gh" && containsString(spec.Args, "diff") {
					return CommandResult{ExitCode: 1}
				}
				if kind == "Issue comments" && spec.Name == "gh" && spec.Args[0] == "issue" {
					result := f.respond(spec)
					result.Stdout = strings.Replace(result.Stdout, `"id":"IC_1"`, `"id":""`, 1)
					return result
				}
				if kind == "PR feedback" && containsString(spec.Args, "repos/acme/iro/pulls/42/comments") {
					return CommandResult{Stdout: "invalid JSON"}
				}
				return f.respond(spec)
			}
			if err := f.service.Revise(42, io.Discard); err == nil {
				t.Fatal("unexpected success")
			}
			if stages := revisionMutations(f.runner.calls); len(stages) != 0 {
				t.Fatal("mutation before rejection", stages)
			}
			for _, path := range []string{f.workspace, ownershipPath(f.service.Dirs, f.identity, 123)} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("preflight created local state %s: %v", path, err)
				}
			}
		})
	}
}

func TestReviseAllowsUnknownCreatorAndDraft(t *testing.T) {
	f := newReviseFixture(t, false)
	f.target = strings.NewReplacer(`"author":{"login":"outside-author"}`, `"author":null`, `"isDraft":false`, `"isDraft":true`).Replace(f.target)
	if err := f.service.Revise(42, io.Discard); err != nil {
		t.Fatal(err)
	}
}

func mustRemove(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}

func TestReviseFailuresPreservePartialStateAndStopDelivery(t *testing.T) {
	for _, tc := range []struct{ name, want, stages string }{
		{"worker", "Author exited", "worker"},
		{"empty report", "no work report", "worker"},
		{"diff check", "validation/staging failed", "worker"},
		{"stage", "validation/staging failed", "worker,add"},
		{"cached check", "validation/staging failed", "worker,add"},
		{"empty diff", "no committable changes", "worker,add"},
		{"diff inspection", "inspect staged revision", "worker,add"},
		{"commit", "revision commit failed", "worker,add,commit"},
		{"commit parent", "no push attempted", "worker,add,commit"},
		{"push", "remote branch may have been updated", "worker,add,commit,push"},
		{"relation after worker", "no push attempted", "worker,add,commit"},
		{"head after worker", "no push attempted", "worker,add,commit"},
		{"local head after worker", "divergent state", "worker"},
		{"relation after commit", "no push attempted", "worker,add,commit"},
		{"head after commit", "no push attempted", "worker,add,commit"},
		{"dirty after commit", "no push attempted", "worker,add,commit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newReviseFixture(t, true)
			workerDone := false
			f.runner.fn = func(spec CommandSpec) CommandResult {
				args := strings.Join(spec.Args, " ")
				if spec.Name == "codex" && containsString(spec.Args, "--ephemeral") {
					workerDone = true
					if tc.name == "worker" {
						f.worker = CommandResult{ExitCode: 7, Stdout: "判断が必要です。", Stderr: "failure details"}
					}
					if tc.name == "empty report" {
						f.worker = CommandResult{}
					}
					if tc.name == "local head after worker" {
						f.head = revisionCommit
					}
				}
				if workerDone && spec.Name == "gh" && containsString(spec.Args, "query="+revisePushTargetQuery) {
					if tc.name == "relation after worker" || tc.name == "relation after commit" && f.head == revisionCommit {
						return CommandResult{Stdout: strings.Replace(f.target, `"state":"OPEN"`, `"state":"CLOSED"`, 1)}
					}
					if tc.name == "head after worker" || tc.name == "head after commit" && f.head == revisionCommit {
						return CommandResult{Stdout: strings.Replace(f.target, `"headRefName":"iro/issue-123"`, `"headRefName":"iro/changed"`, 1)}
					}
				}
				if spec.Name == "git" {
					switch {
					case tc.name == "diff check" && args == "diff --check", tc.name == "stage" && args == "add --all", tc.name == "cached check" && args == "diff --cached --check", tc.name == "commit" && spec.Args[0] == "commit", tc.name == "push" && spec.Args[0] == "push":
						return CommandResult{ExitCode: 1}
					case tc.name == "empty diff" && args == "diff --cached --quiet --exit-code":
						return CommandResult{}
					case tc.name == "diff inspection" && args == "diff --cached --quiet --exit-code":
						return CommandResult{ExitCode: 2}
					case tc.name == "commit parent" && spec.Args[0] == "rev-list":
						return CommandResult{Stdout: revisionCommit + " " + revisionCommit}
					case tc.name == "dirty after commit" && spec.Args[0] == "commit":
						result := f.respond(spec)
						f.dirty = true
						return result
					}
				}
				return f.respond(spec)
			}
			err := f.service.Revise(42, io.Discard)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v, want=%s", err, tc.want)
			}
			if stages := strings.Join(revisionMutations(f.runner.calls), ","); stages != tc.stages {
				t.Fatalf("stages=%s, want=%s", stages, tc.stages)
			}
			if _, err := os.Stat(f.workspace); err != nil {
				t.Fatal("worktree was removed", err)
			}
			if _, err := os.Stat(ownershipPath(f.service.Dirs, f.identity, 123)); err != nil {
				t.Fatal("mapping was removed", err)
			}
		})
	}
}

func TestReviseMaterializationFailures(t *testing.T) {
	for _, tc := range []struct{ name, want, stages string }{
		{"fetch", "could not fetch", "fetch"},
		{"missing object", "was not obtained", "fetch"},
		{"remote drift", "no longer OPEN", "fetch,worktree add,worker,add,commit"},
		{"worktree", "partial local state may remain", "fetch,worktree add"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newReviseFixture(t, false)
			fetched := false
			f.runner.fn = func(spec CommandSpec) CommandResult {
				if spec.Name == "git" && spec.Args[0] == "fetch" {
					fetched = true
					if tc.name == "fetch" {
						return CommandResult{ExitCode: 1}
					}
				}
				if tc.name == "missing object" && spec.Name == "git" && spec.Args[0] == "cat-file" {
					return CommandResult{ExitCode: 1}
				}
				if tc.name == "remote drift" && fetched && containsString(spec.Args, "query="+revisePushTargetQuery) {
					return CommandResult{Stdout: strings.Replace(f.target, `"headRefName":"iro/issue-123"`, `"headRefName":"iro/changed"`, 1)}
				}
				if spec.Name == "git" && containsArgs(spec.Args, "worktree", "add") {
					if tc.name == "worktree" {
						return CommandResult{ExitCode: 1}
					}
				}
				return f.respond(spec)
			}
			err := f.service.Revise(42, io.Discard)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v, want=%s", err, tc.want)
			}
			if stages := strings.Join(revisionMutations(f.runner.calls), ","); stages != tc.stages {
				t.Fatalf("stages=%s, want=%s", stages, tc.stages)
			}
		})
	}
}

func TestExecuteRejectsInvalidReviseNumber(t *testing.T) {
	service := NewService(&fakeCommandRunner{}, NewOSFileSystem())
	for _, args := range [][]string{{"revise"}, {"revise", "0"}, {"revise", "-1"}, {"revise", "1", "2"}, {"revise", "https://github.com/acme/iro/pull/42"}, {"revise", "9999999999999999999999999999999"}} {
		if status := Execute(args, io.Discard, io.Discard, service); status != 2 {
			t.Fatalf("Execute(%v)=%d", args, status)
		}
	}
}

// Revise must not even inspect the invocation checkout's WORKFLOW.
type revisePolicyFiles struct {
	FileSystem
	t    *testing.T
	root string
}

func (f revisePolicyFiles) Stat(path string) (os.FileInfo, error) {
	if path == filepath.Join(f.root, "WORKFLOW.md") {
		f.t.Fatal("inspected invocation WORKFLOW")
	}
	return f.FileSystem.Stat(path)
}

func (f revisePolicyFiles) ReadFile(path string) ([]byte, error) {
	if path == filepath.Join(f.root, "WORKFLOW.md") {
		f.t.Fatal("read invocation WORKFLOW")
	}
	return f.FileSystem.ReadFile(path)
}

func TestReviseUsesFixedStartingPolicyAcrossExplicitInvocations(t *testing.T) {
	for _, existing := range []bool{false, true} {
		for _, absent := range []bool{false, true} {
			t.Run(fmt.Sprintf("existing=%t/absent=%t", existing, absent), func(t *testing.T) {
				f := newReviseFixture(t, existing)
				if absent {
					mustRemove(t, filepath.Join(f.root, "WORKFLOW.md"))
				}
				f.service.FileSystem = revisePolicyFiles{f.service.FileSystem, t, f.root}
				const p1 = "Starting policy P1\n"
				const p2 = "Edited policy P2\n"
				policy, start, next := p1, revisionHead, revisionCommit
				reads, workers, pushes := 0, 0, 0
				f.runner.fn = func(spec CommandSpec) CommandResult {
					args := strings.Join(spec.Args, " ")
					if spec.Name == "git" {
						switch args {
						case "ls-tree -z " + start + " -- WORKFLOW.md":
							if f.head != start || f.dirty || !f.branch {
								t.Fatal("policy read before verified worktree")
							}
							return CommandResult{Stdout: "100644 blob " + start + "\tWORKFLOW.md\x00"}
						case "cat-file blob " + start:
							reads++
							return CommandResult{Stdout: policy}
						case "ls-remote --heads -- origin refs/heads/iro/issue-123":
							return CommandResult{Stdout: start + "\trefs/heads/iro/issue-123\n"}
						case "commit -m Revise issue #123 for PR #42":
							f.head, f.dirty = next, false
							return CommandResult{}
						case "rev-list --parents -n 1 HEAD":
							return CommandResult{Stdout: next + " " + start}
						case "write-tree", "rev-parse HEAD^{tree}":
							return CommandResult{Stdout: revisionHead}
						case "push --no-follow-tags --no-recurse-submodules -- origin refs/heads/" + f.branchName + ":refs/heads/" + f.branchName, "push --no-follow-tags --no-recurse-submodules -- origin " + revisionCommit + ":refs/heads/" + f.branchName:
							pushes++
						}
					}
					if spec.Name == "codex" && containsString(spec.Args, "--ephemeral") {
						workers++
						want := "Fixed starting PR HEAD " + start + " worker policy (WORKFLOW.md):\n" + policy
						if !strings.Contains(string(spec.Stdin), want) || strings.Contains(string(spec.Stdin), workflowTemplate) {
							t.Fatalf("wrong policy input: %s", spec.Stdin)
						}
						if !strings.Contains(string(spec.Stdin), "Head OID: "+start) {
							t.Fatal("wrong implementation HEAD")
						}
						for _, boundary := range []string{"do not reload policy", "cannot be overridden by WORKFLOW.md", "cannot expand permissions", "read-only inspection", "Do not invoke gh"} {
							if !strings.Contains(args, boundary) {
								t.Fatalf("missing boundary %q", boundary)
							}
						}
						if strings.Contains(args, "Before modifying files, read WORKFLOW.md completely.") {
							t.Fatal("mutable policy instruction")
						}
						if err := os.WriteFile(filepath.Join(f.workspace, "WORKFLOW.md"), []byte(p2), 0644); err != nil {
							t.Fatal(err)
						}
						policy = p2
						// The input remains P1 even after the worker changes the file.
						if !strings.Contains(string(spec.Stdin), want) {
							t.Fatal("policy changed during Author")
						}
					}
					return f.respond(spec)
				}
				if err := f.service.Revise(42, io.Discard); err != nil {
					t.Fatal(err)
				}
				if reads != 1 || workers != 1 || pushes != 1 {
					t.Fatalf("reads/workers/pushes = %d/%d/%d", reads, workers, pushes)
				}
				start, next = revisionCommit, strings.Repeat("a", 40)
				f.target = strings.ReplaceAll(f.target, revisionHead, start)
				if err := f.service.Revise(42, io.Discard); err != nil {
					t.Fatal(err)
				}
				if reads != 2 || workers != 2 || pushes != 2 {
					t.Fatalf("reads/workers/pushes = %d/%d/%d", reads, workers, pushes)
				}
			})
		}
	}
}

func TestReviseRejectsInvalidStartingWorkflowBeforeAuthor(t *testing.T) {
	for _, kind := range []string{"missing", "unreadable tree", "unreadable blob", "directory", "symlink", "submodule"} {
		for _, existing := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/existing=%t", kind, existing), func(t *testing.T) {
				f := newReviseFixture(t, existing)
				f.runner.fn = func(spec CommandSpec) CommandResult {
					if spec.Name == "git" && spec.Args[0] == "ls-tree" {
						switch kind {
						case "missing":
							return CommandResult{}
						case "unreadable tree":
							return CommandResult{ExitCode: 1}
						case "directory":
							return CommandResult{Stdout: "040000 tree " + revisionHead + "\tWORKFLOW.md\x00"}
						case "symlink":
							return CommandResult{Stdout: "120000 blob " + revisionHead + "\tWORKFLOW.md\x00"}
						case "submodule":
							return CommandResult{Stdout: "160000 commit " + revisionHead + "\tWORKFLOW.md\x00"}
						}
					}
					if kind == "unreadable blob" && spec.Name == "git" && containsArgs(spec.Args, "cat-file", "blob") {
						return CommandResult{ExitCode: 1}
					}
					return f.respond(spec)
				}
				if err := f.service.Revise(42, io.Discard); err == nil || !strings.Contains(err.Error(), "WORKFLOW.md") {
					t.Fatalf("error = %v", err)
				}
				for _, stage := range revisionMutations(f.runner.calls) {
					if stage == "worker" || stage == "add" || stage == "commit" || stage == "push" {
						t.Fatalf("unexpected stage %s", stage)
					}
				}
			})
		}
	}
}
