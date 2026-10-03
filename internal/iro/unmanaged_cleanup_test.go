package iro

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Model the new read-only inventory commands in existing operation fixtures.
func unmanagedInventoryResult(spec CommandSpec, root, workspace, head string, registered bool) (CommandResult, bool) {
	if spec.Name != "git" {
		return CommandResult{}, false
	}
	switch strings.Join(spec.Args, " ") {
	case "rev-parse --path-format=absolute --git-common-dir":
		return CommandResult{Stdout: filepath.Join(root, ".git") + "\n"}, true
	case "for-each-ref --sort=refname --format=%(refname)%00%(objectname) refs/heads/iro/":
		return CommandResult{}, true
	case "worktree list --porcelain -z":
		output := worktreeRecord(root, "branch refs/heads/main")
		if registered {
			output += "worktree " + workspace + "\x00HEAD " + head + "\x00detached\x00\x00"
		}
		return CommandResult{Stdout: output}, true
	}
	return CommandResult{}, false
}

func TestUnmanagedRemovalValidatesLocalRegistrationBeforeMutation(t *testing.T) {
	for _, state := range []string{
		"valid", "attached", "unregistered", "duplicate", "bare", "locked", "prunable",
		"malformed", "inventory_failure", "common_warning", "foreign_common", "changed_common",
		"missing_path", "symlink", "file", "unreadable_head", "changed_head", "attached_head", "dirty", "unreadable_status",
	} {
		t.Run(state, func(t *testing.T) {
			root := t.TempDir()
			runner := &fakeCommandRunner{}
			service := newTestService(t, runner, root)
			workspace := filepath.Join(runtimeWorkspaceParent(service.Dirs, RepositoryIdentity{Owner: "acme", Name: "iro"}, unmanagedWorkspace), "review-pr-42-123")
			if err := os.MkdirAll(workspace, 0700); err != nil {
				t.Fatal(err)
			}
			if state == "missing_path" || state == "symlink" || state == "file" {
				if err := os.Remove(workspace); err != nil {
					t.Fatal(err)
				}
				if state == "symlink" {
					if err := os.Symlink(root, workspace); err != nil {
						t.Fatal(err)
					}
				} else if state == "file" {
					if err := os.WriteFile(workspace, []byte("Human file"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			removed, commonReads := false, 0
			runner.fn = func(spec CommandSpec) CommandResult {
				if spec.Name != "git" || spec.Dir != root && spec.Dir != workspace {
					t.Fatalf("command escaped local repository: %+v", spec)
				}
				args := strings.Join(spec.Args, " ")
				switch args {
				case "rev-parse --path-format=absolute --git-common-dir":
					commonReads++
					if state == "common_warning" {
						return CommandResult{Stdout: filepath.Join(root, ".git"), Stderr: "warning: unreliable observation"}
					}
					if state == "foreign_common" && spec.Dir == workspace || state == "changed_common" && commonReads == 2 {
						return CommandResult{Stdout: filepath.Join(root, "other.git")}
					}
				case "worktree list --porcelain -z":
					mode := "detached"
					switch state {
					case "attached":
						mode = "branch refs/heads/human"
					case "locked", "prunable":
						mode += "\x00" + state + " Human reason"
					case "bare":
						return CommandResult{Stdout: "worktree " + workspace + "\x00bare\x00\x00"}
					case "malformed":
						return CommandResult{Stdout: "unreadable"}
					case "inventory_failure":
						return CommandResult{ExitCode: 1, Stderr: "unreadable"}
					}
					output := worktreeRecord(root, "branch refs/heads/main")
					if state != "unregistered" && !removed {
						output += worktreeRecord(workspace, mode)
						if state == "duplicate" {
							output += worktreeRecord(workspace, mode)
						}
					}
					return CommandResult{Stdout: output}
				case "symbolic-ref --quiet HEAD":
					if state == "attached_head" {
						return CommandResult{Stdout: "refs/heads/human"}
					}
					return CommandResult{ExitCode: 1}
				case "rev-parse HEAD":
					if state == "unreadable_head" {
						return CommandResult{ExitCode: 1}
					}
					if state == "changed_head" {
						return CommandResult{Stdout: strings.Repeat("f", 40)}
					}
					return CommandResult{Stdout: foundationHEAD}
				case "status --porcelain --untracked-files=all":
					if state == "dirty" {
						return CommandResult{Stdout: "?? Human.txt\n"}
					}
					if state == "unreadable_status" {
						return CommandResult{ExitCode: 1}
					}
					return CommandResult{}
				case "worktree remove -- " + workspace:
					removed = true
					if err := os.Remove(workspace); err != nil {
						t.Fatal(err)
					}
					return CommandResult{}
				case "worktree list --porcelain":
					return CommandResult{Stdout: "worktree " + root + "\nbranch refs/heads/main\n\n"}
				}
				if result, ok := unmanagedInventoryResult(spec, root, workspace, foundationHEAD, !removed); ok {
					return result
				}
				t.Fatalf("unexpected command: %+v", spec)
				return CommandResult{ExitCode: 1}
			}
			err := service.removeUnmanagedWorktree(root, workspace)
			if state == "valid" {
				if err != nil || !removed {
					t.Fatalf("valid cleanup = %v; removed=%t", err, removed)
				}
			} else {
				if err == nil || removed {
					t.Fatalf("unsafe cleanup = %v; removed=%t", err, removed)
				}
				if state != "missing_path" {
					if _, err := os.Lstat(workspace); err != nil {
						t.Fatalf("rejected path was changed: %v", err)
					}
				}
			}
		})
	}
}
