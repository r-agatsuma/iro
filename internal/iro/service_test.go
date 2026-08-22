package iro

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeCommandRunner struct {
	lookups map[string]error
	calls   []CommandSpec
	fn      func(CommandSpec) CommandResult
}

func (f *fakeCommandRunner) LookPath(name string) (string, error) {
	if err, ok := f.lookups[name]; ok {
		return "", err
	}
	return "/fake/bin/" + name, nil
}

func (f *fakeCommandRunner) Run(spec CommandSpec) CommandResult {
	f.calls = append(f.calls, spec)
	if f.fn != nil {
		return f.fn(spec)
	}
	return CommandResult{ExitCode: 0}
}

func newTestService(t *testing.T, runner *fakeCommandRunner, root string) *Service {
	t.Helper()
	stateRoot := filepath.Join(t.TempDir(), "state")
	dataRoot := filepath.Join(t.TempDir(), "data")
	return &Service{
		Runner:     runner,
		FileSystem: NewOSFileSystem(),
		Dirs:       RuntimeDirs{StateRoot: stateRoot, DataRoot: dataRoot},
		Now: func() time.Time {
			return time.Unix(1700000000, 0).UTC()
		},
	}
}

func writeProjectFiles(t *testing.T, root string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "WORKFLOW.md"), []byte(workflowTemplate), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "iro.toml"), []byte(configTemplate), 0644); err != nil {
		t.Fatal(err)
	}
}

func standardFakeResult(spec CommandSpec, root, workspace string, issueFailure bool, dirtyWorkspace bool) CommandResult {
	if spec.Name == "git" {
		switch {
		case len(spec.Args) >= 2 && spec.Args[0] == "rev-parse" && spec.Args[1] == "--show-toplevel":
			return CommandResult{Stdout: root + "\n", ExitCode: 0}
		case len(spec.Args) >= 2 && spec.Args[0] == "rev-parse" && spec.Args[1] == "HEAD":
			return CommandResult{Stdout: "0123456789abcdef\n", ExitCode: 0}
		case len(spec.Args) >= 1 && spec.Args[0] == "status":
			if dirtyWorkspace && spec.Dir == workspace {
				return CommandResult{Stdout: " M changed.txt\n", ExitCode: 0}
			}
			return CommandResult{ExitCode: 0}
		case len(spec.Args) >= 2 && spec.Args[0] == "config":
			return CommandResult{Stdout: "git@github.com:acme/iro.git\n", ExitCode: 0}
		case len(spec.Args) >= 1 && spec.Args[0] == "show-ref":
			return CommandResult{ExitCode: 1, Err: errors.New("not found")}
		case len(spec.Args) >= 2 && spec.Args[0] == "worktree" && spec.Args[1] == "list":
			return CommandResult{Stdout: "worktree " + root + "\nHEAD 0123456789abcdef\nbranch refs/heads/main\n\n", ExitCode: 0}
		case len(spec.Args) >= 2 && spec.Args[0] == "worktree" && spec.Args[1] == "add":
			return CommandResult{ExitCode: 0}
		}
	}
	if spec.Name == "gh" {
		if len(spec.Args) >= 2 && spec.Args[0] == "issue" && spec.Args[1] == "view" {
			if issueFailure {
				return CommandResult{ExitCode: 1, Err: errors.New("issue unavailable")}
			}
			return CommandResult{Stdout: `{"number":123,"title":"Bootstrap","body":"Implement the task","url":"https://github.com/acme/iro/issues/123"}`, ExitCode: 0}
		}
		return CommandResult{ExitCode: 0}
	}
	if spec.Name == "codex" {
		if len(spec.Args) >= 2 && spec.Args[0] == "login" && spec.Args[1] == "status" {
			return CommandResult{ExitCode: 0}
		}
		return CommandResult{Stdout: "変更しました。テスト成功。", ExitCode: 0}
	}
	return CommandResult{ExitCode: 0}
}

func TestInitCreatesBothFilesInGitRepositoryWithoutRemote(t *testing.T) {
	root := t.TempDir()
	git := NewOSCommandRunner()
	result := git.Run(CommandSpec{Name: "git", Args: []string{"init", root}})
	if result.Err != nil {
		t.Fatal(result.Err)
	}

	service := NewService(git, NewOSFileSystem())
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
	var output strings.Builder
	if err := service.Init(&output); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	for _, name := range []string{"WORKFLOW.md", "iro.toml"} {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Fatalf("generated %s: %v", name, err)
		}
	}
	if !strings.Contains(output.String(), "initialized") {
		t.Fatalf("unexpected output: %s", output.String())
	}
}

func TestInitOutsideGitRepositoryFailsWithoutCreatingFiles(t *testing.T) {
	root := t.TempDir()
	service := NewService(NewOSCommandRunner(), NewOSFileSystem())
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })

	if err := service.Init(io.Discard); err == nil {
		t.Fatal("Init() unexpectedly succeeded outside a Git repository")
	}
	for _, name := range []string{"WORKFLOW.md", "iro.toml"} {
		if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatalf("%s was created", name)
		}
	}
}

func TestInitRejectsPartialStateWithoutChangingExistingFile(t *testing.T) {
	root := t.TempDir()
	git := NewOSCommandRunner()
	if result := git.Run(CommandSpec{Name: "git", Args: []string{"init", root}}); result.Err != nil {
		t.Fatal(result.Err)
	}
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
	workflow := filepath.Join(root, "WORKFLOW.md")
	if err := os.WriteFile(workflow, []byte("human-owned"), 0644); err != nil {
		t.Fatal(err)
	}
	service := NewService(git, NewOSFileSystem())
	if err := service.Init(io.Discard); err == nil {
		t.Fatal("Init() unexpectedly repaired partial state")
	}
	data, err := os.ReadFile(workflow)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "human-owned" {
		t.Fatalf("existing file changed to %q", data)
	}
	if _, err := os.Stat(filepath.Join(root, "iro.toml")); !os.IsNotExist(err) {
		t.Fatal("iro.toml was created")
	}
}

func TestDoctorAggregatesMissingDependencies(t *testing.T) {
	runner := &fakeCommandRunner{lookups: map[string]error{
		"git":   errors.New("missing"),
		"gh":    errors.New("missing"),
		"codex": errors.New("missing"),
	}}
	service := NewService(runner, NewOSFileSystem())
	var output strings.Builder
	if err := service.Doctor(&output); err == nil {
		t.Fatal("Doctor() unexpectedly succeeded")
	}
	for _, want := range []string{"git executable", "gh executable", "codex executable"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("doctor output does not contain %q: %s", want, output.String())
		}
	}
}

func TestRunFetchFailureDoesNotCreateWorktree(t *testing.T) {
	root := t.TempDir()
	writeProjectFiles(t, root)
	runner := &fakeCommandRunner{}
	runner.fn = func(spec CommandSpec) CommandResult {
		return standardFakeResult(spec, root, "", true, false)
	}
	service := newTestService(t, runner, root)
	if err := service.Run(123, io.Discard); err == nil {
		t.Fatal("Run() unexpectedly succeeded")
	}
	for _, call := range runner.calls {
		if len(call.Args) >= 2 && call.Name == "git" && call.Args[0] == "worktree" && call.Args[1] == "add" {
			t.Fatal("worktree was created after Issue fetch failure")
		}
		if call.Name == "codex" {
			t.Fatal("Codex was started after Issue fetch failure")
		}
	}
}

func TestRunUsesConfiguredIdentityAndNormativeCodexInvocation(t *testing.T) {
	root := t.TempDir()
	writeProjectFiles(t, root)
	runner := &fakeCommandRunner{}
	runner.fn = func(spec CommandSpec) CommandResult {
		return standardFakeResult(spec, root, "", false, false)
	}
	service := newTestService(t, runner, root)
	var output strings.Builder
	if err := service.Run(123, &output); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	var codexCall *CommandSpec
	var issueFetchCall *CommandSpec
	var commentCall *CommandSpec
	for i := range runner.calls {
		call := &runner.calls[i]
		if call.Name == "codex" && len(call.Args) > 0 && call.Args[len(call.Args)-2] == "--ephemeral" {
			codexCall = call
		}
		if call.Name == "gh" && len(call.Args) >= 2 && call.Args[0] == "issue" && call.Args[1] == "view" {
			issueFetchCall = call
		}
		if call.Name == "gh" && len(call.Args) >= 2 && call.Args[0] == "issue" && call.Args[1] == "comment" {
			commentCall = call
		}
	}
	if codexCall == nil || issueFetchCall == nil || commentCall == nil {
		t.Fatalf("missing expected calls: %+v", runner.calls)
	}
	if !containsArgs(issueFetchCall.Args, "--repo", "acme/iro") {
		t.Fatalf("Issue fetch did not use configured repository: %v", issueFetchCall.Args)
	}
	joined := strings.Join(codexCall.Args, " ")
	for _, want := range []string{"--sandbox workspace-write", "--ask-for-approval never", "sandbox_workspace_write.network_access=true", "developer_instructions=", "exec", "--ephemeral"} {
		if !strings.Contains(joined, want) {
			t.Errorf("Codex invocation does not contain %q: %s", want, joined)
		}
	}
	for _, want := range []string{"Repository: acme/iro", "Issue number: 123", "Implement the task", "https://github.com/acme/iro/issues/123"} {
		if !strings.Contains(string(codexCall.Stdin), want) {
			t.Errorf("Codex payload does not contain %q: %s", want, codexCall.Stdin)
		}
	}
	if !strings.Contains(commentCall.Args[len(commentCall.Args)-1], "生成された変更は未コミット") {
		t.Fatalf("result comment is not a Japanese review checkpoint: %v", commentCall.Args)
	}
}

func TestRunReusesMatchingCleanOwnedWorkspace(t *testing.T) {
	root := t.TempDir()
	writeProjectFiles(t, root)
	runner := &fakeCommandRunner{}
	service := newTestService(t, runner, root)
	identity := RepositoryIdentity{Owner: "acme", Name: "iro"}
	workspace := cleanAbsolutePath(worktreePath(service.Dirs, identity, 123))
	if err := os.MkdirAll(workspace, 0755); err != nil {
		t.Fatal(err)
	}
	mapping := ownershipMapping{Version: 1, Repository: identity.Canonical(), IssueNumber: 123, Branch: "iro/issue-123", Worktree: workspace}
	if err := service.writeOwnership(ownershipPath(service.Dirs, identity, 123), mapping); err != nil {
		t.Fatal(err)
	}
	runner.fn = func(spec CommandSpec) CommandResult {
		if spec.Name == "git" && len(spec.Args) >= 1 && spec.Args[0] == "show-ref" {
			return CommandResult{ExitCode: 0}
		}
		if spec.Name == "git" && len(spec.Args) >= 2 && spec.Args[0] == "worktree" && spec.Args[1] == "list" {
			return CommandResult{Stdout: "worktree " + root + "\nbranch refs/heads/main\n\nworktree " + workspace + "\nbranch refs/heads/iro/issue-123\n\n", ExitCode: 0}
		}
		return standardFakeResult(spec, root, workspace, false, false)
	}
	if err := service.Run(123, io.Discard); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	for _, call := range runner.calls {
		if len(call.Args) >= 2 && call.Name == "git" && call.Args[0] == "worktree" && call.Args[1] == "add" {
			t.Fatal("matching worktree was recreated")
		}
	}
}

func TestRunFailureKeepsWorktreeAndPostsFailureResult(t *testing.T) {
	root := t.TempDir()
	writeProjectFiles(t, root)
	runner := &fakeCommandRunner{}
	runner.fn = func(spec CommandSpec) CommandResult {
		result := standardFakeResult(spec, root, "", false, false)
		if spec.Name == "codex" && len(spec.Args) > 0 && spec.Args[0] == "--cd" {
			return CommandResult{Stdout: "途中まで変更しました。", Stderr: "test failed", ExitCode: 9, Err: errors.New("exit status 9")}
		}
		return result
	}
	service := newTestService(t, runner, root)
	if err := service.Run(123, io.Discard); err == nil {
		t.Fatal("Run() unexpectedly succeeded after Codex failure")
	}
	commentFound := false
	for _, call := range runner.calls {
		if call.Name == "gh" && len(call.Args) >= 2 && call.Args[0] == "issue" && call.Args[1] == "comment" {
			commentFound = true
			if !strings.Contains(call.Args[len(call.Args)-1], "状態: 失敗") {
				t.Fatalf("failure result comment has wrong status: %s", call.Args[len(call.Args)-1])
			}
		}
	}
	if !commentFound {
		t.Fatal("failure result comment was not attempted")
	}
	logs, err := filepath.Glob(filepath.Join(service.Dirs.StateRoot, "runs", identityKeyForTest(), "*.log"))
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) == 0 {
		t.Fatal("Codex failure was not retained in a local run log")
	}
}

func TestRunRejectsUnownedExistingWorkspace(t *testing.T) {
	root := t.TempDir()
	writeProjectFiles(t, root)
	runner := &fakeCommandRunner{}
	workspace := filepath.Join(t.TempDir(), "workspace")
	runner.fn = func(spec CommandSpec) CommandResult {
		if spec.Name == "git" && len(spec.Args) >= 1 && spec.Args[0] == "show-ref" {
			return CommandResult{ExitCode: 0}
		}
		if spec.Name == "git" && len(spec.Args) >= 2 && spec.Args[0] == "worktree" && spec.Args[1] == "list" {
			return CommandResult{Stdout: "worktree " + root + "\nbranch refs/heads/main\n\nworktree " + workspace + "\nbranch refs/heads/iro/issue-123\n\n", ExitCode: 0}
		}
		return standardFakeResult(spec, root, workspace, false, false)
	}
	service := newTestService(t, runner, root)
	identity := RepositoryIdentity{Owner: "acme", Name: "iro"}
	path := worktreePath(service.Dirs, identity, 123)
	if err := os.MkdirAll(path, 0755); err != nil {
		t.Fatal(err)
	}
	if err := service.Run(123, io.Discard); err == nil {
		t.Fatal("Run() unexpectedly guessed ownership")
	}
	for _, call := range runner.calls {
		if len(call.Args) >= 2 && call.Name == "git" && call.Args[0] == "worktree" && call.Args[1] == "add" {
			t.Fatal("unowned workspace was changed")
		}
	}
}

func TestRunRejectsDirtyOwnedWorkspaceWithoutCleanup(t *testing.T) {
	root := t.TempDir()
	writeProjectFiles(t, root)
	runner := &fakeCommandRunner{}
	service := newTestService(t, runner, root)
	identity := RepositoryIdentity{Owner: "acme", Name: "iro"}
	workspace := cleanAbsolutePath(worktreePath(service.Dirs, identity, 123))
	if err := os.MkdirAll(workspace, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "changed.txt"), []byte("keep"), 0644); err != nil {
		t.Fatal(err)
	}
	mapping := ownershipMapping{Version: 1, Repository: identity.Canonical(), IssueNumber: 123, Branch: "iro/issue-123", Worktree: workspace}
	if err := service.writeOwnership(ownershipPath(service.Dirs, identity, 123), mapping); err != nil {
		t.Fatal(err)
	}
	runner.fn = func(spec CommandSpec) CommandResult {
		if spec.Name == "git" && len(spec.Args) >= 1 && spec.Args[0] == "show-ref" {
			return CommandResult{ExitCode: 0}
		}
		if spec.Name == "git" && len(spec.Args) >= 2 && spec.Args[0] == "worktree" && spec.Args[1] == "list" {
			return CommandResult{Stdout: "worktree " + root + "\nbranch refs/heads/main\n\nworktree " + workspace + "\nbranch refs/heads/iro/issue-123\n\n", ExitCode: 0}
		}
		if spec.Name == "git" && len(spec.Args) >= 1 && spec.Args[0] == "status" && spec.Dir == workspace {
			return CommandResult{Stdout: "?? changed.txt\n", ExitCode: 0}
		}
		return standardFakeResult(spec, root, workspace, false, false)
	}
	if err := service.Run(123, io.Discard); err == nil {
		t.Fatal("Run() unexpectedly accepted dirty workspace")
	}
	data, err := os.ReadFile(filepath.Join(workspace, "changed.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "keep" {
		t.Fatalf("dirty workspace was changed: %q", data)
	}
	for _, call := range runner.calls {
		if call.Name == "codex" && len(call.Args) > 0 && call.Args[0] == "--cd" {
			t.Fatal("Codex was started for dirty workspace")
		}
	}
}

func containsArgs(args []string, key, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == key && args[i+1] == value {
			return true
		}
	}
	return false
}

func identityKeyForTest() string {
	return (RepositoryIdentity{Owner: "acme", Name: "iro"}).Key()
}

func TestExecuteRejectsNonPositiveIssueNumber(t *testing.T) {
	service := NewService(&fakeCommandRunner{}, NewOSFileSystem())
	if status := Execute([]string{"run", "0"}, io.Discard, io.Discard, service); status == 0 {
		t.Fatal("Execute() accepted issue number zero")
	}
}

func TestOSCommandRunnerReportsExitCode(t *testing.T) {
	runner := OSCommandRunner{}
	result := runner.Run(CommandSpec{Name: "sh", Args: []string{"-c", "exit 7"}})
	if result.ExitCode != 7 || result.Err == nil {
		t.Fatalf("unexpected command result: %+v", result)
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh is unavailable")
	}
}
