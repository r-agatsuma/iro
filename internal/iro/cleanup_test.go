package iro

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// All Git mutations are simulated. Fixtures touch only disposable files and
// never create or mutate actual Git metadata, refs, or remote state.
type localLifecycleFixture struct {
	service        *Service
	runner         *fakeCommandRunner
	fs             *localLifecycleFS
	root           string
	common         string
	refs           map[string]string
	worktrees      map[string]registeredWorktree
	worktreeFail   map[string]bool
	branchFail     map[string]bool
	keepRegistered map[string]bool
	leaveResidue   map[string]bool
	keepBranch     map[string]bool
	postFailure    string
	mutated        bool
}

type localLifecycleFS struct {
	FileSystem
	t             *testing.T
	removed       []string
	removeFailure map[string]bool
	statFailure   map[string]bool
	resolveFail   map[string]bool
}

func (fs *localLifecycleFS) ReadFile(path string) ([]byte, error) {
	fs.t.Fatalf("local lifecycle consulted a file: %s", path)
	return nil, os.ErrPermission
}

func (fs *localLifecycleFS) ReadDir(path string) ([]os.DirEntry, error) {
	fs.t.Fatalf("local lifecycle scanned a directory: %s", path)
	return nil, os.ErrPermission
}

func (fs *localLifecycleFS) RemoveAll(path string) error {
	fs.removed = append(fs.removed, path)
	if fs.removeFailure[path] {
		return os.ErrPermission
	}
	return fs.FileSystem.RemoveAll(path)
}

func (fs *localLifecycleFS) Lstat(path string) (os.FileInfo, error) {
	if fs.statFailure[path] {
		return nil, os.ErrPermission
	}
	return fs.FileSystem.Lstat(path)
}

func (fs *localLifecycleFS) EvalSymlinks(path string) (string, error) {
	if fs.resolveFail[path] {
		return "", os.ErrPermission
	}
	return fs.FileSystem.EvalSymlinks(path)
}

func newLocalLifecycleFixture(t *testing.T) *localLifecycleFixture {
	t.Helper()
	root := t.TempDir()
	runner := &fakeCommandRunner{lookups: map[string]error{
		"gh": errors.New("unavailable"), "codex": errors.New("unavailable"),
	}}
	service := newTestService(t, runner, root)
	fs := &localLifecycleFS{FileSystem: service.FileSystem, t: t, removeFailure: map[string]bool{}, statFailure: map[string]bool{}, resolveFail: map[string]bool{}}
	service.FileSystem = fs
	f := &localLifecycleFixture{
		service: service, runner: runner, fs: fs, root: root, common: filepath.Join(root, "common"),
		refs: map[string]string{}, worktrees: map[string]registeredWorktree{},
		worktreeFail: map[string]bool{}, branchFail: map[string]bool{},
		keepRegistered: map[string]bool{}, leaveResidue: map[string]bool{}, keepBranch: map[string]bool{},
	}
	f.worktrees[root] = registeredWorktree{Path: root, HEAD: foundationHEAD, Branch: "refs/heads/main"}
	runner.fn = f.run
	return f
}

func (f *localLifecycleFixture) run(spec CommandSpec) CommandResult {
	if spec.Name != "git" {
		f.fs.t.Fatalf("local lifecycle invoked non-Git command: %+v", spec)
	}
	args := strings.Join(spec.Args, " ")
	if args == "rev-parse --path-format=absolute --git-common-dir" && spec.Dir == "" {
		return CommandResult{Stdout: f.common + "\n"}
	}
	if spec.Dir != f.root && spec.Dir != f.common {
		f.fs.t.Fatalf("command escaped current common directory: %+v", spec)
	}
	switch args {
	case "rev-parse --path-format=absolute --git-common-dir":
		if f.mutated && f.postFailure == "common_changed" {
			return CommandResult{Stdout: f.common + "-other\n"}
		}
		return CommandResult{Stdout: f.common + "\n"}
	case "for-each-ref --sort=refname --format=%(refname)%00%(objectname) refs/heads/iro/":
		if f.mutated && f.postFailure == "refs" {
			return CommandResult{ExitCode: 1, Stderr: "unreadable refs"}
		}
		var names []string
		for branch := range f.refs {
			names = append(names, branch)
		}
		sort.Strings(names)
		var output strings.Builder
		for _, branch := range names {
			fmt.Fprintf(&output, "refs/heads/%s\x00%s\n", branch, f.refs[branch])
		}
		return CommandResult{Stdout: output.String()}
	case "worktree list --porcelain -z":
		if f.mutated && f.postFailure == "worktrees" {
			return CommandResult{Stdout: "malformed"}
		}
		var paths []string
		for path := range f.worktrees {
			paths = append(paths, path)
		}
		sort.Strings(paths)
		var output strings.Builder
		for _, path := range paths {
			worktree := f.worktrees[path]
			mode := "detached"
			if worktree.Branch != "" {
				mode = "branch " + worktree.Branch
			}
			if worktree.Locked {
				mode += "\x00locked Human reason"
			}
			if worktree.Prunable {
				mode += "\x00prunable missing path"
			}
			output.WriteString(worktreeRecord(path, mode))
		}
		return CommandResult{Stdout: output.String()}
	}
	if len(spec.Args) == 5 && spec.Args[0] == "worktree" && spec.Args[1] == "remove" && spec.Args[2] == "--force" && spec.Args[3] == "--" {
		f.mutated = true
		path := spec.Args[4]
		if _, registered := f.worktrees[path]; !registered {
			f.fs.t.Fatalf("removal of an undiscovered worktree: %q", path)
		}
		if f.worktreeFail[path] {
			return CommandResult{ExitCode: 1, Stderr: "Git refused removal"}
		}
		if !f.keepRegistered[path] {
			delete(f.worktrees, path)
		}
		if !f.leaveResidue[path] {
			if err := os.RemoveAll(path); err != nil {
				f.fs.t.Fatal(err)
			}
		}
		return CommandResult{}
	}
	if len(spec.Args) == 4 && spec.Args[0] == "branch" && spec.Args[1] == "-D" && spec.Args[2] == "--" {
		f.mutated = true
		branch := spec.Args[3]
		if _, exists := f.refs[branch]; !exists {
			f.fs.t.Fatalf("deletion of an undiscovered ref: %q", branch)
		}
		if f.branchFail[branch] {
			return CommandResult{ExitCode: 1, Stderr: "Git refused branch deletion"}
		}
		if !f.keepBranch[branch] {
			delete(f.refs, branch)
		}
		return CommandResult{}
	}
	f.fs.t.Fatalf("unexpected local lifecycle command: %+v", spec)
	return CommandResult{ExitCode: 1}
}

func (f *localLifecycleFixture) runtimePath(kind runtimeWorkspaceKind, leaf string) string {
	return filepath.Join(runtimeWorkspaceParent(f.service.Dirs, runtimeNamespaceKey("acme-iro-5f858f6c7d45"), kind), leaf)
}

func (f *localLifecycleFixture) addWorktree(t *testing.T, path, branch string) {
	t.Helper()
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	worktree := registeredWorktree{Path: path, HEAD: foundationHEAD, Detached: branch == ""}
	if branch != "" {
		worktree.Branch = "refs/heads/" + branch
	}
	f.worktrees[path] = worktree
}

func (f *localLifecycleFixture) addDelivery(t *testing.T, number int, id deliveryID) (string, string) {
	t.Helper()
	branch := deliveryBranch(number, id)
	path := f.runtimePath(managedWorkspace, deliveryLeaf(number, id))
	f.refs[branch] = foundationHEAD
	f.addWorktree(t, path, branch)
	return branch, path
}

func TestCleanupSelectionBulkAndIssueScoped(t *testing.T) {
	for _, bulk := range []bool{false, true} {
		t.Run(fmt.Sprintf("bulk_%t", bulk), func(t *testing.T) {
			f := newLocalLifecycleFixture(t)
			b7, p7 := f.addDelivery(t, 7, foundationID)
			b7b, p7b := f.addDelivery(t, 7, foundationOtherID)
			b70, p70 := f.addDelivery(t, 70, foundationID)
			legacy := filepath.Join(t.TempDir(), "old-unsuffixed-worktree")
			f.refs["iro/issue-7"] = foundationHEAD
			f.addWorktree(t, legacy, "iro/issue-7")
			f.refs["iro/issue-7-arbitrary-suffix"] = foundationHEAD
			f.refs["iro/issue-70"] = foundationHEAD
			f.refs["iro/issue-07-old"] = foundationHEAD
			f.refs["iro/topic"] = foundationHEAD
			detached7 := f.runtimePath(unmanagedWorkspace, "run-issue-7-123")
			f.addWorktree(t, detached7, "")
			managedDetached7 := f.runtimePath(managedWorkspace, deliveryLeaf(7, deliveryID(strings.Repeat("a", 32))))
			f.addWorktree(t, managedDetached7, "")
			unknownIssue := f.runtimePath(unmanagedWorkspace, "review-pr-7-123")
			f.addWorktree(t, unknownIssue, "")
			human := filepath.Join(t.TempDir(), "ordinary detached")
			f.addWorktree(t, human, "")
			orphan := f.runtimePath(unmanagedWorkspace, "run-issue-7-999")
			if err := os.MkdirAll(orphan, 0700); err != nil {
				t.Fatal(err)
			}
			var output strings.Builder
			args := []string{"cleanup", "7"}
			if bulk {
				args = []string{"cleanup"}
			}
			if code := Execute(args, &output, io.Discard, f.service); code != 0 {
				t.Fatalf("cleanup = %d: %s", code, output.String())
			}
			for _, branch := range []string{b7, b7b, "iro/issue-7", "iro/issue-7-arbitrary-suffix"} {
				if _, exists := f.refs[branch]; exists {
					t.Errorf("selected branch remains: %s", branch)
				}
			}
			for _, path := range []string{p7, p7b, legacy, detached7, managedDetached7} {
				if _, err := os.Lstat(path); !os.IsNotExist(err) {
					t.Errorf("selected path remains: %s (%v)", path, err)
				}
			}
			for _, branch := range []string{b70, "iro/issue-70", "iro/issue-07-old", "iro/topic"} {
				_, exists := f.refs[branch]
				if exists == bulk {
					t.Errorf("wrong selection for %s; bulk=%t", branch, bulk)
				}
			}
			for _, path := range []string{p70, unknownIssue} {
				_, err := os.Lstat(path)
				if os.IsNotExist(err) != bulk {
					t.Errorf("wrong selection for %s; bulk=%t", path, bulk)
				}
			}
			for _, path := range []string{human, orphan} {
				if _, err := os.Lstat(path); err != nil {
					t.Errorf("unselected path changed: %s (%v)", path, err)
				}
			}
		})
	}
}

func TestCleanupDirtyUntrackedIgnoredAndUnpushedStateDoesNotGate(t *testing.T) {
	f := newLocalLifecycleFixture(t)
	branch, path := f.addDelivery(t, 7, foundationID)
	f.refs[branch] = strings.Repeat("f", 40) // No ancestry or remote publication evidence.
	for _, name := range []string{"tracked-change", "untracked", ".ignored"} {
		if err := os.WriteFile(filepath.Join(path, name), []byte("disposable"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.service.Cleanup(7, io.Discard); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("dirty worktree remains: %v", err)
	}
}

func TestCleanupIndependentActionsContinueAfterWorktreeFailure(t *testing.T) {
	f := newLocalLifecycleFixture(t)
	branch, path := f.addDelivery(t, 7, foundationID)
	otherBranch, otherPath := f.addDelivery(t, 8, foundationID)
	f.worktreeFail[path] = true
	var output strings.Builder
	if err := f.service.CleanupAll(&output); err == nil {
		t.Fatal("Git refusal reported success")
	}
	if _, exists := f.refs[branch]; exists {
		t.Fatal("branch deletion not attempted after worktree failure")
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("known residue not removed: %v", err)
	}
	if _, exists := f.refs[otherBranch]; exists {
		t.Fatal("independent branch remains")
	}
	if _, err := os.Lstat(otherPath); !os.IsNotExist(err) {
		t.Fatalf("independent worktree remains: %v", err)
	}
	if len(f.fs.removed) != 1 || f.fs.removed[0] != path {
		t.Fatalf("filesystem removals escaped exact candidate: %v", f.fs.removed)
	}
	if !strings.Contains(output.String(), "remains registered") || !strings.Contains(output.String(), "Confirmed absent: branch "+otherBranch) {
		t.Fatalf("partial deletion not explained: %s", output.String())
	}
}

func TestCleanupKnownResidueOnly(t *testing.T) {
	f := newLocalLifecycleFixture(t)
	_, runtime := f.addDelivery(t, 7, foundationID)
	f.leaveResidue[runtime] = true
	ordinary := filepath.Join(t.TempDir(), "issue-7-resembling-runtime")
	f.refs["iro/issue-7-human-path"] = foundationHEAD
	f.addWorktree(t, ordinary, "iro/issue-7-human-path")
	f.leaveResidue[ordinary] = true
	var output strings.Builder
	if err := f.service.Cleanup(7, &output); err == nil {
		t.Fatal("remaining ordinary path reported success")
	}
	if len(f.fs.removed) != 1 || f.fs.removed[0] != runtime {
		t.Fatalf("arbitrary filesystem deletion: %v", f.fs.removed)
	}
	if _, err := os.Lstat(ordinary); err != nil {
		t.Fatal("unverified ordinary path was removed")
	}
	if !strings.Contains(output.String(), "known path "+strconv.Quote(ordinary)+" remains") {
		t.Fatalf("missing path diagnostic: %s", output.String())
	}
}

func TestCleanupRemainingAndUnknownPostStateNeverSucceeds(t *testing.T) {
	for _, state := range []string{"branch_remaining", "registration_remaining", "filesystem_failure", "filesystem_unknown", "refs", "worktrees", "common_changed", "branch_failure"} {
		t.Run(state, func(t *testing.T) {
			f := newLocalLifecycleFixture(t)
			branch, path := f.addDelivery(t, 7, foundationID)
			switch state {
			case "branch_remaining":
				f.keepBranch[branch] = true
			case "registration_remaining":
				f.keepRegistered[path] = true
			case "filesystem_failure":
				f.leaveResidue[path] = true
				f.fs.removeFailure[path] = true
			case "filesystem_unknown":
				f.fs.statFailure[path] = true
			case "branch_failure":
				f.branchFail[branch] = true
			default:
				f.postFailure = state
			}
			var output strings.Builder
			if err := f.service.Cleanup(7, &output); err == nil {
				t.Fatalf("unresolved state succeeded: %s", output.String())
			}
			if strings.Contains(output.String(), "Confirmed absent:") || !strings.Contains(output.String(), "Remaining / unknown:") {
				t.Fatalf("unresolved state reported absent: %s", output.String())
			}
		})
	}
}

func TestCleanupInvocationTargetAndLockedStateAreMechanismFailures(t *testing.T) {
	for _, state := range []string{"invoking", "locked", "prunable"} {
		t.Run(state, func(t *testing.T) {
			f := newLocalLifecycleFixture(t)
			branch, path := f.addDelivery(t, 7, foundationID)
			if state == "invoking" {
				f.root = path
			}
			wt := f.worktrees[path]
			wt.Locked = state == "locked"
			wt.Prunable = state == "prunable"
			f.worktrees[path] = wt
			f.worktreeFail[path] = true
			f.branchFail[branch] = true
			if err := f.service.Cleanup(7, io.Discard); err == nil {
				t.Fatal("mechanism failures succeeded")
			}
			if !f.mutated {
				t.Fatal("semantic gate prevented mechanism attempts")
			}
			if len(f.fs.removed) != 1 || f.fs.removed[0] != path {
				t.Fatal("known residue action was skipped")
			}
		})
	}
}

func TestCleanupAbsentBranchWithRegisteredWorktree(t *testing.T) {
	f := newLocalLifecycleFixture(t)
	_, path := f.addDelivery(t, 7, foundationID)
	f.refs = map[string]string{}
	if err := f.service.Cleanup(7, io.Discard); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("registered worktree remains")
	}
}

func TestCleanupKnownCandidateWithSymlinkDataRoot(t *testing.T) {
	f := newLocalLifecycleFixture(t)
	useSymlinkDataRoot(t, f.service)
	_, producerPath := f.addDelivery(t, 7, foundationID)
	registeredPath, err := filepath.EvalSymlinks(producerPath)
	if err != nil {
		t.Fatal(err)
	}
	worktree := f.worktrees[producerPath]
	worktree.Path = registeredPath
	delete(f.worktrees, producerPath)
	f.worktrees[registeredPath] = worktree
	f.leaveResidue[registeredPath] = true
	if err := f.service.Cleanup(7, io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(f.fs.removed) != 1 || f.fs.removed[0] != registeredPath {
		t.Fatalf("removal changed Git's exact registered path: %v", f.fs.removed)
	}
	if _, err := os.Lstat(f.service.Dirs.DataRoot); err != nil {
		t.Fatal("ancestor symlink was removed")
	}
}

func TestCleanupPostStateIncludesAlternateRegisteredSpelling(t *testing.T) {
	f := newLocalLifecycleFixture(t)
	useSymlinkDataRoot(t, f.service)
	_, candidate := f.addDelivery(t, 7, foundationID)
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		t.Fatal(err)
	}
	f.runner.fn = func(spec CommandSpec) CommandResult {
		result := f.run(spec)
		if containsArgs(spec.Args, "worktree", "remove") {
			// Model a remaining registration using Git's realpath spelling.
			f.worktrees[resolved] = registeredWorktree{Path: resolved, HEAD: foundationHEAD, Detached: true}
		}
		return result
	}
	var output strings.Builder
	if err := f.service.Cleanup(7, &output); err == nil {
		t.Fatal("alternate registration reported success")
	}
	if !strings.Contains(output.String(), "remains registered at "+strconv.Quote(resolved)) {
		t.Fatalf("alternate registration not reported: %s", output.String())
	}
}

func TestCleanupDanglingKnownPathIsNotAbsent(t *testing.T) {
	f := newLocalLifecycleFixture(t)
	_, candidate := f.addDelivery(t, 7, foundationID)
	f.runner.fn = func(spec CommandSpec) CommandResult {
		result := f.run(spec)
		if containsArgs(spec.Args, "worktree", "remove") {
			if err := os.Symlink(filepath.Join(t.TempDir(), "absent"), candidate); err != nil {
				t.Fatal(err)
			}
		}
		return result
	}
	var output strings.Builder
	if err := f.service.Cleanup(7, &output); err == nil {
		t.Fatal("dangling known path reported success")
	}
	if len(f.fs.removed) != 0 || strings.Contains(output.String(), "Confirmed absent:") {
		t.Fatalf("unverified dangling path was removed or ignored: %s", output.String())
	}
}

func TestCleanupEmptySelectionAndUsage(t *testing.T) {
	for _, args := range [][]string{{"cleanup"}, {"cleanup", "7"}} {
		f := newLocalLifecycleFixture(t)
		if code := Execute(args, io.Discard, io.Discard, f.service); code != 0 || f.mutated {
			t.Fatalf("empty cleanup code=%d mutated=%t", code, f.mutated)
		}
	}
	runner := &fakeCommandRunner{}
	service := NewService(runner, NewOSFileSystem())
	for _, args := range [][]string{{"cleanup", "1", "2"}, {"cleanup", "--all"}, {"cleanup", "--force"}, {"cleanup", "0"}, {"cleanup", "12x"}} {
		if code := Execute(args, io.Discard, io.Discard, service); code != 2 {
			t.Fatalf("Execute(%v) = %d", args, code)
		}
	}
	if len(runner.calls) != 0 {
		t.Fatal("invalid usage executed Git")
	}
}
