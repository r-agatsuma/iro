package iro

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type failingRunLogUpdateFS struct {
	FileSystem
	failure string
	writes  int
}

func (fs *failingRunLogUpdateFS) WriteFile(name string, data []byte, perm os.FileMode) error {
	if strings.HasSuffix(name, ".log") {
		fs.writes++
		if fs.writes == 2 && fs.failure == "write" {
			if err := fs.FileSystem.WriteFile(name, data[:len(data)/2], perm); err != nil {
				return err
			}
			return fmt.Errorf("injected partial write failure")
		}
	}
	return fs.FileSystem.WriteFile(name, data, perm)
}

func (fs *failingRunLogUpdateFS) Rename(oldpath, newpath string) error {
	if fs.writes == 2 && fs.failure == "rename" {
		return fmt.Errorf("injected rename failure")
	}
	return fs.FileSystem.Rename(oldpath, newpath)
}

func TestRunDeliveryFailurePreservesLogWhenUpdateFails(t *testing.T) {
	for _, failure := range []string{"write", "rename", ""} {
		t.Run("failure="+failure, func(t *testing.T) {
			root := t.TempDir()
			writeProjectFiles(t, root)
			runner := &fakeCommandRunner{}
			service := newTestService(t, runner, root)
			fs := &failingRunLogUpdateFS{FileSystem: service.FileSystem, failure: failure}
			service.FileSystem = fs
			var saved []byte
			var logPath string
			runner.fn = func(spec CommandSpec) CommandResult {
				if spec.Name == "gh" && containsArgs(spec.Args, "issue", "comment") {
					return CommandResult{ExitCode: 1}
				}
				if spec.Name == "git" && spec.Args[0] == "commit" {
					logs, err := filepath.Glob(filepath.Join(service.Dirs.StateRoot, "runs", identityKeyForTest(), "*.log"))
					if err != nil || len(logs) != 1 {
						t.Fatalf("missing report before delivery: %v, %v", logs, err)
					}
					logPath = logs[0]
					saved, err = os.ReadFile(logPath)
					if err != nil || !strings.Contains(string(saved), "issue_comment: not attempted") {
						t.Fatalf("invalid initial log: %s, %v", saved, err)
					}
					return CommandResult{ExitCode: 1}
				}
				return standardFakeResult(spec, root, "", false, false)
			}
			var errOut strings.Builder
			err := service.run(123, io.Discard, &errOut)
			if err == nil || !strings.Contains(err.Error(), "commit failed") {
				t.Fatalf("original delivery failure lost: %v", err)
			}
			if fs.writes != 2 || !strings.Contains(errOut.String(), "failure report comment failed") {
				t.Fatalf("missing update or comment failure: writes=%d, %s", fs.writes, errOut.String())
			}
			data, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			if failure != "" {
				if !bytes.Equal(data, saved) || !strings.Contains(errOut.String(), "injected") {
					t.Fatalf("saved log changed or update diagnostic missing: %s, %s", data, errOut.String())
				}
			} else if !strings.Contains(string(data), "issue_comment: failure:") {
				t.Fatalf("comment status was not updated: %s", data)
			}
			report := standardFakeResult(CommandSpec{Name: "codex", Args: []string{"--cd"}}, root, "", false, false).Stdout
			if report == "" || !strings.Contains(string(data), report) {
				t.Fatalf("Author report lost: %s", data)
			}
			entries, err := os.ReadDir(filepath.Dir(logPath))
			if err != nil || len(entries) != 1 {
				t.Fatalf("temporary log not removed: %v, %v", entries, err)
			}
		})
	}
}
