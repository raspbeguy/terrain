package local

import (
	"context"
	"os/exec"
)

// runCommand wraps hostCommand. Container/bubblewrap modes were dropped.
func runCommand(ctx context.Context, workDir string, extraEnv []string, bin string, args []string) *exec.Cmd {
	return hostCommand(ctx, workDir, extraEnv, bin, args...)
}
