package iro

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func useSymlinkDataRoot(t *testing.T, service *Service) {
	t.Helper()
	root := t.TempDir()
	realData := filepath.Join(root, "real-data")
	if err := os.Mkdir(realData, 0700); err != nil {
		t.Fatal(err)
	}
	service.Dirs.DataRoot = filepath.Join(root, "data-link")
	if err := os.Symlink("real-data", service.Dirs.DataRoot); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceRecognitionWithMissingSymlinkedPaths(t *testing.T) {
	for _, operation := range []string{"managed", "run", "review", "revise"} {
		for _, missing := range []string{"none", "leaf", "parent", "namespace"} {
			t.Run(operation+"/"+missing, func(t *testing.T) {
				service, _, _, root, _, _ := foundationService(t)
				useSymlinkDataRoot(t, service)
				identity := RepositoryIdentity{Owner: "acme", Name: "iro"}
				workspace := deliveryWorktreePath(service.Dirs, identity, 89, foundationID)
				kind := managedWorkspace
				if operation == "managed" {
					allocation, err := service.allocateDeliveryWithGenerator(root, identity, 89, func() (deliveryID, error) { return foundationID, nil })
					if err != nil {
						t.Fatal(err)
					}
					if err := service.createDeliveryWorktree(root, allocation, foundationHEAD); err != nil {
						t.Fatal(err)
					}
				} else {
					var err error
					workspace, err = service.createDetachedWorktree(root, identity, detachedWorkspacePattern(operation, 89), foundationHEAD)
					if err != nil {
						t.Fatal(err)
					}
					kind = unmanagedWorkspace
				}
				registered, err := filepath.EvalSymlinks(workspace)
				if err != nil {
					t.Fatal(err)
				}
				removed := workspace
				if missing == "parent" || missing == "namespace" {
					removed = filepath.Dir(removed)
				}
				if missing == "namespace" {
					removed = filepath.Dir(removed)
				}
				if missing != "none" {
					if err := os.RemoveAll(removed); err != nil {
						t.Fatal(err)
					}
				}
				for _, path := range []string{workspace, registered} {
					if got, ok, err := service.recognizeRuntimeWorkspace(path); err != nil || !ok || got != kind {
						t.Fatalf("producer/registered path not recognized: %s (%s, %t, %v)", path, got, ok, err)
					}
				}
			})
		}
	}
}

func TestWorkspacePathResolutionFailsClosed(t *testing.T) {
	for _, failure := range []string{"permission", "dangling_ancestor", "symlink_loop", "non_directory", "unreadable_registration"} {
		t.Run(failure, func(t *testing.T) {
			service, runner, fs, root, _, worktrees := foundationService(t)
			identity := RepositoryIdentity{Owner: "acme", Name: "iro"}
			workspace := deliveryWorktreePath(service.Dirs, identity, 89, foundationID)
			switch failure {
			case "permission":
				fs.failure = "resolution"
			case "dangling_ancestor", "symlink_loop", "non_directory":
				target := filepath.Join(root, "absent")
				if failure == "symlink_loop" {
					target = service.Dirs.DataRoot
				} else if failure == "non_directory" {
					if err := os.WriteFile(target, []byte("Human file"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Symlink(target, service.Dirs.DataRoot); err != nil {
					t.Fatal(err)
				}
			case "unreadable_registration":
				workspace = filepath.Join(root, "broken-link")
				if err := os.Symlink(filepath.Join(root, "absent"), workspace); err != nil {
					t.Fatal(err)
				}
				*worktrees += worktreeRecord(workspace, "detached")
			}
			if kind, ok, err := service.recognizeRuntimeWorkspace(workspace); err == nil || ok || kind != "" {
				t.Fatalf("unreadable location recognized: %s, %t, %v", kind, ok, err)
			}
			calls := 0
			allocation, err := service.allocateDeliveryWithGenerator(root, identity, 89, func() (deliveryID, error) {
				calls++
				return foundationID, nil
			})
			if err == nil || allocation != nil || calls != 1 || fs.mutations != 0 {
				t.Fatalf("observation failure retried or mutated: %+v, %v; calls=%d mutations=%d", allocation, err, calls, fs.mutations)
			}
			commandCount := len(runner.calls)
			if err := service.removeUnmanagedWorktree(root, workspace); err == nil || len(runner.calls) != commandCount {
				t.Fatalf("unreadable location reached Git cleanup: %v", err)
			}
		})
	}
}

func TestDeliveryNamingAndWorkspaceRecognitionFixtures(t *testing.T) {
	service, runner, _, root, _, _ := foundationService(t)
	identity := RepositoryIdentity{Owner: "acme", Name: "iro"}
	leaf := "issue-89-00112233445566778899aabbccddeeff"
	if got := deliveryBranch(89, foundationID); got != "iro/"+leaf {
		t.Fatalf("branch fixture = %q", got)
	}
	path := deliveryWorktreePath(service.Dirs, identity, 89, foundationID)
	if path != filepath.Join(service.Dirs.DataRoot, "workspaces", identity.Key(), leaf) {
		t.Fatalf("managed path fixture = %q", path)
	}
	if number, id, err := parseDeliveryLeaf(leaf); err != nil || number != 89 || id != foundationID {
		t.Fatalf("parsed fixture = %d, %s, %v", number, id, err)
	}
	if number, id, err := parseDeliveryBranch("iro/" + leaf); err != nil || number != 89 || id != foundationID {
		t.Fatalf("parsed branch fixture = %d, %s, %v", number, id, err)
	}
	for _, branch := range []string{leaf, "refs/heads/iro/" + leaf, "iro/issue-89", "iro/" + leaf + "/child"} {
		if _, _, err := parseDeliveryBranch(branch); err == nil {
			t.Fatalf("invalid delivery branch accepted: %s", branch)
		}
	}
	if kind, ok, err := service.recognizeRuntimeWorkspace(path); err != nil || !ok || kind != managedWorkspace {
		t.Fatalf("managed producer path unrecognized: %q, %t", kind, ok)
	}

	// Exercise real filesystem reservations from every detached producer pattern
	// against the exact same predicate used by the removal consumer.
	for _, operation := range []string{"run", "review", "revise"} {
		pattern := detachedWorkspacePattern(operation, 89)
		wantStem := operation + "-pr-89-"
		if operation == "run" {
			wantStem = "run-issue-89-"
		}
		if pattern != wantStem+"*" {
			t.Fatalf("detached pattern fixture = %q", pattern)
		}
		workspace, err := service.createDetachedWorktree(root, identity, pattern, foundationHEAD)
		if err != nil {
			t.Fatal(err)
		}
		if kind, ok, err := service.recognizeRuntimeWorkspace(workspace); err != nil || !ok || kind != unmanagedWorkspace {
			t.Fatalf("detached producer path unrecognized: %s (%q, %t)", workspace, kind, ok)
		}
		if filepath.Dir(workspace) != runtimeWorkspaceParent(service.Dirs, identity, unmanagedWorkspace) || !strings.HasPrefix(filepath.Base(workspace), wantStem) {
			t.Fatalf("detached producer path fixture = %q", workspace)
		}
	}
	if len(runner.calls) != 3 {
		t.Fatalf("unexpected detached creation commands: %+v", runner.calls)
	}
}

func TestWorkspaceRecognitionRejectsHumanAndMalformedPaths(t *testing.T) {
	dirs := RuntimeDirs{DataRoot: t.TempDir()}
	service := &Service{Dirs: dirs, FileSystem: NewOSFileSystem()}
	managed := filepath.Join(dirs.DataRoot, string(managedWorkspace), "repo-key")
	unmanaged := filepath.Join(dirs.DataRoot, string(unmanagedWorkspace), "repo-key")
	for _, path := range []string{
		filepath.Join(t.TempDir(), "human-detached"),
		filepath.Join(managed, "issue-89"),
		filepath.Join(managed, "issue-089-"+string(foundationID)),
		filepath.Join(managed, "issue-0-"+string(foundationID)),
		filepath.Join(managed, "issue-89-"+strings.ToUpper(string(foundationID))),
		filepath.Join(managed, deliveryLeaf(89, foundationID), "nested"),
		filepath.Join(dirs.DataRoot+"-other", string(managedWorkspace), "repo-key", deliveryLeaf(89, foundationID)),
		filepath.Join(unmanaged, "run-pr-89-123"), filepath.Join(unmanaged, "review-issue-89-123"),
		filepath.Join(unmanaged, "revise-pr-089-123"), filepath.Join(unmanaged, "run-issue-89-"),
		filepath.Join(unmanaged, "run-issue-89-*"), filepath.Join(unmanaged, "human-pr-89-123"),
		"workspaces/repo-key/" + deliveryLeaf(89, foundationID),
	} {
		if kind, ok, err := service.recognizeRuntimeWorkspace(path); err != nil || ok {
			t.Fatalf("ordinary/malformed path recognized: %q (%s)", path, kind)
		}
	}
}

func TestUnmanagedRemovalUsesWorkspaceRecognition(t *testing.T) {
	service, runner, _, root, _, _ := foundationService(t)
	for _, path := range []string{filepath.Join(root, "human-detached"), deliveryWorktreePath(service.Dirs, RepositoryIdentity{Owner: "acme", Name: "iro"}, 89, foundationID)} {
		if err := service.removeUnmanagedWorktree(root, path); err == nil {
			t.Fatalf("unmanaged removal accepted non-unmanaged path: %s", path)
		}
	}
	if len(runner.calls) != 0 {
		t.Fatalf("Git removal attempted on unrecognized path: %+v", runner.calls)
	}
}
