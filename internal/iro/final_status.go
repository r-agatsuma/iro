package iro

import (
	"encoding/json"
	"fmt"
	"io"
)

// invocationResult is a local projection of the existing inspection, worker and
// delivery outcomes. It is never an input to automatic remote publication.
type invocationResult struct {
	Operation              string                  `json:"operation"`
	Unmanaged              bool                    `json:"unmanaged"`
	Repository             string                  `json:"repository"`
	Issue                  int                     `json:"issue_number"`
	PRNumber               int                     `json:"pr_number,omitempty"`
	Source                 string                  `json:"source_snapshot"`
	Inspection             string                  `json:"inspection"`
	InspectionReason       string                  `json:"inspection_reason"`
	Worker                 string                  `json:"worker"`
	Delivery               string                  `json:"delivery"`
	Push                   string                  `json:"push,omitempty"`
	PRCreation             string                  `json:"pr_creation,omitempty"`
	RemoteReport           string                  `json:"remote_report"`
	LocalLog               string                  `json:"local_log"`
	InspectionDiagnostic   string                  `json:"inspection_diagnostic"`
	WorkerDiagnostic       *localCommandDiagnostic `json:"worker_diagnostic,omitempty"`
	RemoteReportDiagnostic *localCommandDiagnostic `json:"remote_report_diagnostic,omitempty"`
	OperationError         string                  `json:"operation_error,omitempty"`
	PublicationError       string                  `json:"publication_error,omitempty"`
	WorkerLog              string                  `json:"worker_log,omitempty"`
	DeliveryDiagnostic     string                  `json:"delivery_diagnostic,omitempty"`

	active bool
	run    *runDeliveryState
}

type localCommandDiagnostic struct {
	ExitStatus int    `json:"exit_status"`
	Error      string `json:"error,omitempty"`
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
}

func (r *invocationResult) workerFinished(result CommandResult) {
	r.Worker = "success"
	if !commandSucceeded(result) {
		r.Worker = fmt.Sprintf("failed (exit %d)", result.ExitCode)
	}
	r.WorkerDiagnostic = commandDiagnostic(result)
}

func commandDiagnostic(result CommandResult) *localCommandDiagnostic {
	d := &localCommandDiagnostic{ExitStatus: result.ExitCode, Stdout: result.Stdout, Stderr: result.Stderr}
	if result.Err != nil {
		d.Error = result.Err.Error()
	}
	return d
}

func (r *invocationResult) issueReportFinished(result CommandResult, err error) {
	r.RemoteReportDiagnostic = commandDiagnostic(result)
	r.RemoteReport = "published"
	if err != nil {
		r.RemoteReport = "failed (remote outcome unknown)"
		r.PublicationError = err.Error()
	}
}

func (s *Service) finishInvocation(r *invocationResult, operationErr error, out, errOut io.Writer) {
	if !r.active {
		return
	}
	if operationErr != nil {
		r.OperationError = operationErr.Error()
	}
	if r.run != nil {
		r.DeliveryDiagnostic = r.run.diagnostic()
		r.Push, r.PRCreation, r.PRNumber = r.run.push, r.run.pr, r.run.prNumber
		if r.PRNumber > 0 {
			r.Delivery = fmt.Sprintf("PR #%d created", r.PRNumber)
		} else if r.Delivery != "not attempted" {
			r.Delivery = fmt.Sprintf("failed (push: %s; PR: %s)", r.Push, r.PRCreation)
		}
		// A delivery comment attempt belongs to the existing delivery receipt.
		if r.run.report == "confirmed success" {
			r.RemoteReport = "published"
		} else if r.run.report != "not attempted" {
			r.RemoteReport = "failed (remote outcome unknown)"
		}
		if r.run.reportDiagnostic != nil {
			r.RemoteReportDiagnostic = r.run.reportDiagnostic
		}
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err == nil && r.LocalLog != "" {
		err = s.FileSystem.WriteFile(r.LocalLog, append(data, '\n'), 0600)
	} else if err == nil {
		err = fmt.Errorf("invocation log directory was not created")
	}
	logIdentity := r.LocalLog
	if err != nil {
		fmt.Fprintf(errOut, "warning: could not preserve final status: %v\n%s\n", err, data)
		if logIdentity == "" {
			logIdentity = "unavailable"
		} else {
			logIdentity += " (not saved)"
		}
	}
	report := r.RemoteReport
	if report != "published" && report != "not attempted" && report != "not applicable (local only)" {
		report += "; existing tracker comments may be stale; local result is authoritative"
	}
	fmt.Fprintf(out, "\nINSPECTION: %s\nWORKER: %s\nDELIVERY: %s\nREMOTE REPORT: %s\nLOCAL LOG: %s\n", r.Inspection, r.Worker, r.Delivery, report, logIdentity)
	if r.Operation == "run" && !r.Unmanaged && r.PRNumber > 0 {
		fmt.Fprintf(out, "LAND: iro land %d\n", r.PRNumber)
	}
}
