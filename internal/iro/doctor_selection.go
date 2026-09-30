package iro

import (
	"fmt"
	"io"
)

// Tool inventory diagnostics precede config resolution and remain available
// even when the project cannot be loaded.
func (s *Service) trackerToolDiagnostics(out io.Writer, trackerType string) {
	switch trackerType {
	case "github":
		s.toolDiagnostics(out, "gh")
	}
}

func (s *Service) agentToolDiagnostics(out io.Writer, agentType string) {
	switch agentType {
	case "codex":
		s.toolDiagnostics(out, "codex")
	}
}

func (s *Service) doctorTracker(out io.Writer, trackerType, root string, config Config, configValid bool, check func(string, func() error)) {
	switch trackerType {
	case "github":
		s.doctorGitHub(out, root, config, configValid, check)
	default:
		check("tracker selection", func() error { return fmt.Errorf("unsupported tracker.type %q; supported value is github", trackerType) })
	}
}

func (s *Service) doctorGitHub(out io.Writer, root string, config Config, configValid bool, check func(string, func() error)) {
	remote, host, repository := "", "", ""
	var identity RepositoryIdentity

	if root != "" {
		if configValid {
			remote = config.TrackerRemote
			check("configured GitHub remote", func() error {
				var err error
				identity, err = s.repositoryIdentity(root, config)
				if err == nil {
					host, repository = identity.Host(), identity.String()
				}
				return err
			})
		} else {
			check("configured GitHub remote", func() error { return fmt.Errorf("iro.toml is invalid") })
		}
	}

	fmt.Fprintf(out, "repository configured remote: %s\nrepository GitHub host: %s\nrepository owner/repository: %s\n", knownValue(remote), knownValue(host), knownValue(repository))

	contextValid := false
	check("GitHub CLI context", func() error {
		if repository == "" {
			return fmt.Errorf("configured repository identity is unavailable; fix the configured GitHub remote and retry")
		}
		if err := checkGitHubContext(identity); err != nil {
			return err
		}
		contextValid = true
		return nil
	})

	ghAvailable := false
	check("gh executable", func() error {
		if err := s.requireExecutable("gh"); err != nil {
			return err
		}
		ghAvailable = true
		return nil
	})
	check("GitHub authentication", func() error {
		if !ghAvailable {
			return fmt.Errorf("gh executable is unavailable")
		}
		if !contextValid {
			return fmt.Errorf("GitHub CLI context is invalid; resolve the context diagnostic and retry")
		}
		return s.checkAuth("gh", []string{"auth", "status", "--hostname", identity.Host()}, root)
	})
}

func (s *Service) doctorAgent(agentType, root string, check func(string, func() error)) {
	switch agentType {
	case "codex":
		s.doctorCodex(root, check)
	default:
		check("agent selection", func() error { return unsupportedAgent(agentType) })
	}
}

func (s *Service) doctorCodex(root string, check func(string, func() error)) {
	codexAvailable := false
	check("codex executable", func() error {
		if err := s.requireExecutable("codex"); err != nil {
			return err
		}
		codexAvailable = true
		return nil
	})
	if codexAvailable {
		check("Codex authentication", func() error {
			return s.checkAuth("codex", []string{"login", "status"}, root)
		})
	} else {
		check("Codex authentication", func() error { return fmt.Errorf("codex executable is unavailable") })
	}
}
