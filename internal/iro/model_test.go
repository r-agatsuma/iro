package iro

import (
	"io"
	"reflect"
	"strings"
	"testing"
)

func TestParseModelOverride(t *testing.T) {
	for _, tc := range []struct {
		name  string
		args  []string
		model string
		want  bool
	}{
		{name: "omitted", args: []string{"run", "123"}, want: true},
		{name: "long form", args: []string{"run", "123", "--model", "gpt-test"}, model: "gpt-test", want: true},
		{name: "short form", args: []string{"run", "123", "-m", "gpt-test"}, model: "gpt-test", want: true},
		{name: "missing value", args: []string{"run", "123", "--model"}},
		{name: "empty value", args: []string{"run", "123", "--model", ""}},
		{name: "whitespace value", args: []string{"run", "123", "--model", " \t"}},
		{name: "value looks like option", args: []string{"run", "123", "--model", "-m"}},
		{name: "unsupported option", args: []string{"run", "123", "--other", "value"}},
		{name: "duplicate options", args: []string{"run", "123", "--model", "one", "-m", "two"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model, err := parseModelOverride(tc.args, "run", "issue-number")
			if (err == nil) != tc.want {
				t.Fatalf("parseModelOverride() error = %v, want success=%t", err, tc.want)
			}
			if err == nil && model != tc.model {
				t.Fatalf("parseModelOverride() = %q, want %q", model, tc.model)
			}
		})
	}
}

func TestParseWorkerOptions(t *testing.T) {
	for _, tc := range []struct {
		name            string
		args            []string
		model           string
		reasoningEffort string
		want            bool
	}{
		{name: "omitted", args: []string{"run", "123"}, want: true},
		{name: "reasoning effort", args: []string{"run", "123", "--reasoning-effort", "xhigh"}, reasoningEffort: "xhigh", want: true},
		{name: "model then reasoning effort", args: []string{"run", "123", "--model", "gpt-test", "--reasoning-effort", "xhigh"}, model: "gpt-test", reasoningEffort: "xhigh", want: true},
		{name: "reasoning effort then model", args: []string{"run", "123", "--reasoning-effort", "low", "-m", "gpt-test"}, model: "gpt-test", reasoningEffort: "low", want: true},
		{name: "missing reasoning effort", args: []string{"run", "123", "--reasoning-effort"}},
		{name: "empty reasoning effort", args: []string{"run", "123", "--reasoning-effort", ""}},
		{name: "whitespace reasoning effort", args: []string{"run", "123", "--reasoning-effort", " \t"}},
		{name: "reasoning effort looks like option", args: []string{"run", "123", "--reasoning-effort", "--model", "gpt-test"}},
		{name: "duplicate reasoning effort", args: []string{"run", "123", "--reasoning-effort", "low", "--reasoning-effort", "high"}},
		{name: "duplicate model", args: []string{"run", "123", "--model", "one", "-m", "two"}},
		{name: "unsupported option", args: []string{"run", "123", "--other", "value"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			options, err := parseWorkerOptions(tc.args, "run", "issue-number")
			if (err == nil) != tc.want {
				t.Fatalf("parseWorkerOptions() error = %v, want success=%t", err, tc.want)
			}
			if err == nil && (options.Model != tc.model || options.ReasoningEffort != tc.reasoningEffort) {
				t.Fatalf("parseWorkerOptions() = %+v, want model=%q effort=%q", options, tc.model, tc.reasoningEffort)
			}
		})
	}
}

func TestCodexModelOverrideArguments(t *testing.T) {
	base := []string{"--cd", "workspace", "exec", "--ephemeral"}
	if got := withCodexModel(base, ""); !reflect.DeepEqual(got, base) {
		t.Fatalf("withCodexModel without override = %v, want %v", got, base)
	}
	want := []string{"--cd", "workspace", "--model", "gpt-test", "exec", "--ephemeral"}
	if got := withCodexModel(base, "gpt-test"); !reflect.DeepEqual(got, want) {
		t.Fatalf("withCodexModel with override = %v, want %v", got, want)
	}
}

func TestCodexReasoningEffortOverrideArguments(t *testing.T) {
	base := []string{"--cd", "workspace", "exec", "--ephemeral"}
	want := []string{"--cd", "workspace", "-c", `model_reasoning_effort="xhigh"`, "exec", "--ephemeral"}
	if got := withCodexOptions(base, workerOptions{ReasoningEffort: "xhigh"}); !reflect.DeepEqual(got, want) {
		t.Fatalf("withCodexOptions with reasoning effort = %v, want %v", got, want)
	}

	want = []string{"--cd", "workspace", "--model", "gpt-test", "-c", `model_reasoning_effort="low"`, "exec", "--ephemeral"}
	if got := withCodexOptions(base, workerOptions{Model: "gpt-test", ReasoningEffort: "low"}); !reflect.DeepEqual(got, want) {
		t.Fatalf("withCodexOptions with model and reasoning effort = %v, want %v", got, want)
	}

	if got := withCodexOptions(base, workerOptions{}); !reflect.DeepEqual(got, base) {
		t.Fatalf("withCodexOptions without overrides = %v, want %v", got, base)
	}
}

func TestExecuteRejectsInvalidModelArgumentsBeforeSideEffects(t *testing.T) {
	for _, command := range []string{"run", "review", "revise"} {
		for _, suffix := range [][]string{
			{"--model"},
			{"-m"},
			{"--model", ""},
			{"--model", " \t"},
			{"--model", "-m"},
			{"--other", "value"},
			{"--model", "one", "-m", "two"},
		} {
			args := append([]string{command, "123"}, suffix...)
			runner := &fakeCommandRunner{}
			service := NewService(runner, NewOSFileSystem())
			var errOut strings.Builder
			if status := Execute(args, io.Discard, &errOut, service); status != 2 {
				t.Fatalf("Execute(%v) = %d, want usage status 2; stderr=%s", args, status, errOut.String())
			}
			if len(runner.calls) != 0 {
				t.Fatalf("Execute(%v) ran commands before rejecting usage: %v", args, runner.calls)
			}
		}
	}
}

func TestExecuteRejectsInvalidReasoningEffortArgumentsBeforeSideEffects(t *testing.T) {
	for _, command := range []string{"run", "review", "revise"} {
		for _, suffix := range [][]string{
			{"--reasoning-effort"},
			{"--reasoning-effort", ""},
			{"--reasoning-effort", " \t"},
			{"--reasoning-effort", "--model", "gpt-test"},
			{"--reasoning-effort", "low", "--reasoning-effort", "high"},
			{"--model", "gpt-test", "--model", "other"},
			{"--model", "gpt-test", "--reasoning-effort"},
			{"--reasoning-effort", "low", "--other", "value"},
		} {
			args := append([]string{command, "123"}, suffix...)
			runner := &fakeCommandRunner{}
			service := NewService(runner, NewOSFileSystem())
			var errOut strings.Builder
			if status := Execute(args, io.Discard, &errOut, service); status != 2 {
				t.Fatalf("Execute(%v) = %d, want usage status 2; stderr=%s", args, status, errOut.String())
			}
			if len(runner.calls) != 0 {
				t.Fatalf("Execute(%v) ran commands before rejecting usage: %v", args, runner.calls)
			}
		}
	}
}

func TestRunPassesRequestedModelToAuthor(t *testing.T) {
	root := t.TempDir()
	writeProjectFiles(t, root)
	runner := &fakeCommandRunner{}
	runner.fn = func(spec CommandSpec) CommandResult {
		return standardFakeResult(spec, root, "", false, false)
	}
	service := newTestService(t, runner, root)
	if status := Execute([]string{"run", "123", "--model", "gpt-run"}, io.Discard, io.Discard, service); status != 0 {
		t.Fatalf("Execute(run) = %d", status)
	}
	assertWorkerModel(t, runner.calls, "gpt-run")
}

func TestReviewPassesRequestedModelToReviewer(t *testing.T) {
	root := t.TempDir()
	writeProjectFiles(t, root)
	runner := &fakeCommandRunner{}
	runner.fn = func(spec CommandSpec) CommandResult {
		return reviewFakeResult(spec, root, "review report")
	}
	service := newTestService(t, runner, root)
	if status := Execute([]string{"review", "42", "-m", "gpt-review"}, io.Discard, io.Discard, service); status != 0 {
		t.Fatalf("Execute(review) = %d", status)
	}
	assertWorkerModel(t, runner.calls, "gpt-review")
	for _, call := range runner.calls {
		if call.Name != "codex" || !containsString(call.Args, "--ephemeral") {
			continue
		}
		joined := strings.Join(call.Args, " ")
		if strings.Contains(joined, "Model: gpt-review") || !strings.Contains(joined, "Model: (unknown; not exposed by runtime)") {
			t.Fatalf("requested model was used as Review provenance: %v", call.Args)
		}
		return
	}
}

func TestRevisePassesRequestedModelToAuthor(t *testing.T) {
	fixture := newReviseFixture(t, false)
	if status := Execute([]string{"revise", "42", "--model", "gpt-revise"}, io.Discard, io.Discard, fixture.service); status != 0 {
		t.Fatalf("Execute(revise) = %d", status)
	}
	assertWorkerModel(t, fixture.runner.calls, "gpt-revise")
}

func TestWorkerPassesIndependentReasoningEffortOverride(t *testing.T) {
	t.Run("run author", func(t *testing.T) {
		root := t.TempDir()
		writeProjectFiles(t, root)
		runner := &fakeCommandRunner{}
		runner.fn = func(spec CommandSpec) CommandResult {
			return standardFakeResult(spec, root, "", false, false)
		}
		service := newTestService(t, runner, root)
		if status := Execute([]string{"run", "123", "--reasoning-effort", "xhigh", "--model", "gpt-run"}, io.Discard, io.Discard, service); status != 0 {
			t.Fatalf("Execute(run) = %d", status)
		}
		assertWorkerOptions(t, runner.calls, "gpt-run", "xhigh")
	})

	t.Run("reviewer", func(t *testing.T) {
		root := t.TempDir()
		writeProjectFiles(t, root)
		runner := &fakeCommandRunner{}
		runner.fn = func(spec CommandSpec) CommandResult {
			return reviewFakeResult(spec, root, "review report")
		}
		service := newTestService(t, runner, root)
		if status := Execute([]string{"review", "42", "--model", "gpt-review", "--reasoning-effort", "low"}, io.Discard, io.Discard, service); status != 0 {
			t.Fatalf("Execute(review) = %d", status)
		}
		assertWorkerOptions(t, runner.calls, "gpt-review", "low")
	})

	t.Run("revise author", func(t *testing.T) {
		fixture := newReviseFixture(t, false)
		if status := Execute([]string{"revise", "42", "--reasoning-effort", "medium"}, io.Discard, io.Discard, fixture.service); status != 0 {
			t.Fatalf("Execute(revise) = %d", status)
		}
		assertWorkerOptions(t, fixture.runner.calls, "", "medium")
	})
}

func TestWorkerInvocationsOmitModelWhenNotRequested(t *testing.T) {
	t.Run("run", func(t *testing.T) {
		root := t.TempDir()
		writeProjectFiles(t, root)
		runner := &fakeCommandRunner{}
		runner.fn = func(spec CommandSpec) CommandResult {
			return standardFakeResult(spec, root, "", false, false)
		}
		service := newTestService(t, runner, root)
		if status := Execute([]string{"run", "123"}, io.Discard, io.Discard, service); status != 0 {
			t.Fatalf("Execute(run) = %d", status)
		}
		assertWorkerHasNoModel(t, runner.calls)
		assertWorkerHasNoReasoningEffort(t, runner.calls)
	})

	t.Run("review", func(t *testing.T) {
		root := t.TempDir()
		writeProjectFiles(t, root)
		runner := &fakeCommandRunner{}
		runner.fn = func(spec CommandSpec) CommandResult {
			return reviewFakeResult(spec, root, "review report")
		}
		service := newTestService(t, runner, root)
		if status := Execute([]string{"review", "42"}, io.Discard, io.Discard, service); status != 0 {
			t.Fatalf("Execute(review) = %d", status)
		}
		assertWorkerHasNoModel(t, runner.calls)
		assertWorkerHasNoReasoningEffort(t, runner.calls)
	})

	t.Run("revise", func(t *testing.T) {
		fixture := newReviseFixture(t, false)
		if status := Execute([]string{"revise", "42"}, io.Discard, io.Discard, fixture.service); status != 0 {
			t.Fatalf("Execute(revise) = %d", status)
		}
		assertWorkerHasNoModel(t, fixture.runner.calls)
		assertWorkerHasNoReasoningEffort(t, fixture.runner.calls)
	})
}

func assertWorkerModel(t *testing.T, calls []CommandSpec, model string) {
	t.Helper()
	for _, call := range calls {
		if call.Name != "codex" || !containsString(call.Args, "--ephemeral") {
			continue
		}
		if !containsArgs(call.Args, "--model", model) {
			t.Fatalf("worker invocation did not request model %q: %v", model, call.Args)
		}
		return
	}
	t.Fatalf("worker invocation was not found: %v", calls)
}

func assertWorkerHasNoModel(t *testing.T, calls []CommandSpec) {
	t.Helper()
	for _, call := range calls {
		if call.Name == "codex" && containsString(call.Args, "--ephemeral") {
			if containsString(call.Args, "--model") {
				t.Fatalf("worker invocation unexpectedly requested a model: %v", call.Args)
			}
			return
		}
	}
	t.Fatalf("worker invocation was not found: %v", calls)
}

func assertWorkerOptions(t *testing.T, calls []CommandSpec, model, reasoningEffort string) {
	t.Helper()
	for _, call := range calls {
		if call.Name != "codex" || !containsString(call.Args, "--ephemeral") {
			continue
		}
		if model != "" && !containsArgs(call.Args, "--model", model) {
			t.Fatalf("worker invocation did not request model %q: %v", model, call.Args)
		}
		if model == "" && containsString(call.Args, "--model") {
			t.Fatalf("worker invocation unexpectedly requested a model: %v", call.Args)
		}
		if !containsArgs(call.Args, "-c", `model_reasoning_effort="`+reasoningEffort+`"`) {
			t.Fatalf("worker invocation did not request reasoning effort %q: %v", reasoningEffort, call.Args)
		}
		return
	}
	t.Fatalf("worker invocation was not found: %v", calls)
}

func assertWorkerHasNoReasoningEffort(t *testing.T, calls []CommandSpec) {
	t.Helper()
	for _, call := range calls {
		if call.Name == "codex" && containsString(call.Args, "--ephemeral") {
			if hasCodexReasoningEffort(call.Args) {
				t.Fatalf("worker invocation unexpectedly requested reasoning effort: %v", call.Args)
			}
			return
		}
	}
	t.Fatalf("worker invocation was not found: %v", calls)
}

func hasCodexReasoningEffort(args []string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-c" && strings.HasPrefix(args[i+1], "model_reasoning_effort=") {
			return true
		}
	}
	return false
}
