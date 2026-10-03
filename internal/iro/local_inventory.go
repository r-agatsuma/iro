package iro

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

type localGitRef struct {
	Name string
	HEAD string
}

type registeredWorktree struct {
	Path     string
	HEAD     string
	Branch   string
	Detached bool
	Bare     bool
	Locked   bool
	Prunable bool
}

// localGitInventory is an observation of one local repository, not a claim of
// ownership based on a forge identity. Paths retain Git's exact spelling.
type localGitInventory struct {
	CommonDir string
	Refs      []localGitRef
	Worktrees []registeredWorktree
}

func (s *Service) gitCommonDir(root string) (string, error) {
	result := s.Runner.Run(CommandSpec{Name: "git", Args: []string{"rev-parse", "--path-format=absolute", "--git-common-dir"}, Dir: root})
	value := strings.TrimSuffix(result.Stdout, "\n")
	if !commandSucceeded(result) || !filepath.IsAbs(value) || strings.ContainsAny(value, "\x00\n") {
		return "", fmt.Errorf("could not inspect local Git common directory: %s", strings.TrimSpace(result.Stderr))
	}
	return filepath.Clean(value), nil
}

func (s *Service) localInventory(root string) (localGitInventory, error) {
	common, err := s.gitCommonDir(root)
	if err != nil {
		return localGitInventory{}, err
	}
	result := s.Runner.Run(CommandSpec{Name: "git", Args: []string{"for-each-ref", "--sort=refname", "--format=%(refname)%00%(objectname)", "refs/heads/iro/"}, Dir: root})
	// Git can warn about broken refs while exiting successfully. Do not publish
	// an apparently complete inventory when Git reports an unreadable entry.
	if !commandSucceeded(result) || result.Stderr != "" {
		return localGitInventory{}, fmt.Errorf("could not enumerate local iro refs: %s", strings.TrimSpace(result.Stderr))
	}
	refs, err := parseLocalIroRefs(result.Stdout)
	if err != nil {
		return localGitInventory{}, err
	}
	result = s.Runner.Run(CommandSpec{Name: "git", Args: []string{"worktree", "list", "--porcelain", "-z"}, Dir: root})
	if !commandSucceeded(result) || result.Stderr != "" {
		return localGitInventory{}, fmt.Errorf("could not enumerate registered Git worktrees: %s", strings.TrimSpace(result.Stderr))
	}
	worktrees, err := parseRegisteredWorktrees(result.Stdout)
	if err != nil {
		return localGitInventory{}, err
	}
	observedCommon, err := s.gitCommonDir(root)
	if err != nil || observedCommon != common {
		return localGitInventory{}, fmt.Errorf("local Git common directory changed or became unreadable during inventory")
	}
	return localGitInventory{CommonDir: common, Refs: refs, Worktrees: worktrees}, nil
}

func validGitOID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, c := range value {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func validLocalRef(value string) bool {
	if !strings.HasPrefix(value, "refs/heads/") || strings.HasSuffix(value, "/") || strings.HasSuffix(value, ".") || strings.Contains(value, "..") || strings.Contains(value, "@{") {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	for _, c := range value {
		if c <= ' ' || c == 127 || strings.ContainsRune("~^:?*[\\", c) {
			return false
		}
	}
	return true
}

func parseLocalIroRefs(output string) ([]localGitRef, error) {
	var refs []localGitRef
	seen := make(map[string]bool)
	for _, line := range strings.Split(strings.TrimSuffix(output, "\n"), "\n") {
		if line == "" && output == "" {
			continue
		}
		parts := strings.Split(line, "\x00")
		if len(parts) != 2 || !validLocalRef(parts[0]) || !strings.HasPrefix(parts[0], "refs/heads/iro/") || !validGitOID(parts[1]) || seen[parts[0]] {
			return nil, fmt.Errorf("malformed local iro ref observation %q", line)
		}
		seen[parts[0]] = true
		refs = append(refs, localGitRef{Name: parts[0], HEAD: parts[1]})
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].Name < refs[j].Name })
	return refs, nil
}

// -z avoids Git's quoting of paths containing spaces, tabs, or newlines.
func parseRegisteredWorktrees(output string) ([]registeredWorktree, error) {
	if output == "" || !strings.HasSuffix(output, "\x00\x00") {
		return nil, fmt.Errorf("malformed Git worktree inventory: missing terminated records")
	}
	var worktrees []registeredWorktree
	seenPaths := make(map[string]bool)
	for _, record := range strings.Split(strings.TrimSuffix(output, "\x00\x00"), "\x00\x00") {
		var worktree registeredWorktree
		seenFields := make(map[string]bool)
		for i, field := range strings.Split(record, "\x00") {
			key, value, hasValue := strings.Cut(field, " ")
			if seenFields[key] || (i == 0 && key != "worktree") {
				return nil, fmt.Errorf("malformed Git worktree record: duplicate or misplaced field %q", key)
			}
			seenFields[key] = true
			switch key {
			case "worktree":
				if !filepath.IsAbs(value) {
					return nil, fmt.Errorf("malformed registered worktree path %q", value)
				}
				worktree.Path = value
			case "HEAD":
				if !validGitOID(value) {
					return nil, fmt.Errorf("unreadable registered worktree HEAD %q", value)
				}
				worktree.HEAD = value
			case "branch":
				if !validLocalRef(value) {
					return nil, fmt.Errorf("malformed registered worktree ref %q", value)
				}
				worktree.Branch = value
			case "detached", "bare":
				if hasValue {
					return nil, fmt.Errorf("malformed Git worktree field %q", field)
				}
				if key == "detached" {
					worktree.Detached = true
				} else {
					worktree.Bare = true
				}
			case "locked":
				worktree.Locked = true
			case "prunable":
				worktree.Prunable = true
			default:
				return nil, fmt.Errorf("unrecognized Git worktree field %q", key)
			}
		}
		modes := 0
		for _, mode := range []bool{worktree.Branch != "", worktree.Detached, worktree.Bare} {
			if mode {
				modes++
			}
		}
		if worktree.Path == "" || modes != 1 || !worktree.Bare && worktree.HEAD == "" || worktree.Bare && worktree.HEAD != "" || seenPaths[filepath.Clean(worktree.Path)] {
			return nil, fmt.Errorf("incomplete or conflicting registered worktree observation at %q", worktree.Path)
		}
		seenPaths[filepath.Clean(worktree.Path)] = true
		worktrees = append(worktrees, worktree)
	}
	sort.Slice(worktrees, func(i, j int) bool { return worktrees[i].Path < worktrees[j].Path })
	return worktrees, nil
}
