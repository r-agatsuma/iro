package iro

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// checkGitHubContext validates the inherited environment without changing it.
func checkGitHubContext(identity RepositoryIdentity) error {
	var problems []error
	if host := os.Getenv("GH_HOST"); host != "" && !strings.EqualFold(host, identity.Host()) {
		problems = append(problems, fmt.Errorf("configured GitHub host %q conflicts with observed GH_HOST=%q; remediation: unset GH_HOST or set GH_HOST=%s", identity.Host(), host, identity.Host()))
	}
	if repo := os.Getenv("GH_REPO"); repo != "" {
		observed, err := parseGitHubSelector(repo, identity.Host())
		if err != nil || observed.Canonical() != identity.Canonical() {
			reason := "repository does not match"
			if err != nil {
				reason = err.Error()
			}
			problems = append(problems, fmt.Errorf("configured GitHub repository %q conflicts with observed GH_REPO=%q (%s); remediation: unset GH_REPO or set GH_REPO=%s", identity.Selector(), repo, reason, identity.Selector()))
		}
	}
	return errors.Join(problems...)
}

func parseGitHubSelector(value, configuredHost string) (RepositoryIdentity, error) {
	parts := strings.Split(value, "/")
	if len(parts) == 3 {
		if !strings.EqualFold(parts[0], configuredHost) {
			return RepositoryIdentity{}, fmt.Errorf("host must match configured host %q", configuredHost)
		}
		parts = parts[1:]
	}
	if len(parts) != 2 {
		return RepositoryIdentity{}, fmt.Errorf("expected [HOST/]OWNER/REPO")
	}
	return parseGitHubRepositoryPath(strings.Join(parts, "/"))
}
