// Package role owns the role concept for the workshop: discovery of role
// files on disk, parsing their YAML frontmatter and prompt body, and (in
// a later task) the rendering of role-handoff messages. The package is
// intentionally leaf-ish: it depends only on the ore framework's tool
// sandbox interface and the XDG directory helper, so it can be imported
// by either the app layer (slash handlers) or any other consumer without
// cycle risk.
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

	content := string(data)
	role := &RoleDefinition{Name: name}

	lines := strings.Split(content, "\n")
	if len(lines) > 0 && strings.TrimSpace(lines[0]) == "---" {
		// Find the closing delimiter
		var closeIdx int
		for i := 1; i < len(lines); i++ {
			if strings.TrimSpace(lines[i]) == "---" {
				closeIdx = i
				break
			}
		}
		if closeIdx > 0 {
			frontmatter := strings.Join(lines[1:closeIdx], "\n")
			if err := yaml.Unmarshal([]byte(frontmatter), role); err != nil {
				return nil, fmt.Errorf("parse role frontmatter: %w", err)
			}
			if closeIdx+1 < len(lines) {
				role.Prompt = strings.TrimSpace(strings.Join(lines[closeIdx+1:], "\n"))
			}
			return role, nil
		}
	}

	// No frontmatter or no closing delimiter: entire file is the prompt.
	role.Prompt = strings.TrimSpace(content)
	return role, nil
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
