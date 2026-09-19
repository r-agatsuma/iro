package iro

import (
	"bufio"
	"fmt"
	"strconv"
	"strings"
)

const workflowTemplate = `# WORKFLOW.md

この file は repository / workload 固有の operation policy を記述する場所です。
Human が対象と許可範囲を具体化してください。見出しは記述例であり、iro が parse する必須 schema ではありません。

## Workload / target

- 作業対象、目的、対象環境を記述してください。

## Allowed operations

- Author は Issue scope 内の working tree file を編集できます。
- external workload operation を許可する場合は、対象と操作の種類・範囲を明示してください。
- external workload operation は Issue scope とこの policy の明示的な許可の両方を必要とします。Issue だけでは許可になりません。

## Prohibited operations

- Issue scope 外の操作を行わないでください。
- iro が所有する Git / GitHub delivery lifecycle を worker に許可することはできません。
- Reviewer は read-only inspection / validation に限り、implementation fix や external workload mutation を行いません。

## Access / tool usage

- 利用可能な tool、接続方法、接続先、利用上の制約を記述してください。

## Credential references

- 既存の環境変数、SSH agent、実行環境の認証など、credential の参照方法だけを記述してください。
- secret value 自体をこの file や repository に保存しないでください。

## Validation

- workload に適した検証方法、実行条件、成功基準を記述してください。

## Reporting requirements

- 報告言語、必要な結果・検証 evidence・制約の記載方法を指定してください。
`

const configTemplate = `version = 1

[tracker]
type = "github"
remote = "origin"

[agent]
type = "codex"

[workspace]
strategy = "git-worktree"
`

// Config is the supported iro.toml subset for the bootstrap MVP.
type Config struct {
	Version           int
	TrackerType       string
	TrackerRemote     string
	AgentType         string
	WorkspaceStrategy string
}

func (c Config) Validate() error {
	if c.Version != 1 {
		return fmt.Errorf("unsupported config version %d; supported version is 1", c.Version)
	}
	if c.TrackerType != "github" {
		return fmt.Errorf("unsupported tracker.type %q; supported value is github", c.TrackerType)
	}
	if strings.TrimSpace(c.TrackerRemote) == "" {
		return fmt.Errorf("tracker.remote must be non-empty")
	}
	if c.AgentType != "codex" {
		return fmt.Errorf("unsupported agent.type %q; supported value is codex", c.AgentType)
	}
	if c.WorkspaceStrategy != "git-worktree" {
		return fmt.Errorf("unsupported workspace.strategy %q; supported value is git-worktree", c.WorkspaceStrategy)
	}
	return nil
}

func parseConfig(data []byte) (Config, error) {
	values := make(map[string]string)
	section := ""
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(stripTOMLComment(scanner.Text()))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") {
			if !strings.HasSuffix(line, "]") {
				return Config{}, fmt.Errorf("line %d: invalid table header", lineNumber)
			}
			section = strings.TrimSpace(line[1 : len(line)-1])
			if section != "" && section != "tracker" && section != "agent" && section != "workspace" {
				return Config{}, fmt.Errorf("line %d: unsupported table %q", lineNumber, section)
			}
			continue
		}

		key, rawValue, ok := splitTOMLAssignment(line)
		if !ok {
			return Config{}, fmt.Errorf("line %d: expected key = value", lineNumber)
		}
		key = strings.TrimSpace(key)
		if key == "" || strings.ContainsAny(key, " \t") {
			return Config{}, fmt.Errorf("line %d: invalid key", lineNumber)
		}
		fullKey := key
		if section != "" {
			fullKey = section + "." + key
		}
		if _, exists := values[fullKey]; exists {
			return Config{}, fmt.Errorf("line %d: duplicate key %q", lineNumber, fullKey)
		}
		if !supportedConfigKey(fullKey) {
			return Config{}, fmt.Errorf("line %d: unsupported key %q", lineNumber, fullKey)
		}
		rawValue = strings.TrimSpace(rawValue)
		if fullKey == "version" {
			if strings.HasPrefix(rawValue, `"`) || strings.HasPrefix(rawValue, "'") {
				return Config{}, fmt.Errorf("line %d: version must be an integer", lineNumber)
			}
			if _, err := strconv.Atoi(rawValue); err != nil {
				return Config{}, fmt.Errorf("line %d: version must be an integer", lineNumber)
			}
			values[fullKey] = rawValue
			continue
		}
		if !strings.HasPrefix(rawValue, `"`) && !strings.HasPrefix(rawValue, "'") {
			return Config{}, fmt.Errorf("line %d: value for %q must be a string", lineNumber, fullKey)
		}
		value, err := parseTOMLValue(rawValue)
		if err != nil {
			return Config{}, fmt.Errorf("line %d: %s", lineNumber, err)
		}
		values[fullKey] = value
	}
	if err := scanner.Err(); err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}

	required := []string{"version", "tracker.type", "tracker.remote", "agent.type", "workspace.strategy"}
	for _, key := range required {
		if _, ok := values[key]; !ok {
			return Config{}, fmt.Errorf("missing required key %q", key)
		}
	}
	version, err := strconv.Atoi(values["version"])
	if err != nil {
		return Config{}, fmt.Errorf("version must be an integer")
	}
	config := Config{
		Version:           version,
		TrackerType:       values["tracker.type"],
		TrackerRemote:     values["tracker.remote"],
		AgentType:         values["agent.type"],
		WorkspaceStrategy: values["workspace.strategy"],
	}
	if err := config.Validate(); err != nil {
		return Config{}, err
	}
	return config, nil
}

func supportedConfigKey(key string) bool {
	switch key {
	case "version", "tracker.type", "tracker.remote", "agent.type", "workspace.strategy":
		return true
	default:
		return false
	}
}

func parseTOMLValue(value string) (string, error) {
	if value == "" {
		return "", fmt.Errorf("value is empty")
	}
	if strings.HasPrefix(value, `"`) {
		if !strings.HasSuffix(value, `"`) || len(value) == 1 {
			return "", fmt.Errorf("invalid basic string")
		}
		parsed, err := strconv.Unquote(value)
		if err != nil {
			return "", fmt.Errorf("invalid basic string: %w", err)
		}
		return parsed, nil
	}
	if strings.HasPrefix(value, "'") {
		if len(value) < 2 || !strings.HasSuffix(value, "'") {
			return "", fmt.Errorf("invalid literal string")
		}
		return value[1 : len(value)-1], nil
	}
	return value, nil
}

func stripTOMLComment(line string) string {
	inBasic := false
	inLiteral := false
	escaped := false
	for i, r := range line {
		if inBasic {
			if escaped {
				escaped = false
				continue
			}
			if r == '\\' {
				escaped = true
			} else if r == '"' {
				inBasic = false
			}
			continue
		}
		if inLiteral {
			if r == '\'' {
				inLiteral = false
			}
			continue
		}
		switch r {
		case '"':
			inBasic = true
		case '\'':
			inLiteral = true
		case '#':
			return line[:i]
		}
	}
	return line
}

func splitTOMLAssignment(line string) (string, string, bool) {
	inBasic := false
	inLiteral := false
	escaped := false
	for i, r := range line {
		if inBasic {
			if escaped {
				escaped = false
				continue
			}
			if r == '\\' {
				escaped = true
			} else if r == '"' {
				inBasic = false
			}
			continue
		}
		if inLiteral {
			if r == '\'' {
				inLiteral = false
			}
			continue
		}
		switch r {
		case '"':
			inBasic = true
		case '\'':
			inLiteral = true
		case '=':
			return line[:i], line[i+1:], true
		}
	}
	return "", "", false
}
