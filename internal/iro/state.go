package iro

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// RuntimeDirs contains local, non-repository runtime locations.
type RuntimeDirs struct {
	StateRoot string
	DataRoot  string
}

func DefaultRuntimeDirs() RuntimeDirs {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		home = "."
	}
	stateHome := os.Getenv("XDG_STATE_HOME")
	if stateHome == "" {
		stateHome = filepath.Join(home, ".local", "state")
	}
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		dataHome = filepath.Join(home, ".local", "share")
	}
	return RuntimeDirs{
		StateRoot: filepath.Join(stateHome, "iro"),
		DataRoot:  filepath.Join(dataHome, "iro"),
	}
}

type ownershipMapping struct {
	Version     int    `json:"version"`
	Repository  string `json:"repository"`
	IssueNumber int    `json:"issue_number"`
	Branch      string `json:"branch"`
	Worktree    string `json:"worktree"`
	CreatedAt   string `json:"created_at"`
}

func ownershipPath(dirs RuntimeDirs, identity RepositoryIdentity, issueNumber int) string {
	return filepath.Join(dirs.StateRoot, "ownership", identity.Key(), fmt.Sprintf("issue-%d.json", issueNumber))
}

func worktreePath(dirs RuntimeDirs, identity RepositoryIdentity, issueNumber int) string {
	return filepath.Join(dirs.DataRoot, "workspaces", identity.Key(), fmt.Sprintf("issue-%d", issueNumber))
}

func (s *Service) readOwnership(path string) (ownershipMapping, bool, error) {
	data, err := s.FileSystem.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ownershipMapping{}, false, nil
		}
		return ownershipMapping{}, false, fmt.Errorf("could not read ownership mapping: %w", err)
	}
	var mapping ownershipMapping
	if err := json.Unmarshal(data, &mapping); err != nil {
		return ownershipMapping{}, true, fmt.Errorf("ownership mapping is invalid: %w", err)
	}
	return mapping, true, nil
}

func (s *Service) writeOwnership(path string, mapping ownershipMapping) error {
	data, err := json.MarshalIndent(mapping, "", "  ")
	if err != nil {
		return fmt.Errorf("encode ownership mapping: %w", err)
	}
	data = append(data, '\n')
	if err := s.FileSystem.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create ownership state directory: %w", err)
	}
	file, err := s.FileSystem.CreateNew(path, 0600)
	if err != nil {
		return fmt.Errorf("record ownership mapping: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return fmt.Errorf("write ownership mapping: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close ownership mapping: %w", err)
	}
	return nil
}

func (s *Service) writeRunLog(identity RepositoryIdentity, issueNumber int, started, finished time.Time, branch, worktree string, codexResult CommandResult, commentError error) (string, error) {
	logDir := filepath.Join(s.Dirs.StateRoot, "runs", identity.Key())
	if err := s.FileSystem.MkdirAll(logDir, 0755); err != nil {
		return "", fmt.Errorf("create run log directory: %w", err)
	}
	logName := fmt.Sprintf("issue-%d-%d.log", issueNumber, started.UnixNano())
	logPath := filepath.Join(logDir, logName)
	commentStatus := "success"
	if commentError != nil {
		commentStatus = "failure: " + commentError.Error()
	}
	content := fmt.Sprintf("repository: %s\nissue_number: %d\nbranch: %s\nworktree: %s\nstarted: %s\nfinished: %s\ncodex_exit_status: %d\ncodex_error: %v\nissue_comment: %s\n\n--- stdout ---\n%s\n--- stderr ---\n%s\n",
		identity.String(), issueNumber, branch, worktree, started.UTC().Format(time.RFC3339Nano), finished.UTC().Format(time.RFC3339Nano), codexResult.ExitCode, codexResult.Err, commentStatus, codexResult.Stdout, codexResult.Stderr)
	if err := s.FileSystem.WriteFile(logPath, []byte(content), 0600); err != nil {
		return "", fmt.Errorf("write run log: %w", err)
	}
	return logPath, nil
}
