package iro

import "fmt"

// Unmanaged operations use the built-in backend without reading project config.
const unmanagedAgentType = "codex"

func unsupportedAgent(agentType string) error {
	return fmt.Errorf("unsupported agent.type %q; supported value is codex", agentType)
}

func (s *Service) requireAgent(agentType, root string) error {
	switch agentType {
	case "codex":
		return s.requireCodex(root)
	default:
		return unsupportedAgent(agentType)
	}
}

func (s *Service) requireCodex(root string) error {
	if err := s.requireExecutable("codex"); err != nil {
		return err
	}
	return s.checkAuth("codex", []string{"login", "status"}, root)
}

func (s *Service) runAuthor(agentType string, workspace string, identity RepositoryIdentity, target issue, options workerOptions) CommandResult {
	switch agentType {
	case "codex":
		return s.runCodex(workspace, identity, target, options)
	default:
		return CommandResult{ExitCode: 1, Err: unsupportedAgent(agentType)}
	}
}

func (s *Service) runReviewer(agentType string, workspace string, identity RepositoryIdentity, target reviewPullRequest, origin issue, configData, workflowData []byte, context reviewContext, options workerOptions) CommandResult {
	switch agentType {
	case "codex":
		return s.runCodexReviewer(workspace, identity, target, origin, configData, workflowData, context, options)
	default:
		return CommandResult{ExitCode: 1, Err: unsupportedAgent(agentType)}
	}
}

func (s *Service) runRevisionAuthor(agentType string, workspace string, identity RepositoryIdentity, target reviewPullRequest, origin issue, configData, workflowData []byte, context reviewContext, options workerOptions) CommandResult {
	switch agentType {
	case "codex":
		return s.runCodexRevisionAuthor(workspace, identity, target, origin, configData, workflowData, context, options)
	default:
		return CommandResult{ExitCode: 1, Err: unsupportedAgent(agentType)}
	}
}

func (s *Service) runUnmanagedRevisionAuthor(agentType string, workspace string, identity RepositoryIdentity, target reviewPullRequest, specification issue, context reviewContext, options workerOptions) CommandResult {
	switch agentType {
	case "codex":
		return s.runCodexUnmanagedRevisionAuthor(workspace, identity, target, specification, context, options)
	default:
		return CommandResult{ExitCode: 1, Err: unsupportedAgent(agentType)}
	}
}
