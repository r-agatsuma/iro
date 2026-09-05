package iro

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

type issue struct {
	Number   int            `json:"number"`
	Title    string         `json:"title"`
	Body     string         `json:"body"`
	URL      string         `json:"url"`
	Comments []issueComment `json:"comments"`
}

type issueComment struct {
	ID        string             `json:"id"`
	Author    issueCommentAuthor `json:"author"`
	CreatedAt string             `json:"createdAt"`
	Body      string             `json:"body"`
}

type issueCommentAuthor struct {
	Login string `json:"login"`
}

func (s *Service) requireExecutable(name string) error {
	if _, err := s.Runner.LookPath(name); err != nil {
		return fmt.Errorf("%s executable is unavailable; install it and retry", name)
	}
	return nil
}

func (s *Service) checkAuth(name string, args []string, dir string) error {
	result := s.Runner.Run(CommandSpec{Name: name, Args: args, Dir: dir})
	if !commandSucceeded(result) {
		return fmt.Errorf("%s authentication check failed", name)
	}
	return nil
}

func (s *Service) fetchIssue(root string, identity RepositoryIdentity, number int) (issue, error) {
	result := s.Runner.Run(CommandSpec{
		Name: "gh",
		Args: []string{"issue", "view", strconv.Itoa(number), "--repo", identity.String(), "--json", "number,title,body,url,comments"},
		Dir:  root,
	})
	if !commandSucceeded(result) {
		return issue{}, fmt.Errorf("could not read GitHub Issue #%d", number)
	}
	var target issue
	if err := json.Unmarshal([]byte(result.Stdout), &target); err != nil {
		return issue{}, fmt.Errorf("GitHub returned invalid Issue data")
	}
	if target.Number != number || target.URL == "" {
		return issue{}, fmt.Errorf("GitHub returned an unexpected Issue for #%d", number)
	}
	if err := sortIssueComments(target.Comments); err != nil {
		return issue{}, fmt.Errorf("GitHub returned invalid comments for Issue #%d: %w", number, err)
	}
	return target, nil
}

func sortIssueComments(comments []issueComment) error {
	type sortableComment struct {
		comment   issueComment
		createdAt time.Time
	}

	sorted := make([]sortableComment, len(comments))
	seenIDs := make(map[string]struct{}, len(comments))
	for i, comment := range comments {
		if strings.TrimSpace(comment.ID) == "" {
			return fmt.Errorf("comment %d has no immutable identifier", i+1)
		}
		createdAt, err := time.Parse(time.RFC3339Nano, comment.CreatedAt)
		if err != nil {
			return fmt.Errorf("comment %s has invalid createdAt", comment.ID)
		}
		if _, exists := seenIDs[comment.ID]; exists {
			return fmt.Errorf("comment identifier %s is duplicated", comment.ID)
		}
		seenIDs[comment.ID] = struct{}{}
		sorted[i] = sortableComment{comment: comment, createdAt: createdAt}
	}

	sort.Slice(sorted, func(i, j int) bool {
		if !sorted[i].createdAt.Equal(sorted[j].createdAt) {
			return sorted[i].createdAt.Before(sorted[j].createdAt)
		}
		return sorted[i].comment.ID < sorted[j].comment.ID
	})
	for i := range sorted {
		comments[i] = sorted[i].comment
	}
	return nil
}

func (s *Service) postResult(root string, identity RepositoryIdentity, number int, body string) error {
	result := s.Runner.Run(CommandSpec{
		Name: "gh",
		Args: []string{"issue", "comment", strconv.Itoa(number), "--repo", identity.String(), "--body", body},
		Dir:  root,
	})
	if !commandSucceeded(result) {
		return fmt.Errorf("could not post the run result to GitHub Issue #%d", number)
	}
	return nil
}
