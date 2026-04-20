// Package digests generates weekly cross-issue digest snapshots (Phase 3
// #11). Each run, for each installation, selects up to `topN` interesting
// findings from the past 7 days and stores them as a digest row.
//
// Zero-retention: the digest row stores only finding ids. Message text is
// rendered by the catalog at read time via the digest handler.
package digests

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/ethicguard/ethicguard-api/internal/store"
)

const (
	digestPeriod = 7 * 24 * time.Hour
	topN         = 10
)

// Generator is the single-instance runner used by the scheduler.
type Generator struct {
	Logger  *slog.Logger
	Digests *store.Digests
	Audit   *store.Audit
}

// GenerateForAll iterates every installation and produces one digest row
// each. Errors on individual installations are logged but do not fail the
// run — one bad tenant shouldn't starve the others.
func (g *Generator) GenerateForAll(ctx context.Context, now time.Time) error {
	if g.Digests == nil {
		return fmt.Errorf("digests: repo not set")
	}
	ids, err := g.Digests.ListInstallationIDs(ctx)
	if err != nil {
		return fmt.Errorf("digests list installations: %w", err)
	}
	for _, id := range ids {
		if err := g.GenerateForInstallation(ctx, id, now); err != nil {
			g.Logger.Error("digest generate failed",
				"err", err, "installation_id", id)
		}
	}
	return nil
}

// GenerateForInstallation produces a digest row for one installation. An
// empty candidate set still writes a row (a "nothing notable this week"
// entry) so the UI can render a consistent state.
func (g *Generator) GenerateForInstallation(ctx context.Context, installationID int64, now time.Time) error {
	periodEnd := now.UTC()
	periodStart := periodEnd.Add(-digestPeriod)

	findingIDs, err := g.Digests.CandidateFindingsForDigest(ctx, installationID, periodStart, periodEnd, topN)
	if err != nil {
		return fmt.Errorf("digests candidate findings: %w", err)
	}
	d, err := g.Digests.Insert(ctx, installationID, periodStart, periodEnd, findingIDs)
	if err != nil {
		return fmt.Errorf("digests insert: %w", err)
	}
	g.Logger.Info("digest generated",
		"installation_id", installationID,
		"digest_id", d.ID,
		"findings", len(findingIDs),
		"period_start", periodStart,
		"period_end", periodEnd,
	)
	if g.Audit != nil {
		_ = g.Audit.Log(ctx, installationID, "", "digest.generated", "",
			map[string]any{
				"digest_id":      d.ID,
				"findings_count": len(findingIDs),
				"period_start":   periodStart.Format(time.RFC3339),
				"period_end":     periodEnd.Format(time.RFC3339),
			})
	}
	return nil
}
