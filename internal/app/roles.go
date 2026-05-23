package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/adrg/xdg"
	"go.yaml.in/yaml/v3"
)

// RoleDefinition holds a parsed role file with YAML frontmatter and prompt body.
type RoleDefinition struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Prompt      string
}

// roleDir returns the XDG data directory for workshop roles.
func roleDir() string {
	return filepath.Join(xdg.DataHome, "workshop", "roles")
}

// loadRole reads a role definition from <dir>/<name>.md.
// If the file starts with "---" on its own line, YAML frontmatter between the
// first and second "---" delimiters is parsed; everything after the second
// "---" is the prompt body.
func loadRole(dir, name string) (*RoleDefinition, error) {
	path := filepath.Join(dir, name+".md")
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

// listRoleDefinitions scans dir for *.md files and loads each role definition.
// Returns an empty slice if the directory does not exist. Files that fail to
// load are skipped silently so that one malformed role does not block
// discovery of the others.
func listRoleDefinitions(dir string) ([]RoleDefinition, error) {
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
		role, err := loadRole(dir, roleName)
		if err != nil {
			continue // skip malformed files silently
		}
		roles = append(roles, *role)
	}

	return roles, nil
}
