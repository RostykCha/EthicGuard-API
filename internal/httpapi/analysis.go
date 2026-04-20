package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/ethicguard/ethicguard-api/internal/analysis"
	"github.com/ethicguard/ethicguard-api/internal/auth"
	"github.com/ethicguard/ethicguard-api/internal/catalog"
	"github.com/ethicguard/ethicguard-api/internal/jobs"
	"github.com/ethicguard/ethicguard-api/internal/store"
)

// AnalysisHandler serves POST /v1/analysis. It enqueues an analysis job and
// returns 202 with a job id immediately; a worker goroutine runs the actual
// LLM call out of band. Forge resolvers have a ~25s timeout, so synchronous
// analysis is not an option.
//
// Zero-retention: the normalized payload comes in on the request body, is
// handed to the worker over an in-memory channel, and is never written to
// Postgres. The jobs row stores only ids, the issue key, and status.
type AnalysisHandler struct {
	Logger     *slog.Logger
	Projects   *store.Projects
	Jobs       *store.Jobs
	Dispatcher jobs.Dispatcher
}

type enqueueResponse struct {
	JobID string `json:"jobId"`
}

func (h *AnalysisHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	inst := auth.InstallationFromContext(r.Context())
	if inst == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "no installation"})
		return
	}

	var req analysis.AnalysisRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if req.IssueKey == "" || req.Payload.Key == "" || req.ProjectKey == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "issueKey, projectKey, and payload.key are required"})
		return
	}
	if req.Kind == "" {
		req.Kind = "ac_quality"
	}

	projectID, err := h.Projects.UpsertByKey(r.Context(), inst.ID, req.ProjectKey)
	if err != nil {
		h.Logger.Error("projects upsert failed", "err", err, "cloud_id", inst.CloudID)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "project upsert failed"})
		return
	}

	jobID, err := h.Jobs.Enqueue(r.Context(), inst.ID, projectID, req.IssueKey, req.Kind, "")
	if err != nil {
		h.Logger.Error("jobs enqueue failed", "err", err, "cloud_id", inst.CloudID)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "enqueue failed"})
		return
	}

	reqCopy := req
	model := ""
	if req.Escalate {
		model = "heavy"
	}
	dispErr := h.Dispatcher.Dispatch(r.Context(), jobs.Work{
		JobID:          jobID,
		InstallationID: inst.ID,
		Model:          model,
		Request:        &reqCopy,
	})
	if dispErr != nil {
		if failErr := h.Jobs.MarkFailed(context.Background(), jobID, "busy"); failErr != nil {
			h.Logger.Error("failed to mark busy-rejected job", "err", failErr, "job_id", jobID)
		}
		h.Logger.Warn("dispatch rejected (busy)",
			"err", dispErr,
			"cloud_id", inst.CloudID,
			"issue_key", req.IssueKey,
		)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "busy"})
		return
	}

	h.Logger.Info("analysis enqueued",
		"job_id", jobID,
		"cloud_id", inst.CloudID,
		"issue_key", req.IssueKey,
	)
	writeJSON(w, http.StatusAccepted, enqueueResponse{JobID: strconv.FormatInt(jobID, 10)})
}

// AnalysisStatusHandler serves GET /v1/analysis/{jobId}. Tenant-isolated:
// the job id is scoped to the caller's installation.
//
// During `running` status the response includes whatever findings have been
// inserted so far + a rolling summary — this powers the UI's "progressive
// first paint" without SSE.
//
// Projects (optional) enables the confidence-threshold filter from Phase 2
// #6: findings with score < EffectiveThreshold(category) are dropped before
// the summary is computed, so the ribbon never advertises hidden rows.
type AnalysisStatusHandler struct {
	Logger   *slog.Logger
	Jobs     *store.Jobs
	Findings *store.Findings
	Actions  *store.FindingActions
	Projects *store.Projects
	Catalog  *catalog.Catalog
}

type statusFinding struct {
	ID           int64             `json:"id"`
	Category     string            `json:"category"`
	Severity     string            `json:"severity"`
	Score        int               `json:"score"`
	Anchor       store.Anchor      `json:"anchor"`
	MessageKey   string            `json:"messageKey"`
	Params       map[string]string `json:"params,omitempty"`
	Message      string            `json:"message"`
	RationaleTag string            `json:"rationaleTag,omitempty"`
	Action       *actionPayload    `json:"action,omitempty"`
}

type actionPayload struct {
	Action    string `json:"action"`
	Reason    string `json:"reason,omitempty"`
	CreatedAt string `json:"createdAt"`
}

type statusSummary struct {
	Total  int `json:"total"`
	High   int `json:"high"`
	Medium int `json:"medium"`
	Low    int `json:"low"`
	Info   int `json:"info"`
}

type statusResponse struct {
	JobID      string          `json:"jobId"`
	IssueKey   string          `json:"issueKey"`
	Status     string          `json:"status"`
	ErrorCode  string          `json:"errorCode,omitempty"`
	Summary    *statusSummary  `json:"summary,omitempty"`
	Findings   []statusFinding `json:"findings,omitempty"`
	CreatedAt  string          `json:"createdAt"`
	StartedAt  string          `json:"startedAt,omitempty"`
	FinishedAt string          `json:"finishedAt,omitempty"`
}

func (h *AnalysisStatusHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	inst := auth.InstallationFromContext(r.Context())
	if inst == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "no installation"})
		return
	}

	jobIDStr := r.PathValue("jobId")
	jobID, err := strconv.ParseInt(jobIDStr, 10, 64)
	if err != nil || jobID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid jobId"})
		return
	}

	job, err := h.Jobs.GetByIDForInstallation(r.Context(), jobID, inst.ID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}
		h.Logger.Error("jobs get failed", "err", err, "job_id", jobID, "cloud_id", inst.CloudID)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "lookup failed"})
		return
	}

	resp := statusResponse{
		JobID:     strconv.FormatInt(job.ID, 10),
		IssueKey:  job.IssueKey,
		Status:    string(job.Status),
		CreatedAt: job.CreatedAt.UTC().Format("2006-01-02T15:04:05.000Z"),
	}
	if job.StartedAt != nil {
		resp.StartedAt = job.StartedAt.UTC().Format("2006-01-02T15:04:05.000Z")
	}
	if job.FinishedAt != nil {
		resp.FinishedAt = job.FinishedAt.UTC().Format("2006-01-02T15:04:05.000Z")
	}

	switch job.Status {
	case store.JobFailed:
		resp.ErrorCode = job.Error
	case store.JobRunning, store.JobDone:
		// Return findings-so-far even during 'running' — the worker inserts
		// sequentially, so each poll shows a bit more. Summary counts are
		// live-aggregated from the same rows for the ribbon.
		role := parseRole(r.URL.Query().Get("role"))
		// Load the job's project once so the threshold filter can be applied
		// per-category. Errors are logged and fall through to an unfiltered
		// response (fail-open) rather than blocking findings on a lookup hiccup.
		var project *store.Project
		if h.Projects != nil {
			p, perr := h.Projects.GetByID(r.Context(), job.ProjectID)
			if perr == nil {
				project = p
			} else if !errors.Is(perr, store.ErrNotFound) {
				h.Logger.Warn("project threshold lookup failed; filtering disabled",
					"err", perr, "project_id", job.ProjectID)
			}
		}
		if err := h.attachFindings(r.Context(), &resp, job.ID, role, project); err != nil {
			h.Logger.Error("attach findings failed", "err", err, "job_id", job.ID)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "findings lookup failed"})
			return
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

func (h *AnalysisStatusHandler) attachFindings(ctx context.Context, resp *statusResponse, jobID int64, role catalog.Role, project *store.Project) error {
	rows, err := h.Findings.ListByJob(ctx, jobID)
	if err != nil {
		return err
	}
	// Pull the per-finding actions in one query; zero if no rows exist yet.
	var actions map[int64]*store.FindingAction
	if h.Actions != nil {
		actions, err = h.Actions.ListByJob(ctx, jobID)
		if err != nil {
			return err
		}
	}

	summary := statusSummary{}
	resp.Findings = make([]statusFinding, 0, len(rows))
	for _, f := range rows {
		// Apply the confidence threshold filter (Phase 2 #6). Dropped findings
		// don't count toward the ribbon's summary either — the ribbon should
		// reflect what the user actually sees.
		if project != nil && f.Score < project.EffectiveThreshold(f.Category) {
			continue
		}
		msg, err := h.Catalog.Resolve(f.MessageKey, f.Params, role)
		if err != nil {
			h.Logger.Error("catalog resolve failed",
				"err", err, "job_id", jobID, "message_key", f.MessageKey)
			msg = f.MessageKey
		}
		sf := statusFinding{
			ID:           f.ID,
			Category:     f.Category,
			Severity:     f.Severity,
			Score:        f.Score,
			Anchor:       f.Anchor,
			MessageKey:   f.MessageKey,
			Params:       f.Params,
			Message:      msg,
			RationaleTag: f.RationaleTag,
		}
		if a, ok := actions[f.ID]; ok && a != nil {
			sf.Action = &actionPayload{
				Action:    a.Action,
				Reason:    a.Reason,
				CreatedAt: a.CreatedAt.UTC().Format("2006-01-02T15:04:05.000Z"),
			}
		}
		switch f.Severity {
		case "high":
			summary.High++
		case "medium":
			summary.Medium++
		case "low":
			summary.Low++
		case "info":
			summary.Info++
		}
		summary.Total++
		resp.Findings = append(resp.Findings, sf)
	}
	resp.Summary = &summary
	return nil
}

// parseRole maps the `role` query param to a catalog.Role. Unknown values
// fall back to RoleDefault so a bad client never breaks the response.
func parseRole(q string) catalog.Role {
	switch q {
	case "pm":
		return catalog.RolePM
	case "qa":
		return catalog.RoleQA
	case "dev":
		return catalog.RoleDev
	default:
		return catalog.RoleDefault
	}
}
