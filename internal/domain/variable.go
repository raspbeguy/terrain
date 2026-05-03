package domain

import "time"

// VariableCategory mirrors TFE's distinction: Terraform variables are passed
// as -var arguments / .tfvars; Environment variables are exported into the
// subprocess env (e.g. AWS_ACCESS_KEY_ID, TF_LOG).
type VariableCategory string

const (
	VarCategoryTerraform   VariableCategory = "terraform"
	VarCategoryEnvironment VariableCategory = "env"
)

// Variable is a single key/value pair declared on a workspace or a variable
// set. Sensitive values are masked in display and stored in the system
// keyring rather than plaintext on disk; the on-disk value is a sentinel
// the runner resolves at run-materialization time.
type Variable struct {
	// Key is the variable name (e.g. "instance_count"). Required.
	Key string

	// Value is the literal value. For HCL=true variables this is an HCL
	// expression; otherwise it's a plain string. For sensitive variables
	// this field is empty in memory unless the user just edited it; the
	// real value lives in the keyring.
	Value string

	// Category routes the variable: Terraform → -var; Environment → env.
	Category VariableCategory

	// HCL marks the value as an HCL expression (e.g. `["a", "b"]`) rather
	// than a plain string. TFE-style.
	HCL bool

	// Sensitive hides the value from the UI and routes storage to the
	// keyring. Once sensitive, the plaintext is removed from disk.
	Sensitive bool

	// Description is free-form documentation shown next to the row.
	Description string

	// Declared marks variables that have a `variable "<name>" {}` block in
	// the workspace's .tf source — i.e. part of the module's declared
	// interface. Variables that exist only as terrain-managed entries in
	// terraform.tfvars or in the keyring (no source declaration) report
	// Declared=false; the UI surfaces this as an "ad-hoc" badge so the
	// user can spot stale or accidental entries that terraform itself
	// would warn about. Remote (TFE/HCP/OTF) backends always report false
	// — source-level declarations aren't visible from the API.
	Declared bool
}

// VariableScope names where a variable set applies — global (every workspace
// in the registry), project (one local project, all its workspaces), or
// workspace (a specific workspace). The runner respects TFE's precedence:
// global < project < workspace < workspace .tfvars < CLI overrides.
type VariableScope string

const (
	ScopeGlobal    VariableScope = "global"
	ScopeProject   VariableScope = "project"
	ScopeWorkspace VariableScope = "workspace"
)

// VariableSet is a reusable named collection of variables that can be
// attached to one or more workspaces. Modeled directly after TFE's varset
// concept; for local backends the GUI manages set files on disk.
type VariableSet struct {
	// ID is unique within its Backend.
	ID string

	// BackendID is the owning Backend.
	BackendID string

	// Name is the user-facing label.
	Name string

	// Description is free-form.
	Description string

	// Scope determines where this set applies. Workspaces is the explicit
	// list of workspace IDs the set is attached to (used for ScopeWorkspace
	// scope; empty for global; project ID for ScopeProject).
	Scope      VariableScope
	Workspaces []string
	ProjectID  string

	// Variables is the set's contents. Parsed lazily; an empty list here
	// doesn't necessarily mean the set is empty (use Backend.VariableSetVars
	// to fetch).
	Variables []Variable

	// Priority resolves ties between two sets at the same scope. Higher
	// wins. TFE doesn't expose this directly but local backends use it.
	Priority int

	CreatedAt time.Time
	UpdatedAt time.Time
}
