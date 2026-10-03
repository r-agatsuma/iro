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

// codexRuntime is the concrete Codex worker path, not a shared agent contract.
type codexRuntime struct {
	service *Service
}

func (s *Service) selectAgent(agentType string) (codexRuntime, error) {
	switch agentType {
	case "codex":
		return codexRuntime{service: s}, nil
	default:
		return codexRuntime{}, unsupportedAgent(agentType)
	}
}

func (c codexRuntime) requireExecutable() error {
	return c.service.requireExecutable("codex")
}

func (c codexRuntime) checkAuth(root string) error {
	return c.service.checkAuth("codex", []string{"login", "status"}, root)
}

func (c codexRuntime) preflight(root string) error {
	if err := c.requireExecutable(); err != nil {
		return err
	}
	return c.checkAuth(root)
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
