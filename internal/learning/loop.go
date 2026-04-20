// Package learning holds the dismissal-driven threshold-tuning loop
// (Phase 2 #7). It reads aggregate action data from `finding_actions`,
// computes per-(project, category) dismissal rates over the last 30 days,
// and writes back threshold overrides on `projects.threshold_overrides`.
//
// Zero-retention backbone: the loop reads only enum columns (action,
// category) and numeric counts. It writes only integer thresholds. No
// user-authored text is ever touched.
package learning

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/ethicguard/ethicguard-api/internal/store"
)

const (
	windowDays            = 30
	minSampleSize         = 10
	dismissalRateCutoff   = 0.6
	thresholdBumpPerRound = 10
	thresholdCeiling      = 80
)

type projectCategoryStat struct {
	projectID int64
	category  string
	total     int
	dismissed int
}

// RunOnce executes a single pass of the learning loop. Safe to call from a
// ticker; idempotent per invocation (the rule is monotonic — threshold can
// only rise, capped at ceiling).
func RunOnce(ctx context.Context, logger *slog.Logger, projectsRepo *store.Projects, audit *store.Audit, st *store.Store) error {
	logger = logger.With("component", "learning")
	stats, err := collectStats(ctx, st)
	if err != nil {
		return fmt.Errorf("learning collect: %w", err)
	}
	if len(stats) == 0 {
		logger.Info("no action data in window", "days", windowDays)
		return nil
	}

	// Group by project so we only call SetOverrides once per project row.
	byProject := map[int64][]projectCategoryStat{}
	for _, s := range stats {
		byProject[s.projectID] = append(byProject[s.projectID], s)
	}

	for projectID, rows := range byProject {
		p, err := projectsRepo.GetByID(ctx, projectID)
		if err != nil {
			logger.Warn("project lookup failed; skipping", "err", err, "project_id", projectID)
			continue
		}
		overrides := cloneMap(p.ThresholdOverrides)
		changed := false
		for _, r := range rows {
			if r.total < minSampleSize {
				continue
			}
			rate := float64(r.dismissed) / float64(r.total)
			if rate < dismissalRateCutoff {
				continue
			}
			current := overrides[r.category]
			if current >= thresholdCeiling {
				continue
			}
			next := current + thresholdBumpPerRound
			if next > thresholdCeiling {
				next = thresholdCeiling
			}
			overrides[r.category] = next
			changed = true
			logger.Info("threshold bumped",
				"project_id", projectID,
				"category", r.category,
				"from", current,
				"to", next,
				"samples", r.total,
				"dismissal_rate", rate,
			)
			if audit != nil {
				_ = audit.Log(ctx, p.InstallationID, "", "learning.threshold_adjusted",
					p.ProjectKey, map[string]any{
						"category":   r.category,
						"from":       current,
						"to":         next,
						"samples":    r.total,
						"dismissals": r.dismissed,
					})
			}
		}
		if changed {
			if err := projectsRepo.SetOverrides(ctx, projectID, overrides); err != nil {
				logger.Error("set overrides failed", "err", err, "project_id", projectID)
			}
		}
	}
	return nil
}

// collectStats pulls per-(project, category) accept/dismiss counts over the
// last windowDays. One query for the whole installation set — the total
// row count is bounded by #projects × #categories, which is small.
func collectStats(ctx context.Context, st *store.Store) ([]projectCategoryStat, error) {
	const q = `
		SELECT j.project_id,
		       f.category,
		       COUNT(*) FILTER (WHERE fa.action = 'dismiss'),
		       COUNT(*)
		FROM findings f
		JOIN jobs j ON j.id = f.job_id
		JOIN finding_actions fa ON fa.finding_id = f.id
		WHERE fa.created_at > NOW() - ($1 || ' days')::interval
		GROUP BY j.project_id, f.category
	`
	rows, err := st.Pool.Query(ctx, q, fmt.Sprintf("%d", windowDays))
	if err != nil {
		return nil, fmt.Errorf("learning query: %w", err)
	}
	defer rows.Close()
	var out []projectCategoryStat
	for rows.Next() {
		var s projectCategoryStat
		if err := rows.Scan(&s.projectID, &s.category, &s.dismissed, &s.total); err != nil {
			return nil, fmt.Errorf("learning scan: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func cloneMap(m map[string]int) map[string]int {
	out := make(map[string]int, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
