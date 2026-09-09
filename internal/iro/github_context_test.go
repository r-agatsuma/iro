package iro

import (
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
)

var invalidGitHubContexts = []struct{ name, host, repo string }{
	{"host mismatch", "github.example.com", ""},
	{"host whitespace", " github.com", ""},
	{"host URL", "https://github.com", ""},
	{"host port", "github.com:443", ""},
	{"host mismatch with matching repository", "github.example.com", "github.com/acme/iro"},
	{"owner mismatch", "", "other/iro"},
	{"repository mismatch", "github.com", "acme/other"},
	{"selector host mismatch", "", "github.example.com/acme/iro"},
	{"both mismatch", "github.example.com", "github.example.com/other/repo"},
	{"bare repository", "", "iro"},
	{"blank selector", "", " "},
	{"leading whitespace", "", " acme/iro"},
	{"trailing whitespace", "", "acme/iro "},
	{"newline", "", "acme/iro\n"},
	{"empty host", "", "/acme/iro"},
	{"empty owner", "", "github.com//iro"},
	{"empty repository", "", "acme/"},
	{"trailing slash", "", "acme/iro/"},
	{"extra component", "", "github.com/acme/iro/extra"},
	{"selector URL", "", "https://github.com/acme/iro"},
	{"selector SSH", "", "git@github.com:acme/iro.git"},
	{"selector port", "", "github.com:443/acme/iro"},
	{"query", "", "acme/iro?tab=readme"},
	{"fragment", "", "acme/iro#readme"},
	{"escaped component", "", "acme/%69ro"},
	{"backslash", "", "acme\\iro"},
	{"dot component", "", "acme/."},
	{"parent component", "", "acme/.."},
	{"empty normalized name", "", "acme/.git"},
}

func assertGitHubEnvironmentUnchanged(t *testing.T, host, repo string, unset bool) {
	t.Helper()
	for name, want := range map[string]string{"GH_HOST": host, "GH_REPO": repo} {
		got, present := os.LookupEnv(name)
		if got != want || present == unset {
			t.Fatalf("%s was changed: value=%q, present=%t; want value=%q, present=%t", name, got, present, want, !unset)
		}
	}
}

func assertContextDiagnostic(t *testing.T, diagnostic, host, repo string) {
	t.Helper()
	wants := []string{"configured GitHub", "github.com", "remediation:"}
	if host != "" && host != "github.com" {
		wants = append(wants, fmt.Sprintf("observed GH_HOST=%q", host), "unset GH_HOST", "set GH_HOST=github.com")
	}
	if repo != "" && repo != "github.com/acme/iro" {
		wants = append(wants, "github.com/acme/iro", fmt.Sprintf("observed GH_REPO=%q", repo), "unset GH_REPO", "set GH_REPO=github.com/acme/iro")
	}
	for _, want := range wants {
		if !strings.Contains(diagnostic, want) {
			t.Errorf("diagnostic lacks %q: %s", want, diagnostic)
		}
	}
}

func TestGitHubContextRejectsBeforeSideEffects(t *testing.T) {
	for _, operation := range []string{"run", "review", "revise", "land", "doctor"} {
		for _, tc := range invalidGitHubContexts {
			t.Run(operation+"/"+tc.name, func(t *testing.T) {
				root := t.TempDir()
				writeProjectFiles(t, root)
				runner := &fakeCommandRunner{fn: func(spec CommandSpec) CommandResult {
					switch {
					case spec.Name == "git" && reflect.DeepEqual(spec.Args, []string{"rev-parse", "--show-toplevel"}):
						return CommandResult{Stdout: root}
					case spec.Name == "git" && reflect.DeepEqual(spec.Args, []string{"config", "--get-all", "remote.origin.url"}):
						return CommandResult{Stdout: "git@github.com:acme/iro.git"}
					case operation == "doctor" && reflect.DeepEqual(spec.Args, []string{"--version"}):
						return CommandResult{Stdout: "test version"}
					case operation == "doctor" && spec.Name == "codex" && reflect.DeepEqual(spec.Args, []string{"login", "status"}):
						return CommandResult{}
					default:
						t.Fatalf("unexpected command before context rejection: %+v", spec)
						return CommandResult{ExitCode: 1}
					}
				}}
				service := newTestService(t, runner, root)
				// Permit only project-file reads; runtime state access and all writes fail.
				service.FileSystem = landProjectFiles{t: t, root: root}
				t.Setenv("GH_HOST", tc.host)
				t.Setenv("GH_REPO", tc.repo)
				args := []string{operation, "42"}
				if operation == "doctor" {
					args = args[:1]
				}
				var out, errOut strings.Builder
				if code := Execute(args, &out, &errOut, service); code == 0 {
					t.Fatal("mismatched or malformed context was accepted")
				}
				diagnostic := errOut.String()
				if operation == "doctor" {
					diagnostic = out.String()
					for _, want := range []string{"FAIL: GitHub CLI context", "FAIL: GitHub authentication", "OK: Codex authentication"} {
						if !strings.Contains(diagnostic, want) {
							t.Errorf("doctor did not report/continue checks: missing %q in %s", want, diagnostic)
						}
					}
				} else if out.Len() != 0 {
					t.Errorf("unexpected success output: %s", out.String())
				}
				assertContextDiagnostic(t, diagnostic, tc.host, tc.repo)
				assertGitHubEnvironmentUnchanged(t, tc.host, tc.repo, false)
			})
		}
	}
}

// Verify target selection at the subprocess boundary without contacting GitHub.
func assertExplicitGitHubTarget(t *testing.T, spec CommandSpec) {
	t.Helper()
	if spec.Name != "gh" || reflect.DeepEqual(spec.Args, []string{"--version"}) {
		return
	}
	if len(spec.Args) < 2 {
		t.Fatalf("unexpected GitHub command: %+v", spec)
	}
	switch spec.Args[0] {
	case "auth":
		if !reflect.DeepEqual(spec.Args, []string{"auth", "status", "--hostname", "github.com"}) {
			t.Fatalf("authentication lacks configured host: %+v", spec)
		}
	case "api":
		if !containsArgs(spec.Args, "--hostname", "github.com") {
			t.Fatalf("API lacks configured host: %+v", spec)
		}
		if spec.Args[1] == "graphql" {
			if !containsArgs(spec.Args, "-f", "owner=acme") || !containsArgs(spec.Args, "-f", "name=iro") {
				t.Fatalf("GraphQL lacks configured repository: %+v", spec)
			}
		} else {
			endpoint := spec.Args[1]
			if endpoint == "--paginate" {
				endpoint = spec.Args[2]
			}
			if !strings.HasPrefix(endpoint, "repos/acme/iro/") {
				t.Fatalf("REST lacks configured repository: %+v", spec)
			}
		}
	case "issue", "pr":
		if !containsArgs(spec.Args, "--repo", "github.com/acme/iro") {
			t.Fatalf("command lacks configured host/repository: %+v", spec)
		}
	case "repo":
		if !containsArgs(spec.Args, "clone", "github.com/acme/iro") {
			t.Fatalf("clone lacks configured host/repository: %+v", spec)
		}
	default:
		t.Fatalf("unexpected GitHub operation: %+v", spec)
	}
}

func TestGitHubOperationsAcceptMatchingContextAndBindTargets(t *testing.T) {
	for _, operation := range []string{"run", "review", "revise", "land", "doctor"} {
		for _, tc := range []struct {
			name, host, repo string
			unset            bool
		}{
			{"unset", "", "", true},
			{"empty", "", "", false},
			{"matching host", "github.com", "", false},
			{"matching repository", "", "acme/iro", false},
			{"matching full selector", "github.com", "github.com/acme/iro", false},
			{"normalized selector", "GITHUB.COM", "GITHUB.COM/Acme/IRO.git", false},
		} {
			t.Run(operation+"/"+tc.name, func(t *testing.T) {
				var service *Service
				var runner *fakeCommandRunner
				number := "42"
				switch operation {
				case "land":
					f := newLandFixture(t)
					service, runner = f.service, f.runner
				case "revise":
					f := newReviseFixture(t, false)
					service, runner = f.service, f.runner
				default:
					root := t.TempDir()
					writeProjectFiles(t, root)
					runner = &fakeCommandRunner{fn: func(spec CommandSpec) CommandResult {
						if operation == "review" {
							return reviewFakeResult(spec, root, "レビュー完了。")
						}
						return standardFakeResult(spec, root, "", false, false)
					}}
					service = newTestService(t, runner, root)
					if operation == "run" {
						number = "123"
					}
				}
				t.Setenv("GH_HOST", tc.host)
				t.Setenv("GH_REPO", tc.repo)
				if tc.unset {
					for _, name := range []string{"GH_HOST", "GH_REPO"} {
						if err := os.Unsetenv(name); err != nil {
							t.Fatal(err)
						}
					}
				}
				respond := runner.fn
				runner.fn = func(spec CommandSpec) CommandResult {
					assertExplicitGitHubTarget(t, spec)
					assertGitHubEnvironmentUnchanged(t, tc.host, tc.repo, tc.unset)
					return respond(spec)
				}
				args := []string{operation, number}
				if operation == "doctor" {
					args = args[:1]
				}
				var out, errOut strings.Builder
				if code := Execute(args, &out, &errOut, service); code != 0 {
					t.Fatalf("matching context rejected: %s\n%s", out.String(), errOut.String())
				}
				assertGitHubEnvironmentUnchanged(t, tc.host, tc.repo, tc.unset)
			})
		}
	}
}

func TestGitHubContextUsesConfiguredRemoteNormalization(t *testing.T) {
	for _, remote := range []string{"https://GitHub.com/Acme/IRO.git", "git@github.com:Acme/IRO.git", "ssh://git@github.com/Acme/IRO.git"} {
		t.Run(remote, func(t *testing.T) {
			identity, err := parseGitHubRemote(remote)
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("GH_HOST", "github.com")
			for _, selector := range []string{"acme/iro", "ACME/iro.git", "github.com/acme/iro", "GITHUB.COM/ACME/iro.git"} {
				t.Setenv("GH_REPO", selector)
				if err := checkGitHubContext(identity); err != nil {
					t.Fatalf("normalization differs between remote %q and selector %q: %v", remote, selector, err)
				}
			}
		})
	}
}

func TestGitHubIdentityRejectsMalformedRepositoryPaths(t *testing.T) {
	for _, path := range []string{"acme/", "acme/.git", "acme/.", "acme/..", "acme/iro/extra", "acme//iro", "acme/iro name", "acme/iro#fragment", "acme/iro?query", "acme/%69ro"} {
		t.Run(path, func(t *testing.T) {
			if _, err := parseGitHubRemote("git@github.com:" + path); err == nil {
				t.Fatal("malformed configured repository path was accepted")
			}
			if _, err := parseGitHubSelector(path, "github.com"); err == nil {
				t.Fatal("malformed selector path was accepted")
			}
		})
	}
}
