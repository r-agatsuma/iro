package iro

import (
	"fmt"
	"strings"
)

// These renderers retain GitHub domain data and operation-specific context.
// Other concrete trackers can render their own input for codexRuntime.execute.
func buildGitHubIssueInput(identity RepositoryIdentity, target issue) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "Repository: %s\nIssue number: %d\nIssue title: %s\nIssue URL: %s\n\nIssue body:\n", identity.String(), target.Number, target.Title, target.URL)
	builder.WriteString(target.Body)
	builder.WriteString("\n\nIssue comments (ordered by createdAt, then immutable ID):\n")
	if len(target.Comments) == 0 {
		builder.WriteString("(none)\n")
		return builder.String()
	}
	for i, comment := range target.Comments {
		fmt.Fprintf(&builder, "\nComment %d:\nID: %s\nAuthor: %s\nCreated at: %s\nBody:\n", i+1, comment.ID, normalizedCommentAuthor(comment), comment.CreatedAt)
		builder.WriteString(comment.Body)
		builder.WriteString("\n")
	}
	return builder.String()
}

func normalizedCommentAuthor(comment issueComment) string {
	if strings.TrimSpace(comment.Author.Login) == "" {
		return "(unknown)"
	}
	return comment.Author.Login
}

func buildGitHubPRInput(identity RepositoryIdentity, target reviewPullRequest, origin issue, configData []byte, policy workerPolicy, context reviewContext, issueLabel string) string {
	unknown := func(value string) string {
		if strings.TrimSpace(value) == "" {
			return "(unknown)"
		}
		return value
	}
	var builder strings.Builder
	fmt.Fprintf(&builder, "Repository: %s\n\n", identity.String())
	if configData != nil {
		fmt.Fprintf(&builder, "Project configuration (iro.toml):\n%s\n", configData)
	}
	builder.WriteString(policy.inputContext)
	fmt.Fprintf(&builder, "%s:\nNumber: %d\nTitle: %s\nURL: %s\nBody:\n%s\n\nIssue comments (ordered by createdAt, then immutable ID):\n", issueLabel, origin.Number, origin.Title, origin.URL, origin.Body)
	if len(origin.Comments) == 0 {
		builder.WriteString("(none)\n")
	} else {
		for i, comment := range origin.Comments {
			fmt.Fprintf(&builder, "\nComment %d:\nID: %s\nAuthor: %s\nCreated at: %s\nBody:\n%s\n", i+1, comment.ID, normalizedCommentAuthor(comment), comment.CreatedAt, comment.Body)
		}
	}
	fmt.Fprintf(&builder, "\nPull request metadata:\nNumber: %d\nTitle: %s\nURL: %s\nState: %s\nDraft: %t\nBase: %s\nHead: %s\nHead OID: %s\nHead repository: %s\nAuthor: %s\nMergeable: %s\nReview decision: %s\nChanged files: %d\nAdditions: %d\nDeletions: %d\n%s: #%d\n\nPull request body:\n%s\n", target.Number, target.Title, target.URL, target.State, target.IsDraft, target.BaseRefName, target.HeadRefName, target.HeadRefOID, unknown(target.HeadRepository), unknown(target.Author), target.Mergeable, unknown(target.ReviewDecision), target.ChangedFiles, target.Additions, target.Deletions, issueLabel, target.OriginIssue, target.Body)
	fmt.Fprintf(&builder, "\nChanged file names:\n%s\nPull request diff:\n%s\nPull request conversation comments (GitHub JSON):\n%s\nSubmitted reviews (GitHub JSON):\n%s\nInline review comments (GitHub JSON):\n%s\nChecks (GitHub JSON):\n%s\n", context.ChangedFiles, context.Diff, context.Conversation, context.Reviews, context.InlineReviewThread, context.Checks)
	return builder.String()
}
