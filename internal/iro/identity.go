package iro

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
)

// RepositoryIdentity is the canonical GitHub owner/repository pair.
type RepositoryIdentity struct {
	Owner string
	Name  string
}

func (r RepositoryIdentity) String() string {
	return r.Owner + "/" + r.Name
}

func (r RepositoryIdentity) Canonical() string {
	return strings.ToLower(r.Owner) + "/" + strings.ToLower(r.Name)
}

func (r RepositoryIdentity) Key() string {
	hash := sha256.Sum256([]byte(r.Canonical()))
	owner := safePathPart(strings.ToLower(r.Owner))
	name := safePathPart(strings.ToLower(r.Name))
	return fmt.Sprintf("%s-%s-%s", owner, name, hex.EncodeToString(hash[:])[:12])
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

	if !strings.EqualFold(host, "github.com") {
		return RepositoryIdentity{}, fmt.Errorf("remote host %q is not github.com", host)
	}
	path = strings.Trim(path, "/")
	if strings.HasSuffix(path, ".git") {
		path = strings.TrimSuffix(path, ".git")
	}
	parts := strings.Split(path, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return RepositoryIdentity{}, fmt.Errorf("remote URL does not identify exactly one GitHub repository")
	}
	return RepositoryIdentity{Owner: parts[0], Name: parts[1]}, nil
}

func safePathPart(value string) string {
	var builder strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			builder.WriteRune(r)
		} else {
			builder.WriteByte('-')
		}
	}
	if builder.Len() == 0 {
		return "repo"
	}
	return builder.String()
}

func cleanAbsolutePath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	return filepath.Clean(abs)
}
