package iro

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

func (s *Service) reviewUnmanaged(number, specificationIssue int, options workerOptions, out, errOut io.Writer) error {
	if number <= 0 || specificationIssue <= 0 {
		return fmt.Errorf("PR and Issue numbers must be positive decimal integers")
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
	if err := s.requireExecutable("gh"); err != nil {
		return err
	}
	if err := s.checkAuth("gh", []string{"auth", "status", "--hostname", identity.Host()}, root); err != nil {
		return err
	}
	target, err := s.inspectPRTargetWithIssue(root, identity, number, "review", specificationIssue)
	if err != nil {
		return err
	}
	if !validCommitOID(target.HeadRefOID) || !validCommitOID(target.BaseRefOID) {
		return fmt.Errorf("PR #%d has invalid or unavailable commit OIDs", number)
	}
	origin, err := s.fetchIssue(root, identity, specificationIssue)
	if err != nil {
		return err
	}
	context, err := s.fetchReviewContext(root, identity, number)
	if err != nil {
		return err
	}
	if err := s.requireExecutable("codex"); err != nil {
		return err
	}
	if err := s.checkAuth("codex", []string{"login", "status"}, root); err != nil {
		return err
	}
	// Fetch the recorded commit, never a moving branch or PR ref.
	result := s.Runner.Run(CommandSpec{Name: "git", Args: []string{"fetch", "--no-tags", "--no-write-fetch-head", "--", "origin", target.HeadRefOID}, Dir: root})
	if !commandSucceeded(result) {
		return fmt.Errorf("could not fetch PR #%d snapshot %s from origin", number, target.HeadRefOID)
	}
	workspace, err := s.createDetachedWorktree(root, identity, fmt.Sprintf("review-pr-%d-*", number), target.HeadRefOID)
	if workspace == "" {
		return err
	}
	defer func() {
		if err := s.removeUnmanagedWorktree(root, workspace); err != nil {
			fmt.Fprintf(errOut, "warning: review workspace cleanup failed; retained path %s: %v; inspect manually; do not retry the comment automatically\n", workspace, err)
		}
	}()
	if err != nil {
		return err
	}
	if err := s.verifyDetachedHead(workspace, target.HeadRefOID); err != nil {
		return err
	}
	options.Unmanaged = true
	result = s.runReviewer(workspace, identity, target, origin, nil, []byte("Built-in unmanaged read-only Reviewer policy; project files are not policy inputs."), context, options)
	if !commandSucceeded(result) {
		return fmt.Errorf("Reviewer exited with status %d; no PR comment was posted", result.ExitCode)
	}
	if result.Stdout == "" {
		return fmt.Errorf("Reviewer returned no final response; no PR comment was posted")
	}
	if err := s.postUnmanagedReview(root, identity, number, result.Stdout); err != nil {
		return err
	}
	fmt.Fprintf(out, "Posted independent review to PR #%d\n", number)
	return nil
}

func (s *Service) postUnmanagedReview(root string, identity RepositoryIdentity, number int, body string) error {
	payload, _ := json.Marshal(map[string]string{"body": body})
	result := s.Runner.Run(CommandSpec{Name: "gh", Args: []string{"api", "repos/" + identity.String() + "/issues/" + strconv.Itoa(number) + "/comments", "--hostname", identity.Host(), "--method", "POST", "--include", "--input", "-"}, Dir: root, Stdin: payload})
	// Require a receipt; a transport error or malformed success response may hide a
	// completed write. Never infer absence or retry from a process exit status.
	header, response, found := strings.Cut(strings.ReplaceAll(result.Stdout, "\r\n", "\n"), "\n\n")
	fields := strings.Fields(strings.SplitN(header, "\n", 2)[0])
	status := 0
	if len(fields) >= 2 && strings.HasPrefix(fields[0], "HTTP/") {
		status, _ = strconv.Atoi(fields[1])
	}
	var receipt struct {
		ID int64 `json:"id"`
	}
	if commandSucceeded(result) && found && status == 201 && json.Unmarshal([]byte(response), &receipt) == nil && receipt.ID > 0 {
		return nil
	}
	if status >= 400 && status < 500 && status != 408 {
		return fmt.Errorf("could not post independent review to PR #%d: GitHub rejected the comment (HTTP %d)", number, status)
	}
	return fmt.Errorf("PR #%d comment outcome is uncertain; a comment may exist; inspect PR #%d before retrying", number, number)
}
