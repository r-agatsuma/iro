package iro

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newUnmanagedLandFixture(t *testing.T) *landFixture {
	t.Helper()
	f := newLandFixture(t)
	f.pages = nil
	// Any filesystem access fails; there is no config, workflow, or ownership IO.
	f.service.FileSystem = landProjectFiles{t: t, root: "forbidden", rejectWorkflow: true}
	f.runner.fn = func(spec CommandSpec) CommandResult {
		if spec.Name == "git" && strings.Join(spec.Args, " ") == "remote get-url --all origin" {
			return CommandResult{Stdout: "git@github.com:acme/iro.git\n"}
		}
		if containsString(spec.Args, "query="+unmanagedLandPreflightQuery) {
			if !containsArgs(spec.Args, "--hostname", "github.com") || !containsArgs(spec.Args, "-f", "owner=acme") || !containsArgs(spec.Args, "-f", "name=iro") || !containsArgs(spec.Args, "-F", "number=42") {
				t.Fatalf("wrong target: %+v", spec)
			}
			return CommandResult{Stdout: f.target}
		}
		return f.respond(spec)
	}
	return f
}

func TestUnmanagedLandArbitraryDeliveryAndAmbientSelectors(t *testing.T) {
	t.Setenv("GH_REPO", "elsewhere/other")
	t.Setenv("GH_HOST", "elsewhere.invalid")
	for _, state := range []string{"CLEAN", "UNSTABLE", "HAS_HOOKS", "BEHIND"} {
		t.Run(state, func(t *testing.T) {
			f := newUnmanagedLandFixture(t)
			for _, name := range []string{"iro.toml", "WORKFLOW.md"} {
				if err := os.Remove(filepath.Join(f.root, name)); err != nil {
					t.Fatal(err)
				}
			}
			f.target = strings.NewReplacer(`"defaultBranchRef":{"name":"main"}`, `"defaultBranchRef":null`, `"baseRefName":"main"`, `"baseRefName":"release"`, `iro/issue-123`, `human-feature`, `"totalCount":1`, `"totalCount":0`, `"CLEAN"`, `"`+state+`"`).Replace(f.target)
			var out, errOut bytes.Buffer
			if code := Execute([]string{"land", "42", "--unmanaged"}, &out, &errOut, f.service); code != 0 || f.mergeCalls != 1 || !strings.Contains(out.String(), "Landed PR #42 with merge commit "+landMergeForTest) || strings.Contains(out.String(), "Issue") {
				t.Fatalf("code=%d merges=%d out=%s err=%s", code, f.mergeCalls, &out, &errOut)
			}
		})
	}
}

func TestUnmanagedLandPreflightFailures(t *testing.T) {
	for _, change := range [][2]string{
		{`"OPEN"`, `"CLOSED"`}, {`"number":42`, `"number":43`}, {`"isDraft":false`, `"isDraft":true`}, {`"isDraft":false`, `"isDraft":null`},
		{`acme/iro`, `fork/iro`}, {landHeadForTest, "invalid"}, {`"isArchived":false`, `"isArchived":true`}, {`"isArchived":false`, `"isArchived":null`},
		{`"mergeCommitAllowed":true`, `"mergeCommitAllowed":false`}, {`"WRITE"`, `"READ"`}, {`"MERGEABLE"`, `"UNKNOWN"`},
		{`"CLEAN"`, `"BLOCKED"`}, {`"CLEAN"`, `"UNKNOWN"`}, {`"isMergeQueueEnabled":false`, `"isMergeQueueEnabled":true`}, {`"isMergeQueueEnabled":false`, `"isMergeQueueEnabled":null`},
	} {
		t.Run(change[1], func(t *testing.T) {
			f := newUnmanagedLandFixture(t)
			f.target = strings.ReplaceAll(f.target, change[0], change[1])
			if err := f.service.landUnmanaged(42, io.Discard); err == nil || f.mergeCalls != 0 {
				t.Fatalf("err=%v merges=%d", err, f.mergeCalls)
			}
		})
	}
}

func TestUnmanagedLandMergeFailureNeverRetries(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result CommandResult
		drift  bool
	}{
		{"head drift", CommandResult{}, true},
		{"required policy", CommandResult{ExitCode: 1, Stderr: "required checks failed"}, false},
		{"false", CommandResult{Stdout: `{"merged":false}`}, false},
		{"malformed", CommandResult{Stdout: `not JSON`}, false},
		{"missing merged", CommandResult{Stdout: `{"sha":"` + landMergeForTest + `"}`}, false},
		{"missing sha", CommandResult{Stdout: `{"merged":true}`}, false},
		{"invalid sha", CommandResult{Stdout: `{"merged":true,"sha":"bad"}`}, false},
		{"connection loss", CommandResult{Err: errors.New("connection lost"), ExitCode: 1}, false},
		{"unconfirmed response", CommandResult{Stdout: `{"merged":true,"sha":"` + landMergeForTest + `"}`, Err: errors.New("connection lost")}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newUnmanagedLandFixture(t)
			f.mergeResult = tc.result
			if tc.drift {
				f.liveHead = landMergeForTest
			}
			var out, errOut bytes.Buffer
			if code := Execute([]string{"land", "42", "--unmanaged"}, &out, &errOut, f.service); code != 1 || f.mergeCalls != 1 || out.Len() != 0 || !strings.Contains(errOut.String(), "current HEAD, and repository rules") || !strings.Contains(errOut.String(), "iro land 42 --unmanaged") {
				t.Fatalf("code=%d merges=%d out=%s err=%s", code, f.mergeCalls, &out, &errOut)
			}
		})
	}
}

func TestLandRejectsOptionsBeforeCommands(t *testing.T) {
	for _, mode := range [][]string{{"land", "42"}, {"land", "42", "--unmanaged"}} {
		for _, option := range [][]string{{"--issue", "1"}, {"--model", "x"}, {"-m", "x"}, {"--reasoning-effort", "high"}, {"--no-sandbox"}, {"--unknown"}, {"extra"}, {"--unmanaged=true"}, {"--unmanaged", "--unmanaged"}} {
			f := newLandFixture(t)
			args := append(append([]string{}, mode...), option...)
			if code := Execute(args, io.Discard, io.Discard, f.service); code != 2 || len(f.runner.calls) != 0 {
				t.Fatalf("args=%v code=%d calls=%v", args, code, f.runner.calls)
			}
		}
	}
	for _, args := range [][]string{{"land", "--unmanaged", "42"}, {"land", "42", "--unmanaged", "false"}, {"land", "42", "--unmanaged", "--unmanaged"}} {
		if code := Execute(args, io.Discard, io.Discard, nil); code != 2 {
			t.Fatalf("args=%v code=%d", args, code)
		}
	}
}

func TestUnmanagedLandSharedHeadDoesNotGrantManagedEligibility(t *testing.T) {
	f := newUnmanagedLandFixture(t)
	duplicate := strings.Replace(landActivePRForTest, `"number":42`, `"number":43`, 1)
	// The fixture rejects all operations except the selected PR's preflight/merge.
	// K remains present in the later managed enumeration, with the same head.
	if err := f.service.landUnmanaged(42, io.Discard); err != nil {
		t.Fatal(err)
	}
	f.pages = []string{landDeliveryPage(landActivePRForTest+","+duplicate, false, "")}
	f.service.FileSystem = landProjectFiles{t: t, root: f.root, rejectWorkflow: true}
	if err := f.service.Land(42, io.Discard); err == nil || f.mergeCalls != 1 {
		t.Fatalf("managed accepted shared head after unmanaged success: err=%v merges=%d", err, f.mergeCalls)
	}
}

func TestLandModeSpecificIdentity(t *testing.T) {
	f := newUnmanagedLandFixture(t)
	config := strings.Replace(configTemplate, `remote = "origin"`, `remote = "tracker"`, 1)
	if err := os.WriteFile(filepath.Join(f.root, "iro.toml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	original := f.runner.fn
	managedReads := 0
	f.runner.fn = func(spec CommandSpec) CommandResult {
		if spec.Name == "git" && strings.Join(spec.Args, " ") == "config --get-all remote.tracker.url" {
			return CommandResult{Stdout: "git@github.com:managed/project.git\n"}
		}
		if containsString(spec.Args, "query="+landPreflightQuery) {
			if !containsArgs(spec.Args, "-f", "owner=managed") || !containsArgs(spec.Args, "-f", "name=project") {
				t.Fatalf("managed target drift: %+v", spec)
			}
			managedReads++
			// The configured repository's selected PR is noncanonical.
			return CommandResult{Stdout: strings.NewReplacer("acme/iro", "managed/project", `"totalCount":1`, `"totalCount":0`).Replace(landResponseForTest)}
		}
		return original(spec)
	}
	t.Setenv("GH_REPO", "managed/project")
	t.Setenv("GH_HOST", "github.com")
	if err := f.service.landUnmanaged(42, io.Discard); err != nil {
		t.Fatal(err)
	}
	f.service.FileSystem = landProjectFiles{t: t, root: f.root, rejectWorkflow: true}
	if err := f.service.Land(42, io.Discard); err == nil || !strings.Contains(err.Error(), "exactly one origin") || managedReads != 1 {
		t.Fatalf("managed relation: %v reads=%d", err, managedReads)
	}
	t.Setenv("GH_REPO", "acme/iro")
	if err := f.service.Land(42, io.Discard); err == nil || managedReads != 1 {
		t.Fatalf("managed context: %v reads=%d", err, managedReads)
	}
	if f.mergeCalls != 1 {
		t.Fatalf("merge calls=%d", f.mergeCalls)
	}
}

func TestUnmanagedLandOriginFetchOnly(t *testing.T) {
	for _, urls := range []string{"", "git@github.com:acme/iro.git\ngit@github.com:acme/iro.git\n", "https://invalid.example/acme/iro", "git@github.com:acme/iro.git\n"} {
		t.Run(urls, func(t *testing.T) {
			f := newUnmanagedLandFixture(t)
			original := f.runner.fn
			f.runner.fn = func(spec CommandSpec) CommandResult {
				if spec.Name == "git" {
					switch strings.Join(spec.Args, " ") {
					case "config --get-all remote.origin.url":
						return CommandResult{Stdout: urls}
					case "remote get-url --push --all origin":
						t.Fatal("Land must not inspect push URLs")
					case "config --get-all remote.origin.pushurl":
						return CommandResult{Stdout: "invalid\nhttps://elsewhere.invalid/repo\n"}
					case "config --get-all remote.upstream.url":
						t.Fatal("Land must not inspect upstream")
					}
				}
				return original(spec)
			}
			err := f.service.landUnmanaged(42, io.Discard)
			valid := urls == "git@github.com:acme/iro.git\n"
			if (err == nil) != valid || f.mergeCalls != map[bool]int{false: 0, true: 1}[valid] {
				t.Fatalf("err=%v merges=%d", err, f.mergeCalls)
			}
		})
	}
}

func TestUnmanagedLandNeedsNoDeliveryMetadata(t *testing.T) {
	f := newUnmanagedLandFixture(t)
	f.target = `{"data":{"repository":{"isArchived":false,"mergeCommitAllowed":true,"viewerPermission":"WRITE","pullRequest":{"number":42,"state":"OPEN","isDraft":false,"headRefOid":"` + landHeadForTest + `","headRepository":{"nameWithOwner":"acme/iro"},"mergeable":"MERGEABLE","mergeStateStatus":"CLEAN","isMergeQueueEnabled":false}}}}`
	if err := f.service.landUnmanaged(42, io.Discard); err != nil || f.mergeCalls != 1 {
		t.Fatalf("err=%v merges=%d", err, f.mergeCalls)
	}
}

func TestLandUsageIncludesUnmanaged(t *testing.T) {
	for _, args := range [][]string{nil, {"land"}} {
		var diagnostic bytes.Buffer
		if code := Execute(args, io.Discard, &diagnostic, nil); code != 2 || !strings.Contains(diagnostic.String(), "land <pr-number> [--unmanaged]") {
			t.Fatalf("code=%d diagnostic=%s", code, &diagnostic)
		}
	}
}
