package iro

import (
	"fmt"
	"strings"
)

type gitWorktree struct {
	Path   string
	Branch string
}

func (s *Service) gitRoot() (string, error) {
	result := s.Runner.Run(CommandSpec{Name: "git", Args: []string{"rev-parse", "--show-toplevel"}})
	if !commandSucceeded(result) {
		return "", fmt.Errorf("current directory is not a Git repository")
	}
	root := strings.TrimSpace(result.Stdout)
	if root == "" {
		return "", fmt.Errorf("Git did not return a repository root")
	}
	return cleanAbsolutePath(root), nil
}

func (s *Service) requireGit() error {
	if _, err := s.Runner.LookPath("git"); err != nil {
		return fmt.Errorf("git executable is unavailable; install Git and retry")
	}
	return nil
}

func (s *Service) checkoutClean(dir string) error {
	result := s.Runner.Run(CommandSpec{
		Name: "git",
		Args: []string{"status", "--porcelain", "--untracked-files=all"},
		Dir:  dir,
	})
	if !commandSucceeded(result) {
		return fmt.Errorf("could not inspect Git status")
	}
	if strings.TrimSpace(result.Stdout) != "" {
		return fmt.Errorf("source checkout is dirty; review or clean it before retrying")
	}
	return nil
}

func (s *Service) currentHead(dir string) (string, error) {
	result := s.Runner.Run(CommandSpec{Name: "git", Args: []string{"rev-parse", "HEAD"}, Dir: dir})
	if !commandSucceeded(result) {
		return "", fmt.Errorf("source checkout has no usable HEAD commit")
	}
	head := strings.TrimSpace(result.Stdout)
	if head == "" {
		return "", fmt.Errorf("Git returned an empty HEAD commit")
	}
	return head, nil
}

func (s *Service) remoteURLs(root string, remote string) ([]string, error) {
	result := s.Runner.Run(CommandSpec{
		Name: "git",
		Args: []string{"config", "--get-all", "remote." + remote + ".url"},
		Dir:  root,
	})
	if !commandSucceeded(result) {
		return nil, fmt.Errorf("configured remote %q is missing or has no URL", remote)
	}
	var urls []string
	for _, line := range strings.Split(result.Stdout, "\n") {
		if value := strings.TrimSpace(line); value != "" {
			urls = append(urls, value)
		}
	}
	if len(urls) == 0 {
		return nil, fmt.Errorf("configured remote %q is missing or has no URL", remote)
	}
	return urls, nil
}

func (s *Service) repositoryIdentity(root string, config Config) (RepositoryIdentity, error) {
	urls, err := s.remoteURLs(root, config.TrackerRemote)
	if err != nil {
		return RepositoryIdentity{}, err
	}
	if len(urls) != 1 {
		return RepositoryIdentity{}, fmt.Errorf("configured remote %q has multiple URLs; repository identity is ambiguous", config.TrackerRemote)
	}
	identity, err := parseGitHubRemote(urls[0])
	if err != nil {
		return RepositoryIdentity{}, fmt.Errorf("configured remote %q cannot identify a GitHub repository: %w", config.TrackerRemote, err)
	}
	return identity, nil
}

func (s *Service) branchExists(root string, branch string) (bool, error) {
	result := s.Runner.Run(CommandSpec{
		Name: "git",
		Args: []string{"show-ref", "--verify", "--quiet", "refs/heads/" + branch},
		Dir:  root,
	})
	if result.ExitCode == 0 && result.Err == nil {
		return true, nil
	}
	if result.ExitCode == 1 && result.Err != nil {
		return false, nil
	}
	if result.ExitCode == 1 && result.Err == nil {
		return false, nil
	}
	return false, fmt.Errorf("could not inspect branch %q", branch)
}

func (s *Service) worktrees(root string) ([]gitWorktree, error) {
	result := s.Runner.Run(CommandSpec{Name: "git", Args: []string{"worktree", "list", "--porcelain"}, Dir: root})
	if !commandSucceeded(result) {
		return nil, fmt.Errorf("could not inspect Git worktrees")
	}
	var resultWorktrees []gitWorktree
	var current *gitWorktree
	flush := func() {
		if current != nil && current.Path != "" {
			resultWorktrees = append(resultWorktrees, *current)
		}
		current = nil
	}
	for _, line := range strings.Split(result.Stdout, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			flush()
			current = &gitWorktree{Path: cleanAbsolutePath(strings.TrimPrefix(line, "worktree "))}
		case strings.HasPrefix(line, "branch ") && current != nil:
			current.Branch = strings.TrimPrefix(line, "branch ")
		case strings.TrimSpace(line) == "":
			flush()
		}
	}
	flush()
	return resultWorktrees, nil
}

func commandSucceeded(result CommandResult) bool {
	return result.Err == nil && result.ExitCode == 0
}
