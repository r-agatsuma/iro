package iro

import (
	"path/filepath"
	"strings"
	"testing"
)

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
	if kind, ok := recognizeRuntimeWorkspace(service.Dirs, path); !ok || kind != managedWorkspace {
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
		if kind, ok := recognizeRuntimeWorkspace(service.Dirs, workspace); !ok || kind != unmanagedWorkspace {
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
		if kind, ok := recognizeRuntimeWorkspace(dirs, path); ok {
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
