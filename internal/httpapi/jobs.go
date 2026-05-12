package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/ethicguard/ethicguard-api/internal/analysis"
	"github.com/ethicguard/ethicguard-api/internal/auth"
	"github.com/ethicguard/ethicguard-api/internal/store"
)

// LatestJobLookup extends JobsRepo with the "newest job for this issue" query
// that powers the issue panel.
type LatestJobLookup interface {
	LatestForIssue(ctx context.Context, installationID int64, issueKey string) (*store.Job, error)
}

// JobsHandler serves GET /v1/analysis/{jobId} — the polling endpoint the
// Forge UI hits after enqueuing an analysis. Returns status, the label
// decision (when finished), and findings rendered through the message
// catalog (zero-retention: db carries the key, the UI gets the text).
type JobsHandler struct {
	Logger   *slog.Logger
	Jobs     JobsRepo
	Findings FindingsRepo
}

// LatestIssueHandler serves GET /v1/issues/{issueKey}/latest — used by the
// issue panel to render the most recent analysis without needing to track
// jobId in Jira issue properties.
type LatestIssueHandler struct {
	Logger   *slog.Logger
	Jobs     LatestJobLookup
	JobByID  JobsRepo
	Findings FindingsRepo
}

type jobFinding struct {
	Category   string `json:"category"`
	Severity   string `json:"severity"`
	Score      int    `json:"score"`
	Anchor     any    `json:"anchor"`
	MessageKey string `json:"messageKey"`
	Message    string `json:"message"`
}

type jobResponse struct {
	JobID       int64        `json:"jobId"`
	Status      string       `json:"status"`
	IssueKey    string       `json:"issueKey"`
	ResultLabel string       `json:"resultLabel,omitempty"`
	Error       string       `json:"error,omitempty"`
	Findings    []jobFinding `json:"findings"`
}

func (h *JobsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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

	job, err := h.Jobs.GetByID(r.Context(), inst.ID, jobID)
	if err != nil {
		if store.IsNotFound(err) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "job not found"})
			return
		}
		h.Logger.Error("job lookup failed", "err", err, "job_id", jobID)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "job lookup failed"})
		return
	}

	resp := jobResponse{
		JobID:       job.ID,
		Status:      string(job.Status),
		IssueKey:    job.IssueKey,
		ResultLabel: job.ResultLabel,
		Error:       job.Error,
		Findings:    []jobFinding{},
	}

	// Only fetch findings when the job actually finished — saves a round-trip
	// while it's still queued or running.
	if job.Status == store.JobDone {
		findings, err := h.Findings.ListByJob(r.Context(), job.ID)
		if err != nil {
			h.Logger.Error("findings list failed", "err", err, "job_id", jobID)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "findings lookup failed"})
			return
		}
		for _, f := range findings {
			resp.Findings = append(resp.Findings, jobFinding{
				Category:   f.Category,
				Severity:   f.Severity,
				Score:      f.Score,
				Anchor:     f.Anchor,
				MessageKey: f.MessageKey,
				Message:    analysis.ResolveMessage(f.MessageKey),
			})
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

// ServeHTTP for LatestIssueHandler — looks up the newest job for an issue and
// returns the same shape as GET /v1/analysis/{jobId}. 404 when no analysis
// has ever run for the issue.
func (h *LatestIssueHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	inst := auth.InstallationFromContext(r.Context())
	if inst == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "no installation"})
		return
	}
	issueKey := r.PathValue("issueKey")
	if issueKey == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "issueKey required"})
		return
	}

	job, err := h.Jobs.LatestForIssue(r.Context(), inst.ID, issueKey)
	if err != nil {
		if store.IsNotFound(err) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "no analysis for issue"})
			return
		}
		h.Logger.Error("latest job lookup failed", "err", err, "issue_key", issueKey)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "latest job lookup failed"})
		return
	}

	resp := jobResponse{
		JobID:       job.ID,
		Status:      string(job.Status),
		IssueKey:    job.IssueKey,
		ResultLabel: job.ResultLabel,
		Error:       job.Error,
		Findings:    []jobFinding{},
	}
	if job.Status == store.JobDone {
		findings, err := h.Findings.ListByJob(r.Context(), job.ID)
		if err != nil {
			h.Logger.Error("findings list failed", "err", err, "job_id", job.ID)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "findings lookup failed"})
			return
		}
		for _, f := range findings {
			resp.Findings = append(resp.Findings, jobFinding{
				Category:   f.Category,
				Severity:   f.Severity,
				Score:      f.Score,
				Anchor:     f.Anchor,
				MessageKey: f.MessageKey,
				Message:    analysis.ResolveMessage(f.MessageKey),
			})
		}
	}
	writeJSON(w, http.StatusOK, resp)
}
