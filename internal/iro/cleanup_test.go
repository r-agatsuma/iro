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

type cleanupFixture struct {
	service      *Service
	runner       *fakeCommandRunner
	root         string
	identity     RepositoryIdentity
	workspace    string
	mapping      string
	branch       string
	collision    string
	worktreeGone bool
	branchGone   bool
}

func newCleanupFixture(t *testing.T) *cleanupFixture {
	t.Helper()
	root := t.TempDir()
	runner := &fakeCommandRunner{}
	service := newTestService(t, runner, root)
	identity := RepositoryIdentity{Owner: "acme", Name: "iro"}
	workspace := cleanAbsolutePath(worktreePath(service.Dirs, identity, 123))
	if err := os.MkdirAll(workspace, 0755); err != nil {
		t.Fatal(err)
	}
	writeProjectFiles(t, root)
	mappingPath := ownershipPath(service.Dirs, identity, 123)
	mapping := ownershipMapping{
		Version:     1,
		Repository:  identity.Canonical(),
		IssueNumber: 123,
		Branch:      "iro/issue-123",
		Worktree:    workspace,
	}
	if err := service.writeOwnership(mappingPath, mapping); err != nil {
		t.Fatal(err)
	}

	fixture := &cleanupFixture{
		service:   service,
		runner:    runner,
		root:      root,
		identity:  identity,
		workspace: workspace,
		mapping:   mappingPath,
		branch:    mapping.Branch,
	}
	runner.fn = fixture.run
	return fixture
}

func (f *cleanupFixture) run(spec CommandSpec) CommandResult {
	if spec.Name != "git" {
		return CommandResult{ExitCode: -1, Err: fmt.Errorf("unexpected non-Git command: %s", spec.Name)}
	}
	switch {
	case len(spec.Args) == 2 && spec.Args[0] == "rev-parse" && spec.Args[1] == "--show-toplevel":
		return CommandResult{Stdout: f.root + "\n", ExitCode: 0}
	case len(spec.Args) == 3 && spec.Args[0] == "config" && spec.Args[1] == "--get-all" && spec.Args[2] == "remote.origin.url":
		return CommandResult{Stdout: "git@github.com:acme/iro.git\n", ExitCode: 0}
	case len(spec.Args) == 4 && spec.Args[0] == "show-ref" && spec.Args[1] == "--verify" && spec.Args[2] == "--quiet" && spec.Args[3] == "refs/heads/"+f.branch:
		if f.branchGone {
			return CommandResult{ExitCode: 1, Err: errors.New("not found")}
		}
		return CommandResult{ExitCode: 0}
	case len(spec.Args) == 3 && spec.Args[0] == "worktree" && spec.Args[1] == "list" && spec.Args[2] == "--porcelain":
		if f.worktreeGone {
			return CommandResult{Stdout: "worktree " + f.root + "\nbranch refs/heads/main\n\n", ExitCode: 0}
		}
		output := "worktree " + f.root + "\nbranch refs/heads/main\n\nworktree " + f.workspace + "\nbranch refs/heads/" + f.branch + "\n\n"
		if f.collision != "" {
			output += "worktree " + f.collision + "\nbranch refs/heads/" + f.branch + "\n\n"
		}
		return CommandResult{Stdout: output, ExitCode: 0}
	case len(spec.Args) == 4 && spec.Args[0] == "--no-optional-locks" && spec.Args[1] == "status" && spec.Args[2] == "--porcelain" && spec.Args[3] == "--untracked-files=all":
		return CommandResult{ExitCode: 0}
	case len(spec.Args) == 4 && spec.Args[0] == "merge-base" && spec.Args[1] == "--is-ancestor" && spec.Args[2] == f.branch && spec.Args[3] == "HEAD":
		return CommandResult{ExitCode: 0}
	case len(spec.Args) == 3 && spec.Args[0] == "worktree" && spec.Args[1] == "remove" && spec.Args[2] == f.workspace:
		if err := os.RemoveAll(f.workspace); err != nil {
			return CommandResult{ExitCode: 1, Err: err}
		}
		f.worktreeGone = true
		return CommandResult{ExitCode: 0}
	case len(spec.Args) == 3 && spec.Args[0] == "branch" && spec.Args[1] == "-d" && spec.Args[2] == f.branch:
		f.branchGone = true
		return CommandResult{ExitCode: 0}
	default:
		return CommandResult{ExitCode: -1, Err: fmt.Errorf("unexpected cleanup command: %s %v", spec.Name, spec.Args)}
	}
}

func (f *cleanupFixture) mappingExists(t *testing.T) bool {
	t.Helper()
	_, err := os.Stat(f.mapping)
	return err == nil
}

func TestCleanupCompletesVerifiedLocalLifecycle(t *testing.T) {
	fixture := newCleanupFixture(t)
	if err := os.WriteFile(filepath.Join(fixture.workspace, ".env"), []byte("disposable"), 0600); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	if err := fixture.service.Cleanup(123, &output); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if fixture.mappingExists(t) {
		t.Fatal("ownership mapping was not removed")
	}
	if !fixture.worktreeGone || !fixture.branchGone {
		t.Fatalf("cleanup state = worktreeGone:%v branchGone:%v", fixture.worktreeGone, fixture.branchGone)
	}
	for _, want := range []string{"Issue #123 cleanup complete.", fixture.workspace, fixture.branch, "Removed ownership mapping."} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("cleanup output does not contain %q: %s", want, output.String())
		}
	}
	assertCleanupMutationOrder(t, fixture.runner.calls)
}

func TestCleanupRejectsAncestorFailureBeforeMutation(t *testing.T) {
	fixture := newCleanupFixture(t)
	fixture.runner.fn = func(spec CommandSpec) CommandResult {
		if len(spec.Args) == 4 && spec.Args[0] == "merge-base" {
			return CommandResult{ExitCode: 1, Err: errors.New("not contained")}
		}
		return fixture.run(spec)
	}
	var output strings.Builder
	err := fixture.service.Cleanup(123, &output)
	if err == nil || !strings.Contains(err.Error(), "not contained in the invoking HEAD history") {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if fixture.mappingExists(t) == false || fixture.worktreeGone || fixture.branchGone {
		t.Fatal("ancestor failure changed cleanup resources")
	}
	if strings.Contains(output.String(), "cleanup complete") {
		t.Fatal("ancestor failure reported success")
	}
	assertNoCleanupMutation(t, fixture.runner.calls)
}

func TestCleanupRejectsDirtyTargetWithoutMutation(t *testing.T) {
	fixture := newCleanupFixture(t)
	fixture.runner.fn = func(spec CommandSpec) CommandResult {
		if len(spec.Args) == 4 && spec.Args[0] == "--no-optional-locks" {
			return CommandResult{Stdout: "?? human.txt\n", ExitCode: 0}
		}
		return fixture.run(spec)
	}
	err := fixture.service.Cleanup(123, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "is dirty") {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if fixture.mappingExists(t) == false || fixture.worktreeGone || fixture.branchGone {
		t.Fatal("dirty target was changed")
	}
	assertNoCleanupMutation(t, fixture.runner.calls)
}

func TestCleanupRejectsInvokingTargetWorktree(t *testing.T) {
	identity := RepositoryIdentity{Owner: "acme", Name: "iro"}
	runner := &fakeCommandRunner{}
	service := newTestService(t, runner, "")
	root := cleanAbsolutePath(worktreePath(service.Dirs, identity, 123))
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	writeProjectFiles(t, root)
	mappingPath := ownershipPath(service.Dirs, identity, 123)
	if err := service.writeOwnership(mappingPath, ownershipMapping{Version: 1, Repository: identity.Canonical(), IssueNumber: 123, Branch: "iro/issue-123", Worktree: root}); err != nil {
		t.Fatal(err)
	}
	fixture := &cleanupFixture{service: service, runner: runner, root: root, identity: identity, workspace: root, mapping: mappingPath, branch: "iro/issue-123"}
	runner.fn = fixture.run
	err := service.Cleanup(123, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "invoking checkout is the cleanup target") {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if _, statErr := os.Stat(root); statErr != nil {
		t.Fatalf("invoking checkout was changed: %v", statErr)
	}
	if !fixture.mappingExists(t) {
		t.Fatal("ownership mapping was removed")
	}
	assertNoCleanupMutation(t, runner.calls)
}

func TestCleanupRejectsIssueBranchCheckedOutInAnotherWorktree(t *testing.T) {
	fixture := newCleanupFixture(t)
	fixture.collision = filepath.Join(fixture.root, "other-worktree")

	err := fixture.service.Cleanup(123, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "branch/worktree collision") {
		t.Fatalf("Cleanup() error = %v", err)
	}
	for _, want := range []string{fixture.branch, fixture.collision, "duplicate Issue branch checkout"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Cleanup() error does not contain %q: %v", want, err)
		}
	}
	if fixture.mappingExists(t) == false || !pathExists(fixture.workspace) || fixture.branchGone {
		t.Fatal("branch collision changed cleanup resources")
	}
	assertNoCleanupMutation(t, fixture.runner.calls)
}

func TestCleanupKeepsMappingWhenWorktreeRemovalFails(t *testing.T) {
	fixture := newCleanupFixture(t)
	fixture.runner.fn = func(spec CommandSpec) CommandResult {
		if len(spec.Args) == 3 && spec.Args[0] == "worktree" && spec.Args[1] == "remove" {
			return CommandResult{ExitCode: 1, Err: errors.New("dirty")}
		}
		return fixture.run(spec)
	}
	err := fixture.service.Cleanup(123, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "normal removal") {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if fixture.mappingExists(t) == false || !pathExists(fixture.workspace) || fixture.branchGone {
		t.Fatal("worktree removal failure did not preserve resources")
	}
	for _, call := range fixture.runner.calls {
		if len(call.Args) >= 2 && call.Name == "git" && call.Args[0] == "branch" {
			t.Fatal("branch deletion was attempted after worktree removal failure")
		}
	}
}

func TestCleanupKeepsMappingWhenBranchDeletionFails(t *testing.T) {
	fixture := newCleanupFixture(t)
	fixture.runner.fn = func(spec CommandSpec) CommandResult {
		if len(spec.Args) == 3 && spec.Args[0] == "branch" && spec.Args[1] == "-d" {
			if err := os.RemoveAll(fixture.workspace); err != nil {
				return CommandResult{ExitCode: 1, Err: err}
			}
			fixture.worktreeGone = true
			return CommandResult{ExitCode: 1, Err: errors.New("not fully merged")}
		}
		return fixture.run(spec)
	}
	err := fixture.service.Cleanup(123, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "normal safe deletion") {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if fixture.mappingExists(t) == false || pathExists(fixture.workspace) || fixture.branchGone {
		t.Fatal("branch deletion failure did not preserve mapping and partial state")
	}
	for _, call := range fixture.runner.calls {
		if len(call.Args) >= 2 && call.Name == "git" && call.Args[0] == "branch" && containsArg(call.Args, "-D") {
			t.Fatal("force branch deletion was attempted")
		}
	}
}

func TestExecuteCleanupRejectsInvalidUsage(t *testing.T) {
	runner := &fakeCommandRunner{}
	service := NewService(runner, NewOSFileSystem())
	for _, args := range [][]string{{"cleanup", "1", "2"}, {"cleanup", "--all"}, {"cleanup", "0"}, {"cleanup", "12x"}} {
		var stderr strings.Builder
		if status := Execute(args, io.Discard, &stderr, service); status != 2 {
			t.Fatalf("Execute(%v) = %d, want usage status 2; stderr=%s", args, status, stderr.String())
		}
	}
	if len(runner.calls) != 0 {
		t.Fatalf("invalid usage invoked commands: %v", runner.calls)
	}
}

func assertCleanupMutationOrder(t *testing.T, calls []CommandSpec) {
	t.Helper()
	worktreeIndex := -1
	branchIndex := -1
	for index, call := range calls {
		if call.Name != "git" {
			continue
		}
		if len(call.Args) >= 2 && call.Args[0] == "worktree" && call.Args[1] == "remove" {
			worktreeIndex = index
		}
		if len(call.Args) >= 2 && call.Args[0] == "branch" && call.Args[1] == "-d" {
			branchIndex = index
		}
	}
	if worktreeIndex < 0 || branchIndex < 0 || worktreeIndex >= branchIndex {
		t.Fatalf("unexpected cleanup mutation order: worktree=%d branch=%d calls=%v", worktreeIndex, branchIndex, calls)
	}
}

func assertNoCleanupMutation(t *testing.T, calls []CommandSpec) {
	t.Helper()
	for _, call := range calls {
		if call.Name != "git" || len(call.Args) == 0 {
			continue
		}
		if (call.Args[0] == "worktree" && len(call.Args) > 1 && call.Args[1] == "remove") ||
			(call.Args[0] == "branch" && len(call.Args) > 1 && (call.Args[1] == "-d" || call.Args[1] == "-D")) {
			t.Fatalf("unexpected cleanup mutation command: %v", call.Args)
		}
	}
}

func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestBulkCleanupMixedResourcesAndPartialFailure(t *testing.T) {
	base := newCleanupFixture(t)
	base.runner.lookups = map[string]error{"gh": errors.New("not installed")}
	fixtures := map[int]*cleanupFixture{123: base}
	for _, number := range []int{2, 3, 4, 5, 6} {
		f := &cleanupFixture{service: base.service, root: base.root, identity: base.identity,
			branch:    fmt.Sprintf("iro/issue-%d", number),
			workspace: worktreePath(base.service.Dirs, base.identity, number),
			mapping:   ownershipPath(base.service.Dirs, base.identity, number)}
		if err := os.MkdirAll(f.workspace, 0755); err != nil {
			t.Fatal(err)
		}
		if err := base.service.writeOwnership(f.mapping, ownershipMapping{Version: 1, Repository: base.identity.Canonical(), IssueNumber: number, Branch: f.branch, Worktree: f.workspace}); err != nil {
			t.Fatal(err)
		}
		fixtures[number] = f
	}
	if err := os.WriteFile(fixtures[4].mapping, []byte("invalid json"), 0600); err != nil {
		t.Fatal(err)
	}
	other := RepositoryIdentity{Owner: "other", Name: "repo"}
	otherMapping := ownershipPath(base.service.Dirs, other, 1)
	if err := base.service.writeOwnership(otherMapping, ownershipMapping{Version: 99}); err != nil {
		t.Fatal(err)
	}
	unowned := worktreePath(base.service.Dirs, base.identity, 7)
	if err := os.MkdirAll(unowned, 0755); err != nil {
		t.Fatal(err)
	}
	base.runner.fn = func(spec CommandSpec) CommandResult {
		if strings.Join(spec.Args, " ") == "worktree list --porcelain" {
			output := "worktree " + base.root + "\nbranch refs/heads/main\n\n"
			for _, number := range []int{2, 3, 4, 5, 6, 123} {
				f := fixtures[number]
				if !f.worktreeGone {
					output += "worktree " + f.workspace + "\nbranch refs/heads/" + f.branch + "\n\n"
				}
			}
			return CommandResult{Stdout: output}
		}
		for number, f := range fixtures {
			if spec.Dir == f.workspace && containsArg(spec.Args, "status") {
				if number == 3 {
					return CommandResult{Stdout: "?? human.txt\n"}
				}
				return CommandResult{}
			}
			if containsArg(spec.Args, f.workspace) || containsArg(spec.Args, f.branch) || containsArg(spec.Args, "refs/heads/"+f.branch) {
				if number == 5 && containsArg(spec.Args, "-d") {
					return CommandResult{ExitCode: 1}
				}
				if number == 6 && containsArg(spec.Args, "merge-base") {
					return CommandResult{ExitCode: 1}
				}
				return f.run(spec)
			}
		}
		return base.run(spec)
	}
	var output, stderr strings.Builder
	if code := Execute([]string{"cleanup"}, &output, &stderr, base.service); code != 1 {
		t.Fatalf("code=%d stderr=%s", code, &stderr)
	}
	previous := -1
	for _, want := range []string{"Issue #2: cleaned", "Issue #3: skipped", "Issue #4: failed / attention required", "Issue #5: failed / attention required", "Issue #6: failed / attention required", "Issue #123: cleaned", "2 cleaned, 1 skipped, 3 failed"} {
		index := strings.Index(output.String(), want)
		if index <= previous {
			t.Fatalf("missing or unordered %q in %s", want, &output)
		}
		previous = index
	}
	for _, number := range []int{2, 123} {
		f := fixtures[number]
		if f.mappingExists(t) || !f.worktreeGone || !f.branchGone {
			t.Fatalf("Issue %d not cleaned", number)
		}
	}
	for _, number := range []int{3, 4, 6} {
		f := fixtures[number]
		if !f.mappingExists(t) || f.worktreeGone || f.branchGone {
			t.Fatalf("Issue %d changed", number)
		}
	}
	if !fixtures[5].mappingExists(t) || !fixtures[5].worktreeGone || fixtures[5].branchGone {
		t.Fatal("partial failure state not preserved")
	}
	if !pathExists(otherMapping) || !pathExists(unowned) {
		t.Fatal("unrelated resources changed")
	}
	for _, call := range base.runner.calls {
		if call.Name != "git" || containsArg(call.Args, "--force") || containsArg(call.Args, "-D") || containsArg(call.Args, "fetch") || containsArg(call.Args, "push") || containsArg(call.Args, "ls-remote") {
			t.Fatalf("unexpected command: %+v", call)
		}
	}
}

func TestBulkCleanupSuccessExitStatus(t *testing.T) {
	for _, state := range []string{"clean", "dirty", "empty"} {
		t.Run(state, func(t *testing.T) {
			f := newCleanupFixture(t)
			if state == "empty" {
				if err := os.Remove(f.mapping); err != nil {
					t.Fatal(err)
				}
			}
			f.runner.fn = func(spec CommandSpec) CommandResult {
				if state == "dirty" && containsArg(spec.Args, "status") {
					return CommandResult{Stdout: " M human.txt\n"}
				}
				return f.run(spec)
			}
			var output, stderr strings.Builder
			if code := Execute([]string{"cleanup"}, &output, &stderr, f.service); code != 0 {
				t.Fatalf("code=%d stderr=%s", code, &stderr)
			}
			if state != "clean" {
				assertNoCleanupMutation(t, f.runner.calls)
			}
			if state == "dirty" && !f.mappingExists(t) {
				t.Fatal("dirty mapping removed")
			}
			if state == "clean" && f.mappingExists(t) {
				t.Fatal("clean mapping retained")
			}
		})
	}
}

func TestBulkCleanupPreservesBrokenResources(t *testing.T) {
	for _, state := range []string{"missing branch", "missing worktree", "invalid repository", "collision", "nonregular mapping"} {
		t.Run(state, func(t *testing.T) {
			f := newCleanupFixture(t)
			switch state {
			case "missing branch":
				f.branchGone = true
			case "missing worktree":
				if err := os.RemoveAll(f.workspace); err != nil {
					t.Fatal(err)
				}
			case "invalid repository":
				mapping, _, err := f.service.readOwnership(f.mapping)
				if err != nil {
					t.Fatal(err)
				}
				mapping.Repository = "other/repo"
				if err := os.Remove(f.mapping); err != nil {
					t.Fatal(err)
				}
				if err := f.service.writeOwnership(f.mapping, mapping); err != nil {
					t.Fatal(err)
				}
			case "collision":
				f.collision = filepath.Join(f.root, "collision")
			case "nonregular mapping":
				if err := os.Remove(f.mapping); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(f.mapping, 0755); err != nil {
					t.Fatal(err)
				}
			}
			var output strings.Builder
			if err := f.service.CleanupAll(&output); err == nil {
				t.Fatal("broken resource reported success")
			}
			if !strings.Contains(output.String(), "Issue #123: failed / attention required") {
				t.Fatalf("missing diagnostic: %s", &output)
			}
			assertNoCleanupMutation(t, f.runner.calls)
			if !f.mappingExists(t) {
				t.Fatal("broken mapping removed")
			}
		})
	}
}
