package iro

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
)

const supportedGitHubHost = "github.com"

// RepositoryIdentity is the concrete github.com remote identity, not a
// cross-provider repository model or local Git lifecycle authority.
type RepositoryIdentity struct {
	Owner string
	Name  string
}

func (r RepositoryIdentity) Host() string {
	return supportedGitHubHost
}

func (r RepositoryIdentity) Selector() string {
	return r.Host() + "/" + r.String()
}

func (r RepositoryIdentity) String() string {
	return r.Owner + "/" + r.Name
}

func (r RepositoryIdentity) Canonical() string {
	return strings.ToLower(r.Owner) + "/" + strings.ToLower(r.Name)
}

// Key retains the GitHub path key for compatibility. Remote comparisons must
// use the concrete GitHub identity, never this path grouping value.
func (r RepositoryIdentity) Key() string {
	return string(githubRuntimeNamespace(r))
}

func parseGitHubRemote(raw string) (RepositoryIdentity, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return RepositoryIdentity{}, fmt.Errorf("configured remote URL is empty")
	}

	var host string
	var path string
	if strings.Contains(value, "://") {
		parsed, err := url.Parse(value)
		if err != nil {
			return RepositoryIdentity{}, fmt.Errorf("invalid remote URL: %w", err)
		}
		host = parsed.Hostname()
		path = parsed.Path
	} else if strings.HasPrefix(value, "git@") || strings.HasPrefix(value, "ssh@") {
		at := strings.IndexByte(value, '@')
		colon := strings.IndexByte(value, ':')
		if at < 0 || colon < at+1 {
			return RepositoryIdentity{}, fmt.Errorf("invalid SSH remote URL")
		}
		host = value[at+1 : colon]
		path = value[colon+1:]
	} else {
		return RepositoryIdentity{}, fmt.Errorf("remote URL is not a supported GitHub URL")
	}

	if !strings.EqualFold(host, supportedGitHubHost) {
		return RepositoryIdentity{}, fmt.Errorf("remote host %q is not github.com", host)
	}
	return parseGitHubRepositoryPath(strings.Trim(path, "/"))
}

// Use the same repository path normalization for remotes and CLI selectors.
func parseGitHubRepositoryPath(path string) (RepositoryIdentity, error) {
	path = strings.TrimSuffix(path, ".git")
	parts := strings.Split(path, "/")
	if len(parts) != 2 {
		return RepositoryIdentity{}, fmt.Errorf("expected exactly one GitHub OWNER/REPO")
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return RepositoryIdentity{}, fmt.Errorf("invalid GitHub owner or repository name")
		}
		for _, r := range part {
			if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.') {
				return RepositoryIdentity{}, fmt.Errorf("invalid character in GitHub owner or repository name")
			}
		}
	}
	return RepositoryIdentity{Owner: parts[0], Name: parts[1]}, nil
}

func cleanAbsolutePath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	return filepath.Clean(abs)
}
