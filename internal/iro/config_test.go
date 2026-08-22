package iro

import "testing"

func TestParseConfigAcceptsGeneratedTemplate(t *testing.T) {
	config, err := parseConfig([]byte(configTemplate))
	if err != nil {
		t.Fatalf("parseConfig() error = %v", err)
	}
	if config.Version != 1 || config.TrackerType != "github" || config.TrackerRemote != "origin" || config.AgentType != "codex" || config.WorkspaceStrategy != "git-worktree" {
		t.Fatalf("unexpected config: %+v", config)
	}
}

func TestParseConfigRejectsUnsupportedValue(t *testing.T) {
	data := []byte(`version = 1

[tracker]
type = "github"
remote = "origin"

[agent]
type = "other"

[workspace]
strategy = "git-worktree"
`)
	if _, err := parseConfig(data); err == nil {
		t.Fatal("parseConfig() unexpectedly accepted unsupported agent type")
	}
}

func TestParseGitHubRemote(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		{name: "https", url: "https://github.com/acme/iro.git", want: "acme/iro"},
		{name: "ssh", url: "git@github.com:acme/iro.git", want: "acme/iro"},
		{name: "ssh url", url: "ssh://git@github.com/acme/iro.git", want: "acme/iro"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			identity, err := parseGitHubRemote(tt.url)
			if err != nil {
				t.Fatalf("parseGitHubRemote() error = %v", err)
			}
			if identity.String() != tt.want {
				t.Fatalf("identity = %q, want %q", identity.String(), tt.want)
			}
		})
	}
}
