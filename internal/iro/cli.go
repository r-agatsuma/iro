package iro

import (
	"fmt"
	"io"
	"strings"
)

// Execute dispatches the bootstrap MVP CLI commands and returns an exit status.
func Execute(args []string, out, errOut io.Writer, service *Service) int {
	if len(args) == 0 {
		fmt.Fprintln(errOut, "error: command is required (version, init, doctor, status, run <issue-number> [--model <model> | -m <model>] [--reasoning-effort <effort>] [--no-sandbox], review <pr-number> [--model <model> | -m <model>] [--reasoning-effort <effort>] [--no-sandbox], revise <pr-number> [--model <model> | -m <model>] [--reasoning-effort <effort>] [--no-sandbox], land <pr-number>, or cleanup [<issue-number>])")
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
		options, parseErr := parseWorkerOptions(args, "run", "issue-number")
		if parseErr != nil {
			fmt.Fprintln(errOut, "error:", parseErr)
			return 2
		}
		number, parseErr := parseIssueNumber(args[1])
		if parseErr != nil {
			fmt.Fprintln(errOut, "error:", parseErr)
			return 2
		}
		err = service.runWithOptions(number, options, out, errOut)
	case "review":
		options, parseErr := parseWorkerOptions(args, "review", "pr-number")
		if parseErr != nil {
			fmt.Fprintln(errOut, "error:", parseErr)
			return 2
		}
		number, parseErr := parsePullRequestNumber(args[1])
		if parseErr != nil {
			fmt.Fprintln(errOut, "error:", parseErr)
			return 2
		}
		err = service.reviewWithOptions(number, options, out)
	case "revise":
		options, parseErr := parseWorkerOptions(args, "revise", "pr-number")
		if parseErr != nil {
			fmt.Fprintln(errOut, "error:", parseErr)
			return 2
		}
		number, parseErr := parsePullRequestNumber(args[1])
		if parseErr != nil {
			fmt.Fprintln(errOut, "error:", parseErr)
			return 2
		}
		err = service.reviseWithOptions(number, options, out)
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
		if len(args) == 1 {
			err = service.CleanupAll(out)
			break
		}
		if len(args) != 2 {
			fmt.Fprintln(errOut, "error: usage: iro cleanup [<issue-number>]")
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
	options, err := parseWorkerOptions(args, command, operand)
	return options.Model, err
}

type workerOptions struct {
	NoSandbox       bool
	Model           string
	ReasoningEffort string
}

func parseWorkerOptions(args []string, command, operand string) (workerOptions, error) {
	usage := fmt.Sprintf("usage: iro %s <%s> [--model <model> | -m <model>] [--reasoning-effort <effort>] [--no-sandbox]", command, operand)
	if len(args) < 2 {
		return workerOptions{}, fmt.Errorf("%s", usage)
	}

	var options workerOptions
	modelSet := false
	reasoningEffortSet := false
	for i := 2; i < len(args); i++ {
		switch args[i] {
		case "--no-sandbox":
			if options.NoSandbox {
				return workerOptions{}, fmt.Errorf("no-sandbox option may be specified only once")
			}
			options.NoSandbox = true
		case "--model", "-m":
			if modelSet {
				return workerOptions{}, fmt.Errorf("model option may be specified only once")
			}
			if i+1 >= len(args) || args[i+1] == "" || strings.TrimSpace(args[i+1]) == "" || strings.HasPrefix(args[i+1], "-") {
				return workerOptions{}, fmt.Errorf("model must be a non-empty value")
			}
			options.Model = args[i+1]
			modelSet = true
			i++
		case "--reasoning-effort":
			if reasoningEffortSet {
				return workerOptions{}, fmt.Errorf("reasoning effort option may be specified only once")
			}
			if i+1 >= len(args) || args[i+1] == "" || strings.TrimSpace(args[i+1]) == "" || strings.HasPrefix(args[i+1], "-") {
				return workerOptions{}, fmt.Errorf("reasoning effort must be a non-empty value")
			}
			options.ReasoningEffort = args[i+1]
			reasoningEffortSet = true
			i++
		default:
			return workerOptions{}, fmt.Errorf("%s", usage)
		}
	}
	return options, nil
}
