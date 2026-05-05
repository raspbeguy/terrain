package remote

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/hashicorp/go-tfe"

	"github.com/raspbeguy/terrain/internal/domain"
)

// TestConnection validates token + org and refines capabilities.
// Organizations.Read distinguishes wrong-org from wrong-endpoint cleanly,
// and the returned CostEstimationEnabled flag seeds caps without another call.
func (b *Backend) TestConnection(ctx context.Context) error {
	if b.organization == "" {
		return errors.New("organization missing")
	}
	org, err := b.client.Organizations.Read(ctx, b.organization)
	if err != nil {
		if errors.Is(err, tfe.ErrResourceNotFound) {
			return fmt.Errorf("organization %q not found at this endpoint", b.organization)
		}
		return fmt.Errorf("connection check failed: %w", err)
	}

	b.probeWithOrg(ctx, org)
	return nil
}

// Probe refreshes capabilities. Best-effort; failures keep the cached bitmask.
func (b *Backend) Probe(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	org, err := b.client.Organizations.Read(ctx, b.organization)
	if err != nil {
		slog.Debug("probe: org read failed, keeping cached caps", "err", err)
		return
	}
	b.probeWithOrg(ctx, org)
}

// probeWithOrg starts from optimistic flavor caps and clears bits per probe.
// Each probe has its own short timeout so slow OTF instances don't stall.
func (b *Backend) probeWithOrg(ctx context.Context, org *tfe.Organization) {
	caps := optimisticCaps(b.flavor)

	if org != nil && !org.CostEstimationEnabled {
		caps &^= domain.CapCostEst
	}

	if !b.endpointAvailable(ctx, func(c context.Context) error {
		_, err := b.client.VariableSets.List(c, b.organization, &tfe.VariableSetListOptions{
			ListOptions: tfe.ListOptions{PageSize: 1},
		})
		return err
	}) {
		caps &^= domain.CapVarSets
	}

	if !b.endpointAvailable(ctx, func(c context.Context) error {
		_, err := b.client.Policies.List(c, b.organization, &tfe.PolicyListOptions{
			ListOptions: tfe.ListOptions{PageSize: 1},
		})
		return err
	}) {
		caps &^= domain.CapPolicy
	}

	b.capsMu.Lock()
	b.caps = caps
	b.capsMu.Unlock()
	slog.Info("remote backend probe complete",
		"backend", b.id, "flavor", b.flavor, "caps", caps)
}

// endpointAvailable: 404 = unavailable; other errors treated as available so a
// flaky probe doesn't permanently disable a feature.
func (b *Backend) endpointAvailable(parent context.Context, fn func(context.Context) error) bool {
	ctx, cancel := context.WithTimeout(parent, 4*time.Second)
	defer cancel()
	err := fn(ctx)
	if err == nil {
		return true
	}
	return !errors.Is(err, tfe.ErrResourceNotFound)
}
