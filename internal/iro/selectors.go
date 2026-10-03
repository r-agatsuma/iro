package iro

import "fmt"

// Unmanaged commands use the built-in runtime, never project configuration.
const unmanagedAgentType = "codex"

func unsupportedTracker(value string) error {
	return fmt.Errorf("unsupported tracker.type %q; supported value is github", value)
}

func unsupportedAgent(value string) error {
	return fmt.Errorf("unsupported agent.type %q; supported value is codex", value)
}

func (s *Service) selectAgent(agentType string) (codexRuntime, error) {
	switch agentType {
	case "codex":
		return codexRuntime{runner: s.Runner}, nil
	default:
		return codexRuntime{}, unsupportedAgent(agentType)
	}
}

func (s *Service) requireTrackerExecutable(trackerType string) error {
	switch trackerType {
	case "github":
		return s.requireExecutable("gh")
	default:
		return unsupportedTracker(trackerType)
	}
}

func (s *Service) checkTrackerAuth(trackerType, root string, identity RepositoryIdentity) error {
	switch trackerType {
	case "github":
		return s.checkAuth("gh", []string{"auth", "status", "--hostname", identity.Host()}, root)
	default:
		return unsupportedTracker(trackerType)
	}
}
