package iro

import (
	"encoding/json"
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
	branch, dirty      bool
	worker             CommandResult
}

func newReviseFixture(t *testing.T, existing bool) *reviseFixture {
	t.Helper()
	f := &reviseFixture{t: t, root: t.TempDir(), identity: RepositoryIdentity{Owner: "acme", Name: "iro"}, head: revisionHead, worker: CommandResult{Stdout: "修正しました。テスト成功。"}}
	writeProjectFiles(t, f.root)
	f.runner = &fakeCommandRunner{}
	f.service = newTestService(t, f.runner, f.root)
	f.workspace = worktreePath(f.service.Dirs, f.identity, 123)
	f.target = strings.NewReplacer(`"human-feature"`, `"iro/issue-123"`, `"contributor/iro"`, `"acme/iro"`, "0123456789abcdef", revisionHead).Replace(reviewResponseForTest)
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
	f.registration = fmt.Sprintf("worktree %s\nHEAD %s\nbranch refs/heads/iro/issue-123\n\n", f.workspace, f.head)
}

func (f *reviseFixture) respond(spec CommandSpec) CommandResult {
	if spec.Name == "git" {
		switch strings.Join(spec.Args, " ") {
		case "rev-parse --show-toplevel":
			return CommandResult{Stdout: f.root}
		case "config --get-all remote.origin.url", "remote get-url --push --all origin":
			return CommandResult{Stdout: "git@github.com:acme/iro.git\n"}
		case "ls-remote -- origin", "ls-remote --heads -- origin refs/heads/iro/issue-123":
			return CommandResult{Stdout: revisionHead + "\trefs/heads/iro/issue-123\n"}
		case "show-ref --verify --quiet refs/heads/iro/issue-123":
			if f.branch {
				return CommandResult{}
			}
			return CommandResult{ExitCode: 1}
		case "worktree list --porcelain":
			return CommandResult{Stdout: "worktree " + f.root + "\nbranch refs/heads/main\n\n" + f.registration}
		case "rev-parse --path-format=absolute --git-common-dir":
			return CommandResult{Stdout: filepath.Join(f.root, ".git")}
		case "symbolic-ref --quiet HEAD":
			return CommandResult{Stdout: "refs/heads/iro/issue-123\n"}
		case "rev-parse HEAD":
			return CommandResult{Stdout: f.head}
		case "--no-optional-locks status --porcelain --untracked-files=all":
			if f.dirty {
				return CommandResult{Stdout: " M human.txt\n"}
			}
			return CommandResult{}
		case "fetch --no-tags --no-write-fetch-head --refmap= -- origin refs/heads/iro/issue-123":
			return CommandResult{}
		case "cat-file -t " + revisionHead:
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
		case "push -- origin refs/heads/iro/issue-123:refs/heads/iro/issue-123":
			return CommandResult{}
		}
	}
	if spec.Name == "gh" {
		if containsString(spec.Args, "graphql") {
			if containsString(spec.Args, "query="+reviewPreflightQuery) {
				return CommandResult{Stdout: f.target}
			}
			if !strings.Contains(strings.Join(spec.Args, " "), "states:[OPEN]") {
				f.t.Fatalf("revise did not query active PRs: %v", spec.Args)
			}
			return CommandResult{Stdout: f.active}
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
			if err != nil || !present || validateOwnershipMapping(mapping, 123, f.identity, f.service.Dirs) != nil {
				t.Fatalf("mapping=%+v, present=%t, error=%v", mapping, present, err)
			}
			after, _ := os.ReadFile(mappingPath)
			if existing && string(before) != string(after) {
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
		{"base mismatch", `"baseRefName":"main"`, `"baseRefName":"release"`},
		{"missing default", `"defaultBranchRef":{"name":"main"}`, `"defaultBranchRef":null`},
		{"noncanonical branch", `"headRefName":"iro/issue-123"`, `"headRefName":"human-branch"`},
		{"wrong Issue branch", `"headRefName":"iro/issue-123"`, `"headRefName":"iro/issue-124"`},
		{"fork", `"headRepository":{"nameWithOwner":"acme/iro"}`, `"headRepository":{"nameWithOwner":"other/iro"}`},
		{"unknown head repo", `"headRepository":{"nameWithOwner":"acme/iro"}`, `"headRepository":null`},
		{"no origin", `"totalCount":1`, `"totalCount":0`},
		{"multiple origins", `"totalCount":1`, `"totalCount":2`},
		{"foreign origin", `"repository":{"nameWithOwner":"acme/iro"}`, `"repository":{"nameWithOwner":"other/iro"}`},
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
	for _, kind := range []string{"workflow", "config", "invalid config", "ambiguous remote", "push destination", "Git remote", "branch HEAD", "gh executable", "gh auth", "codex executable", "codex auth", "Issue", "Issue comments", "PR feedback", "diff", "missing pagination", "missing closing relations", "unknown competing head repository"} {
		t.Run(kind, func(t *testing.T) {
			f := newReviseFixture(t, false)
			switch kind {
			case "workflow":
				mustRemove(t, filepath.Join(f.root, "WORKFLOW.md"))
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
			case "missing pagination":
				f.active = strings.Replace(f.active, `"hasNextPage":false`, `"endCursor":null`, 1)
			case "missing closing relations":
				f.active = strings.Replace(f.active, `"totalCount":1`, `"unknown":1`, 1)
			case "unknown competing head repository":
				other := strings.NewReplacer(`"number":42`, `"number":43`, `"headRepository":{"nameWithOwner":"acme/iro"}`, `"headRepository":null`).Replace(activeRevisionPR)
				f.active = strings.Replace(f.active, activeRevisionPR, activeRevisionPR+","+other, 1)
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

func TestReviseActiveRelationsPaginationAndAmbiguity(t *testing.T) {
	for _, kind := range []string{"valid pagination", "duplicate branch", "duplicate Issue", "incomplete relations", "missing target", "changed target", "invalid cursor", "graphql error"} {
		t.Run(kind, func(t *testing.T) {
			f := newReviseFixture(t, false)
			page := 0
			f.runner.fn = func(spec CommandSpec) CommandResult {
				if spec.Name == "gh" && containsString(spec.Args, "graphql") && !containsString(spec.Args, "query="+reviewPreflightQuery) {
					page++
					if page == 1 {
						response := strings.Replace(f.active, `"hasNextPage":false`, `"hasNextPage":true,"endCursor":"next"`, 1)
						if kind == "missing target" {
							response = strings.Replace(response, activeRevisionPR, "", 1)
						}
						return CommandResult{Stdout: response}
					}
					if !containsString(spec.Args, "cursor=next") {
						t.Fatal(spec.Args)
					}
					node := ""
					switch kind {
					case "duplicate branch":
						node = strings.Replace(activeRevisionPR, `"number":42`, `"number":43`, 1)
						node = strings.Replace(node, `"number":123`, `"number":124`, 1)
					case "duplicate Issue":
						node = strings.NewReplacer(`"number":42`, `"number":43`, `"iro/issue-123"`, `"human-branch"`).Replace(activeRevisionPR)
					case "incomplete relations":
						node = strings.Replace(activeRevisionPR, `"totalCount":1`, `"totalCount":101`, 1)
					case "changed target":
						node = strings.Replace(activeRevisionPR, `"number":123`, `"number":124`, 1)
					case "invalid cursor":
						return CommandResult{Stdout: strings.Replace(deliveryResponseForTest, `"hasNextPage":false`, `"hasNextPage":true,"endCursor":"next"`, 1)}
					case "graphql error":
						return CommandResult{Stdout: `{"errors":[{"message":"denied"}]}`}
					}
					return CommandResult{Stdout: strings.Replace(deliveryResponseForTest, `"nodes":[]`, `"nodes":[`+node+`]`, 1)}
				}
				return f.respond(spec)
			}
			_, err := f.service.inspectReviseTarget(f.root, f.identity, 42)
			if (err == nil) != (kind == "valid pagination") || page != 2 {
				t.Fatalf("error=%v, pages=%d", err, page)
			}
		})
	}
}

func TestReviseRejectsPartialDirtyDivergentAndIncoherentState(t *testing.T) {
	for _, kind := range []string{"mapping only", "branch only", "path only", "mapping missing", "branch missing", "path missing", "stale registration", "unregistered", "wrong registration branch", "other checkout", "duplicate registration", "invalid mapping", "repository mismatch", "Issue mismatch", "branch mismatch", "path mismatch", "version mismatch", "dirty", "divergent", "ahead", "behind", "mapping symlink", "path symlink", "different repository", "detached"} {
		t.Run(kind, func(t *testing.T) {
			f := newReviseFixture(t, true)
			mappingPath := ownershipPath(f.service.Dirs, f.identity, 123)
			mapping := f.mapping()
			switch kind {
			case "mapping only":
				f.branch, f.registration = false, ""
				mustRemove(t, f.workspace)
			case "branch only":
				f.registration = ""
				mustRemove(t, f.workspace)
				mustRemove(t, mappingPath)
			case "path only":
				f.branch, f.registration = false, ""
				mustRemove(t, mappingPath)
			case "mapping missing":
				mustRemove(t, mappingPath)
			case "branch missing":
				f.branch = false
			case "path missing":
				mustRemove(t, f.workspace)
			case "stale registration":
				f.branch = false
				mustRemove(t, f.workspace)
				mustRemove(t, mappingPath)
			case "unregistered":
				f.registration = ""
			case "wrong registration branch":
				f.registration = strings.Replace(f.registration, "iro/issue-123", "other", 1)
			case "other checkout":
				f.registration += strings.Replace(f.registration, f.workspace, f.workspace+"-other", 1)
			case "duplicate registration":
				f.registration += f.registration
			case "repository mismatch":
				mapping.Repository = "other/iro"
			case "Issue mismatch":
				mapping.IssueNumber = 124
			case "branch mismatch":
				mapping.Branch = "iro/issue-124"
			case "path mismatch":
				mapping.Worktree = f.workspace + "-other"
			case "version mismatch":
				mapping.Version = 2
			case "dirty":
				f.dirty = true
			case "divergent", "ahead", "behind":
				f.head = revisionCommit
			case "mapping symlink", "path symlink":
				path := mappingPath
				if kind == "path symlink" {
					path = f.workspace
				}
				mustRemove(t, path)
				if err := os.Symlink(filepath.Join(t.TempDir(), "missing"), path); err != nil {
					t.Fatal(err)
				}
			}
			if strings.HasSuffix(kind, "mismatch") || kind == "invalid mapping" {
				data, _ := json.Marshal(mapping)
				if kind == "invalid mapping" {
					data = []byte("not JSON")
				}
				if err := os.WriteFile(mappingPath, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			f.runner.fn = func(spec CommandSpec) CommandResult {
				if kind == "different repository" && spec.Dir == f.workspace && containsString(spec.Args, "--git-common-dir") {
					return CommandResult{Stdout: filepath.Join(f.workspace, ".git")}
				}
				if kind == "detached" && containsString(spec.Args, "symbolic-ref") {
					return CommandResult{ExitCode: 1}
				}
				return f.respond(spec)
			}
			before, _ := os.ReadFile(mappingPath)
			if err := f.service.Revise(42, io.Discard); err == nil {
				t.Fatal("unexpected success")
			}
			if stages := revisionMutations(f.runner.calls); len(stages) != 0 {
				t.Fatal("mutation before rejection", stages)
			}
			after, _ := os.ReadFile(mappingPath)
			if string(before) != string(after) {
				t.Fatal("mapping was changed")
			}
		})
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
		{"commit parent", "local commit remains", "worker,add,commit"},
		{"push", "remote branch may have been updated", "worker,add,commit,push"},
		{"relation after worker", "before commit", "worker"},
		{"head after worker", "before commit", "worker"},
		{"local head after worker", "divergent state", "worker"},
		{"relation after commit", "local commit remains", "worker,add,commit"},
		{"head after commit", "local commit remains", "worker,add,commit"},
		{"dirty after commit", "local commit remains", "worker,add,commit"},
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
				if workerDone && spec.Name == "gh" && containsString(spec.Args, "query="+reviewPreflightQuery) {
					if tc.name == "relation after worker" || tc.name == "relation after commit" && f.head == revisionCommit {
						return CommandResult{Stdout: strings.Replace(f.target, `"state":"OPEN"`, `"state":"CLOSED"`, 1)}
					}
					if tc.name == "head after worker" || tc.name == "head after commit" && f.head == revisionCommit {
						return CommandResult{Stdout: strings.Replace(f.target, revisionHead, revisionCommit, 1)}
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
		{"remote drift", "HEAD changed", "fetch"},
		{"worktree", "partial local state may remain", "fetch,worktree add"},
		{"ownership", "ownership recording failed", "fetch,worktree add"},
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
				if tc.name == "remote drift" && fetched && containsString(spec.Args, "query="+reviewPreflightQuery) {
					return CommandResult{Stdout: strings.Replace(f.target, revisionHead, revisionCommit, 1)}
				}
				if spec.Name == "git" && containsArgs(spec.Args, "worktree", "add") {
					if tc.name == "worktree" {
						return CommandResult{ExitCode: 1}
					}
					if tc.name == "ownership" {
						if err := os.MkdirAll(ownershipPath(f.service.Dirs, f.identity, 123), 0755); err != nil {
							t.Fatal(err)
						}
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
