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
	})

	t.Run("revise", func(t *testing.T) {
		fixture := newReviseFixture(t, false)
		if status := Execute([]string{"revise", "42"}, io.Discard, io.Discard, fixture.service); status != 0 {
			t.Fatalf("Execute(revise) = %d", status)
		}
		assertWorkerHasNoModel(t, fixture.runner.calls)
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
