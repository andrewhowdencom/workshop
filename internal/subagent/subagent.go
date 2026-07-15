// Package subagent owns the sub-agent concept for workshop: discovery of
// sub-agent files on disk and parsing their YAML frontmatter and prompt
// body. The package is intentionally leaf-ish: it depends only on the ore
// framework's tool sandbox interface and the XDG directory helper, so it
// can be imported by either the app layer (the stepFactory closure) or
// any other consumer without cycle risk.
//
// # Construction parallel with roles
//
// Sub-agents are the "tool invocation" counterpart to roles'
// "system-prompt injection". The two packages share identical discovery
// and parsing surface (the body is free-form markdown in both cases);
// they differ in how their content is wired into the agent:
//
//   - role.RoleDefinition is injected into a system-prompt transform
//     on the parent agent. The user picks the role with /role.
//   - subagent.SubagentDefinition is wrapped in a (tool.Tool,
//     tool.ToolFunc) pair via x/subagent.AsTool and registered into
//     the parent's tool.Registry. The LLM picks the sub-agent by
//     emitting a tool call.
//
// # Sub-agents are domain specialists, not restricted capability delegates
//
// A sub-agent's power is bounded by its own spec (the markdown body),
// not by inheritance from the parent's tool set. At v1 the sub-agent
// inherits the parent's full tool registry (x/subagent's
// fresh-per-call factory is wired against the same tool pairs), but
// that is a *capability alignment* choice — the specialist gets the
// same primitives the parent has — not a *safety* choice. Future
// frontmatter fields (pattern, model, tools:) are scoped at the
// sub-agent's own definition, not inferred from the parent.
package subagent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/adrg/xdg"
	"github.com/andrewhowdencom/ore/tool"
	"gopkg.in/yaml.v3"
)

// SubagentDefinition holds a parsed sub-agent file with YAML frontmatter
// and prompt body. The Name is always derived from the filename (basename
// without .md); the Description is read from the YAML frontmatter and
// becomes the tool description when registered with x/subagent.AsTool;
// and the Prompt is the markdown body, which becomes the sub-agent's
// spec / system prompt (augmented by x/subagent.ResultSystemPrompt to
// instruct JSON output conforming to ResultSchema).
type SubagentDefinition struct {
	Name        string `yaml:"-"`
	Description string `yaml:"description"`
	Prompt      string
}

// Dir returns the XDG data directory for workshop sub-agents. The
// directory is not auto-created; callers should treat a non-existent
// directory as "no sub-agents available" (see ListSubagentDefinitions,
// which returns an empty slice for missing dirs).
func Dir() string {
	return filepath.Join(xdg.DataHome, "workshop", "subagents")
}

// ExtractBody returns the content of a sub-agent file with any leading
// YAML frontmatter stripped. The frontmatter is delimited by "---" lines
// at the very start of the file. If the file does not start with "---",
// or has no closing "---", the entire content is returned as the body.
//
// ExtractBody is the canonical frontmatter-parsing primitive for
// sub-agent files. LoadSubagent and LoadBody both delegate to it.
func ExtractBody(content string) (body, frontmatter string) {
	lines := strings.Split(content, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return strings.TrimSpace(content), ""
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			fm := strings.TrimSpace(strings.Join(lines[1:i], "\n"))
			bd := ""
			if i+1 < len(lines) {
				bd = strings.TrimSpace(strings.Join(lines[i+1:], "\n"))
			}
			return bd, fm
		}
	}
	// No closing delimiter: treat the whole content as the body.
	return strings.TrimSpace(content), ""
}

// LoadSubagent reads a sub-agent definition from <dir>/<name>.md.
// If the file starts with "---" on its own line, YAML frontmatter
// between the first and second "---" delimiters is parsed; everything
// after the second "---" is the prompt body. The name is taken from
// the function's name argument (which becomes the file's basename
// minus the .md extension); the YAML `name:` key, if present, is
// ignored — the loader deliberately trusts the filename over the
// frontmatter to keep the on-disk↔registered mapping deterministic.
//
// The sandbox is used for path resolution when a FileSandbox is
// available.
func LoadSubagent(dir, name string, sb tool.Sandbox) (*SubagentDefinition, error) {
	path := filepath.Join(dir, name+".md")
	if fsb, ok := sb.(tool.FileSandbox); ok {
		var err error
		path, err = fsb.ResolvePath(path)
		if err != nil {
			return nil, fmt.Errorf("resolve path: %w", err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read subagent file: %w", err)
	}

	body, frontmatter := ExtractBody(string(data))
	sa := &SubagentDefinition{Name: name, Prompt: body}
	if frontmatter != "" {
		if err := yaml.Unmarshal([]byte(frontmatter), sa); err != nil {
			return nil, fmt.Errorf("parse subagent frontmatter: %w", err)
		}
	}
	return sa, nil
}

// LoadBody reads the sub-agent file at the given path and returns its
// prompt body — the file content with any leading YAML frontmatter
// stripped. This is a path-based convenience over LoadSubagent for
// callers that already hold a path and do not need the parsed
// frontmatter fields.
func LoadBody(path string, sb tool.Sandbox) (string, error) {
	if fsb, ok := sb.(tool.FileSandbox); ok {
		var err error
		path, err = fsb.ResolvePath(path)
		if err != nil {
			return "", fmt.Errorf("resolve path: %w", err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read subagent file: %w", err)
	}
	body, _ := ExtractBody(string(data))
	return body, nil
}

// ListSubagentDefinitions scans dir for *.md files and loads each
// sub-agent definition. Returns an empty slice if the directory does
// not exist. Files that fail to load are skipped silently so that one
// malformed sub-agent does not block discovery of the others. The
// sandbox is used for path resolution when a FileSandbox is available.
func ListSubagentDefinitions(dir string, sb tool.Sandbox) ([]SubagentDefinition, error) {
	if fsb, ok := sb.(tool.FileSandbox); ok {
		var err error
		dir, err = fsb.ResolvePath(dir)
		if err != nil {
			return nil, fmt.Errorf("resolve subagents directory: %w", err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []SubagentDefinition{}, nil
		}
		return nil, fmt.Errorf("read subagents directory: %w", err)
	}

	var subs []SubagentDefinition
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		fname := entry.Name()
		if !strings.HasSuffix(fname, ".md") {
			continue
		}
		saName := strings.TrimSuffix(fname, ".md")
		sa, err := LoadSubagent(dir, saName, sb)
		if err != nil {
			continue
		}
		subs = append(subs, *sa)
	}

	return subs, nil
}
