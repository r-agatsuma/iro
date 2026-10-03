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

// Carry producer observations into the local-only consumer fixture. Commands
// remain simulated; filesystem artifacts are the producer's actual temp files.
func foundationLocalFixture(t *testing.T, service *Service, root string, inventory localGitInventory) *localLifecycleFixture {
	t.Helper()
	f := newLocalLifecycleFixture(t)
	f.root, f.common, f.service.Dirs = root, inventory.CommonDir, service.Dirs
	f.refs, f.worktrees = map[string]string{}, map[string]registeredWorktree{}
	for _, ref := range inventory.Refs {
		f.refs[strings.TrimPrefix(ref.Name, "refs/heads/")] = ref.HEAD
	}
	for _, worktree := range inventory.Worktrees {
		f.worktrees[worktree.Path] = worktree
	}
	return f
}

func TestFoundationUnmanagedRunResidueDoesNotSupplyManagedAuthority(t *testing.T) {
	producer := newUnmanagedFixture(t)
	for _, name := range []string{"iro.toml", "WORKFLOW.md"} {
		if err := os.WriteFile(filepath.Join(producer.root, name), []byte("invalid project authority"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	producer.intercept = func(spec CommandSpec) (CommandResult, bool) {
		if spec.Name == "git" && containsArgs(spec.Args, "worktree", "remove") {
			return CommandResult{ExitCode: 1, Stderr: "teardown refused"}, true
		}
		return CommandResult{}, false
	}
	if err := producer.service.runWithOptions(123, workerOptions{Unmanaged: true}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	var produced struct{ Body, Head, Base string }
	for _, call := range producer.runner.calls {
		if call.Name == "gh" && containsArgs(call.Args, "--method", "POST") {
			if err := json.Unmarshal(call.Stdin, &produced); err != nil {
				t.Fatal(err)
			}
		}
	}
	if produced.Head == "" || !strings.Contains(produced.Body, "Refs #123") {
		t.Fatal("unmanaged producer did not create the raw-body fixture")
	}
	// An unmanaged success does not create or repair managed project authority.
	if err := producer.service.Review(42, io.Discard); err == nil {
		t.Fatal("managed Review adopted the unmanaged configuration/policy")
	}
	for _, name := range []string{"iro.toml", "WORKFLOW.md"} {
		if data, err := os.ReadFile(filepath.Join(producer.root, name)); err != nil || string(data) != "invalid project authority" {
			t.Fatalf("unmanaged producer reconciled project files: %s %v", data, err)
		}
	}
	// Human supplies managed project authority. Its invocation policy differs
	// from the starting HEAD policy, and neither comes from the earlier worker.
	writeProjectFiles(t, producer.root)
	const invocationPolicy, startingPolicy = "Managed invocation Review policy", "Managed fixed starting-H1 Revise policy"
	if err := os.WriteFile(filepath.Join(producer.root, "WORKFLOW.md"), []byte(invocationPolicy), 0600); err != nil {
		t.Fatal(err)
	}
	metadata := reviewMetadataForTest(t, func(repo, pr map[string]any) {
		delete(repo, "defaultBranchRef")
		delete(pr, "closingIssuesReferences")
		pr["body"], pr["headRefName"], pr["baseRefName"] = produced.Body, produced.Head, produced.Base
		pr["headRepository"] = map[string]any{"nameWithOwner": "acme/iro"}
	})
	reviewed := false
	reviewRunner := &fakeCommandRunner{fn: func(spec CommandSpec) CommandResult {
		if containsString(spec.Args, "query="+managedReviewPreflightQuery) {
			return CommandResult{Stdout: metadata}
		}
		if spec.Name == "git" && (spec.Args[0] == "worktree" || spec.Args[0] == "for-each-ref") {
			t.Fatal("managed Review inspected unmanaged local residue")
		}
		if spec.Name == "codex" && containsString(spec.Args, "--ephemeral") {
			reviewed = true
			if !strings.Contains(string(spec.Stdin), invocationPolicy) || strings.Contains(string(spec.Stdin), "Built-in unmanaged") {
				t.Fatal("Review inherited the unmanaged worker authority")
			}
		}
		return reviewFakeResult(spec, producer.root, "managed advisory review")
	}}
	reviewService := newTestService(t, reviewRunner, producer.root)
	reviewService.Dirs = producer.service.Dirs
	if err := reviewService.Review(42, io.Discard); err != nil || !reviewed {
		t.Fatalf("managed Review rejected unmanaged provenance: %v", err)
	}
	residue := registeredWorktree{Path: producer.workspace, HEAD: producer.head, Detached: true}
	local := foundationLocalFixture(t, producer.service, producer.root, localGitInventory{
		CommonDir: filepath.Join(producer.root, ".git"),
		Worktrees: []registeredWorktree{{Path: producer.root, HEAD: foundationHEAD, Branch: "refs/heads/release/topic"}, residue},
	})
	revision := newReviseFixture(t, false)
	revision.root, revision.service.Dirs, revision.target, revision.branchName = producer.root, producer.service.Dirs, metadata, produced.Head
	revised := false
	revision.runner.fn = func(spec CommandSpec) CommandResult {
		if spec.Name == "git" {
			if spec.Args[0] == "for-each-ref" || containsArgs(spec.Args, "worktree", "list") {
				return local.run(spec)
			}
			if containsArgs(spec.Args, "worktree", "add") {
				result := revision.respond(spec)
				local.refs[produced.Head] = revision.head
				local.worktrees[revision.workspace] = registeredWorktree{Path: revision.workspace, HEAD: revision.head, Branch: "refs/heads/" + produced.Head}
				return result
			}
			if spec.Args[0] == "commit" {
				result := revision.respond(spec)
				local.refs[produced.Head] = revision.head
				worktree := local.worktrees[revision.workspace]
				worktree.HEAD = revision.head
				local.worktrees[revision.workspace] = worktree
				return result
			}
			if containsArgs(spec.Args, "cat-file", "blob") {
				return CommandResult{Stdout: startingPolicy}
			}
		}
		if spec.Name == "codex" && containsString(spec.Args, "--ephemeral") {
			revised = true
			if spec.Dir == producer.workspace || !strings.Contains(string(spec.Stdin), startingPolicy) || strings.Contains(string(spec.Stdin), invocationPolicy) || strings.Contains(string(spec.Stdin), "Built-in unmanaged") {
				t.Fatal("Revise adopted unmanaged residue or the wrong policy authority")
			}
		}
		return revision.respond(spec)
	}
	if err := revision.service.Revise(42, io.Discard); err != nil || !revised {
		t.Fatalf("managed Revise rejected unmanaged provenance: %v", err)
	}
	if local.worktrees[producer.workspace] != residue {
		t.Fatal("managed Revise reconciled the unmanaged registration")
	}
	if _, err := os.Stat(producer.workspace); err != nil {
		t.Fatal("managed consumer removed unmanaged residue", err)
	}
	ordinary := filepath.Join(t.TempDir(), "Human detached checkout")
	local.addWorktree(t, ordinary, "")
	unknown := local.runtimePath(unmanagedWorkspace, "run-issue-123-"+string(foundationOtherID))
	if err := os.MkdirAll(unknown, 0700); err != nil {
		t.Fatal(err)
	}
	var status strings.Builder
	if err := local.service.Status(&status); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status.String(), strconv.Quote(producer.workspace)) || !strings.Contains(status.String(), strconv.Quote(revision.workspace)) || strings.Contains(status.String(), ordinary) || strings.Contains(status.String(), unknown) {
		t.Fatalf("Status adopted ordinary/unknown residue or lost producer resources: %s", status.String())
	}
	if err := local.service.Cleanup(123, io.Discard); err != nil || len(local.refs) != 0 || len(local.worktrees) != 2 {
		t.Fatalf("physical Issue selection did not purge both registered producer kinds: %v", err)
	}
	for _, path := range []string{ordinary, unknown} {
		if _, err := os.Stat(path); err != nil {
			t.Fatal("Cleanup discovered/adopted an ordinary or unregistered workspace", err)
		}
	}
}

func TestFoundationDeliveriesBodyRebindAndPhysicalCleanup(t *testing.T) {
	producer := newManagedProducerFixture(t)
	var deliveries []struct{ Body, Head, Base string }
	producer.intercept = func(spec CommandSpec) (CommandResult, bool) {
		if spec.Name == "gh" && containsArgs(spec.Args, "--method", "POST") {
			var pr struct{ Body, Head, Base string }
			if err := json.Unmarshal(spec.Stdin, &pr); err != nil {
				t.Fatal(err)
			}
			deliveries = append(deliveries, pr)
		}
		return CommandResult{}, false
	}
	for i := 0; i < 2; i++ {
		if err := producer.service.Run(123, io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	if len(deliveries) != 2 || deliveries[0].Head == deliveries[1].Head {
		t.Fatalf("same-Issue deliveries were not independent: %+v", deliveries)
	}
	inventory, err := producer.service.localInventory(producer.root)
	if err != nil {
		t.Fatal(err)
	}
	local := foundationLocalFixture(t, producer.service, producer.root, inventory)
	// A physical Issue 124 ref must remain independent of a body rebind to 124.
	local.refs[deliveryBranch(124, foundationID)] = foundationHEAD
	for i, delivery := range deliveries {
		// Review each delivery before and after a later body edit. No provenance,
		// native relation, default base, or other-delivery enumeration is needed.
		for _, issue := range []int{123, 124} {
			body := delivery.Body
			if issue == 124 {
				body = "Refs #124"
			}
			metadata := reviewMetadataForTest(t, func(repo, pr map[string]any) {
				delete(repo, "defaultBranchRef")
				delete(pr, "closingIssuesReferences")
				pr["headRefName"], pr["baseRefName"], pr["body"] = delivery.Head, delivery.Base, body
				pr["headRepository"] = map[string]any{"nameWithOwner": "acme/iro"}
			})
			workers := 0
			runner := &fakeCommandRunner{fn: func(spec CommandSpec) CommandResult {
				if containsString(spec.Args, "query="+managedReviewPreflightQuery) {
					return CommandResult{Stdout: metadata}
				}
				if containsString(spec.Args, "query="+githubOriginCandidateQuery) {
					if !containsString(spec.Args, "number="+strconv.Itoa(issue)) {
						t.Fatalf("Review cached the earlier body relation: %+v", spec)
					}
					return reviewCandidateForTest(issue)
				}
				if spec.Name == "gh" && containsArgs(spec.Args, "issue", "view") {
					if spec.Args[2] != strconv.Itoa(issue) {
						t.Fatal("Review fetched the physical namespace Issue")
					}
					return CommandResult{Stdout: fmt.Sprintf(`{"number":%d,"title":"Specification","body":"Current task","url":"https://github.com/acme/iro/issues/%d","comments":[]}`, issue, issue)}
				}
				if spec.Name == "codex" && containsString(spec.Args, "--ephemeral") {
					workers++
					if !strings.Contains(string(spec.Stdin), fmt.Sprintf("Origin Issue:\nNumber: %d", issue)) {
						t.Fatal("Review did not bind the current specification")
					}
				}
				if spec.Name == "gh" && containsString(spec.Args, "graphql") {
					t.Fatalf("unexpected relation/topology query: %+v", spec)
				}
				return reviewFakeResult(spec, producer.root, "advisory review")
			}}
			reviewService := newTestService(t, runner, producer.root)
			reviewService.Dirs = producer.service.Dirs
			if err := reviewService.Review(42, io.Discard); err != nil || workers != 1 {
				t.Fatalf("Review Issue %d: workers=%d, err=%v", issue, workers, err)
			}

			revision := newReviseFixture(t, false)
			revision.root, revision.service.Dirs = producer.root, producer.service.Dirs
			revision.workspace, revision.branchName, revision.branch = producer.paths[i], delivery.Head, true
			revision.target = metadata
			workers = 0
			revision.runner.fn = func(spec CommandSpec) CommandResult {
				if spec.Name == "git" && (spec.Args[0] == "for-each-ref" || containsArgs(spec.Args, "worktree", "list")) {
					return local.run(spec)
				}
				if containsString(spec.Args, "query="+githubOriginCandidateQuery) {
					if !containsString(spec.Args, "number="+strconv.Itoa(issue)) {
						t.Fatal("Revise cached the earlier body relation")
					}
					return reviewCandidateForTest(issue)
				}
				if spec.Name == "gh" && containsArgs(spec.Args, "issue", "view") {
					return runner.fn(spec)
				}
				if spec.Name == "codex" && containsString(spec.Args, "--ephemeral") {
					workers++
					if spec.Dir != producer.paths[i] || !strings.Contains(string(spec.Stdin), fmt.Sprintf("Origin Issue:\nNumber: %d", issue)) {
						t.Fatal("Revise used an Issue-derived canonical workspace or stale specification")
					}
					// Stop before commit: this invocation proves the next binding while
					// preserving H1 for the next explicit invocation and local cleanup.
					return CommandResult{ExitCode: 1, Stdout: "検証用の停止。"}
				}
				return revision.respond(spec)
			}
			if err := revision.service.Revise(42, io.Discard); err == nil || workers != 1 {
				t.Fatalf("Revise Issue %d: workers=%d, err=%v", issue, workers, err)
			}
		}

		// Land does not resolve even an ambiguous edited body. Local delivery
		// resources remain untouched regardless of the merge response outcome.
		land := newLandFixture(t)
		land.root, land.service.Dirs = producer.root, producer.service.Dirs
		land.service.FileSystem = landProjectFiles{t: t, root: producer.root, rejectWorkflow: true}
		land.target = reviseMetadata(t, land.target, func(repo, pr map[string]any) {
			delete(repo, "defaultBranchRef")
			delete(pr, "closingIssuesReferences")
			pr["headRefName"], pr["baseRefName"], pr["body"] = delivery.Head, delivery.Base, "#123 #124"
		})
		if i == 1 {
			land.mergeResult = CommandResult{ExitCode: -1, Err: errors.New("merge response lost")}
		}
		var output strings.Builder
		err := land.service.Land(42, &output)
		if land.mergeCalls != 1 || (err == nil) != (i == 0) {
			t.Fatalf("Land attempts/status=%d/%v", land.mergeCalls, err)
		}
		if i == 1 && (!strings.Contains(err.Error(), "could not be confirmed") || strings.Contains(output.String(), "Landed")) {
			t.Fatalf("unknown merge outcome was hidden: %v %s", err, output.String())
		}
	}
	var status strings.Builder
	if err := local.service.Status(&status); err != nil {
		t.Fatal(err)
	}
	for i, delivery := range deliveries {
		if !strings.Contains(status.String(), delivery.Head) || !strings.Contains(status.String(), strconv.Quote(producer.paths[i])) {
			t.Fatal("consumer or Land changed local delivery inventory", status.String())
		}
	}
	if err := local.service.Cleanup(124, io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, delivery := range deliveries {
		if local.refs[delivery.Head] == "" {
			t.Fatal("Cleanup treated body rebind as physical ownership")
		}
	}
	// Explicit cleanup purges worker residue even after failed revisions and an
	// unknown merge; it makes no remote outcome or recoverability judgment.
	for _, path := range producer.paths {
		if err := os.WriteFile(filepath.Join(path, "unpublished.txt"), []byte("dirty worker residue"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := local.service.Cleanup(123, io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(local.refs) != 0 || len(local.worktrees) != 1 {
		t.Fatalf("physical delivery resources remain: %v %v", local.refs, local.worktrees)
	}
}

func TestFoundationRunUnknownOutcomeHandsOffToLocalCleanup(t *testing.T) {
	for _, stage := range []string{"push", "PR", "report"} {
		t.Run(stage, func(t *testing.T) {
			producer := newManagedProducerFixture(t)
			attempts := 0
			producer.intercept = func(spec CommandSpec) (CommandResult, bool) {
				if stage == "push" && spec.Name == "git" && spec.Args[0] == "push" || stage == "PR" && spec.Name == "gh" && containsArgs(spec.Args, "--method", "POST") || stage == "report" && spec.Name == "gh" && containsArgs(spec.Args, "pr", "comment") {
					attempts++
					return CommandResult{ExitCode: -1, Err: errors.New("response lost")}, true
				}
				return CommandResult{}, false
			}
			err := producer.service.Run(123, io.Discard)
			if attempts != 1 || (err == nil) != (stage == "report") {
				t.Fatalf("unknown delivery attempts/status=%d/%v", attempts, err)
			}
			inventory, err := producer.service.localInventory(producer.root)
			if err != nil {
				t.Fatal(err)
			}
			local := foundationLocalFixture(t, producer.service, producer.root, inventory)
			logs, err := filepath.Glob(filepath.Join(producer.service.Dirs.StateRoot, "deliveries", "*", "*.log"))
			if err != nil || len(logs) != 1 {
				t.Fatalf("outcome log missing: %v %v", logs, err)
			}
			before, err := os.ReadFile(logs[0])
			if err != nil || !strings.Contains(string(before), "unknown") {
				t.Fatalf("unknown outcome missing: %s %v", before, err)
			}
			if err := local.service.Status(io.Discard); err != nil {
				t.Fatal(err)
			}
			// One local removal fails; independent deletion continues without
			// turning partial cleanup into success or a retry/repair protocol.
			local.worktreeFail[producer.paths[0]] = true
			if err := local.service.Cleanup(123, io.Discard); err == nil {
				t.Fatal("partial cleanup reported success")
			}
			if len(local.refs) != 0 || len(local.fs.removed) != 1 || local.fs.removed[0] != producer.paths[0] {
				t.Fatal("independent branch and known-path cleanup did not continue")
			}
			after, err := os.ReadFile(logs[0])
			if err != nil || string(after) != string(before) || attempts != 1 {
				t.Fatal("local lifecycle changed/repaired the remote outcome observation")
			}
		})
	}
}

func TestFoundationReviseFinalReadNormalPushRace(t *testing.T) {
	for _, race := range []string{"rewind permits normal push", "advance rejects normal push", "push outcome unknown"} {
		t.Run(race, func(t *testing.T) {
			f := newReviseFixture(t, true)
			finalReads, pushes := 0, 0
			f.runner.fn = func(spec CommandSpec) CommandResult {
				if containsString(spec.Args, "query="+revisePushTargetQuery) {
					finalReads++
					if f.head != revisionCommit {
						t.Fatal("final revalidation preceded H1-parent commit")
					}
				}
				if spec.Name == "git" && spec.Args[0] == "push" {
					pushes++
					if finalReads != 1 || strings.Join(spec.Args, " ") != "push --no-follow-tags --no-recurse-submodules -- origin refs/heads/iro/issue-123:refs/heads/iro/issue-123" {
						t.Fatalf("race contract silently became CAS/lease/lock: %+v", spec)
					}
					// Simulate a change after the last read. A rewind may accept C1;
					// a conflicting advance rejects it through ordinary Git semantics.
					if race == "advance rejects normal push" {
						return CommandResult{ExitCode: 1, Stderr: "non-fast-forward"}
					}
					if race == "push outcome unknown" {
						return CommandResult{ExitCode: -1, Err: errors.New("push response lost")}
					}
				}
				return f.respond(spec)
			}
			err := f.service.Revise(42, io.Discard)
			if finalReads != 1 || pushes != 1 || (err == nil) != (race == "rewind permits normal push") {
				t.Fatalf("race attempts/status=%d/%d/%v", finalReads, pushes, err)
			}
			if err != nil && !strings.Contains(err.Error(), "remote branch may have been updated") {
				t.Fatal("push failure was interpreted as remote unchanged", err)
			}
			inventory, err := f.service.localInventory(f.root)
			if err != nil {
				t.Fatal(err)
			}
			local := foundationLocalFixture(t, f.service, f.root, inventory)
			var status strings.Builder
			if err := local.service.Status(&status); err != nil || !strings.Contains(status.String(), revisionCommit) {
				t.Fatalf("local child was not retained for inspection: %v %s", err, status.String())
			}
			if err := local.service.Cleanup(123, io.Discard); err != nil || len(local.refs) != 0 || pushes != 1 {
				t.Fatalf("local purge added remote reconciliation or preserved unpublished history: %v", err)
			}
		})
	}
}
