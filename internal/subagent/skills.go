// Package subagent ships workshop-authored skills as a built-in
// discoverer so every Workshop binary surfaces authoring guidance
// alongside ore's framework-shipped writing-skills.
//
// Skills in this package live under the embedded skills/ directory,
// one subdirectory per skill (each containing a SKILL.md plus an
// optional references/ tree). The embedded loader walks recursively
// and registers every well-formed SKILL.md it finds, so adding
// additional skills is a directory-creation operation.
//
// Composition rules (see x/tool/skills/doc.go for the framework's pattern):
//
//   - The app layer composes BuiltInSkills *after* skills.BuiltInSkills
//     and *before* any filesystem discoverers so the framework's
//     writing-skills wins on name collision, this package's skills win
//     on collision with user-discovered skills, and user filesystem
//     skills can still add names that don't collide.
//   - If two embedded skills ship with the same `name:` field, the
//     Catalog's first-wins deduplication logs and skips the duplicate;
//     do not rely on order to disambiguate.
package subagent

import (
	"embed"

	"github.com/andrewhowdencom/ore/x/tool/skills"
)

//go:embed all:skills
var skillFS embed.FS

// BuiltInSkills is the embedded discoverer for workshop-shipped
// sub-agent skills. The discoverer walks the embedded skills/ tree
// at session start and exposes each SKILL.md it finds to the skills
// toolkit.
var BuiltInSkills skills.Discoverer = skills.NewEmbeddedDiscoverer(skillFS, "skills")