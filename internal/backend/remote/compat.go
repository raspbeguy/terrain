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

// TestConnection issues a low-cost API call to verify credentials and
// organization existence, then runs a capability probe against a few known
// endpoints. Returns a non-nil error with a human-readable message — the
// Add Remote Backend dialog surfaces it as a Toast.
//
// We use Organizations.Read because:
//   - it requires authentication (validates the token)
//   - it's cheap (single record fetch)
//   - 404 on it cleanly distinguishes "wrong org" from "wrong endpoint" or
//     "wrong token" failure modes
//
// The org's CostEstimationEnabled flag also seeds the cost-estimation
// capability without an extra round-trip.
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

	// Refine capabilities now that we have a live connection — TestConnection
	// is the natural time, both for the user-triggered Add Remote flow and
	// for the periodic re-probe (M6+).
	b.probeWithOrg(ctx, org)
	return nil
}

// Probe is the public entry point to refresh capabilities outside of the
// TestConnection flow — currently unused, but exposed for a future "Re-probe"
// preferences action. Best-effort; failures keep the previous bitmask.
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

// probeWithOrg refines b.caps from the org payload + a handful of light
// endpoint probes. We start with the optimistic flavor defaults and clear
// bits the API tells us aren't supported. Each probe has its own short
// timeout — a slow OTF instance shouldn't stall the whole flow.
func (b *Backend) probeWithOrg(ctx context.Context, org *tfe.Organization) {
	caps := optimisticCaps(b.flavor)

	// CostEstimation: the org explicitly tells us. This is the cleanest
	// signal — independent of flavor.
	if org != nil && !org.CostEstimationEnabled {
		caps &^= domain.CapCostEst
	}

	// VariableSets: probe the endpoint. OTF versions before varset support
	// return 404; we strip the cap.
	if !b.endpointAvailable(ctx, func(c context.Context) error {
		_, err := b.client.VariableSets.List(c, b.organization, &tfe.VariableSetListOptions{
			ListOptions: tfe.ListOptions{PageSize: 1},
		})
		return err
	}) {
		caps &^= domain.CapVarSets
	}

	// Policies: probe the endpoint. OTF doesn't ship Sentinel/OPA — strip.
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

// endpointAvailable returns true when fn doesn't return ErrResourceNotFound
// or a 404-equivalent. Other errors (auth, network) are treated as
// "available" — we don't want a flaky probe to permanently disable a feature
// the user might have access to.
func (b *Backend) endpointAvailable(parent context.Context, fn func(context.Context) error) bool {
	ctx, cancel := context.WithTimeout(parent, 4*time.Second)
	defer cancel()
	err := fn(ctx)
	if err == nil {
		return true
	}
	return !errors.Is(err, tfe.ErrResourceNotFound)
}
