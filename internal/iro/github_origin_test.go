package iro

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestGitHubOriginTokenBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       []int
	}{
		{"body boundaries", "#73", []int{73}},
		{"ordinary prose", "Implement #73 please", []int{73}},
		{"fenced code", "```go\n#73\n```", []int{73}},
		{"inline code", "Use ` #73 ` here", []int{73}},
		{"blockquote", "> #73\n", []int{73}},
		{"deduplicated", "#73 Refs #73. Closes #74 #73 #74", []int{73, 74}},
		{"first occurrence order", "#74 #73 #74", []int{74, 73}},
		{"qualified reference", "owner/repo#73", nil},
		{"URL fragment", "https://github.com/owner/repo/issues/12#73", nil},
		{"URL fragment with whitespace suffix", "https://example.com/#73 next", nil},
		{"letters prefix", "abc#73", nil},
		{"non-ASCII prefix", "あ#73", nil},
		{"zero", "#0", nil},
		{"leading zeros", "#073 #0073 #00", nil},
		{"non-ASCII digits", "#７３ #٧٣ #73７", nil},
		{"bare hash", "#", nil},
		{"letters suffix", "#73abc", nil},
		{"local fragment suffix", "#73#74", nil},
		{"no suffix matching", "#073 owner/repo#74 abc#75 https://example.com/#76 #77abc", nil},
		{"unspaced inline code", "`#73`", nil},
		{"unspaced quote", ">#73", nil},
		{"max GraphQL integer", "#2147483647", []int{2147483647}},
		{"invalid overflow boundary", "abc#999999999999999999999 #999999999999999999999x", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := extractGitHubOriginCandidates(tc.body)
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("extract(%q) = %v, %v; want %v", tc.body, got, err, tc.want)
			}
		})
	}
	for _, suffix := range ".,;:!?)]}" {
		t.Run(fmt.Sprintf("suffix_%U", suffix), func(t *testing.T) {
			got, err := extractGitHubOriginCandidates("#73" + string(suffix) + "text")
			if err != nil || !reflect.DeepEqual(got, []int{73}) {
				t.Fatalf("allowed suffix %q: %v, %v", suffix, got, err)
			}
		})
	}
	// The Unicode White_Space set must work on both sides of the token.
	for _, space := range "\t\n\v\f\r \u0085\u00a0\u1680\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200a\u2028\u2029\u202f\u205f\u3000" {
		t.Run(fmt.Sprintf("whitespace_%U", space), func(t *testing.T) {
			got, err := extractGitHubOriginCandidates("text" + string(space) + "#73" + string(space) + "text")
			if err != nil || !reflect.DeepEqual(got, []int{73}) {
				t.Fatalf("Unicode whitespace %U: %v, %v", space, got, err)
			}
		})
	}
	for _, boundary := range "([{'\"`/\\#-_+*=\u200b\ufeff" {
		t.Run(fmt.Sprintf("forbidden_boundary_%U", boundary), func(t *testing.T) {
			for _, body := range []string{string(boundary) + "#73", "#73" + string(boundary)} {
				got, err := extractGitHubOriginCandidates(body)
				if err != nil || len(got) != 0 {
					t.Fatalf("forbidden boundary in %q: %v, %v", body, got, err)
				}
			}
		})
	}
}

func originBodyResponse(t *testing.T, body string) string {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	// Conflicting metadata must never be used as a fallback or priority source.
	return `{"data":{"repository":{"nameWithOwner":"acme/selected","pullRequest":{"number":42,"body":` + string(encoded) + `,"title":"Closes #999","headRefName":"iro/issue-999","closingIssuesReferences":{"totalCount":1,"nodes":[{"number":999}]},"comments":[{"body":"#999"}],"timelineItems":{"nodes":[{"number":999}]}}}}}`
}

func originCandidateResponse(number int) string {
	return fmt.Sprintf(`{"data":{"repository":{"nameWithOwner":"ACME/SELECTED","issueOrPullRequest":{"__typename":"Issue","number":%d,"repository":{"nameWithOwner":"ACME/SELECTED"}}}}}`, number)
}

// This fixture only permits the current-body read and typed local candidate reads.
// Unexpected calls, historical sources, or a different repository fail the test.
func newOriginRunner(t *testing.T, body *string, candidates map[int]CommandResult) *fakeCommandRunner {
	t.Helper()
	return &fakeCommandRunner{fn: func(spec CommandSpec) CommandResult {
		if spec.Name != "gh" || spec.Dir != "/selected" || len(spec.Args) != 12 || !reflect.DeepEqual(spec.Args[:4], []string{"api", "graphql", "--hostname", "github.com"}) || !reflect.DeepEqual(spec.Args[6:11], []string{"-f", "owner=acme", "-f", "name=selected", "-F"}) || spec.Args[4] != "-f" || spec.Stdin != nil {
			t.Fatalf("unexpected origin request: %+v", spec)
		}
		query := strings.TrimPrefix(spec.Args[5], "query=")
		number, err := strconv.Atoi(strings.TrimPrefix(spec.Args[11], "number="))
		if err != nil {
			t.Fatal(err)
		}
		switch query {
		case githubOriginBodyQuery:
			if number != 42 {
				t.Fatalf("read wrong PR: %d", number)
			}
			return CommandResult{Stdout: *body}
		case githubOriginCandidateQuery:
			if response, ok := candidates[number]; ok {
				return response
			}
			return CommandResult{Stdout: originCandidateResponse(number)}
		default:
			t.Fatalf("unexpected origin query: %s", query)
			return CommandResult{}
		}
	}}
}

func TestResolveGitHubOrigin(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		state      githubOriginState
		issues     []int
	}{
		{"empty body", "", githubOriginUnresolved, nil},
		{"no fallback", "owner/repo#73 https://example.com/#73 #073", githubOriginUnresolved, nil},
		{"plain local token", "#73", githubOriginResolved, []int{73}},
		{"repeated token", "Closes #73 Refs #73 #73.", githubOriginResolved, []int{73}},
		{"no keyword priority", "Closes #73 Refs #74", githubOriginAmbiguous, []int{73, 74}},
		{"all candidates validated", "#73 #74 #75", githubOriginAmbiguous, []int{73, 74, 75}},
		{"raw code and quote", "```\n#73\n```\n` #73 `\n> #73\nprose #73", githubOriginResolved, []int{73}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := originBodyResponse(t, tc.body)
			runner := newOriginRunner(t, &body, nil)
			service := &Service{Runner: runner}
			got, err := service.resolveGitHubOrigin("/selected", RepositoryIdentity{Owner: "acme", Name: "selected"}, 42)
			if err != nil || got.State != tc.state || !reflect.DeepEqual(got.Issues, tc.issues) || len(runner.calls) != 1+len(tc.issues) {
				t.Fatalf("resolve = %+v, %v; calls=%d; want %s %v", got, err, len(runner.calls), tc.state, tc.issues)
			}
		})
	}
	for _, query := range []string{githubOriginBodyQuery, githubOriginCandidateQuery} {
		for _, forbidden := range []string{"closingIssuesReferences", "timeline", "comments", "title", "headRef", "author", "createdAt"} {
			if strings.Contains(query, forbidden) {
				t.Fatalf("resolver consults forbidden source %s", forbidden)
			}
		}
	}
}

func TestResolveGitHubOriginCandidateFailure(t *testing.T) {
	valid := originCandidateResponse(75)
	for _, tc := range []struct {
		name   string
		result CommandResult
	}{
		{"PR contamination", CommandResult{Stdout: strings.Replace(valid, `"Issue"`, `"PullRequest"`, 1)}},
		{"missing candidate", CommandResult{Stdout: `{"data":{"repository":{"nameWithOwner":"acme/selected"}}}}`}},
		{"null candidate", CommandResult{Stdout: `{"data":{"repository":{"nameWithOwner":"acme/selected","issueOrPullRequest":null}}}}`}},
		{"unreadable candidate", CommandResult{ExitCode: 1, Stderr: "not found"}},
		{"API error with partial data", CommandResult{Stdout: strings.Replace(valid, `{"data":`, `{"errors":[{"message":"denied"}],"data":`, 1)}},
		{"runner error", CommandResult{Stdout: valid, Err: errors.New("API unavailable")}},
		{"null response", CommandResult{Stdout: "null"}},
		{"null repository", CommandResult{Stdout: `{"data":{"repository":null}}`}},
		{"malformed JSON", CommandResult{Stdout: "{"}},
		{"wrong number", CommandResult{Stdout: strings.Replace(valid, `"number":75`, `"number":76`, 1)}},
		{"null number", CommandResult{Stdout: strings.Replace(valid, `"number":75`, `"number":null`, 1)}},
		{"overflow API number", CommandResult{Stdout: strings.Replace(valid, `"number":75`, `"number":999999999999999999999`, 1)}},
		{"foreign candidate repository", CommandResult{Stdout: strings.Replace(valid, `"repository":{"nameWithOwner":"ACME/SELECTED"}`, `"repository":{"nameWithOwner":"other/repo"}`, 1)}},
		{"foreign query repository", CommandResult{Stdout: strings.Replace(valid, `"nameWithOwner":"ACME/SELECTED"`, `"nameWithOwner":"other/repo"`, 1)}},
		{"missing type", CommandResult{Stdout: strings.Replace(valid, `"__typename":"Issue",`, "", 1)}},
		{"null type", CommandResult{Stdout: strings.Replace(valid, `"__typename":"Issue"`, `"__typename":null`, 1)}},
		{"null candidate repository", CommandResult{Stdout: strings.Replace(valid, `"repository":{"nameWithOwner":"ACME/SELECTED"}`, `"repository":null`, 1)}},
	} {
		for _, raw := range []string{"#75", "#73 #75", "#73 #74 #75"} {
			t.Run(tc.name+"/"+raw, func(t *testing.T) {
				body := originBodyResponse(t, raw)
				runner := newOriginRunner(t, &body, map[int]CommandResult{75: tc.result})
				got, err := (&Service{Runner: runner}).resolveGitHubOrigin("/selected", RepositoryIdentity{Owner: "acme", Name: "selected"}, 42)
				if err == nil || !strings.Contains(err.Error(), "origin relation failed") || got.State != "" || len(got.Issues) != 0 {
					t.Fatalf("candidate failure manufactured a resolution: %+v, %v", got, err)
				}
			})
		}
	}
}

func TestResolveGitHubOriginBodyFailure(t *testing.T) {
	valid := originBodyResponse(t, "#73")
	for _, response := range []string{
		"null", "{", `{"data":{"repository":null}}`,
		strings.Replace(valid, `"pullRequest":{`, `"pullRequest":null,"ignored":{`, 1),
		strings.Replace(valid, `"body":"#73"`, `"body":null`, 1),
		strings.Replace(valid, `"body":"#73",`, "", 1),
		strings.Replace(valid, `"body":"#73"`, `"body":42`, 1),
		strings.Replace(valid, `"number":42`, `"number":43`, 1),
		strings.Replace(valid, `"nameWithOwner":"acme/selected"`, `"nameWithOwner":"other/repo"`, 1),
		strings.Replace(valid, `{"data":`, `{"errors":[{"message":"denied"}],"data":`, 1),
	} {
		runner := newOriginRunner(t, &response, nil)
		got, err := (&Service{Runner: runner}).resolveGitHubOrigin("/selected", RepositoryIdentity{Owner: "acme", Name: "selected"}, 42)
		if err == nil || got.State != "" || len(runner.calls) != 1 {
			t.Fatalf("invalid body response accepted: %s; %+v, %v", response, got, err)
		}
	}
	for _, result := range []CommandResult{{ExitCode: 1}, {Stdout: valid, Err: errors.New("unreadable")}} {
		runner := &fakeCommandRunner{fn: func(CommandSpec) CommandResult { return result }}
		if _, err := (&Service{Runner: runner}).resolveGitHubOrigin("/selected", RepositoryIdentity{Owner: "acme", Name: "selected"}, 42); err == nil {
			t.Fatal("failed PR body read accepted")
		}
	}
}

func TestResolveGitHubOriginOverflowFails(t *testing.T) {
	for _, raw := range []string{"#2147483648", "#999999999999999999999999999999999999999", "#73 #2147483648", "#73 #74 #999999999999999999999999"} {
		body := originBodyResponse(t, raw)
		runner := newOriginRunner(t, &body, nil)
		got, err := (&Service{Runner: runner}).resolveGitHubOrigin("/selected", RepositoryIdentity{Owner: "acme", Name: "selected"}, 42)
		if err == nil || !strings.Contains(err.Error(), "numeric range") || got.State != "" || len(runner.calls) != 1 {
			t.Fatalf("overflow token accepted: %q; %+v, %v", raw, got, err)
		}
	}
}

func TestResolveGitHubOriginRebindsCurrentBody(t *testing.T) {
	body := originBodyResponse(t, "#73")
	runner := newOriginRunner(t, &body, nil)
	service := &Service{Runner: runner}
	for _, tc := range []struct {
		body  string
		state githubOriginState
		want  []int
	}{{"#73", githubOriginResolved, []int{73}}, {"Refs #74", githubOriginResolved, []int{74}}, {"#73 #74", githubOriginAmbiguous, []int{73, 74}}, {"", githubOriginUnresolved, nil}} {
		body = originBodyResponse(t, tc.body)
		got, err := service.resolveGitHubOrigin("/selected", RepositoryIdentity{Owner: "acme", Name: "selected"}, 42)
		if err != nil || got.State != tc.state || !reflect.DeepEqual(got.Issues, tc.want) {
			t.Fatalf("current-body rebind failed: %+v, %v", got, err)
		}
	}
	if len(runner.calls) != 8 {
		t.Fatalf("expected four fresh body reads and four candidate reads; got %d calls", len(runner.calls))
	}
}

func TestGitHubRunBodyOriginContract(t *testing.T) {
	for _, tc := range []struct {
		unmanaged bool
		want      string
	}{{false, "Issue #73 の実装です。\n\nCloses #73\n"}, {true, "Issue #73 の実装です。\n\nRefs #73\n"}} {
		body := githubRunBody(73, tc.unmanaged)
		if body != tc.want {
			t.Fatalf("writer body = %q; want %q", body, tc.want)
		}
		response := originBodyResponse(t, body)
		runner := newOriginRunner(t, &response, nil)
		got, err := (&Service{Runner: runner}).resolveGitHubOrigin("/selected", RepositoryIdentity{Owner: "acme", Name: "selected"}, 42)
		if err != nil || got.State != githubOriginResolved || !reflect.DeepEqual(got.Issues, []int{73}) || len(runner.calls) != 2 {
			t.Fatalf("writer did not yield one deduplicated Issue: %+v, %v", got, err)
		}
	}
}
