package local

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	tfjson "github.com/hashicorp/terraform-json"
)

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

type stateBackend interface {
	LoadState(ctx context.Context, workspaceID string) (*tfjson.State, error)
}

var _ stateBackend = (*Backend)(nil)
