package iro

import (
	"fmt"
	"strconv"
	"strings"
)

type reportStage uint8

const (
	reportAuthor reportStage = iota + 1
	reportDelivery
	reportInspection
)

type reportStatus uint8

const (
	reportFailed reportStatus = iota + 1
	reportDelivered
	reportBlocker
)

// automaticReport contains only iro-owned publication facts. Never add local
// diagnostics, errors, paths, worker prose, or inspection rationale to this type.
// Integer enums are rendered through closed mappings, not their input values.
type automaticReport struct {
	issue    int
	stage    reportStage
	status   reportStatus
	prNumber int
	land     bool
	reason   inspectionReason
}

func buildAutomaticReport(report automaticReport) (string, error) {
	stage := ""
	switch report.stage {
	case reportAuthor:
		stage = "author"
	case reportDelivery:
		stage = "delivery"
	case reportInspection:
		stage = "inspection"
	default:
		return "", fmt.Errorf("invalid automatic report stage")
	}
	status := ""
	switch report.status {
	case reportFailed:
		status = "FAILED"
	case reportDelivered:
		status = "DELIVERED"
	case reportBlocker:
		status = "BLOCKER"
	default:
		return "", fmt.Errorf("invalid automatic report status")
	}
	if report.issue <= 0 || report.prNumber < 0 ||
		(report.status == reportDelivered && (report.stage != reportDelivery || report.prNumber == 0)) ||
		(report.land && report.status != reportDelivered) {
		return "", fmt.Errorf("invalid automatic report facts")
	}
	if (report.status == reportBlocker && (report.stage != reportInspection || report.reason == noInspectionReason || report.reason.name() == "")) ||
		(report.status != reportBlocker && report.reason != noInspectionReason) ||
		(report.stage == reportInspection && (report.prNumber != 0 || report.land || report.status == reportDelivered)) {
		return "", fmt.Errorf("invalid automatic inspection report facts")
	}
	var body strings.Builder
	fmt.Fprintf(&body, "## iro execution report\n\nStatus: %s\nStage: %s\nSource Issue: #%d\nDelivery confirmed: %t\n", status, stage, report.issue, report.prNumber > 0)
	if report.stage == reportInspection {
		fmt.Fprintf(&body, "INSPECTION: %s\nAUTHOR: not started\nDELIVERY: not attempted\n", status)
		if report.status == reportBlocker {
			fmt.Fprintf(&body, "Reason class: %s\n", report.reason.name())
		}
	}
	if report.prNumber > 0 {
		fmt.Fprintf(&body, "PR: #%d\n", report.prNumber)
	}
	if report.land {
		fmt.Fprintf(&body, "\nLand:\n\n`iro land %d`\n", report.prNumber)
	}
	if report.status == reportFailed {
		body.WriteString("\nローカルのログと端末の診断、および local / remote state を確認してください。自動 retry / rollback / repair は行いません。\n")
	}
	return body.String(), nil
}

func (s *Service) postDeliveryReport(root string, identity RepositoryIdentity, report automaticReport) CommandResult {
	body, err := buildAutomaticReport(report)
	if err != nil {
		return CommandResult{ExitCode: -1, Err: err}
	}
	return s.Runner.Run(CommandSpec{
		Name: "gh",
		Args: []string{"pr", "comment", strconv.Itoa(report.prNumber), "--repo", identity.Selector(), "--body", body},
		Dir:  root,
	})
}

// reviewArtifact is an intentionally authored remote artifact, distinct from an
// automatic execution report. Only the designated Reviewer final body belongs here.
type reviewArtifact struct {
	body string
}
