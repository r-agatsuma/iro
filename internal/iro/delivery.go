package iro

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

const deliveryQuery = `query($owner:String!,$name:String!,$cursor:String){repository(owner:$owner,name:$name){defaultBranchRef{name} pullRequests(first:100,after:$cursor,states:[OPEN,CLOSED,MERGED]){nodes{number headRefName headRepository{nameWithOwner} closingIssuesReferences(first:100){totalCount nodes{number repository{nameWithOwner}}}} pageInfo{hasNextPage endCursor}}}}`

// deliveryBase rejects any prior relation, including abandoned PRs, without using authorship.
func (s *Service) deliveryBase(root string, identity RepositoryIdentity, number int, branch string) (string, error) {
	return s.inspectDeliveryPRs(root, identity, number, branch, 0)
}

// A nonzero activePR requires that PR to be the only active delivery relation.
func (s *Service) inspectDeliveryPRs(root string, identity RepositoryIdentity, number int, branch string, activePR int) (string, error) {
	cursor, base := "", ""
	seen := map[string]bool{}
	query := deliveryQuery
	foundTarget := false
	if activePR != 0 {
		query = strings.Replace(query, "states:[OPEN,CLOSED,MERGED]", "states:[OPEN]", 1)
	}
	for {
		args := []string{"api", "graphql", "-f", "query=" + query, "-f", "owner=" + identity.Owner, "-f", "name=" + identity.Name}
		if cursor != "" {
			args = append(args, "-f", "cursor="+cursor)
		}
		result := s.Runner.Run(CommandSpec{Name: "gh", Args: args, Dir: root})
		var response struct {
			Errors []json.RawMessage
			Data   struct {
				Repository *struct {
					DefaultBranchRef *struct{ Name string }
					PullRequests     *struct {
						Nodes []struct {
							Number                  int
							HeadRefName             string
							HeadRepository          *struct{ NameWithOwner string }
							ClosingIssuesReferences *struct {
								TotalCount *int
								Nodes      []struct {
									Number     int
									Repository struct{ NameWithOwner string }
								}
							}
						}
						PageInfo *struct {
							HasNextPage *bool
							EndCursor   string
						}
					}
				}
			}
		}
		if !commandSucceeded(result) || json.Unmarshal([]byte(result.Stdout), &response) != nil || len(response.Errors) > 0 || response.Data.Repository == nil {
			return "", fmt.Errorf("could not inspect repository default branch and delivery PRs; verify GitHub access")
		}
		repo := response.Data.Repository
		if repo.DefaultBranchRef == nil || repo.DefaultBranchRef.Name == "" || repo.PullRequests == nil {
			return "", fmt.Errorf("repository default branch or PR relation is unavailable")
		}
		if base != "" && base != repo.DefaultBranchRef.Name {
			return "", fmt.Errorf("repository default branch changed during inspection; retry")
		}
		base = repo.DefaultBranchRef.Name
		if repo.PullRequests.Nodes == nil || repo.PullRequests.PageInfo == nil || repo.PullRequests.PageInfo.HasNextPage == nil {
			return "", fmt.Errorf("incomplete PR pagination data; cannot verify delivery relation")
		}
		for _, pr := range repo.PullRequests.Nodes {
			if pr.Number <= 0 || pr.HeadRefName == "" || pr.ClosingIssuesReferences == nil || pr.ClosingIssuesReferences.Nodes == nil || pr.ClosingIssuesReferences.TotalCount == nil || *pr.ClosingIssuesReferences.TotalCount != len(pr.ClosingIssuesReferences.Nodes) {
				return "", fmt.Errorf("incomplete PR relation data; inspect repository PRs manually")
			}
			if activePR != 0 && pr.HeadRefName == branch && (pr.HeadRepository == nil || pr.HeadRepository.NameWithOwner == "") {
				return "", fmt.Errorf("PR #%d canonical branch repository is unknown; inspect remote state", pr.Number)
			}
			related := pr.HeadRefName == branch && pr.HeadRepository != nil && strings.EqualFold(pr.HeadRepository.NameWithOwner, identity.String())
			for _, origin := range pr.ClosingIssuesReferences.Nodes {
				if origin.Number <= 0 || origin.Repository.NameWithOwner == "" {
					return "", fmt.Errorf("incomplete Issue closing relation at PR #%d; inspect remote state", pr.Number)
				}
				if origin.Number == number && strings.EqualFold(origin.Repository.NameWithOwner, identity.String()) {
					related = true
				}
			}
			if activePR != 0 && pr.Number == activePR {
				relations := pr.ClosingIssuesReferences.Nodes
				if foundTarget || pr.HeadRefName != branch || pr.HeadRepository == nil || !strings.EqualFold(pr.HeadRepository.NameWithOwner, identity.String()) || len(relations) != 1 || relations[0].Number != number || !strings.EqualFold(relations[0].Repository.NameWithOwner, identity.String()) {
					return "", fmt.Errorf("active PR #%d delivery relation changed or is ambiguous; inspect remote state", activePR)
				}
				foundTarget = true
				continue
			}
			if related {
				if activePR != 0 {
					return "", fmt.Errorf("multiple active delivery PRs for Issue #%d or %s: PR #%d also exists; resolve the relation manually", number, branch, pr.Number)
				}
				return "", fmt.Errorf("delivery relation already exists at PR #%d; inspect it manually and use iro revise for an active PR", pr.Number)
			}
		}
		page := repo.PullRequests.PageInfo
		if !*page.HasNextPage {
			if activePR != 0 && !foundTarget {
				return "", fmt.Errorf("PR #%d is missing from active delivery relations; inspect remote state", activePR)
			}
			return base, nil
		}
		if page.EndCursor == "" || seen[page.EndCursor] {
			return "", fmt.Errorf("invalid PR pagination; cannot verify delivery relation")
		}
		cursor = page.EndCursor
		seen[cursor] = true
	}
}

func (s *Service) verifyDeliveryCheckout(root, base string) error {
	result := s.Runner.Run(CommandSpec{Name: "git", Args: []string{"symbolic-ref", "--quiet", "HEAD"}, Dir: root})
	if !commandSucceeded(result) || strings.TrimSpace(result.Stdout) != "refs/heads/"+base {
		return fmt.Errorf("run requires the repository default branch %q; switch to it manually (detached HEAD is not supported)", base)
	}
	return nil
}

func (s *Service) verifyPushRemote(root, remote string, identity RepositoryIdentity) error {
	result := s.Runner.Run(CommandSpec{Name: "git", Args: []string{"remote", "get-url", "--push", "--all", remote}, Dir: root})
	urls := strings.Fields(result.Stdout)
	if !commandSucceeded(result) || len(urls) != 1 {
		return fmt.Errorf("configured remote push destination is unavailable or ambiguous")
	}
	destination, err := parseGitHubRemote(urls[0])
	if err != nil || destination.Canonical() != identity.Canonical() {
		return fmt.Errorf("configured remote push destination does not match the tracker repository")
	}
	result = s.Runner.Run(CommandSpec{Name: "git", Args: []string{"ls-remote", "--", remote}, Dir: root})
	if !commandSucceeded(result) {
		return fmt.Errorf("configured Git remote is inaccessible; verify remote authentication")
	}
	return nil
}

func (s *Service) deliver(root, workspace string, identity RepositoryIdentity, number int, remote, branch, base string, out, errOut io.Writer) error {
	// Stage only worker changes in the previously verified clean, owned worktree.
	result := s.Runner.Run(CommandSpec{Name: "git", Args: []string{"add", "--all"}, Dir: workspace})
	if !commandSucceeded(result) {
		return fmt.Errorf("could not stage worker changes; worktree and possible staged changes kept at %s", workspace)
	}
	result = s.Runner.Run(CommandSpec{Name: "git", Args: []string{"diff", "--cached", "--quiet", "--exit-code"}, Dir: workspace})
	if result.ExitCode == 0 && result.Err == nil {
		return fmt.Errorf("worker produced no committable changes; no commit, push or PR created; worktree kept at %s", workspace)
	}
	if result.ExitCode != 1 {
		return fmt.Errorf("could not inspect staged changes; worktree and index kept at %s", workspace)
	}
	result = s.Runner.Run(CommandSpec{Name: "git", Args: []string{"commit", "-m", fmt.Sprintf("Implement issue #%d", number)}, Dir: workspace})
	if !commandSucceeded(result) {
		return fmt.Errorf("commit failed; worktree and index kept at %s; inspect Git identity and hooks before retrying", workspace)
	}
	result = s.Runner.Run(CommandSpec{Name: "git", Args: []string{"push", "--", remote, "refs/heads/" + branch + ":refs/heads/" + branch}, Dir: workspace})
	if !commandSucceeded(result) {
		return fmt.Errorf("push failed; local commit remains on %s at %s; remote branch may have been updated, inspect it before retrying", branch, workspace)
	}
	// Use a fixed body, so worker text cannot introduce additional closing relations.
	payload, _ := json.Marshal(map[string]any{"title": fmt.Sprintf("Implement issue #%d", number), "body": fmt.Sprintf("Issue #%d の実装です。\n\nCloses #%d\n", number, number), "head": branch, "base": base, "draft": false})
	result = s.Runner.Run(CommandSpec{Name: "gh", Args: []string{"api", "repos/" + identity.String() + "/pulls", "--method", "POST", "--input", "-"}, Dir: root, Stdin: payload})
	var pr struct {
		Number int `json:"number"`
	}
	if !commandSucceeded(result) || json.Unmarshal([]byte(result.Stdout), &pr) != nil || pr.Number <= 0 {
		return fmt.Errorf("PR creation failed or response was invalid; remote branch %s was pushed and local commit remains at %s; a PR may exist, inspect remote state before retrying", branch, workspace)
	}
	fmt.Fprintf(out, "Created PR #%d\nLand: iro land %d\n", pr.Number, pr.Number)
	hint := fmt.Sprintf("## iro delivery\n\nLand: `iro land %d`\n", pr.Number)
	result = s.Runner.Run(CommandSpec{Name: "gh", Args: []string{"pr", "comment", strconv.Itoa(pr.Number), "--repo", identity.String(), "--body", hint}, Dir: root})
	if !commandSucceeded(result) {
		fmt.Fprintf(errOut, "warning: PR #%d was created, but delivery hint comment failed; Land: iro land %d\n", pr.Number, pr.Number)
	}
	return nil
}
