package iro

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const reviewResponseForTest = `{"data":{"repository":{"defaultBranchRef":{"name":"main"},"pullRequest":{"number":42,"title":"Human contribution","body":"Implements the requested behavior.","url":"https://github.com/acme/iro/pull/42","state":"OPEN","isDraft":false,"baseRefName":"main","headRefName":"human-feature","headRefOid":"0123456789abcdef","headRepository":{"nameWithOwner":"contributor/iro"},"author":{"login":"outside-author"},"mergeable":"MERGEABLE","reviewDecision":"","changedFiles":2,"additions":20,"deletions":3,"closingIssuesReferences":{"totalCount":1,"nodes":[{"number":123,"repository":{"nameWithOwner":"acme/iro"}}]}}}}}`

func reviewFakeResult(spec CommandSpec, root, reviewerOutput string) CommandResult {
	if spec.Name == "git" {
		switch {
		case len(spec.Args) >= 2 && spec.Args[0] == "rev-parse" && spec.Args[1] == "--show-toplevel":
			return CommandResult{Stdout: root + "\n"}
		case len(spec.Args) >= 2 && spec.Args[0] == "rev-parse" && spec.Args[1] == "HEAD":
			return CommandResult{Stdout: "0123456789abcdef\n"}
		case len(spec.Args) >= 2 && spec.Args[0] == "config":
			return CommandResult{Stdout: "git@github.com:acme/iro.git\n"}
		}
	}
	if spec.Name == "gh" {
		switch {
		case len(spec.Args) >= 2 && spec.Args[0] == "api" && spec.Args[1] == "graphql":
			return CommandResult{Stdout: reviewResponseForTest}
		case len(spec.Args) >= 2 && spec.Args[0] == "issue" && spec.Args[1] == "view":
			return CommandResult{Stdout: `{"number":123,"title":"Required behavior","body":"Issue specification body","url":"https://github.com/acme/iro/issues/123","comments":[{"id":"IC_1","author":{"login":"human"},"createdAt":"2025-01-02T03:04:05Z","body":"Issue decision"}]}`}
		case len(spec.Args) >= 2 && spec.Args[0] == "pr" && spec.Args[1] == "diff" && containsArgs(spec.Args, "--repo", "acme/iro"):
			if containsString(spec.Args, "--name-only") {
				return CommandResult{Stdout: "internal/iro/review.go\ninternal/iro/review_test.go\n"}
			}
			return CommandResult{Stdout: "diff --git a/file b/file\n+new behavior\n"}
		case len(spec.Args) >= 2 && spec.Args[0] == "pr" && spec.Args[1] == "view":
			return CommandResult{Stdout: `{"statusCheckRollup":[{"status":"COMPLETED","conclusion":"SUCCESS"}]}`}
		case len(spec.Args) >= 1 && spec.Args[0] == "api":
			switch {
			case strings.Contains(spec.Args[len(spec.Args)-1], "/issues/42/comments"):
				return CommandResult{Stdout: `[[{"body":"conversation"}]]`}
			case strings.Contains(spec.Args[len(spec.Args)-1], "/pulls/42/reviews"):
				return CommandResult{Stdout: `[[{"body":"feedback","state":"CHANGES_REQUESTED"}]]`}
			default:
				return CommandResult{Stdout: `[[{"body":"inline feedback","path":"file"}]]`}
			}
		default:
			return CommandResult{}
		}
	}
	if spec.Name == "codex" {
		if len(spec.Args) >= 2 && spec.Args[0] == "login" && spec.Args[1] == "status" {
			return CommandResult{}
		}
		return CommandResult{Stdout: reviewerOutput}
	}
	return CommandResult{}
}

func TestReviewUsesRemotePRInDisposableWorkspaceAndForwardsOpaqueOutput(t *testing.T) {
	root := t.TempDir()
	writeProjectFiles(t, root)
	if err := os.WriteFile(filepath.Join(root, "unrelated-dirty-file"), []byte("must not matter"), 0644); err != nil {
		t.Fatal(err)
	}
	reviewerOutput := "## iro review\n\nVerdict: FINDING\n\nSummary:\n問題あり\n\nFindings:\n- exact opaque output\n"
	runner := &fakeCommandRunner{}
	runner.fn = func(spec CommandSpec) CommandResult {
		return reviewFakeResult(spec, root, reviewerOutput)
	}
	service := newTestService(t, runner, root)

	var stdout, stderr strings.Builder
	if status := Execute([]string{"review", "42"}, &stdout, &stderr, service); status != 0 {
		t.Fatalf("Execute(review) = %d, stderr=%s", status, stderr.String())
	}
	if !strings.Contains(stdout.String(), "PR #42") {
		t.Fatalf("unexpected stdout: %s", stdout.String())
	}

	var reviewerCall, commentCall, checkoutCall *CommandSpec
	for i := range runner.calls {
		call := &runner.calls[i]
		if call.Name == "codex" && containsString(call.Args, "--ephemeral") {
			reviewerCall = call
		}
		if call.Name == "gh" && len(call.Args) >= 2 && call.Args[0] == "pr" && call.Args[1] == "comment" {
			commentCall = call
		}
		if call.Name == "gh" && len(call.Args) >= 2 && call.Args[0] == "pr" && call.Args[1] == "checkout" {
			checkoutCall = call
		}
		if call.Name == "git" && len(call.Args) > 0 {
			switch call.Args[0] {
			case "status", "symbolic-ref", "show-ref", "worktree", "add", "commit", "push":
				t.Fatalf("review inspected or mutated delivery workspace state: %+v", *call)
			}
		}
	}
	if reviewerCall == nil || commentCall == nil || checkoutCall == nil {
		t.Fatalf("missing review calls: %+v", runner.calls)
	}
	if !containsArgs(reviewerCall.Args, "--sandbox", "workspace-write") || !containsString(reviewerCall.Args, "sandbox_workspace_write.network_access=true") {
		t.Fatalf("Reviewer did not use the expected sandbox and network settings: %v", reviewerCall.Args)
	}
	if !containsString(checkoutCall.Args, "--detach") || !containsArgs(checkoutCall.Args, "--repo", "acme/iro") {
		t.Fatalf("PR checkout was not detached and repository-scoped: %v", checkoutCall.Args)
	}
	if !containsArgs(commentCall.Args, "--repo", "acme/iro") || commentCall.Args[len(commentCall.Args)-1] != reviewerOutput {
		t.Fatalf("Reviewer output was not forwarded unchanged: %q", commentCall.Args[len(commentCall.Args)-1])
	}
	for _, want := range []string{
		"Issue specification body",
		"Issue decision",
		"Implements the requested behavior.",
		"diff --git a/file b/file",
		"internal/iro/review.go",
		"conversation",
		"feedback",
		"inline feedback",
		"SUCCESS",
		workflowTemplate,
	} {
		if !strings.Contains(string(reviewerCall.Stdin), want) {
			t.Errorf("Reviewer payload does not contain %q", want)
		}
	}
	if _, err := os.Stat(reviewerCall.Dir); !os.IsNotExist(err) {
		t.Fatalf("disposable workspace was retained: %s (%v)", reviewerCall.Dir, err)
	}
	if _, err := os.Stat(ownershipPath(service.Dirs, RepositoryIdentity{Owner: "acme", Name: "iro"}, 123)); !os.IsNotExist(err) {
		t.Fatalf("review created an ownership mapping: %v", err)
	}
}

func TestReviewDoesNotInterpretReviewerOutput(t *testing.T) {
	for _, output := range []string{
		"## iro review\n\nVerdict: FINDING\n",
		"arbitrary nonconforming reviewer response\n",
	} {
		t.Run(strings.Fields(output)[0], func(t *testing.T) {
			root := t.TempDir()
			writeProjectFiles(t, root)
			runner := &fakeCommandRunner{}
			runner.fn = func(spec CommandSpec) CommandResult {
				return reviewFakeResult(spec, root, output)
			}
			service := newTestService(t, runner, root)
			if err := service.Review(42, io.Discard); err != nil {
				t.Fatalf("Review() interpreted successful Reviewer output: %v", err)
			}
			for _, call := range runner.calls {
				if call.Name == "gh" && len(call.Args) >= 2 && call.Args[0] == "pr" && call.Args[1] == "comment" {
					if call.Args[len(call.Args)-1] != output {
						t.Fatalf("output changed: got %q want %q", call.Args[len(call.Args)-1], output)
					}
					return
				}
			}
			t.Fatal("review comment was not posted")
		})
	}
}

func TestReviewAllowsDraftPR(t *testing.T) {
	root := t.TempDir()
	writeProjectFiles(t, root)
	response := strings.Replace(reviewResponseForTest, `"isDraft":false`, `"isDraft":true`, 1)
	runner := &fakeCommandRunner{}
	runner.fn = func(spec CommandSpec) CommandResult {
		if spec.Name == "gh" && len(spec.Args) >= 2 && spec.Args[0] == "api" && spec.Args[1] == "graphql" {
			return CommandResult{Stdout: response}
		}
		return reviewFakeResult(spec, root, "draft review")
	}
	service := newTestService(t, runner, root)

	if err := service.Review(42, io.Discard); err != nil {
		t.Fatalf("Review() rejected draft PR: %v", err)
	}
	for _, call := range runner.calls {
		if call.Name == "codex" && containsString(call.Args, "--ephemeral") {
			if !strings.Contains(string(call.Stdin), "Draft: true") {
				t.Fatalf("Reviewer payload omitted draft metadata: %s", call.Stdin)
			}
			return
		}
	}
	t.Fatal("draft PR did not reach Reviewer")
}

func TestReviewRejectsInvalidRemoteRelationBeforeReviewer(t *testing.T) {
	tests := []struct {
		name     string
		replace  string
		with     string
		wantText string
	}{
		{"no origin", `"totalCount":1,"nodes":[{"number":123,"repository":{"nameWithOwner":"acme/iro"}}]`, `"totalCount":0,"nodes":[]`, "exactly one"},
		{"multiple origins", `"totalCount":1,"nodes":[{"number":123,"repository":{"nameWithOwner":"acme/iro"}}]`, `"totalCount":2,"nodes":[{"number":123,"repository":{"nameWithOwner":"acme/iro"}},{"number":124,"repository":{"nameWithOwner":"acme/iro"}}]`, "exactly one"},
		{"different repository origin", `"nameWithOwner":"acme/iro"}}]`, `"nameWithOwner":"other/iro"}}]`, "configured repository"},
		{"non-default base", `"baseRefName":"main"`, `"baseRefName":"release"`, "default branch"},
		{"closed", `"state":"OPEN"`, `"state":"CLOSED"`, "not reviewable"},
		{"merged", `"state":"OPEN"`, `"state":"MERGED"`, "not reviewable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeProjectFiles(t, root)
			response := strings.Replace(reviewResponseForTest, tt.replace, tt.with, 1)
			runner := &fakeCommandRunner{}
			runner.fn = func(spec CommandSpec) CommandResult {
				if spec.Name == "gh" && len(spec.Args) >= 2 && spec.Args[0] == "api" && spec.Args[1] == "graphql" {
					return CommandResult{Stdout: response}
				}
				return reviewFakeResult(spec, root, "review")
			}
			service := newTestService(t, runner, root)
			err := service.Review(42, io.Discard)
			if err == nil || !strings.Contains(err.Error(), tt.wantText) {
				t.Fatalf("Review() error = %v, want %q", err, tt.wantText)
			}
			for _, call := range runner.calls {
				if call.Name == "codex" || call.Name == "gh" && len(call.Args) >= 2 && (call.Args[0] == "repo" && call.Args[1] == "clone" || call.Args[0] == "pr" && call.Args[1] == "comment") {
					t.Fatalf("side effect occurred after invalid precondition: %+v", call)
				}
			}
		})
	}
}

func TestReviewFailureDoesNotPostComment(t *testing.T) {
	for _, tc := range []struct {
		name       string
		worker     CommandResult
		commentErr bool
	}{
		{"reviewer failure", CommandResult{ExitCode: 7, Err: errors.New("failed")}, false},
		{"empty response", CommandResult{}, false},
		{"comment failure", CommandResult{Stdout: "review result"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeProjectFiles(t, root)
			runner := &fakeCommandRunner{}
			commentCalls := 0
			runner.fn = func(spec CommandSpec) CommandResult {
				if spec.Name == "codex" && !(len(spec.Args) >= 2 && spec.Args[0] == "login") {
					return tc.worker
				}
				if spec.Name == "gh" && len(spec.Args) >= 2 && spec.Args[0] == "pr" && spec.Args[1] == "comment" {
					commentCalls++
					if tc.commentErr {
						return CommandResult{ExitCode: 1, Err: errors.New("denied")}
					}
				}
				return reviewFakeResult(spec, root, "unused")
			}
			service := newTestService(t, runner, root)
			if err := service.Review(42, io.Discard); err == nil {
				t.Fatal("Review() unexpectedly succeeded")
			}
			if tc.name != "comment failure" && commentCalls != 0 {
				t.Fatal("review failure posted a PR comment")
			}
		})
	}
}

func TestReviewRejectsInvalidContextBeforeWorkspaceCreation(t *testing.T) {
	root := t.TempDir()
	writeProjectFiles(t, root)
	runner := &fakeCommandRunner{}
	runner.fn = func(spec CommandSpec) CommandResult {
		if spec.Name == "gh" && len(spec.Args) > 0 && spec.Args[0] == "api" && strings.Contains(spec.Args[len(spec.Args)-1], "/issues/42/comments") {
			return CommandResult{Stdout: "not-json"}
		}
		return reviewFakeResult(spec, root, "review")
	}
	service := newTestService(t, runner, root)
	if err := service.Review(42, io.Discard); err == nil || !strings.Contains(err.Error(), "invalid conversation comments") {
		t.Fatalf("Review() error = %v", err)
	}
	for _, call := range runner.calls {
		if call.Name == "codex" || call.Name == "gh" && len(call.Args) >= 2 && call.Args[0] == "repo" && call.Args[1] == "clone" {
			t.Fatalf("workspace or Reviewer started with invalid context: %+v", call)
		}
	}
}

func TestReviewRejectsChangedHeadAndRemovesDisposableWorkspace(t *testing.T) {
	root := t.TempDir()
	writeProjectFiles(t, root)
	workspace := ""
	runner := &fakeCommandRunner{}
	runner.fn = func(spec CommandSpec) CommandResult {
		if spec.Name == "gh" && len(spec.Args) >= 2 && spec.Args[0] == "repo" && spec.Args[1] == "clone" {
			workspace = spec.Args[3]
		}
		if spec.Name == "git" && len(spec.Args) >= 2 && spec.Args[0] == "rev-parse" && spec.Args[1] == "HEAD" {
			return CommandResult{Stdout: "different-head\n"}
		}
		return reviewFakeResult(spec, root, "review")
	}
	service := newTestService(t, runner, root)
	if err := service.Review(42, io.Discard); err == nil || !strings.Contains(err.Error(), "HEAD changed") {
		t.Fatalf("Review() error = %v", err)
	}
	if workspace == "" {
		t.Fatal("review workspace was not created")
	}
	if _, err := os.Stat(workspace); !os.IsNotExist(err) {
		t.Fatalf("disposable workspace was retained: %s (%v)", workspace, err)
	}
	for _, call := range runner.calls {
		if call.Name == "codex" && containsString(call.Args, "--ephemeral") || call.Name == "gh" && len(call.Args) >= 2 && call.Args[0] == "pr" && call.Args[1] == "comment" {
			t.Fatalf("Reviewer or comment started for changed HEAD: %+v", call)
		}
	}
}

func TestExecuteRejectsInvalidReviewNumber(t *testing.T) {
	service := NewService(&fakeCommandRunner{}, NewOSFileSystem())
	for _, args := range [][]string{{"review"}, {"review", "0"}, {"review", "-1"}, {"review", "1", "2"}} {
		var stderr strings.Builder
		if status := Execute(args, io.Discard, &stderr, service); status != 2 {
			t.Fatalf("Execute(%v) = %d, stderr=%s", args, status, stderr.String())
		}
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
