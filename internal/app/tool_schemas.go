package app

var listRolesSchema = map[string]any{
	"type": "object",
}

var getCurrentRoleSchema = map[string]any{
	"type": "object",
}

var switchRoleSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"name": map[string]any{
			"type":        "string",
			"description": "Name of the role to activate",
		},
	},
	"required": []string{"name"},
}

var createWorkspaceSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"branch": map[string]any{
			"type":        "string",
			"description": "Name of the new branch to create in the worktree",
		},
		"base_branch": map[string]any{
			"type":        "string",
			"description": "Optional base branch to create the new branch from",
		},
	},
	"required": []string{"branch"},
}

var destroyWorkspaceSchema = map[string]any{
	"type": "object",
}

var gitCommitSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"title": map[string]any{
			"type":        "string",
			"description": "Required commit title (first line)",
		},
		"message": map[string]any{
			"type":        "string",
			"description": "Optional commit body",
		},
	},
	"required": []string{"title"},
}
