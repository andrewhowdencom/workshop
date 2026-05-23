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
