package app

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

var setTitleSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"title": map[string]any{
			"type":        "string",
			"description": "New conversation title",
		},
	},
	"required": []string{"title"},
}
