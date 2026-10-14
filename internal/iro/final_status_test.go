package iro

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func readTerminalInvocation(t *testing.T, output string) invocationResult {
	t.Helper()
	start := strings.LastIndex(output, "\nINSPECTION: ")
	if start < 0 {
		t.Fatalf("missing final status: %s", output)
	}
	block := output[start+1:]
	fields := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(block), "\n") {
		key, value, ok := strings.Cut(line, ": ")
		if !ok {
			t.Fatalf("unexpected output after final status: %s", block)
		}
		fields[key] = value
	}
	for _, key := range []string{"INSPECTION", "WORKER", "DELIVERY", "REMOTE REPORT", "LOCAL LOG"} {
		if fields[key] == "" {
			t.Fatalf("missing %s: %s", key, block)
		}
	}
	path := fields["LOCAL LOG"]
	if !filepath.IsAbs(path) {
		t.Fatalf("log identity is not exact absolute path: %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("terminal log does not exist", err)
	}
	var result invocationResult
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	if result.LocalLog != path || fields["INSPECTION"] != result.Inspection || fields["WORKER"] != result.Worker || fields["DELIVERY"] != result.Delivery || !strings.HasPrefix(fields["REMOTE REPORT"], result.RemoteReport) {
		t.Fatalf("terminal and local result differ: %s\n%s", block, data)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("final metadata is not private", info, err)
	}
	return result
}

func TestIncludedOperationsExposeAuthoritativeFinalStatus(t *testing.T) {
	for _, mode := range inspectedModes {
		for _, scenario := range []string{"BLOCKER", "BLOCKER report failure", "Inspector report failure", "worker failure", "worker report failure", "delivery failure", "delivery report failure", "success", "Author preflight failure"} {
			t.Run(mode+"/"+scenario, func(t *testing.T) {
				f := newInspectionOperationFixture(t, mode)
				reportFailure := strings.Contains(scenario, "report failure")
				f.runner.inspection = func(spec CommandSpec) CommandResult {
					final := `{"verdict":"PASS","reason_class":"none","rationale":"` + finalSentinel + `"}`
					result := CommandResult{Stdout: stdoutSentinel, Stderr: stderrSentinel}
					if strings.HasPrefix(scenario, "BLOCKER") {
						final = strings.ReplaceAll(strings.ReplaceAll(final, "PASS", "BLOCKER"), "none", "authority_conflict")
					}
					if scenario == "Inspector report failure" {
						result.ExitCode, result.Err = 7, errors.New(subprocessError)
					}
					if scenario == "Author preflight failure" {
						f.runner.lookups = map[string]error{f.agentType: os.ErrNotExist}
					}
					return inspectionResultForTest(spec, final, result)
				}
				previous := f.runner.fn
				f.runner.fn = func(spec CommandSpec) CommandResult {
					if isAuthorCommand(spec) && strings.HasPrefix(scenario, "worker") {
						stdout := stdoutSentinel
						if f.agentType == "copilot" {
							stdout = copilotCompletionForTest(stdout)
						}
						return CommandResult{ExitCode: 9, Err: errors.New(subprocessError), Stdout: stdout, Stderr: stderrSentinel}
					}
					if scenario == "delivery failure" && spec.Name == "git" && spec.Args[0] == "push" {
						return CommandResult{ExitCode: -1, Err: errors.New(subprocessError)}
					}
					if reportFailure && spec.Name == "gh" && (containsArgs(spec.Args, "issue", "comment") || containsArgs(spec.Args, "pr", "comment")) {
						return CommandResult{ExitCode: -1, Err: errors.New(subprocessError), Stdout: subprocessOut, Stderr: subprocessErr}
					}
					return previous(spec)
				}
				var out, errOut strings.Builder
				err := f.execute(workerOptions{}, &out, &errOut)
				delivered := scenario == "success" || scenario == "delivery report failure"
				if (err == nil) != delivered {
					t.Fatal("publication failure changed operation result", err, out.String(), errOut.String())
				}
				result := readTerminalInvocation(t, out.String())
				if result.Operation != f.operation || result.Unmanaged != f.unmanaged || result.Issue != 123 || result.Source != f.head || result.Repository != "acme/iro" {
					t.Fatalf("incorrect invocation binding: %+v", result)
				}
				inspection, worker, delivery, remote := "PASS", "success", "not attempted", "published"
				if strings.HasPrefix(scenario, "BLOCKER") {
					inspection, worker = "BLOCKER", "not started"
					if result.InspectionReason != "authority_conflict" {
						t.Fatal("blocker reason lost", result)
					}
				} else if scenario == "Inspector report failure" {
					inspection, worker = "FAILED", "not started"
				} else if strings.HasPrefix(scenario, "worker") {
					worker = "failed (exit 9)"
					if result.WorkerDiagnostic == nil || result.WorkerDiagnostic.ExitStatus != 9 || result.WorkerDiagnostic.Error != subprocessError || result.WorkerDiagnostic.Stdout != stdoutSentinel || result.WorkerDiagnostic.Stderr != stderrSentinel {
						t.Fatal("raw worker diagnostics lost", result)
					}
				} else if scenario == "Author preflight failure" {
					worker, remote = "not started", "not attempted"
				}
				if reportFailure {
					remote = "failed (remote outcome unknown)"
				}
				if f.operation == "revise" {
					remote = "not applicable (local only)"
				}
				if delivered {
					pr := 42
					if !f.unmanaged && f.operation == "run" {
						pr = 456
					}
					delivery = "PR #" + strconv.Itoa(pr) + " created"
					if f.operation == "revise" {
						delivery = "PR #42 updated"
					}
					if result.PRNumber != pr {
						t.Fatal("delivery receipt lost", result)
					}
					if f.operation == "run" && !f.unmanaged && !strings.HasSuffix(out.String(), "LAND: iro land 456\n") {
						t.Fatal("Land guidance lost", out.String())
					}
				}
				if scenario == "delivery failure" {
					if !strings.HasPrefix(result.Delivery, "failed") || !strings.Contains(result.Delivery, "unknown") || result.Push != "unknown" {
						t.Fatal("delivery failure was obscured", result)
					}
					delivery = result.Delivery
				}
				if result.Inspection != inspection || result.Worker != worker || result.Delivery != delivery || result.RemoteReport != remote {
					t.Fatalf("unexpected outcomes: %+v", result)
				}
				if f.operation == "revise" && result.PRNumber != 42 {
					t.Fatal("selected PR identity lost", result)
				}
				if !strings.Contains(result.InspectionDiagnostic, finalSentinel) || !strings.Contains(result.InspectionDiagnostic, stderrSentinel) {
					t.Fatal("raw inspection diagnostics lost", result)
				}
				bodies := commentPayloads(t, f.runner.calls)
				count := 1
				if f.operation == "revise" || scenario == "Author preflight failure" {
					count = 0
				}
				if len(bodies) != count {
					t.Fatal("comment behavior changed or retried", bodies)
				}
				if reportFailure && f.operation == "run" && !strings.Contains(out.String(), "existing tracker comments may be stale; local result is authoritative") {
					t.Fatal("stale remote state not explicit", out.String())
				}
				if reportFailure && f.operation == "run" && (result.RemoteReportDiagnostic == nil || result.RemoteReportDiagnostic.ExitStatus != -1 || result.RemoteReportDiagnostic.Error != subprocessError || result.RemoteReportDiagnostic.Stdout != subprocessOut || result.RemoteReportDiagnostic.Stderr != subprocessErr) {
					t.Fatal("raw publication diagnostics lost", result)
				}
				for _, body := range bodies {
					if strings.Contains(body, finalSentinel) || strings.Contains(body, result.LocalLog) {
						t.Fatal("local diagnostics published", body)
					}
				}
			})
		}
	}
}

func TestFinalLogIdentifiesEachInvocation(t *testing.T) {
	f := newInspectionOperationFixture(t, "managed Run")
	f.runner.inspection = func(spec CommandSpec) CommandResult {
		return inspectionResultForTest(spec, `{"verdict":"BLOCKER","reason_class":"authority_conflict","rationale":"local"}`, CommandResult{})
	}
	var paths []string
	for i := 0; i < 2; i++ {
		var out, errOut strings.Builder
		if err := f.execute(workerOptions{}, &out, &errOut); err == nil {
			t.Fatal("blocker succeeded")
		}
		paths = append(paths, readTerminalInvocation(t, out.String()).LocalLog)
	}
	if paths[0] == paths[1] {
		t.Fatal("invocations reused a log identity", paths)
	}
	for _, path := range paths {
		if _, err := os.Stat(path); err != nil {
			t.Fatal("earlier invocation log lost", err)
		}
	}
}

type finalStatusFailureFS struct {
	FileSystem
	directory bool
}

func (f finalStatusFailureFS) WriteFile(path string, data []byte, mode os.FileMode) error {
	if filepath.Base(path) == "result.json" {
		return errors.New(operationSentinel)
	}
	return f.FileSystem.WriteFile(path, data, mode)
}

func (f finalStatusFailureFS) MkdirTemp(parent, pattern string) (string, error) {
	if f.directory && filepath.Base(filepath.Dir(parent)) == "inspections" {
		return "", errors.New(operationSentinel)
	}
	return f.FileSystem.MkdirTemp(parent, pattern)
}

func executeWithLocalDiagnostics(t *testing.T, f inspectionOperationFixture, out, errOut *strings.Builder) error {
	t.Helper()
	if f.operation != "revise" || f.unmanaged {
		return f.execute(workerOptions{}, out, errOut)
	}
	// Managed Revise retains its existing process stderr stream.
	file, err := os.CreateTemp(t.TempDir(), "stderr-*")
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stderr
	os.Stderr = file
	defer func() {
		os.Stderr = previous
		_ = file.Close()
	}()
	operationErr := f.execute(workerOptions{}, out, errOut)
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(errOut, file); err != nil {
		t.Fatal(err)
	}
	return operationErr
}

func TestFinalStatusStorageFailurePreservesResultsAndDiagnostics(t *testing.T) {
	for _, mode := range inspectedModes {
		for _, scenario := range []string{"success", "BLOCKER", "directory failure"} {
			t.Run(mode+"/"+scenario, func(t *testing.T) {
				f := newInspectionOperationFixture(t, mode)
				f.service.FileSystem = finalStatusFailureFS{f.service.FileSystem, scenario == "directory failure"}
				f.runner.inspection = func(spec CommandSpec) CommandResult {
					final := `{"verdict":"PASS","reason_class":"none","rationale":"` + finalSentinel + `"}`
					if scenario == "BLOCKER" {
						final = strings.ReplaceAll(strings.ReplaceAll(final, "PASS", "BLOCKER"), "none", "authority_conflict")
					}
					return inspectionResultForTest(spec, final, CommandResult{Stdout: stdoutSentinel, Stderr: stderrSentinel})
				}
				var out, errOut strings.Builder
				err := executeWithLocalDiagnostics(t, f, &out, &errOut)
				if (err == nil) != (scenario == "success") {
					t.Fatal("storage failure changed authoritative operation result", err)
				}
				if !strings.Contains(out.String(), "LOCAL LOG: ") {
					t.Fatal("no log identity", out.String())
				}
				if scenario == "directory failure" {
					if !strings.Contains(out.String(), "INSPECTION: FAILED") || !strings.HasSuffix(out.String(), "LOCAL LOG: unavailable\n") {
						t.Fatal("unavailable log presented as saved", out.String())
					}
					assertNoAuthorOrDelivery(t, f.runner.calls)
				} else {
					if !strings.Contains(out.String(), " (not saved)\n") || !strings.Contains(errOut.String(), "could not preserve final status") || !strings.Contains(errOut.String(), finalSentinel) || !strings.Contains(errOut.String(), stderrSentinel) {
						t.Fatal("local storage failure or diagnostics lost", out.String(), errOut.String())
					}
				}
				if count := len(commentPayloads(t, f.runner.calls)); count != map[string]int{"run": 1, "revise": 0}[f.operation] {
					t.Fatal("storage failure caused comment retry", count)
				}
			})
		}
	}
}

func TestFinalStatusInspectorPreflightFailureHasExactLocalLog(t *testing.T) {
	for _, mode := range inspectedModes {
		t.Run(mode, func(t *testing.T) {
			f := newInspectionOperationFixture(t, mode)
			f.runner.lookups = map[string]error{f.agentType: os.ErrNotExist}
			var out, errOut strings.Builder
			if err := f.execute(workerOptions{}, &out, &errOut); err == nil {
				t.Fatal("missing Inspector accepted")
			}
			result := readTerminalInvocation(t, out.String())
			if result.Inspection != "FAILED" || result.Worker != "not started" || result.Delivery != "not attempted" || result.Source != f.head || result.OperationError == "" || result.InspectionDiagnostic == "" {
				t.Fatal("startup failure not recorded", result)
			}
			assertNoAuthorOrDelivery(t, f.runner.calls)
		})
	}
}

func TestFinalLogIdentityIsAbsoluteWithRelativeStateRoot(t *testing.T) {
	f := newInspectionOperationFixture(t, "managed Run")
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	f.service.Dirs.StateRoot, err = filepath.Rel(cwd, f.service.Dirs.StateRoot)
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut strings.Builder
	if err := f.execute(workerOptions{}, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	readTerminalInvocation(t, out.String())
}
