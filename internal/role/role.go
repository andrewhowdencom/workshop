// Package role owns the role concept for the workshop: discovery of role
// files on disk and parsing their YAML frontmatter and prompt body. The
// package is intentionally leaf-ish: it depends only on the ore
// framework's tool sandbox interface and the XDG directory helper, so it
// can be imported by either the app layer (slash handlers) or any other
// consumer without cycle risk.
package role

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/adrg/xdg"
	"github.com/andrewhowdencom/ore/tool"
	"gopkg.in/yaml.v3"
)

// RoleDefinition holds a parsed role file with YAML frontmatter and prompt body.
type RoleDefinition struct {
	Name        string `yaml:"-"`
	Description string `yaml:"description"`
	Prompt      string
}

// Dir returns the XDG data directory for workshop roles.
func Dir() string {
	return filepath.Join(xdg.DataHome, "workshop", "roles")
}

// ExtractBody returns the content of a role file with any leading YAML
// frontmatter stripped. The frontmatter is delimited by "---" lines at
// the very start of the file. If the file does not start with "---", or
// has no closing "---", the entire content is returned as the body.
//
// ExtractBody is the canonical frontmatter-parsing primitive for role
// files. LoadRole and LoadBody both delegate to it.
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

// LoadRole reads a role definition from <dir>/<name>.md.
// If the file starts with "---" on its own line, YAML frontmatter between
// the first and second "---" delimiters is parsed; everything after the
// second "---" is the prompt body.
// The sandbox is used for path resolution when a FileSandbox is available.
func LoadRole(dir, name string, sb tool.Sandbox) (*RoleDefinition, error) {
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
		return nil, fmt.Errorf("read role file: %w", err)
	}

	body, frontmatter := ExtractBody(string(data))
	role := &RoleDefinition{Name: name, Prompt: body}
	if frontmatter != "" {
		if err := yaml.Unmarshal([]byte(frontmatter), role); err != nil {
			return nil, fmt.Errorf("parse role frontmatter: %w", err)
		}
	}
	return role, nil
}

// LoadBody reads the role file at the given path and returns its prompt
// body — the file content with any leading YAML frontmatter stripped.
// This is a path-based convenience over LoadRole for callers that
// already hold a path (for example, from a source.FileResolver that
// tracks the active role's file location) and do not need the parsed
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
		return "", fmt.Errorf("read role file: %w", err)
	}
	body, _ := ExtractBody(string(data))
	return body, nil
}

// ListRoleDefinitions scans dir for *.md files and loads each role definition.
// Returns an empty slice if the directory does not exist. Files that fail to
// load are skipped silently so that one malformed role does not block
// discovery of the others.
// The sandbox is used for path resolution when a FileSandbox is available.
func ListRoleDefinitions(dir string, sb tool.Sandbox) ([]RoleDefinition, error) {
	if fsb, ok := sb.(tool.FileSandbox); ok {
		var err error
		dir, err = fsb.ResolvePath(dir)
		if err != nil {
			return nil, fmt.Errorf("resolve roles directory: %w", err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []RoleDefinition{}, nil
		}
		return nil, fmt.Errorf("read roles directory: %w", err)
	}

	var roles []RoleDefinition
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		fname := entry.Name()
		if !strings.HasSuffix(fname, ".md") {
			continue
		}
		roleName := strings.TrimSuffix(fname, ".md")
		role, err := LoadRole(dir, roleName, sb)
		if err != nil {
			continue // skip malformed files silently
		}
		roles = append(roles, *role)
	}

	return roles, nil
}
