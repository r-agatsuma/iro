package iro

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
)

const reviewPreflightQuery = `query($owner:String!,$name:String!,$number:Int!){repository(owner:$owner,name:$name){defaultBranchRef{name} pullRequest(number:$number){number title body url state isDraft baseRefName baseRefOid headRefName headRefOid headRepository{nameWithOwner} author{login} mergeable reviewDecision changedFiles additions deletions closingIssuesReferences(first:2){totalCount nodes{number repository{nameWithOwner}}}}}}`

const managedReviewPreflightQuery = `query($owner:String!,$name:String!,$number:Int!){repository(owner:$owner,name:$name){nameWithOwner pullRequest(number:$number){number title body url state isDraft baseRefName baseRefOid headRefName headRefOid headRepository{nameWithOwner} author{login} mergeable reviewDecision changedFiles additions deletions}}}`

type reviewPullRequest struct {
	Number         int
	Title          string
	Body           string
	URL            string
	State          string
	IsDraft        bool
	BaseRefName    string
	BaseRefOID     string
	HeadRefName    string
	HeadRefOID     string
	HeadRepository string
	Author         string
	Mergeable      string
	ReviewDecision string
	ChangedFiles   int
	Additions      int
	Deletions      int
	OriginIssue    int
}

type reviewContext struct {
	ChangedFiles       string
	Diff               string
	Conversation       string
	Reviews            string
	Checks             string
	InlineReviewThread string
}

// Review runs a fresh independent Reviewer and forwards its opaque response to the PR.
func (s *Service) Review(prNumber int, out io.Writer) error {
	return s.reviewWithOptions(prNumber, workerOptions{}, out)
}

func (s *Service) reviewWithModel(prNumber int, model string, out io.Writer) error {
	return s.reviewWithOptions(prNumber, workerOptions{Model: model}, out)
}

func (s *Service) reviewWithOptions(prNumber int, options workerOptions, out io.Writer) error {
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

	configPath := filepath.Join(root, "iro.toml")
	workflowPath := filepath.Join(root, "WORKFLOW.md")
	for _, path := range []string{configPath, workflowPath} {
		present, regular, inspectErr := s.fileState(path)
		if inspectErr != nil || !present || !regular {
			return fmt.Errorf("%s must be a readable regular file", filepath.Base(path))
		}
	}
	configData, err := s.FileSystem.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("iro.toml is missing or unreadable")
	}
	config, err := parseConfig(configData)
	if err != nil {
		return fmt.Errorf("iro.toml is invalid: %w", err)
	}
	workflowData, err := s.FileSystem.ReadFile(workflowPath)
	if err != nil {
		return fmt.Errorf("WORKFLOW.md is missing or unreadable")
	}
	return s.reviewTracker(root, config, prNumber, configData, workflowData, options, out)
}

func (s *Service) reviewTracker(root string, config Config, prNumber int, configData, workflowData []byte, options workerOptions, out io.Writer) error {
	switch config.TrackerType {
	case "github":
		return s.reviewGitHub(root, config, prNumber, configData, workflowData, options, out)
	default:
		return unsupportedTracker(config.TrackerType)
	}
}

func (s *Service) reviewGitHub(root string, config Config, prNumber int, configData, workflowData []byte, options workerOptions, out io.Writer) error {
	if err := requireCodexOperation(config.AgentType, "Review"); err != nil {
		return err
	}
	agent, err := s.selectAgent(config.AgentType)
	if err != nil {
		return err
	}
	identity, err := s.repositoryIdentity(root, config)
	if err != nil {
		return err
	}
	if err := checkGitHubContext(identity); err != nil {
		return err
	}
	if err := s.requireTrackerExecutable(config.TrackerType); err != nil {
		return err
	}
	if err := s.checkTrackerAuth(config.TrackerType, root, identity); err != nil {
		return err
	}

	target, err := s.inspectPRTarget(root, identity, prNumber, "review")
	if err != nil {
		return err
	}
	if !validCommitOID(target.BaseRefOID) {
		return fmt.Errorf("PR #%d base commit is invalid or unavailable; verify remote PR state and retry the review", prNumber)
	}
	if !validCommitOID(target.HeadRefOID) {
		return fmt.Errorf("PR #%d HEAD commit is invalid or unavailable; verify remote PR state and retry the review", prNumber)
	}
	origin, err := s.fetchIssue(root, identity, target.OriginIssue)
	if err != nil {
		return err
	}
	context, err := s.fetchReviewContext(root, identity, prNumber)
	if err != nil {
		return err
	}
	if err := agent.preflight(root); err != nil {
		return err
	}

	workspace, err := s.materializeReviewWorkspace(root, identity, target)
	if err != nil {
		return err
	}
	workspacePresent := true
	defer func() {
		if workspacePresent {
			_ = s.FileSystem.RemoveAll(workspace)
		}
	}()

	policy := managedReviewWorkerPolicy(workflowData, agent.resolvedModelIdentity(), target.BaseRefName, target.BaseRefOID, target.HeadRefOID)
	input := buildGitHubPRInput(identity, target, origin, configData, policy, context, "Origin Issue")
	result := agent.execute(workspace, policy.instructions, "Independently review the GitHub pull request supplied on stdin.", input, options.codexOptions())
	if cleanupErr := s.FileSystem.RemoveAll(workspace); cleanupErr != nil {
		return fmt.Errorf("could not remove disposable review workspace: %w", cleanupErr)
	}
	workspacePresent = false
	if !commandSucceeded(result) {
		return fmt.Errorf("Reviewer exited with status %d; no PR comment was posted", result.ExitCode)
	}
	if result.Stdout == "" {
		return fmt.Errorf("Reviewer returned no final response; no PR comment was posted")
	}
	if err := s.postReview(root, identity, prNumber, result.Stdout); err != nil {
		return err
	}
	fmt.Fprintf(out, "Posted independent review to PR #%d\n", prNumber)
	return nil
}

func (s *Service) inspectPRTarget(root string, identity RepositoryIdentity, number int, operation string) (reviewPullRequest, error) {
	return s.inspectPRTargetWithIssue(root, identity, number, operation, 0)
}

func (s *Service) inspectPRTargetWithIssue(root string, identity RepositoryIdentity, number int, operation string, specificationIssue int) (reviewPullRequest, error) {
	managedRelation := (operation == "review" || operation == "revise") && specificationIssue == 0
	query := reviewPreflightQuery
	if managedRelation {
		query = managedReviewPreflightQuery
	} else if specificationIssue > 0 {
		query = strings.Replace(query, "defaultBranchRef{name} ", "", 1)
		query = strings.Replace(query, " closingIssuesReferences(first:2){totalCount nodes{number repository{nameWithOwner}}}", "", 1)
	}
	result := s.Runner.Run(CommandSpec{
		Name: "gh",
		Args: []string{
			"api", "graphql", "--hostname", identity.Host(),
			"-f", "query=" + query,
			"-f", "owner=" + identity.Owner,
			"-f", "name=" + identity.Name,
			"-F", "number=" + strconv.Itoa(number),
		},
		Dir: root,
	})
	var response struct {
		Errors []json.RawMessage
		Data   struct {
			Repository *struct {
				NameWithOwner    string
				DefaultBranchRef *struct{ Name string }
				PullRequest      *struct {
					Number                  int
					Title                   string
					Body                    *string
					URL                     string
					State                   string
					IsDraft                 bool
					BaseRefName             string
					BaseRefOID              string `json:"baseRefOid"`
					HeadRefName             string
					HeadRefOID              string `json:"headRefOid"`
					HeadRepository          *struct{ NameWithOwner string }
					Author                  *struct{ Login string }
					Mergeable               string
					ReviewDecision          string
					ChangedFiles            int
					Additions               int
					Deletions               int
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
		return reviewPullRequest{}, fmt.Errorf("could not inspect PR #%d metadata; verify GitHub access", number)
	}
	repository := response.Data.Repository
	if !managedRelation && specificationIssue == 0 && (repository.DefaultBranchRef == nil || repository.DefaultBranchRef.Name == "") {
		return reviewPullRequest{}, fmt.Errorf("configured repository default branch is unavailable")
	}
	pr := repository.PullRequest
	if pr == nil || pr.Number != number || pr.URL == "" {
		return reviewPullRequest{}, fmt.Errorf("PR #%d does not exist in %s or is unreadable", number, identity.String())
	}
	if pr.State != "OPEN" {
		if operation == "review" {
			return reviewPullRequest{}, fmt.Errorf("PR #%d is not reviewable; it must be open", number)
		}
		return reviewPullRequest{}, fmt.Errorf("PR #%d must be open for %s", number, operation)
	}
	originNumber := specificationIssue
	if managedRelation {
		if !strings.EqualFold(repository.NameWithOwner, identity.String()) || pr.Body == nil {
			return reviewPullRequest{}, fmt.Errorf("PR #%d origin relation failed: current raw body is missing, unreadable or belongs to an unexpected repository", number)
		}
		relation, err := s.resolveGitHubOriginBody(root, identity, number, *pr.Body)
		if err != nil {
			return reviewPullRequest{}, err
		}
		if relation.State != githubOriginResolved {
			return reviewPullRequest{}, fmt.Errorf("PR #%d origin relation is %s; require exactly one validated local Issue in the current PR body; inspect the PR body", number, relation.State)
		}
		originNumber = relation.Issues[0]
	} else if specificationIssue == 0 {
		if pr.BaseRefName != repository.DefaultBranchRef.Name {
			return reviewPullRequest{}, fmt.Errorf("PR #%d targets %q, but %s requires default branch %q", number, pr.BaseRefName, operation, repository.DefaultBranchRef.Name)
		}
		relations := pr.ClosingIssuesReferences
		if relations.TotalCount != 1 || len(relations.Nodes) != 1 {
			return reviewPullRequest{}, fmt.Errorf("PR #%d must have exactly one origin Issue closing relation; found %d", number, relations.TotalCount)
		}
		origin := relations.Nodes[0]
		if origin.Number <= 0 || !strings.EqualFold(origin.Repository.NameWithOwner, identity.String()) {
			return reviewPullRequest{}, fmt.Errorf("PR #%d origin Issue must belong to configured repository %s", number, identity.String())
		}
		originNumber = origin.Number
	} else if pr.HeadRepository == nil || !strings.EqualFold(pr.HeadRepository.NameWithOwner, identity.String()) {
		return reviewPullRequest{}, fmt.Errorf("unmanaged %s requires PR #%d head repository to be %s", operation, number, identity.String())
	}
	if pr.HeadRefOID == "" {
		return reviewPullRequest{}, fmt.Errorf("PR #%d HEAD commit is unavailable", number)
	}

	target := reviewPullRequest{
		Number:         pr.Number,
		Title:          pr.Title,
		URL:            pr.URL,
		State:          pr.State,
		IsDraft:        pr.IsDraft,
		BaseRefName:    pr.BaseRefName,
		BaseRefOID:     pr.BaseRefOID,
		HeadRefName:    pr.HeadRefName,
		HeadRefOID:     pr.HeadRefOID,
		Mergeable:      pr.Mergeable,
		ReviewDecision: pr.ReviewDecision,
		ChangedFiles:   pr.ChangedFiles,
		Additions:      pr.Additions,
		Deletions:      pr.Deletions,
		OriginIssue:    originNumber,
	}
	if pr.Body != nil {
		target.Body = *pr.Body
	}
	if pr.HeadRepository != nil {
		target.HeadRepository = pr.HeadRepository.NameWithOwner
	}
	if pr.Author != nil {
		target.Author = pr.Author.Login
	}
	return target, nil
}

func (s *Service) fetchReviewContext(root string, identity RepositoryIdentity, number int) (reviewContext, error) {
	run := func(label string, args []string, requireJSON bool) (string, error) {
		result := s.Runner.Run(CommandSpec{Name: "gh", Args: args, Dir: root})
		if !commandSucceeded(result) {
			return "", fmt.Errorf("could not read %s for PR #%d", label, number)
		}
		if requireJSON && !json.Valid([]byte(result.Stdout)) {
			return "", fmt.Errorf("GitHub returned invalid %s for PR #%d", label, number)
		}
		return result.Stdout, nil
	}
	runPaginated := func(label, endpoint string) (string, error) {
		stdout, err := run(label, []string{"api", "--paginate", endpoint, "--hostname", identity.Host()}, false)
		if err != nil {
			return "", err
		}
		normalized, err := normalizeJSONPages(stdout)
		if err != nil {
			return "", fmt.Errorf("GitHub returned invalid %s for PR #%d: %w", label, number, err)
		}
		return normalized, nil
	}

	changedFiles, err := run("changed files", []string{"pr", "diff", strconv.Itoa(number), "--repo", identity.Selector(), "--name-only"}, false)
	if err != nil {
		return reviewContext{}, err
	}
	diff, err := run("diff", []string{"pr", "diff", strconv.Itoa(number), "--repo", identity.Selector()}, false)
	if err != nil {
		return reviewContext{}, err
	}
	conversation, err := runPaginated("conversation comments", "repos/"+identity.String()+"/issues/"+strconv.Itoa(number)+"/comments")
	if err != nil {
		return reviewContext{}, err
	}
	reviews, err := runPaginated("submitted reviews", "repos/"+identity.String()+"/pulls/"+strconv.Itoa(number)+"/reviews")
	if err != nil {
		return reviewContext{}, err
	}
	inlineComments, err := runPaginated("inline review comments", "repos/"+identity.String()+"/pulls/"+strconv.Itoa(number)+"/comments")
	if err != nil {
		return reviewContext{}, err
	}
	checks, err := run("checks", []string{"pr", "view", strconv.Itoa(number), "--repo", identity.Selector(), "--json", "statusCheckRollup"}, true)
	if err != nil {
		return reviewContext{}, err
	}
	return reviewContext{
		ChangedFiles:       changedFiles,
		Diff:               diff,
		Conversation:       conversation,
		Reviews:            reviews,
		Checks:             checks,
		InlineReviewThread: inlineComments,
	}, nil
}

// normalizeJSONPages wraps a stream of JSON arrays in a single array, preserving page order.
func normalizeJSONPages(stdout string) (string, error) {
	decoder := json.NewDecoder(strings.NewReader(stdout))
	var pages [][]json.RawMessage
	for {
		var page []json.RawMessage
		if err := decoder.Decode(&page); err == io.EOF {
			break
		} else if err != nil {
			return "", fmt.Errorf("could not decode page %d: %w", len(pages)+1, err)
		}
		if page == nil {
			return "", fmt.Errorf("page %d must be a JSON array", len(pages)+1)
		}
		pages = append(pages, page)
	}
	if len(pages) == 0 {
		return "", fmt.Errorf("expected at least one JSON array page")
	}
	normalized, err := json.Marshal(pages)
	return string(normalized), err
}

func (s *Service) materializeReviewWorkspace(root string, identity RepositoryIdentity, target reviewPullRequest) (string, error) {
	workspace, err := s.FileSystem.MkdirTemp("", "iro-review-*")
	if err != nil {
		return "", fmt.Errorf("could not create disposable review workspace: %w", err)
	}
	keep := false
	defer func() {
		if !keep {
			_ = s.FileSystem.RemoveAll(workspace)
		}
	}()

	result := s.Runner.Run(CommandSpec{
		Name: "gh",
		Args: []string{"repo", "clone", identity.Selector(), workspace, "--", "--no-checkout"},
		Dir:  root,
	})
	if !commandSucceeded(result) {
		return "", fmt.Errorf("could not clone configured repository into disposable review workspace")
	}
	result = s.Runner.Run(CommandSpec{
		Name: "gh",
		Args: []string{"pr", "checkout", strconv.Itoa(target.Number), "--repo", identity.Selector(), "--detach"},
		Dir:  workspace,
	})
	if !commandSucceeded(result) {
		return "", fmt.Errorf("could not materialize PR #%d HEAD in disposable review workspace", target.Number)
	}
	result = s.Runner.Run(CommandSpec{Name: "git", Args: []string{"rev-parse", "HEAD"}, Dir: workspace})
	if !commandSucceeded(result) || !strings.EqualFold(strings.TrimSpace(result.Stdout), target.HeadRefOID) {
		return "", fmt.Errorf("PR #%d HEAD changed or could not be verified; retry the review", target.Number)
	}
	keep = true
	return workspace, nil
}

func (s *Service) postReview(root string, identity RepositoryIdentity, number int, body string) error {
	result := s.Runner.Run(CommandSpec{
		Name: "gh",
		Args: []string{"pr", "comment", strconv.Itoa(number), "--repo", identity.Selector(), "--body", body},
		Dir:  root,
	})
	if !commandSucceeded(result) {
		return fmt.Errorf("could not post independent review to GitHub PR #%d", number)
	}
	return nil
}

func parsePullRequestNumber(value string) (int, error) {
	if value == "" {
		return 0, fmt.Errorf("pull request number must be a positive decimal integer")
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("pull request number must be a positive decimal integer")
		}
	}
	number, err := strconv.Atoi(value)
	if err != nil || number <= 0 {
		return 0, fmt.Errorf("pull request number must be a positive decimal integer")
	}
	return number, nil
}
