package iro

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
)

const unmanagedDeveloperInstructions = `You are executing one unmanaged iro task under the built-in conservative worker policy.

Do not read iro.toml or WORKFLOW.md. They do not select configuration or worker policy for this operation.
Follow the AGENTS.md instruction chain loaded by Codex within these instructions.
If applicable guidance materially conflicts, stop without editing and report the conflict.
Make only the changes required by the supplied Issue. Do not invent missing requirements or expand the scope.
If a new product or architecture decision is required, stop and report it for human judgment.
Do not provision or repair missing environments, credentials, remotes, branches, or worktrees.
Preserve human-owned files and changes. Do not perform destructive cleanup or automatically retry failed operations.

` + workerSafetyInstructions

// Keep unmanaged transport validation separate from managed remote parsing.
// A matching host and repository path alone do not establish a GitHub endpoint.
func parseUnmanagedGitHubRemote(raw string) (RepositoryIdentity, error) {
	if strings.Contains(raw, "://") {
		parsed, err := url.Parse(raw)
		if err != nil {
			return RepositoryIdentity{}, fmt.Errorf("invalid origin URL; inspect remote configuration manually")
		}
		var defaultPort string
		switch parsed.Scheme {
		case "https":
			defaultPort = "443"
		case "ssh":
			defaultPort = "22"
		default:
			return RepositoryIdentity{}, fmt.Errorf("origin URL must use HTTPS or SSH")
		}
		if parsed.Host != parsed.Hostname() && parsed.Host != parsed.Hostname()+":"+defaultPort {
			return RepositoryIdentity{}, fmt.Errorf("origin URL must use the standard %s port (%s)", parsed.Scheme, defaultPort)
		}
		if strings.ContainsAny(raw, "?#") {
			return RepositoryIdentity{}, fmt.Errorf("origin URL must not contain a query or fragment")
		}
	}
	return parseGitHubRemote(raw)
}

// originIdentity deliberately does not consult project configuration or ambient
// GitHub selectors. Count configured values before parsing, including empty ones.
func (s *Service) originIdentity(root string) (RepositoryIdentity, error) {
	result := s.Runner.Run(CommandSpec{Name: "git", Args: []string{"config", "--get-all", "remote.origin.url"}, Dir: root})
	url, err := singleRemoteURL(result)
	if err != nil {
		return RepositoryIdentity{}, fmt.Errorf("origin requires exactly one configured fetch URL: %w", err)
	}
	identity, err := parseUnmanagedGitHubRemote(url)
	if err != nil {
		return RepositoryIdentity{}, fmt.Errorf("origin cannot identify a supported GitHub repository: %w", err)
	}
	// Git expands url.*.insteadOf for origin reads. A rewrite may change the
	// transport, but must not redirect ref validation to another repository.
	result = s.Runner.Run(CommandSpec{Name: "git", Args: []string{"remote", "get-url", "--all", "origin"}, Dir: root})
	url, err = singleRemoteURL(result)
	if err != nil {
		return RepositoryIdentity{}, fmt.Errorf("origin requires exactly one effective fetch URL: %w", err)
	}
	destination, err := parseUnmanagedGitHubRemote(url)
	if err != nil || destination.Canonical() != identity.Canonical() {
		return RepositoryIdentity{}, fmt.Errorf("origin effective fetch destination must identify the origin GitHub repository; inspect remote configuration manually")
	}
	return identity, nil
}

func singleRemoteURL(result CommandResult) (string, error) {
	values := strings.Split(strings.TrimSuffix(result.Stdout, "\n"), "\n")
	if !commandSucceeded(result) || len(values) != 1 || strings.TrimSpace(values[0]) == "" {
		return "", fmt.Errorf("URL is missing or multiple URLs are configured; inspect remote configuration manually")
	}
	return values[0], nil
}

// validateOriginPushDestination is shared by unmanaged operations that push.
func (s *Service) validateOriginPushDestination(root string, identity RepositoryIdentity) error {
	result := s.Runner.Run(CommandSpec{Name: "git", Args: []string{"remote", "get-url", "--push", "--all", "origin"}, Dir: root})
	url, err := singleRemoteURL(result)
	if err != nil {
		return fmt.Errorf("origin requires exactly one effective push URL: %w", err)
	}
	destination, err := parseUnmanagedGitHubRemote(url)
	if err != nil || destination.Canonical() != identity.Canonical() {
		return fmt.Errorf("origin push destination must identify the origin GitHub repository; inspect remote configuration manually")
	}
	return nil
}

func (s *Service) runUnmanaged(number int, options workerOptions, out, errOut io.Writer) error {
	if number <= 0 {
		return fmt.Errorf("issue number must be a positive decimal integer")
	}
	if err := s.requireGit(); err != nil {
		return err
	}
	root, err := s.gitRoot()
	if err != nil {
		return err
	}
	identity, err := s.originIdentity(root)
	if err != nil {
		return err
	}
	if err := s.checkoutClean(root); err != nil {
		return err
	}
	result := s.Runner.Run(CommandSpec{Name: "git", Args: []string{"symbolic-ref", "--quiet", "HEAD"}, Dir: root})
	ref := strings.TrimSpace(result.Stdout)
	if !commandSucceeded(result) || !strings.HasPrefix(ref, "refs/heads/") || ref == "refs/heads/" {
		return fmt.Errorf("unmanaged run requires a named branch; detached HEAD is not supported")
	}
	base := strings.TrimPrefix(ref, "refs/heads/")
	head, err := s.currentHead(root)
	if err != nil || !validCommitOID(head) {
		return fmt.Errorf("source checkout has no valid HEAD commit")
	}
	branch := fmt.Sprintf("iro/issue-%d", number)
	if err := s.revalidateUnmanagedRun(root, identity, base, head, branch); err != nil {
		return err
	}
	if err := s.requireExecutable("gh"); err != nil {
		return err
	}
	if err := s.checkAuth("gh", []string{"auth", "status", "--hostname", identity.Host()}, root); err != nil {
		return err
	}
	target, err := s.fetchIssue(root, identity, number)
	if err != nil {
		return err
	}
	if err := s.requireExecutable("codex"); err != nil {
		return err
	}
	if err := s.checkAuth("codex", []string{"login", "status"}, root); err != nil {
		return err
	}
	workspace, err := s.createUnmanagedWorktree(root, identity, number, head)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Created unmanaged detached worktree at %s\n", workspace)
	if err := s.verifyDetachedHead(workspace, head); err != nil {
		return err
	}
	if err := s.checkoutClean(workspace); err != nil {
		return fmt.Errorf("new unmanaged worktree is not clean; inspect %s: %w", workspace, err)
	}
	result = s.runCodex(workspace, identity, target, options)
	logErr := s.writeUnmanagedRunLog(identity, number, base, head, workspace, result)
	var operationErr error
	if !commandSucceeded(result) {
		operationErr = fmt.Errorf("Author exited with status %d (%v)", result.ExitCode, result.Err)
	} else if logErr != nil {
		operationErr = logErr
	} else {
		operationErr = s.deliverUnmanaged(root, workspace, identity, number, base, head, branch, result.Stdout, out, errOut)
	}
	if operationErr != nil {
		if logErr != nil {
			fmt.Fprintf(errOut, "warning: could not preserve Author log: %v\nAuthor stdout:\n%s\nAuthor stderr:\n%s\n", logErr, result.Stdout, result.Stderr)
		}
		operationErr = fmt.Errorf("%w; unmanaged worktree kept at %s; inspect local and remote state before any new invocation; no automatic retry or rollback", operationErr, workspace)
		if err := s.postResult(root, identity, number, buildFailureComment(number, workspace, result, operationErr)); err != nil {
			fmt.Fprintf(errOut, "warning: failure report comment failed: %v\n", err)
		}
		return operationErr
	}
	if err := s.removeUnmanagedWorktree(root, workspace); err != nil {
		fmt.Fprintf(errOut, "warning: delivery confirmed, but cleanup failed at %s: %v; do not retry delivery\n", workspace, err)
	}
	return nil
}

func (s *Service) revalidateUnmanagedRun(root string, identity RepositoryIdentity, base, head, branch string) error {
	current, err := s.originIdentity(root)
	if err != nil {
		return fmt.Errorf("origin repository changed or is unreadable: %w", err)
	}
	if current.Canonical() != identity.Canonical() {
		return fmt.Errorf("origin repository changed or is unreadable; inspect remote configuration")
	}
	if err := s.validateOriginPushDestination(root, identity); err != nil {
		return err
	}
	baseRef, taskRef := "refs/heads/"+base, "refs/heads/"+branch
	result := s.Runner.Run(CommandSpec{Name: "git", Args: []string{"ls-remote", "--heads", "--", "origin", baseRef, taskRef}, Dir: root})
	if !commandSucceeded(result) {
		return fmt.Errorf("could not inspect origin base and task refs; verify remote access")
	}
	baseFound := false
	for _, line := range strings.Split(strings.TrimSpace(result.Stdout), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || !validCommitOID(fields[0]) {
			return fmt.Errorf("origin base is missing or remote ref data is invalid")
		}
		if fields[1] == taskRef {
			return fmt.Errorf("remote %s already exists; no push or replacement is allowed", taskRef)
		}
		if fields[1] != baseRef || baseFound || fields[0] != head {
			return fmt.Errorf("origin/%s must remain at source commit %s; base drift or unexpected remote ref detected", base, head)
		}
		baseFound = true
	}
	if !baseFound {
		return fmt.Errorf("origin/%s is missing", base)
	}
	return nil
}

func (s *Service) createUnmanagedWorktree(root string, identity RepositoryIdentity, number int, head string) (string, error) {
	parent := cleanAbsolutePath(filepath.Join(s.Dirs.DataRoot, "unmanaged-workspaces", identity.Key()))
	if err := s.FileSystem.MkdirAll(parent, 0755); err != nil {
		return "", fmt.Errorf("create unmanaged workspace parent: %w", err)
	}
	workspace, err := s.FileSystem.MkdirTemp(parent, fmt.Sprintf("run-issue-%d-*", number))
	if err != nil {
		return "", fmt.Errorf("reserve unique unmanaged workspace: %w", err)
	}
	result := s.Runner.Run(CommandSpec{Name: "git", Args: []string{"worktree", "add", "--detach", workspace, head}, Dir: root})
	if !commandSucceeded(result) {
		return "", fmt.Errorf("could not create detached worktree; inspect possible partial state at %s", workspace)
	}
	return workspace, nil
}

func (s *Service) verifyDetachedHead(workspace, expected string) error {
	result := s.Runner.Run(CommandSpec{Name: "git", Args: []string{"symbolic-ref", "--quiet", "HEAD"}, Dir: workspace})
	if result.ExitCode != 1 || strings.TrimSpace(result.Stdout) != "" {
		return fmt.Errorf("unmanaged worktree must remain detached at %s", workspace)
	}
	head, err := s.currentHead(workspace)
	if err != nil || head != expected {
		return fmt.Errorf("unmanaged worktree HEAD changed or is unreadable at %s; expected %s", workspace, expected)
	}
	return nil
}

func (s *Service) removeUnmanagedWorktree(root, workspace string) error {
	result := s.Runner.Run(CommandSpec{Name: "git", Args: []string{"worktree", "remove", "--", workspace}, Dir: root})
	if !commandSucceeded(result) {
		return fmt.Errorf("Git rejected normal worktree removal")
	}
	present, err := s.pathPresent(workspace)
	if err != nil || present {
		return fmt.Errorf("worktree path removal could not be confirmed")
	}
	worktrees, err := s.worktrees(root)
	if err != nil {
		return err
	}
	for _, worktree := range worktrees {
		if cleanAbsolutePath(worktree.Path) == cleanAbsolutePath(workspace) {
			return fmt.Errorf("Git still registers the worktree")
		}
	}
	return nil
}

func (s *Service) writeUnmanagedRunLog(identity RepositoryIdentity, number int, base, head, workspace string, result CommandResult) error {
	dir := filepath.Join(s.Dirs.StateRoot, "unmanaged-runs", identity.Key())
	if err := s.FileSystem.MkdirAll(dir, 0700); err != nil {
		return err
	}
	content := fmt.Sprintf("repository: %s\nissue_number: %d\nbase: %s\nsource: %s\nworktree: %s\ncodex_exit_status: %d\ncodex_error: %v\n\n--- stdout ---\n%s\n--- stderr ---\n%s\n", identity.String(), number, base, head, workspace, result.ExitCode, result.Err, result.Stdout, result.Stderr)
	file, err := s.FileSystem.CreateNew(filepath.Join(dir, filepath.Base(workspace)+".log"), 0600)
	if err != nil {
		return fmt.Errorf("create unmanaged Author log: %w", err)
	}
	if _, err := io.WriteString(file, content); err != nil {
		_ = file.Close()
		return fmt.Errorf("write unmanaged Author log: %w", err)
	}
	return file.Close()
}

func (s *Service) deliverUnmanaged(root, workspace string, identity RepositoryIdentity, number int, base, head, branch, authorReport string, out, errOut io.Writer) error {
	if err := s.verifyDetachedHead(workspace, head); err != nil {
		return err
	}
	result := s.Runner.Run(CommandSpec{Name: "git", Args: []string{"add", "--all"}, Dir: workspace})
	if !commandSucceeded(result) {
		return fmt.Errorf("could not stage worker changes")
	}
	result = s.Runner.Run(CommandSpec{Name: "git", Args: []string{"diff", "--cached", "--quiet", "--exit-code"}, Dir: workspace})
	if commandSucceeded(result) {
		return fmt.Errorf("worker produced no committable changes; no commit, push or PR created")
	}
	if result.ExitCode != 1 {
		return fmt.Errorf("could not inspect staged changes")
	}
	if err := s.revalidateUnmanagedRun(workspace, identity, base, head, branch); err != nil {
		return fmt.Errorf("unmanaged delivery stopped before commit: %w", err)
	}
	result = s.Runner.Run(CommandSpec{Name: "git", Args: []string{"commit", "-m", fmt.Sprintf("Implement issue #%d", number)}, Dir: workspace})
	if !commandSucceeded(result) {
		return fmt.Errorf("commit failed; inspect Git identity and hooks")
	}
	result = s.Runner.Run(CommandSpec{Name: "git", Args: []string{"rev-list", "--parents", "-n", "1", "HEAD"}, Dir: workspace})
	commits := strings.Fields(result.Stdout)
	if !commandSucceeded(result) || len(commits) != 2 || !validCommitOID(commits[0]) || commits[0] == head || commits[1] != head {
		return fmt.Errorf("delivery commit sole parent could not be verified; no push attempted")
	}
	commit := commits[0]
	if err := s.verifyDetachedHead(workspace, commit); err != nil {
		return err
	}
	if err := s.checkoutClean(workspace); err != nil {
		return fmt.Errorf("delivery commit created but worktree is not clean; no push attempted: %w", err)
	}
	if err := s.revalidateUnmanagedRun(workspace, identity, base, head, branch); err != nil {
		return fmt.Errorf("local commit %s retained; no push attempted: %w", commit, err)
	}
	// An explicit refspec alone does not override push.followTags or push.recurseSubmodules.
	result = s.Runner.Run(CommandSpec{Name: "git", Args: []string{"push", "--no-follow-tags", "--no-recurse-submodules", "--", "origin", commit + ":refs/heads/" + branch}, Dir: workspace})
	if !commandSucceeded(result) {
		return fmt.Errorf("push failed or is uncertain; local commit %s retained; remote branch may have been updated", commit)
	}
	payload, _ := json.Marshal(map[string]any{"title": fmt.Sprintf("Implement issue #%d", number), "body": fmt.Sprintf("Issue #%d の実装です。\n\nRefs #%d\n", number, number), "head": branch, "base": base, "draft": false})
	result = s.Runner.Run(CommandSpec{Name: "gh", Args: []string{"api", "repos/" + identity.String() + "/pulls", "--hostname", identity.Host(), "--method", "POST", "--input", "-"}, Dir: root, Stdin: payload})
	var pr struct{ Number int }
	if !commandSucceeded(result) || json.Unmarshal([]byte(result.Stdout), &pr) != nil || pr.Number <= 0 {
		return fmt.Errorf("partial or uncertain delivery: remote branch %s was pushed; PR creation failed or response was invalid; a PR may exist; local commit %s retained", branch, commit)
	}
	fmt.Fprintf(out, "Created PR #%d (head %s, base %s; Refs #%d)\n", pr.Number, branch, base, number)
	report := fmt.Sprintf("## iro delivery\n\nAuthor report (pre-delivery):\n\n%s\n", authorReport)
	result = s.Runner.Run(CommandSpec{Name: "gh", Args: []string{"pr", "comment", strconv.Itoa(pr.Number), "--repo", identity.Selector(), "--body", report}, Dir: root})
	if !commandSucceeded(result) {
		fmt.Fprintf(errOut, "warning: PR #%d was created, but delivery report comment failed\n", pr.Number)
	}
	return nil
}
