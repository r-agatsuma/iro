package iro

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type deliveryID string

func generateDeliveryID() (deliveryID, error) {
	return readDeliveryID(rand.Reader)
}

func readDeliveryID(random io.Reader) (deliveryID, error) {
	var bytes [16]byte
	if _, err := io.ReadFull(random, bytes[:]); err != nil {
		return "", fmt.Errorf("generate delivery ID: %w", err)
	}
	return deliveryID(hex.EncodeToString(bytes[:])), nil
}

func (id deliveryID) validate() error {
	if len(id) != 32 {
		return fmt.Errorf("delivery ID must be exactly 32 lowercase ASCII hex characters")
	}
	for i := range id {
		c := id[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return fmt.Errorf("delivery ID must be exactly 32 lowercase ASCII hex characters")
		}
	}
	return nil
}

// deliveryAllocation separates read-only selection from resource creation.
// Once fixed, its identity remains unchanged even if creation fails partially.
type deliveryAllocation struct {
	id        deliveryID
	issue     int
	branch    string
	worktree  string
	commonDir string
	fixed     bool
	pushURL   string
	detached  bool
}

func (s *Service) allocateDelivery(root string, namespace runtimeNamespaceKey, issueNumber int) (*deliveryAllocation, error) {
	return s.allocateDeliveryWithGenerator(root, namespace, issueNumber, generateDeliveryID)
}

func (s *Service) allocateDeliveryWithGenerator(root string, namespace runtimeNamespaceKey, issueNumber int, generate func() (deliveryID, error)) (*deliveryAllocation, error) {
	if issueNumber <= 0 {
		return nil, fmt.Errorf("issue number must be positive")
	}
	common, err := s.gitCommonDir(root)
	if err != nil {
		return nil, err
	}
	allocation := &deliveryAllocation{issue: issueNumber, commonDir: common}
	if err := s.selectDelivery(root, namespace, allocation, generate); err != nil {
		return nil, err
	}
	return allocation, nil
}

// Selection may retry collisions only before any local side effect. The bounded
// retry protects callers from a broken/injected generator returning one ID forever.
func (s *Service) selectDelivery(root string, namespace runtimeNamespaceKey, allocation *deliveryAllocation, generate func() (deliveryID, error)) error {
	if allocation.fixed {
		return fmt.Errorf("delivery %s is fixed after the creation boundary; identity cannot change", allocation.id)
	}
	for attempt := 0; attempt < 16; attempt++ {
		id, err := generate()
		if err != nil {
			return err
		}
		if err := id.validate(); err != nil {
			return err
		}
		allocation.id = id
		allocation.branch = deliveryBranch(allocation.issue, id)
		allocation.worktree = cleanAbsolutePath(deliveryWorktreePath(s.Dirs, namespace, allocation.issue, id))
		if allocation.detached {
			allocation.worktree = cleanAbsolutePath(filepath.Join(runtimeWorkspaceParent(s.Dirs, namespace, unmanagedWorkspace), detachedWorkspaceStem("run", allocation.issue)+string(id)))
		}
		collision, err := s.inspectDeliveryCollision(root, allocation)
		if err != nil {
			return err
		}
		if !collision {
			return nil
		}
	}
	return fmt.Errorf("could not allocate delivery identity after 16 delivery collisions; no resources were changed")
}

func (s *Service) inspectDeliveryCollision(root string, allocation *deliveryAllocation) (bool, error) {
	inventory, err := s.localInventory(root)
	if err != nil {
		return false, err
	}
	if inventory.CommonDir != allocation.commonDir {
		return false, fmt.Errorf("delivery allocation belongs to a different local Git common directory")
	}
	ref := "refs/heads/" + allocation.branch
	for _, entry := range inventory.Refs {
		if entry.Name == ref || strings.HasPrefix(entry.Name, ref+"/") {
			return true, nil
		}
	}
	// refs/heads/iro itself also blocks creation of refs/heads/iro/*.
	present, err := s.branchExists(root, "iro")
	if err != nil || present {
		return present, err
	}
	// Lstat includes dangling symlinks: a path is occupied even if its target is absent.
	_, err = s.FileSystem.Lstat(allocation.worktree)
	if err == nil {
		return true, nil
	}
	if !os.IsNotExist(err) {
		return false, fmt.Errorf("inspect delivery worktree path %s: %w", allocation.worktree, err)
	}
	location, err := s.resolveLocalPath(allocation.worktree)
	if err != nil {
		return false, err
	}
	for _, worktree := range inventory.Worktrees {
		if filepath.Clean(worktree.Path) == allocation.worktree || worktree.Branch == ref {
			return true, nil
		}
		registeredLocation, err := s.resolveLocalPath(worktree.Path)
		if err != nil {
			return false, err
		}
		if registeredLocation == location {
			return true, nil
		}
	}
	if allocation.pushURL != "" {
		return s.inspectRemoteDeliveryCollision(root, allocation)
	}
	return false, nil
}

// Call this immediately before the first local side effect (including directory
// creation or fetch). Before it succeeds, selection is still read-only and may
// retry collisions. After it succeeds, selecting another ID is forbidden.
func (s *Service) beginDeliveryCreation(root string, allocation *deliveryAllocation) error {
	if allocation.fixed {
		return fmt.Errorf("delivery %s already crossed the creation boundary", allocation.id)
	}
	collision, err := s.inspectDeliveryCollision(root, allocation)
	if err != nil {
		return err
	}
	if collision {
		return fmt.Errorf("delivery %s collided before creation; no existing ref or path will be adopted", allocation.id)
	}
	allocation.fixed = true
	return nil
}

// createDeliveryWorktree creates only fresh attached or detached local resources. It neither consults
// nor writes v1 ownership JSON. Failures retain the fixed identity and partial state.
func (s *Service) createDeliveryWorktree(root string, allocation *deliveryAllocation, head string) error {
	if !validGitOID(head) {
		return fmt.Errorf("delivery worktree requires a full HEAD object ID")
	}
	if !allocation.fixed {
		if err := s.beginDeliveryCreation(root, allocation); err != nil {
			return err
		}
	} else {
		collision, err := s.inspectDeliveryCollision(root, allocation)
		if err != nil {
			return err
		}
		if collision {
			return fmt.Errorf("fixed delivery %s collided; no existing ref or path will be adopted", allocation.id)
		}
	}
	if err := s.FileSystem.MkdirAll(filepath.Dir(allocation.worktree), 0755); err != nil {
		return fmt.Errorf("create delivery workspace parent for %s: %w", allocation.id, err)
	}
	// Reserve exclusively so even an empty path appearing after inspection cannot
	// be silently adopted by git worktree add. Keep the reservation on failure.
	if err := s.FileSystem.Mkdir(allocation.worktree, 0755); err != nil {
		return fmt.Errorf("reserve delivery worktree %s: %w", allocation.worktree, err)
	}
	args := []string{"worktree", "add", "-b", allocation.branch, allocation.worktree, head}
	if allocation.detached {
		args = []string{"worktree", "add", "--detach", allocation.worktree, head}
	}
	result := s.Runner.Run(CommandSpec{Name: "git", Args: args, Dir: root})
	if !commandSucceeded(result) {
		return fmt.Errorf("delivery %s worktree creation failed; inspect partial local state at %s: %s", allocation.id, allocation.worktree, strings.TrimSpace(result.Stderr))
	}
	return nil
}
