package iro

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

// localResource groups registered worktrees by local branch ref. Worktrees
// without an iro ref are included only with runtime workspace naming evidence.
type localResource struct {
	branch    string
	head      string
	worktrees []registeredWorktree
}

func (s *Service) localResources(inventory localGitInventory) ([]localResource, error) {
	var resources []localResource
	branches := make(map[string]int)
	for _, ref := range inventory.Refs {
		branch := strings.TrimPrefix(ref.Name, "refs/heads/")
		branches[branch] = len(resources)
		resources = append(resources, localResource{branch: branch, head: ref.HEAD})
	}
	var observationErrors []error
	for _, worktree := range inventory.Worktrees {
		branch := strings.TrimPrefix(worktree.Branch, "refs/heads/")
		if strings.HasPrefix(branch, "iro/") {
			index, found := branches[branch]
			if !found {
				index = len(resources)
				branches[branch] = index
				resources = append(resources, localResource{branch: branch, head: worktree.HEAD})
			}
			resources[index].worktrees = append(resources[index].worktrees, worktree)
			continue
		}
		_, runtime, err := s.recognizeRuntimeWorkspace(worktree.Path)
		if err != nil {
			observationErrors = append(observationErrors, fmt.Errorf("classify registered worktree %q: %w", worktree.Path, err))
			continue
		}
		if runtime {
			resources = append(resources, localResource{branch: branch, head: worktree.HEAD, worktrees: []registeredWorktree{worktree}})
		}
	}
	sort.SliceStable(resources, func(i, j int) bool {
		if resources[i].branch != resources[j].branch {
			return resources[i].branch < resources[j].branch
		}
		return resources[i].worktrees[0].Path < resources[j].worktrees[0].Path
	})
	return resources, errors.Join(observationErrors...)
}

// Status displays a read-only inventory of the current local repository.
func (s *Service) Status(out io.Writer) error {
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
	resources, observationErr := s.localResources(inventory)
	fmt.Fprintf(out, "Local repository: %q\n\n", inventory.CommonDir)
	if len(resources) == 0 && observationErr == nil {
		fmt.Fprintln(out, "No local iro resources.")
		return nil
	}
	fmt.Fprintln(out, "BRANCH\tWORKTREE\tHEAD")
	for _, resource := range resources {
		paths := make([]string, 0, len(resource.worktrees))
		for _, worktree := range resource.worktrees {
			paths = append(paths, strconv.Quote(worktree.Path))
		}
		fmt.Fprintf(out, "%s\t%s\t%s\n", inventoryValue(resource.branch), inventoryValue(strings.Join(paths, ", ")), inventoryValue(resource.head))
	}
	return observationErr
}

func inventoryValue(value string) string {
	if value == "" {
		return "none"
	}
	return value
}
