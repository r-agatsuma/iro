package iro

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const githubOriginBodyQuery = `query($owner:String!,$name:String!,$number:Int!){repository(owner:$owner,name:$name){nameWithOwner pullRequest(number:$number){number body}}}`
const githubOriginCandidateQuery = `query($owner:String!,$name:String!,$number:Int!){repository(owner:$owner,name:$name){nameWithOwner issueOrPullRequest(number:$number){__typename ... on Issue{number repository{nameWithOwner}}}}}`

type githubOriginState string

const (
	githubOriginResolved   githubOriginState = "resolved"
	githubOriginUnresolved githubOriginState = "unresolved"
	githubOriginAmbiguous  githubOriginState = "ambiguous"
)

// Issues contains only validated local Issue numbers, in first-occurrence order.
// An error is a relation failure, never a partial resolution.
type githubOriginResolution struct {
	State  githubOriginState
	Issues []int
}

// resolveGitHubOrigin reads the selected PR's current raw body on every call.
// It does not infer origin from other metadata or persist the result.
func (s *Service) resolveGitHubOrigin(root string, identity RepositoryIdentity, prNumber int) (githubOriginResolution, error) {
	result := s.queryGitHubOrigin(root, identity, prNumber, githubOriginBodyQuery)
	var response struct {
		Errors []json.RawMessage
		Data   struct {
			Repository *struct {
				NameWithOwner string
				PullRequest   *struct {
					Number int
					Body   *string
				}
			}
		}
	}
	if !commandSucceeded(result) || json.Unmarshal([]byte(result.Stdout), &response) != nil || len(response.Errors) > 0 || response.Data.Repository == nil {
		return githubOriginResolution{}, fmt.Errorf("PR #%d origin relation failed: could not read current raw body in %s; verify GitHub access", prNumber, identity.String())
	}
	repo := response.Data.Repository
	if !strings.EqualFold(repo.NameWithOwner, identity.String()) || repo.PullRequest == nil || repo.PullRequest.Number != prNumber || repo.PullRequest.Body == nil {
		return githubOriginResolution{}, fmt.Errorf("PR #%d origin relation failed: current raw body is missing, unreadable or belongs to an unexpected PR/repository", prNumber)
	}
	return s.resolveGitHubOriginBody(root, identity, prNumber, *repo.PullRequest.Body)
}

// resolveGitHubOriginBody also accepts a current raw body read with PR metadata.
// Callers must validate the PR/repository identity and reject missing/null bodies.
func (s *Service) resolveGitHubOriginBody(root string, identity RepositoryIdentity, prNumber int, body string) (githubOriginResolution, error) {
	numbers, err := extractGitHubOriginCandidates(body)
	if err != nil {
		return githubOriginResolution{}, fmt.Errorf("PR #%d origin relation failed: %w", prNumber, err)
	}
	for _, number := range numbers {
		if err := s.validateGitHubOriginCandidate(root, identity, number); err != nil {
			return githubOriginResolution{}, fmt.Errorf("PR #%d origin relation failed: %w", prNumber, err)
		}
	}
	state := githubOriginAmbiguous
	switch len(numbers) {
	case 0:
		state = githubOriginUnresolved
	case 1:
		state = githubOriginResolved
	}
	return githubOriginResolution{State: state, Issues: numbers}, nil
}

// extractGitHubOriginCandidates implements a raw lexical protocol, not Markdown.
func extractGitHubOriginCandidates(body string) ([]int, error) {
	var numbers []int
	seen := map[int]bool{}
	for start, r := range body {
		if r != '#' {
			continue
		}
		if start > 0 {
			prefix, _ := utf8.DecodeLastRuneInString(body[:start])
			if !unicode.IsSpace(prefix) {
				continue
			}
		}
		end := start + 1
		if end == len(body) || body[end] < '1' || body[end] > '9' {
			continue
		}
		for end < len(body) && body[end] >= '0' && body[end] <= '9' {
			end++
		}
		if end < len(body) {
			suffix, _ := utf8.DecodeRuneInString(body[end:])
			if !unicode.IsSpace(suffix) && !strings.ContainsRune(".,;:!?)]}", suffix) {
				continue
			}
		}
		// GitHub's GraphQL number argument is a signed 32-bit Int, even on
		// platforms where Go's int is wider. Never discard an overflowing token.
		number, err := strconv.ParseInt(body[start+1:end], 10, 32)
		if err != nil {
			return nil, fmt.Errorf("local Issue token exceeds GitHub's numeric range")
		}
		if !seen[int(number)] {
			seen[int(number)] = true
			numbers = append(numbers, int(number))
		}
	}
	return numbers, nil
}

func (s *Service) validateGitHubOriginCandidate(root string, identity RepositoryIdentity, number int) error {
	result := s.queryGitHubOrigin(root, identity, number, githubOriginCandidateQuery)
	var response struct {
		Errors []json.RawMessage
		Data   struct {
			Repository *struct {
				NameWithOwner      string
				IssueOrPullRequest *struct {
					TypeName   string `json:"__typename"`
					Number     int
					Repository *struct{ NameWithOwner string }
				}
			}
		}
	}
	if !commandSucceeded(result) || json.Unmarshal([]byte(result.Stdout), &response) != nil || len(response.Errors) > 0 || response.Data.Repository == nil {
		return fmt.Errorf("could not validate local Issue #%d in %s; verify GitHub access", number, identity.String())
	}
	repo := response.Data.Repository
	candidate := repo.IssueOrPullRequest
	if !strings.EqualFold(repo.NameWithOwner, identity.String()) || candidate == nil || candidate.TypeName != "Issue" || candidate.Number != number || candidate.Repository == nil || !strings.EqualFold(candidate.Repository.NameWithOwner, identity.String()) {
		return fmt.Errorf("local token #%d is not a readable Issue in %s; inspect the PR body", number, identity.String())
	}
	return nil
}

func (s *Service) queryGitHubOrigin(root string, identity RepositoryIdentity, number int, query string) CommandResult {
	return s.Runner.Run(CommandSpec{
		Name: "gh",
		Args: []string{"api", "graphql", "--hostname", identity.Host(), "-f", "query=" + query, "-f", "owner=" + identity.Owner, "-f", "name=" + identity.Name, "-F", "number=" + strconv.Itoa(number)},
		Dir:  root,
	})
}

// githubRunBody keeps worker text out of the raw-body origin contract.
func githubRunBody(number int, unmanaged bool) string {
	keyword := "Closes"
	if unmanaged {
		keyword = "Refs"
	}
	return fmt.Sprintf("Issue #%d の実装です。\n\n%s #%d\n", number, keyword, number)
}
