// Package subagent ships the workshop-authored subagent-authoring skill
// as a built-in discoverer so every Workshop binary exposes authoring
// guidance for sub-agents in the system prompt, alongside ore's
// framework-shipped writing-skills.
//
// The discoverer walks the embedded FS rooted at the package directory
// (SKILL.md at the top, references/ as a sibling directory). Adding
// additional skills in this package is permitted — the embedded loader
// walks recursively — but should be motivated by a real need; skills
// are SOP knowledge, not a kitchen sink.
//
// Composition rules (see x/tool/skills/doc.go for the framework's pattern):
//
//   - The app layer composes BuiltInSkills *after* skills.BuiltInSkills
//     and *before* any filesystem discoverers so the framework's
//     writing-skills wins on name collision, this skill wins on
//     collision with user-discovered skills, and user filesystem skills
//     can still add names that don't collide.
//   - If two embedded skills ship with the same `name:` field, the
//     Catalog's first-wins deduplication logs and skips the duplicate;
//     do not rely on order to disambiguate.
package subagent

import (
	"embed"

	"github.com/andrewhowdencom/ore/x/tool/skills"
)

//go:embed SKILL.md references
var builtinFS embed.FS

// BuiltInSkills is the embedded discoverer for workshop-shipped
// sub-agent authoring guidance. The discoverer walks the embedded FS
// at session start and exposes each SKILL.md it finds to the skills
// toolkit.
var BuiltInSkills skills.Discoverer = skills.NewEmbeddedDiscoverer(builtinFS, ".")