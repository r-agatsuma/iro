package iro

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

type cleanupTarget struct {
	branch      string
	worktree    string
	mappingPath string
}

// Cleanup removes one verified iro-owned Issue worktree, branch, and mapping.
func (s *Service) Cleanup(issueNumber int, out io.Writer) error {
	if issueNumber <= 0 {
		return fmt.Errorf("issue number must be a positive decimal integer")
	}
	if err := s.requireGit(); err != nil {
		return cleanupPreconditionError(issueNumber, err)
	}
	root, err := s.gitRoot()
	if err != nil {
		return cleanupPreconditionError(issueNumber, err)
	}
	config, err := s.loadInitializedConfig(root)
	if err != nil {
		return cleanupPreconditionError(issueNumber, err)
	}
	identity, err := s.repositoryIdentity(root, config)
	if err != nil {
		return cleanupPreconditionError(issueNumber, err)
	}

	target, err := s.validateCleanupTarget(root, identity, issueNumber)
	if err != nil {
		return cleanupPreconditionError(issueNumber, err)
	}

	if err := s.removeCleanupWorktree(root, target); err != nil {
		return cleanupMutationError(issueNumber, err)
	}
	if err := s.removeCleanupBranch(root, target); err != nil {
		return cleanupMutationError(issueNumber, err)
	}
	if err := s.FileSystem.Remove(target.mappingPath); err != nil {
		return cleanupMutationError(issueNumber, fmt.Errorf("could not remove ownership mapping %s: %w", target.mappingPath, err))
	}
	mappingPresent, err := s.pathPresent(target.mappingPath)
	if err != nil {
		return fmt.Errorf("cleanup of Issue #%d failed after local mutation: could not verify ownership mapping removal: %w\nOwnership mapping removal state could not be safely determined. Inspect the current worktree, branch, and mapping state before taking any manual action", issueNumber, err)
	}
	if mappingPresent {
		return cleanupMutationError(issueNumber, fmt.Errorf("ownership mapping %s still exists after removal", target.mappingPath))
	}

	fmt.Fprintf(out, "Issue #%d cleanup complete.\n\nRemoved worktree:\n  %s\n\nRemoved local branch:\n  %s\n\nRemoved ownership mapping.\n", issueNumber, target.worktree, target.branch)
	return nil
}

func (s *Service) validateCleanupTarget(root string, identity RepositoryIdentity, issueNumber int) (cleanupTarget, error) {
	target := cleanupTarget{
		branch:      fmt.Sprintf("iro/issue-%d", issueNumber),
		worktree:    cleanAbsolutePath(worktreePath(s.Dirs, identity, issueNumber)),
		mappingPath: ownershipPath(s.Dirs, identity, issueNumber),
	}

	present, regular, err := s.fileState(target.mappingPath)
	if err != nil {
		return cleanupTarget{}, fmt.Errorf("could not inspect ownership mapping %s: %w", target.mappingPath, err)
	}
	if !present {
		return cleanupTarget{}, fmt.Errorf("canonical ownership mapping %s does not exist; iro will not infer ownership", target.mappingPath)
	}
	if !regular {
		return cleanupTarget{}, fmt.Errorf("ownership mapping %s is not a regular file", target.mappingPath)
	}
	entries, err := s.FileSystem.ReadDir(filepath.Dir(target.mappingPath))
	if err != nil {
		return cleanupTarget{}, fmt.Errorf("could not inspect ownership mapping directory: %w", err)
	}
	foundRegularEntry := false
	for _, entry := range entries {
		if entry.Name() == filepath.Base(target.mappingPath) {
			if !entry.Type().IsRegular() {
				return cleanupTarget{}, fmt.Errorf("ownership mapping %s is not a regular file", target.mappingPath)
			}
			foundRegularEntry = true
			break
		}
	}
	if !foundRegularEntry {
		return cleanupTarget{}, fmt.Errorf("canonical ownership mapping %s does not exist; iro will not infer ownership", target.mappingPath)
	}
	mapping, mappingPresent, err := s.readOwnership(target.mappingPath)
	if err != nil || !mappingPresent {
		if err != nil {
			return cleanupTarget{}, err
		}
		return cleanupTarget{}, fmt.Errorf("canonical ownership mapping %s does not exist; iro will not infer ownership", target.mappingPath)
	}
	if err := validateOwnershipMapping(mapping, issueNumber, identity, s.Dirs); err != nil {
		return cleanupTarget{}, err
	}
	if mapping.IssueNumber != issueNumber {
		return cleanupTarget{}, fmt.Errorf("ownership mapping Issue number does not match requested Issue #%d", issueNumber)
	}

	branchPresent, err := s.branchExists(root, target.branch)
	if err != nil {
		return cleanupTarget{}, err
	}
	if !branchPresent {
		return cleanupTarget{}, fmt.Errorf("expected local branch %q does not exist", target.branch)
	}
	workspacePresent, err := s.pathPresent(target.worktree)
	if err != nil {
		return cleanupTarget{}, fmt.Errorf("could not inspect expected worktree path %s: %w", target.worktree, err)
	}
	if !workspacePresent {
		return cleanupTarget{}, fmt.Errorf("expected worktree path %s does not exist", target.worktree)
	}
	if cleanAbsolutePath(root) == target.worktree {
		return cleanupTarget{}, fmt.Errorf("invoking checkout is the cleanup target worktree %s", target.worktree)
	}

	worktrees, err := s.worktrees(root)
	if err != nil {
		return cleanupTarget{}, err
	}
	expectedBranch := "refs/heads/" + target.branch
	foundExpected := false
	for _, worktree := range worktrees {
		worktreePath := cleanAbsolutePath(worktree.Path)
		if worktree.Branch == expectedBranch && worktreePath != target.worktree {
			return cleanupTarget{}, fmt.Errorf("Issue branch %q is checked out in another worktree %s; resolve the branch/worktree collision manually", target.branch, worktreePath)
		}
		if worktreePath != target.worktree {
			continue
		}
		if worktree.Branch != expectedBranch {
			return cleanupTarget{}, fmt.Errorf("expected worktree %s has branch %q instead of %q", target.worktree, strings.TrimPrefix(worktree.Branch, "refs/heads/"), target.branch)
		}
		foundExpected = true
	}
	if !foundExpected {
		return cleanupTarget{}, fmt.Errorf("expected path %s is not registered as a Git worktree", target.worktree)
	}

	result := s.Runner.Run(CommandSpec{
		Name: "git",
		Args: []string{"--no-optional-locks", "status", "--porcelain", "--untracked-files=all"},
		Dir:  target.worktree,
	})
	if !commandSucceeded(result) {
		return cleanupTarget{}, fmt.Errorf("could not inspect cleanup target worktree status for %s", target.worktree)
	}
	if strings.TrimSpace(result.Stdout) != "" {
		return cleanupTarget{}, fmt.Errorf("cleanup target worktree %s is dirty; tracked or non-ignored untracked files are present", target.worktree)
	}

	result = s.Runner.Run(CommandSpec{
		Name: "git",
		Args: []string{"merge-base", "--is-ancestor", target.branch, "HEAD"},
		Dir:  root,
	})
	if !commandSucceeded(result) {
		if result.ExitCode == 1 {
			return cleanupTarget{}, fmt.Errorf("issue branch is not contained in the invoking HEAD history\nbranch: %s\nrun cleanup from an integration checkout that contains the Issue branch history", target.branch)
		}
		return cleanupTarget{}, fmt.Errorf("could not verify that issue branch %q is an ancestor of the invoking HEAD", target.branch)
	}

	return target, nil
}

func (s *Service) removeCleanupWorktree(root string, target cleanupTarget) error {
	result := s.Runner.Run(CommandSpec{
		Name: "git",
		Args: []string{"worktree", "remove", target.worktree},
		Dir:  root,
	})
	if !commandSucceeded(result) {
		return fmt.Errorf("Git rejected normal removal of worktree %s; no force cleanup was attempted; ownership mapping was retained", target.worktree)
	}

	present, err := s.pathPresent(target.worktree)
	if err != nil {
		return fmt.Errorf("worktree removal completed but its filesystem state could not be verified: %w; ownership mapping was retained", err)
	}
	if present {
		return fmt.Errorf("worktree removal completed but path %s still exists; ownership mapping was retained", target.worktree)
	}
	worktrees, err := s.worktrees(root)
	if err != nil {
		return fmt.Errorf("worktree removal completed but Git worktree state could not be verified: %w; ownership mapping was retained", err)
	}
	for _, worktree := range worktrees {
		if cleanAbsolutePath(worktree.Path) == target.worktree {
			return fmt.Errorf("worktree removal completed but Git still registers %s; ownership mapping was retained", target.worktree)
		}
	}
	return nil
}

func (s *Service) removeCleanupBranch(root string, target cleanupTarget) error {
	result := s.Runner.Run(CommandSpec{
		Name: "git",
		Args: []string{"branch", "-d", target.branch},
		Dir:  root,
	})
	if !commandSucceeded(result) {
		return fmt.Errorf("worktree %s was removed, but normal safe deletion of local branch %s failed; force deletion was not attempted; ownership mapping was retained", target.worktree, target.branch)
	}

	present, err := s.branchExists(root, target.branch)
	if err != nil {
		return fmt.Errorf("local branch %s was deleted, but its state could not be verified: %w; ownership mapping was retained", target.branch, err)
	}
	if present {
		return fmt.Errorf("local branch %s still exists after safe deletion; ownership mapping was retained", target.branch)
	}
	return nil
}

func cleanupPreconditionError(issueNumber int, cause error) error {
	remediation := fmt.Sprintf("resolve the reported local state and retry `iro cleanup %d`", issueNumber)
	causeText := cause.Error()
	switch {
	case strings.Contains(causeText, "not contained in the invoking HEAD history"):
		remediation = fmt.Sprintf("run cleanup from an integration checkout that contains the Issue branch history: `iro cleanup %d`", issueNumber)
	case strings.Contains(causeText, "invoking checkout is the cleanup target"):
		remediation = fmt.Sprintf("change to a different checkout that contains the Issue branch history, then retry `iro cleanup %d`", issueNumber)
	case strings.Contains(causeText, "branch/worktree collision"):
		remediation = fmt.Sprintf("resolve the duplicate Issue branch checkout manually, then retry `iro cleanup %d`", issueNumber)
	case strings.Contains(causeText, "cleanup target worktree") && strings.Contains(causeText, "is dirty"):
		remediation = fmt.Sprintf("review the target worktree, preserve or discard its changes manually, then retry `iro cleanup %d`", issueNumber)
	case strings.Contains(causeText, "ownership mapping"):
		remediation = fmt.Sprintf("inspect or restore the canonical ownership mapping without guessing ownership, then retry `iro cleanup %d`", issueNumber)
	}
	return fmt.Errorf("cleanup of Issue #%d rejected: %s\nNo resources were changed.\nRemediation: %s", issueNumber, cause, remediation)
}

func cleanupMutationError(issueNumber int, cause error) error {
	return fmt.Errorf("cleanup of Issue #%d failed after local mutation: %s\nOwnership mapping was retained. Inspect the current worktree, branch, and mapping state before taking any manual action", issueNumber, cause)
}
