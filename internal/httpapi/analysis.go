package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"

	"github.com/ethicguard/ethicguard-api/internal/analysis"
	"github.com/ethicguard/ethicguard-api/internal/auth"
	"github.com/ethicguard/ethicguard-api/internal/jobs"
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

// PayloadEnqueuer is what the handler uses to hand the in-memory job entry
// off to the worker pool. Defined as an interface so tests can fake.
type PayloadEnqueuer interface {
	Put(jobID int64, e jobs.Entry)
}

// AnalysisHandler serves POST /v1/analysis — enqueue an AC analysis job.
// The Forge UI (or trigger) sends the normalized issue payload; we register
// a job in Postgres, hand the payload + per-project run options to the
// worker via the in-memory bus, and return the jobId immediately. Polling
// happens via GET /v1/analysis/{id}.
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
		unauthorized(w, "no installation")
		return
	}

	var req analysis.AnalysisRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		badRequest(w, "invalid json")
		return
	}
	if req.IssueKey == "" || req.Payload.Key == "" || req.ProjectKey == "" {
		badRequest(w, "issueKey, projectKey and payload.key are required")
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
		forbidden(w, "issue_type_out_of_scope",
			"project not configured — admin must select issue types in EthicGuard project settings")
		return
	}
	if !cfg.AgentEnabled {
		forbidden(w, "agent_disabled",
			"EthicGuard AC Reviewer is turned off for this project. An admin can re-enable it under Project settings → EthicGuard.")
		return
	}
	if !slices.Contains(cfg.TestedIssueTypes, req.Payload.IssueTypeID) {
		forbidden(w, "issue_type_out_of_scope",
			"this issue type is not enabled for EthicGuard analysis in project settings")
		return
	}

	projectID, err := h.Projects.Upsert(r.Context(), inst.ID, req.ProjectKey)
	if err != nil {
		internalErr(w, h.Logger, err, "project resolve failed",
			"cloud_id", inst.CloudID, "project_key", req.ProjectKey)
		return
	}

	// Pull the actor account id from a header so JWT can stay slim — it
	// also lets the Forge resolver inject the calling user without a token
	// re-mint. Empty is fine (system-driven trigger).
	actor := r.Header.Get("X-EthicGuard-Actor")
	jobID, err := h.Jobs.Enqueue(r.Context(), inst.ID, projectID, req.IssueKey, req.Kind, actor)
	if err != nil {
		internalErr(w, h.Logger, err, "enqueue failed",
			"cloud_id", inst.CloudID, "issue_key", req.IssueKey)
		return
	}

	h.Queue.Put(jobID, jobs.Entry{
		Payload: req.Payload,
		Options: analysis.RunOptions{
			SeverityThreshold: cfg.AgentSeverityThreshold,
			PromptAddendum:    cfg.AgentPromptAddendum,
		},
	})
	h.Logger.Info("analysis enqueued",
		"cloud_id", inst.CloudID,
		"issue_key", req.IssueKey,
		"project_key", req.ProjectKey,
		"job_id", jobID,
		"severity_threshold", cfg.AgentSeverityThreshold,
	)
	writeJSON(w, http.StatusAccepted, enqueueResponse{JobID: jobID, Status: string(store.JobQueued)})
}

// ResultsJobsRepo is the narrow jobs surface the verdict-persistence handler
// needs. Separate from JobsRepo because RecordCompleted only exists on the
// concrete *store.Jobs (Enqueue + MarkDone are owned by the async worker
// path and don't apply here).
type ResultsJobsRepo interface {
	RecordCompleted(ctx context.Context, installationID, projectID int64, issueKey, kind, resultLabel string) (int64, error)
}

// PersistedFindingsRepo is the write side of internal/store/findings.go.
type PersistedFindingsRepo interface {
	InsertBatch(ctx context.Context, jobID int64, findings []store.PersistedFinding) error
}

// AnalysisResultsHandler serves POST /v1/analysis/results — accepts a
// pre-computed analysis verdict from the Forge side (Rovo decided it; our
// stamp-label resolver POSTs it here). Inserts a `done` job row + findings
// in one logical operation so the existing GET /v1/issues/{key}/latest and
// GET /v1/analysis/{id} endpoints can render it.
//
// Zero-retention guard at the boundary: any finding carrying a free-text
// `message` field is rejected (400) before it ever reaches the store. The
// canonical rule lives in CLAUDE.md "The non-negotiable rule"; this is the
// last line of defense if a future caller forgets it.
type AnalysisResultsHandler struct {
	Logger   *slog.Logger
	Jobs     ResultsJobsRepo
	Projects ProjectsRepoFull
	Findings PersistedFindingsRepo
}

type analysisResultsFinding struct {
	Category   string         `json:"category"`
	Severity   string         `json:"severity"`
	Score      int            `json:"score"`
	Anchor     map[string]any `json:"anchor"`
	MessageKey string         `json:"messageKey"`
	// Message MUST be empty on the wire — zero-retention. If a caller sends
	// it, we 400.
	Message string `json:"message,omitempty"`
}

type analysisResultsRequest struct {
	IssueKey   string                   `json:"issueKey"`
	ProjectKey string                   `json:"projectKey"`
	Kind       string                   `json:"kind"`
	Label      string                   `json:"label"`
	Findings   []analysisResultsFinding `json:"findings"`
}

func (h *AnalysisResultsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	inst := auth.InstallationFromContext(r.Context())
	if inst == nil {
		unauthorized(w, "no installation")
		return
	}

	var req analysisResultsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		badRequest(w, "invalid json")
		return
	}
	if err := validateAnalysisResultsRequest(&req); err != nil {
		badRequest(w, err.Error())
		return
	}

	projectID, err := h.Projects.Upsert(r.Context(), inst.ID, req.ProjectKey)
	if err != nil {
		internalErr(w, h.Logger, err, "project resolve failed",
			"cloud_id", inst.CloudID, "project_key", req.ProjectKey)
		return
	}

	jobID, err := h.Jobs.RecordCompleted(r.Context(), inst.ID, projectID, req.IssueKey, req.Kind, req.Label)
	if err != nil {
		internalErr(w, h.Logger, err, "record completed failed",
			"cloud_id", inst.CloudID, "issue_key", req.IssueKey)
		return
	}

	persisted := make([]store.PersistedFinding, 0, len(req.Findings))
	for _, f := range req.Findings {
		persisted = append(persisted, store.PersistedFinding{
			JobID:      jobID,
			Category:   f.Category,
			Severity:   f.Severity,
			Score:      f.Score,
			Anchor:     f.Anchor,
			MessageKey: f.MessageKey,
		})
	}
	if err := h.Findings.InsertBatch(r.Context(), jobID, persisted); err != nil {
		// Job row is already in 'done' state at this point. We log and
		// surface 500; the caller can re-submit if it wants a clean record.
		// The previous orphan stays in the table — operationally harmless
		// (GET endpoints will show it with no findings).
		internalErr(w, h.Logger, err, "persist findings failed",
			"cloud_id", inst.CloudID, "job_id", jobID)
		return
	}

	h.Logger.Info("analysis result recorded",
		"cloud_id", inst.CloudID,
		"issue_key", req.IssueKey,
		"project_key", req.ProjectKey,
		"job_id", jobID,
		"label", req.Label,
		"findings", len(req.Findings),
	)
	writeJSON(w, http.StatusCreated, enqueueResponse{JobID: jobID, Status: string(store.JobDone)})
}
