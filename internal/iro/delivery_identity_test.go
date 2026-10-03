package iro

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDeliveryIDGenerationAndValidation(t *testing.T) {
	data := []byte{0, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff}
	reader := bytes.NewReader(append(data, 0xff))
	id, err := readDeliveryID(reader)
	if err != nil || id != foundationID || reader.Len() != 1 {
		t.Fatalf("exact 16-byte generation = %q, %v; remaining=%d", id, err, reader.Len())
	}
	if id, err := readDeliveryID(bytes.NewReader(data[:15])); err == nil || id != "" {
		t.Fatalf("short entropy accepted: %q, %v", id, err)
	}
	seen := make(map[deliveryID]bool)
	for i := 0; i < 32; i++ {
		id, err := generateDeliveryID()
		if err != nil || id.validate() != nil || seen[id] {
			t.Fatalf("secure generator = %q, %v", id, err)
		}
		seen[id] = true
	}
	for _, value := range []string{"", string(foundationID)[:31], string(foundationID) + "0", strings.ToUpper(string(foundationID)), strings.Repeat("g", 32), strings.Repeat("０", 32), strings.Repeat("0", 31) + "\n"} {
		if deliveryID(value).validate() == nil {
			t.Fatalf("invalid ID accepted: %q", value)
		}
	}
}

// A foundation producer must not read or write any v1 ownership authority.
type foundationFS struct {
	FileSystem
	mutations int
	failure   string
}

func (fs *foundationFS) ReadFile(string) ([]byte, error) {
	panic("foundation must not read ownership JSON")
}

func (fs *foundationFS) CreateNew(string, os.FileMode) (io.WriteCloser, error) {
	panic("foundation must not write ownership JSON")
}

func (fs *foundationFS) Lstat(path string) (os.FileInfo, error) {
	if fs.failure == "observation" {
		return nil, os.ErrPermission
	}
	return fs.FileSystem.Lstat(path)
}

func (fs *foundationFS) MkdirAll(path string, perm os.FileMode) error {
	fs.mutations++
	if fs.failure == "parent" {
		return errors.New("parent creation failed")
	}
	if fs.failure == "racing_path" {
		if err := fs.FileSystem.MkdirAll(path, perm); err != nil {
			return err
		}
		return fs.FileSystem.Mkdir(filepath.Join(path, deliveryLeaf(89, foundationID)), perm)
	}
	return fs.FileSystem.MkdirAll(path, perm)
}

func (fs *foundationFS) Mkdir(path string, perm os.FileMode) error {
	fs.mutations++
	return fs.FileSystem.Mkdir(path, perm)
}

func foundationService(t *testing.T) (*Service, *fakeCommandRunner, *foundationFS, string, *string, *string) {
	t.Helper()
	root := t.TempDir()
	refs, worktrees := "", worktreeRecord(root, "branch refs/heads/main")
	runner := foundationRunner(root, filepath.Join(root, ".git"), &refs, &worktrees)
	service := newTestService(t, runner, root)
	fs := &foundationFS{FileSystem: service.FileSystem}
	service.FileSystem = fs
	return service, runner, fs, root, &refs, &worktrees
}

func TestDeliveryAllocationRetriesOnlyBeforeSideEffects(t *testing.T) {
	identity := RepositoryIdentity{Owner: "acme", Name: "iro"}
	for _, collision := range []string{"branch", "descendant_ref", "namespace_ref", "file", "directory", "dangling_symlink", "attached", "detached_registration", "branch_elsewhere"} {
		t.Run(collision, func(t *testing.T) {
			service, runner, fs, root, refs, worktrees := foundationService(t)
			path := deliveryWorktreePath(service.Dirs, identity, 89, foundationID)
			branch := "refs/heads/" + deliveryBranch(89, foundationID)
			switch collision {
			case "branch", "descendant_ref":
				if collision == "descendant_ref" {
					branch += "/child"
				}
				*refs = branch + "\x00" + foundationHEAD + "\n"
			case "namespace_ref":
				original := runner.fn
				reads := 0
				runner.fn = func(spec CommandSpec) CommandResult {
					if spec.Args[0] == "show-ref" {
						reads++
						if reads == 1 {
							return CommandResult{}
						}
					}
					return original(spec)
				}
			case "attached":
				*worktrees += worktreeRecord(path, "branch "+branch)
			case "detached_registration":
				*worktrees += worktreeRecord(path, "detached")
			case "branch_elsewhere":
				*worktrees += worktreeRecord(filepath.Join(root, "elsewhere"), "branch "+branch)
			default:
				if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
					t.Fatal(err)
				}
				var err error
				switch collision {
				case "file":
					err = os.WriteFile(path, []byte("human file"), 0644)
				case "directory":
					err = os.Mkdir(path, 0755)
				case "dangling_symlink":
					err = os.Symlink(filepath.Join(root, "absent"), path)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			allocation, err := service.allocateDeliveryWithGenerator(root, identity, 89, func() (deliveryID, error) {
				calls++
				if calls == 1 {
					return foundationID, nil
				}
				return foundationOtherID, nil
			})
			if err != nil || calls != 2 || allocation.id != foundationOtherID || allocation.fixed || fs.mutations != 0 {
				t.Fatalf("pre-side-effect retry = %+v, %v; calls=%d mutations=%d", allocation, err, calls, fs.mutations)
			}
			if collision == "file" {
				data, _ := os.ReadFile(path)
				if string(data) != "human file" {
					t.Fatal("existing file overwritten")
				}
			}
		})
	}
}

func TestDeliveryCreationFixesIdentityOnFailure(t *testing.T) {
	identity := RepositoryIdentity{Owner: "acme", Name: "iro"}
	for _, failure := range []string{"parent", "racing_path", "git", ""} {
		t.Run("failure="+failure, func(t *testing.T) {
			service, runner, fs, root, _, _ := foundationService(t)
			allocation, err := service.allocateDeliveryWithGenerator(root, identity, 89, func() (deliveryID, error) { return foundationID, nil })
			if err != nil {
				t.Fatal(err)
			}
			fs.failure = failure
			original := runner.fn
			adds := 0
			runner.fn = func(spec CommandSpec) CommandResult {
				if spec.Args[0] == "worktree" && spec.Args[1] == "add" {
					adds++
					if !allocation.fixed || !reflect.DeepEqual(spec.Args, []string{"worktree", "add", "-b", allocation.branch, allocation.worktree, foundationHEAD}) {
						t.Fatalf("creation did not use fixed identity: %+v", spec)
					}
					if failure == "git" {
						return CommandResult{ExitCode: 1, Stderr: "branch appeared concurrently"}
					}
				}
				return original(spec)
			}
			err = service.createDeliveryWorktree(root, allocation, foundationHEAD)
			if (err != nil) != (failure != "") || !allocation.fixed || fs.mutations == 0 {
				t.Fatalf("creation = %v; fixed=%t mutations=%d", err, allocation.fixed, fs.mutations)
			}
			if err := service.selectDelivery(root, identity, allocation, func() (deliveryID, error) {
				t.Fatal("generator called after first side effect")
				return foundationOtherID, nil
			}); err == nil || allocation.id != foundationID {
				t.Fatalf("post-side-effect identity changed: %+v, %v", allocation, err)
			}
			if (failure == "parent" || failure == "racing_path") && adds != 0 {
				t.Fatal("Git creation attempted after failed path reservation")
			}
			if failure != "parent" {
				if _, err := os.Stat(allocation.worktree); err != nil {
					t.Fatalf("partial path not retained: %v", err)
				}
			}
		})
	}
}

func TestDeliveryAllocationFailsClosed(t *testing.T) {
	identity := RepositoryIdentity{Owner: "acme", Name: "iro"}
	for _, failure := range []string{"entropy", "invalid_id", "repeated_collision", "malformed_inventory", "unreadable_path", "foreign_common", "late_collision", "invalid_head"} {
		t.Run(failure, func(t *testing.T) {
			service, runner, fs, root, refs, worktrees := foundationService(t)
			generator := func() (deliveryID, error) { return foundationID, nil }
			switch failure {
			case "entropy":
				generator = func() (deliveryID, error) { return "", io.ErrUnexpectedEOF }
			case "invalid_id":
				generator = func() (deliveryID, error) { return "invalid", nil }
			case "repeated_collision":
				*refs = "refs/heads/" + deliveryBranch(89, foundationID) + "\x00" + foundationHEAD + "\n"
			case "malformed_inventory":
				*worktrees = "unreadable"
			case "unreadable_path":
				fs.failure = "observation"
			}
			allocation, err := service.allocateDeliveryWithGenerator(root, identity, 89, generator)
			if failure == "foreign_common" || failure == "late_collision" || failure == "invalid_head" {
				if err != nil {
					t.Fatal(err)
				}
				if failure == "foreign_common" {
					runner.fn = foundationRunner(root, filepath.Join(root, "other.git"), refs, worktrees).fn
				} else if failure == "late_collision" {
					*worktrees += worktreeRecord(allocation.worktree, "detached")
				}
				head := foundationHEAD
				if failure == "invalid_head" {
					head = "--force"
				}
				err = service.createDeliveryWorktree(root, allocation, head)
			}
			if err == nil || fs.mutations != 0 {
				t.Fatalf("unsafe failure: allocation=%+v err=%v mutations=%d", allocation, err, fs.mutations)
			}
		})
	}
}

func TestFreshDeliveryAllocationsForSameIssue(t *testing.T) {
	service, _, fs, root, _, _ := foundationService(t)
	identity := RepositoryIdentity{Owner: "acme", Name: "iro"}
	first, err := service.allocateDelivery(root, identity, 89)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.allocateDelivery(root, identity, 89)
	if err != nil || first.id == second.id || first.branch == second.branch || first.worktree == second.worktree || fs.mutations != 0 {
		t.Fatalf("fresh allocations = %+v, %+v, %v; mutations=%d", first, second, err, fs.mutations)
	}
}

func TestFixedDeliveryRechecksWithoutChangingIdentity(t *testing.T) {
	for _, collision := range []bool{false, true} {
		service, _, fs, root, refs, _ := foundationService(t)
		allocation, err := service.allocateDeliveryWithGenerator(root, RepositoryIdentity{Owner: "acme", Name: "iro"}, 89, func() (deliveryID, error) { return foundationID, nil })
		if err != nil {
			t.Fatal(err)
		}
		// A producer can cross the boundary before a fetch, then create resources
		// with the same identity. A newly observed collision cannot trigger retry.
		if err := service.beginDeliveryCreation(root, allocation); err != nil {
			t.Fatal(err)
		}
		if collision {
			*refs = "refs/heads/" + allocation.branch + "\x00" + foundationHEAD + "\n"
		}
		err = service.createDeliveryWorktree(root, allocation, foundationHEAD)
		if (err != nil) != collision || allocation.id != foundationID || !allocation.fixed || collision && fs.mutations != 0 {
			t.Fatalf("fixed creation = %+v, %v; mutations=%d", allocation, err, fs.mutations)
		}
	}
}
