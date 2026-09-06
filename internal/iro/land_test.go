package iro

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const landHeadForTest = "0123456789abcdef0123456789abcdef01234567"
const landMergeForTest = "abcdef0123456789abcdef0123456789abcdef01"
const landResponseForTest = `{"data":{"repository":{"defaultBranchRef":{"name":"main"},"isArchived":false,"mergeCommitAllowed":true,"viewerPermission":"WRITE","pullRequest":{"number":42,"state":"OPEN","isDraft":false,"baseRefName":"main","headRefName":"iro/issue-123","headRefOid":"` + landHeadForTest + `","headRepository":{"nameWithOwner":"acme/iro"},"mergeable":"MERGEABLE","mergeStateStatus":"CLEAN","isMergeQueueEnabled":false,"closingIssuesReferences":{"totalCount":1,"nodes":[{"number":123,"repository":{"nameWithOwner":"acme/iro"}}]}}}}}`
const landActivePRForTest = `{"number":42,"headRefName":"iro/issue-123","headRepository":{"nameWithOwner":"acme/iro"},"closingIssuesReferences":{"totalCount":1,"nodes":[{"number":123,"repository":{"nameWithOwner":"acme/iro"}}]}}`

// Only project configuration reads are available. Any runtime state access or
// filesystem mutation fails, even when local execution state happens to exist.
type landProjectFiles struct {
	FileSystem
	t          *testing.T
	root       string
	unreadable string
}

func (f landProjectFiles) check(path string) {
	f.t.Helper()
	if path != filepath.Join(f.root, "iro.toml") && path != filepath.Join(f.root, "WORKFLOW.md") {
		f.t.Fatalf("land accessed local execution state: %s", path)
	}
}

func (f landProjectFiles) Stat(path string) (os.FileInfo, error) {
	f.check(path)
	return os.Stat(path)
}

func (f landProjectFiles) ReadFile(path string) ([]byte, error) {
	f.check(path)
	if filepath.Base(path) == f.unreadable {
		return nil, os.ErrPermission
	}
	return os.ReadFile(path)
}

type landFixture struct {
	t           *testing.T
	root        string
	service     *Service
	runner      *fakeCommandRunner
	target      string
	pages       []string
	page        int
	liveHead    string
	mergeResult CommandResult
	mergeCalls  int
	merged      bool
}

func landDeliveryPage(nodes string, more bool, cursor string) string {
	return fmt.Sprintf(`{"data":{"repository":{"defaultBranchRef":{"name":"main"},"pullRequests":{"nodes":[%s],"pageInfo":{"hasNextPage":%t,"endCursor":%q}}}}}`, nodes, more, cursor)
}

func newLandFixture(t *testing.T) *landFixture {
	t.Helper()
	f := &landFixture{
		t: t, root: t.TempDir(), target: landResponseForTest,
		pages:       []string{landDeliveryPage(landActivePRForTest, false, "")},
		liveHead:    landHeadForTest,
		mergeResult: CommandResult{Stdout: `{"merged":true,"sha":"` + landMergeForTest + `"}`},
	}
	writeProjectFiles(t, f.root)
	f.runner = &fakeCommandRunner{lookups: map[string]error{"codex": errors.New("not installed")}}
	f.runner.fn = f.respond
	f.service = newTestService(t, f.runner, f.root)
	f.service.FileSystem = landProjectFiles{t: t, root: f.root}
	return f
}

func (f *landFixture) respond(spec CommandSpec) CommandResult {
	f.t.Helper()
	if spec.Name == "git" {
		switch strings.Join(spec.Args, " ") {
		case "rev-parse --show-toplevel":
			return CommandResult{Stdout: f.root}
		case "config --get-all remote.origin.url":
			return CommandResult{Stdout: "git@github.com:acme/iro.git\n"}
		}
	}
	if spec.Name == "gh" && spec.Dir == f.root {
		if reflect.DeepEqual(spec.Args, []string{"auth", "status"}) {
			return CommandResult{}
		}
		if containsArgs(spec.Args, "api", "graphql") {
			if !containsArgs(spec.Args, "-f", "owner=acme") || !containsArgs(spec.Args, "-f", "name=iro") {
				f.t.Fatalf("missing configured repository: %+v", spec)
			}
			if containsString(spec.Args, "query="+landPreflightQuery) {
				if !containsArgs(spec.Args, "-F", "number=42") {
					f.t.Fatalf("wrong target: %+v", spec)
				}
				return CommandResult{Stdout: f.target}
			}
			if containsString(spec.Args, "query="+strings.Replace(deliveryQuery, "states:[OPEN,CLOSED,MERGED]", "states:[OPEN]", 1)) {
				if f.page >= len(f.pages) {
					f.t.Fatalf("unexpected active PR read: %+v", spec)
				}
				if f.page > 0 && !containsArgs(spec.Args, "-f", "cursor=next") {
					f.t.Fatalf("missing next page cursor: %+v", spec)
				}
				result := CommandResult{Stdout: f.pages[f.page]}
				f.page++
				return result
			}
		}
		if reflect.DeepEqual(spec.Args, []string{"api", "repos/acme/iro/pulls/42/merge", "--method", "PUT", "--input", "-"}) {
			f.mergeCalls++
			if f.page != len(f.pages) {
				f.t.Fatal("merge attempted before checking all active delivery PRs")
			}
			var payload map[string]string
			if json.Unmarshal(spec.Stdin, &payload) != nil || !reflect.DeepEqual(payload, map[string]string{"sha": landHeadForTest, "merge_method": "merge"}) {
				f.t.Fatalf("merge did not bind the validated HEAD and normal merge method: %s", spec.Stdin)
			}
			// Model the server's atomic SHA guard independently of preflight data.
			if payload["sha"] != f.liveHead {
				return CommandResult{ExitCode: 1, Stderr: "HTTP 409: Head branch was modified"}
			}
			f.merged = commandSucceeded(f.mergeResult) && strings.Contains(f.mergeResult.Stdout, `"merged":true`)
			return f.mergeResult
		}
	}
	f.t.Fatalf("unexpected command (local state, worker, feedback, repair, or extra remote mutation): %+v", spec)
	return CommandResult{ExitCode: 1}
}

func TestLandUsesOnlyRemoteDeliveryStateAndExplicitHumanAuthorization(t *testing.T) {
	for _, state := range []string{"no local state or reviews", "human-created PR", "AI FINDING", "no GitHub approval", "failing optional checks", "pre-receive hooks", "admin without bypass", "maintainer"} {
		t.Run(state, func(t *testing.T) {
			f := newLandFixture(t)
			switch state {
			case "human-created PR":
				f.target = strings.Replace(f.target, `"number":42`, `"author":{"login":"human"},"number":42`, 1)
			case "AI FINDING":
				f.target = strings.Replace(f.target, `"number":42`, `"comments":[{"body":"Verdict: FINDING"}],"number":42`, 1)
			case "no GitHub approval":
				f.target = strings.Replace(f.target, `"number":42`, `"reviewDecision":"","reviews":[],"number":42`, 1)
			case "failing optional checks":
				f.target = strings.Replace(f.target, `"CLEAN"`, `"UNSTABLE"`, 1)
			case "pre-receive hooks":
				f.target = strings.Replace(f.target, `"CLEAN"`, `"HAS_HOOKS"`, 1)
			case "admin without bypass":
				f.target = strings.Replace(f.target, `"WRITE"`, `"ADMIN"`, 1)
			case "maintainer":
				f.target = strings.Replace(f.target, `"WRITE"`, `"MAINTAIN"`, 1)
			}
			var out, errOut strings.Builder
			if code := Execute([]string{"land", "42"}, &out, &errOut, f.service); code != 0 {
				t.Fatalf("exit %d: %s", code, errOut.String())
			}
			if f.mergeCalls != 1 || !f.merged || !strings.Contains(out.String(), "Landed PR #42 for Issue #123") || !strings.Contains(out.String(), landMergeForTest) || errOut.Len() != 0 {
				t.Fatalf("unexpected merge/output: calls=%d, stdout=%q, stderr=%q", f.mergeCalls, out.String(), errOut.String())
			}
		})
	}
}

func TestLandLeavesExistingLocalExecutionStateUntouched(t *testing.T) {
	f := newLandFixture(t)
	identity := RepositoryIdentity{Owner: "acme", Name: "iro"}
	files := map[string]string{
		ownershipPath(f.service.Dirs, identity, 123):                            "invalid ownership mapping",
		filepath.Join(worktreePath(f.service.Dirs, identity, 123), "human.txt"): "uncommitted human changes",
		filepath.Join(f.root, "dirty.txt"):                                      "invoking checkout changes",
	}
	for path, content := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.service.Land(42, io.Discard); err != nil {
		t.Fatal(err)
	}
	for path, want := range files {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatalf("local file changed at %s: %q, %v", path, got, err)
		}
	}
}

func TestLandRejectsInvalidTargetBeforeMerge(t *testing.T) {
	for _, tc := range []struct{ name, from, to, want string }{
		{"closed", `"state":"OPEN"`, `"state":"CLOSED"`, "must be open"},
		{"merged", `"state":"OPEN"`, `"state":"MERGED"`, "must be open"},
		{"missing PR", `"number":42`, `"number":0`, "does not exist"},
		{"different PR", `"number":42`, `"number":43`, "does not exist"},
		{"draft", `"isDraft":false`, `"isDraft":true`, "remediation: mark the pull request ready for review, then retry `iro land 42`"},
		{"unknown draft", `"isDraft":false`, `"isDraft":null`, "draft state is unavailable"},
		{"missing default", `"defaultBranchRef":{"name":"main"}`, `"defaultBranchRef":null`, "default branch is unavailable"},
		{"wrong base", `"baseRefName":"main"`, `"baseRefName":"release"`, "requires default branch"},
		{"no origin", `"totalCount":1`, `"totalCount":0`, "exactly one origin"},
		{"multiple origins", `"totalCount":1`, `"totalCount":2`, "exactly one origin"},
		{"wrong origin", `"number":123`, `"number":124`, "iro/issue-124"},
		{"invalid origin", `"number":123`, `"number":0`, "origin Issue must belong"},
		{"foreign origin", `"repository":{"nameWithOwner":"acme/iro"}`, `"repository":{"nameWithOwner":"other/iro"}`, "origin Issue must belong"},
		{"incomplete origin", `"nodes":[{"number":123,"repository":{"nameWithOwner":"acme/iro"}}]`, `"nodes":[]`, "exactly one origin"},
		{"wrong canonical branch", `"iro/issue-123"`, `"human-feature"`, "head to be iro/issue-123"},
		{"noncanonical decimal", `"iro/issue-123"`, `"iro/issue-0123"`, "head to be iro/issue-123"},
		{"fork head", `"headRepository":{"nameWithOwner":"acme/iro"}`, `"headRepository":{"nameWithOwner":"contributor/iro"}`, "head to be iro/issue-123"},
		{"unknown head repository", `"headRepository":{"nameWithOwner":"acme/iro"}`, `"headRepository":null`, "head to be iro/issue-123"},
		{"missing HEAD", landHeadForTest, "", "HEAD commit is invalid"},
		{"invalid HEAD", landHeadForTest, strings.Repeat("z", 40), "HEAD commit is invalid"},
		{"archived repository", `"isArchived":false`, `"isArchived":true`, "does not allow normal merge"},
		{"unknown archive state", `"isArchived":false`, `"isArchived":null`, "does not allow normal merge"},
		{"merge method disabled", `"mergeCommitAllowed":true`, `"mergeCommitAllowed":false`, "does not allow normal merge"},
		{"unknown merge method", `"mergeCommitAllowed":true`, `"mergeCommitAllowed":null`, "does not allow normal merge"},
		{"read permission", `"WRITE"`, `"READ"`, "write permission is unavailable"},
		{"triage permission", `"WRITE"`, `"TRIAGE"`, "write permission is unavailable"},
		{"unknown permission", `"viewerPermission":"WRITE"`, `"viewerPermission":null`, "write permission is unavailable"},
		{"queue required", `"isMergeQueueEnabled":false`, `"isMergeQueueEnabled":true`, "requires a merge queue"},
		{"unknown queue policy", `"isMergeQueueEnabled":false`, `"isMergeQueueEnabled":null`, "queue policy is unavailable"},
		{"conflicts", `"MERGEABLE"`, `"CONFLICTING"`, "resolve conflicts"},
		{"pending mergeability", `"MERGEABLE"`, `"UNKNOWN"`, "wait for GitHub"},
		{"blocked", `"CLEAN"`, `"BLOCKED"`, "does not allow land"},
		{"behind", `"CLEAN"`, `"BEHIND"`, "does not allow land"},
		{"dirty", `"CLEAN"`, `"DIRTY"`, "does not allow land"},
		{"pending policy", `"CLEAN"`, `"UNKNOWN"`, "does not allow land"},
		{"unknown policy", `"CLEAN"`, `"FUTURE_STATE"`, "does not allow land"},
		{"missing policy", `"CLEAN"`, `""`, "does not allow land"},
		{"partial GraphQL failure", `{"data":`, `{"errors":[{"message":"failed"}],"data":`, "could not inspect"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newLandFixture(t)
			f.target = strings.Replace(f.target, tc.from, tc.to, 1)
			if f.target == landResponseForTest {
				t.Fatal("fixture was not modified")
			}
			err := f.service.Land(42, io.Discard)
			if err == nil || !strings.Contains(err.Error(), tc.want) || f.mergeCalls != 0 {
				t.Fatalf("error=%v, merges=%d; want %q before merge", err, f.mergeCalls, tc.want)
			}
		})
	}
	for _, target := range []string{"not JSON", `{"data":{"repository":null}}`, strings.Replace(landResponseForTest, `"WRITE"`, `"ADMIN"`, 1)} {
		f := newLandFixture(t)
		f.target = strings.Replace(target, `"CLEAN"`, `"BLOCKED"`, 1)
		if err := f.service.Land(42, io.Discard); err == nil || f.mergeCalls != 0 {
			t.Fatalf("invalid data or admin bypass allowed: %v, merges=%d", err, f.mergeCalls)
		}
	}
}

func TestLandChecksEveryActiveDeliveryRelationBeforeMerge(t *testing.T) {
	duplicate := strings.Replace(landActivePRForTest, `"number":42`, `"number":43`, 1)
	for _, tc := range []struct {
		name, node string
		allowed    bool
	}{
		{"same canonical branch", strings.Replace(duplicate, `"number":123`, `"number":999`, 1), false},
		{"same origin different branch", strings.Replace(duplicate, "iro/issue-123", "human-feature", 1), false},
		{"same origin in fork", strings.Replace(duplicate, `"headRepository":{"nameWithOwner":"acme/iro"}`, `"headRepository":{"nameWithOwner":"fork/iro"}`, 1), false},
		{"unrelated fork canonical name", strings.NewReplacer(`"headRepository":{"nameWithOwner":"acme/iro"}`, `"headRepository":{"nameWithOwner":"fork/iro"}`, `"number":123`, `"number":999`).Replace(duplicate), true},
		{"unknown canonical repository", strings.Replace(duplicate, `"headRepository":{"nameWithOwner":"acme/iro"}`, `"headRepository":null`, 1), false},
		{"truncated closing relations", strings.Replace(duplicate, `"totalCount":1`, `"totalCount":101`, 1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newLandFixture(t)
			f.pages = []string{landDeliveryPage(landActivePRForTest, true, "next"), landDeliveryPage(tc.node, false, "")}
			err := f.service.Land(42, io.Discard)
			if (err == nil) != tc.allowed || f.merged != tc.allowed {
				t.Fatalf("allowed=%t, error=%v, merged=%t", tc.allowed, err, f.merged)
			}
		})
	}
	for _, page := range []string{
		landDeliveryPage("", false, ""),
		landDeliveryPage(strings.Replace(landActivePRForTest, `"number":123`, `"number":124`, 1), false, ""),
		landDeliveryPage(landActivePRForTest+","+landActivePRForTest, false, ""),
		strings.Replace(landDeliveryPage(landActivePRForTest, false, ""), `"main"`, `"release"`, 1),
		landDeliveryPage(landActivePRForTest, true, ""),
		strings.Replace(landDeliveryPage(landActivePRForTest, false, ""), `"hasNextPage":false`, `"hasNextPage":null`, 1),
		`{"errors":[{"message":"denied"}]}`,
		`invalid JSON`,
	} {
		f := newLandFixture(t)
		f.pages = []string{page}
		if err := f.service.Land(42, io.Discard); err == nil || f.mergeCalls != 0 {
			t.Fatalf("invalid active relation allowed: %v, merges=%d", err, f.mergeCalls)
		}
	}
}

func TestLandBindsValidatedHeadAndDoesNotRetryChangedHead(t *testing.T) {
	f := newLandFixture(t)
	f.liveHead = landMergeForTest
	var out, errOut strings.Builder
	code := Execute([]string{"land", "42"}, &out, &errOut, f.service)
	if code != 1 || f.mergeCalls != 1 || f.merged || out.Len() != 0 || !strings.Contains(errOut.String(), landHeadForTest) || !strings.Contains(errOut.String(), "iro land 42") {
		t.Fatalf("exit=%d, calls=%d, merged=%t, stdout=%q, stderr=%q", code, f.mergeCalls, f.merged, out.String(), errOut.String())
	}
}

func TestLandMergeFailureDoesNotRetryRepairOrFallback(t *testing.T) {
	for _, result := range []CommandResult{
		{ExitCode: 1, Stderr: "HTTP 405: required reviews have not been satisfied"},
		{ExitCode: 1, Stderr: "HTTP 403: repository rules rejected the merge"},
		{ExitCode: -1, Err: errors.New("could not start gh")},
		{Stdout: `{"merged":false,"message":"Merge blocked"}`},
		{Stdout: `{"merged":true,"sha":"` + landMergeForTest + `"}`, ExitCode: 1, Err: errors.New("connection lost")},
		{Stdout: `not JSON`},
		{Stdout: `{"merged":true}`},
		{Stdout: `{}`},
	} {
		f := newLandFixture(t)
		f.mergeResult = result
		var out strings.Builder
		err := f.service.Land(42, &out)
		if err == nil || f.mergeCalls != 1 || out.Len() != 0 || !strings.Contains(err.Error(), "failed or could not be confirmed") || !strings.Contains(err.Error(), "no automatic retry") {
			t.Fatalf("error=%v, calls=%d, output=%q", err, f.mergeCalls, out.String())
		}
	}
}

func TestLandRequiresValidProjectContextBeforeRemoteMutation(t *testing.T) {
	for _, name := range []string{"iro.toml", "WORKFLOW.md"} {
		for _, state := range []string{"missing", "directory", "unreadable", "invalid config"} {
			t.Run(name+"/"+state, func(t *testing.T) {
				if state == "invalid config" && name != "iro.toml" {
					return
				}
				f := newLandFixture(t)
				path := filepath.Join(f.root, name)
				switch state {
				case "missing", "directory":
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
					if state == "directory" {
						if err := os.Mkdir(path, 0755); err != nil {
							t.Fatal(err)
						}
					}
				case "unreadable":
					f.service.FileSystem = landProjectFiles{t: t, root: f.root, unreadable: name}
				case "invalid config":
					if err := os.WriteFile(path, []byte("version = 999\n"), 0644); err != nil {
						t.Fatal(err)
					}
				}
				err := f.service.Land(42, io.Discard)
				if err == nil || !strings.Contains(err.Error(), name) || f.mergeCalls != 0 {
					t.Fatalf("error=%v, merges=%d", err, f.mergeCalls)
				}
			})
		}
	}
	for _, name := range []string{"git", "gh"} {
		f := newLandFixture(t)
		f.runner.lookups[name] = errors.New("not installed")
		if err := f.service.Land(42, io.Discard); err == nil || f.mergeCalls != 0 {
			t.Fatalf("missing %s allowed: %v", name, err)
		}
	}
	for _, stage := range []string{"git root", "missing remote", "ambiguous remote", "wrong host", "auth", "target query", "active query"} {
		t.Run(stage, func(t *testing.T) {
			f := newLandFixture(t)
			f.runner.fn = func(spec CommandSpec) CommandResult {
				fail := CommandResult{ExitCode: 1}
				if stage == "git root" && containsArgs(spec.Args, "rev-parse", "--show-toplevel") || stage == "auth" && containsArgs(spec.Args, "auth", "status") {
					return fail
				}
				if containsString(spec.Args, "remote.origin.url") {
					switch stage {
					case "missing remote":
						return fail
					case "ambiguous remote":
						return CommandResult{Stdout: "git@github.com:acme/iro.git\ngit@github.com:other/iro.git\n"}
					case "wrong host":
						return CommandResult{Stdout: "https://example.com/acme/iro"}
					}
				}
				if stage == "target query" && containsString(spec.Args, "query="+landPreflightQuery) || stage == "active query" && strings.Contains(strings.Join(spec.Args, " "), "states:[OPEN]") {
					return fail
				}
				return f.respond(spec)
			}
			if err := f.service.Land(42, io.Discard); err == nil || f.mergeCalls != 0 {
				t.Fatalf("precondition failure allowed: %v, merges=%d", err, f.mergeCalls)
			}
		})
	}
}

func TestExecuteRejectsInvalidLandArgumentsWithoutCommands(t *testing.T) {
	for _, args := range [][]string{{"land"}, {"land", "42", "43"}, {"land", "--yes"}, {"land", "0"}, {"land", "-1"}, {"land", "+1"}, {"land", "1.0"}, {"land", ""}, {"land", "https://github.com/acme/iro/pull/42"}, {"land", "999999999999999999999999999"}} {
		f := newLandFixture(t)
		var errOut strings.Builder
		if code := Execute(args, io.Discard, &errOut, f.service); code != 2 || len(f.runner.calls) != 0 || errOut.Len() == 0 {
			t.Fatalf("args=%v, code=%d, calls=%v, stderr=%s", args, code, f.runner.calls, errOut.String())
		}
	}
	f := newLandFixture(t)
	if err := f.service.Land(0, io.Discard); err == nil || len(f.runner.calls) != 0 {
		t.Fatalf("invalid direct invocation: %v, calls=%v", err, f.runner.calls)
	}
}
