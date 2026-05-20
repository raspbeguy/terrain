package domain

import "time"

type VariableCategory string

const (
	VarCategoryTerraform   VariableCategory = "terraform"
	VarCategoryEnvironment VariableCategory = "env"
)

type Variable struct {
	Key         string
	Value       string
	Category    VariableCategory
	HCL         bool
	Sensitive   bool
	Description string
	Declared    bool
}

// Precedence: global < project < workspace < workspace .tfvars < CLI.
type VariableScope string

const (
	ScopeGlobal    VariableScope = "global"
	ScopeProject   VariableScope = "project"
	ScopeWorkspace VariableScope = "workspace"
)

type VariableSet struct {
	ID          string
	BackendID   string
	Name        string
	Description string
	Scope       VariableScope
	Workspaces  []string
	ProjectID   string
	Variables   []Variable
	Priority    int
	CreatedAt   time.Time
	UpdatedAt   time.Time
}
