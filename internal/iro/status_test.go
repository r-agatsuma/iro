package iro

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestLocalLifecycleRejectsIncompleteGitDiscoveryBeforeMutation(t *testing.T) {
	for _, cleanup := range []bool{false, true} {
		for _, failure := range []string{"git", "common", "refs_warning", "worktrees"} {
			t.Run(strconv.FormatBool(cleanup)+"/"+failure, func(t *testing.T) {
				f := newLocalLifecycleFixture(t)
				f.refs[deliveryBranch(7, foundationID)] = foundationHEAD
				if failure == "git" {
					f.runner.lookups["git"] = errors.New("unavailable")
				}
				f.runner.fn = func(spec CommandSpec) CommandResult {
					switch {
					case failure == "common" && spec.Args[0] == "rev-parse":
						return CommandResult{ExitCode: 1, Stderr: "not a repository"}
					case failure == "refs_warning" && spec.Args[0] == "for-each-ref":
						return CommandResult{Stderr: "warning: ignoring broken ref"}
					case failure == "worktrees" && containsArgs(spec.Args, "worktree", "list"):
						return CommandResult{Stdout: "malformed"}
					}
					return f.run(spec)
				}
				var err error
				if cleanup {
					err = f.service.CleanupAll(io.Discard)
				} else {
					err = f.service.Status(io.Discard)
				}
				if err == nil || f.mutated || len(f.fs.removed) != 0 {
					t.Fatalf("incomplete discovery = %v; mutated=%t", err, f.mutated)
				}
			})
		}
	}
}

func TestStatusLocalInventoryFixtures(t *testing.T) {
	for _, state := range []string{"empty", "branch_only", "attached", "multiple_paths", "runtime_detached", "ordinary_detached", "runtime_human_branch", "missing_path"} {
		t.Run(state, func(t *testing.T) {
			f := newLocalLifecycleFixture(t)
			branch := deliveryBranch(7, foundationID)
			path := f.runtimePath(managedWorkspace, deliveryLeaf(7, foundationID))
			want := "No local iro resources."
			switch state {
			case "branch_only":
				f.refs[branch] = foundationHEAD
				want = branch + "\tnone\t" + foundationHEAD
			case "attached", "multiple_paths", "missing_path":
				f.refs[branch] = foundationHEAD
				f.addWorktree(t, path, branch)
				want = branch + "\t" + strconv.Quote(path)
				if state == "multiple_paths" {
					f.addWorktree(t, path+"-other", branch)
					want += ", " + strconv.Quote(path+"-other")
				}
				want += "\t" + foundationHEAD
				if state == "missing_path" {
					if err := os.RemoveAll(path); err != nil {
						t.Fatal(err)
					}
				}
			case "runtime_detached":
				f.addWorktree(t, path, "")
				want = "none\t" + strconv.Quote(path) + "\t" + foundationHEAD
			case "ordinary_detached":
				path = filepath.Join(t.TempDir(), "detached Human\tworktree\npath")
				f.addWorktree(t, path, "")
			case "runtime_human_branch":
				f.addWorktree(t, path, "human")
				want = "human\t" + strconv.Quote(path) + "\t" + foundationHEAD
			}
			var output, stderr strings.Builder
			if code := Execute([]string{"status"}, &output, &stderr, f.service); code != 0 {
				t.Fatalf("status = %d: %s", code, stderr.String())
			}
			if !strings.Contains(output.String(), want) || !strings.Contains(output.String(), strconv.Quote(f.common)) {
				t.Fatalf("missing local inventory row %q: %s", want, output.String())
			}
			for _, forbidden := range []string{"CLEAN", "DIRTY", "BROKEN", "STATE"} {
				if strings.Contains(output.String(), forbidden) {
					t.Fatalf("legacy ownership state returned: %s", output.String())
				}
			}
			if f.mutated || len(f.fs.removed) != 0 {
				t.Fatal("Status changed local state")
			}
		})
	}
}

func TestLocalLifecycleExcludesSeparateCommonDirectory(t *testing.T) {
	for _, cleanup := range []bool{false, true} {
		t.Run(strconv.FormatBool(cleanup), func(t *testing.T) {
			f := newLocalLifecycleFixture(t)
			other := newLocalLifecycleFixture(t)
			// Identical remote-derived grouping does not combine local repositories.
			other.service.Dirs = f.service.Dirs
			branch, foreignPath := other.addDelivery(t, 7, foundationOtherID)
			f.refs[deliveryBranch(7, foundationID)] = foundationHEAD
			var output strings.Builder
			var err error
			if cleanup {
				err = f.service.CleanupAll(&output)
			} else {
				err = f.service.Status(&output)
			}
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(output.String(), branch) || strings.Contains(output.String(), foreignPath) {
				t.Fatalf("foreign local repository included: %s", output.String())
			}
			if _, err := os.Lstat(foreignPath); err != nil {
				t.Fatal("foreign registered worktree changed")
			}
			if len(other.runner.calls) != 0 {
				t.Fatal("foreign common directory consulted")
			}
		})
	}
}

func TestStatusAndCleanupIgnoreProjectFilesMappingsAndLogs(t *testing.T) {
	for _, cleanup := range []bool{false, true} {
		t.Run(strconv.FormatBool(cleanup), func(t *testing.T) {
			f := newLocalLifecycleFixture(t)
			_, path := f.addDelivery(t, 7, foundationID)
			identity := RepositoryIdentity{Owner: "acme", Name: "iro"}
			mapping := ownershipPath(f.service.Dirs, identity, 7)
			log := filepath.Join(f.service.Dirs.StateRoot, "runs", identity.Key(), "issue-7.log")
			for _, file := range []string{filepath.Join(f.root, "iro.toml"), filepath.Join(f.root, "WORKFLOW.md"), mapping, log} {
				if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(file, []byte("invalid and ignored"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			var err error
			if cleanup {
				err = f.service.Cleanup(7, io.Discard)
			} else {
				err = f.service.Status(io.Discard)
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, file := range []string{mapping, log} {
				data, err := os.ReadFile(file)
				if err != nil || string(data) != "invalid and ignored" {
					t.Fatalf("legacy file changed: %s (%v)", file, err)
				}
			}
			if !cleanup {
				if _, err := os.Lstat(path); err != nil {
					t.Fatal("Status removed workspace")
				}
			}
		})
	}
}

func TestLocalLifecycleUnknownDiscoveryDoesNotHideFailure(t *testing.T) {
	for _, cleanup := range []bool{false, true} {
		t.Run(strconv.FormatBool(cleanup), func(t *testing.T) {
			f := newLocalLifecycleFixture(t)
			branch, known := f.addDelivery(t, 7, foundationID)
			unknown := f.runtimePath(unmanagedWorkspace, "review-pr-8-123")
			f.addWorktree(t, unknown, "")
			f.fs.resolveFail[unknown] = true
			var output strings.Builder
			var err error
			if cleanup {
				err = f.service.CleanupAll(&output)
			} else {
				err = f.service.Status(&output)
			}
			if err == nil {
				t.Fatal("unknown discovery reported success")
			}
			if cleanup {
				if _, exists := f.refs[branch]; exists {
					t.Fatal("independent branch was not deleted")
				}
				if _, err := os.Lstat(known); !os.IsNotExist(err) {
					t.Fatal("independent worktree was not removed")
				}
			} else if !strings.Contains(output.String(), branch) {
				t.Fatal("readable branch row was lost")
			}
			if _, err := os.Lstat(unknown); err != nil {
				t.Fatal("unknown path was guessed into cleanup")
			}
		})
	}
}
