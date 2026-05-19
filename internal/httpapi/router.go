// Package httpapi owns every HTTP route EthicGuard-API exposes and the
// handlers behind them. Public routes (health, version, lifecycle webhook)
// sit outside the auth middleware; `/v1/*` feature routes sit behind a
// Forge-JWT verifier from internal/auth.
//
// Invariant: handlers never reach into Postgres directly. They depend on
// small interfaces (ProjectsRepo, JobsRepo, FindingsRepo, AuditsRepo,
// PayloadEnqueuer) defined near the consumer; main.go wires the concrete
// implementations from internal/store at startup.
package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/ethicguard/ethicguard-api/internal/auth"
	"github.com/ethicguard/ethicguard-api/internal/store"
)

// Deps bundles what the router needs to wire handlers and middleware. The
// LLM is wired into the worker pool now (not the analysis handler), so it
// isn't carried here.
type Deps struct {
	Logger        *slog.Logger
	Installations *store.Installations
	Projects      ProjectsRepoFull
	Audits        AuditsRepo
	Jobs          JobsRepo
	Findings      FindingsRepo
	// FindingsWriter is the write side of the findings repo, used by the
	// /v1/analysis/results endpoint. Kept separate from FindingsRepo so the
	// read-only GET handlers can't accidentally insert.
	FindingsWriter  PersistedFindingsRepo
	Metrics         MetricsRepo
	Queue           PayloadEnqueuer
	InstallerSecret string
	JWTAudience     string
}

// NewRouter builds the http handler tree. Public routes (health, version,
// lifecycle) sit outside the auth middleware; /v1 feature routes live
// behind the Forge-JWT-authenticated middleware.
func NewRouter(d Deps) http.Handler {
	mux := http.NewServeMux()

	// Public routes.
	mux.HandleFunc("GET /v1/health", handleHealth)
	mux.HandleFunc("GET /v1/version", handleVersion)

	// Lifecycle webhook — authed by pre-shared installer secret, not the
	// per-install JWT middleware (chicken-and-egg: before install there's no
	// per-install secret yet).
	if d.Installations != nil {
		lifecycle := &LifecycleHandler{
			Logger:          d.Logger,
			Installations:   d.Installations,
			InstallerSecret: d.InstallerSecret,
		}
		mux.Handle("POST /v1/installations/lifecycle", lifecycle)
	}

	// Feature routes behind auth middleware.
	authed := http.NewServeMux()
	if d.Jobs != nil && d.Projects != nil && d.Queue != nil {
		analysisH := &AnalysisHandler{
			Logger:   d.Logger,
			Jobs:     d.Jobs,
			Projects: d.Projects,
			Queue:    d.Queue,
		}
		authed.Handle("POST /v1/analysis", analysisH)
	}
	// POST /v1/analysis/results — Rovo-stamped verdicts persisted by the
	// Forge stampLabel resolver. Requires the concrete *store.Jobs (for
	// RecordCompleted) and the findings repo. The async-path AnalysisHandler
	// only knows about Enqueue / GetByID, so we wire this with the same
	// d.Jobs interface, narrowed to ResultsJobsRepo at the field.
	if results, ok := d.Jobs.(ResultsJobsRepo); ok && d.Projects != nil && d.FindingsWriter != nil {
		resultsH := &AnalysisResultsHandler{
			Logger:   d.Logger,
			Jobs:     results,
			Projects: d.Projects,
			Findings: d.FindingsWriter,
		}
		authed.Handle("POST /v1/analysis/results", resultsH)
	}
	if d.Jobs != nil && d.Findings != nil {
		jobsH := &JobsHandler{Logger: d.Logger, Jobs: d.Jobs, Findings: d.Findings}
		authed.Handle("GET /v1/analysis/{jobId}", jobsH)
		// The latest-issue route reads the newest job for an issue. d.Jobs
		// (the concrete *store.Jobs) implements both interfaces — see jobs.go.
		if latestLookup, ok := d.Jobs.(LatestJobLookup); ok {
			latestH := &LatestIssueHandler{Logger: d.Logger, Jobs: latestLookup, JobByID: d.Jobs, Findings: d.Findings}
			authed.Handle("GET /v1/issues/{issueKey}/latest", latestH)
		}
	}
	if d.Projects != nil {
		projectsH := &ProjectsHandler{Logger: d.Logger, Projects: d.Projects, Audits: d.Audits}
		// Both GET and PUT share the path; the handler routes by method so we
		// keep the path-value extraction (`{projectKey}`) in one place.
		authed.Handle("GET /v1/projects/{projectKey}/config", projectsH)
		authed.Handle("PUT /v1/projects/{projectKey}/config", projectsH)
	}

	if d.Metrics != nil {
		metricsH := &MetricsHandler{Logger: d.Logger, Metrics: d.Metrics}
		authed.Handle("GET /v1/projects/{projectKey}/metrics", metricsH)
	}

	if d.Installations != nil {
		mw := auth.Middleware(d.Logger, d.Installations, d.JWTAudience)
		mux.Handle("/v1/", mw(authed))
	}

	return withRequestLogging(d.Logger, mux)
}
