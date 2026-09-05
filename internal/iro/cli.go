package iro

import (
	"fmt"
	"io"
)

// Execute dispatches the bootstrap MVP CLI commands and returns an exit status.
func Execute(args []string, out, errOut io.Writer, service *Service) int {
	if len(args) == 0 {
		fmt.Fprintln(errOut, "error: command is required (init, doctor, status, run <issue-number>, review <pr-number>, or cleanup <issue-number>)")
		return 2
	}

	var err error
	switch args[0] {
	case "init":
		if len(args) != 1 {
			fmt.Fprintln(errOut, "error: init does not accept arguments")
			return 2
		}
		err = service.Init(out)
	case "doctor":
		if len(args) != 1 {
			fmt.Fprintln(errOut, "error: doctor does not accept arguments")
			return 2
		}
		err = service.Doctor(out)
	case "status":
		if len(args) != 1 {
			fmt.Fprintln(errOut, "error: status does not accept arguments")
			return 2
		}
		err = service.Status(out)
	case "run":
		if len(args) != 2 {
			fmt.Fprintln(errOut, "error: usage: iro run <issue-number>")
			return 2
		}
		number, parseErr := parseIssueNumber(args[1])
		if parseErr != nil {
			fmt.Fprintln(errOut, "error:", parseErr)
			return 2
		}
		err = service.run(number, out, errOut)
	case "review":
		if len(args) != 2 {
			fmt.Fprintln(errOut, "error: usage: iro review <pr-number>")
			return 2
		}
		number, parseErr := parsePullRequestNumber(args[1])
		if parseErr != nil {
			fmt.Fprintln(errOut, "error:", parseErr)
			return 2
		}
		err = service.Review(number, out)
	case "cleanup":
		if len(args) != 2 {
			fmt.Fprintln(errOut, "error: usage: iro cleanup <issue-number>")
			return 2
		}
		number, parseErr := parseIssueNumber(args[1])
		if parseErr != nil {
			fmt.Fprintln(errOut, "error:", parseErr)
			return 2
		}
		err = service.Cleanup(number, out)
	default:
		fmt.Fprintf(errOut, "error: unknown command %q\n", args[0])
		return 2
	}
	if err != nil {
		fmt.Fprintln(errOut, "error:", err)
		return 1
	}
	return 0
}
