package iro

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const (
	statusClean  = "CLEAN"
	statusDirty  = "DIRTY"
	statusBroken = "BROKEN"
)

type ownershipCandidate struct {
	path      string
	name      string
	issueHint int
	mapping   ownershipMapping
	readErr   error
}

type statusRow struct {
	issueNumber int
	issueLabel  string
	state       string
	branch      string
	worktree    string
}

// Status observes local iro-owned Issue workspaces without changing local or remote state.
func (s *Service) Status(out io.Writer) error {
	if err := s.requireGit(); err != nil {
		return err
	}
	root, err := s.gitRoot()
	if err != nil {
		return err
	}
	config, err := s.loadInitializedConfig(root)
	if err != nil {
		return err
	}
	identity, err := s.repositoryIdentity(root, config)
	if err != nil {
		return err
	}
	candidates, err := s.ownershipCandidates(identity)
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "Repository: %s\n\n", identity.String())
	if len(candidates) == 0 {
		fmt.Fprintln(out, "No iro-managed Issue workspaces.")
		return nil
	}

	worktrees, err := s.worktrees(root)
	if err != nil {
		return err
	}

	fmt.Fprintln(out, "ISSUE\tSTATE\tBRANCH\tWORKTREE")
	broken := 0
	for _, candidate := range candidates {
		row := s.inspectStatus(root, identity, candidate, worktrees)
		if row.state == statusBroken {
			broken++
		}
		fmt.Fprintf(out, "%s\t%s\t%s\t%s\n", row.issueLabel, row.state, row.branch, row.worktree)
	}
	if broken > 0 {
		return fmt.Errorf("status found %d broken iro-managed Issue workspace(s)", broken)
	}
	return nil
}

func (s *Service) loadInitializedConfig(root string) (Config, error) {
	workflowPath := filepath.Join(root, "WORKFLOW.md")
	workflowPresent, workflowRegular, err := s.fileState(workflowPath)
	if err != nil {
		return Config{}, fmt.Errorf("inspect WORKFLOW.md: %w", err)
	}
	if !workflowPresent || !workflowRegular {
		return Config{}, fmt.Errorf("WORKFLOW.md is missing or not a regular file")
	}

	configPath := filepath.Join(root, "iro.toml")
	configPresent, configRegular, err := s.fileState(configPath)
	if err != nil {
		return Config{}, fmt.Errorf("inspect iro.toml: %w", err)
	}
	if !configPresent || !configRegular {
		return Config{}, fmt.Errorf("iro.toml is missing or not a regular file")
	}
	return s.loadConfig(root)
}

func (s *Service) ownershipCandidates(identity RepositoryIdentity) ([]ownershipCandidate, error) {
	directory := filepath.Join(s.Dirs.StateRoot, "ownership", identity.Key())
	entries, err := s.FileSystem.ReadDir(directory)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("could not enumerate ownership mappings: %w", err)
	}

	candidates := make([]ownershipCandidate, 0, len(entries))
	for _, entry := range entries {
		issueHint, filenameErr := parseOwnershipFilename(entry.Name())
		if filenameErr != nil {
			continue
		}
		candidate := ownershipCandidate{
			path:      filepath.Join(directory, entry.Name()),
			name:      entry.Name(),
			issueHint: issueHint,
		}
		if !entry.Type().IsRegular() {
			candidate.readErr = fmt.Errorf("mapping is not a regular file")
		} else {
			data, readErr := s.FileSystem.ReadFile(candidate.path)
			if readErr != nil {
				candidate.readErr = fmt.Errorf("could not read mapping: %w", readErr)
			} else if unmarshalErr := json.Unmarshal(data, &candidate.mapping); unmarshalErr != nil {
				candidate.readErr = fmt.Errorf("mapping is invalid: %w", unmarshalErr)
			}
		}
		candidates = append(candidates, candidate)
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		left, right := candidates[i], candidates[j]
		if left.issueHint != right.issueHint {
			if left.issueHint == 0 {
				return false
			}
			if right.issueHint == 0 {
				return true
			}
			return left.issueHint < right.issueHint
		}
		return left.name < right.name
	})
	return candidates, nil
}

func parseOwnershipFilename(name string) (int, error) {
	const prefix = "issue-"
	const suffix = ".json"
	if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) {
		return 0, fmt.Errorf("mapping filename must be issue-<issue-number>.json")
	}
	value := strings.TrimSuffix(strings.TrimPrefix(name, prefix), suffix)
	if value == "" {
		return 0, fmt.Errorf("mapping filename must contain a positive Issue number")
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return 0, fmt.Errorf("mapping filename must contain a positive Issue number")
		}
	}
	number, err := strconv.Atoi(value)
	if err != nil || number <= 0 {
		return 0, fmt.Errorf("mapping filename must contain a positive Issue number")
	}
	if strconv.Itoa(number) != value {
		return 0, fmt.Errorf("mapping filename must use the canonical Issue number")
	}
	return number, nil
}

func (s *Service) inspectStatus(root string, identity RepositoryIdentity, candidate ownershipCandidate, worktrees []gitWorktree) statusRow {
	issueNumber := candidate.mapping.IssueNumber
	if issueNumber <= 0 {
		issueNumber = candidate.issueHint
	}
	row := statusRow{
		issueNumber: issueNumber,
		issueLabel:  "?",
		state:       statusBroken,
		branch:      candidate.mapping.Branch,
		worktree:    candidate.mapping.Worktree,
	}
	if issueNumber > 0 {
		row.issueLabel = fmt.Sprintf("#%d", issueNumber)
		if row.branch == "" {
			row.branch = fmt.Sprintf("iro/issue-%d", issueNumber)
		}
		if row.worktree == "" {
			row.worktree = cleanAbsolutePath(worktreePath(s.Dirs, identity, issueNumber))
		}
	}

	if candidate.readErr != nil {
		return row
	}
	if err := validateOwnershipMapping(candidate.mapping, candidate.issueHint, identity, s.Dirs); err != nil {
		return row
	}

	row.issueNumber = candidate.mapping.IssueNumber
	row.issueLabel = fmt.Sprintf("#%d", row.issueNumber)
	row.branch = candidate.mapping.Branch
	row.worktree = cleanAbsolutePath(candidate.mapping.Worktree)

	branchPresent, err := s.branchExists(root, candidate.mapping.Branch)
	if err != nil || !branchPresent {
		return row
	}
	workspacePresent, err := s.pathPresent(row.worktree)
	if err != nil || !workspacePresent {
		return row
	}

	foundExpected := false
	for _, worktree := range worktrees {
		if cleanAbsolutePath(worktree.Path) != row.worktree {
			continue
		}
		if worktree.Branch != "refs/heads/"+candidate.mapping.Branch {
			return row
		}
		foundExpected = true
	}
	if !foundExpected {
		return row
	}

	result := s.Runner.Run(CommandSpec{
		Name: "git",
		Args: []string{"--no-optional-locks", "status", "--porcelain", "--untracked-files=all"},
		Dir:  row.worktree,
	})
	if !commandSucceeded(result) {
		return row
	}
	if strings.TrimSpace(result.Stdout) != "" {
		row.state = statusDirty
		return row
	}
	row.state = statusClean
	return row
}

func validateOwnershipMapping(mapping ownershipMapping, filenameIssue int, identity RepositoryIdentity, dirs RuntimeDirs) error {
	if mapping.Version != 1 {
		return fmt.Errorf("unsupported mapping version")
	}
	if mapping.Repository != identity.Canonical() {
		return fmt.Errorf("mapping repository does not match current repository")
	}
	if mapping.IssueNumber <= 0 || mapping.IssueNumber != filenameIssue {
		return fmt.Errorf("mapping Issue number does not match its path")
	}
	expectedBranch := fmt.Sprintf("iro/issue-%d", mapping.IssueNumber)
	if mapping.Branch != expectedBranch {
		return fmt.Errorf("mapping branch does not match the Issue number")
	}
	if strings.TrimSpace(mapping.Worktree) == "" || cleanAbsolutePath(mapping.Worktree) != cleanAbsolutePath(worktreePath(dirs, identity, mapping.IssueNumber)) {
		return fmt.Errorf("mapping worktree does not match the repository and Issue number")
	}
	return nil
}
