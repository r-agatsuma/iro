package iro

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

const (
	workspaceSentinel = "PRIVATE_WORKSPACE_116"
	stateSentinel     = "PRIVATE_STATE_116"
	operationSentinel = "PRIVATE_OPERATION_ERROR_116"
	stdoutSentinel    = "PRIVATE_WORKER_STDOUT_116"
	stderrSentinel    = "PRIVATE_WORKER_STDERR_116"
	finalSentinel     = "PRIVATE_AUTHOR_FINAL_116"
	subprocessOut     = "PRIVATE_SUBPROCESS_STDOUT_116"
	subprocessErr     = "PRIVATE_SUBPROCESS_STDERR_116"
	subprocessError   = "PRIVATE_SUBPROCESS_ERROR_116"
)

// Capture every comment attempt, including failed writes and API publications.
func commentPayloads(t *testing.T, calls []CommandSpec) []string {
	t.Helper()
	var bodies []string
	for _, call := range calls {
		if call.Name != "gh" {
			continue
		}
		if containsArgs(call.Args, "issue", "comment") || containsArgs(call.Args, "pr", "comment") {
			for i, arg := range call.Args {
				if arg == "--body" && i+1 < len(call.Args) {
					bodies = append(bodies, call.Args[i+1])
				}
			}
		}
		if len(call.Args) >= 2 && call.Args[0] == "api" && strings.HasSuffix(call.Args[1], "/comments") && containsArgs(call.Args, "--method", "POST") {
			var payload struct{ Body string }
			if err := json.Unmarshal(call.Stdin, &payload); err != nil {
				t.Fatal(err)
			}
			bodies = append(bodies, payload.Body)
		}
	}
	return bodies
}

type publicationLogFailureFS struct{ FileSystem }

func (f publicationLogFailureFS) MkdirAll(path string, mode os.FileMode) error {
	if strings.Contains(path, string(filepath.Separator)+"runs"+string(filepath.Separator)) || strings.Contains(path, string(filepath.Separator)+"unmanaged-runs"+string(filepath.Separator)) {
		return &os.PathError{Op: "mkdir", Path: path, Err: errors.New(operationSentinel)}
	}
	return f.FileSystem.MkdirAll(path, mode)
}

func TestAutomaticPublicationKeepsDiagnosticsLocal(t *testing.T) {
	for _, mode := range []string{"managed", "unmanaged", "copilot"} {
		for _, scenario := range []string{"worker failure", "worker error with zero exit", "worker and log failure", "log failure", "stage failure", "push failure", "PR failure", "success", "delivery comment failure", "failure comment failure"} {
			t.Run(mode+"/"+scenario, func(t *testing.T) {
				var service *Service
				var runner *fakeCommandRunner
				var intercept *func(CommandSpec) (CommandResult, bool)
				prNumber := 456
				if mode == "unmanaged" {
					f := newUnmanagedFixture(t)
					service, runner, intercept = f.service, f.runner, &f.intercept
					prNumber = 42
				} else {
					f := newManagedProducerFixture(t)
					service, runner, intercept = f.service, f.runner, &f.intercept
					if mode == "copilot" {
						t.Setenv("COPILOT_HOME", t.TempDir())
						writeCopilotConfig(t, f.root)
					}
				}
				service.Dirs.DataRoot = filepath.Join(t.TempDir(), workspaceSentinel)
				service.Dirs.StateRoot = filepath.Join(t.TempDir(), stateSentinel)
				logFailure := strings.Contains(scenario, "log failure")
				if logFailure {
					service.FileSystem = publicationLogFailureFS{service.FileSystem}
				}
				workerFailure := strings.HasPrefix(scenario, "worker") || scenario == "failure comment failure"
				worker := CommandResult{
					Stdout: stdoutSentinel + "\n" + finalSentinel + "\n" + subprocessOut,
					Stderr: stderrSentinel + "\n" + subprocessErr,
				}
				if workerFailure {
					worker.ExitCode = 9
					worker.Err = errors.New(operationSentinel + " " + subprocessError)
					if scenario == "worker error with zero exit" {
						worker.ExitCode = 0
					}
				}
				if mode == "copilot" {
					worker.Stdout = copilotCompletionForTest(worker.Stdout)
				}
				*intercept = func(spec CommandSpec) (CommandResult, bool) {
					if spec.Name == "copilot" {
						if containsString(spec.Args, "--version") {
							return CommandResult{Stdout: "GitHub Copilot CLI 1.0.92."}, true
						}
						if containsString(spec.Args, "--help") {
							return CommandResult{Stdout: copilotHelpForTest}, true
						}
						return worker, true
					}
					if spec.Name == "codex" && containsString(spec.Args, "--ephemeral") {
						return worker, true
					}
					fail := scenario == "stage failure" && spec.Name == "git" && spec.Args[0] == "add" ||
						scenario == "push failure" && spec.Name == "git" && spec.Args[0] == "push" ||
						scenario == "PR failure" && spec.Name == "gh" && containsArgs(spec.Args, "--method", "POST") ||
						scenario == "delivery comment failure" && spec.Name == "gh" && containsArgs(spec.Args, "pr", "comment") ||
						scenario == "failure comment failure" && spec.Name == "gh" && containsArgs(spec.Args, "issue", "comment")
					if fail {
						return CommandResult{ExitCode: -1, Stdout: subprocessOut, Stderr: subprocessErr, Err: errors.New(subprocessError)}, true
					}
					return CommandResult{}, false
				}
				var out, diagnostic strings.Builder
				args := []string{"run", "123"}
				if mode == "unmanaged" {
					args = append(args, "--unmanaged")
				}
				code := Execute(args, &out, &diagnostic, service)
				delivered := scenario == "success" || scenario == "delivery comment failure"
				if (code == 0) != delivered {
					t.Fatalf("exit=%d, diagnostic=%s", code, diagnostic.String())
				}
				bodies := commentPayloads(t, runner.calls)
				if len(bodies) != 1 {
					t.Fatalf("expected one automatic comment, got %v", bodies)
				}
				for _, body := range bodies {
					for _, sentinel := range []string{workspaceSentinel, stateSentinel, operationSentinel, stdoutSentinel, stderrSentinel, finalSentinel, subprocessOut, subprocessErr, subprocessError, service.Dirs.DataRoot, service.Dirs.StateRoot} {
						if strings.Contains(body, sentinel) {
							t.Fatalf("local diagnostic %q crossed publication boundary: %s", sentinel, body)
						}
					}
					status, stage := "FAILED", "delivery"
					if delivered {
						status = "DELIVERED"
					} else if workerFailure {
						stage = "author"
					}
					for _, fact := range []string{"Status: " + status, "Stage: " + stage, "Source Issue: #123"} {
						if !strings.Contains(body, fact) {
							t.Fatalf("missing bounded fact %q: %s", fact, body)
						}
					}
					if delivered {
						if !strings.Contains(body, "PR: #"+strconv.Itoa(prNumber)) || !strings.Contains(body, "Delivery confirmed: true") || strings.Contains(body, "iro land") != (mode != "unmanaged") {
							t.Fatalf("invalid delivery facts: %s", body)
						}
					} else if !strings.Contains(body, "Delivery confirmed: false") || !strings.Contains(body, "ローカル") {
						t.Fatalf("missing failure facts or local guidance: %s", body)
					}
				}
				local := out.String() + diagnostic.String()
				if !logFailure {
					logKind := "runs"
					if mode == "unmanaged" {
						logKind = "unmanaged-runs"
					}
					logs, err := filepath.Glob(filepath.Join(service.Dirs.StateRoot, logKind, "*", "*.log"))
					if err != nil || len(logs) != 1 {
						t.Fatalf("missing worker log: %v %v", logs, err)
					}
					data, err := os.ReadFile(logs[0])
					if err != nil {
						t.Fatal(err)
					}
					local += string(data)
				}
				for _, sentinel := range []string{workspaceSentinel, stdoutSentinel, stderrSentinel, finalSentinel, subprocessOut, subprocessErr} {
					if !strings.Contains(local, sentinel) {
						t.Fatalf("local diagnostic %q was lost", sentinel)
					}
				}
				if (workerFailure || logFailure) && !strings.Contains(local, operationSentinel) {
					t.Fatal("operation error was lost locally")
				}
				if logFailure && !strings.Contains(local, stateSentinel) {
					t.Fatal("state path was lost locally")
				}
			})
		}
	}
}

func TestAutomaticReportTypeExcludesUnrestrictedValues(t *testing.T) {
	typ := reflect.TypeOf(automaticReport{})
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		switch field.Type.Kind() {
		case reflect.Int, reflect.Uint8, reflect.Bool:
		default:
			t.Fatalf("automatic report field %s can carry unrestricted diagnostics: %s", field.Name, field.Type)
		}
	}
}

func TestAutomaticPublicationRejectsInvalidFactsBeforeTrackerIO(t *testing.T) {
	for _, report := range []automaticReport{
		{},
		{issue: 123, stage: reportStage(255), status: reportFailed},
		{issue: 123, stage: reportAuthor, status: reportStatus(255)},
		{stage: reportAuthor, status: reportFailed},
		{issue: 123, stage: reportDelivery, status: reportFailed, prNumber: -1},
		{issue: 123, stage: reportAuthor, status: reportDelivered, prNumber: 42},
		{issue: 123, stage: reportDelivery, status: reportDelivered},
		{issue: 123, stage: reportDelivery, status: reportFailed, land: true},
	} {
		body, err := buildAutomaticReport(report)
		if err == nil || body != "" {
			t.Fatalf("invalid facts produced a body: %+v, %q, %v", report, body, err)
		}
		runner := &fakeCommandRunner{}
		service := newTestService(t, runner, t.TempDir())
		identity := RepositoryIdentity{Owner: "acme", Name: "iro"}
		if err := service.postIssueReport("", identity, report); err == nil {
			t.Fatal("invalid Issue report accepted")
		}
		if result := service.postDeliveryReport("", identity, report); commandSucceeded(result) {
			t.Fatal("invalid delivery report accepted")
		}
		if len(runner.calls) != 0 {
			t.Fatalf("invalid facts reached tracker: %v", runner.calls)
		}
	}
}

func TestReviewArtifactPublicationPreservesIntentionalBody(t *testing.T) {
	for _, unmanaged := range []bool{false, true} {
		t.Run(map[bool]string{false: "managed", true: "unmanaged"}[unmanaged], func(t *testing.T) {
			body := "  Intentional reviewer artifact\n" + finalSentinel + "\n/" + workspaceSentinel + "\n"
			runner := &fakeCommandRunner{fn: func(spec CommandSpec) CommandResult {
				return CommandResult{Stdout: "HTTP/2.0 201 Created\n\n{\"id\":1}", Stderr: stderrSentinel}
			}}
			service := newTestService(t, runner, t.TempDir())
			identity := RepositoryIdentity{Owner: "acme", Name: "iro"}
			artifact := reviewArtifact{body: body}
			var err error
			if unmanaged {
				err = service.postUnmanagedReview("", identity, 42, artifact)
			} else {
				err = service.postReview("", identity, 42, artifact)
			}
			bodies := commentPayloads(t, runner.calls)
			if err != nil || len(bodies) != 1 || bodies[0] != body {
				t.Fatalf("intentional artifact was changed: %v %q", err, bodies)
			}
		})
	}
}
