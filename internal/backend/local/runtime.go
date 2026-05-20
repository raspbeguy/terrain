package local

import (
	"context"
	"os/exec"
)

func runCommand(ctx context.Context, workDir string, extraEnv []string, bin string, args []string) *exec.Cmd {
	return hostCommand(ctx, workDir, extraEnv, bin, args...)
}
