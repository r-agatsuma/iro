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

func reviseMetadata(t *testing.T, value string, edit func(map[string]any, map[string]any)) string {
	t.Helper()
	var response map[string]any
	if err := json.Unmarshal([]byte(value), &response); err != nil {
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

func TestReviseSupportsSelectedRefsWithoutDeliveryTopology(t *testing.T) {
	for _, branch := range []string{"iro/custom/fix", "iro/issue-999", "iro/issue-123-" + strings.Repeat("b", 32), "human/fix"} {
		for _, existing := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/existing=%t", branch, existing), func(t *testing.T) {
				f := newReviseFixture(t, false)
				f.branchName = branch
				f.target = reviseMetadata(t, f.target, func(repo, pr map[string]any) {
					delete(repo, "defaultBranchRef")
					pr["headRefName"], pr["baseRefName"], pr["title"] = branch, "release/topic", "Closes #999"
					pr["closingIssuesReferences"] = map[string]any{"totalCount": 1000}
				})
				if existing {
					f.workspace = filepath.Join(t.TempDir(), "existing-checkout")
					f.createWorktree()
				}
				original := f.workspace
				if existing && !strings.HasPrefix(branch, "iro/") {
					// An existing Human checkout is observation only, never the worker target.
					f.otherRegistrations, f.registration = f.registration, ""
					f.branch = false
					if err := os.WriteFile(filepath.Join(original, "human.txt"), []byte("unchanged"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				mapping := ownershipPath(f.service.Dirs, f.identity, 123)
				if err := os.MkdirAll(filepath.Dir(mapping), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(mapping, []byte("invalid legacy mapping"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := f.service.Revise(42, io.Discard); err != nil {
					t.Fatal(err)
				}
				if data, err := os.ReadFile(mapping); err != nil || string(data) != "invalid legacy mapping" {
					t.Fatalf("legacy mapping changed: %s %v", data, err)
				}
				if !strings.HasPrefix(branch, "iro/") {
					if f.workspace == original || !f.detached {
						t.Fatal("Human checkout adopted")
					}
					if existing {
						if data, err := os.ReadFile(filepath.Join(original, "human.txt")); err != nil || string(data) != "unchanged" {
							t.Fatalf("Human checkout changed: %s %v", data, err)
						}
					}
				} else if existing && f.workspace != original {
					t.Fatal("clean registered iro worktree was not reused")
				}
				pushes, fetches, workers := 0, 0, 0
				for _, call := range f.runner.calls {
					if call.Name == "git" && call.Args[0] == "push" {
						pushes++
						source := "refs/heads/" + branch
						if f.detached {
							source = revisionCommit
						}
						want := "push --no-follow-tags --no-recurse-submodules -- origin " + source + ":refs/heads/" + branch
						if strings.Join(call.Args, " ") != want {
							t.Fatalf("push=%v, want %s", call.Args, want)
						}
					}
					if call.Name == "git" && call.Args[0] == "fetch" {
						fetches++
					}
					if call.Name == "codex" && containsString(call.Args, "--ephemeral") {
						workers++
						if call.Dir != f.workspace || !strings.Contains(string(call.Stdin), "Origin Issue:\nNumber: 123") {
							t.Fatal("worker was not bound to selected workspace/Issue")
						}
					}
				}
				wantFetches := 1
				if existing && strings.HasPrefix(branch, "iro/") {
					wantFetches = 0
				}
				if pushes != 1 || workers != 1 || fetches != wantFetches {
					t.Fatalf("pushes/workers/fetches=%d/%d/%d", pushes, workers, fetches)
				}
			})
		}
	}
}

func TestReviseRejectsUnsafeIroWorkspaceBeforeWorker(t *testing.T) {
	for _, kind := range []string{"dirty", "HEAD mismatch", "actual HEAD mismatch", "registration conflict", "duplicate path", "locked", "prunable", "missing path", "symlink", "wrong common dir", "detached", "unreadable status", "unreadable inventory", "branch-only mismatch"} {
		t.Run(kind, func(t *testing.T) {
			f := newReviseFixture(t, true)
			switch kind {
			case "dirty":
				f.dirty = true
			case "HEAD mismatch", "branch-only mismatch":
				f.head = revisionCommit
				if kind == "branch-only mismatch" {
					f.registration = ""
				}
			case "registration conflict":
				f.registration += strings.ReplaceAll(f.registration, f.workspace, f.workspace+"-other")
			case "duplicate path":
				f.registration += f.registration
			case "locked", "prunable":
				f.registration = strings.TrimSuffix(f.registration, "\n") + kind + "\n\n"
			case "missing path", "symlink":
				mustRemove(t, f.workspace)
				if kind == "symlink" {
					if err := os.Symlink(t.TempDir(), f.workspace); err != nil {
						t.Fatal(err)
					}
				}
			}
			f.runner.fn = func(spec CommandSpec) CommandResult {
				if spec.Name == "git" {
					switch {
					case kind == "actual HEAD mismatch" && containsArgs(spec.Args, "rev-parse", "HEAD"):
						return CommandResult{Stdout: revisionCommit}
					case kind == "wrong common dir" && spec.Dir == f.workspace && containsString(spec.Args, "--git-common-dir"):
						return CommandResult{Stdout: filepath.Join(f.workspace, ".git")}
					case kind == "detached" && spec.Args[0] == "symbolic-ref":
						return CommandResult{ExitCode: 1}
					case kind == "unreadable status" && containsString(spec.Args, "status"), kind == "unreadable inventory" && spec.Args[0] == "for-each-ref":
						return CommandResult{ExitCode: 1}
					}
				}
				return f.respond(spec)
			}
			if err := f.service.Revise(42, io.Discard); err == nil {
				t.Fatal("unsafe workspace accepted")
			}
			if stages := revisionMutations(f.runner.calls); len(stages) != 0 {
				t.Fatalf("mutation before workspace rejection: %v", stages)
			}
		})
	}
}

func TestReviseMaterializesExistingUnregisteredH1BranchWithoutMovingIt(t *testing.T) {
	f := newReviseFixture(t, false)
	f.branch = true
	if err := f.service.Revise(42, io.Discard); err != nil {
		t.Fatal(err)
	}
	created := false
	for _, call := range f.runner.calls {
		if call.Name == "git" && containsArgs(call.Args, "worktree", "add") {
			created = true
			if strings.Join(call.Args, " ") != "worktree add "+f.workspace+" iro/issue-123" {
				t.Fatalf("existing branch overwritten: %v", call.Args)
			}
		}
	}
	if !created {
		t.Fatal("no worktree materialized")
	}
}

func TestRevisePushTargetFaultsKeepSingleChildWithoutPush(t *testing.T) {
	for _, branch := range []string{"iro/issue-123", "human/fix"} {
		for _, kind := range []string{"advance", "rewind", "delete", "closed", "merged", "head repo", "head ref", "unknown head repo", "unreadable PR", "partial API error", "unreadable ref", "push destination", "unreadable push destination", "fetch identity"} {
			t.Run(branch+"/"+kind, func(t *testing.T) {
				f := newReviseFixture(t, branch == "iro/issue-123")
				f.branchName = branch
				f.target = strings.ReplaceAll(f.target, "iro/issue-123", branch)
				workers, targetReads, remoteReads := 0, 0, 0
				f.runner.fn = func(spec CommandSpec) CommandResult {
					if spec.Name == "codex" && containsString(spec.Args, "--ephemeral") {
						workers++
					}
					if containsString(spec.Args, "query="+revisePushTargetQuery) {
						targetReads++
						if f.head != revisionCommit {
							t.Fatal("remote target revalidated before local commit")
						}
						response := reviseMetadata(t, f.target, func(_ map[string]any, pr map[string]any) {
							switch kind {
							case "closed":
								pr["state"] = "CLOSED"
							case "merged":
								pr["state"] = "MERGED"
							case "head repo":
								pr["headRepository"] = map[string]any{"nameWithOwner": "fork/iro"}
							case "head ref":
								pr["headRefName"] = "another-ref"
							case "unknown head repo":
								pr["headRepository"] = nil
							}
						})
						if kind == "unreadable PR" {
							return CommandResult{ExitCode: 1}
						}
						if kind == "partial API error" {
							response = strings.Replace(response, `{"data":`, `{"errors":[{}],"data":`, 1)
						}
						return CommandResult{Stdout: response}
					}
					if spec.Name == "git" && workers > 0 {
						if containsString(spec.Args, "--heads") {
							remoteReads++
							if f.head != revisionCommit {
								t.Fatal("remote ref revalidated before local commit")
							}
							switch kind {
							case "advance":
								return CommandResult{Stdout: revisionCommit + "\trefs/heads/" + branch + "\n"}
							case "rewind":
								return CommandResult{Stdout: strings.Repeat("1", 40) + "\trefs/heads/" + branch + "\n"}
							case "delete":
								return CommandResult{}
							case "unreadable ref":
								return CommandResult{ExitCode: 1}
							}
						}
						if spec.Args[0] == "remote" {
							if kind == "push destination" {
								return CommandResult{Stdout: "git@github.com:other/iro.git"}
							}
							if kind == "unreadable push destination" {
								return CommandResult{ExitCode: 1}
							}
						}
						if kind == "fetch identity" && spec.Args[0] == "config" {
							return CommandResult{Stdout: "git@github.com:other/iro.git"}
						}
					}
					return f.respond(spec)
				}
				err := f.service.Revise(42, io.Discard)
				if err == nil || !strings.Contains(err.Error(), f.workspace) || !strings.Contains(err.Error(), revisionCommit) || !strings.Contains(err.Error(), "no push attempted") || !strings.Contains(err.Error(), "no automatic retry") {
					t.Fatalf("missing retained commit/workspace diagnostic: %v", err)
				}
				if f.head != revisionCommit || targetReads > 1 || remoteReads > 1 {
					t.Fatalf("head/read counts=%s/%d/%d", f.head, targetReads, remoteReads)
				}
				for _, call := range f.runner.calls {
					if call.Name == "git" && call.Args[0] == "push" {
						t.Fatal("push attempted after target drift")
					}
				}
				if _, err := os.Stat(f.workspace); err != nil {
					t.Fatal("workspace not retained", err)
				}
			})
		}
	}
}

func TestReviseBindsBodyOnceAndAllowsBodyOrBaseChanges(t *testing.T) {
	for _, kind := range []string{"body", "base", "both"} {
		t.Run(kind, func(t *testing.T) {
			f := newReviseFixture(t, true)
			starts, validations, specifications, finalReads, pushes := 0, 0, 0, 0, 0
			workerDone := false
			f.runner.fn = func(spec CommandSpec) CommandResult {
				if containsString(spec.Args, "query="+managedReviewPreflightQuery) {
					starts++
					if workerDone {
						t.Fatal("body reread after Author start")
					}
				}
				if containsString(spec.Args, "query="+githubOriginCandidateQuery) {
					validations++
					if workerDone || !containsString(spec.Args, "number=123") {
						t.Fatal("Issue relation rebound")
					}
				}
				if spec.Name == "gh" && containsArgs(spec.Args, "issue", "view") {
					specifications++
				}
				if spec.Name == "codex" && containsString(spec.Args, "--ephemeral") {
					workerDone = true
					if !strings.Contains(string(spec.Stdin), "Head OID: "+revisionHead) || !strings.Contains(string(spec.Stdin), "Origin Issue:\nNumber: 123") || !strings.Contains(string(spec.Stdin), "Base: main") {
						t.Fatal("starting invocation context not bound")
					}
					f.target = reviseMetadata(t, f.target, func(_ map[string]any, pr map[string]any) {
						if kind != "base" {
							pr["body"] = "#456 #789 ambiguous new specification"
						}
						if kind != "body" {
							pr["baseRefName"] = "release/retarget"
						}
					})
				}
				if containsString(spec.Args, "query="+revisePushTargetQuery) {
					finalReads++
					for _, forbidden := range []string{"body", "baseRef", "closingIssues", "defaultBranch", "pullRequests"} {
						if strings.Contains(revisePushTargetQuery, forbidden) {
							t.Fatalf("final read includes %s", forbidden)
						}
					}
					if f.head != revisionCommit {
						t.Fatal("final read preceded commit")
					}
				}
				if spec.Name == "git" && spec.Args[0] == "push" {
					pushes++
				}
				return f.respond(spec)
			}
			if err := f.service.Revise(42, io.Discard); err != nil {
				t.Fatal(err)
			}
			if starts != 1 || validations != 1 || specifications != 1 || finalReads != 1 || pushes != 1 {
				t.Fatalf("binding/final/push counts=%d/%d/%d/%d/%d", starts, validations, specifications, finalReads, pushes)
			}
		})
	}
}

func TestReviseRelationFailuresBeforeWorkspace(t *testing.T) {
	for _, kind := range []string{"unresolved", "ambiguous", "null body", "candidate unavailable", "candidate is PR"} {
		t.Run(kind, func(t *testing.T) {
			f := newReviseFixture(t, false)
			f.target = reviseMetadata(t, f.target, func(_ map[string]any, pr map[string]any) {
				switch kind {
				case "unresolved":
					pr["body"] = "No Issue reference"
				case "ambiguous":
					pr["body"] = "Refs #123 #124"
				case "null body":
					pr["body"] = nil
				}
			})
			f.runner.fn = func(spec CommandSpec) CommandResult {
				if containsString(spec.Args, "query="+githubOriginCandidateQuery) {
					if kind == "candidate unavailable" {
						return CommandResult{ExitCode: 1}
					}
					if kind == "candidate is PR" {
						result := reviewCandidateForTest(123)
						result.Stdout = strings.ReplaceAll(result.Stdout, `"Issue"`, `"PullRequest"`)
						return result
					}
					if containsString(spec.Args, "number=124") {
						return reviewCandidateForTest(124)
					}
				}
				return f.respond(spec)
			}
			if err := f.service.Revise(42, io.Discard); err == nil {
				t.Fatal("invalid body relation accepted")
			}
			if stages := revisionMutations(f.runner.calls); len(stages) != 0 {
				t.Fatalf("mutated before relation validation: %v", stages)
			}
		})
	}
}

func TestReviseCommitIntegrityAndUnknownPush(t *testing.T) {
	for _, kind := range []string{"merge parent", "wrong tree", "unknown push", "failed Human worker"} {
		t.Run(kind, func(t *testing.T) {
			f := newReviseFixture(t, kind != "failed Human worker")
			if kind == "failed Human worker" {
				f.branchName = "human/fix"
				f.target = strings.ReplaceAll(f.target, "iro/issue-123", f.branchName)
				f.worker = CommandResult{ExitCode: 7, Stdout: "失敗しました。"}
			}
			pushes := 0
			f.runner.fn = func(spec CommandSpec) CommandResult {
				if spec.Name == "git" {
					if kind == "merge parent" && spec.Args[0] == "rev-list" {
						return CommandResult{Stdout: revisionCommit + " " + revisionHead + " " + strings.Repeat("2", 40)}
					}
					if kind == "wrong tree" && containsString(spec.Args, "HEAD^{tree}") {
						return CommandResult{Stdout: revisionCommit}
					}
					if spec.Args[0] == "push" {
						pushes++
						if kind == "unknown push" {
							return CommandResult{ExitCode: -1, Err: errors.New("lost subprocess result")}
						}
					}
				}
				return f.respond(spec)
			}
			err := f.service.Revise(42, io.Discard)
			if err == nil || !strings.Contains(err.Error(), f.workspace) || !strings.Contains(err.Error(), "no automatic retry") {
				t.Fatalf("missing failure inspection diagnostic: %v", err)
			}
			wantPushes := 0
			if kind == "unknown push" {
				wantPushes = 1
				if !strings.Contains(err.Error(), "remote branch may have been updated") || !strings.Contains(err.Error(), revisionCommit) {
					t.Fatal(err)
				}
			}
			if pushes != wantPushes {
				t.Fatalf("pushes=%d, want %d", pushes, wantPushes)
			}
			if _, err := os.Stat(f.workspace); err != nil {
				t.Fatal("failed workspace removed", err)
			}
		})
	}
}

func TestRunProducerFixturesRoundTripThroughManagedRevise(t *testing.T) {
	for _, unmanaged := range []bool{false, true} {
		t.Run(fmt.Sprintf("unmanaged=%t", unmanaged), func(t *testing.T) {
			var service *Service
			var runner *fakeCommandRunner
			var root, producedPath, registrations string
			if unmanaged {
				producer := newUnmanagedFixture(t)
				service, runner, root = producer.service, producer.runner, producer.root
			} else {
				producer := newManagedProducerFixture(t)
				service, runner, root = producer.service, producer.runner, producer.root
				if err := service.runWithOptions(123, workerOptions{}, io.Discard, io.Discard); err != nil {
					t.Fatal(err)
				}
				producedPath, registrations = producer.paths[0], producer.registrations
			}
			if unmanaged {
				if err := service.runWithOptions(123, workerOptions{Unmanaged: true}, io.Discard, io.Discard); err != nil {
					t.Fatal(err)
				}
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
				t.Fatal("producer did not emit PR payload")
			}
			consumer := newReviseFixture(t, false)
			consumer.root = root
			consumer.service.Dirs = service.Dirs
			consumer.branchName = produced["head"].(string)
			consumer.workspace = deliveryWorktreePath(service.Dirs, githubRuntimeNamespace(consumer.identity), 123, deliveryID(strings.Repeat("a", 32)))
			consumer.target = reviseMetadata(t, consumer.target, func(repo, pr map[string]any) {
				delete(repo, "defaultBranchRef")
				delete(pr, "closingIssuesReferences")
				pr["body"], pr["baseRefName"], pr["headRefName"] = produced["body"], produced["base"], produced["head"]
			})
			if !unmanaged {
				consumer.workspace, consumer.branch = producedPath, true
				consumer.registration = strings.ReplaceAll(registrations, "\x00", "\n")
			}
			writeProjectFiles(t, root)
			if err := consumer.service.Revise(42, io.Discard); err != nil {
				t.Fatal(err)
			}
			workers, creates := 0, 0
			for _, call := range consumer.runner.calls {
				if call.Name == "git" && containsArgs(call.Args, "worktree", "add") {
					creates++
				}
				if call.Name == "codex" && containsString(call.Args, "--ephemeral") {
					workers++
					for _, want := range []string{"Origin Issue:\nNumber: 123", "Pull request body:\n" + produced["body"].(string), "Base: " + produced["base"].(string), "Head: " + consumer.branchName} {
						if !strings.Contains(string(call.Stdin), want) {
							t.Fatalf("producer binding missing %q", want)
						}
					}
					if !unmanaged && call.Dir != producedPath {
						t.Fatal("managed producer worktree was not reused")
					}
				}
			}
			wantCreates := 0
			if unmanaged {
				wantCreates = 1
			}
			if workers != 1 || creates != wantCreates {
				t.Fatalf("workers/creates=%d/%d", workers, creates)
			}
		})
	}
}
