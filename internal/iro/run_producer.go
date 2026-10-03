package iro

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

// remoteHeads reads the actual endpoint, never a possibly stale tracking ref.
func (s *Service) remoteHeads(root, destination string, refs ...string) (map[string]string, error) {
	args := append([]string{"ls-remote", "--heads", "--", destination}, refs...)
	return s.readRemoteHeads(CommandSpec{Name: "git", Args: args, Dir: root})
}

func (s *Service) readRemoteHeads(spec CommandSpec) (map[string]string, error) {
	result := s.Runner.Run(spec)
	if !commandSucceeded(result) {
		return nil, fmt.Errorf("could not inspect remote refs; verify remote access")
	}
	heads := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(result.Stdout), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 || !validGitOID(fields[0]) || !strings.HasPrefix(fields[1], "refs/heads/") || heads[fields[1]] != "" {
			return nil, fmt.Errorf("remote ref data is invalid")
		}
		heads[fields[1]] = fields[0]
	}
	return heads, nil
}

func (s *Service) runSource(root, remote string) (string, string, error) {
	result := s.Runner.Run(CommandSpec{Name: "git", Args: []string{"symbolic-ref", "--quiet", "HEAD"}, Dir: root})
	ref := strings.TrimSpace(result.Stdout)
	if !commandSucceeded(result) || !strings.HasPrefix(ref, "refs/heads/") || ref == "refs/heads/" {
		return "", "", fmt.Errorf("run requires a named branch; detached HEAD is not supported")
	}
	head, err := s.currentHead(root)
	if err != nil || !validGitOID(head) {
		return "", "", fmt.Errorf("source checkout has no valid HEAD commit")
	}
	heads, err := s.remoteHeads(root, remote, ref)
	if err != nil {
		return "", "", err
	}
	base := strings.TrimPrefix(ref, "refs/heads/")
	if heads[ref] == "" {
		return "", "", fmt.Errorf("configured remote branch %s is missing", base)
	}
	if heads[ref] != head {
		return "", "", fmt.Errorf("local HEAD %s must exactly equal configured remote %s tip %s; ahead, behind or diverged source is not supported; synchronize manually", head, base, heads[ref])
	}
	return base, head, nil
}

func (s *Service) allocateRunDelivery(root string, identity RepositoryIdentity, number int, remote string, detached bool) (*deliveryAllocation, error) {
	result := s.Runner.Run(CommandSpec{Name: "git", Args: []string{"remote", "get-url", "--push", "--all", remote}, Dir: root})
	pushURL, err := singleRemoteURL(result)
	if err != nil {
		return nil, err
	}
	destination, parseErr := parseGitHubRemote(pushURL)
	if detached {
		destination, parseErr = parseUnmanagedGitHubRemote(pushURL)
	}
	if parseErr != nil || destination.Canonical() != identity.Canonical() {
		return nil, fmt.Errorf("push destination does not match the selected repository")
	}
	common, err := s.gitCommonDir(root)
	if err != nil {
		return nil, err
	}
	allocation := &deliveryAllocation{issue: number, commonDir: common, pushURL: pushURL, detached: detached}
	generate := s.newDeliveryID
	if generate == nil {
		generate = generateDeliveryID
	}
	if err := s.selectDelivery(root, githubRuntimeNamespace(identity), allocation, generate); err != nil {
		return nil, err
	}
	return allocation, nil
}

func (s *Service) inspectRemoteDeliveryCollision(root string, allocation *deliveryAllocation) (bool, error) {
	ref := "refs/heads/" + allocation.branch
	// get-url --push already expanded the URL. Rewrite a private alias to that
	// exact endpoint in one pass, rather than rewriting the endpoint a second time.
	alias := "iro-push-endpoint:" + string(allocation.id)
	prefix := []string{"-c", "url." + allocation.pushURL + ".insteadOf=" + alias, "ls-remote"}
	resolveArgs := append(append([]string{}, prefix...), "--get-url", "--", alias)
	resolved := s.Runner.Run(CommandSpec{Name: "git", Args: resolveArgs, Dir: root})
	if !commandSucceeded(resolved) || strings.TrimSpace(resolved.Stdout) != allocation.pushURL {
		return false, fmt.Errorf("could not resolve the exact push endpoint for collision inspection; no remote read attempted")
	}
	// Request both the namespace parent and all descendants of the intended ref.
	args := append(prefix, "--heads", "--", alias, "refs/heads/iro", ref, ref+"/*")
	heads, err := s.readRemoteHeads(CommandSpec{Name: "git", Args: args, Dir: root})
	if err != nil {
		return false, err
	}
	for name := range heads {
		if name == "refs/heads/iro" || name == ref || strings.HasPrefix(name, ref+"/") {
			return true, nil
		}
	}
	return false, nil
}

// These observations belong only to this invocation; they confer no adoption authority.
type runDeliveryState struct {
	allocation       *deliveryAllocation
	push, pr, report string
	prNumber         int
}

func newRunDeliveryState(allocation *deliveryAllocation) *runDeliveryState {
	return &runDeliveryState{allocation: allocation, push: "not attempted", pr: "not attempted", report: "not attempted"}
}

func mutationOutcome(result CommandResult) string {
	if commandSucceeded(result) {
		return "confirmed success"
	}
	// A subprocess error cannot establish that the server did not mutate state.
	if result.ExitCode < 0 {
		return "unknown"
	}
	return "failed (remote outcome unknown)"
}

func (state *runDeliveryState) diagnostic() string {
	a := state.allocation
	return fmt.Sprintf("Delivery %s; Issue #%d; branch %s; workspace %s; push: %s; PR: %s (#%d); report: %s", a.id, a.issue, a.branch, a.worktree, state.push, state.pr, state.prNumber, state.report)
}

func (state *runDeliveryState) failure(err error) error {
	if strings.Contains(err.Error(), "Delivery "+string(state.allocation.id)) {
		return err
	}
	return fmt.Errorf("%w\n%s\nLocal resources may remain at the reported path/ref; no automatic retry, rollback, repair or resume. Unregistered filesystem-only residue may require human inspection", err, state.diagnostic())
}

func (s *Service) finishRunDelivery(identity RepositoryIdentity, state *runDeliveryState, operationErr error, out, errOut io.Writer) error {
	diagnostic := state.diagnostic()
	if operationErr != nil {
		operationErr = state.failure(operationErr)
	} else {
		fmt.Fprintln(out, diagnostic)
	}
	dir := runtimeLogDir(s.Dirs, githubRuntimeNamespace(identity), "deliveries")
	content := diagnostic + "\n"
	if operationErr != nil {
		content += operationErr.Error() + "\n"
	}
	err := s.FileSystem.MkdirAll(dir, 0700)
	if err == nil {
		err = s.createNewFile(filepath.Join(dir, deliveryLeaf(state.allocation.issue, state.allocation.id)+".log"), []byte(content))
	}
	if err != nil {
		fmt.Fprintf(errOut, "warning: could not preserve delivery diagnostic: %v\n%s\n", err, diagnostic)
	}
	return operationErr
}
