package local

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	tfjson "github.com/hashicorp/terraform-json"

	"github.com/raspbeguy/terrain/internal/domain"
)

// Short timeout: a hung binary mustn't keep the run worker alive.
func parsePlanFile(parent context.Context, binary, workDir, planFile string) *domain.PlanResult {
	result := &domain.PlanResult{File: planFile}

	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()

	cmd := hostCommand(ctx, workDir, nil, binary, "show", "-json", planFile)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		result.Err = fmt.Errorf("show -json %s: %w (stderr: %s)", planFile, err, stderr.String())
		return result
	}

	var plan tfjson.Plan
	if err := json.Unmarshal(stdout.Bytes(), &plan); err != nil {
		result.Err = fmt.Errorf("decode plan json: %w", err)
		return result
	}
	result.Parsed = &plan
	return result
}

// Best-effort: binary plan file remains the source of truth.
func persistPlanJSON(runDir string, result *domain.PlanResult) {
	if result == nil || result.Parsed == nil {
		return
	}
	plan, ok := result.Parsed.(*tfjson.Plan)
	if !ok {
		return
	}
	out, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(runDir, "plan.json"), out, 0o644)
}
