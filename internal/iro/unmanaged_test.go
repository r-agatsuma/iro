package iro

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

type unmanagedFixture struct {
	t                     *testing.T
	root, workspace, head string
	runner                *fakeCommandRunner
	service               *Service
	remoteChecks          int
	stages                []string
	intercept             func(CommandSpec) (CommandResult, bool)
}

func newUnmanagedFixture(t *testing.T) *unmanagedFixture {
	t.Helper()
	f := &unmanagedFixture{t: t, root: t.TempDir(), head: reviewBaseForTest}
	f.runner = &fakeCommandRunner{fn: f.run}
	f.service = newTestService(t, f.runner, f.root)
	return f
}

// All Git, worker and tracker commands are faked; only isolated filesystem
// artifacts are real. Unknown commands fail the test instead of succeeding.
func (f *unmanagedFixture) run(spec CommandSpec) CommandResult {
	f.t.Helper()
	command := strings.Join(spec.Args, " ")
	stage := ""
	if spec.Name == "git" {
		switch spec.Args[0] {
		case "ls-remote":
			f.remoteChecks++
			stage = "remote-check"
		case "add", "commit", "push":
			stage = spec.Args[0]
		case "worktree":
			stage = "worktree-" + spec.Args[1]
		}
	}
	if spec.Name == "codex" && containsString(spec.Args, "--ephemeral") {
		stage = "worker"
	}
	if spec.Name == "gh" && containsArgs(spec.Args, "--method", "POST") {
		stage = "pr-create"
	}
	if stage != "" {
		f.stages = append(f.stages, stage)
	}
	if f.intercept != nil {
		if result, ok := f.intercept(spec); ok {
			return result
		}
	}
	switch spec.Name {
	case "git":
		switch command {
		case "rev-parse --show-toplevel":
			return CommandResult{Stdout: f.root}
		case "config --get-all remote.origin.url", "remote get-url --push --all origin":
			return CommandResult{Stdout: "git@github.com:acme/iro.git\n"}
		case "symbolic-ref --quiet HEAD":
			if spec.Dir == f.root {
				return CommandResult{Stdout: "refs/heads/release/topic"}
			}
			return CommandResult{ExitCode: 1}
		case "rev-parse HEAD":
			if spec.Dir == f.root {
				return CommandResult{Stdout: reviewBaseForTest}
			}
			return CommandResult{Stdout: f.head}
		case "ls-remote --heads -- origin refs/heads/release/topic refs/heads/iro/issue-123":
			return CommandResult{Stdout: reviewBaseForTest + "\trefs/heads/release/topic\n"}
		case "status --porcelain --untracked-files=all", "add --all":
			return CommandResult{}
		case "diff --cached --quiet --exit-code":
			return CommandResult{ExitCode: 1}
		case "commit -m Implement issue #123":
			f.head = reviewHeadForTest
			return CommandResult{}
		case "rev-list --parents -n 1 HEAD":
			return CommandResult{Stdout: reviewHeadForTest + " " + reviewBaseForTest}
		case "push -- origin " + reviewHeadForTest + ":refs/heads/iro/issue-123":
			return CommandResult{}
		case "worktree list --porcelain":
			return CommandResult{Stdout: "worktree " + f.root + "\nbranch refs/heads/release/topic\n\n"}
		}
		if containsArgs(spec.Args, "worktree", "add") && len(spec.Args) == 5 && spec.Args[2] == "--detach" && spec.Args[4] == reviewBaseForTest {
			f.workspace = spec.Args[3]
			f.head = reviewBaseForTest
			return CommandResult{}
		}
		if reflect.DeepEqual(spec.Args, []string{"worktree", "remove", "--", f.workspace}) {
			if err := os.RemoveAll(f.workspace); err != nil {
				f.t.Fatal(err)
			}
			return CommandResult{}
		}
	case "gh":
		assertExplicitGitHubTarget(f.t, spec)
		switch {
		case command == "auth status --hostname github.com":
			return CommandResult{}
		case containsArgs(spec.Args, "issue", "view") && spec.Args[2] == "123":
			return standardFakeResult(spec, f.root, "", false, false)
		case containsArgs(spec.Args, "api", "repos/acme/iro/pulls"):
			var body map[string]any
			if err := json.Unmarshal(spec.Stdin, &body); err != nil {
				f.t.Fatal(err)
			}
			if body["head"] != "iro/issue-123" || body["base"] != "release/topic" || body["body"] != "Issue #123 の実装です。\n\nRefs #123\n" || body["draft"] != false {
				f.t.Fatalf("unexpected PR payload: %s", spec.Stdin)
			}
			return CommandResult{Stdout: `{"number":42}`}
		case containsArgs(spec.Args, "pr", "comment") && spec.Args[2] == "42", containsArgs(spec.Args, "issue", "comment") && spec.Args[2] == "123":
			return CommandResult{}
		}
	case "codex":
		if command == "login status" {
			return CommandResult{}
		}
		if containsString(spec.Args, "--ephemeral") {
			if spec.Dir != f.workspace || !containsArgs(spec.Args, "--cd", f.workspace) {
				f.t.Fatalf("worker escaped detached workspace: %+v", spec)
			}
			return CommandResult{Stdout: "変更とテスト完了。Closes #999 is opaque worker text."}
		}
	}
	f.t.Fatalf("unexpected command: %+v", spec)
	return CommandResult{ExitCode: 1}
}

func (f *unmanagedFixture) execute(options ...string) (int, string, string) {
	f.t.Helper()
	var out, errOut strings.Builder
	code := Execute(append([]string{"run", "123", "--unmanaged"}, options...), &out, &errOut, f.service)
	return code, out.String(), errOut.String()
}

func TestUnmanagedRunScenarioA(t *testing.T) {
	f := newUnmanagedFixture(t)
	if code, out, diagnostic := f.execute(); code != 0 || diagnostic != "" || !strings.Contains(out, "PR #42") || strings.Contains(out, "iro land") {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, out, diagnostic)
	}
	want := []string{"remote-check", "worktree-add", "worker", "add", "remote-check", "commit", "remote-check", "push", "pr-create", "worktree-remove", "worktree-list"}
	if !reflect.DeepEqual(f.stages, want) {
		t.Fatalf("stages=%v want=%v", f.stages, want)
	}
	if _, err := os.Stat(f.workspace); !os.IsNotExist(err) {
		t.Fatalf("workspace remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.service.Dirs.StateRoot, "ownership")); !os.IsNotExist(err) {
		t.Fatalf("ownership state created: %v", err)
	}
	logs, err := filepath.Glob(filepath.Join(f.service.Dirs.StateRoot, "unmanaged-runs", "*", "*.log"))
	if err != nil || len(logs) != 1 {
		t.Fatalf("missing Author log: %v %v", logs, err)
	}
	data, err := os.ReadFile(logs[0])
	if err != nil || !strings.Contains(string(data), "変更とテスト完了") || !strings.Contains(string(data), reviewBaseForTest) {
		t.Fatalf("incomplete log: %s %v", data, err)
	}
	assertWorkerHasNoModel(t, f.runner.calls)
	assertWorkerHasNoReasoningEffort(t, f.runner.calls)
	for _, call := range f.runner.calls {
		if call.Name == "codex" && containsString(call.Args, "--ephemeral") {
			if !containsArgs(call.Args, "-c", "developer_instructions="+strconv.Quote(unmanagedDeveloperInstructions)) || !containsArgs(call.Args, "--sandbox", "workspace-write") || !containsArgs(call.Args, "--ask-for-approval", "never") {
				t.Fatalf("incorrect worker policy: %v", call.Args)
			}
			for _, instruction := range []string{"Do not read iro.toml or WORKFLOW.md", "AGENTS.md", "Do not invoke gh", "read-only inspection", "Do not provision or repair", "human judgment", "Return the final work report in Japanese"} {
				if !strings.Contains(unmanagedDeveloperInstructions, instruction) {
					t.Fatalf("missing conservative policy: %s", instruction)
				}
			}
		}
	}
}

type unmanagedFileGuard struct {
	FileSystem
	t *testing.T
}

func (fs unmanagedFileGuard) check(path string) {
	fs.t.Helper()
	if filepath.Base(path) == "iro.toml" || filepath.Base(path) == "WORKFLOW.md" || strings.Contains(path, string(filepath.Separator)+"ownership"+string(filepath.Separator)) {
		fs.t.Fatalf("unmanaged accessed project policy or ownership: %s", path)
	}
}

func (fs unmanagedFileGuard) ReadFile(path string) ([]byte, error) {
	fs.check(path)
	return fs.FileSystem.ReadFile(path)
}

func (fs unmanagedFileGuard) Stat(path string) (os.FileInfo, error) {
	fs.check(path)
	return fs.FileSystem.Stat(path)
}

func TestUnmanagedRunIgnoresProjectFilesAndAmbientContext(t *testing.T) {
	for _, files := range []string{"absent", "invalid", "unrelated", "unreadable", "non-regular"} {
		t.Run(files, func(t *testing.T) {
			f := newUnmanagedFixture(t)
			for _, name := range []string{"iro.toml", "WORKFLOW.md"} {
				path := filepath.Join(f.root, name)
				var err error
				switch files {
				case "invalid":
					err = os.WriteFile(path, []byte("[invalid"), 0600)
				case "unrelated":
					err = os.WriteFile(path, []byte("unrelated policy: use different/repo"), 0600)
				case "unreadable":
					err = os.WriteFile(path, []byte("must not be read"), 0000)
				case "non-regular":
					err = os.Mkdir(path, 0700)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			f.service.FileSystem = unmanagedFileGuard{FileSystem: f.service.FileSystem, t: t}
			t.Setenv("GH_HOST", " malformed://elsewhere ")
			t.Setenv("GH_REPO", "https://other/repo/invalid")
			// The strict runner rejects upstream reads and local Issue branch reads.
			if code, _, diagnostic := f.execute(); code != 0 {
				t.Fatal(diagnostic)
			}
			assertGitHubEnvironmentUnchanged(t, " malformed://elsewhere ", "https://other/repo/invalid", false)
		})
	}
}

func TestUnmanagedCLIOptions(t *testing.T) {
	for _, options := range [][]string{
		{"--unmanaged", "--model", "test-model", "--reasoning-effort", "high", "--no-sandbox"},
		{"--no-sandbox", "--reasoning-effort", "high", "-m", "test-model", "--unmanaged"},
	} {
		f := newUnmanagedFixture(t)
		var diagnostic strings.Builder
		if code := Execute(append([]string{"run", "123"}, options...), io.Discard, &diagnostic, f.service); code != 0 {
			t.Fatalf("%v: %s", options, diagnostic.String())
		}
		assertWorkerOptions(t, f.runner.calls, "test-model", "high")
		for _, call := range f.runner.calls {
			if call.Name == "codex" && containsString(call.Args, "--ephemeral") && (!containsArgs(call.Args, "--sandbox", "danger-full-access") || containsString(call.Args, "sandbox_workspace_write.network_access=true")) {
				t.Fatal(call.Args)
			}
		}
	}
	invalid := [][]string{
		{"run", "--unmanaged", "123"}, {"run", "--unmanaged"}, {"run", "--issue", "123", "--unmanaged"},
		{"review", "42", "--unmanaged"}, {"revise", "42", "--unmanaged"}, {"land", "42", "--unmanaged"},
		{"run", "123", "--issue", "123"},
	}
	for _, suffix := range [][]string{
		{"--unmanaged"}, {"true"}, {"extra"}, {"--issue", "123"}, {"--unknown"}, {"--unmanaged=true"},
		{"--model"}, {"--model", ""}, {"-m", " \t"}, {"--model", "--no-sandbox"}, {"--model", "a", "-m", "b"},
		{"--reasoning-effort"}, {"--reasoning-effort", ""}, {"--reasoning-effort", " "}, {"--reasoning-effort", "-x"},
		{"--reasoning-effort", "high", "--reasoning-effort", "low"}, {"--no-sandbox", "--no-sandbox"},
	} {
		invalid = append(invalid, append([]string{"run", "123", "--unmanaged"}, suffix...))
	}
	for _, args := range invalid {
		runner := &fakeCommandRunner{}
		if code := Execute(args, io.Discard, io.Discard, NewService(runner, NewOSFileSystem())); code != 2 || len(runner.calls) != 0 {
			t.Fatalf("invalid CLI %v: code=%d calls=%v", args, code, runner.calls)
		}
	}
	var usage strings.Builder
	Execute([]string{"run"}, io.Discard, &usage, nil)
	if !strings.Contains(usage.String(), "run <issue-number> [--unmanaged]") {
		t.Fatal(usage.String())
	}
}

func TestUnmanagedPreconditions(t *testing.T) {
	for _, tc := range []struct {
		name, command string
		result        CommandResult
		want          string
	}{
		{"no fetch URL", "config --get-all remote.origin.url", CommandResult{ExitCode: 1}, "exactly one configured fetch URL"},
		{"empty fetch URL", "config --get-all remote.origin.url", CommandResult{Stdout: "\n"}, "exactly one configured fetch URL"},
		{"multiple fetch URLs", "config --get-all remote.origin.url", CommandResult{Stdout: "git@github.com:acme/iro.git\ngit@github.com:acme/iro.git\n"}, "exactly one configured fetch URL"},
		{"empty additional URL", "config --get-all remote.origin.url", CommandResult{Stdout: "git@github.com:acme/iro.git\n\n"}, "exactly one configured fetch URL"},
		{"unsupported host", "config --get-all remote.origin.url", CommandResult{Stdout: "git@example.com:acme/iro.git"}, "supported GitHub repository"},
		{"malformed URL", "config --get-all remote.origin.url", CommandResult{Stdout: "not-a-url"}, "supported GitHub repository"},
		{"zero push URLs", "remote get-url --push --all origin", CommandResult{}, "exactly one effective push URL"},
		{"multiple same push URLs", "remote get-url --push --all origin", CommandResult{Stdout: "git@github.com:acme/iro.git\nhttps://github.com/ACME/IRO\n"}, "exactly one effective push URL"},
		{"different push repository", "remote get-url --push --all origin", CommandResult{Stdout: "git@github.com:other/repo.git"}, "push destination"},
		{"invalid push URL", "remote get-url --push --all origin", CommandResult{Stdout: "invalid"}, "push destination"},
		{"dirty", "status --porcelain --untracked-files=all", CommandResult{Stdout: "?? human.txt\n"}, "dirty"},
		{"detached", "symbolic-ref --quiet HEAD", CommandResult{ExitCode: 1}, "named branch"},
		{"invalid head", "rev-parse HEAD", CommandResult{Stdout: "invalid"}, "valid HEAD"},
		{"missing base", "ls-remote", CommandResult{}, "base is missing"},
		{"base mismatch", "ls-remote", CommandResult{Stdout: reviewHeadForTest + "\trefs/heads/release/topic\n"}, "base drift"},
		{"collision", "ls-remote", CommandResult{Stdout: reviewBaseForTest + "\trefs/heads/release/topic\n" + reviewHeadForTest + "\trefs/heads/iro/issue-123\n"}, "already exists"},
		{"remote failure", "ls-remote", CommandResult{ExitCode: 128}, "remote access"},
		{"Issue unreadable", "issue view", CommandResult{ExitCode: 1}, "could not read GitHub Issue"},
		{"GitHub auth", "auth status", CommandResult{ExitCode: 1}, "authentication check failed"},
		{"Codex auth", "login status", CommandResult{ExitCode: 1}, "authentication check failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newUnmanagedFixture(t)
			f.intercept = func(spec CommandSpec) (CommandResult, bool) {
				return tc.result, strings.HasPrefix(strings.Join(spec.Args, " "), tc.command)
			}
			code, _, diagnostic := f.execute()
			if code != 1 || !strings.Contains(diagnostic, tc.want) {
				t.Fatalf("code=%d stderr=%s", code, diagnostic)
			}
			for _, stage := range f.stages {
				if stage != "remote-check" {
					t.Fatalf("side effect before rejection: %s", stage)
				}
			}
		})
	}
}

func TestUnmanagedRevalidatesBeforeCommitAndPush(t *testing.T) {
	for _, check := range []int{2, 3} {
		for _, failure := range []string{"base drift", "task collision", "identity drift", "push destination drift"} {
			t.Run(fmt.Sprintf("%d/%s", check, failure), func(t *testing.T) {
				f := newUnmanagedFixture(t)
				f.intercept = func(spec CommandSpec) (CommandResult, bool) {
					if spec.Name != "git" {
						return CommandResult{}, false
					}
					if f.remoteChecks == check-1 {
						if failure == "identity drift" && spec.Args[0] == "config" || failure == "push destination drift" && spec.Args[0] == "remote" {
							return CommandResult{Stdout: "git@github.com:other/repo.git"}, true
						}
					}
					if spec.Args[0] == "ls-remote" && f.remoteChecks == check {
						if failure == "base drift" {
							return CommandResult{Stdout: reviewHeadForTest + "\trefs/heads/release/topic\n"}, true
						}
						if failure == "task collision" {
							return CommandResult{Stdout: reviewBaseForTest + "\trefs/heads/release/topic\n" + reviewHeadForTest + "\trefs/heads/iro/issue-123\n"}, true
						}
					}
					return CommandResult{}, false
				}
				code, _, diagnostic := f.execute()
				if code != 1 || !strings.Contains(diagnostic, f.workspace) || containsString(f.stages, "push") || containsString(f.stages, "worktree-remove") {
					t.Fatalf("code=%d stderr=%s stages=%v", code, diagnostic, f.stages)
				}
				if containsString(f.stages, "commit") != (check == 3) {
					t.Fatal(f.stages)
				}
			})
		}
	}
}

func TestUnmanagedDeliveryFailuresRetainState(t *testing.T) {
	for _, tc := range []struct {
		name, command, want string
		result              CommandResult
	}{
		{"worker", "worker", "Author exited", CommandResult{ExitCode: 1, Stdout: "partial report"}},
		{"stage", "add", "stage worker", CommandResult{ExitCode: 1}},
		{"empty", "diff", "no committable", CommandResult{}},
		{"diff failure", "diff", "inspect staged", CommandResult{ExitCode: 128}},
		{"commit", "commit", "commit failed", CommandResult{ExitCode: 1}},
		{"wrong parent", "rev-list", "sole parent", CommandResult{Stdout: reviewHeadForTest + " " + reviewHeadForTest}},
		{"merge commit", "rev-list", "sole parent", CommandResult{Stdout: reviewHeadForTest + " " + reviewBaseForTest + " " + reviewHeadForTest}},
		{"push", "push", "remote branch may have been updated", CommandResult{ExitCode: 1}},
		{"PR failure", "pr-create", "partial or uncertain delivery", CommandResult{ExitCode: 1}},
		{"PR ambiguous", "pr-create", "a PR may exist", CommandResult{Stdout: `{}`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newUnmanagedFixture(t)
			attempts := 0
			f.intercept = func(spec CommandSpec) (CommandResult, bool) {
				match := spec.Name == "git" && spec.Args[0] == tc.command || tc.command == "worker" && spec.Name == "codex" && containsString(spec.Args, "--ephemeral") || tc.command == "pr-create" && spec.Name == "gh" && containsArgs(spec.Args, "--method", "POST")
				if match {
					attempts++
				}
				return tc.result, match
			}
			code, _, diagnostic := f.execute()
			if code != 1 || !strings.Contains(diagnostic, tc.want) || !strings.Contains(diagnostic, f.workspace) || attempts != 1 || containsString(f.stages, "worktree-remove") {
				t.Fatalf("code=%d stderr=%s stages=%v attempts=%d", code, diagnostic, f.stages, attempts)
			}
			if _, err := os.Stat(f.workspace); err != nil {
				t.Fatal(err)
			}
			if tc.command == "pr-create" && !containsString(f.stages, "push") {
				t.Fatal("PR creation was attempted without push")
			}
		})
	}
}

func TestUnmanagedCleanupAndReportFailureRemainSuccess(t *testing.T) {
	for _, failure := range []string{"removal", "path remains", "registration remains", "comment"} {
		t.Run(failure, func(t *testing.T) {
			f := newUnmanagedFixture(t)
			f.intercept = func(spec CommandSpec) (CommandResult, bool) {
				if spec.Name == "git" && containsArgs(spec.Args, "worktree", "remove") {
					if failure == "removal" {
						return CommandResult{ExitCode: 1}, true
					}
					if failure == "path remains" {
						return CommandResult{}, true
					}
				}
				if failure == "registration remains" && spec.Name == "git" && containsArgs(spec.Args, "worktree", "list") {
					return CommandResult{Stdout: "worktree " + f.workspace + "\ndetached\n"}, true
				}
				return CommandResult{ExitCode: 1}, failure == "comment" && spec.Name == "gh" && containsArgs(spec.Args, "pr", "comment")
			}
			code, _, diagnostic := f.execute()
			if code != 0 || !strings.Contains(diagnostic, "warning:") {
				t.Fatalf("code=%d stderr=%s", code, diagnostic)
			}
			if failure != "comment" && (!strings.Contains(diagnostic, f.workspace) || !strings.Contains(diagnostic, "delivery confirmed")) {
				t.Fatal(diagnostic)
			}
			if strings.Count(strings.Join(f.stages, " "), "pr-create") != 1 || strings.Count(strings.Join(f.stages, " "), "push") != 1 {
				t.Fatalf("delivery retried: %v", f.stages)
			}
		})
	}
}

func TestUnmanagedFailureNeverReusesWorkspaceOrManagedState(t *testing.T) {
	f := newUnmanagedFixture(t)
	identity := RepositoryIdentity{Owner: "acme", Name: "iro"}
	mapping := ownershipPath(f.service.Dirs, identity, 123)
	canonical := worktreePath(f.service.Dirs, identity, 123)
	for _, path := range []string{mapping, filepath.Join(canonical, "human.txt")} {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("human-owned"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	f.service.FileSystem = unmanagedFileGuard{FileSystem: f.service.FileSystem, t: t}
	f.intercept = func(spec CommandSpec) (CommandResult, bool) {
		return CommandResult{ExitCode: 1, Stdout: "partial report"}, spec.Name == "codex" && containsString(spec.Args, "--ephemeral")
	}
	if code, _, diagnostic := f.execute(); code != 1 {
		t.Fatal(diagnostic)
	}
	first := f.workspace
	if code, _, diagnostic := f.execute(); code != 1 || first == f.workspace {
		t.Fatalf("failed workspace reused: %s %s %s", first, f.workspace, diagnostic)
	}
	for _, path := range []string{first, f.workspace} {
		if _, err := os.Stat(path); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{mapping, filepath.Join(canonical, "human.txt")} {
		data, err := os.ReadFile(path)
		if err != nil || string(data) != "human-owned" {
			t.Fatalf("managed state changed: %s %v", data, err)
		}
	}
}

func TestUnmanagedG4OriginAndManagedConfiguredRemoteRemainIndependent(t *testing.T) {
	f := newUnmanagedFixture(t)
	writeProjectFiles(t, f.root)
	config := strings.Replace(configTemplate, `remote = "origin"`, `remote = "tracker"`, 1)
	if err := os.WriteFile(filepath.Join(f.root, "iro.toml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	f.runner.fn = func(spec CommandSpec) CommandResult {
		if spec.Name == "git" && spec.Args[0] == "config" {
			if !containsString(spec.Args, "remote.tracker.url") {
				t.Fatalf("managed Run consulted origin: %+v", spec)
			}
			return CommandResult{Stdout: "git@github.com:managed/project.git\n"}
		}
		if spec.Name == "git" && spec.Args[0] == "remote" {
			if spec.Args[len(spec.Args)-1] != "tracker" {
				t.Fatal(spec.Args)
			}
			return CommandResult{Stdout: "git@github.com:managed/project.git\n"}
		}
		if spec.Name == "git" && spec.Args[0] == "push" && !containsArgs(spec.Args, "--", "tracker") {
			t.Fatalf("managed push target changed: %v", spec.Args)
		}
		if spec.Name == "gh" {
			normalized := spec
			normalized.Args = make([]string, len(spec.Args))
			for i, arg := range spec.Args {
				normalized.Args[i] = strings.ReplaceAll(arg, "managed/project", "acme/iro")
				if arg == "owner=managed" {
					normalized.Args[i] = "owner=acme"
				}
				if arg == "name=project" {
					normalized.Args[i] = "name=iro"
				}
			}
			if strings.Contains(strings.Join(spec.Args, " "), "acme/iro") {
				t.Fatalf("managed operation targeted origin repository: %v", spec.Args)
			}
			assertExplicitGitHubTarget(t, normalized)
			if containsArgs(spec.Args, "--method", "POST") {
				var body map[string]any
				if json.Unmarshal(spec.Stdin, &body) != nil || body["base"] != "main" || body["body"] != "Issue #123 の実装です。\n\nCloses #123\n" {
					t.Fatalf("managed delivery changed: %s", spec.Stdin)
				}
			}
		}
		result := standardFakeResult(spec, f.root, "", false, false)
		result.Stdout = strings.ReplaceAll(result.Stdout, "acme/iro", "managed/project")
		return result
	}
	var diagnostic strings.Builder
	if code := Execute([]string{"run", "123"}, io.Discard, &diagnostic, f.service); code != 0 {
		t.Fatal(diagnostic.String())
	}
	mapping := ownershipPath(f.service.Dirs, RepositoryIdentity{Owner: "managed", Name: "project"}, 123)
	before, err := os.ReadFile(mapping)
	if err != nil {
		t.Fatal(err)
	}
	f.runner.fn = f.run
	f.service.FileSystem = unmanagedFileGuard{FileSystem: f.service.FileSystem, t: t}
	if code, _, diagnostic := f.execute(); code != 0 {
		t.Fatal(diagnostic)
	}
	after, err := os.ReadFile(mapping)
	if err != nil || string(before) != string(after) {
		t.Fatalf("unmanaged Run changed managed ownership: %v", err)
	}
}

func TestUnmanagedG2ManagedEligibilityUsesCurrentRelationNotProvenance(t *testing.T) {
	for _, operation := range []string{"review", "revise", "land"} {
		t.Run(operation, func(t *testing.T) {
			f := newUnmanagedFixture(t)
			if code, _, diagnostic := f.execute(); code != 0 {
				t.Fatal(diagnostic)
			}
			writeProjectFiles(t, f.root)
			var response map[string]any
			if err := json.Unmarshal([]byte(landResponseForTest), &response); err != nil {
				t.Fatal(err)
			}
			repo := response["data"].(map[string]any)["repository"].(map[string]any)
			pr := repo["pullRequest"].(map[string]any)
			pr["url"] = "https://github.com/acme/iro/pull/42"
			pr["baseRefOid"] = reviewBaseForTest
			pr["body"] = "Refs #123"
			pr["author"] = map[string]any{"login": "unmanaged-author"}
			pr["baseRefName"] = "release/topic"
			closing := pr["closingIssuesReferences"]
			pr["closingIssuesReferences"] = map[string]any{"totalCount": 0, "nodes": []any{}}
			f.runner.fn = func(spec CommandSpec) CommandResult {
				if spec.Name == "gh" && containsArgs(spec.Args, "api", "graphql") {
					if containsString(spec.Args, "query="+strings.Replace(deliveryQuery, "states:[OPEN,CLOSED,MERGED]", "states:[OPEN]", 1)) {
						return CommandResult{Stdout: landDeliveryPage(landActivePRForTest, false, "")}
					}
					data, _ := json.Marshal(response)
					return CommandResult{Stdout: string(data)}
				}
				if spec.Name == "git" && (spec.Args[0] == "config" || spec.Args[0] == "rev-parse") || spec.Name == "gh" && spec.Args[0] == "auth" {
					return standardFakeResult(spec, f.root, "", false, false)
				}
				t.Fatalf("managed operation proceeded past invalid relation: %+v", spec)
				return CommandResult{ExitCode: 1}
			}
			for _, want := range []string{"requires default branch", "closing relation"} {
				var diagnostic strings.Builder
				if code := Execute([]string{operation, "42"}, io.Discard, &diagnostic, f.service); code != 1 || !strings.Contains(diagnostic.String(), want) {
					t.Fatalf("unmanaged delivery granted %s eligibility: %s", operation, diagnostic.String())
				}
				// A Human changes the base; the missing closing relation still rejects.
				pr["baseRefName"] = "main"
			}
			// With a valid current relation, the same unmanaged provenance/logs do
			// not poison eligibility. Revise's separate local checks still apply.
			pr["closingIssuesReferences"] = closing
			identity := RepositoryIdentity{Owner: "acme", Name: "iro"}
			var err error
			switch operation {
			case "review":
				_, err = f.service.inspectPRTarget(f.root, identity, 42, operation)
			case "revise":
				_, err = f.service.inspectReviseTarget(f.root, identity, 42)
			case "land":
				_, err = f.service.inspectLandTarget(f.root, identity, 42)
			}
			if err != nil {
				t.Fatalf("human-reshaped relation was rejected: %v", err)
			}
		})
	}
}

func TestManagedStatusAndCleanupIgnoreUnmanagedWorkspaces(t *testing.T) {
	f := newUnmanagedFixture(t)
	f.intercept = func(spec CommandSpec) (CommandResult, bool) {
		return CommandResult{ExitCode: 1}, spec.Name == "git" && containsArgs(spec.Args, "worktree", "remove")
	}
	if code, _, diagnostic := f.execute(); code != 0 {
		t.Fatal(diagnostic)
	}
	writeProjectFiles(t, f.root)
	for _, args := range [][]string{{"status"}, {"cleanup"}, {"cleanup", "123"}} {
		var out, diagnostic strings.Builder
		f.runner.calls = nil
		code := Execute(args, &out, &diagnostic, f.service)
		if len(args) == 1 && code != 0 || len(args) == 2 && code != 1 {
			t.Fatalf("%v: code=%d %s", args, code, diagnostic.String())
		}
		for _, call := range f.runner.calls {
			if call.Name != "git" || call.Args[0] != "config" && call.Args[0] != "rev-parse" {
				t.Fatalf("managed status/cleanup claimed unmanaged state: %+v", call)
			}
		}
		if _, err := os.Stat(f.workspace); err != nil {
			t.Fatalf("unmanaged worktree removed: %v", err)
		}
	}
}

func TestUnmanagedRejectsUnexpectedWorkerGitChanges(t *testing.T) {
	for _, change := range []string{"HEAD", "attached", "dirty after commit"} {
		t.Run(change, func(t *testing.T) {
			f := newUnmanagedFixture(t)
			f.intercept = func(spec CommandSpec) (CommandResult, bool) {
				if spec.Name == "git" && spec.Dir == f.workspace && containsString(f.stages, "worker") {
					if change == "HEAD" && spec.Args[0] == "rev-parse" {
						return CommandResult{Stdout: reviewHeadForTest}, true
					}
					if change == "attached" && spec.Args[0] == "symbolic-ref" {
						return CommandResult{Stdout: "refs/heads/human-owned"}, true
					}
					if change == "dirty after commit" && spec.Args[0] == "status" && containsString(f.stages, "commit") {
						return CommandResult{Stdout: " M modified-by-hook\n"}, true
					}
				}
				return CommandResult{}, false
			}
			if code, _, diagnostic := f.execute(); code != 1 || !strings.Contains(diagnostic, f.workspace) || containsString(f.stages, "push") || containsString(f.stages, "worktree-remove") {
				t.Fatalf("code=%d stderr=%s stages=%v", code, diagnostic, f.stages)
			}
		})
	}
}

type unmanagedLogFailure struct{ FileSystem }

func (fs unmanagedLogFailure) CreateNew(path string, perm os.FileMode) (io.WriteCloser, error) {
	return nil, os.ErrPermission
}

func TestUnmanagedLogFailureStopsDeliveryAndPreservesReport(t *testing.T) {
	f := newUnmanagedFixture(t)
	f.service.FileSystem = unmanagedLogFailure{FileSystem: f.service.FileSystem}
	f.intercept = func(spec CommandSpec) (CommandResult, bool) {
		return CommandResult{ExitCode: 1}, spec.Name == "gh" && containsArgs(spec.Args, "issue", "comment")
	}
	code, _, diagnostic := f.execute()
	for _, want := range []string{"create unmanaged Author log", "変更とテスト完了", "failure report comment failed", f.workspace} {
		if code != 1 || !strings.Contains(diagnostic, want) {
			t.Fatalf("code=%d stderr=%s missing=%s", code, diagnostic, want)
		}
	}
	for _, stage := range []string{"add", "commit", "push", "worktree-remove"} {
		if containsString(f.stages, stage) {
			t.Fatalf("continued after log failure: %v", f.stages)
		}
	}
}
