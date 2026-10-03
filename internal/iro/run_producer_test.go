package iro

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func validDeliveryHeadForTest(value any, number int) bool {
	branch, ok := value.(string)
	if !ok {
		return false
	}
	issue, _, err := parseDeliveryBranch(branch)
	return err == nil && issue == number
}

// Producer fixtures model the command-local alias resolution. Real Git rewrite
// behavior is covered separately without accessing a network endpoint.
func pushInspectionForTest(spec CommandSpec) (CommandSpec, *CommandResult) {
	if spec.Name == "git" && len(spec.Args) > 2 && spec.Args[0] == "-c" {
		setting := spec.Args[1]
		spec.Args = spec.Args[2:]
		if containsString(spec.Args, "--get-url") {
			endpoint, _, _ := strings.Cut(strings.TrimPrefix(setting, "url."), ".insteadOf=")
			return spec, &CommandResult{Stdout: endpoint}
		}
	}
	return spec, nil
}

func producerMutationStages(stages []string) []string {
	var mutations []string
	for _, stage := range stages {
		switch stage {
		case "worktree-add", "worker", "add", "commit", "push", "pr-create", "worktree-remove":
			mutations = append(mutations, stage)
		}
	}
	return mutations
}

// The managed producer fixture exposes real local artifacts and fake external
// command observations so later consumers can reuse delivery identities/resources.
type managedProducerFixture struct {
	t                   *testing.T
	root                string
	runner              *fakeCommandRunner
	service             *Service
	refs, registrations string
	branches, paths     []string
	intercept           func(CommandSpec) (CommandResult, bool)
}

func newManagedProducerFixture(t *testing.T) *managedProducerFixture {
	t.Helper()
	f := &managedProducerFixture{t: t, root: t.TempDir()}
	writeProjectFiles(t, f.root)
	f.runner = &fakeCommandRunner{fn: f.run}
	f.service = newTestService(t, f.runner, f.root)
	return f
}

func (f *managedProducerFixture) run(spec CommandSpec) CommandResult {
	f.t.Helper()
	if normalized, result := pushInspectionForTest(spec); result != nil {
		return *result
	} else {
		spec = normalized
	}
	if f.intercept != nil {
		if result, ok := f.intercept(spec); ok {
			return result
		}
	}
	if spec.Name == "git" {
		switch spec.Args[0] {
		case "symbolic-ref":
			return CommandResult{Stdout: "refs/heads/release/topic"}
		case "ls-remote":
			if containsString(spec.Args, "refs/heads/release/topic") {
				return CommandResult{Stdout: foundationHEAD + "\trefs/heads/release/topic\n"}
			}
			return CommandResult{}
		case "for-each-ref":
			return CommandResult{Stdout: f.refs}
		case "worktree":
			if spec.Args[1] == "list" && containsString(spec.Args, "-z") {
				return CommandResult{Stdout: worktreeRecord(f.root, "branch refs/heads/release/topic") + f.registrations}
			}
			if spec.Args[1] == "add" {
				if len(spec.Args) != 6 || spec.Args[2] != "-b" || !validDeliveryHeadForTest(spec.Args[3], 123) || spec.Args[5] != foundationHEAD {
					f.t.Fatalf("wrong creation: %+v", spec)
				}
				f.branches = append(f.branches, spec.Args[3])
				f.paths = append(f.paths, spec.Args[4])
				f.refs += "refs/heads/" + spec.Args[3] + "\x00" + foundationHEAD + "\n"
				f.registrations += worktreeRecord(spec.Args[4], "branch refs/heads/"+spec.Args[3])
			}
		case "push":
			branch := f.branches[len(f.branches)-1]
			if strings.Join(spec.Args, " ") != "push --no-follow-tags --no-recurse-submodules -- origin refs/heads/"+branch+":refs/heads/"+branch {
				f.t.Fatalf("wrong push: %+v", spec)
			}
		case "fetch", "pull", "reset", "rebase", "merge", "branch", "checkout", "switch", "clean", "stash":
			f.t.Fatalf("unexpected source synchronization or cleanup: %+v", spec)
		}
	}
	if spec.Name == "gh" && spec.Args[0] == "api" {
		if spec.Args[1] == "graphql" {
			f.t.Fatal("Run enumerated earlier PRs/default branch")
		}
		var body map[string]any
		if json.Unmarshal(spec.Stdin, &body) != nil || body["base"] != "release/topic" || !validDeliveryHeadForTest(body["head"], 123) || body["body"] != githubRunBody(123, false) {
			f.t.Fatalf("wrong PR: %s", spec.Stdin)
		}
	}
	return standardFakeResult(spec, f.root, "", false, false)
}

func TestRunProducesIndependentDeliveries(t *testing.T) {
	for _, unmanaged := range []bool{false, true} {
		t.Run(map[bool]string{false: "managed", true: "unmanaged"}[unmanaged], func(t *testing.T) {
			var service *Service
			var runner *fakeCommandRunner
			if unmanaged {
				f := newUnmanagedFixture(t)
				service, runner = f.service, f.runner
			} else {
				f := newManagedProducerFixture(t)
				// An earlier same-Issue branch/worktree and invalid v1 mapping do not
				// establish invocation authority or block an independent delivery.
				f.refs = "refs/heads/iro/issue-123\x00" + foundationHEAD + "\n"
				f.registrations = worktreeRecord(filepath.Join(f.root, "earlier"), "branch refs/heads/iro/issue-123")
				oldMapping := ownershipPath(f.service.Dirs, RepositoryIdentity{Owner: "acme", Name: "iro"}, 123)
				if err := os.MkdirAll(filepath.Dir(oldMapping), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(oldMapping, []byte("invalid legacy state"), 0600); err != nil {
					t.Fatal(err)
				}
				service, runner = f.service, f.runner
			}
			for i := 0; i < 2; i++ {
				var diagnostic strings.Builder
				if err := service.runWithOptions(123, workerOptions{Unmanaged: unmanaged}, io.Discard, &diagnostic); err != nil {
					t.Fatalf("run %d: %v %s", i, err, diagnostic.String())
				}
			}
			var heads []string
			for _, call := range runner.calls {
				if call.Name == "gh" && containsArgs(call.Args, "--method", "POST") {
					var body map[string]any
					if err := json.Unmarshal(call.Stdin, &body); err != nil {
						t.Fatal(err)
					}
					heads = append(heads, body["head"].(string))
				}
			}
			if len(heads) != 2 || heads[0] == heads[1] {
				t.Fatalf("shared delivery refs: %v", heads)
			}
			logKind := "runs"
			if unmanaged {
				logKind = "unmanaged-runs"
			}
			logs, err := filepath.Glob(filepath.Join(service.Dirs.StateRoot, logKind, "*", "*.log"))
			if err != nil || len(logs) != 2 {
				t.Fatalf("Author logs overwritten: %v %v", logs, err)
			}
			outcomes, err := filepath.Glob(filepath.Join(service.Dirs.StateRoot, "deliveries", "*", "*.log"))
			if err != nil || len(outcomes) != 2 {
				t.Fatalf("delivery observations missing: %v %v", outcomes, err)
			}
			for _, path := range outcomes {
				data, err := os.ReadFile(path)
				if err != nil || !strings.Contains(string(data), "push: confirmed success; PR: confirmed success") {
					t.Fatalf("missing outcome: %s %v", data, err)
				}
			}
		})
	}
}

func TestRemoteCollisionUsesExpandedPushEndpointOnce(t *testing.T) {
	const raw = "https://github.com/acme/iro.git"
	const endpoint = "git@github.com:acme/iro.git"
	for _, kind := range []string{"insteadOf", "pushInsteadOf", "alias collision", "namespace parent", "namespace child", "similar ref"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			advertisement := filepath.Join(dir, "advertisement")
			ssh := filepath.Join(dir, "ssh")
			transportLog := filepath.Join(dir, "transport.log")
			ref := "refs/heads/" + deliveryBranch(123, foundationID)
			switch kind {
			case "namespace parent":
				ref = "refs/heads/iro"
			case "namespace child":
				ref += "/nested/child"
			case "similar ref":
				ref += "-other"
			}
			line := foundationHEAD + " " + ref + "\x00\n"
			if err := os.WriteFile(advertisement, []byte(fmt.Sprintf("%04x%s0000", len(line)+4, line)), 0600); err != nil {
				t.Fatal(err)
			}
			// SSH is a local protocol stub: Git reads an advertised collision without
			// contacting GitHub, and the stub rejects the rewritten wrong endpoint.
			script := `#!/bin/sh
printf '%s\n' "$*" >> "$IRO_TEST_TRANSPORT_LOG"
case "$*" in
  *git@github.com*"git-upload-pack 'acme/iro.git'") cat "$IRO_TEST_ADVERTISEMENT" ;;
  *) echo "unexpected endpoint" >&2; exit 1 ;;
esac
`
			if err := os.WriteFile(ssh, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("GIT_SSH_COMMAND", ssh)
			t.Setenv("GIT_SSH_VARIANT", "ssh")
			t.Setenv("IRO_TEST_ADVERTISEMENT", advertisement)
			t.Setenv("IRO_TEST_TRANSPORT_LOG", transportLog)
			config := []string{"-c", "url.ssh://wrong.invalid/iro.git.insteadOf=" + endpoint}
			key := "insteadOf"
			if kind == "pushInsteadOf" {
				key = "pushInsteadOf"
			}
			config = append(config, "-c", "url."+endpoint+"."+key+"="+raw)
			if kind == "alias collision" {
				config = append(config, "-c", "url.ssh://wrong.invalid/iro.git.insteadOf=iro-push-endpoint:"+string(foundationID))
			}
			git := NewOSCommandRunner()
			// Allocation receives endpoint after get-url --push has already expanded
			// raw with insteadOf or pushInsteadOf. Only the subsequent read is tested.
			// Demonstrate the original bug with a read-only resolution command.
			args := append(append([]string{}, config...), "ls-remote", "--get-url", "--", endpoint)
			wrong := git.Run(CommandSpec{Name: "git", Args: args, Dir: dir})
			if !commandSucceeded(wrong) || strings.TrimSpace(wrong.Stdout) != "ssh://wrong.invalid/iro.git" {
				t.Fatalf("fixture did not reproduce double rewrite: %+v", wrong)
			}
			runner := &fakeCommandRunner{fn: func(spec CommandSpec) CommandResult {
				spec.Args = append(append([]string{}, config...), spec.Args...)
				return git.Run(spec)
			}}
			service := NewService(runner, NewOSFileSystem())
			allocation := &deliveryAllocation{id: foundationID, issue: 123, branch: deliveryBranch(123, foundationID), pushURL: endpoint}
			collision, err := service.inspectRemoteDeliveryCollision(dir, allocation)
			if kind == "alias collision" {
				if err == nil || len(runner.calls) != 1 {
					t.Fatalf("endpoint mismatch did not stop before remote read: %t %v", collision, err)
				}
				if _, err := os.Stat(transportLog); !os.IsNotExist(err) {
					t.Fatalf("unexpected transport invocation: %v", err)
				}
			} else if err != nil || collision != (kind != "similar ref") {
				t.Fatalf("incorrect collision at actual push endpoint: %t %v", collision, err)
			}
		})
	}
}

func TestRunRemoteNamespaceCollisions(t *testing.T) {
	for _, unmanaged := range []bool{false, true} {
		for _, phase := range []string{"allocation", "creation", "push"} {
			for _, kind := range []string{"parent", "child", "similar ref"} {
				name := fmt.Sprintf("unmanaged=%t/%s/%s", unmanaged, phase, kind)
				t.Run(name, func(t *testing.T) {
					var service *Service
					var intercept *func(CommandSpec) (CommandResult, bool)
					if unmanaged {
						f := newUnmanagedFixture(t)
						service, intercept = f.service, &f.intercept
					} else {
						f := newManagedProducerFixture(t)
						service, intercept = f.service, &f.intercept
					}
					ids, reads, adds, workers, pushes := 0, 0, 0, 0, 0
					service.newDeliveryID = func() (deliveryID, error) {
						ids++
						if ids == 1 {
							return foundationID, nil
						}
						return foundationOtherID, nil
					}
					*intercept = func(spec CommandSpec) (CommandResult, bool) {
						if spec.Name == "codex" && containsString(spec.Args, "--ephemeral") {
							workers++
						}
						if spec.Name != "git" {
							return CommandResult{}, false
						}
						if containsArgs(spec.Args, "worktree", "add") {
							adds++
						}
						if spec.Args[0] == "push" {
							pushes++
						}
						if spec.Args[0] != "ls-remote" || len(spec.Args) < 4 || !strings.HasPrefix(spec.Args[3], "iro-push-endpoint:") {
							return CommandResult{}, false
						}
						reads++
						ref := spec.Args[len(spec.Args)-2]
						if !containsString(spec.Args, "refs/heads/iro") || spec.Args[len(spec.Args)-1] != ref+"/*" {
							t.Fatalf("namespace not requested: %v", spec.Args)
						}
						if phase == "creation" && reads < 2 || phase == "push" && workers == 0 {
							return CommandResult{}, true
						}
						remoteRef := "refs/heads/iro"
						if kind == "child" {
							remoteRef = "refs/heads/" + deliveryBranch(123, foundationID) + "/nested/child"
						} else if kind == "similar ref" {
							remoteRef = ref + "-other"
						}
						// Model ls-remote's ref filtering; an exact-only query would
						// hide parents/children, reproducing the reviewed bug.
						for _, pattern := range spec.Args[4:] {
							if pattern == remoteRef || strings.HasSuffix(pattern, "/*") && strings.HasPrefix(remoteRef, strings.TrimSuffix(pattern, "*")) {
								return CommandResult{Stdout: foundationHEAD + "\t" + remoteRef + "\n"}, true
							}
						}
						return CommandResult{}, true
					}
					var out, diagnostic strings.Builder
					err := service.runWithOptions(123, workerOptions{Unmanaged: unmanaged}, &out, &diagnostic)
					if kind == "similar ref" || phase == "allocation" && kind == "child" {
						wantIDs := 1
						if kind == "child" {
							wantIDs = 2
							if !strings.Contains(out.String(), string(foundationOtherID)) {
								t.Fatal("replacement ID missing", out.String())
							}
						}
						if err != nil || ids != wantIDs || adds != 1 || workers != 1 || pushes != 1 {
							t.Fatalf("independent allocation failed: ids=%d adds=%d workers=%d pushes=%d err=%v", ids, adds, workers, pushes, err)
						}
					} else {
						if err == nil || pushes != 0 {
							t.Fatalf("collision not rejected: pushes=%d err=%v", pushes, err)
						}
						if phase == "allocation" {
							if ids != 16 || adds != 0 || workers != 0 {
								t.Fatalf("namespace parent caused side effects: ids=%d adds=%d workers=%d", ids, adds, workers)
							}
						} else {
							wantEffects := 0
							if phase == "push" {
								wantEffects = 1
							}
							if ids != 1 || adds != wantEffects || workers != wantEffects || !strings.Contains(err.Error(), string(foundationID)) {
								t.Fatalf("creation boundary changed: ids=%d adds=%d workers=%d err=%v", ids, adds, workers, err)
							}
						}
						if phase != "push" {
							if _, statErr := os.Stat(service.Dirs.DataRoot); !os.IsNotExist(statErr) {
								t.Fatalf("workspace directories created before rejection: %v", statErr)
							}
						}
					}
				})
			}
		}
	}
}

func TestRunAllocationCollisionsBeforeAndAfterCreation(t *testing.T) {
	for _, unmanaged := range []bool{false, true} {
		for _, collision := range []string{"local ref", "path", "remote ref", "creation failure", "late remote ref"} {
			t.Run(map[bool]string{false: "managed", true: "unmanaged"}[unmanaged]+"/"+collision, func(t *testing.T) {
				var service *Service
				var intercept *func(CommandSpec) (CommandResult, bool)
				if unmanaged {
					f := newUnmanagedFixture(t)
					service = f.service
					intercept = &f.intercept
				} else {
					f := newManagedProducerFixture(t)
					service = f.service
					intercept = &f.intercept
				}
				idCalls := 0
				service.newDeliveryID = func() (deliveryID, error) {
					idCalls++
					if idCalls == 1 {
						return foundationID, nil
					}
					return foundationOtherID, nil
				}
				identity := RepositoryIdentity{Owner: "acme", Name: "iro"}
				path := deliveryWorktreePath(service.Dirs, githubRuntimeNamespace(identity), 123, foundationID)
				if unmanaged {
					path = filepath.Join(runtimeWorkspaceParent(service.Dirs, githubRuntimeNamespace(identity), unmanagedWorkspace), detachedWorkspaceStem("run", 123)+string(foundationID))
				}
				if collision == "path" {
					if err := os.MkdirAll(path, 0755); err != nil {
						t.Fatal(err)
					}
				}
				workerDone := false
				adds, pushes := 0, 0
				*intercept = func(spec CommandSpec) (CommandResult, bool) {
					if spec.Name == "codex" && containsString(spec.Args, "--ephemeral") {
						workerDone = true
					}
					if spec.Name == "git" {
						if spec.Args[0] == "for-each-ref" && collision == "local ref" {
							return CommandResult{Stdout: "refs/heads/" + deliveryBranch(123, foundationID) + "\x00" + foundationHEAD + "\n"}, true
						}
						if containsArgs(spec.Args, "worktree", "add") {
							adds++
							if collision == "creation failure" {
								return CommandResult{ExitCode: 1, Stderr: "creation failed after reserving path"}, true
							}
						}
						if spec.Args[0] == "push" {
							pushes++
						}
						if spec.Args[0] == "ls-remote" && containsString(spec.Args, "refs/heads/"+deliveryBranch(123, foundationID)) && (collision == "remote ref" || collision == "late remote ref" && workerDone) {
							output := foundationHEAD + "\trefs/heads/" + deliveryBranch(123, foundationID) + "\n"
							if containsString(spec.Args, "refs/heads/release/topic") {
								output = foundationHEAD + "\trefs/heads/release/topic\n" + output
							}
							return CommandResult{Stdout: output}, true
						}
					}
					return CommandResult{}, false
				}
				var out, diagnostic strings.Builder
				err := service.runWithOptions(123, workerOptions{Unmanaged: unmanaged}, &out, &diagnostic)
				before := collision == "local ref" || collision == "path" || collision == "remote ref"
				if before {
					if err != nil || idCalls != 2 || adds != 1 || pushes != 1 || !strings.Contains(out.String(), string(foundationOtherID)) {
						t.Fatalf("pre-side-effect allocation: calls=%d adds=%d pushes=%d error=%v %s", idCalls, adds, pushes, err, diagnostic.String())
					}
				} else {
					if err == nil || idCalls != 1 || pushes != 0 || !strings.Contains(err.Error(), string(foundationID)) || !strings.Contains(err.Error(), path) {
						t.Fatalf("identity changed after creation: calls=%d pushes=%d err=%v", idCalls, pushes, err)
					}
					if _, statErr := os.Stat(path); statErr != nil {
						t.Fatalf("reserved path lost: %v", statErr)
					}
				}
			})
		}
	}
}

func TestRunRemoteFailuresDoNotRetry(t *testing.T) {
	for _, unmanaged := range []bool{false, true} {
		for _, stage := range []string{"push", "PR", "report"} {
			for _, unknown := range []bool{false, true} {
				name := map[bool]string{false: "managed", true: "unmanaged"}[unmanaged] + "/" + stage + map[bool]string{false: " failed", true: " unknown"}[unknown]
				t.Run(name, func(t *testing.T) {
					var service *Service
					var intercept *func(CommandSpec) (CommandResult, bool)
					if unmanaged {
						f := newUnmanagedFixture(t)
						service = f.service
						intercept = &f.intercept
					} else {
						f := newManagedProducerFixture(t)
						service = f.service
						intercept = &f.intercept
					}
					attempts := 0
					*intercept = func(spec CommandSpec) (CommandResult, bool) {
						match := stage == "push" && spec.Name == "git" && spec.Args[0] == "push" || stage == "PR" && spec.Name == "gh" && containsArgs(spec.Args, "--method", "POST") || stage == "report" && spec.Name == "gh" && containsArgs(spec.Args, "pr", "comment")
						if !match {
							return CommandResult{}, false
						}
						attempts++
						if unknown {
							return CommandResult{ExitCode: -1, Err: errors.New("transport interrupted")}, true
						}
						return CommandResult{ExitCode: 1, Stderr: "request rejected"}, true
					}
					var diagnostic strings.Builder
					err := service.runWithOptions(123, workerOptions{Unmanaged: unmanaged}, io.Discard, &diagnostic)
					if attempts != 1 || (err == nil) != (stage == "report") {
						t.Fatalf("retry/status: %d %v %s", attempts, err, diagnostic.String())
					}
					logs, logErr := filepath.Glob(filepath.Join(service.Dirs.StateRoot, "deliveries", "*", "*.log"))
					if logErr != nil || len(logs) != 1 {
						t.Fatalf("outcome log missing: %v %v", logs, logErr)
					}
					data, readErr := os.ReadFile(logs[0])
					text := string(data)
					for _, required := range []string{"Delivery ", "Issue #123", "branch iro/issue-123-", "workspace ", "unknown"} {
						if readErr != nil || !strings.Contains(text, required) {
							t.Fatalf("missing diagnostic %s: %s %v", required, text, readErr)
						}
					}
					if stage == "push" && !strings.Contains(text, "PR: not attempted") {
						t.Fatal(text)
					}
					if stage == "PR" && !strings.Contains(text, "push: confirmed success") {
						t.Fatal(text)
					}
				})
			}
		}
	}
}

func TestManagedRunUsesWorkspaceWorkflow(t *testing.T) {
	f := newManagedProducerFixture(t)
	f.intercept = func(spec CommandSpec) (CommandResult, bool) {
		if spec.Name == "git" && containsArgs(spec.Args, "worktree", "add") {
			// Invocation-side policy exists, but it is absent from the source tree.
			return CommandResult{}, true
		}
		if spec.Name == "codex" && containsString(spec.Args, "--ephemeral") {
			t.Fatal("worker started without workspace policy")
		}
		return CommandResult{}, false
	}
	if err := f.service.Run(123, io.Discard); err == nil {
		t.Fatal("missing workspace WORKFLOW accepted")
	}
}

func TestLaterExplicitRunAfterFailureUsesNewDelivery(t *testing.T) {
	for _, unmanaged := range []bool{false, true} {
		for _, stage := range []string{"worker", "push", "PR"} {
			t.Run(map[bool]string{false: "managed", true: "unmanaged"}[unmanaged]+"/"+stage, func(t *testing.T) {
				var service *Service
				var runner *fakeCommandRunner
				var intercept *func(CommandSpec) (CommandResult, bool)
				if unmanaged {
					f := newUnmanagedFixture(t)
					service = f.service
					runner = f.runner
					intercept = &f.intercept
				} else {
					f := newManagedProducerFixture(t)
					service = f.service
					runner = f.runner
					intercept = &f.intercept
				}
				first := true
				*intercept = func(spec CommandSpec) (CommandResult, bool) {
					match := stage == "worker" && spec.Name == "codex" && containsString(spec.Args, "--ephemeral") || stage == "push" && spec.Name == "git" && spec.Args[0] == "push" || stage == "PR" && spec.Name == "gh" && containsArgs(spec.Args, "--method", "POST")
					if first && match {
						return CommandResult{ExitCode: -1, Err: errors.New("interrupted"), Stdout: "partial Author report"}, true
					}
					return CommandResult{}, false
				}
				var out, diagnostic strings.Builder
				if err := service.runWithOptions(123, workerOptions{Unmanaged: unmanaged}, &out, &diagnostic); err == nil {
					t.Fatal("fault accepted")
				}
				first = false
				if err := service.runWithOptions(123, workerOptions{Unmanaged: unmanaged}, &out, &diagnostic); err != nil {
					t.Fatalf("later explicit Run blocked: %v", err)
				}
				var paths []string
				for _, call := range runner.calls {
					if call.Name == "git" && containsArgs(call.Args, "worktree", "add") {
						paths = append(paths, call.Args[len(call.Args)-2])
					}
				}
				if len(paths) != 2 || paths[0] == paths[1] {
					t.Fatalf("earlier delivery resumed: %v", paths)
				}
				if _, err := os.Stat(paths[0]); err != nil {
					t.Fatalf("failed delivery was rolled back: %v", err)
				}
				logs, err := filepath.Glob(filepath.Join(service.Dirs.StateRoot, "deliveries", "*", "*.log"))
				if err != nil || len(logs) != 2 {
					t.Fatalf("invocations share ID/log: %v %v", logs, err)
				}
			})
		}
	}
}
