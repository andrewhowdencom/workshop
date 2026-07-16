package subagent

import (
	"context"
	"strings"
	"testing"

	"github.com/andrewhowdencom/ore/x/tool/skills"
)

// TestBuiltInSkill_SurfacesInCatalog confirms that the embedded discoverer
// (BuiltInSkills) walks the embedded skills/ tree and surfaces
// subagent-authoring with the expected name and description. Mirrors the
// framework's own TestBuiltInSkills_LoadsPlaceholder for ore/x/tool/skills.
func TestBuiltInSkill_SurfacesInCatalog(t *testing.T) {
	metas, err := BuiltInSkills.Discover(context.Background())
	if err != nil {
		t.Fatalf("BuiltInSkills.Discover error: %v", err)
	}

	var found *skills.SkillMeta
	for i := range metas {
		if metas[i].Name == "subagent-authoring" {
			found = &metas[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("subagent-authoring skill not found in BuiltInSkills catalog; got %d entries", len(metas))
	}
	if found.Description == "" {
		t.Error("subagent-authoring description is empty")
	}
	if !strings.Contains(found.Description, "sub-agents") {
		t.Errorf("description does not mention sub-agents: %q", found.Description)
	}
}

// TestBuiltInSkill_ReadCanonicalReturnsBody confirms that read_skill on
// the skill returns the SKILL.md body with the expected frontmatter.
func TestBuiltInSkill_ReadCanonicalReturnsBody(t *testing.T) {
	body, err := BuiltInSkills.Read(context.Background(), "subagent-authoring", "")
	if err != nil {
		t.Fatalf("BuiltInSkills.Read error: %v", err)
	}

	wants := []string{
		"name: subagent-authoring",
		"description:",
		"ReAct",
		"agent.WithState",
		"collides with already-registered tool",
		"Definition of Done",
		"Review Path",
	}
	for _, w := range wants {
		if !strings.Contains(body, w) {
			t.Errorf("SKILL.md body missing %q", w)
		}
	}
}

// TestBuiltInSkill_ReferencesResolve confirms both reference files
// (referenced from SKILL.md) are present and readable through the same
// read_skill seam. Catches a missing file in skills/subagent-authoring/
// references/ at build time.
func TestBuiltInSkill_ReferencesResolve(t *testing.T) {
	for _, ref := range []string{
		"references/result-schema.md",
		"references/authoring-checklist.md",
	} {
		body, err := BuiltInSkills.Read(context.Background(), "subagent-authoring", ref)
		if err != nil {
			t.Errorf("BuiltInSkills.Read(%q) error: %v", ref, err)
			continue
		}
		if body == "" {
			t.Errorf("reference %q returned empty body", ref)
		}
	}
}