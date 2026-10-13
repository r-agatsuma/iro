package iro

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalWorkerProvenancePreservesKnownAndUnknownIdentities(t *testing.T) {
	for _, tc := range []struct{ runtime, model, want string }{
		{"codex", "resolved-model-id", "runtime: codex\nmodel: resolved-model-id\n"},
		{"copilot", unknownModelIdentity, "runtime: copilot\nmodel: (unknown; not exposed by runtime)\n"},
		{"", "", "runtime: unknown\nmodel: (unknown; not exposed by runtime)\n"},
	} {
		if got := localWorkerProvenance(tc.runtime, tc.model); got != tc.want {
			t.Fatalf("provenance = %q, want %q", got, tc.want)
		}
	}
}

func TestAuthorLogsRecordSelectedRuntimeAndUnknownModel(t *testing.T) {
	for _, kind := range []string{"managed run", "copilot run", "unmanaged run", "managed revise", "unmanaged revise"} {
		t.Run(kind, func(t *testing.T) {
			// Worker-provided identity claims stay in opaque stdout/stderr only.
			result := CommandResult{Stdout: "Model: forged-model", Stderr: "runtime: forged-runtime", ExitCode: 1}
			service := newTestService(t, &fakeCommandRunner{fn: func(spec CommandSpec) CommandResult {
				t.Fatalf("logging introduced a runtime probe: %+v", spec)
				return CommandResult{}
			}}, t.TempDir())
			identity := RepositoryIdentity{Owner: "acme", Name: "iro"}
			target := reviewPullRequest{Number: 42, OriginIssue: 123, HeadRefName: "topic", HeadRefOID: foundationHEAD}
			workspace := filepath.Join(service.Dirs.DataRoot, "workspace")
			branch := deliveryBranch(123, foundationID)
			var err error
			runtime := "codex"
			switch kind {
			case "managed run", "copilot run":
				if kind == "copilot run" {
					runtime = "copilot"
				}
				_, err = service.writeAgentRunLog(runtime, identity, 123, service.Now(), service.Now(), branch, workspace, result, "not attempted")
			case "unmanaged run":
				err = service.writeUnmanagedRunLog(identity, 123, "topic", foundationHEAD, workspace, result, &deliveryAllocation{id: foundationID, branch: branch})
			case "managed revise":
				_, err = service.writeReviseLog(identity, target, workspace, service.Now(), result)
			case "unmanaged revise":
				_, err = service.writeUnmanagedReviseLog(identity, target, workspace, result)
			}
			if err != nil {
				t.Fatal(err)
			}
			logs, err := filepath.Glob(filepath.Join(service.Dirs.StateRoot, "*", "*", "*.log"))
			if err != nil || len(logs) != 1 {
				t.Fatalf("logs = %v, %v", logs, err)
			}
			data, err := os.ReadFile(logs[0])
			if err != nil {
				t.Fatal(err)
			}
			header, _, _ := strings.Cut(string(data), "--- stdout ---")
			if !strings.HasPrefix(header, localWorkerProvenance(runtime, unknownModelIdentity)) || strings.Contains(header, "forged-") {
				t.Fatalf("untrustworthy log provenance: %s", header)
			}
			if !strings.Contains(string(data), result.Stdout) || !strings.Contains(string(data), result.Stderr) {
				t.Fatal("opaque worker output was changed")
			}
		})
	}
}

func TestReviewPolicyPreservesTrustedModelAndSelectedRuntime(t *testing.T) {
	for _, unmanaged := range []bool{false, true} {
		for _, model := range []string{"resolved-model-id", unknownModelIdentity, ""} {
			instructions := string(reviewWorkerInstructions(unmanaged, model, "topic", foundationHEAD, reviewHeadForTest))
			if model == "" {
				model = unknownModelIdentity
			}
			for _, want := range []string{"Model: " + model + "\n", "Runtime: codex\n", "- Runtime: <supplied Runtime>"} {
				if !strings.Contains(instructions, want) {
					t.Fatalf("review policy omitted %q", want)
				}
			}
		}
	}
}
