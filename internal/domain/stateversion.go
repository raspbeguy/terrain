package domain

import "time"

type StateVersion struct {
	ID          string
	BackendID   string
	WorkspaceID string
	Serial      int64
	Lineage     string
	CreatedAt   time.Time
	RunID       string
	RawPath     string
	JSONPath    string
	SHA256      string
}
