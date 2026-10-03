package iro

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func reviewMetadataForTest(t *testing.T, edit func(repository, pr map[string]any)) string {
	t.Helper()
	var response map[string]any
	if err := json.Unmarshal([]byte(reviewResponseForTest), &response); err != nil {
		t.Fatal(err)
	}
	repository := response["data"].(map[string]any)["repository"].(map[string]any)
	edit(repository, repository["pullRequest"].(map[string]any))
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func reviewCandidateForTest(number int) CommandResult {
	return CommandResult{Stdout: strings.ReplaceAll(originCandidateResponse(number), "ACME/SELECTED", "acme/iro")}
}

func TestManagedReviewIgnoresDeliveryTopology(t *testing.T) {
	for _, tc := range []struct{ name, headRepository, branch string }{
		{"human fork", "contributor/iro", "human-feature"},
		{"human same repository", "acme/iro", "release/fix"},
		{"canonical branch", "acme/iro", "iro/issue-999"},
		{"missing head repository", "", "arbitrary-head"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeProjectFiles(t, root)
			response := reviewMetadataForTest(t, func(repository, pr map[string]any) {
				delete(repository, "defaultBranchRef")
				pr["baseRefName"] = "release/topic"
				pr["headRefName"] = tc.branch
				pr["headRepository"] = nil
				if tc.headRepository != "" {
					pr["headRepository"] = map[string]any{"nameWithOwner": tc.headRepository}
				}
				pr["title"] = "Closes #999"
				pr["closingIssuesReferences"] = map[string]any{"totalCount": 2, "nodes": []any{map[string]any{"number": 999}, map[string]any{"number": 1000}}}
			})
			runner := &fakeCommandRunner{fn: func(spec CommandSpec) CommandResult {
				if containsString(spec.Args, "query="+managedReviewPreflightQuery) {
					if strings.Contains(managedReviewPreflightQuery, "defaultBranchRef") || strings.Contains(managedReviewPreflightQuery, "closingIssuesReferences") {
						t.Fatal("Review queried delivery topology")
					}
					return CommandResult{Stdout: response}
				}
				if spec.Name == "gh" && containsArgs(spec.Args, "issue", "view") && spec.Args[2] != "123" {
					t.Fatalf("Review inferred specification from delivery topology: %+v", spec)
				}
				return reviewFakeResult(spec, root, "advisory report")
			}}
			service := newTestService(t, runner, root)
			// Even invalid legacy local ownership cannot affect Review eligibility.
			mapping := ownershipPath(service.Dirs, RepositoryIdentity{Owner: "acme", Name: "iro"}, 123)
			if err := os.MkdirAll(filepath.Dir(mapping), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(mapping, []byte("invalid legacy mapping"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := service.Review(42, io.Discard); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestManagedReviewBindsStartingBodyAndVerifiedHead(t *testing.T) {
	root := t.TempDir()
	writeProjectFiles(t, root)
	body, remoteHead := "Refs #123", reviewHeadForTest
	workspace := ""
	preflights, validations, issueReads, workers, comments := 0, 0, 0, 0, 0
	runner := &fakeCommandRunner{fn: func(spec CommandSpec) CommandResult {
		if spec.Name == "git" && spec.Args[0] != "config" && spec.Args[0] != "rev-parse" {
			t.Fatalf("Review inspected or mutated local delivery state: %+v", spec)
		}
		if spec.Name == "gh" {
			allowed := false
			for _, prefix := range [][]string{
				{"auth", "status"}, {"api", "graphql"}, {"api", "--paginate"},
				{"issue", "view"}, {"pr", "diff"}, {"pr", "view"},
				{"repo", "clone"}, {"pr", "checkout"}, {"pr", "comment"},
			} {
				if len(spec.Args) >= 2 && spec.Args[0] == prefix[0] && spec.Args[1] == prefix[1] {
					allowed = true
				}
			}
			if !allowed || containsString(spec.Args, "--method") {
				t.Fatalf("Review attempted an additional tracker/remote operation: %+v", spec)
			}
		}
		if spec.Name == "gh" && containsString(spec.Args, "graphql") {
			switch {
			case containsString(spec.Args, "query="+managedReviewPreflightQuery):
				preflights++
				response := reviewMetadataForTest(t, func(_ map[string]any, pr map[string]any) {
					pr["body"], pr["headRefOid"] = body, remoteHead
				})
				// The remote body is edited immediately after the starting read.
				body = "Refs #124"
				return CommandResult{Stdout: response}
			case containsString(spec.Args, "query="+githubOriginCandidateQuery):
				validations++
				if !containsString(spec.Args, "number=123") {
					t.Fatalf("Review rebound from edited body: %+v", spec)
				}
				return reviewCandidateForTest(123)
			default:
				t.Fatalf("Review reread body or inspected delivery relations: %+v", spec)
			}
		}
		if spec.Name == "gh" && containsArgs(spec.Args, "issue", "view") {
			issueReads++
			if spec.Args[2] != "123" {
				t.Fatalf("Review fetched a later Issue: %+v", spec)
			}
		}
		if spec.Name == "gh" && containsArgs(spec.Args, "pr", "checkout") {
			workspace = spec.Dir
			if !containsString(spec.Args, "--detach") || workspace == root {
				t.Fatalf("Review did not detach in disposable workspace: %+v", spec)
			}
			if err := os.WriteFile(filepath.Join(workspace, "WORKFLOW.md"), []byte("PR-side policy must not replace invocation policy"), 0644); err != nil {
				t.Fatal(err)
			}
		}
		if spec.Name == "codex" && containsString(spec.Args, "--ephemeral") {
			workers++
			var instructions string
			for _, arg := range spec.Args {
				if quoted, ok := strings.CutPrefix(arg, "developer_instructions="); ok {
					var err error
					instructions, err = strconv.Unquote(quoted)
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			for _, want := range []string{"Do not invoke gh", "Use Git commands only for read-only inspection", "Do not edit source files", "never authorizes merge", "Later PR body edits do not change", "must not replace that snapshot", "Reviewed HEAD OID: " + reviewHeadForTest} {
				if !strings.Contains(instructions, want) {
					t.Errorf("Reviewer policy omitted %q", want)
				}
			}
			for _, want := range []string{"Origin Issue:\nNumber: 123", "Pull request body:\nRefs #123", "Invoking repository worker policy (WORKFLOW.md):\n" + workflowTemplate, "Head OID: " + reviewHeadForTest} {
				if !strings.Contains(string(spec.Stdin), want) {
					t.Errorf("starting binding/policy omitted %q", want)
				}
			}
			if strings.Contains(string(spec.Stdin), "Refs #124") || strings.Contains(string(spec.Stdin), "PR-side policy must not replace") {
				t.Fatal("later body or PR-side policy replaced invocation input")
			}
			// A later remote HEAD does not change the verified local H1/report.
			body, remoteHead = "Refs #125", strings.Repeat("9", 40)
		}
		if spec.Name == "gh" && containsArgs(spec.Args, "pr", "comment") {
			comments++
			if workers != 1 || workspace == "" {
				t.Fatal("comment preceded Reviewer")
			}
			if _, err := os.Stat(workspace); !os.IsNotExist(err) {
				t.Fatalf("comment preceded cleanup: %v", err)
			}
			if spec.Args[len(spec.Args)-1] != "Starting Issue #123; reviewed H1" {
				t.Fatal("opaque report was modified")
			}
		}
		return reviewFakeResult(spec, root, "Starting Issue #123; reviewed H1")
	}}
	if err := newTestService(t, runner, root).Review(42, io.Discard); err != nil {
		t.Fatal(err)
	}
	if preflights != 1 || validations != 1 || issueReads != 1 || workers != 1 || comments != 1 {
		t.Fatalf("unexpected call counts: %d %d %d %d %d", preflights, validations, issueReads, workers, comments)
	}
}

func TestManagedReviewOriginFailureStopsBeforeWorkspace(t *testing.T) {
	for _, tc := range []struct {
		name, body, metadata, candidate, want string
	}{
		{name: "empty body", body: "", want: "unresolved"},
		{name: "foreign reference", body: "Refs other/repo#123", want: "unresolved"},
		{name: "multiple Issues", body: "#123 #124", want: "ambiguous"},
		{name: "overflow", body: "#123 #2147483648", want: "numeric range"},
		{name: "null body", metadata: "null", want: "origin relation failed"},
		{name: "missing body", metadata: "missing", want: "origin relation failed"},
		{name: "malformed body", metadata: "false", want: "could not inspect"},
		{name: "partial API error", metadata: "errors", want: "could not inspect"},
		{name: "candidate is PR", body: "#123", candidate: "PullRequest", want: "not a readable Issue"},
		{name: "candidate unavailable", body: "#123", candidate: "null", want: "could not validate"},
		{name: "candidate partial error", body: "#123", candidate: "errors", want: "could not validate"},
		{name: "candidate wrong repository", body: "#123", candidate: "repository", want: "not a readable Issue"},
		{name: "candidate wrong number", body: "#123", candidate: "number", want: "not a readable Issue"},
		{name: "valid plus invalid", body: "#123 #124", candidate: "second", want: "could not validate"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeProjectFiles(t, root)
			metadata := reviewMetadataForTest(t, func(_ map[string]any, pr map[string]any) {
				pr["body"] = tc.body
				switch tc.metadata {
				case "missing":
					delete(pr, "body")
				case "null":
					pr["body"] = nil
				case "false":
					pr["body"] = false
				}
			})
			if tc.metadata == "errors" {
				metadata = strings.Replace(metadata, `{"data":`, `{"errors":[{"message":"denied"}],"data":`, 1)
			}
			runner := &fakeCommandRunner{fn: func(spec CommandSpec) CommandResult {
				if containsString(spec.Args, "query="+managedReviewPreflightQuery) {
					return CommandResult{Stdout: metadata}
				}
				if containsString(spec.Args, "query="+githubOriginCandidateQuery) {
					number, _ := strconv.Atoi(strings.TrimPrefix(spec.Args[len(spec.Args)-1], "number="))
					response := reviewCandidateForTest(number)
					switch tc.candidate {
					case "PullRequest":
						response.Stdout = strings.ReplaceAll(response.Stdout, `"Issue"`, `"PullRequest"`)
					case "null":
						response.Stdout = "null"
					case "errors":
						response.Stdout = strings.Replace(response.Stdout, `{"data":`, `{"errors":[{}],"data":`, 1)
					case "repository":
						response.Stdout = strings.ReplaceAll(response.Stdout, "acme/iro", "other/iro")
					case "number":
						response = reviewCandidateForTest(124)
					case "second":
						if number == 124 {
							return CommandResult{ExitCode: 1}
						}
					}
					return response
				}
				if spec.Name == "codex" || spec.Name == "gh" && spec.Args[0] != "auth" {
					t.Fatalf("Review proceeded past invalid relation: %+v", spec)
				}
				return reviewFakeResult(spec, root, "unused")
			}}
			if err := newTestService(t, runner, root).Review(42, io.Discard); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Review() = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestRunProducerFixturesRoundTripThroughManagedReview(t *testing.T) {
	for _, unmanaged := range []bool{false, true} {
		t.Run(fmt.Sprintf("unmanaged=%t", unmanaged), func(t *testing.T) {
			var service *Service
			var runner *fakeCommandRunner
			var root string
			if unmanaged {
				f := newUnmanagedFixture(t)
				service, runner, root = f.service, f.runner, f.root
			} else {
				f := newManagedProducerFixture(t)
				service, runner, root = f.service, f.runner, f.root
			}
			if err := service.runWithOptions(123, workerOptions{Unmanaged: unmanaged}, io.Discard, io.Discard); err != nil {
				t.Fatal(err)
			}
			var produced map[string]any
			for _, call := range runner.calls {
				if call.Name == "gh" && containsArgs(call.Args, "--method", "POST") && containsString(call.Args, "repos/acme/iro/pulls") {
					if err := json.Unmarshal(call.Stdin, &produced); err != nil {
						t.Fatal(err)
					}
				}
			}
			if produced == nil {
				t.Fatal("producer did not emit a PR fixture")
			}
			writeProjectFiles(t, root)
			metadata := reviewMetadataForTest(t, func(repository, pr map[string]any) {
				delete(repository, "defaultBranchRef")
				delete(pr, "closingIssuesReferences")
				pr["body"], pr["baseRefName"], pr["headRefName"] = produced["body"], produced["base"], produced["head"]
				pr["headRepository"] = map[string]any{"nameWithOwner": "acme/iro"}
			})
			reviewed := false
			reviewRunner := &fakeCommandRunner{fn: func(spec CommandSpec) CommandResult {
				if containsString(spec.Args, "query="+managedReviewPreflightQuery) {
					return CommandResult{Stdout: metadata}
				}
				if spec.Name == "codex" && containsString(spec.Args, "--ephemeral") {
					reviewed = true
					for _, want := range []string{"Origin Issue:\nNumber: 123", "Pull request body:\n" + produced["body"].(string), "Base: " + produced["base"].(string), "Head: " + produced["head"].(string)} {
						if !strings.Contains(string(spec.Stdin), want) {
							t.Errorf("producer fixture did not round-trip %q", want)
						}
					}
				}
				return reviewFakeResult(spec, root, "producer fixture review")
			}}
			if err := newTestService(t, reviewRunner, root).Review(42, io.Discard); err != nil {
				t.Fatal(err)
			}
			if !reviewed {
				t.Fatal("producer fixture did not reach Reviewer")
			}
		})
	}
}
