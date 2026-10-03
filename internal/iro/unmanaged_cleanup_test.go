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
		if registered && workspace != "" {
			output += "worktree " + workspace + "\x00HEAD " + head + "\x00detached\x00\x00"
		}
		return CommandResult{Stdout: output}, true
	}
	return CommandResult{}, false
}

type unreadableRegisteredPathFS struct {
	FileSystem
	path string
}

func (fs unreadableRegisteredPathFS) Stat(path string) (os.FileInfo, error) {
	if path == fs.path {
		return nil, os.ErrPermission
	}
	return fs.FileSystem.Stat(path)
}

func TestUnmanagedRemovalValidatesLocalRegistrationBeforeMutation(t *testing.T) {
	for _, state := range []string{
		"valid", "attached", "unregistered", "duplicate", "bare", "locked", "prunable",
		"malformed", "inventory_failure", "common_warning", "foreign_common", "changed_common",
		"missing_path", "symlink", "file", "unreadable_head", "changed_head", "attached_head", "dirty", "unreadable_status",
		"different_directory", "missing_registered_path", "unreadable_registered_path", "stale_registration",
	} {
		t.Run(state, func(t *testing.T) {
			root := t.TempDir()
			runner := &fakeCommandRunner{}
			service := newTestService(t, runner, root)
			workspace := filepath.Join(runtimeWorkspaceParent(service.Dirs, githubRuntimeNamespace(RepositoryIdentity{Owner: "acme", Name: "iro"}), unmanagedWorkspace), "review-pr-42-123")
			if err := os.MkdirAll(workspace, 0700); err != nil {
				t.Fatal(err)
			}
			registeredPath := workspace
			switch state {
			case "different_directory":
				registeredPath = t.TempDir()
			case "missing_registered_path":
				registeredPath = filepath.Join(root, "absent")
			case "unreadable_registered_path":
				service.FileSystem = unreadableRegisteredPathFS{FileSystem: service.FileSystem, path: workspace}
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
						output += worktreeRecord(registeredPath, mode)
						if state == "duplicate" {
							output += worktreeRecord(workspace, mode)
						}
						if state == "stale_registration" {
							output += worktreeRecord(filepath.Join(root, "absent"), "detached\x00prunable missing directory")
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
			if state == "valid" || state == "stale_registration" {
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

func TestDetachedWorkspaceProducerAndCleanupWithSymlinkAncestor(t *testing.T) {
	for _, operation := range []string{"run", "review", "revise"} {
		for _, state := range []string{"valid", "attached", "duplicate_alias", "registration_remains"} {
			t.Run(operation+"/"+state, func(t *testing.T) {
				root := t.TempDir()
				runner := &fakeCommandRunner{}
				service := newTestService(t, runner, root)
				realData := filepath.Join(root, "real-data")
				if err := os.Mkdir(realData, 0700); err != nil {
					t.Fatal(err)
				}
				service.Dirs.DataRoot = filepath.Join(root, "data-link")
				if err := os.Symlink(realData, service.Dirs.DataRoot); err != nil {
					t.Fatal(err)
				}
				workspace, registeredPath := "", ""
				removed := false
				runner.fn = func(spec CommandSpec) CommandResult {
					if spec.Name != "git" || spec.Dir != root && spec.Dir != workspace {
						t.Fatalf("command escaped local repository: %+v", spec)
					}
					if containsArgs(spec.Args, "worktree", "add") {
						workspace = spec.Args[3]
						// Model Git's realpath registration without mutating Git state.
						var err error
						registeredPath, err = filepath.EvalSymlinks(workspace)
						if err != nil {
							t.Fatal(err)
						}
						return CommandResult{}
					}
					switch strings.Join(spec.Args, " ") {
					case "worktree list --porcelain -z":
						output := worktreeRecord(root, "branch refs/heads/main")
						mode := "detached"
						if state == "attached" {
							mode = "branch refs/heads/human"
						}
						output += worktreeRecord(registeredPath, mode)
						if state == "duplicate_alias" {
							output += worktreeRecord(workspace, mode)
						}
						return CommandResult{Stdout: output}
					case "symbolic-ref --quiet HEAD":
						return CommandResult{ExitCode: 1}
					case "rev-parse HEAD":
						return CommandResult{Stdout: foundationHEAD}
					case "status --porcelain --untracked-files=all":
						return CommandResult{}
					case "worktree remove -- " + workspace:
						removed = true
						if err := os.Remove(workspace); err != nil {
							t.Fatal(err)
						}
						return CommandResult{}
					case "worktree list --porcelain":
						output := "worktree " + root + "\nbranch refs/heads/main\n\n"
						if state == "registration_remains" {
							output += "worktree " + registeredPath + "\ndetached\n\n"
						}
						return CommandResult{Stdout: output}
					}
					if result, ok := unmanagedInventoryResult(spec, root, registeredPath, foundationHEAD, true); ok {
						return result
					}
					t.Fatalf("unexpected command: %+v", spec)
					return CommandResult{ExitCode: 1}
				}
				path, err := service.createDetachedWorktree(root, githubRuntimeNamespace(RepositoryIdentity{Owner: "acme", Name: "iro"}), detachedWorkspacePattern(operation, 89), foundationHEAD)
				if err != nil || path != workspace || workspace == registeredPath {
					t.Fatalf("symlink producer paths = %q, %q, %q; err=%v", path, workspace, registeredPath, err)
				}
				if kind, ok, err := service.recognizeRuntimeWorkspace(workspace); err != nil || !ok || kind != unmanagedWorkspace {
					t.Fatal("producer path was not recognized")
				}
				inventory, err := service.localInventory(root)
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, entry := range inventory.Worktrees {
					found = found || entry.Path == registeredPath
					if entry.Path == registeredPath {
						if kind, ok, err := service.recognizeRuntimeWorkspace(entry.Path); err != nil || !ok || kind != unmanagedWorkspace {
							t.Fatal("exact registered path was not recognized")
						}
					}
				}
				if !found {
					t.Fatal("inventory did not preserve Git's exact registered path")
				}
				err = service.removeUnmanagedWorktree(root, workspace)
				if (err == nil) != (state == "valid") || removed != (state == "valid" || state == "registration_remains") {
					t.Fatalf("cleanup = %v; removed=%t", err, removed)
				}
				if !removed {
					if _, err := os.Stat(workspace); err != nil {
						t.Fatalf("rejected workspace was not retained: %v", err)
					}
				}
			})
		}
	}
}
