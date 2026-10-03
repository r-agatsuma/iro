package iro

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

type runtimeWorkspaceKind string

const (
	managedWorkspace   runtimeWorkspaceKind = "workspaces"
	unmanagedWorkspace runtimeWorkspaceKind = "unmanaged-workspaces"
)

func runtimeWorkspaceParent(dirs RuntimeDirs, identity RepositoryIdentity, kind runtimeWorkspaceKind) string {
	return filepath.Join(dirs.DataRoot, string(kind), identity.Key())
}

func deliveryWorktreePath(dirs RuntimeDirs, identity RepositoryIdentity, issueNumber int, id deliveryID) string {
	return filepath.Join(runtimeWorkspaceParent(dirs, identity, managedWorkspace), deliveryLeaf(issueNumber, id))
}

func deliveryLeaf(issueNumber int, id deliveryID) string {
	return fmt.Sprintf("issue-%d-%s", issueNumber, id)
}

func deliveryBranch(issueNumber int, id deliveryID) string {
	return "iro/" + deliveryLeaf(issueNumber, id)
}

func parseDeliveryBranch(branch string) (int, deliveryID, error) {
	if !strings.HasPrefix(branch, "iro/") {
		return 0, "", fmt.Errorf("expected iro delivery branch")
	}
	return parseDeliveryLeaf(strings.TrimPrefix(branch, "iro/"))
}

func parseDeliveryLeaf(leaf string) (int, deliveryID, error) {
	parts := strings.Split(leaf, "-")
	if len(parts) != 3 || parts[0] != "issue" {
		return 0, "", fmt.Errorf("expected issue-<number>-<delivery-id>")
	}
	number, err := canonicalPositiveNumber(parts[1])
	if err != nil {
		return 0, "", err
	}
	id := deliveryID(parts[2])
	if err := id.validate(); err != nil {
		return 0, "", err
	}
	return number, id, nil
}

func canonicalPositiveNumber(value string) (int, error) {
	number, err := strconv.Atoi(value)
	if err != nil || number <= 0 || strconv.Itoa(number) != value {
		return 0, fmt.Errorf("expected a canonical positive decimal number")
	}
	return number, nil
}

// These stems are shared by detached workspace producers and path recognition.
func detachedWorkspacePattern(operation string, number int) string {
	return detachedWorkspaceStem(operation, number) + "*"
}

func detachedWorkspaceStem(operation string, number int) string {
	if operation == "run" {
		return fmt.Sprintf("run-issue-%d-", number)
	}
	return fmt.Sprintf("%s-pr-%d-", operation, number)
}

// Recognition is naming evidence only. Consumers must also bind a registered
// worktree to the current local Git common directory before acting on it.
func recognizeRuntimeWorkspace(dirs RuntimeDirs, path string) (runtimeWorkspaceKind, bool) {
	if !filepath.IsAbs(path) {
		return "", false
	}
	rel, err := filepath.Rel(cleanAbsolutePath(dirs.DataRoot), filepath.Clean(path))
	if err != nil {
		return "", false
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) != 3 || parts[1] == "" || parts[1] == "." || parts[1] == ".." {
		return "", false
	}
	kind := runtimeWorkspaceKind(parts[0])
	if kind == managedWorkspace {
		_, _, err := parseDeliveryLeaf(parts[2])
		return kind, err == nil
	}
	if kind != unmanagedWorkspace {
		return "", false
	}
	leafParts := strings.Split(parts[2], "-")
	if len(leafParts) != 4 {
		return "", false
	}
	number, err := canonicalPositiveNumber(leafParts[2])
	if err != nil {
		return "", false
	}
	operation := leafParts[0]
	if operation != "run" && operation != "review" && operation != "revise" {
		return "", false
	}
	stem := detachedWorkspaceStem(operation, number)
	if !strings.HasPrefix(parts[2], stem) {
		return "", false
	}
	suffix := strings.TrimPrefix(parts[2], stem)
	if suffix == "" {
		return "", false
	}
	for _, c := range suffix {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			return "", false
		}
	}
	return kind, true
}
