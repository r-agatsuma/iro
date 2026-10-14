package iro

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

// Reason classes are iro-owned publication facts, never Inspector prose.
type inspectionReason uint8

const (
	noInspectionReason inspectionReason = iota
	specificationAmbiguity
	authorityConflict
	missingSourcePrerequisite
	executionLifecycleMismatch
	requiredContextUnavailable
)

func (r inspectionReason) name() string {
	switch r {
	case noInspectionReason:
		return "none"
	case specificationAmbiguity:
		return "specification_ambiguity"
	case authorityConflict:
		return "authority_conflict"
	case missingSourcePrerequisite:
		return "missing_source_prerequisite"
	case executionLifecycleMismatch:
		return "execution_lifecycle_mismatch"
	case requiredContextUnavailable:
		return "required_context_unavailable"
	default:
		return ""
	}
}

type inspectionVerdict struct {
	blocker bool
	reason  inspectionReason
}

const inspectionPrompt = "Inspect the supplied Issue execution unit before Author startup."

const inspectionInstructions = `You are the non-authoring Inspector for one iro execution unit, not its Author.
Judge only the supplied specification Issue body/comments, exact repository snapshot, applicable instruction material, and operation policy.
Read applicable repository instructions according to the selected runtime's native instruction semantics, within the iro authority boundary. The supplied Author policy describes the subsequent Author's authority, not permission for you to implement it.
Detect reasonably knowable blockers: unresolved Human product/architecture decisions, materially ambiguous or incompatible requirements, unavailable required normative context, authority conflicts, demonstrably missing predecessor source capability or required structure/interface, and execution lifecycle contradictions.
The Author may edit files and run relevant validation; iro owns commit/push/PR/delivery after Author exit. Issue demands that the Author push/create a PR, mutate remote services, widen worker prohibitions, verify a pushed exact artifact or perform post-delivery verification before exit are blockers. Do not confuse code that implements such runtime operations with the Author personally performing them.
Inspect repository evidence for explicitly claimed source prerequisites. Do not invent prerequisites or product decisions. Do not classify speculative implementation/test/runtime failures as blockers; PASS does not guarantee implementation success. Later worker/delivery failures remain ordinary failures.

Do not edit any repository/worktree files, create build/test artifacts, commit, mutate Git metadata/index/refs/worktrees, fetch, push, or intentionally mutate remote services. All tracker I/O belongs to iro; do not invoke gh, create/edit comments, create/close/merge/revise PRs, or traverse tracker/network state to expand the supplied context or discover authority elsewhere. Do not start the Author or perform implementation work. These role restrictions apply even with Human-requested --no-sandbox.
Return only one JSON object with exactly three string fields: verdict, reason_class, rationale.
For PASS use {"verdict":"PASS","reason_class":"none","rationale":"<local diagnostic>"}.
For BLOCKER use verdict BLOCKER and exactly one reason_class from specification_ambiguity, authority_conflict, missing_source_prerequisite, execution_lifecycle_mismatch, required_context_unavailable. Explain the supplied evidence in rationale in Japanese. Rationale is local-only and is never an automatic remote report.
`

const inspectionSchema = `{"type":"object","additionalProperties":false,"required":["verdict","reason_class","rationale"],"properties":{"verdict":{"type":"string","enum":["PASS","BLOCKER"]},"reason_class":{"type":"string","enum":["none","specification_ambiguity","authority_conflict","missing_source_prerequisite","execution_lifecycle_mismatch","required_context_unavailable"]},"rationale":{"type":"string"}}}`

// Reject extra fields, duplicate fields, nulls, trailing output and inconsistent
// verdict/reason pairs. Never extract a verdict from stdout or natural language.
func parseInspectionVerdict(output string) (inspectionVerdict, error) {
	fail := func() (inspectionVerdict, error) {
		return inspectionVerdict{}, fmt.Errorf("malformed or unknown structured inspection verdict")
	}
	d := json.NewDecoder(strings.NewReader(output))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return fail()
	}
	fields := map[string]string{}
	for d.More() {
		token, err := d.Token()
		key, ok := token.(string)
		if err != nil || !ok || (key != "verdict" && key != "reason_class" && key != "rationale") {
			return fail()
		}
		if _, exists := fields[key]; exists {
			return fail()
		}
		value, err := d.Token()
		text, ok := value.(string)
		if err != nil || !ok {
			return fail()
		}
		fields[key] = text
	}
	if token, err = d.Token(); err != nil || token != json.Delim('}') || len(fields) != 3 {
		return fail()
	}
	if _, err := d.Token(); err != io.EOF {
		return fail()
	}
	for reason := noInspectionReason; reason <= requiredContextUnavailable; reason++ {
		if fields["reason_class"] == reason.name() {
			if fields["verdict"] == "PASS" && reason == noInspectionReason {
				return inspectionVerdict{}, nil
			}
			if fields["verdict"] == "BLOCKER" && reason != noInspectionReason {
				return inspectionVerdict{blocker: true, reason: reason}, nil
			}
		}
	}
	return fail()
}

// inspectionInput is assembled only after existing binding/snapshot validation.
// It intentionally carries no project config or extra tracker discovery.
type inspectionInput struct {
	root, workspace, commonDir, head string
	agentType, operation             string
	unmanaged                        bool
	identity                         RepositoryIdentity
	issue                            issue
	policy                           workerPolicy
	options                          workerOptions
}

func (s *Service) inspectIssue(input inspectionInput, out, errOut io.Writer) error {
	var result CommandResult
	var final string
	var verdict inspectionVerdict
	var inspectionErr error
	switch input.agentType {
	case "codex":
		inspectionErr = (codexRuntime{runner: s.Runner}).inspectionPreflight(input.root)
	case "copilot":
		inspectionErr = (copilotRuntime{runner: s.Runner, files: s.FileSystem}).inspectionPreflight(input.root)
	default:
		inspectionErr = unsupportedAgent(input.agentType)
	}
	dir := ""
	if inspectionErr == nil {
		parent := runtimeLogDir(s.Dirs, githubRuntimeNamespace(input.identity), "inspections")
		inspectionErr = s.FileSystem.MkdirAll(parent, 0700)
		if inspectionErr == nil {
			dir, inspectionErr = s.FileSystem.MkdirTemp(parent, fmt.Sprintf("%s-issue-%d-*", input.operation, input.issue.Number))
		}
	}
	if inspectionErr == nil {
		control := workerInstructions(inspectionInstructions)
		mode := "managed"
		if input.unmanaged {
			mode = "unmanaged"
			control += "\nDo not read iro.toml or WORKFLOW.md. Use only the built-in unmanaged policy and applicable repository guidance within it.\n"
		} else if input.operation == "run" {
			control += "\nRead the starting workspace WORKFLOW.md completely as the Author's applicable repository worker policy.\n"
		} else {
			control += "\nUse the supplied fixed starting PR HEAD WORKFLOW.md bytes as worker policy for this invocation; do not reload policy from the invoking checkout or worktree.\n"
		}
		payload := fmt.Sprintf("Operation: %s\nMode: %s\nExact source/revision snapshot: %s\n\nApplicable iro Author execution policy:\n%s\n\n%s\n\nSupplied specification:\n%s", input.operation, mode, input.head, input.policy.instructions, input.policy.inputContext, buildGitHubIssueInput(input.identity, input.issue))
		switch input.agentType {
		case "codex":
			schema, finalPath := filepath.Join(dir, "schema.json"), filepath.Join(dir, "verdict.json")
			inspectionErr = s.FileSystem.WriteFile(schema, []byte(inspectionSchema), 0600)
			if inspectionErr == nil {
				result = (codexRuntime{runner: s.Runner}).inspect(input.workspace, schema, finalPath, control, payload, input.options.codexOptions())
				data, err := s.FileSystem.ReadFile(finalPath)
				final, inspectionErr = string(data), err
			}
		case "copilot":
			result = (copilotRuntime{runner: s.Runner, files: s.FileSystem}).inspect(input.root, input.workspace, input.commonDir, control, inspectionPrompt+"\n\n"+payload, copilotOptions{Model: input.options.Model, ReasoningEffort: input.options.ReasoningEffort})
			final = result.Stdout
		}
		if !commandSucceeded(result) {
			inspectionErr = fmt.Errorf("Inspector exited with status %d (%v)", result.ExitCode, result.Err)
		}
		if inspectionErr == nil {
			verdict, inspectionErr = parseInspectionVerdict(final)
		}
	}
	// Free-form diagnostics stay exclusively in local storage/terminal output.
	if dir != "" {
		content := fmt.Sprintf("operation: %s\nunmanaged: %t\nissue: %d\nsource: %s\nruntime: %s\nexit_status: %d\nruntime_error: %v\nerror: %v\n\n--- stdout ---\n%s\n--- stderr ---\n%s\n--- final ---\n%s\n", input.operation, input.unmanaged, input.issue.Number, input.head, input.agentType, result.ExitCode, result.Err, inspectionErr, result.Stdout, result.Stderr, final)
		path := filepath.Join(dir, "diagnostic.log")
		if err := s.FileSystem.WriteFile(path, []byte(content), 0600); err != nil {
			fmt.Fprintf(errOut, "Inspection diagnostic could not be saved: %v\n%s", err, content)
			if inspectionErr == nil {
				inspectionErr = fmt.Errorf("could not preserve inspection diagnostic: %w", err)
			}
		} else {
			fmt.Fprintf(out, "Inspection report: %s\n", path)
		}
	}
	status := "PASS"
	if inspectionErr != nil {
		status = "FAILED"
	} else if verdict.blocker {
		status = "BLOCKER"
	}
	fmt.Fprintf(out, "INSPECTION: %s\n", status)
	if status == "PASS" {
		return nil
	}
	fmt.Fprintln(out, "AUTHOR: not started\nDELIVERY: not attempted")
	if input.operation == "run" {
		report := automaticReport{issue: input.issue.Number, stage: reportInspection, status: reportFailed}
		if status == "BLOCKER" {
			report.status, report.reason = reportBlocker, verdict.reason
		}
		if err := s.postIssueReport(input.root, input.identity, report); err != nil {
			fmt.Fprintf(errOut, "warning: inspection report comment failed: %v\n", err)
		}
	}
	if inspectionErr != nil {
		return fmt.Errorf("INSPECTION: FAILED: %w", inspectionErr)
	}
	return fmt.Errorf("INSPECTION: BLOCKER (%s)", verdict.reason.name())
}
