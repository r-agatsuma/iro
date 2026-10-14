package iro

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func isInspectionCommand(spec CommandSpec) bool {
	return spec.Name == "codex" && containsString(spec.Args, "--output-schema") || spec.Name == "copilot" && strings.HasPrefix(string(spec.Stdin), inspectionPrompt+"\n")
}

type inspectionOperationFixture struct {
	service                    *Service
	runner                     *fakeCommandRunner
	head, agentType, operation string
	unmanaged                  bool
	execute                    func(workerOptions, io.Writer, io.Writer) error
}

func newInspectionOperationFixture(t *testing.T, mode string) inspectionOperationFixture {
	t.Helper()
	switch mode {
	case "managed Run", "Copilot Run":
		f := newManagedProducerFixture(t)
		agentType := "codex"
		if mode == "Copilot Run" {
			agentType = "copilot"
			t.Setenv("COPILOT_HOME", t.TempDir())
			writeCopilotConfig(t, f.root)
			f.intercept = func(spec CommandSpec) (CommandResult, bool) {
				if spec.Name == "codex" {
					t.Fatal("unselected backend invoked")
				}
				if spec.Name == "copilot" {
					if containsString(spec.Args, "--version") {
						return CommandResult{Stdout: "GitHub Copilot CLI 1.0.91"}, true
					}
					if containsString(spec.Args, "--help") {
						return CommandResult{Stdout: copilotHelpForTest}, true
					}
					return CommandResult{Stdout: copilotCompletionForTest("変更とテスト完了")}, true
				}
				return CommandResult{}, false
			}
		}
		return inspectionOperationFixture{f.service, f.runner, foundationHEAD, agentType, "run", false, func(options workerOptions, out, errOut io.Writer) error {
			return f.service.runWithOptions(123, options, out, errOut)
		}}
	case "unmanaged Run":
		f := newUnmanagedFixture(t)
		f.service.FileSystem = unmanagedFileGuard{f.service.FileSystem, t}
		return inspectionOperationFixture{f.service, f.runner, reviewBaseForTest, "codex", "run", true, func(options workerOptions, out, errOut io.Writer) error {
			options.Unmanaged = true
			return f.service.runWithOptions(123, options, out, errOut)
		}}
	case "managed Revise":
		f := newReviseFixture(t, false)
		return inspectionOperationFixture{f.service, f.runner, revisionHead, "codex", "revise", false, func(options workerOptions, out, errOut io.Writer) error {
			return f.service.reviseWithOptions(42, options, out)
		}}
	case "unmanaged Revise":
		f := newUnmanagedReviseFixture(t)
		return inspectionOperationFixture{f.service, f.runner, revisionHead, "codex", "revise", true, func(options workerOptions, out, errOut io.Writer) error {
			return f.service.reviseUnmanaged(42, 123, options, out, errOut)
		}}
	default:
		t.Fatalf("unknown operation fixture %s", mode)
		return inspectionOperationFixture{}
	}
}

var inspectedModes = []string{"managed Run", "unmanaged Run", "Copilot Run", "managed Revise", "unmanaged Revise"}

func isAuthorCommand(spec CommandSpec) bool {
	return !isInspectionCommand(spec) && (spec.Name == "codex" && containsString(spec.Args, "--ephemeral") || spec.Name == "copilot" && containsString(spec.Args, "--agent"))
}

func assertNoAuthorOrDelivery(t *testing.T, calls []CommandSpec) {
	t.Helper()
	for _, call := range calls {
		if isAuthorCommand(call) || call.Name == "git" && len(call.Args) > 0 && containsString([]string{"add", "commit", "push"}, call.Args[0]) || call.Name == "gh" && (containsString(call.Args, "POST") || containsArgs(call.Args, "pr", "create")) {
			t.Fatalf("Inspection stop reached Author/delivery: %+v", call)
		}
	}
}

func TestIssueInspectionPassInvokesSelectedAuthorWithSameOptions(t *testing.T) {
	for _, mode := range inspectedModes {
		t.Run(mode, func(t *testing.T) {
			f := newInspectionOperationFixture(t, mode)
			options := workerOptions{Model: "requested-model", ReasoningEffort: "high", NoSandbox: f.agentType == "codex"}
			var out, errOut strings.Builder
			if err := f.execute(options, &out, &errOut); err != nil {
				t.Fatal(err, out.String(), errOut.String())
			}
			var execution []CommandSpec
			var stages []string
			for _, call := range f.runner.calls {
				if call.Name == "codex" && containsArgs(call.Args, "login", "status") || call.Name == "copilot" && containsString(call.Args, "--help") {
					stages = append(stages, "preflight")
				}
				if isInspectionCommand(call) {
					stages = append(stages, "Inspection")
					execution = append(execution, call)
					payload := string(call.Stdin)
					for _, want := range []string{"Operation: " + f.operation, "Exact source/revision snapshot: " + f.head, "Issue number: 123", "Issue comments", "Applicable iro Author execution policy"} {
						if !strings.Contains(payload, want) {
							t.Fatalf("missing inspection input %s: %s", want, payload)
						}
					}
					if f.unmanaged && (!strings.Contains(payload, "Mode: unmanaged") || strings.Contains(payload, "Fixed starting PR HEAD")) {
						t.Fatal("unmanaged inspection adopted managed authority", payload)
					}
				}
				if isAuthorCommand(call) {
					stages = append(stages, "Author")
					execution = append(execution, call)
				}
				if call.Name == "codex" && f.agentType != "codex" || call.Name == "copilot" && f.agentType != "copilot" {
					t.Fatal("cross-runtime execution", call)
				}
			}
			if !reflect.DeepEqual(stages, []string{"preflight", "Inspection", "preflight", "Author"}) || len(execution) != 2 {
				t.Fatal("incorrect preflight/role order", stages)
			}
			if execution[0].Dir != execution[1].Dir {
				t.Fatal("Inspector and Author received different snapshot paths")
			}
			for _, call := range execution {
				if !containsArgs(call.Args, "--model", options.Model) {
					t.Fatal("model dropped", call.Args)
				}
				if f.agentType == "codex" {
					if !containsString(call.Args, `model_reasoning_effort="high"`) || !containsArgs(call.Args, "--sandbox", "danger-full-access") || containsString(call.Args, "sandbox_workspace_write.network_access=true") {
						t.Fatal("effort/sandbox option changed", call.Args)
					}
				} else if !containsArgs(call.Args, "--reasoning-effort", "high") {
					t.Fatal("native Copilot option dropped", call.Args)
				}
			}
			if !strings.Contains(out.String(), "INSPECTION: PASS") {
				t.Fatal(out.String())
			}
		})
	}
}

func TestCopilotInspectionExposesOnlyReadOnlyTools(t *testing.T) {
	for _, blocker := range []bool{false, true} {
		t.Run(map[bool]string{false: "PASS", true: "BLOCKER"}[blocker], func(t *testing.T) {
			f := newInspectionOperationFixture(t, "Copilot Run")
			inspections, authors := 0, 0
			checkTools := func(spec CommandSpec, tools string, allowURLs bool) {
				t.Helper()
				var available []string
				for _, arg := range spec.Args {
					if strings.HasPrefix(arg, "--available-tools=") {
						available = append(available, strings.TrimPrefix(arg, "--available-tools="))
					}
				}
				if !reflect.DeepEqual(available, []string{tools}) || containsString(spec.Args, "--allow-all-urls") != allowURLs {
					t.Fatal("incorrect role permissions", spec.Args)
				}
				for _, flag := range []string{"--allow-all", "--yolo", "--allow-all-paths"} {
					if containsString(spec.Args, flag) {
						t.Fatal("unexpected permission override", spec.Args)
					}
				}
				profiles, err := filepath.Glob(filepath.Join(spec.Env["COPILOT_HOME"], "agents", "*.agent.md"))
				if err != nil || len(profiles) != 1 {
					t.Fatal("missing private role profile", profiles, err)
				}
				data, err := os.ReadFile(profiles[0])
				if err != nil || !strings.Contains(string(data), "\ntools: ["+tools+"]\n") {
					t.Fatal("profile tool permissions disagree with invocation", string(data), err)
				}
			}
			f.runner.inspection = func(spec CommandSpec) CommandResult {
				inspections++
				checkTools(spec, "view,glob,grep", false)
				final := `{"verdict":"PASS","reason_class":"none","rationale":"ok"}`
				if blocker {
					final = `{"verdict":"BLOCKER","reason_class":"authority_conflict","rationale":"local"}`
				}
				return inspectionResultForTest(spec, final, CommandResult{})
			}
			previous := f.runner.fn
			f.runner.fn = func(spec CommandSpec) CommandResult {
				if isAuthorCommand(spec) {
					authors++
					checkTools(spec, copilotAuthorTools, true)
				}
				return previous(spec)
			}
			err := f.execute(workerOptions{}, io.Discard, io.Discard)
			if (err != nil) != blocker || inspections != 1 || authors != map[bool]int{false: 1, true: 0}[blocker] {
				t.Fatal("incorrect role execution", inspections, authors, err)
			}
			if blocker {
				assertNoAuthorOrDelivery(t, f.runner.calls)
			}
		})
	}
}

func TestCopilotInspectionValidatesReadOnlyToolCompletion(t *testing.T) {
	for _, tool := range append(strings.Split(copilotAuthorTools, ","), "web_fetch", "task") {
		t.Run(tool, func(t *testing.T) {
			f := newInspectionOperationFixture(t, "Copilot Run")
			f.runner.inspection = func(spec CommandSpec) CommandResult {
				stream := copilotToolCompletionForTest(tool, `{"toolCallId":"x","success":true,"shellExecution":{"exitCode":0}}`)
				stream = strings.Replace(stream, copilotCompletionForTest("完了"), copilotCompletionForTest(`{"verdict":"PASS","reason_class":"none","rationale":"ok"}`), 1)
				return CommandResult{Stdout: stream}
			}
			readOnly := containsString([]string{"view", "glob", "grep"}, tool)
			err := f.execute(workerOptions{}, io.Discard, io.Discard)
			if readOnly {
				if err != nil {
					t.Fatal("read-only inspection tool rejected", err)
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), "INSPECTION: FAILED") {
					t.Fatal("unexpected tool use accepted", tool, err)
				}
				assertNoAuthorOrDelivery(t, f.runner.calls)
			}
		})
	}
}

func TestCopilotInspectionPreflightDoesNotRequireAuthorURLPermissions(t *testing.T) {
	f := newInspectionOperationFixture(t, "Copilot Run")
	previous := f.runner.fn
	f.runner.fn = func(spec CommandSpec) CommandResult {
		if spec.Name == "copilot" && containsString(spec.Args, "--help") {
			return CommandResult{Stdout: strings.ReplaceAll(copilotHelpForTest, "--allow-all-urls", "")}
		}
		return previous(spec)
	}
	f.runner.inspection = func(spec CommandSpec) CommandResult {
		return inspectionResultForTest(spec, `{"verdict":"BLOCKER","reason_class":"authority_conflict","rationale":"local"}`, CommandResult{})
	}
	if err := f.execute(workerOptions{}, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "INSPECTION: BLOCKER") {
		t.Fatal("Author capability blocked Inspector startup", err)
	}
	assertNoAuthorOrDelivery(t, f.runner.calls)
	if err := (copilotRuntime{runner: f.runner, files: f.service.FileSystem}).preflight(""); err == nil || !strings.Contains(err.Error(), "--allow-all-urls") {
		t.Fatal("Author preflight omitted its required capability", err)
	}
}

func TestIssueInspectionStopsWithoutAuthorDeliveryOrDiagnosticPublication(t *testing.T) {
	for _, mode := range inspectedModes {
		for _, outcome := range []string{"BLOCKER", "crash", "timeout", "malformed", "unknown reason", "missing final", "unusable completion", "stdout PASS only"} {
			t.Run(mode+"/"+outcome, func(t *testing.T) {
				f := newInspectionOperationFixture(t, mode)
				f.runner.inspection = func(spec CommandSpec) CommandResult {
					result := CommandResult{Stdout: stdoutSentinel, Stderr: stderrSentinel}
					final := `{"verdict":"BLOCKER","reason_class":"authority_conflict","rationale":"` + finalSentinel + `"}`
					switch outcome {
					case "crash":
						result.ExitCode, result.Err = 7, errors.New(subprocessError)
					case "timeout":
						result.Err = context.DeadlineExceeded
					case "malformed":
						final = "STOP " + finalSentinel
					case "unknown reason":
						final = strings.Replace(final, "authority_conflict", operationSentinel, 1)
					case "missing final":
						return result
					case "unusable completion":
						result.Err = errors.New(subprocessError)
					case "stdout PASS only":
						result.Stdout = `{"verdict":"PASS","reason_class":"none","rationale":""}`
						final = ""
					}
					return inspectionResultForTest(spec, final, result)
				}
				var out, errOut strings.Builder
				err := f.execute(workerOptions{}, &out, &errOut)
				status := "FAILED"
				if outcome == "BLOCKER" {
					status = "BLOCKER"
				}
				if err == nil || !strings.Contains(err.Error(), "INSPECTION: "+status) {
					t.Fatal(err, out.String(), errOut.String())
				}
				for _, want := range []string{"INSPECTION: " + status, "AUTHOR: not started", "DELIVERY: not attempted"} {
					if !strings.Contains(out.String(), want) {
						t.Fatal(out.String())
					}
				}
				assertNoAuthorOrDelivery(t, f.runner.calls)
				preflights := 0
				for _, call := range f.runner.calls {
					if call.Name == "codex" && containsArgs(call.Args, "login", "status") || call.Name == "copilot" && containsString(call.Args, "--help") {
						preflights++
					}
				}
				if preflights != 1 {
					t.Fatalf("Author preflight happened before PASS: %d", preflights)
				}
				comments := commentPayloads(t, f.runner.calls)
				wantComments := 0
				if f.operation == "run" {
					wantComments = 1
				}
				if len(comments) != wantComments {
					t.Fatalf("unexpected report targets: %v", comments)
				}
				for _, report := range comments {
					for _, forbidden := range []string{stdoutSentinel, stderrSentinel, finalSentinel, subprocessError, operationSentinel, f.service.Dirs.StateRoot, "STOP"} {
						if strings.Contains(report, forbidden) {
							t.Fatal("Inspector diagnostic published", report)
						}
					}
					if !strings.Contains(report, "INSPECTION: "+status) || !strings.Contains(report, "Stage: inspection") || status == "FAILED" && strings.Contains(report, "BLOCKER") {
						t.Fatal("incorrect bounded inspection report", report)
					}
					if status == "BLOCKER" && !strings.Contains(report, "Reason class: authority_conflict") {
						t.Fatal(report)
					}
				}
				logs, _ := filepath.Glob(filepath.Join(f.service.Dirs.StateRoot, "inspections", "*", "*", "diagnostic.log"))
				if len(logs) != 1 {
					t.Fatal("missing local diagnostic", logs)
				}
				data, err := os.ReadFile(logs[0])
				if err != nil || !strings.Contains(string(data), stderrSentinel) || outcome == "BLOCKER" && !strings.Contains(string(data), finalSentinel) {
					t.Fatal("local Inspector diagnostics lost", string(data), err)
				}
			})
		}
	}
}

func TestInspectorPreflightFailureIsFailedWithoutBackendFallback(t *testing.T) {
	for _, mode := range inspectedModes {
		for _, failure := range []string{"missing executable", "unusable"} {
			t.Run(mode+"/"+failure, func(t *testing.T) {
				f := newInspectionOperationFixture(t, mode)
				if failure == "missing executable" {
					f.runner.lookups = map[string]error{f.agentType: os.ErrNotExist}
				} else if f.agentType == "codex" {
					f.runner.inspectionHelp = &CommandResult{Stdout: "old codex"}
				} else {
					previous := f.runner.fn
					f.runner.fn = func(spec CommandSpec) CommandResult {
						if spec.Name == "copilot" && containsString(spec.Args, "--help") {
							return CommandResult{ExitCode: 1}
						}
						return previous(spec)
					}
				}
				var out, errOut strings.Builder
				if err := f.execute(workerOptions{}, &out, &errOut); err == nil || !strings.Contains(err.Error(), "INSPECTION: FAILED") {
					t.Fatal(err, out.String())
				}
				assertNoAuthorOrDelivery(t, f.runner.calls)
				for _, call := range f.runner.calls {
					if isInspectionCommand(call) || f.agentType == "copilot" && call.Name == "codex" || f.agentType == "codex" && call.Name == "copilot" {
						t.Fatal("Inspection started or backend fallback", call)
					}
				}
				comments := commentPayloads(t, f.runner.calls)
				if f.operation == "run" && len(comments) != 1 || f.operation == "revise" && len(comments) != 0 {
					t.Fatal(comments)
				}
				for _, body := range comments {
					if !strings.Contains(body, "INSPECTION: FAILED") || strings.Contains(body, "BLOCKER") {
						t.Fatal(body)
					}
				}
			})
		}
	}
}

func TestPassDoesNotReclassifyAuthorPreflightOrWorkerFailures(t *testing.T) {
	for _, mode := range inspectedModes {
		for _, stage := range []string{"Author preflight", "Author runtime"} {
			t.Run(mode+"/"+stage, func(t *testing.T) {
				f := newInspectionOperationFixture(t, mode)
				f.runner.inspection = func(spec CommandSpec) CommandResult {
					if stage == "Author preflight" {
						f.runner.lookups = map[string]error{f.agentType: os.ErrNotExist}
					}
					return inspectionResultForTest(spec, `{"verdict":"PASS","reason_class":"none","rationale":"ok"}`, CommandResult{})
				}
				previous := f.runner.fn
				f.runner.fn = func(spec CommandSpec) CommandResult {
					if stage == "Author runtime" && isAuthorCommand(spec) {
						return CommandResult{ExitCode: 1, Stdout: "test failure " + finalSentinel}
					}
					return previous(spec)
				}
				var out, errOut strings.Builder
				if err := f.execute(workerOptions{}, &out, &errOut); err == nil || strings.Contains(err.Error(), "INSPECTION: BLOCKER") || strings.Contains(err.Error(), "INSPECTION: FAILED") {
					t.Fatal(err, out.String())
				}
				if !strings.Contains(out.String(), "INSPECTION: PASS") {
					t.Fatal(out.String())
				}
				for _, report := range commentPayloads(t, f.runner.calls) {
					if strings.Contains(report, "Stage: inspection") || strings.Contains(report, "BLOCKER") || strings.Contains(report, finalSentinel) {
						t.Fatal("later failure reclassified/published prose", report)
					}
				}
			})
		}
	}
}

func TestInspectorReceivesBlockerSpecificationAndRepositoryPrerequisites(t *testing.T) {
	for _, tc := range []struct {
		body   string
		reason inspectionReason
	}{
		{"Use the predecessor capability in required/predecessor.go; it must already exist.", missingSourcePrerequisite},
		{"The Author must push its commit and create a PR before exiting.", authorityConflict},
		{"Before Author exit, verify the exact pushed commit on the newly created remote PR.", executionLifecycleMismatch},
		{"Implement the missing normative specification supplied only in an unavailable document.", requiredContextUnavailable},
		{"Choose a new product architecture without a Human decision: mutually exclusive options A and B are both required.", specificationAmbiguity},
	} {
		t.Run(tc.reason.name(), func(t *testing.T) {
			f := newInspectionOperationFixture(t, "managed Run")
			previous := f.runner.fn
			f.runner.fn = func(spec CommandSpec) CommandResult {
				result := previous(spec)
				if spec.Name == "gh" && containsArgs(spec.Args, "issue", "view") {
					var issue map[string]any
					if err := json.Unmarshal([]byte(result.Stdout), &issue); err != nil {
						t.Fatal(err)
					}
					issue["body"] = tc.body
					data, _ := json.Marshal(issue)
					result.Stdout = string(data)
				}
				return result
			}
			f.runner.inspection = func(spec CommandSpec) CommandResult {
				if !strings.Contains(string(spec.Stdin), tc.body) || !strings.Contains(string(spec.Stdin), "Exact source/revision snapshot: "+f.head) {
					t.Fatal("specification/snapshot missing")
				}
				if _, err := os.Stat(filepath.Join(spec.Dir, "required", "predecessor.go")); !os.IsNotExist(err) {
					t.Fatal("missing prerequisite was not exposed", err)
				}
				for _, restriction := range []string{"non-authoring Inspector", "Do not edit any repository/worktree files", "Do not start the Author", "traverse tracker/network state", "missing predecessor source capability", "post-delivery verification", "iro owns commit/push/PR/delivery after Author exit"} {
					if !strings.Contains(strings.Join(spec.Args, " "), restriction) {
						t.Fatal("missing Inspector responsibility/authority", restriction)
					}
				}
				return inspectionResultForTest(spec, `{"verdict":"BLOCKER","reason_class":"`+tc.reason.name()+`","rationale":"local"}`, CommandResult{})
			}
			if err := f.execute(workerOptions{}, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), tc.reason.name()) {
				t.Fatal(err)
			}
			assertNoAuthorOrDelivery(t, f.runner.calls)
		})
	}
}

func TestNonAuthorOperationsDoNotInvokeIssueInspection(t *testing.T) {
	for _, operation := range []string{"managed Review", "unmanaged Review", "managed Land", "unmanaged Land", "Status", "Cleanup", "Doctor"} {
		t.Run(operation, func(t *testing.T) {
			var runner *fakeCommandRunner
			var execute func() error
			switch operation {
			case "managed Land", "unmanaged Land":
				f := newLandFixture(t)
				args := []string{"land", "42"}
				if operation == "unmanaged Land" {
					f = newUnmanagedLandFixture(t)
					args = append(args, "--unmanaged")
				}
				runner = f.runner
				execute = func() error {
					var errOut strings.Builder
					if code := Execute(args, io.Discard, &errOut, f.service); code != 0 {
						return errors.New(errOut.String())
					}
					return nil
				}
			case "Status", "Cleanup":
				f := newLocalLifecycleFixture(t)
				runner = f.runner
				if operation == "Status" {
					execute = func() error { return f.service.Status(io.Discard) }
				} else {
					execute = func() error { return f.service.CleanupAll(io.Discard) }
				}
			case "managed Review", "unmanaged Review", "Doctor":
				root := t.TempDir()
				writeProjectFiles(t, root)
				workspace, registered := "", false
				runner = &fakeCommandRunner{fn: func(spec CommandSpec) CommandResult {
					if operation == "Doctor" {
						return standardFakeResult(spec, root, "", false, false)
					}
					if operation == "unmanaged Review" {
						if spec.Name == "git" {
							if spec.Args[0] == "remote" {
								return CommandResult{Stdout: "git@github.com:acme/iro.git"}
							}
							if containsArgs(spec.Args, "worktree", "add") {
								workspace, registered = spec.Args[3], true
								return CommandResult{}
							}
							if containsArgs(spec.Args, "worktree", "remove") {
								registered = false
								return CommandResult{Err: os.RemoveAll(workspace)}
							}
							if spec.Args[0] == "symbolic-ref" {
								return CommandResult{ExitCode: 1}
							}
							if result, ok := unmanagedInventoryResult(spec, root, workspace, reviewHeadForTest, registered); ok {
								return result
							}
						}
						if spec.Name == "gh" && containsString(spec.Args, "graphql") {
							return CommandResult{Stdout: strings.ReplaceAll(reviewResponseForTest, "contributor/iro", "acme/iro")}
						}
						if spec.Name == "gh" && containsArgs(spec.Args, "--method", "POST") {
							return CommandResult{Stdout: "HTTP/2.0 201 Created\r\n\r\n{\"id\":1}"}
						}
					}
					return reviewFakeResult(spec, root, "review report")
				}}
				service := newTestService(t, runner, root)
				switch operation {
				case "Doctor":
					execute = func() error { return service.Doctor(io.Discard) }
				case "managed Review":
					execute = func() error { return service.Review(42, io.Discard) }
				case "unmanaged Review":
					execute = func() error { return service.reviewUnmanaged(42, 123, workerOptions{}, io.Discard, io.Discard) }
				}
			}
			runner.inspection = func(spec CommandSpec) CommandResult {
				t.Fatal("excluded operation invoked Inspector", spec)
				return CommandResult{}
			}
			if err := execute(); err != nil {
				t.Fatal(err)
			}
			for _, call := range runner.calls {
				if isInspectionCommand(call) || operation != "Doctor" && call.Name == "codex" && containsArgs(call.Args, "exec", "--help") {
					t.Fatal("excluded operation performed inspection/preflight", call)
				}
			}
		})
	}
}

func TestDoctorDiagnosesBothRolesOnlyForSelectedRuntime(t *testing.T) {
	for _, agentType := range []string{"codex", "copilot"} {
		for _, inspectionUsable := range []bool{true, false} {
			t.Run(agentType+"/"+map[bool]string{true: "ready", false: "missing capability"}[inspectionUsable], func(t *testing.T) {
				root := t.TempDir()
				writeProjectFiles(t, root)
				unselected := "copilot"
				if agentType == "copilot" {
					writeCopilotConfig(t, root)
					unselected = "codex"
				}
				runner := &fakeCommandRunner{lookups: map[string]error{unselected: os.ErrNotExist}}
				if !inspectionUsable && agentType == "codex" {
					runner.inspectionHelp = &CommandResult{Stdout: "--output-last-message"}
				}
				copilotProbes := 0
				runner.fn = func(spec CommandSpec) CommandResult {
					if spec.Name == unselected {
						t.Fatal("Doctor introduced unselected dependency", spec)
					}
					if spec.Name == "copilot" {
						if containsString(spec.Args, "--version") {
							return CommandResult{Stdout: "GitHub Copilot CLI 1.0.91"}
						}
						if containsString(spec.Args, "--help") {
							copilotProbes++
							if !inspectionUsable && copilotProbes == 1 {
								return CommandResult{Stdout: "missing required flags"}
							}
							return CommandResult{Stdout: copilotHelpForTest}
						}
						t.Fatal("Doctor requested provider/agent execution", spec)
					}
					return standardFakeResult(spec, root, "", false, false)
				}
				var out strings.Builder
				err := newTestService(t, runner, root).Doctor(&out)
				if (err == nil) != inspectionUsable {
					t.Fatal(err, out.String())
				}
				name := "Codex"
				if agentType == "copilot" {
					name = "Copilot"
				}
				for _, role := range []string{"Inspection readiness", "Author readiness"} {
					if !strings.Contains(out.String(), name+" "+role) {
						t.Fatal(out.String())
					}
				}
				if !inspectionUsable && (!strings.Contains(out.String(), "FAIL: "+name+" Inspection") || !strings.Contains(out.String(), "OK: "+name+" Author")) {
					t.Fatal(out.String())
				}
				for _, call := range runner.calls {
					if isInspectionCommand(call) || isAuthorCommand(call) {
						t.Fatal("Doctor ran an agent", call)
					}
				}
			})
		}
	}
}

func TestRevisionInspectorUsesFixedStartingPolicyAndSameIssueComments(t *testing.T) {
	f := newReviseFixture(t, false)
	const policy = "FIXED_PR_SOURCE_POLICY_117"
	const invocationPolicy = "INVOCATION_POLICY_NOT_AUTHORITATIVE_117"
	if err := os.WriteFile(filepath.Join(f.root, "WORKFLOW.md"), []byte(invocationPolicy), 0600); err != nil {
		t.Fatal(err)
	}
	reads := 0
	f.runner.fn = func(spec CommandSpec) CommandResult {
		if spec.Name == "git" && containsArgs(spec.Args, "cat-file", "blob") {
			reads++
			return CommandResult{Stdout: policy}
		}
		if isAuthorCommand(spec) && (!strings.Contains(string(spec.Stdin), policy) || strings.Contains(string(spec.Stdin), invocationPolicy)) {
			t.Fatal("Author received different policy", string(spec.Stdin))
		}
		return f.respond(spec)
	}
	f.runner.inspection = func(spec CommandSpec) CommandResult {
		input := string(spec.Stdin)
		for _, want := range []string{"Fixed starting PR HEAD " + revisionHead, policy, "ID: IC_1", "Author: human", "Issue decision"} {
			if !strings.Contains(input, want) {
				t.Fatal("Inspector missing fixed authority/context", input)
			}
		}
		if strings.Contains(input, invocationPolicy) {
			t.Fatal("Inspector adopted invoking policy")
		}
		return inspectionResultForTest(spec, `{"verdict":"PASS","reason_class":"none","rationale":"ok"}`, CommandResult{})
	}
	if err := f.service.Revise(42, io.Discard); err != nil {
		t.Fatal(err)
	}
	if reads != 1 {
		t.Fatalf("Inspector/Author did not share one fixed policy read: %d", reads)
	}
}

type inspectionLogFailureFS struct{ FileSystem }

func (f inspectionLogFailureFS) WriteFile(path string, data []byte, mode os.FileMode) error {
	if filepath.Base(path) == "diagnostic.log" {
		return errors.New(operationSentinel)
	}
	return f.FileSystem.WriteFile(path, data, mode)
}

func TestInspectorDiagnosticStorageFailureStopsAndKeepsProseLocal(t *testing.T) {
	f := newInspectionOperationFixture(t, "managed Run")
	f.service.FileSystem = inspectionLogFailureFS{f.service.FileSystem}
	f.runner.inspection = func(spec CommandSpec) CommandResult {
		return inspectionResultForTest(spec, `{"verdict":"PASS","reason_class":"none","rationale":"`+finalSentinel+`"}`, CommandResult{Stdout: stdoutSentinel, Stderr: stderrSentinel})
	}
	var out, errOut strings.Builder
	if err := f.execute(workerOptions{}, &out, &errOut); err == nil || !strings.Contains(err.Error(), "INSPECTION: FAILED") {
		t.Fatal(err)
	}
	assertNoAuthorOrDelivery(t, f.runner.calls)
	for _, want := range []string{stdoutSentinel, stderrSentinel, finalSentinel} {
		if !strings.Contains(errOut.String(), want) {
			t.Fatal("lost local diagnostic", errOut.String())
		}
	}
	comments := commentPayloads(t, f.runner.calls)
	if len(comments) != 1 {
		t.Fatal(comments)
	}
	for _, want := range []string{stdoutSentinel, stderrSentinel, finalSentinel, operationSentinel} {
		if strings.Contains(comments[0], want) {
			t.Fatal("published local diagnostic", comments[0])
		}
	}
}

func inspectionResultForTest(spec CommandSpec, final string, result CommandResult) CommandResult {
	if spec.Name == "codex" {
		for i, arg := range spec.Args {
			if arg == "--output-last-message" {
				if err := os.WriteFile(spec.Args[i+1], []byte(final), 0600); err != nil {
					panic(err)
				}
			}
		}
	} else {
		result.Stdout = copilotCompletionForTest(final)
	}
	return result
}

func TestInspectionStructuredVerdictFailsClosed(t *testing.T) {
	for _, output := range []string{
		"PASS", "STOP", "Human decision required", "blocked", "", "{}", "null",
		`{"verdict":"PASS","reason_class":"none"}`,
		`{"verdict":"PASS","reason_class":"none","rationale":null}`,
		`{"verdict":"PASS","reason_class":"none","rationale":"ok","extra":"PASS"}`,
		`{"verdict":"BLOCKER","reason_class":"none","rationale":"wrong"}`,
		`{"verdict":"PASS","reason_class":"authority_conflict","rationale":"wrong"}`,
		`{"verdict":"BLOCKER","reason_class":"secret","rationale":"wrong"}`,
		`{"verdict":"BLOCKED","reason_class":"authority_conflict","rationale":"wrong"}`,
		`{"verdict":"BLOCKER","verdict":"PASS","reason_class":"none","rationale":"wrong"}`,
		`{"verdict":"PASS","reason_class":"none","rationale":"ok"} {}`,
	} {
		if _, err := parseInspectionVerdict(output); err == nil {
			t.Errorf("accepted %s", output)
		}
	}
	for reason := noInspectionReason; reason <= requiredContextUnavailable; reason++ {
		verdict := "BLOCKER"
		if reason == noInspectionReason {
			verdict = "PASS"
		}
		output := `{"verdict":"` + verdict + `","reason_class":"` + reason.name() + `","rationale":"local"}`
		got, err := parseInspectionVerdict(output)
		if err != nil || got.blocker != (reason != noInspectionReason) || got.reason != reason {
			t.Fatalf("%s: %+v, %v", output, got, err)
		}
	}
}
