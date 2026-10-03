package iro

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// githubRuntimeNamespace preserves the existing GitHub path encoding. Future
// providers must supply their own collision-safe key without changing this one.
func githubRuntimeNamespace(identity RepositoryIdentity) runtimeNamespaceKey {
	hash := sha256.Sum256([]byte(identity.Canonical()))
	owner := safePathPart(strings.ToLower(identity.Owner))
	name := safePathPart(strings.ToLower(identity.Name))
	return runtimeNamespaceKey(fmt.Sprintf("%s-%s-%s", owner, name, hex.EncodeToString(hash[:])[:12]))
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
