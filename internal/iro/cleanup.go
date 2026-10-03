package iro

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Cleanup destructively purges locally discovered resources for one Issue.
func (s *Service) Cleanup(issueNumber int, out io.Writer) error {
	if issueNumber <= 0 {
		return fmt.Errorf("issue number must be a positive decimal integer")
	}
	return s.cleanupLocal(issueNumber, out)
}

// CleanupAll destructively purges all discovered local iro resources.
func (s *Service) CleanupAll(out io.Writer) error {
	return s.cleanupLocal(0, out)
}

func issueBranchMatches(branch string, issueNumber int) bool {
	stem := fmt.Sprintf("iro/issue-%d", issueNumber)
	return branch == stem || strings.HasPrefix(branch, stem+"-")
}

// Workspace numbers for review/revise denote PRs, never Issues.
func (s *Service) runtimeWorkspaceIssue(path string) (int, error) {
	kind, runtime, err := s.recognizeRuntimeWorkspace(path)
	if err != nil || !runtime {
		return 0, err
	}
	resolved, err := s.resolveLocalPath(path)
	if err != nil {
		return 0, err
	}
	leaf := filepath.Base(resolved)
	if kind == managedWorkspace {
		number, _, err := parseDeliveryLeaf(leaf)
		return number, err
	}
	if strings.HasPrefix(leaf, "run-issue-") {
		return canonicalPositiveNumber(strings.Split(leaf, "-")[2])
	}
	return 0, nil
}

func (s *Service) cleanupSelection(resources []localResource, issueNumber int) ([]localResource, error) {
	if issueNumber == 0 {
		return resources, nil
	}
	var selected []localResource
	var observationErrors []error
	for _, resource := range resources {
		if issueBranchMatches(resource.branch, issueNumber) {
			selected = append(selected, resource)
			continue
		}
		// Other iro refs are independent branch resources. Do not select them
		// indirectly from only a subset of their attached workspace paths.
		if strings.HasPrefix(resource.branch, "iro/") {
			continue
		}
		var worktrees []registeredWorktree
		for _, worktree := range resource.worktrees {
			number, err := s.runtimeWorkspaceIssue(worktree.Path)
			if err != nil {
				observationErrors = append(observationErrors, err)
			} else if number == issueNumber {
				worktrees = append(worktrees, worktree)
			}
		}
		if len(worktrees) > 0 {
			resource.worktrees = worktrees
			selected = append(selected, resource)
		}
	}
	return selected, errors.Join(observationErrors...)
}

func (s *Service) cleanupLocal(issueNumber int, out io.Writer) error {
	if err := s.requireGit(); err != nil {
		return err
	}
	common, err := s.gitCommonDir("")
	if err != nil {
		return err
	}
	inventory, err := s.localInventory(common)
	if err != nil {
		return err
	}
	resources, discoveryErr := s.localResources(inventory)
	targets, selectionErr := s.cleanupSelection(resources, issueNumber)
	var failures []error
	for _, err := range []error{discoveryErr, selectionErr} {
		if err != nil {
			failures = append(failures, err)
			fmt.Fprintf(out, "Discovery unknown: %s\n", err)
		}
	}
	fmt.Fprintf(out, "Local repository: %q\n\n", inventory.CommonDir)
	// The common directory remains a usable command anchor when the invoking
	// linked worktree itself is selected and removed.
	anchor := inventory.CommonDir
	branchPresent := make(map[string]bool)
	for _, ref := range inventory.Refs {
		branchPresent[strings.TrimPrefix(ref.Name, "refs/heads/")] = true
	}
	for _, target := range targets {
		for _, worktree := range target.worktrees {
			result := s.Runner.Run(CommandSpec{Name: "git", Args: []string{"worktree", "remove", "--force", "--", worktree.Path}, Dir: anchor})
			if !commandSucceeded(result) {
				failures = append(failures, reportCleanupCommandFailure(out, "worktree removal", worktree.Path, result))
			}
		}
		if branchPresent[target.branch] {
			result := s.Runner.Run(CommandSpec{Name: "git", Args: []string{"branch", "-D", "--", target.branch}, Dir: anchor})
			if !commandSucceeded(result) {
				failures = append(failures, reportCleanupCommandFailure(out, "branch deletion", target.branch, result))
			}
		}
		for _, worktree := range target.worktrees {
			if err := s.removeKnownWorkspaceResidue(worktree.Path); err != nil {
				failures = append(failures, err)
				fmt.Fprintf(out, "Failed: %s\n", err)
			}
		}
	}
	post, postErr := s.localInventory(anchor)
	if postErr == nil && post.CommonDir != inventory.CommonDir {
		postErr = fmt.Errorf("local Git common directory changed after cleanup")
	}
	if postErr != nil {
		failures = append(failures, postErr)
		fmt.Fprintf(out, "Post-state unknown: %s\n", postErr)
	}
	absent, unresolved := 0, 0
	for _, target := range targets {
		err := s.confirmCleanupAbsent(target, post, postErr)
		if err != nil {
			unresolved++
			failures = append(failures, err)
			fmt.Fprintf(out, "Remaining / unknown: %s\n", err)
		} else {
			absent++
			fmt.Fprint(out, "Confirmed absent:")
			if strings.HasPrefix(target.branch, "iro/") {
				fmt.Fprintf(out, " branch %s", target.branch)
			}
			for _, worktree := range target.worktrees {
				fmt.Fprintf(out, "; worktree %q", worktree.Path)
			}
			fmt.Fprintln(out)
		}
	}
	fmt.Fprintf(out, "Cleanup summary: %d confirmed absent, %d remaining / unknown, %d failure(s).\n", absent, unresolved, len(failures))
	return errors.Join(failures...)
}

func reportCleanupCommandFailure(out io.Writer, action, target string, result CommandResult) error {
	err := fmt.Errorf("%s for %q failed (exit %d): %s", action, target, result.ExitCode, strings.TrimSpace(result.Stderr))
	if result.Err != nil {
		err = fmt.Errorf("%w: %v", err, result.Err)
	}
	fmt.Fprintf(out, "Failed: %s\n", err)
	return err
}

func (s *Service) removeKnownWorkspaceResidue(path string) error {
	present, err := s.knownCleanupPathPresent(path)
	if err != nil {
		return fmt.Errorf("inspect known workspace path %q: %w", path, err)
	}
	if !present {
		return nil
	}
	_, runtime, err := s.recognizeRuntimeWorkspace(path)
	if err != nil {
		return fmt.Errorf("verify known workspace path %q: %w", path, err)
	}
	if !runtime {
		return nil
	}
	// Only the exact registered candidate is removed. Never scan its parent,
	// derive sibling paths, or replace it with the resolved comparison path.
	if err := s.FileSystem.RemoveAll(path); err != nil {
		return fmt.Errorf("remove known workspace residue %q: %w", path, err)
	}
	return nil
}

func (s *Service) confirmCleanupAbsent(target localResource, post localGitInventory, postErr error) error {
	var remaining []error
	if postErr != nil {
		remaining = append(remaining, fmt.Errorf("Git post-state for branch %s is unknown", inventoryValue(target.branch)))
	} else {
		for _, ref := range post.Refs {
			if ref.Name == "refs/heads/"+target.branch {
				remaining = append(remaining, fmt.Errorf("local branch %s remains", target.branch))
			}
		}
		for _, worktree := range post.Worktrees {
			if strings.HasPrefix(target.branch, "iro/") && worktree.Branch == "refs/heads/"+target.branch {
				remaining = append(remaining, fmt.Errorf("branch %s remains registered at %q", target.branch, worktree.Path))
			}
			for _, candidate := range target.worktrees {
				if filepath.Clean(worktree.Path) == filepath.Clean(candidate.Path) {
					remaining = append(remaining, fmt.Errorf("worktree %q remains registered", candidate.Path))
					continue
				}
				candidateLocation, candidateErr := s.resolveLocalPath(candidate.Path)
				registeredLocation, registeredErr := s.resolveLocalPath(worktree.Path)
				if candidateErr != nil || registeredErr != nil {
					remaining = append(remaining, fmt.Errorf("worktree location post-state unknown: %w", errors.Join(candidateErr, registeredErr)))
				} else if candidateLocation == registeredLocation {
					remaining = append(remaining, fmt.Errorf("worktree %q remains registered at %q", candidate.Path, worktree.Path))
				}
			}
		}
	}
	for _, candidate := range target.worktrees {
		present, err := s.knownCleanupPathPresent(candidate.Path)
		if err != nil {
			remaining = append(remaining, fmt.Errorf("known path %q post-state unknown: %w", candidate.Path, err))
		} else if present {
			remaining = append(remaining, fmt.Errorf("known path %q remains", candidate.Path))
		}
	}
	return errors.Join(remaining...)
}

// Lstat counts dangling symlinks as remaining residue rather than absence.
func (s *Service) knownCleanupPathPresent(path string) (bool, error) {
	_, err := s.FileSystem.Lstat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}
