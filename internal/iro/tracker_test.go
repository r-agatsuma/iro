package iro

import (
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func TestRunValidatesRequiredIssueCommentDataBeforeWorktreeCreation(t *testing.T) {
	for _, unmanaged := range []bool{false, true} {
		mode := "managed"
		if unmanaged {
			mode = "unmanaged"
		}
		for _, tc := range []struct {
			name, comments, payload string
		}{
			{name: "missing comments"},
			{name: "null comments", comments: `null`},
			{name: "object comments", comments: `{}`},
			{name: "null comment", comments: `[null]`},
			{name: "missing body", comments: `[{"id":"IC_1","createdAt":"2025-01-02T03:04:05Z"}]`},
			{name: "null body", comments: `[{"id":"IC_1","createdAt":"2025-01-02T03:04:05Z","body":null}]`},
			{name: "non-string body", comments: `[{"id":"IC_1","createdAt":"2025-01-02T03:04:05Z","body":42}]`},
			{name: "empty comments", comments: `[]`, payload: "Issue comments (ordered by createdAt, then immutable ID):\n(none)\n"},
			{name: "empty body without author", comments: `[{"id":"IC_1","createdAt":"2025-01-02T03:04:05Z","body":""}]`, payload: "ID: IC_1\nAuthor: (unknown)\nCreated at: 2025-01-02T03:04:05Z\nBody:\n\n"},
			{name: "body with null author", comments: `[{"id":"IC_1","author":null,"createdAt":"2025-01-02T03:04:05Z","body":" first line\nsecond line\t"}]`, payload: "ID: IC_1\nAuthor: (unknown)\nCreated at: 2025-01-02T03:04:05Z\nBody:\n first line\nsecond line\t\n"},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				f := newUnmanagedFixture(t)
				args := []string{"run", "123"}
				respond := f.runner.fn
				if unmanaged {
					args = append(args, "--unmanaged")
				} else {
					writeProjectFiles(t, f.root)
					respond = func(spec CommandSpec) CommandResult {
						return standardFakeResult(spec, f.root, "", false, false)
					}
				}
				data := map[string]any{"number": 123, "title": "Task", "body": "Task body", "url": "https://github.com/acme/iro/issues/123"}
				if tc.comments != "" {
					data["comments"] = json.RawMessage(tc.comments)
				}
				response, err := json.Marshal(data)
				if err != nil {
					t.Fatal(err)
				}
				f.runner.fn = func(spec CommandSpec) CommandResult {
					if spec.Name == "gh" && containsArgs(spec.Args, "issue", "view") {
						return CommandResult{Stdout: string(response)}
					}
					return respond(spec)
				}
				var diagnostic strings.Builder
				code := Execute(args, io.Discard, &diagnostic, f.service)
				if tc.payload != "" {
					if code != 0 {
						t.Fatal(diagnostic.String())
					}
					for _, call := range f.runner.calls {
						if call.Name == "codex" && containsString(call.Args, "--ephemeral") && strings.Contains(string(call.Stdin), tc.payload) {
							return
						}
					}
					t.Fatal("worker did not receive the complete comment payload")
				}
				if code != 1 || !strings.Contains(diagnostic.String(), "GitHub returned invalid") {
					t.Fatalf("incomplete Issue data accepted: code=%d stderr=%s", code, diagnostic.String())
				}
				for _, call := range f.runner.calls {
					if call.Name == "codex" || call.Name == "git" && containsArgs(call.Args, "worktree", "add") || call.Name == "gh" && (containsString(call.Args, "POST") || containsString(call.Args, "comment")) {
						t.Fatalf("side effect after invalid Issue comments: %+v", call)
					}
				}
			})
		}
	}
}
