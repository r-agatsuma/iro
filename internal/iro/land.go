package iro

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

const landPreflightQuery = `query($owner:String!,$name:String!,$number:Int!){repository(owner:$owner,name:$name){defaultBranchRef{name} isArchived mergeCommitAllowed viewerPermission pullRequest(number:$number){number state isDraft baseRefName headRefName headRefOid headRepository{nameWithOwner} mergeable mergeStateStatus isMergeQueueEnabled closingIssuesReferences(first:2){totalCount nodes{number repository{nameWithOwner}}}}}}`

const unmanagedLandPreflightQuery = `query($owner:String!,$name:String!,$number:Int!){repository(owner:$owner,name:$name){isArchived mergeCommitAllowed viewerPermission pullRequest(number:$number){number state isDraft headRefOid headRepository{nameWithOwner} mergeable mergeStateStatus isMergeQueueEnabled}}}`

type landTarget struct {
	Number      int
	OriginIssue int
	HeadOID     string
}

// Land treats explicit invocation as Human authorization for one remote merge.
func (s *Service) Land(prNumber int, out io.Writer) error {
	return s.land(prNumber, false, out)
}

func (s *Service) landUnmanaged(prNumber int, out io.Writer) error {
	return s.land(prNumber, true, out)
}

func (s *Service) land(prNumber int, unmanaged bool, out io.Writer) error {
	if prNumber <= 0 {
		return fmt.Errorf("pull request number must be a positive decimal integer")
	}
	if err := s.requireGit(); err != nil {
		return err
	}
	root, err := s.gitRoot()
	if err != nil {
		return err
	}
	var identity RepositoryIdentity
	if unmanaged {
		identity, err = s.originIdentity(root)
	} else {
		var config Config
		config, err = s.loadProjectConfig(root)
		if err == nil {
			identity, err = s.repositoryIdentity(root, config)
		}
		if err == nil {
			err = checkGitHubContext(identity)
		}
	}
	if err != nil {
		return err
	}
	if err := s.requireExecutable("gh"); err != nil {
		return err
	}
	if err := s.checkAuth("gh", []string{"auth", "status", "--hostname", identity.Host()}, root); err != nil {
		return err
	}
	target, err := s.inspectLandTarget(root, identity, prNumber, unmanaged)
	if err != nil {
		return err
	}
	return s.mergeLandTarget(root, identity, target, out)
}

func (s *Service) inspectLandTarget(root string, identity RepositoryIdentity, number int, unmanaged bool) (landTarget, error) {
	query := landPreflightQuery
	if unmanaged {
		query = unmanagedLandPreflightQuery
	}
	result := s.Runner.Run(CommandSpec{
		Name: "gh",
		Args: []string{"api", "graphql", "--hostname", identity.Host(), "-f", "query=" + query, "-f", "owner=" + identity.Owner, "-f", "name=" + identity.Name, "-F", "number=" + strconv.Itoa(number)},
		Dir:  root,
	})
	var response struct {
		Errors []json.RawMessage
		Data   struct {
			Repository *struct {
				DefaultBranchRef   *struct{ Name string }
				IsArchived         *bool
				MergeCommitAllowed *bool
				ViewerPermission   string
				PullRequest        *struct {
					Number                  int
					State                   string
					IsDraft                 *bool
					BaseRefName             string
					HeadRefName             string
					HeadRefOID              string `json:"headRefOid"`
					HeadRepository          *struct{ NameWithOwner string }
					Mergeable               string
					MergeStateStatus        string
					IsMergeQueueEnabled     *bool
					ClosingIssuesReferences struct {
						TotalCount int
						Nodes      []struct {
							Number     int
							Repository struct{ NameWithOwner string }
						}
					}
				}
			}
		}
	}
	if !commandSucceeded(result) || json.Unmarshal([]byte(result.Stdout), &response) != nil || len(response.Errors) > 0 || response.Data.Repository == nil {
		return landTarget{}, fmt.Errorf("could not inspect PR #%d and repository merge policy; verify GitHub access", number)
	}
	repo := response.Data.Repository
	if !unmanaged && (repo.DefaultBranchRef == nil || repo.DefaultBranchRef.Name == "") {
		return landTarget{}, fmt.Errorf("configured repository default branch is unavailable; inspect remote state")
	}
	pr := repo.PullRequest
	if pr == nil || pr.Number != number {
		return landTarget{}, fmt.Errorf("PR #%d does not exist in %s or is unreadable", number, identity.String())
	}
	if pr.State != "OPEN" {
		return landTarget{}, fmt.Errorf("PR #%d must be open for land; inspect the selected PR", number)
	}
	if pr.IsDraft == nil {
		return landTarget{}, fmt.Errorf("PR #%d draft state is unavailable; inspect remote state", number)
	}
	if *pr.IsDraft {
		return landTarget{}, fmt.Errorf("pull request #%d is a draft and cannot be landed\nremediation: mark the pull request ready for review, then retry `%s`", number, landInvocation(number, unmanaged))
	}
	originIssue := 0
	branch := ""
	if unmanaged {
		if pr.HeadRepository == nil || !strings.EqualFold(pr.HeadRepository.NameWithOwner, identity.String()) {
			return landTarget{}, fmt.Errorf("PR #%d head must belong to origin repository %s", number, identity.String())
		}
	} else {
		if pr.BaseRefName != repo.DefaultBranchRef.Name {
			return landTarget{}, fmt.Errorf("PR #%d targets %q, but land requires default branch %q; inspect the selected PR", number, pr.BaseRefName, repo.DefaultBranchRef.Name)
		}
		relations := pr.ClosingIssuesReferences
		if relations.TotalCount != 1 || len(relations.Nodes) != 1 {
			return landTarget{}, fmt.Errorf("PR #%d must have exactly one origin Issue closing relation; inspect remote state", number)
		}
		origin := relations.Nodes[0]
		originIssue = origin.Number
		if origin.Number <= 0 || !strings.EqualFold(origin.Repository.NameWithOwner, identity.String()) {
			return landTarget{}, fmt.Errorf("PR #%d origin Issue must belong to configured repository %s; inspect its closing relation", number, identity.String())
		}
		branch = fmt.Sprintf("iro/issue-%d", origin.Number)
		if pr.HeadRefName != branch || pr.HeadRepository == nil || !strings.EqualFold(pr.HeadRepository.NameWithOwner, identity.String()) {
			return landTarget{}, fmt.Errorf("land requires PR #%d head to be %s in configured repository %s; inspect the selected PR", number, branch, identity.String())
		}
	}
	if !validCommitOID(pr.HeadRefOID) {
		return landTarget{}, fmt.Errorf("PR #%d HEAD commit is invalid or unavailable; inspect remote state", number)
	}
	if repo.IsArchived == nil || *repo.IsArchived || repo.MergeCommitAllowed == nil || !*repo.MergeCommitAllowed {
		return landTarget{}, fmt.Errorf("repository does not allow normal merge commits or its policy is unavailable; inspect repository settings")
	}
	switch repo.ViewerPermission {
	case "WRITE", "MAINTAIN", "ADMIN":
	default:
		return landTarget{}, fmt.Errorf("repository write permission is unavailable; verify GitHub access before retrying land")
	}
	if pr.IsMergeQueueEnabled == nil || *pr.IsMergeQueueEnabled {
		return landTarget{}, fmt.Errorf("PR #%d requires a merge queue or its queue policy is unavailable; land only supports immediate merges; inspect repository policy", number)
	}
	if pr.Mergeable != "MERGEABLE" {
		return landTarget{}, fmt.Errorf("PR #%d mergeability is %q; resolve conflicts or wait for GitHub to determine mergeability, then retry land", number, pr.Mergeable)
	}
	// The merge endpoint enforces any up-to-date requirement for BEHIND heads.
	// Never reinterpret BLOCKED using the viewer's admin permissions.
	switch pr.MergeStateStatus {
	case "CLEAN", "UNSTABLE", "HAS_HOOKS", "BEHIND":
	default:
		return landTarget{}, fmt.Errorf("PR #%d merge state %q does not allow land; inspect repository rules and required checks/reviews, then retry after they are satisfied", number, pr.MergeStateStatus)
	}
	if !unmanaged {
		base, err := s.inspectDeliveryPRs(root, identity, originIssue, branch, number)
		if err != nil {
			return landTarget{}, err
		}
		if base != pr.BaseRefName {
			return landTarget{}, fmt.Errorf("repository default branch changed during inspection; inspect remote state and retry")
		}
	}
	return landTarget{Number: number, OriginIssue: originIssue, HeadOID: pr.HeadRefOID}, nil
}

func (s *Service) mergeLandTarget(root string, identity RepositoryIdentity, target landTarget, out io.Writer) error {
	// The synchronous REST merge endpoint checks sha atomically. Do not request
	// auto-merge or branch deletion, prompt, or change the local checkout.
	payload, _ := json.Marshal(map[string]string{"sha": target.HeadOID, "merge_method": "merge"})
	result := s.Runner.Run(CommandSpec{
		Name: "gh",
		Args: []string{"api", "repos/" + identity.String() + "/pulls/" + strconv.Itoa(target.Number) + "/merge", "--hostname", identity.Host(), "--method", "PUT", "--input", "-"},
		Dir:  root, Stdin: payload,
	})
	var response struct {
		Merged bool
		SHA    string
	}
	if !commandSucceeded(result) || json.Unmarshal([]byte(result.Stdout), &response) != nil || !response.Merged || !validCommitOID(response.SHA) {
		return fmt.Errorf("merge of PR #%d at validated HEAD %s failed or could not be confirmed; inspect the remote PR, current HEAD, and repository rules before explicitly retrying `%s`; no automatic retry was attempted", target.Number, target.HeadOID, landInvocation(target.Number, target.OriginIssue == 0))
	}
	if target.OriginIssue == 0 {
		fmt.Fprintf(out, "Landed PR #%d with merge commit %s\n", target.Number, response.SHA)
	} else {
		fmt.Fprintf(out, "Landed PR #%d for Issue #%d with merge commit %s\n", target.Number, target.OriginIssue, response.SHA)
	}
	fmt.Fprintln(out, "\nSync your local default branch with the remote before the next iro run.")
	fmt.Fprintln(out, "For example: git pull")
	return nil
}

func landInvocation(number int, unmanaged bool) string {
	command := fmt.Sprintf("iro land %d", number)
	if unmanaged {
		command += " --unmanaged"
	}
	return command
}
