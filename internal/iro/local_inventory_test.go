package iro

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const foundationID = deliveryID("00112233445566778899aabbccddeeff")
const foundationOtherID = deliveryID("ffeeddccbbaa99887766554433221100")
const foundationHEAD = "0123456789abcdef0123456789abcdef01234567"

func worktreeRecord(path, mode string) string {
	return "worktree " + path + "\x00HEAD " + foundationHEAD + "\x00" + mode + "\x00\x00"
}

func foundationRunner(root, common string, refs, worktrees *string) *fakeCommandRunner {
	return &fakeCommandRunner{fn: func(spec CommandSpec) CommandResult {
		if spec.Name != "git" || spec.Dir != root {
			return CommandResult{ExitCode: 1, Stderr: "unexpected command or repository"}
		}
		switch spec.Args[0] {
		case "rev-parse":
			return CommandResult{Stdout: common + "\n"}
		case "for-each-ref":
			return CommandResult{Stdout: *refs}
		case "worktree":
			if spec.Args[1] == "list" {
				return CommandResult{Stdout: *worktrees}
			}
			return CommandResult{}
		case "show-ref":
			return CommandResult{ExitCode: 1}
		}
		return CommandResult{ExitCode: 1, Stderr: "unexpected command"}
	}}
}

func TestLocalInventoryFixtures(t *testing.T) {
	root := t.TempDir()
	common := filepath.Join(root, ".git")
	branch := "refs/heads/" + deliveryBranch(89, foundationID)
	otherBranch := "refs/heads/" + deliveryBranch(89, foundationOtherID)
	attached := filepath.Join(root, "attached worktree")
	detached := filepath.Join(root, "human\tworktree\npath")
	for _, tc := range []struct {
		name      string
		refs      string
		worktrees string
		refCount  int
		wtCount   int
	}{
		{"branch_only", branch + "\x00" + foundationHEAD + "\n", worktreeRecord(root, "branch refs/heads/main"), 1, 1},
		{"attached", branch + "\x00" + foundationHEAD + "\n", worktreeRecord(attached, "branch "+branch), 1, 1},
		{"detached", "", worktreeRecord(detached, "detached"), 0, 1},
		{"multiple", otherBranch + "\x00" + foundationHEAD + "\n" + branch + "\x00" + foundationHEAD + "\n", worktreeRecord(detached, "detached") + worktreeRecord(root, "branch refs/heads/main") + worktreeRecord(attached, "branch "+branch), 2, 3},
		{"bare", "", "worktree " + root + "\x00bare\x00\x00" + worktreeRecord(attached, "branch "+branch), 0, 2},
		{"locked_prunable", "", "worktree " + detached + "\x00HEAD " + foundationHEAD + "\x00detached\x00locked Human reason\x00prunable missing directory\x00\x00", 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := foundationRunner(root, common, &tc.refs, &tc.worktrees)
			service := newTestService(t, runner, root)
			inventory, err := service.localInventory(root)
			if err != nil || inventory.CommonDir != common || len(inventory.Refs) != tc.refCount || len(inventory.Worktrees) != tc.wtCount {
				t.Fatalf("inventory = %+v, %v", inventory, err)
			}
			for i, entry := range inventory.Worktrees {
				if i > 0 && inventory.Worktrees[i-1].Path > entry.Path {
					t.Fatal("worktrees are not deterministic")
				}
				if entry.Path == detached && (!entry.Detached || entry.Branch != "" || entry.HEAD != foundationHEAD) {
					t.Fatalf("detached path/HEAD lost: %+v", entry)
				}
				if _, runtime, err := service.recognizeRuntimeWorkspace(entry.Path); err != nil || runtime {
					t.Fatalf("Human worktree classified as runtime: %+v", entry)
				}
			}
			for i, entry := range inventory.Refs {
				if entry.HEAD != foundationHEAD || i > 0 && inventory.Refs[i-1].Name > entry.Name {
					t.Fatalf("invalid/scrambled refs: %+v", inventory.Refs)
				}
			}
			wantArgs := [][]string{
				{"rev-parse", "--path-format=absolute", "--git-common-dir"},
				{"for-each-ref", "--sort=refname", "--format=%(refname)%00%(objectname)", "refs/heads/iro/"},
				{"worktree", "list", "--porcelain", "-z"},
				{"rev-parse", "--path-format=absolute", "--git-common-dir"},
			}
			for i, call := range runner.calls {
				if call.Dir != root || !reflect.DeepEqual(call.Args, wantArgs[i]) {
					t.Fatalf("inventory command escaped repository scope: %+v", call)
				}
			}
		})
	}
}

func TestMalformedInventoryObservations(t *testing.T) {
	branch := "refs/heads/iro/topic"
	validRef := branch + "\x00" + foundationHEAD + "\n"
	for _, value := range []string{
		branch + "\n", branch + "\x00unreadable\n", "refs/remotes/iro/topic\x00" + foundationHEAD + "\n",
		"refs/heads/iro/.hidden\x00" + foundationHEAD + "\n", validRef + validRef, validRef + "\n",
	} {
		if refs, err := parseLocalIroRefs(value); err == nil || refs != nil {
			t.Fatalf("malformed refs accepted: %q => %+v, %v", value, refs, err)
		}
	}
	path := filepath.Join(t.TempDir(), "registered")
	valid := worktreeRecord(path, "detached")
	for _, value := range []string{
		"", strings.TrimSuffix(valid, "\x00"), "HEAD " + foundationHEAD + "\x00\x00",
		worktreeRecord("relative", "detached"), strings.Replace(valid, foundationHEAD, "unreadable", 1),
		worktreeRecord(path, "branch refs/remotes/origin/main"),
		worktreeRecord(path, "branch refs/heads/main\x00detached"), worktreeRecord(path, "detached\x00bare"),
		strings.Replace(valid, "detached", "HEAD "+foundationHEAD+"\x00detached", 1), valid + valid,
		strings.Replace(valid, "detached", "unknown", 1), strings.Replace(valid, "detached", "detached true", 1),
	} {
		if worktrees, err := parseRegisteredWorktrees(value); err == nil || worktrees != nil {
			t.Fatalf("malformed worktrees accepted: %q => %+v, %v", value, worktrees, err)
		}
	}
}

func TestLocalInventoryRejectsUnreadableAndChangingRepository(t *testing.T) {
	root := t.TempDir()
	common := filepath.Join(root, ".git")
	refs, worktrees := "", worktreeRecord(root, "branch refs/heads/main")
	for _, failure := range []string{"common", "relative_common", "initial_common_warning", "final_common_warning", "refs", "broken_ref_warning", "worktrees", "common_changed"} {
		t.Run(failure, func(t *testing.T) {
			runner := foundationRunner(root, common, &refs, &worktrees)
			original := runner.fn
			commonReads := 0
			runner.fn = func(spec CommandSpec) CommandResult {
				if spec.Args[0] == "rev-parse" {
					commonReads++
				}
				switch {
				case failure == "common" && spec.Args[0] == "rev-parse", failure == "refs" && spec.Args[0] == "for-each-ref", failure == "worktrees" && spec.Args[0] == "worktree":
					return CommandResult{ExitCode: 1, Err: errors.New("unreadable"), Stderr: "unreadable"}
				case failure == "relative_common" && spec.Args[0] == "rev-parse":
					return CommandResult{Stdout: ".git\n"}
				case failure == "initial_common_warning" && commonReads == 1, failure == "final_common_warning" && commonReads == 2:
					return CommandResult{Stdout: common + "\n", Stderr: "warning: common directory observation is unreliable"}
				case failure == "broken_ref_warning" && spec.Args[0] == "for-each-ref":
					return CommandResult{Stderr: "warning: ignoring broken ref"}
				case failure == "common_changed" && commonReads == 2:
					return CommandResult{Stdout: common + "-other\n"}
				}
				return original(spec)
			}
			service := newTestService(t, runner, root)
			inventory, err := service.localInventory(root)
			if err == nil || inventory.CommonDir != "" || inventory.Refs != nil || inventory.Worktrees != nil {
				t.Fatalf("unsafe partial inventory: %+v, %v", inventory, err)
			}
		})
	}
}

func TestLocalInventoryReadOnlyGit(t *testing.T) {
	service := NewService(NewOSCommandRunner(), NewOSFileSystem())
	if err := service.requireGit(); err != nil {
		t.Skip(err)
	}
	root, err := service.gitRoot()
	if err != nil {
		t.Skip("source checkout is not available for read-only Git integration")
	}
	inventory, err := service.localInventory(root)
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(inventory.CommonDir) || len(inventory.Worktrees) == 0 {
		t.Fatalf("incomplete actual Git inventory: %+v", inventory)
	}
	found := false
	for _, worktree := range inventory.Worktrees {
		if filepath.Clean(worktree.Path) == root {
			found = true
		}
	}
	if !found {
		t.Fatalf("invoking checkout is missing from inventory: %+v", inventory)
	}
}
