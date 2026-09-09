package iro

import (
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"strings"
)

func knownValue(value string) string {
	if strings.TrimSpace(value) == "" {
		return "unknown"
	}
	return value
}

func writeBuildInfo(out io.Writer, info *debug.BuildInfo, ok bool) {
	version, revision, vcsTime, modified, goVersion := "", "", "", "", ""
	if ok && info != nil {
		version, goVersion = info.Main.Version, info.GoVersion
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				revision = setting.Value
			case "vcs.time":
				vcsTime = setting.Value
			case "vcs.modified":
				modified = setting.Value
			}
		}
	}
	fmt.Fprintf(out, "iro\nversion: %s\nrevision: %s\nvcs-time: %s\nmodified: %s\ngo: %s\n",
		knownValue(version), knownValue(revision), knownValue(vcsTime), knownValue(modified), knownValue(goVersion))
}

func writeVersion(out io.Writer) {
	info, ok := debug.ReadBuildInfo()
	writeBuildInfo(out, info, ok)
}

func writeExecutablePath(out io.Writer, executable func() (string, error)) {
	path, err := executable()
	if err != nil {
		path = ""
	}
	fmt.Fprintf(out, "iro executable path: %s\n", knownValue(path))
}

func (s *Service) toolDiagnostics(out io.Writer, name string) {
	path, err := s.Runner.LookPath(name)
	version := ""
	if err != nil {
		path = ""
	} else {
		result := s.Runner.Run(CommandSpec{Name: name, Args: []string{"--version"}})
		if commandSucceeded(result) {
			version = strings.TrimSpace(strings.SplitN(result.Stdout, "\n", 2)[0])
		}
	}
	fmt.Fprintf(out, "%s executable path: %s\n%s version: %s\n", name, knownValue(path), name, knownValue(version))
}

func writeSelfDiagnostics(out io.Writer) {
	writeVersion(out)
	writeExecutablePath(out, os.Executable)
}
