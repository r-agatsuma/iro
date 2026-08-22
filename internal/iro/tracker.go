package iro

import (
	"encoding/json"
	"fmt"
	"strconv"
)

type issue struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	URL    string `json:"url"`
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
		Args: []string{"issue", "view", strconv.Itoa(number), "--repo", identity.String(), "--json", "number,title,body,url"},
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
	return target, nil
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
