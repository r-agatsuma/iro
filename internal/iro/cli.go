package iro

import (
	"fmt"
	"io"
	"strings"
)

// Execute dispatches the bootstrap MVP CLI commands and returns an exit status.
func Execute(args []string, out, errOut io.Writer, service *Service) int {
	if len(args) == 0 {
		fmt.Fprintln(errOut, "error: command is required (version, init, doctor, status, run <issue-number> [--model <model> | -m <model>], review <pr-number> [--model <model> | -m <model>], revise <pr-number> [--model <model> | -m <model>], land <pr-number>, or cleanup <issue-number>)")
		return 2
	}

	var err error
	switch args[0] {
	case "version":
		if len(args) != 1 {
			fmt.Fprintln(errOut, "error: version does not accept arguments")
			return 2
		}
		writeVersion(out)
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
		model, parseErr := parseModelOverride(args, "run", "issue-number")
		if parseErr != nil {
			fmt.Fprintln(errOut, "error:", parseErr)
			return 2
		}
		number, parseErr := parseIssueNumber(args[1])
		if parseErr != nil {
			fmt.Fprintln(errOut, "error:", parseErr)
			return 2
		}
		err = service.runWithModel(number, model, out, errOut)
	case "review":
		model, parseErr := parseModelOverride(args, "review", "pr-number")
		if parseErr != nil {
			fmt.Fprintln(errOut, "error:", parseErr)
			return 2
		}
		number, parseErr := parsePullRequestNumber(args[1])
		if parseErr != nil {
			fmt.Fprintln(errOut, "error:", parseErr)
			return 2
		}
		err = service.reviewWithModel(number, model, out)
	case "revise":
		model, parseErr := parseModelOverride(args, "revise", "pr-number")
		if parseErr != nil {
			fmt.Fprintln(errOut, "error:", parseErr)
			return 2
		}
		number, parseErr := parsePullRequestNumber(args[1])
		if parseErr != nil {
			fmt.Fprintln(errOut, "error:", parseErr)
			return 2
		}
		err = service.reviseWithModel(number, model, out)
	case "land":
		if len(args) != 2 {
			fmt.Fprintln(errOut, "error: usage: iro land <pr-number>")
			return 2
		}
		number, parseErr := parsePullRequestNumber(args[1])
		if parseErr != nil {
			fmt.Fprintln(errOut, "error:", parseErr)
			return 2
		}
		err = service.Land(number, out)
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

func parseModelOverride(args []string, command, operand string) (string, error) {
	usage := fmt.Sprintf("usage: iro %s <%s> [--model <model> | -m <model>]", command, operand)
	if len(args) == 2 {
		return "", nil
	}
	if len(args) != 4 || args[2] != "--model" && args[2] != "-m" {
		return "", fmt.Errorf("%s", usage)
	}
	if args[3] == "" || strings.TrimSpace(args[3]) == "" || strings.HasPrefix(args[3], "-") {
		return "", fmt.Errorf("model must be a non-empty value")
	}
	return args[3], nil
}
