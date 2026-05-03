package local

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	tfjson "github.com/hashicorp/terraform-json"

	"github.com/raspbeguy/terrain/internal/domain"
)

// LoadState runs `<binary> show -json` (no plan file) against the workspace's
// working directory and decodes the result into *tfjson.State. Used by the
// State tab in the workspace view; M3 ships this via a direct method on
// Backend rather than baked into the StartRun stream so the UI can refresh
// state without triggering a run.
//
// The Backend interface itself stays minimal — this is a method on the
// local backend that the UI uses by type-asserting. M4 adds the equivalent
// for remote backends via go-tfe's StateVersions.Read.
func (b *Backend) LoadState(parent context.Context, workspaceID string) (*tfjson.State, error) {
	wsCtx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	ws, err := b.Workspace(wsCtx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace: %w", err)
	}

	bin, err := DetectBinary()
	if err != nil {
		return nil, fmt.Errorf("detect tofu/terraform: %w", err)
	}

	ctx, cancel2 := context.WithTimeout(parent, 30*time.Second)
	defer cancel2()
	cmd := hostCommand(ctx, ws.WorkingDirectory, nil, bin.Path, "show", "-json")

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s show -json (in %s): %w (stderr: %s)",
			bin.Name, ws.WorkingDirectory, err, stderr.String())
	}

	var state tfjson.State
	if err := json.Unmarshal(stdout.Bytes(), &state); err != nil {
		return nil, fmt.Errorf("decode state json: %w", err)
	}
	return &state, nil
}

// stateBackend is the optional capability extension we'd promote to the
// domain.Backend interface in M4 once both local and remote support it.
// For M3, callers type-assert to this:
type stateBackend interface {
	LoadState(ctx context.Context, workspaceID string) (*tfjson.State, error)
}

// ensure local.Backend satisfies the local-only capability.
var _ stateBackend = (*Backend)(nil)

// hint to keep domain import used (avoids "imported and not used" if state.go
// is the only file that doesn't reference it).
var _ = domain.StatusPending
