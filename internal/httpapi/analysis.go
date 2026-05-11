package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"

	"github.com/ethicguard/ethicguard-api/internal/analysis"
	"github.com/ethicguard/ethicguard-api/internal/auth"
	"github.com/ethicguard/ethicguard-api/internal/store"
)

// JobsRepo is the jobs-storage surface the analysis handler needs.
type JobsRepo interface {
	Enqueue(ctx context.Context, installationID, projectID int64, issueKey, kind, requestedBy string) (int64, error)
	GetByID(ctx context.Context, installationID, jobID int64) (*store.Job, error)
}

// FindingsRepo is what the GET-job handler needs to render findings.
type FindingsRepo interface {
	ListByJob(ctx context.Context, jobID int64) ([]store.PersistedFinding, error)
}

// ProjectsRepoFull extends ProjectsRepo with the Upsert needed to attach a
// job to a project. We keep it as an interface here so the handler stays
// testable without a real Postgres.
type ProjectsRepoFull interface {
	ProjectsRepo
	Upsert(ctx context.Context, installationID int64, projectKey string) (int64, error)
}

// PayloadEnqueuer is what the handler uses to hand the in-memory issue
// payload off to the worker pool. Defined as an interface so tests can fake.
type PayloadEnqueuer interface {
	Put(jobID int64, p analysis.IssuePayload)
}

// AnalysisHandler serves POST /v1/analysis — enqueue an AC analysis job.
// The Forge UI (or trigger) sends the normalized issue payload; we register
// a job in Postgres, hand the payload to the worker via the in-memory bus,
// and return the jobId immediately. Polling happens via GET /v1/analysis/{id}.
//
// Zero-retention: the payload is held only in process memory until the
// worker takes it; Postgres only carries ids, kind, status, label, anchors.
type AnalysisHandler struct {
	Logger   *slog.Logger
	Jobs     JobsRepo
	Projects ProjectsRepoFull
	Queue    PayloadEnqueuer
}

type enqueueResponse struct {
	JobID  int64  `json:"jobId"`
	Status string `json:"status"`
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
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "issueKey, projectKey and payload.key are required"})
		return
	}
	if req.Kind == "" {
		req.Kind = "ac_quality"
	}

	// Scope gate: reject when the issue type isn't in the project's
	// tested_issue_types list. Empty list = "no types configured" = reject
	// everything; admin must opt-in before analyses run.
	cfg, err := h.Projects.GetConfig(r.Context(), inst.ID, req.ProjectKey)
	if err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": "issue_type_out_of_scope",
			"hint":  "project not configured — admin must select issue types in EthicGuard project settings",
		})
		return
	}
	if !slices.Contains(cfg.TestedIssueTypes, req.Payload.IssueTypeID) {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": "issue_type_out_of_scope",
			"hint":  "this issue type is not enabled for EthicGuard analysis in project settings",
		})
		return
	}

	projectID, err := h.Projects.Upsert(r.Context(), inst.ID, req.ProjectKey)
	if err != nil {
		h.Logger.Error("project upsert failed", "err", err, "cloud_id", inst.CloudID, "project_key", req.ProjectKey)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "project resolve failed"})
		return
	}

	// Pull the actor account id from a header so JWT can stay slim — it
	// also lets the Forge resolver inject the calling user without a token
	// re-mint. Empty is fine (system-driven trigger).
	actor := r.Header.Get("X-EthicGuard-Actor")
	jobID, err := h.Jobs.Enqueue(r.Context(), inst.ID, projectID, req.IssueKey, req.Kind, actor)
	if err != nil {
		h.Logger.Error("enqueue failed", "err", err, "cloud_id", inst.CloudID, "issue_key", req.IssueKey)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "enqueue failed"})
		return
	}

	h.Queue.Put(jobID, req.Payload)
	h.Logger.Info("analysis enqueued",
		"cloud_id", inst.CloudID,
		"issue_key", req.IssueKey,
		"project_key", req.ProjectKey,
		"job_id", jobID,
	)
	writeJSON(w, http.StatusAccepted, enqueueResponse{JobID: jobID, Status: string(store.JobQueued)})
}
