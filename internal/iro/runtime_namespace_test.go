package iro

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitHubRuntimeNamespaceCompatibilityFixtures(t *testing.T) {
	// Fixed baseline values: never derive expectations with the encoder under test.
	fixtures := []struct {
		owner, name, key string
	}{
		{"acme", "iro", "acme-iro-5f858f6c7d45"},
		{"ACME", "IRO", "acme-iro-5f858f6c7d45"},
		{"r-agatsuma", "iro", "r-agatsuma-iro-5e77fe1b1354"},
		{"Acme.Tools", "Repo_Name.v2", "acme-tools-repo_name-v2-bd11e9640dbf"},
		{"acme", "a.b", "acme-a-b-2386eb59cb3e"},
		{"acme", "a-b", "acme-a-b-2170c8b743ca"},
	}
	dirs := RuntimeDirs{DataRoot: filepath.Join(t.TempDir(), "data"), StateRoot: filepath.Join(t.TempDir(), "state")}
	for _, fixture := range fixtures {
		t.Run(fixture.owner+"/"+fixture.name, func(t *testing.T) {
			identity := RepositoryIdentity{Owner: fixture.owner, Name: fixture.name}
			namespace := githubRuntimeNamespace(identity)
			if string(namespace) != fixture.key || identity.Key() != fixture.key {
				t.Fatalf("namespace = %q, Key() = %q; want %q", namespace, identity.Key(), fixture.key)
			}
			want := filepath.Join(dirs.DataRoot, "workspaces", fixture.key, "issue-89-00112233445566778899aabbccddeeff")
			if got := deliveryWorktreePath(dirs, namespace, 89, foundationID); got != want {
				t.Fatalf("managed path = %q; want %q", got, want)
			}
			want = filepath.Join(dirs.DataRoot, "unmanaged-workspaces", fixture.key)
			if got := runtimeWorkspaceParent(dirs, namespace, unmanagedWorkspace); got != want {
				t.Fatalf("detached parent = %q; want %q", got, want)
			}
			for _, category := range []string{"runs", "deliveries", "revisions", "unmanaged-runs", "unmanaged-revisions"} {
				want := filepath.Join(dirs.StateRoot, category, fixture.key)
				if got := runtimeLogDir(dirs, namespace, category); got != want {
					t.Fatalf("log directory = %q; want %q", got, want)
				}
			}
		})
	}
}

func TestGitHubLogWriterPathCompatibilityFixtures(t *testing.T) {
	service := newTestService(t, &fakeCommandRunner{}, t.TempDir())
	identity := RepositoryIdentity{Owner: "ACME", Name: "IRO"}
	branch := "iro/issue-89-00112233445566778899aabbccddeeff"
	workspace := filepath.Join(service.Dirs.DataRoot, "unmanaged-workspaces", "acme-iro-5f858f6c7d45", "run-issue-89-00112233445566778899aabbccddeeff")
	allocation := &deliveryAllocation{id: foundationID, issue: 89, branch: branch, worktree: workspace}
	result := CommandResult{Stdout: "Author report"}
	target := reviewPullRequest{Number: 42, OriginIssue: 89, HeadRefName: branch, HeadRefOID: foundationHEAD}
	started := service.Now()
	runPath, err := service.writeRunLog(identity, 89, started, started, branch, workspace, result, "not attempted")
	if err != nil {
		t.Fatal(err)
	}
	var stderr strings.Builder
	if err := service.finishRunDelivery(identity, newRunDeliveryState(allocation), nil, io.Discard, &stderr); err != nil || stderr.Len() != 0 {
		t.Fatalf("delivery diagnostic = %v, %s", err, stderr.String())
	}
	revisePath, err := service.writeReviseLog(identity, target, workspace, started, result)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.writeUnmanagedRunLog(identity, 89, "main", foundationHEAD, workspace, result, allocation); err != nil {
		t.Fatal(err)
	}
	revisionWorkspace := filepath.Join(filepath.Dir(workspace), "revise-pr-42-123456")
	unmanagedRevisePath, err := service.writeUnmanagedReviseLog(identity, target, revisionWorkspace, result)
	if err != nil {
		t.Fatal(err)
	}
	fixtures := []struct {
		category, filename, returned string
	}{
		{"runs", "issue-89-00112233445566778899aabbccddeeff.log", runPath},
		{"deliveries", "issue-89-00112233445566778899aabbccddeeff.log", ""},
		{"revisions", "pr-42-1700000000000000000.log", revisePath},
		{"unmanaged-runs", "run-issue-89-00112233445566778899aabbccddeeff.log", ""},
		{"unmanaged-revisions", "revise-pr-42-123456.log", unmanagedRevisePath},
	}
	for _, fixture := range fixtures {
		want := filepath.Join(service.Dirs.StateRoot, fixture.category, "acme-iro-5f858f6c7d45", fixture.filename)
		if fixture.returned != "" && fixture.returned != want {
			t.Fatalf("%s log path = %q; want %q", fixture.category, fixture.returned, want)
		}
		if data, err := os.ReadFile(want); err != nil || len(data) == 0 {
			t.Fatalf("expected log at %q: %v", want, err)
		}
	}
}

func TestRemoteIdentityIsIndependentOfRuntimeNamespace(t *testing.T) {
	service, runner, _, root, _, _ := foundationService(t)
	// This is mechanism input, not a proposed encoding for any provider.
	namespace := runtimeNamespaceKey("opaque-provider-host-repository-key")
	allocation, err := service.allocateDeliveryWithGenerator(root, namespace, 89, func() (deliveryID, error) { return foundationID, nil })
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(service.Dirs.DataRoot, "workspaces", "opaque-provider-host-repository-key", "issue-89-00112233445566778899aabbccddeeff")
	if allocation.worktree != want {
		t.Fatalf("opaque namespace path = %q; want %q", allocation.worktree, want)
	}
	for _, fixture := range []struct {
		name, url string
		matches   bool
	}{
		{"same_https", "https://github.com/ACME/IRO.git", true},
		{"same_ssh", "git@github.com:acme/iro.git", true},
		{"other_repository", "git@github.com:acme/other.git", false},
		{"other_host", "git@example.com:acme/iro.git", false},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			runner.calls = nil
			runner.fn = func(spec CommandSpec) CommandResult {
				if spec.Name != "git" {
					t.Fatalf("unexpected remote operation: %+v", spec)
				}
				if spec.Args[0] == "remote" {
					return CommandResult{Stdout: fixture.url + "\n"}
				}
				if spec.Args[0] != "ls-remote" {
					t.Fatalf("unexpected Git operation: %+v", spec)
				}
				return CommandResult{}
			}
			identity := RepositoryIdentity{Owner: "acme", Name: "iro"}
			if err := service.verifyPushRemote(root, "origin", identity); (err == nil) != fixture.matches {
				t.Fatalf("managed push validation = %v; matches=%t", err, fixture.matches)
			}
			if !fixture.matches && len(runner.calls) != 1 {
				t.Fatal("remote read preceded destination identity validation")
			}
			if err := service.validateOriginPushDestination(root, identity); (err == nil) != fixture.matches {
				t.Fatalf("unmanaged push validation = %v; matches=%t", err, fixture.matches)
			}
			if allocation.worktree != want || allocation.commonDir != filepath.Join(root, ".git") {
				t.Fatal("remote validation changed local namespace or common-directory binding")
			}
		})
	}
}

func TestOpaqueNamespaceDoesNotReplaceCommonDirectoryMembership(t *testing.T) {
	service, _, fs, root, _, _ := foundationService(t)
	allocation, err := service.allocateDeliveryWithGenerator(root, runtimeNamespaceKey("opaque-provider-key"), 89, func() (deliveryID, error) { return foundationID, nil })
	if err != nil {
		t.Fatal(err)
	}
	allocation.commonDir = filepath.Join(root, "other-common-directory")
	if err := service.beginDeliveryCreation(root, allocation); err == nil || !strings.Contains(err.Error(), "different local Git common directory") {
		t.Fatalf("foreign common directory accepted: %v", err)
	}
	if allocation.fixed || fs.mutations != 0 {
		t.Fatal("foreign common directory reached resource creation")
	}
}
