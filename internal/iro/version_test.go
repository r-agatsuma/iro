package iro

import (
	"errors"
	"reflect"
	"runtime/debug"
	"strings"
	"testing"
)

func TestBuildInfo(t *testing.T) {
	full := &debug.BuildInfo{
		GoVersion: "go1.22.5",
		Main:      debug.Module{Version: "(devel)"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "abcdef"},
			{Key: "vcs.time", Value: "2026-09-06T08:46:19Z"},
			{Key: "vcs.modified", Value: "false"},
		},
	}
	for _, tt := range []struct {
		name string
		info *debug.BuildInfo
		ok   bool
		want string
	}{
		{"full", full, true, "iro\nversion: (devel)\nrevision: abcdef\nvcs-time: 2026-09-06T08:46:19Z\nmodified: false\ngo: go1.22.5\n"},
		{"missing", &debug.BuildInfo{}, true, "iro\nversion: unknown\nrevision: unknown\nvcs-time: unknown\nmodified: unknown\ngo: unknown\n"},
		{"unavailable", full, false, "iro\nversion: unknown\nrevision: unknown\nvcs-time: unknown\nmodified: unknown\ngo: unknown\n"},
		{"nil", nil, true, "iro\nversion: unknown\nrevision: unknown\nvcs-time: unknown\nmodified: unknown\ngo: unknown\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var out strings.Builder
			writeBuildInfo(&out, tt.info, tt.ok)
			if out.String() != tt.want {
				t.Fatalf("output = %q, want %q", out.String(), tt.want)
			}
		})
	}
}

func TestVersionWithoutService(t *testing.T) {
	var out, errOut strings.Builder
	if code := Execute([]string{"version"}, &out, &errOut, nil); code != 0 {
		t.Fatalf("exit = %d: %s", code, errOut.String())
	}
	if !strings.HasPrefix(out.String(), "iro\nversion: ") {
		t.Fatal(out.String())
	}
	out.Reset()
	if code := Execute([]string{"version", "extra"}, &out, &errOut, nil); code != 2 || out.Len() != 0 {
		t.Fatalf("extra argument: exit = %d, output = %q", code, out.String())
	}
}

func TestExecutablePathUnavailable(t *testing.T) {
	var out strings.Builder
	writeExecutablePath(&out, func() (string, error) { return "", errors.New("unavailable") })
	if out.String() != "iro executable path: unknown\n" {
		t.Fatal(out.String())
	}
}

func TestDoctorDiagnosticsPreserveHealthAndReadOnlyCommands(t *testing.T) {
	for _, versionsAvailable := range []bool{true, false} {
		t.Run(map[bool]string{true: "versions", false: "unknown_versions"}[versionsAvailable], func(t *testing.T) {
			root := t.TempDir()
			writeProjectFiles(t, root)
			runner := &fakeCommandRunner{}
			runner.fn = func(spec CommandSpec) CommandResult {
				switch {
				case reflect.DeepEqual(spec.Args, []string{"--version"}):
					if versionsAvailable {
						return CommandResult{Stdout: spec.Name + " version test\nextra detail\n"}
					}
					return CommandResult{ExitCode: 1, Err: errors.New("unsupported")}
				case spec.Name == "git" && reflect.DeepEqual(spec.Args, []string{"rev-parse", "--show-toplevel"}):
					return CommandResult{Stdout: root}
				case spec.Name == "git" && reflect.DeepEqual(spec.Args, []string{"config", "--get-all", "remote.origin.url"}):
					return CommandResult{Stdout: "git@github.com:acme/iro.git"}
				case spec.Name == "gh" && reflect.DeepEqual(spec.Args, []string{"auth", "status"}):
					return CommandResult{}
				case spec.Name == "codex" && reflect.DeepEqual(spec.Args, []string{"login", "status"}):
					return CommandResult{}
				default:
					t.Fatalf("unexpected command: %+v", spec)
					return CommandResult{}
				}
			}
			var out strings.Builder
			if err := newTestService(t, runner, root).Doctor(&out); err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"iro executable path:", "revision:", "project repository root: " + root,
				"project iro.toml: " + root, "project WORKFLOW.md: " + root,
				"repository configured remote: origin", "repository GitHub host: github.com",
				"repository owner/repository: acme/iro", "OK: GitHub authentication", "OK: Codex authentication"} {
				if !strings.Contains(out.String(), want) {
					t.Errorf("missing %q in %s", want, out.String())
				}
			}
			for _, name := range []string{"git", "gh", "codex"} {
				version := "unknown"
				if versionsAvailable {
					version = name + " version test"
				}
				for _, want := range []string{name + " executable path: /fake/bin/" + name, name + " version: " + version} {
					if !strings.Contains(out.String(), want) {
						t.Errorf("missing %q", want)
					}
				}
			}
		})
	}
}

func TestDoctorAggregatesProjectAndAuthFailures(t *testing.T) {
	runner := &fakeCommandRunner{fn: func(spec CommandSpec) CommandResult {
		return CommandResult{ExitCode: 1, Err: errors.New("failure")}
	}}
	var out strings.Builder
	if err := NewService(runner, NewOSFileSystem()).Doctor(&out); err == nil {
		t.Fatal("expected failure")
	}
	for _, want := range []string{"FAIL: Git repository", "FAIL: GitHub authentication", "FAIL: Codex authentication", "repository GitHub host: unknown"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in %s", want, out.String())
		}
	}
}
