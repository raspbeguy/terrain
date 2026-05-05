package domain

import "time"

// VariableCategory mirrors TFE: Terraform vars become -var / tfvars
// arguments; Environment vars are exported into the subprocess env.
type VariableCategory string

const (
	VarCategoryTerraform   VariableCategory = "terraform"
	VarCategoryEnvironment VariableCategory = "env"
)

// Variable is one key/value pair on a workspace or variable set.
// Sensitive values are stored in the keyring; Value is empty in memory
// unless the user just edited it.
type Variable struct {
	Key      string
	Value    string
	Category VariableCategory
	// HCL marks Value as an HCL expression (e.g. `["a", "b"]`) rather
	// than a plain string.
	HCL         bool
	Sensitive   bool
	Description string
	// Declared is true when the variable has a `variable "<name>" {}`
	// block in the workspace's .tf source. tfvars-only entries (or
	// remote backends, where source isn't visible) report false; the UI
	// flags those with an "ad-hoc" badge.
	Declared bool
}

// VariableScope orders the precedence of variable sets:
// global < project < workspace < workspace .tfvars < CLI.
type VariableScope string

const (
	ScopeGlobal    VariableScope = "global"
	ScopeProject   VariableScope = "project"
	ScopeWorkspace VariableScope = "workspace"
)

// VariableSet is a reusable named collection of variables attachable to
// workspaces. Modelled after TFE's varset concept.
type VariableSet struct {
	ID          string
	BackendID   string
	Name        string
	Description string
	// Workspaces is the explicit attachment list for ScopeWorkspace;
	// ProjectID is set for ScopeProject; both are empty for global.
	Scope      VariableScope
	Workspaces []string
	ProjectID  string
	// Variables is parsed lazily; empty here doesn't guarantee the set
	// is empty (use Backend.VariableSetVars to fetch).
	Variables []Variable
	// Priority breaks ties between sets at the same scope (higher wins).
	Priority  int
	CreatedAt time.Time
	UpdatedAt time.Time
}
